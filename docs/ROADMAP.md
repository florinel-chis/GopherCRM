# Roadmap

Unprioritized ideas that are **not** implemented today. This list is the salvaged remainder of two
pre-implementation build checklists (`tasks.md` and `uitasks.md`) that were removed once the
features they planned had shipped; only items verified as still missing are kept here. Shipped
functionality and its test coverage are tracked in [FEATURES.md](FEATURES.md).

## Frontend

- **Kanban board for tasks** — drag-and-drop status board alongside the existing task list
- **Saved filters** — persist per-user filter presets on the list pages
- **Reports and analytics** — the dashboard shows the deal pipeline and the deals won this month
  (`gocrm-ui/src/components/dashboard/PipelineChart.tsx`,
  `gocrm-ui/src/components/dashboard/WonThisMonthTile.tsx`, `GET /dashboard/pipeline`). Still
  open: lead conversion rate over time, ticket metrics, user activity
- **Data export** — the lead list has a CSV export button
  (`gocrm-ui/src/pages/leads/LeadList.tsx`); customer CSV export is API-only
  (`GET /customers/export`, no button in `gocrm-ui/src/pages/customers/CustomerList.tsx`). Still
  open: export from the other list views, a customer export button, and PDF export of lists
- **Accessibility pass** — keyboard navigation and screen reader review against WCAG 2.1 AA
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
  #181 was closed as not planned: the application writes no mixed-case domains; reopen it if a
  database that holds them is in scope.
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

The audit-defect list of the backend build-out and its loose ends are done. Still open:

- **Access-token revocation beyond the session-ending events** — every access token carries the
  account's `users.token_version`, and raising it ends every earlier access token at once. It is
  raised by logout, a password change, a password reset (#236), deactivation (by an administrator
  or through SCIM), a SCIM group change that leaves the account in no mapped group (which
  deactivates it) or that gives it a role lacking a permission the old one had, and the identity
  provider's front-channel sign-out (`GET /auth/oidc/frontchannel-logout`, which logs the account
  out); see README "Known Limitations" and [AUTHORIZATION.md](AUTHORIZATION.md). Still open: a
  refresh rotation leaves the access token it replaces valid until expiry, and a role change made
  any other way leaves the account's access tokens valid until expiry. Revoking a single access
  token would need a `jti` denylist, which is not planned.
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

## Quote PDF — follow-ups (#384)

Limits of the quote PDF renderer that are still open:

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

## Forms module — deliberate v1 cuts

The forms module shipped without these; each is a
candidate follow-up, not an accident:

- **Multi-step forms, conditional field logic, progressive profiling** — single-step only.
- **File-upload fields** — text-shaped field types only.
- **HTML email templates** — confirmation/follow-up/notification mail is plaintext, like the
  password-reset mail it reuses.
- **CSV export of submissions, form-submission webhooks** — submissions are viewable in the UI and
  via the API only, with no CSV export. A form that creates a lead or opens a ticket sends the
  outbound webhook events of that lead or ticket, but a submission has no event of its own (see
  [Outbound webhooks — follow-ups](#outbound-webhooks--follow-ups)).
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
- **Multi-replica leases: the pre-send window** — several replicas may run the worker against one
  MySQL/MariaDB database (#237), but the window between the pre-send check and `Mailer.Send` is
  not fenced: an executor paused exactly there can send once after a stale sweep recorded
  `email_unconfirmed`. Every cancel, lead erasure and ticket delete included, clears the lease
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

## Outbound webhooks — follow-ups

Outbound webhooks send created, updated and deleted events for leads, customers, tickets, deals,
tasks, companies and quotes (the catalogue is `defaultCatalogue` in `internal/webhook/catalog.go`; #345,
#408, #437, #515–#518, #616), and Settings > Webhooks lists the endpoints and their delivery log
with a test ping, a rotate-secret control and redelivery (#409, #750). Still open:

- **User events** — there is no `user.*` event type in `internal/webhook/catalog.go`.
- **More updated events** — customer assignment, a repeat form enquiry and a company delete send
  no updated event.
- **Retry jitter** — the retry delay is a plain doubling up to a ceiling
  (`internal/webhook/runtime.go` `WebhookBackoff`), with no jitter.
- **Pause state on the Settings screen** — an endpoint's `paused_until` and
  `consecutive_failures` are in the API response, but Settings > Webhooks does not show them.

## Single sign-on and SCIM — follow-ups

- **SAML** — not supported: single sign-on is OpenID Connect only, with one identity provider at
  a time.
- **SCIM Bulk, sorting and ETags** — not served; the service provider configuration
  (`internal/idkit/scim/scim.go` `NewServiceProviderConfig`) declares them unsupported.
- **Renaming or deleting a SCIM group** — not possible: `DELETE /Groups/{id}` answers 405 and
  `PUT /Groups/{id}` is not routed (`internal/handler/scim_routes.go` `SetupSCIMRoutes`).

## Audit and API keys — follow-ups

- **Single sign-on settings changes are not audited** — `internal/handler/sso_settings_handler.go`
  writes no audit row, so a change appears on neither Settings → Role audit nor Sign-in audit.
- **Other users' API keys** — an administrator cannot list or revoke another user's API keys:
  `GET /api-keys` lists the caller's own keys, and another user's key is 403 with no admin
  override (`internal/handler/apikey_handler.go` `APIKeyHandler.List`).

## Sensitive settings — follow-ups

- **reCAPTCHA and SMTP credentials** are the next candidates for the sensitive-configuration
  mechanism (admin-editable, encrypted at rest, env fallback) that the AEO provider keys use.
- **Master-secret rotation orphans stored secrets**: sensitive values are sealed with a key
  derived from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it makes stored secrets
  undecryptable — they read as unset and must be re-entered in the UI. The same rotation also
  makes unreadable:
  - the stored single sign-on client secret and client certificate (an unreadable certificate
    counts as not set, and the client secret applies; see
    [single-sign-on.md](user-guide/permissions-access/single-sign-on.md));
  - every webhook signing secret: deliveries fail with `secret_unavailable` until the secret is
    replaced with `POST /webhooks/endpoints/{id}/rotate-secret`.
