# Ticket Management — Test Cases

Playwright E2E test cases for the ticket area: list, create, edit, detail, delete, the status and
priority filters, search, sort, the "My Tickets" endpoint and the per-customer ticket listing, plus
`POST /tickets/bulk/status`. Every **Expected** below states what the application does *today* —
where the current behaviour is wrong or surprising, the expectation still describes it and a
**Known issue** line records the defect. Ticket authorization is the densest RBAC surface in the
product and is enforced in two independent places (the Gin handler and the React route/button
gating); where the two disagree, that disagreement gets its own case.

**Sources**

- `internal/handler/ticket_handler.go`, `internal/handler/routes.go` (`SetupTicketRoutes`,
  `SetupBulkStatusRoutes`), `cmd/main.go`
- `internal/service/ticket_service.go`, `internal/service/bulk_status_service.go`,
  `internal/repository/ticket_repository.go`, `internal/models/ticket.go`,
  `internal/models/customer.go`
- `internal/handler/bulk_handler.go` (`BulkTicketStatusRequest`, `BulkUpdateTicketStatus`)
- `gocrm-ui/src/pages/tickets/TicketList.tsx`, `TicketForm.tsx`, `TicketDetail.tsx` and their
  `*.test.tsx` siblings
- `gocrm-ui/src/api/endpoints/tickets.ts`, `customers.ts`, `gocrm-ui/src/api/client.ts`,
  `gocrm-ui/src/types/index.ts`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `gocrm-ui/src/pages/customers/CustomerDetail.tsx`
- `gocrm-ui/e2e/tests/admin-tickets.spec.ts`, `gocrm-ui/e2e/screenshots/05-tickets.spec.ts`,
  `gocrm-ui/e2e/pages/tickets.page.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`,
  `gocrm-ui/e2e/helpers/admin-auth.ts`
- `docs/FEATURES.md` section 5 (rows 5.1–5.9) and the Gap Summary (G18, G34); `docs/ROADMAP.md`
  ("Sales ticket navigation")
- `docs/screenshots/tickets/01-list.png` … `05-delete-confirm.png`

**Constraints**

- The only login helper today is `gocrm-ui/e2e/helpers/admin-auth.ts` (admin only). Every
  non-admin case needs a support / sales / customer account created through the admin-guarded
  `POST /users` (email from the faker generators in `gocrm-ui/e2e/fixtures/admin-user.ts`, password
  `TempPass123!` — `min=10` plus complexity) and then a real login through `/login`. Self-service
  `POST /auth/register` cannot be used: it always mints a `customer`.
- Ticket creation needs a customer to exist. Create one per spec run via the customers UI or
  `POST /customers` using `generateCustomerData()`; never rely on pre-existing rows.
- Ticket deletion is an ordinary soft delete — tickets carry no personal data of their own, so
  GDPR erasure does not apply to them (`internal/repository/erasure.go` covers users, customers and
  leads only). Cases may still only delete tickets they created.
- `GET /users` is admin-only, so any case that drives the "Assign to Support Agent" picker as a
  non-admin will see it empty. That is behaviour, not flake — see TC-TICK-014.

---

## 5.1 View Tickets List

### TC-TICK-001 — Load the tickets list as an admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in via `AdminAuthHelper.ensureAdminLoggedIn()`; at least one
  ticket exists (create one in `beforeAll` from `generateTicketData()` + a faker customer).
- **Steps:**
  1. Navigate to `/tickets`.
  2. Wait for the `h4` "Tickets" heading and the `table`.
- **Expected:** `GET /api/v1/tickets?page=1&limit=10&status=&priority=&search=` returns 200 with
  `data.tickets` and `data.total`. The "Create Ticket" button is visible. Columns render in this
  order: Ticket #, Subject, Customer, Status, Priority, Assigned To, Created, Last Updated. Ticket #
  is rendered as `#<id>`; Status/Priority are MUI chips with the label title-cased and `in_progress`
  shown as "In Progress".
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin can view tickets list page"

### TC-TICK-002 — Customer column always reads "N/A"
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a ticket created by the test with a customer explicitly
  selected in the form.
- **Steps:**
  1. Navigate to `/tickets`.
  2. Locate the row for the ticket created by the test.
  3. Read the "Customer" cell.
- **Expected:** The cell reads `N/A`, even though the API response for that row carries a fully
  populated `customer` object. Assert `N/A` *and* assert (via `page.waitForResponse`) that the
  `GET /tickets` payload contains `tickets[].customer.company` with the expected company name — the
  data is present, only the rendering is wrong.
- **Known issue:** `TicketList.tsx:142` formats the column as `value?.company_name`, but the backend
  serialises the field as `company` (`internal/models/customer.go:9`) and
  `transformTicketFromBackend` (`gocrm-ui/src/api/endpoints/tickets.ts:46`) does not map the nested
  customer the way `transformCustomerFromBackend` (`endpoints/customers.ts:31`) does. Same root
  cause as TC-TICK-026. Not recorded in FEATURES.md section 5.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-003 — Paginate the list and change rows per page
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; at least 11 tickets exist (seed the shortfall from
  `generateTicketData()`).
- **Steps:**
  1. Navigate to `/tickets`.
  2. Click the table's next-page control.
  3. Change "Rows per page" to 25.
