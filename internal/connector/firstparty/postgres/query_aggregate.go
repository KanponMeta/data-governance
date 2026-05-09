package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/kanpon/data-governance/internal/connector"
)

// Compile-time assertion: Postgres satisfies the optional QueryAggregate capability (Phase 5 D-19).
var _ connector.QueryAggregate = (*Postgres)(nil)

// QueryAggregate executes the given SQL text and returns a single result row.
// It is the Phase 5 quality-rule evaluation entrypoint.
//
// The connector caller MUST wrap ctx with a strict timeout (Pitfall #10) so a
// runaway aggregate cannot block the executor transaction. Errors are returned
// verbatim so the evaluator can record them in quality_results.error_message.
//
// On success the returned AggregateRow holds the column names alongside the
// scanned values; aggregate queries are expected to yield exactly one row, so
// a "no rows" return becomes an error to surface miswritten user assertions
// (e.g., COUNT(*) FROM empty_table_with_filter that excludes everything).
func (p *Postgres) QueryAggregate(ctx context.Context, ref connector.AssetRef, sqlText string) (connector.AggregateRow, error) {
	if err := p.checkClosed(); err != nil {
		return connector.AggregateRow{}, err
	}
	rows, err := p.pool.Query(ctx, sqlText)
	if err != nil {
		return connector.AggregateRow{}, fmt.Errorf("postgres.QueryAggregate: exec: %w", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	cols := make([]string, len(fields))
	for i := range fields {
		cols[i] = string(fields[i].Name)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return connector.AggregateRow{}, fmt.Errorf("postgres.QueryAggregate: %w", err)
		}
		return connector.AggregateRow{Columns: cols}, errors.New("postgres.QueryAggregate: no rows")
	}
	values, err := rows.Values()
	if err != nil {
		return connector.AggregateRow{}, fmt.Errorf("postgres.QueryAggregate: scan: %w", err)
	}
	return connector.AggregateRow{Columns: cols, Values: values}, nil
}
