# What GopherCRM can do

> **For:** People deciding whether GopherCRM fits their team, and the administrators who set it up. Part of the [documentation index](../README.md).

GopherCRM is a self-hosted CRM for a small sales and support team: leads, customers, companies,
deals, quotes, tickets and tasks in one web app, with web forms, follow-up automation and
answer-engine visibility tracking next to them. This page says, area by area, what a user can do
today. How each feature is tested is in [features.md](features.md); what the shipped code does not
do is in [limitations.md](limitations.md).

## Sign-in and accounts

- Sign in with an email address and a password. Passwords need at least 10 characters with an
  upper-case letter, a lower-case letter, a digit and a special character.
- Five failed sign-ins lock the account for 15 minutes. A locked account, a deactivated account and
  a wrong password all get the same answer, so the sign-in page does not reveal which accounts
  exist.
- Forgot your password? Request a reset link by email; it works once and for one hour. Delivery
  needs an SMTP server to be configured. Signed-in users change their password on their profile
  page, which ends their other sessions.
- Stay signed in across browser restarts with "Remember me"; sessions renew themselves in the
  background, also across several open tabs.
- Single sign-on through one OpenID Connect identity provider (for example Microsoft Entra ID),
  with the provider's groups mapped to GopherCRM roles. See
  [Single sign-on](../administration/single-sign-on.md).
- SCIM 2.0 provisioning: an identity provider can create, update and deactivate staff accounts and
  manage group membership. See [SCIM provisioning](../administration/scim-provisioning.md).
- Public self-registration is off by default. When an administrator turns it on, a new sign-up
  always becomes a customer account.

## Users, roles and permissions

- Four system roles: admin, sales, support and customer. Administrators can also create custom
  roles in Settings, cloning a system role or starting from scratch, with a permission tree that
  grants each permission for all records or only the user's own.
- The web app shows pages, menu entries and buttons by the permissions a user holds, and the server
  refuses everything a role is not granted.
- Administrators create users with any role, edit them, and activate or deactivate an account. A
  deactivated user can no longer sign in or use an API key; reactivating restores access. The last
  active administrator cannot be removed.
- The role audit lists role changes and account activations, deactivations, erasures and login
  email changes. See [Roles and permissions](../administration/roles-and-permissions.md).

## Dashboard

- Totals of leads, customers, open tickets and pending tasks, and the lead conversion rate.
- A sales performance chart, the deal pipeline by stage and a "Won this month" tile with amounts
  per currency.
- Recent activity, narrowed to what the viewer may see, and upcoming tasks.
- The API also offers leads by status, tickets by priority, tasks by status, recent tickets and new
  leads for your own reports.

## Leads

- Create a lead with an email address or a phone number, then edit it and track its status and
  classification. Every lead has an owner, and sales users see and work only the leads they own;
  integrations can reassign a lead through the API.
- Search across name, email, company, phone and notes; filter by status and classification; sort
  and page through the list.
- Convert a lead into a customer, carrying its details over.
- Export the leads you may see as a CSV file.
- Integrators can look a lead up by their own external id through the API.

## Customers

- Keep a record per customer: contact name, email, phone, website, full address, notes and a link
  to a company.
- Search, sort and page through customers; see each customer's tickets, deals and quotes.
- Administrators export the customer book as a CSV file; integrations can assign a customer to a
  staff owner through the API.
- Integrators map their own ids to customers through the API.
- A customer can be given a customer-role login to follow their own tickets.

## Companies

- Group leads, customers and deals under a company with a name, a domain, an industry, an owner
  and an address.
- The company list shows live customer and lead counts. Domains are stored in one normal
  form, so the same company cannot be entered twice under the same domain.
- Deleting a company (administrators only) keeps its leads, customers and deals and removes the
  link.

## Deals and pipeline

- Follow each deal through five stages: qualification, proposal, negotiation, won and lost, with a
  default win probability per stage and a reason when a deal is lost.
- Amounts are kept in whole minor units with a currency; the default currency is an
  administrator setting.
- Every stage change is recorded, so a deal shows its full stage history.
- A pipeline board shows the deals of every stage with totals per currency, and can be narrowed to
  an owner or a company. Sales users see their own deals.
- Link deals to a company, a customer and a lead, and attach tasks to a deal.

## Products and quotes

- Keep a product catalogue with names, SKUs, prices, currencies and an active flag.
- Build quotes for a customer or a deal: lines copy the product's name and price, take quantities
  with up to three decimal places, a per-line discount and a per-line tax rate, and the net, tax and
  gross totals are worked out on the server.
- Move a quote from draft to sent, then to accepted, rejected or expired, with a status history;
  only a draft can be edited.
- Email a sent or accepted quote to the customer, optionally with a PDF of the printable quote
  attached. The email carries a link where the client can read the quote and accept or decline it
  without an account.
- Quotes are open to administrators and sales users; sales users see the quotes they own.

## Tickets

