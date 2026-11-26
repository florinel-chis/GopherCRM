# GopherCRM Screenshot Catalog

> **For:** Anyone who wants to see the screens. Part of the [documentation index](README.md).

Reference images of every user-facing screen in the React frontend (`gocrm-ui/`). Each image was
captured from the running application by the Playwright suite in `gocrm-ui/e2e/screenshots/`, at a
1440x900 viewport with `deviceScaleFactor: 2`.
86 captures, taken in five groups:

- 38 regenerated 2026-08-08, when tasks gained labels, against a MySQL-backed backend and the Vite
  dev server;
- the five AEO captures, added 2026-08-11 with the module itself, from fixed demo data;
- the ten products and quotes captures, added 2026-10-28 against the e2e driver's SQLite backend;
- the eight follow-up automation captures, added 2026-10-29 against a SQLite backend;
- 25 taken 2026-11-24 against the e2e driver's SQLite backend: the forgot-password and
  reset-password forms, the six settings captures (three of them recaptured), companies, deals,
  forms, the administration screens and the public quote response page.

The captures record the application as it actually is. Where a screen is a placeholder or a control
does not exist yet, the image shows that rather than a mock-up, and the difference is noted in the
caption or under [Known gaps](#known-gaps).

Sections below map to [docs/FEATURES.md](FEATURES.md), which carries the feature and test-coverage
detail for each area.

## Regenerating

The suite drives the real API, so the backend must be running first. From the repository root:

```bash
DISABLE_RATE_LIMIT=true SERVER_PORT=8090 go run ./cmd
```

Then, from `gocrm-ui/`:

```bash
npm run screenshots
```

The Vite dev server is started automatically by the `webServer` block in
`playwright.config.screenshots.ts` (an already-running one is reused), and the admin account the
specs log in as is seeded by `e2e/global-setup.ts`. Images are written straight to
`docs/screenshots/<area>/`, overwriting the previous run.

The suite creates records through the UI so lists and detail pages are not empty. It never confirms
a delete dialog — deleting a user, customer or lead is an irreversible erasure (see FEATURES.md
section 12), and deleting a label detaches it from every task at once.

The label captures reuse fixed names (`Onboarding`, `Escalation`, `Follow-up`) and create them only
when missing. Labels are unique by name and hard deleted rather than soft deleted, so run-scoped
names would pile up on the very screen being photographed.

## Authentication

FEATURES.md section 1.

![Login page](screenshots/auth/01-login.png)
The login form, with the email, password and "remember me" controls.

![Login validation errors](screenshots/auth/02-login-validation.png)
Client-side validation on submit of an empty login form.

![Registration form](screenshots/auth/03-register.png)
The public registration form, filled in. Registration always creates a `customer` — a
client-supplied role is ignored.

![Registration validation errors](screenshots/auth/04-register-validation.png)
The password-policy messages: minimum 10 characters with uppercase, lowercase, digit and special
character.

![Forgot password form](screenshots/auth/05-forgot-password.png)
The Forgot Password form at `/forgot-password`, with an email address typed in and not submitted:
the Send Reset Link button and the link back to sign-in.

![Reset password form](screenshots/auth/06-reset-password.png)
The Reset Password form opened from a reset link (`/reset-password?token=...`; the capture uses a
placeholder token), with the new password typed twice and not submitted, the show-password
toggles and the link back to sign-in.

To regenerate only the authentication images, on free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/01-auth.spec.ts`.

## Dashboard

FEATURES.md section 2.

![Dashboard overview](screenshots/dashboard/01-overview.png)
Full-page dashboard with the five stat cards (total leads, total customers, open tickets, pending
tasks, conversion rate), the sales chart, Quick Actions and Upcoming Tasks, over seeded data.

## Leads

FEATURES.md section 3.

![Leads list](screenshots/leads/01-list.png)
The leads table with company, contact, email, phone, status, classification, source and created
date.

![Create lead form](screenshots/leads/02-create.png)
The create form, filled in and not submitted.

![Lead detail](screenshots/leads/03-detail.png)
A single lead's detail page.

![Edit lead form](screenshots/leads/04-edit.png)
The edit form populated from the stored record.

![Delete lead confirmation](screenshots/leads/05-delete-confirm.png)
The delete confirmation dialog. Confirming erases the lead's personal data and cascades to the
customer it was converted into.

![Lead search](screenshots/leads/06-search.png)
The list filtered by a search term, which matches across name, email, company, phone and notes.

## Customers

FEATURES.md section 4.

![Customers list](screenshots/customers/01-list.png)
The customers table with company, primary contact, email, phone and customer-since date. Every row carries its own actions; for admin and support the row menu also offers "Create Ticket" for that customer. Note: this image predates #186 and still shows the removed Total Revenue column until it is recaptured.

![Create customer form](screenshots/customers/02-create.png)
The create form with contact details and full address, filled in and not submitted.

![Customer detail](screenshots/customers/03-detail.png)
A single customer's detail page.

![Edit customer form](screenshots/customers/04-edit.png)
The edit form populated from the stored record.

![Delete customer confirmation](screenshots/customers/05-delete-confirm.png)
The admin-only delete confirmation. Confirming erases personal data and cascades to the lead the
customer was converted from.

## Companies and deals

Companies are FEATURES.md section 4b, deals section 4c (4c.9 for the board). The suite seeds three
companies, a customer and a lead of the first company, and five deals across the pipeline through
the API, each with a fixed name or email and created only when missing; nothing is deleted. These
five were captured against the e2e driver's SQLite backend. To regenerate only these images, on
free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/13-companies-deals.spec.ts`.

![Companies list](screenshots/companies/01-list.png)
The companies table at `/companies` with name, domain, industry, city, owner and created date, the
search box, the view, edit and delete actions per row, New Company and server pagination.

![Company detail](screenshots/companies/02-detail.png)
A company's detail page with its employee-range chip, Edit and delete: company information
(domain, website, industry, employees, phone, owner, created and updated times), the address and
notes, then the company's customers, leads and deals, each a paginated table; the Customers and
Deals sections carry New Customer and New Deal buttons.

![Deals list](screenshots/deals/01-list.png)
The deals table at `/deals` with title, company or customer, stage chip, amount with currency,
probability, expected close date and owner, over deals in all five stages, with the search box,
the stage filter, the Open only switch, the List/Board toggle and New Deal. The row actions column
runs past the right edge of the frame.

![Deal pipeline board](screenshots/deals/02-board.png)
The board at `/deals/board`: one column per stage with its deal count, total and weighted total,
and a card per deal with title, company, amount, expected close date, owner initials and an
actions menu. The board scrolls sideways; at this width the Won column is cut off and Lost is out
of frame.

![Deal detail](screenshots/deals/03-detail.png)
A deal in negotiation at `/deals/{id}`: stage chip, the Stage selector, Edit and delete, the deal
information (amount, probability, expected close, owner, company, customer, lead, source and the
timestamps), the empty Quotes, Tasks and Automation sections, and the stage history: created in
qualification, then moved to proposal and to negotiation, each with user and time.

## Products and quotes

FEATURES.md section 4d (4d.9 for the screens). The suite seeds a product catalogue, one customer,
three deals and quotes in every status through the API, then walks one quote through the UI: it
is started from its deal's Quotes section, given two lines, marked as sent, printed and accepted.
The catalogue entries use fixed names and are created only when missing; the quotes and deals are
created on every run. These ten were captured against the e2e driver's SQLite backend. To regenerate only these images, on free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/11-quotes.spec.ts`.

![Products list](screenshots/quotes/01-products-list.png)
The product catalogue at `/products`: name, SKU, price formatted with its currency and the
Active/Inactive chip, with the search box, the active filter and server pagination. The edit and
delete actions and New Product are shown to admins only.

![Product form](screenshots/quotes/02-product-form.png)
The admin-only create form at `/products/new`, filled in and not submitted: name, SKU,
description, price in major units (stored as integer minor units), ISO 4217 currency and the
active switch.

![Quotes list](screenshots/quotes/03-quotes-list.png)
The quotes table at `/quotes` with number, title, status chip, customer, deal, total with
currency and valid-until date, over quotes in all five statuses (draft, sent, accepted, rejected,
expired), with the status filter and server pagination.

![Create quote dialog](screenshots/quotes/04-quote-create-dialog.png)
The New quote dialog opened from a deal's Quotes section, filled in and not yet submitted: the
deal is prefilled and the customer and currency follow from it; title, valid until and notes are
typed in.

![Draft quote detail](screenshots/quotes/05-quote-detail-draft.png)
A draft quote at `/quotes/{id}` with two lines (one at 10 % off) and the subtotal, discount and
total as computed by the server. While the quote is a draft, the Edit button, Add line and the
per-line edit and delete actions are available, and the only status action is Mark as sent.

![Add line dialog](screenshots/quotes/06-quote-add-line-dialog.png)
The Add line dialog: the product picker lists active products in the quote's currency and shows
the unit price; description, quantity, discount percentage (sent as basis points) and an optional
unit-price override complete the line.

![Sent quote detail](screenshots/quotes/07-quote-detail-sent.png)
The same quote after Mark as sent: the status buttons for the valid transitions (Accept, Reject,
Mark expired, Revise), the Email quote action, the sent time, and a status history of two rows.
The line and edit controls are gone outside the draft status.

![Quote print page](screenshots/quotes/08-quote-print.png)
The print layout at `/quotes/{id}/print` on screen: application name, quote number, title, date,
valid until, status, the customer it is prepared for, the lines, the totals and the notes, below
the Back to quote and Print buttons.

![Quote print page under print media](screenshots/quotes/09-quote-print-media.png)
The same page with print media emulated: the app bar, the navigation drawer, the breadcrumbs and
the page's own toolbar are hidden, leaving only the quote document.

![Deal Quotes section](screenshots/quotes/10-deal-quotes-section.png)
The deal detail page after the quote was accepted: its Quotes section lists the accepted quote and
the earlier rejected proposal with number, title, status and total, next to the New quote button.
Accepting a quote leaves the deal's stage unchanged.

![Quote response page](screenshots/quotes/11-quote-response.png)
The public page a client reaches from the link in a quote email (`/quote-response`), opened
without a session and captured before any answer: quote number, valid-until date, the lines with
quantity, unit price, discount, tax rate and total, the subtotal, discount, net, tax and gross,
and the Decline and Accept buttons. The page removes the token from the address bar once it has
read it. Captured on 2026-11-24 against the e2e driver's SQLite backend by its own spec, which
seeds a customer, two products and a sent quote with fixed names and gets the link from
`scripts/e2e/quote-response-link`, so it runs only under `scripts/e2e/run.sh`:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/16-quote-response.spec.ts`.

## Follow-up automation

FEATURES.md section 4e (4e.17 for the screens). The suite seeds three rules through the API with
fixed names, created only when missing: the enabled "New lead follow-up" (lead created: notify the
owner, wait two days, create a follow-up task), the disabled "Proposal sent: check in after three
days" (deal stage changed to proposal: wait, a stage condition, an email to the owner) and the
disabled "Daily nudge for stale contacted leads" (a daily schedule at 08:00 UTC). Two leads owned
by the admin are then created, and the suite polls `GET /automation/runs` until the worker has run
each of them up to its wait step, so the run list, the step log, the lead section and the
notifications show real data. The worker polls every `AUTOMATION_POLL_INTERVAL_SECONDS`
(default 30); these eight were captured against a SQLite backend on :8090 started with
`AUTOMATION_POLL_INTERVAL_SECONDS=2`. A backend with `AUTOMATION_ENABLED=false` fails the
run-dependent captures. To regenerate only these images, on free ports against the e2e database:
`AUTOMATION_POLL_INTERVAL_SECONDS=2 scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/12-automation.spec.ts`.

![Automation rules list](screenshots/automation/01-rules-list.png)
The admin-only rule list at `/automation/rules`: name, entity, a readable trigger summary, the
Enabled switch (switching a rule off asks for confirmation, because it cancels the rule's active
runs), version, last update, and the Edit, Dry run and Delete rule tools. A row click opens the
editor too.

![Rule form](screenshots/automation/02-rule-form.png)
The editor of the deal rule at `/automation/rules/{id}/edit`: name, description, entity type,
trigger with its from/to stage, and three step cards (wait, condition, send email) with move up,
move down and remove controls and the placeholders each text field accepts. The condition's value
is free text that suggests the known stages and is stored verbatim (here `proposal`). The name
field notes that sales users see the name on runs. The Dry run button simulates the form as it is,
unsaved changes included, and saves nothing; saving creates a new version after a confirmation.

![Dry-run dialog](screenshots/automation/03-dry-run-dialog.png)
The dry run of "New lead follow-up" on a seeded lead, opened from the rule list: the notice that
nothing is written, sent or notified, the lead picker, and the simulated step report (notify
succeeded, the wait resumes two days later, the task would be created with its assignee, due date
and lead).

![Automation runs list](screenshots/automation/04-runs-list.png)
The run list at `/automation/runs` with the status, rule, rule ID, entity and record ID filters,
over the two runs waiting on their two-day wait, each naming its rule; each run number links to its detail. Admins see every run, sales only runs on records
it owns.

![Run detail](screenshots/automation/05-run-detail.png)
A waiting run at `/automation/runs/{id}`: rule name and version, record, next step, attempts, the
timestamps and the step log (notify succeeded with its recipient and notification, wait waiting
with its resume time). The Cancel run button is shown to admins while the run is still active.

![Lead Automation section](screenshots/automation/06-lead-automation-section.png)
The lead detail page with its Automation section: the lead's runs with rule name, status chip, resume
time and creation time, and a View all link to the run list filtered to this lead. Deals carry the
same section; support and customers do not see it.

![Notification bell menu](screenshots/automation/07-notification-bell-menu.png)
The app bar's bell for staff users with its unread badge and the menu open: the latest ten
notifications (unread ones in bold), Mark all read and View all. The count is polled every minute
and on window focus.

![Notifications page](screenshots/automation/08-notifications-page.png)
The full list at `/notifications` with the All/Unread filter, Mark all read, and per row the
title and body, received time, read status, Mark read and Open (to the lead or deal; support only
marks read).

## Tickets

FEATURES.md section 5.

![Tickets list](screenshots/tickets/01-list.png)
The tickets table with ticket number, subject, status, priority, customer and assignee.

![Create ticket form](screenshots/tickets/02-create.png)
The create form with subject, description, priority, status and customer picker. Only admin and
support may create tickets.

![Ticket detail](screenshots/tickets/03-detail.png)
A single ticket's detail page, including the description panel.

![Edit ticket form](screenshots/tickets/04-edit.png)
The edit form; support users may only update tickets assigned to them, and sales is read-only.

![Delete ticket confirmation](screenshots/tickets/05-delete-confirm.png)
The delete confirmation dialog. Ticket deletion is admin-only and an ordinary soft delete.

## Tasks

FEATURES.md section 6.

![Tasks list](screenshots/tasks/01-list.png)
The tasks table with title, labels, status, priority, assignee, due date and created date, plus the
search box and the status, priority and label filters. The rows on this page carry no labels; see
[Labels](#labels) for the same list filtered down to a labelled task.

![Create task form](screenshots/tasks/02-create.png)
The create form with title, description, status, priority, due date, assignee and the Labels
multi-select. "Assign To" is captioned "(Optional)" but the API rejects a task without an assignee.

![Task detail](screenshots/tasks/03-detail.png)
A single task's detail page.

![Edit task form](screenshots/tasks/04-edit.png)
The edit form populated from the stored record. Only admins may reassign a task.

![Delete task confirmation](screenshots/tasks/05-delete-confirm.png)
The delete confirmation dialog. Task deletion is admin-only.

## Labels

Free-form colored labels on tasks; related to FEATURES.md section 6 (Task Management).

![Labels list](screenshots/labels/01-list.png)
The label management screen at `/labels`: name chip, hex colour with a swatch, the number of tasks
carrying the label, and the edit and delete actions. Deleting is admin-only.

![Create label dialog](screenshots/labels/02-create-dialog.png)
The create dialog, filled in and not submitted. The colour comes either from the preset swatches or
from the free `#RRGGBB` field below them; the preview chip shows the automatic light/dark text
colour that the chosen background produces.

![Labels on the task form](screenshots/labels/03-task-form.png)
The task form's Labels field with two labels attached. Typing a name that does not exist yet offers
an `Add "…"` option that creates the label inline, without leaving the form.

![Tasks filtered by a label](screenshots/labels/04-list-filtered.png)
The task list filtered to one label, chosen from the Label dropdown. The active filter is echoed as
a chip with a clear affordance, and the chips in the Labels column filter the list when clicked.

![Delete label confirmation](screenshots/labels/05-delete-confirm.png)
The delete confirmation, which states how many tasks the label will be removed from. Labels are hard
deleted along with their task links; the tasks themselves are untouched.

## AEO

Answer-engine visibility tracking; FEATURES.md section 10c. Unlike the other suites, the AEO
captures do not use live runs: a run spends provider credit and takes minutes. The suite answers
the AEO API from fixed demo data (`gocrm-ui/e2e/screenshots/helpers/aeo-fixtures.ts`). That data
has GopherCRM as the tracked brand, three fictional competitors on reserved `.example` domains,
six prompts (one switched off ten days ago), and a daily run for 30 days on a single self-hosted
engine. Only the answers are simulated: every figure is aggregated from them with the backend's
own rules, so the pages agree with each other. With more engines configured, the
per-engine cards and timeline series multiply accordingly. To regenerate only these images, on
free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/10-aeo.spec.ts`.

![AEO dashboard](screenshots/aeo/aeo-dashboard.png)
The dashboard at `/aeo`: brand-visibility gauge over the selected window, per-engine summary cards,
visibility-over-time per engine, the share-of-voice table and the competitor visibility timeline.
The 7/30/90-day selector drives every panel.

![Tracked prompts](screenshots/aeo/aeo-prompts.png)
The prompt list: each row carries its visibility bar and percentage, answer and mention counts, the
last run time and an active toggle. The prompt text opens the answer drawer.

![Answer drawer](screenshots/aeo/aeo-prompt-answers.png)
The per-prompt drawer: one card per engine and run with the full recorded answer, latency and model,
brand mentions highlighted inline, and a run selector to walk the history.

![Citations](screenshots/aeo/aeo-citations.png)
Citation comparisons at `/aeo/citations`: owned-domain citation rate against the competitors'
domains, and how often a citation coincides with a brand mention. Populated only by engines that
return sources (or answers that quote URLs), so it reads sparser than the mention metrics.

![AEO settings](screenshots/aeo/aeo-settings.png)
Settings at `/aeo/settings`: the brand profile (name, aliases, owned domains, competitors), a chip
per engine showing whether its key is configured, the daily-schedule state and the manual Run now
button.

## Users

FEATURES.md section 7.

![Users list](screenshots/users/01-list.png)
The admin-only users table with role and active status, sorted newest first.

![Create user form](screenshots/users/02-create.png)
The create form including the role selector. This screen and the `create-admin` CLI are the only
ways to obtain an elevated role.

![User detail](screenshots/users/03-detail.png)
A single user's detail page.

![Edit user form](screenshots/users/04-edit.png)
The edit form; role and active status are admin-only fields.

![Delete user confirmation](screenshots/users/05-delete-confirm.png)
The delete confirmation dialog, naming the account. Confirming erases personal data and purges the
account's API keys and refresh tokens.

## Settings

Profile relates to FEATURES.md 7.9, API Keys to section 8, and Configuration to section 9. The six
settings captures were taken on 2026-11-24 against the e2e driver's SQLite backend. The listed API
key is created through the API with a fixed name, only when missing, so no generated key is ever
on screen; the create, edit and reset dialogs are captured open and then cancelled, so nothing is
saved. To regenerate only these images, on free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/08-settings.spec.ts`.

![Profile settings](screenshots/settings/01-profile.png)
The Profile screen at `/settings/profile`: the signed-in account's details (name, email, role
chip, member since), read-only, and the Change password form with current, new and confirm fields,
show-password toggles, the password-policy hint and the Change password button.

