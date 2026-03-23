# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

GopherCRM is a CRM system built in Go with a React TypeScript frontend (`gocrm-ui/`). It manages Users, Leads, Customers, Tickets, Tasks, and API Keys with role-based access control (admin, sales, support, customer).

## Development Commands

```bash
make build          # Build binary to bin/gophercrm
make run            # Run the app (go run cmd/main.go)
make test           # Run all tests (go test ./...)
make create-db      # Create MySQL database from scripts/create_database.sql
make deps           # Download and tidy Go modules
make clean          # Remove bin/
make create-admin   # Run admin creation tool (must build-tools first)
make build-tools    # Build CLI tools to bin/

# Run a single test
go test -run TestFunctionName ./internal/service/

# Run tests for a specific package
go test ./internal/handler/
```

## Architecture

Clean architecture with four layers, all behind interfaces for testability:

```
Handler (Gin HTTP) → Service (business logic) → Repository (GORM data access) → Models (domain entities)
```

- **Dependency injection** is manual, wired in `cmd/main.go` via `setupDependencies()`
- **All layers use interfaces** defined in `internal/repository/interfaces.go` and `internal/service/interfaces.go`
- **Repositories** accept `*gorm.DB` and implement `WithTx()` for transaction support
- **Handlers** parse requests, call services, and return unified responses via `utils.RespondSuccess`/`RespondError`

## Key Patterns

**Unified API response** — all endpoints return `utils.APIResponse{Success, Data, Error, Meta}`. Use `utils.RespondSuccess()`, `utils.RespondError()`, `utils.RespondNotFound()`, etc.

**Error types** — sentinel errors in `internal/errors/errors.go` (e.g., `ErrDuplicateEmail`, `ErrNotFound`, `ErrLeadConverted`). Handlers use `errors.Is()` to classify errors — never string comparison. The error handler middleware maps error types to HTTP status codes.

**Transaction management** — use `utils.NewTransactionManager(models.DB).WithTransaction()` for multi-step operations. Repositories have `WithTx()` to participate in transactions.

**Authentication** — JWT Bearer tokens and API Key header (`ApiKey`). API keys use HMAC-SHA256 hashing (with legacy SHA256 fallback for migration). CSRF tokens use HMAC-SHA256 with 24h expiry. Middleware sets user context. Use `middleware.RequireRole()` for RBAC on routes.

**Account security** — account lockout after 5 failed login attempts (15min), password complexity validation (min 10 chars, upper+lower+digit+special), cookie Secure flag defaults to true in production.

**Sort validation** — all repository sort queries validate `sortBy` against per-entity column allowlists via `utils.ValidateSort()` to prevent SQL injection.

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler → Auth → RateLimit.

**Rate limiting** — three tiers: Strict (10/min for auth), Moderate (120/min for writes), Generous (240/min for reads). OPTIONS preflight requests are excluded. Uses `c.ClientIP()` with trusted proxy configuration to prevent IP spoofing.

## Testing

- **Unit tests**: Colocated `*_test.go` files using testify suites and mocks from `internal/mocks/`
- **Integration tests**: `test/integration/` and `tests/` — use SQLite in-memory DB
- **E2E tests**: `gocrm-ui/e2e/` — 69 Playwright tests covering login, registration, and CRUD for all entities. See `gocrm-ui/e2e/README.md`.
- **Frontend unit tests**: `gocrm-ui/src/**/*.test.tsx` — Vitest + React Testing Library (run with `npm test` in `gocrm-ui/`)
- Backend tests use `testify/suite` and `testify/mock`
- Backend coverage includes: handlers, services, middleware (auth, rate limit, error handler, recovery, request ID), utils (sort, password, response, crypto, context, transaction), config, and models

## Configuration

Environment-based via `.env` file (loaded by godotenv). See `.env.example` for all variables. Key settings:
- `DB_*` for MySQL connection
- `JWT_SECRET` (required, min 32 chars)
- `API_KEY_SECRET` (optional, falls back to JWT_SECRET)
- `SERVER_PORT` (default 8080), `SERVER_MODE` (development/production)
- `TRUSTED_PROXIES` (comma-separated CIDRs, empty = trust none)
- `LOG_LEVEL`, `LOG_FORMAT` (json/text)

## Database

MySQL 8.0+ with GORM. Migrations in `migrations/`. Auto-migration runs on startup via `models.MigrateDatabase()`. Global DB handle at `models.DB`.
