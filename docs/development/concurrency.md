# Concurrency: lock order and retry rule

> **For:** Developers changing a write that locks rows of more than one table. Part of the [documentation index](../README.md).

One place for the row-lock order and the retry rule of every write that locks rows of more than
one table: the erasures, lead conversion and update, company delete, bulk status, the session and
credential flows (identity-provider sign-out included), and RBAC writes, and of API-key revoke, a
single-row write that the credential flows race. It records what the code does on the default
branch, kept current by the documentation test, and, separately, the target that #183 asks for. A
flow whose order is only a target is marked `not implemented` or `partial`; nothing here describes
a target as current behaviour.

Each flow lists five fields:

- **Rows locked** — the rows the flow holds a lock on, and the statement that takes it.
- **Order** — the order those locks are taken in.
- **Transaction** — the transaction helper, or `no transaction`.
- **Retry** — what is replayed after a failure.
- **Status** — `implemented` (the flow follows its chain of the global order, or takes one row
  lock in one statement), `partial` (one
  transaction, but no order shared with the flows it races) or `not implemented` (no transaction or
  no lock today).

On MySQL/MariaDB a locking read is `SELECT … FOR UPDATE`; SQLite drops the clause and serialises
writers on its single connection, so every order below matters on MySQL 8 and MariaDB 10.11 only.
The repositories' shared helpers take both kinds of lock:
`internal/repository/locking.go` `LockingRead` (`FOR UPDATE`, on a fresh Session so the clause does not reach the next statement)
and `internal/repository/locking.go` `SharedLockingRead` (`FOR SHARE` on MySQL 8, `LOCK IN SHARE
MODE` on MariaDB, dropped on SQLite).
The documentation test `internal/config/concurrency_doc_test.go` keeps this document tied to the
code: a cited file or symbol that no longer exists, or a pinned fact the code no longer has, fails
it. QA still reviews each entry against the code paths it cites.

## Global lock order

Three chains are specified. A transaction that locks rows of one chain takes them left to right.

- `companies → leads → customers → deals` (#182, implemented): companies by primary key
  (`internal/repository/company_repository.go` `companyRepository.LockLive`), leads by id
  ascending, then customers, then deals. Written down in the comment at the top of
  `internal/service/lead_company_lock.go`.
- `users → refresh_tokens`, `users → password_reset_tokens` and `users → api_keys` (implemented
  for every session and credential-issuing transaction): the owner's `users` row first, by primary
  key, its child rows second. Login, refresh rotation, password change and logout follow
  `users → refresh_tokens`; the password-reset request follows `users → password_reset_tokens`
  and API-key create `users → api_keys` (#342). The password-reset confirm is the exception: it
  claims its `password_reset_tokens` row before the owner, `password_reset_tokens → users`, the
  inversion of the reset request's order (see both flows). A login email change (#554: the own
  profile and the admin edit, the ungoverned `UserService.Update`, the SCIM replace and patch)
  follows `users → password_reset_tokens` too: `UserRepository.UpdateFields` spends the user's
  unused reset tokens right after its `users` UPDATE, in the same unit.
  Every flow that ends all of an account's credentials (#526: the password change, the
  password-reset confirm, the governed deactivation and the SCIM deactivations) does it through
  `revokeAllCredentials` (`internal/service/credential_revocation.go`) after its own `users`
  write, in the order `users → refresh_tokens → api_keys`: `token_version` on the locked `users`
  row, then every `refresh_tokens` row, then every `api_keys` row of the account.
