# Dashboard — Test Cases

Playwright test-case catalog for the dashboard: the landing page at `/`, its five stat cards, the
Quick Actions panel, the Sales Performance chart, the Recent Activities and Upcoming Tasks widgets,
and the nine `/dashboard/*` analytics endpoints plus `/tasks/upcoming` that sit behind them. Every
case below records the behaviour the code produces today; where that behaviour is wrong or
surprising the **Expected** field still states what happens now and a **Known issue** line names the
defect. Several stat and chart semantics documented here are counter-intuitive (a conversion rate
that can exceed 100 %, a "Sales Performance" chart that plots conversions rather than sales, an
"Upcoming Tasks" widget that leads with overdue work) — those are the cases worth reading first.

**Sources**

- `gocrm-ui/src/pages/Dashboard.tsx`
- `gocrm-ui/src/api/endpoints/dashboard.ts`, `gocrm-ui/src/api/client.ts`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `internal/handler/dashboard_handler.go`, `internal/handler/task_handler.go` (`GetUpcoming`),
  `internal/handler/routes.go` (`SetupDashboardRoutes`, `SetupTaskRoutes`), `cmd/main.go`
- `internal/service/{lead,customer,ticket,task}_service.go` and the matching repositories
  (`Count`, `CountOpen`, `CountPending`, `CountByStatus`, `ListRecent`, `ListUpcoming`,
  `ConversionTimestampsSince`)
- `internal/middleware/auth.go` (`RequireRole`), `internal/utils/response.go` (`RespondForbidden`)
- `gocrm-ui/e2e/tests/{login,registration,admin-entity-suite}.spec.ts`,
  `gocrm-ui/e2e/pages/dashboard.page.ts`, `gocrm-ui/e2e/helpers/admin-auth.ts`,
  `gocrm-ui/e2e/fixtures/admin-user.ts`
- `docs/FEATURES.md` sections 2.1, 2.2, 10b ("Dashboard analytics"), Gap Summary G15/G34;
  `docs/ROADMAP.md` ("Follow-ups from the backend build-out")
- Visual reference: `docs/screenshots/dashboard/01-overview.png`

**Constraints**

- `docs/FEATURES.md` row 2.1 claims `admin-entity-suite.spec.ts` "navigation includes dashboard".
  It does not: the navigation test iterates Leads, Customers, Tickets, Tasks and Users only. The
  dashboard is reached incidentally by `AdminAuthHelper.ensureAdminLoggedIn()`, which waits for
  `waitForURL('/')`. Treat the dashboard as effectively unautomated.
- `docs/ROADMAP.md` used to list `/dashboard/stats` as unguarded. It is guarded now —
  `SetupDashboardRoutes` applies `RequireRole(admin, sales, support)` to all nine routes
  (`internal/handler/routes.go:105`) — and ROADMAP's follow-up section records the fix. Cases here
  assert the guard, not the old defect.
- A **customer**-role account is obtainable in-test from public `POST /auth/register`, which always
  forces `customer`. **Sales** and **support** accounts are not: they require an admin-authenticated
  `POST /users`, and no e2e helper does that today (`gocrm-ui/e2e/helpers/` contains only
  `admin-auth.ts`). Sales/support cases are marked *blocked* on that helper.
- Records used by these cases must be created by the test through the faker generators in
  `gocrm-ui/e2e/fixtures/admin-user.ts` (`generateLeadData`, `generateCustomerData`,
  `generateTicketData`, `generateTaskData`). Never delete a record the test did not create: user,
  customer and lead deletion is irreversible GDPR erasure.
- The backend runs at `http://localhost:8090/api/v1` and the SPA at `http://localhost:5173`. The
  moderate rate limiter (120 req/min, burst 30) applies to every authenticated request, dashboard
  widget calls included.

---

## 2.1 Dashboard stats — access and role gating

### TC-DASH-001 — Load the dashboard as admin and confirm the four widget requests
- **Priority:** P0
- **Type:** functional
- **Preconditions:** logged in as the seeded admin (`testAdminCredentials`) via `AdminAuthHelper`.
- **Steps:**
  1. Start recording responses for URLs containing `/dashboard/`.
  2. Navigate to `/`.
  3. Wait for the `h4` heading "Dashboard" to be visible and for network idle.