- **Expected:** Page 2 issues `GET /tickets?page=2&limit=10`; the backend converts `page` to
  `offset=(page-1)*limit` (`ticket_handler.go` List). Changing rows per page issues
  `?page=1&limit=25` and resets to the first page. `meta.total_pages` in the response equals
  `ceil(total/limit)`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-004 — Sales user deep-links to /tickets and gets a read-only list
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user created via admin `POST /users` (faker email) and logged in.
- **Steps:**
  1. Confirm the left navigation shows Dashboard, Leads, Customers, Tasks, Settings — and **no**
     Tickets item.
  2. Navigate directly to `/tickets`.
- **Expected:** The nav item is absent (`MainLayout.tsx` gives the Tickets entry
  `roles: ['admin','support']`), but the deep link works: `/tickets` has no `ProtectedRoute
  requiredRole` in `routes/index.tsx`, and `GET /tickets` returns 200 for sales
  (`TicketHandler.List` rejects only the `customer` role). The table renders every ticket. The
  "Create Ticket" button is hidden and rows expose neither Edit nor Delete
  (`TicketList.tsx:89-91`).
- **Known issue:** Navigation and API disagree by design-drift — see `docs/ROADMAP.md`,
  "Sales ticket navigation": widening the nav or narrowing the read routes is an open product call.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-005 — Customer-role user reaching /tickets sees an empty table, not an error
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer-role user (register through `/register`, which always creates a
  `customer`) logged in.
- **Steps:**
  1. Navigate directly to `/tickets`.
  2. Capture the network response for `GET /api/v1/tickets`.
- **Expected:** The API returns **403** with `error.message` "Customers cannot list all tickets"
  (`ticket_handler.go` List). The page still renders the "Tickets" heading, the filter bar and the
  table header, with zero body rows and no error message: `client.ts` has no 403 branch, and
  `TicketList.tsx:313` falls back to `data?.data || []`. The "Create Ticket" button is hidden.
- **Known issue:** A 403 is silently indistinguishable from "no tickets exist"; the pattern is
  pinned by `TicketList.test.tsx` "handles API errors gracefully", which asserts only the header row
  survives.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-006 — Support user's list is not scoped to their assignments
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user logged in; at least one ticket assigned to that support user and
  at least one assigned to somebody else (both created by the test as admin beforehand).
- **Steps:**
  1. Navigate to `/tickets`.
  2. Compare the visible ticket IDs against the two seeded tickets.
- **Expected:** Both tickets are listed — `GET /tickets` applies no assignee scoping for support
  (`ticket_handler.go` List only bars `customer`). The "Create Ticket" button **is** visible
  (support may create) and every row offers Edit, but not Delete.
- **Known issue:** The row-level Edit affordance is granted to all support users
  (`TicketList.tsx:90` sets `canEdit = canCreate`), while the API only permits editing one's own
  assignments — see TC-TICK-023.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

---

## 5.2 Create Ticket

### TC-TICK-007 — Create a ticket end to end with a customer selected
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test from
  `generateCustomerData()`; ticket payload from `generateTicketData()`.
- **Steps:**
  1. Navigate to `/tickets` and click "Create Ticket".
  2. Fill `input[name="subject"]` and `textarea[name="description"]`.
  3. Pick a Priority from the "Priority" select.
  4. Open the "Customer" Autocomplete, type the seeded company name and pick the option.
  5. Click "Create Ticket" (`button[type="submit"]`).
- **Expected:** `POST /api/v1/tickets` is sent with `{title, description, status, priority,
  customer_id, assigned_to_id}` (the frontend renames `subject` → `title`,
  `endpoints/tickets.ts:80`) and returns **201**. A "Ticket created successfully" snackbar appears
  and the app navigates to `/tickets`, where the new subject is present in the table.
- **Known issue:** The 201 body has `customer: {"id":0,...}` — the handler responds with the
  in-memory struct and never re-reads with the `Customer` preload, and `json:"customer,omitempty"`
  on a non-pointer struct never omits. Harmless here only because the list is refetched.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; the existing
  "admin can create a new ticket successfully" never selects a customer and asserts only that the
  URL contains `/tickets`)

