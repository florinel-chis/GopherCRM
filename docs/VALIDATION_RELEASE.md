# Contextual validation and releases

Validation selects checks relevant to the changed code, with application health smoke
on ordinary changes and the complete matrix before releases. It does not claim that a
path map proves the absence of regressions. Unmapped shared changes retain full coverage; mapped transaction changes use direct
boundary and caller tests with health smoke. Production deploys move to validated, published releases.

## Working prompt

> **Historical.** This section describes the Langflow runner's local validation. Ticket work
> no longer runs through the runner; the text is kept as a record of its design and rollout.

> Implement a documented validation policy that selects relevant tests from the changed
> files, defaults to full validation for uncertain or high-risk changes, and preserves
> independent review and exact-commit merge checks. Add a release workflow that validates
> the release commit, generates a changelog, tags it and publishes the release only after
> all gates pass. Start with an MVP, test its failure paths, and document observed lessons
> and remaining improvements. Preserve already-admitted executions and their evidence.

## Validation policy

| Gate | Every PR / contextual runner | Full release |
|---|---|---|
| Hygiene and core checks | Runner (local): auth/transaction/migration/AEO/mailer profiles: hygiene + API docs gate + targeted Go checks + build/health; runner-only: hygiene + API docs gate + runner tests + build/health; other application changes: `make verify`. CI: hygiene, frontend, and module build + vet + `go test -count=1` on the changed Go packages, their importers and the packages that read the changed files (`scripts/ci/check-changed-go.py`, no `-race`; none for a documentation-only change) | Complete `make verify`, then `check-go.py transaction` and `check-auth.py --persistence` |
| API reference gate | Runner: `make verify-api-docs` in every profile. CI: `python3 scripts/ci/check-api-docs.py` on every change; the extractor's Go tests only when selected | `make verify-api-docs` |
| Frozen issue acceptance tests | All named checks, before browsers | Their ordinary suites plus SQL integration |
| Browser coverage | Health smoke plus affected feature specs, or full fallback | Every enabled spec |
| CI database engines | SQLite (backend job) and MariaDB 10.11 (database gates job: the selected packages, `-count=1`, no `-race`, skips not rejected) | MySQL 8, MariaDB 10.11, SQLite |
| Local runner database engines | SQLite; also MySQL for persistence/full fallback | CI owns the release matrix |
| Migration coverage | CI: a pull request that changes migration code runs `Migrations (MySQL 8)` (not a required check), and a change under `migrations/` or `cmd/migrate` runs the `cmd/migrate` tests on MariaDB through the selection. Runner: local persistence/full fallback also requires migration checks; skips rejected | Dedicated MySQL/MariaDB migration and SQLite upgrade CI: fresh, upgrade, rollback; skips rejected |
| Runner tests | Required when the workflow changes | Always |
| Independent review and exact-head merge checks | Required | Release uses the already-reviewed main commit |

The "Runner" entries above and the per-profile local-validation paragraphs below describe the
Langflow runner's local checks; what CI does is stated in "What CI runs outside a release".

### What CI runs outside a release (#365)

The profile the selector picks decides what the **runner** executes locally and which frontend
branch CI takes. For the Go side, CI has two modes only:

