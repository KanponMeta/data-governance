package s3

import (
	"context"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="s3" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("s3", s3.Factory).
//
// Required params: bucket, region.
// Optional params: endpoint (for localstack/minio), access_key_id, secret_access_key, format.
//
// Security: access_key_id and secret_access_key are NEVER logged (T-02-05-01).
func Factory(params map[string]interface{}) (connector.Connector, error) {
	bucket, _ := params["bucket"].(string)
	if bucket == "" {
		return nil, ErrMissingBucket
	}
	region, _ := params["region"].(string)
	if region == "" {
		return nil, ErrMissingRegion
	}
	// Resolve optional parameters.
	endpoint, _ := params["endpoint"].(string)
	accessKeyID, _ := params["access_key_id"].(string)
	secretAccessKey, _ := params["secret_access_key"].(string)
	format, _ := params["format"].(string)
	if format == "" {
		format = "parquet"
	}

	p := factoryParams{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey, // never logged
		Format:          format,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := buildClient(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("s3 factory: %w", err)
	}
	return New(client, bucket, format), nil
}
