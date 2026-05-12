// Package neighborhood provides the lineage neighborhood query
// (recursive CTE for DAG visualization).
package neighborhood

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the interface for executing queries.
type DBTX interface {
	Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error)
	Query(context.Context, string, ...interface{}) (pgx.Rows, error)
	QueryRow(context.Context, string, ...interface{}) pgx.Row
}

// NeighborhoodAssetsRow represents a row from the neighborhood query.
type NeighborhoodAssetsRow struct {
	Asset string
	Depth int32
}

// NeighborhoodEdgesRow represents a row from the edges query.
type NeighborhoodEdgesRow struct {
	FromAsset string
	ToAsset   string
}

// NeighborhoodAssets fetches asset neighborhood up to depth N (downstream only).
// Uses a recursive CTE similar to TraverseAssetLineage but for neighborhood visualization.
func NeighborhoodAssets(ctx context.Context, db DBTX, fromAsset string, depth int32) ([]NeighborhoodAssetsRow, error) {
	const query = `-- name: NeighborhoodAssets :many
WITH RECURSIVE neighborhood AS (
    SELECT asset_edges.to_asset as asset, 1 AS depth
    FROM asset_edges
    WHERE asset_edges.from_asset = $1 AND asset_edges.superseded_at IS NULL
    UNION ALL
    SELECT asset_edges.to_asset, neighborhood.depth + 1
    FROM asset_edges
    JOIN neighborhood ON asset_edges.from_asset = neighborhood.asset
    WHERE neighborhood.depth < $2 AND asset_edges.superseded_at IS NULL
)
SELECT DISTINCT asset, depth FROM neighborhood ORDER BY depth, asset LIMIT 50`

	rows, err := db.Query(ctx, query, fromAsset, depth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []NeighborhoodAssetsRow
	for rows.Next() {
		var i NeighborhoodAssetsRow
		if err := rows.Scan(&i.Asset, &i.Depth); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// NeighborhoodEdges fetches all edges between assets in the neighborhood.
func NeighborhoodEdges(ctx context.Context, db DBTX, assetList []string) ([]NeighborhoodEdgesRow, error) {
	if len(assetList) == 0 {
		return nil, nil
	}

	// Build the ANY array string
	placeholders := make([]string, len(assetList))
	args := make([]interface{}, len(assetList))
	for i, a := range assetList {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = a
	}

	query := `-- name: NeighborhoodEdges :many
SELECT asset_edges.from_asset, asset_edges.to_asset
FROM asset_edges
WHERE asset_edges.from_asset = ANY(ARRAY[` + strings.Join(placeholders, ",") + `]::text[])
  AND asset_edges.to_asset = ANY(ARRAY[` + strings.Join(placeholders, ",") + `]::text[])
  AND asset_edges.superseded_at IS NULL`

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []NeighborhoodEdgesRow
	for rows.Next() {
		var i NeighborhoodEdgesRow
		if err := rows.Scan(&i.FromAsset, &i.ToAsset); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// pgtype helpers for compatibility
type Timestamptz = pgtype.Timestamptz