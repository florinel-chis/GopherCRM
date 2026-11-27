# GopherCRM Setup Guide

> **For:** Developers setting up the backend and frontend locally. Part of the [documentation index](README.md).

This guide sets up the backend and the frontend for local development, in the order you run the
steps. What each setting means is in [CONFIGURATION.md](CONFIGURATION.md). For a fork (first run,
rebranding, configuration that matters in production, deployment), read [FORKING.md](FORKING.md),
section 2 "First run". Docker users read [DOCKER.md](DOCKER.md).

## Prerequisites

Backend:

- Go 1.25 or newer (`go.mod` declares `go 1.25.0`).
- Either MySQL 8.0+ / MariaDB 10.11 (the default driver), or nothing at all for SQLite
  (`DB_DRIVER=sqlite`, see [SQLite](#sqlite)).
- Git.
- Make, optional: the `make` targets below wrap the plain `go` and `mysql` commands shown next to them.
- Optional, for quote PDF output: a headless Chrome or Chromium and a font, see
  [Quote PDF output](SERVER_PROVISIONING.md#quote-pdf-output). Without it everything else works.

Frontend:

- Node.js 22 or later (CI and `gocrm-ui/Dockerfile` use 22).
- npm.
- A modern web browser.

### Get the code

```bash
git clone https://github.com/florinel-chis/GopherCRM.git
cd GopherCRM
```

Every command below runs from this directory unless it says otherwise.

### Install the dependencies

```bash
make deps
# or
go mod download
```

```bash
cd gocrm-ui
npm ci
```

## Configuration

### Database (MySQL only)

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

### Backend `.env`

```bash
cp .env.example .env
```

Then edit `.env`. A first run needs:

- `JWT_SECRET`, replaced before anything else: generate one with `openssl rand -base64 32`. The
  placeholder in `.env.example` is refused.
- `DB_DRIVER`: `mysql` (default) or `sqlite`.
- MySQL: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`. SQLite: `DB_PATH`, optional.
- `SERVER_PORT` and `SERVER_HOST`, optional.
- `QUOTE_PDF_CHROME_PATH`, optional (default empty = PDF output off): the browser binary the
  backend runs headless to print a quote to PDF. Setup, including a development run, is in
  [Quote PDF output](SERVER_PROVISIONING.md#quote-pdf-output).

What each one means, the values startup refuses and every other variable are in
[CONFIGURATION.md](CONFIGURATION.md#first-run-settings). `.env.example` is the commented reference
for every variable.

#### SQLite

On SQLite the server-connection settings (`DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`,
`DB_PASSWORD`) are ignored, and no `make create-db` is needed. SQLite suits development, demos, CI
and small single-instance deployments. Things to know before relying on it:

- **Everything serializes on one connection.** SQLite takes a database-wide
  write lock, so the pool is deliberately capped at a single connection: within
  the backend process reads queue behind writes exactly as writes do, and there
  is no read concurrency to be had. What WAL mode buys here is that a *separate*
  reader — a backup snapshot, or a `sqlite3` shell — is not
  blocked by the backend's writes. Run only one backend process against one
  file.
- **The file must live on a local filesystem.** WAL mode relies on shared-memory
  locking that NFS, SMB and similar network shares do not implement reliably;
  putting the database on one risks corruption.
- **The database is three files.** `gophercrm.db` plus the `-wal` and `-shm`
  siblings. Never copy a live database file: how to back it up and restore it is in
  [BACKUP_RESTORE.md](BACKUP_RESTORE.md#sqlite).
- **Back up before upgrading.** Auto-migration at startup is the only schema path on
  SQLite and it is not reversible. The order, what each release changes in the file and the free
  disk its first start needs are in [UPGRADING.md](UPGRADING.md#sqlite-release-by-release).
- **`DB_PATH` must not contain `?`**; the connector appends its own pragma query
  string, so startup rejects a path that already carries one.

Docker users get this prewired in `docker-compose.sqlite.yml`; see
[DOCKER.md](DOCKER.md#sqlite-flavor-no-database-server).

### Frontend `gocrm-ui/.env`

```bash
cd gocrm-ui
cp .env.example .env   # VITE_API_BASE_URL=http://localhost:8080/api/v1
```

`VITE_API_BASE_URL` must point at the backend's API prefix; the `VITE_*` values are read at build
time ([CONFIGURATION.md](CONFIGURATION.md#build-time-web-ui-values)).

## Start

Terminal 1, from the repository root:

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

Terminal 2, in gocrm-ui/:

```bash
npm run dev
```

The Vite dev server listens on `http://localhost:5173`.

## Create the first admin

Public registration only ever creates `customer` accounts, so the first admin comes from the CLI. Build it
first; `make create-admin` only runs `bin/create-admin`.

Terminal 3, from the repository root, while the backend runs:

```bash
make build-tools    # builds bin/create-admin
make create-admin   # interactive prompts
```

For scripted provisioning: `bin/create-admin -non-interactive -email ... -name ... -password ...`
(`cmd/create-admin/main.go`). The CLI only checks for 8 characters, but the API applies the full rule
(at least 10 characters with upper case, lower case, a digit and a special character,
`internal/utils/password.go`) when the password is changed later, so choose one that meets it.

For a populated database instead of an empty one, `make sample-data` builds
`sample-data/halvenmark.db` (a commerce agency) and `sample-data/pelicroft.db` (an online
home-goods shop whose forms feed tickets and leads), each a fictional business with its admin and
its staff logins; start the backend on one with `DB_DRIVER=sqlite DB_PATH=sample-data/halvenmark.db`
(or `pelicroft.db`). See [SAMPLE_DATA.md](SAMPLE_DATA.md).

## Sign in

Open http://localhost:5173 and log in with the admin you created.

## Verify

Backend health:

```bash
curl http://localhost:8080/health
```

Call the API directly:

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

1. **Startup refuses the configuration.** `JWT_SECRET` or `DB_DRIVER` holds a value startup
   refuses ([CONFIGURATION.md](CONFIGURATION.md#first-run-settings)). The error names the variable.
2. **Database connection failed.** Check that MySQL is running, the credentials in `.env`, and that
   `DB_NAME` matches the database you created (see [Database](#database-mysql-only)).
3. **Port already in use.** Change `SERVER_PORT` in `.env`, or find the process with `lsof -ti:8080`.
4. **CORS errors, or "Invalid email or password" in the browser while curl works.** Browsers send an
   `Origin` header and the backend answers 403 for origins it does not list. The localhost origins
   of this guide are allowed by default; add any other origin to `CORS_ALLOWED_ORIGINS`
   (`ServerConfig.CORSExtraOrigins` (`internal/config/config.go`), see
   [CONFIGURATION.md](CONFIGURATION.md#cors)). Do not edit the source.

### Frontend

1. **API connection failed.** Check that the backend is running and that `VITE_API_BASE_URL` in
   `gocrm-ui/.env` points at it. Restart `npm run dev` after changing `.env`.
2. **Login does not work.** Check the browser console and network tab, and that the admin exists. The
   frontend holds no secret: the JWT is signed and checked only by the backend.

## Production

Production configuration (`SERVER_MODE=production`, TLS in front, `TRUSTED_PROXIES`,
`CORS_ALLOWED_ORIGINS`, SMTP, `PUBLIC_BASE_URL`, `APP_BASE_URL`, `FORM_RETENTION_DAYS`, single
sign-on and SCIM) is in [CONFIGURATION.md](CONFIGURATION.md). The deployment options are compared
in the table at the top of the [documentation index](README.md), and [DOCKER.md](DOCKER.md)
("Single sign-on and SCIM") has the Compose side of SSO and SCIM. For the release tarball
with systemd and nginx or Apache on a fresh server (MySQL or MariaDB only), follow
[SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) before the first deploy; its
[Reverse proxy and TLS](SERVER_PROVISIONING.md#reverse-proxy-and-tls) section documents both web servers. To serve the frontend yourself,
run `npm run build` and serve `gocrm-ui/dist` with a web server that falls back to `index.html` for
client-side routes.

## API documentation

The API is described in `api/swagger.json` and `api/swagger.yaml` (Swagger 2.0, regenerated with
`make swagger`). The application does not serve a Swagger UI. API routes mount under `API_PREFIX`
(default `/api/v1`); `/health` and the SCIM 2.0 server at `/scim/v2` are outside it.
