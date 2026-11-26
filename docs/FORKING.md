# Forking GopherCRM

> **For:** Teams running GopherCRM under their own name. Part of the [documentation index](README.md).

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

Roles are the four system roles `admin`, `sales`, `support` and `customer` (`UserRole` (`internal/models/user.go`)),
plus custom roles an admin builds from the permission catalogue (`internal/permissions/catalogue.go`,
`SetupRoleRoutes` (`internal/handler/routes.go`)). Routing lives in `internal/handler/routes.go`,
`protected_routes.go`, `aeo_routes.go`, `form_routes.go`, `form_public_routes.go`,
`quote_public_routes.go` and `scim_routes.go`; the wiring is in `setupDependencies` (`cmd/main.go`).
SCIM is mounted under `/scim/v2`, outside the API prefix (`SetupSCIMRoutes` (`internal/handler/scim_routes.go`),
`router.Group("/scim/v2")` in `cmd/main.go`).

| Module | Who may use it | What is guaranteed | Test evidence |
|---|---|---|---|
| Auth, sessions, API keys | public `/auth`; own keys for any authenticated user | rotating refresh tokens, single-use reset tokens claimed inside one transaction, lockout after 5 failures, one generic 401 when login or `POST /auth/refresh` rejects the credential (an operational refresh failure is a retryable 503 `SESSION_REFRESH_UNAVAILABLE` and a malformed body a 400, `internal/handler/auth_handler.go` `AuthHandler.Refresh`; change-password answers 400 for a wrong current password and 409 `SESSION_STALE` for a stale session, `AuthHandler.ChangePassword` (`internal/handler/auth_handler.go`)) | `internal/service/auth_password_reset_regression_test.go`, `auth_refresh_rotation_regression_test.go`, `internal/handler/apikey_handler_test.go` |
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
| Follow-up automation | rules admin; runs admin+sales (sales never sees ticket runs) | outbox event per lead/deal/ticket write (#242); conditional run advance after a locking read of the record; ticket delete cancels the ticket's open runs; lease-based claims with a stale sweep at boot and every tick (#237) | `internal/service/automation_runtime_test.go`, `internal/service/automation_run_lease*_test.go`, `cmd/automation_runtime_test.go`, `internal/service/ticket_automation_*_test.go` |
| AEO (answer-engine tracking) | admin+sales+support; run and generate admin+sales | keys encrypted at rest; environment-only egress allowlist | `internal/service/aeo_service_test.go`, `aeo_egress_boundary_test.go`, `docs/AEO_SECURITY.md` |
| GDPR erasure | deleting a user, customer or lead: admin, plus the owning sales user for an unconverted lead (`LeadHandler.Delete` (`internal/handler/lead_handler.go`)) | personal fields of that identity overwritten, then soft-deleted, in one transaction, together with the unlinked form submissions whose email matches it and their tokens and delivery rows (#214), and the staff-typed text of the deals and quotes linked to the erased customers and leads (title `[redacted]`, notes, lost reason and line descriptions reset) with the notifications about those deals (#245), and the unused response-link tokens of the quotes addressed to the erased customers are invalidated (#395). Ticket and task deletes are soft deletes that scrub no text (`ticketRepository.Delete` (`internal/repository/ticket_repository.go`), `taskRepository.Delete` (`internal/repository/task_repository.go`); a ticket delete also purges its mail deliveries and notifications and cancels its automation runs, #219/#242), so text that names a person stays; for tickets, DEV_ERASURE.md documents an operator redaction procedure to run before the erasure | `internal/repository/identity_erasure_test.go`, `unlinked_submission_erasure_test.go`, `automation_erasure_test.go`, `business_text_erasure_test.go`, `erasure_quote_tokens_test.go`, `tests/ticket_redaction_procedure_test.go`, `tests/business_text_erasure_test.go` |
| Roles and permissions | catalogue and roles `roles.view` (or `users.assign_role` for reading); writes `roles.manage`; role audit log `roles.audit_view`; keys in `internal/permissions/catalogue.go`, routes in `SetupRoleRoutes` (`internal/handler/routes.go`) | custom roles built from the permission catalogue; the escalation rule, the system-role rule and the in-use rule are checked in the role service's transaction, and no role change, deactivation or erasure leaves zero active admins (the last-admin rule) ([AUTHORIZATION.md](AUTHORIZATION.md), [roles and permissions](user-guide/permissions-access/roles-and-permissions.md)) | `internal/integration/role_api_integration_test.go`, `role_escalation_integration_test.go`, `role_last_admin_integration_test.go` |
| Single sign-on | settings admin only, behind `settings.sso_manage` (`SetupSSOSettingsRoutes` (`internal/handler/routes.go`)); sign-in public under `/auth/oidc` | OpenID Connect with one identity provider at a time, no SAML; the role comes from the role mapping at each sign-in; local password accounts keep signing in as the break-glass path ([single sign-on](user-guide/permissions-access/single-sign-on.md)) | `internal/idkit/sso/sso_test.go`, `internal/idkit/sso/logout_test.go`, `internal/handler/auth_oidc_handler_test.go` |
| SCIM provisioning | the identity provider with a SCIM provider token on `/scim/v2`; tokens managed admin only, behind `settings.scim_manage` (`SetupSCIMTokenRoutes` (`internal/handler/scim_routes.go`)) | Users and Groups resources; groups are the role mapping's values; a provider DELETE deactivates ([SCIM provisioning](user-guide/permissions-access/scim-provisioning.md)) | `internal/handler/scim_user_handler_test.go`, `scim_group_handler_test.go`, `internal/idkit/scim/scim_test.go`, `cmd/scim_wiring_test.go` |
| Webhooks | `webhooks.manage`, admin only (`SetupWebhookRoutes` (`internal/handler/routes.go`)) | created, updated and deleted events of the seven entities in `internal/webhook/catalog.go` (users emit none); signed deliveries and a delivery log, with a screen in Settings > Webhooks ([webhooks](user-guide/permissions-access/webhooks.md)) | `internal/handler/webhook_handler_test.go`, `internal/webhook/emitter_test.go`, `cmd/webhook_runtime_test.go` |
| Sign-in and role audit | sign-in audit `audit.sign_ins_view` (`SetupAuthAuditRoutes` (`internal/handler/routes.go`)); role audit `roles.audit_view` | a sign-in audit log of how people and integrations sign in and why a sign-in was refused, plus password changes and resets, logouts and API key creation and revocation; an audit log of role changes ([sign-in audit](user-guide/permissions-access/sign-in-audit.md)) | `internal/handler/auth_audit_log_handler_test.go`, `internal/integration/role_audit_integration_test.go` |
| Dashboard | admin+sales+support | activity lists scoped inside the query | `internal/repository/dashboard_queries_test.go` |
| Configuration | `/configurations/ui` any user; the rest admin | seeding never overwrites a stored value; sensitive values encrypted and masked | `internal/repository/configuration_repository_test.go`, `internal/service/configuration_service_test.go` |

`docs/FEATURES.md` is the long inventory, with every behaviour mapped to its tests.

### 1.2 Not present

Checked against the code on `98dabb04` (#733):

- Outbound webhooks cover the created, updated and deleted events of leads, customers, tickets,
  deals, tasks, companies and quotes (`lead.created`, `lead.updated`, `lead.deleted`, `customer.created`,
  `customer.updated`, `customer.deleted`, `ticket.created`, `ticket.updated`, `ticket.deleted`,
  `deal.created`, `deal.updated`, `deal.deleted`, `task.created`, `task.updated`, `task.deleted`,
  `company.created`, `company.updated`, `company.deleted`, `quote.created`, `quote.updated`,
  `quote.deleted`; #345, #408, #437, #515, #516, #517 and #518, cuts of #232): users emit no
  events. The registry and the delivery log have a screen in
  Settings > Webhooks (#409).
- No MCP server.
- API keys have no scopes of their own (`internal/models/apikey.go` holds only name, hash,
  prefix, owner, last use, expiry, an active flag and a revocation time): a key acts with its owner's current permissions. A
  least-privilege credential is a dedicated user on a custom role, see
  [Least-privilege integration user](AUTHORIZATION.md#least-privilege-integration-user).
- No ticket channel and no inbound mail (`internal/models/ticket.go`). Every ticket is created
  through the API or the UI. Outbound ticket replies by email shipped in #219: a staff comment sent
  with `email_customer: true` is mailed once to the ticket's customer and its outcome recorded in
  `ticket_mail_deliveries` (backend only; no retry). A ticket channel field and inbound mail remain
  later work.
- No internationalisation framework in `gocrm-ui/package.json`.
- Quote PDF output needs a browser binary on the server, `chrome-headless-shell` or a full
  Chrome or Chromium (`QUOTE_PDF_CHROME_PATH`,
  off by default outside the Docker image; `docs/FEATURES.md` row 4d.14, #384), and tax is one
  rate per quote line only: no tax categories, reverse charge or
  per-country rules (`docs/FEATURES.md`, rows 4d.3 and 4d.7; #324, #233). The web UI shows and
  edits the per-line rate and shows the line tax and the quote's tax totals since #325 (as recipe 5
  of [USE_CASES.md](USE_CASES.md) says).
- CSV export covers customers and leads only (`CustomerHandler.Export` (`internal/handler/customer_handler.go`),
  `LeadHandler.Export` (`internal/handler/lead_handler.go`), #234), besides the AEO results export
  (`AEOHandler.Export` (`internal/handler/aeo_export.go`), #244); other list views, and PDF export of lists, are `docs/ROADMAP.md` "Data export" (the PDF of a
  single quote is delivered, #384).
- Authentication is header-only (Bearer or ApiKey); no cookie credential is accepted, so CSRF does
  not apply and the server ships no CSRF protection (README "Known Limitations"). A cookie session
  added in a fork must ship with session-bound CSRF protection (#261).
- An issued JWT ends early only through the events that increment `users.token_version` (#236):
  logout (including the single sign-on front-channel logout, `GET /auth/oidc/frontchannel-logout`,
  which logs the account out the same way), a password change, a password reset, deactivation (by
  an administrator or through SCIM, where a provider DELETE deactivates too), a SCIM group change
  that leaves the account in no mapped group, and a SCIM group change that gives it a role lacking
  a permission the old one had; an erased account's tokens are refused as well. A role change made any other way does not end it, and the token a refresh
  rotation replaces stays valid until it expires (README "Known Limitations").
- The automation worker (#237) and AEO runs with the daily schedule (#229) are coordinated across
  replicas on MySQL/MariaDB (README "Known Limitations"; section 5.4). Several processes on one
  SQLite file are not supported.

### 1.3 Open issues you inherit

The fork inherits the open issues of the upstream repository. List them with
`gh issue list --repo florinel-chis/GopherCRM --state open --limit 200` (add
`--json number --jq length` for the count; without `--limit`, `gh` lists at most 30). The gaps
this guide and [USE_CASES.md](USE_CASES.md) cite by issue number may have closed since; check
each one there before planning around it.

### 1.4 Released and unreleased code

The latest release is 1.4.0 (the `[1.4.0]` heading of `CHANGELOG.md`, tag `v1.4.0`). Together with 1.3.0 it has
everything since 1.2.0: companies, deals, products and quotes, ticket comments, follow-up automation, the transactional token flows,
roles and permissions, API keys, webhooks, single sign-on and SCIM provisioning, and more. Anything under
`[Unreleased]` (the first heading of `CHANGELOG.md`) is on `main` but in no tagged release yet. A fork taken from
`main` gets all of it; a fork taken from the `v1.4.0` tag gets the released part.

## 2. First run

### 2.1 SQLite, from source

No database server is needed. Requires Go 1.25 (the `go` directive of `go.mod`), Node.js 22 or later, and `jq` for the
API example in section 2.4.

```bash
cp .env.example .env
# Edit .env:
#   JWT_SECRET=<output of: openssl rand -base64 32>
#   DB_DRIVER=sqlite
go run ./cmd                     # or: make run
```

The backend listens on `http://localhost:8080` and creates `gophercrm.db` in the working
directory (`DB_PATH`, `Load` (`internal/config/config.go`)). The schema is created by auto-migration at
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
(`API_BASE_URL` in `gocrm-ui/src/api/client.ts`, `VITE_API_BASE_URL=` in `gocrm-ui/.env.example`).

### 2.2 MySQL or MariaDB

```bash
make create-db                   # mysql -u root < scripts/create_database.sql
```

`scripts/create_database.sql` creates `gocrm`, the default `DB_NAME` (read by `Load` (`internal/config/config.go`), and
`.env.example`). A fork that renames the database changes both together; a hygiene test fails when
they differ. Then set `DB_DRIVER=mysql` and the `DB_*` credentials and run `go run ./cmd`.

The Compose stack in `docker-compose.yml` brings up MySQL 8, the API and an nginx-served UI. See
[DOCKER.md](DOCKER.md) and section 5.1 below before using it beyond a laptop.

### 2.3 Three traps on the first run

1. **The example JWT secret stops the app.** `Load` (`internal/config/config.go`) rejects an empty
   value, the literal `default-secret-change-this`, any value starting with `CHANGE_THIS` (the
   placeholder in `.env.example` is one) and values shorter than 32 characters, so a copied
   `.env.example` does not start. Replace the secret with the output of
   `openssl rand -base64 32`.
2. **CORS.** The backend accepts browser requests from `localhost`/`127.0.0.1` on 5173 and 3000,
   and `localhost:8080` (`defaultAllowedOrigins` (`internal/middleware/cors.go`)). Any other cross-origin UI needs
   `CORS_ALLOWED_ORIGINS`. An unlisted origin gets a bare 403, which the login page shows as a
   failed sign-in. The one exception: the pinned `github.com/gin-contrib/cors` v1.7.5 (the `require` block of `go.mod`)
   skips the allowlist when `Origin` is exactly `http://` or `https://` plus the request's `Host`,
   so a UI proxied to the API under the same origin passes without being listed. The shipped
   nginx forwards `Host $host` (`proxy_set_header Host $host` in `gocrm-ui/docker/nginx.conf`), which drops a non-standard port,
   so the exception holds on ports 80 and 443 but not on `localhost:3000`. `curl` without an
   `Origin` header does not reproduce any of this; add
   `-H 'Origin: https://your-ui.example'` when you smoke-test.
3. **Public registration ships off** (`DefaultConfigurations` (`internal/models/configuration.go`)). Sign-up stays
   hidden until an admin enables it under Settings.

### 2.4 Calling the API with a key

API keys go in the `Authorization` header with the `ApiKey` scheme
(`Auth` (`internal/middleware/auth.go`)). A bare `ApiKey:` header is rejected with 401. The API
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

The plaintext key (prefix `gcrm_`) is returned once. It acts with its owner's permissions, so a
key for an integration belongs to a dedicated user on a custom role:
[Least-privilege integration user](AUTHORIZATION.md#least-privilege-integration-user).

### 2.5 Is my clone healthy?

```bash
make verify            # hygiene, frontend build/lint/unit tests, then gofmt + Go build/vet/test -race, API docs
```

This is what a release runs for the backend and frontend, and the check to run before pushing
(`Makefile`, target `verify`). Pull-request CI is narrower: it builds and vets the module and tests
only the Go packages a change touches, their importers and the packages that read the changed
files, without `-race` (`python3 scripts/ci/check-changed-go.py --base origin/main --list` shows
the selection for committed changes). Two caveats:

- The MySQL regression tests (for example `internal/utils/transaction_regression_test.go`) read
  `TEST_MYSQL_DSN` (`user:pass@tcp(host:port)/`, no database name) and **skip** without it. A green
  local run without that variable proves only the SQLite half. Pull-request CI runs them on
  MariaDB 10.11 in the database gates job when their package is selected, and does not reject a
  skip there. A `full` CI run (a release, or a manual dispatch with `full: true`) rejects skips only
  for the roots a named gate selects: the migration roots in `.github/workflows/migrations.yml` (through
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
| Browser title and meta description | the `<meta name="description">` and `<title>` tags of `gocrm-ui/index.html` |
| App name in the UI | one hook, `useProductName` (`gocrm-ui/src/hooks/useProductName.ts`), used by the layout, the printable quote and the sign-in and registration pages. The PDF render page (`/quote-render`, #384) has no session and does not use the hook: it takes the stored name from `POST /quotes/public/render` (`product_name`) and falls back to the build-time name (`buildTimeProductName`). Order: the stored `general.company_name` when a signed-in user's configuration holds a non-blank value, else `VITE_APP_NAME` (`VITE_APP_NAME=` in `gocrm-ui/.env.example`), else `GoCRM` (`FALLBACK_PRODUCT_NAME`). The sign-in and registration pages render before sign-in and always show the build-time name |
| Settings fallback name | `DEFAULT_COMPANY_NAME` in `gocrm-ui/src/contexts/ConfigurationContext.tsx` |
| Favicon (the only logo asset) | `gocrm-ui/public/favicon.svg` |
| Colours | the MUI palette in `gocrm-ui/src/theme/index.ts` (`createAppTheme`); the light theme's primary colour follows the `ui.theme.primary_color` setting for signed-in users |
| Mail subjects and body | the product name in the password reset subject and body, the quote subject and the ticket reply subject comes from `general.company_name` through `service.ResolveMailProductName` (`internal/service/product_name.go`), else `GopherCRM`, and is configurable in Settings; the templates stay in `internal/mailer/smtp.go` (`SendPasswordReset`, password reset), `internal/service/quote_email.go` (`QuoteEmailSubject`) and `internal/service/ticket_comment_email.go` (`TicketCommentEmailSubject`) |
| API title | `// @title` in the general API block above `main` (`cmd/main.go`), then `make swagger` |
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
  keeps the stored value (`configurationRepository.BulkUpsert` (`internal/repository/configuration_repository.go`)). Change existing
  installs through Settings or the configuration API.

### 3.2 Identity (defer)

These are safe to keep. Renaming any of them costs a conflict on every upstream merge:

- the Go module path `github.com/florinel-chis/gophercrm` (the `module` line of `go.mod`; section 7.2);
- the binary `bin/gophercrm` (`Makefile`), the Docker user `gophercrm` (`Dockerfile`), the systemd
  service and `/srv/gophercrm` paths (the `ROOT=${GOPHERCRM_ROOT:-/srv/gophercrm}` block of `scripts/deploy/receive-release.sh`, overridable by
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
| API-key prefix `gcrm_` | `apiKeyService.Generate` (`internal/service/apikey_service.go`), `HashAPIKey` and `HashAPIKeyHMAC` (`internal/utils/crypto.go`) | breaks keys already issued |
| `.gcrm-form`, `gcrm-form-styles`, `--gcrm-*` | `internal/handler/assets/form_embed.js` | breaks CSS that host pages wrote against the embedded form |

### 3.3 Repository identity

Change these before your first release or before running the tooling against your repository:

- `REPO` (the `REPO =` assignment in `scripts/release/release.py`) — the release script creates, downloads and publishes
  releases on this repository.
- the `*` owner line of `.github/CODEOWNERS`.
- README badges and links in `README.md` (the CI and latest-release badges, the two `git clone` commands and the Releases link) and the compare links at the end of
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

`Load` in the "Read in" column is `internal/config/config.go` `Load`, which reads every variable once at
startup.

| Variable | Default | Read in | Notes |
|---|---|---|---|
| `JWT_SECRET` | none (required) | `Load` | at least 32 characters; replace the example (section 2.3) |
| `API_KEY_SECRET` | `JWT_SECRET` | `Load`, `formTokenSecret` (`internal/service/form_service.go`) | see the rotation callout below |
| `DB_DRIVER` | `mysql` | `Load` | `mysql` or `sqlite`; anything else stops startup |
| `DB_PATH` | `gophercrm.db` | `Load` | SQLite only; no `?`; local filesystem (WAL) |
| `DB_HOST/PORT/NAME/USER/PASSWORD` | `localhost`/`3306`/`gocrm`/`root`/empty | `Load` | MySQL only |
| `SERVER_HOST`, `SERVER_PORT` | every interface, `8080` | `Load` | `127.0.0.1` behind a proxy on the same host |
| `SERVER_MODE` | `development` | `Load` | `production` switches Gin to release mode (`setupRouter` (`cmd/main.go`)) and refuses to start with `DISABLE_RATE_LIMIT=true` (`CheckRateLimitSwitch` (`internal/middleware/rate_limit.go`)) |
| `TRUSTED_PROXIES` | trust none | `Load` | see below |
| `CORS_ALLOWED_ORIGINS` | none beyond the localhost list | `Load` | every browser origin of your UI |
| `APP_BASE_URL` | `http://localhost:5173` | `Load` | the UI origin; password-reset links, the client response link of a quote email (`/quote-response?token=`) and the page the server's browser opens to print a quote to PDF (`/quote-render`), so with PDF output on it must be reachable from the backend host; also the single sign-on redirect URI `APP_BASE_URL/auth/callback` and post-logout redirect URI `APP_BASE_URL/login` |
| `QUOTE_PDF_CHROME_PATH` | empty (PDF output off); `/usr/local/bin/quote-pdf-browser` (a launcher of `chrome-headless-shell`) in the Docker image, empty again in the two development Compose files | `Load` | the browser binary that prints quotes to PDF, `chrome-headless-shell` or a full Chrome or Chromium; needs a font, a reachable `APP_BASE_URL`, and the `APP_BASE_URL` origin in `CORS_ALLOWED_ORIGINS` when the API is on another origin (`.env.example`, [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#quote-pdf-output)) |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | `Load` | this backend's public origin; form confirmation links, the SCIM base URL `PUBLIC_BASE_URL/scim/v2` (outside `API_PREFIX`; the proxy must forward `/scim/v2/`) and each SCIM user's `meta.location` |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM` | empty, 587, empty, empty, `no-reply@localhost` | `Load` | empty host means log-only mail |
| `RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY` | empty | `Load` | both or the layer is skipped |
| `FORM_RETENTION_DAYS` | off | `Load` | age in days after which spam and never-confirmed submissions with no linked lead are permanently removed; 3-day floor, 36500 ceiling. The first boot with it set removes the existing backlog; safe on every replica |
| `AUTH_AUDIT_RETENTION_DAYS` | `90` | `Load` | days a sign-in audit row (`auth_audit_log`, #454) is kept; unset, empty, zero, negative or unparseable keeps 90, 36500 ceiling. Pruned at boot and every 15 minutes by the stale bulk operation sweep; safe on every replica |
| `OIDC_ENABLED`, `OIDC_ALLOW_PASSWORD`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_SCOPES`, `OIDC_ROLE_MAPPING` | SSO off, password sign-in for linked accounts off, scopes `openid profile email`, the rest empty | `loadSSOConfig` (`internal/config/sso.go`) | fallbacks behind Settings → Single sign-on, which wins per field; register `APP_BASE_URL/auth/callback`, `APP_BASE_URL/login` and the front-channel logout URL `<API origin>/api/v1/auth/oidc/frontchannel-logout` with the identity provider ([single sign-on](user-guide/permissions-access/single-sign-on.md#values-to-exchange-with-the-identity-provider)) |
| `OIDC_GRAPH_MAX_PAGES` | `10` | `loadSSOConfig` (`internal/config/sso.go`) | environment only; Microsoft Graph pages read per Entra ID sign-in with group overage, 1 to 50 |
| `OIDC_ENTRA_APP_SIGNING_KEYS` | `false` | `loadSSOConfig` (`internal/config/sso.go`) | environment only; Entra ID app registrations with claims mapping |
| `SCIM_RATE_LIMIT_PER_MINUTE`, `SCIM_RATE_LIMIT_BURST` | `1500`, `150` | `loadSCIMConfig` (`internal/config/scim.go`) | environment only; requests per SCIM token (maximum 6000 and 1000) |
| `AUTOMATION_ENABLED` | `true` | `Load` | true on any number of instances sharing one MySQL/MariaDB database (#237); one process on SQLite |
| `AEO_SCHEDULE_ENABLED`, `AEO_SCHEDULE_HOUR` | `true`, `6` | `Load` | daily AEO run |
| `AEO_EGRESS_ALLOWED_ORIGINS`, `AEO_EGRESS_LOCAL_ORIGINS` | empty | `Load` | environment only; see `docs/AEO_SECURITY.md` |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `json` | `Load` | SQL statements only at debug/trace |

The AEO egress policy, the schedule and the custom engine (`AEO_CUSTOM_*`) can be set only in the
environment. `docs/AEO_SECURITY.md` explains why. So can `OIDC_GRAPH_MAX_PAGES`,
`OIDC_ENTRA_APP_SIGNING_KEYS`, `SCIM_RATE_LIMIT_*` and `AUTH_AUDIT_RETENTION_DAYS`; the other
`OIDC_*` variables are fallbacks behind Settings → Single sign-on.

### 4.2 Choices that are irreversible or easy to get wrong

> - **Secrets.** `API_KEY_SECRET` (or `JWT_SECRET` when it is unset) keys the HMAC of API keys
>   (`setupDependencies` (`cmd/main.go`), passed to `NewAPIKeyService`), refresh and password-reset tokens (passed to
>   the auth service in `setupDependencies`), form confirmation tokens and time-trap signatures
>   (`formTokenSecret` (`internal/service/form_service.go`)), and the encryption of sensitive settings
>   (the `configuration-secret` box built in `setupDependencies`, `internal/utils/secretbox.go`). Rotating
>   it invalidates every API key, refresh token, reset link and pending form confirmation, and
>   leaves stored AEO provider keys unreadable (they are treated as unset). The same secret, in
>   `setupDependencies`, also keys:
>   - SCIM tokens (`NewSCIMTokenService`): after a rotation every SCIM token is refused; create
>     new ones in Settings → SCIM tokens and give them to the identity provider;
>   - webhook signing secrets (`webhookBox`, sealed with `webhook.WebhookSecretContext`);
>   - the stored single sign-on client secret, client certificate and private key, and role
>     mapping (the `configuration-secret` box): an unreadable value counts as not set, an
>     unreadable client certificate makes the client secret apply, and an unreadable role mapping
>     becomes empty;
>   - pending quote response links (`quoteTokenSecret`), which stop working.
>
>   Rotating `JWT_SECRET`
>   invalidates every issued access token, but when `API_KEY_SECRET` is set separately and left
>   unchanged, refresh tokens stay valid (they are hashed with it,
>   `authService.sessionTokenSecret` (`internal/service/auth_tokens.go`)) and the UI silently trades them for new access
>   tokens signed with the new secret (the 401 handling in `setupInterceptors` and `refreshFor`, `gocrm-ui/src/api/client.ts`). To end every session,
>   rotate `API_KEY_SECRET` as well, with the consequences above. Per user, logout, a password
>   change, a password reset and a deactivation revoke that user's refresh tokens and end the access
>   tokens issued before them (they increment `users.token_version`, #236); a password change, a
>   password reset and a deactivation also revoke every API key of the user (#526), and a
>   reactivation brings none of them back. Restoring a database backup needs the secrets it was written with; a backup also brings
>   back older `token_version` values, so access tokens ended after it was taken pass again until
>   they expire.
> - **`SERVER_MODE=production`.** Without it Gin runs in debug mode. With it the server refuses to
>   start while `DISABLE_RATE_LIMIT=true` is set in production (environment or `.env`), so the
>   test-only switch cannot lift the `/auth` tier of a deployment. The only cookie the application
>   sets is the short-lived sign-in cookie of single sign-on (`gophercrm_oidc_state`, #528), which
>   carries no credential, and authentication is header-only.
> - **`TRUSTED_PROXIES`.** The rate limiter keys on `c.ClientIP()`
>   (`rateLimiter.middlewareWith` (`internal/middleware/rate_limit.go`)). Behind a reverse proxy or CDN with no trusted
>   proxies, every client shares the proxy's address and therefore one rate-limit bucket: one
>   noisy user locks out everyone on `/auth`. An IPv6 client is counted per /64 network
>   (`rateLimitClientKey`), so the addresses of one IPv6 /64 share a bucket. SCIM requests without
>   a valid token are limited per client address too.
> - **`CORS_ALLOWED_ORIGINS`.** Missing origins fail silently in the browser (section 2.3).
> - **`PUBLIC_BASE_URL` versus `APP_BASE_URL`.** The first is the API's public origin, used in form
>   confirmation links and the SCIM base URL `PUBLIC_BASE_URL/scim/v2`; the second is the UI's,
>   used in password-reset links, in the client response link of a quote email and in the single
>   sign-on redirect URI `APP_BASE_URL/auth/callback` and post-logout redirect URI
>   `APP_BASE_URL/login`. The front-channel logout URL is the API origin followed by
>   `/api/v1/auth/oidc/frontchannel-logout`. Left at their
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
  (`TicketHandler.Create` (`internal/handler/ticket_handler.go`)).
- `security.session_timeout_hours` — the access-token lifetime comes from `JWT_EXPIRY_HOURS` (default 24, `Load` (`internal/config/config.go`), used in
  `authService.GenerateJWT` (`internal/service/auth_tokens.go`)) and the refresh-token lifetime from
  `REFRESH_TOKEN_EXPIRY_DAYS` (default 30, `Load`). `JWT_ACCESS_TOKEN_MINUTES` is read
  (`Load`) but used by nothing.

The company name and colour settings are covered in
section 3.1.

### 4.4 Module switches

Only two exist: `AUTOMATION_ENABLED` (read in `Load`) turns the automation worker off on an
instance, and `AEO_SCHEDULE_ENABLED` (read in `Load`) turns off the daily AEO run. Forms and AEO
as a whole have no switch; removing a module is described in [EXTENDING.md](EXTENDING.md).
`FORM_RETENTION_DAYS` (read in `Load`) is the opposite kind of setting: the form retention purge
stays off until you set it.

## 5. Deployment shapes

### 5.1 Docker Compose on one host

`docker-compose.yml` (MySQL 8, API, UI) and `docker-compose.sqlite.yml` (API, UI) are tuned for a
local machine. Before you put either on the internet:

- They do not forward SMTP, `PUBLIC_BASE_URL`, reCAPTCHA, `AUTOMATION_*`, `AEO_SCHEDULE_*`,
  `OIDC_*`, `SCIM_*` or `AUTH_AUDIT_RETENTION_DAYS` variables to the backend, and they set `APP_BASE_URL` and `CORS_ALLOWED_ORIGINS` to
  `localhost:${UI_PORT}` (`docker-compose.yml:41-65`, `docker-compose.sqlite.yml:25-48`). Apply
  the committed `docker-compose.production.example.yml` with a second `-f` and supply your real
  values from the shell or the repo-root `.env`; [DOCKER.md](DOCKER.md) ("Production override
  example") lists what it forwards and the values it requires. Single sign-on and SCIM settings go into
  your own override ([DOCKER.md](DOCKER.md), "Single sign-on and SCIM").
- Both files switch quote PDF output off (`QUOTE_PDF_CHROME_PATH: ""`), because their
  `APP_BASE_URL` is not reachable from the backend container. The production example does not
  forward the variable, so PDF output stays off under it too; switch it on in your own override
  ([DOCKER.md](DOCKER.md), "Quote PDF output").
- The UI image bakes `VITE_API_BASE_URL=/api/v1` and `VITE_APP_NAME`, empty by default, which
  shows `GoCRM` before sign-in (`gocrm-ui/Dockerfile:19-22`). Set the name with
  `--build-arg VITE_APP_NAME=...` (see [DOCKER.md](DOCKER.md)); the shipped Compose files forward
  `VITE_APP_NAME` from the shell or the repo-root `.env` through `build.args` (#308).
- Put TLS in front, and close the plain-HTTP ports behind it. Passwords and bearer tokens cross
  plain HTTP in clear text, and both files publish the backend's `8080` and the UI's `UI_PORT` on
  every host interface (`docker-compose.yml:66-67,83-84`, `docker-compose.sqlite.yml:51-52,68-69`),
  so a proxy alone leaves both reachable without TLS. `docker-compose.production.example.yml`
  drops the backend publication and binds the UI to `127.0.0.1` (Compose 2.24.4 or later; an
  older Compose accepts the file but keeps the all-interfaces UI publication);
  [DOCKER.md](DOCKER.md) ("Several installations on one host") explains the syntax.
- The SQLite stack is two containers with a fixed project name (`gophercrm-sqlite`), fixed host
  port 8080 and a fixed subnet (`docker-compose.sqlite.yml:20,51-52,74-81`). Several installs on
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
directory `/srv/gophercrm` (bootstrap only re-owns it: `chown root:root /srv/gophercrm` in
`scripts/deploy/bootstrap.sh`); the tools `flock`, `rsync`, `mysqldump`, `gzip`, `curl`, `tar` and
`systemctl` (checked by its `for tool in` loop); a systemd unit for the service (`gophercrm` by
default) that runs `/srv/gophercrm/bin/gophercrm` (a link into the current release, `link_binaries`
in `scripts/deploy/receive-release.sh`) with the environment file `/srv/gophercrm/env/gophercrm.env`
(the `ENV_FILE=` default there, restarted by `restart_and_check`); and a TLS-terminating web server that serves the web root and proxies
`/api/v1/`, `/health` and `/scim/v2/` to the backend. [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) walks a fresh
systemd host through each of them, with a sample unit, environment file and the sites for nginx or
Apache 2.4 ([Reverse proxy and TLS](SERVER_PROVISIONING.md#reverse-proxy-and-tls), #231, #749); no sample file ships under `scripts/deploy`. On
your machine: root SSH access to the server, a `gh` login with admin rights on the repository,
and `main` protected (bootstrap refuses otherwise: its header comment and the
`main is not a protected branch` check).

1. Set `REPO` (the `REPO =` assignment in `scripts/release/release.py`) to your repository.
2. Run `scripts/deploy/bootstrap.sh <user@host> <public URL> <web root>` once. It creates a deploy
   key pinned to `scripts/deploy/receive-release.sh` on the server, a GitHub `production`
   environment, and the four secrets `PRODUCTION_SSH_KEY`, `PRODUCTION_SSH_KNOWN_HOSTS`,
   `PRODUCTION_HOST` and `PRODUCTION_SSH_USER` (the `Check that production is configured` step of
   `.github/workflows/deploy.yml`). It finds the
   repository through `gh repo view`.
3. On the server, `receive-release.sh` reads `GOPHERCRM_ROOT`, `GOPHERCRM_SERVICE`,
   `GOPHERCRM_WEB_ROOT`, `GOPHERCRM_ENV_FILE` and other overrides (the `ROOT=${GOPHERCRM_ROOT:-/srv/gophercrm}`
   block). Its pre-restart backup runs `mysqldump` (`backup_database`), so this path is not ready for SQLite as shipped.
4. Prepare a release with `python3 scripts/release/release.py prepare --version X.Y.Z`, merge it,
   run the **Release** workflow with `publish=false` (a full rehearsal that creates nothing), then
   with `publish=true`. **Deploy** runs after a successful published release.
   `docs/VALIDATION_RELEASE.md` ("Prepare, rehearse, publish") has the detail.

### 5.3 Bring your own deployment

Use the Release workflow for the artifact only, or build it yourself (`make build` plus
`npm run build` in `gocrm-ui/`). `deploy.yml` triggers on every successful Release run; it skips
with a notice while its secrets are missing (the `Check that production is configured` step of
`.github/workflows/deploy.yml`), but disable the
workflow if you never intend to use it.

### 5.4 Worker topology

The automation worker is replica-safe on MySQL/MariaDB (#237): every replica may keep
`AUTOMATION_ENABLED=true`. Each run claim records the engine's instance id and a heartbeat it
renews every 30 s; every write after the claim (step advance, release, both email transactions and
a check just before the send) is fenced on that owner, and a stale sweep at boot and at every tick
recovers a run whose heartbeat is older than 3 minutes, so a run of a crashed replica is resumed by
another without a restart and a live run is never touched. Replica clocks must agree to well under
90 s, and an upgrade stops every replica before starting the new version (README "Known
Limitations"). Rate limits are per process, so they are not shared between replicas. The form
retention purge (`FORM_RETENTION_DAYS`) is also safe on every replica, because extra sweeps only
compete for the same rows.

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
  and generate `JWT_SECRET` at runtime (the `Generate a per-run JWT secret` step of the `e2e` job in
  `.github/workflows/ci.yml`). Deploy secrets are needed
  only for section 5.2.
- **Keep the `gophercrm_runner` Python package.** Ordinary CI imports it:
  the `gophercrm_runner` imports at the top of `scripts/ci/check-auth.py`, `scripts/ci/go_checks.py`
  (used by `check-go.py`), `scripts/ci/check-go.py`, `scripts/ci/check-changed-go.py` (through
  `go_checks.py`) and `scripts/ci/plan-documentation.py`, and `main` in
  `scripts/ci/plan-validation.py`, which reads `validation.py` from the base commit. You may
  drop the Langflow graphs (`automation/langflow/flows/`) and the runner runtime, but not
  `automation/langflow/gophercrm_runner/`. `.github/workflows/remediation-runner.yml` is also a
  reusable workflow that Release calls as its `runner-validation` job
  (`.github/workflows/release.yml`), and the `build` job lists that job in its `needs:`. Deleting
  the workflow means removing that job and its entry in `needs:` from `release.yml` in the same
  change, or Release stops working.
- **Profile selection.** The `plan` job picks one of nine validation profiles from the changed
  paths (the `profile = (...)` expression of `plan` in
  `automation/langflow/gophercrm_runner/validation.py`). To see what CI will run for
  your change, ask the selector instead of guessing:

  ```bash
  python3 automation/langflow/gophercrm_runner/validation.py \
    --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
  ```

  For the runner's local validation, shared code, schema, dependency and API-contract changes fall
  back to `full` (`make verify` plus the whole browser matrix). CI does not: outside a release every
  profile runs the targeted Go tests (`scripts/ci/check-changed-go.py`), and the race detector, both
  engines of the migrations workflow and the browser matrix run only when CI is called with
  `full: true`. A pull request that changes migration code runs the MySQL 8 migration leg alone.
  `docs/VALIDATION_RELEASE.md` has the profile table.
- **Simplifying.** If you do not want contextual selection, make the `plan` job always emit
  `profile=full`, which is what `main` in `scripts/ci/plan-validation.py` writes for a release when it
  has no trusted selector (the `no trusted selector/change base` branch). Do not delete the planner import chain without replacing it.
- **The hygiene hook.** It runs only after `git config core.hooksPath scripts/git-hooks`
  (the `Activate with:` comment of `scripts/git-hooks/pre-commit`). It refuses new folders under `docs/` other than `img`,
  `screenshots`, `testing` and `user-guide` (`ALLOWED_DOCS_DIRS`), and two reserved working-paper paths (the
  case that refuses docs/specs and docs/plans); keep or edit those rules as your own policy. The forbidden-terms sweep runs only when the untracked
  terms file named by `terms_file` exists, so on a fresh clone it is skipped. The size check is the
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
imports, the path is hard-coded in `scripts/ci/check-go.py` (`KNOWN_SKIPS`), `scripts/release/check-sql-tests.py` (the
`required` set of `check`),
`scripts/release/tests/test_release.py` and `scripts/release/tests/test_sql_coverage.py`. Deriving
it from `go.mod` instead is #223.

### 7.3 Conflict hot spots, and seams that avoid them

| Hot spot | Seam |
|---|---|
| `setupDependencies` (`cmd/main.go`) | call one fork function from it, in its own file under `cmd/` |
| `internal/handler/routes.go` | a `fork_routes.go`, the way `aeo_routes.go` and `form_routes.go` are split out |
| the AutoMigrate list in `internal/models/database.go` `autoMigrateModels` | append your models at the end, in one block |
| `gocrm-ui/src/routes/index.tsx` and the navigation in `gocrm-ui/src/layouts/MainLayout.tsx` | group fork routes and nav items in one block each |
| seeded settings in `internal/models/configuration.go` | a fork-specific key prefix, appended at the end |
| `CHANGELOG.md` | a separate fork section, or a separate file |
| `api/swagger.json`, `api/swagger.yaml` | never hand-merge: take either side, then run `make swagger` |
| the four version stamps (`cmd/main.go` `@version`, `gocrm-ui/package.json`, both swagger files) | use your own version line and re-stamp after a merge |

[EXTENDING.md](EXTENDING.md) shows each seam in a worked example.

### 7.4 Migration versions

`migrations/` is applied by golang-migrate (`main` (`cmd/migrate/main.go`), through `migrate.NewWithDatabaseInstance`) on MySQL and MariaDB.
Versions are integers: `go run ./cmd/migrate -command create -name <name>` takes the newest
version in `migrations/` + 1 (only an empty directory starts from the current UTC time as
`YYYYMMDDHHMMSS`), and the `cmd/migrate` tests require every version to be unique, paired and
increasing. A plain `up` does not apply a migration numbered below the
newest version the database has already applied, so an upstream migration older than your newest
fork migration would be silently skipped on an existing database. The procedure:

1. Merge upstream first.
2. Give fork migrations versions above upstream's newest.
3. Before releasing, renumber any fork migration that has not yet shipped if a merge brought in a
   newer upstream one.
4. Rehearse a populated-database upgrade. The `cmd/migrate` tests and
   `.github/workflows/migrations.yml` (MySQL 8 and MariaDB 10.11) run fresh install, populated
   upgrade, rollback and reapply; they need `TEST_MYSQL_DSN` locally.

New tables use `CREATE TABLE IF NOT EXISTS`, because a new binary may already have created them
through AutoMigrate before the migration runs
(the header comment of `migrations/20261026120000_add_products_and_quotes.up.sql`). SQLite installs never run
`migrations/`; their schema comes from AutoMigrate at startup. Every schema change needs both
halves, as [EXTENDING.md](EXTENDING.md) explains.

### 7.5 Security fixes

Watch the `### Security` headings in `CHANGELOG.md` and issues with the `security` label
(`gh issue list --repo florinel-chis/GopherCRM --label security --limit 200`).

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
5. **Your CI is green** on a pull request in your repository. The `migrations` job runs
   there only when the pull request changes migration code, and then on MySQL 8 alone; run CI by
   hand with the `full` input once to exercise both engines and the race detector.
6. **A restored backup.** Restore last night's backup into a scratch install with the same
   secrets, start it, and sign in.
7. **An upstream merge rehearsal.** `git merge upstream/main` on a branch, `make verify`, and the
   selector command from section 6 on the merge commit.

## 9. Why fork instead of rebuild

Measured on `98dabb04`. Commands:
`git ls-files '*.go' | grep -v _test.go | xargs cat | wc -l` (and `| wc -l` on the list for the files);
`git ls-files 'gocrm-ui/src/*.ts' 'gocrm-ui/src/*.tsx' | grep -vE '\.test\.' | xargs cat | wc -l`;
`git ls-files '*_test.go' | xargs cat | wc -l`;
`git ls-files '*_test.go' | xargs grep -hE '^func Test[A-Za-z0-9_]*\(' | wc -l`;
`git ls-files 'gocrm-ui/src/*.test.ts' 'gocrm-ui/src/*.test.tsx' | wc -l`;
`git ls-files 'gocrm-ui/e2e/tests/*.spec.ts' | xargs grep -hE '^\s*test(\.(only|skip|fixme))?\(' | wc -l`;
the `paths` object of `api/swagger.json` (paths, and their `get`/`post`/`put`/`patch`/`delete` operations);
the Total row of `docs/testing/README.md`;
`ls migrations/*.up.sql | wc -l`;
`ls .github/workflows/*.yml | wc -l` and, per workflow, `sed -n '/^jobs:/,$p' <workflow> | grep -cE '^  [A-Za-z0-9_-]+:$'`;
the profiles `plan` (`automation/langflow/gophercrm_runner/validation.py`) can return;
`gh pr list --state merged --limit 500` and `gh issue list --state closed --limit 500` for the history.

- **Application code:** 93,660 lines of Go in 393 files and 37,652 lines of TypeScript/TSX in 205
  files, covering 137 API paths and 193 operations (`api/swagger.json`), four system roles plus
  custom roles, and leads, customers, companies, deals, products and quotes, tickets, tasks, forms,
  follow-up automation, AEO tracking, GDPR erasure, single sign-on, SCIM provisioning, outbound
  webhooks, roles and permissions, and the sign-in and role audit.
- **Tests:** 203,292 lines of Go tests in 617 files, more test code than application code. They
  include 2,160 top-level `Test` functions, 176 Vitest files and 35 Playwright specs with 208
  browser tests, run against MySQL 8, MariaDB 10.11 and SQLite (`.github/workflows/ci.yml`). The
  manual catalog in `docs/testing/` documents 861 cases.
- **Schema:** 46 SQL migration pairs, kept in parity with GORM AutoMigrate and checked by a
  migrations workflow for fresh install, populated upgrade, rollback and reapply.
- **Delivery machinery:** 6 GitHub workflows with 17 jobs; 9 validation profiles that pick the
  checks from the diff; a no-skip SQL coverage gate; one e2e driver used identically locally and
  in CI; a release pipeline that rehearses before it publishes.
- **History (as of 2026-10-31, not re-counted on the measured commit):** 129 merged pull requests
  between 2025-11-15 and 2026-10-31, and 58 closed issues. They include all 27 tickets of an
  architecture audit (tracking issue #94), each recording its delivering pull request and
  validation evidence. For example, #102 (single-use password-reset claims, PR #145) records six
  SQLite/MySQL regression roots and 36 subtests passing without skips.

What this buys you is mostly not lines of code. Many of those pull requests fixed defects a
rebuild would probably repeat, and each fix has a regression test that fails without it:

- transactions that leaked connections or did not roll back on a panic (#106, PR #141);
- single-use tokens that could be spent twice or lost on a failure (#101, #102, #103);
- a CSV export that silently lost rows under a custom sort (#105, PR #150);
- list pages without a total order for tied sort values (#110, PR #177).

What it does not buy you, stated plainly:

- **Open issues:** the fork inherits them (section 1.3).
- **Changelog:** the latest release is 1.4.0; newer work is under `[Unreleased]` (section 1.4).
- **MySQL test server:** the regression tests need one before a local run proves the MySQL half.
- **CI coupling:** CI depends on a Python package in `automation/langflow/` (section 6).
- **Reading time:** the conventions are dense enough that your first change will take a while.

The cost of rebuilding this from scratch has not been measured, so this guide makes no claim
about saved hours. The numbers above are what exists, and each of them can be checked with the
commands listed.
