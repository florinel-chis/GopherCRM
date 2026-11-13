# Server provisioning for the release-tarball deploy

This guide prepares a fresh Linux server for the deploy path described in
[FORKING.md](FORKING.md#52-release-tarball-and-systemd-through-the-shipped-workflows) section 5.2:
the Release workflow builds a tarball, the Deploy workflow sends it over SSH, and
`scripts/deploy/receive-release.sh` installs it. Those scripts install releases on a server that
is already provisioned; they do not provision it. Follow the steps below in order, once, before
the first deploy.

Every file shown here is an example with placeholders. Nothing in this guide ships as a file in
the repository, and nothing under `scripts/deploy` reads this page. When a step and a script
disagree, the script wins: re-check the line references below against your checkout.

## Scope

- **Host.** A Linux server with systemd and nginx, reachable over SSH as root from the machine
  that runs `scripts/deploy/bootstrap.sh`.
- **Database.** MySQL 8 or MariaDB 10.11, on the same host and listening on `127.0.0.1:3306`.
  Installing and tuning the database server is outside this guide; it only creates the database
  and the account the backend uses.
- **SQLite is not supported on this path.** Before every deploy `receive-release` backs the
  database up with `mysqldump` (`scripts/deploy/receive-release.sh:206`) and aborts the deploy when
  that backup fails, and `bootstrap.sh` refuses a server without `mysqldump` (line 63). For a
  SQLite deployment use the SQLite Compose stack described in [DOCKER.md](DOCKER.md).
- **Names and paths.** The guide uses the defaults of `receive-release.sh` (lines 52-56). Change
  them only together with the matching `GOPHERCRM_*` overrides on the server.

| Setting | Default | Used for |
|---|---|---|
| `ROOT` | `/srv/gophercrm` | releases, binaries, backups, deploy script |
| `WEB_ROOT` | `/var/www/gophercrm` | the built UI, synced with `rsync --delete` on every deploy |
| `ENV_FILE` | `/srv/gophercrm/env/gophercrm.env` | the backend environment, also read for the backup |
| `SERVICE` | `gophercrm` | the systemd unit `gophercrm.service`, restarted on every deploy |
| `HEALTH_URL` | `http://127.0.0.1:8080/health` | the post-restart health check |

The public name in the examples is `crm.example.com`; replace it everywhere.

## Packages

`bootstrap.sh` checks for these tools on the server before it installs anything
(`scripts/deploy/bootstrap.sh:63`): `flock`, `rsync`, `mysqldump`, `gzip`, `curl`, `tar` and
`systemctl`. The proxy and the certificate need `nginx` and `certbot` as well.

On Debian or Ubuntu, for example:

```bash
apt-get update
apt-get install util-linux rsync default-mysql-client gzip curl tar systemd nginx certbot
```

Package names differ between distributions and releases (`flock` is part of `util-linux`;
`mysqldump` comes from `default-mysql-client`, `mysql-client` or `mariadb-client`). Verify them for
yours, then confirm every tool is on the `PATH`:

```bash
for tool in flock rsync mysqldump gzip curl tar systemctl nginx certbot; do
  command -v "$tool" >/dev/null || echo "missing: $tool"
done
```

## Service user

The backend runs as an unprivileged system account with no login shell:

```bash
useradd --system --user-group --no-create-home --home-dir /var/lib/gophercrm --shell /usr/sbin/nologin gophercrm
```

The home directory is created by systemd through the unit's `StateDirectory=` (see *Systemd unit*);
it is the only place the service may write.

## Directories

```bash
install -d -o root -g root -m 755 /srv/gophercrm
install -d -o root -g root -m 700 /srv/gophercrm/env
```

`receive-release` runs as root and trusts what it finds under `/srv/gophercrm`, so it refuses any
directory it uses (`/srv/gophercrm`, `releases`, `deploy`, `bin`, `backups` and the web root) that
is a symbolic link, is not owned by root, or is writable by group or others
(`require_private`, `scripts/deploy/receive-release.sh:33-39`); a loosened mode stops the deploy
instead of being silently reset. `bootstrap.sh` re-owns `/srv/gophercrm` but does not create it
(line 69); it creates `bin`, `deploy`, `releases`, `backups` (700) and the web root itself. Mode
755 on `/srv/gophercrm` lets the service user reach `bin/`; `env/` stays root-only because systemd
reads the environment file as root before it drops privileges.

## Environment file

Create `/srv/gophercrm/env/gophercrm.env` with the content below, then replace every `CHANGE_THIS`
value:

```dotenv
# GopherCRM backend environment, read by systemd (EnvironmentFile=) and by
# receive-release for the pre-deploy mysqldump. One KEY=VALUE per line; no
# backslash, dollar sign or backtick in any value.
DB_DRIVER=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=gophercrm
DB_USER=gophercrm
DB_PASSWORD=CHANGE_THIS_DB_PASSWORD
SERVER_HOST=127.0.0.1
SERVER_PORT=8080
SERVER_MODE=production
API_PREFIX=/api/v1
TRUSTED_PROXIES=127.0.0.1
APP_BASE_URL=https://crm.example.com
PUBLIC_BASE_URL=https://crm.example.com
CORS_ALLOWED_ORIGINS=https://crm.example.com
# Generate with: openssl rand -base64 32
JWT_SECRET=CHANGE_THIS_TO_THE_OUTPUT_OF_OPENSSL_RAND
LOG_LEVEL=info
LOG_FORMAT=json
# Optional: a separate key for API keys, tokens and stored settings (FORKING section 4.2).
# API_KEY_SECRET=
# Mail: with SMTP_HOST empty every mail is only logged.
# SMTP_HOST=
# SMTP_PORT=587
# SMTP_USER=
# SMTP_PASSWORD=
# SMTP_FROM=no-reply@example.com
# FORM_RETENTION_DAYS=30
# AUTOMATION_ENABLED=true
```

```bash
chown root:root /srv/gophercrm/env/gophercrm.env
chmod 600 /srv/gophercrm/env/gophercrm.env
```

- **The secrets.** `JWT_SECRET` must be at least 32 characters; any value starting with
  `CHANGE_THIS` is refused and the backend does not start (`internal/config/config.go:270-280`).
  Use `openssl rand -base64 32`. For `DB_PASSWORD` pick a value without quotes, backslash, `$` or
  backtick (for example `openssl rand -hex 24`); `DB_PASSWORD` itself is not checked at startup,
  so a forgotten placeholder only shows up as a database login with a known password. The same
  password goes into the SQL block under *MySQL database*.
- **One parser for two readers.** systemd reads the file through `EnvironmentFile=`, and
  `receive-release` reads `DB_USER`, `DB_PASSWORD`, `DB_HOST`, `DB_PORT` and `DB_NAME` from it with
  `sed` for the backup (`env_value`, `scripts/deploy/receive-release.sh:176-178`). That reader
  takes the last `KEY=` line, strips one pair of quotes and decodes no escapes, so keep each key on
  one active line and the values plain. A wrong `DB_*` value fails the backup, which stops the
  deploy before anything changes.
- **Why `EnvironmentFile=`.** The backend loads only a `.env` file in its working directory
  (`config.Load`, `internal/config/config.go:264`); the unit's working directory holds none, so
  every setting comes from this file through systemd. Do not add a `.env` to
  `/var/lib/gophercrm`: it would only fill keys the environment file leaves unset.
- **`TRUSTED_PROXIES`.** The rate limiter keys on the client address. Behind nginx on the same
  host every request arrives from `127.0.0.1`; trusting that address makes the backend take the
  client address nginx forwards, instead of putting every visitor in one rate-limit bucket
  ([FORKING.md](FORKING.md) section 4.2). nginx below overwrites `X-Forwarded-For` with the
  address it sees, so a client cannot inject one.
- **`SERVER_HOST=127.0.0.1`** keeps port 8080 off the network; only nginx reaches it.
- **URLs.** `APP_BASE_URL` builds password-reset links, `PUBLIC_BASE_URL` form confirmation links,
  and `CORS_ALLOWED_ORIGINS` admits the UI's origin. With the UI and the API on one origin all
  three are the public URL.

Every key in the block is documented in `.env.example`.

## Systemd unit

Install this unit as `/etc/systemd/system/gophercrm.service`:

```ini
[Unit]
Description=GopherCRM API
After=network-online.target mysql.service mariadb.service
Wants=network-online.target

[Service]
Type=simple
User=gophercrm
Group=gophercrm
ExecStart=/srv/gophercrm/bin/gophercrm
EnvironmentFile=/srv/gophercrm/env/gophercrm.env
StateDirectory=gophercrm
WorkingDirectory=/var/lib/gophercrm
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable gophercrm
```

Enable it, but do not start it: `/srv/gophercrm/bin/gophercrm` is a link that the first deploy
creates (`scripts/deploy/receive-release.sh:137`), and that deploy restarts the service itself
(line 161). Notes on the directives:

- `ExecStart` is the link into the active release, so a deploy or a rollback needs no unit change.
- `EnvironmentFile=` has no leading `-`: a missing file stops the start instead of running the
  backend with defaults (MySQL as `root`, every interface).
- `ProtectSystem=strict` makes the whole file system read-only for the service except its
  `StateDirectory=` (`/var/lib/gophercrm`); it never writes `bin/`, `releases/`, `backups/`, `env/`
  or the web root.
- `TimeoutStopSec=30` leaves room for the backend's own shutdown: up to 15 s to drain the
  background workers and 5 s for open requests.
- `After=` names both database units; systemd ignores the one that does not exist.

## MySQL database

Run this as a database administrator (for example `mysql -u root`), with `CHANGE_THIS_DB_PASSWORD`
replaced by the very same value you wrote to `DB_PASSWORD`:

```sql
CREATE DATABASE IF NOT EXISTS gophercrm CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'gophercrm'@'127.0.0.1' IDENTIFIED BY 'CHANGE_THIS_DB_PASSWORD';
GRANT ALL PRIVILEGES ON gophercrm.* TO 'gophercrm'@'127.0.0.1';
```

- The database name, the account and the password match `DB_NAME`, `DB_USER` and `DB_PASSWORD` of
  the environment file. The backend and the pre-deploy `mysqldump` both log in with them, so the
  backend never runs as the MySQL `root` account.
- The grant covers the `gophercrm` database only. The backend needs to create and alter its own
  tables (it migrates its schema at start, see *Schema migrations*); the dump uses
  `--single-transaction --no-tablespaces`, which needs no global privilege.
- The account host is `127.0.0.1` because `DB_HOST=127.0.0.1` connects over TCP. If you change
  `DB_HOST` to `localhost`, the client uses the Unix socket and the account must be
  `'gophercrm'@'localhost'` instead.
- Check the login before going further:
  `mysql -h 127.0.0.1 -u gophercrm -p gophercrm -e 'SELECT 1'`.

## Reverse proxy and TLS

nginx serves the UI from the web root, proxies the API and `/health` to the backend on
`127.0.0.1:8080`, and terminates TLS. The certificate comes from Let's Encrypt through certbot's
webroot mode, which needs the plain-HTTP site first.

Create the ACME directory. It sits outside the web root on purpose: every deploy syncs
`/var/www/gophercrm` with `rsync --delete` (`scripts/deploy/receive-release.sh:151`), which would
remove challenge files placed there.

```bash
install -d -o root -g root -m 755 /var/www/letsencrypt
```

HTTP site, for example `/etc/nginx/sites-available/gophercrm-http` (linked into `sites-enabled`):

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name crm.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/letsencrypt;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}
```

Reload nginx and request the certificate:

```bash
nginx -t && systemctl reload nginx
certbot certonly --webroot -w /var/www/letsencrypt -d crm.example.com --deploy-hook 'systemctl reload nginx'
```

TLS site, for example `/etc/nginx/sites-available/gophercrm-https`:

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name crm.example.com;

    ssl_certificate /etc/letsencrypt/live/crm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/crm.example.com/privkey.pem;

    root /var/www/gophercrm;
    index index.html;

    location /api/v1/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location = /health {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

Enable it and run `nginx -t && systemctl reload nginx` again.

- `proxy_pass` carries no URI part, so the request path reaches the backend unchanged
  (`/api/v1/...` matches `API_PREFIX`).
- `/health` must be proxied: after `receive-release` has seen the new revision on
  `http://127.0.0.1:8080/health`, the Deploy workflow checks `<public URL>/health` as visitors
  reach it (`.github/workflows/deploy.yml:113-129`).