### TC-TICK-008 — Reject a create with missing required fields
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tickets/new`.
  2. Click "Create Ticket" without filling anything.
- **Expected:** No network request is made. The zod resolver
  (`TicketForm.tsx:24-31`) surfaces "Subject is required", "Description is required" and
  "Customer is required" (the last as the Customer field's helper text). The URL stays
  `/tickets/new`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin sees validation errors for invalid ticket data"

### TC-TICK-009 — Accept a very long description
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a test-created customer; `faker.lorem.paragraphs(10)` as the
  description.
- **Steps:**
  1. Navigate to `/tickets/new`, fill subject and the long description, select the customer.
  2. Submit.
- **Expected:** 201. `Description` is `type:text` in the schema
  (`internal/models/ticket.go:21`) and no length validation exists on either side, so the whole
  body round-trips. The detail page renders it with `white-space: pre-wrap`, preserving the
  paragraph breaks.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; the existing
  "admin can handle ticket with long description" ends in `expect(true).toBe(true)` and never
  verifies the save)

### TC-TICK-010 — The Status select on the create form is ignored
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a test-created customer.
- **Steps:**
  1. Navigate to `/tickets/new`; fill subject, description and customer.
  2. Set Status to "Resolved".
  3. Submit and open the new ticket's detail page.
- **Expected:** The request body does contain `"status":"resolved"`, but the created ticket comes
  back and renders as **Open**. `CreateTicketRequest` (`ticket_handler.go:28`) has no `Status`
  field, and the handler hard-codes `Status: models.TicketStatusOpen`.
- **Known issue:** The form offers a control that cannot take effect; the only way to reach a
  non-open status is a follow-up edit. Not recorded in FEATURES.md row 5.2.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-011 — Leaving the agent picker empty assigns the ticket to the creator
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a test-created customer.
- **Steps:**
  1. Create a ticket, leaving "Assign to Support Agent (Optional)" untouched.
  2. Open the new ticket's detail page.
- **Expected:** "Assigned To" shows the logged-in admin's first and last name. The handler
  substitutes the caller when `assigned_to_id` is absent (`ticket_handler.go` Create,
  `if ticket.AssignedToID == nil { ticket.AssignedToID = &currentUserID }`), so a ticket is never
  created unassigned through this form.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-012 — Create a ticket from a customer's detail page with the customer preselected
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test.
- **Steps:**
  1. Open `/customers/<id>` and click "Create Ticket" (also reachable from the customer list's row
     menu).
  2. Observe the Customer field.
  3. Fill subject and description and submit.
- **Expected:** The URL is `/tickets/new?customer_id=<id>`; `customer_id` is prefilled from the
  query string and the Autocomplete is **disabled** (`TicketForm.tsx:224`). The visible label may
  be blank until the customers query resolves. Submitting posts the correct `customer_id` and
  returns 201.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-013 — Rejected assignee role produces a 400 and a generic snackbar
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a test-created customer; a **sales** user created by the test
  (so a non-support, non-admin id is available).
- **Steps:**
  1. Open `/tickets/new` and fill subject, description and customer.
  2. In "Assign to Support Agent (Optional)", pick the sales user (the picker is unfiltered — see
     the Known issue).
  3. Submit.
- **Expected:** `POST /tickets` returns **400** with message
  "tickets can only be assigned to support or admin users: tickets can only be assigned to support
  or admin users" — the handler responds with `err.Error()` and the service wraps
  `apperrors.ErrInvalidAssigneeRole` in `fmt.Errorf` with the sentinel's own text, so the phrase is
  doubled (`ticket_service.go` Create, `ticket_handler.go` Create). The UI shows only the generic
  "Failed to create ticket"
  snackbar — `TicketForm.tsx:96` discards the server message — and stays on `/tickets/new`.
- **Known issue:** The picker is labelled "Support Agent" and queries
  `usersApi.getUsers({ role: 'support', is_active: true })`, but `UserHandler.List` reads only
  `page`, `limit`, `search`, `sort_by`, `sort_order` — `role` and `is_active` are ignored, so the
  dropdown lists the first 20 users of *any* role, customers included.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-014 — Support user can create a ticket but has an empty agent picker
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user logged in; a customer that already exists (created by the admin
  half of the spec).
- **Steps:**
  1. Navigate to `/tickets`; confirm "Create Ticket" is visible; click it.
  2. Observe the Customer and the "Assign to Support Agent (Optional)" pickers.
  3. Fill subject, description, select a customer, submit.
- **Expected:** `/tickets/new` renders — `ProtectedRoute requiredRole={['admin','support']}`
  admits support. The Customer picker is populated (`GET /customers` has no role guard). The agent
  picker is **empty**: `GET /users` is `RequireRole(admin)` (`routes.go:13`) and returns 403 for
  support. Submitting still yields **201** and the ticket is assigned to the creating support user
  per TC-TICK-011.
- **Known issue:** A support user cannot hand a new ticket to a colleague through the UI; the
  failing `GET /users` produces no visible error.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-015 — Sales user is blocked from the create form and from the API
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user logged in.
- **Steps:**
  1. Navigate directly to `/tickets/new`.
  2. Separately, issue `POST /api/v1/tickets` with the sales user's bearer token via
     `request.post()`.
- **Expected:** The SPA redirects to `/unauthorized` (`ProtectedRoute.tsx:35`,
  `<Navigate to="/unauthorized" replace />`) and the form never mounts. The direct API call returns
  **403** with "Only support and admin users can create tickets" (`ticket_handler.go` Create); note
  the handler rejects *before* binding the body, so a malformed payload also yields 403, not 400.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-016 — Customer-role user is blocked from the create form
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer-role user logged in.
- **Steps:**
  1. Navigate directly to `/tickets/new`.
  2. Issue `POST /api/v1/tickets` with that user's token.
- **Expected:** Redirect to `/unauthorized`; the API returns **403** "Only support and admin users
  can create tickets".
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-017 — Cancelling the create form discards the draft
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open `/tickets/new`, type a subject.
  2. Click "Cancel".
- **Expected:** Navigation to `/tickets` with no `POST` issued; the typed subject does not appear in
  the table.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin can handle ticket form cancellation"

---

## 5.3 Edit Ticket

### TC-TICK-018 — Admin edits subject and priority
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by the test.
- **Steps:**
  1. Open `/tickets/<id>/edit` (or the row's edit icon).
  2. Clear and refill Subject; set Priority to "High".
  3. Submit.
- **Expected:** `PUT /api/v1/tickets/<id>` sends only the changed-plus-echoed fields as
  `{title, description, status, priority, assigned_to_id}` and returns **200**. A "Ticket updated
  successfully" snackbar appears and the app navigates to `/tickets` with the new subject and a
  "High" priority chip on the row.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin can edit an existing ticket" and "admin can update ticket priority"

### TC-TICK-019 — Walk a ticket through open → in_progress → resolved
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by the test in status `open`.
- **Steps:**
  1. Edit the ticket, set Status to "In Progress", submit.
  2. Edit again, set Status to "Resolved", submit.
  3. Open the detail page.
- **Expected:** Both `PUT`s return 200. There is no forward-transition state machine — any of
  `open|in_progress|resolved|closed` is accepted by the binding tag
  (`UpdateTicketRequest.Status`, `oneof=open in_progress resolved closed`) and only the
  reopen-from-closed rule in `ticket_service.go` Update constrains it. The detail header chip reads
  "Resolved" with the success colour.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin can update ticket status"

### TC-TICK-020 — A closed ticket cannot be reopened
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; a ticket created by the test and then set to "Closed".
- **Steps:**
  1. Open `/tickets/<id>/edit`.
  2. Set Status to "Open" and submit.
- **Expected:** `PUT /tickets/<id>` returns **400** with message "cannot reopen closed ticket:
  cannot reopen closed ticket" — the handler passes `err.Error()` through, and the service wraps
  `apperrors.ErrClosedTicketReopen` in `fmt.Errorf` with the sentinel's own text, doubling the
  phrase (`ticket_service.go` Update, `ticket_handler.go` Update). The UI shows the
  generic "Failed to update ticket" snackbar, stays on the edit form, and the ticket remains
  Closed after a reload.
- **Known issue:** The specific backend message is dropped by the mutation's `onError`
  (`TicketForm.tsx:111`), so the user is not told *why*.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-021 — The Customer field is blank when the edit form loads
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a ticket created by the test with a customer selected.
- **Steps:**
  1. Open `/tickets/<id>/edit`.
  2. Read the "Customer" Autocomplete's input value.
- **Expected:** Subject, Description, Status and Priority are prefilled correctly, but the Customer
  input renders **empty** even though `customer_id` is set in the form state and the GET payload
  contains the customer. Submitting without touching it still preserves the association
  (`customer_id` is carried in the form values, and the backend ignores `customer_id` on update
  anyway).
- **Known issue:** `TicketForm.tsx:125` seeds `selectedCustomer` from `ticket.customer`, the raw
  backend object, while `getOptionLabel` (line 214) reads `option.company_name` — a field only the
  customers endpoint's transform creates. Same `company` vs `company_name` mismatch as TC-TICK-002.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-022 — Reassign a ticket to another support agent
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; two support users created by the test; a ticket assigned to
  the first.
- **Steps:**
  1. Open `/tickets/<id>/edit`.
  2. In the agent Autocomplete pick the second support user.
  3. Submit, then open the detail page.
- **Expected:** `PUT` carries the new `assigned_to_id`; 200. `ticket_service.go` Update re-validates
  the assignee (must exist and be `support` or `admin`). "Assigned To" on the detail page shows the
  second agent.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-023 — Clearing the agent does not unassign the ticket
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a ticket created by the test and assigned to a support user.
- **Steps:**
  1. Open `/tickets/<id>/edit`.
  2. Clear the "Assign to Support Agent (Optional)" Autocomplete (the clear "x").
  3. Submit and reopen the detail page.
- **Expected:** The `PUT` body **omits** `assigned_to_id` entirely, the API returns 200, and the
  detail page still shows the original assignee. Unassigning is not achievable through the UI, and
  not through the API either.
- **Known issue:** `endpoints/tickets.ts:95` only forwards `assigned_to_id` when it is not
  `undefined`, and clearing the picker sets it to `undefined`; on the backend
  `UpdateTicketRequest.AssignedToID` is a `*uint`, so an explicit `null` also decodes to `nil` and
  is skipped by `if req.AssignedToID != nil`. `TicketForm.test.tsx` "unassigns ticket by clearing
  agent" asserts the mutation is *called* with `assigned_to_id: undefined`, which is exactly the
  value that gets dropped — the unit test passes while the feature does not work.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-024 — Support user may edit only their own assignment
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user logged in; ticket A assigned to that user and ticket B assigned
  to a different support user (both seeded as admin).
- **Steps:**
  1. From `/tickets`, click the row Edit icon on ticket A, change the priority, submit.
  2. Return to `/tickets`, click the row Edit icon on ticket B, change the priority, submit.
  3. Also open `/tickets/<B>` and inspect the header buttons.
- **Expected:** Ticket A: `PUT` returns 200 and the app navigates back to `/tickets`. Ticket B: the
  row-level Edit icon **is** offered and `/tickets/<B>/edit` renders (the route guard only checks
  the role), but the `PUT` returns **403** "You can only update tickets assigned to you"
  (`ticket_handler.go` Update); the UI shows "Failed to update ticket" and stays on the form. On
  `/tickets/<B>` neither Edit nor Delete is rendered (`TicketDetail.tsx:125-130`) — but see
  TC-TICK-027, that page never finishes loading for this user anyway.
- **Known issue:** The list-level affordance (`TicketList.tsx:90`) is coarser than the API rule;
  only the detail page mirrors it.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-025 — Sales and customer users cannot update a ticket at either layer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user and a customer-role user, each created by the test; one existing
  ticket id.
- **Steps:**
  1. As sales, navigate to `/tickets/<id>/edit`.
  2. As sales, `PUT /api/v1/tickets/<id>` directly with a valid body.
  3. Repeat both as the customer-role user.
- **Expected:** Both roles are redirected to `/unauthorized` by
  `ProtectedRoute requiredRole={['admin','support']}`. The direct `PUT` returns **403** with
  "Sales users cannot update tickets" for sales and "Customers cannot update tickets" for the
  customer role; both checks run before the body is bound and before the ticket is looked up, so a
  non-existent id still yields 403 rather than 404.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

---

## Ticket Detail

### TC-TICK-026 — Detail page shows "Customer: N/A" and mislabels the contact as "Created By"
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a ticket created by the test with a known customer whose
  contact name and company name are both known from `generateCustomerData()`.
- **Steps:**
  1. Open `/tickets/<id>`.
  2. Read the "Customer", "Created By" and "Assigned To" blocks.
- **Expected:** "Customer" reads `N/A`. "Created By" shows the **customer's contact name**
  (`first_name last_name`), not the user who created the ticket. "Assigned To" is correct.
  `docs/screenshots/tickets/03-detail.png` captures exactly this state: `Customer: N/A` beside
  `Created By: Gregg Nader`.
- **Known issue:** Two separate defects. (a) `TicketDetail.tsx:183` reads
  `ticket.customer?.company_name`, which the backend never sends — it sends `company`
  (`internal/models/customer.go:9`); the association *is* preloaded
  (`ticket_repository.go` `GetByID` does `Preload("Customer")`), so the screenshot's `N/A` is a
  field-name bug, not a missing join. (b) There is no creator column on `models.Ticket` at all, so
  `transformTicketFromBackend` (`endpoints/tickets.ts:51`) sets `created_by = backendTicket.customer`
  with the comment "Assuming customer created the ticket". Neither is recorded in FEATURES.md
  row 5.1/5.3.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-027 — Detail page spins forever when the ticket is out of scope or missing
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A support user logged in; a ticket assigned to a *different* support user. Also
  a known-nonexistent id (e.g. `999999`).
- **Steps:**
  1. Navigate to `/tickets/<other-users-ticket-id>`.
  2. Navigate to `/tickets/999999`.
- **Expected:** Case 1: `GET /tickets/<id>` returns **403** "You can only view tickets assigned to
  you" (`ticket_handler.go` Get). Case 2: **404** "Ticket not found". In both cases the page shows
  the loading spinner indefinitely and never renders an error: `TicketDetail.tsx:118` short-circuits
  on `isLoading || !ticket`, and a failed query leaves `ticket` undefined forever. Assert the
  spinner is still present after the query has settled and that no ticket heading appears.
- **Known issue:** No 403/404 surface on the detail route; the same trap applies to a customer-role
  user opening a ticket that belongs to another customer.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-028 — The comments panel is decorative and posting fails
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a ticket created by the test.
- **Steps:**
  1. Open `/tickets/<id>` and scroll to "Comments".
  2. Type text into "Add a comment..." and click "Add Comment".
  3. Reload the page.
- **Expected:** The panel always reads "No comments yet" —
  `transformTicketFromBackend` hard-codes `comments: []`. Clicking "Add Comment" issues
  `POST /api/v1/tickets/<id>/comments`, which is **not registered** in `SetupTicketRoutes`, so Gin
  returns **404**; the UI shows the "Failed to add comment" snackbar and the textarea keeps its
  content. After a reload the comment is gone. The button is disabled while the field is empty.
- **Known issue:** There is no comment model, table, handler or route. Per the repo's standing
  directive the endpoint function in `endpoints/tickets.ts` is intended contract awaiting a
  backend, not dead code — the UI must not be deleted. Not listed in FEATURES.md section 5.
- **Automation:** blocked — the negative half is testable, but "comments persist" cannot be covered
  until the backend route exists; track the 404 assertion under
  `gocrm-ui/e2e/tests/admin-tickets.spec.ts`

### TC-TICK-029 — Detail-page action buttons match the caller's role
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** One ticket assigned to support user S. Admin, S, a second support user S2, a
  sales user and a customer-role user all available.
- **Steps:**
  1. Open `/tickets/<id>` as each role in turn.
  2. Record whether the "Edit" button and the red delete `IconButton` are present.
- **Expected:** admin → Edit + Delete. S → Edit only. S2 → neither (and the page never leaves the
  spinner, per TC-TICK-027). sales → neither. customer-role → neither, and the request 403s unless
  the ticket belongs to their own customer record. The rule is
  `canEdit = admin || (support && assigned_to_id === user.id)`, `canDelete = admin`
  (`TicketDetail.tsx:125-130`), mirrored by `TicketDetail.test.tsx` (6 unit tests).
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

---

## 5.4 Delete Ticket

### TC-TICK-030 — Admin deletes a ticket the test created
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by this test (never an inherited row).
- **Steps:**
  1. Open `/tickets/<id>`; click the red delete icon.
  2. Confirm the dialog titled "Delete Ticket" ("Are you sure you want to delete ticket #<id>? This
     action cannot be undone." / "Delete").
  3. Return to `/tickets` and search for the ticket's subject.
- **Expected:** `DELETE /api/v1/tickets/<id>` returns **204 No Content**. A "Ticket deleted
  successfully" snackbar appears, the app navigates to `/tickets`, and the row is gone. This is an
  ordinary GORM soft delete (`ticket_repository.go` `Delete`) — no field is overwritten, unlike the
  GDPR erasure applied to users, customers and leads. A subsequent `GET /tickets/<id>` returns 404.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` "admin can delete a ticket"
  (row-menu variant; it accepts 200 or 204 and asserts nothing about the row afterwards — extend it
  to assert the disappearance)

