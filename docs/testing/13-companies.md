# Companies — Test Cases

End-to-end cases for the companies area added 2026-09-29: the `/companies` list, the create/edit
form, the detail page with its customers and leads sections, deletion (a plain soft delete that
clears the links, not an erasure), search, sort and pagination, and the company link on the lead
and customer forms. Every **Expected** states what the build does **today**, traced to the
handlers, services and repository; the frontend pages named here are the ones the same change
introduces, so their selectors are stated where the backend contract fixes them and left generic
otherwise.

24 cases: 9 automated (8 by `gocrm-ui/e2e/tests/admin-companies.spec.ts`, 6 of them partial, plus
TC-COMP-024 by the forms suite), 15 planned (six of them the sales and support role cases, whose
accounts come from `APIClient.createUser` in `gocrm-ui/e2e/helpers/sso.ts`), 0 blocked.

**Sources**

- `internal/models/company.go`, `internal/models/lead.go`, `internal/models/customer.go`,
  `internal/models/database.go`
- `internal/handler/company_handler.go`, `internal/handler/lead_handler.go`,
  `internal/handler/customer_handler.go`, `internal/handler/routes.go` (`SetupCompanyRoutes`)
- `internal/service/company_service.go`, `internal/service/lead_service.go`,
  `internal/service/lead_company_lock.go`,
  `internal/service/customer_service.go`
- `internal/repository/company_repository.go`, `internal/utils/sort.go`
- `migrations/20260929120000_add_companies.{up,down}.sql`
- `gocrm-ui/src/pages/companies/CompanyList.tsx`, `CompanyForm.tsx`, `CompanyDetail.tsx`,
  `gocrm-ui/src/components/CompanyAutocomplete.tsx`, `gocrm-ui/src/api/endpoints/companies.ts`
- `gocrm-ui/e2e/tests/admin-companies.spec.ts`, `gocrm-ui/e2e/pages/companies.page.ts`
- `docs/FEATURES.md` section 4b; `docs/ROADMAP.md` (companies follow-ups)

**Constraints**

- **Companies hold no personal data and are not erased.** `DELETE /companies/{id}` soft-deletes
  the row after setting `company_id = NULL` on every lead and customer that pointed at it, in one
  transaction (`companyService.Delete`). The domain becomes free again at once, because
  uniqueness is checked among live rows only. A case may still only delete companies it created:
  the delete detaches other people's leads and customers.
- **Two company notions on leads and customers.** The free-text `company` column is untouched and
  remains the contract of the public forms; `company_id` is the curated link. Both are sent and
  shown side by side. Nothing in this change copies one into the other (backfill and domain-based
  auto-linking are ROADMAP items).
- **Domain normalisation happens server-side.** `https://WWW.Acme.Example/about` is stored as
  `acme.example` (`service.NormalizeCompanyDomain`). A duplicate is decided after normalisation,
  case-insensitively, among live companies, and answered with **409** on create and on update.
- **PUT replaces the text fields.** `CompanyRequest` is the same body for create and update; a
  text field absent from a PUT is stored empty. `owner_id` is the exception: absent keeps, `0`
  clears (admin only), another value sets. Sales users may only name themselves as owner.
- **Non-admin accounts come from the test.** Sales and support sessions need an admin-authenticated
  `POST /users`, which `APIClient.createUser` in `gocrm-ui/e2e/helpers/sso.ts` sends;
  `APIClient.login` signs the account in. Cases that need one are planned against
  `admin-companies.spec.ts`; the backend half of every such case is pinned by
  `CompanyHandlerTestSuite.TestRoleMatrix` (`internal/handler/company_handler_test.go`) and
  `CompanyIntegrationTestSuite.TestRoleMatrix` (`test/integration/company_integration_test.go`).
- **The moderate rate tier applies.** 120 req/min with a burst of 30 per IP; a spec that creates
  many companies in a tight loop can 429 itself.

---

## 13.1 List

### TC-COMP-001 — Load the companies list as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one company created by the test.
- **Steps:**
  1. Click "Companies" in the sidebar (between Customers and Tickets).
  2. Wait for `GET /api/v1/companies`.
