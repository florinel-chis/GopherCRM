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
  were (`applySubmissionLead`, `internal/service/form_service.go:1156-1224`). Matching is
  installation-wide, not per form (`GetLatestByEmail`,
  `internal/repository/lead_repository.go:300`). A rule that routes on `source` sees the form that
  created the lead, not the form just submitted. To act on every enquiry, trigger on
  `lead_form_submitted`: it follows the `lead_created` or `lead_updated` of each finalised
  submission, carries the form and submission ids, can be narrowed with `trigger.form_id`, and lets
  conditions branch on that submission's own `utm_*` and `referrer_host` (#220).
- **Form mail failures are recorded, not hidden.** Confirmation, follow-up and each team notification
  get their own delivery row, sent after the submission is stored and stamped `sent`, `failed`,
  `log_only` (no `SMTP_HOST`) or `skipped` (`sendAndStamp`, `internal/service/form_mail_delivery.go:237-289`).
  Staff see the rows on the submission and in `GET /forms/deliveries`; admin and sales retry by hand.
- **Double opt-in confirms an email address. It does not record consent.** A form with consent
  text refuses a submission without the consent flag
  (`internal/service/form_service.go:819-821`). The submission row does not store the consent text
  or its version (`FormSubmission`, `internal/models/form.go:465`). Keep your own record of what
  the consent text said and when it changed.
- **Spam and unconfirmed submissions are kept until you set a retention.** They hold visitor values
  and IP addresses. Erasing a lead, customer or user scrubs the unlinked submissions whose `email`
  matches that person's address, in the same erasure (`docs/DEVELOPMENT.md:1153`); every other
  unlinked submission stays (`docs/DEVELOPMENT.md:1164`). Set `FORM_RETENTION_DAYS` (3 days at
  least, off by default) to remove spam and never-confirmed rows permanently at that age. The
  first boot with it set removes the existing backlog, so take a backup first if you may want
  those rows. Received and confirmed submissions are not purged. Until a separate frontend task
  merges, the form-delete dialogs still say "Collected submissions are retained", which is wrong
  for purged rows.
- **Behind a reverse proxy or CDN, set `TRUSTED_PROXIES`.** The rate limiter keys on the client
  IP, and the stored submission IP comes from the same source. With no trusted proxies, every
  visitor shares the proxy's address and one rate-limit bucket.
- **A request cancelled before its commit writes nothing; one cancelled after it does not undo
  anything.** Public form submit and confirm, lead conversion and bulk status updates run on the
  HTTP request's context (`docs/DEVELOPMENT.md:166-170`, #194). A visitor who closes the tab while
  the transaction is still open leaves no submission behind and does not spend a confirmation
  token. The form transaction commits before the notification and follow-up mail are sent and
  before the response is written (`storeReceivedSubmission` and `ConfirmSubmission`,
  `internal/service/form_service.go:913-946,1024-1068`), so a disconnect after the commit leaves the
  submission stored and the token spent. A client that sees no response cannot conclude that
  nothing was written.
