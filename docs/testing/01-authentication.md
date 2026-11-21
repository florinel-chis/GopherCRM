# Authentication & Session Management — Test Cases

Playwright E2E test cases for registration, login, account lockout, token storage, logout, token
refresh, password reset and protected-route gating. Every **Expected** section records what the
application does **today**, including behaviour that is defective or surprising; where that is the
case a **Known issue** line names it. Cases marked *automated* were verified by reading the spec
file, not by trusting the coverage matrix.

**Sources**

- `docs/FEATURES.md` section 1 (rows 1.1–1.17), section 10b "Auth sessions", section 11.1, Gap
  Summary G11/G12
- `docs/ROADMAP.md` — "Follow-ups from the backend build-out" (missing SPA reset route,
  access-token revocation beyond the session-ending events, concurrent-refresh stampede)
- `gocrm-ui/src/pages/auth/Login.tsx`, `gocrm-ui/src/pages/auth/Register.tsx`
- `gocrm-ui/src/contexts/AuthContext.tsx`, `gocrm-ui/src/api/client.ts`,
  `gocrm-ui/src/api/endpoints/auth.ts`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/components/ProtectedRoute.tsx`,
  `gocrm-ui/src/layouts/MainLayout.tsx`
- `internal/handler/auth_handler.go`, `internal/handler/routes.go`,
  `internal/service/auth_service.go`, `internal/repository/password_reset_token_repository.go`,
  `internal/repository/refresh_token_repository.go`,
  `internal/service/auth_password_reset_regression_test.go`,
  `internal/service/auth_refresh_rotation_regression_test.go`,
  `internal/service/auth_change_password_regression_test.go`, `internal/middleware/rate_limit.go`,
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
- `/auth/*` is behind `RateLimitStrict()` — 10 req/min, burst 5 (`RateLimitStrict` (`internal/middleware/rate_limit.go`),
  applied in `setupDependencies` (`cmd/main.go`)). Any case that issues more than five auth requests in quick
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
  field (`internal/handler/auth_handler.go` `RegisterRequest`), and the handler hard-codes
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
  `user with this email already exists` (the literal text that
  `AuthHandler.Register` (`internal/handler/auth_handler.go`) passes to `utils.RespondConflict`, surfaced by `Register.tsx` as
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
- **Expected:** `localStorage.gophercrm_session` holds the record with `refresh: null` and
  `persist: true`, `localStorage.gophercrm_token` is set and `sessionStorage.gophercrm_token` is
  null — `AuthContext.register()` calls `apiClient.startSession(response.token,
  response.refresh_token, true)`. Neither storage holds a refresh token: the `Register` handler
  builds an `AuthResponse` without `RefreshToken`, so `refresh_token` is absent from the 201 body,
  and the record's `refresh: null` is never filled from the other storage.
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
  wrong assumption is in `gocrm-ui/e2e/helpers/admin-auth.ts` (`createAndLoginAdmin`);
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
  hash for unknown emails (`internal/service/auth_service.go` `authService.authenticate`), so neither the message nor
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
  the same error (`internal/service/auth_service.go` `authService.authenticate`, and again under the owner lock,
  TC-AUTH-026b), deliberately so the account's state is
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
  password result is examined (`internal/service/auth_service.go` `authService.authenticate`), so a correct password
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
  `TestLoginFailedAttemptsConcurrent{SQLite,MySQL}`
  (`internal/service/auth_login_failed_attempts_test.go`) sends parallel wrong passwords
  that have all read the account first and checks the stored count is exact: 4 failures
  leave 4, 20 leave 5 with the lock set, and 3 after an expired lock leave 3 with no lock.
- **Validation:** the [auth profile](../VALIDATION_RELEASE.md) runs these regressions,
  the surrounding auth/session checks, and health/login/registration browser specs.

### TC-AUTH-026b — Login issues its session from the owner row locked in one transaction (#336)
- **Priority:** P0
- **Type:** regression (concurrency)
- **Preconditions:** A service wired with `NewAuthServiceWithSessions` (as `cmd/main.go` does).
- **Steps / Expected:** the email lookup and bcrypt run before the transaction; the competing write
  below commits after that lookup and before the owner's `FOR UPDATE` read by id.
  1. The password hash changed (a password change or reset), `is_active` was set false, or the
     account was erased: the login answers `invalid credentials` (**401** `Invalid email or
     password` over HTTP), inserts no `refresh_tokens` row and leaves `last_login_at`,
     `failed_login_attempts` and `locked_until` as stored.
  2. `locked_until` was set after the login clock: the lockout error, nothing written.
  3. `token_version` was incremented (a logout): the login succeeds and its access JWT carries the
     new value, which `ValidateToken` accepts.
  4. A login that commits first: a password change, a reset or a logout that follows revokes its
     refresh token and ends its access token (token_version mismatch); an erasure deletes the
     refresh token row and the access token's owner is gone; a deactivation revokes nothing, but
     the access token is refused by the `is_active` check and its refresh token answers the generic
     refresh rejection without inserting a successor.
  5. The refresh insert fails for a non-retryable reason: the login answers an access token with an
     empty refresh token and commits `last_login_at`; the signing fails: an error, nothing written;
     a deadlock, lock wait or SQLite busy on the first attempt: the login is replayed once and
     leaves exactly one refresh token.
  6. A refresh rotation raced against a reset, a deactivation and an erasure gives the outcomes of
     step 1 (rejected, no successor) or step 4 (for the successor), by ordering.
  No log line of these paths carries the email, a password, the hash or a token, and no ordering
  ends in MySQL error 1213. A service built by `NewAuthService` keeps the unlocked login.
- **Automation:** `internal/service/auth_login_owner_lock_test.go`
  (`TestLoginOwnerLock{Decisions,Atomicity}{SQLite,MySQL}`,
  `TestLoginConcurrentCredentialChanges{SQLite,MySQL}`,
  `TestRefreshRotationConcurrentCredentialChanges{SQLite,MySQL}`,
  `TestLoginOwnerLockLogsOmitPersonalData`, `TestLoginUnwiredServiceUnchanged`; the MySQL roots use
  a disposable `TEST_MYSQL_DSN` schema built by the real migrations and see each wait in
  `information_schema.PROCESSLIST` on two consecutive polls, the SQLite roots prove the same
  outcomes sequentially through the `loginBeforeLock` seam) and, over HTTP,
  `tests/auth_login_owner_lock_test.go` `TestLoginOwnerLockAPISQLite`.

---

## Token storage & remember me (FEATURES 1.9)

### TC-AUTH-027 — Without "Remember me" the tokens go to sessionStorage
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Fresh browser context, both storages empty. Seeded admin exists.
- **Steps:**
  1. Log in as the seeded admin leaving "Remember me" unticked.
  2. Read `gophercrm_session`, `gophercrm_token` and `gophercrm_refresh_token` from both storages.
- **Expected:** The session record `gophercrm_session` (`{v: 1, id, access, refresh, subject,
  persist: false}`) and both mirror keys are in `sessionStorage`; all three are absent from
  `localStorage`. `AuthContext.login()` passes `persist = Boolean(data.remember_me)` into
  `apiClient.startSession`, which removes the record and both keys from both storages before
  writing the record and then its mirrors (#184).
- **Known issue:** Any helper that asserts `localStorage.getItem('gophercrm_token')` after an
  unticked login fails — `gocrm-ui/e2e/helpers/admin-auth.ts` (`localStorage.getItem('gophercrm_token')`) still does exactly that.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new)

### TC-AUTH-028 — With "Remember me" the tokens go to localStorage
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Fresh browser context, both storages empty. Seeded admin exists.
- **Steps:**
  1. Log in as the seeded admin with `input[name="remember_me"]` checked.
  2. Read both storages.
  3. Open a new page in the same context and navigate to `/`.
- **Expected:** The record (`persist: true`) and both mirror keys are in `localStorage` and absent
  from `sessionStorage`. The new page is authenticated without a further login, because the
  client reads the record from `sessionStorage` first and, finding none there, from
  `localStorage`. Removing `localStorage.gophercrm_token` signs both pages out on their next read:
  a record whose `gophercrm_token` mirror is missing is no session.
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
  `gophercrm_session`, `gophercrm_token` and `gophercrm_refresh_token` are gone from *both*
  `localStorage` and `sessionStorage` (`apiClient.clearTokens()` loops over both). The record is
  removed at once, without waiting for a refresh another tab may be running under the session lock;
  that refresh then stores nothing (#184). Navigating back to `/` redirects to `/login`.
- **Known issue:** FEATURES.md row 1.4 still says "There is no server-side logout endpoint"; that row
  is superseded by section 10b — the route is registered in `setupDependencies` (`cmd/main.go`) behind the auth
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
- **Known issue:** None for the access token since #236: the logout also increments
  `users.token_version`, so the JWT captured before it answers **401** `Invalid credentials` at
  once, exactly like an expired token, while an API key of the account keeps working
  (`tests/auth_access_token_revocation_test.go` `TestAccessTokenRevocationAPISQLite`). A case may
  assert that rejection.
- **Automation:** planned — `gocrm-ui/e2e/tests/login.spec.ts` (new)

---

## Token refresh (FEATURES 10b, gap G12)

### TC-AUTH-031 — A 401 on a data request triggers a silent refresh and a retry
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Logged in as the seeded admin with "Remember me" ticked, on `/customers`.
- **Steps:**
  1. Overwrite the `access` field of the `localStorage.gophercrm_session` record with a
     syntactically valid but bogus JWT, leaving its `refresh` and `id` intact. (Overwriting the
     `gophercrm_token` mirror changes nothing: the client never reads tokens from the mirror keys
     while a record exists.)
  2. Reload or trigger a list fetch (e.g. navigate to `/leads`).
- **Expected:** The first `GET` returns **401**; the client then issues one `POST /auth/refresh`
  through a bare axios instance (no `Authorization` header) and gets **200**; the original request
  is replayed once, marked as retried, with the new bearer token and succeeds; the page renders data
  without a visit to `/login`. Every other request that hit a 401 meanwhile waits on the same
  refresh and is replayed with the same token — the page sends exactly one refresh. The record keeps
  its `id` while its `access` and rotated `refresh` (and the two mirror keys) differ from their
  previous values — rotation is strict, so the replacement must be persisted. A second tab of the
  same remembered session whose request was rejected meanwhile waits for the session lock, finds the
  successor in the record and replays with it without a second `POST /auth/refresh` (#184).
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new). The interceptor
  itself is covered by Vitest with the real interceptors (`gocrm-ui/src/api/client.refresh.test.ts`,
  "concurrent 401s share one refresh and replay every request with the new token"); two tabs by
  `gocrm-ui/src/api/client.crossTab.test.ts`.

### TC-AUTH-032 — A failed refresh logs the user out and hard-redirects to /login
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Logged in as the seeded admin, on any list page.
- **Steps:**
  1. Overwrite both the `access` and the `refresh` field of the stored `gophercrm_session` record
     with garbage, keeping its `id`.
  2. Navigate to `/customers`.
- **Expected:** The data request 401s, `POST /auth/refresh` 401s while the record still holds the
  token that was presented, and the client ends the session once: `clearTokens()` and then
  `window.location.href = '/login'` (skipped when already on `/login`) — a full page load, not a
  React Router navigation. Every request waiting on that refresh is rejected; none stays pending.
  Both storages end up empty, and another open tab of the session finds no session on its next
  request and goes to `/login` without calling `/auth/refresh`. A refresh that fails with a
  **503** (`SESSION_REFRESH_UNAVAILABLE`), a network error or no answer within 15 s (the wait for
  the session lock included) rejects the waiting requests but keeps the stored tokens and does not
  redirect.
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new). Vitest:
  `client.refresh.test.ts` "a failed refresh rejects every waiting request and signs out once",
  "a refresh that never answers settles every waiter after the timeout", "a 503 from refresh keeps
  the stored tokens and a later retry renews".

### TC-AUTH-033 — Refresh keeps the rotated tokens in the storage the session already used
- **Priority:** P2
- **Type:** regression
- **Preconditions:** Logged in as the seeded admin **without** "Remember me" (tokens in
  `sessionStorage`), on a list page.
- **Steps:**
  1. Invalidate only the `access` field of `sessionStorage.gophercrm_session`.
  2. Trigger a data request and wait for the refresh round-trip.
- **Expected:** The new access and refresh tokens land back in the `sessionStorage` record (same
  `id`, `persist: false`) and its mirrors, and `localStorage` stays empty. `client.ts` reads the
  session record whole from one storage and rewrites it there, so a non-remembered session is not
  silently promoted to a persistent one; a refresh token is never taken from the other storage.
- **Automation:** planned — `gocrm-ui/e2e/tests/session-refresh.spec.ts` (new)

### TC-AUTH-033a — Refresh rotation is atomic and single-use
- **Priority:** P0
- **Type:** regression
- **Expected:** `POST /auth/refresh` rotates in one transaction (`internal/service/auth_service.go`
  `authService.RefreshAccessToken`, `internal/repository/refresh_token_repository.go` `refreshTokenRepository.ClaimForRotation`). The
  presented token is looked up; the owner is re-read with a locking read (the transaction-bound
  `UserRepository`, `FOR UPDATE` on MySQL, a plain read on SQLite) before the token is claimed by
  a conditional update that revokes the row only while it is still not revoked and strictly
  unexpired and must affect exactly one row (users row before refresh_tokens rows, the order every
  session transaction follows); the successor row is inserted and its access JWT signed, all before the
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
  only live successor, every loser gets the generic 401 and no credentials; a loser that lost the
  claim while the winner's rotation was still running ends nothing. A token presented again after
  its rotation committed (#547) gets the same 401, adds no row and revokes every refresh token of
  its family, the successors included (also when it has expired meanwhile, and also when the
  successor was rotated on another connection while the request ran), so the successor is then
  refused too; the owner's other sessions and other accounts stay live. A revoked token that is the
  newest of its family (logout, containment) or has no family is refused and changes nothing. Logout stays idempotent around a rotation: logging
  out with the spent token, an unknown token or someone else's token revokes no refresh token and
  succeeds, logging out with the successor revokes exactly that session and a second logout still
  succeeds (every logout also ends the caller's earlier access tokens, #236).
- **Known issue:** a JWT issued before a rotation stays valid until it expires (a logout, a password
  change or a reset ends it, #236). The web client serialises the refreshes of tabs sharing a
  remembered session through the Web Lock `gophercrm-session` (#184); on plain HTTP, where browsers
  offer no Web Locks, two tabs can still present one token at once, and the loser adopts the
  winner's successor only if the winner stored it before the loser's 401 arrived. A loser that
  presented the token after the winner's refresh committed is a reuse (#547) and also ends the
  winner's successor, so both tabs are signed out at their next refresh. A reuse ends refresh
  tokens only: access tokens already issued stay valid until they expire.
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
  SQLite — produce one success, one committed successor and two rejections without credentials:
  on MySQL the losers lost the claim (`token_invalid`) and the winner stays live and rotates again
  before a replay of the original token ends the chain; on SQLite the losers ran after the commit
  (`token_reuse`) and the winner's successor ends with them. On MySQL a reuse held after its
  lookups while the successor rotates on another connection still ends the newest token (#547).
  `TestRefreshRotationValidation{SQLite,MySQL}` also covers the reuse, the reuse of an expired
  token, of an inactive owner's token and the refused successor afterwards, and the
  logout-revoked, contained and family-less tokens that end nothing. All of it runs under the race
  detector with every wait bounded. The conditional claim itself is pinned by
  `internal/repository/refresh_token_repository_test.go`
  (`TestRefreshTokenRepository_ClaimForRotation_ClaimsOnlyLiveTokens`,
  `_RollsBackWithTheTransaction`, `TestRefreshTokenRepository_RevokeByTokenHash_IsUnconditionalAndIdempotent`;
  the reuse lookups and the family revocation by `TestRefreshTokenRepository_GetRevokedByTokenHash_FindsOnlyRevokedRows`,
  `_HasLaterInFamily_MatchesUserFamilyAndGreaterID` and `_RevokeFamily_RevokesOnlyThatUsersFamily`);
  the call sequence, the lost claim, the post-claim failures and the refusal without a
  transaction manager by `internal/service/auth_session_test.go`
  `TestAuthService_RefreshAccessToken_*`.
- **Validation:** the three `refresh-rotation-*` named acceptance checks run the six roots with
  `-race` and reject skipped evidence. `scripts/ci/check-auth.py` discovers these roots from the
  `auth*_test.go` pattern, so the bounded auth profile needs `TEST_MYSQL_DSN` for them as it
  already does for the reset roots.

---

## Password reset & change (FEATURES 10b)

### TC-AUTH-033b — The refresh interceptor settles every request and respects the session
- **Priority:** P0
- **Type:** regression
- **Expected:** In one tab, all requests that hit a 401 share one refresh; on success each is
  replayed once (marked retried, so a 401 on the replay is final and starts no second refresh), on
  failure each is rejected, and a refresh that never answers rejects all of them after 15 s even
  when the transport ignores the abort, after which a new attempt works. A missing refresh token
  rejects every waiter without calling `/auth/refresh`. Each request carries the session it was
  sent under: after logout or a sign-in to another account, a late 401 is not replayed under the
  new session, and a late refresh answer is discarded instead of stored. Logout drops the stored
  tokens at once and waits for the refreshes and password changes still running; a successor one
  of them obtained (kept privately, never stored, and only if it belongs to the session being
  ended) authenticates `POST /auth/logout` (revoke every session), otherwise an expired access
  token is renewed privately once for the same purpose. Another tab replacing the shared session
  (sign-out, sign-in of another account) never makes this tab refresh, replay, write or revoke
  as the wrong account.
  Across tabs (#184): refreshes and password changes run under the origin-wide Web Lock
  `gophercrm-session` and re-read the session record once they hold it. Two tabs of one remembered
  session rejected at the same time — in either order — send exactly one `POST /auth/refresh`;
  both replay once with the successor and neither signs out, and the record keeps its session id.
  A tab that acquires the lock after another tab stored a successor adopts it without calling
  `/auth/refresh`. Without a shared lock, a refresh that loses to another tab adopts the successor
  if it is already stored (same session id, another refresh token), and clears nothing when the
  record belongs to another session (`SessionChangedError`). A 400/401 while the record still holds
  the presented token ends the session in that tab; the other tab's next request finds no session
  and signs out without calling `/auth/refresh`. Sign-out never waits for another tab's lock, and a
  refresh answered after it stores nothing. A refresh still queued for the lock at the 15 s deadline
  never runs and keeps the stored session.
- **Known issue:** other open tabs are not updated live on a sign-in or sign-out; they notice on
  their next request. On plain HTTP (no Web Locks) the lock does not span tabs.
- **Automation:** Vitest — `gocrm-ui/src/api/client.refresh.test.ts` (`ApiClient 401 refresh`,
  `ApiClient session storage`), driving the real interceptors through an injected adapter;
  `gocrm-ui/src/api/client.crossTab.test.ts` (two clients over shared storage and one memory lock),
  `gocrm-ui/src/api/client.sessionRecord.test.ts` and `gocrm-ui/src/api/sessionLock.test.ts`;
  `AuthContext.test.tsx` covers the stale `/users/me` answer, the stale 401 and competing sign-ins.

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
  `RequestPasswordReset` logs internal failures and never varies the response: an unknown,
  inactive, erased or deactivated account, a rollback, an unwired service and a mail failure all
  answer the same 200 (TC-AUTH-036a). Since #554 the answer does not wait for the account lookup or
  the mail: the handler hands the email to the password reset queue and answers at once; the
  queue's worker issues and mails the link and writes the `reset_request` audit row. A request the
  full queue cannot take gets the same 200 (`TestPasswordResetQueue`,
  `TestPasswordResetQueueProductionWiring`).
- **Automation:** blocked — no UI entry point exists (TC-AUTH-034); an API-only assertion belongs in
  the Go integration suite (`tests/auth_session_integration_test.go`) rather than in Playwright

### TC-AUTH-036a — A reset token is issued only to a live, active owner under the owner lock (#342)
- **Priority:** P1
- **Type:** security
- **Preconditions:** An active user; an admin to deactivate or erase it.
- **Steps:** `POST /api/v1/auth/password-reset` for the user; separately, deactivate or erase the
  user (`DELETE /users/{id}`, or `DELETE /customers/{id}` for a customer-role login) while the
  request is between its email lookup and its transaction, and once after it committed.
- **Expected:** Every answer is the identical **200** with the fixed message. A deactivation or
  erasure that commits before the request's owner lock leaves no new token and sends no mail. A
  request that commits first stores exactly one unused token and sends exactly one mail, after the
  commit, to the address of the locked row; a later erasure hard-deletes the token, and after a
  later deactivation `POST /auth/password-reset/confirm` refuses the link with the generic **400**.
  A failure of the invalidation or of the insert writes nothing and sends no mail.
- **Automation:** Go — `internal/service/credential_issuance_owner_lock_test.go`
  (`TestPasswordResetRequestOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
  `TestPasswordResetRequestOwnerLockConcurrentMySQL`, which needs `TEST_MYSQL_DSN`) and, over HTTP,
  `tests/credential_issuance_owner_lock_test.go` (`TestCredentialIssuanceOwnerLockAPISQLite`).
  A reset request for an account whose row another transaction holds waits for that lock on the
  reset queue's worker; the answer does not wait (#554).

### TC-AUTH-036b — A reset request racing a reset confirmation of the same user
- **Priority:** P2
- **Type:** concurrency
- **Preconditions:** An active user holding a spendable reset link.
- **Steps:** Confirm the link and request a new reset for the same user concurrently, in both
  orders.
- **Expected:** Confirm first: the password changes and the request then issues one new unused
  token and one mail. Request first: the confirmation is refused (`ErrInvalidResetToken`, the
  generic **400**), the password is unchanged and exactly one unused token remains. On MySQL, when
  each holds its first lock, the database ends one of them with error 1213 and that one is
  replayed: a request then issues its token after the confirmation (end state as confirm first),
  a confirmation finds its token retired and is refused with the generic **400** and nothing
  written (end state as request first). Neither caller receives error 1213. The request always
  answers 200 and sends exactly one mail.
- **Automation:** Go — `TestPasswordResetRequestVsConfirm{SQLite,MySQL}`
  (`internal/service/credential_issuance_owner_lock_test.go`)

### TC-AUTH-036c — Creating an API key for an owner deactivated or erased mid-request answers 401
- **Priority:** P1
- **Type:** security
- **Preconditions:** A user authenticated by a bearer token.
- **Steps:** `POST /api/v1/api-keys` while the user is deactivated or erased between authentication
  and the key's issuing transaction.
- **Expected:** **401** `UNAUTHORIZED` with the message `Unauthorized` and no key in the body; no
  `api_keys` row is written. A key created before a later erasure is hard-deleted by it; one
  created before a later deactivation is refused at authentication. Any other failure answers
  **500**.
- **Automation:** Go — `TestAPIKeyCreateOwnerUnavailableMapping` (`internal/handler`),
  `TestAPIKeyCreateOwnerLock{Decisions,Atomicity,Concurrent}SQLite` and
  `TestAPIKeyCreateOwnerLockConcurrentMySQL` (`internal/service`)

### TC-AUTH-037 — Complete a password reset end to end
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A user created by the test.
- **Steps:** Request a reset, read the token out of the delivered mail, open the reset link, set a new
  policy-compliant password, then log in with it.
- **Expected:** The token is single-use with a 1 h expiry; confirming returns **200**, clears any
  lockout, increments `users.token_version` (every access token issued before the reset answers
  **401**, #236), revokes every refresh token and revokes every API key of the account for good
  (#526; an API key issued before the reset answers **401**) in the same transaction that writes
  the password; the old password no longer authenticates and a replay of the link returns the
  generic **400**.
- **Automation:** blocked — needs mail capture (with `SMTP_HOST` unset the mailer only writes a
  redacted log line) *and* the missing `/reset-password` SPA route. The API round trip is
  covered in Go by `tests/auth_session_integration_test.go` `AuthSessionIntegrationTestSuite.TestPasswordResetRoundTrip`;
  the credential revocation by `TestCredentialRevocation{SQLite,MySQL}` (`reset_confirm`,
  `reset_confirm_rolls_back_when_the_key_revocation_fails`) and
  `tests/auth_access_token_revocation_test.go` `TestAccessTokenRevocationAPISQLite`
  (`password-reset/confirm`).

### TC-AUTH-037a — Reset confirmation is atomic and single-use
- **Priority:** P0
- **Type:** regression
- **Expected:** `POST /auth/password-reset/confirm` validates the length (at most 72 bytes) and
  the complexity policy and hashes the new password before anything is written, so a weak or
  over-long password leaves the link usable. The token claim — a conditional update that stamps `used_at`
  only while the row is unused and strictly unexpired and must affect exactly one row — the owner
  re-check, the password, lockout and `token_version` update (one write; the increment ends every
  earlier access token, #236) and the revocation of every refresh token of the
  account then run in one transaction (`internal/service/auth_service.go`
  `authService.ConfirmPasswordReset`, `internal/repository/password_reset_token_repository.go` `passwordResetTokenRepository.MarkUsed`).
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
  A password longer than bcrypt's 72-byte limit is refused by the handler before the service is
  called: **400** `BAD_REQUEST` `password must be at most 72 bytes long` (#397; it used to be a
  500; `internal/handler/auth_password_length_test.go` `TestPasswordLengthLimit`). The service
  still refuses such a password itself without spending the token.
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
- **Steps (UI):** `/settings/profile`, "Change password".
- **Expected:** `POST /auth/change-password` carries the tab's current `refresh_token` and returns
  **200** with "Password changed successfully" plus a replacement `token`, `refresh_token` and
  `user`; the page stores the pair in the storage the session already used ("Remember me" →
  `localStorage`) and says "You stay signed in here. Your other sessions are signed out and your
  API keys are revoked." The pre-change refresh token and every other session's refresh token
  answer **401** at `/auth/refresh`, and since #236 every access token issued before the change,
  including the one that made the request, answers **401** at once, while the renewed `token`
  works. Since #526 every API key of the account is revoked in the same transaction, for good
  (an API-key caller's own key included; the answer stays 200). A wrong current password returns **400** with "The
  current password is incorrect" (not 401 — the session presenting it is still authenticated).
- **Automation:** automated — `gocrm-ui/e2e/tests/settings.spec.ts` "changes the password and the
  new one works on the next login", "rejects a wrong current password with the server message";
  the API-key revocation in Go by `TestCredentialRevocation{SQLite,MySQL}`
  (`change_password_with_a_refresh_token`, `change_password_without_a_refresh_token`),
  `TestAccessTokenRevocationAPISQLite` (`change-password`) and
  `TestCredentialRevocationProductionWiring` (`password_change`).

### TC-AUTH-038a — Password change is atomic and renews only the presenting session
- **Priority:** P0
- **Type:** regression
- **Expected:** One transaction: locking owner read, the hash the current password was verified
  against re-checked (a concurrent change makes the second one a 400), the presented refresh token
  claimed, the new hash written together with the incremented `users.token_version` (one write,
  which ends every access token issued before the change, #236), every other refresh token revoked,
  the successor inserted and its access token signed with the new version. A failure at any step
  before the commit leaves the old password, the version and every session as they
  were, and the same request then succeeds. A presented token that is unknown, revoked, expired or
  another user's is **409** `SESSION_STALE` with nothing changed; the UI tells the user to sign in
  again (it is never retried). The web client runs the change under the cross-tab session lock and
  reads the stored session after acquiring it, so a rotation another tab made before the change
  took the lock is followed (the request carries the rotated refresh token and stored access token,
  no 409), and a refresh in another tab waits for the change and adopts its renewed pair (#184). If
  another tab signs out or signs in another account while the change is
  queued or in flight, nothing is sent under, or written over, that session. Without a
  refresh token, and always for an API-key request, every refresh token is revoked and no
  credentials are returned. A sibling refresh racing the change never makes it fail: committed
  first, its successor is revoked by the change; arriving during the change, it is rejected.
- **Known issue:** None for logins racing the change: a login that verified the old password a
  moment before the change commits re-checks the hash under the owner lock and is refused, and one
  that committed first loses its session to the change (#336, TC-AUTH-026b).
- **Automation:** `internal/service/auth_change_password_regression_test.go`
  (`TestChangePassword{Validation,Renewal,Atomicity,ConcurrentRefresh}{SQLite,MySQL}`; the MySQL
  concurrency root runs on a schema built by the real migrations and forces both lock orderings);
  `tests/auth_session_integration_test.go` `AuthSessionIntegrationTestSuite.TestChangePasswordRenewsCurrentSession`,
  `AuthSessionIntegrationTestSuite.TestChangePasswordWithAPIKeyNeverIssuesSession`; Vitest `client.refresh.test.ts`
  (`ApiClient.changePassword`) and `Profile.test.tsx`. The access-token side (#236):
  `internal/service/auth_access_token_revocation_test.go`
  (`TestAccessTokenVersionClaim`, `TestAccessTokenRevokedBy{Logout,PasswordChange,PasswordReset}{SQLite,MySQL}`,
  `TestAccessTokenSurvivesRefreshRotation{SQLite,MySQL}`, `TestLogoutLocksOwnerBeforeRefreshTokens{SQLite,MySQL}`,
  `TestLogoutRequiresSessionStore`, `TestAccessTokenRevocationLogsOmitPersonalData`) and, over HTTP,
  `tests/auth_access_token_revocation_test.go` `TestAccessTokenRevocationAPISQLite`.

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
  `/users` sits under the pathless `ProtectedRoute requiredPermission="users.view"` layout route in
  `routes/index.tsx` (the key comes from `GET /users/me`), and `GET /api/v1/users` passes its `RequireRole(admin)` guard
  (`internal/handler/routes.go` `SetupUserRoutes`).
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
- **Automation:** automated — `gocrm-ui/e2e/tests/registration.spec.ts` "a direct POST /auth/register is refused with 403"; Go: `internal/middleware/registration_test.go`, `tests/auth_integration_test.go` `AuthIntegrationTestSuite.TestRegisterEndpoint_GatedByConfiguration`

### TC-AUTH-046 — GET /auth/registration reports the sign-up switch without authentication
- **Priority:** P1
- **Type:** functional
- **Preconditions:** None (public endpoint, strict rate tier).
- **Steps:**
  1. `GET /api/v1/auth/registration` with no `Authorization` header, with the switch off, then on.
- **Expected:** **200** `{success:true, data:{enabled:false, sso_enabled:false}}` and
  `{enabled:true, sso_enabled:false}` respectively (single sign-on off). A configuration read
  failure is reported as `enabled:false`, matching what the gate enforces. `sso_enabled` is the
  effective single sign-on switch, independent of `enabled`, and `false` when the single sign-on
  configuration cannot be read; nothing else of that configuration is exposed (#436).
- **Automation:** automated — Go: `internal/handler/auth_registration_test.go`; `cmd/auth_oidc_wiring_test.go` `TestOIDCRoutesWired` (the `sso_enabled` of the production router)

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

## OpenID Connect sign-in (FEATURES 1.12)

Backend only (#435): the SPA has no `/auth/callback` page yet, so every case is driven through the
API by the Go suites, against an in-process TLS issuer that checks the PKCE verifier and the
redirect URI. Each case holds on SQLite, MySQL 8 and MariaDB 10.11 alike.

### TC-AUTH-049 — Starting a sign-in returns the issuer's authorization URL
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Single sign-on enabled with an issuer, a client id and a role mapping in
  `/settings/sso`.
- **Steps:**
  1. `POST /api/v1/auth/oidc/start` with no body and no authentication.
- **Expected:** **200** `{authorization_url}` pointing at the discovered authorization endpoint
  with `response_type=code`, the stored `client_id`, `code_challenge_method=S256` and a
  `code_challenge`, a `state` and a `nonce`, `redirect_uri=<APP_BASE_URL>/auth/callback` and the
  effective `scope`. One `oidc_login_attempts` row holds the HMAC of the state (never the state),
  expiring ten minutes later. `Set-Cookie: gophercrm_oidc_state=<the state of the URL>` with
  `Path=/api/v1/auth/oidc`, `Max-Age=600`, `HttpOnly`, `SameSite=Lax`, and sent only over HTTPS
  when `APP_BASE_URL` is https (#528); a refused start (404, 500, 503) sets no cookie, and a successful
  callback clears it (`Max-Age=0`). After the callback, whether it signed in or was
  refused after the claim, the row is spent (`used_at` set) and its `nonce` and `code_verifier`
  are empty; a callback on an expired attempt answers the generic 401 and empties both without
  spending the row, also when that clearing fails. Rows created more than a day ago are deleted by
  the stale bulk-operation sweep (#453). Each `oidc_sign_in` log line carries `request_id` and
  `attempt_id` (#453).
- **Automation:** automated — Go: `internal/service/auth_oidc_test.go` `TestOIDCSignIn{SQLite,MySQL}/start_authorization_url`, `/attempt_cleared_when_used`, `/attempt_cleared_when_expired`, `/attempt_claim_lost_after_read`, `/attempts_pruned_after_a_day`, `/log_correlation`, `/start_cookie_secure`, `TestOIDCClaimExpiredClearFails`; `internal/handler/auth_oidc_handler_test.go` `TestOIDCHandler` (incl. `start_ok_http`, `start_ok_https`, `callback_ok`, `request_id_reaches_service`); `cmd/bulk_recovery_runtime_test.go` `TestBulkOperationRecoveryProductionWiring`

### TC-AUTH-050 — A first sign-in provisions the user, a later one re-applies the mapping
- **Priority:** P0
- **Type:** functional
- **Preconditions:** As TC-AUTH-049; the identity `(iss, sub)` is unknown and its email is held by
  no account.
- **Steps:**
  1. Start, sign in at the issuer, `POST /api/v1/auth/oidc/callback` `{code, state}`.
  2. Change the mapped role in `/settings/sso` and sign in again; then once more unchanged.
  3. `POST /api/v1/auth/password-reset` `{email}` with the provisioned user's address.
- **Expected:** Step 1: **200** with `token`, `refresh_token` and `user`, as `POST /auth/login`
  answers; one active user with the mapped role and no usable password, named from `given_name`/
  `family_name`, or, when both are absent, from `name` split at the first whitespace (#453), one
  `user_identities` row,
  one `role_audit_log` row with `auth_method` `oidc`; the access token authenticates and the
  refresh token rotates. Step 2: the same user with the new role and one more `oidc` audit row; the
  unchanged sign-in adds none. `last_login_at` is not touched. Step 3: **200** with the same
  message as for any address; while `security.sso.allow_password` is off (the default) there is
  no `password_reset_tokens` row and no mail (#444), and a token of that account is refused by
  `/auth/password-reset/confirm` with the generic 400 and stays unspent. With the setting on the
  account is treated like a local one (TC-AUTH-057).
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}/first_sign_in_provisions`, `/names_split_from_name`, `/names_absent`, `TestOIDCProfileNames`, `/second_sign_in_reapplies_mapping`; step 3: `TestPasswordResetSSOAccount{SQLite,MySQL}`, `TestPasswordResetSSOAccountWired`

### TC-AUTH-051 — An IdP email that matches an existing account is never linked
- **Priority:** P0
- **Type:** security
- **Preconditions:** A local account holds `local@example.com`; the IdP identity is unknown.
- **Steps:**
  1. Sign in with an ID token whose `email` is `local@example.com`, then `Local@Example.COM`.
- **Expected:** **401** "Single sign-on failed" each time; no user and no identity are created and
  the local account is unchanged. An erased account's address refuses the same way.
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}/email_of_local_user_not_linked`, `/email_case_variant_not_linked`, `/concurrent_first_sign_in_order`

### TC-AUTH-052 — Every refused callback is the same 401 and writes nothing
- **Priority:** P0
- **Type:** security
- **Preconditions:** As TC-AUTH-049.
- **Steps:**
  1. Call back with a used state, an expired state (over ten minutes), and after the issuer
     setting changed since the start.
  2. Call back with an ID token carrying a wrong nonce, a wrong audience, a foreign signature, a
     `groups` string, no mapped value, a `groups` entry in `_claim_names` on an issuer other than
     Entra ID (TC-AUTH-058 covers Entra ID), or a `sub` that differs
     from a linked one only by a trailing space.
  3. Sign in as an identity whose user is deactivated, or erased (its identity row is kept).
  4. Call back with a valid unspent state but without the `gophercrm_oidc_state` cookie (another
     browser), or with the cookie of another sign-in (a browser that started its own) (#528).
- **Expected:** **401** `UNAUTHORIZED` "Single sign-on failed" for each, with no user, identity,
  refresh token or audit row written. In step 4 the attempt is spent all the same, and the
  sign-in audit row carries the reason `binding_missing` or `binding_mismatch` and the spent
  `attempt_id`; an unknown state stays `state_invalid` whatever the cookie. A token endpoint that fails is **503** `SSO_UNAVAILABLE`.
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}` subtests `reused_state`, `expired_state`, `issuer_changed_after_start`, `wrong_nonce`, `wrong_audience`, `bad_signature`, `malformed_groups_claim`, `no_mapping_match`, `group_overage`, `subject_trailing_space`, `deactivated_user`, `erased_user`, `token_endpoint_error`, `binding_missing`, `binding_mismatch`; `TestOIDCHandler` (`callback_without_cookie`, `callback_ok`, `callback_refused`); browser: `sso.spec.ts` S13 (a callback opened in a fresh browser, and in a browser that started its own sign-in)

### TC-AUTH-053 — The mapping cannot demote the last active admin; password login stays
- **Priority:** P1
- **Type:** security
- **Preconditions:** The only active admin is linked to an identity whose groups now map to sales.
- **Steps:**
  1. Sign in as that identity.
  2. Log in as an admin with a password while single sign-on is enabled.
- **Expected:** Step 1: **401**; the role stays admin, no audit row and no refresh token. Step 2:
  **200** with a token pair (break-glass).
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}/last_admin_demotion_refused`, `/local_admin_password_login`

### TC-AUTH-054 — Disabled, unavailable and private answers
- **Priority:** P1
- **Type:** functional
- **Preconditions:** None.
- **Steps:**
  1. `POST /api/v1/auth/oidc/start` with single sign-on off; then on with an unreachable issuer,
     and with an issuer whose discovery redirects.
  2. Post a callback body without `code`, or with `code` over 4096 bytes or `state` over 256.
- **Expected:** Step 1: **404** `NOT_FOUND`, then **503** `SSO_UNAVAILABLE` "Single sign-on is
  unavailable" (the issuer client follows no redirect). Step 2: **400** `VALIDATION_ERROR`. No
  answer sets a cookie, and the logs of a successful and a failed callback carry none of the code,
  state, ID token, email or subject.
- **Automation:** automated — Go: `cmd/auth_oidc_wiring_test.go` `TestOIDCRoutesWired`; `TestOIDCHandler`; `TestOIDCSignInSQLite/issuer_redirect`, `/log_privacy`; `TestOIDCHTTPClient`

### TC-AUTH-055 — The login page offers single sign-on only while it is on
- **Priority:** P1
- **Type:** functional
- **Preconditions:** None; the e2e backend runs with single sign-on off (no `OIDC_*` set).
- **Steps:**
  1. Go to `/login` and wait for the `GET /auth/registration` probe to answer.
  2. With `sso_enabled` true, press "Sign in with single sign-on".
  3. Repeat step 2 with `POST /auth/oidc/start` failing.
- **Expected:** Step 1: with `sso_enabled` false, missing or a failed probe, there is no "Sign in
  with single sign-on" button; the email and password fields, "Sign In" and "Forgot password?"
  render in every case. With `sso_enabled` true the outlined button renders below the password
  form. Step 2: one `POST /auth/oidc/start`, then the browser goes to the returned
  `authorization_url`. Step 3: the alert "Single sign-on is unavailable. Sign in with your
  password." and the page stays; the button is enabled again.
- **Automation:** automated — `gocrm-ui/e2e/tests/login.spec.ts` "hides the single sign-on button when single sign-on is disabled"; Vitest `src/pages/auth/Login.test.tsx`

### TC-AUTH-056 — The /auth/callback page finishes the sign-in without exposing the code
- **Priority:** P1
- **Type:** security
- **Preconditions:** Signed out.
- **Steps:**
  1. Open `/auth/callback?code=c&state=s` with a callback that succeeds.
  2. Repeat with the callback answering 401 and 503.
  3. Open `/auth/callback` without `code`, without `state`, and with `?error=access_denied`.
- **Expected:** The page renders outside the application shell. The address bar loses its query
  through a replace navigation before anything else. Step 1: exactly one
  `POST /auth/oidc/callback` `{code:"c", state:"s"}` (also under React StrictMode), the token pair
  is stored in sessionStorage only, and the browser lands on `/`. Steps 2 and 3: the one message
  "Single sign-on failed. Try again or sign in with your password." with a "Back to sign in" link
  to `/login` and no stored token; step 3 sends no request. The code, the state and the tokens
  are never rendered.
- **Automation:** automated — Vitest `src/pages/auth/AuthCallback.test.tsx`, `src/routes/index.test.tsx` "renders the page for a signed-out visitor and stays on /auth/callback"

### TC-AUTH-057 — Password sign-in and reset for a linked account follow `allow_password`
- **Priority:** P0
- **Type:** security
- **Preconditions:** An active account with a `user_identities` row and a known password (linked),
  and an active account without one (local). An admin holding `settings.sso_manage`.
- **Steps:**
  1. With `allow_password` off (stored `false`, or empty with `OIDC_ALLOW_PASSWORD` unset):
     `POST /api/v1/auth/login` with each account's correct password; `POST /api/v1/auth/password-reset`
     for each; `POST /api/v1/auth/password-reset/confirm` with a valid token of each.
  2. `PUT /api/v1/settings/sso` `{"allow_password": true}` and repeat step 1.
  3. With the setting on, reset the linked account's password and log in with it; then store
     `allow_password` false and log in with the same password.
- **Expected:** Step 1: the local account logs in (200), gets one token and one mail, and its
  confirm succeeds. The linked account's login is **401** "Invalid email or password" with no
  refresh token and `last_login_at`, `failed_login_attempts` and `locked_until` unchanged; its
  reset request answers the same 200 message with no token and no mail; its confirm is the generic
  **400** and the token stays unspent. Step 2: `GET /settings/sso` reports `allow_password` true
  with source `settings`; the linked account behaves exactly like the local one (login 200 with a
  refresh token, one token and one mail, confirm spends the token, changes the password and
  revokes every refresh token). Step 3: the login with the new password succeeds while on and is
  **401** once off. Sessions already issued are not ended by switching the setting off.
- **Automation:** automated — Go: `TestSSOAccountPasswordPolicy{SQLite,MySQL}` (subtests `off|unset_default_off|on/linked|unlinked/reset_request|reset_confirm|login`, `password_set_while_on_stops_when_off`), `cmd/auth_password_reset_sso_wiring_test.go` `TestSSOPasswordPolicyWired` (the production router), `TestSSOSettingsHandlerAllowPassword`; Vitest `src/pages/settings/SSOSettings.test.tsx` (the switch and its warning)

### TC-AUTH-058 — An Entra ID group overage is resolved through Microsoft Graph or refused
- **Priority:** P1
- **Type:** security
- **Preconditions:** Single sign-on on with an Entra ID issuer
  (`https://login.microsoftonline.com/<tenant>/v2.0`) and a role mapping with a `groups` entry for
  a security group's object id; the app registration has the delegated Microsoft Graph permission
  `User.Read` granted.
- **Steps:**
  1. `POST /api/v1/auth/oidc/start`; repeat with a mapping of only `roles` entries, and with a
     `groups` mapping on another issuer.
  2. Sign in as a person whose ID token carries `_claim_names.groups` instead of `groups` (an
     overage) and who is a member of the mapped group; Graph answers over two pages.
  3. Repeat as a member of no mapped group.
  4. Repeat while Graph answers 500, answers slower than the 5 s budget, sends an
     `@odata.nextLink` on every page beyond `OIDC_GRAPH_MAX_PAGES`, sends a nextLink to another
     host, or redirects.
  5. Sign in with an overage token while the mapping has only `roles` entries, and with a
     `groups` claim and no overage.
- **Expected:** Step 1: the `scope` carries `https://graph.microsoft.com/User.Read`
  once, after the configured scopes, only for the Entra ID issuer with a `groups` mapping; the
  other two scopes are the configured ones. Step 2: signed in with the mapped role; each Graph page
  was a `POST` with `{"securityEnabledOnly":true}` and the access token of that code exchange,
  the first to `/v1.0/me/getMemberObjects`. Step 3: **401** "Single sign-on failed"
  (`no_mapping_match`), nothing written. Step 4: **401** each time, logged as
  `groups_unavailable`, nothing written; no more requests than the page bound, and the other
  host receives none. Step 5: signed in with no Graph request. The access token never appears in
  the logs or in any table.
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}` subtests `graph_overage_match`, `graph_overage_no_match`, `graph_error`, `graph_timeout`, `graph_paging_bound`, `graph_foreign_host`, `graph_redirect`, `graph_no_overage`, `graph_roles_mapping`, `graph_scope_requested`, `graph_token_privacy`; `TestOIDCGraphDefaults`, `TestOIDCResolveEntraGroups`

### TC-AUTH-059 — Groups are read from userinfo when the ID token has none
- **Priority:** P1
- **Type:** security
- **Preconditions:** Single sign-on on (an Okta org server, for example) with a role mapping that
  has a `groups` entry for `crm-sales` and a `roles` entry for `CRM.Support`; the issuer advertises
  a `userinfo_endpoint` in discovery.
- **Steps:**
  1. Sign in as a person whose ID token has no `groups` claim while userinfo answers the same
     `sub` with `groups` holding `crm-sales`.
  2. Repeat with userinfo answering no `groups` member and an ID token carrying `roles`
     `CRM.Support`.
  3. Sign in with a `groups` claim in the ID token (also an empty `[]`), and with a mapping of only
     `roles` entries; repeat with an overage on a non-Entra issuer and an Entra overage resolved
     through Graph.
  4. Sign in while userinfo answers another `sub`, or no `sub`.
  5. Sign in while discovery has no userinfo endpoint or an `http://` one, or userinfo answers 500,
     answers slower than 5 s, redirects, sends more than 1 MiB, answers `application/jwt`, answers
     a JSON array, or sends `groups` as a string, `null` or an array of numbers.
- **Expected:** Step 1: signed in as sales after exactly one `GET` to the userinfo endpoint with
  `Authorization: Bearer <access token of that exchange>`. Step 2: signed in as support. Step 3:
  no userinfo request (the overage cases keep `group_overage` and Graph respectively). Step 4:
  **401** "Single sign-on failed" logged as `userinfo_subject_mismatch`, nothing written. Step 5:
  **401** each time, logged as `groups_unavailable`, nothing written; no request without an https
  endpoint, and a redirect target receives none. The access token never appears in the logs or
  in any table.
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}` subtests `userinfo_groups_mapped`, `userinfo_groups_absent`, `userinfo_not_called`, `userinfo_subject_mismatch`, `userinfo_unavailable`, `userinfo_token_privacy` (and `group_overage`, `graph_overage_match` for the zero-request checks)

### TC-AUTH-060 — Every sign-in outcome is recorded in the audit log with its reason
- **Priority:** P1
- **Type:** security
- **Preconditions:** A sales account with a password; a second account that is deactivated, one
  that is locked, one linked to single sign-on (with `allow_password` off) and one with an API
  key; single sign-on on with a role mapping.
- **Steps:**
  1. Sign in with the right password, a wrong password, an unknown email, and as each of the
     locked, deactivated and linked accounts. A repeat refused sign-in of the same reason,
     account and network in the same UTC minute adds 1 to that row's `attempts` instead of a row
     (#553); run each refusal once, or a minute apart.
  2. Refresh with a live refresh token, an unknown one, and one whose owner was deactivated or
     erased meanwhile; then present the first, already exchanged token again, and after it the
     successor it was exchanged for (#547). The successor's `token_invalid` refusal with no
     account shares the bucket of the unknown token's (#553): send it a minute later, or expect
     `attempts` 2 on that row.
  3. Call a protected route with `ApiKey` and an unknown, a revoked, an expired key and the key
     of a deactivated or erased owner; then with a bad `Bearer` token. A repeat refusal of the
     same reason, account and network in the same UTC minute adds 1 to that row's `attempts`
     instead of a row (#466); run each key once, or a minute apart.
  4. Finish a single sign-on that succeeds, one with an unknown state and one whose groups match
     no mapping entry.
  5. Send one request with an `X-Request-ID` of 100 characters or with a space.
  6. Change the password (#550) with the right current password and the current refresh token,
     with a wrong current password, with a refresh token of another account, as a deactivated
     and as an erased account; then once with malformed JSON and once with a weak new password.
  7. Request a password reset for the sales account, an unknown email, the deactivated account
     and the linked account; confirm a reset link, an unknown token, the same link a second time
     and the link of an account deactivated after it was sent; log out.
  8. From a fresh session (#551), create an API key, revoke it with `DELETE /api-keys/{id}`, then
     send `PUT /api-keys/{id}` with `{"is_active":true}`; create a second key and send `PUT` with
     `{"is_active":false}`; rename a live key, send `{"is_active":true}` on a live key, read the
     keys, and revoke a key of another account.
- **Expected:** Every answer is the same as before (200, the generic 401, 503 for an operational
  refresh failure; 400, 409 `SESSION_STALE` and 401 for the password change, the same 200 for
  every reset request, 400 `Invalid or expired reset token` for each refused confirmation). Each
  step 1–4, 6 and 7 request that reaches the service adds one `auth_audit_log` row, or, for a
  refused `password`, `refresh` or `api_key` outcome, adds 1 to the `attempts` of its bucket's
  row when that row already exists (#466, #553); successes have `attempts` 1. The rows: `password`
  `success`, or `refused` with `invalid_credentials` (`user_id` NULL for the unknown email, the
  account's id for the wrong password), `account_locked`, `account_inactive`, `sso_account`;
  `refresh` `success`, `token_invalid`, `account_inactive`, `account_erased`, `token_reuse` with the
  owner's id for the reused token of step 2 and then `token_invalid` with `user_id` NULL for its
  successor (#547); `api_key`
  `key_invalid`, `key_revoked`, `key_expired`, `account_inactive`, `account_erased`,
  `customer_login_role` (a key of a customer account, #532) (the account's `user_id` when the key
  was found); `oidc` `success` with the user id, `state_invalid`, `no_mapping_match`;
  `password_change` `success`, `invalid_credentials`, `session_stale`, `account_inactive`,
  `account_erased`, each with the caller's id; `reset_request` `success` with the user id,
  `account_unknown` with `user_id` NULL, `account_inactive`, `sso_account`; `reset_confirm`
  `success` with the user id, `token_invalid` with `user_id` NULL for the unknown and the spent
  token, `account_inactive` with the owner's id; `logout` `success` with the user id;
  `api_key_create` `success`, `api_key_revoke` `success` for the `DELETE` and for the `PUT` with
  `is_active` false, and `api_key_enable` `refused` `key_revoked` for the 409 (#551), each with the
  caller's id and the key's id in `api_key_id` and no key material or key name. The two
  step-6 400s, the bad Bearer token, and in step 8 the rename, `is_active` true on a live key, the
  reads and the 403 for another account's key add no row. Every row carries the request id and the client's `/24`; the
  request id of step 5 is stored empty. The `Single sign-on refused` log line names the row in
  `audit_id`.
- **Automation:** automated — Go: `TestAuthAuditOutcomes{SQLite,MySQL}` (incl. the #547 `refresh_token_reuse` and the #550 `password_change_*`, `reset_request_*`, `reset_confirm_*` and `logout_success` subtests), `TestAuthAuditOIDCReasonCoverage`, `TestOIDCSignIn{SQLite,MySQL}` (`assertRefused`, `audit_success_row`, `audit_refusal_row`, `audit_linked_user_refusal`, `audit_id_on_refusal_log`), `TestAuthAuditWired`, `TestAuthHandlerAudit`, `TestAuthAuditAPIKeyChanges` (#551), the `api_key_change_rows` subtest of `TestAuthAuditOutcomes{SQLite,MySQL}`, `TestAuth_RecordsAPIKeyFailure`, `TestAuth_DoesNotRecordBearerFailure`

### TC-AUTH-061 — The audit log and its log lines hold no personal or secret values
- **Priority:** P0
- **Type:** security
- **Preconditions:** A database with the sign-in audit log; the client at `198.51.100.77` with a
  Chrome on Windows user agent.
- **Steps:**
  1. Run a single sign-on that succeeds and one refused after the ID token verified, a password
     sign-in with a wrong password, a refresh and a request with an unknown API key; then (#550)
     a password reset request for the account and for an unknown email, the confirmation of the
     mailed link, a password change that presents the refresh token, and a logout, with a mailer
     that does not log its recipient.
  2. Read every column of every `auth_audit_log` row and the server log of those requests.
- **Expected:** Neither holds the email, the password (old or new), the subject, the access or
  refresh token, the reset token or link, the API key, the code, the state, the nonce, the full IP address or the full user agent. The
  rows hold `198.51.100.0/24` and `Chrome on Windows`. A failed audit write is logged once as
  `audit_write_failed` and the sign-in answers as without the audit.
- **Automation:** automated — Go: `TestAuthAuditPrivacy{SQLite,MySQL}`, `TestAuthAuditWriteFailure`

### TC-AUTH-062 — A refused single sign-on identity is recorded as a stable keyed reference
- **Priority:** P1
- **Type:** security
- **Preconditions:** Single sign-on on; `API_KEY_SECRET` set.
- **Steps:**
  1. Sign in twice as the same person whose groups match no mapping entry.
  2. Sign in as another person refused the same way, and as the same subject at another issuer.
  3. Restart with another `API_KEY_SECRET` and repeat step 1.
  4. Sign in successfully, and with a wrong nonce.
- **Expected:** Step 1 stores the same 64-hex `subject_ref` twice; steps 2 and 3 store different
  ones; step 4 stores NULL (success, and a refusal before the nonce matched).
- **Automation:** automated — Go: `TestAuthAuditSubjectReference`, `TestOIDCSignIn{SQLite,MySQL}` subtests `audit_success_row`, `audit_refusal_row`, `audit_id_on_refusal_log`

### TC-AUTH-063 — Audit rows older than the retention are deleted
- **Priority:** P2
- **Type:** functional
- **Preconditions:** `AUTH_AUDIT_RETENTION_DAYS` unset (90 days); audit rows 91 days and 1 day
  old.
- **Steps:**
  1. Start the server.
  2. Set `AUTH_AUDIT_RETENTION_DAYS` to `abc`, `0`, `-5`, `30` and `99999` in turn.
- **Expected:** Step 1: the boot sweep deletes the 91-day-old row and keeps the 1-day-old one; it
  runs again every 15 minutes. Step 2: the retention is 90, 90, 90, 30 and 36500 days.
- **Automation:** automated — Go: `TestBulkOperationRecoveryProductionWiring`, `TestAuthAuditPrune{SQLite,MySQL}`, `TestBulkOperationRecoveryAuthAuditPruning`, `TestLoadAuthAuditRetentionDays`

### TC-AUTH-064 — Refused API keys are throttled and counted, not written one row each
- **Priority:** P1
- **Type:** security
- **Preconditions:** A fresh server (the moderate tier is per process); an admin with a valid API
  key.
- **Steps:**
  1. From `192.0.2.1` send 200 `GET /api/v1/leads` with `Authorization: ApiKey gcrm_bogus0466`,
     then 50 more from `192.0.2.77`.
  2. Read the `api_key` rows of `auth_audit_log`.
  3. From `203.0.113.9` send `GET /api/v1/leads` with the valid key.
  4. In one UTC minute, refuse 500 keys from addresses of one `/24`, then one at the minute's last
     millisecond and one at the next minute; refuse one from another `/24`, two from one IPv6
     `/48`, and `key_revoked` for two accounts and `key_expired` for one.
- **Expected:** Step 1: every answer is 401 "Invalid credentials" or 429, with 429s once the
  moderate tier's burst of 30 is spent (the tier runs before authentication). Step 2: at most one
  row per UTC minute the run spanned, all `key_invalid` with `client_network` `192.0.2.0/24`, and
  their `attempts` add up to the number of 401s. Step 3: 200 and no new row. Step 4: one row with
  `attempts` 500 and the first refusal's request id, 501 after the last millisecond, a new row
  with `attempts` 1 in the next minute, and a row of its own for the other `/24`, for the `/48`
  (`attempts` 2) and for each reason and account. Refused password sign-ins and refreshes are
  counted the same way, one row per method, reason, account, network and minute (#553);
  successful ones and single sign-on outcomes are one row each with `attempts` 1.
- **Automation:** automated — Go: `TestAuthAuditAPIKeyFailuresBounded`, `TestAuthAuditAPIKeyAggregation{SQLite,MySQL}`, `TestAuthAuditLogAttemptsMigrationFiles`, `TestAuthAuditLogAttemptsMigration`

## SCIM 2.0 provider (FEATURES 1.15 to 1.20)

`/scim/v2` is served outside the API prefix to identity providers. #468 covers the provider
tokens, the SCIM authentication and the three discovery documents; #469 the read of the Users
resource (`GET /scim/v2/Users` with the `userName eq` filter and paging, `GET /scim/v2/Users/{id}`);
#470 the create and replace (`POST /scim/v2/Users`, `PUT /scim/v2/Users/{id}`, TC-AUTH-075 to
TC-AUTH-080); #471 the patch (`PATCH /scim/v2/Users/{id}`, TC-AUTH-081 to TC-AUTH-085), whose
`active` false, like a PUT with `"active":false`, ends the account's sessions at once; #504 the
delete (`DELETE /scim/v2/Users/{id}`, TC-AUTH-086), the same deactivation, never an erasure;
#505 the Entra ID first sign-in that finds a SCIM-created account by `oid` and `tid` (TC-AUTH-087);
#506 the Groups (`GET /scim/v2/Groups`, `GET` and `PATCH /scim/v2/Groups/{id}`, TC-AUTH-088 to
TC-AUTH-092), whose members patch recomputes the members' roles from the groups claim.
`/Schemas/{id}`, `/ResourceTypes/{id}` and a SCIM-shaped 404 for unknown paths are later slices. See the *SCIM provisioning* section of
`docs/AUTHORIZATION.md`.

### TC-AUTH-065 — Every refused SCIM authentication is the same 401 and is counted in the audit log
- **Priority:** P0
- **Type:** security
- **Preconditions:** A fresh server; an admin with a password and an API key; one SCIM token and
  a second one that was revoked. Each request below comes from its own client address.
- **Steps:**
  1. `GET /scim/v2/ServiceProviderConfig` without `Authorization`, with
     `Bearer scim_<64 hex no token has>`, with the revoked token, with the admin's JWT and with
     the admin's API key as `Bearer`.
  2. Repeat with the valid token upper-cased and with a trailing space.
  3. Read the new `auth_audit_log` rows and the server log.
  4. Call `GET /api/v1/users` with the valid SCIM token as `Bearer`.
- **Expected:** Steps 1 and 2: each 401 with content type `application/scim+json` and the body
  `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication failed"}`,
  byte for byte the same. Step 3: rows of method `scim`, outcome `refused`, `user_id` NULL, one
  per reason, client `/24` and UTC minute (#553), whose `attempts` add up to the requests of that
  reason: `token_missing` 1, `token_revoked` 1 and `token_invalid` 5 (the unknown token, the
  JWT, the API key and both step 2 variants); no log line
  holds a presented token or the hash of a stored one. Step 4: 401 — a SCIM token is not a
  credential of the API.
- **Automation:** automated — Go: `TestSCIMRoutesWired`, `TestSCIMAuth`, `TestSCIMTokenStore{SQLite,MySQL}`

### TC-AUTH-066 — SCIM refusals over the per-address limit answer 429 and write no audit row
- **Priority:** P1
- **Type:** security
- **Preconditions:** A fresh server (the SCIM limits are per process); `DISABLE_RATE_LIMIT` unset;
  a valid SCIM token.
- **Steps:**
  1. From one client address send six rapid `GET /scim/v2/ServiceProviderConfig` without a token.
  2. Read the `scim` rows of `auth_audit_log`.
  3. From the same address send `GET /scim/v2/ServiceProviderConfig` with the valid token.
- **Expected:** Requests 1 to 5 are 401; the sixth is 429 with content type
  `application/scim+json` and body status `"429"` (the SCIM refusal limit, 10 a minute, burst 5,
  per client address). Step 2: the `attempts` of the `scim` rows add up to 5, in one
  `token_missing` row per UTC minute (#553); the 429 added none. Step 3: 200, because accepted
  requests count against the SCIM token limit of their token (default 1500 a minute, burst 150),
  not the address. The `/auth`, moderate and generous tiers answer as before.
- **Automation:** automated — Go: `TestSCIMStrictTierWired`, `TestSCIMTokenTierWired`, `TestSCIMRateLimits`

### TC-AUTH-067 — With a valid token the three discovery documents follow RFC 7643
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A SCIM token; `PUBLIC_BASE_URL` set to the backend's public origin.
- **Steps:**
  1. `GET /scim/v2/ServiceProviderConfig`, `/scim/v2/Schemas` and `/scim/v2/ResourceTypes` with
     `Authorization: Bearer <token>`, each also with a query string.
  2. Read the token in `GET /api/v1/settings/scim/tokens`.
- **Expected:** Each answers 200 with `application/scim+json`; query parameters change nothing.
  ServiceProviderConfig: `patch` supported, `filter` supported with `maxResults` 100, `bulk`,
  `sort`, `etag` and `changePassword` not supported, one `oauthbearertoken` scheme. Schemas and
  ResourceTypes: ListResponses with `totalResults` 2 (User, then Group since #506) and
  `startIndex` 1; the User schema holds `userName`, `name` (`givenName`, `familyName`), `emails`
  (`value`, `type`, `primary`) and `active`, the Group schema `displayName` and `members`
  (`value`), each with every §7 characteristic; the resource types are `User` at `/Users` and
  `Group` at `/Groups`.
  `meta.location` starts with `PUBLIC_BASE_URL/scim/v2/`. Step 2: `last_used_at` is set.
- **Automation:** automated — Go: `TestSCIMDiscoveryDocuments`, `TestSCIMRoutesWired`, `TestDiscoveryLocations`

### TC-AUTH-068 — SCIM tokens are issued once, stored as a hash and revoked by an admin only
- **Priority:** P1
- **Type:** security
- **Preconditions:** An admin; a sales, a support and a customer account; a custom role without
  `settings.scim_manage`, and one whose stored `role_permissions` rows hold it (written into the
  table directly: since #524 no custom role can be given the key).
- **Steps:**
  1. As admin, `POST /api/v1/settings/scim/tokens` with `{"name":"Entra"}`, then with a blank and
     a 101-character name.
  2. Read the `scim_tokens` row; `GET /api/v1/settings/scim/tokens`.
  3. `DELETE /api/v1/settings/scim/tokens/:id` twice, then with an unknown id and with `0`.
  4. Call the three routes, with otherwise valid requests, as each other role and both custom
     roles.
- **Expected:** Step 1: 201 with `token` (`scim_` + 64 hex) and `scim_token`; the bad names are
  400 `VALIDATION_ERROR` with `details.name`. Step 2: `token_hash` is the `hmac$` HMAC-SHA256 of
  the token under `API_KEY_SECRET`; no column holds the token; the list is newest first and
  carries `id`, `name`, `created_at`, `last_used_at`, `revoked_at` only. Step 3: 200 with
  `revoked_at` set, the same `revoked_at` the second time, 404, 400. Step 4: 403 `FORBIDDEN` for
  sales, support, customer and both custom roles: the key is admin-only, and a stored row holding
  it is dropped when the role's permissions are resolved.
- **Automation:** automated — Go: `TestSCIMTokenHandler`, `TestSCIMTokenStore{SQLite,MySQL}`, `TestSCIMRoutesWired`, `TestEveryRouteRequiresItsOwnKey`, `TestAdminOnlySettingsPermissionsSQLite`

### TC-AUTH-069 — The Entra ID and Okta userName filters return the staff user
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A SCIM token; the single sign-on issuer `https://idp.example.test`; a staff
  user with a `user_identities` row under that issuer (subject `sub-469`). Each request comes
  from its own client address.
- **Steps:**
  1. `GET /scim/v2/Users?filter=userName+eq+"<email>"` (Entra ID shape).
  2. `GET /scim/v2/Users?filter=userName%20eq%20%22<email>%22&startIndex=1&count=100` (Okta shape).
  3. Repeat step 1 with the address in other ASCII letter case, and with
     `urn:ietf:params:scim:schemas:core:2.0:User:userName EQ`.
  4. `GET /scim/v2/Users/<id>`.
- **Expected:** Steps 1 to 3: 200 `application/scim+json`, a ListResponse with `totalResults` 1,
  `startIndex` 1, `itemsPerPage` 1. The resource has `id` (the user id as a string), `userName`
  and one `emails` entry (the email, `work`, primary), `name.givenName`/`familyName`, `active`,
  `externalId` `sub-469` and `meta.location` `PUBLIC_BASE_URL/scim/v2/Users/<id>`. Step 4: 200
  with the same resource.
- **Automation:** automated — Go: `TestSCIMUsersWired`, `TestSCIMUsers{SQLite,MySQL}`, `TestParseUserFilter`, `TestUserResource`

### TC-AUTH-070 — The userName judge folds ASCII case only
- **Priority:** P0
- **Type:** security
- **Preconditions:** Staff users `jose@…` and `kate@…`; MySQL 8 or MariaDB 10.11, where
  `users.email` is `utf8mb4_unicode_ci`.
- **Steps:**
  1. Filter by an unknown address.
  2. Filter by `"jose@… "` (trailing space), `"josé@…"`, `"joſe@…"` (U+017F LONG S) and
     `"Kate@…"` written with U+212A KELVIN SIGN.
- **Expected:** Each answers 200 with `totalResults` 0 and `"Resources":[]`, although the database
  returns the stored row for every variant of step 2: the returned row is kept only when it is
  equal byte for byte after folding `A`-`Z` alone.
- **Automation:** automated — Go: `TestSCIMUsers{SQLite,MySQL}` (MySQL 8 and MariaDB), `TestUserNameMatches`, `TestSCIMUsersWired`

### TC-AUTH-071 — Erased and customer accounts are never served
- **Priority:** P0
- **Type:** security
- **Preconditions:** A staff user, a customer user, a user erased through `DELETE /users/:id`
  and a deactivated staff user.
- **Steps:**
  1. `GET /scim/v2/Users/<erased id>` and `/<customer id>`.
  2. `GET /scim/v2/Users` without a filter.
  3. Filter by the customer's email and by the erased user's former email.
  4. `GET /scim/v2/Users/<deactivated id>`.
- **Expected:** Step 1: 404 with
  `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`.
  Step 2: `totalResults` counts only the managed accounts (live, not customer, linked under the
  configured issuer; TC-AUTH-093) and no resource carries either id. Step 3: `totalResults` 0. Step 4: 200 with `"active":false`.
- **Automation:** automated — Go: `TestSCIMUsersWired`, `TestSCIMUsers{SQLite,MySQL}`

### TC-AUTH-072 — Unsupported filters and malformed paging are SCIM 400s
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A SCIM token.
- **Steps:**
  1. `GET /scim/v2/Users` with each filter: empty, `externalId eq "x"`, `emails.value eq "x"`,
     `id eq "1"`, `userName ne "x"`, `userName co "x"`, `userName sw "x"`, `userName pr`,
     `userName eq x`, `userName eq "a" and active eq true`, `(userName eq "a")`,
     `userName eq "a" b`.
  2. `GET /scim/v2/Users?count=abc` and `?startIndex=1.5`.
  3. `GET /scim/v2/Users/abc`, `/0`, `/-1` and `/18446744073709551616`.
- **Expected:** Step 1: 400 `application/scim+json` with `"scimType":"invalidFilter"` and
  `"status":"400"`; no user is read. Step 2: 400 with `"scimType":"invalidValue"`. Step 3: the
  SCIM 404, without the id echoed.
- **Automation:** automated — Go: `TestSCIMUserHandler`, `TestParseUserFilter`, `TestParsePaging`, `TestSCIMUsersWired`

### TC-AUTH-073 — The user list pages as RFC 7644 says
- **Priority:** P1
- **Type:** functional
- **Preconditions:** More than 100 visible users.
- **Steps:**
  1. `GET /scim/v2/Users`, then with `startIndex=2&count=2`, `startIndex=0`, `count=0`,
     `count=-1` and `count=500`.
  2. A `userName eq` filter that matches one user, with `startIndex=2`.
- **Expected:** Step 1: id ascending; `startIndex` absent or below 1 is 1; `count` absent is 100,
  negative 0, above 100 is 100; `count=0` gives `totalResults` with `"Resources":[]`;
  `itemsPerPage` is the number of resources returned. Step 2: `totalResults` 1 and no resource.
- **Automation:** automated — Go: `TestSCIMUsers{SQLite,MySQL}`, `TestSCIMUserHandler`, `TestParsePaging`

### TC-AUTH-074 — externalId is the subject under the configured issuer, byte for byte
- **Priority:** P1
- **Type:** functional
- **Preconditions:** The issuer `https://idp.example.test`; users with an identity under it, one
  under `https://other.example.test`, one under `https://idp.example.test ` (trailing space), one
  with two subjects under it and one without any.
- **Steps:**
  1. `GET /scim/v2/Users`.
  2. Clear the issuer setting and `OIDC_ISSUER_URL`, then repeat.
- **Expected:** Step 1: `externalId` is the subject under the configured issuer, the lowest id's
  of the two; it is omitted for the other issuer, the trailing-space issuer (also on MySQL 8 and
  MariaDB, where `utf8mb4_bin` is PAD SPACE) and the user without one. Step 2: no resource has
  `externalId`.
- **Automation:** automated — Go: `TestSCIMUsers{SQLite,MySQL}`, `TestSCIMUsersWired`

### TC-AUTH-075 — A SCIM create provisions a linked staff account with the mapped role
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A SCIM token; the single sign-on issuer configured; a role mapping whose
  entries with claim `roles` map `CRM.Sales` to the sales system role and `CRM.Custom` to a custom
  role.
- **Steps:**
  1. `POST /scim/v2/Users` with `userName`, `externalId` `sub-470`, `name`, `password` and
     `"roles":[{"value":"CRM.Sales"}]`.
  2. Repeat with another address and `externalId` and `"roles":[{"value":"CRM.Custom"}]`,
     `"active":false`.
- **Expected:** Step 1: 201 `application/scim+json` with a `Location` header equal to
  `meta.location`; the resource has the new `id`, the `externalId`, the `userName` and
  `"active":true`. The database holds one users row (role `sales`, `role_id` NULL, a password hash
  the sent password does not verify), one `user_identities` row (the issuer, subject `sub-470`),
  one `role_audit_log` row (`user_role_changed`, `auth_method` `scim`, `actor_user_id` NULL,
  `before_state` NULL) and one `auth_audit_log` row (method `scim`, outcome `success`, `user_id`
  the account). Step 2: role `custom` with the custom role's id, stored inactive.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`create`), `TestSCIMUserWritesWired`, `TestSCIMUserWriteHandler`

### TC-AUTH-076 — A taken address or a linked externalId is a 409 uniqueness
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A user `jose@x.test`; an erased user; a user linked to `sub-taken` under the
  issuer.
- **Steps:**
  1. `POST /scim/v2/Users` with `userName` `JOSE@X.TEST`, then with the erased user's former
     address.
  2. On MySQL 8 or MariaDB 10.11: `"jose@x.test "` (trailing space) and `"josé@x.test"`.
  3. A new address with `externalId` `sub-taken`.
- **Expected:** Each answers 409 with `"scimType":"uniqueness"` and a fixed detail that echoes no
  value; the users (erased rows included), `user_identities`, `role_audit_log` and
  `auth_audit_log` row counts are unchanged. On SQLite the padded address of step 2 fails the
  address format rule instead (400 `invalidValue`), also writing nothing.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`duplicate email`, `duplicate email variants` on MySQL/MariaDB, `identity taken`)

### TC-AUTH-077 — Invalid SCIM user bodies are refused without a write
- **Priority:** P0
- **Type:** negative
- **Preconditions:** A SCIM token and an otherwise valid body.
- **Steps:**
  1. Send `externalId` absent, `""` and `"sub "`.
  2. Send no `roles`, a value only a `groups` entry carries, a value differing in letter case from a
     `roles` entry, a value mapped to the customer role and one mapped to a role id with no row.
  3. Send a malformed body, a JSON array, an object followed by more data, a body over 64 KiB and
     `"active":"False"`.
- **Expected:** Steps 1 and 2: 400 `"scimType":"invalidValue"`. Step 3: 400
  `"scimType":"invalidSyntax"`, and the service is not called. No error body contains the
  submitted `userName` or `externalId`, and the four tables are unchanged.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`missing externalId`, `unmapped role`), `TestSCIMUserWriteHandler`

### TC-AUTH-078 — A SCIM replace overwrites name, email, role and active
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A sales account linked to `sub-sam`; a support account; another account
  holding `ann@x.test`; a customer and an erased account.
- **Steps:**
  1. `PUT /scim/v2/Users/<sales id>` with a new name, a new email and roles mapped to support.
  2. `PUT` the support account keeping its role, without `name`.
  3. Sign the sales account in twice, then `PUT` it with `"active":false`.
  4. `PUT` to an unknown, the erased, the customer id and `abc`.
  5. `PUT` with `userName` `ann@x.test`.
- **Expected:** Step 1: 200; the row holds the new names, email and role `support`; two
  `role_audit_log` rows (`auth_method` `scim`, `actor_user_id` NULL): `user_email_changed` (no
  state, no address) and then `user_role_changed` (before `{role: sales}`, after
  `{role: support}`); and one `auth_audit_log` success row; `externalId` stays `sub-sam` (an
  `externalId` in the body is ignored). Step 2 (the stored `userName` kept): no `role_audit_log` row, one `auth_audit_log` row, names cleared. Step 3:
  `is_active` false, `token_version` one higher and both `refresh_tokens` rows revoked in the
  same transaction (#471), with one `user_deactivated` role audit row (#552); a reactivating `PUT`
  writes one `user_activated` row, and the earlier access and refresh tokens are
  still refused. Step 4: 404 `User not found`. Step 5: 409 `uniqueness`, nothing written.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`replace changes role`, `replace keeps role`, `replace deactivates`, `replace not visible`, `replace email taken`), `TestSCIMUserWritesWired`

### TC-AUTH-079 — SCIM cannot demote or deactivate the last active admin
- **Priority:** P0
- **Type:** security
- **Preconditions:** Exactly one active admin.
- **Steps:**
  1. `PUT /scim/v2/Users/<admin id>` with roles mapped to sales.
  2. `PUT` with roles mapped to admin and `"active":false`.
- **Expected:** Both answer 400 `"scimType":"invalidValue"` with a fixed detail; the account is
  still an active admin and no `role_audit_log` or `auth_audit_log` row is written.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`last admin`)

### TC-AUTH-080 — An OIDC sign-in uses the account SCIM created
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Single sign-on configured with the test issuer and a `roles` mapping entry.
- **Steps:**
  1. `POST /scim/v2/Users` with `externalId` X and a matching role value.
  2. Sign in through OIDC with `sub` X and a matching `roles` claim.
- **Expected:** The sign-in returns tokens for the SCIM-created user id; the users and
  `user_identities` row counts are unchanged.
- **Automation:** automated — Go: `TestSCIMUserWrites{SQLite,MySQL}` (`sign-in reuses account`)

### TC-AUTH-081 — A SCIM patch in Entra ID's form deactivates an account
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A sales account that has signed in with its password.
- **Steps:**
  1. `PATCH /scim/v2/Users/<id>` with
     `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`.
  2. On another linked account, `PATCH` with Microsoft's "replace multiple attributes" body
     without `aadOptscim062020` (`Replace` of `displayName`, `emails[type eq "work"].value`,
     `name.givenName`, `name.familyName`, `externalId` and the enterprise `employeeNumber`), then
     with a body of only `displayName`, `title`, `externalId`, enterprise and `phoneNumbers`
     operations, then with Microsoft's SingleAppRoleAssignment body
     (`{"op":"Add","path":"roles","value":[{"value":"{\"id\":\"...\",\"value\":\"<mapped role>\",\"displayName\":\"Role1234\"}"}]}`) (#629).
- **Expected:** Step 1: 200 `application/scim+json` with `active` false; the row is inactive, its other
  columns unchanged; one `role_audit_log` row `user_deactivated` (`auth_method` `scim`, NULL
  `actor_user_id`, before `{"is_active":true}`, after `{"is_active":false}`; #552) and one
  `auth_audit_log` row (method `scim`, outcome `success`, the account's `user_id`). Step 2: each
  answers 200. The first stores the new email and names; `externalId`, the `user_identities` row
  and the identity count are unchanged and no role row is written. The second changes no account
  column (`updated_at` included) and adds only the `auth_audit_log` success row. The third sets
  the mapped role and writes one `user_role_changed` row.
- **Automation:** automated — Go: `TestSCIMUserPatch{SQLite,MySQL}` (`entra deactivate`, `entra replace without the flag ignores unstored attributes`, `unstored attributes only change nothing`, `entra role as JSON text`), `TestSCIMUserPatchHandler` (`Microsoft Entra ID bodies`), `TestParsePatch`, `TestSCIMUserPatchWired`

### TC-AUTH-082 — A SCIM patch in Okta's path-less form deactivates and reactivates
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A support account that has signed in.
- **Steps:**
  1. `PATCH` with `{"Operations":[{"op":"replace","value":{"active":false}}]}`.
  2. `PATCH` the now inactive account with `"active":false` again.
  3. `PATCH` with `{"Operations":[{"op":"replace","value":{"active":true}}]}`.
- **Expected:** Steps 1 and 3: 200 with `active` false, then true. Each `active` false moves
  `token_version` up by one (also on the already inactive row); `active` true does not. Step 1
  writes one `user_deactivated` role audit row and step 3 one `user_activated` row (`scim`, NULL
  actor; #552); step 2 writes none.
- **Automation:** automated — Go: `TestSCIMUserPatch{SQLite,MySQL}` (`okta deactivate`, `reactivate`), `TestSCIMUserPatchWired`

### TC-AUTH-083 — A SCIM deactivation ends every session at once; reactivation brings none back
- **Priority:** P0
- **Type:** security
- **Preconditions:** A sales account with a password, signed in twice (two access and two
  refresh tokens).
- **Steps:**
  1. `PATCH` `active` false.
  2. Before any refresh call, read the account's `refresh_tokens` rows and `token_version`.
  3. `GET /users/me` with the first access token; `POST /auth/refresh` with the first refresh
     token.
  4. `PATCH` `active` true; repeat step 3 with the first access token and the second refresh
     token; sign in again.
- **Expected:** Step 2: every row is revoked, every API key of the account is revoked
  (`is_active` false, `revoked_at` set; #526) and `token_version` is one higher; the patch wrote
  one `user_deactivated` role audit row, and the step 4 patch writes one `user_activated` row
  (#552). Step 3: both 401,
  and so is an API key of the account. Step 4: the old access token, the unused refresh token and
  the API key stay 401 (the account is active, so only the generation refuses the access token and
  the revocation the key); the new sign-in succeeds and its tokens work.
- **Automation:** automated — Go: `TestSCIMUserPatch{SQLite,MySQL}` (`sessions end`, `reactivate`), `TestSCIMUserWrites{SQLite,MySQL}` (`replace deactivates`), `TestSCIMUserPatchWired`;
  the API keys by `TestCredentialRevocation{SQLite,MySQL}` (`scim_patch_active_false`,
  `scim_replace_active_false`, `scim_patch_active_true_after_a_deactivation_restores_nothing`)

### TC-AUTH-084 — A SCIM patch outside the supported paths is refused and writes nothing
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A sales account.
- **Steps:**
  1. `PATCH` with path `password`, `groups` or `emails[type eq "home"].value`, and path-less with
     `{"value":{"active":true,"password":"x"}}`.
  2. `PATCH` with `{"op":"remove"}` and no path.
  3. `PATCH` with `{"Operations":[]}`, and with an `op` of `move`.
  4. `PATCH` `active` with `"yes"` or `null`, and `remove` of `active`, `userName`, `emails` or
     `roles`.
- **Expected:** Step 1: 400 `invalidPath`; step 2: 400 `noTarget`; step 3: 400 `invalidSyntax`;
  step 4: 400 `invalidValue`. Each detail is fixed and echoes no path or value; the service is not
  called and nothing is written.
- **Automation:** automated — Go: `TestParsePatch`, `TestSCIMUserPatchHandler`, `TestSCIMUserPatch{SQLite,MySQL}` (`refusals write nothing`)

### TC-AUTH-085 — A SCIM patch cannot demote or deactivate the last active admin
- **Priority:** P0
- **Type:** security
- **Preconditions:** Exactly one active admin, signed in once.
- **Steps:**
  1. `PATCH` with path `active` and `"False"`.
  2. `PATCH` path-less with `{"value":{"active":false}}`.
  3. `PATCH` `roles` mapped to sales.
- **Expected:** Each answers 400 `invalidValue` with the detail "The last active admin must stay an
  active admin"; the account stays an active admin, its `token_version` and refresh tokens are
  unchanged, and no `role_audit_log` or `auth_audit_log` row is written. With a second active
  admin the same deactivation succeeds.
- **Automation:** automated — Go: `TestSCIMUserPatch{SQLite,MySQL}` (`last admin`)

### TC-AUTH-086 — A SCIM DELETE deactivates, never erases, and is idempotent
- **Priority:** P0
- **Type:** security
- **Preconditions:** A valid SCIM token; an active staff account signed in twice (two refresh
  tokens); a second active admin exists for the admin step.
- **Steps:**
  1. `DELETE /scim/v2/Users/{id}` without an `Authorization` header, then with an unknown `scim_`
     token.
  2. `DELETE /scim/v2/Users/{id}` with the valid token.
  3. `GET /api/v1/users/me` with an earlier access token and `POST /api/v1/auth/refresh` with an
     earlier refresh token.
  4. Repeat step 2.
  5. `DELETE` the only active admin; `DELETE` `abc`, `0`, an unknown id, a customer and an erased
     account.
- **Expected:** Step 1 answers 401 with the SCIM error body and the account stays active. Step 2
  answers 204 with an empty body: `is_active` false, `token_version` + 1, every refresh token
  revoked before any refresh call, every API key of the account revoked (#526); email, names, role, `role_id`, the `user_identities` row and a
  NULL `deleted_at` are unchanged; one `auth_audit_log` row (method `scim`, outcome `success`) and
  one `role_audit_log` row `user_deactivated` (`scim`, NULL actor; #552). Step 3 answers 401 twice. Step 4 answers 204 and writes nothing (no
  audit row, same `token_version`). Step 5: the last admin is 400 `invalidValue` ("The last active
  admin must stay an active admin") with nothing written; every other id is the SCIM 404 `User not
  found` with nothing written.
- **Automation:** automated — Go: `TestSCIMUserDelete{SQLite,MySQL}`, `TestSCIMUserDeleteHandler`,
  `TestSCIMUserDeleteWired`; the API keys by `TestCredentialRevocation{SQLite,MySQL}`
  (`scim_delete`, `scim_delete_on_an_inactive_account_writes_nothing`) and
  `TestCredentialRevocationProductionWiring` (`scim_delete`)

### TC-AUTH-087 — An Entra ID first sign-in finds the SCIM-created account by oid and tid
- **Priority:** P0
- **Type:** security
- **Preconditions:** Single sign-on configured with the tenant-specific Entra ID issuer
  `https://login.microsoftonline.com/<tenant id>/v2.0` and a role mapping; a staff account created
  through `POST /scim/v2/Users` with `externalId` = the person's `objectId` (its `user_identities`
  row has `source` `scim`).
- **Steps:**
  1. Sign in with single sign-on as that person: the ID token carries a `sub` no account holds,
     `oid` = the `externalId` and `tid` = the issuer's tenant id.
  2. Sign in again with the same `sub` (with or without `oid`/`tid`).
  3. `GET /scim/v2/Users/{id}` for the account.
  4. Repeat step 1 from a clean state with one input changed each time: `tid` of another tenant,
     `tid` in another letter case, no `oid` claim, an issuer that is not Entra ID, an `oid` that
     equals a row a sign-in wrote.
  5. Repeat step 1 with an `oid` that matches no SCIM link and the account's email address in the
     `email` claim; then for a deactivated and for an erased SCIM-created account.
- **Expected:** Step 1 signs in as the SCIM-created account: no new account, the account gets a
  second `user_identities` row `(iss, sub)` with `source` `oidc`, and no role audit row names
  another user. Step 2 signs in as the same account and adds no row. Step 3 still serves the
  `objectId` as `externalId`. Each variant of step 4 provisions a new account with only its own
  `(iss, sub)` row, and the SCIM-created account keeps its one row and gets no session. Step 5 is
  the generic 401 with the audit reason `email_taken`, `account_inactive` and `account_erased`,
  with nothing written. Two concurrent first sign-ins of the same `sub` commit exactly one link.
- **Automation:** automated — Go: `TestOIDCSignIn{SQLite,MySQL}` (`scim_oid_*`),
  `TestOIDCEntraTenant`, `TestSCIMUserWrites{SQLite,MySQL}` (`create`)

### TC-AUTH-088 — The SCIM Groups are the groups-claim values of the role mapping
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A valid SCIM token; a role mapping `[{groups, Sales → sales}, {groups,
  Admins → admin}, {roles, X → sales}]`; members of Sales: an active and a deactivated staff
  account, a customer account and an erased account.
- **Steps:**
  1. `GET /scim/v2/Groups`.
  2. `GET /scim/v2/Groups?filter=displayName eq "Sales"`, then with `"sales"`, then
     `filter=userName eq "a"`.
  3. `GET /scim/v2/Groups/{id of Sales}`.
  4. Remove Sales from the mapping; repeat step 1, then `GET` and `PATCH` the Sales id.
  5. Repeat steps 1 to 3 with `excludedAttributes=members` (also as
     `urn:ietf:params:scim:schemas:core:2.0:Group:members`, any ASCII case), including
     `GET /scim/v2/Groups?excludedAttributes=members&filter=displayName eq "<a name the mapping
     does not hold>"`.
- **Expected:** Step 1 answers 200 `application/scim+json` with `totalResults` 2 and exactly
  `Admins` then `Sales` (the roles entry is no group). Step 2 returns only Sales, then nothing, then
  400 `invalidFilter`. Step 3 lists the active and the deactivated account by their SCIM ids,
  never the customer or the erased account. Step 4 lists only Admins, and both requests answer the
  SCIM 404 `Group not found`. Step 5 answers the same groups without a `members` key; the unknown
  name gives 200 with `totalResults` 0 and `"Resources":[]`. Without the parameter `members` is
  always present, `"members":[]` for an empty group; another excluded name changes nothing.
- **Automation:** automated — Go: `TestSCIMGroups{SQLite,MySQL}` (`list`), `TestSCIMGroupHandler`,
  `TestSCIMGroupsWired`, `TestSCIMParseGroupFilter`, `TestSCIMExcludesMembers`,
  `TestSCIMDiscoveryDocuments`

### TC-AUTH-089 — A group patch recomputes the member's role from the groups claim
- **Priority:** P0
- **Type:** functional
- **Preconditions:** A valid SCIM token; the mapping `[{groups, Admins → admin}, {groups, Sales →
  sales}]`; a second active admin; a sales account U in Sales only, holding a signed-in session
  (access and refresh token) and an API key.
- **Steps:**
  1. `PATCH /scim/v2/Groups/{Admins}` with `add` path `members` value `[{"value":"<U>"}]`.
  2. `PATCH` the same group with `remove` path `members[value eq "<U>"]`.
  3. `PATCH /scim/v2/Groups/{Sales}` removing U.
- **Expected:** Step 1 answers 204 with no body, and a following `GET` of Admins lists U as a
  member; U becomes admin, one `role_audit_log`
  `user_role_changed` row (`auth_method` `scim`, NULL `actor_user_id`) and, after the commit, one
  `auth_audit_log` success row (method `scim`) for U; a raised role ends no session. Step 2 makes U
  sales again with one more audit row; the sales role lacks permissions the admin role had, so U's
  sessions end at once (`token_version` + 1, every refresh token revoked) while U's API key keeps
  working with the sales role. Step 3 leaves U in no mapped group: U is deactivated (`is_active`
  false, role kept), every access token, refresh token and API key of U ends, one role audit row
  `user_deactivated` (`scim`, NULL actor; #552) and one sign-in audit row are written. The same removal of an already deactivated account writes no
  `users` change and ends nothing.
- **Automation:** automated — Go: `TestSCIMGroups{SQLite,MySQL}` (`add makes admin`, `remove
  recomputes from the remaining groups`, `removing the only group deactivates`, `removing an
  inactive account's only group writes no users change`, `lowered role ends sessions`, `remove all
  deactivates every member left in no mapped group`), `TestSCIMGroupsWired`,
  `TestCredentialRevocationProductionWiring` (`scim_group_removal`, `scim_group_lowered_role`)

### TC-AUTH-090 — A group patch never leaves no active admin, but allows a handover
- **Priority:** P0
- **Type:** security
- **Preconditions:** A valid SCIM token; X the only active admin, in Admins and Sales; Y an active
  sales account in no group; W a deactivated staff account.
- **Steps:**
  1. `PATCH` Admins removing X; then `replace` Admins' members with `[]`; then remove all members.
  2. `PATCH` Admins with `replace` members `[W]`.
  3. `PATCH` Admins with `replace` members `[Y]` (or, from the same start, one patch that adds Y and
     removes X).
- **Expected:** Steps 1 and 2 answer 400 with `scimType` `mutability` and the detail "The last active
  admin must stay an active admin"; `scim_group_members`, X's role and `role_id`, `role_audit_log`
  and `auth_audit_log` are unchanged. Step 3 answers 204: Y becomes admin and X sales, with one role
  audit row each, and exactly one active admin remains. When X is in Admins only, step 1's remove
  would deactivate X and is refused the same way, and step 3 deactivates X and ends its credentials
  while Y becomes admin. The Users patch keeps answering its own last-admin refusal with
  `invalidValue` (TC-AUTH-085).
- **Automation:** automated — Go: `TestSCIMGroups{SQLite,MySQL}` (`last_admin`, `last_admin_deactivation`,
  `replace_to_inactive`, `handover_replace`, `handover_deactivates_old_admin`, `handover_add_remove`), `TestSCIMGroupHandler` (`last_admin_mutability`,
  `last_admin_before_invalid_value`)

### TC-AUTH-091 — Group patches outside the members, group deletion and a create outside the mapping are refused
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A valid SCIM token; the mapping of TC-AUTH-089; a customer and an erased
  account.
- **Steps:**
  1. `PATCH` Admins adding `"abc"`, an unknown id, the customer, the erased account, and a valid id
     next to an unknown one.
  2. `PATCH` Admins with path `displayName`, then `remove` without a path.
  3. `POST /scim/v2/Groups` with `{"displayName":"New"}` (no mapping value), then
     `DELETE /scim/v2/Groups/{id}`, `PUT /scim/v2/Groups/{id}`, and finally a `POST` whose
     `externalId` is `Admins` (Microsoft Entra ID's body: both schemas, `displayName` any other
     text, `meta`), sent twice.
- **Expected:** Step 1 answers 400 `invalidValue` each time and changes no table. Step 2 answers
  400 `invalidPath`, then 400 `noTarget`, without reaching the service. Step 3: the first POST
  answers 400 `invalidValue` with the detail "A Group can be created only for a groups value of
  the single sign-on role mapping" (as do case or trailing-space variants of a value and a value
  removed from the mapping), a POST listing members 400 `invalidValue`, a body that is not one JSON
  object or repeats a key 400 `invalidSyntax`; the DELETE answers 405 with the SCIM error body
  (`"status":"405"`) and `Allow: GET, PATCH`, and the PUT the router's plain 404. The POST of
  `Admins` answers 201 both times with the same Admins group, a `Location` equal to its
  `meta.location`, and writes nothing (`scim_group_members` keeps its row count); `externalId`
  wins over `displayName` when both are values.
- **Automation:** automated — Go: `TestSCIMGroups{SQLite,MySQL}` (`invalid members`,
  `mapping to the customer role`, `create`), `TestSCIMGroupHandler`, `TestSCIMParseGroupPatch`,
  `TestSCIMParseGroupCreate`, `TestSCIMGroupsWired`

### TC-AUTH-092 — Two group patches cannot remove the last two admins together
- **Priority:** P0
- **Type:** security
- **Preconditions:** X and Y, the only two active admins, both in Admins and Sales.
- **Steps:**
  1. Patch A removes X from Admins, then patch B removes Y.
  2. From the same start, B first, then A.
  3. From the same start, A and B released together.
- **Expected:** In each case exactly one patch commits, the other answers 400 `mutability`
  (`ErrSCIMGroupLastAdmin`), exactly one active admin remains, and only the winner's role audit and
  sign-in audit rows exist. Holds on SQLite, MySQL 8 and MariaDB 10.11.
- **Automation:** automated — Go: `TestSCIMGroups{SQLite,MySQL}` (`last_admin_race/a_first`,
  `last_admin_race/b_first`, `last_admin_race/concurrent`)

### TC-AUTH-093 — SCIM serves and changes only accounts linked to the single sign-on issuer
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A valid SCIM token; single sign-on issuer configured. L a local admin created in
  GopherCRM (a password, no identity link, active, holding a refresh token) next to a second active
  admin; O an account linked only under another issuer; P an account linked under the issuer plus a
  trailing space; S an account a SCIM create linked; N an account an OIDC sign-in linked.
- **Steps:**
  1. `GET /scim/v2/Users`, and `GET /scim/v2/Users?filter=userName eq "<L's address>"`.
  2. `GET /scim/v2/Users/<id>` for L, O, P, S and N.
  3. `PUT` L with a mapped role and a new `userName`; `PATCH` L's `userName`; `PATCH` L with `active`
     false; `DELETE` L; `PATCH` O with `active` false.
  4. `POST /scim/v2/Users` with L's address as `userName`.
  5. `PATCH` a group adding L, and replacing the members with a list that names L.
  6. With a membership row of L stored before and L the only active admin, `PATCH` the group removing
     L.
  7. Clear the issuer in Settings → Single sign-on and repeat step 1.
- **Expected:** Step 1: only S and N are listed and counted; the filter on L's address answers
  `totalResults` 0. Step 2: 200 for S and N, 404 `User not found` for L, O and P. Step 3: each
  answers 404 `User not found`; L's and O's email, names, role, `role_id`, `is_active` and
  `token_version` are unchanged, their refresh tokens are not revoked, and no `role_audit_log` or
  `auth_audit_log` row is written. Step 4: 409 `uniqueness`, L unchanged. Step 5: 400 `invalidValue`,
  with `scim_group_members`, roles and both audit tables unchanged, and L is never listed as a group
  member. Step 6: 200; the row is deleted, L stays admin, and no role or sign-in audit row names L.
  Step 7: `totalResults` 0. Holds on SQLite, MySQL 8 and MariaDB 10.11.
- **Automation:** automated (backend) — Go: `TestSCIMUsers{SQLite,MySQL}`,
  `TestSCIMUserWrites{SQLite,MySQL}`, `TestSCIMUserPatch{SQLite,MySQL}`,
  `TestSCIMUserDelete{SQLite,MySQL}`, `TestSCIMGroups{SQLite,MySQL}` (the local-account,
  other-issuer and padded-issuer subtests), `TestSCIMUsersWired`, `TestSCIMUserPatchWired`,
  `TestSCIMUserDeleteWired`, `TestSCIMGroupsWired`

### TC-AUTH-094 — A login email change ends the reset links issued before it (#554)
- **Priority:** P1
- **Type:** functional
- **Preconditions:** An active user holding a spendable reset link mailed to its current address.
- **Steps:** Change the user's login email through `PUT /users/me` (own), `PUT /users/{id}` (an
  admin), SCIM `PUT /scim/v2/Users/{id}` or SCIM `PATCH /scim/v2/Users/{id}` (`userName`), then
  confirm the earlier link. Controls: change only the first name, or send the stored email again;
  confirm the link before the email change; request a new reset after the change.
- **Expected:** After an email change the earlier link is refused with the generic **400**
  (`Invalid or expired reset token`) and the password is unchanged. The token is spent in the same
  write as the email; a refused email write (a taken address) or a rolled-back transaction leaves
  it usable. A name-only edit and an unchanged email keep the link working. A confirmation that
  commits before the change stays in effect. A reset requested after the change is mailed to the
  new address and its link works.
- **Automation:** automated (backend) — `internal/service/auth_reset_email_change_test.go`
  (`TestResetLinkEmailChangeSQLite`, `TestResetLinkEmailChangeMySQL`, which needs
  `TEST_MYSQL_DSN`) and `internal/repository/user_repository_test.go`
  (`TestUpdateFieldsEmailSpendsResetTokens`)
