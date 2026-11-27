# Entities

> **For:** Developers changing the rules of the business entities. Part of the [documentation index](../README.md).

The rules of the business entities: quotes and their links, mail delivery, ordered and paginated
reads, tasks and tickets, list filters, lookup indexes, lead owners and the lead and company lock
order, and the bulk status endpoints. Deleting personal data is in
[erasure.md](erasure.md). Part of the developer guide; the index is in [development/README.md](./README.md).
Quotes are in [quotes.md](quotes.md), mail delivery and ticket comment email in [mail.md](mail.md),
and ordered reads, list filters and lookup indexes in [reads-and-filters.md](reads-and-filters.md).

## Tasks and tickets

**A task's deal link (#240).** `tasks.deal_id` is additive to the lead-or-customer rule and follows
`service.CanViewDeal` (`internal/service/deal_visibility.go`), decided by the caller's `deals.view`
scope: `all` any deal, `own` the caller's deals, not held none (on the system matrix that is admin,
sales and every other role). `checkDealLink` is the write side: a missing and a hidden deal both give
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

**The ticket update transaction (#242, #408).** `ticketService.Update` takes the transactional
path `updateWithEvents` (locking read, checks against the locked row, save, events) when automation
events **or** webhooks are wired; with webhooks only it records no automation event
(`automationTx` returns nil) and enqueues `ticket.updated` when a stored value changed. With neither
it stays the plain read and write.

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
(`cmd/migrate`). The MySQL roots skip without `TEST_MYSQL_DSN`; the Database migrations workflow
runs the migration roots.

## Leads and companies

**Lead owners (#255)** — `POST /leads`, `PUT /leads/{id}` and `POST /leads/{id}/assign` share one
check, `checkLeadOwner` in `internal/service/lead_service.go`: the owner must be a live, active admin
or sales user, otherwise `apperrors.ErrInvalidLeadOwner`, which the handlers answer with 400
`INVALID_REFERENCE`, `details.field` naming the body field and one message per route whatever the
reason, so a caller cannot probe which ids exist or what they hold. The reason (`unknown`, `inactive`,
`role`) goes to the log with the ids and nothing else. An erased user's row still satisfies the
foreign key, but no scoped read finds it, so it counts as unknown; a lookup that fails for a database
reason stays a 500. The read runs inside the writing transaction and takes a row lock (the user
repository bound to `repository.LockingRead(tx)`): a plain read in MySQL's
REPEATABLE READ transaction would answer from the snapshot of an earlier plain read, such as the
company check, and miss a deactivation committed since. The owner's custom role and its permissions
are read in the same transaction with a shared lock (`UserRepository.GetByIDWithCurrentRole`, #677),
so a role change committed before the check is seen too; a role write in flight makes the check wait.
That makes the check current, not permanent:
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
**leads** (id ascending) → **customers** → **deals** (see [concurrency.md](concurrency.md)).

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
`AutoMigrate` without foreign keys), skip without it and fail when it is unreachable. CI limit: `make verify-backend` has no DSN and skips them; `scripts/ci/check-go.py transaction`
discovers the MySQL roots through its `lead*`, `company*` and `bulk*` test-file patterns.

## Bulk endpoints

**Bulk endpoints** — only `POST /leads|tickets|tasks/bulk/status` exist, registered by
`SetupBulkStatusRoutes` outside the entity groups. `BulkHandler` depends on `BulkStatusService`
(`internal/service/bulk_status_service.go`), which holds nothing but the three status operations.
The route's permission guard runs first (for tickets the handler checks `tickets.update` before
reading the body); the handler then passes the caller's `leads.update` / `tickets.update` /
`tasks.update` scope to `bulkStatusService.BulkSet*Status(ctx, actorID, scope, ...)`. Scope `all`
may change any record, scope `own` only the caller's own records (leads it owns, tickets and tasks
assigned to it), and any other scope is refused. Each one normalises the ids (1–100, duplicates
count once), checks the scope, writes a pending
`bulk_operations` history row, then inside ONE transaction reads every listed record through
`BulkStatusRepository`, refuses the whole batch if any id is missing, not the caller's to change
or in a forbidden transition (the offending ids go into `error.details`), and only then writes
the status — plus, for leads and tickets (#242), the automation events on the same transaction;
the lead, ticket and task reads are locking reads in id order (#182, #242, #558) — and, as the last
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
from `migrations/20261110120000_add_bulk_operation_status_index.up.sql` on MySQL/MariaDB. `failed`
therefore means none of the batch's records changed, with one exception: rows already `pending`
when this was deployed recorded `completed` after their commit under the old code, so their batch
may have committed; they are swept to `failed` all the same (outcome unknown, documented, not
stored). Sweep log lines carry only the `failed` count and, on failure, the error through
`utils.LogError`. The runtime runs on a `lifecycle.Loop` (`internal/lifecycle`): `Stop` cancels the
sweep and joins its goroutine under main's shared deadline, and `Start` after `Stop` does nothing.
Since #453 every sweep (wired with `service.WithOIDCAttemptPruning`) also prunes the single sign-on
attempts created more than `service.OIDCLoginAttemptRetention` (one day) earlier
(`service.PruneOIDCLoginAttempts`: a plain read of up to 500 ids, then a delete by primary key
repeating the predicate, until a chunk deletes fewer than 500). The prune does not change the
sweep's count or error; a failure is logged through `utils.LogError` and a non-zero count as
`pruned single sign-on attempts` with the `pruned` field.
Since #485 both prunes (this one and the sign-in audit prune of #454) run their statements on the
sweep's context, so `Stop` cancels a prune that is waiting for a database connection instead of
letting it use up main's shared drain deadline.

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
`bulk*_test.go` are discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them. The Database migrations workflow runs the
migration roots.
