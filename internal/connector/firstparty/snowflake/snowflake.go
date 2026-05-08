// Package snowflake implements the connector.Connector interface for Snowflake,
// reading and writing rows via database/sql + gosnowflake driver.
// It mirrors the MySQL connector (SQL archetype) with Snowflake-specific identifier
// quoting ("schema"."table") and DSN format.
//
// Testing strategy (D-CLAUDE-DISCRETION):
//   - Default tests use go-sqlmock to verify SQL generation without a running Snowflake.
//   - Real credential integration tests are gated behind //go:build snowflake_real_creds
//     and run only in nightly CI jobs with SNOWFLAKE_DSN set.
//
// This approach is documented in T-02-05-04 of the threat register — the mock test
// docstring explicitly notes that round-trip data correctness is NOT proven by the
// default tests; the nightly job is the source of truth for correctness.
package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// Register the snowflake driver.
	_ "github.com/snowflakedb/gosnowflake"
	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingDSN is returned by Factory when the "dsn" parameter is absent or empty.
	ErrMissingDSN = errors.New("snowflake: dsn parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("snowflake: connector closed")
)

// Compile-time assertion: Snowflake satisfies connector.Connector.
var _ connector.Connector = (*Snowflake)(nil)

// Snowflake is the Snowflake connector. Lifecycle (D-08): one instance per configured
// connector name, db pool kept for the process lifetime.
type Snowflake struct {
	db     *sql.DB
	mu     sync.RWMutex
	closed bool
}

// New constructs a Snowflake connector. dsn must be a valid Snowflake DSN
// (e.g. "user:password@account/database/schema?warehouse=mywh").
// A connectivity test is performed at startup; callers should call Close() when done.
func New(ctx context.Context, dsn string) (*Snowflake, error) {
	db, err := sql.Open("snowflake", dsn)
	if err != nil {
		return nil, fmt.Errorf("snowflake: open: %w", err)
	}
	// Verify connectivity immediately so startup failures are obvious.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("snowflake: initial ping: %w", err)
	}
	return &Snowflake{db: db}, nil
}

// NewFromDB constructs a Snowflake connector from an existing *sql.DB.
// Used by tests to inject a mock database.
func NewFromDB(db *sql.DB) *Snowflake {
	return &Snowflake{db: db}
}

// APIVersion returns the connector ABI version.
func (s *Snowflake) APIVersion() string { return connector.APIVersion }

// Ping returns the connector's identity and capabilities.
func (s *Snowflake) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	if err := s.db.PingContext(ctx); err != nil {
		return connector.PingResponse{}, fmt.Errorf("snowflake: ping: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "snowflake",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset by querying
// information_schema.columns. Asset identifier may be "DATABASE.SCHEMA.TABLE",
// "SCHEMA.TABLE", or "TABLE".
func (s *Snowflake) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.SchemaResponse{}, err
	}
	schemaName, tableName, err := splitIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.SchemaResponse{}, err
	}

	const q = `
		SELECT column_name, data_type, is_nullable
		  FROM information_schema.columns
		 WHERE table_schema = ? AND table_name = ?
		 ORDER BY ordinal_position
	`
	rows, err := s.db.QueryContext(ctx, q, strings.ToUpper(schemaName), strings.ToUpper(tableName))
	if err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("snowflake: schema query: %w", err)
	}
	defer rows.Close()

	var cols []connector.Column
	for rows.Next() {
		var name, rawType, isNullable string
		if err := rows.Scan(&name, &rawType, &isNullable); err != nil {
			return connector.SchemaResponse{}, fmt.Errorf("snowflake: schema scan: %w", err)
		}
		cols = append(cols, connector.Column{Name: name, RawType: rawType, Nullable: isNullable == "YES" || isNullable == "Y"})
	}
	if err := rows.Err(); err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("snowflake: schema iter: %w", err)
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given asset. ctx is propagated to database/sql.
func (s *Snowflake) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.ReadResponse{}, err
	}
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
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("snowflake: read: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return connector.ReadResponse{}, fmt.Errorf("snowflake: read columns: %w", err)
	}

	out := []connector.Row{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return connector.ReadResponse{}, fmt.Errorf("snowflake: read scan: %w", err)
		}
		r := connector.Row{Fields: make(map[string]any, len(cols))}
		for i, col := range cols {
			r.Fields[col] = vals[i]
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("snowflake: read iter: %w", err)
	}
	return connector.ReadResponse{Rows: out}, nil
}

// Write persists rows to the given asset using a parameterized INSERT.
// All rows must share the same field set (keys are taken from the first row).
// SQL injection is prevented by quoteIdentifier for table/column names and
// by using ? placeholders for all values.
func (s *Snowflake) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := s.checkClosed(); err != nil {
		return connector.WriteResponse{}, err
	}
	if len(req.Rows) == 0 {
		return connector.WriteResponse{RowsWritten: 0}, nil
	}
	ident, err := quoteIdentifier(req.Asset.Identifier)
	if err != nil {
		return connector.WriteResponse{}, err
	}
	cols := make([]string, 0, len(req.Rows[0].Fields))
	for k := range req.Rows[0].Fields {
		cols = append(cols, k)
	}
	// Build INSERT ... VALUES (?, ?, ...), (?, ?, ...).
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
	for ri, r := range req.Rows {
		if ri > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		for ci, c := range cols {
			if ci > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, r.Fields[c])
		}
		sb.WriteString(")")
	}
	result, err := s.db.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("snowflake: write: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("snowflake: write rows affected: %w", err)
	}
	return connector.WriteResponse{RowsWritten: n}, nil
}

// Close closes the database connection pool.
// Idempotent — calling Close() twice is safe.
func (s *Snowflake) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	err := s.db.Close()
	s.closed = true
	return err
}

func (s *Snowflake) checkClosed() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// splitIdentifier splits "DB.SCHEMA.TABLE", "SCHEMA.TABLE", or "TABLE"
// into (schema, table). Returns ("", table) for unqualified identifiers.
func splitIdentifier(id string) (string, string, error) {
	if id == "" {
		return "", "", errors.New("snowflake: empty identifier")
	}
	parts := strings.Split(id, ".")
	switch len(parts) {
	case 3:
		// DB.SCHEMA.TABLE — use SCHEMA.TABLE
		return parts[1], parts[2], nil
	case 2:
		return parts[0], parts[1], nil
	case 1:
		return "", parts[0], nil
	default:
		return "", "", fmt.Errorf("snowflake: invalid identifier %q", id)
	}
}

// quoteIdentifier returns double-quoted identifier(s). For "DB.SCHEMA.TABLE"
// returns `"DB"."SCHEMA"."TABLE"`, for "SCHEMA.TABLE" returns `"SCHEMA"."TABLE"`.
// Rejects identifiers with embedded double quotes (T-02-05-02).
func quoteIdentifier(id string) (string, error) {
	if strings.ContainsRune(id, '"') {
		return "", fmt.Errorf("snowflake: identifier contains illegal character: %q", id)
	}
	if strings.Contains(id, "..") {
		return "", fmt.Errorf("snowflake: identifier contains path traversal sequence: %q", id)
	}
	parts := strings.Split(id, ".")
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = `"` + p + `"`
	}
	return strings.Join(quoted, "."), nil
}
