# E2E Test Suite — GopherCRM

123 end-to-end tests across 12 spec files, run with Playwright against the Vite frontend and a
real backend on MySQL (or MariaDB) or SQLite.

Only the `chromium` project is configured. Tests run serially (`fullyParallel: false`, `workers: 1`)
because they share one database.

## The standard way: `make e2e`

From the repository root:

```bash
(cd gocrm-ui && npm ci && npx playwright install chromium)   # once per machine / after dependency changes
make e2e                                                    # whole suite
make e2e SPECS="e2e/tests/admin-leads.spec.ts"      # selected specs (paths relative to gocrm-ui/)
make e2e E2E_DB_DRIVER=sqlite                               # whole suite on SQLite, no MySQL needed
```

`make e2e` is one of the full gates and takes the machine-wide gate lock (`scripts/gate-lock.sh`),
like `make verify`: a second gate on the same machine waits for the first instead of running
alongside it and timing out for load. The waiting run prints who holds the lock every 30 s;
`GATE_LOCK=0` skips the lock, and CI (`CI=true`) never takes it. Details in
[docs/DEVELOPMENT.md](../../docs/DEVELOPMENT.md), "Gates and targeted checks".

`scripts/e2e/run.sh` does the rest, and CI runs the same script on every pull request, every
merge-queue group and every push to `main`, once per database engine (jobs "E2E (Playwright on
MySQL 8)", "E2E (Playwright on MariaDB 10.11)" and "E2E (Playwright on SQLite)"):

1. Drops and recreates the **`gocrm_e2e`** database (`E2E_DB_NAME`; it refuses any name not ending
   in `_e2e`), so every run starts empty and never touches the development database.
2. Builds the backend and starts it on a free port (from 18091) with `DISABLE_RATE_LIMIT=true`.
   Its log goes to `test-results/e2e-backend.log`.
3. Starts its own Vite server on a free port (from 15173) through `E2E_UI_PORT`, pointed at that
   backend, and runs Playwright with **no retries**, traces kept for failures, and the `line` + `html`
   reporters.
4. Always stops the backend and exits with Playwright's result.

The run pins the backend's environment: the chosen driver, the default API prefix, no SMTP (the
log-only mailer), no reCAPTCHA, no answer-engine keys and no AEO schedule, whatever the root `.env`
says. On macOS it keeps the machine awake (`caffeinate -i`), since an idle sleep mid-run shows up as
a cascade of page-load timeouts.

Database credentials come from the environment, falling back to `DB_*` in the root `.env`. The
MySQL user needs privileges on `gocrm_e2e.*` (for example
`GRANT ALL PRIVILEGES ON gocrm_e2e.* TO 'gophercrm'@'localhost'`).

### On SQLite

`E2E_DB_DRIVER=sqlite` (default `mysql`) runs the same steps without a database server. Step 1
changes: nothing is dropped or created, `gocrm_e2e` is not touched, and neither the `mysql` client
nor any `DB_*` credentials are needed (`E2E_DB_NAME` is ignored). The backend gets
`DB_DRIVER=sqlite` and `DB_PATH` pointing at a new, empty file in the run's temporary directory, and
`create-admin` in global setup inherits the same two variables, so both write to that file. On exit
the file is copied to `test-results/e2e-sqlite.db` next to the backend log for inspection (it does
not live there during the run, because Playwright empties `test-results/` when it starts). Only
`JWT_SECRET` is still required, from the environment or the root `.env`.

`E2E_PLAN_ONLY=1` (exactly `1`; any other value runs the suite) prints the resolved plan (driver,
database or file, reset, required tools, ports) and exits before anything is used.
`scripts/e2e/selftest.sh` (part of `make verify-hygiene`) checks both modes through it, and then
starts the runner with `mysql`, `go`, `npx` and `curl` replaced by recording shims to prove that a
SQLite run never calls `mysql` while a MySQL run resets `gocrm_e2e` first.

New or changed user-visible behaviour brings its e2e spec in the same pull request. Destructive
steps act only on records the test created.

## Running by hand (debugging)