- **Expected:** exactly four dashboard requests fire, all returning 200:
  `GET /dashboard/stats`, `GET /dashboard/activities?limit=10`,
  `GET /dashboard/sales-performance?period=month`, `GET /dashboard/upcoming-tasks?limit=5`.
  The page renders five stat cards, the Quick Actions panel, the Sales Performance paper, Recent
  Activities and Upcoming Tasks.
- **Known issue:** `Dashboard.tsx` calls only four of the nine analytics endpoints. `leads-by-status`,
  `tickets-by-priority`, `tasks-by-status`, `recent-tickets` and `new-leads` are implemented,
  routed and exported from `dashboardApi` but no widget consumes them (see TC-DASH-030).
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-002 — Customer-role user loads the dashboard without stats or charts
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a freshly registered account from `POST /auth/register` (always `customer`),
  using the faker generator in the registration spec's fixtures.
- **Steps:**
  1. Register and land on `/`.
  2. Record all network requests for 3 s after load.
  3. Inspect the page body.
- **Expected:** the page loads (HTTP 200, no redirect). No request is made to any `/dashboard/*`
  URL — `Dashboard.tsx` sets `canViewStats` false for the customer role and passes
  `enabled: canViewStats` to all four queries. The five stat cards, the Sales Performance paper,
  Recent Activities and Upcoming Tasks are all absent. Only the "Dashboard" heading and the Quick
  Actions panel render.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new; the redirect half is already
  covered by `gocrm-ui/e2e/tests/registration.spec.ts` "successful registration redirects to dashboard")

### TC-DASH-003 — Every dashboard endpoint rejects a customer-role token with 403
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a customer account registered by the test; its JWT read from
  `localStorage.gophercrm_token`.
- **Steps:**
  1. For each of `/dashboard/stats`, `/leads-by-status`, `/tickets-by-priority`, `/tasks-by-status`,
     `/sales-performance`, `/activities`, `/upcoming-tasks`, `/recent-tickets`, `/new-leads`, issue
     `GET` through `request.get()` with `Authorization: Bearer <customer token>`.
- **Expected:** every call returns **403** with body
  `{"success":false,"error":{"code":"FORBIDDEN","message":"Insufficient permissions"}}`
  (`middleware.RequireRole` → `utils.RespondForbidden`). No endpoint leaks counts to a customer.
- **Known issue:** `docs/ROADMAP.md` historically listed `/dashboard/stats` as unguarded; the guard
  now exists at `internal/handler/routes.go:105`. This case pins it so the regression cannot return.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-004 — Sales-role user sees the full dashboard with system-wide numbers
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a sales user created by an admin via `POST /users` with `generateUserData()`
  overridden to `role: 'sales'`, then logged in.
- **Steps:**
  1. Log in as the sales user and navigate to `/`.
  2. Read the Total Leads card value.
  3. Navigate to `/leads` and read the reported total.
- **Expected:** all four widget requests return 200 and the stat cards render. **Total Leads on the
  dashboard is the system-wide lead count, not the sales user's own** — `leadRepository.Count()`
  applies no owner filter — so it is normally larger than the count on `/leads`, which
  `LeadHandler.List` narrows to the caller's own leads.
- **Known issue:** the dashboard totals are unscoped for every permitted role. The swagger
  description on `GetStats` states this explicitly; the UI does not.
- **Automation:** blocked — needs a role-login helper; only `AdminAuthHelper` exists in
  `gocrm-ui/e2e/helpers/`

### TC-DASH-005 — Support-role user sees the full dashboard
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** a support user created by an admin via `POST /users`, then logged in.
- **Steps:**
  1. Log in as support and navigate to `/`.
  2. Confirm the stat cards and both widgets render.
  3. Call `GET /dashboard/new-leads` directly with the support token.
- **Expected:** the page renders in full (support is inside the `RequireRole` set). `GET
  /dashboard/new-leads` returns **200 with an empty array**, not 403: `GetNewLeads` falls through to
  the `default` branch and returns `[]models.Lead{}` for support, deliberately, so the page they are
  entitled to load does not break even though `/leads` is admin+sales only.
- **Automation:** blocked — needs a role-login helper

