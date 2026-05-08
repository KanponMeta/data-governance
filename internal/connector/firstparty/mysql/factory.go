package mysql

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="mysql" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("mysql", mysql.Factory).
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
		return nil, fmt.Errorf("mysql factory: %w", err)
	}
	return c, nil
}
