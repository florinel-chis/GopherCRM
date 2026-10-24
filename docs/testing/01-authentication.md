# Authentication & Session Management — Test Cases

Playwright E2E test cases for registration, login, account lockout, token storage, logout, token
refresh, password reset and protected-route gating. Every **Expected** section records what the
application does **today**, including behaviour that is defective or surprising; where that is the
case a **Known issue** line names it. Cases marked *automated* were verified by reading the spec
file, not by trusting the coverage matrix.

**Sources**

- `docs/FEATURES.md` section 1 (rows 1.1–1.9), section 10b "Auth sessions", section 11.1, Gap
  Summary G11/G12
- `docs/ROADMAP.md` — "Follow-ups from the backend build-out" (missing SPA reset route, no
  access-token blocklist, concurrent-refresh stampede)
- `gocrm-ui/src/pages/auth/Login.tsx`, `gocrm-ui/src/pages/auth/Register.tsx`
- `gocrm-ui/src/contexts/AuthContext.tsx`, `gocrm-ui/src/api/client.ts`,
  `gocrm-ui/src/api/endpoints/auth.ts`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `internal/handler/auth_handler.go`, `internal/handler/routes.go`,
  `internal/service/auth_service.go`, `internal/repository/password_reset_token_repository.go`,
  `internal/repository/refresh_token_repository.go`,
  `internal/service/auth_password_reset_regression_test.go`,
  `internal/service/auth_refresh_rotation_regression_test.go`, `internal/middleware/rate_limit.go`,
  `cmd/main.go`
- `gocrm-ui/e2e/tests/login.spec.ts`, `gocrm-ui/e2e/tests/registration.spec.ts`,
  `gocrm-ui/e2e/pages/login.page.ts`, `gocrm-ui/e2e/pages/register.page.ts`,
  `gocrm-ui/e2e/helpers/admin-auth.ts`, `gocrm-ui/e2e/screenshots/helpers/login.ts`,
  `gocrm-ui/e2e/global-setup.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`,
  `gocrm-ui/e2e/fixtures/test-data.ts`
- Visual reference: `docs/screenshots/auth/01-login.png`, `02-login-validation.png`,
  `03-register.png`, `04-register-validation.png`

**Constraints**

- The admin account is seeded out-of-band by `gocrm-ui/e2e/global-setup.ts`, which shells out to
  `cmd/create-admin` with `testAdminCredentials` — `POST /auth/register` can never produce one.
- Non-admin accounts must be created inside the test: a `customer` by self-registering through
  `/register`, and `sales` / `support` by an admin through `/users/new`. There is no role-login
  helper; `AdminAuthHelper` only logs in the seeded admin.
- All records must come from the faker generators in `gocrm-ui/e2e/fixtures/admin-user.ts`
  (`generateUserData()`) or `fixtures/test-data.ts` (`generateTestUser()`). Never hardcode an email.
- `/auth/*` is behind `RateLimitStrict()` — 10 req/min, burst 5 (`internal/middleware/rate_limit.go:124`,
  applied at `cmd/main.go:183`). Any case that issues more than five auth requests in quick
  succession must space them out (≈6 s per extra token) or run the backend with
  `DISABLE_RATE_LIMIT=true`, which bypasses the strict tier only.
- Deleting a user is irreversible GDPR erasure (`internal/repository/erasure.go`). Only delete
  accounts the test itself created.

---

## Registration (FEATURES 1.1, 1.8)

### TC-AUTH-001 — Register a new account through the form
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged out. `const user = generateTestUser()`.
  `security.allow_public_registration` enabled (the e2e global-setup turns it on; it ships
  disabled — see TC-AUTH-045).
- **Steps:**
  1. Go to `/register`.
  2. Fill First Name, Last Name, Email Address, Password, Confirm Password from `user`.
  3. Click **Sign Up**.
- **Expected:** `POST /api/v1/auth/register` returns **201** with `{success:true, data:{token, user}}`;
  `data.user.email` equals the generated email and `data.user.role` is `customer`. The SPA navigates
  to `/` and the dashboard renders.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "successful registration redirects to dashboard"

### TC-AUTH-002 — A client-supplied role in the registration payload is ignored
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged out. `const user = generateTestUser()`.
- **Steps:**
  1. Using a Playwright `APIRequestContext`, `POST /api/v1/auth/register` with the generated user
     plus an extra `"role": "admin"` field.
  2. Log in through the UI as that user.
  3. Navigate to `/users`.
- **Expected:** The register call returns **201**; the response `data.user.role` is `customer`.
  Gin's binder silently drops the unknown `role` key because `handler.RegisterRequest` has no such
  field (`internal/handler/auth_handler.go:29`), and the handler hard-codes
  `Role: models.RoleCustomer`. `/users` redirects to `/unauthorized`.
- **Automation:** planned — `gocrm-ui/e2e/tests/registration.spec.ts` (extended)

### TC-AUTH-003 — Registering an email that already exists is rejected
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged out. `const user = generateTestUser()`.
- **Steps:**
  1. Register `user` at `/register` and land on the dashboard.
  2. Log out from the avatar menu.
  3. Go to `/register` and submit the identical form.
