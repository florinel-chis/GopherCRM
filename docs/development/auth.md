# Authentication

> **For:** Developers changing sign-in, sessions, API keys, single sign-on or SCIM. Part of the [documentation index](../README.md).

Password sign-in, sessions and refresh tokens, API keys, credential issuance and revocation,
single sign-on with OpenID Connect, the SCIM provider and the web client session. Roles and
permissions are in [roles.md](roles.md); the route-by-route permission reference is in
[authorization.md](authorization.md). Part of the developer guide; the index is in [development/README.md](./README.md).

## Authentication flows

| Flow | Code |
|------|------|
| Password login and the failed-attempt count | `internal/service/auth_login.go` `authService.Login` (and `LoginWithTokens`) |
| Refresh-token rotation | `internal/service/auth_refresh.go` `authService.RefreshAccessToken` |
| Logout (refresh revocation, `token_version` increment) | `internal/service/auth_logout.go` `authService.Logout` |
| Password change | `internal/service/auth_password_change.go` `authService.ChangePassword` |
| Password reset request and confirm | `internal/service/auth_password_reset.go` `authService.ConfirmPasswordReset` (and `RequestPasswordReset`) |
| Bearer token check on every request | `internal/service/auth_tokens.go` `authService.ValidateToken` |
| `ApiKey` header check | `internal/service/auth_apikey.go` `authService.ValidateAPIKey` |
| Sign-in audit log, its query and its code registry | `internal/service/auth_audit.go`, `auth_audit_query.go`, `auth_audit_vocabulary.go` |
| OpenID Connect sign-in and identity-provider sign-out | `internal/service/auth_oidc.go` `NewOIDCService`, with the idkit host adapters in `internal/service/auth_oidc_host.go` |
| SCIM token authentication and limits | `internal/middleware/scim_auth.go` `SCIMAuth`, the tokens in `internal/service/scim_token.go`, the SCIM reads and writes in `internal/service/scim_*.go` |
| HTTP handlers | `internal/handler/auth_handler.go` (password flows, refresh, logout) and `internal/handler/auth_oidc_handler.go` (OpenID Connect) |

## Sessions and tokens

**Authentication** — JWT Bearer tokens and API Key header (`ApiKey`). API keys use HMAC-SHA256
hashing (with a legacy SHA256 fallback for migration). Middleware sets the user context. RBAC on routes
comes from `handler.Protected`, which builds each protected route's permission guard from its
`permissions.RouteKeys` entry. An API key whose owner has been erased or deactivated
is rejected at authentication time, and none is issued to such an owner (#342, see *Credential
issuance under the owner lock*).

Lockout: five failed login attempts lock the account for 15 minutes. After an account lock
expires, the next wrong password begins a fresh five-attempt window. The exact expiry is eligible; an active lock still rejects correct credentials. See the
deterministic lockout tests and the real HTTP/SQLite session regression in
[authentication test cases](../testing/01-authentication.md). Bounded auth changes use the
[targeted auth validation profile](../maintainers/validation-and-releases.md), including race tests and
health/login/registration browsers. Releases retain complete validation.

Refresh-token persistence has a separate bounded profile with real SQLite/MySQL rotation
and transaction boundaries, migration checks and health/login/registration browsers. CI
also covers MariaDB. The shared repository interfaces file qualifies only when its Git diff
changes the refresh-token interface alone and the candidate contains all six rotation boundary
roots. See the [validation policy](../maintainers/validation-and-releases.md)
for scope and migration of a stopped execution to the reviewed selector.

