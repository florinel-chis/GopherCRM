# Automation runtime

> **For:** Developers changing the automation runtime. Part of the [documentation index](../README.md).

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
    index, no backfill) on MySQL/MariaDB; see [../reference/data-model.md](../reference/data-model.md#automation-run-leases).
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
- **Erasure hooks.** See *Deleting personal data* in [erasure.md](erasure.md#deleting-personal-data): lead erasure cancels the lead's runs and
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
