// Package hdfs implements the connector.Connector interface for HDFS (Hadoop
// Distributed File System). It reads and writes rows in Parquet, CSV, or JSON
// format using file paths as Asset.Identifier.
//
// Testing strategy: default tests require an HDFS namenode. When
// HDFS_TEST_NAMENODE is not set, all tests are skipped gracefully.
// For local manual testing, see testdata/hdfs/docker-compose.yml.
//
// Security:
//   - T-02-05-02 (path traversal): identifiers containing ".." path segments
//     are rejected before any HDFS operation.
//   - T-02-05-03 (in-memory read): objects are loaded fully into memory;
//     streaming deferred to Phase 3.
package hdfs

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	hdfslib "github.com/colinmarc/hdfs/v2"
	parquet "github.com/parquet-go/parquet-go"

	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingNamenode is returned by Factory when the "namenode" parameter is absent.
	ErrMissingNamenode = errors.New("hdfs: namenode parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("hdfs: connector closed")
	// ErrPathTraversal is returned when an identifier contains ".." segments (T-02-05-02).
	ErrPathTraversal = errors.New("hdfs: identifier contains path traversal sequence")
)

// Compile-time assertion: HDFS satisfies connector.Connector.
var _ connector.Connector = (*HDFS)(nil)

// HDFS is the HDFS connector. Lifecycle (D-08): one instance per configured
// connector name, client kept for the process lifetime.
type HDFS struct {
	client *hdfslib.Client
	format string // "parquet" | "csv" | "json"
	mu     sync.RWMutex
	closed bool
}

// New constructs an HDFS connector using the given address and optional username.
// format selects the row encoding: "parquet" (default), "csv", or "json".
func New(address string, user string, format string) (*HDFS, error) {
	if address == "" {
		return nil, ErrMissingNamenode
	}
	if format == "" {
		format = "parquet"
	}

	opts := hdfslib.ClientOptions{
		Addresses: []string{address},
	}
	if user != "" {
		opts.User = user
	} else {
		// Fall back to OS user — hdfs.New does the same internally.
		opts.User = os.Getenv("USER")
	}

	client, err := hdfslib.NewClient(opts)
	if err != nil {
		return nil, fmt.Errorf("hdfs: new client: %w", err)
	}
	return &HDFS{client: client, format: format}, nil
}

// NewFromClient constructs an HDFS connector from an existing *hdfslib.Client.
// Used by tests to inject a pre-configured client.
func NewFromClient(client *hdfslib.Client, format string) *HDFS {
	if format == "" {
		format = "parquet"
	}
	return &HDFS{client: client, format: format}
}

// APIVersion returns the connector ABI version.
func (h *HDFS) APIVersion() string { return connector.APIVersion }

// Ping verifies HDFS connectivity via StatFs and returns connector identity.
func (h *HDFS) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := h.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	if _, err := h.client.StatFs(); err != nil {
		return connector.PingResponse{}, fmt.Errorf("hdfs: ping StatFs: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "hdfs",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset by reading the file
// at the path given by Asset.Identifier and inferring schema from its format.
func (h *HDFS) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := h.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	path, err := h.pathFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	data, err := h.readFile(path)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	cols, err := h.inferSchema(data, req.Asset.Config)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the file at the path given by Asset.Identifier.
func (h *HDFS) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := h.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return connector.ReadResponse{}, err
	}
	path, err := h.pathFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	data, err := h.readFile(path)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	rows, err := h.decodeRows(data, req.Asset.Config)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	if req.Limit > 0 && int64(len(rows)) > req.Limit {
		rows = rows[:req.Limit]
	}
	return connector.ReadResponse{Rows: rows}, nil
}

// Write encodes rows in the configured format and writes to the HDFS path
// given by Asset.Identifier. Existing files are overwritten.
func (h *HDFS) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := h.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	path, err := h.pathFromIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	data, err := h.encodeRows(req.Rows, req.Asset.Config)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	if err := h.writeFile(path, data); err != nil {
		return connector.WriteResponse{}, err
	}
	return connector.WriteResponse{RowsWritten: int64(len(req.Rows))}, nil
}

// Close closes the HDFS client connection.
// Idempotent — calling Close() twice is safe.
func (h *HDFS) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	err := h.client.Close()
	h.closed = true
	return err
}

func (h *HDFS) checkClosed() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return ErrClosed
	}
	return nil
}

// pathFromIdentifier validates the HDFS path. Paths must be absolute and must
// not contain ".." traversal segments (T-02-05-02).
func (h *HDFS) pathFromIdentifier(id string) (string, error) {
	if id == "" {
		return "", errors.New("hdfs: empty identifier")
	}
	for _, seg := range strings.Split(id, "/") {
		if seg == ".." {
			return "", ErrPathTraversal
		}
	}
	// Ensure the path is absolute.
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return id, nil
}

