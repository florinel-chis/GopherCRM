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

**Error types** — domain-specific errors in `internal/errors/` (auth, business, repository, validation, configuration). The error handler middleware maps these to HTTP status codes.

**Transaction management** — use `utils.NewTransactionManager(models.DB).WithTransaction()` for multi-step operations. Repositories have `WithTx()` to participate in transactions.

**Authentication** — JWT Bearer tokens and API Key header (`ApiKey`). Middleware sets user context. Use `middleware.RequireRole()` for RBAC on routes.

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler → Auth → RateLimit.

**Rate limiting** — three tiers: Strict (5/min for auth), Moderate (60/min for writes), Generous (120/min for reads).

## Testing

- **Unit tests**: Colocated `*_test.go` files using testify suites and mocks from `internal/mocks/`
- **Integration tests**: `test/integration/` — use SQLite in-memory DB via `base_test.go` setup
- Tests use `testify/suite` and `testify/mock`

## Configuration

Environment-based via `.env` file (loaded by godotenv). See `.env.example` for all variables. Key settings:
- `DB_*` for MySQL connection
- `JWT_SECRET` (required, min 32 chars)
- `SERVER_PORT` (default 8080), `SERVER_MODE` (development/production)
- `LOG_LEVEL`, `LOG_FORMAT` (json/text)

## Database

MySQL 8.0+ with GORM. Migrations in `migrations/`. Auto-migration runs on startup via `models.MigrateDatabase()`. Global DB handle at `models.DB`.
