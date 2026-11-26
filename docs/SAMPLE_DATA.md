# Sample data

> **For:** Developers who need a populated database for demos, screenshots and local development. Part of the [documentation index](README.md).

`cmd/sampledata` builds a fresh database and fills it with a fictional business. It runs the
server's own auto-migration and configuration defaults, then writes every record through the
shipped services, so the business rules hold exactly as they do for data entered through the API:
roles and ownership, lead conversion, quote totals, deal stages and probabilities, ticket comments.
It is a development tool and is not part of the release artifact.

This is a different tool from `scripts/seed_e2e_data.sh`, which seeds a running backend over HTTP.

## Quick start

```bash
export JWT_SECRET="..."      # the same value the server will run with
make sample-data             # writes sample-data/halvenmark.db (gitignored)
DB_DRIVER=sqlite DB_PATH=sample-data/halvenmark.db make run
```

Sign in as `admin@halvenmark.example` with the demo password `Halvenmark-Demo-2026!`.

## Command and flags

```bash
go run ./cmd/sampledata --profile <name> --seed <n> (--out <sqlite file> | --dsn <user:pass@tcp(host:port)/dbname>) [--scale f] [--force]
```

| Flag | Meaning |
| --- | --- |
| `--profile` | The business to generate. The only profile today is `halvenmark`; an unknown name is an error that lists the valid ones. |
| `--seed` | Random seed (default 1). The same seed on the same UTC day gives the same data. |
| `--out` | SQLite file to create. Its directory is created when missing. |
| `--dsn` | MySQL 8 or MariaDB 10.11 server and database, `user:pass@tcp(host:port)/dbname`. The database is created (`utf8mb4`, `utf8mb4_unicode_ci`) when it is missing. |
| `--scale` | Multiplies the scaled volumes below; greater than 0 and at most 10 (default 1). |
| `--force` | Deletes the existing database first and builds it again. |

Exactly one of `--out` and `--dsn` is required. The database location comes from these flags
only: `DB_DRIVER`, `DB_PATH` and the other `DB_*` settings are ignored.

`make sample-data` runs
`go run ./cmd/sampledata --profile halvenmark --seed 1 --scale 1 --force --out sample-data/halvenmark.db`.
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

The DSN, its password, the webhook signing secrets and the password hashes are never printed.

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
salts, webhook secrets, form public ids) differ on every run.

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

A scaled count is `max(1, round(n × scale))`. The first records of each set go through every
status or stage, so all of them exist even at `--scale 0.05`.

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

## Fictional names

- Company, product, deal, ticket, task, label and rule names come from curated fictional lists. No
  real company or brand is named. The commerce platforms Magento 2, Adobe Commerce, Shopify and
  PrestaShop appear only as the platform a service package or a deal concerns.
- Emails and websites use only names reserved for examples (RFC 2606 and RFC 6761):
  `*.example`, `example.com`, `example.org` and `example.net`. Staff use `@halvenmark.example` and
  companies `<name>.example`. Emails are unique, because leads and customers refuse duplicates.
- Phone numbers are in the ranges reserved for fiction: Ofcom's `+44 7700 900ddd` and the North
  American `+1 ddd 555 01dd`.
- Person names, street addresses and filler sentences come from gofakeit.

## Off by default

A generated database starts nothing on first boot and sends nothing:

- both webhook endpoints are inactive and point at `.example` hosts;
- no automation event is left to process and no webhook delivery is queued: the generator does not
  wire the automation-event and webhook options of the services;
- the one enabled automation rule has no send_email step;
- no AEO provider key and no other sensitive configuration value is written;
- no mail is sent: the services get a log-only mailer, whatever `SMTP_*` the environment holds.

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

`cmd/sampledata/sampledata_mysql_test.go` holds `TestSampleDataMySQL`, which runs the command
against `TEST_MYSQL_DSN` with a uniquely named database and drops it afterwards. It is registered
in the transaction profile (`TRANSACTION_SOURCES` in `scripts/ci/check-go.py`), which runs it on
MySQL 8 and MariaDB 10.11.