func (h *HDFS) readFile(path string) ([]byte, error) {
	f, err := h.client.Open(path)
	if err != nil {
		return nil, fmt.Errorf("hdfs: open %q: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("hdfs: read %q: %w", path, err)
	}
	return data, nil
}

func (h *HDFS) writeFile(path string, data []byte) error {
	// Ensure parent directory exists.
	dir := path[:strings.LastIndex(path, "/")+1]
	if dir == "" {
		dir = "/"
	}
	if err := h.client.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hdfs: mkdirall %q: %w", dir, err)
	}
	// Remove existing file if present (Create fails on existing files).
	_ = h.client.Remove(path)
	w, err := h.client.Create(path)
	if err != nil {
		return fmt.Errorf("hdfs: create %q: %w", path, err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return fmt.Errorf("hdfs: write %q: %w", path, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("hdfs: close writer %q: %w", path, err)
	}
	return nil
}

func (h *HDFS) format_(assetConfig map[string]string) string {
	if assetConfig != nil {
		if f, ok := assetConfig["format"]; ok && f != "" {
			return f
		}
	}
	return h.format
}

func (h *HDFS) inferSchema(data []byte, assetConfig map[string]string) ([]connector.Column, error) {
	switch h.format_(assetConfig) {
	case "parquet":
		return h.schemaFromParquet(data)
	case "csv":
		return h.schemaFromCSV(data)
	case "json":
		return h.schemaFromJSON(data)
	default:
		return nil, fmt.Errorf("hdfs: unknown format %q", h.format_(assetConfig))
	}
}

func (h *HDFS) decodeRows(data []byte, assetConfig map[string]string) ([]connector.Row, error) {
	switch h.format_(assetConfig) {
	case "parquet":
		return h.decodeParquet(data)
	case "csv":
		return h.decodeCSV(data)
	case "json":
		return h.decodeJSON(data)
	default:
		return nil, fmt.Errorf("hdfs: unknown format %q", h.format_(assetConfig))
	}
}

func (h *HDFS) encodeRows(rows []connector.Row, assetConfig map[string]string) ([]byte, error) {
	switch h.format_(assetConfig) {
	case "parquet":
		return h.encodeParquet(rows)
	case "csv":
		return h.encodeCSV(rows)
	case "json":
		return h.encodeJSON(rows)
	default:
		return nil, fmt.Errorf("hdfs: unknown format %q", h.format_(assetConfig))
	}
}

// --- Parquet encoding ---

func (h *HDFS) encodeParquet(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("hdfs: parquet encode: no rows")
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
			return nil, fmt.Errorf("hdfs: parquet write row: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("hdfs: parquet writer close: %w", err)
	}
	return buf.Bytes(), nil
}

func (h *HDFS) decodeParquet(data []byte) ([]connector.Row, error) {
	rows, err := parquet.Read[any](bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("hdfs: parquet read: %w", err)
	}
	out := make([]connector.Row, 0, len(rows))
	for _, r := range rows {
		switch v := r.(type) {
		case map[string]any:
			out = append(out, connector.Row{Fields: v})
		default:
			return nil, fmt.Errorf("hdfs: parquet row type unexpected: %T", r)
		}
	}
	return out, nil
}

func (h *HDFS) schemaFromParquet(data []byte) ([]connector.Column, error) {
	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("hdfs: parquet schema open: %w", err)
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

func (h *HDFS) encodeCSV(rows []connector.Row) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("hdfs: csv encode: no rows")
	}
	cols := make([]string, 0, len(rows[0].Fields))
	for k := range rows[0].Fields {
		cols = append(cols, k)
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(cols); err != nil {
		return nil, fmt.Errorf("hdfs: csv header: %w", err)
	}
	for _, r := range rows {
		record := make([]string, len(cols))
		for i, c := range cols {
			record[i] = fmt.Sprintf("%v", r.Fields[c])
		}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("hdfs: csv write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("hdfs: csv flush: %w", err)
	}
	return buf.Bytes(), nil
}

func (h *HDFS) decodeCSV(data []byte) ([]connector.Row, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("hdfs: csv header read: %w", err)
	}
	var rows []connector.Row
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hdfs: csv row read: %w", err)
		}
		row := connector.Row{Fields: make(map[string]any, len(headers))}
		for i, h2 := range headers {
			if i < len(record) {
				row.Fields[h2] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (h *HDFS) schemaFromCSV(data []byte) ([]connector.Column, error) {
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("hdfs: csv schema read: %w", err)
	}
	cols := make([]connector.Column, len(headers))
	for i, hdr := range headers {
		cols[i] = connector.Column{Name: hdr, RawType: "string", Nullable: true}
	}
	return cols, nil
}

// --- JSON encoding ---

func (h *HDFS) encodeJSON(rows []connector.Row) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r.Fields); err != nil {
			return nil, fmt.Errorf("hdfs: json encode row: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func (h *HDFS) decodeJSON(data []byte) ([]connector.Row, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var rows []connector.Row
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hdfs: json decode row: %w", err)
		}
		rows = append(rows, connector.Row{Fields: m})
	}
	return rows, nil
}

func (h *HDFS) schemaFromJSON(data []byte) ([]connector.Column, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("hdfs: json schema decode: %w", err)
	}
	cols := make([]connector.Column, 0, len(m))
	for k := range m {
		cols = append(cols, connector.Column{Name: k, RawType: "json", Nullable: true})
	}
	return cols, nil
}
