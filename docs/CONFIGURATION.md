# Configuration reference

> **For:** Operators and developers configuring the GopherCRM backend and web UI. Part of the [documentation index](README.md).

This page is the one reference for the backend environment variables, the secrets and what
rotating them does, the public URLs, TLS, trusted proxies and CORS, and the build-time `VITE_*`
values of the web UI. The deployment guides keep their own commands and link here for what a
setting means: [SETUP.md](SETUP.md) (from source), [DOCKER.md](DOCKER.md) (Compose),
[SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) (systemd server) and [FORKING.md](FORKING.md)
(running it under your own name). `.env.example` is the commented reference for every variable.

## Where configuration comes from

Configuration comes from three places:

1. **Build-time Vite variables** (`gocrm-ui/.env`), inlined into the static UI by `npm run build`:
   `VITE_API_BASE_URL`, `VITE_APP_NAME`. Changing them needs a rebuild. See
   [Build-time web UI values](#build-time-web-ui-values).
2. **Environment variables** for the backend, read once at startup by `internal/config/config.go`
   (and loaded from `.env` by godotenv). `.env.example` documents each one.
3. **Persisted settings** in the `configurations` table, edited under Settings. The AEO provider
   keys, models and base URLs resolve settings first and fall back to the environment, re-read on
   each call (`internal/service/aeo_credentials.go`).

## First-run settings

These are the settings a first start needs, all read in `Load` (`internal/config/config.go`):

- `JWT_SECRET`: required, at least 32 characters. `Load` rejects an empty value, the literal
  `default-secret-change-this`, any value starting with `CHANGE_THIS` (the placeholder in
  `.env.example` is one) and values shorter than 32 characters, so a copied `.env.example` does
  not start. Generate one with `openssl rand -base64 32`. The error names the variable.
- `DB_DRIVER`: `mysql` (default) or `sqlite`. Any other value stops startup.
- MySQL or MariaDB: `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`. They are ignored when
  `DB_DRIVER=sqlite`. `scripts/create_database.sql` creates `gocrm`, the default `DB_NAME`.
- SQLite: `DB_PATH` (default `gophercrm.db`, relative to the working directory). It must not
  contain `?`, because the connector appends its own pragma query string, and it must live on a
  local filesystem (WAL). The operational notes are in [SETUP.md](SETUP.md#sqlite).
- `SERVER_PORT` (default 8080) and `SERVER_HOST` (empty = every interface; `127.0.0.1` behind a
  reverse proxy on the same machine; IPv6 literals are bracketed automatically).

## Backend variables

`Load` in the "Read in" column is `internal/config/config.go` `Load`, which reads every variable once at
startup.

| Variable | Default | Read in | Notes |
|---|---|---|---|
| `JWT_SECRET` | none (required) | `Load` | at least 32 characters; replace the example (see [First-run settings](#first-run-settings)) |
| `API_KEY_SECRET` | `JWT_SECRET` | `Load`, `formTokenSecret` (`internal/service/form_service.go`) | see [Secrets and rotation](#secrets-and-rotation) |
| `DB_DRIVER` | `mysql` | `Load` | `mysql` or `sqlite`; anything else stops startup |
| `DB_PATH` | `gophercrm.db` | `Load` | SQLite only; no `?`; local filesystem (WAL) |
| `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD` | `localhost`/`3306`/`gocrm`/`root`/empty | `Load` | MySQL only |
| `SERVER_HOST`, `SERVER_PORT` | every interface, `8080` | `Load` | `127.0.0.1` behind a proxy on the same host; IPv6 literals are bracketed automatically |
| `SERVER_MODE` | `development` | `Load` | `production` switches Gin to release mode (`setupRouter` (`cmd/main.go`)) and refuses to start with `DISABLE_RATE_LIMIT=true` (`CheckRateLimitSwitch` (`internal/middleware/rate_limit.go`)) |
| `API_PREFIX` | `/api/v1` | `Load` | where the API routes mount; `/health` and `/scim/v2` are outside it |
| `TRUSTED_PROXIES` | trust none | `Load` | comma-separated CIDRs; see [Trusted proxies and TLS](#trusted-proxies-and-tls) |
| `CORS_ALLOWED_ORIGINS` | none beyond the localhost list | `Load` | every browser origin of your UI; see [CORS](#cors) |
| `APP_BASE_URL` | `http://localhost:5173` | `Load` | the UI origin; password-reset links, the client response link of a quote email (`/quote-response?token=`) and the page the server's browser opens to print a quote to PDF (`/quote-render`), so with PDF output on it must be reachable from the backend host; also the single sign-on redirect URI `APP_BASE_URL/auth/callback` and post-logout redirect URI `APP_BASE_URL/login` |
| `QUOTE_PDF_CHROME_PATH` | empty (PDF output off); `/usr/local/bin/quote-pdf-browser` (a launcher of `chrome-headless-shell`) in the Docker image, empty again in the two development Compose files | `Load` | the browser binary that prints quotes to PDF, `chrome-headless-shell` or a full Chrome or Chromium; needs a font, a reachable `APP_BASE_URL`, and the `APP_BASE_URL` origin in `CORS_ALLOWED_ORIGINS` when the API is on another origin (`.env.example`, [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#quote-pdf-output)) |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | `Load` | this backend's public origin; form confirmation links, the SCIM base URL `PUBLIC_BASE_URL/scim/v2` (outside `API_PREFIX`; the proxy must forward `/scim/v2/`) and each SCIM user's `meta.location` |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM` | empty, 587, empty, empty, `no-reply@localhost` | `Load` | empty host means log-only mail |
| `RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY` | empty | `Load` | both or the layer is skipped |
| `JWT_EXPIRY_HOURS` | `24` | `Load` | access-token lifetime, used in `authService.GenerateJWT` (`internal/service/auth_tokens.go`); `JWT_ACCESS_TOKEN_MINUTES` is read but used by nothing |
| `REFRESH_TOKEN_EXPIRY_DAYS` | `30` | `Load` | refresh-token lifetime |
| `FORM_RETENTION_DAYS` | off | `Load` | age in days after which spam and never-confirmed submissions with no linked lead are permanently removed. Unset, empty, 0, negative or unparseable keeps it off; 1 and 2 become the 3-day floor; above 36500 becomes 36500. The first boot with it set removes the existing backlog; safe on every replica |
| `AUTH_AUDIT_RETENTION_DAYS` | `90` | `Load` | days a sign-in audit row (`auth_audit_log`, #454) is kept; unset, empty, zero, negative or unparseable keeps 90, 36500 ceiling. Pruned at boot and every 15 minutes by the stale bulk operation sweep; safe on every replica |
| `OIDC_ENABLED`, `OIDC_ALLOW_PASSWORD`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_SCOPES`, `OIDC_ROLE_MAPPING` | SSO off, password sign-in for linked accounts off, scopes `openid profile email`, the rest empty | `loadSSOConfig` (`internal/config/sso.go`) | fallbacks behind Settings → Single sign-on, which wins per field; register `APP_BASE_URL/auth/callback`, `APP_BASE_URL/login` and the front-channel logout URL `<API origin>/api/v1/auth/oidc/frontchannel-logout` with the identity provider ([single sign-on](user-guide/permissions-access/single-sign-on.md#values-to-exchange-with-the-identity-provider)) |
| `OIDC_GRAPH_MAX_PAGES` | `10` | `loadSSOConfig` (`internal/config/sso.go`) | environment only; Microsoft Graph pages read per Entra ID sign-in with group overage, 1 to 50 |
| `OIDC_ENTRA_APP_SIGNING_KEYS` | `false` | `loadSSOConfig` (`internal/config/sso.go`) | environment only; Entra ID app registrations with claims mapping |
| `SCIM_RATE_LIMIT_PER_MINUTE`, `SCIM_RATE_LIMIT_BURST` | `1500`, `150` | `loadSCIMConfig` (`internal/config/scim.go`) | environment only; requests per SCIM token (maximum 6000 and 1000) |
| `DISABLE_RATE_LIMIT` | unset | `RateLimitStrict`, `NewSCIMRateLimits` and `CheckRateLimitSwitch` (`internal/middleware/rate_limit.go`) | for tests only; see the [rate-limit switch](#the-rate-limit-switch) |
| `AUTOMATION_ENABLED` | `true` | `Load` | starts the follow-up automation worker and runs the boot-time stale sweep; true on any number of instances sharing one MySQL/MariaDB database (#237); one process on SQLite |
| `AUTOMATION_POLL_INTERVAL_SECONDS` | `30` | `Load` | how often the worker polls for events, due runs and schedule passes; also the granularity of every wait |
| `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR` | `true`, `6` | `Load` | daily AEO run |
| `AEO_EGRESS_ALLOWED_ORIGINS`, `AEO_EGRESS_LOCAL_ORIGINS` | empty | `Load` | environment only; see `docs/AEO_SECURITY.md` |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `json` | `Load` | `LOG_FORMAT` is `json` or `text`; SQL statements only at debug/trace |

`AUTOMATION_POLL_INTERVAL_SECONDS` (default 30, clamped to 1..3600) is clamped by
`clampAutomationPollInterval` (`internal/config/config.go`).

`AUTOMATION_ENABLED=false` keeps the API and the event capture working, but no run starts or
resumes and no stranded run is touched.

The AEO egress policy, the schedule and the custom engine (`AEO_CUSTOM_*`) can be set only in the
environment. `docs/AEO_SECURITY.md` explains why. So can `OIDC_GRAPH_MAX_PAGES`,
`OIDC_ENTRA_APP_SIGNING_KEYS`, `SCIM_RATE_LIMIT_*` and `AUTH_AUDIT_RETENTION_DAYS`; the other
`OIDC_*` variables are fallbacks behind Settings → Single sign-on.

### The rate-limit switch

`DISABLE_RATE_LIMIT=true` lifts the strict tier and the two SCIM limits (`SCIM_RATE_LIMIT_PER_MINUTE` and `SCIM_RATE_LIMIT_BURST`), and it is meant for tests only.
With `DISABLE_RATE_LIMIT=true` and `SERVER_MODE=production` the server refuses to start
(`CheckRateLimitSwitch` (`internal/middleware/rate_limit.go`)), whether the variable comes from the environment or from `.env`.

## Secrets and rotation

`API_KEY_SECRET` (or `JWT_SECRET` when it is unset) keys the HMAC of API keys
(`setupDependencies` (`cmd/main.go`), passed to `NewAPIKeyService`), refresh and password-reset tokens (passed to
the auth service in `setupDependencies`), form confirmation tokens and time-trap signatures
(`formTokenSecret` (`internal/service/form_service.go`)), and the encryption of sensitive settings
(the `configuration-secret` box built in `setupDependencies`, `internal/utils/secretbox.go`).
Rotating it invalidates every API key, refresh token, reset link and pending form confirmation, and leaves stored AEO provider keys unreadable (they are treated as unset).
The same secret, in `setupDependencies`, also keys:

- SCIM tokens (`NewSCIMTokenService`): after a rotation every SCIM token is refused; create
  new ones in Settings → SCIM tokens and give them to the identity provider;
- webhook signing secrets (`webhookBox`, sealed with `webhook.WebhookSecretContext`);
- the stored single sign-on client secret, client certificate and private key, and role
  mapping (the `configuration-secret` box): an unreadable value counts as not set, an
  unreadable client certificate makes the client secret apply, and an unreadable role mapping
  becomes empty;
- pending quote response links (`quoteTokenSecret`), which stop working.

Rotating `JWT_SECRET` invalidates every issued access token, but when `API_KEY_SECRET` is set
separately and left unchanged, refresh tokens stay valid (they are hashed with it,
`authService.sessionTokenSecret` (`internal/service/auth_tokens.go`)) and the UI silently trades them for new access
tokens signed with the new secret (the 401 handling in `setupInterceptors` and `refreshFor`, `gocrm-ui/src/api/client.ts`). To end every session,
rotate `API_KEY_SECRET` as well, with the consequences above. Per user, logout, a password
change, a password reset and a deactivation revoke that user's refresh tokens and end the access
tokens issued before them (they increment `users.token_version`, #236); a password change, a
password reset and a deactivation also revoke every API key of the user (#526), and a
reactivation brings none of them back.

Restoring a database backup ties these secrets to the backup: see
[BACKUP_RESTORE.md](BACKUP_RESTORE.md#secrets-and-restored-backups).

## Production choices

### SERVER_MODE

Without `SERVER_MODE=production` Gin runs in debug mode. With it the server refuses to start
while `DISABLE_RATE_LIMIT=true` is set (environment or `.env`), so the test-only switch cannot lift the strict `/auth` tier or the SCIM limits of a deployment.
The only cookie the application sets is the short-lived sign-in cookie of single sign-on
(`gophercrm_oidc_state`, #528), which carries no credential, and authentication is header-only.

### Trusted proxies and TLS

The backend serves plain HTTP. Authentication is header-based (Bearer JWT or `ApiKey`), but the
login request carries the password and every API call carries a token, so put a TLS-terminating
reverse proxy in front before anyone reaches it over a network, and keep the backend port off
the network (`SERVER_HOST=127.0.0.1` behind a proxy on the same host). The nginx and Apache sites
are in [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#reverse-proxy-and-tls); the Compose ports
are in [DOCKER.md](DOCKER.md#running-compose-beyond-localhost).

`TRUSTED_PROXIES` is a comma-separated list of CIDRs; empty means trust none. The rate limiter
keys on `c.ClientIP()` (`rateLimiter.middlewareWith` (`internal/middleware/rate_limit.go`)).
Behind a reverse proxy or CDN with no trusted proxies, every client shares the proxy's address
and therefore one rate-limit bucket: one noisy user locks out everyone on `/auth`. An IPv6 client
is counted per /64 network (`rateLimitClientKey`), so the addresses of one IPv6 /64 share a
bucket. SCIM requests without a valid token are limited per client address too.

### CORS

The backend accepts browser requests from `localhost`/`127.0.0.1` on 5173 and 3000, and
`localhost:8080` (`defaultAllowedOrigins` (`internal/middleware/cors.go`)). Any other
cross-origin UI needs `CORS_ALLOWED_ORIGINS` (comma-separated, `ServerConfig.CORSExtraOrigins`
(`internal/config/config.go`)). Missing origins fail silently in the browser: an unlisted origin
gets a bare 403, which the login page shows as a failed sign-in. The one exception: the pinned
`github.com/gin-contrib/cors` v1.7.5 (the `require` block of `go.mod`) skips the allowlist when
`Origin` is exactly `http://` or `https://` plus the request's `Host`, so a UI proxied to the API
under the same origin passes without being listed. The shipped nginx forwards `Host $host`
(`proxy_set_header Host $host` in `gocrm-ui/docker/nginx.conf`), which drops a non-standard port,
so the exception holds on ports 80 and 443 but not on `localhost:3000`. `curl` without an
`Origin` header does not reproduce any of this; add `-H 'Origin: https://your-ui.example'` when
you smoke-test.

### Public URLs

`PUBLIC_BASE_URL` is the API's public origin, used in form confirmation links and the SCIM base
URL `PUBLIC_BASE_URL/scim/v2`; `APP_BASE_URL` is the UI's, used in password-reset links, in the
client response link of a quote email and in the single sign-on redirect URI
`APP_BASE_URL/auth/callback` and post-logout redirect URI `APP_BASE_URL/login`. The front-channel
logout URL is the API origin followed by `/api/v1/auth/oidc/frontchannel-logout`. Left at their
defaults, both send recipients to `localhost`. With the UI and the API on one origin, both are
the public URL, and so is the one entry `CORS_ALLOWED_ORIGINS` needs.

### Mail (SMTP)

With `SMTP_HOST` empty every mail is logged instead of sent, and the sending code reports success
([KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md)); form mail and ticket comment mail
(`email_customer`, #219) are then recorded `log_only`. A form mail that fails is recorded per mail
and shown to staff (`GET /forms/deliveries`), and admin and sales retry it by hand; nothing
retries automatically.

## Build-time web UI values

`VITE_*` values are read at build time by Vite, from `gocrm-ui/.env` (see
`gocrm-ui/.env.example`), and inlined into the public JavaScript bundle: never put a secret in
one, and rebuild after a change.

- `VITE_API_BASE_URL` points the Axios client at the backend's API prefix. The UI calls
  `http://localhost:8080/api/v1` unless it says otherwise (`API_BASE_URL` in
  `gocrm-ui/src/api/client.ts`). Single sign-on needs the SPA and the API on the same site (the
  same registrable domain and scheme; different ports of `localhost` qualify), because the browser
  must keep and return the sign-in cookie of `POST /auth/oidc/start`.
- `VITE_APP_NAME` is the product name the pages show before sign-in; empty shows `GoCRM`.

The Docker UI image does not read `gocrm-ui/.env` (`gocrm-ui/.dockerignore` excludes it); pass
both as build arguments instead, as [DOCKER.md](DOCKER.md#configuration) shows.
