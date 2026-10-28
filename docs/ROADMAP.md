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
- **Sortable columns the backend has no mapping for** — the customers **Total Revenue** and
  **Status** (`is_active`) headers, and the tickets **Customer** / **Assigned To** and the tasks
  **Assigned To** header, are deliberately non-sortable in the list pages: `utils.AllowedSortColumns`
  has no `total_revenue` and no customers `is_active` entry, and tickets/tasks allow only the
  `customer_id` / `assigned_to_id` foreign keys, which order by id rather than by the name the
  column renders. Restoring the affordance needs an allowlist design first — widen it with real
  orderable columns (or joined expressions) and map each UI column onto one, rather than letting
  the frontend send a column the validator rejects.

## Backend and delivery

- **Serve the OpenAPI spec** — the spec is now generated from handler annotations into `api/`
  (`make swagger`); serving it (e.g. gin-swagger with a Swagger UI route) remains unimplemented
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
- **Revisit the SQLite driver pin** — `github.com/glebarez/sqlite` pulls
  `github.com/glebarez/go-sqlite` v1.21.2, whose own `go.mod` pins
  `modernc.org/sqlite` v1.23.1; module resolution here settles on v1.59.0
  (engine 3.53.4), a much newer build than its author tested against. It works,
  but the pairing is worth re-checking whenever glebarez cuts a release.

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

## Follow-ups from the backend build-out

The audit-defect list and its loose ends are done: dashboard role guard, sentinel-error
classification everywhere, sales read-only on tickets (backend and frontend), standardised
pagination, strict configuration typing, and — instead of deleting the frontend functions that
called missing routes — the routes now exist (auth session lifecycle, dashboard analytics, bulk
status updates, API-key management, customer export/assign, upcoming tasks). Still open:

- **Access-token blocklist** — logout/rotation revoke refresh tokens only; an issued JWT stays
  valid until expiry.
- **Concurrent-refresh stampede** — several simultaneous 401s can race the refresh interceptor;
  with strict rotation the losers get logged out. The backend side is settled (#103): competing
  refreshes of one token commit exactly one successor and the others get the generic 401, so the
  server can no longer mint two sessions or strand one. What remains is client-side: a shared
  in-flight refresh promise so only one request is sent (#115).
- **Production SMTP** — password-reset delivery falls back to a redacted log line unless
  `SMTP_*` is configured; the forms mail and the quote email take the same fallback, and the quote
  email then reports success and stamps `last_emailed_at` with nothing delivered. A deployment
  check (refuse to start, or refuse to stamp, without a configured transport) is a candidate.
- **Sales ticket navigation** — the backend allows sales to read tickets, but the nav item is
  hidden from them (deep links work); widen the nav or narrow the read routes, a product call.

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
- **Retention sweep for unlinked submissions** — spam and never-confirmed pending rows carry
  visitor data (values, IP) and are outside lead erasure; they currently live forever.
- **Field-definition migrations** — editing a form's fields does not rewrite historical
  submission data; renamed fields simply start a new key in `data`.

## Follow-up automation — deliberate v1 cuts

The automation subsystem (#164) shipped its backend and API with these decided out, each a
candidate follow-up:

- **The screens** — rule list and form, dry-run dialog, run list and detail, the lead and deal
  "Automation" sections and the notification bell are the separate `follow-up-automation-ui`
  task; the API is complete without them.
- **Email to leads and customers** — `send_email` reaches staff users only until a consent and
  opt-out model exists.
- **Multi-replica leases** — the worker assumes one API replica: runs are recovered at boot, not
  by a lease timeout on `claimed_at`, so two workers would double-execute.
- **Retention** — processed `automation_events` and finished runs are kept forever; a pruning
  sweep is a retention decision, not an erasure one (the rows hold ids only).
- **Wait-until-event, scoring, configurable stages, campaigns, a template editor, customer,
  ticket and company triggers, a `deal_id` on tasks** — none of them exist; a deal run links its
  task to the deal's customer, else its lead.

## Sensitive settings — follow-ups

- **reCAPTCHA and SMTP credentials** are the next candidates for the sensitive-configuration
  mechanism (admin-editable, encrypted at rest, env fallback) that the AEO provider keys use.
- **Master-secret rotation orphans stored secrets**: sensitive values are sealed with a key
  derived from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it makes stored secrets
  undecryptable — they read as unset and must be re-entered in the UI.
