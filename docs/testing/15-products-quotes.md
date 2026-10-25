# Products & Quotes — Test Cases

Cases for the products and quotes area added 2026-10-26 (issue #158): the product catalogue, quotes
on deals and customers with lines and server-computed totals, the status lifecycle with history,
the plaintext quote email, and the erasure and migration guarantees. This release ships the backend
and API; the frontend slice (list and form pages, the quote detail with its status actions, the
DealDetail section, the print page, the email dialog, nav entries and route guards) is a separate
follow-up task, so its cases below are *planned* against spec files that do not exist yet.

Every **Expected** states what the build does **today**, traced to the handlers, service and
repositories; every automated case names the exact Go test that pins it. As in
[11-aeo.md](11-aeo.md), "automated" here means automated at the Go level: there is no Playwright
spec behind this section yet.

22 cases: 17 automated (Go), 5 planned (the UI slice, against `gocrm-ui/e2e/tests/admin-quotes.spec.ts`
and the Vitest files named in each case), 0 blocked. Priorities: 13 P0, 7 P1, 2 P2.

**Sources**

- `internal/models/product.go`, `internal/models/quote.go`, `internal/models/database.go`
- `internal/handler/product_handler.go`, `internal/handler/quote_handler.go`,
  `internal/handler/routes.go` (`SetupProductRoutes`, `SetupQuoteRoutes`)
- `internal/service/product_service.go`, `internal/service/quote_service.go`,
  `internal/service/quote_email.go`
- `internal/repository/product_repository.go`, `internal/repository/quote_repository.go`,
  `internal/utils/sort.go`, `internal/mailer/smtp.go`
- `migrations/20261026120000_add_products_and_quotes.{up,down}.sql`
- `docs/FEATURES.md` section 4d, `docs/datamodel.md` (Product, Quote, Quote Line, Quote Status Change)

**Constraints**

- **Products are read by admin and sales and written by admin.** Support and customer get **403**
  on every product route; unauthenticated requests get **401**. Quotes carry the deal guard
  (admin and sales); sales sees and edits its own quotes only, and the lists are narrowed inside
  the query, so `meta.total` counts only the caller's quotes.
- **Money is integer minor units** next to a three-letter code shared by every line of a quote.
  The line total is `(gross × (10000 − discount_bp) + 5000) / 10000`, round half up; the gross and
  the quote total are capped at 10^12, a quote holds at most 100 lines, and the gross is checked
  before the discount arithmetic so nothing overflows.
- **Only a draft is editable.** Every write to a quote or its lines starts with a locking read
  (`FOR UPDATE` on MySQL) and answers **409** `CONFLICT` outside draft. A transition is a
  conditional claim (`WHERE id = ? AND status = <from>`) plus one history row in one transaction;
  `draft → sent` counts the lines after the claim and rolls back on zero.
- **The email is a plaintext rendering, not the printable document.** It goes to the linked
  customer's current address with a fixed subject; nothing personal is copied onto the quote.
- **Deals are not touched by quotes.** Accepting a quote changes neither the deal's stage nor its
  amount; expiry is manual (acceptance is refused after `valid_until`).
- **Run the MySQL roots with `TEST_MYSQL_DSN`.** `TestQuoteStatusConcurrentMySQL` and
  `TestProductsAndQuotesMigration` create disposable schemas; they skip without a server.

---

## 15.1 Products

### TC-PQ-001 — Product routes enforce the roles over HTTP
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Users of every role; a product created by the admin.
- **Steps:**
  1. As each role and unauthenticated, call `GET /products`, `GET /products/{id}`, `POST`, `PUT`
     and `DELETE`.
- **Expected:** Admin: 201/200/200/200/204. Sales: 200 on both reads, **403** on the three writes.
  Support and customer: **403** everywhere. No token: **401** `UNAUTHORIZED`. Every response is the
  unified envelope (`success`, `data`, `error.code`).
- **Automation:** automated — `test/integration/product_integration_test.go`
  `TestProductRoutesRoleMatrix`; `internal/handler/product_handler_test.go` `TestProductHandler_RoleGuards`.

### TC-PQ-002 — The product list paginates, filters and sorts; unknown columns are refused
- **Priority:** P1
- **Type:** functional
- **Preconditions:** 25 products with distinct names and SKUs, some inactive, some in USD.
- **Steps:**
  1. `GET /products?limit=0`, `?limit=500`, `?page=2&limit=10`.
  2. `?is_active=false`, `?currency=USD`, `?search=item+1`, `?search=SKU-07`, combinations.
  3. `?sort_by=unit_price_cents&sort_order=asc`, `?sort_by=name&sort_order=desc`.
  4. `?sort_by=deleted_at`, `?is_active=maybe`, `?currency=eur`.
  5. Delete one product and list again.
- **Expected:** Step 1: `limit=0` yields 20 rows and `meta.per_page` 20 (never 0), `limit=500` is
  capped at 100, `meta.total` is 25 throughout. Step 2: each filter narrows the rows and the total;
  search matches name or SKU. Step 3: ordered as asked, with `id` as tie-breaker. Step 4: **400**
  `BAD_REQUEST`, `success: false`, no `data` rows. Step 5: the deleted product is absent from every
  list and search and its `GET` is **404**.
- **Automation:** automated — `product_integration_test.go` `TestProductListPaginationSortAndFilters`;
  `internal/repository/product_repository_test.go` `TestProductRepository_ListFiltersSortsAndHidesDeleted`,
  `_ListRejectsUnknownSortColumns`; `internal/utils/sort_test.go` `TestValidateSortProductsAndQuotes`.

### TC-PQ-003 — Product validation and defaults
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin.
- **Steps:**
  1. `POST /products` with only a name and `unit_price_cents`; then with `is_active: false` and a
     price of 0; then with a missing name, a negative price, a price above 10^12, `currency: eur`,
     a 201-character name, a 65-character SKU.
- **Expected:** The first is **201** with `currency` `EUR` (the `deals.default_currency` setting) and
  `is_active` true; the second keeps the explicit false and the 0. Each of the others is **400**
  `VALIDATION_ERROR`. The widths are `models.ProductNameMaxLength` 200 and `ProductSKUMaxLength`
  64, held to the column types by a test.
- **Automation:** automated — `product_handler_test.go`
  `TestProductHandler_CreateDefaultsActiveAndKeepsExplicitValues`, `TestProductPriceBindingTagMatchesTheModel`;
  `internal/service/product_service_test.go` `TestProductService_*`; `internal/models/product_test.go`
  `TestProductColumnWidthsMatchConstants`.

### TC-PQ-004 — Deleting a product keeps the quote lines priced from it
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A quote with a line priced from a product.
- **Steps:**
  1. `DELETE /products/{id}`; `GET /quotes/{quote}`; try to add another line of the product.
- **Expected:** **204**; the quote's line still reads with its copied description, price and total
  and `product_id` set; the quote can still be edited and sent; a new line of the deleted product is
  **400** `INVALID_REFERENCE`; the product's `GET` is **404**.
- **Automation:** automated — `product_integration_test.go` `TestProductDeleteKeepsQuoteLines`.

## 15.2 Create a quote

### TC-PQ-005 — Links, defaults and the first history row
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A customer; deals with and without that customer; a won and a lost deal.
- **Steps:**
  1. `POST /quotes` with neither link; with only `deal_id`; with `deal_id` and a different
     `customer_id`; with an unknown deal, customer or owner; on the won and the lost deal.
  2. As sales: on another owner's deal; with `owner_id` of somebody else; on its own deal.
  3. Set `deals.default_currency` to `RON` and create a quote without a currency.
- **Expected:** Step 1: **400** `VALIDATION_ERROR`; **201** with `customer_id` taken from the deal
  (null when the deal has none), `status` `draft`, every total 0, `owner_id` the caller,
  `number` `Q-%06d`, `lines: []`, and exactly one history row (`from_status` null, `to_status`
  draft) written in the same transaction; **400** `VALIDATION_ERROR` on the mismatch; **400**
  `INVALID_REFERENCE` for each unknown link (a soft-deleted customer counts as unknown); **409**
  `CONFLICT` on a closed deal. Step 2: **403**, **403**, **201** owned by the caller; an admin may
  name any owner. Step 3: `RON`; an explicit currency wins.
- **Automation:** automated — `test/integration/quote_integration_test.go` `TestQuoteCreateLinksAndDefaults`;
  `internal/service/quote_service_test.go` `TestQuoteCreateWritesQuoteAndHistoryTogether` (a failed
  history write leaves no quote); `internal/handler/quote_handler_test.go` `TestQuoteHandler_CreateAndUpdateBodies`.

## 15.3 Lines and totals

### TC-PQ-006 — A line copies the product's name and price
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A draft quote in EUR; products in EUR and USD, an inactive one and a deleted one.
- **Steps:**
  1. `POST /quotes/{id}/lines` with the EUR product and `quantity` 3.
  2. `PUT /products/{id}` with a new price and name; read the quote; add a second line with
     `discount_bp` 1250.
  3. Add a line with `unit_price_cents` and `description` overrides; with the USD product; with the
     inactive, the deleted and an unknown product.
- **Expected:** Step 1: **201**, the line carries the product's name and price and `line_total_cents`
  3 × price; the quote's subtotal, discount and total are recomputed. Step 2: the first line and the
  totals are unchanged; the second line copies the *new* name and price and rounds half up. Step 3:
  the overrides win within the same bounds; the USD product is **400** `VALIDATION_ERROR`; the other
  three are **400** `INVALID_REFERENCE`; nothing is written by a refusal.
- **Automation:** automated — `quote_integration_test.go` `TestQuoteLineSnapshotsProductPrice`;
  `internal/models/quote_test.go` `TestQuoteLineTotalRoundsHalfUp`, `TestQuoteTotalsSumLines`.

### TC-PQ-007 — Bounds are refused without writes and nothing overflows
- **Priority:** P0
- **Type:** validation
- **Preconditions:** A draft quote with one line.
- **Steps:**
  1. Add and edit lines with `quantity` 0 and 1,000,001, `discount_bp` −1 and 10001, a negative
     price and one above 10^12, a gross above 10^12, the maximal quantity times the maximal price with
     a full discount, a line that would take the quote total over 10^12, and a 101st line.
- **Expected:** Every one is **400** `VALIDATION_ERROR` and the quote row and its lines are byte for
  byte what they were. 10^6 × 10^12 is 10^18, still an int64, and it is refused by the gross check
  before any multiplication by the discount. Exactly the bound is accepted.
- **Automation:** automated — `quote_service_test.go` `TestQuoteLineLimitsRejectedWithoutWrites`;
  `quote_handler_test.go` `TestQuoteLineBindingTagsMatchTheModel`, `TestQuoteHandlerMapsServiceErrors`.

### TC-PQ-008 — Outside draft every write is a conflict
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A quote with a line, moved to sent, then accepted; a second quote with its own line.
- **Steps:**
  1. In each non-draft status: `PUT /quotes/{id}`, `POST …/lines`, `PUT …/lines/{lineId}`,
     `DELETE …/lines/{lineId}`.
  2. Address a line of the second quote through the first quote's path.
  3. Move a sent quote back to draft and repeat the writes.
- **Expected:** Step 1: **409** `CONFLICT` (`ErrQuoteLocked`) each time and no row of `quotes` or
  `quote_lines` changes. Step 2: **404** on a draft; on a locked quote the lock answers first.
  Step 3: **200**; the currency may change only once the quote has no lines (**409** otherwise);
  `valid_until: null` clears the date.
- **Automation:** automated — `quote_integration_test.go` `TestQuoteLockedOutsideDraft`;
  `internal/repository/quote_repository_test.go` `TestQuoteRepository_DraftWritesAreConditional`,
  `_LinesBelongToTheirQuote`.

## 15.4 Status lifecycle

### TC-PQ-009 — Exactly five transitions; everything else writes nothing
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A quote with a line in each of the five statuses.
- **Steps:**
  1. Request every from/to pair through `QuoteService.ChangeStatus`.
  2. Send a quote with no lines; accept a quote whose `valid_until` was yesterday, today, tomorrow.
  3. Walk draft → sent → draft → sent → accepted and read the history.
- **Expected:** Step 1: draft→sent, sent→accepted, sent→rejected, sent→expired and sent→draft
  succeed, set `sent_at` (sent), `closed_at` (the three final statuses) or clear both (sent→draft),
  and append one history row; every other pair, the same status included, is **400**
  `INVALID_STATUS_TRANSITION` with the row and the history untouched. Step 2: **409**
  `ErrQuoteHasNoLines` with the claim rolled back (still draft, no history row); **409**
  `ErrQuoteExpired` yesterday, allowed today and tomorrow; the expired quote can still be rejected
  or marked expired. Step 3: `GET /quotes/{id}/history` lists the five rows oldest first with the
  acting user. The claim is a conditional update: a status flipped between the locking read and the
  claim leaves the claim matching nothing.
- **Automation:** automated — `quote_service_test.go` `TestQuoteStatusTransitionMatrix`;
  `quote_handler_test.go` `TestQuoteStatusBindingTagsMatchTheModel`; `internal/models/quote_test.go`
  `TestQuoteTransitionsAreExactlyTheFiveAllowedMoves`; `quote_repository_test.go`
  `TestQuoteRepository_ClaimStatusIsConditional`, `_StatusChangesOldestFirstAndLatestID`.

### TC-PQ-010 — Concurrent transitions and line writes stay consistent
- **Priority:** P0
- **Type:** regression
- **Preconditions:** SQLite with the one-connection pool; MySQL through `TEST_MYSQL_DSN`.
- **Steps:**
  1. Race accepted against rejected on one sent quote, several rounds.
  2. Race a line add against draft→sent; race the delete of the only line against draft→sent.
- **Expected:** Step 1: exactly one commits; the loser gets **400** `INVALID_STATUS_TRANSITION`;
  exactly one new history row. Step 2: the add either commits first and its line is in the sent
  quote's totals, or is **409** `ErrQuoteLocked`; the delete either commits first and the send is
  **409** `ErrQuoteHasNoLines` with the quote still draft, or is **409** `ErrQuoteLocked`. No
  committed sent quote has zero lines and the committed totals always equal the sum of the lines.
- **Automation:** automated — `internal/service/quote_status_regression_test.go`
  `TestQuoteStatusConcurrentSQLite`, `TestQuoteStatusConcurrentMySQL`; `quote_repository_test.go`
  `TestQuoteRepository_LockingReadsCarryForUpdateOnMySQLOnly` (the `FOR UPDATE` clause is rendered
  on MySQL and dropped on SQLite).

## 15.5 Visibility and delete

### TC-PQ-011 — Sales sees and acts on its own quotes only
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two sales users with deals on one customer and quotes each; support and customer users.
- **Steps:**
  1. As sales: `GET /quotes` (with `limit=1`, with `owner_id` of the other rep),
     `GET /deals/{own}/quotes`, `GET /deals/{other}/quotes`, `GET /customers/{id}/quotes`.
  2. As sales on the other rep's quote: get, update, delete, status, add/edit/delete line, history, email.
  3. As admin: the lists and an update with `owner_id`.
  4. As support and customer: every quote route.
- **Expected:** Step 1: only the caller's quotes with `meta.total` counting only those (the owner
  filter is inside the query, before pagination); the other rep's deal is **403**. Step 2: **403**
  on every route, nothing changes and no mail is sent. Step 3: every quote, and the reassignment
  works. Step 4: **403** everywhere; no token is **401**.