- **Expected:** 200. The page heading reads "Companies" and a table renders with the company name,
  domain, industry, owner and the live customer and lead counts. The response body is
  `{success:true, data:[…], meta:{page, per_page, total, total_pages}}` — `data` is the array
  itself, unlike the `{customers:[…], total}` object of the customers list. Every item carries
  `customer_count` and `lead_count`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "admin can view the companies list page"

### TC-COMP-002 — Search matches name, domain, industry and city
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; three companies created by the test with distinct names,
  domains, industries and cities.
- **Steps:**
  1. Type a fragment of one company's industry into the search box; wait for the request.
  2. Repeat with a fragment of another company's city, then of a domain.
- **Expected:** Each request is `GET /api/v1/companies?search=<fragment>…` and returns only the
  matching company with `meta.total` 1. The search is a substring match over `name`, `domain`,
  `industry` and `city` (`companySearchClause`); it does not match notes, phone or address.
  On MySQL and MariaDB the match is case-insensitive (collation); on SQLite it is case-sensitive.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "admin can search companies" (searches by name and expects one row; the industry, city and
  domain halves are pinned in Go).
  Go: `TestCompanyRepository_ListSearchSortAndPaginate` (`internal/repository/company_repository_test.go`),
  `CompanyIntegrationTestSuite.TestListSearchSortAndPagination` (`test/integration/company_integration_test.go`).

### TC-COMP-003 — Sort by an allowed column; an unknown column is refused
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in; three companies created by the test.
- **Steps:**
  1. Click the Name column header; wait for `GET /api/v1/companies?…sort_by=name&sort_order=asc`.
  2. With a request context, call `GET /api/v1/companies?sort_by=owner_id`.
- **Expected:** Step 1: 200, rows in ascending name order. Step 2: **400** "Invalid sort column".
  The allowlist is `id, name, domain, industry, created_at, updated_at`
  (`utils.AllowedSortColumns["companies"]`); the default order is `created_at desc`. Unlike the
  customers list, which silently drops an unknown `sort_by`, this endpoint rejects it.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new).
  Go: `CompanyHandlerTestSuite.TestList_UnknownSortColumnIs400` (`internal/handler/company_handler_test.go`), `internal/utils/sort_test.go`.

### TC-COMP-004 — Pagination
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more companies than one page holds, all created by the
  test with a run-scoped name prefix used as the search term.
- **Steps:**
  1. Search for the prefix; move to page 2.
- **Expected:** `GET /api/v1/companies?…page=2&limit=<n>` returns the remainder; `meta.page` is 2,
  `meta.total` counts every match, `meta.total_pages` is `ceil(total/limit)`. `page` (1-based)
  overrides `offset` when supplied; `limit` is capped at 100 (`utils.ParseOffsetLimit`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new)

### TC-COMP-005 — Sales and support can read the list; customer role cannot
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user, a support user and a customer-role user.
- **Steps:**
  1. As each user, open `/companies` and observe `GET /api/v1/companies`.
- **Expected:** Sales and support: 200 with the same unfiltered set an admin sees. Customer role:
  **403** "Insufficient permissions" from the group-level `RequireRole(admin, sales, support)`;
  the sidebar item is hidden for that role and the SPA route redirects to `/unauthorized`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`) (customer half obtainable via `/register`). Go:
  `CompanyHandlerTestSuite.TestRoleMatrix` (`internal/handler/company_handler_test.go`),
  `CompanyIntegrationTestSuite.TestRoleMatrix` (`test/integration/company_integration_test.go`).

## 13.2 Create

### TC-COMP-006 — Create a company with every field
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Click "Add Company"; fill name, domain (`https://www.<run>.example/about`), website
     (`https://<run>.example`), industry, employee range `51-200`, phone, address, city, state,
     country, postal code, notes; submit.
- **Expected:** `POST /api/v1/companies` → **201**. The response `data` is the company with its
  `owner` preloaded (null when admin left the owner empty), `customer_count` 0, `lead_count` 0,
  and `domain` normalised to `<run>.example` — lower case, scheme, path and leading `www.`
  stripped. The list shows the new row; the detail page shows every field.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "admin can create a company with all fields and sees them on the detail page" (fills a plain
  domain, so the normalisation half is pinned in Go:
  `TestNormalizeCompanyDomain` (`internal/service/company_service_test.go`))

