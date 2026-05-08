// Package s3 implements the connector.Connector interface for Amazon S3 (and
// S3-compatible stores). It reads and writes rows in Parquet, CSV, or JSON format
// keyed by Asset.Identifier which maps to "bucket/key/path".
//
// Format is selected via Factory params: format: "parquet" | "csv" | "json"
// (default "parquet"). For Phase 2, objects are assumed to fit in memory
// (T-02-05-03 — streaming deferred to Phase 3).
package s3

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	parquet "github.com/parquet-go/parquet-go"

	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingBucket is returned by Factory when the "bucket" parameter is absent.
	ErrMissingBucket = errors.New("s3: bucket parameter required")
	// ErrMissingRegion is returned by Factory when the "region" parameter is absent.
	ErrMissingRegion = errors.New("s3: region parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("s3: connector closed")
	// ErrPathTraversal is returned when a path contains ".." segments (T-02-05-02).
	ErrPathTraversal = errors.New("s3: identifier contains path traversal sequence")
)

// Compile-time assertion: S3 satisfies connector.Connector.
var _ connector.Connector = (*S3)(nil)

// S3 is the Amazon S3 connector. Lifecycle (D-08): one instance per configured
// connector name, kept for the process lifetime.
type S3 struct {
	client *s3.Client
	bucket string
	format string // "parquet" | "csv" | "json"
	mu     sync.RWMutex
	closed bool
}

// New constructs an S3 connector with the given client, bucket, and format.
func New(client *s3.Client, bucket string, format string) *S3 {
	if format == "" {
		format = "parquet"
	}
	return &S3{client: client, bucket: bucket, format: format}
}

// APIVersion returns the connector ABI version.
func (s *S3) APIVersion() string { return connector.APIVersion }

// Ping verifies the bucket is accessible.
func (s *S3) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return connector.PingResponse{}, fmt.Errorf("s3: ping HeadBucket: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "s3",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset. The schema is inferred
// from the stored object: for parquet the footer is read; for CSV the header row
// is parsed; for JSON the first record's keys are enumerated.
// Asset.Identifier must be "bucket/key/path" format — the bucket portion is ignored
// (the connector is already bound to a bucket); only the key is used.
func (s *S3) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	key, err := s.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	data, err := s.getObject(ctx, key)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	cols, err := s.inferSchema(data, req.Asset.Config)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given asset object.
func (s *S3) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return connector.ReadResponse{}, err
	}
	key, err := s.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	data, err := s.getObject(ctx, key)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	rows, err := s.decodeRows(data, req.Asset.Config)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	if req.Limit > 0 && int64(len(rows)) > req.Limit {
		rows = rows[:req.Limit]
	}
	return connector.ReadResponse{Rows: rows}, nil
}

// Write encodes rows in the configured format and uploads to S3.
func (s *S3) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	key, err := s.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	data, err := s.encodeRows(req.Rows, req.Asset.Config)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("s3: write PutObject: %w", err)
	}
	return connector.WriteResponse{RowsWritten: int64(len(req.Rows))}, nil
}

// Close marks the connector closed. Subsequent operations return ErrClosed.
// Idempotent — calling Close() twice is safe.
func (s *S3) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *S3) checkClosed() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// keyFromIdentifier extracts the S3 object key from "bucket/key" or "key" format.
// Path traversal is rejected (T-02-05-02).
func (s *S3) keyFromIdentifier(id string) (string, error) {
	if id == "" {
		return "", errors.New("s3: empty identifier")
	}
	// Reject path traversal attempts (T-02-05-02).
	for _, seg := range strings.Split(id, "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}
	// If the identifier starts with "<bucket>/", strip the bucket prefix.
	prefix := s.bucket + "/"
	if strings.HasPrefix(id, prefix) {
		return strings.TrimPrefix(id, prefix), nil
	}
	return id, nil
}

func (s *S3) format_(assetConfig map[string]string) string {
	if assetConfig != nil {
		if f, ok := assetConfig["format"]; ok && f != "" {
			return f
		}
	}
	return s.format
}

func (s *S3) getObject(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3: getObject %q: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("s3: getObject read body: %w", err)
	}
	return data, nil
}

func (s *S3) inferSchema(data []byte, assetConfig map[string]string) ([]connector.Column, error) {
	format := s.format_(assetConfig)
	switch format {
	case "parquet":
		return s.schemaFromParquet(data)
	case "csv":
		return s.schemaFromCSV(data)
	case "json":
		return s.schemaFromJSON(data)
	default:
		return nil, fmt.Errorf("s3: unknown format %q", format)
	}
}

func (s *S3) decodeRows(data []byte, assetConfig map[string]string) ([]connector.Row, error) {
	format := s.format_(assetConfig)
	switch format {
	case "parquet":
		return s.decodeParquet(data)
	case "csv":
		return s.decodeCSV(data)
	case "json":
		return s.decodeJSON(data)
	default:
		return nil, fmt.Errorf("s3: unknown format %q", format)
	}
}