- **Automation:** automated — `quote_integration_test.go` `TestQuoteVisibilityForSales`;
  `quote_handler_test.go` `TestQuoteHandler_SalesIsScopedToItsOwnQuotes`; `quote_repository_test.go`
  `TestQuoteRepository_ListScopesOwnerBeforePaginationAndDeleteIsSoft`.

### TC-PQ-012 — Delete rules
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A sales owner with a draft and a sent quote.
- **Steps:**
  1. As the owner: `DELETE` the draft, then the sent quote. As another rep: `DELETE` the sent quote.
  2. As admin: `DELETE` the sent quote; read it and the customer's quote list.
- **Expected:** Step 1: **204**, then **409** `CONFLICT`, then **403**. Step 2: **204**; the quote is
  **404** and absent from the lists; its lines and history rows stay in their tables; a second
  delete is **404**.
- **Automation:** automated — `quote_integration_test.go` `TestQuoteDeleteRules`.

## 15.6 Email

### TC-PQ-013 — The plaintext email, its refusals and the conditional stamp
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A sent quote with two lines and a `valid_until`; the recording mailer.
- **Steps:**
  1. Change the customer's email, then `POST /quotes/{id}/email`.
  2. Email a draft, a rejected and an expired quote; a quote without a customer; one whose
     customer is soft-deleted; one whose customer was erased.
  3. Make the mailer fail and email a sent quote.
