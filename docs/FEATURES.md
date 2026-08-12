# GopherCRM Feature Documentation & Test Coverage

> **Last updated:** 2026-08-05, after the privacy/erasure and security-fix work.
> **Purpose:** Track all user-facing features, their test coverage, known gaps, and issues.
> **Convention:** Each feature is described from the user's perspective. Backend and frontend are on the same row.

Every row below was re-checked against the code and the test files in this commit. Where a claim
could not be established from the code it is marked **unverified** rather than guessed. Test counts
are mechanical: Playwright `test(...)` calls; Go test cases (top-level `func Test*` for plain files,
testify suite `Test*` methods for suite files — the one-line suite runner is not counted); and
Vitest `it(...)`/`test(...)` calls. Go subtests inside a `t.Run` are noted where they carry the
coverage.

Verified totals at the time of writing:

| Suite | Result |
|---|---|
| Backend (`go test ./...`) | 9 packages with tests, all pass; clean under `-race` |
| Backend statement coverage | 46.9% (`-coverpkg=./internal/...,./cmd/...`) |
| Frontend unit (`vitest run`) | 16 files, 142 tests, all pass |
| E2E (`playwright test --list`) | 100 tests in 9 files (execution not re-run for this update) |
| `tsc -b` | clean |
| `eslint .` | 40 errors, 137 warnings (pre-existing) |

## How to Read This Document

| Column | Meaning |
|--------|---------|
| **Feature** | What the user can do |
| **Description** | How it works end-to-end |
| **E2E Tests** | Playwright tests that exercise the full stack |
| **Unit Tests** | Go handler/service tests + frontend component tests |
| **Integration Tests** | Go tests that hit a real database (SQLite in-memory) |
| **Status** | `covered` / `partial` / `gap` / `untested` / `unverified` |
| **Known Issues** | Bugs, limitations, missing scenarios |

Status meanings:
- **covered** -- happy path and key edge cases are tested
- **partial** -- happy path tested, but significant scenarios missing
- **gap** -- feature exists but important test coverage is missing
- **untested** -- no automated tests exist
- **unverified** -- the row could not be confirmed from the code; treat the status as unknown

---

## 1. Authentication & Session Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 1.1 | **User Registration** | User fills form (name, email, password), account created, redirected to dashboard. **Always creates a `customer`** — a client-supplied role is ignored | `registration.spec.ts`: 15 tests (valid registration, validation errors, email format, password strength, duplicate email, network error, Enter key submit, loading state, visibility toggle) | `auth_handler_test.go`: 5 tests (RoleInjection_ForcedToCustomer, NoRole_DefaultsToCustomer, DuplicateEmail_Conflict, WeakPassword_BadRequest, Success); `user_service_test.go`: TestRegister_Success, TestRegister_UserExists, TestRegister_SoftDeletedUserExists | `AuthContext.test.tsx`: handles registration; `Register.validation.test.tsx`: 7 password-policy tests | `auth_integration_test.go`: TestRegisterEndpoint; `soft_delete_email_reuse_test.go`: TestRegisterRejectsEmailOfSoftDeletedUser, TestRegisterHandlerReturns409ForSoftDeletedEmail, TestRegisterHandlerReturns409ForLiveDuplicateEmail | **covered** | -- |
| 1.2 | **User Login** | User enters email/password, receives JWT, redirected to dashboard | `login.spec.ts`: 11 tests (render, success, wrong password, unknown email, empty fields, invalid email format, visibility toggle, navigate to registration, Enter key, protected route access, unauthenticated redirect) | `auth_service_test.go`: `TestAuthService_Login` with subtests (successful login, invalid email, wrong password, inactive user) | `AuthContext.test.tsx`: handles login successfully | `auth_integration_test.go`: TestLoginEndpoint; `user_test.go`: TestUserLogin | **covered** | -- |
| 1.3 | **Session Refresh** | Access token would auto-refresh via refresh token | -- | `auth_service_cookie_test.go`: TestAuthService_RefreshAccessToken asserts the stub error | -- | -- | **gap** | Not implemented. `authService.RefreshAccessToken` returns `errors.New("refresh tokens not implemented")` (`internal/service/auth_service.go:244`), and no `/auth/refresh` route is registered |
| 1.4 | **Logout** | User clicks logout, client-side token storage cleared | -- | `auth_service_cookie_test.go`: TestAuthService_InvalidateRefreshToken asserts the stub error | `AuthContext.test.tsx`: handles logout; `client.test.ts`: removes access and refresh tokens from BOTH storages | -- | **partial** | There is no server-side logout endpoint; logout is purely a client-side token wipe |
| 1.5 | **CSRF Protection** | HMAC-SHA256 signed token, 24h expiry, skipped for safe methods and API-key auth | -- | `csrf_test.go`: 6 tests (valid roundtrip, expired, tampered, empty, malformed, cross-secret); `auth_service_cookie_test.go`: Generate/Validate | -- | -- | **gap** | **The middleware is not wired.** `internal/middleware/csrf.go` exists and `config.CSRFConfig` is declared, but `middleware.CSRF` is never installed in `cmd/main.go`, so no route requires a token. Only the token codec is tested; there is no `middleware/csrf_test.go` |
| 1.6 | **API Key Auth** | CLI tools authenticate via `Authorization: ApiKey gcrm_xxx`. Keys hashed with HMAC-SHA256 (legacy SHA256 fallback) | -- | `apikey_handler_test.go`: 7 tests; `auth_service_test.go`: TestAuthService_ValidateAPIKey with subtests incl. *key of an erased user is rejected* and *key of a deactivated user is rejected*; `crypto_test.go`: 5 table-driven tests | -- | `apikey_integration_test.go`: TestAPIKeyAuthentication; `erasure_test.go`: TestAPIKeyOfAnErasedUserCannotAuthenticate, TestAPIKeyOfADeactivatedUserCannotAuthenticate | **covered** | -- |
| 1.7 | **Account Lockout** | Account locked for 15 min after 5 failed login attempts | -- | `auth_service_test.go` subtests: account locked, lock expired allows login, failed attempts increment and lock after 5, successful login resets failed attempts | -- | -- | **covered** | -- |
| 1.8 | **Password Complexity** | Min 10 chars with uppercase, lowercase, digit and special char | `registration.spec.ts`: validates password requirements | `password_test.go`: 1 table-driven test with 8 cases; `auth_handler_test.go`: TestRegister_WeakPassword_BadRequest | `Register.validation.test.tsx`: 7 tests, including the regression that `Password1` used to be accepted | -- | **covered** | The frontend zod schema now mirrors `internal/utils/password.go` |
| 1.9 | **Token Storage** | Tokens go to `sessionStorage` unless "remember me" is chosen, in which case `localStorage` | -- | -- | `client.test.ts`: 14 tests over persist/non-persist, storage precedence and cross-storage cleanup | -- | **covered** | Frontend-only behaviour; no E2E test asserts token lifetime across a tab close |

---

## 2. Dashboard

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 2.1 | **Dashboard Stats** | Shows total leads, customers, open tickets, pending tasks, conversion rate | `admin-entity-suite.spec.ts`: navigation includes dashboard | -- | -- | -- | **gap** | No `dashboard_handler_test.go` exists; no test verifies the stat values |
| 2.2 | **Quick Actions** | Dashboard has buttons to create leads, customers, etc. | -- | -- | -- | -- | **untested** | No tests for quick action navigation |

