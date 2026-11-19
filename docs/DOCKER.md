# Running GopherCRM with Docker

The stack is three containers wired by `docker-compose.yml` at the repo root:

| Service   | Image                          | Purpose                                   | Host port |
| --------- | ------------------------------ | ----------------------------------------- | --------- |
| `db`      | `mysql:8.0`                    | Database (`gocrm`)                        | — (opt-in)|
| `backend` | built from `Dockerfile`        | Go API server + `create-admin` CLI        | 8080      |
| `ui`      | built from `gocrm-ui/Dockerfile` | Production React build served by nginx  | 3000      |

The UI container's nginx proxies `/api/v1/` to the backend, so the browser only
ever talks to `http://localhost:3000`. The browser still sends an `Origin`
header, and nginx forwards `Host` without the port (`gocrm-ui/docker/nginx.conf:14`),
so the backend's CORS allowlist applies; the Compose files list the UI origin
for you (`CORS_ALLOWED_ORIGINS`). The API is also reachable directly on
`http://localhost:8080/api/v1` if you want to hit it with curl or an API key.

If you would rather not run a database server, `docker-compose.sqlite.yml` is a
two-container variant of the same stack backed by a SQLite file — see
[SQLite flavor](#sqlite-flavor-no-database-server).

## Quick start

`JWT_SECRET` is required and must be at least 32 characters; the `.env.example` placeholder is
refused, so a repo-root `.env` copied unedited stops the API container at startup. Compose reads
it from your shell or from the repo-root `.env` file (the same one `.env.example` documents):

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
| `VITE_APP_NAME`    | empty (shows `GoCRM`)    | UI build argument, public; rebuild to change |

The defaults are for local development only — override both DB passwords for
anything shared.

Notes on the wiring:

- The backend runs with `SERVER_MODE=production` (gin release mode). That does
  not make plain HTTP safe. Authentication is header-based (Bearer JWT /
  `ApiKey`, `internal/middleware/auth.go`), but the login request carries the
  password and every API call carries a token, so over plain HTTP both cross
  the network in clear text. The stack as shipped is for `localhost`; put a
  TLS-terminating reverse proxy in front of the UI port before anyone reaches
  it over a network. Both files publish the backend's `8080` and the UI port on
  every host interface (`docker-compose.yml:66-67,83-84`,
  `docker-compose.sqlite.yml:51-52,68-69`), so the proxy alone leaves both
  reachable over plain HTTP: remove the backend publication and bind the UI to
  loopback (or a private network) in an override, as shown under
  [Several installations on one host](#several-installations-on-one-host).
- `TRUSTED_PROXIES=172.28.0.0/16` matches the fixed compose subnet so the
  rate limiter keys on the real client IP forwarded by the ui nginx, not on
  the proxy container's address. If you change the subnet in
  `docker-compose.yml`, change `TRUSTED_PROXIES` with it.
- `VITE_API_BASE_URL` is baked into the UI at **build** time (default
  `/api/v1`, i.e. same-origin through the nginx proxy). To point the UI at a
  different API origin, rebuild:
  `docker compose build ui --build-arg VITE_API_BASE_URL=https://api.example.com/api/v1`.
- `gocrm-ui/Dockerfile` also accepts an optional `VITE_APP_NAME` build
  argument (#303), the product name the sign-in and registration pages show.
  The product name comes from one helper,
  `gocrm-ui/src/hooks/useProductName.ts`: the stored `general.company_name`
  for a signed-in user, otherwise `VITE_APP_NAME`, otherwise `GoCRM`. Its
  default is empty, so an image built without it shows `GoCRM` before sign-in,
  and signed-in users still see the stored company name (Settings) either way.
  Both dev Compose files forward it (#308): the `ui` service builds with
  `build.args` `VITE_APP_NAME: ${VITE_APP_NAME:-}`, so Compose takes the value
  from your shell or the repo-root `.env`, and `docker compose build ui` or
  `docker compose up --build` brands the pages. Unset or empty, the image keeps
  the empty default and shows `GoCRM`. Compose interpolates the whole file even
  for `docker compose build ui`, so `JWT_SECRET` must be set for that command
  too. Set the name when you build the image:

  ```bash
  docker build --build-arg VITE_APP_NAME=Acme gocrm-ui
  VITE_APP_NAME=Acme docker compose build ui
  # or put VITE_APP_NAME=Acme in the repo-root .env, then:
  docker compose up -d --build
  ```

  The signed-out pages then read "Welcome to Acme" and "Create your Acme
  account". Like `VITE_API_BASE_URL`, the value is fixed when the image is
  built: changing the name needs a rebuild. The name is **not a
  secret**: Vite inlines it into the public `JavaScript` bundle that nginx
  serves to every visitor. The `ARG`/`ENV` pair lives only in the build
  stage, not in the nginx stage. Never pass a secret as a `VITE_*` build
  argument. `gocrm-ui/.dockerignore` still keeps `.env` and `.env.*` out of
  the build context, so a local `gocrm-ui/.env` never sets the name in the
  image; only the build argument (or the repo-root `.env` value Compose
  forwards as one) does.
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
  `AUTOMATION_POLL_INTERVAL_SECONDS` in `.env.example`). On MySQL/MariaDB
  every backend container may keep it on (#237): each run claim records the
  container's instance id and a heartbeat it renews every 30 s, every later
  write is fenced on that owner, and a run left `running` by a killed worker
  container is recovered by the next tick of any worker once its heartbeat is
  older than 3 minutes, without a restart. A container with
  `AUTOMATION_ENABLED=false` serves the API and records events but neither
  executes nor recovers runs. AEO is coordinated the same way on
  MySQL/MariaDB (#229): each run has an owner and a heartbeat in
  `aeo_runs`, one run is admitted across all containers, the daily scheduled
  run starts once per slot whichever containers keep `AEO_SCHEDULE_ENABLED`
  on, and a starting container fails only runs whose heartbeat is older than
  3 minutes (see `aeoService.ReconcileRunningRuns` in `setupDependencies`).
  Give every container the same `TZ` and `AEO_SCHEDULE_HOUR`, keep their
  clocks synchronised, and upgrade by stopping every backend container before
  starting the new image. The SQLite stack runs one backend container; several
  processes on one SQLite file are not supported. The form retention purge
  (`FORM_RETENTION_DAYS`) is not part of the worker limit either: it is safe on
  every replica, because extra sweeps only compete for the same rows.

### Quote PDF output

The backend image ships what quote PDF output needs: `chrome-headless-shell` (the browser's old
headless mode as its own binary, without the full browser), the `fonts-liberation` fonts
(without a font the PDF is a valid file with no readable text) and
`QUOTE_PDF_CHROME_PATH=/usr/local/bin/quote-pdf-browser`, a launcher of that binary. The server
prints a quote by running it on `APP_BASE_URL/quote-render`, a public page of the web
application that loads the quote from the API with a short-lived, single-quote, read-only
credential. The full `chromium` package is no longer installed (#393); a deployment that
prefers one can still point `QUOTE_PDF_CHROME_PATH` at a full Chrome or Chromium, or at a
launcher for it: the server starts both kinds of binary with the same arguments.

- **Where the binary comes from.** The runtime stage is `debian:bookworm-slim` and installs
  the Debian package `chromium-headless-shell`. Alpine, the base before #393, has no headless
  shell package (3.20 and 3.22 offer only the full `chromium` and its add-ons), and the
  prebuilt binaries are linked against glibc, so the base had to change. The other candidate,
  copying the binary from the `chromedp/headless-shell` image, was dropped: its builds
  (157 to 159, linux/arm64) wrote no file for `--print-to-pdf` and ran until the time bound.
- **Pinned version.** `ARG HEADLESS_SHELL_VERSION=156.0.8078.79` in the Dockerfile is the one
  place the browser version is written; the package is installed as
  `chromium-headless-shell=<version>-1~deb12u1`. Debian's security repository keeps only the
  newest version, so after the next Chromium update of bookworm the build stops at
  `Version '...' for 'chromium-headless-shell' was not found`. Raise the argument then to the
  candidate that
  `docker run --rm debian:bookworm-slim sh -c 'apt-get update -qq && apt-cache policy chromium-headless-shell'`
  prints (or pass `--build-arg HEADLESS_SHELL_VERSION=<version>`), and run
  `scripts/e2e/quote-pdf-chain.sh image` before you ship the image.
- **Image size.** The browser and the libraries its package depends on are most of the image:
  700,980,224 bytes (about 669 MiB) with `chrome-headless-shell` 156 on `debian:bookworm-slim`,
  against 686,689,600 bytes (about 655 MiB) before with the full Chromium 131 on alpine 3.20
  (`docker image inspect --format '{{.Size}}'`, linux/arm64). The image did not get smaller:
  the headless shell itself is 211 MB and its package pulls in the Mesa and LLVM libraries.
- **Other consequences of the new base.** The image installs `wget` only because both compose
  files health-check the backend with it and `debian:bookworm-slim` has none; it has no `curl`
  and no `ps`. The init is at `/usr/bin/tini`, not where the Alpine image had it (see the
  upgrade notes of #393 in `CHANGELOG.md`).
- **Flags and init, in the image only.** `/usr/local/bin/quote-pdf-browser` is a two-line
  script that execs `/usr/lib/chromium/chromium-headless-shell` with `--no-sandbox` (as uid
  10001 in a container the browser cannot set up its own sandbox and refuses to start; the
  container is the boundary) and `--disable-dev-shm-usage` (a container's `/dev/shm` is 64 MB;
  shared memory goes to `/tmp`). It uses `exec` so that the process the server started, whose
  process group it kills, is the browser itself. The server never passes either flag. The
  image starts the server under `tini`
  (`ENTRYPOINT ["/usr/bin/tini", "--", "gophercrm"]`): a render kills the browser's whole process
  group, the helpers orphaned that way are handed to PID 1, and an init reaps them where the
  server would leave zombies. If you replace the entrypoint, keep an init (`docker run --init`,
  `init: true` in Compose).
- **What the browser may write.** Only one temporary directory per render, under `/tmp`,
  removed afterwards. The image was verified as uid 10001 with a read-only root file system and
  only `/tmp` (tmpfs) and `/data` writable.
- **Off in both development stacks.** `docker-compose.yml` and `docker-compose.sqlite.yml` set
  `QUOTE_PDF_CHROME_PATH: ""`. Their `APP_BASE_URL` is `http://localhost:${UI_PORT}`, and inside
  the backend container `localhost` is that container, not the UI: every render would fail. With
  the variable empty (or naming a file that is not executable) PDF output is switched off: the
  server starts as usual, quote emails go out as text only, `GET /api/v1/quotes/:id/pdf` answers
  503 `PDF_UNAVAILABLE`, `GET /api/v1/configurations/ui` reports `quotes.pdf_available` `false`
  and the quote page shows no Download PDF button.
- **Switching it on.** The production override example does not forward the variable, so the
  stack it is applied to keeps PDF output off. Add `QUOTE_PDF_CHROME_PATH: /usr/local/bin/quote-pdf-browser`
  (the image's default) to the backend `environment` in your own override, together with an `APP_BASE_URL` that meets
  both conditions:
  - it is **reachable from inside the backend container**. A public origin works when the
    container can resolve and reach it (through your TLS proxy); `http://ui`, the UI service on
    the Compose network, works without leaving the host;
  - its origin is **allowed by `CORS_ALLOWED_ORIGINS`** when the API is on another origin than
    `APP_BASE_URL`. The render page calls `POST /api/v1/quotes/public/render` from the browser
    with an `Origin` header; an origin that is not listed gets a bare 403 that the backend does
    not log, the credential is never redeemed, and every render fails: the download answers 503
    `PDF_RENDER_FAILED` and the quote email falls back to text. With the UI nginx proxying
    `/api/v1/` the call is same-origin, but the browser still sends `Origin`, so list the
    `APP_BASE_URL` origin either way.
- **Time budget.** One render runs at a time per backend process and is bounded at 15 s, the
  wait for its turn included. `POST /api/v1/quotes/:id/email` takes at most 15 s for the render
  plus 30 s for the SMTP delivery, 45 s; the web application waits 55 s for that call and for
  the PDF download. A reverse proxy in front must keep a read timeout of 60 s or more
  (`proxy_read_timeout`, 60 s by default in nginx; the UI image's nginx does not lower it), or
  it answers 504 for a mail the backend still delivers.
- Known limits (credentials per backend process, the render URL in the container's process list
  for the life of a render, dates in the container's time zone, UTC) are in `docs/ROADMAP.md`.

## Running Compose beyond localhost

Both Compose files are built for one developer machine. They set a fixed list
of backend variables (`docker-compose.yml:41-65`,
`docker-compose.sqlite.yml:25-48`) and pass nothing else from the host or the
repo-root `.env`: Compose uses that file only to fill the `${...}`
placeholders in the YAML. Before exposing a stack, know what it does and does
not set.

Set by the Compose files:

- `JWT_SECRET`, `API_KEY_SECRET`, `LOG_LEVEL` and, in the MySQL stack,
  `DB_PASSWORD`, from the shell or `.env`.
- `APP_BASE_URL` and `CORS_ALLOWED_ORIGINS`, **hard-coded** to
  `http://localhost:${UI_PORT}` (and `127.0.0.1`). Behind a real host name,
  password-reset links point at localhost. Browsers on an origin that is not
  listed get a bare 403 from the CORS check, except when the `Origin` equals
  `http://` or `https://` plus the `Host` the backend receives, which the
  pinned `gin-contrib/cors` v1.7.5 (`go.mod:7`) lets through: a TLS proxy on
  port 443 that forwards the host name to the UI nginx passes, a UI on any other
  origin than the API's does not. Set `CORS_ALLOWED_ORIGINS` to your real
  origin either way.
- `SERVER_MODE=production`, `TRUSTED_PROXIES` for the fixed subnet,
  `API_PREFIX`, `LOG_FORMAT=json` and the database settings.
- `QUOTE_PDF_CHROME_PATH`, **empty**: quote PDF output is off in both stacks
  (see [Quote PDF output](#quote-pdf-output)).

Not set, so the backend falls back to its defaults (`internal/config/config.go`):

- `SMTP_*`: mail is logged, not sent (see the outbound email note above).
- `PUBLIC_BASE_URL` (default `http://localhost:8080`): form confirmation links
  point at localhost.
- `RECAPTCHA_*`: the reCAPTCHA spam layer is skipped.
- `FORM_RETENTION_DAYS`: the form retention purge stays off, so spam and
  never-confirmed submissions are kept. To enable it, add the variable to the
  backend environment; the first boot with it set permanently removes the
  existing backlog, so take a backup first (see the backup section below).
- `AUTOMATION_ENABLED`, `AUTOMATION_POLL_INTERVAL_SECONDS`: the worker runs
  with its defaults.
- `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR`, `AEO_EGRESS_*`, the
  `AEO_CUSTOM_*` engine and the provider key variables (keys can still be set
  in AEO > Settings).
- `JWT_EXPIRY_HOURS` (access-token lifetime, default 24) and
  `REFRESH_TOKEN_EXPIRY_DAYS` (default 30), `internal/config/config.go:299-304`.
  `JWT_ACCESS_TOKEN_MINUTES` is read too but affects nothing the server
  issues.

To change any of these, add them to the backend `environment` in an override
file rather than editing the shipped files. The committed
`docker-compose.production.example.yml` covers the common production set; see
[Production override example](#production-override-example) below. In the
SQLite stack the backend also loads `/data/.env` from its volume
(`Dockerfile:73-75`); variables set in `environment` take precedence over it.

### Production override example

`docker-compose.production.example.yml` at the repository root is an override
for either stack. Compose never loads it on its own (unlike a
`docker-compose.override.yml`), so a plain `docker compose up` is unchanged;
name it with a second `-f`. It forwards these variables to the backend, each
as a `${...}` interpolation and nothing else:

- `APP_BASE_URL`, `PUBLIC_BASE_URL` and `CORS_ALLOWED_ORIGINS`, **required**:
  Compose refuses the file with a message when one is unset or empty;
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM`;
- `RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY`, `RECAPTCHA_MIN_SCORE`;
- `AUTOMATION_ENABLED`, `AUTOMATION_POLL_INTERVAL_SECONDS`;
- `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR`.

An optional variable you leave unset reaches the backend as an empty string,
which keeps the backend default (mail logged, reCAPTCHA off, the worker on
every 30 seconds, the AEO run at hour 6).

What the values mean here:

- `APP_BASE_URL` is the browser origin of the UI, for example
  `https://crm.example.com`, used in password-reset links and, when quote PDF
  output is switched on, opened by the backend's own browser.
- `PUBLIC_BASE_URL` is the public origin of the UI proxy, normally the same
  value as `APP_BASE_URL`. The example removes the backend's host port, so
  form confirmation links, the hosted form page and the embed snippet reach
  the API through the UI nginx `/api/v1/` proxy, not on port 8080.
- `CORS_ALLOWED_ORIGINS` is a comma-separated list added to the built-in
  localhost origins; list every origin your UI is served from.
- The stack runs one backend. The automation worker (#237), AEO runs and the
  daily schedule (#229) are coordinated across backend replicas on
  MySQL/MariaDB if you scale it: keep the containers' clocks synchronised and
  stop every backend container before starting a new image.

Warning: `.env.example` sets `PUBLIC_BASE_URL` to `http://localhost:8080`. A
repo-root `.env` copied from it therefore satisfies the required check with a
localhost value; replace that line with your UI proxy origin.

Where the values come from: the shell or the repo-root `.env` (already
git-ignored), which Compose reads to fill the placeholders. The example sets
each of its 15 keys in the container environment, empty when unset, and an
environment variable takes precedence over `/data/.env`. In the SQLite stack
a value for one of these keys in `/data/.env` is therefore masked and
ignored; move it to the shell or the repo-root `.env`. Never write a real
value into the YAML. When you extend the file (with `FORM_RETENTION_DAYS`,
`AEO_EGRESS_*`, `AEO_CUSTOM_*`, provider keys or `JWT_EXPIRY_HOURS`, for
example), keep every value as a `${...}` interpolation, or keep the extended
copy outside the working tree so it cannot be committed. Inside the working
tree, name a local copy `docker-compose.prod.yml` (passed with a second
`-f`) or `docker-compose.override.yml` (which Compose loads on its own): both
names are git-ignored.

Ports: the example removes the backend's `8080` publication
(`ports: !reset []`) and replaces the UI publication with
`127.0.0.1:${UI_PORT:-3000}:80` (`ports: !override`), so only a TLS proxy on
the same host reaches the stack. The `!override` tag needs Compose 2.24.4 or
later. An older Compose does **not** reject the file: Compose 2.19.0 accepts
it, `config -q` succeeds, `!reset []` still removes the backend port, but
`!override` is ignored and the loopback entry is appended to the shipped
all-interfaces `"${UI_PORT:-3000}:80"`, so the UI stays reachable over plain
HTTP. Check `docker compose version` (2.24.4 or later), and before exposing
the host read the merged ports with `config` without `-q`, for example
`docker compose -f docker-compose.yml -f docker-compose.production.example.yml config`:
`backend` must show no `ports` and `ui` exactly one publication, with
`host_ip: 127.0.0.1`.

It leaves `TRUSTED_PROXIES`, the subnet, the project name, the volumes, the
secrets (`JWT_SECRET`, `API_KEY_SECRET`, `DB_*`) and `LOG_*` as the stack
sets them, which is why one file fits both stacks.

Validate the merge with both files named, then start with the same pair:

```bash
docker compose -f docker-compose.yml -f docker-compose.production.example.yml config -q
docker compose -f docker-compose.sqlite.yml -f docker-compose.production.example.yml config -q
docker compose -f docker-compose.yml -f docker-compose.production.example.yml up -d --build
```

Use the SQLite file in the last command for that stack, and pass the same
`-f` pair to every later `exec`, `logs` or `down`.

### Several installations on one host

Each stack is two (SQLite) or three (MySQL) containers, not one. The host
ports (`8080` for the backend, `UI_PORT` for the UI), the project name
(`gophercrm-sqlite` in the SQLite file, the directory name for the MySQL
file) and the subnet (`172.28.0.0/16` or `172.29.0.0/16`, which
`TRUSTED_PROXIES` must match) are fixed in the files
(`docker-compose.sqlite.yml:20,52,81`, `docker-compose.yml:67,93`). A second
installation from the same file needs, in its override:

- its own project name (`docker compose -p client-b ...`), which also gives it
  its own volume names;
- its own backend host port, or no published backend port at all, and its own
  `UI_PORT`. A plain `ports:` list in an override is **added** to the shipped
  one, and `ports: []` changes nothing, so both keep `8080` published. Remove a
  publication with the `!reset` tag (checked with Compose 2.19.0), then add the replacement from a further
  file, or replace the list in one file with `!override` (Compose 2.24.4 or
  later):

  ```yaml
  # docker-compose.override.yml
  services:
    backend:
      ports: !reset []          # no host port for the API
    ui:
      ports: !override          # Compose >= 2.24.4
        - "127.0.0.1:3101:80"   # loopback only, behind the TLS proxy
  ```

  Check the result with `docker compose -f docker-compose.yml -f
  docker-compose.override.yml config` before starting it (an explicit `-f`
  disables the automatic pick-up of the override file);
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

Restore with the backend stopped, and match the restore to the backup:

- **Offline copy (option 1):** put back `gophercrm.db` together with the
  `gophercrm.db-wal` and `gophercrm.db-shm` copied with it, replacing all
  three. The WAL can hold committed transactions that are not yet in the main
  file — the backend can exit without a final checkpoint (`cmd/main.go:198-208`)
  — so deleting it, or mixing it with another backup's main file, loses data.
- **`VACUUM INTO` snapshot (option 2):** it is one self-contained file. Put it
  back as `/data/gophercrm.db` and delete any `-wal`/`-shm` left in the
  volume, which belong to the database you are replacing.

A file written by a root container lands root-owned — the throwaway container
runs as root, the backend runs as `gophercrm` (uid 10001) — so hand the
restored files back before starting the backend, in the same root container
that put them in place:

```bash
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data \
  alpine:3.20 sh -c 'chown 10001 /data/gophercrm.db*'
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
touched, and the previous image still opens the file. The task-to-deal link
release does touch one: it adds `tasks.deal_id` and rebuilds `tasks` — drop and
copy, in one transaction with enforcement off — to add `fk_tasks_deal`, so the
first start needs free space in the `/data` volume for a full copy of `tasks`,
and the WAL file holds that copy until the next checkpoint. Every existing task
keeps `deal_id` NULL. The previous image still opens the upgraded file: it
ignores the extra column, so it neither shows nor removes a stored link, and
the link is back when the newer image returns.

## Rebuilding after code changes

```bash
docker compose up -d --build backend   # Go changes
docker compose up -d --build ui        # frontend changes
```
