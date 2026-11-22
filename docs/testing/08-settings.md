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
- `gocrm-ui/src/pages/settings/RoleList.tsx`, `gocrm-ui/src/pages/settings/RoleEditor.tsx`,
  `gocrm-ui/src/api/endpoints/roles.ts` (the role screens, TC-SET-035)
- `gocrm-ui/src/pages/settings/Webhooks.tsx`, `WebhookEndpointDialog.tsx`,
  `WebhookDeliveryLog.tsx`, `gocrm-ui/src/components/OneTimeSecretDialog.tsx`, `gocrm-ui/src/api/endpoints/webhooks.ts`,
  `internal/handler/webhook_handler.go`, `gocrm-ui/e2e/tests/settings-webhooks.spec.ts` (the
  webhook screen, TC-SET-036 to TC-SET-040)
- `gocrm-ui/src/pages/settings/SSOSettings.tsx`, `gocrm-ui/src/api/endpoints/ssoSettings.ts`,
  `internal/handler/sso_settings_handler.go` (the single sign-on screen, TC-SET-041 and TC-SET-043)
- `gocrm-ui/src/pages/settings/SCIMTokens.tsx`, `gocrm-ui/src/components/OneTimeSecretDialog.tsx`,
  `gocrm-ui/src/api/endpoints/scimTokens.ts`, `internal/handler/scim_token_handler.go`,
  `gocrm-ui/e2e/tests/settings-scim-tokens.spec.ts` (the SCIM provider token screen, TC-SET-042)
- `gocrm-ui/src/api/endpoints/apikeys.ts`, `gocrm-ui/src/api/endpoints/configurations.ts`,
  `gocrm-ui/src/api/endpoints/apikeys.test.ts`, `gocrm-ui/src/api/client.ts`
- `gocrm-ui/src/contexts/ConfigurationContext.tsx`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `internal/handler/apikey_handler.go`, `internal/handler/configuration_handler.go`,
  `internal/handler/routes.go` (`SetupAPIKeyRoutes`, `SetupConfigurationRoutes`), `cmd/main.go`
- `internal/service/apikey_service.go`, `internal/service/configuration_service.go`,
  `internal/service/auth_apikey.go` (`ValidateAPIKey`), `internal/middleware/auth.go` (`RequireRole`)
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
- **Configurations are global, shared, mutable state.** There are 23 rows, all seeded by
  `InitializeDefaults()`, and every session of the application reads the same ones. Any spec that
  writes a configuration must run serially and restore the value it changed (POST
  `/configurations/{key}/reset` is the cheapest restore, but note that
  `leads.conversion.allowed_statuses` ships with `value=["qualified", "contacted"]` and
  `default_value=["qualified"]`, so "reset" is not "restore" for that key — see TC-SET-026).
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
- **Expected:** The "API Keys" nav item is visible — the item carries no `permission` key in
  `MainLayout.tsx`, unlike "Configuration" which needs `settings.view`. The route
  `settings/api-keys` sits outside every guarded pathless route in `routes/index.tsx`, so the
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
  backend behaviour is covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestCreate_Success` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestCreateAPIKey`

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
- **Automation:** blocked — no create form exists;
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestUpdate_InvalidName` covers the equivalent rule on update

### TC-SET-005 — Generate a key with a future expiry
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Open the generate dialog, enter a generated name and an expiry one day in the future.
  2. Submit and inspect the created key in the list.
- **Expected:** `POST /api-keys` with `expires_at` as an RFC3339 timestamp (for example
  `2026-12-31T23:59:59Z`) returns **201** and the stored record carries that `expires_at`. Omitting
  the field gives `expires_at` one year from now (#532). Expiry cannot be lifted afterwards:
  `PUT /api-keys/{id}` only accepts `name` and `is_active`.
- **Automation:** blocked — no create form; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestCreate_WithFutureExpiresAt` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestCreateAPIKeyWithExpiry`

### TC-SET-006 — Reject a past or malformed expiry
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Submit the generate form with `expires_at` set to a timestamp in the past.
  2. Repeat with a non-RFC3339 string such as `31-12-2026`.
- **Expected:** Both are **400** with distinct messages from `APIKeyHandler.Create`: `expires_at must
  be in the future` and `expires_at must be an RFC3339 timestamp`. An expiry more than one year ahead
  is **400** `expires_at must be at most one year from now` (#532). No key is minted in either case —
  the handler deliberately refuses to issue a credential that could never authenticate.
- **Automation:** blocked — no create form; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestCreate_PastExpiresAt` /
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestCreate_MalformedExpiresAt` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestCreateAPIKeyWithPastExpiry`

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
- **Automation:** blocked — no table in the page; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestList_Success` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestListAPIKeys`

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
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestGet_Forbidden` /
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestGet_NotFound` /
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestGet_InvalidID` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestGetAPIKey`

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
  inactive and stamps `revoked_at` — so it stays in the list with `is_active=false` and a
  `revoked_at` time; revoking it again answers 200 and keeps the first `revoked_at`. The subsequent API-key call is rejected
  at authentication (`ValidateAPIKey` returns "API key has been revoked"), surfacing as **401**.
  Revoking someone else's key is **403**; an unknown id is **404**.
