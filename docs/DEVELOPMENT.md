# GopherCRM Development Guide

Reference for developers working in this repository: commands, layering, and the conventions the
codebase enforces. For first-time setup instructions see [doc/SETUP.md](../doc/SETUP.md); for the
feature and test-coverage matrix see [doc/FEATURES.md](../doc/FEATURES.md).

## Project Overview

GopherCRM is a CRM system built in Go with a React TypeScript frontend (`gocrm-ui/`). It manages
Users, Leads, Customers, Tickets, Tasks, and API Keys with role-based access control (admin, sales,
support, customer).

## Development Commands

### Backend

```bash
make build          # Build binary to bin/gophercrm
make run            # Run the app (go run cmd/main.go)
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
```

### Frontend (run from `gocrm-ui/`)

```bash
npm run dev            # Vite dev server (expects the backend on :8080)
npm run build          # tsc -b && vite build
npm run lint           # ESLint (lint:fix to auto-fix)
npm run format         # Prettier over src/
npm test               # Vitest unit tests
npm run test:coverage  # Vitest with coverage
npm run test:e2e       # Playwright E2E tests
npm run test:e2e:admin # Admin CRUD suites (uses playwright.config.slow.ts)
npm run test:e2e:report# Open the last Playwright HTML report

# Run a single E2E spec
npx playwright test e2e/tests/login.spec.ts
```

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
- **All API routes** are mounted under `/api/v1` (configurable via `API_PREFIX`)

**Frontend** (`gocrm-ui/`): React 19 + TypeScript + MUI + Vite. Axios client in `src/api/client.ts`
(base URL from `VITE_API_BASE_URL`, default `http://localhost:8080/api/v1`), per-entity endpoint
modules in `src/api/endpoints/`, TanStack Query for data fetching, react-hook-form + zod for forms,
pages organized per entity under `src/pages/`.

## Key Patterns

**Unified API response** — all endpoints return `utils.APIResponse{Success, Data, Error, Meta}`. Use
`utils.RespondSuccess()`, `utils.RespondError()`, `utils.RespondNotFound()`, etc.

**Error types** — sentinel errors live in `internal/errors/errors.go` (for example
`ErrDuplicateEmail`, `ErrNotFound`, `ErrLeadConverted`). Handlers classify errors with `errors.Is()`
— never string comparison. The error handler middleware maps error types to HTTP status codes.

**Transaction management** — use `utils.NewTransactionManager(models.DB).WithTransaction()` for
multi-step operations. Repositories expose `WithTx()` to participate in transactions.

**Authentication** — JWT Bearer tokens and API Key header (`ApiKey`). API keys use HMAC-SHA256
hashing (with a legacy SHA256 fallback for migration). Middleware sets the user context. Use
`middleware.RequireRole()` for RBAC on routes. An API key whose owner has been erased or deactivated
is rejected at authentication time.

**CSRF is not shipped** — `internal/middleware/csrf.go` implements a `CSRF` middleware and a
`CSRFToken` endpoint, and the HMAC-SHA256 token codec in `internal/service/auth_service.go` is unit
tested, but `middleware.CSRF` is never installed in `cmd/main.go` and no route requires a token. It
is dead code today: treat it as an unfinished feature, not a protection you can rely on. There is no
live CSRF exposure in the current design either — authentication is Bearer/API-key only and the
frontend stores its token in `localStorage`/`sessionStorage` and sends it in the `Authorization`
header, so no credential is attached ambiently by the browser. That changes the moment any
cookie-borne session is introduced, and the middleware would then have to be wired up.

**Role assignment** — `POST /auth/register` is public and always creates a `customer`; it ignores
any client-supplied role. Elevated roles are only assignable via the admin-guarded `POST /users` or
the `create-admin` CLI. Never add a client-controlled role to a public endpoint. E2E admin accounts
are seeded by `gocrm-ui/e2e/global-setup.ts`, which shells out to `cmd/create-admin`.

**Account security** — account lockout after 5 failed login attempts (15 min), password complexity
validation (min 10 chars, upper + lower + digit + special), and a cookie `Secure` flag that defaults
to true in production.

