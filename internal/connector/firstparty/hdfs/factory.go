package hdfs

import (
	"context"
	"fmt"
	"time"

	hdfslib "github.com/colinmarc/hdfs/v2"

	"github.com/kanpon/data-governance/internal/connector"
)

// Factory is the config.Factory for type="hdfs" connectors.
// cmd/platform/factories.go calls config.FactoryRegistry.RegisterFactory("hdfs", hdfs.Factory).
//
// Required params:
//   - namenode: HDFS namenode address (e.g. "namenode:8020" or "localhost:9000")
//
// Optional params:
//   - user:   HDFS user name (defaults to OS USER env var)
//   - format: row encoding — "parquet" (default), "csv", or "json"
func Factory(params map[string]interface{}) (connector.Connector, error) {
	namenode, _ := params["namenode"].(string)
	if namenode == "" {
		return nil, ErrMissingNamenode
	}
	user, _ := params["user"].(string)
	format, _ := params["format"].(string)
	if format == "" {
		format = "parquet"
	}

	// Bound construction with a timeout — fast-fail at startup if HDFS is unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx // hdfslib.NewClient does not accept a context; connection is lazy

	opts := hdfslib.ClientOptions{
		Addresses: []string{namenode},
	}
	if user != "" {
		opts.User = user
	}

	client, err := hdfslib.NewClient(opts)
	if err != nil {
		return nil, fmt.Errorf("hdfs factory: new client: %w", err)
	}

	// Verify connectivity by probing StatFs.
	if _, err := client.StatFs(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("hdfs factory: initial StatFs: %w", err)
	}

	return NewFromClient(client, format), nil
}
