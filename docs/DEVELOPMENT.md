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
hashing (with a legacy SHA256 fallback for migration). CSRF tokens use HMAC-SHA256 with a 24h
expiry. Middleware sets the user context. Use `middleware.RequireRole()` for RBAC on routes.

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

**Rate limiting** — three tiers: Strict (10/min for auth), Moderate (120/min for writes), Generous
(240/min for reads). OPTIONS preflight requests are excluded. Uses `c.ClientIP()` with trusted proxy
configuration to prevent IP spoofing. Setting `DISABLE_RATE_LIMIT=true` bypasses **only the Strict
tier** used by the auth endpoints (`internal/middleware/rate_limit.go`); the Moderate and Generous
tiers stay active. That is enough to keep the E2E suite from tripping the login limiter.

## Testing

- **Backend unit tests**: colocated `*_test.go` files using testify suites and mocks from
  `internal/mocks/`
- **Backend integration tests**: `test/integration/` and `tests/` — use an in-memory SQLite database
- **Frontend unit tests**: `gocrm-ui/src/**/*.test.tsx` — Vitest + React Testing Library
  (`npm test` in `gocrm-ui/`)
- **E2E tests**: `gocrm-ui/e2e/` — Playwright suites covering login, registration, and CRUD for all
  entities. See `gocrm-ui/e2e/README.md` and [doc/ADMIN_TESTING.md](../doc/ADMIN_TESTING.md).
- Backend coverage spans handlers, services, middleware (auth, rate limit, error handler, recovery,
  request ID), utils (sort, password, response, crypto, context, transaction), config, and models

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
is retention, not erasure.

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
