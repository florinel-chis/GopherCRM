# Webhooks

A webhook tells another system that a record changed, so an integration no longer needs to poll
GopherCRM for new records. You register an HTTPS address (an endpoint) and pick the events it
should hear about; GopherCRM then sends a small signed request to that address after each such
change. This page describes the Settings → Webhooks screen first, then, under
[For the receiving system](#for-the-receiving-system), what the receiving system gets.
The routes, with their roles, parameters and answers, are in the
[API reference](../../../api/index.html).

## Who can manage webhooks

The screen is Settings → Webhooks (`/settings/webhooks`). It is shown to users whose role holds the
`webhooks.manage` permission: the admin role, unless a custom role is given it (see
[Roles and permissions](roles-and-permissions.md)). The same work can be done through the API, for
example registering an endpoint with `POST /api/v1/webhooks/endpoints`.

## The Webhooks screen

The screen lists the endpoints with their URL, events, status (Active or Inactive) and creation
date, and creates, edits, activates, deactivates and deletes them.

1. Click **Add endpoint**, enter the **URL** (an `https` address), tick one or more of the event
   types below and leave **Active** on to start sending at once.
2. Click **Create**. The signing secret of the new endpoint is shown once, right after it is created, with a
   copy button. It is not kept in the browser and neither the screen nor the API can show it
   again, so store it then in the receiving system.

The URL rules (see [Allowed addresses](#allowed-addresses-and-the-signing-secret)) are checked by
the server, and the form shows its refusal as is. Deleting an endpoint removes its delivery log
with it. Each row also has two buttons:

- **Send test ping** sends one test ping (see [Send a test ping](#send-a-test-ping)) and shows its
  outcome.
- **Rotate secret** asks for a confirmation, rotates the signing secret (see
  [Rotate the signing secret](#rotate-the-signing-secret)) and shows the new secret once in the
  same way, with the time until which the previous secret keeps verifying.

The **Delivery log** tab lists what was sent; see [Delivery log](#delivery-log).

## Event types

An endpoint subscribes to any of twenty-one event types:

| Event | Sent when |
|---|---|
| `lead.created` | a lead is created through the API, or a form creates a new lead (a repeat enquiry to an existing lead sends nothing) |
| `customer.created` | a customer is created through the API or by converting a lead |
| `ticket.created` | a ticket is created through the API, or a confirmed form submission opens one (a duplicate key sends nothing) |
| `deal.created` | a deal is created with `POST /api/v1/deals` |
| `task.created` | a task is created with `POST /api/v1/tasks`, or by an automation create-task step |
| `company.created` | a company is created with `POST /api/v1/companies` |
| `quote.created` | a quote is created with `POST /api/v1/quotes` |
| `lead.updated` | `PUT /api/v1/leads/{id}` changed at least one stored value of the lead, `POST /api/v1/leads/{id}/assign` changed its owner, `POST /api/v1/leads/{id}/convert` converted it (next to `customer.created`), `POST /api/v1/leads/bulk/status` changed its status (one event per lead), or an automation assign-owner step changed its owner |
| `customer.updated` | `PUT /api/v1/customers/{id}` changed at least one stored value of the customer |
| `ticket.updated` | `PUT /api/v1/tickets/{id}` changed at least one stored value of the ticket, `POST /api/v1/tickets/bulk/status` changed its status (one event per ticket), or an automation assign-owner step changed its assignee |
| `deal.updated` | `PUT /api/v1/deals/{id}` changed at least one stored value of the deal, `POST /api/v1/deals/{id}/stage` moved it to another stage, or an automation assign-owner step changed its owner |
| `task.updated` | `PUT /api/v1/tasks/{id}` changed at least one stored value of the task or its label set (order and repeats of `label_ids` do not count), or `POST /api/v1/tasks/bulk/status` changed its status (one event per task) |
| `company.updated` | `PUT /api/v1/companies/{id}` changed at least one stored value of the company |
| `quote.updated` | `PUT /api/v1/quotes/{id}` changed at least one stored value of the quote, a line was added (`POST /api/v1/quotes/{id}/lines`) or removed (`DELETE /api/v1/quotes/{id}/lines/{lineId}`), `PUT /api/v1/quotes/{id}/lines/{lineId}` changed at least one stored value of the line, `POST /api/v1/quotes/{id}/status` moved it to another status, or the client accepted or declined it through its response link (`POST /api/v1/quotes/public/response`) |
| `lead.deleted` | `DELETE /api/v1/leads/{id}`, and every erasure that reaches the lead: erasing the customer it was converted into, or that customer's login (`DELETE /api/v1/customers/{id}`, `DELETE /api/v1/users/{id}`) |
| `customer.deleted` | `DELETE /api/v1/customers/{id}`, and the erasure of a lead converted into the customer or of the customer's login (`DELETE /api/v1/leads/{id}` by an admin, `DELETE /api/v1/users/{id}`) |
| `ticket.deleted` | `DELETE /api/v1/tickets/{id}` |
| `deal.deleted` | `DELETE /api/v1/deals/{id}` |
| `task.deleted` | `DELETE /api/v1/tasks/{id}` (sent once: a delete that loses a race with another delete of the same task answers 404 and sends nothing) |
| `company.deleted` | `DELETE /api/v1/companies/{id}` (the only event it sends: the leads, customers and deals it unlinks get no updated event) |
| `quote.deleted` | `DELETE /api/v1/quotes/{id}` |

An update that sends only the values already stored answers 200 and sends nothing, and assigning
a lead to its current owner sends nothing either, nor does moving a deal to the stage it is already
in. The updated events come from the `PUT` routes in the table (`PUT /api/v1/tasks/{id}` also when only
the label set changed), lead assignment and conversion, the deal stage endpoint, the quote line
endpoints, the quote status endpoint and the client's quote response, the three bulk
status endpoints and the automation assign-owner step only: emailing a quote, the erasure of a
customer or lead that overwrites the text of the quotes linked to it, deleting or erasing a record,
assigning a customer, a ticket comment, a repeat form enquiry, the other automation steps, a
company delete that unlinks its leads, customers and deals (it sends `company.deleted` only), the erasure of a customer or lead that
overwrites the text of the deals linked to it, and a label delete, which removes the label from
every task carrying it, send no updated event. The event says that the record changed, not what
changed: fetch the record to see its current state.

A deleted event goes to every active endpoint subscribed to it, whether or not that endpoint
received an earlier event for the record: an endpoint subscribed to `lead.deleted` alone hears of
every deleted lead, and an endpoint registered after a record was created, or whose earlier
deliveries of it were pruned, still gets the deleted event. An erasure sends the same deleted event as a delete, with no reason and no personal
data: one event per lead or customer the erasure removed, cascades included, and none for a record
that had already been deleted.

## Send a test ping

**Send test ping** (or `POST /api/v1/webhooks/endpoints/:id/test`) sends one signed request to the
endpoint at once, active or not, and answers with the outcome: `{"event_id":"evt_…","outcome":"succeeded",
"http_status":200,"error_code":null}`, or `outcome` `failed` with the error code of a delivery
(`http_status`, `redirect`, `timeout`, `connection_failed`, `destination_refused`) or
`secret_unavailable`, and `http_status` null when no answer arrived. What the request carries is
described under [What a delivery carries](#what-a-delivery-carries). It follows the same address
rules, 10-second timeout and no-redirect rule, is never retried, and is not written to the
delivery log. It also ignores a pause (see
[Pausing](#pausing)). `webhook.ping` is not an event type an endpoint can subscribe to.

## Rotate the signing secret

**Rotate secret** asks for a confirmation and then calls
`POST /api/v1/webhooks/endpoints/:id/rotate-secret`. It generates a new secret and answers with the
endpoint, the new `secret` (shown this once) and `previous_secret_expires_at`, 24 hours from now.
Until then the replaced secret keeps signing next to the new one: every delivery, retry,
redelivery and test ping carries two signatures (see
[Verifying the signature](#verifying-the-signature)), so the receiving system can switch to the new
secret at any moment in that window without refusing a request. From `previous_secret_expires_at`
on only the new signature is sent, and the next delivery clears the replaced secret. A second rotation inside the window replaces the previous
secret: at most two secrets ever sign. Rotation works on an inactive endpoint and never reads the
stored secrets, so it also repairs an endpoint whose secret became unreadable (see
[Allowed addresses](#allowed-addresses-and-the-signing-secret)).

## Delivery log

The **Delivery log** tab of Settings → Webhooks lists each event per endpoint, newest first, with
its event type, record id, status, attempts, last attempt time and last response code, and
filters by endpoint, status and event type. `GET /api/v1/webhooks/deliveries` returns the same rows
(status `pending`, `sending`, `succeeded` or `failed`) and adds the error code and the next
attempt. Finished rows are deleted 30 days after their last change.

Each `succeeded` or `failed` row has a **Redeliver** button, which calls
`POST /api/v1/webhooks/deliveries/:id/redeliver` and reloads the log. The same row goes back to
`pending` with 0 attempts, due at once, and keeps its event id (`webhook-id`), so a receiving
system that deduplicates sees a repeat. A `pending` or `sending` delivery, or one whose endpoint is
deleted or inactive, is refused with 409; the screen shows the refusal's message.

## Pausing

An endpoint whose sent requests fail 3 times in a row is paused: the sender skips all its events
for 1 minute, then 2, 4, … up to 1 hour after each further failure, and the first 2xx answer ends
the pause. Neither a redelivery nor a change to the endpoint ends a pause early, and the test ping
ignores it.

The Webhooks screen does not show whether an endpoint is paused. Read it from
`GET /api/v1/webhooks/endpoints/{id}`, which returns `consecutive_failures` (the failures in a row)
and `paused_until` (the end of the current pause).

## For the receiving system

This part is for whoever builds or runs the system that receives the requests: what each request
carries, how it is delivered and retried, how to check its signature, and which addresses can
receive it.

### What a delivery carries

The body is exactly
`{"id":"evt_<32 hex>","type":"lead.created","occurred_at":"2026-11-20T09:30:00.123Z","record_id":42}`:
no name, address or other personal data. The receiving system fetches the record through the API
with its own credentials; visibility and erasure apply at that moment, so nothing personal leaves
after a person is erased.

A test ping's body is `{"id":"evt_<32 hex>","type":"webhook.ping","occurred_at":"…"}`, without
`record_id`, signed like a delivery.

### Delivery and retries

The event row is written in the same transaction as the record, so a create, an update or a
delete that rolls back sends nothing. A background sender delivers committed events about every
5 seconds as a `POST` with `Content-Type: application/json` and
`User-Agent: GopherCRM-Webhooks/1`. Any 2xx answer is success. A non-2xx answer, a redirect (never
followed), a timeout (10 seconds), a connection failure or a refused destination is retried after
1, 2, 4, … minutes (at most 6 hours apart) until 10 attempts have been made (about 8.5 hours); the
event is then marked failed. Delivery is at least once. A sender works on several endpoints at a
time but sends one endpoint's events one at a time, oldest first, so a slow endpoint does not hold
up the others. A retry or a second replica can still deliver an endpoint's events out of order:
deduplicate on `webhook-id` and do not rely on order.

### Verifying the signature

Signatures follow the Standard Webhooks scheme. Each request carries `webhook-id` (the event id),
`webhook-timestamp` (Unix seconds of this attempt; a retry is re-signed) and `webhook-signature`:
one `v1,<base64>` value, or two space-separated ones (`v1,<new> v1,<previous>`) during a rotation
window. Recompute it with your secret and accept the request when any value matches; the example
below does:

```python
import base64, hashlib, hmac, time

def verify(secret, headers, body: bytes, tolerance=300):
    key = base64.b64decode(secret.removeprefix("whsec_"))
    msg_id, ts = headers["webhook-id"], headers["webhook-timestamp"]
    if abs(time.time() - int(ts)) > tolerance:      # refuse replays older than 5 minutes
        return False
    signed = f"{msg_id}.{ts}.".encode() + body
    expected = base64.b64encode(hmac.new(key, signed, hashlib.sha256).digest()).decode()
    return any(hmac.compare_digest(expected, s.split(",", 1)[1])
               for s in headers["webhook-signature"].split() if s.startswith("v1,"))
```

### Allowed addresses and the signing secret

The URL must be `https`, at most 2048 characters, without credentials, query string or fragment.
Every connection resolves the host again and refuses private, loopback, CGNAT, unique-local,
link-local, metadata, multicast and unspecified addresses; proxy variables are ignored. The AEO
egress allowlists do not apply to webhooks.

The secret is stored encrypted with `API_KEY_SECRET` (or `JWT_SECRET`). After that secret
changes, the sender holds the deliveries of every endpoint whose secret it can no longer open:
they stay `pending` with their attempts unchanged, and go out once the secret can be read again,
after a restart with the previous key or after the endpoint's secret is rotated. Meanwhile the
server's health check reports the webhook sender as paused in the `webhooks` field under `data`
(see the [health check](../../SERVER_PROVISIONING.md#health-check)), and one error line per held
endpoint is logged.
