# Roadmap

Unprioritized ideas that are **not** implemented today. This list is the salvaged remainder of two
pre-implementation build checklists (`tasks.md` and `uitasks.md`) that were removed once the
features they planned had shipped; only items verified as still missing are kept here. Shipped
functionality and its test coverage are tracked in [FEATURES.md](FEATURES.md).

## Frontend

- **Kanban board for tasks** — drag-and-drop status board alongside the existing task list
- **Saved filters** — persist per-user filter presets on the list pages
- **Reports and analytics** — sales pipeline, lead conversion rate over time, ticket metrics,
  user activity
- **Data export** — CSV/PDF export from the list views
- **Accessibility pass** — keyboard navigation and screen reader review against WCAG 2.1 AA
- **Row-0 page objects in the E2E suite** — `customers.page.ts` was reworked to act on the row
  matching a value unique to the test (`rowMatching`) and to address action icons through a
  retrying expectation. The leads, tasks, tickets and users page objects still carry the pattern it
  replaced: `rowIndex = 0` on edit/view/delete, so they act on whatever row happens to sort first
  rather than the record the test created, plus a one-shot `isVisible()` guard that falls through
  to a positional `button` fallback when the icon has not painted yet. Green today, latently flaky —
  port the customers approach across.
- **Vitest cases that time out on a loaded machine**
  - **The failures (2026-09-25, during `make verify` while the Go race tests and other work were
    running; load average up to 71):**
    - `src/routes/index.test.tsx` › "blocks non-admin users from /users via the pathless
      ProtectedRoute layout": the page rendered empty and `findByText` gave up after 1 s.
    - `src/pages/forms/FormBuilder.test.tsx` › "refuses a dropdown without options" and "lets an
      admin pick the lead owner from the user list" hit Vitest's 5 s test timeout.
  - **Not regressions:** each passes 5 of 5 alone, and `make verify-frontend` on a quiet machine
    passes 38 of 38 files.
  - **Fix options:** longer timeouts for these interaction-heavy tests, fewer Vitest workers in
    `make verify`, or run the frontend gate before the race-enabled Go tests.
- **Sortable columns the backend has no mapping for** — the tickets **Customer** /
  **Assigned To** and the tasks **Assigned To** header are deliberately non-sortable in the list
  pages: tickets/tasks allow only the `customer_id` / `assigned_to_id` foreign keys, which order
  by id rather than by the name the column renders. Restoring the affordance needs an allowlist design first — widen it with real
  orderable columns (or joined expressions) and map each UI column onto one, rather than letting
  the frontend send a column the validator rejects. The customers **Phone** header is the
  opposite case: it is offered as sortable and sends `sort_by=phone`, but the customer handler's
  own allowlist (`customerSortColumns`) lacks `phone`, so the request comes back unsorted
  (TC-CUST-031).

## Backend and delivery

- **Serve the OpenAPI spec** — the spec and a static HTML reference are generated into `api/`
  (`make swagger`) and gated in CI (`make verify-api-docs`); serving them from the backend (e.g. a
  Swagger UI route) is deliberately not done
