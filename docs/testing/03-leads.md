# Lead Management — Test Cases

Playwright E2E test-case catalog for the Leads area: the list page, create/edit forms, the detail
page, deletion (which is GDPR erasure), search, sorting, the status and classification filters,
lead-to-customer conversion, pagination, and the bulk status endpoint. Every **Expected** below
states what the application does *today*; where that behaviour is defective or surprising a
**Known issue** line says so and points at the source. The whole `/leads` API group is gated to
admin + sales, so several cases exist purely to pin the difference between the backend guard and
the frontend gating.

**Sources**

- `docs/FEATURES.md` section 3 (rows 3.1–3.11) and section 10 (bulk status intro), Gap Summary
  G16, G17, G21, G34.
- Frontend: `gocrm-ui/src/pages/leads/LeadList.tsx`, `LeadForm.tsx`, `LeadDetail.tsx`;
  `gocrm-ui/src/components/DataTable.tsx`, `ConvertLeadDialog.tsx`, `ConfirmDialog.tsx`,
  `ProtectedRoute.tsx`; `gocrm-ui/src/layouts/MainLayout.tsx`; `gocrm-ui/src/routes/index.tsx`;
  `gocrm-ui/src/api/endpoints/leads.ts`; `gocrm-ui/src/api/client.ts`;
  `gocrm-ui/src/contexts/ConfigurationContext.tsx`.
- Backend: `internal/handler/lead_handler.go`, `internal/handler/routes.go`,
  `internal/handler/bulk_handler.go` (`BulkUpdateLeadStatus`, `BulkLeadStatusRequest`),
  `internal/service/lead_service.go`, `internal/service/bulk_status_service.go`,
  `internal/repository/lead_repository.go`, `internal/repository/erasure_cascade.go`,
  `internal/middleware/auth.go` (`RequireRole`), `internal/middleware/error_handler.go`,
  `internal/utils/response.go`, `internal/models/lead.go`, `internal/models/customer.go`,
  `cmd/main.go`.
- Existing specs: `gocrm-ui/e2e/tests/admin-leads.spec.ts`,
  `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts`,
  `gocrm-ui/e2e/tests/admin-entity-suite.spec.ts`, `gocrm-ui/e2e/screenshots/03-leads.spec.ts`,
  `gocrm-ui/e2e/pages/leads.page.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`.
- Visual reference: `docs/screenshots/leads/01-list.png` … `06-search.png`
  (indexed by `docs/SCREENSHOTS.md`).

**Constraints**

- Test data must come from `generateLeadData()` in `gocrm-ui/e2e/fixtures/admin-user.ts`; never
  hardcode an email address.
- **Deleting a lead is irreversible erasure** (`internal/repository/erasure_cascade.go`), and it
  cascades to the customer the lead was converted into. Only ever delete records the test itself
  created.
- Non-admin roles need accounts the suite cannot self-serve: `POST /auth/register` always creates a
  `customer` (`auth_handler.go`), so sales and support accounts must come from an admin
  `POST /users` call. There is no non-admin login helper — `gocrm-ui/e2e/helpers/admin-auth.ts` is
  the only one — which is why several RBAC cases below are marked blocked.
- The admin seed account is created by `gocrm-ui/e2e/global-setup.ts`.

---

## 3.1 View leads list

### TC-LEAD-001 — Load the leads list as admin and see rows
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one lead exists (create one with
  `generateLeadData()` if the database is empty).
- **Steps:**
  1. Navigate to `/leads`.
  2. Wait for the `h4` "Leads" heading and the table.
- **Expected:** `GET /api/v1/leads?page=1&limit=10` returns 200. The heading "Leads", the
  "Add Lead" button and the table render; `table tbody tr` count is greater than 0. The response
  envelope is `{success, data:{leads,total}, meta:{page,per_page,total,total_pages}}` and the axios
  interceptor unwraps it to `data`.
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should load leads
  page with data"

### TC-LEAD-002 — Verify the list column set and the two chip columns
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; one lead created via `generateLeadData()`.
- **Steps:**
  1. Navigate to `/leads`.
  2. Read the header cells.
  3. Read the Status and Classification cells of the created lead's row.
