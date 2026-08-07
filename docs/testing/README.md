# GopherCRM Test-Case Catalog

A feature-structured catalog of Playwright E2E test cases covering every user-facing feature,
written 2026-08-07 against the code as it stands. Every case documents **observed behaviour** —
what the application does today, verified against the handlers, routes and components — never
intended-but-absent behaviour. Where current behaviour is defective or surprising, the case still
states it as the expected outcome and flags it with a **Known issue** line, mirroring the OpenAPI
convention in this repo: document what the code does, warts included.

Each document was fact-checked against the source after writing; the corrections that pass produced
are folded in. "Automated" means the cited spec file contains an exactly-matching test title, not
that the test was executed as part of writing this catalog.

## Documents

| Document | Area | Cases | P0 | P1 | P2 | Automated | Planned | Blocked |
|---|---|---|---|---|---|---|---|---|
| [01-authentication.md](01-authentication.md) | Login, registration, sessions, lockout, password reset | 44 | 11 | 18 | 15 | 22 | 19 | 3 |
| [02-dashboard.md](02-dashboard.md) | Stats, quick actions, charts, activity widgets | 37 | 10 | 15 | 12 | 1 | 30 | 6 |
| [03-leads.md](03-leads.md) | Lead CRUD, search/sort/filter, conversion, bulk status | 55 | 14 | 25 | 16 | 16 | 30 | 9 |
| [04-customers.md](04-customers.md) | Customer CRUD, export, assignment, tickets-by-customer | 41 | 14 | 15 | 12 | 10 | 21 | 10 |
| [05-tickets.md](05-tickets.md) | Ticket CRUD, role matrix, comments, my-tickets | 48 | 21 | 19 | 8 | 6 | 40 | 2 |
| [06-tasks.md](06-tasks.md) | Task CRUD, assignment rules, due dates, my-tasks | 46 | 11 | 19 | 16 | 8 | 28 | 10 |
| [07-users.md](07-users.md) | User admin, roles, activate/deactivate, erasure, profile | 39 | 14 | 18 | 7 | 13 | 25 | 1 |
| [08-settings.md](08-settings.md) | API keys, configuration settings | 34 | 9 | 16 | 9 | 2 | 17 | 15 |
| [09-cross-cutting.md](09-cross-cutting.md) | RBAC matrix, errors, pagination, rate limits, erasure UX | 50 | 13 | 24 | 13 | 13 | 36 | 1 |
| **Total** | | **394** | **117** | **169** | **108** | **91** | **246** | **57** |

## Case format

Every case carries a stable ID (`TC-<AREA>-NNN`, unique within its document) and these fields:

- **Priority** — `P0` security / RBAC / data integrity / critical path; `P1` core functionality;
  `P2` polish and edge cases.
- **Type** — `functional`, `validation`, `rbac`, `negative` or `regression`.
- **Preconditions** — the exact state required. Entity data always comes from the faker generators
  in `gocrm-ui/e2e/fixtures/admin-user.ts`; the seeded admin (`test-admin@gocrm.test`, provisioned
  by `e2e/global-setup.ts` through `cmd/create-admin`) is the only fixed account.
- **Steps** — concrete UI actions with real field names and selectors where they matter.
- **Expected** — the observed behaviour, with HTTP statuses for API-visible effects.
- **Known issue** — present only when the behaviour is defective or surprising, citing
  `docs/FEATURES.md` / `docs/ROADMAP.md` or the code location.
- **Automation** — `automated` (exact spec file + test title), `planned` (target spec file), or
  `blocked` (with the concrete blocker).

## Environment

```bash
# Backend (port must match gocrm-ui/.env VITE_API_BASE_URL)
DISABLE_RATE_LIMIT=true SERVER_PORT=8090 go run cmd/main.go

# E2E, from gocrm-ui/ (Vite is auto-started by the Playwright webServer block)
npm run test:e2e          # existing suite
npm run screenshots       # documentation screenshot suite (docs/SCREENSHOTS.md)
```

Tests share one MySQL database and must run serially (`workers: 1`). See
[docs/SCREENSHOTS.md](../SCREENSHOTS.md) for the visual reference captures the cases cite.

## Lessons learned

Hard-won constraints from building and running the suites in this repo. New specs that ignore them
fail in ways that look like application bugs.

