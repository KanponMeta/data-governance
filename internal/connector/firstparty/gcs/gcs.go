// Package gcs implements the connector.Connector interface for Google Cloud Storage,
// reading and writing rows in Parquet, CSV, or JSON format keyed by Asset.Identifier
// which maps to "bucket/object-key".
//
// Format is selected via Factory params: format: "parquet" | "csv" | "json"
// (default "parquet"). For Phase 2, objects are assumed to fit in memory
// (T-02-05-03 — streaming deferred to Phase 3).
// Mirrors the S3 connector (object-store archetype) for the Google Cloud SDK.
package gcs

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

	gcstorage "cloud.google.com/go/storage"
	parquet "github.com/parquet-go/parquet-go"
	"google.golang.org/api/option"

	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingBucket is returned by Factory when the "bucket" parameter is absent.
	ErrMissingBucket = errors.New("gcs: bucket parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("gcs: connector closed")
	// ErrPathTraversal is returned when a path contains ".." segments (T-02-05-02).
	ErrPathTraversal = errors.New("gcs: identifier contains path traversal sequence")
)

// Compile-time assertion: GCS satisfies connector.Connector.
var _ connector.Connector = (*GCS)(nil)

// GCS is the Google Cloud Storage connector. Lifecycle (D-08): one instance per
// configured connector name, kept for the process lifetime.
type GCS struct {
	client *gcstorage.Client
	bucket string
	format string // "parquet" | "csv" | "json"
	mu     sync.RWMutex
	closed bool
}

// New constructs a GCS connector with the provided client, bucket, and format.
func New(client *gcstorage.Client, bucket string, format string) *GCS {
	if format == "" {
		format = "parquet"
	}
	return &GCS{client: client, bucket: bucket, format: format}
}

// APIVersion returns the connector ABI version.
func (g *GCS) APIVersion() string { return connector.APIVersion }

// Ping verifies the bucket is accessible via Attrs.
func (g *GCS) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := g.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	_, err := g.client.Bucket(g.bucket).Attrs(ctx)
	if err != nil {
		return connector.PingResponse{}, fmt.Errorf("gcs: ping Attrs: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "gcs",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset. The schema is inferred
// from the stored object: for parquet the footer is read; for CSV the header row
// is parsed; for JSON the first record's keys are enumerated.
// Asset.Identifier must be "bucket/object-key" or just "object-key".
func (g *GCS) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := g.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	key, err := g.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	data, err := g.getObject(ctx, key)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	cols, err := g.inferSchema(data, req.Asset.Config)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given GCS object.
func (g *GCS) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := g.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return connector.ReadResponse{}, err
	}
	key, err := g.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	data, err := g.getObject(ctx, key)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	rows, err := g.decodeRows(data, req.Asset.Config)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	if req.Limit > 0 && int64(len(rows)) > req.Limit {
		rows = rows[:req.Limit]
	}
	return connector.ReadResponse{Rows: rows}, nil
}

// Write encodes rows in the configured format and uploads to GCS.
func (g *GCS) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := g.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	key, err := g.keyFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	data, err := g.encodeRows(req.Rows, req.Asset.Config)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	wc := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	if _, err := wc.Write(data); err != nil {
		_ = wc.Close()
		return connector.WriteResponse{}, fmt.Errorf("gcs: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return connector.WriteResponse{}, fmt.Errorf("gcs: write close: %w", err)
	}
	return connector.WriteResponse{RowsWritten: int64(len(req.Rows))}, nil
}

// Close marks the connector closed and closes the GCS client.
// Idempotent — calling Close() twice is safe.
func (g *GCS) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	err := g.client.Close()
	g.closed = true
	return err
}

func (g *GCS) checkClosed() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return ErrClosed
	}
	return nil
}

// keyFromIdentifier extracts the GCS object key from "bucket/key" or "key" format.
// Path traversal is rejected (T-02-05-02).
func (g *GCS) keyFromIdentifier(id string) (string, error) {
	if id == "" {
		return "", errors.New("gcs: empty identifier")
	}
	// Reject path traversal attempts (T-02-05-02).
	for _, seg := range strings.Split(id, "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}
	// If the identifier starts with "<bucket>/", strip the bucket prefix.
	prefix := g.bucket + "/"
	if strings.HasPrefix(id, prefix) {
		return strings.TrimPrefix(id, prefix), nil
	}
	return id, nil
}

func (g *GCS) format_(assetConfig map[string]string) string {
	if assetConfig != nil {
		if f, ok := assetConfig["format"]; ok && f != "" {
			return f
		}
	}
	return g.format
}

func (g *GCS) getObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs: getObject %q: %w", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("gcs: getObject read body: %w", err)
	}
	return data, nil
}

