// Package bigquery implements the connector.Connector interface for Google BigQuery,
// reading and writing rows via the cloud.google.com/go/bigquery Go client.
// It follows the SQL archetype established by the PostgreSQL connector (D-12).
package bigquery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	bq "cloud.google.com/go/bigquery"
	"github.com/kanpon/data-governance/internal/connector"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

var (
	// ErrMissingProject is returned by Factory when the "project" parameter is absent.
	ErrMissingProject = errors.New("bigquery: project parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("bigquery: connector closed")
)

// Compile-time assertion: BigQuery satisfies connector.Connector.
var _ connector.Connector = (*BigQuery)(nil)

// BigQuery is the Google BigQuery connector. Lifecycle (D-08): one instance per
// configured connector name, client kept for the process lifetime.
type BigQuery struct {
	client  *bq.Client
	project string
	mu      sync.RWMutex
	closed  bool
}

// New constructs a BigQuery connector using the provided client options.
// The project is used for DDL operations; assets must include project in their identifier.
func New(ctx context.Context, project string, opts ...option.ClientOption) (*BigQuery, error) {
	client, err := bq.NewClient(ctx, project, opts...)
	if err != nil {
		return nil, fmt.Errorf("bigquery: new client: %w", err)
	}
	return &BigQuery{client: client, project: project}, nil
}

// APIVersion returns the connector ABI version.
func (b *BigQuery) APIVersion() string { return connector.APIVersion }

// Ping verifies the BigQuery client is functional by listing datasets (lightweight probe).
func (b *BigQuery) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := b.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	// Light probe: list datasets (does not download data, just checks auth + connectivity).
	it := b.client.Datasets(ctx)
	it.ProjectID = b.project
	_, err := it.Next()
	if err != nil && err != iterator.Done {
		// "iterator.Done" means no datasets but connection works — acceptable.
		return connector.PingResponse{}, fmt.Errorf("bigquery: ping: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "bigquery",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset by querying the table metadata.
// Asset identifier must be "project.dataset.table".
func (b *BigQuery) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := b.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	_, dataset, table, err := splitIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	meta, err := b.client.Dataset(dataset).Table(table).Metadata(ctx)
	if err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("bigquery: schema metadata: %w", err)
	}
	cols := make([]connector.Column, len(meta.Schema))
	for i, f := range meta.Schema {
		cols[i] = connector.Column{
			Name:     f.Name,
			RawType:  string(f.Type),
			Nullable: f.Required == false,
		}
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given BigQuery table using SELECT *.
// Asset identifier may be "project.dataset.table" or "dataset.table"; in the
// latter case, the configured default project is used.
func (b *BigQuery) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := b.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return connector.ReadResponse{}, err
	}
	project, dataset, table, err := splitIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	if project == "" {
		project = b.project
	}
	q := fmt.Sprintf("SELECT * FROM `%s`.`%s`.`%s`", project, dataset, table)
	if req.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", req.Limit)
	}
	query := b.client.Query(q)
	it, err := query.Read(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("bigquery: read query: %w", err)
	}
	var rows []connector.Row
	for {
		var vals []bq.Value
		err := it.Next(&vals)
		if err == iterator.Done {
			break
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return connector.ReadResponse{}, ctxErr
			}
			return connector.ReadResponse{}, fmt.Errorf("bigquery: read iter: %w", err)
		}
		schema := it.Schema
		row := connector.Row{Fields: make(map[string]any, len(vals))}
		for i, v := range vals {
			name := schema[i].Name
			row.Fields[name] = v
		}
		rows = append(rows, row)
	}
	if rows == nil {
		rows = []connector.Row{}
	}
	return connector.ReadResponse{Rows: rows}, nil
}

// Write persists rows to the BigQuery table using the Inserter (streaming insert API).
// Asset identifier must be "project.dataset.table".
func (b *BigQuery) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := b.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	_, dataset, table, err := splitIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	ins := b.client.Dataset(dataset).Table(table).Inserter()
	// Convert connector.Row to bigquery.ValueSaver (map-based saver).
	savers := make([]*bqRowSaver, len(req.Rows))
	for i, r := range req.Rows {
		savers[i] = &bqRowSaver{fields: r.Fields}
	}
	if err := ins.Put(ctx, savers); err != nil {
		return connector.WriteResponse{}, fmt.Errorf("bigquery: write insert: %w", err)
	}
	return connector.WriteResponse{RowsWritten: int64(len(req.Rows))}, nil
}

// Close closes the underlying BigQuery client.
// Idempotent — calling Close() twice is safe.
func (b *BigQuery) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	err := b.client.Close()
	b.closed = true
	return err
}

func (b *BigQuery) checkClosed() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return ErrClosed
	}
	return nil
}

// splitIdentifier splits "project.dataset.table" into its three components.
// If only two parts are given ("dataset.table"), uses the connector's project.
func splitIdentifier(id string) (project, dataset, table string, err error) {
	if id == "" {
		return "", "", "", errors.New("bigquery: empty identifier")
	}
	parts := strings.Split(id, ".")
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2], nil
	case 2:
		return "", parts[0], parts[1], nil
	default:
		return "", "", "", fmt.Errorf("bigquery: invalid identifier %q (expected project.dataset.table or dataset.table)", id)
	}
}

// bqRowSaver implements bigquery.ValueSaver for connector.Row.
type bqRowSaver struct {
	fields map[string]any
}

func (s *bqRowSaver) Save() (map[string]bq.Value, string, error) {
	row := make(map[string]bq.Value, len(s.fields))
	for k, v := range s.fields {
		row[k] = bq.Value(v)
	}
	return row, bq.NoDedupeID, nil
}