- **Expected:** Headers, in order: Company, Contact, Email, Phone, Status, Classification, Source,
  Created, plus an unlabelled actions column. Status renders as a filled MUI Chip with the value
  capitalised; Classification renders as an outlined Chip, and a lead created through the UI shows
  "Unclassified" because `LeadForm.tsx` never sends `classification` and the column defaults to
  `unclassified` (`models/lead.go`). Created is formatted `MMM dd, yyyy`. Each row carries
  Visibility / Edit / Delete icon buttons (`DataTable.tsx`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (extended)

### TC-LEAD-003 — A sales user sees only the leads they own
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user created via admin `POST /users`; at least one lead owned by that
  sales user and at least one owned by the admin.
- **Steps:**
  1. Log in as the sales user.
  2. Navigate to `/leads`.
  3. Compare the visible rows against the leads owned by the sales user.
- **Expected:** 200. Only leads whose `owner_id` is the sales user appear — `lead_handler.go:221`
  routes sales users to `leadService.GetByOwner()`. The search box, the classification dropdown and
  the column sort headers still send their parameters, but the sales branch is taken before any of
  them are consulted, so they have no effect at all for this role.
- **Known issue:** For a sales user, search / classification / sort are silently ignored rather
  than rejected (`lead_handler.go:221-239`); the swagger description on `List` documents this.
- **Automation:** blocked — needs a sales role-login helper; only
  `gocrm-ui/e2e/helpers/admin-auth.ts` exists

### TC-LEAD-004 — Support user: Leads is hidden in the nav but the route is not gated
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user created via admin `POST /users`.
- **Steps:**
  1. Log in as the support user.
  2. Inspect the left navigation drawer.
  3. Navigate directly to `/leads` by URL.
- **Expected:** The "Leads" nav entry is absent — `MainLayout.tsx` restricts it to
  `roles: ['admin','sales']`. Direct navigation nevertheless renders the Leads page: the `leads`
  route in `gocrm-ui/src/routes/index.tsx` has **no** `ProtectedRoute requiredRole`, so there is no
  redirect to `/unauthorized`. `GET /api/v1/leads` returns **403**
  `{"success":false,"error":{"code":"FORBIDDEN","message":"Insufficient permissions"}}` from
  `RequireRole(admin, sales)` (`routes.go:24`, `middleware/auth.go:64`). The page shows the
  heading, the "Add Lead" button and an empty table body; no error banner is displayed.
- **Known issue:** Frontend gating and backend gating disagree — the nav hides Leads but the router
  does not, so an unauthorised role reaches a functional-looking page whose every call 403s.
  Compare with the ticket forms (`/tickets/new`, `/tickets/:id/edit`) and `/users`, which *are*
  wrapped in `ProtectedRoute requiredRole` — the `/tickets` list route itself is as open as
  `/leads`.
- **Automation:** blocked — needs a support role-login helper; only
  `gocrm-ui/e2e/helpers/admin-auth.ts` exists

### TC-LEAD-005 — Customer user is refused the leads API
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer account registered through `POST /auth/register` (the public
  endpoint always assigns the `customer` role).
- **Steps:**
  1. Log in as the customer.
  2. Confirm "Leads" is absent from the navigation.
  3. Navigate directly to `/leads`.
- **Expected:** Same as TC-LEAD-004 — nav entry hidden, route reachable, `GET /api/v1/leads`
  returns 403 "Insufficient permissions", table renders empty. The `!isAdminOrSales` branch at
  `lead_handler.go:224` is unreachable through the router.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new customer-role block;
  the account can be created through `registration.spec.ts`'s flow)

---

## 3.2 Create lead

### TC-LEAD-006 — Admin creates a lead with the required fields
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in. Data from `generateLeadData()`.
- **Steps:**
  1. Navigate to `/leads`, click "Add Lead".
  2. Fill Company Name, Contact Name, Email, Phone; pick a Lead Source and a Status.
  3. Click "Create Lead".
- **Expected:** `POST /api/v1/leads` returns **201**. The payload is transformed by
  `endpoints/leads.ts`: `contact_name` is split on the first space into `first_name`/`last_name`,
  `company_name` becomes `company`, and `owner_id` is set to the logged-in admin's own id
  (`LeadForm.tsx:173`). A "Lead created successfully" snackbar shows and the browser navigates
  to `/leads`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can create a new lead
  successfully"

### TC-LEAD-007 — Admin creates a lead with notes and verifies it on the detail page
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in. Data from `generateLeadData()` including `notes`.
- **Steps:**
  1. Create the lead through the form, filling the Notes textarea.
  2. Read the new id from the 201 response body (`data.id`).
  3. Navigate to `/leads/{id}`.
- **Expected:** 201; the detail page shows the company name as the `h4`, and the Notes block is
  rendered (it is conditional on `lead.notes` being non-empty).
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can create lead with
  all optional fields"

### TC-LEAD-008 — Empty form submission is blocked client-side
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/leads/new`.
  2. Click "Create Lead" without filling anything.
- **Expected:** No HTTP request is made — the zod resolver in `LeadForm.tsx` rejects the submit.
  The URL stays `/leads/new` and inline errors appear: "Company name is required", "Contact name is
  required", "Invalid email address", "Phone number is required", "Lead source is required".
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin sees validation errors
  for invalid lead data"

### TC-LEAD-009 — Malformed email is rejected before the request leaves the browser
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open `/leads/new`, fill every field but set Email to `not-an-email`.
  2. Submit.
- **Expected:** Inline error "Invalid email address" under Email; no `POST /leads` is issued; URL
  unchanged.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (extended)

### TC-LEAD-010 — Choosing status "Lost" makes the create request fail
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open `/leads/new`, fill every required field from `generateLeadData()`.
  2. Open the Status select and choose "Lost".
  3. Submit.
- **Expected:** `POST /api/v1/leads` returns **400** with
  `error.code = "VALIDATION_ERROR"`, `error.message = "Validation failed"` and
  `error.details = {"Status":"Status must be one of: new contacted qualified unqualified converted"}`.
  The user sees only a generic red snackbar "Failed to create lead"; no inline field error appears
  and the form stays on `/leads/new` with the entered data intact.
- **Known issue:** Two defects compound here. (a) The frontend offers a `lost` status that the
  backend does not accept, and does not offer `unqualified` which it does —
  `LeadForm.tsx:27` / `LeadList.tsx:34` versus `models/lead.go` and the `oneof` binding tag on
  `CreateLeadRequest.Status` (`lead_handler.go:32`). (b) The error-mapping code in `LeadForm.tsx:89`
  reads `error.response.data.details`, but the API envelope nests the payload under
  `error` (`utils.RespondError`, `response.go:74`), so `details` is always `undefined` and every
  server-side validation failure degrades to the generic message.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-011 — A one-word contact name fails server-side validation
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open `/leads/new`, fill the form but set Contact Name to a single word (no space).
  2. Submit.
- **Expected:** `endpoints/leads.ts` splits on the space, producing `last_name: ""`.
  `POST /api/v1/leads` returns **400**, `error.details = {"LastName":"LastName is required"}`
  (binding tag `required` on `CreateLeadRequest.LastName`). The UI shows the generic
  "Failed to create lead" snackbar and stays on the form — same envelope-shape defect as
  TC-LEAD-010.
- **Known issue:** Contact Name has no client-side rule requiring two words, so a valid-looking
  entry is rejected only by the server, with an unhelpful message.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-012 — A sales user creating a lead becomes its owner automatically
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Sales user logged in.
- **Steps:**
  1. Create a lead through the UI form.
  2. Open the created lead's detail page.
- **Expected:** 201. `LeadForm.tsx` omits `owner_id` for non-admins, and
  `lead_handler.go:138` defaults `OwnerID` to the caller. The detail page's Owner field shows the
  sales user's first and last name.
- **Automation:** blocked — needs a sales role-login helper

### TC-LEAD-013 — A sales user cannot create a lead owned by someone else
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user with a valid JWT; a second user id known to exist.
- **Steps:**
  1. Issue `POST /api/v1/leads` with the sales user's token and an `owner_id` belonging to the
     other user.
- **Expected:** **403** `{"error":{"message":"You can only assign leads to yourself"}}`
  (`lead_handler.go:127`). No lead is created.
- **Known issue:** API-level only — the create form exposes no owner field, so this cannot be
  driven through the UI.
- **Automation:** blocked — needs a sales role-login helper; the assertion itself is an API call
  through Playwright's `request` fixture

### TC-LEAD-014 — Admin create without an explicit owner is rejected
- **Priority:** P0
- **Type:** validation
- **Preconditions:** Admin JWT.
- **Steps:**
  1. Issue `POST /api/v1/leads` with a valid body that omits `owner_id`.
- **Expected:** **400** `{"error":{"code":"BAD_REQUEST","message":"Owner ID is required for admin
  users"}}` (`lead_handler.go:135`). The UI never hits this because `LeadForm.tsx` injects the
  admin's own id, so the case is API-level.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new, via the `request`
  fixture)

### TC-LEAD-015 — Cancelling the create form discards the entry
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open `/leads/new`, type a company name.
  2. Click "Cancel".
- **Expected:** Navigation to `/leads`; no `POST` is issued; the list is unchanged.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can handle lead form
  cancellation"

---

## 3.3 Edit lead

### TC-LEAD-016 — Admin edits company and contact of an existing lead
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created by the test via `generateLeadData()`.
- **Steps:**
  1. Navigate to `/leads`, click the Edit (pencil) icon on the lead's row.
  2. Clear and refill Company Name and Contact Name.
  3. Click "Update Lead".
- **Expected:** `PUT /api/v1/leads/{id}` returns **200** with the updated lead. Snackbar "Lead
  updated successfully"; navigation back to `/leads`. The form is prefilled from
  `GET /leads/{id}` on mount (`LeadForm.tsx:74`).
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can edit an existing
  lead"

### TC-LEAD-017 — Clearing the Notes field does not clear it
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a lead created with non-empty notes.
- **Steps:**
  1. Open `/leads/{id}/edit`.
  2. Select all text in the Notes textarea and delete it.
  3. Submit, then reopen the lead's detail page.
- **Expected:** `PUT /leads/{id}` returns **200** and the success snackbar appears, but the Notes
  block on the detail page still shows the original text. The update handler builds its updates map
  only from non-empty strings (`if req.Notes != ""`, `lead_handler.go:387`), so an emptied optional
  field is dropped rather than written.
- **Known issue:** No text field on a lead can be blanked through the UI; the same
  `!= ""` pattern applies to phone, company, position, source and external_id
  (`lead_handler.go:357-389`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-018 — A sales user cannot edit a lead owned by someone else
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user logged in; a lead owned by the admin.
- **Steps:**
  1. Navigate directly to `/leads/{otherLeadId}/edit`.
- **Expected:** The prefill request `GET /leads/{id}` already returns **403**
  `{"error":{"message":"You can only view your own leads"}}` (`lead_handler.go:297`), so the form
  renders with empty defaults rather than the lead's data. Submitting sends
  `PUT /leads/{id}`, which returns **403** "You can only update your own leads"
  (`lead_handler.go:351`); the UI shows "Failed to update lead".
- **Known issue:** The page does not distinguish "forbidden" from "empty form" — a sales user
  reaching another owner's edit URL sees a blank create-shaped form.
- **Automation:** blocked — needs a sales role-login helper

### TC-LEAD-019 — Owner reassignment is admin-only and not exposed in the UI
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** Sales user JWT; a lead the sales user owns.
- **Steps:**
  1. Issue `PUT /api/v1/leads/{ownLeadId}` with `{"owner_id": <admin id>}`.
- **Expected:** **403** `{"error":{"message":"Only administrators can reassign leads"}}`
  (`lead_handler.go:394`). The same call as admin returns 200 and moves the lead.
- **Known issue:** The edit form has no Owner control at all, so reassignment — which the backend
  supports for admins — is unreachable from the UI.
- **Automation:** blocked — needs a sales role-login helper

---

## 3.4 View lead detail

### TC-LEAD-020 — Open a lead's detail page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created by the test.
- **Steps:**
  1. Read the id from the 201 create response.
  2. Navigate to `/leads/{id}`.
- **Expected:** URL matches `/leads/\d+$`. The company name renders as the `h4` next to a status
  Chip, and the Details tab shows Contact Name, Company, a `mailto:` Email link, a `tel:` Phone
  link, Source, Owner (first + last name of the owning user, or "Unassigned"), Created and Last
  Updated formatted `MMM dd, yyyy HH:mm`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can view lead details"

### TC-LEAD-021 — The Activities tab shows synthetic entries, not real history
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created and then edited at least once.
- **Steps:**
  1. Open `/leads/{id}` and click the "Activities" tab.
- **Expected:** Exactly two list items, always: "Lead created" attributed to "System" at
  `created_at`, and "Status changed to {status}" attributed to the owner's email (or "Unknown") at
  `updated_at`. No further entries appear regardless of how many edits were made.
- **Known issue:** The timeline is hardcoded in `LeadDetail.tsx:130` — there is no activity log
  behind it, and the second entry claims a status change even when the status never changed.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-022 — A non-existent lead id leaves the detail page spinning
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/leads/999999` (an id known not to exist).
- **Expected:** `GET /api/v1/leads/999999` returns **404**
  `{"error":{"code":"NOT_FOUND","message":"Lead not found"}}`. The page renders the `Loading`
  spinner indefinitely, because `LeadDetail.tsx:126` returns `<Loading />` for
  `isLoading || !lead` and never handles the error state. Navigating to `/leads/abc` instead
  returns **400** "Invalid lead ID" (`lead_handler.go:281`) with the same stuck spinner.
- **Known issue:** No not-found or error UI on the lead detail page; verified live against
  `GET /api/v1/leads/999999` and `/leads/abc`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

---

## 3.5 Delete lead (right to erasure)

### TC-LEAD-023 — Admin deletes a lead the test created
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created by this test via `generateLeadData()`. Do not
  delete pre-existing rows.
- **Steps:**
  1. Navigate to `/leads`, locate the created lead's row.
  2. Click its Delete (bin) icon.
  3. In the "Delete Lead" dialog — message "Are you sure you want to delete the lead
     "{company}"? This action cannot be undone." — click "Delete".
- **Expected:** `DELETE /api/v1/leads/{id}` returns **204** with no body. Snackbar "Lead deleted
  successfully"; the leads query is invalidated and the row disappears from the table.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can delete a lead"

### TC-LEAD-024 — Deletion erases the personal data, freeing the email for reuse
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in. Capture the generated email address before creating the lead.
- **Steps:**
  1. Create a lead with `generateLeadData()`; remember its email.
  2. Delete it (TC-LEAD-023 flow).
  3. Search the list for the original email address.
  4. Create a **new** lead reusing the exact same email address.
- **Expected:** Step 3 returns no rows — the search runs `LIKE` over
  `first_name, last_name, email, company, phone, notes`
  (`lead_repository.go:186`) and the erasure has overwritten all of them with a random
  `.invalid` address and placeholder names before soft-deleting the row
  (`erasure_cascade.go`, `erasure.go`). Step 4 returns **201**: the address is reusable because it
  no longer exists anywhere in the table.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-025 — Deleting a converted lead also erases its customer
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a lead created by the test, set to status "Qualified", then
  converted (see TC-LEAD-042). Note the resulting customer id.
- **Steps:**
  1. Confirm the customer exists at `/customers/{customerId}`.
  2. Return to `/leads` and delete the converted lead.
  3. Navigate to `/customers` and search for the original email address.
- **Expected:** 204 on the delete. The customer row no longer appears in the customer list and the
  original email is not found there either — `leadRepository.Delete` calls
  `eraseLeadWithConversionLink` (`lead_repository.go:139`), which follows `leads.customer_id` and
  erases both halves in one transaction (`erasure_cascade.go:234`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new; requires the customers
  page object for the verification step)

### TC-LEAD-026 — A sales user cannot delete a lead they do not own
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user logged in; a lead owned by the admin.
- **Steps:**
  1. Issue `DELETE /api/v1/leads/{otherLeadId}` with the sales user's token.
- **Expected:** **403** `{"error":{"message":"You can only delete your own leads"}}`
  (`lead_handler.go:450`). The lead is untouched. Deleting a lead the sales user *does* own returns
  204.
- **Automation:** blocked — needs a sales role-login helper

---

## 3.6 Search leads

### TC-LEAD-027 — Search by email address
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created with a `generateLeadData()` email.
- **Steps:**
  1. Navigate to `/leads`.
  2. Type the lead's email into the "Search leads..." box.
- **Expected:** `GET /api/v1/leads?...&search=<email>` returns 200 and the table shows the matching
  lead. The search is a case-insensitive `LIKE` across first_name, last_name, email, company, phone
  and notes; `total` in the response reflects the filtered count (`CountSearch`,
  `lead_repository.go:202`).
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should search for a
  lead by email"

### TC-LEAD-028 — Search by company name
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lead whose company name is unique to the run.
- **Steps:**
  1. Navigate to `/leads` and type the company name into the search box.
- **Expected:** 200; only rows whose company matches are listed; the pagination footer's total
  drops to the filtered count.
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should search for a
  lead by company name"

### TC-LEAD-029 — Clearing the search restores the full list
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a search already applied.
- **Steps:**
  1. Clear the search box.
- **Expected:** A fresh `GET /leads` without `search` is issued and the unfiltered list and total
  return. Each keystroke resets `page` to 1 (`LeadList.tsx:232`).
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should clear search
  and show all leads"

### TC-LEAD-030 — A search with no matches yields an empty table
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Type a random string that cannot match any record (for example
     `NoSuchLead_${Date.now()}`).
- **Expected:** 200 with `data.leads = []` and `data.total = 0`. The table body renders with zero
  rows; there is no empty-state message — `DataTable.tsx` renders nothing when `data` is empty.
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should return no
  results for non-existent search"

### TC-LEAD-031 — Search also matches phone and notes
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created with a distinctive notes paragraph and phone
  from `generateLeadData()`.
- **Steps:**
  1. Search for a distinctive word from the lead's notes.
  2. Search for a fragment of the lead's phone number.
- **Expected:** Both searches return the lead — `notes` and `phone` are in the `LIKE` clause
  (`lead_repository.go:186`), even though neither is presented as a searchable column in the UI.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (extended)

### TC-LEAD-032 — Search and sort combine
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; several leads sharing a search token.
- **Steps:**
  1. Search for the shared token.
  2. Click the "Created" column header.
- **Expected:** The request carries both `search` and `sort_by=created_at`; 200. The search branch
  (`lead_handler.go:227`) forwards `sortBy`/`sortOrder` to `leadService.Search`, which builds the
  ORDER BY through `utils.SafeOrderClause` (`lead_repository.go:190`).
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should search and
  sort together"

### TC-LEAD-033 — Search overrides an active classification filter
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; leads exist in more than one classification.
- **Steps:**
  1. Set the Classification dropdown to "Spam".
  2. Without clearing it, type a token into the search box that matches leads of other
     classifications.
- **Expected:** The request contains both `classification=spam` and `search=<token>`, and the
  response contains leads of *any* classification: the handler's branch chain checks `search`
  before `classification` (`lead_handler.go:227-232`), so the classification is dropped. Verified
  live: `?classification=spam&search=gmail` returned 11 rows while `?classification=spam` alone
  returned 0.
- **Known issue:** The two dropdown/search controls appear to compose but do not; the UI still
  shows "Spam" selected while displaying non-spam rows.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (new)

---

## 3.7 Sort leads

### TC-LEAD-034 — Sort by the Created column
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one lead.
- **Steps:**
  1. Navigate to `/leads`.
  2. Click the "Created" column header.
- **Expected:** `GET /api/v1/leads?...&sort_by=created_at&sort_order=asc` returns 200 and the rows
  re-render. `LeadList.handleSort` maps the column id through `fieldMap` before sending
  (`LeadList.tsx:245`).
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should sort by
  Created column descending"

### TC-LEAD-035 — Clicking a header twice toggles the sort direction
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Click "Created" once, then again.
- **Expected:** Two requests, both with `sort_by=created_at`, whose `sort_order` values differ
  (`asc` then `desc`, or the reverse depending on the starting state) —
  `DataTable.handleRequestSort` flips `order` on repeat clicks.
- **Automation:** automated — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` "should toggle sort
  order on double click"

### TC-LEAD-036 — Sort by Company, Contact, Email, Status, Classification and Source
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; enough leads for the ordering to be observable.
- **Steps:**
  1. For each of Company, Contact, Email, Status, Classification, Source: click the header and read
     the outgoing request plus the first rows.
- **Expected:** The requests carry `sort_by=company`, `first_name`, `email`, `status`,
  `classification`, `source` respectively (the Contact column maps to `first_name`,
  `LeadList.tsx:248`), each returns 200, and the visible order changes accordingly. All six are in
  the handler's `allowedSortColumns` allowlist (`lead_handler.go:189`).
- **Known issue:** Gap **G21** in `docs/FEATURES.md` — only `created_at` has E2E coverage today.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (extended; closes
  G21)

### TC-LEAD-037 — Clicking the Phone header sorts nothing
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; several leads with differing phone numbers.
- **Steps:**
  1. Note the order of the first rows.
  2. Click the "Phone" column header.
- **Expected:** The request carries `sort_by=phone`, returns **200**, and the row order is
  **unchanged**. `phone` is absent from `allowedSortColumns`, so the handler blanks `sortBy`
  (`lead_handler.go:201`) and falls through to the plain `List` branch. The header nonetheless
  renders its active-sort arrow, implying a sort that did not happen. Verified live:
  `?sort_by=notes&sort_order=up` returned 200 in default order.
- **Known issue:** Unknown sort columns and unknown sort orders are silently ignored rather than
  rejected with 400; the UI offers a sortable Phone header for a column the API cannot sort.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (new)

---

## 3.8 Filter by status (page-local control, no server support)

### TC-LEAD-038 — The Status dropdown does not change the result set
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; leads exist in at least two different statuses on the first
  page.
- **Steps:**
  1. Navigate to `/leads` and record the visible rows and the pagination total.
  2. Set the Status dropdown to "Converted".
  3. Record the rows and total again.
- **Expected:** A new request is issued with `status=converted` appended, it returns **200**, and
  the rows and total are **identical** to step 1. `lead_handler.go` never reads a `status` query
  parameter, and `LeadList.tsx` performs no client-side filtering either — the parameter is
  transmitted and discarded. Verified live: `?limit=3` and `?limit=3&status=converted` returned the
  same three rows and the same `total: 71`.
- **Known issue:** `docs/FEATURES.md` row 3.8 and gap **G34** describe this as client-side
  filtering; in the leads page it is not filtering at all. `LeadList.test.tsx` "filters leads by
  status" only asserts that `getLeads` was called with `status: 'new'`, which is why the defect
  survives the unit suite. Any test asserting server-side status filtering would be wrong.
- **Automation:** automated (weak) — `gocrm-ui/e2e/tests/admin-leads.spec.ts` "admin can filter
  leads by status" asserts only that the page survives the interaction
  (`expect(filteredCount).toBeGreaterThanOrEqual(0)`); the strict assertion above is planned as an
  extension of the same file.

### TC-LEAD-039 — Changing the status filter resets to page 1
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more than one page of leads.
- **Steps:**
  1. Page forward to page 2.
  2. Change the Status dropdown to any value.
- **Expected:** The next request carries `page=1`; the pagination footer returns to the first page
  (`handleStatusChange` sets `page: 1`, `LeadList.tsx:236`). The rows themselves are those of page
  1 unfiltered, per TC-LEAD-038.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

---

## 3.9 Filter by classification

### TC-LEAD-040 — The Classification dropdown filters server-side
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one lead whose classification is not
  `unclassified` (classification can only be set through the API — the create form never sends it).
- **Steps:**
  1. Navigate to `/leads` and record the total.
  2. Set the Classification dropdown to "Spam".
- **Expected:** `GET /api/v1/leads?...&classification=spam` returns 200; only leads with that
  classification are listed and `total` is the per-classification count
  (`GetByClassification` + `CountByClassification`, `lead_repository.go:110`/`:218`). With no spam
  leads present the table is empty and the total is 0 — verified live.
- **Known issue:** Gap **G17** in `docs/FEATURES.md` — this dropdown has no E2E coverage today.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new; closes G17)

### TC-LEAD-041 — Sorting is dropped while a classification filter is active
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; several leads sharing one classification.
- **Steps:**
  1. Set the Classification dropdown to the shared value.
  2. Click the "Email" column header.
- **Expected:** The request carries both `classification=<value>` and `sort_by=email&sort_order=desc`,
  returns 200, and the rows come back in insertion order, not email order. The
  classification branch precedes the sort branch (`lead_handler.go:230-235`) and
  `GetByClassification` accepts no sort arguments. Verified live: with
  `classification=unclassified&sort_by=email&sort_order=desc` the first emails were
  `Kitty53@…, Ada26@…, Salvatore.Bernier@…`, while `sort_by=email&sort_order=desc` alone returned
  `Wilfredo.Kuphal@…, Shannon.Johnston2@…, …`.
- **Known issue:** Filter and sort do not compose, and the column header still shows an active sort
  indicator.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (new)

---

## 3.10 Convert lead to customer

### TC-LEAD-042 — Convert a qualified lead and observe the broken post-conversion redirect
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a lead created by the test with status **Qualified** (the
  default value of the `leads.conversion.allowed_statuses` configuration is `['qualified']`,
  `ConfigurationContext.tsx:66`).
- **Steps:**
  1. Open `/leads/{id}`.
  2. Click the green "Convert to Customer" button.
  3. In the "Convert Lead to Customer" dialog, optionally adjust Company Name / Address /
     Conversion Notes.
  4. Click "Convert to Customer".
- **Expected:** `POST /api/v1/leads/{id}/convert` returns **200** with the new customer object
  (`{id, first_name, last_name, email, …}`). A "Lead converted to customer successfully" snackbar
  shows. The lead's status becomes `converted` and `leads.customer_id` is set
  (`lead_service.go:326`); the lead row remains in the list with a green "Converted" chip.
  The browser then navigates to **`/customers/undefined`**, which leaves the customer detail page
  on its loading spinner rather than showing the new customer.
- **Known issue:** `endpoints/leads.ts convertLead` types the response as `{customer_id: number}`,
  but the API returns a `models.Customer` whose id field is `id` (`customer.go`,
  `lead_handler.go:537`). `data.customer_id` is therefore `undefined` and both
  `LeadList.tsx:135` and `LeadDetail.tsx:114` navigate to `/customers/undefined`. The conversion
  itself succeeded — only the redirect is broken.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new). `docs/FEATURES.md`
  row 3.10 credits `admin-entity-suite.spec.ts` "admin can create complete CRM workflow:
  Lead -> Customer -> Task", but that test creates a lead and a customer independently and never
  clicks Convert — conversion has no E2E coverage today.

### TC-LEAD-043 — The Convert button appears only for allowed statuses
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; two leads created by the test, one "New" and one "Qualified".
- **Steps:**
  1. Open the "New" lead's detail page and look for the Convert button.
  2. Open the "Qualified" lead's detail page and look again.
- **Expected:** Absent for "New", present for "Qualified" — the button is gated on
  `getLeadConversionStatuses().includes(lead.status)`, whose configuration key
  `leads.conversion.allowed_statuses` defaults to `['qualified']` (`LeadDetail.tsx:158`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-044 — The list page's row-action menu, including Convert, never opens
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; at least one qualified lead.
- **Steps:**
  1. Navigate to `/leads`.
  2. Click the vertical-dots (MoreVert) button in the table toolbar.
- **Expected:** Nothing happens — no menu appears, so "View Details", "Edit", "Convert to Customer"
  and "Delete" are unreachable from the list. The button's handler is
  `(e) => selectedLead && handleMenuOpen(e, selectedLead)` (`LeadList.tsx:350`) and `selectedLead`
  is only ever set *by* `handleMenuOpen`, so the guard can never be satisfied. Conversion from the
  list is impossible; the per-row Visibility / Edit / Delete icons rendered by `DataTable` are the
  only working row actions.
- **Known issue:** Dead UI in `LeadList.tsx` — the toolbar menu and its Convert entry are
  unreachable.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-045 — Converting an already-converted lead is rejected
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a lead created and converted by the test (TC-LEAD-042).
- **Steps:**
  1. Issue a second `POST /api/v1/leads/{id}/convert` for the same lead (through the API — the
     button is hidden once the status is `converted`, per TC-LEAD-043).
- **Expected:** **400** `{"error":{"code":"BAD_REQUEST","message":"lead already converted"}}`.
  `leadService.ConvertToCustomer` returns `apperrors.ErrLeadConverted` both before and inside the
  transaction (`lead_service.go:272`, `:298`), and the handler classifies it with `errors.Is`
  (`lead_handler.go:528`). No second customer is created. Driven through the UI the snackbar would
  read "Failed to convert lead".
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new, via the `request`
  fixture)

### TC-LEAD-046 — The Website field in the convert dialog is discarded
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; a qualified lead created by the test.
- **Steps:**
  1. Open the convert dialog and fill Website with `https://example.com`.
  2. Convert, then inspect the created customer (via `GET /customers/{id}` using the id from the
     200 response body).
- **Expected:** 200. The customer carries the Company, Address and Notes from the dialog but no
  website anywhere: `ConvertLeadRequest.Website` is bound (`lead_handler.go:57`) and then never
  copied into `customerData` (`lead_handler.go:515`), and `models.Customer` has no website column
  at all.
- **Known issue:** A form field that validates as a URL and is then silently dropped.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

### TC-LEAD-047 — A sales user cannot convert a lead they do not own
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user logged in; a qualified lead owned by the admin.
- **Steps:**
  1. Issue `POST /api/v1/leads/{otherLeadId}/convert` with the sales user's token.
- **Expected:** **403** `{"error":{"message":"You can only convert your own leads"}}`
  (`lead_handler.go:510`). No customer is created and the lead's status is unchanged. The same call
  against a lead the sales user owns returns 200.
- **Automation:** blocked — needs a sales role-login helper

### TC-LEAD-048 — Converting a lead whose email already belongs to a customer returns 500
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in. Create a customer with a generated email, then create a
  qualified lead reusing that same email.
- **Steps:**
  1. Convert the lead.
- **Expected:** **500** `{"error":{"code":"INTERNAL_ERROR", …}}`. `customers.email` carries a
  unique index (`models/customer.go`), the insert inside the conversion transaction fails, and the
  handler only classifies `ErrLeadConverted` — every other error falls into
  `utils.RespondInternalError` (`lead_handler.go:531`). The UI shows "Failed to convert lead"; the
  transaction rolls back, so no partial customer is left behind.
- **Known issue:** A duplicate-email conflict is reported as 500 rather than the 409 that
  `apperrors.ErrDuplicateEmail` supports elsewhere (see the API-defect list in
  `docs/ROADMAP.md`). Clean-up: delete both records the test created.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new)

