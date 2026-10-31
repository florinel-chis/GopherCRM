# GopherCRM Use Cases

Starting points for teams deciding whether GopherCRM fits their business, and what they would have
to configure or build first. Each recipe describes what the code does today, with the repository
path that does it, and lists the gaps that stand in the way, with the issue that tracks each one.
For running your own copy see [FORKING.md](FORKING.md); for adding code see
[EXTENDING.md](EXTENDING.md); for the full feature and test matrix see [FEATURES.md](FEATURES.md).

Every recipe was checked against `main` at `4dfe404`. When the code moves, re-check the cited path
before relying on a claim.

## How to read a recipe

Each recipe has the same parts:

- **Fit**: *good* (the flow works end to end with configuration only), *moderate* (it works, but
  one routing or data assumption breaks for part of the business), or *partial* (a usable subset
  works, and the rest needs code).
- **Existing flow**: what happens, in order, and where in the code it happens.
- **Setup**: configuration and data you provide. Nothing in this step changes code.
- **Gaps**: what is missing, with the issue that tracks it. Every gap has an issue; scope the
  project has decided against is listed under *Do not promise* instead.
- **Do not promise**: what a client or manager might assume the system does, but it does not.

When a gap means code changes, do not guess which CI profile the change will run. Run the planner
on the real diff:

```bash
python3 automation/langflow/gophercrm_runner/validation.py --base "$(git rev-parse origin/main)" --head "$(git rev-parse HEAD)"
```

A change to shared, schema, dependency or API-contract paths falls back to the `full` profile
(`docs/VALIDATION_RELEASE.md`).

## Shared caveats

These caveats apply to every recipe that takes in leads through forms.

- **Repeat enquiries do not create a new lead.** The newest live lead with the same email gets a
  note appended and a `lead_updated` automation event. Its owner, `source` and status stay as they
  were (`applySubmissionLead`, `internal/service/form_service.go:1073-1135`). Matching is
  installation-wide, not per form (`GetLatestByEmail`,
  `internal/repository/lead_repository.go:300`). Automation rules that should act on every enquiry
  must listen to `lead_updated` as well as `lead_created`, or they miss the repeats. A rule that
  routes on `source` sees the form that created the lead, not the form just submitted.
- **Form mail is best effort.** Confirmation, follow-up and team notification mail is sent after
  the submission is stored. A delivery failure is logged and swallowed, with no retry
  (`send`, `internal/service/form_service.go:1284-1294`). Without `SMTP_HOST`, every send
  "succeeds" into a redacted log line (`docs/ROADMAP.md`, "Production SMTP"). Tracked in #216.
- **Double opt-in confirms an email address. It does not record consent.** A form with consent
  text refuses a submission without the consent flag
  (`internal/service/form_service.go:763-765`). The submission row does not store the consent text
  or its version (`FormSubmission`, `internal/models/form.go:382`). Keep your own record of what
  the consent text said and when it changed.
