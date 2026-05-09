package testharness

import (
	"context"
	"database/sql"
	"testing"
)

// SeedOrdersFixture creates the fixtures.orders table with 100 rows.
// customer_id is NULL in exactly 10 rows so NullCheck tests have a stable target.
func SeedOrdersFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	// Create schema.
	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS fixtures.orders`); err != nil {
		t.Fatalf("SeedOrdersFixture: drop: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE fixtures.orders (
			order_id   BIGINT PRIMARY KEY,
			customer_id BIGINT,
			amount     DECIMAL(10,2),
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`); err != nil {
		t.Fatalf("SeedOrdersFixture: create: %v", err)
	}

	// Insert 100 rows: 90 with customer_id set, 10 with NULL.
	for i := int64(1); i <= 100; i++ {
		var customerID any = (i % 10) + 1 // 1..10 for rows 1-90
		if i > 90 {
			customerID = nil // NULL for rows 91-100
		}
		amount := float64(i) * 10.00
		if _, err := db.ExecContext(ctx,
			`INSERT INTO fixtures.orders (order_id, customer_id, amount) VALUES ($1, $2, $3)`,
			i, customerID, amount); err != nil {
			t.Fatalf("SeedOrdersFixture: insert row %d: %v", i, err)
		}
	}
}

// ResetOrders truncates the fixtures.orders table.
func ResetOrders(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `TRUNCATE fixtures.orders`); err != nil {
		t.Fatalf("ResetOrders: %v", err)
	}
}

// OrderCount returns the total row count in fixtures.orders.
func OrderCount(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM fixtures.orders`).Scan(&count); err != nil {
		t.Fatalf("OrderCount: %v", err)
	}
	return count
}

// NullCustomerCount returns how many rows have a NULL customer_id.
func NullCustomerCount(t *testing.T, db *sql.DB) (int64, error) {
	var count int64
	err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM fixtures.orders WHERE customer_id IS NULL`).Scan(&count)
	return count, err
}
