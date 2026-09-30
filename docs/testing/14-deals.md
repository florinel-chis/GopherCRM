# Deals — Test Cases

End-to-end cases for the deals area added 2026-09-30: the `/deals` list with its server-side
filters, the create/edit form, the detail page with its stage selector and history timeline, the
deals sections on the company and customer detail pages, and deletion (a soft delete that keeps
the history, not an erasure). Every **Expected** states what the build does **today**, traced to
the handlers, service and repository; every automated case names the exact test title in
`gocrm-ui/e2e/tests/admin-deals.spec.ts`.

33 cases: 16 automated, 13 by `gocrm-ui/e2e/tests/admin-deals.spec.ts` and 3 by
`gocrm-ui/e2e/tests/admin-deals-board.spec.ts` for the pipeline board of section 14.7 (15 of them
partial, as in 10-labels and 13-companies: the spec asserts the core outcome, the rest is pinned
in Go or Vitest), 9 planned (7 against `admin-deals.spec.ts`, 2 against
`admin-deals-board.spec.ts`), 8 blocked on the sales/support role-login helper. Every case
names the Go test that already pins its API behaviour.

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
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-deals.spec.ts`
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can filter the list by stage"
  (types the run stamp, which only the two titles of that run carry, and expects two rows; the
  notes half and `meta.total` are pinned in Go). Go:
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can filter the list by stage"
  (picks "Proposal" on top of a search and expects the one proposal row with its stage chip,
  then "All stages" brings both rows back; the "Open only" toggle is Vitest `DealList.test.tsx`
  and the 400s are Go). Go:
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
  "Created in Proposal" by the admin. `GET /deals/{id}/history` has one row with `from_stage` null.
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can create a deal for a
  company and sees the formatted amount on both detail pages" (creates in qualification with
  every field and the company, asserts the 201, each field and the company link on the detail
  page, the one history row and the deal in the company's Deals section; the response body is
  pinned in Go). Go: `deal_handler_test.go`
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
  1. Submit with an empty title; with a negative amount; with an amount above
     10,000,000,000.00; with currency `eur`; with probability 101; with a title of 201
     characters.
- **Expected:** The form stays open with the message on the field. The form's own schema refuses
  most of these before any request ("Title is required", "Amount must be a positive number with
  at most two decimals", "Amount is too large", "Currency must be a 3-letter ISO code"); whatever
  reaches the API is a **400**: the binding tags refuse the amount (`min=0`, and
  `max=1000000000000`, i.e. 10^12 cents, answered "AmountCents must be at most
  1000000000000"; the service repeats the bound as `models.DealAmountCentsMax`, so the pipeline
  sums stay inside a BIGINT), the currency (`len=3,alpha,uppercase`)
  and the probability (`0–100`); `lengthError` refuses values longer than their column (title 200,
  `lost_reason` 255, `source` 100, notes 65535 bytes) and names the field; the service trims and
  refuses a blank title. A date not of the form `YYYY-MM-DD` is a 400 too.
- **Automation:** automated (partial) — `admin-deals.spec.ts` "validation errors keep the form
  open" (an empty title, `12,5` as amount and `EU` as currency, all refused by the form before
  any request, so the 400s of the API are pinned in Go).
  Go: `deal_handler_test.go` `TestCreate_BindingRejectsBadBodies`,
  `TestCreate_ValuesLongerThanTheirColumnAre400`,
  `TestDeal_AmountAboveTheBoundIsRefusedWithANumericMessage` (create and update),
  `TestDealAmountBindingTagMatchesTheModel`; `deal_service_test.go`
  `TestCreate_ValidationFailuresNeverReachTheWrite`, `TestAmountBound_OnCreateAndUpdate`;
  `deal_repository_test.go` `TestDealRepository_PipelineAggregatesTheMaximumAmount`;
  `deal_test.go`. Vitest: `DealForm.test.tsx` "refuses an amount above the API bound of
  1000000000000 cents and accepts the bound".

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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can create a deal for a
  company and sees the formatted amount on both detail pages" (EUR only: `1234.56` renders as
  `€1,234.56` on the detail page and in the company's Deals section; the second currency and the
  list column are Vitest `dealFormat.test.ts` and `DealList.test.tsx`). Go: n/a
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can edit the title and amount"
  (changes the title and the amount of an unlinked deal and asserts the 200 and the detail page;
  the link rules are Vitest `DealForm.test.tsx` and Go). Go:
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
  has a new row "Qualification to Lost". A `PUT` that keeps the stage adds no row and applies
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
  entries oldest first: "Created in Qualification", "Qualification to Proposal", "Proposal to
  Negotiation", each with the admin's name and a timestamp (`GET /deals/{id}/history`,
  `changed_by` carries id, name and email, never the password).
- **Automation:** automated (partial) — `admin-deals.spec.ts` "moving through the open stages grows
  the history by one row each time" (asserts both 200s, the stage chip, 40% then 70%, the three
  rows with their texts and that nothing is closed; the name and timestamp on each row are Vitest
  `DealHistory.test.tsx`). Go: `deal_service_test.go`
  `TestDealService_ChangeStageAppliesTheRulesAndAppendsHistory`;
  `deal_repository_test.go` `TestDealRepository_StageChangesAreListedOldestFirstWithTheUser`;
  `deal_handler_test.go` `TestHistory_ReturnsTheRowsAsGivenWithTheUser`.

### TC-DEAL-016 — Winning a deal pins the probability and shows closed_at
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; an open deal created by the test.
- **Steps:**
  1. Pick "Won".
- **Expected:** **200**: `stage` `won`, `probability` 100 whatever was sent, `closed_at` set to now
  and shown on the detail page. The history gains "… to Won".
- **Automation:** planned — `admin-deals.spec.ts` (new). Go:
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "marking a deal lost needs a reason,
  shows closed_at and the reason, and moving back clears them" (the empty reason is refused with
  the dialog still open and the stage unchanged, then the 200, the Lost chip, closed_at, the
  reason, 0% and the second history row; the 255-character limit is Go). Go:
  `deal_handler_test.go` `TestChangeStage_PassesTheBodyThroughAndReturnsTheMovedDeal`,
  `TestChangeStage_BindingRejectsBadBodies`; `deal_integration_test.go` `TestStageJourneyWithHistory`.

### TC-DEAL-018 — Reopening clears closed_at and the lost reason
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a lost deal with a reason, created by the test.
- **Steps:**
  1. Pick "Qualification".
- **Expected:** **200**: `probability` 10, `closed_at` absent, `lost_reason` empty. The history
  gains "Lost to Qualification".
- **Automation:** automated (partial) — `admin-deals.spec.ts` "marking a deal lost needs a reason,
  shows closed_at and the reason, and moving back clears them" (reopens to "Proposal" rather than
  "Qualification" and asserts the 200, the chip, closed_at and the reason gone and the third
  history row; the probability is Go). Go: `deal_service_test.go`
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can create a deal for a
  company and sees the formatted amount on both detail pages" (a deal linked to a company only:
  the company link and the history are asserted; the customer and lead links are Vitest
  `DealDetail.test.tsx` and Go, the 404 is Go). Go: `deal_repository_test.go`
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can create a deal for a
  company and sees the formatted amount on both detail pages" (the company page's Deals section
  lists the deal with its amount; the customer page, the "New deal" pre-selection and the 404s
  are Vitest `CompanyDetail.test.tsx` / `CustomerDetail.test.tsx` and Go). Go:
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
- **Automation:** automated (partial) — `admin-deals.spec.ts` "admin can delete a deal" (deletes
  from the list row rather than the detail page and asserts the row is gone from the narrowed
  list; the 404s and the kept history rows are Go). Go:
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

## 14.7 Pipeline board (2026-09-30)

`/deals/board` shows one column per stage, backed by `GET /deals/pipeline` for the column headers
and by the deals list for the cards; a card's "Move to…" menu calls the stage endpoint. Sources:
`internal/handler/deal_handler.go` (`Pipeline`), `internal/service/deal_service.go` (`Pipeline`,
`buildDealPipelineStages`, `weightedCents`), `internal/repository/deal_repository.go`
(`Pipeline`), `gocrm-ui/src/pages/deals/DealBoard.tsx`,
`gocrm-ui/e2e/tests/admin-deals-board.spec.ts`. The API payload is
`{"stages": [{"stage", "count", "totals": [{"currency", "amount_cents", "weighted_cents"}]}]}`
with all five stages in pipeline order, empty ones as `count: 0, totals: []`.

### TC-DEAL-027 — The board shows every stage with its count and totals per currency
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; two deals created by the test in different stages.
- **Steps:**
  1. Open `/deals/board`.
- **Expected:** five columns in the order qualification, proposal, negotiation, won, lost (won and
  lost collapsed by default); each deal's card sits in its stage's column; each header shows the
  count and one total per currency. `GET /api/v1/deals/pipeline` → **200** with the same numbers;
  soft-deleted deals are not counted; amounts in different currencies are never added together.
- **Automation:** automated (partial) — `admin-deals-board.spec.ts` "the board shows created deals in their stage columns with counts and totals" (two deals
  created in Qualification and Proposal sit in their columns; each header count is at least 1;
  each column's EUR total grows by exactly the created amount; the Qualification header shows a
  weighted line; Won and Lost start collapsed). Not asserted end to end: the column order, the API
  response, a second currency and the exclusion of soft-deleted deals — Vitest `DealBoard.test.tsx`
  "renders the five stage columns in order with their counts and totals" and the Go tests.
  Go: `deal_repository_test.go`
  `TestDealRepository_PipelineGroupsByStageAndCurrency`, `TestDealRepository_PipelineAggregatesTheMaximumAmount`; `deal_service_test.go`
  `TestBuildDealPipelineStages_Layout`; `deal_handler_test.go` `TestPipeline_ResponseShape`;
  `deal_pipeline_integration_test.go` `TestPipelineAsAdmin`.

### TC-DEAL-028 — Weighted totals round half up once per stage and currency
- **Priority:** P1
- **Type:** edge
- **Preconditions:** Admin; deals created by the test: 333 EUR at 33 % and 1000000 EUR at 40 % in
  proposal, 1 EUR at 50 % in negotiation.
- **Steps:**
  1. `GET /api/v1/deals/pipeline`.
- **Expected:** proposal EUR `weighted_cents` = round half up of (10989 + 40000000) / 100 =
  **400110**; negotiation EUR = 0.5 → **1**. Rounding is applied to the stage-and-currency sum,
  not per deal (two 1-cent deals at 50 % give 1, not 2).
- **Automation:** planned — `admin-deals-board.spec.ts` (new; the spec makes no API call today).
  Go: `deal_service_test.go` `TestWeightedCents_RoundsHalfUpOnce`, `TestBuildDealPipelineStages_RoundsTheTotalNotEachDeal`;
  `deal_pipeline_integration_test.go` `TestPipelineAsAdmin`.

### TC-DEAL-029 — "Move to…" moves a card and updates both columns
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin; a qualification deal created by the test.
- **Steps:**
  1. On the board, open the card's menu and choose negotiation.
- **Expected:** `POST /api/v1/deals/{id}/stage` with `{"stage":"negotiation"}` → **200**; the
  card appears under negotiation; both headers' counts and totals change; the history gains one
  row.
- **Automation:** automated (partial) — `admin-deals-board.spec.ts` "moving a card to Negotiation through its menu moves it and updates both headers" (the stage
  response is 200, the card leaves Qualification and appears under Negotiation, and both header
  counts change by one). Not asserted end to end: the request body, the header totals and the
  history row. Go: `deal_service_test.go` `TestDealService_ChangeStageAppliesTheRulesAndAppendsHistory`;
  Vitest `DealBoard.test.tsx` "moves a card to Proposal from its menu and refetches both columns and
  the pipeline".

### TC-DEAL-030 — Moving a card to lost asks for a reason
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin; an open deal created by the test.
- **Steps:**
  1. Choose "Move to… lost"; enter a reason; confirm.
- **Expected:** the stage request carries `lost_reason`; the card moves to the lost column (with
  probability 0, so it adds nothing to the weighted total).
- **Automation:** automated (partial) — `admin-deals-board.spec.ts` "moving a card to Lost asks for a reason and shows it under the expanded Lost column" (the dialog
  takes the reason, the stage response is 200, the card appears under the expanded Lost column,
  and the detail page shows the Lost chip and the reason). Not asserted end to end: the request
  body and the weighted total. Go: `deal_service_test.go` `TestApplyDealTransition`; Vitest
  `DealBoard.test.tsx` "asks for a reason before moving a card to Lost and sends it".

### TC-DEAL-031 — Sales sees only its own deals on the board
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** a sales account and another owner, each with deals.
- **Steps:**
  1. As sales, `GET /api/v1/deals/pipeline` and `GET /api/v1/deals/pipeline?owner_id=<other>`.
- **Expected:** both **200** with the caller's own deals only; `owner_id` cannot widen it, as on
  the list. `company_id` still narrows within the caller's deals.
- **Automation:** blocked — needs a role-login helper. Go: `deal_service_test.go`
  `TestDealService_PipelineScoping`; `deal_pipeline_integration_test.go`
  `TestPipelineAsSalesIsScopedToItself`.

### TC-DEAL-032 — Admin filters the pipeline by owner and company
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin; deals of two owners, some linked to a company, all created by the test.
- **Steps:**
  1. `GET /api/v1/deals/pipeline?owner_id=<id>`, `?company_id=<id>`, both, and
     `?company_id=99999`.
  2. `GET /api/v1/deals/pipeline?owner_id=abc` and `?company_id=0`.
- **Expected:** step 1: **200**, the aggregate narrowed accordingly; an id matching nothing gives
  five empty stages. Step 2: **400** "owner_id must be a positive integer" / "company_id must be a
  positive integer", the list's rule.
- **Automation:** planned — `admin-deals-board.spec.ts` (new; the spec makes no API call today).
  Go: `deal_handler_test.go` `TestPipeline_PassesTheFiltersAndTheCaller`, `TestPipeline_BadFiltersAre400`;
  `deal_repository_test.go` `TestDealRepository_PipelineOwnerAndCompanyFilters`;
  `deal_pipeline_integration_test.go` `TestPipelineAsAdmin`.

### TC-DEAL-033 — Support and customer cannot read the pipeline
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** a support account; a customer account.
- **Steps:**
  1. As each, `GET /api/v1/deals/pipeline`; open `/deals/board`.
- **Expected:** **403** from the deals guard; the board route and its nav entry are not offered to
  these roles.
- **Automation:** blocked — needs a role-login helper for support. Go: `deal_handler_test.go`
  `TestRoleMatrix`; `deal_pipeline_integration_test.go` `TestRolesAndBadFilters`.