### TC-DASH-006 — Unauthenticated visit to the dashboard redirects to login
- **Priority:** P0
- **Type:** negative
- **Preconditions:** no token in either storage.
- **Steps:**
  1. Clear `gophercrm_token` and `gophercrm_refresh_token` from `localStorage` and `sessionStorage`.
  2. Navigate to `/`.
- **Expected:** `ProtectedRoute` sees `isAuthenticated` false and issues
  `<Navigate to="/login" replace />`; the URL becomes `/login`. A direct
  `GET /api/v1/dashboard/stats` with no `Authorization` header returns **401**.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended). The existing test
  "unauthenticated user is redirected to login" asserts the same behaviour for `/users`, not for `/`.

---

## 2.1 Dashboard stats — values

### TC-DASH-007 — Five stat cards render the values returned by /dashboard/stats
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Intercept the `GET /dashboard/stats` response body.
  2. Navigate to `/` and wait for the skeletons to be replaced.
  3. Read each card's `h4` value via `DashboardPage.getTotalLeads()`, `getTotalCustomers()`,
     `getOpenTickets()`, `getPendingTasks()` and `getConversionRate()`.
- **Expected:** the cards titled "Total Leads", "Total Customers", "Open Tickets", "Pending Tasks"
  and "Conversion Rate" show `total_leads`, `total_customers`, `open_tickets`, `pending_tasks` and
  `conversion_rate` from the intercepted payload. The conversion rate is rendered with exactly one
  decimal and a percent sign (`value.toFixed(1) + '%'`).
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-008 — Total Leads and Total Customers count every live row, system-wide
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Read Total Leads and Total Customers from the dashboard.
  2. Create one lead with `generateLeadData()` and one customer with `generateCustomerData()`.
  3. Return to `/` and force a reload so the TanStack Query cache refetches.
- **Expected:** both counters increase by exactly one. The counts come from
  `leadRepository.Count()` / `customerRepository.Count()`, plain `Model(&X{}).Count()` calls with no
  `WHERE` clause, so they are unscoped by role and by owner. GORM's default soft-delete scope
  applies, so rows with `deleted_at` set are excluded.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-009 — "Open Tickets" and "Pending Tasks" include the in_progress state
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Read the Open Tickets and Pending Tasks values.
  2. Create a ticket with `generateTicketData()` forced to `status: 'in_progress'`.
  3. Create a task with `generateTaskData()` forced to `status: 'in_progress'`.
  4. Reload `/`.
- **Expected:** both counters increase by one. `ticketRepository.CountOpen()` counts
  `status IN ('open','in_progress')` and `taskRepository.CountPending()` counts
  `status IN ('pending','in_progress')`.
- **Known issue:** the card labels are narrower than the query. A ticket in `in_progress` is
  reported as "Open" and a task in `in_progress` as "Pending"; a user reconciling the card against
  a status filter on `/tickets` or `/tasks` will get a different number. FEATURES.md row 2.1 is
  marked **gap** — no test asserts the values at all.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-010 — Conversion Rate can exceed 100 %
- **Priority:** P0
- **Type:** functional
- **Preconditions:** admin logged in against a database where customers were created directly rather
  than by lead conversion — which is the normal seeded state.
- **Steps:**
  1. Read `total_leads`, `total_customers` and `conversion_rate` from `GET /dashboard/stats`.
  2. Read the Conversion Rate card on `/`.
- **Expected:** `conversion_rate == total_customers / total_leads * 100`
  (`dashboard_handler.go:96`), rendered to one decimal. On the current seeded database that is
  `95 / 71 * 100 = 133.8 %`; the screenshot run captured `90 / 65 * 100 = 138.5 %`. The card shows a
  value above 100 % and no clamping occurs.
- **Known issue:** the formula divides two unrelated populations. `total_customers` counts **all**
  customers, including those created directly through `POST /customers`, while `total_leads` counts
  leads that are still present — and a lead that converts stays in the lead table with status
  `converted`, so it is counted on both sides. The number is not a conversion rate in any useful
  sense and is unbounded above. FEATURES.md row 2.1 (**gap**), G15.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-011 — Conversion Rate is 0.0 % when there are no leads
- **Priority:** P2
- **Type:** validation
- **Preconditions:** an empty database (or a stubbed `GET /dashboard/stats` returning
  `total_leads: 0`, `total_customers: 3`).
