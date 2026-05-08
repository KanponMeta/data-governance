package gcs

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="gcs" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("gcs", gcs.Factory).
//
// Required params: bucket.
// Optional params: credentials_json (service account JSON), endpoint (for fake-gcs-server),
//
//	use_default_credentials (bool), format ("parquet"|"csv"|"json"; default "parquet").
//
// Security: credentials_json is NEVER logged (T-02-05-01).
func Factory(params map[string]interface{}) (connector.Connector, error) {
	bucket, _ := params["bucket"].(string)
	if bucket == "" {
		return nil, ErrMissingBucket
	}
	endpoint, _ := params["endpoint"].(string)
	credentialsJSON, _ := params["credentials_json"].(string) // never logged (T-02-05-01)
	useDefaultCreds, _ := params["use_default_credentials"].(bool)
	format, _ := params["format"].(string)
	if format == "" {
		format = "parquet"
	}

	// ctx is used only for gcstorage.NewClient (initial dial); the client itself
	// does not retain ctx. Subsequent operations use the per-request context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := NewClientFromOptions(ctx, endpoint, credentialsJSON, useDefaultCreds)
	if err != nil {
		return nil, fmt.Errorf("gcs factory: %w", err)
	}
	return New(client, bucket, format), nil
}