### TC-TICK-031 — Cancelling the delete dialog leaves the ticket intact
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by the test.
- **Steps:**
  1. Open the delete confirmation from the list row or the detail page.
  2. Click "Cancel".
  3. Reload `/tickets`.
- **Expected:** The dialog closes, no `DELETE` request is issued, and the ticket is still listed.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; the screenshot spec
  `gocrm-ui/e2e/screenshots/05-tickets.spec.ts` "05 - delete confirmation" only captures the dialog)

### TC-TICK-032 — Only admins can delete, at both layers
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user, a sales user and a customer-role user; one ticket id, with the
  support user assigned to it.
- **Steps:**
  1. As each non-admin role, load `/tickets` and `/tickets/<id>` and look for delete controls.
  2. As each, issue `DELETE /api/v1/tickets/<id>` directly.
- **Expected:** No delete icon anywhere for any non-admin (`canDelete = user?.role === 'admin'` in
  both `TicketList.tsx:91` and `TicketDetail.tsx:130`). Every direct `DELETE` returns **403**
  "Only administrators can delete tickets" — including for the support user the ticket is assigned
  to, and including for a non-existent id, because the role check precedes the id parse
  (`ticket_handler.go` Delete). The ticket still exists afterwards.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

---

## 5.5 Filter by Status / Priority (page-local controls)

