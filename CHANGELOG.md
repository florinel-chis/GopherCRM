# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Follow-up automation: rule names on runs, field-level rule errors, dry run of unsaved edits.**
  Run responses (`GET /automation/runs`, `GET /automation/runs/{id}`, the cancel answer) carry
  `rule_name` — the rule's current name, also for a deleted rule, flagged `rule_deleted` — and
  never the rule's trigger or steps (the `rule` association is no longer serialised at all); the
  run list, run detail, the lead and deal Automation sections and the run list's rule filter show
  the name. Sales users therefore see the names of the rules that ran on their records, also after
  a rule is deleted; the rule form says so under the name. A refused rule body lists every refused
  field in `error.details.fields` as `{field, code, message}` (`steps[2].config.title`, steps
  counted from zero) while `error.message` and `error.code` stay those of the first refusal; the
  editor puts each message under its field, following steps moved while the save was in flight and
  dropping errors of fields edited or steps removed meanwhile. `POST /automation/rules/dry-run`
  (admin) simulates an unsaved rule body after the same validation as a create, saving nothing and
  starting no transaction; the editor's Dry run button now simulates the form as it is, on a new
  rule too, while the rule list's dry run keeps simulating the saved version.
- **Follow-up automation and notifications (UI).** The screens for the automation API:
  `/automation/rules` (admin) lists rules with an enable switch (disabling asks first, because it
  cancels the rule's active runs), Edit, Dry run and delete tools; `/automation/rules/new` and
  `/automation/rules/{id}/edit` build the trigger and up to 20 steps with a sub-form per step
  type, forward-only jumps, placeholder help, status and stage condition values as free text with
  the known values suggested (kept verbatim, as the API compares plain strings), the API's limits
  checked client-side, and an edit
  asks for confirmation before it creates a new version; the dry-run dialog simulates the saved
  rule on a chosen lead or deal and shows each step's outcome, simulated time and details.
  `/automation/runs` (admin and sales; sales sees runs on records it owns) filters by status,
  rule, entity and record, and `/automation/runs/{id}` shows the step log with an admin-only
  Cancel run. Lead and deal detail pages gain an Automation section with the record's runs. Staff
  users get a notification bell in the app bar (unread count polled every 60 s and on window
  focus, the latest ten in its menu, mark read, mark all read) and a `/notifications` page with an
  unread filter; customers see neither. Failed loads show the server message with a retry. The e2e
  spec `automation-rules.spec.ts` builds a rule in
  the form and dry-runs it; eight captures are in `docs/screenshots/automation/`.

- **Products and quotes (UI).** The frontend slice of the quotes feature: `/products` (admin and
  sales; name, SKU, price with currency, active chip, search, active filter, server pagination and
  sorting) with an admin-only form at `/products/new` and `/products/{id}/edit` (name ≤ 200,
  SKU ≤ 64, price in major units sent as `unit_price_cents`, currency defaulting to
  `deals.default_currency`, active switch); `/quotes` (number, title, status, customer, deal,
  total, valid until; status filter, server pagination) with a create dialog (title, deal and
  customer pickers, currency, valid until, notes); `/quotes/{id}` with the lines table, add/edit/
  delete of lines and an edit dialog while the quote is a draft and no edit control otherwise,
  the server's subtotal, discount and total, status buttons for the valid transitions only (the
  server message on `INVALID_STATUS_TRANSITION` and `CONFLICT`), a Print link, an "Email quote"
  confirmation on sent and accepted quotes that names the customer and distinguishes a 409 from a
  502, and the status history; `/quotes/{id}/print`, a print-styled route whose app chrome is
  hidden under `@media print` and whose Print button calls `window.print()`; a "Quotes" section on
  the deal detail page with "New quote" prefilled with the deal and hidden on won or lost deals;
  Products and Quotes navigation entries and routes for admin and sales only, the product form
  routes for admin only. Discounts are typed as percentages and sent as basis points; totals are
  never recomputed in the browser. New endpoint modules `src/api/endpoints/products.ts` and
  `quotes.ts`; Vitest coverage per screen and the Playwright spec
  `gocrm-ui/e2e/tests/admin-quotes.spec.ts` (product → deal → quote → line → sent → print →
  accepted, with print-media emulation). No new dependency. (#167)

- **Products and quotes screenshots.** Ten documentation captures under
  `docs/screenshots/quotes/` (product list and form, quote list, the create dialog from a deal,
  the draft detail with two lines, the add-line dialog, the sent detail with its status actions,
  the print page on screen and under print media, and the deal's Quotes section with an accepted
  quote), produced by the new screenshot spec `gocrm-ui/e2e/screenshots/11-quotes.spec.ts` and
  indexed in `docs/SCREENSHOTS.md`. The capture review also fixed the "Active" filter on
  `/products` and the "Status" filter on `/quotes`, whose floating label overlapped the empty
  "All products" / "All statuses" value: the label is now shrunk and the outline notched.

- **Follow-up automation and notifications (API).** Admin-defined rules (`/automation/rules`)
  start on lead events (`lead_created`, `lead_updated`, `lead_status_changed`), deal events
  (`deal_created`, `deal_updated`, `deal_stage_changed`, won and lost included) or a daily UTC
  schedule, and run ordered steps: `wait`, `condition` (allowlisted fields and operators, `else`
  stop or forward jump, `amount_cents` only in the deal's currency), `create_task`, `assign_owner`,
  `send_email` to a staff user and `notify` (in-app). Every lead and deal write path — the lead
  service, form submissions, bulk status/assign/convert, conversion, and deal create/update/stage —
  records an outbox event inside the transaction that commits the write; automation-originated
  writes record `origin=automation` events that start no run. A worker (`AUTOMATION_ENABLED`,
  `AUTOMATION_POLL_INTERVAL_SECONDS`, see `.env.example`) claims events with a conditional update,
  starts one run per enabled matching rule (`clause.OnConflict{DoNothing: true}` on the dedupe
  key, so a duplicate is neither an error nor a row on SQLite or MySQL), executes each step in one
  transaction behind a locking read of the record and a conditional advance of the run row, parks
  waits in `resume_at` so they survive a restart, continues schedule passes from a persisted
  cursor (500 records per tick, one pass per UTC day, same-day catch-up, no backfill) and
  reconciles interrupted runs at boot (a `send_email` left in `sending` is failed with
  `email_unconfirmed` and never resent); a process started with `AUTOMATION_ENABLED=false`
  neither executes nor reconciles runs, so it leaves the worker replica's runs alone. The email
  step is two transactions around one `Mailer.Send`. A `create_task` title or description accepts
  only the non-personal placeholders (`lead.id`, `lead.status`, `lead.source`, the `deal.*`
  fields) because a task survives the erasure of its lead; the name, email, phone, company and
  owner placeholders stay available to the email and notification text, which does not outlive
  the person. Rule updates cancel the rule's active runs with `rule_changed` (disable:
  `rule_disabled`, delete: `rule_deleted`); `POST /automation/runs/{id}/cancel` is `manual`. A dry
  run (`POST /automation/rules/{id}/dry-run`) reports each step's would-be outcome and writes,
  sends and notifies nothing. The run history (`/automation/runs`) follows lead and deal
  visibility inside the query; `/notifications` serves a staff user's own notifications. Lead
  erasure cancels the lead's active runs (`record_deleted`) and hard-deletes its notifications,
  before the scrub; user erasure hard-deletes the user's notifications. Logs carry ids, statuses,
  step types, outcomes and error codes only. Schema: AutoMigrate plus the MySQL/MariaDB migration
  `20261028120000_add_follow_up_automation` (six new tables, nothing altered). The worker assumes a
  single API replica and no retention pruning exists yet. The screens are described in the
  "Follow-up automation and notifications (UI)" entry above. (#164)

- **Products and quotes (API).** A product catalogue (`/products`: name, SKU, unit price in
  integer minor units with an ISO 4217 currency, active flag; admin writes, sales reads) and
  quotes linked to a deal and/or a customer (`/quotes`, `/deals/{id}/quotes`,
  `/customers/{id}/quotes`; admin and sales, sales narrowed to its own quotes inside the query).
  Lines copy the product's name and price when they are added, take a per-line discount in basis
  points, and the server recomputes the totals with round-half-up on every line change; the line
  gross and the quote total are capped at 10^12 minor units and a quote holds at most 100 lines.
  The lifecycle is draft → sent → accepted/rejected/expired (sent → draft to revise): each
  transition is a conditional claim plus one `quote_status_changes` row in a single transaction,
  draft → sent reads the line count after the claim and refuses an empty quote, and acceptance is
  refused after `valid_until`. Only drafts are editable. `POST /quotes/{id}/email` sends a
  plaintext rendering of a sent or accepted quote to the linked customer's current address with a
  fixed subject and stamps `last_emailed_at` by a conditional update that a revision during the
  send defeats (409); a mailer failure is 502 `MAIL_DELIVERY_FAILED`. Without `SMTP_HOST` the
  quote email takes the same logging fallback as the password reset: it is logged, not delivered,
  and the call still answers 200 and stamps `last_emailed_at`. The SMTP mailer refuses a
  recipient or subject containing CR or LF (`mailer.ErrInvalidHeader`), and its failure log keeps
  the code of a relay reply without the text, which can echo the recipient. Quotes copy no personal
  data, so customer erasure leaves them intact and the PII sweep covers the four new tables. Schema:
  AutoMigrate plus the MySQL/MariaDB migration `20261026120000_add_products_and_quotes`. The
  frontend (products and quotes pages, print page, DealDetail section) is a separate follow-up.
  (#158)

- **Feature analysis workflow (runner 0.9.0).** `automation/langflow/flows/analysis.json` and
  `gophercrm-runner analyze` turn one feature request into a gitignored
  `docs/specs/<date>-<slug>.md`: a one-page summary, then acceptance criteria in the task
  manifest shape, the data model with both migration halves, API, screens, permissions, Mermaid
  diagrams, test plan, documentation to update, cited research notes, an independent review with
  one bounded revision and the open questions. Agents are read-only and restricted to a detached
  worktree at `origin/main`; the runner writes the file, records provider-reported cost and
  duration per call, and verifies a digest chain between stages. Refinements re-enter with
  numbered owner comments and inherit the parent run's journals. See the
  [runner guide](automation/langflow/README.md#analyse-a-feature-request).

- **Frontend task profile with named Vitest evidence (runner).** A task manifest may now set
  `validation_profile: "frontend"`. Its acceptance checks are registry entries of the new
  optional `kind: "vitest"` (`go` remains the default, so existing entries and frozen contract
  hashes are unchanged) whose argument array is exactly `npm --prefix gocrm-ui test --silent --
  --run --reporter=json` followed by test files under `gocrm-ui/src`; each named test is a file
  and the exact `fullName` the JSON reporter prints, and must report `passed` exactly once.
  Failed, skipped, pending or todo cases, a required file without results, and duplicates fail
  acceptance. A frontend ticket changes only `gocrm-ui/`, `docs/`, `README.md` and
  `CHANGELOG.md`, cannot mix Go and Vitest checks, keeps `make verify` (build, zero-warning lint,
  unit tests) and requires the selected browser checks on SQLite and MySQL; freezing it needs
  `database_test_commands` and `TEST_MYSQL_DSN`. For every profile, the Vitest and Playwright
  configuration, the Vitest setup files and the whole `gocrm-ui/e2e/` suite are now protected
  validation gates that no agent may change; only a newly added `gocrm-ui/e2e/tests/*.spec.ts`
  is admitted. See the [runner guide](automation/langflow/README.md#execute-the-queue).
- `gophercrm-runner retire-issue` retires an unfinished ticket execution whose GitHub issue was
  closed outside the runner, recording the closure and its closing pull requests as unvalidated
  by that execution, releasing its admission slot and stopping the queue without modifying GitHub.
- **Maintainer-commit adoption for ticket executions (runner).** `gophercrm-runner adopt-commit
  --issue N --head FULL_SHA --reason ...` continues an unfinished ticket whose repair budget is
  exhausted, or whose candidate must be corrected by hand, with an inspected maintainer commit on
  the ticket branch: the counterpart of `complete-pr --refresh --head`. The worktree must be
  clean at that exact commit, the commit must descend from the recorded head, its diff from the
  journal base must pass the protected-path and frozen-scope gates, and a recorded pull request
  must already carry it. Adoption records an `adoptions` entry, invalidates tests and review as
  `freeze --refresh` does and returns the execution to `validate`; it consumes no repair round
  and leaves spent calls and the deadline unchanged. See the
  [operations runbook](automation/langflow/OPERATIONS.md#adopt-a-maintainer-commit).

- A shared bounded mailer validation profile: API build/vet, SMTP transport/header/privacy
  regressions, focused password-reset/form/quote/automation delivery callers and browser health smoke.
  Required tests cannot be absent or skipped; mixed application changes and explicit
  full/release runs retain complete coverage.

- **AEO runtime ownership.** The daily scheduler now starts whenever `AEO_SCHEDULE_ENABLED` is
  on, even when no provider key exists at boot: engines are resolved from the environment plus
  the administrator's stored keys when a tick is due, so a key saved in Settings is used by the
  next scheduled run without a restart. A tick that finds the module unconfigured is
  logged once at info level and the loop sleeps until the next day. At shutdown the process
  closes run admission (a run requested while draining answers 503 `SHUTTING_DOWN`), cancels
  the scheduler and every manual, scheduled or single-prompt run in flight — cancellation
  reaches provider calls and repository writes — joins them, and only then closes the database.
  When cleanup can persist it, an interrupted run is finalized as `failed` (or `partial` when answers were already recorded)
  with `completed_at` set; queries cut short are counted as failed and not recorded as answers.
  The drain waits at most 15 seconds; if a provider or database write still has not released by
  then, the process exits without closing the database and the next boot's reconciliation fails
  the row, as after a crash. (#111)

- Shared Go validation isolates application database variables while retaining the disposable
  `TEST_MYSQL_DSN`, so configuration defaults are tested consistently locally and in MariaDB CI.
  Runner policies can explicitly allow zero repair rounds for a continuation whose repair
  allowance is exhausted; validation, independent review and CI remain required.

- A conservative committed proof for comment-only `.env.example` edits, allowing required
  configuration guidance to accompany bounded validation without relaxing active-setting,
  mixed-scope or explicit full/release gates.

- A shared bounded AEO runtime validation profile with committed startup/interface scope
  proofs, required lifecycle roots, focused Go checks and health/provider browsers. SQL,
  MariaDB migration coverage and explicit full/release gates remain mandatory.
- Opt-in candidate validation inspection through `confirm-validation`; changed candidates
  need new inspection, and stopped executions retain their spent budgets.

- Runner recovery and Markdown handoff reports, batch-wide audited budget extensions,
  worker heartbeats, installed-module verification and validation-cost previews. Heavy commands
  share a machine lock, record bounded resource observations and enforce wall-clock deadlines
  after sleep. Explicit retention preview/apply reclaims only inactive merged validation
  dependencies while preserving source and evidence.

- A shared transaction validation profile with SQLite/MySQL boundaries, affected-caller race
  regressions and health smoke; CI adds MariaDB boundaries. Partial full-suite attempts remain
  evidence of interrupted runs, and explicit full/release validation includes the SQL boundaries.
  Browser port conflicts stop for environment recovery without launching a code repair.

- Dedicated PR migration checks on disposable MySQL 8 and MariaDB 10.11 services,
  including populated upgrades, rollback/reapply and SQLite upgrade fixtures. Required
  tests must execute without skips; CI retains their results and tested revision. Both engines
  feed the existing required Backend check. Workflow/checker-only changes use shared targeted
  migration validation and health smoke.
- A targeted auth validation profile with auth/session race regressions and application
  health, login and registration checks. Explicit full/release validation remains complete.
- Explicit documentation impact and review decisions in new Langflow runner configurations,
  with candidate-bound evidence and verification after merge. CI publishes impact reports;
  documentation updates remain in the reviewed PR.
- Bounded prepared backend batches and a runner-only validation profile using relevant workflow
  tests and application health smoke. Releases retain the full validation matrix.

- Contextual browser validation for PRs and the local Langflow runner: health smoke plus
  affected feature specs, with full fallback for shared or unknown changes. Selected core checks,
  named issue regressions, independent review and exact-commit merge checks remain required.
- A manual full-validation Release workflow with rehearsal mode, version/changelog preparation,
  verified artifacts, annotated tags and draft-first publication. Production deploys now consume
  published release artifacts after checking their full-validation run and commit identity.

- **`SERVER_HOST`.** Optional listen interface for the API server. Empty, the default, keeps
  the previous behaviour of binding every interface; `127.0.0.1` restricts the server to the
  local machine behind a reverse proxy. The startup log now names the full listen address.
- **Deal pipeline aggregates.** `GET /deals/pipeline` (admin and sales; sales always sees its own
  deals, admin may filter by `owner_id` and `company_id`) returns all five stages in pipeline
  order with each stage's deal count and totals per currency: the amount and the weighted amount,
  round half up of Σ `amount_cents × probability` / 100, rounded once per stage and currency.
  `GET /dashboard/pipeline` returns the same stages for the dashboard plus `won_this_month`, the
  deals won in the current calendar month in UTC, counted and summed per currency (admin all,
  sales own; support and customer 403). Aggregation is integer arithmetic grouped in SQL with no
  date functions, the same on MySQL, MariaDB and SQLite; no schema change.
- **Deal amount bound.** `amount_cents` on `POST`/`PUT /deals` is now capped at 1000000000000
  (10,000,000,000.00 in major units) so the pipeline sums keep ample headroom; larger values are
  refused with a 400 naming the bound.
- **Deals with stage history.** A sales opportunity with a fixed pipeline and an append-only
  record of every stage it has been through.
  - `/deals` (admin and sales; sales sees and edits its own, delete is admin only): list with
    search over title and notes, server-side filters (`stage`, `open=true`, `company_id`,
    `customer_id`, `owner_id`), a sort allowlist and pagination; get with owner, company,
    customer and lead; create and update with validation of the links (400
    `INVALID_REFERENCE`; on update, links are checked only when the request sets them, so a
    deal keeps links to erased records and stays editable); `POST /deals/{id}/stage` and
    `GET /deals/{id}/history`;
    `GET /companies/{id}/deals` and `GET /customers/{id}/deals`.
  - Stages `qualification`, `proposal`, `negotiation`, `won`, `lost` with default probabilities
    10/40/70/100/0; `won` is always 100 and `lost` always 0; `closed_at` is set on entering a
    closed stage and cleared on leaving it; `lost_reason` is kept only while lost. Every stage
    change, the create included, writes one history row in the same transaction; asking for the
    current stage again writes nothing.
  - Money is an integer amount in minor units (`amount_cents`) next to a three-letter currency
    code; the default currency is the new `deals.default_currency` setting (category `deals`,
    shipped as `EUR`, admin-editable, and exposed on `GET /configurations/ui`).
    `expected_close_date` is a plain `YYYY-MM-DD` date on the wire.
  - Companies report `deal_count`, and deleting a company also clears `company_id` on its deals.
  - Deals hold no personal data: erasing a linked customer, lead or user leaves them and their
    history intact.
  - Migration `20260930120000_add_deals` for MySQL and MariaDB (two new tables, nothing altered);
    auto-migration covers SQLite, populated files included.
- **Companies.** A curated organisation record next to the free-text company field of leads and
  customers.
  - `/companies`: list with search (name, domain, industry, city), a sort allowlist and
    pagination; get with the owner and live customer and lead counts; create and update with the
    domain normalised (lower case, no scheme, path or `www.`) and unique among live companies
    (409); admin-only delete that clears the links from leads and customers in the same
    transaction. Reading is open to admin, sales and support; writing to admin and sales.
  - `GET /companies/{id}/customers` and `GET /companies/{id}/leads` page through the linked
    records; sales sees only the leads it owns.
  - Leads and customers accept an optional `company_id` on create and update (unknown → 400
    `INVALID_REFERENCE`; on update `0` clears, absent keeps) and return `company_record` on their
    detail endpoints. The free-text `company` column and the public forms are unchanged.
  - Erasure is unchanged: `company_id` is a business link and stays on the erased row; companies
    hold no personal data.
  - Migration `20260929120000_add_companies` for MySQL and MariaDB; auto-migration covers SQLite.
- `LICENSE`: GopherCRM is released under the MIT License. The README no longer describes the
  licensing as unresolved.
- **E2E on SQLite in CI.** The e2e suite now also runs against SQLite on every pull request
  (job "E2E (Playwright on SQLite)", no database service). `make e2e E2E_DB_DRIVER=sqlite` runs it
  locally on a new SQLite file and leaves the `gocrm_e2e` MySQL database alone; the default
  (`mysql`) is unchanged.

### Changed

- Refresh-token persistence validation now uses auth/session and transaction boundary checks,
  named SQLite/MySQL rotation regressions, migrations and auth browsers; CI adds MariaDB.
  Shared repository-interface changes qualify only with proof that other declarations are
  unchanged and all six required rotation boundary roots are present in the candidate.
  Unrelated entity browsers and frontend unit suites are excluded from this
  bounded profile; explicit full/release validation remains complete.

- **Lookup indexes for leads and companies (#109).** `leads.email`, `leads.classification`,
  `leads.external_id` and `companies.domain` are now indexed, non-uniquely (a person may be
  captured as a lead twice; a soft-deleted company keeps its domain while a live one reuses it).
  The duplicate-domain check compares the stored, already lower-cased domain with plain equality
  instead of `LOWER(domain)`, which no index could serve on SQLite or MySQL. MySQL/MariaDB
  upgrades run `migrations/20261029140000_add_lead_company_lookup_indexes` (idempotent after an
  AutoMigrate start, lower-cases any domain written outside the application; rollback drops the
  four indexes); SQLite gets the same indexes from AutoMigrate, and application startup
  (`models.MigrateDatabase`) lower-cases such domains on every driver, so SQLite installs, which
  never run the SQL migration, keep catching a mixed-case duplicate. Query plans before and after are
  recorded in `docs/datamodel.md`.

- **CI runs for the merge queue.** The workflow also runs on `merge_group` events, so the suite
  passes on the queue's temporary branch, on top of the current `main`, before a pull request
  merges. Production deploys still follow only CI runs of pushes to `main`.

- **Bulk status service narrowed (internal, no API change).** `POST /leads|tickets|tasks/bulk/status`
  now run on a `BulkStatusService` holding only the three status operations, over a
  `BulkStatusRepository` (the read and write halves of a status change) and a
  `BulkOperationRepository` reduced to the history-row create and outcome update. Responses,
  authorization, all-or-nothing transactions, `error.details`, the `bulk_operations` history
  rows and the lead automation events are unchanged; new SQLite/MySQL regression roots pin the
  rollback, history and event behaviour (#112).

### Removed

- **`repository.NewCustomerRepositoryWithLeadErasure` (#108).** The identity cascade now lives in
  the plain user, customer and lead repositories, so no constructor can erase half a person;
  `cmd/main.go` uses `NewCustomerRepository`.
- **The unused generic bulk subsystem (#112).** Nothing routed it since the generic
  `/bulk/:resource` handlers were dropped: the `ProcessBulk*` dispatchers, the
  `BulkCreate*`/`BulkUpdate*`/`BulkDelete*`/`BulkAction*` service methods for users, leads,
  customers, tasks and tickets, the matching repository mutations (including the batch-erasure
  wrapper and its per-item savepoint helpers, which only that path used), the bulk-operation read
  methods, their mocks and tests, and the request/response DTOs. Single-record erasure is
  untouched. No table or column is dropped: `bulk_operations` keeps receiving one row per status
  update, and `bulk_operation_items` stays in the schema although nothing writes it any more
  (retirement is a separate roadmap item).
- **Customer fields the API never stored (#118).** The customer form no longer offers Industry,
  Number of Employees, Annual Revenue or the "Active Customer" switch, the customer list drops
  its Status column and the customer detail page drops the Industry, Annual Revenue and Employees
  fields and the Active/Inactive chip. None of them exists on the customer API: saves discarded
  what was typed and reads showed invented defaults (blank, zero, always active). The Company
  record already owns industry and employee range, and customers link to it; there is no
  customer activation concept. The frontend `Customer` type and the read adapter no longer carry
  the four fields. No backend change.

### Fixed

- **`make build` and `make run` work again.** They compiled only `cmd/main.go`, but the `cmd`
  package now spans several files, so both failed with `undefined: drainAEO`. They build the
  package (`./cmd`) now, and the documented run commands use `go run ./cmd`.

- **Mail to internationalised domains the CRM accepts (#180).** The SMTP transport refused some
  non-ASCII domain labels the CRM's email validation accepts, for example `info@col·legi.cat`
  (U+00B7, allowed by RFC 5892), with `ErrInvalidAddress` before dialling even when the relay
  advertises SMTPUTF8, so password resets and quote emails to such saved addresses failed. A
  domain label may now hold any non-ASCII character except controls (format controls included,
  ZWNJ/ZWJ excepted) and whitespace; the address is delivered with SMTPUTF8 and still refused
  before `MAIL FROM` when the relay does not advertise it. Domain literals, CR/LF, `<` and `>`
  stay refused everywhere, the local part keeps its rules, and no refusal echoes the address.
  Regression root: `TestSMTPInternationalisedDomains` (`internal/mailer/smtp_idn_test.go`).
- **Cancelled requests stop lead conversion, form submissions and bulk status updates (#107).**
  `POST /leads/{id}/convert`, `POST /forms/public/{key}/submissions`,
  `POST /forms/public/confirm` and `POST /{leads,tickets,tasks}/bulk/status` used to run their
  database work and the reCAPTCHA check on a fresh background context, so a client that
  disconnected or timed out left the request queued for SQLite's single pooled connection, a
  MySQL row lock or the captcha service, and its writes landed afterwards. The handlers now pass
  the request context through the services into the transaction manager, the reads that precede
  the transaction (the conversion's ownership check included), the spam row, the bulk history row
  and the captcha call: a cancelled request returns at once with nothing written, a confirmation
  link stays usable, and a captcha check abandoned by the visitor's request is no longer filed as
  captcha spam. A bulk batch cancelled after its history row is written is recorded as failed by
  a write bounded to 5 s; if the connection or row is still held when that expires, the row stays
  pending and a warning is logged. Conversion, submission, confirmation and the bulk updates are transaction roots: called inside an
  enclosing transaction they now refuse with `utils.ErrTransactionRootRequired` and write or send
  nothing (no caller does so today). Responses to requests that are still connected are
  unchanged, and mail still goes out only after the commit. The conversion settings are still read
  without the request context. AEO runs keep their own lifecycle context.
  `TransactionManager.DB(ctx)` returns the request-bound handle (the active transaction when
  there is one) for statements outside a managed callback.

- **Overflow menus on the lead, customer, user, ticket and task lists (#114).** The "more"
  button sat in the table toolbar and acted on a selected record that nothing ever set, so it
  never opened. Each row now has its own menu, named for the row ("More actions for <name>"),
  that carries only what the row's view/edit/delete icons do not: Convert to Customer on leads
  whose status allows conversion, Create Ticket on customers for admin and support, Activate or
  Deactivate on users (deactivating your own account is disabled, as the API refuses it), and
  Mark as Complete on tasks that are not completed. The ticket list's menu only repeated view,
  edit and delete under the same role rules as the row icons, so it is gone. Every menu acts on
  its own row whatever the sort, filter or page, and a click inside it no longer falls through to
  the row. `DataTable` gains a `rowActions` slot and the new `RowActionsMenu` component.
- **Lead owner changes reach the API (#119).** `leadsApi.updateLead` now sends `owner_id` when the
  caller provides it and still omits it otherwise; the API keeps reassignment admin-only.
- **Role guards on the lead routes and customer creation (#120).** `/leads`, `/leads/new`,
  `/leads/{id}` and `/leads/{id}/edit` and `/customers/new` now sit under an admin-and-sales
  route guard matching the API, so a support or customer deep link lands on Access Denied
  instead of a page whose requests all fail. The customer list hides "Add Customer" from the same
  roles. Customer list, detail and edit routes are unchanged.
- **Paginated lists no longer repeat or skip rows that share a sort value (#110).** The lead,
  customer, task (including the label filter), ticket, user and company lists and searches, and
  the company customer and lead sub-lists, now always end their `ORDER BY` with the table's id:
  ascending id without a sort, otherwise the requested column then id in the same direction, the
  id qualified with the table so the label join stays unambiguous. Before, tied values (every row
  of one import shares `created_at`) came back in whatever order the engine chose per statement,
  and on MySQL consecutive pages repeated some rows and never showed others. The support-scoped
  ticket list used to break ties by ascending id even for a descending sort; it now follows the
  sort direction like the rest.
- **Lead conversion keeps the company link and enforces the conversion settings (#104).**
  `POST /leads/{id}/convert` copies the lead's `company_id` to the new customer when it still
  names a live company (a link to a deleted company is dropped and the conversion still succeeds),
  and now enforces the three lead-conversion settings on the server, before anything is written:
  the lead's status must be in `leads.conversion.allowed_statuses`, non-blank `notes` are required
  when `leads.conversion.require_notes` is on — a refusal is 400 `VALIDATION_ERROR` whose
  `details` name the field (`status` with the allowed statuses, or `notes`) — and with
  `leads.conversion.auto_assign_owner` on the lead's owner becomes the customer's
  `assigned_to_id` (never `user_id`). Before, all three settings were ignored by the server. The
  settings are read on every conversion; a failed read is a 500 with nothing written. The
  allowed-status `valid_values` listed `lost`, which is not a lead status, instead of
  `unqualified`; the list is now `new, contacted, qualified, unqualified`, and the boot-time seeder
  corrects the metadata of existing rows while keeping the statuses an administrator chose.
  `GET /configurations/ui` now also carries read-only `leads.conversion.require_notes` and
  `leads.conversion.auto_assign_owner` entries next to the allowed statuses, with the values the
  server enforces. The conversion is now the only way to reach `converted`: lead create, lead
  update and `POST /leads/bulk/status` refuse it with 400 `VALIDATION_ERROR` (`details.field`
  `status`), where they used to mark a lead converted without a customer and outside the
  settings; a converted lead keeps its status on edit, and the lead form no longer offers
  "Converted". The conversion reads the lead with a row lock and a lead update re-reads it under
  the same lock, so a concurrent status change either lands first (the conversion is refused) or
  after the commit (and keeps the new `customer_id`) instead of being lost or bypassing the
  allowed statuses. A lead that already has a customer is refused as already converted even if its
  status has since moved on (it used to be converted a second time). A lead erased between the
  handler's lookup and the conversion is a 404, not a 500. Refused conversions, duplicate emails,
  already converted or missing leads are logged as warnings by both the handler and the service;
  only server failures are logged as errors. Known gap: a company deleted while a lead converts can still leave the new customer
  linked to the soft-deleted company (tracked in #182).
- **The UI loads its configuration after sign-in and follows the conversion settings (#117,
  #104).** The configuration provider never requested `GET /configurations/ui`, so every page used
  built-in defaults — the Convert button appeared only on `qualified` leads whatever the
  administrator allowed. The provider now loads the list once a session is established (never on
  the sign-in or registration pages, so it cannot start a refresh or redirect there), keyed by the
  signed-in user: signing in loads it, signing out clears it, and switching accounts loads the new
  account's values without ever showing the previous ones (the session now drops cached queries
  before the new account renders, so its first requests are not discarded). The provider, the deal
  form and the default-currency hook share this one authenticated query. Display values (company
  name, primary colour) fall back to built-in defaults; the conversion rules never do: while they
  load, after a failed load, or when the server leaves out the allowed statuses or the notes rule
  (it omits a setting it could not read), the lead page shows Convert to Customer disabled with
  "Loading conversion settings…" or "Could not load conversion settings" and a Retry, the lead list
  shows the row action disabled on every unconverted lead plus a warning with Retry, and the
  dialog claims no rule and does not convert. A save or reset on the Settings > Configuration page
  refreshes the list. Once known, the lead list menu and the lead page offer Convert to Customer on
  the configured statuses (an empty list hides it). The
  conversion dialog requires notes when `leads.conversion.require_notes` is on (blank notes are
  refused before the request, with the server's wording), says that the lead's owner becomes the
  customer's assigned user when `leads.conversion.auto_assign_owner` is on, and stays open on a
  refusal: a notes refusal is shown on the notes field, any other (status not allowed, lead already
  converted, no email) above the form with the server's message, in place of the earlier toast. A
  refusal re-reads the settings (status or notes) or the lead (any other), so a stale page catches
  up, and a re-read keeps what the user typed; when the re-read settings or lead no longer allow the
  conversion, the open dialog says why and disables Convert. Moving from one lead to another closes
  the dialog and drops the previous lead's refusal, also when the user returns to it; a conversion that answers after the user moved on no longer
  opens that customer.
- **Erasure follows the customer login (#108).** A customer record and its own login
  (`customers.user_id` pointing at a `customer`-role account) are now erased together, from either
  end and through a converted lead, with every lead converted into those customers, in one
  transaction. Erasing a login used to leave its customer records holding the person's name,
  email, phone and address; erasing a customer left the login, its password hash and working API
  keys and refresh tokens behind. Staff accounts are never erased this way: a staff user in
  `user_id` is left alone by a customer or lead erasure, a staff user's own erasure leaves the
  customers pointing at it alone, and owners, assignees and account managers are never touched.
  Each person is scrubbed exactly once per erasure, cycles terminate, and a failure anywhere rolls
  back the whole graph. Limits: an erasure can still miss a conversion, link or credential committed
  concurrently (#183); graphs erased before this change are not repaired automatically, and fully
  soft-deleted ones have no supported path yet (`docs/DEVELOPMENT.md` lists finder queries for the
  rest); business free text, legacy `bulk_operation_items` payloads and unlinked form submissions
  are retained by design.
- **Only an admin can delete a converted lead.** Erasing a converted lead erases the customer it
  became (and now that customer's login), and deleting a customer is admin-only, but the owning
  sales user could do it through `DELETE /leads/{id}`. The sales owner now gets 403 for a
  converted lead, decided inside the erasure transaction before anything is written; deleting an
  unconverted lead is unchanged. `LeadService.Delete` takes the caller's role, and the lead
  repository gains `DeleteUnconverted`.
- **An erasure joined to a caller's transaction is all-or-nothing.** Repositories obtained through
  `WithTx` now bracket the erasure in a checked `SAVEPOINT`, released on success and rolled back on
  an error or a panic; a caller that handled a failed erasure (or recovered its panic) and
  committed used to keep its partial writes (some rows scrubbed, some credentials purged). A failed
  `ROLLBACK TO SAVEPOINT` rolls the caller's transaction back and is reported with
  `ErrRollbackFailed` and `ErrTransactionPoisoned`. The savepoint statements go to the underlying
  transaction, so they also work with GORM's `PrepareStmt` on MySQL, which cannot prepare them.
- **Soft-deleted ticket comments are redacted on user erasure.** The redaction skipped comments
  that had been soft-deleted earlier, which kept their bodies.
- **The session that changes the password stays signed in (#116).** `POST /auth/change-password`
  is now one transaction: the owner row is locked, the hash it was verified against is re-checked,
  the caller's current refresh token (new optional `refresh_token` body field) is claimed, the new
  hash written, every other refresh token revoked and the replacement session issued. Any failure
  before the commit rolls all of it back — the revocation is no longer best-effort after a committed
  password (a failed COMMIT stays ambiguous, as for every transaction). The
  response carries `token`, `refresh_token` and `user` when the session was renewed; a presented
  token that is not a live session of the caller is `409 SESSION_STALE`; API-key requests never
  receive session credentials. The Profile page stores the replacement pair in the storage the
  session already used and no longer promises that other devices are signed out immediately
  (their access tokens keep working until they expire). Refresh rotation now locks the owner row
  before claiming the token, the order every session transaction follows, so a password change
  and a concurrent refresh can no longer deadlock on MySQL/MariaDB.
- **The 401 refresh interceptor settles every request (#115).** One shared refresh per session;
  every waiting request is resolved or rejected on every outcome; every replay is marked so a
  second 401 is final; a refresh that does not answer within 15 s rejects its waiters on its own;
  requests are tagged with the session they were sent under and never replayed, nor their late
  refresh stored, after logout or a sign-in to another account. A refresh rejected with 400/401
  ends the session; a timeout, network error or 5xx keeps the stored tokens. `/auth/refresh`
  answers operational failures with `503 SESSION_REFRESH_UNAVAILABLE` instead of the generic 401,
  so a transient database error no longer signs the user out. Logout drops the stored tokens at
  once, waits for a refresh already on the wire and authenticates the server-side revoke-all with
  the successor of a refresh or password change still running (kept privately, never stored, and
  only when it belongs to the session being ended) or, when its access token has expired, with a
  private renewal. Every operation is bound to the session it was requested under (generation,
  account and refresh-token lineage), so another tab signing out or signing in another account
  never makes this tab send, write or revoke as the wrong account. The client reads the refresh token only from
  the storage that holds the access token and a new sign-in replaces both keys. Cross-tab refresh
  coordination remains open (#184).
- **A login paused across an erasure can no longer restore the erased account.** Login
  bookkeeping, password writes and profile updates wrote the whole user struct with GORM `Save`,
  which falls back to an upsert when its update matches no row: a login that read the account
  before an erasure wrote the name, email and password hash back and cleared `deleted_at`. User
  writes are now conditional, column-specific updates of a live row (`UserRepository.UpdateFields`;
  the full-struct `Update` is gone), and such a login is refused. Re-verifying the password under
  the owner lock at login is tracked in #183.

- **An automation rule update no longer undoes a concurrent disable.** `PUT
  /automation/rules/{id}` without `enabled` now keeps the switch as read under the rule's row lock
  inside the update transaction; before, the handler read the rule first and wrote that value
  back, so a disable committed in between was silently reverted.
- **`make swagger` resolves the notification handler's types.** The handler now imports the
  models package its annotations reference, so the spec generates again.

- **Runner recovery and gate gaps.** Every `gocrm-ui/vitest.*` file is now an existing
  validation gate, because Vitest discovers `vitest.workspace.*` and `vitest.projects.*` beside
  its configuration; previously only `vitest.config*` was protected. `retire-issue` now also
  requires `--processes-stopped` when a validation command is recorded as running, and records
  its disposition in `check_dispositions`. A Vitest receipt now records the validation checkout
  that produced it and is verified against that checkout, so a receipt reused across two
  interrupted resumes no longer fails the exact-path match and discards every retained check.
  A failed `gh` invocation (for example a TLS handshake timeout against the GitHub API) is now
  an environment failure: validation stops at its stage, records the attempt as `environment`
  and is resumed by the same command, instead of treating the failure as a test failure and
  spending a provider call and a repair round on an agent with nothing to change.
- **The remediation runner no longer strands an implementation that changes a path outside the
  frozen task scope.** Finalization checked the frozen scope before committing, so a single
  out-of-scope edit left the agent's work uncommitted at `finalize_implementation`, where `run`
  repeated the same refusal, `freeze --refresh` required a revalidation first and `revalidate`
  refused both the stage and the dirty worktree. Protected and private paths and changed
  validation gates are still refused before anything is written. The scope check now runs after
  the commit: the implementation and its claims are kept on the runner branch, `last_error`
  records the violation and the execution stops at `validate`, where the same check still blocks
  testing, review and publication. The operator inspects the diff and either widens the scope
  (`revalidate` when main has advanced, then `freeze --refresh` and `run --execute`, which
  validates the existing commit without another implementation call) or leaves the execution
  for an explicit disposition.
- **Feature analysis checks anticipate the execution gates.** The specification and revision
  stages now report context references that are not Markdown, are missing at the base commit or
  exceed the 128 KiB guidance cap; Go test-plan names that are not bare identifiers or appear in
  more than one registry entry; `./cmd/migrate` registry entries whose `-run` pattern and tests
  do not cover `TestMigrationFresh_FullUpAndDown` and
  `TestMigrationUpgrade_FromMainAutoMigratedSchema`; and migration or CHANGELOG changes whose
  scope omits `cmd/migrate/*_test.go` or `CHANGELOG.md`. An over-budget one-pager note states
  the excess and the largest fields, and the revision reports a one-pager it did not shrink. The
  stage rules state each requirement.
- **The remediation runner waits for an in-progress workflow run before it collects failed-job
  logs.** A required check that failed while other jobs of the same run were still running made
  the runner request the run's failed-job logs, which `gh` refuses until the run completes; the
  ticket or PR completion then stopped on that refusal instead of entering its repair loop. The
  runner now waits, within the CI deadline, until every failed run has completed and then fetches
  the logs once.
- **Sorted customer CSV exports no longer lose rows (#105).** `GET /customers/export` read the
  book in 500-row primary-key batches through GORM's `FindInBatches` while also applying the
  requested `sort_by`. The batch continuation is `id > last id of the previous batch`, which is
  only a valid cursor under id order: with any other `sort_by`, every customer whose id was below
  the previous batch's last id was skipped, so a sorted export of more than 500 customers silently
  came back incomplete while looking like a whole file. The repository now reads every matching row
  with one ordered statement, ordered by the requested column and then by id, both in the requested
  direction (id ascending without `sort_by`), so duplicate values, NULLs in the legacy nullable
  `company` column and collation ties order deterministically, `desc` is the exact reverse of
  `asc`, and the read is one consistent snapshot on MySQL and SQLite. The sort allowlist,
  invalid-parameter handling, search filter, erasure exclusion, CSV header and quoting, formula
  prefix, admin-only guard and the all-or-nothing fetch before the response are unchanged.
  Regression roots: `TestCustomerExportTraversal{SQLite,MySQL}` in
  `internal/repository/customer_export_traversal_test.go` and `TestCustomerExportCSV{SQLite,MySQL}`
  in `tests/customer_export_csv_test.go`, both on more than 1,000 rows whose sort values run
  against id order with duplicates straddling the old batch boundaries; the MySQL roots use a
  disposable schema on `TEST_MYSQL_DSN`.
- **Refresh token rotation is atomic and single-use (#103).**
  `POST /auth/refresh` used to read the presented token, revoke it with an unconditional update
  and insert its replacement as independent writes: two simultaneous requests could both pass the
  lookup and both mint a successor, and a failure after the revocation stranded the session with
  no usable token. The rotation is now one transaction through the transaction manager and the
  `WithTx` repositories: the token is claimed by a new conditional
  `RefreshTokenRepository.ClaimForRotation` (not revoked, strictly unexpired, exactly one affected
  row), the owner is re-read with the same locking read as the password reset (so an account
  disabled by another MySQL connection right after the token lookup is refused instead of served
  from the stale snapshot), the successor is inserted and its access JWT signed before the commit.
  A failure of the insert or of the signing rolls the claim back, so the presented token stays
  usable, no extra row exists and no credentials are returned; competing refreshes of one token
  commit exactly one successor and the losers and any later replay receive the generic 401
  without touching the winner. An inactive, erased or missing owner is still refused generically
  and still has the presented token revoked: that containment revocation is committed with the
  rejection and no successor is created. Logout keeps its unconditional, idempotent
  `RevokeByTokenHash`; a rotation is refused rather than run as separate writes when the
  transaction manager is not wired. Public errors and API responses are unchanged. Regressions run
  on SQLite and on a disposable MySQL schema
  (`TestRefreshRotation{Validation,Atomicity,Concurrent}{SQLite,MySQL}`), with the insert fault
  injected through the transaction-bound repository and the signing fault through an
  instance-local seam; see `docs/testing/01-authentication.md` TC-AUTH-033a. Client-side
  refresh coalescing is the #115 entry above.
- **Password reset confirmation is atomic and single-use (#102).**
  `POST /auth/password-reset/confirm` used to mark the token used by an unconditional update
  before hashing and writing the password, and revoked sessions only best-effort afterwards: a
  failure after the stamp burnt a usable link, two simultaneous confirmations could both pass the
  lookup, and a failed revocation left stolen sessions alive next to the new password. The new
  password is now validated and hashed before anything is written, so a weak or unhashable
  password leaves the link usable. The token is then claimed by a conditional update (unused,
  strictly unexpired, exactly one affected row) in the same transaction that re-checks the owner
  is active and not erased, writes the password, clears the lockout and revokes every refresh
  token. The owner re-check is a locking read (the transaction-bound `UserRepository`): on MySQL a
  plain read inside the transaction answered from the snapshot taken at the token lookup, so an
  account disabled by another connection right after that lookup still looked active and the
  password update wrote the stale `is_active` back. Any failure rolls the whole unit back and
  the same link works on retry; competing
  confirmations commit exactly once with the winner's password; a confirmation is refused rather
  than run as separate writes when the transaction manager is not wired. Public errors and API
  responses are unchanged. Regressions run on SQLite and on a disposable MySQL schema
  (`TestPasswordReset{Validation,Atomicity,Concurrent}{SQLite,MySQL}`, plus the two-connection
  `TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL`); see
  `docs/testing/01-authentication.md`.

- **Runner validation progress is retained and failed checks are recoverable (#146).**
  Early Go assertion diagnostics are preserved when later passing tests fill a failed
  command's log, and expected structured service logs are distinguished from real
  environment failures. A shared, commit-pinned `repair-validation` recovery command queues
  an inspected source repair while retaining failed attempts, spent budgets and all
  validation/review gates. Validation resume verifies and retains successful checks for the
  unchanged candidate, then runs missing checks in a fresh checkout instead of repeating
  completed suites. Unusable checkpoints are retained with their invalidation reason while
  every selected check runs again under a new validation identity and the original limits.
  Both JSON and text-format service logs are recognized. See `automation/langflow/README.md`
  for the operator procedure.

- **Form confirmation tokens are consumed atomically (#101).** Confirming a double-opt-in
  submission used to stamp the token as used before the transaction that created or linked the
  lead and confirmed the submission, by an update on the id alone. A failure in that transaction
  consumed a usable link, and two simultaneous clicks could both pass the lookup. The token is now
  claimed inside the same transaction by a conditional update (unused, strictly unexpired, exactly
  one affected row) before the pending submission is re-read and the lead and submission are
  written; a lead or submission write failure rolls the claim back so the same link works on
  retry, competing requests yield exactly one confirmation, and the team notification and the
  follow-up mail go out only after the commit. Public errors and API responses are unchanged.
  Regressions run on SQLite and on a disposable MySQL schema (`TestFormConfirmation{TokenClaim,
  Rollback,Concurrent}{SQLite,MySQL}`); see `docs/testing/12-forms.md`.

- Constrain runner implementation reports to the frozen acceptance IDs before provider
  generation. Descriptive criterion strings, unknown IDs and duplicates are rejected by
  both the response schema and parser; existing PRs without criteria claim an empty list.
  This prevents completed edits being stopped by a schema/parser mismatch. Tests and
  independent review remain the acceptance evidence.

- **Transaction manager: panics, nesting and finalization failures (#106).** A panic inside
  `WithTransaction` now rolls the transaction back before propagating unchanged, so SQLite's
  single pooled connection is released instead of held by an abandoned transaction. A call made
  inside an active transaction reuses it under a savepoint rather than beginning a second root
  transaction (which blocked forever on that pool): an inner failure rolls back to its savepoint
  while the outer unit may still commit, and an outer failure discards every nested write. Commit
  and rollback failures are reported with `errors.Is`-inspectable sentinels alongside the
  callback's own error, a rollback already performed by context cancellation is not an error
  (a nested call whose enclosing context ended reports the cancellation, not a failed cleanup),
  and `WithTransactionAndRetry` replays only attempts that rolled back cleanly, never an
  ambiguous commit; inside a transaction it runs once and defers a retryable failure to the
  enclosing root retry. A negative retry count is rejected without running the callback. A failed
  `ROLLBACK TO SAVEPOINT` poisons the transaction (`ErrTransactionPoisoned`): an outer callback
  that recovers the nested panic or swallows the inner error and returns nil no longer commits the
  possibly retained inner writes; every enclosing call returns the cleanup error, new nested calls
  are refused, and the root rolls back instead. See the transaction section of
  `docs/DEVELOPMENT.md`.
- Expired login lockouts now start a fresh failure window instead of relocking on the
  first mistyped password; active locks and generic public authentication failures remain enforced.
- **AEO profile updates on MySQL.** Saving an existing brand profile preserves its
  creation timestamp instead of writing a zero date, which strict MySQL rejected with a 500.
  Saves return the stored timestamps and continue to allow clearing optional fields.
- **SQLite auto-migration on a populated file.** Adding a foreign key to an existing table makes
  the SQLite driver drop and recopy the table, which foreign-key enforcement rejected as soon as
  another table referenced its rows: a 1.2.0 file upgraded only if no row in `tickets` or
  `tasks` and no converted lead referenced a customer or lead, which rules out any database that
  has been used. Auto-migration on SQLite now runs with enforcement off, as SQLite prescribes for
  table rebuilds, verifies every foreign key afterwards and refuses to start on a violation. A
  fixture test upgrades a populated v1.2.0 file, including one left half-applied by a failed
  attempt; MySQL and MariaDB are unchanged.

### Security

- **SMTP delivery is bounded and validates its headers (#113).** The SMTP mailer no longer calls
  `smtp.SendMail`, which had no timeout: a relay that stalled at the greeting or any later command
  held the calling request or worker indefinitely. Each delivery now dials within 10 s and runs
  the whole conversation (greeting, EHLO, STARTTLS, AUTH, MAIL/RCPT/DATA, QUIT) under one 30 s
  deadline that is a hard bound: the raw connection is closed on every path and, at the latest,
  when the deadline passes, so TLS cleanup cannot add its own grace period. STARTTLS stays
  opportunistic — used whenever the relay advertises it, and a failed handshake aborts the
  delivery; PLAIN credentials are never sent over an unencrypted connection to a host other than
  localhost, and configured credentials against a relay that offers no AUTH fail instead of
  submitting unauthenticated. The recipient and the configured `SMTP_FROM` must each be one RFC
  5322 address (display names allowed). The envelope address is serialised safely: a dot-atom
  local part as is, any other local part re-quoted (`RCPT TO:<"a b"@example.com>`) — `net/mail`
  returns quoted local parts unquoted, so `"x> NOTIFY=NEVER"@example.com` would otherwise have
  reached `RCPT TO` as extra ESMTP parameters. Control characters, `<` or `>` anywhere in the
  address (even quoted) and domain literals are refused with `mailer.ErrInvalidAddress` before any
  relay is contacted; an internationalised address is delivered only when the relay advertises
  SMTPUTF8 and otherwise refused before `MAIL FROM`. CR or LF in the sender is refused like the recipient and subject
  (`mailer.ErrInvalidHeader`), the envelope carries the bare address (previously a recipient with
  a display name produced a malformed `RCPT TO`), and a non-ASCII subject is RFC 2047 encoded and
  folded into lines of at most 76 characters. A relay reply such as a 535 is returned as it is even
  when the abort that follows runs into the deadline; only I/O failures caused by the deadline are
  reported as `os.ErrDeadlineExceeded`. The body, declared UTF-8, is now sent quoted-printable instead of raw 8-bit, with every
  line ending (including a bare CR) normalised to CRLF and long lines soft-wrapped. Failure logs
  no longer carry the subject (automation subjects can expand `{{lead.email}}`) or, for the
  password reset, the recipient; transport errors go through `utils.LogError`, which now also
  reduces a malformed relay reply (`textproto.ProtocolError`) to `smtp_protocol_error`.
- **Service and HTTP logging privacy.** The sample `LOG_LEVEL` is now `info`. Service logs omit
  emails, names, titles, search terms and raw sort values, while shared helpers omit argument,
  result and handler response payloads (including issued credentials). Access logs omit query
  strings. Logged errors redact quoted value suffixes, form validation logs summarize the field
  count, and SMTP failures retain the protocol code without relay response text. Operation names,
  IDs, validated statuses, counts and request timings remain. API responses and returned errors
  are unchanged; other raw diagnostics, request/header/IP metadata, mailer recipients and older
  logs still require a retention policy.
- **The SQL log follows `LOG_LEVEL` and never carries parameter values.** The GORM logger was
  fixed at its most verbose level, so every statement went to stdout with its bound values
  (email addresses, names, token hashes) whatever `LOG_LEVEL` said. Statements are now logged
  only at `debug` or `trace`; every other level logs failed and slow statements only. At every
  level statements are printed with `?` placeholders instead of the bound values, and quoted
  values in driver error messages (MySQL's `Duplicate entry 'alice@example.com'`) are redacted
  as well. A record-not-found result is no longer logged as a failed statement.

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
