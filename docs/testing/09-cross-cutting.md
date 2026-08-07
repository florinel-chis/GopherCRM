# Cross-Cutting Concerns — Test Cases

Playwright E2E test cases for the concerns that span every page: the route/role matrix, session
expiry and the 401 interceptor, the error-handling UX (404, access denied, snackbars, the
`{success,data,error}` envelope), navigation, the pagination/sort/filter conventions shared by all
list pages, rate limiting as both a constraint and a behaviour, the UI-visible half of GDPR erasure,
and the end-to-end CRM workflow. Every **Expected** section records what the application does
**today**, warts included; where the behaviour is defective or surprising a **Known issue** line
names it. Cases marked *automated* were verified by opening the spec file, not by trusting the
coverage matrix.

**Sources**

- `docs/FEATURES.md` sections 11 (rows 11.1–11.8) and 12 (rows 12.1–12.12), plus rows 2.2, 3.8, 5.5,
  6.5, 7.5, 7.8 for the shared list conventions; Gap Summary G22, G34
- `docs/ROADMAP.md` — "Follow-ups from the backend build-out" (no access-token blocklist,
  concurrent-refresh stampede, sales ticket navigation)
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`, `gocrm-ui/src/components/Breadcrumbs.tsx`,
  `gocrm-ui/src/components/DataTable.tsx`, `gocrm-ui/src/components/ErrorBoundary.tsx`,
  `gocrm-ui/src/pages/NotFound.tsx`, `gocrm-ui/src/pages/Unauthorized.tsx`,
  `gocrm-ui/src/pages/Dashboard.tsx`
- `gocrm-ui/src/api/client.ts`, `gocrm-ui/src/api/endpoints/*.ts`,
  `gocrm-ui/src/contexts/AuthContext.tsx`, and the five list pages
  (`pages/{leads,customers,tickets,tasks,users}/*List.tsx`)
- `internal/handler/routes.go`, `cmd/main.go`, `internal/middleware/auth.go`,
  `internal/middleware/rate_limit.go`, `internal/utils/response.go`, `internal/utils/sort.go`,
  `internal/handler/{lead,customer,ticket,task,user}_handler.go`
- `internal/repository/erasure.go`, `internal/repository/erasure_cascade.go`,
  `internal/repository/{user,customer}_repository.go`, `internal/models/{ticket,task,customer}.go`
- `gocrm-ui/e2e/tests/*.spec.ts`, `gocrm-ui/e2e/screenshots/02-dashboard.spec.ts`,
  `gocrm-ui/e2e/helpers/admin-auth.ts`, `gocrm-ui/e2e/global-setup.ts`,
  `gocrm-ui/e2e/fixtures/admin-user.ts`
- Visual reference: `docs/screenshots/misc/01-unauthorized.png`, `misc/02-not-found.png`,
  `docs/screenshots/dashboard/01-overview.png`

**Constraints**

- Only the `admin` account is seeded, by `gocrm-ui/e2e/global-setup.ts` shelling out to
  `cmd/create-admin`. Every `sales` / `support` account in this file has to be created inside the
  test through `/users/new` as admin, and a `customer` account through `/register` (that endpoint
  always forces the `customer` role). There is no role-login helper — `AdminAuthHelper` only logs in
  the seeded admin — so all role cases below carry that cost.
- All records must come from the faker generators in `gocrm-ui/e2e/fixtures/admin-user.ts`
  (`generateUserData`, `generateLeadData`, `generateCustomerData`, `generateTicketData`,
  `generateTaskData`). Never hardcode an email.
- **Pacing.** `/auth/*` sits behind `RateLimitStrict()` — 10 req/min, burst 5, per client IP
  (`internal/middleware/rate_limit.go:124`, applied at `cmd/main.go:183`). A role case that logs a
  second and third user in burns one token each, and the whole suite shares one IP. Space auth
  requests ~6 s apart or run the backend with `DISABLE_RATE_LIMIT=true`, which bypasses **only** the
  strict tier. Every authenticated request additionally consumes the moderate tier — 120 req/min,
  burst 30 (`rate_limit.go:136`, `cmd/main.go:197`) — which is *not* bypassable; a spec that loops
  over 30+ page loads without pause will start seeing 429s.
- `playwright.config.ts` runs `workers: 1`, `fullyParallel: false`. Keep it that way: parallel
  workers share the rate-limit bucket and the same database rows.
- Deleting a **user, customer or lead** is irreversible GDPR erasure
  (`internal/repository/erasure.go`). Only ever delete records the test itself created.

---

## Route and role matrix (FEATURES 11.1)

The matrix below is derived from `gocrm-ui/src/routes/index.tsx` (SPA gate),
`gocrm-ui/src/layouts/MainLayout.tsx` (`navItems[].roles`) and `internal/handler/routes.go` +
the in-handler role checks (API gate). It is the specification the cases in this section assert.

| SPA route | SPA gate | Nav item shown to | API guard for the data the page loads |
|---|---|---|---|
| `/login`, `/register`, `/unauthorized` | none (public) | — | — |
| `/` (Dashboard) | authenticated | all roles | `GET /dashboard/*` → `RequireRole(admin, sales, support)` (`routes.go:105`); the page skips the calls entirely for `customer` (`Dashboard.tsx` `canViewStats`) |
| `/leads`, `/leads/new`, `/leads/:id`, `/leads/:id/edit` | authenticated only — **no role gate** | admin, sales | whole `/leads` group is `RequireRole(admin, sales)` (`routes.go:24`) → 403 for support and customer |
| `/customers`, `/customers/new`, `/customers/:id`, `/customers/:id/edit` | authenticated only | admin, sales, support | list/get: handler allows admin, sales, support (`customer_handler.go:157`, `:239`) → 403 for customer; create: `RequireRole(admin, sales)`; delete + export: `RequireRole(admin)`; assign: `RequireRole(admin, sales)` |
| `/tickets`, `/tickets/:id` | authenticated only | admin, support | list/get: 403 only for customer (`ticket_handler.go:135`, `:354`) — **sales can read** |
| `/tickets/new`, `/tickets/:id/edit` | `requiredRole=['admin','support']` (`routes/index.tsx:81`) | — | create: admin + support (`ticket_handler.go:69`); update: 403 for customer and sales (`:410`, `:416`), support only for its own assignment (`:447`); delete: admin only (`:511`) |
| `/tasks`, `/tasks/new`, `/tasks/:id`, `/tasks/:id/edit` | authenticated only | all roles | list: admin sees all, everyone else only their own (`task_handler.go:230`); create: admin, support, sales (`:69`) → 403 for customer; delete: admin only (`:499`) |
| `/users`, `/users/new`, `/users/:id`, `/users/:id/edit`, `/settings/configuration` | `requiredRole='admin'` (`routes/index.tsx:118`) | Users: admin; Configuration: admin | `POST/GET /users` and `DELETE /users/:id` are `RequireRole(admin)`; `GET /users/:id` is self-or-admin (`user_handler.go:209`); `/configurations` is admin except `/configurations/ui` |
| `/settings/profile`, `/settings/api-keys` | authenticated only | all roles | `/users/me` and the whole `/api-keys` group carry no role guard |
| anything else (`*`) | **outside** `ProtectedRoute` | — | — |

The important asymmetry: apart from ticket writes, users and configuration, the SPA has **no**
route-level role gate. Leads, customers, tickets (read) and tasks are reachable by deep link for
every authenticated role; the refusal happens at the API and is not surfaced.

### TC-XCUT-001 — Redirect an unauthenticated visitor from a protected route to /login
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged out; both storages cleared of `gophercrm_token` / `gophercrm_refresh_token`.
- **Steps:**
  1. Clear the tokens from `localStorage` and `sessionStorage`.
  2. Navigate to `/users`.
- **Expected:** `ProtectedRoute` sees `isAuthenticated === false` and renders
  `<Navigate to="/login" state={{from: location}} replace />`; the URL becomes `/login` and the login
  form renders. No request is made to `/api/v1/users`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "unauthenticated user is redirected
  to login"

### TC-XCUT-002 — Return the visitor to the deep link they were denied
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged out. Seeded admin credentials.
- **Steps:**
  1. Navigate to `/customers/new`.
  2. On the login page, sign in as the seeded admin.
- **Expected:** `Login.tsx` reads `location.state.from.pathname` and navigates there with
  `{replace: true}`, so the URL after login is `/customers/new`, not `/`. The back button does not
  return to `/login`.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-XCUT-003 — Show the 404 page, not the login redirect, for an unknown path while logged out
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Logged out, tokens cleared.
- **Steps:** Navigate to `/no-such-page`.
- **Expected:** The URL stays `/no-such-page` and the 404 page renders (`404`, "Page Not Found",
  "Go to Dashboard"). The catch-all `*` route is declared **outside** the `ProtectedRoute` branch
  (`routes/index.tsx:156`), so an unknown path is never authentication-gated.
- **Known issue:** The 404 page leaks the existence of the SPA shell to anonymous visitors and its
  "Go to Dashboard" button navigates to `/`, which then bounces to `/login`. Harmless, but the
  two-hop journey is easy to mistake for a bug.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new). The logged-in variant is
  automated — `gocrm-ui/e2e/screenshots/02-dashboard.spec.ts` "not found screen".

### TC-XCUT-004 — Admin reaches every route in the matrix
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as the seeded admin.
- **Steps:** Visit, in order, `/`, `/leads`, `/customers`, `/tickets`, `/tickets/new`, `/tasks`,
  `/users`, `/users/new`, `/settings/profile`, `/settings/api-keys`, `/settings/configuration`.
- **Expected:** Every route renders its own page heading and no navigation ends on `/unauthorized`
  or `/login`. The list endpoints all answer **200**.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts` "admin can
  navigate between all entity pages" covers `/leads`, `/customers`, `/tickets`, `/tasks`, `/users`;
  the settings and `new` routes are not covered. Extend
  `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts`.

### TC-XCUT-005 — Sales is bounced from the admin-only routes to /unauthorized
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Admin creates a `sales` user via `/users/new` from `generateUserData()`; log in
  as that user.
- **Steps:** Navigate to `/users`, then `/users/new`, then `/settings/configuration`.
- **Expected:** Each navigation lands on `/unauthorized` (`replace`, so the back button does not
  re-enter) and renders the Access Denied screen. The lazily imported page component is never
  loaded and no `GET /api/v1/users` request is issued. Issuing that request directly from an
  `APIRequestContext` with the sales token returns **403** with
  `{"success":false,"error":{"code":"FORBIDDEN","message":"Insufficient permissions"}}`
  (`internal/middleware/auth.go:64`).
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new; needs a role-login helper)

### TC-XCUT-006 — Sales can read tickets by deep link although the nav item is hidden
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as a `sales` user; at least one ticket exists (create it as admin
  first from `generateTicketData()`).
- **Steps:**
  1. Confirm the sidebar has no **Tickets** entry.
  2. Navigate directly to `/tickets`.
  3. Open the first ticket.
- **Expected:** Both pages render and `GET /api/v1/tickets` and `GET /api/v1/tickets/:id` return
  **200** — the ticket handler rejects only the `customer` role (`ticket_handler.go:135`, `:354`).
  The **Create Ticket** button is absent and the row Edit/Delete icons are absent (`TicketList`
  `canCreate`/`canEdit`/`canDelete`).
- **Known issue:** Nav gating and API gating disagree: `MainLayout.navItems` restricts the Tickets
  entry to `['admin','support']` while the API lets sales read. Recorded in `docs/ROADMAP.md`
  ("Sales ticket navigation") as an open product call.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-007 — Sales is bounced from the ticket write routes
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged in as a `sales` user; a ticket id created earlier by the admin.
- **Steps:** Navigate to `/tickets/new`, then to `/tickets/<id>/edit`.
- **Expected:** Both land on `/unauthorized` — the pathless layout route at `routes/index.tsx:80-95`
  wraps both children in `requiredRole={['admin','support']}`. A direct
  `PUT /api/v1/tickets/<id>` with the sales token returns **403** (`ticket_handler.go:416`).
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-008 — Support opens /leads and gets an empty table instead of a refusal
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Admin creates a `support` user via `/users/new`; several leads exist. Log in as
  the support user.
- **Steps:**
  1. Confirm the sidebar has no **Leads** entry.
  2. Navigate directly to `/leads`.
- **Expected:** The Leads page renders with its heading, search box and filters. `GET /api/v1/leads`
  returns **403** (`routes.go:24`), the table body is empty and the pagination reads `0-0 of 0`.
  **No error message, snackbar or Access Denied screen is shown.**
- **Known issue:** `LeadList` destructures only `{ data, isLoading }` from `useQuery` and never reads
  `isError`, so an authorization failure is indistinguishable from "no leads yet". The same pattern
  is in `CustomerList`, `TicketList`, `TaskList` and `UserList`. Not tracked in FEATURES; see
  TC-XCUT-026.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-009 — Support can create a ticket but not delete one
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as a `support` user.
- **Steps:**
  1. Go to `/tickets` and click **Create Ticket**.
  2. Fill the form from `generateTicketData()` and save.
  3. Return to `/tickets` and inspect the row actions for the new ticket.
- **Expected:** `/tickets/new` renders (support is in the SPA guard list) and
  `POST /api/v1/tickets` returns **201** (`ticket_handler.go:69`). Back on the list, the Delete icon
  is not rendered for support (`TicketList` `canDelete` is admin-only) and a direct
  `DELETE /api/v1/tickets/:id` with the support token returns **403** (`ticket_handler.go:511`).
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-010 — Customer sees a three-item sidebar
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Register a `customer` through `/register` with `generateTestUser()`; stay logged
  in as that account.
- **Steps:** Inspect the permanent drawer.
- **Expected:** Exactly **Dashboard**, **Tasks** and **Settings** (expanding to Profile and API Keys)
  are present. Leads, Customers, Tickets, Users and Settings → Configuration are absent, because
  `canViewNavItem` filters on `navItems[].roles` and the `customer` role appears in none of them.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-011 — Customer deep-links into /customers and /tickets and sees empty pages
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged in as the `customer` account from TC-XCUT-010; customers and tickets
  exist.
- **Steps:** Navigate to `/customers`, then `/tickets`, then `/tickets/new`.
- **Expected:** `/customers` and `/tickets` both render their page shells with an empty table;
  `GET /api/v1/customers` returns **403** (`customer_handler.go:157`) and `GET /api/v1/tickets`
  returns **403** (`ticket_handler.go:135`). `/tickets/new` is the only one that is properly
  refused: it redirects to `/unauthorized`.
- **Known issue:** Read routes for other people's business data are gated only by the API; the SPA
  shows a page that looks like an empty account rather than a refusal. Same root cause as
  TC-XCUT-008.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-012 — Customer sees only their own tasks and cannot create one
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as the `customer` account; the admin has created at least one task
  assigned to somebody else.
- **Steps:**
  1. Open **Tasks** from the sidebar.
  2. Click **New Task** (or navigate to `/tasks/new`), fill the form and save.
- **Expected:** `GET /api/v1/tasks` returns **200** but only with tasks assigned to this user —
  `task_handler.go:230` gives the full list to admins and the caller's own tasks to everyone else.
  The `/tasks/new` route renders (no SPA gate), but `POST /api/v1/tasks` returns **403**
  (`task_handler.go:69`, which admits only admin, support and sales) and the form surfaces the
  failure without navigating away.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-013 — Customer dashboard renders without stat cards and issues no dashboard requests
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as the `customer` account.
- **Steps:** Open `/` and record the network requests.
- **Expected:** The "Dashboard" heading and the **Quick Actions** panel render; the five stat cards,
  the Sales Performance chart, Recent Activities and Upcoming Tasks are all absent. No request is
  made to `/api/v1/dashboard/*` — `Dashboard.tsx` sets `canViewStats` false for the customer role and
  passes `enabled: canViewStats` to every query, deliberately anticipating the
  `RequireRole(admin, sales, support)` guard at `routes.go:105`.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-014 — Profile and API Keys are reachable by every role
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** One account per role (admin seeded, sales/support created by admin, customer
  self-registered).
- **Steps:** For each role, navigate to `/settings/profile` and `/settings/api-keys`.
- **Expected:** Both render for all four roles. `GET /api/v1/users/me` and `GET /api/v1/api-keys`
  return **200** — neither route carries a `RequireRole` guard (`routes.go:14`, `:77-86`).
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-015 — The customer list shows a Delete icon to sales, which the API refuses
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged in as a `sales` user; a customer created by the test (as admin) exists.
- **Steps:**
  1. Open `/customers`.
  2. Click the row Delete icon and confirm in the dialog.
- **Expected:** The icon **is** rendered — `CustomerList` passes `onDelete` unconditionally, unlike
  `UserList` which gates it on `isAdmin`. `DELETE /api/v1/customers/:id` returns **403**
  (`routes.go:44`), the error snackbar "Failed to delete customer" appears, the dialog stays open
  and the row is still listed after a reload.
- **Known issue:** Frontend gating and backend guard disagree here in the dangerous direction — the
  UI offers an irreversible action that the caller is not allowed to perform. Not listed in
  FEATURES; the equivalent gating exists on `UserList.tsx:314`.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

---

## Session expiry and the 401 interceptor (FEATURES 1.3/1.4, section 10b)

### TC-XCUT-016 — An expired access token is refreshed silently and the request retried
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin with a valid refresh token in storage.
- **Steps:**
  1. Overwrite `gophercrm_token` in the active storage with an expired or malformed JWT, leaving
     `gophercrm_refresh_token` intact.
  2. Navigate to `/customers`.
- **Expected:** `GET /api/v1/customers` returns **401**, the response interceptor
  (`api/client.ts:58`) posts to `/auth/refresh` through a bare axios instance, stores the new access
  token **and** the rotated refresh token in the same storage the old ones lived in, replays the
  original request, and the customer list renders. The user is never sent to `/login`.
- **Automation:** planned — `gocrm-ui/e2e/tests/session.spec.ts` (new)

### TC-XCUT-017 — A 401 with no refresh token hard-redirects to /login
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin, then remove `gophercrm_refresh_token` from both
  storages and replace `gophercrm_token` with an expired JWT.
- **Steps:** Navigate to `/leads`.
- **Expected:** The interceptor throws "No refresh token available", `clearTokens()` empties both
  storages and `window.location.href = '/login'` performs a **full page navigation** (not a React
  Router transition), so the SPA remounts on the login page. Nothing of the previous session
  survives a reload.
- **Automation:** planned — `gocrm-ui/e2e/tests/session.spec.ts` (new)

### TC-XCUT-018 — A replayed refresh token ends the session
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin. Capture the current refresh token, force one
  successful refresh (TC-XCUT-016), then restore the **old** refresh token into storage.
- **Steps:** Expire the access token again and navigate to any list page.
- **Expected:** `POST /auth/refresh` returns **401** because rotation is strict and the presented
  token is already dead; the interceptor clears both storages and hard-redirects to `/login`.
- **Automation:** planned — `gocrm-ui/e2e/tests/session.spec.ts` (new)

### TC-XCUT-019 — A 401 from /auth/login does not trigger the refresh path
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Logged out.
- **Steps:** Submit the login form with the seeded admin email and a wrong password.
- **Expected:** The error alert "Invalid email or password" is shown on the login form, the URL stays
  `/login`, and **no** `POST /auth/refresh` request is made — the interceptor skips any URL
  containing `/auth/` (`api/client.ts:57`), which is what stops the redirect loop.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/login.spec.ts` "login fails with wrong
  password" asserts the message but not the absence of the refresh call; extend it.

### TC-XCUT-020 — Concurrent 401s queue behind a single refresh
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin with a valid refresh token; access token expired.
- **Steps:** Land on `/` (the dashboard fires four parallel `dashboard/*` queries) and observe the
  network log.
- **Expected:** Exactly one `POST /auth/refresh` is issued. The first failing request sets
  `isRefreshing`; the rest push a callback into `refreshSubscribers` and are replayed with the new
  token once it arrives.
- **Known issue:** If the refresh **fails**, the queued subscribers are never invoked or rejected —
  their promises never settle. The hard redirect to `/login` masks it in practice. Recorded in
  `docs/ROADMAP.md` as "Concurrent-refresh stampede".
- **Automation:** planned — `gocrm-ui/e2e/tests/session.spec.ts` (new)

### TC-XCUT-021 — Logout revokes the refresh token and clears both storages
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin; note the stored refresh token.
- **Steps:**
  1. Open the avatar menu and click **Logout**.
  2. After landing on `/login`, replay `POST /api/v1/auth/refresh` with the noted token from an
     `APIRequestContext`.
- **Expected:** `POST /api/v1/auth/logout` returns **200**, `AuthContext.logout` clears
  `gophercrm_token` and `gophercrm_refresh_token` from `localStorage` *and* `sessionStorage`, and the
  app navigates to `/login`. The replayed refresh returns **401**.
- **Known issue:** The access token itself stays valid until it expires — there is no blocklist
  (`docs/ROADMAP.md`, "Access-token blocklist"). A token captured before logout still authenticates.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended) — closes G11.

---

## Error-handling UX (FEATURES 11.4)

### TC-XCUT-022 — The 404 page renders for an unknown path
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin.
- **Steps:** Navigate to `/no-such-page`.
- **Expected:** Headings `404` and `Page Not Found`, the body text "The page you are looking for
  doesn't exist or has been moved." and a **Go to Dashboard** button that navigates to `/`. The
  page renders outside `MainLayout`, so there is no sidebar and no breadcrumb trail.
- **Automation:** automated — `gocrm-ui/e2e/screenshots/02-dashboard.spec.ts` "not found screen"

### TC-XCUT-023 — The Access Denied page renders and offers a way back
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin.
- **Steps:** Navigate to `/unauthorized` and click **Go to Dashboard**.
- **Expected:** A lock icon, heading "Access Denied", subheading "You don't have permission to access
  this page" and the body "Please contact your administrator if you believe this is an error." The
  button navigates to `/`. Like the 404 page it renders without the sidebar.
- **Automation:** automated (partial) — `gocrm-ui/e2e/screenshots/02-dashboard.spec.ts` "access
  denied screen" asserts the heading and subheading; the button is not exercised.

### TC-XCUT-024 — A blocked request surfaces an error on the form instead of hanging
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged out, on `/register`.
- **Steps:**
  1. Route `**/auth/register` to `route.abort()`.
  2. Fill the form from `generateTestUser()` and submit.
- **Expected:** The submit button leaves its loading state and a general error alert appears above
  the form; the URL stays `/register`. The axios error has no `response`, so the envelope-unwrapping
  branch is skipped and the component falls back to its generic message.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "handles network error
  gracefully"

### TC-XCUT-025 — A failed mutation shows the error snackbar and leaves the row untouched
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin, on `/leads`, with a lead created by the test.
- **Steps:**
  1. Route `**/api/v1/leads/*` DELETE to `route.fulfill({status: 500})`.
  2. Click the row Delete icon and confirm.
- **Expected:** The snackbar "Failed to delete lead" appears (`LeadList` `deleteMutation.onError`),
  the confirm dialog stays open, the row is still present after closing it, and the leads query is
  **not** invalidated. The equivalent strings are "Failed to delete customer", "Failed to delete
  user", "Failed to delete ticket", "Failed to delete task".
- **Automation:** planned — `gocrm-ui/e2e/tests/error-ux.spec.ts` (new)

### TC-XCUT-026 — A failing list query is swallowed into an empty table
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin, with at least one customer.
- **Steps:**
  1. Route `**/api/v1/customers?*` to `route.fulfill({status: 500, body: '{"success":false,"error":{"code":"INTERNAL_ERROR","message":"An unexpected error occurred"}}'})`.
  2. Navigate to `/customers`.
- **Expected:** The page renders its heading, toolbar and an **empty** table with `0-0 of 0`
  pagination. No snackbar, no alert, no retry affordance — `QueryClient` is configured with
  `retry: false` (`App.tsx`) and none of the list pages read `isError`. This is the same failure mode
  as the 403 in TC-XCUT-008.
- **Known issue:** Server errors and authorization failures are visually identical to an empty
  dataset on every list page. FEATURES 11.4 records error handling as **covered**, which is true of
  the API envelope but not of the list-page UX.
- **Automation:** planned — `gocrm-ui/e2e/tests/error-ux.spec.ts` (new)

### TC-XCUT-027 — The client unwraps the success envelope and the error envelope
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Logged in as the seeded admin.
- **Steps:**
  1. Capture the raw `GET /api/v1/customers` response body while the customer list loads.
  2. Trigger a duplicate-email conflict by creating a customer with the email of an existing one.
- **Expected:** The wire body is `{"success":true,"data":{...},"meta":{...}}`, while the page code
  receives `data` directly — the response interceptor replaces `response.data` with
  `response.data.data` whenever `success` is true and `data` is defined (`api/client.ts:47`). On the
  conflict, the wire body is `{"success":false,"error":{"code":"CONFLICT","message":...}}` and the
  interceptor replaces `error.response.data` with the inner `error` object, which is what the form
  renders.
- **Known issue:** The unwrap is conditional on `response.data.data !== undefined`. An endpoint that
  answers `{"success":true}` with no `data` leaves the raw envelope in place, so callers see
  `{success:true}` rather than `undefined`.
- **Automation:** planned — `gocrm-ui/e2e/tests/error-ux.spec.ts` (new)

### TC-XCUT-028 — A render-time exception is caught by the ErrorBoundary
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin.
- **Steps:** Force a render error inside a routed page.
- **Expected:** `ErrorBoundary` (wrapping the whole app in `App.tsx`) renders its fallback instead of
  a blank page; the stack is shown only when `import.meta.env.DEV`.
- **Automation:** blocked — there is no route or query parameter that makes a page throw during
  render, and Playwright cannot inject a component fault from outside. Needs a dev-only crash route
  before this can be automated. FEATURES 11.4 notes the missing `ErrorBoundary` test.

---

## Navigation (FEATURES 2.2, 11.1)

### TC-XCUT-029 — Sidebar entries match the role
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** One account per role.
- **Steps:** For each role, open `/` and list the drawer entries, expanding **Settings**.
- **Expected:**
  - admin — Dashboard, Leads, Customers, Tickets, Tasks, Users, Settings (Profile, API Keys, Configuration)
  - sales — Dashboard, Leads, Customers, Tasks, Settings (Profile, API Keys)
  - support — Dashboard, Customers, Tickets, Tasks, Settings (Profile, API Keys)
  - customer — Dashboard, Tasks, Settings (Profile, API Keys)
  The Settings group header is always rendered because it has no `roles` of its own; only its
  children are filtered.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

### TC-XCUT-030 — Breadcrumbs reflect the route and skip id segments
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin; a lead created by the test.
- **Steps:** Visit `/`, `/leads`, `/leads/new`, `/leads/<id>`, `/leads/<id>/edit`,
  `/settings/api-keys`.
- **Expected:**
  - `/` — no breadcrumb bar at all (the component returns `null` when there are no path segments).
  - `/leads` — `Dashboard / Leads`, with Leads as plain text.
  - `/leads/new` — `Dashboard / Leads / New`, Leads is a link.
  - `/leads/<id>` — `Dashboard / Leads`; the numeric segment is skipped, so the detail page shows
    the same trail as the list.
  - `/leads/<id>/edit` — `Dashboard / Leads / Edit`.
  - `/settings/api-keys` — `Dashboard / Settings / Api-keys`.
- **Known issue:** `routeLabels` in `components/Breadcrumbs.tsx` maps the key `apikeys`, but the
  route segment is `api-keys`, so the lookup misses and the fallback capitalisation produces
  "Api-keys" instead of "API Keys". On a detail route the last crumb is the parent list, which makes
  it look like a link that is inexplicably inert.
- **Automation:** planned — `gocrm-ui/e2e/tests/navigation.spec.ts` (new)

### TC-XCUT-031 — Quick actions navigate to the create forms
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin, on `/`.
- **Steps:** Click **New Lead**, go back, **New Ticket**, back, **New Task**, back, **View
  Customers**.
- **Expected:** `/leads/new`, `/tickets/new`, `/tasks/new` and `/customers` respectively, each
  rendering its page. This closes FEATURES 2.2, which is currently **untested**.
- **Automation:** automated (visibility only) — `gocrm-ui/e2e/screenshots/02-dashboard.spec.ts`
  "dashboard overview" asserts the Quick Actions panel is visible; no test clicks the buttons.
  Planned — `gocrm-ui/e2e/tests/navigation.spec.ts` (new).

### TC-XCUT-032 — Quick actions are shown to roles that cannot use them
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as the `customer` account.
- **Steps:** On `/`, click **New Lead**, fill the form and save.
- **Expected:** All four Quick Action buttons render — the panel has no role gate at all. **New
  Lead** navigates to `/leads/new` (no SPA gate on lead routes), the form renders, and only on save
  does `POST /api/v1/leads` return **403** (`routes.go:24`). **New Ticket** is the one that is
  properly refused, redirecting to `/unauthorized`.
- **Known issue:** The dashboard offers three actions that a customer-role user cannot complete, and
  two of them fail only after the form has been filled in.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new)

---

## Pagination, sort and filter conventions (FEATURES 11.5, 11.6, 3.8, 5.5, 6.5, 7.8)

### TC-XCUT-033 — The rows-per-page control offers 5/10/25/50 and resets to page 1
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin with more than 25 leads (create them, or use an
  already populated environment).
- **Steps:**
  1. Open `/leads`; note the pagination footer.
  2. Advance to page 2.
  3. Change **Rows per page** to 25.
- **Expected:** The default is 10 rows (`LeadList` `filters.limit = 10`) and the select offers
  exactly 5, 10, 25, 50 (`DataTable` `rowsPerPageOptions`). Page 2 issues `?page=2&limit=10`; the
  handler converts it with `offset = (page-1) * limit` (`lead_handler.go:181`). Changing the page
  size issues `?page=1&limit=25` — the page index is explicitly reset — and the footer reads
  `1-25 of N`. The same behaviour holds on `/customers`, `/tickets`, `/tasks` and `/users`.
- **Automation:** planned — `gocrm-ui/e2e/tests/pagination-sort.spec.ts` (new)

### TC-XCUT-034 — The server caps limit at 100
- **Priority:** P1
- **Type:** validation
- **Preconditions:** An `APIRequestContext` holding the admin bearer token.
- **Steps:** `GET /api/v1/customers?limit=500`.
- **Expected:** **200**, at most 100 rows, and `meta` reads
  `{"page":1,"per_page":100,"total":N,"total_pages":ceil(N/100)}` — `utils.ParseOffsetLimit`
  clamps at 100 (`internal/utils/response.go:158`). Verified live against the running backend.
- **Known issue:** The cap is unreachable from the UI: the largest rows-per-page option is 50, so
  this can only be asserted through the API context.
- **Automation:** planned — `gocrm-ui/e2e/tests/pagination-sort.spec.ts` (new)

### TC-XCUT-035 — limit=0 falls back to the default page size instead of 500-ing
- **Priority:** P1
- **Type:** regression
- **Preconditions:** An `APIRequestContext` holding the admin bearer token.
- **Steps:** `GET /api/v1/tickets?limit=0`, and repeat for `/leads`, `/customers`, `/tasks`,
  `/users`.
- **Expected:** **200** on every endpoint with `meta.per_page = 20`. `ParseOffsetLimit` only accepts
  a parsed value `> 0`, so `limit=0` never reaches the pagination arithmetic. Regression guard for
  G28, where this single query parameter turned every list endpoint into a 500.
- **Automation:** planned — `gocrm-ui/e2e/tests/pagination-sort.spec.ts` (new)

### TC-XCUT-036 — An unknown sort column is ignored rather than rejected
- **Priority:** P1
- **Type:** validation
- **Preconditions:** An `APIRequestContext` holding the admin bearer token; leads exist.
- **Steps:**
  1. `GET /api/v1/leads?sort_by=created_at&sort_order=desc` and record the ordering.
  2. `GET /api/v1/leads?sort_by=password;DROP TABLE leads&sort_order=desc`.
  3. `GET /api/v1/leads?sort_by=created_at&sort_order=sideways`.
- **Expected:** All three return **200**. The unknown column is blanked (`sortBy = ""` at
  `lead_handler.go:201`) and the query falls back to the repository default ordering — there is no
  400 and no error message, so the only visible effect is that the requested order is not applied.
  An invalid `sort_order` is coerced to `asc` at the lead handler. The same silent-fallback pattern
  is in `ticket_handler.go:161`, `task_handler.go:216`, `user_handler.go:142` and
  `customer_handler.go:178`; the deeper `utils.ValidateSort` allowlist
  (`internal/utils/sort.go`) is what actually keeps the value out of the `ORDER BY`.
- **Automation:** planned — `gocrm-ui/e2e/tests/pagination-sort.spec.ts` (new)

### TC-XCUT-037 — Clicking a column header toggles the order and returns to page 1
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin on `/leads` with more than one page of leads.
- **Steps:** Advance to page 2, then click the **Created** header twice.
- **Expected:** The first click issues `sort_by=created_at&sort_order=asc&page=1`, the second
  `sort_order=desc&page=1`; `DataTable.handleRequestSort` flips the direction only when the same
  column is clicked again, and every list page resets `page` to 1 in its `handleSort`. The active
  header shows the MUI sort arrow and the visually hidden "sorted ascending"/"sorted descending"
  text.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should
  sort by Created column descending" and "should toggle sort order on double click" cover leads
  only; G21 asks for the other columns.

### TC-XCUT-038 — Status, priority and role dropdowns do not filter anything
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin; leads in more than one status, tickets in more
  than one status and priority, users in more than one role.
- **Steps:**
  1. On `/leads`, note the visible rows, then select **Status → New** and re-read the table.
  2. Repeat on `/tickets` with **Status** and **Priority**, on `/tasks` with both, and on `/users`
     with **Role**.
- **Expected:** In every case the request is re-issued with the extra query parameter
  (`?status=new`, `?role=sales`, …), the backend **ignores** it, and the rendered rows and the total
  in the pagination footer are **unchanged**. Verified live: `GET /leads?limit=5&status=new` returns
  the same `total=71` and the same first row (status `contacted`) as the unfiltered call, and
  `GET /users?limit=5&role=admin` returns non-admin users. The one dropdown that does work is the
  lead **Classification** filter, which `lead_handler.go:178` reads — and only when `search` is empty
  and the caller is an admin, because the search branch is evaluated first and sales users are routed
  to `GetByOwner` before either is considered.
- **Known issue:** FEATURES 3.8, 5.5, 6.5 and 7.8 and gap G34 describe these as *client-side filters
  over the current page*. That is too generous: no list page filters the fetched rows at all — the
  only `.filter()` in `TaskList.tsx` (line 252) buckets tasks by day for the calendar view. The
  existing E2E tests pass because they assert `expect(count).toBeGreaterThanOrEqual(0)`
  (`admin-leads.spec.ts` "admin can filter leads by status", `admin-tasks.spec.ts` "admin can filter
  tasks by status" / "by priority", `admin-tickets.spec.ts` "admin can filter tickets by status" /
  "by priority"), which cannot fail.
- **Automation:** planned — `gocrm-ui/e2e/tests/pagination-sort.spec.ts` (new). The existing filter
  tests should be tightened to assert the no-op explicitly, or the parameters implemented backend-side.

---

## Rate limiting (FEATURES 11.2, gap G22)

### TC-XCUT-039 — The strict tier rejects a burst of login attempts with 429
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Backend started **without** `DISABLE_RATE_LIMIT`. Logged out. Run this case in
  isolation — it exhausts the shared `/auth` bucket for roughly a minute.
- **Steps:** Submit the login form six times in quick succession with a generated (non-existent)
  email.
- **Expected:** The first five submissions consume the burst and answer **401**; the sixth returns
  **429** with
  `{"success":false,"error":{"code":"TOO_MANY_REQUESTS","message":"Too many requests. Please try again later.","details":{"retry_after":"60s"}}}`
  (`internal/middleware/rate_limit.go:106-111`). Tokens refill at 10/min, so a further attempt
  succeeds after ~6 s. The login form shows its generic error text, not the retry hint.
- **Known issue:** Closes G22 ("Rate limiting has no E2E or integration test"). The bucket is keyed on
  `c.ClientIP()`, which depends on `TRUSTED_PROXIES` being set correctly; behind a misconfigured
  proxy every client shares one bucket.
- **Automation:** planned — `gocrm-ui/e2e/tests/rate-limit.spec.ts` (new, must run serially and last)

### TC-XCUT-040 — The moderate tier applies to authenticated reads as well as writes
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin; an `APIRequestContext` with the bearer token.
- **Steps:** Issue 40 `GET /api/v1/customers` calls back to back from the API context.
- **Expected:** The first ~30 (the burst) return **200**; once the bucket empties the remainder
  return **429** until it refills at 2 req/s. Reads get no special treatment —
  `RateLimitModerate()` is applied to the entire protected group at `cmd/main.go:197` and
  `RateLimitGenerous()` (240/min) is defined at `rate_limit.go:142` and never used.
- **Known issue:** The inline comment at `cmd/main.go:197` still says "60 req/min" and is stale; the
  limiter is 120/min, burst 30. Gap G37 covers the dead generous tier.
- **Automation:** planned — `gocrm-ui/e2e/tests/rate-limit.spec.ts` (new)

### TC-XCUT-041 — DISABLE_RATE_LIMIT bypasses the auth tier only
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Backend started with `DISABLE_RATE_LIMIT=true`.
- **Steps:** Repeat TC-XCUT-039, then repeat TC-XCUT-040.
- **Expected:** The login burst no longer produces a 429 — `RateLimitStrict()` returns a pass-through
  handler when the variable is set (`rate_limit.go:125`). The authenticated burst **still** produces
  429s: the moderate tier has no such switch. A suite that assumes the flag removes all limiting will
  flake on long list-heavy specs.
- **Automation:** planned — `gocrm-ui/e2e/tests/rate-limit.spec.ts` (new)

---

## Erasure as seen through the UI (FEATURES section 12)

### TC-XCUT-042 — Deleting a lead removes the row and frees the email address
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin. `const lead = generateLeadData()`; create it
  through `/leads/new`.
- **Steps:**
  1. On `/leads`, click the row Delete icon for that lead and confirm.
  2. Search the list for `lead.email`.
  3. Create a new lead through `/leads/new` reusing the exact same email.
- **Expected:** `DELETE /api/v1/leads/:id` returns **200**, the snackbar "Lead deleted successfully"
  appears and the query is invalidated so the row disappears. The search returns no rows. The
  re-creation succeeds with **201** — the erasure replaced the address with a random
  `deleted-<32 hex>@anonymized.invalid` placeholder (`internal/repository/erasure.go`
  `newAnonymizedEmail`) before soft-deleting, so the unique index no longer holds the original.
- **Known issue:** Irreversible by design (FEATURES 12.1-12.3). The confirm dialog says "This action
  cannot be undone" but does not say the personal data is erased rather than archived.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can delete a
  lead" covers the row disappearing; the email-reuse half is planned for the same file.

### TC-XCUT-043 — Deleting a customer is admin-only and frees the email address
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin. `const customer = generateCustomerData()`;
  create it through `/customers/new`.
- **Steps:**
  1. Delete it from `/customers` and confirm.
  2. Create a new customer with the same email.
- **Expected:** `DELETE /api/v1/customers/:id` returns **200** for admin (`routes.go:44`), the
  snackbar "Customer deleted successfully" appears, the row is gone, and the re-creation returns
  **201** rather than the **409** a live duplicate would produce. If the customer came from a
  converted lead, the originating lead is erased too (`NewCustomerRepositoryWithLeadErasure`,
  `cmd/main.go:138`) and disappears from `/leads`.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can
  delete a customer"; email reuse and the lead cascade are planned for the same file.

### TC-XCUT-044 — Deleting a user erases them, and an admin cannot delete themselves
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged in as the seeded admin. `const user = generateUserData()`; create it
  through `/users/new`.
- **Steps:**
  1. Delete the generated user from `/users` and confirm.
  2. Search `/users` for that email, then register a fresh account with it through `/register`.
  3. Open the row menu on the admin's **own** row and inspect the Delete item; then issue
     `DELETE /api/v1/users/<own id>` from the API context.
- **Expected:** Step 1 returns **200**, shows "User deleted successfully" and removes the row.
  Step 2 finds nothing and the registration succeeds with **201**. In step 3 the menu's Delete item
  is `disabled` when `selectedUser.id === currentUser.id`, and the direct API call returns **400**
  with "You cannot delete your own account" (`user_handler.go:347`) — not a 403.
- **Known issue:** The `DataTable` row Delete icon (`UserList.tsx:314`) carries **no** self-deletion
  guard; only the overflow-menu item does. The 400 from the API is the real protection. FEATURES 7.4
  notes there is no E2E delete test at all today.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-users.spec.ts` (extended) — closes half of G20.