---

## 3.11 Pagination

### TC-LEAD-049 — Page forward and back through the lead list
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; more than 10 leads exist (create the shortfall via
  `generateLeadData()`).
- **Steps:**
  1. Navigate to `/leads` (10 rows per page by default).
  2. Click the next-page arrow in the pagination footer.
  3. Click the previous-page arrow.
- **Expected:** Step 2 issues `GET /leads?page=2&limit=10` (MUI's zero-based page index is
  incremented in `handlePageChange`, `LeadList.tsx:260`), returns 200 with a disjoint set of rows,
  and `meta.page` is 2. Step 3 returns to the original rows. Verified live:
  `?limit=10&page=3` returned `meta {page: 3, per_page: 10, total: 71, total_pages: 8}`.
- **Known issue:** Row 3.11 in `docs/FEATURES.md` — pagination is unit-tested only; there is no E2E
  test.
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (new)

### TC-LEAD-050 — Changing rows-per-page resets to the first page
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more than 25 leads.
- **Steps:**
  1. Navigate to page 2.
  2. Change "Rows per page" to 25 (options are 5, 10, 25, 50).
- **Expected:** A request with `limit=25&page=1`; 200; the footer shows page 1 and up to 25 rows
  (`handleRowsPerPageChange`, `LeadList.tsx:263`). The backend caps `limit` at 100 and never
  returns a zero limit — `?limit=0` falls back to 20 rather than 500ing
  (`utils.ParseOffsetLimit`; verified live, `meta.per_page` came back as 20).
