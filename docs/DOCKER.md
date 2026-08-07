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

## Rebuilding after code changes

```bash
docker compose up -d --build backend   # Go changes
docker compose up -d --build ui        # frontend changes
```
