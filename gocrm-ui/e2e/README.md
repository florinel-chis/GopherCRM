# E2E Test Suite — GopherCRM

111 end-to-end tests across 10 spec files, run with Playwright against the Vite frontend
(`http://localhost:5173`) and a real backend. The frontend reads its API base URL from
`VITE_API_BASE_URL` in `gocrm-ui/.env`, which points at `http://localhost:8090/api/v1` locally — start
the backend on whichever port that file names.

Only the `chromium` project is configured. Tests run serially (`fullyParallel: false`, `workers: 1`)
because they share one database.

## Running Tests

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

This account cannot be created through the UI or the API. `POST /auth/register` is public and always
creates a `customer`, ignoring any role in the request body, so an admin has to come from the CLI or
from an existing admin calling `POST /users`. Do not "fix" a failing admin spec by trying to register
an admin through the app.

## Coverage

| Spec | Tests | Area |
|------|-------|------|
| `login.spec.ts` | 11 | Auth — render, success, wrong password, unknown user, empty and invalid input, password visibility, register link, Enter key, protected routes, unauthenticated redirect |
| `registration.spec.ts` | 15 | Auth — success and redirect, validation (empty, email format, password complexity, mismatch), duplicate email, visibility toggle, Enter key, loading state, field preservation, navigation, network error |
| `admin-tickets.spec.ts` | 14 | Tickets — list, create, view, edit, delete, status and priority filters |
| `admin-tasks.spec.ts` | 13 | Tasks — list, create, edit, view, delete, status and priority filters, minimal data |
| `admin-users.spec.ts` | 12 | Users — list, create, edit, view, delete, search, role filter |
| `admin-leads.spec.ts` | 11 | Leads — list, create, edit, view, delete, status filter, search, minimal data |
| `admin-customers.spec.ts` | 10 | Customers — list, create, edit, view, delete, search, validation, cancel, minimal data, duplicate email |
| `leads-sorting-search.spec.ts` | 8 | Leads — column sorting and search behaviour |
| `admin-entity-suite.spec.ts` | 6 | Cross-entity — navigation, CRM workflow, data isolation, quick creation, sidebar |
| `labels.spec.ts` | 11 | Task labels — create, duplicate name, attach to a task, inline creation, chips in list and detail, chip and dropdown filtering, rename/recolour, delete and detach |

Counts are per `test(...)` block and will drift; `npx playwright test --list` is authoritative.

## Layout

```
e2e/
├── global-setup.ts     # Seeds the admin account via cmd/create-admin
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
  three entity forms each and run for 60–100 s, which exceeds the 60 s per-test budget in
  `playwright.config.slow.ts`. They pass with `--timeout=180000`. Verified 2026-08-08; unrelated to
  the entity being created — the timeout lands on the customers step.
- Never hardcode an email in a spec that creates records. Whether a fixed address is free depends on
  what earlier runs left behind, so the create step turns into an intermittent 409. The only
  hardcoded account is the seeded admin, which global setup owns.
