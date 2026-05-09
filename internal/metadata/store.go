// Package metadata provides the asset metadata read/write store (D-17).
// The store is INSERT-only for asset_metadata rows; reads apply COALESCE logic
// to merge the runtime override with the code-default from asset_versions.
package metadata

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/storage/ent/assetmetadata"
	"github.com/kanpon/data-governance/internal/storage/ent/assetversion"
)

// Effective is the resolved metadata for an asset or column.
type Effective struct {
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// Resolution is the full GET /v1/assets/:name/metadata response body.
// It surfaces all three layers so callers can understand where values come from.
type Resolution struct {
	CodeDefault     Effective  `json:"code_default"`
	RuntimeOverride *Effective `json:"runtime_override,omitempty"` // nil if no override row
	Effective       Effective  `json:"effective"`                  // COALESCE result
}

// PutInput is the input to Store.Put.
type PutInput struct {
	Asset       string
	Column      *string   // nil = asset-level
	Description *string   // nil = leave runtime untouched (not overridden here)
	Owner       *string
	Tags        *[]string
	SetBy       uuid.UUID
	Merge       bool // merge tags with existing runtime tags if true; replace if false
}

// Store is the concrete metadata store backed by an ent.Client.
type Store struct {
	ent *ent.Client
}

// NewStore creates a Store that reads/writes through c.
func NewStore(c *ent.Client) *Store {
	return &Store{ent: c}
}

// Get returns the Resolution for (asset, column).
// column nil = asset-level metadata.
// If there is no asset_versions row, CodeDefault is zero-value.
// If there is no asset_metadata row, RuntimeOverride is nil.
func (s *Store) Get(ctx context.Context, asset string, column *string) (Resolution, error) {
	// 1. Load latest asset_versions row for code_default.
	var cd Effective
	avRows, err := s.ent.AssetVersion.Query().
		Where(assetversion.AssetEQ(asset)).
		Order(assetversion.ByCreatedAt(sql.OrderDesc())).
		Limit(1).
		All(ctx)
	if err != nil {
		return Resolution{}, err
	}
	if len(avRows) > 0 {
		av := avRows[0]
		cd.Description = av.Description
		cd.Owner = av.Owner
		cd.Tags = av.Tags
	}

	// 2. Load latest asset_metadata row for runtime override.
	q := s.ent.AssetMetadata.Query().
		Where(assetmetadata.AssetEQ(asset)).
		Order(assetmetadata.BySetAt(sql.OrderDesc())).
		Limit(1)
	if column == nil {
		q = q.Where(assetmetadata.ColumnNameIsNil())
	} else {
		q = q.Where(assetmetadata.ColumnNameEQ(*column))
	}
	amRows, err := q.All(ctx)
	if err != nil {
		return Resolution{}, err
	}

	var rt *Effective
	if len(amRows) > 0 {
		am := amRows[0]
		rt = &Effective{
			Description: am.Description,
			Owner:       am.Owner,
			Tags:        am.Tags,
		}
	}

	// 3. COALESCE: runtime override wins field-by-field over code_default.
	eff := coalesce(cd, rt)

	return Resolution{
		CodeDefault:     cd,
		RuntimeOverride: rt,
		Effective:       eff,
	}, nil
}

// Put inserts a new asset_metadata row (INSERT-only — no UPDATE).
// It reads the current effective state to merge tags when in.Merge is true,
// then inserts the row and returns the new Effective.
func (s *Store) Put(ctx context.Context, in PutInput) (Effective, error) {
	// 1. Load current resolution for merge support.
	cur, err := s.Get(ctx, in.Asset, in.Column)
	if err != nil {
		return Effective{}, err
	}

	// 2. Compute new tags.
	var newTags []string
	if in.Tags != nil {
		if in.Merge {
			// Union: start with current effective tags, add new ones.
			existing := cur.Effective.Tags
			merged := make([]string, 0, len(existing)+len(*in.Tags))
			seen := map[string]bool{}
			for _, t := range existing {
				if !seen[t] {
					seen[t] = true
					merged = append(merged, t)
				}
			}
			for _, t := range *in.Tags {
				if !seen[t] {
					seen[t] = true
					merged = append(merged, t)
				}
			}
			newTags = merged
		} else {
			newTags = *in.Tags
		}
	} else {
		// No tags provided — carry forward effective tags.
		newTags = cur.Effective.Tags
	}

	// 3. Compute new description and owner.
	newDesc := cur.Effective.Description
	if in.Description != nil {
		newDesc = *in.Description
	}
	newOwner := cur.Effective.Owner
	if in.Owner != nil {
		newOwner = *in.Owner
	}

	// 4. INSERT asset_metadata row.
	creator := s.ent.AssetMetadata.Create().
		SetAsset(in.Asset).
		SetNillableColumnName(in.Column).
		SetDescription(newDesc).
		SetOwner(newOwner).
		SetTags(newTags).
		SetSetBy(in.SetBy).
		SetSetAt(time.Now().UTC())
	if _, err := creator.Save(ctx); err != nil {
		return Effective{}, err
	}

	return Effective{
		Description: newDesc,
		Owner:       newOwner,
		Tags:        newTags,
	}, nil
}

// coalesce returns the effective metadata by preferring non-empty runtime fields
// over code-default fields.
func coalesce(cd Effective, rt *Effective) Effective {
	eff := cd
	if rt == nil {
		return eff
	}
	if rt.Description != "" {
		eff.Description = rt.Description
	}
	if rt.Owner != "" {
		eff.Owner = rt.Owner
	}
	if len(rt.Tags) > 0 {
		eff.Tags = rt.Tags
	}
	return eff
}