- **Expected:** Step 1: `Mailer.Send` is called once with the *current* address, the subject exactly
  `Quote Q-000042 from GopherCRM` and a body listing the number, title, every line (description,
  quantity, unit price, discount, line total), subtotal, discount, total with currency,
  `valid_until` and notes; **200** with `last_emailed_at` set by a conditional update. Step 2:
  **409** `CONFLICT` each time and no mail. Step 3: **502** `MAIL_DELIVERY_FAILED` and
  `last_emailed_at` unchanged. No log line of the call contains the recipient, the customer's name
  or the body — a transport error that quotes the address is returned, not logged.
- **Automation:** automated — `internal/service/quote_email_test.go` `TestQuoteEmailDelivery`,
  `TestQuoteEmailLogsOmitPersonalData`; `quote_handler_test.go` `TestQuoteHandlerMapsServiceErrors`.

### TC-PQ-014 — A revision during the send is detected
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A sent quote; a mailer whose `Send` commits a transition before returning; a
  wrapped repository that commits a revision right after the quote is read.
- **Steps:**
  1. During the send move the quote sent→draft; then sent→draft→sent; then sent→accepted.
  2. Between the service's read of the history marker and its read of the quote, commit
     sent→draft, add a line and draft→sent.
- **Expected:** Step 1: the first two answer **409** `CONFLICT` (`ErrQuoteChangedDuringSend`) with
  `last_emailed_at` unchanged — the stamp's `NOT EXISTS` on history rows with `to_status = 'draft'`
  and an id above the marker read before sending is what tells the re-sent quote apart from an
  untouched one; the third succeeds, because acceptance is not a revision. An older revision does
  not block a later send. Step 2: the mail that went out carries the pre-revision total and the
  call is **409** with `last_emailed_at` unchanged, because the marker is read before the quote;
  a second send with nothing racing is stamped with the revised rendering.
