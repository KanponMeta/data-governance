---
phase: 05-governance
reviewed: 2026-05-10T02:06:36Z
depth: quick
files_reviewed: 91
files_reviewed_list:
  - cmd/platform/audit.go
  - cmd/platform/governance.go
  - cmd/platform/governance_test.go
  - cmd/platform/main.go
  - cmd/platform/policy.go
  - cmd/platform/reconciler.go
  - cmd/platform/reconciler_test.go
  - cmd/platform/role.go
  - cmd/platform/scheduler.go
  - cmd/platform/worker.go
  - configs/notifications.example.yaml
  - configs/policies.example.yaml
  - internal/api/audit_handlers.go
  - internal/api/governance_handlers.go
  - internal/api/policy_handlers.go
  - internal/api/role_handlers.go
  - internal/api/router.go
  - internal/asset/asset.go
  - internal/asset/builder.go
  - internal/asset/builder_test.go
  - internal/asset/fingerprint.go
  - internal/asset/io_masking.go
  - internal/asset/io_masking_test.go
  - internal/asset/quality_builder_test.go
  - internal/asset/types.go
  - internal/audit/anchor.go
  - internal/audit/canonical.go
  - internal/audit/export.go
  - internal/audit/retention.go
  - internal/audit/types.go
  - internal/audit/verify.go
  - internal/audit/writer.go
  - internal/auth/casbin.go
  - internal/auth/jwt.go
  - internal/auth/middleware.go
  - internal/auth/rbac_model.conf
  - internal/auth/service.go
  - internal/connector/capability.go
  - internal/connector/firstparty/bigquery/masking.go
  - internal/connector/firstparty/postgres/query_aggregate.go
  - internal/connector/firstparty/snowflake/masking.go
  - internal/connector/mask_types.go
  - internal/event/event.go
  - internal/event/types.go
  - internal/governance/auto_approval.go
  - internal/governance/auto_approval_test.go
  - internal/governance/handler.go
  - internal/governance/handler_test.go
  - internal/governance/pii_propagator.go
  - internal/governance/pii_propagator_test.go
  - internal/governance/reviewers.go
  - internal/governance/reviewers_test.go
  - internal/governance/sla_scanner.go
  - internal/governance/sla_scanner_test.go
  - internal/governance/workflow.go
  - internal/governance/workflow_test.go
  - internal/lineage/capture.go
  - internal/lineage/capture_test.go
  - internal/notification/channel.go
  - internal/notification/router.go
  - internal/notification/smtp.go
  - internal/notification/template.go
  - internal/notification/webhook.go
  - internal/notification/worker.go
  - internal/platform/registry.go
  - internal/policy/handler.go
  - internal/policy/mask.go
  - internal/policy/mask_test.go
  - internal/policy/reconciler.go
  - internal/policy/store.go
  - internal/policy/store_test.go
  - internal/policy/sync_job.go
  - internal/policy/yaml_loader.go
  - internal/quality/dispatcher.go
  - internal/quality/evaluator.go
  - internal/quality/freshness.go
  - internal/quality/rule.go
  - internal/quality/store.go
  - internal/runtime/executor.go
  - internal/runtime/executor_mask_test.go
  - internal/runtime/executor_test.go
  - internal/runtime/export_test.go
  - internal/runtime/hooks.go
  - internal/runtime/quality_executor_test.go
  - internal/schedule/daemon_freshness_test.go
  - internal/schedule/daemon.go
  - internal/storage/ent/schema/asset_version.go
  - internal/storage/ent/schema/governance_review.go
  - migrations/20260510000001_phase5_audit_rbac.sql
  - migrations/20260510000002_phase5_column_policies.sql
  - migrations/20260510000003_phase5_quality.sql
  - migrations/20260510000004_phase5_pii_propagation.sql
  - migrations/20260510000005_phase5_governance_workflow.sql
findings:
  critical: 6
  warning: 11
  info: 6
  total: 23
status: issues_found
---

