# Background runtimes

> **For:** Developers changing work that outlives a request. Part of the [documentation index](README.md).

The work that outlives a request: the AEO runtime and its shutdown, the automation runtime,
outbound webhooks, the form retention purge, form mail delivery, form attribution, form ticket
intake, and the AEO runtime validation. The shared building blocks are summarised under
[Background runtimes and outbound requests](DEVELOPMENT.md#background-runtimes-and-outbound-requests).
Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

## AEO background work and shutdown

The API owns AEO goroutines beyond individual requests. `cmd/aeo_runtime.go`
holds them: `service.NewAEORuntime` wires the service together with the owner of its runs, the
daily scheduler is a joinable `aeo.Scheduler` armed whenever `AEO_SCHEDULE_ENABLED` is on, and
`setupDependencies` returns the stop handle `main` drains — through `drainAEO`, bounded by
`aeoDrainTimeout` (15 s) — before `models.CloseDatabase`. HTTP requests then have their
existing separate 5 s shutdown deadline. Inside the service, admission and the
active-run count share one mutex (a `lifecycle.Owner`), separate from `startMu`, so shutdown never queues
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
  (lock wait timeout), or MariaDB error 1020 (record changed since it was read), instead of a
  duplicate key; `utils.RetryReasonOf` classifies the typed driver error, and 1020 is handled
  like a deadlock. A slot claim then looks the slot up first and
  answers `ErrAEOScheduleSlotTaken` when a row holds it; otherwise the INSERT is replayed, at
  most 3 attempts in total per claim, without sleep or backoff, and every attempt goes through
  the duplicate-key classification (after a deadlock the winner may not have committed yet, so
  the replay waits for it and ends as a duplicate key). A deadlock or a 1020 on all three
  attempts is `ErrRunInProgress`, never the raw driver error; for a scheduled claim that is the scheduler's
  "a run is already in progress" skip, so the slot is not retried. A lock wait timeout is
  replayed once and a second one on the same claim is returned unchanged as the driver error:
  each arrives only after `innodb_lock_wait_timeout` (50 s by default) and `startMu` is held
  across the claim, so the worst case is two lock waits during which this process starts no other
  run. The replay is safe because a failed attempt left nothing behind: GORM runs the INSERT in
  its own short transaction, which the server rolls back on a deadlock or a 1020 and GORM rolls back after
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
`migrations/20261114120000_add_aeo_run_ownership.up.sql` (each column and index only when missing) on
MySQL/MariaDB; see [datamodel.md](datamodel.md#aeo-run-ownership). Regression roots:
`TestAEORunOwnership{SchemaSQLite,SQLiteUpgrade,JSONHidden}` (`internal/models`);
`TestAEORunOwnershipMigrationFiles` (the newest-migration assertion has since moved to
`TestAutomationRunLeaseMigrationFiles`, #237) and
`TestAEORunOwnershipMigration` (`cmd/migrate`); `TestAEORun{Claim,Finish,Heartbeat,AbandonedSweep}{SQLite,MySQL}`
and `TestAEORunClaimContention{SQLite,MySQL}` (#371, injected deadlock, lock wait timeout and 1020)
(`internal/repository`); `TestAEOReplica{BootSparesLiveRun,Admission,ScheduleSlot,DeadOwnerRecovered,FinalizeAfterTakeover}{SQLite,MySQL}`,
`TestAEORunHeartbeatLifecycle` and `TestAEOReplicaLogsOmitPersonalData` (`internal/service`,
two service instances with separate pools on one SQLite file or one disposable `TEST_MYSQL_DSN`
schema); `TestSchedulerPassesSlot`, `TestSchedulerSlotTakenSkip`,
`TestEngineFinalizeRefusedWhenNotOwned` (`internal/aeo`); `TestAEOReplicaAPISQLite` (`tests`);
`TestAEOReplicaProductionWiring` (`cmd`). The MySQL roots skip without the variable and fail
when it is unreachable; `scripts/ci/check-go.py` discovers them through its `aeo*_test.go`
patterns (the `aeo` and `transaction` profiles). Registering the migration roots
`TestAEORunOwnershipMigration{,Files}` in the Database migrations workflow
is an operator follow-up.

**AEO export (#244).** `GET /aeo/export` (`AEOHandler.Export`, `internal/handler/aeo_export.go`)
sits in the `/aeo` read group, so admin, sales and support reach it and the customer role gets 403
from the `aeo.view` guard `handler.Protected` builds from its `RouteKeys` entry, before the handler
runs.

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

## Automation runtime

Follow-up automation (#164) is the second owner of background work. `internal/service/
automation_engine.go` holds the engine type that executes rules, with its lifecycle, dispatch,
schedule, run-lease, step, email-step and stale-run recovery code in `automation_lifecycle.go`,
`automation_dispatch.go`, `automation_schedule.go`, `automation_run_lease.go`,
`automation_steps.go`, `automation_email_step.go` and `automation_recovery.go`;
`automation_runtime.go` owns the worker goroutine (a `lifecycle.Loop` that also ends with the
engine's owned context; the engine's admission count is a `lifecycle.Owner`, both in
`internal/lifecycle`; `Start` after `Stop` is a no-op, so a stopped worker is never armed again), and
`cmd/automation_runtime.go` wires both into `setupDependencies`, which returns one drain handle
(`combineStops`) that `main` runs before `models.CloseDatabase`. The combined handle stops the
automation worker, the form retention purge, the stale bulk operation sweep (#196), the password
reset queue (#554: the request in flight finishes, the waiting ones are dropped and counted), the
webhook sender (#345) and AEO together: every handle starts at the same moment under the one shared
deadline, each cancels its own work before it waits, and the combined call returns only after
every handle has returned, with their errors joined in argument order (#561). No handle depends
on another, so a slow drain does not delay the others; `TestAutomationRuntimeLifecycle` (`cmd`)
covers this.
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
    `migrations/20261116120000_add_automation_run_leases.up.sql` (each column only when missing, no
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
  handle (`aeoDrainTimeout`, 15 s, shared with the form retention, bulk operation sweep, webhook
  and AEO drains, which run at the same time). A drained
  runtime holds no goroutine, and a repeated `Stop` is a no-op that returns nil
  even on a context that has already ended: the join and the admission drain check their
  "already done" state before they look at the deadline. The same crash policy as AEO applies when
  the deadline passes: the process exits without closing the database, the run's lease goes stale
  and the next sweep on any worker replica recovers it.
- **Erasure hooks.** See *Deleting personal data* in [DEV_ERASURE.md](DEV_ERASURE.md#deleting-personal-data): lead erasure cancels the lead's runs and
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
- **Rule validation.** `automationService.validateRule` (`internal/service/automation_validation.go`) collects every
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

## Outbound webhooks (#345)

Signed, thin events for `lead.created`, `customer.created` and `ticket.created` (#345), for
`lead.updated`, `customer.updated` and `ticket.updated` (#408), for `lead.deleted`,
`customer.deleted` and `ticket.deleted` (#437), for `deal.created`, `deal.updated` and
`deal.deleted` (#515), for `task.created`, `task.updated` and `task.deleted` (#516), for
`company.created`, `company.updated` and `company.deleted` (#517) and for `quote.created`,
`quote.updated` and `quote.deleted` (#518), delivered
after the writing transaction commits. Code: `internal/models/webhook.go`,
`internal/repository/webhook_repository.go`, `internal/webhook/{emitter,runtime,endpoint_service,signing}.go`,
`internal/handler/webhook_handler.go`, `internal/egress/`, `cmd/webhook_runtime.go`.

- **Registry.** `/webhooks/endpoints` (list, create, read, update, delete, test, rotate-secret) and
  `/webhooks/deliveries` sit behind the `webhooks.manage` guard (admin) that `handler.Protected`
  builds from their `RouteKeys` entries. The handler validates every input before
  calling `WebhookService` (400 `VALIDATION_ERROR`, `error.details` keyed by `url`, `event_types`,
  `body`, `id`, `sort_by`, `endpoint_id`, `status` or `event_type`, the value never echoed).
  `webhook.ValidateWebhookURL` accepts https without userinfo, query or fragment, at most 2048
  characters, and refuses a non-public IP literal (`egress.CheckPublicURL`); a host name is judged at
  every connection. `event_types` takes any non-empty subset of the twenty-one types
  (`webhook.AllEventTypes`); the raw array may hold at most 32 entries before deduplication
  (`webhookEventTypesMaxItems`, raised from 20 by #518 so an array listing every type once is
  accepted; `TestWebhookEventTypesMaxItemsCoversCatalogue` keeps it at least the number of
  types), is stored canonical (deduplicated, sorted, comma-separated; all
  twenty-one are 302 bytes in the `varchar(512)` column, widened from `varchar(255)` by migration
  `20261129120007_widen_webhook_event_types`, whose down half refuses while a row is longer than
  255 bytes) and answered as an array. Create generates 32 random bytes, returns `whsec_` + base64 once in the 201,
  and stores `utils.NewSecretBox(API_KEY_SECRET, "webhook-endpoint-secret").Seal(secret)`; no other
  answer carries the secret or `secret_sealed` (`json:"-"`). `is_active` has **no GORM default**
  (GORM substitutes a column default for a zero-value bool, so an explicit false would be lost); the
  service writes true when the request omits it, and `UpdateEndpoint` writes the column
  explicitly. Delete is one `WithTransaction`: a locking read of the endpoint (`FOR UPDATE` on
  MySQL), `DELETE webhook_deliveries WHERE endpoint_id = ?`, then the endpoint.
  `POST /webhooks/endpoints/:id/test` (#501, `WebhookService.TestEndpoint`) sends one signed
  `webhook.ping` (body `{id, type, occurred_at}`, no `record_id`; `models.WebhookEventPing` is not
  in `webhook.AllEventTypes`, so it cannot be subscribed to) synchronously through `sendWebhook`,
  the send path `WebhookRuntime.attempt` uses (public-only client, `WebhookRequestTimeout`, drain,
  no redirect followed, the same error codes). It works on an inactive endpoint, is not retried and
  writes nothing: the endpoint read is its only statement. An unopenable secret answers 200
  `secret_unavailable` without network work; a service built without `WithWebhookPingClients`
  (wired with the egress guard in `cmd/main.go`) answers 500. One log line carries
  `operation=webhook_endpoint_test`, `endpoint_id`, `outcome`, `error_code` and `http_status` only.
  `POST /webhooks/deliveries/:id/redeliver` (#502, `WebhookService.RedeliverDelivery`) reads the
  delivery (a miss is 404), then its endpoint (missing or inactive: `ErrWebhookEndpointUnavailable`,
  409), then calls `WebhookRepository.RequeueDelivery`: one `UPDATE webhook_deliveries … WHERE id = ?
  AND status IN ('succeeded','failed')` that sets `pending`, `attempts` 0, `next_attempt_at` and
  `updated_at` now (UTC), `lease_until` and `last_http_status` NULL and `error_code` ''; `event_id`
  and `last_attempt_at` keep their values and no row is inserted. No locking read and no
  transaction: the predicate in the statement keeps it apart from a claim (which needs `pending`)
  and from a prune pass (whose `DELETE` re-checks `status IN (succeeded, failed) AND updated_at <
  cutoff`). A zero-changed-row count is re-checked with the write's own predicate, as for the claim
  (the write always changes `status`, so it is never identical); no match is
  `ErrWebhookDeliveryNotRequeued`, which the service turns into 404 when the row is gone and
  `ErrWebhookDeliveryNotRedeliverable` (409) otherwise. The endpoint check is a read before the
  write, not part of its predicate: an endpoint deactivated in between still gets the row requeued,
  and the runtime then fails it with `endpoint_inactive`. The record the event names is never read.
  Log lines (Info on success, Warn per 409) carry `operation=webhook_delivery_redeliver`,
  `delivery_id`, `endpoint_id` and `error_code` only. Tests: `TestWebhookRepository{SQLite,MySQL}`
  (the requeue subtests), `internal/service/webhook_redeliver_test.go`
  (`TestWebhookRedeliver{SQLite,MySQL}`, registered in the transaction profile of
  `scripts/ci/check-go.py`), `TestWebhookRedeliverHandler`.
  `POST /webhooks/endpoints/:id/rotate-secret` (#503, `WebhookService.RotateEndpointSecret`)
  generates a new secret, seals it under the same context and calls
  `WebhookRepository.RotateEndpointSecret`: one `UPDATE webhook_endpoints SET
  previous_secret_sealed = secret_sealed, previous_secret_expires_at = ?, secret_sealed = ?,
  updated_at = ? WHERE id = ?` through `db.Exec`, so the assignment order is explicit (MySQL
  evaluates SET left to right, SQLite reads the old row), with `previous_secret_expires_at` = now +
  `WebhookSecretOverlap` (24 h). No read before it and no locking read: concurrent rotations
  serialise on the row lock and each keeps the other's secret as the previous one. Zero affected
  rows is re-checked with a count by id (missing is 404). It works on an inactive endpoint and
  never opens a stored secret, so it replaces one sealed under an older `API_KEY_SECRET`; it
  answers 200 with `models.WebhookEndpointSecretRotated` (the endpoint plus `secret`, shown once)
  and logs `operation=webhook_endpoint_rotate_secret` and `endpoint_id` only. `UpdateEndpoint`
  still writes only `url`, `event_types` and `is_active`.
- **Catalogue and emitter.** `internal/webhook/catalog.go` is the one registry of the entities
  that emit events: `NewCatalogue` and the default catalogue (`Lead`, `Customer`, `Ticket`,
  `Deal`, `Task`, `Company`, `Quote`, in that order; `defaultCatalogue`) give every event type (`AllEventTypes`, sorted), the exact check (`IsEventType`; the
  ping's type is never one), and the two refusal messages (`SubscribeRule`, behind
  `webhook.WebhookEventTypesRule`, and `FilterRule`, used by the handler and by
  `WebhookService.ListDeliveries`). `TestCatalogueSwaggerEnums` keeps the seven event type `enum`
  lists of `api/swagger.json` equal to it. `internal/webhook/emitter.go` holds the outbox writer:
  `NewEmitter` returns an `Emitter` (the no-op `Nop` for a nil repository), whose `Bind(tx)` gives a
  `TxEmitter` with `Created`, `Updated` and `Deleted(ctx, entity, ids...)`; an entity the catalogue
  does not register is refused before any read. From this module `internal/webhook` imports
  `internal/errors`, `internal/models`, `internal/repository`, `internal/utils`, `internal/egress`
  (the outbound request guard) and `internal/lifecycle` (the worker runtime); it must not reach
  `internal/aeo`, `internal/service` or `internal/handler`, directly or through other packages, which
  `internal/webhook/boundary_test.go` `TestWebhookImportBoundary` enforces. A new entity
  needs one catalogue entry and one emitter call per write path; the event types, the check and
  the messages follow.
- **Outbox in the creating transaction.** `WithLeadWebhooks(e)`, `WithCustomerWebhooks(e)`,
  `WithTicketWebhooks(e, txManager)`, `WithFormWebhooks(e)`, `WithDealWebhooks(e)`,
  `WithTaskWebhooks(e, txManager)`, `WithCompanyWebhooks(e)` (#517) and `WithQuoteWebhooks(e)`
  (#518, an option of both `NewQuoteService` and `NewQuoteServiceWithResponseLinks`) take a
  `webhook.Emitter`;
  `cmd/main.go` builds one `webhook.NewEmitter(webhookRepo)` and passes it, to the ticket and task
  options with the shared `txManager`. Each service holds `webhook.Nop` without the option (a nil
  emitter means `Nop`; `WithTicketWebhooks` sets the emitter and its transactional paths, and
  `WithTaskWebhooks` its emitter, only when both arguments are non-nil). The customer service takes the manager as a required constructor
  argument (`NewCustomerService(..., txManager, opts...)`, #615), so its create and update are
  always transactional. After the record insert,
  the bound emitter reads the active endpoints subscribed to the type (a plain read) and writes
  one `pending` row per endpoint in one batched INSERT in that transaction (pinned by
  `TestWebhookOutboxBatchInsert{SQLite,MySQL}`), all with one `evt_<32 hex>` id, `attempts` 0
  and `next_attempt_at = occurred_at` (UTC, ms), so the row is due at once. Paths: `LeadService.Create`, the form
  create_lead path for a new lead (never a repeat enquiry), `CustomerService.Create` (always one
  transaction), `LeadService.ConvertToCustomer` (inside each retry attempt, so a replay
  commits one row set), `TicketService.Create` and form ticket intake with outcome `created` (never
  `duplicate`), `DealService.Create` (after the stage-history row and the automation event),
  `TaskService.Create` / `CreateWithLabels` (#516, the task, its `task_labels` rows and the outbox
  rows in one `txManager` transaction, the checks before it; a failure resets the task id to 0)
  and the automation `create_task` step (`task.created`, #516, in the step transaction; a dry run
  writes neither the task nor a row), and `CompanyService.Create` (`company.created`, #517: with
  `WithCompanyWebhooks` the insert and the outbox rows run in one `txManager` transaction, the
  normalisation and the owner and domain checks before it; a failure resets the company id to 0),
  and `QuoteService.Create` / `CreateWithPolicy` (`quote.created`, #518, after the draft history
  row in the existing quote transaction).
  A failure after the record insert rolls back the record and every row. Without the
  options every path is unchanged.
- **Updated events (#408).** The same options, `WithBulkWebhooks(e)` (passed to the one
  `NewBulkStatusService` call in `cmd/main.go`) and `WithAutomationWebhooks(e)` (passed to the one
  `NewAutomationEngine` call there) make these update paths write `*.updated` rows in the
  transaction of the write: `LeadService.Update` (`applyUpdate`, `lead.updated`; without a
  transaction manager the update stays non-transactional and writes none), `LeadService.Assign`
  (`lead.updated` when the owner changed), `LeadService.ConvertToCustomer` (`lead.updated` next to
  `customer.created`, inside each retry attempt, so a replayed conversion commits one of each),
  `CustomerService.Update` (`updateLocked`, `customer.updated`), `TicketService.Update`
  (`updateWithEvents`, `ticket.updated`), `DealService.Update` and `DealService.ChangeStage`
  (`deal.updated`; with `WithDealWebhooks` each first reads the deal row through
  `repository.DealLockingReader`, `FOR UPDATE` on MySQL/MariaDB, inside its transaction, and a
  repository without it refuses the write), `TaskService.Update` / `UpdateWithLabels`
  (`task.updated`, #516; with `WithTaskWebhooks` the `WithTaskRowLock` write runs, its locked row
  is the comparison base, and when `label_ids` was sent the stored label ids are read under lock
  too through `repository.TaskLockingReader.GetLabelIDsForUpdate`: a label set that differs from
  the distinct ids sent counts as a change, order and repeats do not, an empty list against an
  empty set is no change and against a non-empty set is one), `CompanyService.Update`
  (`company.updated`, #517; with `WithCompanyWebhooks` one `txManager` transaction reads the live
  company through `repository.CompanyLockingReader`, `FOR UPDATE` on MySQL/MariaDB, then writes it;
  a company deleted before that read is `apperrors.ErrNotFound` (404) with nothing written, and a
  repository without the locking read refuses the write), the quote writes (#518, with
  `WithQuoteWebhooks`, each in its existing transaction: `QuoteService.Update` compares the quote
  read under lock by `lockDraft` with the state `UpdateDraft` wrote, the totals, status and stamps
  taken from the locked row; `AddLine` and `DeleteLine` always emit, after `recomputeTotals`,
  because the line set changed; `UpdateLine` compares the line's `QuoteLine.WebhookState()` read by
  `GetLine` under the quote lock with the line it writes, so a patch that repeats the stored values
  emits nothing; `ChangeStatus` emits for every committed transition, after the history row, and a
  refused or rolled-back one, such as sent with no lines, writes nothing; `RespondPublic` emits
  after the client's history row, so its plain read of the endpoints follows the customer read),
  the automation `assign_owner` step (`lead.updated` on a
  lead run, `ticket.updated` on a ticket run, `deal.updated` on a deal run, in the step
  transaction; a dry run writes none), and `BulkSetLeadStatus` / `BulkSetTicketStatus` /
  `BulkSetTaskStatus` (#516) (one event,
  with its own event id, per record whose locked status differed from the requested one;
  one `Updated` call with every changed id reads the endpoints once per batch and inserts every row in one statement).
  **The rule:** an event is written iff the committed write changed the record's `WebhookState()`
  (`internal/models`: `Lead`, `Customer`, `Ticket`, `Deal`, `Task`, `Company` and `Quote` each return a struct
  compared with `==`), comparing the state of the row read under lock inside the writing
  transaction with the state written. Each state holds exactly the columns the model's
  repository update writes, minus id, created_at, updated_at and deleted_at (lead: first_name,
  last_name, email, phone, company, position, source, status, classification, external_id, notes,
  owner_id, customer_id, company_id; customer: the fifteen columns `customerRepository.Update`
  writes; ticket: title, description, status, priority, customer_id, assigned_to_id, resolution,
  due_at as an instant; deal: title, stage, amount_cents, currency, probability,
  expected_close_date and closed_at as instants, lost_reason, source, notes, company_id,
  customer_id, lead_id, owner_id; task: title, description, status, priority, due_date as an
  instant, assigned_to_id, lead_id, customer_id, deal_id, with the label set compared apart;
  company: name, domain, website, industry, employee_range, phone, address, city, state, country,
  postal_code, notes, owner_id, without the owner record and the counts; quote: title, currency,
  valid_until as an instant, notes, deal_id, customer_id, owner_id, the five totals, status,
  sent_at and closed_at as instants, the columns of `UpdateDraft`, `UpdateTotals` and
  `ClaimStatus`, without `last_emailed_at`; quote line: product_id, position, description,
  quantity, unit_price_cents, discount_bp, line_total_cents, tax_rate_bp, tax_cents, the columns
  `quoteRepository.UpdateLine` saves minus quote_id),
  nullable ids compared by value. `TestWebhookStateCoversUpdateColumns`
  (`internal/repository`) captures the columns of those updates, of `ConvertToCustomer` and of the
  automation `SetLeadOwner` / `SetTicketAssignee` / `SetDealOwner`, and fails when they and the state drift apart: a
  column added to an update must be added to its state. An update that sends the stored values
  still answers 200 and still records its automation events; it only writes no webhook row, and
  assigning a lead to its current owner writes none either. The handlers do read-modify-write from
  a plain read made before the transaction, so a write that reverts a column another request
  committed in between (a concurrent conversion's customer_id included) counts as a change and
  emits an event: that is the rule, not a defect. A refusal (closed-ticket reopen, bad assignee,
  unknown `company_id`, status `converted`, duplicate email, a customer that is no longer live)
  returns before any outbox write. Deliberately silent (no updated event): delete and erasure (they
  write the deleted events below instead), `CustomerService.Assign`, ticket comments (the
  first-response stamp included), customer external ids, the form repeat-enquiry note append, the
  other automation rule steps, a company delete unlinking `company_id` (`UnlinkLeads`,
  `UnlinkCustomers`, `UnlinkDeals`; it writes `company.deleted` and nothing else), the startup
  lower-casing of stored company domains in `models.MigrateDatabase` (a migration, not a request
  write), the erasure scrub of the text of linked deals
  (`scrubReachedDeals`) and quotes (`scrubReachedQuotes`), the startup `gross_total_cents`
  backfill of stored quotes in `models.MigrateDatabase` (a migration), `QuoteService.Email` (its
  `last_emailed_at` stamp is a delivery record, not a change to the quote's terms or status), the
  quote response-token and PDF render-credential writes, and a label hard delete, whose `labelRepository` purge of `task_labels`
  changes the label set of every task that carried the label.
- **Deleted events (#437, #531).** `TxEmitter.Deleted` (`internal/webhook/emitter.go`)
  de-duplicates the record ids and writes them as `Created` and `Updated` do: one plain read of the active endpoints subscribed to the
  deleted type (`ListActiveEndpointsFor`), then one insert of one `pending` row per (endpoint,
  record), one event id per record, in the shape of the created rows; no subscribed endpoint means
  no insert. It does not read `webhook_deliveries`, so a deleted event goes to every active
  endpoint subscribed to it, whether or not that endpoint received an earlier event for the record
  (an endpoint subscribed to the deleted types only, a record created before the endpoint existed,
  a record whose earlier deliveries were pruned). `TicketService.Delete` with `WithTicketWebhooks` runs
  `ticketRepo.WithTx(tx).Delete` (its own transaction becomes a savepoint) and then the
  `ticket.deleted` emit in one `txManager` transaction; the 404 pre-read stays outside it.
  `DealService.Delete` with `WithDealWebhooks` runs `dealRepo.WithTx(tx).Delete` and then the
  `deal.deleted` emit in one `txManager` transaction (#515). `TaskService.Delete` with
  `WithTaskWebhooks` (#516) keeps its 404 pre-read outside and then, in one `txManager`
  transaction, re-reads the live task through `TaskLockingReader.GetByIDForUpdate`, soft-deletes
  it with `taskRepo.WithTx(tx).Delete` and emits `task.deleted`. `taskRepository.Delete` succeeds
  on a row that is already gone, so the locking read is what makes a delete emit at most once: a
  concurrent delete waits on the lock (MySQL/MariaDB) or the one pooled connection (SQLite), then
  finds no live row and answers 404 with no soft delete and no event. `CompanyService.Delete`
  with `WithCompanyWebhooks` (#517) emits `company.deleted` inside its existing transaction, after
  `LockLive`, the unlinks of leads, customers and deals and the soft delete; the lock order and
  the 404s are unchanged, and the unlinked records get no updated event. `QuoteService.Delete`
  with `WithQuoteWebhooks` (#518) runs in one `txManager` transaction: `GetByIDForUpdate` (a
  missing or deleted quote is `apperrors.ErrQuoteNotFound`, 404), the draft-only check of a sales
  caller on that locked row (`apperrors.ErrQuoteLocked`, 409), the soft delete and `quote.deleted`;
  unwired it keeps the plain read before the delete. Like every deleted event it goes to every
  active endpoint subscribed to it, not only to endpoints that announced the quote.
  Erasure writes `lead.deleted` and `customer.deleted` through a production listener option on
  the three repositories (`repository.WithLeadErasureListener`, `WithCustomerErasureListener`,
  `WithUserErasureListener`, `ErasureListener`/`ErasedRecords` in
  `internal/repository/erasure_listener.go`), which `cmd/main.go` sets to
  `webhook.NewErasureListener(webhookEmitter)` (nil, so no call, for the no-op emitter; lead ids
  first, then customer ids) and every `WithTx` copies (the governed user
  erasure included). `erasureCascade.erase` calls it last, after `scrubBusinessText`, inside the
  unit and with its tx, for the leads and customers whose locked row existed and was live
  (`deleted_at` read by the existing locking reads of `lockGraph`, no extra statement): a legacy
  soft-deleted row is re-scrubbed but announced no more, and a non-admin refusal or a changed
  graph returns before it. A listener error fails the unit; every replayed or restarted attempt
  calls it again inside that attempt, so the commit keeps one row set. The erasure services are
  not wrapped in a transaction (that would turn `eraseGraph` into its one-shot savepoint mode).
  Users and other entities get no deleted event. Tests:
  `internal/service/webhook_deleted_events_test.go` (`TestWebhookDeleted{FanOut,Atomicity}{SQLite,MySQL}`),
  `internal/service/deal_webhook_events_test.go` (`TestDealWebhook{FanOut,Atomicity}{SQLite,MySQL}`,
  every deal write path; the MySQL roots open their schema with `loc=Local`, as production does),
  `internal/service/task_webhook_events_test.go` (`TestTaskWebhook{FanOut,Atomicity}{SQLite,MySQL}`,
  every task write path including the bulk batch, the automation `create_task` step and a delete
  that loses its race; the MySQL roots open their schema with `loc=Local`),
  `internal/service/company_webhook_events_test.go` (`TestCompanyWebhook{FanOut,Atomicity}{SQLite,MySQL}`,
  create, update and delete, the unlinked records' silence included),
  `internal/service/quote_webhook_events_test.go` (`TestQuoteWebhook{FanOut,Atomicity}{SQLite,MySQL}`,
  every quote write path: create, update, the three line writes, each transition, the client's
  accept and decline, delete, and the silence of an email and of a refused write),
  `tests/webhooks_deleted_test.go`, `cmd/webhook_deleted_wiring_test.go`.
- **Runtime.** `webhook.WebhookRuntime` runs one pass at start and every `WebhookPollInterval`
  (5 s): `RecoverExpiredLeases` (a `sending` row past `lease_until` back to `pending`, or `failed`
  with `lease_expired` at the cap), the secret check, a plain `SELECT id, endpoint_id … WHERE
  status = 'pending' AND next_attempt_at <= now ORDER BY next_attempt_at, id` (`ListDueDeliveries`)
  without the held endpoints' rows, then the rows are
  grouped per endpoint and each group is sorted by ascending id (the discovery order is not id
  order: a retried row comes back after newer ones). The groups go to a pool of
  `WebhookWorkers` (4) goroutines, set with `WithWebhookWorkers` in `cmd/main.go` (a runtime built
  without the option has 1, the sequential pass), started and joined inside `RunOnce`. One worker
  takes a whole group and sends it one row at a time, so a slow endpoint holds only its own worker
  and the other endpoints keep moving (#568); it checks the context before every claim and claims
  each row just before its send, never the group up front, so the lease covers only the row being
  sent. A failed or retried row does not hold back the later rows of its endpoint. The pass waits
  for every worker before the prune. Per row: a conditional claim by primary key (`pending → sending`,
  `attempts + 1`, `last_attempt_at`, `lease_until = now + WebhookSendLease` 2 min), one POST, and a
  finish fenced on `status = 'sending' AND attempts = <claimed>`. A missing or inactive endpoint
  fails the row with `endpoint_inactive` and sends nothing. 2xx: `succeeded`, `last_http_status`,
  `next_attempt_at` NULL, `error_code` ''. Otherwise
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
- **Secret check (#559).** After the lease recovery every pass reads the id and `secret_sealed` of
  every active endpoint (`ListActiveEndpointSecrets`, a plain SELECT in id order, no locking read)
  and opens each current secret with the runtime's box through `webhookOpenCurrentSecret`, the
  same helper `webhookSigningSecrets` uses (opens, not empty, a `whsec_` key). The endpoints that
  fail form the held set, kept on the runtime and judged again on every pass; `ListDueDeliveries`
  takes their ids as `skipEndpoints` and adds `AND endpoint_id NOT IN ?` (no predicate for an empty
  list), so a held backlog never fills the batch of 100. Held rows are never claimed or written
  (`status`, `attempts`, `next_attempt_at`, `error_code`, `last_attempt_at` stay) and the circuit
  is not touched; lease recovery and the prune keep running, and inactive endpoints are not
  checked (their rows still fail `endpoint_inactive`). The rows go out on the first pass after a
  restart with the right key or a rotation. `WebhookPassResult.HeldEndpoints` counts the held set;
  `SenderStatus()` (mutex-guarded, read by `GET /health` as `webhooks.state` `paused` with
  `reason` `secret_unavailable`, else `running`; the top-level `status` stays `healthy`) reports
  paused while the last completed check held an endpoint, false before the first. The set and its
  log lines change under one lock: entering logs one Error "Webhook deliveries held: endpoint
  signing secret cannot be opened" (`operation=webhook_secret_check`, `endpoint_id`,
  `error_code=secret_unavailable` only), leaving one Info "Webhook deliveries resumed"
  (`operation`, `endpoint_id`), never repeated while nothing changes. A read error is logged like a
  discovery error (`operation=webhook_secret_check`) and fails the pass with the set unchanged.
  An endpoint whose secret stops opening between the check and its attempt is retried on the
  normal schedule with `secret_unavailable` (nothing sent, circuit untouched); the next check
  holds it. Tests: `internal/service/webhook_secret_hold_test.go`
  (`TestWebhookSecretHold{SQLite,MySQL}`), `cmd/webhook_secret_hold_wiring_test.go`.
- **Endpoint pause (#569).** Each endpoint has a circuit in `webhook_endpoints.consecutive_failures`
  and `paused_until`, written only by the runtime. An attempt counts only when its request went
  through `sendWebhook` (the endpoint was read, active and its secret opened; `attemptOutcome.sent`):
  a 2xx is a success, and `http_status`, `redirect`, `timeout`, `connection_failed` and
  `destination_refused` are failures. `destination_refused` covers both an egress refusal (the
  guard refused the host or the dial) and a request-build error, so an endpoint the policy refuses
  is paused after 3 attempts too: not only real HTTP answers count. A database failure reading the
  endpoint, `endpoint_inactive`, `secret_unavailable` and a runtime without a client factory send
  nothing and leave the circuit alone. After `finish` stored a status (a lost fence records
  nothing), `deliver` calls `RecordEndpointSuccess` (one `UPDATE … SET consecutive_failures = 0,
  paused_until = NULL WHERE id = ? AND (consecutive_failures <> 0 OR paused_until IS NOT NULL)`) or
  `RecordEndpointFailure` (one `UPDATE … SET consecutive_failures = consecutive_failures + 1,
  paused_until = ? WHERE id = ? AND consecutive_failures = ?`, fenced on the count `attempt` read
  with the endpoint; zero rows is a lost race and not an error, and `RecordEndpointFailure` reports
  whether it updated the row, #586). Neither touches `updated_at`, opens
  a transaction or takes a locking read; a write error is one Warn
  (`operation=webhook_endpoint_health`) that never fails the pass. From the
  `WebhookEndpointPauseAfter` (3rd) consecutive failure `paused_until` = the finish time +
  `WebhookEndpointPause(f)` = `WebhookBackoff(f − 2, WebhookEndpointPauseBase 1 min,
  WebhookEndpointPauseMax 1 h)`: 1, 2, 4, 8, 16, 32 minutes, then 1 hour; below 3 it stays NULL.
  For one row alone the pause is always shorter than the row's own retry delay (2^(n−3) against
  2^(n−1) minutes), so a single delivery keeps its schedule. Discovery adds `AND endpoint_id NOT IN
  (SELECT id FROM webhook_endpoints WHERE paused_until IS NOT NULL AND paused_until > ?)` bound to
  the same `now`, in SQL so a paused endpoint's rows never fill the batch of 100 (at `paused_until
  == now` the endpoint is due again), and a worker stops its group as soon as an attempt pauses the
  endpoint past `r.now()`: the rest of the group is neither claimed nor touched and keeps its
  `attempts` and `next_attempt_at`. A pause logs one Info "Webhook endpoint paused" with
  `operation=webhook_endpoint_pause`, `endpoint_id`, `consecutive_failures` and `paused_until`
  only. The pause log line and the group stop happen only when the failure write updated the row:
  a lost fence (another replica's reset or failure won it) logs nothing and does not stop the
  group (#586). The test ping neither reads nor writes the circuit, a redelivered row of a paused endpoint
  (`RequeueDelivery`) waits for the pause, and a `PUT` on the endpoint (a URL change or a
  reactivation) does not clear it, deliberately; `webhookEndpointColumns` stays `url`,
  `event_types`, `is_active`.
- **Signing.** `webhook.SignWebhook(secret, eventID, unixSeconds, body)` returns `v1,` +
  base64(HMAC-SHA256(base64decode(secret minus whsec_), id + "." + timestamp + "." + body)). Each
  attempt is re-signed with its own `webhook-timestamp`. `NewWebhookRequest` takes the secrets as
  a list and joins one signature per secret with a space; `webhookSigningSecrets` picks them (#503):
  the opened current secret first, then the previous secret while `now` is strictly before
  `previous_secret_expires_at` (at the expiry instant the window is over). An unopenable current
  secret sends nothing: the per-pass secret check holds the endpoint's rows before they are
  claimed, and an attempt that still meets one is retried with `secret_unavailable` (#559); an
  unopenable previous secret is dropped
  with one Warn (`operation`, `endpoint_id`, `error_code=previous_secret_unavailable`) and the
  request carries the current signature. `WebhookRuntime.attempt` takes ONE `r.now()` reading for
  the window decision and the request timestamp, and when the previous secret has expired calls
  `ClearExpiredPreviousSecret` with that reading before sending: one conditional `UPDATE … SET
  previous_secret_sealed = NULL, previous_secret_expires_at = NULL WHERE id = ? AND
  previous_secret_expires_at IS NOT NULL AND previous_secret_expires_at <= ?`, fenced in the
  statement so a rotation committed after the read survives; it does not touch `updated_at`, and
  a failure is a Warn that never blocks the send. Retries and redeliveries go through `attempt`,
  so they carry both signatures inside the window too. The test ping uses the same selection with
  one `time.Now()` reading, and never clears. Headers: `webhook-id`,
  `webhook-timestamp`, `webhook-signature`, `Content-Type: application/json`, `User-Agent:
  GopherCRM-Webhooks/1`. Body: exactly `{"id","type","occurred_at","record_id"}`.
- **Egress.** Requests go through `(*egress.Guard).PublicHTTPClient(url)` (see
  [AEO_SECURITY.md](AEO_SECURITY.md#public-only-egress-for-webhooks)), 10 s per attempt
  (`WebhookRequestTimeout`), response bodies read up to 4 KiB and discarded. `cmd/webhook_runtime.go`
  builds it from `egress.New()`, the real resolver and dialer.
- **Drain.** `Stop` cancels the loop: no worker starts a further claim, every attempt in flight
  (one per busy worker) runs to its end (the endpoint read and the expired-secret clear each
  bounded by `webhookFinishTimeout`, 5 s, the request by its timeout, the finish write by the same
  5 s bound, #561), `RunOnce` joins its workers, then the loop goroutine joins. After `Stop`
  returns nil nothing is written. `combineStops` stops the webhook sender together with the other
  runtimes under the one shared deadline.
- **Replicas.** Safe on every replica: claims are conditional, and a crashed sender's lease
  expires and is recovered. Delivery is at least once (a crash between the POST and the finish
  resends after the lease). The per-endpoint id order holds within one runtime's pass only: a
  retry goes back in the queue behind newer rows, and two replicas can each send rows of the same
  endpoint, so consumers deduplicate on `webhook-id` and do not rely on order.
- **Privacy.** Rows hold ids, the event type, a status and timestamps; erasure and ticket delete
  leave them (a non-goal). Log lines of the registry, the outbox, the runtime and the handler carry
  `operation`, `endpoint_id`, `delivery_id`, `event_type`, `record_id`, `attempts`, `status`,
  `http_status`, `error_code`, `consecutive_failures` and `paused_until` only (plus a sanitised
  database error); never the URL, host,
  secret, sealed value, signature or a body. Transport errors, which quote the URL, are classified
  and never logged.
- **Schema.** `webhook_endpoints` and `webhook_deliveries` (see
  [datamodel.md](datamodel.md#outbound-webhooks)) from `MigrateDatabase` on SQLite and
  `migrations/20261120120000_add_outbound_webhooks.up.sql` (created only when missing) on MySQL/MariaDB.
  The two rotation columns (#503) come from AutoMigrate and
  `migrations/20261127120000_webhook_secret_rotation.up.sql` (each added only when missing), the two
  pause columns (#569, `consecutive_failures int NOT NULL DEFAULT 0`, `paused_until datetime(3)
  NULL`) from AutoMigrate and `migrations/20261129120005_webhook_endpoint_pause.up.sql` (each added
  only when missing, no backfill, no index).

Regression roots: `TestWebhookSchemaSQLite`, `TestWebhookJSON` (`internal/models`);
`TestEgressPublicClient` (`internal/egress`); `TestWebhookRepository{SQLite,MySQL}`
(`internal/repository`); `TestWebhookSignature`, `TestWebhookOutbox{Atomicity,FanOut}{SQLite,MySQL}`,
`TestWebhookDeliveryAfterCommit`, `TestWebhookRetryBackoffAndCap`, `TestWebhookUndeliverableEndpoint`,
`TestWebhookRuntimeLifecycle`, `TestWebhookEgressRefusal`, `TestWebhookRequestTimeout`,
`TestWebhookEndpointService`, `TestWebhookDeliveryLogAndPrune`, `TestWebhookLogsOmitSecrets`,
`TestWebhookUpdated{FanOut,Atomicity}{SQLite,MySQL}` (#408),
`TestWebhookWorkerPool{SQLite,MySQL}` (#568, registered in the transaction profile of
`scripts/ci/check-go.py`) (`internal/service`);
`TestWebhookRoutesAdminOnly`, `TestWebhookHandlerValidation` (`internal/handler`);
`TestWebhookAPISQLite`, `TestWebhookUpdatedAPISQLite` (`tests`); `TestWebhookProductionWiring`,
`TestWebhookUpdatedProductionWiring` (`cmd`);
`TestWebhookMigrationFiles`, `TestWebhookMigration`
(`cmd/migrate`); `TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates`
(`test/integration`). The MySQL roots use a disposable `TEST_MYSQL_DSN` schema, skip without it
and fail when it is unreachable. The four `TestWebhookUpdated…` service roots are registered in
the transaction profile (`TRANSACTION_SOURCES` in `scripts/ci/check-go.py`, asserted by
`scripts/ci/tests/test_go_checks.py`). Operator follow-ups: register the #345 roots in
`scripts/ci/check-go.py` (they are still unregistered), the new enums (`event_type`, delivery `status`, `error_code`) in
`scripts/ci/check-api-docs.py`, and the migration roots in `.github/workflows/migrations.yml`.
Signing-secret rotation (#503): `TestWebhookSecretRotation{SQLite,MySQL}`
(`internal/service/webhook_secret_rotation_test.go`, not in the transaction profile: there is no
transaction; the backend job runs it with `TEST_MYSQL_DSN`), `TestWebhookSignature` (the
two-secret subtest), `TestWebhookRotateSecretHandler` (`internal/handler`),
`TestWebhookSecretRotationSQLiteUpgrade` (`internal/models`), and
`TestWebhookSecretRotationMigrationFiles` (it held the newest-migration assertion from #503 until
#505 moved it to `TestUserIdentitySourceMigrationFiles`; #506 moved it on to
`TestSCIMGroupMembersMigrationFiles`) and
`TestWebhookSecretRotationMigration` (`cmd/migrate`); the migration and SQLite-upgrade roots are
required by the migrations workflow, `scripts/ci/check-go.py migration` and
`scripts/release/check-sql-tests.py`.
Endpoint pause (#569): `TestWebhookEndpointPause{SQLite,MySQL}`
(`internal/service/webhook_endpoint_pause_test.go`, registered in the transaction profile through
`TRANSACTION_SOURCES` and asserted by `scripts/ci/tests/test_go_checks.py`), the three circuit
subtests of `TestWebhookRepository{SQLite,MySQL}`, `TestWebhookSchemaSQLite` and `TestWebhookJSON`
(the new columns and keys), `TestWebhookEndpointPauseSQLiteUpgrade` (`internal/models`), and
`TestWebhookEndpointPauseMigrationFiles` (it held the newest-migration assertion from #569 until
#551) and `TestWebhookEndpointPauseMigration` (`cmd/migrate`); the migration and SQLite-upgrade
roots are required by the migrations workflow, `scripts/ci/check-go.py migration` and
`scripts/release/check-sql-tests.py`.
API key rows of the sign-in audit log (#551): migration `20261129120006_auth_audit_log_api_key_id`
adds `auth_audit_log.api_key_id`. `TestAuthAuditLogAPIKeyIDMigrationFiles` (it holds the
newest-migration assertion since #551, moved from `TestWebhookEndpointPauseMigrationFiles`) and
`TestAuthAuditLogAPIKeyIDMigration` (`cmd/migrate`) are required by the migrations workflow,
`scripts/ci/check-go.py migration` and `scripts/release/check-sql-tests.py`;
`TestAuthAuditLogMigration` and `TestAuthAuditLogAttemptsMigration` leave the new column out of
their comparisons.

## Form retention purge

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
- **The drain.** The runtime runs on a `lifecycle.Loop` (`internal/lifecycle`).
  `FormRetentionRuntime.Stop` cancels the sweep's context, so a chunk in flight rolls
  back and no further chunk starts, then joins the goroutine under `main`'s shared deadline. After a
  `Stop` that returned nil no row changes. A deadline that passes first returns an error, with no such
  guarantee, and `main` then skips closing the database as it does for the other runtimes. The purge
  drains together with the automation worker, the stale bulk operation sweep, the password reset
  queue, the webhook sender and AEO:
  `combineStops(automation.Stop, formRetention.Stop, bulkRecovery.Stop, passwordReset.Stop, webhooks.Stop, aeoRuntime.Stop)`.
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

## Form mail delivery (#216)

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

## Form attribution and the form-submitted event

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

## Form ticket intake (#213)

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
- **Ticket.** `open`, `medium`, unassigned, no `due_at`, `first_responded_at` not written;
  description exactly `Submitted through form <form id>, submission <submission id>.`, which holds
  no submitted value. The title is exactly `Form: <form name>` cut to 255 runes, unless the form
  sets `ticket_title_field` (#804, below). A
  `ticket_created` event (origin `form`, `actor_id` NULL) is recorded in the same transaction.
- **Ticket title field (#804).** `forms.ticket_title_field` (`varchar(50) NOT NULL DEFAULT ''`)
  names one text or select field of a `create_ticket` form. `Form.ValidateDefinition` trims it and,
  when it is non-empty, refuses it (400 `VALIDATION_ERROR`, nothing written) without
  `create_ticket`, when no field has exactly that name, or when that field is not text or select.
  At confirmation `formTicketTitle` (`internal/service/form_ticket_intake.go`) reads the stored
  form: if it still has a text or select field of that name, the submitted value has every control
  rune replaced by a space and is trimmed, and a non-empty result gives `<form name>: <value>` cut
  to 255 runes. No setting, a blank value, or a field removed or retyped since the submission gives
  `Form: <form name>`. `PUT /forms/{id}` replaces wholesale, so an absent value clears it; the
  builder always sends it (`''` unless the ticket action is chosen). The value is ticket text: it
  reaches the `ticket.created` webhook delivery and the `ticket_created` event like any title and
  is kept by erasure (see [DEV_ERASURE.md](DEV_ERASURE.md)). Log lines never carry the title.
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
  `forms.ticket_title_field` comes from `MigrateDatabase` on SQLite and from
  `20261129120009_add_form_ticket_title_field` (only when missing) on MySQL/MariaDB; existing forms
  read `''`.

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

## AEO runtime validation and candidate inspection

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
