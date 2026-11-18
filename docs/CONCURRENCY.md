# Concurrency: lock order and retry rule

One place for the row-lock order and the retry rule of every write that locks rows of more than
one table: the erasures, lead conversion and update, company delete, bulk status, the session and
credential flows, and RBAC writes. It records what the code does at `a415cf1` (with Login updated
for #336, the password-reset request and API-key create for #342, and the lead-rooted erasure
against lead conversion for #337, and the RBAC writes for #348) and, separately, the target that #183 asks for. A flow whose order is only a target is marked
`not implemented` or `partial`; nothing here describes a target as current behaviour.

Each flow lists five fields:

- **Rows locked** — the rows the flow holds a lock on, and the statement that takes it.
- **Order** — the order those locks are taken in.
- **Transaction** — the transaction helper, or `no transaction`.
- **Retry** — what is replayed after a failure.
- **Status** — `implemented` (the flow follows its chain of the global order), `partial` (one
  transaction, but no order shared with the flows it races) or `not implemented` (no transaction or
  no lock today).

On MySQL/MariaDB a locking read is `SELECT … FOR UPDATE`; SQLite drops the clause and serialises
writers on its single connection, so every order below matters on MySQL 8 and MariaDB 10.11 only.
The documentation test `internal/config/concurrency_doc_test.go` keeps this document tied to the
code: a cited file or line that no longer exists, or a pinned fact the code no longer has, fails
it. QA still reviews each entry against the code paths it cites.

## Global lock order

Three chains are specified. A transaction that locks rows of one chain takes them left to right.

- `companies → leads → customers → deals` (#182, implemented): companies by primary key
  (`CompanyRepository.LockLive`, `internal/repository/company_repository.go:64`), leads by id
  ascending, then customers, then deals. Written down at `internal/service/lead_service.go:190`.
- `users → refresh_tokens`, `users → password_reset_tokens` and `users → api_keys` (implemented
  for every session and credential-issuing transaction): the owner's `users` row first, by primary
  key, its child rows second. Login, refresh rotation, password change and logout follow
  `users → refresh_tokens`; the password-reset request follows `users → password_reset_tokens`
  and API-key create `users → api_keys` (#342). The password-reset confirm is the exception: it
  claims its `password_reset_tokens` row before the owner, `password_reset_tokens → users`, the
  inversion of the reset request's order (see both flows).
- `admin set → users (id ascending) → roles (id ascending)` (#259, implemented by #348) for every governed user write and, without the admin set, every role write.
  The admin set is the `users` rows of the active admins, locked by primary key in id order; a
  role's `role_permissions` rows come after its `roles` row (see the RBAC flow below).

**Outbound webhooks (#345), a self-contained order.** The endpoint delete
(`internal/repository/webhook_repository.go`, `DeleteEndpoint`) takes `webhook_endpoints →
webhook_deliveries`: a locking read of the endpoint by primary key, then the delete of its delivery
rows, then the endpoint. The delivery runtime (`internal/service/webhook_runtime.go`) never takes a
locking read: it discovers due rows, expired leases and prunable rows with plain `SELECT`s and
writes each row by primary key with a conditional `UPDATE` or `DELETE` that re-checks the
discovered state, so it never locks through `idx_webhook_deliveries_status_next` or
`idx_webhook_deliveries_updated_at`. The outbox rows a create path writes are inserts into its own
transaction after the record insert, behind a plain read of the endpoints; they add no lock edge to
the chains above.

**Cross-chain order (undecided).** No source specifies the order for flows spanning several
chains: an erasure reaches users, customers and leads from whichever root it starts at, and the
session and credential flows touch users while lead conversion touches leads and customers. `C4`
(#183) owns that decision. Until then the current erasure order depends on the root: the
user-rooted erasure locks the user before its customers and leads, the customer-rooted erasure
locks the customer before its leads and its login, and the lead-rooted erasure cancels the lead's
automation runs and then locks the lead before the customer it was converted into (#337). This
document pins no combined sequence across the chains.

## Retry rule

The rule is `WithTransactionAndRetry` in `internal/utils/transaction.go:176`:

- Each attempt is a fresh root transaction (`runRoot`, `internal/utils/transaction.go:244`). Only a
  root transaction is replayed, and only after a clean rollback: a failed `COMMIT` or `ROLLBACK`
  is ambiguous and is returned as it is (`internal/utils/transaction.go:196-198`).
- An attempt is replayed only for a retryable error: MySQL error 1213 (deadlock) or 1205 (lock wait
  timeout), SQLite busy or locked (`isRetryableError`, `internal/utils/transaction.go:473`).
- Replays stop after `maxRetries` replays and as soon as the context is done; a negative
  `maxRetries` is refused before the callback runs.
- Inside an active transaction the callback runs once under a savepoint, and a retryable failure is
  returned tagged `ErrNestedRetryDeferred` (`internal/utils/transaction.go:60`, `:181-187` in the
  same file): a deadlock has already destroyed the enclosing transaction, so only the enclosing
  root may replay the whole unit.

The rule is current behaviour only for lead conversion (`internal/service/lead_service.go:1064`),
login (`internal/service/auth_service.go:283`), the password-reset request
(`internal/service/auth_service.go:1220`), API-key create
(`internal/service/apikey_service.go:86`) and the RBAC user-role and role writes
(`internal/service/role_governance.go:116`).
For every other flow below it is the #183 target, a bounded whole-root retry, not today's
behaviour. The lead writes also restart the whole root on `errLeadCompanyMoved`, a separate bounded
loop (`runLeadCompanyAttempts`) described in their entries.

## Flows

### User-rooted erasure

- **Rows locked:** the `users` row (locking read of its role,
  `internal/repository/erasure_cascade.go:464`, then the scrub and a locking read of the address,
  `readErasedEmail`, `internal/repository/erasure.go:271`), its `api_keys`, `refresh_tokens` and
  `password_reset_tokens` rows (deleted by `purgeCredentials`,
  `internal/repository/erasure.go:508`), and for a customer-role login every linked `customers`
  row and the `leads` converted into them; then the text of their deals and quotes
  (`internal/repository/erasure_cascade.go:324`).
- **Order:** users → api_keys, refresh_tokens, password_reset_tokens → customers (id order,
  `internal/repository/erasure_cascade.go:484-493`) → leads converted into each customer (id
  order) → deals and quotes text. Each converted lead is reached through `eraseLead`, so it
  inherits the lead-rooted per-lead order customers → automation runs → leads: the lead's runs are
  cancelled, then the lead is read with a locking read by id (#337). A staff login stops after its
  credentials.
- **Transaction:** `userRepository.Delete` → `eraseGraph`
  (`internal/repository/user_repository.go:180`) → `runErasureUnit`
  (`internal/repository/erasure.go:407`): a `db.Transaction`, or a checked SAVEPOINT inside the
  caller's transaction. On `DELETE /users/{id}` the caller is the governed user write of #348
  (see the RBAC flow), which holds the admin set, the actor and the target before the erasure
  starts.
- **Retry:** none today for the erasure itself; a deadlock or lock wait timeout reaches the
  caller, which on `DELETE /users/{id}` is the governed user write and replays its whole root
  (see the RBAC flow). Target (C4): the #183 retry rule on every erasure root.
- **Status:** partial

The user is locked before its customers and leads, the opposite of the `companies → leads →
customers → deals` direction a lead-rooted erasure follows. The credential purge takes
`password_reset_tokens` after the user, while password-reset confirm takes them in the opposite
order, so the two can deadlock (an error and a rollback, never a partial commit). The
password-reset request and API-key create lock the user before their child rows (#342), the
erasure's own order: against a user-rooted or customer-rooted erasure they either commit first
and the purge removes what they issued, or wait on the `users` row and are refused.

### Customer-rooted erasure

- **Rows locked:** the `customers` row (the scrub `UPDATE` by id is the first statement on the
  row and takes its lock, `internal/repository/erasure.go:161`; the address and then `user_id` are
  read after it with locking reads by id, `readErasedEmail` at
  `internal/repository/erasure.go:271` and `internal/repository/erasure_cascade.go:420`), the
  `leads` converted into it, then its customer-role login (`users`) with that login's
  credentials; then the deals and quotes text.
- **Order:** customers → automation runs → leads (per converted lead, in id order: the plain
  pluck by `customer_id`, `internal/repository/erasure_cascade.go:436-437`, then `eraseLead`
  cancels the lead's runs and reads the lead with a locking read by id,
  `internal/repository/erasure_cascade.go:362` and `:373` in the same file) → users
  (`internal/repository/erasure_cascade.go:449`) → deals and quotes text.
- **Transaction:** `customerRepository.Delete` (`internal/repository/customer_repository.go:158`)
  → `eraseGraph` → `runErasureUnit`, as for the user-rooted erasure.
- **Retry:** none today. Target (C4): the #183 retry rule.
- **Status:** partial

### Lead-rooted erasure

- **Rows locked:** the automation runs of the lead (cancelled first, `cancelLeadAutomation`, the
  lead plan's `BeforeScrub`, `internal/repository/erasure_cascade.go:76`, run by `eraseLead` at
  `internal/repository/erasure_cascade.go:362`), the `leads` row (locking read by id,
  `internal/repository/erasure_cascade.go:373`, the first read of the leads row in the unit; the
  non-admin refusal and whether `customer_id` is followed are decided on that locked row only),
  then the customer it was converted into (`eraseCustomer`: the scrub `UPDATE` by id, then
  locking reads by id of the address and of `user_id`, `internal/repository/erasure_cascade.go:415`
  and `:420` in the same file) with that customer's other leads and its login.
- **Order:** automation runs (`cancelLeadAutomation`) → leads (locking read by id,
  `internal/repository/erasure_cascade.go:373`) → customers (locking read by id, `eraseCustomer`,
  `internal/repository/erasure_cascade.go:420`) → users → deals and quotes text. The converted-lead
  pluck (`WHERE customer_id = ?`, `internal/repository/erasure_cascade.go:436-437`) stays a plain
  read: a locking read there would take next-key locks on the `leads.customer_id` index and block
  unrelated conversions, and it is not needed, because a conversion always inserts a new customer,
  so no lead can be converted into a customer being erased. Every erased row is soft-deleted by
  primary key only (`softDeleteErased`, `internal/repository/erasure.go:231`: a locking read of
  whether the row is live, then GORM's soft delete with its `deleted_at` predicate suppressed,
  `UPDATE … SET deleted_at = ? WHERE id = ?`, still issued through the Delete callback chain),
  never with GORM's `AND deleted_at IS NULL`: with that predicate the customer soft delete was
  seen holding a gap lock on `idx_customers_deleted_at` on MariaDB 10.11 (not on MySQL 8), which
  blocked an unrelated conversion's customer `INSERT` until the erasure committed.
  QA recorded `TestLeadErasureUnrelatedConversionNotBlockedMySQL` passing with the
  primary-key-only soft delete as an explicit `UPDATE` on MariaDB 10.11.19 and MySQL 8.0; the root
  also passed in the Delete-chain form used now, together with `TestIdentityErasureMySQL`, on
  MariaDB 10.11 (QA) and on MySQL 8.0 (the #355 acceptance run). The cancel stays ahead of the
  lead lock so that the erasure has modified rows when it waits on a lead an automation step
  holds, and is the survivor of that deadlock.
- **Transaction:** `leadRepository.Delete` (admins, `internal/repository/lead_repository.go:159`)
  or `leadRepository.DeleteUnconverted` (non-admins, refused with `ErrConvertedLeadNeedsAdmin` on
  the locked lead row before the lead is written, the cancel rolled back with the unit,
  `internal/repository/lead_repository.go:172`) → `eraseGraph` → `runErasureUnit`.
- **Retry:** none today. Target (C4): the #183 retry rule.
- **Status:** partial

Against lead conversion (companies → leads → customers) there is no cycle: both lock the lead row
by primary key before they touch the customer, and the erasure takes no company lock. Conversion
first: the erasure waits on the lead's locking read, then reads the committed `customer_id`; an
admin erasure erases the new customer too, a non-admin erasure is refused and changes nothing.
Erasure first: the conversion waits on its `lockedLeadRead` and then finds the lead gone (see
Lead conversion). A conversion committed between the cancel and the lead lock is seen the same
way. Neither ordering ends in error 1213.

Customer- and user-rooted erasures still share no order with lead conversion beyond the per-lead
order above (customer/login linking races stay #183), no erasure shares one with the session
flows, and their order depends on the root. With no retry rule yet, all three are `partial`.

### Lead conversion

- **Rows locked:** the `companies` rows of the lead's stored company and of a caller-supplied
  `company_id` (`lockLeadCompanies`, `internal/service/lead_service.go:275`), then the `leads` row
  (`lockedLeadRead`, `internal/service/lead_service.go:1123`); the new `customers` row is inserted
  (`internal/service/lead_service.go:1186`). No deal is touched.
- **Order:** a plain read of the lead outside the transaction
  (`internal/service/lead_service.go:1019`); in each attempt a plain read of `company_id`
  (`internal/service/lead_service.go:1102`) → companies, ascending id
  (`internal/service/lead_service.go:288`) → the locked lead → customers. A locked lead that names
  another company ends the attempt with `errLeadCompanyMoved`; at most `leadCompanyMaxRestarts`
  restarts (`internal/service/lead_service.go:214`). A conversion queued behind a lead erasure
  finds the lead gone: its `lockedLeadRead` returns not found (`gorm.ErrRecordNotFound`, which
  `apperrors.IsNotFound` matches, so `POST /leads/{id}/convert` answers 404) and nothing is written
  (#337, see Lead-rooted erasure).
- **Transaction:** a transaction root (an enclosing transaction is refused with
  `utils.ErrTransactionRootRequired`); `runLeadCompanyAttempts`
  (`internal/service/lead_service.go:1063`) around one root transaction per attempt.
- **Retry:** `WithTransactionAndRetry` with `maxRetries` 3 (`internal/service/lead_service.go:1064`,
  `:1053` in the same file) replays the attempt's root on a deadlock, lock wait timeout or SQLite
  busy/locked; around it `runLeadCompanyAttempts` (`internal/service/lead_service.go:334`) restarts
  the whole root at most `leadCompanyMaxRestarts` (3) times, on `errLeadCompanyMoved` only and only
  after a clean rollback (`shouldRestartLeadCompanyAttempt`, `internal/service/lead_service.go:227`),
  then returns `ErrLeadCompanyContended`.
- **Status:** implemented

### Lead update

- **Rows locked:** the `companies` rows of the stored and of the sent `company_id`, the `leads`
  row (`lockedLeadRead`) and, when `owner_id` changes, the new owner's `users` row (`lockedUserRead`,
  `internal/service/lead_service.go:163`).
- **Order:** in each attempt a plain read of `company_id` (`internal/service/lead_service.go:527`)
  → companies, ascending id (`internal/service/lead_service.go:538`) → the locked lead and the
  locked owner (`internal/service/lead_service.go:559`). Same chain and the same
  `errLeadCompanyMoved` check as the conversion, bounded by `leadCompanyMaxRestarts`.
- **Transaction:** `WithTransaction` per attempt (`internal/service/lead_service.go:514`) inside
  `runLeadCompanyAttempts` (`internal/service/lead_service.go:513`).
- **Retry:** `runLeadCompanyAttempts` restarts the whole root at most `leadCompanyMaxRestarts` times,
  on `errLeadCompanyMoved` only; each attempt is a plain `WithTransaction`, so there is no deadlock
  retry: error 1213 or 1205 reaches the caller.
- **Status:** implemented

### Company delete

- **Rows locked:** the `companies` row (`LockLive`, `internal/service/company_service.go:122`),
  then every `leads`, `customers` and `deals` row that names it (`UnlinkLeads`, `UnlinkCustomers`,
  `UnlinkDeals`, `internal/repository/company_repository.go:160-180`), then the company's soft
  delete.
- **Order:** a plain pre-read outside the transaction (`internal/service/company_service.go:104`)
  → companies → leads → customers → deals.
- **Transaction:** `WithTransaction` (`internal/service/company_service.go:116`).
- **Retry:** none today. Target: the #183 retry rule.
- **Status:** implemented

### Bulk status

- **Rows locked:** leads, `FOR UPDATE` in id ascending order (`GetLeadsByIDs`,
  `internal/repository/bulk_status_repository.go:69`, #182, no company lock); tickets, `FOR
  UPDATE` in id ascending order (`GetTicketsByIDs`, `internal/repository/bulk_status_repository.go:89`,
  #242); tasks only at the write; and last the batch's own `bulk_operations` row (`CompletePending`,
  `internal/repository/bulk_operation_repository.go:39`).
- **Order:** the pending `bulk_operations` row is inserted before and outside the transaction
  (`internal/service/bulk_status_service.go:153`); inside it the batch's rows of one table, then
  its `bulk_operations` row (`internal/service/bulk_status_service.go:166`). `GetTasksByIDs`
  (`internal/repository/bulk_status_repository.go:102-104`) reads the tasks without a lock: the
  task batch locks only its task rows (at the `UPDATE` in `SetTaskStatus`,
  `internal/repository/bulk_status_repository.go:114`) and its own `bulk_operations` row
  (`CompletePending`), so it can invert no cross-table order. The completed-task check
  (`internal/service/bulk_status_service.go:370`) is a read before that `UPDATE` and is not
  protected by a row lock: a task completed by another transaction between the read and the
  `UPDATE` is moved out of `completed` (a ROADMAP gap).
- **Transaction:** `WithTransaction` (`internal/service/bulk_status_service.go:158`); a
  transaction root.
- **Retry:** none today. Target: the #183 retry rule.
- **Status:** implemented

### Login

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  `internal/service/auth_service.go:291`, read at `internal/service/auth_service.go:384`) and
  written by `UpdateFields` (`last_login_at` and the lockout clear,
  `internal/service/auth_service.go:409`); then the inserted `refresh_tokens` row
  (`issueRefreshToken`, `internal/service/auth_service.go:431`), whose foreign-key check
  (`fk_refresh_tokens_user`, `migrations/20250806120000_initial_schema.up.sql:172`) takes its
  shared lock on an owner row this transaction already holds.
- **Order:** plain `GetByEmail` outside the transaction (`internal/service/auth_service.go:210`) →
  bcrypt → `FOR UPDATE` owner read by id → `UpdateFields` → refresh token insert in a savepoint
  (`internal/service/auth_service.go:426`) → signing of the access JWT from the locked
  `token_version` (`internal/service/auth_service.go:456`). Lock order users → refresh_tokens. The
  locked row must still carry the hash bcrypt verified, be active, be live (not erased) and not be
  locked, else the login is refused and nothing is written. The failure bookkeeping of a wrong
  password stays a single statement outside the transaction.
- **Transaction:** one issuing transaction per attempt (`loginInTransaction`,
  `internal/service/auth_service.go:371`) on a service built by `NewAuthServiceWithSessions`; a
  service without a transaction manager (`NewAuthService`) keeps the legacy unlocked statements. A
  non-retryable failure of the refresh insert rolls back to its savepoint and the login answers an
  access token only; a signing failure rolls the whole transaction back.
- **Retry:** `WithTransactionAndRetry` with `loginMaxRetries` 3
  (`internal/service/auth_service.go:283`, `:166` in the same file) replays the whole root on a
  deadlock, lock wait timeout or SQLite busy/locked, including one raised by the refresh insert
  (returned tagged `ErrNestedRetryDeferred` from its savepoint).
- **Status:** implemented

### Refresh rotation

- **Rows locked:** the owner's `users` row (`FOR UPDATE`, `internal/service/auth_service.go:717`,
  read at `internal/service/auth_service.go:791`), the presented `refresh_tokens` row (the
  conditional claim `ClaimForRotation`, `internal/service/auth_service.go:807`), and the inserted
  successor (`internal/service/auth_service.go:824`).
- **Order:** a plain lookup of the token by hash (`internal/service/auth_service.go:765`) →
  users → refresh_tokens (the claim, then the successor). The owner is locked before the claim
  (`internal/service/auth_service.go:778`). Every owner decision (active, live) is taken on the
  locked row; the token itself is re-checked by the conditional claim.
- **Transaction:** `WithTransaction` (`internal/service/auth_service.go:705`).
- **Retry:** none today. Target (C2): the #183 retry rule.
- **Status:** implemented

Logout (`internal/service/auth_service.go:875`) and password change
(`internal/service/auth_service.go:1013`) follow the same users → refresh_tokens order in one
`WithTransaction` each, and decide on the locked owner row (the password change re-checks the
verified hash and the active flag under the lock).

### Password-reset request

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  `internal/service/auth_service.go:1228`, read by `lockLiveCredentialOwner` at
  `internal/service/auth_service.go:1152`); then the user's outstanding `password_reset_tokens`
  rows, spent by `InvalidateAllForUser` (`internal/service/auth_service.go:1236`), and the inserted
  token (`internal/service/auth_service.go:1244`), both through the transaction-bound repository.
  The insert's foreign-key check (`fk_password_reset_tokens_user`,
  `migrations/20261001120000_v1_2_0_schema_parity.up.sql:152`) takes its shared lock on an owner
  row this transaction already holds.
- **Order:** plain `GetByEmail` outside the transaction (`internal/service/auth_service.go:1191`;
  an unknown or inactive address stops here) → `FOR UPDATE` owner read by id → `InvalidateAllForUser`
  → insert → commit → mail (`internal/service/auth_service.go:1265`), sent once, after the commit,
  to the address of the locked row. Lock order users → password_reset_tokens. A missing, erased
  (soft-deleted) or inactive locked row refuses the request with nothing written and no mail; the
  answer is nil in every case. An erasure or deactivation that commits before the lock therefore
  issues nothing, and one that commits after the request ends the token it issued (the erasure
  hard-deletes it, the confirm refuses an inactive owner).
- **Transaction:** one `WithTransactionAndRetry` root per request
  (`internal/service/auth_service.go:1220`) on `context.Background()`; a failure of the
  invalidation or of the insert rolls both back and sends no mail. A service without a
  transaction manager writes nothing and sends nothing.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `resetRequestMaxRetries` 3
  (`internal/service/auth_service.go:1128`) replays the whole root on a deadlock, lock wait timeout
  or SQLite busy/locked.
- **Status:** implemented

The request (users → password_reset_tokens) inverts the confirm's order (password_reset_tokens →
users) on the same user. When the confirm commits first, the request waits on the `users` row and
then issues a fresh token; when the request commits first, the confirm waits on the token row its
`InvalidateAllForUser` spent and its claim is refused with the generic `ErrInvalidResetToken`. When
each holds its first lock, InnoDB ends one of them with error 1213: a request victim is replayed by
`WithTransactionAndRetry` (the end state is that of the confirm first), a confirm victim (plain
`WithTransaction`, no retry) returns the error with nothing written (the end state is that of the
request first). Neither ordering commits half a unit. Putting the confirm under the retry rule is a
follow-up.

### Password-reset confirm

- **Rows locked:** the `password_reset_tokens` row (the conditional claim `MarkUsed`,
  `internal/service/auth_service.go:1367`), the owner's `users` row (`FOR UPDATE`,
  `internal/service/auth_service.go:1325`, read at `internal/service/auth_service.go:1384`, written
  at `internal/service/auth_service.go:1400`), and every `refresh_tokens` row of the user (revoked,
  `internal/service/auth_service.go:1412`).
- **Order:** password_reset_tokens → users → refresh_tokens, after a plain lookup of the token by
  hash (`internal/service/auth_service.go:1349`). This inverts the reset request's order; the
  outcome is described under that flow.
- **Transaction:** `WithTransaction` (`internal/service/auth_service.go:1312`).
- **Retry:** none today; a 1213 against a concurrent reset request returns the error with nothing
  written. Target: the #183 retry rule.
- **Status:** implemented

### API-key create

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  `internal/service/apikey_service.go:94`, read by `lockLiveCredentialOwner`), then the inserted
  `api_keys` row (`internal/service/apikey_service.go:105`, through `apiKeyRepo.WithTx(tx)`), whose
  foreign-key check (`fk_api_keys_user`, `migrations/20250806120000_initial_schema.up.sql:135`)
  takes its shared lock on an owner row this transaction already holds.
- **Order:** `apiKeyService.Generate` (`internal/service/apikey_service.go:64`): `FOR UPDATE` owner
  read by id → insert. Lock order users → api_keys. A missing, erased (soft-deleted) or inactive
  locked row is refused with an error matching `ErrAccountUnavailable` (`POST /api-keys` answers
  401), no row and no key. An erasure or deactivation that commits first refuses the create; one
  that commits after it hard-deletes the key (erasure) or leaves it unusable (`ValidateAPIKey`
  refuses an inactive owner).
- **Transaction:** one `WithTransactionAndRetry` root per create
  (`internal/service/apikey_service.go:86`) on `context.Background()`. A service built without the
  user repository or the transaction manager returns `ErrAPIKeyIssuanceUnavailable` and writes
  nothing.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `apiKeyCreateMaxRetries` 3
  (`internal/service/apikey_service.go:27`) replays the whole root on a deadlock, lock wait timeout
  or SQLite busy/locked.
- **Status:** implemented

### RBAC user-role and role writes

- **Rows locked:** a governed user write (`POST /users`, every `PUT /users/{id}` on another
  account or that writes `role`, `role_id` or `is_active`, activate, deactivate and
  `DELETE /users/{id}`; `governedWrite`, `internal/service/user_governed_writes.go:221`, and
  `CreateAs`, `internal/service/user_governed_writes.go:91`) locks the `users` rows of the active
  admins (`LockActiveAdmins`, `internal/repository/user_repository.go:284`: a plain read discovers
  the ids, then a locking read by primary key in id order, never through `idx_users_role`), the
  `users` rows of the actor and the target (`LockLiveByIDs`,
  `internal/repository/user_repository.go:322`, one locking read by primary key in id order) and
  the `roles` rows of the actor's, the target's and the requested custom role with their
  `role_permissions` rows (`RoleRepository.LockByIDs`,
  `internal/repository/role_repository.go:227`: the roles by primary key in id order, then their
  permissions, both locking reads, so a transaction that waited reads the committed rows and not
  its snapshot). A role write (`internal/service/role_service.go:232`, `:274`, `:352` and `:418`
  in the same file: create, update, delete, clone) locks the actor's `users` row and then the
  `roles` rows of the actor's role and the role it touches; `ReplacePermissions` and `Delete`
  (`internal/repository/role_repository.go:140`, `:178` in the same file) lock the `roles` row
  again by primary key before they delete its `role_permissions` rows. The role delete also takes
  a locking read of the live users holding the role, by `role_id`
  (`LockLiveRoleHolders`, `internal/repository/user_repository.go:343`), before the roles and
  again after them (`internal/service/role_service.go:358`, `:382` in the same file).
- **Order:** admin set → users (id ascending) → roles (id ascending), with a role's
  `role_permissions` rows after its `roles` row. A role write starts at the actor's `users` row:
  users before roles. The actor is read again under its lock and every refusal (the lost
  permission, the escalation rule, the customer-login rule, the last-admin invariant) is decided
  after the locks, from the locked rows. Whether a customer login has a linked customer record
  (`HasLinkedCustomerRecord`, a plain read of `customers` by `user_id` after the locks) takes no
  lock, so the lock order is unchanged. The role delete is the one place where `users` rows are
  not taken in ascending id: its holder reads lock by `role_id`, after the actor's row and, the
  second time, after the roles; a deadlock that this causes is replayed.
- **Transaction:** one root transaction per write (`roleGovernance.run`,
  `internal/service/role_governance.go:116`) through the `WithTx` repositories; the audit row
  (`role_audit_log`) is inserted in it, so a failed insert rolls the change back. The erasure of
  `DELETE /users/{id}` runs inside it as its savepoint-bracketed unit
  (`internal/service/user_governed_writes.go:293`).
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `roleWriteMaxRetries` 3
  (`internal/service/role_governance.go:28`) replays the whole root on a deadlock, lock wait
  timeout or SQLite busy/locked.
- **Status:** implemented

## How the #175 failure modes are avoided

**Foreign-key-check lock inversion.** On InnoDB an insert or update of a child row whose foreign key
names a parent takes a shared lock on that parent row. A flow that locks a child first and then
writes through a foreign key to a parent another transaction already holds inverts the order even
when its own statements look ordered. The specified chains lock parents first: `refresh_tokens.user_id`
references `users`, so a session transaction locks the owner before it claims or inserts a token
(the rotation that claimed first deadlocked against a password change with error 1213, see
`internal/service/auth_service.go:778`); in the #182 chain the company is locked before the lead,
and the lead before the customer row that names the company. The #175 inversion runs through lead
updates: an update that locked the lead and then wrote its `company_id` took the foreign key
check's shared lock on the company, while a company delete held the company and waited on the
lead in `UnlinkLeads`. Since #182 the update locks the companies before the lead, so both wait on
the company row first.

Caveat: the #182 MySQL roots run on an `AutoMigrate` schema without foreign keys
(`DisableForeignKeyConstraintWhenMigrating`, `internal/service/automation_test_helpers_test.go:50`,
schema opened by `openAutomationMySQL`, `internal/service/automation_test_helpers_test.go:88`). They
prove the row-lock order, not the foreign key check's shared locks, which are untested on the real
`migrations/` schema.

**No `performance_schema.data_lock_waits` on MariaDB.** MariaDB 10.11 has no
`performance_schema.data_lock_waits` table (MDEV-31165), so a contention probe cannot ask the
server who waits on whom. The lock-order roots in
`internal/service/lead_company_lock_order_mysql_test.go` poll `information_schema.PROCESSLIST`
instead (`internal/service/lead_company_lock_order_mysql_test.go:39`) and count a wait when the
same statement on the same connection is still executing on two consecutive polls
(`lockWaitProbe`, `internal/service/lead_form_bulk_cancellation_test.go:783`). The probe behaves the
same on MySQL 8 and MariaDB 10.11.

## Flow to ticket map

| Flow | Ticket | Status |
|---|---|---|
| User-rooted erasure | C4 | partial |
| Customer-rooted erasure | C4 | partial |
| Lead-rooted erasure | C4 | partial |
| Lead conversion | #182 | implemented |
| Lead update | #182 | implemented |
| Company delete | #182 | implemented |
| Bulk status | #182, #242 | implemented |
| Login | C2 | implemented |
| Refresh rotation | C2 | implemented |
| Password-reset request | C3 | implemented |
| Password-reset confirm | C3 | implemented |
| API-key create | C3 | implemented |
| RBAC user-role and role writes | #348 | implemented |

C2–C4 are Paperclip ids with no GitHub issue yet; C2–C4 follow the family order of the
#183 comment of 14 Nov 2026 (C2 session credentials, C3 reset and API-key issuance, C4 erasure
graph discovery). The RBAC lock order of #259 is implemented by #348.
