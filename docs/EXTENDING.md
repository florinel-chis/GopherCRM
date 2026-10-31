# Extending GopherCRM

A cookbook for application developers and coding agents who add to, or cut from, GopherCRM. Each
recipe names a reference implementation to copy, the files to touch in order, the tests that prove
the change, and the gate that will check it.

This guide is the short entry point. [DEVELOPMENT.md](DEVELOPMENT.md) is the long-form contract
for every pattern named here; when the two disagree, DEVELOPMENT.md and the code win. For running
and deploying a fork see [FORKING.md](FORKING.md); for choosing a starting point see
[USE_CASES.md](USE_CASES.md).

Counts in this guide were measured on `4dfe404`, with the command next to each one.

## 1. Rules that protect you

Most rules here are enforced in code, so a change that copies a neighbouring file inherits them.
Break one and a reviewer will flag it as **Important** under [REVIEW.md](../REVIEW.md).

| Rule | Where it lives | Long form |
|---|---|---|
| Every JSON response uses the `{success, data, error, meta}` envelope, sent only through `utils.Respond*`. The existing non-JSON contracts stay as they are: the public embed script and the hosted and confirmation pages (`c.Data`, `internal/handler/form_public_handler.go:85,166-222`), the customer CSV export (`customer_handler.go:484,537`), and deletes that answer an empty 204 (`customer_handler.go:465`) | `internal/utils/response.go` | DEVELOPMENT.md, "Unified API response" (line 87) |
| Errors are sentinels classified with `errors.Is`; misses are wrapped as `fmt.Errorf("<entity> %d not found: %w", id, ...)`; `apperrors.IsNotFound` covers raw `gorm.ErrRecordNotFound` too | `internal/errors/errors.go` | "Error types" (line 90) |
| Sort columns come only from a per-entity allowlist | `utils.ValidateSort`, `utils.AllowedSortColumns` in `internal/utils/sort.go` | "Sort validation" (line 374) |
| Lists parse paging through `utils.ParseOffsetLimit` and walk a total order (sort column, then `id`) | `internal/utils/response.go:146`, `internal/repository/page_order.go` | "Pagination" (line 534), "Paginated reads walk a total order" (line 498) |
| Unpaginated reads are one ordered statement, never `FindInBatches` with a non-id order | `customerRepository.ListAllForExport` (`internal/repository/customer_repository.go:212`) | line 482 |
| Multi-step writes run in `TransactionManager.WithTransaction`; nested calls reuse the transaction under a savepoint | `internal/utils/transaction.go:149-176` | "Transaction management" (line 94) |
| A single-use token is spent by a conditional `UPDATE` inside the transaction that does the dependent writes; mail goes out after the commit | password reset, refresh rotation, form confirmation | line 224 onwards |
| Lead visibility has one policy: admin sees all leads, sales its own, every other role none | `service.CanViewLead`, `RedactTaskLead(s)`, `RedactDealLead(s)` in `internal/service/lead_visibility.go` | code comments in that file |
| Service logs carry ids, statuses and counts, never emails, names, titles, search terms or raw sort values; access logs omit query strings | `internal/service/logging_privacy_test.go`, `internal/handler/logging_privacy_test.go` | end of "Deleting personal data" |
| Deleting a person overwrites every personal field, then soft-deletes, in one transaction | `internal/repository/erasure.go`, `erasure_cascade.go` | [Deleting personal data](DEVELOPMENT.md#deleting-personal-data) |
| A schema change ships both halves: GORM AutoMigrate and a `migrations/` up/down pair | `internal/models/database.go:111`, `migrations/` | [Database](DEVELOPMENT.md#database) |
| Duplicate keys become a sentinel, not a driver string | `internal/repository/duplicate_key.go` | "Duplicate email" (line 540) |

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

   Any path under `SHARED_PREFIXES` or in `SHARED_FILES` (`validation.py:39-51`: `migrations/`,
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

Add the model to `autoMigrateModels` in `internal/models/database.go:111`, after the models it
references (products and quotes sit at lines 120-123). On SQLite this is the only schema path;
`cmd/migrate` refuses database commands there (`ErrSQLiteUnsupported`, `cmd/migrate/main.go:26`).

### 3.3 SQL migration pair (the MySQL/MariaDB half)

- Create the pair with `go run ./cmd/migrate -command create -name add_<entity>`
  (`createNewMigration`, `cmd/migrate/main.go:205`). It writes
  `migrations/<timestamp>_add_<entity>.{up,down}.sql`. The repository holds 12 pairs (`ls
  migrations/*.up.sql | wc -l`, measured on 4dfe404).
- New tables use `CREATE TABLE IF NOT EXISTS`, with column, index and constraint names identical to
  what AutoMigrate produces. The new binary may already have created the table before the operator
  runs the migration. Copy the header and body of
  `migrations/20261026120000_add_products_and_quotes.up.sql`.
- Do not alter existing tables in the same migration as new ones. Adding a foreign key to an
  existing table makes the SQLite side rebuild that table; that needs its own upgrade fixture test.
- Never edit a migration that has shipped. Add a new pair instead.
- Add the table to `expectedTables` in `TestMigrationFiles_PairsAndCompleteness`
  (`cmd/migrate/main_test.go:77`). Add a migration test modelled on `TestProductsAndQuotesMigration`
  (`cmd/migrate/products_quotes_migration_test.go:165`): fresh install, AutoMigrate first, schema
  parity, rollback.

**On a fork:** migration versions are numeric, and golang-migrate (`cmd/migrate/main.go:101-110`)
does not apply a file numbered below the newest version a database has already applied. Merge
upstream first, give fork migrations timestamps later than upstream's latest, re-stamp unreleased
fork migrations after every upstream sync, and rehearse a populated-database upgrade before
release. [FORKING.md](FORKING.md) covers upstream sync in full.

### 3.4 Repository

- Declare the interface in `internal/repository/interfaces.go`; implement it in
  `internal/repository/<entity>_repository.go`. `productRepository` is the template.
- Provide `WithTx(tx *gorm.DB) <Entity>Repository` so the service can bind the repository to a
  transaction (`product_repository.go`, last function).
- List: validate the sort with `utils.ValidateSort("<table>", sortBy, sortOrder)` after adding the
  table to `AllowedSortColumns` (`internal/utils/sort.go:9`), then order by the column and by `id`
  in the same direction. Use `orderPage` / `orderByColumnThenID` from `page_order.go` when the
  endpoint has a "no sort" default. Search filters are bound parameters, never interpolated
  (`productSearchClause`).
- A delete that matched no row returns `gorm.ErrRecordNotFound` (`productRepository.Delete`).
- A unique column wraps the driver error into a sentinel through `duplicate_key.go`. GORM's
  `ErrDuplicatedKey` never fires here (DEVELOPMENT.md line 727).
- Tests: `internal/repository/<entity>_repository_test.go` on SQLite, covering filters, sort,
  rejection of an unknown sort column and soft-delete hiding (copy
  `TestProductRepository_ListRejectsUnknownSortColumns`). If the entity has a paginated list, add
  it to the fixtures of `TestPaginationDeterminism{SQLite,MySQL}`
  (`internal/repository/pagination_determinism_test.go`).

### 3.5 Service

- Interface in `internal/service/interfaces.go`, implementation in
  `internal/service/<entity>_service.go`.
- Validate input and wrap with sentinels: `fmt.Errorf("... : %w", apperrors.ErrValidation)` and
  `fmt.Errorf("product %d not found: %w", id, apperrors.ErrProductNotFound)`
  (`internal/service/product_service.go:51,120`). Declare new sentinels in
  `internal/errors/errors.go` next to the existing ones.
- Multi-step writes take `*utils.TransactionManager` and run inside `WithTransaction`, using the
  `WithTx` repositories. `QuoteService` is the reference for conditional claims in one transaction
  (DEVELOPMENT.md line 377).
- Record-level visibility (who sees which row) belongs here or in the repository query, applied
  **before** pagination, so counts and pages agree. If the entity can link a lead, apply
  `CanViewLead` on write and redact on every read; never write a separate role check for a lead
  link.
- Log operation names, ids and counts only. `internal/service/logging_privacy_test.go` is the
  pattern for asserting that a log line carries no personal data.

### 3.6 Handler and Swagger

- `internal/handler/<entity>_handler.go`: parse with `ShouldBindJSON`, page with
  `utils.ParseOffsetLimit`, answer through `utils.Respond*`, and map errors with `errors.Is` /
  `apperrors.IsNotFound` (the error switch at `internal/handler/product_handler.go:317-320`).
  Handlers never import gorm.
- Annotate each handler (`@Summary`, `@Security BearerAuth`, `@Security ApiKeyAuth`, `@Router`;
  see `product_handler.go:66-80`). Annotations describe what the code does, including its warts.
- Regenerate the spec with `make swagger` (writes `api/swagger.json` and `api/swagger.yaml`).

### 3.7 Routes and wiring

- Add `Setup<Entity>Routes` to `internal/handler/routes.go`. Put the role guard on the group with
  `middleware.RequireRole(...)` and narrow individual routes where needed
  (`SetupProductRoutes`, `routes.go:97-105`: group admin+sales, writes admin).
- In `cmd/main.go`, `setupDependencies` (line 182): construct the repository with `models.DB`
  (line 195), the service (line 238), the handler (line 298), and register the routes in the
  `protected` group (line 404). There is no container; the wiring is plain code.
- If a static path sits next to a `/:id` route, extend `TestAllRouteSetupsCoexist`
  (`internal/handler/routes_test.go:16`).

### 3.8 Mocks

Service tests use testify mocks in `internal/mocks/` (and four repository mocks in
`internal/repository/mocks/`). Several carry a "generated" header but were written by hand
(`internal/mocks/ProductRepository.go:2`). Add the mock for your new interface and review it: a
wrong mock lets a test pass for the wrong reason (`REVIEW.md`, last paragraph).

### 3.9 Frontend

- Types in `gocrm-ui/src/types/index.ts` (the `Product` interface is at line 193).
- Endpoint module `gocrm-ui/src/api/endpoints/<entity>.ts`, exported from `endpoints/index.ts`.
  The axios client (`src/api/client.ts:216`) unwraps the success envelope, so endpoint functions
  receive `data` directly. Error envelopes are unwrapped flat (`client.ts:237`); read them with
  `getAPIError` (`src/utils/apiError.ts`), which returns `{code, message, details}`.
- Pages under `gocrm-ui/src/pages/<entity>/` (TanStack Query, react-hook-form with zod), lazily
  imported in `gocrm-ui/src/routes/index.tsx` behind `<ProtectedRoute requiredRole={[...]}>` with
  the same roles as the API group (products: lines 178-211).
- Navigation entry in `gocrm-ui/src/layouts/MainLayout.tsx` with matching `roles` (products: line
  89). The UI role check only hides things; the server guard is the authorization.
- Render dates through `src/utils/date.ts`, never a raw `format(new Date(x))`.
- Vitest next to each page and endpoint module (`ProductList.test.tsx`, `products.test.ts`).

### 3.10 Personal data and erasure

Decide first whether the new table stores personal data or only refers to people by id.

- **Ids only**: erasure leaves the rows in place. Quotes are the nearest example: they copy no
  field from the customer and read the recipient at send time (`internal/models/quote.go:71-74`).
  They are not free of personal data, though. The quote title and notes and each line description
  are text typed by staff (`quote.go:77,92,202`; `QuoteRequest`, `internal/handler/quote_handler.go:36-44`),
  can name the client, and survive customer erasure, because `customerErasurePlan`
  (`internal/repository/customer_repository.go:120`) does not reach quote tables. That is a known
  gap: #245. A new table with free-text columns belongs in the next bullet.
  For a table that really holds ids only, still prove it. Add the table to the list in
  `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates`
  (`test/integration/erasure_pii_sweep_test.go:247`), and add a canary test like
  `TestThePersonalDataSweepCoversQuoteTables` (line 269) that writes the person into the table and
  asserts the sweep sees it. A sweep that cannot see a table proves nothing; the quote canary
  proves the sweep can see quote rows, not that erasure clears them.
- **Personal fields** (names, email, phone, address, free text typed by or about a person): the
  erasure must reach them in the same transaction. Staff-typed text about a person counts. Either extend a plan in
  `internal/repository/erasure_cascade.go` (`leadErasurePlan`; `customerErasurePlan` and
  `userErasurePlan` live in their repositories), using `Scrub` for columns and `AfterScrub` /
  `BeforeScrub` for rows outside the entity's own table (as `scrubLeadFormSubmissions` does), or add
  a step to the cascade. A unique column gets a placeholder, never a blank. Rows keyed by an
  external identifier under a unique key must be **deleted**, not blanked.
- Extend `TestIdentityErasure{SQLite,MySQL}` (`internal/repository/identity_erasure_test.go`) and
  the sweep tests so the person is seeded into the new table before the erasure.
- Update the matrix in [DEVELOPMENT.md](DEVELOPMENT.md#deleting-personal-data). Known gaps you
  inherit: unlinked form submissions and ticket free text (#214), quote free text
  (#245), and the missing lock protocol between erasure and conversion or
  authentication (#183).

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
  Bulk changes follow `SetupBulkStatusRoutes` (`routes.go:273`): at most 100 ids, all or nothing,
  per-item authorization (DEVELOPMENT.md line 547).
- **Screen.** Endpoint function, page, lazy route, nav entry, Vitest, and an e2e spec for a
  user-visible flow (`REVIEW.md`). Frontend endpoint functions that call routes the backend lacks
  are a to-do list, not dead code: implement the route instead of deleting the function. One is
  open now: `POST /leads/:id/assign` (#185).

## 5. Add a setting

- Seed it in `DefaultConfigurations()` (`internal/models/configuration.go:264`) with key, type,
  category, default and description. `configRepo.InitializeDefaults()` runs at every start
  (`cmd/main.go:73`) on both drivers; re-seeding refreshes metadata but never overwrites a stored
  value (`BulkUpsert`, `internal/repository/configuration_repository.go:62`). Changing a default
  therefore does not touch existing installations.
- A secret gets `IsSensitive: true` (the AEO keys, `configuration.go:367-404`): encrypted at rest by
  `internal/utils/secretbox.go`, blanked in responses, read only through `GetSecret`. The key derives
  from `API_KEY_SECRET` (falling back to `JWT_SECRET`); rotating it orphans stored secrets.
- Everything in category `ui` reaches every authenticated user through `GET /configurations/ui`
  (`GetUIConfigurations`, `internal/handler/configuration_handler.go:403`). Never put a sensitive
  key there.
- Ship the consumer and its test in the same change. A seeded setting nobody reads is a defect:
  `tickets.auto_assign_support` and `security.session_timeout_hours` are seeded and not read
  (#224).
- Security toggles fail closed on a read error (`middleware.RequireRegistrationEnabled`).

## 6. Add an automation event or step

Read [Automation runtime](DEVELOPMENT.md#automation-runtime) first. The vocabulary lives in
`internal/models/automation.go`: six event types plus the `schedule` trigger (lines 30-38), five
origins (lines 44-50) and six step types (lines 53-60).

**A new step type:**

1. Constant in `internal/models/automation.go`. `step_type` is `varchar(20)` (line 226), so keep the
   name short.
2. Validation in `validateStep` (`internal/service/automation_validation.go:277`).
3. Dry run in `AutomationEngine.DryRun` (`internal/service/automation_dry_run.go:17`).
4. Execution in `planStep` (`internal/service/automation_engine.go:690`): build a `stepPlan` whose
   `effect` writes through the transaction-bound repositories, so it commits or rolls back with the
   run's progress. Mail is the exception and has its own crash semantics (`executeEmailStep`,
   called from `executeStep` at line 860); copy that path for any external side effect.
5. Erasure: if the step writes anything that can name a person, `cancelAutomationForEntity`
   (`internal/repository/erasure.go:350`) must reach it.
6. UI: `gocrm-ui/src/pages/automation/RuleStepFields.tsx`, `ruleFormSchema.ts`,
   `automationFormat.ts`, and the types in `gocrm-ui/src/types/automation.ts`.
7. Tests: execution and dry run (`automation_step_execution_test.go`,
   `automation_dry_run_test.go`), validation (`automation_validation_test.go`), log privacy
   (`automation_log_privacy_test.go`), and the Vitest files next to the edited components.

**A new event or trigger:**

1. Constant in the event block. `event_type` and `trigger_type` are `varchar(40)`; `entity_type`
   and `origin` are `varchar(20)` (lines 181-182, 258-263).
2. Record the event **inside the write transaction** of the business change, through the
   functional option pattern (`WithLeadAutomationEvents`, `internal/service/lead_service.go:47`;
   helpers in `internal/service/automation_events.go`). Wire the option in `cmd/main.go`
   (`setupDependencies`, lines 226-273). A write that rolls back must record nothing.
3. Writes made by automation itself record origin `automation`, which the dispatcher claims but
   never turns into a run. That is the loop guard; keep it.
4. Allow the trigger for its entity in `automationTriggerTypes`
   (`internal/service/automation_validation.go:102`). `validateRule` refuses any trigger missing
   from that map before `validateTrigger` runs (`automation_validation.go:141-145`), so without
   this step a rule on the new event cannot be saved.
5. A new entity type also needs its own entry in that map, `loadRecord` and `lockRecord`
   (`automation_engine.go:598,623`), the erasure cancel for that entity, and validation of values in
   `validateTrigger`/`validAutomationValue` (`automation_validation.go:219,267`).
6. Frontend: the trigger unions and `AUTOMATION_TRIGGERS` in `gocrm-ui/src/types/automation.ts:8-26`,
   `TRIGGER_TYPES` in `gocrm-ui/src/pages/automation/ruleFormSchema.ts:84`, and the label in
   `TRIGGER_LABELS` (`automationFormat.ts:28`).
7. Tests: copy `TestAutomationLeadEvents{SQLite,MySQL}` (`automation_lead_events_test.go`: one event
   per write path, the right origin, ids and values only) and `automation_dispatch_test.go`; add a
   rule-acceptance case to `automation_validation_test.go` (a rule on the new trigger saves, one on
   the wrong entity is refused) and to `ruleFormSchema.test.ts`.

Wanted but missing: ticket events (#242), and a form-finalised event with
attribution fields (#220).

## 7. Add a form field type

Constant and `validateFieldType` in `internal/models/form.go:52-60,282`; submitted-value validation
in `validateValues` (`internal/service/form_service.go:711`, called from `SubmitPublic` at line
657); the embed renderer `internal/handler/assets/form_embed.js` (embedded with go:embed, no build
step); the hosted page `internal/handler/form_public_html.go`. In the frontend, extend the closed
`FormFieldType` union (`gocrm-ui/src/api/endpoints/forms.ts:9`) first: the builder imports it, and
`tsc` rejects a new option without it. Then add the option to `fieldTypes` and to the `type`
`z.enum` in `gocrm-ui/src/pages/forms/FormBuilder.tsx:48,85`.
Do not reorder the spam layers in `SubmitPublic` and do not change the success-shaped response for
spam: both are deliberate (`REVIEW.md`, "Skip"). Mapping a field to a new lead column is a schema
change (section 3).

## 8. Add an AEO engine

First check whether you need one. The `custom` engine talks to any OpenAI-compatible server (LM
Studio, vLLM, Ollama and others), keyed by `AEO_CUSTOM_BASE_URL`
(`LoadProvidersWithGuard`, `internal/aeo/provider.go`), and reaches nothing until the operator lists
it in `AEO_EGRESS_ALLOWED_ORIGINS` or `AEO_EGRESS_LOCAL_ORIGINS` ([AEO_SECURITY.md](AEO_SECURITY.md)).

If a new named engine is still required:

1. Implement `Provider` (`Name`, `Model`, `Query`; `internal/aeo/provider.go:81`). For an
   OpenAI-compatible API, configure `NewOpenAICompatProvider` instead of writing a client; the
   module uses exactly two provider SDKs (`anthropic-sdk-go` and `openai-go`, `go.mod`), with no
   hand-rolled HTTP.
2. Register it in `LoadProvidersWithGuard` and `ProviderStatusesFor` in the same file.
3. Add an egress key in `internal/config/aeo_egress.go` (constants and `aeoEgressKeys`, lines
   17-28) and the engine's official HTTPS origin in `officialOrigins` (`internal/aeo/egress.go:50`).
4. Environment variables in `AEOConfig` (`internal/config/config.go:144`, defaults at line 290).
5. Admin settings: the sensitive key in `DefaultConfigurations()` (next to the five existing keys),
   the model/base-URL overrides via `aeoConfigProviders` (`internal/models/aeo_configuration.go:11`),
   and the overlay in `EffectiveAEOConfig` (`internal/service/aeo_credentials.go:41`).
6. UI: `gocrm-ui/src/pages/aeo/AEOSettings.tsx` and `AEOProviderConnections.tsx`.
7. Tests: `internal/aeo/provider_test.go`, `egress_test.go`, `aeo_credentials_test.go`, and the
   `aeo-providers.spec.ts` browser spec.

The bounded `aeo` profile excludes schema, shared configuration, frontend and dependency changes
([VALIDATION_RELEASE.md](VALIDATION_RELEASE.md)), so a new engine normally runs `full`.

## 9. Integrate over REST, or add an export

- **Integration.** Create a dedicated user for each integration, issue an API key for it
  (`POST /api/v1/api-keys`) and send it as `Authorization: ApiKey <key>`
  (`internal/middleware/auth.go:24-27`):

  ```bash
  curl -s -H "Authorization: ApiKey $GOPHERCRM_KEY" \
    "https://crm.example.com/api/v1/leads?offset=0&limit=20"
  ```

  A key acts with its user's full role; keys have no scopes (`internal/models/apikey.go`, #211).
  A support identity sees only tickets assigned to it, and a sales identity gets 403 on ticket
  create (`internal/handler/ticket_handler.go:87,195`), so one identity that does both must be an
  admin. External ids for customers are not modelled yet (#212); the lead external-id lookup is
  not routed (#215).
- **Export.** Copy `ListAllForExport`: one ordered statement with a total order, filtered and
  sorted through the allowlist; never `FindInBatches` with a non-id order.

## 10. Add a role or a permission

This recipe has the highest blast radius in this guide: a missed check either leaks data or locks
users out.

Roles are a string type with four values (`internal/models/user.go:9-16`), and they are checked in
several layers at once:

- group and route guards: `middleware.RequireRole` in `internal/handler/routes.go`,
  `aeo_routes.go`, `form_routes.go`;
- checks inside handlers that compare the role string (tickets: `ticket_handler.go:87-416`);
- service policies (`CanViewLead` and the redaction helpers, ticket and dashboard scoping, staff
  checks for automation assignees in `automation_validation.go`, `staffUserProblem`);
- user management: the `oneof=admin sales support customer` binding in
  `internal/handler/user_handler.go:28,35`; registration always creates `customer`;
- the frontend: the `role` unions in `gocrm-ui/src/types/index.ts:6,499` and
  `src/api/endpoints/users.ts:18,25`, `ProtectedRoute` guards in `src/routes/index.tsx`, and `roles`
  on nav entries in `MainLayout.tsx`.

Measured on 4dfe404: 28 non-test Go files reference `models.Role`
(`command grep -rl "models\.Role" internal cmd --include='*.go' | command grep -v _test.go | wc -l`)
and 14 non-test frontend files contain a `'support'` or `"support"` literal
(`command grep -rl "'support'\|\"support\"" gocrm-ui/src --include='*.ts' --include='*.tsx' | command grep -v '\.test\.' | wc -l`).

Recipe:

1. Write the access matrix first: every route and every visible record, for the new role.
2. Write the visibility tests before the change, copying `internal/integration/`
   (`ticket_visibility_integration_test.go`, `lead_visibility_integration_test.go`). They must fail.
3. Add the constant, then walk every hit of both greps above, plus raw `c.GetString("user_role")`
   comparisons, and decide each one explicitly.
4. Extend the handler role tests and the frontend `ProtectedRoute` and nav tests
   (`src/routes/index.test.tsx`).
5. Keep public registration creating `customer` only, and keep elevated roles coming only from
   `POST /users` or `cmd/create-admin`.

**A permission** (one more route for an existing role) is the same recipe in small: change the
guard, add the denied-role and allowed-role tests, and change the matching frontend guard. There is
no permission matrix as data; a fork that needs several new roles should consider building one.
A least-privilege integration role is tracked in #211.

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
| Wiring | `cmd/main.go` (import line 12; construction lines 245-270; handler line 304; reconcile and scheduler lines 338-357; route line 413); `cmd/aeo_runtime.go`. The shutdown drain is shared: `setupDependencies` returns `combineStops(automation.Stop, aeoRuntime.Stop)` (line 427) and `drainAEO` (`cmd/aeo_runtime.go:94`) runs it, so keep a drain for the automation worker |
| Configuration | `AEOConfig` and its block (`internal/config/config.go:144,290`), `internal/config/aeo_egress.go`; the five sensitive keys and the generation-engine key in `configuration.go:367-410`; `internal/models/aeo_configuration.go`, together with its two callers in `internal/models/configuration.go`: `Configuration.SetValue` calls `validateAEOProviderSetting` for every string setting (line 102; keep the plain `c.Value = str` assignment), and `DefaultConfigurations` appends `defaultAEOProviderConfigurations()` (line 417; return `defaults` alone). Without both edits the backend does not compile |
| Models and schema | `internal/models/aeo.go`; five models from `autoMigrateModels` (`database.go:134-138`); a new drop pair for the `aeo_*` tables created in `20261001120000_v1_2_0_schema_parity`; their names in `expectedTables` and in the sweep table list |
| Dependencies | `anthropic-sdk-go` and `openai-go` in `go.mod`, then `go mod tidy` |
| Frontend | `src/pages/aeo/`, `src/api/endpoints/aeo.ts` and its export, the routes (`routes/index.tsx:319-346`), the AEO nav group (`MainLayout.tsx`, from line 161), the AEO case in `ConfigurationSettings.test.tsx` |
| Tests and CI | AEO tests; `aeo-providers.spec.ts`; screenshot spec `10-aeo.spec.ts`; the `aeo` profile, `AEO_REQUIRED_ROOTS` and the `aeo` entry in `validation.py` (imported by `scripts/ci/check-go.py:7`); the AEO port and variables in `scripts/e2e/run.sh`; `internal/aeo/tls_roots_e2e.go` goes with the package |
| Docs | `docs/AEO_SECURITY.md`, `docs/testing/11-aeo.md`, README and FEATURES sections |

**Other modules:**

| Module | Prefer | Coupling to watch |
|---|---|---|
| Automation | `AUTOMATION_ENABLED=false` (`internal/config/config.go:322`) stops the worker; events are still recorded in the outbox | Event capture runs inside lead, deal, form and bulk transactions through the `With...AutomationEvents` options (`cmd/main.go:226-273`). Erasure skips the automation tables if they are absent (`erasure.go:329-365`). UI: `pages/automation/`, `NotificationBell.tsx`, the runs section in `LeadDetail.tsx` and `DealDetail.tsx` |
| Products and quotes | Removal (quotes reference deals and customers, nothing references quotes) | Wiring `cmd/main.go:195-196,238-239,298-299,404-405`; AutoMigrate `database.go:120-123`; a drop pair can reuse the body of `20261026120000_add_products_and_quotes.down.sql`; UI: `pages/products/`, `pages/quotes/`, nav, the quotes list in `DealDetail.tsx`, `Breadcrumbs.tsx` |
| Forms | Removal is possible but touches erasure | `internal/forms/` (challenge, reCAPTCHA), `internal/handler/form_*` and `assets/form_embed.js`, the CORS public prefix (`cmd/main.go:161-162`), `cfg.Forms`, `WithFormAutomationEvents`, and `scrubLeadFormSubmissions` in the lead erasure plan (`erasure_cascade.go:73,103`) |

**Never remove:** users and authentication, leads, customers, companies (foreign-key targets of
leads, customers and deals), configuration, and erasure. Every other module depends on them.

## 12. The gates

| Gate | What it checks | Where |
|---|---|---|
| `plan` job | Picks one profile from the diff and records the documentation impact | `.github/workflows/ci.yml:37`; `scripts/ci/plan-validation.py` reads `validation.py` from the **base** ref (line 49), so a change cannot weaken its own gate |
| `hygiene` | File-size guard, e2e runner self-test, deploy-script test, Python tests for `scripts/ci` and `scripts/release` | `ci.yml:69`, `make verify-hygiene` |
| `migrations` | Fresh install, populated upgrade, rollback and reapply on MySQL 8 and MariaDB 10.11, plus the SQLite upgrade fixtures; rejects skips | `.github/workflows/migrations.yml`, called from `ci.yml:86`; `backend` depends on it |
| `backend` | `make verify-backend` (build, vet, `go test -race`) on `full`; the profile checkers on bounded profiles | `ci.yml:92` |
| `frontend` | `npm ci`, build, lint with zero warnings, Vitest | `ci.yml:173`, `make verify-frontend` |
| `e2e` | Playwright on MySQL 8, MariaDB 10.11 and SQLite through `scripts/e2e/run.sh` | `ci.yml:201` |
| No-skip SQL gate | Required SQL roots must pass; any skip or fail is an error | `scripts/release/check-sql-tests.py` |
| Release | Full matrix; every `cmd/migrate` and `internal/integration` root against MySQL with no skips | `.github/workflows/release.yml:94-98` |

The selector knows 9 profiles: `full`, `mailer`, `aeo`, `migration`, `transaction`,
`auth-persistence`, `auth`, `runner` and `contextual`
(`sed -n 584,586p automation/langflow/gophercrm_runner/validation.py`, measured on 4dfe404). Each
bounded profile's scope is in [VALIDATION_RELEASE.md](VALIDATION_RELEASE.md).

Points that catch people out:

- **A new MySQL root does not run on every PR by itself.** The `full` profile's `make
  verify-backend` step sets no `TEST_MYSQL_DSN`, so MySQL roots skip there. The migrations workflow
  runs a fixed list of roots (`migrations.yml`, the `-run` expression at lines 64-65). Add a new
  migration root to that list, to the `--migrations-only` set in `check-sql-tests.py` and to
  `MIGRATION_SOURCES` in `scripts/ci/check-go.py:53`, or it first runs against MySQL at release
  time. On 4dfe404, `TestProductsAndQuotesMigration` and `TestMigrationAutomationSchema` are in none
  of the three (`command grep -rn "ProductsAndQuotes\|MigrationAutomation" .github scripts` prints
  nothing); tracked in #228.
- **Run browsers the way CI does.** `make e2e` builds a disposable backend with the `gophercrm_e2e`
  tag, resets a `*_e2e` database and pins the environment. `make e2e E2E_DB_DRIVER=sqlite` needs no
  database server; `make e2e SPECS="e2e/tests/<spec>.ts"` narrows the run.
- **`make verify`** is what the `full` profile runs for a general application change. Bounded
  profiles (`auth`, `auth-persistence`, `transaction`, `migration`, `aeo`, `mailer`) replace it with
  their targeted commands (`core_commands`, `validation.py:605-617`; [VALIDATION_RELEASE.md](VALIDATION_RELEASE.md),
  line 21). Run what the planner selects for your diff, or `make verify` when you want the full set.
- **Documentation impact** is advisory input to review, not approval
  ([DOCUMENTATION_WORKFLOW.md](DOCUMENTATION_WORKFLOW.md)). The reviewer still decides whether
  each listed document is accurate.
- **The pre-commit hook** (`scripts/git-hooks/pre-commit`) runs only after
  `git config core.hooksPath scripts/git-hooks`. It refuses new folders under `docs/` and tracked
  files over the size limit. New files in `docs/` are fine.

On a fork, keep the `gophercrm_runner` Python package even if you never use the remediation runner:
ordinary CI imports it (`scripts/ci/check-auth.py:16-19`, `scripts/ci/go_checks.py:10-13`,
`scripts/ci/plan-documentation.py:10-12`, `scripts/ci/check-go.py:7`, and
`scripts/ci/plan-validation.py:49` reads `validation.py`). The Langflow graphs, the runner runtime
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
