# Deals — Test Cases

End-to-end cases for the deals area added 2026-09-30: the `/deals` list with its server-side
filters, the create/edit form, the detail page with its stage selector and history timeline, the
deals sections on the company and customer detail pages, and deletion (a soft delete that keeps
the history, not an erasure). Every **Expected** states what the build does **today**, traced to
the handlers, service and repository. The frontend pages and the e2e spec are written in parallel
with the backend, so the intended test titles are given on every admin case and the orchestrating
session reconciles them with `gocrm-ui/e2e/tests/admin-deals.spec.ts` once both halves are merged.

26 cases: 0 automated, 20 planned against `admin-deals.spec.ts`, 6 blocked on the sales role-login
helper. Every case names the Go test that already pins its API behaviour.

**Sources**

- `internal/models/deal.go`, `internal/models/company.go`, `internal/models/database.go`,
  `internal/models/configuration.go` (`deals.default_currency`)
- `internal/handler/deal_handler.go`, `internal/handler/routes.go` (`SetupDealRoutes`),
  `internal/handler/configuration_handler.go` (`GetUIConfigurations`)
- `internal/service/deal_service.go` (`applyDealTransition`), `internal/service/company_service.go`
- `internal/repository/deal_repository.go`, `internal/repository/company_repository.go`,
  `internal/utils/sort.go`
- `migrations/20260930120000_add_deals.{up,down}.sql`
- `gocrm-ui/src/pages/deals/`, `gocrm-ui/src/api/endpoints/deals.ts`,
  `gocrm-ui/e2e/tests/admin-deals.spec.ts`, `gocrm-ui/e2e/pages/deals.page.ts`
- `docs/FEATURES.md` section 4c

**Constraints**

- **Deals are admin and sales only.** The `/deals` group and both sub-lists carry
  `RequireRole(admin, sales)`; support and customer roles get **403** everywhere. Sales sees and
  edits its own deals: the list is always narrowed to the caller (`owner_id` in the query cannot
  widen it), and get, update, stage and history on another owner's deal answer **403** "You can
  only view your own deals" — the answer `GET /leads/{id}` gives, not a 404. Delete is admin only.
- **Deals hold no personal data and are not erased.** `DELETE /deals/{id}` is a soft delete; the
  `deal_stage_changes` rows stay. Erasing a linked customer, lead or user leaves `customer_id`,
  `lead_id`, `owner_id` and `changed_by_id` in place as business links. A case may only delete
  deals it created.
- **Stage rules live in one place** (`applyDealTransition`) and apply to create, `PUT` and the
  stage endpoint alike: probability defaults to 10/40/70 for the open stages unless sent; `won` is
  always 100 and `lost` always 0 whatever is sent; `closed_at` is set on entering `won` or `lost`
  (`won → lost` re-stamps it) and cleared on entering an open stage; `lost_reason` is stored only
  while `lost`. Every stage change writes one history row in the same transaction; the create
  writes the first (`from_stage` null); the same stage again writes nothing.
- **Money is integer minor units** (`amount_cents`) next to a three-letter code. The API never
  sums across currencies. The default currency is the `deals.default_currency` setting (`EUR` as
  shipped), served to the UI on `GET /configurations/ui`.
- **`PUT` is the new state of the text fields, amount, stage and date;** the links follow the
  scalar rule of `company_id` on leads (absent keeps, `0` clears, a value sets), `owner_id` absent
  keeps, `currency` absent keeps.
- **Only the admin account is available to e2e.** Sales sessions need an admin-authenticated
  `POST /users` plus a role-login helper that does not exist yet. Cases that need one are marked
  *blocked*; the backend half of every such case is pinned by `internal/handler/deal_handler_test.go`
  `TestRoleMatrix` / `TestSalesMayOnlyTouchItsOwnDeals` and
  `test/integration/deal_integration_test.go` `TestRoleMatrix` / `TestSalesOwnerRuleAndOwnDealsOnly`.
- **The moderate rate tier applies.** 120 req/min with a burst of 30 per IP.

---

## 14.1 List

