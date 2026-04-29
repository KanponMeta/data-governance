# Data Governance Platform

## What This Is

An open-source data governance platform written in Go, inspired by Dagster's asset-centric architecture. It combines data orchestration (software-defined assets, pipeline scheduling, execution engine) with enterprise-grade governance (field-level lineage, data quality rules, metadata catalog, column-level access control, and approval workflows). Designed for data engineers who build pipelines, analysts who explore data, and governance teams who enforce policies — all from a single platform.

## Core Value

A data practitioner can define, run, and govern data assets in code — and every downstream consumer can trust what they're working with, trace where it came from to the field level, and know who is allowed to see it.

## Requirements

### Validated

(None yet — ship to validate)

### Active

**Orchestration Engine**
- [ ] User can define data assets in Go code with explicit upstream dependencies
- [ ] Platform resolves and executes assets in dependency order
- [ ] User can schedule asset materialization via cron or event triggers
- [ ] Platform retries failed asset materializations with configurable backoff
- [ ] User can define partitioned assets (time-based, categorical)

**Data Lineage**
- [ ] Platform automatically captures table-level lineage from asset definitions
- [ ] Platform captures field-level lineage (which output columns derive from which input columns)
- [ ] User can visualize lineage as an interactive DAG in the UI
- [ ] User can trace impact of a field change downstream (impact analysis)

**Data Quality**
- [ ] User can define quality rules on assets (null checks, range checks, custom SQL assertions)
- [ ] Platform evaluates quality rules on each asset materialization
- [ ] Platform sends alerts when quality rules fail or SLAs are breached
- [ ] User can view quality history and trend per asset

**Metadata Management**
- [ ] Platform auto-discovers and registers schema metadata on asset materialization
- [ ] User can annotate assets, tables, and fields with descriptions and tags
- [ ] User can search the data catalog by name, tag, owner, or description
- [ ] Platform tracks schema evolution (diff between versions)

**Access Control**
- [ ] Admin can define roles and assign users to roles
- [ ] Admin can set column-level access policies (specific roles can only see specific columns)
- [ ] Platform enforces column masking / redaction at query time based on role
- [ ] All access events are written to an immutable audit log

**Governance Workflows**
- [ ] User can submit a data asset for governance review before publishing
- [ ] Governance team can approve or reject asset publication with comments
- [ ] Platform sends notifications on approval request and decision
- [ ] All approval decisions are recorded in the audit log

**Compliance & Audit**
- [ ] Platform maintains a complete, tamper-evident audit trail of all data access and mutations
- [ ] User can export audit logs for GDPR / SOC2 compliance reporting
- [ ] Platform supports data retention policies (TTL on assets and audit records)

**Connectors**
- [ ] Platform connects to PostgreSQL and MySQL as source/sink
- [ ] Platform connects to BigQuery and Snowflake as source/sink
- [ ] Platform connects to S3, GCS, and HDFS as source/sink
- [ ] Connector interface is extensible (users can implement custom connectors in Go)

**Observability UI**
- [ ] User can view all assets, their status, and last materialization time
- [ ] User can view execution run history and logs per asset
- [ ] User can view the full lineage graph with field-level drill-down
- [ ] User can view quality scores and alerts dashboard

### Out of Scope

- Python SDK — Go-first; Python bindings deferred post-stable API
- Row-level security — column-level is the initial scope; row-level is a future extension
- Built-in compute execution (Spark, dbt runs) — platform orchestrates and tracks; execution is delegated to external systems
- Multi-tenant SaaS hosting — open-source self-hosted only for v1

## Context

- **Inspiration**: Dagster (Python) — asset-centric model, rich UI, strong observability. This project replicates and extends with Go-native implementation and stronger governance primitives.
- **Key gap Dagster doesn't fill**: field-level lineage, approval workflows, column-level access control, and compliance audit trails.
- **Language**: Go backend. Frontend stack TBD (likely React + TypeScript for the UI).
- **Deployment target**: Open source, self-hosted (Docker Compose / Kubernetes).
- **Reference reading**: Dagster source at https://github.com/dagster-io/dagster and docs at https://docs.dagster.io/

## Constraints

- **Tech Stack**: Go backend (no Python runtime dependency in core) — Go is the team's primary language
- **Open Source**: Apache 2.0 or similar permissive license — target community adoption
- **Self-contained**: Must run on a single machine for development (Docker Compose)
- **Connector extensibility**: Connector interface must be a stable public API from day one — third-party connectors are a key adoption driver

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Asset-centric model (not task-centric) | Dagster proved assets are a better mental model for data work than arbitrary tasks — lineage and governance map naturally to assets | — Pending |
| Go-only core, no Python runtime | Eliminates Python dependency for operators; SDK can be added later once API is stable | — Pending |
| Field-level lineage as first-class feature | Dagster stops at asset level; field-level is the main differentiator for governance use cases | — Pending |
| Open source (self-hosted) for v1 | Maximize adoption and community trust before commercial features | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-04-29 after initialization*
