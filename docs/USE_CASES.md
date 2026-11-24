# GopherCRM Use Cases

Starting points for teams deciding whether GopherCRM fits their business, and what they would have
to configure or build first. Each recipe describes what the code does today, with the repository
path that does it, and lists the gaps that stand in the way, with the issue that tracks each one.
For running your own copy see [FORKING.md](FORKING.md); for adding code see
[EXTENDING.md](EXTENDING.md); for the full feature and test matrix see [FEATURES.md](FEATURES.md).

Every recipe was last corrected against `main` at `98dabb04` (#733), and the cited paths and
symbols are checked by `scripts/ci/check-doc-citations.py`. When the code moves, re-check the cited
path before relying on a claim.

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
  were (`formService.applySubmissionLead` (`internal/service/form_lead_link.go`)). Matching is
  installation-wide, not per form
  (`leadRepository.GetLatestByEmail` (`internal/repository/lead_repository.go`)). A rule that routes on `source` sees the form that
  created the lead, not the form just submitted. To act on every enquiry, trigger on
  `lead_form_submitted`: it follows the `lead_created` or `lead_updated` of each finalised
  submission, carries the form and submission ids, can be narrowed with `trigger.form_id`, and lets
  conditions branch on that submission's own `utm_*` and `referrer_host` (#220).
- **Form mail failures are recorded, not hidden.** Confirmation, follow-up and each team notification
  get their own delivery row, sent after the submission is stored and stamped `sent`, `failed`,
  `log_only` (no `SMTP_HOST`) or `skipped` (`formService.sendAndStamp` (`internal/service/form_mail_delivery.go`)).
  Staff see the rows on the submission and in `GET /forms/deliveries`; admin and sales retry by hand.
- **Double opt-in confirms an email address. It does not record consent.** A form with consent
  text refuses a submission without the consent flag
  (`formService.validateValues` (`internal/service/form_validation.go`)). The submission row does not
  store the consent text or its version (`FormSubmission` (`internal/models/form.go`)). Keep your own record of what
  the consent text said and when it changed.
- **Spam and unconfirmed submissions are kept until you set a retention.** They hold visitor values
  and IP addresses. Erasing a lead, customer or user scrubs the unlinked submissions whose `email`
  matches that person's address, in the same erasure
  (`docs/DEV_ERASURE.md`, "Deleting personal data", `scrubUnlinkedSubmissions`); every other
  unlinked submission stays (`docs/DEV_ERASURE.md`, "matches no erased row"). Set `FORM_RETENTION_DAYS` (3 days at
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
  HTTP request's context (`docs/DEV_TRANSACTIONS.md`, "Request context (#107)", #194). A visitor who closes the tab while
  the transaction is still open leaves no submission behind and does not spend a confirmation
  token. The form transaction commits before the notification and follow-up mail are sent and
  before the response is written (`formService.storeReceivedSubmission` (`internal/service/form_public.go`) and
  `formService.ConfirmSubmission` (`internal/service/form_confirmation.go`)), so a disconnect after the commit leaves the
  submission stored and the token spent. A client that sees no response cannot conclude that
  nothing was written.
- **Conversion follows the administrator's rules.** Converting a lead to a customer requires its
  status to be in `leads.conversion.allowed_statuses`. The seeded value is `contacted` and
  `qualified`, from `DefaultConfigurations` (`internal/models/configuration.go`), so a fresh form lead (status `new`)
  must be worked before it can be converted. `leads.conversion.require_notes` can require notes,
  and `leads.conversion.auto_assign_owner` (on by default) makes the lead's owner the customer's
  account manager (`leadService.ConvertToCustomer` (`internal/service/lead_conversion.go`)). The UI offers
  Convert only on the allowed statuses (#192). A lead without an email cannot be converted (400),
  and one whose email already belongs to a customer gets a 409 (`LeadHandler.ConvertToCustomer` (`internal/handler/lead_handler.go`)).

## 1. Small agency: website forms to leads

**Fit: good.**

An agency with a handful of salespeople takes enquiries from its own website, works them as leads,
and turns some into deals and quotes.

### Existing flow

1. An admin builds a form: fields (exactly one email field, named `email`), consent text, notify
   emails, a default lead owner, allowed origins, double opt-in, and a follow-up mail that can carry
   a content link (`Form` (`internal/models/form.go`); screens in `gocrm-ui/src/pages/forms/`).
   The default owner must be an active admin or sales user
   (`formService.checkLeadOwner` (`internal/service/form_admin.go`)). Under **Submission action** the admin chooses what a
   submission creates: a lead (this recipe), a support ticket for the live customer with the
   submitted address (#213, #335), or nothing. A ticket form requires double opt-in and has no lead
   owner; the builder switches double opt-in on and refuses to save without it.
2. The site embeds the snippet served from `GET /api/v1/forms/public/embed.js`
   (`SetupFormPublicRoutes` (`internal/handler/form_public_routes.go`); usage comment at the top of
   `internal/handler/assets/form_embed.js`). The alternatives are the hosted page at
   `/api/v1/forms/public/:key/view`, routed in the same function, or a native POST. When the form
   has an allowlist and you use the hosted page, the CRM's own host must be in the allowed origins,
   because the hosted page submits from the CRM origin
   (`requestOrigin` (`internal/handler/form_public_handler.go`);
   `originAllowed` (`internal/service/form_antispam.go`)). Test both embed paths from a real browser.
3. Spam layers run in a fixed order: validation, then the HMAC time trap (3 seconds to 24 hours),
   origin, honeypot, and optional reCAPTCHA v3
   (`formService.SubmitPublic` (`internal/service/form_public.go`); `internal/forms/`). A spam verdict gets the same
   response as a genuine submission and is stored with `status=spam`.
4. With double opt-in, `GET /forms/public/confirm` renders a button and only the POST spends the
   token. The token claim, the lead write and the submission update are one transaction, and mail
   goes out after the commit (`formService.ConfirmSubmission` (`internal/service/form_confirmation.go`);
   `internal/service/form_confirmation_regression_test.go`).
5. A new lead gets `source` = the form name and `owner` = the form's default owner. A repeat gets
   a note instead (see the shared caveats). Sales users see only the leads they own; admins see
   every lead (`CanViewLead` (`internal/service/lead_visibility.go`)). A form that creates
   leads must have a default owner: `ValidateDefinition` refuses `create_lead` without one
   (`Form.ValidateDefinition` (`internal/models/form.go`), called from
   `formService.prepare` (`internal/service/form_admin.go`)). A form
   with lead creation turned off may have no owner; its submissions are stored and no lead is
   written (`formService.storeReceivedSubmission` (`internal/service/form_public.go`) and
   `formService.confirmInTransaction` (`internal/service/form_confirmation.go`) check `CreateLead`).
6. Automation reacts to `lead_created` (and `lead_updated` for repeats), or to `lead_form_submitted`
   for every finalised enquiry, optionally narrowed to one form with `trigger.form_id`. Typical
   steps: a condition on `source` or on the enquiry's `utm_campaign`, `create_task`, `notify`,
   `wait`, a condition `status == new`, and a staff `send_email` (event names such as
   `AutomationEventLeadFormSubmitted` (`internal/models/automation.go`), step names such as
   `AutomationStepCreateTask` (`internal/models/automation.go`); condition fields in
   `automationConditionFields` (`internal/service/automation_condition.go`)). Dry-run a rule before enabling it
   (`internal/service/automation_dry_run.go`).
7. Conversion to a customer, then a deal and a quote (`internal/service/lead_conversion.go`,
   `internal/service/deal_service.go`, `internal/service/quote_service.go`).
8. Optionally, AEO tracking for the agency's **own** brand (see recipe 3).

### Setup

1. Deploy with TLS in front and replace every secret ([FORKING.md](FORKING.md)). The example
   `JWT_SECRET` in `.env.example` is refused at startup, so a copied example does not start until
   the secret is replaced.
2. Set `SMTP_*`, `PUBLIC_BASE_URL` (confirmation links), `APP_BASE_URL` (password-reset links),
   `TRUSTED_PROXIES` and `CORS_ALLOWED_ORIGINS`. The shipped Compose files do not forward SMTP,
   `PUBLIC_BASE_URL`, automation or reCAPTCHA variables, and they hard-code localhost URLs
   (the `backend` service's `environment` block in `docker-compose.yml` and
   `docker-compose.sqlite.yml`, with `APP_BASE_URL: http://localhost:${UI_PORT:-3000}`). If you deploy with Compose,
   apply `docker-compose.production.example.yml` with a second `-f` (see "Production override
   example" in [DOCKER.md](DOCKER.md)).
3. Create the first admin with `create-admin` (`cmd/create-admin`), then create sales users from
   the admin UI.
4. Create one form per site with allowed origins, notify emails, a default owner and double
   opt-in. Embed it and submit once from a real browser.
5. Write rules for created and updated leads, dry-run them, then enable them. The worker starts on
   every instance by default (`AUTOMATION_ENABLED`, `AutomationConfig.Enabled` (`internal/config/config.go`)). Several API
   instances sharing one MySQL/MariaDB database may all run it: run claims carry a heartbeat
   lease (#237, `docs/DEV_RUNTIMES.md`, "Run leases"). Keep replica clocks in sync and stop every
   instance before an upgrade; a SQLite deployment stays one process.
6. Decide whether `contacted` and `qualified` are the right conversion statuses for your team, and
   change `leads.conversion.*` in Settings if not.

### Running it for clients

If the agency runs GopherCRM on behalf of clients, give each client its own installation: a
separate Compose project, host port, volume and secrets. The SQLite Compose file starts two
containers (backend and UI) with a fixed project name, port and subnet
(`docker-compose.sqlite.yml`), so a second client on the same host needs its own copies of those
values. One shared installation does not work for several clients, for two reasons:

- lead email matching is installation-wide, so enquiries from different clients' sites merge into
  one lead (`leadRepository.GetLatestByEmail` (`internal/repository/lead_repository.go`));
- AEO tracks exactly one brand per installation (`AEOProfile` (`internal/models/aeo.go`)).

### Gaps

- Reassigning a lead from the lead list: `POST /leads/:id/assign` is routed (an admin on any lead, a
  sales user on its own, #185) and `leadsApi.assignLead` calls it
  (`assignLead` in `gocrm-ui/src/api/endpoints/leads.ts`), but no screen offers it yet. The API, `owner_id` on a
  lead update (admin only) and the automation `assign_owner` step are the working paths today.
- Attribution: each finalised enquiry stores its own `utm_source`, `utm_medium`, `utm_campaign`,
  `utm_term`, `utm_content` and `referrer_host` on its `form_submissions` row, apart from the lead's
  `source` (`FormSubmission` (`internal/models/form.go`)). A `lead_form_submitted` rule,
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
  build-time `VITE_APP_NAME` (else `GoCRM`). The password reset mail and the quote and ticket
  reply subjects use the same setting, read at send time, else `GopherCRM`
  (`internal/service/product_name.go`, #292). The Docker UI image takes an optional
  `VITE_APP_NAME` build argument (`docker build --build-arg VITE_APP_NAME=Acme gocrm-ui`, #303;
  empty by default, which shows `GoCRM`); both dev Compose files forward it from the shell or the
  repo-root `.env` through `build.args` (delivered, #308).
- AEO export: `GET /aeo/export` (admin, sales, support) downloads one run (`run_id`) or a 7, 30 or
  90-day window (`days`) as CSV (one row per citation) or JSON (runs, answers with their text and
  citations), at most 10,000 answers per file (`internal/handler/aeo_export.go`, delivered, #244).
  The web UI downloads it from the AEO dashboard (selected window) and the prompts answer drawer
  (selected run) for the same roles (delivered, #318). Threshold alerts remain a gap; scheduled or
  emailed client reports are a deliberate cut (#320, docs/ROADMAP.md).

### Do not promise

- a multi-client portal, or client logins that show a client "their" leads;
- nurture campaigns;
- AEO as a ranking or traffic measure: it samples answers and matches brand mentions literally
  (`DetectMentions` (`internal/aeo/analysis.go`));
- client reports built with `npm run screenshots`: the AEO screenshot suite serves fixed fictional
  fixtures, not your data (`mockAeoApi` in `gocrm-ui/e2e/screenshots/10-aeo.spec.ts`);
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
   (`leadService.convertAttempt` (`internal/service/lead_conversion.go`)). Companies are created and linked separately, with
   normalised, unique domains (`normalizeCompany` (`internal/service/company_service.go`)).
3. A deal moves through five stages (`qualification`, `proposal`, `negotiation`, `won`, `lost`),
   with a stage-history row per change and a `lost_reason` kept while lost
   (`internal/service/deal_service.go`, `internal/models/deal_pipeline.go`).
4. A quote is built from catalogue products standing in for service packages, printed from the
   browser (`gocrm-ui/src/pages/quotes/QuotePrint.tsx`) or emailed as plain text
   (`internal/service/quote_email.go`).
5. A `deal_stage_changed` rule creates a follow-up task (`AutomationEventDealStageChanged` (`internal/models/automation.go`)).

### Setup

- Set `deals.default_currency` in Settings (`DefaultConfigurations` (`internal/models/configuration.go`)).
- Create the products (admin only) and the sales users. Configure SMTP for quote email.
- Review the pipeline on the dashboard (`/` in the browser) and the deal board (`/deals/board`,
  `gocrm-ui/src/routes/index.tsx`: `element: <Dashboard />` and `path: 'deals/board'`), both admin
  and sales. An integration reads the same
  totals from the authenticated API at `GET /api/v1/dashboard/pipeline`
  (`SetupDashboardRoutes` (`internal/handler/routes.go`)).
- The quote email subject reads "Quote Q-… from" followed by the `general.company_name` setting,
  or `GopherCRM` when it is blank (`internal/service/quote_email.go`, #292). The printed quote's
  heading shows the same setting (#291).

**Variant: sponsorship or partnership sales.** The same objects work: the partner organisation is a
company, each package is a product, each proposal is a deal with a quote. Recurring gifts and
renewals are not modelled.

### Gaps

- Quote limits, which apply to every quote recipe: one tax rate per line only (no tax categories,
  reverse charge or per-country rules; the API carries it since #324 and the web UI shows and
  edits it since #325); the email of a sent quote carries a single-use
  client accept/decline link that opens a public page of the web application (#382, #383; staff
  can still record the answer); acceptance does not move the deal; expiry is a manual status (acceptance is refused
  once `valid_until` has passed, `quoteService.ChangeStatus` (`internal/service/quote_service.go`)); a PDF of the quote (download and email
  attachment) exists when the server has a browser binary, `chrome-headless-shell` or a full
  Chrome or Chromium (`QUOTE_PDF_CHROME_PATH`,
  #384), otherwise browser print only
  (`docs/FEATURES.md`, section 4d). Tracked in #233.
- A task's `deal_id` (set by a deal rule, through the API, or with the task form's deal picker by
  admin and sales) is shown on task detail and listed in the deal detail Tasks section from
  `GET /deals/{id}/tasks`. Tasks created by a deal rule before #240 are not backfilled
  (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts").

### Do not promise

Invoicing, e-signature (the client's accept page behind the emailed link, #383, records a
click, not a signature), timesheets, or fractional hours on quote lines.

## 3. Product or SaaS company: inbound capture plus single-brand AEO monitoring

**Fit: good.**

A software company captures demo requests and gated-content downloads, and tracks how answer
engines describe its product against competitors.

### Existing flow

- **Demo and gated-content forms.** The follow-up mail replaces `{content_link}` with the form's
  content URL, and is sent only when the form has a follow-up subject (`recordAcceptedMail` and
  `renderFormFollowUp`: `formService.recordAcceptedMail` (`internal/service/form_mail_delivery.go`),
  `renderFormFollowUp` (`internal/service/form_mail_delivery.go`)). With double
  opt-in, the follow-up goes out after confirmation, so the link reaches only a confirmed address.
- **Routing.** Automation conditions can test a lead's `source`, `classification`, `company`,
  `status` and `owner_id`, the lead entries of
  `automationConditionFields` (`internal/service/automation_condition.go`). A
  `lead_form_submitted` rule can also test the triggering submission's `utm_source`,
  `utm_medium`, `utm_campaign`, `utm_term`, `utm_content` and `referrer_host`
  (`automationAttributionFields` (`internal/service/automation_condition.go`)).
- **AEO.** One brand profile with aliases, owned domains and competitors
  (`AEOProfile` (`internal/models/aeo.go`)). Prompts can be generated by the engine set in
  `integration.aeo.generation_engine` (`DefaultConfigurations` (`internal/models/configuration.go`)). Runs go to
  Anthropic, OpenAI, Gemini, Kimi, Perplexity or a custom OpenAI-compatible engine, each
  restricted by the egress policy (`internal/aeo/`, `docs/AEO_SECURITY.md`). The dashboard shows
  share of voice and citations (`gocrm-ui/src/pages/aeo/AEODashboard.tsx`). AEO read routes are
  open to admin, sales and support; writes to admin and sales (`SetupAEORoutes` (`internal/handler/aeo_routes.go`)).
- **Lead lookup by external id.** A lead's `external_id` (`Lead.ExternalID` (`internal/models/lead.go`))
  is looked up through `GET /leads?external_id=` (`LeadHandler.List` (`internal/handler/lead_handler.go`),
  which hands off to `listByExternalID`): an admin finds any lead, a sales user only its own leads,
  every other role none. The value is not unique, the match is exact (the MySQL/MariaDB collation
  makes it case-insensitive, see the `List` annotation), and at most the first 100 matching leads
  are read. Leads have no mapping table like the customers' (section 9, #212).
- **Push to a product or marketing system.** Outbound webhooks send the `lead.created`,
  `lead.updated`, `lead.deleted`, `customer.*`, `deal.*` and `quote.*` events (created, updated,
  deleted; `internal/webhook/catalog.go`) to an endpoint an admin registers in Settings > Webhooks,
  signed and recorded in a delivery log ([webhooks](user-guide/permissions-access/webhooks.md)).
  Users emit no events.

### Setup

- Enter provider keys in Settings > AEO. They are encrypted at rest (`internal/utils/secretbox.go`)
  and resolved before the environment fallback (`internal/service/aeo_credentials.go`). Rotating
  `API_KEY_SECRET` (or `JWT_SECRET`, which it falls back to) orphans them
  (`docs/ROADMAP.md`, "Sensitive settings — follow-ups").
- Review the scheduler: `AEO_SCHEDULE_ENABLED` defaults to `true` and `AEO_SCHEDULE_HOUR` to 6
  (`AEOConfig.ScheduleEnabled` (`internal/config/config.go`),
  `AEOConfig.ScheduleHour` (`internal/config/config.go`)). Scheduled runs spend provider credit.
- `AEO_QUERY_TIMEOUT_SECONDS` (default 60) needs raising for a slow self-hosted engine. A model
  server on your LAN needs an `AEO_EGRESS_LOCAL_ORIGINS` entry (`docs/AEO_SECURITY.md`).
- Review the default model IDs (the `AEO_<ENGINE>_MODEL` defaults in `Load` (`internal/config/config.go`)). Pinned IDs get retired, so
  prefer rolling aliases where the provider offers them.
- Add reCAPTCHA keys if the forms attract spam (`RECAPTCHA_SITE_KEY`, `RECAPTCHA_SECRET_KEY`).

### Gaps

- AEO results can be exported as CSV or JSON through `GET /aeo/export` (#244) or the export
  buttons on the AEO dashboard and in the prompts answer drawer (#318); there is no threshold
  alerting, and scheduled client reports are a deliberate cut (#320, docs/ROADMAP.md).
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
- Staff create tickets (support and admin, `TicketHandler.Create` (`internal/handler/ticket_handler.go`)), assign them,
  write internal notes or public comments, and record a resolution
  (`internal/service/ticket_service.go`). A ticket has a title, description, status, priority,
  customer, assignee and resolution, and nothing else (`Ticket`, `internal/models/ticket.go`).
- A customer user can read its own ticket at `GET /tickets/:id`, with internal comments removed
  (`TicketHandler.Get` (`internal/handler/ticket_handler.go`)), and `TicketDetail` renders it
  read-only (`canOnRecord('tickets.update'` in `gocrm-ui/src/pages/tickets/TicketDetail.tsx`).
  `GET /tickets` and `GET /tickets/my` refuse customers
  (`TicketHandler.List` (`internal/handler/ticket_handler.go`),
  `TicketHandler.ListMyTickets` (`internal/handler/ticket_handler.go`)), but `GET /customers/:id/tickets` lists the
  tickets of the customer record linked to the caller's account and refuses any other id
  (`SetupTicketRoutes` (`internal/handler/routes.go`),
  `TicketHandler.ListByCustomer` (`internal/handler/ticket_handler.go`); tested in
  `runTicketCustomerVisibility` (`internal/integration/ticket_visibility_integration_test.go`)). The UI has no customer
  ticket navigation, create form or comment form.
- Tasks with labels (`internal/service/task_service.go`, `internal/service/label_service.go`).
- Bulk status updates, at most 100 ids, all or nothing (`internal/service/bulk_status_service.go`).
- The dashboard's recent tickets are narrowed to what the caller may see.
- **Ticket events to an external tool.** Outbound webhooks send `ticket.created`, `ticket.updated`
  and `ticket.deleted` (and the `customer.*` and `task.*` events; `internal/webhook/catalog.go`) to an
  endpoint an admin registers in Settings > Webhooks, for example a chat channel or an issue
  tracker ([webhooks](user-guide/permissions-access/webhooks.md)).

### Setup

- Seed companies and customers with a script against the REST API. There is no import screen.
- Assign tickets deliberately. A ticket created without `assigned_to_id` is assigned to the
  user who created it (`TicketHandler.Create` (`internal/handler/ticket_handler.go`)), so an admin or an
  integration key that omits the field becomes the assignee. Nothing distributes tickets among
  support users; the `tickets.auto_assign_support` setting that suggested otherwise was retired
  in #224.
- Train staff on the difference between internal notes and public comments.
- An integration that creates both customers and tickets **with one identity** uses the key of
  a dedicated user on a custom role holding `customers.create` and `tickets.create`
  (`SetupCustomerRoutes` (`internal/handler/routes.go`),
  `TicketHandler.Create` (`internal/handler/ticket_handler.go`)): no system role
  other than admin holds both, and an API key has no scopes of its own, it acts with its owner's
  permissions (`APIKey` (`internal/models/apikey.go`)). Such a key can read nothing and must
  name `assigned_to_id` on every ticket it creates. Setup, rotation and revocation:
  [Least-privilege integration user](AUTHORIZATION.md#least-privilege-integration-user).

### Gaps before a shared desk

- Support users see only tickets assigned to them, in the list and the detail
  (`TicketHandler.List` (`internal/handler/ticket_handler.go`),
  `TicketHandler.Get` (`internal/handler/ticket_handler.go`)). There is no unassigned or shared queue: #259.
- Ticket status and priority filters run server-side (#218, delivered): `GET /tickets` applies
  them before pagination, so totals are correct, and for support they only narrow the assigned
  tickets (`docs/FEATURES.md`, 5.5).
- Reply email is API only: a staff comment posted with `email_customer: true` is mailed once to the
  ticket's customer and recorded in `ticket_mail_deliveries`, but the comment form has no checkbox
  for it and a failed mail is not retried. Inbound mail and a ticket channel field remain gaps
  (#219).
- No intake path from a form or an external system that is safe to retry: a ticket has no
  submission or external key, so idempotent import is a schema change (#213). Customers, unlike
  tickets, can be mapped to an external system's ids (delivered, #212): `POST
  /customers/{id}/external-ids` and the exact lookup `GET /customers?source=&scope=&external_id=`.
- Ticket title, description and resolution survive erasure, as business records
  (`docs/DEV_ERASURE.md`, "Business records. Free text may have been typed"). When they name the person, redact them through `PUT /tickets/{id}`
  before `DELETE /customers/{id}`, as the operator procedure for ticket text in
  `docs/DEV_ERASURE.md` (*Deleting personal data*) describes.
- Ticket automation triggers are delivered (#242, rule screens #312): an admin rule, built in the
  rule form with the entity Ticket, can start on `ticket_created`, `ticket_updated`,
  `ticket_status_changed` (from/to ticket statuses) or a daily schedule over
  `filters.ticket_status`, and notify the assignee, create a task linked to the ticket's customer
  or reassign the ticket to another support or admin user. Deleting a ticket cancels its open
  runs. Still open: comment, first-response, SLA and overdue triggers, and run visibility for
  support users (`docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts").
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
  - VAT is one tax rate per line (`tax_rate_bp`, with the line tax and the quote's tax and gross
    totals computed by the server, #324; `docs/FEATURES.md`, 4d.3): there are no tax categories,
    reverse charge or per-country rules; the web UI edits the rate in the line dialog and shows the
    line tax and the net, tax and gross totals on the detail and print pages (#325). This
    remaining limit is part of #233, with the other quote limits from recipe 2. Line quantities
    accept up to three decimal places through the API since #343 (0.001 to 1,000,000,
    `docs/FEATURES.md`, 4d.11); the web UI line dialog takes them too, and the detail and print
    pages show them without trailing zeros (#346).
  - Product SKUs stay indexed but not unique on purpose (`internal/models/product.go`), so do not
    key an ERP sync on them. Map the ERP's identifiers instead: `POST /products/{id}/external-ids`
    stores an exact, case-sensitive `(source, scope, external_id)` and
    `GET /products?source=&scope=&external_id=` finds the live product (#239; admin writes, admin
    and sales lookups). There is no UI for the mappings.
- **Erasure:** erasing a customer or lead keeps its deals and quotes, links, totals and history,
  and overwrites their staff-typed text in the same transaction, whatever the quote status: deal
  title `[redacted]` with empty notes and lost reason, quote title `[redacted]` with empty notes,
  and each line's description reset to its product's name (#245; `docs/DEV_ERASURE.md`, *Deleting
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
- **Limit:** automation schedules run daily in UTC (`AutomationTrigger.HourUTC` (`internal/models/automation.go`)).
- **Do not promise:** sessions, capacity, booking or attendance tracking.

## 7. Team that signs in through Microsoft Entra ID

**Fit: good.**

A company whose staff already sign in to Microsoft 365 wants them to reach the CRM with the same
account, get their CRM role from Entra ID, and lose access when they leave.

### Existing flow

- **Single sign-on.** OpenID Connect with one identity provider at a time
  (`SetupSSOSettingsRoutes` (`internal/handler/routes.go`), settings behind the admin-only
  `settings.sso_manage`). The sign-in page shows **Sign in with single sign-on**; the first
  sign-in creates an active account with no usable password, and later sign-ins recognise the
  person by the provider's identifier, never by email
  ([single sign-on](user-guide/permissions-access/single-sign-on.md)).
- **Roles from Entra ID.** The role mapping turns an Entra ID app role (claim `roles`) or a security
  group (claim `groups`, by Object ID) into a GopherCRM role, a custom role included, at every
  sign-in; a person no mapping row matches gets no access. A session that started with single
  sign-on can be refreshed for 24 hours after the sign-in at the provider.
- **Sign-out.** Logout also sends the browser to the Entra ID sign-out page, and Entra ID's
  front-channel logout (`GET /auth/oidc/frontchannel-logout`) ends every GopherCRM session of that
  person.
- **Optional SCIM provisioning.** Entra ID creates, updates and deactivates accounts before people
  sign in, over `/scim/v2` with a SCIM token an admin creates in Settings > SCIM tokens
  (`SetupSCIMRoutes` (`internal/handler/scim_routes.go`), `SetupSCIMTokenRoutes` (`internal/handler/scim_routes.go`);
  [SCIM provisioning](user-guide/permissions-access/scim-provisioning.md)). A provider deactivation
  or DELETE deactivates the account and ends its tokens.
- **Audit.** Refused sign-ins and SCIM token failures carry a reason code in Settings > Sign-in audit
  ([sign-in audit](user-guide/permissions-access/sign-in-audit.md)).

### Setup

Follow [How to: single sign-on with Microsoft Entra ID](user-guide/permissions-access/how-to-microsoft-entra-id.md):

- Create a local break-glass administrator with a password (`create-admin`) **before** enabling
  single sign-on, and keep it: local accounts can always sign in with their password, so a provider
  outage, an expired certificate or secret, or a wrong role mapping cannot lock you out.
- Register the application in Entra ID with the redirect URI `APP_BASE_URL/auth/callback`, the
  sign-out return address `APP_BASE_URL/login`, the front-channel logout URL and the `sid` optional
  claim; prefer a certificate over a client secret.
- Create app roles (for example `CRM.Admin`, `CRM.Sales`), assign people or groups (groups need
  Entra ID P1), and set **Assignment required?** to Yes.
- In Settings > Single sign-on enter the issuer, client id and credential, map each app role to a
  GopherCRM role, and switch it on.
- For SCIM, set the Tenant URL to `PUBLIC_BASE_URL/scim/v2` and make the reverse proxy forward
  every path under `/scim/v2/` to the backend: the SCIM base URL is outside `/api/v1`, and without
  that rule Entra ID's **Test Connection** receives the web page.

### Gaps

- No SAML, and one identity provider at a time: a second tenant or another provider needs code.
- SCIM serves Users and the role-mapping Groups only: a group POST never adds a group, group DELETE
  answers 405, group PUT is not routed, renaming a group is refused (Entra ID's default group
  mapping sends `displayName`, so map the groups in Settings > Single sign-on instead), and
  `/scim/v2/Bulk`, sorting and ETags are not served (scim-provisioning.md, "What is not served").
- A role changed in Entra ID applies at the person's next sign-in, at the latest 24 hours later;
  access tokens issued before stay valid until they expire unless a SCIM group change ends them
  (section 8, "Regulated health records").

### Do not promise

SAML, several identity providers, groups created or renamed from Entra ID through SCIM, or that
removing a person from a group leaves the account as it is. Removing them from one group gives
them the role of the first mapping row that matches one of their remaining groups; removing them
from their last mapped group deactivates the account, as SCIM `active` false does (signed out
everywhere, API keys revoked), so keep integration and service accounts in a mapped group. A group change never erases an account:
deactivate people at the provider, and erase the account in GopherCRM when the data must go.

## 8. Poor fits

Each of these would need a different core model, not a recipe.

| Business | Why it does not fit |
|---|---|
| Field-service dispatch | No dispatch calendar, no offline mode and no attachments |
| Recruiting | No attachments for CVs, and the pipeline links one deal to one customer, where recruiting needs many candidates per opening |
| Nonprofit donor CRM | No recurring gifts, campaigns or nurture email to contacts |
| Multi-tenant reseller hosting | No tenant column; settings and the AEO profile are installation-wide singletons (`AEOProfile` (`internal/models/aeo.go`)) |
| Regulated health records | No field-level encryption, no read audit, and a single access token cannot be revoked on its own: only logout (the single sign-on front-channel logout included), a password change, a password reset, deactivation (by an administrator or through SCIM) and a SCIM group change that leaves the account in no mapped group or gives it a role lacking a permission the old one had end access tokens early, all of an account's at once (#236; `docs/ROADMAP.md`, "Access-token revocation beyond the session-ending events") |
| An e-commerce order system inside the CRM | Products and quotes are sales tools; the shop platform stays the system of record for orders, stock and payment |

## 9. Cross-cutting gaps

Gaps that more than one recipe runs into. Check `gh issue list` for the current state before
planning around an issue number.

| Gap | Recipes | Evidence | Issue |
|---|---|---|---|
| Lead reassignment from the lead list (no UI control; API routes exist) | 1, 2 | `gocrm-ui/src/api/endpoints/leads.ts` (`assignLead`) | — |
| Delivered: per-submission attribution columns, and `lead_form_submitted` per finalised submission with `trigger.form_id`. Still open: first- or last-touch attribution on the lead, and the embed script filling hidden `utm_*` fields | 1, 3, 6 | `applyFormAttribution` (`internal/service/form_attribution.go`); `formService.applySubmissionLead` (`internal/service/form_lead_link.go`); `FormSubmission` (`internal/models/form.go`) | #220 |
| Form mail is retried by hand only: no automatic retry or backoff, and `sent` means accepted by the relay (no bounce confirmation) | 1, 3, 6 | `formService.RetryDelivery` (`internal/service/form_mail_delivery.go`) | — |
| Delivered: erasure scrubs unlinked submissions of the erased address; ticket free text is kept, behind a documented operator redaction procedure. Still open: unlinked submissions of other addresses stay until the retention purge | 1, 4 | `docs/DEV_ERASURE.md`, "Deleting personal data" (`scrubUnlinkedSubmissions`, "matches no erased row", "Operator procedure for ticket text") | #214 |
| Delivered: erasure redacts the deal and quote text of the erased customer or lead and deletes notifications about those deals. Still open: a task rendered from `{{deal.title}}` before the erasure keeps that text | 2, 5, 6 | `internal/repository/business_text_erasure.go` | #245 |
| Delivered: the UI takes its product name from one helper (stored `general.company_name`, else `VITE_APP_NAME`, else `GoCRM`) and the light theme's primary colour from `ui.theme.primary_color` (#291); the password reset, quote and ticket reply mails take their product name from the same setting, else `GopherCRM` (#292); the Docker UI image's optional `VITE_APP_NAME` build argument (#303), forwarded by both dev Compose files through `build.args` (#308) | 1, 2 | `internal/service/product_name.go`; `gocrm-ui/Dockerfile`; `docker-compose.yml` | #221 |
| Delivered: production Compose override example `docker-compose.production.example.yml` (SMTP, base URLs, automation, reCAPTCHA, AEO schedule, CORS; no backend host port, UI on loopback); AEO runs and the daily schedule coordinated across backend replicas on MySQL/MariaDB (#229); the automation worker on several backend replicas (#237). Still open: TLS proxy configuration | all | `docker-compose.production.example.yml`; the `backend` service's `environment` block in `docker-compose.yml` and `docker-compose.sqlite.yml`; `docs/FORKING.md` 5.4 | #222, #229, #237 |
| Delivered: a least-privilege credential for integrations is a dedicated user on a custom role holding only the keys it needs, documented with setup, rotation and revocation in [AUTHORIZATION.md](AUTHORIZATION.md#least-privilege-integration-user) and proven by `TestLeastPrivilegeIntegrationUser*`. Also delivered: outbound webhooks for the created, updated and deleted events of leads, customers, tickets, deals, tasks, companies and quotes, signed, with a delivery log and a screen in Settings > Webhooks. Still open: API keys have no scopes of their own, and an administrator cannot list or revoke another user's keys | 3, 4 | `internal/integration/integration_user_integration_test.go`; `APIKey` (`internal/models/apikey.go`); `internal/webhook/catalog.go` | #211 |
| Delivered: external identity mapping for customers, a `(source, scope, external_id)` table unique on the exact triple (customer email stays globally unique), written by admin and sales, resolved by an exact lookup on `GET /customers`, and deleted with the customer by every erasure path. Still open: no UI, and no mapping table for leads (they keep one external_id column), users, companies or deals | 3, 4 | `internal/models/customer_external_id.go`; `internal/handler/customer_external_id_handler.go` | #212 |
| Retry-safe intake from forms or external systems into tickets (schema change) | 4 | `internal/models/ticket.go` | #213 |
| Delivered: the lead `external_id` lookup is routed as `GET /leads?external_id=` (admin any lead, sales own leads). Still open: no mapping table for leads | 3 | `LeadHandler.List` (`internal/handler/lead_handler.go`) | #215 |
| Delivered: server-side ticket status and priority filters (#218). Still open: a shared or unassigned support queue | 4 | `TicketHandler.List` (`internal/handler/ticket_handler.go`); `docs/FEATURES.md` 5.5 | #259 |
| Delivered: outbound ticket replies by email — a staff comment sent with `email_customer: true` is mailed once to the ticket's customer, the outcome recorded per comment (backend only; no retry). Still open: the comment-form checkbox, a ticket channel field and inbound mail | 4 | `internal/service/ticket_comment_email.go`; `docs/FEATURES.md` 5.10 | #219 |
| Delivered: a bulk status batch writes `completed` inside its own transaction, and a row left `pending` (its batch never committed, its `failed` write timed out) is failed by a sweep at boot and every 15 minutes once it is an hour old. Still open: rows already `pending` before the upgrade are swept to `failed` although their batch may have committed (outcome unknown, not stored) | 4 | `internal/service/bulk_status_service.go`; `internal/service/bulk_operation_recovery_runtime.go`; `docs/DEV_ENTITIES.md`, *Bulk endpoints* | #196 |
| Nurture or customer email from automation | 1, 2, 3 | `docs/ROADMAP.md`, "Email to leads and customers" | #238 |
| Delivered: more than one automation worker on MySQL/MariaDB — lease-based run claims, owner-fenced writes and a stale sweep at boot and on every tick. Still open: several processes on one SQLite file; the window between the pre-send check and the relay call (an email can go out once after it was recorded `email_unconfirmed`, never resent) | 1 | `internal/service/automation_run_lease.go`, `internal/service/automation_recovery.go`; `docs/DEV_RUNTIMES.md`, "Run leases (#237)" | #237 |
| Delivered: one tax rate per quote line with stored line tax and quote tax/gross totals (#324; shown and edited in the web UI, #325); fractional line quantities with up to three decimal places through the API (#343; web UI #346); a single-use client accept/decline link in the quote email with its two public API routes (#382) and the public page that answers it (#383); the quote as a PDF, downloadable and attached to the quote email, when the server has a Chrome or Chromium binary (#384). Still open: tax categories, reverse charge, per-country rules | 2, 5 | `internal/models/quote.go`, `internal/models/quote_quantity.go`; `docs/FEATURES.md` 4d | #233, #324, #325, #343, #346, #382, #383, #384 |
| Delivered: product external-id mappings as the ERP key (API only). Still open: no UI for the mappings; SKUs stay non-unique by design | 5 | `docs/DEV_ERASURE.md`, "Product external ids (#239)" | #239 |
| Delivered: a task's deal link on the task form (admin and sales), task detail and deal detail. Still open: tasks created by earlier automation runs are not backfilled | 2 | `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #240 |
| Delivered: ticket automation triggers (`ticket_created`, `ticket_updated`, `ticket_status_changed`, schedule) in the API and the rule form, rule list, run list and dry run (#312), cancelled on ticket delete. Still open: comment/SLA/overdue triggers and support run visibility | 4 | `internal/service/automation_validation.go`; `gocrm-ui/src/types/automation.ts`; `docs/ROADMAP.md`, "Follow-up automation — deliberate v1 cuts" | #242, #312 |
| Ticket attachments | 4 | `internal/models/ticket.go` | #243 |
| Delivered: AEO export of one run or a 7/30/90-day window as CSV or JSON (`GET /aeo/export`, admin, sales, support; at most 10,000 answers), with download buttons on the AEO dashboard and in the prompts answer drawer (#318). Still open: threshold alerting; scheduled or emailed client reports are a deliberate cut (#320, docs/ROADMAP.md); the screenshot suite stays fixtures only | 1, 3 | `internal/handler/aeo_export.go`; `gocrm-ui/src/pages/aeo/aeoExport.ts`; `docs/DEV_RUNTIMES.md`, "AEO export (#244)"; `gocrm-ui/e2e/screenshots/10-aeo.spec.ts` (`mockAeoApi`, fixtures only) | #244, #318, #320 |
