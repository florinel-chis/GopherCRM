# Concurrency: lock order and retry rule

One place for the row-lock order and the retry rule of every write that locks rows of more than
one table: the erasures, lead conversion and update, company delete, bulk status, the session and
credential flows, and RBAC writes. It records what the code does at `a415cf1` and, separately,
the target that #183 (and #259 for RBAC) asks for. A flow whose order is only a target is marked
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
  ascending, then customers, then deals. Written down at `internal/service/lead_service.go:174`.
- `users → refresh_tokens` (implemented for every session transaction): the owner's `users` row
  first, its `refresh_tokens` rows second. Refresh rotation, password change, logout and
  password-reset confirm follow it; the reset confirm claims its `password_reset_tokens` row before
  the owner (see that flow).
- `admin set → users (id ascending) → roles (id ascending)` is the #259 target for RBAC writes (R6, R7), not implemented.

**Cross-chain order (undecided).** No source specifies the order for flows spanning several
chains: an erasure reaches users, customers and leads from whichever root it starts at, and the
session and credential flows touch users while lead conversion touches leads and customers. `C4`
(#183) owns that decision. Until then the current erasure order depends on the root: the
user-rooted erasure locks the user before its customers and leads, the customer-rooted erasure
locks the customer before its leads and its login, and the lead-rooted erasure locks the lead
before the customer it was converted into. This document pins no combined sequence across the
chains.

## Retry rule

The rule is `WithTransactionAndRetry` in `internal/utils/transaction.go:176`:

- Each attempt is a fresh root transaction (`runRoot`, `internal/utils/transaction.go:244`). Only a
  root transaction is replayed, and only after a clean rollback: a failed `COMMIT` or `ROLLBACK`
  is ambiguous and is returned as it is (`internal/utils/transaction.go:196-201`).
- An attempt is replayed only for a retryable error: MySQL error 1213 (deadlock) or 1205 (lock wait
  timeout), SQLite busy or locked (`isRetryableError`, `internal/utils/transaction.go:473`).
- Replays stop after `maxRetries` replays and as soon as the context is done; a negative
  `maxRetries` is refused before the callback runs.
- Inside an active transaction the callback runs once under a savepoint, and a retryable failure is
  returned tagged `ErrNestedRetryDeferred` (`internal/utils/transaction.go:60`, `:181-187` in the
  same file): a deadlock has already destroyed the enclosing transaction, so only the enclosing
  root may replay the whole unit.

The rule is current behaviour only for lead conversion (`internal/service/lead_service.go:1035`).
For every other flow below it is the #183 target, a bounded whole-root retry, not today's
behaviour. The lead writes also restart the whole root on `errLeadCompanyMoved`, a separate bounded
loop (`runLeadCompanyAttempts`) described in their entries.

## Flows

### User-rooted erasure

- **Rows locked:** the `users` row (locking read of its role,
  `internal/repository/erasure_cascade.go:418`, then the scrub), its `api_keys`,
  `refresh_tokens` and `password_reset_tokens` rows (deleted by `purgeCredentials`,
  `internal/repository/erasure.go:436`), and for a customer-role login every linked `customers`
  row and the `leads` converted into them; then the text of their deals and quotes
  (`internal/repository/erasure_cascade.go:296`).
- **Order:** users → api_keys, refresh_tokens, password_reset_tokens → customers (id order,
  `internal/repository/erasure_cascade.go:439-447`) → leads converted into each customer (id
  order) → deals and quotes text. A staff login stops after its credentials.
- **Transaction:** `userRepository.Delete` → `eraseGraph`
  (`internal/repository/user_repository.go:164`) → `runErasureUnit`
  (`internal/repository/erasure.go:335`): a `db.Transaction`, or a checked SAVEPOINT inside the
  caller's transaction.
- **Retry:** none today; a deadlock or lock wait timeout reaches the caller. Target (C4): the
  #183 retry rule on the whole erasure root.
- **Status:** partial

The user is locked before its customers and leads, the opposite of the `companies → leads →
customers → deals` direction a lead-rooted erasure follows. The credential purge takes
`password_reset_tokens` after the user, while password-reset confirm takes them in the opposite
order, so the two can deadlock (an error and a rollback, never a partial commit).

### Customer-rooted erasure

- **Rows locked:** the `customers` row (plain read, the scrub takes the row lock straight away,
  `internal/repository/erasure_cascade.go:373`), the `leads` converted into it, then its
  customer-role login (`users`) with that login's credentials; then the deals and quotes text.
- **Order:** customers → leads (id order, `internal/repository/erasure_cascade.go:390`) → users
  (`internal/repository/erasure_cascade.go:403`) → deals and quotes text.
- **Transaction:** `customerRepository.Delete` (`internal/repository/customer_repository.go:158`)
  → `eraseGraph` → `runErasureUnit`, as for the user-rooted erasure.
- **Retry:** none today. Target (C4): the #183 retry rule.
- **Status:** partial

### Lead-rooted erasure

- **Rows locked:** the automation runs of the lead (cancelled first by the plan's `BeforeScrub`,
  `cancelLeadAutomation`, `internal/repository/erasure_cascade.go:76`), the `leads` row (scrub,
  then a locking re-read of `customer_id`, `internal/repository/erasure_cascade.go:349`), then the
  customer it was converted into with that customer's leads and login.
- **Order:** a deliberately non-locking first read of the lead
  (`internal/repository/erasure_cascade.go:320-326`) → automation runs → leads → customers →
  users → deals and quotes text.
- **Transaction:** `leadRepository.Delete` (admins, `internal/repository/lead_repository.go:141`)
  or `leadRepository.DeleteUnconverted` (non-admins, refused before any write when the lead was
  converted, `internal/repository/lead_repository.go:153`) → `eraseGraph` → `runErasureUnit`.
- **Retry:** none today. Target (C4): the #183 retry rule.
- **Status:** partial

All three erasures are one transaction each, but they share no order with lead conversion or the
session flows, and their order depends on the root. That is why they are `partial`.

### Lead conversion

- **Rows locked:** the `companies` rows of the lead's stored company and of a caller-supplied
  `company_id` (`lockLeadCompanies`, `internal/service/lead_service.go:259`), then the `leads` row
  (`lockedLeadRead`, `internal/service/lead_service.go:1094`); the new `customers` row is inserted
  (`internal/service/lead_service.go:1157`). No deal is touched.
- **Order:** a plain read of the lead outside the transaction
  (`internal/service/lead_service.go:990`); in each attempt a plain read of `company_id`
  (`internal/service/lead_service.go:1073`) → companies, ascending id
  (`internal/service/lead_service.go:272`) → the locked lead → customers. A locked lead that names
  another company ends the attempt with `errLeadCompanyMoved`; at most `leadCompanyMaxRestarts`
  restarts (`internal/service/lead_service.go:198`).
- **Transaction:** a transaction root (an enclosing transaction is refused with
  `utils.ErrTransactionRootRequired`); `runLeadCompanyAttempts`
  (`internal/service/lead_service.go:1034`) around one root transaction per attempt.
- **Retry:** `WithTransactionAndRetry` with `maxRetries` 3 (`internal/service/lead_service.go:1035`,
  `:1043` in the same file) replays the attempt's root on a deadlock, lock wait timeout or SQLite
  busy/locked; around it `runLeadCompanyAttempts` (`internal/service/lead_service.go:318`) restarts
  the whole root at most `leadCompanyMaxRestarts` (3) times, on `errLeadCompanyMoved` only and only
  after a clean rollback (`shouldRestartLeadCompanyAttempt`, `internal/service/lead_service.go:211`),
  then returns `ErrLeadCompanyContended`.
- **Status:** implemented

### Lead update

- **Rows locked:** the `companies` rows of the stored and of the sent `company_id`, the `leads`
  row (`lockedLeadRead`) and, when `owner_id` changes, the new owner's `users` row (`lockedUserRead`,
  `internal/service/lead_service.go:147`).