### TC-XCUT-045 — A ticket whose customer was erased still opens
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin. Create a customer, create a ticket against it,
  then delete the customer.
- **Steps:** Open `/tickets` and then the ticket's detail page.
- **Expected:** The ticket is still listed and still opens — erasure keeps the row and its foreign
  keys (FEATURES 12.9). The **Customer** column and the detail page's Customer field both read
  `N/A`. The API confirms why: GORM's `Preload("Customer")` skips the soft-deleted row, so the
  value-typed `Customer` field (`internal/models/ticket.go:25`) serialises as a zero object
  (`"customer":{"id":0,"first_name":"","email":"",...}`). The anonymised `.invalid` placeholder
  address never reaches the client.
- **Known issue:** `TicketList.tsx:142` and `TicketDetail.tsx:183` read `customer.company_name`,
  but the API field is `company` (`internal/models/customer.go:9`), so the Customer column shows
  `N/A` for **live** customers too — an erased and a healthy customer are indistinguishable here.
- **Automation:** planned — `gocrm-ui/e2e/tests/erasure-ui.spec.ts` (new)

### TC-XCUT-046 — A task whose assignee was erased still renders
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin. Create a user, create a task assigned to them,
  then delete the user.
- **Steps:** Open `/tasks` and then the task's detail page.
- **Expected:** The task is still listed, and the **Assigned To** cell reads "Unassigned" — but it
  reads "Unassigned" for **every** task, erased assignee or live one. The column's id is `assignee`
  (`TaskList.tsx:165`), a field that never exists on the payload: the API sends the user object as
  `assigned_to` (`internal/models/task.go:27`), and `transformTaskFromBackend`
  (`api/endpoints/tasks.ts:36`) then overwrites `assigned_to` with the numeric `assigned_to_id`
  without ever setting `assignee`. The cell's `format` therefore always receives `undefined` and
  falls into its "Unassigned" fallback (`TaskList.tsx:172`) — erasure produces no visible change
  here. Ticket assignees are the surface where erasure *is* observable: `TicketList` reads the
  `assigned_to` object the API sends, and `Ticket.AssignedTo` is a pointer
  (`internal/models/ticket.go:27`), so a live assignee shows their name and an erased one
  "Unassigned".
