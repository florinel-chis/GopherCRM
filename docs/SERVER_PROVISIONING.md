# Server provisioning for the release-tarball deploy

This guide prepares a fresh Linux server for the deploy path described in
[FORKING.md](FORKING.md#52-release-tarball-and-systemd-through-the-shipped-workflows) section 5.2:
the Release workflow builds a tarball, the Deploy workflow sends it over SSH, and
`scripts/deploy/receive-release.sh` installs it. Those scripts install releases on a server that
is already provisioned; they do not provision it. Follow the steps below in order, once, before
the first deploy.

Every file shown here is an example with placeholders. Nothing in this guide ships as a file in
the repository, and nothing under `scripts/deploy` reads this page. When a step and a script
disagree, the script wins: re-check the functions and lines cited below against your checkout.

## Scope

- **Host.** A Linux server with systemd and nginx or Apache 2.4, reachable over SSH as root from the machine
  that runs `scripts/deploy/bootstrap.sh`.
- **Database.** MySQL 8 or MariaDB 10.11, on the same host and listening on `127.0.0.1:3306`.
  Installing and tuning the database server is outside this guide; it only creates the database
  and the account the backend uses.
- **SQLite is not supported on this path.** Before every deploy `receive-release` backs the
  database up with `mysqldump` (`backup_database` in `scripts/deploy/receive-release.sh`) and
  aborts the deploy when that backup fails, and `bootstrap.sh` refuses a server without
  `mysqldump` (its `for tool in flock rsync mysqldump gzip curl tar systemctl` check). This is a
  founder decision (13 Nov 2026, #305): receive-release keeps only its `mysqldump` backup, and
  there is no SQLite backup branch. For a SQLite deployment use the SQLite Compose stack described
  in [DOCKER.md](DOCKER.md).
- **Names and paths.** The guide uses the defaults of `receive-release.sh` (the block that starts
  with `ROOT=${GOPHERCRM_ROOT:-/srv/gophercrm}`). Change them only together with the matching
  `GOPHERCRM_*` overrides on the server.

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
(`for tool in flock rsync mysqldump gzip curl tar systemctl` in `scripts/deploy/bootstrap.sh`):
`flock`, `rsync`, `mysqldump`, `gzip`, `curl`, `tar` and `systemctl`. The proxy and the certificate need `nginx` and `certbot` as well
(or Apache 2.4 and certbot's Apache plugin, see [Reverse proxy and TLS](#reverse-proxy-and-tls)).

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

With Apache instead of nginx, install `apache2 python3-certbot-apache` in place of `nginx certbot`
(Debian or Ubuntu; `python3-certbot-apache` pulls in `certbot`), and check `apachectl` in place of
`nginx`:

```bash
apt-get install util-linux rsync default-mysql-client gzip curl tar systemd apache2 python3-certbot-apache
for tool in flock rsync mysqldump gzip curl tar systemctl apachectl certbot; do
  command -v "$tool" >/dev/null || echo "missing: $tool"
done
```

Optional, for quote PDF output (see [Quote PDF output](#quote-pdf-output)): a browser and at
least one font package. The default choice is `chrome-headless-shell`, the browser's headless
mode as its own binary, which is what the Docker image ships:
`apt-get install chromium-headless-shell fonts-liberation` on Debian 12 (binary
`/usr/bin/chromium-headless-shell`). Where the distribution has no such package, take the
`chrome-headless-shell` build of Chrome for Testing for your platform, or install a full
Chromium or Chrome instead (`apt-get install chromium fonts-liberation` on Debian; the browser
package is `chromium-browser`, a snap, on Ubuntu): a full browser is still accepted. Verify the
name and the path of the binary for your release. `bootstrap.sh` neither checks for nor
installs them.

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
(`require_private` in `scripts/deploy/receive-release.sh`); a loosened mode stops the deploy
instead of being silently reset. `bootstrap.sh` re-owns `/srv/gophercrm` but does not create it
(`chown root:root /srv/gophercrm`); it creates `bin`, `deploy`, `releases`, `backups` (700) and the web root itself. Mode
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
# Quote PDF output: chrome-headless-shell, or a full Chromium or Chrome; unset keeps it off.
# QUOTE_PDF_CHROME_PATH=/usr/bin/chromium-headless-shell
```

```bash
chown root:root /srv/gophercrm/env/gophercrm.env
chmod 600 /srv/gophercrm/env/gophercrm.env
```

- **The secrets.** `JWT_SECRET` must be at least 32 characters; any value starting with
  `CHANGE_THIS` is refused and the backend does not start (`Load` (`internal/config/config.go`)).
  Use `openssl rand -base64 32`. For `DB_PASSWORD` pick a value without quotes, backslash, `$` or
  backtick (for example `openssl rand -hex 24`); `DB_PASSWORD` itself is not checked at startup,
  so a forgotten placeholder only shows up as a database login with a known password. The same
  password goes into the SQL block under *MySQL database*.
- **One parser for two readers.** systemd reads the file through `EnvironmentFile=`, and
  `receive-release` reads `DB_USER`, `DB_PASSWORD`, `DB_HOST`, `DB_PORT` and `DB_NAME` from it with
  `sed` for the backup (`env_value` in `scripts/deploy/receive-release.sh`). That reader
  takes the last `KEY=` line, strips one pair of quotes and decodes no escapes, so keep each key on
  one active line and the values plain. A wrong `DB_*` value fails the backup, which stops the
  deploy before anything changes.
- **Why `EnvironmentFile=`.** The backend loads only a `.env` file in its working directory
  (`config.Load` (`internal/config/config.go`)); the unit's working directory holds none, so
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
- **Single sign-on and SCIM.** `APP_BASE_URL` also gives the single sign-on redirect URI
  `APP_BASE_URL/auth/callback` and the post-logout redirect URI `APP_BASE_URL/login`; register
  both with the identity provider. The front-channel logout URL is
  `https://crm.example.com/api/v1/auth/oidc/frontchannel-logout`, served through the `/api/v1/`
  location below. `PUBLIC_BASE_URL` also gives the SCIM base URL `PUBLIC_BASE_URL/scim/v2` and
  each user's `meta.location`. That base URL is outside `API_PREFIX` (the `/scim/v2` group in
  `cmd/main.go`, built with `cfg.Forms.PublicBaseURL` in `setupDependencies`), so the proxy needs
  its own rule: the nginx `location /scim/v2/` and the Apache `ProxyPass /scim/v2/` lines of the
  sites below. An install that uses them may add these environment-only settings to this file:
  `OIDC_GRAPH_MAX_PAGES`, `OIDC_ENTRA_APP_SIGNING_KEYS`, `SCIM_RATE_LIMIT_PER_MINUTE`,
  `SCIM_RATE_LIMIT_BURST` and `AUTH_AUDIT_RETENTION_DAYS` (defaults and limits in
  `.env.example`). The seven `OIDC_*` fallbacks are better set in Settings → Single sign-on.
  The values to exchange are listed in the user guide:
  [single sign-on](user-guide/permissions-access/single-sign-on.md#values-to-exchange-with-the-identity-provider)
  and [SCIM base URL](user-guide/permissions-access/scim-provisioning.md#base-url).

Every key in the block is documented in `.env.example`.

### Quote PDF output

Optional. With `QUOTE_PDF_CHROME_PATH` unset, or naming a file that is not executable, the backend
runs with PDF output off: quote emails go out as text only, `GET /api/v1/quotes/:id/pdf` answers
503 `PDF_UNAVAILABLE` and the web application shows no Download PDF button. The backend still
starts; a path that does not resolve is one warning in the journal.

To switch it on:

1. Install `chrome-headless-shell` (or a full Chromium or Chrome) and at least one font package
   (see *Packages*). Without a font the PDF is a valid file with no readable text.
2. Uncomment `QUOTE_PDF_CHROME_PATH` in the environment file and set it to the binary
   (`/usr/bin/chromium-headless-shell` for the Debian package; the path of a full Chrome or
   Chromium works the same way), then restart the service.
3. Check the two conditions of a render. The backend starts the browser headless, the browser
   opens `APP_BASE_URL/quote-render`, and that page loads the quote from the API with a
   short-lived, single-quote, read-only credential:
   - `APP_BASE_URL` must be reachable **from the server itself** (the public host name must
     resolve and connect from the host, through the same nginx);
   - when the API is on another origin than `APP_BASE_URL`, that origin must be listed in
     `CORS_ALLOWED_ORIGINS`. With the UI and the API on one origin, as in this guide, nothing is
     needed. A missing origin shows as a bare 403 on `POST /api/v1/quotes/public/render` that the
     backend does not log, and every render then fails: the download answers 503
     `PDF_RENDER_FAILED` and the email falls back to text.

**From source, in development.** The same variable switches it on for `go run ./cmd` or a local
binary. A full Chrome is accepted, for example
`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` on macOS. `APP_BASE_URL` defaults to
the Vite dev server, `http://localhost:5173`, so the frontend must be running there. The UI on 5173
and the API on 8080 are two origins: the localhost origins 5173, 3000 and 8080 are admitted by
default, and any other origin must be listed in `CORS_ALLOWED_ORIGINS`.

**The unit needs no change.** The renderer writes only below one temporary directory per render
(the browser profile, `HOME`, `TMPDIR` and the XDG directories), which
`PrivateTmp=yes` provides, and removes it afterwards; `ProtectHome=yes` and `ProtectSystem=strict`
stay as they are. The browser process does not inherit the service's environment: it gets `PATH`,
the locale, `TZ` and those directories, so no value of the environment file reaches it. The
backend never passes `--no-sandbox`; if your browser cannot start its sandbox as the service
user, point `QUOTE_PDF_CHROME_PATH` at a small wrapper script that adds the flag and starts the
browser with `exec` (as the Docker image's `/usr/local/bin/quote-pdf-browser` does), and treat
that as your own decision. The backend drives the browser over its DevTools pipe, so a wrapper
must not close or redirect file descriptors 3 and 4: the browser it starts has to inherit them
(a script that only adds flags does). The browser's helper processes that lose their parent when a render ends
are reaped by the host's init, as for any service.

**Not verified on a systemd host.** For this feature the systemd deployment was not run on a real
host. What was verified is the nearest equivalent: the Docker image running as an unprivileged
user with a read-only root file system and only `/tmp` and the data directory writable. Check a
download after switching it on.

**Time budget.** One render runs at a time per backend process and is bounded at 15 s, the wait
for its turn included. `POST /api/v1/quotes/:id/email` therefore takes at most 15 s for the render
plus 30 s for the SMTP delivery, 45 s; the web application waits 55 s for that call and for the
download. Keep the reverse proxy's read timeout at 60 s or more: both sites below set 60 s
(`proxy_read_timeout 60s` in the nginx TLS site, `ProxyTimeout 60` in the Apache HTTPS site). A shorter timeout makes the proxy answer 504
for a mail the backend still delivers.

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
creates (`link_binaries` in `scripts/deploy/receive-release.sh`), and that deploy restarts the
service itself (`restart_and_check`). Notes on the directives:

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

The web server serves the UI from the web root, proxies the API, the SCIM endpoint and `/health` to the
backend on `127.0.0.1:8080`, and terminates TLS. This section covers nginx first; the same setup for
Apache 2.4 follows under [Apache 2.4](#apache-24). With nginx the certificate comes from Let's Encrypt
through certbot's webroot mode, which needs the plain-HTTP site first.

Create the ACME directory. It sits outside the web root on purpose: every deploy syncs
`/var/www/gophercrm` with `rsync --delete` (`activate` in `scripts/deploy/receive-release.sh`), which would
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

    client_max_body_size 1m;
    proxy_read_timeout 60s;

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

    location /scim/v2/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # The quote email links here with the client's response token in the query string;
    # keep that request out of the access log.
    location = /quote-response {
        access_log off;
        add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'" always;
        add_header X-Frame-Options "DENY" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Referrer-Policy "same-origin" always;
        try_files /index.html =404;
    }

    location / {
        add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'" always;
        add_header X-Frame-Options "DENY" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header Referrer-Policy "same-origin" always;
        try_files $uri $uri/ /index.html;
    }
}
```

Enable it and run `nginx -t && systemctl reload nginx` again.

- `proxy_pass` carries no URI part, so the request path reaches the backend unchanged
  (`/api/v1/...` matches `API_PREFIX`, `/scim/v2/...` the SCIM routes).
- `/scim/v2/` must reach the backend, not the web app: identity providers provision users at
  `PUBLIC_BASE_URL/scim/v2`. Without the location the request falls through to `location /`
  and gets the UI's `index.html` with 200.
- `/health` must be proxied: after `receive-release` has seen the new revision on
  `http://127.0.0.1:8080/health`, the Deploy workflow checks `<public URL>/health` as visitors
  reach it (the `Verify from outside` step of `.github/workflows/deploy.yml`).
- `Host $host` keeps the UI and the API on one origin for the CORS check, and `X-Forwarded-For
  $remote_addr` is the single address the backend trusts through `TRUSTED_PROXIES=127.0.0.1`.
- `try_files ... /index.html` serves the single-page UI for client-side routes.
- The link of a quote email is `APP_BASE_URL/quote-response?token=...`. The backend's own access
  log records paths without the query string, but nginx's default access log records the whole
  request line, so do not log the query string of `/quote-response`: the
  `location = /quote-response` block above turns its access log off (a custom `log_format` that
  uses `$uri` instead of `$request` works too). The page drops the token from the address bar
  after reading it and sends it to the API in a request header, so `/api/v1/` requests carry no
  token in their URL.
- The four `add_header ... always;` lines (a content policy, `X-Frame-Options`,
  `X-Content-Type-Options` and `Referrer-Policy`) are the same set as the Docker image's
  `gocrm-ui/docker/nginx.conf`, and `always` puts them on error answers too. nginx drops inherited
  `add_header` lines in a location that sets its own, so they are repeated in each web app
  location instead of the server level. `location /api/v1/`, `location = /health` and
  `location /scim/v2/` carry none: proxied answers keep the backend's own headers. A UI built with `VITE_API_BASE_URL` on another
  origin must add that origin to `connect-src`.
- `client_max_body_size 1m` and `proxy_read_timeout 60s` are nginx's own defaults, written out so
  the Apache site below can match them, so the nginx site behaves as before. No route takes a file
  upload, and the backend's own body caps (public form submissions, SCIM) and its text-field caps
  are 64 KiB, well under 1 MiB; 60 s is above the 45 s a quote email may take (see
  [Quote PDF output](#quote-pdf-output)). They sit at server level, so every proxied location
  gets the same values.
- The web root stays empty until the first deploy, so the site answers 404 until then.

### Apache 2.4

On a server that runs Apache 2.4 instead of nginx, the setup below replaces the two nginx sites:
it proxies the same paths, sends the backend the same client address, puts the same browser
headers on the web app only and applies the same timeout and, to every request that declares its
length, the same body limit. Paths follow Debian and Ubuntu (`/etc/apache2`, `a2enmod`,
`a2ensite`); adapt them for other distributions.

Enable the modules the sites use:

```bash
a2enmod proxy proxy_http rewrite headers ssl setenvif
```

HTTP site, for example `/etc/apache2/sites-available/gophercrm-http.conf`:

```apache
<VirtualHost *:80>
    ServerName crm.example.com
    Redirect permanent / https://crm.example.com/
</VirtualHost>
```

Enable it, check and reload Apache, and request the certificate:

```bash
a2ensite gophercrm-http
apachectl configtest && systemctl reload apache2
certbot certonly --apache -d crm.example.com --deploy-hook 'systemctl reload apache2'
```

`certonly` only obtains the certificate: certbot answers the challenge through a temporary
configuration of its own and does not rewrite the site files, so the sites stay as written here.

HTTPS site, for example `/etc/apache2/sites-available/gophercrm-https.conf`:

```apache
<VirtualHost *:443>
    ServerName crm.example.com

    SSLEngine on
    SSLCertificateFile /etc/letsencrypt/live/crm.example.com/fullchain.pem
    SSLCertificateKeyFile /etc/letsencrypt/live/crm.example.com/privkey.pem

    DocumentRoot /var/www/gophercrm

    # The quote email links here with the client's response token in the query string;
    # keep that request out of the access log.
    SetEnvIf Request_URI "^/quote-response$" gophercrm_nolog
    CustomLog ${APACHE_LOG_DIR}/gophercrm-access.log combined env=!gophercrm_nolog
    ErrorLog ${APACHE_LOG_DIR}/gophercrm-error.log

    LimitRequestBody 1048576
    ProxyTimeout 60

    # LimitRequestBody does not reach proxied requests; refuse a declared body over 1 MiB
    # before it is proxied.
    RewriteEngine On
    RewriteCond expr "%{HTTP:Content-Length} -gt 1048576"
    RewriteRule ^ - [R=413,L]

    ProxyPreserveHost On
    ProxyAddHeaders Off
    RequestHeader set X-Real-IP "expr=%{REMOTE_ADDR}"
    RequestHeader set X-Forwarded-For "expr=%{REMOTE_ADDR}"
    RequestHeader set X-Forwarded-Proto "https"

    ProxyPass /api/v1/ http://127.0.0.1:8080/api/v1/
    ProxyPassReverse /api/v1/ http://127.0.0.1:8080/api/v1/
    ProxyPass /health http://127.0.0.1:8080/health
    ProxyPassReverse /health http://127.0.0.1:8080/health
    ProxyPass /scim/v2/ http://127.0.0.1:8080/scim/v2/
    ProxyPassReverse /scim/v2/ http://127.0.0.1:8080/scim/v2/

    <Directory /var/www/gophercrm>
        Options FollowSymLinks
        AllowOverride None
        Require all granted

        RewriteEngine On
        RewriteCond %{REQUEST_FILENAME} !-f
        RewriteRule ^ index.html [L]

        Header always set Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
        Header always set X-Frame-Options "DENY"
        Header always set X-Content-Type-Options "nosniff"
        Header always set Referrer-Policy "same-origin"
    </Directory>
</VirtualHost>
```

Enable it with `a2ensite gophercrm-https`, then run `apachectl configtest && systemctl reload apache2`
again.

- `ProxyPass` and `ProxyPassReverse` cover exactly the paths the nginx TLS site proxies:
  `/api/v1/`, `/health` and `/scim/v2/`. The target repeats the path, so the request path reaches
  the backend unchanged, as with nginx's `proxy_pass` without a URI part.
- `ProxyPass /health` is a prefix, while nginx's `location = /health` matches the path exactly.
  The difference is harmless: any other path that starts with `/health` reaches the backend and
  gets its 404.
- `/quote-response` is not proxied, as in the nginx site: the backend has no such route, and the
  page calls `/api/v1/quotes/public/response` itself. The request falls through to the SPA rewrite
  and gets the browser headers. The `SetEnvIf` and `CustomLog ... env=!gophercrm_nolog` pair keeps
  it out of the access log, the Apache form of nginx's `access_log off`, because the default
  `combined` format records the query string that carries the token.
- `ProxyPreserveHost On` is nginx's `Host $host`. `ProxyAddHeaders Off` stops mod_proxy from
  appending its own `X-Forwarded-*` headers, and the three `RequestHeader set` lines replace
  whatever the client sent: `X-Real-IP` and `X-Forwarded-For` carry the connecting address
  (`REMOTE_ADDR`, nginx's `$remote_addr`) and `X-Forwarded-Proto` is `https`. The backend reads
  `X-Forwarded-For`, then `X-Real-IP`, from the trusted proxy `TRUSTED_PROXIES=127.0.0.1`, so both
  must be overwritten on every proxied request, or a client could choose its own rate-limit and
  lockout key.
- `RewriteCond %{REQUEST_FILENAME} !-f` with `RewriteRule ^ index.html [L]` is nginx's
  `try_files $uri $uri/ /index.html`: every path that is not a file of the built UI gets
  `index.html`. mod_rewrite in a `<Directory>` needs `Options FollowSymLinks`.
- The four `Header always set` lines are the browser headers of the nginx web app locations, and
  `always` puts them on error answers too. They sit inside `<Directory /var/www/gophercrm>`, which
  applies only to requests that map to the web root. Proxied requests never map to it, so
  `/api/v1/`, `/health` and `/scim/v2/` keep the backend's own headers: this is the Apache form of
  "not on proxied API responses". Put no `Header` line at the `<VirtualHost>` level. A UI built with
  `VITE_API_BASE_URL` on another origin must add that origin to `connect-src`.
- The body limit is nginx's `client_max_body_size 1m` in two parts. `LimitRequestBody 1048576`
  applies only to requests Apache answers itself (the web root); mod_proxy_http forwards a
  proxied body whatever its size. The vhost-level `RewriteCond expr "%{HTTP:Content-Length} -gt 1048576"`
  with `RewriteRule ^ - [R=413,L]` therefore answers 413 to any request, proxied or not, whose
  `Content-Length` exceeds 1 MiB, as nginx does; a body of exactly 1 MiB passes. A chunked body
  (`Transfer-Encoding: chunked`, no `Content-Length`) is not checked by either line: Apache
  forwards it, and only the backend's own caps (64 KiB on public form submissions and SCIM)
  bound it. The web app's own requests carry a `Content-Length`.
- `ProxyTimeout 60` equals the nginx TLS site's `proxy_read_timeout 60s`, so the quote email's
  45 s budget fits under the 60 s timeout on either server.

When Cloudflare (or another CDN) fronts the server, `REMOTE_ADDR` is the CDN's address and every
visitor shares one rate-limit key. Add these lines to the HTTPS site, after its two `REMOTE_ADDR`
lines, which stay in place:

```apache
RequestHeader set X-Real-IP "expr=%{REMOTE_ADDR}"
RequestHeader set X-Forwarded-For "expr=%{REMOTE_ADDR}"
SetEnvIfNoCase CF-Connecting-IP "^(.+)$" CF_CLIENT_IP=$1
RequestHeader set X-Forwarded-For "%{CF_CLIENT_IP}e" env=CF_CLIENT_IP
RequestHeader set X-Real-IP "%{CF_CLIENT_IP}e" env=CF_CLIENT_IP
```

- The first two lines are the site's own `REMOTE_ADDR` lines, repeated to show where the others
  go: add the Cloudflare lines after them and keep them.
- mod_headers applies the `RequestHeader` lines in order, so the conditional `set` lines override
  the two headers only when the request carries `CF-Connecting-IP`.
- `REMOTE_ADDR` stays the fallback. Without it, a request lacking `CF-Connecting-IP` would pass the
  client's own `X-Forwarded-For` and `X-Real-IP` to the backend, because `ProxyAddHeaders` is Off.
- `CF-Connecting-IP` itself can be forged by any client that reaches the origin directly. The
  variant is safe only when a firewall limits the origin's port 443 to Cloudflare's address
  ranges; otherwise a client picks its own rate-limit and lockout key.
- `TRUSTED_PROXIES` stays `127.0.0.1`: the backend still talks only to Apache.

### Updating an existing server

A server provisioned before the SCIM location was added answers `/scim/v2/` with the web app.
On nginx, add the `location /scim/v2/` block above to the TLS site file, next to
`location /api/v1/`, then check and reload:

```bash
nginx -t && systemctl reload nginx
```

On Apache, add the `ProxyPass` and `ProxyPassReverse` lines for `/scim/v2/` from the
[Apache HTTPS site](#apache-24) above to the site, then check and reload:

```bash
apachectl configtest && systemctl reload apache2
```

Then check from outside. Without a SCIM token the backend refuses the request, so
`/scim/v2/ServiceProviderConfig` should print `401 application/scim+json`:

```bash
curl -s -o /dev/null -w '%{http_code} %{content_type}\n' https://crm.example.com/scim/v2/ServiceProviderConfig
```

`200 text/html` means the request still reaches the web app.

On either server, check `/health` as visitors reach it: its JSON carries `"revision":"<sha>"`,
the field the `Verify from outside` step of `.github/workflows/deploy.yml` looks for:

```bash
curl -fsS https://crm.example.com/health
```

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

The backend runs `models.MigrateDatabase` (GORM auto-migration) at every start (`main` (`cmd/main.go`)),
so the first start creates the tables and later starts add new columns and tables.

The release tarball carries the SQL half as well: `bin/migrate` and the `migrations/` directory
(`for target in gophercrm create-admin migrate` and `cp -R migrations release/migrations` in
`scripts/release/build.sh`), found on the server at `/srv/gophercrm/current/migrations` with
the tool at `/srv/gophercrm/current/bin/migrate`. receive-release does not run them: no deploy
step calls `bin/migrate` or reads `current/migrations`. On MySQL and MariaDB the SQL migrations are
applied with golang-migrate as described in
[FORKING.md, section 7.4](FORKING.md#74-migration-versions), including the rules on version
order and the populated-database rehearsal. `bin/migrate` reads the same settings as the backend
through `config.Load`, so it needs this environment file. This path does not automate the step:
run it yourself after a deploy that brings new files under `migrations/`, and keep the pre-deploy
backup until it has succeeded.

The step stays manual by founder decision (13 Nov 2026, #305). Run it as root on the server, and
only once `receive-release deploy` has reported `revision <sha> is live` (in the Deploy workflow
log or in `/srv/gophercrm/deploy/deploy.log`):

```bash
systemd-run --pty --uid=gophercrm --gid=gophercrm -p EnvironmentFile=/srv/gophercrm/env/gophercrm.env /srv/gophercrm/current/bin/migrate -command up -dir /srv/gophercrm/current/migrations
```

- **Order.** The command runs after the deploy, never before it: the new SQL files reach the
  server only when `receive-release` unpacks the revision into `releases/<sha>` and points
  `current` at it. By then the restart has already run `MigrateDatabase`, and the SQL files are
  written for that order (`CREATE TABLE IF NOT EXISTS`, see FORKING §7.4).
- **Not after a rollback.** When the deploy reports `deploy of <sha> rolled back to <previous>`,
  skip the step: `current` points at the previous release again, not at the revision you meant to
  migrate.
- **Absolute paths.** A `systemd-run` service starts in `/`, so the default `-dir migrations`
  would resolve to `/migrations`; pass the absolute `-dir`. The tool is reached through
  `current/bin/migrate`, because `receive-release` links only `gophercrm` and `create-admin` into
  `/srv/gophercrm/bin`.
- **The whole environment file.** `bin/migrate` loads its settings through `config.Load`, which
  refuses to start without a valid `JWT_SECRET`, so it needs the same `EnvironmentFile=` as the
  service, not only the `DB_*` keys.
- **A failed migration leaves the schema dirty.** golang-migrate then records the failed version
  as dirty and refuses every further `up` until it is resolved. Keep the pre-deploy backup under
  `/srv/gophercrm/backups` and inspect the schema. Either restore the backup (see *Backups*), or
  revert the partial changes of the failed migration by hand and run the same `systemd-run` line
  with `-command force -version <V>` in place of `-command up`, where `<V>` is the last version
  that applied cleanly (the one before the dirty version). Force the failed version itself only if
  you completed its statements by hand; forcing it otherwise skips it for good. Then rerun the
  command with `-command up`.

## First admin

Public registration only ever creates customer accounts, so the first admin is created on the
server with `bin/create-admin`, as the service user and with the service environment:

```bash
systemd-run --pty --uid=gophercrm --gid=gophercrm -p EnvironmentFile=/srv/gophercrm/env/gophercrm.env /srv/gophercrm/bin/create-admin
```

It prompts for the email, the name and the password, and reads the password from the terminal
without echo (`term.ReadPassword` in `run` (`cmd/create-admin/main.go`)), which is why `--pty` is
required. Type the password at the prompt. Do not use create-admin's flag mode
(the `-non-interactive` and `-password` flags of `run` (`cmd/create-admin/main.go`)): it takes the password on the command line, where the process
list and the shell history can see it. The link `/srv/gophercrm/bin/create-admin` exists once the
first deploy has run.

## Backups

Before every deploy `receive-release` writes
`/srv/gophercrm/backups/<UTC timestamp>-<sha>.sql.gz` with `mysqldump` and the `DB_*` values of the
environment file (`backup_database` in `scripts/deploy/receive-release.sh`). The directory is root-only (700);
the newest 10 dumps are kept (`GOPHERCRM_KEEP_BACKUPS`). A failed dump aborts the deploy before
anything changes.

These dumps are taken only when a deploy runs, and they stay on the same server. Schedule your own
`mysqldump` for regular backups and copy the files to another machine. Restoring is manual, for
example `gunzip -c <file> | mysql -h 127.0.0.1 -u gophercrm -p gophercrm` with the service stopped.
A restored database needs the secrets it was written with ([FORKING.md](FORKING.md) section 4.2).