**Sort validation** — all repository sort queries validate `sortBy` against per-entity column
allowlists via `utils.ValidateSort()` to prevent SQL injection.

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler → Auth →
RateLimit.

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

**Rate limiting** — two tiers are actually wired in `cmd/main.go`:

| Tier | Limit | Where it is applied |
|------|-------|---------------------|
| `RateLimitStrict()` | 10 req/min, burst 5 | the `/auth` group — `register` and `login` only (`cmd/main.go:154`) |
| `RateLimitModerate()` | 120 req/min, burst 30 | every authenticated route, reads and writes alike (`cmd/main.go:164`) |

There is no separate tier for reads. `RateLimitGenerous()` (240 req/min, burst 40) is defined at
`internal/middleware/rate_limit.go:142` but is **never applied to any route** — it is unused code,
and any doc or comment implying reads get a more generous budget is wrong. The inline comment beside
the moderate tier in `cmd/main.go` still says "60 req/min"; the real value is 120. OPTIONS preflight
requests are excluded. Limiting is keyed on `c.ClientIP()`, which is why `TRUSTED_PROXIES` must be
set correctly — otherwise a spoofed `X-Forwarded-For` defeats the limiter.

`DISABLE_RATE_LIMIT=true` bypasses **only the Strict tier**. The check lives inside `RateLimitStrict`
(`internal/middleware/rate_limit.go:125`), so the moderate tier on authenticated traffic stays active
regardless. That is deliberate and is enough to keep the E2E suite from tripping the login limiter.

## Testing

| Layer | Location | Runner |
|-------|----------|--------|
| Backend unit | colocated `*_test.go` | `go test ./...` — testify suites, mocks from `internal/mocks/` |
| Backend integration | `test/integration/`, `tests/` | `go test ./...` — in-memory SQLite |
| Frontend unit | `gocrm-ui/src/**/*.test.tsx` | `npm test` — Vitest + React Testing Library |
| E2E | `gocrm-ui/e2e/tests/` | `npm run test:e2e` — Playwright, Chromium |

```bash
go test ./...                       # everything
go test -race ./...                 # race detector — must stay clean
go test -cover ./...                # per-package statement coverage
go test -run TestFunctionName ./internal/service/
```

Current state: 9 Go packages pass, including under `-race` with zero races detected; backend
statement coverage is 46.9%. The frontend has 16 test files / 142 tests and `tsc` is clean. ESLint
currently reports around 40 errors, all pre-existing and mostly unused Playwright fixture arguments —
they are unrelated to recent work, so do not treat a clean lint run as a gate until they are cleared.

Backend coverage spans handlers, services, middleware (auth, rate limit, error handler, recovery,
request ID), utils (sort, password, response, crypto, context, transaction), config, and models.

The E2E suite is 100 tests across 9 spec files and needs a real backend and database. Admin accounts
cannot be self-registered — `gocrm-ui/e2e/global-setup.ts` seeds them by shelling out to
`cmd/create-admin`. See `gocrm-ui/e2e/README.md` and [doc/ADMIN_TESTING.md](../doc/ADMIN_TESTING.md).

## Gotchas

Traps that have already cost time here. Each is stated as the wrong move and the correct one.

**Do not add a composite `UNIQUE(email, deleted_at)` index to allow email reuse after soft delete.**
It looks like the obvious fix and it silently removes the constraint you care about. Both MySQL and
SQLite treat `NULL`s in a unique index as distinct, so every live row (`deleted_at IS NULL`) compares
unequal to every other live row and unlimited duplicate *live* emails become insertable. This was
verified empirically against both engines, not assumed. Email reuse is instead handled by erasure
overwriting the address on delete (see *Deleting personal data* below), so the original value no
longer exists in the table and can be registered again.