# Phase 5: Code Review Report

**Reviewed:** 2026-05-10T02:06:36Z
**Depth:** quick
**Files Reviewed:** 91
**Status:** issues_found

## Summary

Phase 5 introduces the security/governance backbone of the platform: hash-chain audit log, Casbin RBAC, column-level masking (Snowflake DDM, BigQuery CLS, in-pipeline), PII propagation, governance approval workflow, quality DSL, and notifications. Defensive practices are visible in many places — `crypto/subtle.ConstantTimeCompare` is documented for HMAC verification, `WithValidMethods` JWT allowlist prevents `alg=none`, masking DDL uses Postgres-style identifier quoting (`""`), Casbin enforces RBAC on every governance/audit/policy endpoint, RLS is enabled on `audit.audit_log`, and `crypto/rand` (not `math/rand`) is used for invite tokens.

However, several CRITICAL bugs threaten the core guarantees of the phase:

1. **Audit hash-chain integrity is broken** by a timestamp mismatch between hash computation and row insertion (`internal/audit/writer.go`). Every entry where the caller passes a zero `OccurredAt` (which is the case for `emitVerifyFailedEntry`, `emitExportAuditEntry`, and many SLA-scanner / sync-job paths) will store a hash that does not match the stored timestamp — making `audit.Verify` report tamper detection on a clean chain.
2. **`audit.Verify` cannot run** at all: it scans `occurred_at` (a `TIMESTAMPTZ`) into an `int64` variable. pgx will reject this on first row, so the verification path (and `/audit/verify` REST endpoint, `audit verify` CLI) is non-functional.
3. **Governance approval bypass**: `Workflow.Approve` counts approvals by string-searching previous comments for `"approved by "` — a single decider can self-approve repeatedly, or a `Reject` comment containing the substring would be counted as an approval. There is no check that the decider belongs to the reviewer pool, that the decider is not the submitter (no four-eyes), or that the decider has not already voted. This subverts the entire SOC 2 governance posture the migration claims.
4. **In-process notification queue silently breaks transactional enqueue**: `InsertTx` ignores the supplied `*sql.Tx` and inserts immediately, contradicting documented contract. Notifications fire even when the originating tx rolls back.
5. **CLI accepts `ACTOR_ID` env var with no authentication** for governance submit/approve/reject. Anyone with shell access can decide reviews as any user UUID, and the audit chain records the spoofed UUID as the legitimate actor.
6. **Quality rule SQL is built via `fmt.Sprintf`** (`NullCheck`, `RangeCheck` in `internal/asset/types.go`) with column names embedded in `"..."` but without escaping embedded quotes — a column name containing `"` would break out and inject SQL into the warehouse.

Warnings cluster around: error masking in handlers (raw DB errors leaked to clients), governance gate fail-open race window, byte-vs-rune indexing in `ApplyPartial`, fragile substring matching of error messages for "table missing" detection, audit `WriteEntry` calls that omit `OccurredAt`, and the unused `havePrior` in PII propagator code paths. Info items cover dead code, magic numbers, and `==` instead of `errors.Is`.

## Critical Issues

### CR-01: Audit hash-chain integrity broken — hash uses input `OccurredAt`, row stores defaulted `time.Now()`

**File:** `internal/audit/writer.go:53-71`
**Issue:** `computeSelfHash` is called with `e.OccurredAt` BEFORE the zero-value default substitution applied at line 56-59. When a caller invokes `WriteEntry` without setting `OccurredAt` (common — see `emitExportAuditEntry`, `emitVerifyFailedEntry`, retry-failure paths), the hash is computed using `(0).UnixNano()` while the row is INSERTed with `time.Now().UTC()`. `Verify` recomputes from the stored timestamp and reports a chain mismatch on every such row.

