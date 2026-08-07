# Settings — Test Cases

Test cases for the Settings section: API key management (FEATURES.md section 8, extended by
section 10b "API keys") and Configuration Settings (FEATURES.md section 9). Every Expected field
below records what the current build does, including the places where it is defective or
surprising; those carry a "Known issue" line. The two screens are in very different shape:
`ConfigurationSettings.tsx` is a complete, working page, while `APIKeys.tsx` is a heading-only stub,
so the API key cases are written against the backend behaviour that already exists and are marked
blocked until the page is built.

## Sources

- `gocrm-ui/src/pages/settings/APIKeys.tsx`, `gocrm-ui/src/pages/settings/ConfigurationSettings.tsx`
- `gocrm-ui/src/api/endpoints/apikeys.ts`, `gocrm-ui/src/api/endpoints/configurations.ts`,
  `gocrm-ui/src/api/endpoints/apikeys.test.ts`, `gocrm-ui/src/api/client.ts`
- `gocrm-ui/src/contexts/ConfigurationContext.tsx`, `gocrm-ui/src/components/ConfigurationOverview.tsx`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `internal/handler/apikey_handler.go`, `internal/handler/configuration_handler.go`,
  `internal/handler/routes.go` (`SetupAPIKeyRoutes`, `SetupConfigurationRoutes`), `cmd/main.go`
- `internal/service/apikey_service.go`, `internal/service/configuration_service.go`,
  `internal/service/auth_service.go` (`ValidateAPIKey`), `internal/middleware/auth.go` (`RequireRole`)
- `internal/models/configuration.go` (`SetValue`, `IsValidValue`, `DefaultConfigurations`),
  `internal/repository/configuration_repository.go`
- `internal/handler/apikey_handler_test.go`, `tests/apikey_integration_test.go`,
  `tests/configuration_integration_test.go`
- `gocrm-ui/e2e/screenshots/08-settings.spec.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`,
  `gocrm-ui/e2e/helpers/admin-auth.ts`, `gocrm-ui/e2e/screenshots/helpers/login.ts`
- `docs/FEATURES.md` sections 8, 9, 10b and the Gap Summary (G13, G14, G34);
  `docs/ROADMAP.md`; `docs/SCREENSHOTS.md` (`docs/screenshots/settings/`)

## Constraints

- **Roles other than admin need a helper that does not exist yet.** `gocrm-ui/e2e/helpers/` only
  contains `admin-auth.ts`, which logs in the seeded admin. A customer account can be produced
  through `/register` (public registration always creates a `customer`), but sales and support
  accounts must be created by an admin through the Users page using `generateUserData()` from
  `e2e/fixtures/admin-user.ts` and then logged in through the login form. Cases needing that say so.
- **Configurations are global, shared, mutable state.** There are only seven rows, all seeded by
  `InitializeDefaults()`, and every session of the application reads the same ones. Any spec that
  writes a configuration must run serially and restore the value it changed (POST
  `/configurations/{key}/reset` is the cheapest restore, but note that `tickets.auto_assign_support`
  ships with `value=true` and `default_value=false`, so "reset" is not "restore" for that key —
  see TC-SET-026).
- **API key names in tests must come from a faker generator**, not a literal. Add
  `generateAPIKeyData()` to `e2e/fixtures/admin-user.ts` alongside the existing generators, building
  the name the same way they build emails (a `key_` prefix, `Date.now()` and
  `faker.string.alphanumeric(6)`), rather than hardcoding one.
- Nothing in this area performs GDPR erasure. Revoking an API key sets `is_active=false` and keeps
  the row (`APIKeyHandler.Revoke`); there is no hard delete route, and `deleteAPIKey()` in the
  endpoint module issues the identical `DELETE /api-keys/{id}` as `revokeAPIKey()` — a duplicate,
  not a distinct operation.
- Backend base URL for API-level assertions: `/api/v1` (`API_PREFIX`).

---

## Page shell and navigation

### TC-SET-001 — Confirm the API Keys page renders a heading and nothing else
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in (`ensureAdminLoggedIn`).
- **Steps:**
  1. Navigate to `/settings/api-keys`.
  2. Wait for the page to settle.
  3. Inspect the page for any table, button or form control.