---

## 3. Lead Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 3.1 | **View Leads List** | Table shows leads with company, contact, email, phone, status, classification, source, created date | `admin-leads.spec.ts`: admin can view leads list page; `leads-sorting-search.spec.ts`: should load leads page with data | `lead_handler_test.go`: TestList_AdminViewsAll, TestList_SalesViewsOwn, TestList_SupportUserSeesOnlyOwnLeads (+ search/sort/classification variants) | `LeadList.test.tsx`: renders lead list with data, shows loading state initially | `lead_integration_test.go`: TestListLeads_AdminSeesAll, TestListLeads_SalesSeesOnlyOwn | **covered** | The whole `/leads` group is gated to admin+sales by `RequireRole` in `routes.go`, so the support/customer handler tests exercise a path the router does not reach |
| 3.2 | **Create Lead** | Form with company, contact, email, phone, source, status, notes; admin must specify owner | `admin-leads.spec.ts`: create a new lead, create with all optional fields, validation errors | `lead_handler_test.go`: TestCreate_Success, TestCreate_SalesUserWithOwnerID, TestCreate_AdminRequiresOwnerID | `LeadForm.test.tsx`: renders create form, submits with valid data | `lead_integration_test.go`: TestCreateLead_AdminSuccess, TestCreateLead_SalesSuccess, TestCreateLead_SalesCannotAssignToOthers | **covered** | -- |
| 3.3 | **Edit Lead** | Update any lead field; sales can only edit own leads | `admin-leads.spec.ts`: edit an existing lead | `lead_handler_test.go`: TestUpdate_Success, TestUpdate_SalesUserReassign | `LeadForm.test.tsx`: edit mode | `lead_integration_test.go`: TestUpdateLead_Success | **covered** | -- |
| 3.4 | **View Lead Detail** | Navigate to lead detail page | `admin-leads.spec.ts`: view lead details | `lead_handler_test.go`: TestGet_Success, TestGet_SalesUserForbidden | -- | `lead_integration_test.go`: TestGetLead_AdminSuccess, TestGetLead_SalesCannotAccessOthers | **covered** | -- |
| 3.5 | **Delete Lead** | Deletion **erases** the lead's personal data, then soft-deletes the row; cascades to the customer it was converted into | `admin-leads.spec.ts`: delete a lead | `lead_handler_test.go`: TestDelete_Success; `lead_service_test.go`: TestDelete_Success, TestDelete_NotFound, TestDelete_LookupFailurePropagates | `LeadList.test.tsx`: handles lead deletion | `lead_integration_test.go`: TestDeleteLead_Success; `lead_erasure_test.go`: 14 tests | **covered** | Irreversible — see section 12 |
| 3.6 | **Search Leads** | Type in search box, filters across name, email, company, phone, notes | `admin-leads.spec.ts`: search leads; `leads-sorting-search.spec.ts`: by email, by company, clear search, no results | `lead_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort, TestList_SupportUserWithSearchSeesOnlyOwnLeads | `LeadList.test.tsx`: filters leads by search term | -- | **covered** | -- |
| 3.7 | **Sort Leads** | Click column headers to sort asc/desc | `leads-sorting-search.spec.ts`: sort by Created desc, toggle sort order, search and sort together | `lead_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByCreatedAtAsc, TestList_SortByInvalidColumn, TestList_SortByInvalidOrder, TestList_SortWithPagination | -- | -- | **partial** | No integration test. Only `created_at` is exercised E2E; other columns untested |
| 3.8 | **Filter by Status** | Dropdown to filter leads by status | `admin-leads.spec.ts`: admin can filter leads by status | -- | `LeadList.test.tsx`: filters leads by status | -- | **partial** | Confirmed: `lead_handler.go` reads `classification` but **not** `status` from the query string, so status filtering is client-side over the current page only |
| 3.9 | **Filter by Classification** | Dropdown to filter by classification (hot_lead, lead, spam, etc.) | -- | `lead_handler_test.go`: TestList_CustomerUserWithClassificationSeesOnlyOwnLeads | -- | -- | **partial** | Backend reads `classification` (`lead_handler.go:154`) and one handler test now touches it, but no E2E test covers the dropdown |
| 3.10 | **Convert Lead to Customer** | Convert a qualified lead into a customer record | `admin-entity-suite.spec.ts`: complete CRM workflow: Lead -> Customer -> Task | `lead_handler_test.go`: TestConvertToCustomer_Success, _AlreadyConverted, _SalesUserForbidden; `lead_service_test.go`: 4 conversion tests | `LeadList.test.tsx`: handles lead conversion for qualified leads | `lead_integration_test.go`: 3 conversion tests; `lead_conversion_transaction_test.go`: 2 transaction tests | **covered** | -- |
| 3.11 | **Pagination** | Navigate between pages of leads | -- | `lead_handler_test.go`: TestList_SortWithPagination | `LeadList.test.tsx`: handles pagination | -- | **partial** | No dedicated E2E pagination test |

---

## 4. Customer Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 4.1 | **View Customers List** | Table showing customers with name, email, company, revenue, status | `admin-customers.spec.ts`: admin can view customers list page | `customer_handler_test.go`: TestList_Success, TestList_WithPagination | `CustomerList.test.tsx`: renders list, displays active/inactive status, formats total revenue | `customer_integration_test.go`: TestListCustomers_AdminSeesAll, _SalesCanSee, _SupportCanSee, _CustomerRoleForbidden | **covered** | -- |
| 4.2 | **Create Customer** | Form with name, email, phone, company, full address | `admin-customers.spec.ts`: create customer, minimal data, validation errors, duplicate email | `customer_handler_test.go`: TestCreate_Success, TestCreate_DuplicateEmail, TestCreate_ForbiddenForSupportUser, TestCreate_DuplicateEmailDoesNotLeakDriverInternals; `customer_service_test.go`: TestCreate_SoftDeletedCustomerExists | -- | `customer_integration_test.go`: TestCreateCustomer_AdminSuccess, _SalesSuccess, _DuplicateEmail, _SupportUserForbidden; `soft_delete_email_reuse_test.go`: TestCustomerCreateRejectsEmailOfSoftDeletedCustomer, TestCreateCustomerHandlerDoesNotLeakDriverErrorForSoftDeletedEmail | **covered** | Duplicate email now returns **409** (previously 400) and no longer leaks driver text |
| 4.3 | **Edit Customer** | Update customer fields | `admin-customers.spec.ts`: edit an existing customer | `customer_handler_test.go`: TestUpdate_Success, TestUpdate_DuplicateEmail, TestUpdate_DuplicateEmailDoesNotLeakDriverInternals | -- | `customer_integration_test.go`: TestUpdateCustomer_AdminSuccess, _DuplicateEmail, _SupportUserForbidden | **covered** | -- |
| 4.4 | **Delete Customer** | Admin-only. **Erases** personal data, then soft-deletes; cascades to the lead it was converted from | `admin-customers.spec.ts`: delete a customer | `customer_handler_test.go`: TestDelete_Success, TestDelete_NotFound, TestDelete_GenuineFailureIsInternalError, TestDelete_ForbiddenForNonAdmin | `CustomerList.test.tsx`: handles customer deletion | `customer_integration_test.go`: TestDeleteCustomer_AdminSuccess, _SalesUserForbidden; `erasure_test.go` + `lead_erasure_test.go` cascade tests | **covered** | Irreversible — see section 12 |
| 4.5 | **Search Customers** | Search across first_name, last_name, email, company, phone, notes | `admin-customers.spec.ts`: search customers | `customer_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort | `CustomerList.test.tsx`: filters customers by search term | -- | **covered** | -- |
| 4.6 | **Sort Customers** | Click column headers to sort | -- | `customer_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | No E2E test |
| 4.7 | **Pagination** | Navigate customer pages | -- | `customer_handler_test.go`: TestList_WithPagination | -- | `customer_integration_test.go`: TestPagination | **covered** | -- |