- **Steps:**
  1. Route-intercept `**/dashboard/stats` and fulfil with a payload whose `total_leads` is 0.
  2. Navigate to `/`.
- **Expected:** the card shows `0.0%`. The handler guards the division with `if totalLeads > 0`
  (`dashboard_handler.go:95`), so no `NaN`/`+Inf` reaches the JSON. The frontend's
  `(stats?.conversion_rate || 0).toFixed(1)` gives the same result if the field is missing.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-012 — Stat-card trend arrows are hardcoded, not computed
- **Priority:** P2
- **Type:** regression
- **Preconditions:** admin logged in.
- **Steps:**
  1. Note the trend line under Total Leads, Total Customers and Open Tickets.
  2. Create three leads with `generateLeadData()`.
  3. Reload `/` and re-read the trend lines.
- **Expected:** the trends are unchanged: green "12 %" under Total Leads, green "8 %" under Total
  Customers, red "5 %" under Open Tickets, and none under Pending Tasks or Conversion Rate. They are
  literals passed as `trend={12}`, `trend={8}` and `trend={-5}` in `Dashboard.tsx` (lines 149, 162,
  175); the API returns no trend data at all.
- **Known issue:** the arrows imply a period-over-period comparison the backend never computes. They
  will read "12 % up" on an empty database.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-013 — Deleting a lead removes it from the stats and from the widgets
- **Priority:** P1
- **Type:** regression
- **Preconditions:** admin logged in.
- **Steps:**
  1. Create a lead with `generateLeadData()` and note its company name and email.
  2. Reload `/`, record Total Leads, and confirm the new lead heads Recent Activities as
     "New lead — <Contact> (<Company>) was added as a lead".
  3. Go to `/leads`, delete **that lead only**, and confirm the deletion dialog.
  4. Return to `/` and reload.
- **Expected:** Total Leads is back to its pre-creation value and the activity entry is gone.
  Deletion erases the lead's personal fields and then soft-deletes the row, and every dashboard
  query runs under GORM's soft-delete scope, so the row stops being counted and stops appearing in
  `ListRecent`. Re-registering the same email afterwards succeeds — erasure replaced the stored
  address with a random `.invalid` one.
- **Known issue:** deletion is irreversible GDPR erasure (FEATURES.md section 12). Only delete
  records the test created.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-014 — Assert every stat against records the test seeded (G15)
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in; API request context with the admin token.
- **Steps:**
  1. `GET /dashboard/stats` and store the five values as the baseline.
  2. Create, via the API, one lead, one customer, one `open` ticket and one `pending` task using the
     faker generators.
  3. `GET /dashboard/stats` again.
  4. Recompute the expected conversion rate from the new totals.
- **Expected:** `total_leads`, `total_customers`, `open_tickets` and `pending_tasks` are each exactly
  baseline + 1, and `conversion_rate` equals `total_customers / total_leads * 100` to within
  floating-point tolerance.
- **Known issue:** closes G15 — "Dashboard stats not tested for correctness"; there is no
  `dashboard_handler_test.go` assertion of stat values against the database today.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

---

## 2.2 Quick Actions

### TC-DASH-015 — Quick Action buttons navigate to their targets as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. On `/`, click "New Lead"; assert the URL, then go back to `/`.
  2. Repeat for "New Ticket", "New Task" and "View Customers".
- **Expected:** the four buttons navigate to `/leads/new`, `/tickets/new`, `/tasks/new` and
  `/customers` respectively (`Dashboard.tsx` lines 215, 223, 231, 238) and the target page renders.
  FEATURES.md row 2.2 is **untested**.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-016 — Quick Actions are shown to every role, including customer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a customer account registered by the test.
- **Steps:**
  1. Land on `/` after registration.
  2. Assert all four Quick Action buttons are visible.
  3. Compare against the sidebar.
- **Expected:** all four buttons are visible. The Quick Actions panel sits **outside** the
  `canViewStats` guard in `Dashboard.tsx` (lines 207-243) and has no role check of its own, while
  `MainLayout`'s `navItems` hides Leads (admin+sales), Customers (admin+sales+support) and Tickets
  (admin+support) from a customer. The sidebar and the Quick Actions therefore disagree on the same
  page.