### TC-TICK-033 — The Status filter does not change the visible rows
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; at least one ticket in status `open` and one in `resolved`,
  both created by the test and both on the first page (set `limit` high enough, or search first).
- **Steps:**
  1. Navigate to `/tickets`.
  2. Open the "Status" select and choose "Resolved".
  3. Capture the outgoing request and compare the row set before and after.
- **Expected:** A new request goes out as `GET /tickets?page=1&limit=10&status=resolved&priority=&search=`
  and the row set is **identical** — the open ticket is still listed. `TicketHandler.List` reads only
  `page`, `offset`, `limit`, `sort_by`, `sort_order` and `search`; `status` is silently discarded,
  and `TicketList.tsx` renders `data?.data` with no local `.filter()`, so nothing is filtered
  client-side either. Verified live against `localhost:8090`: `GET /tickets?limit=100&status=closed`
  returns all 24 rows with `total: 24`.
- **Known issue:** FEATURES.md row 5.5 and gap **G34** describe this as "client-side over the current
  page". That is inaccurate for tickets — there is no client-side filtering at all; the control is
  inert. The row and the gap entry both need correcting.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; the existing
  "admin can filter tickets by status" only asserts `count >= 0`, which passes either way)

### TC-TICK-034 — The Priority filter does not change the visible rows
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; tickets of at least two different priorities created by the
  test.
