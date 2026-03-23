# E2E Test Suite — GopherCRM

69 end-to-end tests using Playwright against the running frontend (port 5173) and backend (port 8090).

## Running Tests

```bash
# Prerequisites: backend and frontend must be running
cd /path/to/gophercrm && go run cmd/main.go          # Terminal 1
cd /path/to/gophercrm/gocrm-ui && npm run dev         # Terminal 2

# Run all tests
npx playwright test

# Run a specific file
npx playwright test e2e/tests/login.spec.ts

# Run headed (see browser)
npx playwright test --headed

# Debug mode
npx playwright test --debug
```

## Test Admin Account

`test-admin@gocrm.test` / `AdminPass123!` — auto-created by `AdminAuthHelper` if missing.

## Test Coverage (69 tests)

| File | Tests | Entity | Scenarios |
|------|-------|--------|-----------|
| login.spec.ts | 11 | Auth | Render, success, wrong password, missing user, empty, invalid email, visibility toggle, register link, Enter key, protected routes, unauth redirect |
| registration.spec.ts | 15 | Auth | Success+redirect, validation (empty/email/password complexity/mismatch), duplicate email, visibility, Enter key, loading state, preservation, nav, network error |
| admin-customers.spec.ts | 10 | Customers | List, create, edit, view, delete, search, validation, cancel, minimal data, duplicate email |
| admin-leads.spec.ts | 8 | Leads | List, create, edit, view, delete, status filter, search, minimal data |
| admin-users.spec.ts | 7 | Users | List, create, edit, view, delete, search, role filter |
| admin-tasks.spec.ts | 8 | Tasks | List, create, edit, view, delete, status filter, priority filter, minimal data |
| admin-tickets.spec.ts | 5 | Tickets | List, create, view, status filter, priority filter |
| admin-entity-suite.spec.ts | 5 | Cross-entity | Navigation, CRM workflow, data isolation, quick creation, sidebar |

## Architecture

```
e2e/
├── fixtures/           # Test data generators (unique emails with timestamps)
├── helpers/            # Admin login helper (auto-registers if needed)
├── pages/              # Page Object Models (selector layer)
└── tests/              # Test specs (behavior assertions)
```

**Conventions**: `input[name="..."]` selectors, `button[type="submit"]` for forms, `[data-testid="EditIcon"]` for row actions, `[role="dialog"]` for confirmations, timestamp emails for isolation.
