# GopherCRM Setup Guide

> **For:** Developers setting up the backend and frontend locally. Part of the [documentation index](README.md).

This guide sets up the backend and the frontend for local development. For a fork (first run, rebranding,
configuration that matters in production, deployment), read [FORKING.md](FORKING.md), section 2 "First
run", instead of repeating it here. Docker users read [DOCKER.md](DOCKER.md).

## Get the code

```bash
git clone https://github.com/florinel-chis/GopherCRM.git
cd GopherCRM
```

Every command below runs from this directory unless it says otherwise.

## Backend Setup

### 1. Prerequisites

- Go 1.25 or newer (`go.mod` declares `go 1.25.0`).
- Either MySQL 8.0+ / MariaDB 10.11 (the default driver), or nothing at all for SQLite
  (`DB_DRIVER=sqlite`, see step 3).
- Git.
- Make, optional: the `make` targets below wrap the plain `go` and `mysql` commands shown next to them.
- Optional, for quote PDF output: a headless Chrome or Chromium and a font, see
  [Quote PDF output](SERVER_PROVISIONING.md#quote-pdf-output). Without it everything else works.

### 2. Database (MySQL only)

```bash
make create-db
# or, when root needs a password:
mysql -u root -p < scripts/create_database.sql
```

`make create-db` runs `mysql -u root` without a password prompt (`Makefile`, target `create-db`).
`scripts/create_database.sql` creates a database named **`gocrm`**, the default `DB_NAME`
(`internal/config/config.go`, `.env.example`). If you choose another name, create that database
yourself and set `DB_NAME` to match.

Skip this step with SQLite: the file is created on first start.

### 3. Environment configuration

```bash
cp .env.example .env
```

Then edit `.env`. The settings that matter for a first run (all read in `internal/config/config.go`):

- `JWT_SECRET`: required, at least 32 characters. Generate one with `openssl rand -base64 32`. The
  application refuses the placeholder in `.env.example` and any value starting with `CHANGE_THIS`, so
  replace it before anything else.
- `DB_DRIVER`: `mysql` (default) or `sqlite`. Any other value stops startup.
- MySQL: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`.
- SQLite: `DB_PATH` (default `gophercrm.db`, relative to the working directory; see
  [SQLite](#sqlite) below).
- `SERVER_PORT` (default 8080) and `SERVER_HOST` (empty = every interface; `127.0.0.1` behind a reverse
  proxy).

- `QUOTE_PDF_CHROME_PATH` (optional, default empty = PDF output off): the browser binary the
  backend runs headless to print a quote to PDF. Setup, including a development run, is in
  [Quote PDF output](SERVER_PROVISIONING.md#quote-pdf-output).

`.env.example` is the commented reference for every other variable.

#### SQLite

On SQLite the server-connection settings (`DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`,
`DB_PASSWORD`) are ignored, and no `make create-db` is needed. SQLite suits development, demos, CI
and small single-instance deployments. Things to know before relying on it:

- **Everything serializes on one connection.** SQLite takes a database-wide
  write lock, so the pool is deliberately capped at a single connection: within
  the backend process reads queue behind writes exactly as writes do, and there
  is no read concurrency to be had. What WAL mode buys here is that a *separate*
  reader — the `VACUUM INTO` snapshot below, or a `sqlite3` shell — is not
  blocked by the backend's writes. Run only one backend process against one
  file.
- **The file must live on a local filesystem.** WAL mode relies on shared-memory
  locking that NFS, SMB and similar network shares do not implement reliably;
  putting the database on one risks corruption.
- **The database is three files.** `gophercrm.db` plus the `-wal` and `-shm`
  siblings. A graceful shutdown checkpoints the WAL back into the main file; a
  process killed outright leaves it in place, which is normal and recovers on the
  next open.
- **Never copy a live database file.** To back it up, either stop the backend and
  copy all three files, or take an online snapshot with
  `sqlite3 gophercrm.db "VACUUM INTO 'backup.db'"`. A bare `cp` of `gophercrm.db`
  on a running system produces a stale or torn copy.
- **Back up before upgrading.** On SQLite the schema advances only through the
  auto-migration that runs at startup — the `migrations/` SQL is MySQL-only — and
  auto-migration is not reversible. Take the offline copy or the `VACUUM INTO`
  snapshot above first. What each release changes in the file, and the free disk
  its first start needs, is in
  [DOCKER.md](DOCKER.md#upgrading-the-sqlite-database).
- **`DB_PATH` must not contain `?`**; the connector appends its own pragma query
  string, so startup rejects a path that already carries one.

Docker users get this prewired in `docker-compose.sqlite.yml`; see
[DOCKER.md](DOCKER.md#sqlite-flavor-no-database-server).

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

For a populated database instead of an empty one, `make sample-data` builds
`sample-data/halvenmark.db` with a fictional business, its admin and its staff logins; start the
backend on it with `DB_DRIVER=sqlite DB_PATH=sample-data/halvenmark.db`. See
[SAMPLE_DATA.md](SAMPLE_DATA.md).

## Frontend Setup

### 1. Prerequisites

- Node.js 22 or later (CI and `gocrm-ui/Dockerfile` use 22).
- npm.
- A modern web browser.

### 2. Install and configure

```bash
cd gocrm-ui
npm ci
cp .env.example .env   # VITE_API_BASE_URL=http://localhost:8080/api/v1
```

`VITE_*` values are read at build time by Vite. `VITE_API_BASE_URL` must point at the backend's API prefix. Single sign-on needs the SPA and the API on the same site (the same registrable domain and scheme; different ports of `localhost` qualify), because the browser must keep and return the sign-in cookie of `POST /auth/oidc/start`.

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

1. **Startup refuses the configuration.** `JWT_SECRET` is missing, shorter than 32 characters or the
   `.env.example` placeholder (a value starting with `CHANGE_THIS`), or `DB_DRIVER` has an unknown
   value. The error names the variable.
2. **Database connection failed.** Check that MySQL is running, the credentials in `.env`, and that
   `DB_NAME` matches the database you created (see step 2).
3. **Port already in use.** Change `SERVER_PORT` in `.env`, or find the process with `lsof -ti:8080`.
4. **CORS errors, or "Invalid email or password" in the browser while curl works.** Browsers send an
   `Origin` header and the backend answers 403 for origins it does not list, unless the `Origin` is
   the request's own scheme and `Host` (same-origin; `github.com/gin-contrib/cors v1.7.5` in `go.mod`).
   `http://localhost:5173`, `:3000` and `:8080` are allowed by default (`internal/middleware/cors.go`);
   add any other origin to `CORS_ALLOWED_ORIGINS` (comma-separated, `ServerConfig.CORSExtraOrigins` (`internal/config/config.go`)).
   Do not edit the source.

### Frontend

1. **API connection failed.** Check that the backend is running and that `VITE_API_BASE_URL` in
   `gocrm-ui/.env` points at it. Restart `npm run dev` after changing `.env`.
2. **Login does not work.** Check the browser console and network tab, and that the admin exists. The
   frontend holds no secret: the JWT is signed and checked only by the backend.

## Production

Production configuration (`SERVER_MODE=production`, TLS in front, `TRUSTED_PROXIES`,
`CORS_ALLOWED_ORIGINS`, SMTP, `PUBLIC_BASE_URL`, `APP_BASE_URL`, `FORM_RETENTION_DAYS`, and single
sign-on and SCIM: the addresses to register, the `/scim/v2` proxy rule and the environment-only
settings) and the deploy options are covered in
[FORKING.md](FORKING.md) (sections 4 and 5) and [DOCKER.md](DOCKER.md) ("Single sign-on and SCIM"). For the release tarball
with systemd and nginx or Apache on a fresh server (MySQL or MariaDB only), follow
[SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) before the first deploy; its
[Reverse proxy and TLS](SERVER_PROVISIONING.md#reverse-proxy-and-tls) section documents both web servers. To serve the frontend yourself,
run `npm run build` and serve `gocrm-ui/dist` with a web server that falls back to `index.html` for
client-side routes.

## API documentation

The API is described in `api/swagger.json` and `api/swagger.yaml` (Swagger 2.0, regenerated with
`make swagger`). The application does not serve a Swagger UI. API routes mount under `API_PREFIX`
(default `/api/v1`); `/health` and the SCIM 2.0 server at `/scim/v2` are outside it.
