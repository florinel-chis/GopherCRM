# Running GopherCRM with Docker

The stack is three containers wired by `docker-compose.yml` at the repo root:

| Service   | Image                          | Purpose                                   | Host port |
| --------- | ------------------------------ | ----------------------------------------- | --------- |
| `db`      | `mysql:8.0`                    | Database (`gocrm`)                        | — (opt-in)|
| `backend` | built from `Dockerfile`        | Go API server + `create-admin` CLI        | 8080      |
| `ui`      | built from `gocrm-ui/Dockerfile` | Production React build served by nginx  | 3000      |

The UI container's nginx proxies `/api/v1/` to the backend, so the browser only
ever talks to `http://localhost:3000` — no CORS involved. The API is also
reachable directly on `http://localhost:8080/api/v1` if you want to hit it with
curl or an API key.

If you would rather not run a database server, `docker-compose.sqlite.yml` is a
two-container variant of the same stack backed by a SQLite file — see
[SQLite flavor](#sqlite-flavor-no-database-server).

## Quick start

`JWT_SECRET` is required and must be at least 32 characters. Compose reads it
from your shell or from the repo-root `.env` file (the same one
`.env.example` documents):

```bash
# only needed if you don't already have a .env with JWT_SECRET
export JWT_SECRET="$(openssl rand -base64 32)"

docker compose up -d --build
```

Then open http://localhost:3000. Schema is created automatically by the
backend's auto-migration on startup; the MySQL image creates the `gocrm`
database itself, so no SQL seed script is needed.

Create the first admin account (interactive prompt, or pass flags):

```bash
docker compose exec backend create-admin
# or scripted:
docker compose exec backend create-admin -non-interactive \
  -email admin@example.com -name "Admin" -password 'ChangeMe!2345'
```

Public registration (`POST /auth/register`) only ever creates `customer`
accounts, so this step is how you bootstrap an elevated role.

## Data persistence

MySQL data lives in the named volume `db_data`, mounted at `/var/lib/mysql`.
It survives `docker compose stop`/`start` **and** `docker compose down`.

```bash
docker compose down        # containers gone, data kept
docker compose up -d       # same data, same accounts
docker compose down -v     # destroys the volume — this is the only way data is lost
```

## Configuration

Compose interpolates these from the shell or the repo-root `.env`:

| Variable           | Default                  | Notes                                        |
| ------------------ | ------------------------ | -------------------------------------------- |
| `JWT_SECRET`       | *(required)*             | ≥ 32 chars; startup fails otherwise          |
| `API_KEY_SECRET`   | falls back to JWT_SECRET | HMAC secret for API-key hashing              |
| `DB_PASSWORD`      | `gocrm-dev-password`     | app user password (`gocrm`)                  |
| `DB_ROOT_PASSWORD` | `gocrm-dev-root-password`| MySQL root password                          |
| `UI_PORT`          | `3000`                   | host port for the UI (set it if 3000 is busy)|
| `LOG_LEVEL`        | `info`                   |                                              |

The defaults are for local development only — override both DB passwords for
anything shared.

Notes on the wiring:

- The backend runs with `SERVER_MODE=production` (gin release mode). That does
  not make plain HTTP safe. Authentication is header-based (Bearer JWT /
  `ApiKey`, `internal/middleware/auth.go`), but the login request carries the
  password and every API call carries a token, so over plain HTTP both cross
  the network in clear text. The stack as shipped is for `localhost`; put a
  TLS-terminating reverse proxy in front of the UI port before anyone reaches
  it over a network.
- `TRUSTED_PROXIES=172.28.0.0/16` matches the fixed compose subnet so the
  rate limiter keys on the real client IP forwarded by the ui nginx, not on
  the proxy container's address. If you change the subnet in
  `docker-compose.yml`, change `TRUSTED_PROXIES` with it.
- `VITE_API_BASE_URL` is baked into the UI at **build** time (default
  `/api/v1`, i.e. same-origin through the nginx proxy). To point the UI at a
  different API origin, rebuild:
  `docker compose build ui --build-arg VITE_API_BASE_URL=https://api.example.com/api/v1`.
- `VITE_API_BASE_URL` is the only build argument `gocrm-ui/Dockerfile`
  accepts, and `gocrm-ui/.dockerignore` keeps `.env` and `.env.*` out of the
  build context. `VITE_APP_NAME` therefore never reaches the UI image, which
  always shows the fallback name from the source (`MainLayout.tsx:256` and
  three other places). To brand the image, add `ARG VITE_APP_NAME` and
  `ENV VITE_APP_NAME=$VITE_APP_NAME` before `RUN npm run build` in
  `gocrm-ui/Dockerfile` and pass `--build-arg VITE_APP_NAME=...`. A single
  product-name source is tracked in #221.
- AEO: configure API keys, models, and base URLs as an admin under **AEO >
  Settings**. Changes take effect on the next run and on prompt generation
  without rebuilding. Keys are encrypted; model and endpoint overrides are
  ordinary configuration values. Blank overrides use the backend environment,
  then the provider default. The Compose files do not automatically forward
  arbitrary host environment variables: to use `AEO_*_MODEL` or
  `AEO_*_BASE_URL` instead of the UI, add them to the backend `environment`.
  Gemini uses an OpenAI-compatible API base (including `/v1beta/openai/`);
  Anthropic uses the Messages API root (without `/v1/messages`). An HTTP 503
  can indicate provider capacity: inspect the saved answer error before
  changing endpoints. A model appearing in a provider's catalog does not
  guarantee access for your account.
- Outbound email: `SMTP_HOST` is unset, so the mailer logs deliveries
  instead of sending — password-reset links, forms mail, quote emails and
  the `send_email` step of automation rules alike. A quote email in this mode
  still answers 200 and stamps the quote's `last_emailed_at`, and an
  automation email step is logged as `succeeded`, although nothing was
  delivered. Add the `SMTP_*` variables to the backend environment to enable
  real mail.
- Follow-up automation: the worker is on by default (`AUTOMATION_ENABLED`,
  `AUTOMATION_POLL_INTERVAL_SECONDS` in `.env.example`) and assumes a single
  backend container. If you scale the backend service, keep the worker
  enabled on exactly one replica and set `AUTOMATION_ENABLED=false` on the
  others: those serve the API and record events but neither execute runs nor
  reconcile them at boot. Runs left `running` by a killed worker container
  are recovered when the next worker container boots.

## Running Compose beyond localhost

Both Compose files are built for one developer machine. They set a fixed list
of backend variables (`docker-compose.yml:41-60`,
`docker-compose.sqlite.yml:25-43`) and pass nothing else from the host or the
repo-root `.env`: Compose uses that file only to fill the `${...}`
placeholders in the YAML. Before exposing a stack, know what it does and does
not set.

Set by the Compose files:

- `JWT_SECRET`, `API_KEY_SECRET`, `LOG_LEVEL` and, in the MySQL stack,
  `DB_PASSWORD`, from the shell or `.env`.
- `APP_BASE_URL` and `CORS_ALLOWED_ORIGINS`, **hard-coded** to
  `http://localhost:${UI_PORT}` (and `127.0.0.1`). Behind a real host name,
  password-reset links point at localhost and browsers on the real origin get
  a bare 403 from the CORS check.
- `SERVER_MODE=production`, `TRUSTED_PROXIES` for the fixed subnet,
  `API_PREFIX`, `LOG_FORMAT=json` and the database settings.

Not set, so the backend falls back to its defaults (`internal/config/config.go`):

- `SMTP_*`: mail is logged, not sent (see the outbound email note above).
- `PUBLIC_BASE_URL` (default `http://localhost:8080`): form confirmation links
  point at localhost.
- `RECAPTCHA_*`: the reCAPTCHA spam layer is skipped.
- `AUTOMATION_ENABLED`, `AUTOMATION_POLL_INTERVAL_SECONDS`: the worker runs
  with its defaults.
- `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR`, `AEO_EGRESS_*`, the
  `AEO_CUSTOM_*` engine and the provider key variables (keys can still be set
  in AEO > Settings).
- `JWT_ACCESS_TOKEN_MINUTES`, `REFRESH_TOKEN_EXPIRY_DAYS` and the other
  token lifetimes.

To change any of these, add them to the backend `environment` in a
`docker-compose.override.yml` (Compose merges it automatically for
`docker-compose.yml`; pass it with a second `-f` for the SQLite file) rather
than editing the shipped files. A documented production override is tracked
in #222. In the SQLite stack the backend also loads `/data/.env` from its
volume (`Dockerfile:36-38`); variables set in `environment` take precedence
over it.

### Several installations on one host

Each stack is two (SQLite) or three (MySQL) containers, not one. The host
ports (`8080` for the backend, `UI_PORT` for the UI), the project name
(`gophercrm-sqlite` in the SQLite file, the directory name for the MySQL
file) and the subnet (`172.28.0.0/16` or `172.29.0.0/16`, which
`TRUSTED_PROXIES` must match) are fixed in the files
(`docker-compose.sqlite.yml:20,47,73`, `docker-compose.yml:62,85`). A second
installation from the same file needs, in its override:

- its own project name (`docker compose -p client-b ...`), which also gives it
  its own volume names;
- its own backend host port, or no published backend port at all, and its own
  `UI_PORT`;
- its own subnet, with `TRUSTED_PROXIES` changed to match: two networks with
  the same range cannot both be created;
- its own `JWT_SECRET` and `API_KEY_SECRET`;
- its own host name routed by the reverse proxy, with `APP_BASE_URL`,
  `PUBLIC_BASE_URL` and `CORS_ALLOWED_ORIGINS` set to it.

## SQLite flavor (no database server)

`docker-compose.sqlite.yml` is a standalone alternative to the file above: the
same `backend` and `ui` services, no `db` service, and the backend runs with
`DB_DRIVER=sqlite` writing to a single file at `DB_PATH=/data/gophercrm.db`.
It is a complete stack, not an override — pass only that file:

```bash
docker compose -f docker-compose.sqlite.yml up -d --build
docker compose -f docker-compose.sqlite.yml exec backend create-admin
docker compose -f docker-compose.sqlite.yml logs -f backend
docker compose -f docker-compose.sqlite.yml down
```

`JWT_SECRET`, `API_KEY_SECRET`, `UI_PORT` and `LOG_LEVEL` behave exactly as in
the MySQL stack; `DB_PASSWORD` / `DB_ROOT_PASSWORD` are unused because there is
no database server. Schema still comes from the backend's auto-migration on
startup — the SQL files in `migrations/` target MySQL and are not applied here.

Differences from the MySQL stack worth knowing:

- The file declares its own compose project name (`gophercrm-sqlite`), so it
  never adopts or orphans the MySQL stack's containers, and its own subnet
  (`172.29.0.0/16`, matched by `TRUSTED_PROXIES`) so the two networks coexist.
  Host ports are the same, so the stacks are **alternatives**: stop one before
  starting the other.
- The database file lives in the named volume `gophercrm-sqlite-data` mounted
  at `/data`. It survives `down` and is destroyed only by
  `docker compose -f docker-compose.sqlite.yml down -v`.
- The image creates `/data` owned by the `gophercrm` user, so a fresh volume is
  writable on first boot.
- The connection runs in WAL mode, which requires a **local filesystem**. Do
  not bind-mount `/data` from NFS, SMB or any other network share: SQLite's
  locking is unreliable there and the database can be corrupted. The default
  named volume is local, so this only matters if you replace it.

### Backing up the SQLite database

WAL mode means the database is `gophercrm.db` **plus** `gophercrm.db-wal` and
`gophercrm.db-shm`. Copying `gophercrm.db` alone while the backend is running
yields a stale or torn file.

The volume's full Docker name is `gophercrm-sqlite_gophercrm-sqlite-data`
(project name plus volume name), which is what a throwaway container mounts.

Two safe options:

```bash
# 1. Offline copy: stop the app, then copy the file and its WAL siblings.
#    The glob assumes the database exists — on a stack that has never booted
#    there is nothing to copy and cp reports "no such file".
docker compose -f docker-compose.sqlite.yml stop backend
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data -v "$PWD:/backup" \
  alpine:3.20 sh -c 'cp -a /data/gophercrm.db* /backup/'
docker compose -f docker-compose.sqlite.yml start backend
```

```bash
# 2. Online snapshot, nothing stopped: VACUUM INTO writes one consistent file
#    (the sqlite3 CLI comes from the throwaway container, not the app image).
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data -v "$PWD:/backup" \
  alpine:3.20 sh -c "apk add --no-cache sqlite >/dev/null && \
    sqlite3 /data/gophercrm.db \"VACUUM INTO '/backup/gophercrm-backup.db'\""
```

Restore by putting the file back as `/data/gophercrm.db` with the backend
stopped, and removing any leftover `-wal`/`-shm` next to it. A snapshot from
option 2 lands root-owned — the throwaway container runs as root, the backend
runs as `gophercrm` (uid 10001) — so hand it back before starting the backend,
in the same root container that put the file in place:

```bash
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data \
  alpine:3.20 chown 10001 /data/gophercrm.db
```

Without it the backend opens the database read-only and fails on the first
write ("attempt to write a readonly database").

### Upgrading the SQLite database

Take one of the two backups above before pulling a new image: the schema
advances only through auto-migration on startup, which is not reversible. The
release after 1.2.0 adds the company link to `leads` and `customers`, and the
SQLite driver adds a foreign key by rebuilding the table — drop and copy. The
two rebuilds run one after the other, each in its own transaction, so the first
start needs free space in the `/data` volume for a full copy of one table at a
time, while the WAL file accumulates both rebuilds until the next checkpoint.
Auto-migration runs with foreign-key enforcement switched off, as SQLite
prescribes for table rebuilds, checks every foreign key once it is done and
refuses to start if the check finds a dangling reference. A refused start has
already committed the schema change; only the data violation is left.
`docker compose -f docker-compose.sqlite.yml logs backend` names the tables
involved as `child -> parent`; either restore the backup or fix those rows, then
start again. The 1.2.0 image still opens the upgraded file — it ignores the
extra tables and columns — so the image can be rolled back even though the
schema is not. The deals release adds the `deals` and `deal_stage_changes`
tables only: it rebuilds no existing table, and the previous image still opens
the file. The quotes release does the same with `products`, `quotes`,
`quote_lines` and `quote_status_changes`: four new tables, no existing table
touched, and the previous image still opens the file.

## Rebuilding after code changes

```bash
docker compose up -d --build backend   # Go changes
docker compose up -d --build ui        # frontend changes
```
