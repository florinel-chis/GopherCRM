# AEO runtime

> **For:** Developers changing the AEO background work, its shutdown and its validation. Part of the [documentation index](../README.md).

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
MySQL/MariaDB; see [../reference/data-model.md](../reference/data-model.md#aeo-run-ownership). Regression roots:
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

## AEO runtime validation and candidate inspection

Bounded AEO backend/runtime work uses `python3 scripts/ci/check-go.py aeo` with
build/vet, exact uncached race roots and mandatory SQL availability, plus health/provider
browsers and migration gates. Startup and shared-interface exceptions require committed
scope proofs and six lifecycle roots; other application changes keep their fallback.
See [validation policy](../maintainers/validation-and-releases.md) for scope and CI engines. A fresh runner
policy may enable `validation_review_required` to hold after commit and before checks.
Inspect the actual diff and complete command plan, then use `confirm-validation`; a
comment-only `.env.example` edit needs the shared committed proof to retain a bounded
profile; changing active settings or unsupported syntax retains full fallback. A
changed candidate needs another inspection. See [operations](../../automation/langflow/OPERATIONS.md).

For changes confined to `internal/mailer/smtp.go` and mailer package tests, `python3 scripts/ci/check-go.py mailer`
requires the SMTP deadline/header/TLS/privacy roots and selected password-reset/form/quote/automation delivery
callers, then runs API build, vet and uncached race checks. Local browser coverage is SQLite
health smoke; CI retains all engine health and migration gates. No SMTP account or external
relay is needed by these tests. See [validation policy](../maintainers/validation-and-releases.md) for the exact
scope; service, schema and runtime-setting changes are outside this bounded profile.
