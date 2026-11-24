# Extending GopherCRM

A cookbook for application developers and coding agents who add to, or cut from, GopherCRM. Each
recipe names a reference implementation to copy, the files to touch in order, the tests that prove
the change, and the gate that will check it.

This guide is the short entry point. [DEVELOPMENT.md](DEVELOPMENT.md) is the long-form contract
for every pattern named here; when the two disagree, DEVELOPMENT.md and the code win. For running
and deploying a fork see [FORKING.md](FORKING.md); for choosing a starting point see
[USE_CASES.md](USE_CASES.md).

Counts in this guide were measured on `a51e336f`, with the command next to each one.

## 1. Rules that protect you

Most rules here are enforced in code, so a change that copies a neighbouring file inherits them.
Break one and a reviewer will flag it as **Important** under [REVIEW.md](../REVIEW.md).

| Rule | Where it lives | Long form |
|---|---|---|
| Every JSON response uses the `{success, data, error, meta}` envelope, sent only through `utils.Respond*`. The existing non-JSON contracts stay as they are: the public embed script and the hosted and confirmation pages (`c.Data` in `FormPublicHandler.EmbedScript`, `FormPublicHandler.ViewPage`, `FormPublicHandler.ConfirmPage` and `FormPublicHandler.Confirm` (`internal/handler/form_public_handler.go`)), the customer CSV export (`CustomerHandler.Export` (`internal/handler/customer_handler.go`)), the lead CSV export (`LeadHandler.Export` (`internal/handler/lead_handler.go`)), and deletes that answer an empty 204 (`CustomerHandler.Delete` (`internal/handler/customer_handler.go`)) | `internal/utils/response.go` | DEVELOPMENT.md, "Unified API response" |
| Errors are sentinels classified with `errors.Is`; misses are wrapped as `fmt.Errorf("<entity> %d not found: %w", id, ...)`; `apperrors.IsNotFound` covers raw `gorm.ErrRecordNotFound` too | `internal/errors/errors.go` | "Error types" |
| Sort columns come only from a per-entity allowlist | `utils.ValidateSort`, `utils.AllowedSortColumns` in `internal/utils/sort.go` | "Sort validation" |
| Lists parse paging through `utils.ParseOffsetLimit` and walk a total order (sort column, then `id`) | `ParseOffsetLimit` (`internal/utils/response.go`), `internal/repository/page_order.go` | "Pagination", "Paginated reads walk a total order" |
| Unpaginated reads are one ordered statement, never `FindInBatches` with a non-id order | `customerRepository.ListAllForExport` (`internal/repository/customer_repository.go`), `leadRepository.ListAllForExport` (`internal/repository/lead_repository.go`) | "Unpaginated reads must not mix a non-id `ORDER BY` with `FindInBatches`" |
| Multi-step writes run in `TransactionManager.WithTransaction`; nested calls reuse the transaction under a savepoint | `TransactionManager.WithTransaction` (`internal/utils/transaction.go`) | "Transaction management" |
| Transactions that lock rows of more than one table lock them in the one documented order and retry only the whole root | `internal/utils/transaction.go`, `internal/service/lead_company_lock.go` | [CONCURRENCY.md](CONCURRENCY.md) |
| A single-use token is spent by a conditional `UPDATE` inside the transaction that does the dependent writes; mail goes out after the commit | password reset, refresh rotation, form confirmation | "Authentication", from "A password reset link is spent only by a committed reset" |
| Lead visibility has one policy, by the caller's `leads.view` scope: `all` sees every lead, `own` the caller's, not held none (on the system matrix: admin, sales, every other role) | `service.CanViewLead`, `RedactTaskLead(s)`, `RedactDealLead(s)` in `internal/service/lead_visibility.go` | code comments in that file |
| A task's deal link has one policy, by the caller's `deals.view` scope: `all` sees every deal, `own` the caller's, not held none (on the system matrix: admin, sales, every other role). A handler returning a task redacts it again on every read | `service.CanViewDeal`, `RedactTaskDeal(s)`, `checkDealLink` in `internal/service/deal_visibility.go` | code comments in that file |
| Service logs carry ids, statuses and counts, never emails, names, titles, search terms or raw sort values; access logs omit query strings | `internal/service/logging_privacy_test.go`, `internal/handler/logging_privacy_test.go` | end of "Deleting personal data" |
| Deleting a person overwrites every personal field, then soft-deletes, in one transaction | `internal/repository/erasure.go`, `erasure_cascade.go` | [Deleting personal data](DEVELOPMENT.md#deleting-personal-data) |
| A schema change ships both halves: GORM AutoMigrate and a `migrations/` up/down pair | `internal/models/database.go` `autoMigrateModels`, `migrations/`, `cmd/migrate/schema_parity_migration_test.go` | [Database](DEVELOPMENT.md#database) |
| Duplicate keys become a sentinel, not a driver string | `internal/repository/duplicate_key.go` | "Duplicate email" |

Two review rules apply to every recipe (`REVIEW.md`, "Tests"):

- the change is proven by a test that fails without it;
- behaviour that passes on SQLite but fails on MySQL 8 or MariaDB 10.11 is a defect. The test suite
  runs on SQLite; tests that need a real server read `TEST_MYSQL_DSN` and **skip** without it
  (DEVELOPMENT.md, [Gotchas](DEVELOPMENT.md#gotchas)).

## 2. Before you start

1. **Find the reference.** Every recipe below names the closest existing implementation. Copy its
   shape, then change names. Products (`internal/models/product.go` through
   `gocrm-ui/src/pages/products/`) is a recent, complete entity and the reference for most of this
   guide.
2. **Know which gate your diff will select.** Do not guess a profile.
   `automation/langflow/gophercrm_runner/validation.py` chooses one from the changed paths. Run it
   on your real diff:

   ```bash
   python3 automation/langflow/gophercrm_runner/validation.py \
     --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
   ```

   Any path under `SHARED_PREFIXES` or in `SHARED_FILES` (`SHARED_PREFIXES` and `SHARED_FILES` in
   `automation/langflow/gophercrm_runner/validation.py`: `migrations/`,
   `cmd/`, `internal/models/`, `internal/config/`, `internal/middleware/`, `internal/utils/`,
   `internal/handler/routes.go`, both `interfaces.go` files, `go.mod`, the frontend `routes/`,
   `layouts/`, `types/` and `components/` folders, among others) is shared infrastructure. Outside
   the narrow exceptions of the bounded profiles it selects `full`. A new entity always touches
   several of these paths, so expect `full`.
3. **Know which docs your diff touches.** The documentation-impact planner lists them:

   ```bash
   PYTHONPATH=automation/langflow python3 -m gophercrm_runner.documentation \
     --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
   ```

   See [DOCUMENTATION_WORKFLOW.md](DOCUMENTATION_WORKFLOW.md). Update the listed documents in the
   same pull request, or say why each one is still accurate.

## 3. Add an entity end to end

Reference: products. The file list is in build order; each layer sits behind an interface, and
dependency injection is manual.

### 3.1 Model

- `internal/models/<entity>.go`. Give every string column an explicit width
  (`type:varchar(N)`), and export the limit as a constant the service checks.
- Pin widths with a test like `TestProductColumnWidthsMatchConstants`
  (`internal/models/product_test.go`), so a widened column and a stale constant cannot drift.
- A boolean or number that may legitimately be `false` or `0` gets **no** GORM `default:` tag: a
  default overwrites an explicit zero value on create. The handler DTO owns the default with a
  pointer (`*bool`). `TestProductPriceAndActiveFlagHaveNoColumnDefault` pins this for products.

### 3.2 AutoMigrate (the SQLite half)

Add the model to `internal/models/database.go` `autoMigrateModels`, after the models it
references (products and quotes sit after the comment "Product and Quote before QuoteLine"). On SQLite this is the only schema path;
`cmd/migrate` refuses database commands there (`ErrSQLiteUnsupported` (`cmd/migrate/main.go`)).

### 3.3 SQL migration pair (the MySQL/MariaDB half)

- Create the pair with `go run ./cmd/migrate -command create -name add_<entity>`
  (`createNewMigration` (`cmd/migrate/main.go`)). It writes
  `migrations/<version>_add_<entity>.{up,down}.sql`, where `<version>` is the newest existing
  version + 1 (the clock is used only for an empty directory). The repository holds 46 pairs (`ls
  migrations/*.up.sql | wc -l`, measured on a51e336f).
- New tables use `CREATE TABLE IF NOT EXISTS`, with column, index and constraint names identical to
  what AutoMigrate produces. The new binary may already have created the table before the operator
  runs the migration. Copy the header and body of
  `migrations/20261026120000_add_products_and_quotes.up.sql`.
- Do not alter existing tables in the same migration as new ones. Adding a foreign key to an
  existing table makes the SQLite side rebuild that table; that needs its own upgrade fixture test.
- Never edit a migration that has shipped. Add a new pair instead.
- Add the table to `expectedTables` in `TestMigrationFiles_PairsAndCompleteness`
  (`cmd/migrate/main_test.go`). Add a migration test modelled on `TestProductsAndQuotesMigration`
  (`cmd/migrate/products_quotes_migration_test.go`): fresh install, AutoMigrate first, schema
  parity, rollback.

**On a fork:** migration versions are numeric, and golang-migrate (`main` (`cmd/migrate/main.go`), through `migrate.NewWithDatabaseInstance`)
does not apply a file numbered below the newest version a database has already applied. Merge
upstream first, give fork migrations versions above upstream's newest, renumber unreleased
fork migrations after every upstream sync, and rehearse a populated-database upgrade before
release. [FORKING.md](FORKING.md) covers upstream sync in full.

### 3.4 Repository

- Declare the interface in `internal/repository/interfaces.go`; implement it in
  `internal/repository/<entity>_repository.go`. `productRepository` is the template.
- Provide `WithTx(tx *gorm.DB) <Entity>Repository` so the service can bind the repository to a
  transaction (`product_repository.go`, last function).
- List: validate the sort with `utils.ValidateSort("<table>", sortBy, sortOrder)` after adding the
  table to `AllowedSortColumns` (`internal/utils/sort.go`), then order by the column and by `id`
  in the same direction. Use `orderPage` / `orderByColumnThenID` from `page_order.go` when the
  endpoint has a "no sort" default. Search filters are bound parameters, never interpolated
  (`productSearchClause`).
- A delete that matched no row returns `gorm.ErrRecordNotFound` (`productRepository.Delete`).
- A unique column wraps the driver error into a sentinel through `duplicate_key.go`. GORM's
  `ErrDuplicatedKey` never fires here (DEVELOPMENT.md, [Gotchas](DEVELOPMENT.md#gotchas), "Do not rely on
  `gorm.ErrDuplicatedKey`").
- Tests: `internal/repository/<entity>_repository_test.go` on SQLite, opened with
  `testsupport.OpenSQLite`, covering filters, sort, rejection of an unknown sort column and
  soft-delete hiding (copy
  `TestProductRepository_ListRejectsUnknownSortColumns`). If the entity has a paginated list, add
  it to the fixtures of `TestPaginationDeterminism{SQLite,MySQL}`
  (`internal/repository/pagination_determinism_test.go`).

### 3.5 Service

- Interface in `internal/service/interfaces.go`, implementation in
  `internal/service/<entity>_service.go`.
- Validate input and wrap with sentinels: `fmt.Errorf("... : %w", apperrors.ErrValidation)` and
  `fmt.Errorf("product %d not found: %w", id, apperrors.ErrProductNotFound)`
  (`validateProduct` and `productService.GetByID` in `internal/service/product_service.go`). Declare new sentinels in
  `internal/errors/errors.go` next to the existing ones.
- Multi-step writes take `*utils.TransactionManager` and run through
  `internal/service/txunit.go` `txDeps.Run` (built with `newTxDeps(tm)`), which opens one
  transaction and hands the callback the transaction, the `WithTx` repositories and the webhook
  and automation outlets bound to it. Which lock or retry primitive a write needs is in the
  decision table of [DEVELOPMENT.md](DEVELOPMENT.md#transactions-and-row-locks). `QuoteService` is the reference for conditional claims in one transaction
  (DEVELOPMENT.md, "Quotes: conditional claims, one transaction each").
- Record-level visibility (who sees which row) belongs here or in the repository query, applied
  **before** pagination, so counts and pages agree. If the entity can link a lead, apply
  `CanViewLead` on write and redact on every read; never write a separate role check for a lead
  link.
- Log operation names, ids and counts only. `internal/service/logging_privacy_test.go` is the
  pattern for asserting that a log line carries no personal data.

### 3.6 Handler and Swagger

- `internal/handler/<entity>_handler.go`: parse with `ShouldBindJSON`, page with
  `utils.ParseOffsetLimit`, answer through `utils.Respond*`, and map errors with `errors.Is` /
  `apperrors.IsNotFound` (the error switch in `ProductHandler.respondError` (`internal/handler/product_handler.go`)).
  Handlers never import gorm.
- Annotate each handler (`@Summary`, `@Security BearerAuth`, `@Security ApiKeyAuth`, `@Router`;
  see `ProductHandler.Create` (`internal/handler/product_handler.go`)). Annotations describe what the code does, including its warts.
- Regenerate the spec with `make swagger` (writes `api/swagger.json` and `api/swagger.yaml`).

### 3.7 Routes and wiring

- Add `Setup<Entity>Routes` to `internal/handler/routes.go`. Start it with
  `routes := Protected(router)` and register every route and sub-group through `routes`
  (`SetupProductRoutes` (`internal/handler/routes.go`)); the route files name no permission.
  `handler.Protected` builds each route's guard from its `permissions.RouteKeys` entry
  (`internal/permissions/routes.go`), so add one entry per route there (products: reads
  `products.view`, writes `products.create`/`update`/`delete`). A route without an entry panics at
  registration.
- In `setupDependencies` (`cmd/main.go`): construct the repository with `models.DB`
  (`repository.NewProductRepository`), the service (`service.NewProductService`), the handler
  (`handler.NewProductHandler`), and register the routes in the `protected` group
  (`handler.SetupProductRoutes(protected, productHandler)`). There is no container; the wiring is plain code.
- If a static path sits next to a `/:id` route, extend `TestAllRouteSetupsCoexist`
  (`internal/handler/routes_test.go`).
- The system-role route matrix, `cmd/testdata/system_role_route_matrix.json`, is frozen: the
  golden is not regenerated. List each new route on
  `cmd/system_role_route_matrix_test.go` `routeMatrixAfterFreeze`, declare it in `matrixRequests`
  (fixture kind, valid body, note; seed a fixture kind for the entity in `seedRouteMatrix`) and give
  it a `permissions.RouteKeys` entry, as in §4. A route that is neither recorded, nor listed after
  the freeze, nor on the test's public list fails `TestRouteMatrixCoversEveryProtectedRoute`
  (DEVELOPMENT.md, "System-role route matrix").
- Permissions for a new entity: add the entity's keys to the catalogue in
  `internal/permissions/catalogue.go` and its grants for each system role to
  `internal/permissions/system_roles.go`, then update the golden
  `internal/permissions/testdata/system_roles.golden.json`, the tables of
  [AUTHORIZATION.md](AUTHORIZATION.md) and `gocrm-ui/src/permissions/systemRoleFixture.ts` (§10).

### 3.8 Mocks

Service tests use testify mocks in `internal/mocks/` (and four repository mocks in
`internal/repository/mocks/`). Several carry a "generated" header but were written by hand
(the header of `internal/mocks/ProductRepository.go`). Add the mock for your new interface and review it: a
wrong mock lets a test pass for the wrong reason (`REVIEW.md`, last paragraph).

### 3.9 Frontend

- Types in `gocrm-ui/src/types/index.ts` (the `export interface Product` declaration).
- Endpoint module `gocrm-ui/src/api/endpoints/<entity>.ts`, exported from `endpoints/index.ts`.
  The axios client (the response interceptor in `setupInterceptors`, `gocrm-ui/src/api/client.ts`)
  unwraps the success envelope, so endpoint functions receive `data` directly. Error envelopes are
  unwrapped flat ("Unwrap error response if it's in the wrapped format" in the same interceptor); read them with
  `getAPIError` (`src/utils/apiError.ts`), which returns `{code, message, details}`. Pages show a server
  message with `apiErrorMessage(error, fallback, { details })` from the same file and never read
  `error.response.data` themselves; `details` adds the field messages of a 400 answer only.
- Pages under `gocrm-ui/src/pages/<entity>/` (TanStack Query, react-hook-form with zod), lazily
  imported in `gocrm-ui/src/routes/index.tsx` behind `<ProtectedRoute requiredPermission="...">`
  with the permission key the API asks for the page's main call (products: the `products.view`,
  `products.create` and `products.update` guards). Buttons inside a page ask
  `usePermissions()` (`src/hooks/usePermissions.ts`) for the key of the call behind them.
- Navigation entry in `gocrm-ui/src/layouts/MainLayout.tsx` with the matching `permission` key
  (products: `products.view`). The keys come from `GET /users/me`; the UI check only hides
  things; the server guard is the authorization.
- Render dates through `src/utils/date.ts`, never a raw `format(new Date(x))`.
- Vitest next to each page and endpoint module (`ProductList.test.tsx`, `products.test.ts`).

### 3.10 Personal data and erasure

Decide first whether the new table stores personal data or only refers to people by id.

- **Ids only**: erasure leaves the rows in place. Deals and quotes show why "ids only" is rarer
  than it looks: they copy no field from the customer, and a quote reads its recipient at send
  time, but their title, notes, `lost_reason` and line descriptions are typed by staff and can name
  the client. Since #245 the erasure reaches them through a step of the cascade
  (`internal/repository/business_text_erasure.go`, run by `eraseGraph` once the person's rows have
  been traversed): the linked deals and quotes are kept with their links and amounts and their
  text is overwritten. A new table with free-text columns belongs in the next bullet.
  "Ids only" does not always mean "nothing to do" either: `quote_response_tokens` holds a hash,
  ids and timestamps, yet a row is a credential mailed to the erased person, so since #395 the
  same step stamps the unused rows of the erased customers' quotes (kept, no longer usable;
  `internal/repository/erasure_quote_tokens_test.go`). A table of links, tokens or
  subscriptions addressed to a person needs the same question.
  For a table that really holds ids only, still prove it. Add the table to the list in
  `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates`
  (`test/integration/erasure_pii_sweep_test.go`), and add a canary test like
  `TestThePersonalDataSweepCoversQuoteTables` (`test/integration/erasure_pii_sweep_test.go`) that writes the person into the table and
  asserts the sweep sees it. A sweep that cannot see a table proves nothing; the quote canary
  proves the sweep can see quote rows, and `business_text_erasure_sweep_test.go` proves the
  erasure clears them.
- **Personal fields** (names, email, phone, address, free text typed by or about a person): the
  erasure must reach them in the same transaction. Staff-typed text about a person counts. Either extend a plan in
  `internal/repository/erasure_cascade.go` (`leadErasurePlan`; `customerErasurePlan` and
  `userErasurePlan` live in their repositories), using `Scrub` for columns and `AfterScrub` /
  `BeforeScrub` for rows outside the entity's own table (as `scrubLeadFormSubmissions` does), or add
  a step to the cascade (as `scrubBusinessText` does for deals and quotes and, last, for the
  response tokens of the erased customers' quotes, probing each table with
  `tx.Migrator().HasTable`). A unique column gets a placeholder, never a blank. Rows keyed by an
  external identifier under a unique key must be **deleted**, not blanked.
- Extend `TestIdentityErasure{SQLite,MySQL}` (`internal/repository/identity_erasure_test.go`) and
  the sweep tests so the person is seeded into the new table before the erasure.
- Update the matrix in [DEVELOPMENT.md](DEVELOPMENT.md#deleting-personal-data). Known gaps you
  inherit: ticket free text, which the erasure keeps and an operator redacts first through the
  documented procedure (#214), tasks rendered from `{{deal.title}}` before an erasure (they keep
  the text; deal and quote text itself is scrubbed since #245), and what the erasure lock order of
  #385 leaves open under #183 (the governed user write still locks against it in the opposite
  order; see [CONCURRENCY.md](CONCURRENCY.md)). A new table that an
  erasure must lock goes into that order, after the tables it depends on, by primary key only. A new table that copies a person's email without a link to
  the erased row can follow `scrubUnlinkedSubmissions` (`internal/repository/erasure.go`).

### 3.11 Tests at every layer

| Layer | Where | Reference |
|---|---|---|
| Model widths and defaults | `internal/models/<entity>_test.go` | `product_test.go` |
| Repository (SQLite) | `internal/repository/<entity>_repository_test.go` | `product_repository_test.go` |
| Anything transactional or driver-specific (SQLite and MySQL roots) | `...{SQLite,MySQL}` test pairs reading `TEST_MYSQL_DSN` | `internal/utils/transaction_regression_test.go`, `quote_status_regression_test.go` |
| Service with mocks | `internal/service/<entity>_service_test.go` | `product_service_test.go` |
| Handler and role matrix over HTTP | `internal/handler/<entity>_handler_test.go` | `product_handler_test.go` |
| Visibility across roles | `internal/integration/` | `ticket_visibility_integration_test.go` |
| Migration | `cmd/migrate/<entity>_migration_test.go` | `products_quotes_migration_test.go` |
| Erasure | `test/integration/erasure_pii_sweep_test.go` | section 3.10 |
| Frontend units | next to the component | `ProductList.test.tsx` |
| Browser | `gocrm-ui/e2e/tests/` | `admin-quotes.spec.ts` |
| Screenshots (user-facing pages) | `gocrm-ui/e2e/screenshots/` | `11-quotes.spec.ts` |

Run the MySQL roots locally before claiming parity, with `TEST_MYSQL_DSN` pointing at a disposable
server (`user:pass@tcp(host:port)/`, no database name; each root creates and drops its own
schema). A root that skipped proves nothing.

### 3.12 Documentation and catalog cases

- `CHANGELOG.md` under `[Unreleased]`; `docs/FEATURES.md` (a row per capability with its tests);
  `docs/datamodel.md` for the new table; `docs/ROADMAP.md` for deliberate cuts.
- A test-case file in `docs/testing/` (`NN-<area>.md`) with `TC-<AREA>-NNN` cases that state
  observed behaviour, as in `15-products-quotes.md`, plus a row in the totals table of
  `docs/testing/README.md`.
- The planner suggests a feature's catalog file only when its name appears in `FEATURE_DOCS`
  (`automation/langflow/gophercrm_runner/documentation.py`). Add the new area there, with a
  mapping test, or the planner will not point reviewers at it.

## 4. Add an endpoint or a screen to an existing entity

- **Endpoint.** Handler method with annotations, route in the entity's `Setup...Routes` with its
  guard, service method behind the interface, `make swagger`. Test the denied roles and the record
  scope, not only the success path. Static segments next to `/:id` go into `routes_test.go`.
  Bulk changes follow `SetupBulkStatusRoutes` (`internal/handler/routes.go`): at most 100 ids, all or nothing,
  per-item authorization (DEVELOPMENT.md, "Bulk endpoints"). A new protected route also goes into the
  route tests of `cmd`: the system-role golden is frozen and is not regenerated, so list the route
  on `routeMatrixAfterFreeze`, give it a `matrixRequests` entry and add its key to
  `permissions.RouteKeys` (DEVELOPMENT.md, "System-role route matrix"). A change to what an
  existing route answers to a system role fails the replay of the golden.
- **Screen.** Endpoint function, page, lazy route, nav entry, Vitest, and an e2e spec for a
  user-visible flow (`REVIEW.md`). Frontend endpoint functions that call routes the backend lacks
  are a to-do list, not dead code: implement the route instead of deleting the function, as
  `POST /leads/:id/assign` was (#185).

## 5. Add a setting

- Seed it in `DefaultConfigurations()` (`internal/models/configuration.go`) with key, type,
  category, default and description. `configRepo.InitializeDefaults()` runs at every start
  (`main` (`cmd/main.go`)) on both drivers; re-seeding refreshes metadata but never overwrites a stored
  value (`configurationRepository.BulkUpsert` (`internal/repository/configuration_repository.go`)). Changing a default
  therefore does not touch existing installations.
- A secret gets `IsSensitive: true` (the AEO keys, the `integration.aeo.*_api_key` entries of `DefaultConfigurations` (`internal/models/configuration.go`)): encrypted at rest by
  `internal/utils/secretbox.go`, blanked in responses, read only through `GetSecret`. The key derives
  from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it orphans stored secrets.
- Everything in category `ui` reaches every authenticated user through `GET /configurations/ui`
  (`ConfigurationHandler.GetUIConfigurations` (`internal/handler/configuration_handler.go`)). Never put a sensitive
  key there.
- Ship the consumer and its test in the same change. A seeded setting nobody reads is a defect.
  To retire one, remove it from `DefaultConfigurations`, add its key to
  `models.RetiredConfigurationKeys` (startup deletes it) and ship a migration pair that deletes
  it on MySQL, as #224 did for `tickets.auto_assign_support` and
  `security.session_timeout_hours`.
- Security toggles fail closed on a read error (`middleware.RequireRegistrationEnabled`).

## 6. Add an automation event or step

Read [Automation runtime](DEVELOPMENT.md#automation-runtime) first. The vocabulary lives in
`internal/models/automation.go`: ten event types plus the `schedule` trigger (the `Event types.` const
block), five origins (`Event origins.`) and six step types (`Step types.`).

**A new step type:**

1. Constant in `internal/models/automation.go`. `step_type` is `varchar(20)` (`internal/models/automation.go` `AutomationStep.StepType`), so keep the
   name short.
2. Validation in `validateStep` (`internal/service/automation_validation.go`).
3. Dry run in `AutomationEngine.DryRun` (`internal/service/automation_dry_run.go`).
4. Execution in `AutomationEngine.planStep` (`internal/service/automation_steps.go`): build a `stepPlan` whose
   `effect` writes through the transaction-bound repositories, so it commits or rolls back with the
   run's progress. Mail is the exception and has its own crash semantics (`executeEmailStep`
   (`internal/service/automation_email_step.go`), called from `executeStep`
   (`internal/service/automation_steps.go`)); copy that path for any external side effect. Several replicas may
   run the worker (#237), so every run write after the claim goes through the owner-fenced
   `AdvanceRun`/`ReleaseRun` (the `advance` helper) and an external side effect needs the same
   owner checks as the email step: the locking check of the run row in its first transaction and
   the plain read just before the call.
5. Erasure: if the step writes anything that can name a person, `cancelAutomationForEntity`
   (`internal/repository/erasure.go`) must reach it.
6. UI: `gocrm-ui/src/pages/automation/RuleStepFields.tsx`, `ruleFormSchema.ts`,
   `automationFormat.ts`, and the types in `gocrm-ui/src/types/automation.ts`.
7. Tests: execution and dry run (`automation_step_execution_test.go`,
   `automation_dry_run_test.go`), validation (`automation_validation_test.go`), log privacy
   (`automation_log_privacy_test.go`), and the Vitest files next to the edited components.

**A new event or trigger:**

1. Constant in the event block. `event_type` and `trigger_type` are `varchar(40)`; `entity_type`
   and `origin` are `varchar(20)` (`AutomationRule.EntityType` and `AutomationRule.TriggerType` (`internal/models/automation.go`),
   `AutomationEvent.EventType` and `AutomationEvent.Origin` (`internal/models/automation.go`)).
2. Record the event **inside the write transaction** of the business change, through the
   functional option pattern (`WithLeadAutomationEvents` (`internal/service/lead_service.go`);
   helpers in `internal/service/automation_events.go`). Wire the option in `setupDependencies`
   (`cmd/main.go`). A write that rolls back must record nothing.
3. Writes made by automation itself record origin `automation`, which the dispatcher claims but
   never turns into a run. That is the loop guard; keep it.
4. Allow the trigger for its entity in `automationTriggerTypes`
   (`internal/service/automation_validation.go`). `validateRule` refuses any trigger missing
   from that map before `validateTrigger` runs (`automationService.validateRule` (`internal/service/automation_validation.go`)), so without
   this step a rule on the new event cannot be saved.
5. A new entity type also needs its own entry in that map, `loadRecord` and `lockRecord`
   (`internal/service/automation_steps.go`), the erasure cancel for that entity, and validation of values in
   `validateTrigger`/`validAutomationValue` (`internal/service/automation_validation.go`). Tickets (#242) are the
   third worked entity and show the variants: a run's owner taken from another column
   (`recordFromTicket`, `assigned_to_id`), condition fields and placeholder allowlists of their own
   (`automation_condition.go`; no free text and no customer field), a role rule on
   `assign_owner` (support or admin, checked in `validateRule` and again at run time by
   `resolveAssignTarget`), a `create_task` link to another record (the ticket's customer), a schedule
   filter field of their own (`AutomationScheduleFilter.TicketStatus`, `filters.ticket_status`) and a
   **delete-time** cancellation instead of an erasure one: `ticketRepository.Delete` locks the row,
   then calls `cancelAutomationForEntity` in the delete's transaction, because tickets have no
   erasure of their own. Every writer of the entity locks its row first, as the step's locking read
   does, so writers and steps queue on that row instead of deadlocking (see
   `TestAutomationTicketLockOrderMySQL`). A new closed set that reaches the API (such as the
   `ticket_status` filter) also needs its row in the closed-set table of
   `scripts/ci/check-api-docs.py`; for `filters.ticket_status` that row is an operator follow-up.
6. Frontend: the trigger unions and `AUTOMATION_TRIGGERS` in `gocrm-ui/src/types/automation.ts`,
   `TRIGGER_TYPES` in `gocrm-ui/src/pages/automation/ruleFormSchema.ts`, and the label in
   `TRIGGER_LABELS` (`automationFormat.ts`). A new entity type also needs its condition fields,
   placeholders and task placeholders in `types/automation.ts`, its branch in `entityValues`,
   `entityValueWord` and the schedule filter of `ruleFormSchema.ts` (plus the server path in
   `TOP_LEVEL_PATHS`), its label and path in `automationFormat.ts`, a dry-run picker in
   `DryRunDialog.tsx` and its link in `notificationTarget.ts`, as the ticket entity has (#312).
7. Tests: copy `TestAutomationLeadEvents{SQLite,MySQL}` (`automation_lead_events_test.go`: one event
   per write path, the right origin, ids and values only) and `automation_dispatch_test.go`; add a
   rule-acceptance case to `automation_validation_test.go` (a rule on the new trigger saves, one on
   the wrong entity is refused) and to `ruleFormSchema.test.ts`.

Ticket events (#242) are delivered in the backend and their rule screens (rule form, rule list, run
list, dry run) in the frontend (#312); comment, first-response, SLA and overdue triggers are still
missing (see the roadmap).

## 7. Add a form field type

Constant (`FormFieldText` and its siblings) and `validateFieldType` in `internal/models/form.go`; submitted-value validation
in `formService.validateValues` (`internal/service/form_validation.go`), called from `formService.SubmitPublic` (`internal/service/form_public.go`); the embed renderer `internal/handler/assets/form_embed.js` (embedded with go:embed, no build
step); the hosted page `internal/handler/form_public_html.go`. In the frontend, extend the closed
`FormFieldType` union (`gocrm-ui/src/api/endpoints/forms.ts`) first: the builder imports it, and
`tsc` rejects a new option without it. Then add the option to `fieldTypes` and to the `type`
`z.enum` of the field schema in `gocrm-ui/src/pages/forms/formBuilderSchema.ts`.
Do not reorder the spam layers in `SubmitPublic` and do not change the success-shaped response for
spam: both are deliberate (`REVIEW.md`, "Skip"). Mapping a field to a new lead column is a schema
change (section 3).

## 8. Add an AEO engine

First check whether you need one. The `custom` engine talks to any OpenAI-compatible server (LM
Studio, vLLM, Ollama and others), keyed by `AEO_CUSTOM_BASE_URL`
(`LoadProvidersWithGuard`, `internal/aeo/provider.go`), and reaches nothing until the operator lists
it in `AEO_EGRESS_ALLOWED_ORIGINS` or `AEO_EGRESS_LOCAL_ORIGINS` ([AEO_SECURITY.md](AEO_SECURITY.md)).

If a new named engine is still required:

1. Implement `Provider` (`Name`, `Model`, `Query`; `internal/aeo/provider.go` `Provider`). For an
   OpenAI-compatible API, configure `NewOpenAICompatProvider` instead of writing a client; the
   module uses exactly two provider SDKs (`anthropic-sdk-go` and `openai-go`, `go.mod`), with no
   hand-rolled HTTP.
2. Register it in `LoadProvidersWithGuard` and `ProviderStatusesFor` in the same file.
3. Add an egress key in `internal/config/aeo_egress.go` (its key constants and `aeoEgressKeys`) and
   the engine's official HTTPS origin in `officialOrigins` (`internal/aeo/egress.go`).
4. Environment variables in `AEOConfig` (`internal/config/config.go`, defaults in its `Load` block).
5. Admin settings: the sensitive key in `DefaultConfigurations()` (next to the five existing keys),
   the model/base-URL overrides via `aeoConfigProviders` (`internal/models/aeo_configuration.go`),
   and the overlay in `EffectiveAEOConfig` (`internal/service/aeo_credentials.go`).
6. UI: `gocrm-ui/src/pages/aeo/AEOSettings.tsx` and `AEOProviderConnections.tsx`.
7. Tests: `internal/aeo/provider_test.go`, `egress_test.go`, `aeo_credentials_test.go`, and the
   `aeo-providers.spec.ts` browser spec.

The bounded `aeo` profile excludes schema, shared configuration, frontend and dependency changes
([VALIDATION_RELEASE.md](VALIDATION_RELEASE.md)), so a new engine normally runs `full`.

## 9. Integrate over REST, or add an export

- **Integration.** Create a dedicated user for each integration, issue an API key for it
  (`POST /api/v1/api-keys`) and send it as `Authorization: ApiKey <key>`
  (`Auth` (`internal/middleware/auth.go`)):

  ```bash
  curl -s -H "Authorization: ApiKey $GOPHERCRM_KEY" \
    "https://crm.example.com/api/v1/leads?offset=0&limit=20"
  ```

  A key has no scopes of its own (`internal/models/apikey.go`): it acts with the permissions its
  user holds at each request. Put the integration's user on a custom role that holds only the
  keys it needs, for example `customers.create` and `tickets.create` for an intake that creates
  both; the setup, the keys per case, rotation and revocation are in
  [Least-privilege integration user](AUTHORIZATION.md#least-privilege-integration-user). A key of a
  system-role user is bound by that role: a support identity sees only tickets assigned to it,
  and a sales identity gets 403 on ticket create (`TicketHandler.Create` (`internal/handler/ticket_handler.go`)). An
  integration finds a lead by its external id through `GET /leads?external_id=`
  (`LeadHandler.List` (`internal/handler/lead_handler.go`); admin any lead, sales its own, exact
  match, not unique).
- **Export.** Copy `ListAllForExport`: one ordered statement with a total order, filtered and
  sorted through the allowlist; never `FindInBatches` with a non-id order.

## 10. Add a role or a permission

This recipe has the highest blast radius in this guide: a missed check either leaks data or locks
users out.

Roles are a string type with five values (`UserRole` (`internal/models/user.go`)): the four system
roles and `custom`, the role of an account on a custom role (`role_id`). They are checked in
several layers at once:

- route guards: `handler.Protected` (`internal/handler/protected_routes.go`) builds each protected
  route's `middleware.RequireAnyPermission` / `RequirePermission` guard from its entry in
  `permissions.RouteKeys` (`internal/permissions/routes.go`); a route without an entry there or in
  `permissions.SelfServiceRoutes` panics at registration, and `TestEveryRouteRequiresItsOwnKey`
  holds the table and the router together;
- permission and scope checks inside handlers, all through `internal/handler/permission_context.go`
  (tickets: the `TicketHandler` methods in `internal/handler/ticket_handler.go`); a system role is its matrix in
  `internal/permissions/system_roles.go`;
- service policies (`CanViewLead` and the redaction helpers, ticket and dashboard scoping, staff
  checks for automation assignees in `automation_validation.go`, `staffUserProblem`);
- user management: the `oneof=admin sales support customer custom` binding in
  `CreateUserRequest.Role` and `UpdateUserRequest.Role` (`internal/handler/user_handler.go`); registration always creates `customer`;
- the frontend: the `role` unions of `User` and `RegisterRequest` in `gocrm-ui/src/types/index.ts` and of
  `CreateUserData` and `UpdateUserData` in `src/api/endpoints/users.ts` (the user ones include `custom`, which travels with
  `role_id`), and the system-role fixture the UI tests are fed from
  (`src/permissions/systemRoleFixture.ts`, compared with the golden matrix by
  `systemRoleFixture.test.ts`). Routes, navigation and buttons read permission keys, not role
  names (`src/permissions/noRoleGating.test.ts` fails on a role-name gate).

Measured on a51e336f: 28 non-test Go files reference `models.Role`
(`command grep -rl "models\.Role" internal cmd --include='*.go' | command grep -v _test.go | wc -l`)
and 6 non-test frontend files contain a `'support'` or `"support"` literal
(`command grep -rl "'support'\|\"support\"" gocrm-ui/src --include='*.ts' --include='*.tsx' | command grep -v '\.test\.' | wc -l`).

Recipe:

1. Write the access matrix first: every route and every visible record, for the new role.
2. Write the visibility tests before the change, copying `internal/integration/`
   (`ticket_visibility_integration_test.go`, `lead_visibility_integration_test.go`). They must fail.
3. Add the constant, then walk every hit of both greps above, plus the role's matrix in
   `internal/permissions/system_roles.go` and the helpers of `permission_context.go` (handlers no
   longer read `user_role` themselves), and decide each one explicitly.
4. Extend the handler role tests, add the role to the frontend system-role fixture
   (`src/permissions/systemRoleFixture.ts`) and extend the per-role route and nav tests
   (`src/routes/index.test.tsx`, `src/layouts/MainLayout.test.tsx`).
5. Keep public registration creating `customer` only, and keep elevated roles coming only from
   `POST /users` or `cmd/create-admin`.

**A permission** (one more route for an existing role) is the same recipe in small: change the
guard, add the denied-role and allowed-role tests, and change the matching frontend guard. The
permission matrix is data: the keys and their tree are `internal/permissions/catalogue.go`
`catalogue`, the grants of each system role are `internal/permissions/system_roles.go`
`systemRoles`, and the route-to-key table is `permissions.RouteKeys`. A new key or grant changes
the golden `internal/permissions/testdata/system_roles.golden.json`, which
`TestSystemRoleMatricesEqualGolden` compares with `systemRoles`, and the tables of
[AUTHORIZATION.md](AUTHORIZATION.md), which `TestAuthorizationGuideMatchesCatalogue` compares with
the catalogue; update both, and the frontend fixture, in the same change. A new route goes on
`cmd/system_role_route_matrix_test.go` `routeMatrixAfterFreeze` (§3.7).
A role for an integration needs no code: it is a custom role created through `POST /roles`, see
[Least-privilege integration user](AUTHORIZATION.md#least-privilege-integration-user).

## 11. Removing a module safely

General rules:

- Remove from the top down: navigation and routes, then handlers and wiring, then services and
  repositories, then models.
- Never edit an applied migration. Dropping tables is a new migration pair. SQLite installations
  never run `migrations/`; on SQLite, leave the tables (removing the model from AutoMigrate stops
  managing them but does not drop them) or drop them with a manual SQL step after a backup.
- Keep erasure intact, and run the `full` profile once after a removal.
- Decide the level first: **hide** (remove navigation, keep the code), **retain data** (remove the
  code, keep the tables), or **delete data** (remove the code and drop the tables).

**Worked example: AEO.** No other module imports `internal/aeo` except through the files below, so
it is the cleanest removal.

| Layer | Remove or change |
|---|---|
| Backend | `internal/aeo/`; `internal/service/aeo_*`; `internal/repository/aeo_repository.go`; `internal/handler/aeo_handler.go`, `aeo_routes.go`; `AEORepository` and `AEOService` in both `interfaces.go`; the AEO mocks |
| Wiring | `cmd/main.go` (the `internal/aeo` import; in `setupDependencies` the construction from `aeoRepo` and `aeo.LoadProviders` to `aeoRuntime.runs.Service()`, the handler `handler.NewAEOHandler`, the reconcile `aeoService.ReconcileRunningRuns` and `aeoRuntime.startScheduler`, and the route call `handler.SetupAEORoutes`); `cmd/aeo_runtime.go`. The shutdown drain is shared: `setupDependencies` returns `combineStops(automation.Stop, formRetention.Stop, bulkRecovery.Stop, passwordReset.Stop, webhooks.Stop, aeoRuntime.Stop)` and `drainAEO` (`cmd/aeo_runtime.go`) runs it, so keep a drain for the automation worker, the form retention purge, the stale bulk operation sweep, the password reset queue and the webhook sender. The outbound webhook sender (#345) uses the shared outbound-request guard in `internal/egress` (`(*egress.Guard).PublicHTTPClient`, `egress.CheckPublicURL`), which does not depend on the module. Runs are coordinated across replicas through the ownership columns of `aeo_runs` (`owner_instance`, `heartbeat_at`, `running_lock`, `schedule_slot`; #229): the service claims with `AEORepository.ClaimRun`, renews the heartbeat, and the engine finalizes through the conditional `FinishRun` (there is no full-struct run update) |
| Configuration | `AEOConfig` and its `AEO:` block in `Load` (`internal/config/config.go`), `internal/config/aeo_egress.go`; the five sensitive keys and the generation-engine key (`integration.aeo.generation_engine`) in `DefaultConfigurations` (`internal/models/configuration.go`); `internal/models/aeo_configuration.go`, together with its two callers in `internal/models/configuration.go`: `Configuration.SetValue` (`internal/models/configuration.go`) calls `validateAEOProviderSetting` for every string setting (keep the plain `c.Value = str` assignment), and `DefaultConfigurations` (`internal/models/configuration.go`) appends `defaultAEOProviderConfigurations()` at its end (return `defaults` alone). Without both edits the backend does not compile |
| Models and schema | `internal/models/aeo.go`; five models from `internal/models/database.go` `autoMigrateModels` (`AEOProfile` to `AEOCitation`); a new drop pair for the `aeo_*` tables created in `20261001120000_v1_2_0_schema_parity` and extended by `20261114120000_add_aeo_run_ownership`; their names in `expectedTables` and in the sweep table list |
| Dependencies | `anthropic-sdk-go` and `openai-go` in `go.mod`, then `go mod tidy` |
| Frontend | `src/pages/aeo/`, `src/api/endpoints/aeo.ts` and its export, the routes (the `aeo.view` `ProtectedRoute` group in `routes/index.tsx`), the AEO nav group (`MainLayout.tsx`, the `title: 'AEO'` entry), the AEO case in `ConfigurationSettings.test.tsx` |
| Tests and CI | AEO tests; `aeo-providers.spec.ts`; screenshot spec `10-aeo.spec.ts`; the `aeo` profile, `AEO_REQUIRED_ROOTS` and the `aeo` entry in `validation.py` (imported at the top of `scripts/ci/check-go.py`); the AEO port and variables in `scripts/e2e/run.sh`; `internal/aeo/tls_roots_e2e.go` goes with the package |
| Docs | `docs/AEO_SECURITY.md`, `docs/testing/11-aeo.md`, README and FEATURES sections |

**Other modules:**

| Module | Prefer | Coupling to watch |
|---|---|---|
| Automation | `AUTOMATION_ENABLED=false` (read by `Load` (`internal/config/config.go`)) stops the worker on that instance; events are still recorded in the outbox. The worker may run on several replicas sharing MySQL/MariaDB (run leases, #237) | Event capture runs inside lead, deal, ticket, form and bulk transactions through the `With...AutomationEvents` options (`cmd/main.go`, `setupDependencies`); the ticket delete cancels the ticket's runs (`ticket_repository.go`). Erasure skips the automation tables if they are absent (`purgeUserNotifications` (`internal/repository/erasure.go`), and `probeAutomationTables` (`internal/repository/erasure.go`) for the cancel of a lead erasure). UI: `pages/automation/`, `NotificationBell.tsx`, the runs section in `LeadDetail.tsx` and `DealDetail.tsx` |
| Products and quotes | Removal (quotes reference deals and customers, nothing references quotes) | Wiring in `setupDependencies` (`cmd/main.go`): `productRepo` and `quoteRepo`, the product and quote services through `service.WithQuotePDF`, the three product and quote handlers, `handler.SetupQuotePublicRoutes`, and `handler.SetupProductRoutes` with `handler.SetupQuoteRoutes`; AutoMigrate `internal/models/database.go` `autoMigrateModels` (the product and quote models); a drop pair can reuse the body of `20261026120000_add_products_and_quotes.down.sql`; UI: `pages/products/`, `pages/quotes/`, nav, the quotes list in `DealDetail.tsx`, `Breadcrumbs.tsx` |
| Forms | Removal is possible but touches erasure | `internal/forms/` (challenge, reCAPTCHA), `internal/handler/form_*` and `assets/form_embed.js`, the CORS public prefix (`middleware.CORSWithPublicPrefixes` in `setupRouter` (`cmd/main.go`)), `cfg.Forms`, `WithFormAutomationEvents`, the retention purge (`cmd/form_retention_runtime.go`, `internal/service/form_submission_purge.go`, `form_retention_runtime.go`), `scrubLeadFormSubmissions` in the lead erasure plan (`leadErasurePlan` and `scrubLeadFormSubmissions` (`internal/repository/erasure_cascade.go`)) and `scrubUnlinkedSubmissions`, which every erasure with an email column runs (`eraseRecord` (`internal/repository/erasure.go`)) |

**Never remove:** users and authentication, leads, customers, companies (foreign-key targets of
leads, customers and deals), configuration, and erasure. Every other module depends on them.

## 12. The gates

| Gate | What it checks | Where |
|---|---|---|
| `plan` job | Picks one profile from the diff and records the documentation impact | the `plan` job of `.github/workflows/ci.yml`; `scripts/ci/plan-validation.py` reads `validation.py` from the **base** ref (`main` there: the `base = {...}` lookup, then the `git show` of `validation.py`), so a change cannot weaken its own gate |
| `hygiene` | File-size guard, e2e runner self-test, deploy-script test, Python tests for `scripts/ci` and `scripts/release` | the `hygiene` job of `ci.yml`, `make verify-hygiene` |
| `migrations` | Fresh install, populated upgrade, rollback and reapply on MySQL 8 and MariaDB 10.11, plus the SQLite upgrade fixtures; rejects skips. Both engines run on a `full` run (a release, or a manual dispatch with `full: true`); a pull request that changes migration code (`migrations/`, `cmd/migrate/`, the workflow or its coverage gate) runs the MySQL 8 leg alone, which is not a required check; any other pull request does not run it | `.github/workflows/migrations.yml`, called from the `migrations` job of `ci.yml`; on `full`, `backend` requires its success |
| `backend` | Outside `full`: the gofmt check on every tracked Go file (`scripts/ci/check-go-format.py`), module build and vet, `python3 scripts/ci/check-api-docs.py`, and `go test -count=1` (no `-race`) on the changed packages, their importers and the packages that read the changed files; no Go test for a documentation-only change. On `full`: `make verify-api-docs`, `make verify-backend` (the same gofmt check, build, vet, `go test -race`), `check-go.py transaction` and `check-auth.py --persistence` on MySQL 8 | the `backend` job of `ci.yml`, `scripts/ci/check-changed-go.py` |
| `db-gates` | Outside `full`: the same selected packages against MariaDB 10.11 (`TEST_MYSQL_DSN` set, no `-race`, skips not rejected). On `full`: `check-go.py transaction` on MariaDB | the `db-gates` job of `ci.yml` |
| `frontend` | `npm ci`, build, the end-to-end type-check (`tsc -p tsconfig.e2e.json`), lint with zero warnings, Vitest | the `frontend` job of `ci.yml`, `make verify-frontend` |
| `e2e` | Playwright on MySQL 8, MariaDB 10.11 and SQLite through `scripts/e2e/run.sh`; only when the release workflow calls CI | the `e2e` job of `ci.yml` |
| No-skip SQL gate | Required SQL roots must pass; any skip or fail is an error | `scripts/release/check-sql-tests.py` |
| Release | Full matrix; every `cmd/migrate` and `internal/integration` root against MySQL with no skips | the `Fresh, upgrade, rollback and real SQL integration tests` step of `.github/workflows/release.yml` |

The selector knows 9 profiles: `full`, `mailer`, `aeo`, `migration`, `transaction`,
`auth-persistence`, `auth`, `runner` and `contextual`
(the `profile = (...)` expression of `plan` in
`automation/langflow/gophercrm_runner/validation.py`, measured on a51e336f). Each
bounded profile's scope is in [VALIDATION_RELEASE.md](VALIDATION_RELEASE.md).

Points that catch people out:

- **A new MySQL root runs on a pull request only when its package is selected, and only on
  MariaDB.** The database gates job runs `scripts/ci/check-changed-go.py --tests-only` with
  `TEST_MYSQL_DSN` on MariaDB 10.11 for the packages the change touches, their importers and the
  packages that read the changed files; it does not use `-race` and does not reject a skip. The
  backend job has no database, so MySQL roots skip there. A test that reads files outside its own
  package needs a row in that script's `EXTERNAL_READERS` table. On a `full` run the `make
  verify-backend` step sets no `TEST_MYSQL_DSN` either, so MySQL roots skip there too, and the
  migrations workflow runs a fixed list of roots (`migrations.yml`, the `-run` expression of the
  `Migrations, SQLite upgrades and repository SQL regressions` step). Add a new migration root to that list, to the `--migrations-only` set in `check-sql-tests.py` and to
  `MIGRATION_SOURCES` (and the required set) in `scripts/ci/check-go.py`, or it first runs against
  MySQL at release time. A pull request that changes migration code runs that list on MySQL 8 only. A new auth MySQL root in a file `check-auth.py` already selects runs on
  `full` through `check-auth.py --persistence` (#230); a MySQL root anywhere else needs a checker
  that selects it.
- **Run browsers the way CI does.** `make e2e` builds a disposable backend with the `gophercrm_e2e`
  tag, resets a `*_e2e` database and pins the environment. `make e2e E2E_DB_DRIVER=sqlite` needs no
  database server; `make e2e SPECS="e2e/tests/<spec>.ts"` narrows the run.
- **`make verify`** is what the `full` profile runs for a general application change, locally and
  for a release; pull-request CI runs the targeted Go selection instead
  (`python3 scripts/ci/check-changed-go.py --base origin/main` reproduces it for committed changes). Bounded
  profiles (`auth`, `auth-persistence`, `transaction`, `migration`, `aeo`, `mailer`) replace it with
  their targeted commands (`core_commands` in `automation/langflow/gophercrm_runner/validation.py`;
  [VALIDATION_RELEASE.md](VALIDATION_RELEASE.md#validation-policy), the "Hygiene and core checks" row). Run what the planner selects for your diff, or `make verify` when you want the full set.
- **Documentation impact** is advisory input to review, not approval
  ([DOCUMENTATION_WORKFLOW.md](DOCUMENTATION_WORKFLOW.md)). The reviewer still decides whether
  each listed document is accurate.
- **The pre-commit hook** (`scripts/git-hooks/pre-commit`) runs only after
  `git config core.hooksPath scripts/git-hooks`. It refuses new folders under `docs/` and tracked
  files over the size limit. New files in `docs/` are fine.

On a fork, keep the `gophercrm_runner` Python package even if you never use the remediation runner:
ordinary CI imports it (the `gophercrm_runner` imports at the top of `scripts/ci/check-auth.py`,
`scripts/ci/go_checks.py`, `scripts/ci/plan-documentation.py` and `scripts/ci/check-go.py`, and `main` in
`scripts/ci/plan-validation.py` reads `validation.py`; `scripts/ci/check-changed-go.py` goes through `go_checks.py`). The Langflow graphs, the runner runtime
and `remediation-runner.yml` can go. [FORKING.md](FORKING.md) has the full keep/drop list.

## 13. Working with coding agents

Recipes, reference files and falsifiable gates are what make delegated changes cheap to check. A
brief that works:

```text
Goal: <one sentence of behaviour, with the issue number>.
Recipe: docs/EXTENDING.md section <n>. Reference implementation: <file>.
Rules: docs/EXTENDING.md section 1; long form in docs/DEVELOPMENT.md.
Prove it: write the test that fails on the current code first, then fix.
Gate: run validation.py on the diff and run the commands its profile selects, plus every named
acceptance check. make verify is what the full profile runs; a bounded profile (auth,
auth-persistence, transaction, migration, aeo, mailer) replaces it with targeted commands.
Do not: edit applied migrations, CI workflow or validation policy files, or a test to make it
pass.
Stop and report if: a test needs TEST_MYSQL_DSN and none is available, or the change needs a file
outside the scope above.
```

Review the result as you would any other change: re-run the gate yourself on the final tree, and
check that each new test fails without the change (`REVIEW.md`).

The repository also ships development tooling in `automation/langflow/` (the remediation runner)
that takes one GitHub issue at a time through a worktree, tests, an independent review, a pull
request and CI. Adopting it on a fork is described in
[automation/langflow/README.md](../automation/langflow/README.md).

## 14. Add a webhook event or entity

Webhook events are declared once and derived everywhere else. The reference implementation is the
quote entity (#518). Steps, in order:

1. Add the `Entity` const in `internal/webhook/catalog.go` and append it to
   `defaultCatalogue`, the `internal/webhook/catalog.go` `NewCatalogue` call. The event types
   (`<entity>.created`, `.updated`, `.deleted`), the `IsEventType` check and the refusal messages
   follow from it.
2. Add a `WebhookState()` for the model in `internal/models/webhook_state.go` (the pattern is
   `internal/models/webhook_state.go` `Lead.WebhookState`): the columns an update compares to
   decide whether an `updated` event is due. `TestWebhookStateCoversUpdateColumns`
   (`internal/repository/webhook_state_columns_test.go`) fails when a repository update writes a
   column the state does not compare, or the state compares one no update writes; add the entity
   to it.
3. Add a `With<Entity>Webhooks` service option (the pattern is
   `internal/service/lead_service.go` `WithLeadWebhooks`) and wire it in `cmd/main.go`
   `setupDependencies` with the shared emitter. Every write path then makes one `TxEmitter` call
   (`Created`, `Updated` or `Deleted`) inside the write's own transaction, usually the
   `Webhooks` outlet of the `txDeps.Run` unit, so the outbox row commits or rolls back with the
   write.
4. Add the new event types to the swagger `enums` tags of `WebhookEndpoint.EventTypes` and
   `WebhookDelivery.EventType` (`internal/models/webhook.go`), of the request bodies and the
   `event_type` filter in `internal/handler/webhook_handler.go`, and the counts in their
   descriptions, then run `make swagger`. `internal/webhook/catalog_test.go`
   `TestCatalogueSwaggerEnums` fails while any event type list of `api/swagger.json` differs from
   the catalogue.
5. Check the headroom: an endpoint may list at most
   `internal/webhook/endpoint_service.go` `webhookEventTypesMaxItems` event types (32; 21 exist),
   and the stored subscription must fit the `varchar(512)` column,
   `internal/models/webhook.go` `webhookEventTypesColumnMaxBytes`. Widening the column needs a
   migration pair (§3.3) as well as the AutoMigrate tag.
6. Describe the new events on the user-guide page
   `docs/user-guide/permissions-access/webhooks.md`; `scripts/ci/check-user-guide.py` resolves
   every event type the page names against the catalogue.

Tests: a service test per write path that asserts one outbox row in the same transaction and none
after a rollback (`internal/service/quote_webhook_events_test.go` is the pattern), plus a
handler or API test. Long form: [DEVELOPMENT.md](DEVELOPMENT.md#outbound-webhooks-345).

## 15. Background runtimes and outbound requests

- **A background runtime** (a worker, a sweep, a queue) is built from `internal/lifecycle`:
  `internal/lifecycle/loop.go` `NewLoop` runs a body at start and then on every tick or wake on one
  goroutine until `Stop`, and `internal/lifecycle/owner.go` `NewOwner` admits each unit of owned
  work and lets a shutdown close admission and drain it. Add the runtime's `Stop` to the drain
  handle `setupDependencies` returns (`combineStops` in `cmd/main.go`), so a shutdown stops it
  under the same deadline as the others. `internal/service/bulk_operation_recovery_runtime.go` is
  a small reference.
- **An outbound HTTP request** to a URL that an administrator or a user supplied goes through the
  egress guard, never through `http.DefaultClient`: `internal/egress/public.go`
  `Guard.PublicHTTPClient` returns a client that reaches only that origin through public
  addresses and follows no redirect, and `internal/egress/public.go` `CheckPublicURL` validates
  the URL when it is stored. The webhook sender is the reference (`internal/webhook/runtime.go`,
  `internal/webhook/endpoint_service.go`). AEO provider calls use the engine policy in
  `internal/aeo/egress.go` instead.
- Long form: [DEVELOPMENT.md](DEVELOPMENT.md#background-runtimes-and-outbound-requests).
