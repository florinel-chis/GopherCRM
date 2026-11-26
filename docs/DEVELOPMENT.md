# GopherCRM Development Guide

> **For:** Developers working in this repository. Part of the [documentation index](README.md).

Reference for developers working in this repository: commands, layering, and the conventions the
codebase enforces. For first-time setup instructions see [SETUP.md](SETUP.md); for the
feature and test-coverage matrix see [FEATURES.md](FEATURES.md).

This page keeps the overview, the commands, the architecture, the package map and the repository
layout, the tech stack, the conventions, the gotchas, the configuration and the API reference. The long-form sections live on seven topic
pages, listed under [Topic pages](#topic-pages).

## Project Overview

GopherCRM is a CRM system built in Go with a React TypeScript frontend (`gocrm-ui/`). It manages
users, leads, customers, companies, deals, tickets, tasks, products and quotes, and ships forms
(embeddable lead capture and ticket intake), follow-up automation, AEO (answer-engine visibility
tracking), outbound webhooks and API keys. Access is permission-based: the four system roles
(admin, sales, support, customer) and custom roles grant permission keys with a scope. Users sign
in with a password or through SSO (OpenID Connect), and an identity provider can provision
accounts over SCIM.

## Development Commands

### Backend

```bash
make build          # Build binary to bin/gophercrm
make run            # Run the app (go run ./cmd)
make test           # Run all tests (go test ./...)
make create-db      # Create MySQL database from scripts/create_database.sql
make deps           # Download and tidy Go modules
make clean          # Remove bin/
make build-tools    # Build CLI tools to bin/
make create-admin   # Run admin creation tool (requires build-tools first)

# Run a single test
go test -run TestFunctionName ./internal/service/

# Run tests for a specific package
go test ./internal/handler/

# Run the integration suites (in-memory SQLite, no MySQL needed)
go test ./test/integration/ ./tests/
```

### Frontend (run from `gocrm-ui/`)

```bash
npm run dev            # Vite dev server (expects the backend on :8080)
npm run build          # tsc -b && vite build
npm run preview        # Serve the production build locally
npm run lint           # ESLint (lint:fix to auto-fix)
npm run format         # Prettier over src/
npm test               # Vitest unit tests (watch mode)
npm test -- --run --maxWorkers=3   # one run on three workers (what make verify-frontend runs)
npm run test:coverage  # Vitest with coverage
npm run test:e2e       # Playwright E2E tests
npm run test:e2e:admin # Admin CRUD suites (uses playwright.config.slow.ts)
npm run test:e2e:report# Open the last Playwright HTML report

# Run a single E2E spec
npx playwright test e2e/tests/login.spec.ts
```

## Repository hooks

Activate the repository's guardrails once per clone:

```bash
git config core.hooksPath scripts/git-hooks
```

The pre-commit hook keeps the documentation tree deliberate: new folders under `docs/` are
refused unless added to the allowlist inside the hook, and local working papers
(`docs/specs/`, `docs/plans/` — gitignored) are refused even when force-added. It also runs an
optional content sweep driven by a machine-local term list when one is present.

## Architecture

Clean architecture with four layers, all behind interfaces for testability:

```
Handler (Gin HTTP) → Service (business logic) → Repository (GORM data access) → Models (domain entities)
```

- **Dependency injection** is manual, wired in `cmd/main.go` via `setupDependencies()`
- **All layers use interfaces** defined in `internal/repository/interfaces.go` and
  `internal/service/interfaces.go`
- **Repositories** accept `*gorm.DB` and implement `WithTx()` for transaction support
- **Handlers** parse requests, call services, and return unified responses via
  `utils.RespondSuccess`/`RespondError`
- **Route prefix**: API routes are mounted under `/api/v1` (configurable via `API_PREFIX`); `/health`
  and `/scim/v2` are mounted outside it
- **Identity layer**: `internal/idkit` holds the identity layer, described in
  [internal/idkit/README.md](../internal/idkit/README.md): `idkit` (the host interfaces in
  `internal/idkit/host.go`), `oidc` (OpenID Connect client helpers), `entra` (Entra ID rules),
  `rolemap` (claim-to-role mapping), `sso` (the sign-in), `scim` and `scim/server` (SCIM protocol
  and server), the test kits `oidctest` and `scim/scimtest`, and `example` (a runnable host over
  in-memory stores). It must not reach gin, gorm, `internal/models`, `internal/repository`,
  `internal/utils` or logrus, directly or through other packages;
  `internal/idkit/boundary_test.go` `TestIDKitImportBoundary` enforces this. GopherCRM wires its
  adapters in `NewOIDCService` (`internal/service/auth_oidc.go`); the adapters
  (`NewIDKitIdentityStore`, `NewIDKitSessionIssuer` and the rest) live in
  `internal/service/auth_oidc_host.go`.

**Frontend** (`gocrm-ui/`): React 19 + TypeScript + MUI + Vite. Axios client in `src/api/client.ts`
(base URL from `VITE_API_BASE_URL`, default `http://localhost:8080/api/v1`), per-entity endpoint
modules in `src/api/endpoints/`, TanStack Query for data fetching, react-hook-form + zod for forms,
pages organized per entity under `src/pages/`.

### Transactions and row locks

Choose the primitive by what the write needs; the full contract is under **Transaction management**
in [DEV_TRANSACTIONS.md](DEV_TRANSACTIONS.md) and the lock order of every flow is in [CONCURRENCY.md](CONCURRENCY.md).

| Need | Use | Behaviour |
|------|-----|-----------|
| A service write that touches several rows, emits webhook or automation events, or both (the default) | `internal/service/txunit.go` `txDeps.Run` | Opens one transaction on the context (a savepoint when the context already carries one) and hands the callback a `txUnit`: the transaction, the repositories bound to it, the webhook `TxEmitter` and the automation repository, so everything commits or rolls back together. A service builds it with `newTxDeps(tm)`, which refuses a nil manager. |
| A write that can lose a lock race and should be replayed (deadlock, lock wait timeout, SQLite busy) | `internal/utils/transaction.go` `TransactionManager.WithTransactionAndRetry` | Each attempt is a fresh root transaction; only an attempt that rolled back cleanly is replayed, never an ambiguous commit. Inside an active transaction it runs once under a savepoint, and a retryable failure comes back tagged `ErrNestedRetryDeferred`, so the enclosing root decides whether to replay. |
| Read rows inside the transaction and keep them until commit, because the write depends on them | `internal/repository/locking.go` `LockingRead` | `SELECT … FOR UPDATE` on MySQL/MariaDB; the SQLite dialector drops the clause. The fresh `Session` keeps the clause off later statements, so bind only reads to the handle. |
| Read rows that must not change before commit, while other readers may read them too | `internal/repository/locking.go` `SharedLockingRead` | `FOR SHARE` on MySQL 8, `LOCK IN SHARE MODE` on MariaDB; waits for a writer, not for other shared readers. |
| Lock one entity by id through its repository | the per-repository `*LockingReader` interfaces, e.g. `internal/repository/interfaces.go` `TicketLockingReader` (also `TaskLockingReader`, `DealLockingReader`, `CompanyLockingReader`) | `GetByIDForUpdate(id)` on a repository bound to the transaction with `WithTx`; services take the interface as an option so the unlocked path stays available to callers without a transaction. |

### Background runtimes and outbound requests

A background runtime is built from `internal/lifecycle`: `internal/lifecycle/loop.go` `NewLoop`
runs a body at start and then on every tick or wake on one goroutine until `Stop`, and
`internal/lifecycle/owner.go` `NewOwner` is the admission count and context of owned work that a
shutdown closes and drains. The automation worker, the form retention purge, the bulk operation
sweep, the webhook sender and the AEO runtime use them. A new runtime adds its `Stop` to the drain
handle `setupDependencies` returns (`combineStops`), so the shutdown stops it under the same
deadline. An outbound HTTP request to a URL an administrator or a user supplied goes through the
egress guard: `internal/egress/public.go` `Guard.PublicHTTPClient` returns a client that reaches
only that origin through public addresses and follows no redirect, and
`internal/egress/public.go` `CheckPublicURL` validates the URL before it is stored. The AEO
provider clients share the same dial-time checks under their own engine policy
(`internal/aeo/egress.go`).

The authentication flows are mapped to their code in
[DEV_AUTH.md](DEV_AUTH.md#authentication-flows), and the last-admin guard is described in
[DEV_RBAC.md](DEV_RBAC.md#last-admin-guard).

## Package map

| Package | Role | Long form |
|---------|------|-----------|
| `internal/handler` | Gin HTTP handlers and the route table: parse the request, call a service, answer through `utils.Respond*` | [DEV_ENTITIES.md](DEV_ENTITIES.md), [DEV_RBAC.md](DEV_RBAC.md) |
| `internal/service` | Business logic, the transaction units and the authentication flows | [DEV_TRANSACTIONS.md](DEV_TRANSACTIONS.md), [DEV_AUTH.md](DEV_AUTH.md) |
| `internal/repository` | GORM data access, row locks and the erasure of personal data | [DEV_ERASURE.md](DEV_ERASURE.md), [DEV_ENTITIES.md](DEV_ENTITIES.md) |
| `internal/models` | Domain entities and the SQLite auto-migration | [DEV_ENTITIES.md](DEV_ENTITIES.md), [datamodel.md](datamodel.md) |
| `internal/permissions` | The permission catalogue and the permission keys of every protected route | [DEV_RBAC.md](DEV_RBAC.md), [AUTHORIZATION.md](AUTHORIZATION.md) |
| `internal/idkit` | The identity layer: OpenID Connect, single sign-on and SCIM, independent of the CRM ([README](../internal/idkit/README.md)) | [DEV_AUTH.md](DEV_AUTH.md) |
| `internal/webhook` | Outbound webhook events, signing and delivery | [DEV_RUNTIMES.md](DEV_RUNTIMES.md#outbound-webhooks-345) |
| `internal/egress` | The guard for outbound requests to URLs an administrator or a user supplied | [DEV_RUNTIMES.md](DEV_RUNTIMES.md#outbound-webhooks-345) |
| `internal/lifecycle` | The loop and the owner every background runtime is built from | [DEV_RUNTIMES.md](DEV_RUNTIMES.md) |
| `internal/testsupport` | Test-only helpers that open the SQLite and MySQL test databases | [DEV_TESTING_CI.md](DEV_TESTING_CI.md#testing) |

### Outside `internal/`

The rest of the repository, beside the packages above:

```
cmd/                  # The server (main.go: entry point and setupDependencies wiring; the background runtimes)
├── create-admin/     # CLI that provisions an admin account
└── migrate/          # Runner for the MySQL/MariaDB SQL migrations
test/integration/     # Integration tests (in-memory SQLite)
tests/                # Further integration tests
migrations/           # MySQL/MariaDB SQL migrations, up/down pairs
scripts/              # create_database.sql, anonymize_legacy_deleted_pii.sql, seed and smoke scripts,
                      # ci/ (checks and planners), release/, deploy/, e2e/ (the make e2e driver), git-hooks/
api/                  # Generated Swagger 2.0 spec and static HTML reference (make swagger)
docs/                 # This documentation; index in README.md
gocrm-ui/             # React TypeScript frontend
├── src/
│   ├── api/          # Axios client and per-entity endpoint modules
│   ├── components/   # Reusable UI components
│   ├── contexts/     # React contexts (auth, config, snackbar)
│   ├── hooks/        # Custom React hooks
│   ├── layouts/      # Shell layouts
│   ├── pages/        # Pages per entity
│   ├── routes/       # Route table
│   ├── test/         # Vitest setup and helpers
│   ├── theme/        # Material-UI theme
│   ├── types/        # TypeScript type definitions
│   └── utils/        # Helpers (dates, API errors)
├── e2e/              # Playwright suites, page objects and global setup
└── public/           # Static assets
```

## Tech stack

Backend:

- **Go 1.25+**.
- **Gin 1.10**: HTTP framework.
- **GORM 1.30**: ORM. **MySQL 8.0+** is the default database; **SQLite** is a supported alternative
  through `DB_DRIVER` (pure-Go driver, no cgo) and is what the test suite runs on.
- **JWT** (`golang-jwt/jwt/v5`): access tokens.
- **Logrus**: structured logging.
- **Testify**: unit and integration test suites.

Frontend:

- **React 19** with **TypeScript 5.8**.
- **Material-UI (MUI) v7**: component library.
- **React Router 7**: client-side routing.
- **TanStack Query 5**: data fetching and caching.
- **React Hook Form + Zod**: forms and validation.
- **Axios**: HTTP client.
- **Recharts**: dashboard charts.
- **Vite 6**: build tool and dev server.
- **Vitest 3 + Playwright**: unit and end-to-end tests.

## Topic pages

| Page | Content |
|------|---------|
| [DEV_TRANSACTIONS.md](DEV_TRANSACTIONS.md) | The transaction manager contract, service transaction units and the request context |
| [DEV_AUTH.md](DEV_AUTH.md) | Sign-in, sessions and refresh tokens, API keys, credential revocation, single sign-on, SCIM and the web client session |
| [DEV_RBAC.md](DEV_RBAC.md) | Role assignment, custom roles, admin-only permissions, the role API and the last-admin guard |
| [DEV_ENTITIES.md](DEV_ENTITIES.md) | Quotes, mail delivery, ordered reads, tasks and tickets, list filters, lead owners, the lead and company lock order and bulk endpoints |
| [DEV_ERASURE.md](DEV_ERASURE.md) | Deleting personal data: the erasure cascade, what is scrubbed and kept, operator procedures and limits |
| [DEV_RUNTIMES.md](DEV_RUNTIMES.md) | The AEO and automation runtimes, outbound webhooks, form retention, form mail, attribution and ticket intake |
| [DEV_TESTING_CI.md](DEV_TESTING_CI.md) | Test layout, CI jobs and gates, documentation checks, characterisation gates and the API docs gate |

## Conventions

**Unified API response** — all endpoints return `utils.APIResponse{Success, Data, Error, Meta}`. Use
`utils.RespondSuccess()`, `utils.RespondError()`, `utils.RespondNotFound()`, etc.

**Error types** — sentinel errors live in `internal/errors/errors.go` (for example
`ErrDuplicateEmail`, `ErrNotFound`, `ErrLeadConverted`). Handlers classify errors with `errors.Is()`
— never string comparison. The error handler middleware maps error types to HTTP status codes.

**Account security** — account lockout (the rule is stated once, under **Authentication** in [DEV_AUTH.md](DEV_AUTH.md))
and password complexity validation (min 10 chars, upper + lower + digit + special).

**Sort validation** — all repository sort queries validate `sortBy` against per-entity column
allowlists via `utils.ValidateSort()` to prevent SQL injection.

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler, then on the
`protected` group RateLimit (moderate) → Auth: since #466 a request is charged before its
credential is checked.

**Pagination** — every list endpoint parses `offset`/`limit` through `utils.ParseOffsetLimit`
(`internal/utils/response.go`). It defaults to `offset=0, limit=20`, caps `limit` at 100, ignores
non-positive and unparseable values, and **never returns `limit=0`** — callers divide by it when
building pagination metadata, and `?limit=0` used to panic that arithmetic and turn every list
endpoint into a 500. Do not hand-roll query parsing in a handler; call the helper.

**Duplicate email** — repositories translate driver-level unique-constraint violations into the
sentinel `apperrors.ErrDuplicateEmail` via `isDuplicateKeyError` in
`internal/repository/duplicate_key.go`. Handlers classify it with `errors.Is` and return 409 for both
users and customers. The helper is only safe on tables whose sole unique index is `email`, which is
true of `users` and `customers`; adding a second unique index to either means the helper can no
longer attribute a hit to the email column.

**Rate limiting** — all three tiers are wired, in `cmd/main.go`, `SetupFormPublicRoutes` and
`SetupQuotePublicRoutes`, and the SCIM provider has its own two limits:

| Tier | Limit | Where it is applied |
|------|-------|---------------------|
| `RateLimitStrict()` | 10 req/min, burst 5 | the **public** `/auth` group — `GET /auth/registration`, `POST /auth/register`, `/auth/login`, `/auth/refresh`, `/auth/password-reset`, `/auth/password-reset/confirm`, `POST /auth/oidc/start`, `POST /auth/oidc/callback` and `GET /auth/oidc/frontchannel-logout` (the `authRoutes` group in `setupDependencies`) — plus the public form `submit` and `confirm` routes via `SetupFormPublicRoutes`, and the quote client response routes `GET` and `POST /quotes/public/response` via `SetupQuotePublicRoutes` (one shared instance) |
| `RateLimitModerate()` | 120 req/min, burst 30 | every authenticated route, reads and writes alike — applied once to the `protected` group in `setupDependencies`, which is where `/auth/logout` and `/auth/change-password` live too (the `protectedAuth` subgroup). Charged before authentication (#466), so requests with refused or missing credentials count too, and a 429 can answer before a 401 |
| `RateLimitGenerous()` | 240 req/min, burst 40 | the three public form GETs — definition, `embed.js` and the hosted view — via `SetupFormPublicRoutes`, and `POST /quotes/public/render` (the PDF render read, its own instance) via `SetupQuotePublicRoutes` |
| SCIM limits | per token: `SCIM_RATE_LIMIT_PER_MINUTE` / `SCIM_RATE_LIMIT_BURST` (1500/150); refusals per client address: 10/min, burst 5 | `/scim/v2`, inside `middleware.SCIMAuth`; built by `internal/middleware/rate_limit.go` `NewSCIMRateLimits`, described under **SCIM provider tokens and discovery (#468)** in [DEV_AUTH.md](DEV_AUTH.md) |

All three tiers are live, but the split is public routes versus authenticated traffic, **not** reads
versus writes. On authenticated traffic there is no separate tier for reads: the generous tier is
reachable only through unauthenticated routes (the three `/forms/public` GETs and the
`POST /quotes/public/render` read), so any doc or comment implying that an
authenticated read gets a more generous budget is wrong. The inline comment beside the moderate tier
in `cmd/main.go` now matches the code — it read "60 req/min" until 2026-09-18, when it was corrected
to 120 req/min, burst 30. OPTIONS preflight requests are excluded. Limiting is keyed on
`c.ClientIP()`, which is why `TRUSTED_PROXIES` must be set correctly — otherwise a spoofed
`X-Forwarded-For` defeats the limiter. With no trusted proxies every client behind one proxy
shares the proxy's bucket; with a trusted proxy the rightmost untrusted forwarded address,
IPv4 or IPv6, is the identity. The bucket key is that address for an IPv4 client (an
IPv4-mapped IPv6 address counts as its IPv4 address) and its /64 network for an IPv6 client
(`rateLimitClientKey`), so the addresses of one IPv6 /64 share one bucket.

Visitor state is bounded and owns no goroutine:

- **Per-instance capacity.** Every `RateLimit(...)` call — and therefore every tier and every
  route group that constructs one — has its own token buckets and tracks at most 10,000
  clients. State is held in process memory, so limits are neither shared between replicas nor kept
  across restarts.
- **Eviction at capacity.** When an instance is full and no record can be reclaimed, a new
  client is still admitted: the record with the earliest reclaim time, the least recently active
  client, is dropped to make room (one per admission, without a log line), so the count never
  exceeds 10,000. A more recently active client keeps its bucket and its remaining tokens, and a
  throttled one stays throttled. The dropped client's bucket may not have refilled, so it starts
  again with a fresh burst on its next request. The reclaim time is the later of last-seen plus
  five minutes and the refill-to-burst time; every live tier refills within 30 s, so the order is
  the last-seen order.
- **Idle expiry.** A record is reclaimed only after five minutes without a request (throttled
  requests count as activity) *and* once its bucket has refilled to the full burst. Dropping it
  is then indistinguishable from keeping it, so expiry never grants tokens early, even for a
  custom low-rate limiter whose bucket takes hours to refill.
- **Cleanup lifecycle.** Expiry is opportunistic: each request reclaims at most four eligible
  records, chosen from a heap ordered by their earliest safe expiry time, so neither ordinary nor
  rejected requests scan the table. There is no ticker or background goroutine, so constructing
  and discarding routers (as tests do) leaks nothing and needs no shutdown.

The previously exported config-driven `RateLimiter` (with `RateLimitType`, `PublicRateLimit` and
`SmartRateLimit`) was never wired to a route and has been removed, together with its tests; the
relevant assertions (per-IP keys, spoofed-header resistance, 429 envelope) now exercise the
production `RateLimit`. The unused `config.RateLimitConfig` struct remains and has never
configured the live tiers. There are no runtime knobs for capacity or idle expiry.

`internal/middleware/rate_limit_test.go` covers admission and eviction at capacity
(`TestRateLimitCapacity`), clock-driven refill and safe expiry (`TestRateLimitExpiry`), goroutine-free
construction and disposal (`TestRateLimitLifecycle`), concurrent arrivals and cleanup under `-race`
(`TestRateLimitConcurrency`), the 429/abort/OPTIONS contract (`TestRateLimitHTTPBehavior`),
untrusted and trusted proxy identities including IPv6 (`TestRateLimitTrustedProxy`) and per-instance
state (`TestRateLimitTierIsolation`) and the client keys with the IPv6 /64 grouping
(`TestRateLimitClientKey`). Expiry tests use an injected clock rather than sleeping.
`internal/middleware/rate_limit_live_test.go` adds `TestProductionRefusesRateLimitSwitch`, the
startup refusal of the switches below in production mode.

`DISABLE_RATE_LIMIT=true` bypasses the strict tier and the two SCIM limits. The check lives inside
`middleware.RateLimitStrict`, which returns a pass-through handler when the variable is set, and
inside `NewSCIMRateLimits`, which then returns nil. The generous tier stays active, and so does the
moderate tier on authenticated traffic — including on `/auth/logout` and
`/auth/change-password`, which are authenticated and therefore never saw the strict tier to begin
with. That remains the behavior of ordinary builds outside production mode. With
`DISABLE_RATE_LIMIT=true` and `SERVER_MODE=production` the server refuses to start, whether the
switch comes from the environment or from `.env` (`middleware.CheckRateLimitSwitch`, called in
`main` before the database is opened).

`make e2e` builds a separate disposable binary with the `gophercrm_e2e` build tag and sets
`GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true`. Together these opt functional browser tests out of all
three live tiers, avoiding cross-test token-bucket state and dashboard 429s. Ordinary binaries
ignore this switch, even if it is set. The tagged binary also keeps every tier enabled unless the
switch is exactly `true`, and refuses to start with it in production mode. Do not deploy the tagged test binary. `make verify-backend` tests live-tier
burst enforcement, the `DISABLE_RATE_LIMIT` switch (strict tier lifted, moderate and generous kept), ordinary-build isolation, and the tagged opt-out;
no browser assertion or retry setting is relaxed.

## Gotchas

Traps that have already cost time here. Each is stated as the wrong move and the correct one.

**Do not add a composite `UNIQUE(email, deleted_at)` index to allow email reuse after soft delete.**
It looks like the obvious fix and it silently removes the constraint you care about. Both MySQL and
SQLite treat `NULL`s in a unique index as distinct, so every live row (`deleted_at IS NULL`) compares
unequal to every other live row and unlimited duplicate *live* emails become insertable. This was
verified empirically against both engines, not assumed. Email reuse is instead handled by erasure
overwriting the address on delete (see *Deleting personal data* in [DEV_ERASURE.md](DEV_ERASURE.md#deleting-personal-data)), so the original value no
longer exists in the table and can be registered again.

**Do not rely on `gorm.ErrDuplicatedKey`.** GORM only produces it when the connection is opened with
`gorm.Config{TranslateError: true}`, and no `gorm.Open` call in this project sets it — confirm with
`command grep -rn TranslateError`. Detection therefore falls through to driver-specific checks in
`internal/repository/duplicate_key.go`: MySQL error 1062 (`ER_DUP_ENTRY`) and the SQLite message
`UNIQUE constraint failed`. If you enable `TranslateError` later, do it on every connection at once,
or the two paths will disagree between production and tests.

**Do not assume a schema or query behaves the same in tests as in production.** Tests run against
in-memory SQLite through `github.com/glebarez/sqlite` — the same pure-Go driver a `DB_DRIVER=sqlite`
deployment uses, so the driver is no longer a difference. The *dialect* still is: production is
MySQL 8 by default and SQLite only by choice. Anything schema- or driver-specific — index semantics,
error codes, collation, `ON CONFLICT` versus `ON DUPLICATE KEY` — has to work on both, and a green
test suite proves only the SQLite half. Both gotchas above are instances of exactly this split.

**Do not pin a `:memory:` database to one connection when the test cancels a transaction.**
`database/sql` discards the pooled connection of a transaction whose context is cancelled, and
the replacement connection to `:memory:` is a fresh, empty database — the next statement fails
with `no such table`. Tests that exercise shutdown or cancellation (the AEO runtime roots in
`internal/service/aeo_runtime_test.go`) open a file under `t.TempDir()` with the production
pragmas instead; `TestAEORuntimeSQLiteSurvivesADiscardedConnection` pins that behaviour.

## Configuration

Environment-based via a `.env` file (loaded by godotenv). See `.env.example` for the full list. Key
settings:

- `DB_DRIVER` — `mysql` (default) or `sqlite`; anything else fails at startup
- `DB_PATH` — SQLite database file (default `gophercrm.db`), read only when
  `DB_DRIVER=sqlite`; must not contain `?`, because the connector appends its own pragma query string
- `DB_*` — MySQL connection settings, ignored when `DB_DRIVER=sqlite`
- `JWT_SECRET` — required, minimum 32 characters; values starting with `CHANGE_THIS` are refused
- `API_KEY_SECRET` — optional, falls back to `JWT_SECRET`
- `SERVER_PORT` (default 8080), `SERVER_MODE` (`development` / `production`)
- `SERVER_HOST` — interface to bind; empty (default) listens on every interface, `127.0.0.1`
  behind a reverse proxy on the same machine; IPv6 literals are bracketed automatically
- `API_PREFIX` (default `/api/v1`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; empty means trust none
- `LOG_LEVEL`, `LOG_FORMAT` (`json` / `text`)
- `DISABLE_RATE_LIMIT` — lifts the strict tier and the two SCIM limits; for tests only. `true` stops startup when
  `SERVER_MODE` is `production`
- `AUTOMATION_ENABLED` (default true) — starts the follow-up automation worker and runs the
  boot-time stale sweep; the API and the event capture work without it, but no run starts or
  resumes and no stranded run is touched. It may be on for any number of replicas sharing one
  MySQL/MariaDB database (run leases, #237); a SQLite file stays one process
- `AUTOMATION_POLL_INTERVAL_SECONDS` (default 30, clamped to 1..3600) — how often the worker
  polls for events, due runs and schedule passes; also the granularity of every wait
- `FORM_RETENTION_DAYS` (default off) — age in days after which spam and never-confirmed form
  submissions with no linked lead are permanently removed. Unset, empty, 0, negative or unparseable
  keeps it off; 1 and 2 become the 3-day floor; above 36500 becomes 36500. The first boot with it
  set removes the existing backlog (see *Form retention purge*)
- `AUTH_AUDIT_RETENTION_DAYS` (default 90) — days a sign-in audit row (`auth_audit_log`, #454) is
  kept; the stale bulk operation sweep deletes older rows at boot and every 15 minutes. Unset,
  empty, 0, negative or unparseable keeps 90; above 36500 becomes 36500. Environment only

Frontend configuration lives in `gocrm-ui/.env` (see `gocrm-ui/.env.example`); `VITE_API_BASE_URL`
points the Axios client at the backend.

## Database

GORM over MySQL 8.0+ by default, with SQLite selectable through `DB_DRIVER` (pure-Go driver, no
cgo). `internal/database/database.go` `Open` is the single place a connection is opened and owns every
driver-specific decision — dialector, DSN shape, connection pool.

Auto-migration runs on startup via `models.MigrateDatabase()` and is the **only** schema path on
SQLite: the SQL files in `migrations/` are written for MySQL. The global DB handle is `models.DB`.
The data model is documented in [datamodel.md](datamodel.md). Operational notes for the SQLite
flavour — single-connection pool, WAL, backups — are in [SETUP.md](SETUP.md#sqlite) and
[DOCKER.md](DOCKER.md).

On MySQL and MariaDB both paths exist. `models.MigrateDatabaseWith` creates every table with
`ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`, the options the SQL migrations
write, whatever the database's default collation is; a table that already exists keeps its
collation. `cmd/migrate/schema_parity_migration_test.go`
(`TestSchemaParityMigration`) compares them: it builds one fresh schema from every SQL migration and
one with `models.MigrateDatabaseWith`, then compares the tables, each column's type and nullability,
and the indexes (unique or not, index type, ordered columns with prefix lengths; the index name is
ignored) from `INFORMATION_SCHEMA`. Column defaults, collations, column order, foreign keys and index
or constraint names are not compared. A new difference fails the test with one line per difference:
fix the model or the migration, or add it to `schemaParityAllowList` in that file with a reason. An
allow-list entry that no longer matches a difference on the engine under test fails as well. The
test needs `TEST_MYSQL_DSN` and skips without it.

## API Reference

The [README](../README.md#api-documentation) points to the reference; the reference itself is the
list of the REST surface. It is generated and checked in under `api/`:

- `api/swagger.json` and `api/swagger.yaml`: the Swagger 2.0 spec, built by swag v1.16.6 from the
  annotations on the routed handlers, the request/response types in `internal/handler/` and
  `internal/models/` (plus four types from `internal/service/`), and the general block in `cmd/main.go`.
- `api/index.html`: a static, self-contained HTML rendering of the spec
  (`scripts/docs/render_api_reference.py`, standard-library Python). It has no scripts, fonts or
  network references, so opening the file from disk works.

`make swagger` regenerates all three; run it after changing a handler annotation, a documented
type or a route, and commit the result. The application serves none of it (deliberate), and the
router in `internal/handler/` remains the source of truth when the two disagree.

### OpenAPI spec

The annotations document what the code does, including its warts. A defect found while
documenting is recorded under [Defects found while documenting the
API](#defects-found-while-documenting-the-api) below, never put into the spec as intended
behaviour; a product limitation still goes to [ROADMAP.md](ROADMAP.md).

- **General block** (`cmd/main.go`): authentication (`Authorization: Bearer <jwt>` and
  `Authorization: ApiKey <key>`, refresh rotation, access tokens ended by logout or a password
  change or reset, otherwise valid until expiry), the response
  envelope, the shared error codes, pagination, rate limits, CORS and `X-Request-ID`, the two
  security schemes (both the `Authorization` header) and one `@tag.name`/`@tag.description` pair
  per tag. A new tag needs a declaration there.
- **Operations**: summary, a description that names the roles admitted, the visibility narrowing,
  side effects (mail, automation events, erasure) and the rate-limit tier, exactly one tag, a
  described parameter for every input, and a `@Failure` line per status the handler can return
  with its `error.code` values and conditions. List operations document their defaults,
  clamping and allowed `sort_by` columns; bodyless 204s stay schema-free; the public form pages
  declare their real media types.
- **Types**: every field carries a comment (meaning, unit, format, nullability, who sets it),
  plus struct tags where the code enforces something: `enums` for closed sets, `minimum`,
  `maximum` and `maxLength` only for character limits the code checks (byte limits stay in
  prose), `example`, `format`, `x-nullable`, `readonly`. Use `swaggertype` where the Go type
  serialises differently (for example `gorm.DeletedAt` as a nullable timestamp). A value whose
  JSON type varies stays untyped: `interface{}` fields need no tag, and `json.RawMessage` is
  replaced by `interface{}` through `scripts/docs/swag-overrides` (passed by `make swagger`).
  A field the service, not the binder, requires (or an array whose length it bounds) gets a
  `validate:"required"` / `validate:"min=1,max=20"` tag: swag turns it into `required` /
  `minItems`/`maxItems`, while gin validates only `binding` tags, so it changes nothing at
  runtime (say so in the type's comment). Where the JSON
  differs from the struct (a `MarshalJSON`, a `gin.H` payload, a list wrapper) a
  documentation-only type describes it (`models.QuoteJSON`, `handler.LeadListData`, ...); such a
  type is never bound or encoded. No JSON tag, binding or validation changes for documentation's
  sake.
- **Known swag behaviours**: binding failures report `error.details` keyed by Go field name, and
  swag keeps comment line breaks in descriptions (the HTML renderer joins them). An escaped quote
  (`\"`) inside a `@Failure` description reaches the spec verbatim; plain double quotes work.

### Defects found while documenting the API

Recorded while writing the API reference (2026-10 onwards); the spec documents the current
behaviour, warts included. Found by reading the code path unless noted.

- **`PUT /users/{id}` accepts an admin's own deactivation or demotion while another active admin
  remains**, which `POST /users/{id}/deactivate` refuses with 400 for every caller's own account.
  The last active admin is protected on both routes since #348 (409 `LAST_ADMIN`).
- **Database failures reported as 404** — these lookups report every lookup error, a database
  failure included, as a missing record or reference:
  - `GET`, `PUT` and `DELETE /leads/{id}` and the pre-read of `POST /leads/{id}/convert` answer
    404 `Lead not found` (the convert pre-read answers 500 only when the request was cancelled);
  - the assignee and customer lookups of `POST /tasks` and `PUT /tasks/{id}` answer 404;
  - the customer lookup of `POST /tickets` answers 404, and the assignee lookups of
    `POST /tickets` and `PUT /tickets/{id}` answer 400.
- **A failed quote email answers 502 `MAIL_DELIVERY_FAILED`** — behind Cloudflare the JSON body is
  replaced; errors that must reach the client use 503 elsewhere.
- **Validation details are keyed by Go field name** (`FirstName`) for binding failures but by JSON
  name for service-level field errors; Gin's validator has no tag-name function registered.
- **Zero-valued nested objects** — `Lead.owner`, `Ticket.customer` and `Task.assigned_to` are
  non-pointer structs tagged `omitempty`, which Go ignores, so responses that do not load them
  carry an object with id 0 instead of omitting the field.