- **Order:** in each attempt a plain read of `company_id` (`internal/service/lead_service.go:504`)
  → companies, ascending id (`internal/service/lead_service.go:515`) → the locked lead and the
  locked owner (`internal/service/lead_service.go:536`). Same chain and the same
  `errLeadCompanyMoved` check as the conversion, bounded by `leadCompanyMaxRestarts`.
- **Transaction:** `WithTransaction` per attempt (`internal/service/lead_service.go:491`) inside
  `runLeadCompanyAttempts` (`internal/service/lead_service.go:490`).
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

- **Rows locked:** none by a locking read. The conditional `UpdateFields`
  (`internal/repository/user_repository.go:93`) of the failure bookkeeping or its reset and
  `UpdateLastLogin` lock the `users` row for their own statement only; the `refresh_tokens` row is
  inserted afterwards (`issueRefreshToken`, `internal/service/auth_service.go:458`).
- **Order:** plain `GetByEmail` (`internal/service/auth_service.go:173`) → bcrypt →
  `UpdateFields` (`internal/service/auth_service.go:224` or `:238` in the same file) →
  `UpdateLastLogin` (`internal/service/auth_service.go:254`) → refresh token insert
  (`internal/service/auth_service.go:406-407`). Time order follows users → refresh_tokens, but no
  lock is held across statements.
- **Transaction:** no transaction; each statement commits on its own.
- **Retry:** none. Target (C2): one issuing transaction that re-verifies the hash and the active
  state under the owner lock, users → refresh_tokens, under the #183 retry rule.