- `Host $host` keeps the UI and the API on one origin for the CORS check, and `X-Forwarded-For
  $remote_addr` is the single address the backend trusts through `TRUSTED_PROXIES=127.0.0.1`.
- `try_files ... /index.html` serves the single-page UI for client-side routes.
- The web root stays empty until the first deploy, so the site answers 404 until then.

## Bootstrap and first deploy

From the repository root on your workstation (root SSH access to the server, a `gh` login with
admin rights, `main` protected):

```bash
scripts/deploy/bootstrap.sh root@203.0.113.10 https://crm.example.com /var/www/gophercrm
```

Use the server's direct address, not a CDN-proxied name. Bootstrap installs `receive-release` as
`/srv/gophercrm/deploy/receive-release`, writes the web root into its configuration, pins a new
deploy key to it in `authorized_keys`, and creates the GitHub `production` environment with its
secrets ([FORKING.md](FORKING.md) section 5.2, steps 1-2).

Then publish a release (`docs/VALIDATION_RELEASE.md`, "Prepare, rehearse, publish"). The Deploy
workflow runs `receive-release deploy <sha>`, which unpacks the tarball into
`/srv/gophercrm/releases/<sha>`, takes the `mysqldump` backup, points `/srv/gophercrm/current` and
`bin/` at the release, syncs the UI into the web root, restarts `gophercrm.service` and waits for
`http://127.0.0.1:8080/health` to report the new revision, rolling back on failure. Watch the
first start with `journalctl -u gophercrm -f`.

