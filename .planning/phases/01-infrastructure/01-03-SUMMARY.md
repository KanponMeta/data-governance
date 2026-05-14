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
  - T-03-03 (JWT claims tampering): WithValidMethods(["HS256"]) rejects algorithm confusion
  - T-03-05 (Information disclosure): signing key bytes never in error messages, JWTSigningKey is []byte
---

# Phase 01 Plan 03: 加密原语和配置加载器总结

## 一句话总结

纯 Go 认证基础，带 bcrypt 密码哈希 (cost 12)、JWT 发放/验证 (仅 HS256 白名单)、以及在启动时验证 32 字节签名密钥的环境驱动配置加载器。

## 已构建内容

Plan 03 在任何 HTTP surface area 存在之前建立安全关键原语:

1. **bcrypt 密码哈希** (`internal/auth/password.go`) - HashPassword/VerifyPassword，成本 12，所有失败返回统一的 ErrInvalidCredentials 哨兵 (防止用户枚举)
2. **JWT 发放/验证器** (`internal/auth/jwt.go`) - TokenIssuer 通过 WithValidMethods 实现仅 HS256 白名单，ErrTokenExpired 返回非 nil Claims 以供审计日志记录
3. **配置加载器** (`internal/config/config.go`) - Load() 在任何 HTTP 监听器绑定之前验证 DATABASE_URL 必需和 JWT_SIGNING_KEY >= 32 字节

## 提交

| 提交 | 描述 |
|--------|-------------|
| 5b475bd | fix(01-03): correct config tests (typo: err != err, unused import, byte-length fixes) |
| a9fdcff | feat(01-03): config loader with 32-byte signing key enforcement |
| 2571a58 | chore(01-03): add golang-jwt/jwt/v5 and golang.org/x/crypto dependencies |
| 62c5d6b | test(01-03): add failing test for bcrypt password hashing |
| cf9b21b | test(01-03): add failing test for JWT issuer/verifier |

## 威胁缓解

| 威胁 | 缓解措施 | 文件 |
|--------|------------|------|
| T-03-01 Password spoofing | bcrypt cost=12, constant-time CompareHashAndPassword | password.go |
| T-03-02 JWT bearer spoofing | HMAC-SHA256 with >= 32 byte key enforced at config.Load | config.go, jwt.go |
| T-03-03 JWT algorithm confusion | WithValidMethods(["HS256"]) rejects none/HS384/HS512 | jwt.go |
| T-03-05 Key disclosure in logs | JWTSigningKey is []byte (not string) | config.go |

## Plan 05 (Auth Service) 的注意事项

1. **中间件必须将 ErrTokenExpired 处理为 401** - ErrTokenExpired 返回非 nil *Claims 且 UserID 已填充。中间件应在返回 401 之前使用 claims.UserID 发出 auth.token_expired 事件。
2. **ErrInvalidToken 是包装的** - 使用 errors.Is(err, auth.ErrInvalidToken) 进行比较，而不是直接的 == 比较。
3. **Config.JWTSigningKey 是 []byte** - 如果需要日志记录，%v 打印 [107 101 121 ...] 而不是字面密钥。
4. **密码错误映射到 400** - HashPassword 对 len < 8 或 len > 72 返回错误；调用者映射到 HTTP 400。
5. **VerifyPassword 对所有失败返回 ErrInvalidCredentials** - 包括格式错误的哈希。这是故意的 (T-03-04) - 不要试图区分失败模式。
6. **JWTAccessTTL 默认 15m，JWTRefreshTTL 默认 168h** - config.go 中 getEnvDefault 调用设置的配置默认值。

## 验证结果

| 检查 | 结果 |
|-------|--------|
| `go build ./internal/auth/... ./internal/config/...` | PASS |
| `go vet ./internal/auth/... ./internal/config/...` | PASS |
| `go test -race -count=1` (config: 8 cases, auth: 20 cases) | PASS |
| `bcryptCost = 12` in password.go | PASS |
| `minSigningKeyBytes = 32` in config.go | PASS |
| `WithValidMethods` in jwt.go | PASS |
| `jwt.SigningMethodNone` + `UnsafeAllowNoneSignatureType` in jwt_test.go | PASS |
| `Config.JWTSigningKey` is `[]byte` (not `string`) | PASS |

## 自我检查

所有声明已验证:
- 提交存在: 5b475bd, a9fdcff, 2571a58, 62c5d6b, cf9b21b
- 创建的文件: internal/auth/{password.go,password_test.go,jwt.go,jwt_test.go}, internal/config/{config.go,config_test.go}
- 测试数量: 28 个总计 (8 个 config + 20 个 auth 子测试分布在 5 个测试函数中)
- 所有来自 PLAN.md 的验收标准已通过 grep + 自动化检查验证