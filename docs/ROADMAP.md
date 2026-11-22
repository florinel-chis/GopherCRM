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
- **Vitest cases that time out on a loaded machine** — reduced in #403, not closed for the whole suite.
  - **The failures (2026-09-25, during `make verify` while the Go race tests and other work were
    running; load average up to 71):**
    - `src/routes/index.test.tsx` › "blocks non-admin users from /users via the pathless
      ProtectedRoute layout": the page rendered empty and `findByText` gave up after 1 s.
    - `src/pages/forms/FormBuilder.test.tsx` › "refuses a dropdown without options" and "lets an
      admin pick the lead owner from the user list" hit Vitest's 5 s test timeout.
    - Later runs under contention added `src/layouts/MainLayout.test.tsx` › "shows the Automation
      group for admin and sales and hides it for support and customer" and synchronous
      `FormBuilder.test.tsx` cases to the list.
  - **Not regressions:** each file passes alone.
  - **What was done (#403):**
    - `make verify-frontend` and the documented one-shot command run Vitest with `--maxWorkers=3`
      (`npm test -- --run --maxWorkers=3`).
    - `make verify` runs the frontend gate before the race-enabled Go tests, so the two no longer
      compete for the processor.
    - Every test in `src/routes/index.test.tsx`, `src/pages/forms/FormBuilder.test.tsx` and
      `src/layouts/MainLayout.test.tsx` carries an explicit test timeout (30 s or more) and every
      `findBy*`/`waitFor` call in them an explicit 15 s timeout, and the `MainLayout` test waits
      for the opened Automation group instead of reading it straight after the click. These three
      files no longer depend on the runner's 5 s test default or the 1 s query default: with the
      runner default forced to 1 ms (`--testTimeout=1`) all 63 of their tests pass, where 36 failed
      before.
  - **The check that was run:** ten consecutive runs of the three files next to one CPU-bound busy
    process per core (10 on a 10-core machine), started and stopped by the test command itself:
    63 of 63 passed in all ten runs. That load is the bound set for the check; it is not claimed
    to reproduce the original timeouts.
  - **What remains:** the sibling files (`MainLayout.branding.test.tsx`,
    `MainLayout.permissions.test.tsx`, `routes/index.permissions.test.tsx`,
    `FormBuilder.ticket.test.tsx`) and the rest of the suite keep the default timeouts and rely on
    the worker cap. A machine loaded well beyond one busy process per core can still time them
    out; give a file that does so the same explicit timeouts.
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
  (`make swagger`) and gated in CI (`scripts/ci/check-api-docs.py`, locally `make verify-api-docs`); serving them from the backend (e.g. a
  Swagger UI route) is deliberately not done
- **Cross-process AEO coordination: follow-ups** — run ownership is delivered (#229): every
  run has a database owner and heartbeat, admission is one insert guarded by the unique
  `aeo_runs.running_lock`, the daily run is claimed once per slot through `schedule_slot`, and a
  replica fails only runs whose heartbeat is older than 3 minutes (or that started over 6 hours
  ago). Still open: fencing of `aeo_answers` rows a superseded owner writes between losing its
  run and its next heartbeat (at most one 30 s interval); an optional periodic sweep, so a crashed
  run stops reading `running` before the next boot or start attempt; and several processes on one
  SQLite file, which stays unsupported.
- **AEO reporting: follow-ups** — the CSV/JSON export is delivered (#244): `GET /aeo/export`
  downloads one run or a 7/30/90-day window, capped at 10,000 answers, and the web UI download
  control is delivered (#318): Export CSV / Export JSON on the AEO dashboard (the selected window)
  and Export run as CSV / JSON in the prompts answer drawer (the selected run), for admin, sales and
  support. Still open: threshold alerts or notifications on visibility, PDF client reports
  (scheduled or emailed reports: see the deliberate cut below), a higher or streamed export cap for
  large 90-day windows, and the `trigger`/`status` rows of the export run type in the closed-set
  table of `scripts/ci/check-api-docs.py` (operator follow-up).
- **AEO scheduled client reports: deliberate cut** — scheduled or emailed AEO client reports are
  deliberately not built (#320); `GET /aeo/export` and its web UI buttons (#244, #318) are the only
  client-report output. Like the other cuts this is a candidate follow-up, not an accident. Three
  things must exist first: (1) a single scheduling owner across replicas, so each report slot is
  sent exactly once — the #229 pattern, where the daily AEO run is claimed once per slot through
  `aeo_runs.schedule_slot`; (2) a per-mail delivery record that never reports false success — the
  #216 pattern, where `form_mail_deliveries` keeps `sent`, `failed` and `log_only` apart, so a
  log-only mailer is never recorded as delivered; (3) an owner decision on mailing external,
  non-user recipients — the consent and opt-out question of #238, which already keeps automation
  `send_email` limited to staff users.
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
- **Clearing text fields on customer and lead updates: what stays open** — `PUT /customers/{id}`
  (#270) and `PUT /leads/{id}` (#398) store a sent `""` for their optional text fields, while an
  omitted key or a JSON `null` keeps the stored value (`first_name`, `last_name` and `email`
  still ignore an empty value by design). Not offered on either entity: a JSON `null` meaning
  "clear" (a three-state wrapper) or a field mask. The lead form requires "Company (as entered)"
  and "Lead Source" and has no position or external-id field, so a lead's `company`, `position`,
  `source` and `external_id` are cleared through the API only. The lead update does not re-check
  the email-or-phone rule of create, so the API accepts clearing the phone of a lead that has no
  email and leaves a lead with neither contact (pinned as observed behaviour by
  `TestLeadUpdateClearsOptionalTextFields` in `tests/lead_clear_fields_integration_test.go`).
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
- **Compose `build.args` pass-through of `VITE_APP_NAME`** — delivered (#308).
  `gocrm-ui/Dockerfile` accepts an optional `VITE_APP_NAME` build argument (#303), and the `ui`
  service in `docker-compose.yml` and `docker-compose.sqlite.yml` now forwards it through
  `build.args` (`${VITE_APP_NAME:-}`) from the shell or the repo-root `.env`; unset, the image
  keeps the empty default (`GoCRM`).

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
- **Other `company_id` writers outside the lock order** — lead conversion, lead update and bulk
  lead status now serialise with company deletion (#182, see the lock order in
  [DEVELOPMENT.md](DEVELOPMENT.md)), but lead create, customer create/update, deal writes and form
  intake still check `company_id` with a plain read and take no company lock, so one of them
  committing while the company is deleted can still link a soft-deleted company. Links left by
  that race before #182 are not repaired.

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
- **Erasure vs conversion/authentication concurrency: tracked in #183** (current orders in
  [CONCURRENCY.md](CONCURRENCY.md)). The identity erasure
  (#108) is one transaction. Lead-rooted erasure against a conversion of the same lead is closed by
  #337: the erasure cancels the lead's automation runs, then locks the lead row and decides on it,
  so a conversion either commits first (an admin erasure erases its customer too, a non-admin one
  is refused) or waits and answers 404. Since #385 every erasure, whichever row it starts from,
  discovers the person's rows, locks them in one order (automation runs, leads, customers, users,
  then the credential tables, ids ascending), re-checks the graph under the locks and is under the
  retry rule: opposite entry points queue instead of deadlocking, a lost deadlock or lock wait is
  replayed, and a graph that moved restarts the erasure, both within a bound. Still open: the
  governed user write (`DELETE /users/{id}`) holds the target's `users` row before its nested
  erasure locks leads and customers, so it can deadlock with a customer- or lead-rooted erasure
  of the same person (either side is replayed; a governed delete replayed after the other
  erasure committed answers 404, the account being erased already); inside a caller's transaction the erasure's re-check of the reverse links
  answers from the caller's snapshot; and nothing locks the `users` row when `customers.user_id`
  is set (no shipped code path sets it; it comes from a seed, an import or SQL), so a customer
  linked to the login after the erasure's re-check and before its commit is not erased. Login (#336), refresh
  rotation, the password-reset request and API-key creation (#342) lock the owner's `users` row
  before they issue anything, so against a user- or customer-rooted erasure they either commit
  first (and the erasure purges what they issued) or wait and are refused. The reset request and
  the reset confirm lock in opposite orders; when they deadlock the request is replayed and the
  confirm fails with nothing written (putting the confirm under the retry rule is open). A deal or quote write
  racing the business-text scrub (#245) can be missed like a conversion: a deal linked, or a quote
  edited, while the erasure is in flight can keep its text.
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

- ~~**Changing the login email needs no current password**~~ — done (#394): `PUT /users/me`, and
  `PUT /users/{id}` on the caller's own id, change the email only when the body carries the
  account's `current_password` (400 `CURRENT_PASSWORD_REQUIRED` without it, 400
  `INVALID_CURRENT_PASSWORD` when it does not match, nothing written), for a Bearer token and an
  API key alike, so a live access token or API key alone can no longer redirect the password
  reset. Changing another account's email (`users.update`) needs no password, as before. Known
  limits: a wrong `current_password` is not counted toward the login lockout, as on
  `POST /auth/change-password` (the same authenticated rate tier, so no new guess surface); an
  email change revokes no session and sends no confirmation mail.
- **`PUT /users/{id}` accepts an admin's own deactivation or demotion while another active admin
  remains**, which `POST /users/{id}/deactivate` refuses with 400 for every caller's own account.
  The last active admin is protected on both routes since #348 (409 `LAST_ADMIN`).
- **Database failures reported as 404** — these lookups report every lookup error, a database
  failure included, as a missing record or reference:
  - `GET`, `PUT` and `DELETE /leads/{id}` and the pre-read of `POST /leads/{id}/convert` answer
    404 `Lead not found` (the convert pre-read answers 500 only when the request was cancelled);
  - the assignee and customer lookups of `POST /tasks` and `PUT /tasks/{id}` answer 404;
  - the customer lookup of `POST /tickets` answers 404, and the assignee lookups of
    `POST /tickets` and `PUT /tickets/{id}` answer 400.
- **Quote PDF: the render credential store is per process (#384).** A render credential lives
  in the memory of the backend process that minted it. With several backend instances behind one
  origin, a render succeeds only when the render page's `POST /quotes/public/render` reaches the
  instance that started it; otherwise the credential is unknown there, the render fails, the
  download answers 503 `PDF_RENDER_FAILED` and the email falls back to text. A shared store (or
  a signed, self-describing credential) would lift this.
- **Quote PDF: the date is formatted in the server's time zone (#384).** The document formats
  its date in the browser's zone. The headless browser runs in the server's zone (UTC in the
  Docker image), so near midnight the PDF can show another issue date than the print page of the
  staff member's own browser.
- **Quote PDF output is off in the two development Compose files (#384).** Their `APP_BASE_URL`
  (`http://localhost:<UI_PORT>`) is not reachable from the backend container, so
  `docker-compose.yml` and `docker-compose.sqlite.yml` set `QUOTE_PDF_CHROME_PATH` empty, and
  `docker-compose.production.example.yml` does not forward it; an operator switches it on in an
  override (`docs/DOCKER.md`).
- **Quote PDF: the systemd deployment was not run on a real host (#384).** The renderer is built
  to need only a writable temporary directory, which the unit of `docs/SERVER_PROVISIONING.md`
  provides (`PrivateTmp=yes`); the nearest proof is the Docker image running as an unprivileged
  user with a read-only root file system. No automatic test drives a real browser through the
  web application either: `scripts/e2e/quote-pdf-chain.sh` (`local` with a full Chrome or
  Chromium, `image` with the image's `chrome-headless-shell`) is a manual release check.
- **Quote PDF: the full quote chain in the image is rendered by hand, on linux/arm64 only
  (#393, #420).** `scripts/e2e/quote-pdf-chain.sh image` (ten quotes through the web
  application, text and fonts of the PDF checked) is a manual release check and was run on
  linux/arm64. For linux/amd64 the only render is the single-page PDF of the
  [Docker image](../.github/workflows/docker-image.yml) workflow, which also builds the browser
  download for both architectures; it is not a required check.
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
- **CSV export of submissions, form-submission webhooks** — submissions are viewable in the UI and
  via the API. Outbound webhooks for created leads, customers and tickets ship with #345 (a form
  that creates a lead or opens a ticket sends `lead.created` / `ticket.created`), and #408 adds
  `lead.updated`, `customer.updated` and `ticket.updated` for `PUT /leads/{id}`,
  `PUT /customers/{id}`, `PUT /tickets/{id}` and the lead and ticket bulk status endpoints (#616
  adds lead assignment, lead conversion and the automation assign-owner step), and
  #437 adds `lead.deleted`, `customer.deleted` and `ticket.deleted` for deletes and erasures, and
  #515 adds `deal.created`, `deal.updated` and `deal.deleted`, #516 adds `task.created`,
  `task.updated` and `task.deleted`, #517 adds `company.created`, `company.updated` and
  `company.deleted`, and #518 adds `quote.created`, `quote.updated` and `quote.deleted`; still
  open: a submission event of its own, events of other entities (users), updated events for customer assignment, a repeat form enquiry or a company
  delete, secret rotation
  and retry jitter (the registry and the delivery log have a screen since #409).
- **Per-form styling themes** — the embed exposes CSS custom properties (`--gcrm-*`) and nothing
  else.
- **Per-form or per-status retention, and a settings screen** — the retention purge has one age,
  `FORM_RETENTION_DAYS`, off by default, and it removes only spam and never-confirmed rows with no
  linked lead. Received rows from forms that create no lead are kept.
- **Field-definition migrations** — editing a form's fields does not rewrite historical
  submission data; renamed fields simply start a new key in `data`.

## Form ticket intake — follow-ups (#213)

Form ticket intake ships with the form builder's "Create a support ticket" submission action
(#335) and the FormDetail "Creates tickets" line, with these open:

- ~~**Per-render idempotency key in the embed script**~~ — done: the hosted view renders one
  `crypto/rand` key per render as `data-idempotency-key` (#340), and the embed script sends it, or
  a key it draws once per rendered form from `crypto.getRandomValues`, on every submission of the
  render (#341).
- **Lead deduplication for `create_lead` forms** — the server ignores `idempotency_key` on
  `create_lead` and no-action forms, so a resend there still stores a second submission and can add
  a second lead note or lead; storing and honouring the key for those forms needs its own backend
  change.
- **Submission views** — the ticket outcome, ticket link and key on the submission list and
  detail. (The builder's ticket action shipped in #335.)
- **Authenticated ticket import endpoint** — for integrators that do not need double opt-in (Q2).
- **Unmatched submitter policy** — today a confirmed address that matches no live customer gets no
  ticket (`no_customer`); creating a customer or using a fallback customer is undecided (Q1).
- **Key expiry** — keys live until their ticket is deleted or the customer or submitter is erased
  (Q5).
- **Per-form default assignee or priority** — tickets start unassigned at medium priority (Q4).
- **Operator follow-ups** — register the new regression roots in `scripts/ci/check-go.py` and the
  migration roots in the Database migrations workflow, and add the `ticket_outcome` closed set to
  `scripts/ci/check-api-docs.py`.

## Follow-up automation — deliberate v1 cuts

The automation subsystem (#164) shipped its backend, API and screens with these decided out,
each a candidate follow-up:

- **Email to leads and customers** — `send_email` reaches staff users only until a consent and
  opt-out model exists.
- ~~**Multi-replica leases**~~ — done (#237): each claim records the engine's instance id and a
  heartbeat lease, every write after it is fenced on the owner, and a stale sweep at boot and on
  every tick recovers runs whose lease is older than 3 minutes, so several replicas may run the
  worker against one MySQL/MariaDB database. Still open: fencing the window between the pre-send
  check and `Mailer.Send` (an executor paused exactly there can send once after a sweep recorded
  `email_unconfirmed`). Every cancel, lead erasure and ticket delete included, clears the lease
  columns since #333.
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
  create/update events (`actor_id` stays NULL there); a ticket "Automation runs" section on the
  ticket detail page; and narrowing the `assign_owner` user picker of a ticket rule to support and
  admin users (the API's `unknown_reference` refusal is shown on the field instead). The rule form,
  rule list, run list and dry run handle ticket rules since #312. Every `ticket_updated` starts a
  new run of a matching rule, so a rule that should fire once per resolution belongs on
  `ticket_status_changed` or behind a condition.

## Sensitive settings — follow-ups

- **reCAPTCHA and SMTP credentials** are the next candidates for the sensitive-configuration
  mechanism (admin-editable, encrypted at rest, env fallback) that the AEO provider keys use.
- **Master-secret rotation orphans stored secrets**: sensitive values are sealed with a key
  derived from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it makes stored secrets
  undecryptable — they read as unset and must be re-entered in the UI.
