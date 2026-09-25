# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.2.0] - 2026-09-25

### Added

- **SQLite backend.**
  - `DB_DRIVER` selects `mysql` (the default) or `sqlite`, and `DB_PATH` names the SQLite file. An
    empty `DB_PATH`, or one containing `?`, stops startup.
  - SQLite runs on a pure-Go driver with WAL, foreign keys and a busy timeout. Shutdown never
    hangs on a stuck query, transactions follow the request context, and lock errors are retried.
  - The test suites run on the same driver that ships.
  - `docker-compose.sqlite.yml` runs the backend and UI without a MySQL service; `docs/DOCKER.md`
    covers backups.
- **Profile and API Keys settings pages.**
  - **Profile** shows the signed-in account and changes its password. Changing it revokes every
    refresh token, so no session can be renewed; access tokens already issued stay valid until
    they expire.
  - **API Keys** lists your keys, creates new ones (the key is shown exactly once, behind a copy
    button) and revokes them in place.
- `/health` reports `revision`, the commit the running binary was built from.
- **Automated production deploy** (`.github/workflows/deploy.yml`, `scripts/deploy/`).
  - When CI passes on `main`, the tested commit is built and shipped over SSH to a single forced
    command on the server.
  - That command backs up the database, switches releases atomically, waits for `/health` to
    report the new revision, and rolls back on its own if it does not.
  - `scripts/deploy/bootstrap.sh` sets up a server once. Manual rollback and status checks run from
    the Actions tab.
- **Continuous integration on every pull request.**
  - Checks: repository hygiene, backend build/vet/race tests, frontend build/lint/tests, and the
    Playwright end-to-end suite on both MySQL 8 and MariaDB 10.11.
  - `make verify` runs every CI gate except the end-to-end suite. `make e2e` runs that suite
    locally against a throwaway `*_e2e` database on free ports.

### Changed

- **A lead needs either an email address or a phone number.**
  - Before, the API required an email and the lead form required both. A lead with neither gets
    a 400.
  - API consumers can now receive leads with an empty `email`.
  - A lead without an email can't be converted to a customer (customers need one). The conversion
    answers 400 and says to add an email first.
  - Update treats an empty field as untouched.
- **Forms:** when `first_name` and `last_name` are both absent, a single `name` field is split
  onto them, so leads are no longer named `Form <email local part>`. The `name` value is no longer
  copied into the lead's notes.
- **Form definitions** report each field's `max_length` capped at the width of the lead column it
  maps to: email 255, first and last name 100, phone 50, company 200, position 100. Existing forms
  that declared more publish the lower value. Field names and options do not change.
- The AEO documentation screenshots use demo data with GopherCRM as the tracked brand.

### Removed

- `DB_SSL_MODE`. It was read into the configuration but never used.

### Fixed

- **API keys could not be created on MySQL or MariaDB.** The hash column was too narrow and the
  insert failed. `api_keys.key_hash` is now `varchar(128)`.
- **Lead conversion:**
  - a lead whose email already belongs to a customer answers 409 instead of 500;
  - the lead page shows the server's message when a conversion is refused.
