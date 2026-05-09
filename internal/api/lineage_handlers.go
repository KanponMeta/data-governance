package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/kanpon/data-governance/internal/lineage/impact"
)

// assetNameRE validates asset names against the allowed character set (T-04-07-02).
// Defense-in-depth: sqlc parameterized queries already prevent SQL injection,
// but the regex provides an additional layer of input validation.
var assetNameRE = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,256}$`)

// impactHandler returns an http.HandlerFunc for GET /v1/lineage/impact.
// Query parameters:
//
//	?asset=NAME        (required) — starting asset
//	?direction=        (optional, default "downstream") — "upstream" | "downstream"
//	?depth=N           (optional, default 10) — hops; must be ≤ 25 (D-14, T-04-07-01)
//	?column=COL        (optional) — column-level traversal
//	?as_of=RFC3339     (optional) — point-in-time mode (D-15)
func impactHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asset := r.URL.Query().Get("asset")
		if asset == "" {
			writeProblemJSON(w, http.StatusBadRequest, "asset_required", "?asset=NAME is required")
			return
		}
		// T-04-07-02: validate asset name against allowed character set.
		if !assetNameRE.MatchString(asset) {
			writeProblemJSON(w, http.StatusBadRequest, "invalid_asset_name",
				`asset name must match ^[a-zA-Z0-9_.\-]{1,256}$`)
			return
		}

		direction := r.URL.Query().Get("direction")
		if direction == "" {
			direction = "downstream"
		}
		if direction != "upstream" && direction != "downstream" {
			writeProblemJSON(w, http.StatusBadRequest, "invalid_direction",
				"direction must be 'upstream' or 'downstream'")
			return
		}

		depth := impact.DefaultDepth
		if d := r.URL.Query().Get("depth"); d != "" {
			v, err := strconv.Atoi(d)
			if err != nil {
				writeProblemJSON(w, http.StatusBadRequest, "invalid_depth", "depth must be a positive integer")
				return
			}
			depth = v
		}
		// T-04-07-01: handler-level depth cap (D-14 layer 3 of 3).
		if depth > impact.MaxDepth {
			writeProblemJSON(w, http.StatusBadRequest, "depth_exceeded",
				fmt.Sprintf("depth must be <= %d", impact.MaxDepth))
			return
		}

		q := impact.ImpactQuery{
			Asset:     asset,
			Direction: direction,
			Depth:     depth,
		}
		if col := r.URL.Query().Get("column"); col != "" {
			q.Column = &col
		}
		if asOf := r.URL.Query().Get("as_of"); asOf != "" {
			parsed, err := time.Parse(time.RFC3339, asOf)
			if err != nil {
				writeProblemJSON(w, http.StatusBadRequest, "invalid_as_of", "as_of must be RFC3339")
				return
			}
			q.AsOf = &parsed
		}

		if deps.LineageDB == nil {
			writeProblemJSON(w, http.StatusServiceUnavailable, "lineage_unavailable",
				"lineage database connection not configured")
			return
		}
		result, err := impact.Analyze(r.Context(), deps.LineageDB, q)
		if err != nil {
			if errors.Is(err, impact.ErrDepthExceeded) {
				writeProblemJSON(w, http.StatusBadRequest, "depth_exceeded",
					fmt.Sprintf("depth must be <= %d", impact.MaxDepth))
				return
			}
			if errors.Is(err, impact.ErrInvalidDirection) {
				writeProblemJSON(w, http.StatusBadRequest, "invalid_direction", err.Error())
				return
			}
			writeProblemJSON(w, http.StatusInternalServerError, "impact_failed", err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, result)
	}
}

// exportLineageHandler returns an http.HandlerFunc for GET /v1/lineage/export.
// Query parameters:
//
//	?asset=NAME           (required)
//	?format=openlineage   (required; only "openlineage" is supported now)
//	?since=RFC3339        (optional) — filter runs with finished_at >= since
func exportLineageHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		asset := r.URL.Query().Get("asset")
		if asset == "" {
			writeProblemJSON(w, http.StatusBadRequest, "asset_required", "?asset=NAME is required")
			return
		}

		format := r.URL.Query().Get("format")
		if format != "openlineage" {
			writeProblemJSON(w, http.StatusBadRequest, "unsupported_format",
				"format must be 'openlineage' (other formats reserved for future use)")
			return
		}

		var since time.Time
		if s := r.URL.Query().Get("since"); s != "" {
			parsed, err := time.Parse(time.RFC3339, s)
			if err != nil {
				writeProblemJSON(w, http.StatusBadRequest, "invalid_since",
					"since must be RFC3339 (e.g. 2026-01-01T00:00:00Z)")
				return
			}
			since = parsed
		}

		events, err := deps.OLTranslator.TranslateAsset(r.Context(), asset, since)
		if err != nil {
			writeProblemJSON(w, http.StatusInternalServerError, "export_failed", err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, events)
	}
}
