# Outbound Webhooks — Test Cases

Cases for the outbound webhooks (#345 and its follow-ups): the admin-only registry and delivery
log API, the outbox rows every write of the seven catalogue entities records in its own
transaction, the Standard Webhooks signature, the test ping (#501), redelivery (#502), secret
rotation (#503), the per-pass secret check (#559), retry backoff and the endpoint pause (#569),
the egress guard, the delivery log retention, the log privacy of every webhook line and the
migrations. The Settings > Webhooks screen is covered in [08-settings.md](08-settings.md) and is
not repeated here: TC-SET-036 to TC-SET-040 (register, edit and delete, permission gating,
delivery log filters, URL refusal) and TC-SET-044 to TC-SET-046 (test ping, secret rotation and
redelivery from the screen).

Every **Expected** states what the build does **today**, traced to the handlers, services,
runtime and repository; every automated case names the Go roots that pin it. No browser spec
exercises the API directly: TC-HOOK-023 plans one.

23 cases: 22 automated (backend), 1 planned, 0 blocked. Priorities: 11 P0, 11 P1, 1 P2.

## Sources

- `internal/handler/routes.go` (`SetupWebhookRoutes`), `internal/handler/webhook_handler.go`,
  `internal/handler/protected_routes.go`, `internal/permissions/routes.go` (`RouteKeys`),
  `internal/permissions/catalogue.go`
- `internal/webhook/catalog.go`, `internal/webhook/emitter.go`,
  `internal/webhook/endpoint_service.go`, `internal/webhook/runtime.go`,
  `internal/webhook/signing.go`
- `internal/models/webhook.go`, `internal/models/webhook_state.go`
- `internal/repository/webhook_repository.go`, `internal/repository/interfaces.go`
- `internal/egress/egress.go`, `internal/egress/public.go`
- `cmd/main.go`, `cmd/webhook_runtime.go`
- `migrations/20261120120000_add_outbound_webhooks.up.sql`,
  `migrations/20261127120000_webhook_secret_rotation.up.sql`,
  `migrations/20261129120005_webhook_endpoint_pause.up.sql`,
  `migrations/20261129120007_widen_webhook_event_types.up.sql` and their `.down.sql` halves
- `docs/integrations/webhooks.md`

## Constraints

- **Admin only.** Every `/webhooks` route is guarded by the `webhooks.manage` permission key
  (`internal/permissions/routes.go` `RouteKeys`); among the system roles only admin holds it, so
  sales, support and customer get **403** `FORBIDDEN` before the handler runs.
- **The outbox is transactional.** An event is a set of `webhook_deliveries` rows (one per active
  endpoint subscribed to its type, one shared `evt_` id) written inside the transaction of the
  write that caused it; a rolled-back write leaves no row. Rows are sent only after the commit,
  by the runtime (`internal/webhook/runtime.go` `NewWebhookRuntime`).
- **Thin body, signed.** A delivery body is `{id, type, occurred_at, record_id}` and nothing
  else; no personal data leaves the CRM in a delivery.
- **Run the MySQL roots with `TEST_MYSQL_DSN`.** Every `*MySQL` root creates a disposable
  schema; it skips without the variable and fails when the server is unreachable. A green run
  without it proves the SQLite half only.
- **No real network.** The Go roots send to an `httptest` TLS consumer through an injected egress
  guard; no case needs an external host.

---

## 17.1 Access

### TC-HOOK-001 — Every webhook route is admin only
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Credentials of every role, as a Bearer token and as an `ApiKey`.
- **Steps:**
  1. Call each of the nine routes of `SetupWebhookRoutes` without credentials and with an
     invalid token: `GET` and `POST /webhooks/endpoints`, `GET`, `PUT` and `DELETE
     /webhooks/endpoints/:id`, `POST /webhooks/endpoints/:id/test`, `POST
     /webhooks/endpoints/:id/rotate-secret`, `GET /webhooks/deliveries`, `POST
     /webhooks/deliveries/:id/redeliver`.
  2. Repeat as sales, support and customer, with both schemes.
  3. Repeat as admin, with both schemes.
  4. Read every admin answer for a signing secret.
- **Expected:** Step 1: **401** `UNAUTHORIZED`. Step 2: **403** `FORBIDDEN` for every role and
  scheme, and the service is never called. Step 3: admin reaches the service (200, 201 for the
  create, 204 for the delete). Step 4: a `whsec_` secret appears only in the answers of `POST
  /webhooks/endpoints` and `POST /webhooks/endpoints/:id/rotate-secret`; no answer carries
  `secret_sealed` or `previous_secret_sealed`.
- **Automation:** automated (backend) — `internal/handler/webhook_handler_test.go`
  `TestWebhookRoutesAdminOnly`; over the real middleware stack, sales and support refused in
  `tests/webhooks_test.go` `TestWebhookAPISQLite`

### TC-HOOK-002 — Malformed input is 400 VALIDATION_ERROR keyed by the field
- **Priority:** P1
- **Type:** validation
- **Preconditions:** An admin.
- **Steps:**
  1. `POST /webhooks/endpoints` with, one at a time: an `http://` URL, a URL with a query string,
     a URL with credentials, a URL over 2048 characters, a private, loopback or metadata IP
     literal, no URL, an empty, missing or unknown `event_types`, `webhook.ping` as an event type,
     an empty body.
  2. `PUT /webhooks/endpoints/1` with an empty body, `{}`, an `http://` URL, an unknown or empty
     `event_types`.
  3. Send a non-positive or non-numeric id to `GET`, `PUT`, `DELETE`, `/test` and `/redeliver`.
  4. `GET /webhooks/endpoints?sort_by=url`, and `GET /webhooks/deliveries` with an unknown
     `status`, an unknown `event_type`, a non-numeric or zero `endpoint_id`.
- **Expected:** **400** `VALIDATION_ERROR` each time, with `error.details` keyed by `url`,
  `event_types`, `body`, `id`, `sort_by`, `status`, `event_type` or `endpoint_id`; the submitted
  value is never echoed in the answer. Malformed JSON is 400 `BAD_REQUEST` ("Invalid request
  format"). Nothing is written.
- **Automation:** automated (backend) — `internal/handler/webhook_handler_test.go`
  `TestWebhookHandlerValidation`

## 17.2 Event fan-out

### TC-HOOK-003 — Lead, customer and ticket creates enqueue one row per subscribed endpoint
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Endpoint A subscribed to `lead.created`, `customer.created` and
  `ticket.created`; B to `lead.created`; C inactive with all three; D to `ticket.created`.
- **Steps:**
  1. Create a lead (`LeadService.Create`), a lead through a form with `create_lead`, a customer,
     a lead conversion, a ticket and a ticket through form intake.
  2. Read the `webhook_deliveries` rows of each record.
- **Expected:** One row per active subscribed endpoint: `lead.created` for A and B,
  `customer.created` and `ticket.created` for A (and D for the ticket); never for the inactive C.
  The rows of one event share one `evt_` + 32 hex id and are `pending`, attempts 0, no error
  code, no last attempt, due at `occurred_at`. An endpoint deactivated by another connection, or
  created inactive, gets no row. A conversion replayed after a retryable failure commits one row
  set. Without the outbox option every path behaves as before and writes no row.
- **Automation:** automated (backend) — `internal/service/webhook_outbox_test.go`
  `TestWebhookOutboxFanOutSQLite`, `TestWebhookOutboxFanOutMySQL`,
  `TestWebhookLeadCreateWithoutTransactionManager`

### TC-HOOK-004 — Lead, customer and ticket changes enqueue the updated event only on a change
- **Priority:** P0
- **Type:** functional
- **Preconditions:** An endpoint subscribed to `lead.updated`, `customer.updated` and
  `ticket.updated`; a created-only endpoint; an inactive endpoint.
- **Steps:**
  1. Update a lead, a customer and a ticket with a changed value.
  2. Update each with its stored values.
  3. Change lead and ticket status through the bulk status endpoints; assign a lead to another
     owner and to its current owner; convert a lead; run the automation `assign_owner` step.
  4. Send refused updates (another customer's email, an unknown `company_id`, status
     `converted`).
- **Expected:** Step 1: one `lead.updated`, `customer.updated`, `ticket.updated` row for the
  subscribed endpoint, none for the created-only or inactive one. Step 2: no row. Step 3: a row
  for each record whose stored value changed (a conversion writes `lead.updated` next to
  `customer.created`); assigning the current owner writes nothing. Step 4: no row is left by any
  refusal. The delivered body is thin (four fields).
- **Automation:** automated (backend) — `internal/service/webhook_updated_events_test.go`
  `TestWebhookUpdatedFanOutSQLite`, `TestWebhookUpdatedFanOutMySQL`,
  `TestWebhookUpdatedAutomationSQLite`, `TestWebhookUpdatedAutomationMySQL`

### TC-HOOK-005 — Lead and customer erasure and ticket delete enqueue the deleted event
- **Priority:** P0
- **Type:** functional
- **Preconditions:** An endpoint subscribed to every type (`AllEventTypes` in
  `internal/webhook/catalog.go`).
- **Steps:**
  1. Erase a lead and a customer; delete a ticket.
  2. Erase a customer with a converted lead, the converted lead itself, and the customer's
     login user (through the plain and the governed user delete).
  3. Delete a record no endpoint received an earlier event for, and one whose earlier deliveries
     were pruned.
  4. Delete a second time; erase a customer soft-deleted earlier; attempt a refused non-admin
     erasure.
- **Expected:** Steps 1 to 3: one `lead.deleted`, `customer.deleted` or `ticket.deleted` row per
  subscribed active endpoint for every erased or deleted record, cascades included, whether or not
  the endpoint saw an earlier event; erasure is indistinguishable from delete in the outbox.
  Step 2 writes both `customer.deleted` and `lead.deleted`. Step 4: no row. A restarted erasure
  commits one row set. The delivered body carries none of the erased person's data.
- **Automation:** automated (backend) — `internal/service/webhook_deleted_events_test.go`
  `TestWebhookDeletedFanOutSQLite`, `TestWebhookDeletedFanOutMySQL`

### TC-HOOK-006 — Deal, task, company and quote writes enqueue created, updated and deleted
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Endpoints subscribed to every type; the catalogue registers lead, customer,
  ticket, deal, task, company and quote (`internal/webhook/catalog.go` `NewCatalogue`,
  `defaultCatalogue`), which gives twenty-one event types `<entity>.created`,
  `<entity>.updated`, `<entity>.deleted`.
- **Steps:**
  1. Deal: create, update (same values, then a changed note, then another stage), `ChangeStage`,
     the automation `assign_owner` step, delete.
  2. Task: create with and without labels, the automation `create_task` step, update (unchanged,
     changed title, changed label set alone, empty label list), bulk status, delete.
  3. Company: create (and a refused create), update (unchanged, changed text, owner cleared and
     set), delete of a company with linked leads, customers and deals.
  4. Quote: create, update (unchanged, changed notes), add, edit and delete lines, each status
     transition and a refused one, email, the client's public accept and decline, delete.
- **Expected:** `deal.created`, `deal.updated`, `deal.deleted`; `task.created`, `task.updated`,
  `task.deleted`; `company.created`, `company.updated`, `company.deleted`; `quote.created`,
  `quote.updated`, `quote.deleted` — one row per subscribed active endpoint, and only when a
  stored value changed. A company delete writes `company.deleted` only: clearing `company_id` on
  its leads, customers and deals writes no updated event. A lead-linked task create writes no
  lead row; emailing a quote writes no row; a refused transition, refused delete, refused bulk
  batch or lost delete race writes no row. An endpoint subscribed to every type is stored with
  the twenty-one sorted types. A service built without the emitter writes no row.
- **Automation:** automated (backend) — `internal/service/deal_webhook_events_test.go`
  `TestDealWebhookFanOutSQLite`, `TestDealWebhookFanOutMySQL`;
  `internal/service/task_webhook_events_test.go` `TestTaskWebhookFanOutSQLite`,
  `TestTaskWebhookFanOutMySQL`; `internal/service/company_webhook_events_test.go`
  `TestCompanyWebhookFanOutSQLite`, `TestCompanyWebhookFanOutMySQL`;
  `internal/service/quote_webhook_events_test.go` `TestQuoteWebhookFanOutSQLite`,
  `TestQuoteWebhookFanOutMySQL`

### TC-HOOK-007 — The event is written in the entity's transaction
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Two endpoints subscribed to every type; an outbox repository that writes the
  rows and then fails (or fails after the soft delete, or an erasure listener that fails after
  its enqueue).
- **Steps:**
  1. With the failing outbox, create, update and delete a record of each of the seven entities
     (and for quotes the line writes, the status change and the public response; for tasks bulk
     status; for deals `ChangeStage`).
  2. Erase a lead, a customer and a user with the failing outbox, and a cascade with the failing
     listener.
  3. Retry the ticket delete with the real outbox.
- **Expected:** Steps 1 and 2: the write fails with the injected error, the record is unchanged
  (no new row, the stored state rolled back, a deleted record still live, company links kept)
  and no `webhook_deliveries` row exists. Step 3: the delete succeeds and writes exactly one
  deleted row, so the failed attempt committed nothing.
- **Automation:** automated (backend) — `internal/service/webhook_outbox_test.go`
  `TestWebhookOutboxAtomicitySQLite`, `TestWebhookOutboxAtomicityMySQL`;
  `internal/service/webhook_updated_events_test.go` `TestWebhookUpdatedAtomicitySQLite`,
  `TestWebhookUpdatedAtomicityMySQL`; `internal/service/webhook_deleted_events_test.go`
  `TestWebhookDeletedAtomicitySQLite`, `TestWebhookDeletedAtomicityMySQL`;
  `internal/service/deal_webhook_events_test.go` `TestDealWebhookAtomicitySQLite`,
  `TestDealWebhookAtomicityMySQL`; `internal/service/task_webhook_events_test.go`
  `TestTaskWebhookAtomicitySQLite`, `TestTaskWebhookAtomicityMySQL`;
  `internal/service/company_webhook_events_test.go` `TestCompanyWebhookAtomicitySQLite`,
  `TestCompanyWebhookAtomicityMySQL`; `internal/service/quote_webhook_events_test.go`
  `TestQuoteWebhookAtomicitySQLite`, `TestQuoteWebhookAtomicityMySQL`

### TC-HOOK-008 — Deleted events through the API
- **Priority:** P1
- **Type:** functional
- **Preconditions:** SQLite file database with the full schema; the real middleware, repositories
  and services as `cmd/main.go` wires them; an admin.
- **Steps:**
  1. `POST /webhooks/endpoints` subscribed to `lead.deleted`, `customer.deleted` and
     `ticket.deleted`; then `PUT` it down to `ticket.deleted`.
  2. `GET /webhooks/deliveries?event_type=customer.deleted`; `POST /webhooks/endpoints` with
     `lead.archived`.
  3. Register a second endpoint with every type.
  4. `POST /tickets`, then `DELETE /tickets/<id>`.
  5. `POST /leads`, then `DELETE /leads/<id>`.
- **Expected:** Step 1: 201 with `event_types` answered sorted (`customer.deleted`,
  `lead.deleted`, `ticket.deleted`), then 200 with `ticket.deleted`. Step 2: 200; then 400
  `VALIDATION_ERROR` with `details.event_types`. Step 4: `ticket.created` was written; the delete
  is 204 and writes two `ticket.deleted` rows, one per subscribed endpoint, under one event id,
  both `pending` — also for the endpoint that never received `ticket.created`. Step 5: 204 and one
  `lead.deleted` row, for the every-type endpoint only.
- **Automation:** automated (backend) — `tests/webhooks_deleted_test.go`
  `TestWebhookDeletedAPISQLite`; the production wiring in `cmd/webhook_deleted_wiring_test.go`
  `TestWebhookDeletedProductionWiring`

## 17.3 Signing and secrets

### TC-HOOK-009 — A delivery carries the Standard Webhooks headers and signature
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A `whsec_` secret.
- **Steps:**
  1. Sign the pinned vector with `SignWebhook` and verify it with an independent verifier, at the
     signing time and ten minutes later.
  2. Change one byte of the id, the timestamp or the body.
  3. Sign with a secret that is not `whsec_` plus base64.
  4. Build a delivery request (`NewWebhookRequest`) and a retry of it.
  5. Generate secrets with `NewWebhookSecret`.
- **Expected:** Step 1: the signature equals the pinned `v1,` + base64 HMAC-SHA256 over
  `<webhook-id>.<webhook-timestamp>.<body>` keyed by the base64-decoded part of the secret; the
  verifier accepts it and refuses the stale timestamp. Step 2: verification fails. Step 3: an
  error. Step 4: a POST with `webhook-id` (the `evt_` id), `webhook-timestamp` (Unix seconds),
  `webhook-signature`, a JSON content type and the user agent `GopherCRM-Webhooks/1`; the body is
  exactly `{"id","type","occurred_at","record_id"}` with `occurred_at` in UTC with milliseconds;
  a retry carries its own timestamp and signature over the same body. Step 5: `whsec_` plus the
  base64 of 32 random bytes.
- **Automation:** automated (backend) — `internal/service/webhook_signing_test.go`
  `TestWebhookSignature`; the code under test is `internal/webhook/signing.go` `SignWebhook` and
  `internal/webhook/signing.go` `NewWebhookRequest`

### TC-HOOK-010 — A rotation signs with both secrets during the overlap window
- **Priority:** P0
- **Type:** functional
- **Preconditions:** An active endpoint with its create secret; a consumer that records requests.
- **Steps:**
  1. Rotate the secret (`RotateEndpointSecret`) and deliver inside the 24 h window.
  2. Deliver at `previous_secret_expires_at` and one second after it.
  3. Rotate twice inside the window; rotate concurrently from two connections.
  4. Ping the endpoint inside the window and after it.
  5. Rotate an inactive endpoint and an unknown id.
- **Expected:** Step 1: the new secret matches `whsec_` + 44 base64 characters and differs from
  the old one; `webhook-signature` carries two space-separated `v1,` signatures, the new secret's
  first, each verifying only under its own secret (`internal/webhook/signing.go`
  `newSignedWebhookRequest`); nothing is cleared inside the window. Step 2: one signature, under
  the new secret; the expired previous secret is cleared without bumping `updated_at`, and a
  clear computed before a newer rotation committed is fenced. Step 3: at most two secrets ever
  sign; concurrent rotations keep exactly the two returned secrets. Step 4: the ping signs twice
  inside the window, writes nothing and does not clear an expired previous secret. Step 5: the
  inactive endpoint is rotated; the unknown id is not found. An endpoint without a previous
  secret sends one signature; an unopenable previous secret is dropped with one Warn line.
- **Automation:** automated (backend) — `internal/service/webhook_secret_rotation_test.go`
  `TestWebhookSecretRotationSQLite`, `TestWebhookSecretRotationMySQL`; the window length is
  `internal/webhook/endpoint_service.go` `WebhookSecretOverlap`

### TC-HOOK-011 — The rotate-secret route answers the new secret once
- **Priority:** P1
- **Type:** functional
- **Preconditions:** An admin; endpoint 4.
- **Steps:**
  1. `POST /webhooks/endpoints/4/rotate-secret` with no body.
  2. `GET /webhooks/endpoints/4`.
  3. `POST /webhooks/endpoints/0/rotate-secret` and `/abc/rotate-secret`.
  4. Rotate an unknown endpoint.
- **Expected:** Step 1: 200 with `id`, the new `secret` and `previous_secret_expires_at`; no
  `secret_sealed` or `previous_secret_sealed`. Step 2: 200 with `previous_secret_expires_at` and
  no secret of any kind. Step 3: 400 `VALIDATION_ERROR` with `details.id`, no service call. Step
  4: 404 `NOT_FOUND`.
- **Automation:** automated (backend) — `internal/handler/webhook_handler_test.go`
  `TestWebhookRotateSecretHandler`

### TC-HOOK-012 — An endpoint whose secret does not open is held, not failed
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Active endpoints, some with a `secret_sealed` the runtime's key cannot open
  (for example after `API_KEY_SECRET` changed); due rows for each.
- **Steps:**
  1. Run a delivery pass.
  2. Run a pass with a runtime that holds the right key.
  3. Rotate the secret of one held endpoint and run a pass.
  4. Fill the outbox with held rows beyond the discovery batch, plus one deliverable row.
  5. Make a secret stop opening between the check and the attempt.
- **Expected:** Step 1: the held rows stay `pending` with their attempts, `next_attempt_at`,
  `error_code` and `last_attempt_at` untouched, and the endpoint circuit is not touched. Step 2:
  the held rows are delivered. Step 3: only the rotated endpoint is released. Step 4: the held
  backlog does not fill the batch; the deliverable row goes out. Step 5: the row is retried with
  `secret_unavailable`, not failed. Entering the held set logs one Error line, leaving it one Info
  line.
- **Automation:** automated (backend) — `internal/service/webhook_secret_hold_test.go`
  `TestWebhookSecretHoldSQLite`, `TestWebhookSecretHoldMySQL`; the health wiring in
  `cmd/webhook_secret_hold_wiring_test.go` `TestWebhookSecretHoldHealthWiring`

## 17.4 Test ping

### TC-HOOK-013 — A test ping sends one signed request and writes nothing
- **Priority:** P1
- **Type:** functional
- **Preconditions:** An admin; endpoints answering 200, 500 and a redirect, one that never answers,
  one whose host resolves to a private address, one inactive, one with an unopenable secret.
- **Steps:**
  1. `POST /webhooks/endpoints/<id>/test` (no body) for each endpoint.
  2. Ping an unknown id.
  3. Through the production router, ping an inactive endpoint at `https://localhost/hook`.
- **Expected:** Step 1: 200 every time. The answering endpoint receives exactly one request,
  signed under the endpoint's secret, with the body `{"id","type","occurred_at"}`, type
  `webhook.ping`, no `record_id`, and `webhook-id` equal to the body's id; outcome `succeeded`
  with `http_status` 200 (the inactive endpoint too). A 500 is `failed` with `error_code`
  `http_status`; a redirect is `failed` with `redirect` and is not followed; the silent endpoint
  is `failed` with `timeout`; the private host is `failed` with `destination_refused` and is
  never dialled; the unopenable secret is `failed` with `secret_unavailable` and nothing is sent.
  `http_status` is null when no answer arrived. No ping is retried, no delivery row is written,
  and the endpoint's `updated_at`, `is_active`, URL and secret are unchanged. Step 2: 404. Step 3:
  200 with `outcome` `failed`, `error_code` `destination_refused`, `http_status` null, no
  `whsec_` in the answer and no delivery row (`cmd/main.go` hands the service the public-only
  egress guard through `WithWebhookPingClients`). A service built without that option answers
  `ErrWebhooksUnavailable` (500).
- **Automation:** automated (backend) — `internal/service/webhook_ping_test.go`
  `TestWebhookPing`; `cmd/webhook_ping_wiring_test.go` `TestWebhookPingProductionWiring`

## 17.5 Delivery, retry and pause

### TC-HOOK-014 — A failing delivery is retried with exponential backoff up to ten attempts
- **Priority:** P0
- **Type:** functional
- **Preconditions:** An endpoint subscribed to `lead.created` that answers 503; an injected clock.
- **Steps:**
  1. Create a lead and run delivery passes, moving the clock to each `next_attempt_at` (and one
     second before it).
  2. Advance the clock 15 days and run a pass.
  3. Replace the endpoint with one that answers 302.
- **Expected:** Step 1: one request per attempt. After attempts 1 to 9 the row is `pending` with
  `error_code` `http_status`, `last_http_status` 503 and `next_attempt_at` 1, 2, 4, 8, 16, 32, 64,
  128 and 256 minutes later (`WebhookBackoff` with `WebhookRetryBase` 1 min and `WebhookRetryMax`
  6 h); nothing is sent a second before it. Attempt 10 (`WebhookMaxAttempts`) stores `failed` with
  no `next_attempt_at`. Step 2: a failed row is never retried; exactly ten requests in all, each
  freshly signed with its own timestamp over the same body. Step 3: `pending` with `error_code`
  `redirect` and `last_http_status` 302; the `Location` target receives nothing.
- **Automation:** automated (backend) — `internal/service/webhook_runtime_test.go`
  `TestWebhookRetryBackoffAndCap`; the timeout path in `internal/service/webhook_runtime_test.go`
  `TestWebhookRequestTimeout`

### TC-HOOK-015 — Consecutive failures pause an endpoint; a 2xx resets the pause
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A failing endpoint; a healthy endpoint; an injected clock.
- **Steps:**
  1. With `consecutive_failures` at 2, run a pass whose attempt fails.
  2. Seed another due row and run a pass a second before `paused_until`, then at it.
  3. Keep failing at each `paused_until`.
  4. Let the endpoint answer 2xx at the next `paused_until`.
  5. Run passes with a second paused endpoint next to a healthy one, an inactive endpoint and one
     whose secret does not open.
- **Expected:** Step 1: the third consecutive failure (`WebhookEndpointPauseAfter`) stores
  `consecutive_failures` 3 and `paused_until` = finish time + 1 min (`internal/webhook/runtime.go`
  `WebhookEndpointPausedUntil`). Step 2: nothing is sent before `paused_until` and the second row
  stays `pending`, unclaimed, attempts 0; at `paused_until` one request goes out, and the fourth
  failure pauses for 2 min. Step 3: the pause doubles — 4, 8, 16, 32 minutes, then an hour
  (`internal/webhook/runtime.go` `WebhookEndpointPause`, capped at `WebhookEndpointPauseMax`);
  an attempt that pauses the endpoint stops the rest of its group in that pass. Step 4: both
  columns reset to 0 and NULL. Step 5: other endpoints are unaffected; attempts that send
  nothing (inactive endpoint, held secret) do not count; a stale failure write after a reset
  changes nothing. A `PUT` on the endpoint does not clear the pause.
- **Automation:** automated (backend) — `internal/service/webhook_endpoint_pause_test.go`
  `TestWebhookEndpointPauseSQLite`, `TestWebhookEndpointPauseMySQL`; the fenced writes are
  `internal/repository/interfaces.go` `WebhookRepository.RecordEndpointFailure` and
  `internal/repository/interfaces.go` `WebhookRepository.RecordEndpointSuccess`

### TC-HOOK-016 — A delivery to a non-public address is refused and recorded
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Active endpoints subscribed to `lead.created` whose host names resolve to a
  loopback, a private, a link-local metadata and an IPv6 loopback address.
- **Steps:**
  1. Create a lead and run a delivery pass.
  2. Read the delivery rows.
- **Expected:** Step 1: an attempt per endpoint, and no connection is opened to any of them; no
  request is received. Step 2: every row records the refused attempt: `error_code`
  `destination_refused`, `last_http_status` null, attempts 1, `status` `pending` with
  `next_attempt_at` one retry step (1 min) later. The row follows the retry schedule of
  TC-HOOK-014 and is stored `failed` when the tenth attempt is refused. (A public-looking host
  name is accepted at registration; an IP-literal private host is refused there, see
  TC-HOOK-002.)
- **Automation:** automated (backend) — `internal/service/webhook_runtime_test.go`
  `TestWebhookEgressRefusal`

### TC-HOOK-017 — The delivery log lists newest first and prunes finished rows after 30 days
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Two endpoints; deliveries for a lead and a ticket, one of them `failed`.
- **Steps:**
  1. List deliveries without a filter, with offset 1 and limit 1, by endpoint, by status
     `failed` and by event type `ticket.created`; then with status `done` and type
     `lead.archived`.
  2. Seed `succeeded`, `failed` and `pending` rows older than `WebhookDeliveryRetention` and a
     recent `succeeded` row; run a pass.
  3. Seed another old `failed` row; run a pass 10 minutes later, then after `WebhookPruneInterval`.
- **Expected:** Step 1: newest first (id descending), paginated, each filter narrowing to its
  rows; the unknown status and type are validation errors. A row carries exactly 14 fields (`id`,
  `endpoint_id`, `event_id`, `event_type`, `record_id`, `occurred_at`, `status`, `attempts`,
  `next_attempt_at`, `last_attempt_at`, `last_http_status`, `error_code`, `created_at`,
  `updated_at`), never `lease_until`, a URL, a secret or a payload. Step 2: the old `succeeded`
  and `failed` rows are pruned; the `pending` row and the recent row stay. Step 3: no second
  prune within the hour; after it the old row is pruned.
- **Automation:** automated (backend) — `internal/service/webhook_runtime_test.go`
  `TestWebhookDeliveryLogAndPrune`

## 17.6 Redelivery

### TC-HOOK-018 — Redeliver a succeeded or failed delivery
- **Priority:** P1
- **Type:** functional
- **Preconditions:** An admin; an active endpoint; a `failed` and a `succeeded` delivery; a
  delivery whose record has since been removed.
- **Steps:**
  1. `POST /webhooks/deliveries/<id>/redeliver` (no body) on the failed row; run a pass.
  2. Repeat on the succeeded row and on the row whose record is gone.
  3. Redeliver an unknown id.
- **Expected:** Step 1: 200 with the same row (same `id` and `event_id`), `status` `pending`,
  attempts 0, due at once, `last_http_status` and `error_code` cleared; the runtime sends it
  again under the same `webhook-id` with the full budget of ten attempts. Step 2: the same, with
  no new row written; the record is never read and the original `record_id` is sent. Step 3: 404
  `NOT_FOUND` ("Webhook delivery not found").
- **Automation:** automated (backend) — `internal/service/webhook_redeliver_test.go`
  `TestWebhookRedeliverSQLite`, `TestWebhookRedeliverMySQL`;
  `internal/handler/webhook_handler_test.go` `TestWebhookRedeliverHandler`

### TC-HOOK-019 — A pending or sending delivery is not redelivered
- **Priority:** P1
- **Type:** concurrency
- **Preconditions:** An admin; a `pending` and a `sending` delivery; a due row.
- **Steps:**
  1. Redeliver the `pending` row and the `sending` row.
  2. Let a worker claim the due row, then redeliver it.
  3. Redeliver a failed row, then let two claims compete; redeliver a second failed row and run
     two passes.
  4. Redeliver a row twice.
- **Expected:** Step 1: 409 `CONFLICT` "Webhook delivery is pending or being sent"
  (`internal/webhook/endpoint_service.go` `ErrWebhookDeliveryNotRedeliverable`); the row is
  unchanged and nothing is sent. Step 2: the same; the claimed row keeps its attempts. Step 3:
  exactly one claim succeeds (attempt 1 of a fresh budget); two passes send exactly one request.
  Step 4: the second call is 409 and the pending row is unchanged.
- **Automation:** automated (backend) — `internal/service/webhook_redeliver_test.go`
  `TestWebhookRedeliverSQLite`, `TestWebhookRedeliverMySQL`;
  `internal/handler/webhook_handler_test.go` `TestWebhookRedeliverHandler`

### TC-HOOK-020 — A delivery of a deleted or inactive endpoint is not redelivered
- **Priority:** P1
- **Type:** negative
- **Preconditions:** An admin; a `failed` delivery of an inactive endpoint; a `failed` delivery
  whose endpoint row no longer exists.
- **Steps:**
  1. Redeliver each.
- **Expected:** 409 `CONFLICT` "Webhook endpoint is deleted or inactive"
  (`internal/webhook/endpoint_service.go` `ErrWebhookEndpointUnavailable`); the row is unchanged
  and nothing is sent. The endpoint check is a read before the write: an endpoint deactivated in
  between still gets the row requeued, and the runtime then fails it with `endpoint_inactive`.
  `DELETE /webhooks/endpoints/<id>` removes the endpoint's delivery rows in the same
  transaction, so after an API delete the delivery id answers 404.
- **Automation:** automated (backend) — `internal/service/webhook_redeliver_test.go`
  `TestWebhookRedeliverSQLite`, `TestWebhookRedeliverMySQL`;
  `internal/handler/webhook_handler_test.go` `TestWebhookRedeliverHandler`

## 17.7 Logs, schema and the API round trip

### TC-HOOK-021 — Webhook log lines carry no secret, URL, body or personal data
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logging at debug in JSON; one endpoint that answers 200 then 500 with a
  canary response body, one whose host resolves to a private address; leads with canary names
  and emails.
- **Steps:**
  1. Create two leads and run a pass after each.
  2. Update the endpoint URL, attempt an `http://` registration, delete an endpoint twice.
  3. Read every captured log line.
- **Expected:** No line contains an endpoint URL, host or path, a `whsec_` secret, a sealed
  secret, a `webhook-signature` value, a request body, the response body or the lead's name or
  email. Every line whose `operation` starts with `webhook_` carries only `level`, `msg`, `time`,
  `operation`, `endpoint_id`, `delivery_id`, `event_type`, `record_id`, `attempts`, `status`,
  `http_status`, `error_code` and `error`. The ping, redelivery and rotation paths log the
  operation and ids only.
- **Automation:** automated (backend) — `internal/service/webhook_runtime_test.go`
  `TestWebhookLogsOmitSecrets`; the per-path log subtests of
  `internal/service/webhook_ping_test.go` `TestWebhookPing`,
  `internal/service/webhook_redeliver_test.go` `TestWebhookRedeliverSQLite` and
  `internal/service/webhook_secret_hold_test.go` `TestWebhookSecretHoldSQLite`

### TC-HOOK-022 — The webhook schema migrates on MySQL, MariaDB and SQLite
- **Priority:** P2
- **Type:** migration
- **Preconditions:** `TEST_MYSQL_DSN` for the MySQL and MariaDB roots; SQLite files holding the
  pre-#503 and pre-#569 `webhook_endpoints` tables.
- **Steps:**
  1. Apply, roll back and reapply the four webhook migrations.
  2. Run `MigrateDatabase` on the older SQLite files, twice.
- **Expected:** Step 1: `20261120120000_add_outbound_webhooks` creates `webhook_endpoints` and
  `webhook_deliveries` only when missing, matching AutoMigrate, and its down half drops only
  them; `20261127120000_webhook_secret_rotation` adds `previous_secret_sealed` and
  `previous_secret_expires_at`; `20261129120005_webhook_endpoint_pause` adds
  `consecutive_failures` (NOT NULL default 0) and `paused_until`; each adds only missing columns
  and its down half drops only them. `20261129120007_widen_webhook_event_types` widens
  `event_types` to 512 characters, and its down half refuses to narrow while a row is longer
  than 255 bytes. Step 2: the new columns are added with NULL or 0, every existing endpoint is
  byte-identical, and the second run changes nothing.
- **Automation:** automated (backend) — `cmd/migrate/webhook_migration_test.go`
  `TestWebhookMigrationFiles`, `TestWebhookMigration`;
  `cmd/migrate/webhook_secret_rotation_migration_test.go` `TestWebhookSecretRotationMigration`;
  `cmd/migrate/webhook_endpoint_pause_migration_test.go` `TestWebhookEndpointPauseMigration`;
  `cmd/migrate/webhook_event_types_width_migration_test.go` `TestWebhookEventTypesWidthMigration`;
  `internal/models/webhook_secret_rotation_upgrade_test.go`
  `TestWebhookSecretRotationSQLiteUpgrade`; `internal/models/webhook_endpoint_pause_upgrade_test.go`
  `TestWebhookEndpointPauseSQLiteUpgrade`

### TC-HOOK-023 — Registry and delivery log round trip against the running backend
- **Priority:** P1
- **Type:** functional
- **Preconditions:** The `make e2e` backend; the seeded admin.
- **Steps:**
  1. `POST /webhooks/endpoints` with a public https URL and `lead.created`.
  2. `GET /webhooks/endpoints/<id>` and `GET /webhooks/endpoints`.
  3. Create a lead; `GET /webhooks/deliveries?endpoint_id=<id>`.
  4. `GET /webhooks/deliveries?status=bogus`.
  5. `DELETE /webhooks/endpoints/<id>`, then list its deliveries.
- **Expected:** Step 1: 201 with `is_active` true and a `whsec_` secret. Step 2: 200, neither
  answer carries the secret, `secret` or `secret_sealed`. Step 3: one `lead.created` row for the
  endpoint (it stays `pending` while the host is unreachable). Step 4: 400 `VALIDATION_ERROR`.
  Step 5: 204, and no delivery row of the endpoint is left.
- **Automation:** planned — `gocrm-ui/e2e/tests/webhooks-api.spec.ts` (new); pinned at the Go
  level by `tests/webhooks_test.go` `TestWebhookAPISQLite`
