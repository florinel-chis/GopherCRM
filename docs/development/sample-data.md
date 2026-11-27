# Sample data

> **For:** Developers who need a populated database for demos, screenshots and local development. Part of the [documentation index](../README.md).

`cmd/sampledata` builds a fresh database and fills it with a fictional business. It runs the
server's own auto-migration and configuration defaults, then writes every record through the
shipped services, so the business rules hold exactly as they do for data entered through the API:
roles and ownership, lead conversion, quote totals, deal stages and probabilities, ticket comments.
The `pelicroft` profile also sends its customers' messages through the public form path, so its
tickets and leads come from form submissions as they would on a live shop.
It is a development tool and is not part of the release artifact.

This is a different tool from `scripts/seed_e2e_data.sh`, which seeds a running backend over HTTP.

## Quick start

```bash
export JWT_SECRET="..."      # the same value the server will run with
make sample-data             # writes sample-data/halvenmark.db and sample-data/pelicroft.db (gitignored)
DB_DRIVER=sqlite DB_PATH=sample-data/halvenmark.db make run
```

Sign in as `admin@halvenmark.example` with the demo password `Halvenmark-Demo-2026!`. For the
online shop, start the backend on `sample-data/pelicroft.db` and sign in as
`admin@pelicroft.example` with `Pelicroft-Demo-2026!`.

## Command and flags

```bash
go run ./cmd/sampledata --profile <name> --seed <n> (--out <sqlite file> | --dsn <user:pass@tcp(host:port)/dbname>) [--scale f] [--force]
```

| Flag | Meaning |
| --- | --- |
| `--profile` | The business to generate: `halvenmark` (a commerce agency) or `pelicroft` (an online home-goods shop). An unknown name is an error that lists the valid ones. |
| `--seed` | Random seed (default 1). The same seed on the same UTC day gives the same data. |
| `--out` | SQLite file to create. Its directory is created when missing. |
| `--dsn` | MySQL 8 or MariaDB 10.11 server and database, `user:pass@tcp(host:port)/dbname`. The database is created (`utf8mb4`, `utf8mb4_unicode_ci`) when it is missing. |
| `--scale` | Multiplies the scaled volumes below; greater than 0 and at most 10 (default 1). |
| `--force` | Deletes the existing database first and builds it again. |

Exactly one of `--out` and `--dsn` is required. The database location comes from these flags
only: `DB_DRIVER`, `DB_PATH` and the other `DB_*` settings are ignored.

`make sample-data` runs
`go run ./cmd/sampledata --profile halvenmark --seed 1 --scale 1 --force --out sample-data/halvenmark.db`
and then
`go run ./cmd/sampledata --profile pelicroft --seed 1 --scale 1 --force --out sample-data/pelicroft.db`.
It passes `--force` because it rebuilds its own gitignored output.

### Refusals and `--force`

- **Production.** The command reads `.env` in the working directory and the environment, the same
  way the server does (`config.Load`). When `SERVER_MODE` is `production`, from either place, it
  refuses before touching any file or database, with or without `--force`.
- **Existing data.** A database whose `users` table already has rows is refused unless `--force`
  is given. Nothing is written.
- **`--force` deletes data.** With `--out` it removes the file and its `-wal` and `-shm`
  sidecars. With `--dsn` it runs `DROP DATABASE` on the database the DSN names, then creates it
  again. Only that database is dropped, but everything in it is lost. Never point `--dsn --force`
  at a database you want to keep.

The DSN, its password, the webhook signing secrets, the password hashes and the form confirmation
tokens and links are never printed.

### JWT_SECRET

`JWT_SECRET` is required, as for the server and `cmd/create-admin`. Use the value the server will
run with. The webhook endpoint secrets are sealed with `API_KEY_SECRET`, or `JWT_SECRET` when it is
unset, so a server with another secret cannot read them. Rebuilding the file with the right secret
fixes that. Sign-in works whatever the secret, because passwords are bcrypt hashes.

### Determinism