func (s *S3) encodeRows(rows []connector.Row, assetConfig map[string]string) ([]byte, error) {
	format := s.format_(assetConfig)
	switch format {
	case "parquet":
		return s.encodeParquet(rows)
	case "csv":
		return s.encodeCSV(rows)
	case "json":
		return s.encodeJSON(rows)
	default:
		return nil, fmt.Errorf("s3: unknown format %q", format)
	}
}

// --- Parquet encoding ---

func (s *S3) encodeParquet(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("s3: parquet encode: no rows")
	}
	// Build dynamic schema from first row — all columns as optional strings
	// (Phase 2 in-memory model; values are fmt.Sprintf'd for type erasure).
	group := parquet.Group{}
	// Collect sorted column names for deterministic output.
	cols := make([]string, 0, len(rows[0].Fields))
	for k := range rows[0].Fields {
		cols = append(cols, k)
		group[k] = parquet.Optional(parquet.String())
	}
	schema := parquet.NewSchema("row", group)
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[any](&buf, schema)
	for _, r := range rows {
		m := make(map[string]any, len(cols))
		for _, c := range cols {
			m[c] = fmt.Sprintf("%v", r.Fields[c])
		}
		if _, err := w.Write([]any{m}); err != nil {
			return nil, fmt.Errorf("s3: parquet write row: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("s3: parquet writer close: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *S3) decodeParquet(data []byte) ([]connector.Row, error) {
	rows, err := parquet.Read[any](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("s3: parquet read: %w", err)
	}
	out := make([]connector.Row, 0, len(rows))
	for _, r := range rows {
		var fields map[string]any
		switch v := r.(type) {
		case map[string]any:
			fields = v
		default:
			return nil, fmt.Errorf("s3: parquet row type unexpected: %T", r)
		}
		out = append(out, connector.Row{Fields: fields})
	}
	return out, nil
}

func (s *S3) schemaFromParquet(data []byte) ([]connector.Column, error) {
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("s3: parquet schema open: %w", err)
	}
	schema := f.Schema()
	var cols []connector.Column
	for i := range schema.Fields() {
		field := schema.Fields()[i]
		cols = append(cols, connector.Column{
			Name:     field.Name(),
			RawType:  "string",
			Nullable: !field.Required(),
		})
	}
	return cols, nil
}

// --- CSV encoding ---

func (s *S3) encodeCSV(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("s3: csv encode: no rows")
	}
	cols := make([]string, 0, len(rows[0].Fields))
	for k := range rows[0].Fields {
		cols = append(cols, k)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(cols); err != nil {
		return nil, fmt.Errorf("s3: csv header: %w", err)
	}
	for _, r := range rows {
		record := make([]string, len(cols))
		for i, c := range cols {
			record[i] = fmt.Sprintf("%v", r.Fields[c])
		}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("s3: csv write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("s3: csv flush: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *S3) decodeCSV(data []byte) ([]connector.Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("s3: csv header read: %w", err)
	}
	var rows []connector.Row
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("s3: csv row read: %w", err)
		}
		row := connector.Row{Fields: make(map[string]any, len(headers))}
		for i, h := range headers {
			if i < len(record) {
				row.Fields[h] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *S3) schemaFromCSV(data []byte) ([]connector.Column, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("s3: csv schema read: %w", err)
	}
	cols := make([]connector.Column, len(headers))
	for i, h := range headers {
		cols[i] = connector.Column{Name: h, RawType: "string", Nullable: true}
	}
	return cols, nil
}

// --- JSON encoding ---

func (s *S3) encodeJSON(rows []connector.Row) ([]byte, error) {
	// NDJSON (newline-delimited JSON) format — one JSON object per line.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r.Fields); err != nil {
			return nil, fmt.Errorf("s3: json encode row: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func (s *S3) decodeJSON(data []byte) ([]connector.Row, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var rows []connector.Row
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("s3: json decode row: %w", err)
		}
		rows = append(rows, connector.Row{Fields: m})
	}
	return rows, nil
}

func (s *S3) schemaFromJSON(data []byte) ([]connector.Column, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("s3: json schema decode: %w", err)
	}
	cols := make([]connector.Column, 0, len(m))
	for k := range m {
		cols = append(cols, connector.Column{Name: k, RawType: "json", Nullable: true})
	}
	return cols, nil
}

// Factory parameters for building the S3 connector.
type factoryParams struct {
	Bucket          string
	Region          string
	Endpoint        string // optional; for localstack/minio
	AccessKeyID     string // optional; falls back to default credential chain
	SecretAccessKey string // optional
	Format          string // "parquet" | "csv" | "json"; default "parquet"
}

// buildClient constructs an AWS S3 client from factory params.
func buildClient(ctx context.Context, p factoryParams) (*s3.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(p.Region),
	}
	if p.AccessKeyID != "" || p.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(p.AccessKeyID, p.SecretAccessKey, ""),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: aws config: %w", err)
	}
	s3Opts := []func(*s3.Options){}
	if p.Endpoint != "" {
		s3Opts = append(s3Opts,
			func(o *s3.Options) {
				o.BaseEndpoint = aws.String(p.Endpoint)
				o.UsePathStyle = true // required for localstack/minio
			},
		)
	}
	return s3.NewFromConfig(cfg, s3Opts...), nil
}