**Do not rely on `gorm.ErrDuplicatedKey`.** GORM only produces it when the connection is opened with
`gorm.Config{TranslateError: true}`, and no `gorm.Open` call in this project sets it — confirm with
`command grep -rn TranslateError`. Detection therefore falls through to driver-specific checks in
`internal/repository/duplicate_key.go`: MySQL error 1062 (`ER_DUP_ENTRY`) and the SQLite message
`UNIQUE constraint failed`. If you enable `TranslateError` later, do it on every connection at once,
or the two paths will disagree between production and tests.

**Do not assume a schema or query behaves the same in tests as in production.** Tests run against
in-memory SQLite; production is MySQL 8. Anything schema- or driver-specific — index semantics, error
codes, collation, `ON CONFLICT` versus `ON DUPLICATE KEY` — has to work on both, and a green test
suite proves only the SQLite half. Both gotchas above are instances of exactly this split.

## Configuration

Environment-based via a `.env` file (loaded by godotenv). See `.env.example` for the full list. Key
settings:

- `DB_*` — MySQL connection
- `JWT_SECRET` — required, minimum 32 characters
- `API_KEY_SECRET` — optional, falls back to `JWT_SECRET`
- `SERVER_PORT` (default 8080), `SERVER_MODE` (`development` / `production`)
- `API_PREFIX` (default `/api/v1`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; empty means trust none
- `LOG_LEVEL`, `LOG_FORMAT` (`json` / `text`)
- `DISABLE_RATE_LIMIT` — test-only escape hatch, never enable in production

Frontend configuration lives in `gocrm-ui/.env` (see `gocrm-ui/.env.example`); `VITE_API_BASE_URL`
points the Axios client at the backend.

## Database

MySQL 8.0+ with GORM. Migrations live in `migrations/`. Auto-migration runs on startup via
`models.MigrateDatabase()`. The global DB handle is `models.DB`. The data model is documented in
[doc/datamodel.md](../doc/datamodel.md).

## Deleting personal data

Deleting a user, customer or lead performs a **permanent erasure**, not a recoverable soft delete.
This implements the right to erasure under GDPR Article 17, and the storage-limitation principle of
Article 5(1)(e): flagging a row as deleted while retaining the person's name, email and phone number
is retention, not erasure. It is implemented in `internal/repository/erasure.go` and
`internal/repository/erasure_cascade.go`.

What happens on `DELETE`:

- Every personal field is overwritten in place. The email is replaced with a unique, non-routable
  placeholder in the reserved `.invalid` domain (RFC 2606) that is generated from `crypto/rand` and
  is **not** derived from the original address, so it cannot be reversed or linked back.
- The user's API keys and refresh tokens are purged, so credentials cannot outlive the account. An
  API key whose owner has been erased or deactivated is rejected at authentication time regardless.
- Only then is the row soft-deleted. The row is deliberately **kept** so that foreign keys from
  tickets, tasks and leads still resolve — business records survive, the person does not.
- Scrub and soft-delete happen in one transaction, so a failure part-way cannot leave a half-erased
  but still-live record. In a bulk delete each item is isolated by its own savepoint, so one failing
  item neither corrupts itself nor rolls back the rest of the batch.
- A lead that was converted into a customer is cascaded in both directions via `leads.customer_id`,
  because conversion copies the person's details into the customer row.

Two consequences worth knowing:

- **It is irreversible.** To suspend access reversibly, set `is_active = false` instead; deactivation
  never touches personal data.
- **The email address becomes reusable.** Because the original address no longer exists anywhere in
  the table, the person can register again with it.

Rows that were soft-deleted *before* this behaviour existed still hold personal data. They are not
migrated automatically — run `scripts/anonymize_legacy_deleted_pii.sql` by hand once you have taken a
backup. It is irreversible.

Not covered by the code, and worth an operational decision: application logs record the email address
on login and on customer create/update, and issued JWTs embed it until they expire. Erasing the
database does not reach either, so log retention needs its own policy.

## API Reference

The REST surface is enumerated in the [README](../README.md#api-documentation). A generated
OpenAPI/Swagger spec is checked in under `docs/swagger.json` and `docs/swagger.yaml`; it is not
currently served by the application, so treat the router in `internal/handler/` as the source of
truth when the two disagree.
