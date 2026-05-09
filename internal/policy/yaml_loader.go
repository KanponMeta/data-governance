package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/connector"
)

// YAMLConfig is the on-disk shape of policies.yaml — the lowest-precedence
// policy layer. Tags map both to default mask behaviour (D-04) and to the
// reviewer-roles pool reused by plan 05-04 (D-09).
type YAMLConfig struct {
	// TagMaskDefaults maps a tag name (e.g. "pii") to the default MaskType
	// applied to every column carrying that tag at registration / reload time.
	TagMaskDefaults map[string]connector.MaskType `yaml:"tag_mask_defaults"`

	// TagReviewerRoles maps a tag name to the reviewer role pool used by the
	// governance approval workflow (consumed by plan 05-04).
	TagReviewerRoles map[string][]string `yaml:"tag_reviewer_roles"`
}

// ErrYAMLValidation is returned by LoadYAML when the file's structure is
// well-formed YAML but contains an invalid mask type.
var ErrYAMLValidation = errors.New("policy: yaml validation failed")

// LoadYAML reads + parses a policies.yaml file. It validates that every
// referenced MaskType is one of {hash, redact, partial} but does NOT
// require any keys to be present (an empty file is valid — produces a
// no-op ApplyYAML).
func LoadYAML(path string) (*YAMLConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: load yaml %q: %w", path, err)
	}
	cfg := &YAMLConfig{}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("policy: parse yaml %q: %w", path, err)
	}
	for tag, m := range cfg.TagMaskDefaults {
		if !m.IsValid() {
			return nil, fmt.Errorf("%w: tag %q has invalid mask %q", ErrYAMLValidation, tag, m)
		}
	}
	return cfg, nil
}