- **Known issue:** the panel advertises actions the caller cannot perform. Backend guards still hold
  — `/leads` is `RequireRole(admin, sales)` and ticket create is support+admin — so this is a UI
  affordance defect, not an authorization hole.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-017 — Customer clicking "New Ticket" is redirected to /unauthorized
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a customer account registered by the test, on `/`.
- **Steps:**
  1. Click "New Ticket".
- **Expected:** the URL becomes `/unauthorized` and the Unauthorized page renders. `tickets/new`
  sits under the pathless `ProtectedRoute requiredRole={['admin','support']}` layout route in
  `routes/index.tsx`, which issues `<Navigate to="/unauthorized" replace />`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-018 — Customer clicking "New Lead" reaches the lead form, which then fails on the API
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a customer account registered by the test, on `/`.
- **Steps:**
  1. Click "New Lead".
  2. Fill the form with `generateLeadData()` and submit.
- **Expected:** the URL becomes `/leads/new` and the `LeadForm` page renders — the `leads/*` routes
  carry **no** `requiredRole` in `routes/index.tsx`, so the client-side guard does not fire. The
  submit issues `POST /leads`, which the backend rejects with **403 / `Insufficient permissions`**
  (the whole `/leads` group is `RequireRole(admin, sales)`), and the form surfaces the error rather
  than navigating away.
- **Known issue:** the front end and the back end disagree: `/tickets/new` is route-guarded but
  `/leads/new`, `/tasks/new` and `/customers` are not, so three of the four Quick Actions lead a
  customer into a page they cannot use. Related to the "Sales ticket navigation" product call in
  `docs/ROADMAP.md`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-019 — Support clicking "New Lead" reaches a form it cannot submit
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** a support user created by an admin via `POST /users`, then logged in, on `/`.
- **Steps:**
  1. Confirm the sidebar shows no "Leads" item.
  2. Click the "New Lead" Quick Action.
  3. Fill the form and submit.
- **Expected:** the sidebar hides Leads (`navItems` restricts it to admin+sales) yet the Quick Action
  navigates to `/leads/new`; `POST /leads` returns **403**.
- **Automation:** blocked — needs a role-login helper

---

## 10b — Sales Performance chart (`GET /dashboard/sales-performance`)

### TC-DASH-020 — Chart renders twelve calendar-month buckets ending with the current month
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Intercept `GET /dashboard/sales-performance?period=month`.
  2. Navigate to `/` and wait for the recharts SVG inside the "Sales Performance" paper.
  3. Read the response `labels` array and the rendered X-axis tick labels.
- **Expected:** `labels` holds twelve `YYYY-MM` strings, oldest first, the last being the current
  month (verified live: `2025-09 … 2026-08`), and `datasets[0].label` is `"Conversions"`. The
  X axis renders a subset of those ticks (recharts thins them); the axis order matches the array.
  Buckets are built in Go by `buildTimeBuckets` with exact `AddDate` calendar arithmetic, so no
  month is skipped or doubled.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-021 — The chart plots lead conversions, not lead volume, and is flat when few leads convert
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in against the seeded database.
- **Steps:**
  1. Read `total_leads` from `GET /dashboard/stats`.
  2. Read `GET /dashboard/leads-by-status` and note the `converted` count.
  3. Read `GET /dashboard/sales-performance?period=month` and sum `datasets[0].data`.
- **Expected:** the summed series counts only leads whose status is `converted` and whose
  `updated_at` falls inside the twelve-month window — verified live: 71 leads, 7 of them
  `converted`, series `[0,0,0,0,0,0,7,0,0,0,0,0]`. Most buckets are 0, so the area chart is a flat
  line at the baseline with a single spike, and the Y axis auto-scales to the largest bucket. The
  screenshot run captured this as an apparently empty chart despite 65 leads existing.
- **Known issue:** the widget is titled "Sales Performance" but the dataset is labelled
  "Conversions" and counts lead status transitions. This CRM stores neither revenue nor pipeline
  value, so no sales figure is available; the title is misleading. Note also that recharts animates
  the area on mount, so a screenshot taken immediately after load can capture the series
  part-drawn — assert on the intercepted payload, not on pixels.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-022 — Conversion time is `updated_at`, so a later edit moves the data point
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin logged in; a lead the test created and converted in an earlier month is
  not reproducible in a single run, so drive this through two reads around one edit.
