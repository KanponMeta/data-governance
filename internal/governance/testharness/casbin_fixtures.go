package testharness

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/casbin/casbin/v2"
	pgxadapter "github.com/pckhoi/casbin-pgx-adapter/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewCasbinFixture returns a *casbin.Enforcer backed by the pgxadapter connected
// to the test database. It loads the RBAC model from internal/auth/rbac_model.conf.
// Returns an error if the model file is not yet created (Task 2 dependency).
func NewCasbinFixture(t *testing.T, db *sql.DB, pool *pgxpool.Pool) (*casbin.Enforcer, error) {
	t.Helper()

	modelPath := "internal/auth/rbac_model.conf"

	// Check if model file exists — if not, return the expected error.
	if _, err := os.Stat(modelPath); err != nil {
		if os.IsNotExist(err) {
			return nil, &ErrModelNotReady{"rbac_model.conf not yet created — Task 2 dependency"}
		}
		return nil, err
	}

	adapter, err := pgxadapter.NewAdapter(pool, pgxadapter.WithTableName("casbin_rule"))
	if err != nil {
		return nil, err
	}

	e, err := casbin.NewEnforcer(modelPath, adapter)
	if err != nil {
		return nil, err
	}

	if err := e.LoadPolicy(); err != nil {
		// If model file is missing the error message mentions the path.
		if strings.Contains(err.Error(), modelPath) || strings.Contains(err.Error(), "model") {
			return nil, &ErrModelNotReady{"rbac_model.conf not yet created — Task 2 dependency"}
		}
		return nil, err
	}

	return e, nil
}

// ErrModelNotReady is returned when the RBAC model file is not yet created.
type ErrModelNotReady struct{ msg string }

func (e *ErrModelNotReady) Error() string { return e.msg }
