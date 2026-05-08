# Phase 3: 调度、传感器与分区 - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-08
**Phase:** 03-scheduling-sensors-partitions
**Areas discussed:** Scheduler architecture, Sensor model, Partition + DSL composition, Backfill priority + isolation

---

## Scheduler architecture

### Q1: Where should the scheduler daemon run?

| Option | Description | Selected |
|--------|-------------|----------|
| New `scheduler` subcommand | `./platform scheduler` alongside server/worker/materialize. Clean separation, independent scaling. Matches D-02 multi-mode pattern. | ✓ |
| Embedded in `worker` | In-process scheduler goroutine in every worker. Needs leader election to avoid multi-replica double-firing. | |
| Embedded in `server` | In API server process. Mixes data-plane into control-plane node; still needs leader election. | |

**User's choice:** New `scheduler` subcommand
**Notes:** Aligns with Phase 2 D-02 single-binary multi-mode pattern; scheduler down ≠ worker down.

### Q2: How are upcoming scheduled runs persisted?

| Option | Description | Selected |
|--------|-------------|----------|
| Lazy `schedules` table holds (asset_name, cron_expr, last_fire_at) | Each tick selects due rows, enqueues `runs`. `runs` table holds only claimable rows. Survives restart via persisted last_fire_at. | ✓ |
| Eager pre-enqueue with `not_before` | Pre-insert future runs; modify claim query to filter `not_before <= NOW()`. Bloats `runs` table; changes claim semantics. | |
| Hybrid: lazy + pre-enqueue immediate-next | Persist last_fire_at lazily, pre-enqueue exactly one upcoming run per schedule. Middle ground; more moving parts. | |

**User's choice:** Lazy `schedules` table
**Notes:** Smallest blast radius — preserves Phase 2 D-17 atomicity guarantee unchanged. `runs` continues to mean "claimable now."

### Q3: Which library implements the cron tick?

| Option | Description | Selected |
|--------|-------------|----------|
| Custom DB-backed loop using `robfig/cron/v3` for parsing only | Use parser + `Next()` API; tick driven by SELECT FOR UPDATE SKIP LOCKED. Same primitive as run claiming. Multi-replica safe without leader election. | ✓ |
| Introduce River with PeriodicJobs | Add riverqueue/river (currently absent from go.mod). Big architectural shift; replaces or layers over current claim model. | |
| robfig/cron in-process scheduler with leader election | robfig's full Cron + Postgres advisory lock for leadership. Single-leader bottleneck; new failure modes (lock expiry, split-brain). | |

**User's choice:** Custom DB-backed loop using robfig/cron/v3 parser only
**Notes:** Confirms Phase 3 stays off River for v1. SKIP LOCKED is the universal multi-replica safety primitive.

### Q4: Catch-up on missed schedules

| Option | Description | Selected |
|--------|-------------|----------|
| Fire only the most recent missed window | Logs "missed N", enqueues one catch-up. Avoids run avalanche. Dagster default. | ✓ |
| Fire ALL missed windows | Every fire is meaningful. Risk: 12-hour outage on 5-min cron = 144 runs. | |
| Per-schedule policy: OnMissed Skip/LatestOnly/All | Most flexible. Largest test surface. | |

**User's choice:** Fire only the most recent missed window
**Notes:** Per-schedule policy override deferred to v1.x polish.

---

## Sensor model

### Q1: Where does sensor evaluation execute?

| Option | Description | Selected |
|--------|-------------|----------|
| In the `scheduler` subcommand alongside cron | Same daemon, same DB-backed lock pattern. Reuses schedules infrastructure. | ✓ |
| Separate `sensor` subcommand | Independent scaling for heavy sensors (long external API timeouts). More moving parts. | |
| Sensor evaluation as a regular run kind | Each tick is a queued run executed by workers. Maximally consistent; high event-log volume. | |

**User's choice:** In the `scheduler` subcommand
**Notes:** Sensors and cron are both "evaluate-due-rows-and-enqueue-runs" loops over the same primitive.

### Q2: User-facing sensor contract

