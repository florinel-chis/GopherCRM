# Concurrency: lock order and retry rule

One place for the row-lock order and the retry rule of every write that locks rows of more than
one table: the erasures, lead conversion and update, company delete, bulk status, the session and
credential flows, and RBAC writes. It records what the code does at `a415cf1` (with Login updated
for #336, the password-reset request and API-key create for #342, and the lead-rooted erasure
against lead conversion for #337, the RBAC writes for #348, the one erasure lock order with
its retry for #385, and the erasure's stamp of quote response links for #395) and, separately, the target that #183 asks for. A flow whose order is only a target is marked
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
the chains above. The same holds for the updated events (#408): the lead update, the ticket update
and the lead and ticket bulk status batches insert their rows after the record write, inside the
transaction and under the row locks they already took, behind the same plain read of the endpoints
(once per batch). The customer update is the one path that gained a transaction: wired with
webhooks, `CustomerService.Update` (`updateWithWebhooks`, `internal/service/customer_service.go`)
takes exactly one row lock, the `customers` row it writes (a locking read by primary key, then the
`UPDATE`), and inserts its rows after it. Its duplicate-email and company checks are plain reads
made before the transaction, and it never calls `CompanyRepository.LockLive` or any other locking
read on `companies` or `leads`, so it sits at the `customers` step of
`companies → leads → customers → deals` and cannot invert the order against a company delete
(`UnlinkCustomers`).

**Quote response links (#382), a self-contained order.** Three flows take `quotes →
quote_response_tokens`, each starting with the locking read of the quote by primary key
(`QuoteRepository.GetByIDForUpdate`) and writing token rows only after it. The email mint
(`internal/service/quote_email.go`, `mintResponseLink`) re-checks that the quote is still sent and
that no history row was written since the marker read before the send, stamps the quote's unused
token rows and inserts the new one, in one short transaction that ends before the mail goes out.
A staff status change (`internal/service/quote_service.go`, `ChangeStatus`) that leaves `sent`
stamps the unused token rows after its status claim. The client response
(`internal/service/quote_response.go`, `RespondPublic`) finds the token with a plain read outside
the transaction, then claims it with one conditional `UPDATE` by primary key
(`internal/repository/quote_response_token_repository.go`, `Claim`: unused, unexpired, quote live
and sent, all in the statement), claims the status (`WHERE status = 'sent'`) and inserts the
history row. Two answers with one token serialise on the quote lock (on the single connection on
SQLite) and the second claims nothing. Transactions of two different quotes share no quote lock, so
the token writes must not meet in the table either: `InvalidateForQuote` reads the ids of the
quote's unused rows with a plain `SELECT` (complete, because every writer of those rows holds the
quote lock before the first read of its transaction) and stamps them by primary key, with no
`UPDATE` at all when there is none. No statement locks through
`idx_quote_response_tokens_quote_id`, so no gap lock is held when the mint inserts; an `UPDATE`
filtered on `quote_id` took one under REPEATABLE READ on MySQL and MariaDB and two first emails of
two quotes deadlocked (error 1213).

The erasure of a quote's customer is the fourth writer of `quote_response_tokens` (#395), and it
takes the same order. Its text scrub updates the quotes it reached by primary key
(`scrubReachedQuotes`, `internal/repository/business_text_erasure.go`), which is its lock on each
of those `quotes` rows; only after that does `invalidateErasedCustomerQuoteLinks` stamp `used_at`
(`decision` stays NULL) on the unused token rows of the quotes addressed to an erased customer,
with the statements of `InvalidateForQuote`: a plain `SELECT` of the ids, then an `UPDATE` by
primary key that still requires `used_at IS NULL`, and no `UPDATE` when there is no id. It issues
no locking read and no write filtered on `quote_id`, so it holds no gap of
`idx_quote_response_tokens_quote_id` either. A quote reached only through a scrubbed deal and
addressed to another customer keeps its tokens. For the three quote flows the plain id read is
complete, as said above. For the erasure it is complete only for the tokens committed before the
snapshot of its transaction, which is fixed before the quote locks (after the `users` lock; in
`DELETE /users/{id}` by the governed write's first read): a client answer committed after the
snapshot is read as unused and left alone by the `used_at IS NULL` predicate of the stamp, and a
token minted after the snapshot by a quote email in flight is not seen and not stamped. That
last case is open (`docs/ROADMAP.md`, #395); closing it needs a re-check of the customer under
the quote lock in `mintResponseLink`.

The erasure reaches `quotes` and then `quote_response_tokens` while it holds its `leads`,
`customers`, `users` and credential locks and the `deals` rows it scrubbed, so there is a lock
edge from the erasure order into this one. It closes no cycle: the three quote flows read
`users`, `customers` and `deals` with plain reads only and take no lock on `users`, `customers`,
`leads` or `deals` after the quote lock, so nothing waits in the opposite direction. An erasure
and a client answer on one token queue on the `quotes` row: the answer commits and stands, or
the erasure commits and the answer is refused with the generic rejection.

**Erasure order (#385).** One order spans the chains for every erasure, whichever row it starts
from: `automation runs → leads → customers → users → refresh_tokens → password_reset_tokens →
api_keys`, ids ascending inside each table. The automation runs of every discovered lead are
cancelled first (writes only, #337); every other step is a locking read by primary key only
(`lockGraph`, `internal/repository/erasure_cascade.go:821`, and `lockCredentials`, `:934` in the
same file). The lead before the customer is the direction of the #182 chain, and the user before
its credential rows is the direction of the session chain. Two flows still lock against this
order. The governed user write of `DELETE /users/{id}` (#348) holds the target's `users` row,
with the admin set and the actor, before its nested erasure locks leads and customers; against a
customer- or lead-rooted erasure of the same person that is a possible deadlock, and whichever
side is the victim is replayed. The password-reset confirm claims its `password_reset_tokens` row
before the owner's `users` row and is replayed when it is the victim (`resetConfirmMaxRetries`):
against an erasure it commits first or is refused with the generic `ErrInvalidResetToken`, its
replay finding the token gone or the owner erased, while an erasure that is the victim is
replayed. A governed delete that is replayed after the other erasure committed finds the account
already erased and answers not found (404), as a delete of an erased user does. After the
credential rows every erasure writes the text of the linked `deals` and `quotes` by primary key
and then, last, stamps the unused `quote_response_tokens` rows of the erased customers' quotes by
primary key (#395, see the quote response links above).

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

The rule is current behaviour only for lead conversion (`internal/service/lead_service.go:1092`),
login (`internal/service/auth_service.go:283`), the password-reset request
(`internal/service/auth_service.go:1220`), the password-reset confirm
(`internal/service/auth_service.go:1327`), API-key create
(`internal/service/apikey_service.go:86`), the RBAC user-role and role writes
(`internal/service/role_governance.go:161`) and, since #385, the three erasures
(`runErasureUnit`, `internal/repository/erasure.go:431`).
For company delete, bulk status and refresh rotation it is the #183
target, a bounded whole-root retry, not today's behaviour. The lead writes also restart the whole
root on `errLeadCompanyMoved`, a separate bounded loop (`runLeadCompanyAttempts`) described in
their entries; the erasures restart theirs on `ErrErasureGraphChanged` (`eraseGraph`), and the
governed user write does the same around its nested erasure.

## Flows

### User-rooted erasure

- **Rows locked:** for a customer-role login: the automation runs of the leads converted into its
  customers (cancelled, `cancelAutomationInTables`, `internal/repository/erasure.go:608`, called
  at `internal/repository/erasure_cascade.go:739`), those `leads` rows
  (`internal/repository/erasure_cascade.go:831`), every `customers` row linked to the login
  (`:853` in the same file), the `users` row (`:874`, the role is read under this lock) and the
  user's `refresh_tokens`, `password_reset_tokens` and `api_keys` rows (`lockCredentials`,
  `internal/repository/erasure_cascade.go:934`). All of them are locking reads by primary key. A
  staff login is erased alone: its `users` row and its credential rows. The scrub follows the
  locks: `eraseRecord` (`internal/repository/erasure.go:146`) with a locking read of the address
  (`readErasedEmail`, `internal/repository/erasure.go:274`), the credential delete by `user_id`
  (`purgeCredentials`, `internal/repository/erasure.go:536`, which stays the authority for what
  is removed), then the text of the linked deals and quotes
  (`internal/repository/erasure_cascade.go:797`), whose `deals`, `quotes` and `quote_lines` rows
  are written by primary key or by `quote_id`, and last the unused `quote_response_tokens` rows of
  the quotes addressed to an erased customer, stamped by primary key after those `quotes` rows
  (#395).
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, after a discovery of the
  graph with plain reads (`discoverErasureGraph`, `internal/repository/erasure_cascade.go:497`);
  after the credential rows, deals and quotes text, then `quote_response_tokens` (#395).
  The credential ids are read with a plain `SELECT … WHERE user_id IN (…)` after the `users`
  lock and then locked by primary key. No locking read filters on `leads.customer_id`,
  `customers.user_id`, `deleted_at` or a credential table's `user_id`: such a read locks an index
  range and its gaps. Right after the `users` lock the graph is read again
  (`internal/repository/erasure_cascade.go:756`): forward links (`customer_id`, `user_id`,
  `role`) from the locked rows, reverse links (the leads of a customer, the customers of a
  login) by plain reads; a graph that differs from the discovery rolls the unit back with
  `ErrErasureGraphChanged`. Snapshot rule: under REPEATABLE READ the first plain read of a
  transaction fixes its snapshot, so the discovery and the schema probes of the cancel
  (`probeAutomationTables`, `internal/repository/erasure.go:581`) run on the root handle before
  the transaction begins, and no plain read of any kind runs on the transaction before the
  `users` lock, the last graph lock: only the writes of the cancel and `FOR UPDATE` reads by
  primary key. A migrator probe counts as a plain read: on MySQL 8 a select on
  `information_schema` fixes the snapshot (on MariaDB 10.11 it does not), and the reverse
  re-read would then miss a customer linked to the login before the locks were granted.
- **Transaction:** `userRepository.Delete` (`internal/repository/user_repository.go:191`) →
  `eraseGraph` (`internal/repository/erasure_cascade.go:665`) → `runErasureUnit`
  (`internal/repository/erasure.go:429`): a root transaction of its own, or a checked SAVEPOINT
  inside the caller's transaction. On `DELETE /users/{id}` the caller is the governed user write
  of #348 (see the RBAC flow), which holds the admin set, the actor and the target before the
  erasure starts; the discovery then answers from that transaction's snapshot.
- **Retry:** `WithTransactionAndRetry` with `erasureMaxRetries` 3
  (`internal/repository/erasure.go:389`) replays the transaction on a deadlock, lock wait timeout
  or SQLite busy/locked, on the same discovery (the re-read under the locks catches a stale one).
  Around it `eraseGraph` starts over from a new discovery on `ErrErasureGraphChanged` only, and
  only after a clean rollback (`shouldRestartErasure`,
  `internal/repository/erasure_cascade.go:698`), at most `erasureGraphMaxRestarts` (3) times
  (`internal/repository/erasure.go:390`); then the error, which wraps the sentinel, is returned.
  Inside a caller's transaction the unit runs once and both kinds of error go to the caller: on
  `DELETE /users/{id}` the governed user write replays or restarts its whole root (see the RBAC
  flow).
- **Status:** implemented

The session and credential-issuing flows lock the user before its child rows, the erasure's own
order for those tables: against an erasure they either commit first and the purge removes what
they issued, or wait on the `users` row and are refused. Two inversions remain. The governed user
write holds the target's `users` row before its nested erasure locks leads and customers, so
against a customer- or lead-rooted erasure of the same person it can deadlock; the victim is
replayed, by `roleWriteMaxRetries` on the governed side and by `erasureMaxRetries` on the other.
The password-reset confirm takes `password_reset_tokens` before `users` and is replayed by
`resetConfirmMaxRetries`: when each side holds its first lock the victim is replayed, whichever
it is, and a replayed confirm that finds the token gone or the owner erased is refused with
`ErrInvalidResetToken` (a rollback and a fresh attempt, never a partial commit).
A governed delete that is replayed after the other erasure committed finds the account
already erased and answers not found (404), as a delete of an erased user does.

Known gaps of the re-check. Inside a caller's transaction the reverse re-read answers from the
caller's snapshot, so a customer linked to the login while a governed user erasure is in flight
can be missed. No shipped code path writes `customers.user_id` (customer create, customer update
and lead conversion never set it; it comes from a seed, an import or SQL), and such a write
takes no lock on the `users` row: a customer linked to the login after the erasure's reverse
re-read and before its commit is missed outside a caller's transaction too. The read of the
unlinked form submissions of an erased address (`scrubUnlinkedSubmissions`,
`internal/repository/erasure.go:326`) is a plain read, so a submission inserted concurrently can
survive.

### Customer-rooted erasure

- **Rows locked:** the automation runs of the leads converted into the customer (cancelled
  first), those `leads` rows, the `customers` row and, when its login is a customer-role account,
  every other `customers` row of that login with their leads, the login's `users` row and its
  credential rows: the statements of the user-rooted erasure, the graph being the same person. A
  staff account in `user_id` is locked (`internal/repository/erasure_cascade.go:874`, its role
  decides the link) and left alone. Each row's address is read by `readErasedEmail` during the
  scrub. The text scrub then writes the linked `deals` and `quotes`, and the unused
  `quote_response_tokens` rows of the customers' quotes are stamped by primary key after those
  `quotes` rows (#395).
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, with the discovery, the
  re-read after the `users` lock and the snapshot rule of the user-rooted erasure; after the
  credential rows, deals and quotes text, then `quote_response_tokens` (#395). The leads of
  the customer are found by the plain pluck by `customer_id`
  (`convertedLeadsOf`, `internal/repository/erasure_cascade.go:464`) in the discovery and again
  in the re-read, never by a locking read.
- **Transaction:** `customerRepository.Delete` (`internal/repository/customer_repository.go:170`)
  → `eraseGraph` → `runErasureUnit`, as for the user-rooted erasure.
- **Retry:** `WithTransactionAndRetry` with `erasureMaxRetries` 3 and the `ErrErasureGraphChanged`
  restart bounded by `erasureGraphMaxRestarts`, as for the user-rooted erasure.
- **Status:** implemented

### Lead-rooted erasure

- **Rows locked:** the automation runs of every lead of the graph (cancelled first, with the
  answers of the discovery's schema probes; the lead plan still names the cancel as its
  `BeforeScrub`, `cancelLeadAutomation`, `internal/repository/erasure_cascade.go:78`, which the
  unit clears after running it), the `leads` row (locking read by id,
  `internal/repository/erasure_cascade.go:831`, the first lock on the leads row in the unit; the
  non-admin refusal is decided on that locked row, and a `customer_id` that differs from the
  discovery ends the attempt), then the customer it was converted into
  (`internal/repository/erasure_cascade.go:853`) with that customer's other leads, its login and
  the login's credential rows. `readErasedEmail` reads each address during the scrub. The text
  scrub then writes the linked `deals` and `quotes`, and for a converted lead the unused
  `quote_response_tokens` rows of the customer's quotes are stamped by primary key after those
  `quotes` rows (#395).
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, with the discovery, the
  re-read after the `users` lock and the snapshot rule of the user-rooted erasure: automation
  runs (`cancelAutomationInTables`) → leads (locking read by id,
  `internal/repository/erasure_cascade.go:831`) → customers (locking read by id, `:853` in the
  same file) → users (locking read by id, `:874`) → the credential rows → deals and quotes text,
  then `quote_response_tokens` (#395).
  `DeleteUnconverted` discovers and locks the root lead only and never reads or locks a customer
  or a user. The converted-lead pluck (`WHERE customer_id = ?`,
  `internal/repository/erasure_cascade.go:466-467`) stays a plain read: a locking read there
  would take next-key locks on the `leads.customer_id` index and block unrelated conversions,
  and it is not needed, because a conversion always inserts a new customer,
  so no lead can be converted into a customer being erased. Every erased row is soft-deleted by
  primary key only (`softDeleteErased`, `internal/repository/erasure.go:234`: a locking read of
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
- **Transaction:** `leadRepository.Delete` (admins, `internal/repository/lead_repository.go:163`)
  or `leadRepository.DeleteUnconverted` (non-admins, refused with `ErrConvertedLeadNeedsAdmin` on
  the locked lead row before the lead is written, the cancel rolled back with the unit,
  `internal/repository/lead_repository.go:173`) → `eraseGraph` → `runErasureUnit`.
- **Retry:** `WithTransactionAndRetry` with `erasureMaxRetries` 3 and the `ErrErasureGraphChanged`
  restart bounded by `erasureGraphMaxRestarts`, as for the user-rooted erasure. A refusal is not
  retried.
- **Status:** implemented

Against lead conversion (companies → leads → customers) there is no cycle: both lock the lead row
by primary key before they touch the customer, and the erasure takes no company lock. Conversion
first: the erasure waits on the lead's locking read, then reads the committed `customer_id`; a
non-admin erasure is refused and changes nothing, and an admin erasure, whose discovery did not
list the new customer, rolls back with `ErrErasureGraphChanged`, discovers again and erases the
new customer too. Erasure first: the conversion waits on its `lockedLeadRead` and then finds the
lead gone (see Lead conversion). A conversion committed between the cancel and the lead lock is
seen the same way. Neither ordering ends in error 1213.

All three erasures take the lead before the customer, the direction lead conversion takes, and
the user before its credential rows, the direction of the session flows; two erasures of one
person queue on its first row whichever rows they were started from. What stays outside the
order is listed under the user-rooted erasure: the governed user write, the password-reset
confirm and the gaps of the re-check.

### Lead conversion

- **Rows locked:** the `companies` rows of the lead's stored company and of a caller-supplied
  `company_id` (`lockLeadCompanies`, `internal/service/lead_service.go:275`), then the `leads` row
  (`lockedLeadRead`, `internal/service/lead_service.go:1151`); the new `customers` row is inserted
  (`internal/service/lead_service.go:1214`). No deal is touched.
- **Order:** a plain read of the lead outside the transaction
  (`internal/service/lead_service.go:1047`); in each attempt a plain read of `company_id`
  (`internal/service/lead_service.go:1130`) → companies, ascending id
  (`internal/service/lead_service.go:288`) → the locked lead → customers. A locked lead that names
  another company ends the attempt with `errLeadCompanyMoved`; at most `leadCompanyMaxRestarts`
  restarts (`internal/service/lead_service.go:214`). A conversion queued behind a lead erasure
  finds the lead gone: its `lockedLeadRead` returns not found (`gorm.ErrRecordNotFound`, which
  `apperrors.IsNotFound` matches, so `POST /leads/{id}/convert` answers 404) and nothing is written
  (#337, see Lead-rooted erasure).
- **Transaction:** a transaction root (an enclosing transaction is refused with
  `utils.ErrTransactionRootRequired`); `runLeadCompanyAttempts`
  (`internal/service/lead_service.go:1091`) around one root transaction per attempt.
- **Retry:** `WithTransactionAndRetry` with `maxRetries` 3 (`internal/service/lead_service.go:1092`,
  `:1099` in the same file) replays the attempt's root on a deadlock, lock wait timeout or SQLite
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
  (`internal/service/bulk_status_service.go:383`) is a read before that `UPDATE` and is not
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
`WithTransactionAndRetry` (the end state is that of the confirm first), a confirm victim is
replayed by the same rule (`resetConfirmMaxRetries`) and refused with `ErrInvalidResetToken`, its
replay finding the token spent by the request (the end state is that of the request first).
Neither ordering commits half a unit, and neither party returns the 1213.

### Password-reset confirm

- **Rows locked:** the `password_reset_tokens` row (the conditional claim `MarkUsed`,
  `internal/service/auth_service.go:1382`), the owner's `users` row (`FOR UPDATE`,
  `internal/service/auth_service.go:1340`, read at `internal/service/auth_service.go:1399`, written
  at `internal/service/auth_service.go:1415`), and every `refresh_tokens` row of the user (revoked,
  `internal/service/auth_service.go:1427`).
- **Order:** password_reset_tokens → users → refresh_tokens, after a plain lookup of the token by
  hash (`internal/service/auth_service.go:1364`). This inverts the reset request's order; the
  outcome is described under that flow.
- **Transaction:** one `WithTransactionAndRetry` root per confirmation
  (`internal/service/auth_service.go:1327`) on `context.Background()`; every attempt redoes the
  lookup, the claim, the owner lock and the writes.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `resetConfirmMaxRetries` 3
  (`internal/service/auth_service.go:1275`) replays the whole root on a deadlock, lock wait timeout
  or SQLite busy/locked. A replay that finds the token spent or gone, or its owner erased, is
  refused with `ErrInvalidResetToken`. An exhausted bound returns the last error with nothing
  written: `POST /auth/password-reset/confirm` answers the generic 500 and the link is still
  valid. The worst case is four lock waits, one per attempt, with no timeout of its own, as for
  login and the reset request.
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
  admins (`LockActiveAdmins`, `internal/repository/user_repository.go:294`: a plain read discovers
  the ids, then a locking read by primary key in id order, never through `idx_users_role`), the
  `users` rows of the actor and the target (`LockLiveByIDs`,
  `internal/repository/user_repository.go:332`, one locking read by primary key in id order) and
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
  (`LockLiveRoleHolders`, `internal/repository/user_repository.go:353`), before the roles and
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
  `internal/service/role_governance.go:128`) through the `WithTx` repositories; the audit row
  (`role_audit_log`) is inserted in it, so a failed insert rolls the change back. The erasure of
  `DELETE /users/{id}` runs inside it as its savepoint-bracketed unit
  (`internal/service/user_governed_writes.go:293`), once and without a retry of its own. By then
  the write holds the target's `users` row, so the nested erasure takes leads and customers after
  users, the one inversion of the erasure order (#385); `governedWrite` is not reordered.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `roleWriteMaxRetries` 3
  (`internal/service/role_governance.go:28`, called at `:161` in the same file) replays the whole
  root on a deadlock, lock wait timeout or SQLite busy/locked, one raised inside the nested
  erasure included. Around it `roleGovernance.run` restarts the whole root when the nested
  erasure returns `ErrErasureGraphChanged`, only after a clean rollback
  (`shouldRestartGovernedWrite`, `internal/service/role_governance.go:146`) and at most
  `roleWriteGraphMaxRestarts` (3) times (`internal/service/role_governance.go:33`).
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
| User-rooted erasure | #385 | implemented |
| Customer-rooted erasure | #385 | implemented |
| Lead-rooted erasure | #385 | implemented |
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

C2 and C3 are Paperclip ids with no GitHub issue yet; C2–C4 follow the family order of the
#183 comment of 14 Nov 2026 (C2 session credentials, C3 reset and API-key issuance, C4 erasure
graph discovery). C4 is implemented by #385: one lock order for the three erasures, the re-check
of the graph under the locks and the bounded whole-root retry. The RBAC lock order of #259 is
implemented by #348.
