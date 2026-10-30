# Customer Management — Test Cases

Playwright end-to-end test cases for the customer area: the list page, the create/edit form, the
detail page with its Tickets and History tabs, deletion (which is a GDPR Article 17 erasure, not a
recoverable soft delete), search, sort, pagination, and the two 2026-08 endpoints that have no UI
yet — CSV export and assignment. Every **Expected** below states what the build does *today*; where
that is defective or surprising the case still asserts the current behaviour and a **Known issue**
line records it, so the document stays truthful in the same way the OpenAPI annotations do.

A large share of the surprises in this area come from `gocrm-ui/src/api/endpoints/customers.ts`,
whose `transformCustomerFromBackend()` fabricates fields the backend does not have. Cases that
depend on that are flagged.

**Sources**

- `gocrm-ui/src/pages/customers/CustomerList.tsx`, `CustomerForm.tsx`, `CustomerDetail.tsx`
- `gocrm-ui/src/api/endpoints/customers.ts`, `endpoints/tickets.ts`, `src/api/client.ts`
- `gocrm-ui/src/components/DataTable.tsx`, `src/routes/index.tsx`, `src/layouts/MainLayout.tsx`
- `internal/handler/customer_handler.go`, `internal/handler/ticket_handler.go` (`ListByCustomer`,
  `List`), `internal/handler/routes.go` (`SetupCustomerRoutes`, `SetupTicketRoutes`)
- `internal/models/customer.go`
- `gocrm-ui/e2e/tests/admin-customers.spec.ts`, `gocrm-ui/e2e/pages/customers.page.ts`,
  `gocrm-ui/e2e/helpers/admin-auth.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`,
  `gocrm-ui/e2e/screenshots/04-customers.spec.ts`
- `docs/FEATURES.md` rows 4.1–4.7, row 5.9, section 10b "Customers", section 12; `docs/ROADMAP.md`

**Constraints**

- All customer records must be created by the test itself using `generateCustomerData()` from
  `gocrm-ui/e2e/fixtures/admin-user.ts`. Deleting a customer is irreversible erasure and cascades to
  the lead it was converted from, so no case may delete a record it did not create.
- Only `AdminAuthHelper` exists in `gocrm-ui/e2e/helpers/`. Sales and support sessions require a user
  created through the admin Users page (`POST /users`) plus a new role-login helper; those cases are
  marked accordingly. A customer-role session can be obtained from the public `/auth/register` flow,
  which always mints a `customer`.
- `/customers/export` and `/customers/{id}/assign` have no UI control anywhere in the frontend, so
  the cases for them are API-level and need a request-context (`request.newContext()`) helper rather
  than page interaction.

---

## 4.1 View Customers List

### TC-CUST-001 — Load the customers list as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in (`test-admin@gocrm.test`).
- **Steps:**
  1. Navigate to `/customers`.
  2. Wait for the `GET /api/v1/customers` response.
- **Expected:** 200. The `h4` heading reads "Customers", the "Add Customer" button is visible, and a
  `table` renders with the columns Company, Primary Contact, Email, Phone, Total Revenue, Status,
  Customer Since, Actions. The request carries `page=1&limit=10&search=`; the response body is
  `{success:true,data:{customers:[…],total:N},meta:{…}}`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can view customers list page"

### TC-CUST-002 — Total Revenue is a client-side constant; there is no Status column
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin logged in; at least one customer exists.
- **Steps:**
  1. Navigate to `/customers`.
  2. Read the column headers and the "Total Revenue" cell of every visible row.
  3. Inspect the `GET /api/v1/customers` response body.
- **Expected:** There is no "Status" column and no Active/Inactive chip (removed in #118: customers
  have no activation concept). Every Total Revenue cell reads `$0` regardless of the data; the
  response body contains no `total_revenue` field.
- **Known issue:** `transformCustomerFromBackend()` in `gocrm-ui/src/api/endpoints/customers.ts`
  still hardcodes `total_revenue: 0`; `models.Customer` (`internal/models/customer.go`) has no such
  column. It no longer invents `is_active`, `industry`, `annual_revenue` or `employee_count`.
- **Automation:** automated (Vitest, status half) — `CustomerList.test.tsx` "has no status column";
  the revenue half is planned in `gocrm-ui/e2e/tests/admin-customers.spec.ts`

### TC-CUST-003 — Sales user can read the customers list
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user created via the admin Users page; logged in as that user.
- **Steps:**
  1. Click "Customers" in the sidebar.
  2. Wait for `GET /api/v1/customers`.
- **Expected:** The nav item is present (`MainLayout.tsx` lists `roles: ['admin','sales','support']`),
  the request returns 200 and the same unfiltered set an admin sees — `CustomerHandler.List` applies
  no ownership filter for sales.
- **Automation:** blocked — needs a role-login helper; only `AdminAuthHelper` exists