- **Known issue:** The task **detail** page has the same field mismatch — it reads `task.assignee`
  and `task.creator`, which the payload never carries, so it shows "Unassigned"/"Unknown" for every
  task regardless of erasure (`TaskDetail.tsx:222`, `:236`). No task surface distinguishes an
  erased assignee from a healthy one; asserting an erasure-specific rendering on tasks would be
  wrong.
- **Automation:** planned — `gocrm-ui/e2e/tests/erasure-ui.spec.ts` (new)

### TC-XCUT-047 — Deactivation is the reversible alternative and keeps the personal data
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin; a user created by the test.
- **Steps:**
  1. Open `/users/<id>/edit`, switch **Active Account** off and save.
  2. Try to log in as that user in a fresh context.
  3. Switch **Active Account** back on and log in again.
- **Expected:** `PUT /api/v1/users/:id` returns **200**; `/users` still lists the account with its
  real name and email and an "Inactive" status chip — nothing is anonymised (FEATURES 12.10). The
  login attempt in step 2 fails with the same generic **401** as a wrong password (deliberate
  anti-enumeration). Step 3 restores access, proving the action is reversible.
- **Known issue:** The two dedicated activate/deactivate affordances do **not** work. The
  `UserList` overflow menu can never open — its trigger is
  `onClick={(e) => selectedUser && handleMenuOpen(e, selectedUser)}` (`UserList.tsx:320`), and
  `selectedUser` is only ever set *by* `handleMenuOpen`, so the guard is always false. The `UserDetail`
  **Deactivate**/**Activate** button calls `POST /users/:id/deactivate` and `/activate`
  (`api/endpoints/users.ts:72-80`), which are not registered in `routes.go` at all, so it 404s into
  the "Failed to deactivate user" snackbar. Per the repository's standing directive those endpoint
  functions are intended contract awaiting a backend, not dead code. The Edit form's switch is the
  only working path. FEATURES 7.5 / 12.10 and gap G20 cover the missing coverage.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-users.spec.ts` (extended) — closes the rest of G20.

---

## Cross-entity CRM workflow (FEATURES 11.8)

### TC-XCUT-048 — Walk a person from lead to customer to ticket to task
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin. `generateLeadData()`, `generateTicketData()`,
  `generateTaskData()`.
- **Steps:**
  1. Create a lead through `/leads/new` and set its status to `qualified`.
  2. From `/leads`, use the row action **Convert** and confirm the dialog.
  3. Open `/customers` and find the customer created by the conversion.
  4. Create a ticket through `/tickets/new` against that customer.
  5. Create a task through `/tasks/new` linked to the same customer, assigning it to the admin.
  6. Open the ticket detail and the task detail.
- **Expected:** `POST /api/v1/leads/:id/convert` returns **200**, the snackbar "Lead converted to
  customer successfully" appears, and the lead's status becomes `converted`. The new customer carries
  the lead's name, email, phone and company. The ticket is created with **201** and lists on
  `/tickets`; the task is created with **201** and lists on `/tasks`. Converting the same lead a
  second time is refused by the API (the lead is already converted).
- **Known issue:** The conversion copies the personal data into a second row, which is why deleting
  either half cascades to the other (FEATURES 12.6). A test that deletes only the customer must not
  assume the lead survives.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts` "admin can
  create complete CRM workflow: Lead -> Customer -> Task" creates the three entities independently:
  it never calls Convert and never creates a ticket. Extend that spec, or add the conversion leg to
  `gocrm-ui/e2e/tests/admin-leads.spec.ts`.

### TC-XCUT-049 — Validation failures across the four create forms keep the user on the form
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Logged in as the seeded admin.
- **Steps:** For `/leads/new`, `/customers/new`, `/tasks/new` and `/users/new`, click **Save** with
  every field empty.
- **Expected:** Each form shows its zod/react-hook-form field errors, no POST is issued, and the URL
  still ends in `/new`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts` "admin can handle error
  scenarios gracefully"

### TC-XCUT-050 — An admin can create one account per role and each can log in
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Logged in as the seeded admin. Three `generateUserData()` records forced to
  `sales`, `support` and `customer`.
- **Steps:** Create each through `/users/new`, then log in as each in a fresh browser context,
  pausing ≥6 s between logins.
- **Expected:** All three creations return **201** with the requested role — `POST /users` is the
  admin-guarded route where an elevated role may be assigned, unlike `/auth/register`, which always
  forces `customer`. Each account logs in successfully and lands on `/`, where the sidebar matches
  TC-XCUT-029.
- **Known issue:** Without pacing, the third login trips the strict `/auth` tier. This is the
  fixture every RBAC case in this file depends on; it belongs in a shared helper rather than in each
  spec.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts` "admin can
  manage user roles and access control" creates the three accounts but never logs in as them. The
  login half is planned for `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new).