### TC-COMP-007 — Validation: name required, website must be http(s), employee range from the list
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Submit the form with an empty name.
  2. Fill the name, set website to `acme.example`, submit.
  3. With a request context, `POST /api/v1/companies` with `employee_range: "lots"`.
- **Expected:** Step 1 is stopped client-side by the zod schema (no request). Step 2: the client
  schema rejects it; if the request is forced, the API answers **400** "website must be an http or
  https URL" (`normalizeCompany`). Step 3: **400** from the binding tag
  `oneof=1-10 11-50 51-200 201-500 501-1000 1000+`. A blank or whitespace-only name sent directly
  is **400** "company name is required".
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "validation errors keep the form open" (steps 1 and 2, client-side; step 3 is Go only).
  Go: `CompanyHandlerTestSuite.TestCreate_BindingRejectsBadBodies` (`internal/handler/company_handler_test.go`),
  `CompanyServiceTestSuite.TestCreate_ValidationFailures` (`internal/service/company_service_test.go`).

### TC-COMP-008 — Values longer than their column are refused with 400
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. `POST /api/v1/companies` with a 201-character name; then with a 101-character city; then
     with notes of 65,536 bytes.
- **Expected:** **400** each time, the message naming the field and its limit ("name is too long
  (at most 200 characters)", "Notes are too long (at most 65535 bytes)"). Varchar columns are
  measured in characters, notes in bytes. The limits are `models.Company*MaxLength`, held to the
  declared column widths by `internal/models/company_column_limits_test.go`, so MySQL and MariaDB never see a
  value they would reject with a driver error (SQLite would accept it silently).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new).
  Go: `CompanyHandlerTestSuite.TestCreate_ValuesLongerThanTheirColumnAre400` (`internal/handler/company_handler_test.go`).

### TC-COMP-009 — Duplicate domain is 409 on create
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; a company with domain `<run>.example` created by the test.
- **Steps:**
  1. Create another company with domain `HTTPS://WWW.<RUN>.EXAMPLE/`; submit.
- **Expected:** `POST /api/v1/companies` → **409**, `error.code` `CONFLICT`, message
  `domain "<run>.example" is already used by another company: a company with this domain already
  exists`. The form stays open and shows the message. The check compares the stored, lower-cased
  domain with the lower-cased input among live rows (`companyRepository.ExistsByDomain`, served by
  the non-unique `idx_companies_domain`); there is deliberately no unique index, so a soft-deleted
  company does not reserve its domain.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "a duplicate domain is refused with the server message on the domain field" (sends the same
  domain, not the upper-case scheme variant; it asserts the 409 and that the domain field shows
  a server message, not the exact text).
  Go: `CompanyHandlerTestSuite.TestCreate_DuplicateDomainIs409` (`internal/handler/company_handler_test.go`),
  `CompanyIntegrationTestSuite.TestCreateNormalisesTheDomainAndRefusesDuplicates` (`test/integration/company_integration_test.go`).

### TC-COMP-010 — Unknown owner_id is 400 INVALID_REFERENCE
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin token.
- **Steps:**
  1. `POST /api/v1/companies` with `owner_id: 999999`.
- **Expected:** **400** with `error.code` `INVALID_REFERENCE` and message
  `unknown owner_id 999999: assignee not found`. A soft-deleted user is unknown too. The bad
  reference is in the body, so it is not a 404.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (request context).
  Go: `CompanyHandlerTestSuite.TestCreate_UnknownOwnerIsInvalidReference` (`internal/handler/company_handler_test.go`).

### TC-COMP-011 — Sales creates a company and becomes its owner; naming another owner is 403
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user logged in.
- **Steps:**
  1. Create a company without choosing an owner.
  2. `POST /api/v1/companies` with `owner_id` of another user.
- **Expected:** Step 1: 201 and `owner_id` is the sales user's own id. Step 2: **403** "You can
  only assign companies to yourself". Admins may name any live user or leave the owner empty.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`). Go:
  `CompanyHandlerTestSuite.TestCreate_SalesGetsItselfAsOwnerWhenNoneIsSent` (`internal/handler/company_handler_test.go`),
  `CompanyHandlerTestSuite.TestCreate_SalesMayNotAssignSomebodyElse` (`internal/handler/company_handler_test.go`);
  `CompanyIntegrationTestSuite.TestSalesOwnerRuleAndOwnLeadsOnly` (`test/integration/company_integration_test.go`).