- **Status:** not implemented

### Refresh rotation

- **Rows locked:** the owner's `users` row (`FOR UPDATE`, `internal/service/auth_service.go:541`,
  read at `internal/service/auth_service.go:615`), the presented `refresh_tokens` row (the
  conditional claim `ClaimForRotation`, `internal/service/auth_service.go:631`), and the inserted
  successor (`internal/service/auth_service.go:648`).
- **Order:** a plain lookup of the token by hash (`internal/service/auth_service.go:589`) →
  users → refresh_tokens (the claim, then the successor). The owner is locked before the claim
  (`internal/service/auth_service.go:602`).
- **Transaction:** `WithTransaction` (`internal/service/auth_service.go:529`).
- **Retry:** none today. Target (C2): the #183 retry rule.
- **Status:** implemented

Logout (`internal/service/auth_service.go:699`) and password change
(`internal/service/auth_service.go:837`) follow the same users → refresh_tokens order in one
`WithTransaction` each.

### Password-reset request

- **Rows locked:** none by a locking read; no owner lock. The user is read plainly by
  `GetByEmail` (`internal/service/auth_service.go:961`); the user's outstanding
  `password_reset_tokens` are spent by `InvalidateAllForUser`
  (`internal/service/auth_service.go:976`) and a new one is inserted
  (`internal/service/auth_service.go:992`); the mail goes out last.
- **Order:** users (plain read) → password_reset_tokens (update, then insert), each statement on
  its own.
- **Transaction:** no transaction.
- **Retry:** none. Target (C3): one transaction with the owner lock first, under the #183 retry
  rule.
- **Status:** not implemented

### Password-reset confirm

- **Rows locked:** the `password_reset_tokens` row (the conditional claim `MarkUsed`,
  `internal/service/auth_service.go:1103`), the owner's `users` row (`FOR UPDATE`,
  `internal/service/auth_service.go:1061`, read at `internal/service/auth_service.go:1120`, written
  at `internal/service/auth_service.go:1136`), and every `refresh_tokens` row of the user (revoked,
  `internal/service/auth_service.go:1148`).
- **Order:** password_reset_tokens → users → refresh_tokens, after a plain lookup of the token by
  hash (`internal/service/auth_service.go:1085`).
- **Transaction:** `WithTransaction` (`internal/service/auth_service.go:1048`).
- **Retry:** none today. Target (C3): the #183 retry rule.
- **Status:** implemented

### API-key create

- **Rows locked:** none; one `api_keys` insert.
- **Order:** `apiKeyService.Generate` (`internal/service/apikey_service.go:24`) runs one `Create`
  (`internal/service/apikey_service.go:42`); the owner's `users` row is neither read nor locked.
- **Transaction:** no transaction.
- **Retry:** none. Target (C3): one transaction that locks the owner first, so an erasure in
  flight cannot miss the new key, under the #183 retry rule.
- **Status:** not implemented

### RBAC user-role and role writes

- **Rows locked:** none by a locking read. `UserService.Update`
  (`internal/service/user_service.go:55`) reads the user plainly and writes the role with one
  conditional `UpdateFields` (`internal/service/user_service.go:107`). There is no `roles` table:
  a role is a string column of `users` (`internal/models/user.go:14`).
- **Order:** users only, one statement. The #259 target is admin set → users (id ascending) →
  roles (id ascending).
- **Transaction:** no transaction today.
- **Retry:** none. Target (R6, R7): one root transaction under the #183 retry rule.
- **Status:** not implemented

## How the #175 failure modes are avoided

**Foreign-key-check lock inversion.** On InnoDB an insert or update of a child row whose foreign key
names a parent takes a shared lock on that parent row. A flow that locks a child first and then
writes through a foreign key to a parent another transaction already holds inverts the order even
when its own statements look ordered. The specified chains lock parents first: `refresh_tokens.user_id`
references `users`, so a session transaction locks the owner before it claims or inserts a token
(the rotation that claimed first deadlocked against a password change with error 1213, see
`internal/service/auth_service.go:602`); in the #182 chain the company is locked before the lead,
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
| Login | C2 | not implemented |
| Refresh rotation | C2 | implemented |
| Password-reset request | C3 | not implemented |
| Password-reset confirm | C3 | implemented |
| API-key create | C3 | not implemented |
| RBAC user-role and role writes | R6, R7 | not implemented |

C2–C4 and R6–R7 are Paperclip ids with no GitHub issue yet; C2–C4 follow the family order of the
#183 comment of 14 Nov 2026 (C2 session credentials, C3 reset and API-key issuance, C4 erasure
graph discovery), and R6–R7 are the #259 RBAC lock tickets.