- **Expected:** The route resolves and renders `<h1>API Keys</h1>` only. There is no "Generate key"
  button, no table, no dialog; the component body is a single heading
  (`gocrm-ui/src/pages/settings/APIKeys.tsx`). No request to `/api-keys` is issued — the endpoint
  module `apiKeysApi` has no caller in `src/`.
- **Known issue:** FEATURES.md rows 8.1–8.3 describe generate/list/revoke as user-facing features;
  the backend implements all of them but the page is a stub, so none is reachable from the UI. The
  capture `docs/screenshots/settings/02-api-keys.png` records this state.
- **Automation:** automated — `gocrm-ui/e2e/screenshots/08-settings.spec.ts` "api keys page"

### TC-SET-002 — Confirm every authenticated role can reach the API Keys page
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** A customer account registered through `/register` using `generateUserData()`
  (the public endpoint forces the `customer` role).
- **Steps:**
  1. Log in as the customer.
  2. Expand the "Settings" group in the left navigation.
  3. Click "API Keys".
- **Expected:** The "API Keys" nav item is visible — the item carries no `roles` restriction in
  `MainLayout.tsx`, unlike "Configuration" which is `roles: ['admin']`. The route
  `settings/api-keys` sits outside the admin-guarded pathless route in `routes/index.tsx`, so the
  page loads and shows the heading. This matches the backend: `SetupAPIKeyRoutes` installs no
  `RequireRole`, so any authenticated role may manage its own keys.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-apikeys.spec.ts` (new; closes part of G13)

---

## 8.1 Generate API key

### TC-SET-003 — Generate a key and show the plaintext value exactly once
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Seeded admin logged in; key name from `generateAPIKeyData()`.
- **Steps:**
  1. Go to `/settings/api-keys`.
  2. Click the generate control, enter the generated name, submit.
  3. Note the plaintext key shown in the result dialog.
  4. Close the dialog and reload the page.
- **Expected:** `POST /api-keys` returns **201** with `{ key, api_key }`; `key` begins with `gcrm_`
  followed by 64 hex characters (`apiKeyService.Generate`), `api_key.prefix` equals the first 8 hex
  characters of the key **without** the `gcrm_` prefix, and `api_key.key_hash` is stripped by
  `transformAPIKeyFromBackend` before it reaches component state. After the reload the plaintext
  value is nowhere on the page — only the prefix — because only the HMAC hash is stored. Today the
  page offers no control to drive, so the flow cannot be executed from the UI at all.
- **Known issue:** FEATURES.md 8.1 — "No E2E test"; the underlying cause is that the page is a stub.
- **Automation:** blocked — the API Keys page is a heading-only stub, so there is no create control;
  backend behaviour is covered by `internal/handler/apikey_handler_test.go` `TestCreate_Success` and
  `tests/apikey_integration_test.go` `TestCreateAPIKey`

### TC-SET-004 — Reject a key name shorter than three characters
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Open the generate dialog.
  2. Enter `ab` as the name and submit.
- **Expected:** `POST /api-keys` returns **400**; the binding tag is
  `binding:"required,min=3,max=100"` on `CreateAPIKeyRequest.Name`, and the bind error is routed
  through `gin.ErrorTypeBind` to the error-handler middleware. A name of 101 characters is rejected
  the same way. No key row is created.
- **Automation:** blocked — no create form exists; `apikey_handler_test.go` `TestUpdate_InvalidName`
  covers the equivalent rule on update

### TC-SET-005 — Generate a key with a future expiry
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Open the generate dialog, enter a generated name and an expiry one day in the future.
  2. Submit and inspect the created key in the list.
- **Expected:** `POST /api-keys` with `expires_at` as an RFC3339 timestamp (for example
  `2026-12-31T23:59:59Z`) returns **201** and the stored record carries that `expires_at`. Omitting
  the field leaves `expires_at` null and the key never expires. Expiry cannot be lifted afterwards:
  `PUT /api-keys/{id}` only accepts `name` and `is_active`.
- **Automation:** blocked — no create form; covered by `apikey_handler_test.go`
  `TestCreate_WithFutureExpiresAt` and `tests/apikey_integration_test.go` `TestCreateAPIKeyWithExpiry`

### TC-SET-006 — Reject a past or malformed expiry
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Submit the generate form with `expires_at` set to a timestamp in the past.
  2. Repeat with a non-RFC3339 string such as `31-12-2026`.
- **Expected:** Both are **400** with distinct messages from `APIKeyHandler.Create`: `expires_at must
  be in the future` and `expires_at must be an RFC3339 timestamp`. No key is minted in either case —
  the handler deliberately refuses to issue a credential that could never authenticate.
- **Automation:** blocked — no create form; covered by `apikey_handler_test.go`
  `TestCreate_PastExpiresAt` / `TestCreate_MalformedExpiresAt` and
  `tests/apikey_integration_test.go` `TestCreateAPIKeyWithPastExpiry`

---

## 8.2 List API keys

### TC-SET-007 — List the caller's own keys with prefix, state and last-used
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in with at least one key created by the test.
- **Steps:**
  1. Go to `/settings/api-keys`.
  2. Read the table.
- **Expected:** `GET /api-keys` returns **200** with a bare `APIKey[]` (the axios interceptor unwraps
  the `{success,data}` envelope, and `apiKeysApi.getAPIKeys` synthesises the pagination shape
  client-side — the backend performs no pagination, filtering or sorting here). The list contains the
  caller's keys only: `APIKeyHandler.List` passes `c.GetUint("user_id")` straight to the service, so
  there is no admin-wide view and the `user_id` / `is_active` fields of `APIKeyFilters` are ignored by
  the server. Revoked keys are included, distinguishable by `is_active=false`. Each row shows the
  8-character prefix, never the plaintext key, plus `last_used_at`.
- **Automation:** blocked — no table in the page; covered by `apikey_handler_test.go`
  `TestList_Success` and `tests/apikey_integration_test.go` `TestListAPIKeys`

### TC-SET-008 — Refuse to read another user's key
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Two accounts, each owning one key: the seeded admin and a support user created
  through the admin Users page with `generateUserData()`.
- **Steps:**
  1. As the support user, request `GET /api-keys/{id}` for the admin's key id.
  2. Repeat as an admin against the support user's key id.
- **Expected:** **403** in *both* directions with `You are not authorized to view this API key`.
  Ownership is enforced in `apiKeyService.ownedKey`, and there is no admin override — an admin is
  as forbidden as anyone else. An id that does not exist is **404** `API key not found`; a
  non-numeric id is **400** `Invalid API key ID`.
- **Automation:** blocked — no UI surface and no role-login helper; covered by
  `apikey_handler_test.go` `TestGet_Forbidden` / `TestGet_NotFound` / `TestGet_InvalidID` and
  `tests/apikey_integration_test.go` `TestGetAPIKey`

### TC-SET-009 — Confirm the stored hash never reaches the browser
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Seeded admin with one key.
- **Steps:**
  1. Load `/settings/api-keys` and capture the `/api-keys` response body.
  2. Search the rendered DOM for the hash value.
- **Expected:** Whatever the backend serialises, `transformAPIKeyFromBackend` strips `key_hash`
  before the object is returned to callers, on the list, get and create paths alike, so no hash is
  ever placed in component state or rendered.
- **Automation:** blocked — no page consumes the endpoint; the transform itself is pinned by the
  Vitest unit test `gocrm-ui/src/api/endpoints/apikeys.test.ts` "lists keys from the bare array at
  /api-keys and strips key_hash"

---

## 8.3 Revoke and edit API keys

### TC-SET-010 — Revoke a key and confirm it stops authenticating
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Seeded admin logged in; one key created by the test, plaintext retained.
- **Steps:**
  1. Call an authenticated endpoint with `Authorization: ApiKey <plaintext>` and confirm it succeeds.
  2. In the UI, revoke the key.
  3. Repeat the call from step 1.
- **Expected:** `DELETE /api-keys/{id}` returns **200** with
  `{"message":"API key revoked successfully"}`. The row is **not** deleted — `Revoke` marks it
  inactive — so it stays in the list with `is_active=false`. The subsequent API-key call is rejected
  at authentication (`ValidateAPIKey` returns "API key has been revoked"), surfacing as **401**.
  Revoking someone else's key is **403**; an unknown id is **404**.
- **Automation:** blocked — no revoke control in the page; covered by `apikey_handler_test.go`
  `TestRevoke_Success` / `TestRevoke_Forbidden` / `TestRevoke_NotFound` and
  `tests/apikey_integration_test.go` `TestRevokeAPIKey`

### TC-SET-011 — Rename a key, and reject an update that changes nothing
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Seeded admin logged in with one key created by the test.
- **Steps:**
  1. Edit the key's name to a new generated value and save.
  2. Submit an update whose body contains neither `name` nor `is_active`.
- **Expected:** The rename returns **200** with the updated record. The empty update returns **400**
  `At least one of name or is_active must be provided` — the request struct uses pointers precisely
  so that an omitted `is_active` is not read as "deactivate", and a body with no updatable field is
  refused rather than reported as a successful no-op.
- **Automation:** blocked — no edit control; covered by `apikey_handler_test.go` `TestUpdate_Rename`
  and `TestUpdate_EmptyBody`

### TC-SET-012 — Reactivate a revoked key
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Seeded admin logged in; a key created and then revoked by the test, plaintext
  retained.
- **Steps:**
  1. Send `PUT /api-keys/{id}` with `{"is_active": true}`.
  2. Retry the plaintext key against an authenticated endpoint.
- **Expected:** **200**, and the *same plaintext key starts working again* — revocation is an
  owner-controlled flag, not a tombstone. Expiry is unaffected by this endpoint: an expired key stays
  unusable however `is_active` is set, because expiry is checked separately in `ValidateAPIKey`.
  Anyone wanting an irreversible kill must create a new key and abandon the old one.
- **Automation:** blocked — no edit control; covered by `apikey_handler_test.go`
  `TestUpdate_Reactivate` and `tests/apikey_integration_test.go` `TestUpdateAPIKey`

---

## 8.4 Keys die with their owner (and expiry, section 10b)

### TC-SET-013 — Reject a key whose owner has been deactivated
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A support user created with `generateUserData()`, owning one key whose plaintext
  is retained.
- **Steps:**
  1. Confirm the key authenticates.
  2. As admin, deactivate that user (`is_active=false`) from the Users page.
  3. Retry the key.
- **Expected:** **401** — `ValidateAPIKey` loads the owner through the scoped repository lookup and
  refuses the key with "API key owner is not active". Deactivation is the reversible alternative to
  erasure and touches no personal data; reactivating the user makes the key work again.
- **Automation:** blocked — no UI surface for API keys and no role-login helper; covered by
  `internal/service/auth_service_test.go` `TestAuthService_ValidateAPIKey` subtest "key of a
  deactivated user is rejected" and `test/integration/erasure_test.go`
  `TestAPIKeyOfADeactivatedUserCannotAuthenticate`

### TC-SET-014 — Reject a key whose owner has been erased
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A support user created with `generateUserData()` (created by the test, so it is
  safe to delete), owning one key whose plaintext is retained.
- **Steps:**
  1. Confirm the key authenticates.
  2. As admin, delete that user from the Users page.
  3. Retry the key.
- **Expected:** **401**. Erasure purges API keys inside the same transaction, so the key normally no
  longer exists; the owner lookup in `ValidateAPIKey` is the second line of defence and rejects any
  key that survived (restored from a backup, written by an older release). The deletion is
  irreversible GDPR erasure — only ever apply it to the account the test created.
- **Automation:** blocked — needs an API-key fixture the UI cannot create; covered by
  `test/integration/erasure_test.go`
  `TestUserErasureDestroysCredentialsThatWouldOutliveTheAccount` and
  `TestAPIKeyOfAnErasedUserCannotAuthenticate`

### TC-SET-015 — Reject an expired key at authentication time
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A key created with an `expires_at` just far enough in the future to be accepted,
  then allowed to lapse (or seeded directly).
- **Steps:**
  1. Use the key before the expiry instant.
  2. Use it again after.
- **Expected:** Success first, then **401** — `ValidateAPIKey` checks
  `apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now())` before anything else about the
  owner. The key row remains listed with `is_active=true`; expiry and revocation are independent
  states, and nothing in the API can extend an expiry.
- **Automation:** blocked — waiting out a real expiry is not viable in an E2E run and the API
  refuses a past `expires_at` at creation; covered by `tests/apikey_integration_test.go`
  `TestExpiredAPIKeyIsRejectedAtAuth`

---

## 9.1 View configurations

### TC-SET-016 — View all configurations as admin, grouped into category tabs
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Navigate to `/settings/configuration`.
  2. Wait for the "General (n)" tab and the first table row.
  3. Step through the tabs.
- **Expected:** The page issues `GET /configurations` (admin-only) and renders one table per
  category with columns Key, Description, Type, Current Value, Default Value, System, Read-Only,
  Actions. With the seeded defaults there are seven entries: General (1) `general.company_name`,
  UI & Theme (1) `ui.theme.primary_color`, Security (1) `security.session_timeout_hours`, Leads (3)
  `leads.conversion.{allowed_statuses,require_notes,auto_assign_owner}`, Tickets (1)
  `tickets.auto_assign_support`. A warning banner sits above the tabs. If the request fails the page
  shows the snackbar "Failed to load configurations" and an empty shell.
- **Automation:** automated — `gocrm-ui/e2e/screenshots/08-settings.spec.ts` "configuration settings
  page"

### TC-SET-017 — Show the empty-state text for categories with no entries
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in, default configuration set.
- **Steps:**
  1. Open `/settings/configuration`.
  2. Select the "Customers", "Tasks" and "Integration" tabs in turn.
- **Expected:** Each shows `No configurations found for <label>` and no table. The tab labels read
  `Customers (0)`, `Tasks (0)`, `Integration (0)` — the counts come from the client-side filter
  `getConfigurationsByCategory`, not from a server query, so all eight category tabs exist
  regardless of what is stored.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (new; closes G14)

### TC-SET-018 — Render each value according to its declared type
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in, default configuration set.
- **Steps:**
  1. Open `/settings/configuration`.
  2. Inspect the Current Value cell on the Leads and Tickets tabs.
- **Expected:** `boolean` renders as a chip labelled `True` (green) or `False`; `array` renders one
  chip per element, so `leads.conversion.allowed_statuses` shows `qualified` and `contacted`;
  `string` and `integer` render as plain text; `json` renders monospaced. The Key cell is monospaced,
  `is_system` rows carry a blue "System" chip, and `is_read_only` rows would carry a warning
  "Read-Only" chip — no seeded row sets that flag, so the chip is never seen with default data.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

---

## 9.2 Update configuration

### TC-SET-019 — Update a string configuration and confirm it persists
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in. Spec runs serially and restores the original value in
  teardown.
- **Steps:**
  1. Open `/settings/configuration`, General tab.
  2. Click the edit (pencil) action on `general.company_name`.
  3. Replace the value in the text field with a faker-generated company name and click "Save".
  4. Reload the page.
  5. Restore the original value the same way.
- **Expected:** `PUT /configurations/general.company_name` with `{"value":"<new name>"}` returns
  **200** with the updated entry; the snackbar reads `Configuration updated successfully`, the dialog
  closes, the page reloads its data via `GET /configurations`, and the table cell shows the new
  value. After a full page reload the value is still the new one.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-020 — Toggle a boolean configuration through the switch
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in; spec restores the original value.
- **Steps:**
  1. Tickets tab, edit `tickets.auto_assign_support`.
  2. Flip the switch in the dialog and save.
- **Expected:** The dialog renders a MUI `Switch` (its label text follows the state, "True"/"False").
  Saving sends a real JSON boolean, so `Configuration.SetValue` stores `"true"`/`"false"` and the
  Current Value chip changes colour. A `false` value is accepted, not mistaken for a missing field:
  `SetConfigurationRequest.Value` is `json.RawMessage`, so `binding:"required"` only rejects a
  genuinely absent key.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-021 — Edit an array configuration through the constrained multi-select
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in; spec restores the original value
  (`["qualified", "contacted"]`, which is **not** the stored default).
- **Steps:**
  1. Leads tab, edit `leads.conversion.allowed_statuses`.
  2. Open the "Select Values" multi-select and read the options.
  3. Deselect `contacted`, save.
- **Expected:** The dialog shows `Valid values: ["new", "contacted", "qualified", "converted",
  "lost"]` and a multi-select offering exactly those five options, because the entry's type is
  `array` and `valid_values` parses to a string array. Saving sends a JSON array; the service checks
  every element against the allowlist (`Configuration.IsValidValue`) before storing. The result is
  **200** and the Current Value cell drops the `contacted` chip.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-022 — Reject an integer value outside the allowlist
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Security tab, edit `security.session_timeout_hours`.
  2. Type `25` into the number field and click "Save".
- **Expected:** `PUT /configurations/security.session_timeout_hours` returns **400** with
  `Invalid value for configuration` (`ErrConfigurationInvalidValue`, raised by `IsValidValue`
  against `[1, 8, 24, 48, 72, 168]`). The dialog stays open and the snackbar shows the page's own
  generic text `Failed to update configuration`. The stored value is unchanged; `24` saves fine.
- **Known issue:** the edit dialog switches on `type` before `valid_values`
  (`ConfigurationSettings.tsx` `renderEditField`), so an *integer* entry with an allowlist gets a
  free-form number field while a *string* entry with an allowlist gets a dropdown. The constraint is
  only visible as the "Valid values:" line of text, and the rejection reason from the API is
  discarded in favour of a generic message (`handleSaveEdit` ignores the error body).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-023 — Clear the integer field and observe the null rejection
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Security tab, edit `security.session_timeout_hours`.
  2. Select the field contents and delete them, leaving it empty.
  3. Click "Save".
- **Expected:** The number field's `onChange` runs `parseInt('', 10)`, which is `NaN`; `NaN`
  serialises to JSON `null`, so the request body is `{"value":null}` and the handler answers **400**
  `Configuration value cannot be null` (the explicit null check in `ConfigurationHandler.Set`, since
  no configuration type can hold a null). The snackbar again shows the generic
  `Failed to update configuration` and the value is untouched.
- **Known issue:** the dialog performs no client-side validation before sending, so an empty numeric
  field produces a server round-trip and an unhelpful message rather than an inline field error.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-024 — Confirm values are type-checked, never coerced
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin bearer token (take it from the logged-in session and drive this through
  Playwright's `request` fixture; the dialog cannot produce a wrong-typed value for these keys).
- **Steps:**
  1. `PUT /configurations/leads.conversion.require_notes` with `{"value":"yes"}`.
  2. `PUT /configurations/security.session_timeout_hours` with `{"value":24.5}`.
  3. `PUT /configurations/general.company_name` with `{"value":42}`.
  4. Re-read each key with `GET /configurations/{key}`.
- **Expected:** All three are **400** `Invalid value for configuration`, and every stored value is
  unchanged. `Configuration.SetValue` returns an error on a type mismatch instead of coercing —
  a non-boolean used to become `"false"` and a non-string `""`, which answered the caller with a
  success and a corrupted value. An integral float such as `24.0` is still accepted for an integer
  entry; `24.5` is not (`asInteger` refuses to truncate). Correctly typed values still work.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (API-level assertions
  alongside the UI ones); backend coverage in `tests/configuration_integration_test.go`
  `TestSetConfiguration_TypeMismatch` and `TestSetConfiguration_CorrectTypesStillAccepted`

### TC-SET-025 — Cancel an edit without writing
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Edit `general.company_name`, type a different value.
  2. Click "Cancel".
  3. Reopen the same entry.
- **Expected:** No request is issued, no snackbar appears, and the table still shows the original
  value. Reopening the dialog shows the stored value again, because `handleEdit` re-seeds
  `editValue` from the configuration each time it opens.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

---

## 9.3 Reset configuration

### TC-SET-026 — Reset a configuration to its stored default
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in. Note the pre-state of `tickets.auto_assign_support`:
  `value=true`, `default_value=false`.
- **Steps:**
  1. Tickets tab, click the reset (circular arrow) action on `tickets.auto_assign_support`.
  2. Observe the row.
- **Expected:** `POST /configurations/tickets.auto_assign_support/reset` returns **200**; the
  snackbar reads `Configuration reset to default`, the list reloads and the Current Value chip flips
  from `True` to `False`. There is **no** confirmation dialog — a single click overwrites the current
  value with `default_value`.
- **Known issue:** the seeded row ships with a value that differs from its own default
  (`models.DefaultConfigurations()`), so "reset" visibly changes behaviour on a system nobody has
  edited, and the unconfirmed single click makes that easy to trigger by accident. A spec that resets
  this key must restore `true` afterwards, or later runs start from a different state.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-027 — Reject a reset for an unknown key
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin bearer token; driven through the `request` fixture (the UI only offers
  reset buttons for keys it just listed).
- **Steps:**
  1. `POST /configurations/does.not.exist/reset`.
  2. `PUT /configurations/does.not.exist` with `{"value":"x"}`.
- **Expected:** Both return **404** `Configuration not found`. The service classifies the miss once,
  in `configurationService.GetByKey`, wrapping it as `apperrors.ErrNotFound`; the handler maps that
  to 404 and anything else to 500, so a driver failure is not reported as an unknown key.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`; backend coverage in
  `tests/configuration_integration_test.go` `TestResetConfiguration_UnknownKey` and
  `TestSetConfiguration_UnknownKey`