### TC-DEAL-001 — Load the deals list as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one deal created by the test.
- **Steps:**
  1. Click "Deals" in the sidebar (after Leads).
  2. Wait for `GET /api/v1/deals`.
- **Expected:** 200. The page heading reads "Deals" and a table renders with the title, company or
  customer, stage chip, amount formatted by currency, probability, expected close and owner. The
  body is `{success:true, data:[…], meta:{page, per_page, total, total_pages}}` — `data` is the
  bare array, each item carrying `owner`, `company`, `customer` and `lead` when linked.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-deals.spec.ts`
  "admin can view the deals list page". Go: `deal_handler_test.go`
  `TestList_PassesEveryFilterAndReturnsTheArrayWithMeta`.

### TC-DEAL-002 — Search matches title and notes
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; two deals created by the test, one with a distinctive note.
- **Steps:**
  1. Type a fragment of one deal's title into the search box; wait for the request.
  2. Type the distinctive note fragment.
- **Expected:** Each request is `GET /api/v1/deals?search=<fragment>…` and returns only the matching
  deal with `meta.total` 1. The match is a substring over `title` and `notes` (`dealSearchClause`),
  not over the company name or the owner. Case-insensitive on MySQL and MariaDB, case-sensitive
  on SQLite.
- **Automation:** planned — `admin-deals.spec.ts` "admin can search deals". Go:
  `deal_repository_test.go` `TestDealRepository_ListFiltersSortsAndPaginates`,
  `deal_integration_test.go` `TestListFiltersSortAndPagination`.

### TC-DEAL-003 — Filter by stage and by open deals, server-side
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; deals in `negotiation`, `won` and `lost` created by the test.
- **Steps:**
  1. Pick "Negotiation" in the stage filter; wait for the request.
  2. Switch on the "Open only" toggle with no stage picked.
- **Expected:** Step 1: `GET /api/v1/deals?stage=negotiation…` returns the negotiation deal only.
  Step 2: `…open=true…` returns every deal whose stage is neither `won` nor `lost`; the toggle is
  only sent when on. Unlike the lead status filter, these narrow the whole result set on the
  server (`DealListFilter`). `stage=closed` or `open=maybe` is a **400**.
- **Automation:** planned — `admin-deals.spec.ts` "admin can filter deals by stage". Go:
  `deal_handler_test.go` `TestList_BadFiltersAre400`, `deal_repository_test.go`
  `TestDealRepository_ListFiltersSortsAndPaginates`.

### TC-DEAL-004 — Sort by an allowed column; an unknown column is refused
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in; three deals with different amounts created by the test.
- **Steps:**
  1. Click the Amount column header; wait for `GET /api/v1/deals?…sort_by=amount_cents&sort_order=asc`.
  2. With a request context, call `GET /api/v1/deals?sort_by=owner_id`.
- **Expected:** Step 1: 200, rows in ascending amount. Step 2: **400** "Invalid sort column". The
  allowlist is `id, title, stage, amount_cents, probability, expected_close_date, closed_at,
  created_at, updated_at` (`utils.AllowedSortColumns["deals"]`); the default is `created_at desc`
  with `id` as tie-breaker.
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `sort_test.go`,
  `deal_repository_test.go` `TestDealRepository_ListRejectsUnknownSortColumns`.

### TC-DEAL-005 — Sales sees its own deals only; support and customer roles get 403
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two sales users each owning a deal, a support user and a customer-role user.
- **Steps:**
  1. As each sales user, open `/deals`; then request `GET /api/v1/deals?owner_id=<the other rep>`.
  2. As support and as customer, request `GET /api/v1/deals`.
- **Expected:** Step 1: each rep sees only its own deals, and the `owner_id` filter does not widen
  the scope (the handler overrides it with the caller). Step 2: **403** "Insufficient permissions"
  from the group guard; the sidebar hides "Deals" for those roles.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go`
  `TestRoleMatrix`, `TestList_SalesIsAlwaysNarrowedToItself`; `deal_integration_test.go`
  `TestRoleMatrix`, `TestSalesOwnerRuleAndOwnDealsOnly`.

