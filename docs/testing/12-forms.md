# Forms — Test Cases

Cases for the forms module: the form definitions managed at `/forms`, the public rendering and
submission surface under `/api/v1/forms/public/`, the spam pipeline, the double-opt-in email flow,
lead creation, and the erasure of submission data. Every **Expected** states what the build does
**today**; where the behaviour is surprising a **Known issue** line names it.

**Sources**

- `internal/models/form.go`, `internal/models/form_mail_delivery.go`, `internal/models/database.go`
- `internal/repository/form_repository.go`, `internal/repository/form_mail_delivery_repository.go`,
  `internal/repository/erasure_cascade.go`
- `internal/service/form_service.go`, `internal/service/form_mail_delivery.go`,
  `internal/service/interfaces.go`, `internal/service/form_submission_purge.go`,
  `internal/service/form_retention_runtime.go`, `cmd/form_retention_runtime.go`
- `internal/forms/challenge.go`, `internal/forms/recaptcha.go`
- `internal/handler/form_handler.go`, `form_delivery_handler.go`, `form_routes.go`,
  `form_public_handler.go`, `form_public_routes.go`, `form_public_html.go`, `assets/form_embed.js`
- `internal/middleware/cors.go`, `internal/config/config.go`, `internal/mailer/`
- `gocrm-ui/src/pages/forms/`, `gocrm-ui/src/api/endpoints/forms.ts`
- `scripts/forms_live_smoke.sh` (manual, needs a running backend and an admin JWT)

**Constraints**

- **No forms Playwright spec exists yet.** Cases against the SPA are *planned* for
  `gocrm-ui/e2e/tests/forms.spec.ts`; where a Go or Vitest test already pins the behaviour the
  Automation line names it.
- **`/forms` is closed to `customer`.** Group guard is `RequireRole(admin, sales, support)`;
  create/update need admin or sales, delete needs admin. The SPA mirrors this (route gate plus
  read-only rendering for support).
- **The public endpoints are open by design** — no auth, permissive credential-less CORS, and
  per-route rate limits (generous on reads, strict on submits/confirms). E2E against them must
  send an `Origin` header to prove the cross-origin path, and must respect that the strict tier
  (10/min, burst 5) throttles rapid submits unless `DISABLE_RATE_LIMIT=true`.
- **Mail is log-only without `SMTP_HOST`** (each delivery is recorded `log_only`), and the log
  redacts tokenised links, so the opt-in confirm flow can only be end-to-end tested with a mail
  sink; the token path itself is pinned in Go tests.
- **The confirmation regressions run on real SQL.** `form_confirmation_regression_test.go` runs
  every scenario on in-memory SQLite with the production one-connection pool and, when
  `TEST_MYSQL_DSN` points at a disposable server, on a uniquely named MySQL schema that is created
  and dropped per root. Without the variable the MySQL roots skip, which the named acceptance
  checks reject; with it set but unreachable they fail. Never point the variable at an
  application database.

## Definition management

- **TC-FORM-001 — create a form with valid fields** · automated ·
  `internal/service/form_service_test.go` (create cases) + `form_handler_test.go` (201 envelope).
- **TC-FORM-002 — definition validation rejects bad field sets** (no email field, duplicate names,
  select without options, hidden+required, redirect without URL, lead creation without owner) ·
  automated · `internal/models/form_test.go` `TestFormValidateDefinition` table.
- **TC-FORM-003 — default owner must be an active admin or sales user** · automated ·
  `form_service_test.go` owner-check cases.
- **TC-FORM-004 — public id is random, unique, and immutable across updates** · automated ·
  `form_service_test.go` (create + update cases).
- **TC-FORM-005 — RBAC matrix on /forms** (support reads but cannot write; customer 403 on
  everything; delete admin-only, 204) · automated · `form_handler_test.go` RBAC cases.
- **TC-FORM-006 — list carries submission_count and meta.total** · automated ·
  `form_handler_test.go` list cases; UI table in `FormList.test.tsx`.
- **TC-FORM-007 — builder blocks invalid definitions client-side** (zod mirror: one email field,
  redirect URL required, option editor) · automated · `gocrm-ui/src/pages/forms/FormBuilder.test.tsx`.

## Public rendering and submission

- **TC-FORM-020 — published form renders via the embed script on a foreign origin** · planned
  (`forms.spec.ts`); verified manually 2026-08-12 with Playwright against a `localhost:8777` host
  page (render, inline validation, submit, thank-you message; zero console errors from the embed).
- **TC-FORM-021 — draft/archived/deleted forms answer 404 publicly** · automated ·
  `form_service_test.go` + `form_public_handler_test.go`.