**Fix:** Default the timestamp BEFORE hashing:
```go
// 4. Insert the audit row.
occurredAt := e.OccurredAt
if occurredAt.IsZero() {
    occurredAt = time.Now().UTC()
}
// ... actorID / expiresAt prep ...

// 3. Compute self_hash using the SAME occurredAt that will be persisted.
seq = prevSeq + 1
selfHash := computeSelfHash(seq, prevHash, occurredAt, e.EventType, e.ActorID, e.ResourceType, e.ResourceID, payloadBytes)
```

### CR-02: `audit.Verify` scans `TIMESTAMPTZ` into `int64` — verification path is non-functional

**File:** `internal/audit/verify.go:55, 61, 94`
**Issue:** Line 55 declares `var occurredAtUnixNano int64`. Line 61 scans `occurred_at` (a `TIMESTAMPTZ` column per migration 20260510000001 line 28) into that int64. pgx will reject this with a scan error on the first row, so the entire `Verify` function fails immediately. This means `/audit/verify` REST endpoint, `platform audit verify` CLI, and the recursive `emitVerifyFailedEntry` self-audit are all dead code paths. The audit package has no tests covering this codepath, which is why the bug has not been caught.

**Fix:** Scan into `time.Time` and convert:
```go
var occurredAt time.Time
if err := rows.Scan(&seq, &rowPrevHash, &storedHash, &occurredAt, &eventType, &actorID, &resourceType, &resourceID, &payload); err != nil {
    return Result{Err: fmt.Errorf("verify: scan row %d: %w", seq, err)}, nil
}
// ... pass occurredAt.UnixNano() to computeSelfHashFromRow:
computedHash := computeSelfHashFromRow(seq, prevHash, occurredAt.UnixNano(), eventType, actorStr, resourceType, resourceID, payload)
```

Also add a unit test for `audit.Verify` against a freshly-built chain — the absence of any test in `internal/audit/` is the root cause this slipped through.

### CR-03: Governance approval bypass — quorum counted by substring of unprivileged comment text

**File:** `internal/governance/workflow.go:316, 331-347, 573-584`
**Issue:** `decide()` builds a vote ledger by appending `"[approved by <uuid>]"` or `"[rejected by <uuid>]"` to the row's `comment` column. `countApprovals` then scans that text for `"approved by "`. Three failure modes:

1. **Self-approval / repeat approval**: there is no check that the decider has already voted. A user with `RequirePermission("/governance/reviews/*", "write")` can `POST /approve` `quorum-1` times themselves to flip the row to approved.
2. **Submitter == decider**: no four-eyes check. The submitter can approve their own review (assuming they hold both the engineer and governance permissions, or only the governance permission is checked).
3. **Substring collision**: a free-form `Reject` comment containing the literal `approved by ` (e.g., a reviewer rejecting because the change "should have been approved by privacy-team first") would count toward the next call's approvals tally.
4. **Membership in pool not checked**: any user holding the governance permission can decide on any review — even reviews routed exclusively to `pool.Roles=['privacy-team']`.

**Fix:** Replace string-ledger with a structured `governance_review_decisions` table (one row per (review_id, decider_id), unique key) and gate `decide()` on:
- decider_id not in already-voted set
- decider_id distinct from submitter_id (configurable)
- decider's roles intersect `pool.Roles`
- count of approval rows >= effective quorum

Until that schema lands, at minimum validate `decider != submitter` and reject second votes from the same decider in `decide()`:
```go
if decider == submitterID {
    return Review{}, errors.New("governance: decider cannot be submitter (four-eyes)")
}
if strings.Contains(currentComment, "by "+decider.String()+"]") {
    return Review{}, errors.New("governance: decider already voted on this review")
}
```

### CR-04: `InProcessQueue.InsertTx` ignores tx and inserts immediately — breaks documented atomic-enqueue contract

