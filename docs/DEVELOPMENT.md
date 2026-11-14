# GopherCRM Development Guide

Reference for developers working in this repository: commands, layering, and the conventions the
codebase enforces. For first-time setup instructions see [SETUP.md](SETUP.md); for the
feature and test-coverage matrix see [FEATURES.md](FEATURES.md).

## Project Overview

GopherCRM is a CRM system built in Go with a React TypeScript frontend (`gocrm-ui/`). It manages
Users, Leads, Customers, Tickets, Tasks, and API Keys with role-based access control (admin, sales,
support, customer).

## Development Commands

### Backend

```bash
make build          # Build binary to bin/gophercrm
make run            # Run the app (go run ./cmd)
make test           # Run all tests (go test ./...)
make create-db      # Create MySQL database from scripts/create_database.sql
make deps           # Download and tidy Go modules
make clean          # Remove bin/
make build-tools    # Build CLI tools to bin/
make create-admin   # Run admin creation tool (requires build-tools first)

# Run a single test
go test -run TestFunctionName ./internal/service/

# Run tests for a specific package
go test ./internal/handler/
```

### Frontend (run from `gocrm-ui/`)

```bash
npm run dev            # Vite dev server (expects the backend on :8080)
npm run build          # tsc -b && vite build
npm run lint           # ESLint (lint:fix to auto-fix)
npm run format         # Prettier over src/
npm test               # Vitest unit tests
npm run test:coverage  # Vitest with coverage
npm run test:e2e       # Playwright E2E tests
npm run test:e2e:admin # Admin CRUD suites (uses playwright.config.slow.ts)
npm run test:e2e:report# Open the last Playwright HTML report

# Run a single E2E spec
npx playwright test e2e/tests/login.spec.ts
```

## Repository hooks

Activate the repository's guardrails once per clone:

```bash
git config core.hooksPath scripts/git-hooks
```

The pre-commit hook keeps the documentation tree deliberate: new folders under `docs/` are
refused unless added to the allowlist inside the hook, and local working papers
(`docs/specs/`, `docs/plans/` — gitignored) are refused even when force-added. It also runs an
optional content sweep driven by a machine-local term list when one is present.

## Architecture

Clean architecture with four layers, all behind interfaces for testability:

```
Handler (Gin HTTP) → Service (business logic) → Repository (GORM data access) → Models (domain entities)
```

- **Dependency injection** is manual, wired in `cmd/main.go` via `setupDependencies()`
- **All layers use interfaces** defined in `internal/repository/interfaces.go` and
  `internal/service/interfaces.go`
- **Repositories** accept `*gorm.DB` and implement `WithTx()` for transaction support
- **Handlers** parse requests, call services, and return unified responses via
  `utils.RespondSuccess`/`RespondError`
- **All API routes** are mounted under `/api/v1` (configurable via `API_PREFIX`)

**Frontend** (`gocrm-ui/`): React 19 + TypeScript + MUI + Vite. Axios client in `src/api/client.ts`
(base URL from `VITE_API_BASE_URL`, default `http://localhost:8080/api/v1`), per-entity endpoint
modules in `src/api/endpoints/`, TanStack Query for data fetching, react-hook-form + zod for forms,
pages organized per entity under `src/pages/`.

## Key Patterns

**Unified API response** — all endpoints return `utils.APIResponse{Success, Data, Error, Meta}`. Use
`utils.RespondSuccess()`, `utils.RespondError()`, `utils.RespondNotFound()`, etc.

**Error types** — sentinel errors live in `internal/errors/errors.go` (for example
`ErrDuplicateEmail`, `ErrNotFound`, `ErrLeadConverted`). Handlers classify errors with `errors.Is()`
— never string comparison. The error handler middleware maps error types to HTTP status codes.

**Transaction management** — use `utils.NewTransactionManager(models.DB).WithTransaction()` for
multi-step operations. Repositories expose `WithTx()` to participate in transactions. The manager
(`internal/utils/transaction.go`) guarantees the following, on SQLite and MySQL alike:

- **Root calls.** A context without a transaction begins one on the root handle, bound to the
  caller's context so a dead request never waits for a pooled connection. A nil return commits;
  an error rolls back and is returned as is (identity preserved).
- **Nested calls.** A context that already carries an active transaction reuses it under a
  `SAVEPOINT`; no second root connection is ever acquired, which matters because the SQLite pool
  holds exactly one. An inner error rolls back to the savepoint and the outer callback decides
  whether to continue; inner success stays provisional until the outermost call commits, and an
  outer error or panic discards every nested write. Nesting is unlimited in depth. The savepoint is
  created with the inner context but rolled back with the enclosing one, so cancelling an inner
  context cannot leave its writes in the outer transaction. A nested call on an already-cancelled
  context, or on a handle with GORM's `DisableNestedTransaction`, is refused before the callback
  runs (`ErrSavepointFailed`, `ErrNestedTransactionsDisabled`).
- **Panics.** A panic (or `runtime.Goexit`) unwinding through the callback rolls back the
  transaction or savepoint and keeps propagating untouched: the caller's `recover` sees the
  identical value, and the connection is back in the pool. The manager never swallows a panic.
- **Poisoned transactions.** A `ROLLBACK TO SAVEPOINT` that fails (for example because a MySQL
  deadlock already destroyed the transaction, or the connection broke) leaves the inner writes
  possibly in place, so the whole transaction is poisoned: the nested call returns
  `ErrTransactionPoisoned` (also matching `ErrRollbackFailed` and the driver error), every
  enclosing managed call returns it too even when its callback recovered the panic or swallowed
  the inner error and returned nil, further nested calls on that transaction are refused before
  their callback runs, and the root never attempts the commit. The manager issues no further
  statements on a poisoned transaction: enclosing levels skip their own savepoint rollback and
  the root performs the single `ROLLBACK` that discards everything. If that root rollback fails as
  well, both failures are joined. The outer callback's own returned error, if any, is preserved
  alongside; an error it swallowed is not resurrected. A poisoned attempt whose root rollback
  succeeded left no writes, so the retry helper may replay it when its error is still retryable.
  For a transaction begun outside the manager, poisoning binds only the managed calls below the
  first one; the owner receives the error from that call and must roll back itself.
- **Cancellation.** A context that is already done fails before the callback runs
  (`ErrBeginFailed` for a root or `ErrSavepointFailed` for a nested call, wrapping the context
  error). Cancellation during a root callback makes the commit fail (`ErrCommitFailed`, also
  matching `context.Canceled`/`DeadlineExceeded`) and the work is discarded. A nested call checks
  its context after the callback and rolls back to its savepoint even if the callback returned
  nil; the uncancelled outer transaction may still commit its own work after handling that error.
  A rollback that `database/sql` already performed for the cancelled root context is not
  reported as a failure. Likewise, a `ROLLBACK TO SAVEPOINT` refused only because the enclosing
  context has ended does not poison the transaction: that transaction can no longer commit
  (`database/sql` refuses `COMMIT` on a done context and discards the root transaction; an
  enclosing nested level fails its own context check and rolls back on its live parent handle),
  so the nested call returns the cancellation and the root rolls back on it without attempting
  the commit. An outer callback that swallows that cancellation and returns nil still gets
  `ErrCommitFailed` with the context error, and nothing is persisted.
- **Finalization failures.** `ErrBeginFailed`, `ErrSavepointFailed`, `ErrCommitFailed`,
  `ErrRollbackFailed` and `ErrTransactionPoisoned` tag what went wrong; a rollback failure is
  joined to the callback's error so both stay inspectable with `errors.Is`. A failed commit is
  ambiguous from the client's side and is never reported as success, and a failed rollback is
  never followed by a commit.
- **Retries.** `WithTransactionAndRetry` replays deadlocks, lock waits and SQLite busy/locked
  errors in a fresh root transaction, at most `maxRetries` times, and only when the failed attempt
  rolled back cleanly: an attempt that failed at commit or rollback is returned without replay,
  and the loop stops as soon as the context is done. A negative count returns
  `ErrInvalidRetryCount` without running the callback. Inside an active transaction the callback
  runs exactly once under a savepoint; a retryable failure is rolled back to that savepoint and
  returned tagged with `ErrNestedRetryDeferred` (still classified as retryable), because a MySQL
  deadlock has already destroyed the enclosing transaction and the only safe replay is the
  enclosing root attempt. An outer callback that swallows that error commits none of the inner
  writes.

Regression roots: `TestTransactionPanic{SQLite,MySQL}`, `TestTransactionNested{SQLite,MySQL}`,
`TestTransactionCancellationRetry{SQLite,MySQL}` (one-connection pools, persisted-row assertions,
every wait bounded by a context; the MySQL roots create and drop a uniquely named schema on the
`TEST_MYSQL_DSN` server), `TestTransactionFinalizationFailures` (a connection-pool wrapper that
injects begin/commit/rollback/savepoint failures deterministically, including poisoned commits
across several nesting levels) and `TestTransactionRecoveredNestedPanicCannotCommitFailedRollback`
(the independent reproduction of a recovered nested panic whose failed savepoint rollback used to
be committed).