## Schema migrations

The backend runs `models.MigrateDatabase` (GORM auto-migration) at every start (`cmd/main.go:140`),
so the first start creates the tables and later starts add new columns and tables.

The release tarball carries the SQL half as well: `bin/migrate` and the `migrations/` directory
(`scripts/release/build.sh:8,15`), found on the server at `/srv/gophercrm/current/migrations` with
the tool at `/srv/gophercrm/current/bin/migrate`. receive-release does not run them: no deploy
step calls `bin/migrate` or reads `current/migrations`. On MySQL and MariaDB the SQL migrations are
applied with golang-migrate as described in
[FORKING.md, section 7.4](FORKING.md#74-migration-versions), including the rules on version
order and the populated-database rehearsal. `bin/migrate` reads the same settings as the backend
through `config.Load`, so it needs this environment file. This path does not automate the step:
run it yourself after a deploy that brings new files under `migrations/`, and keep the pre-deploy
backup until it has succeeded.

## First admin

Public registration only ever creates customer accounts, so the first admin is created on the
server with `bin/create-admin`, as the service user and with the service environment:

```bash
systemd-run --pty --uid=gophercrm --gid=gophercrm -p EnvironmentFile=/srv/gophercrm/env/gophercrm.env /srv/gophercrm/bin/create-admin
```

It prompts for the email, the name and the password, and reads the password from the terminal
without echo (`term.ReadPassword`, `cmd/create-admin/main.go:104-115`), which is why `--pty` is
required. Type the password at the prompt. Do not use create-admin's flag mode
(`cmd/create-admin/main.go:34-35`): it takes the password on the command line, where the process
list and the shell history can see it. The link `/srv/gophercrm/bin/create-admin` exists once the
first deploy has run.

## Backups

Before every deploy `receive-release` writes
`/srv/gophercrm/backups/<UTC timestamp>-<sha>.sql.gz` with `mysqldump` and the `DB_*` values of the
environment file (`scripts/deploy/receive-release.sh:190-210`). The directory is root-only (700);
the newest 10 dumps are kept (`GOPHERCRM_KEEP_BACKUPS`). A failed dump aborts the deploy before
anything changes.

These dumps are taken only when a deploy runs, and they stay on the same server. Schedule your own
`mysqldump` for regular backups and copy the files to another machine. Restoring is manual, for
example `gunzip -c <file> | mysql -h 127.0.0.1 -u gophercrm -p gophercrm` with the service stopped.
A restored database needs the secrets it was written with ([FORKING.md](FORKING.md) section 4.2).