- **Steps:**
  1. Create a lead with `generateLeadData()` and convert it to a customer.
  2. Read `GET /dashboard/sales-performance?period=month` and note the current month's bucket value.
  3. Edit the converted lead (change its notes) and save.
  4. Re-read the endpoint.
- **Expected:** the lead counts once in the current month's bucket both times; the edit rewrites
  `updated_at`, which is what `ConversionTimestampsSince` plucks. A lead converted in an earlier
  month and edited today would move from its conversion month into the current one.
- **Known issue:** `leads` has no `converted_at` column, so `updated_at` stands in for the
  conversion moment — documented in the repository comment on `ListRecentlyConverted` and in the
  swagger description of `GetSalesPerformance`. Any post-conversion edit silently rewrites history.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-023 — `period` accepts week/quarter/year and falls back to month for anything else
- **Priority:** P2
- **Type:** validation
- **Preconditions:** admin API token.
- **Steps:**
  1. `GET /dashboard/sales-performance?period=week`, `=quarter`, `=year`, `=banana` and with the
     parameter omitted.
- **Expected:** 200 in every case. `week` → 12 labels formatted `YYYY-MM-DD`, each the Monday of an
  ISO week; `quarter` → 8 labels formatted `YYYY-QN`; `year` → 5 labels formatted `YYYY`;
  `banana` and the omitted case → the 12-month series (the `default` arm of `buildTimeBuckets`).
  No 400 is returned for an unrecognised period.
- **Known issue:** the UI can never exercise the other windows — `Dashboard.tsx:114` hardcodes
  `dashboardApi.getSalesPerformance('month')` and renders no period selector, even though
  `dashboardApi` types the parameter as `'week' | 'month' | 'quarter' | 'year'`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new; API-level assertions
  through `request.get()`)

---

## 10b — Recent Activities (`GET /dashboard/activities`)

### TC-DASH-024 — Activity feed lists synthesised events newest first
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in against a database with leads, tickets and tasks.
- **Steps:**
  1. Intercept `GET /dashboard/activities?limit=10`.
  2. Navigate to `/` and read the Recent Activities list items.
- **Expected:** at most 10 entries, each rendered as a primary title and a secondary
  `"<description> — <locale date>"`. The payload entries carry `type` in
  `lead_created | lead_converted | ticket_created | ticket_resolved | task_completed`, an `id` of
  the form `lead-42-created`, and are sorted by `created_at` descending with an `id` tie-break. The
  titles rendered are "New lead", "Lead converted", "New ticket", "Ticket resolved",
  "Task completed".
- **Known issue:** there is no activity or audit table; entries are synthesised from row timestamps,
  and the resolved/converted/completed variants use `updated_at`, so a later edit re-dates the
  event.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-025 — A lead created by the test appears at the top of the feed
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Create a lead with `generateLeadData()`.
  2. Navigate to `/` and reload.
- **Expected:** the first Recent Activities entry reads "New lead" with the description
  `"<First> <Last> (<Company>) was added as a lead"` built by `describePerson`, followed by today's
  locale-formatted date. A lead with no name would render "Unnamed lead"; a lead with no company
  omits the parenthesised part.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-026 — The activity limit is capped at 50 and bad values fall back to 10
- **Priority:** P2
- **Type:** validation
- **Preconditions:** admin API token.
- **Steps:**
  1. `GET /dashboard/activities?limit=200`, `?limit=0`, `?limit=-3`, `?limit=abc`, and with no
     parameter.
- **Expected:** 200 in every case. `limit=200` returns at most 50 entries (`maxDashboardLimit`); `0`,
  `-3`, `abc` and the omitted case all fall back to 10 (`parseDashboardLimit` ignores non-positive
  and unparseable values). The response is always a JSON array, never `null`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-027 — The activity actor carries the account email in `username`
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin API token.
- **Steps:**
  1. `GET /dashboard/activities?limit=10` and inspect the `user` object of a `lead_created` entry
     and of a `ticket_created` entry for an unassigned ticket.
- **Expected:** `user.username` holds the account's **email address** — `models.User` has no
  username column, so `activityUserFrom` maps `user.Email` onto it (verified live:
  `"username":"test-admin@gocrm.test"`). For a record with no resolvable owner or assignee the
  `user` object is present but zero-valued (`{"id":0,"username":"","first_name":"","last_name":""}`)
  rather than `null`.