### TC-COMP-012 — Support cannot create or update; customer role cannot reach the routes
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user and a customer-role user; a company created by the test.
- **Steps:**
  1. As support: `POST /api/v1/companies` and `PUT /api/v1/companies/{id}`.
  2. As customer role: `GET /api/v1/companies/{id}`.
- **Expected:** Step 1: **403** on both (`RequireRole(admin, sales)` on POST and PUT). Step 2:
  **403** (the per-route guard built from `RouteKeys`). The UI hides the Add and Edit buttons from support.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`). Go:
  `CompanyHandlerTestSuite.TestRoleMatrix` (`internal/handler/company_handler_test.go`) and
  `CompanyIntegrationTestSuite.TestRoleMatrix` (`test/integration/company_integration_test.go`).

## 13.3 Detail and edit

### TC-COMP-013 — Detail page shows the fields, the owner and the counts
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a company created by the test with an owner, one linked
  customer and one linked lead (TC-COMP-019, TC-COMP-020).
- **Steps:**
  1. Open the company's row.
- **Expected:** `GET /api/v1/companies/{id}` → 200 with every field, `owner` preloaded,
  `customer_count` 1 and `lead_count` 1 counted over live rows only (an erased customer no
  longer counts). An unknown id is **404** "Company not found"; a non-numeric id is **400**
  "Invalid company ID".
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new)

### TC-COMP-014 — Edit replaces the fields and keeps the owner
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a company created by the test with a website and notes.
- **Steps:**
  1. Open the edit form, change the name, clear the website, submit.
- **Expected:** `PUT /api/v1/companies/{id}` → 200. The name changes, the website is stored empty,
  `owner_id` is unchanged (the form did not send it), and the response is re-read with the owner
  and refreshed counts. PUT is a full replacement of the text fields: what the form leaves blank
  is cleared.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "admin can edit a company" (changes the industry and checks the detail page; clearing the
  website and the kept owner are pinned in Go and in `CompanyForm.test.tsx`).
  Go: `CompanyHandlerTestSuite.TestUpdate_ReplacesTheFieldsAndKeepsTheOwner` (`internal/handler/company_handler_test.go`).

### TC-COMP-015 — Duplicate domain is 409 on update; a company may keep its own domain
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; companies A (`a-<run>.example`) and B (`b-<run>.example`)
  created by the test.
- **Steps:**
  1. Edit B, set its domain to `A-<RUN>.example`, submit.
  2. Edit A, change only its name, submit.
- **Expected:** Step 1: **409** with the duplicate-domain message; B is unchanged. Step 2: 200 —
  the pre-check excludes the row being updated (`ExistsByDomain(domain, excludeID)`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new).
  Go: `CompanyServiceTestSuite.TestUpdate_ExcludesItselfFromTheDuplicateCheck` (`internal/service/company_service_test.go`),
  `CompanyServiceTestSuite.TestUpdate_DuplicateDomainIs409Sentinel` (`internal/service/company_service_test.go`).

### TC-COMP-016 — Owner rules on update
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A company created by the test with owner X; an admin and a sales user Y.
- **Steps:**
  1. As sales Y: `PUT` with `owner_id` of X; then with `owner_id: 0`; then with `owner_id` of Y.
  2. As admin: `PUT` with `owner_id: 0`; then with `owner_id: 999999`.
- **Expected:** Step 1: **403**, **403**, then 200 with Y as owner. Step 2: 200 with `owner_id`
  absent from the response (cleared), then **400** `INVALID_REFERENCE`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`); both halves through the request context. Go:
  `CompanyHandlerTestSuite.TestUpdate_OwnerRules` (`internal/handler/company_handler_test.go`).

## 13.4 Delete

### TC-COMP-017 — Admin deletes a company; its customers and leads are unlinked, not erased
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a company created by the test with one customer and one
  lead linked to it (both created by the test).
- **Steps:**
  1. Click the delete icon on the company's row; confirm.
  2. Open the customer's detail page and the lead's detail page.
