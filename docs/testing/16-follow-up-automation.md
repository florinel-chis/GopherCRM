# Follow-up Automation — Test Cases

Cases for the follow-up automation and notifications area added 2026-10-28 (issue #164): admin
rules with ordered steps, the outbox events every lead and deal write records, the dispatcher,
the step executor, waits and schedules that survive a restart, the dry run, the run history, the
in-app notifications, the boot reconciliation, the erasure hooks and the migration, and the
screens added 2026-10-29: rule list and form, dry-run dialog, run list and detail, the lead and
deal "Automation" sections, the notification bell and page.

Every **Expected** states what the build does **today**, traced to the handlers, services and
repositories; every automated case names the exact test that pins it. Cases 001 to 027 are automated at the Go
level; the frontend cases 028 to 033 by Vitest and by the Playwright spec
`gocrm-ui/e2e/tests/automation-rules.spec.ts`, which covers the admin flow only (as in
[15-products-quotes.md](15-products-quotes.md), the sales and support behaviour is pinned in
Vitest because no role-login e2e helper exists).

33 cases: 33 automated (27 Go, 6 Vitest/Playwright), 0 planned, 0 blocked.
Priorities: 18 P0, 14 P1, 1 P2.

**Sources**

- `internal/models/automation.go`, `internal/models/notification.go`, `internal/models/database.go`
- `internal/handler/automation_handler.go`, `internal/handler/notification_handler.go`,
  `internal/handler/routes.go` (`SetupAutomationRoutes`, `SetupNotificationRoutes`)
- `internal/service/automation_service.go`, `automation_engine.go`, `automation_condition.go`,
  `automation_dry_run.go`, `automation_runtime.go`, `automation_events.go`,
  `notification_service.go`; the event hooks in `lead_service.go`, `deal_service.go`,
  `form_service.go`, `bulk_operation_service.go` and `bulk_status_service.go`
- `internal/repository/automation_repository.go`, `notification_repository.go`, `erasure.go`,
  `erasure_cascade.go`, `internal/utils/sort.go`
- `cmd/main.go`, `cmd/automation_runtime.go`, `internal/config/config.go`
- `migrations/20261028120000_add_follow_up_automation.{up,down}.sql`
- `docs/FEATURES.md` section 4e, `docs/datamodel.md` (Follow-up Automation),
  `docs/DEVELOPMENT.md` (Automation runtime)

**Constraints**

- **Rules are admin-only.** Sales, support and customer get **403** on every `/automation/rules`
  route; the run history is admin and sales, with sales narrowed inside the query to the leads and
  deals it currently owns; support and customer get 403 there too. Notifications are the
  recipient's own; customers are never addressed and get 403.
- **Events are recorded in the write transaction.** A write that rolls back records nothing; a
  bulk item that fails records nothing for that item. No event or run-log column holds personal
  data.
- **Every claim is a conditional update.** The event claim (`processed_at IS NULL`), the run claim
  (`status = pending|waiting`), the per-step advance (`status = 'running' AND next_position = ?`)
  and the schedule cursor (`(schedule_pass_on, schedule_cursor)`) each match exactly one row or
  roll back. Each step first re-reads its record with a locking read (`FOR UPDATE` on MySQL).
- **Email is at most once.** The `send_email` step commits a `sending` row before the one
  `Mailer.Send`; a row left in `sending` is failed with `email_unconfirmed` and never resent.
  Without `SMTP_HOST` the development `LogMailer` logs the recipient and subject and the step is
  `succeeded` with nothing delivered.
- **Single replica, no retention.** The worker (`AUTOMATION_ENABLED`, default true) assumes one
  API replica: runs left `running` are recovered at the boot of the worker process, not by a
  lease; a process started with the worker off neither executes nor reconciles runs. Processed
  events and finished runs are kept.
- **A task never carries the person.** A `create_task` title or description accepts only
  `lead.id`, `lead.status`, `lead.source` and the `deal.*` placeholders, because the task is kept
  when its lead is erased; the name, email, phone, company and owner placeholders are refused in
  it (they stay available to the email and notification text, which does not outlive the person).
- **Run the MySQL roots with `TEST_MYSQL_DSN`.** Every `*MySQL` root and
  `TestMigrationAutomationSchema` create disposable schemas; they skip without a server and fail
  when it is unreachable. The paused-step races (a cancel, a rule change or an erasure committed
  from a second connection after a step's first read) exist only on MySQL.

---

## 16.1 Rules

### TC-AUT-001 — Rule routes enforce the roles over HTTP
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Users of every role.
- **Steps:**
  1. As sales, support and customer, call every `/automation/rules` route (`POST`, `GET`, `GET
     /:id`, `PUT /:id`, `DELETE /:id`, `POST /:id/enable`, `/disable`, `/dry-run`).
  2. Repeat unauthenticated.
- **Expected:** **403** for the three roles on every route; **401** unauthenticated.
- **Automation:** automated — `tests/automation_api_test.go`: `TestAutomationRuleAPISQLite`
  ("sales, support and customer get 403 on every rule route").

### TC-AUT-002 — Create returns 201 with the steps numbered 1..n
- **Priority:** P0
- **Type:** functional
- **Preconditions:** An admin; an active sales user to name as a recipient.
- **Steps:**
  1. `POST /automation/rules` with a `lead_status_changed` rule (`trigger.to = qualified`) and
     four steps: `wait`, `condition`, `create_task` (recipient owner), `notify` (recipient user).
  2. `GET /automation/rules/{id}`.
- **Expected:** **201** with `version 1`, `enabled true`, the trigger echoed and the four steps in
  order with positions 1..4 and their decoded `config`; the GET returns the same rule.
- **Automation:** automated — `TestAutomationRuleAPISQLite` ("create returns 201 with the steps
  numbered 1..n").

### TC-AUT-003 — Invalid rules are 400 INVALID_INPUT and write nothing
- **Priority:** P0
- **Type:** validation
- **Preconditions:** An admin.
- **Steps:** `POST /automation/rules` with, one at a time: an invalid trigger type for the entity,
  an invalid trigger value, a wait of 0 minutes, an unknown step type, 21 steps, 91 days of waits,
  a backward `else_position`, an out-of-range `else_position`, an unknown `{{lead.password}}`
  placeholder, a `create_task` whose title carries `{{lead.first_name}}` or whose description
  carries `{{lead.phone}}` (a task survives the erasure of its lead, so only `lead.id`,
  `lead.status`, `lead.source` and the `deal.*` placeholders are allowed in it), no steps.
- **Expected:** **400** `INVALID_INPUT` each time; the rule and step row counts are unchanged.
- **Automation:** automated — `TestAutomationRuleAPISQLite` ("invalid rules are 400 INVALID_INPUT
  and write nothing"); the condition refusals in `internal/service/automation_condition_test.go`:
  `TestAutomationConditionBranching` ("validation refuses unknown fields, operators and value
  types"); the task placeholder refusals and the task renderer that blanks a personal placeholder
  in `TestAutomationTaskPlaceholdersExcludePersonalData` (same file).

### TC-AUT-004 — An unknown, inactive or non-staff user is 400 INVALID_REFERENCE
- **Priority:** P0
- **Type:** validation
- **Preconditions:** A deactivated sales user, a customer-role user.
- **Steps:** `POST /automation/rules` with an `assign_owner` step naming an unknown id, the
  deactivated user, the customer.
- **Expected:** **400** `INVALID_REFERENCE` each time.
- **Automation:** automated — `TestAutomationRuleAPISQLite` ("an unknown or inactive user is 400
  INVALID_REFERENCE").

### TC-AUT-005 — Update, enable, disable and delete
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A rule created by the admin.
- **Steps:**
  1. `PUT /automation/rules/{id}` with a new name and one `notify` step, `enabled` absent.
  2. `POST .../disable`, then `POST .../enable`.
  3. `PUT` with no steps.
  4. `DELETE /automation/rules/{id}`, then `GET`, `POST .../enable` and `PUT /automation/rules/999999`.
- **Expected:** The PUT answers **200** with `version 2`, the new name, the one step and the
  switch kept; disable and enable flip `enabled`; the empty PUT is **400** `INVALID_INPUT`; the
  delete is **204** and the rule is **404** afterwards, as is the unknown id.
- **Automation:** automated — `TestAutomationRuleAPISQLite` ("update, enable, disable and delete").

### TC-AUT-006 — The list pages through the shared parser and refuses unknown sort columns
- **Priority:** P1
- **Type:** functional
- **Preconditions:** At least two live rules (the automated case creates its second one itself,
  because the delete of TC-AUT-005 leaves only one).
- **Steps:** `GET /automation/rules?limit=0`; `?limit=1&offset=1&sort_by=name&sort_order=asc`;
  `?sort_by=trigger_config`; `?enabled=maybe`; `?enabled=true&entity_type=lead`.
- **Expected:** `limit=0` gives `per_page 20`; the page of one holds one rule; the unknown sort
  column and the bad boolean are **400**; the filtered list is **200**.
- **Automation:** automated — `TestAutomationRuleAPISQLite` ("the list pages through the shared
  parser and refuses unknown sort columns"); `internal/utils/sort_test.go`:
  `TestValidateSortAutomationAllowlists`.

### TC-AUT-007 — A rule change cancels its active runs and a cancelled running run does nothing more
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A rule with pending, waiting, running and completed runs.
- **Steps:**
  1. Update the rule; disable another rule with a pending run; delete a third.
  2. Let the executor that still holds the running run try its next step.
  3. Update a rule through a repository whose step replacement fails.
- **Expected:** The update bumps the version, replaces the steps and cancels the pending, waiting
  and running runs with `rule_changed` (the completed one is untouched); disable cancels with
  `rule_disabled`, delete soft-deletes and cancels with `rule_deleted`; the held running run's
  conditional advance affects zero rows, so no task, notification or run-step row appears; the
  faulty replacement leaves the version, the steps and the runs exactly as they were.
- **Automation:** automated — `internal/service/automation_rule_lifecycle_test.go`:
  `TestAutomationRuleLifecycleSQLite`, `TestAutomationRuleLifecycleMySQL`.

## 16.2 Events and dispatch

### TC-AUT-008 — Every lead write path records its events in its own transaction
- **Priority:** P0
- **Type:** functional
- **Preconditions:** The lead, form and bulk services wired with the automation repository.
- **Steps:**
  1. Create and update a lead through `LeadService`; update it again without a status change; make
     an update that rolls back.
  2. Submit a public form twice with the same address.
  3. Bulk status with a missing id, then without; bulk assign with a missing id; bulk convert.
  4. Convert a lead to a customer.
- **Expected:** `lead_created` (origin `user`); `lead_updated` plus `lead_status_changed` with
  `from_value`/`to_value` when the status differs, `lead_updated` alone otherwise; nothing for the
  rolled-back update; `lead_created` then `lead_updated` with origin `form`; the refused batch
  records nothing, the accepted one `lead_updated` per lead plus `lead_status_changed` where the
  status changed (origin `bulk`, `actor_id` the admin); the failed bulk item records nothing; the
  conversion records `lead_updated` and `lead_status_changed … converted` with origin
  `conversion`; no event column holds a name, address, phone or note.
- **Automation:** automated — `internal/service/automation_lead_events_test.go`:
  `TestAutomationLeadEventsSQLite`, `TestAutomationLeadEventsMySQL`.

### TC-AUT-009 — Deal writes record events with the deal row and its history row
- **Priority:** P0
- **Type:** functional
- **Preconditions:** The deal service wired with the automation repository.
- **Steps:** Create a deal; PUT it with the same stage; PUT it into `proposal`; `POST /stage` into
  `won`, then `lost`, then `lost` again; make a stage change whose history insert fails.
- **Expected:** `deal_created` with `to_value qualification`; `deal_updated` alone; `deal_updated`
  plus `deal_stage_changed qualification → proposal`; `deal_stage_changed` into won and into
  lost; nothing for the same stage; nothing — and no deal or history row — for the rolled-back
  write.
- **Automation:** automated — `internal/service/automation_deal_events_test.go`:
  `TestAutomationDealEventsSQLite`, `TestAutomationDealEventsMySQL`.

### TC-AUT-010 — The dispatcher claims an event once and starts one run per matching rule
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A matching rule, a rule for any status change, a `lead_created` rule, a rule
  with a non-matching `from`, a disabled rule and a deleted rule.
- **Steps:**
  1. Record `lead_status_changed new → qualified`; dispatch twice.
  2. Insert a run with an existing dedupe key.
  3. Record an event with origin `automation`; dispatch.
  4. Record an event and let two dispatchers race for it, four rounds.
- **Expected:** Two runs, one per enabled matching rule, each with the rule version, `pending`,
  `next_position 1`, the event id and `evt:<event_id>:rule:<rule_id>`; `processed_at` is set and
  the second dispatch starts nothing; the duplicate insert is no error and no row (`ON CONFLICT DO
  NOTHING` on SQLite, `ON DUPLICATE KEY UPDATE id=id` on MySQL, asserted on the rendered SQL); the
  automation-originated event is claimed and starts nothing; every race starts exactly two runs.
- **Automation:** automated — `internal/service/automation_dispatch_test.go`:
  `TestAutomationDispatchSQLite`, `TestAutomationDispatchMySQL`.

## 16.3 Execution

### TC-AUT-011 — A run performs every effect once, in order
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A `lead_created` rule with `create_task` (owner, due in 2 days),
  `assign_owner` (another sales user), `notify` (that user) and `send_email` (owner).
- **Steps:** Start a run and execute it.
- **Expected:** `completed` with four `succeeded` rows; one task assigned to the lead's owner at
  execution time, due 48 h later (UTC), linked to the lead and not to a customer, titled with the
  lead's id and none of the lead's name, email or phone; the owner
  changed, with one `origin=automation` event that starts no run; one notification for the new
  owner; `Mailer.Send` called once, to the new owner's address; the step details carry the task,
  owner and recipient ids.
- **Automation:** automated — `internal/service/automation_step_execution_test.go`:
  `TestAutomationStepExecutionSQLite`, `TestAutomationStepExecutionMySQL` ("a lead run performs
  every effect once in order").

### TC-AUT-012 — A deal run links the task to the customer, else the lead, never both
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A `deal_created` rule with a `create_task` step; three deals: with a
  customer and a lead, with a lead only, with neither.
- **Steps:** Dispatch and execute.
- **Expected:** The first task carries `customer_id` only, the second `lead_id` only, the third
  neither; every run completes.
- **Automation:** automated — `TestAutomationStepExecution{SQLite,MySQL}` ("a deal run links the
  task to the customer, else the lead, never both").

### TC-AUT-013 — A fault after the effect rolls the step back and the retry performs it once
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A rule with `create_task` then `notify`; the engine's `afterEffect` seam
  failing once.
- **Steps:** Execute; clear the seam; execute again.
- **Expected:** After the fault: no task, no run-step row, the run back in `pending` at position 1
  with `attempts 1`. After the retry: exactly one task, one notification, `completed`, two rows.
- **Automation:** automated — `TestAutomationStepExecution{SQLite,MySQL}` ("a fault after the
  effect rolls the step back and the retry performs it once").

### TC-AUT-014 — An unavailable recipient is skipped with recipient_unavailable
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A rule naming a user who is erased after the rule is saved; a second rule
  whose assignee is deactivated after it is saved.
- **Steps:** Execute both.
- **Expected:** The notify, task and email steps addressed to the erased user are `skipped` with
  `recipient_unavailable`, the owner notification still goes out and the run completes; the assign
  to the inactive user is skipped and the owner is unchanged.
- **Automation:** automated — `TestAutomationStepExecution{SQLite,MySQL}` ("an inactive or erased
  recipient is skipped with recipient_unavailable").

### TC-AUT-015 — send_email is two transactions around one send
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A rule with `send_email` then `notify`; the recording mailer.
- **Steps:**
  1. Execute with the mailer failing.
  2. Execute with a seam reading the step row before the send; then try to insert a second
     `sending` row at the position.
  3. Execute with a fault injected after the send; execute again.
  4. Execute with a cancel committed between the two halves.
- **Expected:** (1) the row is `failed`/`mail_failed` and the run goes on to the notify; (2) the
  `sending` row is committed before `Mailer.Send`, the send goes to the owner, the row ends
  `succeeded`, and the second insert is refused by the unique key; (3) one send, the row left in
  `sending`, the run handed back, and the next execution marks the row and the run
  `failed`/`email_unconfirmed` without a second send; (4) the send in flight goes out, the row is
  finalized, the run stays `cancelled`/`manual` at position 1.
- **Automation:** automated — `TestAutomationStepExecution{SQLite,MySQL}` ("send_email is two
  transactions around one send").

### TC-AUT-016 — A cancel, a rule change or an erasure committed after the first read leaves no effect (MySQL)
- **Priority:** P0
- **Type:** regression
- **Preconditions:** `TEST_MYSQL_DSN`; a rule whose first step is `create_task`, `notify` or
  `assign_owner`; the engine's `afterRecordRead` seam.
- **Steps:** Pause the step after its locking read of the lead and, from a second connection,
  commit a manual cancel, a rule update, or a lead erasure (which blocks on the lead row until the
  step's advance deadlocks against the cancelled run; the step is the victim and retries).
- **Expected:** The run is `cancelled` with `manual`, `rule_changed` or `record_deleted`; no
  run-step row, task, notification or owner change exists; the erased lead keeps its owner id.
- **Automation:** automated — `TestAutomationStepExecutionMySQL` ("a cancel, a rule change or an
  erasure committed after the first read leaves no effect"); the repository-level twin in
  `internal/repository/automation_erasure_test.go`: `TestAutomationErasureMySQL`.

### TC-AUT-017 — Conditions evaluate a fresh read and branch
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Rules with a condition followed by effects; a deal rule on `amount_cents`.
- **Steps:**
  1. Run the matrix of operators on a lead and a deal in memory.
  2. Execute a true condition, a false one with `else=stop`, and a false one with `else=jump` after
     changing the lead between dispatch and execution.
  3. Execute an EUR `amount_cents` condition against a USD and an EUR deal.
- **Expected:** The matrix answers as specified (`contains` case-insensitive, `is_empty` on a blank
  company, `amount_cents` false in another currency); true continues and notifies; stop completes
  at the condition with no notification; jump lands on position 3, skipping the task; the USD deal
  evaluates false and the EUR one true.
- **Automation:** automated — `internal/service/automation_condition_test.go`:
  `TestAutomationConditionBranching`.

### TC-AUT-018 — Waits persist, survive a restart and are cancelled when the record or rule goes
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A rule with `wait 60`, a condition and a notify; an injected clock.
- **Steps:**
  1. Execute; tick twice at 59 minutes; change the lead; advance to 61 minutes; rebuild the engine
     and service; execute.
  2. Soft-delete a waiting run's lead, advance, execute. Erase another's lead. Update a third's rule.
  3. (MySQL) Commit a cancel from a second connection after the resuming step's first read.
- **Expected:** `waiting` with `resume_at = start + 60 min` (UTC) and `next_position 2`, one
  `waiting` row, nothing before `resume_at`; the fresh engine resumes it, the condition sees the
  change and the run completes with its notification; the deleted record cancels with
  `record_deleted` at the resume, the erased one inside the erasure, the changed rule with
  `rule_changed` — none runs a further step; the MySQL cancel leaves no effect or row.
- **Automation:** automated — `internal/service/automation_wait_resume_test.go`:
  `TestAutomationWaitResumeSQLite`, `TestAutomationWaitResumeMySQL`.

### TC-AUT-019 — A schedule rule makes one pass per UTC day in batches of 500
- **Priority:** P0
- **Type:** functional
- **Preconditions:** 1,201 matching leads, a non-matching one and an erased one; a schedule rule
  at `hour_utc 9` filtered on `status new`; an injected clock.
- **Steps:** Tick at 08:30, then at 09:05, 09:35 and 10:05; tick again; insert a duplicate dedupe
  key; jump to 15:00 two days later; race two workers on the same batch; run a `stale_days 3` rule.
- **Expected:** Nothing before the hour; 500, 1,000 and 1,201 runs after the three ticks, in id
  order, each `sch:<rule_id>:<day>:<entity_id>`, the cursor and `schedule_pass_on` advancing and
  `schedule_completed_on` set by the short batch; every matching lead exactly once, the
  non-matching and erased ones never; the repeat tick and the duplicate insert add nothing; the day
  the process was down is not backfilled and the late tick starts today's pass; the race commits
  exactly one batch of 500 with no record twice; `stale_days` visits only the record not updated
  for three days.
- **Automation:** automated — `internal/service/automation_schedule_test.go`:
  `TestAutomationScheduleTriggerSQLite`, `TestAutomationScheduleTriggerMySQL`;
  `internal/repository/automation_repository_test.go`: `TestAutomationRepositoryConditionalUpdates`.

### TC-AUT-020 — The dry run reports every step and changes nothing
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A rule with `wait 120`, `assign_owner`, a condition on `owner_id`,
  `create_task`, `send_email` and `notify`; a deal rule with a jumping condition; existing rows in
  every table.
- **Steps:** Snapshot every table; dry-run the lead rule and the deal rule; dry-run with the wrong
  entity type, a missing record and a missing rule; snapshot again. Over HTTP, `POST
  /automation/rules/{id}/dry-run` as admin, with the wrong entity type, a missing record, a missing
  rule, and as sales.
- **Expected:** One report entry per step with the outcome, the simulated time (the wait advances
  it by 120 min without pausing), the owner from/to, the condition fed by the simulated assign,
  the task assignee, due date and lead link, the recipient ids, and the jump's branch; the
  snapshots are identical, `Mailer.Send` was never called and no run, notification, task or event
  exists; the refusals are `ErrValidation`, `ErrNotFound` and `ErrAutomationRuleNotFound`, over
  HTTP **400** `INVALID_INPUT`, **404**, **404** and **403**.
- **Automation:** automated — `internal/service/automation_dry_run_test.go`:
  `TestAutomationDryRunNoSideEffectsSQLite`, `TestAutomationDryRunNoSideEffectsMySQL`;
  `tests/automation_api_test.go`: `TestAutomationRunAPISQLite` ("dry run").

## 16.4 Run history and notifications

### TC-AUT-021 — The run history applies the role scope inside the query
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Runs on leads and deals owned by two sales users, a lead reassigned after its
  run started, an erased lead's run.
- **Steps:**
  1. In the repository, list with the admin scope and with each sales scope, with offset and limit,
     and with every allowlisted and a forbidden sort column.
  2. In the service, list and get as support, customer and an empty role; as admin; as sales.
  3. Over HTTP, `GET /automation/runs` and `/automation/runs/{id}` as admin, sales (own and
     somebody else's run), support and customer; `?sort_by=rule_id`; `?status=paused`.
- **Expected:** The admin scope returns every run; the sales scope only runs whose lead or deal
  the caller owns now (the reassigned lead's run follows the lead, the erased lead's run belongs to
  nobody), the total matching and the scope applied before offset and limit; the sort allowlist is
  `id, created_at, updated_at, status, resume_at`; support, customer and no role get
  `ErrForbidden` before any query; over HTTP somebody else's run is **404** `NOT_FOUND` for sales,
  support and customer get **403**, the bad sort and status are **400**.
- **Automation:** automated — `internal/repository/automation_repository_test.go`:
  `TestAutomationRunListScopeSQLite`, `TestAutomationRunListScopeMySQL`;
  `internal/service/automation_run_service_test.go`: `TestAutomationRunServiceRoles`;
  `tests/automation_api_test.go`: `TestAutomationRunAPISQLite`.

### TC-AUT-022 — Cancel is admin-only, manual, and refuses a finished run
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A waiting run.
- **Steps:** `POST /automation/runs/{id}/cancel` as sales, then as admin, then again; then on an
  unknown id.
- **Expected:** **403**; **200** with `cancelled`/`manual`; **409** `INVALID_STATUS_TRANSITION`;
  **404**.
- **Automation:** automated — `TestAutomationRunAPISQLite` ("cancel is admin-only and refuses a
  finished run"); `TestAutomationRunServiceRoles`.

### TC-AUT-023 — Notifications are the recipient's own
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** 25 notifications for a sales user, 2 for a support user, 1 for the admin.
- **Steps:** `GET /notifications` (default page, page 2) as each staff role; `?unread=sometimes`;
  `GET /notifications/unread-count`; `POST /notifications/{id}/read` on an own notification twice,
  on somebody else's, on an unknown id; `?unread=true`; `POST /notifications/read-all`; every route
  as customer.
- **Expected:** Each user sees only its own rows, newest first, 20 per page with the total; the
  bad boolean is **400**; the unread count is the caller's; marking an own notification is **200**
  (twice), somebody else's and an unknown id **404**; read-all marks only the caller's unread rows
  and the other user's count is unchanged; customers get **403** everywhere.
- **Automation:** automated — `tests/notification_api_test.go`: `TestNotificationAPISQLite`.

## 16.5 Runtime, erasure and migration

### TC-AUT-024 — Boot reconciliation and the drain
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A run left `running` between two ordinary steps; one left `running` after
  its first email transaction; a waiting and a completed run.
- **Steps:**
  1. Rebuild the engine and reconcile; execute.
  2. Through the real wiring over a SQLite file: seed a stranded run, build the router with
     `AUTOMATION_ENABLED=true`, record an event, wait for the worker, stop under the deadline,
     record another event, wait. Repeat with `AUTOMATION_ENABLED=false`, again with a stranded
     run seeded first.
  3. Start a runtime whose first step pauses after its effect; stop it while paused; release.
- **Expected:** (1) the interrupted run is `pending` at its `next_position` and later completes
  without repeating its first step; the unconfirmed email's row and run are `failed` with
  `email_unconfirmed` and the mail is never sent again; waiting and completed runs are untouched;
  an idle reconcile is a no-op. (2) The stranded run is failed before the worker starts and the log
  says so; "Automation worker started" appears once; the event becomes a completed run with its
  notification; the stop logs "Automation worker stopped", the goroutine count returns to its
  baseline, the later event stays unprocessed and a second stop is a no-op; with the worker off,
  "Automation worker not started" appears, the stranded run is *not* reconciled (still `running`,
  its `sending` row untouched, no recovery log line — a process without the worker owns no run
  and leaves the worker replica's runs alone), the event is never dispatched and the stop is
  immediate. (3) Stop blocks while the step is in flight, returns once it commits, the run is
  handed back at its next position, the second run was never claimed, and no row changes after
  Stop returned; a second Stop is a no-op even on a context that has already ended, as is Stop on
  a runtime that never started; a fresh runtime finishes both runs.
- **Automation:** automated — `internal/service/automation_reconcile_test.go`:
  `TestAutomationReconcileSQLite`, `TestAutomationReconcileMySQL`;
  `cmd/automation_runtime_test.go`: `TestAutomationRuntimeLifecycle`;
  `internal/service/automation_runtime_test.go`: `TestAutomationRuntimeDrain`.

### TC-AUT-025 — Lead and user erasure end the automation state inside the erasure transaction
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A lead with pending, waiting, running and completed runs and notifications
  (plus another lead's); a user with notifications.
- **Steps:** Erase the lead; erase the user; erase a lead with a delete callback that fails the
  notification purge; (MySQL) erase a lead from a second connection while a step holds the lead
  under `FOR UPDATE`.
- **Expected:** The lead's pending, waiting and running runs are `cancelled`/`record_deleted`, the
  completed one and the other lead's run untouched, the lead's notifications gone and the other
  lead's kept, the lead scrubbed and soft-deleted; the user's notifications are gone and the other
  user's kept; the failing purge rolls the whole erasure back (the run still pending, the
  notification and the lead's data still there); on MySQL the step's advance is the deadlock
  victim, the erasure commits, a retried step finds the lead gone and the run cancelled, and no
  run-step row, task or notification exists.
- **Automation:** automated — `internal/repository/automation_erasure_test.go`:
  `TestAutomationErasureSQLite`, `TestAutomationErasureMySQL`.

### TC-AUT-026 — The migration pair creates and drops the six tables
- **Priority:** P1
- **Type:** functional
- **Preconditions:** `TEST_MYSQL_DSN` for the schema root.
- **Steps:** Read the pair; on MySQL, migrate to the previous head, optionally AutoMigrate first
  and insert a rule, apply the pair by version (`Migrate(20261028120000)`, so a later pair cannot
  change what the root exercises), compare every table with the AutoMigrate reference, run
  `MigrateDatabase`, `Migrate` to the same version again, `Steps(-1)`, `Migrate` again.
- **Expected:** The up file creates the six tables with the unique keys and the five foreign keys
  and alters nothing; the down file drops the six; `main_test.go`'s expected-table list names
  them; on MySQL the schema equals the AutoMigrate reference column for column, index for index
  and key for key, the pre-existing row is kept, `MigrateDatabase` changes nothing, the repeated
  `Migrate` is `ErrNoChange`, the rollback drops exactly the six tables and the re-apply restores
  them.
- **Automation:** automated — `cmd/migrate/automation_migration_test.go`:
  `TestMigrationAutomationTablesListed`, `TestMigrationAutomationSchema`;
  `cmd/migrate/main_test.go` version and table assertions.

### TC-AUT-027 — Logs omit personal data and mail text
- **Priority:** P2
- **Type:** regression
- **Preconditions:** A lead with distinctive name, email, phone and notes; a rule with every step
  type; a failing send.
- **Steps:** Dispatch, execute and dry-run while capturing the logger.
- **Expected:** No log line carries the lead's name, email, phone or notes, the email or
  notification subject or body, the rendered task title, a staff address or the transport error;
  the lines carry `run_id`, `rule_id`, `position`, `step_type`, `outcome` and `error_code`.
- **Automation:** automated — `internal/service/automation_log_privacy_test.go`:
  `TestAutomationLogsOmitPersonalData`.

## 16.6 Frontend

### TC-AUT-028 — Rule list with pagination, sorting and the enable switch
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Rules in the API; the admin.
- **Steps:** Open `/automation/rules`; sort and page; flip a switch on and off; open the route as
  sales.
- **Expected:** Rows from the server (name, entity type, trigger summary, switch, version,
  updated time) with Dry run and Delete rule tools; sorting is server-side on the allowlisted
  columns; switching on calls `POST /automation/rules/{id}/enable` straight away, switching off
  asks for confirmation (it cancels the rule's active runs) and then calls `disable`, and the list
  refreshes; delete asks first; sales is redirected to `/unauthorized`.
- **Automation:** automated — `gocrm-ui/src/pages/automation/AutomationRuleList.test.tsx`:
  "renders name, entity, trigger, enabled switch, version and updated time from a mocked list
  response", "pages with Next and page size: offset follows the zero-based table page and a size
  change resets to the first page", "sorts server-side on the allowed columns and never on Tools",
  "enabling calls the enable endpoint and refreshes the list", "disabling asks for confirmation,
  then calls the disable endpoint and refreshes the list", "a sales user on the route renders the
  unauthorized redirect".

### TC-AUT-029 — Rule form mirrors the server limits
- **Priority:** P1
- **Type:** validation
- **Preconditions:** The admin.
- **Steps:** On `/automation/rules/new`, build a `lead_status_changed` rule with wait, condition,
  task and notify steps and save; try a 21st step; try a backward jump; edit an existing rule;
  submit a rule the server refuses.
- **Expected:** The POST payload matches the API; Add step is disabled at 20 steps and a jump can
  target only a later step; the server's 400 message is shown on the step it names (`step N: ...`)
  or above the buttons (the API returns a message, not a field map); saving an edit asks for
  confirmation, warns that the rule's pending, waiting and running runs will be cancelled, and
  sends no `enabled`.
- **Automation:** automated — `gocrm-ui/src/pages/automation/AutomationRuleForm.test.tsx`: "builds
  a lead_status_changed rule with wait, condition, task and notify steps and asserts the POST
  payload", "refuses a 21st step client-side", "refuses a backward else_position client-side",
  "editing shows the cancellation warning before the PUT and omits enabled", "shows a server 400
  message on the step it names and other messages above the buttons".

### TC-AUT-030 — Dry-run dialog
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A rule and a lead the admin can see.
- **Steps:** Open "Dry run" from the list and from the edit page; pick the record; submit; repeat
  with a record that answers 404.
- **Expected:** One row per step with its outcome chip, simulated time and details, a persistent
  notice that nothing is written, sent or notified; the record type is fixed to the rule's entity
  type; from the edit page the saved version is simulated (a caption says so); the 404 shows
  inline.
- **Automation:** automated — `gocrm-ui/src/pages/automation/DryRunDialog.test.tsx`: "a mocked
  report renders every step row and the no-side-effects notice", "fixes the record type to the
  rule entity type and offers deals of every stage", "a 404 shows the error inline";
  `AutomationRuleList.test.tsx`: "opens the dry-run dialog from the Tools column without opening
  the editor"; `AutomationRuleForm.test.tsx`: "opens the dry run from the edit page for the saved
  rule even with unsaved changes".

### TC-AUT-031 — Run list, run detail and the lead and deal "Automation" sections
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Runs in the API; an admin, a sales and a support user.
- **Steps:** Open `/automation/runs`, filter by status, rule and entity; open a run; open a lead
  and a deal detail.
- **Expected:** The filters build the query string (sales filters by rule ID and never requests
  the rule list); runs show `Rule #<id> · v<n>` because the API does not return the rule name;
  the detail shows the step log and, for the admin only, a Cancel run button on pending, waiting
  and running runs; the detail pages list the record's runs through
  `GET /automation/runs?entity_type=&entity_id=`; support and customers see no section.
- **Automation:** automated — `gocrm-ui/src/pages/automation/AutomationRunList.test.tsx`: "list
  filters build the query: status, rule, entity type and record id", "lets a sales user filter by
  rule id without requesting the rule list"; `AutomationRunDetail.test.tsx`: "the detail shows
  step rows with outcome, time and details", "Cancel is visible for admin only on pending,
  waiting and running runs"; `AutomationRunsSection.test.tsx`: "lists the record runs through
  entity_type and entity_id", "renders nothing for support and customer users", "LeadDetail
  renders the section", "DealDetail renders the section".

### TC-AUT-032 — Notification bell and page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Notifications for a staff user; a customer user.
- **Steps:** Load the app as each role; click the bell; mark one read; "Mark all read"; open
  `/notifications` with the unread filter.
- **Expected:** Admin, sales and support see the bell with the unread count (refetched every 60 s
  and on focus) and the latest 10; marking read updates the count; the page is paginated with an
  unread filter; customers see no bell and nothing is requested for them.
- **Automation:** automated — `gocrm-ui/src/components/layout/NotificationBell.test.tsx`: "shows
  the unread count badge for admin, sales and support", "renders no bell and requests nothing for
  the customer role", "opens the latest 10 notifications and marks one read on click", "marks all
  read from the menu", "refetches the unread count at 60,000 ms and not at 59,999 ms", "refetches
  the unread count when the window regains focus";
  `gocrm-ui/src/pages/notifications/NotificationList.test.tsx`: "lists notifications with server
  pagination", "the unread filter sends unread=true".

### TC-AUT-033 — End to end: create a disabled rule, dry-run it, the count is unchanged
- **Priority:** P1
- **Type:** functional
- **Preconditions:** The seeded admin and a lead created by the spec through the API (removed
  again afterwards with the rule); `make e2e` (MySQL) or `make e2e E2E_DB_DRIVER=sqlite`.
- **Steps:** Log in; note the bell's unread count; open `/automation/rules/new`; fill a
  `lead_created` rule with a condition (status equals new) and a notify step to the record owner;
  save it disabled; find it in the list with its switch off; open the dry run from the Tools
  column; choose the lead; run it; go back to the dashboard.
- **Expected:** The POST carries `enabled: false` and both steps; two step rows render (condition
  succeeded and continues, notify succeeded for the admin); the bell's unread count and
  `GET /notifications/unread-count` are unchanged.
- **Automation:** automated (partial: admin only; the sales and support behaviour is in the
  Vitest files of TC-AUT-028 to 032) — `gocrm-ui/e2e/tests/automation-rules.spec.ts`: "admin
  creates a disabled lead_created rule with a condition and a notify step, dry-runs it on a lead
  and the unread count stays the same".
