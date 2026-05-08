# Phase 3: 调度、传感器与分区 - Context

**Gathered:** 2026-05-08
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 3 makes the Phase 2 execution engine self-driving. It adds *what enqueues runs and how they're sliced*, while leaving the run-execution kernel untouched. Specifically:

- **Cron scheduler daemon** — auto-fires asset materializations at user-declared cron expressions, durable across process restart
- **Event sensors** — user-supplied `Sense(ctx)` poll functions trigger materializations when external conditions become true, with dedup and cooldown
- **Time partitions** — daily/weekly/monthly partitioned assets keyed in UTC ISO-8601 windows
- **Category partitions** — static category-keyed partitioned assets (e.g., per-region, per-customer)
- **Backfill** — operator-submitted historical-range or category-set materialization, with priority-isolated claim semantics so backfills cannot starve normal scheduled runs (PITFALLS #6 mitigation)
- **DSL extensions** — `.Schedule(cron)`, `.Sensor(spec)`, `.Partitions(strategy)` chained methods on the existing builder

New capabilities (lineage capture, Schema diff, governance, RBAC, Web UI) belong in Phase 4+.

The Phase 2 execution kernel — DAG executor, FIFO claim with `SELECT … FOR UPDATE SKIP LOCKED`, retry engine, concurrency token pool, event log writer — is **not modified** except for additive schema changes (`runs.partition_key`, `runs.priority`, claim ordering update) and additional `event_type` enum values.

</domain>

<decisions>
## Implementation Decisions

### Scheduler Architecture

- **D-01:** Scheduler runs as a new `./platform scheduler` subcommand, parallel to existing `server` / `worker` / `materialize` (extends Phase 2 D-02 multi-mode pattern). Scheduler enqueues runs into the `runs` table; workers execute them. Scheduler down ⇒ no new runs queued, but in-flight runs unaffected. Operators scale scheduler and worker pools independently.

- **D-02:** Schedule state persistence is **lazy**. New `schedules` table holds `(asset_name, cron_expr, last_fire_at, next_fire_at, paused_at, ...)`. Each tick (default 30s) the scheduler selects rows with `next_fire_at <= NOW()` using `SELECT ... FOR UPDATE SKIP LOCKED`, enqueues a run row in `runs`, updates `last_fire_at` and recomputes `next_fire_at`. The `runs` table holds *only claimable runs*, never future runs.

- **D-03:** Cron expression parsing uses `robfig/cron/v3` (parser + `Next()` API only — *not* its in-process Cron scheduler). Scheduler tick loop is custom: a single Postgres query drives all schedule firing. Multi-replica safety comes from the same `SELECT FOR UPDATE SKIP LOCKED` primitive that protects run claiming (Phase 2 D-17). **No leader election, no advisory locks.** River is **not** introduced — `go.mod` does not currently include River, and the SKIP LOCKED model is sufficient for ORCH-05 / ORCH-06 acceptance criteria.

- **D-04:** Missed-schedule recovery: **fire only the most recent missed window per schedule** when scheduler restarts after downtime. Scheduler emits a `schedule.missed` event with the count of skipped fires for ops visibility. Avoids run-avalanche after a multi-hour outage. Aligns with Dagster default behavior. (Per-schedule policy override is a deferred v1.x feature.)

### Sensor Model

- **D-05:** Sensors run **inside the same `scheduler` subcommand** as cron, sharing the tick loop and SKIP LOCKED primitives. New `sensors` table mirrors `schedules`: `(asset_name, sensor_name, min_interval, last_evaluated_at, last_fired_at, last_run_key, cooldown_until, consecutive_failures, disabled_at, ...)`. Each tick selects sensors due for evaluation, calls user's `Sense(ctx)`, and conditionally enqueues runs.

- **D-06:** User-facing sensor contract:
  ```go
  type SensorResult struct {
      Fired   bool
      RunKey  string         // dedup key — same key as last fire => skip
      Payload map[string]any // attached to triggered run's MaterializeResult.Metadata
  }
  type SensorFunc func(ctx context.Context) (SensorResult, error)
  ```
  Builder: `asset.New("x").Sensor(asset.SensorSpec{Name:"...", MinInterval: 30*time.Second, Cooldown: 5*time.Minute, Sense: senseFn})`. `Payload` becomes a future Phase 4 lineage hook (consistent with Phase 2 D-04 `MaterializeResult.Metadata` design).

- **D-07:** Two-layer dedup — RunKey comparison **and** cooldown window (belt-and-suspenders against bugs in user code). If `RunKey == last_run_key` ⇒ no enqueue. If `NOW() < cooldown_until` ⇒ no enqueue regardless of key. `Cooldown` defaults to `0` (off, opt-in).

- **D-08:** `Sense()` error handling: log structured error, emit `sensor.evaluation_failed` event in `event_log`, retry next tick (no failed-run row created — sensor errors are infrastructure noise, not data work). After `consecutive_failures >= N` (configurable, default 60), set `sensors.disabled_at = NOW()` and emit `sensor.disabled` event. Operator must manually re-enable. Surfaces broken sensors loudly without spamming the runs table.

### Partition Model & DSL Composition

- **D-09:** Partitions declared via single `.Partitions(spec)` method where `spec` implements a typed `asset.PartitionStrategy` interface. v1 strategies:
  - `asset.DailyPartitions{Start, TZ}`
  - `asset.WeeklyPartitions{Start, TZ}` (ISO week)
  - `asset.MonthlyPartitions{Start, TZ}`
  - `asset.CategoryPartitions{Keys []string}`

  At most one strategy per asset. `MaterializeFunc` learns its partition via `io.PartitionKey() string` on the existing `AssetIO` (Phase 2 D-04 surface). Future strategies (dynamic / DB-driven) plug in by adding new types; the builder method does not change.

- **D-10:** Partitioned runs persist as **rows in the existing `runs` table** with a new nullable `partition_key VARCHAR(128)` column. Non-partitioned runs leave it `NULL`. New unique constraint `(asset_name, partition_key)` scoped to in-flight states (`queued`, `starting`, `running`) prevents duplicate concurrent runs of the same partition. Existing claim query, retry, event_log, heartbeat reaper all work unchanged. Acceptance criterion 3 ("each partition has its own event log entries") falls out for free — every run already emits its own `run.*` events keyed by `run_id`.

- **D-11:** Time-partition keys are **UTC start-of-window ISO-8601 strings**:
  - Daily: `2024-01-15`
  - Weekly: `2024-W03`
  - Monthly: `2024-01`
  - Category: the user-supplied key string (e.g., `us`, `eu`, `apac`)

  TZ on the partition spec is for *cron alignment and display*, not key encoding — storage stays UTC to avoid DST landmines.

- **D-12:** Builder methods compose orthogonally. All combinations are valid:
  - `.Schedule(cron).Partitions(daily)` — cron fire enqueues a run for the *current* partition window (e.g., yesterday's daily partition)
  - `.Sensor(spec).Partitions(...)` — `SensorResult` may include an explicit `PartitionKey` to fire one specific partition; if empty, fires the latest current partition
  - `.Schedule(cron).Sensor(spec).Partitions(daily)` — both triggers active independently
  - `.Retry(...)` and `.Resource(...)` apply per-run regardless of how the run was triggered

### Backfill (PITFALLS #6 — required before backfill API ships)

- **D-13:** Three-layer backfill isolation:
  1. **Priority column** — `runs.priority` enum: `critical | normal | backfill` (default `normal`). Stored as VARCHAR(16) with CHECK constraint, mirroring the Phase 2 `state` column pattern (D-17).
  2. **Priority-then-FIFO claim** — `ClaimNext` query becomes `ORDER BY priority ASC, queued_at ASC` (priority enum mapped to integers internally; `critical=0, normal=1, backfill=2`). Normal runs always preempt queued backfill runs without changing the SKIP LOCKED atomicity guarantee. **The existing 50-goroutine claim atomicity test (Phase 2 D-17) must continue to pass with the new ORDER BY.**
  3. **Concurrency token pool resource tag** — backfill runs additionally acquire a `backfill` weighted resource from the existing `concurrency_tokens` table (Phase 2 D-16). Per-asset token weight default `1`; pool capacity `max_concurrent_backfill` defaults to `5`, configurable per-asset and globally. Together prevents both queue-position starvation **and** connector saturation (the two halves of PITFALLS #6).

- **D-14:** Backfill submitted via new `./platform backfill <asset> --partitions=<spec> [--priority=backfill]` CLI subcommand. Spec accepts:
  - Date range: `--partitions=2024-01-01:2024-12-31`
  - Comma list: `--partitions=us,eu,apac`
  - Single key: `--partitions=2024-01-15`

  Returns `backfill_id` immediately. Status polled via `./platform backfill status <backfill_id>`. CLI is the v1 surface; REST endpoint is a Phase 6 UI dependency, not Phase 3 work.

- **D-15:** Backfill chunking strategy: **enqueue all partition runs immediately, rely on `max_concurrent_backfill` for in-flight cap.** Submission inserts every partition row into `runs` with `priority='backfill'` and a shared `backfill_id` (new column). Claim ordering + token pool capacity ensures only N are in-flight at any time. Trade-off accepted: a 365-partition backfill creates 365 `runs` rows immediately, but progress is trivially queryable (`SELECT state, count(*) FROM runs WHERE backfill_id=$1 GROUP BY state`), retries work normally, no batch-coordinator goroutine to crash-recover.

- **D-16:** Per-partition failure semantics: **partition failures are independent.** A failed partition does NOT halt sibling partitions in the same backfill (acceptance criterion 4). Each partition run lives or dies on its own per-asset retry policy (Phase 2 D-15). The backfill summary view aggregates terminal states; operator re-runs failures by submitting a new backfill scoped to the failed subset. No backfill-specific retry logic — that would shadow `Retry(...)` and confuse semantics.

### Event Log Additions

- **D-17:** New `event_type` enum values added to extend Phase 1 D-10 / Phase 2 D-18:
  - **Schedule:** `schedule.fired`, `schedule.missed`, `schedule.paused`, `schedule.resumed`
  - **Sensor:** `sensor.evaluated`, `sensor.fired`, `sensor.evaluation_failed`, `sensor.disabled`, `sensor.cooldown_skipped`, `sensor.dedup_skipped`
  - **Backfill:** `backfill.submitted`, `backfill.run_enqueued`, `backfill.completed`
  - **Partition:** partition lifecycle is already covered by the standard `run.*` events; no new types needed (run rows carry `partition_key`).

  All new types follow the Phase 1 D-09 RLS-immutability rules — append-only, no UPDATE/DELETE permissions for the application DB user.

### Claude's Discretion

- Exact tick-loop timing tolerance: scheduler tick interval defaults to 30s; allowed jitter and the precise SQL for `next_fire_at` recomputation are implementation details.
- Whether `schedules` and `sensors` ent entities share a base mixin or stay independent.
- Internal layout of priority enum mapping (string ⇄ int) — must be consistent but the exact representation is open.
- CLI output format for `./platform backfill status` (plain text vs structured) — pick one, ship one.
- Whether the `backfill_id` is a UUID, a timestamp-prefixed string, or a sortable ID — operators need to copy/paste it; user UX call.
- Whether sensor `consecutive_failures` reset on the first successful evaluation or require explicit operator reset — defaulting to auto-reset on success unless test data argues otherwise.
- Whether `WeeklyPartitions` defaults to ISO weeks (Mon-Sun) vs locale weeks — pick ISO (D-11 already implies it) and document.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Requirements & Roadmap
- `.planning/REQUIREMENTS.md` — Phase 3 in-scope: ORCH-05 (cron), ORCH-06 (sensors), ORCH-07 (time partitions), ORCH-08 (category partitions)
- `.planning/ROADMAP.md` §Phase 3 — four acceptance criteria + dependency on Phase 2

### Project Context
- `.planning/PROJECT.md` §关键决策 — concurrency token pool single-pool mandate (carried through D-13)
- `.planning/phases/01-infrastructure/01-CONTEXT.md` — Phase 1 decisions: D-09 RLS immutability of event_log, D-10 event_type enum extension model
- `.planning/phases/02-execution-engine/02-CONTEXT.md` — Phase 2 decisions: D-01 builder DSL, D-02 multi-mode binary, D-04 MaterializeResult.Metadata hook, D-14..D-18 retry / concurrency / claim / event extensions

### Research (must-read for Phase 3)
- `.planning/research/ARCHITECTURE.md` §SchedulerDaemon (line 42) — Dagster scheduler architecture reference
- `.planning/research/ARCHITECTURE.md` §Scheduler (line 433) — scheduler responsibilities and dependencies
- `.planning/research/PITFALLS.md` §陷阱 6 (line 137) — Backfill resource isolation: REQUIRES priority queue before backfill API ships (mitigated by D-13/D-14/D-15/D-16)
- `.planning/research/PITFALLS.md` §陷阱 1 (line 15) — Run state atomicity: claim ORDER BY change must preserve 50-goroutine atomicity test (constraint on D-13)
- `.planning/research/PITFALLS.md` §陷阱 2 (line 40) — Single concurrency pool mandate carries through to backfill resource tagging (D-13 layer 3)
- `.planning/research/PITFALLS.md` §阶段特定警告 (line 300) — explicit Phase 3 scheduler/concurrency/backfill warnings table
- `.planning/research/PITFALLS.md` external link — LakeFS backfill guide (https://lakefs.io/blog/backfilling-data-foolproof-guide/)
- `.planning/research/STACK.md` — note: River is documented but NOT installed (D-03 confirms staying off River for v1)

### Tech Stack & Conventions
- `CLAUDE.md` §技术栈 — robfig/cron/v3 is acceptable cron parser; River documented but D-03 deliberately stays off River
- `CLAUDE.md` §备选方案对比 — confirms rejection of in-process plugins, GORM, Gin, Fiber (no Phase-3-specific reversals)

### Phase 2 Code (frozen contracts Phase 3 builds on)
- `internal/asset/builder.go`, `asset.go` — builder DSL surface to extend with `.Schedule(...)`, `.Sensor(...)`, `.Partitions(...)`
- `internal/asset/io.go` — `AssetIO` interface to extend with `PartitionKey() string`
- `internal/run/claim.go` — FIFO claim query to update with priority-then-queued_at ordering (D-13)
- `internal/run/lifecycle.go`, `state.go` — state machine (no transition changes; only event_type additions)
- `internal/runtime/executor.go` — executor unchanged; reads `partition_key` from claimed run and passes through
- `internal/event/event.go`, `writer.go`, `types.go` — event writer reused; `event_type` enum extended (D-17)
- `internal/concurrency/` — token pool reused for backfill resource tag (D-13 layer 3)
- `cmd/platform/main.go`, `factories.go` — add `scheduler` and `backfill` subcommands alongside existing server/worker/materialize
- `migrations/20260507120000_phase2_run_tables.sql` — `runs` table to extend with `partition_key`, `priority`, `backfill_id` columns + unique constraint

### External References
- robfig/cron/v3: https://pkg.go.dev/github.com/robfig/cron/v3 — cron expression parser and `Next()` API used per D-03
- Dagster scheduling docs: https://docs.dagster.io/concepts/partitions-schedules-sensors — partition + sensor model reference
- Dagster issue #25743 (concurrency layering deadlock) — informs D-13 single-pool reuse
- Dagster issue #15155 (duplicate runs in backfills) — informs D-13 ORDER BY + atomicity preservation

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets (from Phase 2)
- **`asset.Builder` / `asset.Asset`** — Phase 3 extends with three new chained builder methods (`Schedule`, `Sensor`, `Partitions`); SDK API additions, no breaking changes.
- **`asset.AssetIO`** — Phase 3 adds `PartitionKey() string` accessor; existing `Read`/`Write` semantics unchanged.
- **`run.ClaimNext` / `runs` table** — Phase 3 adds `partition_key`, `priority`, `backfill_id` columns and a priority-aware ORDER BY. Atomicity guarantee (50-goroutine test) must be preserved through the change.
- **`runtime.Executor`** — unchanged. Reads claimed run with new fields and forwards `partition_key` into `AssetIO`. No new lifecycle states.
- **`concurrency.Pool`** — reused for backfill resource tag; no API changes.
- **`event.Writer`** — reused; only the `event_type` enum (Phase 1 D-10) gains new values per D-17.
- **`storage.Storage` / ent client** — Phase 3 adds three new ent entities (`Schedule`, `Sensor`, `Backfill`) and migration for run-table column additions, all via the established ent + Atlas pattern (Phase 1 D-04).
- **`cmd/platform/main.go` switch** — Phase 3 adds two new cases: `scheduler` and `backfill` (compile-time consistent with the existing `server`/`worker`/`materialize` shape).

### Established Patterns
- `SELECT … FOR UPDATE SKIP LOCKED` (Phase 2 D-17) is the universal multi-replica safety primitive — Phase 3 reuses it for both schedule firing and sensor evaluation.
- Subcommand-per-mode binary (Phase 2 D-02) — adding `scheduler` and `backfill` subcommands is purely additive.
- Functional builder + `.Register()` (Phase 2 D-01) — Phase 3 adds chained methods; ordering of method calls remains irrelevant.
- Append-only event log with PostgreSQL RLS (Phase 1 D-09) — Phase 3 events follow the same model; no UPDATE/DELETE on `event_log`.
- ent + Atlas migration with hand-managed CHECK constraints and role grants (Phase 2 migration `20260507120000_phase2_run_tables.sql`) — same pattern for new schedules/sensors/backfills tables.

### Integration Points
- `internal/asset/` — DSL extensions (Schedule/Sensor/Partitions builder methods, ent-free Go types)
- `internal/schedule/` (NEW) — scheduler tick loop, schedules table CRUD, missed-window detection
- `internal/sensor/` (NEW) — sensor evaluation loop sharing the scheduler subcommand goroutine pool
- `internal/partition/` (NEW) — partition strategies (Daily/Weekly/Monthly/Category), partition-key generation, validation
- `internal/backfill/` (NEW) — backfill submission service (CLI handler), partition-spec parsing, mass-enqueue logic
- `internal/run/claim.go` — modify ORDER BY to add `priority ASC` first; preserve atomicity test
- `cmd/platform/scheduler.go` (NEW) — `scheduler` subcommand entry point, ties together schedule + sensor loops
- `cmd/platform/backfill.go` (NEW) — `backfill` and `backfill status` subcommand handlers
- `migrations/2026MMDDHHMMSS_phase3_*.sql` — schedules, sensors, backfills tables + run column additions

</code_context>

<specifics>
## Specific Ideas

- **No River.** Phase 2 documented River as the queue but never installed it. Phase 3 deliberately stays on the SKIP LOCKED + heartbeat reaper model. River is reconsidered if/when Phase 4+ pulls it in for transactional inbox patterns; not Phase 3 work.
- **Scheduler and sensor share a daemon.** Both are "evaluate-due-rows-and-enqueue-runs" loops over the same primitive. Splitting into two binaries is operational overkill for v1.
- **Partition key in `runs.partition_key`, not a separate table.** Smallest schema change that lets every existing run mechanism (claim, retry, heartbeat reaper, event log) work for partitioned runs unmodified. The acceptance-criterion-3 promise of "each partition has its own event log entries" is satisfied automatically because each partition is its own run.
- **Three-layer backfill isolation is non-negotiable.** PITFALLS #6 explicitly says priority queue MUST be designed before the backfill API ships. The combination of priority claim ordering AND token-pool resource tag prevents both queue-position starvation and connector saturation; either alone fails a different half of the pitfall.
- **The 50-goroutine claim atomicity test (Phase 2 verification deliverable) must continue to pass.** D-13's claim ORDER BY change is additive (just adds a primary sort key); the SKIP LOCKED + CHECK + WHERE state='queued' triple guard remains intact. Phase 3 plans MUST re-run this test as part of acceptance.
- **Sensor `Payload` is a Phase 4 lineage hook.** Mirrors Phase 2 D-04 reasoning: structure-free `map[string]any` now, lineage extension reads it later.
- **Time partitions store UTC strings.** TZ on the spec is for cron alignment and display, not key identity. DST landmines avoided by construction.

</specifics>

<deferred>
## Deferred Ideas

- **Per-schedule missed-fire policy (`OnMissed: Skip | LatestOnly | All`)** — D-04 ships LatestOnly only. Per-asset override is a v1.x polish if real users hit it.
- **REST `/backfills` endpoint** — Phase 6 UI dependency, not Phase 3 scope. CLI is the v1 backfill submission surface.
- **Cursor-based sensor contract** — `func(ctx, cursor) (fires []SensorFire, newCursor, error)` is more powerful for high-volume sources (S3-new-objects, Kafka offsets), but D-06 ships the simpler single-fire-per-tick contract first. Cursor sensors revisit when the first user hits the limit.
- **Sensor + Partition `SensorResult.PartitionKey` semantics** — D-12 says it's allowed; exact behavior when key is omitted (default to "current latest" partition) is a planning detail and may move to D- when planner pins it.
- **Dynamic partition strategies** — D-09 leaves room for `DynamicPartitions` (DB-driven category lists), but v1 ships only static `CategoryPartitions{Keys}`. Dynamic strategies revisit when a real use case appears.
- **Partition dependency mapping** — when a partitioned downstream asset reads a partitioned upstream asset, how is the upstream-partition selection done? (Same key? Window join? User-supplied PartitionMapping?) Phase 3 ships independent-partition assets; partition-to-partition dependency mapping deferred to Phase 4 (lineage) where the partition-aware DAG semantics naturally co-design.
- **Partition pause / disable** — operator-driven schedule/sensor pause is captured in the schema (`paused_at` column per D-02) but the CLI/REST surface for pausing is Phase 6 UI work.
- **Sensor secret/credential injection** — D-05 sensors run inside the scheduler process and rely on the same env-var-interpolated config that Phase 2 D-09 uses for connector credentials. Vault/KMS integration is still a v2 concern (Phase 2 deferred list).
- **Backfill cancellation** — operator may want to cancel an in-flight backfill. Phase 3 v1 relies on per-run cancellation (already supported by Phase 2 state machine: `running → canceled`). Bulk cancel-by-backfill_id is a v1.x convenience feature.
- **River migration** — if Phase 4+ benefits from River's transactional inbox or Web UI, revisit. Phase 3 stays off River.
- **Load testing of priority-then-FIFO claim under heavy backfill** — the 50-goroutine test (Phase 2) covers atomicity. A separate load profile (1000 backfill rows + 50 normal rows + 50 concurrent claimers asserting normal-runs-claimed-first) is a recommended Phase 3 verification artifact, called out by D-13 but not yet planned.

</deferred>

---

*Phase: 03-scheduling-sensors-partitions*
*Context gathered: 2026-05-08*