### TC-CUST-004 — Support user reads the list but its Edit and Delete icons fail
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user created via the admin Users page; logged in as that user; at
  least one customer created by the test as admin beforehand.
- **Steps:**
  1. Navigate to `/customers`.
  2. Observe the Actions column of the first row.
  3. Click the Edit (pencil) icon, change the Company Name, click "Update Customer".
  4. Return to `/customers`, click the Delete (bin) icon on the same row and confirm.
- **Expected:** Step 1–2: 200, and the view / edit / delete icon buttons all render — `DataTable`
  renders them whenever `onEdit`/`onDelete` are supplied and `CustomerList.tsx:201-202` supplies them
  unconditionally, with no role check. Step 3: `PUT /api/v1/customers/{id}` returns 403 with
  `error.message` "Insufficient permissions to update customers"; the page stays on the edit form and
  an error snackbar reads "Failed to update customer". Step 4: `DELETE /api/v1/customers/{id}` returns
  403 "Only administrators can delete customers"; the snackbar reads "Failed to delete customer" and
  the row remains.
- **Known issue:** Frontend gating and backend gating disagree. The backend is correct; the UI offers
  three actions a support user cannot perform and reports the refusal with a generic message that does
  not mention permissions.
- **Automation:** blocked — needs a role-login helper

### TC-CUST-005 — Customer-role user reaches /customers by deep link and sees an empty table
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer-role account registered through `/register` (the public endpoint
  always creates a `customer`); logged in as that user.
- **Steps:**
  1. Confirm the sidebar has no "Customers" item.
  2. Navigate directly to `/customers`.
  3. Observe the `GET /api/v1/customers` response and the rendered page.
- **Expected:** The nav item is hidden. The deep link still renders the page — `src/routes/index.tsx`
  wraps `customers*` in the bare `ProtectedRoute` with **no** `requiredRole`, so there is no redirect
  to `/unauthorized`. `GET /api/v1/customers` returns **403** with "Insufficient permissions to list
  customers". The page shows the "Customers" heading (no "Add Customer" button, which is admin and
  sales only since #120) and a table with zero rows; no error message is displayed, because `CustomerList` never reads the query's `error` state
  and the axios interceptor only acts on 401.
- **Known issue:** A forbidden page renders as an empty-but-normal page. Compare `/users` and the
  ticket forms (`/tickets/new`, `/tickets/:id/edit`), which are role-gated in the router, as are
  `/leads/*` and `/customers/new` since #120 (see TC-LEAD-004 and TC-CUST-013). The customer list,
  detail and edit routes stay open on purpose: the API scopes them itself.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new, or a dedicated
  `customer-role-access.spec.ts`)

---

## 4.2 Create Customer

### TC-CUST-006 — Create a customer with the full form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in. Data from `generateCustomerData()`.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Fill Company (as entered), Website, Primary Contact Name, Email, Phone, Street Address, City,
     State/Province, Country, Postal Code, Notes.
  3. Click "Create Customer".
- **Expected:** `POST /api/v1/customers` returns **201**; a success snackbar reads "Customer created
  successfully" and the app navigates to `/customers`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can create a new customer successfully"

### TC-CUST-007 — Create a customer with only the required fields
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Fill only Company Name, Primary Contact Name (two words), Email, Phone.
  3. Click "Create Customer".
- **Expected:** 201. The zod schema (`CustomerForm.tsx:21-37`) requires company_name, contact_name,
  email and phone; everything else is optional. Note that phone is required by the *frontend* only —
  `CreateCustomerRequest` marks only first_name, last_name and email as `binding:"required"`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can create customer with minimal required data"

### TC-CUST-008 — Submitting an empty create form is blocked client-side
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Click "Create Customer" with every field empty.
- **Expected:** No HTTP request is made. Field errors appear: "Company name is required", "Contact
  name is required", "Invalid email address", "Phone number is required". The URL stays
  `/customers/new`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin sees validation errors for invalid customer data"

### TC-CUST-009 — A malformed Website value is rejected client-side
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Fill the required fields; type `example.com` (no scheme) into Website.
  3. Click "Create Customer".
- **Expected:** No request is made; the Website field shows "Website must be an http(s) URL".
  Empty values are allowed; values over 255 characters are rejected. Both customer creation and
  lead conversion apply these rules, and the API independently validates them.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-010 — Duplicate email on create returns 409 but the UI shows a generic message
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; one customer already created by the test with a known email.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Fill fresh `generateCustomerData()` values but reuse the existing customer's email.
  3. Click "Create Customer".
- **Expected:** `POST /api/v1/customers` returns **409** with
  `error.message` "customer with this email already exists" (no driver text — see
  `customer_handler.go` `Create`). The UI stays on `/customers/new` and shows an error snackbar
  reading exactly **"Failed to create customer"**.
