# Personal-data deletion

> **For:** Developers changing what deleting a user, customer or lead does. Part of the [documentation index](README.md).

What deleting a user, customer or lead does: the erasure cascade, what is scrubbed and what is
kept, external ids, the operator procedures, the limits and the log policy. Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

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
  customer, so only an admin may erase one: `LeadService.Delete(ctx, id, actorRole)` gives any other
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
- **Response links (#395).** After the text, the same step stamps `used_at` on every unused
  `quote_response_tokens` row of the quotes whose `customer_id` is an erased customer (live and
  soft-deleted quotes, every status) and leaves `decision` NULL
  (`invalidateErasedCustomerQuoteLinks` in `business_text_erasure.go`). A response link mailed
  before the erasure is then refused on both public routes with the generic 400 "Invalid or
  expired quote link", and the quote keeps its status, dates, totals and history. A row the client
  already spent is unchanged, and so are the tokens of a quote that was reached only through a
  scrubbed deal and is addressed to another, live customer: that link was mailed to the other
  customer. The rows are kept (a hash, ids and timestamps). The stamp runs after the unit's
  update of those `quotes` rows (the order `quotes → quote_response_tokens`), reads the ids with a
  plain `SELECT` and writes by primary key only, so it holds no gap of the `quote_id` index
  ([CONCURRENCY.md](CONCURRENCY.md)); the table is probed with `HasTable` like the others. It is
  part of the unit: a rollback hands the link back. The ids come from the snapshot of the
  erasure's transaction, so a token minted by a quote email that is in flight while the erasure
  runs is not stamped; that link is refused all the same, because the public read requires a live
  customer and the answer re-reads the customer under the quote lock. A mint that locks the quote
  after the erasure committed reads the customer as gone, stores no token and sends nothing.
- **One unit, exactly once.** The whole graph is one transaction; a repository obtained through
  `WithTx` brackets it in a checked `SAVEPOINT` of the caller's transaction (released on success,
  rolled back on an error or a panic, sent past GORM's prepared-statement wrapper), so a caller
  that handles a failed erasure, or recovers its panic, and commits keeps none of it (a failed `ROLLBACK TO SAVEPOINT` rolls
  the caller's transaction back and reports `utils.ErrRollbackFailed` +
  `utils.ErrTransactionPoisoned`). Nothing is retried inside; a deadlock reaches the caller. The
  traversal remembers every row it has visited, checked before any read, so each row is scrubbed
  once and the cycle user → customer → lead → customer → user terminates.
- **Cancellation (#571).** The erasure runs on the request's context, from the handler
  (`c.Request.Context()` in the customer, lead and user `Delete` handlers) through
  `CustomerService.Delete(ctx, id)`, `LeadService.Delete(ctx, id, actorRole)` and
  `UserService.Delete`/`DeleteAs` into `eraseGraph` and `runErasureUnit`: the discovery, the
  transaction (`WithTransactionAndRetry(ctx, …)`) and every statement of the unit are bound to it.
  A request that is cancelled or times out before the commit rolls back every overwrite, the
  credential purges and the soft delete, and answers the generic 500; the error wraps
  `context.Canceled` or `context.DeadlineExceeded` and is neither replayed nor restarted. Inside a
  caller's transaction (`WithTx`, the governed user delete) the unit's savepoint is rolled back on
  `context.WithoutCancel(ctx)`, so a cancelled request still discards the unit's writes without
  poisoning a caller transaction whose own context is alive. Regression roots:
  `TestErasureCancellation{SQLite,MySQL}` (`internal/repository/erasure_cancellation_test.go`:
  user, customer and lead roots, cancelled before the call, after the first scrub, past a deadline
  and inside a caller's transaction), and the handler tests `TestDelete_PassesTheRequestContext`
  of the customer, lead and user handler suites.
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
  `TestGovernedUserErasureVsCustomerErasureMySQL`, `TestGovernedWriteReplaysErasureGraphChange`,
  `TestPasswordResetConfirmRetrySQLite`),
  through the test-only `repository.ErasureHooks`. CI limit: `erasure_lock_order_test.go`,
  `erasure_credential_order_test.go` and, for the response links (#395),
  `erasure_quote_tokens_test.go` and `quote_response_erasure_test.go`, and for the deleted webhook
  events written at the end of the unit (#437) `webhook_deleted_events_test.go`, are discovered by
  `scripts/ci/check-go.py transaction`. CI
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
| `user_identities` of an erased user (#435) | not touched | The row (issuer URL, opaque subject, `user_id`) stays linked to the erased user, so the erased person's next OIDC sign-in is refused (401) instead of provisioning a new account. It holds no name or address |
| `auth_audit_log` | not touched | Ids only (`user_id`, `attempt_id`), a reason code, the request id, an HMAC subject reference, claim values, a `/24` or `/48` network prefix, a user-agent family, a count (`attempts`) and, on an aggregated API-key row, an `aggregate_key` built from the method, reason, `user_id`, network and minute (#466); no foreign key. Rows age out after `AUTH_AUDIT_RETENTION_DAYS` (default 90) through the stale bulk operation sweep (#454) |
| `oidc_login_attempts` | not touched | No user link: a state hash, a nonce and a PKCE verifier (both blanked once the row is used or found expired), the issuer and timestamps; the stale bulk operation sweep prunes rows a day after their creation |
| `notifications` addressed to an erased user, whose entity is an erased lead, or whose entity is a deal reached below (#245) | hard-deleted | notifications about unreached deals and other leads |
| `ticket_comments` written by an erased user (live and soft-deleted) | `body` → `[deleted]` | ticket, author id, `is_internal`, deletion state |
| `automation_runs` of an erased lead | pending/waiting/running → `cancelled` / `record_deleted` | finished runs (ids and statuses only); runs of a reached deal keep running and render the redacted title |
| `deals` whose `customer_id` is an erased customer or whose `lead_id` is an erased lead (live and soft-deleted; #245) | `title` → `[redacted]`; `notes`, `lost_reason` → `''` | stage, probability, amount, currency, `source`, every link, `owner_id`, dates, deletion state |
| `quotes` whose `customer_id` is an erased customer or whose `deal_id` is a deal above (every status, live and soft-deleted; #245) | `title` → `[redacted]`; `notes` → `''` | status, currency, totals, `valid_until`, `sent_at`, `closed_at`, `last_emailed_at`, every link, `owner_id`, deletion state |
| `quote_lines` of those quotes | `description` → the name of the line's product (soft-deleted products included) | product, quantity, prices, discount, total, position |
| `quote_response_tokens` of the quotes whose `customer_id` is an erased customer (#395) | unused rows: `used_at` stamped, `decision` stays NULL, so a mailed response link is refused | the rows, `token_hash`, `quote_id`, `expires_at`; rows the client spent; the tokens of quotes addressed to another customer. A hash, ids and timestamps, no personal data |
| `tickets` (`title`, `description`, `resolution`), `tasks` (`title`, `description`), `companies` (`notes`), staff-written ticket comments, deals and quotes reached only through `owner_id` | not touched | Business records. Free text may have been typed to contain a name; that is an accepted limitation, not scrubbed (no reliable way to tell a name from content). Ticket text can be redacted by an operator before the erasure (see *Operator procedure for ticket text*). A task a `create_task` step rendered from `{{deal.title}}` before the erasure keeps that text |
| `automation_events`, `automation_run_steps`, `deal_stage_changes`, `quote_status_changes`, `bulk_operations`, `products`, `deals.source` | not touched | Their writers store ids, statuses, values of non-personal fields and error codes; the `form_id` and `submission_id` of a `lead_form_submitted` event are ids, and its attribution stays on the submission row, which is scrubbed. Products are catalogue rows, not personal records |
| `roles`, `role_permissions`, `role_audit_log` (#344) | not touched | Role names carry no personal text by rule; audit rows hold ids, role names, permission keys, scopes and `is_active` only, by contract (the #348 writers store role names, descriptions, permissions, `role` and `role_id` only; the #552 account rows store `is_active` or nothing, never an address). An erased user keeps its `role_id` until `ClearRoleIDOfErased` |
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

Regression roots for the response links of an erased customer's quotes (#395), on SQLite and on
a disposable `TEST_MYSQL_DSN` schema, both files registered in the transaction profile of
`scripts/ci/check-go.py`: `internal/repository/erasure_quote_tokens_test.go`:
`TestErasureInvalidatesQuoteResponseTokens{SQLite,MySQL}` (customer, converted-lead and
customer-login roots, spent and foreign tokens untouched, no sent quote, a `WithTx` rollback) and
`TestErasureQuoteTokenWritesLeaveGapsFreeMySQL` (another session inserts tokens of other quotes
while the erasure is open); `internal/service/quote_response_erasure_test.go`:
`TestQuoteResponseAfterCustomerErasure{SQLite,MySQL}` (mint, erase, both public calls refused;
the erasure racing a client answer).

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
the `customers.view` check the handler validates the three values, with no service call
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
schema and skip without it; the Database migrations workflow runs the migration roots.

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
  side, and a governed delete replayed after the other erasure committed answers 404); inside a caller's transaction the re-check of the
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
