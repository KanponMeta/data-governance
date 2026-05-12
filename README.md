# Data Governance Platform

A Go-native data governance platform inspired by Dagster's asset-centric architecture. Combines data orchestration (software-defined assets, pipeline scheduling, execution engine) with enterprise-grade governance capabilities (field-level lineage, data quality rules, metadata catalog, column-level access control, and approval workflows) — in a single binary.

## Features

### Core Platform
- **Asset Execution Engine** — Define assets in Go with explicit upstream dependencies, trigger materialization via CLI or API
- **DAG-based Execution** — Topological ordering, 50-goroutine atomic claim, exponential backoff retry
- **Scheduling** — Cron schedules, event sensors, time/category partition backfill
- **Connectors** — PostgreSQL, MySQL, BigQuery, Snowflake, S3, GCS, HDFS (extensible via hashicorp/go-plugin)

### Governance
- **Field-Level Lineage** — Auto-captured asset and column lineage, recursive CTE impact analysis
- **Schema Evolution** — Auto-captured schema on materialization, breaking change classification
- **RBAC + Column Masking** — Casbin-based policies, Snowflake DDM / BigQuery CLS sync
- **Hash-Chain Audit Log** — Append-only with RLS protection, export to JSON/CSV
- **Governance Workflow** — Submit assets for review, approve/reject with mandatory comments
- **Quality Rules** — NullCheck, RangeCheck, SQLAssertion evaluated after each materialization

### API & UI
- **REST + ConnectRPC API** — Full API surface via chi router and ConnectRPC protocol
- **React SPA** — Embedded in Go binary via `go:embed`, served on non-API routes
- **Asset Dashboard** — Asset list with state, last materialized time, quality badges
- **Lineage DAG** — Interactive visualization with ReactFlow + dagre, column drilldown
- **Governance Inbox** — Approve/reject workflow UI
- **Catalog Search** — PostgreSQL full-text search with tag/owner filtering

## Quick Start

```bash
# Start with Docker Compose
cp .env.example .env
docker compose up -d

# Platform runs at http://localhost:8080
# API docs at http://localhost:8080/v1/docs (when swagger enabled)
```

### Local Development

```bash
# Prerequisites
go 1.22+, node 18+, pnpm, postgres 16+

# Setup
cp .env.example .env
export DATABASE_URL=postgres://platform:platform@localhost:5432/platform?sslmode=disable

# Build and run migrations
make build
make migrate

# Run platform
./bin/platform start

# Or run with go run
go run ./cmd/platform start

# Frontend (separate terminal)
cd web && pnpm install && pnpm dev
```

## CLI Commands

```bash
./bin/platform help

Commands:
  start          Start the platform server
  healthcheck    Health check endpoint
  migrate        Run database migrations
  materialize    Trigger asset materialization
  scheduler      Run the scheduler daemon
  backfill       Submit partition backfill
  impact         Run impact analysis
  schema         Manage schema changes
  lineage        Export lineage data
  audit          Export audit log
  role           Manage RBAC roles
  policy         Manage column policies
  governance     Manage governance workflow
  reconciler    Run policy reconciler
```

## Architecture

```
cmd/platform/           Platform binary entry point
internal/
  asset/               Asset definition DSL and registry
  run/                 Run state machine and claim logic
  runtime/             DAG executor and concurrency pool
  schedule/            Cron scheduler daemon
  sensor/              Event sensor evaluator
  backfill/            Partition backfill submission
  lineage/             Lineage capture and traversal
  schema/              Schema capture and diff
  metadata/            Asset metadata annotations
  policy/              Column policies and masking
  governance/          Workflow state machine
  quality/             Quality rule evaluation
  notification/        Webhook and SMTP dispatch
  audit/               Hash-chain audit log writer
  connector/            Connector interface definition
    firstparty/        Built-in connectors (postgres, mysql, bigquery, snowflake, s3, gcs, hdfs)
  api/                 REST + ConnectRPC handlers
  auth/                JWT service, Casbin enforcer, middleware
  storage/             ent schema and storage abstraction
  event/               Append-only event writer
web/                   React 19 SPA (embedded via go:embed)
proto/                 ConnectRPC IDL definitions
migrations/            Atlas SQL migrations
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | — | PostgreSQL connection string |
| `JWT_SIGNING_KEY` | — | 32-byte secret for JWT signing (min 32 chars) |
| `JWT_ACCESS_TTL` | `15m` | JWT access token TTL |
| `PLATFORM_HTTP_ADDR` | `:8080` | HTTP listen address |
| `PLATFORM_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |

## API

REST endpoints at `/v1/*`, ConnectRPC at `/v1/connect/*`.

Key endpoints:
- `POST /v1/auth/register` — User registration
- `POST /v1/auth/login` — Login, returns JWT in cookie
- `GET /v1/assets` — List assets with state
- `GET /v1/lineage/impact?asset=X&column=Y` — Impact analysis
- `POST /v1/governance/submit` — Submit asset for review
- `GET /v1/catalog/search?q=...` — Full-text search

## Tech Stack

| Component | Technology |
|-----------|------------|
| Backend | Go 1.22, ent, Atlas, chi, ConnectRPC |
| Database | PostgreSQL 16 |
| Job Queue | River ( Postgres-based) |
| Auth | JWT (HS256), Casbin RBAC |
| Lineage | sqlc + recursive CTE |
| Frontend | React 19, TypeScript, TanStack Router/Query |
| DAG Viz | ReactFlow (@xyflow/react), dagre |
| Charts | Recharts |
| Deployment | Docker Compose, single binary |

## Status

**v1.0 MVP** — Shipped 2026-05-12

All 64 v1 requirements satisfied. Known tech debt: main.go wiring gaps, Phase 6 stubs (quality trend/alerts/admin policies). See [`.planning/MILESTONES.md`](.planning/MILESTONES.md) for details.

## License

Apache 2.0