## 14.2 Create

### TC-DEAL-006 — Create a deal for a company with every field
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a company created by the test.
- **Steps:**
  1. Click "New deal"; fill title, pick the company in the autocomplete, amount, currency, stage
     "proposal", probability, expected close date, source, notes; submit.
- **Expected:** `POST /api/v1/deals` → **201** with the deal: `company_id` set and `company`
  preloaded, `amount_cents` the integer sent, `expected_close_date` as `YYYY-MM-DD`, `probability`
  as sent, `closed_at` absent. The detail page shows every field and one history entry
  "→ proposal" by the admin. `GET /deals/{id}/history` has one row with `from_stage` null.
- **Automation:** planned — `admin-deals.spec.ts` "admin can create a deal for a company and sees
  it on the detail page". Go: `deal_handler_test.go`
  `TestCreate_PassesTheFieldsAndTheCallerAsOwner`; `deal_integration_test.go`
  `TestCreateAppliesDefaultsAndValidates`.

### TC-DEAL-007 — A minimal deal takes the defaults
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; `deals.default_currency` at its shipped value.
- **Steps:**
  1. Create a deal with only a title.
- **Expected:** **201**: `stage` `qualification`, `probability` 10, `currency` `EUR` (the
  `deals.default_currency` setting, which the form pre-fills from `GET /configurations/ui`),
  `amount_cents` 0, `owner_id` the caller. Changing the setting in Settings > Configuration
  changes the currency of the next deal without a restart; a stored value that is not three
  upper-case letters falls back to `EUR` with a warning in the log.
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `deal_service_test.go`
  `TestDealService_CurrencyDefaultComesFromConfiguration`,
  `TestDealService_CreateWritesTheDealAndItsFirstHistoryRowTogether`; `deal_integration_test.go`
  `TestDefaultCurrencyFollowsTheConfiguration`; `configuration_handler_test.go`
  `TestGetUIConfigurations_CarriesTheDefaultDealCurrency`.

### TC-DEAL-008 — Validation errors keep the form open
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Submit with an empty title; with a negative amount; with currency `eur`; with probability
     101; with a title of 201 characters.
- **Expected:** Each is a **400** and the form stays open with the message on the field. The
  binding tags refuse the amount (`min=0`), the currency (`len=3,alpha,uppercase`) and the
  probability (`0–100`); `lengthError` refuses values longer than their column (title 200,
  `lost_reason` 255, `source` 100, notes 65535 bytes) and names the field; the service trims and
  refuses a blank title. A date not of the form `YYYY-MM-DD` is a 400 too.
- **Automation:** planned — `admin-deals.spec.ts` "validation errors keep the deal form open".
  Go: `deal_handler_test.go` `TestCreate_BindingRejectsBadBodies`,
  `TestCreate_ValuesLongerThanTheirColumnAre400`; `deal_service_test.go`
  `TestCreate_ValidationFailuresNeverReachTheWrite`; `deal_test.go`.

### TC-DEAL-009 — An unknown company, customer, lead or owner is INVALID_REFERENCE
- **Priority:** P0
- **Type:** validation
- **Preconditions:** A request context as admin.
- **Steps:**
  1. `POST /api/v1/deals` with `company_id: 999999`; repeat for `customer_id`, `lead_id`,
     `owner_id`.
- **Expected:** **400** with `error.code` `INVALID_REFERENCE` and a message naming the field
  (`unknown company_id 999999: …`). A soft-deleted row counts as unknown. Nothing is written.
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `deal_handler_test.go`
  `TestCreate_UnknownReferencesAreInvalidReference`; `deal_service_test.go`
  `TestCreate_EachLinkMustBeALiveRow`; `deal_integration_test.go`
  `TestCreateAppliesDefaultsAndValidates`.

### TC-DEAL-010 — Sales owns what it creates and cannot assign anybody else
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user.
- **Steps:**
  1. As sales, create a deal without an owner; then with `owner_id` of another user.
  2. As admin, create a deal with `owner_id` of a sales user.