- **Automation:** automated — `quote_email_test.go` `TestQuoteEmailRevisedDuringSend`;
  `quote_repository_test.go` `TestQuoteRepository_StampEmailedRefusesANewerRevisionToDraft`.

### TC-PQ-015 — The SMTP transport refuses header injection
- **Priority:** P0
- **Type:** regression
- **Preconditions:** `SMTPMailer` pointed at a local listener standing in for the relay.
- **Steps:**
  1. Call `Send` and `SendPasswordReset` with a recipient containing CR, LF or CRLF, and `Send` with
     such a subject; then with a clean message.
- **Expected:** Every bad value returns an error matching `mailer.ErrInvalidHeader` through
  `errors.Is`, echoes nothing of the injected value and opens no connection to the listener; the
  clean message does dial it.
- **Automation:** automated — `internal/mailer/header_injection_test.go` `TestSMTPMailerRejectsHeaderInjection`.

## 15.7 Erasure and schema

### TC-PQ-016 — Erasing a customer leaves the quote and nothing of the person
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A customer with a deal, a quote on that deal with a line and three history rows.
- **Steps:**
  1. Erase the customer; sweep every table; read the quote.
- **Expected:** Before the erasure the person is in `customers` only. Afterwards the whole-database
  sweep finds none of the email, names or phone in any table; the quote survives with its
  `customer_id`, `deal_id`, status, line and history; the deal keeps its link; the email path now
  answers `ErrQuoteNoRecipient`. The sweep schema contains `products`, `quotes`, `quote_lines` and
  `quote_status_changes` and reads soft-deleted rows of them.
