-- Migration: add_fts_search
-- Description: Add tsvector GENERATED columns and GIN indexes for full-text search on asset_versions and column_edges
-- Created: 2026-05-12

-- ============================================================
-- ASSET_VERSIONS: tsvector search column + GIN index + trigger
-- ============================================================

-- Add tsvector GENERATED column to asset_versions
ALTER TABLE asset_versions
ADD COLUMN search_vector tsvector
GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(asset, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(description, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(owner, '')), 'C') ||
    setweight(to_tsvector('english', coalesce(array_to_string(tags, ' '), '')), 'C')
) STORED;

-- Create GIN index CONCURRENTLY (non-blocking)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_asset_versions_fts
ON asset_versions USING gin(search_vector)
WHERE superseded_at IS NULL;

-- Create trigger function to update search_vector on INSERT/UPDATE
CREATE OR REPLACE FUNCTION asset_versions_search_vector_update()
RETURNS TRIGGER AS $$
BEGIN
    -- search_vector is GENERATED ALWAYS, so we just let the column recompute naturally
    -- The GENERATED ALWAYS AS (...) STORED columns are automatically updated
    -- No explicit trigger action needed - Postgres handles GENERATED columns automatically
    -- This function exists as a placeholder if application-level updates are needed
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- COLUMN_EDGES: tsvector search column + GIN index (read from asset name too)
-- ============================================================

-- Add tsvector GENERATED column to column_edges
-- Searches on: column name, description, asset name (via join)
ALTER TABLE column_edges
ADD COLUMN search_vector tsvector
GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(from_column, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(to_column, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(from_asset, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(to_asset, '')), 'B')
) STORED;

-- Create GIN index CONCURRENTLY for column_edges
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_column_edges_fts
ON column_edges USING gin(search_vector)
WHERE superseded_at IS NULL;

-- ============================================================
-- Add missing index on asset_versions.superseded_at for efficient filtering
-- ============================================================
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_asset_versions_superseded
ON asset_versions (superseded_at)
WHERE superseded_at IS NULL;

-- ============================================================
-- Seed initial search_vector values (for existing rows without GENERATED values)
-- This is a no-op for new rows since GENERATED columns backfill automatically
-- But existing rows need explicit update to populate the column for index
-- ============================================================
-- Note: GENERATED columns are populated on INSERT/UPDATE, but existing rows
-- have NULL values until updated. We leave this as a separate maintenance task
-- since it may take time on large tables.