- **Automation:** planned — `gocrm-ui/e2e/tests/leads-sorting-search.spec.ts` (new)

---

## Section 10 — Bulk lead status endpoint

`POST /leads/bulk/status` is routed by `SetupBulkStatusRoutes` (`routes.go:125`) with its own
`RequireRole(admin, sales)` guard. **The UI exposes no bulk selection for leads**: `LeadList.tsx`
does not pass `selectable` to `DataTable`, so no checkboxes render, and `DataTable`'s bulk-delete
handler is an empty `// TODO`. `leadsApi.bulkUpdateStatus` in
`gocrm-ui/src/api/endpoints/leads.ts` therefore has no caller. All cases below are consequently
**API-level** cases driven through Playwright's `request` fixture, not UI cases.

### TC-LEAD-051 — Bulk-set the status of several leads
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin JWT; three leads created by the test.
- **Steps:**
  1. `POST /api/v1/leads/bulk/status` with
     `{"lead_ids":[id1,id2,id3],"status":"contacted"}`.
  2. Re-read each lead.
- **Expected:** **200** with a `models.BulkStatusUpdateResult` body; all three leads report
  `status: "contacted"`. The write is a single all-or-nothing transaction
  (`bulk_status_service.go:172`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new API-level block)

### TC-LEAD-052 — One unowned lead fails the whole sales batch
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user JWT; one lead owned by that user and one owned by the admin.
- **Steps:**
  1. `POST /leads/bulk/status` with both ids and `{"status":"qualified"}`.
  2. Re-read the sales user's own lead.
