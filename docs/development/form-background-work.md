# Form background work

> **For:** Developers changing the form retention purge, form mail, attribution and ticket intake. Part of the [documentation index](../README.md).

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
  is kept by erasure (see [erasure.md](erasure.md)). Log lines never carry the title.
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
  [../reference/data-model.md](../reference/data-model.md#form-ticket-keys)) come from `MigrateDatabase` on SQLite and from
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
