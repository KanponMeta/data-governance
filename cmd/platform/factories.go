package main

import (
	"github.com/kanpon/data-governance/internal/connector/config"
	"github.com/kanpon/data-governance/internal/connector/firstparty/bigquery"
	"github.com/kanpon/data-governance/internal/connector/firstparty/gcs"
	"github.com/kanpon/data-governance/internal/connector/firstparty/mysql"
	"github.com/kanpon/data-governance/internal/connector/firstparty/postgres"
	"github.com/kanpon/data-governance/internal/connector/firstparty/s3"
	// Snowflake + HDFS imports added by task 5.2b.
)

// newFactoryRegistry returns a FactoryRegistry pre-loaded with all first-party
// connector factories (D-06). Task 5.2b adds snowflake and hdfs to complete all 7.
func newFactoryRegistry() *config.FactoryRegistry {
	r := config.NewFactoryRegistry()
	r.RegisterFactory("postgres", postgres.Factory)
	r.RegisterFactory("mysql", mysql.Factory)
	r.RegisterFactory("s3", s3.Factory)
	r.RegisterFactory("bigquery", bigquery.Factory)
	r.RegisterFactory("gcs", gcs.Factory)
	// Snowflake + HDFS added by task 5.2b.
	return r
}
