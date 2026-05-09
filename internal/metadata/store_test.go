package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// openTestClient creates an in-memory SQLite ent client for testing.
func openTestClient(t *testing.T) *Store {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	return NewStore(client)
}

func TestStore_Get_NoOverride(t *testing.T) {
	ctx := context.Background()
	store := openTestClient(t)

	// Seed an asset_versions row — code_default.
	desc := "the desc"
	owner := "team-a"
	tags := []string{"tag1", "tag2"}
	_, err := store.ent.AssetVersion.Create().
		SetAsset("asset_a").
		SetCodeHash("hash1").
		SetDescription(desc).
		SetOwner(owner).
		SetTags(tags).
		SetCreatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed AssetVersion: %v", err)
	}

	res, err := store.Get(ctx, "asset_a", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.RuntimeOverride != nil {
		t.Error("expected RuntimeOverride to be nil when no asset_metadata row")
	}
	if res.CodeDefault.Description != desc {
		t.Errorf("CodeDefault.Description = %q, want %q", res.CodeDefault.Description, desc)
	}
	if res.CodeDefault.Owner != owner {
		t.Errorf("CodeDefault.Owner = %q, want %q", res.CodeDefault.Owner, owner)
	}
	if res.Effective.Description != desc {
		t.Errorf("Effective.Description = %q, want %q", res.Effective.Description, desc)
	}
}

func TestStore_Get_WithOverride(t *testing.T) {
	ctx := context.Background()
	store := openTestClient(t)

	// Seed asset_version (code_default).
	_, err := store.ent.AssetVersion.Create().
		SetAsset("asset_b").
		SetCodeHash("hash1").
		SetDescription("code desc").
		SetOwner("code-owner").
		SetTags([]string{"code-tag"}).
		SetCreatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed AssetVersion: %v", err)
	}

	// Seed asset_metadata (runtime override).
	setBy := uuid.New()
	rtDesc := "runtime desc"
	rtOwner := "runtime-owner"
	rtTags := []string{"rt-tag"}
	_, err = store.ent.AssetMetadata.Create().
		SetAsset("asset_b").
		SetDescription(rtDesc).
		SetOwner(rtOwner).
		SetTags(rtTags).
		SetSetBy(setBy).
		SetSetAt(time.Now()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed AssetMetadata: %v", err)
	}

	res, err := store.Get(ctx, "asset_b", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.RuntimeOverride == nil {
		t.Fatal("expected RuntimeOverride to be non-nil")
	}
	if res.RuntimeOverride.Description != rtDesc {
		t.Errorf("RuntimeOverride.Description = %q, want %q", res.RuntimeOverride.Description, rtDesc)
	}
	// Effective should use runtime override (COALESCE: non-empty runtime wins).
	if res.Effective.Description != rtDesc {
		t.Errorf("Effective.Description = %q, want %q", res.Effective.Description, rtDesc)
	}
	if res.Effective.Owner != rtOwner {
		t.Errorf("Effective.Owner = %q, want %q", res.Effective.Owner, rtOwner)
	}
}

func TestStore_Put_AppendsRow(t *testing.T) {
	ctx := context.Background()
	store := openTestClient(t)

	actor := uuid.New()
	desc := "new desc"
	eff, err := store.Put(ctx, PutInput{
		Asset:       "asset_c",
		Description: &desc,
		SetBy:       actor,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if eff.Description != desc {
		t.Errorf("Put returned Effective.Description = %q, want %q", eff.Description, desc)
	}

	// Verify row actually inserted.
	rows, err := store.ent.AssetMetadata.Query().All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Asset != "asset_c" {
		t.Errorf("row.Asset = %q, want asset_c", rows[0].Asset)
	}
	if rows[0].SetBy != actor {
		t.Errorf("row.SetBy = %v, want %v", rows[0].SetBy, actor)
	}

	// Second Put is INSERT-only — should add another row.
	desc2 := "updated desc"
	_, err = store.Put(ctx, PutInput{Asset: "asset_c", Description: &desc2, SetBy: actor})
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	rows2, _ := store.ent.AssetMetadata.Query().All(ctx)
	if len(rows2) != 2 {
		t.Errorf("expected 2 rows after second Put (INSERT-only), got %d", len(rows2))
	}
}

func TestStore_Put_MergeTags(t *testing.T) {
	ctx := context.Background()
	store := openTestClient(t)

	actor := uuid.New()
	// Initial tags.
	tags1 := []string{"a", "b"}
	_, err := store.Put(ctx, PutInput{Asset: "asset_d", Tags: &tags1, SetBy: actor})
	if err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	// Merge=true — should union.
	tags2 := []string{"c"}
	eff, err := store.Put(ctx, PutInput{Asset: "asset_d", Tags: &tags2, SetBy: actor, Merge: true})
	if err != nil {
		t.Fatalf("merge Put: %v", err)
	}
	tagSet := map[string]bool{}
	for _, tag := range eff.Tags {
		tagSet[tag] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !tagSet[want] {
			t.Errorf("after merge, expected tag %q, got tags %v", want, eff.Tags)
		}
	}
}

func TestStore_Put_ReplaceTags(t *testing.T) {
	ctx := context.Background()
	store := openTestClient(t)

	actor := uuid.New()
	// Initial tags.
	tags1 := []string{"a", "b"}
	_, err := store.Put(ctx, PutInput{Asset: "asset_e", Tags: &tags1, SetBy: actor})
	if err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	// Merge=false — should replace.
	tags2 := []string{"c"}
	eff, err := store.Put(ctx, PutInput{Asset: "asset_e", Tags: &tags2, SetBy: actor, Merge: false})
	if err != nil {
		t.Fatalf("replace Put: %v", err)
	}
	if len(eff.Tags) != 1 || eff.Tags[0] != "c" {
		t.Errorf("after replace, expected tags [c], got %v", eff.Tags)
	}
}