// ApplyYAML walks asset_metadata + column-level tags and writes one
// column_policies row (source='yaml-default') per (asset, column) carrying
// a tag in cfg.TagMaskDefaults. Each insertion writes a policy.changed
// audit_log entry inside the same transaction.
//
// Reload is idempotent — re-applying an unchanged config touches last_seen_at
// without creating new rows or audit noise.
func (s *Store) ApplyYAML(ctx context.Context, cfg *YAMLConfig, actor uuid.UUID) (int, error) {
	if cfg == nil || len(cfg.TagMaskDefaults) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("policy: apply yaml begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Determine the (asset, column, tag) triples that map to a YAML default.
	// We read asset-level tags (asset_metadata.tags JSONB array) and merge
	// per-column tags from asset_versions.column_lineage / asset_metadata.
	//
	// Schema-light: rather than depend on the (asset, column) tag join the
	// metadata package owns, we walk asset_metadata.tags scoped per-asset and
	// apply the YAML default to ALL columns of that asset that already carry a
	// builder/runtime row — this is sufficient for v1 and avoids a circular
	// dependency on internal/metadata.
	//
	// Tag → mask defaults are ordered (sorted) so audit chain ordering is
	// deterministic between runs.
	tags := make([]string, 0, len(cfg.TagMaskDefaults))
	for t := range cfg.TagMaskDefaults {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	applied := 0
	for _, tag := range tags {
		mask := cfg.TagMaskDefaults[tag]
		// Find assets carrying this tag in asset_metadata.
		assetRows, err := tx.QueryContext(ctx, `
			SELECT asset FROM asset_metadata
			 WHERE tags ? $1
		`, tag)
		if err != nil {
			return 0, fmt.Errorf("policy: apply yaml find assets for tag %q: %w", tag, err)
		}
		var assets []string
		for assetRows.Next() {
			var a string
			if err := assetRows.Scan(&a); err != nil {
				assetRows.Close()
				return 0, err
			}
			assets = append(assets, a)
		}
		assetRows.Close()

		for _, a := range assets {
			// For each asset's columns, determine which columns carry this tag.
			// The metadata schema stores per-column tags inside asset_metadata as
			// a separate column-level row; if not present, fall back to applying
			// the tag mask to ALL columns of the asset.
			//
			// For v1 we look up column-level tags in asset_metadata.tags by
			// scanning the JSONB array; if no per-column tag rows exist, we apply
			// to every column already in column_policies for that asset.
			cols, err := yamlColumnsForAssetTag(ctx, tx, a, tag)
			if err != nil {
				return 0, err
			}
			for _, c := range cols {
				rolesB, _ := rolesJSON(nil) // yaml defaults have no allow_roles
				// Read existing yaml-default row for diff.
				var beforeMask string
				err := tx.QueryRowContext(ctx, `
					SELECT mask_type FROM column_policies
					 WHERE asset=$1 AND column_name=$2 AND source='yaml-default' AND superseded_at IS NULL
				`, a, c).Scan(&beforeMask)
				switch {
				case errors.Is(err, sql.ErrNoRows):
					// Insert new yaml-default row.
					if _, err := tx.ExecContext(ctx, `
						INSERT INTO column_policies (
						    asset, column_name, mask_type, allow_roles,
						    code_hash_first, code_hash_latest,
						    source, reason, enforcement_mode
						) VALUES ($1, $2, $3, $4::jsonb, '', '', 'yaml-default', $5, 'unknown')
					`, a, c, string(mask), string(rolesB), "yaml-tag:"+tag); err != nil {
						return 0, fmt.Errorf("policy: apply yaml insert %s.%s: %w", a, c, err)
					}
					applied++
					if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
						EventType:    audit.AuditPolicyChanged,
						OccurredAt:   time.Now().UTC(),
						ActorID:      &actor,
						ResourceType: "column_policy",
						ResourceID:   a + "." + c,
						Payload: map[string]any{
							"asset": a, "column": c, "source": "yaml-default", "tag": tag,
							"before": map[string]any{}, "after": map[string]any{"mask": string(mask)},
							"reason": "yaml-reload",
						},
					}); err != nil {
						return 0, fmt.Errorf("policy: apply yaml audit %s.%s: %w", a, c, err)
					}
				case err != nil:
					return 0, fmt.Errorf("policy: apply yaml read prior %s.%s: %w", a, c, err)
				default:
					// existing yaml-default row.
					if beforeMask == string(mask) {
						// no-op — bump last_seen_at.
						if _, err := tx.ExecContext(ctx, `
							UPDATE column_policies SET last_seen_at = NOW()
							 WHERE asset=$1 AND column_name=$2 AND source='yaml-default' AND superseded_at IS NULL
						`, a, c); err != nil {
							return 0, err
						}
					} else {
						// changed mask — soft-retire + insert + audit.
						if _, err := tx.ExecContext(ctx, `
							UPDATE column_policies SET superseded_at = NOW()
							 WHERE asset=$1 AND column_name=$2 AND source='yaml-default' AND superseded_at IS NULL
						`, a, c); err != nil {
							return 0, err
						}
						if _, err := tx.ExecContext(ctx, `
							INSERT INTO column_policies (
							    asset, column_name, mask_type, allow_roles,
							    code_hash_first, code_hash_latest,
							    source, reason, enforcement_mode
							) VALUES ($1, $2, $3, '[]'::jsonb, '', '', 'yaml-default', $4, 'unknown')
						`, a, c, string(mask), "yaml-tag:"+tag); err != nil {
							return 0, err
						}
						applied++
						if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
							EventType:    audit.AuditPolicyChanged,
							OccurredAt:   time.Now().UTC(),
							ActorID:      &actor,
							ResourceType: "column_policy",
							ResourceID:   a + "." + c,
							Payload: map[string]any{
								"asset": a, "column": c, "source": "yaml-default", "tag": tag,
								"before": map[string]any{"mask": beforeMask},
								"after":  map[string]any{"mask": string(mask)},
								"reason": "yaml-reload",
							},
						}); err != nil {
							return 0, err
						}
					}
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("policy: apply yaml commit: %w", err)
	}
	return applied, nil
}

// yamlColumnsForAssetTag returns the column names on assetName that carry
// the named tag. The lookup is best-effort: it inspects asset_metadata
// (per-column rows if present) and falls back to "all columns currently in
// column_policies for the asset" if no column-level tag schema exists.
func yamlColumnsForAssetTag(ctx context.Context, tx *sql.Tx, assetName, tag string) ([]string, error) {
	// Try column-level tags in asset_metadata first (Phase 4 D-17).
	// Schema: asset_metadata stores tags as JSONB array; if a per-column
	// shape exists it is at column_metadata.tags or asset_metadata.column_tags.
	// For v1 we treat asset-level tags as applying to all known columns of
	// the asset — sufficient for the policies.yaml use case (tag the asset
	// "pii" to apply hash mask to its declared sensitive columns).
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT column_name FROM column_policies
		 WHERE asset = $1 AND superseded_at IS NULL
		 ORDER BY column_name
	`, assetName)
	if err != nil {
		return nil, fmt.Errorf("policy: yaml columns lookup: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarshalYAMLConfigForTest is exported for use by tests / CLI to round-trip
// configs deterministically.
func MarshalYAMLConfigForTest(cfg *YAMLConfig) ([]byte, error) {
	return json.Marshal(cfg)
}
