# Mail

> **For:** Developers changing mail delivery and ticket comment email. Part of the [documentation index](../README.md).

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
- **Schema.** `ticket_mail_deliveries` (see [../reference/data-model.md](../reference/data-model.md#ticket-mail-delivery)) comes
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
