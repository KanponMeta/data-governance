# Phase 2: 执行引擎 - Context

**Gathered:** 2026-05-07
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 2 delivers the execution kernel that turns user-defined assets into reliable runs, plus seven first-party connectors that read/write data:

- **Asset DSL** — Functional builder for defining assets in user Go code with explicit upstream dependencies
- **DAG executor** — In-memory topological resolution (heimdalr/dag) + River-backed step dispatch
- **Retry engine** — Asset-level retry policies with exponential backoff
- **Concurrency control** — Single global token pool with resource tagging (avoids Dagster issue #25743 deadlock)
- **Run claiming** — `SELECT FOR UPDATE SKIP LOCKED` + state-enum CHECK constraints (atomic, no duplicates)
- **CLI** — On-demand asset materialization via `materialize <asset>` subcommand
- **Seven first-party connectors** — PostgreSQL, MySQL, BigQuery, Snowflake, S3, GCS, HDFS, all in-process implementations of the v1.0.0 frozen `connector.Connector` interface

New capabilities (cron schedules, sensors, partitions, lineage capture, governance, UI) belong in later phases.

</domain>

<decisions>
## Implementation Decisions

### Asset Definition DSL & Loading

- **D-01:** Functional builder DSL — `asset.New("users_clean").Upstream("users_raw").Connector("postgres-prod").Materialize(fn).Register()`. Variadic `Upstream(...)`, chained options, `Register()` adds to a process-global `DefinitionRegistry`.
- **D-02:** Users compile their own binary that links the platform SDK + their asset definitions. Single binary with mode subcommands: `./myproject server` (REST/UI), `./myproject worker` (executes runs), `./myproject materialize <asset>` (CLI trigger). Go's static linking removes the need for Dagster's gRPC code-location two-process model.
- **D-03:** Assets reference connectors **by name** (string), not by direct configuration. Platform startup loads connector configs from a config file (yaml/toml) keyed by the same names. Credentials travel as environment variables interpolated into config (e.g., `password: $PG_PROD_PASSWORD`). Aligns with Phase 1 connector.proto convention (sensitive values via env-var indirection).
- **D-04:** `Materialize` signature: `func(ctx context.Context, input AssetIO) (MaterializeResult, error)`. `AssetIO` provides `Read(asset)` / `Write(rows)` helpers that delegate to the connector behind the scenes — users do **not** call `connector.Read/Write` directly. `MaterializeResult{RowsWritten, Metadata map[string]any}` returns business-meaningful counts and is the hook for Phase 4 lineage extension.
- **D-05:** Platform discovers assets via runtime registry calls in user `init()`/`main()` — no reflection scan, no codegen. The `worker`/`materialize` subcommands rely on the same import graph that pulled assets into the registry.

### First-Party Connector Packaging

- **D-06:** All seven first-party connectors (PG, MySQL, BQ, Snowflake, S3, GCS, HDFS) are compiled in-process into the platform binary. Each implements `connector.Connector` directly (extending the Phase 1 `example_inproc/postgres_stub.go` pattern).
- **D-07:** `connector.Registry` supports two loaders: `RegisterInProcess(name, impl)` for first-party (Phase 2) and `RegisterPlugin(name, pluginPath)` via `hashicorp/go-plugin` (deferred until first third-party connector ships, but interface stays accessible).
- **D-08:** Connector lifecycle = process-global singletons. Each named connector is initialized once at platform startup (e.g., `pgxpool.New(...)`), held for the process lifetime, and reused across all materialize runs. Connection pools live inside the connector implementation.
- **D-09:** Credentials live in a startup config file (yaml or toml) keyed by connector name; secret fields are environment-variable interpolations (e.g., `password: ${PG_PROD_PASSWORD}`). No Vault integration in Phase 2 — that is a v2 concern.
- **D-10:** Cloud connector CI testing uses local emulators/fakes — no real cloud credentials in CI:
  - PostgreSQL/MySQL → `testcontainers-go` with real container images
  - BigQuery → `goccy/bigquery-emulator`
  - Snowflake → community mock (or interface-only conformance test if no usable mock exists; full integration becomes a nightly job)
  - S3 → LocalStack or minio
  - GCS → `fsouza/fake-gcs-server`
  - HDFS → `colinmarc/hdfs` against a dockerized HDFS image

### Phase Scope Split

- **D-11:** Phase 2 stays as **one phase** in ROADMAP.md. Internal granularity comes from multiple `02-N-PLAN.md` files. Suggested plan partitioning (planner may refine):
  - `02-01-PLAN.md` — Asset DSL + DefinitionRegistry + AssetIO
  - `02-02-PLAN.md` — DAG executor (heimdalr/dag) + River step dispatch + run lifecycle (state machine, atomic claiming)
  - `02-03-PLAN.md` — Retry engine + concurrency token pool + run-event log additions
  - `02-04-PLAN.md` — PostgreSQL connector (reference impl) + CLI `materialize` subcommand
  - `02-05-PLAN.md` — Remaining six connectors (MySQL, BigQuery, Snowflake, S3, GCS, HDFS) — likely one plan with sub-tasks per connector since they share the interface
- **D-12:** Connector delivery order: PostgreSQL leads (validates the architecture and acceptance criterion 4); the other six are an "alternate implementations of the same interface" batch.
- **D-13:** All five ROADMAP acceptance criteria (topological execution, retry, concurrent-claim safety, PG-on-CLI run, all-7-connectors-pass-integration) MUST PASS for Phase 2 to be considered complete.

### Retry & Concurrency Design

- **D-14:** Two-layer retry: **River handles infrastructure faults** (worker crash, network blip — its native `max_attempts` + retry policy); **the engine handles business faults** (Materialize returns `error`). Engine retry counter and backoff are per-asset config and tracked in the `event_log` (`run.step.retry_scheduled` events). A worker restart does NOT consume engine retry budget.
- **D-15:** Retry policy declared on the asset builder: `asset.New("x").Retry(asset.RetryPolicy{Max: 3, InitialDelay: 30*time.Second, MaxDelay: 5*time.Minute, JitterPct: 25}).Materialize(fn)`. A platform-level default (also declared in startup config) applies when an asset omits `Retry(...)`.
- **D-16:** Concurrency token pool = **single global `concurrency_tokens` Postgres table** with rows holding `(run_id, asset_id, resource_tag, weight, acquired_at)`. Run-level, op-level, and resource-level limits all check out / return tokens against this same table. Resource tags come from the asset builder: `asset.New("x").Resource("postgres-prod", 1)` (multiple resources allowed; weight default 1). This is the single source of truth that PITFALLS #2 demands — three-layer hierarchical pools are explicitly REJECTED.
- **D-17:** Run claiming uses `SELECT ... WHERE state = 'queued' FOR UPDATE SKIP LOCKED` (PostgreSQL). The `runs` table state column has a `CHECK` constraint enumerating `(queued|starting|running|succeeded|failed|canceled)` and forbids backward transitions except via a privileged `reset` operation. **Required test for Phase 2 verification:** spawn 50 concurrent goroutines that try to claim the same queued run and assert exactly one wins (covers acceptance criterion 3 + PITFALLS #1).
- **D-18:** Run lifecycle event log additions to the Phase 1 `event_type` enum: `run.queued`, `run.started`, `run.step.started`, `run.step.succeeded`, `run.step.failed`, `run.step.retry_scheduled`, `run.succeeded`, `run.failed`, `run.canceled`. All retries — count, scheduled time, error message — are recorded as events (acceptance criterion 2).

### Claude's Discretion

- Internal implementation of `AssetIO` (whether it streams rows lazily, batches, or buffers) — performance details that don't change the user-facing contract.
- Specific River queue topology (one queue vs multiple priority queues) — Phase 2 default is one queue, planner may revisit if backfill priority comes up early.
- Internal layout of `concurrency_tokens` table indexes and acquisition retry strategy — implementation freedom as long as the contract holds.
- Whether `MaterializeResult.Metadata` keys are typed via constants in Phase 2 or stay free-form `map[string]any` — small UX call.
- Choice of yaml vs toml for the startup config file — pick one, commit to it, no need for both.
- Whether the `materialize` CLI subcommand blocks until run completion (synchronous) or returns a run-id immediately (asynchronous) — Phase 2 default is **synchronous with a `--detach` flag for async**, but planner may simplify to one mode if integration tests force it.
- Decision on whether Snowflake gets a usable mock or its full integration test becomes a nightly real-creds job — contingent on what exists in the Go ecosystem at planning time.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — Phase 2 in-scope requirements: ORCH-01, ORCH-02, ORCH-03, ORCH-04, ORCH-09, ORCH-10, CONN-01, CONN-02, CONN-03, CONN-04, CONN-05, CONN-06, CONN-07
- `.planning/ROADMAP.md` §Phase 2 — verification criteria + dependency on Phase 1

### Project Context
- `.planning/PROJECT.md` §关键决策 — concurrency token pool MUST be designed with execution engine
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — Phase 1 decisions (D-01..D-10) including connector ABI freeze, event log design with RLS, storage abstraction

### Research (must-read for Phase 2)
- `.planning/research/ARCHITECTURE.md` §1 — Dagster execution pipeline (AssetGraph → ExecutionPlan → Step dispatch); §1.2 (asset definitions are runtime objects, not DB rows); §1.3 (executor model)
- `.planning/research/PITFALLS.md` §1 — Run state atomicity (SKIP LOCKED + CHECK constraints + 50-goroutine test)
- `.planning/research/PITFALLS.md` §2 — Single concurrency token pool, NOT layered (Dagster issue #25743)
- `.planning/research/PITFALLS.md` §6 — Backfill resource isolation (deferred to Phase 3 but design hooks now)
- `.planning/research/PITFALLS.md` §9 — Connector API stability (Phase 1 froze v1.0.0; Phase 2 must not break it)
- `.planning/research/STACK.md` — Tech stack choices and versions (River, heimdalr/dag, hashicorp/go-plugin)
- `.planning/research/SUMMARY.md` §推荐技术栈 — high-level rationale

### Tech Stack & Conventions
- `CLAUDE.md` §技术栈 — River v0.35.x, heimdalr/dag v1.5.x, hashicorp/go-plugin v1.7.x, connectrpc/connect-go v1.19.x, ent v0.14.x, sqlc v1.31.x
- `CLAUDE.md` §备选方案对比 — explicitly excluded (Temporal, GORM, Gin, Fiber, golang-migrate, Go native plugin)

### Phase 1 Code (frozen contracts Phase 2 builds on)
- `internal/connector/connector.go` — `Connector` interface (frozen v1.0.0)
- `internal/connector/proto/connector.proto` — proto IDL (frozen v1.0.0)
- `internal/connector/registry.go` — connector registry (Phase 2 extends with in-process loader)
- `internal/connector/example_inproc/postgres_stub.go` — in-process pattern reference
- `internal/storage/storage.go` — Storage interface, ent client, WithTx
- `internal/storage/ent/` — ent schema (Phase 2 adds Run, RunStep, ConcurrencyToken entities)
- `internal/event/event.go`, `writer.go`, `types.go` — event log writer (Phase 2 adds new event types)
- `cmd/platform/main.go` — current entry point (Phase 2 adds `worker` and `materialize` subcommands)

### External References
- River queue docs: https://riverqueue.com/ — for retry policy + cron + transactional enqueue patterns
- heimdalr/dag: https://pkg.go.dev/github.com/heimdalr/dag — topological sort, cycle detection, traversal
- Dagster issue #15155 (run-claim atomicity), #25743 (concurrency layering deadlock) — referenced for test design

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phase 1)
- **`internal/connector.Connector`** interface — Phase 2's seven first-party connectors implement this directly; ABI is frozen.
- **`connector.Registry`** — Phase 2 extends with `RegisterInProcess(name, impl)` for first-party loading.
- **`internal/connector/example_inproc/postgres_stub.go`** — already demonstrates the in-process pattern (D-06 generalizes this to seven connectors).
- **`Storage` interface + ent client** — Phase 2 adds entities (Run, RunStep, ConcurrencyToken) via ent schema and Atlas migrations using the existing pattern.
- **`event.Writer`** — Phase 2 reuses the writer; only the `event_type` enum gets new values (D-18).
- **`auth` JWT middleware** — `materialize` CLI authenticates via the same JWT path as REST API; reused, not rebuilt.
- **`api/router.go`** + `grpc_stub.go` — Phase 2 fills concrete handlers for run-related routes (trigger, status) on top of the existing chi + connect-go skeleton.

### Established Patterns
- ent schema + Atlas migrations for all metadata persistence (Phase 1 D-04 layout)
- RFC 7807 Problem+JSON for HTTP errors (Phase 1 D-06)
- Single transaction via `Storage.WithTx` for any cross-table write (run state + event_log writes go together)
- Append-only events with PostgreSQL RLS forbidding UPDATE/DELETE (Phase 1 D-09) — Phase 2 events follow the same model
- Functional builder + Register() (D-01) is a NEW pattern Phase 2 establishes; downstream phases (Phase 3 schedules, Phase 4 lineage) will hang off the same registry

### Integration Points
- `connector.Connector` (frozen v1.0.0) — Phase 2 implementations ship in `internal/connector/firstparty/{postgres,mysql,bigquery,snowflake,s3,gcs,hdfs}/`
- `Storage.Ent()` — Phase 2 adds Run, RunStep, ConcurrencyToken ent entities
- `event.Writer` — Phase 2 writes the new `run.*` event types
- `cmd/platform/main.go` — Phase 2 adds `server`, `worker`, `materialize` subcommands (Phase 1 already runs `start` for HTTP API)
- New SDK package (likely `pkg/asset` or `internal/asset` exported via re-export) — user binaries import this for `asset.New(...)`. SDK boundary makes this the FIRST package the platform exports for external consumption.

</code_context>

<specifics>
## Specific Ideas

- **Single binary, multiple modes** — leveraging Go static linking is the central architectural insight that simplifies the platform vs Dagster's Python-mandated two-process gRPC model. Don't introduce code-location subprocess machinery just because Dagster has it.
- **PostgreSQL connector is the reference implementation.** It's the only acceptance-criterion-pinned connector ("CLI command runs successfully against local Postgres"), it's already validated by Phase 1 storage layer, and it lets us close the engine-validation loop without coordinating six external systems first.
- **Single concurrency token pool with resource tags** is non-negotiable per PITFALLS #2 + PROJECT.md key decisions. Reject any planner instinct toward "let's start with a simple per-run counter and add layers later" — that's exactly the Dagster failure mode.
- **The 50-goroutine claim test** is a verification deliverable, not a nice-to-have. It directly maps to ROADMAP acceptance criterion 3.
- **Retry visibility in event log** maps directly to ROADMAP acceptance criterion 2 — every retry attempt and timestamp must be queryable from `event_log`.
- **First package exported as SDK** — Phase 2 marks the moment users start importing platform code. The `asset` package signature stability matters from this point onward; treat builder method names as a public API.

</specifics>

<deferred>
## Deferred Ideas

- **Cron schedules + sensors** — Phase 3 (ORCH-05, ORCH-06).
- **Time / category partitions + backfill** — Phase 3 (ORCH-07, ORCH-08). Backfill resource isolation (PITFALLS #6) needs priority queues; Phase 2 only must not preclude this — token pool resource tags already give us the hook.
- **go-plugin third-party connector loader** — interface is reserved (D-07) but actual subprocess scaffolding waits for first real third-party connector demand. Not part of v1 acceptance.
- **Vault / KMS credential integration** — Phase 2 uses env-var interpolation only. Vault is a v2 concern.
- **OpenLineage event emission during materialization** — Phase 4 wires this in; Phase 2's `MaterializeResult.Metadata` is the future hook.
- **Async-only `materialize` CLI** — defaulting to synchronous for the simple case; `--detach` is at Claude's discretion, async-only is a v1.x polish.
- **Lineage extraction from Materialize calls** — Phase 4 (LINE-01..LINE-06).
- **Schema capture on every materialize** — Phase 4 (META-01, META-02).
- **Snowflake real-creds nightly integration job** — only if no usable Go mock exists; otherwise covered by emulator strategy (D-10).

</deferred>

---

*Phase: 02-execution-engine*
*Context gathered: 2026-05-07*
