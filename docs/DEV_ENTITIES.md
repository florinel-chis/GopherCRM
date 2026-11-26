# Entities

> **For:** Developers changing the rules of the business entities. Part of the [documentation index](README.md).

The rules of the business entities: quotes and their links, mail delivery, ordered and paginated
reads, tasks and tickets, list filters, lookup indexes, lead owners and the lead and company lock
order, and the bulk status endpoints. Deleting personal data is in
[DEV_ERASURE.md](DEV_ERASURE.md). Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

## Quotes

**Quotes: conditional claims, one transaction each** — every write to a quote or its lines runs in
one transaction that starts with a locking read of the quote (`QuoteRepository.GetByIDForUpdate`,
`SELECT … FOR UPDATE` on MySQL, a plain read on SQLite where the dialector drops the clause) and
refuses anything but a draft with `apperrors.ErrQuoteLocked`; the totals are recomputed from the
lines inside that transaction and written by a conditional update `WHERE status = 'draft'`
(`UpdateTotals`, exactly one row, else the whole write rolls back). A status transition
(`QuoteService.ChangeStatus`) claims the row with `UPDATE … WHERE id = ? AND status = <from>`
(`ClaimStatus`; exactly one row, else `ErrInvalidQuoteTransition`, which is also the lost-race
answer), then — for `draft → sent` only — counts the lines with a locking read *after* the claim
(`CountLinesForUpdate`; zero rolls the claim back with `ErrQuoteHasNoLines`), and appends the
`quote_status_changes` row in the same transaction. The line count is read after the claim on
purpose: a plain count before it would answer from the REPEATABLE READ snapshot on MySQL and miss
a line deleted by a competing transaction. Two competing transitions therefore commit exactly one,
and a line write racing the send is either inside the sent quote's totals or refused as locked; no
committed sent quote has zero lines. The conditional updates re-check their own conditions when
the MySQL driver reports zero rows for an update that wrote the values the row already held.
Accepting a quote never touches its deal (owner decision). Regression roots:
`TestQuoteStatusTransitionMatrix`, `TestQuoteLineLimitsRejectedWithoutWrites`,
`TestQuoteCreateWritesQuoteAndHistoryTogether` (`internal/service/quote_service_test.go`),
`TestQuoteStatusConcurrent{SQLite,MySQL}` (`internal/service/quote_status_regression_test.go`; the
MySQL root creates a disposable schema on `TEST_MYSQL_DSN`, skips without it and fails when it is
unreachable) and `TestQuoteRepository_LockingReadsCarryForUpdateOnMySQLOnly`.

**Quote tax (#324).** A line carries `tax_rate_bp` (0..10000, `models.QuoteTaxRateBPMax`; 2100 is
21 %), written like every other line field: only while the quote is a draft (`lockDraft` refuses
anything else with `ErrQuoteLocked`, 409). `POST /quotes/{id}/lines` binds it as an `int64` (absent
is 0) and `PUT /quotes/{id}/lines/{lineId}` as an `*int64` (absent keeps the stored rate, 0 clears
it), both `binding:"min=0,max=10000"`, so an out-of-range value is 400 `VALIDATION_ERROR` before the
service is called; `validateLineBounds` repeats the bound, so the service refuses it with
`apperrors.ErrValidation` before any transaction starts. `computeLine` sets `line_total_cents` and
then `tax_cents = models.ComputeQuoteLineTax(line_total_cents, tax_rate_bp)`, `(total × bp + 5000) /
10000` round half up. `recomputeTotals` sums the lines with `models.SumQuoteTotals` (subtotal,
discount and net total exactly as `ComputeQuoteTotals`, tax as Σ line `tax_cents`, gross as total +
tax) and refuses a gross total above 10^12 (`QuoteAmountCentsMax`, now the gross bound; the net
total is never larger) with `ErrValidation`, rolling the whole write back. `UpdateTotals(id,
models.QuoteTotals)` writes all five totals and `updated_at` under the same `WHERE id = ? AND
status = 'draft'`, with the `matched()` re-check. `total_cents` keeps its meaning (net after
discount); tax is rounded per line, so the quote tax can differ by cents from tax on per-rate
totals. MySQL/MariaDB get the columns from `20261117120000_add_quote_tax` (each only when missing,
plus one gross backfill); SQLite from `MigrateDatabase`, whose `backfillQuoteGrossTotals` step sets
`gross_total_cents = total_cents + tax_cents` by primary key wherever it differs, on every driver and
without touching `updated_at`, so a backfilled database takes no write. Regression roots:
`TestQuoteLineTaxCompute`, `TestQuoteTotalsWithTax`, `TestQuoteTaxColumnsSchemaSQLite`,
`TestQuoteTaxSQLiteUpgrade`, `TestQuoteTaxJSON` (`internal/models`); `TestQuoteLineTaxRateBinding`
(`internal/handler`); `TestQuoteTaxRateBoundsRejectedWithoutWrites`,
`TestQuoteLineTaxRounding{SQLite,MySQL}`, `TestQuoteGrossCapRefusalWritesNothing{SQLite,MySQL}`,
`TestQuoteTaxRateNonDraftRefused{SQLite,MySQL}`, `TestQuoteEmailShowsTax` (`internal/service`);
`TestQuoteRepositoryUpdateTotalsWithTax{SQLite,MySQL}` (`internal/repository`);
`TestQuoteTaxMigrationFiles` (it held the newest-migration assertion until #343 moved it to
`TestQuoteQuantityMigrationFiles`), `TestQuoteTaxMigration` (`cmd/migrate`); and, over HTTP,
`TestQuoteTaxAPISQLite` (`tests/quote_tax_test.go`). The MySQL roots skip without
`TEST_MYSQL_DSN`; the migration roots run in the Database migrations workflow. Registering the
other roots in `scripts/ci/check-go.py` is an operator follow-up.

