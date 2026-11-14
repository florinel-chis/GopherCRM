# Forking GopherCRM

This guide is for someone who wants to run GopherCRM under their own name, change it, and keep
taking upstream fixes. It covers what the code base gives you, the first run, rebranding,
configuration, deployment, CI on a fork and staying in sync with upstream.

Two companion documents cover the rest:

- [USE_CASES.md](USE_CASES.md) — worked recipes for concrete deployments, with the fit and the gaps
  of each.
- [EXTENDING.md](EXTENDING.md) — how to add an entity, a route, a migration or a settings key, and
  how to remove a module, without breaking the conventions the tests enforce.

Every count in this guide names the commit it was measured on and the command that produced it.
Every factual claim names the file it comes from. Re-check both before relying on them: the code
moves faster than this page.

## 1. What you get, and what you do not

### 1.1 Modules

Roles are `admin`, `sales`, `support` and `customer` (`internal/models/user.go`). Routing lives in
`internal/handler/routes.go`, `aeo_routes.go`, `form_routes.go` and `form_public_routes.go`; the
wiring is in `setupDependencies` (`cmd/main.go:257`).

| Module | Who may use it | What is guaranteed | Test evidence |
|---|---|---|---|
| Auth, sessions, API keys | public `/auth`; own keys for any authenticated user | rotating refresh tokens, single-use reset tokens claimed inside one transaction, lockout after 5 failures, one generic 401 when login or `POST /auth/refresh` rejects the credential (an operational refresh failure is a retryable 503 `SESSION_REFRESH_UNAVAILABLE` and a malformed body a 400, `internal/handler/auth_handler.go:211-238`; change-password answers 400 for a wrong current password and 409 `SESSION_STALE` for a stale session, `:340-346`) | `internal/service/auth_password_reset_regression_test.go`, `auth_refresh_rotation_regression_test.go`, `internal/handler/apikey_handler_test.go` |
| Registration | public, ships **off** | always creates a `customer`; fails closed on a settings read error | `internal/handler/auth_registration_test.go`, `internal/middleware/registration_test.go` |
| Users | admin; self for get/update | deactivated users' bearer tokens are rejected; delete is erasure | `internal/handler/user_handler_test.go`, `internal/service/user_service_test.go` |
| Leads | admin, sales (own leads only) | email or phone required; conversion rules from settings; one visibility policy; CSV export of the leads the caller may list in one totally ordered statement | `internal/service/lead_conversion_rules_test.go`, `lead_visibility_test.go`, `internal/integration/lead_visibility_integration_test.go`, `internal/repository/lead_export_traversal_test.go` |
| Customers | create admin+sales; export and delete admin | unique email (409); CSV export in one totally ordered statement | `internal/service/customer_service_test.go`, `internal/repository/customer_export_traversal_test.go` |
| Companies | read admin+sales+support; write admin+sales | normalised unique domain; delete clears links in one transaction | `internal/service/company_service_test.go`, `internal/repository/company_domain_normalization_test.go` |
| Deals | admin, sales (own) | five stages; one history row per stage change in the same transaction; integer money | `internal/service/deal_service_test.go`, `internal/repository/deal_repository_test.go` |
| Products and quotes | products read admin+sales, write admin; quotes admin+sales (own) | every status transition is a conditional update plus a history row | `internal/service/quote_status_regression_test.go` |
| Tickets and comments | create support+admin; sales read-only; support own assignments; customer own, non-internal | visibility applied in the query before pagination | `internal/handler/ticket_handler_test.go`, `internal/integration/ticket_visibility_integration_test.go` |
| Tasks and labels | tasks in-handler; labels write admin+sales+support | hidden lead links redacted on every read | `internal/handler/task_labels_handler_test.go`, `task_lead_visibility_handler_test.go` |
| Forms | admin `/forms` read admin+sales+support; public `/forms/public/*` | spam layers in fixed order; double opt-in claim, lead write and submission update in one transaction | `internal/service/form_service_test.go`, `form_confirmation_regression_test.go`, `internal/handler/form_public_handler_test.go` |
| Follow-up automation | rules admin; runs admin+sales (sales never sees ticket runs) | outbox event per lead/deal/ticket write (#242); conditional run advance after a locking read of the record; ticket delete cancels the ticket's open runs; boot reconciliation | `internal/service/automation_runtime_test.go`, `cmd/automation_runtime_test.go`, `internal/service/ticket_automation_*_test.go` |
| AEO (answer-engine tracking) | admin+sales+support; run and generate admin+sales | keys encrypted at rest; environment-only egress allowlist | `internal/service/aeo_service_test.go`, `aeo_egress_boundary_test.go`, `docs/AEO_SECURITY.md` |
| GDPR erasure | deleting a user, customer or lead: admin, plus the owning sales user for an unconverted lead (`internal/handler/lead_handler.go:738-746`) | personal fields of that identity overwritten, then soft-deleted, in one transaction, together with the unlinked form submissions whose email matches it and their tokens and delivery rows (#214), and the staff-typed text of the deals and quotes linked to the erased customers and leads (title `[redacted]`, notes, lost reason and line descriptions reset) with the notifications about those deals (#245). Ticket and task deletes are soft deletes that scrub no text (`internal/repository/ticket_repository.go`, `task_repository.go:53`; a ticket delete also purges its mail deliveries and notifications and cancels its automation runs, #219/#242), so text that names a person stays; for tickets, DEVELOPMENT.md documents an operator redaction procedure to run before the erasure | `internal/repository/identity_erasure_test.go`, `unlinked_submission_erasure_test.go`, `automation_erasure_test.go`, `business_text_erasure_test.go`, `tests/ticket_redaction_procedure_test.go`, `tests/business_text_erasure_test.go` |
| Dashboard | admin+sales+support | activity lists scoped inside the query | `internal/repository/dashboard_queries_test.go` |
| Configuration | `/configurations/ui` any user; the rest admin | seeding never overwrites a stored value; sensitive values encrypted and masked | `internal/repository/configuration_repository_test.go`, `internal/service/configuration_service_test.go` |

`docs/FEATURES.md` is the long inventory, with every behaviour mapped to its tests.

### 1.2 Not present

Checked by searching the tracked tree on `4dfe404`:

- No outbound webhooks (`git ls-files internal cmd | xargs grep -liE webhook` finds nothing;
  #232).
- No MCP server.
- API keys have no scopes: a key acts with its owner's full role (`internal/models/apikey.go`
  holds only name, hash, prefix, owner, expiry and an active flag). Least-privilege credentials
  are #211.
- No ticket channel and no inbound mail (`internal/models/ticket.go`). Every ticket is created
  through the API or the UI. Outbound ticket replies by email shipped in #219: a staff comment sent
  with `email_customer: true` is mailed once to the ticket's customer and its outcome recorded in
  `ticket_mail_deliveries` (backend only; no retry). A ticket channel field and inbound mail remain
  later work.
- No internationalisation framework in `gocrm-ui/package.json`.
- No quote PDF and no tax lines (`docs/FEATURES.md`, rows 4d.3 and 4d.7; #233).
- CSV export covers customers and leads only (`internal/handler/customer_handler.go:582`,
  `lead_handler.go:915`, #234); other list views and PDF are `docs/ROADMAP.md` "Data export".
- The CSRF middleware is not installed (`internal/middleware/csrf.go`; README "Known Limitations";
  #235).
- An issued JWT ends early only through logout, a password change or a password reset, which
  increment `users.token_version` (#236). A role change does not end it, and the token a refresh
  rotation replaces stays valid until it expires (README "Known Limitations").
- The automation worker assumes a single API process (README "Known Limitations"; section 5.4;
  #237). AEO runs and the daily schedule are coordinated across replicas on MySQL/MariaDB (#229,
  section 5.4).

### 1.3 Open issues you inherit

Open on 2026-10-31, 56 issues
(`gh issue list --repo florinel-chis/GopherCRM --state open --limit 200 --json number --jq length`;
without `--limit`, `gh` lists at most 30):

- #94, the tracker of the architecture audit, was closed on 2026-10-31; all 27 audit tickets
  (#95–#121) are closed too.
- #181–#187 — review follow-ups, among them #183 (a shared concurrency protocol for erasure,
  conversion and authentication), #184 (cross-tab token refresh) and #185
  (`POST /leads/:id/assign`, routed now; no screen offers it yet).
- #196 — bulk status operations whose outcome write failed stay stuck.
- #197–#209 — improvements to the remediation runner (development tooling in
  `automation/langflow/`). They do not affect the application.
- #210–#224 and #226–#245 — gaps found while preparing this guide, cited where they apply below.

Run `gh issue list --repo florinel-chis/GopherCRM --state open --limit 200` for the current list.

### 1.4 Released and unreleased code

The latest release is 1.2.0 (`CHANGELOG.md:1040`). Everything under `[Unreleased]`
(`CHANGELOG.md:8`, about 730 lines) is on `main` but in no tagged release: companies, deals,
products and quotes, ticket comments, follow-up automation, the transactional token flows and more.
A fork taken from `main` gets all of it. A fork taken from the `v1.2.0` tag gets none of it.

## 2. First run

### 2.1 SQLite, from source

No database server is needed. Requires Go 1.25 (`go.mod:3`), Node 20 or later, and `jq` for the
API example in section 2.4.

```bash
cp .env.example .env
# Edit .env:
#   JWT_SECRET=<output of: openssl rand -base64 32>
#   DB_DRIVER=sqlite
go run ./cmd                     # or: make run
```

The backend listens on `http://localhost:8080` and creates `gophercrm.db` in the working
directory (`DB_PATH`, `internal/config/config.go:285`). The schema is created by auto-migration at
startup. `cmd/main.go` takes no command-line flags: `go run ./cmd --help` starts the server.

In a second terminal, create the first admin. Self-service registration only ever creates
`customer` accounts:

```bash
make build-tools                 # builds bin/create-admin
make create-admin                # interactive
# or scripted:
bin/create-admin -non-interactive -email admin@example.com -name "Admin" -password 'ChangeMe!2345'
```

Then the UI:

```bash
cd gocrm-ui
npm ci
npm run dev                      # Vite on http://localhost:5173
```

The UI calls `http://localhost:8080/api/v1` unless `VITE_API_BASE_URL` says otherwise
(`gocrm-ui/src/api/client.ts:26`, `gocrm-ui/.env.example:2`).

### 2.2 MySQL or MariaDB

```bash
make create-db                   # mysql -u root < scripts/create_database.sql
```

`scripts/create_database.sql` creates `gocrm`, the default `DB_NAME` (`internal/config/config.go`,
`.env.example`). A fork that renames the database changes both together; a hygiene test fails when
they differ. Then set `DB_DRIVER=mysql` and the `DB_*` credentials and run `go run ./cmd`.

The Compose stack in `docker-compose.yml` brings up MySQL 8, the API and an nginx-served UI. See
[DOCKER.md](DOCKER.md) and section 5.1 below before using it beyond a laptop.

### 2.3 Three traps on the first run

1. **The example JWT secret stops the app.** `internal/config/config.go:270-280` rejects an empty
   value, the literal `default-secret-change-this`, any value starting with `CHANGE_THIS` (the
   placeholder in `.env.example` is one) and values shorter than 32 characters, so a copied
   `.env.example` does not start. Replace the secret with the output of
   `openssl rand -base64 32`.
2. **CORS.** The backend accepts browser requests from `localhost`/`127.0.0.1` on 5173 and 3000,
   and `localhost:8080` (`internal/middleware/cors.go:14-20`). Any other cross-origin UI needs
   `CORS_ALLOWED_ORIGINS`. An unlisted origin gets a bare 403, which the login page shows as a
   failed sign-in. The one exception: the pinned `github.com/gin-contrib/cors` v1.7.5 (`go.mod:7`)
   skips the allowlist when `Origin` is exactly `http://` or `https://` plus the request's `Host`,
   so a UI proxied to the API under the same origin passes without being listed. The shipped
   nginx forwards `Host $host` (`gocrm-ui/docker/nginx.conf:14`), which drops a non-standard port,
   so the exception holds on ports 80 and 443 but not on `localhost:3000`. `curl` without an
   `Origin` header does not reproduce any of this; add
   `-H 'Origin: https://your-ui.example'` when you smoke-test.
3. **Public registration ships off** (`internal/models/configuration.go:323-330`). Sign-up stays
   hidden until an admin enables it under Settings.

### 2.4 Calling the API with a key

API keys go in the `Authorization` header with the `ApiKey` scheme
(`internal/middleware/auth.go:19-28`). A bare `ApiKey:` header is rejected with 401. The API
reference (`api/index.html`, generated from `cmd/main.go`) documents the same
`Authorization: ApiKey <key>` form.

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"ChangeMe!2345"}' | jq -r .data.token)
KEY=$(curl -s -X POST http://localhost:8080/api/v1/api-keys \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"integration"}' | jq -r .data.key)
curl -s http://localhost:8080/api/v1/leads -H "Authorization: ApiKey $KEY"
```

The plaintext key (prefix `gcrm_`) is returned once. Remember that it carries its owner's full
role (#211).

### 2.5 Is my clone healthy?

```bash
make verify            # hygiene + Go build/vet/test -race + frontend build/lint/unit tests
```

This is what core CI runs for a general application change (`Makefile`, target `verify`). Two
caveats:

- The MySQL regression tests (for example `internal/utils/transaction_regression_test.go`) read
  `TEST_MYSQL_DSN` (`user:pass@tcp(host:port)/`, no database name) and **skip** without it. A green
  local run without that variable proves only the SQLite half. CI rejects skips only for the roots
  a named gate selects: the migration roots in `.github/workflows/migrations.yml` (through
  `scripts/release/check-sql-tests.py --migrations-only`), the roots `scripts/ci/check-auth.py`
  and `scripts/ci/check-go.py` pick for the auth, transaction and AEO profiles (a `full` run also
  executes `check-go.py transaction` and `check-auth.py --persistence`), and, in the Release
  workflow, `./cmd/migrate` and the `internal/integration` MySQL roots. `make verify-backend`
  itself runs without `TEST_MYSQL_DSN`, so on a `full` run any MySQL root outside those checkers
  skips.
- `make e2e` runs the Playwright suite against a disposable backend and database
  (`scripts/e2e/run.sh`). `make e2e E2E_DB_DRIVER=sqlite` needs no database server.

You do not need to rename the Go module to build, test or ship. See section 7.2.

## 3. Rebranding

Do the cosmetic column first. Defer the identity column, because every rename there is a merge
conflict on each upstream sync.

### 3.1 Cosmetic

| What | Where |
|---|---|
| Browser title and meta description | `gocrm-ui/index.html:7-8` |
| App name in the UI | one hook, `useProductName` (`gocrm-ui/src/hooks/useProductName.ts`), used by the layout, the printable quote and the sign-in and registration pages. Order: the stored `general.company_name` when a signed-in user's configuration holds a non-blank value, else `VITE_APP_NAME` (`gocrm-ui/.env.example:6`), else `GoCRM` (`FALLBACK_PRODUCT_NAME`). The sign-in and registration pages render before sign-in and always show the build-time name |
| Settings fallback name | `DEFAULT_COMPANY_NAME` in `gocrm-ui/src/contexts/ConfigurationContext.tsx:26` |
| Favicon (the only logo asset) | `gocrm-ui/public/favicon.svg` |
| Colours | the MUI palette in `gocrm-ui/src/theme/index.ts` (`createAppTheme`); the light theme's primary colour follows the `ui.theme.primary_color` setting for signed-in users |
| Mail subjects and body | the product name in the password reset subject and body, the quote subject and the ticket reply subject comes from `general.company_name` through `service.ResolveMailProductName` (`internal/service/product_name.go`), else `GopherCRM`, and is configurable in Settings; the templates stay in `internal/mailer/smtp.go` (`SendPasswordReset`, password reset), `internal/service/quote_email.go` (`QuoteEmailSubject`) and `internal/service/ticket_comment_email.go` (`TicketCommentEmailSubject`) |
| API title | `// @title` in `cmd/main.go:24`, then `make swagger` |
| README images | `docs/img/gophercrm-*.png` |

The **Settings** company name (`general.company_name`) and primary colour
(`ui.theme.primary_color`) apply to the signed-in UI (#291): the name appears in the navigation
drawer and on the printed quote, and the colour becomes the light theme's primary colour
(`gocrm-ui/src/theme/BrandThemeProvider.tsx`; the dark theme, secondary colour and snackbars are
unchanged). Accepted colours are 3- or 6-digit hex, `rgb()`/`rgba()` and `hsl()`/`hsla()`; a named
colour, `var(...)`, 4/8-digit hex, `color()`, `oklch()` or anything malformed keeps `#1976d2`
(`gocrm-ui/src/theme/primaryColor.ts`). A fresh install stores `GopherCRM`, so the name changes from
the build-time `GoCRM` on the sign-in page to `GopherCRM` after sign-in until you set your own.
The same name also reaches the password reset mail (subject and first body line) and the quote and
ticket reply subjects, read at send time (#292); a blank value, one holding a control character or
one longer than 100 characters falls back to `GopherCRM` there.

Things that look like rebrand controls and are not:

- **A changed seed default does not reach an existing database.** Seeding refreshes metadata but
  keeps the stored value (`internal/repository/configuration_repository.go:78-94`). Change existing
  installs through Settings or the configuration API.

### 3.2 Identity (defer)

These are safe to keep. Renaming any of them costs a conflict on every upstream merge:

- the Go module path `github.com/florinel-chis/gophercrm` (`go.mod:1`; section 7.2);
- the binary `bin/gophercrm` (`Makefile`), the Docker user `gophercrm` (`Dockerfile`), the systemd
  service and `/srv/gophercrm` paths (`scripts/deploy/receive-release.sh:44-60`, overridable by
  environment);
- the build tag `gophercrm_e2e` and the `GOPHERCRM_E2E_*` variables (`scripts/e2e/run.sh`,
  `internal/middleware`);
- the runner package `gophercrm-remediation-runner` (`automation/langflow/pyproject.toml`);
- the frontend directory name `gocrm-ui/`.

**Compatibility identifiers.** Leave these alone unless you accept the stated breakage:

| Identifier | Where | Renaming it… |
|---|---|---|
| `gophercrm_session` (the stored session record), `gophercrm_token`, `gophercrm_refresh_token` (its mirrors, which the browser specs read) | `gocrm-ui/src/api/sessionRecord.ts` | signs every user out once; renaming the mirrors also breaks the Playwright helpers that read or remove `gophercrm_token` |
| Web Lock name `gophercrm-session` | `gocrm-ui/src/api/sessionLock.ts` | lets tabs running the old and the new build refresh one session at the same time until they reload |
| API-key prefix `gcrm_` | `internal/service/apikey_service.go:47`, `internal/utils/crypto.go:14-29` | breaks keys already issued |
| `.gcrm-form`, `gcrm-form-styles`, `--gcrm-*` | `internal/handler/assets/form_embed.js` | breaks CSS that host pages wrote against the embedded form |

### 3.3 Repository identity

Change these before your first release or before running the tooling against your repository:

- `REPO` in `scripts/release/release.py:15` — the release script creates, downloads and publishes
  releases on this repository.
- `.github/CODEOWNERS:1`.
- README badges and links (`README.md:6,8,31,46,110`) and the compare links at the end of
  `CHANGELOG.md`.
- If you use the remediation runner: `repository`, `tracker` (issue 94 upstream), the issue lists
  and `completion_target` in your copy of `automation/langflow/gophercrm_runner/config.example.json`.

### 3.4 Licence

GopherCRM is MIT licensed. Keep the copyright notice in `LICENSE` and add your own line below it.
Keep `automation/langflow/THIRD_PARTY_NOTICES.md` while you ship that directory.

## 4. Configuration that matters

Configuration comes from three places:

1. **Build-time Vite variables** (`gocrm-ui/.env`), inlined into the static UI by `npm run build`:
   `VITE_API_BASE_URL`, `VITE_APP_NAME`. Changing them needs a rebuild. The Docker UI image does
   not read `gocrm-ui/.env` (`gocrm-ui/.dockerignore` excludes it); pass both as build arguments
   instead, for example `docker build --build-arg VITE_APP_NAME=Acme gocrm-ui` (#303; see
   [DOCKER.md](DOCKER.md)). Without the argument the image shows `GoCRM` before sign-in. The
   shipped Compose files forward `VITE_APP_NAME` from the shell or the repo-root `.env` through
   `build.args` (#308).
2. **Environment variables** for the backend, read once at startup by `internal/config/config.go`
   (and loaded from `.env` by godotenv). `.env.example` documents each one.
3. **Persisted settings** in the `configurations` table, edited under Settings. The AEO provider
   keys, models and base URLs resolve settings first and fall back to the environment, re-read on
   each call (`internal/service/aeo_credentials.go`).

### 4.1 Variables to decide before production

| Variable | Default | Read at | Notes |
|---|---|---|---|
| `JWT_SECRET` | none (required) | `config.go:271` | at least 32 characters; replace the example (section 2.3) |
| `API_KEY_SECRET` | `JWT_SECRET` | `config.go:318`, `internal/service/form_service.go:333` | see the rotation callout below |
| `DB_DRIVER` | `mysql` | `config.go:284` | `mysql` or `sqlite`; anything else stops startup |
| `DB_PATH` | `gophercrm.db` | `config.go:285` | SQLite only; no `?`; local filesystem (WAL) |
| `DB_HOST/PORT/NAME/USER/PASSWORD` | `localhost`/`3306`/`gocrm`/`root`/empty | `config.go:286-290` | MySQL only |
| `SERVER_HOST`, `SERVER_PORT` | every interface, `8080` | `config.go:294-295` | `127.0.0.1` behind a proxy on the same host |
| `SERVER_MODE` | `development` | `config.go:296` | `production` switches Gin to release mode (`cmd/main.go:217-219`). It also defaults the unused cookie `Secure` flag (`config.go:476-481`); no shipped route sets an auth cookie |
| `TRUSTED_PROXIES` | trust none | `config.go:297` | see below |
| `CORS_ALLOWED_ORIGINS` | none beyond the localhost list | `config.go:298` | every browser origin of your UI |
| `APP_BASE_URL` | `http://localhost:5173` | `config.go:328` | the UI origin; password-reset links |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | `config.go:356` | this backend's public origin; form confirmation links |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM` | empty, 587, empty, empty, `no-reply@localhost` | `config.go:321-325` | empty host means log-only mail |
| `RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY` | empty | `config.go:357-358` | both or the layer is skipped |
| `FORM_RETENTION_DAYS` | off | `config.go:360` | age in days after which spam and never-confirmed submissions with no linked lead are permanently removed; 3-day floor, 36500 ceiling. The first boot with it set removes the existing backlog; safe on every replica |
| `AUTOMATION_ENABLED` | `true` | `config.go:363` | true on exactly one instance |
| `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR` | `true`, `6` | `config.go:350-351` | daily AEO run |
| `AEO_EGRESS_ALLOWED_ORIGINS`, `AEO_EGRESS_LOCAL_ORIGINS` | empty | `config.go:353` | environment only; see `docs/AEO_SECURITY.md` |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `json` | `config.go:313-314` | SQL statements only at debug/trace |

The AEO egress policy, the schedule and the custom engine (`AEO_CUSTOM_*`) can be set only in the
environment. `docs/AEO_SECURITY.md` explains why.

### 4.2 Choices that are irreversible or easy to get wrong

> - **Secrets.** `API_KEY_SECRET` (or `JWT_SECRET` when it is unset) keys the HMAC of API keys
>   (`cmd/main.go:309`), refresh and password-reset tokens (`cmd/main.go:291`), form confirmation
>   tokens and time-trap signatures (`internal/service/form_service.go:327-338`), and the
>   encryption of sensitive settings (`cmd/main.go:294`, `internal/utils/secretbox.go`). Rotating
>   it invalidates every API key, refresh token, reset link and pending form confirmation, and
>   leaves stored AEO provider keys unreadable (they are treated as unset). Rotating `JWT_SECRET`
>   invalidates every issued access token, but when `API_KEY_SECRET` is set separately and left
>   unchanged, refresh tokens stay valid (they are hashed with it,
>   `internal/service/auth_service.go:408-413`) and the UI silently trades them for new access
>   tokens signed with the new secret (`gocrm-ui/src/api/client.ts:232-256`). To end every session,
>   rotate `API_KEY_SECRET` as well, with the consequences above. Per user, logout, a password
>   change and a password reset revoke that user's refresh tokens and end the access tokens issued
>   before them (they increment `users.token_version`, #236), and a deactivated user's refresh is
>   refused. Restoring a database backup needs the secrets it was written with; a backup also brings
>   back older `token_version` values, so access tokens ended after it was taken pass again until
>   they expire.
> - **`SERVER_MODE=production`.** Without it Gin runs in debug mode. The application issues no auth
>   cookies (`utils.SetAuthCookies` in `internal/utils/cookie.go:21` has no caller outside tests),
>   so the cookie settings it also affects have no effect today.
> - **`TRUSTED_PROXIES`.** The rate limiter keys on `c.ClientIP()`
>   (`internal/middleware/rate_limit.go:185`). Behind a reverse proxy or CDN with no trusted
>   proxies, every client shares the proxy's address and therefore one rate-limit bucket: one
>   noisy user locks out everyone on `/auth`.
> - **`CORS_ALLOWED_ORIGINS`.** Missing origins fail silently in the browser (section 2.3).
> - **`PUBLIC_BASE_URL` versus `APP_BASE_URL`.** The first is the API's public origin, used in form
>   confirmation links; the second is the UI's, used in password-reset links. Left at their
>   defaults, both send recipients to `localhost`.
> - **SMTP.** With `SMTP_HOST` empty every mail is logged instead of sent, and the sending code
>   reports success (README "Known Limitations"); form mail and ticket comment mail
>   (`email_customer`, #219) are then recorded `log_only`. A form mail
>   that fails is recorded per mail and shown to staff (`GET /forms/deliveries`), and admin and
>   sales retry it by hand; nothing retries automatically.

### 4.3 Settings

Settings are seeded at startup from `internal/models/configuration.go`. Two settings that had no
effect were retired in #224; startup deletes them (`models.RetiredConfigurationKeys`):

- `tickets.auto_assign_support` — nothing distributes tickets among support users. A ticket
  created without `assigned_to_id` is assigned to its creator
  (`internal/handler/ticket_handler.go:107-110`).
- `security.session_timeout_hours` — the access-token lifetime comes from `JWT_EXPIRY_HOURS` (default 24, `internal/config/config.go:302`, used at
  `internal/service/auth_service.go:358`) and the refresh-token lifetime from
  `REFRESH_TOKEN_EXPIRY_DAYS` (default 30, `config.go:307`). `JWT_ACCESS_TOKEN_MINUTES` is read
  (`config.go:303`) but only feeds the unused cookie helper.

The company name and colour settings are covered in
section 3.1.

### 4.4 Module switches

Only two exist: `AUTOMATION_ENABLED` (`config.go:363`) turns the automation worker off on an
instance, and `AEO_SCHEDULE_ENABLED` (`config.go:350`) turns off the daily AEO run. Forms and AEO
as a whole have no switch; removing a module is described in [EXTENDING.md](EXTENDING.md).
`FORM_RETENTION_DAYS` (`config.go:360`) is the opposite kind of setting: the form retention purge
stays off until you set it.

## 5. Deployment shapes

### 5.1 Docker Compose on one host

`docker-compose.yml` (MySQL 8, API, UI) and `docker-compose.sqlite.yml` (API, UI) are tuned for a
local machine. Before you put either on the internet:

- They do not forward SMTP, `PUBLIC_BASE_URL`, reCAPTCHA, `AUTOMATION_*` or `AEO_SCHEDULE_*`
  variables to the backend, and they set `APP_BASE_URL` and `CORS_ALLOWED_ORIGINS` to
  `localhost:${UI_PORT}` (`docker-compose.yml:41-60`, `docker-compose.sqlite.yml:25-43`). Apply
  the committed `docker-compose.production.example.yml` with a second `-f` and supply your real
  values from the shell or the repo-root `.env`; [DOCKER.md](DOCKER.md) ("Production override
  example") lists what it forwards and the values it requires.
- The UI image bakes `VITE_API_BASE_URL=/api/v1` and `VITE_APP_NAME`, empty by default, which
  shows `GoCRM` before sign-in (`gocrm-ui/Dockerfile:19-22`). Set the name with
  `--build-arg VITE_APP_NAME=...` (see [DOCKER.md](DOCKER.md)); the shipped Compose files forward
  `VITE_APP_NAME` from the shell or the repo-root `.env` through `build.args` (#308).
- Put TLS in front, and close the plain-HTTP ports behind it. Passwords and bearer tokens cross
  plain HTTP in clear text, and both files publish the backend's `8080` and the UI's `UI_PORT` on
  every host interface (`docker-compose.yml:61-62,78-79`, `docker-compose.sqlite.yml:46-47,63-64`),
  so a proxy alone leaves both reachable without TLS. `docker-compose.production.example.yml`
  drops the backend publication and binds the UI to `127.0.0.1` (Compose 2.24.4 or later; an
  older Compose accepts the file but keeps the all-interfaces UI publication);
  [DOCKER.md](DOCKER.md) ("Several installations on one host") explains the syntax.
- The SQLite stack is two containers with a fixed project name (`gophercrm-sqlite`), fixed host
  port 8080 and a fixed subnet (`docker-compose.sqlite.yml:20,46-47,69-76`). Several installs on
  one host need separate project names, ports, subnets, volumes and secrets.
- Back up SQLite by stopping the backend and copying the file with its `-wal` and `-shm`
  siblings (restore all three together), or with `VACUUM INTO` (see [DOCKER.md](DOCKER.md)).
  Back up before every upgrade: on
  SQLite the schema only moves forward through startup auto-migration.

Validate the merged result with every file named (an explicit `-f` also turns off the automatic
pick-up of a `docker-compose.override.yml`), then start with the same pair:

```bash
docker compose -f docker-compose.yml -f docker-compose.production.example.yml config -q
docker compose -f docker-compose.sqlite.yml -f docker-compose.production.example.yml config -q
```

Run `config` without `-q` to read the merged `ports:` before you expose anything.

### 5.2 Release tarball and systemd, through the shipped workflows

The upstream pipeline builds a Linux AMD64 tarball and deploys it over SSH:

The scripts install releases on a server you have already provisioned; they do not provision it.
Before the first deploy you need, on the server: a working MySQL or MariaDB database; the
directory `/srv/gophercrm` (bootstrap only re-owns it, `scripts/deploy/bootstrap.sh:69`); the
tools `flock`, `rsync`, `mysqldump`, `gzip`, `curl`, `tar` and `systemctl` (checked at line 63);
a systemd unit for the service (`gophercrm` by default) that runs `/srv/gophercrm/bin/gophercrm`
(a link into the current release, line 137) with the environment file
`/srv/gophercrm/env/gophercrm.env` (`scripts/deploy/receive-release.sh:54-55`, restarted at
line 161); and a TLS-terminating web server that serves the web root and proxies
`/api/v1` to the backend. [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) walks a fresh
systemd/nginx host through each of them, with a sample unit, environment file and nginx sites
(#231); no sample file ships under `scripts/deploy`. On
your machine: root SSH access to the server, a `gh` login with admin rights on the repository,
and `main` protected (bootstrap refuses otherwise, lines 2-4 and 45-46).

1. Set `REPO` in `scripts/release/release.py:15` to your repository.
2. Run `scripts/deploy/bootstrap.sh <user@host> <public URL> <web root>` once. It creates a deploy
   key pinned to `scripts/deploy/receive-release.sh` on the server, a GitHub `production`
   environment, and the four secrets `PRODUCTION_SSH_KEY`, `PRODUCTION_SSH_KNOWN_HOSTS`,
   `PRODUCTION_HOST` and `PRODUCTION_SSH_USER` (`.github/workflows/deploy.yml:52-55`). It finds the
   repository through `gh repo view`.
3. On the server, `receive-release.sh` reads `GOPHERCRM_ROOT`, `GOPHERCRM_SERVICE`,
   `GOPHERCRM_WEB_ROOT`, `GOPHERCRM_ENV_FILE` and other overrides (lines 44-60). Its pre-restart
   backup runs `mysqldump` (line 206), so this path is not ready for SQLite as shipped.
4. Prepare a release with `python3 scripts/release/release.py prepare --version X.Y.Z`, merge it,
   run the **Release** workflow with `publish=false` (a full rehearsal that creates nothing), then
   with `publish=true`. **Deploy** runs after a successful published release.
   `docs/VALIDATION_RELEASE.md` ("Prepare, rehearse, publish") has the detail.

### 5.3 Bring your own deployment

Use the Release workflow for the artifact only, or build it yourself (`make build` plus
`npm run build` in `gocrm-ui/`). `deploy.yml` triggers on every successful Release run; it skips
with a notice while its secrets are missing (`.github/workflows/deploy.yml:58`), but disable the
workflow if you never intend to use it.

### 5.4 Worker topology

Run the automation worker on exactly one instance. Every other replica sets
`AUTOMATION_ENABLED=false` (README "Known Limitations"). Rate limits are per process, so they are
not shared between replicas either. The form retention purge (`FORM_RETENTION_DAYS`) needs no such
rule: it is safe on every replica, because extra sweeps only compete for the same rows.

AEO is replica-safe on MySQL/MariaDB (#229). Each run has a database owner and a heartbeat the
owner renews every 30 s; admission is one insert guarded by the unique `aeo_runs.running_lock`, so
a run on any replica answers 409 to a start on another; the daily run is claimed once per slot
through the unique `aeo_runs.schedule_slot`, so every replica may keep `AEO_SCHEDULE_ENABLED` on.
The boot reconciliation (`aeoService.ReconcileRunningRuns` in `setupDependencies`) and the sweep
before every start fail only runs whose heartbeat is older than 3 minutes, legacy rows without one,
or runs started more than 6 hours ago, so a starting replica never fails another replica's live
run. A run of a crashed process is recovered up to 3 minutes after the crash, at the next boot or
start attempt. Requirements:

- every replica uses the same `TZ` and `AEO_SCHEDULE_HOUR` (the slot is the configured local hour
  stored in UTC);
- replica clocks are synchronised to well under 90 s;
- upgrades stop every replica before starting the new version: an older binary neither sets the
  running lock nor spares other replicas' runs, so mixed versions are not supported;
- several processes on one SQLite file are not supported (SQLite deployments run one process).

## 6. CI on a fork

- **No secrets are needed for CI.** The workflows start throwaway databases in service containers
  and generate `JWT_SECRET` at runtime (`.github/workflows/ci.yml:298`). Deploy secrets are needed
  only for section 5.2.
- **Keep the `gophercrm_runner` Python package.** Ordinary CI imports it:
  `scripts/ci/check-auth.py:16-19`, `scripts/ci/go_checks.py:10-13` (used by `check-go.py`),
  `scripts/ci/check-go.py:7`, `scripts/ci/plan-documentation.py:10`, and
  `scripts/ci/plan-validation.py:49`, which reads `validation.py` from the base commit. You may
  drop the Langflow graphs (`automation/langflow/flows/`) and the runner runtime, but not
  `automation/langflow/gophercrm_runner/`. `.github/workflows/remediation-runner.yml` is also a
  reusable workflow that Release calls as its `runner-validation` job
  (`.github/workflows/release.yml:54-56`), and the `build` job needs that job (line 108). Deleting
  the workflow means removing that job and its entry in `needs:` from `release.yml` in the same
  change, or Release stops working.
- **Profile selection.** The `plan` job picks one of nine validation profiles from the changed
  paths (`automation/langflow/gophercrm_runner/validation.py:584-586`). To see what CI will run for
  your change, ask the selector instead of guessing:

  ```bash
  python3 automation/langflow/gophercrm_runner/validation.py \
    --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
  ```

  Shared code, schema, dependency and API-contract changes fall back to `full`
  (`make verify` plus the whole browser matrix). `docs/VALIDATION_RELEASE.md` has the profile table.
- **Simplifying.** If you do not want contextual selection, make the `plan` job always emit
  `profile=full`, which is what `scripts/ci/plan-validation.py:51-58` writes when it has no trusted
  selector. Do not delete the planner import chain without replacing it.
- **The hygiene hook.** It runs only after `git config core.hooksPath scripts/git-hooks`
  (`scripts/git-hooks/pre-commit:15`). It refuses new folders under `docs/` other than `img`,
  `screenshots` and `testing` (line 19), and two reserved working-paper paths (lines 37-38); keep
  or edit those rules as your own policy. The forbidden-terms sweep runs only when the untracked
  terms file named at line 52 exists, so on a fresh clone it is skipped. The size check is the
  same one `make verify-hygiene` runs.

## 7. Staying in sync with upstream

### 7.1 Remote and merge

```bash
git remote add upstream https://github.com/florinel-chis/GopherCRM.git
git fetch upstream
git merge upstream/main
```

Merge rather than rebase: a merge keeps upstream commit ids, so later merges stay small.

The hygiene hook (section 6) is a `pre-commit` hook, and it is the only hook the repository ships
(`scripts/git-hooks/`). It runs on `git commit`, including `--amend` and a conflicted merge that
you conclude with `git commit`. It does not run on a merge that completes without conflicts (Git
calls `pre-merge-commit` there, which is not shipped) or on the commits a rebase replays. Run
`make verify-hygiene` after a sync to get its size check.

### 7.2 The module path

Keep `github.com/florinel-chis/gophercrm`. The module is never fetched with `go get`, so the name
does not have to match your repository, and it appears in 323 tracked files, this guide included
(`git ls-files | xargs grep -l 'github.com/florinel-chis/gophercrm' | wc -l`, measured on
`f5ec601`). A rename touches every one of them on every sync.

If you rename anyway, do it in one mechanical commit and redo it after each merge. Besides the Go
imports, the path is hard-coded in `scripts/ci/check-go.py:60`, `scripts/release/check-sql-tests.py:11-32`,
`scripts/release/tests/test_release.py` and `scripts/release/tests/test_sql_coverage.py`. Deriving
it from `go.mod` instead is #223.

### 7.3 Conflict hot spots, and seams that avoid them

| Hot spot | Seam |
|---|---|
| `setupDependencies` in `cmd/main.go:257` | call one fork function from it, in its own file under `cmd/` |
| `internal/handler/routes.go` | a `fork_routes.go`, the way `aeo_routes.go` and `form_routes.go` are split out |
| the AutoMigrate list in `internal/models/database.go:114-156` | append your models at the end, in one block |
| `gocrm-ui/src/routes/index.tsx` and the navigation in `gocrm-ui/src/layouts/MainLayout.tsx` | group fork routes and nav items in one block each |
| seeded settings in `internal/models/configuration.go` | a fork-specific key prefix, appended at the end |
| `CHANGELOG.md` | a separate fork section, or a separate file |
| `api/swagger.json`, `api/swagger.yaml` | never hand-merge: take either side, then run `make swagger` |
| the four version stamps (`cmd/main.go` `@version`, `gocrm-ui/package.json`, both swagger files) | use your own version line and re-stamp after a merge |

[EXTENDING.md](EXTENDING.md) shows each seam in a worked example.

### 7.4 Migration versions

`migrations/` is applied by golang-migrate (`cmd/migrate/main.go:102`) on MySQL and MariaDB.
Versions are numeric timestamps (`go run ./cmd/migrate -command create -name <name>` stamps
`time.Now()` as `YYYYMMDDHHMMSS`). A plain `up` does not apply a migration numbered below the
newest version the database has already applied, so an upstream migration older than your newest
fork migration would be silently skipped on an existing database. The procedure:

1. Merge upstream first.
2. Give fork migrations timestamps later than upstream's newest.
3. Before releasing, re-stamp any fork migration that has not yet shipped if a merge brought in a
   newer upstream one.
4. Rehearse a populated-database upgrade. The `cmd/migrate` tests and
   `.github/workflows/migrations.yml` (MySQL 8 and MariaDB 10.11) run fresh install, populated
   upgrade, rollback and reapply; they need `TEST_MYSQL_DSN` locally.

New tables use `CREATE TABLE IF NOT EXISTS`, because a new binary may already have created them
through AutoMigrate before the migration runs
(`migrations/20261026120000_add_products_and_quotes.up.sql:6-8`). SQLite installs never run
`migrations/`; their schema comes from AutoMigrate at startup. Every schema change needs both
halves, as [EXTENDING.md](EXTENDING.md) explains.

### 7.5 Security fixes

Watch the `### Security` headings in `CHANGELOG.md` and issues with the `security` label
(`gh issue list --repo florinel-chis/GopherCRM --label security --limit 200`). On 2026-10-31 the
open ones are #183, #210 and #211, plus #206 for the remediation runner.

## 8. Fresh-fork acceptance walkthrough

Each step has an observable result. Do them in order on a new fork before you take real data.

1. **Build.** `make build` writes `bin/gophercrm`; `make verify-hygiene` ends with `OK`.
2. **First admin.** `bin/create-admin -non-interactive ...` succeeds, and you can sign in through
   the UI.
3. **Your name in mail.** With SMTP configured, request a password reset: the subject carries your
   product name (section 3.1), and the link points at your `APP_BASE_URL`.
4. **A form from a real browser.** Embed a form on a page served from another origin, add that
   origin to the form's allowlist, and submit it from a browser. The request carries an `Origin`
   header; the lead appears. If you also use the hosted form page or an iframe, the CRM's own
   origin must be on the allowlist too. With double opt-in, the confirmation link points at your
   `PUBLIC_BASE_URL`.
5. **Your CI is green** on a pull request in your repository, including the `migrations` job.
6. **A restored backup.** Restore last night's backup into a scratch install with the same
   secrets, start it, and sign in.
7. **An upstream merge rehearsal.** `git merge upstream/main` on a branch, `make verify`, and the
   selector command from section 6 on the merge commit.

## 9. Why fork instead of rebuild

Measured on `4dfe404`. Commands:
`git ls-files '*.go' | grep -v _test.go | xargs cat | wc -l`;
`git ls-files '*_test.go' | xargs grep -hE '^func Test[A-Za-z0-9_]*\(' | wc -l`;
`git ls-files 'gocrm-ui/src/*.ts' 'gocrm-ui/src/*.tsx' | grep -vE '\.test\.'`;
`git ls-files 'gocrm-ui/e2e/tests/*.spec.ts' | xargs grep -hE '^\s*test(\.(only|skip|fixme))?\('`;
the `paths` object of `api/swagger.json`;
`gh pr list --state merged --limit 500` and `gh issue list --state closed --limit 500`.

- **Application code:** 48,263 lines of Go in 192 files and 30,259 lines of TypeScript/TSX in 160
  files, covering 103 API paths and 149 operations (`api/swagger.json`), four roles, and leads,
  customers, companies, deals, products and quotes, tickets, tasks, forms, follow-up automation,
  AEO tracking and GDPR erasure.
- **Tests:** 75,472 lines of Go tests in 217 files, more test code than application code. They
  include 990 top-level `Test` functions, 107 Vitest files and 22 Playwright specs with 161 browser
  tests, run against MySQL 8, MariaDB 10.11 and SQLite (`.github/workflows/ci.yml`). The manual
  catalog in `docs/testing/` documents over 600 cases.
- **Schema:** 12 SQL migration pairs, kept in parity with GORM AutoMigrate and checked by a
  migrations workflow for fresh install, populated upgrade, rollback and reapply.
- **Delivery machinery:** 5 GitHub workflows with 15 jobs; 9 validation profiles that pick the
  checks from the diff; a no-skip SQL coverage gate; one e2e driver used identically locally and
  in CI; a release pipeline that rehearses before it publishes.
- **History:** 129 merged pull requests between 2025-11-15 and 2026-10-31, and 58 closed issues.
  They include all 27 tickets of an architecture audit (tracking issue #94), each recording its
  delivering pull request and validation evidence. For example, #102 (single-use password-reset
  claims, PR #145) records six SQLite/MySQL regression roots and 36 subtests passing without skips.

What this buys you is mostly not lines of code. Many of those pull requests fixed defects a
rebuild would probably repeat, and each fix has a regression test that fails without it:

- transactions that leaked connections or did not roll back on a panic (#106, PR #141);
- single-use tokens that could be spent twice or lost on a failure (#101, #102, #103);
- a CSV export that silently lost rows under a custom sort (#105, PR #150);
- list pages without a total order for tied sort values (#110, PR #177).

What it does not buy you, stated plainly:

- **Open issues:** the fork inherits them (section 1.3).
- **Changelog:** a large unreleased section on top of 1.2.0 (section 1.4).
- **MySQL test server:** the regression tests need one before a local run proves the MySQL half.
- **CI coupling:** CI depends on a Python package in `automation/langflow/` (section 6).
- **Reading time:** the conventions are dense enough that your first change will take a while.

The cost of rebuilding this from scratch has not been measured, so this guide makes no claim
about saved hours. The numbers above are what exists, and each of them can be checked with the
commands listed.