- **Expected:** `DELETE /api/v1/companies/{id}` → **204**; the row disappears and
  `GET /api/v1/companies/{id}` is now 404. The customer and the lead still exist with their
  free-text company unchanged and `company_id` and `company_record` absent. The domain can be
  reused immediately (TC-COMP-009's check sees live rows only). Deleting again is **404**.
  Concurrency (#182): the delete's transaction locks the company row first
  (`CompanyRepository.LockLive`, `FOR UPDATE` on MySQL/MariaDB), then unlinks leads, customers
  and deals, then soft-deletes. A lead conversion or lead edit running at the same time either
  committed first (its link is then cleared by the delete) or waits for that lock and never links
  the deleted company; a company deleted between the delete's own lookup and its lock is **404**
  with nothing written.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "deleting a company leaves the customer with its text company only" (customer half through the
  UI; the lead half, the 404 afterwards and the domain reuse are pinned in Go).
  Go: `TestCompanyServiceDelete_UnlinksLeadsAndCustomersThenSoftDeletes` (`internal/service/company_service_test.go`),
  `TestCompanyServiceDelete_RollsBackTheUnlinksWhenTheDeleteFails` (`internal/service/company_service_test.go`);
  `CompanyIntegrationTestSuite.TestDeleteUnlinksLeadsAndCustomers` (`test/integration/company_integration_test.go`); lock order (#182):
  `TestCompanyServiceDeleteLocksCompanyFirst` (`internal/service/company_delete_lock_test.go`),
  `TestCompanyServiceDeleteRacedAfterPreReadSQLite` (`internal/service/company_delete_lock_test.go`); `TestLeadCompanyLockOrderOutcomesSQLite` (`internal/service/lead_company_lock_order_test.go`),
  `TestLeadConversionVsCompanyDeleteMySQL` (`internal/service/lead_company_lock_order_mysql_test.go`), `TestLeadUpdateVsCompanyDeleteMySQL` (`internal/service/lead_company_lock_order_mysql_test.go`),
  `TestBulkLeadStatusVsCompanyDeleteMySQL` (`internal/service/bulk_lead_company_delete_test.go`) — the MySQL roots skip without
  `TEST_MYSQL_DSN`; `internal/repository/company_lock_live_test.go` `TestCompanyRepositoryLockLive{SQLite,MySQL}`.

### TC-COMP-018 — Sales and support cannot delete
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user and a support user; a company created by the test.
- **Steps:**
  1. As each: `DELETE /api/v1/companies/{id}`.
- **Expected:** **403** both times (`RequireRole(admin)` on DELETE); the row remains. The UI hides
  the delete icon from both roles.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`). Go:
  `CompanyHandlerTestSuite.TestRoleMatrix` (`internal/handler/company_handler_test.go`) and
  `CompanyIntegrationTestSuite.TestRoleMatrix` (`test/integration/company_integration_test.go`).

## 13.5 The link from customers and leads

### TC-COMP-019 — Link a customer to a company through the customer form
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a company created by the test.
- **Steps:**
  1. Open the new-customer form; type the company's name into the "Company (linked)" autocomplete
     (it calls `GET /api/v1/companies?search=<typed>`); pick it; type free text into "Company (as
     entered)"; fill the required fields; submit.
  2. Open the customer's detail page, then the company's detail page.
- **Expected:** `POST /api/v1/customers` carries `company_id` and `company`; the response has
  both, and `GET /api/v1/customers/{id}` includes `company_record` with the company's name. The
  customer detail shows the linked company as a link to it; the company detail lists the
  customer under its customers and reports `customer_count` 1. The two company fields are
  independent: neither overwrites the other.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-companies.spec.ts`
  "linking a customer to a company shows the link on both detail pages".
  Go: `CompanyIntegrationTestSuite.TestLeadAndCustomerCompanyLinkThroughTheAPI` (`test/integration/company_integration_test.go`).

### TC-COMP-020 — Link a lead to a company; sales sees only its own leads on the company
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A company created by the test; two sales users each owning one lead linked
  to it (leads created as admin with `owner_id`, or by each sales user).
- **Steps:**
  1. As one sales user, open the company's detail page, Leads section
     (`GET /api/v1/companies/{id}/leads`).
  2. As admin, the same.
  3. As support, `GET /api/v1/companies/{id}/leads`.
- **Expected:** Step 1: 200 with only that user's lead, `meta.total` 1. Step 2: both leads,
  `meta.total` 2, and the company's `lead_count` is 2 for every reader. Step 3: **403** — the
  leads sub-list is admin and sales only, as the leads endpoints are; support still reads
  `/companies/{id}/customers`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (extended); the sales or
  support account comes from `APIClient.createUser` and `APIClient.login`
  (`gocrm-ui/e2e/helpers/sso.ts`), the admin half in the same spec. Go:
  `CompanyIntegrationTestSuite.TestSalesOwnerRuleAndOwnLeadsOnly` (`test/integration/company_integration_test.go`),
  `TestCompanyRepository_ListLeadsScopesByOwnerAndListCustomersPaginates` (`internal/repository/company_repository_test.go`).

### TC-COMP-021 — Unknown company_id is 400 INVALID_REFERENCE on leads and customers
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin token.
- **Steps:**
  1. `POST /api/v1/customers` and `POST /api/v1/leads` with `company_id: 999999`.
  2. `PUT /api/v1/customers/{id}` and `PUT /api/v1/leads/{id}` with `company_id: 999999`.
- **Expected:** **400** with `error.code` `INVALID_REFERENCE` and message
  `unknown company_id 999999: company not found` each time; nothing is written. A soft-deleted
  company is unknown too. This is the scalar counterpart of the `label_ids` rule on tasks.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (request context).
  Go: `internal/handler/company_link_handler_test.go`, `internal/service/company_link_service_test.go`,
  `CompanyIntegrationTestSuite.TestLeadAndCustomerCompanyLinkThroughTheAPI` (`test/integration/company_integration_test.go`).

### TC-COMP-022 — On update, absent company_id keeps the link and 0 clears it
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a customer and a lead linked to a company, all created by
  the test.
- **Steps:**
  1. Edit the customer's phone only; save. Repeat for the lead.
  2. Clear the "Company (linked)" autocomplete on each form; save.
- **Expected:** Step 1: the PUT bodies carry no `company_id` and the responses still carry the
  original `company_id`. Step 2: the PUT bodies carry `company_id: 0`; the responses carry no
  `company_id`, the detail pages show no linked company, and the free-text company is unchanged.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new).
  Go: `LeadHandlerTestSuite.TestUpdate_CompanyIDAbsentKeepsZeroClearsValueSets` (`internal/handler/company_link_handler_test.go`)
  and `CustomerHandlerTestSuite.TestUpdate_CompanyIDAbsentKeepsZeroClearsValueSets` (`internal/handler/company_link_handler_test.go`)
  (leads and customers), `CompanyIntegrationTestSuite.TestLeadAndCustomerCompanyLinkThroughTheAPI` (`test/integration/company_integration_test.go`).

### TC-COMP-023 — Erasing a linked customer keeps the company and the link
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a company and a customer linked to it, both created by the
  test.
- **Steps:**
  1. Delete the customer (GDPR erasure) from the customers list; confirm.
  2. Open the company's detail page.
- **Expected:** The customer's personal fields are overwritten and the row soft-deleted, as
  before this change. The company is untouched: its name, domain and owner stand, and its
  `customer_count` drops to 0 because it counts live rows. The erased row's `company_id` is kept
  (business link, like `assigned_to_id`). Nothing of the person appears in the `companies` table.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-companies.spec.ts` (new).
  Go:
  `TestErasingALinkedCustomerAndLeadKeepsTheCompanyLinkAndTheCompany` (`test/integration/company_integration_test.go`);
  `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates` (`test/integration/erasure_pii_sweep_test.go`)
  now lists `companies`.

### TC-COMP-024 — Public form submissions still create leads without a company link
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A published form (`forms-public-api.spec.ts` fixtures).
- **Steps:**
  1. Submit the form through `POST /api/v1/forms/public/{publicId}/submissions`.
  2. As admin, read the created lead.
- **Expected:** 200 success-shaped response as before; the lead exists with the submitted
  free-text `company` and no `company_id`. The lead model gained a nullable column and the public
  DTO did not change; the migration adds the column with `NULL`, so existing rows are untouched.
- **Automation:** automated — `gocrm-ui/e2e/tests/forms-public-api.spec.ts` (existing suite,
  listed for CD-1 in the plan; it asserts the lead is created, not the absence of `company_id`).