### TC-SET-028 — Confirm a multi-key configuration write is atomic
- **Priority:** P2
- **Type:** functional
- **Preconditions:** None expressible through the UI.
- **Steps:**
  1. Trigger a write path that touches several configuration rows.
  2. Force a failure part-way.
  3. Verify no row changed.
- **Expected:** Undetermined. `BulkUpsert` (used only by `InitializeDefaults` at startup) runs inside
  `db.Transaction`, but no HTTP route writes more than one key, so there is nothing to exercise from
  a test session.
- **Known issue:** FEATURES.md 9.4 — the row previously cited
  `test/integration/configuration_transaction_test.go`, which does not exist; configuration
  transactionality is untested.
- **Automation:** blocked — no endpoint writes multiple configurations, so there is no user-facing
  behaviour to assert; would need a Go integration test against `BulkUpsert`

### TC-SET-029 — Confirm read-only entries cannot be edited or reset
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A configuration row with `is_read_only=true`. **None of the seven seeded rows
  sets this flag** (verified against `models.DefaultConfigurations()` and a live
  `GET /configurations`), so the fixture must be inserted directly.
- **Steps:**
  1. Insert a read-only configuration row.
  2. Open `/settings/configuration` and find it.
  3. Attempt to edit and to reset it, both from the UI and through the API.
