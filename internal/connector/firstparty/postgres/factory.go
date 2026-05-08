package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="postgres" connectors. Plan 02-04's
// factories.go calls config.FactoryRegistry.RegisterFactory("postgres", postgres.Factory).
func Factory(params map[string]interface{}) (connector.Connector, error) {
	dsn, _ := params["dsn"].(string)
	if dsn == "" {
		return nil, ErrMissingDSN
	}
	// Bound construction with a timeout — fast-fail at startup if the DB is unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres factory: %w", err)
	}
	return c, nil
}
