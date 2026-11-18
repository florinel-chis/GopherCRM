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
`middleware.RequirePermission()` for RBAC on routes. An API key whose owner has been erased or deactivated
is rejected at authentication time, and none is issued to such an owner (#342, see *Credential
issuance under the owner lock*).

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
tokens locks the `users` row first and the `refresh_tokens` rows second: the login (owner, then
the last-login write, then the refresh insert, #336), the reset (reset token, owner, refresh
tokens), the rotation (owner, then the claim), the password change (owner, then the claim, the
revocation and the successor) and the logout (owner, then the revocation, then the
`token_version` increment, #236). `refresh_tokens.user_id` references `users` with a
foreign key, so on MySQL/MariaDB every successor insert also takes a shared lock on the owner row;
a rotation that claimed its token before locking the owner deadlocked against a password change
(error 1213). The credential-issuing transactions follow the same owner-first rule (#342): the
password-reset request locks the owner, then spends and inserts `password_reset_tokens` (users →
password_reset_tokens), and API-key create locks the owner, then inserts into `api_keys` (users →
api_keys). The reset confirm (token first, then owner) inverts the request's order; see
*Credential issuance under the owner lock* below. New session and credential flows must follow
the same order (see [CONCURRENCY.md](CONCURRENCY.md)).

**Credential issuance under the owner lock (#342)** — a password-reset token and an API key are
issued only to a live, active owner.

- **Reset request.** `RequestPasswordReset` keeps its plain `GetByEmail` (an unknown or inactive
  address stops there), then runs one `WithTransactionAndRetry` transaction
  (`resetRequestMaxRetries` 3) on `context.Background()` that reads the owner by primary key
  through the user repository bound to `tx.Clauses(clause.Locking{Strength: "UPDATE"})` on a fresh
  `Session` (`lockLiveCredentialOwner`; `FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite).
  A missing, erased (soft-deleted) or inactive locked row writes nothing. Otherwise
  `InvalidateAllForUser` and `Create` run through `PasswordResetTokenRepository.WithTx`; a failure
  of either rolls both back. `SendPasswordReset` is called once, after the commit, with the address
  of the locked row. The method returns nil in every case (unknown, inactive, refused, rolled back,
  unwired, mail failure), so `POST /auth/password-reset` keeps its one 200 answer. A service
  without a transaction manager now writes and sends nothing.
- **API-key create.** `NewAPIKeyService(apiKeyRepo, userRepo, txManager, apiKeySecret)`;
  `cmd/main.go` passes the shared `userRepo` and `txManager`. `Generate` runs one
  `WithTransactionAndRetry` transaction (`apiKeyCreateMaxRetries` 3) on `context.Background()`
  with the same owner lock and inserts through `apiKeyRepo.WithTx(tx).Create`. A refused owner
  returns an error matching `service.ErrAccountUnavailable` (no row, no key), which `POST /api-keys`
  answers 401 `UNAUTHORIZED` `Unauthorized`; without the user repository or the manager it returns
  `service.ErrAPIKeyIssuanceUnavailable` and writes nothing (500, like every other failure).
- **Outcome.** An erasure (user- or customer-rooted) or a deactivation that commits before the lock
  makes the issuance write nothing; one that commits after it hard-deletes the token or key
  (erasure) or leaves it unusable (`ConfirmPasswordReset` and `ValidateAPIKey` refuse an inactive
  owner).
- **Reset request vs confirm.** The request locks users → password_reset_tokens, the confirm
  password_reset_tokens → users. Confirm first: the request waits on the users row and issues a
  fresh token. Request first: the confirm waits on the token row and its claim is refused
  (`ErrInvalidResetToken`). Both holding their first lock: MySQL ends one with error 1213; a
  request victim is replayed by the retry helper, a confirm victim (plain `WithTransaction`)
  returns the error with nothing written. Putting the confirm under the retry rule is a follow-up.
- **Accepted residual.** A reset request for an account whose users row another transaction holds
  waits for that lock, which a timing observer could tell apart from an unknown address. Neither
  path moves onto the request context (the interfaces stay context-free).
- **Logs.** The refusal, rollback and unwired lines carry `user_id`, `operation`
  (`RequestPasswordReset`, `GenerateAPIKey`) and `error_code` only, plus the error through
  `utils.LogError`; never the address, a name, the token or its hash, the key, its prefix or its
  name.

Roots (`internal/service/credential_issuance_owner_lock_test.go`):
`TestPasswordResetRequestOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
`TestPasswordResetRequestOwnerLockConcurrentMySQL`, `TestPasswordResetRequestVsConfirm{SQLite,MySQL}`,
`TestAPIKeyCreateOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
`TestAPIKeyCreateOwnerLockConcurrentMySQL` and `TestCredentialIssuanceOwnerLockLogsOmitPersonalData`;
`TestAPIKeyCreateOwnerUnavailableMapping` (`internal/handler/apikey_owner_lock_test.go`);
`TestCredentialIssuanceOwnerLockAPISQLite` (`tests`); `TestAPIKeyOwnerLockProductionWiring` (`cmd`).
On SQLite a competitor commits through the instance-local seams `resetBeforeLock` and
`apiKeyService.beforeLock`, between the lookup and the transaction, or after the issuance; the
MySQL roots run on a disposable `TEST_MYSQL_DSN` schema built by the real migrations, see each
wait in `information_schema.PROCESSLIST` on two consecutive polls and assert that no ordering
outside the interleaved request/confirm case ends in error 1213. The acceptance run sets
`TEST_MYSQL_DSN`; `make verify-backend` has no DSN and skips them. Registering the new roots in
`scripts/ci/check-go.py` and `scripts/ci/check-auth.py` is an operator follow-up.

**Login issuing transaction (#336)** — on a service built by `NewAuthServiceWithSessions`,
`LoginWithTokens` and `Login` keep the plain `GetByEmail` and bcrypt (against a dummy hash for an
unknown address) before any transaction, so the timing, the generic errors and the failure
bookkeeping of a wrong password (one `UpdateFields` outside the transaction) are unchanged. A
verified password then starts one `WithTransactionAndRetry` transaction (`loginMaxRetries` 3) that
locks the owner row by primary key (`FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite) and
decides on that row only: a hash other than the one bcrypt verified, `is_active` false or an
erased (soft-deleted) row is the generic `invalid credentials`, and a `locked_until` after the
login clock is the lockout error; each refusal rolls back with nothing written. Otherwise one
`UpdateFields` writes `last_login_at` and clears `failed_login_attempts`/`locked_until`, the
refresh token is inserted under a savepoint (`LoginWithTokens` only; `Login` never issues one) and
the access JWT is signed from the locked `token_version`. A non-retryable failure of the insert
rolls back to the savepoint and the login answers an access token with an empty refresh token
(the documented JWT-only fallback, logged with `error_code=refresh_token_insert_failed`); a
retryable one (deadlock, lock wait, SQLite busy) comes back tagged `utils.ErrNestedRetryDeferred`
and the whole root is replayed; a signing failure rolls everything back. A password change, reset,
deactivation or erasure that commits before the lock therefore refuses the login, a logout lets it
through with the incremented `token_version`, and one that commits after it ends the session it
issued the usual way. The log lines of the transaction carry `user_id`, `operation` and
`error_code` only. A service built by `NewAuthService` (no transaction manager) keeps the legacy
unlocked statements and issues no refresh token. `POST /auth/login` still answers every error
with 401 `Invalid email or password`. Roots: `TestLoginOwnerLockDecisions{SQLite,MySQL}`,
`TestLoginOwnerLockAtomicity{SQLite,MySQL}`, `TestLoginConcurrentCredentialChanges{SQLite,MySQL}`
(login against a password change, a reset confirmation, `UserService.Update(is_active=false)`,
`UserService.Delete` and a logout, in both orderings), `TestRefreshRotationConcurrentCredentialChanges{SQLite,MySQL}`
(rotation against a reset, a deactivation and an erasure, in both orderings),
`TestLoginOwnerLockLogsOmitPersonalData` and `TestLoginUnwiredServiceUnchanged` in
`internal/service/auth_login_owner_lock_test.go`, and, over HTTP, `TestLoginOwnerLockAPISQLite`
(`tests/auth_login_owner_lock_test.go`). On SQLite the competing write runs through the
instance-local `loginBeforeLock` seam, between the lookup and the transaction; the MySQL roots run
on a disposable `TEST_MYSQL_DSN` schema built by the real migrations, see each wait in
`information_schema.PROCESSLIST` on two consecutive polls and assert that no ordering ends in
error 1213. They skip without the variable and fail when it is unreachable. Registering the new
roots in `scripts/ci/check-go.py` and `scripts/ci/check-auth.py` is an operator follow-up.

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
nothing is written. Refresh rotation and login never change the counter: the rotation and a wired
login (#336) sign the value their locking read returned, and the access token it replaces stays valid until it expires. A
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
re-checked). Login bookkeeping (the failure count, and on a wired service the last-login write
of the issuing transaction), `UpdateLastLogin` (the unwired login), the reset, the password change
and `UserService.Update` all go through it; a login whose account vanished underneath it is
refused (`TestLoginErasedDuringAuthentication{SQLite,MySQL}` in
`internal/service/auth_login_erasure_regression_test.go`). A wired login re-checks the password
hash and the account state under the owner lock before it issues its session (#336, see *Login
issuing transaction*).

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
discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them.

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

**Authentication is header-only** — `middleware.Auth` reads the `Authorization: Bearer` header or
the `ApiKey` header and nothing else. No cookie credential is accepted, so the browser attaches no
credential on its own and CSRF does not apply; the server therefore ships no CSRF protection, and
the unused middleware, token codec and cookie helpers that once sat in the tree were removed. The
frontend keeps its token in `localStorage`/`sessionStorage` and sends it in the `Authorization`
header. `tests/auth_integration_test.go` pins the position: a valid access token sent only in
cookies answers 401. Any future cookie session must ship with session-bound CSRF protection (see
#261).

**Role assignment** — `POST /auth/register` is public and always creates a `customer`; it ignores
any client-supplied role. Elevated roles are only assignable via `POST /users` and `PUT /users/{id}`
by a caller holding `users.assign_role` (the admin system role does), or the `create-admin` CLI. Never add a client-controlled role to a public endpoint. E2E admin accounts
are seeded by `gocrm-ui/e2e/global-setup.ts`, which shells out to `cmd/create-admin`.

**RBAC persistence (#259 S2, #344)** — the storage of custom roles. The permission catalogue and
its enforcement read it since #347; the role API and the governed user writes of #348 (next
paragraph) are the callers of its repository methods.

- **Models and order.** `models.Role`, `RolePermission` and `RoleAuditEntry` (`roles`,
  `role_permissions`, `role_audit_log`, see [datamodel.md](datamodel.md#role-model)) are registered
  in `autoMigrateModels` ahead of `User`, because `users.role_id` references `roles`
  (`fk_users_custom_role`, ON DELETE RESTRICT; plus `idx_users_role` and `idx_users_role_id`). On
  SQLite the foreign key rebuilds `users` under `database.Migrate`; the indexes survive.
- **The JSON.** `User.RoleID` is `json:"role_id,omitempty"`, so a NULL value adds no key and a
  system-role user's answer is unchanged; `CustomRole` is `json:"-"`. Since #348 `role_id` is a
  documented property and `models.RoleCustom` (`"custom"`) is a typed `UserRole` constant, the
  fifth value of the swag enum and of the closed set `scripts/ci/check-api-docs.py` collects by
  type. A `custom` user holds only the permissions of its role row (#347); `POST /users` and
  `PUT /users/{id}` create one (`role = custom` with `role_id`).
- **The seed.** `seedSystemRoles` runs in `MigrateDatabase` after `normalizeCompanyDomains` on every
  start and driver. It reads `SELECT system_key FROM roles WHERE system_key IS NOT NULL` (plain,
  non-locking) and inserts only the missing rows of `models.SystemRoleSeeds` (#259's names and
  descriptions, the same literals as the SQL up) with `ON CONFLICT DO NOTHING`, so a replica booting
  at the same time is not an error (an insert that fails, for example as the victim of a lock
  conflict with that replica, is accepted when a re-read then finds the row). An up-to-date database takes that one SELECT and no write; an
  existing system row is never updated, so its name and description are not rewritten. It logs
  nothing. When a custom role already holds the `name_key` of a missing system row, the start fails
  before writing anything with `seed system roles: roles.name_key "<key>" is held by a custom role;
  rename that role and restart`. Recovery: rename that custom role, for example `UPDATE roles SET
  name = 'Sales team', name_key = 'sales team' WHERE id = <id>`, and restart.
  `models.MigrateDatabaseWith(db)` runs the same steps on an explicit handle (tests of two pools).
- **`RoleRepository`** (`internal/repository/role_repository.go`). `Create` and `Update` trim the
  name, refuse a blank one, one over 100 characters or a description over 500 with
  `apperrors.ErrValidation` before any statement, and set `name_key = models.RoleNameKey(name)`
  (trimmed, lower-cased). `Create` refuses a non-nil `SystemKey` (`ErrValidation`), so
  `idx_roles_name_key` is the only unique index a repository write can hit and a duplicate key is
  `apperrors.ErrRoleNameTaken`. `Update` writes `name`, `name_key`, `description` and `updated_at`
  only, never `system_key`; refusing writes to system rows is the role service's job (owner
  decision 2, `SYSTEM_ROLE_IMMUTABLE`). `ReplacePermissions(roleID, permissions)` locks the role (`FOR UPDATE` on
  MySQL/MariaDB), deletes its rows and inserts the new set in one transaction (a savepoint under
  `WithTx`): a failure after the delete keeps the previous set, an empty slice leaves none, a
  missing role is `gorm.ErrRecordNotFound`. `GetByID` returns the permissions ordered by
  `permission_key`. `LockByIDs(ids)` (#348) locks role rows by primary key in ascending id and reads
  their permissions with a locking read too. `List(offset, limit, sortBy, sortOrder)` returns a page (each role with its permissions) and the total from the
  same predicate, ordered by `utils.AllowedSortColumns["roles"]` (`id`, `name`, `created_at`,
  `updated_at`) and then `roles.id` in the same direction (`orderPage`), or by id ascending; another
  column is an error before any query. `Delete(id)` locks the role, deletes its permissions and then
  the role in one transaction; the foreign-key refusal of a role still referenced by a user, live or
  erased (MySQL 1451, SQLite `FOREIGN KEY constraint failed`, classified by `isForeignKeyRefusal` in
  `foreign_key.go`), is `apperrors.ErrRoleInUse` and rolls the permission delete back.
- **User repository.** `UpdateFields` accepts `role_id` (a value or nil). `GetByID` of a user with
  `role_id` NULL is still exactly one SELECT; a custom-role user also gets `CustomRole` with its
  permissions. The optional interface `repository.UserRoleMaintenance` (the `TicketLockingReader`
  pattern, so the hand-written mocks are unchanged) adds `LockActiveAdmins`, which discovers ids with
  a plain `SELECT id … WHERE role = 'admin' AND is_active AND deleted_at IS NULL ORDER BY id`, then
  locks those rows by primary key only (`WHERE id IN (…) ORDER BY id`, `FOR UPDATE` on MySQL/MariaDB)
  and returns the locked rows that still match, in id order: no locking statement filters on role,
  so no lock is taken through `idx_users_role`. `ClearRoleIDOfErased(roleID)` sets `role_id` NULL on
  the soft-deleted users of that role and returns the count, after which the role can be deleted.
  #348 adds `LockLiveByIDs` (the actor and the target, by primary key in id order),
  `LockLiveRoleHolders` (the live users of a role, a locking read by `role_id`) and
  `CountLiveRoleHolders` (the `user_count` of the role API).
- **Migration.** `20261119120000_add_roles_and_permissions` (#259 planned `20261101120000`, which
  `add_task_deal_link` already holds). Its down half sets `role = 'support'` and `is_active = 0` on
  every live custom-role user before it drops anything (owner decision 8): irreversible for those
  users, so take a backup before rolling back on MySQL/MariaDB. SQLite has no down path: roll back by
  restoring the pre-upgrade backup. An older binary started on the upgraded file would keep the role
  tables and `fk_users_custom_role` (its AutoMigrate drops nothing), and every `custom` user would
  match no role guard of that binary and get 403 on every guarded route.
- **Erasure.** `roles`, `role_permissions` and `role_audit_log` are not touched by the erasure:
  role names carry no personal text by rule, and audit rows hold ids, role names, permission keys
  and scopes only: the writers of #348 store a role's name, description and permissions, or a
  user's `role` and `role_id`, and nothing else. An erased user keeps its `role_id`, which keeps
  the foreign key in place until the role delete runs `ClearRoleIDOfErased`.
- **Collation.** On MySQL/MariaDB `roles.name_key` keeps `utf8mb4_unicode_ci`, so names differing
  only by accents or trailing spaces also collide there; SQLite compares binary. No collation guard
  is added (pending the owner's answer).

Regression roots: `TestRoleSchemaSQLite`, `TestRoleSQLiteUpgradeFromV120`, `TestUserRoleIDJSON`,
`TestRoleNameKey`, `TestRoleFieldsAreInTheAPIReference` (`internal/models`);
`TestRoleRepository{SQLite,MySQL}`, `TestForeignKeyRefusalClassifier`,
`TestSystemRoleSeed{SQLite,MySQL}` (statements captured by GORM callbacks; the MySQL root also
starts two pools at once), `TestUserRepositoryRoleColumns{SQLite,MySQL}` (`internal/repository`);
`TestRoleMigrationFiles` (it held the newest-migration assertion until #345 moved it to
`TestWebhookMigrationFiles`),
`TestMigrationRolesSchema` and `TestMigrationRolesDownDemotesCustomUsers` (`cmd/migrate`). The
SQLite roots use the production connector over a file under `t.TempDir()`; the MySQL roots a
disposable `TEST_MYSQL_DSN` schema, skipped without the variable (the acceptance run sets it and
fails on a skip). Operator follow-up (owner decision 9): register the migration and seed roots in
`.github/workflows/migrations.yml` and the repository roots in `scripts/ci/check-go.py`.

**Role API and governed user writes (#259 S4, #348)** — `internal/service/role_service.go` serves
`GET /permissions/catalogue`, `/roles` (list, create, get, update, delete), `POST /roles/{id}/clone`
and `GET /roles/audit`; `internal/service/user_governed_writes.go` holds the actor-taking user
writes (`CreateAs`, `UpdateAs`, `SetActiveAs`, `DeleteAs`) that `POST /users`, `PUT /users/{id}`,
activate, deactivate and `DELETE /users/{id}` call. What both share is in
`internal/service/role_governance.go`.

- **One transaction per write.** Each write is a `WithTransactionAndRetry` root through the `WithTx`
  repositories that locks the active admin set (user writes only), the actor and the target in
  ascending id, then the custom roles in ascending id, reads all of them again under the locks and
  only then decides; the audit row is inserted in the same transaction. Lock order and retry are in
  [CONCURRENCY.md](CONCURRENCY.md) (flow "RBAC user-role and role writes").
- **The escalation rule** is one function, `checkEscalation`: a permission set exceeds the actor's
  when one of its grants is not covered (`own` is below `all`, a missing key below `own`); a system
  admin is exempt. A role write checks the requested set (create, update, clone) and, on update and
  delete, the role's stored set, so a manager can neither grant above its own permissions nor edit
  or delete a role that is above them. A user write checks the target as it is now and the role it
  is to receive, on the caller's own account too. The `customer` system role stands for the empty
  set there (`escalationSet`): it holds the one system-only key, which no custom role can hold, so
  it never counts as above a manager. A custom role holding `users.create` alone can create a
  customer login, and one holding `users.update`, `users.activate` or `users.delete` can manage one.
  A customer login with a linked customer record (a `customers` row naming it in `user_id`, erased
  rows included) asks for one key more (#378): erasing it needs `customers.delete`, changing its
  email needs `customers.update`. `governedTx.keysFor` adds the key from the stored role and email,
  after a plain read of `customers` inside the transaction.
- **Order of the refusals.** User writes: `NOT_FOUND`, `ROLE_NOT_FOUND`, `FORBIDDEN` (the actor lost
  the route's key while the request waited, or lacks the customer key a write to a customer login
  with a linked customer record needs), `ROLE_ESCALATION`, `CUSTOMER_LOGIN_ROLE`, `LAST_ADMIN`.
  `PUT` and `DELETE /roles/{id}`: `ROLE_NOT_FOUND`, `FORBIDDEN`, `SYSTEM_ROLE_IMMUTABLE`,
  `ROLE_ESCALATION` for the stored set, then on `PUT` `INVALID_PERMISSION` and `ROLE_ESCALATION` for
  the requested set, on `DELETE` `ROLE_IN_USE` (after the escalation check, so a manager below the
  role does not learn its `user_count`). A refusal writes nothing.
- **Which user writes are governed.** Every `PUT /users/{id}` on another account (profile fields
  included: changing a superior's email would redirect its password reset) and every one that
  writes `role`, `role_id` or `is_active`, the caller's own account included. A profile-only update
  of the caller's own account and `PUT /users/me` stay the plain `UserService.Update`. `Register`,
  `Update` and `Delete` keep their behaviour for the non-HTTP writers (`cmd/create-admin`, public
  registration); a user service built without `service.WithUserRoleGovernance` runs the actor-taking
  methods as those plain writes, which is what the older suites rely on.
- **Audit.** `role_audit_log` gets one row per role create, clone (`role_created`), update and
  delete, and per change of a user's `role` or `role_id` (`user_role_changed`, also for the role a
  new account is created with). `auth_method` is the request's `service.AuthMethodContextKey` value
  (`bearer` or `api_key`). The log lines of these paths carry ids and action names only.

Regression roots (`internal/integration`, on the real Auth, routes, handlers and repositories):
`TestRoleAPI{SQLite,MySQL}`, `TestRoleAPIRejectsInvalidPermissionsSQLite`,
`TestRoleAPIDeleteAssignedRoleSQLite`, `TestLastAdminInvariant{SQLite,MySQL}`,
`TestLastAdminConcurrent{SQLite,MySQL}`, `TestLastAdminAPISQLite`, `TestRoleEscalationRefused`,
`TestRoleEscalationConcurrent{SQLite,MySQL}`, `TestCustomerLoginCannotReceiveCustomRole`,
`TestRoleAuditAtomic{SQLite,MySQL}`, `TestRoleChangeLogsOmitPersonalData`, `TestRoleAuditAPISQLite`,
`TestUserRoleAssignmentAPISQLite`, `TestLinkedCustomerLoginWrites{SQLite,MySQL}`;
`TestGovernedWritesToLinkedCustomerLogin` (`internal/service`); and
`TestUserWritesAreRoleGovernedOnTheRealRouter` (`cmd`), which shows that `setupRouter` wires the
governance option.

**Account security** — account lockout after 5 failed login attempts (15 min) and password
complexity validation (min 10 chars, upper + lower + digit + special).

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

**Quote tax (#324).** A line carries `tax_rate_bp` (0..10000, `models.QuoteTaxRateBPMax`; 2100 is
21 %), written like every other line field: only while the quote is a draft (`lockDraft` refuses
anything else with `ErrQuoteLocked`, 409). `POST /quotes/{id}/lines` binds it as an `int64` (absent
is 0) and `PUT /quotes/{id}/lines/{lineId}` as an `*int64` (absent keeps the stored rate, 0 clears
it), both `binding:"min=0,max=10000"`, so an out-of-range value is 400 `VALIDATION_ERROR` before the
service is called; `validateLineBounds` repeats the bound, so the service refuses it with
`apperrors.ErrValidation` before any transaction starts. `computeLine` sets `line_total_cents` and
then `tax_cents = models.ComputeQuoteLineTax(line_total_cents, tax_rate_bp)`, `(total × bp + 5000) /
10000` round half up. `recomputeTotals` sums the lines with `models.SumQuoteTotals` (subtotal,
discount and net total exactly as `ComputeQuoteTotals`, tax as Σ line `tax_cents`, gross as total +
tax) and refuses a gross total above 10^12 (`QuoteAmountCentsMax`, now the gross bound; the net
total is never larger) with `ErrValidation`, rolling the whole write back. `UpdateTotals(id,
models.QuoteTotals)` writes all five totals and `updated_at` under the same `WHERE id = ? AND
status = 'draft'`, with the `matched()` re-check. `total_cents` keeps its meaning (net after
discount); tax is rounded per line, so the quote tax can differ by cents from tax on per-rate
totals. MySQL/MariaDB get the columns from `20261117120000_add_quote_tax` (each only when missing,
plus one gross backfill); SQLite from `MigrateDatabase`, whose `backfillQuoteGrossTotals` step sets
`gross_total_cents = total_cents + tax_cents` by primary key wherever it differs, on every driver and
without touching `updated_at`, so a backfilled database takes no write. Regression roots:
`TestQuoteLineTaxCompute`, `TestQuoteTotalsWithTax`, `TestQuoteTaxColumnsSchemaSQLite`,
`TestQuoteTaxSQLiteUpgrade`, `TestQuoteTaxJSON` (`internal/models`); `TestQuoteLineTaxRateBinding`
(`internal/handler`); `TestQuoteTaxRateBoundsRejectedWithoutWrites`,
`TestQuoteLineTaxRounding{SQLite,MySQL}`, `TestQuoteGrossCapRefusalWritesNothing{SQLite,MySQL}`,
`TestQuoteTaxRateNonDraftRefused{SQLite,MySQL}`, `TestQuoteEmailShowsTax` (`internal/service`);
`TestQuoteRepositoryUpdateTotalsWithTax{SQLite,MySQL}` (`internal/repository`);
`TestQuoteTaxMigrationFiles` (it held the newest-migration assertion until #343 moved it to
`TestQuoteQuantityMigrationFiles`), `TestQuoteTaxMigration` (`cmd/migrate`); and, over HTTP,
`TestQuoteTaxAPISQLite` (`tests/quote_tax_test.go`). The MySQL roots skip without
`TEST_MYSQL_DSN`; registering them in `scripts/ci/check-go.py` and the migration roots in the
Database migrations workflow is an operator follow-up.

**Quote quantities (#343).** A line quantity is an exact decimal from 0.001 to 1,000,000 with no
non-zero digit beyond the third decimal place.

- **The type.** `models.QuoteQuantity` (`internal/models/quote_quantity.go`) holds integer
  thousandths (`Milli()`); it implements `json.Marshaler`/`Unmarshaler`, `driver.Valuer` and
  `sql.Scanner`, and maps to `quote_lines.quantity_milli` (`bigint NOT NULL DEFAULT 0`, JSON key
  `quantity`). `ParseQuoteQuantity` and `UnmarshalJSON` read the JSON number literal exactly, never
  through a float64: exponent forms and trailing zeros are judged by value (`1e-3`, `1.5000`,
  `1000000.000` are accepted); zero, `-0`, a negative, a value above 1,000,000, a non-zero digit past
  the third decimal, a literal over 64 bytes or an exponent magnitude above 30 is a
  `*models.QuoteQuantityError` (checked before any digit is converted); a string, boolean, array or
  object is another error. `MarshalJSON` and `String()` write the shortest decimal (`1.5`, `2`,
  `0.001`), so a whole number round-trips byte-identical. `QuoteQuantityFromMilli` and
  `QuoteQuantityFromUnits` are unchecked constructors (tests and the scanner); the constants are
  `QuoteQuantityScale` (1000), `QuoteLineQuantityMilliMin` (1) and `QuoteLineQuantityMilliMax`
  (10^9); `QuoteLineQuantityMax` (10^6 units) is kept.
- **The handler.** `QuoteLineRequest.Quantity` and `QuoteLineUpdateRequest.Quantity` are
  `*models.QuoteQuantity` (`binding:"required"` on add only). When `ShouldBindJSON` fails with a
  `*models.QuoteQuantityError`, `respondQuoteLineBindError` answers 400 `VALIDATION_ERROR`, message
  `Validation failed`, `error.details.quantity` = `models.QuoteQuantityBoundsMessage`, before any
  read; every other bind error goes to the error middleware as before (a string quantity is 400
  `BAD_REQUEST`, a missing or null one on POST 400 `VALIDATION_ERROR` keyed `Quantity`). An absent
  or null quantity on PUT keeps the stored value.
- **The arithmetic.** `models.ComputeQuoteLineAmounts(quantity, unitPriceCents, discountBP)`
  returns `gross = round_half_up(milli × price / 1000)` and `lineTotal = round_half_up(milli × price ×
  (10000 − bp) / 10^7)` in 128-bit integer arithmetic (`math/bits`), exact up to milli 10^9 and price
  10^12; `ok` is false when the rounded gross exceeds `QuoteAmountCentsMax`. The line total is
  rounded once from the exact product, so `discount_cents` can differ by a cent from rounding the
  gross first. `ComputeQuoteTotals` and `SumQuoteTotals` take the subtotal as the sum of the rounded
  line gross values. For whole quantities every result equals the former integer formula, which
  `ComputeQuoteLineTotal(int64 units, …)` keeps. `validateLineBounds` refuses a quantity outside
  1..10^9 thousandths with `apperrors.ErrValidation` before any transaction starts; `computeLine`
  refuses a rounded gross above 10^12 inside it.
- **The email.** `RenderQuoteEmail` prints `quantity 1.5 x …`; a whole quantity renders exactly as
  before (`quantity 3 x 9.99 EUR`).
- **The schema.** MySQL/MariaDB: `migrations/20261118120000_store_quote_line_quantity_milli` adds
  `quantity_milli` when missing, copies `quantity × 1000` where it is still 0 while `quantity`
  exists, and drops `quantity`, each step behind an `INFORMATION_SCHEMA` probe; its down half
  re-adds `quantity bigint NOT NULL` (no default), sets it to `GREATEST(1, (quantity_milli + 500)
  DIV 1000)` and drops `quantity_milli`. Every driver: after AutoMigrate (which adds
  `quantity_milli` and never drops a column), `MigrateDatabase` runs
  `migrateLegacyQuoteLineQuantity`, which does nothing when `quantity` is absent and otherwise
  converts the rows still at 0 by primary key in chunks of 500 (plain `SELECT` discovery,
  `updated_at` untouched) and then issues the native `ALTER TABLE quote_lines DROP COLUMN quantity`
  (SQLite 3.35+, MySQL, MariaDB). Never use `Migrator().DropColumn` for it: with a table name the
  glebarez/sqlite migrator dereferences a nil schema and panics, and with a model it rebuilds the
  table through `recreateTable` and loses `idx_quote_lines_quote_id` and
  `idx_quote_lines_product_id`. The legacy column has no index, constraint or foreign key, so the
  native drop needs no rebuild. The parity reference of the migration is the schema
  `MigrateDatabase` builds on the previous head, not plain AutoMigrate; the earlier quote migration
  roots filter both quantity columns (`withoutQuoteQuantityColumns`).
- **Upgrade and rollback.** Stop every replica before starting the new version: an old binary
  cannot write quote lines once `quantity` is dropped. An older binary's AutoMigrate re-adds
  `quantity bigint NOT NULL` without the values, so every line would read 0: before starting one,
  run the down migration (MySQL/MariaDB) or restore the pre-upgrade backup (SQLite, which has no
  down path). The down rounds fractional quantities and leaves the stored totals as they are.

Regression roots: `TestQuoteQuantityParse`, `TestQuoteQuantityJSON`, `TestQuoteLineAmounts`,
`TestQuoteQuantitySQLiteUpgrade` (`internal/models`); `TestQuoteLineQuantityBinding` and the edited
`TestQuoteLineBindingTagsMatchTheModel` (`internal/handler`);
`TestQuoteFractionalQuantityBoundsRejectedWithoutWrites`,
`TestQuoteFractionalQuantityTotals{SQLite,MySQL}`, `TestQuoteEmailFractionalQuantity`
(`internal/service`); `TestQuoteLineQuantityStorage{SQLite,MySQL}` (`internal/repository`);
`TestQuoteQuantityMigrationFiles` (it held the newest-migration assertion until #344 moved it to
`TestRoleMigrationFiles`) and
`TestQuoteQuantityMigration` (`cmd/migrate`); and, over HTTP, `TestQuoteFractionalQuantityAPISQLite`
(`tests/quote_quantity_test.go`). The existing quote tests changed only how they build quantities
(`models.QuoteQuantityFromUnits`). The MySQL roots skip without `TEST_MYSQL_DSN`; registering the
new roots in `scripts/ci/check-go.py` and the migration roots in the Database migrations workflow
is an operator follow-up.

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
the recipient is never copied onto the quote, the body (`RenderQuoteEmail`) ends every line with
` | tax <rate>% <tax>` (also at 0 %) and closes with `Subtotal:`, `Discount:`, `Net total:`
(`total_cents`), `Tax:` and `Gross total:` (#324), and the log lines of the call carry `quote_id` and
`customer_id` only — a transport error that quotes the address is returned, not logged. Roots:
`TestQuoteEmail{Delivery,LogsOmitPersonalData,RevisedDuringSend}` (`internal/service/quote_email_test.go`)
and `TestQuoteRepository_StampEmailedRefusesANewerRevisionToDraft`.

**Quote PDF: a headless browser prints the SPA's render page (#384)** — `service.WithQuotePDF(svc,
renderer)` switches PDF output on for a quote service (`cmd/main.go` does it when
`NewChromeQuotePDFRenderer(QUOTE_PDF_CHROME_PATH)` returns a renderer; an empty path is off, a
path that is not an executable file is off with one warn line; an empty `APP_BASE_URL` is off
too). The pieces, all in `internal/service/quote_pdf.go` and `quote_pdf_chrome*.go`:

- **Render step.** `RenderPDF(id)` (behind `GET /quotes/:id/pdf`) and `Email` call
  `renderQuotePDF`: take the single render slot, mint a credential, call
  `QuotePDFRenderer.Render(ctx, APP_BASE_URL + "/quote-render#" + token)`, and require that the
  page redeemed the credential before the bytes count. Every failure is
  `apperrors.ErrQuotePDFRenderFailed` (503 `PDF_RENDER_FAILED`) with a reason in the log
  (`no_turn_within_bound`, `timeout`, `renderer_error`, `page_never_loaded_the_quote`); the
  renderer's own error is dropped, because nothing guarantees it is free of the URL. PDF off is
  `apperrors.ErrQuotePDFUnavailable` (503 `PDF_UNAVAILABLE`).
- **Render credential.** 32 random bytes (`generateOpaqueToken`), stored only as a hash in an
  in-memory map of the service with the quote id and an expiry equal to the bound, deleted when
  the render returns. It travels in the URL **fragment**, so the server that serves the page
  never receives it, and then in the body of `POST /quotes/public/render`
  (`QuoteService.GetRenderView`), which returns `models.QuoteRenderView` (the quote as
  `GET /quotes/:id` serialises it, and the trimmed `general.company_name`) for the credential's
  own quote and nothing the caller names. Every refusal is `apperrors.ErrQuoteRenderRejected`,
  one generic 400. It is not a session: the Auth middleware does not know it. A browser that
  exits cleanly without the credential having been redeemed printed something else (an error
  page: `APP_BASE_URL` unreachable, CORS refusing the call), so that render fails.
- **Fallback in the email.** `Email` renders after the recipient checks and before
  `mintResponseLink`, with no transaction held. With PDF off, a mailer that does not implement
  `mailer.AttachmentSender`, or a failed or late render, the mail goes out through `Mailer.Send`
  exactly as before and one warn line carries the quote id, the customer id and the reason. The
  render's failure is never an error of the email. Otherwise the mail goes out through
  `SendWithAttachment` with `Quote-<number>.pdf` (`application/pdf`): `multipart/mixed`, the text
  part as `Send` composes it, the file base64 in 76-column lines; a filename with CR, LF, a quote
  or a backslash is refused before the relay is contacted.
- **Built child environment.** `ChromeQuotePDFRenderer` starts one browser per render with a
  fresh temp dir as `--user-data-dir`, `HOME`, `TMPDIR`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME`,
  and an environment that is built, not inherited: `PATH`, `LANG`, `LC_ALL` and `TZ` when set,
  plus those directories. `JWT_SECRET`, `API_KEY_SECRET`, `DB_PASSWORD` and `SMTP_PASSWORD`
  never reach a browser process, and a hardened unit (`ProtectHome`, `ProtectSystem=strict`,
  `PrivateTmp`) or a read-only root file system leaves it everything it needs. The browser is the
  leader of its own process group and the whole group is killed at the bound and after every
  render, so no helper survives. Flags: `--headless`, `--print-to-pdf`, `--no-pdf-header-footer`,
  `--virtual-time-budget` (so the page's fetch finishes and is painted), `--use-mock-keychain`
  and `--password-store=basic` (with `HOME` in a temp dir a macOS Chrome otherwise waits for a
  login keychain it cannot find). `--no-sandbox` is never passed from Go: an image that needs it
  sets it in its launcher (the Dockerfile writes `/etc/chromium/chromium.conf`). Neither the URL,
  its fragment nor the browser's stdout or stderr is logged or wrapped into an error.
- **Time budget (a contract, tested).** `QuotePDFRenderTimeout` = 15 s is the only definition of
  the bound and covers the wait for the slot: one render at a time per process, and a render
  queued behind another fails within the same 15 s. `POST /quotes/:id/email` therefore takes at
  most 15 s + `mailer.DefaultSendTimeout` (30 s) = 45 s and `GET /quotes/:id/pdf` at most 15 s.
  The SPA passes `QUOTE_PDF_REQUEST_TIMEOUT_MS` = 55 000 to `emailQuote` and `downloadQuotePdf`
  (the default `API_TIMEOUT` of 30 s would abort a call the server still completes, and the user
  would send the quote twice); a reverse proxy needs a read timeout of 60 s or more.
  `TestQuotePDFTimeoutBudget` and `quotes.test.ts` pin the numbers; change them together.
- **UI.** `GET /configurations/ui` appends the synthetic read-only `quotes.pdf_available`
  (`ConfigurationHandler.WithQuotePDFAvailable`); `isQuotePdfAvailable` in
  `configuration-context.ts` reads it and QuoteDetail shows **Download PDF** only when it is
  `true`. `QuoteDocument.tsx` is the document shared by `QuotePrint` (toolbar, inside the shell)
  and `QuoteRender` (public route `/quote-render`, no toolbar, no shell). `downloadQuotePdf`
  asks for a blob; an error body is then a Blob as well, which the client's envelope unwrapping
  does not see, so the function reads it and rethrows the error with the flat `{code, message}`
  body every other call has.

Roots: `TestQuotePDF{Off,Render,TimeoutBudget}`, `TestQuoteRenderCredential`
(`internal/service/quote_pdf_test.go`); `TestChromeQuotePDFRenderer{MissingBinary,Timeout,Environment,RealBinary}`
(`quote_pdf_chrome_test.go`; `RealBinary` drives the binary named by `QUOTE_PDF_CHROME_PATH` and
skips without it, and none of the four is in a no-skip gate: CI has no browser);
`TestQuoteEmailPDFAttachment` (`quote_email_test.go`, in `MAILER_CALLER_ROOTS`);
`TestQuotePDFHandler`, `TestQuotePublicRender`, `TestUIConfigurationsQuotePDFAvailable`
(`internal/handler`); `TestSMTPAttachment`, `TestLogMailerAttachment` (`internal/mailer`);
`TestDockerfileShipsChromium`, `TestLoad_QuotePDFChromePath` (`internal/config`).

*The real chain is checked by hand, and belongs to the release checks.* The e2e backend runs
with `QUOTE_PDF_CHROME_PATH` pinned empty (`scripts/e2e/run.sh`), and the tests above use fake
renderers, so nothing automatic notices a change to `QuoteDocument`, the render route or the
browser flags that makes the PDF show the loading state or an empty page: "the credential was
redeemed" proves the page's request arrived, not that the answer was painted.
`scripts/e2e/quote-pdf-smoke.sh` checks exactly that chain (Chrome, the SPA's `/quote-render`,
`POST /quotes/public/render`, the text of the printed file) against a backend on `localhost`. It
logs in, creates a customer, a product and a quote with one line `PDF smoke line <hex>`,
downloads `GET /quotes/<id>/pdf`, fails unless the answer is 200 `application/pdf` starting with
`%PDF`, writes the file and prints `number=` and `line=` (with `SMOKE_EMAIL=1` it also sends and
emails the quote and prints `emailed=yes`). Run it before a release, locally and against the
built image:

```bash
S=$(mktemp -d)                                   # outside the repository
go build -o "$S/gophercrm" ./cmd && go build -o "$S/create-admin" ./cmd/create-admin
(cd gocrm-ui && VITE_API_BASE_URL=http://localhost:18095/api/v1 npx vite build --outDir "$S/dist")

# the same environment for the server and for create-admin
export DB_DRIVER=sqlite DB_PATH="$S/pdf.db" SERVER_HOST=127.0.0.1 SERVER_PORT=18095 \
  SERVER_MODE=development API_PREFIX=/api/v1 JWT_SECRET="$(openssl rand -base64 32)" SMTP_HOST= \
  APP_BASE_URL=http://localhost:15195 CORS_ALLOWED_ORIGINS=http://localhost:15195 \
  QUOTE_PDF_CHROME_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
"$S/gophercrm" &                                 # terminal 1
"$S/create-admin" -non-interactive -email admin@example.com -name Admin -password '<throwaway>'
(cd gocrm-ui && npx vite preview --outDir "$S/dist" --port 15195 --strictPort) &   # terminal 2

BASE_URL=http://localhost:18095/api/v1 ADMIN_EMAIL=admin@example.com ADMIN_PASSWORD='<throwaway>' \
  bash scripts/e2e/quote-pdf-smoke.sh "$S/local.pdf"
# the text of the file must hold both printed values (poppler-utils; from a container if not installed)
docker run --rm -v "$S":/w alpine:3.20 sh -c 'apk add -q poppler-utils && pdftotext /w/local.pdf /w/local.txt'
command grep -cF '<number>' "$S/local.txt"; command grep -cF '<line>' "$S/local.txt"   # each 1 or more
```

Negative control: stop `vite preview` and run the script again; it must exit non-zero with 503
`PDF_RENDER_FAILED`. For the image, start the SQLite Compose stack with an override that sets
`read_only: true`, `tmpfs: [/tmp]`, `APP_BASE_URL: http://ui`,
`CORS_ALLOWED_ORIGINS: http://ui,http://localhost:<UI_PORT>` and
`QUOTE_PDF_CHROME_PATH: /usr/bin/chromium-browser` on the backend, run the script with
`BASE_URL=http://localhost:<UI_PORT>/api/v1`, and check the text the same way plus `pdffonts`
(at least one embedded font). The script starts nothing, prints no token and no password, refuses
a `BASE_URL` whose host is not `localhost` or `127.0.0.1` (it creates data), and is referenced by
no workflow, Makefile target or gate.

**Quote response link: a single-use token claimed with the transition (#382)** — a quote service
built with `NewQuoteServiceWithResponseLinks(..., tokenRepo, appBaseURL, tokenSecret)` (what
`cmd/main.go` wires, with `APP_BASE_URL` and `API_KEY_SECRET`, else `JWT_SECRET`) appends
`<APP_BASE_URL>/quote-response?token=<token>` to the email of a *sent* quote. `NewQuoteService`
keeps its signature for callers that send no link: its emails carry none and both public methods
answer `apperrors.ErrQuoteResponseRejected` (the `NewAuthService` / `NewAuthServiceWithSessions`
precedent). The token is 256 random bits (`generateOpaqueToken`); only `hashOpaqueToken` of it is
stored, in `quote_response_tokens`, and neither the token nor the link reaches a log field or an
error text.

- **Mint.** After the recipient checks and before `Mailer.Send`, `mintResponseLink` runs one short
  transaction: `GetByIDForUpdate(quote)`, the re-check that it is still sent (`ErrQuoteNotSent`
  otherwise), the re-read of the latest history id against the marker read at the start
  (`ErrQuoteChangedDuringSend` when it moved: a revision and re-send in between would mail the old
  rendering with a live link to the new quote), `InvalidateForQuote`, `Create`. Both refusals are
  409 and send nothing. No transaction is held during the send. A failed send leaves the stored
  hash in place: its plain value was never delivered, and the next email stamps it.
- **Expiry.** `expires_at` is the start of the day after `valid_until` in `time.Local`, stored in
  UTC (`quoteResponseExpiry`), the same day `dateBefore` allows a staff acceptance; NULL without a
  `valid_until`. A sent quote whose `valid_until` has passed is still emailed, without a link and
  without a row; an accepted quote is emailed as before.
- **Read.** `GetPublicResponse(token)` hashes, calls `FindUsableByHash` and loads the quote. It
  never writes, so mail scanners and previews do not spend the link.
- **Claim.** `RespondPublic(token, decision)` reads the token by hash, then in one transaction, in
  the order `quotes → quote_response_tokens`: `GetByIDForUpdate(quote)`, `Claim` (one conditional
  `UPDATE` setting `used_at`, `decision` and `updated_at` `WHERE id = ? AND used_at IS NULL AND
  (expires_at IS NULL OR expires_at > ?) AND EXISTS (the quote is live and sent)`; exactly one row,
  else `gorm.ErrRecordNotFound`), `ClaimStatus(sent → accepted | rejected)` with `closed_at`, and
  the history row with `changed_by_id` = the quote owner. A failure after the claim rolls it back
  and the link stays usable. The deal repository is not touched. This is the single-use token
  shape of the form confirmation and the password reset; do not pre-check then update.
- **Invalidation.** `ChangeStatus` stamps the quote's unused rows in its own transaction whenever
  it leaves `sent` (`InvalidateForQuote`, `decision` stays NULL), so a link stays dead after
  sent → draft → sent until the next email mints a new one. `InvalidateForQuote` reads the ids
  with a plain `SELECT` and stamps them by primary key (no statement when there is none): an
  `UPDATE` filtered on `quote_id` gap-locks the index on MySQL and MariaDB and deadlocked the
  first emails of two different quotes. Call it only after the locking read of the quote.
- **HTTP.** `SetupQuotePublicRoutes` mounts `GET` and `POST /quotes/public/response` on the public
  group with one shared `RateLimitStrict()` instance and no Auth. The body is validated before any
  token lookup (400 `VALIDATION_ERROR`); every `ErrQuoteResponseRejected` is 400 `BAD_REQUEST`
  "Invalid or expired quote link"; the response is `models.QuotePublicView` (number, status,
  currency, `valid_until`, lines, five totals; no id, title, notes, owner, customer, deal or
  product reference).
- **Page (#383).** The link opens the SPA route `/quote-response`, declared at the top level of
  `gocrm-ui/src/routes/index.tsx`, outside `ProtectedRoute` and `MainLayout`
  (`gocrm-ui/src/pages/quotes/QuoteResponse.tsx`). It issues one `GET` per mount (no retry, no
  refetch: the strict bucket is shared with the `POST`), asks for a confirmation before the one
  `POST`, and treats a 400 on the `GET`, or a 400 `BAD_REQUEST` on the `POST`, as a dead link.
  The token is read from the query string and never rendered or logged.
- **Public client.** The page's endpoint module (`gocrm-ui/src/api/endpoints/quoteResponse.ts`)
  calls through `publicApi`, which is `apiClient.getPublicClient()`: a second axios instance
  with the base URL, timeout, transport and envelope unwrapping of `api` and no request
  interceptor. It never attaches the stored session's `Authorization` header and a 401 is neither
  refreshed nor replayed. Use it for any endpoint that is answered without a session and must
  not carry one; touch it only inside function bodies, as many tests mock `../client` with a
  factory that defines `api` alone.
- **Browser-test link.** The mail body is the only place the plain token exists, and the
  log-only mailer never writes it. `go run ./scripts/e2e/quote-response-link -quote-id N` loads
  the configuration like the server, wires the quote service as `cmd/main.go` does with a mailer
  that keeps the message in memory, calls `Email(N)` and prints the link line, and nothing else,
  on stdout (the loggers are pointed at stderr). It exits non-zero without output when the quote
  is not emailed with a link (a draft, an accepted quote, a sent quote past `valid_until`), when
  `E2E_UI_PORT` is not set (only `scripts/e2e/run.sh` exports it) and, before any connection,
  when `DB_DRIVER` is mysql and `DB_NAME` does not end in `_e2e`. Like a staff email it
  invalidates the previous link of the quote and stamps `last_emailed_at`, which is why it must
  never run against a real database. It is not part of the server binary and adds no route.

Roots: `TestQuoteEmailResponseLink` (`internal/service/quote_email_test.go`);
`TestQuoteResponse{View,Decision,RollbackKeepsLink,DeadLinks}`
(`internal/service/quote_response_test.go`); `TestQuoteResponseConcurrent{SQLite,MySQL}`
(`internal/service/quote_response_regression_test.go`; the MySQL root connects with `loc=Local`
like the application, skips without `TEST_MYSQL_DSN` and is not part of a CI gate, like the other
quote concurrency roots); `TestQuoteResponseTokenRepository` (`internal/repository`);
`TestQuotePublic{View,Respond}`, `TestQuotePublicRoutesWinOverTheIDParameter` (`internal/handler`);
`TestQuoteResponseTokensMigrationFiles` (it holds the newest-migration assertion) and
`TestQuoteResponseTokensMigration` (`cmd/migrate`). Page and client: `QuoteResponse.test.tsx`,
`client.public.test.ts`, `quoteResponse.test.ts`, the "quote response route" block of
`routes/index.test.tsx`, `gocrm-ui/e2e/tests/quote-response.spec.ts`, and
`TestQuoteResponseLink{PrintsUsableLink,RefusesDraftQuote,RefusesNonE2EDatabase}`
(`scripts/e2e/quote-response-link`).

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
discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them.

**Lead and company lock order (#182)** — lead conversion, lead update, bulk lead status and company
deletion share one lock order: **companies** (by primary key, `CompanyRepository.LockLive`:
`SELECT id … FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite, `gorm.ErrRecordNotFound` for a
missing or soft-deleted company; it takes no context and runs on the `WithTx(tx)` handle) →
**leads** (id ascending) → **customers** → **deals** (see [CONCURRENCY.md](CONCURRENCY.md)).

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
`bulk*_test.go` are discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them. Registering the migration roots in the Database
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
The runner's local validation picks a profile from the changed paths; its bounded profiles
(`auth`, `auth-persistence`, `transaction`, `migration`, `aeo`, `mailer`) replace `make verify` with
their `scripts/ci/check-auth.py` / `scripts/ci/check-go.py` race roots on SQLite and MySQL, named
acceptance checks and health smoke; see [validation and releases](VALIDATION_RELEASE.md) for each scope.

**CI outside a release runs targeted Go tests (#365).** For a pull request, a merge-queue group and a
push to `main`, CI runs hygiene, backend, frontend and the database gates job:

- **Backend** builds and vets the whole module, runs `python3 scripts/ci/check-api-docs.py`, and
  runs `python3 scripts/ci/check-changed-go.py` on the plan job's changed paths: one
  `go test -count=1 -timeout 10m` over the selected packages, without `-race` and without a
  database server (MySQL-backed tests skip there).
- **Database gates (MariaDB 10.11)** runs the same selection with `--tests-only` and
  `TEST_MYSQL_DSN` pointing at a MariaDB 10.11 service, so the MySQL-backed tests of the selected
  packages run there. Nothing rejects a skip in this job.
- A documentation-only change (`*.md`, anything under `docs/`, `LICENSE`) runs no Go test at all.
- The race detector (`make verify-backend`), `make verify-api-docs`, the no-skip checkers
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
`Go tests: every package (<reason>)`, and prints each command before running it.

```bash
python3 scripts/ci/check-changed-go.py --base origin/main          # what CI will build, vet and test
python3 scripts/ci/check-changed-go.py --base origin/main --list   # the selection only
```

`--base` compares that revision with `HEAD`, so it sees committed changes only: on a tree with
uncommitted edits a "none selected" is not a pass. Two consequences to know:

- **A new test that reads files outside its own package needs a row** in `EXTERNAL_READERS`
  (`scripts/ci/check-changed-go.py`), with a case in `scripts/ci/tests/test_changed_go.py`. Until it
  has one, only a full run exercises it when those files change.
- **`internal/config` pins documentation text** (`README.md`, `CHANGELOG.md`, `SERVER_PROVISIONING.md`
  and `docs/{DEVELOPMENT,FORKING,DOCKER,USE_CASES,ROADMAP,EXTENDING,SETUP}.md`). A documentation-only
  pull request runs no Go test, so it can break one of those pins and still be green; the next change
  that selects `internal/config` then fails for a reason outside its diff. Run
  `go test -count=1 ./internal/config` after editing those files. `internal/permissions` pins
  `docs/AUTHORIZATION.md` in the same way (its catalogue and matrix tables against the code, the
  keys and routes of its integration-user section, and the pointers to it in `README.md`,
  `docs/FEATURES.md` and `CHANGELOG.md`): run
  `go test -count=1 ./internal/permissions` after editing any of those four.

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
  `routeMatrixPublic` list in the test: `/health`, the six unauthenticated `/auth` routes, the
  six `/forms/public` routes and the two `/quotes/public/response` routes and `POST /quotes/public/render`. A new route in neither fails `TestRouteMatrixCoversEveryProtectedRoute`.
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
  boot-time stale sweep; the API and the event capture work without it, but no run starts or
  resumes and no stranded run is touched. It may be on for any number of replicas sharing one
  MySQL/MariaDB database (run leases, #237); a SQLite file stays one process
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
  (`ErrConvertedLeadNeedsAdmin`, an `apperrors.ErrForbidden`, 403) inside the erasure transaction,
  on the lead row it has locked, before the lead is written (#337). The unit rolls back, the
  automation cancel included, so a refusal changes nothing.
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
- The lead's automation state ends in the same transaction, *before* the lead is even read
  (`erasurePlan.BeforeScrub`, run by the erasure unit for every lead of the graph, ahead of its first locking read of a lead): every pending, waiting or running run on the lead is cancelled with
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
- **One lock order for every erasure (#385).** `eraseGraph` (`internal/repository/erasure_cascade.go`)
  is the entry point of the user, customer and lead erasures and runs one protocol: (1) discover
  the graph from the root with plain reads; (2) cancel the automation runs of every discovered
  lead, ids ascending; (3) lock with locking reads by primary key only, ids ascending inside each
  table, leads → customers → users → refresh_tokens → password_reset_tokens → api_keys; (4) read
  the graph again, forward links from the locked rows and reverse links by plain reads issued
  after the users lock, and roll back with `ErrErasureGraphChanged` when it differs from the
  discovery; (5) decide the non-admin refusal on the locked lead, then scrub, purge and
  soft-delete as before. When the repository is not bound to a transaction, the discovery and the
  schema probes of the cancel (`probeAutomationTables`) run on the root handle before the
  transaction begins, and the transaction issues no plain read before the users lock, so the
  re-check of step 4 sees everything that committed before the locks were granted: under
  REPEATABLE READ the first plain read fixes the snapshot, and on MySQL 8 a migrator probe (a
  select on `information_schema`) counts as one (on MariaDB 10.11 it does not). The transaction
  runs under `WithTransactionAndRetry` (`erasureMaxRetries` replays of a deadlock, a lock wait or
  a busy SQLite file, on the same discovery); a changed graph starts over from a new discovery,
  at most `erasureGraphMaxRestarts` times. Through `WithTx` the erasure is one checked SAVEPOINT
  of the caller's transaction, runs once and hands both kinds of error to the caller; the governed
  user write (`roleGovernance.run`) restarts its root on `ErrErasureGraphChanged`, at most
  `roleWriteGraphMaxRestarts` times. Regression roots: `internal/repository/erasure_lock_order_test.go`
  (`TestErasureCrossRootMySQL`, `TestErasureLockOrder{SQLite,MySQL}`,
  `TestErasureGraphChanged{SQLite,MySQL}`, `TestErasureRetryBoundSQLite`) and
  `internal/service/erasure_credential_order_test.go` (`TestErasureVsCredentialFlows{SQLite,MySQL}`,
  `TestGovernedUserErasureVsCustomerErasureMySQL`, `TestGovernedWriteReplaysErasureGraphChange`),
  through the test-only `repository.ErasureHooks`. CI limit: `erasure_lock_order_test.go` and
  `erasure_credential_order_test.go` are discovered by `scripts/ci/check-go.py transaction`. CI
  runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on
  MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs them on MariaDB 10.11
  only, without `-race` and without rejecting a skip.
- **Lead erasure against lead conversion (#337).** After the cancel, the erasure reads the lead
  with a locking read by primary key (`FOR UPDATE` on MySQL/MariaDB), the first lock it takes on
  the leads row, and takes the non-admin refusal and the decision to follow `customer_id` on that
  locked row only (since #385 a `customer_id` that differs from the discovery restarts the
  erasure). The customer and the login are locked by primary key next. Every erased row (lead,
  customer, user) is then scrubbed by primary key; its original address is read by
  `readErasedEmail`, a locking read by primary key, and only then replaced by the placeholder.
  None of them answers from a REPEATABLE READ
  snapshot older than a conversion that committed meanwhile. The converted-lead pluck
  (`WHERE customer_id = ?`) stays a plain read, so no lock is taken through the
  `leads.customer_id` index: a conversion always inserts a new customer, so no lead can be
  converted into a customer being erased. Every erased row is soft-deleted by `softDeleteErased`:
  a locking read by primary key of whether the row is still live (a row soft-deleted earlier keeps
  its deletion time and gets no further write), then GORM's own soft delete,
  `UPDATE … SET deleted_at = ? WHERE id = ?`, with its `deleted_at IS NULL` predicate suppressed
  (the unexported `primaryKeyOnlySoftDelete` marker sets GORM's `soft_delete_enabled` clause,
  gorm v1.30.0). It still runs through GORM's Delete callback chain, so Delete callbacks and
  hooks keep firing (the erasure-atomicity tests inject their soft-delete fault there). GORM's
  missing-WHERE guard counts the suppressed predicate as a condition and would refuse the single
  `id = ?` with `gorm.ErrMissingWhereClause`, so that one statement runs in a
  `Session{AllowGlobalUpdate: true}`; `softDeleteErased` refuses a zero id instead, and
  `TestLeadErasureLockOrderSQLite` asserts that every erased table's soft delete carries exactly
  `WHERE id = ?`. With GORM's predicate, QA observed on MariaDB 10.11 a gap lock of the erasure's
  customer soft delete on `idx_customers_deleted_at` that made an unrelated conversion's
  `INSERT INTO customers` wait for the erasure to commit (MySQL 8 never waited); with the
  primary-key-only soft delete QA recorded `TestLeadErasureUnrelatedConversionNotBlockedMySQL`
  passing as an explicit `UPDATE` on MariaDB 10.11.19 and MySQL 8.0; the root also passed in the
  Delete-chain form used now, together with `TestIdentityErasureMySQL`, on MariaDB 10.11 (QA) and
  on MySQL 8.0 (the #355 acceptance run).
  Lock order: automation runs → leads → customers, the
  same lead-before-customer direction as conversion (companies → leads → customers), so the two
  never form a cycle. Outcomes: a conversion that holds the lead lock commits first and the
  erasure sees its `customer_id` — an admin erasure (`Delete`) erases the new customer too, a
  non-admin one (`DeleteUnconverted`) is refused and changes nothing, automation runs included;
  a conversion that comes second waits on its `lockedLeadRead` and then finds the lead gone
  (`gorm.ErrRecordNotFound`, which `apperrors.IsNotFound` matches, so `POST /leads/{id}/convert`
  answers 404 as before). The service's plain pre-read in `LeadService.Delete` decides only the
  404. Customer- and user-rooted erasures inherit the per-lead order (customers → automation runs
  → leads) and the locking `readErasedEmail`; a customer root still waits on its scrub `UPDATE`.
  Test-only seams: `repository.WithLeadErasureHooks` (a variadic option of `NewLeadRepository`
  with `AfterAutomationCancel`, called with the unit's transaction handle and the lead id, and
  `AfterLeadLock`, called with the lead id; copied by `WithTx`, never wired in production) and the
  instance-local `leadService.afterDeletePreRead`, between `Delete`'s pre-read and the erasure
  unit. `TestLeadErasureConversionCommittedAfterCancelMySQL` uses the handle to issue a plain read
  of the lead before it parks, as the pre-#337 discovery read did: that read opens the unit's
  REPEATABLE READ snapshot (the probes, the cancel `UPDATE` and the notification `DELETE` open
  none), the conversion commits after it, and the root asserts that a plain read of the new
  customer on the same transaction still finds nothing when the erasure resumes. Only the locking
  discovery reads see past that snapshot, so the root fails when they are reverted to plain reads.
  Roots: `TestLeadErasureConversionFirstMySQL`,
  `TestLeadErasureErasureFirstMySQL`, `TestLeadErasureConversionCommittedAfterCancelMySQL`,
  `TestLeadErasureUnrelatedConversionNotBlockedMySQL` and `TestLeadErasureVsConversionSQLite`
  (`internal/service/lead_erasure_conversion_lock_test.go`); `TestLeadErasureLockOrderSQLite`,
  `TestLeadErasureDiscoveryReadsLockMySQL`, `TestLeadErasureSurvivesAutomationStepMySQL` and
  `TestLeadErasureHooksDefaultUnset` (`internal/repository/lead_erasure_lock_order_test.go`, the
  statements captured through GORM callbacks registered only for the repository call);
  `TestLeadErasureHooksNotWiredInProduction` (`cmd`); `TestConcurrencyDocLeadErasureVsConversion`
  and `TestErasureSoftDeleteRecordedDocs` (`internal/config`). The MySQL roots use a disposable `TEST_MYSQL_DSN` schema, see each wait in
  `information_schema.PROCESSLIST` on two consecutive polls, assert that no ordering ends in error
  1213, skip without the variable and fail when it is unreachable; `make verify-backend` has no DSN
  and skips them. They must pass on MySQL 8 and MariaDB 10.11 alike: `scripts/ci/check-go.py
  transaction` already discovers them through its `lead*_test.go` globs and runs them on MariaDB
  in CI's database gates. See [CONCURRENCY.md](CONCURRENCY.md), Lead-rooted erasure.

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
| `roles`, `role_permissions`, `role_audit_log` (#344) | not touched | Role names carry no personal text by rule; audit rows hold ids, role names, permission keys and scopes only, by contract (the #348 writers store role names, descriptions, permissions, `role` and `role_id` only). An erased user keeps its `role_id` until `ClearRoleIDOfErased` |
| `bulk_operation_items` (`data`, `error`) | not touched | Legacy rows written by the generic bulk subsystem removed in #112 can hold request payloads; nothing writes the table any more (retirement is on the roadmap) |
| `form_submissions` with no `lead_id` (spam, never-confirmed opt-ins, forms that create no lead) whose `email` matches no erased row | not touched by erasure | Not linked to any person the CRM erased. With `FORM_RETENTION_DAYS` set, the retention purge removes old spam and never-confirmed rows (see *Form retention purge*); received rows of forms that create no lead stay |
| `customer_external_ids` of an erased customer (#212) | hard-deleted (the customer plan's `AfterScrub`, every root that reaches the customer) | mappings of other customers |
| `ticket_mail_deliveries` whose `customer_id` is an erased customer or whose `ticket_id` names one of its tickets, soft-deleted tickets included (#219) | hard-deleted (the customer plan's `AfterScrub`, `purgeCustomerTicketMailDeliveries`, every root that reaches the customer; skipped by a `HasTable` probe when the table is absent) | deliveries of other customers; the rows of a comment whose staff author is erased |
| `form_ticket_keys` whose `customer_id` is an erased customer or whose `ticket_id` names one of its tickets, soft-deleted tickets included, and the key rows of every submission the lead or unlinked-submission scrub reaches (#213) | hard-deleted (`purgeCustomerFormTicketKeys` in the customer plan's `AfterScrub`, every root that reaches the customer; `scrubSubmissionTicketKeys` in `scrubLeadFormSubmissions` and `scrubUnlinkedSubmissions`; each skipped by a `HasTable` probe when the table is absent) | key rows of other customers and submissions |
| `form_submissions.idempotency_key` of the submissions those two scrubs reach (#213) | `''` | `ticket_id`, `ticket_outcome` (an id and an outcome) |
| `tickets` opened by a form (#213) | not touched | Their text is fixed (`Form: <form name>`, `Submitted through form <id>, submission <id>.`) and holds no submitted value |
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

- **Concurrency.** The erasure is one transaction. A lead-rooted erasure locks the lead after
  cancelling its automation runs and decides on the locked row, so a conversion of that lead either
  commits first (the erasure erases its customer, or refuses a non-admin caller) or waits and
  answers 404 (#337, see *Lead erasure against lead conversion*). Since #385 every erasure
  discovers the person's rows first, cancels the automation runs of the leads, locks leads,
  customers, users and the credential rows in that order by primary key, re-checks the graph under
  the locks and only then scrubs (see *One lock order for every erasure*): two erasures of one
  person started from different rows queue instead of deadlocking, a lost deadlock or lock wait is
  replayed (`erasureMaxRetries`), and a graph that moved between the discovery and the locks
  restarts the erasure (`ErrErasureGraphChanged`, `erasureGraphMaxRestarts`); when a bound is
  exhausted the caller gets the generic 500. What stays open: the governed user write of
  `DELETE /users/{id}` holds the target's user row before its nested erasure locks leads and
  customers (a deadlock with an erasure of the same person from another row is replayed by either
  side, and a governed delete replayed after the other erasure committed answers 404); the
  password-reset confirm takes its token before the user row and is not retried, so it may fail
  with error 1213 against an erasure (on MariaDB 10.11 also against one that already holds the
  token row: the confirm's claim locks the `deleted_at` index entry first); inside a caller's transaction the re-check of the
  reverse links answers from the caller's snapshot; and a write of `customers.user_id` takes no
  lock on the user row (no shipped code path writes it), so a customer linked to the login after
  the erasure's re-check and before its commit escapes the cascade. Login (#336), refresh rotation, the
  password-reset request and API-key creation (#342) lock the user row before they issue anything,
  so against a user- or customer-rooted erasure they either commit first (and the erasure purges
  what they issued) or wait and are refused. The original address is read with a locking read of
  the erased row (#337), but the read of the matching unlinked submission ids is a plain snapshot
  read, so a submission from the same address inserted concurrently can survive. Tracked in #183.
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
  when the claim names a slot that a row already holds. Lock contention on the claim (#371) gets
  the same answers, for scheduled and slot-less (manual, single-prompt) claims alike: concurrent
  claimants of the same unique keys can make MySQL/MariaDB answer error 1213 (deadlock) or 1205
  (lock wait timeout) instead of a duplicate key. A slot claim then looks the slot up first and
  answers `ErrAEOScheduleSlotTaken` when a row holds it; otherwise the INSERT is replayed, at
  most 3 attempts in total per claim, without sleep or backoff, and every attempt goes through
  the duplicate-key classification (after a deadlock the winner may not have committed yet, so
  the replay waits for it and ends as a duplicate key). A deadlock on all three attempts is
  `ErrRunInProgress`, never the raw driver error; for a scheduled claim that is the scheduler's
  "a run is already in progress" skip, so the slot is not retried. A lock wait timeout is
  replayed once and a second one on the same claim is returned unchanged as the driver error:
  each arrives only after `innodb_lock_wait_timeout` (50 s by default) and `startMu` is held
  across the claim, so the worst case is two lock waits during which this process starts no other
  run. The replay is safe because a failed attempt left nothing behind: GORM runs the INSERT in
  its own short transaction, which the server rolls back on a deadlock and GORM rolls back after
  a lock wait timeout, and `ClaimRun` is never called on a transaction handle.
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
`TestAEORunOwnershipMigrationFiles` (the newest-migration assertion has since moved to
`TestAutomationRunLeaseMigrationFiles`, #237) and
`TestAEORunOwnershipMigration` (`cmd/migrate`); `TestAEORun{Claim,Finish,Heartbeat,AbandonedSweep}{SQLite,MySQL}`
and `TestAEORunClaimContention{SQLite,MySQL}` (#371, injected deadlock and lock wait timeout)
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
from `RequirePermission(aeo.view)` before the handler runs.

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
stale bulk operation sweep (#196), then the webhook sender (#345), then AEO — before
`models.CloseDatabase`. That order describes the code and is not a contract: no handle depends on
another, all five share one deadline, and no test asserts the order.
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
  `dedupe_key` — `ON CONFLICT DO NOTHING` on SQLite, `ON DUPLICATE KEY UPDATE id=id` on
  MySQL/MariaDB, no error and no row for a duplicate on either. The insert is executed without
  `RETURNING` and reports insertion from the driver's affected-row count (1 insert, 0 duplicate),
  because MariaDB 10.5 and later would otherwise return the existing row for a duplicate and read
  as an insert (#367; `automation_run_dedupe_test.go`: `TestAutomationRunDedupe{SQLite,MySQL}`).
  The executor claims a run with `UPDATE … SET status =
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
  `sending`, and a row found in `sending` — by the next execution or by the stale sweep — is
  failed with `email_unconfirmed` together with the run; the mail is never sent again. A run
  cancelled between the send and the stamp stays cancelled with its row finalized; a run cancelled
  before the pre-send check (below) is not sent and its row stays `sending`.
- **Run leases (#237).** Several replicas may run the worker against one MySQL/MariaDB database.
  - *Ownership.* Each `AutomationEngine` generates an instance id at construction (32 lowercase
    hex characters from `crypto/rand`). `ClaimRun(id, fromStatus, owner, now)` writes `status =
    running`, `owner_instance`, `heartbeat_at` and `claimed_at` in one conditional update.
    `ExecuteDue` reads the clock afresh for every claim of its batch: the runs execute one after
    another, and a time read before the loop would stamp a late claim with a lease that is
    already stale. The batch is a plain read taken before the claims, so between that read and a
    claim another engine may have claimed the run, executed a step and released it back to
    `pending` at a later position: the claim still matches. After every successful claim the
    executor therefore re-reads the run by primary key and runs from the stored `next_position`
    and `attempts`, skipping it if it is no longer `running` under this engine; it never executes
    from the batch snapshot. A fenced write that ends the hold and matches nothing is logged at Info
    with `run_id` and `operation`. Both lease columns are `json:"-"`; `claimed_at` is still
    serialised and is not a lease.
  - *Heartbeat.* After a successful claim the executor starts one goroutine that calls
    `RenewRunLease(id, owner, at)` every `automationRunHeartbeatInterval` (30 s): `UPDATE … SET
    heartbeat_at WHERE id = ? AND status = 'running' AND owner_instance = ?`, re-checked with a read
    when MySQL reports zero changed rows. `repository.ErrAutomationRunNotOwned` (a cancel, a
    takeover) cancels the run's context: the executor starts no further step and writes nothing
    more. Any other failure is logged and retried at the next interval. The goroutine is joined
    before `executeRun` returns, so once `AutomationRuntime.Stop` returns nil nothing renews.
  - *Fenced writes.* `AdvanceRun` and `ReleaseRun` also require `owner_instance = owner` and clear
    `owner_instance` and `heartbeat_at` when the run leaves `running`. The writes that end the
    executor's hold outside a step (complete, fail after the last attempt, release, cancel on a rule
    change) each run in their own short transaction replayed on a deadlock, a lock wait or SQLite
    busy (`runWrite`); one that still fails is logged with `run_id` and `operation` only, and the
    run stays `running` under the expired lease until the sweep recovers it. The first `send_email`
    transaction refuses a run row (read `FOR UPDATE`) that is not running at its position under its
    own owner; a plain read of the run just before `Mailer.Send` (the pre-send check) does the same,
    so an executor paused there neither sends nor stamps; the second transaction advances (fenced)
    and then stamps with `FinishSendingStep(id, outcome, errorCode, details)`, which matches only a
    row still in `sending`. A claimed run whose position already holds a finalized `send_email`
    row (an earlier executor's late stamp) is not sent again and not left running: the executor
    advances past it (fenced), or fails the run when the row is `email_unconfirmed`
    (`continuePastRecordedEmail`). `CancelRun`, `CancelRunsForRule` and `CancelRunsForEntity` keep their
    predicates and clear the lease through the shared cancel helper. Lead erasure and ticket delete
    cancel with their own statement (`cancelAutomationForEntity`), which since #333 also sets
    `owner_instance` and `heartbeat_at` to NULL in the same conditional UPDATE that sets `cancelled`
    (predicate, transaction, `HasTable` probe and ticket-row-first lock order unchanged). Every cancel
    therefore releases the lease: the former owner's next `RenewRunLease` returns
    `ErrAutomationRunNotOwned`, its fenced `AdvanceRun`/`ReleaseRun` match nothing, `ClaimRun` and the
    stale sweep ignore the row, and a cancel issued after a worker's terminal transition or another
    cancel matches nothing. Runs cancelled by an erasure or a ticket delete before #333 keep their
    stale lease values (no backfill); nothing reads them on a cancelled run. `claimed_at` is not
    cleared by a cancel. `FailRun`,
    `ResetRunToPending`, `UpdateRunStepOutcome` and `ListRunsByStatus` are gone.
  - *Stale sweep.* `AutomationEngine.SweepStaleRuns` replaces `Reconcile`. It runs in
    `setupDependencies` before the worker and the HTTP server start (only when `AUTOMATION_ENABLED`
    is true) and at the start of every worker tick, on every replica. `ListStaleRunningRuns` is a
    plain read of the running rows whose `heartbeat_at` is NULL (a legacy row) or older than
    `automationRunLeaseStaleAfter` (3 min), in id order; each is recovered in one transaction by
    `RecoverStaleRun(id, observedOwner, heartbeatBefore, failCode, now)`, an update by primary key
    that re-checks `running`, the observed owner (NULL-safe) and the staleness, so a run renewed,
    re-claimed or finished meanwhile is left alone. A run whose last step row is a `send_email` in
    `sending` is failed with `email_unconfirmed` and the row stamped the same in that transaction;
    every other stale run goes back to `pending` at its `next_position` with `attempts` unchanged.
    The choice comes from a plain read taken before the transaction, so after the conditional
    update the transaction re-reads the last step row; when it differs (a stale owner committed its
    sending row, or stamped it, in between) the recovery rolls back and the next sweep decides on
    the current row. A sending row is inserted only by the owner while it holds the run row, so none
    can appear after the update. A live run on another replica is never touched. Locks are taken run row first, step row second,
    the order of the email step, and the sweep and the heartbeat each write one run row by primary
    key, so they queue behind a step instead of deadlocking.
  - *Limits.* The window between the pre-send check and `Send` stays open: an executor paused
    exactly there can still send once after a sweep recorded `email_unconfirmed` (outcome unknown,
    never resent). Replica clocks must agree to well under 90 s. Upgrade by stopping every replica
    before starting the new version: an older binary's boot `Reconcile` resets live runs. Several
    processes on one SQLite file are not supported.
  - *Logs.* The claim, heartbeat, heartbeat-loss, pre-send, sweep, takeover and fenced-write lines
    carry operation names, ids, positions, step types, outcomes, error codes and counts (plus the
    sanitised database error of a failed renewal or recovery); never the instance id.
  - *Schema.* `models.AutomationRun.OwnerInstance` (`varchar(64)`) and `HeartbeatAt`
    (`datetime(3)`), both nullable, from `MigrateDatabase` on SQLite and
    `migrations/20261116120000_add_automation_run_leases` (each column only when missing, no
    index, no backfill) on MySQL/MariaDB; see [datamodel.md](datamodel.md#automation-run-leases).
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
  the deadline passes: the process exits without closing the database, the run's lease goes stale
  and the next sweep on any worker replica recovers it.
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
`TestAutomationReconcile{SQLite,MySQL}` (now the stale sweep), `TestAutomationRuntimeDrain`,
`TestAutomationLogsOmitPersonalData` (`internal/service`); `TestAutomationRunListScope{SQLite,MySQL}`,
`TestAutomationRunRuleNames{SQLite,MySQL}`,
`TestAutomationErasure{SQLite,MySQL}` (the MySQL root reproduces the paused-step deadlock at the
repository level) (`internal/repository`); `TestAutomationRunServiceRoles`;
`TestAutomationRuleAPISQLite`, `TestAutomationRunAPISQLite`, `TestNotificationAPISQLite` (`tests`);
`TestAutomationRuntimeLifecycle` (`cmd`, the real wiring over a SQLite file: the worker replica
sweeps and runs, a replica with the worker off leaves a stranded run untouched);
`TestMigrationAutomationSchema` and `TestMigrationAutomationTablesListed` (`cmd/migrate`). The MySQL
roots skip without the variable and fail when it points at an unreachable server.

Run lease roots (#237), two engines with separate pools on one SQLite file or one disposable
`TEST_MYSQL_DSN` schema unless noted: `TestAutomationRunLeaseJSONHidden`,
`TestAutomationRunLeaseSQLiteUpgrade` (`internal/models`); `TestAutomationRunLeaseMigrationFiles`
(the newest-migration assertion has since moved to `TestQuoteTaxMigrationFiles`, #324),
`TestAutomationRunLeaseMigration` (`cmd/migrate`);
`TestAutomationRunLeaseRepository{SQLite,MySQL}` (`internal/repository`);
`TestAutomationRunLeaseTakeover{SQLite,MySQL}`, `TestAutomationRunLeaseEmailAtMostOnce{SQLite,MySQL}`
(a stale first transaction, a send parked before and after the pre-send check, exact `Send`
counts), `TestAutomationRunLeaseLiveRunSpared{SQLite,MySQL}`, `TestAutomationRunLeaseHeartbeatLifecycle`
(SQLite), `TestAutomationRunLeaseConcurrentWorkers{SQLite,MySQL}` (with a subtest that parks an
engine through the `beforeClaim` seam between its batch read and its claim while the other engine
steps and releases the run, and asserts the late claimer finishes from the stored position
without a sweep),
`TestAutomationRunLeaseLockOrderMySQL` (a heartbeat and a sweep against a step in both orderings,
each wait seen in `information_schema.PROCESSLIST` on two consecutive polls, no error 1213) and
`TestAutomationRunLeaseLogsOmitPersonalData` (`internal/service`, through the engine's
instance-local seams `afterRecordRead`, `beforeStep`, `beforeSend`, `afterSend` and
`afterStaleRecover`); `TestAutomationRunLeaseProductionWiring` (`cmd`, the real wiring and clock
over a SQLite file, observed through the `automationRuntimeBuilt` hook and
`AutomationEngine.SetRecoveryObserver`). Cancel roots (#333), on SQLite and on a disposable
`TEST_MYSQL_DSN` schema (skipped without it): `TestAutomationCancelReleasesLease{SQLite,MySQL}`
(`internal/repository`: admin and non-admin lead erasure, ticket delete with a failure injected into
the `automation_runs` UPDATE, `CancelRun`, `CancelRunsForRule` for the three rule reasons and
`CancelRunsForEntity`, each on a run claimed by `ClaimRun`, followed by every worker write, plus a
late cancel after a completed, failed or already-cancelled run, run sequentially) and
`TestAutomationCancelPathsReleaseLease{SQLite,MySQL}` (`internal/service`: `CancelRun`,
`UpdateRule`, `SetEnabled(false)` and `DeleteRule`, with the run JSON key set checked).
Registering the MySQL roots in `scripts/ci/check-go.py`
and the migration roots in the Database migrations workflow is an operator follow-up.

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

### Outbound webhooks (#345)

Signed, thin events for `lead.created`, `customer.created` and `ticket.created`, delivered after
the creating transaction commits. Code: `internal/models/webhook.go`,
`internal/repository/webhook_repository.go`, `internal/service/webhook_{outbox,signing,endpoint_service,runtime}.go`,
`internal/handler/webhook_handler.go`, `internal/aeo/egress_public.go`, `cmd/webhook_runtime.go`.

- **Registry.** `/webhooks/endpoints` (list, create, read, update, delete) and
  `/webhooks/deliveries` sit behind `RequirePermission(webhooks.manage)` (admin). The handler validates every input before
  calling `WebhookService` (400 `VALIDATION_ERROR`, `error.details` keyed by `url`, `event_types`,
  `body`, `id`, `sort_by`, `endpoint_id`, `status` or `event_type`, the value never echoed).
  `service.ValidateWebhookURL` accepts https without userinfo, query or fragment, at most 2048
  characters, and refuses a non-public IP literal (`aeo.CheckPublicURL`); a host name is judged at
  every connection. `event_types` is stored canonical (deduplicated, sorted, comma-separated) and
  answered as an array. Create generates 32 random bytes, returns `whsec_` + base64 once in the 201,
  and stores `utils.NewSecretBox(API_KEY_SECRET, "webhook-endpoint-secret").Seal(secret)`; no other
  answer carries the secret or `secret_sealed` (`json:"-"`). `is_active` has **no GORM default**
  (GORM substitutes a column default for a zero-value bool, so an explicit false would be lost); the
  service writes true when the request omits it, and `UpdateEndpoint` writes the column
  explicitly. Delete is one `WithTransaction`: a locking read of the endpoint (`FOR UPDATE` on
  MySQL), `DELETE webhook_deliveries WHERE endpoint_id = ?`, then the endpoint.
- **Outbox in the creating transaction.** `WithLeadWebhooks(repo)`, `WithCustomerWebhooks(repo,
  txManager)`, `WithTicketWebhooks(repo, txManager)` and `WithFormWebhooks(repo)`; `cmd/main.go`
  passes one `WebhookRepository` and the shared `txManager`. After the record insert,
  `enqueueWebhookEvent` reads the active endpoints subscribed to the type (a plain read) and inserts
  one `pending` row per endpoint, all with one `evt_<32 hex>` id, `attempts` 0 and `next_attempt_at
  = occurred_at` (UTC, ms), so the row is due at once. Paths: `LeadService.Create`, the form
  create_lead path for a new lead (never a repeat enquiry), `CustomerService.Create` (now one
  transaction when wired), `LeadService.ConvertToCustomer` (inside each retry attempt, so a replay
  commits one row set), `TicketService.Create` and form ticket intake with outcome `created` (never
  `duplicate`). A failure after the record insert rolls back the record and every row. Without the
  options every path is unchanged.
- **Runtime.** `service.WebhookRuntime` runs one pass at start and every `WebhookPollInterval`
  (5 s): `RecoverExpiredLeases` (a `sending` row past `lease_until` back to `pending`, or `failed`
  with `lease_expired` at the cap), a plain `SELECT id … WHERE status = 'pending' AND
  next_attempt_at <= now`, then per row a conditional claim by primary key (`pending → sending`,
  `attempts + 1`, `last_attempt_at`, `lease_until = now + WebhookSendLease` 2 min), one POST, and a
  finish fenced on `status = 'sending' AND attempts = <claimed>`. A missing or inactive endpoint
  fails the row with `endpoint_inactive`, an unopenable secret with `secret_unavailable`; neither
  sends. 2xx: `succeeded`, `last_http_status`, `next_attempt_at` NULL, `error_code` ''. Otherwise
  (`http_status`, `redirect`, `timeout`, `connection_failed`, `destination_refused`) the row goes back
  to `pending` at `now + min(WebhookRetryBase 1 min × 2^(attempts−1), WebhookRetryMax 6 h)`, or
  `failed` when `attempts` reached `models.WebhookMaxAttempts` (10). Finished rows whose
  `updated_at` is older than `WebhookDeliveryRetention` (30 days) are pruned at most once per
  `WebhookPruneInterval` (1 h), by primary key in chunks of 500. Every MySQL zero-changed-row count
  is re-checked with a read of the write's own predicate: the claim, the finish and the lease
  recovery always change `status` or `lease_until`, so a row that no longer matches is a lost race
  (`ErrWebhookDeliveryNotClaimed`, `ErrWebhookDeliveryNotSending`, or skipped), never an identical
  write. Re-checking against the written values instead would let a second claimer with the same
  clock reading count the first claimer's row as its own. The constants are package constants, not
  settings.
- **Signing.** `service.SignWebhook(secret, eventID, unixSeconds, body)` returns `v1,` +
  base64(HMAC-SHA256(base64decode(secret minus whsec_), id + "." + timestamp + "." + body)). Each
  attempt is re-signed with its own `webhook-timestamp`. Headers: `webhook-id`,
  `webhook-timestamp`, `webhook-signature`, `Content-Type: application/json`, `User-Agent:
  GopherCRM-Webhooks/1`. Body: exactly `{"id","type","occurred_at","record_id"}`.
- **Egress.** Requests go through `(*aeo.EgressGuard).PublicHTTPClient(url)` (see
  [AEO_SECURITY.md](AEO_SECURITY.md#public-only-egress-for-webhooks)), 10 s per attempt
  (`WebhookRequestTimeout`), response bodies read up to 4 KiB and discarded. `cmd/webhook_runtime.go`
  builds it from `aeo.NewEgressGuard(config.AEOEgressPolicy{})`, the real resolver and dialer.
- **Drain.** `Stop` cancels the loop: no further claim starts, an attempt already claimed runs to
  its end (request timeout plus a 5 s finish bound), then the goroutine joins. After `Stop`
  returns nil nothing is written. Order in `combineStops`: automation, form retention, bulk sweep,
  webhooks, AEO.
- **Replicas.** Safe on every replica: claims are conditional, and a crashed sender's lease
  expires and is recovered. Delivery is at least once (a crash between the POST and the finish
  resends after the lease) and unordered; consumers deduplicate on `webhook-id`.
- **Privacy.** Rows hold ids, the event type, a status and timestamps; erasure and ticket delete
  leave them (a non-goal). Log lines of the registry, the outbox, the runtime and the handler carry
  `operation`, `endpoint_id`, `delivery_id`, `event_type`, `record_id`, `attempts`, `status`,
  `http_status` and `error_code` only (plus a sanitised database error); never the URL, host,
  secret, sealed value, signature or a body. Transport errors, which quote the URL, are classified
  and never logged.
- **Schema.** `webhook_endpoints` and `webhook_deliveries` (see
  [datamodel.md](datamodel.md#outbound-webhooks)) from `MigrateDatabase` on SQLite and
  `migrations/20261120120000_add_outbound_webhooks` (created only when missing) on MySQL/MariaDB.

Regression roots: `TestWebhookSchemaSQLite`, `TestWebhookJSON` (`internal/models`);
`TestEgressPublicClient` (`internal/aeo`); `TestWebhookRepository{SQLite,MySQL}`
(`internal/repository`); `TestWebhookSignature`, `TestWebhookOutbox{Atomicity,FanOut}{SQLite,MySQL}`,
`TestWebhookDeliveryAfterCommit`, `TestWebhookRetryBackoffAndCap`, `TestWebhookUndeliverableEndpoint`,
`TestWebhookRuntimeLifecycle`, `TestWebhookEgressRefusal`, `TestWebhookRequestTimeout`,
`TestWebhookEndpointService`, `TestWebhookDeliveryLogAndPrune`, `TestWebhookLogsOmitSecrets`
(`internal/service`); `TestWebhookRoutesAdminOnly`, `TestWebhookHandlerValidation`
(`internal/handler`); `TestWebhookAPISQLite` (`tests`); `TestWebhookProductionWiring` (`cmd`);
`TestWebhookMigrationFiles` (now holding the newest-migration assertion), `TestWebhookMigration`
(`cmd/migrate`); `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates`
(`test/integration`). The MySQL roots use a disposable `TEST_MYSQL_DSN` schema, skip without it
and fail when it is unreachable. Operator follow-ups: register the roots in
`scripts/ci/check-go.py`, the new enums (`event_type`, delivery `status`, `error_code`) in
`scripts/ci/check-api-docs.py`, and the migration roots in `.github/workflows/migrations.yml`.

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
  drains after the automation worker and before the stale bulk operation sweep, the webhook sender
  and AEO:
  `combineStops(automation.Stop, formRetention.Stop, bulkRecovery.Stop, webhooks.Stop, aeoRuntime.Stop)`.
- **Replicas.** The purge is safe on every replica, like the automation worker (#237): extra sweeps only
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
are discovered through `form*_test.go` by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them.

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

### Form ticket intake (#213)

A form with `create_ticket` opens a support ticket when a pending submission is confirmed. The
logic lives in `internal/service/form_ticket_intake.go` and
`internal/repository/form_ticket_repository.go`; `cmd/main.go` passes
`service.WithFormTicketIntake(formTicketRepo, txManager)` to `NewFormService`.

- **Definition.** `create_ticket` requires `double_opt_in` and excludes `create_lead`
  (`Form.ValidateDefinition`, `ErrInvalidFormDefinition`, 400 with nothing written); it needs no
  default owner. On `POST /forms` and `PUT /forms/{id}` an absent `create_ticket` is false, and an
  absent `create_lead` is false when `create_ticket` is true. The write guard is unchanged: admin
  and sales.
- **Key.** `POST /forms/public/{key}/submissions` accepts an optional `idempotency_key`
  (`^[A-Za-z0-9._:-]{1,128}$`, not starting with `submission-`, never personal data). The handler
  checks it after binding and before any service call; a refusal is 400 `VALIDATION_ERROR` with
  `error.details.idempotency_key`, never echoing the value. Only the pending row of a
  `create_ticket` form stores it; spam and every other form store `''`. A confirmation without a
  client key uses `submission-<submission id>`, which deduplicates that one submission only. The
  hosted view (`ViewPage`, #340) renders one key per render as `data-idempotency-key` on the
  script tag: 16 bytes from the handler's instance-local `random` reader (`crypto/rand.Reader`,
  read with `io.ReadFull`) as 32 lowercase hex characters, served with `Cache-Control: no-store`
  so no cache hands one render's key to another visitor, never logged, and still without any
  service call. When the reader fails the shell is served without the attribute and one Warn
  line carries only `operation=hosted_view_key` and `error_code=random_unavailable`.
- **Embed script key (#341).** `form_embed.js` resolves one key per script closure (one rendered
  form), once, right after the `data-form-key` check and before the definition request
  (`resolveIdempotencyKey`): the tag's `data-idempotency-key` when it matches
  `^[A-Za-z0-9._:-]{1,128}$` and does not start with `submission-`; otherwise 16 bytes from
  `crypto.getRandomValues` as 32 lowercase hex characters; otherwise (no `window.crypto`, no
  `getRandomValues`, or it throws) no key, and the field is omitted from the body. There is no
  `Math.random` or `crypto.randomUUID` fallback. Every submission of the render sends the same
  `idempotency_key`, including the resend after a non-2xx answer, a 400 field correction or a
  network failure; two forms on one page get distinct keys. The script never logs the key and
  does not retry on its own. Deduplication happens only at confirmation on `create_ticket` forms
  (one ticket per key); `create_lead` and no-action forms ignore the key, so duplicate leads are
  still possible. `embed.js` keeps `Cache-Control: public, max-age=3600`, so a browser may run
  the previous script, which sends no key, for up to an hour after an upgrade.
- **Placement.** Only a confirmation opens a ticket, inside the confirmation transaction after the
  token claim. Lock order: token (claim) → customer (`LockLiveCustomerByEmail`) → ticket → key row
  → submission. Spam and never-confirmed rows open nothing.
- **Customer match.** `deleted_at IS NULL AND email IN (address, lower(address))`, lowest id
  first, `FOR UPDATE` on MySQL/MariaDB and a plain read on SQLite: the rule the unlinked-submission
  erasure (#214) uses. SQLite compares binary (the stored address equal to the submitted one or to
  its lower-cased form); on MySQL/MariaDB the `utf8mb4_unicode_ci` collation of `customers.email`
  decides (case- and accent-insensitive, trailing spaces ignored). No match: no ticket, outcome
  `no_customer` (no customer is created).
- **Ticket.** `open`, `medium`, unassigned, no `due_at`, `first_responded_at` not written; title
  exactly `Form: <form name>` cut to 255 runes, description exactly `Submitted through form <form
  id>, submission <submission id>.`. No submitted value is copied into the ticket. A
  `ticket_created` event (origin `form`, `actor_id` NULL) is recorded in the same transaction.
- **Duplicates.** `CreateTicketWithKey` inserts the ticket and then the key row in one savepoint,
  with no pre-read. The unique `idx_form_ticket_keys_identity (form_id, idempotency_key)` is the
  only unique index of the table, so a duplicate-key error is `repository.ErrFormTicketKeyTaken`;
  the savepoint is rolled back (the ticket with it) and `GetKeyForUpdate` reads the committed row.
  The same customer: `duplicate`, and the submission links the existing ticket. Another customer:
  `key_conflict`, no link. On MySQL a concurrent confirmation's key insert waits on the first one's
  uncommitted entry and then gets the duplicate error, never 1213.
- **Outcomes.** `form_submissions.ticket_outcome` is one of `''`, `created`, `duplicate`,
  `key_conflict`, `no_customer`, `unavailable`, with `ticket_id` set for `created` and `duplicate`.
  A service built without `WithFormTicketIntake` records `unavailable` instead of refusing like
  `ErrTicketCommentMailUnavailable`: the confirmation is public, and a refusal would change the
  public page, roll back the token claim and leave the link failing on every retry, while the
  recorded outcome stays visible to staff. The public answers (`SubmitOutcome` and the confirmation
  page) are identical for every outcome. Form mail is unchanged.
- **Atomicity.** A failure after the ticket insert (key insert, event insert, submission update,
  form mail rows) rolls the whole confirmation back, token claim included, so the link works again.
- **Erasure race (#183).** The confirmation locks token then customer; a customer erasure locks the
  customer then the address's tokens (`scrubUnlinkedSubmissions`). On MySQL one of the two may
  therefore end in error 1213 and roll back whole; the erasure is replayed when it is the victim
  (#385), the confirmation is not retried. Confirmation first: both
  commit and the erasure purges the key row. Erasure first: the confirmation finds no live
  customer (`no_customer`) or an invalid link. No committed state keeps a key row of an erased
  customer.
- **Purges.** `ticketRepository.Delete` hard-deletes the ticket's key rows in its transaction, so
  the key can open a new ticket. Customer erasure (every root) purges the rows of the customer and
  of its tickets; the lead and unlinked-submission scrubs set `idempotency_key` to `''` and purge
  the key rows of their submissions. Each step has a `HasTable` probe; a failure rolls the unit
  back. Keys never expire otherwise.
- **Logs.** The intake logs `form_id`, `submission_id`, `ticket_id`, `ticket_outcome` and error
  codes only, never the key, the address, a submitted value, the form name or the ticket text.
- **Schema.** `forms.create_ticket`, `form_submissions.idempotency_key`, `ticket_id`,
  `ticket_outcome`, `idx_form_submissions_ticket_id` and `form_ticket_keys` (see
  [datamodel.md](datamodel.md#form-ticket-keys)) come from `MigrateDatabase` on SQLite and from
  `20261115120000_add_form_ticket_intake` (each only when missing) on MySQL/MariaDB;
  `ensureFormTicketKeyCollation` gives an AutoMigrate-created key column `utf8mb4_bin`.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema (skipped without it) where
named: `TestFormTicketTargetDefinition`, `TestFormTicketIntakeSchemaSQLite`,
`TestFormTicketIntakeSQLiteUpgrade`, `TestFormSubmissionTicketJSON` (`internal/models`);
`TestFormTicketFlagHandler`, `TestFormPublicSubmitIdempotencyKeyValidation`, and for the hosted
view key (#340) `TestHostedViewRendersPerRenderIdempotencyKey`, `TestHostedViewIsNotCached`,
`TestHostedViewWithoutRandomnessOmitsKey` and `TestHostedFormResubmissionWithSameKeyOpensOneTicket`
(the rendered key, submitted and confirmed twice over HTTP on the real service with ticket intake
and one-connection in-memory SQLite, opens one ticket) (`internal/handler`); for the embed script
key (#341, `internal/handler/form_embed_idempotency_test.go`, all on the script served by
`GET /forms/public/embed.js`) `TestFormEmbedScriptIdempotencyKeyContract` (structure: one
closure-level key, sent under `if (idempotencyKey)` in `handleSubmit`),
`TestFormEmbedScriptIdempotencyKeyNoWeakFallback`, `TestFormEmbedScriptIdempotencyKeyNeverLogged`
(no `console.*` call mentions the key) and `TestFormEmbedScriptIdempotencyKeyBehaviour` (runs the
extracted `resolveIdempotencyKey` in `node`, which it requires and never skips), plus the browser
spec `gocrm-ui/e2e/tests/form-embed-idempotency.spec.ts` (the real served script on SQLite: the
same key on a resend after a 500 and after an aborted POST, stored on both pending rows; distinct
keys for two tags; no `idempotency_key` on a keyless render);
`TestFormTicketKeyRepository{SQLite,MySQL}`, `TestFormTicketCustomerMatch{SQLite,MySQL}`,
`TestFormTicketKeyErasure{SQLite,MySQL}`, `TestTicketDeletePurgesFormTicketKeys{SQLite,MySQL}`
(`internal/repository`); `TestFormTicketIntakeConfirmed{SQLite,MySQL}`,
`TestFormTicketIntakePendingSpamAndKeyless`, `TestFormTicketIntakeDuplicateKey{SQLite,MySQL}`,
`TestFormTicketIntakeKeyConflict{SQLite,MySQL}`, `TestFormTicketIntakeNoCustomer`,
`TestFormTicketIntakeUnwired`, `TestFormTicketIntakeAtomicity{SQLite,MySQL}`,
`TestFormTicketIntakeVsCustomerErasure{SQLite,MySQL}` (both MySQL orderings forced, each wait seen
in `information_schema.PROCESSLIST` on two consecutive polls) and
`TestFormTicketIntakeLogsOmitPersonalData` (`internal/service`); `TestFormTicketIntakeAPISQLite`
(`tests`); `TestFormTicketIntakeErasureLeavesNoPersonalDataInAnyTable` and
`TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates` (`test/integration`);
`TestFormTicketIntakeMigrationFiles`, `TestFormTicketIntakeMigration` (`cmd/migrate`);
`TestFormTicketIntakeProductionWiring` (`cmd`).

Operator follow-ups: register the new roots in `scripts/ci/check-go.py` and the migration roots in
the Database migrations workflow; add the `ticket_outcome` closed set to
`scripts/ci/check-api-docs.py`; register the #340 and #341 handler roots in
`scripts/ci/check-go.py`.

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
