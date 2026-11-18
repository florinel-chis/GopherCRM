# Products & Quotes — Test Cases

Cases for the products and quotes area added 2026-10-26 (issue #158): the product catalogue, quotes
on deals and customers with lines and server-computed totals, the status lifecycle with history,
the plaintext quote email, and the erasure and migration guarantees. The frontend slice
(2026-10-28, issue #167) added the list and form pages, the quote detail with its status actions,
the DealDetail section, the print page, the email dialog, the nav entries and the route guards;
its cases are section 15.8. Product external-id mappings (2026-11, issue #239) are section 15.9.
The per-line tax rate and the tax and gross totals (2026-11, issue #324) are section 15.10; their
web UI (issue #325) is TC-PQ-037 in that section. The web UI for fractional line quantities
(issue #346, over the API of #343) is TC-PQ-038 in section 15.11. The client response link of the
quote email and its two public routes (issue #382, backend only) are section 15.12.

Every **Expected** states what the build does **today**, traced to the handlers, service,
repositories and React pages; every automated case names the exact test that pins it. Sections
15.1 to 15.7 are automated at the Go level; section 15.8 names its Vitest files and titles and the
single test of `gocrm-ui/e2e/tests/admin-quotes.spec.ts`, which walks the admin flow from product
to accepted quote (TC-PQ-018 to 021 are *automated (partial)* by it, as in 14-deals: the spec
asserts the core outcome, the rest is pinned in Vitest).

46 cases: 46 automated (39 Go, 7 Vitest and Playwright), 0 planned, 0 blocked. Priorities: 27 P0, 17 P1, 2 P2.

**Sources**

- `internal/models/product.go`, `internal/models/quote.go`, `internal/models/database.go`
- `internal/handler/product_handler.go`, `internal/handler/quote_handler.go`,
  `internal/handler/routes.go` (`SetupProductRoutes`, `SetupQuoteRoutes`)
- `internal/service/product_service.go`, `internal/service/quote_service.go`,
  `internal/service/quote_email.go`
- `internal/repository/product_repository.go`, `internal/repository/quote_repository.go`,
  `internal/utils/sort.go`, `internal/mailer/smtp.go`
- `migrations/20261026120000_add_products_and_quotes.{up,down}.sql`
- `gocrm-ui/src/api/endpoints/products.ts`, `quotes.ts`; `gocrm-ui/src/pages/products/`,
  `gocrm-ui/src/pages/quotes/`, `gocrm-ui/src/pages/deals/DealDetail.tsx`,
  `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/layouts/MainLayout.tsx`
- `docs/FEATURES.md` section 4d, `docs/datamodel.md` (Product, Quote, Quote Line, Quote Status Change)

**Constraints**

- **Products are read by admin and sales and written by admin.** Support and customer get **403**
  on every product route; unauthenticated requests get **401**. Quotes carry the deal guard
  (admin and sales); sales sees and edits its own quotes only, and the lists are narrowed inside
  the query, so `meta.total` counts only the caller's quotes.
- **Money is integer minor units** next to a three-letter code shared by every line of a quote.
  The line total is `(gross × (10000 − discount_bp) + 5000) / 10000`, round half up; the line tax is
  `(line_total × tax_rate_bp + 5000) / 10000`, round half up (#324); the line's gross and the
  quote's gross total (`total_cents + tax_cents`) are capped at 10^12, a quote holds at most 100
  lines, and the gross is checked before the discount arithmetic so nothing overflows.
- **Only a draft is editable.** Every write to a quote or its lines starts with a locking read
  (`FOR UPDATE` on MySQL) and answers **409** `CONFLICT` outside draft. A transition is a
  conditional claim (`WHERE id = ? AND status = <from>`) plus one history row in one transaction;
  `draft → sent` counts the lines after the claim and rolls back on zero.
- **The email is a plaintext rendering, not the printable document.** It goes to the linked
  customer's current address with the subject `Quote Q-… from <general.company_name>`, or
  `GopherCRM` when that setting is blank (#292); nothing personal is copied onto the quote.
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
  1. Add and edit lines with `quantity` 0, 0.0001, 1.2345 and 1,000,001 (since #343 a quantity is
     an exact decimal from 0.001 to 1,000,000 with at most three decimals; a refusal carries
     `error.details.quantity`), `discount_bp` −1 and 10001, a negative
     price and one above 10^12, a gross above 10^12, the maximal quantity times the maximal price with
     a full discount, an untaxed line that would take the quote's (gross) total over 10^12, and a
     101st line. The taxed cases of the cap are TC-PQ-031 and TC-PQ-032.
- **Expected:** Every one is **400** `VALIDATION_ERROR` and the quote row and its lines are byte for
  byte what they were. 10^6 × 10^12 is 10^18, still an int64, and it is refused by the gross check
  before any multiplication by the discount. Exactly the bound is accepted.
- **Automation:** automated — `quote_service_test.go` `TestQuoteLineLimitsRejectedWithoutWrites`;
  `quote_handler_test.go` `TestQuoteLineBindingTagsMatchTheModel`, `TestQuoteHandlerMapsServiceErrors`;
  for the fractional bounds (#343) `TestQuoteFractionalQuantityBoundsRejectedWithoutWrites`
  (`internal/service`) and `TestQuoteLineQuantityBinding` (`internal/handler`).

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
  `Quote Q-000042 from <general.company_name>` (the stored name read at send time, trimmed; or
  `GopherCRM` when it is blank, missing, unreadable, holds a control character or exceeds 100
  characters — #292) and a body listing the number, title, every line (description,
  quantity, unit price, discount, line total, tax rate and tax), subtotal, discount, net total, tax
  and gross total with currency (#324, see TC-PQ-034), `valid_until` and notes; **200** with `last_emailed_at` set by a conditional update. Step 2:
  **409** `CONFLICT` each time and no mail. Step 3: **502** `MAIL_DELIVERY_FAILED` and
  `last_emailed_at` unchanged. No log line of the call contains the recipient, the customer's name
  or the body — a transport error that quotes the address is returned, not logged: the service
  does not log it, and the SMTP mailer's own failure line keeps the reply code without its text.
- **Automation:** automated — `internal/service/quote_email_test.go` `TestQuoteEmailDelivery`,
  `TestQuoteEmailLogsOmitPersonalData`; `quote_handler_test.go` `TestQuoteHandlerMapsServiceErrors`;
  `internal/mailer/smtp_relay_reply_test.go` `TestSMTPMailerSendLogOmitsTheRelayReply` (a stub relay
  answers `RCPT TO` with a 550 that echoes the address; the log line carries `smtp_code=550` and not
  the address).

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
  `quote_status_changes` and reads soft-deleted rows of them. Text that names the person (#245):
  a quote whose `customer_id` is the erased customer, or whose `deal_id` is a deal of an erased
  customer or lead (even when it names another customer), live or deleted and in every status, an
  accepted one included, reads `title` `[redacted]` and empty `notes` afterwards, and each line's
  `description` is its product's name (a deleted product's too); status, totals, dates, line
  amounts and `quote_status_changes` are unchanged; a quote of another customer is unchanged.
- **Automation:** automated — `test/integration/erasure_pii_sweep_test.go`
  `TestCustomerErasureWithQuotesLeavesNoPersonalData`, `TestThePersonalDataSweepCoversQuoteTables`;
  `test/integration/business_text_erasure_sweep_test.go`
  `TestCustomerErasureWithNamedDealAndQuoteTextLeavesNoPersonalData`,
  `TestLeadErasureWithNamedDealAndQuoteTextLeavesNoPersonalData`;
  `internal/repository/business_text_erasure_test.go` `TestCustomerErasureScrubsQuoteText{SQLite,MySQL}`,
  `TestBusinessTextErasureAtomic{SQLite,MySQL}`; `tests/business_text_erasure_test.go`
  `TestBusinessTextErasureAPISQLite`.

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

## 15.8 Frontend (2026-10-28, issue #167)

The React slice over the API above. Every call goes through `gocrm-ui/src/api/endpoints/products.ts`
and `quotes.ts` (pinned by `products.test.ts`, 8 tests, and `quotes.test.ts`, 15 tests). Money is
rendered from the server's integer minor units with the quote's currency; a discount is typed as a
percentage and sent in basis points (`quoteFormat.ts`, 6 tests with the three quantity tests of
#346), and so is a tax rate since #325
(TC-PQ-037); the subtotal, discount, net total, tax and gross total are always the server's. The one Playwright test, "admin creates a product, quotes a deal from its
Quotes section, sends, prints and accepts the quote" in `gocrm-ui/e2e/tests/admin-quotes.spec.ts`,
runs on SQLite, MySQL and MariaDB like the other admin suites.

### TC-PQ-018 — Products list and form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin and sales logged in.
- **Steps:**
  1. Open `/products`; as admin open "New Product", enter `12.50` EUR and save; edit and delete a
     row with its confirmation; as sales open the list.
- **Expected:** Name, SKU, the price formatted with its currency and an Active/Inactive chip; the
  search box, the active filter (All / Active only / Inactive only) and the pager refetch with
  `search`, `is_active` and `offset`/`limit`; sorting on the allowlisted columns only. "New
  Product", the row Edit and the row Delete render for admin only. The form validates name
  (required, ≤ 200), SKU (≤ 64), price (`^\d+(\.\d{1,2})?$`, ≤ 10^12 cents, converted with the
  existing `decimalToCents` so `12.50` becomes `unit_price_cents` 1250), currency (three
  letters, upper-cased, defaulting to `deals.default_currency` until edited) and the active
  switch, and sends the body in cents; the server's `VALIDATION_ERROR` message is shown.
- **Automation:** automated — `gocrm-ui/src/pages/products/ProductList.test.tsx`: "ProductList
  renders name, SKU, formatted price and active state from the products response", "ProductList
  shows the New, Edit and Delete controls for admin and hides them for sales", "ProductList
  refetches with the active filter and server pagination parameters" (plus the sort/search and
  the create/edit/delete cases); `ProductForm.test.tsx`: "ProductForm submits unit_price_cents
  1250 for a price of 12.50 EUR", "ProductForm rejects an empty name and a 201-character name
  without submitting" (plus SKU/price/currency validation, the configured default currency, a
  free product, edit mode and the server message); `admin-quotes.spec.ts` (partial: the product
  step creates `12.50` EUR and checks the list row).

### TC-PQ-019 — Quotes list and detail
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a draft quote with lines.
- **Steps:**
  1. Open `/quotes`, filter by status; open the quote; "Add line" with 12.5 % off; edit a line;
     delete a line with its confirmation; open a sent quote.
- **Expected:** The list shows the number (`Q-000042`), title, status chip, customer (name and
  company), deal title, total with currency and valid-until (`—` when absent), refetching with
  `status=sent` on filter and with `offset`/`limit` and the allowlisted `sort_by` on paging and
  sorting; a row opens `/quotes/{id}`; "New Quote" opens the create dialog. The detail shows the
  header fields, the lines table (description, qty, unit price, discount %, tax rate %, tax, line
  total; the quantity without trailing zeros, `1.5`, `2`, `0.001`, since #346, TC-PQ-038) and the
  server's subtotal, discount, net total, tax and gross total (#325, TC-PQ-037);
  "Add line" opens a dialog whose product picker offers
  active products in the quote's currency (`GET /products?is_active=true&currency=`), takes a
  quantity and a discount in percent and posts `POST /quotes/{id}/lines` with `discount_bp` 1250
  for `12.5`; the line Edit dialog sends the partial update with the discount in basis points;
  line Delete asks for confirmation; the Edit dialog changes title, valid-until (null clears) and
  notes. On a sent, accepted, rejected or expired quote none of Edit, Add line, line Edit and
  line Delete renders, and the lines and totals still read.
- **Automation:** automated — `gocrm-ui/src/pages/quotes/QuoteList.test.tsx`: "QuoteList renders
  number, title, status, customer, deal, total and valid-until from the quotes response",
  "QuoteList refetches with status=sent when the status filter changes" (plus the first-page
  request, paging/sorting and the row/dialog cases); `QuoteDetail.test.tsx`: "QuoteDetail shows
  the lines and the subtotal, discount and total from the server response", "QuoteDetail offers
  Add line on a draft and submits discount_bp 1250 for 12.5 percent", "QuoteDetail hides every
  edit control when the quote is not a draft" (plus local validation, line edit and delete, the
  quote edit dialog, delete and the not-found state); `admin-quotes.spec.ts` (partial: a line at
  10 % off, the totals 25.00 / 2.50 / 22.50 and the controls gone after sending).

### TC-PQ-020 — Status actions and the deal's Quotes section
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a deal with quotes.
- **Steps:**
  1. On a draft click "Mark as sent"; on a sent quote see Accept, Reject, Mark expired, Revise;
     on a closed quote see no button.
  2. Open the deal; use "New quote"; open a won or lost deal.
- **Expected:** Only the valid transitions are offered (draft: Mark as sent; sent: Accept,
  Reject, Mark expired, Revise; accepted/rejected/expired: "No further status changes"); each
  posts `{status}` to `/quotes/{id}/status`, stores the returned quote and refetches the history;
  a 400 `INVALID_STATUS_TRANSITION` (a lost race) or a 409 `CONFLICT` (no lines, past
  `valid_until`) shows the server's message inline and in the snackbar. DealDetail's "Quotes"
  section lists `GET /deals/{id}/quotes` (number linking to the quote, title, status chip, total
  with currency, paged five at a time); "New quote" opens the create dialog with the deal
  prefilled (and its customer and currency) and is hidden on a won or lost deal and for a viewer
  who cannot write.
- **Automation:** automated — `gocrm-ui/src/pages/quotes/QuoteStatusActions.test.tsx`:
  "QuoteStatusActions offers only the transitions valid from the current status",
  "QuoteStatusActions shows the server message for INVALID_STATUS_TRANSITION and CONFLICT
  responses" (plus the post-and-refresh case); `DealQuotesSection.test.tsx`: "DealQuotesSection
  lists the deal's quotes with status and total", "DealQuotesSection hides New quote for a won or
  lost deal" (plus the read-only viewer, the prefilled dialog and the empty text);
  `gocrm-ui/src/pages/deals/DealDetail.test.tsx`: "DealDetail renders the Quotes section for the
  deal", "DealDetail hides New quote in the Quotes section of a lost deal";
  `admin-quotes.spec.ts` (partial: Mark as sent and Accept with the history growing, the quote
  started from the section and shown there as accepted with its total, the deal's stage untouched).

### TC-PQ-021 — Print page and email dialog
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a sent quote with a customer.
- **Steps:**
  1. Open `/quotes/{id}/print`; click Print; emulate print media.
  2. Click "Email quote", confirm; repeat with the API answering 409 and 502.
- **Expected:** The print layout shows the application name, "Quote Q-000042", the title, the
  date (sent_at, else created_at), valid until, the customer's name and company, the lines table,
  the totals and the notes; the Print button calls `window.print()`. Under print media the app
  bar, the navigation drawer, the breadcrumbs and the page's own toolbar are hidden while the
  quote content stays visible. "Email quote" renders on sent and accepted quotes only; the dialog
  names the customer recipient (no address field, the API reads the current address itself) and
  posts `/quotes/{id}/email`; a 409 reads "The quote was not emailed: …" with the server's
  reason and keeps the send button, a 502 reads "The quote email could not be delivered. The
  quote was not changed; try again later."; success shows "Emailed on <time>" and the detail
  page its "Last emailed" field; a quote without a customer cannot be sent.
- **Automation:** automated — `gocrm-ui/src/pages/quotes/QuotePrint.test.tsx`: "QuotePrint
  renders the header, the lines and the totals from the quote", "QuotePrint calls window.print
  when Print is clicked"; `QuoteEmailDialog.test.tsx`: "QuoteEmailDialog names the customer
  recipient and shows the last emailed time on success", "QuoteEmailDialog shows distinct
  messages for 409 and 502 responses" (plus the no-customer case); `admin-quotes.spec.ts`
  (partial: the print page's content, then `page.emulateMedia({ media: 'print' })` with the
  navigation entry, the app bar and the toolbar hidden and the quote number, line and total
  visible, because jsdom does not evaluate `@media` rules). The email send is not exercised end
  to end: the e2e backend runs the log-only mailer, so it would stamp a success without a
  delivery (see 4d.7).

### TC-PQ-022 — Navigation and route guards
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** Users of every role.
- **Steps:**
  1. As each role, look for the Products and Quotes nav entries and visit `/products`,
     `/products/new`, `/products/1/edit`, `/quotes`, `/quotes/1`, `/quotes/1/print`.
- **Expected:** Admin and sales see the two entries (next to Deals) and reach the list, detail
  and print pages; support and customer see no entry and get the "Access Denied" redirect on
  every one of the URLs, as on the other role-guarded pages. `/products/new` and
  `/products/{id}/edit` are admin only: a sales user is redirected from them like support and
  customer, while an admin reaches "Create New Product".
- **Automation:** automated — `gocrm-ui/src/routes/index.test.tsx`: "products and quotes routes
  redirect support and customer users away from /products, /products/new, /quotes, /quotes/1 and
  /quotes/1/print", "products and quotes routes render the list and quote pages for admin and
  sales users and redirect sales away from /products/new and /products/1/edit";
  `gocrm-ui/src/layouts/MainLayout.test.tsx`: "MainLayout shows the Products and Quotes
  navigation entries for admin and sales and hides them for support and customer". The
  support/customer and sales cases are Vitest-only until a role-login e2e helper exists
  (lesson 8).

## 15.9 Product external-id mappings (#239)

`product_external_ids` maps an integrator's exact, case-sensitive `(source, scope, external_id)`
to a live product. Backend only: no UI. Sources: `internal/models/product_external_id.go`,
`internal/repository/product_external_id_repository.go`,
`internal/service/product_external_id_service.go`, `internal/service/product_service.go`
(`Delete`), `internal/handler/product_external_id_handler.go`, `internal/handler/routes.go`
(`SetupProductRoutes`), `migrations/20261112120000_add_product_external_ids.{up,down}.sql`.

### TC-PQ-023 — Create a mapping; a duplicate triple is 409
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin; two products.
- **Steps:**
  1. `POST /products/{a}/external-ids` with `{"source":"erp","scope":"plant-01","external_id":"MAT-100042"}`.
  2. Repeat on `{a}`, then on `{b}`.
- **Expected:** Step 1: **201** with `id`, `product_id`, `source`, `scope`, `external_id`,
  `created_at`, `updated_at` and no embedded product. Step 2: **409** `CONFLICT` both times
  ("external id is already mapped to a product"), nothing written. `products.sku` is unaffected
  and stays non-unique.
- **Automation:** automated — `tests/product_external_id_api_test.go` `TestProductExternalIDAPISQLite`;
  `internal/repository/product_external_id_repository_test.go` `TestProductExternalIDRepository{SQLite,MySQL}`;
  `internal/handler/product_external_id_handler_test.go` `TestProductExternalIDHandlerErrorMapping`.

### TC-PQ-024 — The identity is exact and case-sensitive; validation names the field
- **Priority:** P1
- **Type:** validation
- **Preconditions:** A mapping `("erp","plant-01","MAT-100042")`.
- **Steps:**
  1. Map `("erp","plant-01","mat-100042")` and `("erp","Plant-01","MAT-100042")` to another product.
  2. `POST` with a trailing space, a control character, invalid UTF-8, an empty source, a 65-rune
     source, a 129-rune scope, a 256-rune external_id.
- **Expected:** Step 1: **201** both times; each triple resolves to its own product. Step 2:
  **400** `VALIDATION_ERROR` with `details.field` (`source`, `scope` or `external_id`) and
  `details.reason`; the value is never echoed and nothing is written. On MySQL/MariaDB the three
  columns carry `utf8mb4_bin`.
- **Automation:** automated — `TestProductExternalIDAPISQLite`; `internal/service/product_external_id_service_test.go`
  `TestProductExternalIDValidation`; `internal/models/product_external_id_test.go` `TestProductExternalIDSchemaSQLite`.

### TC-PQ-025 — Lookup on GET /products
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A mapping on an active product and one on an inactive product.
- **Steps:**
  1. As admin and as sales, `GET /products?source=erp&scope=plant-01&external_id=MAT-100042`.
  2. The inactive product's triple; a triple mapped to nothing; the right triple with `scope`
     absent; `?external_id=x` alone; the triple plus `search=zzz&is_active=maybe&page=3`.
  3. As support and customer, the same lookup.
- **Expected:** Step 1: **200**, `data` an array holding the product, `meta.page` 1,
  `meta.total` 1. Step 2: the inactive product is returned; no match is **200** with `data: []`;
  an absent scope means `''`, so no match; `?external_id=x` is **400** `VALIDATION_ERROR` with
  `details.field` `source` and no service call; the other list parameters are ignored. Step 3:
  **403**.
- **Automation:** automated — `TestProductExternalIDAPISQLite`; `TestProductListExternalIDLookup`.

### TC-PQ-026 — Sales cannot write a mapping
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A mapping; users of every role.
- **Steps:**
  1. As sales, support and customer, `POST /products/{id}/external-ids` and
     `DELETE /products/{id}/external-ids/{mappingId}`.
- **Expected:** **403** `FORBIDDEN` everywhere; the mapping table is unchanged.
- **Automation:** automated — `TestProductExternalIDAPISQLite`; `TestProductExternalIDRoutesRoles`.

### TC-PQ-027 — Deleting another product's mapping is 404
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Mapping `m` on product `a`; product `b`.
- **Steps:**
  1. `DELETE /products/{b}/external-ids/{m}`; then `DELETE /products/{a}/external-ids/{m}`.
- **Expected:** **404** `NOT_FOUND` and nothing deleted; then **204**, the row is hard-deleted and
  the triple can be mapped again.
- **Automation:** automated — `TestProductExternalIDAPISQLite`; `TestProductExternalIDRepository{SQLite,MySQL}`.

### TC-PQ-028 — Deleting a product frees its keys
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Product `a` with several mappings and a quote line; product `b` with a mapping.
- **Steps:**
  1. `DELETE /products/{a}`; look up `a`'s triples; map one of them to `b`; `POST` a mapping on `a`.
- **Expected:** **204**. In one transaction the product row is locked, every mapping of `a` is
  hard-deleted and `a` is soft-deleted; a failure in the soft delete rolls the purge back. The
  lookup answers `data: []`; the triple maps to `b` (**201**); `b`'s own mapping is untouched; the
  quote line keeps its `product_id`; the mapping on `a` is **404**. A second delete is **404**
  and writes nothing. A mapping create racing the delete either commits first (and is purged) or
  answers **404**; no mapping ever survives on a deleted product.
- **Automation:** automated — `TestProductExternalIDAPISQLite`; `internal/service/product_delete_purge_test.go`
  `TestProductDeleteFreesExternalIDs{SQLite,MySQL}`; `internal/service/product_external_id_race_test.go`
  `TestProductExternalIDCreateRacesDelete{SQLite,MySQL}`; `cmd/product_external_id_wiring_test.go`
  `TestProductExternalIDProductionWiring`.

### TC-PQ-029 — Migration on a fresh database and on one holding products
- **Priority:** P1
- **Type:** migration
- **Preconditions:** A disposable MySQL/MariaDB schema (`TEST_MYSQL_DSN`); a SQLite file.
- **Steps:**
  1. Apply every migration on a fresh schema; roll `20261112120000_add_product_external_ids` back
     and reapply; roll everything back.
  2. Migrate to `20261111120000`, insert a live and a soft-deleted product, apply the migration
     (with and without the table created by AutoMigrate first), roll it back and reapply.
  3. On SQLite, start the application on a database migrated before the change.
- **Expected:** The table, its `utf8mb4_bin` columns, the unique identity index, the
  `product_id` index and `fk_product_external_ids_product` match what `MigrateDatabase` creates;
  the products are unchanged by the up, the down and the reapply; mappings are insertable
  afterwards; the down drops only `product_external_ids`. On SQLite `MigrateDatabase` creates the
  table and a second start changes nothing.
- **Automation:** automated — `cmd/migrate/product_external_ids_migration_test.go`
  `TestProductExternalIDsMigrationFiles`, `TestProductExternalIDsMigration`;
  `TestProductExternalIDSQLiteUpgrade`. The MySQL root skips without a server; registering it in
  the Database migrations workflow is an operator follow-up.

## 15.10 Per-line tax and gross totals (#324)

The API cases TC-PQ-030 to TC-PQ-036 are backend; the screens follow in #325 (TC-PQ-037). A line carries `tax_rate_bp` (0 to 10000, 2100 = 21 %, default 0,
writable only while the quote is a draft) and the stored, read-only `tax_cents` =
`(line_total_cents × tax_rate_bp + 5000) / 10000`, round half up. The quote carries `tax_cents`
(Σ line tax) and `gross_total_cents` (`total_cents + tax_cents`); `total_cents` stays the net total
after discount. Sources: `internal/models/quote.go` (`ComputeQuoteLineTax`, `SumQuoteTotals`,
`QuoteTotals`), `internal/models/database.go` (`backfillQuoteGrossTotals`),
`internal/service/quote_service.go`, `internal/service/quote_email.go`,
`internal/repository/quote_repository.go` (`UpdateTotals`), `internal/handler/quote_handler.go`,
`migrations/20261117120000_add_quote_tax.{up,down}.sql`.

### TC-PQ-030 — Tax rounds half up per line, and is recomputed on every line write
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A draft quote.
- **Steps:**
  1. Add a line with `quantity` 3, `unit_price_cents` 999, `discount_bp` 1250, `tax_rate_bp` 2100.
  2. Add two lines of 0.01 at `tax_rate_bp` 5000.
  3. Patch the first line's `tax_rate_bp` alone to 1500, then to 0; then to 2100 and its quantity
     to 1; delete one of the 0.01 lines.
- **Expected:** Step 1: `line_total_cents` 2622, line `tax_cents` 551, quote `tax_cents` 551 and
  `gross_total_cents` 3173. Step 2: each small line is taxed 1 on its own, the quote tax is 553 (per
  line, then summed). Step 3: tax 393, then 0 (the explicit zero is stored), then 184 on the new line
  total 874; the delete recomputes without the line. Pinned arithmetic: (1000, 2100) → 210; (1, 5000)
  → 1; (1, 4999) → 0; (333, 1500) → 50; any total at 0 bp → 0; at 10000 bp → the total; 10^12 at
  10000 bp → 10^12. `total_cents` is always Σ `line_total_cents`.
- **Automation:** automated — `internal/models/quote_tax_test.go` `TestQuoteLineTaxCompute`,
  `TestQuoteTotalsWithTax`; `internal/service/quote_tax_test.go` `TestQuoteLineTaxRounding{SQLite,MySQL}`.

### TC-PQ-031 — A tax rate out of range is refused before anything is written
- **Priority:** P0
- **Type:** validation
- **Preconditions:** A draft quote with one line, and a sent one.
- **Steps:**
  1. `POST /quotes/{id}/lines` and `PUT /quotes/{id}/lines/{lineId}` with `tax_rate_bp` −1 and 10001.
  2. Call `QuoteService.AddLine` and `UpdateLine` directly with the same values.
  3. `POST` a line without `tax_rate_bp`; `PUT` a line without it.
- **Expected:** Step 1: **400** `VALIDATION_ERROR` from the binding; the service is not called.
  Step 2: `apperrors.ErrValidation` before any transaction starts (on the sent quote too, the bound
  answers before the lock); the quote, its lines and totals are unchanged. Step 3: the add stores
  rate 0; the update keeps the stored rate; an explicit 0 clears it.
- **Automation:** automated — `internal/handler/quote_tax_test.go` `TestQuoteLineTaxRateBinding`;
  `internal/service/quote_tax_test.go` `TestQuoteTaxRateBoundsRejectedWithoutWrites`.

### TC-PQ-032 — The 10^12 cap is on the gross total; a refusal writes nothing
- **Priority:** P0
- **Type:** validation
- **Preconditions:** A draft quote.
- **Steps:**
  1. Add one line: `quantity` 1, `unit_price_cents` 8×10^11, `discount_bp` 0, `tax_rate_bp` 2500.
  2. Patch its price to 8×10^11 + 1; patch its rate alone to 2501; add another line of 0.01.
  3. On a fresh draft, add the 8×10^11 + 1 line at 2500 bp directly.
  4. On a draft whose untaxed net total is exactly 10^12, patch a rate to 1 bp.
- **Expected:** Step 1: commits with tax 2×10^11 and gross exactly 10^12. Steps 2 to 4: each is
  **400** `VALIDATION_ERROR` (`ErrValidation`): (8×10^11 + 1) × 2500 + 5000, divided by 10000, is
  still 2×10^11, so the gross is 10^12 + 1. The quote row and every line row are byte for byte what
  they were: no line inserted, no rate or tax changed, totals unchanged.
- **Automation:** automated — `internal/service/quote_tax_test.go`
  `TestQuoteGrossCapRefusalWritesNothing{SQLite,MySQL}`.

### TC-PQ-033 — A tax write on a non-draft quote is a conflict
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A quote with one line in each of sent, accepted, rejected and expired.
- **Steps:**
  1. Add a line with `tax_rate_bp` 2100; patch the existing line's `tax_rate_bp` alone.
- **Expected:** `ErrQuoteLocked` (**409** `CONFLICT`) each time; nothing is written.
- **Automation:** automated — `internal/service/quote_tax_test.go`
  `TestQuoteTaxRateNonDraftRefused{SQLite,MySQL}`.

### TC-PQ-034 — The quote email shows line tax and the net, tax and gross totals
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A sent quote with a line at 21 % and a line at 0 %; the recording mailer.
- **Steps:**
  1. `POST /quotes/{id}/email`.
- **Expected:** Each line ends with ` | tax <rate>% <tax>`, the 0 % line included
  (` | tax 0.00% 0.00 EUR`); the totals read `Subtotal:`, `Discount:`, `Net total:` (`total_cents`),
  `Tax:` (`tax_cents`) and `Gross total:` (`gross_total_cents`), in that order, with the currency.
- **Automation:** automated — `internal/service/quote_tax_test.go` `TestQuoteEmailShowsTax`;
  `internal/service/quote_email_test.go` `TestQuoteEmailDelivery`.

### TC-PQ-035 — Existing quotes read tax 0 and gross = total after the upgrade
- **Priority:** P1
- **Type:** migration
- **Preconditions:** A disposable MySQL/MariaDB schema (`TEST_MYSQL_DSN`) at `20261116120000` with a
  live and a soft-deleted quote and their lines; a SQLite file whose quote tables predate the columns.
- **Steps:**
  1. Apply `20261117120000_add_quote_tax` (fresh, populated, with and without AutoMigrate first),
     roll it back, reapply it; roll it back with one column already dropped.
  2. On SQLite, run `models.MigrateDatabase` twice.
- **Expected:** The four columns are `bigint NOT NULL DEFAULT 0` exactly as AutoMigrate creates
  them; existing lines read rate and tax 0, existing quotes tax 0 and `gross_total_cents =
  total_cents` (or `total_cents + tax_cents` where AutoMigrate's binary already wrote tax), soft-
  deleted quotes included, `updated_at` unchanged; the down removes exactly the four columns and
  keeps every row; a guarded down still succeeds with a column gone. On SQLite the second run issues
  no `UPDATE` of `quotes`.
- **Automation:** automated — `cmd/migrate/quote_tax_migration_test.go` `TestQuoteTaxMigrationFiles`,
  `TestQuoteTaxMigration`; `cmd/migrate/main_test.go` `TestMigrationFresh_FullUpAndDown`,
  `TestMigrationUpgrade_FromMainAutoMigratedSchema`; `internal/models/quote_tax_test.go`
  `TestQuoteTaxColumnsSchemaSQLite`, `TestQuoteTaxSQLiteUpgrade`, `TestQuoteTaxJSON`;
  `internal/repository/quote_tax_repository_test.go` `TestQuoteRepositoryUpdateTotalsWithTax{SQLite,MySQL}`.

### TC-PQ-036 — A sales user taxes its own draft over HTTP
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Real middleware, repositories and SQLite; a sales user with its own draft.
- **Steps:**
  1. Add and edit lines with `tax_rate_bp`; `GET /quotes/{id}` and `GET /quotes`.
  2. Send a rate out of range, a write whose gross total exceeds 10^12, and, after sending the quote,
     a taxed add and a rate patch.
- **Expected:** Step 1: the 201/200 bodies and both reads carry `tax_cents`, `gross_total_cents`
  and per line `tax_rate_bp` and `tax_cents` as integers; `total_cents` is unchanged (net). Step 2:
  **400** `VALIDATION_ERROR`, **400** `VALIDATION_ERROR` and **409** `CONFLICT`, each with the
  `quotes` and `quote_lines` tables unchanged.
- **Automation:** automated — `tests/quote_tax_test.go` `TestQuoteTaxAPISQLite`.

### TC-PQ-037 — The web UI edits the tax rate and shows line tax and the net, tax and gross totals (#325)
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin (or a sales owner) logged in; a draft quote in EUR and a product at 12.50 EUR.
- **Steps:**
  1. "Add line": quantity 2, discount 10, Tax (%) 21; then add another line leaving Tax (%) at `0`.
  2. Edit a line: the Tax (%) field shows the stored rate (2100 → `21`, 1250 → `12.5`); change it,
     then set it to `0`.
  3. Enter `100.01`, `21.555`, `-1`, `abc` or nothing in Tax (%) and submit.
  4. Submit a line while the API refuses it (409 on a quote that is no longer a draft).
  5. Read the detail page of the draft and of a sent quote, then the print page; print a quote with
     no lines.
- **Expected:** Step 1: the add body is `{product_id, quantity: 2, discount_bp: 1000, tax_rate_bp:
  2100}`, and `tax_rate_bp: 0` when the field is left at `0`. Step 2: the update body always carries
  `tax_rate_bp` with the field's value in basis points (the stored rate when unchanged, 0 when set to
  0) next to `description`, `quantity`, `discount_bp` and `unit_price_cents`. Step 3: the field shows
  "Tax must be a percentage between 0 and 100 with at most two decimals" and no request is sent.
  Step 4: the server message shows in `quote-line-error` and the dialog stays open. Step 5: the lines
  table has "Tax rate" (`quote-line-tax-rate`, e.g. `21%`, `0%` untaxed) and "Tax"
  (`quote-line-tax`, the line's `tax_cents`) between "Discount" and "Line total" on every status;
  the totals read Subtotal, Discount, "Net total" (`quote-total`, `total_cents`), "Tax"
  (`quote-tax`, `tax_cents`) and a bold "Gross total" (`quote-gross`, `gross_total_cents`), each
  the server's value even when it does not add up; an untaxed quote shows tax 0.00 and a gross equal
  to the net total. The print page shows the same columns (`quote-print-line-tax-rate`,
  `quote-print-line-tax`) and totals (`quote-print-total`, `quote-print-tax`, `quote-print-gross`),
  and its "No lines" row spans seven columns. Against the real API (2 × 12.50, 10 % off, 21 %): line
  tax 4.73, net 22.50, tax 4.73, gross 27.23; at 0 % tax 0.00 and gross 22.50. The quotes list and
  the deal's Quotes section still show the net total.
- **Automation:** automated — `gocrm-ui/src/pages/quotes/QuoteLineDialog.test.tsx` ("QuoteLineDialog
  tax rate …", 6 tests); `QuoteDetail.tax.test.tsx` ("QuoteDetail tax …", 4 tests);
  `QuotePrint.tax.test.tsx` ("QuotePrint tax …", 3 tests); the updated `QuoteDetail.test.tsx` bodies;
  browser: `gocrm-ui/e2e/tests/admin-quotes-tax.spec.ts` (the add, the edit to 0 and back, the sent
  quote and its print page). `admin-quotes.spec.ts` runs unchanged.

## 15.11 Fractional line quantities in the web UI (#346)

Since #343 the API takes a line quantity as an exact decimal from 0.001 to 1,000,000 with at most
three decimals and answers a refused one with 400 `VALIDATION_ERROR`, `error.details.quantity`.
The web UI follows in #346: the line dialog checks the text with `isValidQuoteQuantity` and sends
`Number(text)`; the detail and print pages render `formatQuantity` (`quoteFormat.ts`). Line and
quote totals are always the server's; the browser computes none of them.

### TC-PQ-038 — The web UI adds and edits fractional quantities and shows them without trailing zeros (#346)
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin (or a sales owner) logged in; a draft quote (id 42) in EUR and a product
  (id 1) at 12.50 EUR.
- **Steps:**
  1. Open "Add line": read the Quantity field. Pick the product, type `1.5`, leave discount and tax
     at `0`, add.
  2. Add lines with `0.001`, `1000000`, `1000000.000` and `999999.999`.
  3. In "Add line" and in "Edit line", type `0`, `0.000`, `0.0001`, `1.2345`, `1.5000`,
     `1000000.001`, `1000001`, `-1`, `+1`, `.5`, `1.`, `1e3`, `1,5`, `abc` or nothing and submit.
  4. Open "Edit line" on a stored line with quantity 2.5; change it to `0.125` and save.
  5. Submit an add and an edit while the API answers 400 `VALIDATION_ERROR` with a string
     `error.details.quantity`; then edit the field.
  6. Submit while the API answers 400 `VALIDATION_ERROR` keyed on another field, or another code
     (409 `CONFLICT`).
  7. Read the detail and print pages of a quote whose lines hold 1.5, 0.001 and 2.
- **Expected:** Step 1: the input is `type="text"`, `inputmode="decimal"`, `name="quantity"`, with
  no `min`, `max` or `step`, and its helper text reads "From 0.001 to 1,000,000, up to three
  decimals"; the add body is exactly `{product_id: 1, quantity: 1.5, discount_bp: 0, tax_rate_bp:
  0}`. Step 2: the bodies carry `quantity` 0.001, 1000000, 1000000 and 999999.999 (JSON numbers;
  surrounding spaces are trimmed). Step 3: no request is sent; the field is invalid and its helper
  text is exactly "Quantity must be a number from 0.001 to 1,000,000 with at most three decimals".
  Only a dot is a decimal separator, no thousands separator, sign or exponent is accepted, and
  trailing zeros past the third decimal (`1.5000`) are refused locally although the API accepts
  them by value. Step 4: the field is prefilled `2.5`; the `PUT /quotes/42/lines/{lineId}` body
  carries `quantity` 0.125 next to `description`, `discount_bp`, `tax_rate_bp` and
  `unit_price_cents`. Step 5: on POST and on PUT the API's message is the Quantity helper text, the
  field is invalid, `quote-line-error` does not render and the dialog stays open; editing the field
  clears the message and restores the default hint. Step 6: `quote-line-error` shows the API
  message (`Validation failed`, `quote is not a draft`) and the Quantity field is not invalid.
  Step 7: the quantity cells (`quote-line-quantity`, `quote-print-line-quantity`) read `1.5`,
  `0.001` and `2` (shortest decimal, no trailing zeros, no exponent, no thousands separator); the
  line totals (`quote-line-total`, `quote-print-line-total`), subtotal, discount, net, tax and gross
  totals are the server's values even when they differ from any client-side product. The quotes
  list and the deal's Quotes section show no line quantity and are unchanged.
- **Automation:** automated — `gocrm-ui/src/pages/quotes/QuoteLineDialog.test.tsx` ("QuoteLineDialog
  quantity …", 7 tests: "sends quantity 1.5 when adding a line", "accepts the bounds 0.001 and
  1000000 and sends them as numbers", "refuses a quantity outside 0.001 to 1,000,000 or with more
  than three decimals without calling the API", "prefills the edit form with the stored fractional
  quantity and sends the changed value", "shows the API quantity message on the Quantity field
  instead of the alert", "keeps a refusal without details.quantity in the alert", "uses a text input
  with decimal input mode named quantity and shows the range hint"); `quoteFormat.test.ts`
  ("quoteFormat formats a quantity without trailing zeros", "quoteFormat validates quantity text
  from 0.001 to 1000000 with at most three decimals", "quoteFormat keeps a three-decimal quantity
  exact through Number and JSON"); `QuoteDetail.test.tsx` ("QuoteDetail shows fractional line
  quantities without trailing zeros and the server's line and quote totals", and "QuoteDetail
  refuses an invalid discount and quantity locally" with the new message); `QuotePrint.test.tsx`
  ("QuotePrint prints fractional line quantities without trailing zeros and the server's totals").
  Local SQLite browser validation (not a frozen acceptance check):
  `gocrm-ui/e2e/tests/admin-quotes-fractional-quantity.spec.ts`, "Admin - Quote fractional
  quantities" › "admin adds and edits a fractional line quantity and sees it without trailing zeros
  on the detail and print pages" (add 1.5, edit to 0.125, a local refusal of `1.2345` with no
  request, a fulfilled 400 with `details.quantity` on the field, and the print page).
  `admin-quotes.spec.ts` and `admin-quotes-tax.spec.ts`, which fill whole quantities, run
  unchanged.

## 15.12 Client response link (#382, backend)

Since #382 the email of a sent quote ends with a single-use link,
`APP_BASE_URL/quote-response?token=<token>`. Only the HMAC-SHA256 hash of the token is stored
(`quote_response_tokens`). Two unauthenticated routes on the strict rate tier answer it:
`GET /quotes/public/response?token=` shows the quote and never writes; `POST
/quotes/public/response` `{token, decision}` claims the token and moves the quote `sent → accepted
| rejected` in one transaction. Every refusal is the same 400 `BAD_REQUEST` "Invalid or expired
quote link". There is no page behind the link yet, so these cases are API and service level.

Sources: `internal/service/quote_email.go` (`mintResponseLink`), `internal/service/quote_response.go`,
`internal/repository/quote_response_token_repository.go`, `internal/handler/quote_public_handler.go`,
`internal/handler/quote_public_routes.go`, `internal/models/quote_response_token.go`,
`migrations/20261121120000_add_quote_response_tokens.{up,down}.sql`.

### TC-PQ-039 — The email of a sent quote carries a single-use response link
- **Priority:** P0
- **Type:** functional, security
- **Preconditions:** A sent quote with lines and a live customer; the recording mailer; logs captured at debug level.
- **Steps:**
  1. `POST /quotes/{id}/email`; read the mail body and the `quote_response_tokens` rows of the quote.
  2. Email the same quote again.
  3. Email an accepted quote, a sent quote whose `valid_until` was yesterday, and a sent quote without `valid_until`.
  4. Make the mailer fail and email a sent quote.
- **Expected:** Step 1: the body is the rendering of the quote followed by one final block with
  `<APP_BASE_URL>/quote-response?token=<token>` (no doubled slash when `APP_BASE_URL` ends in one);
  exactly one row with `used_at` NULL exists, its `token_hash` is the HMAC-SHA256 hex of the mailed
  token, `expires_at` is the start of the day after `valid_until` in the server's local zone, and
  no log line contains the token, its hash or the link. Step 2: a new token; the first row has
  `used_at` set and `decision` NULL, one row is unused, and the first link is rejected. Step 3: the
  accepted quote and the quote past `valid_until` are mailed without a link and store no row; the
  quote without `valid_until` gets a row with `expires_at` NULL. Step 4: 502
  `MAIL_DELIVERY_FAILED`; the error text does not contain the token; the stored row stays and the
  next email stamps it.
- **Automation:** automated — `internal/service/quote_email_test.go` `TestQuoteEmailResponseLink`.

### TC-PQ-040 — A quote that changed before the link is stored is refused and nothing is sent
- **Priority:** P1
- **Type:** functional, concurrency
- **Preconditions:** A sent quote; a status change committed between the service's read of the quote and the transaction that stores the token.
- **Steps:**
  1. Let the quote move to accepted, or to draft, in that window, then let the email continue.
  2. Let the quote move to draft, gain a line and move to sent again in that window.
  3. Let the quote move to draft and back to sent while the mail is in flight.
- **Expected:** Step 1: 409 (`ErrQuoteNotSent`), no token row, no mail. Step 2: 409
  (`ErrQuoteChangedDuringSend`), no token row, no mail; a later email mails a link to the revised
  quote. Step 3: 409 as before #382 (the mail has gone out), and the link in that mail is already
  rejected because the transition stamped its token.
- **Automation:** automated — `internal/service/quote_email_test.go` `TestQuoteEmailResponseLink`.

### TC-PQ-041 — The link shows the quote without spending the token
- **Priority:** P0
- **Type:** functional, security
- **Preconditions:** A sent quote with two lines, emailed; no credentials.
- **Steps:**
  1. `GET /quotes/public/response?token=<token>` twice.
  2. The same with an unknown token, an empty token and no token.
  3. The same after the token was used, and with the clock past the end of the `valid_until` day.
- **Expected:** Step 1: 200 with exactly `number`, `status` (`sent`), `currency`, `valid_until`
  (`YYYY-MM-DD`, omitted when unset), `lines` (each `description`, `quantity`, `unit_price_cents`,
  `discount_bp`, `line_total_cents`, `tax_rate_bp`, `tax_cents`) and `subtotal_cents`,
  `discount_cents`, `total_cents`, `tax_cents`, `gross_total_cents`; no id, title, notes, owner,
  customer, deal or product reference; the token row is unchanged (`used_at` NULL) after both
  calls. Steps 2 and 3: 400, `error.code` `BAD_REQUEST`, message `Invalid or expired quote link`.
  No log line contains the token.
- **Automation:** automated — `internal/service/quote_response_test.go` `TestQuoteResponseView`;
  `internal/handler/quote_public_handler_test.go` `TestQuotePublicView`.

### TC-PQ-042 — The client accepts or declines through the link
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A sent quote of a deal, emailed; no credentials.
- **Steps:**
  1. `POST /quotes/public/response` with `{token, decision: "maybe"}`, then with a malformed body and with `token` missing.
  2. `POST` with `decision: "accept"` (and, on another quote, `"decline"`).
  3. `POST` again with the same token; `GET` with it.
- **Expected:** Step 1: 400 `VALIDATION_ERROR`, decided before any token lookup; the token stays
  usable. Step 2: 200 with the public view and `status` `accepted` (`rejected`); the quote has
  `closed_at` set and `sent_at` kept; exactly one new `quote_status_changes` row (`sent →
  accepted | rejected`, `changed_by_id` = the quote owner); the token row has `used_at` set and
  `decision` `accept` (`decline`); the deal's `stage` and `closed_at` are unchanged. Step 3: the
  generic 400, and no quote, history or token row changes.
- **Automation:** automated — `internal/service/quote_response_test.go` `TestQuoteResponseDecision`;
  `internal/handler/quote_public_handler_test.go` `TestQuotePublicRespond`.
- **Known issue:** the history row names the quote owner, so `GET /quotes/{id}/history` shows a
  client answer as the owner's (`docs/ROADMAP.md`).

### TC-PQ-043 — Two answers with one token commit exactly one
- **Priority:** P0
- **Type:** concurrency
- **Preconditions:** A sent quote whose `valid_until` is today, emailed. SQLite on the one-connection pool; MySQL 8 and MariaDB 10.11 on a disposable `TEST_MYSQL_DSN` schema.
- **Steps:**
  1. Start two `RespondPublic` calls with the same token behind one barrier (accept against decline, and accept twice).
  2. Race one `RespondPublic` against a staff `ChangeStatus` to rejected.
  3. Email two different sent quotes at the same moment, each for the first time and then both again.
- **Expected:** Step 1: one call succeeds, the other is `ErrQuoteResponseRejected`; one committed
  history row; the token row carries the winner's decision; the deal is unchanged. Step 2: exactly
  one of the two commits; when staff wins the token is stamped with `decision` NULL and the client
  is rejected; when the client wins staff gets `INVALID_STATUS_TRANSITION`. Step 3: both emails
  succeed on every engine (no deadlock between the token writes of two quotes) and each quote ends
  with exactly one unused token row.
- **Automation:** automated — `internal/service/quote_response_regression_test.go`
  `TestQuoteResponseConcurrent{SQLite,MySQL}`.

### TC-PQ-044 — A failure after the claim keeps the link usable
- **Priority:** P1
- **Type:** functional, transaction
- **Preconditions:** A sent quote, emailed; the history insert fails once.
- **Steps:**
  1. `RespondPublic(token, accept)` with the failing history write.
  2. `RespondPublic(token, accept)` again.
- **Expected:** Step 1: an error that is not the generic rejection (HTTP 500) and does not contain
  the token; the quote is still sent with `closed_at` NULL, no history row was added and the
  token's `used_at` is NULL. Step 2: success.
- **Automation:** automated — `internal/service/quote_response_test.go`
  `TestQuoteResponseRollbackKeepsLink`.

### TC-PQ-045 — Every way a link dies
- **Priority:** P0
- **Type:** functional, security
- **Preconditions:** Sent quotes, emailed; a settable service clock.
- **Steps:**
  1. Clock at 23:59:59 local on the `valid_until` day, then at 00:00:00 local the next day.
  2. Staff move the quote from sent to accepted, rejected, expired or draft.
  3. Staff move it to draft and to sent again without a new email; then email it again.
  4. A quote without `valid_until`, the clock ten years on.
  5. An admin deletes the sent quote.
- **Expected:** Step 1: `GET` and `POST` succeed at 23:59:59 and are both rejected from 00:00:00;
  `expires_at` is the start of the next local day whatever location the stored date was read in.
  Step 2: the transition stamps the token (`decision` NULL) and both calls are rejected. Step 3:
  the old link stays rejected while the quote is sent again; the link of the new email works.
  Step 4: the link still works. Step 5: both calls are rejected and the token row is unchanged.
  Every rejection leaves the quote, history, token and deal rows unchanged.
- **Automation:** automated — `internal/service/quote_response_test.go`
  `TestQuoteResponseDeadLinks`; `internal/repository/quote_response_token_repository_test.go`
  `TestQuoteResponseTokenRepository`.

### TC-PQ-046 — The public routes sit next to the guarded quote routes; the migration
- **Priority:** P1
- **Type:** security, migration
- **Preconditions:** The public and the protected route groups on one engine; MySQL 8 and MariaDB 10.11 for the migration.
- **Steps:**
  1. `GET` and `POST /quotes/public/response` without credentials; `GET /quotes/1` and `GET /quotes/public` without credentials.
  2. Apply `20261121120000_add_quote_response_tokens` on a fresh schema, on the previous head, and on a schema `MigrateDatabase` built first; roll back; reapply.
- **Expected:** Step 1: the two public routes reach the public handler; `/quotes/1` and
  `/quotes/public` (the `:id` route) answer 401; no other path under `/quotes/public` exists.
  Step 2: the table equals what `MigrateDatabase` builds (unique `idx_quote_response_tokens_token_hash`,
  `idx_quote_response_tokens_quote_id`, no foreign key), rows created first are kept, rollback
  drops only this table, and no existing table is altered.
- **Automation:** automated — `internal/handler/routes_test.go`
  `TestQuotePublicRoutesWinOverTheIDParameter`, `TestAllRouteSetupsCoexist`;
  `cmd/migrate/quote_response_tokens_migration_test.go` `TestQuoteResponseTokensMigrationFiles`,
  `TestQuoteResponseTokensMigration`.