- **Form submissions:**
  - a value wider than the column it is stored in is rejected with a field error ("X must be at
    most N characters long") instead of failing with a 500 on MySQL and MariaDB;
  - a pending double opt-in whose stored data holds such a value is refused at confirmation;
  - the raw submission is stored as `MEDIUMTEXT`, so large multi-field submissions fit.
- **Lead notes** are capped at 65,535 bytes, the width of the column.
  - Create and update reject longer notes with a 400.
  - Repeated form submissions still append to a lead's notes. Each appended block is capped at
    16 KiB, marked when cut, and closed by an end line.
  - When space runs out, only complete form blocks are dropped, oldest first. Every other byte is
    kept wherever it sits: staff notes before, between or after blocks, and blocks written before
    this release. Public submissions can therefore never erase what staff wrote.
  - If the kept text leaves no room, a one-line pointer is appended instead, or nothing when not
    even that fits.
  - The full submission always stays with its form.
- **AEO:**
  - a citation URL over 1,024 characters or a host over 255 is skipped and logged, instead of
    losing the whole recorded answer;
  - an engine whose configured name (`AEO_CUSTOM_NAME`, 40 characters) or model id
    (`AEO_*_MODEL`, 120 characters) does not fit its column is left out of runs, with an error log
    naming the setting.
- **Configuration seeding** failed on MySQL (`NO_ZERO_DATE`, Error 1292) for older rows with no
  `created_at`. The whole seeding transaction rolled back on every boot, so new defaults were
  never inserted.
- **Lists:** the sort order survives refetches, and descending order works again. Columns the
  backend cannot sort no longer offer sorting.
- **Rendering errors:** an error in one route shows a recoverable fallback instead of blanking the
  app. Missing or invalid dates render as a placeholder instead of crashing the page.
- **Dependencies:** `@mui/utils` is declared as a direct dependency, so strict installers such as
  pnpm resolve it (#54).

### Upgrade notes

- **Schema changes.** Auto-migration at startup widens two columns on MySQL and MariaDB:
  `api_keys.key_hash` becomes `varchar(128)` and `form_submissions.data` becomes `MEDIUMTEXT`.
  - Existing rows and the unique index are kept; this was verified on MySQL and on MariaDB 10.11.
  - Take a database backup first. The automated deploy does this for you.
  - If you apply SQL by hand, `migrations/20260923120000_widen_api_key_hash.{up,down}.sql` covers
    `key_hash`. `form_submissions.data` is widened by auto-migration only.
- **Configuration.**
  - Remove `DB_SSL_MODE` from your environment if you set it; it has no effect.
  - An unknown `DB_DRIVER` value now stops the server at startup.
  - `cmd/migrate` refuses database commands under SQLite, where the schema comes from
    auto-migration.
- **Docker.** The image now runs in `/data`, which is also where `godotenv` looks for `.env`.

## [1.1.0] - 2026-08-12

### Added

- **Forms module.**
  - Build contact, quote-request and lead-capture forms in the CRM. Embed them on any website with
    one script tag, or share the hosted form page.
  - Submissions create leads, or match existing ones, automatically.
  - **Double opt-in:** the visitor confirms their email through a single-use link.
  - Layered spam protection: honeypot, minimum-fill-time challenge, per-IP rate limits, a per-form
    origin allowlist, and optional reCAPTCHA v3.
  - The embeddable widget has no dependencies, can be themed through CSS custom properties, and is
    served by the backend.
  - Erasing a lead also scrubs its linked form submissions and confirmation tokens.
- **API keys managed in Settings.**
  - AEO provider keys are encrypted at rest (AES-256-GCM), never echoed back by the API, and take
    effect without a restart. Environment variables remain the fallback.
  - The configuration system gained a generic mechanism for sensitive values.
- The AEO prompt-generation engine is selectable per deployment.
- A single AEO prompt can be run on demand from its drawer, including inactive drafts.

### Changed

- AEO answer transcripts render as markdown, with brand mentions still highlighted.
- AEO errors distinguish a missing key from a rejected one.

### Fixed

- Reasoning-class models get enough completion budget to finish their answers, instead of stopping
  mid-sentence.
- Gemini defaults to the rolling `gemini-flash-latest` alias, which fixes 404s from a retired
  pinned model.
- Boot-time configuration seeding no longer overwrites stored values with defaults.
- Public form endpoints answer cross-origin requests with credential-less CORS; every other route
  keeps the strict allowlist.

### Upgrade notes

- **No breaking changes.** Schema additions (the forms tables and configuration flags) apply
  through auto-migration at startup.
- **New optional environment variables:** `PUBLIC_BASE_URL`, `RECAPTCHA_SITE_KEY`,
  `RECAPTCHA_SECRET_KEY` and `RECAPTCHA_MIN_SCORE`. See `.env.example`.

## [1.0.0] - 2026-08-11

First tagged release. It shipped:
- **Answer Engine Optimization (AEO):** brand visibility tracking across six answer engines, with a
  dashboard, prompt transcripts and citation comparisons;
- **task labels**;
- **the Docker Compose stack**, with `CORS_ALLOWED_ORIGINS`.

The GitHub release describes these in full. The entries below were collected before the release
was tagged, and do not cover those three.

### BREAKING

- Deleting a **user, customer or lead** is now irreversible. `DELETE` overwrites every personal
  field on the row before soft-deleting it, so there is nothing left to restore. Use
  `is_active = false` to suspend an account without destroying its data. Deleting a ticket or a
  task is unchanged and remains an ordinary soft delete.
- The erased email address becomes reusable, because the original no longer exists in the table.
  Code that assumed a deleted account's address stayed permanently reserved will behave differently.
- `POST /auth/register` no longer honours a `role` field in the request body. Clients that relied on
  it to provision non-customer accounts must use the admin-guarded `POST /users` or the
  `create-admin` CLI.

### Added

- Right-to-erasure implementation (GDPR Art. 17) in `internal/repository/erasure.go` and
  `erasure_cascade.go`. Personal fields are overwritten in place and the row is soft-deleted in a
  single transaction; the row is kept so foreign keys from tickets and tasks still resolve.
- Replacement email addresses generated from `crypto/rand` in the reserved `.invalid` domain
  (RFC 2606). They are not derived from the original address, so they are neither reversible nor
  linkable.
- Cascading erasure between a converted lead and its customer, in both directions, via
  `leads.customer_id`. Wired through `repository.NewCustomerRepositoryWithLeadErasure`.
- Per-item `SAVEPOINT` isolation in bulk deletion, so a failing item neither commits a half-erased
  live record nor rolls back the rest of the batch. Erasure refuses to run when GORM's nested
  transactions are disabled.
- `scripts/anonymize_legacy_deleted_pii.sql` to remediate rows soft-deleted before erasure existed.
  Manual and irreversible by design; deliberately not wired into auto-migration.
- Test coverage for erasure, atomicity, cross-table PII sweep, bulk isolation and duplicate-email
  semantics: `test/integration/erasure_test.go`, `lead_erasure_test.go`,
  `erasure_atomicity_test.go`, `erasure_pii_sweep_test.go`, `bulk_erasure_test.go`,
  `bulk_erasure_isolation_test.go`, `soft_delete_email_reuse_test.go`.
- Handler unit tests for auth and API keys (`internal/handler/auth_handler_test.go`,
  `apikey_handler_test.go`) and a bulk persistence suite
  (`internal/service/bulk_operation_persistence_test.go`).
- Frontend tests for token storage, route protection and the registration password policy
  (`src/api/client.test.ts`, `src/components/ProtectedRoute.test.tsx`,
  `src/routes/index.test.tsx`, `src/pages/auth/Register.validation.test.tsx`).
- `gocrm-ui/e2e/global-setup.ts` provisions the E2E admin account through the `create-admin` CLI,
  since registration can no longer create an admin.
- **Auth session lifecycle.** Login now issues a rotating refresh token alongside the JWT;
  `POST /auth/refresh` exchanges it (strict rotation — replaying a used token returns 401);
  `POST /auth/logout` revokes the caller's refresh tokens; `POST /auth/change-password` verifies
  the current password and revokes all sessions; `POST /auth/password-reset` +
  `/password-reset/confirm` implement a single-use, 1-hour, anti-enumeration reset flow. Tokens are
  stored only as HMAC-SHA256 hashes. Mail goes through a new `internal/mailer` package — SMTP via
  `SMTP_*` env vars, with a redacting log fallback for development. Erasing a user also purges
  their password-reset tokens.
- **Dashboard analytics.** Eight new admin/sales/support endpoints: grouped counts for leads by
  status, tickets by priority and tasks by status; lead conversions over time
  (`sales-performance`, bucketed in Go so SQLite and MySQL agree); an activity feed synthesized
  from lead/ticket/task events; upcoming tasks; recent tickets; and new leads (scoped per role).
  Plus `GET /tasks/upcoming` for a forward due-date window.
- **Bulk status updates.** `POST /leads|tickets|tasks/bulk/status` (up to 100 ids,
  all-or-nothing in one transaction) wired onto the previously unrouted bulk machinery, with
  per-item authorization mirroring the single-item rules and failing responses that name the
  offending ids.
- **API key management.** `GET` and `PUT /api-keys/{id}` (rename, deactivate, reactivate), an
  optional `expires_at` on creation, and expiry enforced at authentication time.
- **Customer operations.** `GET /customers/export` streams a CSV of all matching customers
  (admin-only, spreadsheet-formula-injection-safe) and `POST /customers/{id}/assign` assigns a
  customer to an active admin/sales user via the new `assigned_to_id` column, which erasure
  deliberately leaves intact.
- A regenerable Swagger 2.0 spec at `api/swagger.json` / `api/swagger.yaml`, built by `make swagger`
  from swag annotations now covering all 43 routed operations. Status codes, permission rules and
  response shapes were derived from the actual code paths, including the unflattering ones (see the
  API-defects list in `docs/ROADMAP.md`).

### Changed

- API keys and refresh tokens belonging to an erased user are purged with the account.
- Deactivation (`is_active = false`) is documented and tested as the reversible alternative to
  deletion; it never touches personal data.
- Frontend tokens are stored in `sessionStorage` by default and only move to `localStorage` when
  "remember me" is selected.
- The registration form's validation now mirrors the server password policy: minimum 10 characters
  with an uppercase letter, a lowercase letter, a digit and a special character.
- The hand-written documentation moved from `doc/` into `docs/`; there is now a single
  documentation directory.
- `remember_me` is gone from the login request body. It was bound and never read — token lifetime
  comes solely from `JWT_EXPIRY_HOURS` — and the frontend's remember-me checkbox only ever chose
  the client-side storage location, which is unchanged. Clients still sending the field are
  unaffected (unknown JSON keys are ignored).
- `PUT /configurations/{key}` binds its value as `json.RawMessage`, making present-vs-absent
  structural: `false`, `0` and `""` are accepted (they already were, but only via a
  version-sensitive quirk of the validator's interface handling), while an absent or `null`
  value is rejected with an explicit 400.
- Password fields in register and user create/update requests declare `min=10` at binding time,
  matching the complexity rule that was already enforced afterwards. 8–9 character passwords were
  always rejected; they are now rejected at validation with an accurate message (and the generated
  spec no longer claims a minimum of 8).
- The old generated `docs/` Swagger package (`docs.go`, `models.go`, `swagger.json`,
  `swagger.yaml`) is gone, along with the direct `swaggo/swag` dependency — it was generated in
  March, imported by nothing, and documented removed behaviour (a client-supplied `role` on
  register, a CSRF token on login). The CLI now runs via `go run` pinned in the Makefile.
- Backend statement coverage is now 46.9% (measured with
  `-coverpkg=./internal/...,./cmd/...`), up from a previously reported 41.3%. The frontend suite is
  now 142 tests across 16 files, all passing, up from a previously reported 100 passing with 11
  failures. Backend tests are clean under `-race`.

### Fixed

- Wrong status codes for missing records, all rooted in string-compared errors or gorm's sentinel
  leaking through unclassified: `DELETE /api-keys/{id}` and `POST /configurations/{key}/reset`
  returned 500 instead of 404 for a missing key, and `PUT /users/{id}` / `PUT /users/me` returned
  500 for a nonexistent user. All three now classify with `errors.Is` sentinels.
- Database failures are no longer masked as 404 on `GET /customers/{id}` (and its update
  pre-check), `GET`/`PUT`/`DELETE /tickets/{id}`, `GET`/`PUT`/`DELETE /tasks/{id}` and
  `GET /configurations/{key}` — a genuine internal error now returns 500 instead of "not found".
- `apperrors.IsNotFound` now actually recognises a raw `gorm.ErrRecordNotFound`, which its doc
  comment had always claimed but its implementation never did — the sentinels share a message but
  not an identity. The per-file workaround helpers this forced are gone.
- Task lists now honour `limit` and `offset`. The handler read `page`/`per_page` while the
  frontend sends `page`/`limit`, so the frontend's page size was silently ignored and any
  `per_page` over 100 fell back to 20 instead of capping. Tasks now use the same
  `ParseOffsetLimit` + `page`-override parsing as every other list; `per_page` is no longer read
  anywhere and the dead `ParsePaginationParams`/`CalculateOffset`/`CalculateTotalPages` helpers
  are removed.
- Bulk create asserted a slice pointer to a slice and panicked on every call, so all bulk create
  paths failed; it also reported rows it had never inserted as successful.
- Editing a task returned 403 for non-admin users when the request merely echoed the task's current
  assignee.
- Duplicate emails now return 409 consistently. Customer creation and update previously returned
  400, and database driver text could leak into the error response.
- Admin routes defined both a static `element` and a `lazy` import, so React Router rendered a
  placeholder and the real Users and Configuration pages were unreachable.
- `make build-tools` wrote `bin/.create-admin` while `make create-admin` executed
  `bin/create-admin`, so the documented two-step sequence always failed.

### Security

- `GET /dashboard/stats` was the only route without a role guard: any authenticated account —
  including the `customer` role that public registration hands out — could read system-wide lead,
  customer, ticket and task counts. It now requires admin, sales or support; the frontend
  dashboard no longer requests stats for customer-role users.
- The sales role had unrestricted write access to every ticket (update, reassign, resolve),
  while support — the role tickets are actually assigned to — was limited to its own assignments.
  Sales is now read-only on tickets: `PUT /tickets/{id}` returns 403.
- `POST /auth/register` accepted a client-supplied role, allowing anyone to create an admin account
  and receive a valid token. The endpoint now always creates a `customer`.
- `GET /customers/:id/tickets` performed no ownership check, letting any authenticated user read
  another customer's tickets. A customer-role user may now only read their own.
- `?limit=0` reached the pagination arithmetic and panicked, turning every list endpoint into a 500
  via a single query parameter.
- An API key whose owner has been erased or deactivated is now rejected at authentication time.

### Known issues

- Application logs record the email address on login and on customer create/update, and issued JWTs
  embed it until they expire. Database erasure does not reach either, so log and token retention
  need their own policy.
- `internal/middleware/csrf.go` is implemented and unit-tested but never installed in `cmd/main.go`,
  so no route currently requires a CSRF token.
- `RateLimitGenerous` (240/min) is defined but never applied; two tiers are active, not three.
- The generic `/bulk/:resource` handlers in `internal/handler/bulk_handler.go` remain unrouted;
  only the entity-specific `bulk/status` endpoints are reachable over HTTP.
- An already-issued JWT cannot be revoked before it expires; logout and refresh rotation revoke
  refresh tokens only, and there is no access-token blocklist.
- Password-reset delivery requires SMTP configuration; without `SMTP_HOST` the reset link is only
  written (redacted) to the application log.
- ESLint reports 40 errors and 137 warnings in the frontend, mostly unused Playwright fixture
  arguments and `any` types. These pre-date this work and were not touched. `tsc -b` is clean.

[Unreleased]: https://github.com/florinel-chis/GopherCRM/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/florinel-chis/GopherCRM/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/florinel-chis/GopherCRM/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/florinel-chis/GopherCRM/releases/tag/v1.0.0