1. **Module format.** `gocrm-ui/package.json` declares `"type": "module"`, so on current Node
   Playwright loads TS config and setup files as ES modules: `__dirname` does not exist. Derive
   paths from `import.meta.url` (see `e2e/global-setup.ts` and `e2e/screenshots/helpers/capture.ts`
   — both were broken by this before being fixed).
2. **Assert app outcomes, not storage internals.** The JWT lands in `sessionStorage` unless
   "Remember me" is ticked; only then does it go to `localStorage`. Helpers that assert
   `localStorage.getItem('gophercrm_token')` after an unticked login pass or fail on an
   implementation detail — and two existing ones make exactly that assumption
   (see 01-authentication.md). Assert the redirect and the rendered page instead, or tick the box.
3. **One database, serial execution.** All specs share the dev MySQL database. `workers: 1` is not
   an optimization choice; parallel workers corrupt each other's list assertions.
4. **Generated emails only.** Unique constraints outlive test runs (soft-deleted rows included).
   Any hardcoded entity email becomes an intermittent 409. The faker generators embed a timestamp
   and random suffix; use them for every created record.
5. **Deletes are irreversible.** Deleting a user, customer or lead is GDPR erasure, not a soft
   delete. A test may only delete records it created itself, and dialog-capture tests must cancel,
   never confirm.
6. **`DISABLE_RATE_LIMIT` lifts only the strict `/auth` tier.** The moderate tier — 120 req/min,
   burst 30, keyed per IP — still applies to every authenticated request. A fast suite hammering
   list endpoints from localhost can trip it; it also makes the 5-attempt account lockout
   unobservable unless the env var is set (the limiter 429s the sixth attempt first).
7. **Filters are client-side.** Lead status, ticket status/priority, task filters and the user role
   filter narrow only the currently loaded page. A case asserting server-side filtered result sets
   asserts behaviour that does not exist.
8. **Elevated roles come from outside the app.** Public registration always creates a `customer`.
   The admin comes from the `create-admin` CLI; sales and support accounts require an
   admin-authenticated `POST /users`, and no e2e helper does that yet — building that role-login
   helper is the single change that unblocks the most planned RBAC cases.
9. **The code outranks the docs.** Cataloguing found `docs/FEATURES.md` rows 1.3/1.4 stale (session
   refresh and logout exist since the 2026-08 build-out) and a ROADMAP defect already fixed
   (`/dashboard/stats` is guarded now). Every claim in these documents was traced to code, and new
   cases should be grounded the same way.

## Defects surfaced while cataloguing

Writing expected-vs-observed cases flushed out real defects; the details live in the per-area
documents as **Known issue** lines. The most consequential:

- Conversion rate can exceed 100 % by construction — `total_customers / total_leads` over two
  unrelated populations, with converted leads counted on both sides (02-dashboard.md).
- The dashboard stat-card trend arrows are hardcoded literals (`12`, `8`, `-5`), not data
  (02-dashboard.md).
- All four dashboard stat counts are unscoped: a sales user sees system-wide totals while the list
  pages narrow to their own records (02-dashboard.md).
- Registration stores the token in `localStorage` unconditionally and receives no refresh token, so
  a newly registered session cannot refresh and is hard-redirected to `/login` on its first 401
  (01-authentication.md).
- The `/leads` and `/tickets` list routes have no route-level role guard in the SPA — the backend
  403s, but the SPA renders a broken page rather than redirecting to `/unauthorized`
  (03-leads.md, 05-tickets.md).
- Ticket sentinel errors are surfaced doubled ("cannot reopen closed ticket: cannot reopen closed
  ticket") because the handler returns `err.Error()` of a wrapped sentinel with identical text
  (05-tickets.md).
- The users list reports `meta.total` as the length of the returned page, so pagination totals are
  wrong beyond page one (07-users.md).
- The Profile and API Keys settings pages are heading-only stubs while their backends are complete
  and integration-tested (07-users.md, 08-settings.md).

## Implementation order

1. The sales/support role-login helper (`e2e/helpers/`), then the blocked RBAC cases across
   tickets, tasks, leads and the dashboard.
2. P0 planned cases: the ticket role matrix (05), erasure-visible effects (03, 04, 07), and the
   RBAC route matrix (09).
3. New spec files the planned cases target: `dashboard.spec.ts`, `admin-apikeys.spec.ts`,
   `admin-configurations.spec.ts`, plus extensions to the existing admin suites.
4. P1/P2 planned cases per document, in ID order.