- **Known issue:** the feed exposes account email addresses to every admin, sales and support
  caller. The dashboard UI does not render the actor, so nothing is displayed today, but the field
  is in the payload.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-028 — Empty activity feed shows the "Nothing yet" placeholder
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Route-intercept `**/dashboard/activities**` and fulfil with `{"success":true,"data":[]}`.
  2. Navigate to `/`.
- **Expected:** the Recent Activities paper shows the text "Nothing yet" in the secondary text
  colour and no list items (`Dashboard.tsx:300`). The same placeholder applies to Upcoming Tasks.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

---

## 10b — Upcoming Tasks (`GET /dashboard/upcoming-tasks`, `GET /tasks/upcoming`)

### TC-DASH-029 — Upcoming Tasks widget lists at most five tasks in due-date order with a priority chip
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Intercept `GET /dashboard/upcoming-tasks?limit=5`.
  2. Navigate to `/` and read the Upcoming Tasks list items.
- **Expected:** at most five items, each showing the task title, a secondary
  `"Due <locale date>"`, and a right-aligned outlined chip carrying the task priority. The payload
  is ordered by `due_date` ascending (`taskRepository.ListUpcoming`), and tasks whose status is
  `completed` or `cancelled`, or that have no `due_date` at all, are excluded by `openDueQuery`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-030 — "Upcoming Tasks" leads with overdue tasks
- **Priority:** P1
- **Type:** functional
- **Preconditions:** admin logged in against a database containing at least one open task whose due
  date is in the past.
- **Steps:**
  1. Read `GET /dashboard/upcoming-tasks?limit=5`.
  2. Compare the first entry's `due_date` with the current date.
- **Expected:** the first entry may be — and on the seeded database is — already overdue (verified
  live: a `pending` task due `2026-03-22` returned first on `2026-08-07`; the screenshot shows
  "Due 3/22/2026"). `ListUpcoming` deliberately keeps overdue tasks because they most need
  attention, and the widget renders the past date verbatim with no overdue styling.
- **Known issue:** a widget titled "Upcoming Tasks" whose top rows are months overdue, with nothing
  in the UI marking them as late.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-031 — Upcoming Tasks is unscoped for admin and self-scoped for every other role
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** an admin and a support user; at least one task assigned to a third user.
- **Steps:**
  1. As admin, read `GET /dashboard/upcoming-tasks?limit=50` and note the distinct
     `assigned_to_id` values.
  2. As the support user, read the same endpoint.
- **Expected:** the admin response spans multiple assignees (`taskService.GetUpcoming`), while the
  support response contains only tasks whose `assigned_to_id` is the support user's own id —
  `GetUpcomingTasks` branches on `c.GetString("user_role") == "admin"` and otherwise calls
  `GetUpcomingByAssignee(c.GetUint("user_id"), …)`.
- **Automation:** blocked — needs a role-login helper

### TC-DASH-032 — `/tasks/upcoming` behaves differently from the dashboard widget and has no role guard
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** an admin token and a customer token (the latter from public registration).
- **Steps:**
  1. As admin, `GET /tasks/upcoming` and `GET /tasks/upcoming?days=200` and `?days=abc`.
  2. As customer, `GET /tasks/upcoming`.
  3. As customer, `GET /dashboard/upcoming-tasks` for comparison.
- **Expected:** as admin, 200 with the open tasks due between **now** and N days ahead — overdue
  tasks are **excluded** here, unlike the dashboard widget; `days` defaults to 7, `abc` and
  non-positive values fall back to 7, and 200 is clamped to 90. At most 100 tasks are returned.
  As customer, `GET /tasks/upcoming` returns **200** (usually an empty array) because
  `SetupTaskRoutes` applies **no** `RequireRole` to the `/tasks` group — the handler narrows a
  non-admin to its own assignments — while `GET /dashboard/upcoming-tasks` returns **403** for the
  same account.
- **Known issue:** two endpoints named for the same concept disagree on whether overdue work is
  "upcoming" and on who may call them. Nothing in the UI calls `/tasks/upcoming`.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new; API-level assertions)

---

## 10b — Analytics endpoints with no dashboard widget