![API keys settings](screenshots/settings/02-api-keys.png)
The API Keys screen at `/settings/api-keys`: the note that every role sees and creates only its
own keys, Create API Key, and the key list with name, key prefix, Active status chip, created,
expiry and last-used dates and the revoke action.

![Create API key dialog](screenshots/settings/03-api-key-create.png)
The Create API Key dialog with a name and an expiry date filled in and not submitted. The expiry is
optional: left empty, the key expires in one year, and it can be at most one year away.

![Configuration settings](screenshots/settings/04-configuration.png)
The admin-only Configuration screen at `/settings/configuration`: the warning banner, per-category
tabs with their value counts, and the General tab's table with key, description, type, current and
default value, the System and Read-Only columns, and the edit and reset actions.

![Edit configuration dialog](screenshots/settings/05-configuration-edit.png)
The Edit Configuration dialog opened on `general.company_name`: the key, its description and type,
the value editor and the Cancel and Save buttons. The capture cancels it.

![Reset configuration confirmation](screenshots/settings/06-configuration-reset.png)
The Reset to default? confirmation for `general.company_name`, naming the default value it would
restore. The capture cancels it; it never confirms a reset.

## Administration

The administration screens under Settings: roles and the role editor (FEATURES.md 11.1), webhooks
and their delivery log (section 10d, 10d.5 for the screen), single sign-on (1.12 and 1.13), the
sign-in audit (1.14), the role audit (11.1) and SCIM tokens (1.15). The notifications page is not
repeated here; it is shown under [Follow-up automation](#follow-up-automation)
(`docs/screenshots/automation/08-notifications-page.png`). Every record is seeded through the API with a fixed name
and created only when missing: two webhook endpoints on the reserved `.invalid` domain (which never
resolves, so no delivery leaves the machine), a lead whose creation and update fill the delivery
log, a sales user when the role audit would be empty, and a SCIM token. Endpoints and the token are
created through the API, so their one-time secret dialogs never open. Nothing is saved, revoked,
sent or deleted from the screens. These eight were captured on 2026-11-24 against the e2e
driver's SQLite backend. To regenerate only these images, on free ports against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/15-administration.spec.ts`.

![Roles](screenshots/administration/01-roles.png)
The role list at `/settings/roles`: the four system roles (Admin, Sales, Support, Customer) with
their descriptions, the System type chip, user and permission counts and a Clone action each, and
the New role button.

![Role editor](screenshots/administration/02-role-editor.png)
The built-in Sales role at `/settings/roles/{id}`, read-only: the notice that a system role cannot
be changed and can be cloned, the Clone button, name and description, and the permission tree
grouped by area, with the Own/All scope selector next to the permissions that have one.

![Webhook endpoints](screenshots/administration/03-webhooks.png)
The Endpoints tab at `/settings/webhooks`: one inactive and one active endpoint with URL,
subscribed event chips, status, created date and the Activate/Deactivate, Send test ping, Rotate
secret, edit and delete actions, plus Add endpoint.

![Webhook delivery log](screenshots/administration/04-webhook-deliveries.png)
The Delivery log tab with its endpoint, status and event-type filters, listing the `lead.updated`
and `lead.created` deliveries of one lead, newest first, with record id, status (pending: the host
never resolves), attempts, last attempt time and last response code.

![Single sign-on](screenshots/administration/05-sso.png)
The single sign-on settings at `/settings/sso` in their default state: the Enabled switch, the
switch that keeps password sign-in for linked accounts with its warning, issuer URL, client ID,
scopes, client secret, the client certificate and private key fields, the role mapping list with
Add mapping, and Save. No secret or certificate is set.

![Sign-in audit](screenshots/administration/06-sign-in-audit.png)
The sign-in audit at `/settings/sign-in-audit`: the method, outcome, reason, user id and time
filters, and the newest rows (here the capture suite's own password sign-ins) with time, method,
outcome, reason, attempts, user id, client network, browser family, request id and subject
reference. Rows hold ids, never an email or a name.

![Role audit](screenshots/administration/07-role-audit.png)
The role audit at `/settings/role-audit`: one row, a user created with the sales role, showing time,
action, method, acting and target user ids and the role.

![SCIM tokens](screenshots/administration/08-scim-tokens.png)
The SCIM token list at `/settings/scim-tokens`, after the token was created: name, created,
last-used and revoked dates and the revoke action, with Create token and the note that a token is
shown once, when it is created.

## Forms

Embeddable web forms. FEATURES.md has no section of its own for them (form retention is row 9.5);
the forms module is described in [DEV_RUNTIMES.md](DEV_RUNTIMES.md). The suite seeds a published
contact form and a draft webinar form through the API with fixed names, created only when missing,
and sends the contact form one submission through the public API, the way an embedding site does,
while it has none. The builder is captured as loaded and never saved. These three were captured on
2026-11-24 against the e2e driver's SQLite backend. To regenerate only these images, on free ports
against the e2e database:
`scripts/e2e/run.sh --config=playwright.config.screenshots.ts e2e/screenshots/14-forms.spec.ts`.

![Forms list](screenshots/forms/01-list.png)
The form list at `/forms` with name, status chip (Draft, Published), submission count and created
date, the status filter, the view, edit and delete actions and Create Form.

![Form builder](screenshots/forms/02-builder.png)
The builder at `/forms/{id}/edit` for the contact form, not saved: basics (name, description,
status), five field cards with label, machine name, placeholder, help text, required switch and the
move and remove controls, the after-submission choice with thank-you message and consent text,
notifications, follow-up and double opt-in, spam protection with reCAPTCHA and allowed domains, and
the submission action with its lead owner.

![Form detail](screenshots/forms/03-detail.png)
The contact form's detail page: the summary (status, field count, after-submission action, double
opt-in, lead and ticket creation, reCAPTCHA, notifications), the embed snippet and the hosted-page
link with copy and open buttons, and the submissions table with one received submission linked to
the lead it created.

## Error screens

FEATURES.md section 11 (role-based access and error handling).

![Access denied](screenshots/misc/01-unauthorized.png)
The Access Denied screen shown when an authenticated user opens a route their role does not allow.

![Not found](screenshots/misc/02-not-found.png)
The 404 screen for an unknown client-side route.

## Known gaps

Screens with no image because the UI does not exist yet:

- **Bulk status updates** (FEATURES.md section 10) — the endpoints are routed, but no frontend
  screen drives them.