**File:** `internal/notification/worker.go:253-260`
**Issue:** The `InsertTx` doc comment says "we deliberately Insert AFTER tx.Commit() in production callers" but the code unconditionally calls `Insert` ignoring the supplied `*sql.Tx`. Callers use `q.InsertTx(ctx, tx, args)` BEFORE `tx.Commit()` (see `internal/governance/workflow.go:232`, `internal/governance/sla_scanner.go:148`, `internal/quality/freshness.go:141`). If `tx.Commit()` fails the audit row + governance row + sla_breach_emitted_at update all roll back, but the notification has already been queued and will dispatch — emitting a phantom "review submitted" / "SLA breached" event for a transition that never persisted.

**Fix:** Either (a) buffer the args until after commit by registering a `tx.Commit` hook (Go's `database/sql` has no such hook, so wrap commit explicitly) or (b) document that the in-process queue is best-effort and have callers pivot to `Insert` after commit:
```go
// Production callers: insert AFTER tx.Commit succeeds.
if err := tx.Commit(); err != nil { return err }
_ = w.queue.Insert(ctx, args) // post-commit
```
Long-term: swap to River, whose `InsertTx` honours the tx natively (this is exactly the gap River was selected to close).

### CR-05: CLI `getActorFromEnv` permits arbitrary actor spoofing into the hash-chain

**File:** `cmd/platform/governance.go:289-296`, `cmd/platform/policy.go:190-251`
**Issue:** The CLI reads `ACTOR_ID` from env without any authentication and passes it as the actor for `Submit / Approve / Reject / Reassign` — these calls write `actor_id` into the hash-chain audit log. Anyone with shell access can post `ACTOR_ID=<arbitrary-uuid> ./platform governance review <id> --approve` and the chain will record the impersonation as legitimate. The submit/reject/approve handlers cannot tell a CLI invocation from a server-internal actor at the audit-payload level.

**Fix:** Require CLI users to authenticate (`platform login` issuing a JWT, then store the JWT in `~/.platform/credentials.json`); read the user UUID from the verified JWT, never from env. Until that lands, mark every CLI-originated audit entry with a synthetic actor and a `payload.cli=true` attribute so chain readers can quarantine those rows:
```go
func getActorFromEnv() (uuid.UUID, error) {
    return uuid.Nil, errors.New("CLI auth not yet implemented; use REST API")
}
```
And gate the governance/role/policy CLI subcommands behind a build tag or env feature flag (`PLATFORM_CLI_DANGEROUS=1`) so production deployments cannot accidentally expose the bypass.

### CR-06: Quality rule SQL injection via unescaped column name in `NullCheck` / `RangeCheck`

**File:** `internal/asset/types.go:193-195, 240-242`
**Issue:** `NullCheck.Evaluate` builds SQL via `fmt.Sprintf("... \"%s\" IS NULL ... FROM %s", n.Column, eval.AssetTable())`. The column name is wrapped in `"..."` but quotes inside `n.Column` are NOT doubled. A column declared with name `email"; DROP TABLE x; --` would close the identifier quote and inject. Same defect in `RangeCheck.Evaluate` (line 240). `eval.AssetTable()` is also concatenated raw — its source is connector-specific but `connector.QualifiedTable(ref)` does not escape.

While column names typically come from trusted asset declarations, this is still an injection surface — a third-party connector returning attacker-controlled column names (e.g., from upstream catalog discovery) would compromise the warehouse. SQLAssertion is even more permissive (line 302: `strings.ReplaceAll(s.SQL, "${asset}", eval.AssetTable())`).

**Fix:** Escape identifiers and validate/sanitise:
```go
func quoteIdent(s string) string {
    return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
// NullCheck.Evaluate
sqlText := fmt.Sprintf(
    `SELECT COUNT(*)::float8 AS total, COUNT(*) FILTER (WHERE %s IS NULL)::float8 AS nulls FROM %s`,
    quoteIdent(n.Column), eval.AssetTable())
```
And add an identifier validator (e.g., `regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)`) on `Column` at builder-time so malformed names fail registration, not at warehouse-execution time.

## Warnings

### WR-01: `RequirePermission` checks only primary role; multi-role users may be denied access they should have

**File:** `internal/auth/middleware.go:128-150`
**Issue:** Comment claims "extracts all roles from the authenticated Principal's JWT" but the code only checks `p.Role` (singular, the primary role). The `Claims.Roles []string` field is populated by `Login` (per `jwt.go:34`) but never consulted at enforcement time. A user assigned both `data-engineer` and `governance` will only have the primary role enforced — defeats multi-role design.

**Fix:** Iterate roles:
```go
roles := []string{p.Role}
// (extract additional roles from principal — currently Principal.Roles is not in the struct)
for _, r := range roles {
    if allowed, _ := enforcer.Enforce("role:"+r, obj, act); allowed {
        next.ServeHTTP(w, r); return
    }
}
writeForbidden(w, ...)
```
And extend `Principal` to carry the full role set from JWT.

### WR-02: REST handlers leak raw DB error messages back to clients (information disclosure)

**File:** `internal/api/role_handlers.go:50, 62, 83, 109, 128`; `internal/policy/handler.go:85, 109, 128, 145, 151`; `internal/governance/handler.go:118, 197, 238`
**Issue:** Many handlers do `http.Error(w, err.Error(), 500)` or `writeProblem(w, 500, "Internal Server Error", err.Error())`. DB errors include schema names, table names, constraint names, and pgx wrapper text — all useful to an attacker mapping the system. `assignRolesHandler` would, for example, reveal that there is a FK on `roles.name` if the requester sends an unknown role.

**Fix:** Log the full error server-side; return only a generic detail to clients:
```go
slog.Error("assign_role failed", "actor", actor.UserID, "err", err)
writeProblem(w, 500, "Internal Server Error", "internal error; see server logs")
```

### WR-03: `handleVerify` only verifies seq=1, not the whole chain

**File:** `internal/api/audit_handlers.go:73`
**Issue:** Calls `audit.Verify(ctx, db, 1, 0)` — `to=0`. Inside `Verify`, `if to < from { to = from }` clamps `to=1`, so the endpoint scans exactly one row. Operators calling `/audit/verify` get a false "OK" even when later rows are tampered.

**Fix:** Look up `MAX(seq)` first (as `cmd/platform/audit.go:60-67` already does correctly), then call `Verify(ctx, db, 1, maxSeq)`.

### WR-04: `bytesEqual` for hash comparison should be `subtle.ConstantTimeCompare`

**File:** `internal/audit/verify.go:96, 126-136`
**Issue:** Locally-defined `bytesEqual` does not use constant-time comparison. While the audit hash is not a credential, exposing timing differences during chain verification could let a tampering attacker probe which prefix matched. Also the project explicitly mandates `subtle.ConstantTimeCompare` for HMAC checks elsewhere — for consistency, audit hash compare should use it.

**Fix:**
```go
import "crypto/subtle"
// ...
if subtle.ConstantTimeCompare(computedHash, storedHash) != 1 {
    // mismatch
}
```

### WR-05: `audit.WriteEntry` callers frequently omit `OccurredAt`, compounding CR-01

**File:** `internal/audit/export.go:220-230`; `internal/audit/verify.go:176-186`; `internal/policy/sync_job.go:113-126`
**Issue:** Multiple call sites construct `audit.Entry{ EventType: ..., ResourceType: ..., Payload: ... }` without setting `OccurredAt`. Combined with CR-01, every such row corrupts the chain. Even after CR-01 is fixed, every audit-emitting site should explicitly set `OccurredAt = time.Now().UTC()` so the test of "do callers always supply the timestamp" can be made into a vet rule.

**Fix:** Add a defensive check:
```go
func WriteEntry(ctx context.Context, tx *sql.Tx, e Entry) (int64, error) {
    if e.OccurredAt.IsZero() {
        e.OccurredAt = time.Now().UTC()
    }
    // ... rest of WriteEntry uses e.OccurredAt for both hash + insert
}
```
And update all call sites to set `OccurredAt` explicitly.

### WR-06: `ApplyPartial` byte-indexing breaks multi-byte UTF-8

**File:** `internal/policy/mask.go:88-97`
**Issue:** `len(value)`, `value[:reveal]`, `value[len(value)-reveal:]` all index by byte. For UTF-8 strings, this can split a rune mid-sequence and produce invalid UTF-8 in the masked output, or expose more characters than intended (e.g., a 3-byte character with `reveal=2` reveals 2 bytes of a 3-byte rune).

**Fix:** Convert to `[]rune`:
```go
func ApplyPartial(value string, reveal int) string {
    if reveal <= 0 { reveal = 2 }
    runes := []rune(value)
    if len(runes) <= 2*reveal+1 {
        return ApplyRedact(value)
    }
    mid := strings.Repeat("*", len(runes)-2*reveal)
    return string(runes[:reveal]) + mid + string(runes[len(runes)-reveal:])
}
```

### WR-07: `isUndefinedTable` substring matching is fragile

**File:** `internal/governance/auto_approval.go:372-380`
**Issue:** Detects "table does not exist" by `strings.Contains(err.Error(), "does not exist")`. Locale-dependent (Postgres translates messages), version-dependent, and matches false positives — e.g., a constraint error "value 'pii' does not exist in enum" would silently swallow as "table missing". Risk: legitimate query errors degrade to "fail-open, no policy needed", letting submissions auto-approve when they should error.

**Fix:** Use pgx error codes:
```go
import "github.com/jackc/pgx/v5/pgconn"
func isUndefinedTable(err error) bool {
    var pe *pgconn.PgError
    if errors.As(err, &pe) {
        return pe.Code == "42P01" || pe.Code == "42703" // undefined_table | undefined_column
    }
    return false
}
```

### WR-08: `connectorName` calls `Ping` with `context.Background()` — ignores caller cancellation

**File:** `internal/policy/sync_job.go:140-149`
**Issue:** When River is shutting down or the worker is cancelled, `Work` returns immediately, but if it called `connectorName` for a log line, that helper kicks off a `Ping` with background context — could hang indefinitely against an unresponsive warehouse. `Work` is already returning; the log-call leaks a goroutine.

**Fix:** Pass through the work context:
```go
func connectorName(ctx context.Context, c connector.Connector) string {
    resp, err := c.Ping(ctx, connector.PingRequest{})
    // ...
}
```
And update the four call sites.

### WR-09: Governance gate fail-open on missing `asset_versions` row creates a race-window bypass

**File:** `internal/runtime/executor.go:290-294`
**Issue:** When `governance_state` query returns `sql.ErrNoRows` the gate "allows the run to proceed (D-09 fail-open)". Comment justifies as "race during first registration." But this is exactly the bypass an attacker would target: register an asset with a never-before-seen `code_hash`, immediately enqueue a run, and the gate allows materialization with no governance review.

**Fix:** Fail-closed with retry — the registration race is expected to resolve within milliseconds, so a brief sleep + retry is more sound than skipping the gate:
```go
case errors.Is(err, sql.ErrNoRows):
    return fmt.Errorf("step %q: %w (asset_version not yet registered; retry shortly)", a.Name(), errMaterializationGated)
```

### WR-10: `CreateRole` audit-emits even when ON CONFLICT DO NOTHING means no row was created

**File:** `internal/auth/service.go:330-356`
**Issue:** The `INSERT ... ON CONFLICT (name) DO NOTHING` is paired with an unconditional `audit.WriteEntry`. When a duplicate `CreateRole` call hits an existing role, the chain records a `role.created` event for a row that already existed. Audit chain consumers cannot distinguish "first-time create" from "no-op replay."

**Fix:** Check `RowsAffected` before emitting the audit:
```go
res, err := tx.ExecContext(ctx, `INSERT INTO roles ... ON CONFLICT ...`, ...)
if err != nil { return err }
n, _ := res.RowsAffected()
if n == 0 { /* skip audit; role already exists */ return nil }
```

### WR-11: `dedupRoles` returns input slice unmodified when len <= 1, allowing aliasing mutation

**File:** `internal/governance/reviewers.go:139-153`
**Issue:** Returns `in` directly when `len(in) <= 1`. Callers (`Submit`, `Reassign`) hold this slice and may later mutate `pool.Roles` via `append(pool.Roles, ...)`. If the caller's source slice has spare capacity, the mutation will affect the original. Subtle aliasing bug.

**Fix:** Always copy:
```go
out := make([]string, 0, len(in))
seen := make(map[string]struct{}, len(in))
for _, r := range in {
    if _, ok := seen[r]; ok { continue }
    seen[r] = struct{}{}
    out = append(out, r)
}
return out
```

## Info

### IN-01: `err == sql.ErrNoRows` should be `errors.Is(err, sql.ErrNoRows)`

**File:** `internal/governance/pii_propagator.go:129, 301`
**Issue:** Direct equality fails when errors are wrapped. Project elsewhere uses `errors.Is`. Currently the direct compare works because the immediate caller is `tx.QueryRowContext().Scan` which returns `sql.ErrNoRows` un-wrapped, but consistency matters.

**Fix:** `case errors.Is(err, sql.ErrNoRows):`

### IN-02: `err == ErrTokenExpired` should be `errors.Is(err, ErrTokenExpired)`

**File:** `internal/auth/middleware.go:74`
**Issue:** Same as IN-01 — direct `==` works today only because `jwt.go` returns `ErrTokenExpired` directly. If a future caller wraps the error with `%w`, the comparison silently fails.

**Fix:** `if errors.Is(err, ErrTokenExpired) {`

### IN-03: Magic number 256 for max upstreams should be a named constant

**File:** `internal/lineage/capture.go:77-79`
**Issue:** `len(ups) > 256` repeats a constant inline. Named constant improves discoverability.

**Fix:**
```go
const maxDeclaredUpstreams = 256 // DoS guard: real assets have <50 upstreams
if len(ups) > maxDeclaredUpstreams { ... }
```

### IN-04: `havePrior` declared but only used by SetXxx-style branch, never by INSERT path in `applyOverride`

**File:** `internal/governance/pii_propagator.go:118-185`
**Issue:** The variable is set to `false` only in the `sql.ErrNoRows` case and to `true` in the default. Its sole reader is the `if !havePrior` branch on line 162. Reading the function, the dead-code feel is that `havePrior` is redundant — the same signal is in `priorAuditSeq.Valid` for the audit-emit decision. Minor but worth a refactor pass.

**Fix:** Drop the variable; use `if errors.Is(err, sql.ErrNoRows)` branching directly.

### IN-05: Commented-out / dead "event_log entry" in `Reassign`

**File:** `internal/governance/workflow.go:478-481`
**Issue:** Block comment "event_log entry (no hash-chain write; this is operational, not access-control)" followed by no code. Either implement or delete. The migration adds `governance.reviewer_reassigned` to the event_log CHECK constraint, so the event type is provisioned but never emitted — operators have no observability for the reassign action.

**Fix:** Append the event:
```go
_ = w.events.Append(ctx, event.Event{
    Type:         event.EventTypeGovernanceReviewerReassigned,
    ResourceType: "governance_review",
    ResourceID:   reviewID.String(),
    Payload: map[string]any{"actor": actor.String(), "old": oldPool, "new": newPool},
})
```

### IN-06: Notification rule patterns parsed inline; consider a Pattern type with `Match(eventType)` method

**File:** `internal/notification/router.go:109-122`
**Issue:** `matchPattern` is a free function that re-parses the same pattern on every call. For a config with N rules and M events, this is N*M parses. Trivial overhead today (M=10/sec, N=10) but easy to clean up.

**Fix:** Pre-compile into a typed `Pattern` (exact / wildcard / prefix) at `NewRouter` time and store on `RuleConfig`.

---

_Reviewed: 2026-05-10T02:06:36Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: quick_