- **TC-FORM-022 — the public definition leaks nothing** (no notify emails, owner ids, or email
  bodies in the JSON) · automated · `form_service_test.go` leak assertion.
- **TC-FORM-023 — server-side validation** (required, email format, select membership, unknown
  keys, consent required when consent text set, and a lead-mapped value wider than its column —
  email 255, first/last name 100, phone 50, company 200, position 100 — even when the definition
  declares more) returns 400 with per-field details before any spam layer or insert · automated ·
  `form_service_test.go` + `form_public_handler_test.go`, and on MySQL by
  `gocrm-ui/e2e/tests/forms-public-api.spec.ts` (before the column limits this path answered 500
  with Error 1406). Saved and published definitions state the
  clamped limit; a legacy pending opt-in row holding an over-wide value is refused at confirm
  and the rollback hands its token back unspent.
- **TC-FORM-024 — submissions over 64KB are rejected (413)** · automated ·
  `form_public_handler_test.go`.
- **TC-FORM-025 — cross-origin requests to /forms/public get permissive credential-less CORS while
  other routes keep the allowlist** · automated · `internal/middleware/cors_test.go`.
- **TC-FORM-026 — hosted standalone page serves the form** (`/forms/public/:key/view`) · automated
  (page shell + key echo) · `form_public_handler_test.go`; full render covered by TC-FORM-020's
  mechanism.

## Spam pipeline

- **TC-FORM-040 — honeypot content marks the submission spam but answers success-shaped** ·
  automated · `form_service_test.go`; exercised live by `scripts/forms_live_smoke.sh`.
- **TC-FORM-041 — sub-3-second fills are spam (time_trap), forged challenges are 400** · automated ·
  `form_service_test.go` + `internal/forms/challenge_test.go`.
- **TC-FORM-042 — origin outside allowed_domains is spam (domain), exact host[:port] match** ·
  automated · `form_service_test.go` origin cases.
- **TC-FORM-043 — reCAPTCHA failures/low scores are spam (captcha); missing server keys skip the
  layer** · automated · `form_service_test.go` + `internal/forms/recaptcha_test.go` (stub server).
- **TC-FORM-044 — spam rows are stored with a reason, create no lead and send no mail** ·
  automated · `form_service_test.go` (mail-capture fake asserts zero sends).
- **TC-FORM-045 — strict rate tier throttles rapid submits** · planned · needs an e2e that
  tolerates 429s; the tier wiring itself is asserted in `form_public_handler_test.go` route setup.

## Double opt-in and mail

- **TC-FORM-060 — opt-in submissions stay pending, create no lead, and email a confirmation
  link** · automated · `form_service_test.go` opt-in cases.
- **TC-FORM-061 — GET /confirm never consumes the token; only the POSTed button does** ·
  automated · `form_public_handler_test.go` (service-call counter).
- **TC-FORM-062 — confirmation is single-use and expires after 48h; re-submitting invalidates the
  earlier token** · automated · `form_service_test.go` + `form_repository_test.go`.
  - The token is claimed by a conditional `UPDATE` (`used_at IS NULL AND expires_at > now`, exactly
    one affected row) inside the same transaction that re-reads the pending submission, writes the
    lead and confirms the submission. A spent, expired, unknown or re-submitted token, a token that
    expires or is spent between the lookup and the claim, a submission that is no longer pending
    and a token whose submission or form is gone are all refused with nothing written and nothing
    mailed. Before this, the token was stamped by an unconditional update *before* the transaction,
    so a failed confirmation consumed a usable link and two simultaneous clicks could both pass
    the lookup.
  - `TestFormConfirmationTokenClaimSQLite` / `TestFormConfirmationTokenClaimMySQL`
    (`form_confirmation_regression_test.go`) and `TestFormRepositoryMarkConfirmationTokenUsed`.
