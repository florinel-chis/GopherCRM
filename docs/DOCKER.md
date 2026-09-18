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

- The backend runs with `SERVER_MODE=production` (gin release mode). That is
  safe over plain HTTP because authentication is entirely header-based
  (Bearer JWT / `ApiKey`); the server never sets cookies.
- `TRUSTED_PROXIES=172.28.0.0/16` matches the fixed compose subnet so the
  rate limiter keys on the real client IP forwarded by the ui nginx, not on
  the proxy container's address. If you change the subnet in
  `docker-compose.yml`, change `TRUSTED_PROXIES` with it.
- `VITE_API_BASE_URL` is baked into the UI at **build** time (default
  `/api/v1`, i.e. same-origin through the nginx proxy). To point the UI at a
  different API origin, rebuild:
  `docker compose build ui --build-arg VITE_API_BASE_URL=https://api.example.com/api/v1`.
- Password-reset emails: `SMTP_HOST` is unset, so the mailer logs deliveries
  instead of sending. Add the `SMTP_*` variables to the backend environment
  to enable real mail.

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
stopped, and removing any leftover `-wal`/`-shm` next to it.

## Rebuilding after code changes

```bash
docker compose up -d --build backend   # Go changes
docker compose up -d --build ui        # frontend changes
```
