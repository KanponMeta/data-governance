// Package impact provides the public entry point for lineage impact analysis.
// It wraps the sqlc-generated recursive CTE queries (internal/lineage/queries)
// with parameter validation, depth capping, and direction enforcement (D-14, D-19, D-20).
package impact

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	lineageq "github.com/kanpon/data-governance/internal/lineage/queries"
)

const (
	// DefaultDepth is used when ImpactQuery.Depth is ≤ 0.
	DefaultDepth = 10
	// MaxDepth is the hard ceiling enforced by impact.Analyze (D-14 layer 1 of 3).
	// The SQL CTE also enforces LEAST(@max_depth::int, 25) as layer 2.
	// The Wave 7 REST handler validates ?depth ≤ 25 as layer 3.
	MaxDepth     = 25
)

var (
	// ErrDepthExceeded is returned when ImpactQuery.Depth > MaxDepth.
	ErrDepthExceeded = errors.New("impact: depth exceeds hard ceiling 25")
	// ErrAssetRequired is returned when ImpactQuery.Asset is empty.
	ErrAssetRequired = errors.New("impact: asset is required")
	// ErrInvalidDirection is returned when ImpactQuery.Direction is not
	// "upstream" or "downstream".
	ErrInvalidDirection = errors.New("impact: direction must be 'upstream' or 'downstream'")
)

// ImpactQuery defines the parameters for a lineage traversal request.
type ImpactQuery struct {
	// Asset is the starting asset name. Required.
	Asset string
	// Column is nil for asset-level traversal; non-nil triggers column-level
	// traversal using the column_edges table. Aligned with plans 04-07/04-08
	// to avoid type mismatch at compile time.
	Column *string
	// Direction is "upstream" or "downstream".
	Direction string
	// Depth controls how many hops to traverse. Values ≤ 0 default to
	// DefaultDepth (10). Values > MaxDepth (25) return ErrDepthExceeded.
	Depth int
	// AsOf is nil for active-edges-only traversal (WHERE superseded_at IS NULL).
	// Non-nil switches to point-in-time mode (D-15): edges visible at that moment.
	AsOf *time.Time
}

// ImpactNode represents a single node in the traversal result.
type ImpactNode struct {
	// Asset is the asset name.
	Asset string
	// Column is nil for asset-level results; non-nil for column-level results.
	Column *string
	// Depth is the number of hops from the starting asset.
	Depth int
}

// Impact is the return value of Analyze.
type Impact struct {
	// Query is the validated/normalized ImpactQuery used for this traversal.
	Query ImpactQuery
	// Nodes is the ordered list of traversal results, sorted by depth then asset name.
	Nodes []ImpactNode
}

// DB is the read interface accepted by Analyze.
// *pgxpool.Pool satisfies lineageq.DBTX, so callers typically pass their pool.
type DB = lineageq.DBTX

// Analyze runs the recursive CTE traversal for the given ImpactQuery.
//
// Depth validation (D-14, layer 1 of 3):
//   - Depth ≤ 0 → defaulted to DefaultDepth (10)
//   - Depth > MaxDepth (25) → ErrDepthExceeded returned immediately, no DB call
//
// Direction: "upstream" | "downstream" — any other value returns ErrInvalidDirection.
// Asset: empty string returns ErrAssetRequired.
//
// When Column is nil, TraverseAssetLineage is called (asset-level graph).
// When Column is non-nil, TraverseColumnLineage is called (column-level graph).
func Analyze(ctx context.Context, db DB, q ImpactQuery) (Impact, error) {
	if q.Asset == "" {
		return Impact{}, ErrAssetRequired
	}
	if q.Direction != "upstream" && q.Direction != "downstream" {
		return Impact{}, ErrInvalidDirection
	}
	if q.Depth <= 0 {
		q.Depth = DefaultDepth
	}
	if q.Depth > MaxDepth {
		return Impact{}, fmt.Errorf("%w: requested %d", ErrDepthExceeded, q.Depth)
	}

	queries := lineageq.New()

	// Build the pgtype.Timestamptz from *time.Time.
	useAsOf := q.AsOf != nil
	var pgAsOf pgtype.Timestamptz
	if useAsOf {
		pgAsOf = pgtype.Timestamptz{Time: *q.AsOf, Valid: true}
	}

	var nodes []ImpactNode

	if q.Column == nil {
		// Asset-level traversal.
		rows, err := queries.TraverseAssetLineage(ctx, db, lineageq.TraverseAssetLineageParams{
			Direction: q.Direction,
			Asset:     q.Asset,
			UseAsOf:   useAsOf,
			AsOf:      pgAsOf,
			MaxDepth:  int32(q.Depth),
		})
		if err != nil {
			return Impact{}, fmt.Errorf("impact: traverse asset lineage: %w", err)
		}
		nodes = make([]ImpactNode, 0, len(rows))
		for _, r := range rows {
			asset := asString(r.Asset)
			nodes = append(nodes, ImpactNode{
				Asset: asset,
				Depth: int(r.Depth),
			})
		}
	} else {
		// Column-level traversal.
		rows, err := queries.TraverseColumnLineage(ctx, db, lineageq.TraverseColumnLineageParams{
			Direction: q.Direction,
			Asset:     q.Asset,
			ColName:   *q.Column,
			UseAsOf:   useAsOf,
			AsOf:      pgAsOf,
			MaxDepth:  int32(q.Depth),
		})
		if err != nil {
			return Impact{}, fmt.Errorf("impact: traverse column lineage: %w", err)
		}
		nodes = make([]ImpactNode, 0, len(rows))
		for _, r := range rows {
			asset := asString(r.Asset)
			col := asString(r.ColumnName)
			nodes = append(nodes, ImpactNode{
				Asset:  asset,
				Column: &col,
				Depth:  int(r.Depth),
			})
		}
	}

	return Impact{Query: q, Nodes: nodes}, nil
}

// asString converts an interface{} value (returned by sqlc for CASE expression columns)
// to a string. The underlying type from pgx for a text/varchar column is string.
func asString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return fmt.Sprintf("%v", v)
	}
}