---

## 5. Ticket Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 5.1 | **View Tickets List** | Table with subject, status, priority, customer, assignee | `admin-tickets.spec.ts`: admin can view tickets list page | `ticket_handler_test.go`: TestList_Success, TestList_CustomerRole_Forbidden | `TicketList.test.tsx`: 19 tests | `ticket_test.go`: TestTicketLifecycle | **covered** | -- |
| 5.2 | **Create Ticket** | Form with subject, description, priority, customer, assignee | `admin-tickets.spec.ts`: create ticket, validation errors, long description | `ticket_handler_test.go`: TestCreate_Success, TestCreate_ValidationError, TestCreate_CustomerRole_Forbidden; `ticket_service_test.go`: 4 create tests | `TicketForm.test.tsx`: 18 tests | `ticket_test.go`: TestTicketLifecycle, TestTicketValidation | **covered** | Only **admin and support** may create tickets (`ticket_handler.go:52`); sales cannot |
| 5.3 | **Edit Ticket** | Update ticket fields including status transitions | `admin-tickets.spec.ts`: edit ticket, update status, update priority | `ticket_handler_test.go`: TestUpdate_Success_Admin, TestUpdate_Support_NotAssigned_Forbidden, TestUpdate_Support_Assigned_Success | `TicketForm.test.tsx`: edit mode, change agent, unassign | `ticket_test.go`: TestTicketStatusTransitions | **covered** | -- |
| 5.4 | **Delete Ticket** | Admin-only deletion. Ordinary soft delete — tickets hold no personal data of their own | `admin-tickets.spec.ts`: delete a ticket | `ticket_handler_test.go`: TestDelete_Success_Admin, TestDelete_NonAdmin_Forbidden, TestDelete_ServiceError | `TicketList.test.tsx`: handles deletion, cancel deletion | `bulk_erasure_test.go`: TestBulkDeleteTasksAndTicketsRemainPlainSoftDeletes | **covered** | -- |
| 5.5 | **Filter by Status/Priority** | Dropdown filters for status and priority | `admin-tickets.spec.ts`: filter by status, filter by priority | -- | `TicketList.test.tsx`: filters by status, by priority, clears filters, combines filters | -- | **partial** | Confirmed: `ticket_handler.go` reads only `page`, `limit`, `search`, `sort_by`, `sort_order`. Filtering is client-side over the current page |
| 5.6 | **Search Tickets** | Search across title, description, resolution | `admin-tickets.spec.ts`: search tickets | `ticket_handler_test.go`: TestList_SearchByTitle, TestList_SearchWithSort | `TicketList.test.tsx`: filters by search term | -- | **covered** | -- |
| 5.7 | **Sort Tickets** | Click column headers to sort | -- | `ticket_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | No E2E test |
| 5.8 | **My Tickets** | Support users see their assigned tickets | -- | `ticket_handler_test.go`: TestListMyTickets_Success, TestListMyTickets_CustomerRole_Forbidden | -- | `ticket_test.go`: TestListMyTickets | **partial** | No E2E test |
| 5.9 | **Tickets by Customer** | `GET /customers/:id/tickets` — a customer-role user may only read their own | -- | `ticket_handler_test.go`: TestListByCustomer_Success, _CustomerRole_OtherCustomer_Forbidden, _CustomerRole_OwnCustomer_Success, _AdminRole_AnyCustomer_Success, _CustomerRole_LookupError_Forbidden | -- | `ticket_test.go`: TestListByCustomer | **covered** | Previously had **no ownership check** (IDOR); regression tests added. Still no E2E test |

---

## 6. Task Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 6.1 | **View Tasks List** | Table with title, status, priority, assignee, due date | `admin-tasks.spec.ts`: admin can view tasks list page | `task_handler_test.go`: TestListTasks_Success, TestListTasks_NonAdminGetsOwnTasks | -- | `internal/integration/task_integration_test.go`: TestTaskLifecycle | **partial** | No frontend unit test for `TaskList` |
| 6.2 | **Create Task** | Form with title, description, priority, due date, assignee, related lead/customer | `admin-tasks.spec.ts`: create task, minimal data, different priorities | `task_handler_test.go`: TestCreateTask_Success, _NonAdminAssignToOther_Forbidden, _ValidationError, _ServiceError; `task_service_test.go`: 8 create tests | -- | `internal/integration/task_integration_test.go`: TestTaskCreationPermissions, TestTaskValidation | **partial** | No frontend unit test for `TaskForm` |
| 6.3 | **Edit Task** | Update task fields; mark as complete. Only admins may reassign | `admin-tasks.spec.ts`: edit task, track task progress through status changes | `task_handler_test.go`: TestUpdateTask_Success, _NonAdminReassign_Forbidden, _NonAdminSameAssignee_Success, _NonAdminDifferentAssignee_Forbidden, _AdminReassign_Success, _NonAdminAccessOthersTask_Forbidden | -- | `internal/integration/task_integration_test.go`: TestTaskLifecycle, TestTaskBusinessRules | **covered** | Editing used to 403 for non-admins when the request merely echoed the current assignee; `TestUpdateTask_NonAdminSameAssignee_Success` is the regression test |
| 6.4 | **Delete Task** | Admin-only deletion. Ordinary soft delete | `admin-tasks.spec.ts`: delete a task | `task_handler_test.go`: TestDeleteTask_Success, TestDeleteTask_NonAdminForbidden | -- | `bulk_erasure_test.go`: TestBulkDeleteTasksAndTicketsRemainPlainSoftDeletes | **covered** | -- |
| 6.5 | **Filter by Status/Priority** | Dropdown filters | `admin-tasks.spec.ts`: filter by status, filter by priority | -- | -- | -- | **partial** | Confirmed: `task_handler.go` reads only `search`, `sort_by`, `sort_order` (plus pagination). Filtering is client-side |
| 6.6 | **Search Tasks** | Search across title, description | `admin-tasks.spec.ts`: search tasks | `task_handler_test.go`: TestListTasks_SearchByTitle, TestListTasks_SearchWithSort | -- | -- | **covered** | -- |
| 6.7 | **Sort Tasks** | Click column headers to sort | -- | `task_handler_test.go`: TestListTasks_SortByCreatedAtDesc, TestListTasks_SortByInvalidColumn | -- | -- | **partial** | No E2E test |
| 6.8 | **Due Date Management** | Set and validate due dates | `admin-tasks.spec.ts`: covered incidentally by create/edit specs | -- | -- | `internal/integration/task_integration_test.go`: TestTaskWithDueDate | **partial** | No dedicated E2E test title for due-date validation; the previously claimed "manage due dates" / "date validation" specs do not exist |
| 6.9 | **My Tasks** | Non-admin users see their assigned tasks | -- | `task_handler_test.go`: TestGetMyTasks_NonAdminCanAccess, TestMyTasks_ParsePaginationSuccess | -- | `internal/integration/task_integration_test.go`: TestMyTasks | **partial** | No E2E test |

---

## 6b. Task Labels (2026-08)

Free-form colored labels group tasks without a project hierarchy. Screenshots under
`docs/screenshots/labels/`; catalog cases in `docs/testing/10-labels.md`.

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 6b.1 | **Label CRUD** | `/labels` management page: create/rename/recolor with preset swatch picker, duplicate names rejected (409), delete is admin-only and hard-deletes | `labels.spec.ts`: create, duplicate-name error, rename+recolor, delete | `label_handler_test.go`, `label_service_test.go` | `LabelList.test.tsx` | `label_repository_test.go` (SQLite, many2many round-trip, hard delete clears join rows) | **covered** | Labels are hard-deleted (no PII, avoids the soft-delete unique-index trap) |
| 6b.2 | **Attach labels to tasks** | Multi-select Autocomplete on the task form with inline creation (palette-assigned color); `label_ids` on create/update, capped at 100, unknown ids → 400 | `labels.spec.ts`: attach on create, inline create, replace set with [] | `task_labels_handler_test.go`, `task_labels_test.go` | `TaskForm.test.tsx` (incl. 409 recovery selecting the existing label) | -- | **covered** | Inline create hidden from customer role (would always 403) |
| 6b.3 | **Chips in list/detail** | Colored chips with luminance-picked text color; list column capped at 3 with +N overflow | `labels.spec.ts`: chips in row and detail | -- | `LabelChip.test.tsx`, `TaskList.test.tsx` | -- | **covered** | -- |
| 6b.4 | **Filter tasks by label** | Chip click or dropdown sets `?label_id=`; active filter echoed as a deletable chip | `labels.spec.ts`: chip-click filter, dropdown + clear | `task_handler_test.go` label filter cases | `TaskList.test.tsx` | `label_repository_test.go` filter cases | **covered** | Server cannot combine `label_id` with `search` (label filter wins); the UI disables search while a label filter is active |

---

## 7. User Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 7.1 | **View Users List** | Admin sees table of all users with role and status | `admin-users.spec.ts`: admin can view users list page | `user_handler_test.go`: TestList_Success | `routes/index.test.tsx`: renders the REAL `UserList` at `/users` for an admin | `user_test.go`: TestUserCRUD | **covered** | The `/users` admin page was unreachable — the route defined both a static `element` and a `lazy` import, so React Router rendered a placeholder. `routes/index.test.tsx` pins the invariant |
| 7.2 | **Create User** | Admin creates users with any role. This and the `create-admin` CLI are the only ways to obtain an elevated role | `admin-users.spec.ts`: create user, different roles, validation errors, password mismatch, duplicate email | `user_handler_test.go`: TestCreate_Success, TestCreate_EmailConflict | -- | `user_test.go`: TestUserRegistration, TestEmailUniqueness | **covered** | -- |
| 7.3 | **Edit User** | Update user profile, role, active status. Only admins may change role or `is_active` | `admin-users.spec.ts`: edit an existing user | `user_handler_test.go`: TestUpdate_Success | -- | `user_test.go`: TestUserCRUD | **covered** | -- |
| 7.4 | **Delete User** | Admin-only. **Erases** personal data, purges API keys and refresh tokens, then soft-deletes. Cannot delete yourself | -- | `user_handler_test.go`: TestDelete_Success, TestDelete_NotFound, TestDelete_GenuineFailureIsInternalError, TestDelete_SelfDeletion; `user_service_test.go`: TestDelete_Success, TestDelete_NotFound, TestDelete_LookupFailureIsNotReportedAsNotFound | -- | `erasure_test.go`: 27 tests; `erasure_atomicity_test.go`: 11 tests | **covered** | The previously claimed `admin-users.spec.ts` "cannot delete self" E2E test does not exist — `admin-users.spec.ts` has no delete test at all. Irreversible — see section 12 |
| 7.5 | **Activate/Deactivate** | Toggle user active status. This is the **reversible** way to suspend access | -- | -- | -- | `erasure_test.go`: TestUserDeactivationDoesNotAnonymiseAnything; `erasure_pii_sweep_test.go`: TestDeactivationLeavesThePersonalDataWhereItIs | **partial** | No handler unit test for the toggle. The previously claimed "deactivate and activate users" E2E test does not exist |
| 7.6 | **Search Users** | Search across email, first_name, last_name | `admin-users.spec.ts`: search users | `user_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort | -- | -- | **covered** | -- |
| 7.7 | **Sort Users** | Click column headers to sort | -- | `user_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | No E2E test |
| 7.8 | **Filter by Role** | Dropdown to filter by role | `admin-users.spec.ts`: admin can filter users by role | -- | -- | -- | **partial** | **Correction to the previous version of this document:** `user_handler.go` does **not** read a `role` query param (only `page`, `limit`, `search`, `sort_by`, `sort_order`). Filtering is client-side over the current page |
| 7.9 | **My Profile** | User can view/edit own profile | -- | `user_handler_test.go`: TestGetMe_Success, TestUpdateMe_Success, TestGet_Forbidden | -- | `user_test.go`: TestMeEndpoints | **partial** | No E2E test |

---

## 8. API Key Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 8.1 | **Generate API Key** | User creates a named API key for CLI access | -- | `apikey_handler_test.go`: TestCreate_Success, TestCreate_Error; `apikey_service_test.go`: TestAPIKeyService_Generate, _Generate_CreateError | -- | `apikey_integration_test.go`: TestCreateAPIKey | **partial** | No E2E test. Handler tests now exist (this row previously said they did not) |
| 8.2 | **List API Keys** | View all keys (masked) with last-used info | -- | `apikey_handler_test.go`: TestList_Success, TestList_Error; `apikey_service_test.go`: TestAPIKeyService_GetByUser, _List | -- | `apikey_integration_test.go`: TestListAPIKeys | **partial** | No E2E test |
| 8.3 | **Revoke API Key** | Delete/revoke an API key | -- | `apikey_handler_test.go`: TestRevoke_Success, _NotFound, _Forbidden; `apikey_service_test.go`: TestAPIKeyService_Revoke | -- | `apikey_integration_test.go`: TestRevokeAPIKey | **partial** | No E2E test |
| 8.4 | **Keys die with their owner** | Erasing or deactivating a user invalidates their keys | -- | `auth_service_test.go` subtests: key of an erased user is rejected; key of a deactivated user is rejected | -- | `erasure_test.go`: TestUserErasureDestroysCredentialsThatWouldOutliveTheAccount, TestAPIKeyOfAnErasedUserCannotAuthenticate, TestAPIKeyOfADeactivatedUserCannotAuthenticate | **covered** | -- |

---

## 9. Configuration Settings

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 9.1 | **View Configurations** | Admin sees all system configurations; `/configurations/ui` is open to any authenticated user | -- | -- | -- | `configuration_integration_test.go`: TestGetAllConfigurations_AdminOnly, TestGetUIConfigurations, TestGetConfigurationByCategory, TestGetConfigurationByKey, TestUnauthorizedAccess | **partial** | No E2E test; no `configuration_handler_test.go` and no configuration service unit test |
| 9.2 | **Update Configuration** | Admin can change configuration values | -- | -- | -- | `configuration_integration_test.go`: TestSetConfiguration, _InvalidValue, _ReadOnly, TestBooleanConfiguration, TestArrayConfiguration | **partial** | No E2E test |
| 9.3 | **Reset Configuration** | Admin can reset to defaults | -- | -- | -- | `configuration_integration_test.go`: TestResetConfiguration | **partial** | No E2E test |
| 9.4 | **Configuration Transactions** | Multi-step configuration writes are atomic | -- | -- | -- | -- | **untested** | The previous version of this document listed `test/integration/configuration_transaction_test.go`. **That file does not exist** — no test covers configuration transactionality |

---

## 10. Bulk Operations

**Bulk status updates ARE reachable over HTTP** since the 2026-08 build-out:
`POST /leads|tickets|tasks/bulk/status` (max 100 ids, all-or-nothing, per-item authorization),
covered by 24 handler tests (`bulk_handler_test.go`) and 20 integration tests
(`test/integration/bulk_status_update_test.go`) including rollback proofs. The generic
`/bulk/:resource` create/update/delete/action handlers below remain unrouted; their rows describe
the service and repository layers, which *are* exercised.

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 10.1 | **Bulk Create** | Create multiple records at once | -- | `bulk_operation_service_test.go`: TestValidateBulkRequest, TestCreateBulkOperation, TestProcessBulkCreate_InvalidResourceType, TestConvertMapToModel; `bulk_operation_persistence_test.go`: TestBulkCreateUsers_Success, _DuplicateInLaterBatch_RollsBackEverything, _DuplicateInSingleBatch_ReportsFailure, TestBulkCreate_AllResourceTypes_RollBackOnError | -- | -- | **partial** | Previously panicked on **every** call (a slice pointer was asserted to a slice) and reported rows it never inserted as successful. Fixed and pinned by the persistence suite. Still no HTTP route and no E2E test |
| 10.2 | **Bulk Update** | Update multiple records at once | -- | `bulk_operation_persistence_test.go`: TestBulkUpdateUsers_PartialSuccessIsReal | -- | -- | **partial** | Only the user resource is covered; no HTTP route |
| 10.3 | **Bulk Delete** | Delete multiple records at once. Users, customers and leads are **erased**; tickets and tasks stay plain soft deletes | -- | `bulk_operation_persistence_test.go`: TestBulkDeleteUsers_MissingIDIsNotASuccess, _SelfDeleteIsReported, _ErasesRatherThanHides, TestBulkDeleteCustomers_ErasesTheOriginatingLead, TestBulkDeleteLeads_ErasesTheConvertedCustomer | -- | `bulk_erasure_test.go`: 8 tests; `bulk_erasure_isolation_test.go`: 4 tests | **covered** (service/repo layer) | No HTTP route and no E2E test |
| 10.4 | **Bulk Actions** | Apply actions to multiple records | -- | -- | -- | -- | **untested** | No tests at all; no HTTP route |

---

## 10b. 2026-08 Backend Build-out (session lifecycle, analytics, export/assign, API-key management)

Added after this matrix was first compiled; every row verified by the named suites. See
CHANGELOG "Added" for the full behavioural description.

| Feature | Endpoints | Tests |
|---|---|---|
| Auth sessions | `POST /auth/refresh`, `/auth/logout`, `/auth/change-password`, `/auth/password-reset`, `/auth/password-reset/confirm`; login returns a rotating refresh token | `internal/service/auth_session_test.go`, `internal/repository/refresh_token_repository_test.go`, `password_reset_token_repository_test.go`, `internal/mailer/mailer_test.go`, `tests/auth_session_integration_test.go` (login→refresh→replay-401→logout lifecycle, mailed reset round-trip) |
| Dashboard analytics | `GET /dashboard/{leads-by-status,tickets-by-priority,tasks-by-status,sales-performance,activities,upcoming-tasks,recent-tickets,new-leads}`, `GET /tasks/upcoming` | 85 tests: `dashboard_handler_test.go`, pure Go time-bucketing tests (leap day / quarter rollover), `internal/repository/dashboard_queries_test.go`, service tests; mutation-checked |
| Bulk status | `POST /{leads,tickets,tasks}/bulk/status` | see section 10 above |
| API keys | `GET/PUT /api-keys/{id}`, `expires_at` on create, expiry enforced at auth | `apikey_service_test.go`, `apikey_handler_test.go`, `tests/apikey_integration_test.go` incl. `TestExpiredAPIKeyIsRejectedAtAuth` |
| Customers | `GET /customers/export` (CSV, admin), `POST /customers/{id}/assign` (new `assigned_to_id` column, survives erasure) | handler/service/repo suites + `tests/customer_integration_test.go` (15 new tests) incl. erasure regression |
| Erasure | password-reset tokens purged with the account | `test/integration/erasure_test.go` `TestUserErasureDestroysCredentialsThatWouldOutliveTheAccount` |

## 10c. Answer Engine Optimization (AEO) (2026-08)

Tracks how often LLM answer engines mention the brand. Catalog cases in `docs/testing/11-aeo.md`; manual
live-provider smoke in `scripts/aeo_live_smoke.sh`. **No Playwright coverage exists yet** — every
row below is unit/integration only, which is why none is marked *covered*.

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 10c.1 | **Brand profile** | `/aeo/settings`: brand name, aliases, owned domains, competitors; single row pinned to ID 1; domains lowercased and `www.`-stripped on save | none | `aeo_handler_test.go`, `aeo_service_test.go` | `AEOSettings.test.tsx` | `aeo_repository_test.go` (SQLite, JSON-in-TEXT round-trip) | **partial** | Whitespace-only brand name returned 500 until 2026-08-11; now 400 |
| 10c.2 | **Prompts** | `/aeo/prompts`: up to 100 active, ≤500 runes, duplicates rejected case-insensitively over live rows, soft delete keeps the answers | none | `aeo_handler_test.go`, `aeo_service_test.go` | `AEOPrompts.test.tsx` | `aeo_repository_test.go` | **partial** | No unique index on `text` by design — the soft-delete trap; uniqueness is a service-level `LOWER(text)` pre-check |
| 10c.3 | **Providers** | Six engines behind one interface: Anthropic SDK + one OpenAI-compatible wrapper for OpenAI, Gemini, Kimi, Perplexity and any custom base URL; keyless engine skipped | none | `internal/aeo/anthropic_test.go`, `openai_compat_test.go`, `provider_test.go` (httptest servers: success, empty choices, 429, 500, malformed JSON, perplexity citations) | `AEOSettings.test.tsx` (chips) | -- | **partial** | Live credentials are exercised only by the manual smoke script |
| 10c.4 | **Runs + engine** | `POST /aeo/runs` returns 202 and executes on a background context; worker pool of 4, 60s per query; one answer row per (prompt × provider) including failures; run ends completed/partial/failed | none | `internal/aeo/engine_test.go`, `aeo_service_test.go` | -- | `aeo_repository_test.go` | **partial** | An issued run cannot be cancelled; the overlap guard is process-local (single-process assumption, see the spec) |
| 10c.5 | **Run recovery** | Runs stranded in `running` by a crash or a deploy are failed at startup (`ReconcileRunningRuns`) and swept by `StartRun` after 6h | none | `aeo_service_test.go` (`TestReconcileRunningRuns`, `TestStartRun_SweepsRunsStrandedByACrash`, `TestStartRun_ConcurrentCallsStartExactlyOneRun`) | -- | `aeo_repository_test.go` (`TestAEORepository_MarkStaleRunsFailed`) | **partial** | Added 2026-08-11: before it, one stranded row rejected every later run with 409 permanently |
| 10c.6 | **Mention + citation analysis** | Unicode-aware word-boundary matching for brand/aliases/competitors, first-mention rune offset, URL extraction and domain normalisation (`www.`, port, case) | none | `internal/aeo/analysis_test.go` | `AEOPrompts.test.tsx` (highlighting) | -- | **partial** | Matching is literal: no stemming, no fuzzy matching |
| 10c.7 | **Dashboard + citations** | `/aeo` and `/aeo/citations`: visibility, per-provider timeline with no gap days, share of voice, citation and brand-mention rates; 7/30/90 windows, range clamped to 90 days | none | `aeo_service_test.go` (arithmetic incl. zero-answer case), `aeo_handler_test.go` | `AEODashboard.test.tsx`, `AEOCitations.test.tsx` | `aeo_repository_test.go` (aggregations + a portability scan for MySQL/SQLite-only SQL) | **partial** | Per-day bucketing is done in Go; no `DATE()`/`strftime` anywhere by design |
| 10c.8 | **Scheduler** | Daily run at `AEO_SCHEDULE_HOUR`, panic-recovered, stops with the server's background context | none | `internal/aeo/scheduler_test.go` (`NextRunAt` boundaries) | -- | -- | **partial** | Every replica would arm its own scheduler — the module assumes one API process |
| 10c.9 | **RBAC** | Group guard admin/sales/support (customer 403 everywhere); writes admin+sales; prompt delete admin-only; SPA nav and routes mirror it | none | `aeo_handler_test.go` role matrix, `routes_test.go` static/param coexistence | -- | -- | **partial** | Sales/support paths are untestable end-to-end until a role-login helper exists |

---

## 11. Cross-Cutting Concerns

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 11.1 | **Role-Based Access** | Different roles see different data and have different permissions | `admin-users.spec.ts`: manage user permissions through roles; `login.spec.ts`: unauthenticated user is redirected to login | Handler tests cover role checks throughout; `auth_test.go`: 12 middleware tests | `ProtectedRoute.test.tsx`: 7 tests; `routes/index.test.tsx`: blocks non-admins from `/users` | `auth_integration_test.go`: TestProtectedRoute; `user_test.go`: TestPermissionEnforcement, TestProtectedRoutes | **covered** | -- |
| 11.2 | **Rate Limiting** | Per-IP token bucket. `RateLimitStrict` (10/min, burst 5) on `/auth/*`; `RateLimitModerate` (120/min, burst 30) on every authenticated route | -- | `rate_limit_test.go`: 13 tests | -- | -- | **partial** | No E2E or integration test. `RateLimitGenerous` (240/min) is defined in `rate_limit.go:142` but never applied, so there are two active tiers, not three, and reads are not treated differently from writes. The `// 60 req/min` comment at `cmd/main.go:164` is stale. `DISABLE_RATE_LIMIT=true` bypasses only the strict tier |
| 11.3 | **Request Logging** | All requests logged with structured JSON | -- | `logger_test.go`: 3 table-driven tests; `request_id_test.go`: 4 tests | -- | -- | **covered** | Logs record the email address on login and on customer create/update — see section 12 |
| 11.4 | **Error Handling** | Consistent error format across all endpoints | `admin-entity-suite.spec.ts`: admin can handle error scenarios gracefully | `error_handler_test.go`: 6 tests; `recovery_test.go`: 4 tests; `response_test.go`: 27 tests | -- | `error_handling_test.go`: 6 tests (400, 401, 403, 404, 500, consistent format) | **covered** | No frontend `ErrorBoundary` test |
| 11.5 | **Pagination Parameters** | `page`, `limit`, `offset` parsing via `ParseOffsetLimit` (every list endpoint; `per_page` is no longer read anywhere) | -- | `response_test.go`: TestParseOffsetLimit_Defaults, _Custom, _ExceedsMax, _NeverReturnsZeroLimit, _RejectsNegativeOffset; `task_handler_test.go`: limit-honoured / cap / offset / page-conversion tests | -- | -- | **covered** | `?limit=0` used to reach the pagination arithmetic and panic, turning every list endpoint into a 500 via one query parameter. `TestParseOffsetLimit_NeverReturnsZeroLimit` is the regression test. Tasks previously read `page`/`per_page` and ignored the frontend's `limit` |
| 11.6 | **Sort Validation** | `sort_by` is validated against per-entity column allowlists | -- | `sort_test.go`: 11 tests; per-handler `TestList_SortByInvalidColumn` | -- | -- | **covered** | -- |
| 11.7 | **Transactions** | Multi-step operations run in one transaction | -- | `transaction_test.go`: 7 tests | -- | `lead_conversion_transaction_test.go`, `user_registration_transaction_test.go`, `erasure_atomicity_test.go` | **covered** | -- |
| 11.8 | **CRM Workflow** | Complete flow: Lead -> Customer -> Ticket -> Task | `admin-entity-suite.spec.ts`: complete CRM workflow: Lead -> Customer -> Task | -- | -- | -- | **partial** | Only E2E; no integration test for the full flow. The E2E title covers Lead -> Customer -> Task, not tickets |

---

## 12. Data Deletion & Privacy

Deleting a **user, customer or lead** is an erasure implementing GDPR Article 17, not a recoverable
soft delete. See [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md#deleting-personal-data) for the full
rationale. Implementation lives in `internal/repository/erasure.go` and `erasure_cascade.go`.

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 12.1 | **Personal fields overwritten** | Every personal column is overwritten in place before the row is soft-deleted | -- | -- | -- | `erasure_test.go`: TestUserErasureScrubsEveryPersonalFieldAndDisablesThePassword, TestUserErasureRemovesPersonalDataFromTheTable, TestCustomerErasureRemovesPersonalDataFromTheTable; `lead_erasure_test.go`: TestLeadErasureRemovesPersonalDataFromTheTable, TestLeadErasureKeepsTheNonPersonalBusinessFields | **covered** | -- |
| 12.2 | **Non-reversible placeholder email** | Replacement address comes from `crypto/rand` in the reserved `.invalid` domain and is not derived from the original | -- | -- | -- | `erasure_test.go`: TestUserErasureKeepsTheRowSoftDeletedWithAPlaceholderEmail, TestSuccessiveUserErasuresProduceDistinctPlaceholders, TestSuccessiveCustomerErasuresProduceDistinctPlaceholders | **covered** | -- |
| 12.3 | **Email becomes reusable** | The erased address can be registered again | -- | -- | -- | `erasure_test.go`: TestErasedUserEmailCanBeRegisteredAgain, TestErasedCustomerEmailCanBeUsedAgain; `bulk_erasure_test.go`: TestBulkDeletedUserEmailCanBeRegisteredAgain | **covered** | -- |
| 12.4 | **Credentials purged** | API keys and refresh tokens are hard-deleted with the account | -- | -- | -- | `erasure_test.go`: TestUserErasureDestroysCredentialsThatWouldOutliveTheAccount, TestUserErasureFailsAndRollsBackWhenCredentialsCannotBePurged | **covered** | -- |
| 12.5 | **Atomicity** | Scrub and soft delete happen in one transaction; a failure leaves no half-erased live record | -- | -- | -- | `erasure_atomicity_test.go`: 11 tests, incl. TestUserErasureRollsBackEverythingWhenTheSoftDeleteFails and TestAUserErasureCanBeRetriedAfterAFailedAttempt | **covered** | -- |
| 12.6 | **Conversion cascade** | A converted lead and its customer erase each other via `leads.customer_id`, in both directions | -- | -- | -- | `lead_erasure_test.go`: TestErasingAConvertedLeadAlsoErasesTheCustomerItBecame, TestErasingAConvertedCustomerAlsoErasesTheOriginatingLead, TestErasingACustomerErasesEveryLeadConvertedIntoIt, TestErasureDoesNotReachPeopleWhoAreNotLinked | **covered** | Wired in `cmd/main.go` via `NewCustomerRepositoryWithLeadErasure` |
| 12.7 | **Bulk isolation** | Each bulk item gets its own SAVEPOINT, so a failing item neither commits a half-erased live record nor rolls back the batch | -- | -- | -- | `bulk_erasure_isolation_test.go`: 4 tests; `erasure_atomicity_test.go`: TestBulkUserErasureRollsBackTheFailingItemOnly, TestBulkCustomerErasureRollsBackTheFailingItemOnly, TestBulkLeadErasureRollsBackTheWholeCascadeOfAFailingItem | **covered** | Refuses to run when GORM's nested transactions are disabled |
| 12.8 | **Cross-table PII sweep** | No personal data of an erased person survives in any migrated table | -- | -- | -- | `erasure_pii_sweep_test.go`: 8 tests, incl. TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates | **covered** | -- |
| 12.9 | **Business records survive** | Tickets and tasks referencing the erased person still resolve | -- | -- | -- | `erasure_test.go`: TestUserErasureDoesNotCascadeAwayBusinessRecords, TestCustomerErasureDoesNotCascadeAwayTickets | **covered** | -- |
| 12.10 | **Deactivation stays reversible** | `is_active = false` suspends access without touching personal data | -- | -- | -- | `erasure_test.go`: TestUserDeactivationDoesNotAnonymiseAnything; `erasure_pii_sweep_test.go`: TestDeactivationLeavesThePersonalDataWhereItIs | **covered** | No handler unit test for the toggle itself (see 7.5) |
| 12.11 | **Legacy remediation** | `scripts/anonymize_legacy_deleted_pii.sql` scrubs rows soft-deleted before erasure existed | -- | -- | -- | -- | **untested** | Manual, irreversible, deliberately not wired into auto-migration. No automated test exercises the script |
| 12.12 | **Logs and issued tokens** | Erasure does not reach application logs or already-issued JWTs | -- | -- | -- | -- | **gap** | Logs record the email on login and on customer create/update; issued JWTs embed it until expiry. Log retention needs its own policy — this is an operational gap, not a code defect |

---

## Gap Summary

### Resolved by this work

| # | Gap | Impact | Resolution |
|---|-----|--------|------------|
| ~~G1--G10~~ | ~~Search / sort / pagination non-functional across entities~~ | ~~Lists unusable~~ | **FIXED** earlier, in PR #4 and PR #9 |
| ~~G24~~ | ~~Auth service tests don't compile~~ | ~~Package would not build~~ | **FIXED** — `internal/service` builds and passes |
| ~~G25~~ | ~~Lead service tests don't compile~~ | ~~Package would not build~~ | **FIXED** — `internal/service` builds and passes |
| ~~G26~~ | ~~Registration accepted a client-supplied role~~ | ~~Anyone could mint an admin account and receive a valid token~~ | **FIXED** — `auth_handler.go` hard-codes `RoleCustomer`; `auth_handler_test.go` pins it |
| ~~G27~~ | ~~`GET /customers/:id/tickets` had no ownership check~~ | ~~Any authenticated user could read another customer's tickets~~ | **FIXED** — ownership check plus 4 regression tests |
| ~~G28~~ | ~~`?limit=0` panicked the pagination arithmetic~~ | ~~Every list endpoint returned 500 via one query parameter~~ | **FIXED** — `TestParseOffsetLimit_NeverReturnsZeroLimit` |
| ~~G29~~ | ~~Bulk create asserted a slice pointer to a slice~~ | ~~All bulk create paths panicked; rows never inserted were reported as successful~~ | **FIXED** — `bulk_operation_persistence_test.go` |
| ~~G30~~ | ~~Editing a task 403'd for non-admins echoing the current assignee~~ | ~~Non-admins could not edit their own tasks~~ | **FIXED** — `TestUpdateTask_NonAdminSameAssignee_Success` |
| ~~G31~~ | ~~Admin routes defined both `element` and `lazy`~~ | ~~Users and Configuration pages were unreachable~~ | **FIXED** — `routes/index.test.tsx` pins the invariant |
| ~~G32~~ | ~~Deletion retained name, email and phone indefinitely~~ | ~~Retention, not erasure; the email address stayed permanently unusable~~ | **FIXED** — section 12 |

### Medium priority (missing test coverage for working features)

| # | Gap | Suggested Action |
|---|-----|------------------|
| G11 | No E2E test for logout | Add to `login.spec.ts` |
| G12 | No token refresh at all | Implement `RefreshAccessToken`, then test it |
| G13 | No E2E tests for API key management | Create `admin-apikeys.spec.ts` |
| G14 | No E2E tests for configuration settings | Create `admin-configurations.spec.ts` |
| G15 | Dashboard stats not tested for correctness | Add a `dashboard_handler_test.go` and assert values against the DB |
| G16 | Bulk operations have no HTTP route | Decide: register the routes and test them, or delete the handler |
| G17 | Lead classification filter has no E2E test | Add to `admin-leads.spec.ts` |
| G18 | No E2E test for "My Tasks" / "My Tickets" views | Add to existing admin specs |
| G19 | No E2E test for user profile (My Profile) | Create `profile.spec.ts` |
| G20 | No E2E test for deleting a user, nor for activate/deactivate | Add to `admin-users.spec.ts` (7.4, 7.5) |
| G33 | No frontend unit tests for `TaskList` / `TaskForm` / `UserList` | Mirror the ticket test files |
| G34 | Status/priority/role filters are client-side only | Either add backend query params, or document the list pages as page-local filters |

### Low priority

| # | Gap | Suggested Action |
|---|-----|------------------|
| G21 | No sort E2E tests for columns other than Created | Extend `leads-sorting-search.spec.ts` |
| G22 | Rate limiting has no E2E or integration test | Add an integration test |
| G23 | CSRF rejection not tested at route level | Add a test that omits the CSRF token |
| G35 | CSRF middleware exists but is never installed | Either wire `middleware.CSRF` into `cmd/main.go` and add a rejection test, or remove it |
| G37 | `RateLimitGenerous` is dead code | Apply it to read routes, or delete it and stop documenting three tiers |
| G36 | ESLint reports 40 errors / 137 warnings | Mostly unused Playwright fixture args and `any`; untouched by the privacy work |

---

## Test File Reference

Counts are mechanical (see the note at the top of this document) and were taken from the working
tree, not from a test run.

### E2E Tests (Playwright) -- `gocrm-ui/e2e/tests/`

| File | Tests | Entities |
|------|-------|----------|
| `registration.spec.ts` | 15 | Auth |
| `login.spec.ts` | 11 | Auth |
| `admin-leads.spec.ts` | 11 | Leads |
| `admin-customers.spec.ts` | 10 | Customers |
| `admin-tickets.spec.ts` | 14 | Tickets |
| `admin-tasks.spec.ts` | 13 | Tasks |
| `admin-users.spec.ts` | 12 | Users |
| `admin-entity-suite.spec.ts` | 6 | Cross-entity |
| `leads-sorting-search.spec.ts` | 8 | Leads |
| **Total** | **100** | |

`debug-avatar.spec.ts` no longer exists. The admin account these suites log in as is provisioned by
`gocrm-ui/e2e/global-setup.ts`, which shells out to `cmd/create-admin` — self-service registration
can no longer create an admin. Note that `gocrm-ui/e2e/README.md` still says 69 tests; `playwright
test --list` reports 100.

### Backend Unit Tests -- `internal/`

| File | Tests | Layer |
|------|-------|-------|
| `handler/lead_handler_test.go` | 24 | Handler |
| `handler/ticket_handler_test.go` | 28 | Handler |
| `handler/task_handler_test.go` | 25 | Handler |
| `handler/customer_handler_test.go` | 19 | Handler |
| `handler/user_handler_test.go` | 16 | Handler |
| `handler/apikey_handler_test.go` | 7 | Handler |
| `handler/auth_handler_test.go` | 5 | Handler |
| `service/lead_service_test.go` | 20 | Service |
| `service/task_service_test.go` | 19 | Service |
| `service/customer_service_test.go` | 14 | Service |
| `service/ticket_service_test.go` | 14 | Service |
| `service/user_service_test.go` | 14 | Service |
| `service/bulk_operation_persistence_test.go` | 10 | Service |
| `service/csrf_test.go` | 6 | Service |
| `service/apikey_service_test.go` | 5 | Service |
| `service/auth_service_cookie_test.go` | 5 | Service |
| `service/bulk_operation_service_test.go` | 4 | Service |
| `service/auth_service_test.go` | 4 (16 subtests) | Service |
| `middleware/rate_limit_test.go` | 13 | Middleware |
| `middleware/auth_test.go` | 12 | Middleware |
| `middleware/error_handler_test.go` | 6 | Middleware |
| `middleware/recovery_test.go` | 4 | Middleware |
| `middleware/request_id_test.go` | 4 | Middleware |
| `middleware/logger_test.go` | 3 | Middleware |
| `utils/response_test.go` | 27 | Utility |
| `utils/context_test.go` | 11 | Utility |
| `utils/sort_test.go` | 11 | Utility |
| `utils/cookie_test.go` | 9 | Utility |
| `utils/transaction_test.go` | 7 | Utility |
| `utils/crypto_test.go` | 5 | Utility |
| `utils/password_test.go` | 1 (8 cases) | Utility |
| `config/config_test.go` | 13 | Config |
| `models/database_test.go` | 2 | Models |

### Integration Tests -- `test/integration/`, `tests/` and `internal/integration/`

| File | Tests | Scope |
|------|-------|-------|
| `test/integration/erasure_test.go` | 27 | Erasure (users, customers) |
| `test/integration/lead_erasure_test.go` | 14 | Erasure (leads, conversion cascade) |
| `test/integration/erasure_atomicity_test.go` | 11 | Erasure atomicity and rollback |
| `test/integration/erasure_pii_sweep_test.go` | 8 | Cross-table PII sweep |
| `test/integration/bulk_erasure_test.go` | 8 | Bulk erasure |
| `test/integration/bulk_erasure_isolation_test.go` | 4 | Bulk savepoint isolation |
| `test/integration/soft_delete_email_reuse_test.go` | 9 | Duplicate-email semantics, 409s |
| `test/integration/user_test.go` | 7 | Users |
| `test/integration/ticket_test.go` | 6 | Tickets |
| `test/integration/error_handling_test.go` | 6 | Errors |
| `test/integration/lead_conversion_transaction_test.go` | 2 | Lead TX |
| `test/integration/user_registration_transaction_test.go` | 2 | User TX |
| `internal/integration/task_integration_test.go` | 7 | Tasks |
| `tests/customer_integration_test.go` | 17 | Customers |
| `tests/lead_integration_test.go` | 13 | Leads |
| `tests/configuration_integration_test.go` | 13 | Config |
| `tests/apikey_integration_test.go` | 4 | API keys |
| `tests/auth_integration_test.go` | 3 | Auth |

The task integration suite lives under `internal/integration/`, not `test/integration/` — the
previous version of this document listed it in the wrong directory.

### Frontend Unit Tests -- `gocrm-ui/src/`

| File | Tests | Component |
|------|-------|-----------|
| `pages/tickets/TicketList.test.tsx` | 19 | TicketList |
| `pages/tickets/TicketForm.test.tsx` | 18 | TicketForm |
| `api/client.test.ts` | 14 | API client / token storage |
| `components/ConfirmDialog.test.tsx` | 11 | ConfirmDialog |
| `components/Breadcrumbs.test.tsx` | 9 | Breadcrumbs |
| `pages/leads/LeadList.test.tsx` | 9 | LeadList |
| `components/DataTable.test.tsx` | 8 | DataTable |
| `components/ProtectedRoute.test.tsx` | 7 | ProtectedRoute |
| `contexts/AuthContext.test.tsx` | 7 | AuthContext |
| `hooks/useSnackbar.test.tsx` | 7 | useSnackbar |
| `pages/auth/Register.validation.test.tsx` | 7 | Register validation |
| `pages/customers/CustomerList.test.tsx` | 7 | CustomerList |
| `components/form/FormTextField.test.tsx` | 6 | FormTextField |
| `pages/leads/LeadForm.test.tsx` | 6 | LeadForm |
| `components/Loading.test.tsx` | 4 | Loading |
| `routes/index.test.tsx` | 3 | Route table |
| **Total** | **142** | 16 files |