- **Steps:**
  1. Navigate to `/tickets`; choose "Urgent" in the "Priority" select.
  2. Compare the row set before and after; then choose "All Priorities".
- **Expected:** `priority=urgent` is sent and ignored; the rows are unchanged, and non-urgent
  tickets remain visible. Resetting to "All Priorities" sends `priority=` and again changes nothing.
  Both selects do reset `page` to 1, which is observable in the request.
- **Known issue:** Same root cause as TC-TICK-033 (FEATURES.md row 5.5, gap G34).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; the existing
  "admin can filter tickets by priority" asserts `count >= 0`)

---

## 5.6 Search Tickets

### TC-TICK-035 — Search narrows the list by title
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by the test whose subject contains a unique
  token (e.g. `faker.string.alphanumeric(10)` appended to the generated subject).
- **Steps:**
  1. Navigate to `/tickets`.
  2. Type the unique token into "Search tickets...".
- **Expected:** `GET /tickets?...&search=<token>` returns exactly one row, and `data.total` is 1.
  Search is genuinely server-side: `ticket_repository.go` `Search` matches
  `title LIKE ? OR description LIKE ? OR resolution LIKE ?` and `CountSearch` recounts, so the
  pagination footer agrees with the row count. Search also takes precedence over the plain sorted
  listing in the handler.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended; "admin can search
  tickets" searches the literal string "ticket" and asserts `count >= 0`)

### TC-TICK-036 — Search matches the description as well as the title
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a ticket created by the test whose *description* contains a
  unique token that does not appear in its subject.