- **Expected:** Step 1: **201** with `owner_id` the sales user; then **403** "You can only assign
  deals to yourself". Step 2: **201** with the named owner. `owner_id: 0` is a 400.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go`
  `TestCreate_OwnerRules`; `deal_integration_test.go` `TestSalesOwnerRuleAndOwnDealsOnly`.

### TC-DEAL-011 — Amounts render per currency
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a deal in EUR and one in USD created by the test.
- **Steps:**
  1. Open `/deals`.
- **Expected:** Each amount is formatted with `Intl.NumberFormat` for its own currency from the
  integer `amount_cents` (e.g. 125000 → €1,250.00 / $1,250.00). Nothing on the page sums across
  currencies.
- **Automation:** planned — `admin-deals.spec.ts` "amounts are formatted by currency". Go: n/a
  (presentation); the integer contract is pinned by `deal_integration_test.go`
  `TestCreateAppliesDefaultsAndValidates`.

## 14.3 Edit

### TC-DEAL-012 — Edit a deal; links absent keep, 0 clears
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a deal linked to a company, created by the test.
- **Steps:**
  1. Open the deal, change the title only; save.
  2. Clear the company picker; save.
- **Expected:** Step 1: `PUT /api/v1/deals/{id}` without `company_id` → **200**, `company_id`
  unchanged, the notes and other text fields as sent (an absent text field is cleared). Step 2:
  the body carries `company_id: 0` → `company_id` gone from the response and NULL in the
  database. `owner_id` and `currency` absent keep their values.
- **Automation:** planned — `admin-deals.spec.ts` "admin can edit a deal". Go:
  `deal_handler_test.go` `TestUpdate_ReplacesTheFieldsAndKeepsTheOwner`,
  `TestUpdate_LinksAbsentKeepZeroClearsValueSets`; `deal_integration_test.go`
  `TestStageJourneyWithHistory`.

### TC-DEAL-013 — Editing the stage on the form goes through the transition rules
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a deal in `qualification` created by the test.
- **Steps:**
  1. On the edit form, change the stage to "lost" and type a lost reason; save.
  2. Open the history.
- **Expected:** **200** with `probability` 0, `closed_at` set and `lost_reason` stored; the history
  has a new row `qualification → lost`. A `PUT` that keeps the stage adds no row and applies
  `probability` only when sent.
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `deal_service_test.go`
  `TestDealService_UpdateGoesThroughTheTransitionOnlyWhenTheStageChanges`;
  `deal_integration_test.go` `TestStageJourneyWithHistory`.

### TC-DEAL-014 — Sales cannot edit another rep's deal nor reassign its own
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two sales users, one deal each.
- **Steps:**
  1. As rep A, `PUT /api/v1/deals/{rep B's deal}`.
  2. As rep A, `PUT` its own deal with `owner_id` of rep B.
- **Expected:** Step 1: **403** "You can only view your own deals". Step 2: **403** "You can only
  assign deals to yourself". Admin may do both.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go`
  `TestSalesMayOnlyTouchItsOwnDeals`, `TestUpdate_OwnerRules`; `deal_integration_test.go`
  `TestRoleMatrix`.

## 14.4 Stage changes and history

### TC-DEAL-015 — Moving through the open stages grows the history
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a deal in `qualification` created by the test.
- **Steps:**
  1. On the detail page pick "Proposal" in the stage selector; then "Negotiation".
  2. Read the history timeline.
- **Expected:** Each pick is `POST /api/v1/deals/{id}/stage` `{stage}` → **200** with the deal:
  `probability` 40 then 70 (the new stage's default), `closed_at` absent. The timeline lists three
  entries oldest first: "→ qualification", "qualification → proposal", "proposal → negotiation",
  each with the admin's name and a timestamp (`GET /deals/{id}/history`, `changed_by` carries id,
  name and email, never the password).
- **Automation:** planned — `admin-deals.spec.ts` "moving a deal through the stages grows the
  history". Go: `deal_service_test.go` `TestDealService_ChangeStageAppliesTheRulesAndAppendsHistory`;
  `deal_repository_test.go` `TestDealRepository_StageChangesAreListedOldestFirstWithTheUser`;
  `deal_handler_test.go` `TestHistory_ReturnsTheRowsAsGivenWithTheUser`.