- **Automation:** blocked — no revoke control in the page; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestRevoke_Success` /
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestRevoke_Forbidden` /
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestRevoke_NotFound` and
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestRevokeAPIKey`

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
- **Automation:** blocked — no edit control; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestUpdate_Rename` and
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestUpdate_EmptyBody`

### TC-SET-012 — A revoked key cannot be switched on again
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Seeded admin logged in; a key created and then revoked by the test, plaintext
  retained.
- **Steps:**
  1. Send `PUT /api-keys/{id}` with `{"is_active": true}` (and once with a `name` as well), signed in.
  2. Send the same request authenticated with another live key of the same user.
  3. Retry the plaintext key against an authenticated endpoint.
- **Expected:** Step 1 answers **409** `CONFLICT` ("A revoked API key cannot be switched on
  again") and changes nothing: name, `is_active`, `revoked_at` and `updated_at` stay as they were.
  Step 2 answers **403** `FORBIDDEN`: only a signed-in session may send `is_active: true`. The key
  stays refused (**401**). A key that was inactive before `revoked_at` existed is refused the same
  way. `is_active: true` on a live key answers 200 and changes nothing. Expiry is checked
  separately in `ValidateAPIKey` and is not affected by this endpoint.
- **Automation:** blocked — no edit control; covered by
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestUpdate_SwitchOnRevokedIsConflict`,
  `internal/handler/apikey_handler_test.go` `APIKeyHandlerTestSuite.TestUpdate_SwitchOnFromAPIKeyIsForbidden`,
  `tests/apikey_integration_test.go` `APIKeyIntegrationTestSuite.TestUpdateAPIKey` and
  `internal/service/auth_apikey_revocation_test.go` `TestAPIKeyRevocationFinalSQLite`

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
  refuses the key with "API key owner is not active". The deactivation from the Users page also
  revokes every key of the user in the same transaction (#526: `is_active` false, `revoked_at`
  set). Deactivation is the reversible alternative to erasure and touches no personal data; the
  key stays revoked after reactivation, so the user issues a new one.
- **Automation:** blocked — no UI surface for API keys and no role-login helper; covered by
  `internal/service/auth_service_test.go` `TestAuthService_ValidateAPIKey` subtest "key of a
  deactivated user is rejected", `test/integration/erasure_test.go`
  `TestAPIKeyOfADeactivatedUserCannotAuthenticate`, `TestCredentialRevocation{SQLite,MySQL}`
  (`set_active_false`, `set_active_true_after_a_deactivation_restores_nothing`) and
  `TestCredentialRevocationProductionWiring` (`administrator_deactivation_then_activation`)

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
  `APIKeyIntegrationTestSuite.TestExpiredAPIKeyIsRejectedAtAuth`

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
  Actions. With the seeded defaults there are 23 entries: General (1) `general.company_name`,
  UI & Theme (1) `ui.theme.primary_color`, Security (1) `security.allow_public_registration`,
  Leads (3) `leads.conversion.{allowed_statuses,require_notes,auto_assign_owner}`, Deals (1)
  `deals.default_currency`, Integration (16) the `integration.aeo.*` keys, models and base URLs,
  and none under Tickets (`security.session_timeout_hours` and `tickets.auto_assign_support` were
  retired in #224; startup deletes them). A warning banner sits above the tabs. If the request fails the page
  shows an error alert with the server's message (or `Failed to load configurations`) and a `Retry`
  button, and no tabs: no "No configurations found for ..." text appears. Retry issues
  `GET /configurations` again and shows the tables once it succeeds (`ConfigurationSettings.test.tsx`
  "shows an error with Retry instead of empty tabs, and loads again on Retry").
- **Automation:** automated — `gocrm-ui/e2e/screenshots/08-settings.spec.ts` "configuration settings
  page"

### TC-SET-017 — Show the empty-state text for categories with no entries
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in, default configuration set.
- **Steps:**
  1. Open `/settings/configuration`.
  2. Select the "Customers", "Tickets" and "Tasks" tabs in turn.
- **Expected:** Each shows `No configurations found for <label>` and no table. The tab labels read
  `Customers (0)`, `Tickets (0)`, `Tasks (0)` — the counts come from the client-side filter
  `getConfigurationsByCategory`, not from a server query, so all nine category tabs exist
  regardless of what is stored (Tickets has been empty since #224 retired
  `tickets.auto_assign_support`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (new; closes G14)

### TC-SET-018 — Render each value according to its declared type
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in, default configuration set.
- **Steps:**
  1. Open `/settings/configuration`.
  2. Inspect the Current Value cell on the Leads and General tabs.
- **Expected:** `boolean` renders as a chip labelled `True` (green) or `False`; `array` renders one
  chip per element, so `leads.conversion.allowed_statuses` shows `qualified` and `contacted`;
  `string` renders as plain text. No seeded entry is `integer` (since #224) or `json`, so their
  rendering (plain text, monospaced) needs a test-only fixture inserted directly. The Key cell is monospaced,
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
  1. Leads tab, edit `leads.conversion.require_notes`.
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
- **Expected:** The dialog shows `Valid values: ["new", "contacted", "qualified", "unqualified"]`
  and a multi-select offering exactly those four options (before #104 the list was `new, contacted,
  qualified, converted, lost`: `lost` is not a lead status and `unqualified` could not be chosen;
  the seeder corrects the metadata of existing rows on boot and keeps their value), because the entry's type is
  `array` and `valid_values` parses to a string array. Saving sends a JSON array; the service checks
  every element against the allowlist (`Configuration.IsValidValue`) before storing. The result is
  **200** and the Current Value cell drops the `contacted` chip.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

### TC-SET-022 — Reject an integer value outside the allowlist
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Edit an integer entry whose valid values are `[1, 8, 24, 48, 72, 168]`.
  2. Type `25` into the number field and click "Save".
- **Expected:** `PUT /configurations/{key}` returns **400** with
  `Invalid value for configuration` (`ErrConfigurationInvalidValue`, raised by `IsValidValue`
  against `[1, 8, 24, 48, 72, 168]`). The dialog stays open and the snackbar shows the API's message
  `Invalid value for configuration` (`handleSaveEdit` falls back to `Failed to update configuration`
  only when the error carries no message). The stored value is unchanged; `24` saves fine.
- **Known issue:** the edit dialog switches on `type` before `valid_values`
  (`ConfigValueEditor.tsx` `ConfigValueEditor`), so an *integer* entry with an allowlist gets a
  free-form number field while a *string* entry with an allowlist gets a dropdown. The constraint is
  only visible as the "Valid values:" line of text.
- **Automation:** blocked — no seeded entry is an integer since #224 retired
  `security.session_timeout_hours`; backend coverage in `tests/configuration_integration_test.go`
  `ConfigurationIntegrationTestSuite.TestSetConfiguration_InvalidValue` (a test-only `test.choice.integer` entry)

### TC-SET-023 — Clear the integer field and get an inline field error
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Edit an integer entry.
  2. Select the field contents and delete them, leaving it empty.
  3. Click "Save".
- **Expected:** No request is sent. Number fields keep the typed text until Save, which refuses an
  empty or non-finite entry with the inline field error `Enter a number` (and a fractional entry for
  an integer with `Enter a whole number`); the dialog stays open and the value is untouched. JSON and
  free-form array fields work the same way: the text shows exactly what was typed, and Save refuses
  invalid JSON with `Enter valid JSON`, or a non-array for an `array` entry with `Enter a JSON array`.
  At the API a body of `{"value":null}` is still answered **400** `Configuration value cannot be null`
  (`ConfigurationHandler.Set`).
- **Automation:** blocked in the browser — no seeded entry is an integer since #224 retired
  `security.session_timeout_hours`; the dialog is covered by `ConfigurationSettings.test.tsx`
  "refuses an empty number", "refuses incomplete JSON with a field error and sends nothing" and
  "saves a typed array and refuses a non-array for an array setting"

### TC-SET-024 — Confirm values are type-checked, never coerced
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin bearer token (take it from the logged-in session and drive this through
  Playwright's `request` fixture; the dialog cannot produce a wrong-typed value for these keys).
- **Steps:**
  1. `PUT /configurations/leads.conversion.require_notes` with `{"value":"yes"}`.
  2. `PUT /configurations/general.company_name` with `{"value":42}`.
  3. Re-read each key with `GET /configurations/{key}`.
- **Expected:** Both are **400** `Invalid value for configuration`, and every stored value is
  unchanged. `Configuration.SetValue` returns an error on a type mismatch instead of coercing —
  a non-boolean used to become `"false"` and a non-string `""`, which answered the caller with a
  success and a corrupted value. No seeded entry is an integer since #224; for one, an integral float
  such as `24.0` is accepted and `24.5` is not (`asInteger` refuses to truncate), which the backend
  tests cover with test-only entries. Correctly typed values still work.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts` (API-level assertions
  alongside the UI ones); backend coverage in `tests/configuration_integration_test.go`
  `ConfigurationIntegrationTestSuite.TestSetConfiguration_TypeMismatch` and `TestSetConfiguration_CorrectTypesStillAccepted`

### TC-SET-025 — Cancel an edit without writing
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Seeded admin logged in.
- **Steps:**
  1. Edit `general.company_name`, type a different value.
  2. Click "Cancel".
  3. Reopen the same entry.
- **Expected:** No request is issued, no snackbar appears, and the table still shows the original
  value. Reopening the dialog shows the stored value again, because `handleEdit` re-seeds the
  editor from the configuration each time it opens.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`

---

## 9.3 Reset configuration

### TC-SET-026 — Reset a configuration to its stored default
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin logged in. Note the pre-state of
  `leads.conversion.allowed_statuses`: `value=["qualified", "contacted"]`,
  `default_value=["qualified"]`.
- **Steps:**
  1. Leads tab, click the reset (circular arrow) action on `leads.conversion.allowed_statuses`.
  2. In the `Reset to default?` dialog, click `Reset`.
  3. Observe the row.
- **Expected:** The click opens a confirmation dialog naming the key and its default value and
  sends nothing; `Cancel` closes it with no request. `Reset` sends
  `POST /configurations/leads.conversion.allowed_statuses/reset`, which returns **200**; the
  snackbar reads `Configuration reset to default`, the list reloads and the Current Value cell drops
  the `contacted` chip.
- **Known issue:** the seeded row ships with a value that differs from its own default
  (`models.DefaultConfigurations()`), so "reset" visibly changes behaviour on a system nobody has
  edited. A spec that resets this key must restore `["qualified", "contacted"]` afterwards, or later
  runs start from a different state.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`; the confirmation step
  is covered by `ConfigurationSettings.test.tsx` "asks before resetting and resets only on Confirm"

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
  `tests/configuration_integration_test.go` `ConfigurationIntegrationTestSuite.TestResetConfiguration_UnknownKey` and
  `tests/configuration_integration_test.go` `ConfigurationIntegrationTestSuite.TestSetConfiguration_UnknownKey`

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
  a configuration_transaction_test.go under test/integration, which does not exist; configuration
  transactionality is untested.
- **Automation:** blocked — no endpoint writes multiple configurations, so there is no user-facing
  behaviour to assert; would need a Go integration test against `BulkUpsert`

### TC-SET-029 — Confirm read-only entries cannot be edited or reset
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A configuration row with `is_read_only=true`. **None of the 23 seeded rows
  sets this flag** (verified against `models.DefaultConfigurations()` and a live
  `GET /configurations`), so the fixture must be inserted directly.
- **Steps:**
  1. Insert a read-only configuration row.
  2. Open `/settings/configuration` and find it.
  3. Attempt to edit and to reset it, both from the UI and through the API.
- **Expected:** The row shows a warning "Read-Only" chip and, for a caller holding
  `settings.update`, both action `IconButton`s are `disabled`, so the UI cannot start either
  operation. A caller without `settings.update` sees no Edit or Reset button on any row
  (`ConfigurationSettings.test.tsx` "shows no Edit or Reset to a caller who may only view settings"). At the API, `PUT /configurations/{key}` and
  `POST /configurations/{key}/reset` both return **400** `Configuration is read-only`
  (`ErrConfigurationReadOnly`, raised in `configurationService.Set` and `.Reset`).
- **Automation:** blocked — no seeded read-only configuration exists to drive the UI path; the API
  path is covered by
  `tests/configuration_integration_test.go` `ConfigurationIntegrationTestSuite.TestSetConfiguration_ReadOnly` and
  `tests/configuration_integration_test.go` `ConfigurationIntegrationTestSuite.TestResetConfiguration_ReadOnly`, which create their own row

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
- **Expected:** The "Configuration" nav item is absent for all three (`permission: 'settings.view'` in
  `MainLayout.tsx`), and so is "Roles" (`roles.view`, TC-SET-035), while "Profile" and "API Keys" remain. The deep link redirects to
  `/unauthorized` — `settings/configuration` is a child of the pathless
  `ProtectedRoute requiredPermission="settings.view"` route, and `ProtectedRoute` issues
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
- **Expected:** **200** for both, with the same entries: the whole `ui` category
  (`ui.theme.primary_color`), `general.company_name`, `deals.default_currency`, and three
  *synthetic* read-only lead-conversion entries assembled in the handler (since #104):
  `leads.conversion.allowed_statuses` (array), `leads.conversion.require_notes` and
  `leads.conversion.auto_assign_owner` (boolean, `"true"`/`"false"`), carrying the values the
  convert endpoint enforces. The synthetic entries are not stored rows — they come back with
  `id: 0`, zero timestamps, an empty `default_value` and `is_read_only: true`, and their
  `category` is `leads` even though the whole `leads` category is otherwise admin-only. An entry
  whose setting cannot be read is omitted. No `RequireRole` guards
  this route, deliberately: it is the one configuration read the frontend is allowed to make.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-configurations.spec.ts`; backend coverage in
  `tests/configuration_integration_test.go` `ConfigurationIntegrationTestSuite.TestGetUIConfigurations` and, for the lead-conversion
  entries, `internal/handler/configuration_handler_test.go` `ConfigurationHandlerTestSuite.TestGetUIConfigurations_CarriesTheLeadConversionContract`

### TC-SET-034 — Confirm configuration changes reach the leads pages
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Seeded admin; a lead in status `contacted` created with `generateLeadData()`;
  `leads.conversion.allowed_statuses` at its seeded value `["qualified", "contacted"]`.
- **Steps:**
  1. Open `/login` and record the network requests; sign in.
  2. Open the lead's detail page and look for the "Convert to Customer" action.
  3. Change `leads.conversion.allowed_statuses` to `["qualified"]` on the Configuration page, then
     revisit the lead.
  4. Sign out, sign in as another user, and record the requests again.
- **Expected:** No `GET /configurations/ui` is issued on the login page; one is issued after the
  sign-in. The action is **present** for the `contacted` lead in step 2 and absent after step 3:
  the save refreshes the app-wide configuration (no reload needed). Signing out drops the values;
  the next account's sign-in requests them again and never sees the previous account's copy
  (one shared TanStack Query entry keyed by user id; the session drops cached queries before the
  new account renders). While the request is in flight, or if it fails, the display getters
  return their defaults, `'GopherCRM'` for the company name and `'#1976d2'` for the primary
  colour, and the error is exposed as `error`; the lead-conversion rules are reported as loading
  or unavailable, never defaulted, and the lead pages show the action disabled with a Retry. Before #117 the provider's mount effect was a
  TODO that never loaded anything, so editing these keys had no visible effect.
- **Automation:** automated — Vitest `src/contexts/ConfigurationContext.test.tsx` (no request
  without a session or while it is restored, configured values, sign-in loads, sign-out clears,
  account switch refetches without showing the previous values, failed fetch, malformed or missing
  rule entry, empty status list, one shared request, explicit refresh, and sign-in, sign-out,
  account switch and session reload through the real `AuthProvider`);
  `src/pages/leads/LeadConversionSettings.test.tsx` (the lead pages while the rules load or failed);
  `src/pages/settings/ConfigurationSettings.test.tsx` (refresh after save and reset); the browser
  path in `gocrm-ui/e2e/tests/leads-convert-api.spec.ts` "a contacted lead converts only with notes
  when the settings say so" (settings saved through the API, then a fresh sign-in)

## Roles

### TC-SET-035 — Clone a system role, edit it and see the change reach its holder
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Seeded admin; no role named "Field Sales".
- **Steps:**
  1. Open `/settings/roles` and press Clone on the Sales row; name the copy "Field Sales".
  2. In the editor that opens, untick the "Deals" group and save.
  3. Create a user with the role "Field Sales" (TC-USER-041) and sign in as that user in a second
     browser context.
  4. As the admin, open the role again, tick "View deals" and save.
  5. Give the user's window the focus again.
- **Expected:** The list shows the four system roles (type System, only a Clone action) and the
  custom ones (type Custom; Edit, Clone and Delete) with their user and permission counts; the
  entry and the pages need `roles.view`, every action and `/settings/roles/new` need
  `roles.manage`, and without it the list shows no action and the editor opens read-only. Step 1:
  `POST /roles/<id>/clone` → 201 and the editor of the copy opens with what Sales holds ("View
  deals" at scope Own, the "Deals" parent partly ticked). Step 2: `PUT /roles/<id>` carries
  `name`, `description` and `permissions` (one `{key, scope}` per ticked permission, no group
  key) and no deal key. Step 3: the user sees Leads in the navigation and no Deals, and `/deals`
  lands on `/unauthorized`. Step 5: without a reload or a new sign-in the Deals entry appears:
  the focus refetches `GET /users/me`, and a role change applies on the holder's next request
  (the editor says so on a custom role). A system role opens read-only with a Clone button; a
  permission only a system role holds (`tickets.view_own_customer`) is not offered, and neither are
the two admin-only permissions (`settings.sso_manage`, `settings.scim_manage`). Deleting a
  role that users still hold answers 409 `ROLE_IN_USE` and the confirmation stays open with the
  number of holders; a name another role has answers 409 `ROLE_NAME_TAKEN` on the name field.
- **Automation:** automated — `gocrm-ui/e2e/tests/roles.spec.ts` "a cloned role decides what its
  holder sees, and a re-granted permission arrives on focus"; Vitest
  `src/pages/settings/RoleList.test.tsx`, `src/pages/settings/RoleEditor.test.tsx`,
  `src/routes/index.permissions.test.tsx` and `src/layouts/MainLayout.permissions.test.tsx`

## Webhooks

The screen at `/settings/webhooks` (#409) manages the outbound webhook endpoints and shows their
delivery log; the events, the signature and the retry schedule are in the README section
"Integrations: webhooks". The browser spec registers its endpoints under the reserved `.invalid`
domain, which never resolves, so nothing is delivered outside the machine and no delivery of it
can succeed.

### TC-SET-036 — Register a webhook endpoint and see its signing secret exactly once
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Seeded admin; no endpoint with the URL of step 2.
- **Steps:**
  1. Open Settings > Webhooks and press "Add endpoint".
  2. Enter `https://e2e-hooks.invalid/a`, tick `lead.created`, leave Active on and press Create.
  3. Read the dialog that opens, then press Done.
  4. Reload the page.
- **Expected:** The form offers one checkbox per event type, nine in all (`customer.created`,
  `customer.deleted`, `customer.updated`, `lead.created`, `lead.deleted`, `lead.updated`,
  `ticket.created`, `ticket.deleted`, `ticket.updated`, #437: the list
  of `src/api/endpoints/webhooks.ts`, held equal to the `event_types` enum of `api/swagger.json`).
  Step 2: `POST /webhooks/endpoints` carries `url`, `event_types` and `is_active` → 201. Step 3:
  the "Signing secret" dialog shows a read-only `whsec_` value, a "Copy signing secret" button
  and the notice that it cannot be shown again. Step 4: the endpoint is listed with its URL, the
  `lead.created` chip, "Active" and its created date; no `whsec_` text is in the page, in a field,
  in `localStorage` or in `sessionStorage`. The secret is held in the page's component state only:
  the list is fetched again after the create, never patched with the answer, and neither cache of
  the query client holds it after the dialog closes. No other view renders a secret.
- **Automation:** automated — `gocrm-ui/e2e/tests/settings-webhooks.spec.ts` "admin registers an
  endpoint, sees the secret once, edits and deletes it"; Vitest
  `src/pages/settings/Webhooks.test.tsx`, `src/components/OneTimeSecretDialog.test.tsx`,
  `src/pages/settings/WebhookEndpointDialog.test.tsx` and `src/api/endpoints/webhooks.test.ts`

### TC-SET-037 — Edit, deactivate and delete a webhook endpoint
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin; the endpoint of TC-SET-036.
- **Steps:**
  1. Press Edit on the endpoint's row, tick `lead.updated`, switch Active off and press Save.
  2. Reload the page.
  3. Press Delete on the row, read the confirmation and confirm.
  4. Reload the page.
- **Expected:** The edit form opens with the stored URL and events. Step 1: `PUT
  /webhooks/endpoints/<id>` carries `url`, `event_types` and `is_active` → 200. Step 2: the row
  shows `lead.created` and `lead.updated` and "Inactive". The Activate / Deactivate button of a row
  sends a `PUT` with `is_active` only. Step 3: the "Delete webhook endpoint" dialog names the URL
  and says "Its delivery log is removed with it"; Cancel sends nothing; Delete sends `DELETE
  /webhooks/endpoints/<id>` → 204. Step 4: the endpoint is gone from the list. When the deleted
  row was the only one of a later page, the list shows the last existing page, not "No webhook
  endpoints yet.", which appears only when no endpoint is left (#423).
- **Automation:** automated — `gocrm-ui/e2e/tests/settings-webhooks.spec.ts` "admin registers an
  endpoint, sees the secret once, edits and deletes it"; Vitest
  `src/pages/settings/Webhooks.test.tsx` and `src/pages/settings/WebhookEndpointDialog.test.tsx`

### TC-SET-038 — Keep a caller without webhooks.manage out of the Webhooks screen
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A user whose `GET /users/me` permissions hold `settings.view` and
  `roles.manage` but not `webhooks.manage`; a second one that holds `webhooks.manage`.
- **Steps:**
  1. As the first user, open the Settings group of the navigation, then navigate to
     `/settings/webhooks`.
  2. Repeat as the second user.
- **Expected:** The first user sees no Webhooks entry under Settings and lands on
  `/unauthorized`; the second sees the entry and the page. The permission list decides, never the
  role name: among the system roles only admin holds the key, and `GET /webhooks/endpoints` and
  `GET /webhooks/deliveries` answer 403 to every other caller.
- **Automation:** automated — Vitest `src/routes/index.permissions.test.tsx` "open the webhooks
  screen only with webhooks.manage", `src/layouts/MainLayout.permissions.test.tsx` "shows Webhooks
  under Settings only to a caller holding webhooks.manage" and
  `src/permissions/noRoleGating.test.ts`; the API refusal in `TestWebhookRoutesAdminOnly`
  (`internal/handler`). No browser spec signs in with a role that lacks the key.

### TC-SET-039 — Read the webhook delivery log and narrow it with its filters
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin; endpoint A subscribed to `lead.created` and endpoint B to
  `lead.created` and `lead.updated`, no other endpoint; then one lead created and one stored value
  of it changed with `PUT /leads/<id>`.
- **Steps:**
  1. Open Settings > Webhooks and the "Delivery log" tab.
  2. Choose endpoint A in the Endpoint filter.
  3. Set Endpoint back to "All endpoints" and choose `lead.updated` in the Event type filter.
  4. Set Event type back to "All event types" and choose `succeeded` in the Status filter.
- **Expected:** Step 1: the columns Event type, Record ID, Status, Attempts, Last attempt and Last
  response code (a dash while there is none); three rows in the order of the API, newest first:
  `lead.updated`, then the two `lead.created`, each with the lead's id. Step 2: the request carries
  `endpoint_id` and one `lead.created` row is left. Step 3: `event_type=lead.updated`, no
  `endpoint_id`, one row. Step 4: `status=succeeded` and the text "No deliveries match these
  filters." A filter set to All is left out of the request; a changed filter returns to the first
  page; pagination uses `meta.total`. The rows stay `pending` (briefly `sending`) because the host
  never resolves. The log shows no URL, secret, event id or payload. A later page that comes back
  empty while `meta.total` is above 0 moves to the last existing page; "No deliveries match these
  filters." appears only when the total is 0 (#423).
- **Known issue:** The Endpoint filter offers one API page, the 100 newest endpoints; the log has
  no endpoint column, so rows of different endpoints are told apart only through the filter.
- **Automation:** automated — `gocrm-ui/e2e/tests/settings-webhooks.spec.ts` "the delivery log
  lists the events newest first and narrows by endpoint, event type and status"; Vitest
  `src/pages/settings/WebhookDeliveryLog.test.tsx` and `src/api/endpoints/webhooks.test.ts`

### TC-SET-040 — Show the server's refusal of an invalid webhook URL
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Seeded admin.
- **Steps:**
  1. In the "Add webhook endpoint" form enter `http://e2e-hooks.invalid/a`, tick `lead.created`
     and press Create.
  2. Repeat with a URL that carries credentials (`https://user:pw@host/x`) and one with a query
     string (`https://host/x?a=1`).
  3. Press Create with the URL empty and no event ticked.
- **Expected:** Steps 1 and 2: the form sends the request unchanged (it checks no scheme,
  credentials or query string itself), `POST /webhooks/endpoints` answers 400, and the form shows
  the alert "Validation failed" and, under the URL field, the rule of `details.url` ("must be an
  https URL of at most 2048 characters, without credentials, query string or fragment, ..."); a
  `details.event_types` text is shown under the checkboxes. The form stays open with what was
  typed and no endpoint is created. Step 3: "Enter the URL of the endpoint" and "Select at least
  one event" are shown and no request is sent.
- **Automation:** automated — `gocrm-ui/e2e/tests/settings-webhooks.spec.ts` "admin registers an
  endpoint, sees the secret once, edits and deletes it" (the `http://` URL); Vitest
  `src/pages/settings/WebhookEndpointDialog.test.tsx` (all three URLs, the event detail and the
  two presence checks)

### TC-SET-041 — Edit the single sign-on settings with a write-only client secret
- **Priority:** P1
- **Type:** security
- **Preconditions:** Signed in with `settings.sso_manage`; a client secret is stored and the role
  mapping has two rows.
- **Steps:**
  1. Open Settings > Single sign-on.
  2. Press Save without changing anything.
  3. Type a new client secret and press Save.
  4. Press "Clear client secret" and press Save.
  5. Press "Add mapping", enter a value, choose a role from the list and press Save.
  6. Open the screen with only `settings.view`.
- **Expected:** Step 1: the Enabled switch, the "Allow password sign-in and password reset for
  accounts linked to single sign-on" switch with its always-shown offboarding warning (#457),
  Issuer URL, Client ID, Scopes (separated by spaces),
  an empty Client secret input with "A client secret is set", and the mapping rows in stored order
  with the note that the first matching row wins; a field from the environment or the default
  says so. A row whose role the list does not name, or every row when the role list fails (an
  error alert), shows "Role #id" and keeps its id. Step 2: `PUT /settings/sso` with the shown
  values (`allow_password` included), the two rows in the same order and no `client_secret` key. Step 3: the body carries the
  secret; afterwards the input is empty and the secret appears nowhere on the page. Step 4: the
  body carries `client_secret: ""` and the page shows "No client secret is set". Step 5: the new
  row is sent last with the chosen `role_id`. A 400 lists the refused fields of `details` (for
  example `role_mapping[0].role_id`). Step 6: `/unauthorized`, and the "Single sign-on" navigation
  entry is shown only with `settings.sso_manage`.
- **Known issue:** `PUT /settings/sso` is a full replacement, so the first save stores the
  environment-derived values shown as settings; a later `OIDC_*` change no longer applies to them.
- **Automation:** automated — Vitest `src/pages/settings/SSOSettings.test.tsx`, `src/routes/index.permissions.test.tsx` "open the single sign-on screen only with settings.sso_manage", `src/layouts/MainLayout.permissions.test.tsx` "shows Single sign-on under Settings only to a caller holding settings.sso_manage"

### TC-SET-043 — Set, show and clear the single sign-on client certificate
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Signed in with `settings.sso_manage`; no client certificate is stored; a
  certificate and its unencrypted RSA-2048 private key were generated for the test (never a
  committed key).
- **Steps:**
  1. Open Settings > Single sign-on.
  2. Paste the certificate into "Client certificate (PEM)" and the key into "Client private key
     (PEM)", and press Save.
  3. Reload the screen and read `GET /settings/sso`.
  4. Paste only a certificate (or a key that does not match it) and press Save.
  5. Press "Clear client certificate" and press Save.
- **Expected:** Step 1: the two inputs are empty and `sso-certificate-status` reads "No client
  certificate is set". Step 2: the PUT carries both `client_certificate` and `client_private_key`;
  the answer is 200 and holds neither PEM; both inputs are empty afterwards and the status reads
  "A client certificate is set (thumbprint <T>, expires <date>); it is used instead of the client
  secret", where <T> is the uppercase hex SHA-1 of the certificate; an expired stored certificate
  also says that it has expired. Step 3: the same status; the GET body carries
  `client_certificate_set` true, the same thumbprint, `client_certificate_expires_at` and
  `sources.client_certificate` "settings", and neither the page nor the body contains the key.
  Step 4: 400 `VALIDATION_ERROR` with the refusal under the matching input
  (`details.client_private_key` or `details.client_certificate`), no message quoting the pasted
  value, and nothing stored. Step 5: before the save the status reads "The client certificate will
  be cleared when you save"; the PUT carries both keys as "", and afterwards the status reads "No
  client certificate is set". A save that leaves both inputs empty sends neither key and keeps the
  stored pair.
- **Automation:** automated — Playwright `gocrm-ui/e2e/tests/sso.spec.ts` "S15: a client certificate is saved on the settings screen, shown by its thumbprint, its key never"; Vitest `src/pages/settings/SSOSettings.test.tsx` "client certificate"; Go `internal/handler/sso_settings_handler_test.go` `TestSSOSettingsHandlerClientCertificate`, `internal/service/sso_settings_test.go` `TestSSOSettingsClientCertificate`, `internal/service/auth_oidc_test.go` `TestOIDCClientAssertion` (the client assertion of the code exchange)

### TC-SET-042 — Create, show once and revoke a SCIM provider token
- **Priority:** P1
- **Type:** security
- **Preconditions:** Signed in with `settings.scim_manage` (the admin system role).
- **Steps:**
  1. Open Settings > SCIM tokens from the navigation.
  2. Press "Create token", enter a name and press Create.
  3. Copy the token, then press Done.
  4. Call `GET /scim/v2/ServiceProviderConfig` on the backend origin with
     `Authorization: Bearer <token>`.
  5. Press "Revoke <name>" on the new row, then Revoke in the confirmation.
  6. Repeat step 4.
  7. Open `/settings/scim-tokens` with only `settings.sso_manage`, and with no keys.
- **Expected:** Step 1: `/settings/scim-tokens` with the columns Name, Created, Last used, Revoked
  and Actions, every date through `formatDate` (the dash for a null date), revoked tokens listed
  with their revoke action disabled. Step 2: `POST /settings/scim/tokens` with the trimmed name; a
  blank name is refused in the form and a server 400 is shown in the dialog. A dialog shows the
  token (`scim_` + 64 hex characters, equal to the POST answer) once, read-only, with a "Copy SCIM
  token" button and the warning that it is shown only once and cannot be retrieved again. Step 3:
  the token is gone from the page and is kept in no query or mutation cache, storage or snackbar;
  the list is read again and the new row shows the dash under Last used. Step 4: 200. Step 5: the
  confirmation says the identity provider using it will be refused at once; Cancel sends nothing;
  Revoke sends `DELETE /settings/scim/tokens/{id}` (200), and the row stays listed with a Revoked
  date and a disabled action. Step 6: 401. Step 7: `/unauthorized`, and the "SCIM tokens"
  navigation entry is shown only with `settings.scim_manage`.
- **Automation:** automated — Playwright `gocrm-ui/e2e/tests/settings-scim-tokens.spec.ts` "creates a token shown once, revokes it, and the revoked token is refused on /scim/v2"; Vitest `src/pages/settings/SCIMTokens.test.tsx`, `src/routes/index.permissions.test.tsx` "open the SCIM tokens screen only with settings.scim_manage", `src/layouts/MainLayout.permissions.test.tsx` "shows SCIM tokens under Settings only to a caller holding settings.scim_manage"