Fake values come from [gofakeit](https://github.com/brianvoe/gofakeit) v7.18.2, from a seeded
instance only, and are used only for person names, street addresses, sentences, paragraphs and
date jitter. The history ends at the start of the current UTC day, so two runs with the same seed
on the same day write the same names, statuses, amounts and dates. Generated secrets (password
salts, webhook secrets, form public ids, form confirmation tokens) differ on every run.

## The `halvenmark` profile

"Halvenmark Commerce Studio" is a fictional agency that builds and runs online shops for small
retailers. `general.company_name` is set to that name. The curated lists live in
`cmd/sampledata/halvenmark.go`.

### Fixed records (not scaled)

- **14 users**:
  - two admins: `admin@halvenmark.example` and `tobias.wrenfield@halvenmark.example`;
  - five sales users: `ilse.drummond`, `caspar.ollerton`, `priya.castellane`, `jonah.featherby`
    and `odette.marchbank`, all `@halvenmark.example`;
  - four support users: `rafe.tilbury`, `senna.holloway`, `bram.achterberg` and `lucia.penrose`;
  - two users with the custom role "Delivery lead", `esme.ravensworth` and `dorian.kettleby`, who
    read customers, companies, deals, products, quotes and tickets and manage tasks;
  - one customer login, `nadia.okonkwo@larkhollow-ceramics.example`. It is linked to the customer
    record of Larkhollow Ceramics, which has tickets.
- **15 products**: service packages in EUR, for example "Magento 2 Upgrade Sprint" and "Shopify
  Theme Build".
- **8 task labels**.
- **3 automation rules**. Only "Call back new leads" is enabled. It is triggered by lead_created
  and has only create_task and notify steps. "Email the owner when a deal is won" (send_email) and
  the schedule-triggered "Nudge stalled proposals" are disabled.
- **1 lead form**, "Project enquiry": published, allowed origin `www.halvenmark.example`, no
  submissions.
- **2 webhook endpoints** on `https://hooks.halvenmark.example/...`, both inactive.

### Scaled records (volume at `--scale 1`)

A scaled count is `max(1, round(n × scale))`, and a volume of 0 stays 0 (halvenmark has none).
The first records of each set go through every status or stage, so all of them exist even at
`--scale 0.05`.

| Records | Volume | Notes |
| --- | --- | --- |
| Leads | 600 | All five statuses; sources website, referral, conference and partner; owned by sales users. 60 are converted through the lead service. |
| Customers | 250 | The 60 converted leads included; most belong to a company. |
| Companies | 120 | Fictional names on `<name>.example` domains. |
| Deals | 180 | All five stages, EUR, 3,000 to 180,000 EUR, with their stage history. |
| Quotes | 140 | All five statuses, 1 to 4 product lines each, on customers and deals. |
| Tickets | 400 | On customers, assigned to support, each with 1 to 8 comments; some comments are internal notes. |
| Tasks | 800 | Overdue, upcoming, completed and cancelled; assigned to staff; many have labels. |
| Notifications | 60 | For staff users; some are read. |

Dates are spread over the 18 months before the anchor day. The services stamp the current time and
have no clock option, so the generator rewrites `created_at`, `updated_at` and the times the
services stamp afterwards (`cmd/sampledata/backdate.go`). The order stays consistent: a comment is
never older than its ticket, a stage change never older than its deal, a converted customer never
older than its lead. Overdue and upcoming tasks are due before and after the anchor day.

### What the profile does not contain

- **Labels on leads.** Labels exist only on tasks in GopherCRM (`models.Task.Labels`), so the
  profile labels tasks. Leads have no labels.
- **Quote PDFs.** `GET /quotes/{id}/pdf` renders a PDF on demand when `QUOTE_PDF_CHROME_PATH` is
  set; nothing is stored, so the generator writes none.

## The `pelicroft` profile

"Pelicroft Home" is a fictional online shop for kitchenware, table linen, bedding, candles and
decor. `general.company_name` is set to that name. The curated lists live in
`cmd/sampledata/pelicroft.go`, and the storefront steps in `cmd/sampledata/storefront.go`.

Where halvenmark writes its leads and tickets directly, pelicroft lets them arrive the way a live
shop's do: every shopper message, wholesale enquiry and newsletter sign-up is a submission through
`FormService.SubmitPublic` (origin `https://www.pelicroft.example`, a page URL on that host, an IP
address in the RFC 5737 documentation ranges, a browser user agent). Contact submissions that are
not spam are confirmed at once through `FormService.ConfirmSubmission`, so form ticket intake opens
the ticket, and the automation events the form path records are dispatched and executed by the
automation engine, in-process, before the command finishes.

### Fixed records (not scaled)

- **8 users**, all `@pelicroft.example` except the customer login:
  - one admin, `admin@pelicroft.example`;
  - one user with the custom role "Store manager", `linnea.ashworth@pelicroft.example`, who reads
    customers, companies, deals, products, quotes and tickets and views, creates and updates
    tasks. Only the triage rule gives this user tasks;
  - four support users: `tamsin.greaves`, `elio.marchetti`, `hanne.vestergaard` and
    `ruairi.delaney`;
  - one sales user for wholesale, `bastien.morrow`, the default owner of the wholesale leads;
  - one customer login, `margit.lindqvist@example.com`, linked to a shopper customer record that
    has contact-form tickets.
- **40 products**: the wholesale catalogue in EUR, SKUs `PHW-KIT-...`, `PHW-TAB-...`,
  `PHW-BED-...`, `PHW-CAN-...` and `PHW-DEC-...`.
- **8 task labels**.
- **3 published forms**, created through the form service, without captcha and without
  notification addresses:

  | Form | Does | Fields | Allowed origins |
  | --- | --- | --- | --- |
  | Contact us | `create_ticket`, `double_opt_in`, `ticket_title_field` `topic` | `name` (text, required), `email`, `order_number` (text), `topic` (select: Order status, Return or exchange, Damaged item, Product question, Other), `message` (textarea, required) | `www.pelicroft.example`, `localhost:8099` |
  | Wholesale enquiry | `create_lead`, owner the wholesale sales user | `name`, `email`, `company`, `phone`, `message` | `www.pelicroft.example` |
  | Newsletter | stores the sign-up only | `email`, `first_name` (optional) | `www.pelicroft.example` |

- **3 automation rules**, created before the first submission:
  - "Triage contact-form tickets" (enabled): on `ticket_created`, a create_task step for the store
    manager, due 2 days after it runs;
  - "Tell wholesale about new enquiries" (enabled): on `lead_form_submitted` of the Wholesale
    enquiry form, a notify step to the lead's owner;
  - "Email the assignee when a ticket is resolved" (disabled): a send_email step on
    `ticket_status_changed` to resolved.

  No enabled rule has a send_email or wait step.

### Scaled records (volume at `--scale 1`)

| Records | Volume | Notes |
| --- | --- | --- |
| Customers | 3,000 | 2,960 shoppers on `example.com`, `example.org` and `example.net`, plus the 40 converted boutiques. |
| Contact submissions | 1,500 | Spread over the months in proportion to their days, November and December weighted three times; see below. |
| Wholesale enquiries | 150 | One lead each, every one from a unique boutique address on `<name>.example`; some moved to contacted or qualified. |
| Converted wholesale leads | 40 | Linked to a boutique company, then converted with the lead service. |
| Boutique companies | 60 | Fictional names on `<name>.example` domains. |
| Deals | 45 | On the converted boutiques, with the halvenmark stage paths and their stage history. |
| Quotes | 40 | Priced from the catalogue through the quote service. |
| Newsletter sign-ups | 300 | Stored as received. |
| Manual tasks | 400 | For admin, support and sales users, never for the store manager. |

The first contact submissions are one honeypot spam, one from a disallowed origin
(`https://bulk-offers.example.net`), one that trips the time trap, and one from an address that
is no customer's. After those, about 92% come from existing shoppers, 4% from unknown
`example.com` addresses and 4% are spam. So the forms hold:

- Contact us: `confirmed` submissions with ticket outcome `created` (about 1,390 tickets at
  `--scale 1`) or `no_customer` (no ticket), and `spam` submissions (honeypot, domain or
  time_trap) with no ticket, no key and no confirmation token. None stays `pending`.
- Wholesale enquiry and Newsletter: `received` submissions (they have no double opt-in).

Each ticket the intake opens is handled through the ticket service: it gets a support assignee, 1
to 6 comments by support or admin (some internal notes, written with `CreateComment`, so no mail),
a priority from the topic support read (Damaged item high, Product question and Other low, the rest
medium), a due date 72 hours after it opened, and a status, with a resolution when resolved or
closed. The triage rule then gives the store manager one task per ticket, and the wholesale rule
gives the wholesale sales user one notification per wholesale lead. Every automation run completes.

The summary the command prints for pelicroft ends with one more line,
`contact form spam planned: N`, the number of contact submissions the generator sent as spam.

Ticket statuses are `open`, `in_progress`, `resolved` and `closed`; a ticket that is "pending" in
the shop's own words (waiting on the customer or the courier) is `in_progress`.

The history spans the 18 months before the anchor day, as for halvenmark. Besides `created_at`
and `updated_at`, the generator moves every stamp the real path writes at run time into it: a
submission's `confirmed_at`, the token's `used_at` and `expires_at` (48 hours after the
submission), the mail delivery's `sent_at` and `last_attempt_at`, the ticket's
`first_responded_at` and `due_at`, the ticket key, the event's `processed_at`, the run's
`claimed_at`, `heartbeat_at`, `resume_at` and `finished_at`, the run step's `executed_at`, the rule
steps, and the triage task (due 2 days after its step ran) and notification. A stamp the path left
empty stays empty. Roles and role permissions, the audit logs and the configuration rows keep their
real time, as for halvenmark. The order holds: a ticket is never older than its submission, a lead
never older than its submission, a converted customer never older than its lead, and an event, run,
task or notification never older than the ticket or lead it belongs to.

### What the profile does not contain

- **A topic-based rule.** Ticket rules can test only a ticket's status, priority, assignee and
  customer; the topic and the order number stay on the submission. So the store manager triages
  every contact-form ticket, not only the damaged-item ones.
- **The submitted text on the ticket.** Only the topic reaches the ticket: the form's
  `ticket_title_field` is `topic`, so each ticket is titled "Contact us: <topic>" (for example
  "Contact us: Damaged item"). The description names the form and submission ids only, and the
  message, name and order number stay on the submission, where staff read them.
- **Mail to shoppers.** The confirmation mail is captured in memory by the generator and never
  sent; staff comments are written without customer mail.

### Trying the contact form by hand

1. Start the backend on `sample-data/pelicroft.db` with `SMTP_HOST` and `SMTP_PORT` pointing at a
   local mail catcher (for example `SMTP_HOST=127.0.0.1 SMTP_PORT=1025`), and `PUBLIC_BASE_URL`
   set to the backend's own origin, so the confirmation link reaches it.
2. Sign in as `admin@pelicroft.example`, open Forms → Contact us and copy the embed snippet. It has
   the shape
   `<script src=".../forms/public/embed.js" data-form-key="..." async></script>`.
3. Put the snippet in a local HTML page and serve it on `http://localhost:8099`, for example with
   `python3 -m http.server 8099` in the page's folder. That origin is on the form's allowlist.
4. Submit the form with the email of a generated shopper (Customers lists them). Wait at least
   three seconds after the page loads, or the time trap files it as spam.
5. Open the confirmation link from the mail catcher. A "Contact us: <topic>" ticket opens for that
   shopper (the form's `ticket_title_field` is `topic`; with no topic chosen it is "Form: Contact
   us"), and the automation worker (`AUTOMATION_ENABLED`, on by default) gives the store
   manager a triage task at its next poll. An address that
   belongs to no customer is confirmed too, with ticket outcome `no_customer`.

## Fictional names

- Company, product, deal, ticket, task, label and rule names come from curated fictional lists. No
  real company or brand is named. The commerce platforms Magento 2, Adobe Commerce, Shopify and
  PrestaShop appear only as the platform a service package or a deal concerns.
- Emails and websites use only names reserved for examples (RFC 2606 and RFC 6761):
  `*.example`, `example.com`, `example.org` and `example.net`. Staff use `@halvenmark.example` or
  `@pelicroft.example`, companies `<name>.example`, and pelicroft's shoppers and newsletter readers
  `example.com`, `example.org` and `example.net`. Emails are unique, because leads and customers
  refuse duplicates.
- Phone numbers are in the ranges reserved for fiction: Ofcom's `+44 7700 900ddd` and the North
  American `+1 ddd 555 01dd`.
- Pelicroft's form submissions come from IP addresses in the RFC 5737 documentation ranges
  (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24).
- Person names, street addresses and filler sentences come from gofakeit.

## Off by default

A generated database starts nothing on first boot and sends nothing:

- halvenmark's two webhook endpoints are inactive and point at `.example` hosts; pelicroft has
  none, and no profile wires the webhook option of a service, so no webhook delivery is queued;
- no automation event is left to process and no run is unfinished. halvenmark does not wire the
  automation-event options of the services. pelicroft wires them only on its form service, and the
  generator drains what that records in-process with the automation engine (`Dispatch`, then
  `ExecuteDue`) before it finishes: every event is processed and every run completed;
- no enabled automation rule has a send_email or wait step;
- no AEO provider key and no other sensitive configuration value is written;
- no mail is sent, whatever `SMTP_*` the environment holds: the services get a log-only mailer,
  and pelicroft's form service an in-process mailer that keeps the confirmation mail in memory for
  the generator to read the link from (its delivery rows end `sent`, so nothing is retried on
  boot). Pelicroft's ticket comments are written without customer mail, so it has no ticket mail
  delivery.

## Tests

`cmd/sampledata/sampledata_test.go` runs on SQLite at `--scale 0.05`:

- `TestSampleDataDeterministic`: same counts and same hash with the same seed, a different hash with
  another seed.
- `TestSampleDataReservedDomains`: every email host, URL host, company domain and phone column
  number in every table is in a reserved range. It also shows that the scan reports one that is
  not.
- `TestSampleDataRefusals`: production mode, existing data without `--force`, the rebuild with
  `--force`, and the flag errors.
- `TestSampleDataScale`: the counts, the business rules, the backdated window and the
  off-by-default state. It includes an SMTP listener named by `SMTP_HOST` that must receive no
  connection.
- `TestSampleDataSignIn`: every generated user signs in with the demo password, and the summary
  prints the admin email and the password once each.

`cmd/sampledata/pelicroft_test.go` runs the `pelicroft` profile on SQLite at `--scale 0.05`:

- `TestSampleDataPelicroftDeterministic`: same counts and same hash (form submissions included)
  with the same seed, a different hash with another seed.
- `TestSampleDataPelicroftReservedDomains`: the reserved-name scan over every table, form
  submissions included.
- `TestSampleDataPelicroftScale`, with the subtests `form_tickets` (every ticket came from a
  confirmed Contact us submission through ticket intake), `spam` (each spam layer caught some, and
  the count is what the generator planned), `wholesale`, `automation` (the drained outbox, one
  triage task per ticket and one notification per wholesale lead), `timestamps` (every `_at` column
  of the backdated tables, read from the schema, inside the window, the run-time stamps
  included; the relative columns and the order of the records), `seasonality` and
  `off_by_default`, with the same SMTP listener as `TestSampleDataScale`.
- `TestSampleDataPelicroftSignIn`: every generated user signs in with the demo password.

`cmd/sampledata/sampledata_mysql_test.go` holds `TestSampleDataMySQL`, which runs the command
against `TEST_MYSQL_DSN` with a uniquely named database per profile, one subtest each
(`halvenmark`, `pelicroft`), and drops them afterwards. It is registered in the transaction
profile (`TRANSACTION_SOURCES` in `scripts/ci/check-go.py`), which runs it on MySQL 8 and
MariaDB 10.11.