**Request context (#107)** — lead conversion (including the handler's ownership read), lead
assignment (`LeadService.Assign`, #255), the public form submission and confirmation, and the
three routed bulk status updates run on the HTTP request's context: the handler passes
`c.Request.Context()` to the service, the service hands it to `WithTransaction` /
`WithTransactionAndRetry`, and the reads and writes it issues outside the managed callback are
bound to it as well — the lead and published-form reads that precede the
transaction and the spam row go through `txManager.DB(ctx)` (a handle bound to the context that
reuses the active transaction when the context carries one), and the bulk history row is written
with `BulkOperationRepository.Create(ctx, ...)`. The reCAPTCHA call uses the same context. A request
that is cancelled or times out therefore stops waiting for a pooled connection or a row lock,
rolls back and writes nothing: a confirmation token stays unspent, and a captcha check abandoned
by the request returns the context's error instead of a spam verdict. A bulk batch cancelled after
its history row exists records `failed` through `UpdateStatus` on `context.WithoutCancel(ctx)`
bounded by 5 s; if the connection or the row is still held elsewhere when that bound expires, the
write is abandoned, a warning is logged and the row stays `pending` until the sweep fails it (see
*Bulk endpoints*, #196). Mail and notifications still go out only after the commit and are not
tied to the request. The cancellation surfaces as the generic failure answer (500, or the
invalid-link page for a confirmation) to a client already gone, and is logged like any failure.

These entry points (`LeadService.ConvertToCustomer`, `FormService.SubmitPublic` and
`ConfirmSubmission`, `BulkStatusService.BulkSet*Status`, and `TicketService.CreateCommentWithCustomerEmail`,
#219, which also runs on the request context) are transaction roots. Called with a
context that already carries a transaction they return `utils.ErrTransactionRootRequired` before
reading or writing anything: they send mail or commit an audit row after their own commit, and
the conversion settings are read on the root handle, so running inside an enclosing transaction
would either announce work that transaction could still roll back or need a second connection
(which deadlocks SQLite's single-connection pool). No production caller nests them; the handlers
are their only callers. `LeadService.Assign` also runs on the request context but is not one of
these roots: it sends no mail and commits no audit row after its commit, so it does not refuse an
enclosing transaction; called inside one, it nests under a savepoint.

Not yet bound: the conversion settings (`ConfigurationService` takes no context), and every service
method not named above (`LeadService.Create` and `Update` among them), which still passes
`context.Background()`. AEO runs deliberately keep the service-owned lifecycle context and never
take a request's. Regression roots: `TestRequestCancellation{SQLite,MySQL}`
(one-connection pool; cancelled while the first read, the transaction or the spam write waits,
cancelled before the call, and the bulk outcome recorded `failed`, or left `pending` until a sweep
fails it, when its bounded write cannot get the connection), `TestRequestCancellationMySQLLockWait`
(queued behind another transaction's row lock, observed as the same statement still executing in
`information_schema.PROCESSLIST` on two consecutive polls, which works on MySQL 8 and MariaDB
10.11), `TestRequestCancellationCaptchaVerifier` (a stalled verification server),
`TestRequestRootRequired{SQLite,MySQL}`, `TestTransactionManagerDB{SQLite,MySQL}` and, in the
handler package, `TestConvertToCustomer_CancelledWhileTheOwnershipReadWaits`.

**Authentication** — JWT Bearer tokens and API Key header (`ApiKey`). API keys use HMAC-SHA256
hashing (with a legacy SHA256 fallback for migration). Middleware sets the user context. Use
`middleware.RequireRole()` for RBAC on routes. An API key whose owner has been erased or deactivated
is rejected at authentication time.

After an account lock expires, the next wrong password begins a fresh five-attempt window.
The exact expiry is eligible; an active lock still rejects correct credentials. See the
deterministic lockout tests and the real HTTP/SQLite session regression in
[authentication test cases](testing/01-authentication.md). Bounded auth changes use the
[targeted auth validation profile](VALIDATION_RELEASE.md), including race tests and
health/login/registration browsers. Releases retain complete validation.

Refresh-token persistence has a separate bounded profile with real SQLite/MySQL rotation
and transaction boundaries, migration checks and health/login/registration browsers. CI
also covers MariaDB. The shared repository interfaces file qualifies only when its Git diff
changes the refresh-token interface alone and the candidate contains all six rotation boundary
roots. See the [validation policy](VALIDATION_RELEASE.md)
for scope and migration of a stopped execution to the reviewed selector.

A password reset link is spent only by a committed reset. `ConfirmPasswordReset` validates the
complexity policy and hashes the new password before anything is written, so a weak password, or
one bcrypt refuses (over 72 bytes), leaves the link usable. It then runs one transaction through
the transaction manager and the `WithTx` repositories: the token is claimed by a conditional
update (`used_at IS NULL AND expires_at > now`, exactly one affected row, else the generic
rejection), the owner is re-read with a locking read (the transaction-bound `UserRepository`, `SELECT …
FOR UPDATE` on MySQL, a plain read on SQLite where the dialector drops the clause) and refused if
inactive or erased, the password is written with the lockout cleared, and every refresh token of
the account is revoked. The locking read matters on MySQL: a plain read inside the REPEATABLE READ
transaction answers from the snapshot taken at the token lookup, so an account deactivated or
erased by another connection just after that lookup would still look active and be reset. A failure of the password
update or of the revocation is returned and rolls the claim back, so the same link works on retry;
competing confirmations commit exactly once, and a token that expires or is spent between the
lookup and the claim is refused. The service refuses the confirmation rather than running the
writes separately when the transaction manager or a repository is not wired, which is why
`NewAuthServiceWithSessions` takes the manager explicitly; `NewAuthService` remains for stateless
JWT callers. Regression roots: `TestPasswordReset{Validation,Atomicity,Concurrent}{SQLite,MySQL}`
in `internal/service/auth_password_reset_regression_test.go` (one-connection in-memory SQLite,
a disposable MySQL schema created from `TEST_MYSQL_DSN`, every wait bounded, every assertion on
committed rows), plus the conditional-claim tests in
`internal/repository/password_reset_token_repository_test.go` and the two-connection
`TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL` in
`internal/service/auth_reset_owner_race_test.go`, which commits the deactivation from a second
connection after the token lookup. The MySQL roots skip without the
variable and fail when it points at an unreachable server; because the auth checker discovers
its roots from `auth*_test.go` and rejects skips, the bounded auth profile needs `TEST_MYSQL_DSN`
as well.

A refresh token is rotated by the same kind of unit of work (#103). `RefreshAccessToken` runs one
transaction through the transaction manager and the `WithTx` repositories: the presented token is
looked up, the owner is re-read with the same locking read as the reset flow (`FOR UPDATE` on
MySQL, a plain read on SQLite), the token is claimed by `RefreshTokenRepository.ClaimForRotation`,
a conditional update (`is_revoked = false AND expires_at > now`, exactly one affected row, else the
generic rejection), the successor row is inserted and its access JWT is signed. An owner lookup
that fails for a database reason aborts before the claim. A failure of the
successor insert or of the signing rolls the claim back with it, so the presented token stays
usable, no extra row exists and no credentials are returned; the same token succeeds once the
fault is gone. Two requests presenting one token therefore commit exactly one successor: the loser's
claim finds nothing to revoke and is rejected before it could insert anything, and a later replay
cannot invalidate the winner. The one rejection that commits is a dead owner: when the locking read
finds the account inactive, erased or missing, the claim stands as the containment revocation of
that token and the caller still receives the generic `ErrInvalidRefreshToken`. The locking read is
what makes a deactivation committed by another connection after the token lookup visible on MySQL;
a plain read in the REPEATABLE READ transaction would still show the account as active and issue it
a fresh session. `RevokeByTokenHash` stays unconditional and idempotent for logout and
invalidation; it is not the rotation claim. Like the reset, the rotation is refused rather than run
as separate writes when the transaction manager is not wired. Regression roots:
`TestRefreshRotation{Validation,Atomicity,Concurrent}{SQLite,MySQL}` in
`internal/service/auth_refresh_rotation_regression_test.go` (the same one-connection SQLite and
disposable MySQL schema as the reset roots; the MySQL validation root uses two connections for the
owner-deactivated-after-lookup case, the MySQL concurrent root one connection per competitor plus
one for verification; the insert fault is injected through the transaction-bound repository and
the signing fault through an instance-local seam of the service under test, never a global), plus
the claim tests in `internal/repository/refresh_token_repository_test.go` and the call-sequence
cases in `internal/service/auth_session_test.go`. `POST /auth/refresh` answers only
`ErrInvalidRefreshToken` with the generic 401; any other failure is a `503
SESSION_REFRESH_UNAVAILABLE`. A failure before the commit rolled the rotation back, so clients keep
the still-usable token and retry; a failed COMMIT is ambiguous (the rotation may have been applied),
in which case the retry answers 401 and the session ends.

**Lock order for session writes** — every transaction that writes both an account and its refresh
tokens locks the `users` row first and the `refresh_tokens` rows second: the reset (reset token,
owner, refresh tokens), the rotation (owner, then the claim), the password change (owner, then
the claim, the revocation and the successor) and the logout (owner, then the revocation, then the
`token_version` increment, #236). `refresh_tokens.user_id` references `users` with a
foreign key, so on MySQL/MariaDB every successor insert also takes a shared lock on the owner row;
a rotation that claimed its token before locking the owner deadlocked against a password change
(error 1213). New session flows must follow the same order.

**Password change** (#116) — `ChangePassword(userID, current, new, refreshToken)` verifies the
current password and hashes the new one before any transaction, then runs one transaction: locking
owner read (missing, erased or inactive → `ErrAccountUnavailable`, 401), the locked hash must still
be the one the password was verified against (otherwise a concurrent change won →
`ErrInvalidCurrentPassword`, 400), the presented refresh token must be a live token of the caller
and is claimed (otherwise `ErrSessionStale`, 409 `SESSION_STALE`, nothing written), the password is
written with `UpdateFields`, every refresh token of the user is revoked, and the successor is
inserted and its access token signed. Any failure before the commit rolls back everything; a failed
COMMIT (`utils.ErrCommitFailed`) is ambiguous — the server may have applied the change — and is
answered 500 without credentials, so the client cannot assume either outcome. Without a refresh token —
and always for API-key requests, which the auth middleware records under
`service.AuthMethodContextKey` — every token is revoked and nothing is issued. The password write
also increments `users.token_version`, so every access JWT issued before the change, the caller's
own included, is refused once it commits; the renewed access token is signed with the new value
(see *Access-token revocation* below). Roots:
`TestChangePassword{Validation,Renewal,Atomicity,ConcurrentRefresh}{SQLite,MySQL}` and
`TestRefreshRotationLocksOwnerBeforeClaimSQLite` in
`internal/service/auth_change_password_regression_test.go`; the MySQL concurrency root migrates a
disposable schema with the real `migrations/` (foreign key and indexes as in production, server
default REPEATABLE READ, checked portably on MySQL 8 and MariaDB 10.11) and forces both orderings
of a password change against a refresh of the presented token and of a sibling token. Each
ordering parks the first request inside its transaction, waits for the second to signal that it is
about to run its locking owner read, and releases the first only once
`information_schema.PROCESSLIST` (MySQL and MariaDB alike) shows that `FOR UPDATE` still executing
on two consecutive polls — i.e. blocked on the row lock. On SQLite the single pooled connection
serialises the two requests instead, so there is no lock wait to observe.

**Access-token revocation** (#236) — every access JWT carries the owner's `users.token_version`
(`bigint unsigned NOT NULL DEFAULT 0`, `json:"-"`) as the integer claim `token_version`, signed by
`GenerateJWT`. `ValidateToken`, which already reads the user row on every Bearer request, checks the
claim's shape before that read (absent reads 0; a value that is not a JSON number, not integral,
negative or above 2^53-1 is refused before any conversion to `uint`) and, after the unchanged
signature, expiry, missing-user and `is_active` checks, refuses a claim that differs from the stored
value with `service.ErrAccessTokenRevoked`; `middleware.Auth` answers it with the same 401 `Invalid
credentials` an expired token gets. The sentinel and the claim helpers live in
`internal/service/access_token_version.go`. Three operations increment the counter by exactly one
through `UpdateFields` under the users-row lock, inside the transaction they already run: the
password change and the reset confirmation in the same write as the password, and `Logout`, which is
now one transaction of its own (locking owner read, then the refresh-token revocation — every token
without a body, otherwise only the caller's own presented token — then the increment; a failure of
either rolls both back). The increment happens for Bearer and API-key callers alike, with or without
a body. **`Logout` refuses with an error, answered 500 by the handler, and writes nothing when the
transaction manager or the refresh-token repository is not wired** (a service built with
`NewAuthService` included); a caller the scoped locking read no longer finds (erased) gets nil and
nothing is written. Refresh rotation and login never change the counter: the rotation signs the
value its locking read returned, and the access token it replaces stays valid until it expires. A
role change does not increment it, API keys are not affected, and there is no per-token denylist.
The log lines of the rejection and of the three increments carry `user_id` and `operation` only.
MySQL and MariaDB get the column from `20261109120000_add_user_token_version` (added only when
missing), SQLite from `MigrateDatabase`; existing rows read 0, which is also what a token signed
before the claim existed reads, so an upgrade ends no session. Roots, in
`internal/service/auth_access_token_revocation_test.go` unless noted (the MySQL roots use a
disposable `TEST_MYSQL_DSN` schema and skip without it): `TestAccessTokenVersionClaim`,
`TestAccessTokenRevokedBy{Logout,PasswordChange,PasswordReset}{SQLite,MySQL}`,
`TestLogoutRequiresSessionStore`, `TestAccessTokenSurvivesRefreshRotation{SQLite,MySQL}`,
`TestLogoutLocksOwnerBeforeRefreshTokens{SQLite,MySQL}` (the MySQL root runs on the real migrations
and forces a logout against a rotation of the same user in both orderings, each wait seen in
`information_schema.PROCESSLIST` on two consecutive polls, with no error 1213),
`TestAccessTokenRevocationLogsOmitPersonalData`; `TestUserRepositoryTokenVersion{SQLite,MySQL}`
(`internal/repository`); `TestUserTokenVersionJSONHidden`, `TestUserTokenVersionSQLiteUpgrade`
(`internal/models`); `TestUserTokenVersionMigrationFiles`, `TestUserTokenVersionMigration`
(`cmd/migrate`); and, over HTTP, `TestAccessTokenRevocationAPISQLite`
(`tests/auth_access_token_revocation_test.go`). Registering the migration roots in the Database
migrations workflow is an operator follow-up.

**`PUT /users/me` refuses password fields** (#254) — `POST /auth/change-password` is the only
authenticated way to change a password. `UpdateMeRequest` declares `password`, `current_password`
and `new_password` as `json.RawMessage`, so a non-nil field means the key was present, whatever its
value (`null` included) and letter case. After a successful bind, any of the three is answered 400
`PASSWORD_FIELD_NOT_ACCEPTED` with one Warn line that omits the value, and the service is not
called, so profile fields beside it are not written either; other unknown keys stay ignored. A body
that fails binding keeps its `VALIDATION_ERROR` or `BAD_REQUEST`. `UserService.Update` has no
password branch and ignores a `password` key in its map, and `UpdateUserRequest` has no password
field, so `PUT /users/{id}` ignores the key too. Roots: `TestUpdateMeRefusesPasswordFields`,
`TestUpdateMeProfileFieldsStillApply`, `TestUpdateMeRefusalLogOmitsPassword`
(`internal/handler/user_me_password_test.go`), `TestUserServiceUpdateIgnoresPasswordKey` and, on
real middleware, repositories and SQLite, `tests/users_me_password_test.go`.

**User writes never upsert** — `UserRepository` has no full-struct update. GORM's `Save` falls back
to an INSERT … ON CONFLICT when its UPDATE matches no row, so a login that read an account before
an erasure wrote the erased name, email and hash back and cleared `deleted_at`, and any stale
struct could put an old password hash back. `UpdateFields(id, columns)` writes only the named
columns (a closed allowlist) with `WHERE id = ? AND deleted_at IS NULL` and reports
`gorm.ErrRecordNotFound` when no live row matched (MySQL reports changed rows, so a zero count is
re-checked). Login bookkeeping, `UpdateLastLogin`, the reset, the password change and
`UserService.Update` all go through it; a login whose account vanished underneath it is refused
(`TestLoginErasedDuringAuthentication{SQLite,MySQL}` in
`internal/service/auth_login_erasure_regression_test.go`). Re-verifying the password under the
owner lock when a login issues its session is tracked in #183.

**Customer writes never upsert (#256)** — `CustomerRepository.Update` is one `UPDATE … WHERE id = ?
AND deleted_at IS NULL` of named columns: the names, email, phone, company, website, position,
address, city, state, country, postal code, notes, `company_id`, `assigned_to_id` and `updated_at`
(the handle's `NowFunc`, copied to the struct). `id`, `created_at`, `user_id`, `deleted_at` and the
preloaded `User` and `CompanyRecord` are not written, and it never inserts. A zero count is
re-checked with a locking read, not a plain one: MySQL reports changed rows, so an identical write
affects zero rows and returns nil, while a plain read in a REPEATABLE READ transaction could still
show a row whose erasure has committed and turn the lost write into a false success. No live row
returns `gorm.ErrRecordNotFound`; `CustomerService.Update` and `Assign` log one Warn line with ids
only and return `apperrors.ErrNotFound`, which `PUT /customers/{id}` and
`POST /customers/{id}/assign` answer with 404 `NOT_FOUND`. An update that commits before an erasure
is scrubbed by it, whichever of the customer, converted-lead or customer-role-login deletes it came
through. Live customers stay last-write-wins on purpose: two edits, a stale `PUT` undoing an assign
or the reverse, and a stale write putting back a `company_id` that `UnlinkCustomers` cleared. None
of these restores anything on an erased row, and `customerService` still has no transaction. Roots
(`test/integration/erasure_atomicity_test.go`):
`TestCustomerUpdateLostToErasure{SQLite,MySQL}`, `TestCustomerAssignLostToErasure{SQLite,MySQL}`,
`TestCustomerWriteAfterErasure{SQLite,MySQL}`, `TestCustomerWriteConcurrentWithErasure{SQLite,MySQL}`
(the MySQL root confirms both blocked orderings in `information_schema.PROCESSLIST`),
`TestCustomerUpdateLiveRow{SQLite,MySQL}`, `TestCustomerUpdateDuplicateEmail{SQLite,MySQL}` and
`TestCustomerLostWriteLogsOmitPersonalData`; `TestCustomerHandlerUpdateMapsServiceErrors`
(`internal/handler/customer_update_errors_test.go`). The MySQL roots use a disposable schema on
`TEST_MYSQL_DSN`, skip without it and fail when it is unreachable. CI limit: those MySQL roots are
discovered by `scripts/ci/check-go.py transaction`, which CI runs only on the transaction, bounded
and full profiles (bounded and full also on MariaDB 10.11), while `make verify-backend` has no DSN
and skips them, so a `contextual` PR's CI does not execute them.

**Web client session** (#115, #184, `gocrm-ui/src/api/client.ts`) — the `ApiClient` owns the
stored session.

- **One record** (`src/api/sessionRecord.ts`). The session is one JSON record under
  `gophercrm_session`: `{ v: 1, id, access, refresh, subject, persist }`, in `localStorage` when
  "Remember me" was ticked and in `sessionStorage` otherwise. `startSession` removes everything
  from both storages, then writes the record with a fresh id (32 lowercase hex characters from
  `crypto.getRandomValues`); `subject` is the access token's `user_id` claim, read unverified only to
  tell sessions apart. A refresh or password change rewrites the whole record in one `setItem`,
  keeping the id, the subject and the storage. The record is read whole from one storage,
  sessionStorage first, so a session without a refresh token never takes one from the other
  storage. Every write also sets the legacy keys `gophercrm_token` and (or removes)
  `gophercrm_refresh_token` in the same storage, after the record. They are write-only mirrors for
  the browser specs: the client never reads a token from them while a record exists, but a record
  whose `gophercrm_token` mirror is missing is no session (that is how the specs sign out). Such a
  read removes nothing; only `clearTokens()` or an expiry whose snapshot matches the record does. A
  malformed record (not JSON, `v !== 1`, no string id or access token) is removed with its mirrors.
  When neither storage holds any record, a legacy two-key session is migrated once: the storage
  holding `gophercrm_token` (sessionStorage first) with its own refresh token or none, under the
  deterministic id `legacy-<sha256 of the refresh token, or of the access token>` so two tabs
  migrating at once agree (a synchronous SHA-256, because `crypto.subtle` is asynchronous and absent
  on plain HTTP). A storage holding only a refresh token is not migrated and the key is removed.
- **Snapshots.** Every operation — a request, a refresh, a password change, a logout — captures a
  snapshot when it is requested (before waiting for any lock): this tab's generation and the record's
  session id (with the account and refresh token read alongside). Before it sends, retries or
  writes anything, storage must still hold that session: same generation, same id. Another tab's
  refresh keeps the id, so the operation follows it; another tab's sign-in (new id) or sign-out (no
  record) stops it with `SessionChangedError`, and a late pair is kept privately, only for
  revocation. Sign-in, sign-out and an expired session start a new generation; a refresh or
  password change renews the current one. Requests are replayed (once, marked `_retry`) only under
  the session they were sent with.
- **The session lock** (`src/api/sessionLock.ts`). Every refresh and password change runs as a
  holder of one origin-wide exclusive lock: `navigator.locks.request('gophercrm-session', { mode:
  'exclusive', signal }, fn)`. Without `navigator.locks` (a non-secure context such as the bundled
  plain-HTTP nginx) `createSessionLock()` falls back to an in-tab promise-chain lock, and tabs are
  coordinated only by the record checks below. `ApiClientOptions.sessionLock` injects the lock;
  tests pass one `createMemorySessionLock()` (with a read-only `acquisitions` counter) to several
  clients. In one tab all 401s of a session share one refresh. Once it holds the lock, a refresh
  re-reads the record: if it holds another access token than the one the failed request carried,
  another refresh (in any tab) already renewed the session, and that token is adopted without
  calling `/auth/refresh`. The password change reads the record after acquiring the lock, so it
  presents the pair another tab rotated to; it renews an expired access token through the internal
  refresh inside the same lock hold (the lock is never taken twice) and rebuilds its body from the
  renewed pair. A 409 `SESSION_STALE` from the server still surfaces.
- **Failures.** The refresh deadline (15 s) counts from the moment the refresh is requested, the
  wait for the lock included: a refresh still queued at the deadline is withdrawn (its holder never
  runs, nothing is sent) and every waiter rejects with `RefreshTimeoutError`, even if the
  transport ignores the abort. A 400/401 from refresh ends the session (record removed,
  `onSessionExpired` once in that tab) only while the record still holds the refresh token that
  was presented: if the record holds another refresh token of the same session id (a tab whose
  lock was not shared spent it first), its access token is adopted; if it holds another session or
  none, nothing is cleared and the waiters reject with `SessionChangedError`. Another tab notices an
  ended session on its next request, whose refresh ends without calling `/auth/refresh`. A timeout,
  network failure or 5xx keeps the tokens.
- **Logout** (`endSession`) removes the record at once, without waiting for the lock, so another
  tab's refresh cannot delay it; that refresh then stores nothing and its waiters reject with
  `SessionChangedError`. Logout waits for the refreshes and password changes of this tab's ending
  generation still running. If one of them spent the stored refresh token, its successor pair —
  kept privately, never stored, and used only when it was obtained for the session id being ended —
  authenticates the revocation. Otherwise the captured access token is used, and if it has expired
  the client renews privately once, so the no-body revoke-all is still authenticated.

Other open tabs are not updated live on a sign-in or sign-out; they notice on their next request.
Tests drive the real interceptors through an injected adapter: `src/api/client.refresh.test.ts`
(single tab), `src/api/client.sessionRecord.test.ts` (the record, including a read injected between
the record write and the mirror write), `src/api/client.crossTab.test.ts` (two clients sharing
storage and one memory lock, in both response orders) and `src/api/sessionLock.test.ts`.

**CSRF is not shipped** — `internal/middleware/csrf.go` implements a `CSRF` middleware and a
`CSRFToken` endpoint, and the HMAC-SHA256 token codec in `internal/service/auth_service.go` is unit
tested, but `middleware.CSRF` is never installed in `cmd/main.go` and no route requires a token. It
is dead code today: treat it as an unfinished feature, not a protection you can rely on. There is no
live CSRF exposure in the current design either — authentication is Bearer/API-key only and the
frontend stores its token in `localStorage`/`sessionStorage` and sends it in the `Authorization`
header, so no credential is attached ambiently by the browser. That changes the moment any
cookie-borne session is introduced, and the middleware would then have to be wired up.

**Role assignment** — `POST /auth/register` is public and always creates a `customer`; it ignores
any client-supplied role. Elevated roles are only assignable via the admin-guarded `POST /users` or
the `create-admin` CLI. Never add a client-controlled role to a public endpoint. E2E admin accounts
are seeded by `gocrm-ui/e2e/global-setup.ts`, which shells out to `cmd/create-admin`.

**Account security** — account lockout after 5 failed login attempts (15 min), password complexity
validation (min 10 chars, upper + lower + digit + special), and a cookie `Secure` flag that defaults
to true in production.

**Sort validation** — all repository sort queries validate `sortBy` against per-entity column
allowlists via `utils.ValidateSort()` to prevent SQL injection.

**Quotes: conditional claims, one transaction each** — every write to a quote or its lines runs in
one transaction that starts with a locking read of the quote (`QuoteRepository.GetByIDForUpdate`,
`SELECT … FOR UPDATE` on MySQL, a plain read on SQLite where the dialector drops the clause) and
refuses anything but a draft with `apperrors.ErrQuoteLocked`; the totals are recomputed from the
lines inside that transaction and written by a conditional update `WHERE status = 'draft'`
(`UpdateTotals`, exactly one row, else the whole write rolls back). A status transition
(`QuoteService.ChangeStatus`) claims the row with `UPDATE … WHERE id = ? AND status = <from>`
(`ClaimStatus`; exactly one row, else `ErrInvalidQuoteTransition`, which is also the lost-race
answer), then — for `draft → sent` only — counts the lines with a locking read *after* the claim
(`CountLinesForUpdate`; zero rolls the claim back with `ErrQuoteHasNoLines`), and appends the
`quote_status_changes` row in the same transaction. The line count is read after the claim on
purpose: a plain count before it would answer from the REPEATABLE READ snapshot on MySQL and miss
a line deleted by a competing transaction. Two competing transitions therefore commit exactly one,
and a line write racing the send is either inside the sent quote's totals or refused as locked; no
committed sent quote has zero lines. The conditional updates re-check their own conditions when
the MySQL driver reports zero rows for an update that wrote the values the row already held.
Accepting a quote never touches its deal (owner decision). Regression roots:
`TestQuoteStatusTransitionMatrix`, `TestQuoteLineLimitsRejectedWithoutWrites`,
`TestQuoteCreateWritesQuoteAndHistoryTogether` (`internal/service/quote_service_test.go`),
`TestQuoteStatusConcurrent{SQLite,MySQL}` (`internal/service/quote_status_regression_test.go`; the
MySQL root creates a disposable schema on `TEST_MYSQL_DSN`, skips without it and fails when it is
unreachable) and `TestQuoteRepository_LockingReadsCarryForUpdateOnMySQLOnly`.

**Quote email: no transaction during SMTP, a conditional stamp after it** — `QuoteService.Email`
reads the latest `quote_status_changes` id *first*, then the quote and the customer's *current*
address, hands the plaintext rendering to the `Mailer` with no transaction held, and then stamps
`last_emailed_at` with one conditional update (`StampEmailed`): `WHERE id = ? AND status IN
('sent','accepted') AND NOT EXISTS (a history row of the quote with to_status = 'draft' and an id
above the marker)`. A `sent → draft` revision that commits after the marker was read — while the
mail is in flight, or between the marker read and the read of the quote, where the rendering that
went out would predate it — even one followed by a fresh send, which the status alone could not
tell apart, matches no row, and the call answers 409 `ErrQuoteChangedDuringSend` with
`last_emailed_at` unchanged. Keep the marker read before the quote read: the other order leaves a
window in which a stale rendering is recorded as the last email. A `Mailer` failure is 502
`MAIL_DELIVERY_FAILED` and stamps nothing. The development `LogMailer` (no `SMTP_HOST`) is not a
failure: its `Send` logs and returns nil, so the quote is stamped although nothing was delivered —
the same fallback the password reset shares (form mail records it `log_only`, see the README's
known limitations). The subject is `Quote Q-000042 from <name>`, the name resolved at send time by
`service.ResolveMailProductName` from the configuration source the service already holds for the
default currency (see *Mail product name*, #292; `GopherCRM` when that source is nil),
the recipient is never copied onto the quote, and the log lines of the call carry `quote_id` and
`customer_id` only — a transport error that quotes the address is returned, not logged. Roots:
`TestQuoteEmail{Delivery,LogsOmitPersonalData,RevisedDuringSend}` (`internal/service/quote_email_test.go`)
and `TestQuoteRepository_StampEmailedRefusesANewerRevisionToDraft`.

**The SMTP transport refuses CR/LF in headers** — `SMTPMailer.Send` and `SendPasswordReset` return
an error matching `mailer.ErrInvalidHeader` (through `errors.Is`) for a recipient or subject that
contains `\r` or `\n`, before any relay is dialled: either character would end the header line and
let the remainder inject further headers (an extra recipient, a forged subject). The check is
repeated inside the private `send`, so no future caller can bypass it. The generic `Send` failure
log carries only the transport error passed through `utils.LogError` — neither the subject nor
the recipient: an SMTP reply keeps its code and loses its text, because a relay that rejects a
recipient normally echoes the address in that text (`550 5.1.1 <who@example.com>: Recipient address
rejected`). The raw error is still returned for `errors.As`. The password-reset failure line logs
the same sanitised error and the redacted link, without the recipient; the development
`LogMailer` still records the recipient and the subject as before. Roots:
`TestSMTPMailerRejectsHeaderInjection` (`internal/mailer/header_injection_test.go`) and
`TestSMTPMailerSendLogOmitsTheRelayReply` (`internal/mailer/smtp_relay_reply_test.go`), whose stub
relay answers `RCPT TO` with a 550 that quotes the address.

**SMTP delivery is one bounded exchange (#113)** — `SMTPMailer` does not use `smtp.SendMail`,
which has no timeout. It dials within `mailer.DefaultDialTimeout` (10 s), sets one deadline of
`mailer.DefaultSendTimeout` (30 s, counted from the start of the dial) on the connection for the
whole conversation — greeting, EHLO, STARTTLS, AUTH, MAIL/RCPT/DATA, QUIT — and closes the raw
TCP connection on every path; a watchdog (`context.AfterFunc`) closes it at the deadline at the
latest, so cleanup such as the TLS close_notify (which sets its own five-second write deadline)
cannot extend the bound. A failure caused by the deadline matches `os.ErrDeadlineExceeded`.
The timeouts are package defaults, not configuration. **STARTTLS is opportunistic**: it is used
whenever the relay advertises it, verifying the certificate against `SMTP_HOST`, and a failed
handshake aborts the delivery, but with no `SMTP_USER` a relay that does not offer STARTTLS —
or an attacker on the path who strips the offer — receives the message unencrypted. There is no
require-TLS setting (see the roadmap); implicit TLS (port 465) is not supported. With
`SMTP_USER` set the relay must offer AUTH, and net/smtp's `PlainAuth` refuses to send the
credentials over an unencrypted connection unless the host is `localhost`/`127.0.0.1`/`::1`. A
failed QUIT after the relay accepted DATA is not a failure. The recipient and `SMTP_FROM` are
parsed with `net/mail.ParseAddress` — exactly one address, display name allowed — and the
addr-spec is then serialised for the envelope (`envelopeAddress`): a dot-atom local part as is,
any other local part as an RFC 5321 quoted string with `"` and `\` escaped
(`RCPT TO:<"a b"@example.com>`), because `net/mail` returns quoted local parts unquoted and
`"x> NOTIFY=NEVER"@example.com` would otherwise end `RCPT TO:<…>` early and inject ESMTP
parameters. Control characters, `<` and `>` anywhere in the address — refused even inside
quotes, where RFC 5321 would allow the brackets, because they are what lets text escape the
command's angle brackets — and domain literals are `mailer.ErrInvalidAddress`, without echoing
the value. Non-ASCII addresses (RFC 6531) are delivered only when the relay advertises SMTPUTF8
after EHLO (net/smtp's `Client.Mail` then adds the `SMTPUTF8` parameter itself) and are refused
with `ErrInvalidAddress` before `MAIL FROM` otherwise. A non-ASCII local part must be printable
letters, digits and marks; a domain label may hold ASCII letters, digits and `-` and any
non-ASCII character except controls (format controls such as bidi overrides included, ZWNJ/ZWJ
excepted) and whitespace, so every label the CRM's validator accepts — `info@col·legi.cat`
(U+00B7, RFC 5892) — goes out as `RCPT TO:<info@col·legi.cat>` with no IDNA conversion (#180;
root `TestSMTPInternationalisedDomains`, `internal/mailer/smtp_idn_test.go`). The headers carry
`mail.Address.String()`; CR/LF in the sender is `ErrInvalidHeader` like the recipient and
subject. A non-ASCII subject is Q-encoded into encoded words that never split a character and
folded so that every line holding encoded words, `Subject: ` included, is at most 76 characters
(RFC 2047 section 2). Only I/O failures caused by the deadline (a timeout, or the watchdog's
close) are reported as `os.ErrDeadlineExceeded`; a relay reply such as a 535 that decided the
outcome is returned as it is even when net/smtp's abort and QUIT afterwards stall. The body is sent as
`Content-Transfer-Encoding: quoted-printable` (7-bit on the wire, long lines soft-wrapped) after
its line endings (CRLF, bare LF, bare CR) are normalised to CRLF; dot-stuffing is left to the SMTP
data writer. The `Send` failure log carries neither the recipient nor the subject (automation
subjects are rendered from `{{lead.email}}` and similar placeholders), only the error through
`utils.LogError`, which also reduces a malformed relay reply (`textproto.ProtocolError`, which
carries the whole line) to `smtp_protocol_error`. Roots, against in-process relays on `127.0.0.1` with injected
sub-second timeouts (`internal/mailer/smtp_transport_test.go`): `TestSMTPDeadlines`,
`TestSMTPHeaders`, `TestSMTPTLSAuth`, `TestSMTPFailureRedaction`, and
`TestSMTPInternationalisedDomains` (`internal/mailer/smtp_idn_test.go`).

**Unpaginated reads must not mix a non-id `ORDER BY` with `FindInBatches`.** GORM's
`FindInBatches` continues each batch with `id > last id of the previous batch`, a cursor that is
only valid when the rows are ordered by id; under any other ordering it skips and repeats rows
across batches. The customer CSV export did exactly that and lost rows past the first 500 (#105).
`CustomerRepository.ListAllForExport` now reads every matching row with one ordered statement whose
order is total: the requested column, then `id`, both in the requested direction, or `id` ascending
without a sort column. That makes duplicate values, NULLs in the legacy nullable `company` column
and MySQL collation ties deterministic, makes `desc` the exact reverse of `asc`, and makes the read
one consistent snapshot on both engines. The result is still fetched in full before the handler
writes a byte. Regression roots: `TestCustomerExportTraversal{SQLite,MySQL}`
(`internal/repository/customer_export_traversal_test.go`) and `TestCustomerExportCSV{SQLite,MySQL}`
(`tests/customer_export_csv_test.go`); the MySQL roots create a disposable schema on
`TEST_MYSQL_DSN`, skip without it and fail when it is unreachable. If a whole-book read ever needs
to be chunked again, continue on a composite `(sort column, id)` keyset that handles NULL
explicitly, never on the primary key alone.
`LeadRepository.ListAllForExport` (`GET /leads/export`, #234) follows the same rule: one statement
over the live leads matching a `LeadExportFilter` (owner, search, exact classification, each
applied only when given), ordered by the validated column and then `leads.id` in the same
direction, or by `id` ascending; converted leads are included and erased ones are not.
`LeadService.ExportForActor` applies the list's scope: an admin gets the search with the sort,
else the classification in id order, else the sort, else id order; a sales user its own leads in
id order with every parameter ignored; any other role `apperrors.ErrForbidden` without a read.
Regression roots: `TestLeadExportTraversal{SQLite,MySQL}`
(`internal/repository/lead_export_traversal_test.go`) and `TestLeadExportCSV{SQLite,MySQL}` and
`TestLeadExportRoles{SQLite,MySQL}` (`tests/lead_export_csv_test.go`), under the same MySQL rules.

**Paginated reads walk a total order (#110).** OFFSET/LIMIT pages are separate statements, so
an `ORDER BY` that leaves rows tied lets the engine return the tied rows in a different order on
each page; MySQL's filesort with a LIMIT does exactly that, and consecutive pages repeated some
rows and never showed others. Every list and search over leads, customers, tasks (including the
label join), tickets, users and companies, and the company customer/lead sub-lists, therefore
ends with the table's id: `orderPage` (`internal/repository/page_order.go`) validates the column
through `utils.ValidateSort`, orders by it and then by id in the same direction, or by id
ascending when no sort is requested; the company list keeps its newest-first default with id
descending after `created_at`. Columns are emitted through `clause.OrderByColumn` qualified with
the table name, so a joined query cannot resolve them against the other table. New paginated
reads should use the same helpers. Regression roots: `TestPaginationDeterminism{SQLite,MySQL}`
(`internal/repository/pagination_determinism_test.go`), where every row shares `created_at` and
every sort column is tied, both directions, with filters, checking every page against the
expected sequence and the count. The Database migrations workflow runs both roots, the MySQL one
on MySQL 8 and MariaDB 10.11, and its checker rejects a skip.

**A task's deal link (#240).** `tasks.deal_id` is additive to the lead-or-customer rule and follows
`service.CanViewDeal` (`internal/service/deal_visibility.go`): admin any deal, sales its own, every
other role none. `checkDealLink` is the write side: a missing and a hidden deal both give
`ErrDealNotFound` with one message, and only a not-found lookup is mapped, so an outage answers 500.
`TaskService.UpdateWithLabels` takes `dealIDSent`: absent keeps the stored link unchecked, a sent id
is checked even when it equals the stored one, and 0 clears the link when the actor may see the stored
deal; a clear of a hidden link returns no error and keeps it. The create and update paths detach
`task.Deal` before every repository write, because GORM writes a preloaded belongs-to id back over
the foreign key, and re-attach it for the response. Every handler answering with a task goes through
`redactTaskLinks`, and `Task.MarshalJSON` fills the `deal` summary (id, title, stage) from the
preloaded deal only. Regression roots: `TestTaskDealLinkWriteRules`, `TestTaskDealLinkClear`,
`TestCanViewDeal`, `TestRedactTaskDeal`, `TestTaskDealLogsOmitDealTitle`,
`TestAutomationDealTaskLink{SQLite,MySQL}`, `TestAutomationDryRunReportsDealLink`
(`internal/service`); `TestTaskDealLinkRepository{SQLite,MySQL}`, `TestTaskDealLinkSurvivesErasure`
(`internal/repository`); `TestTaskDealReadRoutes{SQLite,MySQL}`, `TestDealTasksEndpoint{SQLite,MySQL}`,
`TestTaskDealLinkIntegration{SQLite,MySQL}` (`internal/integration`); `TestTaskDealHandlerErrorMapping`;
`TestTaskJSONDealSummary`, `TestTaskDealLinkSQLiteUpgradeFromV120` (`internal/models`);
`TestTaskDealLinkMigration`, `TestTaskDealLinkMigrationFiles` (`cmd/migrate`).

**The ticket assignee (#269).** `ticketService.Update` detaches the preloaded `AssignedTo` before
`ticketRepo.Update` whenever its id differs from `AssignedToID`, because `Save` writes a preloaded
belongs-to id back over the foreign key. It compares the attached user with the foreign key, not
with the validated change alone, since the handler's read and the service's read are separate.
After the write it attaches the user the foreign key names, for the response: the validated
assignee when it looked one up, otherwise the user of its own read, and none for a nil key. The
repository keeps `Save`, so a stale `PUT` stays last-write-wins. With automation events wired
(`WithTicketAutomationEvents`, #242, as `cmd/main.go` wires it) the update is one transaction that
locks the ticket row first and runs these checks against the locked row (see *Automation
runtime*); without the option there is no transaction, as before. Ticket assignees have no
is-active check; an existing user with the support or admin role qualifies.
Regression roots: `TestTicketServiceUpdateDetachesStaleAssignee` (`internal/service`);
`TestTicketReassignmentPersists{SQLite,MySQL}`, `TestTicketReassignmentResponse{SQLite,MySQL}`,
`TestTicketReassignmentBySupportAssignee{SQLite,MySQL}`, `TestTicketUpdateKeepsAssignee{SQLite,MySQL}`
and `TestTicketReassignmentRefusedWritesNothing{SQLite,MySQL}` (`internal/integration`). CI limit:
the MySQL roots skip in `make verify-backend`, which has no DSN, and `scripts/ci/check-go.py` does
not discover `internal/integration`, so only the runner's acceptance checks and the release
workflow run them.

**Ticket SLA fields (#278).** `tickets.due_at` and `tickets.first_responded_at` are nullable
timestamps; `overdue` is computed by `Ticket.IsOverdue(now)` and never stored (true
when `due_at` is strictly before now in UTC and the status is `open` or `in_progress`).
`Ticket.MarshalJSON` writes both timestamps in UTC with millisecond precision, or `null`, and
`overdue` from the wall clock, on every route that returns a ticket. The handler decodes `due_at`
(a `json.RawMessage`) before calling the service: absent keeps it on `PUT /tickets/{id}`, `null`
clears it, an RFC 3339 string with any offset is stored in UTC truncated to milliseconds, and any
other value is 400 `INVALID_DUE_AT` (`error.details.field` `due_at`) with nothing read or written.
Only `TicketRepository.CreateCommentWithFirstResponse` writes `first_responded_at`: it inserts the
comment and runs `UPDATE tickets SET first_responded_at = <created_at> WHERE id = ? AND
first_responded_at IS NULL AND deleted_at IS NULL` in one transaction (a savepoint through
`WithTx`). `ticketService.CreateComment` calls it for a non-internal comment whose author, as the
service reads it, is admin or support, and the plain `CreateComment` otherwise. `Create` and
`Update` omit the column, so a body or a ticket read before the stamp can neither set nor clear
it, and `Update` refills the struct with the stored value. MySQL and MariaDB get the columns from
`20261108120000_add_ticket_sla_fields` (`datetime(3) NULL`, each only when missing, no backfill);
SQLite from `MigrateDatabase`. Regression roots: `TestTicketOverdue`, `TestTicketJSONSLAFields`,
`TestTicketSLAFieldsSQLiteUpgrade` (`internal/models`); `TestTicketDueAtHandlerParsing`,
`TestTicketResponseShapeUnchanged` (`internal/handler`); `TestTicketCreateCommentFirstResponse`
(`internal/service`); `TestTicketFirstResponseRepository{SQLite,MySQL}` (`internal/repository`);
`TestTicketDueAtLifecycleSQLite`, `TestTicketDueAtInvalidWritesNothingSQLite`,
`TestTicketFirstResponseSQLite`, `TestTicketSLAFieldsOnEveryReadPathSQLite`
(`internal/integration`); `TestTicketSLAFieldsMigration`, `TestTicketSLAFieldsMigrationFiles`
(`cmd/migrate`). The MySQL roots skip without `TEST_MYSQL_DSN`; registering the migration roots in
the Database migrations workflow is an operator follow-up.

**Ticket list filters (#218).** `GET /tickets` takes optional `status` and `priority` query
parameters, each one exact, case-sensitive value of `models.TicketStatus` or
`models.TicketPriority`. The handler validates them with `IsValid` before any query, after the
customer 403: MySQL's default collation is case-insensitive and would match `Open` to `open`. An
unknown value is 400 `VALIDATION_ERROR` (`utils.RespondValidationError`, `error.details` keyed by
the parameter name, the value not echoed) and no service is called. `c.Query` takes the first
value of a repeated parameter, and an empty value counts as absent. Only when a filter is present
does the handler call `TicketService.ListFiltered(filter, offset, limit, sortBy, sortOrder)` with
a `repository.TicketListFilter{AssigneeID, Search, Status, Priority}`, forcing `AssigneeID` to the
caller's own `user_id` for support; without one the existing `List`, `ListSorted`, `Search` and
`ListAssigned` branches run with unchanged arguments. `TicketRepository.ListByFilter` (preloads,
`orderPage`, OFFSET/LIMIT) and `CountByFilter` share one predicate builder,
`whereTicketListFilter`, which ANDs the optional assignee (a non-nil 0 matches no ticket, as in
`TicketAssigneeScope`), status and priority with the parenthesised search OR, so the rows and the
total come from the same filter before offset and limit. The service logs ids, offsets, counts
and presence flags only, never the search term or the raw sort. No schema change or index.
Regression roots: `TestTicketEnumIsValid` (`internal/models`); `TestTicketListFilterHandler`
(`internal/handler`); `TestTicketServiceListFiltered` (`internal/service`);
`TestTicketListFilterRepository{SQLite,MySQL}` (`internal/repository`);
`TestTicketListFilters{SQLite,MySQL}` (`internal/integration`). The MySQL roots use a disposable
`TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable.

**Ticket comment email (#219).** `POST /tickets/{id}/comments` takes an optional boolean
`email_customer`. The handler's checks are unchanged (role, id, ticket lookup, assignment, bind,
blank body); after them, `email_customer` together with `is_internal` is 400 `VALIDATION_ERROR`
(`error.details.email_customer`) and nothing is written. Customer, sales and unassigned support
callers keep their 403 before the body is read. Without the flag the handler calls the unchanged
`TicketService.CreateComment` and the 201 has no `delivery` key; with it,
`TicketService.CreateCommentWithCustomerEmail(c.Request.Context(), comment)`, and the 201 carries
the comment with its `delivery`. A not-found error is 404; anything else, including
`service.ErrTicketCommentMailUnavailable`, is 500.

- **Enabled by an option.** `NewTicketService` keeps its three parameters and takes
  `service.WithTicketCommentMail(deliveryRepo, txManager, mailer)`; `cmd/main.go` passes the
  ticket mail delivery repository, the shared `txManager` and the shared `appMailer`. Without the
  repository or the manager the method returns `ErrTicketCommentMailUnavailable` and writes
  nothing; a nil mailer is recorded `skipped`/`no_mailer`.
- **A transaction root.** Called with a context that already carries a transaction it returns
  `utils.ErrTransactionRootRequired` before reading or writing. Independently of the handler, it
  returns `service.ErrTicketCommentEmailNotAllowed`, with nothing written and nothing sent, for an
  internal note or an author (read by the service) who is neither admin nor support.
- **One transaction.** The ticket and the author are read on the request context, then one
  `WithTransaction` inserts the comment with its `first_responded_at` stamp
  (`CreateCommentWithFirstResponse`, a savepoint) and a `pending` `ticket_mail_deliveries` row
  (`TicketMailDeliveryRepository.WithTx(tx).Create`). A failure rolls all three back and nothing
  is sent; a cancelled request writes nothing.
- **After the commit, with no transaction held.** A nil mailer is `skipped`/`no_mailer`, decided
  before the customer is read. Otherwise the ticket's customer is read live through
  `customerRepo.GetByID`: not found (erased, soft-deleted, missing) is `skipped`/
  `customer_unavailable`, a database failure `failed`/`lookup_failed`, a blank address after
  trimming `skipped`/`no_recipient`. Then `Mailer.Send` is called exactly once with the trimmed
  address, the subject `Ticket T-000042 from <name>` (`service.TicketCommentEmailSubject`,
  `models.TicketNumber`; the name comes from `service.ResolveMailProductName`, read only here,
  after the commit and once a send will happen, through `service.WithTicketProductName`, else
  `GopherCRM`, #292) and a plaintext body holding the ticket number, the title and this
  comment's text only. A send error is `failed` with `timeout` (`os.ErrDeadlineExceeded`),
  `invalid_recipient` (`mailer.ErrInvalidAddress`, `mailer.ErrInvalidHeader`) or `mail_failed`; a
  mailer that implements `mailer.LogOnlyReporter` and reports `LogsOnly()` (the `LogMailer`, no
  `SMTP_HOST`) is `log_only` with `sent_at` null; otherwise `sent` with `sent_at`.
- **The stamp.** `TicketMailDeliveryRepository.FinishPending` is `UPDATE … WHERE id = ? AND status
  = 'pending'` on `context.WithoutCancel(ctx)` bounded by 5 s; it matches nothing on a row that is
  already finished (`repository.ErrTicketMailDeliveryNotPending`), so a second stamp never
  overwrites the first. A stamp that fails is logged as a Warn line with ids only; the method still
  returns nil and the returned delivery shows the stored `pending`.
- **At most once, no retry.** The answer is 201 whatever the mail outcome. Nothing resends: a row
  left `pending` (the stamp failed, or the process stopped between the commit and the stamp)
  means the outcome is unknown. There is no retry route, background retry or stuck detection.
- **Reads.** `delivery` (`gorm:"-"`, `omitempty`) is set only on that 201. `GET /tickets/{id}`,
  `GET /tickets/{id}/comments` and every other read carry no `delivery` key, for staff and
  customers alike.
- **Purge.** `ticketRepository.Delete` runs one transaction (a savepoint under `WithTx`) that
  locks the ticket row, hard-deletes the ticket's delivery rows (`HasTable` probe), cancels the
  ticket's automation runs and purges its notifications (#242, see *Automation runtime*) and then
  soft-deletes the ticket; the
  comments stay as before. The customer plan's `AfterScrub` also runs
  `purgeCustomerTicketMailDeliveries`, which hard-deletes every row whose `customer_id` is the
  erased customer or whose `ticket_id` names one of its tickets, soft-deleted tickets included, for
  all three erasure roots (see *Deleting personal data*). Erasing the comment's staff author keeps
  the rows (that erasure redacts comments, it does not delete them).
- **Privacy.** A row holds ids, a status, an error code and timestamps: no address, subject, title
  or comment text. The log lines of the path carry operation names, `ticket_id`, `comment_id`,
  `delivery_id`, `status` and `error_code` only; the mailer's error, which may quote the address,
  is classified and never logged.
- **Schema.** `ticket_mail_deliveries` (see [datamodel.md](datamodel.md#ticket-mail-delivery)) comes
  from `models.MigrateDatabase` on SQLite and from
  `migrations/20261113120000_add_ticket_mail_deliveries` (created only when missing) on
  MySQL/MariaDB.

Regression roots: `TestTicketCommentEmailHandlerFlag`, `TestTicketCommentEmailHandlerErrorMapping`
(`internal/handler`); `TestTicketCommentEmailDelivery`, `TestTicketCommentEmailSkipReasons`,
`TestTicketCommentEmailFailures`, `TestTicketCommentEmailLogOnly`,
`TestTicketCommentEmailAtomicity{SQLite,MySQL}`, `TestTicketCommentEmailRefusedWhenUnwired`,
`TestTicketCommentEmailTransactionRootRequired`,
`TestTicketCommentEmailServiceRefusesInternalOrNonStaff`,
`TestTicketCommentEmailLogsOmitPersonalData`, `TestTicketCommentEmailRecordHoldsNoAddress`
(`internal/service`); `TestTicketMailDeliveryJSON`, `TestTicketMailDeliverySchemaSQLite`,
`TestTicketNumber` (`internal/models`); `TestTicketMailDeliveryRepository{SQLite,MySQL}`,
`TestTicketDeletePurgesMailDeliveries{SQLite,MySQL}`,
`TestTicketMailDeliveryErasure{SQLite,MySQL,WithoutTable}` (`internal/repository`);
`TestTicketMailDeliveriesMigrationFiles`, `TestTicketMailDeliveriesMigration` (`cmd/migrate`; the
newest-migration assertion has since moved to `TestAEORunOwnershipMigrationFiles`, #229); `TestTicketCommentEmailAPISQLite` (`tests`);
`TestTicketCommentMailProductionWiring` (`cmd`);
`TestTicketMailDeliveryErasureLeavesNoPersonalDataInAnyTable` (`test/integration`). The MySQL roots
use a disposable `TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable; CI limit:
`make verify-backend` has no DSN and skips them. Registering the migration roots in the Database
migrations workflow is an operator follow-up.

**Mail product name (#292).** The password reset mail (subject `Reset your <name> password` and the
first body line `A password reset was requested for your <name> account.`), the quote mail subject
and the ticket reply mail subject take the product name from one place:
`service.ResolveMailProductName(source)` (`internal/service/product_name.go`).

- **The rule.** It calls `source.GetString("general.company_name")` (`service.ConfigGeneralCompanyName`)
  on every call, with no cache, so a value changed through the configuration endpoints reaches
  the next mail without a restart. The value is used trimmed of surrounding whitespace when it is
  non-blank, valid UTF-8, free of control characters (CR and LF included) and at most
  `service.MailProductNameMaxRunes` (100) runes. Otherwise the name is `GopherCRM`
  (`service.DefaultMailProductName`): a nil source, a missing row (`apperrors.ErrNotFound`), any
  other read error, a blank value, invalid UTF-8, a control character or a longer value. A read
  failure never blocks a send.
- **Wiring.** `MailProductNameSource` is the one-method `GetString` interface;
  `ConfigurationService` satisfies it. `cmd/main.go` builds `configService` before `authService`
  and passes `service.WithAuthProductName(configService)` (a variadic option of
  `NewAuthServiceWithSessions`) and `service.WithTicketProductName(configService)`; the quote
  service reuses the configuration source it already takes for the default currency. A service
  built without the option, or with a nil source, names `GopherCRM`.
- **When it is read.** Only when a mail will go out, with no transaction held: the reset after the
  token is stored (an unknown or inactive address makes no read), the quote before `Mailer.Send`,
  the ticket reply after the commit and the customer read (a `no_mailer`, `customer_unavailable`
  or `no_recipient` delivery makes no read).
- **The mailer.** `Mailer.SendPasswordReset(to, resetURL, productName)` takes the name; the SMTP
  transport trims it and replaces a blank one, or one holding a control character, with
  `mailer.DefaultProductName` (`GopherCRM`), so CR/LF can never reach the Subject header. A
  non-ASCII name is Q-encoded like any subject. The `LogMailer` does not log the name.
- **Logs.** A fallback caused by a read error or an invalid value logs one Warn line with
  `config_key=general.company_name` and `reason` (`lookup_failed`, with the error through
  `utils.LogError`, or `invalid_value`); a nil source or a blank value logs nothing. No line of
  the resolver or of the three send paths carries the stored name, accepted or rejected.
- **No writes.** Nothing writes, seeds, re-seeds or migrates a configuration row; no key is added.
  The configuration write endpoints still accept any string, so a value the mail refuses (over
  100 runes, a control character) can still show in the web app while the mail says `GopherCRM`.
- **Not covered.** Form mail subjects and automation `send_email` subjects are written by users and
  unchanged; `SMTP_FROM` and the other mail bodies are unchanged.

Regression roots: `TestMailProductNameResolver`, `TestMailProductNameQuoteSubject`,
`TestMailProductNameTicketSubject`, `TestMailProductNamePasswordReset`,
`TestMailProductNameLogsOmitValue` (`internal/service/product_name_test.go`);
`TestPasswordResetProductName` (`internal/mailer`, an in-process relay on 127.0.0.1);
`TestMailProductNameAPISQLite` (`tests`, the configurations rows compared before and after the
sends, and after a further `InitializeDefaults`); `TestMailProductNameProductionWiring` (`cmd`,
`setupRouter` against an in-process relay). Registering these roots in `scripts/ci/check-go.py`
profiles is an operator follow-up.

**Lookup indexes are measured (#109).** `leads.email`, `leads.classification`,
`leads.external_id` and `companies.domain` carry non-unique indexes declared in the model tags and
in `migrations/20261029140000_add_lead_company_lookup_indexes`; the plans before and after are in
[datamodel.md](datamodel.md#lookup-indexes-and-query-plans). A predicate that wraps an indexed
column in a function (`LOWER(domain)`) hides it from the index on SQLite and on MySQL/MariaDB;
store the value normalised and compare it plainly instead. `TestLookupIndexesServeLookupsSQLite`
captures the statements the repository really runs and asserts their SQLite plans use the
indexes; `TestLookupIndexesMigration` (fresh up/down, populated upgrade, AutoMigrate first,
rollback and reapply, index parity) is one of the roots the Database migrations workflow requires.
Plain equality relies on every stored domain being lower case: the service normalises on write,
and `models.MigrateDatabase` lower-cases any other row on every start on every driver, which is how
SQLite installs, which never run the SQL migration, get it. It discovers the rows with a plain
non-locking `SELECT id` (binary comparison on MySQL/MariaDB) and updates only those ids, by primary
key in chunks of 500, so a normalised database takes no write and no row lock on startup; a single
predicate `UPDATE` would lock every scanned record under InnoDB REPEATABLE READ on every boot. Roots:
`TestCompanyDomainNormalization{SQLite,MySQL}` (`internal/repository/company_domain_normalization_test.go`).

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler → Auth →
RateLimit.

**Pagination** — every list endpoint parses `offset`/`limit` through `utils.ParseOffsetLimit`
(`internal/utils/response.go`). It defaults to `offset=0, limit=20`, caps `limit` at 100, ignores
non-positive and unparseable values, and **never returns `limit=0`** — callers divide by it when
building pagination metadata, and `?limit=0` used to panic that arithmetic and turn every list
endpoint into a 500. Do not hand-roll query parsing in a handler; call the helper.

**Lead owners (#255)** — `POST /leads`, `PUT /leads/{id}` and `POST /leads/{id}/assign` share one
check, `checkLeadOwner` in `internal/service/lead_service.go`: the owner must be a live, active admin
or sales user, otherwise `apperrors.ErrInvalidLeadOwner`, which the handlers answer with 400
`INVALID_REFERENCE`, `details.field` naming the body field and one message per route whatever the
reason, so a caller cannot probe which ids exist or what they hold. The reason (`unknown`, `inactive`,
`role`) goes to the log with the ids and nothing else. An erased user's row still satisfies the
foreign key, but no scoped read finds it, so it counts as unknown; a lookup that fails for a database
reason stays a 500. The read runs inside the writing transaction and takes a row lock (the user
repository bound to `tx.Clauses(clause.Locking{Strength: "UPDATE"})`): a plain read in MySQL's
REPEATABLE READ transaction would answer from the snapshot of an earlier plain read, such as the
company check, and miss a deactivation committed since. That makes the check current, not permanent:
a deactivation after it can still leave an inactive owner, because lead writes and user deactivation
share no lock protocol yet (#183). An update checks only an owner that differs from the stored one,
so a lead whose owner has left stays editable; an assignment always checks the user it names. The
assign route applies `service.CanViewLead` to the locked row before the owner check, so visibility
answers first and a sales user who hands a lead over loses it at once. A service built without a user
repository returns `ErrLeadOwnerCheckUnavailable` from these writes instead of skipping the check,
and `Assign` also refuses without a transaction manager. Public form submissions
(`form.DefaultOwnerID`) and the automation `assign_owner` step write `owner_id` under their own
rules. Regression roots: `TestLeadOwner{Unknown,Deactivated,Erased,WrongRole}Rejected{SQLite,MySQL}`,
`TestLeadOwnerValidAccepted{SQLite,MySQL}`, `TestLeadAssign{Permissions,SalesHandOff,
RevalidatesUnchangedOwner,ConvertedLead}SQLite`, `TestLeadUpdateUnchangedOwnerNotRevalidatedSQLite`,
`TestLeadOwnerRoleRulesPrecedeOwnerCheckSQLite`, `TestLeadOwnerLookupFailureIsServerErrorSQLite` and
`TestLeadOwnerRefusalLogsOmitPersonalData` (`test/integration/lead_owner_validation_test.go`);
`TestLeadOwnerCheckRefusedWhenUnwired`, `TestLeadOwnerSeesDeactivationCommittedAfterLeadLockMySQL` and
`TestLeadAssignIsAtomicSQLite` (`internal/service/lead_owner_validation_test.go`);
`TestLeadOwnerProductionWiring` (`cmd/lead_owner_wiring_test.go`). CI limit: the MySQL roots above are
discovered by `scripts/ci/check-go.py transaction`, which CI runs only on the transaction, bounded and
full profiles (bounded and full also on MariaDB 10.11), while `make verify-backend` has no DSN and skips them.

**Lead and company lock order (#182)** — lead conversion, lead update, bulk lead status and company
deletion share one lock order: **companies** (by primary key, `CompanyRepository.LockLive`:
`SELECT id … FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite, `gorm.ErrRecordNotFound` for a
missing or soft-deleted company; it takes no context and runs on the `WithTx(tx)` handle) →
**leads** (id ascending) → **customers** → **deals**.

- `CompanyService.Delete` keeps its plain pre-read (the 404 fast path), then in one transaction
  locks the company first, then runs `UnlinkLeads`, `UnlinkCustomers`, `UnlinkDeals` and the soft
  delete. A company deleted between the pre-read and the lock is `apperrors.ErrNotFound` (404) with
  nothing written. It stays on `context.Background()`.
- `LeadService.ConvertToCustomer` and `LeadService.Update` (with a transaction manager) run each
  attempt as: a plain read of the lead's `company_id` on the transaction; `LockLive` on the
  distinct non-nil ids among that value and the company the call names (the caller's
  `customer.company_id` for a conversion, the sent `company_id` for an update), ascending; the
  lead's locking read. When the locked lead names another company than the plain read (a delete
  unlinked it, or another write moved it, in between) the attempt rolls back with the unexported
  `errLeadCompanyMoved` and is restarted. A conversion links the lead's company only when that
  lock found it live, else converts without the link (the existing Warn line). An update checks a
  sent `company_id` against the lock: a company deleted while the update waited is
  `ErrCompanyNotFound` (400 `INVALID_REFERENCE`); a stored company found deleted is not an error
  (the restart sees the unlink). Without a transaction manager `Update` is unchanged.
- Restarts happen only for `errLeadCompanyMoved` on an attempt that rolled back cleanly (not
  `ErrRollbackFailed`, `ErrTransactionPoisoned`, `ErrCommitFailed` or `ErrBeginFailed`) on a
  context that is not done (`shouldRestartLeadCompanyAttempt`), at most 3 times (4 attempts); then
  the call returns `apperrors.ErrLeadCompanyContended` with no customer, lead change or automation
  event written, answered 409 `CONFLICT` by `POST /leads/{id}/convert` and `PUT /leads/{id}`. The
  conversion's deadlock retries (`WithTransactionAndRetry`, 3) still run inside each attempt.
- `BulkStatusService.BulkSetLeadStatus` takes no company lock (`SetLeadStatus` never writes
  `company_id`) and reads its leads with `BulkStatusRepository.GetLeadsByIDs`, now a locking read of
  the live leads ordered by id ascending (`FOR UPDATE` on MySQL); soft-deleted leads still read as
  missing.
- Result: once a conversion or lead update and a company delete have both committed, no live
  customer or lead names the soft-deleted company, in either order, on SQLite, MySQL 8 and
  MariaDB 10.11, and neither order ends in error 1213.
- The restart, exhaustion, missing-company and company-lock log lines carry `lead_id`,
  `company_id`, `operation`, attempt counts and `error_code` only.
- Not covered: lead create, customer create/update, deal writes and form intake still check
  `company_id` with a plain read and can race a delete (see the roadmap); existing links to deleted
  companies are not repaired.

Regression roots: `TestCompanyRepositoryLockLive{SQLite,MySQL}`,
`TestBulkStatusLeadsLockingRead{SQLite,MySQL}` (`internal/repository`);
`TestCompanyServiceDeleteLocksCompanyFirst`, `TestCompanyServiceDeleteRacedAfterPreReadSQLite`,
`TestLeadCompanyRestartDecision`, `TestLeadCompanyRestartBoundSQLite` (an instance-local seam moves
the lead's `company_id` inside each attempt), `TestLeadCompanyLockOrderOutcomesSQLite`,
`TestLeadCompanyLockLogsOmitPersonalData`, `TestLeadConversionVsCompanyDeleteMySQL`,
`TestLeadUpdateVsCompanyDeleteMySQL` and `TestBulkLeadStatusVsCompanyDeleteMySQL`
(`internal/service`; the MySQL roots park the first party through instance-local seams, see the
second party's wait in `information_schema.PROCESSLIST` on two consecutive polls, and count the
attempts with an instance-local counter); `TestLeadCompanyContendedHandlerMapping`
(`internal/handler`). The MySQL roots use a disposable `TEST_MYSQL_DSN` schema (migrated by
`AutoMigrate` without foreign keys), skip without it and fail when it is unreachable. CI limit:
`make verify-backend` has no DSN and skips them; registering the roots in
`scripts/ci/check-go.py` is an operator follow-up.

**Duplicate email** — repositories translate driver-level unique-constraint violations into the
sentinel `apperrors.ErrDuplicateEmail` via `isDuplicateKeyError` in
`internal/repository/duplicate_key.go`. Handlers classify it with `errors.Is` and return 409 for both
users and customers. The helper is only safe on tables whose sole unique index is `email`, which is
true of `users` and `customers`; adding a second unique index to either means the helper can no
longer attribute a hit to the email column.

**Bulk endpoints** — only `POST /leads|tickets|tasks/bulk/status` exist, registered by
`SetupBulkStatusRoutes` outside the entity groups. `BulkHandler` depends on `BulkStatusService`
(`internal/service/bulk_status_service.go`), which holds nothing but the three status operations.
Each one normalises the ids (1–100, duplicates count once), checks the role, writes a pending
`bulk_operations` history row, then inside ONE transaction reads every listed record through
`BulkStatusRepository`, refuses the whole batch if any id is missing, not the caller's to change
or in a forbidden transition (the offending ids go into `error.details`), and only then writes
the status — plus, for leads and tickets (#242), the automation events on the same transaction;
the ticket read is a locking read in id order — and, as the last
write of that transaction, sets the history row `completed` (see below). There is no generic bulk
create/update/delete/action machinery any more (#112); a new
bulk operation gets its own narrow service method and repository halves the same way, and a bulk
erasure would need per-item isolation designed afresh (each erasure is one savepoint-bracketed
unit, `runErasureUnit`, but a batch would also have to report per-item failures and authorize
every item's whole graph).

**Every bulk history row reaches a terminal status (#196).** `completed` is written inside the
batch transaction by `BulkOperationRepository.WithTx(tx).CompletePending(ctx, id)`, a conditional
`UPDATE … WHERE id = ? AND status = 'pending'` by primary key. It commits with the records or not
at all, so a row that is still `pending` belongs to a batch that did not commit. When it matches
no row it returns `apperrors.ErrBulkOperationNotPending` and the whole batch rolls back. That
sentinel is a plain `errors.New` that wraps none of `ErrNotFound`, `ErrForbidden` or
`gorm.ErrRecordNotFound` and is not an `AppError`, so `respondBulkStatusError` answers 500 with no
handler change: a batch whose own row was swept before it committed answers 500 and changes
nothing. After a batch that rolled back, `recordBulkStatusOutcome` writes only `failed`, through
`UpdateStatus` on `context.WithoutCancel(ctx)` bounded by 5 s. `UpdateStatus` is conditional on
`status = 'pending'` too, so a late write never overwrites a `completed` or `failed` row; its
`ErrBulkOperationNotPending` is ignored without a warning, and any other failure is only logged
(`operation_id` and the error through `utils.LogError`) and never changes the answer.

A row whose `failed` write timed out stays `pending` until `service.BulkOperationRecoveryRuntime`
(`internal/service/bulk_operation_recovery_runtime.go`, wired by `cmd/bulk_recovery_runtime.go`)
sweeps it. The sweep runs at boot and every `BulkOperationSweepInterval` (15 minutes) on every
replica, with the cutoff `now - BulkOperationStaleAfter` (1 hour); the horizon, the interval and
the chunk size (`repository.BulkOperationSweepChunk`, 500) are package constants, not settings.
`MarkStalePendingFailed(ctx, cutoff)` works in two steps: a plain, non-locking `SELECT id … WHERE
status = 'pending' AND created_at < cutoff AND deleted_at IS NULL ORDER BY id`, read in chunks of
at most 500 by keyset, then per chunk one `UPDATE` by primary key (`id IN (…)` plus the same
predicate) that sets `failed` and `updated_at`. It returns the summed count; extra sweeps on other
replicas only compete for the same rows, each changing once. The sweep must never take a lock
through `idx_bulk_operations_status` (no locking read, no `UPDATE` whose predicate is the status
index): an index-driven `UPDATE` locks the secondary index before the primary key, the reverse of
`CompletePending`'s order, and deadlocks (MySQL/MariaDB error 1213) against live batches. The
index (non-unique, `bulk_operations(status)`) comes from `models.MigrateDatabase` on SQLite and
from `migrations/20261110120000_add_bulk_operation_status_index` on MySQL/MariaDB. `failed`
therefore means none of the batch's records changed, with one exception: rows already `pending`
when this was deployed recorded `completed` after their commit under the old code, so their batch
may have committed; they are swept to `failed` all the same (outcome unknown, documented, not
stored). Sweep log lines carry only the `failed` count and, on failure, the error through
`utils.LogError`. `Stop` cancels the sweep and joins its goroutine under main's shared deadline.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestBulkOutcomeRecovery{SQLite,MySQL}` (a failed completion rolls the batch back; a rolled-back
batch whose `failed` write times out is `pending`, then `failed` after one sweep past the horizon;
a row swept before its batch completed; late writes), `TestBulkOperationRecoveryRuntimeLifecycle`,
`TestBulkOperationRecoveryLogsOmitPersonalData` and the updated
`TestRequestCancellation{SQLite,MySQL}` and `TestBulkStatusService{SQLite,MySQL}`
(`internal/service`); `TestBulkOperationRepositoryOutcome{SQLite,MySQL}` (1,201 stale rows in three
chunks, captured statements, an `EXPLAIN` on MySQL) and `TestBulkOperationSweepConcurrent{SQLite,MySQL}`
(sequential on SQLite; on MySQL concurrent sweeps, a sweep racing a batch, and live batches against
looping sweeps with no error 1213) (`internal/repository`); `TestBulkOperationStatusIndexSQLite`
(`internal/models`); `TestBulkOperationRecoveryProductionWiring` (`cmd`, the real wiring over a
SQLite file); `TestBulkOperationStatusIndexMigrationFiles` and `TestBulkOperationStatusIndexMigration`
(`cmd/migrate`). CI limit: the MySQL roots in `internal/service` and `internal/repository`
`bulk*_test.go` are discovered by `scripts/ci/check-go.py transaction`, which CI runs only on the
transaction, bounded and full profiles (bounded and full also on MariaDB 10.11), while
`make verify-backend` has no DSN and skips them. Registering the migration roots in the Database
migrations workflow is an operator follow-up.

**Rate limiting** — all three tiers are wired, in `cmd/main.go` and in `SetupFormPublicRoutes`:

| Tier | Limit | Where it is applied |
|------|-------|---------------------|
| `RateLimitStrict()` | 10 req/min, burst 5 | the **public** `/auth` group (`register`, `login`, `refresh`, `password-reset`, `password-reset/confirm`) — the `authRoutes` group in `setupDependencies` — plus the public form `submit` and `confirm` routes via `SetupFormPublicRoutes` |
| `RateLimitModerate()` | 120 req/min, burst 30 | every authenticated route, reads and writes alike — applied once to the `protected` group in `setupDependencies`, which is where `/auth/logout` and `/auth/change-password` live too (the `protectedAuth` subgroup) |
| `RateLimitGenerous()` | 240 req/min, burst 40 | the three public form GETs — definition, `embed.js` and the hosted view — via `SetupFormPublicRoutes` |

All three tiers are live, but the split is public-forms versus everything else, **not** reads versus
writes. On authenticated traffic there is no separate tier for reads: the generous tier is reachable
only through the unauthenticated `/forms/public` GETs, so any doc or comment implying that an
authenticated read gets a more generous budget is wrong. The inline comment beside the moderate tier
in `cmd/main.go` now matches the code — it read "60 req/min" until 2026-09-18, when it was corrected
to 120 req/min, burst 30. OPTIONS preflight requests are excluded. Limiting is keyed on
`c.ClientIP()`, which is why `TRUSTED_PROXIES` must be set correctly — otherwise a spoofed
`X-Forwarded-For` defeats the limiter. With no trusted proxies every client behind one proxy
shares the proxy's bucket; with a trusted proxy the rightmost untrusted forwarded address,
IPv4 or IPv6, is the identity.

Visitor state is bounded and owns no goroutine:

- **Per-instance capacity.** Every `RateLimit(...)` call — and therefore every tier and every
  route group that constructs one — has its own token buckets and tracks at most 10,000 client
  IPs. State is held in process memory, so limits are neither shared between replicas nor kept
  across restarts.
- **Conservative overflow.** When an instance is full, a previously unseen IP gets the ordinary
  429 envelope. Existing clients are never evicted to make room and keep spending their own
  remaining tokens. A flood of distinct IPs can therefore lock out new clients of that tier until
  records expire; it cannot buy anyone a fresh burst.
- **Idle expiry.** A record is reclaimed only after five minutes without a request (throttled
  requests count as activity) *and* once its bucket has refilled to the full burst. Dropping it
  is then indistinguishable from keeping it, so expiry never grants tokens early, even for a
  custom low-rate limiter whose bucket takes hours to refill.
- **Cleanup lifecycle.** Expiry is opportunistic: each request reclaims at most four eligible
  records, chosen from a heap ordered by their earliest safe expiry time, so neither ordinary nor
  rejected requests scan the table. There is no ticker or background goroutine, so constructing
  and discarding routers (as tests do) leaks nothing and needs no shutdown.

The previously exported config-driven `RateLimiter` (with `RateLimitType`, `PublicRateLimit` and
`SmartRateLimit`) was never wired to a route and has been removed, together with its tests; the
relevant assertions (per-IP keys, spoofed-header resistance, 429 envelope) now exercise the
production `RateLimit`. The unused `config.RateLimitConfig` struct remains and has never
configured the live tiers. There are no runtime knobs for capacity or idle expiry.

`internal/middleware/rate_limit_test.go` covers capacity and repeated overflow
(`TestRateLimitCapacity`), clock-driven refill and safe expiry (`TestRateLimitExpiry`), goroutine-free
construction and disposal (`TestRateLimitLifecycle`), concurrent arrivals and cleanup under `-race`
(`TestRateLimitConcurrency`), the 429/abort/OPTIONS contract (`TestRateLimitHTTPBehavior`),
untrusted and trusted proxy identities including IPv6 (`TestRateLimitTrustedProxy`) and per-instance
state (`TestRateLimitTierIsolation`). Expiry tests use an injected clock rather than sleeping.

`DISABLE_RATE_LIMIT=true` bypasses **only the Strict tier**. The check lives inside
`middleware.RateLimitStrict`, which returns a pass-through handler when the variable is set, so the
moderate tier on authenticated traffic stays active regardless — including on `/auth/logout` and
`/auth/change-password`, which are authenticated and therefore never saw the strict tier to begin
with. That remains the behavior of ordinary builds.

`make e2e` builds a separate disposable binary with the `gophercrm_e2e` build tag and sets
`GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true`. Together these opt functional browser tests out of all
three live tiers, avoiding cross-test token-bucket state and dashboard 429s. Ordinary binaries
ignore this switch, even if it is set. The tagged binary also keeps every tier enabled unless the
switch is exactly `true`. Do not deploy the tagged test binary. `make verify-backend` tests live-tier
burst enforcement, the legacy strict-only switch, ordinary-build isolation, and the tagged opt-out;
no browser assertion or retry setting is relaxed.

## Testing

| Layer | Location | Runner |
|-------|----------|--------|
| Backend unit | colocated `*_test.go` | `go test ./...` — testify suites, mocks from `internal/mocks/` |
| Backend integration | `test/integration/`, `tests/` | `go test ./...` — in-memory SQLite |
| Frontend unit | `gocrm-ui/src/**/*.test.tsx` | `npm test` — Vitest + React Testing Library |
| E2E | `gocrm-ui/e2e/tests/` | `npm run test:e2e` — Playwright, Chromium |

```bash
go test ./...                       # everything
go test -race ./...                 # race detector — must stay clean
go test -cover ./...                # per-package statement coverage
go test -run TestFunctionName ./internal/service/
```

Use current command output for test counts and coverage; historical counts are not release gates.
`make verify` runs hygiene checks, Go build/vet/race tests, and frontend build/lint/unit tests.
The bounded transaction profile runs API build/vet, transaction and affected-caller race
regressions, named acceptance checks, configured migration checks and health smoke on SQLite
and MySQL. CI adds MariaDB transaction checks and health smoke. It avoids unrelated feature
browsers and frontend unit tests; see the same policy for its exact scope and known gap.
The bounded auth profile replaces the full core checks with targeted auth/session race tests,
API/frontend build checks, and health/login/registration browsers; it does not run frontend
lint or unit tests. Other application profiles include ESLint as a required, passing gate.
Since 11 November 2026 CI runs no browser spec for a pull request, a merge-queue group or a push to
`main`: the complete browser matrix on MySQL, MariaDB and SQLite runs only when the release workflow
calls CI with `full: true`. Pull-request CI runs hygiene, backend, frontend and the database gates job
(the profile-specific MariaDB regressions). Browser coverage before publication comes from the
remediation runner's local validation (the changed feature's specs on SQLite), which is a separate
scope. Runner-only changes use hygiene and runner/probe tests and API/frontend build checks rather
than unrelated application suites. Local `make e2e` still defaults to the full suite. See [validation and releases](VALIDATION_RELEASE.md)
for selection, full-suite overrides, version preparation, rehearsals and publication.
Production deployment follows a published full-validation release, rather than every main push.

The separate [Database migrations workflow](../.github/workflows/migrations.yml) runs focused
fresh/upgrade/rollback checks on disposable MySQL 8 and MariaDB 10.11 services for every PR,
merge group and main push. Both jobs retain the existing SQLite upgrade fixtures and reject
missing or skipped required coverage. Their artifacts include the tested revision and Go JSON
results, and the required Backend check depends on both jobs. Changes confined to this workflow
and its checker use targeted migration regressions and health smoke; see [migration CI](VALIDATION_RELEASE.md#migration-ci) for the exact coverage.

The same runner also hosts the feature analysis workflow (`gophercrm-runner analyze`, see the
[runner guide](../automation/langflow/README.md#analyse-a-feature-request)), which writes a
gitignored `docs/specs/` document and never changes tracked files.

Runner changes require `make verify-runner` and the focused checks selected by the policy.
Install `automation/langflow[flows]`
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
`cmd/create-admin`. See `gocrm-ui/e2e/README.md` and [ADMIN_TESTING.md](ADMIN_TESTING.md).

## Gotchas

Traps that have already cost time here. Each is stated as the wrong move and the correct one.

**Do not add a composite `UNIQUE(email, deleted_at)` index to allow email reuse after soft delete.**
It looks like the obvious fix and it silently removes the constraint you care about. Both MySQL and
SQLite treat `NULL`s in a unique index as distinct, so every live row (`deleted_at IS NULL`) compares
unequal to every other live row and unlimited duplicate *live* emails become insertable. This was
verified empirically against both engines, not assumed. Email reuse is instead handled by erasure
overwriting the address on delete (see *Deleting personal data* below), so the original value no
longer exists in the table and can be registered again.

**Do not rely on `gorm.ErrDuplicatedKey`.** GORM only produces it when the connection is opened with
`gorm.Config{TranslateError: true}`, and no `gorm.Open` call in this project sets it — confirm with
`command grep -rn TranslateError`. Detection therefore falls through to driver-specific checks in
`internal/repository/duplicate_key.go`: MySQL error 1062 (`ER_DUP_ENTRY`) and the SQLite message
`UNIQUE constraint failed`. If you enable `TranslateError` later, do it on every connection at once,
or the two paths will disagree between production and tests.

**Do not assume a schema or query behaves the same in tests as in production.** Tests run against
in-memory SQLite through `github.com/glebarez/sqlite` — the same pure-Go driver a `DB_DRIVER=sqlite`
deployment uses, so the driver is no longer a difference. The *dialect* still is: production is
MySQL 8 by default and SQLite only by choice. Anything schema- or driver-specific — index semantics,
error codes, collation, `ON CONFLICT` versus `ON DUPLICATE KEY` — has to work on both, and a green
test suite proves only the SQLite half. Both gotchas above are instances of exactly this split.

**Do not pin a `:memory:` database to one connection when the test cancels a transaction.**
`database/sql` discards the pooled connection of a transaction whose context is cancelled, and
the replacement connection to `:memory:` is a fresh, empty database — the next statement fails
with `no such table`. Tests that exercise shutdown or cancellation (the AEO runtime roots in
`internal/service/aeo_runtime_test.go`) open a file under `t.TempDir()` with the production
pragmas instead; `TestAEORuntimeSQLiteSurvivesADiscardedConnection` pins that behaviour.

## Configuration

Environment-based via a `.env` file (loaded by godotenv). See `.env.example` for the full list. Key
settings:

- `DB_DRIVER` — `mysql` (default) or `sqlite`; anything else fails at startup
- `DB_PATH` — SQLite database file (default `gophercrm.db`), read only when
  `DB_DRIVER=sqlite`; must not contain `?`, because the connector appends its own pragma query string
- `DB_*` — MySQL connection settings, ignored when `DB_DRIVER=sqlite`
- `JWT_SECRET` — required, minimum 32 characters; values starting with `CHANGE_THIS` are refused
- `API_KEY_SECRET` — optional, falls back to `JWT_SECRET`
- `SERVER_PORT` (default 8080), `SERVER_MODE` (`development` / `production`)
- `SERVER_HOST` — interface to bind; empty (default) listens on every interface, `127.0.0.1`
  behind a reverse proxy on the same machine; IPv6 literals are bracketed automatically
- `API_PREFIX` (default `/api/v1`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; empty means trust none
- `LOG_LEVEL`, `LOG_FORMAT` (`json` / `text`)
- `DISABLE_RATE_LIMIT` — test-only escape hatch, never enable in production
- `AUTOMATION_ENABLED` (default true) — starts the follow-up automation worker and runs the
  boot-time reconciliation; the API and the event capture work without it, but no run starts or
  resumes and no stranded run is touched. Enable it on exactly one replica.
- `AUTOMATION_POLL_INTERVAL_SECONDS` (default 30, clamped to 1..3600) — how often the worker
  polls for events, due runs and schedule passes; also the granularity of every wait
- `FORM_RETENTION_DAYS` (default off) — age in days after which spam and never-confirmed form
  submissions with no linked lead are permanently removed. Unset, empty, 0, negative or unparseable
  keeps it off; 1 and 2 become the 3-day floor; above 36500 becomes 36500. The first boot with it
  set removes the existing backlog (see *Form retention purge*)

Frontend configuration lives in `gocrm-ui/.env` (see `gocrm-ui/.env.example`); `VITE_API_BASE_URL`
points the Axios client at the backend.

## Database

GORM over MySQL 8.0+ by default, with SQLite selectable through `DB_DRIVER` (pure-Go driver, no
cgo). `internal/database.Open` is the single place a connection is opened and owns every
driver-specific decision — dialector, DSN shape, connection pool.

Auto-migration runs on startup via `models.MigrateDatabase()` and is the **only** schema path on
SQLite: the SQL files in `migrations/` are written for MySQL. The global DB handle is `models.DB`.
The data model is documented in [datamodel.md](datamodel.md). Operational notes for the SQLite
flavour — single-connection pool, WAL, backups — are in the README and [DOCKER.md](DOCKER.md).

## Deleting personal data

Deleting a user, customer or lead performs a **permanent erasure**, not a recoverable soft delete.
This implements the right to erasure under GDPR Article 17, and the storage-limitation principle of
Article 5(1)(e): flagging a row as deleted while retaining the person's name, email and phone number
is retention, not erasure. It is implemented in `internal/repository/erasure.go` and
`internal/repository/erasure_cascade.go`.

What happens on `DELETE`:

- **The person is erased, not the row you clicked.** One person can be stored three times: a lead,
  the customer it was converted into (`leads.customer_id`) and that customer's own login
  (`customers.user_id` pointing at a `customer`-role account). Deleting any of them erases every
  row of that connected graph — every customer whose `user_id` is the erased login, every lead
  converted into an erased customer, the customer a converted lead became, the login of an erased
  customer — soft-deleted rows included, in one transaction (`internal/repository/erasure_cascade.go`).
  The cascade lives in the plain repositories (`NewUserRepository`, `NewCustomerRepository`,
  `NewLeadRepository`); there is no special constructor to wire.
- **Staff links are never followed.** The `customers.user_id` edge counts only when the user's
  role, read inside the transaction, is `customer`. A staff account in that column is a data error,
  not the same person, and following it would let a customer erasure destroy an admin, sales or
  support account past the admin-only `DELETE /users/{id}` and its self-delete guard. So a customer
  or lead erasure leaves a staff user there alone, and erasing a staff user directly erases that
  user only. `assigned_to_id`, `owner_id`, ticket/task assignees, `company_id` and deal, quote and
  ticket links are business links: never followed to another person, never cleared (the deal and
  quote links are read to find the business text to scrub, see below). `customers.user_id` itself is
  kept (an id, not personal data). Note the naming trap: the body of `POST /customers/{id}/assign`
  calls its field `user_id`, but it writes `assigned_to_id`.
- **Who may erase what.** `DELETE /users/{id}` and `DELETE /customers/{id}` are admin-only routes.
  `DELETE /leads/{id}` admits the admin and the owning sales user, but a converted lead reaches a
  customer, so only an admin may erase one: `LeadService.Delete(id, actorRole)` gives any other
  role `LeadRepository.DeleteUnconverted`, which refuses a converted lead
  (`ErrConvertedLeadNeedsAdmin`, an `apperrors.ErrForbidden`, 403) inside the erasure transaction
  before anything is written, and again once the lead row is locked by the scrub. A refusal
  changes nothing.
- Every personal field is overwritten in place. The email is replaced with a unique, non-routable
  placeholder in the reserved `.invalid` domain (RFC 2606) that is generated from `crypto/rand` and
  is **not** derived from the original address, so it cannot be reversed or linked back.
- Every erased user, however it was reached, loses its API keys, refresh tokens and password-reset
  tokens and the notifications addressed to it, and the ticket comments it wrote are redacted to
  `[deleted]` — soft-deleted comments included, each keeping its own deletion state. An API key
  whose owner has been erased or deactivated is rejected at authentication time regardless.
- Only then is each row soft-deleted. The rows are deliberately **kept** so that foreign keys from
  tickets, tasks, deals, quotes and leads still resolve — business records survive, the person
  does not.
- **Deal and quote text (#245).** Once the traversal has reached every row of the person, the
  same unit scrubs the staff-typed text of the deals and quotes linked to the erased customers and
  leads (`internal/repository/business_text_erasure.go`, run by `eraseGraph` after the cascade):
  every deal whose `customer_id` is an erased customer or whose `lead_id` is an erased lead gets
  `title` `[redacted]`, `notes` `''` and `lost_reason` `''`; every quote whose `customer_id` is an
  erased customer, or whose `deal_id` is one of those deals (even when its `customer_id` names
  another customer), gets `title` `[redacted]` and `notes` `''`, whatever its status (an accepted
  quote included), and each of its lines gets the name of its product as `description` (soft-deleted
  products included); and every notification with `entity_type = 'deal'` about one of those deals
  is hard-deleted (`Unscoped`), whatever its recipient, because an automation step renders
  `deal.title` into it. Live and soft-deleted rows are reached alike and keep their own deletion
  state. Stage, probability, amount, currency, `source`, the links, the owner, status, totals,
  dates, line quantities, prices, discounts and positions, `deal_stage_changes` and
  `quote_status_changes` are unchanged. `owner_id` is never followed, so a staff user's erasure
  leaves the deals and quotes they own alone. Each table is probed with `tx.Migrator().HasTable`
  and skipped when absent (fixtures that migrate a subset); when it exists every failure rolls the
  unit back, and the errors name tables and ids only. The fields are overwritten wholesale; there
  is no attempt to detect a name inside them. A locked (sent, accepted …) quote is rewritten all
  the same: the erasure is the only writer that may.
- **One unit, exactly once.** The whole graph is one transaction; a repository obtained through
  `WithTx` brackets it in a checked `SAVEPOINT` of the caller's transaction (released on success,
  rolled back on an error or a panic, sent past GORM's prepared-statement wrapper), so a caller
  that handles a failed erasure, or recovers its panic, and commits keeps none of it (a failed `ROLLBACK TO SAVEPOINT` rolls
  the caller's transaction back and reports `utils.ErrRollbackFailed` +
  `utils.ErrTransactionPoisoned`). Nothing is retried inside; a deadlock reaches the caller. The
  traversal remembers every row it has visited, checked before any read, so each row is scrubbed
  once and the cycle user → customer → lead → customer → user terminates.
- The lead's automation state ends in the same transaction, *before* the scrub
  (`erasurePlan.BeforeScrub`): every pending, waiting or running run on the lead is cancelled with
  `cancel_reason = record_deleted`, so no further step runs against it, and every notification
  whose entity is the lead is hard-deleted, because a rendered title or body may name the person.
  Run logs hold ids only and stay. The tasks a `create_task` step created stay too, as every task
  does (a business record whose `lead_id` still resolves). A task's title and description accept
  only the placeholders `lead.id`, `lead.status`, `lead.source` and the `deal.*` fields; a
  person's field (`lead.first_name`, `lead.email`, `lead.phone`, `lead.company`, `owner.*`) is
  refused when the rule is validated, and the task renderer blanks it even in a rule stored before
  the allowlist was narrowed. The `deal.*` fields are not free of personal data, though:
  `deal.title` is staff-typed and may name the client. The erasure redacts the deal's title
  (#245), but a task rendered from `{{deal.title}}` before the erasure keeps that text — an
  accepted, documented limitation (see the matrix below). A user erasure hard-deletes the notifications addressed to the user alongside the
  credential purge. A failure in either step rolls the whole erasure back; a
  table absent from the schema (a fixture that migrates a subset) is skipped by a migrator probe,
  never by swallowing an error. The order matters on MySQL: an automation step holds the lead row
  under `FOR UPDATE` while it runs, so an erasure that reached the lead first would wait on that
  lock and let the step commit its task; cancelling the runs first means the erasure has modified
  rows by the time it waits, which makes it the survivor of the resulting deadlock — the step
  (nothing modified yet) is rolled back, retried, and finds the run cancelled and the lead gone.

What the erasure touches, and what it keeps on purpose:

| Table | Scrubbed in place / removed | Kept, and why |
|---|---|---|
| `users` (erased account) | `email` → placeholder; `password` → unusable marker; `first_name`, `last_name`; `is_active` false; `last_login_at`, `failed_login_attempts`, `locked_until` | `id`, `role` (not personal; recognises an erased customer login), timestamps |
| `customers` | `email` → placeholder; names, `phone`, `company`, `website`, `position`, `address`, `city`, `state`, `country`, `postal_code`, `notes` | `user_id`, `assigned_to_id`, `company_id` (ids, business links) |
| `leads` | `email` → placeholder; names, `phone`, `company`, `position`, `notes`, `external_id` | `source`, `status`, `classification`, `owner_id`, `customer_id`, `company_id` |
| `form_submissions` linked by `lead_id` (live and soft-deleted) | `data` → `{}`; `email` → the lead's placeholder; `ip_address`, `user_agent`, `referrer`; the six attribution columns `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content`, `referrer_host` (#220) | form, status, `lead_id` |
| `form_confirmation_tokens` and `form_mail_deliveries` of those submissions | hard-deleted | — |
| `form_submissions` with no `lead_id` whose `email` matches the erased lead, customer or user (#214; live and soft-deleted, rows of soft-deleted forms, every status) | `email` → the erased row's placeholder; `data` → `{}`; `ip_address`, `user_agent`, `referrer` and the six attribution columns → `''`; their `form_confirmation_tokens` and `form_mail_deliveries` hard-deleted. Matched on the `email` column only, `email IN (original, lower(original))`: exact or lower-cased on SQLite; on MySQL/MariaDB the `utf8mb4_unicode_ci` collation decides (case- and accent-insensitive, trailing spaces ignored), the rule form intake's lead linking uses. The `data` JSON is not searched. Every submission from that address is affected, a shared inbox's included; a blank or placeholder original matches nothing | `form_id`, `status`, `lead_id` (NULL), the row's own deletion state |
| `api_keys`, `refresh_tokens`, `password_reset_tokens` of an erased user | hard-deleted | — |
| `notifications` addressed to an erased user, whose entity is an erased lead, or whose entity is a deal reached below (#245) | hard-deleted | notifications about unreached deals and other leads |
| `ticket_comments` written by an erased user (live and soft-deleted) | `body` → `[deleted]` | ticket, author id, `is_internal`, deletion state |
| `automation_runs` of an erased lead | pending/waiting/running → `cancelled` / `record_deleted` | finished runs (ids and statuses only); runs of a reached deal keep running and render the redacted title |
| `deals` whose `customer_id` is an erased customer or whose `lead_id` is an erased lead (live and soft-deleted; #245) | `title` → `[redacted]`; `notes`, `lost_reason` → `''` | stage, probability, amount, currency, `source`, every link, `owner_id`, dates, deletion state |
| `quotes` whose `customer_id` is an erased customer or whose `deal_id` is a deal above (every status, live and soft-deleted; #245) | `title` → `[redacted]`; `notes` → `''` | status, currency, totals, `valid_until`, `sent_at`, `closed_at`, `last_emailed_at`, every link, `owner_id`, deletion state |
| `quote_lines` of those quotes | `description` → the name of the line's product (soft-deleted products included) | product, quantity, prices, discount, total, position |
| `tickets` (`title`, `description`, `resolution`), `tasks` (`title`, `description`), `companies` (`notes`), staff-written ticket comments, deals and quotes reached only through `owner_id` | not touched | Business records. Free text may have been typed to contain a name; that is an accepted limitation, not scrubbed (no reliable way to tell a name from content). Ticket text can be redacted by an operator before the erasure (see *Operator procedure for ticket text*). A task a `create_task` step rendered from `{{deal.title}}` before the erasure keeps that text |
| `automation_events`, `automation_run_steps`, `deal_stage_changes`, `quote_status_changes`, `bulk_operations`, `products`, `deals.source` | not touched | Their writers store ids, statuses, values of non-personal fields and error codes; the `form_id` and `submission_id` of a `lead_form_submitted` event are ids, and its attribution stays on the submission row, which is scrubbed. Products are catalogue rows, not personal records |
| `bulk_operation_items` (`data`, `error`) | not touched | Legacy rows written by the generic bulk subsystem removed in #112 can hold request payloads; nothing writes the table any more (retirement is on the roadmap) |
| `form_submissions` with no `lead_id` (spam, never-confirmed opt-ins, forms that create no lead) whose `email` matches no erased row | not touched by erasure | Not linked to any person the CRM erased. With `FORM_RETENTION_DAYS` set, the retention purge removes old spam and never-confirmed rows (see *Form retention purge*); received rows of forms that create no lead stay |
| `customer_external_ids` of an erased customer (#212) | hard-deleted (the customer plan's `AfterScrub`, every root that reaches the customer) | mappings of other customers |
| `ticket_mail_deliveries` whose `customer_id` is an erased customer or whose `ticket_id` names one of its tickets, soft-deleted tickets included (#219) | hard-deleted (the customer plan's `AfterScrub`, `purgeCustomerTicketMailDeliveries`, every root that reaches the customer; skipped by a `HasTable` probe when the table is absent) | deliveries of other customers; the rows of a comment whose staff author is erased |
| Logs, already-issued JWTs | out of reach | See below |

The whole-schema sweep tests (`test/integration/erasure_pii_sweep_test.go`,
`test/integration/business_text_erasure_sweep_test.go`,
`internal/repository/identity_erasure_test.go`) seed the person into every scrubbed target above
first and assert the sweep can see each copy before the erasure; an empty table would prove
nothing.

Regression roots for the deal and quote text (#245), on SQLite and on a disposable
`TEST_MYSQL_DSN` schema (skipped without it) unless noted, in
`internal/repository/business_text_erasure_test.go`:
`TestCustomerErasureScrubsDealText{SQLite,MySQL}`, `TestCustomerErasureScrubsQuoteText{SQLite,MySQL}`,
`TestLeadErasureScrubsDealAndQuoteText{SQLite,MySQL}` (admin and non-admin lead erasure, converted
lead, customer-role login, refused erasure), `TestBusinessTextErasureLeavesUnrelatedRecords{SQLite,MySQL}`,
`TestBusinessTextErasureAtomic{SQLite,MySQL}` (a failure injected into the `quote_lines` scrub, and
a `WithTx` caller that rolls back), `TestBusinessTextErasurePurgesDealNotifications{SQLite,MySQL}`
and `TestBusinessTextErasureWithoutDealTables` (SQLite, two schema subsets); the identity sweep
`TestIdentityErasure{SQLite,MySQL}`; `TestCustomerErasureWithNamedDealAndQuoteTextLeavesNoPersonalData`,
`TestLeadErasureWithNamedDealAndQuoteTextLeavesNoPersonalData` and
`TestErasingALinkedCustomerLeadAndUserKeepsTheDealAndItsHistory` (`test/integration`); and, over
HTTP, `TestBusinessTextErasureAPISQLite` (`tests`). CI limit: the MySQL roots skip in
`make verify-backend`, which has no DSN; registering them in a checker is an operator follow-up.

**Unlinked submissions (#214).** `eraseRecord` reads the row's original address before it writes
the placeholder (the plan's `AfterScrub` sees only the placeholder) and, after `AfterScrub` and
before the soft delete, runs `scrubUnlinkedSubmissions` for every plan with an email column — leads,
customers, staff and customer-role users alike. It is skipped when the original is blank or already
ends in `@anonymized.invalid`, and when `form_submissions` is absent from the schema
(`tx.Migrator().HasTable`; the `internal/service` fixtures migrate only users, customers and
credentials); `form_mail_deliveries` keeps its own probe. When the table exists every failure
propagates and rolls the unit back, and returned errors carry table names and ids, never an address.
Regression roots: `TestUnlinkedSubmissionErasure{SQLite,MySQL}`
(`internal/repository/unlinked_submission_erasure_test.go`: the four roots, mixed-case addresses,
every status, soft-deleted rows and forms, a spendable confirmation token found by
`GetConfirmationTokenByHash` before and not after, untouched rows of other addresses and of a
namesake lead, blank and placeholder originals, an injected failure after the scrub, a caller
rollback, and a schema without `form_submissions`) and
`TestUnlinkedSubmissionErasureLeavesNoPersonalDataInAnyTable` (`test/integration`).

**Customer external ids (#212).** `customer_external_ids` maps an integrator's `(source, scope,
external_id)` to a customer: `source varchar(64)`, `scope varchar(128) NOT NULL DEFAULT ''`,
`external_id varchar(255)`, all `NOT NULL`, a foreign key to `customers.id`, no `deleted_at`. Its
only unique index is `idx_customer_external_ids_identity (source, scope, external_id)`, so
`isDuplicateKeyError` can attribute a hit there to the triple
(`apperrors.ErrCustomerExternalIDTaken`, 409); `idx_customer_external_ids_customer_id` is not
unique. Comparison is exact on every engine: SQLite compares with `BINARY`, MySQL and MariaDB carry
`utf8mb4_bin` on the three columns, from `20261111120000_add_customer_external_ids` (which creates
the table only when missing) or from the collation guard in `models.MigrateDatabase`, which reads
`information_schema.COLUMNS` and issues the `ALTER TABLE … MODIFY` only when a column differs, so a
schema the application alone created is converted once and later starts take no DDL. `utf8mb4_bin`
is a PAD SPACE collation on MySQL, which is one reason `service.ValidateCustomerExternalIdentity`
refuses leading and trailing whitespace; it also refuses invalid UTF-8 and control characters and
counts the limits (64/128/255) in runes. `CustomerExternalIDService.AddExternalID` validates, then
runs one transaction on the request context that locks the live customer row
(`CustomerExternalIDRepository.LockLiveCustomer`, `FOR UPDATE` on MySQL, a plain read on SQLite)
and inserts. Every customer erasure scrubs, and so locks, that row before its `AfterScrub`
(`purgeCustomerExternalIDs`, `erasure_cascade.go`) hard-deletes the customer's mappings, so a create
racing an erasure either commits first and loses its mapping to the erasure, or waits and answers
`ErrNotFound` (404); no mapping survives on an erased customer and neither ordering deadlocks. The
purge probes the table with `tx.Migrator().HasTable`; when it exists every failure rolls the unit
back with an error naming the table and the customer id only. `DELETE
/customers/{id}/external-ids/{mappingId}` deletes only a row of that customer (else 404).
`GET /customers` enters lookup mode when the query carries `source`, `scope` or `external_id`: after
the role check (admin, sales, support) the handler validates the three values, with no service call
on a refusal, and answers the list shape with at most one live customer. A service built without
its repository or transaction manager returns `service.ErrCustomerExternalIDsUnavailable` from
every method. Log lines carry operation names, `customer_id`, `mapping_id`, counts and presence
flags only, never a value of the triple. Regression roots: `TestCustomerExternalIDSchemaSQLite`,
`TestCustomerExternalIDSQLiteUpgrade`, `TestCustomerExternalIDJSON` (`internal/models`);
`TestCustomerExternalIDsMigrationFiles`, `TestCustomerExternalIDsMigration` (`cmd/migrate`);
`TestCustomerExternalIDRepository{SQLite,MySQL}`, `TestCustomerExternalIDErasure{SQLite,MySQL,WithoutTable}`
(`internal/repository`); `TestCustomerExternalIDValidation`,
`TestCustomerExternalIDMaxLengthsMatchTheColumns`, `TestCustomerExternalIDServiceRefusedWhenUnwired`,
`TestCustomerExternalIDLogsOmitValues`, `TestCustomerExternalIDCreateRacesErasure{SQLite,MySQL}` (the
MySQL root forces both orderings, each wait seen in `information_schema.PROCESSLIST` on two
consecutive polls) (`internal/service`); `TestCustomerExternalIDHandlerErrorMapping`,
`TestCustomerListExternalIDLookup`, `TestCustomerExternalIDHandlerLogsOmitValues`
(`internal/handler`); `TestCustomerExternalIDAPISQLite` (`tests`);
`TestCustomerExternalIDErasureLeavesNoPersonalDataInAnyTable` (`test/integration`);
`TestCustomerExternalIDProductionWiring` (`cmd`). The MySQL roots use a disposable `TEST_MYSQL_DSN`
schema and skip without it; registering the migration roots in the Database migrations workflow is
an operator follow-up.

**Product external ids (#239).** `product_external_ids` copies the customer pattern for the
catalogue: `product_id` (NOT NULL, `fk_product_external_ids_product` to `products.id`), `source
varchar(64)`, `scope varchar(128) NOT NULL DEFAULT ''`, `external_id varchar(255)`, no
`deleted_at`. Its only unique index is `idx_product_external_ids_identity (source, scope,
external_id)`, so `isDuplicateKeyError` attributes a hit to the triple
(`apperrors.ErrProductExternalIDTaken`, 409); `idx_product_external_ids_product_id` is not unique.
`products.sku` stays indexed and non-unique on purpose (a unique index on a soft-deleted table
either blocks the reuse of a code or means nothing). Comparison is exact: SQLite's `BINARY`, and
`utf8mb4_bin` on MySQL/MariaDB from `20261112120000_add_product_external_ids` (created only when
missing) or from `ensureProductExternalIDCollation` in `models.MigrateDatabase`, which, like the
customer guard, issues the `ALTER TABLE … MODIFY` only when `information_schema.COLUMNS` shows a
column with another collation. `service.ValidateProductExternalIdentity` delegates to
`ValidateCustomerExternalIdentity` (same widths and rules, the same
`*CustomerExternalIDFieldError`). `ProductExternalIDService.AddExternalID` validates, then runs one
transaction on the request context that locks the live product (`LockLiveProduct`, `FOR UPDATE` on
MySQL, a plain read on SQLite; an inactive product is live) and inserts. **`ProductService.Delete(ctx,
id)` is now one `WithTransaction`**: the same locking read, `PurgeForProduct` (hard delete of every
mapping of the product), then the product's soft delete, through the transaction-bound
repositories; a failure anywhere rolls the purge back, and a missing or already-deleted product is
`ErrProductNotFound` with nothing written. Because both writes lock the product row first, a create
racing a delete either commits first and is purged, or waits and answers `ErrNotFound` (404); no
mapping survives on a deleted product and neither ordering deadlocks. Quote lines keep their
`product_id`. **A `ProductService` built without `WithProductExternalIDs(repo, txManager)` refuses
`Delete` with `service.ErrProductExternalIDsUnavailable` (500) and writes nothing**, and a
`ProductExternalIDService` without its repository or manager returns the same error from every
method; `cmd/main.go` and `test/integration/base_test.go` wire both. `POST
/products/{id}/external-ids` and `DELETE /products/{id}/external-ids/{mappingId}` are admin only (the
route guard, repeated in the handler); the delete removes only a row of that product (else 404).
`GET /products` enters lookup mode when the query carries `source`, `scope` or `external_id`, even
empty: the handler validates the three before any service call, ignores the other list parameters
and answers `data` as an array of 0 or 1 live product with `meta.page` 1 (admin and sales). There
is no erasure step: products hold no personal data. Log lines carry operation names, `product_id`,
`mapping_id`, `mappings_removed`, counts and presence flags only. Regression roots:
`TestProductExternalIDSchemaSQLite`, `TestProductExternalIDSQLiteUpgrade`,
`TestProductExternalIDJSON` (`internal/models`); `TestProductExternalIDsMigrationFiles`,
`TestProductExternalIDsMigration` (`cmd/migrate`; the newest-migration assertion has since moved to
`TestTicketMailDeliveriesMigrationFiles`, #219);
`TestProductExternalIDRepository{SQLite,MySQL}` (`internal/repository`);
`TestProductExternalIDValidation`, `TestProductExternalIDServiceRefusedWhenUnwired`,
`TestProductDeleteRefusedWhenUnwired`, `TestProductExternalIDLogsOmitValues`,
`TestProductDeleteFreesExternalIDs{SQLite,MySQL}` (an injected soft-delete failure, captured
statement order), `TestProductExternalIDCreateRacesDelete{SQLite,MySQL}` (the MySQL root forces both
orderings, each wait seen in `information_schema.PROCESSLIST` on two consecutive polls)
(`internal/service`); `TestProductExternalIDHandlerErrorMapping`, `TestProductListExternalIDLookup`,
`TestProductExternalIDRoutesRoles` (`internal/handler`); `TestProductExternalIDAPISQLite` (`tests`);
`TestProductExternalIDProductionWiring` (`cmd`). The MySQL roots use a disposable `TEST_MYSQL_DSN`
schema and skip without it; registering the migration roots in the Database migrations workflow and
`scripts/ci/check-go.py` is an operator follow-up.

**Operator procedure for ticket text.** `tickets.title`, `description` and `resolution` stay as
business records; the erasure does not scrub them. When a customer's tickets name the person,
redact them before the erasure:

1. List the customer's tickets with `GET /customers/{id}/tickets` (admin or sales see all of them).
2. For each ticket, `PUT /tickets/{id}` with `title`, `description` and `resolution` set to
   `[redacted]` and `status` omitted, as an admin or as the assigned support user (sales,
   customers and other support users get 403). A closed ticket stays closed. Empty strings are
   ignored by the handler, so a field cannot be cleared, only overwritten.
3. Consider whether an Article 17(3) exemption (a legal obligation, or the establishment, exercise
   or defence of legal claims) requires keeping some of the text, and keep it if so.
4. Run `DELETE /customers/{id}`. The redacted tickets survive with their `customer_id`.

Comments the person wrote through their login are redacted by the erasure itself; staff-written
comments are not, and have no update route. Regression root: `TestTicketRedactionProcedureSQLite`
(`tests/ticket_redaction_procedure_test.go`).

Two consequences worth knowing:

- **It is irreversible.** To suspend access reversibly, set `is_active = false` instead; deactivation
  never touches personal data.
- **The email address becomes reusable.** Because the original address no longer exists anywhere in
  the table, the person can register again with it.

**Deploying a new erasure rule does not repair history.** Graphs erased before #108 can still hold
the other half of the person: a live customer whose login was erased, or a live customer-role login
whose customers were erased. Finding them is a read-only query:

```sql
-- live customers linked to an erased customer-role login
SELECT c.id FROM customers c JOIN users u ON u.id = c.user_id
 WHERE c.deleted_at IS NULL AND u.deleted_at IS NOT NULL AND u.role = 'customer';
-- live customer-role logins with an erased customer
SELECT DISTINCT u.id FROM users u JOIN customers c ON c.user_id = u.id
 WHERE u.deleted_at IS NULL AND u.role = 'customer' AND c.deleted_at IS NOT NULL;
-- live leads converted into an erased customer
SELECT l.id FROM leads l JOIN customers c ON c.id = l.customer_id
 WHERE l.deleted_at IS NULL AND c.deleted_at IS NOT NULL;
```

Every query returns a **live** row. Review each hit (it is the same person only if the link was
right), take a backup, and erase it as an admin through the API (`DELETE /customers/{id}`,
`/users/{id}`, `/leads/{id}`): that runs the shared cascade, which also re-scrubs the
already-erased rows of the graph. Do this under supervision; it is irreversible.

There is **no supported path yet for a graph whose rows are all soft-deleted.** The API refuses to
erase what it cannot see: every `DELETE` looks the row up with an ordinary (scoped) query first, so
a soft-deleted root answers 404 — the same check that makes a second `DELETE` of an erased record a
404 rather than a silent success. The repository cascade itself would scrub such a graph, but no
command exposes it; until one exists (follow-up to #108, alongside #183), such rows can only be
handled by hand under supervision.

Rows that were soft-deleted *before* deletion became an erasure at all still hold personal data.
`scripts/anonymize_legacy_deleted_pii.sql` scrubs soft-deleted `users` and `customers` rows and
purges API keys and refresh tokens, run by hand once you have taken a backup. It is **not**
equivalent to the erasure above: it does not touch leads, password-reset tokens, notifications,
ticket comments or form submissions, and does not follow any link to a live counterpart, so its
own verification can pass while linked personal data survives. After running it, run the queries
above and erase every live hit through the API; fully soft-deleted graphs remain the gap described
in the previous paragraph. It is irreversible.

Known limits of the erasure, in one place:

- **Concurrency.** The erasure is one transaction, but it does not yet share a lock protocol with
  lead conversion, customer/login linking, login, refresh rotation, password reset or API-key
  creation. A conversion, link or credential committed while an erasure is in flight can escape
  the cascade; opposite entry points can deadlock (an error, never a partial commit). The read of
  the original address and the read of the matching unlinked submission ids are plain snapshot
  reads, so a submission from the same address inserted concurrently can survive. Tracked in
  #183.
- **History.** Deploying a new erasure rule does not repair graphs erased under an older one (see
  above); fully soft-deleted graphs have no supported path yet.
- **Retained content.** Business free text (tickets, tasks, company notes, staff-written comments,
  `deals.source`, deals and quotes reached only through their owner) and legacy
  `bulk_operation_items` payloads are kept by design (see the matrix); ticket text can be redacted
  first (see *Operator procedure for ticket text*). Deal and quote text of the erased person is
  scrubbed (#245), but a task rendered from `{{deal.title}}` before the erasure keeps its copy, and
  deals and quotes of graphs erased before #245 are not repaired.
  Unlinked form submissions are erased only when their `email` matches the erased row; others
  stay until the retention purge removes them. Logs and issued JWTs are out of reach.
- **Form-delete dialogs.** Both form-delete dialogs still say "Collected submissions are retained"
  until a separate frontend task merges. Once `FORM_RETENTION_DAYS` is set that is wrong for spam
  and never-confirmed rows, which the purge removes from soft-deleted forms too.

The sample configuration defaults to `LOG_LEVEL=info`. Service logs keep operation names, entity
IDs, validated statuses and counts, and omit email, name, title, search and raw sort fields. Shared
logging helpers omit arbitrary arguments and response payloads, and HTTP access logs omit query
strings. Logged errors omit quoted value suffixes; form field errors use a count summary, and SMTP
errors retain the protocol code. These changes affect log output only, preserving returned errors
and API response bodies.

Log retention still needs its own policy: other raw diagnostic text, request IDs, client IPs,
Origin/User-Agent headers and mailer recipient metadata remain, as do older logs. Database erasure
does not clear these logs or the email embedded in already-issued JWTs.

## API Reference

The REST surface is enumerated in the [README](../README.md#api-documentation). The complete
reference is generated and checked in under `api/`:

- `api/swagger.json` and `api/swagger.yaml`: the Swagger 2.0 spec, built by swag v1.16.6 from the
  annotations on the routed handlers, the request/response types in `internal/handler/` and
  `internal/models/` (plus four types from `internal/service/`), and the general block in `cmd/main.go`.
- `api/index.html`: a static, self-contained HTML rendering of the spec
  (`scripts/docs/render_api_reference.py`, standard-library Python). It has no scripts, fonts or
  network references, so opening the file from disk works.

`make swagger` regenerates all three; run it after changing a handler annotation, a documented
type or a route, and commit the result. The application serves none of it (deliberate), and the
router in `internal/handler/` remains the source of truth when the two disagree.

### OpenAPI spec

The annotations document what the code does, including its warts. A defect found while
documenting goes to [ROADMAP.md](ROADMAP.md), never into the spec as intended behaviour.

- **General block** (`cmd/main.go`): authentication (`Authorization: Bearer <jwt>` and
  `Authorization: ApiKey <key>`, refresh rotation, access tokens ended by logout or a password
  change or reset, otherwise valid until expiry), the response
  envelope, the shared error codes, pagination, rate limits, CORS and `X-Request-ID`, the two
  security schemes (both the `Authorization` header) and one `@tag.name`/`@tag.description` pair
  per tag. A new tag needs a declaration there.
- **Operations**: summary, a description that names the roles admitted, the visibility narrowing,
  side effects (mail, automation events, erasure) and the rate-limit tier, exactly one tag, a
  described parameter for every input, and a `@Failure` line per status the handler can return
  with its `error.code` values and conditions. List operations document their defaults,
  clamping and allowed `sort_by` columns; bodyless 204s stay schema-free; the public form pages
  declare their real media types.
- **Types**: every field carries a comment (meaning, unit, format, nullability, who sets it),
  plus struct tags where the code enforces something: `enums` for closed sets, `minimum`,
  `maximum` and `maxLength` only for character limits the code checks (byte limits stay in
  prose), `example`, `format`, `x-nullable`, `readonly`. Use `swaggertype` where the Go type
  serialises differently (for example `gorm.DeletedAt` as a nullable timestamp). A value whose
  JSON type varies stays untyped: `interface{}` fields need no tag, and `json.RawMessage` is
  replaced by `interface{}` through `scripts/docs/swag-overrides` (passed by `make swagger`).
  A field the service, not the binder, requires (or an array whose length it bounds) gets a
  `validate:"required"` / `validate:"min=1,max=20"` tag: swag turns it into `required` /
  `minItems`/`maxItems`, while gin validates only `binding` tags, so it changes nothing at
  runtime (say so in the type's comment). Where the JSON
  differs from the struct (a `MarshalJSON`, a `gin.H` payload, a list wrapper) a
  documentation-only type describes it (`models.QuoteJSON`, `handler.LeadListData`, ...); such a
  type is never bound or encoded. No JSON tag, binding or validation changes for documentation's
  sake.
- **Known swag behaviours**: binding failures report `error.details` keyed by Go field name, and
  swag keeps comment line breaks in descriptions (the HTML renderer joins them). An escaped quote
  (`\"`) inside a `@Failure` description reaches the spec verbatim; plain double quotes work.

### API docs gate

`make verify-api-docs` (part of `make verify`, an unconditional CI step, and in every bounded
validation profile) fails when the reference is stale or incomplete. It regenerates the three
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
such as `guard := middleware.RequireRole(...)`); requires every status the route middleware can
answer with to be a documented response (401 for `middleware.Auth`, 403 for `RequireRole` and
`RequireRegistrationEnabled`, 429 for the three rate limiters); identifies the handler each route
registers (a method value on a typed handler variable, a function, or a factory call such as
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

### AEO background work and shutdown

The API owns AEO goroutines beyond individual requests. `cmd/aeo_runtime.go`
holds them: `service.NewAEORuntime` wires the service together with the owner of its runs, the
daily scheduler is a joinable `aeo.Scheduler` armed whenever `AEO_SCHEDULE_ENABLED` is on, and
`setupDependencies` returns the stop handle `main` drains — through `drainAEO`, bounded by
`aeoDrainTimeout` (15 s) — before `models.CloseDatabase`. HTTP requests then have their
existing separate 5 s shutdown deadline. Inside the service, admission and the
active-run count share one mutex (`aeoRunOwner`), separate from `startMu`, so shutdown never queues
behind a start blocked in a database call; it closes admission, cancels the shared run context and
waits. Run records and answer writes go through `AEORepository.WithContext(runCtx)`, and the
engine finalizes on a fresh context bounded by `EngineOptions.CleanupTimeout`. When adding
background work elsewhere, follow the same shape: register before the first database call, hand
the registration to the goroutine, release after the terminal write, and never close the pool
while the count is non-zero. Tests substitute `aeoSchedulerClock` to deliver a due tick on demand.

**Run ownership across replicas (#229).** AEO runs are safe across several API replicas sharing
one MySQL/MariaDB database.

- **Ownership.** Each `aeoService` generates an instance id at construction (32 lowercase hex
  characters from `crypto/rand`; `WithAEOInstanceID` for tests) and stores it in
  `aeo_runs.owner_instance` of every run it claims. While the executor runs, a heartbeat goroutine
  renews `heartbeat_at` every `aeoRunHeartbeatInterval` (30 s) through
  `RenewRunHeartbeat(id, owner, at)` (`UPDATE … WHERE id = ? AND status = 'running' AND
  owner_instance = ?`; MySQL's zero changed rows are re-checked with a read). The goroutine is
  joined before the run's admission is released, so `AEORuntime.Shutdown` returning nil leaves no
  heartbeat writer. `WithAEOClock` and `WithAEOHeartbeatInterval` are the test seams.
- **Admission.** `startRun` runs: lifecycle admission; the profile and provider checks; `startMu`;
  the abandoned-run sweep; prompt selection; `ClaimRun`. The claim is one INSERT with
  `running_lock = 1` and `heartbeat_at = started_at`; the unique `idx_aeo_runs_running_lock` admits
  one running row across every process, so there is no count-then-insert any more and
  `startMu` only serialises this process's own sweep and claim. A duplicate key is
  `apperrors.ErrRunInProgress` (409, or a scheduled skip), or `apperrors.ErrAEOScheduleSlotTaken`
  when the claim names a slot that a row already holds.
- **The daily slot.** The scheduler calls `StartScheduledRun(ctx, slot)` with the instant it
  computed with `aeo.NextRunAt`, never the wake-up time; the slot is stored as
  `slot.UTC().Format("2006-01-02T15:04Z")` under the unique `idx_aeo_runs_schedule_slot`, so one
  replica starts it and the others log one info line (`error_code=schedule_slot_taken`), also
  after the run finished. A scheduled claim refused by a manual run's lock records no slot, and a
  skipped slot is not retried. A body trigger of `scheduled` on `POST /aeo/runs` stores no slot.
  Every replica must share `TZ` and `AEO_SCHEDULE_HOUR`.
- **Recovery.** `FailAbandonedRuns(startedBefore, heartbeatBefore)` fails, in one conditional
  UPDATE, the running rows that started more than `aeoRunStaleAfter` (6 h) ago or whose
  `heartbeat_at` is NULL (legacy) or older than `aeoRunHeartbeatStaleAfter` (3 min), clearing
  `running_lock`. It runs at boot (`ReconcileRunningRuns`, on every replica whatever
  `AUTOMATION_ENABLED` and `AEO_SCHEDULE_ENABLED`) and before every start; there is no periodic
  sweep, so a crashed run reads `running` until the next boot or start after the horizon (up to
  3 minutes of 409 after a crash). A live run on another replica is never failed.
- **Finalisation.** The engine and `failInterruptedRun` write the terminal state with
  `FinishRun(run)`, `UPDATE … SET status, completed_at, total_queries, failed_queries,
  updated_at, running_lock = NULL WHERE id = ? AND status = 'running' AND owner_instance = ?`.
  `AEORepository` has no `UpdateRun` and nothing saves an `aeo_runs` struct. A run another
  replica failed matches nothing (`apperrors.ErrAEORunNotOwned`, one Warn line with `run_id`),
  and its owner's next heartbeat cancels the executor. Answers written between the takeover and
  that heartbeat (at most one interval) stay on the failed run; fencing them is a roadmap item.
- **Logs.** The claim refusal, slot skip, heartbeat loss, refused finalisation and recovery lines
  carry `run_id`, counts, the trigger, the status and error codes; never the brand, a prompt, the
  instance id or the slot.
- **Deployment.** Upgrade by stopping every replica before starting the new version (an older
  binary fails every running row at boot and sets no lock). Replica clocks must agree to well
  under 90 s. Several processes on one SQLite file are not supported.

Schema: `models.AEORun` (`json:"-"` on the four columns) via `MigrateDatabase` on SQLite, and
`migrations/20261114120000_add_aeo_run_ownership` (each column and index only when missing) on
MySQL/MariaDB; see [datamodel.md](datamodel.md#aeo-run-ownership). Regression roots:
`TestAEORunOwnership{SchemaSQLite,SQLiteUpgrade,JSONHidden}` (`internal/models`);
`TestAEORunOwnershipMigrationFiles` (now holding the newest-migration assertion) and
`TestAEORunOwnershipMigration` (`cmd/migrate`); `TestAEORun{Claim,Finish,Heartbeat,AbandonedSweep}{SQLite,MySQL}`
(`internal/repository`); `TestAEOReplica{BootSparesLiveRun,Admission,ScheduleSlot,DeadOwnerRecovered,FinalizeAfterTakeover}{SQLite,MySQL}`,
`TestAEORunHeartbeatLifecycle` and `TestAEOReplicaLogsOmitPersonalData` (`internal/service`,
two service instances with separate pools on one SQLite file or one disposable `TEST_MYSQL_DSN`
schema); `TestSchedulerPassesSlot`, `TestSchedulerSlotTakenSkip`,
`TestEngineFinalizeRefusedWhenNotOwned` (`internal/aeo`); `TestAEOReplicaAPISQLite` (`tests`);
`TestAEOReplicaProductionWiring` (`cmd`). The MySQL roots skip without the variable and fail
when it is unreachable; registering them in `scripts/ci/check-go.py` and the migration roots in
the Database migrations workflow is an operator follow-up.

**AEO export (#244).** `GET /aeo/export` (`AEOHandler.Export`, `internal/handler/aeo_export.go`)
sits in the `/aeo` read group, so admin, sales and support reach it and the customer role gets 403
from `RequireRole` before the handler runs.

- **Parameters.** `format` is `csv` (default) or `json`; `run_id` is a positive integer; `days` is
  checked against `aeoAllowedRangeDays` (7, 30, 90) and, unlike the dashboard's
  `aeoReportingRange`, never replaced by a fallback; `run_id` with `days` is refused; neither means
  `days=30`. An empty value counts as absent. A refusal is 400 `VALIDATION_ERROR` with
  `error.details` keyed by the parameter name, the value not echoed, and no service call.
- **Service.** `AEOService.Export(ctx, models.AEOExportSelection)` (`internal/service/aeo_export.go`)
  reads the run first for a run selection (missing → `apperrors.ErrNotFound`, 404, before any answer
  read). A window is computed on the instance clock (`WithAEOClock`): `to` is the start of tomorrow
  UTC, `from` is `to` minus `days`, and `generated_at` is the same clock reading; both bounds are
  returned in the selection. It then calls `ListAnswersForExport(ctx, filter,
  AEOExportMaxAnswers+1)`; more than `service.AEOExportMaxAnswers` (10,000) answers is
  `service.ErrAEOExportTooLarge` (422 `EXPORT_TOO_LARGE`, `error.details.max_answers`) with no
  citation, run or prompt read. Otherwise it reads the citations, the runs referenced by the answers
  (plus the selected run) and the prompt texts, and assembles `models.AEOExport`; an empty window
  is empty slices with no further read. Any other error is 500.
- **Repository.** The four reads form `repository.AEOExportReader`, which `aeoRepository`
  implements and the service reaches by a type assertion on its `AEORepository` (the
  `TicketLockingReader` pattern), so hand-written `AEORepository` doubles in other packages keep
  compiling; a repository without it answers `service.ErrAEOExportUnavailable` (500).
  `ListAnswersForExport` reads live answers only, without citations, by `run_id` equality or
  `created_at >= from AND created_at < to`, ordered by `created_at` then `id`, at most `limit` rows.
  `ListCitationsByAnswerIDs` de-duplicates and sorts the ids and reads them in chunks of
  `repository.AEOExportChunkSize` (500) per statement ordered by `answer_id` then `id`, so the
  concatenation keeps that order; an empty list issues no statement. `ListRunsByIDs` (live runs,
  id order) and `ListPromptTextsByIDs` (Unscoped: a soft-deleted prompt keeps its text) chunk the
  same way. No date or JSON function, no schema change, no new index.
- **Output.** Every row is read and the whole file rendered in memory before the status line, so a
  failure is a JSON error without `Content-Disposition`. CSV: `text/csv; charset=utf-8`, the pinned
  `aeoExportCSVColumns`, one row per citation and one row with empty citation cells per answer
  without citations, no `answer_text` column, RFC 3339 UTC timestamps, `true`/`false`,
  `competitor_mentions` as `name=count` pairs sorted by name joined by `;`; every free-text cell
  goes through `aeoCSVSafeField` (`csvSafeField` plus a leading LF and the full-width ＝ ＋ － ＠;
  `csvSafeField` and the customer and lead exports are unchanged). JSON: `application/json;
  charset=utf-8`, the raw `models.AEOExport` (`format_version` 1). Attachments are
  `aeo-export-run-<id>.<ext>` or `aeo-export-<days>d-<YYYY-MM-DD>.<ext>`, the date being the
  returned `selection.to` minus 24 h in UTC (the handler reads no clock). The export types carry no
  ownership field, so `owner_instance`, `heartbeat_at`, `running_lock` and `schedule_slot` never
  appear.
- **Logs.** Handler lines carry `operation` (`aeo_export`), `format`, `run_id` or `days` and
  `error_code`; service lines add the answer, citation and run counts. Neither logs a raw query
  value, prompt or answer text, URL, domain, competitor, brand or model.

Regression roots: `TestAEOExport{HandlerValidation,Roles,ErrorMapping,HandlerLogsOmitPersonalData,CSVShape,CSVFormulaGuard,JSON}`
(`internal/handler/aeo_export_test.go`); `TestAEOExport{Service,LogsOmitPersonalData}`
(`internal/service/aeo_export_test.go`); `TestAEOExportRepository{SQLite,MySQL}` and
`TestAEOExportIDChunks` (`internal/repository/aeo_export_repository_test.go`, statement counts
through a GORM query callback); `TestAEOExportAPISQLite` (`tests/aeo_export_test.go`, real
middleware, repositories and SQLite). The MySQL root uses a disposable `TEST_MYSQL_DSN` schema,
skips without the variable (as in `make verify-backend`) and fails when it is unreachable; the
acceptance run sets it. Operator follow-ups: registering the MySQL root in
`scripts/ci/check-go.py`, and adding the `trigger`/`status` enums of `models.AEOExportRun` to the
closed-set table in `scripts/ci/check-api-docs.py`.

### Automation runtime

Follow-up automation (#164) is the second owner of background work. `internal/service/
automation_engine.go` executes rules, `automation_runtime.go` owns the worker goroutine, and
`cmd/automation_runtime.go` wires both into `setupDependencies`, which returns one drain handle
(`combineStops`) that `main` runs — automation first, then the form retention purge, then the
stale bulk operation sweep (#196), then AEO — before `models.CloseDatabase`. That order describes
the code and is not a contract: no handle depends on another, all four share one deadline, and no
test asserts the order.
The pieces, and why each is shaped the way it is:

- **The outbox event is written in the write transaction.** `LeadService.Create/Update/
  Assign/ConvertToCustomer`, the form submission path, the bulk lead and ticket status paths,
  `DealService.Create/Update/ChangeStage` and `TicketService.Create/Update` (#242, see *Tickets*
  below) take the `AutomationRepository` through a functional
  option (`WithLeadAutomationEvents` and friends) and record `automation_events` rows on the
  transaction-bound repository, so a write that rolls back records nothing and a refused bulk
  status batch records nothing for any of its leads. A create records `*_created`; an update `*_updated` plus
  `*_status_changed` / `*_stage_changed` with `from_value` and `to_value` when the value differs;
  a lead assign `lead_updated` alone; the stage endpoint `deal_stage_changed` alone; the same stage again nothing. Events carry ids,
  types, statuses and the origin (`user`, `form`, `bulk`, `conversion`, `automation`), never a
  name or an address. A form submission linked to a lead adds `lead_form_submitted` with its
  `form_id` and `submission_id` (see *Form attribution and the form-submitted event*).
- **Three conditional claims.** The dispatcher claims an event with `UPDATE … SET processed_at
  WHERE id = ? AND processed_at IS NULL` (exactly one row) and, in the same transaction, inserts
  one run per enabled matching rule with `clause.OnConflict{DoNothing: true}` on the unique
  `dedupe_key` — `ON CONFLICT DO NOTHING` on SQLite, `ON DUPLICATE KEY UPDATE id=id` on MySQL, no
  error and no row for a duplicate on either. The executor claims a run with `UPDATE … SET status =
  'running' WHERE id = ? AND status = <pending|waiting>` (a waiting run only once `resume_at <=
  now`). A schedule batch advances the rule's cursor with `UPDATE … WHERE id = ? AND
  schedule_cursor = ?` plus `schedule_pass_on IS NULL` before the first pass or `schedule_pass_on
  = ?` afterwards (`AdvanceScheduleCursor`); two workers handing out the same batch commit
  exactly one. Every one of these changes a column value when it matches, so MySQL's changed-rows
  count is the matched count.
- **Each step: a locking read of the record, then a conditional advance.** A step other than
  `send_email` is one `WithTransactionAndRetry` transaction that, in order, re-reads the lead, deal
  or ticket with the transaction-bound locking read (`GetLeadForUpdate` / `GetDealForUpdate` /
  `GetTicketForUpdate`, `SELECT …
  FOR UPDATE` on MySQL, a plain read on SQLite), advances the run with `UPDATE … WHERE id = ? AND
  status = 'running' AND next_position = <expected>` — exactly one row, else the transaction rolls
  back and the executor stops — applies the effect (task, owner change plus an `origin =
  automation` event, notification) and inserts the `automation_run_steps` row. The locking read is
  not decoration: a plain read inside MySQL's REPEATABLE READ transaction answers from the snapshot
  taken at the transaction's first statement, so a cancel, a rule change or an erasure committed by
  another connection after that read would be invisible, and the step would act on a record that
  no longer exists or a run that is already cancelled. With the locking read the record is current
  and the conditional advance is what notices the run moved. A fault after the effect rolls the
  effect, the log row and the advance back together; the run is handed back to `pending` at the
  same position (`attempts + 1`, the third failure fails it with `step_failed`) and the next
  execution performs the step exactly once. A recipient or assignee that is inactive, erased or not
  staff is logged as `skipped` with `recipient_unavailable`. The task's title and description are
  rendered through the task placeholder allowlist (ids, status, source, deal fields), never
  through the wider email and notification one: a task outlives the erasure of its lead.
- **Email at most once.** `send_email` cannot be one transaction — a relay holds no lock and cannot
  be rolled back — so it is two: the first performs the same locking read and a locking check of
  the run row, then commits a run-step row with outcome `sending` (unique on `run_id, position`, so
  a second executor cannot start the same send); only after that commit is `Mailer.Send` called,
  once, with the staff user's current address; the second marks the row `succeeded` or `failed`
  (`mail_failed`) and advances the run conditionally. A fault after the send leaves the row in
  `sending`, and a row found in `sending` — by the next execution or by the boot reconciliation —
  is failed with `email_unconfirmed` together with the run; the mail is never sent again. A run
  cancelled between the two halves stays cancelled with its row finalized.
- **Boot reconciliation, single replica.** `AutomationEngine.Reconcile` runs in `setupDependencies`
  before the worker and the HTTP server start, and only when `AUTOMATION_ENABLED` is true: a run
  left in `running` goes back to `pending` at its `next_position`; one whose last step row is a
  `send_email` in `sending` is failed as above. That is only safe because the worker assumes a
  single API replica: no live executor owns the runs it touches, and there is no lease timeout on
  `claimed_at`. A process started with the worker off owns no run and therefore reconciles
  nothing — a restart of such a replica beside the worker replica would otherwise reset the runs
  the worker is executing (their next conditional advance would fail and the step would be
  repeated) and fail an email that is mid-send. A second replica with the worker enabled would
  double-execute; multi-replica leases are a roadmap item.
- **Waits and schedules survive a restart.** A wait step advances the run to `waiting` with
  `resume_at = now + duration` (written in UTC, compared with a UTC parameter, so SQLite's text
  timestamps order correctly) and `next_position` after the wait; the worker claims it at the first
  tick at or after `resume_at`. A schedule rule makes one pass per UTC day, starting at the first
  tick at or after `hour_utc` — a process that was down at the hour catches up later the same day;
  a UTC day missed entirely is not backfilled — handing out at most 500 live matching records per
  tick in id order from the persisted cursor, with the dedupe key
  `sch:<rule_id>:<YYYY-MM-DD>:<entity_id>`; a batch shorter than 500 sets `schedule_completed_on`.
- **Loop prevention.** `assign_owner` records its event with `origin = automation`; the dispatcher
  claims such an event and starts nothing. A rule holds at most 20 steps and at most 90 days of
  waits, and a condition's `else_position` only jumps forward, so a run executes a bounded number
  of steps.
- **Rule changes cancel runs.** Every update, in one transaction, locks the rule row, increments
  `version`, replaces the steps and cancels the rule's pending, waiting and running runs with
  `rule_changed`; disable cancels with `rule_disabled`, delete (soft) with `rule_deleted`. A
  running run cancelled this way performs no further step: its next conditional advance affects
  zero rows. Each run records the `rule_version` it started from as a second guard.
- **The drain.** `AutomationRuntime.Stop` cancels the worker context (so the loop claims no further
  run), closes admission, cancels the owned context and waits for every admitted step to release —
  a step transaction in flight finishes or rolls back before `Stop` returns, and nothing is written
  afterwards — then joins the goroutine, all under the one deadline main passes to the combined
  handle (`aeoDrainTimeout`, 15 s, shared with the form retention, bulk operation sweep and AEO
  drains that run after it). A drained
  runtime holds no goroutine, and a repeated `Stop` is a no-op that returns nil
  even on a context that has already ended: the join and the admission drain check their
  "already done" state before they look at the deadline. The same crash policy as AEO applies when
  the deadline passes: the process exits without closing the database and the next boot reconciles.
- **Erasure hooks.** See *Deleting personal data* above: lead erasure cancels the lead's runs and
  purges its notifications before the scrub; user erasure purges the user's notifications; the
  erasure of a deal's customer or lead purges the notifications about that deal (#245) and leaves
  its runs running, so later steps render the redacted title.
- **Tickets (#242).** `ticket` is the third entity type, with `ticket_created` (`to_value` the
  stored status), `ticket_updated` and `ticket_status_changed` (`from_value`/`to_value`), and the
  `schedule` trigger over `filters.ticket_status` (a `TicketStatus`, refused on lead and deal
  rules, while `filters.status`/`filters.stage` are refused on ticket rules).
  - *Writers.* `TicketService.Create` and `Update` take `WithTicketAutomationEvents(repo,
    txManager)` (wired in `cmd/main.go`). Create inserts the ticket and `ticket_created` in one
    `WithTransaction`. Update, in one transaction, locks the ticket row first
    (`ticketRepository.GetByIDForUpdate`, through the optional `repository.TicketLockingReader`
    interface so the hand-written mocks keep compiling), runs the closed-reopen and assignee checks
    against the row that locking read returned (the assignee lookup is transaction-bound), saves,
    and records `ticket_updated` plus `ticket_status_changed` when the status differs from the
    locked row's; a priority, assignee or `due_at` change alone records `ticket_updated` only. Both
    carry origin `user` and `actor_id` NULL (the `TicketService` interface has no actor). Without
    the option both methods behave as before: no transaction, no event, the same errors. The bulk
    ticket status batch reads its tickets with `BulkStatusRepository.GetTicketsByIDs`, now a locking
    read ordered by id (`FOR UPDATE` on MySQL), and records the events from the locked statuses
    with origin `bulk` and the caller as `actor_id`. A refused or failed write records nothing.
  - *Rules.* The owner of a ticket run is `assigned_to_id` (an unassigned ticket makes an
    `owner` recipient `recipient_unavailable`). Condition fields are `status`, `priority` (strings)
    and `owner_id`, `customer_id` (numbers). Notify and email placeholders are `ticket.id`,
    `ticket.number` (`models.TicketNumber`), `ticket.status`, `ticket.priority` and `owner.*`; task
    placeholders the four `ticket.*` ones. The title, description, resolution and every customer
    field are refused: ticket text is free text that may name the customer, and customer erasure
    keeps tickets and their runs, which is safe only because nothing customer-related is rendered
    or compared. An `assign_owner` user on a ticket rule must be support or admin
    (`unknown_reference` at `steps[i].config.user_id`, answered `INVALID_REFERENCE`); other staff
    may still be notify or task recipients by `user_id`.
  - *Execution.* `lockRecord` reads the ticket with `AutomationRepository.GetTicketForUpdate` (bare
    row, `FOR UPDATE` on MySQL, soft-deleted is not found); the dry run with `GetTicket`.
    `create_task` links the ticket's `customer_id` only (tasks have no ticket column). `assign_owner`
    re-checks the target at run time (`resolveAssignTarget`: live, active, support or admin, else
    `skipped`/`recipient_unavailable`), writes only `assigned_to_id` and `updated_at`
    (`SetTicketAssignee`) and records `ticket_updated` with origin `automation`, which starts no run.
    A ticket schedule batch is `ListScheduleCandidateIDs` over live tickets with the optional status
    and `updated_at < StaleBefore`.
  - *Delete, not erasure.* Tickets have no erasure of their own. `ticketRepository.Delete` is one
    transaction (a savepoint under `WithTx`) that locks the live ticket row (a missing or deleted
    ticket is `gorm.ErrRecordNotFound`, which the service answers as 404), hard-deletes its
    `ticket_mail_deliveries`, cancels its pending, waiting and running runs with `record_deleted`
    and `finished_at` and hard-deletes the notifications about it (`cancelAutomationForEntity`), then
    soft-deletes the ticket; each table is probed with `HasTable`, and any failure rolls all of it
    back. A run the dispatcher starts later from an event recorded before the delete is cancelled
    at its first step. Customer erasure does not touch tickets or their runs.
  - *Lock order.* The ticket row is locked first by every ticket writer (update, bulk batch, delete)
    and by every step, before the run row, so writers and steps queue on that row and neither
    ordering ends in error 1213 on MySQL.
  - *Visibility.* Unchanged: admins see ticket runs; the sales run scope matches owned leads and
    deals only, so a sales user's list holds no ticket run and its read is 404; support and
    customers keep 403 on the run routes. The list filters and both dry-run bodies accept
    `entity_type=ticket`; any other filter value is 400 `entity_type must be lead, deal or ticket`.
    The web UI builds ticket rules since #312: the frontend allowlists in
    `gocrm-ui/src/types/automation.ts` (`AUTOMATION_TRIGGERS.ticket`, `AUTOMATION_CONDITION_FIELDS`,
    `AUTOMATION_PLACEHOLDERS`, `AUTOMATION_TASK_PLACEHOLDERS`, `AUTOMATION_TICKET_STATUSES`,
    `AUTOMATION_TICKET_PRIORITIES`) mirror the sets above, the form sends a schedule filter as
    `filters.ticket_status`, and a server refusal of `trigger.filters.ticket_status` maps to the
    Only status field. Roots: `ruleFormSchema.ticket.test.ts`, `AutomationRuleForm.ticket.test.tsx`,
    `DryRunDialog.ticket.test.tsx`, `automationFormat.ticket.test.ts`,
    `AutomationRuleList.ticket.test.tsx`, `AutomationRunList.ticket.test.tsx`
    (`gocrm-ui/src/pages/automation/`) and `notificationTarget.ticket.test.ts`; the browser spec
    `gocrm-ui/e2e/tests/automation-ticket-rules.spec.ts` proves the create/edit round trip against
    the real API. The `assign_owner` picker still lists every staff user; a sales user is refused
    by the API (`unknown_reference`) and the refusal is shown on the picker.
  - *Logs.* The ticket write, delete, dispatch, step and dry-run lines carry ids, statuses, outcomes
    and error codes; never the ticket's text, the customer's name or email, or rendered text.
- **Dry run.** `AutomationEngine.DryRun` reads the record and the users with plain reads and
  simulates every step in memory — waits advance a simulated clock, a simulated assign feeds the
  conditions and recipients after it — and reports ids, outcomes and branches. It changes no row,
  calls `Mailer.Send` zero times and creates no run, notification, task or event. The draft dry
  run (`AutomationService.DryRunDraft`, `POST /automation/rules/dry-run`) hands an unsaved rule
  body through `validateRule` — the normalisation and every check of a create, the staff-user
  lookups included — and then to the same `DryRun`; it starts no transaction, takes no lock and
  saves nothing (no temporary rule row), and the report carries `draft: true`.
- **Rule validation.** `validateRule` (`internal/service/automation_validation.go`) collects every
  refusal in one pass into a `*RuleValidationError`: each `{field, code, message}` goes to
  `error.details.fields`, while `Error()` and `Unwrap()` are those of the first refusal exactly as
  the single-error validator produced it (`step N: ...: validation failed`, or
  `user N is inactive: assignee not found` for a reference), so the message and the
  `INVALID_INPUT`/`INVALID_REFERENCE` code are unchanged for clients that read only the message.
  Order is fixed: an unknown `entity_type` stops, an unknown `trigger_type` skips the trigger
  checks, `from` is checked before `to`, no steps or more than 20 skip the step checks, a field
  keeps only its first refusal, and the staff-user lookups run last, once per distinct user, in
  the order the steps first name them (they used to iterate a map, so the first message of a body
  naming two bad users was not stable before).
- **Rule names on runs.** Runs are readable by sales users and rules only by admins, so
  `AutomationRun.Rule` is `json:"-"` and the run reads (`ListRuns`, `GetRunByID`) fill
  `rule_name`/`rule_deleted` with one extra query over the page's distinct rule ids,
  soft-deleted rules included. Only the name crosses: sales users see the names of the rules that
  ran on their records, also after a rule is deleted, so rule names must not carry personal or
  confidential text.
- **Logs.** The engine logs operation names, `run_id`, `rule_id`, `event_id`, `entity_type`,
  `entity_id`, `position`, `step_type`, `outcome` and `error_code` only; a transport error, which
  may quote the address, is not logged.

Regression roots, all on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestAutomationLeadEvents{SQLite,MySQL}`, `TestAutomationDealEvents{SQLite,MySQL}`,
`TestAutomationDispatch{SQLite,MySQL}` (two dispatchers racing for one event),
`TestAutomationRuleLifecycle{SQLite,MySQL}` (a fault injected during the step replacement),
`TestAutomationStepExecution{SQLite,MySQL}` (the MySQL root pauses a step after its first read
through the engine's instance-local seams and commits a cancel, a rule change and an erasure from
a second connection), `TestAutomationConditionBranching`,
`TestAutomationTaskPlaceholdersExcludePersonalData` (a personal placeholder is refused in a task
and blanked by the task renderer), `TestAutomationWaitResume{SQLite,MySQL}`
(a fresh engine on the same database after the clock passes `resume_at`),
`TestAutomationScheduleTrigger{SQLite,MySQL}` (1,201 records over three ticks, a racing worker),
`TestAutomationDryRunNoSideEffects{SQLite,MySQL}` (a before/after snapshot of every table),
`TestAutomationDraftDryRunNoSideEffects{SQLite,MySQL}` (the snapshot plus an instrumented pool and
GORM callbacks that refuse any transaction start, write or locking read during the draft),
`TestAutomationDraftDryRunValidatesLikeCreate`, `TestAutomationRuleValidationCollectsAllFields`,
`TestAutomationRuleValidationLegacyMessage`, `TestAutomationRuleValidationReferencePaths`,
`TestAutomationReconcile{SQLite,MySQL}`, `TestAutomationRuntimeDrain`,
`TestAutomationLogsOmitPersonalData` (`internal/service`); `TestAutomationRunListScope{SQLite,MySQL}`,
`TestAutomationRunRuleNames{SQLite,MySQL}`,
`TestAutomationErasure{SQLite,MySQL}` (the MySQL root reproduces the paused-step deadlock at the
repository level) (`internal/repository`); `TestAutomationRunServiceRoles`;
`TestAutomationRuleAPISQLite`, `TestAutomationRunAPISQLite`, `TestNotificationAPISQLite` (`tests`);
`TestAutomationRuntimeLifecycle` (`cmd`, the real wiring over a SQLite file: the worker replica
reconciles and runs, a replica with the worker off leaves a stranded run untouched);
`TestMigrationAutomationSchema` and `TestMigrationAutomationTablesListed` (`cmd/migrate`). The MySQL
roots skip without the variable and fail when it points at an unreachable server.

Ticket roots (#242), on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestAutomationTicketEvents{SQLite,MySQL}`, `TestTicketServiceWithoutAutomationUnchanged` (SQLite),
`TestAutomationTicketBulkEvents{SQLite,MySQL}`, `TestAutomationTicketRuleValidation`,
`TestAutomationTicketStepExecution{SQLite,MySQL}` (the MySQL root parks a step after its locking
read and sees a delete from a second connection wait in `information_schema.PROCESSLIST`),
`TestAutomationTicketLockOrderMySQL` (an update, a bulk batch and a delete against a step in both
orderings, each wait seen on two consecutive polls, no error 1213),
`TestAutomationTicketSchedule{SQLite,MySQL}`, `TestAutomationTicketDryRunNoSideEffectsSQLite`,
`TestAutomationTicketLogsOmitPersonalData` (`internal/service`);
`TestTicketDeleteCancelsAutomation{SQLite,MySQL}` (an injected failure in each statement, a schema
without the automation tables), `TestAutomationTicketRepository{SQLite,MySQL}` (captured
statements) (`internal/repository`); `TestAutomationHandlerTicketEntity` (`internal/handler`);
`TestAutomationTicketAPISQLite` (`tests`); `TestTicketAutomationProductionWiring` (`cmd`).
Registering the MySQL roots in `scripts/ci/check-go.py` is an operator follow-up, as is the
`AutomationScheduleFilter` `ticket_status` row of the closed-set table in
`scripts/ci/check-api-docs.py`.

### Form retention purge

Spam and never-confirmed submissions hold visitor values and IP addresses. With
`FORM_RETENTION_DAYS` set, `service.FormSubmissionPurger` hard-deletes those rows and their
confirmation tokens once they pass that age. `cmd/form_retention_runtime.go` builds nothing while
the setting is off, so no goroutine runs and no row is read.

- **What goes.** A `spam` or `pending` row with a null `lead_id` and `created_at` before the cutoff,
  with all its tokens and its `form_mail_deliveries` rows (#216), soft-deleted rows and rows of
  soft-deleted forms included. A pending row that still holds a spendable token stays until the
  token is spent or expires; a spam row cannot be confirmed, so a token does not protect it. Each
  confirmation retry (see *Form mail delivery*) re-arms a 48 h token, which keeps that pending
  submission out of the `FORM_RETENTION_DAYS` purge until the token is spent or expires.
  `received` and `confirmed` rows and lead-linked rows are never touched: lead erasure owns the
  latter, and identity erasure scrubs unlinked rows of an erased address in place (#214).
- **The floor.** Config raises 1 and 2 days to 3. `Sweep` also returns `ErrFormRetentionTooShort`
  before reading or writing anything for a retention below 72 hours, the 48-hour link lifetime plus
  a margin, so a wiring mistake cannot delete.
- **First run.** The runtime sweeps immediately on start and then every
  `service.FormRetentionSweepInterval` (24 hours). The first boot with the setting therefore removes
  the whole existing backlog, permanently, without any request. Take a backup first if you may want
  those rows.
- **Clocks.** `created_at` comes from GORM's `NowFunc`, which is UTC in production, and is compared
  with a UTC cutoff. `expires_at` is written from the process's local clock and compared with that
  same clock, unconverted: SQLite compares timestamps as text, so the two sides must share a zone.
- **Chunks.** `Sweep` walks spam and then pending by ascending id, at most 500 rows per
  `WithTransactionAndRetry` transaction. A chunk (`FormRepository.PurgeSubmissionChunk`) reads its
  candidates and token ids with plain reads, locks the tokens by primary key, locks the submissions
  by primary key, re-checks the rule on what those locking reads return, and only then deletes
  tokens, the purged submissions' delivery rows (read with a plain read, deleted by primary key and
  counted in `FormSubmissionPurgeChunk.Deliveries`) and submissions. Locking by primary key keeps the locking reads off the secondary indexes, whose
  next-key locks would block a new submission's insert; the deletes still reach those indexes (see
  *Re-submissions*). A failure rolls back that chunk only, and
  the next sweep picks up what is left. The walk never selects a row twice, so a row that survives
  the re-check cannot keep a sweep busy.
- **Lock order.** Token first, submission second, the order a confirmation and a confirmation
  retry use; delivery rows are deleted only after both locks. A confirmation that
  claimed its token makes the chunk wait; the chunk then sees the row confirmed and leaves it. A
  confirmation that read its token before a purge finds nothing to claim and gets
  `ErrInvalidConfirmationToken`, so it can never reach `UpdateSubmission`'s `Save` and restore the
  row. Two sweeps at once are safe: the loser of a chunk locks nothing that is left and counts
  nothing.
- **Re-submissions.** `storePendingSubmission` spends the older tokens of the same form and address
  inside its transaction, which deadlocks with a chunk removing one of those older rows. This was
  observed, not theoretical: on MySQL 8.0 the forced purge-first ordering deadlocked 9 times out of
  9 at repository level and InnoDB rolled back the re-submission. The purge's primary-key locks do
  not cover the `deleted_at` index, so holding them does not keep the re-submission out of the way
  of its deletes. The function therefore now runs under `WithTransactionAndRetry`. Each attempt
  inserts its row with a fresh id and mints a fresh token, and the confirmation mail goes out once,
  after the commit. `storeReceivedSubmission` and `recordSpam` also insert rows but are not
  replayed, and what a purge costs them is unmeasured; `ConfirmSubmission` has no retry either.
  MariaDB was not probed for any of this.
- **The drain.** `FormRetentionRuntime.Stop` cancels the sweep's context, so a chunk in flight rolls
  back and no further chunk starts, then joins the goroutine under `main`'s shared deadline. After a
  `Stop` that returned nil no row changes. A deadline that passes first returns an error, with no such
  guarantee, and `main` then skips closing the database as it does for the other runtimes. The purge
  drains after the automation worker and before the stale bulk operation sweep and AEO:
  `combineStops(automation.Stop, formRetention.Stop, bulkRecovery.Stop, aeoRuntime.Stop)`.
- **Replicas.** The purge is safe on every replica, unlike the automation worker: extra sweeps only
  compete for the same rows.
- **Logs.** A sweep logs `spam_removed`, `pending_removed`, `tokens_removed` and `chunks`, and a
  failure adds the error through `utils.LogError`. The boot line `form retention purge enabled`
  carries `retention_days` alone. The purge's statements bind ids and timestamps only.

There is no new index: selection reads `status`, `lead_id` and `created_at` without a lock, so a
large table costs read time, not lock time.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestFormSubmissionPurge{RemovesEligible,KeepsIneligible}{SQLite,MySQL}` (`internal/repository`);
`TestFormSubmissionPurge{BacklogChunks,ChunkAtomic,ConcurrentConfirmation,CompetingSweeps,
LateConfirmationLink,CutoffClockBasis,ConcurrentResubmission}{SQLite,MySQL}`,
`TestFormSubmissionPurgeRefusesShortRetention`, `TestFormSubmissionPurgeLogsOmitPersonalData`,
`TestFormRetentionRuntimeLifecycle` and `TestFormRetentionRuntimeStopDuringChunk`
(`internal/service`); `TestFormRetentionAPISQLite` (`tests`); `TestFormRetentionProductionWiring`
(`cmd`, which parks a chunk to prove the drain handle joins it); `TestLoadFormRetentionDays` (`internal/config`). The MySQL concurrency roots confirm each
wait in `information_schema.PROCESSLIST` on two consecutive polls; SQLite's single connection cannot
overlap two transactions, so its roots prove the same outcomes in sequence. CI limit: the MySQL roots
are discovered through `form*_test.go` by `scripts/ci/check-go.py transaction`, which CI runs only on
the transaction, bounded and full profiles (bounded and full also on MariaDB 10.11), while
`make verify-backend` has no DSN and skips them, so a `contextual` PR's CI does not execute them.

### Form mail delivery (#216)

Every form mail is recorded: the double-opt-in confirmation, the follow-up and one notification per
`NotifyEmails` address each get a `form_mail_deliveries` row (`models.FormMailDelivery`). The code
lives in `internal/service/form_mail_delivery.go` and `internal/repository/form_mail_delivery_repository.go`.

- **Written with the submission.** The rows are inserted `pending` in the transaction that stores a
  received or pending submission, or that confirms one, so spam and a rolled-back transaction leave
  no row. A notification row is written for every non-blank address. Submissions stored before this
  change have no rows (no backfill).
- **Claimed, sent, stamped.** After the commit each row is claimed with a conditional update
  (`ClaimPendingDelivery`: `pending` → `sending`, `attempts + 1`, `last_attempt_at`), `Mailer.Send` is
  called once, and the outcome is stamped by `FinishDelivery`, which matches only a `sending` row:
  `sent` (accepted by the relay, `sent_at` set), `failed` with `error_code` `mail_failed`, `timeout`
  (`os.ErrDeadlineExceeded`) or `invalid_recipient` (`mailer.ErrInvalidAddress`,
  `mailer.ErrInvalidHeader`), `log_only` when the mailer implements `mailer.LogOnlyReporter` and
  reports `LogsOnly()` (the `LogMailer`, no `SMTP_HOST`; `sent_at` stays null), or `skipped` with
  `no_mailer` or `no_recipient`. The claim and the stamp each run on their own
  `context.WithoutCancel(ctx)` with a fresh timeout created at that statement (5 s,
  `WithFormMailStampTimeout` per service instance), so a cancelled request or a slow relay cannot
  leave a row `sending`; `Send` itself is bounded only by the mailer. The public submit and confirm
  answers never change: a delivery failure is recorded, not returned. A claim that fails leaves the
  row `pending` and nothing is sent.
- **Stuck rows.** A `pending` or `sending` row whose `updated_at` is more than
  `service.FormMailStuckAfter` (15 minutes) before the service clock is reported `stuck` and becomes
  retryable. `retryable` in the JSON means status-eligible only (`failed`, `log_only` or stuck); a
  retry may still answer 409 or 404 for reasons that depend on the submission, the form or
  `notify_emails`.
- **Reads.** `GET /forms/submissions/{id}` and `GET /forms/{id}/submissions` carry `deliveries` per
  submission (`[]` when there are none). `GET /forms/deliveries` (admin, sales, support) is the
  attention list across forms: `failed`, `log_only` and stuck rows by default, or exactly the stored
  `status` given, filtered by `form_id` and `kind`, id descending, paginated.
- **Retry.** `POST /forms/deliveries/{id}/retry` (admin and sales) calls `FormService.RetryDelivery`,
  a transaction root: one `WithTransactionAndRetry` transaction reads the delivery, locks the
  submission by primary key, reads the form (a soft-deleted form refuses) and ends with
  `ClaimDeliveryForRetry`, a conditional update that only a `failed`, `log_only` or stuck row
  passes. After the commit the message is re-rendered from the current form and sent once to the
  submitter or to the `NotifyEmails` address whose HMAC (`recipient_hash`, never serialised) the
  row stores; an address no longer listed refuses, there is no recipient override. Two concurrent
  retries send once: the loser's claim matches nothing. Answers: 200 with the updated row, 409
  `FORM_DELIVERY_NOT_RETRYABLE` (`apperrors.ErrFormDeliveryNotRetryable`), 404 for an unknown row
  or a vanished submission, 502 `MAIL_DELIVERY_FAILED` when the send fails (the row is stamped
  `failed`). A stamp that finds no row because the delivery was purged or erased meanwhile logs a
  Warn line with ids and returns the send's outcome.
- **Confirmation retry.** Inside the same kind of transaction, after plain reads of the delivery and
  the submission: `InvalidatePendingTokens` for the form and address (token rows first, the
  confirmation's lock order), the submission locked by primary key and re-checked (pending, no
  lead), a refusal when the form has a newer non-spam submission of the same email (a locking read
  on MySQL, plain on SQLite), then the submission's single existing token row is re-armed in place
  by primary key (`ReArmConfirmationToken`: new `token_hash`, `expires_at` = now + 48 h, `used_at`
  NULL), and the delivery is claimed. No token row is ever inserted; zero or two token rows refuse.
  The old link stops working, the mailed one confirms.
- **Purge and erasure.** `PurgeSubmissionChunk` deletes the rows of the submissions it purges (see
  *Form retention purge*), and a lead erasure hard-deletes the rows of the lead's submissions right
  after their confirmation tokens (`scrubLeadFormSubmissions`), so a retry of such an id is 404.
  The erasure of a lead, customer or user does the same for the unlinked submissions whose `email`
  matches the erased row's original address (`scrubUnlinkedSubmissions`, #214; see *Deleting
  personal data*).
- **Privacy.** A row holds ids, the kind, the status, an error code, counts and timestamps; no
  address, subject or body. Delivery log lines carry `form_id`, `submission_id`, `delivery_id`, the
  `mail` kind, `status` and `error_code`, plus the error through `utils.LogError`.
- **Not done.** No automatic or background retry, no backoff, no bounce or provider confirmation.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestFormMailDeliveryRecorded{SQLite,MySQL}`, `TestFormMailDeliveryRetry{SQLite,MySQL}`,
`TestFormMailDeliveryConfirmationRetry{SQLite,MySQL}`, `TestFormMailDeliveryErasedWithLead{SQLite,MySQL}`,
`TestFormMailDeliveryLogOnlyRecorded`, `TestFormMailDeliveryStuckRows` and
`TestFormMailDeliveryLogsOmitPersonalData` (`internal/service`);
`TestFormMailDeliveryRepository{SQLite,MySQL}` and `TestFormMailDeliveryPurgedWithSubmission{SQLite,MySQL}`
(`internal/repository`); `TestFormDeliveryHandlerErrorMapping` and `TestFormDeliveryRoutesRoles`
(`internal/handler`); `TestFormMailDeliveryAPISQLite` (`tests`); `TestFormMailDeliveryJSON`
(`internal/models`); `TestLogMailerReportsLogOnly` (`internal/mailer`); `TestFormMailDeliveriesMigration`
and `TestFormMailDeliveriesMigrationFiles` (`cmd/migrate`, migration
`20261106120000_add_form_mail_deliveries`). CI limit: as for the purge, the MySQL roots skip in
`make verify-backend`, which has no DSN.

### Form attribution and the form-submitted event

Each finalised enquiry (#220) keeps its own attribution on its `form_submissions` row, apart from
the lead's `source`, and a `lead_form_submitted` automation event lets rules route each enquiry.
The parsing lives in `internal/service/form_attribution.go`.

- **Six columns.** `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content`
  (`varchar(100)`) and `referrer_host` (`varchar(255)`), all `NOT NULL DEFAULT ''`, serialised as
  strings, empty when absent. No index, no backfill: rows stored before this change read empty.
- **Parsing is deterministic and minimal.** For each `utm_*` key a non-blank submitted field of that
  name (a hidden input the form declares) wins; otherwise the first value of that query parameter of
  an absolute `http`/`https` `page_url` is used. Values lose invalid UTF-8 and control characters,
  are trimmed, lower-cased and cut to 100 runes. `referrer_host` is the lower-cased host of such a
  URL without port, credentials, path or query, cut to 255 characters. Anything else (another
  scheme, a relative or unparsable URL) gives empty strings. The page URL used is the stored
  `referrer` (cut to 512 characters), so a confirmation parses exactly what arrived.
- **Where it applies.** `storeReceivedSubmission` sets the columns before the insert, and
  `confirmInTransaction` sets them when the confirmation finalises the row; a pending row stays
  empty until then. `newSubmission` does not, because `recordSpam` shares it: a spam row stores
  empty attribution whatever its fields and `page_url` carried. `leads.source` keeps the form name
  the lead was created with, and the `utm_*` lines still go into the lead's notes.
- **The event.** When a finalised submission of a `create_lead` form is linked to a lead,
  `applySubmissionLead` records `lead_form_submitted` (entity `lead`, origin `form`, nullable
  `automation_events.form_id` and `submission_id` set) right after the `lead_created` (new lead)
  or `lead_updated` (repeat enquiry) event, in the same transaction as the link, so a rollback
  records neither. Spam, pending and `create_lead`-off submissions record none. The event carries
  ids only.
- **Dispatch and validation.** `lead_form_submitted` is a lead trigger only; a deal rule on it is
  `trigger_type` `invalid_value`. `trigger.form_id` (0 or omitted: every form) narrows the rule to
  one form in `triggerMatches` and is `not_applicable` on any other trigger. The six attribution
  names are string condition fields of a lead rule on `lead_form_submitted` and `not_applicable`
  everywhere else; the draft dry run runs the same `validateRule`.
- **Evaluation.** A condition on an attribution field reads the submission named by the run's
  triggering event (`AutomationRepository.GetEventFormSubmission`), inside the step transaction
  after the lead's locking read, and uses it only when its `lead_id` is the run's lead; otherwise,
  and in every dry run, the fields read empty. Two enquiries to the same lead therefore branch on
  their own values.
- **Erasure and logs.** The lead erasure empties the six columns on every linked row, live and
  soft-deleted (`scrubLeadFormSubmissions`), and the erasure of a lead, customer or user empties
  them on the unlinked rows of the erased address (`scrubUnlinkedSubmissions`, #214); the event
  rows keep their ids. No log line of the
  submission, confirmation, dispatch or step paths carries an attribution value, the host or the
  page URL.
- **Schema.** `MigrateDatabase` adds the eight columns on SQLite; MySQL and MariaDB get them from
  `20261107120000_add_form_attribution`, which adds each one only when missing.
- **Not done.** No first- or last-touch attribution on the lead, and the embed script does not fill
  hidden `utm_*` inputs from the host page's URL.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestFormAttributionParse`, `TestFormSubmissionAttribution{SQLite,MySQL}`,
`TestFormSubmittedEvent{SQLite,MySQL}`, `TestAutomationFormSubmittedDispatch{SQLite,MySQL}`,
`TestAutomationFormTriggerValidation`, `TestAutomationAttributionConditions{SQLite,MySQL}`,
`TestAutomationDryRunAttributionEmpty` (SQLite) and `TestFormAttributionLogsOmitValues`
(`internal/service`); `TestFormAttributionErasedWithLead{SQLite,MySQL}` and the identity sweep
`TestIdentityErasure{SQLite,MySQL}` (`internal/repository`);
`TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates` and
`TestLeadErasureLeavesNoPersonalDataInAnyTable` (`test/integration`); `TestFormAttributionAPISQLite`
(`tests`); `TestFormSubmissionAttributionJSON` and `TestFormAttributionSQLiteUpgrade`
(`internal/models`); `TestFormAttributionMigration` and `TestFormAttributionMigrationFiles`
(`cmd/migrate`). CI limit: as for the purge, the MySQL roots skip in `make verify-backend`, which
has no DSN.

### AEO runtime validation and candidate inspection

Bounded AEO backend/runtime work uses `python3 scripts/ci/check-go.py aeo` with
build/vet, exact uncached race roots and mandatory SQL availability, plus health/provider
browsers and migration gates. Startup and shared-interface exceptions require committed
scope proofs and six lifecycle roots; other application changes keep their fallback.
See [validation policy](VALIDATION_RELEASE.md) for scope and CI engines. A fresh runner
policy may enable `validation_review_required` to hold after commit and before checks.
Inspect the actual diff and complete command plan, then use `confirm-validation`; a
comment-only `.env.example` edit needs the shared committed proof to retain a bounded
profile; changing active settings or unsupported syntax retains full fallback. A
changed candidate needs another inspection. See [operations](../automation/langflow/OPERATIONS.md).

For changes confined to `internal/mailer/smtp.go` and mailer package tests, `python3 scripts/ci/check-go.py mailer`
requires the SMTP deadline/header/TLS/privacy roots and selected password-reset/form/quote/automation delivery
callers, then runs API build, vet and uncached race checks. Local browser coverage is SQLite
health smoke; CI retains all engine health and migration gates. No SMTP account or external
relay is needed by these tests. See [validation policy](VALIDATION_RELEASE.md) for the exact
scope; service, schema and runtime-setting changes are outside this bounded profile.
