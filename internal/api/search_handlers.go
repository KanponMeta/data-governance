package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SearchResult represents a single search result item.
type SearchResult struct {
	Type        string   `json:"type"` // "asset", "column"
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Owner       string   `json:"owner"`
	Tags        []string `json:"tags"`
	AssetName   string   `json:"asset_name"` // for columns: parent asset
	Highlight   string   `json:"highlight"`
}

// SearchResponse is GET /v1/catalog/search response.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
	Page    int            `json:"page"`
	Query   string         `json:"query"`
	Tags    []string       `json:"tags"`    // all available tags for filter chips
	Owners  []string       `json:"owners"`  // all available owners for dropdown
}

// searchHandler handles GET /v1/catalog/search.
// Query params: q (optional), type (optional: asset|column|table), tag (optional), owner (optional), page (optional, default 1).
// When q is empty but tag/owner is set, performs browse with filters (UI-03).
// When q is set, performs FTS search with filters (META-04).
func searchHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		tag := strings.TrimSpace(r.URL.Query().Get("tag"))
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		searchType := r.URL.Query().Get("type") // asset, column, or empty (all)
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			if v := parsePositiveInt(p); v > 0 {
				page = v
			}
		}
		pageSize := 20
		offset := (page - 1) * pageSize

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		results, total, tags, owners, err := performSearch(ctx, deps, query, tag, owner, searchType, pageSize, offset)
		if err != nil {
			InternalServerError(w, err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, SearchResponse{
			Results: results,
			Total:   total,
			Page:    page,
			Query:   query,
			Tags:    tags,
			Owners:  owners,
		})
	}
}

