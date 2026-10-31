# GopherCRM Setup Guide

This guide sets up the backend and the frontend for local development. For a fork (first run, rebranding,
configuration that matters in production, deployment), read [FORKING.md](FORKING.md), section 2 "First
run", instead of repeating it here. Docker users read [DOCKER.md](DOCKER.md).

## Backend Setup

### 1. Prerequisites

- Go 1.25 or newer (`go.mod` declares `go 1.25.0`).
- Either MySQL 8.0+ / MariaDB 10.11 (the default driver), or nothing at all for SQLite
  (`DB_DRIVER=sqlite`, see step 3).
- Git.

### 2. Database (MySQL only)

```bash
make create-db
# or, when root needs a password:
mysql -u root -p < scripts/create_database.sql
```

`make create-db` runs `mysql -u root` without a password prompt (`Makefile`, target `create-db`).
`scripts/create_database.sql` creates a database named **`gophercrm`**, while the default `DB_NAME` is
**`gocrm`** (`internal/config/config.go:248`, `.env.example`). Either set `DB_NAME=gophercrm` in `.env`, or
create a `gocrm` database yourself.

Skip this step with SQLite: the file is created on first start.

### 3. Environment configuration

```bash
cp .env.example .env
```

Then edit `.env`. The settings that matter for a first run (all read in `internal/config/config.go`):

- `JWT_SECRET`: required, at least 32 characters. Generate one with `openssl rand -base64 32`. The
  placeholder in `.env.example` is long enough to pass the startup check, so the application **starts with
  it**; replace it before anything else (#210).
- `DB_DRIVER`: `mysql` (default) or `sqlite`. Any other value stops startup.
- MySQL: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`.
- SQLite: `DB_PATH` (default `gophercrm.db`; it must not contain `?`).
- `SERVER_PORT` (default 8080) and `SERVER_HOST` (empty = every interface; `127.0.0.1` behind a reverse
  proxy).

`.env.example` is the commented reference for every other variable.

### 4. Install dependencies

```bash
make deps
# or
go mod download
```

### 5. Run the backend

```bash
make run
# or
go run ./cmd
```

`make run` runs `go run ./cmd` (`Makefile`, target `run`). The entry point is the `cmd` package, not
`cmd/main.go` alone: the AEO and automation runtimes live in `cmd/aeo_runtime.go` and
`cmd/automation_runtime.go`, so `go run cmd/main.go` does not compile. `make build` writes the binary to
`bin/gophercrm`.

The schema is created by auto-migration at startup. The API listens on `http://localhost:8080`.

### 6. Create the first admin

Public registration only ever creates `customer` accounts, so the first admin comes from the CLI. Build it
first; `make create-admin` only runs `bin/create-admin`:

```bash
make build-tools    # builds bin/create-admin
make create-admin   # interactive prompts
```

For scripted provisioning: `bin/create-admin -non-interactive -email ... -name ... -password ...`
(`cmd/create-admin/main.go`). The CLI only checks for 8 characters, but the API applies the full rule
(at least 10 characters with upper case, lower case, a digit and a special character,
`internal/utils/password.go`) when the password is changed later, so choose one that meets it.

## Frontend Setup

### 1. Prerequisites

- Node.js 22 (what CI and `gocrm-ui/Dockerfile` use).
- npm.

### 2. Install and configure

```bash
cd gocrm-ui
npm ci
cp .env.example .env   # VITE_API_BASE_URL=http://localhost:8080/api/v1
```

`VITE_*` values are read at build time by Vite. `VITE_API_BASE_URL` must point at the backend's API prefix.

### 3. Run the frontend

```bash
npm run dev
```

The Vite dev server listens on `http://localhost:5173`.

## Running both together

Terminal 1, from the repository root:

```bash
go run ./cmd
```

Terminal 2:

```bash
cd gocrm-ui
npm run dev
```

## Testing the setup

### 1. Backend health

```bash
curl http://localhost:8080/health
```

### 2. Log in through the frontend

Open http://localhost:5173 and log in with the admin you created.

### 3. Call the API directly

```bash
# Log in; the response data carries "token" and "refresh_token"
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"your-password"}'

# Use the access token on protected routes
curl http://localhost:8080/api/v1/users/me \
  -H "Authorization: Bearer YOUR_TOKEN_HERE"

# An API key goes in the same header with the ApiKey scheme
curl http://localhost:8080/api/v1/users/me \
  -H "Authorization: ApiKey YOUR_API_KEY"
```

The two schemes are parsed in `internal/middleware/auth.go`.

## Common issues

### Backend

1. **Startup refuses the configuration.** `JWT_SECRET` is missing or shorter than 32 characters, or
   `DB_DRIVER` has an unknown value. The error names the variable.
2. **Database connection failed.** Check that MySQL is running, the credentials in `.env`, and that
   `DB_NAME` matches the database you created (see step 2).
3. **Port already in use.** Change `SERVER_PORT` in `.env`, or find the process with `lsof -ti:8080`.
4. **CORS errors, or "Invalid email or password" in the browser while curl works.** Browsers send an
   `Origin` header and the backend answers 403 for origins it does not list. `http://localhost:5173`,
   `:3000` and `:8080` are allowed by default (`internal/middleware/cors.go`); add any other origin to
   `CORS_ALLOWED_ORIGINS` (comma-separated, `internal/config/config.go:258`). Do not edit the source.

### Frontend

1. **API connection failed.** Check that the backend is running and that `VITE_API_BASE_URL` in
   `gocrm-ui/.env` points at it. Restart `npm run dev` after changing `.env`.
2. **Login does not work.** Check the browser console and network tab, and that the admin exists. The
   frontend holds no secret: the JWT is signed and checked only by the backend.

## Production

Production configuration (`SERVER_MODE=production`, TLS in front, `TRUSTED_PROXIES`,
`CORS_ALLOWED_ORIGINS`, SMTP, `PUBLIC_BASE_URL` and `APP_BASE_URL`) and the deploy options are covered in
[FORKING.md](FORKING.md) (sections 4 and 5) and [DOCKER.md](DOCKER.md). To serve the frontend yourself,
run `npm run build` and serve `gocrm-ui/dist` with a web server that falls back to `index.html` for
client-side routes.

## API documentation

The API is described in `api/swagger.json` and `api/swagger.yaml` (Swagger 2.0, regenerated with
`make swagger`). The application does not serve a Swagger UI. All routes mount under `API_PREFIX`
(default `/api/v1`).
