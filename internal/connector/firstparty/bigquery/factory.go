package bigquery

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
	"google.golang.org/api/option"
)

// Factory is the config.Factory for type="bigquery" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("bigquery", bigquery.Factory).
//
// Required params: project.
// Optional params: credentials_json (base64 encoded service account JSON or raw JSON),
//
//	emulator_endpoint (for goccy/bigquery-emulator).
//
// Security: credentials_json is NEVER logged (T-02-05-01).
func Factory(params map[string]interface{}) (connector.Connector, error) {
	project, _ := params["project"].(string)
	if project == "" {
		return nil, ErrMissingProject
	}
	emulatorEndpoint, _ := params["emulator_endpoint"].(string)
	credentialsJSON, _ := params["credentials_json"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := buildClientOptions(emulatorEndpoint, credentialsJSON)
	c, err := New(ctx, project, opts...)
	if err != nil {
		return nil, fmt.Errorf("bigquery factory: %w", err)
	}
	return c, nil
}

// buildClientOptions constructs the []option.ClientOption for the BigQuery client.
// Credentials are handled here and NEVER logged (T-02-05-01).
func buildClientOptions(emulatorEndpoint, credentialsJSON string) []option.ClientOption {
	var opts []option.ClientOption
	if emulatorEndpoint != "" {
		// Emulator mode: disable auth, point to local server.
		opts = append(opts,
			option.WithEndpoint(emulatorEndpoint),
			option.WithoutAuthentication(),
		)
	} else if credentialsJSON != "" {
		// Production: use provided service account JSON.
		// NOTE: credentialsJSON value is NEVER included in log output (T-02-05-01).
		opts = append(opts, option.WithCredentialsJSON([]byte(credentialsJSON)))
	}
	return opts
}