- **Automation:** automated — `test/integration/erasure_pii_sweep_test.go`
  `TestCustomerErasureWithQuotesLeavesNoPersonalData`, `TestThePersonalDataSweepCoversQuoteTables`.

### TC-PQ-017 — The migration matches AutoMigrate and rolls back cleanly
- **Priority:** P1
- **Type:** regression
- **Preconditions:** A MySQL or MariaDB server through `TEST_MYSQL_DSN`.
- **Steps:**
  1. Apply `20261026120000_add_products_and_quotes` to a database at the previous head, and to one
     where AutoMigrate already created the four tables (with a row in `products`).
  2. Run `models.MigrateDatabase`, a second `Up`, `Steps(-1)`, `Up` again.
- **Expected:** Both apply; the four tables have the same columns, indexes and foreign-key names as
  AutoMigrate produces; the pre-existing row survives; `MigrateDatabase` changes nothing; the second
  `Up` is `ErrNoChange`; `Steps(-1)` drops exactly the four tables; the latest version assertions
  read `20261026120000`.
- **Automation:** automated — `cmd/migrate/products_quotes_migration_test.go`
  `TestProductsAndQuotesMigration`; `cmd/migrate/main_test.go` `TestMigrationFiles_PairsAndCompleteness`,
  `TestMigrationUpgrade_FromMainAutoMigratedSchema`, `TestMigrationFresh_FullUpAndDown`.