**Quote quantities (#343).** A line quantity is an exact decimal from 0.001 to 1,000,000 with no
non-zero digit beyond the third decimal place.

- **The type.** `models.QuoteQuantity` (`internal/models/quote_quantity.go`) holds integer
  thousandths (`Milli()`); it implements `json.Marshaler`/`Unmarshaler`, `driver.Valuer` and
  `sql.Scanner`, and maps to `quote_lines.quantity_milli` (`bigint NOT NULL DEFAULT 0`, JSON key
  `quantity`). `ParseQuoteQuantity` and `UnmarshalJSON` read the JSON number literal exactly, never
  through a float64: exponent forms and trailing zeros are judged by value (`1e-3`, `1.5000`,
  `1000000.000` are accepted); zero, `-0`, a negative, a value above 1,000,000, a non-zero digit past
  the third decimal, a literal over 64 bytes or an exponent magnitude above 30 is a
  `*models.QuoteQuantityError` (checked before any digit is converted); a string, boolean, array or
  object is another error. `MarshalJSON` and `String()` write the shortest decimal (`1.5`, `2`,
  `0.001`), so a whole number round-trips byte-identical. `QuoteQuantityFromMilli` and
  `QuoteQuantityFromUnits` are unchecked constructors (tests and the scanner); the constants are
  `QuoteQuantityScale` (1000), `QuoteLineQuantityMilliMin` (1) and `QuoteLineQuantityMilliMax`
  (10^9); `QuoteLineQuantityMax` (10^6 units) is kept.
- **The handler.** `QuoteLineRequest.Quantity` and `QuoteLineUpdateRequest.Quantity` are
  `*models.QuoteQuantity` (`binding:"required"` on add only). When `ShouldBindJSON` fails with a
  `*models.QuoteQuantityError`, `respondQuoteLineBindError` answers 400 `VALIDATION_ERROR`, message
  `Validation failed`, `error.details.quantity` = `models.QuoteQuantityBoundsMessage`, before any
  read; every other bind error goes to the error middleware as before (a string quantity is 400
  `BAD_REQUEST`, a missing or null one on POST 400 `VALIDATION_ERROR` keyed `Quantity`). An absent
  or null quantity on PUT keeps the stored value.
- **The arithmetic.** `models.ComputeQuoteLineAmounts(quantity, unitPriceCents, discountBP)`
  returns `gross = round_half_up(milli × price / 1000)` and `lineTotal = round_half_up(milli × price ×
  (10000 − bp) / 10^7)` in 128-bit integer arithmetic (`math/bits`), exact up to milli 10^9 and price
  10^12; `ok` is false when the rounded gross exceeds `QuoteAmountCentsMax`. The line total is
  rounded once from the exact product, so `discount_cents` can differ by a cent from rounding the
  gross first. `ComputeQuoteTotals` and `SumQuoteTotals` take the subtotal as the sum of the rounded
  line gross values. For whole quantities every result equals the former integer formula, which
  `ComputeQuoteLineTotal(int64 units, …)` keeps. `validateLineBounds` refuses a quantity outside
  1..10^9 thousandths with `apperrors.ErrValidation` before any transaction starts; `computeLine`
  refuses a rounded gross above 10^12 inside it.
- **The email.** `RenderQuoteEmail` prints `quantity 1.5 x …`; a whole quantity renders exactly as
  before (`quantity 3 x 9.99 EUR`).
- **The schema.** MySQL/MariaDB: `migrations/20261118120000_store_quote_line_quantity_milli.up.sql` adds
  `quantity_milli` when missing, copies `quantity × 1000` where it is still 0 while `quantity`
  exists, and drops `quantity`, each step behind an `INFORMATION_SCHEMA` probe; its down half
  re-adds `quantity bigint NOT NULL` (no default), sets it to `GREATEST(1, (quantity_milli + 500)
  DIV 1000)` and drops `quantity_milli`. Every driver: after AutoMigrate (which adds
  `quantity_milli` and never drops a column), `MigrateDatabase` runs
  `migrateLegacyQuoteLineQuantity`, which does nothing when `quantity` is absent and otherwise
  converts the rows still at 0 by primary key in chunks of 500 (plain `SELECT` discovery,
  `updated_at` untouched) and then issues the native `ALTER TABLE quote_lines DROP COLUMN quantity`
  (SQLite 3.35+, MySQL, MariaDB). Never use `Migrator().DropColumn` for it: with a table name the
  glebarez/sqlite migrator dereferences a nil schema and panics, and with a model it rebuilds the
  table through `recreateTable` and loses `idx_quote_lines_quote_id` and
  `idx_quote_lines_product_id`. The legacy column has no index, constraint or foreign key, so the
  native drop needs no rebuild. The parity reference of the migration is the schema
  `MigrateDatabase` builds on the previous head, not plain AutoMigrate; the earlier quote migration
  roots filter both quantity columns (`withoutQuoteQuantityColumns`).
- **Upgrade and rollback.** Stop every replica before starting the new version: an old binary
  cannot write quote lines once `quantity` is dropped. An older binary's AutoMigrate re-adds
  `quantity bigint NOT NULL` without the values, so every line would read 0: before starting one,
  run the down migration (MySQL/MariaDB) or restore the pre-upgrade backup (SQLite, which has no
  down path). The down rounds fractional quantities and leaves the stored totals as they are.

Regression roots: `TestQuoteQuantityParse`, `TestQuoteQuantityJSON`, `TestQuoteLineAmounts`,
`TestQuoteQuantitySQLiteUpgrade` (`internal/models`); `TestQuoteLineQuantityBinding` and the edited
`TestQuoteLineBindingTagsMatchTheModel` (`internal/handler`);
`TestQuoteFractionalQuantityBoundsRejectedWithoutWrites`,
`TestQuoteFractionalQuantityTotals{SQLite,MySQL}`, `TestQuoteEmailFractionalQuantity`
(`internal/service`); `TestQuoteLineQuantityStorage{SQLite,MySQL}` (`internal/repository`);
`TestQuoteQuantityMigrationFiles` (it held the newest-migration assertion until #344 moved it to
`TestRoleMigrationFiles`) and
`TestQuoteQuantityMigration` (`cmd/migrate`); and, over HTTP, `TestQuoteFractionalQuantityAPISQLite`
(`tests/quote_quantity_test.go`). The existing quote tests changed only how they build quantities
(`models.QuoteQuantityFromUnits`). The MySQL roots skip without `TEST_MYSQL_DSN`; the migration
roots run in the Database migrations workflow. Registering the other new roots in
`scripts/ci/check-go.py` is an operator follow-up.

**Quote email: no transaction during SMTP, a conditional stamp after it** — `QuoteService.Email`
reads the latest `quote_status_changes` id *first*, then the quote and the customer's *current*
address, hands the plaintext rendering to the `Mailer` with no transaction held, and then stamps
`last_emailed_at` with one conditional update (`StampEmailed`): `WHERE id = ? AND status IN
('sent','accepted') AND NOT EXISTS (a history row of the quote with to_status = 'draft' and an id
above the marker)`. A `sent → draft` revision that commits after the marker was read — while the
mail is in flight, or between the marker read and the read of the quote, where the rendering that
went out would predate it — even one followed by a fresh send, which the status alone could not
tell apart, matches no row, and the call answers 409 `ErrQuoteChangedDuringSend` with
`last_emailed_at` unchanged. Keep the marker read before the quote read: the other order leaves a
window in which a stale rendering is recorded as the last email. A `Mailer` failure is 502
`MAIL_DELIVERY_FAILED` and stamps nothing. The development `LogMailer` (no `SMTP_HOST`) is not a
failure: its `Send` logs and returns nil, so the quote is stamped although nothing was delivered —
the same fallback the password reset shares (form mail records it `log_only`, see the README's
known limitations). The subject is `Quote Q-000042 from <name>`, the name resolved at send time by
`service.ResolveMailProductName` from the configuration source the service already holds for the
default currency (see *Mail product name*, #292; `GopherCRM` when that source is nil),
the recipient is never copied onto the quote, the body (`RenderQuoteEmail`) ends every line with
` | tax <rate>% <tax>` (also at 0 %) and closes with `Subtotal:`, `Discount:`, `Net total:`
(`total_cents`), `Tax:` and `Gross total:` (#324), and the log lines of the call carry `quote_id` and
`customer_id` only — a transport error that quotes the address is returned, not logged. Roots:
`TestQuoteEmail{Delivery,LogsOmitPersonalData,RevisedDuringSend}` (`internal/service/quote_email_test.go`)
and `TestQuoteRepository_StampEmailedRefusesANewerRevisionToDraft`.

**Quote PDF: a headless browser prints the SPA's render page (#384)** — `service.WithQuotePDF(svc,
renderer)` switches PDF output on for a quote service (`cmd/main.go` does it when
`NewChromeQuotePDFRenderer(QUOTE_PDF_CHROME_PATH)` returns a renderer; an empty path is off, a
path that is not an executable file is off with one warn line; an empty `APP_BASE_URL` is off
too). The pieces, all in `internal/service/quote_pdf.go` and `quote_pdf_chrome*.go`:

- **Render step.** `RenderPDF(id)` (behind `GET /quotes/:id/pdf`) and `Email` call
  `renderQuotePDF`: take the single render slot, mint a credential, call
  `QuotePDFRenderer.Render(ctx, APP_BASE_URL + "/quote-render#" + token)`, and require that the
  page redeemed the credential exactly once and loaded the quote with it before the bytes
  count. Every failure is `apperrors.ErrQuotePDFRenderFailed` (503 `PDF_RENDER_FAILED`) with a
  reason in the log (`no_turn_within_bound`, `timeout`, `renderer_error`,
  `page_never_loaded_the_quote`, `credential_redeemed_more_than_once`); the
  renderer's own error is dropped, because nothing guarantees it is free of the URL. PDF off is
  `apperrors.ErrQuotePDFUnavailable` (503 `PDF_UNAVAILABLE`).
- **Render credential.** 32 random bytes (`generateOpaqueToken`), stored only as a hash in an
  in-memory map of the service with the quote id and an expiry equal to the bound, deleted when
  the render returns. It travels in the URL **fragment**, so the server that serves the page
  never receives it, and then in the body of `POST /quotes/public/render`
  (`QuoteService.GetRenderView`), which returns `models.QuoteRenderView` (the quote as
  `GET /quotes/:id` serialises it, and the trimmed `general.company_name`) for the credential's
  own quote and nothing the caller names. Every refusal is `apperrors.ErrQuoteRenderRejected`,
  one generic 400. It is single use: the lookup, the expiry check and the count of
  presentations are one critical section, so only the first presentation gets the quote; a
  second one, concurrent or not, is the rejection and fails that render. The render page
  (`QuoteRender.tsx`) therefore sends its one request behind a ref, also under StrictMode's
  development double effect. It is not a session: the Auth middleware does not know it. A browser that
  exits cleanly without the credential having been redeemed printed something else (an error
  page: `APP_BASE_URL` unreachable, CORS refusing the call), so that render fails.
- **Fallback in the email.** `Email` renders after the recipient checks and before
  `mintResponseLink`, with no transaction held. With PDF off, a mailer that does not implement
  `mailer.AttachmentSender`, or a failed or late render, the mail goes out through `Mailer.Send`
  exactly as before and one warn line carries the quote id, the customer id and the reason. The
  render's failure is never an error of the email. Otherwise the mail goes out through
  `SendWithAttachment` with `Quote-<number>.pdf` (`application/pdf`): `multipart/mixed`, the text
  part as `Send` composes it, the file base64 in 76-column lines; a filename with CR, LF, a quote
  or a backslash is refused before the relay is contacted.
- **Built child environment.** `ChromeQuotePDFRenderer` starts one browser per render with a
  fresh temp dir as `--user-data-dir`, `HOME`, `TMPDIR`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME`,
  and an environment that is built, not inherited: `PATH`, `LANG`, `LC_ALL` and `TZ` when set,
  plus those directories. `JWT_SECRET`, `API_KEY_SECRET`, `DB_PASSWORD` and `SMTP_PASSWORD`
  never reach a browser process, and a hardened unit (`ProtectHome`, `ProtectSystem=strict`,
  `PrivateTmp`) or a read-only root file system leaves it everything it needs. The browser is the
  leader of its own process group and the whole group is killed at the bound and after every
  render, so no helper survives. Flags: `--headless`, `--remote-debugging-pipe`,
  `--use-mock-keychain` and `--password-store=basic` (with `HOME` in a temp dir a macOS Chrome
  otherwise waits for a login keychain it cannot find). The list is one for both kinds of binary, `chrome-headless-shell`
  (what the Docker image ships since #393) and a full Chrome or Chromium: the full browser needs
  `--headless` and the keychain switches, the headless shell accepts and ignores them.
  `--no-sandbox` is never passed from Go: an image that needs it sets it in its launcher (the
  Dockerfile writes `/usr/local/bin/quote-pdf-browser`, which execs the headless shell). Neither the URL,
  its fragment nor anything the browser sent or wrote is logged or wrapped into an error.
- **The browser is driven over its DevTools pipe (#396).** The argument list holds no URL. The
  browser is started with `--remote-debugging-pipe`, reads commands on its file descriptor 3 and
  writes replies and events on 4 (`cmd.ExtraFiles`; one JSON object and a NUL byte per message),
  and a small client on the standard library (`quote_pdf_chrome_pipe.go`, no Go dependency)
  sends `Target.createTarget` (`about:blank`), `Target.attachToTarget` (`flatten`),
  `Page.enable`, `Page.navigate` with the render URL, waits for `Page.loadEventFired`, sets
  `Emulation.setVirtualTimePolicy` (`pauseIfNetworkFetchesPending`, budget
  `quotePDFVirtualTimeBudgetMS`) and waits for `Emulation.virtualTimeBudgetExpired`, so the
  page's fetch finishes and is painted, then `Page.printToPDF` (`printBackground`, no header or
  footer, `preferCSSPageSize`) and decodes the base64 answer. The render URL, whose fragment is
  the credential, is therefore on no command line, and no TCP debugging port is opened: only the
  backend process holds the other ends of the pipe. A reply with an error, a navigation that
  reports an `errorText`, EOF on the pipe or a browser that exits fails the render at once
  rather than at the bound, and an answer that does not start with `%PDF` is refused. The
  renderer needs a Unix host: file descriptors beyond the standard three cannot be handed to a
  child on Windows, where the server builds and every render fails (503 `PDF_RENDER_FAILED`).
  A launcher script named by `QUOTE_PDF_CHROME_PATH` must not close or redirect file
  descriptors 3 and 4.
- **Time budget (a contract, tested).** `QuotePDFRenderTimeout` = 15 s is the only definition of
  the bound and covers the wait for the slot: one render at a time per process, and a render
  queued behind another fails within the same 15 s. `POST /quotes/:id/email` therefore takes at
  most 15 s + `mailer.DefaultSendTimeout` (30 s) = 45 s and `GET /quotes/:id/pdf` at most 15 s.
  The SPA passes `QUOTE_PDF_REQUEST_TIMEOUT_MS` = 55 000 to `emailQuote` and `downloadQuotePdf`
  (the default `API_TIMEOUT` of 30 s would abort a call the server still completes, and the user
  would send the quote twice); a reverse proxy needs a read timeout of 60 s or more.
  `TestQuotePDFTimeoutBudget` and `quotes.test.ts` pin the numbers; change them together.
- **UI.** `GET /configurations/ui` appends the synthetic read-only `quotes.pdf_available`
  (`ConfigurationHandler.WithQuotePDFAvailable`); `isQuotePdfAvailable` in
  `configuration-context.ts` reads it and QuoteDetail shows **Download PDF** only when it is
  `true`. `QuoteDocument.tsx` is the document shared by `QuotePrint` (toolbar, inside the shell)
  and `QuoteRender` (public route `/quote-render`, no toolbar, no shell). `downloadQuotePdf`
  asks for a blob; an error body is then a Blob as well, which the client's envelope unwrapping
  does not see, so the function reads it and rethrows the error with the flat `{code, message}`
  body every other call has.

Roots: `TestQuotePDF{Off,Render,TimeoutBudget}`, `TestQuoteRenderCredential`
(`internal/service/quote_pdf_test.go`); `TestChromeQuotePDFRenderer{MissingBinary,Timeout,Environment,LaunchArguments,Pipe,RealBinary}`
(`quote_pdf_chrome_test.go`; `LaunchArguments` pins the keychain switches, `--headless`,
`--remote-debugging-pipe`, the absence of `--no-sandbox`, of a debugging port switch and of the
page URL in every argument, and that the URL arrives whole in `Page.navigate`; `Pipe` pins a
PDF answer over 1 MiB, a browser that exits or answers with an error, an answer that is not a
PDF, and that no render leaves a file descriptor open; `RealBinary` drives the binary named by
`QUOTE_PDF_CHROME_PATH`, holds the render open with a pending request of the page and reads the
host's `ps` output meanwhile, and skips without the variable; none of the six is in a no-skip
gate: CI has no browser. The stand-in browser of these roots is the test binary itself, run as
`TestHelperQuotePDFBrowser`, which returns at once in an ordinary run);
`TestQuoteEmailPDFAttachment` (`internal/service/quote_email_test.go`, in `MAILER_CALLER_ROOTS`);
`TestQuotePDFHandler`, `TestQuotePublicRender`, `TestUIConfigurationsQuotePDFAvailable`
(`internal/handler`); `TestSMTPAttachment`, `TestLogMailerAttachment` (`internal/mailer`);
`TestDockerfileShipsHeadlessShell`, `TestLoad_QuotePDFChromePath` (`internal/config`).

*The real chain is checked by hand, and belongs to the release checks.* The e2e backend runs
with `QUOTE_PDF_CHROME_PATH` pinned empty (`scripts/e2e/run.sh`), and the tests above use fake
renderers, so nothing automatic notices a change to `QuoteDocument`, the render route or the
browser flags that makes the PDF show the loading state or an empty page: "the credential was
redeemed" proves the page's request arrived, not that the answer was painted.
`scripts/e2e/quote-pdf-smoke.sh` checks exactly that chain (the browser, the SPA's
`/quote-render`, `POST /quotes/public/render`, the printed file) against a backend on
`localhost`. It logs in, creates a customer, a product and a quote with one taxed line
`PDF smoke line <hex>` (2 x 123.45 at 19 percent), downloads `GET /quotes/<id>/pdf`, fails
unless the answer is 200 `application/pdf` starting with `%PDF`, writes the file and prints
four lines, `number=`, `line=`, `total=` (the net total, 246.90) and `gross_total=` (293.81),
the totals taken from the API's answer and required to differ (with `SMOKE_EMAIL=1` it also
sends and emails the quote and prints `emailed=yes`). It starts nothing, prints no token and no
password and refuses a `BASE_URL` whose host is not `localhost` or `127.0.0.1` (it creates
data).

`scripts/e2e/quote-pdf-chain.sh` is the one command around it: it starts what the chain needs,
runs the smoke script, reads the text of the PDF (`pdftotext` and `pdffonts` from this machine,
otherwise from an `alpine:3.20` container; without either it fails) and fails unless the text
holds the number, the line, the net total and the gross total. On exit it stops everything it
started and checks that nothing is left. Run both modes before a release, and `image` after
every change of `HEADLESS_SHELL_VERSION` and its two checksums,
`HEADLESS_SHELL_SHA256_AMD64` and `HEADLESS_SHELL_SHA256_ARM64`:

```bash
# a full Chrome or Chromium (or a headless shell) on this machine, backend and web app built
# into a temporary directory, SQLite, free ports
QUOTE_PDF_CHROME_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  bash scripts/e2e/quote-pdf-chain.sh local

# the image built from this checkout with its own default browser (chrome-headless-shell)
bash scripts/e2e/quote-pdf-chain.sh image
```

`local` needs `QUOTE_PDF_CHROME_PATH` from the caller and `gocrm-ui/node_modules`; it builds the
web app with an absolute `VITE_API_BASE_URL` into its own directory (`gocrm-ui/dist` is not
touched) and serves it with `vite preview`. `image` needs Docker with Compose v2. It writes a
complete compose file of its own into the temporary directory (not an override of the
repository's files): the backend with a read-only root file system, `/tmp` on tmpfs, SQLite on
a volume and `APP_BASE_URL: http://ui`, the UI on a free local port. The file never sets
`QUOTE_PDF_CHROME_PATH`; the script fails unless the container's value equals the image's
configured default. It then waits for the backend's health check, prints the browser's version
and the image size, renders ten quotes, checks the text and at least one embedded font, polls
`docker compose top` for up to 10 seconds until no browser process and no zombie is left, and
requires the backend to stop in under 10 seconds with an exit code other than 137. Admin
password and JWT secret are generated per run and never printed. Neither script is referenced
by a workflow, a Makefile target or a gate.

Negative control, by hand: with a backend from the recipe of `local` running and the web app
stopped, the smoke script must exit non-zero with 503 `PDF_RENDER_FAILED`.

**Quote response link: a single-use token claimed with the transition (#382)** — a quote service
built with `NewQuoteServiceWithResponseLinks(..., tokenRepo, appBaseURL, tokenSecret)` (what
`cmd/main.go` wires, with `APP_BASE_URL` and `API_KEY_SECRET`, else `JWT_SECRET`) appends
`<APP_BASE_URL>/quote-response?token=<token>` to the email of a *sent* quote. `NewQuoteService`
keeps its signature for callers that send no link: its emails carry none and both public methods
answer `apperrors.ErrQuoteResponseRejected` (the `NewAuthService` / `NewAuthServiceWithSessions`
precedent). The token is 256 random bits (`generateOpaqueToken`); only `hashOpaqueToken` of it is
stored, in `quote_response_tokens`, and neither the token nor the link reaches a log field or an
error text.

- **Mint.** After the recipient checks and before `Mailer.Send`, `mintResponseLink` runs one short
  transaction: `GetByIDForUpdate(quote)`, the re-check that it is still sent (`ErrQuoteNotSent`
  otherwise), a plain read of the quote's customer through the transaction-bound customer
  repository as the first consistent read after the lock (`ErrQuoteNoRecipient` for a quote
  without a customer or whose customer was erased, for example during the PDF render), the re-read
  of the latest history id against the marker read at the start (`ErrQuoteChangedDuringSend` when
  it moved: a revision and re-send in between would mail the old rendering with a live link to the
  new quote), `Create`. The refusals are 409 and send nothing. The customer read takes no lock: the
  erasure locks `customers` before `quotes`. No transaction is held during the send.
- **Post-send invalidation.** Only after the mailer reports success, and before `StampEmailed`,
  `invalidateEarlierResponseLinks` runs a second short transaction: `GetByIDForUpdate(quote)` (a
  quote deleted meanwhile ends the step) and `InvalidateOlderForQuote(quote, new token id)`, which
  stamps the quote's unused rows with a lower id, so of two overlapping emails the one that
  finishes last never kills the newer link. A failed send stamps nothing: the link of the previous
  successful email stays usable, and the undelivered row stays stored until the next successful
  send stamps it. A failure of this step is one warn line with the ids ("Quote email: earlier
  response links not invalidated"); the email still succeeds and the earlier links survive until
  the next successful send.
- **Expiry.** `expires_at` is the start of the day after `valid_until` in `time.Local`, stored in
  UTC (`quoteResponseExpiry`), the same day `dateBefore` allows a staff acceptance; NULL without a
  `valid_until`. A sent quote whose `valid_until` has passed is still emailed, without a link and
  without a row; an accepted quote is emailed as before.
- **Read.** `GetPublicResponse(token)` hashes, calls `FindUsableByHash` and loads the quote. It
  never writes, so mail scanners and previews do not spend the link. The read's predicates are the
  claim's plus a live customer of the quote (a nested `EXISTS` on `customers`), so a link of an
  erased customer is refused even when the erasure did not stamp its row.
- **Claim.** `RespondPublic(token, decision)` reads the token by hash, then in one transaction, in
  the order `quotes → quote_response_tokens`: `GetByIDForUpdate(quote)`, a plain read of the
  quote's customer as live (the first consistent read after the lock; a quote without a customer
  or with an erased one is the generic rejection, which catches an erasure that commits between
  the token read and the transaction), `Claim` (one conditional
  `UPDATE` setting `used_at`, `decision` and `updated_at` `WHERE id = ? AND used_at IS NULL AND
  (expires_at IS NULL OR expires_at > ?) AND EXISTS (the quote is live and sent)`; exactly one row,
  else `gorm.ErrRecordNotFound`; it reads no `customers` row, because a subquery of an `UPDATE`
  takes shared locks on MySQL and MariaDB and would wait on an erasure under the quote lock), `ClaimStatus(sent → accepted | rejected)` with `closed_at`, and
  the history row with `changed_by_id` NULL and `source` `client` (#400; staff transitions write
  the acting user and `source` `staff`, and `GET /quotes/{id}/history` omits `changed_by` on a
  client row). A failure after the claim rolls it back
  and the link stays usable. The deal repository is not touched. This is the single-use token
  shape of the form confirmation and the password reset; do not pre-check then update.
- **Invalidation.** `ChangeStatus` stamps the quote's unused rows in its own transaction whenever
  it leaves `sent` (`InvalidateForQuote`, `decision` stays NULL), so a link stays dead after
  sent → draft → sent until the next email mints a new one. `InvalidateForQuote` reads the ids
  with a plain `SELECT` and stamps them by primary key (no statement when there is none): an
  `UPDATE` filtered on `quote_id` gap-locks the index on MySQL and MariaDB and deadlocked the
  first emails of two different quotes. Call it only after the locking read of the quote.
  The erasure of the quote's customer (#395) stamps the same way, with the same statements
  (`invalidateQuoteResponseTokens`), after its own update of the quote row: a link mailed before
  the erasure gets the generic rejection and the quote stays `sent`. See "Response links" under
  the erasure in [DEV_ERASURE.md](DEV_ERASURE.md#deleting-personal-data) for which quotes and for a token minted while the erasure runs.
- **HTTP.** `SetupQuotePublicRoutes` mounts `GET` and `POST /quotes/public/response` on the public
  group with one shared `RateLimitStrict()` instance and no Auth. The `GET` reads the token from
  the `X-Quote-Token` request header only (a token in the query string is not read; the strict
  CORS policy allows the header); the `POST` reads it from the body. A group-level handler sets
  `Cache-Control: no-store` before the rate limiters, so every answer of the three
  `/quotes/public` routes carries it, 429 included; `GET /quotes/:id/pdf` sets it as its first
  statement. The body is validated before any
  token lookup (400 `VALIDATION_ERROR`); every `ErrQuoteResponseRejected` is 400 `BAD_REQUEST`
  "Invalid or expired quote link"; the response is `models.QuotePublicView` (number, status,
  currency, `valid_until`, lines, five totals; no id, title, notes, owner, customer, deal or
  product reference).
- **Page (#383).** The link opens the SPA route `/quote-response`, declared at the top level of
  `gocrm-ui/src/routes/index.tsx`, outside `ProtectedRoute` and `MainLayout`
  (`gocrm-ui/src/pages/quotes/QuoteResponse.tsx`). It issues one `GET` per mount (no retry, no
  refetch: the strict bucket is shared with the `POST`), asks for a confirmation before the one
  `POST`, and treats a 400 on the `GET`, or a 400 `BAD_REQUEST` on the `POST`, as a dead link.
  It takes the token from the link once (a `useState` initialiser, so StrictMode's development
  remount keeps it), then removes it from the address bar with a replace navigation
  (`setSearchParams({}, { replace: true })`, in an effect), sends it in the `X-Quote-Token`
  header of the `GET` and in the body of the `POST`, and never renders, logs or stores it in web
  storage. Accepted consequence: reloading the page after the token is gone shows the
  invalid-link message, and the client opens the mailed link again. The mailed link itself stays
  `APP_BASE_URL/quote-response?token=...`.
- **Public client.** The page's endpoint module (`gocrm-ui/src/api/endpoints/quoteResponse.ts`)
  calls through `publicApi`, which is `apiClient.getPublicClient()`: a second axios instance
  with the base URL, timeout, transport and envelope unwrapping of `api` and no request
  interceptor. It never attaches the stored session's `Authorization` header and a 401 is neither
  refreshed nor replayed. Use it for any endpoint that is answered without a session and must
  not carry one; touch it only inside function bodies, as many tests mock `../client` with a
  factory that defines `api` alone.
- **Browser-test link.** The mail body is the only place the plain token exists, and the
  log-only mailer never writes it. `go run ./scripts/e2e/quote-response-link -quote-id N` loads
  the configuration like the server, wires the quote service as `cmd/main.go` does with a mailer
  that keeps the message in memory, calls `Email(N)` and prints the link line, and nothing else,
  on stdout (the loggers are pointed at stderr). It exits non-zero without output when the quote
  is not emailed with a link (a draft, an accepted quote, a sent quote past `valid_until`), when
  `E2E_UI_PORT` is not set (only `scripts/e2e/run.sh` exports it) and, before any connection,
  when `DB_DRIVER` is mysql and `DB_NAME` does not end in `_e2e`. Like a staff email it
  invalidates the previous link of the quote and stamps `last_emailed_at`, which is why it must
  never run against a real database. It is not part of the server binary and adds no route.

Roots: `TestQuoteEmailResponseLink` (`internal/service/quote_email_test.go`);
`TestQuoteResponse{View,Decision,RollbackKeepsLink,DeadLinks}`
(`internal/service/quote_response_test.go`); `TestQuoteResponseConcurrent{SQLite,MySQL}`
(`internal/service/quote_response_regression_test.go`; the MySQL root connects with `loc=Local`
like the application, skips without `TEST_MYSQL_DSN` and is not part of a CI gate, like the other
quote concurrency roots); `TestQuoteResponseTokenRepository` (`internal/repository`);
`TestQuotePublic{View,Respond}`, `TestQuotePublicRoutesWinOverTheIDParameter` (`internal/handler`);
`TestQuoteResponseTokensMigrationFiles` and `TestQuoteResponseTokensMigration` (`cmd/migrate`).
The history source (#400): `TestQuoteHistoryClientAnswer` (`internal/handler`, real routes and
service), `TestQuoteHistorySourceMigrationFiles` (the newest-migration assertion moved to
`TestUserIdentitiesMigrationFiles` with #435) and
`TestQuoteHistorySourceMigration` (`cmd/migrate`, migration `20261122120000_quote_history_source`),
`TestQuoteHistorySourceSQLiteUpgrade` (`internal/models`). The erasure of the customer (#395):
`TestQuoteResponseAfterCustomerErasure{SQLite,MySQL}`
(`internal/service/quote_response_erasure_test.go`) and
`TestErasureInvalidatesQuoteResponseTokens{SQLite,MySQL}`,
`TestErasureQuoteTokenWritesLeaveGapsFreeMySQL` (`internal/repository/erasure_quote_tokens_test.go`),
both files in the transaction gate of `scripts/ci/check-go.py`. Page and client: `QuoteResponse.test.tsx`,
`client.public.test.ts`, `quoteResponse.test.ts`, the "quote response route" block of
`routes/index.test.tsx`, `gocrm-ui/e2e/tests/quote-response.spec.ts`, and
`TestQuoteResponseLink{PrintsUsableLink,RefusesDraftQuote,RefusesNonE2EDatabase}`
(`scripts/e2e/quote-response-link`).

## Mail transport

**The SMTP transport refuses CR/LF in headers** — `SMTPMailer.Send` and `SendPasswordReset` return
an error matching `mailer.ErrInvalidHeader` (through `errors.Is`) for a recipient or subject that
contains `\r` or `\n`, before any relay is dialled: either character would end the header line and
let the remainder inject further headers (an extra recipient, a forged subject). The check is
repeated inside the private `send`, so no future caller can bypass it. The generic `Send` failure
log carries only the transport error passed through `utils.LogError` — neither the subject nor
the recipient: an SMTP reply keeps its code and loses its text, because a relay that rejects a
recipient normally echoes the address in that text (`550 5.1.1 <who@example.com>: Recipient address
rejected`). The raw error is still returned for `errors.As`. The password-reset failure line logs
the same sanitised error and the redacted link, without the recipient; the development
`LogMailer` still records the recipient and the subject as before. Roots:
`TestSMTPMailerRejectsHeaderInjection` (`internal/mailer/header_injection_test.go`) and
`TestSMTPMailerSendLogOmitsTheRelayReply` (`internal/mailer/smtp_relay_reply_test.go`), whose stub
relay answers `RCPT TO` with a 550 that quotes the address.

**SMTP delivery is one bounded exchange (#113)** — `SMTPMailer` does not use `smtp.SendMail`,
which has no timeout. It dials within `mailer.DefaultDialTimeout` (10 s), sets one deadline of
`mailer.DefaultSendTimeout` (30 s, counted from the start of the dial) on the connection for the
whole conversation — greeting, EHLO, STARTTLS, AUTH, MAIL/RCPT/DATA, QUIT — and closes the raw
TCP connection on every path; a watchdog (`context.AfterFunc`) closes it at the deadline at the
latest, so cleanup such as the TLS close_notify (which sets its own five-second write deadline)
cannot extend the bound. A failure caused by the deadline matches `os.ErrDeadlineExceeded`.
The timeouts are package defaults, not configuration. **STARTTLS is opportunistic**: it is used
whenever the relay advertises it, verifying the certificate against `SMTP_HOST`, and a failed
handshake aborts the delivery, but with no `SMTP_USER` a relay that does not offer STARTTLS —
or an attacker on the path who strips the offer — receives the message unencrypted. There is no
require-TLS setting (see the roadmap); implicit TLS (port 465) is not supported. With
`SMTP_USER` set the relay must offer AUTH, and net/smtp's `PlainAuth` refuses to send the
credentials over an unencrypted connection unless the host is `localhost`/`127.0.0.1`/`::1`. A
failed QUIT after the relay accepted DATA is not a failure. The recipient and `SMTP_FROM` are
parsed with `net/mail.ParseAddress` — exactly one address, display name allowed — and the
addr-spec is then serialised for the envelope (`envelopeAddress`): a dot-atom local part as is,
any other local part as an RFC 5321 quoted string with `"` and `\` escaped
(`RCPT TO:<"a b"@example.com>`), because `net/mail` returns quoted local parts unquoted and
`"x> NOTIFY=NEVER"@example.com` would otherwise end `RCPT TO:<…>` early and inject ESMTP
parameters. Control characters, `<` and `>` anywhere in the address — refused even inside
quotes, where RFC 5321 would allow the brackets, because they are what lets text escape the
command's angle brackets — and domain literals are `mailer.ErrInvalidAddress`, without echoing
the value. Non-ASCII addresses (RFC 6531) are delivered only when the relay advertises SMTPUTF8
after EHLO (net/smtp's `Client.Mail` then adds the `SMTPUTF8` parameter itself) and are refused
with `ErrInvalidAddress` before `MAIL FROM` otherwise. A non-ASCII local part must be printable
letters, digits and marks; a domain label may hold ASCII letters, digits and `-` and any
non-ASCII character except controls (format controls such as bidi overrides included, ZWNJ/ZWJ
excepted) and whitespace, so every label the CRM's validator accepts — `info@col·legi.cat`
(U+00B7, RFC 5892) — goes out as `RCPT TO:<info@col·legi.cat>` with no IDNA conversion (#180;
root `TestSMTPInternationalisedDomains`, `internal/mailer/smtp_idn_test.go`). The headers carry
`mail.Address.String()`; CR/LF in the sender is `ErrInvalidHeader` like the recipient and
subject. A non-ASCII subject is Q-encoded into encoded words that never split a character and
folded so that every line holding encoded words, `Subject: ` included, is at most 76 characters
(RFC 2047 section 2). Only I/O failures caused by the deadline (a timeout, or the watchdog's
close) are reported as `os.ErrDeadlineExceeded`; a relay reply such as a 535 that decided the
outcome is returned as it is even when net/smtp's abort and QUIT afterwards stall. The body is sent as
`Content-Transfer-Encoding: quoted-printable` (7-bit on the wire, long lines soft-wrapped) after
its line endings (CRLF, bare LF, bare CR) are normalised to CRLF; dot-stuffing is left to the SMTP
data writer. The `Send` failure log carries neither the recipient nor the subject (automation
subjects are rendered from `{{lead.email}}` and similar placeholders), only the error through
`utils.LogError`, which also reduces a malformed relay reply (`textproto.ProtocolError`, which
carries the whole line) to `smtp_protocol_error`. Roots, against in-process relays on `127.0.0.1` with injected
sub-second timeouts (`internal/mailer/smtp_transport_test.go`): `TestSMTPDeadlines`,
`TestSMTPHeaders`, `TestSMTPTLSAuth`, `TestSMTPFailureRedaction`, and
`TestSMTPInternationalisedDomains` (`internal/mailer/smtp_idn_test.go`).

## Ordered and paginated reads

**Unpaginated reads must not mix a non-id `ORDER BY` with `FindInBatches`.** GORM's
`FindInBatches` continues each batch with `id > last id of the previous batch`, a cursor that is
only valid when the rows are ordered by id; under any other ordering it skips and repeats rows
across batches. The customer CSV export did exactly that and lost rows past the first 500 (#105).
`CustomerRepository.ListAllForExport` now reads every matching row with one ordered statement whose
order is total: the requested column, then `id`, both in the requested direction, or `id` ascending
without a sort column. That makes duplicate values, NULLs in the legacy nullable `company` column
and MySQL collation ties deterministic, makes `desc` the exact reverse of `asc`, and makes the read
one consistent snapshot on both engines. The result is still fetched in full before the handler
writes a byte. Regression roots: `TestCustomerExportTraversal{SQLite,MySQL}`
(`internal/repository/customer_export_traversal_test.go`) and `TestCustomerExportCSV{SQLite,MySQL}`
(`tests/customer_export_csv_test.go`); the MySQL roots create a disposable schema on
`TEST_MYSQL_DSN`, skip without it and fail when it is unreachable. If a whole-book read ever needs
to be chunked again, continue on a composite `(sort column, id)` keyset that handles NULL
explicitly, never on the primary key alone.
`LeadRepository.ListAllForExport` (`GET /leads/export`, #234) follows the same rule: one statement
over the live leads matching a `LeadExportFilter` (owner, search, exact classification, each
applied only when given), ordered by the validated column and then `leads.id` in the same
direction, or by `id` ascending; converted leads are included and erased ones are not.
`LeadService.ExportForActor` applies the list's scope: an admin gets the search with the sort,
else the classification in id order, else the sort, else id order; a sales user its own leads in
id order with every parameter ignored; any other role `apperrors.ErrForbidden` without a read.
Regression roots: `TestLeadExportTraversal{SQLite,MySQL}`
(`internal/repository/lead_export_traversal_test.go`) and `TestLeadExportCSV{SQLite,MySQL}` and
`TestLeadExportRoles{SQLite,MySQL}` (`tests/lead_export_csv_test.go`), under the same MySQL rules.

**Paginated reads walk a total order (#110).** OFFSET/LIMIT pages are separate statements, so
an `ORDER BY` that leaves rows tied lets the engine return the tied rows in a different order on
each page; MySQL's filesort with a LIMIT does exactly that, and consecutive pages repeated some
rows and never showed others. Every list and search over leads, customers, tasks (including the
label join), tickets, users and companies, and the company customer/lead sub-lists, therefore
ends with the table's id: `orderPage` (`internal/repository/page_order.go`) validates the column
through `utils.ValidateSort`, orders by it and then by id in the same direction, or by id
ascending when no sort is requested; the company list keeps its newest-first default with id
descending after `created_at`. Columns are emitted through `clause.OrderByColumn` qualified with
the table name, so a joined query cannot resolve them against the other table. New paginated
reads should use the same helpers. Regression roots: `TestPaginationDeterminism{SQLite,MySQL}`
(`internal/repository/pagination_determinism_test.go`), where every row shares `created_at` and
every sort column is tied, both directions, with filters, checking every page against the
expected sequence and the count. The Database migrations workflow runs both roots, the MySQL one
on MySQL 8 and MariaDB 10.11, and its checker rejects a skip.

## Tasks and tickets

**A task's deal link (#240).** `tasks.deal_id` is additive to the lead-or-customer rule and follows
`service.CanViewDeal` (`internal/service/deal_visibility.go`), decided by the caller's `deals.view`
scope: `all` any deal, `own` the caller's deals, not held none (on the system matrix that is admin,
sales and every other role). `checkDealLink` is the write side: a missing and a hidden deal both give
`ErrDealNotFound` with one message, and only a not-found lookup is mapped, so an outage answers 500.
`TaskService.UpdateWithLabels` takes `dealIDSent`: absent keeps the stored link unchecked, a sent id
is checked even when it equals the stored one, and 0 clears the link when the actor may see the stored
deal; a clear of a hidden link returns no error and keeps it. The create and update paths detach
`task.Deal` before every repository write, because GORM writes a preloaded belongs-to id back over
the foreign key, and re-attach it for the response. Every handler answering with a task goes through
`redactTaskLinks`, and `Task.MarshalJSON` fills the `deal` summary (id, title, stage) from the
preloaded deal only. Regression roots: `TestTaskDealLinkWriteRules`, `TestTaskDealLinkClear`,
`TestCanViewDeal`, `TestRedactTaskDeal`, `TestTaskDealLogsOmitDealTitle`,
`TestAutomationDealTaskLink{SQLite,MySQL}`, `TestAutomationDryRunReportsDealLink`
(`internal/service`); `TestTaskDealLinkRepository{SQLite,MySQL}`, `TestTaskDealLinkSurvivesErasure`
(`internal/repository`); `TestTaskDealReadRoutes{SQLite,MySQL}`, `TestDealTasksEndpoint{SQLite,MySQL}`,
`TestTaskDealLinkIntegration{SQLite,MySQL}` (`internal/integration`); `TestTaskDealHandlerErrorMapping`;
`TestTaskJSONDealSummary`, `TestTaskDealLinkSQLiteUpgradeFromV120` (`internal/models`);
`TestTaskDealLinkMigration`, `TestTaskDealLinkMigrationFiles` (`cmd/migrate`).

**The ticket update transaction (#242, #408).** `ticketService.Update` takes the transactional
path `updateWithEvents` (locking read, checks against the locked row, save, events) when automation
events **or** webhooks are wired; with webhooks only it records no automation event
(`automationTx` returns nil) and enqueues `ticket.updated` when a stored value changed. With neither
it stays the plain read and write.

**The ticket assignee (#269).** `ticketService.Update` detaches the preloaded `AssignedTo` before
`ticketRepo.Update` whenever its id differs from `AssignedToID`, because `Save` writes a preloaded
belongs-to id back over the foreign key. It compares the attached user with the foreign key, not
with the validated change alone, since the handler's read and the service's read are separate.
After the write it attaches the user the foreign key names, for the response: the validated
assignee when it looked one up, otherwise the user of its own read, and none for a nil key. The
repository keeps `Save`, so a stale `PUT` stays last-write-wins. With automation events wired
(`WithTicketAutomationEvents`, #242, as `cmd/main.go` wires it) the update is one transaction that
locks the ticket row first and runs these checks against the locked row (see *Automation
runtime*); without the option there is no transaction, as before. Ticket assignees have no
is-active check; an existing user with the support or admin role qualifies.
Regression roots: `TestTicketServiceUpdateDetachesStaleAssignee` (`internal/service`);
`TestTicketReassignmentPersists{SQLite,MySQL}`, `TestTicketReassignmentResponse{SQLite,MySQL}`,
`TestTicketReassignmentBySupportAssignee{SQLite,MySQL}`, `TestTicketUpdateKeepsAssignee{SQLite,MySQL}`
and `TestTicketReassignmentRefusedWritesNothing{SQLite,MySQL}` (`internal/integration`). CI limit:
the MySQL roots skip in `make verify-backend`, which has no DSN, and `scripts/ci/check-go.py` does
not discover `internal/integration`, so only the runner's acceptance checks and the release
workflow run them.

**Ticket SLA fields (#278).** `tickets.due_at` and `tickets.first_responded_at` are nullable
timestamps; `overdue` is computed by `Ticket.IsOverdue(now)` and never stored (true
when `due_at` is strictly before now in UTC and the status is `open` or `in_progress`).
`Ticket.MarshalJSON` writes both timestamps in UTC with millisecond precision, or `null`, and
`overdue` from the wall clock, on every route that returns a ticket. The handler decodes `due_at`
(a `json.RawMessage`) before calling the service: absent keeps it on `PUT /tickets/{id}`, `null`
clears it, an RFC 3339 string with any offset is stored in UTC truncated to milliseconds, and any
other value is 400 `INVALID_DUE_AT` (`error.details.field` `due_at`) with nothing read or written.
Only `TicketRepository.CreateCommentWithFirstResponse` writes `first_responded_at`: it inserts the
comment and runs `UPDATE tickets SET first_responded_at = <created_at> WHERE id = ? AND
first_responded_at IS NULL AND deleted_at IS NULL` in one transaction (a savepoint through
`WithTx`). `ticketService.CreateComment` calls it for a non-internal comment whose author, as the
service reads it, is admin or support, and the plain `CreateComment` otherwise. `Create` and
`Update` omit the column, so a body or a ticket read before the stamp can neither set nor clear
it, and `Update` refills the struct with the stored value. MySQL and MariaDB get the columns from
`20261108120000_add_ticket_sla_fields` (`datetime(3) NULL`, each only when missing, no backfill);
SQLite from `MigrateDatabase`. Regression roots: `TestTicketOverdue`, `TestTicketJSONSLAFields`,
`TestTicketSLAFieldsSQLiteUpgrade` (`internal/models`); `TestTicketDueAtHandlerParsing`,
`TestTicketResponseShapeUnchanged` (`internal/handler`); `TestTicketCreateCommentFirstResponse`
(`internal/service`); `TestTicketFirstResponseRepository{SQLite,MySQL}` (`internal/repository`);
`TestTicketDueAtLifecycleSQLite`, `TestTicketDueAtInvalidWritesNothingSQLite`,
`TestTicketFirstResponseSQLite`, `TestTicketSLAFieldsOnEveryReadPathSQLite`
(`internal/integration`); `TestTicketSLAFieldsMigration`, `TestTicketSLAFieldsMigrationFiles`
(`cmd/migrate`). The MySQL roots skip without `TEST_MYSQL_DSN`; the Database migrations workflow
runs the migration roots.

## List filters

**Ticket list filters (#218).** `GET /tickets` takes optional `status` and `priority` query
parameters, each one exact, case-sensitive value of `models.TicketStatus` or
`models.TicketPriority`. The handler validates them with `IsValid` before any query, after the
customer 403: MySQL's default collation is case-insensitive and would match `Open` to `open`. An
unknown value is 400 `VALIDATION_ERROR` (`utils.RespondValidationError`, `error.details` keyed by
the parameter name, the value not echoed) and no service is called. `c.Query` takes the first
value of a repeated parameter, and an empty value counts as absent. Only when a filter is present
does the handler call `TicketService.ListFiltered(filter, offset, limit, sortBy, sortOrder)` with
a `repository.TicketListFilter{AssigneeID, Search, Status, Priority}`, forcing `AssigneeID` to the
caller's own `user_id` for support; without one the existing `List`, `ListSorted`, `Search` and
`ListAssigned` branches run with unchanged arguments. `TicketRepository.ListByFilter` (preloads,
`orderPage`, OFFSET/LIMIT) and `CountByFilter` share one predicate builder,
`whereTicketListFilter`, which ANDs the optional assignee (a non-nil 0 matches no ticket, as in
`TicketAssigneeScope`), status and priority with the parenthesised search OR, so the rows and the
total come from the same filter before offset and limit. The service logs ids, offsets, counts
and presence flags only, never the search term or the raw sort. No schema change or index.
Regression roots: `TestTicketEnumIsValid` (`internal/models`); `TestTicketListFilterHandler`
(`internal/handler`); `TestTicketServiceListFiltered` (`internal/service`);
`TestTicketListFilterRepository{SQLite,MySQL}` (`internal/repository`);
`TestTicketListFilters{SQLite,MySQL}` (`internal/integration`). The MySQL roots use a disposable
`TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable.

**Lead list status filter (#426).** `GET /leads` takes an optional `status` query parameter, one
exact, case-sensitive value of `models.LeadStatus` (`new`, `contacted`, `qualified`, `unqualified`,
`converted`). The `external_id` branch still runs first and overrides it (an invalid status next to
a non-empty `external_id` is not validated). The handler then reads `status` with `c.Query` (first
value of a repeated parameter; empty counts as absent) and validates it with `LeadStatus.IsValid`
before any service call: an unknown value is 400 `VALIDATION_ERROR` with `error.details.status`, the
value not echoed or logged. `IsValid` is the one list: bulk status's `isValidLeadStatus` delegates
to it. Since #618 every request past `external_id` takes ONE path,
`LeadService.ListFiltered(repository.LeadListFilter{OwnerID, Search, Classification, Status},
offset, limit, sortBy, sortOrder)`, and the parameters a caller may use are always ANDed: a caller
without `leads.view` at scope all (sales) gets `OwnerID` forced to its own `user_id`, the status
when given and an empty sort (its search, classification and sort stay ignored); an admin gets
search, classification, status and the sort together. The owner is preloaded on every path.
`LeadRepository.ListByFilter` (`orderPage`, then OFFSET/LIMIT; id ascending without a sort) and
`CountByFilter` share one predicate builder, `whereLeadListFilter`, which ANDs `leads.owner_id`,
`leads.classification` and `leads.status` with the parenthesised search OR over the six searched
columns, so the rows and the total come from the same filter before offset and limit; erased
(soft-deleted) leads stay excluded. The search is a literal substring: `containsPattern`
(`internal/repository/like_pattern.go`) escapes `!`, `%` and `_` with `!`, and every LIKE is
`col LIKE ? ESCAPE '!'` (built by `likeAnyClause`), so `%`, `_`, `!` and a backslash match only
themselves on SQLite, MySQL and MariaDB. `!` rather than a backslash because `'\\'` is one
character on MySQL but two on SQLite, and MySQL treats a backslash as its default LIKE escape.
`LeadRepository.ListAllForExport` builds its WHERE from the same `whereLeadListFilter`, so
`GET /leads/export` matches exactly the rows the admin list would (search, classification and
the sort combined; sales its own leads only). The service logs offset, limit, the owner id and
the presence flags `status_filter`, `search_filter` and `classification_filter` only, never the
search term or the raw sort. `GET /leads/export` keeps ignoring `status`. No schema change.
Regression roots: `TestLeadStatusIsValid` (`internal/models`); `TestLeadListStatusFilterHandler`
(`internal/handler`); `TestLeadServiceListFiltered` (`internal/service`);
`TestLeadListFilterRepository{SQLite,MySQL}` (`internal/repository`). The MySQL root uses a
disposable `TEST_MYSQL_DSN` schema, skips without it and fails when it is unreachable.

**Task list filters (#427).** `GET /tasks` takes optional `status` and `priority` query
parameters, each one exact, case-sensitive value of `models.TaskStatus` (`pending`, `in_progress`,
`completed`, `cancelled`) or `models.TaskPriority` (`low`, `medium`, `high`; `urgent` is a ticket
priority only). The handler reads them with `c.Query` (first value of a repeated parameter; empty
counts as absent, which is what the list page sends for "All") and validates them with
`TaskStatus.IsValid` / `TaskPriority.IsValid` before any service call: an unknown value is 400
`VALIDATION_ERROR` with `error.details` keyed by the offending parameter name(s), the value not
echoed and the warning log carrying only the count. Since #618 every request takes ONE path,
`TaskService.ListFiltered(repository.TaskListFilter{AssigneeID, LabelID, Search, Status,
Priority}, offset, limit, sortBy, sortOrder)`, and the parameters a caller may use are always
ANDed: a caller holding `tasks.view` at scope all (admin) gets `AssigneeID` nil and `label_id`,
`search`, the filters and the normalised sort combined; any other caller gets `AssigneeID` forced
to its own `user_id`, no search, and the sort only together with `label_id` (id ascending
otherwise). `GET /tasks/my` stays on `TaskService.GetByAssignee`, which reads through the same
`ListByFilter`/`CountByFilter` with `AssigneeID` and the preloads `Labels`, `Lead` and `Deal` only
(its `assigned_to` stays zero-valued). `TaskRepository.ListByFilter` (the `taskListPreloads` on
`GET /tasks`, so `assigned_to` is filled on every path; `orderPage`, then OFFSET/LIMIT) and
`CountByFilter` share one predicate builder, `whereTaskListFilter`, which joins `task_labels` only
for a label filter and ANDs `tasks.assigned_to_id`, `tasks.status` and `tasks.priority` with the
parenthesised `(tasks.title LIKE ? ESCAPE '!' OR tasks.description LIKE ? ESCAPE '!')`, every
column qualified and every value bound; the search term is escaped by `containsPattern` as for
leads, so it matches literally. Soft-deleted tasks stay excluded. The service logs offset, limit, the assignee and label ids, the
total and the presence flags `status_filter`, `priority_filter` and `search_filter` only. No
schema change. Regression roots: `TestTaskEnumIsValid` (`internal/models`);
`TestTaskListFilterHandler` (`internal/handler`); `TestTaskServiceListFiltered`
(`internal/service`); `TestTaskListFilterRepository{SQLite,MySQL}` (`internal/repository`). The
MySQL root uses a disposable `TEST_MYSQL_DSN` schema, skips without it and fails when it is
unreachable.

**User list role filter (#428).** `GET /users` takes an optional `role` query parameter, one exact,
case-sensitive value of `models.UserRole` (`admin`, `sales`, `support`, `customer`, `custom`). The
handler reads it with `c.Query` (first value of a repeated parameter; empty counts as absent, a
blank is not trimmed) and validates it with `UserRole.IsValid` before pagination or any service
call: an unknown value is 400 `VALIDATION_ERROR` with `error.details.role`, the value not echoed.
With a role the handler calls `UserService.ListFiltered(repository.UserListFilter{Search, Role},
offset, limit, sortBy, sortOrder)`; without one the existing `Search`, `ListSorted` and `List`
branches run unchanged. `UserRepository.ListByFilter` (`orderPage`, then OFFSET/LIMIT; id
ascending without a sort) and `CountByFilter` share one predicate builder,
`whereUserListFilter`, which ANDs `users.role = ?` with the parenthesised search OR, so the rows
and the total come from the same filter before offset and limit; soft-deleted users stay excluded.
The service logs nothing about the filter. No schema change (`idx_users_role` already exists).
Regression roots: `TestUserRoleIsValid` (`internal/models`); `TestUserListRoleFilterHandler`
(`internal/handler`); `TestUserServiceListFiltered` (`internal/service`);
`TestUserListRoleFilterRepository{SQLite,MySQL}` (`internal/repository`);
`TestUserListRoleFilters{SQLite,MySQL}` (`internal/integration`). The MySQL roots use a disposable
`TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable.

## Ticket comment email and the mail product name

**Ticket comment email (#219).** `POST /tickets/{id}/comments` takes an optional boolean
`email_customer`. The handler's checks are unchanged (role, id, ticket lookup, assignment, bind,
blank body); after them, `email_customer` together with `is_internal` is 400 `VALIDATION_ERROR`
(`error.details.email_customer`) and nothing is written. Customer, sales and unassigned support
callers keep their 403 before the body is read. Without the flag the handler calls the unchanged
`TicketService.CreateComment` and the 201 has no `delivery` key; with it,
`TicketService.CreateCommentWithCustomerEmail(c.Request.Context(), comment)`, and the 201 carries
the comment with its `delivery`. A not-found error is 404; anything else, including
`service.ErrTicketCommentMailUnavailable`, is 500.

- **Enabled by an option.** `NewTicketService` keeps its three parameters and takes
  `service.WithTicketCommentMail(deliveryRepo, txManager, mailer)`; `cmd/main.go` passes the
  ticket mail delivery repository, the shared `txManager` and the shared `appMailer`. Without the
  repository or the manager the method returns `ErrTicketCommentMailUnavailable` and writes
  nothing; a nil mailer is recorded `skipped`/`no_mailer`.
- **A transaction root.** Called with a context that already carries a transaction it returns
  `utils.ErrTransactionRootRequired` before reading or writing. Independently of the handler, it
  returns `service.ErrTicketCommentEmailNotAllowed`, with nothing written and nothing sent, for an
  internal note or an author (read by the service) who is neither admin nor support.
- **One transaction.** The ticket and the author are read on the request context, then one
  `WithTransaction` inserts the comment with its `first_responded_at` stamp
  (`CreateCommentWithFirstResponse`, a savepoint) and a `pending` `ticket_mail_deliveries` row
  (`TicketMailDeliveryRepository.WithTx(tx).Create`). A failure rolls all three back and nothing
  is sent; a cancelled request writes nothing.
- **After the commit, with no transaction held.** A nil mailer is `skipped`/`no_mailer`, decided
  before the customer is read. Otherwise the ticket's customer is read live through
  `customerRepo.GetByID`: not found (erased, soft-deleted, missing) is `skipped`/
  `customer_unavailable`, a database failure `failed`/`lookup_failed`, a blank address after
  trimming `skipped`/`no_recipient`. Then `Mailer.Send` is called exactly once with the trimmed
  address, the subject `Ticket T-000042 from <name>` (`service.TicketCommentEmailSubject`,
  `models.TicketNumber`; the name comes from `service.ResolveMailProductName`, read only here,
  after the commit and once a send will happen, through `service.WithTicketProductName`, else
  `GopherCRM`, #292) and a plaintext body holding the ticket number, the title and this
  comment's text only. A send error is `failed` with `timeout` (`os.ErrDeadlineExceeded`),
  `invalid_recipient` (`mailer.ErrInvalidAddress`, `mailer.ErrInvalidHeader`) or `mail_failed`; a
  mailer that implements `mailer.LogOnlyReporter` and reports `LogsOnly()` (the `LogMailer`, no
  `SMTP_HOST`) is `log_only` with `sent_at` null; otherwise `sent` with `sent_at`.
- **The stamp.** `TicketMailDeliveryRepository.FinishPending` is `UPDATE … WHERE id = ? AND status
  = 'pending'` on `context.WithoutCancel(ctx)` bounded by 5 s; it matches nothing on a row that is
  already finished (`repository.ErrTicketMailDeliveryNotPending`), so a second stamp never
  overwrites the first. A stamp that fails is logged as a Warn line with ids only; the method still
  returns nil and the returned delivery shows the stored `pending`.
- **At most once, no retry.** The answer is 201 whatever the mail outcome. Nothing resends: a row
  left `pending` (the stamp failed, or the process stopped between the commit and the stamp)
  means the outcome is unknown. There is no retry route, background retry or stuck detection.
- **Reads.** `delivery` (`gorm:"-"`, `omitempty`) is set only on that 201. `GET /tickets/{id}`,
  `GET /tickets/{id}/comments` and every other read carry no `delivery` key, for staff and
  customers alike.
- **Purge.** `ticketRepository.Delete` runs one transaction (a savepoint under `WithTx`) that
  locks the ticket row, hard-deletes the ticket's delivery rows (`HasTable` probe), cancels the
  ticket's automation runs and purges its notifications (#242, see *Automation runtime*) and then
  soft-deletes the ticket; the
  comments stay as before. The customer plan's `AfterScrub` also runs
  `purgeCustomerTicketMailDeliveries`, which hard-deletes every row whose `customer_id` is the
  erased customer or whose `ticket_id` names one of its tickets, soft-deleted tickets included, for
  all three erasure roots (see *Deleting personal data*). Erasing the comment's staff author keeps
  the rows (that erasure redacts comments, it does not delete them).
- **Privacy.** A row holds ids, a status, an error code and timestamps: no address, subject, title
  or comment text. The log lines of the path carry operation names, `ticket_id`, `comment_id`,
  `delivery_id`, `status` and `error_code` only; the mailer's error, which may quote the address,
  is classified and never logged.
- **Schema.** `ticket_mail_deliveries` (see [datamodel.md](datamodel.md#ticket-mail-delivery)) comes
  from `models.MigrateDatabase` on SQLite and from
  `migrations/20261113120000_add_ticket_mail_deliveries.up.sql` (created only when missing) on
  MySQL/MariaDB.

Regression roots: `TestTicketCommentEmailHandlerFlag`, `TestTicketCommentEmailHandlerErrorMapping`
(`internal/handler`); `TestTicketCommentEmailDelivery`, `TestTicketCommentEmailSkipReasons`,
`TestTicketCommentEmailFailures`, `TestTicketCommentEmailLogOnly`,
`TestTicketCommentEmailAtomicity{SQLite,MySQL}`, `TestTicketCommentEmailRefusedWhenUnwired`,
`TestTicketCommentEmailTransactionRootRequired`,
`TestTicketCommentEmailServiceRefusesInternalOrNonStaff`,
`TestTicketCommentEmailLogsOmitPersonalData`, `TestTicketCommentEmailRecordHoldsNoAddress`
(`internal/service`); `TestTicketMailDeliveryJSON`, `TestTicketMailDeliverySchemaSQLite`,
`TestTicketNumber` (`internal/models`); `TestTicketMailDeliveryRepository{SQLite,MySQL}`,
`TestTicketDeletePurgesMailDeliveries{SQLite,MySQL}`,
`TestTicketMailDeliveryErasure{SQLite,MySQL,WithoutTable}` (`internal/repository`);
`TestTicketMailDeliveriesMigrationFiles`, `TestTicketMailDeliveriesMigration` (`cmd/migrate`; the
newest-migration assertion has since moved to `TestAEORunOwnershipMigrationFiles`, #229); `TestTicketCommentEmailAPISQLite` (`tests`);
`TestTicketCommentMailProductionWiring` (`cmd`);
`TestTicketMailDeliveryErasureLeavesNoPersonalDataInAnyTable` (`test/integration`). The MySQL roots
use a disposable `TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable; CI limit:
`make verify-backend` has no DSN and skips them. Registering the migration roots in the Database
migrations workflow is an operator follow-up.

**Mail product name (#292).** The password reset mail (subject `Reset your <name> password` and the
first body line `A password reset was requested for your <name> account.`), the quote mail subject
and the ticket reply mail subject take the product name from one place:
`service.ResolveMailProductName(source)` (`internal/service/product_name.go`).

- **The rule.** It calls `source.GetString("general.company_name")` (`service.ConfigGeneralCompanyName`)
  on every call, with no cache, so a value changed through the configuration endpoints reaches
  the next mail without a restart. The value is used trimmed of surrounding whitespace when it is
  non-blank, valid UTF-8, free of control characters (CR and LF included) and at most
  `service.MailProductNameMaxRunes` (100) runes. Otherwise the name is `GopherCRM`
  (`service.DefaultMailProductName`): a nil source, a missing row (`apperrors.ErrNotFound`), any
  other read error, a blank value, invalid UTF-8, a control character or a longer value. A read
  failure never blocks a send.
- **Wiring.** `MailProductNameSource` is the one-method `GetString` interface;
  `ConfigurationService` satisfies it. `cmd/main.go` builds `configService` before `authService`
  and passes `service.WithAuthProductName(configService)` (a variadic option of
  `NewAuthServiceWithSessions`) and `service.WithTicketProductName(configService)`; the quote
  service reuses the configuration source it already takes for the default currency. A service
  built without the option, or with a nil source, names `GopherCRM`.
- **When it is read.** Only when a mail will go out, with no transaction held: the reset after the
  token is stored (an unknown or inactive address makes no read), the quote before `Mailer.Send`,
  the ticket reply after the commit and the customer read (a `no_mailer`, `customer_unavailable`
  or `no_recipient` delivery makes no read).
- **The mailer.** `Mailer.SendPasswordReset(to, resetURL, productName)` takes the name; the SMTP
  transport trims it and replaces a blank one, or one holding a control character, with
  `mailer.DefaultProductName` (`GopherCRM`), so CR/LF can never reach the Subject header. A
  non-ASCII name is Q-encoded like any subject. The `LogMailer` does not log the name.
- **Logs.** A fallback caused by a read error or an invalid value logs one Warn line with
  `config_key=general.company_name` and `reason` (`lookup_failed`, with the error through
  `utils.LogError`, or `invalid_value`); a nil source or a blank value logs nothing. No line of
  the resolver or of the three send paths carries the stored name, accepted or rejected.
- **No writes.** Nothing writes, seeds, re-seeds or migrates a configuration row; no key is added.
  The configuration write endpoints still accept any string, so a value the mail refuses (over
  100 runes, a control character) can still show in the web app while the mail says `GopherCRM`.
- **Not covered.** Form mail subjects and automation `send_email` subjects are written by users and
  unchanged; `SMTP_FROM` and the other mail bodies are unchanged.

Regression roots: `TestMailProductNameResolver`, `TestMailProductNameQuoteSubject`,
`TestMailProductNameTicketSubject`, `TestMailProductNamePasswordReset`,
`TestMailProductNameLogsOmitValue` (`internal/service/product_name_test.go`);
`TestPasswordResetProductName` (`internal/mailer`, an in-process relay on 127.0.0.1);
`TestMailProductNameAPISQLite` (`tests`, the configurations rows compared before and after the
sends, and after a further `InitializeDefaults`); `TestMailProductNameProductionWiring` (`cmd`,
`setupRouter` against an in-process relay). Registering these roots in `scripts/ci/check-go.py`
profiles is an operator follow-up.

## Lookup indexes

**Lookup indexes are measured (#109).** `leads.email`, `leads.classification`,
`leads.external_id` and `companies.domain` carry non-unique indexes declared in the model tags and
in `migrations/20261029140000_add_lead_company_lookup_indexes.up.sql`; the plans before and after are in
[datamodel.md](datamodel.md#lookup-indexes-and-query-plans). A predicate that wraps an indexed
column in a function (`LOWER(domain)`) hides it from the index on SQLite and on MySQL/MariaDB;
store the value normalised and compare it plainly instead. `TestLookupIndexesServeLookupsSQLite`
captures the statements the repository really runs and asserts their SQLite plans use the
indexes; `TestLookupIndexesMigration` (fresh up/down, populated upgrade, AutoMigrate first,
rollback and reapply, index parity) is one of the roots the Database migrations workflow requires.
Plain equality relies on every stored domain being lower case: the service normalises on write,
and `models.MigrateDatabase` lower-cases any other row on every start on every driver, which is how
SQLite installs, which never run the SQL migration, get it. It discovers the rows with a plain
non-locking `SELECT id` (binary comparison on MySQL/MariaDB) and updates only those ids, by primary
key in chunks of 500, so a normalised database takes no write and no row lock on startup; a single
predicate `UPDATE` would lock every scanned record under InnoDB REPEATABLE READ on every boot. Roots:
`TestCompanyDomainNormalization{SQLite,MySQL}` (`internal/repository/company_domain_normalization_test.go`).

## Leads and companies

**Lead owners (#255)** — `POST /leads`, `PUT /leads/{id}` and `POST /leads/{id}/assign` share one
check, `checkLeadOwner` in `internal/service/lead_service.go`: the owner must be a live, active admin
or sales user, otherwise `apperrors.ErrInvalidLeadOwner`, which the handlers answer with 400
`INVALID_REFERENCE`, `details.field` naming the body field and one message per route whatever the
reason, so a caller cannot probe which ids exist or what they hold. The reason (`unknown`, `inactive`,
`role`) goes to the log with the ids and nothing else. An erased user's row still satisfies the
foreign key, but no scoped read finds it, so it counts as unknown; a lookup that fails for a database
reason stays a 500. The read runs inside the writing transaction and takes a row lock (the user
repository bound to `repository.LockingRead(tx)`): a plain read in MySQL's
REPEATABLE READ transaction would answer from the snapshot of an earlier plain read, such as the
company check, and miss a deactivation committed since. The owner's custom role and its permissions
are read in the same transaction with a shared lock (`UserRepository.GetByIDWithCurrentRole`, #677),
so a role change committed before the check is seen too; a role write in flight makes the check wait.
That makes the check current, not permanent:
a deactivation after it can still leave an inactive owner, because lead writes and user deactivation
share no lock protocol yet (#183). An update checks only an owner that differs from the stored one,
so a lead whose owner has left stays editable; an assignment always checks the user it names. The
assign route applies `service.CanViewLead` to the locked row before the owner check, so visibility
answers first and a sales user who hands a lead over loses it at once. A service built without a user
repository returns `ErrLeadOwnerCheckUnavailable` from these writes instead of skipping the check,
and `Assign` also refuses without a transaction manager. Public form submissions
(`form.DefaultOwnerID`) and the automation `assign_owner` step write `owner_id` under their own
rules. Regression roots: `TestLeadOwner{Unknown,Deactivated,Erased,WrongRole}Rejected{SQLite,MySQL}`,
`TestLeadOwnerValidAccepted{SQLite,MySQL}`, `TestLeadAssign{Permissions,SalesHandOff,
RevalidatesUnchangedOwner,ConvertedLead}SQLite`, `TestLeadUpdateUnchangedOwnerNotRevalidatedSQLite`,
`TestLeadOwnerRoleRulesPrecedeOwnerCheckSQLite`, `TestLeadOwnerLookupFailureIsServerErrorSQLite` and
`TestLeadOwnerRefusalLogsOmitPersonalData` (`test/integration/lead_owner_validation_test.go`);
`TestLeadOwnerCheckRefusedWhenUnwired`, `TestLeadOwnerSeesDeactivationCommittedAfterLeadLockMySQL` and
`TestLeadAssignIsAtomicSQLite` (`internal/service/lead_owner_validation_test.go`);
`TestLeadOwnerProductionWiring` (`cmd/lead_owner_wiring_test.go`). CI limit: the MySQL roots above are
discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them.

**Lead and company lock order (#182)** — lead conversion, lead update, bulk lead status and company
deletion share one lock order: **companies** (by primary key, `CompanyRepository.LockLive`:
`SELECT id … FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite, `gorm.ErrRecordNotFound` for a
missing or soft-deleted company; it takes no context and runs on the `WithTx(tx)` handle) →
**leads** (id ascending) → **customers** → **deals** (see [CONCURRENCY.md](CONCURRENCY.md)).

- `CompanyService.Delete` keeps its plain pre-read (the 404 fast path), then in one transaction
  locks the company first, then runs `UnlinkLeads`, `UnlinkCustomers`, `UnlinkDeals` and the soft
  delete. A company deleted between the pre-read and the lock is `apperrors.ErrNotFound` (404) with
  nothing written. It stays on `context.Background()`.
- `LeadService.ConvertToCustomer` and `LeadService.Update` (with a transaction manager) run each
  attempt as: a plain read of the lead's `company_id` on the transaction; `LockLive` on the
  distinct non-nil ids among that value and the company the call names (the caller's
  `customer.company_id` for a conversion, the sent `company_id` for an update), ascending; the
  lead's locking read. When the locked lead names another company than the plain read (a delete
  unlinked it, or another write moved it, in between) the attempt rolls back with the unexported
  `errLeadCompanyMoved` and is restarted. A conversion links the lead's company only when that
  lock found it live, else converts without the link (the existing Warn line). An update checks a
  sent `company_id` against the lock: a company deleted while the update waited is
  `ErrCompanyNotFound` (400 `INVALID_REFERENCE`); a stored company found deleted is not an error
  (the restart sees the unlink). Without a transaction manager `Update` is unchanged.
- Restarts happen only for `errLeadCompanyMoved` on an attempt that rolled back cleanly (not
  `ErrRollbackFailed`, `ErrTransactionPoisoned`, `ErrCommitFailed` or `ErrBeginFailed`) on a
  context that is not done (`shouldRestartLeadCompanyAttempt`), at most 3 times (4 attempts); then
  the call returns `apperrors.ErrLeadCompanyContended` with no customer, lead change or automation
  event written, answered 409 `CONFLICT` by `POST /leads/{id}/convert` and `PUT /leads/{id}`. The
  conversion's deadlock retries (`WithTransactionAndRetry`, 3) still run inside each attempt.
- `BulkStatusService.BulkSetLeadStatus` takes no company lock (`SetLeadStatus` never writes
  `company_id`) and reads its leads with `BulkStatusRepository.GetLeadsByIDs`, now a locking read of
  the live leads ordered by id ascending (`FOR UPDATE` on MySQL); soft-deleted leads still read as
  missing.
- Result: once a conversion or lead update and a company delete have both committed, no live
  customer or lead names the soft-deleted company, in either order, on SQLite, MySQL 8 and
  MariaDB 10.11, and neither order ends in error 1213.
- The restart, exhaustion, missing-company and company-lock log lines carry `lead_id`,
  `company_id`, `operation`, attempt counts and `error_code` only.
- Not covered: lead create, customer create/update, deal writes and form intake still check
  `company_id` with a plain read and can race a delete (see the roadmap); existing links to deleted
  companies are not repaired.

Regression roots: `TestCompanyRepositoryLockLive{SQLite,MySQL}`,
`TestBulkStatusLeadsLockingRead{SQLite,MySQL}` (`internal/repository`);
`TestCompanyServiceDeleteLocksCompanyFirst`, `TestCompanyServiceDeleteRacedAfterPreReadSQLite`,
`TestLeadCompanyRestartDecision`, `TestLeadCompanyRestartBoundSQLite` (an instance-local seam moves
the lead's `company_id` inside each attempt), `TestLeadCompanyLockOrderOutcomesSQLite`,
`TestLeadCompanyLockLogsOmitPersonalData`, `TestLeadConversionVsCompanyDeleteMySQL`,
`TestLeadUpdateVsCompanyDeleteMySQL` and `TestBulkLeadStatusVsCompanyDeleteMySQL`
(`internal/service`; the MySQL roots park the first party through instance-local seams, see the
second party's wait in `information_schema.PROCESSLIST` on two consecutive polls, and count the
attempts with an instance-local counter); `TestLeadCompanyContendedHandlerMapping`
(`internal/handler`). The MySQL roots use a disposable `TEST_MYSQL_DSN` schema (migrated by
`AutoMigrate` without foreign keys), skip without it and fail when it is unreachable. CI limit: `make verify-backend` has no DSN and skips them; `scripts/ci/check-go.py transaction`
discovers the MySQL roots through its `lead*`, `company*` and `bulk*` test-file patterns.

## Bulk endpoints

**Bulk endpoints** — only `POST /leads|tickets|tasks/bulk/status` exist, registered by
`SetupBulkStatusRoutes` outside the entity groups. `BulkHandler` depends on `BulkStatusService`
(`internal/service/bulk_status_service.go`), which holds nothing but the three status operations.
The route's permission guard runs first (for tickets the handler checks `tickets.update` before
reading the body); the handler then passes the caller's `leads.update` / `tickets.update` /
`tasks.update` scope to `bulkStatusService.BulkSet*Status(ctx, actorID, scope, ...)`. Scope `all`
may change any record, scope `own` only the caller's own records (leads it owns, tickets and tasks
assigned to it), and any other scope is refused. Each one normalises the ids (1–100, duplicates
count once), checks the scope, writes a pending
`bulk_operations` history row, then inside ONE transaction reads every listed record through
`BulkStatusRepository`, refuses the whole batch if any id is missing, not the caller's to change
or in a forbidden transition (the offending ids go into `error.details`), and only then writes
the status — plus, for leads and tickets (#242), the automation events on the same transaction;
the lead, ticket and task reads are locking reads in id order (#182, #242, #558) — and, as the last
write of that transaction, sets the history row `completed` (see below). There is no generic bulk
create/update/delete/action machinery any more (#112); a new
bulk operation gets its own narrow service method and repository halves the same way, and a bulk
erasure would need per-item isolation designed afresh (each erasure is one savepoint-bracketed
unit, `runErasureUnit`, but a batch would also have to report per-item failures and authorize
every item's whole graph).

**Every bulk history row reaches a terminal status (#196).** `completed` is written inside the
batch transaction by `BulkOperationRepository.WithTx(tx).CompletePending(ctx, id)`, a conditional
`UPDATE … WHERE id = ? AND status = 'pending'` by primary key. It commits with the records or not
at all, so a row that is still `pending` belongs to a batch that did not commit. When it matches
no row it returns `apperrors.ErrBulkOperationNotPending` and the whole batch rolls back. That
sentinel is a plain `errors.New` that wraps none of `ErrNotFound`, `ErrForbidden` or
`gorm.ErrRecordNotFound` and is not an `AppError`, so `respondBulkStatusError` answers 500 with no
handler change: a batch whose own row was swept before it committed answers 500 and changes
nothing. After a batch that rolled back, `recordBulkStatusOutcome` writes only `failed`, through
`UpdateStatus` on `context.WithoutCancel(ctx)` bounded by 5 s. `UpdateStatus` is conditional on
`status = 'pending'` too, so a late write never overwrites a `completed` or `failed` row; its
`ErrBulkOperationNotPending` is ignored without a warning, and any other failure is only logged
(`operation_id` and the error through `utils.LogError`) and never changes the answer.

A row whose `failed` write timed out stays `pending` until `service.BulkOperationRecoveryRuntime`
(`internal/service/bulk_operation_recovery_runtime.go`, wired by `cmd/bulk_recovery_runtime.go`)
sweeps it. The sweep runs at boot and every `BulkOperationSweepInterval` (15 minutes) on every
replica, with the cutoff `now - BulkOperationStaleAfter` (1 hour); the horizon, the interval and
the chunk size (`repository.BulkOperationSweepChunk`, 500) are package constants, not settings.
`MarkStalePendingFailed(ctx, cutoff)` works in two steps: a plain, non-locking `SELECT id … WHERE
status = 'pending' AND created_at < cutoff AND deleted_at IS NULL ORDER BY id`, read in chunks of
at most 500 by keyset, then per chunk one `UPDATE` by primary key (`id IN (…)` plus the same
predicate) that sets `failed` and `updated_at`. It returns the summed count; extra sweeps on other
replicas only compete for the same rows, each changing once. The sweep must never take a lock
through `idx_bulk_operations_status` (no locking read, no `UPDATE` whose predicate is the status
index): an index-driven `UPDATE` locks the secondary index before the primary key, the reverse of
`CompletePending`'s order, and deadlocks (MySQL/MariaDB error 1213) against live batches. The
index (non-unique, `bulk_operations(status)`) comes from `models.MigrateDatabase` on SQLite and
from `migrations/20261110120000_add_bulk_operation_status_index.up.sql` on MySQL/MariaDB. `failed`
therefore means none of the batch's records changed, with one exception: rows already `pending`
when this was deployed recorded `completed` after their commit under the old code, so their batch
may have committed; they are swept to `failed` all the same (outcome unknown, documented, not
stored). Sweep log lines carry only the `failed` count and, on failure, the error through
`utils.LogError`. The runtime runs on a `lifecycle.Loop` (`internal/lifecycle`): `Stop` cancels the
sweep and joins its goroutine under main's shared deadline, and `Start` after `Stop` does nothing.
Since #453 every sweep (wired with `service.WithOIDCAttemptPruning`) also prunes the single sign-on
attempts created more than `service.OIDCLoginAttemptRetention` (one day) earlier
(`service.PruneOIDCLoginAttempts`: a plain read of up to 500 ids, then a delete by primary key
repeating the predicate, until a chunk deletes fewer than 500). The prune does not change the
sweep's count or error; a failure is logged through `utils.LogError` and a non-zero count as
`pruned single sign-on attempts` with the `pruned` field.
Since #485 both prunes (this one and the sign-in audit prune of #454) run their statements on the
sweep's context, so `Stop` cancels a prune that is waiting for a database connection instead of
letting it use up main's shared drain deadline.

Regression roots, on SQLite and on a disposable `TEST_MYSQL_DSN` schema unless noted:
`TestBulkOutcomeRecovery{SQLite,MySQL}` (a failed completion rolls the batch back; a rolled-back
batch whose `failed` write times out is `pending`, then `failed` after one sweep past the horizon;
a row swept before its batch completed; late writes), `TestBulkOperationRecoveryRuntimeLifecycle`,
`TestBulkOperationRecoveryLogsOmitPersonalData` and the updated
`TestRequestCancellation{SQLite,MySQL}` and `TestBulkStatusService{SQLite,MySQL}`
(`internal/service`); `TestBulkOperationRepositoryOutcome{SQLite,MySQL}` (1,201 stale rows in three
chunks, captured statements, an `EXPLAIN` on MySQL) and `TestBulkOperationSweepConcurrent{SQLite,MySQL}`
(sequential on SQLite; on MySQL concurrent sweeps, a sweep racing a batch, and live batches against
looping sweeps with no error 1213) (`internal/repository`); `TestBulkOperationStatusIndexSQLite`
(`internal/models`); `TestBulkOperationRecoveryProductionWiring` (`cmd`, the real wiring over a
SQLite file); `TestBulkOperationStatusIndexMigrationFiles` and `TestBulkOperationStatusIndexMigration`
(`cmd/migrate`). CI limit: the MySQL roots in `internal/service` and `internal/repository`
`bulk*_test.go` are discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them. The Database migrations workflow runs the
migration roots.
