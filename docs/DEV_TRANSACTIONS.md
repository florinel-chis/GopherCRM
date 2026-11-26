# Transactions and request context

> **For:** Developers changing transactions or request-context handling. Part of the [documentation index](README.md).

How the transaction manager behaves on every driver, the service transaction unit that
binds repositories, webhook events and automation writes to one transaction, and which entry
points run on the request context. The table that says which primitive to choose is under
[Transactions and row locks](DEVELOPMENT.md#transactions-and-row-locks); the lock order of every
flow is in [CONCURRENCY.md](CONCURRENCY.md). Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

## Transaction manager

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
- **Retries.** `WithTransactionAndRetry` replays deadlocks (error 1213), lock wait timeouts
  (1205), MariaDB "record has changed since last read" (1020) and SQLite busy/locked errors in a
  fresh root transaction, classified from the typed driver error by `utils.RetryReasonOf`
  (`internal/utils/retryable.go`), never from the message text, at most `maxRetries` times, and only when the failed attempt
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
`TEST_MYSQL_DSN` server), `TestRetryReasonOf` and `TestTransactionRetryableError{SQLite,MySQL}`
(the classification against real SQLite busy and UNIQUE errors and real MySQL/MariaDB deadlock,
lock wait timeout and duplicate-key errors, and on MariaDB a real 1020 that is replayed),
`TestTransactionFinalizationFailures` (a connection-pool wrapper that
injects begin/commit/rollback/savepoint failures deterministically, including poisoned commits
across several nesting levels) and `TestTransactionRecoveredNestedPanicCannotCommitFailedRollback`
(the independent reproduction of a recovered nested panic whose failed savepoint rollback used to
be committed).

## Service transaction units

**Service transaction units (#615)** — `internal/service/txunit.go` is the shared way a service
runs one write in one transaction. `newTxDeps(txManager)` panics on a nil manager (a service
built without one is a wiring mistake, not a mode) and holds the webhook emitter (`webhook.Nop`
unless set), the optional automation repository and the base repositories to bind.
`txDeps.Run(ctx, fn)` calls `WithTransaction` once on the caller's context, resolves the
transaction once (`utils.ErrNoTransaction` if it is missing) and hands `fn` a `txUnit`: `Tx`, the
repositories bound with `WithTx(Tx)`, `Webhooks` (`emitter.Bind(Tx)`) and `Automation`
(`automationTx`, nil without an automation repository); a locking read binds a repository to
`repository.LockingRead(u.Tx)`, the `FOR UPDATE` handle with its fresh `Session`. `fn`'s error is
returned unchanged. Called inside an enclosing transaction it nests under a savepoint. The
customer service is the first user (`CustomerService.Create` and `Update`); the other services
keep their own transaction code for now. Regression root: `TestTxUnit` (`internal/service`).

## Request context

**Request context (#107)** — lead conversion (including the handler's ownership read), lead
assignment (`LeadService.Assign`, #255), the public form submission and confirmation, the
three routed bulk status updates, customer create and update (`CustomerService.Create` and
`Update`, #615) and the three erasures (`CustomerService.Delete`,
`LeadService.Delete`, `UserService.Delete`/`DeleteAs`, #571) run on the HTTP request's context: the handler passes
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
these roots, and neither are `CustomerService.Create` and `Update` (#615), which run their one
transaction (`txDeps.Run`) on the request context too: none of them sends mail or commits an audit
row after its commit, so none refuses an enclosing transaction; called inside one, each nests
under a savepoint. The erasures are not roots
either: on a caller's transaction (`WithTx`, the governed user delete) the erasure unit nests
under a savepoint of its own (see *Deleting personal data*, Cancellation).

Not yet bound: the conversion settings (`ConfigurationService` takes no context), the plain
`GetByID` pre-reads of the three erasures, and every service method not named above
(`LeadService.Create` and `Update` among them), which still passes `context.Background()`. AEO runs deliberately keep the service-owned lifecycle context and never
take a request's. Regression roots: `TestRequestCancellation{SQLite,MySQL}`
(one-connection pool; cancelled while the first read, the transaction or the spam write waits,
cancelled before the call, and the bulk outcome recorded `failed`, or left `pending` until a sweep
fails it, when its bounded write cannot get the connection), `TestRequestCancellationMySQLLockWait`
(queued behind another transaction's row lock, observed as the same statement still executing in
`information_schema.PROCESSLIST` on two consecutive polls, which works on MySQL 8 and MariaDB
10.11), `TestRequestCancellationCaptchaVerifier` (a stalled verification server),
`TestRequestRootRequired{SQLite,MySQL}`, `TestTransactionManagerDB{SQLite,MySQL}` and, in the
handler package, `TestConvertToCustomer_CancelledWhileTheOwnershipReadWaits`.