### TC-DASH-033 — Distribution endpoints return zero-filled canonical label sets
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin API token.
- **Steps:**
  1. `GET /dashboard/leads-by-status`, `/dashboard/tickets-by-priority`,
     `/dashboard/tasks-by-status`.
- **Expected:** each returns `{labels, datasets:[{label, data}]}` with the full canonical label set
  in fixed order and a zero where no row holds that value — leads
  `new, contacted, qualified, unqualified, converted` (verified live:
  `[17,22,25,0,7]`), tickets `low, medium, high, urgent`, tasks
  `pending, in_progress, completed, cancelled`. The dataset labels are `"Leads"`, `"Tickets"`,
  `"Tasks"`. A value present in the data but absent from the canonical list is appended after the
  canonical ones in sorted order rather than dropped (`buildChart`). `labels` and `datasets` are
  never `null`.
- **Known issue:** no dashboard widget renders any of these. `dashboardApi.getLeadsByStatus`,
  `getTicketsByPriority`, `getTasksByStatus`, `getRecentTickets` and `getNewLeads` exist and the
  routes are live, but `Dashboard.tsx` calls only `getStats`, `getRecentActivities`,
  `getSalesPerformance` and `getUpcomingTasks`.
- **Automation:** blocked — no UI surface renders these; the case can only be asserted at the API
  level from `gocrm-ui/e2e/tests/dashboard.spec.ts`

### TC-DASH-034 — Recent tickets and new leads scope by role
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** admin, sales and support tokens.
- **Steps:**
  1. As each role, `GET /dashboard/recent-tickets?limit=5` and `GET /dashboard/new-leads?limit=5`.
- **Expected:** `recent-tickets` returns the same system-wide newest-first list for admin, sales and
  support — visibility mirrors the ticket list, with no narrowing to the caller's assignments.
  `new-leads` returns every lead for admin, only the caller's own leads for sales
  (`GetRecentByOwner`), and an **empty array with status 200** for support. Both endpoints cap
  `limit` at 50 and never return `null`.
- **Automation:** blocked — needs a role-login helper; no UI surface renders these widgets

---

## Presentation and page-local behaviour

### TC-DASH-035 — Skeletons render while the dashboard queries are in flight
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin logged in.
- **Steps:**
  1. Route-intercept `**/dashboard/**` and delay each response by ~2 s.
  2. Navigate to `/` and inspect the page before the responses land.
- **Expected:** five rectangular `MuiSkeleton` blocks of height 140 stand in for the stat cards, a
  300-high skeleton for the Sales Performance chart, and 140-high skeletons for Recent Activities
  and Upcoming Tasks. The "Dashboard" heading and the Quick Actions panel are visible immediately —
  they do not depend on any query.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-036 — The Conversion Rate card wraps onto its own row
- **Priority:** P2
- **Type:** functional
- **Preconditions:** admin logged in, viewport at the Playwright default (1280 wide).
- **Steps:**
  1. Navigate to `/` and compare the bounding boxes of the five stat cards.
- **Expected:** four cards sit on the first row and Conversion Rate alone on the second. Each card
  is `flex: 1 1 calc(25% - 18px)` at the `md` breakpoint inside a `flexWrap: 'wrap'` container, so
  the fifth card necessarily wraps — this is the layout captured in
  `docs/screenshots/dashboard/01-overview.png`, not a rendering fault.
- **Automation:** planned — `gocrm-ui/e2e/tests/dashboard.spec.ts` (new)

### TC-DASH-037 — The Dashboard sidebar item is visible to every role and is the post-login landing page
- **Priority:** P2
- **Type:** functional
- **Preconditions:** any authenticated account.
- **Steps:**
  1. Log in.
  2. Assert the URL is `/`.
  3. Navigate to `/leads` (or any reachable page) and click the sidebar "Dashboard" item.
- **Expected:** login lands on `/`; the sidebar's Dashboard entry carries no `roles` restriction in
  `MainLayout`'s `navItems`, so it is visible to admin, sales, support and customer alike, and
  clicking it returns to `/`.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/login.spec.ts` "successful login
  redirects to dashboard" and `gocrm-ui/e2e/tests/registration.spec.ts` "successful registration
  redirects to dashboard" both assert the landing URL; the sidebar half is planned for
  `gocrm-ui/e2e/tests/dashboard.spec.ts`