- **Spam and unconfirmed submissions are kept forever.** They hold visitor values and IP addresses,
  and they sit outside lead erasure (`docs/DEVELOPMENT.md:873`; `docs/ROADMAP.md`, "Forms module —
  deliberate v1 cuts"). Decide on a purge policy and run it by hand until #217 (retention purge)
  and #214 (erasure of unlinked submissions) land.
- **Behind a reverse proxy or CDN, set `TRUSTED_PROXIES`.** The rate limiter keys on the client
  IP, and the stored submission IP comes from the same source. With no trusted proxies, every
  visitor shares the proxy's address and one rate-limit bucket.
- **A request cancelled before its commit writes nothing; one cancelled after it does not undo
  anything.** Public form submit and confirm, lead conversion and bulk status updates run on the
  HTTP request's context (`docs/DEVELOPMENT.md:166-170`, #194). A visitor who closes the tab while
  the transaction is still open leaves no submission behind and does not spend a confirmation
  token. The form transaction commits before the notification and follow-up mail are sent and
  before the response is written (`storeReceivedSubmission` and `ConfirmSubmission`,
  `internal/service/form_service.go:856-881,950-987`), so a disconnect after the commit leaves the
  submission stored and the token spent. A client that sees no response cannot conclude that
  nothing was written.
- **Conversion follows the administrator's rules.** Converting a lead to a customer requires its
  status to be in `leads.conversion.allowed_statuses`. The seeded value is `contacted` and
  `qualified` (`internal/models/configuration.go:267-280`), so a fresh form lead (status `new`)
  must be worked before it can be converted. `leads.conversion.require_notes` can require notes,
  and `leads.conversion.auto_assign_owner` (on by default) makes the lead's owner the customer's
  account manager (`ConvertToCustomer`, `internal/service/lead_service.go:578`). The UI offers
  Convert only on the allowed statuses (#192). A lead without an email cannot be converted (400),
  and one whose email already belongs to a customer gets a 409 (`internal/handler/lead_handler.go:549-553`).

## 1. Small agency: website forms to leads

**Fit: good.**

An agency with a handful of salespeople takes enquiries from its own website, works them as leads,
and turns some into deals and quotes.

### Existing flow

1. An admin builds a form: fields (exactly one email field, named `email`), consent text, notify
   emails, a default lead owner, allowed origins, double opt-in, and a follow-up mail that can carry
   a content link (`Form`, `internal/models/form.go:151-192`; screens in `gocrm-ui/src/pages/forms/`).
   The default owner must be an active admin or sales user (`checkLeadOwner`,
   `internal/service/form_service.go:464`).
2. The site embeds the snippet served from `GET /api/v1/forms/public/embed.js`
   (`internal/handler/form_public_routes.go:26`; usage comment at the top of
   `internal/handler/assets/form_embed.js`). The alternatives are the hosted page at
   `/api/v1/forms/public/:key/view` (`form_public_routes.go:30`) or a native POST. When the form
   has an allowlist and you use the hosted page, the CRM's own host must be in the allowed origins,
   because the hosted page submits from the CRM origin (`requestOrigin`,
   `internal/handler/form_public_handler.go:250-257`; `originAllowed`,
   `internal/service/form_service.go:588`). Test both embed paths from a real browser.
3. Spam layers run in a fixed order: validation, then the HMAC time trap (3 seconds to 24 hours),
   origin, honeypot, and optional reCAPTCHA v3 (`SubmitPublic`,
   `internal/service/form_service.go:642-703`; `internal/forms/`). A spam verdict gets the same
   response as a genuine submission and is stored with `status=spam`.
4. With double opt-in, `GET /forms/public/confirm` renders a button and only the POST spends the
   token. The token claim, the lead write and the submission update are one transaction, and mail
   goes out after the commit (`ConfirmSubmission`, `internal/service/form_service.go:950`;
   `internal/service/form_confirmation_regression_test.go`).
5. A new lead gets `source` = the form name and `owner` = the form's default owner. A repeat gets
   a note instead (see the shared caveats). Sales users see only the leads they own; admins see
   every lead (`CanViewLead`, `internal/service/lead_visibility.go:23`). A form that creates
   leads must have a default owner: `ValidateDefinition` refuses `create_lead` without one
   (`internal/models/form.go:273-275`, called from `internal/service/form_service.go:455`). A form
   with lead creation turned off may have no owner; its submissions are stored and no lead is
   written (`internal/service/form_service.go:867-869`).
6. Automation reacts to `lead_created` (and `lead_updated` for repeats). Typical steps: a condition
   on `source`, `create_task`, `notify`, `wait`, a condition `status == new`, and a staff
   `send_email` (event and step names in `internal/models/automation.go:31-59`; condition fields in
   `internal/service/automation_condition.go:43-50`). Dry-run a rule before enabling it
   (`internal/service/automation_dry_run.go`).
7. Conversion to a customer, then a deal and a quote (`internal/service/lead_service.go`,
   `internal/service/deal_service.go`, `internal/service/quote_service.go`).
8. Optionally, AEO tracking for the agency's **own** brand (see recipe 3).

### Setup

1. Deploy with TLS in front and replace every secret ([FORKING.md](FORKING.md)). The example
   `JWT_SECRET` in `.env.example` passes the startup check, so a copied example runs with a public
   secret until #210 lands.
2. Set `SMTP_*`, `PUBLIC_BASE_URL` (confirmation links), `APP_BASE_URL` (password-reset links),
   `TRUSTED_PROXIES` and `CORS_ALLOWED_ORIGINS`. The shipped Compose files do not forward SMTP,
   `PUBLIC_BASE_URL`, automation or reCAPTCHA variables, and they hard-code localhost URLs
   (`docker-compose.yml:54-60`, `docker-compose.sqlite.yml:37-43`). If you deploy with Compose,
   write an override until #222 ships one ([DOCKER.md](DOCKER.md)).
3. Create the first admin with `create-admin` (`cmd/create-admin`), then create sales users from
   the admin UI.
4. Create one form per site with allowed origins, notify emails, a default owner and double
   opt-in. Embed it and submit once from a real browser.
5. Write rules for created and updated leads, dry-run them, then enable them. The worker starts on
   every instance by default (`AUTOMATION_ENABLED`, `internal/config/config.go:322`) and assumes
   one replica (`docs/ROADMAP.md`, "Multi-replica leases"). If you run more than one API instance,
   set `AUTOMATION_ENABLED=false` on all but one.
6. Decide whether `contacted` and `qualified` are the right conversion statuses for your team, and
   change `leads.conversion.*` in Settings if not.

### Running it for clients

If the agency runs GopherCRM on behalf of clients, give each client its own installation: a
separate Compose project, host port, volume and secrets. The SQLite Compose file starts two
containers (backend and UI) with a fixed project name, port and subnet
(`docker-compose.sqlite.yml`), so a second client on the same host needs its own copies of those
values. One shared installation does not work for several clients, for two reasons:

- lead email matching is installation-wide, so enquiries from different clients' sites merge into
  one lead (`internal/repository/lead_repository.go:300`);
- AEO tracks exactly one brand per installation (`AEOProfile`, `internal/models/aeo.go:28-31`).

### Gaps

- Reassigning a lead from the lead list: the frontend client calls `POST /leads/:id/assign`
  (`gocrm-ui/src/api/endpoints/leads.ts:137`), which is not routed (`internal/handler/routes.go:25-35`).
  Tracked in #185. The working paths today are `owner_id` on a lead update (only an admin may
  reassign, `internal/handler/lead_handler.go:363`) and the automation `assign_owner` step.
- Attribution: UTM parameters land in lead notes. Do not map them into `source`, which automation
  routes on. Separate attribution fields are #220.
- No nurture email to leads: `send_email` reaches staff users only (`docs/ROADMAP.md`, "Email to
  leads and customers"). Tracked in #238.
- Form mail retry: #216. Submission retention purge: #217.
- Branding: mail subjects are hard-coded (`internal/mailer/smtp.go:84`,
  `internal/service/quote_email.go:12-15`), and the `general.company_name` and
  `ui.theme.primary_color` settings have no consumer in the UI
  (`gocrm-ui/src/contexts/ConfigurationContext.tsx:92-93`). Tracked in #221.
- AEO export or client reporting: #244.

### Do not promise

- a multi-client portal, or client logins that show a client "their" leads;
- nurture campaigns;
- AEO as a ranking or traffic measure: it samples answers and matches brand mentions literally
  (`DetectMentions`, `internal/aeo/analysis.go:42`);
- client reports built with `npm run screenshots`: the AEO screenshot suite serves fixed fictional
  fixtures, not your data (`gocrm-ui/e2e/screenshots/10-aeo.spec.ts:11-15`);
- "assign from the lead list" (#185).

## 2. Consultancy or professional services

**Fit: good.**

A firm that sells engagements: an enquiry becomes a qualified opportunity, a proposal and a won or
lost deal.

### Existing flow

1. An enquiry form creates a lead (recipe 1).
2. The lead is worked to `contacted` or `qualified`, then converted under the
   `leads.conversion.*` rules (shared caveats). If the lead links a company record, the link carries
   over to the customer when that company is still live; conversion does not create a company
   (`internal/service/lead_service.go:559-569`). Companies are created and linked separately, with
   normalised, unique domains (`normalizeCompany`, `internal/service/company_service.go:204`).
3. A deal moves through five stages (`qualification`, `proposal`, `negotiation`, `won`, `lost`),
   with a stage-history row per change and a `lost_reason` kept while lost
   (`internal/service/deal_service.go`, `internal/models/deal_pipeline.go`).
4. A quote is built from catalogue products standing in for service packages, printed from the
   browser (`gocrm-ui/src/pages/quotes/QuotePrint.tsx`) or emailed as plain text
   (`internal/service/quote_email.go`).
5. A `deal_stage_changed` rule creates a follow-up task (`internal/models/automation.go:36`).

### Setup

- Set `deals.default_currency` in Settings (`internal/models/configuration.go:344`).
- Create the products (admin only) and the sales users. Configure SMTP for quote email.
- Review the pipeline on the dashboard (`/` in the browser) and the deal board (`/deals/board`,
  `gocrm-ui/src/routes/index.tsx:53,161`), both admin and sales. An integration reads the same
  totals from the authenticated API at `GET /api/v1/dashboard/pipeline`
  (`internal/handler/routes.go:219`).
- The quote email subject reads "Quote Q-… from GopherCRM" (`internal/service/quote_email.go:12-15`).
  Changing it is a code edit until #221 lands.

**Variant: sponsorship or partnership sales.** The same objects work: the partner organisation is a
company, each package is a product, each proposal is a deal with a quote. Recurring gifts and
renewals are not modelled.

### Gaps

- Quote limits, which apply to every quote recipe: no tax lines; integer quantities (1 to
  1,000,000, `internal/models/quote.go:203,280`); staff record acceptance, and there is no client
  accept link; acceptance does not move the deal; expiry is a manual status (acceptance is refused
  once `valid_until` has passed, `internal/models/quote.go:88-91`); no PDF, only browser print
  (`docs/FEATURES.md`, section 4d). Tracked in #233.
- Tasks have no `deal_id`. A task created by a deal rule links to the deal's customer, or its lead
  (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts"). Tracked in #240.
- The customer pages show a fabricated `total_revenue` of zero: #186.
- Sales users can read tickets, but the navigation item is hidden from them (`docs/ROADMAP.md`,
  "Sales ticket navigation"). Tracked in #241.

### Do not promise

Invoicing, e-signature, client accept links, timesheets, or fractional hours on quote lines.

## 3. Product or SaaS company: inbound capture plus single-brand AEO monitoring

**Fit: good.**

A software company captures demo requests and gated-content downloads, and tracks how answer
engines describe its product against competitors.

### Existing flow

- **Demo and gated-content forms.** The follow-up mail replaces `{content_link}` with the form's
  content URL, and is sent only when the form has a follow-up subject (`sendFollowUpMail`,
  `internal/service/form_service.go:1242-1254`). With double opt-in, the follow-up goes out after
  confirmation, so the link reaches only a confirmed address.
- **Routing.** Automation conditions can test a lead's `source`, `classification`, `company`,
  `status` and `owner_id` (`internal/service/automation_condition.go:43-50`).
- **AEO.** One brand profile with aliases, owned domains and competitors
  (`internal/models/aeo.go:28-31`). Prompts can be generated by the engine set in
  `integration.aeo.generation_engine` (`internal/models/configuration.go:407`). Runs go to
  Anthropic, OpenAI, Gemini, Kimi, Perplexity or a custom OpenAI-compatible engine, each
  restricted by the egress policy (`internal/aeo/`, `docs/AEO_SECURITY.md`). The dashboard shows
  share of voice and citations (`gocrm-ui/src/pages/aeo/AEODashboard.tsx`). AEO read routes are
  open to admin, sales and support; writes to admin and sales (`internal/handler/aeo_routes.go:17-18`).

### Setup

- Enter provider keys in Settings > AEO. They are encrypted at rest (`internal/utils/secretbox.go`)
  and resolved before the environment fallback (`internal/service/aeo_credentials.go`). Rotating
  `API_KEY_SECRET` (or `JWT_SECRET`, which it falls back to) orphans them
  (`docs/ROADMAP.md`, "Sensitive settings — follow-ups").
- Review the scheduler: `AEO_SCHEDULE_ENABLED` defaults to `true` and `AEO_SCHEDULE_HOUR` to 6
  (`internal/config/config.go:310-311`). Scheduled runs spend provider credit.
- `AEO_QUERY_TIMEOUT_SECONDS` (default 60) needs raising for a slow self-hosted engine. A model
  server on your LAN needs an `AEO_EGRESS_LOCAL_ORIGINS` entry (`docs/AEO_SECURITY.md`).
- Review the default model IDs (`internal/config/config.go:290-308`). Pinned IDs get retired, so
  prefer rolling aliases where the provider offers them.
- Add reCAPTCHA keys if the forms attract spam (`RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY`).

### Gaps

- Lead `external_id` is stored (`internal/models/lead.go:44`), and `LeadService.GetByExternalID`
  exists (`internal/service/lead_service.go:143`), but no route calls it: #215.
- No AEO export or alerting: #244.
- No nurture email (recipe 1).

### Do not promise

That sales can be attributed to AEO, that AEO improves rankings, usage or billing sync from the
product, or tenant isolation for the SaaS company's own customers.

## 4. Customer issue register for a small IT or services firm

**Fit: partial.**

Staff record customer issues taken by phone or email, assign them to technicians, and keep the
history. This is a staff-maintained register, not a shared helpdesk.

### Existing flow

- Companies with normalised domains; customers linked to a company and to an account manager.
- Staff create tickets (support and admin, `internal/handler/ticket_handler.go:87`), assign them,
  write internal notes or public comments, and record a resolution
  (`internal/service/ticket_service.go`). A ticket has a title, description, status, priority,
  customer, assignee and resolution, and nothing else (`Ticket`, `internal/models/ticket.go`).
- A customer user can read its own ticket at `GET /tickets/:id`, with internal comments removed
  (`internal/handler/ticket_handler.go:385-405`), and `TicketDetail` renders it read-only
  (`gocrm-ui/src/pages/tickets/TicketDetail.tsx:127-135`). `GET /tickets` and `GET /tickets/my`
  refuse customers (`ticket_handler.go:153,316`), but `GET /customers/:id/tickets` lists the
  tickets of the customer record linked to the caller's account and refuses any other id
  (`internal/handler/routes.go:148`, `ticket_handler.go:250-260`; tested in
  `internal/integration/ticket_visibility_integration_test.go:560`). The UI has no customer
  ticket navigation, create form or comment form.
- Tasks with labels (`internal/service/task_service.go`, `internal/service/label_service.go`).
- Bulk status updates, at most 100 ids, all or nothing (`internal/service/bulk_status_service.go`).
- The dashboard's recent tickets are narrowed to what the caller may see.

### Setup

- Seed companies and customers with a script against the REST API. There is no import screen.
- Assign tickets deliberately. A ticket created without `assigned_to_id` is assigned to the
  user who created it (`internal/handler/ticket_handler.go:107-110`), so an admin or an
  integration key that omits the field becomes the assignee. The seeded
  `tickets.auto_assign_support` setting has no effect (`internal/models/configuration.go:354`; no
  code reads it). Tracked in #224.
- Train staff on the difference between internal notes and public comments.
- An integration that creates both customers and tickets **with one identity** must use an admin
  API key: customer create is admin and sales (`internal/handler/routes.go:40`), ticket create is
  support and admin (`internal/handler/ticket_handler.go:87`), and API keys carry their owner's
  full role with no scopes (`APIKey`, `internal/models/apikey.go:5`). Two identities also work: a
  sales key creates the customer and a support key creates the ticket. The support key can then
  read only tickets assigned to its own user (`ticket_handler.go:195`), which is the default when
  it omits `assigned_to_id`; a ticket it assigns to a technician drops out of its reads. A
  least-privilege credential is tracked in #211.

### Gaps before a shared desk

- Support users see only tickets assigned to them, in the list and the detail
  (`internal/handler/ticket_handler.go:195`). There is no unassigned queue: #218.
- Status and priority filters run client-side over the current page (`docs/FEATURES.md`, 5.5):
  #218.
- No inbound mail, and no reply email to the customer: #219.
- No intake path from a form or an external system that is safe to retry: a ticket has no
  submission or external key, so idempotent import is a schema change (#213). Mapping customers
  to an external system's ids is #212.
- Ticket title, description and resolution survive erasure, as business records
  (`docs/DEVELOPMENT.md:870`). Whether complaint text should be scrubbed is #214.
- No ticket automation triggers (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1
  cuts"): #242.
- No SLA fields and no attachments on tickets: #243.

### Do not promise

A helpdesk, SLAs, omnichannel support, or a customer portal.

## 5. Quote-led B2B supplier

**Fit: partial.**

A supplier that answers requests for quotation from business customers.

- **Flow:** companies, contacts, a product catalogue, quote lines that copy the product price and
  allow an override and a percentage discount per line, and pipeline totals per currency
  (`internal/models/product.go`, `internal/models/quote.go`, `internal/models/deal_pipeline.go`).
- **Gaps:**
  - No VAT or tax lines (`docs/FEATURES.md`, 4d.3), which most EU business-to-business quotes need,
    and integer quantities only (`internal/models/quote.go:203`). Both are part of #233,
    with the other quote limits from recipe 2.
  - Product SKUs are indexed but not unique (`internal/models/product.go:19`), so they are not safe
    as a key into an ERP: #239.
- **Do not promise:** an order, a delivery date, an inventory commitment, or a stock, order or ERP
  handoff.

## 6. Training provider

**Fit: moderate.**

A provider that runs courses for individuals and corporate groups.

- **Flow:** one form per course; the syllabus link goes out in the follow-up mail; an automation
  rule creates a callback task; a corporate group becomes a company, a deal and a per-seat quote.
- **Gaps:**
  - A repeat enquiry keeps the lead's original `source` (shared caveats), so routing by course
    breaks for anyone who enquires about a second course. A form-finalised event that carries the
    form and submission ids is the first change needed: #220.
- **Limit:** automation schedules run daily in UTC (`internal/models/automation.go:12`).
- **Do not promise:** sessions, capacity, booking or attendance tracking.

## 7. Poor fits

Each of these would need a different core model, not a recipe.

| Business | Why it does not fit |
|---|---|
| Field-service dispatch | No dispatch calendar, no offline mode, no attachments, and tasks have no `deal_id` (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts") |
| Recruiting | No attachments for CVs, and the pipeline links one deal to one customer, where recruiting needs many candidates per opening |
| Nonprofit donor CRM | No recurring gifts, campaigns or nurture email to contacts |
| Multi-tenant reseller hosting | No tenant column; settings and the AEO profile are installation-wide singletons (`internal/models/aeo.go:28-31`) |
| Regulated health records | No field-level encryption, no read audit, and an issued access token cannot be revoked before it expires (`docs/ROADMAP.md`, "Access-token blocklist") |
| An e-commerce order system inside the CRM | Products and quotes are sales tools; the shop platform stays the system of record for orders, stock and payment |

## 8. Cross-cutting gaps

Gaps that more than one recipe runs into. Check `gh issue list` for the current state before
planning around an issue number.

| Gap | Recipes | Evidence | Issue |
|---|---|---|---|
| Lead reassignment from the lead list (`POST /leads/:id/assign` not routed) | 1, 2 | `gocrm-ui/src/api/endpoints/leads.ts:137`; `internal/handler/routes.go:25-35` | #185 |
| Lead attribution fields, and an event per finalised form submission | 1, 3, 6 | `internal/service/form_service.go:1073-1135` | #220 |
| Form mail failures are swallowed, with no delivery status or retry | 1, 3, 6 | `internal/service/form_service.go:1284-1294` | #216 |
| Retention purge of spam and unconfirmed submissions | 1, 3, 6 | `docs/ROADMAP.md`, "Forms module — deliberate v1 cuts" | #217 |
| Erasure of unlinked submissions and ticket free text | 1, 4 | `docs/DEVELOPMENT.md:870,873` | #214 |
| One product-name source for UI, mail subjects and Docker builds | 1, 2 | `internal/mailer/smtp.go:84`; `internal/service/quote_email.go:12-15`; `gocrm-ui/src/contexts/ConfigurationContext.tsx:92-93` | #221 |
| Production Compose override (SMTP, base URLs, automation, reCAPTCHA, AEO schedule, CORS) | all | `docker-compose.yml:54-60`; `docker-compose.sqlite.yml:37-43` | #222 |
| The example JWT secret passes the startup check | all | `.env.example`; `internal/config/config.go` | #210 |
| Least-privilege credential for integrations (API keys carry the owner's full role) | 3, 4 | `internal/models/apikey.go:5` | #211 |
| External identity mapping for customers, as a `(source, scope, external_id)` table rather than columns, because customer email is globally unique | 3, 4 | `internal/models/customer.go:9` | #212 |
| Retry-safe intake from forms or external systems into tickets (schema change) | 4 | `internal/models/ticket.go` | #213 |
| Route the lead `external_id` lookup | 3 | `internal/service/lead_service.go:143` | #215 |
| Shared support queue and server-side ticket filters | 4 | `internal/handler/ticket_handler.go:195`; `docs/FEATURES.md` 5.5 | #218 |
| Customer-visible ticket replies by email | 4 | no ticket path sends mail | #219 |
| Seeded settings that have no effect (`tickets.auto_assign_support`, `security.session_timeout_hours`) | 4 | `internal/models/configuration.go:333,354` | #224 |
| Fabricated `total_revenue` on customers | 2, 5 | customer list and detail pages | #186 |
| A bulk status outcome row can stay `pending` after its write fails | 4 | `internal/service/bulk_status_service.go` | #196 |
| Nurture or customer email from automation | 1, 2, 3 | `docs/ROADMAP.md`, "Email to leads and customers" | #238 |
| More than one automation worker | 1 | `docs/ROADMAP.md`, "Multi-replica leases" | #237 |
| Tax lines, fractional quantities, client accept link, PDF quotes | 2, 5 | `internal/models/quote.go`; `docs/FEATURES.md` 4d | #233 |
| Unique product SKUs | 5 | `internal/models/product.go:19` | #239 |
| `deal_id` on tasks | 2 | `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #240 |
| Ticket navigation for sales users | 2 | `docs/ROADMAP.md`, "Sales ticket navigation" | #241 |
| Ticket automation triggers | 4 | `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #242 |
| Ticket SLA fields and attachments | 4 | `internal/models/ticket.go` | #243 |
| AEO export, alerting and client reporting | 1, 3 | `gocrm-ui/e2e/screenshots/10-aeo.spec.ts:11-15` (fixtures only) | #244 |
