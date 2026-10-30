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
make run            # Run the app (go run cmd/main.go)
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
be committed). Services still call the manager with `context.Background()`; request-context
propagation is tracked in #107.

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
erased by another connection just after that lookup would still look active and the full-row
password update would write the stale `is_active` back. A failure of the password
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
looked up, then claimed by `RefreshTokenRepository.ClaimForRotation`, a conditional update
(`is_revoked = false AND expires_at > now`, exactly one affected row, else the generic rejection),
the owner is re-read with the same locking read as the reset flow (`FOR UPDATE` on MySQL, a plain
read on SQLite), the successor row is inserted and its access JWT is signed. A failure of the
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
cases in `internal/service/auth_session_test.go`. The frontend still sends several refreshes when
parallel requests see a 401 at once; the backend now rejects all but one of them, and coalescing
them client-side is tracked separately (#115).

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
the same fallback the password reset and the forms mail already share, documented in the README's
known limitations. The subject is fixed (`Quote Q-000042 from GopherCRM`),
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
with `ErrInvalidAddress` before `MAIL FROM` otherwise. The headers carry
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
`TestSMTPHeaders`, `TestSMTPTLSAuth`, `TestSMTPFailureRedaction`.

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

**Middleware stack** (in order): CORS → RequestID → Logger → Recovery → ErrorHandler → Auth →
RateLimit.

**Pagination** — every list endpoint parses `offset`/`limit` through `utils.ParseOffsetLimit`
(`internal/utils/response.go`). It defaults to `offset=0, limit=20`, caps `limit` at 100, ignores
non-positive and unparseable values, and **never returns `limit=0`** — callers divide by it when
building pagination metadata, and `?limit=0` used to panic that arithmetic and turn every list
endpoint into a 500. Do not hand-roll query parsing in a handler; call the helper.

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
the status — plus, for leads, the automation events on the same transaction. The history row is
set to `completed` or `failed` afterwards; a failure to do so is logged and never changes the
response. There is no generic bulk create/update/delete/action machinery any more (#112); a new
bulk operation gets its own narrow service method and repository halves the same way, and a bulk
erasure would need per-item isolation designed afresh (each erasure is one savepoint-bracketed
unit, `runErasureUnit`, but a batch would also have to report per-item failures and authorize
every item's whole graph).

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
CI selects health smoke and affected browser suites, with full coverage for shared or unknown application changes, on
MySQL, MariaDB and SQLite. Runner-only changes use hygiene and runner/probe tests, API/frontend
build checks, and health smoke rather than unrelated application suites. Local
`make e2e` still defaults to the full suite. See [validation and releases](VALIDATION_RELEASE.md)
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
- `JWT_SECRET` — required, minimum 32 characters
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
  ticket links are business links: never followed, never cleared. `customers.user_id` itself is
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
  does (a business record whose `lead_id` still resolves), and that is safe because their text
  cannot name the person: a task's title and description accept only the non-personal
  placeholders (`lead.id`, `lead.status`, `lead.source` and the `deal.*` fields), a personal one
  (`lead.first_name`, `lead.email`, `lead.phone`, `lead.company`, `owner.*`) is refused when the
  rule is validated, and the task renderer blanks it even in a rule stored before the allowlist
  was narrowed. A user erasure hard-deletes the notifications addressed to the user alongside the
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
| `form_submissions` linked by `lead_id` | `data` → `{}`; `email` → the lead's placeholder; `ip_address`, `user_agent`, `referrer` | form, status, `lead_id` |
| `form_confirmation_tokens` of those submissions | hard-deleted | — |
| `api_keys`, `refresh_tokens`, `password_reset_tokens` of an erased user | hard-deleted | — |
| `notifications` addressed to an erased user, or whose entity is an erased lead | hard-deleted | notifications about deals and other leads |
| `ticket_comments` written by an erased user (live and soft-deleted) | `body` → `[deleted]` | ticket, author id, `is_internal`, deletion state |
| `automation_runs` of an erased lead | pending/waiting/running → `cancelled` / `record_deleted` | finished runs (ids and statuses only) |
| `tickets` (`title`, `description`, `resolution`), `tasks` (`title`, `description`), `deals` (`title`, `notes`, `lost_reason`, `source`), `quotes` (`title`, `notes`), `quote_lines` (`description`), `companies` (`notes`), staff-written ticket comments | not touched | Business records. Free text may have been typed to contain a name; that is an accepted limitation, not scrubbed (no reliable way to tell a name from content) |
| `automation_events`, `automation_run_steps`, `deal_stage_changes`, `quote_status_changes`, `bulk_operations` | not touched | Their writers store ids, statuses, values of non-personal fields and error codes |
| `bulk_operation_items` (`data`, `error`) | not touched | Legacy rows written by the generic bulk subsystem removed in #112 can hold request payloads; nothing writes the table any more (retirement is on the roadmap) |
| `form_submissions` with no `lead_id` (spam, never-confirmed opt-ins, forms that create no lead) | not touched | Not linked to any person the CRM can erase; a retention concern (roadmap) |
| Logs, already-issued JWTs | out of reach | See below |

The whole-schema sweep tests (`test/integration/erasure_pii_sweep_test.go`,
`internal/repository/identity_erasure_test.go`) seed the person into every scrubbed target above
first and assert the sweep can see each copy before the erasure; an empty table would prove
nothing.

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
  the cascade; opposite entry points can deadlock (an error, never a partial commit). Tracked in
  #183.
- **History.** Deploying a new erasure rule does not repair graphs erased under an older one (see
  above); fully soft-deleted graphs have no supported path yet.
- **Retained content.** Business free text (tickets, tasks, deals, quotes, quote lines, company
  notes, staff-written comments), legacy `bulk_operation_items` payloads and unlinked form
  submissions are kept by design (see the matrix); logs and issued JWTs are out of reach.

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

The REST surface is enumerated in the [README](../README.md#api-documentation). A generated
Swagger 2.0 spec is checked in under `api/swagger.json` and `api/swagger.yaml`, built from the swag
annotations on the handlers and the general block in `cmd/main.go`. Regenerate it with
`make swagger` (runs the pinned swag CLI via `go run`) after changing a handler or a route; the spec
is not served by the application, and the router in `internal/handler/` remains the source of truth
when the two disagree.

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

### Automation runtime

Follow-up automation (#164) is the second owner of background work. `internal/service/
automation_engine.go` executes rules, `automation_runtime.go` owns the worker goroutine, and
`cmd/automation_runtime.go` wires both into `setupDependencies`, which returns one drain handle
(`combineStops`) that `main` runs — automation first, then AEO — before `models.CloseDatabase`.
The pieces, and why each is shaped the way it is:

- **The outbox event is written in the write transaction.** `LeadService.Create/Update/
  ConvertToCustomer`, the form submission path, the bulk lead status path and
  `DealService.Create/Update/ChangeStage` take the `AutomationRepository` through a functional
  option (`WithLeadAutomationEvents` and friends) and record `automation_events` rows on the
  transaction-bound repository, so a write that rolls back records nothing and a refused bulk
  status batch records nothing for any of its leads. A create records `*_created`; an update `*_updated` plus
  `*_status_changed` / `*_stage_changed` with `from_value` and `to_value` when the value differs;
  the stage endpoint `deal_stage_changed` alone; the same stage again nothing. Events carry ids,
  types, statuses and the origin (`user`, `form`, `bulk`, `conversion`, `automation`), never a
  name or an address.
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
  `send_email` is one `WithTransactionAndRetry` transaction that, in order, re-reads the lead or
  deal with the transaction-bound locking read (`GetLeadForUpdate` / `GetDealForUpdate`, `SELECT …
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
  handle (`aeoDrainTimeout`, 15 s, shared with the AEO drain that runs after it). A drained
  runtime holds no goroutine, and a repeated `Stop` is a no-op that returns nil
  even on a context that has already ended: the join and the admission drain check their
  "already done" state before they look at the deadline. The same crash policy as AEO applies when
  the deadline passes: the process exits without closing the database and the next boot reconciles.
- **Erasure hooks.** See *Deleting personal data* above: lead erasure cancels the lead's runs and
  purges its notifications before the scrub; user erasure purges the user's notifications.
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