func (g *GCS) inferSchema(data []byte, assetConfig map[string]string) ([]connector.Column, error) {
	format := g.format_(assetConfig)
	switch format {
	case "parquet":
		return g.schemaFromParquet(data)
	case "csv":
		return g.schemaFromCSV(data)
	case "json":
		return g.schemaFromJSON(data)
	default:
		return nil, fmt.Errorf("gcs: unknown format %q", format)
	}
}

func (g *GCS) decodeRows(data []byte, assetConfig map[string]string) ([]connector.Row, error) {
	format := g.format_(assetConfig)
	switch format {
	case "parquet":
		return g.decodeParquet(data)
	case "csv":
		return g.decodeCSV(data)
	case "json":
		return g.decodeJSON(data)
	default:
		return nil, fmt.Errorf("gcs: unknown format %q", format)
	}
}

func (g *GCS) encodeRows(rows []connector.Row, assetConfig map[string]string) ([]byte, error) {
	format := g.format_(assetConfig)
	switch format {
	case "parquet":
		return g.encodeParquet(rows)
	case "csv":
		return g.encodeCSV(rows)
	case "json":
		return g.encodeJSON(rows)
	default:
		return nil, fmt.Errorf("gcs: unknown format %q", format)
	}
}

// --- Parquet encoding ---

func (g *GCS) encodeParquet(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("gcs: parquet encode: no rows")
	}
	group := parquet.Group{}
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
			return nil, fmt.Errorf("gcs: parquet write row: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("gcs: parquet writer close: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *GCS) decodeParquet(data []byte) ([]connector.Row, error) {
	rows, err := parquet.Read[any](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("gcs: parquet read: %w", err)
	}
	out := make([]connector.Row, 0, len(rows))
	for _, r := range rows {
		var fields map[string]any
		switch v := r.(type) {
		case map[string]any:
			fields = v
		default:
			return nil, fmt.Errorf("gcs: parquet row type unexpected: %T", r)
		}
		out = append(out, connector.Row{Fields: fields})
	}
	return out, nil
}

func (g *GCS) schemaFromParquet(data []byte) ([]connector.Column, error) {
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("gcs: parquet schema open: %w", err)
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

func (g *GCS) encodeCSV(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("gcs: csv encode: no rows")
	}
	cols := make([]string, 0, len(rows[0].Fields))
	for k := range rows[0].Fields {
		cols = append(cols, k)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(cols); err != nil {
		return nil, fmt.Errorf("gcs: csv header: %w", err)
	}
	for _, r := range rows {
		record := make([]string, len(cols))
		for i, c := range cols {
			record[i] = fmt.Sprintf("%v", r.Fields[c])
		}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("gcs: csv write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("gcs: csv flush: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *GCS) decodeCSV(data []byte) ([]connector.Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("gcs: csv header read: %w", err)
	}
	var rows []connector.Row
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcs: csv row read: %w", err)
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

func (g *GCS) schemaFromCSV(data []byte) ([]connector.Column, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("gcs: csv schema read: %w", err)
	}
	cols := make([]connector.Column, len(headers))
	for i, h := range headers {
		cols[i] = connector.Column{Name: h, RawType: "string", Nullable: true}
	}
	return cols, nil
}

// --- JSON encoding ---

func (g *GCS) encodeJSON(rows []connector.Row) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r.Fields); err != nil {
			return nil, fmt.Errorf("gcs: json encode row: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func (g *GCS) decodeJSON(data []byte) ([]connector.Row, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var rows []connector.Row
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gcs: json decode row: %w", err)
		}
		rows = append(rows, connector.Row{Fields: m})
	}
	return rows, nil
}

func (g *GCS) schemaFromJSON(data []byte) ([]connector.Column, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("gcs: json schema decode: %w", err)
	}
	cols := make([]connector.Column, 0, len(m))
	for k := range m {
		cols = append(cols, connector.Column{Name: k, RawType: "json", Nullable: true})
	}
	return cols, nil
}

// NewClientFromOptions constructs a GCS client from explicit options.
// endpoint is optional; when set, all authentication is disabled and requests
// are routed to the provided endpoint (for fake-gcs-server in tests).
// credentialsJSON is never logged (T-02-05-01).
func NewClientFromOptions(ctx context.Context, endpoint, credentialsJSON string, useDefaultCredentials bool) (*gcstorage.Client, error) {
	var opts []option.ClientOption
	if endpoint != "" {
		// Emulator / fake-gcs-server mode.
		opts = append(opts,
			option.WithEndpoint(endpoint),
			option.WithoutAuthentication(),
		)
	} else if credentialsJSON != "" {
		// Production service account.
		// NOTE: credentialsJSON value is NEVER included in log output (T-02-05-01).
		opts = append(opts, option.WithCredentialsJSON([]byte(credentialsJSON)))
	}
	client, err := gcstorage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: new client: %w", err)
	}
	return client, nil
}
