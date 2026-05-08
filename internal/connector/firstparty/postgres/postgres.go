// Package postgres implements the connector.Connector interface for PostgreSQL,
// reading and writing rows via pgxpool. It is the reference first-party connector
// (D-12) — other connectors in plan 02-05 follow the same shape.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingDSN is returned by Factory when the "dsn" parameter is absent or empty.
	ErrMissingDSN = errors.New("postgres: dsn parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("postgres: connector closed")
)

// Compile-time assertion: Postgres satisfies connector.Connector.
var _ connector.Connector = (*Postgres)(nil)

// Postgres is the PostgreSQL connector. Lifecycle (D-08): one instance per
// configured connector name, pool kept for the process lifetime.
type Postgres struct {
	pool   *pgxpool.Pool
	mu     sync.RWMutex
	closed bool
}

// New constructs a Postgres connector. dsn must be a valid PG connection string.
// The pool is initialized with a connection test; callers should call Close() when done.
func New(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	// Verify connectivity immediately so startup failures are obvious.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: initial ping: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// APIVersion returns the connector ABI version.
func (p *Postgres) APIVersion() string { return connector.APIVersion }

// Ping returns the connector's identity and capabilities.
func (p *Postgres) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := p.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	if err := p.pool.Ping(ctx); err != nil {
		return connector.PingResponse{}, fmt.Errorf("postgres: ping: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "postgres",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns the column definitions for the given asset by querying
// information_schema.columns. Asset identifier may be "schema.table" or "table"
// (defaults to "public" schema).
func (p *Postgres) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := p.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	schemaName, tableName, err := splitIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}
	const q = `
		SELECT column_name, data_type, is_nullable
		  FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2
		 ORDER BY ordinal_position
	`
	rows, err := p.pool.Query(ctx, q, schemaName, tableName)
	if err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("postgres: schema query: %w", err)
	}
	defer rows.Close()

	var cols []connector.Column
	for rows.Next() {
		var name, rawType, isNullable string
		if err := rows.Scan(&name, &rawType, &isNullable); err != nil {
			return connector.SchemaResponse{}, fmt.Errorf("postgres: schema scan: %w", err)
		}
		cols = append(cols, connector.Column{Name: name, RawType: rawType, Nullable: isNullable == "YES"})
	}
	if err := rows.Err(); err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("postgres: schema iter: %w", err)
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given asset. ctx is propagated to pgxpool so
// context cancellation interrupts the query and returns context.Canceled (PITFALLS §10).
func (p *Postgres) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := p.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
	// Check context before issuing query (fast path for already-canceled context).
	if err := ctx.Err(); err != nil {
		return connector.ReadResponse{}, err
	}
	ident, err := quoteIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.ReadResponse{}, err
	}
	query := "SELECT * FROM " + ident
	if req.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", req.Limit)
	}
	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		// Unwrap pgx-wrapped context errors to expose context.Canceled / context.DeadlineExceeded
		// directly (PITFALLS §10).
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("postgres: read: %w", err)
	}
	defer rows.Close()

	fieldDescs := rows.FieldDescriptions()
	out := []connector.Row{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return connector.ReadResponse{}, fmt.Errorf("postgres: read scan: %w", err)
		}
		r := connector.Row{Fields: make(map[string]any, len(fieldDescs))}
		for i, fd := range fieldDescs {
			r.Fields[string(fd.Name)] = values[i]
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("postgres: read iter: %w", err)
	}
	return connector.ReadResponse{Rows: out}, nil
}

// Write persists rows to the given asset using a parameterized INSERT.
// All rows must share the same field set (keys are taken from the first row).
// SQL injection is prevented by quoteIdentifier for table/column names and
// by using $N placeholders for all values.
func (p *Postgres) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := p.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	ident, err := quoteIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	// Discover column order from the first row (rows must share the same fields in v1).
	cols := make([]string, 0, len(req.Rows[0].Fields))
	for k := range req.Rows[0].Fields {
		cols = append(cols, k)
	}
	// Build INSERT ... VALUES ($1, $2, ...), ($N, $N+1, ...).
	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(ident)
	sb.WriteString(" (")
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(",")
		}
		qc, qerr := quoteIdentifier(c)
		if qerr != nil {
			return connector.WriteResponse{}, qerr
		}
		sb.WriteString(qc)
	}
	sb.WriteString(") VALUES ")
	args := make([]any, 0, len(req.Rows)*len(cols))
	param := 1
	for ri, r := range req.Rows {
		if ri > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		for ci, c := range cols {
			if ci > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "$%d", param)
			args = append(args, r.Fields[c])
			param++
		}
		sb.WriteString(")")
	}
	ct, err := p.pool.Exec(ctx, sb.String(), args...)
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("postgres: write: %w", err)
	}
	return connector.WriteResponse{RowsWritten: ct.RowsAffected()}, nil
}

// Close drains the pool. Subsequent operations return ErrClosed.
// Idempotent — calling Close() twice is safe.
func (p *Postgres) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.pool.Close()
	p.closed = true
	return nil
}

func (p *Postgres) checkClosed() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return ErrClosed
	}
	return nil
}

// splitIdentifier splits "schema.table" into (schema, table). If no dot is present,
// returns ("public", id).
func splitIdentifier(id string) (string, string, error) {
	if id == "" {
		return "", "", errors.New("postgres: empty identifier")
	}
	parts := strings.SplitN(id, ".", 2)
	if len(parts) == 1 {
		return "public", parts[0], nil
	}
	return parts[0], parts[1], nil
}

// quoteIdentifier returns "schema"."table" with safe identifier quoting.
// For single-token names (e.g., column names) returns "name".
// Rejects identifiers with embedded double quotes (defense against SQL injection
// via asset names; legitimate names should never contain quotes — T-02-04-03).
func quoteIdentifier(id string) (string, error) {
	if strings.ContainsRune(id, '"') {
		return "", fmt.Errorf("postgres: identifier contains illegal character: %q", id)
	}
	if !strings.Contains(id, ".") {
		// Single-token identifier (column name or unqualified table name).
		return `"` + id + `"`, nil
	}
	s, t, err := splitIdentifier(id)
	if err != nil {
		return "", err
	}
	return `"` + s + `"."` + t + `"`, nil
}
