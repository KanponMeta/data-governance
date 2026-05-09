package schema

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// WriteSchemaChanges INSERTs one schema_changes row per change. Returns the
// list of inserted IDs (callers include these in audit-pointer event payloads).
//
// All inserts run in the supplied tx so they roll back atomically with the
// schema_versions INSERT in the caller tx (D-11 audit-trail consistency).
//
// Classify is called with IsWideningPostgres as the lattice — this resolves
// the provisional ChangeTypeWidened emitted by Diff to either type_widened or
// type_narrowed based on the PostgreSQL type lattice rules (D-09).
func WriteSchemaChanges(ctx context.Context, tx *sql.Tx,
	runID uuid.UUID, asset, codeHash string,
	prevVersionID *uuid.UUID, newVersionID uuid.UUID,
	changes []SchemaChange) ([]uuid.UUID, error) {

	if len(changes) == 0 {
		return nil, nil
	}

	const ins = `
		INSERT INTO schema_changes
			(id, asset, run_id, code_hash, prev_version_id, new_version_id,
			 change_type, column_name,
			 prev_type, new_type, prev_nullable, new_nullable,
			 is_breaking, observed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`

	ids := make([]uuid.UUID, 0, len(changes))
	now := time.Now().UTC()

	for _, c := range changes {
		changeType, isBreaking := Classify(c, IsWideningPostgres)
		id := uuid.New()

		// prevVersionID is optional (nil on first capture).
		var prevPtr any
		if prevVersionID != nil {
			prevPtr = *prevVersionID
		}

		// column_name = "" means PK-level change → store as NULL.
		var colNameParam any = c.ColumnName
		if c.ColumnName == "" {
			colNameParam = nil
		}

		if _, err := tx.ExecContext(ctx, ins,
			id,
			asset,
			runID,
			codeHash,
			prevPtr,
			newVersionID,
			changeType,
			colNameParam,
			stringPtrToParam(c.PrevType),
			stringPtrToParam(c.NewType),
			boolPtrToParam(c.PrevNullable),
			boolPtrToParam(c.NewNullable),
			isBreaking,
			now,
		); err != nil {
			return nil, fmt.Errorf("schema: insert schema_changes (%s): %w", changeType, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// uuidsToStrings converts a []uuid.UUID to []string for JSON serialization.
func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// Package-private helpers shared between writer_diff.go and future callers.

// stringPtrToParam converts *string to any (returns nil for nil pointer).
func stringPtrToParam(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// boolPtrToParam converts *bool to any (returns nil for nil pointer).
func boolPtrToParam(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}
