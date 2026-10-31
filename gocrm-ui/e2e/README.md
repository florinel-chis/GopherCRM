# E2E Test Suite — GopherCRM

End-to-end tests run with Playwright against the Vite frontend and a real backend on MySQL
(or MariaDB) or SQLite.

Only the `chromium` project is configured. Tests run serially (`fullyParallel: false`, `workers: 1`)
because they share one database.

## The standard way: `make e2e`

From the repository root:

```bash
(cd gocrm-ui && npm ci && npx playwright install chromium)   # once per machine / after dependency changes
make e2e                                                    # whole suite
make e2e SPECS="e2e/tests/admin-leads.spec.ts"      # selected specs (paths relative to gocrm-ui/)
make e2e E2E_DB_DRIVER=sqlite                               # whole suite on SQLite, no MySQL needed
make e2e E2E_DB_DRIVER=sqlite SPECS="e2e/tests/aeo-providers.spec.ts" # AEO provider regression suite
```

`scripts/e2e/run.sh` does the rest, and CI runs the same script on every pull request, every
merge-queue group and every push to `main`, once per database engine (jobs "E2E (Playwright on
MySQL 8)", "E2E (Playwright on MariaDB 10.11)" and "E2E (Playwright on SQLite)"):

1. Drops and recreates the **`gocrm_e2e`** database (`E2E_DB_NAME`; it refuses any name not ending
   in `_e2e`), so every run starts empty and never touches the development database.
2. Builds a disposable backend with `-tags=gophercrm_e2e` and starts it on a free port (from 18091)
   with `GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true`. This bypasses all live limiter tiers for functional
   tests so one case cannot exhaust another case's tokens. Normal binaries ignore this switch;
   `make verify-backend` verifies normal enforcement and the build-tag boundary. The existing
   `DISABLE_RATE_LIMIT=true` strict-tier setting is also retained.
   Its log goes to `test-results/e2e-backend.log`.
3. Starts its own Vite server on a free port (from 15173) through `E2E_UI_PORT`, pointed at that
   backend, and runs Playwright with **no retries**, traces kept for failures, and the `line` + `html`
   reporters.
4. Always stops the backend and exits with Playwright's result.

The run pins the backend's environment: the chosen driver, the default API prefix, no SMTP (the
log-only mailer), no reCAPTCHA, no answer-engine keys, provider model/endpoint overrides or AEO
schedule, whatever the root `.env` says. On macOS it keeps the machine awake (`caffeinate -i`),
since an idle sleep mid-run shows up as a cascade of page-load timeouts.

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

### AEO provider regression coverage

`aeo-providers.spec.ts` runs in the standard suite on every database engine. It saves and clears
all five provider connections through the UI and checks persistence after reload. Anthropic and
Gemini generation and single-prompt tracking use the real Go SDKs against a per-test HTTPS server
bound to the run's selected `127.0.0.1` port, with dummy stored keys. Changing a model and endpoint
between generation and tracking must take effect immediately. Gemini's array-shaped 503 response
also exercises SDK retries, the generation message, and the stored error displayed in prompt
details. Staff/customer checks cover the UI restrictions and server-side configuration permissions.

Each `make e2e` invocation generates a short-lived certificate and private key in its temporary
directory. The disposable `gophercrm_e2e` build accepts that certificate only with the explicit
`GOPHERCRM_E2E_AEO_TLS=1` opt-in. The runner sets `GODEBUG=x509usefallbackroots=1` so Go uses this
temporary trust pool on macOS as well as Linux; certificate and hostname verification remain
enabled. Ordinary production builds ignore these fixture variables. No certificate is installed
in the system trust store, and cleanup removes the generated TLS files. Regression tests exercise
both build modes, the opt-in, verified TLS, and rejection of a wrong hostname.

`E2E_AEO_PORT` selects that fixture port (by default, the first free port from 18191).
Tests run sequentially and reuse it. The runner replaces inherited AEO egress settings with
exact per-engine HTTPS origins at that port; it grants no wildcard or custom-engine exception.
These environment settings prepare the harness for #98's operator-controlled destination policy.

Failure coverage requires HTTP status, SDK retries, and the UI rendering the recorded error.
It does not require retaining a provider's raw response message; #98 separately hardens that
failure payload and the allowed credential destinations.

The runner enables these global-setting mutations with `E2E_AEO_STUBS=1` only after creating its
fresh database and disabling live keys and scheduling. Direct Playwright runs skip this spec.
The fixture also refuses an initially configured provider or stored sensitive key; it restores
changed configurations and any existing brand profile, deletes its prompts/users, and closes its
stub after cleanup. A new profile and completed runs remain in the disposable test database as
evidence because the API has no profile/run deletion endpoint. No live provider traffic is used.

## Running by hand (debugging)

The manual setup below uses the development database, the backend on 8080 and the dev server on
5173. Export `VITE_API_BASE_URL` in Terminal 2 so both Vite and Playwright's direct API calls use
that backend. Vite also reads `gocrm-ui/.env` (`.env.example` ships 8080), but a shell export takes
precedence. Playwright does not read that file: without the export, its API helpers fall back to
`http://localhost:8090/api/v1` (`helpers/env.ts`). If you choose another backend port, change both
`SERVER_PORT` and the exported URL.

```bash
# Terminal 1 — backend (port must match VITE_API_BASE_URL)
cd /path/to/gophercrm
DISABLE_RATE_LIMIT=true SERVER_PORT=8080 go run ./cmd

# Terminal 2 — from gocrm-ui/
cd /path/to/gophercrm/gocrm-ui
export VITE_API_BASE_URL=http://localhost:8080/api/v1
npm run test:e2e                    # all specs
npm run test:e2e:headed             # visible browser
npm run test:e2e:debug              # Playwright inspector
npm run test:e2e:ui                 # interactive UI mode
npm run test:e2e:report             # open the last HTML report
npm run test:e2e:admin              # admin CRUD specs, playwright.config.slow.ts
npm run test:e2e:admin:cleanup      # pattern-based delete on the development database, see below

# A single spec
npx playwright test e2e/tests/login.spec.ts
```

The Vite dev server is started automatically by the config's `webServer` block
(`reuseExistingServer: true`). If a dev server is already running, it must use the same API URL;
restart it with the exported value if necessary. The backend is **not** started for you.

`DISABLE_RATE_LIMIT=true` is worth setting: it bypasses the strict 10 req/min limiter on
`/auth/register` and `/auth/login`, which the login and registration specs would otherwise trip. It
does not disable rate limiting elsewhere — authenticated routes keep their moderate tier — so it is
not a way to run the suite faster, only a way to stop the login limiter from producing false failures. For
complete functional runs use `make e2e`, whose dedicated tagged binary isolates all live tiers;
this manual normal-build path intentionally retains ordinary throttling behavior.

`npm run test:e2e:admin:cleanup` runs `e2e/scripts/cleanup-admin-test-data.sh`, kept from the time
the specs ran against the development database only. It connects with `mysql` as `DB_USER`
(default `root`) to `DB_NAME` (default `gocrm`) and deletes rows by name and title patterns, among
them every lead and customer with an `@example.com` address, users whose address matches
`%admin_%@example.com` or `%test_%@example.com`, and every ticket or task whose title contains
`Test`, then resets the auto-increment counters. `make e2e` never needs it, because
it drops `gocrm_e2e` first. Run it only against a database you are willing to clean that way.

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
| `aeo-providers.spec.ts` | 5 | AEO — five-provider connection persistence/clearing, real Anthropic/Gemini SDK generation and tracking, hot model/endpoint edits, Gemini 503 retries/errors, admin/staff/customer permissions |
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
