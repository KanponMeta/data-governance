package governance

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/policy"
)

func noop(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
	return asset.MaterializeResult{}, nil
}

func mustBuild(t *testing.T, b *asset.Builder) *asset.Asset {
	t.Helper()
	a, err := b.Build()
	require.NoError(t, err)
	return a
}

// ---- Builder-only ----

func TestResolveReviewers_BuilderOnly(t *testing.T) {
	a := mustBuild(t, asset.New("orders").
		Connector("snowflake").
		Materialize(noop).
		Reviewers("team-data-gov", "privacy-team").
		Quorum(asset.Quorum2))

	r := NewResolver(nil, nil)
	pool, err := r.ResolveReviewers(context.Background(), a, nil, "")
	require.NoError(t, err)
	require.Equal(t, []string{"team-data-gov", "privacy-team"}, pool.Roles)
	require.Equal(t, 2, pool.Quorum)
	require.Contains(t, pool.Source, "builder")
}

// ---- YAML tag rules ----

func TestResolveReviewers_YamlTagRules(t *testing.T) {
	a := mustBuild(t, asset.New("a").Connector("c").Materialize(noop))
	yaml := &policy.YAMLConfig{
		TagReviewerRoles: map[string][]string{
			"pii":     {"privacy-team"},
			"finance": {"finance-gov"},
		},
	}
	r := NewResolver(nil, yaml)
	pool, err := r.ResolveReviewers(context.Background(), a, []string{"pii", "finance"}, "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"privacy-team", "finance-gov"}, pool.Roles)
	require.Equal(t, 1, pool.Quorum)
	require.Contains(t, pool.Source, "yaml-tag:pii")
	require.Contains(t, pool.Source, "yaml-tag:finance")
}

// ---- Owner fallback ----

func TestResolveReviewers_OwnerFallback_OnlyWhenEmpty(t *testing.T) {
	a := mustBuild(t, asset.New("a").Connector("c").Materialize(noop))

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectQuery(`SELECT roles FROM team_owners WHERE owner_email=\$1`).
		WithArgs("data-eng@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"roles"}).
			AddRow([]byte(`["team-data-gov","data-eng-leads"]`)))

	r := NewResolver(db, nil)
	pool, err := r.ResolveReviewers(context.Background(), a, nil, "data-eng@example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"team-data-gov", "data-eng-leads"}, pool.Roles)
	require.Contains(t, pool.Source, "owner-fallback")
	require.NoError(t, mock.ExpectationsWereMet())
}

// Owner fallback MUST NOT run when builder/yaml already populated the pool.
func TestResolveReviewers_OwnerFallback_SkippedWhenPopulated(t *testing.T) {
	a := mustBuild(t, asset.New("a").Connector("c").Materialize(noop).Reviewers("builder-role"))

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	// No mock.ExpectQuery — fallback must not fire.

	r := NewResolver(db, nil)
	pool, err := r.ResolveReviewers(context.Background(), a, nil, "data-eng@example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"builder-role"}, pool.Roles)
	require.NotContains(t, pool.Source, "owner-fallback")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---- Union of builder + yaml ----

func TestResolveReviewers_UnionOfBuilderAndYaml(t *testing.T) {
	a := mustBuild(t, asset.New("a").Connector("c").Materialize(noop).Reviewers("builder-role"))
	yaml := &policy.YAMLConfig{
		TagReviewerRoles: map[string][]string{"pii": {"privacy-team"}},
	}
	r := NewResolver(nil, yaml)
	pool, err := r.ResolveReviewers(context.Background(), a, []string{"pii"}, "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"builder-role", "privacy-team"}, pool.Roles)
}

// ---- Dedup ----

func TestResolveReviewers_Dedups(t *testing.T) {
	a := mustBuild(t, asset.New("a").
		Connector("c").
		Materialize(noop).
		Reviewers("privacy-team").
		Reviewers("privacy-team", "team-data-gov"))
	yaml := &policy.YAMLConfig{
		TagReviewerRoles: map[string][]string{"pii": {"privacy-team"}}, // duplicate
	}
	r := NewResolver(nil, yaml)
	pool, err := r.ResolveReviewers(context.Background(), a, []string{"pii"}, "")
	require.NoError(t, err)
	// privacy-team appears 3 times; expect deduped to 1
	require.ElementsMatch(t, []string{"privacy-team", "team-data-gov"}, pool.Roles)
}

// ---- QuorumAll preserved ----

func TestResolveReviewers_QuorumAllPreserved(t *testing.T) {
	a := mustBuild(t, asset.New("a").
		Connector("c").
		Materialize(noop).
		Reviewers("a", "b", "c").
		Quorum(asset.QuorumAll))
	r := NewResolver(nil, nil)
	pool, err := r.ResolveReviewers(context.Background(), a, nil, "")
	require.NoError(t, err)
	require.Equal(t, int(asset.QuorumAll), pool.Quorum)
	require.Equal(t, []string{"a", "b", "c"}, pool.Roles)
}
