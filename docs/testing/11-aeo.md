# Answer Engine Optimization (AEO) — Test Cases

Cases for the AEO module: the brand profile and provider roster at `/aeo/settings`, the tracked
prompts and answer transcripts at `/aeo/prompts`, the visibility dashboard at `/aeo`, the citation
report at `/aeo/citations`, and the run lifecycle behind all four. Every **Expected** states what
the build does **today**; where the behaviour is surprising a **Known issue** line names it.

**Sources**

- `docs/specs/2026-08-11-aeo-design.md`
- `internal/models/aeo.go`, `internal/models/database.go`
- `internal/handler/aeo_handler.go`, `internal/handler/aeo_routes.go`, `cmd/main.go`
- `internal/service/aeo_service.go`, `internal/service/interfaces.go`
- `internal/repository/aeo_repository.go`, `internal/repository/interfaces.go`
- `internal/aeo/` — `provider.go`, `anthropic.go`, `openai_compat.go`, `analysis.go`,
  `engine.go`, `scheduler.go`
- `internal/errors/errors.go`, `internal/config/config.go`
- `gocrm-ui/src/pages/aeo/AEODashboard.tsx`, `AEOPrompts.tsx`, `AEOCitations.tsx`,
  `AEOSettings.tsx`
- `gocrm-ui/src/api/endpoints/aeo.ts`, `gocrm-ui/src/routes/index.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `scripts/aeo_live_smoke.sh` (manual live-provider smoke, never in CI)

**Constraints**

- **No AEO Playwright spec exists yet.** Every case below is *planned* against
  `gocrm-ui/e2e/tests/aeo.spec.ts` or *blocked*. Where a Go or Vitest test already pins the same
  behaviour, the Automation line names it so the E2E case can be written against a known-good
  contract rather than re-derived.
- **Runs cost money.** A run is `active prompts × configured providers` external calls. No E2E case
  may trigger `POST /aeo/runs` against a deployment whose environment carries real provider keys.
  The intended E2E setup runs the backend with `AEO_CUSTOM_BASE_URL` pointed at a local stub (or at
  LM Studio) and every hosted key unset, which is also what makes the run deterministic.
- **`/aeo` is closed to `customer`.** `SetupAEORoutes` guards the whole group with
  `RequireRole(admin, sales, support)`; writes (profile, prompts, generate, run) additionally
  require admin or sales, and prompt deletion requires admin. The SPA mirrors this: one pathless
  `ProtectedRoute requiredRole={['admin','sales','support']}` wraps all four routes and the sidebar
  entry carries the same roles.
- **Only the admin account is available to e2e.** Sales and support accounts need an
  admin-authenticated `POST /users` plus a role-login helper that still does not exist; a
  *customer* is obtainable through public registration. Cases needing a non-admin, non-customer
  session are marked *blocked* with that note — the same blocker 10-labels.md records.
- **The reporting window is an enum, not a number.** `?days=` accepts only 7, 30 and 90
  (`aeoAllowedRangeDays`); anything else silently falls back to 30. The service clamps any range
  wider than 90 days regardless of how it was derived.
- **Answers are non-deterministic.** Never assert on answer text from a live provider. Assert on
  the recorded row (`brand_mentioned`, `first_mention_pos`, `citations`) with a stubbed provider,
  or on structure only.

---

## 11.1 Settings and Brand Profile

### TC-AEO-001 — Settings page on a fresh install has no profile
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; `aeo_profiles` empty.
- **Steps:**
  1. Navigate to `/aeo/settings`.
  2. Wait for the network to settle.
- **Expected:** `GET /api/v1/aeo/profile` returns 404 `NOT_FOUND`; the endpoint module maps that single status to `null` rather than an error (`aeoApi.getProfile`). The page renders the `h4` "AEO Settings", the alert "No brand profile configured yet. Save one below before starting a run.", and an empty profile form. No error `Alert` is shown — an unconfigured profile is a state, not a failure.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Unit-pinned by `gocrm-ui/src/pages/aeo/AEOSettings.test.tsx` and `gocrm-ui/src/api/endpoints/aeo.test.ts`.

### TC-AEO-002 — Save a brand profile
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in on `/aeo/settings`.
- **Steps:**
  1. Fill Brand name `Acme`, description, one alias `Acme Inc`, one owned domain `WWW.Acme.com`.
  2. Add a competitor `Globex` with domain `globex.com`.
  3. Press **Save profile**.
- **Expected:** `PUT /api/v1/aeo/profile` returns 200 with the stored profile. The service normalises before writing: domains are trimmed, lowercased, a leading `www.` is stripped and duplicates dropped, so the response carries `acme.com`. The row's ID is pinned to 1 — saving again updates the same row instead of creating a second profile. A success toast appears and the form repopulates from the response.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-003 — Whitespace-only brand name is a 400, not a 500
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin token.
- **Steps:**
  1. `PUT /api/v1/aeo/profile` with `{"brand_name":"   "}`.
- **Expected:** 400 with `error.code` `VALIDATION_ERROR`. `required` only rejects an empty string, so the blank name reaches the service, which trims it and returns `ErrAEOInvalidProfile`; the handler matches that sentinel with `errors.Is`. Through the UI the zod schema rejects it first and no request is made.
- **Known issue:** Fixed 2026-08-11 — this previously fell through `respondError`'s default branch and returned 500 `INTERNAL_ERROR` with an error-level log. Regression-tested by `internal/handler/aeo_handler_test.go` "TestSaveProfile_BlankBrandNameIs400".
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` (API-level assertion).