- **Conversion follows the administrator's rules.** Converting a lead to a customer requires its
  status to be in `leads.conversion.allowed_statuses`. The seeded value is `contacted` and
  `qualified` (`internal/models/configuration.go:267-280`), so a fresh form lead (status `new`)
  must be worked before it can be converted. `leads.conversion.require_notes` can require notes,
  and `leads.conversion.auto_assign_owner` (on by default) makes the lead's owner the customer's
  account manager (`ConvertToCustomer`, `internal/service/lead_service.go:736`). The UI offers
  Convert only on the allowed statuses (#192). A lead without an email cannot be converted (400),
  and one whose email already belongs to a customer gets a 409 (`internal/handler/lead_handler.go:851-853`).

## 1. Small agency: website forms to leads

**Fit: good.**

An agency with a handful of salespeople takes enquiries from its own website, works them as leads,
and turns some into deals and quotes.

### Existing flow

1. An admin builds a form: fields (exactly one email field, named `email`), consent text, notify
   emails, a default lead owner, allowed origins, double opt-in, and a follow-up mail that can carry
   a content link (`Form`, `internal/models/form.go:172-276`; screens in `gocrm-ui/src/pages/forms/`).
   The default owner must be an active admin or sales user (`checkLeadOwner`,
   `internal/service/form_service.go:520`).
2. The site embeds the snippet served from `GET /api/v1/forms/public/embed.js`
   (`internal/handler/form_public_routes.go:26`; usage comment at the top of
   `internal/handler/assets/form_embed.js`). The alternatives are the hosted page at
   `/api/v1/forms/public/:key/view` (`form_public_routes.go:30`) or a native POST. When the form
   has an allowlist and you use the hosted page, the CRM's own host must be in the allowed origins,
   because the hosted page submits from the CRM origin (`requestOrigin`,
   `internal/handler/form_public_handler.go:250-257`; `originAllowed`,
   `internal/service/form_service.go:644`). Test both embed paths from a real browser.
3. Spam layers run in a fixed order: validation, then the HMAC time trap (3 seconds to 24 hours),
   origin, honeypot, and optional reCAPTCHA v3 (`SubmitPublic`,
   `internal/service/form_service.go:698-761`; `internal/forms/`). A spam verdict gets the same
   response as a genuine submission and is stored with `status=spam`.
4. With double opt-in, `GET /forms/public/confirm` renders a button and only the POST spends the
   token. The token claim, the lead write and the submission update are one transaction, and mail
   goes out after the commit (`ConfirmSubmission`, `internal/service/form_service.go:1024`;
   `internal/service/form_confirmation_regression_test.go`).
5. A new lead gets `source` = the form name and `owner` = the form's default owner. A repeat gets
   a note instead (see the shared caveats). Sales users see only the leads they own; admins see
   every lead (`CanViewLead`, `internal/service/lead_visibility.go:23`). A form that creates
   leads must have a default owner: `ValidateDefinition` refuses `create_lead` without one
   (`internal/models/form.go:356-358`, called from `internal/service/form_service.go:511`). A form
   with lead creation turned off may have no owner; its submissions are stored and no lead is
   written (`internal/service/form_service.go:928-935`).
6. Automation reacts to `lead_created` (and `lead_updated` for repeats), or to `lead_form_submitted`
   for every finalised enquiry, optionally narrowed to one form with `trigger.form_id`. Typical
   steps: a condition on `source` or on the enquiry's `utm_campaign`, `create_task`, `notify`,
   `wait`, a condition `status == new`, and a staff `send_email` (event and step names in
   `internal/models/automation.go:31-64`; condition fields in
   `internal/service/automation_condition.go:47-74`). Dry-run a rule before enabling it
   (`internal/service/automation_dry_run.go`).
7. Conversion to a customer, then a deal and a quote (`internal/service/lead_service.go`,
   `internal/service/deal_service.go`, `internal/service/quote_service.go`).
8. Optionally, AEO tracking for the agency's **own** brand (see recipe 3).

### Setup

1. Deploy with TLS in front and replace every secret ([FORKING.md](FORKING.md)). The example
   `JWT_SECRET` in `.env.example` is refused at startup, so a copied example does not start until
   the secret is replaced.
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
   every instance by default (`AUTOMATION_ENABLED`, `internal/config/config.go:363`) and assumes
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

- Reassigning a lead from the lead list: `POST /leads/:id/assign` is routed (an admin on any lead, a
  sales user on its own, #185) and `leadsApi.assignLead` calls it
  (`gocrm-ui/src/api/endpoints/leads.ts:137`), but no screen offers it yet. The API, `owner_id` on a
  lead update (admin only) and the automation `assign_owner` step are the working paths today.
- Attribution: each finalised enquiry stores its own `utm_source`, `utm_medium`, `utm_campaign`,
  `utm_term`, `utm_content` and `referrer_host` on its `form_submissions` row, apart from the lead's
  `source` (`FormSubmission`, `internal/models/form.go:503-524`). A `lead_form_submitted` rule,
  narrowed to one form with `trigger.form_id`, can branch on them. The values come from hidden
  `utm_*` fields or the `page_url` query; the embed script does not fill the hidden fields from the
  host page. The `utm_*` lines also still land in the lead's notes. Do not map them into `source`.
- No nurture email to leads: `send_email` reaches staff users only (`docs/ROADMAP.md`, "Email to
  leads and customers"). Tracked in #238.
- Form mail: `GET /forms/deliveries` lists failed, log-only and stuck mails; admin and sales retry
  one with `POST /forms/deliveries/{id}/retry`. Old spam and unconfirmed submissions: set
  `FORM_RETENTION_DAYS` (see the shared caveats).
- Branding: for signed-in users the UI shows the `general.company_name` setting as the product name
  (navigation drawer, printed quote) and uses a supported `ui.theme.primary_color` (hex, `rgb()`,
  `hsl()`) as the light theme's primary colour (`gocrm-ui/src/hooks/useProductName.ts`,
  `gocrm-ui/src/theme/BrandThemeProvider.tsx`, #291). The sign-in and registration pages show the
  build-time `VITE_APP_NAME` (else `GoCRM`). Mail subjects are still hard-coded
  (`internal/mailer/smtp.go:84`, `internal/service/quote_email.go:12-15`; #221), and the Docker UI
  image takes no `VITE_APP_NAME` build argument (#222).
- AEO export or client reporting: #244.

### Do not promise

- a multi-client portal, or client logins that show a client "their" leads;
- nurture campaigns;
- AEO as a ranking or traffic measure: it samples answers and matches brand mentions literally
  (`DetectMentions`, `internal/aeo/analysis.go:42`);
- client reports built with `npm run screenshots`: the AEO screenshot suite serves fixed fictional
  fixtures, not your data (`gocrm-ui/e2e/screenshots/10-aeo.spec.ts:11-15`);
- "assign from the lead list": no screen offers it.

## 2. Consultancy or professional services

**Fit: good.**

A firm that sells engagements: an enquiry becomes a qualified opportunity, a proposal and a won or
lost deal.

### Existing flow

1. An enquiry form creates a lead (recipe 1).
2. The lead is worked to `contacted` or `qualified`, then converted under the
   `leads.conversion.*` rules (shared caveats). If the lead links a company record, the link carries
   over to the customer when that company is still live; conversion does not create a company
   (`internal/service/lead_service.go:837-850`). Companies are created and linked separately, with
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
  (`internal/handler/routes.go:225`).
- The quote email subject reads "Quote Q-… from GopherCRM" (`internal/service/quote_email.go:12-15`).
  Changing it is a code edit until #221 lands. The printed quote's heading already shows the
  `general.company_name` setting (#291).

**Variant: sponsorship or partnership sales.** The same objects work: the partner organisation is a
company, each package is a product, each proposal is a deal with a quote. Recurring gifts and
renewals are not modelled.

### Gaps

- Quote limits, which apply to every quote recipe: no tax lines; integer quantities (1 to
  1,000,000, `internal/models/quote.go:203,280`); staff record acceptance, and there is no client
  accept link; acceptance does not move the deal; expiry is a manual status (acceptance is refused
  once `valid_until` has passed, `internal/models/quote.go:92-95`); no PDF, only browser print
  (`docs/FEATURES.md`, section 4d). Tracked in #233.
- A task's `deal_id` (set by a deal rule, through the API, or with the task form's deal picker by
  admin and sales) is shown on task detail and listed in the deal detail Tasks section from
  `GET /deals/{id}/tasks`. Tasks created by a deal rule before #240 are not backfilled
  (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts").

### Do not promise

Invoicing, e-signature, client accept links, timesheets, or fractional hours on quote lines.

## 3. Product or SaaS company: inbound capture plus single-brand AEO monitoring

**Fit: good.**

A software company captures demo requests and gated-content downloads, and tracks how answer
engines describe its product against competitors.

### Existing flow

- **Demo and gated-content forms.** The follow-up mail replaces `{content_link}` with the form's
  content URL, and is sent only when the form has a follow-up subject (`recordAcceptedMail` and
  `renderFormFollowUp`, `internal/service/form_mail_delivery.go:124-148,350-356`). With double
  opt-in, the follow-up goes out after confirmation, so the link reaches only a confirmed address.
- **Routing.** Automation conditions can test a lead's `source`, `classification`, `company`,
  `status` and `owner_id` (`internal/service/automation_condition.go:47-57`). A
  `lead_form_submitted` rule can also test the triggering submission's `utm_source`,
  `utm_medium`, `utm_campaign`, `utm_term`, `utm_content` and `referrer_host`
  (`internal/service/automation_condition.go:68-74`).
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
  (`internal/config/config.go:350-351`). Scheduled runs spend provider credit.
- `AEO_QUERY_TIMEOUT_SECONDS` (default 60) needs raising for a slow self-hosted engine. A model
  server on your LAN needs an `AEO_EGRESS_LOCAL_ORIGINS` entry (`docs/AEO_SECURITY.md`).
- Review the default model IDs (`internal/config/config.go:330-348`). Pinned IDs get retired, so
  prefer rolling aliases where the provider offers them.
- Add reCAPTCHA keys if the forms attract spam (`RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY`).

### Gaps

- Lead `external_id` is stored (`internal/models/lead.go:67`), and `LeadService.GetByExternalID`
  exists (`internal/service/lead_service.go:218`), but no route calls it: #215.
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
  (`internal/handler/routes.go:150`, `ticket_handler.go:250-260`; tested in
  `internal/integration/ticket_visibility_integration_test.go:560`). The UI has no customer
  ticket navigation, create form or comment form.
- Tasks with labels (`internal/service/task_service.go`, `internal/service/label_service.go`).
- Bulk status updates, at most 100 ids, all or nothing (`internal/service/bulk_status_service.go`).
- The dashboard's recent tickets are narrowed to what the caller may see.

### Setup

- Seed companies and customers with a script against the REST API. There is no import screen.
- Assign tickets deliberately. A ticket created without `assigned_to_id` is assigned to the
  user who created it (`internal/handler/ticket_handler.go:107-110`), so an admin or an
  integration key that omits the field becomes the assignee. Nothing distributes tickets among
  support users; the `tickets.auto_assign_support` setting that suggested otherwise was retired
  in #224.
- Train staff on the difference between internal notes and public comments.
- An integration that creates both customers and tickets **with one identity** must use an admin
  API key: customer create is admin and sales (`internal/handler/routes.go:42`), ticket create is
  support and admin (`internal/handler/ticket_handler.go:87`), and API keys carry their owner's
  full role with no scopes (`APIKey`, `internal/models/apikey.go:5`). Two identities also work: a
  sales key creates the customer and a support key creates the ticket. The support key can then
  read only tickets assigned to its own user (`ticket_handler.go:195`), which is the default when
  it omits `assigned_to_id`; a ticket it assigns to a technician drops out of its reads. A
  least-privilege credential is tracked in #211.

### Gaps before a shared desk

- Support users see only tickets assigned to them, in the list and the detail
  (`internal/handler/ticket_handler.go:195`). There is no unassigned or shared queue: #259.
- Ticket status and priority filters run server-side (#218, delivered): `GET /tickets` applies
  them before pagination, so totals are correct, and for support they only narrow the assigned
  tickets (`docs/FEATURES.md`, 5.5).
- No inbound mail, and no reply email to the customer: #219.
- No intake path from a form or an external system that is safe to retry: a ticket has no
  submission or external key, so idempotent import is a schema change (#213). Customers, unlike
  tickets, can be mapped to an external system's ids (delivered, #212): `POST
  /customers/{id}/external-ids` and the exact lookup `GET /customers?source=&scope=&external_id=`.
- Ticket title, description and resolution survive erasure, as business records
  (`docs/DEVELOPMENT.md:1161`). When they name the person, redact them through `PUT /tickets/{id}`
  before `DELETE /customers/{id}`, as the operator procedure for ticket text in
  `docs/DEVELOPMENT.md` (*Deleting personal data*) describes.
- No ticket automation triggers (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1
  cuts"): #242.
- Tickets now carry `due_at`, `first_responded_at` and a computed `overdue` flag (#278); there are
  still no attachments on tickets: #243.

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
- **Erasure:** erasing a customer or lead keeps its deals and quotes, links, totals and history,
  and overwrites their staff-typed text in the same transaction, whatever the quote status: deal
  title `[redacted]` with empty notes and lost reason, quote title `[redacted]` with empty notes,
  and each line's description reset to its product's name (#245; `docs/DEVELOPMENT.md`, *Deleting
  personal data*). Keep anything an Article 17(3) exemption requires outside those fields, for
  example in the accounting system.
- **Do not promise:** an order, a delivery date, an inventory commitment, or a stock, order or ERP
  handoff.

## 6. Training provider

**Fit: moderate.**

A provider that runs courses for individuals and corporate groups.

- **Flow:** one form per course; the syllabus link goes out in the follow-up mail; an automation
  rule creates a callback task; a corporate group becomes a company, a deal and a per-seat quote.
- **Gaps:**
  - A repeat enquiry keeps the lead's original `source` (shared caveats), so do not route by course
    on `source`. Trigger on `lead_form_submitted` with `trigger.form_id` set to the course's form
    instead: it starts for every finalised enquiry, a repeat included, and the run can branch on
    that enquiry's own `utm_*` values (#220).
- **Limit:** automation schedules run daily in UTC (`internal/models/automation.go:12`).
- **Do not promise:** sessions, capacity, booking or attendance tracking.

## 7. Poor fits

Each of these would need a different core model, not a recipe.

| Business | Why it does not fit |
|---|---|
| Field-service dispatch | No dispatch calendar, no offline mode and no attachments |
| Recruiting | No attachments for CVs, and the pipeline links one deal to one customer, where recruiting needs many candidates per opening |
| Nonprofit donor CRM | No recurring gifts, campaigns or nurture email to contacts |
| Multi-tenant reseller hosting | No tenant column; settings and the AEO profile are installation-wide singletons (`internal/models/aeo.go:28-31`) |
| Regulated health records | No field-level encryption, no read audit, and a single access token cannot be revoked on its own: only logout, a password change or a reset ends access tokens early, all of an account's at once (#236; `docs/ROADMAP.md`, "Access-token revocation beyond the session-ending events") |
| An e-commerce order system inside the CRM | Products and quotes are sales tools; the shop platform stays the system of record for orders, stock and payment |

## 8. Cross-cutting gaps

Gaps that more than one recipe runs into. Check `gh issue list` for the current state before
planning around an issue number.

| Gap | Recipes | Evidence | Issue |
|---|---|---|---|
| Lead reassignment from the lead list (no UI control; API routes exist) | 1, 2 | `gocrm-ui/src/api/endpoints/leads.ts:137` | — |
| Delivered: per-submission attribution columns, and `lead_form_submitted` per finalised submission with `trigger.form_id`. Still open: first- or last-touch attribution on the lead, and the embed script filling hidden `utm_*` fields | 1, 3, 6 | `internal/service/form_service.go:1156-1224`; `internal/models/form.go:503-524` | #220 |
| Form mail is retried by hand only: no automatic retry or backoff, and `sent` means accepted by the relay (no bounce confirmation) | 1, 3, 6 | `internal/service/form_mail_delivery.go:451-489` | — |
| Delivered: erasure scrubs unlinked submissions of the erased address; ticket free text is kept, behind a documented operator redaction procedure. Still open: unlinked submissions of other addresses stay until the retention purge | 1, 4 | `docs/DEVELOPMENT.md:1153,1161,1164` | #214 |
| Delivered: erasure redacts the deal and quote text of the erased customer or lead and deletes notifications about those deals. Still open: a task rendered from `{{deal.title}}` before the erasure keeps that text | 2, 5, 6 | `internal/repository/business_text_erasure.go` | #245 |
| Delivered: the UI takes its product name from one helper (stored `general.company_name`, else `VITE_APP_NAME`, else `GoCRM`) and the light theme's primary colour from `ui.theme.primary_color` (#291). Still open: mail subjects and the Docker `VITE_APP_NAME` build argument | 1, 2 | `internal/mailer/smtp.go:84`; `internal/service/quote_email.go:12-15`; `gocrm-ui/Dockerfile` | #221, #222 |
| Production Compose override (SMTP, base URLs, automation, reCAPTCHA, AEO schedule, CORS) | all | `docker-compose.yml:54-60`; `docker-compose.sqlite.yml:37-43` | #222 |
| Least-privilege credential for integrations (API keys carry the owner's full role) | 3, 4 | `internal/models/apikey.go:5` | #211 |
| Delivered: external identity mapping for customers, a `(source, scope, external_id)` table unique on the exact triple (customer email stays globally unique), written by admin and sales, resolved by an exact lookup on `GET /customers`, and deleted with the customer by every erasure path. Still open: no UI, and no mapping table for leads (they keep one external_id column), users, companies or deals | 3, 4 | `internal/models/customer_external_id.go`; `internal/handler/customer_external_id_handler.go` | #212 |
| Retry-safe intake from forms or external systems into tickets (schema change) | 4 | `internal/models/ticket.go` | #213 |
| Route the lead `external_id` lookup | 3 | `internal/service/lead_service.go:218` | #215 |
| Delivered: server-side ticket status and priority filters (#218). Still open: a shared or unassigned support queue | 4 | `internal/handler/ticket_handler.go:195`; `docs/FEATURES.md` 5.5 | #259 |
| Customer-visible ticket replies by email | 4 | no ticket path sends mail | #219 |
| Delivered: a bulk status batch writes `completed` inside its own transaction, and a row left `pending` (its batch never committed, its `failed` write timed out) is failed by a sweep at boot and every 15 minutes once it is an hour old. Still open: rows already `pending` before the upgrade are swept to `failed` although their batch may have committed (outcome unknown, not stored) | 4 | `internal/service/bulk_status_service.go`; `internal/service/bulk_operation_recovery_runtime.go`; `docs/DEVELOPMENT.md`, *Bulk endpoints* | #196 |
| Nurture or customer email from automation | 1, 2, 3 | `docs/ROADMAP.md`, "Email to leads and customers" | #238 |
| More than one automation worker | 1 | `docs/ROADMAP.md`, "Multi-replica leases" | #237 |
| Tax lines, fractional quantities, client accept link, PDF quotes | 2, 5 | `internal/models/quote.go`; `docs/FEATURES.md` 4d | #233 |
| Unique product SKUs | 5 | `internal/models/product.go:19` | #239 |
| Delivered: a task's deal link on the task form (admin and sales), task detail and deal detail. Still open: tasks created by earlier automation runs are not backfilled | 2 | `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #240 |
| Ticket automation triggers | 4 | `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #242 |
| Ticket attachments | 4 | `internal/models/ticket.go` | #243 |
| AEO export, alerting and client reporting | 1, 3 | `gocrm-ui/e2e/screenshots/10-aeo.spec.ts:11-15` (fixtures only) | #244 |