A password reset link is spent only by a committed reset. `ConfirmPasswordReset` validates the
complexity policy and hashes the new password before anything is written, so a weak password, or
one bcrypt refuses (over 72 bytes), leaves the link usable. It then runs one transaction through
the transaction manager (`WithTransactionAndRetry`, replayed at most `resetConfirmMaxRetries`
times after a deadlock, a lock wait timeout or a busy SQLite file) and the `WithTx`
repositories: the token is claimed by a conditional
update (`used_at IS NULL AND expires_at > now`, exactly one affected row, else the generic
rejection), the owner is re-read with a locking read (the transaction-bound `UserRepository`, `SELECT …
FOR UPDATE` on MySQL, a plain read on SQLite where the dialector drops the clause) and refused if
inactive or erased, or, on a service built with `WithAuthIdentities` (as `cmd/main.go` does) and
while `security.sso.allow_password` is off (#457, read once before the transaction), if it has a
linked OpenID Connect identity (#444, a plain read of `user_identities`), the password is
written with the lockout cleared, and every refresh token of
the account is revoked. The locking read matters on MySQL: a plain read inside the REPEATABLE READ
transaction answers from the snapshot taken at the token lookup, so an account deactivated or
erased by another connection just after that lookup would still look active and be reset. A failure of the password
update or of the revocation is returned and rolls the claim back, so the same link works on retry;
competing confirmations commit exactly once, and a token that expires or is spent between the
lookup and the claim is refused. A replayed attempt redoes the lookup, the claim, the owner lock
and the writes, so a confirmation that lost a deadlock to an erasure or to a reset request of the
same account ends in the generic rejection (or commits) instead of error 1213; when the bound is
exhausted the last error is returned, the route answers the generic 500 and the link is still
valid. The service refuses the confirmation rather than running the
writes separately when the transaction manager or a repository is not wired, which is why
`NewAuthServiceWithSessions` takes the manager explicitly; `NewAuthService` remains for stateless
JWT callers. Regression roots: `TestPasswordReset{Validation,Atomicity,Concurrent}{SQLite,MySQL}`
in `internal/service/auth_password_reset_regression_test.go` (one-connection in-memory SQLite,
a disposable MySQL schema created from `TEST_MYSQL_DSN`, every wait bounded, every assertion on
committed rows), plus the conditional-claim tests in
`internal/repository/password_reset_token_repository_test.go` and the two-connection
`TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL` in
`internal/service/auth_reset_owner_race_test.go`, which commits the deactivation from a second
connection after the token lookup. The retry itself is pinned by
`TestPasswordResetConfirmRetrySQLite` in `internal/service/erasure_credential_order_test.go`
(a deadlock on the first attempt, on every attempt, and an error that is not retryable). The MySQL roots skip without the
variable and fail when it points at an unreachable server; because the auth checker discovers
its roots from `auth*_test.go` and rejects skips, the bounded auth profile needs `TEST_MYSQL_DSN`
as well.

A refresh token is rotated by the same kind of unit of work (#103). `RefreshAccessToken` runs one
transaction through the transaction manager and the `WithTx` repositories: the presented token is
looked up, the owner is re-read with the same locking read as the reset flow (`FOR UPDATE` on
MySQL, a plain read on SQLite), the token is claimed by `RefreshTokenRepository.ClaimForRotation`,
a conditional update (`is_revoked = false AND expires_at > now`, exactly one affected row, else the
generic rejection), the successor row is inserted and its access JWT is signed. The successor carries the presented
row's `family_id`, `sign_in_method` and `session_started_at` unchanged (#546). A single sign-on
session (#548) has a fixed limit of `session_started_at + 24` hours (`ssoSessionMaxAge`, no
setting): right after the defence-in-depth check and before the owner lock, a presented `oidc` row
at or past that instant, or one without a start, is refused through `ssoSessionExpired` with the
plain generic error, so the transaction rolls back and nothing is written (audit `token_invalid`
with the owner's id). `issueRefreshToken` caps the `expires_at` of every `oidc` row it issues (the
sign-in, each rotation, a password-change renewal) at the same instant, so newer rows already miss
the live lookup past it. `changePasswordInTransaction` applies the same check after the owner lock
and the owner checks and before the claim (past the limit: `session_stale`, nothing written), and
its successor keeps an `oidc` session's method and start while starting a new family. An owner lookup
that fails for a database reason aborts before the claim. A failure of the
successor insert or of the signing rolls the claim back with it, so the presented token stays
usable, no extra row exists and no credentials are returned; the same token succeeds once the
fault is gone. Two requests presenting one token therefore commit exactly one successor: the loser's
claim finds nothing to revoke and is rejected before it could insert anything, and ends nothing
else, because it passed the live lookup. A token presented after a committed rotation is a reuse
(#547): when the live lookup misses, `GetRevokedByTokenHash` reads the revoked row in the same
transaction, and when `HasLaterInFamily` finds a later row of its user and `family_id` (only a
rotation adds one), the owner is locked as above and `RevokeFamily` revokes every live token of the
family, the winner's successor included; the refusal is the same generic one, audited as
`token_reuse` with the owner's id. A revoked token that is the newest of its family (logout,
password change, deactivation, containment) or has an empty `family_id` is the plain refusal and
writes nothing. Access JWTs already issued are not ended by a reuse. The rejections that commit
are a reuse and a dead owner: when the locking read
finds the account inactive, erased or missing, the claim stands as the containment revocation of
that token and the caller still receives the generic `ErrInvalidRefreshToken`. The locking read is
what makes a deactivation committed by another connection after the token lookup visible on MySQL;
a plain read in the REPEATABLE READ transaction would still show the account as active and issue it
a fresh session. `RevokeByTokenHash` stays unconditional and idempotent for logout and
invalidation; it is not the rotation claim. Like the reset, the rotation is refused rather than run
as separate writes when the transaction manager is not wired. Regression roots:
`TestRefreshRotation{Validation,Atomicity,Concurrent}{SQLite,MySQL}` in
`internal/service/auth_refresh_rotation_regression_test.go` (the same one-connection SQLite and
disposable MySQL schema as the reset roots; the MySQL validation root uses two connections for the
owner-deactivated-after-lookup case, the MySQL concurrent root one connection per competitor plus
one for verification; the insert fault is injected through the transaction-bound repository and
the signing fault through an instance-local seam of the service under test, never a global), plus
the claim tests in `internal/repository/refresh_token_repository_test.go` and the call-sequence
cases in `internal/service/auth_session_test.go`. `POST /auth/refresh` answers only
`ErrInvalidRefreshToken` with the generic 401; any other failure is a `503
SESSION_REFRESH_UNAVAILABLE`. A failure before the commit rolled the rotation back, so clients keep
the still-usable token and retry; a failed COMMIT is ambiguous (the rotation may have been applied),
in which case the retry answers 401 and the session ends.

**Lock order for session writes** — every transaction that writes both an account and its refresh
tokens locks the `users` row first and the `refresh_tokens` rows second. The per-flow orders
(login, password reset, rotation, password change, logout, credential issuance) are in
"Global lock order" and the flows of [concurrency.md](concurrency.md). Why users first: `refresh_tokens.user_id` references `users` with a foreign key, so on
MySQL/MariaDB every successor insert takes a shared lock on the owner row; a rotation that claimed
its token before locking the owner deadlocked against a password change (error 1213). New session
and credential flows follow the same order.

## Single sign-on

**OpenID Connect sign-in (#435)** — `POST /auth/oidc/start` and `POST /auth/oidc/callback`
(`internal/idkit/sso`, wired by `internal/service/auth_oidc.go`, and
`internal/handler/auth_oidc_handler.go`) sign in with the
single sign-on settings of #434, re-read on every call. The single-use token pattern above covers
the sign-in attempts too: Start stores only the HMAC of the state (`oidc_login_attempts`, keyed
like the other opaque tokens, ten minutes), and the callback spends the row before it talks to
the issuer: a plain read by the state hash, then one conditional `UPDATE` by id
(`used_at IS NULL AND expires_at > now`, one affected row or the generic refusal) that also
blanks the nonce and the PKCE verifier, as its own committed statement, so a state works once
even when the sign-in is then refused. A row found expired has the two values blanked by its own
conditional `UPDATE` and is refused all the same; if that clearing fails the answer is still the
generic 401 (#453). Rows are deleted one day after their creation by the stale bulk operation
sweep (`service.PruneOIDCLoginAttempts`), not by the sign-in. The same sweep deletes sign-in
audit rows (`auth_audit_log`, #454) older than `AUTH_AUDIT_RETENTION_DAYS` (default 90,
`service.PruneAuthAuditLog`, chunks of 500); `Callback` writes one such row per call after its
transaction and logs `audit_id` with the outcome. A successful `POST /auth/login` or
`POST /auth/refresh` writes one such row too; a refused one (#553), and a refused `ApiKey`
(#466), adds to the row of its bucket (`attempts`, one row per method, reason, account, client
network and UTC minute; see the *Sign-in audit log* section of `docs/development/authorization.md`). The issuer client (`oidc.NewHTTPClient`, `internal/idkit/oidc/httpclient.go`) has a 10 s
timeout, follows no redirect and ignores `HTTP(S)_PROXY`; `NewOIDCService` builds it once, after
the options (`sso.New`), and every sign-in shares it. Tests trust their TLS issuer through
`WithOIDCRootCAs`, and there is no option that replaces the client. The sign-in's discovery keeps
one discovered issuer (`oidc.ProviderCache`, `internal/idkit/oidc/provider.go`) for
`oidc.ProviderCacheTTL` (24 h), keyed by issuer,
app-key mode (`OIDC_ENTRA_APP_SIGNING_KEYS` on an Entra ID issuer) and, in that mode, client id;
a failed discovery is never kept and the mutex is held while a miss fetches. The test issuer
`internal/idkit/oidctest/issuer.go` `oidctest.Issuer`, used by the fixture of
`internal/service/auth_oidc_test.go`, counts discovery and JWKS requests: the subtests
`provider_cache_daily_refresh`, `provider_cache_unknown_kid`, `provider_cache_refetch` and
`entra_app_signing_keys` under `TestOIDCSignIn{SQLite,MySQL}` assert exact counts, and the
fixture's `reset` clears the cache and the counters. After the code exchange
(PKCE, redirect URI `APP_BASE_URL/auth/callback`) and the ID-token checks (signature, `iss`,
`aud`, `exp`, nonce compared in constant time), one governed transaction
(`oidcSignInRunner`, `internal/service/auth_oidc_host.go`) takes
`admin set → users → roles → user_identities → refresh_tokens`: the link rule of
`internal/idkit/sso` reads the `(iss, sub)` identity, and the account store
(`idkit.AccountStore`, implemented by `oidcAccountStore`) re-applies the mapped role with the
governed rules minus the actor-key and escalation checks (`ErrLastAdmin` and
`ErrCustomerLoginRole` refuse; a change writes a `user_role_changed` row with `auth_method`
`oidc`), or provisions the user and its audit row, whose identity the link rule then inserts;
the session issuer last inserts the refresh token and signs the access token from the row it
reads in the transaction. `last_login_at` and
the lockout counters are untouched. Refusals are `ErrSSOSignInFailed` (401) and are logged with an
`error_code` only; the code, state, nonce, verifier, tokens, email, subject and claims are never
logged. Every `oidc_sign_in` line carries `request_id` (the handler passes it on the context,
`utils.WithRequestID`) and `attempt_id` (the attempt row, `0` before Start created it or the
callback claimed it) (#453). Regression roots: `TestOIDCSignInSQLite` and `TestOIDCSignInMySQL`
(`internal/service/auth_oidc_test.go`, one shared subtest table against the in-process TLS issuer
`internal/idkit/oidctest/issuer.go` `oidctest.Issuer`, which checks the PKCE verifier and the
redirect URI; the Microsoft Graph fake and the second host stay in that test file; the MySQL root skips without
`TEST_MYSQL_DSN` and is discovered by the auth checker), `TestOIDCHTTPClient`
(`internal/idkit/oidc`), `TestOIDCEntraTenant` and `TestOIDCResolveEntraGroups`
(`internal/idkit/entra`),
`TestOIDCHandler` (`internal/handler`), `TestOIDCRoutesWired` (`cmd`) and
`TestUserIdentitiesMigration{Files,}` (`cmd/migrate`). The user-facing rules (first-match
mapping, overage, subject whitespace, JIT, erasure) are in
[authorization.md](authorization.md#oidc-sign-in).

**Single sign-on sign-out (#634)** — the callback answers `id_token` next to the token pair, and
the SPA keeps it in the session record (`idToken`). `POST /auth/logout` takes an optional
`id_token_hint` (at most 16384 bytes, else 400 `VALIDATION_ERROR` before anything is revoked);
only after `Logout` committed does the handler ask `OIDCService.EndSessionURL`
(`sso.Service.EndSessionURL`, `internal/idkit/sso/logout.go`, its own 5 s deadline) and answer
`end_session_url`; a refused hint or a failed lookup answers the plain message, and the logout
itself is unchanged (same revocation, `token_version` increment and audit row). The SPA then calls
`window.location.assign` with it, and the provider returns the browser to
`APP_BASE_URL/login`. The unauthenticated `GET /auth/oidc/frontchannel-logout?sid=&iss=` (strict
tier, `Cache-Control: no-cache, no-store`, `Pragma: no-cache`, no frame headers) runs
`sso.Service.FrontChannelLogout`: an `iss` that is sent must equal the configured issuer, and the
host's `idkitSessionEnder` (`internal/service/auth_oidc_host.go`) reads the distinct owners of
the live refresh tokens whose `provider_session_hash` is the HMAC of `issuer + "\n" + sid` and
calls `authService.Logout(userID, "")` for each, so every session of such an account ends, with
one `logout` audit row each and no new lock order. The hash is written by `idkitSessionIssuer`
from the verified `iss` and the ID token's `sid` (absent, non-string or over 256 bytes counts as
none) and copied by rotation and the password-change renewal; the raw `sid` and the ID token are
never stored or logged. MySQL/MariaDB get the column from
`20261129120008_refresh_token_provider_session`. Roots: `TestCallbackProviderSession`,
`TestEndSessionURL`, `TestFrontChannelLogout` (`internal/idkit/sso`), `TestRefreshProviderSession`,
`TestFrontChannelLogout`, `TestFrontChannelLogoutMySQL` (`internal/service`),
`TestLogoutEndSession`, `TestFrontChannelLogout` (`internal/handler`), `TestOIDCRoutesWired_SignOut`
(`cmd`) and `sso.spec.ts` S16.

## SCIM

**SCIM provider tokens and discovery (#468)** — `/scim/v2` is mounted in `setupRouter` outside
the API prefix (`scimGroup`, passed to `setupDependencies`) behind `middleware.SCIMAuth`, which
takes the two SCIM limits of `middleware.NewSCIMRateLimits` (#555): the SCIM token limit counts
accepted requests per token id (`cfg.SCIM`, from `SCIM_RATE_LIMIT_PER_MINUTE` and
`SCIM_RATE_LIMIT_BURST`, defaults 1500 and 150, capped at 6000 and 1000 by
`config.NormalizeSCIMRateLimits`, which the constructor applies too), the SCIM refusal limit
counts refusals per client address (10/min, burst 5), every method counts, and a request over
either gets a 429 in the SCIM error shape with no audit row; then the three discovery documents (`internal/idkit/scim`, built once by
`server.New` in `internal/idkit/scim/server`). Tokens live in `scim_tokens` as `utils.HashAPIKeyHMAC`
hashes (`internal/service/scim_token.go`); the lookup is by hash and the row is accepted only
when `hmac.Equal` holds, because MySQL and MariaDB compare `token_hash` case-insensitively. Each
refusal adds 1 to the `auth_audit_log` row of its reason, client network and UTC minute
(method `scim`; `token_missing`, `token_invalid`, `token_revoked`; #553); neither a token nor a hash is logged. The admin API
(`/settings/scim/tokens`, `settings.scim_manage`) is routed from `internal/handler/scim_routes.go`.
Regression roots: `TestSCIMRoutesWired`, `TestSCIMStrictTierWired`, `TestSCIMTokenTierWired`
(`cmd`), `TestSCIMAuth`, `TestSCIMRateLimits` (`internal/middleware`), `TestLoad_SCIMRateLimit`,
`TestNormalizeSCIMRateLimits` (`internal/config`), `TestSCIMTokenHandler`, `TestSCIMDiscoveryDocuments`
(`internal/handler`), `TestSCIMTokenStore{SQLite,MySQL}` (`internal/service`, discovered by the
auth checker), `TestSCIMTokenSchemaSQLite` (`internal/models`), and the migration roots
`TestSCIMTokensMigrationFiles` (it held the newest-migration assertion from #468, moved from
`TestAuthAuditLogAttemptsMigrationFiles`, until #503 moved it on to
`TestWebhookSecretRotationMigrationFiles`, then #506 to `TestSCIMGroupMembersMigrationFiles`; the
newest migration is now `20261129120001_admin_only_sso_scim_permissions`, whose
`TestAdminOnlyPermissionsMigrationFiles` holds the assertion since #524) and `TestSCIMTokensMigration` (`cmd/migrate`), both
required by the migrations workflow, `scripts/ci/check-go.py migration` and
`scripts/release/check-sql-tests.py`. The rules are in the *SCIM provisioning* section of
[authorization.md](authorization.md#scim-provisioning).

**SCIM HTTP layer (#649)** — the SCIM 2.0 answers come from `internal/idkit/scim/server`, a
`net/http` package that imports only the standard library, `internal/idkit` and
`internal/idkit/scim`. `server.New` builds a `Server` from the host's `Users`, `Groups` and
`Logger`; `Routes` lists its 14 routes; `ServeHTTP` routes on the method and the decoded path
under `scim.BasePath`. The package owns the body reading (any Content-Type, one JSON object of at
most `MaxBodyBytes`, unknown attributes ignored, else 400 `invalidSyntax` without a host call), the
mapping of `ErrNotFound`, `ErrInvalidValue`, `ErrUniqueness` and `ErrLastAdmin` to SCIM answers,
the `Location` header, and the managed-accounts rule, `Manages` and `ExternalID` (byte-exact
issuer). GopherCRM implements the interfaces in `internal/handler/scim_server.go`
(`NewSCIMServer`, over `SCIMUserService` and `SCIMGroupService`; it translates the service
sentinels, `errors.Is(err, apperrors.ErrNotFound)` included, and logs through `utils.Logger`), and
`SetupSCIMRoutes` mounts it with `gin.WrapH` on the same literal routes after a middleware that
puts the request id and client facts on the request context, which the write audit rows read. The
host keeps the token authentication (`middleware.SCIMAuth` with its limits and refusal audit),
`PUBLIC_BASE_URL`, the SQL narrowing and the governed writes. Regression roots: `TestServerRoutes`,
`TestServerErrorMapping`, `TestServerBodies`, `TestServerLogging`, `TestManagedJudges` and
`TestNewRefusesMissingProviders` (`internal/idkit/scim/server`), `TestSCIMRoutesMatchServer`
(`internal/handler`) and `TestIDKitImportBoundary` (`internal/idkit`).

**SCIM user read (#469)** — `GET /scim/v2/Users` and `/Users/:id` are mounted by the same
`SetupSCIMRoutes` (`internal/handler/scim_routes.go`); `internal/idkit/scim/server` does
the query parsing, the status mapping and the `application/scim+json` answer. `internal/service/scim_users.go` (`SCIMUserService`) applies the
userName judge, pages a filtered set in Go and takes `externalId` from the identities under the
effective issuer of the SSO settings service with `server.ExternalID` in `withExternalIDs`, over each
user's rows in ascending id order; the reads are in
`internal/repository/scim_user_repository.go` (`SCIMUserRepository`: managed accounts only, that
is live, non-customer users with a `user_identities` row whose issuer equals the effective issuer
exactly, any `source`, applied in SQL by `whereManagedBy` with a `LENGTH` guard against the PAD SPACE
collation, shared with `SCIMGroupMemberRepository` (#529); identities of a page in one
`issuer = ? AND user_id IN ?` query). The replace, patch and delete and the group patch refuse an
account that is not managed like an unknown id, judged by `server.Manages` in `requireManaged` right
after the users lock. `internal/idkit/scim` holds the User
resource, `ParseUserFilter`, `ParsePaging`, `WriteErrorType` and `UserNameMatches`, the
ASCII-only judge: never replace it with `strings.EqualFold`, whose Unicode folding matches
U+212A and U+017F the way `utf8mb4_unicode_ci` does. Regression roots: `TestSCIMUsersSQLite` and
`TestSCIMUsersMySQL` (`internal/service`, discovered by the auth checker; run the MySQL root on
MariaDB too), `TestSCIMUserHandler` (`internal/handler`), `TestParseUserFilter`,
`TestUserNameMatches`, `TestParsePaging`, `TestUserResource`, `TestWriteErrorType`
(`internal/idkit/scim`) and `TestSCIMUsersWired` (`cmd`).

**SCIM user writes (#470)** — `POST /scim/v2/Users` and `PUT /scim/v2/Users/:id` are mounted by
`SetupSCIMRoutes`; `internal/idkit/scim/server` reads the body
(`http.MaxBytesReader` at 64 KiB, one JSON object, no trailing data, typed attributes, else 400
`invalidSyntax` without calling the service), and `NewSCIMServer` translates
`service.ErrSCIMLastAdmin` (400 `invalidValue`, checked first), `service.ErrSCIMInvalidValue` (400
`invalidValue`), `service.ErrSCIMUniqueness` (409 `uniqueness`) and `apperrors.ErrNotFound` (404)
to the server's sentinels. `internal/service/scim_user_writes.go` holds `Create` and
`Replace`, enabled by `service.WithSCIMUserWrites` on `NewSCIMUserService` (wired in
`cmd/main.go` with the user, role, role-audit and identity repositories, the refresh token
repository (#471), the transaction manager and the sign-in audit writer; without the option both
answer a wiring error, 500). Both run in
`roleGovernance.run` with the lock order of the OIDC sign-in, take the role from the `roles`
entries of the SSO mapping (`rolemap.Match` (`internal/idkit/rolemap/rolemap.go`) with no groups), refuse a missing or `customer`
mapped role, keep the last-admin rule, record `user_role_changed` with
`Actor{AuthMethod: AuthMethodSCIM}` (actor id 0, stored as NULL by `governedTx.record`) and write
one successful `auth_audit_log` row with method `scim` after the commit. Replace checks a changed
address with `SCIMUserRepository.EmailTakenByOther` (live or erased, `LOWER` on both sides).
Regression roots: `TestSCIMUserWritesSQLite` and `TestSCIMUserWritesMySQL` (`internal/service`,
discovered by the auth checker; run the MySQL root on MariaDB too), `TestSCIMUserWriteHandler`
(`internal/handler`), `TestWriteErrorTypeWriteTypes` (`internal/idkit/scim`) and
`TestSCIMUserWritesWired` (`cmd`). The rules are in
[authorization.md](authorization.md#users-create-and-replace).

**SCIM user patch (#471)** — `PATCH /scim/v2/Users/:id` is mounted by `SetupSCIMRoutes`;
the server reads the body as the writes do (64 KiB, one JSON object, no trailing
data) and parses it with `scim.ParseUserPatch` (`internal/idkit/scim/patch.go`, pure), which returns a
`scim.UserPatch` of pointers (nil = untouched) or a `*scim.PatchError` whose `ScimType`
(`invalidSyntax`, `invalidPath`, `noTarget`, `invalidValue`) the server answers with a fixed
detail that never echoes the path or value; service refusals map as for the writes. Paths and
path-less keys match with ASCII-only folding (`asciiEqualFold`, never `strings.EqualFold`).
Since #629 a second table (`ignoredPatchNames`, `ignoredPatchMultiValued`, `enterpriseUserURN`,
checked by `isIgnoredPatchPath`) names the User attributes the CRM does not store, whose
operations are skipped, and `decodeRoles` reads a role `value` that is one JSON object with a
string `value` member (Entra ID's form without `aadOptscim062020`) as that inner string.
`scimUserService.Patch` (`internal/service/scim_user_writes.go`) runs in `roleGovernance.run`
with `admin set → users → roles → refresh_tokens → api_keys`, applies the patch to the locked row, checks
only the touched attributes (Replace's sequence for a changed address), resolves `roles` through
the mapping, keeps the last-admin rule and records `user_role_changed` only on a role change.
Since #552 Replace and Patch also record `user_email_changed` when the written `userName` differs
from the locked row's email and `user_activated`/`user_deactivated` when `active` differs from its
`is_active` (`governedTx.recordEmailChange`, `recordActiveChange`), after the revocation and
before the role row.
A write that sets `active` false (Patch, and Replace since #471, and `scimUserService.Delete`
since #504) calls `revokeAllCredentials` (`internal/service/credential_revocation.go`, #526)
after its `users` write, in the same transaction: `token_version = locked + 1`, every refresh
token and every API key revoked. `WithSCIMUserWrites` takes the refresh token and the API key
repositories (`cmd/main.go` passes `refreshTokenRepo` and `apiKeyRepo`); without either, Patch,
Delete and a deactivating Replace answer a wiring error (500) and write nothing.
Regression roots: `TestSCIMUserPatchSQLite` and `TestSCIMUserPatchMySQL` (`internal/service`,
discovered by the auth checker; run the MySQL root on MariaDB too), `TestSCIMUserPatchHandler`
(`internal/handler`), `TestParsePatch` (`internal/idkit/scim`), `TestSCIMUserPatchWired` (`cmd`) and
`TestConcurrencyDocSCIMUserPatch` (`internal/config`). The rules are in
[authorization.md](authorization.md#users-patch).

**SCIM user delete (#504)** — `DELETE /scim/v2/Users/:id` (no body
read, 204 on success, refusals mapped as for the writes) calls `scimUserService.Delete`, which runs
`admin set → users → refresh_tokens → api_keys → role_audit_log` in `roleGovernance.run` (no `roles` lock, no role
write): an active visible target gets `is_active` false, then `revokeAllCredentials`, then its
`user_deactivated` role audit row (#552), an
already inactive one commits nothing and records no audit row, the last active admin is
`ErrSCIMLastAdmin`. It never calls the erasure path. Regression roots:
`TestSCIMUserDeleteSQLite` and `TestSCIMUserDeleteMySQL` (`internal/service`, discovered by the
auth checker; run the MySQL root on MariaDB too), `TestSCIMUserDeleteHandler`
(`internal/handler`), `TestSCIMUserDeleteWired` (`cmd`) and `TestConcurrencyDocSCIMUserPatch`
(`internal/config`). The rules are in [authorization.md](authorization.md#users-delete).

**SCIM groups (#506)** — `GET /scim/v2/Groups`, `GET`/`PATCH /scim/v2/Groups/:id` (204 on success),
the create `POST /Groups` that resolves a mapping value to its existing group (#630) and the
DELETE 405 are mounted by `SetupSCIMRoutes` from the
server's Groups half (`internal/idkit/scim/server/groups.go`; `NewSCIMServer` translates
`ErrSCIMGroupLastAdmin` to `server.ErrLastAdmin`, answered 400 `mutability` before
`server.ErrInvalidValue`). The groups are
the distinct `groups`-claim values of the effective role mapping (`internal/service/scim_groups.go`,
`SCIMGroupService`); the id is `scim.GroupID` (unpadded base64url), and `internal/idkit/scim/groups.go`
and `group_patch.go` hold the Group resource, `ParseGroupFilter`, `ParseGroupCreate`,
`ExcludesMembers` (`excludedAttributes=members`) and `ParseGroupPatch`; `scimGroupService.Create`
resolves `externalId`, then `displayName`, to a current value and writes nothing. Membership
is the `scim_group_members` table (`models.SCIMGroupMember`, `ensureSCIMGroupMemberCollation`,
`internal/repository/scim_group_member_repository.go`; migration
`20261129120000_add_scim_group_members`). `scimGroupService.Patch`
(`internal/service/scim_group_writes.go`) runs `admin set → users → roles → scim_group_members →
refresh_tokens → api_keys → role_audit_log` in `roleGovernance.run`; it reads the group's rows and the members' other groups
with a `FOR UPDATE` handle so MySQL's REPEATABLE READ snapshot, taken before the admin-set wait,
is not used. It recomputes each added or removed visible member's role with `rolemap.Match`
(`internal/idkit/rolemap/rolemap.go`) over its groups and no roles, and `scimMappedRole`. Since #530 an active member that gets no match is deactivated
instead (`is_active` false, role kept, `revokeAllCredentials` on the locked row; an inactive one
gets no `users` write), and a role write whose old permission set exceeds the new one
(`permissionsExceed(escalationSet(old), escalationSet(new))`) calls `endSessions` after its
`UpdateFields`; the role audit rows follow every `users` write and revocation: a
`user_deactivated` row per deactivated member (#552) and a `user_role_changed` row per role
change, in the order of the `users` writes.
`WithSCIMGroupWrites` takes the refresh token and API key repositories, and `Patch` returns
`errSCIMGroupWritesNotWired` (500) before reading anything when either is nil. The patch refuses
with `ErrSCIMGroupLastAdmin` (standalone, not an `ErrSCIMInvalidValue`) only when admins existed
and none is left, a deactivated account counting as none. Regression roots:
`TestSCIMGroupsSQLite` and `TestSCIMGroupsMySQL` (`internal/service/auth_scim_groups_test.go`,
discovered by the auth checker; run the MySQL root on MariaDB too; the `last_admin_race`
subtests), `TestSCIMGroupHandler` and `TestSCIMDiscoveryDocuments` (`internal/handler`),
`TestSCIMGroupResource`, `TestSCIMGroupID`, `TestSCIMParseGroupFilter` and
`TestSCIMParseGroupPatch` (`internal/idkit/scim`), `TestSCIMGroupMemberSchemaSQLite`
(`internal/models`), `TestSCIMGroupsWired` and the `scim_group_*` subtests of
`TestCredentialRevocationProductionWiring` (`cmd`), `TestConcurrencyDocSCIMGroupPatch`
(`internal/config`), and `TestSCIMGroupMembersMigrationFiles` (it held the newest-migration
assertion until #524 moved it to `TestAdminOnlyPermissionsMigrationFiles`) and
`TestSCIMGroupMembersMigration` (`cmd/migrate`), required by the migrations workflow,
`scripts/ci/check-go.py migration` and `scripts/release/check-sql-tests.py`. The rules are in
[authorization.md](authorization.md#groups).

## Credentials and account writes

**Credential issuance under the owner lock (#342)** — a password-reset token and an API key are
issued only to a live, active owner.

- **Reset request.** `RequestPasswordReset` keeps its plain `GetByEmail` (an unknown or inactive
  address stops there), then runs one `WithTransactionAndRetry` transaction
  (`resetRequestMaxRetries` 3) on `context.Background()` that reads the owner by primary key
  through the user repository bound to `repository.LockingRead(tx)`, the locking clause on a fresh
  `Session` (`lockLiveCredentialOwner`; `FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite).
  The lock falls on the `users` row only: `UserRepository.GetByID` reads the owner's custom role
  and its permissions without a lock, on the same transaction.
  A missing, erased (soft-deleted) or inactive locked row writes nothing. With
  `WithAuthIdentities` (`internal/service/auth_sso_policy.go`; `cmd/main.go` passes `repository.NewUserIdentityRepository`; a nil
  repository skips the check) a plain `UserIdentityRepository.WithTx(tx).ExistsForUser` read then
  refuses an account with a linked OpenID Connect identity the same way (#444, `error_code`
  `sso_account`), and the confirm refuses such an account's token with `ErrInvalidResetToken`
  too. Both run that read only while `security.sso.allow_password` is off (#457): the policy,
  `WithAuthSSOPasswordPolicy` (`internal/service/auth_sso_policy.go`; `cmd/main.go` passes the SSO
  settings service; nil means off), is
  read once before the transaction begins, never inside it, because the SQLite pool holds one
  connection. With the setting on the read is skipped and a linked account is treated like a
  local one. Password login applies the same rule: inside its issuing transaction, after the owner
  checks and before any write, a linked account is refused with the generic 401 (`error_code`
  `sso_account`, nothing written). Otherwise
  `InvalidateAllForUser` and `Create` run through `PasswordResetTokenRepository.WithTx`; a failure
  of either rolls both back. `SendPasswordReset` is called once, after the commit, with the address
  of the locked row. The method returns nil in every case (unknown, inactive, refused, rolled back,
  unwired, mail failure), so `POST /auth/password-reset` keeps its one 200 answer. A service
  without a transaction manager now writes and sends nothing.
  Since #554 the handler does none of this while the client waits: `cmd/main.go` builds a
  `service.PasswordResetQueue` over the auth service and the sign-in audit writer
  (`cmd/password_reset_runtime.go`) and hands it to `AuthHandler.WithPasswordResetQueue`. The
  handler validates the body, calls `Submit` with the request's audit context and answers the
  same 200, whatever `Submit` returns. The queue's one worker (`PasswordResetQueueSize` 100
  waiting requests, handled in submission order) calls `IssuePasswordReset` and then
  `RecordPasswordResetRequest` on a `context.WithoutCancel` copy of the request context, so the
  row keeps the request id and client network. A full or stopped queue drops the request with a
  warning (`reset_queue_full`, `reset_queue_stopped`) and an `unavailable` `reset_request` row
  with no user id; a panic in one item is recovered (`reset_worker_panic`). No queue log line
  carries the email, the token or the link. A handler built without a queue (the handler-only
  setups in `tests/` and `internal/handler`) still issues and audits before it answers.
- **API-key create.** `NewAPIKeyService(apiKeyRepo, userRepo, txManager, apiKeySecret)`;
  `cmd/main.go` passes the shared `userRepo` and `txManager`. `Generate` runs one
  `WithTransactionAndRetry` transaction (`apiKeyCreateMaxRetries` 3) on `context.Background()`
  with the same owner lock and inserts through `apiKeyRepo.WithTx(tx).Create`. A refused owner
  returns an error matching `service.ErrAccountUnavailable` (no row, no key), which `POST /api-keys`
  answers 401 `UNAUTHORIZED` `Unauthorized`; without the user repository or the manager it returns
  `service.ErrAPIKeyIssuanceUnavailable` and writes nothing (500, like every other failure).
- **Outcome.** An erasure (user- or customer-rooted) or a deactivation that commits before the lock
  makes the issuance write nothing; one that commits after it hard-deletes the token or key
  (erasure) or leaves it unusable (`ConfirmPasswordReset` and `ValidateAPIKey` refuse an inactive
  owner).
- **Reset request vs confirm.** The request locks users → password_reset_tokens, the confirm
  password_reset_tokens → users. Confirm first: the request waits on the users row and issues a
  fresh token. Request first: the confirm waits on the token row and its claim is refused
  (`ErrInvalidResetToken`). Both holding their first lock: MySQL ends one with error 1213; a
  request victim is replayed by the retry helper, and so is a confirm victim, whose replay
  finds the token spent by the request and is refused (`ErrInvalidResetToken`). Neither party
  returns the 1213.
- **Lock waits.** A reset request for an account whose users row another transaction holds
  waits for that lock on the password reset queue's worker (#554); the answer does not wait.
  Neither path moves onto the request context (the interfaces stay context-free).
- **Logs.** The refusal, rollback and unwired lines carry `user_id`, `operation`
  (`RequestPasswordReset`, `GenerateAPIKey`) and `error_code` only, plus the error through
  `utils.LogError`; never the address, a name, the token or its hash, the key, its prefix or its
  name.

Roots (`internal/service/credential_issuance_owner_lock_test.go`):
`TestPasswordResetRequestOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
`TestPasswordResetRequestOwnerLockConcurrentMySQL`, `TestPasswordResetRequestVsConfirm{SQLite,MySQL}`,
`TestAPIKeyCreateOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
`TestAPIKeyCreateOwnerLockConcurrentMySQL` and `TestCredentialIssuanceOwnerLockLogsOmitPersonalData`;
`TestAPIKeyCreateOwnerUnavailableMapping` (`internal/handler/apikey_owner_lock_test.go`);
`TestCredentialIssuanceOwnerLockAPISQLite` (`tests`); `TestAPIKeyOwnerLockProductionWiring` (`cmd`).
On SQLite a competitor commits through the instance-local seams `resetBeforeLock` and
`apiKeyService.beforeLock`, between the lookup and the transaction, or after the issuance; the
MySQL roots run on a disposable `TEST_MYSQL_DSN` schema built by the real migrations, see each
wait in `information_schema.PROCESSLIST` on two consecutive polls and assert that no ordering
outside the interleaved request/confirm case ends in error 1213. The acceptance run sets
`TEST_MYSQL_DSN`; `make verify-backend` has no DSN and skips them. Registering the new roots in
`scripts/ci/check-go.py` and `scripts/ci/check-auth.py` is an operator follow-up.

**Login issuing transaction (#336)** — on a service built by `NewAuthServiceWithSessions`,
`LoginWithTokens` and `Login` keep the plain `GetByEmail` and bcrypt (against a dummy hash for an
unknown address) before any transaction, so the timing and the generic errors are unchanged. A
wrong password (#545) then runs its own `WithTransactionAndRetry` root (`loginMaxRetries` 3) that
locks the owner `users` row by primary key and writes `failed_login_attempts`/`locked_until` from
that row only (`recordLoginFailureInTransaction`): an erased row or an active lock set by a
parallel attempt writes nothing, an expired lock starts a fresh window, and the fifth counted
failure sets the 15-minute lock, so parallel failed attempts each count. A failure to record it
is logged with `error_code=login_failure_record_failed` and the login still answers
`invalid credentials`. A verified password then starts one `WithTransactionAndRetry` transaction (`loginMaxRetries` 3) that
locks the owner row by primary key (`FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite) and
decides on that row only: a hash other than the one bcrypt verified, `is_active` false or an
erased (soft-deleted) row is the generic `invalid credentials`, and a `locked_until` after the
login clock is the lockout error. With `WithAuthIdentities` and while
`security.sso.allow_password` is off (#457; the policy is read once before the transaction), a
plain `UserIdentityRepository.WithTx(tx).ExistsForUser` read follows those checks and refuses an
account linked to single sign-on with the generic `invalid credentials` (`error_code`
`sso_account`), in the order users → (read) user_identities → refresh_tokens. Each refusal rolls
back with nothing written. Otherwise one
`UpdateFields` writes `last_login_at` and clears `failed_login_attempts`/`locked_until`, the
refresh token is inserted under a savepoint (`LoginWithTokens` only; `Login` never issues one) and
the access JWT is signed from the locked `token_version`. A non-retryable failure of the insert
rolls back to the savepoint and the login answers an access token with an empty refresh token
(the documented JWT-only fallback, logged with `error_code=refresh_token_insert_failed`); a
retryable one (deadlock, lock wait, SQLite busy) comes back tagged `utils.ErrNestedRetryDeferred`
and the whole root is replayed; a signing failure rolls everything back. A password change, reset,
deactivation or erasure that commits before the lock therefore refuses the login, a logout lets it
through with the incremented `token_version`, and one that commits after it ends the session it
issued the usual way. The log lines of the transaction carry `user_id`, `operation` and
`error_code` only. A service built by `NewAuthService` (no transaction manager) keeps the legacy
unlocked statements and issues no refresh token. `POST /auth/login` still answers every error
with 401 `Invalid email or password`. Roots: `TestLoginOwnerLockDecisions{SQLite,MySQL}`,
`TestLoginOwnerLockAtomicity{SQLite,MySQL}`, `TestLoginConcurrentCredentialChanges{SQLite,MySQL}`
(login against a password change, a reset confirmation, `UserService.Update(is_active=false)`,
`UserService.Delete` and a logout, in both orderings), `TestRefreshRotationConcurrentCredentialChanges{SQLite,MySQL}`
(rotation against a reset, a deactivation and an erasure, in both orderings),
`TestLoginOwnerLockLogsOmitPersonalData` and `TestLoginUnwiredServiceUnchanged` in
`internal/service/auth_login_owner_lock_test.go`, and, over HTTP, `TestLoginOwnerLockAPISQLite`
(`tests/auth_login_owner_lock_test.go`), and for the failure count
`TestLoginFailedAttemptsConcurrent{SQLite,MySQL}`, `TestLoginFailedAttemptsRecordFailureLogSQLite`
and `TestLoginWrongPasswordErasedDuringRecordSQLite`
(`internal/service/auth_login_failed_attempts_test.go`). On SQLite the competing write runs through the
instance-local `loginBeforeLock` seam, between the lookup and the transaction; the MySQL roots run
on a disposable `TEST_MYSQL_DSN` schema built by the real migrations, see each wait in
`information_schema.PROCESSLIST` on two consecutive polls and assert that no ordering ends in
error 1213. They skip without the variable and fail when it is unreachable. `scripts/ci/check-auth.py`
discovers these files through its `auth*_test.go` patterns.

**Password change** (#116) — `ChangePassword(userID, current, new, refreshToken)` verifies the
current password and hashes the new one before any transaction, then runs one transaction: locking
owner read (missing, erased or inactive → `ErrAccountUnavailable`, 401), the locked hash must still
be the one the password was verified against (otherwise a concurrent change won →
`ErrInvalidCurrentPassword`, 400), the presented refresh token must be a live token of the caller
and is claimed (otherwise `ErrSessionStale`, 409 `SESSION_STALE`, nothing written), the password is
written with `UpdateFields`, every refresh token of the user is revoked, and the successor is
inserted and its access token signed. Any failure before the commit rolls back everything; a failed
COMMIT (`utils.ErrCommitFailed`) is ambiguous — the server may have applied the change — and is
answered 500 without credentials, so the client cannot assume either outcome. Without a refresh token —
and always for API-key requests, which the auth middleware records under
`service.AuthMethodContextKey` — every token is revoked and nothing is issued. The password write
also increments `users.token_version`, so every access JWT issued before the change, the caller's
own included, is refused once it commits; the renewed access token is signed with the new value
(see *Access-token revocation* below). Roots:
`TestChangePassword{Validation,Renewal,Atomicity,ConcurrentRefresh}{SQLite,MySQL}` and
`TestRefreshRotationLocksOwnerBeforeClaimSQLite` in
`internal/service/auth_change_password_regression_test.go`; the MySQL concurrency root migrates a
disposable schema with the real `migrations/` (foreign key and indexes as in production, server
default REPEATABLE READ, checked portably on MySQL 8 and MariaDB 10.11) and forces both orderings
of a password change against a refresh of the presented token and of a sibling token. Each
ordering parks the first request inside its transaction, waits for the second to signal that it is
about to run its locking owner read, and releases the first only once
`information_schema.PROCESSLIST` (MySQL and MariaDB alike) shows that `FOR UPDATE` still executing
on two consecutive polls — i.e. blocked on the row lock. On SQLite the single pooled connection
serialises the two requests instead, so there is no lock wait to observe.

**Access-token revocation** (#236) — every access JWT carries the owner's `users.token_version`
(`bigint unsigned NOT NULL DEFAULT 0`, `json:"-"`) as the integer claim `token_version`, signed by
`GenerateJWT`. `ValidateToken`, which already reads the user row on every Bearer request, checks the
claim's shape before that read (absent reads 0; a value that is not a JSON number, not integral,
negative or above 2^53-1 is refused before any conversion to `uint`) and, after the unchanged
signature, expiry, missing-user and `is_active` checks, refuses a claim that differs from the stored
value with `service.ErrAccessTokenRevoked`; `middleware.Auth` answers it with the same 401 `Invalid
credentials` an expired token gets. The sentinel and the claim helpers live in
`internal/service/access_token_version.go`. The counter is incremented by exactly one through
`UpdateFields` under the users-row lock, inside the transaction the operation already runs, by
`Logout` and by the callers of `revokeAllCredentials` (#526, below): the password change, the
reset confirmation, the governed deactivation and the SCIM deactivations. `Logout` is one
transaction of its own (locking owner read, then the refresh-token revocation — every token
without a body, otherwise only the caller's own presented token — then the increment; a failure of
either rolls both back). The increment happens for Bearer and API-key callers alike, with or without
a body. **`Logout` refuses with an error, answered 500 by the handler, and writes nothing when the
transaction manager or the refresh-token repository is not wired** (a service built with
`NewAuthService` included); a caller the scoped locking read no longer finds (erased) gets nil and
nothing is written. Refresh rotation and login never change the counter: the rotation and a wired
login (#336) sign the value their locking read returned, and the access token it replaces stays valid until it expires. A
role change does not increment it, logout does not affect API keys, and there is no per-token
denylist. The log lines of the rejection and of the increments carry `user_id` and `operation` only.
MySQL and MariaDB get the column from `20261109120000_add_user_token_version` (added only when
missing), SQLite from `MigrateDatabase`; existing rows read 0, which is also what a token signed
before the claim existed reads, so an upgrade ends no session. Roots, in
`internal/service/auth_access_token_revocation_test.go` unless noted (the MySQL roots use a
disposable `TEST_MYSQL_DSN` schema and skip without it): `TestAccessTokenVersionClaim`,
`TestAccessTokenRevokedBy{Logout,PasswordChange,PasswordReset}{SQLite,MySQL}`,
`TestLogoutRequiresSessionStore`, `TestAccessTokenSurvivesRefreshRotation{SQLite,MySQL}`,
`TestLogoutLocksOwnerBeforeRefreshTokens{SQLite,MySQL}` (the MySQL root runs on the real migrations
and forces a logout against a rotation of the same user in both orderings, each wait seen in
`information_schema.PROCESSLIST` on two consecutive polls, with no error 1213),
`TestAccessTokenRevocationLogsOmitPersonalData`; `TestUserRepositoryTokenVersion{SQLite,MySQL}`
(`internal/repository`); `TestUserTokenVersionJSONHidden`, `TestUserTokenVersionSQLiteUpgrade`
(`internal/models`); `TestUserTokenVersionMigrationFiles`, `TestUserTokenVersionMigration`
(`cmd/migrate`); and, over HTTP, `TestAccessTokenRevocationAPISQLite`
(`tests/auth_access_token_revocation_test.go`). The Database migrations workflow runs the
migration roots.

**One step ends every credential** (#526) — `revokeAllCredentials`
(`internal/service/credential_revocation.go`) takes `credentialStores` (the users, refresh token
and API key repositories, all bound to the caller's transaction; `users` is the handle the caller
already writes the owner through) and the owner row the caller locked. In the documented order
users → refresh_tokens → api_keys it writes `token_version` + 1 (and updates the in-memory owner,
so a token signed afterwards carries the new value), calls `RefreshTokenRepository.RevokeAllForUser`
and then `APIKeyRepository.RevokeAllForUser(userID, at)`, one `UPDATE` that sets `is_active` false
and `revoked_at` on every live key whose `revoked_at` is NULL (an inactive legacy key included; a
key revoked earlier keeps its first `revoked_at`). Every error is wrapped with `%w` and rolls the
caller back; the helper logs nothing. Its callers, after their own `users` write: the password
change (`changePasswordInTransaction`, which writes only `password` itself and issues the renewed
successor after the helper), the reset confirmation (`confirmPasswordResetInTransaction`), the
governed deactivation (`governedWrite` with `is_active` false, wired by
`WithUserCredentialRevocation`), the SCIM deactivations and the SCIM group patch that leaves an
active account in no mapped group (#530). The first two steps are their own helper,
`endSessions` (`token_version` + 1, then `RefreshTokenRepository.RevokeAllForUser`), which
`revokeAllCredentials` calls before the API keys and which the SCIM group patch also calls alone
for a role that lacks a permission the old one had (API keys keep working: each request re-reads
the owner). `ChangePassword` and
`ConfirmPasswordReset` refuse, writing nothing, on a service without the API key repository.
`Logout` keeps its own revocation and never touches API keys; the refresh-rotation containment,
the erasure credential purge and the plain `UserService.Update` are unchanged. Which event ends
which credential is tabled in [authorization.md](authorization.md#which-events-end-which-credentials).
Roots: `TestCredentialRevocation{SQLite,MySQL}` (`internal/service`, discovered by the auth
checker; run the MySQL root on MariaDB too), `TestCredentialRevocationProductionWiring` (`cmd`),
`TestAPIKeyRepository_RevokeAllForUser` (`internal/repository`) and, over HTTP,
`TestAccessTokenRevocationAPISQLite`.

**`PUT /users/me` refuses password fields** (#254) — `POST /auth/change-password` is the only
authenticated way to change a password. `UpdateMeRequest` declares `password` and `new_password`
as `json.RawMessage`, so a non-nil field means the key was present, whatever its value (`null`
included) and letter case. After a successful bind, either of the two is answered 400
`PASSWORD_FIELD_NOT_ACCEPTED` with one Warn line that omits the value, and the service is not
called, so profile fields beside it are not written either, also when the body carries a new email
and the correct `current_password`; other unknown keys stay ignored. A body that fails binding
keeps its `VALIDATION_ERROR` or `BAD_REQUEST`. `UserService.Update` has no password branch and
ignores a `password` key in its map, and `UpdateUserRequest` has no field that sets a password, so
`PUT /users/{id}` ignores the key too.

**An own email change needs the current password** (#394) — the login email is where a password
reset goes, so an account changes its own only with its password. `UpdateMeRequest` and
`UpdateUserRequest` carry `current_password` as a plain string (`null` and `""` count as absent; a
non-string is a binding `BAD_REQUEST`). `PUT /users/me` always, and `PUT /users/{id}` when the id
is the caller's own (Bearer token and API key alike), call `UserService.CheckOwnEmailChange` after
binding and before either write, `Update` or the governed `UpdateAs`, but only for a body that
carries an `email`. The service reads the account and answers nil when the email is the stored one
(the comparison `Update` makes); otherwise `apperrors.ErrCurrentPasswordRequired` (400
`CURRENT_PASSWORD_REQUIRED`) or `apperrors.ErrCurrentPasswordIncorrect` (400
`INVALID_CURRENT_PASSWORD`), each with one Warn line that carries neither the value nor an email,
and nothing is written. The guard answers before the duplicate lookup, so it does not confirm an
address to a credential without the password. When the email does not change, or the account is
another one, `current_password` is ignored and never verified. A wrong value is not counted toward
the login lockout, as on `POST /auth/change-password`; no session is revoked and no mail is sent.
Roots: `TestUpdateMeEmailChangeNeedsCurrentPassword` (`internal/handler/user_me_password_test.go`)
and, on the real stack, `TestOwnEmailChangeNeedsCurrentPassword`,
`TestOwnProfileUpdateWithoutEmailChangeNeedsNoPassword`, `TestOtherUsersEmailChangeNeedsNoPassword`
(`tests/users_me_password_test.go`). Roots of the refusal of #254: `TestUpdateMeRefusesPasswordFields`,
`TestUpdateMeProfileFieldsStillApply`, `TestUpdateMeRefusalLogOmitsPassword`
(`internal/handler/user_me_password_test.go`), `TestUserServiceUpdateIgnoresPasswordKey` and, on
real middleware, repositories and SQLite, `tests/users_me_password_test.go`.

**User writes never upsert** — `UserRepository` has no full-struct update. GORM's `Save` falls back
to an INSERT … ON CONFLICT when its UPDATE matches no row, so a login that read an account before
an erasure wrote the erased name, email and hash back and cleared `deleted_at`, and any stale
struct could put an old password hash back. `UpdateFields(id, columns)` writes only the named
columns (a closed allowlist) with `WHERE id = ? AND deleted_at IS NULL` and reports
`gorm.ErrRecordNotFound` when no live row matched (MySQL reports changed rows, so a zero count is
re-checked). Login bookkeeping (the failure count, and on a wired service the last-login write
of the issuing transaction), `UpdateLastLogin` (the unwired login), the reset, the password change
and `UserService.Update` all go through it; a login whose account vanished underneath it is
refused (`TestLoginErasedDuringAuthentication{SQLite,MySQL}` in
`internal/service/auth_login_erasure_regression_test.go`). A wired login re-checks the password
hash and the account state under the owner lock before it issues its session (#336, see *Login
issuing transaction*). A write that names `email` (#554; callers add it only when the address
changes: `UpdateAs` for `PUT /users/me` and `PUT /users/{id}`, the ungoverned
`UserService.Update`, the SCIM replace and patch) also spends every unused reset token of the
user (`PasswordResetTokenRepository.InvalidateAllForUser`) in one `r.db.Transaction` with the
users UPDATE: a root transaction on a plain handle, a SAVEPOINT on a transaction-bound one. The
token write runs only after the UPDATE matched the live row, and a failed users write (duplicate
email, not found) or a caller rollback changes no token (`TestUpdateFieldsEmailSpendsResetTokens`
in `internal/repository`, `TestResetLinkEmailChange{SQLite,MySQL}` in `internal/service`).

**Customer writes never upsert (#256)** — `CustomerRepository.Update` is one `UPDATE … WHERE id = ?
AND deleted_at IS NULL` of named columns: the names, email, phone, company, website, position,
address, city, state, country, postal code, notes, `company_id`, `assigned_to_id` and `updated_at`
(the handle's `NowFunc`, copied to the struct). `id`, `created_at`, `user_id`, `deleted_at` and the
preloaded `User` and `CompanyRecord` are not written, and it never inserts. A zero count is
re-checked with a locking read, not a plain one: MySQL reports changed rows, so an identical write
affects zero rows and returns nil, while a plain read in a REPEATABLE READ transaction could still
show a row whose erasure has committed and turn the lost write into a false success. No live row
returns `gorm.ErrRecordNotFound`; `CustomerService.Update` and `Assign` log one Warn line with ids
only and return `apperrors.ErrNotFound` (`Update` is always one transaction, #408/#615: a
locking read of the live customer, which answers a missing row first with the same error, then the
write and the `customer.updated` outbox rows; its email and company checks stay plain reads before
the transaction), which `PUT /customers/{id}` and
`POST /customers/{id}/assign` answer with 404 `NOT_FOUND`. An update that commits before an erasure
is scrubbed by it, whichever of the customer, converted-lead or customer-role-login deletes it came
through. Live customers stay last-write-wins on purpose: two edits, a stale `PUT` undoing an assign
or the reverse, and a stale write putting back a `company_id` that `UnlinkCustomers` cleared. None
of these restores anything on an erased row, and `Assign` stays a plain write without a transaction. Roots
(`test/integration/erasure_atomicity_test.go`):
`TestCustomerUpdateLostToErasure{SQLite,MySQL}`, `TestCustomerAssignLostToErasure{SQLite,MySQL}`,
`TestCustomerWriteAfterErasure{SQLite,MySQL}`, `TestCustomerWriteConcurrentWithErasure{SQLite,MySQL}`
(the MySQL root confirms both blocked orderings in `information_schema.PROCESSLIST`),
`TestCustomerUpdateLiveRow{SQLite,MySQL}`, `TestCustomerUpdateDuplicateEmail{SQLite,MySQL}` and
`TestCustomerLostWriteLogsOmitPersonalData`; `TestCustomerHandlerUpdateMapsServiceErrors`
(`internal/handler/customer_update_errors_test.go`). The MySQL roots use a disposable schema on
`TEST_MYSQL_DSN`, skip without it and fail when it is unreachable. CI limit: those MySQL roots are
discovered by `scripts/ci/check-go.py transaction`. CI runs that checker only on a `full` run (a release, or a manual dispatch with `full: true`), on MySQL 8 and MariaDB 10.11. On a pull request the database gates job runs these roots on MariaDB 10.11 only when `scripts/ci/check-changed-go.py` selects their package, without `-race` and without rejecting a skip, and the backend job has no DSN and skips them.

## Web client

**Web client session** (#115, #184, `gocrm-ui/src/api/client.ts`) — the `ApiClient` owns the
stored session.

- **One record** (`src/api/sessionRecord.ts`). The session is one JSON record under
  `gophercrm_session`: `{ v: 1, id, access, refresh, subject, persist }`, in `localStorage` when
  "Remember me" was ticked and in `sessionStorage` otherwise. `startSession` removes everything
  from both storages, then writes the record with a fresh id (32 lowercase hex characters from
  `crypto.getRandomValues`); `subject` is the access token's `user_id` claim, read unverified only to
  tell sessions apart. A refresh or password change rewrites the whole record in one `setItem`,
  keeping the id, the subject and the storage. The record is read whole from one storage,
  sessionStorage first, so a session without a refresh token never takes one from the other
  storage. Every write also sets the legacy keys `gophercrm_token` and (or removes)
  `gophercrm_refresh_token` in the same storage, after the record. They are write-only mirrors for
  the browser specs: the client never reads a token from them while a record exists, but a record
  whose `gophercrm_token` mirror is missing is no session (that is how the specs sign out). Such a
  read removes nothing; only `clearTokens()` or an expiry whose snapshot matches the record does. A
  malformed record (not JSON, `v !== 1`, no string id or access token) is removed with its mirrors.
  When neither storage holds any record, a legacy two-key session is migrated once: the storage
  holding `gophercrm_token` (sessionStorage first) with its own refresh token or none, under the
  deterministic id `legacy-<sha256 of the refresh token, or of the access token>` so two tabs
  migrating at once agree (a synchronous SHA-256, because `crypto.subtle` is asynchronous and absent
  on plain HTTP). A storage holding only a refresh token is not migrated and the key is removed.
- **Snapshots.** Every operation — a request, a refresh, a password change, a logout — captures a
  snapshot when it is requested (before waiting for any lock): this tab's generation and the record's
  session id (with the account and refresh token read alongside). Before it sends, retries or
  writes anything, storage must still hold that session: same generation, same id. Another tab's
  refresh keeps the id, so the operation follows it; another tab's sign-in (new id) or sign-out (no
  record) stops it with `SessionChangedError`, and a late pair is kept privately, only for
  revocation. Sign-in, sign-out and an expired session start a new generation; a refresh or
  password change renews the current one. Requests are replayed (once, marked `_retry`) only under
  the session they were sent with.
- **The session lock** (`src/api/sessionLock.ts`). Every refresh and password change runs as a
  holder of one origin-wide exclusive lock: `navigator.locks.request('gophercrm-session', { mode:
  'exclusive', signal }, fn)`. Without `navigator.locks` (a non-secure context such as the bundled
  plain-HTTP nginx) `createSessionLock()` falls back to an in-tab promise-chain lock, and tabs are
  coordinated only by the record checks below. `ApiClientOptions.sessionLock` injects the lock;
  tests pass one `createMemorySessionLock()` (with a read-only `acquisitions` counter) to several
  clients. In one tab all 401s of a session share one refresh. Once it holds the lock, a refresh
  re-reads the record: if it holds another access token than the one the failed request carried,
  another refresh (in any tab) already renewed the session, and that token is adopted without
  calling `/auth/refresh`. The password change reads the record after acquiring the lock, so it
  presents the pair another tab rotated to; it renews an expired access token through the internal
  refresh inside the same lock hold (the lock is never taken twice) and rebuilds its body from the
  renewed pair. A 409 `SESSION_STALE` from the server still surfaces.
- **Failures.** The refresh deadline (15 s) counts from the moment the refresh is requested, the
  wait for the lock included: a refresh still queued at the deadline is withdrawn (its holder never
  runs, nothing is sent) and every waiter rejects with `RefreshTimeoutError`, even if the
  transport ignores the abort. A 400/401 from refresh ends the session (record removed,
  `onSessionExpired` once in that tab) only while the record still holds the refresh token that
  was presented: if the record holds another refresh token of the same session id (a tab whose
  lock was not shared spent it first), its access token is adopted; if it holds another session or
  none, nothing is cleared and the waiters reject with `SessionChangedError`. Another tab notices an
  ended session on its next request, whose refresh ends without calling `/auth/refresh`. A timeout,
  network failure or 5xx keeps the tokens.
- **Logout** (`endSession`) removes the record at once, without waiting for the lock, so another
  tab's refresh cannot delay it; that refresh then stores nothing and its waiters reject with
  `SessionChangedError`. Logout waits for the refreshes and password changes of this tab's ending
  generation still running. If one of them spent the stored refresh token, its successor pair —
  kept privately, never stored, and used only when it was obtained for the session id being ended —
  authenticates the revocation. Otherwise the captured access token is used, and if it has expired
  the client renews privately once, so the no-body revoke-all is still authenticated.

Other open tabs are not updated live on a sign-in or sign-out; they notice on their next request.
Tests drive the real interceptors through an injected adapter: `src/api/client.refresh.test.ts`
(single tab), `src/api/client.sessionRecord.test.ts` (the record, including a read injected between
the record write and the mirror write), `src/api/client.crossTab.test.ts` (two clients sharing
storage and one memory lock, in both response orders) and `src/api/sessionLock.test.ts`.

**Authentication is header-only** — `middleware.Auth` reads the `Authorization: Bearer` header or
the `ApiKey` header and nothing else. No cookie credential is accepted, so the browser attaches no
credential on its own and CSRF does not apply; the server therefore ships no CSRF protection, and
the unused middleware, token codec and cookie helpers that once sat in the tree were removed. The
frontend keeps its token in `localStorage`/`sessionStorage` and sends it in the `Authorization`
header. `tests/auth_integration_test.go` pins the position: a valid access token sent only in
cookies answers 401. Any future cookie session must ship with session-bound CSRF protection (see
#261).
