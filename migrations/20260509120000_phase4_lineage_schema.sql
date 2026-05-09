-- Phase 4 lineage + schema migration (Wave 1 plan 04-02 fills this in).
-- This file is intentionally empty in Wave 0; the filename slot is reserved so
-- Atlas roundtrip + lint checks see a known migration timestamp before plan 04-02
-- adds CREATE TABLE statements for asset_edges, column_edges, schema_versions,
-- schema_changes, asset_versions, asset_metadata.
--
-- Wave 0 reservation: see .planning/phases/04-schema/04-VALIDATION.md "Wave 0 Requirements".
SELECT 1; -- atlas migrate lint requires at least one statement; SELECT is a no-op DDL
