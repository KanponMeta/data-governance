-- internal/lineage/queries/lineage.sql

-- name: TraverseAssetLineage :many
-- TraverseAssetLineage walks the asset_edges graph from @asset in the requested
-- direction (downstream | upstream), returning each visited (asset, depth) up to
-- @max_depth. Caller MUST validate @max_depth <= 25 before calling (D-14).
WITH RECURSIVE lineage AS (
    -- base: assets directly adjacent to @asset
    SELECT
        CASE WHEN @direction::text = 'downstream' THEN to_asset
                                                    ELSE from_asset END AS asset,
        1 AS depth,
        ARRAY[CASE WHEN @direction::text = 'downstream' THEN from_asset
                                                          ELSE to_asset END,
              CASE WHEN @direction::text = 'downstream' THEN to_asset
                                                          ELSE from_asset END]::text[] AS path
    FROM asset_edges
    WHERE
        CASE WHEN @direction::text = 'downstream' THEN from_asset = @asset::text
                                                    ELSE to_asset   = @asset::text END
        AND CASE WHEN @use_as_of::bool
                 THEN first_seen_at <= @as_of::timestamptz
                      AND (superseded_at IS NULL OR superseded_at > @as_of::timestamptz)
                 ELSE superseded_at IS NULL END

    UNION ALL

    SELECT
        CASE WHEN @direction::text = 'downstream' THEN e.to_asset
                                                    ELSE e.from_asset END,
        l.depth + 1,
        l.path || ARRAY[CASE WHEN @direction::text = 'downstream' THEN e.to_asset
                                                                    ELSE e.from_asset END]::text[]
    FROM asset_edges e
    JOIN lineage l ON
        CASE WHEN @direction::text = 'downstream' THEN e.from_asset = l.asset
                                                    ELSE e.to_asset   = l.asset END
    WHERE
        l.depth < LEAST(@max_depth::int, 25)  -- D-14 SQL-level hard ceiling (defense in depth; impact.Analyze is layer 1, this CTE bound is layer 2)
        AND CASE WHEN @use_as_of::bool
                 THEN e.first_seen_at <= @as_of::timestamptz
                      AND (e.superseded_at IS NULL OR e.superseded_at > @as_of::timestamptz)
                 ELSE e.superseded_at IS NULL END
        AND NOT (l.path @> ARRAY[
            CASE WHEN @direction::text = 'downstream' THEN e.to_asset
                                                        ELSE e.from_asset END
        ]::text[])  -- cycle guard (Pitfall 2)
)
SELECT DISTINCT asset, depth FROM lineage ORDER BY depth, asset;

-- name: TraverseColumnLineage :many
-- Same template but on column_edges, with (asset, column) tuples in the path.
WITH RECURSIVE lineage AS (
    SELECT
        CASE WHEN @direction::text = 'downstream' THEN to_asset    ELSE from_asset    END AS asset,
        CASE WHEN @direction::text = 'downstream' THEN to_column   ELSE from_column   END AS column_name,
        1 AS depth,
        ARRAY[
            CASE WHEN @direction::text = 'downstream' THEN from_asset || '.' || from_column
                                                        ELSE to_asset   || '.' || to_column   END,
            CASE WHEN @direction::text = 'downstream' THEN to_asset   || '.' || to_column
                                                        ELSE from_asset || '.' || from_column END
        ]::text[] AS path
    FROM column_edges
    WHERE
        CASE WHEN @direction::text = 'downstream'
             THEN from_asset = @asset::text AND from_column = @col_name::text
             ELSE to_asset   = @asset::text AND to_column   = @col_name::text END
        AND CASE WHEN @use_as_of::bool
                 THEN first_seen_at <= @as_of::timestamptz
                      AND (superseded_at IS NULL OR superseded_at > @as_of::timestamptz)
                 ELSE superseded_at IS NULL END

    UNION ALL

    SELECT
        CASE WHEN @direction::text = 'downstream' THEN e.to_asset    ELSE e.from_asset    END,
        CASE WHEN @direction::text = 'downstream' THEN e.to_column   ELSE e.from_column   END,
        l.depth + 1,
        l.path || ARRAY[
            CASE WHEN @direction::text = 'downstream' THEN e.to_asset || '.' || e.to_column
                                                        ELSE e.from_asset || '.' || e.from_column END
        ]::text[]
    FROM column_edges e
    JOIN lineage l ON
        CASE WHEN @direction::text = 'downstream'
             THEN e.from_asset = l.asset AND e.from_column = l.column_name
             ELSE e.to_asset   = l.asset AND e.to_column   = l.column_name END
    WHERE
        l.depth < LEAST(@max_depth::int, 25)  -- D-14 SQL-level hard ceiling (defense in depth)
        AND CASE WHEN @use_as_of::bool
                 THEN e.first_seen_at <= @as_of::timestamptz
                      AND (e.superseded_at IS NULL OR e.superseded_at > @as_of::timestamptz)
                 ELSE e.superseded_at IS NULL END
        AND NOT (l.path @> ARRAY[
            CASE WHEN @direction::text = 'downstream' THEN e.to_asset || '.' || e.to_column
                                                        ELSE e.from_asset || '.' || e.from_column END
        ]::text[])
)
SELECT DISTINCT asset, column_name, depth FROM lineage ORDER BY depth, asset, column_name;
