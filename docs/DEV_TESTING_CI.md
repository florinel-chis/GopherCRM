# Testing and CI

> **For:** Developers changing tests, CI jobs or their gates. Part of the [documentation index](README.md).

Where the tests live and how they run, the CI jobs and their gates, the documentation checks,
the system-role route matrix, the sign-in and SCIM characterisation gate, the Vitest timeouts
and the API docs gate. The validation profiles are in
[VALIDATION_RELEASE.md](VALIDATION_RELEASE.md). Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

## Testing

| Layer | Location | Runner |
|-------|----------|--------|
| Backend unit | colocated `*_test.go` | `go test ./...` — testify suites, mocks from `internal/mocks/` |
| Backend integration | `test/integration/`, `tests/`, `internal/integration/` | `go test ./...` — in-memory SQLite |
| Frontend unit | `gocrm-ui/src/**/*.test.tsx` | `npm test -- --run --maxWorkers=3` — Vitest + React Testing Library, one run on three workers |
| E2E | `gocrm-ui/e2e/tests/` | `npm run test:e2e` — Playwright, Chromium |

```bash
go test ./...                       # everything
go test -race ./...                 # race detector — must stay clean
go test -cover ./...                # per-package statement coverage
go test -run TestFunctionName ./internal/service/
```

Use current command output for test counts and coverage; historical counts are not release gates.
`make verify` runs hygiene checks, then frontend build, the end-to-end type-check (`tsc -p tsconfig.e2e.json`),
lint and unit tests (Vitest on three workers), then the Go
formatting check and Go build/vet/race tests, then the API reference check. The frontend gate runs before the
race-enabled Go tests so the two do not compete for the processor.
The runner's local validation picks a profile from the changed paths; its bounded profiles
(`auth`, `auth-persistence`, `transaction`, `migration`, `aeo`, `mailer`) replace `make verify` with
their `scripts/ci/check-auth.py` / `scripts/ci/check-go.py` race roots on SQLite and MySQL, named
acceptance checks and health smoke; see [validation and releases](VALIDATION_RELEASE.md) for each scope.