- **Not `full`** (every pull request, merge-queue group and push to `main`, whatever the profile):
  the Backend job builds and vets the module, runs `python3 scripts/ci/check-api-docs.py`, and runs
  `python3 scripts/ci/check-changed-go.py --plan <validation-plan.json>`: first
  `python3 scripts/ci/check-go-format.py` (`gofmt -l` on every tracked Go file; an unformatted file
  fails the job before the build), then `go test -count=1` without
  `-race` on the packages the change touches, their transitive importers and the packages whose
  tests read the changed files. The Database gates job runs the same selection with `--tests-only`
  against MariaDB 10.11 (`TEST_MYSQL_DSN` set, skips not rejected). A documentation-only change runs
  no Go test. The browser matrix is skipped, and so is the migrations matrix unless a pull request
  changes migration code: then its MySQL 8 leg runs (see [Migration CI](#migration-ci)). The selection rules and the
  external-readers table are in [DEVELOPMENT.md](DEVELOPMENT.md#testing).
- **`full`** (the release workflow, or a manual dispatch of CI with the `full` input): the two-engine
  migrations matrix, `make verify-api-docs`, `make verify-backend` (the formatting check
  `scripts/ci/check-go-format.py`, then build, vet and `-race` tests), then
  `check-go.py transaction` and `check-auth.py --persistence` on MySQL 8, and
  `check-go.py transaction` on MariaDB 10.11. The browser matrix additionally needs a calling
  workflow, so only a release runs it.

The profile checkers (`scripts/ci/check-auth.py`, `scripts/ci/check-go.py <profile>`) therefore run
in CI only on `full`; the per-profile paragraphs below describe the runner's local validation.
A manual dispatch has no base commit, so its plan records no changed paths: with `full: false` it
tests every package without `-race`.

Outside CI, the [Docker image](../.github/workflows/docker-image.yml) workflow (#420) builds the
backend image and prints one PDF with its browser weekly, on demand and on pull requests that
change the `Dockerfile` or the workflow; it is not a required check, and no profile selects it.

[Documentation maintenance](DOCUMENTATION_WORKFLOW.md) adds a commit-bound impact plan and
explicit documentation decisions to the existing independent review call. The runner verifies
the reviewed document blobs after merge. CI retains the impact report for PRs and main pushes;
that report is advisory input to semantic review. This does not widen the selected test profile
or add a separate model call on the successful path.

`automation/langflow/gophercrm_runner/validation.py` owns the deterministic path map.
Known entity changes select related specs and the read-only health smoke test; mixed
changes take the union. Unmapped shared infrastructure, schema, dependencies, hand-written API contracts,
unknown application paths select the complete browser suite. Runner-only changes under
`automation/langflow/`, `scripts/ci/`, and the CI/runner workflow definitions select the
`runner` profile: local `make verify-hygiene`, `make verify-api-docs`, `make verify-runner`, all frozen acceptance
checks, and SQLite health smoke. CI runs the frontend build health check, the targeted Go run
described above (the Go probe under `automation/langflow/probes/mysql` when it changes, otherwise
`internal/config`) and runner tests; since 11 November 2026 browser specs run in CI only for a release. It does
not run unrelated application unit/race suites or the full feature-browser matrix for
this profile. Mixed runner/application changes retain the application's core and relevant
feature coverage. Operator and developer tools that the server, the CI gates and
`make e2e` never execute are listed by exact path in `TOOLING_FILES`
(`scripts/create_database.sql`, `scripts/seed_e2e_data.sh`, the two `scripts/*_live_smoke.sh`
helpers and `gocrm-ui/cleanup-test-users.sh`). Like documentation, they add no browser specs
beyond the health smoke. The database script and the cleanup script have behaviour tests in
`make verify-hygiene`, which every profile includes; the seed and live-smoke helpers have none. A new
script is not on that list and stays unmapped. Release/deployment scripts, Makefile, unmapped security/shared code
and unknown paths still select full coverage. A deleted selected
spec or unavailable trusted base also forces full coverage. Renames include both paths.
The plan records changed paths, selected specs, policy version and fallback reasons; CI
artifacts include both commit SHAs and runner journals bind the plan to their candidate.
The root `.env.example` may accompany a bounded profile when a committed base/head proof
shows that only comments or blank lines changed. Both versions must be regular non-executable
files with simple unquoted assignments; active lines and their order must remain identical.
Quoted, escaped, expanded, multiline or control-character input, value changes, missing files
and path-only previews cannot grant this exception. It does not narrow another changed path
or an explicit full/release plan. Documentation review still checks the changed prose.
Maintainers should explicitly choose full validation for high-risk semantic changes even
when their paths are mapped; the selector does not infer security impact from issue prose.

The bounded `auth` profile covers `auth_service.go` and the files split from it
(`auth_sso_policy.go`, `auth_login.go`, `auth_tokens.go`, `auth_apikey.go`, `auth_refresh.go`,
`auth_logout.go`, `auth_password_change.go`, `auth_password_reset.go`), `auth_handler.go`,
`auth_registration.go`, auth test files in their service/handler packages and `tests/`,
and the auth-check script itself. Docs and runner changes may accompany them; any other
application path retains the full fallback unless it qualifies for the separate
`auth-persistence` profile below. This is a scope rule, not a claim that all auth changes are low risk.

For this profile, local validation runs `python3 scripts/ci/check-auth.py` (CI runs it only on `full`): API compilation,
vet for the selected Go packages, then uncached race tests selected from auth/session,
service logging-privacy, middleware, token-repository, password, HTTP integration and
`test/integration/erasure_test.go` files. The logging-privacy and erasure tests cover login
log redaction and API-key rejection for erased or deactivated owners; both exercise
the auth service directly (the login code in `auth_login.go`, the API-key validation in `auth_apikey.go`). Test roots are discovered from those files, so newly added auth regressions are
included. Every root must report an exact pass; missing, duplicate or skipped roots and
skipped descendants fail. The file patterns are explicit: a new test location must be added
to the check policy. Retained password-reset and rotation sources include SQL regressions,
so service-only auth changes must exercise them too: discovery of any MySQL root requires
`TEST_MYSQL_DSN` before compilation.
Local browser coverage is health smoke, login and registration on SQLite; CI builds the frontend
and runs the targeted backend and database gates, and runs browser specs only for a release (11 November 2026). Frozen issue checks remain
mandatory. Workflow changes also run runner tests. No full frontend unit suite or unrelated
feature-browser suite is added by this profile. An explicitly frozen database profile still
requires its MySQL/migration checks; explicit full and releases retain the complete matrix.

The `auth-persistence` profile extends those auth paths to the refresh-token repository,
its tests and generated `RefreshTokenRepository` mock. A change to the shared repository
interfaces file qualifies only when comparison of the actual base/head Git blobs proves
that all changes are inside the existing `RefreshTokenRepository` interface body. Comments,
literals and nested braces cannot hide another declaration. The candidate must also contain
all six `TestRefreshRotation{Atomicity,Concurrent,Validation}{SQLite,MySQL}` roots in
`internal/service/auth*_test.go`. They may land with the application fix; they are not yet
present on the policy's original main base. Missing roots retain full fallback, as missing
browser specs do, instead of selecting a profile that cannot run. Path-only manifest previews
cannot establish these exceptions. Changes to another declaration, imports,
schema, middleware, unrelated application code or dependencies retain full coverage.

Local validation runs hygiene, `python3 scripts/ci/check-auth.py --persistence`, transaction
boundaries via `python3 scripts/ci/check-go.py transaction --storage-only`, all frozen named
acceptance checks, configured migrations, and health/login/registration browsers on SQLite
and MySQL. The auth checker requires a disposable SQL server and all six exact rotation
atomicity, concurrency and validation roots across SQLite/MySQL; missing or skipped roots
and descendants fail. CI runs the changed packages' tests on MariaDB (the database gates job,
no `-race`, skips not rejected); the checkers and the migration gates on both SQL engines run in CI
only on `full`, and browser specs only for a release (11 November 2026).
Frontend build health remains required in CI; unrelated frontend units and entity browsers
are excluded. Explicit full validation and releases remain complete.

The shared `check-go.py` profiles (AEO, transaction, migration and mailer) isolate application
database connection variables from Go subprocesses,
while retaining `TEST_MYSQL_DSN` for disposable fixtures. This applies equally to local and
CI checks, including MariaDB; browser/server connection settings remain unchanged.

The bounded `mailer` profile covers only `internal/mailer/smtp.go` and package test files,
plus documentation and proven comment-only `.env.example` edits.
The mailer interface/factory, log transport, unmapped production helpers, service/handler
changes, schema/configuration changes and other mixed application changes retain full fallback. Workflow changes add runner checks.

`python3 scripts/ci/check-go.py mailer` runs API build, vet and uncached race checks for all
mailer tests, log-error sanitation and fourteen existing mail caller roots (password reset, forms, quote email and the automation
`send_email` step).
It requires `TestSMTPDeadlines`, `TestSMTPHeaders`, `TestSMTPTLSAuth` and
`TestSMTPFailureRedaction`, and every selected caller root, before starting Go commands.
Selection requires all four SMTP test declarations exactly once in committed regular
package test files; comments and literals cannot supply them. This is declaration presence,
not proof of build eligibility, execution or meaningful assertions; build and exact runtime
checks plus independent review remain mandatory. Missing
or duplicate roots retain full fallback; these roots will be supplied by the SMTP behavior
fix, not this policy prerequisite. The checker also rejects missing or skipped roots as
defense in depth; they do not silently reduce coverage. The source boundary has
no persistence change, so local browsers need only SQLite health smoke. SMTP behavior is
covered by protocol fixtures, and no login/form browser pretends to test a real SMTP relay.
CI keeps the frontend build and the targeted backend and database gates; the two-engine
migration gates and browser specs run in CI for a release (11 November 2026), and the MySQL 8 migration
leg for a pull request that changes migration code. Every frozen named acceptance
command still runs; explicit full/release coverage remains unchanged.

The bounded `aeo` profile covers AEO engine/provider files, its service, credentials,
repository, handler and their tests/mocks, plus `cmd/aeo_runtime.go` and AEO startup tests.
It excludes schema, shared configuration, middleware, frontend and dependency changes.
Docs and runner changes may accompany it. `cmd/main.go` requires a committed Go AST
comparison: imports, database initialization, route/middleware wiring and unrelated
constructors must remain unchanged. Only AEO construction/reconciliation/scheduling,
returning a drain handle through the existing router/dependency calls, and the shutdown
portion of `main` are masked. This proves scope, not semantic correctness. The shared
repository interfaces file qualifies only for changes inside `AEORepository`.

Selection requires `TestAEORuntimeStartup` and `TestAEORuntimeShutdown` in
`cmd/aeo*_test.go`, plus `TestAEORunLifecycle{SQLite,MySQL}` and
`TestAEOConcurrentShutdown{SQLite,MySQL}` in `internal/service/aeo*_test.go`.
These roots belong to the runtime implementation, not the policy rollout. Missing or
duplicate roots, deleted inputs or failed scope proofs retain full coverage. Path-only
previews cannot prove startup/interface exceptions or root availability; inspect the
committed selection before execution.

Local checks are hygiene, `python3 scripts/ci/check-go.py aeo`, every frozen named
acceptance check, configured migration checks, and health smoke plus `aeo-providers`
browsers on SQLite/MySQL. The checker builds the API, vets selected packages and runs
uncached race roots from all cmd/AEO engine tests, AEO service/repository/handler tests,
configuration tests, AEO model tests and transaction boundaries. A disposable
`TEST_MYSQL_DSN` is mandatory; missing, skipped or duplicate required roots and skipped
descendants fail. CI runs this checker only on `full`; for a pull request it keeps frontend build
health and runs the changed packages' tests on SQLite and MariaDB. The provider
browser covers runtime credentials but does not replace scheduled-start acceptance.
No unrelated entity browsers or full frontend unit suite is added. Explicit full/release
gates remain complete: explicit full local validation and full/release CI run the
transaction caller profile, which discovers all AEO service/repository roots, on configured
SQL engines (MySQL locally; MySQL and MariaDB in CI). Every discovered SQL root must pass,
including future lifecycle roots; this also works before those new roots land.

The bounded `transaction` profile covers only `internal/utils/transaction.go` and its
`transaction*_test.go` tests, with accompanying docs and runner changes. It runs hygiene,
`python3 scripts/ci/check-go.py transaction`, frozen acceptance checks, and health smoke on
SQLite and MySQL locally. The shared Go checker builds the API, vets selected packages and
runs uncached race tests discovered from transaction tests and their lead, form, deal,
company, AEO and bulk service/repository/integration callers. Missing boundary roots,
unavailable SQL, skipped or duplicate required roots and incomplete package output fail.
One existing empty `TestLeadConversionTransaction/concurrent_conversion_attempts` placeholder
is explicitly reported as a coverage gap; any other skip still fails. It does not exempt
transaction acceptance tests or claim lead-conversion concurrency coverage.
CI runs this Go profile only on `full` (SQLite/MySQL, and the transaction boundaries on MariaDB);
for a pull request it runs frontend build health and the changed packages' tests on SQLite and
MariaDB. The configured local
migration checks remain required. There are no full frontend unit or unrelated feature-browser
runs in this profile. Other application changes in the same diff retain full fallback;
explicit full and release runs retain the complete matrix and execute these transaction
boundaries on both SQL engines even though ordinary Go suites may skip without SQL. Every `full`
run in CI and explicit full local validation also run `python3 scripts/ci/check-auth.py
--persistence` against MySQL, so the auth and refresh-rotation MySQL roots, which
`make verify-backend` skips without `TEST_MYSQL_DSN`, cannot skip on a general change (#230). New transaction callers
or test locations require updating the maintained source map.

The smoke test checks `/health`, rejection of anonymous ticket access, admin login and
successful collection/page loading for leads, customers, tickets, tasks and users. It
creates no business records. It does not replace a ticket's behavior-specific regression.
Existing skipped browser scenarios remain coverage gaps; a passing run is not evidence
that those scenarios were exercised. The named acceptance parser rejects skipped roots.

CI normally runs the selector from the base commit; pushes to reviewed main use main's
committed selector. To roll out a reviewed policy correction without rerunning an obsolete
full-suite rule, a maintainer may set the repository variable `REVIEWED_VALIDATION_POLICY_SHA`
to the **independently reviewed full candidate SHA** before pushing it. This authorizes
only that exact head, never a branch, arbitrary PR text or a later commit. A mismatch uses
the base policy. For GitHub's synthetic PR merge checkout, the event's base and pinned
branch tip must match both merge parents; the selector still comes from the reviewed tip.
The retained plan records the policy commit and its source. Remove the
variable after the policy merges. Explicit full/release validation overrides this narrow
profile even when the policy pin matches. Ordinary PRs cannot authorize their own policy
through a tracked file. The four required job names remain active;
selection failure fails them rather than leaving required checks skipped or pending.
A manual CI run has no base commit: with the default `full: false` it tests every Go package without
`-race`, and with `full: true` it runs the release-grade backend, database and migration gates (never
the browser matrix). To inspect a local plan:

```bash
python3 automation/langflow/gophercrm_runner/validation.py \
  --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
python3 scripts/ci/check-changed-go.py --base origin/main --list   # the Go packages CI will test
make verify
make e2e E2E_DB_DRIVER=sqlite SPECS='e2e/tests/health-smoke.spec.ts e2e/tests/admin-tickets.spec.ts'
```

Use literal spec names from the plan. Omitting `SPECS` runs the full browser suite.
The base must be an ancestor of the candidate. CI validates the merged candidate; the
runner requires the candidate to contain current main before merge.

## API reference gate

`make verify-api-docs` keeps the committed API reference honest. It runs the route extractor's
Go tests, then `scripts/ci/check-api-docs.py`, which regenerates `api/swagger.json`,
`api/swagger.yaml` and `api/index.html` into a temporary directory through the same
`make api-docs` target `make swagger` uses and compares the bytes, and checks the spec itself:
every route registered in `cmd/main.go` and `internal/handler/*routes*.go` (read with go/ast by
`scripts/ci/apiroutes`) has an operation and the reverse, with `GET /health` the one documented
exclusion; an operation behind `middleware.Auth` offers exactly the two schemes as alternatives
(no anonymous `{}` alternative, no combined requirement) and a public one declares none; every
status the route middleware can answer with (401, 403, 429) is documented; the handler the router
registers for a route carries its `@Router` annotation, and the query parameters, form fields and
request body that handler reads (read from source by the same tool) are documented, the body as a
`$ref` to the bound type with the right `required` flag and a 400 response, and nothing it does
not read is;
every operation, parameter, response, property and tag is described and every operation has a
success and an error response; tags are declared once and used; `$ref`s resolve; path
parameters match their templates; `basePath` equals the `API_PREFIX` default; the two security
schemes name the `Authorization` header and show their `Authorization: Bearer <...>` /
`Authorization: ApiKey <...>` form; and the known closed sets carry exactly the values of their
Go constants. The checker's rule-by-rule mutation tests and the renderer's tests run in
`make verify-hygiene`.

The gate is part of `make verify` and is the second command of every bounded and
runner command set in `validation.py`. In CI the Backend job runs the make target only on `full`.
Every other run executes `python3 scripts/ci/check-api-docs.py` directly, on every change
(documentation-only included, after Go setup, without adding a job), and runs the extractor's Go
tests only when `scripts/ci/check-changed-go.py` selects `scripts/ci/apiroutes`: for a change to the
extractor, to `check-api-docs.py`, to `internal/handler/`, `internal/utils/`, `cmd/` or `api/`. Because the gate proves the three generated files
(`api/swagger.json`, `api/swagger.yaml`, `api/index.html`) equal a fresh `make swagger`, they
select no coverage of their own: the annotated Go sources, `cmd/main.go`, `scripts/docs/` and
the Makefile that produce them are classified as usual. A lead handler annotation change runs
the lead suites, not the full browser matrix. Any other path under `api/` selects the full profile
(reason `API contract`).

## Migration CI

[Database migrations](../.github/workflows/migrations.yml) is called by CI on a `full` run
(a release, or a manual dispatch of CI with `full: true`), where both engines run, and on a pull
request that changes migration code, where only `Migrations (MySQL 8)` runs (#376). It supports a
manual trigger for diagnosis, which runs both engines. A pull request without migration code, a
merge group and a main push do not call it (#365).

The `plan` job decides: `scripts/ci/plan-validation.py` writes `migrations=true` when a changed,
added or deleted path is under `migrations/` or `cmd/migrate/`, or is in `MIGRATION_WORKFLOW_FILES`
of the trusted selector (the migrations workflow, `scripts/release/check-sql-tests.py` and its two
test files). It also writes `true` when it has no change set or the trusted selector has no such
set, so the check runs when the question cannot be answered. CI passes `mysql8_only` to the
workflow, which picks its matrix from one of two fixed engine lists; the steps are the same for
every leg, including the no-skip check. `internal/models/**` and the Go test files of the migration
roots do not select the leg.

On a full run the
required Backend job fails unless both database jobs succeeded, so migration failures block that check
without requiring new branch-protection names. On a pull request the MySQL 8 leg is not a required
check and Backend does not read its result: branch protection does not wait for it, while the
remediation runner treats any failed check on the pull request as failed and does not merge. Outside a full run a change under `migrations/` or
`cmd/migrate` also runs the `cmd/migrate` tests through `scripts/ci/check-changed-go.py`, on MariaDB
10.11 in the database gates job, where a skip is not rejected. The reusable call checks out the same revision
as the rest of CI, including releases. Its two jobs use disposable MySQL 8
and MariaDB 10.11 services. They run only the migration, SQLite upgrade and repository SQL regressions:
fresh SQL up/down, a populated prior schema, AutoMigrate-first upgrades, website-column
rollback/reapply, the lead/company lookup-index migration (fresh, populated, AutoMigrate-first,
rollback/reapply and index parity with AutoMigrate), the SQLite query-plan check for those
indexes, the startup company-domain normalisation on SQLite and the service engine, the
pagination total-order roots on SQLite and the service engine, and the existing
populated/half-applied SQLite fixtures. This adds no
feature-browser run. Tests create uniquely named schemas in the service and temporary
SQLite files; never run these commands against an application database.

The shared `scripts/release/check-sql-tests.py --migrations-only` checker requires all
fifteen named roots and successful package completion. Missing, duplicate, failed or
skipped evidence fails, including skipped descendants under a passing parent. Thus a
test suite that skips because no SQL server is reachable cannot turn the migration job
green. The default release mode continues to require real SQL integration coverage and
also explicitly requires the populated website, lookup-index, products/quotes and automation
migration roots. The release
workflow needs no repository roots of its own: it calls the CI workflow, whose Database
migrations jobs run and check the fifteen roots on both engines for the release commit.

Changes confined to this workflow and the SQL evidence checker/tests select the bounded
`migration` profile. Local validation runs hygiene (including checker failure-path tests),
`python3 scripts/ci/check-go.py migration` against disposable MySQL plus SQLite upgrade fixtures,
all frozen acceptance checks and SQLite health smoke. Accompanying runner changes also run
runner checks. CI runs the module build and vet, the targeted Go tests and frontend build health;
in CI the MySQL 8 migration leg runs for a pull request that changes migration code and both engines on `full`. Schema, migration SQL, application code and unrelated release
machinery remain outside this profile and retain their full fallback. Explicit full/release
validation remains complete. This is shared profile data and checker code, not a per-ticket helper.

Each engine retains the tested Git revision, Go JSON log and a concise list of passing
required roots in `migration-results-<engine>-<run-id>` artifacts. The job summary shows
the same root list. These results supplement the contextual application checks; they do
not change the selected browser profile or replace semantic review of migration safety.

## Local Langflow rollout

> **Historical.** This section describes the Langflow runner's local validation. Ticket work
> no longer runs through the runner; the text is kept as a record of its design and rollout.

New example configurations use `"validation_mode": "contextual"`. Existing configurations
that omit the field use `"legacy"`: the original v0.3 behavior and policy hashes. Legacy
mode retains its original conditional local browser coverage. Explicit `"full"` always
requires the complete SQLite/MySQL browser and migration matrix, including UI-only and
documentation changes, and therefore requires disposable SQL configuration. Do not edit
an admitted execution's policy. Finish it, or explicitly stop and retain it when correcting
a policy; record the handoff and remaining budgets, then create a new configuration/state
directory for contextual completion with fresh evidence. The installed selector's content hash
is part of contextual and explicit-full validation policy, so upgrading it invalidates unfinished evidence.
Both ticket and existing-PR workflows use the same selection helper. Receipts still bind
commands, output, head, base and contract; review, CI and merge do not accept stale evidence.

A contextual full fallback requires disposable SQL configuration for shared application
changes. Runner-only work needs just local SQLite smoke. `doctor` checks browser
availability for contextual mode. Database setup and safety remain operator responsibilities;
never point E2E commands at an application database.

## Prepare, rehearse, publish

1. Finish and merge the intended PRs. In a clean branch, choose a stable SemVer version
   greater than the current one and run `python3 scripts/release/release.py prepare --version X.Y.Z`.
   This moves Unreleased notes into a dated section and updates both frontend version files
   and changelog comparison links, plus the API annotation and Swagger versions. Review the curated notes and merge that version PR normally.
2. Run **Release** on `main`, with the version and **publish=false**. An empty version rehearses
   the current Unreleased snapshot instead. The workflow runs complete core/browser/runner
   suites plus real MySQL/MariaDB migration and integration tests, then builds a Linux AMD64
   artifact. No tag, GitHub release or production deployment is created by a rehearsal.
3. Run **Release** again on `main`, with the prepared version and **publish=true**. This is a
   fresh full validation of that exact dispatch commit, not reuse of an old rehearsal. Only
   after all gates pass does it create an annotated `vX.Y.Z` tag at that SHA and a draft release.
   Curated changelog text is combined with GitHub's generated PR notes; an entry longer than
   `NOTES_LIMIT` in `scripts/release/release.py` is condensed to its headings and entry titles
   with a link to the full `CHANGELOG.md`, because GitHub caps a release body at 125,000
   characters. Assets are downloaded
   and verified before the release becomes public. Existing tags/releases are never overwritten.
4. The successful publication receipt triggers **Deploy**. It downloads the same artifact,
   verifies its checksums, archive metadata, tag SHA and successful Release run, and only
   deploys the latest stable release. The existing server backup, atomic activation, health
   check and rollback protocol remain in use. An unconfigured production environment skips
   deployment. Manual `deploy` requires a release tag; manual `rollback` and `status` remain.

Production bootstrap configures SSH and reports status; it no longer deploys an unversioned
main build. Publish a validated release after setup.

The artifact contains API, create-admin and migration binaries, frontend files configured
with relative `/api/v1`, migrations, license, changelog, revision and a release manifest.
`SHA256SUMS` covers the archive and manifest. The packaged production binary is extracted
and booted against a temporary SQLite database; health, revision and anonymous access are checked. Version, SHA and run ID link the release to
its validation. Checksums detect corruption; this MVP does not claim signed provenance.
The release token receives write permission only in the publication job. Release helpers
run with Python 3.12 or later; the runner also requires Python 3.12.

A failure after tagging or draft creation deliberately stops instead of guessing recovery.
Inspect the tag, draft and run. Do not move a published tag. Complete recovery only after
checking the exact tested SHA and asset hashes, or prepare a new version. A missing publication
receipt never deploys automatically; a validated published release can be deployed manually.
A failed release run is not accepted by manual deployment. If GitHub denies release creation
or a repository rule blocks tagging, fix the repository configuration and use a new reviewed
version as needed. Repository immutable-release settings are not changed by this implementation.

## Process and lessons

> **Historical.** This section describes the Langflow runner's local validation. Ticket work
> no longer runs through the runner; the text is kept as a record of its design and rollout.

The motivating #97 run spent about twenty minutes on each full local browser suite while
core verification took only a few minutes. Its full policy was already frozen, so it completed
under that policy. The workflow change was developed in a separate worktree. This avoids
retroactively calling less testing equivalent to already-promised testing.

During #135, the user correctly pointed out that treating every automation change as
shared application code defeated the lighter-workflow objective. The full run was stopped
and its partial evidence retained. Policy version 2 separates runner-only work from
application changes, applies the same profile locally and in CI, and retains full release
validation. The correction also replaces the unconditional local `make verify` command
for this profile with relevant runner/hygiene checks and health smoke. A policy pin is an
explicit maintainer rollout decision after independent review, not self-approval by a PR.
Local agent instructions must point to this policy rather than unconditionally requesting
`make verify`; otherwise those instructions can reintroduce the expensive run manually.

Portable frontend configuration also exposed an integration dependency: copied form embed
links assumed an absolute API base. Regression tests reproduced the failure with `/api/v1`;
public links now resolve against the CRM origin while preserving absolute API configurations.

The first independent #97 review found no code blocker but returned prose inside receipt-ID
fields. The runner rejected it. A fresh budgeted review with explicit format instructions
passed; the original failed invocation remains recorded. The reviewer prompt now spells out
exact identifiers and empty findings for passed criteria. No approval was synthesized.

The adversarial review also found a release DSN incompatible with migration test setup and
missing reverse dependencies in the path map. The DSN now uses a bare server prefix, checked
against the real MySQL driver. Mappings include the known consuming features and multi-entity
journeys; unknown/shared paths still go full. A path map is a maintained heuristic, not a
complete dependency analysis.

A final override check caught a distinction that compatibility alone could not cover:
legacy v0.3 behavior skipped local browsers for UI-only changes. A regression reproduced
that omission when selecting explicit full validation. The modes are now separate:
omitted/legacy preserves old evidence; explicit full always requires the complete matrix.
Both ticket and PR paths have regressions for this case.

A second review found an incompatible `gh api` flag combination in receipt lookup and two
remaining lead consumers. Receipt lookup now parses paginated JSON in Python and was checked
with the real read-only GitHub CLI call; the consumer regressions pin the expanded map.
Booting a native artifact also caught the smoke harness reading the health payload outside
its response envelope. That harness now passes against an actual production-mode API binary.
The final Linux artifact is exercised by Release rehearsal; no product release is implied
by the local fixture.

Selector tests cover mixed features, unknown/shared paths, renamed/deleted inputs and missing
trusted bases. Release tests cover version preparation, tag reuse, transport failures, archive
corruption/traversal, mismatched commits, draft failures and unvalidated deployment. Actual
workflow syntax, health smoke and repository verification supplement those isolated tests.

This correction follows the stopped #103 recovery and is the bounded `auth-persistence` profile.
That recovery replayed unrelated entity browsers under its old frozen policy. Passed checks and the interrupted browser attempt remain historical
evidence. Install the reviewed selector, retain the old journal and remaining budgets, then
use a new configuration/state for fresh focused completion; never rewrite an admitted policy
or relabel the interrupted check as passed. Record actual candidate selection before expensive
work. A manifest containing scope globs is a conservative preview, not the committed diff.

This profile corrects the #106 run. The profile is the bounded `transaction` profile; the run
selected all 159 SQLite browser cases merely because the transaction helper was shared. That run was stopped and retained as partial evidence.
The revised candidate needs fresh focused validation and independent review. An orphaned Vite
server also exposed a port-conflict classification gap: occupied browser ports now stop as
environment failures instead of spending a source-repair call. Stop only the identified stale
process, then resume; do not change application code or reuse an unknown server. Do not edit a
frozen execution's policy or reinterpret its interrupted checks: retain it, document the
handoff and remaining budgets, and use an explicitly new configuration/state for completion.

This refinement came from issue #100 and is the bounded `auth` profile: the previous map
treated even a bounded login-counter fix as a full application change. Deterministic boundary tests and real session HTTP coverage
provide direct evidence for that behavior without replaying unrelated entity journeys.
Use an explicitly new local configuration/state after installing the updated selector;
its content hash invalidates unfinished contextual execution policies.

Potential follow-ups, after measuring real contextual runs:

- Record p50/p95 duration by profile, changed feature and database; compare failures discovered
  in full rehearsals with earlier contextual results before expanding the map.
- Replace existing skipped browser scenarios with stable record-specific tests. A mapped suite
  containing skips is not complete feature coverage.
- Add artifact attestations/signing and an explicitly audited resume operation for partial
  publication. Current fail-closed recovery is safe but needs operator work.
- Consider periodic full runs to detect mapping omissions between releases. No new schedule is
  installed by this change.

References: [required check behavior](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks),
[reusable workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows),
[generated release notes](https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes),
and [draft-first immutable release guidance](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases).