- **Expected:** The row shows a warning "Read-Only" chip and both action `IconButton`s are
  `disabled`, so the UI cannot start either operation. At the API, `PUT /configurations/{key}` and
  `POST /configurations/{key}/reset` both return **400** `Configuration is read-only`
  (`ErrConfigurationReadOnly`, raised in `configurationService.Set` and `.Reset`).
- **Automation:** blocked — no seeded read-only configuration exists to drive the UI path; the API
  path is covered by `tests/configuration_integration_test.go` `TestSetConfiguration_ReadOnly` and
  `TestResetConfiguration_ReadOnly`, which create their own row

---

## Configuration access control

### TC-SET-030 — Keep non-admin roles out of the Configuration page
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer account from `/register`, and a sales and a support account created
  by the admin through the Users page with `generateUserData()`.
- **Steps:**
  1. Log in as each non-admin role in turn.
  2. Expand the "Settings" nav group.
  3. Navigate directly to `/settings/configuration`.
- **Expected:** The "Configuration" nav item is absent for all three (`roles: ['admin']` in
  `MainLayout.tsx`), while "Profile" and "API Keys" remain. The deep link redirects to
  `/unauthorized` — `settings/configuration` is a child of the pathless
  `ProtectedRoute requiredRole="admin"` route, and `ProtectedRoute` issues
  `<Navigate to="/unauthorized" replace />`. The Configuration Settings heading never renders and no
  `GET /configurations` request is made.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (needs a role-login
  helper for sales/support)

