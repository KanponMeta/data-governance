package main

import (
	"github.com/kanpon/data-governance/internal/connector/config"
	"github.com/kanpon/data-governance/internal/connector/firstparty/postgres"
	// Additional first-party packages added in plan 02-05.
)

// newFactoryRegistry returns a FactoryRegistry pre-loaded with all first-party
// connector factories (D-06). Plan 02-05 extends this list with the other six.
func newFactoryRegistry() *config.FactoryRegistry {
	r := config.NewFactoryRegistry()
	r.RegisterFactory("postgres", postgres.Factory)
	// Plan 02-05 adds: mysql, bigquery, snowflake, s3, gcs, hdfs.
	return r
}