The manual setup below uses the development database and the dev server on 5173. The frontend
reads its API base URL from `VITE_API_BASE_URL` in `gocrm-ui/.env`, so start the backend on the
port that file names.

```bash
# Terminal 1 — backend (port must match VITE_API_BASE_URL)
cd /path/to/gophercrm && DISABLE_RATE_LIMIT=true SERVER_PORT=8090 go run cmd/main.go

# Terminal 2 — from gocrm-ui/
npm run test:e2e                    # all specs
npm run test:e2e:headed             # visible browser
npm run test:e2e:debug              # Playwright inspector
npm run test:e2e:ui                 # interactive UI mode
npm run test:e2e:report             # open the last HTML report
npm run test:e2e:admin              # admin CRUD specs, playwright.config.slow.ts
npm run test:e2e:admin:cleanup      # e2e/scripts/cleanup-admin-test-data.sh

# A single spec
npx playwright test e2e/tests/login.spec.ts
```

The Vite dev server is started automatically by the config's `webServer` block
(`reuseExistingServer: true`), so Terminal 2 is enough if one is already running. The backend is
**not** started for you.

`DISABLE_RATE_LIMIT=true` is worth setting: it bypasses the strict 10 req/min limiter on
`/auth/register` and `/auth/login`, which the login and registration specs would otherwise trip. It
does not disable rate limiting elsewhere — authenticated routes keep their moderate tier — so it is
not a way to run the suite faster, only a way to stop the login limiter from producing false failures.

## Admin Account

`test-admin@gocrm.test` / `AdminPass123!`, defined in `fixtures/admin-user.ts`.

It is seeded by `global-setup.ts`, which shells out to `go run ./cmd/create-admin -non-interactive`
from the repo root before any test starts. Re-running is harmless — the CLI exits non-zero when the
account already exists and global setup treats that as success.

Global setup then logs in as this admin and enables `security.allow_public_registration` through
the API (`helpers/registration-config.ts`): the switch ships **off**, and most of the suite —
the registration specs, the settings specs' throwaway accounts — assumes an open
`/auth/register`. This means global setup needs the backend to be up and reachable at
`VITE_API_BASE_URL`. The disabled state is covered by `registration.spec.ts`, which flips the
switch off and back on around its own tests. `global-teardown.ts` restores the switch to what
global setup found, so a screenshots or manual run against the dev backend on 8090 does not
leave that database's sign-up permanently reopened.

This account cannot be created through the UI or the API. `POST /auth/register` is public and always
creates a `customer`, ignoring any role in the request body, so an admin has to come from the CLI or
from an existing admin calling `POST /users`. Do not "fix" a failing admin spec by trying to register
an admin through the app.

## Coverage

| Spec | Tests | Area |
|------|-------|------|
| `login.spec.ts` | 11 | Auth — render, success, wrong password, unknown user, empty and invalid input, password visibility, register link, Enter key, protected routes, unauthenticated redirect |
| `registration.spec.ts` | 18 | Auth — success and redirect, validation (empty, email format, password complexity, mismatch), duplicate email, visibility toggle, Enter key, loading state, field preservation, navigation, network error, disabled switch (no sign-up link, notice instead of form, API 403) |
| `admin-tickets.spec.ts` | 14 | Tickets — list, create, view, edit, delete, status and priority filters |
| `admin-tasks.spec.ts` | 13 | Tasks — list, create, edit, view, delete, status and priority filters, minimal data |
| `admin-users.spec.ts` | 12 | Users — list, create, edit, view, delete, search, role filter |
| `admin-leads.spec.ts` | 11 | Leads — list, create, edit, view, delete, status filter, search, minimal data |
| `admin-customers.spec.ts` | 10 | Customers — list, create, edit, view, delete, search, validation, cancel, minimal data, duplicate email |
| `admin-companies.spec.ts` | 8 | Companies — list, create with all fields, validation, duplicate domain 409 on the field, edit, search, link a customer through the customer form and see it on both detail pages, delete and the customer falls back to its text company |
| `admin-deals.spec.ts` | 8 | Deals — list, create for a company with the formatted amount on both detail pages, validation, stage moves with the history growing, lost with a required reason (closed_at and reason shown, cleared on reopening), edit title and amount, stage filter, delete |
| `admin-deals-board.spec.ts` | 5 | Deals board — created deals in their stage columns with counts and totals, move to Negotiation through the card menu (both headers update), move to Lost with a reason under the expanded Lost column, dashboard "Won this month" tile counts a deal moved to Won, list/board toggle round-trip and remembered choice |
| `leads-sorting-search.spec.ts` | 8 | Leads — column sorting and search behaviour |
| `admin-entity-suite.spec.ts` | 6 | Cross-entity — navigation, CRM workflow, data isolation, quick creation, sidebar |
| `labels.spec.ts` | 11 | Task labels — create, duplicate name, attach to a task, inline creation, chips in list and detail, chip and dropdown filtering, rename/recolour, delete and detach |

