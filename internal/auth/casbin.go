package auth

import (
	"context"

	"github.com/casbin/casbin/v2"
	pgxadapter "github.com/pckhoi/casbin-pgx-adapter/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewEnforcer creates a Casbin RBAC enforcer backed by the pgxadapter.
// The adapter connects to casbin_rule table in the platform database.
// The model is loaded from internal/auth/rbac_model.conf.
func NewEnforcer(ctx context.Context, pool *pgxpool.Pool, modelPath string) (*casbin.Enforcer, error) {
	adapter, err := pgxadapter.NewAdapter(pool, pgxadapter.WithTableName("casbin_rule"))
	if err != nil {
		return nil, err
	}

	e, err := casbin.NewEnforcer(modelPath, adapter)
	if err != nil {
		return nil, err
	}

	if err := e.LoadPolicy(); err != nil {
		return nil, err
	}

	e.EnableAutoSave(true)
	return e, nil
}
