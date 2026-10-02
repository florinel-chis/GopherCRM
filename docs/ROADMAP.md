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
- **Customer `website` is collected and discarded** — both forms that create a customer, the
  customer form itself and the lead-conversion dialog, render a `Website` input (URL-validated, and
  declared on the TypeScript `Customer` type), but the Go `Customer` model has no such column. The
  value is silently dropped on save and comes back empty on the next edit. Either store it (model
  field, DTOs, migration) or drop the input; leaving it teaches users the CRM keeps something it
  never had.
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
  - **Done so far (2026-10-02):** the three files that timed out under load on 2026-10-02
    (`src/pages/tickets/TicketForm.test.tsx`, `src/pages/forms/FormBuilder.test.tsx`,
    `src/pages/customers/CustomerForm.test.tsx`) raise their own `testTimeout` to 15 s with
    `vi.setConfig`; `VERIFY_JOBS=N` caps Vitest workers and `go test -p`; and the full gates
    serialise through `scripts/gate-lock.sh`, which removes the overlap that caused the load.
    `src/routes/index.test.tsx` keeps the default and stays on this list.
- **AEO Citations copy describes the wrong denominator** — the backend computes
  `owned_citation_rate` and every `citation_rate` as citations to that company or domain divided by
  all citations in the window (`internal/service/aeo_service.go`, `Citations`), so the company rates
  add up to 100% together with unaffiliated domains. The page describes them as shares of answers:
  "answers citing a domain you own" under the owned-domain rate and "Share of answers citing each
  company's domains" on the chart (`gocrm-ui/src/pages/aeo/AEOCitations.tsx`). Either reword the
  copy to "share of all citations", or change the backend to count answers, which needs a second
  aggregation per company.
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
- **CI pipeline** — build, lint, unit tests, and E2E on pull requests
- **Coordinated shutdown for detached background work** — an AEO run executes in a goroutine
  launched with `context.Background()` (`aeo_service.go`, `go executor.Execute(...)`), so it is
  neither cancelled nor waited for at SIGTERM: `stopBackground()` only stops the scheduler from
  starting new runs, and the process closes the database handle as soon as HTTP drains. An active
  run can therefore write to a closed pool. The damage is bounded — the stale-run reconciliation at
  the next boot treats the row as a crash and recovers it — but the run's remaining results are
  lost. Closing it properly means a `WaitGroup` plus a cancellable run context joined before the
  close, and a clear owner for the database handle so nothing outlives it.
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

- **UI pages for the password-reset flow** — `authApi.changePassword` now has one
  (`/settings/profile`), but `requestPasswordReset` and `resetPassword` are still called by no
  page; the reset email links to `/reset-password?token=...`, which has no route in the SPA yet.
- **Access-token blocklist** — logout/rotation revoke refresh tokens only; an issued JWT stays
  valid until expiry.
- **Concurrent-refresh stampede** — several simultaneous 401s can race the refresh interceptor;
  with strict rotation the losers get logged out. A shared in-flight refresh promise would fix it.
- **Production SMTP** — password-reset delivery falls back to a redacted log line unless
  `SMTP_*` is configured.
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

## Sensitive settings — follow-ups

- **reCAPTCHA and SMTP credentials** are the next candidates for the sensitive-configuration
  mechanism (admin-editable, encrypted at rest, env fallback) that the AEO provider keys use.
- **Master-secret rotation orphans stored secrets**: sensitive values are sealed with a key
  derived from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it makes stored secrets
  undecryptable — they read as unset and must be re-entered in the UI.
