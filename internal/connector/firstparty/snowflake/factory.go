package snowflake

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="snowflake" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("snowflake", snowflake.Factory).
//
// Required params: dsn (Snowflake DSN string carrying account, user, password, warehouse,
// database and schema — format: user:password@account/database/schema?warehouse=mywh).
//
// Security: dsn MUST NEVER be logged because it contains the password (T-02-05-01).
func Factory(params map[string]interface{}) (connector.Connector, error) {
	dsn, _ := params["dsn"].(string)
	if dsn == "" {
		return nil, ErrMissingDSN
	}
	// Bound construction with a timeout — fast-fail at startup if the account is unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// NOTE: dsn is NEVER logged (T-02-05-01 — it carries the password).
	c, err := New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("snowflake factory: %w", err)
	}
	return c, nil
}