### TC-AEO-004 — Competitor and alias list limits
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin token.
- **Steps:**
  1. `PUT /api/v1/aeo/profile` with 21 competitors.
  2. Repeat with 21 brand aliases, then with 21 owned domains.
- **Expected:** Each returns 400 `VALIDATION_ERROR` from the binding tags (`max=20` on all three collections, `max=120` per alias, `max=255` per domain, `max=2000` on the description). Nothing is written.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-005 — Provider chips report configuration, never keys
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; backend started with exactly one provider key set.
- **Steps:**
  1. Navigate to `/aeo/settings` and read the provider chips.
  2. Inspect the `GET /api/v1/aeo/providers` response body.
- **Expected:** Six chips (`anthropic`, `openai`, `gemini`, `kimi`, `perplexity`, custom), each `data-testid="provider-chip-<name>"`, showing the engine name and the model it would use. Exactly one is marked configured. The payload carries only `name`, `model` and `configured` — no key material, no base URL. The custom engine counts as configured when `AEO_CUSTOM_BASE_URL` is set, with or without a key.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-006 — "Run now" with no engines configured
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; backend started with no provider keys and no `AEO_CUSTOM_BASE_URL`; a saved profile.
- **Steps:**
  1. On `/aeo/settings`, press **Run now**.
- **Expected:** `POST /api/v1/aeo/runs` returns 503 with `error.code` `PROVIDERS_UNAVAILABLE` and the message "no AEO providers are configured". The page shows that message in an error toast and no run row is created. Note the ordering: a missing profile is reported before missing providers.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Sentinel mapping pinned by `internal/handler/aeo_handler_test.go`.

### TC-AEO-007 — Support sees settings read-only
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Support user logged in.
- **Steps:**
  1. Navigate to `/aeo/settings`.
- **Expected:** The page loads (the group guard admits support) but every form control is disabled and neither **Save profile** nor **Run now** renders (`canManage = admin || sales`). A direct `PUT /api/v1/aeo/profile` as support returns 403.
- **Automation:** blocked — no sales/support login helper; elevated roles only come from an admin-authenticated `POST /users`.

### TC-AEO-008 — Customer cannot reach the AEO section
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Customer account (public registration).
- **Steps:**
  1. Log in as the customer and inspect the sidebar.
  2. Navigate directly to `/aeo`, `/aeo/prompts`, `/aeo/citations`, `/aeo/settings`.
  3. Call `GET /api/v1/aeo/dashboard` with the customer token.
