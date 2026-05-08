// Package mysql implements the connector.Connector interface for MySQL,
// reading and writing rows via database/sql + go-sql-driver/mysql.
// It mirrors the PostgreSQL connector (D-12 reference implementation)
// adapted for MySQL syntax and backtick identifier quoting.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// Register the mysql driver.
	_ "github.com/go-sql-driver/mysql"
	"github.com/kanpon/data-governance/internal/connector"
)

var (
	// ErrMissingDSN is returned by Factory when the "dsn" parameter is absent or empty.
	ErrMissingDSN = errors.New("mysql: dsn parameter required")
	// ErrClosed is returned by any operation after Close() has been called.
	ErrClosed = errors.New("mysql: connector closed")
)

// Compile-time assertion: MySQL satisfies connector.Connector.
var _ connector.Connector = (*MySQL)(nil)

// MySQL is the MySQL connector. Lifecycle (D-08): one instance per configured
// connector name, db pool kept for the process lifetime.
type MySQL struct {
	db     *sql.DB
	mu     sync.RWMutex
	closed bool
}

// New constructs a MySQL connector. dsn must be a valid MySQL DSN
// (e.g. "user:pass@tcp(host:3306)/dbname"). A connectivity test is performed
// at startup; callers should call Close() when done.
func New(ctx context.Context, dsn string) (*MySQL, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql: open: %w", err)
	}
	// Verify connectivity immediately so startup failures are obvious.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql: initial ping: %w", err)
	}
	return &MySQL{db: db}, nil
}

// APIVersion returns the connector ABI version.
func (m *MySQL) APIVersion() string { return connector.APIVersion }

// Ping returns the connector's identity and capabilities.
func (m *MySQL) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	if err := m.checkClosed(); err != nil {
		return connector.PingResponse{}, err
	}
	if err := m.db.PingContext(ctx); err != nil {
		return connector.PingResponse{}, fmt.Errorf("mysql: ping: %w", err)
	}
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "mysql",
		ConnectorVersion: "1.0.0",
		Capabilities:     connector.Capabilities{SupportsSchemaCapture: true},
	}, nil
}

// Schema returns column definitions for the given asset by querying
// information_schema.columns. Asset identifier may be "database.table" or "table"
// (defaults to the current database when no dot is present).
func (m *MySQL) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	if err := m.checkClosed(); err != nil {
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
	rows, err := m.db.QueryContext(ctx, q, schemaName, tableName)
	if err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("mysql: schema query: %w", err)
	}
	defer rows.Close()

	var cols []connector.Column
	for rows.Next() {
		var name, rawType, isNullable string
		if err := rows.Scan(&name, &rawType, &isNullable); err != nil {
			return connector.SchemaResponse{}, fmt.Errorf("mysql: schema scan: %w", err)
		}
		cols = append(cols, connector.Column{Name: name, RawType: rawType, Nullable: isNullable == "YES"})
	}
	if err := rows.Err(); err != nil {
		return connector.SchemaResponse{}, fmt.Errorf("mysql: schema iter: %w", err)
	}
	return connector.SchemaResponse{Columns: cols, CapturedAt: time.Now().UTC()}, nil
}

// Read returns rows from the given asset. ctx is propagated to database/sql so
// context cancellation interrupts the query (PITFALLS §10).
func (m *MySQL) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	if err := m.checkClosed(); err != nil {
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
	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("mysql: read: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return connector.ReadResponse{}, fmt.Errorf("mysql: read columns: %w", err)
	}

	out := []connector.Row{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return connector.ReadResponse{}, fmt.Errorf("mysql: read scan: %w", err)
		}
		r := connector.Row{Fields: make(map[string]any, len(cols))}
		for i, col := range cols {
			// MySQL driver returns []byte for text types; convert to string for consistency.
			if b, ok := vals[i].([]byte); ok {
				r.Fields[col] = string(b)
			} else {
				r.Fields[col] = vals[i]
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return connector.ReadResponse{}, ctxErr
		}
		return connector.ReadResponse{}, fmt.Errorf("mysql: read iter: %w", err)
	}
	return connector.ReadResponse{Rows: out}, nil
}

// Write persists rows to the given asset using a parameterized INSERT.
// All rows must share the same field set (keys are taken from the first row).
// SQL injection is prevented by quoteIdentifier for table/column names and
// by using ? placeholders for all values.
func (m *MySQL) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	if err := m.checkClosed(); err != nil {
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
	result, err := m.db.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("mysql: write: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return connector.WriteResponse{}, fmt.Errorf("mysql: write rows affected: %w", err)
	}
	return connector.WriteResponse{RowsWritten: n}, nil
}

// Close closes the database connection pool. Subsequent operations return ErrClosed.
// Idempotent — calling Close() twice is safe.
func (m *MySQL) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	err := m.db.Close()
	m.closed = true
	return err
}

func (m *MySQL) checkClosed() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrClosed
	}
	return nil
}

// splitIdentifier splits "database.table" into (database, table). If no dot is present,
// returns ("", id) — the caller's DSN determines the default database in MySQL.
func splitIdentifier(id string) (string, string, error) {
	if id == "" {
		return "", "", errors.New("mysql: empty identifier")
	}
	parts := strings.SplitN(id, ".", 2)
	if len(parts) == 1 {
		return "", parts[0], nil
	}
	return parts[0], parts[1], nil
}

// quoteIdentifier returns backtick-quoted identifier(s). For "db.table" returns
// "`db`.`table`"; for a single token returns "`name`".
// Rejects identifiers with embedded backticks (defense against SQL injection
// via asset names; legitimate names should never contain backticks — T-02-05-02).
// Also rejects identifiers with ".." segments (HDFS/S3 path traversal guard — T-02-05-02).
func quoteIdentifier(id string) (string, error) {
	if strings.ContainsRune(id, '`') {
		return "", fmt.Errorf("mysql: identifier contains illegal character: %q", id)
	}
	if strings.Contains(id, "..") {
		return "", fmt.Errorf("mysql: identifier contains path traversal sequence: %q", id)
	}
	if !strings.Contains(id, ".") {
		return "`" + id + "`", nil
	}
	s, t, err := splitIdentifier(id)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "`" + t + "`", nil
	}
	return "`" + s + "`.`" + t + "`", nil
}