- `admin set → users (id ascending) → roles (id ascending)` (#259, implemented by #348) for every governed user write and, without the admin set, every role write (the admin set is taken by `internal/service/last_admin_guard.go` `lockAdminSet`, which calls `LockActiveAdmins`).
  The OIDC sign-in (#435) continues the chain: `admin set → users → roles → user_identities → refresh_tokens`.
  The SCIM user create and replace (#470) follow `admin set → users → roles → user_identities`; the
  SCIM user patch (#471) and a deactivating replace take the RBAC order and then revoke the
  account's refresh tokens and API keys last, which keeps `users → refresh_tokens → api_keys`.
  The governed deactivation (`POST /users/{id}/deactivate`, `PUT /users/{id}` with `is_active`
  false, #526) does the same after the RBAC order. The SCIM user delete (#504) takes the admin set
  and the target's `users` row, no `roles`, then revokes the refresh tokens and the API keys:
  `admin set → users → refresh_tokens → api_keys`, a prefix-compatible subsequence. The SCIM group
  patch (#506) follows `admin set → users → roles → scim_group_members`, then writes its
  `role_audit_log` rows; it reads the group's membership rows with a locking read right after the
  admin set (see the flow below).
  The admin set is the `users` rows of the active admins, locked by primary key in id order; a
  role's `role_permissions` rows come after its `roles` row (see the RBAC flow below).

**Outbound webhooks (#345), a self-contained order.** The endpoint delete
(`internal/repository/webhook_repository.go`, `DeleteEndpoint`) takes `webhook_endpoints →
webhook_deliveries`: a locking read of the endpoint by primary key, then the delete of its delivery
rows, then the endpoint. The delivery runtime (`internal/webhook/runtime.go`) never takes a
locking read: it discovers due rows, expired leases and prunable rows with plain `SELECT`s and
writes each row by primary key with a conditional `UPDATE` or `DELETE` that re-checks the
discovered state, so it never locks through `idx_webhook_deliveries_status_next` or
`idx_webhook_deliveries_updated_at`. A manual redelivery (#502, `RequeueDelivery`) is one more
such write: a conditional `UPDATE` by primary key fenced on `status IN ('succeeded','failed')`,
with no locking read, so it cannot overlap a claim (which needs `pending`), and it moves
`updated_at` forward, so a prune pass whose `DELETE` re-checks the finished state and the cutoff
leaves the requeued row alone. Its endpoint check is a plain read before the write, not part of the
predicate: an endpoint deactivated in between still gets the row requeued, and the runtime then
fails it with `endpoint_inactive`. A signing-secret rotation (#503, `RotateEndpointSecret`) is one
`UPDATE webhook_endpoints … WHERE id = ?` by primary key with no read before it, so concurrent
rotations serialise on the endpoint row and each keeps the other's secret as the previous one. The
runtime's clear of an expired previous secret (`ClearExpiredPreviousSecret`) is a conditional
`UPDATE` by primary key fenced on `previous_secret_expires_at <= now` in the statement, so a
rotation committed after the runtime read the endpoint is never cleared; it leaves `updated_at`
alone. Neither adds a lock edge. The endpoint pause (#569) adds two more conditional `UPDATE`s
of `webhook_endpoints` by primary key, outside any transaction and after no locking read:
`RecordEndpointFailure` (`consecutive_failures + 1` and `paused_until`, fenced on the
`consecutive_failures` the runtime read with the endpoint) and `RecordEndpointSuccess` (reset to 0
and NULL, fenced on the endpoint not being reset already); both leave `updated_at` alone. Discovery
leaves out paused endpoints with a plain subselect of `webhook_endpoints` inside its non-locking
`SELECT`. None of the three adds a lock edge. The outbox rows a create path writes are inserts into its own
transaction after the record insert, behind a plain read of the endpoints; they add no lock edge to
the chains above. The same holds for the updated events (#408): the lead update, the ticket update
and the lead and ticket bulk status batches insert their rows after the record write, inside the
transaction and under the row locks they already took, behind the same plain read of the endpoints
(once per batch). The customer update is the one path that gained a transaction, and it is
always one (#615): `CustomerService.Update` (`updateLocked`, `internal/service/customer_service.go`)
takes exactly one row lock, the `customers` row it writes (a locking read by primary key, then the
`UPDATE`), and inserts its rows after it. Its duplicate-email and company checks are plain reads
made before the transaction, and it never calls `CompanyRepository.LockLive` or any other locking
read on `companies` or `leads`, so it sits at the `customers` step of
`companies → leads → customers → deals` and cannot invert the order against a company delete
(`UnlinkCustomers`).

**Quote response links (#382), a self-contained order.** Four flows take `quotes →
quote_response_tokens`, each starting with the locking read of the quote by primary key
(`QuoteRepository.GetByIDForUpdate`) and writing token rows only after it. The email mint
(`internal/service/quote_email.go`, `mintResponseLink`) re-checks that the quote is still sent,
reads the quote's customer as live and checks that no history row was written since the marker
read before the send, then inserts the new token row, in one short transaction that ends before
the mail goes out; it stamps no earlier row. After the mailer reports success, the post-send step
(`invalidateEarlierResponseLinks`, same file) runs a second short transaction: the quote lock, then
`InvalidateOlderForQuote`, which stamps only the quote's unused rows with an id lower than the
token just sent, by primary key. A staff status change (`internal/service/quote_service.go`,
`ChangeStatus`) that leaves `sent` stamps the unused token rows after its status claim. The client
response (`internal/service/quote_response.go`, `RespondPublic`) finds the token with a plain read
outside the transaction (`FindUsableByHash`, which also requires a live customer of the quote),
then, under the quote lock, reads the quote's customer as live, claims the token with one
conditional `UPDATE` by primary key (`internal/repository/quote_response_token_repository.go`,
`Claim`: unused, unexpired, quote live and sent, all in the statement), claims the status
(`WHERE status = 'sent'`) and inserts the history row. The customer reads of the mint and of the
answer are plain reads and the first consistent read after the quote lock, so on MySQL and
MariaDB their snapshot is taken after the lock was granted, after an erasure that held the quote
has committed; no plain read precedes the lock in either transaction. The claim reads no
`customers` row: a subquery of an `UPDATE` takes shared locks on the rows it reads under
REPEATABLE READ, and under the quote lock that would wait on, and deadlock with, an erasure that
holds the customer row and waits for the quote. Two answers with one token serialise on the quote lock (on the single connection on
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
addressed to another customer keeps its tokens. For the four quote flows the plain id read is
complete, as said above. For the erasure it is complete only for the tokens committed before the
snapshot of its transaction, which is fixed before the quote locks (after the `users` lock; in
`DELETE /users/{id}` by the governed write's first read): a client answer committed after the
snapshot is read as unused and left alone by the `used_at IS NULL` predicate of the stamp, and a
token minted after the snapshot by a quote email in flight is not seen and not stamped. Such a
token is dead all the same: the mint locked the quote before the erasure did, so the erasure's
customer update was not committed when the mint read the customer, but the read predicate of
`FindUsableByHash` and the customer read of `RespondPublic` refuse it once the erasure commits.
A mint that locks the quote after the erasure committed reads the customer as gone and stores
nothing.

The erasure reaches `quotes` and then `quote_response_tokens` while it holds its `leads`,
`customers`, `users` and credential locks and the `deals` rows it scrubbed, so there is a lock
edge from the erasure order into this one. It closes no cycle: the three quote flows read
`users`, `customers` and `deals` with plain reads only and take no lock on `users`, `customers`,
`leads` or `deals` after the quote lock, so nothing waits in the opposite direction. An erasure
and a client answer on one token queue on the `quotes` row: the answer commits and stands, or
the erasure commits and the answer is refused with the generic rejection.

**Quote webhook events (#518).** With `WithQuoteWebhooks` every quote write emits its webhook
event as the last step of its existing transaction, after the quote lock: `Create` after the
draft history row, `Update`, `AddLine`, `UpdateLine` and `DeleteLine` after the locking read of
`lockDraft` and their writes, `ChangeStatus` and `RespondPublic` after their history row, and
`Delete` (now one transaction when wired: `QuoteRepository.GetByIDForUpdate`, the draft-only
check on that row, the soft delete) after the soft delete. The emit is a plain read of the active
`webhook_endpoints` subscribed to the type and one `INSERT` into `webhook_deliveries`; neither table
is locked and no other row is read, so the quote order above is unchanged. In `RespondPublic` the
emit follows the history row, so the plain read of the quote's customer stays the first consistent
read after the quote lock.

**Erasure order (#385).** One order spans the chains for every erasure, whichever row it starts
from: `automation runs → leads → customers → users → refresh_tokens → password_reset_tokens →
api_keys`, ids ascending inside each table. The automation runs of every discovered lead are
cancelled first (writes only, #337); every other step is a locking read by primary key only
(`lockGraph` and `lockCredentials`:
`internal/repository/erasure_cascade.go` `erasureCascade.lockGraph` and `erasureCascade.lockCredentials`). The lead before the customer is the direction of the #182 chain, and the user before
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

The rule is `WithTransactionAndRetry` in
`internal/utils/transaction.go` `TransactionManager.WithTransactionAndRetry`:

- Each attempt is a fresh root transaction (`internal/utils/transaction.go` `TransactionManager.runRoot`). Only a
  root transaction is replayed, and only after a clean rollback: a failed `COMMIT` or `ROLLBACK`
  is ambiguous and is returned as it is (the `replayable` check of
  `TransactionManager.WithTransactionAndRetry`).
- An attempt is replayed only for a retryable error, classified from the driver's typed error and
  never from its message text (`RetryReasonOf` in `internal/utils/retryable.go`): MySQL/MariaDB
  error 1213 (deadlock), 1205 (lock wait timeout) or MariaDB 1020 (record changed since it was
  read, raised under `innodb_snapshot_isolation`; the server rolls the transaction back as on a
  deadlock), and SQLite busy or locked (primary result code 5 or 6, extended codes included).
- Replays stop after `maxRetries` replays and as soon as the context is done; a negative
  `maxRetries` is refused before the callback runs.
- Inside an active transaction the callback runs once under a savepoint, and a retryable failure is
  returned tagged `ErrNestedRetryDeferred` (`internal/utils/transaction.go`, returned by
  `TransactionManager.WithTransactionAndRetry` in the same file): a deadlock has already destroyed the enclosing transaction, so only the enclosing
  root may replay the whole unit.

The rule is current behaviour only for lead conversion
(`internal/service/lead_conversion.go` `leadService.ConvertToCustomer`), login
(`internal/service/auth_login.go` `authService.authenticate`), the password-reset request
(`internal/service/auth_password_reset.go` `authService.IssuePasswordReset`), the password-reset
confirm (`internal/service/auth_password_reset.go` `authService.CompletePasswordReset`), API-key
create (`internal/service/apikey_service.go` `apiKeyService.Generate`), the RBAC user-role and role
writes (`internal/service/role_governance.go` `roleGovernance.runOnce`) and, since #385, the three
erasures (`internal/repository/erasure.go` `runErasureUnit`).
For company delete, bulk status and refresh rotation it is the #183
target, a bounded whole-root retry, not today's behaviour. The lead writes also restart the whole
root on `errLeadCompanyMoved`, a separate bounded loop (`runLeadCompanyAttempts`) described in
their entries; the erasures restart theirs on `ErrErasureGraphChanged` (`eraseGraph`), and the
governed user write does the same around its nested erasure.

## Flows

### User-rooted erasure

- **Rows locked:** for a customer-role login: the automation runs of the leads converted into its
  customers (cancelled, `internal/repository/erasure.go` `cancelAutomationInTables`, called by
  `internal/repository/erasure_cascade.go` `erasureCascade.erase`), those `leads` rows, every
  `customers` row linked to the login and the `users` row (the role is read under this lock), all
  three in `internal/repository/erasure_cascade.go` `erasureCascade.lockGraph`, and the user's
  `refresh_tokens`, `password_reset_tokens` and `api_keys` rows (`lockCredentials`,
  `internal/repository/erasure_cascade.go` `erasureCascade.lockCredentials`). All of them are locking reads by primary key. A
  staff login is erased alone: its `users` row and its credential rows. The scrub follows the
  locks: `eraseRecord` (`internal/repository/erasure.go`) with a locking read of the address
  (`internal/repository/erasure.go` `readErasedEmail`), the credential delete by `user_id`
  (`internal/repository/erasure.go` `purgeCredentials`, which stays the authority for what
  is removed), then the text of the linked deals and quotes
  (`internal/repository/business_text_erasure.go` `erasureCascade.scrubBusinessText`), whose `deals`, `quotes` and `quote_lines` rows
  are written by primary key or by `quote_id`, and last the unused `quote_response_tokens` rows of
  the quotes addressed to an erased customer, stamped by primary key after those `quotes` rows
  (#395). Last, inside the unit, the erasure listener the shipped server wires (#437,
  `internal/repository/erasure_cascade.go` `erasureCascade.notifyListener`) writes the `lead.deleted` and
  `customer.deleted` webhook events of the leads and customers that were live when locked: a
  plain read of the active `webhook_endpoints`, then one `INSERT` into `webhook_deliveries` with a
  row for every active endpoint subscribed to the deleted type, whether or not that endpoint
  received an earlier event for the record; `webhook_deliveries` is not read (#531). It
  locks nothing, and a restarted or replayed attempt writes its rows again inside that attempt
  only.
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, after a discovery of the
  graph with plain reads (`internal/repository/erasure_cascade.go` `discoverErasureGraph`);
  after the credential rows, deals and quotes text, then `quote_response_tokens` (#395).
  The webhook outbox read and insert come after all of them (#437).
  The credential ids are read with a plain `SELECT … WHERE user_id IN (…)` after the `users`
  lock and then locked by primary key. No locking read filters on `leads.customer_id`,
  `customers.user_id`, `deleted_at` or a credential table's `user_id`: such a read locks an index
  range and its gaps. Right after the `users` lock the graph is read again
  (`internal/repository/erasure_cascade.go` `erasureCascade.erase`): forward links (`customer_id`, `user_id`,
  `role`) from the locked rows, reverse links (the leads of a customer, the customers of a
  login) by plain reads; a graph that differs from the discovery rolls the unit back with
  `ErrErasureGraphChanged`. Snapshot rule: under REPEATABLE READ the first plain read of a
  transaction fixes its snapshot, so the discovery and the schema probes of the cancel
  (`internal/repository/erasure.go` `probeAutomationTables`) run on the root handle before
  the transaction begins, and no plain read of any kind runs on the transaction before the
  `users` lock, the last graph lock: only the writes of the cancel and `FOR UPDATE` reads by
  primary key. A migrator probe counts as a plain read: on MySQL 8 a select on
  `information_schema` fixes the snapshot (on MariaDB 10.11 it does not), and the reverse
  re-read would then miss a customer linked to the login before the locks were granted.
- **Transaction:** `userRepository.Delete` (`internal/repository/user_repository.go`) →
  `eraseGraph` (`internal/repository/erasure_cascade.go`) → `runErasureUnit`
  (`internal/repository/erasure.go`): a root transaction of its own, or a checked SAVEPOINT
  inside the caller's transaction. On `DELETE /users/{id}` the caller is the governed user write
  of #348 (see the RBAC flow), which holds the admin set, the actor and the target before the
  erasure starts; the discovery then answers from that transaction's snapshot.
- **Retry:** `WithTransactionAndRetry` with `erasureMaxRetries` 3
  (`internal/repository/erasure.go` `erasureMaxRetries`) replays the transaction on a deadlock, lock wait timeout
  or SQLite busy/locked, on the same discovery (the re-read under the locks catches a stale one).
  Around it `eraseGraph` starts over from a new discovery on `ErrErasureGraphChanged` only, and
  only after a clean rollback (`shouldRestartErasure`,
  `internal/repository/erasure_cascade.go` `shouldRestartErasure`), at most
  `erasureGraphMaxRestarts` (3) times (`internal/repository/erasure.go` `erasureGraphMaxRestarts`); then the error, which wraps the sentinel, is returned.
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
`internal/repository/erasure.go` `scrubUnlinkedSubmissions`) is a plain read, so a submission inserted concurrently can
survive.

### Customer-rooted erasure

- **Rows locked:** the automation runs of the leads converted into the customer (cancelled
  first), those `leads` rows, the `customers` row and, when its login is a customer-role account,
  every other `customers` row of that login with their leads, the login's `users` row and its
  credential rows: the statements of the user-rooted erasure, the graph being the same person. A
  staff account in `user_id` is locked
  (`internal/repository/erasure_cascade.go` `erasureCascade.lockGraph`, its role
  decides the link) and left alone. Each row's address is read by `readErasedEmail` during the
  scrub. The text scrub then writes the linked `deals` and `quotes`, and the unused
  `quote_response_tokens` rows of the customers' quotes are stamped by primary key after those
  `quotes` rows (#395).
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, with the discovery, the
  re-read after the `users` lock and the snapshot rule of the user-rooted erasure; after the
  credential rows, deals and quotes text, then `quote_response_tokens` (#395). The leads of
  the customer are found by the plain pluck by `customer_id`
  (`internal/repository/erasure_cascade.go` `convertedLeadsOf`) in the discovery and again
  in the re-read, never by a locking read.
- **Transaction:** `customerRepository.Delete` (`internal/repository/customer_repository.go`)
  → `eraseGraph` → `runErasureUnit`, as for the user-rooted erasure.
- **Retry:** `WithTransactionAndRetry` with `erasureMaxRetries` 3 and the `ErrErasureGraphChanged`
  restart bounded by `erasureGraphMaxRestarts`, as for the user-rooted erasure.
- **Status:** implemented

### Lead-rooted erasure

- **Rows locked:** the automation runs of every lead of the graph (cancelled first, with the
  answers of the discovery's schema probes; the lead plan still names the cancel as its
  `BeforeScrub`, `cancelLeadAutomation`, in
  `internal/repository/erasure_cascade.go` `leadErasurePlan`, which the unit clears after running it), the `leads` row (locking read by
  id, `internal/repository/erasure_cascade.go` `erasureCascade.lockGraph`, the first lock on the
  leads row in the unit; the
  non-admin refusal is decided on that locked row, and a `customer_id` that differs from the
  discovery ends the attempt), then the customer it was converted into
  (a locking read by id in the same function) with that customer's other leads, its login and
  the login's credential rows. `readErasedEmail` reads each address during the scrub. The text
  scrub then writes the linked `deals` and `quotes`, and for a converted lead the unused
  `quote_response_tokens` rows of the customer's quotes are stamped by primary key after those
  `quotes` rows (#395), and last the webhook outbox of the user-rooted erasure (#437).
- **Order:** automation runs → leads → customers → users → refresh_tokens →
  password_reset_tokens → api_keys, ids ascending inside each table, with the discovery, the
  re-read after the `users` lock and the snapshot rule of the user-rooted erasure: automation
  runs (`cancelAutomationInTables`) → leads (locking read by id,
  `internal/repository/erasure_cascade.go` `erasureCascade.lockGraph`) → customers (locking read
  by id, the same function) → users (locking read by id, the same function) → the credential
  rows → deals and quotes text,
  then `quote_response_tokens` (#395).
  `DeleteUnconverted` discovers and locks the root lead only and never reads or locks a customer
  or a user. The converted-lead pluck (`WHERE customer_id = ?` in
  `internal/repository/erasure_cascade.go` `convertedLeadsOf`) stays a plain read: a locking read there
  would take next-key locks on the `leads.customer_id` index and block unrelated conversions,
  and it is not needed, because a conversion always inserts a new customer,
  so no lead can be converted into a customer being erased. Every erased row is soft-deleted by
  primary key only (`internal/repository/erasure.go` `softDeleteErased`: a locking read of
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
- **Transaction:** `leadRepository.Delete` (admins,
  `internal/repository/lead_repository.go` `leadRepository.Delete`) or `leadRepository.DeleteUnconverted` (non-admins, refused with
  `ErrConvertedLeadNeedsAdmin` on the locked lead row before the lead is written, the cancel
  rolled back with the unit,
  `internal/repository/lead_repository.go` `leadRepository.DeleteUnconverted`) → `eraseGraph` → `runErasureUnit`.
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
  `company_id`, locked by `internal/service/lead_company_lock.go` `leadService.lockLeadCompanies`, then the
  `leads` row (`lockedLeadRead`); the new `customers` row is inserted (both in
  `internal/service/lead_conversion.go` `leadService.convertAttempt`). No deal is touched.
- **Order:** a plain read of the lead outside the transaction
  (`internal/service/lead_conversion.go` `leadService.ConvertToCustomer`); in each attempt a plain
  read of `company_id` (in `internal/service/lead_conversion.go` `leadService.convertAttempt`) →
  companies, ascending id (`internal/service/lead_company_lock.go` `leadService.lockLeadCompanies`)
  → the locked lead → customers. A locked lead that names another company ends the attempt with
  `errLeadCompanyMoved`; at most `leadCompanyMaxRestarts` restarts
  (`internal/service/lead_company_lock.go` `leadCompanyMaxRestarts`). A conversion queued behind a lead erasure
  finds the lead gone: its `lockedLeadRead` returns not found (`gorm.ErrRecordNotFound`, which
  `apperrors.IsNotFound` matches, so `POST /leads/{id}/convert` answers 404) and nothing is written
  (#337, see Lead-rooted erasure).
- **Transaction:** a transaction root (an enclosing transaction is refused with
  `utils.ErrTransactionRootRequired`); `runLeadCompanyAttempts`
  (`internal/service/lead_company_lock.go`), called by
  `internal/service/lead_conversion.go` `leadService.ConvertToCustomer`, around one root transaction per attempt.
- **Retry:** `WithTransactionAndRetry` with `maxRetries` 3
  (`internal/service/lead_conversion.go` `leadService.ConvertToCustomer`) replays the attempt's root on a deadlock, lock wait timeout or SQLite
  busy/locked; around it `runLeadCompanyAttempts` (`internal/service/lead_company_lock.go`) restarts
  the whole root at most `leadCompanyMaxRestarts` (3) times, on `errLeadCompanyMoved` only and only
  after a clean rollback (`internal/service/lead_company_lock.go` `shouldRestartLeadCompanyAttempt`),
  then returns `ErrLeadCompanyContended`.
- **Status:** implemented

### Lead update

- **Rows locked:** the `companies` rows of the stored and of the sent `company_id`, the `leads`
  row (`lockedLeadRead`) and, when `owner_id` changes, the new owner's `users` row
  (`internal/service/lead_service.go` `leadService.lockedUserRead`) and, for a custom-role owner, a
  shared lock on the owner's `roles` row and then its `role_permissions` rows
  (`UserRepository.GetByIDWithCurrentRole`:
  `internal/repository/user_repository.go` `userRepository.GetByIDWithCurrentRole` reads both through `SharedLockingRead`).
  The plain read of `company_id` fixes the REPEATABLE READ snapshot, so a plain role read would
  answer with the permissions as they stood before it; the shared lock reads the committed rows,
  so a role change committed before the owner check is seen. It waits for a role write in flight
  and does not serialise lead writes for other holders of the same role.
- **Order:** in each attempt (`internal/service/lead_service.go` `leadService.Update`) a plain
  read of `company_id` → companies, ascending id → the locked lead and the locked owner → the
  owner's custom `roles` row → its `role_permissions` rows (both shared). Same chain and the same `errLeadCompanyMoved` check as the
  conversion, bounded by `leadCompanyMaxRestarts`.
- **Transaction:** `WithTransaction` per attempt inside `runLeadCompanyAttempts`
  (`internal/service/lead_company_lock.go`), both called by
  `internal/service/lead_service.go` `leadService.Update`.
- **Retry:** `runLeadCompanyAttempts` restarts the whole root at most `leadCompanyMaxRestarts` times,
  on `errLeadCompanyMoved` only; each attempt is a plain `WithTransaction`, so there is no deadlock
  retry: error 1213 or 1205 reaches the caller. A role delete locks the role's holders in `users`
  again after it holds the `roles` row, while the owner check holds the owner's `users` row and
  then asks for a shared lock on that `roles` row; a concurrent delete of the owner's role can
  therefore make the lead write fail with a deadlock error, which is not replayed.
- **Status:** implemented

### Company delete

- **Rows locked:** the `companies` row (`LockLive`, called by
  `internal/service/company_service.go` `companyService.Delete`), then every `leads`, `customers` and `deals` row that names it
  (`internal/repository/company_repository.go` `companyRepository.UnlinkLeads`,
  `companyRepository.UnlinkCustomers`, `companyRepository.UnlinkDeals`), then the company's soft
  delete. With webhooks wired (#517) the `company.deleted` event follows the soft delete: a plain read of the active `webhook_endpoints`, then
  one `INSERT` into `webhook_deliveries` with a row for every active endpoint subscribed to it;
  neither table is locked, and the unlinked leads, customers and deals get no event.
- **Order:** a plain pre-read outside the transaction → companies → leads → customers → deals.
- **Transaction:** `WithTransaction` in
  `internal/service/company_service.go` `companyService.Delete`.
- **Retry:** none today. Target: the #183 retry rule.
- **Status:** implemented

### Bulk status

- **Rows locked:** leads, `FOR UPDATE` in id ascending order (`GetLeadsByIDs`,
  `internal/repository/bulk_status_repository.go` `bulkStatusRepository.GetLeadsByIDs`, #182, no
  company lock); tickets, `FOR UPDATE` in id ascending order (`GetTicketsByIDs`,
  `internal/repository/bulk_status_repository.go` `bulkStatusRepository.GetTicketsByIDs`, #242);
  tasks, `FOR UPDATE` in id ascending order (`GetTasksByIDs`,
  `internal/repository/bulk_status_repository.go` `bulkStatusRepository.GetTasksByIDs`, #558); and
  last the batch's own `bulk_operations` row (`CompletePending`,
  `internal/repository/bulk_operation_repository.go` `bulkOperationRepository.CompletePending`).
- **Order:** the pending `bulk_operations` row is inserted before and outside the transaction
  (`internal/service/bulk_status_service.go` `bulkStatusService.runBulkStatusUpdate`); inside it
  the batch's rows of one table, then its `bulk_operations` row. The task batch locks only its task
  rows (`GetTasksByIDs`, called by
  `internal/service/bulk_status_service.go` `bulkStatusService.BulkSetTaskStatus`) and its own
  `bulk_operations` row (`CompletePending`), so it can invert no cross-table order. The found,
  assignee and completed-task checks (in `bulkStatusService.BulkSetTaskStatus`) judge the
  locked rows, so a status or assignee committed by another transaction before the lock is the one
  they see. The single task update (`taskService.UpdateWithLabels`, `PUT /tasks/{id}`) is one
  transaction that locks the same row (`GetByIDForUpdate`,
  `internal/repository/task_repository.go` `taskRepository.GetByIDForUpdate`) before its
  completed-task re-check and the write
  (`internal/service/task_service.go` `taskService.writeLocked`), so the batch and the update wait for each other.
- **Transaction:** `WithTransaction` in
  `internal/service/bulk_status_service.go` `bulkStatusService.runBulkStatusUpdate`; a
  transaction root.
- **Retry:** none today. Target: the #183 retry rule.
- **Status:** implemented

### Login

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  bound in `internal/service/auth_login.go` `authService.authenticate`, read in
  `internal/service/auth_login.go` `authService.loginInTransaction`) and written there by
  `UpdateFields` (`last_login_at` and the lockout clear); between the owner read and that write,
  only while `security.sso.allow_password` is off (#457) and the service is built with
  `WithAuthIdentities`, a plain (non-locking) read of the owner's `user_identities` rows
  (`ExistsForUser`); then the inserted `refresh_tokens` row (`issueRefreshToken`), whose
  foreign-key check (`fk_refresh_tokens_user` in `migrations/20250806120000_initial_schema.up.sql`)
  takes its
  shared lock on an owner row this transaction already holds. The owner's custom role and its
  permissions are not locked: the custom role is read without a lock (`UserRepository.GetByID`
  reads the `roles` row and its `role_permissions` on the same transaction without the locking
  clause), so a sign-in never waits for a lock on a role row.
- **Order:** plain `GetByEmail` outside the transaction → bcrypt → the single sign-on password
  policy, read once outside the transaction (`checkSSOIdentity`; these three in
  `internal/service/auth_login.go` `authService.authenticate`) → `FOR UPDATE` owner read by id →
  plain `user_identities` read (setting off only) → `UpdateFields` → refresh token insert in a
  savepoint → signing of the access JWT from the locked `token_version` (these in
  `internal/service/auth_login.go` `authService.loginInTransaction`). Lock order users → (read)
  user_identities → refresh_tokens while the setting is off; with it on the read is skipped and the
  order is users → refresh_tokens. The locked row must still carry the hash bcrypt verified, be
  active, be live (not erased) and not be locked, and while the setting is off the owner must have
  no linked OpenID Connect identity, else the login is refused with the generic error and nothing
  is written. A wrong password (#545) runs
  its own `WithTransactionAndRetry` root (`loginMaxRetries`, in
  `internal/service/auth_login.go` `authService.authenticate`) after bcrypt: it locks the owner
  `users` row by primary key (`FOR UPDATE`) and writes `failed_login_attempts`/`locked_until` from
  that row (`internal/service/auth_login.go` `recordLoginFailureInTransaction`), users only, so
  parallel failed attempts each count (`TestLoginFailedAttemptsConcurrent{SQLite,MySQL}`).
- **Transaction:** one issuing transaction per attempt (`loginInTransaction`,
  `internal/service/auth_login.go` `authService.loginInTransaction`) on a service built by `NewAuthServiceWithSessions`; a
  service without a transaction manager (`NewAuthService`) keeps the legacy unlocked statements. A
  non-retryable failure of the refresh insert rolls back to its savepoint and the login answers an
  access token only; a signing failure rolls the whole transaction back. The sign-in audit row
  (#454) is not part of it: for a `POST /auth/login` that reaches `LoginWithTokens` the handler
  writes, on the root handle after the transaction has committed or rolled back (never once per
  replay), one `auth_audit_log` `INSERT` for a success, or for a refusal the aggregated upsert
  that adds 1 to the row of its method, reason, account, network and UTC minute (#553); a failed
  audit write changes nothing.
- **Retry:** `WithTransactionAndRetry` with `loginMaxRetries` 3
  (`internal/service/auth_login.go` `authService.authenticate`, bound by
  `internal/service/auth_login.go` `loginMaxRetries`) replays the whole root on a
  deadlock, lock wait timeout or SQLite busy/locked, including one raised by the refresh insert
  (returned tagged `ErrNestedRetryDeferred` from its savepoint).
- **Status:** implemented

### Refresh rotation

- **Rows locked:** the owner's `users` row (`FOR UPDATE`, bound in
  `internal/service/auth_refresh.go` `authService.RefreshAccessToken`, read in
  `internal/service/auth_refresh.go` `authService.rotateRefreshTokenInTransaction`), the presented
  `refresh_tokens` row (the conditional claim `ClaimForRotation`) and the inserted successor, both
  in the same function. On the reuse path (#547) the owner's `users` row and every live
  `refresh_tokens` row of the presented token's family (`RevokeFamily`), both in
  `internal/service/auth_refresh.go` `authService.refuseRevokedRefreshToken`.
- **Order:** a plain lookup of the token by hash → users → refresh_tokens (the claim, then the
  successor). The owner is locked before the claim
  (`internal/service/auth_refresh.go` `authService.rotateRefreshTokenInTransaction`). Every owner decision (active, live) is taken on the
  locked row; the token itself is re-checked by the conditional claim. When the live lookup
  misses, the reuse path is: plain lookups of the revoked row and of a later row of its family
  (`internal/service/auth_refresh.go` `authService.refuseRevokedRefreshToken`) → users (`FOR UPDATE`) → refresh_tokens
  (`RevokeFamily`), still users → refresh_tokens. A request that lost the claim to a concurrent
  rotation never takes this path: it passed the live lookup and is refused at the claim.
- **Transaction:** the sign-in audit row (#454) is written by the handler on the root handle
  after the rotation returned, outside its transaction: one `auth_audit_log` `INSERT` for a
  success, or for a refusal the aggregated upsert into the row of its bucket (#553).
  `WithTransaction` in `internal/service/auth_refresh.go` `authService.RefreshAccessToken`; the reuse path commits its family
  revocation in the same transaction and returns the generic refusal.
- **Retry:** none today. Target (C2): the #183 retry rule.
- **Status:** implemented

Logout (`internal/service/auth_logout.go` `authService.Logout`) and password change
(`internal/service/auth_password_change.go` `authService.ChangePassword`) follow the same users → refresh_tokens order in one
`WithTransaction` each, and decide on the locked owner row (the password change re-checks the
verified hash and the active flag under the lock). The password change continues with the
account's `api_keys` rows (`revokeAllCredentials`, in
`internal/service/auth_password_change.go` `authService.changePasswordInTransaction`, #526):
users → refresh_tokens → api_keys, and the successor `refresh_tokens` row is inserted after that.
Logout never touches `api_keys`.

### Password-reset request

Since #554 the request runs on the one worker of the password reset queue
(`service.PasswordResetQueue`), after `POST /auth/password-reset` has answered; the transaction
below is unchanged.

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  bound in `internal/service/auth_password_reset.go` `authService.IssuePasswordReset`, read by
  `internal/service/auth_service.go` `lockLiveCredentialOwner`); then a plain (non-locking) read of
  the user's `user_identities` rows (`ExistsForUser`, #444; only when the service is built with
  `WithAuthIdentities`, as `cmd/main.go` does, and only while `security.sso.allow_password` is
  off, #457, a policy read once before the transaction); then the user's outstanding
  `password_reset_tokens` rows, spent by `InvalidateAllForUser`, and the inserted token, both
  through the transaction-bound repository, all in `authService.IssuePasswordReset`.
  The insert's foreign-key check (`fk_password_reset_tokens_user` in
  `migrations/20261001120000_v1_2_0_schema_parity.up.sql`) takes its shared lock on an owner
  row this transaction already holds.
- **Order:** plain `GetByEmail` outside the transaction (an unknown or inactive address stops
  here) → `FOR UPDATE` owner read by id → plain `user_identities` read → `InvalidateAllForUser` →
  insert → commit → mail, sent once, after the commit, to the address of the locked row
  (`internal/service/auth_password_reset.go` `authService.IssuePasswordReset`). Lock order users → (read) user_identities →
  password_reset_tokens (users → password_reset_tokens with the setting on). A missing, erased
  (soft-deleted) or inactive locked row, or, while the setting is off, an owner with a linked
  OpenID Connect identity (an account linked to single sign-on), refuses the request
  with nothing written and no mail; the answer is nil in every case. An erasure or deactivation
  that commits before the lock therefore issues nothing, and one that commits after the request ends the token it issued (the erasure
  hard-deletes it, the confirm refuses an inactive owner).
- **Transaction:** one `WithTransactionAndRetry` root per request
  (`internal/service/auth_password_reset.go` `authService.IssuePasswordReset`) on `context.Background()`; a failure of the
  invalidation or of the insert rolls both back and sends no mail. A service without a
  transaction manager writes nothing and sends nothing.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `resetRequestMaxRetries` 3
  (`internal/service/auth_password_reset.go` `resetRequestMaxRetries`) replays the whole root on a deadlock, lock wait timeout
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

- **Rows locked:** the `password_reset_tokens` row (the conditional claim `MarkUsed`), the
  owner's `users` row (`FOR UPDATE`, bound in
  `internal/service/auth_password_reset.go` `authService.CompletePasswordReset`, then read, written and, for `token_version`, written by
  `revokeAllCredentials`), then every `refresh_tokens` row and every `api_keys` row of the user
  (revoked by the same `revokeAllCredentials`, #526); the claim, the reads and the writes are in
  `internal/service/auth_password_reset.go` `authService.confirmPasswordResetInTransaction`.
  Between the owner read and the password write, a plain (non-locking) read of the owner's
  `user_identities` rows (`ExistsForUser`, #444) refuses an account linked to single sign-on
  with `ErrInvalidResetToken`; the rollback leaves the token unspent. That read happens only
  while `security.sso.allow_password` is off (#457; the policy is read once before the
  transaction, in `authService.CompletePasswordReset`); with it on it is skipped.
- **Order:** password_reset_tokens → users → (read) user_identities → refresh_tokens → api_keys (the read
  only while `security.sso.allow_password` is off), after a
  plain lookup of the token by hash (in `authService.confirmPasswordResetInTransaction`). This inverts the reset request's order; the
  outcome is described under that flow.
- **Transaction:** one `WithTransactionAndRetry` root per confirmation
  (`internal/service/auth_password_reset.go` `authService.CompletePasswordReset`) on `context.Background()`; every attempt redoes the
  lookup, the claim, the owner lock and the writes.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `resetConfirmMaxRetries` 3
  (`internal/service/auth_password_reset.go` `resetConfirmMaxRetries`) replays the whole root on a deadlock, lock wait timeout
  or SQLite busy/locked. A replay that finds the token spent or gone, or its owner erased, is
  refused with `ErrInvalidResetToken`. An exhausted bound returns the last error with nothing
  written: `POST /auth/password-reset/confirm` answers the generic 500 and the link is still
  valid. The worst case is four lock waits, one per attempt, with no timeout of its own, as for
  login and the reset request.
- **Status:** implemented

### API-key create

- **Rows locked:** the owner's `users` row, read by primary key with a locking read (`FOR UPDATE`,
  read by `lockLiveCredentialOwner`), then the inserted `api_keys` row (through
  `apiKeyRepo.WithTx(tx)`), both in `internal/service/apikey_service.go` `apiKeyService.Generate`;
  the insert's foreign-key check (`fk_api_keys_user` in
  `migrations/20250806120000_initial_schema.up.sql`) takes its shared lock on an owner row this transaction already holds.
- **Order:** `apiKeyService.Generate` (`internal/service/apikey_service.go`): `FOR UPDATE` owner
  read by id → insert. Lock order users → api_keys. A missing, erased (soft-deleted) or inactive
  locked row is refused with an error matching `ErrAccountUnavailable` (`POST /api-keys` answers
  401), no row and no key. A locked owner that is a customer account is refused with
  `ErrAPIKeyCustomerAccount` (403, #532), so a role change that commits first refuses the create
  too. An erasure or deactivation that commits first refuses the create; one that commits after it
  hard-deletes the key (erasure) or leaves it unusable (`ValidateAPIKey` refuses an inactive owner,
  and a customer one). An `expires_at` more than one year ahead is refused before the
  transaction.
- **Transaction:** one `WithTransactionAndRetry` root per create
  (`internal/service/apikey_service.go` `apiKeyService.Generate`) on `context.Background()`. A service built without the
  user repository or the transaction manager returns `ErrAPIKeyIssuanceUnavailable` and writes
  nothing.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `apiKeyCreateMaxRetries` 3
  (`internal/service/apikey_service.go` `apiKeyCreateMaxRetries`) replays the whole root on a deadlock, lock wait timeout
  or SQLite busy/locked.
- **Status:** implemented

### API-key revoke

- **Rows locked:** the target `api_keys` row only, through one conditional `UPDATE`
  (`internal/repository/apikey_repository.go` `apiKeyRepository.Revoke`: the predicate
  `id = ? AND user_id = ? AND revoked_at IS NULL` is in the statement, which sets `is_active` false
  and `revoked_at`). A plain ownership read (`apiKeyService.ownedKey`) comes before it and locks
  nothing. No owner `users` lock is taken. Both `DELETE /api-keys/{id}`
  (`internal/service/apikey_service.go` `apiKeyService.Revoke`) and `PUT /api-keys/{id}` with
  `is_active` false (`internal/service/apikey_service.go` `apiKeyService.Update`) end in that
  statement.
- **Order:** one row lock in one statement, the `api_keys` step of `users → api_keys`. The revoke
  holds no other lock, so it cannot invert a chain. A key that is already revoked keeps its first
  `revoked_at` (0 rows changed). A rename that asks for `is_active` true carries the
  `revoked_at IS NULL` predicate in its own statement
  (`internal/repository/apikey_repository.go` `apiKeyRepository.Rename`), so against a revoke that committed first it changes nothing and is
  refused as a revoked key.
- **Transaction:** `no transaction`.
- **Retry:** none; the single statement is not replayed.
- **Status:** implemented

### RBAC user-role and role writes

- **Rows locked:** a governed user write (`POST /users`, every `PUT /users/{id}` on another
  account or that writes `role`, `role_id` or `is_active`, activate, deactivate and
  `DELETE /users/{id}`; `internal/service/user_governed_writes.go` `userService.governedWrite` and
  `userService.CreateAs`) locks the admin set, the `users` rows of the active admins
  (`internal/service/last_admin_guard.go` `lockAdminSet`, which calls `LockActiveAdmins`,
  `internal/repository/user_repository.go` `userRepository.LockActiveAdmins`: a plain read
  discovers the ids, then a locking read by primary key in id order, never through
  `idx_users_role`), the `users` rows of the actor and the target (`LockLiveByIDs`,
  `internal/repository/user_repository.go` `userRepository.LockLiveByIDs`, one locking read by
  primary key in id order) and
  the `roles` rows of the actor's, the target's and the requested custom role with their
  `role_permissions` rows (`RoleRepository.LockByIDs`,
  `internal/repository/role_repository.go` `roleRepository.LockByIDs`: the roles by primary key in id order, then their
  permissions, both locking reads, so a transaction that waited reads the committed rows and not
  its snapshot). A role write (`internal/service/role_service.go` `roleService.Create`,
  `roleService.Update`, `roleService.Delete` and `roleService.Clone`) locks the actor's `users` row and then the
  `roles` rows of the actor's role and the role it touches; `ReplacePermissions` and `Delete`
  (`internal/repository/role_repository.go` `roleRepository.ReplacePermissions`,
  `roleRepository.Delete`) lock the `roles` row
  again by primary key before they delete its `role_permissions` rows. The role delete also takes
  a locking read of the live users holding the role, by `role_id`
  (`LockLiveRoleHolders`,
  `internal/repository/user_repository.go` `userRepository.LockLiveRoleHolders`), before the roles and again after them (both in
  `internal/service/role_service.go` `roleService.Delete`).
- **Order:** admin set → users (id ascending) → roles (id ascending), with a role's
  `role_permissions` rows after its `roles` row. A role write starts at the actor's `users` row:
  users before roles. The actor is read again under its lock and every refusal (the lost
  permission, the escalation rule, the customer-login rule, the last-admin invariant) is decided
  after the locks, from the locked rows. The last-admin refusal is `adminSet.checkAfter`
  (`internal/service/last_admin_guard.go`), which judges the state after the write, with
  `adminSet.include` counting the locked target rows: the last-admin rule counts the locked row of
  the account being changed as an admin before the write whenever that row is an active admin,
  next to the admin set: `LockActiveAdmins` discovers the admins with a consistent read, so on MySQL/MariaDB an
  admin promoted by a transaction that committed while this one waited (a SCIM group handover,
  #506) is missing from that set, and only the locking read of `LockLiveByIDs` sees it (#513).
  During such a race this can only refuse more often, never less: a write is refused even when a
  third admin was promoted concurrently, which is deliberate. Whether a customer login has a linked customer record
  (`HasLinkedCustomerRecord`, a plain read of `customers` by `user_id` after the locks) takes no
  lock, so the lock order is unchanged. The role delete is the one place where `users` rows are
  not taken in ascending id: its holder reads lock by `role_id`, after the actor's row and, the
  second time, after the roles; a deadlock that this causes is replayed. The startup data step
  that removes the admin-only keys from custom roles (#524, `stripAdminOnlyRolePermissions` in
  `internal/models`) takes one transaction per role and locks only that `roles` row before it
  deletes the role's `role_permissions` rows, so it holds no `users` lock and cannot invert the
  users-then-roles order. A governed write that deactivates (`is_active` false, also on an
  account already inactive, #526) then ends every credential of the target with
  `revokeAllCredentials` after its `users` write and before the audit row: `token_version` on the
  locked row, every `refresh_tokens` row, every `api_keys` row, so the write continues with
  refresh_tokens → api_keys. A service built without `WithUserCredentialRevocation` refuses such a
  write before any write. The `role_audit_log` rows come last (#552): after the `users` write and
  the revocation, `user_email_changed` for a changed email, `user_activated` or
  `user_deactivated` for a changed `is_active`, then `user_role_changed`; an erasure inserts
  `user_erased` after its nested erasure returned, in the same root, so a restart or replay
  commits one row. A changed email (#554) also spends the target's unused `password_reset_tokens`
  rows inside `UpdateFields`, right after the `users` UPDATE (a SAVEPOINT in the root): users →
  password_reset_tokens, the reset request's order and the inversion of the reset confirm's. When
  such a write and a confirm of the same user deadlock, InnoDB ends one of them and its root is
  replayed (`roleWriteMaxRetries` here, `resetConfirmMaxRetries` for the confirm); a replayed
  confirm finds its token spent and answers the generic 400.
- **Transaction:** one root transaction per write (`roleGovernance.run`,
  `internal/service/role_governance.go` `roleGovernance.run`) through the `WithTx` repositories; the audit row
  (`role_audit_log`) is inserted in it, so a failed insert rolls the change back. The erasure of
  `DELETE /users/{id}` runs inside it as its savepoint-bracketed unit
  (`tx.users.Delete` in `userService.governedWrite`), once and without a retry of its own. By then
  the write holds the target's `users` row, so the nested erasure takes leads and customers after
  users, the one inversion of the erasure order (#385); `governedWrite` is not reordered.
- **Retry:** `WithTransactionAndRetry` (the #183 rule) with `roleWriteMaxRetries` 3
  (`internal/service/role_governance.go` `roleWriteMaxRetries`, used by `roleGovernance.runOnce`)
  replays the whole
  root on a deadlock, lock wait timeout or SQLite busy/locked, one raised inside the nested
  erasure included. Around it `roleGovernance.run` restarts the whole root when the nested
  erasure returns `ErrErasureGraphChanged`, only after a clean rollback
  (`shouldRestartGovernedWrite`,
  `internal/service/role_governance.go` `shouldRestartGovernedWrite`) and at most `roleWriteGraphMaxRestarts` (3) times
  (`internal/service/role_governance.go` `roleWriteGraphMaxRestarts`).
- **Status:** implemented

### OIDC sign-in

- **Rows locked:** the callback of `POST /auth/oidc/callback` (`Service.Callback`,
  `internal/idkit/sso/service.go`, wired by `NewOIDCService` in `internal/service/auth_oidc.go`)
  first spends its `oidc_login_attempts` row
  (`OIDCLoginAttemptRepository.Claim`, `internal/repository/oidc_repository.go`): a plain read by
  `state_hash`, then one conditional `UPDATE` by id (`used_at IS NULL AND expires_at > now`, which
  also blanks the nonce and verifier), its own committed statement before the transaction and
  before the issuer is contacted. No row is locked by the read; a competing claim between the read
  and the `UPDATE` leaves zero affected rows and the generic refusal. The unusable password of a first sign-in is hashed with bcrypt before
  the transaction (`oidcPasswordFirstRunner.WithTransaction`), once per callback, so no lock is
  held during it. The governed transaction (`oidcSignInRunner.WithTransaction`,
  `internal/service/auth_oidc_host.go`) then locks the `users` rows of the active admins
  (`LockActiveAdmins`); the link rule (`Service.signInAccount`, `internal/idkit/sso/link.go`)
  reads the `user_identities` row of the exact `(issuer, subject)` pair with a
  plain read; when that pair is unknown and the callback vetted an Entra ID `oid` (#505), it
  plain-reads the `source` `scim` row of `(issuer, oid)`, which names the SCIM-created account.
  The account store (`oidcAccountStore.SignIn`, `internal/service/auth_oidc_host.go`)
  then locks the linked `users` row (`LockLiveByIDs`) and the `roles` rows of the user's current
  custom role and of the mapped role (`RoleRepository.LockByIDs`). The last-admin refusal of a
  re-applied role counts the locked `users` row as an admin before the sign-in when it is an
  active admin, next to the admin set, as every governed write does (#513). It then writes: the
  role change and
  its `role_audit_log` row (for a SCIM-matched account also the `user_identities` insert of the
  `(issuer, subject)` link), or on a first sign-in (`oidcAccountStore.Provision`) the `users`
  insert and the audit row and then the `user_identities` insert, and last the `refresh_tokens`
  insert.
- **Order:** admin set → users → roles → user_identities → refresh_tokens. The plain identity
  read takes no lock; it only names the `users` row to lock. A first sign-in locks no existing
  `users` row: its new row and its identity row are inserts, and a competing first sign-in of the
  same address or identity loses on the unique index and rolls back whole. The plain read of the
  SCIM row takes no lock either; a competing first sign-in that commits the same `(issuer,
  subject)` link first makes the link insert fail on the unique index (`identity_taken`, rolled
  back whole) on MySQL/MariaDB, while on SQLite the write after the competing commit is refused as
  busy and the replay finds the committed link directly.
- **Transaction:** one root transaction (`roleGovernance.runWithContext`, which replays and restarts as `roleGovernance.run` does) through the `WithTx` repositories;
  the role change, the audit row, the provisioned user, its identity and the refresh token commit
  together or not at all, and the access token is signed from the row the transaction wrote. The
  attempt claim is not part of it: a state is spent even when the sign-in is then refused. Nor is
  the sign-in audit row (#454): `Callback` writes one `auth_audit_log` row per call, as its own
  `INSERT` on the root handle after the transaction has ended, and then logs its outcome with
  that row's id (`audit_id`).
- **Retry:** `WithTransactionAndRetry` with `roleWriteMaxRetries` 3, as every governed write; a
  replayed attempt inserts fresh rows.
- **Status:** implemented

### Identity-provider sign-out

- **Rows locked:** `GET /auth/oidc/frontchannel-logout` calls `oidcService.FrontChannelLogout`
  (`internal/service/auth_oidc.go`), which ends the provider session through
  `internal/service/auth_oidc_host.go` `idkitSessionEnder.EndProviderSession`. That refuses to run
  inside a transaction (`idkit.ErrInTransaction`), reads the distinct owners of the live refresh
  tokens that carry the provider session hash with a plain read ordered by `user_id`
  (`internal/repository/refresh_token_repository.go` `refreshTokenRepository.LiveUserIDsByProviderSession`), and then calls
  `internal/service/auth_logout.go` `authService.Logout` with `(userID, "")` for each owner. Each
  call takes what a logout takes: its own `WithTransaction`, a `LockingRead` of the owner's `users`
  row, then the revocation of every `refresh_tokens` row of the account, then the `token_version`
  write on the locked `users` row.
- **Order:** the plain owner read, outside any transaction; then, one account after the other in
  `user_id` order, the Logout order users → refresh_tokens. No lock is held from one account to the
  next.
- **Transaction:** one `WithTransaction` per account, the one of the logout; no transaction spans
  accounts. One `logout` sign-in audit row per account is written after that transaction, outside
  it. The first failure stops the loop, and the accounts before it stay logged out.
- **Retry:** none (each logout is a plain `WithTransaction`).
- **Status:** implemented

### SCIM user create and replace

- **Rows locked:** `POST /scim/v2/Users` (`scimUserService.Create`,
  `internal/service/scim_user_writes.go`) locks the `users` rows of the active admins
  (`LockActiveAdmins`) and the `roles` row the `roles` mapping entry names
  (`RoleRepository.LockByIDs`), then reads with plain reads whether any user, erased ones
  included, holds the address (`EmailExistsFoldedUnscoped`) and whether the `user_identities` row
  of `(issuer, externalId)` exists, and writes the `users` insert, the `user_identities` insert and
  the `role_audit_log` row. `PUT /scim/v2/Users/{id}` (`scimUserService.Replace`) locks the admin
  set, the target's `users` row (`LockLiveByIDs`) and the `roles` rows of the target's current
  custom role and of the mapped role, reads whether another user holds a changed address
  (`SCIMUserRepository.EmailTakenByOther`, plain), then writes the `users` columns and, on a role
  change, the audit row. Right after the `users` lock it reads the target's `user_identities` rows
  with a plain read (`SCIMUserRepository.IdentitiesForUsers`) and refuses, as a not-found and before
  any check or write, an account with no identity under the effective issuer (#529); the
  application never deletes a `user_identities` row, so this read needs no lock and the order does
  not change. It writes no `user_identities` row. A replace with `active` false (#471, #526)
  then ends every credential of the account after the `users` write (`revokeAllCredentials`):
  `token_version` plus one, every `refresh_tokens` row and every `api_keys` row revoked.
  A replace that changes the address (#554) also spends the target's unused
  `password_reset_tokens` rows inside `UpdateFields`, right after the `users` UPDATE; a deadlock
  with a reset confirm of the same user is resolved as for the governed writes (see *RBAC
  user-role and role writes*).
  The last-admin rule counts the target's locked `users` row as an admin before the write whenever
  that row is an active admin, next to the admin set: `LockActiveAdmins` discovers the admins with
  a consistent read, so on MySQL/MariaDB an admin promoted by a SCIM group handover that committed
  while this write waited is missing from that set, and only the locking read of `LockLiveByIDs`
  sees it (#513). This can only refuse more often during such a race, never less.
- **Order:** admin set → users → roles → user_identities, the OIDC sign-in's order without its
  `refresh_tokens` step; a deactivating replace takes `refresh_tokens` and then `api_keys` last
  instead of `user_identities` (see the SCIM user patch). A create locks no existing `users` row: its new row
  and its identity row are inserts, and a competing create of the same address or identity, or a
  first OIDC sign-in, loses on the unique index and rolls back whole (409).
- **Transaction:** one root transaction (`roleGovernance.run`) through the `WithTx`
  repositories; the user, its identity and the audit row commit together or not at all. The
  successful `auth_audit_log` row is written after the commit, on the root handle, as its own
  `INSERT`.
- **Retry:** `WithTransactionAndRetry` with `roleWriteMaxRetries` 3, as every governed write; a
  replayed attempt inserts fresh rows.
- **Status:** implemented

### SCIM user patch

- **Rows locked:** `PATCH /scim/v2/Users/{id}` (`scimUserService.Patch`,
  `internal/service/scim_user_writes.go`) locks the `users` rows of the active admins
  (`LockActiveAdmins`), the target's `users` row (`LockLiveByIDs`) and the `roles` rows of the
  target's current custom role and, when the patch sets `roles`, of the mapped role
  (`RoleRepository.LockByIDs`). The patch is applied to the locked row. A changed address is
  checked with a plain read (`SCIMUserRepository.EmailTakenByOther`); then the touched `users`
  columns are written in one `UPDATE`; on `active` false `revokeAllCredentials` then sets
  `token_version` to the locked value plus one, revokes every `refresh_tokens` row and then every
  `api_keys` row of the account (#526); then the `role_audit_log` rows are inserted: a changed
  address its `user_email_changed` row, a changed `active` its `user_activated` or
  `user_deactivated` row (#552), and a role change its `user_role_changed` row. A changed address
  (#554) also spends the target's unused `password_reset_tokens` rows inside `UpdateFields`, right
  after the `users` UPDATE, so the patch continues users → password_reset_tokens; a deadlock with a
  reset confirm of the same user is resolved as for the governed writes. Like the replace, it plain-reads the target's
  `user_identities` rows right after the `users` lock and refuses an account not linked under the
  effective issuer as a not-found before anything is written (#529); it writes no
  `user_identities` row. The delete is its own flow (see *SCIM user delete*).
  The last-admin rule counts the target's locked `users` row as an admin before the write whenever
  that row is an active admin, next to the admin set: `LockActiveAdmins` discovers the admins with
  a consistent read, so on MySQL/MariaDB an admin promoted by a SCIM group handover that committed
  while this write waited is missing from that set, and only the locking read of `LockLiveByIDs`
  sees it (#513). This can only refuse more often during such a race, never less.
- **Order:** admin set → users → roles → refresh_tokens → api_keys, then the audit rows. The
  `users` row comes before its
  `refresh_tokens` and `api_keys` rows, as in refresh rotation, logout and password change, so a
  competing refresh waits on the `users` row and then finds its token revoked.
- **Transaction:** one root transaction (`roleGovernance.run`) through the `WithTx`
  repositories; the `users` write, the revocation and the audit row commit together or not at
  all, and a refusal (the last-admin rule, a taken address, an unmapped role, a failed revocation
  of the refresh tokens or the API keys) writes nothing. The successful `auth_audit_log` row is
  written after the commit, on the root handle, as its own `INSERT`.
- **Retry:** `WithTransactionAndRetry` with `roleWriteMaxRetries` 3, as every governed write.
- **Status:** implemented

### SCIM user delete

- **Rows locked:** `DELETE /scim/v2/Users/{id}`
  (`internal/service/scim_user_writes.go` `scimUserService.Delete`, #504) locks the admin set (`lockAdminSet`, through
  `LockActiveAdmins`) and the target's `users` row (`LockLiveByIDs`) and no `roles` row, since it
  writes no role; it plain-reads the target's identities as the patch does (#529); on an active
  target it writes `is_active` false, then `revokeAllCredentials` writes `token_version` plus one
  and revokes every `refresh_tokens` and every `api_keys` row of the account, and last inserts the
  `user_deactivated` `role_audit_log` row (#552). On an already inactive target it writes nothing
  and commits an empty transaction. The last-admin rule counts the target's locked `users` row as
  an admin before the write whenever that row is an active admin, next to the admin set, for the
  reason given under the patch (#513).
- **Order:** admin set → users → refresh_tokens → api_keys → role_audit_log, the patch's order
  without `roles`. The `users` row comes before its `refresh_tokens` and `api_keys` rows, so a
  competing refresh waits on the `users` row and then finds its token revoked.
- **Transaction:** one root transaction (`roleGovernance.run`) through the `WithTx`
  repositories; the `users` write, the revocation and the audit row commit together or not at
  all, and a refusal (the last-admin rule, a failed revocation of the refresh tokens or the API
  keys) writes nothing. The successful `auth_audit_log` row is written after the commit, on the
  root handle, as its own `INSERT`; a delete of an already inactive account writes none.
- **Retry:** `WithTransactionAndRetry` with `roleWriteMaxRetries` 3, as every governed write.
- **Status:** implemented

### SCIM group patch

- **Rows locked:** `PATCH /scim/v2/Groups/{id}` (`scimGroupService.Patch`,
  `internal/service/scim_group_writes.go`) locks the `users` rows of the active admins
  (`LockActiveAdmins`) first, so two group patches that both reach the same admin wait for each
  other. `LockActiveAdmins` discovers the admins with a consistent read, so on MySQL/MariaDB a
  patch that waited for an admin a concurrent handover demoted no longer sees the new admin in
  that set; the last-admin rule therefore also counts every affected user that the locking read
  of `LockLiveByIDs` shows as an active admin, and refuses a result without one.
  It then reads the group's `scim_group_members` rows with a locking read
  (`SCIMGroupMemberRepository.ListMemberIDs` on a `FOR UPDATE` handle), so it sees what a group
  patch committed while it waited for the admin set rather than its REPEATABLE READ snapshot;
  every other group patch holds the admin set before it reaches those rows, so this read waits on
  no one. It locks the `users` rows of the members it adds or removes and of every id an add or a
  replace names (`LockLiveByIDs`), plain-reads which of them are linked under the effective issuer
  (`SCIMGroupMemberRepository.ManagedUserIDs`, #529; identities are never deleted), then the `roles` rows of their current custom roles and of the
  mapped roles (`RoleRepository.LockByIDs`), then reads those users' other memberships with the
  same locking read (`GroupsOfUsers`). It writes the membership rows (`Remove`, then `Add`), then,
  per affected managed user: an active user left in no mapped group gets `is_active` false
  (`UpdateFields`) and `revokeAllCredentials` (`token_version`, every `refresh_tokens` row, every
  `api_keys` row; #530); a user whose role changes gets its `users` row (`UpdateFields`) and, when
  the new role lacks a permission the old one had, `endSessions` (`token_version`, every
  `refresh_tokens` row). The `role_audit_log` rows of the role changes are written last.
- **Order:** admin set → users → roles → scim_group_members → refresh_tokens → api_keys →
  role_audit_log. The membership, refresh-token and API-key writes take the shared foreign-key
  lock on `users` rows the patch already holds.
- **Transaction:** one root transaction (`roleGovernance.run`) through the `WithTx`
  repositories; the membership rows, the role writes, the deactivations, the credential
  revocations and the audit rows commit together or not at all, and a refusal (an id that is no
  managed account, a mapping to a missing or customer role, a result without an active admin, where
  a deactivated account no longer counts as one, a failed revocation of the refresh tokens or the
  API keys) writes nothing. One successful `auth_audit_log` row per managed
  account whose membership changed is written after the commit, on the root handle; a patch that changes
  nothing writes none.
- **Retry:** `WithTransactionAndRetry` with `roleWriteMaxRetries` 3, as every governed write.
  With no active admin the admin set locks nothing; two group patches can then deadlock on
  membership rows and the loser is replayed.
- **Status:** implemented

## How the #175 failure modes are avoided

**Foreign-key-check lock inversion.** On InnoDB an insert or update of a child row whose foreign key
names a parent takes a shared lock on that parent row. A flow that locks a child first and then
writes through a foreign key to a parent another transaction already holds inverts the order even
when its own statements look ordered. The specified chains lock parents first: `refresh_tokens.user_id`
references `users`, so a session transaction locks the owner before it claims or inserts a token
(the rotation that claimed first deadlocked against a password change with error 1213, see
`internal/service/auth_refresh.go` `authService.rotateRefreshTokenInTransaction`); in the #182 chain the company is locked before the lead,
and the lead before the customer row that names the company. The #175 inversion runs through lead
updates: an update that locked the lead and then wrote its `company_id` took the foreign key
check's shared lock on the company, while a company delete held the company and waited on the
lead in `UnlinkLeads`. Since #182 the update locks the companies before the lead, so both wait on
the company row first.

Caveat: the #182 MySQL roots run on an `AutoMigrate` schema without foreign keys
(`DisableForeignKeyConstraintWhenMigrating`, set by
`internal/service/automation_test_helpers_test.go` `automationGormConfig`, schema opened by
`internal/service/automation_test_helpers_test.go` `openAutomationMySQL`). They
prove the row-lock order, not the foreign key check's shared locks, which are untested on the real
`migrations/` schema.

**No `performance_schema.data_lock_waits` on MariaDB.** MariaDB 10.11 has no
`performance_schema.data_lock_waits` table (MDEV-31165), so a contention probe cannot ask the
server who waits on whom. The lock-order roots in
`internal/service/lead_company_lock_order_mysql_test.go` poll `information_schema.PROCESSLIST`
instead (`internal/service/lead_company_lock_order_mysql_test.go` `awaitLeadCompanyLockWait`) and count a wait when the
same statement on the same connection is still executing on two consecutive polls
(`internal/service/lead_form_bulk_cancellation_test.go` `lockWaitProbe`). The probe behaves the
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
| Bulk status | #182, #242, #558 | implemented |
| Login | C2 | implemented |
| Refresh rotation | C2 | implemented |
| Password-reset request | C3 | implemented |
| Password-reset confirm | C3 | implemented |
| API-key create | C3 | implemented |
| API-key revoke | #525 | implemented |
| RBAC user-role and role writes | #348 | implemented |
| OIDC sign-in | #435 | implemented |
| Identity-provider sign-out | #634 | implemented |
| SCIM user create and replace | #470 | implemented |
| SCIM user patch | #471 | implemented |
| SCIM user delete | #504 | implemented |
| SCIM group patch | #506 | implemented |

C2 and C3 are Paperclip ids with no GitHub issue yet; C2–C4 follow the family order of the
#183 comment of 14 Nov 2026 (C2 session credentials, C3 reset and API-key issuance, C4 erasure
graph discovery). C4 is implemented by #385: one lock order for the three erasures, the re-check
of the graph under the locks and the bounded whole-root retry. The RBAC lock order of #259 is
implemented by #348.