- **Expected:** No "AEO" nav entry. Each direct navigation is bounced by `ProtectedRoute`. The API call returns 403 — the group-level `RequireRole(admin, sales, support)` rejects `customer` on every AEO route, read or write.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` (customer session available via `registration.spec.ts`'s flow).

---

## 11.2 Prompts

### TC-AEO-009 — Prompt list carries per-window visibility
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one prompt with recorded answers in the last 30 days.
- **Steps:**
  1. Navigate to `/aeo/prompts`.
- **Expected:** `GET /api/v1/aeo/prompts?days=30&…` returns 200 with a bare array plus pagination meta (`total`, `page`, `per_page`). Each row shows the text, an active switch, `visibility` as a bar (`data-testid="visibility-bar-<id>"`), the mention/answer counts and the last run timestamp. Visibility is computed over the requested window only and is 0..100 with one decimal; a prompt with no scored answers shows 0, never `NaN`.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Arithmetic pinned by `internal/service/aeo_service_test.go`.

### TC-AEO-010 — Add prompts
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in on `/aeo/prompts`.
- **Steps:**
  1. Open **Add prompts**, enter two distinct questions, submit.
- **Expected:** `POST /api/v1/aeo/prompts` returns 201 with both created prompts (`is_active` true, `created_by_id` set to the caller). The create is all-or-nothing inside one transaction. The list refetches and both rows appear.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-011 — Duplicate prompt text is rejected case-insensitively
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A live prompt "Which CRM is best for startups?" exists.
- **Steps:**
  1. `POST /api/v1/aeo/prompts` with `["WHICH crm IS best FOR startups?"]`.
  2. Repeat with a batch of two prompts where only the second collides.
- **Expected:** Both return 409 `CONFLICT` (`ErrDuplicatePrompt`) and **nothing** is saved — in the batch case the first prompt is rolled back with the second. Uniqueness is a service-level `LOWER(text)` pre-check over live rows only; there is deliberately no unique index on `aeo_prompts.text`, so the text of a soft-deleted prompt is reusable.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-012 — Whitespace-only prompt text is a 400, not a 500
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin token.
- **Steps:**
  1. `POST /api/v1/aeo/prompts` with `{"prompts":["   "]}`.
- **Expected:** 400 `VALIDATION_ERROR`. `dive,required` passes because the string is non-empty; the service trims it to nothing and returns `ErrAEOInvalidPrompt`, which the handler now maps explicitly.
- **Known issue:** Fixed 2026-08-11 — previously 500 `INTERNAL_ERROR`. Regression-tested by `internal/handler/aeo_handler_test.go` "TestCreatePrompts_BlankTextIs400".
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` (API-level assertion).

### TC-AEO-013 — Prompt length and batch-size ceilings
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin token.
- **Steps:**
  1. `POST /api/v1/aeo/prompts` with one 501-character prompt.
  2. `POST` with 26 prompts.
  3. `POST` with `{"prompts":[]}`.
- **Expected:** All three return 400 `VALIDATION_ERROR` from the binding (`min=1,max=25,dive,required,max=500`). The 500-rune ceiling mirrors the `varchar(500)` column, so a driver truncation error is never reachable.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-014 — The 100 active-prompt cap
- **Priority:** P1
- **Type:** validation
- **Preconditions:** 100 active prompts exist.
- **Steps:**
  1. `POST /api/v1/aeo/prompts` with one more.
  2. Deactivate one prompt, then re-activate it via `PUT /api/v1/aeo/prompts/:id`.
- **Expected:** Step 1 returns 400 `VALIDATION_ERROR` with "active prompt limit of 100 reached". Step 2's re-activation counts against the same cap and is refused the same way while the roster is full. The cap is a cost guard: 100 prompts × 6 engines is 600 provider calls per run.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Pinned by `internal/service/aeo_service_test.go`.

### TC-AEO-015 — Toggle a prompt active/inactive
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in on `/aeo/prompts` with one active prompt.
- **Steps:**
  1. Flip the row switch (`aria-label="Toggle <text>"`).
- **Expected:** `PUT /api/v1/aeo/prompts/:id` with `{"is_active":false}` returns 200. Only the supplied field changes — `text` omitted means "leave it alone". Inactive prompts are excluded from the next run but keep their historical answers and still appear in the list.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-016 — Delete a prompt (admin only)
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; one prompt with recorded answers.
- **Steps:**
  1. Press the row's delete action and confirm.