- **Known issue:** `CustomerForm.tsx:82-84` discards the server message and always renders the same
  generic string, so the user is not told the email is the problem. FEATURES.md row 4.2 records only
  the backend half ("Duplicate email now returns 409"). The existing E2E assertion passes on its URL
  branch, not on the message branch.
- **Automation:** automated (partially) — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can handle duplicate customer email validation"; extend it to pin the 409 status and the exact snackbar text

### TC-CUST-011 — A single-word contact name is rejected by the backend as a missing last name
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Fill Company Name, Email, Phone, and set Primary Contact Name to a single word ("Cher").
  3. Click "Create Customer".
- **Expected:** The client splits the name on spaces (`customers.ts:64`), so it sends
  `first_name:"Cher", last_name:""`. `POST /api/v1/customers` returns **400** (binding failure on the
  required `last_name`) and the snackbar reads "Failed to create customer". The form stays on
  `/customers/new` with no field-level error.
- **Known issue:** A valid mononym cannot be entered; the failure surfaces as an unexplained generic
  error because the frontend models one `contact_name` while the backend models `first_name` +
  `last_name`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-012 — The form offers only fields the API stores
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Look for Industry, Number of Employees, Annual Revenue and an "Active Customer" switch.
  3. Fill every field shown and click "Create Customer"; inspect the `POST /api/v1/customers`
     request body.
  4. Open the new customer's edit page: every saved field is loaded. Clear Website, Street
     Address, City, State/Province, Country, Postal Code and Notes, save while inspecting the
     `PUT /api/v1/customers/{id}` request body, and reload the edit page.
- **Expected:** None of the four controls exists (removed in #118; industry and employee range are
  on the linked Company, and customers have no activation concept). The create body contains
  `first_name, last_name, email, phone, company, website, address, city, state, country,
  postal_code, notes` (and `company_id` when one is linked). Step 4 *submission*: the PUT body
  carries each cleared field as an empty string and the API answers 200. Step 4 *persistence*:
  after the reload only Website is empty; Street Address, City, State/Province, Country, Postal
  Code and Notes come back with their previous values, because `CustomerHandler.Update` treats an
  empty string as "unchanged" for every field except `website` (TC-CUST-018).
