---
phase: 01-infrastructure
plan: 03
status: complete
tags:
  - auth
  - jwt
  - bcrypt
  - config
  - security

dependency_graph:
  requires:
    - 01-01 (go.mod, module structure)
    - 01-02 (storage layer)
  provides:
    - internal/auth/password.go (bcrypt hash + verify)
    - internal/auth/jwt.go (TokenIssuer, Issue, Verify)
    - internal/config/config.go (Load with validation)
  affects:
    - 01-05 (auth Service, middleware, chi router, main wiring)

tech_stack:
  added:
    - golang.org/x/crypto v0.50.0 (bcrypt)
    - github.com/golang-jwt/jwt/v5 v5.3.1 (JWT signing/verification)
  patterns:
    - bcrypt cost=12 (~250ms on 2026 laptop)
    - HS256-only algorithm allowlist via WithValidMethods
    - single ErrInvalidCredentials sentinel (no user enumeration)
    - ErrTokenExpired returns non-nil Claims for audit logging

key_files:
  created:
    - internal/auth/password.go (HashPassword, VerifyPassword, bcryptCost=12)
    - internal/auth/password_test.go (5 test cases for password primitives)
    - internal/auth/jwt.go (TokenIssuer, NewTokenIssuer, Issue, Verify, Claims)
    - internal/auth/jwt_test.go (9 test cases for JWT primitives)
    - internal/config/config.go (Load, minSigningKeyBytes=32, Config struct)
    - internal/config/config_test.go (8 test cases for config loader)
  modified:
    - go.mod (added jwt/v5, upgraded crypto/sync/mod/text)
    - go.sum (updated checksums)

decisions:
  - bcrypt cost 12 selected to balance security (~250ms) against UX (interactive login)
  - HS256-only allowlist enforced via WithValidMethods (rejects HS384, HS512, none, RSA)
  - Verify returns non-nil Claims on ErrTokenExpired so middleware can record actor_id for auth.token_expired event
  - Config.JWTSigningKey is []byte (not string) to prevent accidental %v from printing key bytes
  - All Verify failures return ErrInvalidToken (not errors.Is) since errors are wrapped

metrics:
  duration_minutes: ~30
  completed_date: "2026-05-06T08:00:00Z"
  tasks_completed: 3
  commits: 6
  files_created: 6
  files_modified: 2

deviations:
  - Go 1.25 required (Go 1.22 had gccgo build issues with internal/abi redeclaration)
  - Fixed config_test.go: 'if err !=' typo corrected, unused os import removed, byte-length strings corrected
  - jwt_test.go uses errors.Is(err, ErrInvalidToken) since Verify wraps errors

threats_mitigated:
  - T-03-01 (Password spoofing): bcrypt cost=12, constant-time CompareHashAndPassword
  - T-03-02 (JWT bearer spoofing): HMAC-SHA256 with >= 32 byte key enforced at config.Load
  - T-03-03 (JWT claims tampering): WithValidMethods([\"HS256\"]) rejects algorithm confusion
  - T-03-05 (Information disclosure): signing key bytes never in error messages, JWTSigningKey is []byte
---

# Phase 01 Plan 03: Cryptographic Primitives and Config Loader Summary

## One-liner

Pure-Go auth foundation with bcrypt password hashing (cost 12), JWT issuance/verification with HS256-only allowlist, and env-driven config loader that validates 32-byte signing key at boot.

## What Was Built

Plan 03 establishes the security-critical primitives before any HTTP surface area exists:

1. **bcrypt password hashing** (`internal/auth/password.go`) - HashPassword/VerifyPassword with cost 12, uniform ErrInvalidCredentials sentinel for all failures (prevents user enumeration)
2. **JWT issuer/verifier** (`internal/auth/jwt.go`) - TokenIssuer with HS256-only allowlist via WithValidMethods, ErrTokenExpired returns non-nil Claims for audit logging
3. **Config loader** (`internal/config/config.go`) - Load() validates DATABASE_URL required and JWT_SIGNING_KEY >= 32 bytes before any HTTP listener binds

## Commits

| Commit | Description |
|--------|-------------|
| 5b475bd | fix(01-03): correct config tests (typo: err != err, unused import, byte-length fixes) |
| a9fdcff | feat(01-03): config loader with 32-byte signing key enforcement |
| 2571a58 | chore(01-03): add golang-jwt/jwt/v5 and golang.org/x/crypto dependencies |
| 62c5d6b | test(01-03): add failing test for bcrypt password hashing |
| cf9b21b | test(01-03): add failing test for JWT issuer/verifier |

## Threat Mitigation

| Threat | Mitigation | File |
|--------|------------|------|
| T-03-01 Password spoofing | bcrypt cost=12, constant-time CompareHashAndPassword | password.go |
| T-03-02 JWT bearer spoofing | HMAC-SHA256 with >= 32 byte key enforced at config.Load | config.go, jwt.go |
| T-03-03 JWT algorithm confusion | WithValidMethods(["HS256"]) rejects none/HS384/HS512 | jwt.go |
| T-03-05 Key disclosure in logs | JWTSigningKey is []byte (not string) | config.go |

## Gotchas for Plan 05 (Auth Service)

1. **Middleware MUST handle ErrTokenExpired as 401** - ErrTokenExpired returns non-nil *Claims with UserID populated. Middleware should emit auth.token_expired event with claims.UserID before returning 401.
2. **ErrInvalidToken is wrapped** - Use errors.Is(err, auth.ErrInvalidToken) for comparison, not direct == comparison.
3. **Config.JWTSigningKey is []byte** - If logging is needed, %v prints [107 101 121 ...] not the literal key.
4. **Password errors map to 400** - HashPassword returns error for len < 8 or len > 72; caller maps to HTTP 400.
5. **VerifyPassword returns ErrInvalidCredentials for ALL failures** - Including malformed hash. This is intentional (T-03-04) - do NOT attempt to distinguish failure modes.
6. **JWTAccessTTL default is 15m, JWTRefreshTTL default is 168h** - Config defaults set in config.go getEnvDefault calls.

## Verification Results

| Check | Result |
|-------|--------|
| `go build ./internal/auth/... ./internal/config/...` | PASS |
| `go vet ./internal/auth/... ./internal/config/...` | PASS |
| `go test -race -count=1` (config: 8 cases, auth: 20 cases) | PASS |
| `bcryptCost = 12` in password.go | PASS |
| `minSigningKeyBytes = 32` in config.go | PASS |
| `WithValidMethods` in jwt.go | PASS |
| `jwt.SigningMethodNone` + `UnsafeAllowNoneSignatureType` in jwt_test.go | PASS |
| `Config.JWTSigningKey` is `[]byte` (not `string`) | PASS |

## Self-Check

All claims verified:
- Commits exist: 5b475bd, a9fdcff, 2571a58, 62c5d6b, cf9b21b
- Files created: internal/auth/{password.go,password_test.go,jwt.go,jwt_test.go}, internal/config/{config.go,config_test.go}
- Test count: 28 total (8 config + 20 auth subtests across 5 test functions)
- All acceptance criteria from PLAN.md verified via grep + automated checks