- **Steps:**
  1. Search for the description-only token.
- **Expected:** The ticket is returned — the `LIKE` covers `description` and `resolution` too. The
  Subject column will not visibly contain the search term, which is correct.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

### TC-TICK-037 — A search with no matches empties the table without an error
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Search for a random token that cannot match (`faker.string.uuid()`).
- **Expected:** 200 with `{"tickets": [], "total": 0}`; the table shows the header row only and the
  pagination footer reads 0. No error message, no spinner. Clearing the box restores the full list.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (extended)

---

## 5.7 Sort Tickets

### TC-TICK-038 — Sorting by Subject, Status, Priority and Created works
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least three tickets with distinguishable subjects.
- **Steps:**
  1. Navigate to `/tickets`; click the "Subject" column header, then click it again.
  2. Repeat for "Status", "Priority", "Created" and "Last Updated".
- **Expected:** The first click issues `sort_by=title&sort_order=asc` (the UI maps `subject` →
  `title`, `TicketList.tsx:211-219`), the second `sort_order=desc`, and the rows reorder accordingly.
  `created_at`, `updated_at`, `title`, `status` and `priority` are the whole allowlist in
  `TicketHandler.List`; the repository routes them through `utils.SafeOrderClause`. Sorting also
  resets to page 1.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (new describe block; FEATURES
  row 5.7 records "No E2E test")

### TC-TICK-039 — Sorting by Ticket #, Customer or Assigned To silently does nothing
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; at least three tickets whose id order differs from their
  default order.
- **Steps:**
  1. Click the "Ticket #" column header; record the row order.
  2. Click "Customer", then "Assigned To".
- **Expected:** The requests carry `sort_by=id`, `sort_by=customer` and `sort_by=assigned_to`
  respectively, and in every case the returned order is unchanged: none of those columns is in the
  handler's allowlist, so `sortBy` is blanked and the plain `List` path runs. The header still
  renders its sort arrow, so the UI claims a sort that did not happen.
- **Known issue:** Not recorded in FEATURES.md row 5.7, which only notes the missing E2E test.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tickets.spec.ts` (new describe block)

---

## 5.8 My Tickets

### TC-TICK-040 — GET /tickets/my has no UI surface
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A support user logged in with at least one ticket assigned to them and one
  assigned to somebody else.
- **Steps:**
  1. Inspect the left navigation and the `/tickets` page for any "My Tickets" entry, tab or toggle.
  2. Call `GET /api/v1/tickets/my` directly with the support user's token.
- **Expected:** No UI element anywhere routes to `/tickets/my` — a repo-wide search of
  `gocrm-ui/src` for `tickets/my`, `My Tickets` and `myTickets` returns nothing, and
  `endpoints/tickets.ts` has no wrapper for it. The direct API call returns **200** with only the
  ticket assigned to the caller. A sales user also gets 200 but always an empty list (tickets can
  only be assigned to support/admin); a customer-role user gets **403** "Customers cannot have
  tickets assigned to them".
- **Known issue:** Gap **G18** in `docs/FEATURES.md` ("No E2E test for 'My Tasks' / 'My Tickets'
  views") understates it — for tickets there is no view to test.
- **Automation:** blocked — the API half is coverable from a Playwright `request` context, but the
  UI half cannot exist until a page or nav entry is built

---

## 5.9 Tickets by Customer

### TC-TICK-041 — A customer-role user may read only their own customer's tickets
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two customer records created by the test, each linked to a different
  customer-role user via `customers.user_id`; at least one ticket per customer. The first
  customer-role user logged in. Note the linkage cannot be provisioned through the API or UI:
  `CreateCustomerRequest` has no `user_id` field, and the `Assign` route sets `assigned_to_id`
  (the staff account manager), not `user_id` — the rows must be linked directly in the database,
  as the handler tests do (see also TC-CUST-041).
- **Steps:**
  1. Call `GET /api/v1/customers/<own-customer-id>/tickets`.
  2. Call `GET /api/v1/customers/<other-customer-id>/tickets`.
- **Expected:** Step 1 returns **200** with only that customer's tickets and a matching `total`.
  Step 2 returns **403** "Customers can only view their own tickets"
  (`ticket_handler.go` `ListByCustomer`, which resolves the caller's customer record via
  `customerService.GetByUserID` and compares ids). A lookup failure — including a customer-role user
  with no linked customer record — also yields 403, never a 500.
- **Known issue:** This was gap **G27**, an IDOR with no ownership check at all; it is fixed and
  pinned by four handler tests, but FEATURES.md row 5.9 still records "no E2E test".
- **Automation:** blocked — needs a way to link a customer-role user to a customer record
  (`customers.user_id` is not settable through any UI or public endpoint today, exactly as in
  TC-CUST-041); once that exists the case is API-level via `request.newContext()` in
  `gocrm-ui/e2e/tests/tickets-rbac.spec.ts`

### TC-TICK-042 — Staff roles may read any customer's tickets
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Admin, sales and support users; a customer with at least one ticket.
- **Steps:**
  1. Call `GET /api/v1/customers/<id>/tickets` as admin, then as sales, then as support.
- **Expected:** **200** for all three — the ownership branch in `ListByCustomer` applies only to the
  `customer` role, and there is no `RequireRole` on the route (`routes.go:61`). Support in particular
  can read tickets that are not assigned to it through this route, even though
  `GET /tickets/:id` would 403 for the same rows.
- **Known issue:** The per-assignment scoping on `GET /tickets/:id` is not mirrored on
  `GET /customers/:id/tickets`; the two read paths disagree for the support role.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-rbac.spec.ts` (new)

