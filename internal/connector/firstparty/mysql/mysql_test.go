package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	// Register mysql driver for direct test setup.
	_ "github.com/go-sql-driver/mysql"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/connector/firstparty/conformance"
	"github.com/kanpon/data-governance/internal/connector/firstparty/mysql"
)

var testDSN string

func TestMain(m *testing.M) {
	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	c, err := tcmysql.Run(ctx, "mysql:8",
		tcmysql.WithDatabase("test"),
		tcmysql.WithUsername("test"),
		tcmysql.WithPassword("test"),
	)
	if err != nil {
		// Docker unavailable — skip suite gracefully.
		os.Exit(0)
	}
	defer func() {
		if c != nil {
			_ = c.Terminate(ctx)
		}
	}()
	dsn, err := c.ConnectionString(ctx)
	if err != nil {
		os.Exit(1)
	}
	testDSN = dsn
	os.Exit(m.Run())
}

// TestCompileTimeAssertion verifies that MySQL satisfies connector.Connector.
func TestCompileTimeAssertion(t *testing.T) {
	var _ connector.Connector = (*mysql.MySQL)(nil)
}

func TestFactory_MissingDSN(t *testing.T) {
	_, err := mysql.Factory(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing DSN, got nil")
	}
}

func TestMySQL_Conformance(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	c, err := mysql.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("mysql.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Parse database name from DSN for information_schema queries.
	// testcontainers DSN format: "test:test@tcp(host:port)/test"
	// We pass the schema-qualified identifier "test.conform_users".
	dbName := "test"
	tableName := "conform_users"

	setup := conformance.Setup{
		CreateAsset: func(t *testing.T, c connector.Connector) connector.AssetRef {
			t.Helper()
			// Use a raw *sql.DB to create the table before the connector runs.
			db, err := sql.Open("mysql", testDSN)
			if err != nil {
				t.Fatalf("setup: open db: %v", err)
			}
			defer db.Close()
			_, err = db.ExecContext(context.Background(),
				fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.%s (id BIGINT NOT NULL, email VARCHAR(255))", dbName, tableName))
			if err != nil {
				t.Fatalf("setup: create table: %v", err)
			}
			return connector.AssetRef{Identifier: dbName + "." + tableName}
		},
		CleanupAsset: func(t *testing.T, c connector.Connector, ref connector.AssetRef) {
			t.Helper()
			db, err := sql.Open("mysql", testDSN)
			if err != nil {
				return
			}
			defer db.Close()
			_, _ = db.ExecContext(context.Background(),
				fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", dbName, tableName))
		},
		ExpectedSchema: []connector.Column{
			{Name: "id", Nullable: false},
			{Name: "email", Nullable: true},
		},
		SeedRows: []connector.Row{
			{Fields: map[string]any{"id": int64(1), "email": "alice@example.com"}},
			{Fields: map[string]any{"id": int64(2), "email": "bob@example.com"}},
			{Fields: map[string]any{"id": int64(3), "email": "carol@example.com"}},
		},
	}
	conformance.RunConformance(t, c, setup)
}

func TestMySQL_APIVersion(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	c, err := mysql.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("mysql.New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if got := c.APIVersion(); got != connector.APIVersion {
		t.Errorf("APIVersion() = %q, want %q", got, connector.APIVersion)
	}
}

func TestMySQL_Close_Idempotent(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	c, err := mysql.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("mysql.New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMySQL_QuoteIdentifier_RejectBacktick(t *testing.T) {
	if testDSN == "" {
		t.Skip("no test DSN (docker unavailable)")
	}
	ctx := context.Background()
	c, err := mysql.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("mysql.New: %v", err)
	}
	defer func() { _ = c.Close() }()
	// Read with a backtick-injected identifier should return an error.
	_, err = c.Read(ctx, connector.ReadRequest{
		Asset: connector.AssetRef{Identifier: "db.`injection"},
	})
	if err == nil {
		t.Error("expected error for backtick in identifier, got nil")
	}
}
