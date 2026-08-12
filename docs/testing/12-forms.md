# Forms — Test Cases

Cases for the forms module: the form definitions managed at `/forms`, the public rendering and
submission surface under `/api/v1/forms/public/`, the spam pipeline, the double-opt-in email flow,
lead creation, and the erasure of submission data. Every **Expected** states what the build does
**today**; where the behaviour is surprising a **Known issue** line names it.

**Sources**

- `internal/models/form.go`, `internal/models/database.go`
- `internal/repository/form_repository.go`, `internal/repository/erasure_cascade.go`
- `internal/service/form_service.go`, `internal/service/interfaces.go`
- `internal/forms/challenge.go`, `internal/forms/recaptcha.go`
- `internal/handler/form_handler.go`, `form_routes.go`, `form_public_handler.go`,
  `form_public_routes.go`, `form_public_html.go`, `assets/form_embed.js`
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
- **Mail is log-only without `SMTP_HOST`**, and the log redacts tokenised links, so the opt-in
  confirm flow can only be end-to-end tested with a mail sink; the token path itself is pinned in
  Go tests.

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
  keys, consent required when consent text set) returns 400 with per-field details · automated ·
  `form_service_test.go` + `form_public_handler_test.go`.
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
- **TC-FORM-063 — confirmation flips the submission, creates/attaches the lead, sends the
  follow-up with the content link** · automated · `form_service_test.go`.
- **TC-FORM-064 — full opt-in loop through a real inbox** · blocked · needs an SMTP sink
  (mailpit or similar); the log mailer redacts the tokenised link by design.
- **TC-FORM-065 — custom confirmation bodies missing {confirmation_link} still get the link
  appended** · automated · `form_service_test.go`.

## Leads and erasure

- **TC-FORM-080 — a clean submission creates a lead owned by the form's default owner with the
  form name as source** · automated · `form_service_test.go`; live in the smoke script.
- **TC-FORM-081 — an existing lead with the same email is reused: submission linked, note
  appended, no duplicate lead** · automated · `form_service_test.go` dedupe cases.
- **TC-FORM-082 — erasing a lead scrubs its submissions and hard-deletes their confirmation
  tokens, in both directions of the conversion cascade** · automated ·
  `test/integration/form_submission_erasure_test.go`.
- **TC-FORM-083 — unlinked spam/pending submissions are untouched by lead erasure** · automated ·
  same file. **Known issue:** their eventual clean-up is a retention sweep that does not exist
  yet (ROADMAP).