### TC-SET-031 — Confirm the API refuses configuration reads and writes for non-admins
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A bearer token for a non-admin role.
- **Steps:**
  1. `GET /configurations`, `GET /configurations/category/leads`,
     `GET /configurations/general.company_name`.
  2. `PUT /configurations/general.company_name`, `POST /configurations/general.company_name/reset`.
- **Expected:** All five return **403** with the body message `Insufficient permissions`. Each route
  carries `middleware.RequireRole(models.RoleAdmin)` in `SetupConfigurationRoutes`, and the guard
  aborts before the handler runs — so the handlers' own message (`Only admin users can view
  configurations`) is dead text that a client never sees. Worth asserting the exact string, because
  the two guards disagree and only one of them is reachable.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (API-level, via the
  `request` fixture)

### TC-SET-032 — Redirect an unauthenticated visitor away from Settings
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** No session (storage cleared).
- **Steps:**
  1. Navigate to `/settings/configuration`.
  2. Navigate to `/settings/api-keys`.
- **Expected:** Both redirect to `/login` with the attempted path kept in navigation state
  (`ProtectedRoute` — the outer guard runs before any role check, so an anonymous visitor lands on
  the login page rather than `/unauthorized`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-033 — Confirm the UI configuration endpoint is open to every authenticated role
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** A bearer token for a customer account.
- **Steps:**
  1. `GET /configurations/ui` as the customer.
  2. Compare with the same call as admin.
- **Expected:** **200** for both, with the same three entries: the whole `ui` category
  (`ui.theme.primary_color`), `general.company_name`, and a *synthetic*
  `leads.conversion.allowed_statuses` assembled in the handler. The synthetic entry is not a stored
  row — it comes back with `id: 0`, zero timestamps and an empty `default_value`, and its `category`
  is `leads` even though the whole `leads` category is otherwise admin-only. No `RequireRole` guards
  this route, deliberately: it is the one configuration read the frontend is allowed to make.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`; backend coverage in
  `tests/configuration_integration_test.go` `TestGetUIConfigurations`

