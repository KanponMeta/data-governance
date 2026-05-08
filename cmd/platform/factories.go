package main

import (
	"github.com/kanpon/data-governance/internal/connector/config"
	"github.com/kanpon/data-governance/internal/connector/firstparty/bigquery"
	"github.com/kanpon/data-governance/internal/connector/firstparty/gcs"
	hdfsc "github.com/kanpon/data-governance/internal/connector/firstparty/hdfs"
	"github.com/kanpon/data-governance/internal/connector/firstparty/mysql"
	"github.com/kanpon/data-governance/internal/connector/firstparty/postgres"
	"github.com/kanpon/data-governance/internal/connector/firstparty/s3"
	"github.com/kanpon/data-governance/internal/connector/firstparty/snowflake"
)

// newFactoryRegistry returns a FactoryRegistry pre-loaded with all seven
// first-party connector factories (D-06):
//
//   - postgres  (SQL, pgxpool)
//   - mysql     (SQL, go-sql-driver/mysql)
//   - snowflake (SQL, gosnowflake)
//   - s3        (object store, AWS SDK v2, parquet/csv/json)
//   - gcs       (object store, cloud.google.com/go/storage, parquet/csv/json)
//   - hdfs      (object store, colinmarc/hdfs/v2, parquet/csv/json)
//   - bigquery  (analytical, cloud.google.com/go/bigquery)
func newFactoryRegistry() *config.FactoryRegistry {
	r := config.NewFactoryRegistry()
	r.RegisterFactory("postgres", postgres.Factory)
	r.RegisterFactory("mysql", mysql.Factory)
	r.RegisterFactory("snowflake", snowflake.Factory)
	r.RegisterFactory("s3", s3.Factory)
	r.RegisterFactory("gcs", gcs.Factory)
	r.RegisterFactory("hdfs", hdfsc.Factory)
	r.RegisterFactory("bigquery", bigquery.Factory)
	return r
}