- Support staff and administrators open tickets for a customer, with a subject, description,
  priority, status and assignee.
- Reply with comments or internal notes; a staff comment can also be emailed to the customer.
- Filter by status and priority, search, sort and page. Support users work the tickets assigned to
  them; sales users can read tickets.
- Customers with a login read their own tickets and the comments that are not internal.
- A web form can open a ticket from a visitor's submission (see [Web forms](#web-forms)).

## Tasks and labels

- Create tasks with a title, description, priority, due date and assignee, linked to a lead, a
  customer or a deal, and mark them complete.
- Filter by status and priority, search, sort and page; users see the tasks assigned to them.
- Tag tasks with coloured labels, see them as chips in the list and filter the list by label.

## Web forms

- Build lead capture forms in the CRM and embed them on any website with one script tag. Each
  submission creates a lead, with the visitor's answers kept in the lead's notes.
- Spam protection in layers: a time trap, an allowlist of the sites a form may be posted from, a
  hidden honeypot field and optional reCAPTCHA v3. Spam is stored and marked, and the visitor sees
  the same answer as for a real submission.
- Optional double opt-in: the visitor confirms by email before the lead is created.
- A form can open a support ticket instead of a lead, titled from one of its answers. See
  [Forms](../user-guide/forms.md).
- Old spam and never-confirmed submissions can be purged after a retention period the operator
  sets.

## Follow-up automation and notifications

- Administrators write rules that start on lead, deal and ticket events or on a daily schedule.
- A rule runs ordered steps: wait, check a condition and branch, create a task, assign an owner,
  email a staff user or send an in-app notification.
- A dry run shows what a rule would do on a chosen record, without changing or sending anything.
- Run history with each step's outcome; administrators can cancel a run. Waits survive a restart.
- Each user has a notification bell with an unread count and a page of their own notifications.

## Answer-engine visibility (AEO)

- Describe your brand (name, aliases, domains and competitors) and write the prompts your buyers
  might ask, or let an engine suggest them.
- Run the prompts against Anthropic, OpenAI, Gemini, Kimi, Perplexity or any OpenAI-compatible
  endpoint, on demand or on a daily schedule.
- See how often each engine mentions you and your competitors, the share of voice, the sites the
  answers cite and how this changes over time. Export the results as CSV or JSON.
- Provider keys are kept encrypted in Settings, and each engine may reach only its own official
  address unless the operator allows another. See [AEO](../user-guide/aeo.md).

## API keys and outbound webhooks

- Every staff user can create named API keys for scripts and integrations. A key expires after at
  most one year, can be revoked at any time and stops working when its owner is deactivated or
  erased. See [API keys](../integrations/api-keys.md).
- The whole REST API is described by an OpenAPI reference that can be read offline
  ([api/index.html](../../api/index.html)).
- Outbound webhooks send a signed request to your endpoint when a lead, customer, company, deal,
  quote, ticket or task is created, updated or deleted. Send a test ping, rotate a signing secret
  without downtime, and read or retry deliveries in the delivery log. See
  [Webhooks](../integrations/webhooks.md).

## Settings

- Administrators edit the system configuration by category in the web app and can reset a value
  to its default: company name, default deal currency, public sign-up, lead conversion and more.
- The same Settings area holds users, roles, the role audit, the sign-in audit, single sign-on,
  SCIM tokens, webhooks and your own profile and API keys, each shown to the users whose role
  holds its permission. See [Settings and configuration](../administration/settings-and-configuration.md).

## Privacy and data

- Deleting a user, customer or lead erases their personal data in place (GDPR Art. 17), including
  the linked lead, customer and login of the same person, and removes their credentials. Tickets,
  tasks, deals and quotes stay, so the business history is complete. See
  [Privacy and erasure](../user-guide/privacy-and-erasure.md).
- Deactivating an account is the reversible alternative: access stops and nothing is erased.
- Export leads and customers as CSV, and AEO results as CSV or JSON.
- The sign-in audit keeps a pseudonymous record of sign-in outcomes, and the role audit records
  role and account changes. See [Sign-in audit](../administration/sign-in-audit.md).

## Running it

- One Go binary serves the API, next to a static web app built with React.
- Run it on SQLite with no database server, or on MySQL 8 or MariaDB 10.11, with the schema
  migrations included.
- Docker Compose files for the SQLite and the MySQL stack ([../operations/docker.md](../operations/docker.md)), a release
  tarball for a Linux server ([../operations/systemd.md](../operations/systemd.md)), or run from source
  ([../development/local-setup.md](../development/local-setup.md)).
- Built-in account lockout and per-client rate limiting; MIT licensed.

## More

- [features.md](features.md): test coverage, for contributors: every feature, its tests and its
  known gaps.
- [limitations.md](limitations.md): what the shipped code does not do, or does only
  under conditions.
- [user-guide/README.md](../user-guide/README.md): the user guide for people who use the web app and
  the administrators who set it up.
- [roadmap.md](roadmap.md): ideas that are not implemented today.