### TC-SET-034 — Confirm configuration changes do not reach the leads pages
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Seeded admin logged in; a lead in status `contacted` created with
  `generateLeadData()`; `leads.conversion.allowed_statuses` at its seeded value
  `["qualified", "contacted"]`.
- **Steps:**
  1. Record the network requests made after login.
  2. Open the lead's detail page and look for the "Convert to Customer" action.
  3. Change `leads.conversion.allowed_statuses` in the Configuration page, then revisit the lead.
- **Expected:** No `GET /configurations/ui` request is ever issued, and the "Convert to Customer"
  action is **absent** for a `contacted` lead even though the stored configuration allows that
  status. `LeadDetail.tsx` and `LeadList.tsx` gate the action on
  `useConfiguration().getLeadConversionStatuses()`, but `ConfigurationProvider`'s `useEffect` never
  calls `loadConfigurations()` — it carries a TODO ("Re-enable once backend configuration endpoint is
  stable") and only clears the loading flag, so `configurations` stays `[]` and every getter falls
  back to its hardcoded default: `['qualified']` for conversion statuses, `'GopherCRM'` for the
  company name, `'#1976d2'` for the primary colour. Editing any of those three keys therefore has no
  observable effect anywhere in the application.
- **Known issue:** `gocrm-ui/src/contexts/ConfigurationContext.tsx` — the provider is mounted in
  `App.tsx` but never loads; the Configuration page writes values that nothing reads. Not listed in
  the FEATURES.md Gap Summary; the closest entry is G14 (no E2E coverage for configuration settings).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`
