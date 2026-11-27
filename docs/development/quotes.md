# Quotes

> **For:** Developers changing quotes, their links and the quote PDF. Part of the [documentation index](../README.md).

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
the same fallback the password reset shares (form mail records it `log_only`, see
[../reference/limitations.md](../reference/limitations.md)). The subject is `Quote Q-000042 from <name>`, the name resolved at send time by
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
  the erasure in [erasure.md](erasure.md#deleting-personal-data) for which quotes and for a token minted while the erasure runs.
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