### TC-TICK-043 — The customer detail "Tickets" tab lists every ticket, not the customer's
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; two customers created by the test, each with a ticket created
  by the test.
- **Steps:**
  1. Open `/customers/<A>` and switch to the Tickets tab.
  2. Look for customer B's ticket in the list.
- **Expected:** Customer B's ticket **is** listed under customer A. The page calls
  `ticketsApi.getTickets({ customer_id })` (`CustomerDetail.tsx:80-82`), i.e.
  `GET /tickets?customer_id=<id>` — and `customer_id` is not among the query parameters
  `TicketHandler.List` reads, so it is ignored and every ticket comes back. The purpose-built
  `GET /customers/:id/tickets` route is never called by the frontend.
- **Known issue:** Same class of defect as the inert status/priority filters (TC-TICK-033). Not
  recorded against row 5.9, which describes the endpoint rather than its (absent) consumer.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (extended)

---

## POST /tickets/bulk/status

`SetupBulkStatusRoutes` registers this route with no `RequireRole` guard; all authorization lives in
`BulkHandler.BulkUpdateTicketStatus` and `bulkOperationService.BulkSetTicketStatus`. No UI calls it —
`ticketsApi.bulkUpdateStatus` exists in `endpoints/tickets.ts` but no page imports it, and
`DataTable.tsx:292` still carries a `// TODO: Implement bulk delete functionality`. These cases are
therefore API-level, driven from a Playwright `request` context with a UI verification pass afterwards.

### TC-TICK-044 — Admin sets one status across several tickets
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; three tickets created by the test, all `open`.
- **Steps:**
  1. `POST /api/v1/tickets/bulk/status` with `{"ticket_ids":[a,b,c],"status":"in_progress"}`.
  2. Reload `/tickets` and read the Status chips of the three rows.
- **Expected:** **200** with a `BulkStatusUpdateResult` reporting three updated ids. All three rows
  show "In Progress". The write is one all-or-nothing transaction
  (`runBulkStatusUpdate`), and duplicate ids in the payload are de-duplicated by
  `normalizeBulkStatusIDs`.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-bulk-status.spec.ts` (new)

### TC-TICK-045 — Sales and customer roles are refused before the body is read
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user and a customer-role user; two valid ticket ids.
- **Steps:**
  1. As sales, POST a valid payload; then POST an intentionally malformed body (`{}`).
  2. Repeat as the customer-role user.
- **Expected:** **403** in all four calls — "Sales users cannot update tickets" and "Customers
  cannot update tickets" respectively. The malformed body still yields 403, not 400: the handler
  checks the role before `ShouldBindJSON` so an unauthorized caller learns nothing about payload
  shape (`bulk_handler.go:447-457`). No ticket changes status.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-bulk-status.spec.ts` (new)

### TC-TICK-046 — Support bulk update names the tickets it may not touch
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user; ticket A assigned to them, ticket B assigned to someone else,
  both `open`.
- **Steps:**
  1. As the support user, POST `{"ticket_ids":[A,B],"status":"resolved"}`.
  2. Re-read both tickets.
- **Expected:** **403** "You can only update tickets assigned to you", with `error.details`
  naming B's id. **Neither** ticket changes — the whole batch rolls back, so A is still `open`.
  Posting only `[A]` afterwards returns 200.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-bulk-status.spec.ts` (new)

### TC-TICK-047 — A closed ticket in the batch aborts the whole update
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; ticket A `open`, ticket B `closed` (both created by the test).
- **Steps:**
  1. POST `{"ticket_ids":[A,B],"status":"open"}`.
  2. Re-read both tickets.
  3. POST `{"ticket_ids":[A,B],"status":"closed"}`.
- **Expected:** Step 1 returns **400** with `error.details.closed_ids` containing B, and A is
  untouched — the bulk route must not be a way around the single-record reopen ban
  (`bulk_status_service.go:238-252`). Step 3 succeeds (200): moving a closed ticket *to* closed is
  not a reopen.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-bulk-status.spec.ts` (new)

### TC-TICK-048 — Payload limits and unknown ids are rejected
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in; one valid ticket id; one id known not to exist.
- **Steps:**
  1. POST `{"ticket_ids":[],"status":"open"}`.
  2. POST 101 ids.
  3. POST `{"ticket_ids":[valid],"status":"archived"}`.
  4. POST `{"ticket_ids":[valid, 999999],"status":"resolved"}`.
- **Expected:** Steps 1–3 return **400** from the binding tags
  (`ticket_ids` is `required,min=1,max=100,dive,gt=0`; `status` is
  `required,oneof=open in_progress resolved closed`). Step 4 returns **404** with the missing id
  listed in `error.details`, and the valid ticket is *not* updated.
- **Automation:** planned — `gocrm-ui/e2e/tests/tickets-bulk-status.spec.ts` (new)