**Opening a test database (#613).** New Go tests open their database through the test-only
package `internal/testsupport` instead of a setup helper of their own; only `_test.go` files
import it, so it is never linked into the server.

- `internal/testsupport/sqlite.go` `OpenSQLite` opens a SQLite database private to the call: by
  default in memory under a unique name, with `foreign_keys(1)`, one connection and a discarding
  logger, migrates the `Models` option and closes the pool when the test ends. Options select a
  file under `t.TempDir()` (`File`, with the `BusyTimeout` and `WAL` pragmas), foreign keys off,
  the open and idle pool sizes separately (or the driver default) and the `*gorm.Config`. A test
  that cancels a transaction uses `File`, not the in-memory default (see the `:memory:` gotcha
  under [Gotchas](DEVELOPMENT.md#gotchas)).
- `internal/testsupport/mysql.go` `MySQLFromEnv` returns the `TEST_MYSQL_DSN` server
  configuration and skips the test without the variable; `OpenMySQL` creates a uniquely named
  schema on it, fails (it does not skip) when the server is unreachable and drops the schema when
  the test ends; `OpenMySQLSchema` opens another handle on a schema the test already has and drops
  nothing.
- `internal/testsupport/dialect.go` `Open` opens a `Dialect` (`SQLite` or `MySQL`), and
  `ForEachDialect` runs one test body in the subtests `sqlite` and `mysql`, the second skipping
  without `TEST_MYSQL_DSN`.

The `internal/repository`, `internal/service` and `internal/webhook` tests use it; the other
packages still have their own helpers.

**CI outside a release runs targeted Go tests (#365).** For a pull request, a merge-queue group and a
push to `main`, CI runs hygiene, backend, frontend and the database gates job:

- **Backend** checks the Go formatting, builds and vets the whole module, runs
  `python3 scripts/ci/check-api-docs.py`, and runs `python3 scripts/ci/check-changed-go.py` on the
  plan job's changed paths: one `go test -count=1 -timeout 10m` over the selected packages, without
  `-race` and without a database server (MySQL-backed tests skip there).
- **Go formatting (#405).** `python3 scripts/ci/check-go-format.py` runs `gofmt -l` on every Go
  file git tracks (untracked sources are never read) and fails with the list of files gofmt would
  rewrite, or with gofmt's report when a file does not parse. Both paths of the Backend job start
  it before `go build`: `check-changed-go.py` on every run that builds and vets, a
  documentation-only change included (not with `--tests-only`, so not in the Database gates job),
  and `make verify-backend` as its first step on a full run. To fix a failure, run `gofmt -w` on
  the listed files and commit the result. The check uses the `gofmt` on `PATH` (the one next to
  `go` when there is none): in the Backend job that is the Go version of `go.mod`, locally your
  own. A Go upgrade that changes gofmt's output fails the check on every pull request until the
  tree is reformatted, so reformat in the pull request that raises the version.
- **Database gates (MariaDB 10.11)** runs the same selection with `--tests-only` and
  `TEST_MYSQL_DSN` pointing at a MariaDB 10.11 service, so the MySQL-backed tests of the selected
  packages run there. Nothing rejects a skip in this job.
- A documentation-only change (`*.md`, anything under `docs/`, `LICENSE`) runs no Go test at all.
- The race detector (`make verify-backend`, which starts with the formatting check), `make verify-api-docs`, the no-skip checkers
  (`check-go.py transaction`, `check-auth.py --persistence`), the MySQL 8 service of the Backend
  job, the browser matrix and the two-engine run of the Database migrations workflow happen only
  when CI is called with `full: true`: the release workflow, or a manual dispatch of CI with the `full` input
  (which runs everything but the browsers).
- A pull request that changes migration code (a path under `migrations/` or `cmd/migrate/`, the
  migrations workflow or its coverage gate) also runs the `Migrations (MySQL 8)` leg of the
  Database migrations workflow; see below.
- A manual dispatch has no base to compare with, so with `full: false` it tests every package
  without `-race`.

What `check-changed-go.py` selects, from a universe of every package outside `gocrm-ui/` and `docs/`:

1. A plan without changed paths, an empty change set, or a change to `go.mod`/`go.sum`: every package.
2. A `.go` file whose directory is not a package at the head (a deleted package): every package.
3. Documentation selects nothing.
4. Any other path selects the nearest directory (its own included) that is a package, so files
   behind a build tag, `testdata` and embedded assets resolve to their package.
5. Every package that imports one of those, transitively, through its sources or its tests.
6. Packages whose tests read the changed files from disk without importing them (added as they
   are, without their importers):

   | Changed path | Also selects |
   |---|---|
   | `migrations/` | `cmd/migrate`, `internal/service`, `internal/config` |
   | `api/` | `internal/models`, `tests`, `scripts/ci/apiroutes` |
   | `internal/handler/`, `internal/utils/`, `cmd/` | `scripts/ci/apiroutes` |
   | `scripts/ci/check-api-docs.py` | `scripts/ci/apiroutes` |
   | `internal/`, `cmd/` | `cmd` |
   | `internal/models/`, `internal/service/`, `internal/utils/`, `internal/repository/`, `cmd/`, `scripts/release/` | `internal/config` |
   | `.github/workflows/release.yml` | `scripts/release` |

7. Any other non-documentation path that is in no package and matches no row (`.env.example`, the
   compose files, `Dockerfile`, `Makefile`, `gocrm-ui/`, `scripts/deploy/`, `scripts/ci/*.py`, other
   workflows) selects `internal/config` alone.

When `internal/middleware` or `internal/aeo` is selected, a second run adds `-tags=gophercrm_e2e`
for those two. The log starts with `Go tests: <n> of <total> packages selected` and the package
directories, `Go tests: none selected (documentation-only change)` or
`Go tests: every package (<reason>)`, and prints each command before running it, the formatting
check first.

```bash
python3 scripts/ci/check-go-format.py                              # gofmt over the tracked Go files
python3 scripts/ci/check-changed-go.py --base origin/main          # what CI will format-check, build, vet and test
python3 scripts/ci/check-changed-go.py --base origin/main --list   # the selection only
```

`--base` compares that revision with `HEAD`, so it sees committed changes only: on a tree with
uncommitted edits a "none selected" is not a pass. Two consequences to know:

- **A new test that reads files outside its own package needs a row** in `EXTERNAL_READERS`
  (`scripts/ci/check-changed-go.py`), with a case in `scripts/ci/tests/test_changed_go.py`. Until it
  has one, only a full run exercises it when those files change.
- **`internal/config` pins documentation text** (`README.md`, `CHANGELOG.md`, `SERVER_PROVISIONING.md`,
  `docs/README.md` (the CONCURRENCY index bullet), `docs/KNOWN_LIMITATIONS.md` (the
  `DISABLE_RATE_LIMIT` sentences) and `docs/{DEVELOPMENT,FORKING,DOCKER,USE_CASES,ROADMAP,EXTENDING,SETUP}.md`). A documentation-only
  pull request runs no Go test, so it can break one of those pins and still be green; the next change
  that selects `internal/config` then fails for a reason outside its diff. Run
  `go test -count=1 ./internal/config` after editing those files. `internal/permissions` pins
  `docs/AUTHORIZATION.md` in the same way (its catalogue and matrix tables against the code, the
  keys and routes of its integration-user section, and the pointers to it in `docs/README.md`,
  `docs/FEATURES.md` and `CHANGELOG.md`): run
  `go test -count=1 ./internal/permissions` after editing any of those four.
- **The administrator guide under `docs/user-guide/permissions-access/` is checked by name**
  (#447). `python3 scripts/ci/check-user-guide.py` resolves every backticked name in its six pages
  against the code: an environment variable or error code (and `NAME=` in a fenced block) must be a
  string literal in a tracked non-test Go file, as must a dotted setting or permission key; a
  `METHOD /path` route must exist with that method in `api/swagger.json`; a bare `/path` must be a
  swagger path or a `path:` entry of `gocrm-ui/src/routes/index.tsx`. It fails on a missing page
  and on a class with no token at all. Write a route with one method per backticked token
  (`GET /settings/sso`, never a combined pair), path parameters as `{id}` as in the swagger, and
  without the `/api/v1` prefix; write host names without backticks. Its unit test,
  `scripts/ci/tests/test_user_guide.py`, runs the check on the real guide inside
  `make verify-hygiene`, so a rename in the code that the guide still names fails the `hygiene` job.
- **The docs cite code by symbol, and the citations are checked** (#487, #488, #489).
  `python3 scripts/ci/check-doc-citations.py` checks every tracked `docs/**/*.md` and `README.md`,
  discovered through `git ls-files`, so a new doc is covered the moment it is tracked (pass file
  names as arguments to check only those; a file named explicitly is checked in full). The one
  listed addition is `internal/idkit/README.md`, named in the script's `EXTRA_DEFAULT_FILES`; other
  `README.md` files outside `docs/` are not discovered. `CHANGELOG.md` is deliberately not checked:
  its released entries are history and name files that were later removed or renamed. No doc
  is exempt: the script's `EXEMPT_FILES` is empty since `docs/CONCURRENCY.md` was converted to
  symbol citations too (#743), and `internal/config/concurrency_doc_test.go` pins each of its
  auth-service symbols (`internal/service/auth_service.go` and the per-flow files split from it,
  #639) to the statement the doc cites it for (#581).
  Every doc goes through all four rules. Every backticked
  repository path must be a tracked file or directory, judged by `git ls-files` only, so an
  untracked or gitignored local file does not count and a local run agrees with CI; a `*` glob must
  match a tracked file, and a path with `<`, `>`, `{`, `}` or `$` is a placeholder. The gitignored
  outputs and local directories the docs legitimately cite are listed in its `IGNORED_OUTPUTS`
  allowlist (`bin/gophercrm`, `gocrm-ui/.env`, `gocrm-ui/dist`, and the local-only `docs/specs`,
  `docs/plans` and `gocrm-ui/node_modules`), which also covers any path below an entry. The one
  path skip is a planned Playwright spec: a missing `gocrm-ui/e2e/tests/*.spec.ts` cited in a
  `docs/testing/*.md` catalog file passes, because the catalog names the specs it plans and
  dropping those names would change its Automation lines and case counts; the same missing spec
  in any other doc fails. A Go symbol is cited next to its file, as
  `` `internal/models/database.go` `autoMigrateModels` `` or
  `` `setupDependencies` (`cmd/main.go`) ``, with a full repository path; the symbol (a func, a
  method as `Type.Method` or `(*Type).Method`, a testify suite test as `Suite.TestName`, a type,
  var or const, an interface method or a struct field, optionally prefixed by the package name)
  must be declared in that file, which the go/parser helper `scripts/ci/docsymbols` lists. A
  symbol that is not next to its path in one of those two forms is not checked, so keep the pair
  together. No checked doc may cite code by line: a `file.go:N`-style citation (`.go`, `.ts`,
  `.tsx`, `.py`, `.sh`, `.sql`) anywhere, fenced blocks included, fails. Cite a non-Go file by its
  full path plus a function, component or const name, a route path, a heading, job or step name,
  key, shell function or a short quoted anchor that exists in that file. In review, treat a line
  citation in a doc as a defect and ask for the file plus symbol or anchor. The six Compose and
  Dockerfile line citations in `docs/FORKING.md` and the Compose line citations in
  `docs/DOCKER.md` stay, because tests recompute them from the files
  (`TestComposeUIDocCitations` for `docs/DOCKER.md`) and rule c does not match `.yml` citations.
  Rule d checks the links: every inline Markdown link outside a fenced block whose target has no
  URL scheme and does not start with `/` must name a `.md` file, optionally with a `#anchor`, or a
  bare `#anchor` in the same file. The file is resolved against the citing doc's directory and must
  be tracked, and the anchor must be the GitHub slug of one of the target's headings outside fenced
  blocks (link text kept, backticks and punctuation dropped, spaces turned into `-`, a repeated
  heading suffixed `-1`, `-2`) or an explicit `<a name>`/`<a id>`. A moved or renamed heading
  therefore fails the check in every doc that still links its old anchor.
  Its unit test, `scripts/ci/tests/test_doc_citations.py`, runs the check with its default
  discovery on the real tree inside `make verify-hygiene`.

Since 11 November 2026 CI runs no browser spec outside a release: the complete browser matrix on
MySQL, MariaDB and SQLite runs only when the release workflow calls CI with `full: true`. Browser
coverage before publication comes from the remediation runner's local validation (the changed
feature's specs on SQLite), which is a separate scope. Local `make e2e` still defaults to the full
suite. See [validation and releases](VALIDATION_RELEASE.md)
for selection, full-suite overrides, version preparation, rehearsals and publication.
Production deployment follows a published full-validation release, rather than every main push.

The separate [Database migrations workflow](../.github/workflows/migrations.yml) runs focused
fresh/upgrade/rollback checks on disposable MySQL 8 and MariaDB 10.11 services. CI calls it in two
cases. On a `full` run (a release, or a manual dispatch with `full: true`) both engines run. On a
pull request whose diff changes migration code only `Migrations (MySQL 8)` runs, with the same
steps: the `plan` job's `migrations` output (`scripts/ci/plan-validation.py`) is `true` when a
changed, added or deleted path is under `migrations/` or `cmd/migrate/` or is in the selector's
`MIGRATION_WORKFLOW_FILES` (the migrations workflow, `scripts/release/check-sql-tests.py` and its
tests), and also when the change set or that set cannot be read. Any other pull request, a
merge-queue group and a push to `main` do not call it, and a direct dispatch of the workflow for
diagnosis runs both engines. Every leg
retains the existing SQLite upgrade fixtures and rejects missing or skipped required coverage. The
artifacts include the tested revision and Go JSON results, and on a full run the required Backend
check fails unless both jobs succeeded. On a pull request `Migrations (MySQL 8)` is not a required
check and the Backend check does not depend on its result, so branch protection does not wait for
it; the remediation runner waits for every check on the pull request and treats any failed one as
failed, so a red leg still stops its merge. On a pull request a change under `migrations/` also runs the
`cmd/migrate`, `internal/service` and `internal/config` tests through the selection above (on
MariaDB in the database gates job); see [migration CI](VALIDATION_RELEASE.md#migration-ci) for the exact coverage.
The `./cmd/migrate` race run in this workflow (so in every pull request's `Migrations (MySQL 8)`
leg as well) and in the release `sql-validation` job uses `go test -timeout 30m` inside a 40-minute
job, because a slow runner took 2.4 times as long and a package timeout drops every root after the
slow one; `scripts/release/tests/test_sql_coverage.py` pins both values.

The same runner also hosts the feature analysis workflow (`gophercrm-runner analyze`, see the
[runner guide](../automation/langflow/README.md#analyse-a-feature-request)), which writes a
gitignored `docs/specs/` document and never changes tracked files.

Runner changes require `make verify-runner` and the focused checks selected by the policy.
Install `automation/langflow` with its `flows` extra
in an isolated Python 3.12 environment and set `RUNNER_PYTHON` when that interpreter is not
`python3`. This target runs the deterministic workflow suite, compares the checked-in graph
without rewriting it, and tests the read-only database probe. Runner CI uses the same target,
including merge-group events. See the [runner guide](../automation/langflow/README.md).

The [runner operations guide](../automation/langflow/OPERATIONS.md) covers a shared recovery
report, worker heartbeats, consistent batch-budget extensions, validation previews and explicit
cleanup of completed dependencies. Heavy commands across state directories share one per-user
machine lock and retain bounded resource observations. Readiness compares installed runner
modules with the trusted checkout; verify this after upgrades and restart long-lived workers.
These controls preserve the selected acceptance/review gates and do not enable automatic cleanup.

The runner preserves Go assertion failures in bounded repair diagnostics instead of showing
only the end of a long log. Expected structured service logs from mock failures do not classify
a separate Go assertion failure as unavailable infrastructure. Inspect full private logs before
recovery: genuine environment errors still require restoring prerequisites. For a verified
misclassification, the shared `repair-validation` command records an exact-commit repair request
without refunding limits or relabelling failed evidence; normal repair, validation and review
remain mandatory. See [validation recovery](../automation/langflow/README.md#inspected-validation-misclassification).

On ordinary validation resume, successful checks for the unchanged candidate can be reused
only after verifying their original checkout and complete receipt identities/digests. Missing
checks run in a fresh checkout with its own locked browser dependencies. Failed full browser
commands still run in full; source repairs and base refresh invalidate earlier checks. This
avoids repeating passed backend/frontend/SQLite checks after a later environment interruption
without weakening the selected gates or independent review.
If the old checkout or evidence is missing or changed, the runner records the invalidated
checkpoint and reruns all selected checks under a new validation identity. Original artifacts,
spent calls/repairs and the deadline remain intact. Runner/gate code is protected from repair
agents; maintainers prepare those corrections and use the existing PR refresh workflow.

Update relevant documentation in the same PR. CI identifies affected guides; the local
workflow records explicit review decisions and verifies documentation after merge. Include
documentation paths during task preparation and explain when an unchanged document remains
accurate. See [documentation maintenance](DOCUMENTATION_WORKFLOW.md).

Backend coverage spans handlers, services, middleware (auth, rate limit, error handler, recovery,
request ID), utils (sort, password, response, crypto, context, transaction), config, and models.

The E2E suite needs a real backend and database. Admin accounts
cannot be self-registered — `gocrm-ui/e2e/global-setup.ts` seeds them by shelling out to
`cmd/create-admin`. See `gocrm-ui/e2e/README.md` and the test-case catalog in [testing/README.md](testing/README.md).

### System-role route matrix (#322)

`cmd/testdata/system_role_route_matrix.json` records what the router `setupRouter` builds answers
to each of the four system roles (admin, sales, support, customer) on every protected route: the
HTTP status and the envelope's `error.code` (`""` on success or a body that is not an envelope).
`cmd/system_role_route_matrix_test.go` holds the harness and five tests. It runs on a SQLite file
only, on purpose: one golden characterises the router's guards, and the database roots keep
covering MySQL and MariaDB behaviour of the handlers.

- **Cases.** A route with a path parameter has `own`, `foreign`, `missing` and `malformed`; a
  route without one has only `own`, the valid request; a route whose handler binds a body also has
  `malformed_body` (invalid JSON, on the own id). `own` is a record the caller owns or is assigned
  (lead, deal, quote and company owner; task and ticket assignee; the customer record assigned to a
  staff caller or linked to the customer login, and a ticket of that customer; the caller's own
  user id, API key and notification). `foreign` is a live record of another user; for a resource
  without an owner it is a second existing record. `missing` is a well-formed id no row has, not
  even a soft-deleted one; `malformed` is a non-numeric id. Where the parameter is not a record id
  (`/configurations/:key`, `/configurations/category/:category`) the route's `note` says what the
  four cases mean.
- **Parents.** On a parent-scoped list (`/customers/:id/deals`, `/customers/:id/quotes`,
  `/customers/:id/tickets`, `/deals/:id/quotes`, `/deals/:id/tasks`, `/companies/:id/deals`) the
  cases refer to the parent record. On the four two-parameter routes
  (`PUT|DELETE /quotes/:id/lines/:lineId`, `DELETE /customers/:id/external-ids/:mappingId`,
  `DELETE /products/:id/external-ids/:mappingId`) the parent is the caller's own parent in every
  case and the child id varies; `foreign` is an existing child of another parent, and the `note`
  says so.
- **Notes.** The own request carries a valid body, so admin `own` is 2xx. Where it cannot be (an
  administrator deactivating or deleting itself, an AEO call with no provider configured) the
  route's `note` says why; generation fails on a non-2xx admin `own` without a note.
- **Public list.** Every route of `router.Routes()` is either in the golden or in the explicit
  `routeMatrixPublic` list in the test: `/health`, the nine unauthenticated `/auth` routes (the
  OpenID Connect start, callback and front-channel sign-out included), the six `/forms/public` routes, the two
  `/quotes/public/response` routes and `POST /quotes/public/render`, and the fourteen `/scim/v2`
  routes (the discovery documents of #468, the Users read of #469, the Users create and replace
  of #470, the Users patch of #471, the Users delete of #504 and the five Groups routes of #506),
  33 in all. A new route in neither
  fails `TestRouteMatrixCoversEveryProtectedRoute`.
- **Isolation.** Every cell runs against the same seeded state: a cell that wrote anything is
  followed by a restore of every table from a snapshot. Each request has a client address of its
  own, because the authenticated rate tier is live in a normal build; a 401 or 429 in a cell is a
  harness failure, never a recorded value. Nothing leaves the process (no SMTP host, no reCAPTCHA
  keys, no AEO provider keys or egress origins, inactive webhook endpoints).

The golden is frozen (see the last paragraph of this section), so a pull request that adds a
protected route no longer regenerates it. It lists the route on `routeMatrixAfterFreeze` (the full
`METHOD /api/v1/...` key), declares the request in `matrixRequests` (fixture kind, valid body,
note) and adds the route's key to `permissions.RouteKeys`. The replay and the recorded-versus-served
comparison leave such a route out; `TestRouteMatrixCoversEveryProtectedRoute` checks that every
listed route is served, is absent from the golden and has a `RouteKeys` entry, and the route is
covered by `TestZeroPermissionRoleIsDeniedEverywhere` and `TestEveryRouteRequiresItsOwnKey`
instead. The eight routes of the role API (#348) are the first entries; their fixture is a pair of
unassigned custom roles without permissions, restored with every other table after a cell that
wrote. A pull request that changes what an existing route answers to a system role changes a
recorded cell and fails the replay: that is the regression the golden exists to catch.

The generator stays in the tree for its guard test:

```bash
go test ./cmd -run '^TestSystemRoleRouteMatrix$' -update-route-matrix
```

It refuses to write (see below). `base_commit` is `main` at the point the golden was generated
from; a status that looks wrong in the golden was recorded as observed and reported, never
adjusted by hand.

The fixture anchors (`TestRouteMatrixFixtureAnchors`) do not read the golden, because the golden
is written by the harness that replays it. They assert from the database that each `own`,
`foreign` and `missing` fixture is what its name says, and send a hand-written table of documented
ownership rules to the live router (sales on its own and a foreign lead, deal, quote and task;
support and customer on tickets; admin on each foreign record). Extend that table by hand when an
ownership rule is added; never derive it from the golden.

Once a route file (`cmd/main.go` or a non-test `internal/handler/*routes*.go`) uses
`RequirePermission` or `RequireAnyPermission`, generation refuses and writes nothing, while the
replay test keeps running against the frozen golden: from then on the file is the record of the
system roles before permission guards, not something to regenerate.

### Sign-in, SCIM and last-admin characterisation gate (#636)

Two recorded-output suites and the existing last-admin roots pin what single sign-on, SCIM
provisioning and the last-admin rule do today. A pull request that refactors authentication runs
this gate and passes it unchanged.

**Single sign-on golden.** `internal/service/auth_oidc_golden_test.go` `TestOIDCSignInGoldenSQLite`
and `internal/service/auth_oidc_golden_test.go` `TestOIDCSignInGoldenMySQL` replay one fixed run of
20 callbacks against the in-process test issuer on one database, with no reset between steps: three
sign-ins (a just-in-time provisioning, an unchanged repeat, a remap to support), then 17 refusals
(`state_invalid`, `binding_missing`, `binding_mismatch`, `id_token_invalid`, `nonce_mismatch`,
`subject_invalid`, `groups_malformed`, `group_overage`, `userinfo_subject_mismatch`,
`groups_unavailable`, `no_mapping_match`, `email_taken`, `account_inactive`, `account_erased`,
`last_admin`, `token_exchange_failed`, `issuer_changed`). Both roots compare their run with the one
file `internal/service/testdata/oidc_signin_golden.json`, so every step has the same outcome on
SQLite, MySQL and MariaDB. Per step it records the error class (`""`, `sign_in_failed` or
`unavailable`), whether tokens were returned, the refresh tokens added, the step's one
`auth_audit_log` row, its `role_audit_log` rows and the account the step's identity is linked to
afterwards. Users appear by fixed labels (`jit`, `admin_only`, `inactive`, `erased`,
`password_owner`) and roles by name; a claimed attempt is recorded as `claimed` after it is checked
against the step's state, a subject reference as `hmac` after it is checked to be 64 hex digits.
Ids and timestamps are left out. The only other normalisation replaces the test issuer's URL with
`<issuer>` and the second issuer of `issuer_changed` with `<other-issuer>`; the file holds no URL,
email address or raw subject. `internal/service/auth_oidc_golden_test.go` `TestOIDCSignInGoldenCompare`
checks that the comparison reports a changed reason, a missing `auth_audit_log` row and an extra
`role_audit_log` row by their step, and nothing for the unaltered file.

`identity_taken` is engine-specific and is not in the golden: it is reached only through the
unique-index collision of a concurrent link insert, which MySQL and MariaDB refuse while SQLite
replays the busy write and signs in. The `scim_oid_concurrent_link_before_insert` case of
`internal/service/auth_oidc_test.go` `TestOIDCSignInMySQL` pins it.

**SCIM provisioning cycle golden.** `cmd/scim_cycle_golden_test.go` `TestSCIMProvisioningCycleGolden`
sends 20 requests in Microsoft Entra ID's documented shapes through `setupRouter` over a fresh
SQLite file, each through the SCIM test client `internal/idkit/scim/scimtest/client.go`
`scimtest.Client` (bearer token, `application/scim+json`, its own client address): the user lookup, create, repeated create, read, patch with and without a path,
replace, the group lookup, create, member add, read, member remove and delete (answered 405), a
missing user, an unparsable filter, a malformed patch body, the deactivation, the delete and a read
after it. `cmd/testdata/scim_provisioning_cycle.json` records each request and its status, the
`Content-Type`, `Location` and `ETag` headers (`""` when absent) and the body. Only `meta.created`
and `meta.lastModified` are replaced with `<timestamp>`, after they are checked to be RFC 3339 and
in order; ids, locations and hosts are kept as served. The file never holds the SCIM token.
`cmd/scim_cycle_golden_test.go` `TestSCIMProvisioningCycleGoldenCompare` checks that a changed
status, a missing body field and a changed header are reported by their step.

**Golden files.** They are written only by their update flags:

```bash
go test -count=1 ./internal/service -run '^TestOIDCSignInGoldenSQLite$' -update-oidc-golden
go test -count=1 ./cmd -run '^TestSCIMProvisioningCycleGolden$' -update-scim-cycle
```

The MySQL root never writes. A refactoring pull request leaves both files byte-identical. A change
to either is a behaviour change for its own ticket: it is regenerated, recorded as observed and
never edited by hand.

**Last-admin roots.** On MySQL and MariaDB, eight roots:
`internal/service/auth_governed_last_admin_test.go` `TestGovernedLastAdminAfterHandoverMySQL`,
`internal/service/auth_oidc_test.go` `TestOIDCSignInMySQL` (its `last_admin` subtests and
`last_admin_race/after_handover`), `internal/service/auth_scim_groups_test.go` `TestSCIMGroupsMySQL`,
`internal/service/auth_scim_user_delete_test.go` `TestSCIMUserDeleteMySQL`,
`internal/service/auth_scim_user_patch_test.go` `TestSCIMUserPatchMySQL`,
`internal/service/auth_scim_user_writes_test.go` `TestSCIMUserWritesMySQL`,
`internal/integration/role_last_admin_integration_test.go` `TestLastAdminInvariantMySQL` and
`internal/integration/role_last_admin_integration_test.go` `TestLastAdminConcurrentMySQL`. On
SQLite, four roots: `internal/service/auth_last_admin_guard_test.go` `TestLastAdminGuard`,
`internal/integration/role_last_admin_integration_test.go` `TestLastAdminInvariantSQLite`,
`internal/integration/role_last_admin_integration_test.go` `TestLastAdminConcurrentSQLite` and
`internal/integration/role_last_admin_integration_test.go` `TestLastAdminAPISQLite`.

**Commands.** With `TEST_MYSQL_DSN` set to the MySQL 8 server (the MySQL roots skip without it, so
check that they ran):

```bash
timeout 540 go test -count=1 -run '^TestOIDCSignInGolden(SQLite|MySQL|Compare)$' ./internal/service
timeout 300 go test -count=1 -run '^TestSCIMProvisioningCycleGolden(Compare)?$' ./cmd
timeout 540 go test -count=1 -run '^(TestGovernedLastAdminAfterHandoverMySQL|TestSCIM(Groups|UserDelete|UserPatch|UserWrites)MySQL|TestLastAdminGuard)$' ./internal/service
timeout 540 go test -count=1 -run '^TestOIDCSignInMySQL$' ./internal/service
timeout 540 go test -count=1 -run '^TestLastAdmin' ./internal/integration
```

The same on MariaDB 10.11, with `TEST_MYSQL_DSN="$TEST_MARIADB_DSN"` pointing the MySQL roots at
it:

```bash
TEST_MYSQL_DSN="$TEST_MARIADB_DSN" timeout 540 go test -count=1 -run '^TestOIDCSignInGoldenMySQL$' ./internal/service
TEST_MYSQL_DSN="$TEST_MARIADB_DSN" timeout 540 go test -count=1 -run '^(TestGovernedLastAdminAfterHandoverMySQL|TestSCIM(Groups|UserDelete|UserPatch|UserWrites)MySQL|TestLastAdminGuard)$' ./internal/service
TEST_MYSQL_DSN="$TEST_MARIADB_DSN" timeout 540 go test -count=1 -run '^TestOIDCSignInMySQL$' ./internal/service
TEST_MYSQL_DSN="$TEST_MARIADB_DSN" timeout 540 go test -count=1 -run '^TestLastAdmin' ./internal/integration
```

### Vitest timeouts under load (#403)

Some Vitest cases time out on a heavily loaded machine. #403 reduced this; it is not closed for the
whole suite.

- **The failures (2026-09-25, during `make verify` while the Go race tests and other work were
  running; load average up to 71):**
  - `src/routes/index.test.tsx` › "blocks non-admin users from /users via the pathless
    ProtectedRoute layout": the page rendered empty and `findByText` gave up after 1 s.
  - `src/pages/forms/FormBuilder.test.tsx` › "refuses a dropdown without options" and "lets an
    admin pick the lead owner from the user list" hit Vitest's 5 s test timeout.
  - Later runs under contention added `src/layouts/MainLayout.test.tsx` › "shows the Automation
    group for admin and sales and hides it for support and customer" and synchronous
    `FormBuilder.test.tsx` cases to the list.
- **Not regressions:** each file passes alone.
- **What was done (#403):**
  - `make verify-frontend` and the documented one-shot command run Vitest with `--maxWorkers=3`
    (`npm test -- --run --maxWorkers=3`).
  - `make verify` runs the frontend gate before the race-enabled Go tests, so the two no longer
    compete for the processor.
  - Every test in `src/routes/index.test.tsx`, `src/pages/forms/FormBuilder.test.tsx` and
    `src/layouts/MainLayout.test.tsx` carries an explicit test timeout (30 s or more) and every
    `findBy*`/`waitFor` call in them an explicit 15 s timeout, and the `MainLayout` test waits
    for the opened Automation group instead of reading it straight after the click. These three
    files no longer depend on the runner's 5 s test default or the 1 s query default: with the
    runner default forced to 1 ms (`--testTimeout=1`) all 63 of their tests pass, where 36 failed
    before.
- **The check that was run:** ten consecutive runs of the three files next to one CPU-bound busy
  process per core (10 on a 10-core machine), started and stopped by the test command itself:
  63 of 63 passed in all ten runs. That load is the bound set for the check; it is not claimed
  to reproduce the original timeouts.
- **What remains:** the sibling files (`MainLayout.branding.test.tsx`,
  `MainLayout.permissions.test.tsx`, `routes/index.permissions.test.tsx`,
  `FormBuilder.ticket.test.tsx`) and the rest of the suite keep the default timeouts and rely on
  the worker cap. A machine loaded well beyond one busy process per core can still time them
  out; give a file that does so the same explicit timeouts.

## API docs gate

`make verify-api-docs` (part of `make verify` and of every runner validation profile) fails when the
reference is stale or incomplete. It is two commands: `go test ./scripts/ci/apiroutes` (the route
extractor's own tests), then `python3 scripts/ci/check-api-docs.py`. CI runs the make target only on
a `full` run. Every other CI run executes `python3 scripts/ci/check-api-docs.py` directly, on every
change (documentation-only included), and tests the extractor only when
`scripts/ci/check-changed-go.py` selects `scripts/ci/apiroutes` (a change to the extractor, to
`check-api-docs.py`, to `internal/handler/`, `internal/utils/`, `cmd/` or `api/`). The check regenerates the three
artifacts into a temporary directory and compares them byte for byte; matches routes and
operations in both directions (the route list comes from `scripts/ci/apiroutes`, a go/ast reader
that follows `setupRouter`, the `Setup*Routes` calls, group prefixes and aliases, and refuses
registration forms it cannot resolve, such as `Any`, `Handle` or a non-literal path; `GET /health`
outside the base path is the one exclusion); requires descriptions on every operation,
parameter, response, tag and property (inline schemas included; an override of a property its
base definition describes, such as the envelope's `data`, is exempt); requires one declared tag
per operation, at least one 2xx/3xx and one 4xx/5xx response, resolvable `$ref`s, matching path
parameters, `basePath` equal to the `API_PREFIX` default in `internal/config`, and exactly the
`BearerAuth` and `ApiKeyAuth` schemes on the `Authorization` header, each description showing its
`Authorization: <scheme> <credential>` form; requires the security of each operation to match its
route (behind `middleware.Auth` exactly the two alternatives `[{BearerAuth: []}, {ApiKeyAuth: []}]`,
with no anonymous `{}` alternative or combined requirement; none on a public route; no global
`security`; the extractor tracks `Use`, `Group` and per-route middleware, also through variables
such as `strict := middleware.RateLimitStrict()`); requires every status the route middleware
can answer with to be a documented response (401 for `middleware.Auth`, 403 for
`middleware.RequireRegistrationEnabled`, 429 for the three rate limiters). The 403 of a protected
route comes from the permission guard `handler.Protected` builds from `permissions.RouteKeys`,
which the extractor does not count; `TestAPIReferenceNamesRouteKeys` in `internal/handler` holds
every such operation to a documented 403 that names the route's keys. The extractor also
identifies the handler each route registers (a method value on a typed handler variable, a function, or a factory call such as
`handler.RegistrationStatus(...)`), requires that handler to carry the route's `@Router` line and
refuses an `@Router` line for a routed path on any other function; compares the inputs of that
handler (query parameters, form fields and a bound body, found by the same tool through the
helpers it hands the `*gin.Context` to, `utils.ParseOffsetLimit` included) with the documented
parameters in both directions, and for a bound body requires exactly one body parameter whose
schema is a `$ref` to the bound Go type, `required` exactly when the handler refuses a missing body
(its own top-level `if err := c.ShouldBindJSON(&req); err != nil`), and a documented 400; it
refuses readers it does not model (`QueryMap`, `ShouldBindQuery`, ...), a bound value whose type it
cannot read, and a context or router group (a variable or an inline `Group(...)` call) handed to a
function it cannot follow; and checks the known closed sets (roles, statuses, stages, automation and
form enums) against their Go constants in `internal/models`, so a new constant without the
matching `enums` tag fails. The closed-set table lives in `scripts/ci/check-api-docs.py`; extend it
when a new closed set reaches the API. Mutation tests for every rule are in
`scripts/ci/tests/test_api_docs.py`, the renderer's tests in `test_api_reference.py`.
Semantic accuracy (does the prose match the code?) remains a review responsibility.