- **Expected:** `POST /auth/register` returns **409**; the page shows a MUI error alert containing
  `user with this email already exists` (the literal text from
  `internal/handler/auth_handler.go` `RespondConflict`, surfaced by `Register.tsx` as
  `err.response?.data?.message` after `client.ts` unwraps the `{success,error}` envelope).
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "shows error for duplicate email registration"

### TC-AUTH-004 — Password policy is enforced client-side before submit
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Logged out, on `/register`.
- **Steps:**
  1. Fill the name and email fields from `generateTestUser()`.
  2. For each of `testPasswords.tooShort`, `.noUppercase`, `.noLowercase`, `.noNumber`, fill Password
     and Confirm Password with it and click **Sign Up**.
- **Expected:** No network request is made. The Password field turns red and shows the matching zod
  message from `Register.tsx`: "Password must be at least 10 characters" / "…at least one uppercase
  letter" / "…at least one lowercase letter" / "…at least one number" / "…at least one special
  character". The URL stays `/register`.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "validates password requirements"

### TC-AUTH-005 — Confirm Password must match Password
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Logged out, on `/register`.
- **Steps:**
  1. Fill all fields from `generateTestUser()`, then set Confirm Password to a different valid
     password.
  2. Click **Sign Up**.
- **Expected:** The Confirm Password field shows "Passwords don't match" and no request is sent.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "validates password confirmation match"

### TC-AUTH-006 — Submitting an empty registration form shows required-field errors
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Logged out, on `/register`.
- **Steps:** Click **Sign Up** without typing anything.
- **Expected:** The form stays on `/register`; First Name / Last Name / Email / Password show
  validation errors and no `POST /auth/register` is issued. Matches
  `docs/screenshots/auth/04-register-validation.png`.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "shows validation errors for empty fields"

### TC-AUTH-007 — Registration rejects a malformed email address
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Logged out, on `/register`.
- **Steps:** Fill valid names and passwords, set Email Address to `not-an-email`, click **Sign Up**.
- **Expected:** The Email field shows "Invalid email address"; no request is sent.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "validates email format"

### TC-AUTH-008 — Password visibility toggle reveals and re-hides the Password field
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/register`.
- **Steps:**
  1. Type a password into the Password field.
  2. Click the `toggle password visibility` icon button next to it, then click it again.
- **Expected:** The field's `type` starts as `password`, becomes `text` after the first click and
  returns to `password` after the second.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "password visibility toggle works"

### TC-AUTH-009 — Confirm Password has its own independent visibility toggle
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/register`.
- **Steps:**
  1. Type into both Password and Confirm Password.
  2. Click only the second `toggle password visibility` button.
- **Expected:** Confirm Password becomes `type="text"` while Password stays `type="password"` — the
  two are backed by separate `showPassword` / `showConfirmPassword` state in `Register.tsx`.
- **Automation:** planned — `gocrm-ui/e2e/tests/registration.spec.ts` (extended; the existing toggle
  test only drives the first field)

### TC-AUTH-010 — Registration persists the JWT to localStorage and issues no refresh token
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Logged out, both storages empty. `const user = generateTestUser()`.
- **Steps:**
  1. Register through the form and wait for `/`.
  2. Read `localStorage` and `sessionStorage` for `gophercrm_token` and `gophercrm_refresh_token`.
- **Expected:** `localStorage.gophercrm_token` is set and `sessionStorage.gophercrm_token` is null —
  `AuthContext.register()` calls `apiClient.setToken(response.token)` with no `persist` argument, so
  the default `persist = true` applies. Neither storage holds a refresh token: the `Register` handler
  builds an `AuthResponse` without `RefreshToken`, so `refresh_token` is absent from the 201 body.
- **Known issue:** Login and registration disagree about persistence — login honours the "Remember me"
  checkbox (`AuthContext.tsx` `login()`), registration always persists. A newly registered session
  also has no refresh token, so the first 401 in `client.ts` throws "No refresh token available" and
  drops the user straight to `/login` instead of refreshing. Not recorded in FEATURES.md row 1.9,
  which describes only the login path.
- **Automation:** planned — `gocrm-ui/e2e/tests/registration.spec.ts` (extended)

### TC-AUTH-011 — Submit button shows a loading state while registration is in flight
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/register`, form filled from `generateTestUser()`.
- **Steps:** Click **Sign Up** and observe the button.
- **Expected:** The button label changes to "Creating account..." and is disabled until the request
  settles.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "shows loading state during submission"

### TC-AUTH-012 — A network failure during registration shows an error instead of hanging
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Logged out, on `/register`, form filled from `generateTestUser()`.
- **Steps:** Abort or fail the `**/auth/register` route via `page.route`, then click **Sign Up**.
- **Expected:** The page stays on `/register` and shows the fallback alert "Registration failed.
  Please try again." (`Register.tsx` `onSubmit` catch branch).
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "handles network error gracefully"

### TC-AUTH-013 — Field values survive a failed validation pass
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/register`.
- **Steps:** Fill every field but give a weak password, submit, then inspect the inputs.
- **Expected:** First Name, Last Name and Email keep the values that were typed; only the offending
  field carries an error.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "preserves form data on validation error"