- **Expected:** `DELETE /api/v1/aeo/prompts/:id` returns 204 with no body. The delete is soft, so the recorded answers and their citations survive and historical dashboards do not change retroactively. The row leaves the list. Deleting a non-existent id returns 404 `NOT_FOUND`.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-017 — Sales can manage prompts but not delete them
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Sales user logged in.
- **Steps:**
  1. Create a prompt, toggle it, then attempt a delete.
- **Expected:** Create and update succeed (write guard is admin+sales). The delete button does not render (`canDelete = admin`), and a direct `DELETE /api/v1/aeo/prompts/:id` returns 403.
- **Automation:** blocked — no sales/support login helper.

### TC-AEO-018 — Prompt generation without an Anthropic key
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; backend started with `ANTHROPIC_API_KEY` empty; a saved profile.
- **Steps:**
  1. Open **Generate prompts** and submit.
- **Expected:** `POST /api/v1/aeo/prompts/generate` returns 503 with `error.code` `PROVIDER_NOT_CONFIGURED` (distinct from the run endpoint's `PROVIDERS_UNAVAILABLE`). The dialog surfaces the message and stays open. With no profile saved the same endpoint returns 409 instead.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-019 — Prompt generation happy path
- **Priority:** P2
- **Type:** functional
- **Preconditions:** `ANTHROPIC_API_KEY` set; a saved profile.
- **Steps:**
  1. Open **Generate prompts**, request 10, tick a subset, save.
- **Expected:** 200 with `{"prompts":[…]}` — suggestions are never stored, only the ticked ones are POSTed to `/aeo/prompts`. List decoration (numbering, bullets, quotes) is stripped and fragments shorter than 12 runes are discarded.
- **Automation:** blocked — needs a live Anthropic call; covered manually by `scripts/aeo_live_smoke.sh`.

### TC-AEO-020 — Answer transcript highlights brand mentions
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a prompt with at least one recorded answer that mentions the brand.
- **Steps:**
  1. Open the prompt's detail drawer on `/aeo/prompts`.
- **Expected:** `GET /api/v1/aeo/prompts/:id/answers` returns 200, newest first, each answer carrying its `citations` array. Each answer renders as `data-testid="answer-card-<id>"` with the provider, model and latency; every brand or alias occurrence inside `data-testid="answer-transcript"` is wrapped in `data-testid="brand-mention"`. Matching is case-insensitive and respects unicode word boundaries, so `Acme` inside `Acmerica` is **not** highlighted. A failed query renders as a card carrying its `error` string and no transcript.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Detection pinned by `internal/aeo/analysis_test.go`.

### TC-AEO-021 — Filter the transcript by run
- **Priority:** P2
- **Type:** functional
- **Preconditions:** A prompt with answers from at least two runs.
- **Steps:**
  1. Open the drawer, switch the run selector from **All runs** to a specific run.
- **Expected:** The request repeats with `run_id=<id>` and only that run's answers are listed; the total in the pagination meta narrows to match. Switching back to **All runs** drops the parameter.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-022 — Reporting window toggle on the prompt list
- **Priority:** P2
- **Type:** functional
- **Preconditions:** A prompt whose answers are older than 7 days.
- **Steps:**
  1. Switch the window toggle from 30 to 7 days.
- **Expected:** The list refetches with `days=7` and the prompt's visibility, answer and mention counts drop to 0 while the row itself stays. Only 7, 30 and 90 are offered; a hand-crafted `days=45` is ignored server-side and treated as 30.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

---

## 11.3 Runs

### TC-AEO-023 — Start a run
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; saved profile; ≥1 active prompt; one stub provider configured.
- **Steps:**
  1. Press **Run now** on `/aeo/settings`.
  2. Poll `GET /api/v1/aeo/runs`.
- **Expected:** `POST /api/v1/aeo/runs` returns **202** immediately with the run row: `status` `running`, `trigger` `manual`, `total_queries` = active prompts × configured providers, `triggered_by_id` = the caller. The response does not wait for the engine — the executor runs on a background context so cancelling the request cannot abort a run that is already writing answers. The run later settles to `completed`, `partial` or `failed` with `completed_at` and `failed_queries` filled in.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` (stub provider only — see the cost constraint).

### TC-AEO-024 — A second run is refused while one is running
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A run in `running`.
- **Steps:**
  1. `POST /api/v1/aeo/runs`.
- **Expected:** 409 `CONFLICT` with "an AEO run is already in progress". The scheduler hits the same guard and logs the skip instead of failing. The UI surfaces the message and leaves the existing run alone.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-025 — Run precondition ordering
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Empty `aeo_profiles`; no provider keys; no active prompts.
- **Steps:**
  1. `POST /api/v1/aeo/runs` and record the status.
  2. Save a profile, repeat.
  3. Configure one provider, repeat.
- **Expected:** 409 (`ErrProfileNotConfigured`), then 503 `PROVIDERS_UNAVAILABLE`, then 404 `NOT_FOUND` ("no active AEO prompts"). The order is fixed so the caller always gets the most actionable reason first.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-026 — A run stranded by a restart does not brick the module
- **Priority:** P0
- **Type:** regression
- **Preconditions:** A row in `aeo_runs` with `status='running'` and no `completed_at` — reproduce by killing the backend mid-run, or by inserting one directly.
- **Steps:**
  1. Restart the backend and read its startup log.
  2. `POST /api/v1/aeo/runs`.
- **Expected:** Startup reconciliation flips every `running` row to `failed` with `completed_at` stamped and logs "Recovered AEO runs left behind by a previous process"; the subsequent run starts normally. Without a restart the same row is swept lazily by the next `StartRun` once it is older than 6 h (`aeoRunStaleAfter`), so a crash cannot block the daily scheduled run either.
- **Known issue:** Fixed 2026-08-11. Before that there was no reconciliation anywhere: the executor is in-process and `finalize` never ran after a crash, so a single stranded row made every later run answer 409 and every scheduled run log "skipped" **forever**, until an operator ran `UPDATE aeo_runs SET status='failed'` by hand. Regression-tested by `internal/service/aeo_service_test.go` "TestReconcileRunningRuns" / "TestStartRun_SweepsRunsStrandedByACrash" and `internal/repository/aeo_repository_test.go` "TestAEORepository_MarkStaleRunsFailed".
- **Automation:** planned — backend-level; a Playwright case can only assert the second half (run starts after restart).

### TC-AEO-027 — Concurrent run requests start exactly one run
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Saved profile, ≥1 active prompt, one stub provider, no run in flight.
- **Steps:**
  1. Fire eight `POST /api/v1/aeo/runs` requests simultaneously.
- **Expected:** One 202 and seven 409 `CONFLICT`; exactly one row is inserted into `aeo_runs`. The realistic trigger is a manual run landing on the scheduled hour.
- **Known issue:** Fixed 2026-08-11 — the guard was a non-atomic check-then-insert (`CountRunsByStatus` then `CreateRun` with nothing in between), so concurrent callers could all read zero and all fan out, doubling provider spend. Serialised in the service. This covers one API process, which is what the module assumes: a second replica would run its own scheduler and needs a database-level claim. Regression-tested by `internal/service/aeo_service_test.go` "TestStartRun_ConcurrentCallsStartExactlyOneRun" (fails without the guard, verified).
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` via `request` fan-out.

### TC-AEO-028 — Sorting the run list by trigger
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Runs of both triggers exist; backend on **MySQL**, not SQLite.
- **Steps:**
  1. `GET /api/v1/aeo/runs?sort_by=trigger&sort_order=desc`.
  2. Repeat for every allowlisted column: `id`, `status`, `trigger`, `started_at`, `completed_at`, `created_at`.
  3. `GET /api/v1/aeo/runs?sort_by=started_at;DROP TABLE aeo_runs`.
- **Expected:** Steps 1–2 return 200, sorted, with `id` as the tie-breaker. Step 3 returns 400 `VALIDATION_ERROR` — the column is checked against an allowlist before any `ORDER BY` is built.
- **Known issue:** Fixed 2026-08-11 — `trigger` is a MySQL reserved word and the order clause interpolated it unquoted, so `ORDER BY trigger desc` was error 1064 on MySQL/MariaDB and a 500 for the client, while SQLite (the whole Go suite) accepted it. Identifiers are backtick-quoted now. **Must be exercised against MySQL**: a green SQLite run proves nothing here. Regression-tested by `internal/repository/aeo_repository_test.go` "TestAEORepository_ListRuns_SortByTriggerIsQuoted".
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts` (the e2e stack runs MySQL, so this is the layer that can catch it).

### TC-AEO-029 — A failing provider degrades the run, it does not abort it
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Two stub providers, one returning 500 for every request.
- **Steps:**
  1. Start a run over one active prompt and wait for it to settle.
  2. Open the prompt transcript.
- **Expected:** Two answer rows: one healthy, one with `error` set, empty `answer_text` and `brand_mentioned` false. The run ends `partial` with `failed_queries` 1 of `total_queries` 2. All-failed ends `failed`; none-failed ends `completed`. Failed answers are excluded from every visibility denominator.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Pinned by `internal/aeo/engine_test.go`.

### TC-AEO-030 — Run list pagination and detail
- **Priority:** P2
- **Type:** functional
- **Preconditions:** More than 20 runs recorded.
- **Steps:**
  1. `GET /api/v1/aeo/runs` then `?offset=20&limit=20`.
  2. `GET /api/v1/aeo/runs/:id` for a known run, then for id 999999.
- **Expected:** Both pages return 200 with `meta.total`, `meta.page` and `meta.per_page`; the default window is offset 0 / limit 20 and `limit` is capped at 100 (`utils.ParseOffsetLimit`; `?limit=0` must not 500). The detail call returns the run, and the unknown id returns 404 `NOT_FOUND`.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

---

## 11.4 Dashboard

### TC-AEO-031 — Dashboard states: loading, empty, error, populated
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/aeo` with no recorded answers.
  2. Repeat after a run has recorded answers.
  3. Repeat with the backend stopped.
- **Expected:** A `CircularProgress` labelled "Loading AEO dashboard" while in flight; then "No AEO data yet" for the empty range; then the visibility gauge (`data-testid="aeo-visibility-gauge"`), the provider timeline (`aeo-provider-timeline`), the share-of-voice table (`aeo-share-of-voice`) and the competitor timeline (`aeo-competitor-timeline`); and with the backend down, `Alert severity="error"` reading "Failed to load the AEO dashboard. Please try again." — never a blank page.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Unit-pinned by `gocrm-ui/src/pages/aeo/AEODashboard.test.tsx`.

### TC-AEO-032 — The timeline has no gaps
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Answers recorded on some but not all days of the window.
- **Steps:**
  1. `GET /api/v1/aeo/dashboard?days=7` and read `timeline`.
- **Expected:** Exactly 7 entries, ascending by `day` (`YYYY-MM-DD`, UTC). Days with no answers are present with `overall: 0` and an empty `by_provider`, so the chart draws a continuous line instead of interpolating over a hole. Bucketing happens in Go, never with `DATE()`/`strftime` — the same statement has to run on MySQL and SQLite.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-033 — Share of voice
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A window whose answers mention the brand and at least one competitor.
- **Steps:**
  1. `GET /api/v1/aeo/dashboard?days=30` and read `share_of_voice`.
- **Expected:** The brand entry comes first with `is_brand: true`, competitors follow in profile order. `share` sums to ~100 across the list and every percentage is 0..100 with one decimal. With zero scored answers every value is 0 — no division by zero, no `NaN` on the wire.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Arithmetic pinned by `internal/service/aeo_service_test.go`.

### TC-AEO-034 — Window bounds
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin token.
- **Steps:**
  1. `GET /api/v1/aeo/dashboard?days=90`, then `?days=365`, then `?days=abc`.
- **Expected:** 200 in all three cases. 90 is honoured; 365 and `abc` fall back to the 30-day default because only 7/30/90 are accepted, and the service clamps any range wider than 90 days independently. `from` is inclusive, `to` exclusive and set to the start of tomorrow UTC, so today's answers always count whatever the caller's timezone.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

---

## 11.5 Citations

### TC-AEO-035 — Citation report
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Answers recorded that cite an owned domain and a competitor domain.
- **Steps:**
  1. Navigate to `/aeo/citations`.
- **Expected:** `GET /api/v1/aeo/citations?days=30` returns 200. Two bar charts (owned-domain citation rate by company, brand-mention rate by company) and a `top_domains` table of at most 20 rows sorted by citations descending. `by_company` starts with the brand (`is_brand: true`) followed by each competitor in profile order. All rates are 0..100 with one decimal. With no citations the page shows "No AEO data yet" rather than empty axes.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Unit-pinned by `gocrm-ui/src/pages/aeo/AEOCitations.test.tsx`.

### TC-AEO-036 — Domain attribution
- **Priority:** P2
- **Type:** functional
- **Preconditions:** A profile owning `acme.com`; answers citing `https://WWW.Acme.com/blog`, `https://acme.com:8443/x`, `https://globex.com/a` and a malformed URL.
- **Steps:**
  1. Run, then read `top_domains`.
- **Expected:** The first two collapse into one `acme.com` row with `is_owned: true` — the host is lowercased, `www.` stripped and the port dropped. `globex.com` is attributed to the competitor by name. The malformed URL normalises to `""` and is not counted. Trailing punctuation is trimmed off URLs extracted from prose.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`. Pinned by `internal/aeo/analysis_test.go`.

---

## 11.6 Cross-cutting

### TC-AEO-037 — Unauthenticated access
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** No token.
- **Steps:**
  1. Call each of the 14 AEO routes with no `Authorization` header, then with a garbage Bearer token.
- **Expected:** 401 in every case, in the standard envelope. No route is reachable anonymously; there is no equivalent of the unguarded `/dashboard/stats` here.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-038 — API-key authentication works on AEO routes
- **Priority:** P2
- **Type:** functional
- **Preconditions:** An API key belonging to an admin.
- **Steps:**
  1. `GET /api/v1/aeo/providers` with the `ApiKey` header instead of a Bearer token.
- **Expected:** 200. Both security schemes are annotated on every AEO handler and both are accepted; a key whose owner is deactivated or erased, or one past `expires_at`, is rejected at auth time with 401.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-039 — Route coexistence: `/prompts/generate` vs `/prompts/:id`
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin token; a prompt whose id is known.
- **Steps:**
  1. `POST /api/v1/aeo/prompts/generate`.
  2. `PUT /api/v1/aeo/prompts/<id>`.
  3. `GET /api/v1/aeo/prompts/<id>/answers`.
- **Expected:** Each reaches its own handler — the static segment never shadows the parameterised one and vice versa. Gin panics at startup on a bad mix, so the smoke test in `internal/handler/routes_test.go` is what keeps this honest; it was extended for these routes.
- **Automation:** automated (backend) — `internal/handler/routes_test.go`; planned at E2E level in `gocrm-ui/e2e/tests/aeo.spec.ts`.

### TC-AEO-040 — Rate limiting applies to AEO traffic
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin token; backend started without `DISABLE_RATE_LIMIT`.
- **Steps:**
  1. Issue 130 `GET /api/v1/aeo/providers` calls inside a minute.
- **Expected:** The moderate tier (120/min, burst 30) starts answering 429. `DISABLE_RATE_LIMIT=true` does **not** lift this — it only bypasses the strict `/auth` tier — so a fast E2E suite can 429 itself here exactly as it can elsewhere.
- **Automation:** planned — `gocrm-ui/e2e/tests/aeo.spec.ts`.

---

## Coverage summary

| Section | Cases | P0 | P1 | P2 |
|---|---|---|---|---|
| 11.1 Settings and brand profile | 8 | 2 | 5 | 1 |
| 11.2 Prompts | 14 | 1 | 9 | 4 |
| 11.3 Runs | 8 | 1 | 5 | 2 |
| 11.4 Dashboard | 4 | 0 | 2 | 2 |
| 11.5 Citations | 2 | 0 | 1 | 1 |
| 11.6 Cross-cutting | 4 | 1 | 1 | 2 |
| **Total** | **40** | **5** | **23** | **12** |

Automation status: 1 automated (backend route smoke), 36 planned, 3 blocked (two on the missing
sales/support login helper, one on a live Anthropic call).