| Option | Description | Selected |
|--------|-------------|----------|
| `func(ctx) (SensorResult{Fired, RunKey, Payload}, error)` | RunKey enables platform-side dedup. Payload is Phase 4 lineage hook. | ✓ |
| `func(ctx) (bool, error)` simple boolean | Minimal, no dedup, all logic pushed into user code. | |
| `func(ctx, cursor) (fires []SensorFire, newCursor, error)` cursor-based | Powerful for S3/Kafka high-volume sources. Overkill for typical sensors. | |

**User's choice:** SensorResult contract with Fired / RunKey / Payload
**Notes:** Cursor-based variant deferred until first user hits the simpler model's limit.

### Q3: Dedup approach

| Option | Description | Selected |
|--------|-------------|----------|
| RunKey-based dedup + minimum cooldown | Belt-and-suspenders. RunKey same as last = skip; OR cooldown active = skip. | ✓ |
| RunKey-based dedup only | Cleaner, trusts user to return distinct keys. | |
| Cooldown only (no RunKey) | Forces every sensor to set Cooldown. Loses dedup-by-identity use case. | |

**User's choice:** RunKey + cooldown
**Notes:** Cooldown defaults to 0 (off, opt-in); RunKey is the primary mechanism, cooldown is the safety net.

### Q4: Sense() error handling

| Option | Description | Selected |
|--------|-------------|----------|
| Log + emit `sensor.evaluation_failed` event, retry next tick | Treat as infra noise. Auto-disable after N consecutive failures (default 60). | ✓ |
| Treat sensor errors as failed runs | Maximum visibility; pollutes runs table with infra-not-data failures. | |
| Silent retry (no event) | Quietest, worst observability. | |

**User's choice:** Log + event + retry, auto-disable after threshold
**Notes:** Auto-disable surfaces stuck sensors loudly. Manual operator re-enable required.

---

## Partition + DSL composition

### Q1: Partition DSL shape

| Option | Description | Selected |
|--------|-------------|----------|
| Single `.Partitions(spec)` typed strategy interface | DailyPartitions / WeeklyPartitions / MonthlyPartitions / CategoryPartitions. New strategies plug in by adding types. | ✓ |
| Separate `.TimePartitions(...)` and `.CategoryPartitions(...)` methods | Two methods, no abstraction. Locks out future partition kinds without API changes. | |
| Generic `.Partitions(keys []string, kind PartitionKind)` | User passes raw keys; pushes time expansion + TZ correctness into user code. | |

**User's choice:** Single `.Partitions(spec)` typed strategy
**Notes:** Strategy pattern leaves room for `DynamicPartitions` etc. later without breaking the builder.

### Q2: Partitioned-run persistence

| Option | Description | Selected |
|--------|-------------|----------|
| Add `partition_key VARCHAR(128) NULL` to `runs` | Smallest schema change. Existing claim/retry/event_log all work unchanged. | ✓ |
| New `partition_runs` table linked to `runs` | Cleaner separation; new lifecycle code; JOIN overhead. | |
| Encode partition into asset_name (`sales:2024-01-01`) | No schema change. Breaks asset-name semantics; messy for Phase 4+ lineage/governance. | |

**User's choice:** Add `partition_key` column to `runs`
**Notes:** Plus `(asset_name, partition_key)` unique constraint scoped to in-flight states to prevent duplicate concurrent runs.

### Q3: Time-partition keying

| Option | Description | Selected |
|--------|-------------|----------|
| UTC start-of-window ISO-8601 strings (`2024-01-15`, `2024-W03`, `2024-01`) | Avoids DST landmines. Matches Dagster/dbt conventions. TZ on spec is for cron/display only. | ✓ |
| TZ-aware key string (`2024-01-15America/New_York`) | Maximally explicit; long strings; comparison requires parsing. | |
| Unix epoch seconds | Simplest internal storage; loses human-readability. | |

**User's choice:** UTC ISO-8601 strings
**Notes:** Storage stays UTC; TZ-aware behavior delivered via spec, not key.

### Q4: Composition with other builder methods

| Option | Description | Selected |
|--------|-------------|----------|
| All compose orthogonally | Schedule + Sensor + Partitions + Retry + Resource all coexist. Mirrors Dagster. | ✓ |
| Schedule + Partitions only; Sensor + Partitions disallowed in v1 | Defers rare combination. Loses sensor-fired-backfill pattern. | |
| Mutually exclusive triggers | Strictest. Forces clean asset design. Loses flexibility. | |