- **TC-FORM-063 — confirmation flips the submission, creates/attaches the lead, sends the
  follow-up with the content link** · automated · `form_service_test.go`.
  - All of it commits or none of it does: an injected lead insert, lead update or submission update
    failure rolls back the claim, the submission and the lead (an existing lead's notes included),
    the same link then works on retry, and no notification or follow-up is sent for the failed
    attempt. Mail goes out only after the commit; the regression's mailer reads the database when
    each message is handed over and asserts it already sees the confirmed, linked submission and
    the spent token.
  - `TestFormConfirmationRollbackSQLite` / `TestFormConfirmationRollbackMySQL`.
- **TC-FORM-064 — full opt-in loop through a real inbox** · blocked · needs an SMTP sink
  (mailpit or similar); the log mailer redacts the tokenised link by design.
- **TC-FORM-065 — custom confirmation bodies missing {confirmation_link} still get the link
  appended** · automated · `form_service_test.go`.
- **TC-FORM-066 — simultaneous clicks on one confirmation link yield exactly one confirmation**
  · automated · `TestFormConfirmationConcurrentSQLite` / `TestFormConfirmationConcurrentMySQL`.
  Three requests race for one link: exactly one succeeds, one lead is created and linked, the
  team is notified once and the visitor gets one follow-up; the others are told the link is no
  longer valid, and a later replay is refused. On MySQL every competitor is held inside its
  transaction after reading the token as spendable until all have read it, so the conditional
  claim alone decides. On SQLite the production pool holds one connection, which serialises the
  competitors (a barrier inside the transaction would deadlock), so the competitors start together
  and the assertions are the same. Every wait is bounded.

## Leads and erasure

- **TC-FORM-080 — a clean submission creates a lead owned by the form's default owner with the
  form name as source** · automated · `form_service_test.go`; live in the smoke script.
- **TC-FORM-081 — an existing lead with the same email is reused: submission linked, note
  appended, no duplicate lead** · automated · `form_service_test.go` dedupe cases.
- **TC-FORM-082 — erasing a lead scrubs its submissions and hard-deletes their confirmation
  tokens, in both directions of the conversion cascade** · automated ·
  `test/integration/form_submission_erasure_test.go`.
- **TC-FORM-083 — unlinked spam/pending submissions are untouched by lead erasure** · automated ·
  same file. Their clean-up is the retention purge, cases TC-FORM-090 onward.
- **TC-FORM-084 — repeated large submissions from one address keep that lead's notes within the
  TEXT column (65,535 bytes).**
  - Each submission block is capped at 16 KiB.
  - Only whole form blocks are dropped, oldest first, under a marker; the newest is always kept.
  - A block runs from its header line to its end line. Everything else is **never** dropped:
    staff text before, between or after blocks, and blocks written before blocks carried an end
    line. Two exceptions: trailing newlines give way to the next block, and a verbatim copy of the
    trim marker is moved. Text typed *inside* a block, or a pasted copy of a whole block, goes with
    that block.
  - When the kept text leaves no room, a one-line pointer to the stored submission is added instead.
    Pointers are never dropped, so a lead whose kept text is near the limit fills up with them;
    staff clean such notes up by hand.
  - Neither a submitted value nor the form name can forge a block header or end line, not even with
    extra dashes.
  - Every submission stays whole with the form.
  · automated · `lead_notes_test.go` (including a 2,000-case randomized invariant sweep),
  `form_service_test.go`,
  and on MySQL by `forms-public-api.spec.ts` (before the bound the fourth such submission answered
  500 with Error 1406).
- **TC-FORM-085 — markup-heavy values (`<`, `&`, which JSON-escape to six bytes) are accepted and
  stored intact: `form_submissions.data` is MEDIUMTEXT, which holds any submission the 64 KiB body
  cap admits** · automated · `form_submission_data_test.go`, and on MySQL by
  `forms-public-api.spec.ts` (a 10,000-character `<` message used to answer 500).
- **TC-FORM-086 — staff notes added to a form-created lead survive repeated large submissions.**
  A lead created by a form starts with a form block, so staff notes added later follow it.
  - Steps: submit once (the lead is created); append a staff note through `PUT /leads/{id}`; submit
    five ~20 KB messages from the same address.
  - The staff note is still there, the notes fit 65,535 bytes, the newest block is kept and the
    oldest are trimmed.
  - Before the fix, everything after the first block header counted as form blocks, so a few public
    submissions erased the staff note.
  · automated · `lead_notes_test.go` `TestAppendLeadNotesKeepsStaffTextWrittenAfterABlock`, and on
  MySQL and MariaDB by `forms-public-api.spec.ts`.

## Retention purge

The MySQL roots use a disposable schema on `TEST_MYSQL_DSN`, and confirm every lock wait in
`information_schema.PROCESSLIST` on two consecutive polls. SQLite's single connection proves the
same outcomes in sequence.

- **TC-FORM-090 — `FORM_RETENTION_DAYS` keeps the purge off unless it is a positive number, and
  lands inside 3..36500** · automated · `internal/config/config_test.go`
  `TestLoadFormRetentionDays`; `cmd/form_retention_runtime_test.go`
  `TestFormRetentionProductionWiring` proves off and on through `setupRouter`, and that the drain
  handle `main` receives waits for a chunk parked in flight. The first boot with the setting removes
  the backlog without any request.
- **TC-FORM-091 — only old spam and never-confirmed rows with no lead go, with their tokens; every
  other row is byte-for-byte unchanged** · automated ·
  `internal/repository/form_submission_purge_test.go`
  `TestFormSubmissionPurge{RemovesEligible,KeepsIneligible}{SQLite,MySQL}`;
  `form_submission_purge_test.go` `TestFormSubmissionPurgeCutoffClockBasis{SQLite,MySQL}` and
  `TestFormSubmissionPurgeRefusesShortRetention`.
- **TC-FORM-092 — a backlog goes in chunks of 500, each atomic, and competing sweeps count each row
  once** · automated · `form_submission_purge_test.go`
  `TestFormSubmissionPurge{BacklogChunks,ChunkAtomic,CompetingSweeps}{SQLite,MySQL}`.
- **TC-FORM-093 — a purge never loses a confirmation or a re-submission, and a late link gets the
  generic rejection** · automated · `form_submission_purge_test.go`
  `TestFormSubmissionPurge{ConcurrentConfirmation,ConcurrentResubmission,LateConfirmationLink}{SQLite,MySQL}`;
  `tests/form_retention_api_test.go` `TestFormRetentionAPISQLite` covers the lists, the count, the
  404 and the invalid-link page over HTTP.
- **TC-FORM-094 — the runtime sweeps at start and on every tick, drains a chunk in flight, and logs
  counts only** · automated · `form_retention_runtime_test.go`
  `TestFormRetentionRuntime{Lifecycle,StopDuringChunk}`; `form_submission_purge_test.go`
  `TestFormSubmissionPurgeLogsOmitPersonalData`. **Known issue:** both form-delete dialogs still say
  "Collected submissions are retained" until a separate frontend task merges.

## Mail delivery records

Every form mail gets a `form_mail_deliveries` row (#216). The service and repository roots run on
in-memory SQLite and, with `TEST_MYSQL_DSN`, on a disposable MySQL schema. The SPA does not show the
rows yet (a later frontend task).

- **TC-FORM-100 — every mail is recorded in the submission or confirmation transaction and stamped
  with its outcome** (one notification row per address plus the follow-up for a received
  submission, the confirmation for a pending one, notifications and follow-up on confirmation;
  `sent`, `failed` with `mail_failed`/`timeout`/`invalid_recipient`, `skipped` with
  `no_mailer`/`no_recipient`; no row for spam or a rolled-back transaction; a slow `Send` or a
  request cancelled after the commit never leaves a row `sending`; the public answers are
  unchanged) · automated · `internal/service/form_mail_delivery_test.go`
  `TestFormMailDeliveryRecorded{SQLite,MySQL}`.
- **TC-FORM-101 — without `SMTP_HOST` the LogMailer's deliveries read `log_only`, never `sent`** ·
  automated · `TestFormMailDeliveryLogOnlyRecorded`; `internal/mailer/log_only_test.go`
  `TestLogMailerReportsLogOnly`.
- **TC-FORM-102 — a failed, log-only or stuck follow-up or notification is retried once, from the
  current form, and every refusal writes nothing** (sent, skipped and fresh pending/sending rows; a
  form without follow-up subject; a submission neither received nor confirmed; an address no longer
  in `notify_emails`; a soft-deleted form; the loser of two concurrent retries; 404 for an unknown
  row or a vanished submission; a send failure stamps `failed`) · automated ·
  `TestFormMailDeliveryRetry{SQLite,MySQL}`.
- **TC-FORM-103 — a confirmation retry re-arms the single token in place and the new link confirms**
  (the old link is dead; refused, with every token unchanged, for a confirmed or lead-linked
  submission, a newer non-spam submission of the same address, or zero or two token rows; an old
  expired submission survives the following purge) · automated ·
  `TestFormMailDeliveryConfirmationRetry{SQLite,MySQL}`.
- **TC-FORM-104 — pending or sending rows become stuck and retryable after 15 minutes** · automated
  · `TestFormMailDeliveryStuckRows`.
- **TC-FORM-105 — delivery log lines carry ids, kind, status and error code only** · automated ·
  `TestFormMailDeliveryLogsOmitPersonalData`.
- **TC-FORM-106 — the purge and lead erasure remove the rows of the submissions they remove** ·
  automated · `internal/repository/form_mail_delivery_repository_test.go`
  `TestFormMailDeliveryPurgedWithSubmission{SQLite,MySQL}`;
  `TestFormMailDeliveryErasedWithLead{SQLite,MySQL}`.
- **TC-FORM-107 — staff reads and the retry route over HTTP** (`deliveries` on the submission reads,
  `[]` when none, no address field; `GET /forms/deliveries` filters and pagination; retry 200, 409,
  404, 502; support reads but gets 403 on retry; customer 403 everywhere) · automated ·
  `tests/form_mail_delivery_api_test.go` `TestFormMailDeliveryAPISQLite`;
  `internal/handler/form_delivery_handler_test.go`; `TestFormMailDeliveryRepository{SQLite,MySQL}`;
  `TestFormMailDeliveryJSON`.