### TC-DEAL-016 — Winning a deal pins the probability and shows closed_at
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; an open deal created by the test.
- **Steps:**
  1. Pick "Won".
- **Expected:** **200**: `stage` `won`, `probability` 100 whatever was sent, `closed_at` set to now
  and shown on the detail page. The history gains "… → won".
- **Automation:** planned — `admin-deals.spec.ts` "winning a deal shows closed_at". Go:
  `deal_service_test.go` `TestApplyDealTransition`; `deal_integration_test.go`
  `TestStageJourneyWithHistory`.

### TC-DEAL-017 — Losing a deal asks for a reason and stores it
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; an open deal created by the test.
- **Steps:**
  1. Pick "Lost"; leave the reason empty and try to confirm; then type a reason and confirm.
- **Expected:** The UI requires the reason before it sends. The request is
  `POST /api/v1/deals/{id}/stage` `{stage:"lost", lost_reason}` → **200**: `probability` 0,
  `closed_at` set, `lost_reason` as typed. The API itself accepts an empty reason (the requirement
  is the form's); a reason over 255 characters is a **400**.
- **Automation:** planned — `admin-deals.spec.ts` "losing a deal requires a reason". Go:
  `deal_handler_test.go` `TestChangeStage_PassesTheBodyThroughAndReturnsTheMovedDeal`,
  `TestChangeStage_BindingRejectsBadBodies`; `deal_integration_test.go` `TestStageJourneyWithHistory`.

### TC-DEAL-018 — Reopening clears closed_at and the lost reason
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lost deal with a reason, created by the test.
- **Steps:**
  1. Pick "Qualification".
- **Expected:** **200**: `probability` 10, `closed_at` absent, `lost_reason` empty. The history
  gains "lost → qualification".
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `deal_service_test.go`
  `TestApplyDealTransition`, `TestDealService_ChangeStageAppliesTheRulesAndAppendsHistory`.

### TC-DEAL-019 — Asking for the current stage again changes nothing
- **Priority:** P2
- **Type:** negative
- **Preconditions:** A request context as admin; a won deal created by the test.
- **Steps:**
  1. `POST /api/v1/deals/{id}/stage` `{stage:"won", probability: 5}`.
- **Expected:** **200** with the deal exactly as it was — probability still 100, the same
  `closed_at` — and no new history row. `{stage:"closed"}` is a **400**; an unknown deal a **404**.
- **Automation:** planned — `admin-deals.spec.ts` (new). Go: `deal_service_test.go`
  `TestChangeStage_SameStageWritesNothing`; `deal_integration_test.go` `TestStageJourneyWithHistory`.

### TC-DEAL-020 — Sales cannot move or read the history of another rep's deal
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two sales users, one deal each.
- **Steps:**
  1. As rep A, `POST /api/v1/deals/{rep B's deal}/stage` and `GET …/history`.
- **Expected:** **403** on both, before the service is reached; on its own deal both are **200**.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go`
  `TestSalesMayOnlyTouchItsOwnDeals`; `deal_integration_test.go` `TestRoleMatrix`.

## 14.5 Detail and sub-lists

### TC-DEAL-021 — The detail page shows the links and the history
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a deal linked to a company, a customer and a lead, created by
  the test.
- **Steps:**
  1. Open the deal from the list.
- **Expected:** `GET /api/v1/deals/{id}` → **200** with `owner`, `company`, `customer` and `lead`
  preloaded (`dealPreloads`); the page links each to its own detail page and renders the history
  timeline from `GET /deals/{id}/history`. An unknown id is **404** "Deal not found".
- **Automation:** planned — `admin-deals.spec.ts` "admin can create a deal for a company and sees
  it on the detail page". Go: `deal_repository_test.go`
  `TestDealRepository_GetByIDPreloadsTheAssociations`; `deal_handler_test.go` `TestGet_NotFoundAndBadID`.

### TC-DEAL-022 — Company and customer detail pages list their deals
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a company, a customer and a deal linked to both, created by
  the test.
- **Steps:**
  1. Open the company's detail page; then the customer's.
  2. Click "New deal" on the company page.
- **Expected:** `GET /api/v1/companies/{id}/deals` and `GET /api/v1/customers/{id}/deals` → **200**,
  `data` the array newest first, `meta` the pagination; the company's `deal_count` is 1. "New
  deal" opens the form with the company pre-selected. An unknown company or customer is **404**
  "Company not found" / "Customer not found".
- **Automation:** planned — `admin-deals.spec.ts` "a company detail page lists its deals". Go:
  `deal_handler_test.go` `TestSubLists_SalesIsNarrowedAdminIsNotAndMissingParentsAre404`;
  `deal_repository_test.go` `TestCompanyRepository_DealCountAndUnlinkDeals`;
  `deal_integration_test.go` `TestListFiltersSortAndPagination`.

### TC-DEAL-023 — Sales sees only its own deals in the sub-lists
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two sales users each owning a deal at the same company.
- **Steps:**
  1. As each rep, `GET /api/v1/companies/{id}/deals`; as support, the same.
- **Expected:** Each rep sees one deal with `meta.total` 1; the admin sees two; support is **403**.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go`
  `TestSubLists_SalesIsNarrowedAdminIsNotAndMissingParentsAre404`; `deal_service_test.go`
  `TestListByCompany_ScopesTheFilterToTheCompanyAndOwner`; `deal_integration_test.go` `TestRoleMatrix`.

## 14.6 Delete and data

### TC-DEAL-024 — Delete is admin only
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales user owning a deal.
- **Steps:**
  1. As the sales owner, `DELETE /api/v1/deals/{id}`.
- **Expected:** **403** even on its own deal (`RequireRole(admin)` on the route); the delete button
  is hidden for sales.
- **Automation:** blocked — needs a role-login helper. Go: `deal_handler_test.go` `TestRoleMatrix`;
  `deal_integration_test.go` `TestRoleMatrix`.

### TC-DEAL-025 — Admin deletes a deal; the history stays
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a deal with two stage changes, created by the test.
- **Steps:**
  1. Delete the deal from its detail page; confirm.
- **Expected:** `DELETE /api/v1/deals/{id}` → **204**; the list no longer shows it; `GET` and a
  second `DELETE` are **404**. The row is soft-deleted and its `deal_stage_changes` rows remain in
  the table (they are not reachable through the API once the deal is gone).
- **Automation:** planned — `admin-deals.spec.ts` "admin can delete a deal". Go:
  `deal_service_test.go` `TestDealService_DeleteIsSoftAndHistoryStays`; `deal_repository_test.go`
  `TestDealRepository_DeleteIsSoftAndKeepsTheHistory`; `deal_integration_test.go`
  `TestCompanyDeleteUnlinksDealsAndDealDeleteKeepsHistory`.

### TC-DEAL-026 — Deleting a company unlinks its deals; erasing a person keeps them
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin logged in; a company with a deal, and a customer linked to another deal,
  all created by the test.
- **Steps:**
  1. Delete the company; open the deal.
  2. Delete (erase) the customer; open its deal and history.
- **Expected:** Step 1: the deal is intact with no company (`company_id` NULL, cleared in the same
  transaction as the company's soft delete). Step 2: the customer's personal fields are
  overwritten as before; the deal keeps `customer_id`, its stage and its history rows; nothing of
  the person appears in `deals` or `deal_stage_changes`.
- **Automation:** planned — `admin-deals.spec.ts` (new); `admin-companies.spec.ts` for the company
  half. Go: `deal_service_test.go` `TestCompanyServiceDelete_UnlinksDealsToo`;
  `deal_integration_test.go` `TestErasingALinkedCustomerLeadAndUserKeepsTheDealAndItsHistory`,
  `TestCompanyDeleteUnlinksDealsAndDealDeleteKeepsHistory`; `erasure_pii_sweep_test.go`
  `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates` now lists both tables.