**User's choice:** All compose orthogonally
**Notes:** Schedule fires the current-window partition; Sensor returns optional `PartitionKey`; both can be active independently.

---

## Backfill priority + isolation

### Q1: Backfill isolation strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Priority column + priority-then-FIFO claim + token pool resource tag | Three-layer defense: ordering preempts queue-position starvation; token tag prevents connector saturation. | ✓ |
| Token pool resource tag only | Pure D-16 reuse, no schema change. FIFO claim still suffers head-of-line blocking. | |
| Priority column only | Cheap claim reorder. Doesn't solve connector saturation half of PITFALLS #6. | |

**User's choice:** Three-layer (priority column + claim ORDER BY + token pool tag)
**Notes:** Each layer addresses a distinct half of PITFALLS #6. Phase 2's 50-goroutine atomicity test must continue to pass after the ORDER BY change.

### Q2: Backfill submission API

| Option | Description | Selected |
|--------|-------------|----------|
| CLI subcommand `./platform backfill <asset> --partitions=<spec>` | Mirrors `materialize` subcommand. Returns `backfill_id` immediately. | ✓ |
| REST `POST /backfills` only | Smaller binary surface; less convenient for dev loop. | |
| Both CLI and REST | More test surface; consistent with API-first. | |

**User's choice:** CLI subcommand
**Notes:** REST endpoint deferred to Phase 6 UI work.

### Q3: Backfill chunking

| Option | Description | Selected |
|--------|-------------|----------|
| Configurable `max_concurrent_backfill` (default 5); enqueue all upfront | Insert all 365 rows; claim limit + token pool enforce concurrency. Trivial progress query. | ✓ |
| Batched enqueue: scheduler enqueues N at a time, waits, repeats | Smaller `runs` mid-backfill; new batch coordinator goroutine to crash-recover. | |
| Per-partition single enqueue, parallelism via concurrency tokens only | Reverts to PITFALLS #6 failure mode. | |

**User's choice:** Enqueue all upfront, cap via `max_concurrent_backfill` and token pool
**Notes:** 365 rows up-front is acceptable. Progress queryable via `GROUP BY state`. Retries work normally.

### Q4: Per-partition failure semantics

| Option | Description | Selected |
|--------|-------------|----------|
| Continue — each partition independent; failures visible and re-runnable | Maps to acceptance criterion 4. Operator re-submits failed subset. | ✓ |
| Stop on first failure; cancel remaining | Conservative; useful for causally-ordered partitions; noisy on transient flakes. | |
| Retry-then-continue | Shadows existing per-asset Retry policy. Reject. | |

**User's choice:** Continue — partitions are independent
**Notes:** Existing Retry policy (Phase 2 D-15) covers per-partition retry; no backfill-specific retry layer needed.

---

## Claude's Discretion

Recorded in CONTEXT.md "Claude's Discretion" subsection. Summary of items left open:
- Tick-loop precise timing tolerance and `next_fire_at` recomputation SQL
- Whether `schedules` and `sensors` ent entities share a base mixin
- Internal layout of priority enum (string ⇄ int)
- CLI output format for `./platform backfill status`
- `backfill_id` shape (UUID vs sortable string)
- Sensor `consecutive_failures` reset semantics on first success
- ISO vs locale weeks for `WeeklyPartitions` (D-11 implies ISO)

## Deferred Ideas

Recorded in CONTEXT.md `<deferred>` section. Highlights:
- Per-schedule missed-fire policy override
- REST `/backfills` endpoint (Phase 6 UI dependency)
- Cursor-based sensor contract (high-volume sources)
- Dynamic partition strategies (DB-driven category lists)
- Partition-to-partition dependency mapping (Phase 4 lineage co-design)
- Schedule/sensor pause CLI/REST surface (Phase 6)
- Vault/KMS sensor credential injection (v2)
- Bulk backfill cancellation by `backfill_id`
- Possible River migration (Phase 4+ if it brings value)
- Load test of priority-then-FIFO claim under heavy backfill