- **Known issue:** Clearing an optional customer field other than Website does not persist — a
  backend limitation recorded in `docs/ROADMAP.md` ("Customer update cannot clear optional text
  fields"). The backend also owns a `Position` field that the form never exposes. Before #118 the
  four controls accepted input that every save discarded.
- **Automation:** submission half automated (Vitest) — `CustomerForm.test.tsx` "CustomerForm
  retained fields" asserts the create and update payloads and the edit-form reload from a stored
  record, with a mocked API; nothing automated asserts persistence. The browser half (including
  the persistence check) is planned in `gocrm-ui/e2e/tests/admin-customers.spec.ts`

### TC-CUST-012a — Website persists through both creation paths and can be cleared
- **Priority:** P1
- **Type:** regression
- **Steps:** Create a customer with a website, reopen it, change the website, reopen again, then
  clear it and reload. Convert a qualified lead with a website and reload the resulting customer.
- **Expected:** Detail links and edit fields retain the saved website. Clearing it removes the link.
  Conversion navigates to the newly created customer's ID. Unrelated API updates preserve websites.
- **Automation:** `gocrm-ui/e2e/tests/customer-website.spec.ts`; API regression coverage in
  `tests/customer_website_integration_test.go` also checks validation, export and erasure.

### TC-CUST-013 — Support user cannot create a customer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Support user logged in.
- **Steps:**
  1. Navigate to `/customers` and look for "Add Customer".
  2. Navigate directly to `/customers/new`.
  3. With the support user's token, `POST /api/v1/customers` with valid `generateCustomerData()`
     values.
- **Expected:** Step 1: no "Add Customer" button. Step 2: redirect to `/unauthorized` ("Access
  Denied") — since #120 the route sits under an admin+sales `ProtectedRoute`. Step 3: the
  route-level `middleware.RequireRole(admin, sales)` answers **403**.
- **Automation:** automated (Vitest) — `src/routes/index.test.tsx` "lead and customer creation
  routes" and `CustomerList.rowActions.test.tsx` "shows Add Customer to …"; the browser case is
  blocked on a role-login helper

### TC-CUST-014 — Sales user can create a customer
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Sales user logged in.
- **Steps:**
  1. `/customers` → "Add Customer", fill valid data, submit.
- **Expected:** 201; navigation to `/customers`; the new record appears in the list. Sales is on the
  allowlist in both `routes.go` and the handler's own check.
- **Automation:** blocked — needs a role-login helper

### TC-CUST-015 — Cancelling the create form discards input
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers` → "Add Customer".
  2. Type a company name, click "Cancel".
- **Expected:** Navigation to `/customers` with no HTTP write; the typed value is not persisted.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can handle customer form cancellation"

---

## 4.3 Edit Customer

### TC-CUST-016 — Edit a customer's company name
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test.
- **Steps:**
  1. `/customers`, click the Edit icon on the created row.
  2. Wait for the form to load with the existing values.
  3. Clear "Company Name", type a new value, click "Update Customer".
- **Expected:** `PUT /api/v1/customers/{id}` returns **200**; the snackbar reads "Customer updated
  successfully"; the app navigates to `/customers` and the row shows the new company name.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can edit an existing customer"

### TC-CUST-017 — Duplicate email on update returns 409
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; two customers created by the test, A and B.
- **Steps:**
  1. Open B's edit form.
  2. Replace B's email with A's email.
  3. Click "Update Customer".
- **Expected:** `PUT /api/v1/customers/{B}` returns **409** with "customer with this email already
  exists". The page stays on `/customers/{B}/edit` and the snackbar reads "Failed to update
  customer". B's stored email is unchanged.
- **Known issue:** As with create, the 409 message is replaced by a generic string
  (`CustomerForm.tsx:96-98`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-018 — Clearing an optional field on edit does not clear it server-side
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a customer created by the test with Notes and City populated.
- **Steps:**
  1. Open the customer's edit form.
  2. Clear the "Notes" field and the "City" field entirely.
  3. Click "Update Customer", then reopen the record.
- **Expected:** The form sends `notes: ""` and `city: ""` and the API answers 200, but the original
  Notes and City are still present after the reload. `CustomerHandler.Update` applies each text
  field only `if req.X != ""`, so an empty string is read as "leave unchanged". The UI gives no
  indication that the clearing was ignored. Website is the exception: it is a pointer in the
  request, so clearing it does persist (TC-CUST-012a), and `company_id: 0` clears the company link.
- **Known issue:** Address, city, state, country, postal code and notes cannot be blanked through
  the API or the UI; website and the company link can. The behaviour is documented in the
  handler's swagger `@Description` ("empty fields leave the stored value unchanged"), listed as a
  gap in FEATURES.md row 4.3 and recorded as a defect in `docs/ROADMAP.md` ("Customer update
  cannot clear optional text fields"). TC-CUST-012 shows the same split for the full form.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-019 — Support user cannot update a customer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Support user logged in; a customer created by the test as admin.
- **Steps:**
  1. Navigate directly to `/customers/{id}/edit`.
  2. Change a field and submit.
- **Expected:** The edit form loads (the `GET /customers/{id}` read is permitted for support), but
  `PUT /api/v1/customers/{id}` returns **403** "Insufficient permissions to update customers". The
  route itself carries no `RequireRole`; the guard is inside `CustomerHandler.Update`.
- **Automation:** blocked — needs a role-login helper

---

## 4.4 View Customer Detail

### TC-CUST-020 — Open a customer detail page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test.
- **Steps:**
  1. `/customers`, click the row (or the eye icon).
- **Expected:** URL matches `/customers/{id}`; `GET /api/v1/customers/{id}` returns 200; the header
  shows the company name with a status chip, and the buttons "Create Ticket", "Edit" and a red bin
  icon. Three tabs render: Details, Tickets, History.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can view customer details"

### TC-CUST-021 — Detail page shows only stored business information
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin logged in; a customer created by the test with a website.
- **Steps:**
  1. Open the customer's detail page, Details tab.
  2. Read the heading and the Contact Information and Business Information blocks.
- **Expected:** No Active/Inactive chip next to the heading and no Industry, Annual Revenue or
  Employees entries (removed in #118). Business Information shows Total Revenue "$0" and Customer
  Since. The Website row displays the saved URL as a link; it is absent when the saved website is
  empty.
- **Known issue:** Total Revenue is still a client-side constant (TC-CUST-002).
- **Automation:** automated (Vitest) — `CustomerDetail.test.tsx` "CustomerDetail fields"; browser
  case planned in `gocrm-ui/e2e/tests/admin-customers.spec.ts`

### TC-CUST-022 — History tab renders two synthetic entries
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin logged in; a customer created by the test.
- **Steps:**
  1. Open the customer's detail page and select the "History" tab.
  2. Observe the entries and check the network tab.
- **Expected:** Exactly two list items: "Customer account created — System — {created_at}" and
  "Customer information updated — Unknown — {updated_at}". No activity request is issued.
- **Known issue:** The list is a hardcoded literal in `CustomerDetail.tsx:101-116`; there is no
  activity store. The second entry always attributes to "Unknown" because it reads
  `customer.owner?.email` and `models.Customer` has no `owner` field.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

---

## 4.4 / 12.x Delete Customer (erasure)

### TC-CUST-023 — Admin deletes a customer and the row disappears
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by this test (never a pre-existing record).
- **Steps:**
  1. `/customers`, search for the created company name so the row is unambiguous.
  2. Click the Delete icon on that row.
  3. Read the confirmation dialog and click "Delete".
- **Expected:** The dialog title is "Delete Customer" and the body reads `Are you sure you want to
  delete the customer "<company>"? This action cannot be undone.`
  `DELETE /api/v1/customers/{id}` returns **204** (no body). A success snackbar reads "Customer
  deleted successfully", the customers query is invalidated and the row is gone after the refetch.
  A subsequent `GET /api/v1/customers/{id}` returns 404.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can delete a customer" (it deletes the row matching its own customer's email and asserts that row disappears)

### TC-CUST-024 — An erased customer's email can be used again
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Create a customer with `generateCustomerData()`; record the email.
  2. Delete that customer and confirm.
  3. Create a new customer reusing exactly the same email address.
- **Expected:** Step 2 returns 204. Step 3 returns **201**, not 409 — deletion overwrites the stored
  address with a random one in the reserved `.invalid` domain before soft-deleting the row
  (`internal/repository/erasure.go`), so the original address is free. This is the user-visible proof
  that deletion is erasure and not retention.
- **Known issue:** Not a defect, but note that the erasure is irreversible; the reversible
  alternative (`is_active = false`) does not exist for customers at all — the model has no such
  column.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-025 — Deleting a converted customer also erases the originating lead
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Create a lead with `generateLeadData()`; note its company name and email.
  2. Convert the lead to a customer from the lead detail page.
  3. Delete the resulting customer from `/customers` and confirm.
  4. Go to `/leads` and search for the original lead company name and email.
- **Expected:** 204 on the delete. The lead no longer appears in `/leads`, and its email address is
  free for reuse — the cascade runs in the same transaction via `leads.customer_id`
  (`internal/repository/erasure_cascade.go`; the cascade lives in the plain customer repository).
  FEATURES.md 12.6 covers this at the integration level only.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new; may need to live in a
  cross-entity spec alongside `admin-entity-suite.spec.ts`)

### TC-CUST-042 — Deleting a customer erases its customer login
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a customer whose `user_id` names a `customer`-role account
  (set by import or SQL — no screen writes `user_id`), the login holding an API key; a second
  customer whose `user_id` names a sales account.
- **Steps:**
  1. Delete the first customer from `/customers`.
  2. Try the login's API key against `GET /api/v1/tickets`; look for the login in `/users`.
  3. Delete the second customer; look for the sales account in `/users`.
- **Expected:** Step 1 → 204. Step 2 → 401, and the login is gone from `/users`: it is the same
  person, so it is erased in the same transaction (names, email, password, API keys, refresh and
  reset tokens), and so is every other customer record linked to it (#108). Step 3 → 204, and the
  sales account is untouched: a staff account in `user_id` is never followed, and neither is
  `assigned_to_id`.
- **Automation:** planned — needs a fixture that links a login to a customer. Go coverage:
  `internal/repository/identity_erasure_test.go` (`customer_root_erases_the_whole_graph`,
  `staff_login_link_is_never_followed`); `erasure_pii_sweep_test.go`
  `TestErasingACustomerWithALoginLeavesNoPersonalDataInAnyTable`.

### TC-CUST-026 — Sales user cannot delete a customer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user logged in; a customer created by the test.
- **Steps:**
  1. `/customers`, click the Delete icon on the row, confirm the dialog.
- **Expected:** `DELETE /api/v1/customers/{id}` returns **403** — the route carries
  `middleware.RequireRole(models.RoleAdmin)` and `CustomerHandler.Delete` repeats the check with
  "Only administrators can delete customers". The UI shows the error snackbar "Failed to delete
  customer"; the confirm dialog stays open (`setDeleteDialog({open:false})` runs only on success) and
  the row remains in the list.
- **Known issue:** The delete control is offered to a role that cannot use it, and the failed dialog
  is not dismissed.
- **Automation:** blocked — needs a role-login helper

---

## 4.5 Search Customers

### TC-CUST-027 — Search narrows the list server-side
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test with a distinctive company name.
- **Steps:**
  1. `/customers`.
  2. Type the distinctive company name into "Search customers...".
  3. Wait for the `GET /api/v1/customers` request carrying `search=<term>&page=1`.
- **Expected:** 200. The request is genuinely server-side (`CustomerHandler.List` routes a non-empty
  `search` to `customerService.Search`, which covers first_name, last_name, email, company, phone and
  notes). Only matching rows render and the pagination total reflects the filtered count. The page
  index resets to 1.
- **Automation:** automated (partially) — `gocrm-ui/e2e/tests/admin-customers.spec.ts` "admin can search customers"; that test performs the search but asserts nothing — extend it to assert the row count and the request parameters

### TC-CUST-028 — Searching for a term with no matches yields an empty table
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers`, type a random 20-character string into the search box.
- **Expected:** 200 with `total: 0`; the table body renders zero rows and the pagination footer reads
  "0-0 of 0". There is no "no results" empty state — `DataTable` renders an empty `TableBody`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-029 — Search fires one request per keystroke
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers`, record network requests.
  2. Type a 6-character term into the search box at normal speed.
- **Expected:** Six `GET /api/v1/customers` requests, one per character — `handleSearch` sets state
  directly with no debounce and the search value is part of the TanStack Query key
  (`CustomerList.tsx:48-51, 142-144`).
- **Known issue:** Undebounced search against a route covered by `RateLimitModerate` (120/min,
  burst 30); a fast typist on a long term can approach the limit. Compare the roadmap note on rate
  limiting having no integration coverage (FEATURES.md 11.2 / G22).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

---

## 4.6 Sort Customers

### TC-CUST-030 — Sorting by Company and Email round-trips to the server
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least three customers exist.
- **Steps:**
  1. `/customers`.
  2. Click the "Company" column header, then click it again.
  3. Click the "Email" column header.
- **Expected:** Each click issues `GET /api/v1/customers` with `sort_by=company` (then `sort_order`
  toggling `asc` → `desc`) and `sort_by=email`, plus `page=1`. `CustomerList.handleSort` maps
  `company_name → company`, `contact_name → first_name`, `email → email`, `created_at → created_at`;
  all four are on the handler's `customerSortColumns` allowlist, so the ordering actually changes.
- **Known issue:** FEATURES.md 4.6 is **partial** — no E2E test exists (the sort-E2E gap family,
  G21).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new), mirroring
  `leads-sorting-search.spec.ts`

### TC-CUST-031 — Phone sorts nothing, Total Revenue is not sortable, there is no Status column
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; several customers with different phone numbers.
- **Steps:**
  1. `/customers` and read the column headers.
  2. Click the "Phone" column header; note the request and the row order.
  3. Try to click the "Total Revenue" header.
- **Expected:** The headers are Company, Primary Contact, Email, Phone, Total Revenue and Customer
  Since; there is no Status column (removed in #118). Step 2 sends `sort_by=phone`, which is not in
  the handler's `customerSortColumns` (`created_at, updated_at, first_name, last_name, email,
  company`), so the handler blanks `sort_by` and returns the unsorted list with 200: the row order
  does not change while the `TableSortLabel` arrow flips as if it had. Step 3: Total Revenue is
  plain text with no sort control (`sortable: false` in `CustomerList.tsx`), so no request is sent.
- **Known issue:** The Phone header looks sortable but is inert. Dropping the unknown column is
  the deliberate SQL-injection guard, so the fix is either `sortable: false` on Phone or `phone`
  in the handler allowlist (`utils.AllowedSortColumns` already lists it for customers). Recorded
  in `docs/ROADMAP.md` ("Sortable columns the backend has no mapping for").
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

---

## 4.7 Pagination

### TC-CUST-032 — Page through the customer list
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; more than 10 customers exist (the default page size).
- **Steps:**
  1. `/customers`.
  2. Click the "next page" arrow in the table footer.
  3. Click "previous page".
- **Expected:** Step 2 issues `GET /api/v1/customers?page=2&limit=10`; the handler converts page to
  `offset = (page-1)*limit` and returns the second slice with `meta.page: 2`. The rows differ from
  page 1 and the footer reads "11-20 of N". Step 3 returns to page 1.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-033 — Changing rows-per-page resets to the first page
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more than 25 customers exist.
- **Steps:**
  1. `/customers`, advance to page 2.
  2. Change "Rows per page" to 25.
- **Expected:** `GET /api/v1/customers?page=1&limit=25` — `handleRowsPerPageChange` resets
  `page: 1`. 200; 25 rows render. A request with `limit` above 100 would be capped by
  `utils.ParseOffsetLimit`, and `limit=0` is coerced away rather than producing a 500 (FEATURES.md
  11.5 / G28 regression).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

---

## 10b — CSV Export (`GET /customers/export`)

### TC-CUST-034 — The customers page offers no export control
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `/customers`.
  2. Look for an "Export" / "Download CSV" button in the header, the toolbar and the row menu.
- **Expected:** No such control exists anywhere on the page. `customersApi.exportCustomers` is
  implemented in `gocrm-ui/src/api/endpoints/customers.ts:115-127` but has no caller — the frontend
  API surface is the intended contract, and the missing piece here is the UI, not the backend.
- **Known issue:** FEATURES.md 10b lists the endpoint as delivered; the UI half is outstanding.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new; assert the absence
  today, invert the assertion when the button ships)

### TC-CUST-035 — Admin can download the customer CSV
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin token obtained through the login flow; a customer created by the test.
- **Steps:**
  1. Issue `GET /api/v1/customers/export` with the admin bearer token from a Playwright
     `request.newContext()`.
- **Expected:** **200**. Headers `Content-Type: text/csv; charset=utf-8` and
  `Content-Disposition: attachment; filename=customers-export.csv`. The body is raw CSV, **not** the
  `{success,data}` envelope. First line is exactly
  `id,first_name,last_name,email,phone,company,address,notes,assigned_to_id,created_at,updated_at,website`.
  The created customer appears as a row with an empty `assigned_to_id` cell and RFC3339 timestamps.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new, API-level); the
  header, row rendering, quoting and formula prefix are pinned at the API level by
  `tests/customer_export_csv_test.go` (`TestCustomerExportCSV{SQLite,MySQL}`)

### TC-CUST-036 — Export is admin-only and ignores pagination
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Tokens for a sales user, a support user and a customer-role user; more than one
  page of customers exists.
- **Steps:**
  1. `GET /api/v1/customers/export` with the sales token.
  2. Repeat with the support token, then with the customer-role token.
  3. Repeat with the admin token and `?limit=1&page=1` appended; count the data rows.
- **Expected:** All three calls in steps 1–2 return **403** — the route carries
  `middleware.RequireRole(models.RoleAdmin)` and `CustomerHandler.Export` repeats the check with
  "Only administrators can export customers". Note that sales and support *can* read
  `GET /customers`; export is deliberately narrower because it egresses the whole customer base at
  once. Step 3 returns 200 and **every** non-erased customer, not one — the export path reads no
  pagination parameters, only `search`, `sort_by` and `sort_order`. Soft-deleted (erased) customers
  are absent.
- **Automation:** blocked in Playwright — needs sales/support tokens, i.e. a role-login helper; the
  customer-role and admin halves can be automated today. The 403/401 contract for every non-admin
  role is asserted against the real routes by `tests/customer_export_csv_test.go`

### TC-CUST-036a — A sorted export of a large book is complete and deterministic
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin token; more than 1,000 live customers whose sort values run against id
  order and repeat (several customers per company, several created in the same second), a few of
  them erased, and a `search` term that still matches more than 500 of them.
- **Steps:**
  1. `GET /api/v1/customers/export?sort_by=company&sort_order=desc`; parse the body as CSV.
  2. Repeat for every allowed `sort_by` (`created_at`, `updated_at`, `first_name`, `last_name`,
     `email`, `company`) in both directions, with and without `search`, and once with no `sort_by`.
  3. Repeat one request and compare the two bodies.
- **Expected:** Every request returns 200 and every matching live customer exactly once — the row
  count equals the matching count and no id repeats. Rows are ordered by the requested column and
  then by id, both in the requested direction, so duplicate sort values always come out in the same
  order and `desc` is the exact reverse of `asc`; with no `sort_by` the order is id ascending.
  Erased customers are absent, the search still narrows the file, and two identical requests return
  identical bodies. The same holds on SQLite and MySQL.
- **Known issue (fixed, #105):** `ListAllForExport` used to apply the requested `ORDER BY` on top of
  GORM's `FindInBatches`, whose continuation is `id > last id of the previous batch`. Under any
  non-id ordering that cursor skipped every customer whose id was below the previous batch's last
  id, so a sorted export of more than 500 rows silently lost rows while looking complete. The
  repository now reads the rows with one ordered statement.
- **Automation:** automated (API-level) — `internal/repository/customer_export_traversal_test.go`
  (`TestCustomerExportTraversal{SQLite,MySQL}`: the repository against an adversarial fixture whose
  duplicate values straddle the old batch boundaries, including a NULL and an empty company) and
  `tests/customer_export_csv_test.go` (`TestCustomerExportCSV{SQLite,MySQL}`: the real routes,
  JWT middleware and handler, parsed with `encoding/csv`; also pins quoting of commas, quotes and
  newlines, the apostrophe prefix on formula-like cells, authorization and the JSON error envelope
  when the read fails before the response starts). The MySQL roots create and drop a disposable
  schema on `TEST_MYSQL_DSN`; they skip without the variable and fail when it is unreachable.

---

## 10b — Assign Customer (`POST /customers/{id}/assign`)

### TC-CUST-037 — No UI exposes customer assignment
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a customer created by the test.
- **Steps:**
  1. Open the customer's detail page and its row menu on `/customers`.
  2. Open the edit form.
- **Expected:** No "Assign", "Owner" or "Account manager" control exists in any of the three places.
  `customersApi.assignCustomer` exists (`customers.ts:110-113`, posting `{ user_id }`, which matches
  `handler.AssignCustomerRequest`) but has no caller, and `CustomerForm` has no assignee field.
- **Known issue:** As with export, the backend shipped in the 2026-08 build-out and the UI did not.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new; assert absence today)

### TC-CUST-038 — Assigning to an admin or sales user succeeds
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin token; a customer created by the test; a sales user created by the test.
- **Steps:**
  1. `POST /api/v1/customers/{id}/assign` with body `{"user_id": <sales user id>}`.
  2. `GET /api/v1/customers/{id}`.
  3. `GET /api/v1/customers/export` and locate the row.
- **Expected:** Step 1 returns **200** with the updated customer. Step 2 shows
  `assigned_to_id: <sales user id>`. Step 3 shows that id in the `assigned_to_id` CSV column.
  `AssignedToID` is a foreign key to a staff account, not personal data, so it deliberately survives
  erasure of the customer.
- **Automation:** blocked — needs a helper that creates a sales user and reads back its id

### TC-CUST-039 — Assignment rejects support, customer, inactive and unknown assignees
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin token; a customer created by the test; a support user, a customer-role
  user, and a deactivated sales user created by the test.
- **Steps:**
  1. Assign the customer to the support user.
  2. Assign it to the customer-role user.
  3. Assign it to the deactivated sales user.
  4. Assign it to a user id that does not exist.
  5. Assign a customer id that does not exist to a valid sales user.
- **Expected:** 1 and 2 → **400** "Customers can only be assigned to sales or admin users".
  3 → **400** "Cannot assign a customer to a deactivated user". 4 → **404** "User not found"
  (the `ErrAssigneeNotFound` sentinel is matched before the customer's). 5 → **404** "Customer not
  found". The distinction is deliberate: 404 means the account was not found at all, 400 means a real
  account was refused on its merits.
- **Automation:** blocked — needs role/user-provisioning helpers

---

## 5.9 — Tickets by Customer

### TC-CUST-040 — The Tickets tab lists every ticket in the system, not the customer's
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; two customers created by the test, A and B; at least one ticket
  created against B and none against A.
- **Steps:**
  1. Open customer A's detail page.
  2. Select the "Tickets" tab.
  3. Inspect the request the tab issues.
- **Expected:** The tab shows tickets that belong to **B** (and to every other customer), up to the
  default page size of 20. The request is `GET /api/v1/tickets?customer_id={A}` — not
  `GET /api/v1/customers/{A}/tickets` — and `TicketHandler.List` never reads `customer_id`, so the
  parameter is silently discarded and the full ticket list comes back with 200.
- **Known issue:** A customer-scoped panel headed "Support Tickets" shows other customers' ticket
  subjects. The correctly scoped route exists and is ownership-checked
  (`TicketHandler.ListByCustomer`), but `CustomerDetail.tsx:79-83` calls
  `ticketsApi.getTickets({ customer_id })` instead. Related to the G27 IDOR that was fixed on the
  other route; FEATURES.md 5.9 records "still no E2E test" but not this frontend mismatch.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-customers.spec.ts` (new)

### TC-CUST-041 — A customer user cannot read another customer's tickets
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer-role account whose `customers.user_id` points at customer record X;
  a second customer record Y with at least one ticket, both created by the test.
- **Steps:**
  1. Log in as the customer-role user and obtain its token.
  2. `GET /api/v1/customers/{Y}/tickets`.
  3. `GET /api/v1/customers/{X}/tickets`.
- **Expected:** Step 2 → **403** "Customers can only view their own tickets"
  (`ticket_handler.go` `ListByCustomer` resolves the caller's own customer record via
  `customerService.GetByUserID` and compares ids; a lookup failure is also 403, never a leak).
  Step 3 → **200** with that customer's own tickets and a `{tickets, total}` payload.
- **Known issue:** This route was the G27 IDOR (any authenticated user could read any customer's
  tickets). It is regression-tested at the handler and integration level only; there is still no E2E
  coverage, and no UI path reaches it.
- **Automation:** blocked — needs a helper that links a registered customer-role user to a customer
  record (`customers.user_id` is not settable through any UI or public endpoint today); the API-level
  403 half can be automated once that link exists

### TC-CUST-042 — Create a ticket for a customer from its row menu
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Admin (or support) logged in; three customers created by the test.
- **Steps:**
  1. `/customers`, sort by Company.
  2. Open "More actions for <second customer>" (by mouse, or focus it and press Enter) and choose
     "Create Ticket".
  3. Repeat step 1 as a sales user and look for any "More actions" button.
- **Expected:** Step 2 opens `/tickets/new?customer_id=<that customer's id>` with the customer
  preselected; the click does not open the customer's detail page. The menu holds only "Create
  Ticket": view, edit and delete are the row's own icons. Step 3: no menu at all, because
  `POST /tickets` is admin and support only. Before #114 the only menu button sat in the table
  toolbar and never opened.
- **Automation:** automated (Vitest) — `CustomerList.rowActions.test.tsx`; the browser case is
  planned in `gocrm-ui/e2e/tests/admin-customers.spec.ts` (admin half)