Counts are per `test(...)` block and will drift; `npx playwright test --list` is authoritative.

## Layout

```
e2e/
├── global-setup.ts     # Seeds the admin account via cmd/create-admin (same DB_DRIVER/DB_PATH or DB_NAME as the backend)
├── fixtures/           # admin-user.ts (credentials + faker generators), test-data.ts
├── helpers/            # admin-auth.ts — login helper for the admin suites
├── pages/              # Page Object Models: one per screen, selectors live here
├── scripts/            # cleanup-admin-test-data.sh
└── tests/              # Specs — behaviour assertions only
```

Two Playwright configs: `playwright.config.ts` (default) and `playwright.config.slow.ts`, used by the
`test:e2e:admin` and `test:e2e:slow` scripts when longer timeouts are needed.

## Conventions

- Keep selectors in `pages/`, assertions in `tests/`. A spec that reaches for a raw CSS selector is a
  sign a page object is missing a method.
- Selector style: `input[name="..."]` for fields, `button[type="submit"]` for form submission,
  `[data-testid="EditIcon"]` for row actions, `[role="dialog"]` for confirmation modals.
- Generate entity data with the faker helpers in `fixtures/admin-user.ts`
  (`generateLeadData`, `generateCustomerData`, `generateTicketData`, `generateTaskData`,
  `generateUserData`). They embed a timestamp and a random suffix in each email so parallel or
  repeated runs cannot collide on a unique constraint.
- Labels are unique by name and **hard** deleted, so no tombstone reserves a name. `labels.spec.ts`
  scopes every name it creates to the run and deletes them all again; leftovers would stay visible
  on `/labels` forever and leak into the documentation captures. The screenshot suite
  (`screenshots/09-labels.spec.ts`) deliberately does the opposite — fixed names, created only when
  missing — so the captures stay stable across runs.
- Three `admin-entity-suite.spec.ts` tests (CRM workflow, bulk operations, cross-entity search) walk
  three entity forms each and run for 60–100 s locally and about 1.7× that on a CI runner. They
  carry their own 180 s budget (`MULTI_ENTITY_TEST_TIMEOUT_MS`), so no `--timeout` flag is needed.
- Companies are unique by domain among live rows, so `companies.page.ts` searches by domain
  (`generateCompanyData()` stamps one per run) rather than by row position.
- Deals are found by title: `generateDealData(stamp)` stamps one per run and `deals.page.ts`
  searches for it. MUI Selects (the stage filter, the stage selectors on the form and the detail
  page) and Autocompletes are addressed by `getByRole('combobox', ...)`, never `getByLabel`: once
  open, the listbox shares the label and strict mode would match two elements.
- The deals board (`deals-board.page.ts`) addresses a column as the region named by its stage and a
  card by its stamped title inside that column. Other deals share the database, so header counts are
  read before a move and compared relatively, never as absolute totals. The board's cards are the
  first 100 per stage sorted by expected close, so the spec gives its deals a near date. The List /
  Board choice lives in `localStorage` (`gcrm.deals.view`); each test starts with a fresh browser
  context, so a spec that picks the board does not leak into the next one.
- Never hardcode an email in a spec that creates records. Whether a fixed address is free depends on
  what earlier runs left behind, so the create step turns into an intermittent 409. The only
  hardcoded account is the seeded admin, which global setup owns.
