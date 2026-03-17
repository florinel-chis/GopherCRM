# GopherCRM Feature Documentation & Test Coverage

> **Last updated:** 2026-03-17 (updated after PR #9)
> **Purpose:** Track all user-facing features, their test coverage, known gaps, and issues.
> **Convention:** Each feature is described from the user's perspective. Backend and frontend are on the same row.

## How to Read This Document

| Column | Meaning |
|--------|---------|
| **Feature** | What the user can do |
| **Description** | How it works end-to-end |
| **E2E Tests** | Playwright tests that exercise the full stack |
| **Unit Tests** | Go handler/service tests + frontend component tests |
| **Integration Tests** | Go tests that hit a real database |
| **Status** | `covered` / `partial` / `gap` / `untested` |
| **Known Issues** | Bugs, limitations, missing scenarios |

Status meanings:
- **covered** -- happy path and key edge cases are tested
- **partial** -- happy path tested, but significant scenarios missing
- **gap** -- feature exists but important test coverage is missing
- **untested** -- no automated tests exist

---

## 1. Authentication & Session Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 1.1 | **User Registration** | User fills form (name, email, password), account created, redirected to dashboard | `registration.spec.ts`: 15 tests (valid registration, validation errors, email format, password strength, duplicate email, network error, Enter key submit, loading state, visibility toggle) | `auth_service_test.go`: Login tests only | `AuthContext.test.tsx`: handles registration | `auth_integration_test.go`: TestRegisterEndpoint; `auth_cookie_integration_test.go`: TestRegisterWithCookies | **covered** | -- |
| 1.2 | **User Login** | User enters email/password, gets JWT cookie, redirected to dashboard | `registration.spec.ts`: can navigate to login page | `auth_service_test.go`: TestLogin (success, invalid email, wrong password, inactive user) | `AuthContext.test.tsx`: handles login successfully | `auth_integration_test.go`: TestLoginEndpoint; `auth_cookie_integration_test.go`: TestLoginWithCookies | **covered** | -- |
| 1.3 | **Session Refresh** | Access token auto-refreshes via refresh token cookie | -- | `auth_service_cookie_test.go`: TestRefreshAccessToken (success, invalid token, inactive user) | -- | `auth_cookie_integration_test.go`: TestRefreshToken | **partial** | No E2E test for token expiry/refresh flow |
| 1.4 | **Logout** | User clicks logout, cookies cleared, redirected to login | -- | `auth_service_cookie_test.go`: TestInvalidateRefreshToken | `AuthContext.test.tsx`: handles logout | `auth_cookie_integration_test.go`: TestLogout | **partial** | No E2E logout test |
| 1.5 | **CSRF Protection** | State-changing requests require CSRF token | -- | `auth_service_cookie_test.go`: TestGenerateCSRFToken, TestValidateCSRFToken | -- | `auth_cookie_integration_test.go`: TestCSRFTokenEndpoint | **partial** | No E2E test verifying CSRF rejection |
| 1.6 | **API Key Auth** | CLI tools authenticate via `Authorization: ApiKey gcrm_xxx` header | -- | `auth_service_test.go`: TestValidateAPIKey (valid, invalid, expired) | -- | `apikey_integration_test.go`: TestAPIKeyAuthentication | **covered** | -- |

---

## 2. Dashboard

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 2.1 | **Dashboard Stats** | Shows total leads, customers, open tickets, pending tasks, conversion rate | `admin-entity-suite.spec.ts`: navigation includes dashboard | -- | -- | -- | **gap** | No tests verify stat values are correct; no backend unit tests for DashboardHandler |
| 2.2 | **Quick Actions** | Dashboard has buttons to create leads, customers, etc. | -- | -- | -- | -- | **untested** | No tests for quick action navigation |

---

## 3. Lead Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 3.1 | **View Leads List** | Table shows leads with company, contact, email, phone, status, classification, source, created date | `admin-leads.spec.ts`: admin can view leads list; `leads-sorting-search.spec.ts`: should load leads page with data | `lead_handler_test.go`: TestList_AdminViewsAll, TestList_SalesViewsOwn | `LeadList.test.tsx`: renders lead list with data, shows loading state | `lead_integration_test.go`: TestListLeads_AdminSeesAll, TestListLeads_SalesSeesOnlyOwn | **covered** | -- |
| 3.2 | **Create Lead** | Form with company, contact, email, phone, source, status, notes; admin must specify owner | `admin-leads.spec.ts`: create new lead, create with all optional fields, validation errors | `lead_handler_test.go`: TestCreate_Success, TestCreate_SalesUserWithOwnerID, TestCreate_AdminRequiresOwnerID | `LeadForm.test.tsx`: renders create form, submits with valid data, prevents invalid | `lead_integration_test.go`: TestCreateLead_AdminSuccess, TestCreateLead_SalesSuccess, TestCreateLead_SalesCannotAssignToOthers | **covered** | -- |
| 3.3 | **Edit Lead** | Update any lead field; sales can only edit own leads | `admin-leads.spec.ts`: edit existing lead | `lead_handler_test.go`: TestUpdate_Success, TestUpdate_SalesUserReassign | `LeadForm.test.tsx`: renders edit form, updates lead | `lead_integration_test.go`: TestUpdateLead_Success | **covered** | -- |
| 3.4 | **View Lead Detail** | Navigate to lead detail page | `admin-leads.spec.ts`: view lead details | `lead_handler_test.go`: TestGet_Success, TestGet_SalesUserForbidden | -- | `lead_integration_test.go`: TestGetLead_AdminSuccess, TestGetLead_SalesCannotAccessOthers | **covered** | -- |
| 3.5 | **Delete Lead** | Delete with confirmation dialog; sales can only delete own | `admin-leads.spec.ts`: delete a lead | `lead_handler_test.go`: TestDelete_Success | `LeadList.test.tsx`: handles lead deletion | `lead_integration_test.go`: TestDeleteLead_Success | **covered** | -- |
| 3.6 | **Search Leads** | Type in search box, filters across name, email, company, phone, notes | `admin-leads.spec.ts`: search leads; `leads-sorting-search.spec.ts`: search by email, by company, clear search, no results | `lead_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort | `LeadList.test.tsx`: filters leads by search term | -- | **covered** | Search was non-functional until fix in PR #4 |
| 3.7 | **Sort Leads** | Click column headers to sort asc/desc | `leads-sorting-search.spec.ts`: sort by Created desc, toggle sort order, search and sort together | `lead_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByCreatedAtAsc, TestList_SortByInvalidColumn, TestList_SortByInvalidOrder, TestList_SortWithPagination | -- | -- | **partial** | Sorting was non-functional until fix in PR #4. No integration test. Only `created_at` sort tested E2E; other columns untested. |
| 3.8 | **Filter by Status** | Dropdown to filter leads by status (new, contacted, qualified, etc.) | `admin-leads.spec.ts`: filter leads by status | -- | `LeadList.test.tsx`: filters leads by status | -- | **partial** | No backend unit test for status filter; handler uses classification filter but not status filter from query params |
| 3.9 | **Filter by Classification** | Dropdown to filter by classification (hot_lead, lead, spam, etc.) | -- | -- | -- | -- | **gap** | Frontend has the dropdown but no E2E test covers it. Backend handler supports `classification` query param but no unit test. |
| 3.10 | **Convert Lead to Customer** | Convert a qualified lead into a customer record | -- | `lead_handler_test.go`: TestConvertToCustomer_Success, TestConvertToCustomer_AlreadyConverted, TestConvertToCustomer_SalesUserForbidden | `LeadList.test.tsx`: handles lead conversion for qualified leads | `lead_integration_test.go`: TestConvertToCustomer_Success, TestConvertToCustomer_AlreadyConverted, TestConvertToCustomer_SalesUserForbidden; `lead_conversion_transaction_test.go`: transaction tests | **covered** | -- |
| 3.11 | **Pagination** | Navigate between pages of leads | -- | `lead_handler_test.go`: TestList_SortWithPagination | `LeadList.test.tsx`: handles pagination | -- | **partial** | Page-based pagination was broken until fix in PR #4 (handler only read `offset`, not `page`). No dedicated E2E pagination test. |

---

## 4. Customer Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 4.1 | **View Customers List** | Table showing customers with name, email, company, etc. | `admin-customers.spec.ts`: view customers list | `customer_handler_test.go`: TestList_Success, TestList_WithPagination | `CustomerList.test.tsx`: renders list, displays status | `customer_integration_test.go`: TestListCustomers (Admin, Sales, Support, CustomerRole) | **covered** | -- |
| 4.2 | **Create Customer** | Form with name, email, phone, company, full address | `admin-customers.spec.ts`: create customer, with full address, minimal data, validation errors | `customer_handler_test.go`: TestCreate_Success, TestCreate_DuplicateEmail, TestCreate_ForbiddenForSupportUser | -- | `customer_integration_test.go`: TestCreateCustomer (Admin, Sales, DuplicateEmail, SupportForbidden) | **covered** | -- |
| 4.3 | **Edit Customer** | Update customer fields | `admin-customers.spec.ts`: edit existing customer | `customer_handler_test.go`: TestUpdate_Success, TestUpdate_DuplicateEmail | -- | `customer_integration_test.go`: TestUpdateCustomer_AdminSuccess, TestUpdateCustomer_DuplicateEmail, TestUpdateCustomer_SupportUserForbidden | **covered** | -- |
| 4.4 | **Delete Customer** | Delete with confirmation | `admin-customers.spec.ts`: delete customer | `customer_handler_test.go`: TestDelete_Success, TestDelete_ForbiddenForNonAdmin | `CustomerList.test.tsx`: handles deletion | `customer_integration_test.go`: TestDeleteCustomer_AdminSuccess, TestDeleteCustomer_SalesUserForbidden | **covered** | -- |
| 4.5 | **Search Customers** | Search across customer fields | `admin-customers.spec.ts`: search customers | `customer_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort | `CustomerList.test.tsx`: filters by search term | -- | **covered** | Fixed in PR #9. Backend search across first_name, last_name, email, company, phone, notes. |
| 4.6 | **Sort Customers** | Click column headers to sort | -- | `customer_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | Fixed in PR #9. Backend sort + frontend onSort wired. No E2E test yet. |
| 4.7 | **Pagination** | Navigate customer pages | -- | `customer_handler_test.go`: TestList_WithPagination | -- | `customer_integration_test.go`: TestPagination | **covered** | Fixed in PR #9: handler now reads `page` param. |

---

## 5. Ticket Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 5.1 | **View Tickets List** | Table with subject, status, priority, customer, assignee | `admin-tickets.spec.ts`: view tickets list | `ticket_handler_test.go`: TestList_Success, TestList_CustomerRole_Forbidden | `TicketList.test.tsx`: 19 tests (render, status/priority chips, filters, pagination, empty state, errors) | `ticket_test.go`: TestTicketLifecycle | **covered** | -- |
| 5.2 | **Create Ticket** | Form with subject, description, priority, customer, assignee | `admin-tickets.spec.ts`: create ticket, with all fields, validation errors | `ticket_handler_test.go`: TestCreate_Success, TestCreate_ValidationError, TestCreate_CustomerRole_Forbidden | `TicketForm.test.tsx`: 17 tests (create, validate, assign, pre-select customer, error handling) | `ticket_test.go`: TestTicketLifecycle | **covered** | -- |
| 5.3 | **Edit Ticket** | Update ticket fields including status transitions | `admin-tickets.spec.ts`: edit ticket, update status, update priority | `ticket_handler_test.go`: TestUpdate_Success_Admin, TestUpdate_Support_NotAssigned_Forbidden, TestUpdate_Support_Assigned_Success | `TicketForm.test.tsx`: edit form, update data, change agent, unassign | `ticket_test.go`: TestTicketStatusTransitions | **covered** | -- |
| 5.4 | **Delete Ticket** | Admin-only deletion | `admin-tickets.spec.ts`: delete ticket | `ticket_handler_test.go`: TestDelete_Success_Admin, TestDelete_NonAdmin_Forbidden, TestDelete_ServiceError | `TicketList.test.tsx`: handles deletion, cancel deletion | -- | **covered** | -- |
| 5.5 | **Filter by Status/Priority** | Dropdown filters for status and priority | `admin-tickets.spec.ts`: filter by status, filter by priority | -- | `TicketList.test.tsx`: filters by status, by priority, clears filters, combines filters | -- | **partial** | Backend does not support status/priority query param filtering. Filtering is likely client-side only (current page). |
| 5.6 | **Search Tickets** | Search across ticket fields | `admin-tickets.spec.ts`: search tickets | `ticket_handler_test.go`: TestList_SearchByTitle, TestList_SearchWithSort | `TicketList.test.tsx`: filters by search term | -- | **covered** | Fixed in PR #9. Backend search across title, description, resolution. |
| 5.7 | **Sort Tickets** | Click column headers to sort | -- | `ticket_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | Fixed in PR #9. Backend sort + frontend onSort wired. No E2E test yet. |
| 5.8 | **My Tickets** | Support users see their assigned tickets | -- | `ticket_handler_test.go`: TestListMyTickets_Success, TestListMyTickets_CustomerRole_Forbidden | -- | `ticket_test.go`: TestListMyTickets | **partial** | No E2E test |
| 5.9 | **Tickets by Customer** | View tickets for a specific customer | -- | `ticket_handler_test.go`: TestListByCustomer_Success | -- | `ticket_test.go`: TestListByCustomer | **partial** | No E2E test |

---

## 6. Task Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 6.1 | **View Tasks List** | Table with title, status, priority, assignee, due date | `admin-tasks.spec.ts`: view tasks list | `task_handler_test.go`: TestListTasks_Success, TestListTasks_NonAdminGetsOwnTasks | -- | `task_integration_test.go`: TestTaskLifecycle | **covered** | -- |
| 6.2 | **Create Task** | Form with title, description, priority, due date, assignee, related lead/customer | `admin-tasks.spec.ts`: create task, minimal data, different priorities | `task_handler_test.go`: TestCreateTask_Success, TestCreateTask_NonAdminAssignToOther_Forbidden, TestCreateTask_ValidationError | -- | `task_integration_test.go`: TestTaskCreationPermissions, TestTaskValidation | **covered** | -- |
| 6.3 | **Edit Task** | Update task fields; mark as complete | `admin-tasks.spec.ts`: edit task, mark complete, status changes | `task_handler_test.go`: TestUpdateTask_Success, TestUpdateTask_NonAdminReassign_Forbidden, TestUpdateTask_NonAdminAccessOthersTask_Forbidden | -- | `task_integration_test.go`: TestTaskLifecycle | **covered** | -- |
| 6.4 | **Delete Task** | Admin-only deletion | `admin-tasks.spec.ts`: delete task | `task_handler_test.go`: TestDeleteTask_Success, TestDeleteTask_NonAdminForbidden | -- | -- | **covered** | -- |
| 6.5 | **Filter by Status/Priority** | Dropdown filters | `admin-tasks.spec.ts`: filter by status, filter by priority | -- | -- | -- | **partial** | Backend likely does not support these query params; client-side filtering only |
| 6.6 | **Search Tasks** | Search across task fields | `admin-tasks.spec.ts`: search tasks | `task_handler_test.go`: TestListTasks_SearchByTitle, TestListTasks_SearchWithSort | -- | -- | **covered** | Fixed in PR #9. Backend search across title, description. |
| 6.7 | **Sort Tasks** | Click column headers to sort | -- | `task_handler_test.go`: TestListTasks_SortByCreatedAtDesc, TestListTasks_SortByInvalidColumn | -- | -- | **partial** | Fixed in PR #9. Backend sort + frontend onSort wired. No E2E test yet. |
| 6.8 | **Due Date Management** | Set and validate due dates | `admin-tasks.spec.ts`: manage due dates, date validation | -- | -- | `task_integration_test.go`: TestTaskWithDueDate | **partial** | -- |
| 6.9 | **My Tasks** | Non-admin users see their assigned tasks | -- | `task_handler_test.go`: TestMyTasks_ParsePaginationSuccess | -- | `task_integration_test.go`: TestMyTasks | **partial** | No E2E test |

---

## 7. User Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 7.1 | **View Users List** | Admin sees table of all users with role and status | `admin-users.spec.ts`: view users list | `user_handler_test.go`: TestList_Success | -- | `user_test.go`: TestUserCRUD | **covered** | -- |
| 7.2 | **Create User** | Admin creates users with role assignment | `admin-users.spec.ts`: create user, different roles, validation errors, password mismatch, duplicate email | `user_handler_test.go`: TestCreate_Success, TestCreate_EmailConflict | -- | `user_test.go`: TestUserRegistration, TestEmailUniqueness | **covered** | -- |
| 7.3 | **Edit User** | Update user profile, role, active status | `admin-users.spec.ts`: edit user | `user_handler_test.go`: TestUpdate_Success | -- | `user_test.go`: TestUserCRUD | **covered** | -- |
| 7.4 | **Delete User** | Admin can delete users but not themselves | `admin-users.spec.ts`: cannot delete self | `user_handler_test.go`: TestDelete_Success, TestDelete_SelfDeletion | -- | -- | **covered** | -- |
| 7.5 | **Activate/Deactivate** | Toggle user active status | `admin-users.spec.ts`: deactivate and activate users | -- | -- | -- | **partial** | No backend unit test for the toggle action specifically |
| 7.6 | **Search Users** | Search across user fields | `admin-users.spec.ts`: search users | `user_handler_test.go`: TestList_SearchByEmail, TestList_SearchWithSort | -- | -- | **covered** | Fixed in PR #9. Backend search across email, first_name, last_name. |
| 7.7 | **Sort Users** | Click column headers to sort | -- | `user_handler_test.go`: TestList_SortByCreatedAtDesc, TestList_SortByInvalidColumn | -- | -- | **partial** | Fixed in PR #9. Backend sort + frontend onSort wired. No E2E test yet. |
| 7.8 | **Filter by Role** | Dropdown to filter by role | `admin-users.spec.ts`: filter by role | -- | -- | -- | **gap** | Backend supports `role` param but no unit test |
| 7.9 | **My Profile** | User can view/edit own profile | -- | `user_handler_test.go`: TestGetMe_Success, TestUpdateMe_Success | -- | `user_test.go`: TestMeEndpoints | **partial** | No E2E test |

---

## 8. API Key Management

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 8.1 | **Generate API Key** | User creates a named API key for CLI access | -- | `apikey_service_test.go`: TestGenerate, TestGenerate_CreateError | -- | `apikey_integration_test.go`: TestCreateAPIKey | **partial** | No E2E test; no handler-level unit test |
| 8.2 | **List API Keys** | View all keys (masked) with last-used info | -- | `apikey_service_test.go`: TestGetByUser, TestList | -- | `apikey_integration_test.go`: TestListAPIKeys | **partial** | No E2E test |
| 8.3 | **Revoke API Key** | Delete/revoke an API key | -- | `apikey_service_test.go`: TestRevoke (success, unauthorized, not found) | -- | `apikey_integration_test.go`: TestRevokeAPIKey | **partial** | No E2E test |

---

## 9. Configuration Settings

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 9.1 | **View Configurations** | Admin sees all system configurations | -- | -- | -- | `configuration_integration_test.go`: TestGetAllConfigurations_AdminOnly, TestGetUIConfigurations, TestGetConfigurationByCategory, TestGetConfigurationByKey | **partial** | No E2E test; no handler/service unit tests |
| 9.2 | **Update Configuration** | Admin can change configuration values | -- | -- | -- | `configuration_integration_test.go`: TestSetConfiguration, TestSetConfiguration_InvalidValue, TestSetConfiguration_ReadOnly | **partial** | No E2E test |
| 9.3 | **Reset Configuration** | Admin can reset to defaults | -- | -- | -- | `configuration_integration_test.go`: TestResetConfiguration | **partial** | No E2E test |

---

## 10. Bulk Operations

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 10.1 | **Bulk Create** | Create multiple records at once | -- | `bulk_operation_service_test.go`: TestValidateBulkRequest, TestCreateBulkOperation, TestProcessBulkCreate_InvalidResourceType, TestConvertMapToModel | -- | -- | **gap** | No E2E tests; no handler tests; no integration tests |
| 10.2 | **Bulk Update** | Update multiple records at once | -- | -- | -- | -- | **untested** | No tests at all |
| 10.3 | **Bulk Delete** | Delete multiple records at once | -- | -- | -- | -- | **untested** | No tests at all |
| 10.4 | **Bulk Actions** | Apply actions to multiple records | -- | -- | -- | -- | **untested** | No tests at all |

---

## 11. Cross-Cutting Concerns

| # | Feature | Description | E2E Tests | Unit Tests (Backend) | Unit Tests (Frontend) | Integration Tests | Status | Known Issues |
|---|---------|-------------|-----------|----------------------|-----------------------|-------------------|--------|--------------|
| 11.1 | **Role-Based Access** | Different roles see different data and have different permissions | `admin-users.spec.ts`: manage permissions through roles | Handler tests cover role checks | `AuthContext.test.tsx` | `auth_integration_test.go`: TestProtectedRoute; `user_test.go`: TestPermissionEnforcement | **covered** | -- |
| 11.2 | **Rate Limiting** | API requests are rate-limited per role | -- | `rate_limit_test.go`: 11 tests (public, auth, admin, headers, by IP/user/API key, forwarded IP, disabled, error, key generation, role detection) | -- | -- | **partial** | No E2E test; no integration test |
| 11.3 | **Request Logging** | All requests logged with structured JSON | -- | `logger_test.go`: 8 tests (success, query params, client/server error, errors, user agent, request ID, sensitive data) | -- | -- | **covered** | -- |
| 11.4 | **Error Handling** | Consistent error format across all endpoints | -- | -- | -- | `error_handling_test.go`: 6 tests (400, 401, 403, 404, 500, consistent format) | **partial** | No frontend error boundary tests |
| 11.5 | **CRM Workflow** | Complete flow: Lead -> Customer -> Ticket -> Task | `admin-entity-suite.spec.ts`: complete CRM workflow | -- | -- | -- | **partial** | Only E2E; no integration test for the full flow |

---

## Gap Summary

### High Priority (Features exist but have no/broken tests)

| # | Gap | Impact | Status | Resolution |
|---|-----|--------|--------|------------|
| ~~G1~~ | ~~Customer search is non-functional~~ | ~~Users cannot search customers~~ | **FIXED** | PR #9 (issue #5) |
| ~~G2~~ | ~~Customer sort is non-functional~~ | ~~Users cannot sort customer columns~~ | **FIXED** | PR #9 (issue #5) |
| ~~G3~~ | ~~Ticket search is non-functional~~ | ~~Users cannot search tickets~~ | **FIXED** | PR #9 (issue #6) |
| ~~G4~~ | ~~Ticket sort is non-functional~~ | ~~Users cannot sort ticket columns~~ | **FIXED** | PR #9 (issue #6) |
| ~~G5~~ | ~~Task search is non-functional~~ | ~~Users cannot search tasks~~ | **FIXED** | PR #9 (issue #7) |
| ~~G6~~ | ~~Task sort is non-functional~~ | ~~Users cannot sort task columns~~ | **FIXED** | PR #9 (issue #7) |
| ~~G7~~ | ~~User sort is non-functional~~ | ~~Users cannot sort user columns~~ | **FIXED** | PR #9 (issue #8) |
| ~~G8~~ | ~~Customer pagination broken~~ | ~~Page navigation may not work~~ | **FIXED** | PR #9 (issue #5) |
| ~~G9~~ | ~~Ticket pagination broken~~ | ~~Page navigation may not work~~ | **FIXED** | PR #9 (issue #6) |
| ~~G10~~ | ~~Task pagination broken~~ | ~~Page navigation may not work~~ | **FIXED** | PR #9 (issue #7) |

### Medium Priority (Missing test coverage for working features)

| # | Gap | Suggested Action |
|---|-----|------------------|
| G11 | No E2E test for logout flow | Add to registration.spec.ts or new auth.spec.ts |
| G12 | No E2E test for token refresh | Add test that waits for token expiry |
| G13 | No E2E tests for API key management | Create admin-apikeys.spec.ts |
| G14 | No E2E tests for configuration settings | Create admin-configurations.spec.ts |
| G15 | Dashboard stats not tested for correctness | Add tests that verify stat values match DB |
| G16 | Bulk operations largely untested | Add handler + integration tests |
| G17 | Lead classification filter has no tests | Add E2E + handler test |
| G18 | No E2E test for "My Tasks" / "My Tickets" views | Add to existing admin specs |
| G19 | No E2E test for user profile (My Profile) | Create profile.spec.ts |
| G20 | Lead status filter has no backend test | Add handler test for `status` query param -- **note: handler may not read this param** |

### Low Priority

| # | Gap | Suggested Action |
|---|-----|------------------|
| G21 | No sort E2E tests for columns other than Created | Extend leads-sorting-search.spec.ts |
| G22 | Rate limiting has no E2E or integration test | Add integration test |
| G23 | CSRF rejection not tested E2E | Add test that omits CSRF token |
| G24 | Auth service tests don't compile (outdated constructor) | Fix `auth_service_test.go` constructor args |
| G25 | Lead service tests don't compile (outdated constructor) | Already partially fixed in PR #4; finish fixing `auth_service_test.go` in same package |

---

## Test File Reference

### E2E Tests (Playwright) -- `gocrm-ui/e2e/tests/`

| File | Tests | Entities |
|------|-------|----------|
| `registration.spec.ts` | 15 | Auth |
| `admin-leads.spec.ts` | 12 | Leads |
| `admin-customers.spec.ts` | 12 | Customers |
| `admin-tickets.spec.ts` | 16 | Tickets |
| `admin-tasks.spec.ts` | 17 | Tasks |
| `admin-users.spec.ts` | 15 | Users |
| `admin-entity-suite.spec.ts` | 6 | Cross-entity |
| `leads-sorting-search.spec.ts` | 8 | Leads |
| `debug-avatar.spec.ts` | 1 | Debug |

### Backend Unit Tests -- `internal/`

| File | Tests | Layer |
|------|-------|-------|
| `handler/lead_handler_test.go` | 20 | Handler |
| `handler/customer_handler_test.go` | 15 | Handler |
| `handler/ticket_handler_test.go` | 20 | Handler |
| `handler/task_handler_test.go` | 22 | Handler |
| `handler/user_handler_test.go` | 14 | Handler |
| `service/lead_service_test.go` | 18 | Service |
| `service/user_service_test.go` | 11 | Service |
| `service/apikey_service_test.go` | 5 | Service |
| `service/auth_service_test.go` | ~10 | Service (broken) |
| `service/auth_service_cookie_test.go` | ~10 | Service |
| `service/task_service_test.go` | 19 | Service |
| `service/ticket_service_test.go` | 14 | Service |
| `service/customer_service_test.go` | 13 | Service |
| `service/bulk_operation_service_test.go` | 4 | Service |
| `middleware/logger_test.go` | 8 | Middleware |
| `middleware/rate_limit_test.go` | 11 | Middleware |
| `utils/cookie_test.go` | ~15 | Utility |
| `utils/transaction_test.go` | ~12 | Utility |

### Integration Tests -- `tests/` and `test/integration/`

| File | Tests | Scope |
|------|-------|-------|
| `tests/auth_integration_test.go` | 3 | Auth |
| `tests/auth_cookie_integration_test.go` | 6 | Auth cookies |
| `tests/apikey_integration_test.go` | 4 | API keys |
| `tests/lead_integration_test.go` | 13 | Leads |
| `tests/customer_integration_test.go` | 17 | Customers |
| `tests/configuration_integration_test.go` | 13 | Config |
| `test/integration/user_test.go` | 7 | Users |
| `test/integration/ticket_test.go` | 6 | Tickets |
| `test/integration/error_handling_test.go` | 6 | Errors |
| `test/integration/task_integration_test.go` | 7 | Tasks |
| `test/integration/configuration_transaction_test.go` | 1 | Config TX |
| `test/integration/lead_conversion_transaction_test.go` | 2 | Lead TX |
| `test/integration/user_registration_transaction_test.go` | 2 | User TX |

### Frontend Unit Tests -- `gocrm-ui/src/`

| File | Tests | Component |
|------|-------|-----------|
| `components/Loading.test.tsx` | 4 | Loading |
| `components/DataTable.test.tsx` | 8 | DataTable |
| `components/form/FormTextField.test.tsx` | 6 | FormTextField |
| `components/Breadcrumbs.test.tsx` | 9 | Breadcrumbs |
| `components/ConfirmDialog.test.tsx` | 11 | ConfirmDialog |
| `hooks/useSnackbar.test.tsx` | 7 | useSnackbar |
| `pages/leads/LeadForm.test.tsx` | 6 | LeadForm |
| `pages/leads/LeadList.test.tsx` | 9 | LeadList |
| `pages/customers/CustomerList.test.tsx` | 7 | CustomerList |
| `pages/tickets/TicketForm.test.tsx` | 17 | TicketForm |
| `pages/tickets/TicketList.test.tsx` | 19 | TicketList |
| `contexts/AuthContext.test.tsx` | 7 | AuthContext |