## 15.8 Frontend (planned — separate follow-up task)

### TC-PQ-018 — Products list and form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin and sales logged in.
- **Steps:**
  1. Open `/products`; as admin open "New", enter `12.50` EUR and save; edit and delete.
- **Expected:** Name, SKU, formatted price with currency and an active chip, server pagination,
  sorting and an active filter; New/Edit/Delete render for admin only; the form validates name
  (required, ≤ 200), SKU (≤ 64), price (≥ 0, converted to `unit_price_cents` 1250) and currency.
- **Automation:** planned — `gocrm-ui/src/pages/products/ProductList.test.tsx`,
  `ProductForm.test.tsx`; `gocrm-ui/e2e/tests/admin-quotes.spec.ts` (new).

### TC-PQ-019 — Quotes list and detail
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a draft quote.
- **Steps:**
  1. Open `/quotes`, filter by status; open the quote, add a line with 12.5 % off, edit and delete it.
- **Expected:** The list shows the number (`Q-000042`), title, status chip, customer, deal, total and
  valid-until, refetching with `status=sent` on filter; the detail shows the lines table and the
  server's subtotal, discount and total; "Add line" sends `discount_bp` 1250 and edit controls are
  hidden outside draft.
- **Automation:** planned — `gocrm-ui/src/pages/quotes/QuoteList.test.tsx`, `QuoteDetail.test.tsx`;
  `admin-quotes.spec.ts`.

### TC-PQ-020 — Status actions and the deal's Quotes section
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a deal with quotes.
- **Steps:**
  1. On a draft click "Mark as sent"; on a sent quote see Accept, Reject, Mark expired, Revise.
  2. Open the deal; use "New quote".
- **Expected:** Only the valid transitions are offered; each posts to `/quotes/{id}/status` and
  refreshes the quote and history; a 400 `INVALID_STATUS_TRANSITION` or a 409 shows the server
  message. DealDetail lists `GET /deals/{id}/quotes` with status and total; "New quote" is hidden on
  a won or lost deal.
- **Automation:** planned — `gocrm-ui/src/pages/quotes/QuoteStatusActions.test.tsx`,
  `DealQuotesSection.test.tsx`, a case in `gocrm-ui/src/pages/deals/DealDetail.test.tsx`;
  `admin-quotes.spec.ts`.

### TC-PQ-021 — Print page and email dialog
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a sent quote with a customer.
- **Steps:**
  1. Open `/quotes/{id}/print`; click Print. Manual check: print preview hides the app navigation.
  2. Click "Email quote", confirm.
- **Expected:** The print layout shows number, title, date, valid until, customer name and company,
  the lines table and totals; `window.print()` is called; navigation is hidden under
  `@media print`. The dialog names the customer recipient (no free-text address) and posts
  `/quotes/{id}/email`; 409 and 502 show distinct messages; success shows the last emailed time.
- **Automation:** planned — `gocrm-ui/src/pages/quotes/QuotePrint.test.tsx`, `QuoteEmailDialog.test.tsx`;
  the `@media print` hiding is a manual check.

### TC-PQ-022 — Navigation and route guards
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** Users of every role.
- **Steps:**
  1. As each role, look for the Products and Quotes nav entries and visit `/products`,
     `/products/new`, `/quotes`, `/quotes/1`, `/quotes/1/print`.
- **Expected:** Admin and sales see the entries and reach the pages; support and customer see no
  entry and are redirected as on the other role-guarded pages.
- **Automation:** planned — `gocrm-ui/src/routes/index.test.tsx` (new role-guard cases),
  `gocrm-ui/src/layouts/MainLayout.test.tsx` (new).