- **Expected:** **403**, message "You can only update your own leads", with the not-owned id listed
  in `error.details` (`bulk_status_service.go:191`). The user's own lead is **unchanged** — the
  batch is refused in full before any write.
- **Automation:** blocked — needs a sales role-login helper

### TC-LEAD-053 — Malformed bulk payloads are rejected
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin JWT.
- **Steps:**
  1. Post an empty `lead_ids` array.
  2. Post 101 ids.
  3. Post a valid id list with a non-existent id.
- **Expected:** (1) and (2) return **400** — the binding tag is
  `required,min=1,max=100,dive,gt=0` (`bulk_handler.go:371`). (3) returns **404** with the missing
  ids listed in `error.details` (`bulk_status_service.go:188`) and nothing written.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new API-level block)

### TC-LEAD-054 — The frontend's bulk helper can emit a status the endpoint refuses
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin JWT.
- **Steps:**
  1. `POST /leads/bulk/status` with `{"lead_ids":[id],"status":"lost"}` — the value
     `leadsApi.bulkUpdateStatus` accepts, since its parameter is typed `Lead['status']`.
- **Expected:** **400**, `error.details` naming `Status` with
  "must be one of: new contacted qualified unqualified converted". Nothing is written.
- **Known issue:** Same frontend/backend status-vocabulary mismatch as TC-LEAD-010; here it is
  baked into a TypeScript signature that would compile happily.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new API-level block)

---

## Frontend contract with no backend route

### TC-LEAD-055 — Lead assignment endpoint does not exist
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin JWT; a lead created by the test; a second user id.
- **Steps:**
  1. `POST /api/v1/leads/{id}/assign` with `{"user_id": <other id>}` — the call
     `leadsApi.assignLead` makes.
- **Expected:** **404** from Gin's no-route handler: the leads group registers only `POST ""`,
  `GET ""`, `GET/PUT/DELETE "/:id"` and `POST "/:id/convert"` (`routes.go:26-31`). No lead is
  reassigned. The equivalent customer route, `POST /customers/{id}/assign`, *does* exist
  (`docs/FEATURES.md` section 10b), which is why the frontend function was written.
- **Known issue:** `leadsApi.assignLead` is an unimplemented intended contract, not dead code —
  per the repository directive the backend route should be built rather than the function deleted.
  Reassignment is currently possible only through `PUT /leads/{id}` with `owner_id`, admin-only
  (TC-LEAD-019).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-leads.spec.ts` (new API-level block); revisit
  as a functional case once the route exists
