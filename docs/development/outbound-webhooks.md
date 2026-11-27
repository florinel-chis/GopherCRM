# Outbound webhooks

> **For:** Developers changing outbound webhook delivery. Part of the [documentation index](../README.md).

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
  [../operations/aeo-egress.md](../operations/aeo-egress.md#public-only-egress-for-webhooks)), 10 s per attempt
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
  [../reference/data-model.md](../reference/data-model.md#outbound-webhooks)) from `MigrateDatabase` on SQLite and
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