### TC-AUTH-014 — Editing a field clears its validation message
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/register`, at least one field in error after a submit attempt.
- **Steps:** Type a valid value into the field that shows an error.
- **Expected:** The helper text disappears once the field re-validates.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "clears error messages when field is edited"

### TC-AUTH-015 — Registration and login pages link to each other
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out.
- **Steps:**
  1. From `/register`, click "Already have an account? Sign In".
  2. From `/login`, click "Don't have an account? Sign Up".
- **Expected:** Step 1 lands on `/login`, step 2 lands on `/register`; both are unguarded top-level
  routes in `routes/index.tsx`.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "can navigate to login page"
  (the reverse direction is `gocrm-ui/e2e/tests/login.spec.ts` "can navigate to registration page")

---

## Login (FEATURES 1.2)

### TC-AUTH-016 — Login page renders its controls
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out.
- **Steps:** Navigate to `/login`.
- **Expected:** "Sign In" heading, `input[name="email"]`, `input[name="password"]`, the "Remember me"
  checkbox, the **Sign In** submit button, "Forgot password?" and "Don't have an account? Sign Up"
  links are all visible. Matches `docs/screenshots/auth/01-login.png`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "login page renders correctly"

### TC-AUTH-017 — Valid credentials log in and land on the dashboard
- **Priority:** P0
- **Type:** functional
- **Preconditions:** The seeded admin from `global-setup.ts` exists (`testAdminCredentials`).
- **Steps:**
  1. Go to `/login`.
  2. Fill Email Address and Password with `testAdminCredentials`.
  3. Click **Sign In**.
- **Expected:** `POST /api/v1/auth/login` returns **200** with `data.token`, `data.refresh_token` and
  `data.user`; the SPA navigates to `/`; `AuthContext` then issues `GET /users/me` (200) to hydrate
  the user.
- **Known issue:** The existing spec hardcodes `admin@gophercrm.local` / `GopherCRM2024!`, which
  `global-setup.ts` does not seed, and asserts the JWT in `localStorage` while leaving "Remember me"
  unticked — the app writes to `sessionStorage` in that case (`client.ts` `writeToken`). The same
  wrong assumption is in `gocrm-ui/e2e/helpers/admin-auth.ts:47`;
  `gocrm-ui/e2e/screenshots/helpers/login.ts` documents the trap and ticks the box instead. A
  rewrite should use `testAdminCredentials` and assert the redirect, not a storage key.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "successful login redirects to dashboard"

### TC-AUTH-018 — A wrong password keeps the user on the login page
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Seeded admin exists.
- **Steps:** Submit the admin email with `WrongPassword123!`.
- **Expected:** `POST /auth/login` returns **401** with message `Invalid email or password`; the page
  shows that text in a MUI error alert and the URL still contains `/login`. No token is written to
  either storage. `client.ts` deliberately skips the refresh interceptor for URLs containing
  `/auth/`, so there is no redirect loop.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "login fails with wrong password"

### TC-AUTH-019 — An unknown email produces the same generic failure
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logged out. `const user = generateTestUser()` — never registered.
- **Steps:** Submit that email with any password.
- **Expected:** **401** with the identical `Invalid email or password` message and the identical
  rendered alert as TC-AUTH-018. `authenticate()` always runs a bcrypt comparison against a dummy
  hash for unknown emails (`internal/service/auth_service.go:122-145`), so neither the message nor
  the timing distinguishes the two cases. Do not assert a distinguishable error — indistinguishability
  is the requirement.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "login fails with non-existent email"

### TC-AUTH-020 — Empty login fields are blocked client-side
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Logged out, on `/login`.
- **Steps:** Click **Sign In** with both fields empty.
- **Expected:** No request is sent; the fields report their zod errors ("Invalid email address" /
  "Password is required"). Matches `docs/screenshots/auth/02-login-validation.png`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "login fails with empty fields"

### TC-AUTH-021 — A malformed email is blocked client-side
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Logged out, on `/login`.
- **Steps:** Enter `invalid-email` plus any password and submit.
- **Expected:** "Invalid email address" under the Email field; no `POST /auth/login`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "login fails with invalid email format"

### TC-AUTH-022 — Login password visibility toggle works
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Logged out, on `/login`.
- **Steps:** Type a password, click `toggle password visibility`, click it again.
- **Expected:** The input `type` cycles `password` → `text` → `password`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "password visibility toggle works"

### TC-AUTH-023 — Enter submits the login form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Seeded admin exists; on `/login` with both fields filled.
- **Steps:** Focus the Password field and press `Enter`.
- **Expected:** `POST /auth/login` fires and returns **200**; the SPA navigates to `/`. The submit is
  the native form submission wired through `handleSubmit(onSubmit)`.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "form can be submitted with Enter key"

### TC-AUTH-024 — Login returns the user to the page they were deep-linked to
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged out with both storages cleared. Seeded admin exists.
- **Steps:**
  1. Navigate directly to `/customers`.
  2. Observe the redirect to `/login`.
  3. Log in as the seeded admin.
- **Expected:** After login the SPA lands on `/customers`, not `/`. `ProtectedRoute` passes
  `state={{from: location}}` on its `Navigate`, and `Login.tsx` reads
  `location.state?.from?.pathname` into `from` and calls `navigate(from, {replace: true})`.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-025 — A deactivated account fails login with the ordinary credentials error
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin. `const u = generateUserData()`.
- **Steps:**
  1. Create `u` via `/users/new` (role `sales`), then open `/users/:id/edit` and clear the Active
     flag.
  2. Log out.
  3. Log in as `u`.
- **Expected:** **401** with message `Invalid email or password` — byte-identical to the wrong-password
  response. `authenticate()` checks `IsActive` only *after* the bcrypt comparison succeeds and reuses
  the same error (`internal/service/auth_service.go:180-184`), deliberately so the account's state is
  not disclosed. The login page shows the same alert as any other failure.
- **Known issue:** None — this is intended anti-enumeration behaviour, documented in the `Login`
  swagger annotation. A case expecting "account disabled" would be wrong.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended; needs the admin user-management
  page to deactivate)

---

## Account lockout (FEATURES 1.7)

### TC-AUTH-026 — Five consecutive failures lock the account for 15 minutes
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin; create `const u = generateUserData()` via
  `/users/new`, then log out. Backend started with `DISABLE_RATE_LIMIT=true`, or each attempt spaced
  ≈6 s apart.
- **Steps:**
  1. Submit the login form five times with `u.email` and a wrong password.
  2. Submit a sixth time with the **correct** password.
- **Expected:** All six attempts return **401** with `Invalid email or password`. The fifth failure
  sets `locked_until = now + 15m` and the sixth is rejected by the lock check that runs *before* the
  password result is examined (`internal/service/auth_service.go:152-178`), so a correct password
  does not unlock the account. The error text never mentions a lock.
- **Known issue:** The strict tier (10/min, burst 5) sits in front of `/auth/login`, so an
  un-throttled run of six rapid attempts returns **429** ("Too many requests. Please try again
  later.") before lockout can be observed. Spacing or `DISABLE_RATE_LIMIT=true` is mandatory — see
  FEATURES.md row 11.2.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new lockout describe block)

### TC-AUTH-026a — Expiry starts a fresh failure window
- **Priority:** P0
- **Expected:** A lock remains active strictly before `locked_until`. At or after that
  instant, a wrong password persists one failed attempt and clears the expired lock.
  Five new failures create a new 15-minute lock. A successful login clears the counter
  and lock in storage. Unknown, incorrect, locked and inactive accounts retain the same
  public **401** with `Invalid email or password`.
- **Automation:** `internal/service/auth_lockout_test.go` uses deterministic time at
  one nanosecond before, exactly at and one nanosecond after expiry. It checks repeated
  failures and successful reset without waiting 15 minutes. The real HTTP/SQLite case
  `AuthSessionIntegrationTestSuite.TestLoginFailureResponsesRemainGeneric` verifies
  equivalent failure responses and persisted state through the session login endpoint.
- **Validation:** the [auth profile](../VALIDATION_RELEASE.md) runs these regressions,
  the surrounding auth/session checks, and health/login/registration browser specs.

---

## Token storage & remember me (FEATURES 1.9)

### TC-AUTH-027 — Without "Remember me" the tokens go to sessionStorage
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Fresh browser context, both storages empty. Seeded admin exists.
- **Steps:**
  1. Log in as the seeded admin leaving "Remember me" unticked.
  2. Read `gophercrm_token` and `gophercrm_refresh_token` from both storages.
- **Expected:** Both tokens are in `sessionStorage`; both are absent from `localStorage`.
  `AuthContext.login()` passes
  `persist = Boolean(data.remember_me)` into `setToken` / `setRefreshToken`, and `writeToken`
  removes the key from the other storage before writing.
- **Known issue:** Any helper that asserts `localStorage.getItem('gophercrm_token')` after an
  unticked login fails — `gocrm-ui/e2e/helpers/admin-auth.ts:47` still does exactly that.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new)

### TC-AUTH-028 — With "Remember me" the tokens go to localStorage
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Fresh browser context, both storages empty. Seeded admin exists.
- **Steps:**
  1. Log in as the seeded admin with `input[name="remember_me"]` checked.
  2. Read both storages.
  3. Open a new page in the same context and navigate to `/`.
- **Expected:** Both tokens are in `localStorage` and absent from `sessionStorage`. The new page
  is authenticated without a further login, because `readToken` falls back from `sessionStorage`
  to `localStorage`.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new)

---

## Logout (FEATURES 10b, gap G11)

### TC-AUTH-029 — Logout calls the backend, clears both storages and returns to /login
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin (any remember-me choice).
- **Steps:**
  1. Click the avatar button in the top bar to open the user menu.
  2. Click the **Logout** menu item.
- **Expected:** `POST /api/v1/auth/logout` fires with the `Authorization: Bearer` header and returns
  **200** with `data.message = "Logged out successfully"`. The SPA navigates to `/login`;
  `gophercrm_token` and `gophercrm_refresh_token` are gone from *both* `localStorage` and
  `sessionStorage` (`apiClient.clearTokens()` loops over both). Navigating back to `/` redirects to
  `/login`.
- **Known issue:** FEATURES.md row 1.4 still says "There is no server-side logout endpoint"; that row
  is superseded by section 10b — the route is registered at `cmd/main.go:211` behind the auth
  middleware. Closes gap **G11**.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new logout describe block)

### TC-AUTH-030 — Logout revokes the refresh token, so it cannot be replayed
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin with "Remember me" ticked.
- **Steps:**
  1. Capture `localStorage.gophercrm_refresh_token`.
  2. Log out via the user menu.
  3. From an `APIRequestContext`, `POST /api/v1/auth/refresh` with `{refresh_token: <captured>}`.
- **Expected:** The refresh call returns **401** with `Invalid or expired refresh token`. A logout
  with no body revokes every refresh token of the user (`AuthHandler.Logout`).
- **Known issue:** The access token issued before logout keeps working until it expires — there is no
  JWT blocklist (`docs/ROADMAP.md`, "Access-token blocklist"). A case asserting that the old JWT is
  rejected immediately would be wrong.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new)

---

## Token refresh (FEATURES 10b, gap G12)

### TC-AUTH-031 — A 401 on a data request triggers a silent refresh and a retry
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin with "Remember me" ticked, on `/customers`.
- **Steps:**
  1. Overwrite `localStorage.gophercrm_token` with a syntactically valid but bogus JWT, leaving the
     refresh token intact.
  2. Reload or trigger a list fetch (e.g. navigate to `/leads`).
- **Expected:** The first `GET` returns **401**; the client then issues `POST /auth/refresh` through a
  bare axios instance (no `Authorization` header) and gets **200**; the original request is replayed
  with the new bearer token and succeeds; the page renders data without a visit to `/login`. Both
  `gophercrm_token` and the rotated `gophercrm_refresh_token` in storage differ from their previous
  values — rotation is strict, so the replacement must be persisted.
- **Known issue:** FEATURES.md row 1.3 ("Not implemented", "no `/auth/refresh` route is registered")
  is stale; the route exists at `cmd/main.go:188` and the interceptor is wired in
  `gocrm-ui/src/api/client.ts`. Closes gap **G12**.
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new)

### TC-AUTH-032 — A failed refresh logs the user out and hard-redirects to /login
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin, on any list page.
- **Steps:**
  1. Overwrite both `gophercrm_token` and `gophercrm_refresh_token` with garbage.
  2. Navigate to `/customers`.
- **Expected:** The data request 401s, `POST /auth/refresh` 401s, and the interceptor's catch branch
  runs `clearTokens()` followed by `window.location.href = '/login'` — a full page load, not a
  React Router navigation. Both storages end up empty.
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new)

### TC-AUTH-033 — Refresh keeps the rotated tokens in the storage the session already used
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Logged in as the seeded admin **without** "Remember me" (tokens in
  `sessionStorage`), on a list page.
- **Steps:**
  1. Invalidate only `sessionStorage.gophercrm_token`.
  2. Trigger a data request and wait for the refresh round-trip.
- **Expected:** The new access and refresh tokens land back in `sessionStorage`, and `localStorage`
  stays empty. `client.ts` derives `persist` from
  `sessionStorage.getItem(REFRESH_TOKEN_KEY) === null`, so a non-remembered session is not silently
  promoted to a persistent one.
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new)

### TC-AUTH-033a — Refresh rotation is atomic and single-use
- **Priority:** P0
- **Type:** regression
- **Expected:** `POST /auth/refresh` rotates in one transaction (`internal/service/auth_service.go`
  `RefreshAccessToken`, `internal/repository/refresh_token_repository.go` `ClaimForRotation`). The
  presented token is looked up, then claimed by a conditional update that revokes the row only
  while it is still not revoked and strictly unexpired and must affect exactly one row; the owner
  is re-read with a locking read (the transaction-bound `UserRepository`, `FOR UPDATE` on MySQL, a
  plain read on SQLite); the successor row is inserted and its access JWT signed, all before the
  commit. A failure of the successor insert or of the signing rolls everything back, the claim
  included: the presented token stays usable, no extra row exists, no credentials are returned,
  and the same token rotates once the fault is gone. An unknown, revoked or expired token, one
  that expires or is revoked between the lookup and the claim, and a missing transaction manager
  all return the same generic **401** `Invalid or expired refresh token` and leave the committed
  state unchanged. An inactive, erased (soft-deleted) or missing owner — including one deactivated
  by another connection after the token lookup, which the locking read sees and a plain read under
  REPEATABLE READ would not — is refused with the same 401, no successor is created and the claim
  is committed as the containment revocation of that token: reactivating the account does not
  bring it back, while the owner's other sessions are untouched. Competing refreshes presenting
  one token commit exactly one successor: the winner receives a working access token and the
  only live successor, every loser gets the generic 401 and no credentials, and a later replay
  neither adds a row nor invalidates the winner. Logout stays idempotent around a rotation: logging
  out with the spent token, an unknown token or someone else's token is a no-op success, logging
  out with the successor revokes exactly that session and a second logout still succeeds.
- **Known issue:** several parallel 401s in the SPA still each send a refresh; the backend rejects
  all but one and the client-side coalescing is deferred (`docs/ROADMAP.md`, "Concurrent-refresh
  stampede", #115). A JWT issued before a rotation or logout stays valid until it expires.
- **Automation:** `internal/service/auth_refresh_rotation_regression_test.go` runs every scenario
  on in-memory SQLite with the production one-connection pool and on a disposable MySQL schema
  created from `TEST_MYSQL_DSN` (a configured but unreachable server fails; an absent variable
  skips, which the named acceptance checks reject). `TestRefreshRotationValidation{SQLite,MySQL}`:
  unknown/empty/revoked/expired tokens, expiry and revocation between lookup and claim through
  the transaction's own handle (rejected, rolled back, same token rotates afterwards), inactive,
  erased, soft-deleted-with-surviving-token and missing owners, an owner deactivated or
  soft-deleted through the transaction's handle between the lookup and the owner check, on MySQL
  (two connections) an owner deactivated by a second connection after the token lookup, refusal
  without a transaction manager, and a committed rotation with the logout idempotence cases.
  `TestRefreshRotationAtomicity{SQLite,MySQL}`: an injected successor-insert failure (through the
  transaction-bound repository) and an injected signing failure (an instance-local seam of the
  service under test) leave the presented token live with no extra row and no returned
  credentials, then the same token rotates; a committed rotation yields a validating access
  token, a chain of successors keeps exactly one live, the sibling session and a bystander are
  untouched; an inactive owner's token is contained and stays revoked after reactivation.
  `TestRefreshRotationConcurrent{SQLite,MySQL}`: three competitors presenting one token — held
  after the lookup on MySQL's multi-connection pool, serialised by the single connection on
  SQLite — produce one success, one committed successor, two generic rejections without
  credentials, a rejected replay that leaves the winner live, and a winner that rotates again,
  under the race detector with every wait bounded. The conditional claim itself is pinned by
  `internal/repository/refresh_token_repository_test.go`
  (`TestRefreshTokenRepository_ClaimForRotation_ClaimsOnlyLiveTokens`,
  `_RollsBackWithTheTransaction`, `TestRefreshTokenRepository_RevokeByTokenHash_IsUnconditionalAndIdempotent`);
  the call sequence, the lost claim, the post-claim failures and the refusal without a
  transaction manager by `internal/service/auth_session_test.go`
  `TestAuthService_RefreshAccessToken_*`.
- **Validation:** the three `refresh-rotation-*` named acceptance checks run the six roots with
  `-race` and reject skipped evidence. `scripts/ci/check-auth.py` discovers these roots from the
  `auth*_test.go` pattern, so the bounded auth profile needs `TEST_MYSQL_DSN` for them as it
  already does for the reset roots.

---

## Password reset & change (FEATURES 10b)

### TC-AUTH-034 — "Forgot password?" leads to the 404 page
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged out, on `/login`.
- **Steps:** Click the "Forgot password?" link.
- **Expected:** The URL becomes `/forgot-password` and the SPA renders the catch-all `NotFound` page —
  the "404" heading and "Page Not Found". `routes/index.tsx` declares no `/forgot-password` route, so
  the `path: '*'` entry matches.
- **Known issue:** The login page links to a page that does not exist. `docs/ROADMAP.md`, "UI pages
  for the new auth flows": `authApi.requestPasswordReset` and `resetPassword` are real endpoints but
  no page calls them.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-035 — A reset link from the email lands on the 404 page
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged out.
- **Steps:** Navigate directly to `/reset-password?token=anything`.
- **Expected:** The `NotFound` page renders. The backend builds this link from `APP_BASE_URL` in
  `internal/mailer`, but the SPA has no matching route, so a real reset email is a dead end in the UI.
- **Known issue:** `docs/ROADMAP.md` — "the reset email links to `/reset-password?token=...`, which
  has no route in the SPA yet".
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-036 — Requesting a password reset answers identically for known and unknown emails
- **Priority:** P1
- **Type:** negative
- **Preconditions:** The seeded admin exists; `const ghost = generateTestUser()` is never registered.
- **Steps:** From an `APIRequestContext`, `POST /api/v1/auth/password-reset` twice — once with the
  admin email, once with `ghost.email`.
- **Expected:** Both return **200** with the same body:
  `data.message = "If an account exists for that email, a password reset link has been sent"`.
  `RequestPasswordReset` logs internal failures and never varies the response.
- **Automation:** blocked — no UI entry point exists (TC-AUTH-034); an API-only assertion belongs in
  the Go integration suite (`tests/auth_session_integration_test.go`) rather than in Playwright

### TC-AUTH-037 — Complete a password reset end to end
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A user created by the test.
- **Steps:** Request a reset, read the token out of the delivered mail, open the reset link, set a new
  policy-compliant password, then log in with it.
- **Expected:** The token is single-use with a 1 h expiry; confirming returns **200**, clears any
  lockout and revokes every refresh token of the account in the same transaction that writes the
  password; the old password no longer authenticates and a replay of the link returns the generic
  **400**.
- **Automation:** blocked — needs mail capture (with `SMTP_HOST` unset the mailer only writes a
  redacted log line) *and* the missing `/reset-password` SPA route. The API round trip is
  covered in Go by `tests/auth_session_integration_test.go` `TestPasswordResetRoundTrip`.

### TC-AUTH-037a — Reset confirmation is atomic and single-use
- **Priority:** P0
- **Type:** regression
- **Expected:** `POST /auth/password-reset/confirm` validates the complexity policy and hashes the
  new password before anything is written, so a weak password, or one bcrypt refuses (over 72
  bytes), leaves the link usable. The token claim — a conditional update that stamps `used_at`
  only while the row is unused and strictly unexpired and must affect exactly one row — the owner
  re-check, the password and lockout update and the revocation of every refresh token of the
  account then run in one transaction (`internal/service/auth_service.go`
  `ConfirmPasswordReset`, `internal/repository/password_reset_token_repository.go` `MarkUsed`).
  The owner re-check is a locking read (the transaction-bound `UserRepository`, `FOR UPDATE` on MySQL,
  a plain read on SQLite): it sees the account as currently committed, so an owner deactivated or
  erased by another connection after the token lookup is refused instead of being reactivated by
  the password update written from a stale REPEATABLE READ snapshot.
  A failure of the claim, of the password update or of the revocation is returned, rolls the
  claim back and the same link works on retry. An unknown, spent or expired token, one that
  expires or is spent between the lookup and the claim, and an inactive, erased (soft-deleted)
  or missing owner all return the same generic **400** `Invalid or expired reset token` and
  leave the committed state unchanged: token unspent, old password, lockout and sessions as
  they were, unrelated accounts untouched. Competing confirmations of one link with different
  valid passwords commit exactly once: the winner's password is stored, every prior session of
  the owner is revoked, the losers are told the link is invalid and did not change the
  password, and a replay cannot alter that outcome. A successful reset clears the lockout, so
  the owner signs in immediately with only the new password.
- **Known issue:** a policy-compliant password longer than bcrypt's 72-byte limit passes the
  handler's `min=10` binding and the complexity check, is refused by the service without
  spending the token, and is reported as **500** rather than 400 because the handler classifies
  only the token sentinel. The link stays usable.
- **Automation:** `internal/service/auth_password_reset_regression_test.go` runs every scenario
  on in-memory SQLite with the production one-connection pool and on a disposable MySQL schema
  created from `TEST_MYSQL_DSN` (a configured but unreachable server fails; an absent variable
  skips, which the named acceptance checks reject). `TestPasswordResetValidation{SQLite,MySQL}`:
  weak and unhashable passwords with same-link completion, unknown/empty/spent/expired tokens,
  expiry and spend between lookup and claim through the transaction's own handle, inactive,
  erased, soft-deleted-with-surviving-token and missing owners, and an owner deactivated or
  soft-deleted through the transaction's handle between the lookup and the owner check (rejected,
  rolled back, same link completes afterwards). The stale-snapshot case that needs a second
  connection is `TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL`
  (`internal/service/auth_reset_owner_race_test.go`): a REPEATABLE READ transaction reads the
  token, another connection commits `is_active = false`, and the reset must reject, leave the
  account disabled with its old password and the token unspent.
  `TestPasswordResetAtomicity{SQLite,MySQL}`: injected claim, password-update and
  session-revocation failures leave everything as seeded and the same token succeeds on retry;
  success clears the lockout, revokes both owner sessions, leaves a bystander's password, token
  and session alone and permits login only with the new password; replay is rejected.
  `TestPasswordResetConcurrent{SQLite,MySQL}`: three competitors with different passwords —
  held after the lookup on MySQL's multi-connection pool, serialised by the single connection
  on SQLite — produce one commit, one spent token, the winner's password, revoked sessions and
  a rejected replay, under the race detector with every wait bounded. The conditional claim
  itself is pinned by `internal/repository/password_reset_token_repository_test.go`
  (`TestPasswordResetTokenRepository_MarkUsed_ClaimsOnlySpendableTokens`,
  `_RollsBackWithTheTransaction`); the call sequence and the refusal without a transaction
  manager by `internal/service/auth_session_test.go` `TestAuthService_ConfirmPasswordReset`.
- **Validation:** the three `password-reset-*` named acceptance checks run the six roots with
  `-race` and reject skipped evidence. `scripts/ci/check-auth.py`, the
  [auth profile](../VALIDATION_RELEASE.md) checker, discovers these roots from the
  `auth*_test.go` pattern and also fails on a skipped root, so the bounded auth profile now
  requires `TEST_MYSQL_DSN` pointing at a disposable MySQL server.

### TC-AUTH-038 — Change the password of the signed-in account
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as a user the test created.
- **Steps:** Open a change-password form, supply the current and a new password, submit.
- **Expected:** `POST /auth/change-password` returns **200** with "Password changed successfully" and
  revokes all refresh tokens; a wrong current password returns **400** with "The current password is
  incorrect" (not 401 — the session presenting it is still authenticated).
- **Automation:** blocked — `authApi.changePassword` exists but no page calls it; there is no UI to
  drive (`docs/ROADMAP.md`, "UI pages for the new auth flows")

---

## Protected routes & session bootstrap (FEATURES 11.1)

### TC-AUTH-039 — An unauthenticated visitor is redirected to the login page
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Both storages cleared.
- **Steps:** Navigate directly to `/users`.
- **Expected:** The URL becomes `/login`. `ProtectedRoute` sees `isAuthenticated === false` and
  renders `<Navigate to="/login" replace>`; no API request for user data is made.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "unauthenticated user is redirected to login"

### TC-AUTH-040 — An authenticated admin reaches an admin-only route
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** Seeded admin exists.
- **Steps:** Log in as the admin, then navigate to `/users`.
- **Expected:** The user list renders and the URL does not contain `/login` or `/unauthorized`.
  `/users` sits under the pathless `ProtectedRoute requiredRole="admin"` layout route in
  `routes/index.tsx`, and `GET /api/v1/users` passes its `RequireRole(admin)` guard
  (`internal/handler/routes.go:13`).
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "logged in user can access protected routes"

### TC-AUTH-041 — Non-admin roles are bounced from /users by the client before the API is called
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Logged in as the seeded admin; create `const s = generateUserData()` with role
  `sales` and another with role `support` via `/users/new`; separately self-register a `customer`
  through `/register`. Log out between roles.
- **Steps:** For each of `sales`, `support`, `customer`: log in, then navigate directly to `/users`.
- **Expected:** Every role is redirected to `/unauthorized`, which shows the "Access Denied" page.
  No `GET /api/v1/users` request is issued at all — the route guard resolves before the page loads.
  Issuing the same `GET /api/v1/users` from an `APIRequestContext` with that role's bearer token
  returns **403**, so client gating and server guard agree on the outcome but not on the mechanism:
  the UI never produces the 403 that the API would.
- **Automation:** planned — `gocrm-ui/e2e/tests/rbac-routes.spec.ts` (new; no role-login helper
  exists yet, so the spec must create the users and log in through the form)

### TC-AUTH-042 — A stale token in storage is discarded and the visitor sent to login
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged out; seed `localStorage.gophercrm_token` with an expired or malformed JWT
  and leave `gophercrm_refresh_token` unset.
- **Steps:** Navigate to `/`.
- **Expected:** `AuthContext.loadUser()` calls `GET /users/me`, receives **401**, and clears both
  storages; `ProtectedRoute` then redirects to `/login`. A spinner is shown while `isLoading` is
  true — the redirect must not be asserted before it resolves.
- **Known issue:** `loadUser` clears tokens on **403** as well as 401, so a legitimately authenticated
  user who is forbidden from `/users/me` would also be logged out. Not reachable today, since
  `GET /users/me` carries no `RequireRole` guard.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-043 — A page reload keeps the session alive
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin without "Remember me", on `/customers`.
- **Steps:** Reload the page.
- **Expected:** `GET /api/v1/users/me` returns **200**, the avatar and navigation re-render with the
  admin's initials, and the URL stays `/customers`. Tokens remain in `sessionStorage` only.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-044 — A transient non-auth error does not log the user out
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Logged in as the seeded admin.
- **Steps:**
  1. Intercept `**/users/me` and answer with **429** once.
  2. Reload the page.
- **Expected:** Tokens stay in storage and the user is not redirected to `/login`. `loadUser` clears
  tokens only for status 401 or 403; every other failure, including the rate limiter's 429, leaves
  the stored credentials alone.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (extended)

### TC-AUTH-045 — Public registration ships disabled and the API refuses sign-up
- **Priority:** P0
- **Type:** security
- **Preconditions:** `security.allow_public_registration` at its shipped default (`false`), or set
  to `false` via `PUT /api/v1/configurations/security.allow_public_registration` as admin.
- **Steps:**
  1. `POST /api/v1/auth/register` with a valid registration payload.
- **Expected:** **403** `{success:false}` ("Public registration is disabled"); no account is
  created. The gate fails closed: a missing configuration row or a configuration read error
  answers the same 403.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "a direct POST /auth/register is refused with 403"; Go: `internal/middleware/registration_test.go`, `tests/auth_integration_test.go` `TestRegisterEndpoint_GatedByConfiguration`

### TC-AUTH-046 — GET /auth/registration reports the sign-up switch without authentication
- **Priority:** P1
- **Type:** functional
- **Preconditions:** None (public endpoint, strict rate tier).
- **Steps:**
  1. `GET /api/v1/auth/registration` with no `Authorization` header, with the switch off, then on.
- **Expected:** **200** `{success:true, data:{enabled:false}}` and `{enabled:true}` respectively.
  A configuration read failure is reported as `enabled:false`, matching what the gate enforces.
- **Automation:** automated — Go: `internal/handler/auth_registration_test.go`

### TC-AUTH-047 — Login page hides the Sign Up link while registration is disabled
- **Priority:** P1
- **Type:** functional
- **Preconditions:** `security.allow_public_registration` = `false`.
- **Steps:**
  1. Go to `/login` and wait for the `GET /auth/registration` probe to answer.
- **Expected:** The "Forgot password?" link renders; "Don't have an account? Sign Up" does not.
  The link also stays hidden while the probe is in flight or failing — it appears only on a
  confirmed `enabled:true`.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "login page offers no sign-up link"; Vitest: `src/pages/auth/Login.test.tsx`

### TC-AUTH-048 — /register shows a disabled notice instead of the form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** `security.allow_public_registration` = `false`.
- **Steps:**
  1. Go to `/register` directly.
- **Expected:** An info alert "Registration is disabled on this system…" with a "Back to Sign In"
  link; no form fields render, so nothing can be submitted. With the switch on, the form renders
  as in TC-AUTH-001.
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "the register page shows the disabled notice instead of the form"; Vitest: `src/pages/auth/Register.gating.test.tsx`