- **Cross-process AEO coordination** — the AEO runtime now owns, cancels and joins its runs at
  shutdown within one process (#111), but the overlap guard and the daily scheduler are still
  process-local: two API replicas would each arm a scheduler and each admit a run. Horizontal
  scaling needs a database-level claim on `aeo_runs` and a single scheduling owner.
- **Extend the SQLite upgrade fixtures per release** — auto-migration
  (`models.MigrateDatabase`) is the only schema path when `DB_DRIVER=sqlite`;
  the SQL files in `migrations/` target MySQL. The v1.2.0 → next upgrade is
  covered by a fixture (`internal/models/testdata/sqlite-v1.2.0-schema.sql`
  and `database_sqlite_upgrade_test.go`: a populated file with referenced
  rows, and the half-applied state a failed first start leaves behind). Each
  release that changes the schema should add its own N-1 fixture, dumped from
  a fresh file of the previous release, so the chain keeps being exercised.
  Backing up before upgrading (see
  [DOCKER.md](DOCKER.md#upgrading-the-sqlite-database)) stays the rule.
- **Lead update cannot clear optional text fields (defect)** — the customer half was fixed in
  #270: `PUT /customers/{id}` now stores a sent `""` for `phone`, `company`, `position`,
  `address`, `city`, `state`, `country`, `postal_code` and `notes`, while an omitted key or a
  JSON `null` keeps the stored value (`first_name`, `last_name` and `email` still ignore an empty
  value by design). The lead handler has the same defect and was left out of that change: its
  optional text fields still apply only when non-empty. A JSON `null` meaning "clear" (a
  three-state wrapper) or a field mask is not offered on either entity. A browser test that
  clears a customer field and reloads (TC-CUST-018, browser half) is still planned.
- **Retire the unused bulk-operation schema** — since the generic bulk subsystem was removed
  (#112), nothing writes `bulk_operation_items` or the `success_count`/`failure_count` columns
  of `bulk_operations`; the status endpoints write only the parent row's type, resource, totals
  and status. Dropping them needs its own decision (existing rows are history), a
  `migrations/` down/up pair, the matching AutoMigrate change and an SQLite upgrade fixture.
- **Revisit the SQLite driver pin** — `github.com/glebarez/sqlite` pulls
  `github.com/glebarez/go-sqlite` v1.21.2, whose own `go.mod` pins
  `modernc.org/sqlite` v1.23.1; module resolution here settles on v1.59.0
  (engine 3.53.4), a much newer build than its author tested against. It works,
  but the pairing is worth re-checking whenever glebarez cuts a release.
- **Domain repair locking on MySQL** — the startup normalisation of mixed-case company domains
  updates them by primary key in chunks of 500; the optimiser may still plan a large `IN` list as
  a full scan and lock unrelated rows while it runs. It only runs when mixed-case domains exist.
  Tracked in #181.
- **Compose `build.args` pass-through of `VITE_APP_NAME`** — `gocrm-ui/Dockerfile` accepts an
  optional `VITE_APP_NAME` build argument (#303), but the `ui` service in `docker-compose.yml`
  and `docker-compose.sqlite.yml` declares no `build.args` for it, so `docker compose up --build`
  builds with the empty default (`GoCRM`) unless the argument is given on the
  `docker compose build ui --build-arg ...` command line. Forwarding it from the shell or the
  repo-root `.env` is #308.

## Companies — follow-ups

- **Backfill `company_id` from the free-text column** — every lead and customer created before
  2026-09-29 carries only the text `company`. A one-off script that groups the distinct texts,
  proposes a company per group (name from the text, domain from the email addresses) and links
  the rows after a human review would seed the companies table; it is deliberately not part of
  the migration, which only adds the nullable column.
- **Domain-based auto-link from forms** — a public form submission that creates a lead could
  set `company_id` when the submitter's email domain matches a live company's `domain`
  (free-mail domains excluded). Today the forms module writes only the text column, so a lead
  from `jane@acme.example` is not linked to the Acme company even when it exists; linking by hand
  on the lead form is the only path.
- **Lead conversion vs company deletion race** — a company deleted while a lead converts can leave
  the new customer linked to a soft-deleted company; tracked in #182.

## Follow-ups from the backend build-out

The audit-defect list and its loose ends are done: dashboard role guard, sentinel-error
classification everywhere, sales read-only on tickets (backend and frontend), standardised
pagination, strict configuration typing, and — instead of deleting the frontend functions that
called missing routes — the routes now exist (auth session lifecycle, dashboard analytics, bulk
status updates, API-key management, customer export/assign, upcoming tasks). Still open:

- **Access-token revocation beyond the session-ending events** — logout, a password change and a
  password reset now end every earlier access token through `users.token_version` (#236). A
  refresh rotation still leaves the access token it replaces valid until expiry, and a role change
  ends nothing; revoking a single access token would need a `jti` denylist, which is not planned.
  The web client's Profile copy still says access held elsewhere "ends when it expires"; that
  frontend text is a separate ticket.
- **Live session updates across tabs** — refreshes and password changes are coordinated across
  tabs by the Web Lock `gophercrm-session` and the session is stored as one record (#184), but
  other open tabs are not told about a sign-in or sign-out: they notice on their next request.
  Without `navigator.locks` (plain HTTP) the lock is per tab and a losing refresh relies on
  re-reading the record, which adopts a successor stored first but cannot help when the losing
  tab's 401 reaches it before the winner stored the successor.
- **Login issued from an obsolete verification** — a login that verified the old password a moment
  before a password change commits still gets its session inserted after the change; login should
  re-verify the hash and the active state under the owner lock in the issuing transaction, with one
  lock protocol shared by erasure and the auth flows (#183).
- **Erasure vs conversion/authentication concurrency: tracked in #183.** The identity erasure
  (#108) is one transaction, but it does not yet share a lock protocol with lead conversion,
  customer/login linking, login, refresh rotation, password reset or API-key creation: a
  conversion or credential committed while an erasure is in flight can be missed, and opposite
  entry points can deadlock (an error, never a partial commit). The same holds for a deal or quote
  write racing the business-text scrub (#245): a deal linked, or a quote edited, while the erasure
  is in flight can keep its text.
- **Deal and quote text left by the erasure (#245 follow-ups).** The erasure redacts the deal and
  quote text of the erased customers and leads, but a task a `create_task` step rendered from
  `{{deal.title}}` before the erasure keeps that text (`deal.title` stays allowed in task
  templates), deals and quotes of graphs erased before #245 are not repaired, and the web client
  has no indication that a title was redacted by an erasure (a later frontend ticket).
- **Production SMTP** — password-reset delivery falls back to a redacted log line unless
  `SMTP_*` is configured; the forms mail and the quote email take the same fallback, and the quote
  email then reports success and stamps `last_emailed_at` with nothing delivered. A deployment
  check (refuse to start, or refuse to stamp, without a configured transport) is a candidate.
- **SMTP require-TLS policy** — STARTTLS is opportunistic: without `SMTP_USER`, a relay that does
  not advertise STARTTLS (or a path attacker who strips the offer) receives mail, including
  password-reset and confirmation links, in cleartext. A setting that refuses delivery without
  TLS (and optionally implicit TLS on port 465) is a known gap.

## Defects found while documenting the API (2026-10)

Recorded while writing the API reference; the spec documents the current behaviour, warts
included. Found by reading the code path unless noted.

- **Changing the login email needs no current password** — `PUT /users/me` and `PUT /users/{id}`
  change the address a password reset is mailed to without asking for the current password, so
  whoever holds a live access token or API key can redirect the reset to their own address. Whether
  to guard it is an owner decision (found with #254, which closed the password path of
  `PUT /users/me` only).
- **`PUT /users/{id}` lets an admin deactivate or demote itself**, which
  `POST /users/{id}/deactivate` refuses.
- **Registration and `POST /users` answer 400 with the raw service error text** for any failure,
  database errors included. A password over bcrypt's 72-byte limit is a 400 there but a 500 on
  change-password and reset confirm; no endpoint checks the byte length first.
- **Database failures reported as 404** — `GET /users/{id}`, `GET /users/me` and the user lookup of
  `POST /customers/{id}/assign` map every lookup error to not found.
- **A failed quote email answers 502 `MAIL_DELIVERY_FAILED`** — behind Cloudflare the JSON body is
  replaced; errors that must reach the client use 503 elsewhere.
- **Validation details are keyed by Go field name** (`FirstName`) for binding failures but by JSON
  name for service-level field errors; Gin's validator has no tag-name function registered.
- **Zero-valued nested objects** — `Lead.owner`, `Ticket.customer` and `Task.assigned_to` are
  non-pointer structs tagged `omitempty`, which Go ignores, so responses that do not load them
  carry an object with id 0 instead of omitting the field.

## Forms module — deliberate v1 cuts

The forms module shipped without these; each is a
candidate follow-up, not an accident:

- **Multi-step forms, conditional field logic, progressive profiling** — single-step only.
- **File-upload fields** — text-shaped field types only.
- **HTML email templates** — confirmation/follow-up/notification mail is plaintext, like the
  password-reset mail it reuses.
- **CSV export of submissions, webhooks** — submissions are viewable in the UI and via the API.
- **Per-form styling themes** — the embed exposes CSS custom properties (`--gcrm-*`) and nothing
  else.
- **Per-form or per-status retention, and a settings screen** — the retention purge has one age,
  `FORM_RETENTION_DAYS`, off by default, and it removes only spam and never-confirmed rows with no
  linked lead. Received rows from forms that create no lead are kept.
- **Field-definition migrations** — editing a form's fields does not rewrite historical
  submission data; renamed fields simply start a new key in `data`.

## Follow-up automation — deliberate v1 cuts

The automation subsystem (#164) shipped its backend, API and screens with these decided out,
each a candidate follow-up:

- **Email to leads and customers** — `send_email` reaches staff users only until a consent and
  opt-out model exists.
- **Multi-replica leases** — the worker assumes one API replica: runs are recovered at boot, not
  by a lease timeout on `claimed_at`, so two workers would double-execute.
- **Retention** — processed `automation_events` and finished runs are kept forever; a pruning
  sweep is a retention decision, not an erasure one (the rows hold ids only).
- **Wait-until-event, scoring, configurable stages, campaigns, a template editor, customer and
  company triggers** — none of them exist. A deal run records the deal on its task
  (`deal_id`, #240) and links the deal's customer, else its lead; tasks created by earlier runs are
  not backfilled. The task form, task detail and deal detail show and edit the link.
- **Ticket automation beyond status changes** — ticket rules (#242) start on `ticket_created`,
  `ticket_updated`, `ticket_status_changed` and the daily schedule. Still missing: comment,
  first-response, SLA and overdue triggers; a `tasks.ticket_id` link (a ticket run's task links
  the ticket's customer only); run visibility for support users; the actor on single ticket
  create/update events (`actor_id` stays NULL there); and the rule-form, rule-list and run-list
  screens for ticket rules, which are a later frontend change (ticket rules are API-only until
  then). Every `ticket_updated` starts a new run of a matching rule, so a rule that should fire
  once per resolution belongs on `ticket_status_changed` or behind a condition.

## Sensitive settings — follow-ups

- **reCAPTCHA and SMTP credentials** are the next candidates for the sensitive-configuration
  mechanism (admin-editable, encrypted at rest, env fallback) that the AEO provider keys use.
- **Master-secret rotation orphans stored secrets**: sensitive values are sealed with a key
  derived from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it makes stored secrets
  undecryptable — they read as unset and must be re-entered in the UI.