// performSearch executes FTS query with optional tag/owner filters.
// When query is empty: browse mode (UI-03) - return all assets/columns matching tag/owner filters
// When query is set: search mode (META-04) - FTS search with tag/owner filters applied
func performSearch(ctx context.Context, deps Deps, query, tag, owner, searchType string, limit, offset int) ([]SearchResult, int, []string, []string, error) {
	// Validate search type
	if searchType != "" && searchType != "asset" && searchType != "column" && searchType != "table" {
		return nil, 0, nil, nil, errors.New("invalid search type: must be empty, asset, column, or table")
	}

	// Get pgxpool from LineageDB
	var pool *pgxpool.Pool
	if deps.LineageDB != nil {
		pool, _ = deps.LineageDB.(*pgxpool.Pool)
	}
	if pool == nil {
		return nil, 0, nil, nil, errors.New("database connection not available")
	}

	// Build the base query parts
	var args []any
	argIdx := 1

	// Query text for FTS (empty query = browse mode)
	queryPattern := ""
	if query != "" {
		queryPattern = fmt.Sprintf("plainto_tsquery('english', $%d)", argIdx)
		args = append(args, query)
		argIdx++
	}

	// Tag filter
	tagPattern := ""
	if tag != "" {
		tagPattern = fmt.Sprintf("tags @> ARRAY[$%d]", argIdx)
		args = append(args, tag)
		argIdx++
	}

	// Owner filter
	ownerPattern := ""
	if owner != "" {
		ownerPattern = fmt.Sprintf("owner = $%d", argIdx)
		args = append(args, owner)
		argIdx++
	}

	// Build WHERE conditions for asset_versions subquery
	avConditions := []string{"superseded_at IS NULL"}
	if queryPattern != "" {
		avConditions = append(avConditions, fmt.Sprintf("(search_vector @@ %s OR $1 = '')", queryPattern))
	}
	if tagPattern != "" {
		avConditions = append(avConditions, tagPattern)
	}
	if ownerPattern != "" {
		avConditions = append(avConditions, ownerPattern)
	}

	// Build WHERE conditions for column_edges subquery
	ceConditions := []string{"ce.superseded_at IS NULL", "av.superseded_at IS NULL"}
	if queryPattern != "" {
		ceConditions = append(ceConditions, fmt.Sprintf("(ce.search_vector @@ %s OR $1 = '')", queryPattern))
	}
	if tagPattern != "" {
		ceConditions = append(ceConditions, tagPattern)
	}
	if ownerPattern != "" {
		ceConditions = append(ceConditions, ownerPattern)
	}

	// Determine if we should search assets, columns, or both
	searchAssets := searchType == "" || searchType == "asset" || searchType == "table"
	searchColumns := searchType == "" || searchType == "column"

	// Build the main query
	var mainSQL string
	if searchAssets && searchColumns {
		// Both - use UNION ALL
		highlightExpr := "ts_headline('english', asset || ' ' || coalesce(description, ''), coalesce(NULLIF(plainto_tsquery('english', coalesce($1, ''), ''), plainto_tsquery('english', '')), 'english'::regconfig), 'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20')"
		if query == "" {
			highlightExpr = "coalesce(description, '')"
		}

		mainSQL = fmt.Sprintf(`
			SELECT type, name, description, owner, tags, asset_name, highlight, rank FROM (
				SELECT 'asset' as type,
					   asset as name,
					   description,
					   owner,
					   tags,
					   NULL::text as asset_name,
					   %s as highlight,
					   ts_rank(search_vector, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig)) as rank
				FROM asset_versions av
				WHERE %s
				UNION ALL
				SELECT 'column' as type,
					   ce.from_column as name,
					   ce.to_column as description, -- use destination column as "description" for display
					   av.owner,
					   av.tags,
					   ce.from_asset as asset_name,
					   ts_headline('english', ce.from_column || ' ' || ce.to_column, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig), 'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20') as highlight,
					   ts_rank(ce.search_vector, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig)) as rank
				FROM column_edges ce
				JOIN asset_versions av ON ce.from_asset = av.asset AND av.superseded_at IS NULL
				WHERE %s
			) combined
			ORDER BY rank DESC, name ASC
			LIMIT $%d OFFSET $%d`,
			highlightExpr,
			strings.Join(avConditions, " AND "),
			strings.Join(ceConditions, " AND "),
			argIdx, argIdx+1)
	} else if searchAssets {
		highlightExpr := "ts_headline('english', asset || ' ' || coalesce(description, ''), coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig), 'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20')"
		if query == "" {
			highlightExpr = "coalesce(description, '')"
		}
		mainSQL = fmt.Sprintf(`
			SELECT 'asset' as type,
				   asset as name,
				   description,
				   owner,
				   tags,
				   NULL::text as asset_name,
				   %s as highlight,
				   ts_rank(search_vector, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig)) as rank
			FROM asset_versions av
			WHERE %s
			ORDER BY rank DESC, name ASC
			LIMIT $%d OFFSET $%d`,
			highlightExpr,
			strings.Join(avConditions, " AND "),
			argIdx, argIdx+1)
	} else {
		// columns only
		mainSQL = fmt.Sprintf(`
			SELECT 'column' as type,
				   ce.from_column as name,
				   ce.to_column as description,
				   av.owner,
				   av.tags,
				   ce.from_asset as asset_name,
				   ts_headline('english', ce.from_column || ' ' || ce.to_column, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig), 'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20') as highlight,
				   ts_rank(ce.search_vector, coalesce(NULLIF(plainto_tsquery('english', coalesce($1, '')), plainto_tsquery('english', '')), 'english'::regconfig)) as rank
			FROM column_edges ce
			JOIN asset_versions av ON ce.from_asset = av.asset AND av.superseded_at IS NULL
			WHERE %s
			ORDER BY rank DESC, name ASC
			LIMIT $%d OFFSET $%d`,
			strings.Join(ceConditions, " AND "),
			argIdx, argIdx+1)
	}

	// Build count query (without limit/offset)
	var countSQL string
	if searchAssets && searchColumns {
		countSQL = fmt.Sprintf(`
			SELECT COUNT(*) FROM (
				SELECT 1 FROM asset_versions av WHERE %s
				UNION ALL
				SELECT 1 FROM column_edges ce
				JOIN asset_versions av ON ce.from_asset = av.asset AND av.superseded_at IS NULL
				WHERE %s
			) combined`,
			strings.Join(avConditions, " AND "),
			strings.Join(ceConditions, " AND "))
	} else if searchAssets {
		countSQL = fmt.Sprintf(`SELECT COUNT(*) FROM asset_versions av WHERE %s`, strings.Join(avConditions, " AND "))
	} else {
		countSQL = fmt.Sprintf(`
			SELECT COUNT(*) FROM column_edges ce
			JOIN asset_versions av ON ce.from_asset = av.asset AND av.superseded_at IS NULL
			WHERE %s`, strings.Join(ceConditions, " AND "))
	}

	// Add limit/offset args
	args = append(args, limit, offset)

	// Execute count query
	var total int
	countArgs := args[:len(args)-2] // remove limit/offset for count
	err := pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("count query failed: %w", err)
	}

	// Execute main query
	rows, err := pool.Query(ctx, mainSQL, args...)
	if err != nil {
		return nil, 0, nil, nil, fmt.Errorf("search query failed: %w", err)
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		var tags []string
		var rank float64
		err := rows.Scan(&r.Type, &r.Name, &r.Description, &r.Owner, &tags, &r.AssetName, &r.Highlight, &rank)
		if err != nil {
			return nil, 0, nil, nil, fmt.Errorf("scan row failed: %w", err)
		}
		r.Tags = tags
		results = append(results, r)
	}

	// Fetch all available tags and owners for filter UI (UI-03)
	availableTags, _ := fetchAvailableTags(ctx, pool)
	availableOwners, _ := fetchAvailableOwners(ctx, pool)

	return results, total, availableTags, availableOwners, nil
}

func fetchAvailableTags(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT unnest(tags) as tag
		FROM asset_versions
		WHERE superseded_at IS NULL AND tags IS NOT NULL AND array_length(tags, 1) > 0
		ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			continue
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func fetchAvailableOwners(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT owner
		FROM asset_versions
		WHERE superseded_at IS NULL AND owner IS NOT NULL AND owner != ''
		ORDER BY owner`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	owners := []string{}
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			continue
		}
		owners = append(owners, owner)
	}
	return owners, nil
}

func parsePositiveInt(s string) int {
	var v int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + int(c-'0')
	}
	return v
}