# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Settings > Webhooks has buttons for the three webhook admin routes of 1.3.0. Each endpoint row
  has "Send test ping", which shows the outcome (`succeeded` with the HTTP status, or `failed` with
  the error code and the HTTP status or "no response"), and "Rotate secret", which asks for a
  confirmation and then shows the new signing secret once, with the time until which the previous
  secret keeps verifying. Like the create secret, the new secret is kept only while its dialog is
  open: never in the query or mutation cache, browser storage, the URL or the console. The
  delivery log has an Actions column with a "Redeliver" button on `succeeded` and `failed` rows;
  a refusal (409) shows the server's message. The create-time secret notice now points at Rotate
  secret instead of deleting and registering the endpoint again. No API change (#727).
- `docs/SERVER_PROVISIONING.md` has a complete Apache 2.4 setup next to nginx under "Reverse proxy
  and TLS" (#749): the modules, a port-80 redirect site, `certbot certonly --apache`, and an HTTPS
  site with the SPA fallback, the browser headers on the web app only, `/quote-response` kept out
  of the access log, the client address from `REMOTE_ADDR` (plus a Cloudflare variant that keeps
  it as the fallback and needs a firewall limiting the origin to Cloudflare) and the timeout and
  body limit of the nginx site (the body limit for proxied requests is a `Content-Length` check,
  because `LimitRequestBody` does not reach them; chunked bodies stay bounded only by the
  backend), whose `client_max_body_size 1m` and `proxy_read_timeout 60s`
  defaults are now written out. README, SETUP, DOCKER and FORKING link the section. Docs only.
- Administrator user-guide pages for webhooks, API keys and the sign-in audit (#731):
  `docs/user-guide/permissions-access/webhooks.md` (the Settings > Webhooks screen with the test
  ping, secret rotation, the delivery log and redelivery, pausing and where its state is read, the
  event types, delivery and retries, and signature verification), `api-keys.md` (creating, using
  and revoking a key) and `sign-in-audit.md` (the filters and columns, retention, and every method
  and reason code). The guide overview links them. The README webhook section is now a short
  summary that links the new page, which holds the text that was there.
  `scripts/ci/check-user-guide.py` checks the three pages, resolves webhook event names against the event catalogue in
  `internal/webhook/catalog.go`, and checks that the sign-in audit page names every method and
  reason code of `internal/service/auth_audit_vocabulary.go` and no other. Docs and checker only.

### Changed

- Operator documentation for single sign-on and SCIM (#729). `docs/SERVER_PROVISIONING.md`,
  `docs/DOCKER.md` (new section "Single sign-on and SCIM") and `docs/FORKING.md` name the addresses
  to register with the identity provider (`APP_BASE_URL/auth/callback`, `APP_BASE_URL/login` and
  the front-channel logout URL under `/api/v1`), the SCIM base URL `PUBLIC_BASE_URL/scim/v2`
  outside `/api/v1` with the nginx and Apache proxy rules, and the environment-only `OIDC_*`,
  `SCIM_*` and `AUTH_AUDIT_RETENTION_DAYS` settings with how to pass them in a Compose override.
  FORKING section 4.2 lists every stored value that depends on `API_KEY_SECRET` (SCIM tokens,
  webhook signing secrets, the stored single sign-on client secret, client certificate and role
  mapping, quote response links). SETUP, DEVELOPMENT and FORKING section 5.2 say that `/health`
  and `/scim/v2` sit outside the API prefix, and the SCIM base URL steps of the user guide link
  the proxy setup. Docs only.
- Documentation states one Node minimum, Node.js 22 or later, in README, SETUP, FORKING, the UI
  README and the runner README (#737). The per-release SQLite upgrade notes are written once, in
  `docs/DOCKER.md` "Upgrading the SQLite database", now covering a binary run from source as well
  as the image, and README links there. Quote PDF setup is described once, in
  `docs/SERVER_PROVISIONING.md` "Quote PDF output" (which gains the development-run notes), with
  links from README, SETUP, DOCKER, FORKING and `.env.example`. The README Documentation table is
  the single index of the guides: it lists every guide, and the "Docs:" line and the "Fork it"
  link list are gone.
- `README.md` is brought up to 1.3.0. It describes four system roles plus custom roles managed in
  Settings → Roles, replaces "Admin Features" with an "Administration and settings" section that
  names every Settings screen and the permission it needs, lists the Deals configuration tab and
  re-measures the fork figures on `bbc5c81f`. The feature list gains single sign-on, SCIM
  provisioning, webhooks, audit logs and API key expiry. The API overview names `/health` and
  `/scim/v2` as the routes outside `API_PREFIX`, points to the SCIM provisioning page, spells out
  every route of the API reference in full (roles and the permission catalogue, user activate and
  deactivate, the single sign-on and SCIM token settings, the sign-in audit, front-channel logout,
  forms and labels; AEO details stay in the reference) and documents `id_token_hint` and
  `end_session_url` on logout. The project tree lists every `internal/` package. Docs only (#730).
- User guide corrections in `docs/user-guide/permissions-access/`: the roles page gives the Admin
  role 76 permissions, the count of the system-role definition; the SCIM behaviour is described
  once, on the SCIM provisioning page, and the FAQ, the single sign-on page and the overview link
  to it; the single sign-on troubleshooting table lives on the single sign-on page, and the
  testing page keeps only its test-specific row and links to it. Docs only (#732).
- `docs/ROADMAP.md` lists only what is still open (#734). Shipped items are removed or narrowed
  (the Profile wording, webhook secret rotation, items marked done, the sales pipeline and the
  lead CSV export), the access-token bullet lists every event that ends access tokens today, the
  master-secret note covers the single sign-on and webhook secrets, the webhook items leave the
  forms cuts, and new follow-up sections cover outbound webhooks, single sign-on and SCIM, audit
  and API keys, and the quote PDF limits. The Vitest timeouts under load and the API defects
  found while documenting move to `docs/DEVELOPMENT.md`, and `docs/testing/07-users.md` points
  there. Docs only.
- `docs/ADMIN_TESTING.md` is retired because it no longer matched the end-to-end suite.
  `gocrm-ui/e2e/README.md` is the guide to that suite and now also lists the
  `test:e2e:admin:headed` and `test:e2e:admin:suite` scripts and the failure artifacts (screenshots,
  videos, traces on retry). `docs/testing/README.md`, the README documentation table and
  `docs/DEVELOPMENT.md` point to it (#736). Docs only.
- The 1.3.0 section of this changelog has one heading of each kind (Added, Changed, Removed,
  Fixed, Security), each entry kept word for word under its kind, and a new Upgrade notes
  subsection that collects the operator steps its entries already describe (#746).
  `python3 scripts/ci/check-doc-citations.py` now also checks `internal/idkit/README.md` by
  default (`EXTRA_DEFAULT_FILES`), and says why `CHANGELOG.md` stays unchecked; that README now
  states that a SCIM `POST /Groups` resolves to an existing group and creates no value, and that
  `DELETE /Groups/{id}` answers 405. A release unit test keeps every changelog section to one
  heading of each kind. Docs and CI checks only.
- `docs/FEATURES.md` matches the current code and tests (#741). The rows for logout, API key
  authentication and management, OpenID Connect sign-in and dashboard stats name the tests that
  cover them, and the webhook section introduction lists all seven entities. Gap G11 is narrowed,
  G13 resolved, G15 narrowed and G35 marked as a duplicate of G23. The Playwright count table is
  regenerated from `playwright test --list` (208 tests in 35 files), the Go, integration and
  Vitest count tables are marked as not recounted, and the header date is refreshed. Docs only.
- The historical process documents are marked as historical and the review policy is up to date
  (#745). `REVIEW.md` is dated, states the formatting and lint checks CI runs, and its review
  passes name permission keys, custom roles, single sign-on, SCIM, API key lifetime, webhooks and
  quote public links. The old refactoring guide is removed, along with its entry in
  `scripts/ci/check-doc-citations.py`. The runner-era sections of `docs/DOCUMENTATION_WORKFLOW.md`
  and `docs/VALIDATION_RELEASE.md` carry a historical banner, the runner history is grouped under
  the historical section, and finished follow-ups are no longer listed as open. Docs only.

### Fixed

- The SCIM endpoint is reachable behind the shipped reverse proxies (#728). Neither the UI
  image's `gocrm-ui/docker/nginx.conf` nor the TLS site in `docs/SERVER_PROVISIONING.md`
  forwarded `/scim/v2/`, so identity providers calling `PUBLIC_BASE_URL/scim/v2` got the web app's
  `index.html` with 200, and the production Compose override, which drops the backend's host
  port, left SCIM with no route at all. Both nginx configurations now carry a `location /scim/v2/`
  with the directives of `location /api/v1/`, and the guide gives the Apache equivalent
  (`ProxyPass`/`ProxyPassReverse` for `/api/v1/`, `/health` and `/scim/v2/`). A Docker install
  picks it up with the rebuilt UI image; an existing server adds the location, checks and reloads
  (`nginx -t && systemctl reload nginx` or `apachectl configtest && systemctl reload apache2`) and
  expects `401 application/scim+json` from `/scim/v2/ServiceProviderConfig` (see "Updating an
  existing server" in the guide).
- `TestWebhookEventTypesWidthMigration` (`cmd/migrate`) can no longer wait without bound on
  MySQL/MariaDB. The release run in which it hit the package timeout did not hang there: the
  package's 10-minute `-timeout` fired 3 s into the long-row subtest because the roots before it
  ran 2.4 times slower than on the passing attempt. Every wait of the subtest is now bounded
  anyway. Its migrate sessions carry `lock_wait_timeout=5`, so a metadata lock fails with error
  1205. The migration lock fails with `ErrLocked` after 10 s. Each migrate call and `Close` runs
  under a 30 s deadline that fails the test naming the step and kills the test database's
  sessions. The lenient instance is closed before the strict one continues. Three new subtests
  prove each bound. The migration SQL, the subtest's assertions and the CI root lists are
  unchanged. A package that outgrows its `-timeout` can still cost the release SQL gate the roots
  after it. Tests only (#726).

## [1.3.0] - 2026-11-23

### Added

- **Single sign-on sign-out at the identity provider (#634).** Signing out of a single sign-on
  session now also ends the session at the identity provider. `POST /auth/oidc/callback` also
  returns `id_token`, which the web app keeps with the session. `POST /auth/logout` takes an
  optional `id_token_hint` (at most 16384 bytes, else 400 `VALIDATION_ERROR`); after the sessions
  ended, and only for a hint that verifies for the configured issuer and client (an expired one
  included) and is linked to the caller, the answer carries `end_session_url`: the provider's
  `end_session_endpoint` with `id_token_hint`, `post_logout_redirect_uri` `APP_BASE_URL/login` and
  `client_id`. The web app sends the browser there, and the provider returns it to the sign-in
  page. The lookup is bounded to 5 seconds and never fails the logout; password sessions are
  unchanged (no body, no URL, same message). The new unauthenticated
  `GET /auth/oidc/frontchannel-logout?sid=&iss=` (strict `/auth` tier) is the provider's
  front-channel sign-out: it logs out every account with a live session that signed in under that
  provider session (all of the account's sessions, as a logout without a body does) with one
  `logout` sign-in audit row each, and answers 200 without caching and without frame headers; an
  `iss` that is sent must equal the configured issuer. Each refresh token of a single sign-on
  session records `provider_session_hash`, a keyed hash of the issuer and the ID token's `sid`
  claim (never the `sid` itself), copied by rotation and the password-change renewal; migration
  `20261129120008_refresh_token_provider_session` adds the column and its index on MySQL and
  MariaDB (legacy rows keep ''). No setting or environment variable is added. The Microsoft Entra
  ID how-to lists what to register: the redirect URI `APP_BASE_URL/login`, the front-channel logout
  URL and the `sid` optional claim of the ID token.
- **Quote webhook events (#518).** Quotes join the webhook catalogue with three thin events, so
  there are now twenty-one event types. `quote.created` comes from `POST /quotes`. `quote.updated`
  comes from `PUT /quotes/{id}` only when a stored value changed (compared with the quote row read
  under lock in the update's transaction), from every line add (`POST /quotes/{id}/lines`) and line
  removal (`DELETE /quotes/{id}/lines/{lineId}`), from a line edit (`PUT
  /quotes/{id}/lines/{lineId}`) only when a stored line value changed, and from every committed
  status transition: `POST /quotes/{id}/status` (sent, accepted, rejected, expired and back to
  draft) and the client's accept or decline through `POST /quotes/public/response`. A refused or
  rolled-back transition writes nothing. `quote.deleted` comes from `DELETE /quotes/{id}`, now one
  transaction with a locking read of the quote and the draft-only check of a sales caller on that
  row. Like the other deleted events (#531), it goes to every active endpoint subscribed to it,
  including one that never received an event for the quote; the outbox does not track which
  endpoints announced a quote. Each event is written in the transaction of the quote write, one
  pending delivery per active endpoint subscribed to it, and the payload is thin (the quote id).
  Emailing a quote (its `last_emailed_at` stamp), the response-token and PDF render-credential
  writes and the erasure scrub of quote text send nothing. All twenty-one event types take 302
  bytes and fit the existing `varchar(512)` column, so there is no migration; the raw
  `event_types` array of an endpoint may now hold 32 entries before deduplication (was 20), so an
  array listing every type once is accepted. Settings > Webhooks offers the three quote events.
- **Company webhook events (#517).** Companies join the webhook catalogue with three thin events:
  `company.created` (`POST /companies`), `company.updated` (`PUT /companies/{id}` only when a stored
  value changed, compared with the company row read under lock in the update's transaction) and
  `company.deleted` (`DELETE /companies/{id}`). Each is written in the transaction of the company
  write, one pending delivery per active endpoint subscribed to it. Like the other deleted events
  (#531), `company.deleted` goes to every active endpoint subscribed to it, including one that never
  received an event for the company. A company delete sends only `company.deleted`: clearing
  `company_id` on its leads, customers and deals sends no `lead.updated`, `customer.updated` or
  `deal.updated`. The payload is thin (the company id). An update of a company deleted after the
  handler's read answers 404. All eighteen event types take 260 bytes and fit the existing
  `varchar(512)` column, so there is no migration. Settings > Webhooks offers the three company
  events.
- **Task webhook events (#516).** Tasks join the webhook catalogue with three thin events:
  `task.created` (`POST /tasks` and the automation create-task step, in the step transaction),
  `task.updated` (`PUT /tasks/{id}` when a stored value changed or `label_ids` named another set of
  labels, order and repeats aside; `POST /tasks/bulk/status`, one event per task whose status
  changed) and `task.deleted` (`DELETE /tasks/{id}`). Each is written in the transaction of the task
  write, one pending delivery per active endpoint subscribed to it; an update compares the task row
  and label set read under lock in that transaction with what it writes. Like the other deleted
  events, `task.deleted` goes to every active endpoint subscribed to it, including one that never
  received an event for the task, and it goes out exactly once: the delete re-reads the task under
  lock, so a delete that loses a race with another delete answers 404 and sends nothing. The
  payload is thin (the task id) and carries no lead data; a consumer reads `GET /tasks/{id}`, where
  the lead redaction applies. A label delete, which removes the label from every task, sends no
  `task.updated`. All fifteen event types take 212 bytes and fit the existing `varchar(512)`
  column, so there is no migration. Settings > Webhooks offers the three task events.
- **Deal webhook events (#515).** Deals join the webhook catalogue with three thin events:
  `deal.created` (`POST /deals`), `deal.updated` (`PUT /deals/{id}` when a stored value changed,
  `POST /deals/{id}/stage` to another stage, and the automation assign-owner step when it changes
  the owner) and `deal.deleted` (`DELETE /deals/{id}`). Each is written in the transaction of the
  deal write, one pending delivery per active endpoint subscribed to it; an update compares the
  deal row read under lock in that transaction with the row written. Like the other deleted
  events, `deal.deleted` goes to every active endpoint subscribed to it, including one that never
  received an event for the deal. A company delete that unlinks deals and the erasure scrub of
  deal text send no `deal.updated`. An endpoint can now subscribe to twelve event types, which
  take 173 bytes, so `webhook_endpoints.event_types` widens from `varchar(255)` to `varchar(512)`
  (migration `20261129120007_widen_webhook_event_types`; SQLite widens it at startup). Its down
  migration refuses, under any `sql_mode`, while an endpoint's `event_types` is longer than 255
  bytes, and truncates nothing: shorten those subscriptions first. Settings > Webhooks offers the
  three deal events.
- **Single sign-on with a client certificate (#633).** Settings > Single sign-on accepts a client
  certificate and its RSA private key (PEM, at least 2048 bits, PKCS#8 or PKCS#1, unencrypted) next
  to the client secret, stored encrypted in the new reserved settings
  `security.sso.client_certificate` and `security.sso.client_private_key`. While a usable pair is
  set, the code exchange authenticates with a signed client assertion (`private_key_jwt`, RFC
  7523, PS256) instead of the client secret; without one the secret is sent as before.
  `PUT /settings/sso` takes `client_certificate` and `client_private_key` together (both absent
  keep the pair, both "" clear it) and refuses a certificate that does not parse or has expired, or
  a key that is encrypted, not RSA, too short or does not match, field by field and without
  storing anything. `GET /settings/sso` reports `client_certificate_set`, the SHA-1 thumbprint and
  the expiry, never the PEM or the key. There is no environment variable for the certificate. The
  Microsoft Entra ID guide explains how to create, upload and renew the certificate.

- **Browser headers on the web app and the backend's form pages (#564).** Every page and asset of
  the web app, error answers included, now carries a content policy (`Content-Security-Policy`,
  same-origin scripts, styles, images, fonts and API calls only; no framing), `X-Frame-Options:
  DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`. They are set in the
  Docker image's nginx config (`gocrm-ui/docker/nginx.conf`), in the nginx site of
  `docs/SERVER_PROVISIONING.md` and in `vite preview`, so the release browser tests run with them
  on; the proxied API keeps the backend's own headers, and the dev server is unchanged. The
  backend's form confirmation pages send a content policy of their own with the same companion
  headers, and the hosted form page (`GET /forms/public/{key}/view`) sends a content policy that
  allows reCAPTCHA, `nosniff` and `Referrer-Policy: strict-origin-when-cross-origin`, and stays
  frameable. Images on other sites that an AEO answer links in its markdown no longer load in the
  answer view. A UI built with `VITE_API_BASE_URL` on another origin must add that origin to
  `connect-src` in `gocrm-ui/docker/nginx.conf`.

- **Role audit rows for account activation, deactivation, erasure and email change, and a Role
  audit screen (#552).** `role_audit_log` gains the actions `user_activated`, `user_deactivated`,
  `user_erased` and `user_email_changed`, each written in the transaction of the change with the
  acting user and `auth_method`, `role_id` NULL, an empty `role_name` and the account in
  `target_user_id`. Activation rows store `{"is_active":…}` before and after; the erasure and
  email rows store no state and neither address. Writers: `PUT /users/{id}` (a changed email or
  `is_active`, in the order email, activation, role), `POST /users/{id}/activate` and
  `/deactivate`, `DELETE /users/{id}`, SCIM `PUT`/`PATCH /scim/v2/Users/{id}` (method `scim`, no
  actor), SCIM `DELETE /scim/v2/Users/{id}` on an active account and a SCIM group patch that
  deactivates members. Repeating a deactivation, activating an active account, sending the stored
  email again, a SCIM create and every refusal write no account row; if the row cannot be written
  the change is not made. An OpenID Connect sign-in, a caller's own profile-only email change and
  the erasure of a customer login through `DELETE /customers/{id}` or a lead erasure write none.
  A new read-only Settings → Role audit screen (`/settings/role-audit`, `roles.audit_view`) lists
  `GET /roles/audit` with pagination. No migration, route or setting.
- **Sign-in audit rows for API key creation, revocation and refused re-enabling (#551).**
  `POST /api-keys` answered 201 writes one `auth_audit_log` row with method `api_key_create`; a
  revocation answered 200, through `DELETE /api-keys/{id}` (an idempotent repeat included) or
  `PUT /api-keys/{id}` with `is_active` false, writes one with method `api_key_revoke`; and
  `PUT /api-keys/{id}` with `is_active` true answered 409 because the key is revoked writes one
  with method `api_key_enable`, outcome `refused`, reason `key_revoked`. Each row carries the
  caller's user id, the request id, the client network and the browser family, and the key's
  numeric id in the new nullable column `auth_audit_log.api_key_id`; never the key, its prefix,
  its hash or its name. A rename alone, `is_active` true on a live key, any other refusal and every
  read write no row. Migration `20261129120006_auth_audit_log_api_key_id` adds the column on
  MySQL and MariaDB; SQLite gets it from AutoMigrate. `GET /audit/sign-ins` returns `api_key_id`
  (null when unset), and it and Settings > Sign-in audit accept the 12 methods and 44 reason codes
  as filters. Answers, status codes and messages of `/api-keys` are unchanged.
- **Changing the single sign-on issuer asks for confirmation while accounts are linked (#556).**
  `PUT /settings/sso` with an otherwise valid body whose effective issuer (`issuer_url`, or
  `OIDC_ISSUER_URL` when it is empty) differs from the current one answers 409
  `ISSUER_CHANGE_UNCONFIRMED` with `details.affected_links`, the number of `user_identities` rows
  under the current issuer (any source, erased accounts included, compared byte for byte), and
  stores nothing, unless the body carries `"confirm_issuer_change": true`. Setting the first
  issuer, keeping it, or changing it while no account is linked saves as before. Settings → Single
  sign-on shows the dialog "Change the issuer?" with that count and resends the save on
  "Change issuer". The issuer URL, stored or from `OIDC_ISSUER_URL`, is now limited to 512 bytes,
  the width of `user_identities.issuer` (was 2048); a longer value is refused on save and a longer
  `OIDC_ISSUER_URL` is treated as unset.
- **A webhook endpoint that keeps failing is paused with a growing back-off (#569).** Every
  delivery attempt that was actually sent and failed (a non-2xx answer, a redirect, a timeout, a
  connection failure or a refused destination) adds one to the new
  `webhook_endpoints.consecutive_failures`; from the 3rd consecutive failure the endpoint gets
  `paused_until` = the attempt's finish + 1 minute, doubling with each further failure up to 1
  hour (1, 2, 4, 8, 16, 32, then 60 minutes). Until then the sender leaves out every due delivery
  of that endpoint, and a pass stops sending to an endpoint as soon as an attempt pauses it; the
  skipped rows stay `pending` with their attempts and next attempt unchanged, and other endpoints
  are sent to as before. A 2xx answer resets both columns. Attempts that send nothing
  (`endpoint_inactive`, `secret_unavailable`, a database failure) do not count, and the test ping
  neither reads nor changes the pause. A redelivered row of a paused endpoint waits for the pause,
  and a `PUT` on the endpoint does not clear it. Both columns are read-only on the endpoint API
  (`consecutive_failures`, `paused_until`); one log line "Webhook endpoint paused" carries the
  endpoint id, the count and the pause end only. Migration `20261129120005_webhook_endpoint_pause`
  adds the two columns on MySQL and MariaDB; SQLite gets them from AutoMigrate. The per-delivery
  retry schedule and attempt cap are unchanged. A failure write that loses its fence to another
  replica neither logs the pause nor stops the pass for that endpoint (#586).
- **Sign-in audit rows for password changes, password resets and logouts (#550).**
  `POST /auth/change-password`, `POST /auth/password-reset`, `POST /auth/password-reset/confirm`
  and `POST /auth/logout` each write one `auth_audit_log` row once the request reaches the
  service, with methods `password_change`, `reset_request`, `reset_confirm` and `logout`. A row
  uses the same pseudonymous fields as a sign-in: the user id, a reason code, the request id, the
  client network and the browser family; never an email address, a token or a full address. Two
  reason codes are new: `session_stale` (a password change whose refresh token is not a live
  session of the caller) and `account_unknown` (a reset requested for an email with no account,
  stored without a user id). A reset completion with an unknown, spent or expired token is
  `token_invalid` without a user id. `GET /audit/sign-ins` and Settings > Sign-in audit accept
  the nine methods and 43 reason codes as filters. Answers, status codes and messages are
  unchanged.
- **Schema comparison of the two MySQL/MariaDB schema paths (#523).**
  `cmd/migrate/schema_parity_migration_test.go` builds one fresh schema from every SQL migration and
  one with the startup auto-migration (`models.MigrateDatabaseWith`) and compares their tables,
  column types, nullability and indexes (uniqueness and ordered columns, whatever the index name)
  from `INFORMATION_SCHEMA`. Any difference fails the test with one line per difference. The 18
  differences that exist today (nullability of ten columns, the type of
  `users.failed_login_attempts`, and the seven indexes only migration `20250806120001` creates) are
  listed in an allow-list with a reason each; an entry that no longer matches fails too. It runs in
  the Database migrations workflow on MySQL 8 and MariaDB 10.11 and skips without `TEST_MYSQL_DSN`.
- **SCIM 2.0 Groups (#506).** With a SCIM token, `GET /scim/v2/Groups` (paged by `startIndex` and
  `count`, the one filter `displayName eq "<value>"`) and `GET /scim/v2/Groups/{id}` serve the
  distinct `groups`-claim values of the effective single sign-on role mapping as core Group
  resources, ordered by bytes, with their visible members (never an erased or customer account);
  the id is the unpadded base64url of the value, and a value the mapping no longer holds is the
  SCIM 404 `Group not found`. `PATCH /scim/v2/Groups/{id}` adds, removes or replaces members (Entra
  ID and Okta forms); a `displayName` or other path is 400 `invalidPath`, a remove without a path
  `noTarget`, and a member id that is no visible user 400 `invalidValue`. In one governed
  transaction it recomputes each added or removed member's role with the sign-in's first-match rule
  on the groups claim (since #530 an active account left in no mapped group is deactivated instead),
  writes the `user_role_changed` audit rows with
  `auth_method` `scim` and no actor, and after the commit one sign-in audit row with method `scim`
  per member whose membership changed. Only a result that leaves no active admin is refused, with
  400 `mutability`, so a handover to another active account passes; replacing the last admin with
  a deactivated account is refused. `POST /scim/v2/Groups` and `DELETE /scim/v2/Groups/{id}` answer
  a SCIM 405 with an `Allow` header; `PUT` is not routed. `/Schemas` and `/ResourceTypes` now list
  User and Group. Membership is stored in the new `scim_group_members` table (AutoMigrate on
  SQLite, `migrations/20261129120000_add_scim_group_members` for MySQL/MariaDB, `group_value`
  `utf8mb4_bin`). A group patch maps the groups claim only and a Users write the roles claim only,
  so the last write sets the role; editing the mapping recomputes no role until the person's next
  group patch. The Users writes keep answering the last-admin refusal with `invalidValue`.
- **Entra ID sign-in finds the SCIM-created account (#505).** Entra ID sends the `objectId` as
  SCIM `externalId`, but its ID tokens carry a pairwise `sub`, so the first single sign-on of a
  SCIM-provisioned person used to be refused as `email_taken`. Now an unknown `(iss, sub)` signs in
  as the SCIM-created account when the configured issuer is Entra ID's tenant-specific
  `https://login.microsoftonline.com/<tenant id>/v2.0`, the token's `tid` equals that tenant id
  byte for byte and its `oid` equals the `externalId` SCIM stored under that issuer; the account
  must be active and not erased (`account_inactive`, `account_erased` otherwise), and the
  `(iss, sub)` link is written in the same governed transaction. `user_identities` gains `source`
  (`oidc`|`scim`, default `oidc`; AutoMigrate on SQLite, `migrations/20261128120000_user_identity_source`
  for MySQL/MariaDB, existing rows read `oidc`); SCIM create writes `scim`. Every other case keeps
  the previous path, and email never links an account.
- **SCIM 2.0 user delete (#504).** `DELETE /scim/v2/Users/{id}` answers 204 with an empty body
  and deactivates the account as `active` false does: in one governed transaction
  (`admin set → users → refresh_tokens`) it sets `is_active` false, increments `token_version` and
  revokes every refresh token, so every session ends at once. It never erases: the email, names,
  role, `role_id` and single sign-on link stay, and erasure remains the administrator's
  `DELETE /api/v1/users/{id}`. A repeat on the inactive account answers 204 and writes nothing.
  The last active admin is 400 `invalidValue`, an id the provider does not see the SCIM 404 `User
  not found`. A deactivation writes one `auth_audit_log` row with method `scim` and, since #552,
  one `user_deactivated` role audit row. No new setting, table or migration.
- **Webhook signing-secret rotation (#503).** `POST /api/v1/webhooks/endpoints/{id}/rotate-secret`
  (`webhooks.manage`) generates a new `whsec_` secret and answers 200 with the endpoint, the new
  `secret` (shown this once) and `previous_secret_expires_at` (now + 24 h). The replaced secret is
  kept, sealed, until then, and every delivery, retry, redelivery and test ping in that window
  carries two space-separated signatures in `webhook-signature` (`v1,<new> v1,<previous>`, the new
  one first); after it only the new one is sent, and the runtime clears the expired secret on its
  next send of that endpoint (the test ping writes nothing and never clears). A second rotation
  inside the window replaces the previous secret, so at most two secrets sign. Rotation is one
  primary-key UPDATE that never opens a stored secret: it works on an inactive endpoint and repairs
  one whose secret became unreadable after an `API_KEY_SECRET` change. A previous secret that
  cannot be opened is dropped with a `previous_secret_unavailable` warning and the request goes
  out with the current signature. Schema: `webhook_endpoints.previous_secret_sealed` and
  `previous_secret_expires_at` (both nullable), through AutoMigrate and the MySQL/MariaDB
  migration `20261127120000_webhook_secret_rotation`. API-only: the Settings screen has no rotate
  control yet.
- **Webhook delivery redelivery (#502).** `POST /api/v1/webhooks/deliveries/{id}/redeliver`
  (`webhooks.manage`) puts a `succeeded` or `failed` delivery back into the queue with one
  conditional UPDATE of the same row: `pending`, `attempts` 0, due at once, `last_http_status` and
  `error_code` cleared (`last_attempt_at` keeps its value). The runtime then sends it again with
  the same event id (`webhook-id`) and the full retry budget of 10 attempts; the record it names is
  not read. It answers 200 with the delivery; a `pending` or `sending` delivery is 409 `CONFLICT`
  ("Webhook delivery is pending or being sent"), a delivery whose endpoint is deleted or inactive
  is 409 ("Webhook endpoint is deleted or inactive"), an unknown id 404. No new row is written. No
  UI change.
- **Webhook endpoint test ping (#501).** `POST /api/v1/webhooks/endpoints/{id}/test`
  (`webhooks.manage`) sends one signed `webhook.ping` request (`{id, type, occurred_at}`, no
  `record_id`) to the endpoint, synchronously, through the same egress guard, signing, 10-second
  timeout and no-redirect rule as deliveries, and answers 200 with
  `{event_id, outcome, http_status, error_code}`; a failed ping, an unopenable secret
  (`secret_unavailable`) included, is a 200 with `outcome` `failed`. It works on an inactive
  endpoint, is not retried, writes no `webhook_deliveries` row and leaves the endpoint unchanged.
  `webhook.ping` is not an event type an endpoint can subscribe to. No UI change.
- **SCIM provider tokens in Settings (#490).** The SPA screen Settings > SCIM tokens
  (`/settings/scim-tokens`) lists the SCIM provider tokens with their created, last-used and
  revoked dates (a revoked token stays listed, its revoke action disabled), creates a token and
  shows it once in a dialog with a copy button and a shown-once warning (it is kept in component
  state only and dropped on Done), and revokes a token behind a confirmation. The route and the
  navigation entry under Settings need `settings.scim_manage`. No backend change: the screen uses
  the `/settings/scim/tokens` API of #468.
- **Documentation citation check (#487).** `scripts/ci/check-doc-citations.py` checks the code
  that `docs/FORKING.md` and `docs/EXTENDING.md` cite: every backticked repository path must be a
  tracked file or directory (a short allowlist covers the gitignored build outputs the guides
  name), every Go symbol cited next to a `.go` path must be declared in that file (read by the new
  go/parser helper `scripts/ci/docsymbols`), and the two guides may not cite code by line number.
  Its unit test runs in `make verify-hygiene`.
- **More guides cite code by symbol (#488).** `docs/USE_CASES.md`, `docs/SERVER_PROVISIONING.md`,
  `README.md`, `docs/REFACTORING_GUIDE.md`, `docs/FEATURES.md`, `docs/DOCKER.md`,
  `docs/datamodel.md` and `docs/SETUP.md` no longer cite code, scripts or configuration by line
  number: Go code is cited as file plus symbol, other files by a function name, step, key or short
  quoted anchor. The Compose line citations in `docs/DOCKER.md` stay, because a test recomputes
  them. The eight files join the citation check, the stale migration stems, short paths and
  value-as-symbol citations it reported in them are fixed, and `gocrm-ui/dist` is allowed as a
  build output.
- **Every tracked doc cites code by symbol (#489).** The test-case catalog under `docs/testing`
  no longer cites code by line number: its `file:N` citations, bare `:N` continuations and prose
  line references now name the file plus the Go symbol (a testify suite test as `Suite.TestName`),
  or for other files the full path plus a function, component, route or short quoted anchor.
  `scripts/ci/check-doc-citations.py` now checks every tracked `docs/**/*.md` and `README.md`,
  discovered through `git ls-files`, with all three rules. `docs/CONCURRENCY.md` is the one
  exemption, because `internal/config/concurrency_doc_test.go` recomputes its line citations; the
  one path skip is a planned Playwright spec named in the catalog that does not exist yet; and the
  local-only `docs/specs`, `docs/plans` and `gocrm-ui/node_modules` are ignored outputs. The stale
  migration stems, short paths and misattributed symbols the wider check found in the catalog and
  in `docs/DEVELOPMENT.md` are fixed, and the development guide and the catalog's case format
  describe the rule.
- **Administrator guide: testing single sign-on (#481).** A sixth page,
  `docs/user-guide/permissions-access/testing-single-sign-on.md`, tells an administrator how to
  test single sign-on against their own Entra ID tenant: the Entra ID setup by reference to the
  how-to, the values to copy, a local run with a break-glass local administrator and the secret in
  a git-ignored env file, quick API checks, a live test table, where to read the result (sign-in
  audit, the `oidc_sign_in` log lines), external `#EXT#` accounts and common errors. The overview
  and the single sign-on page link it, and `scripts/ci/check-user-guide.py` checks it.
- **Administrator guide: SCIM provisioning (#491).** A seventh page,
  `docs/user-guide/permissions-access/scim-provisioning.md`, documents the SCIM 2.0 provider as it
  behaves today: the base URL (`PUBLIC_BASE_URL/scim/v2`), creating and revoking SCIM tokens, the
  supported operations and their refusals, the role mapping, what is not served (user delete and
  groups answer a plain 404), the audit rows, and the known limitation that an Entra ID
  `externalId` (the `objectId`) never matches the pairwise `sub`, so the person's first single
  sign-on is refused with `email_taken`. The overview and the single sign-on page link it, and
  `scripts/ci/check-user-guide.py` now resolves `/scim/v2` routes and paths against the body of
  `SetupSCIMRoutes` in `internal/handler/scim_routes.go`, reporting a missing prefix, function or
  route set as a problem.

- **SCIM 2.0 user patch (#471).** With a SCIM token, `PATCH /scim/v2/Users/{id}` applies an
  RFC 7644 PatchOp: `add`, `replace` and `remove` (op in any ASCII case, so Entra ID's `Replace`
  passes) on `active`, `userName`, `name`, `name.givenName`, `name.familyName`, `emails`,
  `emails[type eq "work"].value` and `roles`, with a `path` or in the path-less form whose value
  is an object of those attributes (Okta, and Entra ID's value-object form). `active` takes a JSON
  boolean or the strings `true`/`false` in any ASCII case (Entra ID sends `"False"`). Operations
  apply in order and the last write wins; a `userName` the patch sets wins over its `emails`. Any
  other path or path-less key (`externalId`, `displayName`, `name.formatted`, extension URNs) is
  400 `invalidPath`, a `remove` without a path 400 `noTarget`, a body that is not a PatchOp with a
  non-empty `Operations` array 400 `invalidSyntax`, and a wrongly typed value or a `remove` of
  `active`, `userName`, `emails` or `roles` 400 `invalidValue`. The touched attributes get the
  checks of PUT; `roles` resolves through the role mapping, and an untouched role is kept. A write
  that sets `active` false, through PATCH or PUT, now deprovisions at once: in the same governed
  transaction it increments the account's `token_version`, so every access token issued before is
  refused, and revokes every refresh token of the account. `active` true reactivates the account
  without bringing the old sessions back. Demoting or deactivating the last active admin is 400
  `invalidValue`; a role change writes the `user_role_changed` audit row with `auth_method`
  `scim`, every success one sign-in audit row with method `scim`, and a refusal writes nothing.
  `DELETE /scim/v2/Users/{id}` (#504) and `/Groups` (#506) came later. No new setting, table or
  migration.
- **SCIM 2.0 user create and replace (#470).** With a SCIM token, `POST /scim/v2/Users` creates
  a staff or custom account (201, `Location` equal to `meta.location`) and
  `PUT /scim/v2/Users/{id}` replaces its name, email, role and, when sent, `active` (200). The
  role always comes from the single sign-on role mapping: the first entry with claim `roles`
  whose value equals a sent `roles[].value` exactly; no match, a missing role row or a mapping to
  `customer` is 400 `invalidValue`. A create links the account to the effective SSO issuer with
  `externalId` as the subject, so a later OIDC sign-in with that `sub` uses it, and stores an
  unusable random password (a sent password is never used). An address another account holds,
  live or erased, in any letter case (on MySQL/MariaDB also padded and accented variants), or an
  already linked `externalId`, is 409 `uniqueness`; a missing or invalid `externalId`, an empty
  or long `userName`, a name part over 100 characters, no configured issuer or a `userName` that
  is not an address is 400 `invalidValue`; a body that is not one JSON object of at most 64 KiB
  with correctly typed attributes is 400 `invalidSyntax`. Replace refuses to demote or deactivate
  the last active admin (400 `invalidValue`) and answers 404 for unknown, erased and customer
  accounts. Each write is one governed transaction; a create and a role change write a
  `user_role_changed` audit row with `auth_method` `scim` and `actor_user_id` NULL, and every
  success writes one sign-in audit row with method `scim` and outcome `success`. A refusal writes
  nothing. No new setting, table or migration.
- **SCIM 2.0 user read (#469).** With a SCIM token, `GET /scim/v2/Users/{id}` returns one core
  User resource and `GET /scim/v2/Users` a ListResponse in id order, paged by `startIndex`
  (1-based) and `count` (default and maximum 100, 0 for the total only), with the one filter
  `userName eq "<value>"`; any other filter is 400 `invalidFilter` and a non-integer
  `startIndex` or `count` 400 `invalidValue`, in the RFC 7644 error shape with the new
  `scimType`. Erased and customer accounts are never served (404 by id); deactivated staff are
  served with `active` false. The userName match is judged in Go on each row the database returns,
  with ASCII-only case folding, so the MySQL/MariaDB collation of `users.email` cannot match a
  padded, accented or Unicode-fold variant (U+017F LONG S, U+212A KELVIN SIGN) of a stored
  address. `externalId` is the subject of the user's identity under the configured single sign-on
  issuer, compared byte for byte. The routes are outside the OpenAPI document like the discovery
  routes. No new setting, table or migration.
- **SCIM 2.0 provider, first slice (#468).** `/scim/v2` is served outside the API prefix to
  identity providers. Administrators issue SCIM provider tokens through
  `GET`/`POST /api/v1/settings/scim/tokens` and revoke them with
  `DELETE /api/v1/settings/scim/tokens/{id}`, guarded by the new permission
  `settings.scim_manage` (held by the admin system role). A token (`scim_` + 64 hex characters)
  is shown once; the new `scim_tokens` table stores only its HMAC under `API_KEY_SECRET`, plus
  `last_used_at` and `revoked_at` (migration `20261126120000_add_scim_tokens`, AutoMigrate on
  SQLite). With a valid `Authorization: Bearer` token, `GET /scim/v2/ServiceProviderConfig`,
  `/scim/v2/Schemas` (the core User schema) and `/scim/v2/ResourceTypes` answer in
  `application/scim+json`. A user's JWT or API key is refused there, and a SCIM token is refused
  on the API. Every refusal is the same 401 in the RFC 7644 error shape and is recorded in the
  sign-in audit log (counted per reason, client network and minute since #553) with the new method `scim` and the new reasons `token_missing` and `token_revoked`
  (or `token_invalid`); a strict tier (10 requests a minute, burst 5, per client address)
  answers 429 in the same shape before the authentication. The sign-in audit filters and the
  SPA's lists offer the new method and reasons. The discovery routes are excluded from the
  OpenAPI document like `GET /health`. User provisioning beyond the read in #469, and Groups, are
  later slices.
- **Administrator guide (#447).** `docs/user-guide/permissions-access/` holds five pages on roles,
  permissions and single sign-on: an overview, roles and permissions (the system roles per
  permission group, custom roles, assigning roles, API keys, the last-admin and escalation rules,
  the role audit trail), single sign-on (OpenID Connect sign-in, the settings,
  mixed accounts, troubleshooting), a Microsoft Entra ID walk-through and an FAQ. README and
  `docs/AUTHORIZATION.md` link it. The new check `scripts/ci/check-user-guide.py` fails when a
  setting or permission key, environment variable, error code, API route or SPA path the guide
  names no longer exists in the code; its unit test runs in `make verify-hygiene`. The pre-commit
  hook allows the new `docs/user-guide` folder.
- **Sign-in audit API and screen (#455).** `GET /api/v1/audit/sign-ins` reads the sign-in audit
  log, newest first, paginated, filtered by method, outcome, reason, user id and a from/to range;
  an unknown filter value is a 400. It is guarded by the new permission `audit.sign_ins_view`,
  which among the system roles only admin holds (custom roles may hold it). Settings > Sign-in
  audit (`/settings/sign-in-audit`) shows the rows with the same filters, and is listed and
  opened only with that permission.
- **Sign-in audit log (#454).** A new `auth_audit_log` table (migration
  `20261124120000_add_auth_audit_log`, and AutoMigrate) records one pseudonymous row per password
  sign-in, single sign-on callback and refresh, and per refused `ApiKey` credential: the method,
  the outcome, a stable reason code (for example `invalid_credentials`, `sso_account`,
  `token_invalid`, `key_expired`, or the single sign-on `error_code` such as `no_mapping_match`),
  the user id when the account was found, the sign-in attempt and request ids, an HMAC reference of
  a single sign-on identity refused after its ID token verified, the role and group claim values
  the mapping used, the client's `/24` or `/48` network and a user-agent family from a fixed list.
  It never stores an email, a name, a subject, a token, a code, a state, a nonce, a password, the
  full IP address or the full user agent. The row is written after the sign-in's transaction; a
  failed write is logged and changes no answer, and no status, body or message changed. Rows older
  than `AUTH_AUDIT_RETENTION_DAYS` (environment only, default 90) are deleted by the stale bulk
  operation sweep; erasure leaves them to age out. The `oidc_sign_in` refusal, 503 and success log
  lines carry `audit_id`. See the *Sign-in audit log* section of `docs/AUTHORIZATION.md`.

- **Browser tests of single sign-on against a local test issuer (#446).** `make e2e` now builds
  and starts `scripts/e2e/oidc-issuer`, a disposable OpenID Connect issuer on
  `https://127.0.0.1:<E2E_OIDC_PORT>` (default the first free port from 18291) that serves the
  run's per-run test certificate and is stopped with the run. It has one client with a per-run
  secret and the SPA's `/auth/callback` as its only redirect URI, and refuses a wrong
  `redirect_uri` (at the authorization and the token endpoint), a missing or `plain` PKCE
  challenge, a wrong verifier, a missing nonce, an unknown client, a wrong secret and a reused
  code (`go test ./scripts/e2e/oidc-issuer`). The new `gocrm-ui/e2e/tests/sso.spec.ts` (12 tests)
  saves the settings through Settings > Single sign-on, signs in through the browser round trip
  and covers JIT provisioning, role mapping and its re-application, an unmatched claim, the
  last-admin rule, an email held by a local account, a deactivated account, the single-use
  callback and a tampered state, break-glass password login, `allow_password` governing the reset
  mail, and the issuer's own checks; it switches single sign-on off again when it ends. The run
  now pins the `OIDC_*` variables empty and `LOG_LEVEL=info`, so a developer's `.env` cannot turn
  single sign-on on, and exports `E2E_OIDC_ISSUER_URL`, `E2E_OIDC_CLIENT_ID`,
  `E2E_OIDC_CLIENT_SECRET` and `E2E_BACKEND_LOG` to Playwright. `E2E_PLAN_ONLY=1` prints the
  issuer origin (never the secret) and refuses an invalid or colliding `E2E_OIDC_PORT`.
- **Single sign-on: password sign-in and reset for linked accounts are a setting, off by default
  (#457).** The new setting `security.sso.allow_password` (environment fallback
  `OIDC_ALLOW_PASSWORD`, default false; seeded empty, reserved like the other `security.sso.*`
  rows, so 7 such rows now exist) decides whether an account with a `user_identities` row may also
  use a password. Off: the #444 reset refusal applies, and `POST /auth/login` now refuses such an
  account's correct password with the same generic 401 "Invalid email or password", writing
  nothing (no `last_login_at`, no lockout change, no refresh token). On: a linked account uses
  password sign-in and reset like a local one. Accounts without an identity row are never
  affected, so local break-glass accounts keep working. `GET`/`PUT /settings/sso` carry
  `allow_password` and `sources.allow_password` (null or absent clears the stored value, so
  `OIDC_ALLOW_PASSWORD` applies); an unreadable stored value counts as off. Settings > Single
  sign-on has the switch "Allow password sign-in and password reset for accounts linked to single
  sign-on" with an always-shown warning: while it is on, a password keeps working after the person
  is removed from the identity provider or loses the mapped role, so offboarding must also
  deactivate the account in GopherCRM. The policy is read once per request, before the
  transaction; the login gains a plain `user_identities` read after the owner lock while it is off
  (lock order users → (read) user_identities → refresh_tokens). Switching the setting off does not
  end sessions or refresh tokens issued before, and changes to it are not audited yet, like every
  other single sign-on setting. `cmd/main.go` wires it through `service.WithAuthSSOPasswordPolicy`.
  No schema change.

- **Webhooks: deleted events for leads, customers and tickets (#437).** Endpoints can subscribe to
  `lead.deleted`, `customer.deleted` and `ticket.deleted` (nine event types in all, in the API,
  the OpenAPI enums and the Settings > Webhooks picker). `DELETE /tickets/{id}` writes
  `ticket.deleted` in the transaction of the soft delete; every erasure unit, whether entered from
  a lead, a customer or a user and including its cascades, writes `lead.deleted` and
  `customer.deleted` for each lead and customer it found live and erased, at the end of the unit
  through an erasure listener on the lead, customer and user repositories, so the erasure
  protocol (discovery, lock order, re-check, restart) is unchanged and a rolled-back delete sends
  nothing. A deleted event goes to every active endpoint subscribed to it, whether or not that
  endpoint received an earlier event for the record (#531). An erasure sends exactly what a
  delete sends: the thin body, no reason and no personal data. No migration: all nine types stored
  together take 134 of the 255 bytes of `webhook_endpoints.event_types`.
- **OpenID Connect sign-in, SPA (#436).** `GET /auth/registration` also returns `sso_enabled`,
  the effective single sign-on switch (false when it cannot be read; nothing else of the
  configuration is exposed), so the login page needs no second request on the strict `/auth`
  tier. While it is true the login page shows "Sign in with single sign-on", which starts the
  sign-in through `POST /auth/oidc/start`; the password form stays. The new public
  `/auth/callback` page removes `code` and `state` from the address bar, posts them once to
  `POST /auth/oidc/callback` and keeps the session in sessionStorage (no remember-me across the
  redirect); every failure shows one generic message with a link back to sign in. The new
  Settings > Single sign-on screen (`settings.sso_manage`) edits `GET`/`PUT /settings/sso`: a
  write-only client secret (set, kept or cleared), the scopes and the ordered role mapping, with
  each field's source. A first save stores the environment-derived values shown as settings.
- **OpenID Connect sign-in, backend (#435).** `POST /auth/oidc/start` returns the issuer's
  authorization URL (authorization code flow with PKCE, S256, a state and a nonce, redirect URI
  `APP_BASE_URL/auth/callback`) and `POST /auth/oidc/callback` `{code, state}` finishes the
  sign-in, both on the strict public `/auth` tier. They use the issuer, client and role mapping of
  the single sign-on settings (#434). Success returns the same `token`, `refresh_token` and `user`
  as `POST /auth/login`. Single sign-on off is 404, on but unconfigured,
  undiscoverable or with a failing token endpoint is 503 `SSO_UNAVAILABLE`, and every refusal is
  401 "Single sign-on failed". Identities are linked only by the exact `(iss, sub)` pair in the new
  `user_identities` table; a subject with leading or trailing whitespace is refused on every
  engine, and `email` never links an account. An unknown identity is provisioned on its first
  sign-in with the mapped role and no usable password, unless the token has no email or any user,
  erased ones included, already holds the address in any letter case. The role is the first
  mapping entry, in stored order, whose `groups` or `roles` claim holds its value; no match and an
  Entra ID group overage are refused. Every sign-in re-applies the mapped role through the
  governed role write, so the last active admin cannot be demoted and a change is audited with
  `auth_method` `oidc`. A started sign-in is a single-use `oidc_login_attempts` row holding the
  HMAC of the state, valid ten minutes. Erasure keeps a user's identity row, so an erased person's
  next sign-in is refused. Password login stays as break-glass. Migration
  `20261123120000_add_oidc_sign_in` creates the two tables; `docs/AUTHORIZATION.md` gains Okta and
  Entra ID examples. New dependencies: `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`.
- **Single sign-on settings, no sign-in yet (#434).** The backend half of the OpenID Connect
  configuration for RBAC phase 2. Six settings are seeded in the security category:
  `security.sso.enabled`, `issuer_url`, `client_id`, `client_secret`, `scopes` and
  `role_mapping`; the client secret and the role mapping are encrypted at rest. They are read
  and written only through the new admin-only `GET` and `PUT /settings/sso`, guarded by the new
  permission `settings.sso_manage` (held by the admin system role; `settings.view` and
  `settings.update` do not open it). GET returns the effective values with a `sources` object
  (`settings`, `env` or `default`) and `client_secret_set`; the secret itself is never
  returned. PUT replaces everything but the secret (absent keeps it, `""` clears it), validates
  the issuer (absolute https URL, no credentials, query, fragment or zone identifier), the
  scopes (must contain `openid`) and the role mapping (claim `groups` or `roles`, a stable
  value, an existing role id; an unknown one is 400 `INVALID_REFERENCE`) before writing
  anything. The `OIDC_ENABLED`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`,
  `OIDC_SCOPES` and `OIDC_ROLE_MAPPING` variables are fallbacks behind the stored settings
  (documented, commented out, in `.env.example`). It fails closed: single sign-on is off when
  its stored switch cannot be read, and a stored mapping that cannot be read or parsed is empty
  rather than the environment one. Since #435 these settings sign users in (see the OpenID
  Connect sign-in entry above). No schema change: the rows are seeded at startup.
- **Settings > Webhooks screen (#409).** Webhook endpoints and their delivery log no longer need
  the API: `/settings/webhooks` lists the endpoints (URL, subscribed events, active flag, created
  date) and creates, edits, activates, deactivates and deletes them, and its "Delivery log" tab
  shows each delivery (event type, record id, status, attempts, last attempt, last response code)
  newest first, with filters for endpoint, status and event type. The signing secret of a new
  endpoint is shown once, with a copy button, and is not kept in the browser afterwards. The
  server's refusal of a URL (not https, with credentials, with a query string) is shown in the
  form as it is returned. Deleting an endpoint asks first and says that its delivery log is
  removed with it. The Settings entry and the route appear only for a role that holds
  `webhooks.manage` (the admin role by default). No API or schema change.
- **Webhooks for updated leads, customers and tickets (#408).** Three new event types,
  `lead.updated`, `customer.updated` and `ticket.updated`, next to the three created events of
  #345. `PUT /leads/{id}`, `PUT /customers/{id}`, `PUT /tickets/{id}` and the bulk status
  endpoints `POST /leads/bulk/status` and `POST /tickets/bulk/status` write one event per record
  whose stored values actually changed, in the same transaction as the write, with the same thin
  signed body (`id`, `type`, `occurred_at`, `record_id`). An update that sends only the stored
  values answers 200 and sends nothing; a refused or rolled-back update sends nothing either.
  `POST` and `PUT /webhooks/endpoints` accept any non-empty subset of the six types (a raw
  `event_types` array may now hold up to 20 entries, duplicates collapse) and
  `GET /webhooks/deliveries?event_type=` filters on all six. With webhooks wired, the customer
  update is now one transaction with a locking read of the customer, and the ticket update runs
  its locking transaction also when no automation is wired. Deleting or erasing a record,
  converting a lead (still `customer.created` only), assigning a lead or a customer, ticket
  comments, automation rule steps, a repeat form enquiry, a company delete and the bulk task
  status endpoint send no updated event. No schema change.
- **The quote history shows a client's answer as the client's (#400).** An accept or decline
  through the response link of a quote email (#382) used to be recorded with the quote owner
  as its user. The history row of such an answer now has no user and says who answered:
  - **API.** Every entry of `GET /quotes/{id}/history` carries a new field `source`, `staff`
    or `client`. A client answer has `"source": "client"`, `"changed_by_id": null` and no
    `changed_by`; `changed_by_id` is therefore nullable. Staff changes read as before
    (`source` `staff`, `changed_by_id` and `changed_by` of the acting user). The public
    response routes and their errors are unchanged.
  - **Web UI.** The status history on the quote page shows `Client` as the actor of such a row.
  - **Schema:** migration `20261122120000_quote_history_source` on MySQL/MariaDB adds
    `quote_status_changes.source` (`varchar(20) NOT NULL DEFAULT 'staff'`, only when missing)
    and makes `changed_by_id` nullable (the foreign key to `users` stays); `MigrateDatabase`
    does the same on SQLite by rebuilding the table, rows kept. Rollback gives every row
    without a user the owner of its quote, restores `NOT NULL` and drops `source`.
  - **Not backfilled.** Answers recorded before the upgrade keep the quote owner as their user
    and read `source` `staff`.

- **Quote PDF output (#384).** A quote can be downloaded as a PDF and the quote email carries
  it as an attachment. The server prints the document with a headless Chrome or Chromium: the
  same document as the print page, served by a new public page of the web application,
  `APP_BASE_URL/quote-render`.
  - **`QUOTE_PDF_CHROME_PATH`** (new, default empty) names the browser binary. Empty, or a
    path that is not an executable file, switches PDF output off without stopping the server:
    quote emails go out as text only, `GET /quotes/{id}/pdf` answers 503 `PDF_UNAVAILABLE` and
    the web application hides its Download PDF button. The browser opens
    `APP_BASE_URL/quote-render`, so `APP_BASE_URL` must be reachable from the backend host,
    and when the API is on another origin than `APP_BASE_URL` that origin must be in
    `CORS_ALLOWED_ORIGINS`; otherwise every render fails and the email falls back to text.
  - **`GET /quotes/{id}/pdf`** (admin and sales, the permission and the refusals of
    `GET /quotes/{id}`) answers the raw file, `Content-Type: application/pdf`,
    `Content-Disposition: attachment; filename="Quote-Q-000042.pdf"`. PDF output off is 503
    `PDF_UNAVAILABLE`; a render that fails or runs out of time is 503 `PDF_RENDER_FAILED`.
    The quote page of the web application has a **Download PDF** button next to Print when
    `GET /configurations/ui` reports the new read-only entry `quotes.pdf_available` as `true`.
  - **Attachment on the quote email.** `POST /quotes/{id}/email` attaches
    `Quote-<number>.pdf` (`multipart/mixed`, the text part unchanged). When PDF output is
    off, the render fails or it runs out of time, the mail goes out as text only, exactly as
    before, and the request still succeeds. `mailer.AttachmentSender` is a new optional
    interface (`SMTPMailer` and `LogMailer` implement it); the `Mailer` interface is unchanged.
  - **`POST /quotes/public/render`** (no authentication, generous rate tier) is the read the
    render page makes with a render credential: 32 random bytes minted for one render, kept
    only as a hash in the memory of the rendering process, valid for that one quote, read-only,
    until the render returns (at most 15 seconds). It travels in the URL fragment and in a
    request body, never in a URL a server receives. Any unusable credential is one generic 400.
  - **Time budget.** One render runs at a time per process and is bounded at 15 seconds, the
    wait for its turn included; with the 30-second SMTP bound the email call takes at most
    45 seconds. The web application now gives `POST /quotes/{id}/email` and the PDF download a
    55-second request timeout (the default stays 30 seconds), so it no longer reports a failure
    for a mail the server still delivers. A reverse proxy in front needs a read timeout of 60
    seconds or more (the nginx default).
  - **Docker image.** The runtime image now installs a browser and a font and sets
    `QUOTE_PDF_CHROME_PATH`, so PDF output is on by default in the image, which grows from
    about 53 MiB to about 655 MiB (`docs/DOCKER.md`). The image also installs `tini` and starts
    the server under it, so the browser's orphaned helper processes are reaped; which browser,
    which path and which init path the image ends up with is in the #393 entry under Changed.
    A deployment that replaces the entrypoint should keep an init. `docker-compose.yml` and
    `docker-compose.sqlite.yml` set the variable empty: their `APP_BASE_URL`
    (`http://localhost:<UI_PORT>`) is not reachable from the backend container, so the
    development stacks ship with PDF output off.
  - The browser process gets a built environment (`PATH`, locale, `TZ` and directories inside
    the render's temporary directory), never the server's: no secret of the server reaches it.
    `scripts/e2e/quote-pdf-smoke.sh` is a manual check of the whole chain against a local
    backend (`docs/DEVELOPMENT.md`). No schema change, no new dependency of the Go module or
    the web application. Known limits are in `docs/ROADMAP.md`.

- **Public accept/decline page for the quote link (#383).** The link of a quote email (#382)
  now opens a page of the web application, `/quote-response?token=...`, that needs no account:
  it shows the quote number, the validity date, the lines and the subtotal, discount, net, tax
  and gross totals in the quote's currency, with the buttons Accept and Decline. Each opens one
  confirmation dialog naming the quote; only Confirm sends the answer, once, and the page then
  shows "You have accepted quote Q-…" or "You have declined quote Q-…". A link that is unknown,
  used, expired or invalidated shows "This link is no longer valid. It may already have been
  used or it has expired." with no quote data and no buttons; any other failure to read the
  quote shows "The quote could not be loaded. Please try again later."; a failed answer that is
  not a dead link keeps the quote and the buttons for another try. The page reads the link once
  per visit (no retry, no refetch on focus), renders outside the application shell with no
  navigation and no sign-in link, and never shows or logs the token.
  - **No bearer token on public calls.** `ApiClient.getPublicClient()` (exported as `publicApi`)
    is a second axios instance with the same base URL, timeout and envelope handling as `api`
    but no request interceptor: it never sends the stored session's `Authorization` header and
    never refreshes or replays on a 401. The new endpoint module
    `src/api/endpoints/quoteResponse.ts` uses it for `GET` and `POST /quotes/public/response`.
  - **Browser-test link command.** `go run ./scripts/e2e/quote-response-link -quote-id N`
    emails a sent quote through the real quote service with an in-memory mailer and prints the
    link, because the log-only mailer writes no mail body and no route returns the token. It is
    not part of the server binary, adds no route, runs only under `make e2e` (it requires
    `E2E_UI_PORT`) and refuses a MySQL database whose name does not end in `_e2e`.
  - No API, schema or configuration change.

- **Client accept/decline link in the quote email, backend (#382).** The email of a *sent* quote
  (`POST /quotes/{id}/email`) now ends with a single-use response link,
  `APP_BASE_URL/quote-response?token=...`. Only the HMAC-SHA256 hash of the token is stored, in
  the new table `quote_response_tokens` (keyed by `API_KEY_SECRET`, else `JWT_SECRET`, like the
  other opaque tokens). Two unauthenticated routes on the strict rate tier answer the link:
  `GET /quotes/public/response?token=` shows the quote (number, status, currency, `valid_until`,
  lines and the five totals, nothing else) without spending the token, and
  `POST /quotes/public/response` with `{token, decision}` (`accept` or `decline`) claims the
  token and moves the quote `sent → accepted | rejected` with `closed_at` and one history row,
  in one transaction. Every refusal (unknown, used, expired or invalidated token, a quote that
  is no longer sent or was deleted) is the same 400 `BAD_REQUEST` "Invalid or expired quote
  link"; an invalid body is 400 `VALIDATION_ERROR` and leaves the link usable. Of two concurrent
  answers with one token exactly one commits; a failure after the claim rolls it back and the
  link stays usable. The linked deal is not changed.
  - **Life of a link.** It works until the end of the quote's `valid_until` day (server's local
    date), or while the quote is sent when `valid_until` is not set. Each new email of the quote
    invalidates the previous link, and any staff status change away from sent invalidates the
    current one, so a link stays dead after sent → draft → sent until the next email. An
    accepted quote, and a sent quote whose `valid_until` has passed, are emailed without a link.
    A quote that left sent, or had any status change recorded, between the start of the email
    request and the storing of the link is refused with 409 and nothing is sent.
  - **History attribution.** The history row of a client answer has no user and `source`
    `client` (#400, above); the token row (`used_at`, `decision`) records which link was
    spent. This migration alters no existing table; `20261122120000_quote_history_source`
    (#400) changes `quote_status_changes`.
  - **Not included.** The page that answers the link is #383. The public view holds no
    personal data. Erasing the customer of
    a sent quote invalidates its mailed links since #395 (see Fixed).
  - **Schema:** migration `20261121120000_add_quote_response_tokens` on MySQL/MariaDB (one
    `CREATE TABLE IF NOT EXISTS`, no foreign key), `MigrateDatabase` on SQLite; rollback drops
    the table.

- **Role screens in Settings and a role picker on the user form (#381, second part of S6 of
  #260).** Settings gains "Roles" (`/settings/roles`, shown with `roles.view`): the four system
  roles and the custom ones with their user and permission counts. With `roles.manage` a system
  role can be cloned, and a custom role edited, cloned and deleted; a role that users still hold
  is not deleted and the confirmation names how many hold it. The role editor
  (`/settings/roles/new`, `/settings/roles/:id`) takes a name, a description and the permission
  tree of `GET /permissions/catalogue`: a tri-state checkbox per group, an All/Own choice beside
  each ticked permission that can be limited to own records, and no entry for a permission only
  a system role holds. A system role, and any role for a caller without `roles.manage`, opens
  read-only. A change to a custom role applies to its holders on their next request. Frontend
  only: no route, schema or API change.
  - **User form.** The role select lists the roles of `GET /roles` (system and custom) instead
    of four fixed values and sends `role: "custom"` with `role_id` for a custom role. An edit
    that does not touch the picker sends the stored role back unchanged, also when that role is
    past the 100 roles the list returns (shown as `Role #<id>`). A customer login is offered the
    system roles only. Without `users.assign_role` the picker is no longer rendered (it used to
    be shown disabled): a new account is created as a customer and an update sends no role. The
    refusals `LAST_ADMIN`, `CUSTOMER_LOGIN_ROLE` and `ROLE_ESCALATION` each show their own
    sentence in the form. The role label of a custom-role user on the Users list and detail
    pages is unchanged (the raw value).
  - **Tests.** `pages/settings/RoleList.test.tsx`, `pages/settings/RoleEditor.test.tsx`,
    `pages/users/UserForm.roles.test.tsx`, added cases in `routes/index.permissions.test.tsx`
    and `layouts/MainLayout.permissions.test.tsx`, and the browser spec
    `gocrm-ui/e2e/tests/roles.spec.ts` (clone Sales, take the deal permissions out, assign the
    role, sign in as its holder, grant "View deals" again and see it arrive on window focus).
    The spec selector adds `roles` to the `user` spec set; a change under `pages/settings/`
    still selects the whole browser suite.
- **Permission-driven routes, navigation and buttons in the SPA (#380, first part of S6 of
  #259).** The SPA no longer decides by role name. `AuthProvider` publishes the effective
  permissions of `GET /users/me` on session restore, after a login, after a registration and
  again whenever the window regains focus; the new `usePermissions` hook exposes `can(key)` and
  `scopeOf(key)`; `ProtectedRoute` takes `requiredPermission` (one key or an any-of list) in place
  of `requiredRole`; and the route table, the sidebar and every page control that read the
  signed-in user's role now ask for the permission key of the API call behind them. A user on a
  custom role therefore sees what its role grants. A missing or empty permission list grants
  nothing, whatever the role is called. `/tickets` and `/tickets/:id` (any of `tickets.view`,
  `tickets.view_own_customer`), the task pages (`tasks.view`) and `/labels` (`labels.view`) gain
  a guard that excludes no system role. A refused focus refetch (401 or 403) signs the tab out;
  any other failure keeps the session. Frontend only: no route, schema or API change.
  - **Changed for the system roles.** The `customer` role is redirected to `/unauthorized` from
    `/customers` and `/customers/:id` (both now need `customers.view`; before, the pages rendered
    empty behind a 403) and no longer sees the dashboard "View Customers" button. `support` and
    `customer` are redirected from `/customers/:id/edit` (now `customers.update`), and `support`
    no longer sees the customer Edit controls on the customer list and detail pages. Everything
    else is unchanged for admin, sales, support and customer; the customer Delete controls stay
    as they were (shown, and refused by the API for support).
  - **Not in this change.** The role screens in Settings, the role picker of the user form and
    the browser spec for custom roles came with the second part of #260 (#381, above); the role
    label of a custom-role user still shows the raw value.
  - **Tests.** `usePermissions.test.tsx`, `ProtectedRoute.permissions.test.tsx`,
    `MainLayout.permissions.test.tsx`, `AuthContext.permissions.test.tsx`,
    `routes/index.permissions.test.tsx`, `TicketDetail.permissions.test.tsx`,
    `permissions/noRoleGating.test.ts` (fails on any role-name gate in the source) and
    `permissions/systemRoleFixture.test.ts` (the UI test fixture equals
    `internal/permissions/testdata/system_roles.golden.json`).
- **Least-privilege integration user, documented and tested (#211).** `docs/AUTHORIZATION.md`
  has a new section "Least-privilege integration user": a custom role holding only the keys the
  integration needs, a dedicated user on it and an API key issued to that user, with the keys for
  ticket and lead intake, key rotation and revocation. `TestLeastPrivilegeIntegrationUserSQLite`
  and `TestLeastPrivilegeIntegrationUserMySQL` in `internal/integration` build that setup through
  `POST /roles`, `POST /users` and `POST /api-keys` and show a key whose role holds
  `customers.create` and `tickets.create` creating a customer and a ticket and answered 403
  `FORBIDDEN` on every other route tried, the user, role and export routes among them.
  `docs/EXTENDING.md`, `docs/FORKING.md` and `docs/USE_CASES.md` no longer say that a key carries
  its owner's full role. No route, schema or SPA change.
- **Authorization guide `docs/AUTHORIZATION.md` (#349, slice S5 of #259), with a test.** The
  guide holds the phased plan of #259, the permission catalogue, the matrix of the four system
  roles, custom roles and scopes, the self-service routes, the service-boundary
  adapter table, the last-admin invariant, the escalation rule, the audit trail, and a design note
  for phase 2 (#261: single sign-on, identity linking, provisioning), which is not implemented.
  `internal/permissions/authorization_guide_test.go` compares the guide's tables with
  `permissions.Leaves()` and `permissions.ForSystemRole`, and pins the pointers to the guide in
  README, `docs/FEATURES.md` and this file. No route, schema or SPA change.
- **Role API, last-admin invariant, escalation rule and audit trail (#348, slice S4 of #259).**
  Eight new routes: `GET /permissions/catalogue`, `GET` and `POST /roles`, `GET`, `PUT` and
  `DELETE /roles/{id}`, `POST /roles/{id}/clone` and `GET /roles/audit`. Reading the catalogue and
  the roles needs `roles.view` or `users.assign_role`, every role write `roles.manage`, the audit
  listing `roles.audit_view`; the admin system role holds all four. The four system roles can be
  cloned but neither edited nor deleted (409 `SYSTEM_ROLE_IMMUTABLE`). `role` gains the value
  `custom`: `POST /users` and `PUT /users/{id}` accept it together with `role_id`, and
  `models.User` returns `role_id` for an account on a custom role.
  - **New error codes.** 409 `LAST_ADMIN`, 403 `ROLE_ESCALATION`, 409 `CUSTOMER_LOGIN_ROLE`, 400
    `INVALID_ROLE_ASSIGNMENT` (role `custom` without `role_id`, or `role_id` with any other role),
    400 `INVALID_PERMISSION` (an unknown key, a key only a system role holds, a key named twice or
    a scope the key does not allow; `details.keys`), 409 `SYSTEM_ROLE_IMMUTABLE`, 409 `ROLE_IN_USE`
    (a live user, deactivated included, still holds the role; `details.user_count`), 409
    `ROLE_NAME_TAKEN` and 404 `ROLE_NOT_FOUND`.
  - **Last admin.** No role change, deactivation or erasure can leave zero active admins: a
    change that would is refused with 409 `LAST_ADMIN`, also when the last admin demotes or
    deactivates itself through `PUT /users/{id}`, which that route accepted before. Only active,
    non-erased users on the admin system role count.
  - **Escalation rule.** A caller that is not on the admin system role can neither grant
    permissions above its own (create, update or clone a role, assign a role, create a user with a
    role) nor edit or delete a role whose permissions are above its own, nor edit, activate,
    deactivate or erase a user whose permissions are above its own. `own` is below `all`, a
    missing key below `own`. Behaviour change: `PUT /users/{id}` on another account is now refused
    with 403 `ROLE_ESCALATION` for a manager below the target, profile fields included (changing a
    superior's email would redirect its password reset). The rule applies to the caller's own
    account too: it cannot give itself a role above its own.
  - **A customer login is the floor of the rule.** The `customer` system role never counts as
    above a manager, as a target and as a requested role: its matrix holds
    `tickets.view_own_customer`, the one key no custom role can hold. So a custom role holding
    `users.create` alone can create a customer login (any other role needs `users.assign_role`,
    403 `FORBIDDEN` otherwise), and one holding `users.update`, `users.activate` or `users.delete`
    can edit, deactivate, activate and erase a customer login (one with a linked customer record
    also asks for a customer key on erasure and on an email change, see #378 under Changed). A
    customer login cannot receive a custom role (409 `CUSTOMER_LOGIN_ROLE`).
  - **Audit trail.** Every role create, clone, update and delete and every change of a user's
    `role` or `role_id` (the role a new account is created with included) commits one
    `role_audit_log` row in the same transaction (`role_created`, `role_updated`, `role_deleted`,
    `user_role_changed`), with the acting user, how the request authenticated (`bearer` or
    `api_key`) and the state before and after. A failed audit insert rolls the change back. Rows
    hold ids, role names, permission keys and scopes only. `GET /roles/audit` lists them, newest
    first, filterable by `role_id` and `target_user_id`.
  - **Lock order.** Every governed user write is one transaction that locks the active admins,
    then the caller and the target in ascending id, then their custom roles in ascending id; a
    role write locks the caller, then the roles. The caller's permissions are read again under
    the locks and every rule is decided there, so a request that waited behind a promotion, a
    widened role or the caller's own demotion is refused. See
    [docs/CONCURRENCY.md](docs/CONCURRENCY.md), flow "RBAC user-role and role writes".
  - **Answer order.** `PUT` and `DELETE /roles/{id}` answer `ROLE_NOT_FOUND`, `FORBIDDEN`,
    `SYSTEM_ROLE_IMMUTABLE`, `ROLE_ESCALATION` and only then `INVALID_PERMISSION` or `ROLE_IN_USE`,
    so a manager below a role does not learn how many users hold it.
  - **Tests.** The system-role route matrix of #322 is frozen and stays byte-identical; the eight
    new routes are listed in `routeMatrixAfterFreeze` and covered by the deny-by-default test (165
    protected routes) and the own-key test (`permissions.RouteKeys` has 155 entries), which accepts
    403 `ROLE_ESCALATION` as past the permission check on the five user-write routes only.
    No SPA change: the Settings screens are #260.
- **Permission catalogue and deny-by-default enforcement (#347, slice S3 of #259).** The new
  `internal/permissions` package holds the permission tree (`entity.action` leaves, each with an
  `all` or `own` scope where the leaf allows it), the matrices of the four system roles (pinned by
  `internal/permissions/testdata/system_roles.golden.json`) and `permissions.RouteKeys`, the one
  table that says which key opens which protected route. `middleware.RequirePermission` and
  `middleware.RequireAnyPermission` replace every `RequireRole` guard in the route files, and
  handlers and services decide by key and scope, so a user with `role = custom` gets exactly what
  its role row grants and 403 `FORBIDDEN` everywhere else, on the next request after the row
  changes and for `ApiKey` credentials as well. The permission check comes before any ownership
  check: a caller without the key is refused on its own records too. Ten self-service routes need
  authentication only (`GET`/`PUT /users/me`, the five `/api-keys` routes, `GET /configurations/ui`,
  `POST /auth/logout`, `POST /auth/change-password`). `GET /users/me` now also returns `role_name`
  (the system role key, or the name of the custom role) and `permissions` (`[{key, scope}]` sorted
  by key). Two keys go beyond the catalogue designed in #259 because their routes arrived later:
  `leads.export` (`GET /leads/export`) and `webhooks.manage` (the six `/webhooks` routes). The
  eligibility checks follow keys too: ticket assignee `assignment.ticket_assignee`, customer
  assignee `assignment.customer_assignee`, form default owner and lead owner
  `assignment.form_owner`. What the four system roles are answered is unchanged: the route matrix
  of #322 replays byte-identical. Custom roles are created and assigned through the role API of
  #348 (next entry). The API-docs gate does not yet require a 403 on a permission-guarded route.
- **System-role route matrix (#322, part of #259), tests only.**
  `cmd/testdata/system_role_route_matrix.json` records what the router built by `setupRouter`
  answers (HTTP status and `error.code`) to each of the four system roles on all 157 protected routes, for
  an own record, a foreign record, a missing id, a malformed id and, where the handler binds a
  body, a malformed body. Five tests in `cmd/system_role_route_matrix_test.go` keep it honest: the
  replay against the live router, route coverage (every route is recorded or on the explicit
  public list), hand-written fixture anchors that do not read the file, the provenance of the
  golden, and the comparison itself. A pull request that adds or changes a protected route
  regenerates the file with
  `go test ./cmd -run '^TestSystemRoleRouteMatrix$' -update-route-matrix`; generation refuses once
  a route file uses `RequirePermission` or `RequireAnyPermission`, while the replay keeps running
  against the frozen file. No production code changes.
- **Signed outbound webhooks for created leads, customers and tickets (#345).** Admins register
  HTTPS endpoints under `/webhooks/endpoints` (list, create, read, update, delete) with a subset of
  `lead.created`, `customer.created` and `ticket.created` and an active flag; each endpoint gets a
  signing secret (`whsec_…`) that is shown once, in the create response, and stored encrypted. Every
  create path (the API, lead conversion, form lead and form ticket intake) writes one delivery row
  per active subscribed endpoint inside its own transaction, so a rolled-back create sends nothing.
  A background sender delivers the committed rows every 5 seconds as a thin JSON body (`id`, `type`,
  `occurred_at`, `record_id`; no personal data) with Standard Webhooks headers (`webhook-id`,
  `webhook-timestamp`, `webhook-signature` = HMAC-SHA256 over `id.timestamp.body`), through the
  public-only egress guard (no private, loopback, link-local or metadata destinations, no redirects,
  10-second timeout). Failures are retried with exponential backoff (1 minute doubling, at most 6
  hours) up to 10 attempts. `GET /webhooks/deliveries` lists the delivery log; finished rows are
  pruned after 30 days. `POST /customers` now inserts the customer and its outbox rows in one
  transaction. **Schema:** migration `20261120120000_add_outbound_webhooks` on MySQL/MariaDB,
  `MigrateDatabase` on SQLite; rollback drops the two tables. **Operator follow-up:** register the
  new test roots in the CI gate files.
- **Role storage for custom roles (#344, RBAC slice S2 of #259).** New tables `roles`,
  `role_permissions` and `role_audit_log`, and `users.role_id` (with `idx_users_role`,
  `idx_users_role_id` and the foreign key `fk_users_custom_role`, ON DELETE RESTRICT). Every start
  seeds the four built-in roles when missing (Admin "Full access. Cannot be edited or deleted.",
  Sales "Built-in sales access. Cannot be edited or deleted.", Support "Built-in support access.
  Cannot be edited or deleted.", Customer "A customer's own login. Cannot be edited, deleted or
  cloned into portal access.") and never rewrites an existing one; a start fails, naming the
  system key, when a custom role already holds the name of a missing built-in role. No API
  change: no route reads the new tables yet, and every response is unchanged.
  **Schema:** migration `20261119120000_add_roles_and_permissions` on MySQL/MariaDB,
  `MigrateDatabase` on SQLite (which rebuilds `users` to add the foreign key). **Rollback:** the
  MySQL/MariaDB down turns every live custom-role user into an inactive support user before it
  drops the role objects, so take a backup first; SQLite has no down path, so a rollback restores
  the pre-upgrade backup.
- **Fractional quantities in the quote line dialog (#346).** The web UI's Add line and Edit line
  dialogs accept a quantity from 0.001 to 1,000,000 with up to three decimals (a dot as the decimal
  separator, no thousands separator); anything else is refused on the spot with "Quantity must be
  a number from 0.001 to 1,000,000 with at most three decimals" and nothing is sent. When the API
  refuses the quantity, its message shows on the Quantity field instead of the generic error. The
  quote detail and print pages show quantities without trailing zeros (`1.5`, `2`, `0.001`); line
  and quote totals are still exactly the server's.
- **Fractional quote line quantities (#343).** A quote line quantity may now have up to three
  decimal places, from 0.001 to 1,000,000 (1.5 hours, 0.25 kg). `POST /quotes/{id}/lines` and
  `PUT /quotes/{id}/lines/{lineId}` take and return `quantity` as a JSON number (`1.5`, `2`,
  `0.001`, never a string); whole numbers work and read exactly as before. Zero, a negative value,
  a value above 1,000,000 or a fourth non-zero decimal is 400 `VALIDATION_ERROR` with
  `error.details.quantity`, and nothing is written; a quantity sent as a string is 400
  `BAD_REQUEST`. The line total is unit price × quantity after the discount, rounded half up to the
  cent once and computed exactly (no floating point); the quote subtotal sums the rounded line
  amounts; whole quantities give the same totals as before. The quote email prints the quantity
  without trailing zeros (`quantity 1.5 x 9.99 EUR`).
  **Schema:** `quote_lines.quantity` is replaced by `quote_lines.quantity_milli` (thousandths of a
  unit), through migration `20261118120000_store_quote_line_quantity_milli` on MySQL/MariaDB and the
  application's startup migration on every driver; existing quantities keep their value. **Upgrade:**
  stop every replica before starting the new version. **Rollback:** an older binary started on the
  upgraded database reads every quantity as 0, so first run the down migration on MySQL/MariaDB
  (whole quantities restored, fractional ones rounded half up, at least 1) or restore the pre-upgrade
  backup on SQLite.
- **The embed script sends an idempotency key (#341).** `embed.js` now resolves one key per
  rendered form: the hosted view's `data-idempotency-key` when it is valid, otherwise 16 bytes from
  `crypto.getRandomValues` as 32 lowercase hex characters, otherwise none (the field is then
  omitted; there is no `Math.random` fallback). It sends that key as `idempotency_key` on every
  submission of the render, a resend after an error or a network failure included, and never logs
  it. A resend on a `create_ticket` form therefore opens at most one ticket after confirmation.
  Leads are not deduplicated: `create_lead` and no-action forms ignore the key. `embed.js` is still
  served `public, max-age=3600`, so browsers may run the previous script, which sends no key, for
  up to an hour after the upgrade. No API change. Roots (`internal/handler`):
  `TestFormEmbedScriptIdempotencyKeyContract`, `TestFormEmbedScriptIdempotencyKeyNoWeakFallback`,
  `TestFormEmbedScriptIdempotencyKeyNeverLogged`, `TestFormEmbedScriptIdempotencyKeyBehaviour`
  (needs `node`); browser spec `gocrm-ui/e2e/tests/form-embed-idempotency.spec.ts`.
- **Per-render idempotency key on the hosted form view (#340).** Every render of
  `GET /forms/public/{key}/view` puts one fresh random key (128 bits from `crypto/rand`, 32
  lowercase hex characters, valid as the `idempotency_key` of the public submission) on the
  embed script tag as `data-idempotency-key`, and the page is now served with
  `Cache-Control: no-store` so no cache shares one render's key. If the server's random source
  fails the page is served without the attribute and one Warn line (`operation=hosted_view_key`,
  `error_code=random_unavailable`) is logged; the key is never logged. The embed script sends
  this key since #341 (above). No API shape, route or status change. Roots (`internal/handler`):
  `TestHostedViewRendersPerRenderIdempotencyKey`, `TestHostedViewIsNotCached`,
  `TestHostedViewWithoutRandomnessOmitsKey`, `TestHostedFormResubmissionWithSameKeyOpensOneTicket`.
- **Ticket action in the form builder (#335).** The builder's "Lead capture" panel is now
  "Submission action", one choice between "Create a lead", "Create a support ticket" (#213) and
  "Create nothing". The ticket action switches double opt-in on, turns lead creation off and hides
  the lead owner and the repeat-lead note; the owner is sent as 0 unless the action is lead. Saving
  refuses, on the field and without a request, a ticket form without double opt-in ("A form that
  creates tickets needs double opt-in") and a stored form that both creates leads and tickets ("A
  form cannot both create leads and create tickets"). The double opt-in switch reads "Require email
  confirmation before a lead or ticket is created", and FormDetail shows "Creates tickets: yes/no".
  Frontend only, no API change. Vitest: `FormBuilder.ticket.test.tsx`, `FormBuilder.test.tsx`,
  `FormDetail.test.tsx`; browser: `e2e/tests/form-builder-ticket.spec.ts`.
- **Quote tax in the web UI (#325).** The quote line dialog has a "Tax (%)" field (0 to 100, at
  most two decimals, the discount rule; `0` on a new line, the stored rate on an edit) that is
  always sent as `tax_rate_bp`; an invalid rate is refused on the field without a request, and a
  server refusal shows its message in the dialog. The quote detail and print pages gain "Tax rate"
  and "Tax" columns per line and show "Net total" (`total_cents`, test ids `quote-total` and
  `quote-print-total` unchanged), "Tax" and a bold "Gross total" from the server values, on every
  status; the print page's empty row spans seven columns. The quotes list and the deal's Quotes
  section still show the net total. Frontend only, no API change. Vitest:
  `QuoteLineDialog.test.tsx`, `QuoteDetail.tax.test.tsx`, `QuotePrint.tax.test.tsx`; browser:
  `e2e/tests/admin-quotes-tax.spec.ts`.
- **Per-line tax rate and tax/gross quote totals (#324, backend).** A quote line takes
  `tax_rate_bp` (0–10000 basis points, 2100 = 21 %, default 0, writable only while the quote is a
  draft) on `POST /quotes/{id}/lines` and `PUT /quotes/{id}/lines/{lineId}` (absent keeps the stored
  rate, 0 clears it; out of range is 400 `VALIDATION_ERROR`). The server stores the line's
  `tax_cents` (`(line_total_cents × tax_rate_bp + 5000) / 10000`, rounded half up per line) and the
  quote's `tax_cents` (Σ line tax) and `gross_total_cents` (`total_cents + tax_cents`), recomputed in
  the same transaction as the other totals; every quote response carries the new fields and
  `total_cents` keeps its meaning (net after discount). The 10^12 cap now applies to the gross
  total, and a refused write changes nothing. The plaintext quote email shows each line's tax and
  the net, tax and gross totals (`Total:` is now `Net total:`). Schema:
  `migrations/20261117120000_add_quote_tax` (MySQL/MariaDB, each column only when missing, plus a
  gross backfill) and AutoMigrate with an idempotent `MigrateDatabase` backfill on SQLite; existing
  quotes read tax 0 and gross = total. The web UI follows in #325; no tax categories, reverse
  charge or per-country rules.
- **One lock order for erasure, conversion, authentication and role writes (#323).**
  `docs/CONCURRENCY.md` records, for each of 13 flows, the rows locked, the order, the transaction
  helper, the retry and whether the order is implemented, plus the three lock chains, the
  undecided cross-chain order (owned by C4), the retry rule (`WithTransactionAndRetry`, current for
  lead conversion only), how the #175 failure modes are avoided and a flow → ticket map. Linked
  from EXTENDING, DEVELOPMENT, ROADMAP and README, and pinned to the code by
  `internal/config/concurrency_doc_test.go`. Documentation only; no behaviour changes.
- **Lease-based automation run claims (#237).** Several API replicas may now run the follow-up
  automation worker (`AUTOMATION_ENABLED=true`) against one MySQL/MariaDB database. Each claim
  records the engine's instance id and a heartbeat in `automation_runs.owner_instance` and
  `heartbeat_at` (both hidden from the API; `claimed_at` is unchanged), renewed every 30 s while
  the run executes. Every write after the claim is fenced on that owner: the step advance, the
  release, the first `send_email` transaction, a check just before the send and the email stamp
  (only while the row is still `sending`); cancels clear the owner. The boot-only, owner-blind
  reconciliation is replaced by a stale sweep that runs at boot and at the start of every worker
  tick on every replica: a running run whose heartbeat is NULL or older than 3 minutes goes back to
  `pending` at its position (attempts unchanged), or is failed with `email_unconfirmed` when its
  email is still `sending`; a live run on another replica is never touched. No step runs twice
  and no mail is sent twice, and a run abandoned by a crashed worker is recovered within the
  stale horizon without a restart. An executor paused between the pre-send check and the relay
  call can still send once after the sweep recorded `email_unconfirmed` (never resent). Schema
  migration `20261116120000_add_automation_run_leases` (MySQL/MariaDB, each column only when
  missing; SQLite through `MigrateDatabase`); no API change. **Upgrade by stopping every replica
  before starting the new version** (an older binary's boot reconciliation resets live runs), and
  keep replica clocks within well under 90 s. Several processes on one SQLite file stay
  unsupported. Registering the new MySQL roots in `scripts/ci/check-go.py` and the migration roots
  in the Database migrations workflow is an operator follow-up.

- **Form → ticket intake (#213).** A double opt-in form with `create_ticket` (admin or sales on
  `POST /forms` and `PUT /forms/{id}`; absent means false, excludes `create_lead`, which then
  defaults to false; with `create_lead` true or without double opt-in it is the existing 400) opens
  one open, medium-priority, unassigned ticket when a pending submission is confirmed, for the
  live customer whose email is the confirmed address or its lower-cased form. The ticket text is
  fixed (`Form: <form name>`, `Submitted through form <id>, submission <id>.`) and holds no
  submitted value. Ticket, `form_ticket_keys` row and `ticket_created` event (origin `form`) commit
  in the confirmation transaction after the token claim. `POST /forms/public/{key}/submissions`
  accepts an optional `idempotency_key` (`^[A-Za-z0-9._:-]{1,128}$`, not starting with
  `submission-`; anything else is 400 `VALIDATION_ERROR` with `error.details.idempotency_key`,
  the value not echoed): confirmations carrying the same key open at most one ticket per form.
  A keyless submission uses `submission-<id>`, so retries from the hosted view and the embed
  script, which send no key, are not deduplicated yet. Each submission records `ticket_outcome`
  (`created`, `duplicate`, `key_conflict`, `no_customer`, `unavailable`) and `ticket_id`; the
  public answers are identical for every outcome. Key rows are hard-deleted with their ticket
  and by every erasure of the customer or of the submitter, which also empties the submission's
  key. Schema migration `20261115120000_add_form_ticket_intake` (MySQL/MariaDB; SQLite through
  `MigrateDatabase`): `forms.create_ticket`, `form_submissions.idempotency_key`, `ticket_id`,
  `ticket_outcome`, `idx_form_submissions_ticket_id` and the `form_ticket_keys` table. Backend
  only; the form builder and submission views follow in a frontend ticket.
- **AEO results export (#244).** `GET /aeo/export` (admin, sales, support; customers 403) downloads
  the answers and citations of one run (`run_id`) or of a 7, 30 or 90-day window (`days`, default
  30, any other value 400 rather than clamped; with `run_id` also 400) ending at the start of
  tomorrow UTC, as CSV (`format=csv`, default: pinned columns, one row per citation and one row per
  answer without citations, no answer text, every free-text cell guarded against formulas
  including a leading line feed and the full-width ＝ ＋ － ＠) or JSON (`format=json`:
  `models.AEOExport` with runs, answers, answer text and citations). Attachments are named
  `aeo-export-run-<id>.<ext>` or `aeo-export-<days>d-<YYYY-MM-DD>.<ext>`. At most 10,000 answers:
  a larger selection is 422 `EXPORT_TOO_LARGE` (`error.details.max_answers`), detected before any
  citation is read; an unknown run is 404. Every row is read before the first byte is written; the
  run-ownership columns of #229 are never exported; log lines carry ids, counts and error codes
  only. No schema change. The web UI download control followed in #318 (below). Still open:
  threshold alerts and scheduled client reports.
- **AEO export download control in the web UI (#318).** The AEO dashboard header has Export CSV
  and Export JSON buttons that download the selected 7, 30 or 90-day window
  (`GET /aeo/export?format=…&days=…`), and the prompts answer drawer has Export run as CSV /
  Export run as JSON buttons that download the run chosen in its run filter (`run_id`; every
  prompt of that run, disabled while "All runs" is selected). The controls are shown to admin,
  sales and support only. The file is named by the client (`aeo-export-run-<id>.<ext>` or
  `aeo-export-<days>d-<YYYY-MM-DD>.<ext>`, the browser's UTC date), because CORS does not expose
  `Content-Disposition`; around UTC midnight on a skewed clock it can differ from the server's
  name. Errors are read from the Blob body and shown with a status-specific message (422 with the
  10,000-answer limit, 400, 403, 404, otherwise a generic failure); no file is saved on error.
  Frontend only; no API change.
- **AEO run ownership and the daily slot across API replicas (#229).** `aeo_runs` gains the
  nullable `owner_instance`, `heartbeat_at`, `running_lock` and `schedule_slot` columns (none is
  serialised) with the unique indexes `idx_aeo_runs_running_lock` and `idx_aeo_runs_schedule_slot`
  (migration `20261114120000_add_aeo_run_ownership` on MySQL/MariaDB, each added only when missing;
  `MigrateDatabase` on SQLite). A run is admitted by one insert guarded by the running lock, so a
  run on any replica answers 409 to a start on another; the owner renews its heartbeat every 30 s;
  the scheduler claims each daily slot once (`StartScheduledRun` with the `NextRunAt` instant),
  and other replicas log a skip. Boot reconciliation and the sweep before every start fail only
  runs whose heartbeat is older than 3 minutes (or NULL) or that started over 6 hours ago, so a
  starting replica no longer fails another replica's live run; a crashed run is recovered up to
  3 minutes after the crash. Finalisation is a conditional owner+status update (`FinishRun`)
  instead of `Save`: a run another replica failed stays failed and its owner's next heartbeat
  cancels it. Upgrade by stopping every replica before starting the new version; replicas must
  share `TZ` and `AEO_SCHEDULE_HOUR`. API status codes and shapes are unchanged.
- **Ticket rules in the automation rule form, rule list, run list and dry run (#312).** The rule
  form offers the entity Ticket with the triggers Ticket created, Ticket updated, Ticket status
  changed (From status / To status: Open, In progress, Resolved, Closed) and Schedule (Only
  status, sent as `trigger.filters.ticket_status`); conditions on Status, Priority, Owner ID and
  Customer ID (status and priority values suggested); and the ticket placeholder lists. Client
  validation mirrors the API's ticket allowlists (`gocrm-ui/src/types/automation.ts`), and a
  refusal of `trigger.filters.ticket_status` or of a non-support `assign_owner` user is shown on
  its field. The rule list labels ticket rules Ticket, the run list filters by Ticket and links a
  ticket run to `/tickets/<id>`, the dry run picks a ticket (`T-000042 · <subject>`), and a ticket
  notification links to its ticket (admin, sales and support). Lead and deal rules are unchanged.
  Frontend only; browser spec `gocrm-ui/e2e/tests/automation-ticket-rules.spec.ts`.
- **Ticket triggers for follow-up automation (#242).** Automation rules take `entity_type: ticket`
  with the triggers `ticket_created`, `ticket_updated`, `ticket_status_changed` (from/to ticket
  statuses) and `schedule` (`filters.ticket_status`, `stale_days`). `TicketService.Create` and
  `Update`, wired with `service.WithTicketAutomationEvents(automationRepo, txManager)` in
  `cmd/main.go`, record the events in the ticket write's transaction; the update now locks the
  ticket row first and runs the closed-reopen and assignee checks against it. The bulk ticket
  status batch reads its tickets with a locking read in id order and records the events (origin
  `bulk`, actor the caller). A ticket rule's owner is the assignee; conditions test `status`,
  `priority`, `owner_id` and `customer_id`; placeholders are `ticket.id`, `ticket.number`,
  `ticket.status`, `ticket.priority` (plus `owner.*` outside tasks), never ticket text or customer
  data; `create_task` links the ticket's customer; `assign_owner` needs a support or admin user and
  writes only `assigned_to_id`. `DELETE /tickets/{id}` cancels the ticket's pending, waiting and
  running runs with `record_deleted` and purges its notifications in its transaction. The rule and
  run list filters and both dry-run bodies accept `ticket`; an unknown `entity_type` filter now
  answers `entity_type must be lead, deal or ticket`. No schema change.
- **Docker UI image `VITE_APP_NAME` build argument (#303).** `gocrm-ui/Dockerfile` declares
  `ARG VITE_APP_NAME=` and `ENV VITE_APP_NAME=$VITE_APP_NAME` in its build stage, before
  `npm run build`, so `docker build --build-arg VITE_APP_NAME=Acme gocrm-ui` makes the signed-out
  sign-in and registration pages read "Welcome to Acme" and "Create your Acme account". The empty
  default keeps `GoCRM`; signed-in users still see the stored `general.company_name`. The value is
  not a secret: Vite inlines it into the public JavaScript bundle nginx serves.
  `gocrm-ui/.dockerignore` still excludes `.env` files. The shipped Compose files forward it
  through `build.args` (#308, below). Documented in `docs/DOCKER.md`;
  `gocrm-ui/src/hooks/productNameBuildArg.test.ts` pins the Dockerfile and the documentation, and
  `gocrm-ui/e2e/tests/product-name-signed-out.spec.ts` checks the signed-out pages in a browser.
- **Compose `build.args` pass-through of `VITE_APP_NAME` (#308).** The `ui` service in
  `docker-compose.yml` and `docker-compose.sqlite.yml` now builds with `context: ./gocrm-ui` and
  `args: VITE_APP_NAME: ${VITE_APP_NAME:-}`, so a `VITE_APP_NAME` set in the shell or the repo-root
  `.env` reaches the UI image on `docker compose build ui` or `docker compose up --build`; unset or
  empty keeps `GoCRM`. The value stays public build-time text in the build stage, never in the
  nginx stage. The Compose line citations in `docs/DOCKER.md` and `docs/FORKING.md` moved with the
  three added lines; `internal/config/compose_ui_build_args_test.go` pins the Compose shape, the
  Dockerfile build stage, the documentation and the citations.
- **Release-tarball decisions in the server provisioning guide (#305).**
  `docs/SERVER_PROVISIONING.md` records the founder decision of 13 Nov 2026 that SQLite is not
  supported on the release-tarball path: `receive-release` keeps only its `mysqldump` backup, with
  no SQLite backup branch. Its *Schema migrations* section now gives the exact manual step,
  `systemd-run --pty --uid=gophercrm --gid=gophercrm -p EnvironmentFile=/srv/gophercrm/env/gophercrm.env
  /srv/gophercrm/current/bin/migrate -command up -dir /srv/gophercrm/current/migrations`, run as
  root only after the deploy reports the revision live (never after a rollback), and the recovery
  of a dirty schema (keep the backup; restore it, or revert by hand and `-command force` the last
  version that applied cleanly; rerun `up`). The step stays manual; `receive-release` is
  unchanged. The sample unit, environment and nginx files of #305 item 1 are not delivered yet.
  `internal/config/server_provisioning_guide_test.go` pins the decision, the command against
  `cmd/migrate` and this entry.
- **Server provisioning guide for the release-tarball path (#231).** `docs/SERVER_PROVISIONING.md`
  takes a fresh systemd/nginx Linux host to its first deploy through `scripts/deploy/bootstrap.sh`
  and the Deploy workflow: packages, the `gophercrm` system user, `/srv/gophercrm` and the root-only
  environment file, a hardened sample systemd unit, a least-privilege MySQL/MariaDB database and
  user whose name and password match the environment file, nginx with a certbot webroot and TLS,
  the first admin created interactively, and the `mysqldump` backups `receive-release` takes. The
  backend runs `models.MigrateDatabase` at every start; the SQL half in the tarball's
  `migrations/` is applied by hand as FORKING §7.4 describes, since `receive-release` does not
  run it. SQLite is not supported on that path. Docs only: nothing under `scripts/deploy` changes.
  Linked from FORKING §5.2, the README and `docs/SETUP.md`;
  `internal/config/server_provisioning_guide_test.go` pins the guide to the deploy scripts,
  `.env.example` and `config.Load`.
- **Production Compose override example (#222).** `docker-compose.production.example.yml` at the
  repository root is applied with a second `-f` to either `docker-compose.yml` or
  `docker-compose.sqlite.yml`. It forwards `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`,
  `SMTP_FROM`, `APP_BASE_URL`, `PUBLIC_BASE_URL`, `CORS_ALLOWED_ORIGINS`, `RECAPTCHA_SITE_KEY`,
  `RECAPTCHA_SECRET_KEY`, `RECAPTCHA_MIN_SCORE`, `AUTOMATION_ENABLED`,
  `AUTOMATION_POLL_INTERVAL_SECONDS`, `AEO_SCHEDULE_ENABLED` and `AEO_SCHEDULE_HOUR` to the backend
  through `${...}` interpolation only. `APP_BASE_URL`, `PUBLIC_BASE_URL` and `CORS_ALLOWED_ORIGINS`
  are required (Compose stops with a message when one is unset); a blank optional value keeps the
  backend default. It removes the backend's host port (`!reset []`) and binds the UI to
  `127.0.0.1:${UI_PORT:-3000}` (`!override`, Compose 2.24.4 or later; an older Compose such as
  2.19.0 accepts the file but ignores `!override` and keeps the all-interfaces UI publication, so
  check the merged ports with `config` without `-q`). Values come from the shell
  or the repo-root `.env`; for its 15 keys the example masks the SQLite stack's `/data/.env`. It
  leaves `TRUSTED_PROXIES`, the subnet, the project name, volumes and secrets alone, and the shipped
  Compose files are unchanged. Documented in `docs/DOCKER.md` ("Production override example");
  `internal/config/compose_production_example_test.go` pins its shape, its mapping onto
  `config.Load` and the documentation.
- **Email a staff ticket reply to the customer (#219, backend).** `POST /tickets/{id}/comments`
  accepts an optional `email_customer` boolean. Admin, and the support user the ticket is assigned
  to, may set it on a comment that is not an internal note (with `is_internal` it is 400
  `VALIDATION_ERROR`; customer, sales and unassigned support callers keep their 403). The comment,
  its `first_responded_at` stamp and a `pending` delivery record commit in one transaction; after
  the commit one plaintext mail goes to the ticket customer's current address (subject
  `Ticket T-000042 from GopherCRM`, since #292 the configured product name; the ticket number, the title and this comment's text only), and
  the record is stamped `sent`, `failed` (`mail_failed`, `timeout`, `invalid_recipient`,
  `lookup_failed`), `log_only` (no `SMTP_HOST`) or `skipped` (`no_mailer`, `no_recipient`,
  `customer_unavailable` for an erased or missing customer). The 201 carries it as `delivery`
  whatever the outcome; no read path returns it and nothing retries. The new
  `ticket_mail_deliveries` table holds ids, a status, an error code and timestamps — never an
  address or content — and its rows are hard-deleted when their ticket is deleted and when the
  ticket's customer is erased (all three erasure roots). MySQL/MariaDB get the table from migration
  `20261113120000_add_ticket_mail_deliveries` (created only when missing), SQLite from
  `MigrateDatabase`. Registering the migration roots in the Database migrations workflow is an
  operator follow-up; the comment-form checkbox is a later frontend ticket.
- **Product name and primary colour from the settings (#291).** The web app names itself through
  one hook, `useProductName`: the stored `general.company_name` when a signed-in user's
  configuration holds a non-blank value, otherwise `VITE_APP_NAME`, otherwise `GoCRM`. The
  navigation drawer, the printed quote heading and the sign-in and registration pages use it; the
  sign-in and registration pages render signed out, so they show the build-time name from the first
  render and request no configuration. A fresh install stores `GopherCRM`, so signed-in users see
  that name until an admin changes it. The light theme's primary colour now follows
  `ui.theme.primary_color` (`BrandThemeProvider`, under the configuration provider): 3- or 6-digit
  hex, `rgb()`/`rgba()` and `hsl()`/`hsla()` values are applied and MUI derives the light and dark
  variants; any other value, and every signed-out page, keeps `#1976d2`. The dark theme, secondary
  colour and snackbars are unchanged. Frontend only; no API change. Mail subjects followed in
  #292 and the Docker `VITE_APP_NAME` build argument in #303.
- **External identity mappings for products (#239).** A new `product_external_ids` table maps an
  integrator's `(source, scope, external_id)` to a product, unique on the exact, case-sensitive
  triple with the customer mappings' rules (source 1-64, scope 0-128 and `''` by default,
  external_id 1-255 characters; no control characters and no leading or trailing whitespace;
  `utf8mb4_bin` on MySQL/MariaDB). Rows are hard-deleted. Admins create one with
  `POST /products/{id}/external-ids` (201; 409 when the triple is already mapped, to this or another
  product; 404 for a missing or deleted product) and delete it with
  `DELETE /products/{id}/external-ids/{mappingId}` (204; 404 for another product's mapping); sales
  gets 403 on both. `GET /products?source=&scope=&external_id=` answers the list shape with at most
  one live product, inactive ones included, to admin and sales. `DELETE /products/{id}` now
  hard-deletes the product's mappings and soft-deletes the product in one transaction, so its keys
  can be mapped again; quote lines keep their `product_id`. `products.sku` stays non-unique; no UI.
  MySQL and MariaDB get the table from `20261112120000_add_product_external_ids`, SQLite from
  `MigrateDatabase`, which also converts the three columns to `utf8mb4_bin` on a MySQL schema the
  application alone created. Registering the migration roots in the Database migrations workflow is
  an operator follow-up.
- **Lead list CSV export button (#290).** Admins see "Export all leads (CSV)", sales users "Export
  my leads (CSV)"; support and customer users see no button. It downloads `GET /leads/export` as
  `leads-export.csv` and ignores the list filters (no query parameters are sent); the button is
  disabled while the request is pending, and a failure shows "Could not export leads." and saves
  no file. Frontend only; no API change.
- **Task–deal link in the web client (#240).** The task form has a Deal picker for admin and sales
  users (sales see only their own deals), searched on the server and offering won and lost deals
  too; its search results are cached apart from the quote dialog's open-only deal picker. Support
  and customer users get no picker and never send `deal_id`. Editing a task sends `deal_id` only
  when the picker was changed; clearing it sends `deal_id: 0`, and after every update the form
  re-reads the task and opens it as the API returned it, so a clear the API refused (a link to a
  deal the user cannot see) still shows the link. Task detail shows the linked deal's title, as a
  link to the deal, and its stage whenever the API returned a deal summary. Deal detail has a
  paginated Tasks section (sales see only the tasks assigned to them). Every task save marks the
  cached deal task lists stale. Frontend only; no API change.
- **External identity mappings for customers (#212).** A new `customer_external_ids` table maps an
  integrator's `(source, scope, external_id)` to a customer, unique on the exact, case-sensitive
  triple (source 1-64, scope 0-128 and `''` by default, external_id 1-255 characters; no control
  characters and no leading or trailing whitespace; `utf8mb4_bin` on MySQL/MariaDB). Admin and sales
  create one with `POST /customers/{id}/external-ids` (201; 409 when the triple is already mapped,
  to this or another customer; 404 for a missing or erased customer) and delete it with
  `DELETE /customers/{id}/external-ids/{mappingId}` (204; 404 for another customer's mapping).
  `GET /customers?source=&scope=&external_id=` answers the list shape with at most one live
  customer to admin, sales and support. Every customer erasure path hard-deletes the customer's
  mappings in the same transaction. Customer email stays globally unique; no UI. MySQL and MariaDB
  get the table from `20261111120000_add_customer_external_ids`, SQLite from `MigrateDatabase`,
  which also converts the three columns to `utf8mb4_bin` on a MySQL schema the application alone
  created. Registering the migration roots in the Database migrations workflow is an operator
  follow-up.
- **CSV export of leads (#234).** `GET /leads/export` downloads `leads-export.csv`
  (`text/csv; charset=utf-8`) holding exactly the leads the caller may list: an admin gets every
  live lead under the list's precedence (a non-empty `search` with the sort, else an exact
  `classification` in ascending id order, else `sort_by`/`sort_order`, else ascending id order), a
  sales user only its own leads in ascending id order whatever it sends, and support and customer
  callers 403. `external_id`, `page`, `offset` and `limit` are not read. The columns are id,
  first_name, last_name, email, phone, company, position, source, status, classification,
  external_id, notes, owner_id, customer_id, company_id, created_at and updated_at; timestamps are
  RFC 3339 in UTC and every text cell gets the customer export's formula guard. The rows come from
  one ordered statement whose order ends in the lead id (#105) and are read in full before the
  first byte, so a database failure is a JSON 500, never a truncated file. Erased leads are
  absent, converted leads present. No UI button yet, and no schema change.
- **Server-side ticket filters (#218).** `GET /tickets` accepts optional `status`
  (`open`, `in_progress`, `resolved`, `closed`) and `priority` (`low`, `medium`, `high`, `urgent`)
  query parameters, matched exactly and case-sensitively. They combine with each other, with
  `search` and with `sort_by`/`sort_order`, and apply in the query before offset and limit, so
  `total` and the pagination metadata count the filtered set; for support they only narrow the
  tickets assigned to the caller, and customers still get 403. An empty value is ignored, a
  repeated parameter uses its first value, and an unknown or differently cased value is answered
  400 `VALIDATION_ERROR` with `error.details` keyed by the parameter name. The ticket screen
  already sends these parameters, so its status and priority selects now filter on the server with
  correct totals. No schema change.
- **Early revocation of access tokens (#236).** Every access JWT carries the account's
  `users.token_version` as the integer claim `token_version`, and a Bearer request whose claim is
  not the stored value is refused with the usual 401 `Invalid credentials` (a token without the
  claim reads as 0; a malformed claim is refused). `POST /auth/logout` (Bearer or API key, with or
  without a body), `POST /auth/change-password` and `POST /auth/password-reset/confirm` increment
  the counter by one in the transaction that revokes the refresh tokens, so every access token
  issued before them stops working at once; the token renewed by a password change is signed with
  the new value. Refresh rotation and login leave the counter alone, so the access token a rotation
  replaces still expires on its own. Logout is now one transaction that locks the users row before
  any refresh-token row, and refuses with a 500 when the session store is not wired. MySQL installs
  run `20261109120000_add_user_token_version`; SQLite gets the column through AutoMigrate, and
  existing accounts read 0, so no session ends on upgrade. API keys are not affected.
- **Ticket SLA fields (#278).** Tickets carry a nullable `due_at`, which admin and support callers
  set on `POST /tickets` and `PUT /tickets/{id}` as an RFC 3339 timestamp (stored in UTC with
  millisecond precision; on update an absent key keeps it and `null` clears it; any other value is
  400 `INVALID_DUE_AT` and nothing is written), and a nullable `first_responded_at`, which the server
  sets once to the `created_at` of the first non-internal comment by an admin or support user, in
  the same transaction as the comment. Internal notes, status changes, reassignments, bulk status
  updates and request bodies never set it. A read-only `overdue` flag (a past `due_at` on an open or
  in-progress ticket, compared in UTC) is computed on every read. All three appear wherever a ticket
  is returned, under the existing visibility rules. MySQL installs run
  `20261108120000_add_ticket_sla_fields`; SQLite gets the columns through AutoMigrate. Existing
  tickets are not backfilled. No UI yet; attachments remain #243.
- **Per-enquiry form attribution and the `lead_form_submitted` event (#220).** Each finalised
  submission (received, or confirmed through double opt-in) stores its own `utm_source`,
  `utm_medium`, `utm_campaign`, `utm_term`, `utm_content` and `referrer_host` on its
  `form_submissions` row, taken from a submitted field of that name or else from the `page_url`
  query, normalised and clipped; `referrer_host` keeps the page's host only. Spam and pending rows
  stay empty, and `leads.source` keeps the form that created the lead. When such a submission of a
  `create_lead` form is linked to a lead, a `lead_form_submitted` event with `form_id` and
  `submission_id` follows the `lead_created` or `lead_updated` in the same transaction. Lead rules
  can trigger on it, narrow it to one form with `trigger.form_id`, and branch on the six
  attribution fields of the triggering submission (empty in a dry run); anywhere else those fields
  and `form_id` are refused with `not_applicable`. Lead erasure empties the attribution of every
  linked submission. `GET /forms/submissions/{id}` and `GET /forms/{id}/submissions` return the six
  fields. MySQL installs run `20261107120000_add_form_attribution`; SQLite gets the columns through
  AutoMigrate. Existing submissions and events are not backfilled, and the embed script does not
  fill hidden `utm_*` fields.
- **Form mail delivery records and manual retry (#216).** Every form mail (double-opt-in
  confirmation, follow-up, one notification per `notify_emails` address) gets a
  `form_mail_deliveries` row in the submission or confirmation transaction, stamped `sent`,
  `failed` (with `mail_failed`, `timeout` or `invalid_recipient`), `log_only` (no `SMTP_HOST`) or
  `skipped` after the send. Failures used to be logged and swallowed, and the log-only fallback
  looked like success. The rows appear as `deliveries` on `GET /forms/submissions/{id}` and
  `GET /forms/{id}/submissions` and in the new attention list `GET /forms/deliveries`; admin and
  sales retry a failed, log-only or stuck row with `POST /forms/deliveries/{id}/retry`. A
  confirmation retry re-arms the submission's single token with a new 48-hour link. The rows hold
  no address or content and are removed with purged and erased submissions. MySQL installs run
  `20261106120000_add_form_mail_deliveries`; SQLite gets the table through AutoMigrate. The public
  submit and confirm answers are unchanged, and nothing retries automatically.

- **Retention purge for spam and unconfirmed form submissions (#217).** With the new
  `FORM_RETENTION_DAYS` set, spam and never-confirmed submissions with no linked lead are permanently
  removed with their confirmation tokens once they pass that age. It is off by default. 1 and 2 are
  raised to a 3-day floor, and anything above 36500 is lowered to it. The purge runs at boot and then
  every 24 hours, so **the first boot with the setting removes the whole existing backlog**: take a
  backup first if you may want those rows. A pending row that still holds a spendable token stays
  until the token is spent or expires, and received, confirmed and lead-linked rows are never touched.
  The drain is now automation, then the purge, then AEO. Both form-delete dialogs still say "Collected
  submissions are retained" until a separate frontend task merges, which is wrong for purged rows.
  No schema change.
- **Tasks can link a deal (#240).** A task gains a nullable `deal_id`, additive to the
  lead-or-customer link: migration `20261101120000_add_task_deal_link` on MySQL/MariaDB, AutoMigrate
  on SQLite (the first start rebuilds `tasks`). `POST /tasks` and `PUT /tasks/{id}` accept `deal_id`
  when it names a live deal the caller may see, closed deals included: admin any, sales its own, and
  a hidden deal is 404 like a missing one. On update an absent `deal_id` keeps the link, 0 clears it,
  and a clear of a link hidden from the caller answers 200 and keeps it. Every task the API returns
  carries `deal_id` and a `deal` summary of id, title and stage only when the caller may see that
  deal, decided on every read. `GET /deals/{id}/tasks` lists a deal's live tasks: every task for an
  admin, only the tasks assigned to the caller for the sales owner. A `create_task` step of a deal
  rule records the deal beside its customer-else-lead link, in the run step and in the dry run. Tasks
  created by earlier runs are not backfilled.
- **Lead owner validation and `POST /leads/{id}/assign` (#255, #185).** `POST /leads`,
  `PUT /leads/{id}` and the new assign route refuse an owner that is unknown, deactivated, erased or
  neither an admin nor a sales user with 400 `INVALID_REFERENCE` (`details.field` names the body
  field, one message per route), writing nothing and recording no event; an unknown id used to fail
  on the foreign key as a 500 and an erased user's id was accepted. An update that leaves the owner
  alone, or re-sends it, is not checked, so a lead whose owner has left stays editable; an assignment
  always checks the user it names. The assign route takes the `{user_id}` body the frontend client
  already sends and follows the lead visibility policy on the locked row: an admin on any lead, a
  sales user on its own, which it loses at once when it hands the lead over. The check is a locking
  read of the user, so a deactivation committed earlier is seen on MySQL too. `NewLeadService` now
  takes the user repository; a service built without it refuses these writes
  (`ErrLeadOwnerCheckUnavailable`) rather than skipping the check. Public form default owners, the
  automation `assign_owner` step and leads whose owner already left are unchanged.

- **Lead lookup by external id.** `GET /leads?external_id=` returns the live leads whose
  `external_id` equals the value, in the usual list shape (ascending id, owner loaded, `total` is
  their number), so integrations can correlate their records with CRM leads. It takes precedence
  over `search`, `classification`, the sort and paging. Visibility follows the lead policy (admin
  any lead, sales its own) row by row: a lead the caller may not see is left out, never 403 or
  404, so an unknown id and a hidden one read the same. At most the first 100 matching leads are
  read. A value longer than the 255-character column is 400 `VALIDATION_ERROR`.

- **Complete API reference and its CI gate.** Every operation of the Swagger 2.0 spec in `api/`
  now names the roles it admits, its parameters with defaults and clamping, the `error.code`
  values it can return and their conditions, and its side effects; every schema property is
  described, with examples, enums for the closed sets and the limits the code enforces. The
  general description covers authentication, the response envelope, the error catalogue,
  pagination, rate limits (and the absence of rate-limit headers), CORS and `X-Request-ID`, and
  every tag is described. The four public form pages that return JavaScript or HTML
  (`GET /forms/public/embed.js`, `GET /forms/public/{key}/view`, `GET` and `POST
  /forms/public/confirm`) are documented under a new `public-forms` tag, the quote responses now
  show the fields their JSON really carries (`number` and the customer, deal and owner
  summaries), and list payloads are typed. `make swagger` also renders `api/index.html`, a static
  offline HTML reference. `make verify-api-docs` (in `make verify`, CI and every validation
  profile) fails when `api/` differs from a fresh generation, a route has no operation or an
  operation no route, an operation's security disagrees with the route's authentication (exactly
  the two schemes as alternatives behind it), a 401, 403 or 429 the route middleware can return is
  undocumented, a query parameter, form field or body the registered handler reads is undocumented
  (or a documented one is never read, or the body's schema or required flag differs from what the
  handler binds), the base path or a security scheme's header form is wrong, a description, tag or
  success response is missing, or a closed set's enum drifts from its Go constants.

- **Follow-up automation: rule names on runs, field-level rule errors, dry run of unsaved edits.**
  Run responses (`GET /automation/runs`, `GET /automation/runs/{id}`, the cancel answer) carry
  `rule_name` — the rule's current name, also for a deleted rule, flagged `rule_deleted` — and
  never the rule's trigger or steps (the `rule` association is no longer serialised at all); the
  run list, run detail, the lead and deal Automation sections and the run list's rule filter show
  the name. Sales users therefore see the names of the rules that ran on their records, also after
  a rule is deleted; the rule form says so under the name. A refused rule body lists every refused
  field in `error.details.fields` as `{field, code, message}` (`steps[2].config.title`, steps
  counted from zero) while `error.message` and `error.code` stay those of the first refusal; the
  editor puts each message under its field, following steps moved while the save was in flight and
  dropping errors of fields edited or steps removed meanwhile. `POST /automation/rules/dry-run`
  (admin) simulates an unsaved rule body after the same validation as a create, saving nothing and
  starting no transaction; the editor's Dry run button now simulates the form as it is, on a new
  rule too, while the rule list's dry run keeps simulating the saved version.
- **Follow-up automation and notifications (UI).** The screens for the automation API:
  `/automation/rules` (admin) lists rules with an enable switch (disabling asks first, because it
  cancels the rule's active runs), Edit, Dry run and delete tools; `/automation/rules/new` and
  `/automation/rules/{id}/edit` build the trigger and up to 20 steps with a sub-form per step
  type, forward-only jumps, placeholder help, status and stage condition values as free text with
  the known values suggested (kept verbatim, as the API compares plain strings), the API's limits
  checked client-side, and an edit
  asks for confirmation before it creates a new version; the dry-run dialog simulates the saved
  rule on a chosen lead or deal and shows each step's outcome, simulated time and details.
  `/automation/runs` (admin and sales; sales sees runs on records it owns) filters by status,
  rule, entity and record, and `/automation/runs/{id}` shows the step log with an admin-only
  Cancel run. Lead and deal detail pages gain an Automation section with the record's runs. Staff
  users get a notification bell in the app bar (unread count polled every 60 s and on window
  focus, the latest ten in its menu, mark read, mark all read) and a `/notifications` page with an
  unread filter; customers see neither. Failed loads show the server message with a retry. The e2e
  spec `automation-rules.spec.ts` builds a rule in
  the form and dry-runs it; eight captures are in `docs/screenshots/automation/`.

- **Products and quotes (UI).** The frontend slice of the quotes feature: `/products` (admin and
  sales; name, SKU, price with currency, active chip, search, active filter, server pagination and
  sorting) with an admin-only form at `/products/new` and `/products/{id}/edit` (name ≤ 200,
  SKU ≤ 64, price in major units sent as `unit_price_cents`, currency defaulting to
  `deals.default_currency`, active switch); `/quotes` (number, title, status, customer, deal,
  total, valid until; status filter, server pagination) with a create dialog (title, deal and
  customer pickers, currency, valid until, notes); `/quotes/{id}` with the lines table, add/edit/
  delete of lines and an edit dialog while the quote is a draft and no edit control otherwise,
  the server's subtotal, discount and total, status buttons for the valid transitions only (the
  server message on `INVALID_STATUS_TRANSITION` and `CONFLICT`), a Print link, an "Email quote"
  confirmation on sent and accepted quotes that names the customer and distinguishes a 409 from a
  502, and the status history; `/quotes/{id}/print`, a print-styled route whose app chrome is
  hidden under `@media print` and whose Print button calls `window.print()`; a "Quotes" section on
  the deal detail page with "New quote" prefilled with the deal and hidden on won or lost deals;
  Products and Quotes navigation entries and routes for admin and sales only, the product form
  routes for admin only. Discounts are typed as percentages and sent as basis points; totals are
  never recomputed in the browser. New endpoint modules `src/api/endpoints/products.ts` and
  `quotes.ts`; Vitest coverage per screen and the Playwright spec
  `gocrm-ui/e2e/tests/admin-quotes.spec.ts` (product → deal → quote → line → sent → print →
  accepted, with print-media emulation). No new dependency. (#167)

- **Products and quotes screenshots.** Ten documentation captures under
  `docs/screenshots/quotes/` (product list and form, quote list, the create dialog from a deal,
  the draft detail with two lines, the add-line dialog, the sent detail with its status actions,
  the print page on screen and under print media, and the deal's Quotes section with an accepted
  quote), produced by the new screenshot spec `gocrm-ui/e2e/screenshots/11-quotes.spec.ts` and
  indexed in `docs/SCREENSHOTS.md`. The capture review also fixed the "Active" filter on
  `/products` and the "Status" filter on `/quotes`, whose floating label overlapped the empty
  "All products" / "All statuses" value: the label is now shrunk and the outline notched.

- **Follow-up automation and notifications (API).** Admin-defined rules (`/automation/rules`)
  start on lead events (`lead_created`, `lead_updated`, `lead_status_changed`), deal events
  (`deal_created`, `deal_updated`, `deal_stage_changed`, won and lost included) or a daily UTC
  schedule, and run ordered steps: `wait`, `condition` (allowlisted fields and operators, `else`
  stop or forward jump, `amount_cents` only in the deal's currency), `create_task`, `assign_owner`,
  `send_email` to a staff user and `notify` (in-app). Every lead and deal write path — the lead
  service, form submissions, bulk status/assign/convert, conversion, and deal create/update/stage —
  records an outbox event inside the transaction that commits the write; automation-originated
  writes record `origin=automation` events that start no run. A worker (`AUTOMATION_ENABLED`,
  `AUTOMATION_POLL_INTERVAL_SECONDS`, see `.env.example`) claims events with a conditional update,
  starts one run per enabled matching rule (`clause.OnConflict{DoNothing: true}` on the dedupe
  key, so a duplicate is neither an error nor a row on SQLite or MySQL), executes each step in one
  transaction behind a locking read of the record and a conditional advance of the run row, parks
  waits in `resume_at` so they survive a restart, continues schedule passes from a persisted
  cursor (500 records per tick, one pass per UTC day, same-day catch-up, no backfill) and
  reconciles interrupted runs at boot (a `send_email` left in `sending` is failed with
  `email_unconfirmed` and never resent); a process started with `AUTOMATION_ENABLED=false`
  neither executes nor reconciles runs, so it leaves the worker replica's runs alone. The email
  step is two transactions around one `Mailer.Send`. A `create_task` title or description accepts
  only the placeholders `lead.id`, `lead.status`, `lead.source` and the `deal.*` fields because
  a task survives the erasure of its lead (`deal.title` is staff-typed and may name the client; a
  task rendered from it before an erasure keeps that text, see #245); the name, email, phone, company and
  owner placeholders stay available to the email and notification text, which does not outlive
  the person. Rule updates cancel the rule's active runs with `rule_changed` (disable:
  `rule_disabled`, delete: `rule_deleted`); `POST /automation/runs/{id}/cancel` is `manual`. A dry
  run (`POST /automation/rules/{id}/dry-run`) reports each step's would-be outcome and writes,
  sends and notifies nothing. The run history (`/automation/runs`) follows lead and deal
  visibility inside the query; `/notifications` serves a staff user's own notifications. Lead
  erasure cancels the lead's active runs (`record_deleted`) and hard-deletes its notifications,
  before the scrub; user erasure hard-deletes the user's notifications. Logs carry ids, statuses,
  step types, outcomes and error codes only. Schema: AutoMigrate plus the MySQL/MariaDB migration
  `20261028120000_add_follow_up_automation` (six new tables, nothing altered). The worker assumes a
  single API replica and no retention pruning exists yet. The screens are described in the
  "Follow-up automation and notifications (UI)" entry above. (#164)

- **Products and quotes (API).** A product catalogue (`/products`: name, SKU, unit price in
  integer minor units with an ISO 4217 currency, active flag; admin writes, sales reads) and
  quotes linked to a deal and/or a customer (`/quotes`, `/deals/{id}/quotes`,
  `/customers/{id}/quotes`; admin and sales, sales narrowed to its own quotes inside the query).
  Lines copy the product's name and price when they are added, take a per-line discount in basis
  points, and the server recomputes the totals with round-half-up on every line change; the line
  gross and the quote total are capped at 10^12 minor units and a quote holds at most 100 lines.
  The lifecycle is draft → sent → accepted/rejected/expired (sent → draft to revise): each
  transition is a conditional claim plus one `quote_status_changes` row in a single transaction,
  draft → sent reads the line count after the claim and refuses an empty quote, and acceptance is
  refused after `valid_until`. Only drafts are editable. `POST /quotes/{id}/email` sends a
  plaintext rendering of a sent or accepted quote to the linked customer's current address with a
  fixed subject and stamps `last_emailed_at` by a conditional update that a revision during the
  send defeats (409); a mailer failure is 502 `MAIL_DELIVERY_FAILED`. Without `SMTP_HOST` the
  quote email takes the same logging fallback as the password reset: it is logged, not delivered,
  and the call still answers 200 and stamps `last_emailed_at`. The SMTP mailer refuses a
  recipient or subject containing CR or LF (`mailer.ErrInvalidHeader`), and its failure log keeps
  the code of a relay reply without the text, which can echo the recipient. Customer erasure keeps
  the quotes, and since #245 redacts their title, notes and line descriptions; the PII sweep
  covers the four new tables. Schema:
  AutoMigrate plus the MySQL/MariaDB migration `20261026120000_add_products_and_quotes`. The
  frontend (products and quotes pages, print page, DealDetail section) is a separate follow-up.
  (#158)

- **Feature analysis workflow (runner 0.9.0).** `automation/langflow/flows/analysis.json` and
  `gophercrm-runner analyze` turn one feature request into a gitignored
  `docs/specs/<date>-<slug>.md`: a one-page summary, then acceptance criteria in the task
  manifest shape, the data model with both migration halves, API, screens, permissions, Mermaid
  diagrams, test plan, documentation to update, cited research notes, an independent review with
  one bounded revision and the open questions. Agents are read-only and restricted to a detached
  worktree at `origin/main`; the runner writes the file, records provider-reported cost and
  duration per call, and verifies a digest chain between stages. Refinements re-enter with
  numbered owner comments and inherit the parent run's journals. See the
  [runner guide](automation/langflow/README.md#analyse-a-feature-request).

- **Frontend task profile with named Vitest evidence (runner).** A task manifest may now set
  `validation_profile: "frontend"`. Its acceptance checks are registry entries of the new
  optional `kind: "vitest"` (`go` remains the default, so existing entries and frozen contract
  hashes are unchanged) whose argument array is exactly `npm --prefix gocrm-ui test --silent --
  --run --reporter=json` followed by test files under `gocrm-ui/src`; each named test is a file
  and the exact `fullName` the JSON reporter prints, and must report `passed` exactly once.
  Failed, skipped, pending or todo cases, a required file without results, and duplicates fail
  acceptance. A frontend ticket changes only `gocrm-ui/`, `docs/`, `README.md` and
  `CHANGELOG.md`, cannot mix Go and Vitest checks, keeps `make verify` (build, zero-warning lint,
  unit tests) and requires the selected browser checks on SQLite and MySQL; freezing it needs
  `database_test_commands` and `TEST_MYSQL_DSN`. For every profile, the Vitest and Playwright
  configuration, the Vitest setup files and the whole `gocrm-ui/e2e/` suite are now protected
  validation gates that no agent may change; only a newly added `gocrm-ui/e2e/tests/*.spec.ts`
  is admitted. See the [runner guide](automation/langflow/README.md#execute-the-queue).
- `gophercrm-runner retire-issue` retires an unfinished ticket execution whose GitHub issue was
  closed outside the runner, recording the closure and its closing pull requests as unvalidated
  by that execution, releasing its admission slot and stopping the queue without modifying GitHub.
- **Maintainer-commit adoption for ticket executions (runner).** `gophercrm-runner adopt-commit
  --issue N --head FULL_SHA --reason ...` continues an unfinished ticket whose repair budget is
  exhausted, or whose candidate must be corrected by hand, with an inspected maintainer commit on
  the ticket branch: the counterpart of `complete-pr --refresh --head`. The worktree must be
  clean at that exact commit, the commit must descend from the recorded head, its diff from the
  journal base must pass the protected-path and frozen-scope gates, and a recorded pull request
  must already carry it. Adoption records an `adoptions` entry, invalidates tests and review as
  `freeze --refresh` does and returns the execution to `validate`; it consumes no repair round
  and leaves spent calls and the deadline unchanged. See the
  [operations runbook](automation/langflow/OPERATIONS.md#adopt-a-maintainer-commit).

- A shared bounded mailer validation profile: API build/vet, SMTP transport/header/privacy
  regressions, focused password-reset/form/quote/automation delivery callers and browser health smoke.
  Required tests cannot be absent or skipped; mixed application changes and explicit
  full/release runs retain complete coverage.

- **AEO runtime ownership.** The daily scheduler now starts whenever `AEO_SCHEDULE_ENABLED` is
  on, even when no provider key exists at boot: engines are resolved from the environment plus
  the administrator's stored keys when a tick is due, so a key saved in Settings is used by the
  next scheduled run without a restart. A tick that finds the module unconfigured is
  logged once at info level and the loop sleeps until the next day. At shutdown the process
  closes run admission (a run requested while draining answers 503 `SHUTTING_DOWN`), cancels
  the scheduler and every manual, scheduled or single-prompt run in flight — cancellation
  reaches provider calls and repository writes — joins them, and only then closes the database.
  When cleanup can persist it, an interrupted run is finalized as `failed` (or `partial` when answers were already recorded)
  with `completed_at` set; queries cut short are counted as failed and not recorded as answers.
  The drain waits at most 15 seconds; if a provider or database write still has not released by
  then, the process exits without closing the database and the next boot's reconciliation fails
  the row, as after a crash. (#111)

- Shared Go validation isolates application database variables while retaining the disposable
  `TEST_MYSQL_DSN`, so configuration defaults are tested consistently locally and in MariaDB CI.
  Runner policies can explicitly allow zero repair rounds for a continuation whose repair
  allowance is exhausted; validation, independent review and CI remain required.

- A conservative committed proof for comment-only `.env.example` edits, allowing required
  configuration guidance to accompany bounded validation without relaxing active-setting,
  mixed-scope or explicit full/release gates.

- A shared bounded AEO runtime validation profile with committed startup/interface scope
  proofs, required lifecycle roots, focused Go checks and health/provider browsers. SQL,
  MariaDB migration coverage and explicit full/release gates remain mandatory.
- Opt-in candidate validation inspection through `confirm-validation`; changed candidates
  need new inspection, and stopped executions retain their spent budgets.

- Runner recovery and Markdown handoff reports, batch-wide audited budget extensions,
  worker heartbeats, installed-module verification and validation-cost previews. Heavy commands
  share a machine lock, record bounded resource observations and enforce wall-clock deadlines
  after sleep. Explicit retention preview/apply reclaims only inactive merged validation
  dependencies while preserving source and evidence.

- A shared transaction validation profile with SQLite/MySQL boundaries, affected-caller race
  regressions and health smoke; CI adds MariaDB boundaries. Partial full-suite attempts remain
  evidence of interrupted runs, and explicit full/release validation includes the SQL boundaries.
  Browser port conflicts stop for environment recovery without launching a code repair.

- Dedicated PR migration checks on disposable MySQL 8 and MariaDB 10.11 services,
  including populated upgrades, rollback/reapply and SQLite upgrade fixtures. Required
  tests must execute without skips; CI retains their results and tested revision. Both engines
  feed the existing required Backend check. Workflow/checker-only changes use shared targeted
  migration validation and health smoke.
- A targeted auth validation profile with auth/session race regressions and application
  health, login and registration checks. Explicit full/release validation remains complete.
- Explicit documentation impact and review decisions in new Langflow runner configurations,
  with candidate-bound evidence and verification after merge. CI publishes impact reports;
  documentation updates remain in the reviewed PR.
- Bounded prepared backend batches and a runner-only validation profile using relevant workflow
  tests and application health smoke. Releases retain the full validation matrix.

- Contextual browser validation for PRs and the local Langflow runner: health smoke plus
  affected feature specs, with full fallback for shared or unknown changes. Selected core checks,
  named issue regressions, independent review and exact-commit merge checks remain required.
- A manual full-validation Release workflow with rehearsal mode, version/changelog preparation,
  verified artifacts, annotated tags and draft-first publication. Production deploys now consume
  published release artifacts after checking their full-validation run and commit identity.

- **`SERVER_HOST`.** Optional listen interface for the API server. Empty, the default, keeps
  the previous behaviour of binding every interface; `127.0.0.1` restricts the server to the
  local machine behind a reverse proxy. The startup log now names the full listen address.
- **Deal pipeline aggregates.** `GET /deals/pipeline` (admin and sales; sales always sees its own
  deals, admin may filter by `owner_id` and `company_id`) returns all five stages in pipeline
  order with each stage's deal count and totals per currency: the amount and the weighted amount,
  round half up of Σ `amount_cents × probability` / 100, rounded once per stage and currency.
  `GET /dashboard/pipeline` returns the same stages for the dashboard plus `won_this_month`, the
  deals won in the current calendar month in UTC, counted and summed per currency (admin all,
  sales own; support and customer 403). Aggregation is integer arithmetic grouped in SQL with no
  date functions, the same on MySQL, MariaDB and SQLite; no schema change.
- **Deal amount bound.** `amount_cents` on `POST`/`PUT /deals` is now capped at 1000000000000
  (10,000,000,000.00 in major units) so the pipeline sums keep ample headroom; larger values are
  refused with a 400 naming the bound.
- **Deals with stage history.** A sales opportunity with a fixed pipeline and an append-only
  record of every stage it has been through.
  - `/deals` (admin and sales; sales sees and edits its own, delete is admin only): list with
    search over title and notes, server-side filters (`stage`, `open=true`, `company_id`,
    `customer_id`, `owner_id`), a sort allowlist and pagination; get with owner, company,
    customer and lead; create and update with validation of the links (400
    `INVALID_REFERENCE`; on update, links are checked only when the request sets them, so a
    deal keeps links to erased records and stays editable); `POST /deals/{id}/stage` and
    `GET /deals/{id}/history`;
    `GET /companies/{id}/deals` and `GET /customers/{id}/deals`.
  - Stages `qualification`, `proposal`, `negotiation`, `won`, `lost` with default probabilities
    10/40/70/100/0; `won` is always 100 and `lost` always 0; `closed_at` is set on entering a
    closed stage and cleared on leaving it; `lost_reason` is kept only while lost. Every stage
    change, the create included, writes one history row in the same transaction; asking for the
    current stage again writes nothing.
  - Money is an integer amount in minor units (`amount_cents`) next to a three-letter currency
    code; the default currency is the new `deals.default_currency` setting (category `deals`,
    shipped as `EUR`, admin-editable, and exposed on `GET /configurations/ui`).
    `expected_close_date` is a plain `YYYY-MM-DD` date on the wire.
  - Companies report `deal_count`, and deleting a company also clears `company_id` on its deals.
  - Erasing a linked customer, lead or user keeps the deals and their history; since #245 erasing
    the customer or lead (or a customer-role login reaching them) redacts their title, notes and
    lost reason. Erasing a staff owner does not.
  - Migration `20260930120000_add_deals` for MySQL and MariaDB (two new tables, nothing altered);
    auto-migration covers SQLite, populated files included.
- **Companies.** A curated organisation record next to the free-text company field of leads and
  customers.
  - `/companies`: list with search (name, domain, industry, city), a sort allowlist and
    pagination; get with the owner and live customer and lead counts; create and update with the
    domain normalised (lower case, no scheme, path or `www.`) and unique among live companies
    (409); admin-only delete that clears the links from leads and customers in the same
    transaction. Reading is open to admin, sales and support; writing to admin and sales.
  - `GET /companies/{id}/customers` and `GET /companies/{id}/leads` page through the linked
    records; sales sees only the leads it owns.
  - Leads and customers accept an optional `company_id` on create and update (unknown → 400
    `INVALID_REFERENCE`; on update `0` clears, absent keeps) and return `company_record` on their
    detail endpoints. The free-text `company` column and the public forms are unchanged.
  - Erasure is unchanged: `company_id` is a business link and stays on the erased row; companies
    hold no personal data.
  - Migration `20260929120000_add_companies` for MySQL and MariaDB; auto-migration covers SQLite.
- `LICENSE`: GopherCRM is released under the MIT License. The README no longer describes the
  licensing as unresolved.
- **E2E on SQLite in CI.** The e2e suite now also runs against SQLite on every pull request
  (job "E2E (Playwright on SQLite)", no database service). `make e2e E2E_DB_DRIVER=sqlite` runs it
  locally on a new SQLite file and leaves the `gocrm_e2e` MySQL database alone; the default
  (`mysql`) is unchanged.

### Changed

- **Identity package boundary (#641).** The new `internal/idkit` package will hold the sign-in,
  role mapping and SCIM code. `TestIDKitImportBoundary` in `internal/idkit/boundary_test.go` fails
  when any non-test package under `internal/idkit` reaches gin, gorm, `internal/models`,
  `internal/repository`, `internal/utils` (the global `utils.Logger`) or logrus, directly or
  through other packages. Behaviour is unchanged.
- **SCIM protocol package moved (#642).** The SCIM filter and patch parsers, schemas, discovery
  documents, list responses and error bodies moved to `internal/idkit/scim` and no longer depend
  on gin or the CRM models: the error writers take an `http.ResponseWriter` and the User resource
  is built from `scim.UserData`, which the handlers fill from the CRM user. Behaviour is
  unchanged.
- **Role mapping package (#645).** The first-match rule that maps the `groups` and `roles` claims
  to a role moved to `internal/idkit/rolemap` (`rolemap.Rule`, `rolemap.Match`,
  `rolemap.UsesGroups`). The OIDC sign-in callback, the SCIM Users writes and the SCIM group patch
  all call `rolemap.Match`. The stored mapping format, the settings API and behaviour are
  unchanged.
- **OpenID Connect client package (#643).** The generic OpenID Connect client helpers moved from
  `internal/service/auth_oidc.go` to `internal/idkit/oidc`: the issuer HTTP client
  (`oidc.NewHTTPClient`), the cached discovered provider (`oidc.ProviderCache`), the state, nonce
  and PKCE values (`oidc.NewAuthRequest`, `oidc.AuthCodeURL`), the client secret or certificate
  (`private_key_jwt`) code exchange (`oidc.Client`, `oidc.ClientAssertion`), the claim parsing
  and subject and name rules (`oidc.Claims`, `oidc.ValidSubject`, `oidc.ProfileNames`) and the
  sign-in error types (`oidc.RefusalError`, `oidc.UnavailableError`). The OIDC service, the SCIM
  Users writes and the sign-in audit call the new package; the userinfo group resolution, the
  attempt storage and the sign-in flow stay in `internal/service` (the Entra ID rules followed in
  #644). Answers, audit rows and refusal codes are unchanged.
- **Entra ID package (#644).** The Microsoft Entra ID helpers moved from
  `internal/service/auth_oidc.go` and `internal/service/sso_settings.go` to `internal/idkit/entra`:
  the issuer and tenant checks (`entra.IsIssuer`, `entra.Tenant`), the tenant/object-id link hint
  for SCIM-created accounts (`entra.LinkHint`), and the Microsoft Graph group lookup with its
  paging, origin, page-size, page-count and deadline bounds (`entra.GroupResolver`,
  `entra.EffectiveMaxPages`). The strict string-array decoder that the Graph and userinfo group
  reads share is now `oidc.StrictStringArray`. Answers, audit rows and refusal codes are
  unchanged.
- **Identity host interfaces (#646).** `internal/idkit/host.go` declares the interfaces the
  identity layer needs from its host: `idkit.IdentityStore`, `idkit.AttemptStore`,
  `idkit.SessionIssuer`, `idkit.AuditSink`, `idkit.ConfigSource`, `idkit.TxRunner`, `idkit.Clock`
  and `idkit.Logger`, with the neutral types and errors they use. Each one states whether it joins
  the caller's transaction (identities, sessions) or runs only outside one (sign-in attempts,
  audit, configuration). `internal/service/auth_oidc_host.go` holds GopherCRM's implementations
  over the existing repositories and services, with contract tests. Nothing uses them yet.
  Behaviour is unchanged.
- **Identity test kits (#647).** `internal/idkit/oidctest` is an in-process OpenID Connect issuer for
  tests (discovery, JWKS with key rotation, the code exchange with PKCE S256 and client secret or
  private_key_jwt authentication, userinfo) with no provider-specific code, and
  `internal/idkit/scim/scimtest` is a SCIM test client that sends the bearer token and the SCIM
  media type and decodes SCIM error bodies. The single sign-on service tests and the SCIM
  provisioning-cycle golden test now use them; only tests import either kit. Behaviour is
  unchanged and both golden files are byte-identical.
- **Single sign-on in the identity package (#648).** The OpenID Connect sign-in moved from
  `internal/service/auth_oidc.go` into the new package `internal/idkit/sso`, which runs the start
  and the callback over the host interfaces only (`sso.Service`): the attempt claim, the code
  exchange, the token and claim checks, the Graph and userinfo group reads, the role mapping, one
  transaction, the audit event and the log lines. The account link rule moved with it: the exact
  (issuer, subject) pair, the Entra ID oid and tid link only to an identity SCIM created under the
  same issuer, and email never links. `internal/idkit/host.go` adds `idkit.AccountStore` (sign in
  an existing account with the mapped role, provision a new one) and `idkit.Profile`; GopherCRM
  implements it with the governed role write and the provisioning in
  `internal/service/auth_oidc_host.go`, and `NewOIDCService` now wires the #646 adapters into the
  sign-in. The unusable password of a first sign-in is still hashed before the transaction
  starts. The sign-in audit check also scans `internal/idkit/sso`. Answers, audit rows, role
  audit rows, refusal codes, log lines and the lock order are unchanged, and the sign-in golden
  file is byte-identical.
- **SCIM server in the identity package (#649).** The SCIM 2.0 HTTP layer moved from the gin
  handlers into the new `net/http` package `internal/idkit/scim/server`: `server.New` builds a
  `Server` over the host's `server.Users`, `server.Groups` and `server.Logger`, `server.Routes`
  lists its 14 routes under `/scim/v2`, and the package owns the error sentinels
  (`ErrNotFound`, `ErrInvalidValue`, `ErrUniqueness`, `ErrLastAdmin`) and their SCIM answers, the
  lenient body reading (any Content-Type, one JSON object of at most 64 KiB, unknown attributes
  ignored) and the managed-accounts rule (`server.Manages`, `server.ExternalID`), which the SCIM
  user service now calls. GopherCRM implements the interfaces in `internal/handler/scim_server.go`
  (`NewSCIMServer`) and `SetupSCIMRoutes` mounts the server with `gin.WrapH` on the same routes
  behind the unchanged SCIM token authentication. Answers, audit rows and log lines are
  unchanged, and the provisioning-cycle golden file is byte-identical.
- **Identity package example and README (#650).** `internal/idkit/example` is a runnable host
  of the identity package: in-memory implementations of every host interface and of the SCIM
  server's `Users` and `Groups`, and a small `net/http` app that serves the OpenID Connect start
  and callback and the SCIM server behind a bearer token. Its tests drive it end to end against
  the `oidctest` issuer, shaped as a generic OpenID Connect provider with a groups claim, through
  the `scimtest` client (`go test ./internal/idkit/example`). `internal/idkit/README.md`
  describes the packages, the host interfaces, the sign-in and SCIM rules, issuer support, the
  test kits, the limits and the licences, and the package comments under `internal/idkit` now
  name the host application generically and carry no issue numbers. GopherCRM behaviour is
  unchanged.
- **Lead route descriptions name the route guard (#698).** The `POST /leads` and
  `GET /leads/export` descriptions in the OpenAPI spec now name the route guard built from that
  route's `permissions.RouteKeys` entry instead of the `/leads` group guard that #680 removed.
  Behaviour is unchanged.
- **Services decide from the caller's permission scope (#681).** A service call governed by a
  permission key now receives the caller's id and the scope (`all`, `own` or not held) of that one
  key, and `CanViewLead`, `CanViewDeal`, the automation run history, the lead CSV export, the deal
  and dashboard pipelines, lead erasure, `GET /deals/{id}/tasks` and the three bulk status
  endpoints decide from it. The adapter that turned a permission set into a stand-in system role
  (`permissions.RoleForCall`) is removed. Every system role and custom role keeps the access it had;
  the lead export service log names the `scope` instead of the `role`. See "Service-side scope" in
  `docs/AUTHORIZATION.md`.
- **Protected routes register through `handler.Protected` (#680).** The helper in
  `internal/handler/protected_routes.go` looks every protected route up in
  `permissions.RouteKeys` or `permissions.SelfServiceRoutes` and builds its guard from that entry;
  a route with neither entry panics at registration, naming the route. The route files and the
  protected `/auth` routes in `cmd/main.go` no longer name a permission constant or a permission
  middleware, and no protected route is registered on the raw gin group. `RouteKey` gains `AllOf`
  (the two company sub-lists also require `companies.view`) and `CheckedInHandler` (the routes
  whose handler decides in its first statement). The outer group guards built from
  `permissions.GroupKeys`, which is removed, are gone: every route under them carries its own
  guard or handler check from the same group. Every route answers every role with the same status
  and error code as before, and the OpenAPI spec is unchanged.

- **The outbound-request guard, the webhook runtime and the background lifecycle have their own
  packages (#617).** The dial-time checks, the redirect refusal and the public-only client move
  from `internal/aeo` to `internal/egress`, and the AEO provider guard builds on them. The webhook
  delivery runtime, signing and endpoint service move from `internal/service` to
  `internal/webhook`, which no longer depends on `internal/aeo` or `internal/service`. The webhook
  sender, the stale bulk-operation sweep, the form retention purge, the automation worker and AEO
  run ownership share one start/stop and admission type in `internal/lifecycle` (`Loop`,
  `Owner`). Three small lifecycle differences: the automation worker's stop timeout error now
  wraps the context error (same text), a stopped automation worker is no longer armed again by a
  later `Start`, and a repeated AEO shutdown after the drain always returns nil. Behaviour, log
  lines and error texts are otherwise unchanged.

- **Lead assignment, lead conversion and the automation assign-owner step send updated webhook
  events (#616).** `POST /leads/{id}/assign` now writes `lead.updated` when the owner changed
  (assigning the current owner again sends nothing), `POST /leads/{id}/convert` writes
  `lead.updated` next to `customer.created`, and the automation `assign_owner` step writes
  `lead.updated` on a lead run and `ticket.updated` on a ticket run when it changed the owner or
  assignee, each in the transaction of the write. Before, these paths sent no updated event. Each
  of the lead, customer and ticket models now defines the state its updated event compares
  (`WebhookState()`), and a test keeps that state in step with the columns the update writes;
  the lead and ticket states now also compare `customer_id`, which their updates write. Customer
  assignment, ticket comments, a repeat form enquiry and a company delete still send no updated
  event.

- **Leads and tasks list through one query path each (#618).** `GET /leads`, `GET /leads/export`
  and `GET /tasks` build one filter, and the repository answers it with one shared predicate for
  the rows and the total; the parameters a caller may use are always combined. For admins that
  means a lead search combines with the classification and the sort (before, the search dropped
  the classification and a classification dropped the sort), and a task `label_id` combines with
  `search` (before, `label_id` dropped the search). The lead export follows the list: an admin's
  search, classification and sort apply together. Sales users still get their own leads in id
  order with search, classification and sort ignored, and non-admin task callers still get their
  own tasks without search, sorted only together with `label_id`. The lead `owner` and the task
  `assigned_to` are now loaded on every `GET /leads` and `GET /tasks` path; `GET /tasks/my` is
  unchanged. A lead or task search term is matched literally: `%`, `_`, `!` and `\` match only
  themselves, on SQLite, MySQL and MariaDB (`LIKE ... ESCAPE '!'`). The duplicate list, sort,
  search, owner, classification and label repository and service methods are removed and their
  tests run against the shared path.

- **End-to-end tests assert the outcome their titles name (#622).** The customer, user,
  registration, lead search and sorting, and cross-entity browser tests wait on the API response
  and the rendered rows instead of fixed delays, and assert the result: every create answers 201
  and the record is found by a value unique to it; duplicate customer and user emails answer 409
  and the snackbar shows "customer with this email already exists" / "user with this email already
  exists"; an empty form is stopped by the browser's own check and, with that check switched off,
  shows the exact field messages and sends no request; the registration loading state and
  network-error message are checked. The leads, customers and dashboard page objects address
  elements by role, label or test id rather than styling classes or parent walks, and the app bar's
  account button has the accessible name "Account menu". Three tests are renamed to say what they
  check: "admin can create several records of each entity", "admin can create a user for each
  role" and "should sort by Created column ascending on the first click".
- **Table row action buttons have names (#688).** The view, edit and delete buttons of a table row
  are named View, Edit and Delete, followed by the record's name where the list shows it ("Edit
  Acme Ltd"), so screen readers announce them and the browser tests find them by name.

- **End-to-end code is type-checked in `make verify-frontend` (#621).** The new
  `gocrm-ui/tsconfig.e2e.json` covers `e2e/` (specs, page objects, helpers) and the three
  `playwright.config*.ts` files, and `make verify-frontend`, which the `frontend` CI job runs,
  runs that type-check (`npx tsc -p` on that config) after the production build. The response-wait
  helper (`apiResponse`) and the load-timeout constant (`LOADED`) that ten specs each defined are
  shared from `gocrm-ui/e2e/helpers/api.ts`. Browser tests still run only for releases.

- **Shared role and paged-list hooks in the web app (#624).** `gocrm-ui/src/hooks/useRoles.ts` is
  the one role-list query (key `['roles']`) for Settings > Roles, the user form and the single
  sign-on settings, so a role created or changed in Settings > Roles now shows up in the single
  sign-on role-mapping dropdown without a reload. `gocrm-ui/src/api/list.ts` holds the shared
  `ListResult<T>` page shape, `toListResult` and `withoutUnset` (unset filters are left out of the
  query). `gocrm-ui/src/hooks/usePagedList.ts` holds the page and rows-per-page state and the move
  back to the last existing page; the webhook endpoints, webhook deliveries, sign-in audit and role
  audit lists use it and keep the current rows on screen while the next page loads. Frontend only:
  no API change.
- **Form builder page split into modules (#626).** `gocrm-ui/src/pages/forms/FormBuilder.tsx` keeps
  the route component, the form state, the queries and mutations, the Basics and Fields sections and
  the buttons. The schema and the value conversions moved to `formBuilderSchema.ts`, the chip list
  editor to `StringListEditor.tsx`, the field editor to `FieldEditor.tsx`, the after-submission,
  notification, follow-up and spam sections to `FormSettingsSections.tsx` and the submission action
  with the lead owner picker to `SubmissionActionSection.tsx`, all in the same folder. Frontend only:
  no behaviour change and no API change.
- **Auth service split into one file per flow (#639).** `internal/service/auth_service.go` keeps the
  sentinels, the service struct, its options, the constructors and the credential-owner helper; the
  flows moved, unchanged, to `auth_login.go`, `auth_tokens.go`, `auth_apikey.go`, `auth_refresh.go`,
  `auth_logout.go`, `auth_password_change.go`, `auth_password_reset.go` and `auth_sso_policy.go`
  (single sign-on password policy), all under `internal/service/`. Code is only moved: there is no
  behaviour change. The concurrency guide's citations and their pin test, the docs that name the moved
  functions and the `auth` validation profile's file list follow the new files.
- **One last-admin guard (#638).** The rule that keeps at least one active admin is written once, in
  `internal/service/last_admin_guard.go`: it locks the active admin set, counts the locked rows of
  the accounts a write touches, and judges the state after the write, counting accounts that gain
  the admin role as well as those that lose it. User updates, activation, deactivation and erasure,
  the OIDC sign-in, the SCIM user writes and the SCIM group patch all use it. Behaviour, error
  responses and lock order are unchanged.
- **One helper for locking reads (#637).** `repository.LockingRead(tx)` is the one place that builds
  the `FOR UPDATE` handle on a fresh `Session`; the services (sign-in, refresh, password reset,
  password change, logout, API-key create, lead owner and lead checks, SCIM group membership,
  customer update) and the erasure, quote, automation, form-ticket and external-id repositories use
  it instead of their own copies. A user read through a locking handle now locks only the `users`
  row: `UserRepository.GetByID` reads the custom role and its permissions without a lock, so a
  sign-in or refresh of a custom-role user does not wait for a lock held on its role. Otherwise
  behaviour is unchanged.

- Web app: one shared dialog, `OneTimeSecretDialog` in `src/components/OneTimeSecretDialog.tsx`,
  shows a value that is shown only once: a new API key, a new SCIM token and a new webhook signing
  secret (#620). It replaces the two per-screen dialogs and the inline API key dialog. The three
  one-time value dialogs now close only through their Done button; a click outside them or Escape
  leaves them open. When the browser offers no clipboard access, the copy button shows an error
  instead of a success message. The copy error text is now the same on all three screens: "Could
  not copy to the clipboard. Select and copy it manually." (it named the token, the secret or the
  key before). Titles, labels, button names, success messages and test ids are unchanged; the SCIM
  token dialog's title id is now `scim-token-value-title`.

- Web app: API error messages are read by one shared helper (#623), `apiErrorMessage` in
  `src/utils/apiError.ts`, instead of each page parsing the error body. The single sign-on settings
  page now shows the refused fields on one line after the server message, and field details are
  shown only for 400 answers.
- Web app: removed the unused `authApi.refreshToken` helper (session refresh runs only through the
  API client) and a stale placeholder test in `LeadList.test.tsx` (#625).
- The automation engine service file is split by responsibility (#619): `automation_engine.go` keeps
  the engine type and its constructor, and the lifecycle, dispatch, schedule, run-lease, step,
  email-step and stale-run recovery code moves unchanged into `automation_lifecycle.go`,
  `automation_dispatch.go`, `automation_schedule.go`, `automation_run_lease.go`,
  `automation_steps.go`, `automation_email_step.go` and `automation_recovery.go` under
  `internal/service/`. No behaviour change.
- **Customer create and update always run as one transaction on the request context (#615).**
  Services get one shared helper that runs a write in a single transaction and gives the callback
  the repositories and the webhook and automation outlets bound to that transaction. The customer
  service is the first to use it: its transaction manager is now a required constructor argument,
  so customer create and update always run as one transaction, also where the service is built
  without webhooks. They run on the request's context, so cancelling the request ends the
  database work. Answers and webhook events are unchanged.

- **Webhook events come from one catalogue and one emitter (#614).** The webhook entities (lead,
  customer, ticket) are registered once in the new `internal/webhook` package. Every create,
  update, bulk-status, form, delete and erasure path writes its delivery rows through an emitter
  bound to its transaction, which does nothing when webhooks are not configured. The list of
  event types, the event type check and the validation messages are derived from the catalogue,
  and a test keeps the event type lists of the API description equal to it. The delivery rows,
  the API and the database schema are unchanged, except that the service-level refusal of an
  unknown delivery-log `event_type` now names all nine types (it named the six created and
  updated types). A later entity needs one catalogue entry and one emitter call per write path.

- **Single sign-on keeps the issuer's discovery document and signing keys for 24 hours (#632).**
  A sign-in no longer fetches the discovery document and the signing keys every time: they are
  kept per issuer for up to 24 hours and fetched again when an ID token names a key id the kept
  keys do not contain. A failed discovery is not kept, and a change of the configured issuer
  (or, with the switch below, of the client id) takes effect on the next sign-in. The new
  environment variable `OIDC_ENTRA_APP_SIGNING_KEYS` (default off) makes an Entra ID issuer's ID
  tokens be checked against the application's own signing keys at `jwks_uri?appid=<client id>`,
  which Entra ID uses when the app registration has claims mapping; any other issuer ignores it.
- **SCIM Groups accept Microsoft Entra ID's group requests (#630).** `POST /scim/v2/Groups` no
  longer answers 405: when the body's `externalId` or, failing that, its `displayName` equals a
  `groups` value of the single sign-on role mapping byte for byte, it answers 201 with that
  existing group, its `Location` and `id`, and writes nothing, so a repeat gives the same answer.
  Any other value, a non-string value or a body with neither is refused with 400 `invalidValue`,
  a body that lists members with 400 `invalidValue` (add them with PATCH), and a malformed body
  with 400 `invalidSyntax`. `GET /scim/v2/Groups` (including `filter=displayName eq "..."`) and
  `GET /scim/v2/Groups/{id}` honour `excludedAttributes=members`. A successful
  `PATCH /scim/v2/Groups/{id}` answers 204 with no body instead of 200 with the group; its
  refusals are unchanged. `DELETE` still answers 405, now with `Allow: GET, PATCH` only. The
  Entra ID guide keeps `objectId` → `displayName` as the group mapping: under Entra ID's default
  group mapping the group is created, but Entra ID's later `displayName` update is refused with
  400 `invalidPath`.

- **SCIM user patch accepts both Microsoft Entra ID PATCH formats (#629).** A role can arrive as a
  JSON object serialised into the role's `value` string (Entra ID's default PATCH format) or as a
  plain string (the standard form); the inner `value` is used. Add, replace and remove operations
  on User attributes GopherCRM does not keep (`externalId`, `displayName`, `title`, `nickName`,
  `preferredLanguage`, phone numbers, addresses, `name.formatted` and the other unkept name parts,
  and enterprise extension attributes) answer 200 and change nothing. Other unknown attributes,
  such as `password` or `groups`, are still refused with 400 `invalidPath`. The provisioning
  guides no longer ask administrators to delete those attribute mappings.

- **Recorded-output tests pin single sign-on and SCIM provisioning before the authentication
  refactoring (#636).** `TestOIDCSignInGoldenSQLite` and `TestOIDCSignInGoldenMySQL` replay one
  fixed run of 20 single sign-on callbacks (three sign-ins, then 17 refusals) and compare every
  error class, sign-in audit row, role audit row and linked account with one recorded file, on
  SQLite, MySQL and MariaDB alike. `TestSCIMProvisioningCycleGolden` sends a fixed 20-request SCIM
  cycle (users, groups and error answers) through the shipped router and compares status, headers
  and bodies with its recorded file. The developer guide lists these tests and the existing
  last-admin tests on MySQL, MariaDB and SQLite, with the commands to run them, as the checks a
  refactoring keeps passing unchanged. Tests and documentation only; nothing in the shipped server
  changes.

- **Tests open their databases through a shared test-support package (#613).** The new
  test-only package `internal/testsupport` opens a private SQLite database (`OpenSQLite`), reads
  the `TEST_MYSQL_DSN` server and skips without it (`MySQLFromEnv`), creates and drops a
  disposable MySQL/MariaDB schema (`OpenMySQL`), opens a second handle on an existing schema
  (`OpenMySQLSchema`), and switches or loops over the two dialects (`Open`, `ForEachDialect`).
  The `internal/repository` tests use it instead of their own setup helpers; test names and
  results are unchanged, and the other packages follow later. Only tests import the package, so
  nothing in the shipped server changes.

- **Single sign-on: the Entra ID group overage lookup requests the delegated Microsoft Graph
  permission `User.Read` instead of `GroupMember.Read.All` (#631).** For an Entra ID issuer whose
  role mapping has a `groups` entry, `POST /auth/oidc/start` now adds the scope
  `https://graph.microsoft.com/User.Read`, the least-privileged permission for
  `POST /v1.0/me/getMemberObjects`. The Graph call, its paging and budget and the stored settings
  are unchanged; a scope configured in `OIDC_SCOPES` or the settings is kept as it is. Admins who
  granted `GroupMember.Read.All` to the app registration for this lookup can remove it.

- **The default SCIM limit per provider token is 25 requests a second (#635).** A SCIM provider
  token may now make 1500 requests a minute (25 a second, up from 10 a second) with a burst of 150
  by default, the rate Microsoft Entra documents for provisioning. `SCIM_RATE_LIMIT_PER_MINUTE` and
  `SCIM_RATE_LIMIT_BURST` still override it, with the same maximums of 6000 and 1000.
- **Single sign-on sessions can be refreshed for 24 hours after the sign-in (#548).** A refresh
  session that started with single sign-on (`refresh_tokens.sign_in_method` `oidc`) can be rotated
  only until `session_started_at + 24` hours, a fixed limit with no setting: every refresh token
  such a session issues expires by then, and an older `oidc` token presented past the limit, or one
  without a start, is refused with the usual `401` (audit reason `token_invalid`) and nothing is
  written. The person then signs in at the identity provider again, which applies role and access
  changes made there. An access token issued by the last allowed refresh stays valid until its own
  expiry (`JWT_EXPIRY_HOURS`). Password sessions keep `REFRESH_TOKEN_EXPIRY_DAYS`. A password change
  that renews a single sign-on session keeps its sign-in method and start, so the renewed session
  has the same limit; past it, `POST /auth/change-password` answers `409 SESSION_STALE` and changes
  nothing. No migration, route or setting.

- **Password reset requests are answered at once; an email change ends earlier reset links
  (#554).** `POST /auth/password-reset` checks the body and answers the same `200` before the
  account is looked up: the email goes on an in-process queue (one worker, so requests are handled
  in order; up to 100 waiting), which issues the token, sends the mail and writes the
  `reset_request` audit row, normally within seconds. A request the full queue cannot take, or one
  that arrives during shutdown, is dropped with the same `200` and recorded as a `reset_request`
  row refused `unavailable` with no user id; at shutdown the request in flight finishes and the
  waiting ones are dropped. Changing an account's login email (`PUT /users/me`, `PUT /users/{id}`,
  SCIM `PUT`/`PATCH /scim/v2/Users/{id}`) now spends the account's unused reset tokens in the same
  write, so a link mailed before the change no longer works; other profile edits keep it. No
  migration, route or setting.

- **Sign-in audit: refused sign-ins, refreshes and SCIM authentications are counted per minute
  (#553).** A refused `POST /auth/login`, a refused `POST /auth/refresh` and a refused SCIM
  authentication on `/scim/v2` no longer write an `auth_audit_log` row each. Like refused API
  keys (#466), they add to one row per method, reason, account, client network and UTC minute,
  whose `attempts` holds the count; the row keeps the first attempt's request id, user-agent
  family and time. Successful sign-ins and refreshes, single sign-on callbacks, password changes,
  password resets, logouts and API key changes still write one row each. Answers, rate limits and
  reason codes are unchanged, and `GET /audit/sign-ins` and Settings > Sign-in audit show every
  reason with the full count. No migration: the `attempts` column and the unique
  `aggregate_key` index exist since #466.

- **Shutdown stops the background workers together (#561).** At shutdown the automation worker,
  the form retention purge, the bulk operation sweep, the webhook sender and AEO are now stopped
  at the same time and waited for together within the existing 15-second limit, instead of one
  after another, so a worker that is slow to stop no longer delays the others. The endpoint read
  and the expired-secret clear inside a webhook delivery attempt now each have a 5-second limit,
  like the write that records the attempt's outcome.

- **Browser tests started by `make e2e` and the release workflow run against the production build
  of the web app (#563).** `scripts/e2e/run.sh` builds the web app with
  `VITE_API_BASE_URL=/api/v1 npm run build`, the relative API base of the release artifact and the
  Docker image, into the run's temporary directory (`gocrm-ui/dist` is left alone; the build log is
  copied to `gocrm-ui/test-results/e2e-ui-build.log`), and Playwright serves it with
  `vite preview`, which forwards `/api/v1` to the run's backend on the web app's own origin. The
  runner needs `npm`, and its plan prints `ui_server=preview ui_api_base_url=/api/v1`. A new
  health-smoke test checks that the browser loaded the built `/assets/*.js` bundle, no dev-server
  module, and reached the API on the web app's origin. Manual Playwright runs without the runner
  still use the dev server.
- **SCIM requests with a valid provider token are limited per token (#555).** Requests on
  `/scim/v2` whose SCIM provider token is accepted are counted per token instead of per client
  address: by default 1500 a minute with a burst of 150, set by `SCIM_RATE_LIMIT_PER_MINUTE`
  (maximum 6000) and `SCIM_RATE_LIMIT_BURST` (maximum 1000); an unset, unparseable or below-1
  value takes the default. A full provisioning cycle from an identity provider's shared
  addresses is no longer throttled. Requests without a valid token keep the limit of 10 a minute,
  burst 5, per client address, whatever the HTTP method; above it they get the SCIM 429 and
  write no sign-in audit row. `DISABLE_RATE_LIMIT=true` and the functional-test switch lift both
  limits. `.env.example`, the SCIM guides and the developer docs describe the two limits.

- **Webhook deliveries wait instead of failing when the endpoint's signing secret cannot be read
  (#559).** Before every pass the sender opens the current signing secret of each active endpoint
  with the configured key (`API_KEY_SECRET`, or `JWT_SECRET` without it). The due deliveries of an
  endpoint whose secret cannot be opened are left out of the pass: they stay `pending`, unclaimed,
  with their attempts, next attempt, error code and last attempt time unchanged, the endpoint's
  failure count and pause are not touched, and the other endpoints are sent to as before. They go
  out on the first pass after a restart with the right key or after the endpoint's secret is
  rotated, without a manual redelivery. `GET /health` gains a `webhooks` object:
  `{"state": "running"}`, or `{"state": "paused", "reason": "secret_unavailable"}` while at least
  one endpoint is held; the top-level `status`, `revision` and the 200 answer are unchanged. One
  Error "Webhook deliveries held: endpoint signing secret cannot be opened" is logged when an
  endpoint is held and one Info "Webhook deliveries resumed" when it is released, each with the
  operation and endpoint id only (plus the error code on the first). A secret that stops opening
  between the check and the attempt now gives a normal retry with `secret_unavailable` instead of
  a failed delivery. The test ping is unchanged.
- **Webhook outbox rows of one change are written in one batched INSERT (#570).** The outbox rows
  of one event (one per subscribed endpoint) and of a bulk batch of records are written by a
  single multi-row INSERT inside the transaction that makes the change, so they roll back with it.
  The production path already did this; `TestWebhookOutboxBatchInsert{SQLite,MySQL}` now pins one
  statement per change, in the caller's transaction, on SQLite, MySQL and MariaDB. No behaviour
  change.
- **Webhook deliveries are sent by a small worker pool (#568).** A delivery pass used to send its
  due rows one after another, so one slow endpoint held up every other endpoint until its request
  timed out. The runtime now hands each endpoint's due rows to one of 4 workers
  (`service.WebhookWorkers`, set in `cmd/main.go` with `service.WithWebhookWorkers`), which sends
  them one at a time in id order, oldest first; the other endpoints keep moving. Each row is still
  claimed just before its send, claims, fences and the 2-minute lease are unchanged, and `Stop`
  waits for every attempt in flight before it returns. Delivery stays at least once: a retry or a
  second replica can still reorder an endpoint's events, so consumers deduplicate on `webhook-id`.
  Discovery (`WebhookRepository.ListDueDeliveries`, which replaces the id-only method) now
  returns each due row's `endpoint_id` with its id; the query is otherwise the same plain SELECT.
- **A refresh token presented again after its exchange ends its session (#547).** When
  `POST /auth/refresh` receives a token that a committed refresh already exchanged for a
  successor, it still answers the same 401 `Invalid or expired refresh token`, and it now also
  revokes every refresh token of that sign-in session (the token's `family_id`), the successors
  included, in a transaction that commits. The sign-in audit log records the refusal with method
  `refresh`, outcome `refused`, the new reason code `token_reuse` and the account's user id; the
  reason filter of `GET /audit/sign-ins` and Settings > Sign-in audit accept it (44 codes). A
  request that only lost a concurrent exchange of the same token is refused as before and ends
  nothing; a token revoked by logout, a password change or a deactivation, and a token with no
  recorded session, are refused as before as well. Access tokens already issued keep working until
  they expire.
- **Refresh tokens record their session (#546).** `refresh_tokens` gains `family_id` (shared by
  every token of one rotation chain, indexed by `idx_refresh_tokens_family_id`), `sign_in_method`
  (`password` or `oidc`) and `session_started_at` (when that sign-in happened). A password sign-in,
  a single sign-on sign-in and the token issued by a password change each start a new family of 32
  hex characters; a refresh rotation gives the new token the family, method and start of the token
  it replaces. Existing tokens get values at upgrade, from the startup step on every driver and from
  migration `20261129120004_refresh_token_session` on MySQL/MariaDB: each is its own family (its id),
  the method is `oidc` for an account linked to an identity provider and `password` otherwise, and
  the start is the token's `created_at`. Routes, responses and refusals are unchanged.
- **Sign-in audit vocabulary in one place (#549).** The five methods, two outcomes and 41 reason
  codes of the sign-in audit log are declared once, in `internal/service/auth_audit_vocabulary.go`.
  The code that writes audit rows (password, refresh, API key, single sign-on and SCIM) passes these
  constants, and the `GET /audit/sign-ins` filter accepts exactly the registered values.
  `TestAuthAuditWritersUseRegisteredValues` fails when a writer passes a value that is not
  registered or a declared constant is missing from the registry. No behaviour change: stored
  values, answers and the documented filter values are the same.
- **Sign-in audit writer check covers forwarded variables (#577).**
  `TestAuthAuditWritersUseRegisteredValues` now also fails when a value assigned to a variable that
  is forwarded into an audit entry (such as the reason fallback of the SCIM refusal) is not a
  registry constant. Test-only: nothing stored or answered changes.
- **Settings > Configuration (#565).** Edit and Reset show only to users who hold
  `settings.update`; a refused save or reset shows the server's message. JSON and free-form list
  values are typed as text and checked on Save (`Enter valid JSON`, `Enter a JSON array`), and a
  number field refuses an empty or non-numeric entry (`Enter a number`) instead of sending it; a
  malformed list of allowed values no longer breaks the editor. Reset asks for confirmation first. A
  settings list that fails to load shows an error with a Retry button instead of empty
  "No configurations found" tabs. Frontend only (`ConfigurationSettings.tsx`, new
  `ConfigValueEditor.tsx`), covered by `ConfigurationSettings.test.tsx`.
- **Database retries decide from the driver's typed error (#567).** One helper,
  `utils.RetryReasonOf` (`internal/utils/retryable.go`), classifies MySQL/MariaDB errors 1213
  (deadlock), 1205 (lock wait timeout) and MariaDB 1020 (record has changed since it was read,
  raised under `innodb_snapshot_isolation`), and SQLite busy/locked (primary result code 5 or 6),
  through `errors.As` on `*mysql.MySQLError` and the SQLite driver's error, also when wrapped. The
  message text is no longer read, so an unrelated error that mentions a deadlock is not replayed.
  `WithTransactionAndRetry` and the AEO run claim both use it; the claim treats 1020 like a
  deadlock (replayed, at most three attempts, then "run in progress"). Retry counts, the flows that
  retry, public errors and status codes are unchanged. `TestRetryReasonOf` and
  `TestTransactionRetryableError{SQLite,MySQL}` prove the classification against real errors on
  SQLite, MySQL 8 and MariaDB 10.11.

- **A SCIM group patch sets the account's state from the groups it is left in (#530).** An active
  managed account that `PATCH /scim/v2/Groups/{id}` leaves in no mapped group is deactivated in the
  patch's transaction: `is_active` false, its role kept, no `user_role_changed` row, and every
  access token, refresh token and API key ends, as for a SCIM deactivation; an account that is
  already deactivated gets no `users` write. A group patch that gives an account a role lacking a
  permission the old one had ends its signed-in sessions at once (`token_version` + 1, every
  refresh token revoked); its API keys keep working with the new role. A raised or unchanged role
  ends nothing. The last-admin rule counts a deactivated account as no longer an active admin, so
  removing the last active admin from its only mapped group is refused with 400 `mutability`,
  while a handover to another active account still passes. A failed revocation rolls the whole
  patch back.

- **API keys expire after at most one year, customer accounts hold none, and creating a key needs
  a recent sign-in (#532).** `POST /api-keys` without `expires_at` gives the key an expiry one year
  from now; an `expires_at` later than one year from now is 400 `expires_at must be at most one year
  from now`. A key stored without an expiry counts as expiring one year after its `created_at`.
  Migration `20261129120003_api_key_lifetime`, and the same step at every startup on every driver,
  gives every existing key without an expiry, or with a later one, an expiry one year from the
  upgrade (earlier expiries are unchanged) and revokes every key of a customer account; its down
  half changes nothing. Customer accounts cannot create keys (403 `FORBIDDEN` "Customer accounts
  cannot hold API keys", also when the role changes while the key is issued), and a key owned by a
  customer account is refused at authentication with the sign-in audit reason
  `customer_login_role`. Creating a key needs a sign-in (password or single sign-on) within the
  last 15 minutes: access tokens from a sign-in carry a new `auth_time` claim (the sign-in time),
  tokens from `POST /auth/refresh` carry none, and a missing or older sign-in time is 403
  `REAUTHENTICATION_REQUIRED` "Sign in again to create an API key: creating a key needs a sign-in
  within the last 15 minutes", before the body is read. Settings > API Keys offers at most one year
  ahead, says that an empty expiry means one year, and shows the sign-in-again message in the create
  dialog. Renaming, listing and revoking keys are unchanged; no schema, route or setting is added.

- **A password change, a password reset and a deactivation end every credential of the account in
  one step (#526).** `POST /auth/change-password`, `POST /auth/password-reset/confirm`, an
  administrator's deactivation (`POST /users/{id}/deactivate`, `PUT /users/{id}` with `is_active`
  false) and a SCIM deactivation (`PATCH` or `PUT` with `active` false, `DELETE
  /scim/v2/Users/{id}`) now also revoke every API key of the account for good (`is_active` false,
  `revoked_at` stamped; a key revoked earlier keeps its first time), the key that made a
  password-change request included (the answer stays 200). The administrator's deactivation now
  also revokes every refresh token and ends every access token issued before it, as the SCIM
  deactivation already did. Reactivating an account brings none of these credentials back. Logout
  is unchanged and leaves API keys alone. Each of these writes is one transaction: when any part of
  the revocation fails, nothing changes, and the reset link stays usable. The Profile page says
  that the other sessions are signed out and the API keys are revoked. docs/AUTHORIZATION.md lists
  which events end which credentials. No migration, route or setting is added; response bodies and
  status codes are unchanged.

- **API keys are created and switched on only from a signed-in session, and revoking a key is
  final (#525).** `POST /api-keys`, and `PUT /api-keys/{id}` with `is_active: true`, answer 403
  `FORBIDDEN` to a request authenticated with an API key and write nothing; a signed-in session
  (password or single sign-on) still gets 201. Renaming and revoking with a key still work.
  Revoking a key (`DELETE /api-keys/{id}` or `PUT` with `is_active: false`) stamps the new
  `revoked_at` field, returned by the API-key reads; revoking it again answers 200 and keeps the
  first time. `PUT /api-keys/{id}` with `is_active: true` on a revoked key, including a key that
  was inactive before this release, answers 409 `CONFLICT` and changes nothing, the name included;
  on a live key it answers 200 and changes nothing. A rename writes only the name, so a rename sent
  at the same time as a revocation cannot switch the key back on.
  `migrations/20261129120002_api_key_revoked_at` adds the nullable `api_keys.revoked_at` column on
  MySQL/MariaDB; existing rows keep it NULL. Tests on SQLite, MySQL 8 and MariaDB 10.11 cover a
  rename or a switch-on request racing a revocation in each order.

- **Single sign-on finishes only in the browser that started it (#528).** `POST /auth/oidc/start`
  now sets a short-lived sign-in cookie, `gophercrm_oidc_state` (`HttpOnly`, `SameSite=Lax`, ten
  minutes, `Path=<API_PREFIX>/auth/oidc`, sent only over HTTPS when `APP_BASE_URL` is https), tied to the
  sign-in it stored; a refused start sets none. `POST /auth/oidc/callback` refuses a callback that
  arrives without that cookie or with a different value, after spending the attempt, with the
  usual 401 "Single sign-on failed", and records the new sign-in audit reasons `binding_missing`
  and `binding_mismatch`. A later start in the same browser replaces the cookie, so only the
  latest sign-in started there can finish; a successful callback clears it. The cookie carries no
  credential and no other route reads it. The web app sends credentials on these two calls only.
  The web app and the API must be served from the same site, as the shipped nginx proxy does.

- **SCIM reads and changes only the accounts it created or that are linked to the single sign-on
  issuer (#529).** An account is served by the SCIM Users and Groups resources when it is live, not
  a customer, and has an identity link whose issuer equals the configured single sign-on issuer
  exactly, written by a SCIM create or an OpenID Connect sign-in. Accounts created in GopherCRM
  itself, such as the local administrator, and accounts linked only under another issuer are no
  longer listed or counted by `GET /scim/v2/Users`, and `GET`, `PUT`, `PATCH` and `DELETE
  /scim/v2/Users/{id}` answer 404 `User not found` for them and change nothing; with no issuer
  configured no account is served. Group reads list only such accounts as members, a group `add`
  or `replace` naming another account is refused with 400 `invalidValue` like an unknown id, and a
  `remove` of a membership row stored before deletes only the row, leaving that account's role and
  audit rows alone. A `POST /scim/v2/Users` with such an account's address still answers 409
  `uniqueness`. Changing the issuer hides the accounts linked only under the old one. The SCIM
  provisioning guide states the rule.
- **Single sign-on and SCIM settings are admin-only (#524).** The permissions
  `settings.sso_manage` and `settings.scim_manage` can be held only by the Admin system role.
  Creating or updating a custom role with either one is refused with 400 `INVALID_PERMISSION`
  naming the key and writes nothing; `GET /permissions/catalogue` marks both as not selectable, so
  the role editor no longer offers them; a custom role's stored rows holding them are ignored when
  its permissions are resolved; and a clone of the Admin role leaves them out. On every start, on
  every driver, the application removes both keys from any custom role that holds them and writes
  one `role_updated` row per changed role to the role audit log, with `auth_method` `migration`,
  no actor and the role's name, description and stored permissions before and after;
  `migrations/20261129120001_admin_only_sso_scim_permissions` does the same on MySQL/MariaDB, and
  its rollback restores nothing. Because the Admin role now holds permissions no custom role can
  hold, an admin account is always above a custom-role user: only an admin can assign the Admin
  role or edit, deactivate or delete an admin. Admins keep full access to both settings screens.

- **Rate limiting keeps serving new clients when its client table is full, counts IPv6 clients
  per /64 and refuses its test switch in production (#527).** When a rate-limit instance tracks
  its maximum of 10,000 clients, a new client is now admitted and the least recently active entry
  is dropped, instead of the new client receiving 429; the tracked count never exceeds the
  maximum, and more recently active clients keep their remaining allowance (a dropped client
  starts again with a fresh burst). IPv6 clients are counted per /64 network and IPv4 clients per
  address (an IPv4-mapped IPv6 address counts as its IPv4 address), for direct peers and for
  clients forwarded by a trusted proxy. The server refuses to start when `DISABLE_RATE_LIMIT=true`
  (or, in the functional-test build, `GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true`) is set together with
  `SERVER_MODE=production`, from the environment or from `.env`; other modes are unaffected. The
  container run of `scripts/e2e/quote-pdf-chain.sh` no longer sets the switch and paces its ten
  sign-ins instead.

- **Webhooks: deleted events go to every endpoint subscribed to them (#531).** `lead.deleted`,
  `customer.deleted` and `ticket.deleted` now go to every active endpoint subscribed to the event
  type, whether or not that endpoint received an earlier event for the record: an endpoint
  subscribed to the deleted types only, a record created before the endpoint was registered and a
  record whose earlier deliveries were pruned all get the deleted event. The outbox no longer
  reads the delivery log when it writes a deleted event: one read of the subscribed endpoints,
  then one insert, in the same transaction as the delete or erasure. Which records produce a
  deleted event, the event body and the transaction boundaries are unchanged. No migration.

- **`migrate create` numbers a new migration as the newest existing version + 1 (#522).**
  `go run ./cmd/migrate -command create -name <name>` now takes the highest version in the
  migrations directory plus one (the next one is `20261129120001`), so a new migration always
  sorts after every migration a database may already have applied. The clock is used only for an
  empty directory. The command accepts only letters, digits and underscores in the name, never
  overwrites a file, and refuses to write anything while the existing files break the layout. A new
  `cmd/migrate` test requires every file in `migrations/` to follow
  `<version>_<name>.{up,down}.sql`, each version to have exactly one up and one down file under one
  name, and versions to increase strictly in file name order. Existing versions are unchanged.

- **Sign-in audit: refused API keys are throttled and counted, not written one row each (#466).**
  The protected routes now run the moderate rate limiter (120 requests a minute, burst 30, per
  client address) before authentication, so a throttled request gets a 429 without a key lookup
  or an audit write; requests with refused or missing credentials count against that budget too,
  and a 429 can answer before a 401. Each refused `ApiKey` that reaches the key check adds to one
  `auth_audit_log` row per reason, user id, client `/24` or `/48` network and UTC minute, through
  one conditional upsert on a unique internal `aggregate_key`; the new `attempts` column counts the
  refusals, and the row keeps the first refusal's request id, user-agent family and time.
  Since #553 refused password sign-ins and refreshes are counted the same way; successful ones
  and single sign-on outcomes keep one row each (`attempts` 1), bounded by the strict `/auth`
  tier. `GET /audit/sign-ins` and Settings > Sign-in audit show `attempts`;
  `aggregate_key` is never returned. Migration `20261125120000_auth_audit_log_attempts` adds the
  two columns and the unique index, each only when missing; existing rows read `attempts` 1.

- **Sign-in audit: a revoked API key is recorded as `key_revoked` (#465).** The API-key lookup
  now returns a key whatever its `is_active` flag says, so the existing revocation check refuses a
  revoked key and the `auth_audit_log` row reads `key_revoked` with the owner's user id instead of
  `key_invalid` with none. The 401 the client gets is unchanged, byte for byte.

- **Single sign-on: sign-in hygiene (#453).** Every `oidc_sign_in` log line now carries a
  `request_id` (the HTTP request's, passed to the service on its context) and an `attempt_id` (the
  `oidc_login_attempts` row, `0` before Start created it or the callback claimed it); codes,
  states, nonces, verifiers, tokens, emails, subjects, names and claims are still never logged.
  The callback's claim is now a plain read by the state hash followed by one conditional `UPDATE`
  by id that also blanks the PKCE verifier and the nonce; a callback that finds the attempt
  expired blanks them too and still answers the generic 401, even when that clearing fails. The
  per-start purge of attempts that expired an hour ago is gone: the stale bulk-operation sweep (at
  boot and every 15 minutes, on every replica) now prunes attempts created more than one day ago.
  A first sign-in whose ID token has neither `given_name` nor `family_name` takes the names from
  `name`, split at the first whitespace. No schema change: clearing writes the empty string to the
  NOT NULL columns.
- **Single sign-on: groups are read from the userinfo endpoint when the ID token has none
  (#451).** When the role mapping has a `groups` entry, the ID token has no `groups` claim (an
  empty `[]` counts as present) and no Entra ID overage is being resolved through Graph, the
  callback now sends one `GET` to the issuer's discovered `userinfo_endpoint` with the access
  token of the same code exchange (Okta and other issuers that return thin tokens). The request
  has a 5-second deadline, follows no redirect, uses no proxy, needs an absolute `https` endpoint
  and reads at most 1 MiB of `application/json`; signed `application/jwt` userinfo is not
  supported. The userinfo `sub` must equal the ID token's `sub` or the sign-in is refused as
  `userinfo_subject_mismatch`; every other failure refuses as `groups_unavailable`. Only `groups`
  is read (absent means no groups, so a `roles` entry may still match); email, names and roles
  still come from the ID token. The access token and the userinfo answer are never stored,
  logged or cached. This also applies to an Entra ID issuer: with a `groups` mapping, a
  group-less person (no `groups` claim and no overage) now depends on Entra's userinfo endpoint,
  and a failure there refuses the sign-in even when a `roles` entry would match.
  docs/AUTHORIZATION.md gains a step-by-step Okta setup. No setting, migration or scope changes;
  Okta's `groups` scope comes from `OIDC_SCOPES`.
- **Single sign-on: an Entra ID group overage is resolved through Microsoft Graph instead of
  refused (#450).** When an Entra ID ID token (issuer host `login.microsoftonline.com`) moves its
  groups into `_claim_names` and the role mapping has a `groups` entry, the callback now reads the
  person's security-group object ids with `POST /v1.0/me/getMemberObjects`
  (`securityEnabledOnly: true`) on `https://graph.microsoft.com`, using the access token of the
  same code exchange, and maps them with the usual first-match rule. Graph gets one 5-second
  budget per sign-in, no redirects or proxy, same-host `@odata.nextLink` paging up to the new
  environment variable `OIDC_GRAPH_MAX_PAGES` (1..50, default 10), and any failure refuses the
  sign-in with the error code `groups_unavailable`. For such a configuration
  `POST /auth/oidc/start` adds the delegated scope
  `https://graph.microsoft.com/GroupMember.Read.All`, which needs admin consent in the tenant.
  Without a `groups` entry an overage is now ignored and `roles` decides; on any other issuer
  (sovereign Entra clouds included) it is still refused as `group_overage`. `_claim_sources` is
  never read, and the access token and Graph results are neither stored nor logged. App roles
  remain the recommended Entra ID setup. `GET`/`PUT /settings/sso` are unchanged.
- **The e2e test certificate trust is refused in production mode (#446).** The
  `-tags=gophercrm_e2e` backend no longer installs the per-run test CA from a package `init()`:
  `cmd/main.go` calls `aeo.InstallE2ETestRoots` once, right after loading the configuration and
  before the database or any listener is opened. It still does nothing without
  `GOPHERCRM_E2E_AEO_TLS=1`, and now exits with "refusing the e2e test TLS roots in production
  mode" when `SERVER_MODE` is `production` (in any letter case, also when set only in `.env`); a
  missing or invalid `E2E_AEO_CERT` is a startup error instead of a panic. Normal builds have a
  no-op twin and never trust the test CA.
- **The generic configuration endpoints no longer serve the single sign-on settings (#434).**
  `GET /configurations` and `GET /configurations/category/security` leave out every
  `security.sso.*` entry, and `GET`, `PUT /configurations/{key}` and
  `POST /configurations/{key}/reset` answer 404 for them, whatever the case or trailing spaces
  of the key (MySQL and MariaDB match keys case-insensitively). The configuration service's
  generic writes refuse them the same way; `/settings/sso` is their only API.
- **Quote response token leaves the URL after the first read; public quote responses and the
  PDF are not cached; a render credential is single use (#431).**
  - **Response page and API.** The `/quote-response` page takes the token from the emailed link
    once, then removes it from the address bar and that browser history entry with a replace
    navigation; reloading the page afterwards shows the invalid-link message, and the client
    opens the mailed link again. `GET /quotes/public/response` now reads the token from the
    `X-Quote-Token` request header only; a token sent as `?token=` is not read and gets the
    generic 400 "Invalid or expired quote link". The answer still sends the token in the body of
    `POST /quotes/public/response`, and the emailed link is unchanged. The strict CORS policy
    allows the `X-Quote-Token` header.
  - **No caching.** `GET` and `POST /quotes/public/response`, `POST /quotes/public/render` and
    `GET /quotes/{id}/pdf` answer with `Cache-Control: no-store`, error answers included.
  - **Render credential.** A PDF render credential can be redeemed once: a second redemption,
    concurrent or not, is the generic 400 "Invalid or expired render link" and that render fails
    (log reason `credential_redeemed_more_than_once`).
  - **Docs.** `docs/SERVER_PROVISIONING.md` and `docs/DOCKER.md` explain how to keep the query
    string of `/quote-response` out of a reverse proxy's access log, with an nginx example.
- **The backend image downloads `chrome-headless-shell` from Chrome for Testing instead of
  installing the Debian package (#420).** The image build no longer breaks when Debian's
  security repository drops the pinned `chromium-headless-shell` version. The browser stays on
  milestone 156 (156.0.8078.80, the Chrome for Testing patch release of the same Chromium
  revision; Chrome for Testing does not list the Debian package's patch number) and quote PDFs
  look the same.
  - **Source.** A download stage fetches the `linux64` or `linux-arm64` zip of
    `ARG HEADLESS_SHELL_VERSION` from Chrome for Testing, where Google keeps every published
    version, checks it against `HEADLESS_SHELL_SHA256_AMD64` or `HEADLESS_SHELL_SHA256_ARM64`
    and unpacks it into `/opt/chrome-headless-shell`. Any other target architecture stops the
    build. The runtime stage installs the shared libraries the binary loads, from the zip's
    `deb.deps`, and fails the build when `ldd` reports one as not found, plus `fontconfig`,
    whose font aliases the Debian package used to bring (without them every text is set in
    Liberation Mono). To change the version, set it and both checksums (`docs/DOCKER.md`).
  - **Size.** 471,504,459 bytes against 700,980,224 bytes before (linux/arm64): the Mesa
    drivers and LLVM that the Debian package pulled in are gone.
  - **Workflow.** A new `Docker image` workflow (weekly, on demand, and on pull requests that
    change the `Dockerfile` or the workflow) builds the download stage for linux/amd64 and
    linux/arm64 and the whole image for linux/amd64, prints a page to PDF with the image's
    launcher and checks the header and the browser version. It is not a required check.
  - **Upgrade note.** The Debian package's binary `chromium-headless-shell` (in the package's
    library directory) and its wrapper `/usr/bin/chromium-headless-shell` no longer exist in the
    image. A deployment whose `QUOTE_PDF_CHROME_PATH` names either of them has PDF output
    switched off; use the image's default, `/usr/local/bin/quote-pdf-browser`.
- **Quote PDF: the render URL no longer appears on the browser's command line (#396).** The URL
  of a render, with the render credential in its fragment, was the last argument of the browser
  process and so readable in the host's process list for the life of the render. The browser is
  now started with `--remote-debugging-pipe` and no URL, and the server drives it over its
  DevTools pipe (the browser's file descriptors 3 and 4): it opens a tab, navigates, waits for
  the page's request and paint, and prints with `Page.printToPDF`. No debugging port is
  opened; only the backend process can talk to the browser. No Go dependency was added: the
  pipe client is a small one on the standard library, and `go.mod` and `go.sum` are unchanged.
  The 15 s bound, one render at a time, the process-group kill, the temporary profile and the
  built environment are as before, and a browser that fails or exits now fails the render at
  once. This closes the limit recorded in `docs/ROADMAP.md`.
  - **For operators.** A launcher script named by `QUOTE_PDF_CHROME_PATH` must hand file
    descriptors 3 and 4 through to the browser, that is, not close or redirect them; one that
    execs the browser, as the Docker image's `/usr/local/bin/quote-pdf-browser` does, needs no
    change. The renderer now needs a Unix host: on Windows the server still builds, but every
    render fails (503 `PDF_RENDER_FAILED` on a download, text-only quote emails), where it
    could render before.
- **The four erasure roots that dominated the MySQL run of `internal/repository` share one migrated
  schema per root (#404).** `TestIdentityErasureMySQL`, `TestUnlinkedSubmissionErasureMySQL`,
  `TestLeadErasureScrubsDealAndQuoteTextMySQL` and `TestFormTicketKeyErasureMySQL` created and
  migrated a new schema for every subtest (36 in all). On MySQL and MariaDB each root now migrates
  once and gives every subtest a new handle on that schema after putting it back into its
  post-migration state: the rows the migration did not seed are removed and the id counters that
  moved are restored, and a comparison with the post-migration snapshot fails the run when
  anything leaks from one subtest into the next. The subtests that change the schema keep a
  database of their own, the SQLite roots keep a fresh file per subtest, and no subtest or
  assertion was removed; `each_node_is_scrubbed_exactly_once` additionally pins the id overlap its
  per-table count relies on. Test code only.
- **The eleven skipped ticket browser cases run, each against a ticket it seeds (#402).** The
  `test.fixme` cases in `admin-tickets.spec.ts` (create, edit, view, delete, search, status and
  priority filter, navigation, status and priority update, long description) seed their own
  customer or ticket through the API, address it by a unique subject and assert the outcome: the
  status of the save, the row in the list, or the stored value read back from the API. The four
  filter cases for leads, tasks and users stay `test.fixme` with a real assertion and the number
  of the defect they wait for in the title: `GET /leads` ignores `status` (#426), `GET /tasks`
  ignores `status` and `priority` (#427), `GET /users` ignores `role` (#428). Test code only.

- **The leads, tasks, tickets and users E2E page objects act on the row that matches a value
  unique to the test (#401).** Edit, view and delete take that value (lead company, task title,
  ticket subject, user email), search the list for it and click the action icon through a
  retrying expectation, as the customers page object already did. The index-based actions, the
  one-shot visibility check and the positional button fallback behind it are removed, and so are
  the unused positional row readers. The specs open and assert the record they created; the
  ticket cases that need a seeded ticket stay `fixme`. Test code only: the product does not
  change.
- **The backend image prints quotes with `chrome-headless-shell` instead of the full Chromium
  (#393).** What a quote PDF looks like does not change, and a full Chrome or Chromium named
  by `QUOTE_PDF_CHROME_PATH` keeps working: the server starts both kinds of binary with the
  same arguments.
  - **Base and binary.** The runtime stage moves from `alpine:3.20` to `debian:bookworm-slim`
    and installs the Debian package `chromium-headless-shell` 156, pinned by
    `ARG HEADLESS_SHELL_VERSION` in the Dockerfile, in place of the full
    `chromium` package. Alpine has no headless shell package and the prebuilt binaries need
    glibc. That pin expired at Debian's next Chromium update; #420 replaced the package with the
    Chrome for Testing download.
  - **Size.** 700,980,224 bytes against 686,689,600 bytes before (linux/arm64): the image is
    not smaller, the package's library dependencies make up for the missing browser UI.
  - **Health check.** The image installs `wget`, which both compose files call for the backend
    health check and the new base lacks. It has no `curl` and no `ps`.
  - **Upgrade notes.** The image's default is now
    `QUOTE_PDF_CHROME_PATH=/usr/local/bin/quote-pdf-browser`, a launcher that execs the
    headless shell with the two container flags. `/usr/bin/chromium-browser` no longer exists
    in the image: an override that sets `QUOTE_PDF_CHROME_PATH: /usr/bin/chromium-browser`
    switches PDF output off on the new image (one warning in the log, text-only quote emails,
    503 `PDF_UNAVAILABLE`); set it to `/usr/local/bin/quote-pdf-browser` or drop the line.
    `tini` moved from `/sbin/tini` to `/usr/bin/tini`: an override that names the entrypoint
    needs the new path. `/etc/chromium/chromium.conf` is gone; the flags are in the launcher.
  - **Checks.** `scripts/e2e/quote-pdf-chain.sh local|image` runs the real chain (browser,
    render page, API, text of the PDF) with a full Chrome or with the built image and stops
    what it started; `scripts/e2e/quote-pdf-smoke.sh` now creates a taxed line and also prints
    `total=` and `gross_total=`. The render in the image was verified on linux/arm64 only.

- **Frontend unit tests hold up better on a busy machine (#403).** `make verify-frontend` runs
  Vitest on three workers (`npm test -- --run --maxWorkers=3`) and `make verify` runs the frontend
  gate before the race-enabled Go tests. Every test in `src/routes/index.test.tsx`,
  `src/pages/forms/FormBuilder.test.tsx` and `src/layouts/MainLayout.test.tsx` has an explicit
  test timeout and every `findBy*`/`waitFor` call in them an explicit per-call timeout, so these
  files no longer depend on the 5 s test default or the 1 s query default. Test infrastructure
  only; no application code changed. The rest of the suite keeps the default timeouts
  (`docs/ROADMAP.md`).
- **Go sources are gofmt-formatted and the Backend check keeps them so (#405).** Twelve Go files
  that `gofmt -l` reported were reformatted (whitespace only, no behaviour change). The new
  `python3 scripts/ci/check-go-format.py` runs `gofmt -l` on every tracked Go file and fails with
  the list of unformatted files; the Backend check starts it before `go build` on both of its
  paths, `scripts/ci/check-changed-go.py` for pull requests and `make verify-backend` for full
  runs. Fix a failure with `gofmt -w` on the listed files.
- **Writes to a customer login with a linked customer record follow the customer permissions
  (#378).** Erasing a customer login that has a linked customer record (`DELETE /api/v1/users/{id}`)
  now needs `customers.delete` next to `users.delete`, and changing its email
  (`PUT /api/v1/users/{id}`) needs `customers.update` next to `users.update`. A custom role without
  the customer key is answered 403 `FORBIDDEN` and nothing is written. The four system roles are
  answered as before, and a customer login without a linked customer record is managed with the
  user keys alone, as before.
- **`POST /leads/{id}/assign` asks a custom role for `leads.assign_owner` when it names another
  user (#377).** A custom role calling `POST /api/v1/leads/{id}/assign` needs `leads.update`, and
  `leads.assign_owner` as well when `user_id` is not the caller, the key an owner change through
  `PUT /api/v1/leads/{id}` asks for; without it the answer is 403 `FORBIDDEN` "You can only assign
  leads to yourself" and nothing is written. Assigning a lead to oneself needs `leads.update` alone.
  The four system roles are answered as before (a sales user still hands over its own lead): the
  system-role route matrix is byte-identical. The route-key table declares the further key, and
  `TestLeadAssignRequiresAssignOwnerKey` in `cmd` covers a custom role with and without it. API
  docs regenerated; no schema or SPA change.
- **CI runs the MySQL 8 migration check on pull requests that change migration code (#376, CI).**
  A pull request whose diff adds, changes or deletes a path under `migrations/` or `cmd/migrate/`,
  or changes the migrations workflow or its coverage gate (`scripts/release/check-sql-tests.py`
  and its tests), now runs `Migrations (MySQL 8)` from the Database migrations workflow, with the
  same steps as a release, the no-skip check included. Other pull requests, merge-queue groups
  and pushes to `main` still skip it, and a `full` run (a release, or a manual run with
  `full: true`) is unchanged: MySQL 8 and MariaDB 10.11. The decision is the new `migrations`
  output of `scripts/ci/plan-validation.py`, read from the trusted selector's own migration set;
  it answers `true` when the change set cannot be read. The leg is not a required check and the
  Backend job does not depend on its result outside `full`; the four required checks are the same.
- **CI runs targeted backend tests outside a release (#365, CI).** For a pull request, a merge-queue
  group and a push to `main`, the Backend job builds and vets the module, runs the API reference
  check (`scripts/ci/check-api-docs.py`) and runs `go test -count=1` without `-race` only on the Go
  packages the change touches, the packages that import them and the few whose tests read the
  changed files from disk; the new `scripts/ci/check-changed-go.py` makes that selection from the
  plan job's changed paths (`--base origin/main` reproduces it locally). A documentation-only change
  runs no Go tests. The Database gates job runs the same selection against MariaDB 10.11, without
  rejecting skipped tests. The full suite with `-race`, MySQL 8, the two-engine migrations matrix,
  `make verify-api-docs` and the no-skip checkers run only when CI is called with `full: true`: the
  release workflow, or a manual dispatch, which gains a `full` input (default off; without it a
  manual run tests every package without `-race`). The four required check names, the release
  workflow and `make verify` are unchanged.
- **Roadmap: scheduled AEO client reports are a deliberate cut (#320, docs).** docs/ROADMAP.md
  records scheduled or emailed client reports as a deliberate cut and names its prerequisites: a
  single scheduling owner across replicas (#229), a mail delivery record without false success
  (#216) and a decision on external recipients (#238). No behaviour change.
- **Mail names the configured product (#292, backend).** The password reset mail (subject and
  first body line), the quote mail subject and the ticket reply mail subject now use the stored
  `general.company_name` setting, read at send time, instead of the fixed `GopherCRM`. The value is
  used trimmed when it is non-blank, valid UTF-8, free of control characters and at most 100
  characters; otherwise, and when the setting is missing or cannot be read, the mail names
  `GopherCRM` and is still sent. A fallback other than a blank value logs one warning with the key
  and a reason, never the value. No configuration row is written, seeded or migrated. For forks:
  `Mailer.SendPasswordReset` takes a third `productName` argument, and `QuoteEmailSubject` and
  `TicketCommentEmailSubject` take the name as a second argument.
- **CI: the browser suite is release-only.** The `E2E (Playwright on …)` jobs run only when the CI
  workflow is called with `full: true`, which only the release workflow does; pull requests,
  merge-queue groups, pushes to `main` and manual dispatches skip them, so no browser spec runs in
  pull-request CI at all. The profile-specific database regressions moved to a new required job,
  `Database gates (MariaDB 10.11)`, and the remediation runner's required checks follow (hygiene,
  backend, frontend, database gates). The runner's local validation is unchanged. This supersedes the
  bounded browser selection described in the next entry.
- **CI runs the full browser suite only for a release.** Pull requests, the merge queue and
  pushes to `main` ran the smoke spec and the changed features' specs on every database, even
  when a change touched shared code, and kept the backend and database gates. A release still
  forces the complete suite. A manual run can no longer ask for it. (Superseded before release by
  the entry above: pull-request CI now runs no browser spec.)
- **The double opt-in submission replays on a deadlock (#217).** Spending the older tokens of a
  re-submission can deadlock with the retention purge, so `storePendingSubmission` now runs under
  `WithTransactionAndRetry`, with a fresh submission id on each attempt and one confirmation mail
  after the commit.
- **Erasure redacts the deal and quote text that can name the person (#245).** Erasing a customer
  or lead (directly, through a converted lead or through a customer-role login) now also, in the
  same transaction, sets the title of every linked deal to `[redacted]` and empties its notes and
  lost reason; sets the title of every quote addressed to an erased customer or grown from such a
  deal to `[redacted]` and empties its notes, whatever its status; resets each line description of
  those quotes to its product's name; and hard-deletes the notifications about those deals. Live
  and soft-deleted rows are reached alike. The rows, their links, stages, amounts, statuses, totals
  and history stay, `deals.source` is kept, and deals and quotes reached only through `owner_id`
  are untouched. A task a `create_task` step rendered from `{{deal.title}}` before the erasure keeps
  its text, and graphs erased before this release are not repaired. No schema change and no API
  response change.

- **Sales users see Tickets in the navigation.** The API already let sales read every ticket and
  the ticket pages already showed it no create, edit or delete; only the nav item was hidden, so
  sales reached tickets by deep link alone. The navigation now matches the API, and the dashboard
  "New Ticket" quick action and the customer page's "Create Ticket" and "New Ticket" buttons, which
  led sales (and the dashboard one customers) to the unauthorized page, show only to admin and
  support (#241).
- Refresh-token persistence validation now uses auth/session and transaction boundary checks,
  named SQLite/MySQL rotation regressions, migrations and auth browsers; CI adds MariaDB.
  Shared repository-interface changes qualify only with proof that other declarations are
  unchanged and all six required rotation boundary roots are present in the candidate.
  Unrelated entity browsers and frontend unit suites are excluded from this
  bounded profile; explicit full/release validation remains complete.

- **Lookup indexes for leads and companies (#109).** `leads.email`, `leads.classification`,
  `leads.external_id` and `companies.domain` are now indexed, non-uniquely (a person may be
  captured as a lead twice; a soft-deleted company keeps its domain while a live one reuses it).
  The duplicate-domain check compares the stored, already lower-cased domain with plain equality
  instead of `LOWER(domain)`, which no index could serve on SQLite or MySQL. MySQL/MariaDB
  upgrades run `migrations/20261029140000_add_lead_company_lookup_indexes` (idempotent after an
  AutoMigrate start, lower-cases any domain written outside the application; rollback drops the
  four indexes); SQLite gets the same indexes from AutoMigrate, and application startup
  (`models.MigrateDatabase`) lower-cases such domains on every driver, so SQLite installs, which
  never run the SQL migration, keep catching a mixed-case duplicate. Query plans before and after are
  recorded in `docs/datamodel.md`.

- **CI runs for the merge queue.** The workflow also runs on `merge_group` events, so the suite
  passes on the queue's temporary branch, on top of the current `main`, before a pull request
  merges. Production deploys still follow only CI runs of pushes to `main`.

- **Bulk status service narrowed (internal, no API change).** `POST /leads|tickets|tasks/bulk/status`
  now run on a `BulkStatusService` holding only the three status operations, over a
  `BulkStatusRepository` (the read and write halves of a status change) and a
  `BulkOperationRepository` reduced to the history-row create and outcome update. Responses,
  authorization, all-or-nothing transactions, `error.details`, the `bulk_operations` history
  rows and the lead automation events are unchanged; new SQLite/MySQL regression roots pin the
  rollback, history and event behaviour (#112).

### Removed

- **The unused CSRF middleware and cookie-session helpers (#362).** Authentication is header-only
  (Bearer or ApiKey) and no cookie credential is accepted, so CSRF does not apply; the code that
  prepared a cookie session was never installed and is gone: `internal/middleware/csrf.go`
  (`CSRF`, `CSRFToken`), the `GenerateCSRFToken`/`ValidateCSRFToken` token codec on `AuthService`,
  `config.CSRFConfig`, the whole of `internal/utils/cookie.go` (`SetAuthCookies`,
  `ClearAuthCookies`, `SetSecureCookie`, `ClearCookie`, `GetTokenFromCookie`, `SetCSRFCookie`,
  `IsCookieSecureContext`, `ParseSameSite`, `ValidateCookieConfig`, `GetCookieMaxAge`,
  `GenerateSecureToken`, `HashToken` and the cookie-name constants) and
  `JWTConfig.CookieSameSite`/`CookieDomain`/`CookieSecure` with the production warning about the
  `Secure` flag. `JWT_COOKIE_SECURE`, `JWT_COOKIE_DOMAIN` and `JWT_COOKIE_SAMESITE` are no longer
  read; setting them is harmless. No route, response or behaviour changes. A new integration test
  pins that a valid access token sent only in cookies answers 401 on a protected route. Any
  future cookie session must ship with session-bound CSRF protection (#261).
- **Two settings that did nothing.** `security.session_timeout_hours` and
  `tickets.auto_assign_support` were seeded and never read: session length comes from
  `JWT_EXPIRY_HOURS` and `REFRESH_TOKEN_EXPIRY_DAYS`, and tickets are not distributed among support
  users (a ticket created without `assigned_to_id` goes to its creator). Startup now deletes both
  rows on every driver, and `migrations/20261031120000_retire_inert_configurations` does the same
  on MySQL and MariaDB; its down migration restores them as first seeded. The Settings page no
  longer lists them (#224).
- **`repository.NewCustomerRepositoryWithLeadErasure` (#108).** The identity cascade now lives in
  the plain user, customer and lead repositories, so no constructor can erase half a person;
  `cmd/main.go` uses `NewCustomerRepository`.
- **The unused generic bulk subsystem (#112).** Nothing routed it since the generic
  `/bulk/:resource` handlers were dropped: the `ProcessBulk*` dispatchers, the
  `BulkCreate*`/`BulkUpdate*`/`BulkDelete*`/`BulkAction*` service methods for users, leads,
  customers, tasks and tickets, the matching repository mutations (including the batch-erasure
  wrapper and its per-item savepoint helpers, which only that path used), the bulk-operation read
  methods, their mocks and tests, and the request/response DTOs. Single-record erasure is
  untouched. No table or column is dropped: `bulk_operations` keeps receiving one row per status
  update, and `bulk_operation_items` stays in the schema although nothing writes it any more
  (retirement is a separate roadmap item).
- **Customer fields the API never stored (#118).** The customer form no longer offers Industry,
  Number of Employees, Annual Revenue or the "Active Customer" switch, the customer list drops
  its Status column and the customer detail page drops the Industry, Annual Revenue and Employees
  fields and the Active/Inactive chip. None of them exists on the customer API: saves discarded
  what was typed and reads showed invented defaults (blank, zero, always active). The Company
  record already owns industry and employee range, and customers link to it; there is no
  customer activation concept. The frontend `Customer` type and the read adapter no longer carry
  the four fields. No backend change.

### Fixed

- **Release notes fit GitHub's limit.** A changelog entry longer than 60,000 characters is
  published as its headings and entry titles, with a link to the full changelog.

- `TestMySQLBaseDSNFromEnvironmentOnly` (`cmd/migrate`) now checks the skip decision of the
  `TEST_MYSQL_DSN` helper through a pure decision function and a recording fake instead of
  skipping real subtests, so it emits no skip events and the release SQL gate
  (`scripts/release/check-sql-tests.py`) passes again on MySQL 8 and MariaDB 10.11. The gate's
  no-skip rule is unchanged, and the MySQL/MariaDB migration roots still skip without
  `TEST_MYSQL_DSN`. Tests only (#720).
- `docs/FEATURES.md` row 1.12 now gives the shared OpenID Connect sign-in table
  (`oidcSignInCases`) as 70 top-level subtests instead of 66: the four `audit_*` cases #463 added
  were never counted. The SQLite root runs 70 top-level subtests and the MySQL root 71, the extra
  one being `last_admin_race/after_handover`, a single subtest whose name contains a slash rather
  than a parent with a nested subtest. Docs only (#713).
- On MySQL and MariaDB the startup auto-migration (`models.MigrateDatabase`) now creates tables in
  `utf8mb4_unicode_ci` with `ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`, as the SQL migrations do,
  instead of inheriting the database's default collation (MySQL 8's `utf8mb4_0900_ai_ci` would
  change trailing-space email matching). Existing tables are not converted. This also fixes the
  intermittent `TestFormTicketCustomerMatchMySQL` failure on MariaDB in CI (#696).
- The labels, deals and tickets browser-test page objects find row actions by role and accessible
  name (`Edit label`/`Delete label` on the labels page, `View`/`Edit`/`Delete` on the deal and
  ticket lists) instead of by MUI icon test ids, which the production build the browser tests run
  against since #604 does not render, so their edit, view and delete clicks no longer time out
  (#692).
- **The lead owner check sees a custom-role change committed during the lead write (#677).** On
  MySQL/MariaDB the owner check of `POST /leads`, `PUT /leads/{id}` and `POST /leads/{id}/assign`
  read the owner's custom role and its permissions from the snapshot fixed by the transaction's
  first plain read, so an owner whose role had just lost `assignment.form_owner` could still be
  accepted. The check now reads them through `UserRepository.GetByIDWithCurrentRole`, which takes
  a shared lock on the `roles` row and its `role_permissions` rows after the owner's locked `users`
  row, so the change is seen and the write is refused with 400 `INVALID_REFERENCE`. A role write in
  flight makes the check wait; other lead writes for holders of the same role do not wait for each
  other. `GetByID`, and with it sign-in and refresh, is unchanged.
- `TestTaskRowLockProductionWiring` (test only, #663) now registers its query callback before the
  router starts the background runtimes and no longer removes it while they may still run, so the
  race detector no longer reports the bulk-operation recovery sweep reading gorm's callback list
  while the test changes it.
- The validation planner now treats the AEO service files split out of `aeo_service.go` in #651
  (`aeo_profile.go`, `aeo_prompts.go`, `aeo_prompt_generation.go`, `aeo_runs.go`,
  `aeo_reports.go`, `aeo_normalize.go` under `internal/service/`) as AEO paths, so an AEO-only
  change to them selects the bounded `aeo` validation profile again instead of `contextual`, or
  `full` when mixed with other AEO files (#665).
- The validation planner now treats `internal/service/aeo_export.go` (the AEO export service, #244)
  as an AEO path, so an AEO-only change to it selects the bounded `aeo` validation profile instead
  of `contextual`, or `full` when mixed with other AEO files (#669).
- The validation planner now treats `internal/handler/aeo_export.go` (the AEO export handler, #244)
  as an AEO path, so an AEO-only change to it selects the bounded `aeo` validation profile instead
  of `contextual`, or `full` when mixed with other AEO files (#683).
- The Microsoft Entra ID provisioning guidance now gives Entra ID's default `externalId` source as
  `mailNickname` (docs only, #628). The how-to
  (`docs/user-guide/permissions-access/how-to-microsoft-entra-id.md`) gains a SCIM provisioning
  section that names each Entra admin center screen: it maps `objectId` to `externalId` before the
  first cycle, keeps only the user attributes GopherCRM reads, adds the app-role expression mapping
  to `roles` and says that a later app-role change is refused today (400 `invalidPath`, changed in
  GopherCRM instead), and makes groups match by sending the group's `objectId` as `displayName`.
  `docs/user-guide/permissions-access/scim-provisioning.md` and `docs/AUTHORIZATION.md` no longer
  call `objectId` the default. This supersedes the Entra ID mapping wording of the #505 and #491
  entries.
- `docs/FEATURES.md` row 1.12 now gives the shared OIDC sign-in table as 66 subtests instead of
  65 (#579 added `sign_in_records_session`), and 67 on `TestOIDCSignInMySQL`, which also runs
  `last_admin_race` (docs only, #611).
- `TestPasswordResetRequestOwnerLockConcurrentMySQL` now holds an erasure competitor uncommitted
  until the reset mail is recorded, so the mailer's committed check no longer races the erasure
  deleting the token (test only, #597).
- The deadlock subtest of `TestTransactionRetryableErrorMySQL` now detects the first connection's
  lock wait from information_schema.PROCESSLIST instead of the cached INNODB_TRX view, which a
  continuous poll kept stale, and fails on a probe query error instead of ignoring it
  (test only, #590).
- The single sign-on callback route test in `gocrm-ui/src/routes/index.test.tsx` now waits for
  the page to clear the query string instead of reading it once right after the failure message
  appears, since the page clears it in an effect after the first render (test only, #607).
- **A quote response link is created, sent and accepted only while the quote's customer exists,
  and earlier links stop working only after a successful send (#557).** The mint transaction of
  `POST /quotes/{id}/email` (`mintResponseLink` in `internal/service/quote_email.go`) now reads
  the quote's customer under the quote lock and refuses with 409 when it is gone, storing no
  token and sending nothing. The public read (`FindUsableByHash`) also requires a live customer,
  and `RespondPublic` re-reads the customer under the quote lock before the claim; both refuse
  with the same generic 400. The claim itself reads no `customers` row, so it adds no lock wait
  against a customer erasure on MySQL and MariaDB. The mint no longer invalidates the links of
  earlier emails: after the mailer reports success, a short transaction stamps only the quote's
  unused tokens older than the one just sent (`InvalidateOlderForQuote`), so a failed send leaves
  the client's current link usable. A failure of that step after the mail went out is logged
  with ids only and the request still succeeds. No migration and no new route. Covered by new
  subtests in `quote_email_test.go`, `quote_response_erasure_test.go` (SQLite, MySQL 8 and
  MariaDB 10.11) and `quote_response_token_repository_test.go`.

- **A refused user reload signs the tab out the same way the focus check does (#574).** When
  `GET /users/me` answered 401 or 403 to the reload that follows a role save or an own-account
  save, the web app cleared the tokens and the query cache but kept the old user and its
  permission list published with no token stored. `loadUser` in
  `gocrm-ui/src/contexts/AuthContext.tsx` now also publishes the signed-out state, as
  `refetchOnFocus` already did; a late refusal of a session that was replaced in the meantime
  still changes nothing, and a 429 or a network error keeps the session. The mount path is
  unchanged. Covered by new cases in `gocrm-ui/src/contexts/AuthContext.permissions.test.tsx`.
  Frontend only, no API change.

- **The password-reset and #175 citations of `docs/CONCURRENCY.md` point at the right lines again
  (#581).** 23 citations of `internal/service/auth_service.go` (the retry-rule roots, the
  password-reset request and confirm flows and the #175 failure-mode paragraph) had drifted to
  unrelated statements and are re-pointed. The new `TestConcurrencyDocAuthServiceCitationsPinned`
  in `internal/config/concurrency_doc_test.go` pins every auth_service.go citation of the document
  to its enclosing function and a text fragment of the cited line, so a drifting line fails CI.
  Documentation and tests only.

- **An erasure stops and rolls back when its request is cancelled or times out (#571).**
  `DELETE /customers/{id}`, `DELETE /leads/{id}` and `DELETE /users/{id}` now run the erasure on
  the request's context, through the service and into the erasure transaction. A client that goes
  away, or a request that times out, before the commit leaves the person untouched: no field is
  overwritten, no credential is purged and nothing is soft-deleted, and the request answers the
  generic 500. Inside a caller's transaction the erasure's savepoint is still rolled back, without
  aborting that transaction. No API or schema change.
- **Bulk task status changes and single task updates read the task rows with a locking read
  (#558).** `POST /tasks/bulk/status` reads its tasks with a locking read in id order, and
  `PUT /tasks/{id}` writes in one transaction that first reads the task row with a locking read
  and re-checks the completed-task rule against it. Each writer now sees the status the other
  committed: a task completed by one request is not moved out of `completed` by the other, and the
  bulk assignee check judges the current assignee. No API or schema change.
- **Failed sign-in attempts made at the same time are each counted (#545).** A wrong password
  is now recorded in one transaction on the stored account row, read with a locking read, so the
  account lock after five failed attempts no longer depends on request timing. An expired lock
  still starts a fresh window, and an erased account's row is left untouched. A concurrent
  regression test covers SQLite, MySQL 8 and MariaDB 10.11.
- **The web app reloads the signed-in user after a role save and after an own-account save
  (#566).** Saving an existing role in the role editor, and saving the signed-in user's own
  account in the user form, now read `GET /users/me` again, so navigation, route guards and
  buttons follow the new permission list straight away instead of at the next window focus or
  page reload. Creating a role, saving another account and a refused save do not reload.

- **A users write racing a SCIM group handover can no longer remove the last active admin
  (#513).** SCIM user replace, patch and delete, the governed user write behind
  `PUT`/activate/deactivate/`DELETE /users` and the OIDC sign-in role re-apply judged the
  last-admin rule only from the admin set `LockActiveAdmins` returned. On MySQL/MariaDB that set
  is discovered with a consistent read, so a handover X → Y that committed while the write waited
  on X left it empty, and a demotion, deactivation or deletion of Y committed with no active admin
  left. The guard now also counts the target's own locked row when it is an active admin; the
  write is refused as before (409 `LAST_ADMIN`, SCIM 400 `invalidValue`, OIDC `last_admin`).

- **The `cmd/migrate` MySQL-backed tests read their server only from `TEST_MYSQL_DSN` (#514).**
  They now skip when the variable is unset or empty and fail when it names a server that does
  not answer. They no longer probe a local MySQL with the `.env` configuration, `$USER`, `root`
  or the e2e credentials, so an empty variable can no longer run them against whatever server
  the machine happens to have.

- **The `cmd/migrate` MySQL/MariaDB tests accept a `TEST_MYSQL_DSN` that carries query
  parameters (#519).** They built each disposable database's DSN by appending the name and
  `?multiStatements=true&parseTime=true` to the base, so a base such as
  `user:pass@tcp(host:port)/?parseTime=true` failed every root with "invalid DSN". The tests now
  parse the base with the driver and set the database name on it: their own `multiStatements` and
  `parseTime` win, every other parameter (timeout, charset, TLS, ...) is kept. Every other
  `TEST_MYSQL_DSN` reader already parsed the DSN with `ParseDSN`, so none needed a change.

- **Drifted code citations in the fork and extension guides (#473, #474).** The line-number
  citations in `docs/FORKING.md` and `docs/EXTENDING.md`, several of which no longer pointed at
  the code they described, are replaced by citations of the file plus the declared symbol, or of a
  heading, job, step, key or quoted anchor for non-Go files. The six Compose and Dockerfile line
  citations that tests recompute from the files are kept.
- **Shutdown cancels a stale bulk-operation sweep prune that is waiting for the database (#485).**
  The stale bulk-operation sweep's single sign-on attempt prune and sign-in audit prune now run
  their statements on the sweep's context (`PruneCreatedBefore` takes a `context.Context`). Before,
  a prune waiting for a pooled connection or a lock ignored `Stop`, used up the whole shared drain
  deadline and made the later stops (webhook sender, AEO) report that they did not stop; this was
  the cause of the intermittent `TestAEORuntimeShutdown` failure.
- **`TestAEORuntimeShutdown` admission precondition is exact (#494).** Its first subtest took any
  rise of the pool's `WaitCount` as proof that the due tick was waiting in run admission, but the
  boot passes of the stale bulk-operation sweep and the webhook sender could raise it first. The
  subtest now settles both before it holds the connection (it seeds one expired sign-in audit row
  and waits for the sweep's prune log line, and stops the webhook sender), so it proves that
  cancelling the owner's context releases a waiting scheduled `StartRun`. Test-only.

- **The `docs/FORKING.md` §7.3 citation of the AutoMigrate list points at the list again (#464).**
  The conflict hot-spot table cited `internal/models/database.go:114-156`, lines inside an
  unrelated backfill helper; it now cites `internal/models/database.go:212-275`, the
  `tx.AutoMigrate(` call in `autoMigrateModels` through its closing parenthesis. Documentation only.

- **Password reset is refused for an account linked to single sign-on while
  `security.sso.allow_password` is off, the default (#444, made a setting by #457).**
  With the setting off, `POST /auth/password-reset` issues no token and sends no mail for an
  account with a linked OpenID Connect identity (a `user_identities` row, which only the first
  OIDC sign-in creates), and `POST /auth/password-reset/confirm` refuses such an account's token
  with the generic 400 and leaves it unspent. The public answers are unchanged (the same 200
  message, the same 400), so the rule discloses nothing. Without it, anyone controlling the
  provisioned address could set a password and keep signing in after the identity provider
  offboarded the person, bypassing the role mapping. With the setting off the #435 account's "no
  usable password" holds; turning it on lets such an account reset like a local one. The check runs inside the
  existing transactions, a plain read after the owner lock; `cmd/main.go` wires it through
  `service.WithAuthIdentities`. Decision recorded with it: JIT provisioning still does not require
  `email_verified=true` and does not read the claim, because Entra ID usually leaves it out of its
  ID tokens, the email never links or authorizes an account, and while reset is refused for linked
  accounts (the default) an unverified address does not lead to a password.
- **The Tasks list Status and Priority dropdowns filter again (#427).** `GET /tasks` now reads its
  optional `status` (one of `pending`, `in_progress`, `completed`, `cancelled`) and `priority` (one
  of `low`, `medium`, `high`) query parameters on the server, each matched exactly and
  case-sensitively. They apply in the query before offset and limit, so `data.total` and `meta`
  count only the matching tasks. For an admin they combine with each other, with `label_id`, with
  `search` (which `label_id` still overrides) and with `sort_by`/`sort_order`; for every other role
  they narrow only the caller's own tasks, combine with `label_id`, ignore `search`, and honour the
  sort only together with `label_id`, as before. An empty value is ignored, a repeated parameter
  uses its first value, and any other value (`done`, `Completed`, `urgent`) is answered 400
  `VALIDATION_ERROR` with `error.details` keyed by the offending parameter name(s), the value not
  echoed, before anything is read. The filtered path fills `assigned_to` for every caller,
  including the non-admin list. The `admin-tasks.spec.ts` cases "admin can filter tasks by status
  (#427)" and "admin can filter tasks by priority (#427)" run instead of being `test.fixme`. No
  schema change.
- **The Leads list Status dropdown filters again (#426).** `GET /leads` now reads its optional
  `status` query parameter on the server: one of `new`, `contacted`, `qualified`, `unqualified`,
  `converted`, matched exactly and case-sensitively. It applies in the query before offset and
  limit, so `meta` counts only the matching leads. For an admin it combines with `search`,
  `classification` and `sort_by`/`sort_order`; for a sales user it narrows only the caller's own
  leads (search, classification and the sort stay ignored there, as before). An empty value is
  ignored, a repeated parameter uses its first value, and any other value (`lost`, `Qualified`) is
  answered 400 `VALIDATION_ERROR` with `error.details.status`, the value not echoed. `external_id`
  still overrides every other parameter, `status` included. The Status dropdown drops `Lost`,
  which no lead can have, and gains `Unqualified`; the `admin-leads.spec.ts` case "admin can
  filter leads by status (#426)" runs instead of being `test.fixme`. `GET /leads/export` still
  ignores `status`. No schema change.
- **The Users list Role dropdown filters again (#428).** `GET /users` now reads its optional
  `role` query parameter on the server: one of `admin`, `sales`, `support`, `customer`, `custom`,
  matched exactly and case-sensitively. It applies in the query before offset and limit, so `meta`
  counts only the matching users, and it combines with `search` and `sort_by`/`sort_order`. An
  empty value is ignored, a repeated parameter uses its first value, and any other value (`Sales`,
  `manager`, a blank) is answered 400 `VALIDATION_ERROR` with `error.details.role`, the value not
  echoed. Visible consequence: the ticket form's "Assign to Support Agent" picker, which asks for
  `role=support&is_active=true`, now lists only support users; admins, though valid assignees, are
  no longer offered there. `is_active` is still ignored, so inactive support users stay listed.
  No schema change.
- **The Webhooks screen no longer shows an empty list after the last row of a later page is gone (#423).**
  On Settings > Webhooks, deleting the only endpoint of the last page left the endpoints table on
  that page: it showed "No webhook endpoints yet." and a pagination label such as "21-20 of 20"
  while endpoints still existed. The delivery log did the same with "No deliveries match these
  filters." when a later page came back empty (deliveries pruned, or their endpoint deleted, since
  the page was opened). Both tables now move to the last page that exists when a fetched page is
  empty while `meta.total` is above 0, show their empty-state text only when the total is 0, and
  never hand the pagination control a page beyond the last. Frontend only: no API change.

- **A mailed quote response link stops working when the quote's customer is erased (#395).**
  Erasing a customer left the response link of a quote email (#382) usable: the client could
  still open and answer a sent quote after the erasure. The erasure now stamps `used_at` (with
  `decision` NULL, as a newer email or a staff status change does) on every unused
  `quote_response_tokens` row of the quotes addressed to an erased customer, whichever root
  reaches the customer (`DELETE /customers/{id}`, the admin erasure of a converted lead, the
  erasure of a customer-role login). `GET` and `POST /quotes/public/response` then answer the
  same 400 "Invalid or expired quote link" as for a used or expired token, and the quote keeps
  its status, dates, totals and history.
  - **Same transaction.** The stamp is part of the erasure unit, after its update of those
    quotes: a failed or rolled-back erasure leaves the link usable.
  - **Primary-key writes.** The token ids are read with a plain `SELECT` and stamped by primary
    key, the statements of the existing invalidation, so the erasure holds no gap of the
    `quote_id` index and does not block the mint of another quote's link; the order is
    `quotes → quote_response_tokens` (`docs/CONCURRENCY.md`).
  - **Untouched.** Rows the client already spent, the tokens of other customers' quotes, and
    the tokens of a quote that shares the erased customer's deal but is addressed to another
    customer. No schema change, no change to the public errors or the web application.
  - **Not covered.** A quote email that is in flight while the erasure runs can still mint and
    mail a link the erasure does not see (`docs/ROADMAP.md`).
  - Roots, on SQLite, MySQL 8 and MariaDB 10.11, registered in the transaction profile of
    `scripts/ci/check-go.py`: `TestQuoteResponseAfterCustomerErasure{SQLite,MySQL}`
    (`internal/service/quote_response_erasure_test.go`),
    `TestErasureInvalidatesQuoteResponseTokens{SQLite,MySQL}` and
    `TestErasureQuoteTokenWritesLeaveGapsFreeMySQL`
    (`internal/repository/erasure_quote_tokens_test.go`).
- **A password-reset confirmation that loses a deadlock is replayed instead of failing (#407).**
  `POST /auth/password-reset/confirm` claims its token before it locks the owner's `users` row,
  the reverse of the order an erasure of that user and a new reset request take. A confirmation
  racing either could be ended by the database with error 1213 and answered 500 although nothing
  was wrong with the link; on MariaDB 10.11 this happened every time the erasure already held the
  token row, because the claim locks an index entry before the row there. The confirming
  transaction now runs under the retry rule (`WithTransactionAndRetry`, at most
  `resetConfirmMaxRetries` = 3 replays after a deadlock, a lock wait timeout or a busy SQLite
  file). Every attempt redoes the lookup, the claim, the owner lock and the writes, so a replay
  that finds the token spent or gone, or its owner erased, gets the usual 400 for an invalid or
  expired link, and one that finds them intact commits the reset. When the bound is exhausted the
  route still answers the generic 500 with nothing written and the link is still valid. The order
  of the locks, the refresh rotation, the erasure, the routes and the schema are unchanged. Roots
  (`internal/service`): `TestPasswordResetConfirmRetrySQLite` (new),
  `TestErasureVsCredentialFlowsMySQL` and `TestPasswordResetRequestVsConfirmMySQL`, which no
  longer accept error 1213 from the confirmation on either engine. `docs/CONCURRENCY.md` records
  the confirm under the retry rule.

- **Registration and `POST /users` no longer return the service error text, and an over-long
  password is refused the same way everywhere (#397).** `POST /auth/register` and `POST /users`
  used to answer 400 `BAD_REQUEST` with the raw error for any failure of the write, a database
  error included. A user that cannot be stored is now 500 `INTERNAL_ERROR` with the generic
  message and the cause goes to the log only; a duplicate email stays 409 `CONFLICT`, a weak
  password 400, and the role refusals of `POST /users` keep their codes. The four routes that
  take a new password (`POST /auth/register`, `POST /users`, `POST /auth/change-password`,
  `POST /auth/password-reset/confirm`) now check its length before anything else and answer
  400 `BAD_REQUEST` `password must be at most 72 bytes long` for more than 72 bytes (bytes,
  not characters: the bcrypt limit). Change-password and reset confirm used to answer 500
  there, registration and `POST /users` a 400 with the bcrypt text. Nothing is stored or
  changed and a reset link is not spent. `PUT /users/{id}` and `PUT /users/me` accept no
  password and are unchanged. **Compatibility:** a client that treated every 400 of the two
  create routes as a correctable input error now sees 500 for a server-side failure.

- **`PUT /leads/{id}` can clear the optional text fields (#398).** `phone`, `company`,
  `position`, `source`, `external_id` and `notes` used to apply only when non-empty, so a lead
  form saved with an emptied Phone or Notes answered 200 and the old text came back on reload.
  A sent `""` is now stored; an omitted key or a JSON `null` still keeps the stored value, and
  `first_name`, `last_name`, `email`, `status` and `classification` still ignore an empty value.
  **Compatibility:** an API client that sent `""` for one of the six fields used to have it
  ignored and now clears the field; send `null` or leave the key out to keep the value. The
  email-or-phone rule of create is still not re-checked on update, so the phone of a lead
  without an email can be cleared through the API. Emptying Notes removes the whole stored text,
  the form-submission blocks in it included. The lead form still requires "Company (as entered)"
  and "Lead Source" and has no position or external-id field, so in the web application only
  Phone and Notes can be emptied; the other four are cleared through the API. Customer updates
  (#270) are unchanged.
- **Users: a database failure on a user lookup answers 500, not 404 (#399).** `GET /users/{id}`,
  `GET /users/me` and the user lookup of `POST /customers/{id}/assign` answered 404
  `User not found` for every lookup error. Only a missing user is a 404 now; a read that fails
  is logged at error level and answered with 500 `INTERNAL_ERROR` and the generic message, with
  no driver text. On the assign route the customer is not updated.
- **Privacy: two erasures of one person no longer end in a deadlock error (#385).** An erasure
  started from a user, from a customer and from a lead locked the same rows in different orders
  and was never retried: on MySQL 8 and MariaDB 10.11 a user-rooted erasure against a
  customer-rooted one, and a lead-rooted against a customer-rooted one, ended with error 1213 for
  one caller (a 500; nothing was half-erased). Every erasure (`userRepository.Delete`,
  `customerRepository.Delete`, `leadRepository.Delete` and `DeleteUnconverted`) now discovers the
  person's rows with plain reads, cancels the automation runs of the leads, locks by primary key
  in one order (leads, customers, users, `refresh_tokens`, `password_reset_tokens`, `api_keys`,
  ids ascending), reads the graph again under the locks and only then scrubs. A graph that moved
  between the discovery and the locks rolls back with `repository.ErrErasureGraphChanged` and the
  erasure starts over, at most `erasureGraphMaxRestarts` (3) times; a lost deadlock or lock wait
  is replayed by `WithTransactionAndRetry`, at most `erasureMaxRetries` (3) times. The discovery
  and the schema probes of the cancel run before the transaction, which issues no plain read
  before its last graph lock: on MySQL 8 a select on `information_schema` fixes the REPEATABLE
  READ snapshot, and the re-check would otherwise miss a customer linked meanwhile. Inside a
  caller's transaction (`WithTx`) the erasure runs once; the governed user write behind
  `DELETE /users/{id}` restarts its root on a changed graph. What is erased, the public errors
  and the status codes are unchanged; an exhausted bound is the generic 500 a deadlock was. Two
  opposite orders remain and are documented in [docs/CONCURRENCY.md](docs/CONCURRENCY.md): the
  governed user write holds the target's `users` row before the nested erasure locks leads and
  customers (replayed by either side; a governed delete replayed after the other erasure
  committed answers 404, the account being erased already), and the password-reset confirm takes
  its token before the `users` row (it is replayed when it loses a deadlock, see the #407 entry
  under Fixed).
  The new seams (`repository.ErasureHooks`) are test-only, and
  the regression roots (`internal/repository/erasure_lock_order_test.go`,
  `internal/service/erasure_credential_order_test.go`) are registered in the
  `scripts/ci/check-go.py transaction` gate.
- **AEO: concurrent run claims no longer surface a deadlock on MySQL/MariaDB (#371).** Several
  replicas claiming the same daily slot, or starting manual or single-prompt runs at the same
  moment, could make the server answer the claim INSERT with error 1213 (deadlock) instead of a
  duplicate key, and that raw driver error reached the caller: a 500 for a manual start, an error
  log for the scheduler. Reproduced on MariaDB 10.11 for both claim kinds. `ClaimRun` now treats
  error 1213 and error 1205 (lock wait timeout) on the claim as contention: a scheduled claim
  looks the slot up first and answers "slot already started" when a row holds it; otherwise the
  INSERT is replayed, at most 3 attempts in total per claim, each going through the usual
  duplicate-key classification. A deadlock on every attempt answers "run in progress" (409, or a
  scheduled skip), never the driver error. A lock wait timeout is replayed once; a second one on
  the same claim is still returned as the driver error, so a claim waits for at most two lock
  waits. No sleep or backoff, no schema change and no API change. Regression roots:
  `TestAEORunClaimContention{SQLite,MySQL}` (`internal/repository`), with the existing
  eight-claimant `TestAEORunClaimMySQL` as the concurrency proof.
- **Automation: a duplicate run dedupe key is no longer reported as a started run on MariaDB
  10.5+ (#367).** On MariaDB the driver appended `RETURNING` to the dedupe insert
  (`ON DUPLICATE KEY UPDATE id=id`), so a duplicate `dedupe_key` came back with the existing row
  and `CreateRunIgnoringDuplicate` answered "inserted" with that row's id; event dispatch and the
  schedule trigger then over-counted started runs. No duplicate row was ever stored, and MySQL 8
  and SQLite were not affected. The same statement is now executed without `RETURNING` and the
  result is the driver's affected-row count (1 insert, 0 duplicate) on MySQL, MariaDB and SQLite;
  the passed run keeps id 0 on a duplicate. No schema change.
- **Privacy: a lead erasure no longer misses the customer of a concurrent conversion (#337).**
  `leadRepository.Delete` and `DeleteUnconverted` still cancel the lead's automation runs first,
  then read the lead with a locking read by primary key (`FOR UPDATE` on MySQL/MariaDB) and take the
  non-admin refusal and the decision to follow `customer_id` on that locked row only. The address
  read before the scrub (`readErasedEmail`) and `eraseCustomer`'s read of the customer are locking
  reads by primary key too, and every erased row is now scrubbed with its address read after the
  scrub's row lock; the converted-lead pluck stays a plain read. A conversion that commits while an
  erasure is in flight is therefore either seen (an admin erasure erases the new customer too, a
  non-admin erasure is refused with `ErrConvertedLeadNeedsAdmin`, 403, and changes nothing) or
  waits behind it and answers 404; a non-admin erasure can no longer pass the "unconverted" check
  on stale state. Every erased lead, customer and user is now soft-deleted by an `UPDATE` that
  filters by primary key only (GORM's soft delete, still through its Delete callback chain, with
  the `AND deleted_at IS NULL` predicate suppressed, so `UPDATE … SET deleted_at = ? WHERE id = ?`,
  after a locking read of whether the row is still live): MariaDB 10.11 was seen serving that
  predicate as a locking scan of the `deleted_at` index, blocking an unrelated conversion's
  customer insert until the erasure committed.
  Customer- and user-rooted erasures inherit the per-lead order. No route, status
  code or schema changes; lead-rooted erasure still has no retry. Test-only seams:
  `repository.WithLeadErasureHooks` (never wired in production) and a lead service seam after
  `Delete`'s pre-read. The stale-snapshot root (`TestLeadErasureConversionCommittedAfterCancelMySQL`)
  opens the erasure's REPEATABLE READ snapshot with a plain read of the lead before the conversion
  commits, and asserts that snapshot is still stale when the erasure resumes, so it fails when the
  locking discovery reads are reverted to plain reads. See [docs/CONCURRENCY.md](docs/CONCURRENCY.md).
- **Security: reset tokens and API keys are issued only to a live, active owner under the owner
  lock (#342).** `RequestPasswordReset` keeps its plain email lookup, then runs one
  `WithTransactionAndRetry` transaction (3 retries) that locks the owner row by id (`FOR UPDATE` on
  MySQL/MariaDB) and, only when that row is live and active, spends the outstanding reset tokens
  and inserts the new one through the transaction-bound repository (users →
  password_reset_tokens); the mail goes out once, after the commit, to the address of the locked
  row. `apiKeyService.Generate` locks the owner the same way before it inserts the key (users →
  api_keys). An erasure or deactivation that commits first now makes the request issue nothing; one
  that commits after it hard-deletes the token or key (erasure) or leaves it unusable
  (deactivation). The reset answer is unchanged (the same 200 in every case); `POST /api-keys` now
  answers 401 `Unauthorized`, without a key, for an owner deactivated or erased mid-request
  (`service.ErrAccountUnavailable`) and 500 for any other failure. A failed invalidation or insert
  rolls both back and sends no mail (before, a failed invalidation was logged and the new token
  inserted anyway). A reset request on a service without a transaction manager now issues nothing,
  and `NewAPIKeyService` takes the user repository and the transaction manager
  (`NewAPIKeyService(apiKeyRepo, userRepo, txManager, apiKeySecret)`; without them `Generate`
  returns `service.ErrAPIKeyIssuanceUnavailable`). The new order inverts the reset confirm's
  (password_reset_tokens → users): when both hold their first lock on MySQL one ends in error 1213
  and is replayed, the request by this change and the confirm since #407 (see Fixed). No schema
  change.
  `docs/CONCURRENCY.md` records both C3 flows as implemented. Roots (`internal/service`):
  `TestPasswordResetRequestOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
  `TestPasswordResetRequestOwnerLockConcurrentMySQL`, `TestPasswordResetRequestVsConfirm{SQLite,MySQL}`,
  `TestAPIKeyCreateOwnerLock{Decisions,Atomicity,Concurrent}SQLite`,
  `TestAPIKeyCreateOwnerLockConcurrentMySQL`, `TestCredentialIssuanceOwnerLockLogsOmitPersonalData`;
  `TestAPIKeyCreateOwnerUnavailableMapping` (`internal/handler`);
  `TestCredentialIssuanceOwnerLockAPISQLite` (`tests`); `TestAPIKeyOwnerLockProductionWiring` (`cmd`).
- **Security: login issues its session from the owner row locked in one transaction (#336).** On a
  service built by `NewAuthServiceWithSessions` (production wiring), `LoginWithTokens` and `Login`
  keep the plain email lookup and bcrypt before any transaction, then run one
  `WithTransactionAndRetry` transaction that locks the owner row by id (`FOR UPDATE` on
  MySQL/MariaDB) and refuses, with nothing written, a hash other than the verified one, an inactive
  or erased account (`invalid credentials`) or an active lock (the lockout error). It writes
  `last_login_at` and the lockout clear, inserts the refresh token under a savepoint (a
  non-retryable failure keeps the JWT-only fallback) and signs the access JWT from the locked
  `token_version`, in the order users → refresh_tokens; a deadlock, lock wait or SQLite busy
  replays the whole transaction. A password change, reset, deactivation or erasure committed while
  a login was verifying can no longer be followed by a session minted from the stale row. No API,
  schema or error-message change; a service built by `NewAuthService` keeps the unlocked login.
  `docs/CONCURRENCY.md` records Login (C2) as implemented. Roots:
  `TestLoginOwnerLock{Decisions,Atomicity}{SQLite,MySQL}`,
  `TestLoginConcurrentCredentialChanges{SQLite,MySQL}`,
  `TestRefreshRotationConcurrentCredentialChanges{SQLite,MySQL}`,
  `TestLoginOwnerLockLogsOmitPersonalData`, `TestLoginUnwiredServiceUnchanged` and
  `TestLoginOwnerLockAPISQLite`.
- **Saving a ticket form in the builder keeps ticket intake on (#335).** The builder sent no
  `create_ticket`, and `PUT /forms/{id}` stores an omitted flag as false, so saving a form
  configured through the API for #213 silently turned ticket intake off. Every create and update
  now sends the flag.
- **A cancelled automation run releases its lease (#333).** Runs cancelled by a lead erasure
  (admin or the owning sales user) or a ticket delete no longer keep `owner_instance` and
  `heartbeat_at`: `cancelAutomationForEntity` now sets both to NULL in the same conditional update
  that sets `cancelled` (`record_deleted`), as the manual and rule cancels already did. The
  predicate, the transaction and the ticket-row-first lock order are unchanged; a cancel issued
  after a run completed, failed or was already cancelled changes nothing. No API, JSON or schema
  change, and no backfill of runs cancelled earlier. Roots:
  `TestAutomationCancelReleasesLease{SQLite,MySQL}` (`internal/repository`) and
  `TestAutomationCancelPathsReleaseLease{SQLite,MySQL}` (`internal/service`).
- **Lead conversion, lead update and bulk lead status are serialised with company deletion
  (#182).** One lock order is shared: companies (`CompanyRepository.LockLive`, `FOR UPDATE` by
  primary key on MySQL/MariaDB) → leads (id ascending) → customers → deals. `DELETE
  /companies/{id}` locks the company before unlinking; a conversion or lead update locks the
  companies its attempt involves, then the lead, and restarts (at most 3 times, only after a clean
  rollback) when the lead's `company_id` changed in between; bulk lead status locks only its
  leads, in id order. Once both have committed, no live customer or lead references a
  soft-deleted company. New answer: `POST /leads/{id}/convert` and `PUT /leads/{id}` return 409
  `CONFLICT` ("The lead's company changed during the write; retry") when the lead's company keeps
  changing; nothing is written. A company deleted between the delete's lookup and its lock is
  404 with nothing written. Lead create, customer and deal writes and form intake are not covered
  yet (see the roadmap).
- **Full-page documentation screenshots.** The fixed app bar and the permanent navigation drawer no longer appear part-way down full-page captures: the screenshot helper applies a screen-only stylesheet for the shot that pins both to the top of the document (`gocrm-ui/e2e/screenshots/helpers/capture.ts`, `gocrm-ui/src/test/screenshotCapture.ts`). The live application and print media are unchanged; the images themselves are regenerated with `npm run screenshots` in a follow-up. (#187)
- **Local Compose overrides are git-ignored.** `docker-compose.override.yml` and `docker-compose.prod.yml` are in `.gitignore`, so a fork's local copy of the production override with real values cannot be committed by accident; `docs/DOCKER.md` names both. (#302)
- **Browser tabs sharing a remembered session no longer sign each other out (#184).** The web
  client runs every refresh and password change under one origin-wide exclusive lock (the Web Lock
  `gophercrm-session`; an in-tab lock where `navigator.locks` is missing, as on plain HTTP) and
  re-reads the stored session once it holds it: a tab whose request was rejected after another tab
  renewed the session adopts the stored successor instead of spending the refresh token again, and
  a password change presents the pair another tab rotated to. A refresh rejected with 400/401 ends
  the session only while storage still holds the token that was presented; one that finds another
  session stored clears nothing. The refresh deadline (15 s) now includes the wait for the lock,
  and a refresh still queued at the deadline never runs. Sign-out does not wait for the lock. The
  session is stored as one record under `gophercrm_session` (session id, both tokens, account,
  persistence choice) read whole from one storage, so a session without a refresh token never takes
  one from the other storage; `gophercrm_token` and `gophercrm_refresh_token` remain as write-only
  mirrors (removing `gophercrm_token` still signs the tab out), and a session stored under the two
  legacy keys is migrated once on the first read, so nobody is signed out by the upgrade. Other
  open tabs still learn about a sign-in or sign-out only on their next request.
- **Bulk status history rows no longer stay `pending` for good (#196).** A bulk status batch
  (`POST /leads|tickets|tasks/bulk/status`) now writes its `bulk_operations` row `completed` as the
  last write of its own transaction, with a conditional update from `pending`, so the row commits
  with the records or not at all. After a batch that rolled back, `failed` is written by a
  conditional update that never overwrites a terminal row. A row whose `failed` write timed out
  (lock contention, a held pool) is failed by a sweep that every replica runs at boot and every 15
  minutes, once the row is an hour old; it reads its candidates without locking and updates them by
  primary key in chunks of 500. `failed` therefore means none of the batch's records changed, except
  for rows already `pending` before this upgrade, which are swept to `failed` although their batch
  may have committed. The one answer that changes: a batch whose own row was swept before it
  committed answers 500 and changes nothing. MySQL installs run
  `20261110120000_add_bulk_operation_status_index` (a non-unique index on `bulk_operations(status)`);
  SQLite gets it through AutoMigrate. No setting is added.
- **Customers no longer show a fabricated Total Revenue (#186).** The API has never returned a
  revenue value; the web client's adapter defaulted `total_revenue` to 0, so the customer list and
  detail pages showed `$0` for every customer. The "Total Revenue" column is gone from the
  customer list and the "Total Revenue" entry from the detail page's Business Information, and
  `total_revenue` is removed from the frontend `Customer` type, the adapter and the test factory.
  No revenue is computed; that would be a separate feature. Frontend only: the API is unchanged.
- **Erasure reaches unlinked form submissions of the erased address (#214).** Erasing a lead, a
  customer, a staff user or a customer-role user now also scrubs, in the same erasure unit, every
  `form_submissions` row with no `lead_id` (spam, never-confirmed opt-ins, `create_lead=false`
  forms) whose `email` is the erased row's original address or its lower-cased form, live and
  soft-deleted, on live and deleted forms, in every status: the email becomes the erased row's
  placeholder, `data` becomes `{}`, and the IP, user agent, referrer and six attribution columns
  are emptied; `form_id`, `status`, the null `lead_id` and the deletion state stay. Their
  confirmation tokens and mail delivery rows are hard-deleted, so the old link no longer confirms
  and no retry mails the address. Only the email column is matched (exact or lower-cased on
  SQLite, the `utf8mb4_unicode_ci` collation on MySQL/MariaDB), so every submission from that
  address is affected, a shared inbox's included; a blank or placeholder address matches nothing.
  A schema without `form_submissions` skips the step. Ticket title, description and resolution
  stay as business records: DEVELOPMENT.md documents an operator procedure that redacts them
  through `PUT /tickets/{id}` before `DELETE /customers/{id}`.
- **`PUT /customers/{id}` can clear the optional text fields (#270).** `phone`, `company`,
  `position`, `address`, `city`, `state`, `country`, `postal_code` and `notes` used to apply only
  when non-empty, so the customer form's cleared fields answered 200 and came back on reload. A
  sent `""` is now stored; an omitted key or a JSON `null` still keeps the stored value.
  `first_name`, `last_name` and `email` still ignore an empty value, and `website` and `company_id`
  keep their own rules. The lead update had the same limitation until #398.
- **`PUT /tickets/{id}` stores a new assignee (#269).** On a ticket that already had an assignee the
  request validated the new user, answered 200 and kept the old one, because GORM wrote the
  preloaded `AssignedTo` id back over the foreign key. The service now detaches a preloaded user
  that differs from the foreign key before the write and attaches the user the key names afterwards,
  so `assigned_to_id` and `assigned_to` both name the new assignee. Assigning an unassigned ticket
  now also fills `assigned_to` in the response. A support user who reassigns a ticket loses access
  to it at once, as the assignee rule already implied.
- **A customer write can no longer restore an erased person (#256).** `PUT /customers/{id}` and
  `POST /customers/{id}/assign` wrote the customer back with GORM's `Save`, which falls back to an
  upsert, so a request that read the customer before an erasure could restore the personal data and
  clear `deleted_at`. `CustomerRepository.Update` is now a conditional update of named columns on a
  live row that never inserts. A write that loses to an erasure, through a customer, converted-lead
  or customer-role-login delete, answers 404 `NOT_FOUND` with nothing written and logs a Warn line
  with ids only. It no longer writes `user_id`, `created_at`, `deleted_at` or the preloaded login and
  company. Live customers behave as before, including last-write-wins, the website and `company_id`
  clears and the 409 on a duplicate email.

- **A documented API change no longer runs the whole browser suite by itself.** The regenerated
  `api/swagger.json`, `api/swagger.yaml` and `api/index.html` selected the full profile on every
  annotation change. `make verify-api-docs`, which every profile runs, already fails unless they
  equal a fresh `make swagger`, so the planner now classifies the Go sources that produce them
  instead: a lead handler change runs the lead suites. Any other path under `api/` still selects
  full coverage. Validation policy version 13.
- **Changes to standalone operator scripts no longer run the whole browser suite.** The CI and
  runner validation planner treated every path under `scripts/` as shared code, so the #226 fix to
  `scripts/create_database.sql` ran all Playwright specs on three databases. Five tools that the
  server, the CI gates and `make e2e` never execute are now listed by exact path and, like
  documentation, add no browser specs beyond the health smoke; the database and cleanup scripts'
  tests run in `make verify-hygiene`. Validation policy version 12.
- **`make create-db` creates the database the server connects to.** `scripts/create_database.sql`
  created `gophercrm`, while the default `DB_NAME` is `gocrm`, so the documented MySQL path failed
  to connect on the first start. The script now creates `gocrm`, `gocrm-ui/cleanup-test-users.sh`
  reads `DB_NAME` (default `gocrm`) instead of naming `gophercrm`, the backup step in
  `scripts/anonymize_legacy_deleted_pii.sql` dumps the schema the file runs against, and a hygiene
  test keeps the script, the config default, `.env.example` and both Compose settings on one name
  (#226).
- **The API key security scheme names the header the server reads.** The spec described a
  separate `ApiKey` header, which is never read; it now documents `Authorization: ApiKey <key>`
  (#227).
- **Pull-request CI runs the products/quotes and automation migration roots and the auth MySQL
  roots.** The migrations job now runs `TestProductsAndQuotesMigration` and
  `TestMigrationAutomationSchema` on MySQL 8 and MariaDB 10.11 and rejects their skips (#228).
  A `full` run also executes `check-auth.py --persistence` against MySQL; before, those roots
  skipped on every general change because `make verify-backend` has no `TEST_MYSQL_DSN` (#230).
- **`make build` and `make run` work again.** They compiled only `cmd/main.go`, but the `cmd`
  package now spans several files, so both failed with `undefined: drainAEO`. They build the
  package (`./cmd`) now, and the documented run commands use `go run ./cmd`.

- **Mail to internationalised domains the CRM accepts (#180).** The SMTP transport refused some
  non-ASCII domain labels the CRM's email validation accepts, for example `info@col·legi.cat`
  (U+00B7, allowed by RFC 5892), with `ErrInvalidAddress` before dialling even when the relay
  advertises SMTPUTF8, so password resets and quote emails to such saved addresses failed. A
  domain label may now hold any non-ASCII character except controls (format controls included,
  ZWNJ/ZWJ excepted) and whitespace; the address is delivered with SMTPUTF8 and still refused
  before `MAIL FROM` when the relay does not advertise it. Domain literals, CR/LF, `<` and `>`
  stay refused everywhere, the local part keeps its rules, and no refusal echoes the address.
  Regression root: `TestSMTPInternationalisedDomains` (`internal/mailer/smtp_idn_test.go`).
- **Cancelled requests stop lead conversion, form submissions and bulk status updates (#107).**
  `POST /leads/{id}/convert`, `POST /forms/public/{key}/submissions`,
  `POST /forms/public/confirm` and `POST /{leads,tickets,tasks}/bulk/status` used to run their
  database work and the reCAPTCHA check on a fresh background context, so a client that
  disconnected or timed out left the request queued for SQLite's single pooled connection, a
  MySQL row lock or the captcha service, and its writes landed afterwards. The handlers now pass
  the request context through the services into the transaction manager, the reads that precede
  the transaction (the conversion's ownership check included), the spam row, the bulk history row
  and the captcha call: a cancelled request returns at once with nothing written, a confirmation
  link stays usable, and a captcha check abandoned by the visitor's request is no longer filed as
  captcha spam. A bulk batch cancelled after its history row is written is recorded as failed by
  a write bounded to 5 s; if the connection or row is still held when that expires, the row stays
  pending and a warning is logged. Conversion, submission, confirmation and the bulk updates are transaction roots: called inside an
  enclosing transaction they now refuse with `utils.ErrTransactionRootRequired` and write or send
  nothing (no caller does so today). Responses to requests that are still connected are
  unchanged, and mail still goes out only after the commit. The conversion settings are still read
  without the request context. AEO runs keep their own lifecycle context.
  `TransactionManager.DB(ctx)` returns the request-bound handle (the active transaction when
  there is one) for statements outside a managed callback.

- **Overflow menus on the lead, customer, user, ticket and task lists (#114).** The "more"
  button sat in the table toolbar and acted on a selected record that nothing ever set, so it
  never opened. Each row now has its own menu, named for the row ("More actions for <name>"),
  that carries only what the row's view/edit/delete icons do not: Convert to Customer on leads
  whose status allows conversion, Create Ticket on customers for admin and support, Activate or
  Deactivate on users (deactivating your own account is disabled, as the API refuses it), and
  Mark as Complete on tasks that are not completed. The ticket list's menu only repeated view,
  edit and delete under the same role rules as the row icons, so it is gone. Every menu acts on
  its own row whatever the sort, filter or page, and a click inside it no longer falls through to
  the row. `DataTable` gains a `rowActions` slot and the new `RowActionsMenu` component.
- **Lead owner changes reach the API (#119).** `leadsApi.updateLead` now sends `owner_id` when the
  caller provides it and still omits it otherwise; the API keeps reassignment admin-only.
- **Role guards on the lead routes and customer creation (#120).** `/leads`, `/leads/new`,
  `/leads/{id}` and `/leads/{id}/edit` and `/customers/new` now sit under an admin-and-sales
  route guard matching the API, so a support or customer deep link lands on Access Denied
  instead of a page whose requests all fail. The customer list hides "Add Customer" from the same
  roles. Customer list, detail and edit routes are unchanged.
- **Paginated lists no longer repeat or skip rows that share a sort value (#110).** The lead,
  customer, task (including the label filter), ticket, user and company lists and searches, and
  the company customer and lead sub-lists, now always end their `ORDER BY` with the table's id:
  ascending id without a sort, otherwise the requested column then id in the same direction, the
  id qualified with the table so the label join stays unambiguous. Before, tied values (every row
  of one import shares `created_at`) came back in whatever order the engine chose per statement,
  and on MySQL consecutive pages repeated some rows and never showed others. The support-scoped
  ticket list used to break ties by ascending id even for a descending sort; it now follows the
  sort direction like the rest.
- **Lead conversion keeps the company link and enforces the conversion settings (#104).**
  `POST /leads/{id}/convert` copies the lead's `company_id` to the new customer when it still
  names a live company (a link to a deleted company is dropped and the conversion still succeeds),
  and now enforces the three lead-conversion settings on the server, before anything is written:
  the lead's status must be in `leads.conversion.allowed_statuses`, non-blank `notes` are required
  when `leads.conversion.require_notes` is on — a refusal is 400 `VALIDATION_ERROR` whose
  `details` name the field (`status` with the allowed statuses, or `notes`) — and with
  `leads.conversion.auto_assign_owner` on the lead's owner becomes the customer's
  `assigned_to_id` (never `user_id`). Before, all three settings were ignored by the server. The
  settings are read on every conversion; a failed read is a 500 with nothing written. The
  allowed-status `valid_values` listed `lost`, which is not a lead status, instead of
  `unqualified`; the list is now `new, contacted, qualified, unqualified`, and the boot-time seeder
  corrects the metadata of existing rows while keeping the statuses an administrator chose.
  `GET /configurations/ui` now also carries read-only `leads.conversion.require_notes` and
  `leads.conversion.auto_assign_owner` entries next to the allowed statuses, with the values the
  server enforces. The conversion is now the only way to reach `converted`: lead create, lead
  update and `POST /leads/bulk/status` refuse it with 400 `VALIDATION_ERROR` (`details.field`
  `status`), where they used to mark a lead converted without a customer and outside the
  settings; a converted lead keeps its status on edit, and the lead form no longer offers
  "Converted". The conversion reads the lead with a row lock and a lead update re-reads it under
  the same lock, so a concurrent status change either lands first (the conversion is refused) or
  after the commit (and keeps the new `customer_id`) instead of being lost or bypassing the
  allowed statuses. A lead that already has a customer is refused as already converted even if its
  status has since moved on (it used to be converted a second time). A lead erased between the
  handler's lookup and the conversion is a 404, not a 500. Refused conversions, duplicate emails,
  already converted or missing leads are logged as warnings by both the handler and the service;
  only server failures are logged as errors. Known gap: a company deleted while a lead converts can still leave the new customer
  linked to the soft-deleted company (tracked in #182).
- **The UI loads its configuration after sign-in and follows the conversion settings (#117,
  #104).** The configuration provider never requested `GET /configurations/ui`, so every page used
  built-in defaults — the Convert button appeared only on `qualified` leads whatever the
  administrator allowed. The provider now loads the list once a session is established (never on
  the sign-in or registration pages, so it cannot start a refresh or redirect there), keyed by the
  signed-in user: signing in loads it, signing out clears it, and switching accounts loads the new
  account's values without ever showing the previous ones (the session now drops cached queries
  before the new account renders, so its first requests are not discarded). The provider, the deal
  form and the default-currency hook share this one authenticated query. Display values (company
  name, primary colour) fall back to built-in defaults; the conversion rules never do: while they
  load, after a failed load, or when the server leaves out the allowed statuses or the notes rule
  (it omits a setting it could not read), the lead page shows Convert to Customer disabled with
  "Loading conversion settings…" or "Could not load conversion settings" and a Retry, the lead list
  shows the row action disabled on every unconverted lead plus a warning with Retry, and the
  dialog claims no rule and does not convert. A save or reset on the Settings > Configuration page
  refreshes the list. Once known, the lead list menu and the lead page offer Convert to Customer on
  the configured statuses (an empty list hides it). The
  conversion dialog requires notes when `leads.conversion.require_notes` is on (blank notes are
  refused before the request, with the server's wording), says that the lead's owner becomes the
  customer's assigned user when `leads.conversion.auto_assign_owner` is on, and stays open on a
  refusal: a notes refusal is shown on the notes field, any other (status not allowed, lead already
  converted, no email) above the form with the server's message, in place of the earlier toast. A
  refusal re-reads the settings (status or notes) or the lead (any other), so a stale page catches
  up, and a re-read keeps what the user typed; when the re-read settings or lead no longer allow the
  conversion, the open dialog says why and disables Convert. Moving from one lead to another closes
  the dialog and drops the previous lead's refusal, also when the user returns to it; a conversion that answers after the user moved on no longer
  opens that customer.
- **Erasure follows the customer login (#108).** A customer record and its own login
  (`customers.user_id` pointing at a `customer`-role account) are now erased together, from either
  end and through a converted lead, with every lead converted into those customers, in one
  transaction. Erasing a login used to leave its customer records holding the person's name,
  email, phone and address; erasing a customer left the login, its password hash and working API
  keys and refresh tokens behind. Staff accounts are never erased this way: a staff user in
  `user_id` is left alone by a customer or lead erasure, a staff user's own erasure leaves the
  customers pointing at it alone, and owners, assignees and account managers are never touched.
  Each person is scrubbed exactly once per erasure, cycles terminate, and a failure anywhere rolls
  back the whole graph. Limits: an erasure can still miss a conversion, link or credential committed
  concurrently (#183); graphs erased before this change are not repaired automatically, and fully
  soft-deleted ones have no supported path yet (`docs/DEVELOPMENT.md` lists finder queries for the
  rest); business free text, legacy `bulk_operation_items` payloads and unlinked form submissions
  are retained by design.
- **Only an admin can delete a converted lead.** Erasing a converted lead erases the customer it
  became (and now that customer's login), and deleting a customer is admin-only, but the owning
  sales user could do it through `DELETE /leads/{id}`. The sales owner now gets 403 for a
  converted lead, decided inside the erasure transaction before anything is written; deleting an
  unconverted lead is unchanged. `LeadService.Delete` takes the caller's role, and the lead
  repository gains `DeleteUnconverted`.
- **An erasure joined to a caller's transaction is all-or-nothing.** Repositories obtained through
  `WithTx` now bracket the erasure in a checked `SAVEPOINT`, released on success and rolled back on
  an error or a panic; a caller that handled a failed erasure (or recovered its panic) and
  committed used to keep its partial writes (some rows scrubbed, some credentials purged). A failed
  `ROLLBACK TO SAVEPOINT` rolls the caller's transaction back and is reported with
  `ErrRollbackFailed` and `ErrTransactionPoisoned`. The savepoint statements go to the underlying
  transaction, so they also work with GORM's `PrepareStmt` on MySQL, which cannot prepare them.
- **Soft-deleted ticket comments are redacted on user erasure.** The redaction skipped comments
  that had been soft-deleted earlier, which kept their bodies.
- **The session that changes the password stays signed in (#116).** `POST /auth/change-password`
  is now one transaction: the owner row is locked, the hash it was verified against is re-checked,
  the caller's current refresh token (new optional `refresh_token` body field) is claimed, the new
  hash written, every other refresh token revoked and the replacement session issued. Any failure
  before the commit rolls all of it back — the revocation is no longer best-effort after a committed
  password (a failed COMMIT stays ambiguous, as for every transaction). The
  response carries `token`, `refresh_token` and `user` when the session was renewed; a presented
  token that is not a live session of the caller is `409 SESSION_STALE`; API-key requests never
  receive session credentials. The Profile page stores the replacement pair in the storage the
  session already used and no longer promises that other devices are signed out immediately
  (their access tokens keep working until they expire). Refresh rotation now locks the owner row
  before claiming the token, the order every session transaction follows, so a password change
  and a concurrent refresh can no longer deadlock on MySQL/MariaDB.
- **The 401 refresh interceptor settles every request (#115).** One shared refresh per session;
  every waiting request is resolved or rejected on every outcome; every replay is marked so a
  second 401 is final; a refresh that does not answer within 15 s rejects its waiters on its own;
  requests are tagged with the session they were sent under and never replayed, nor their late
  refresh stored, after logout or a sign-in to another account. A refresh rejected with 400/401
  ends the session; a timeout, network error or 5xx keeps the stored tokens. `/auth/refresh`
  answers operational failures with `503 SESSION_REFRESH_UNAVAILABLE` instead of the generic 401,
  so a transient database error no longer signs the user out. Logout drops the stored tokens at
  once, waits for a refresh already on the wire and authenticates the server-side revoke-all with
  the successor of a refresh or password change still running (kept privately, never stored, and
  only when it belongs to the session being ended) or, when its access token has expired, with a
  private renewal. Every operation is bound to the session it was requested under (generation,
  account and refresh-token lineage), so another tab signing out or signing in another account
  never makes this tab send, write or revoke as the wrong account. The client reads the refresh token only from
  the storage that holds the access token and a new sign-in replaces both keys. Cross-tab refresh
  coordination remains open (#184).
- **A login paused across an erasure can no longer restore the erased account.** Login
  bookkeeping, password writes and profile updates wrote the whole user struct with GORM `Save`,
  which falls back to an upsert when its update matches no row: a login that read the account
  before an erasure wrote the name, email and password hash back and cleared `deleted_at`. User
  writes are now conditional, column-specific updates of a live row (`UserRepository.UpdateFields`;
  the full-struct `Update` is gone), and such a login is refused. Re-verifying the password under
  the owner lock at login is tracked in #183.

- **An automation rule update no longer undoes a concurrent disable.** `PUT
  /automation/rules/{id}` without `enabled` now keeps the switch as read under the rule's row lock
  inside the update transaction; before, the handler read the rule first and wrote that value
  back, so a disable committed in between was silently reverted.
- **`make swagger` resolves the notification handler's types.** The handler now imports the
  models package its annotations reference, so the spec generates again.

- **Runner recovery and gate gaps.** Every `gocrm-ui/vitest.*` file is now an existing
  validation gate, because Vitest discovers `vitest.workspace.*` and `vitest.projects.*` beside
  its configuration; previously only `vitest.config*` was protected. `retire-issue` now also
  requires `--processes-stopped` when a validation command is recorded as running, and records
  its disposition in `check_dispositions`. A Vitest receipt now records the validation checkout
  that produced it and is verified against that checkout, so a receipt reused across two
  interrupted resumes no longer fails the exact-path match and discards every retained check.
  A failed `gh` invocation (for example a TLS handshake timeout against the GitHub API) is now
  an environment failure: validation stops at its stage, records the attempt as `environment`
  and is resumed by the same command, instead of treating the failure as a test failure and
  spending a provider call and a repair round on an agent with nothing to change.
- **The remediation runner no longer strands an implementation that changes a path outside the
  frozen task scope.** Finalization checked the frozen scope before committing, so a single
  out-of-scope edit left the agent's work uncommitted at `finalize_implementation`, where `run`
  repeated the same refusal, `freeze --refresh` required a revalidation first and `revalidate`
  refused both the stage and the dirty worktree. Protected and private paths and changed
  validation gates are still refused before anything is written. The scope check now runs after
  the commit: the implementation and its claims are kept on the runner branch, `last_error`
  records the violation and the execution stops at `validate`, where the same check still blocks
  testing, review and publication. The operator inspects the diff and either widens the scope
  (`revalidate` when main has advanced, then `freeze --refresh` and `run --execute`, which
  validates the existing commit without another implementation call) or leaves the execution
  for an explicit disposition.
- **Feature analysis checks anticipate the execution gates.** The specification and revision
  stages now report context references that are not Markdown, are missing at the base commit or
  exceed the 128 KiB guidance cap; Go test-plan names that are not bare identifiers or appear in
  more than one registry entry; `./cmd/migrate` registry entries whose `-run` pattern and tests
  do not cover `TestMigrationFresh_FullUpAndDown` and
  `TestMigrationUpgrade_FromMainAutoMigratedSchema`; and migration or CHANGELOG changes whose
  scope omits `cmd/migrate/*_test.go` or `CHANGELOG.md`. An over-budget one-pager note states
  the excess and the largest fields, and the revision reports a one-pager it did not shrink. The
  stage rules state each requirement.
- **The remediation runner waits for an in-progress workflow run before it collects failed-job
  logs.** A required check that failed while other jobs of the same run were still running made
  the runner request the run's failed-job logs, which `gh` refuses until the run completes; the
  ticket or PR completion then stopped on that refusal instead of entering its repair loop. The
  runner now waits, within the CI deadline, until every failed run has completed and then fetches
  the logs once.
- **Sorted customer CSV exports no longer lose rows (#105).** `GET /customers/export` read the
  book in 500-row primary-key batches through GORM's `FindInBatches` while also applying the
  requested `sort_by`. The batch continuation is `id > last id of the previous batch`, which is
  only a valid cursor under id order: with any other `sort_by`, every customer whose id was below
  the previous batch's last id was skipped, so a sorted export of more than 500 customers silently
  came back incomplete while looking like a whole file. The repository now reads every matching row
  with one ordered statement, ordered by the requested column and then by id, both in the requested
  direction (id ascending without `sort_by`), so duplicate values, NULLs in the legacy nullable
  `company` column and collation ties order deterministically, `desc` is the exact reverse of
  `asc`, and the read is one consistent snapshot on MySQL and SQLite. The sort allowlist,
  invalid-parameter handling, search filter, erasure exclusion, CSV header and quoting, formula
  prefix, admin-only guard and the all-or-nothing fetch before the response are unchanged.
  Regression roots: `TestCustomerExportTraversal{SQLite,MySQL}` in
  `internal/repository/customer_export_traversal_test.go` and `TestCustomerExportCSV{SQLite,MySQL}`
  in `tests/customer_export_csv_test.go`, both on more than 1,000 rows whose sort values run
  against id order with duplicates straddling the old batch boundaries; the MySQL roots use a
  disposable schema on `TEST_MYSQL_DSN`.
- **Refresh token rotation is atomic and single-use (#103).**
  `POST /auth/refresh` used to read the presented token, revoke it with an unconditional update
  and insert its replacement as independent writes: two simultaneous requests could both pass the
  lookup and both mint a successor, and a failure after the revocation stranded the session with
  no usable token. The rotation is now one transaction through the transaction manager and the
  `WithTx` repositories: the token is claimed by a new conditional
  `RefreshTokenRepository.ClaimForRotation` (not revoked, strictly unexpired, exactly one affected
  row), the owner is re-read with the same locking read as the password reset (so an account
  disabled by another MySQL connection right after the token lookup is refused instead of served
  from the stale snapshot), the successor is inserted and its access JWT signed before the commit.
  A failure of the insert or of the signing rolls the claim back, so the presented token stays
  usable, no extra row exists and no credentials are returned; competing refreshes of one token
  commit exactly one successor and the losers and any later replay receive the generic 401
  without touching the winner. An inactive, erased or missing owner is still refused generically
  and still has the presented token revoked: that containment revocation is committed with the
  rejection and no successor is created. Logout keeps its unconditional, idempotent
  `RevokeByTokenHash`; a rotation is refused rather than run as separate writes when the
  transaction manager is not wired. Public errors and API responses are unchanged. Regressions run
  on SQLite and on a disposable MySQL schema
  (`TestRefreshRotation{Validation,Atomicity,Concurrent}{SQLite,MySQL}`), with the insert fault
  injected through the transaction-bound repository and the signing fault through an
  instance-local seam; see `docs/testing/01-authentication.md` TC-AUTH-033a. Client-side
  refresh coalescing is the #115 entry above.
- **Password reset confirmation is atomic and single-use (#102).**
  `POST /auth/password-reset/confirm` used to mark the token used by an unconditional update
  before hashing and writing the password, and revoked sessions only best-effort afterwards: a
  failure after the stamp burnt a usable link, two simultaneous confirmations could both pass the
  lookup, and a failed revocation left stolen sessions alive next to the new password. The new
  password is now validated and hashed before anything is written, so a weak or unhashable
  password leaves the link usable. The token is then claimed by a conditional update (unused,
  strictly unexpired, exactly one affected row) in the same transaction that re-checks the owner
  is active and not erased, writes the password, clears the lockout and revokes every refresh
  token. The owner re-check is a locking read (the transaction-bound `UserRepository`): on MySQL a
  plain read inside the transaction answered from the snapshot taken at the token lookup, so an
  account disabled by another connection right after that lookup still looked active and the
  password update wrote the stale `is_active` back. Any failure rolls the whole unit back and
  the same link works on retry; competing
  confirmations commit exactly once with the winner's password; a confirmation is refused rather
  than run as separate writes when the transaction manager is not wired. Public errors and API
  responses are unchanged. Regressions run on SQLite and on a disposable MySQL schema
  (`TestPasswordReset{Validation,Atomicity,Concurrent}{SQLite,MySQL}`, plus the two-connection
  `TestPasswordResetOwnerDeactivatedAfterTokenReadMySQL`); see
  `docs/testing/01-authentication.md`.

- **Runner validation progress is retained and failed checks are recoverable (#146).**
  Early Go assertion diagnostics are preserved when later passing tests fill a failed
  command's log, and expected structured service logs are distinguished from real
  environment failures. A shared, commit-pinned `repair-validation` recovery command queues
  an inspected source repair while retaining failed attempts, spent budgets and all
  validation/review gates. Validation resume verifies and retains successful checks for the
  unchanged candidate, then runs missing checks in a fresh checkout instead of repeating
  completed suites. Unusable checkpoints are retained with their invalidation reason while
  every selected check runs again under a new validation identity and the original limits.
  Both JSON and text-format service logs are recognized. See `automation/langflow/README.md`
  for the operator procedure.

- **Form confirmation tokens are consumed atomically (#101).** Confirming a double-opt-in
  submission used to stamp the token as used before the transaction that created or linked the
  lead and confirmed the submission, by an update on the id alone. A failure in that transaction
  consumed a usable link, and two simultaneous clicks could both pass the lookup. The token is now
  claimed inside the same transaction by a conditional update (unused, strictly unexpired, exactly
  one affected row) before the pending submission is re-read and the lead and submission are
  written; a lead or submission write failure rolls the claim back so the same link works on
  retry, competing requests yield exactly one confirmation, and the team notification and the
  follow-up mail go out only after the commit. Public errors and API responses are unchanged.
  Regressions run on SQLite and on a disposable MySQL schema (`TestFormConfirmation{TokenClaim,
  Rollback,Concurrent}{SQLite,MySQL}`); see `docs/testing/12-forms.md`.

- Constrain runner implementation reports to the frozen acceptance IDs before provider
  generation. Descriptive criterion strings, unknown IDs and duplicates are rejected by
  both the response schema and parser; existing PRs without criteria claim an empty list.
  This prevents completed edits being stopped by a schema/parser mismatch. Tests and
  independent review remain the acceptance evidence.

- **Transaction manager: panics, nesting and finalization failures (#106).** A panic inside
  `WithTransaction` now rolls the transaction back before propagating unchanged, so SQLite's
  single pooled connection is released instead of held by an abandoned transaction. A call made
  inside an active transaction reuses it under a savepoint rather than beginning a second root
  transaction (which blocked forever on that pool): an inner failure rolls back to its savepoint
  while the outer unit may still commit, and an outer failure discards every nested write. Commit
  and rollback failures are reported with `errors.Is`-inspectable sentinels alongside the
  callback's own error, a rollback already performed by context cancellation is not an error
  (a nested call whose enclosing context ended reports the cancellation, not a failed cleanup),
  and `WithTransactionAndRetry` replays only attempts that rolled back cleanly, never an
  ambiguous commit; inside a transaction it runs once and defers a retryable failure to the
  enclosing root retry. A negative retry count is rejected without running the callback. A failed
  `ROLLBACK TO SAVEPOINT` poisons the transaction (`ErrTransactionPoisoned`): an outer callback
  that recovers the nested panic or swallows the inner error and returns nil no longer commits the
  possibly retained inner writes; every enclosing call returns the cleanup error, new nested calls
  are refused, and the root rolls back instead. See the transaction section of
  `docs/DEVELOPMENT.md`.
- Expired login lockouts now start a fresh failure window instead of relocking on the
  first mistyped password; active locks and generic public authentication failures remain enforced.
- **AEO profile updates on MySQL.** Saving an existing brand profile preserves its
  creation timestamp instead of writing a zero date, which strict MySQL rejected with a 500.
  Saves return the stored timestamps and continue to allow clearing optional fields.
- **SQLite auto-migration on a populated file.** Adding a foreign key to an existing table makes
  the SQLite driver drop and recopy the table, which foreign-key enforcement rejected as soon as
  another table referenced its rows: a 1.2.0 file upgraded only if no row in `tickets` or
  `tasks` and no converted lead referenced a customer or lead, which rules out any database that
  has been used. Auto-migration on SQLite now runs with enforcement off, as SQLite prescribes for
  table rebuilds, verifies every foreign key afterwards and refuses to start on a violation. A
  fixture test upgrades a populated v1.2.0 file, including one left half-applied by a failed
  attempt; MySQL and MariaDB are unchanged.

### Security

- **Changing your own login email needs your current password (#394).** The login email is where
  a password reset is sent, and until now `PUT /users/me` and `PUT /users/{id}` moved it for
  whoever held a live access token or API key of the account. Both routes now refuse an email
  change of the caller's own account unless the body carries `current_password`:
  - no usable `current_password` (key absent, `null` or `""`) answers 400
    `CURRENT_PASSWORD_REQUIRED`; a value that does not match answers 400
    `INVALID_CURRENT_PASSWORD`. Nothing is written in either case (no name beside the email
    either), and the check runs before the address is looked up, so the answer does not tell
    whether the address is in use.
  - The rule holds for a Bearer token and for an API key, on `PUT /users/me` and on
    `PUT /users/{id}` with the caller's own id, also when that body carries `role` or `is_active`.
  - **API clients and API-key integrations** that change their own account's email must add
    `"current_password": "<the account's password>"` to the body. Nothing changes for an update
    without `email`, for one that sends the stored email again, or for changing **another**
    account's email with `users.update`: there `current_password` is ignored and not verified.
  - `current_password` alone on `PUT /users/me` is therefore no longer answered
    `PASSWORD_FIELD_NOT_ACCEPTED`; `password` and `new_password` still are, also next to a correct
    `current_password`. A `current_password` that is not a JSON string is 400 `BAD_REQUEST`.
  - In the web application the user edit form (`/users/<own id>/edit`) shows a required
    **Current password** field as soon as the signed-in user changes its own email, and sends it.
    `/settings/profile` still has no email field.
  - Known limits: a wrong `current_password` is not counted toward the login lockout, as on
    `POST /auth/change-password` (same authenticated rate tier); an email change revokes no
    session and sends no confirmation mail.

- **The example `JWT_SECRET` is refused at startup (#210).** `JWT_SECRET` values starting with
  `CHANGE_THIS`, which includes the `.env.example` placeholder, are refused at startup in every
  `SERVER_MODE`, by the server, `cmd/migrate` and `cmd/create-admin`. **A deployment still on the
  placeholder stops at its next restart and must set a new secret.** Replacing `JWT_SECRET` always
  invalidates issued access tokens. When `API_KEY_SECRET` is unset or also rotated, it also
  invalidates API keys, refresh tokens, reset links and pending form confirmations, and leaves
  stored AEO provider keys unreadable. With a separate, unchanged `API_KEY_SECRET`, refresh tokens
  stay valid. See "Choices that are irreversible or easy to get wrong" in `docs/FORKING.md`,
  section 4.2.

- **`PUT /users/me` no longer changes a password (#254).** It used to set a new password without
  the current one and without revoking any refresh token. A body that carries `password`,
  `current_password` or `new_password`, with any value (`null` included) and in any letter case,
  is now refused with 400 `PASSWORD_FIELD_NOT_ACCEPTED` and writes nothing, not even the profile
  fields beside it, and a correct current password does not open the route. A body that also fails
  binding keeps its `VALIDATION_ERROR` or `BAD_REQUEST` answer. **API clients that set a password
  on this route must move to `POST /auth/change-password`**, the only authenticated path, which
  verifies the current password and revokes refresh tokens. `UserService.Update` ignores a
  `password` key, and `PUT /users/{id}` still ignores one in its body. Profile updates without a
  password field behave as before.

- **SMTP delivery is bounded and validates its headers (#113).** The SMTP mailer no longer calls
  `smtp.SendMail`, which had no timeout: a relay that stalled at the greeting or any later command
  held the calling request or worker indefinitely. Each delivery now dials within 10 s and runs
  the whole conversation (greeting, EHLO, STARTTLS, AUTH, MAIL/RCPT/DATA, QUIT) under one 30 s
  deadline that is a hard bound: the raw connection is closed on every path and, at the latest,
  when the deadline passes, so TLS cleanup cannot add its own grace period. STARTTLS stays
  opportunistic — used whenever the relay advertises it, and a failed handshake aborts the
  delivery; PLAIN credentials are never sent over an unencrypted connection to a host other than
  localhost, and configured credentials against a relay that offers no AUTH fail instead of
  submitting unauthenticated. The recipient and the configured `SMTP_FROM` must each be one RFC
  5322 address (display names allowed). The envelope address is serialised safely: a dot-atom
  local part as is, any other local part re-quoted (`RCPT TO:<"a b"@example.com>`) — `net/mail`
  returns quoted local parts unquoted, so `"x> NOTIFY=NEVER"@example.com` would otherwise have
  reached `RCPT TO` as extra ESMTP parameters. Control characters, `<` or `>` anywhere in the
  address (even quoted) and domain literals are refused with `mailer.ErrInvalidAddress` before any
  relay is contacted; an internationalised address is delivered only when the relay advertises
  SMTPUTF8 and otherwise refused before `MAIL FROM`. CR or LF in the sender is refused like the recipient and subject
  (`mailer.ErrInvalidHeader`), the envelope carries the bare address (previously a recipient with
  a display name produced a malformed `RCPT TO`), and a non-ASCII subject is RFC 2047 encoded and
  folded into lines of at most 76 characters. A relay reply such as a 535 is returned as it is even
  when the abort that follows runs into the deadline; only I/O failures caused by the deadline are
  reported as `os.ErrDeadlineExceeded`. The body, declared UTF-8, is now sent quoted-printable instead of raw 8-bit, with every
  line ending (including a bare CR) normalised to CRLF and long lines soft-wrapped. Failure logs
  no longer carry the subject (automation subjects can expand `{{lead.email}}`) or, for the
  password reset, the recipient; transport errors go through `utils.LogError`, which now also
  reduces a malformed relay reply (`textproto.ProtocolError`) to `smtp_protocol_error`.
- **Service and HTTP logging privacy.** The sample `LOG_LEVEL` is now `info`. Service logs omit
  emails, names, titles, search terms and raw sort values, while shared helpers omit argument,
  result and handler response payloads (including issued credentials). Access logs omit query
  strings. Logged errors redact quoted value suffixes, form validation logs summarize the field
  count, and SMTP failures retain the protocol code without relay response text. Operation names,
  IDs, validated statuses, counts and request timings remain. API responses and returned errors
  are unchanged; other raw diagnostics, request/header/IP metadata, mailer recipients and older
  logs still require a retention policy.
- **The SQL log follows `LOG_LEVEL` and never carries parameter values.** The GORM logger was
  fixed at its most verbose level, so every statement went to stdout with its bound values
  (email addresses, names, token hashes) whatever `LOG_LEVEL` said. Statements are now logged
  only at `debug` or `trace`; every other level logs failed and slow statements only. At every
  level statements are printed with `?` placeholders instead of the bound values, and quoted
  values in driver error messages (MySQL's `Duplicate entry 'alice@example.com'`) are redacted
  as well. A record-not-found result is no longer logged as a failed statement.

### Upgrade notes

- **Back up the database, then upgrade and apply the SQL migrations.** 1.3.0 adds 42 migration
  pairs, from `20260929120000_add_companies` to `20261129120008_refresh_token_provider_session`.
  The backend auto-migrates at every start. On MySQL/MariaDB the SQL half is applied with
  `bin/migrate -command up` after the deploy reports the revision live; on the release-tarball
  path that step is manual, and `docs/SERVER_PROVISIONING.md` gives the exact command under
  "Schema migrations".
- **Expect API keys to expire within a year and customer keys to be revoked.** Migration
  `20261129120003_api_key_lifetime`, and the same step at every startup, gives every key without an
  expiry, or with a later one, an expiry one year from the upgrade and revokes every key of a
  customer account; its down half changes nothing. Creating a key now needs a sign-in within the
  last 15 minutes.
- **Single sign-on and SCIM settings become admin-only.** `settings.sso_manage` and
  `settings.scim_manage` are removed from every custom role at startup and by
  `20261129120001_admin_only_sso_scim_permissions`, with one `role_updated` audit row per changed
  role; the rollback restores nothing.
- **Stop every replica before starting 1.3.0.** The quote quantity change
  (`20261118120000_store_quote_line_quantity_milli`), the automation run leases and the AEO run
  ownership require it. To roll back to an older binary, first run the quantity down migration on
  MySQL/MariaDB, or restore the pre-upgrade backup on SQLite.
- **Point `QUOTE_PDF_CHROME_PATH` at the image's launcher.** The Docker image's default is
  `/usr/local/bin/quote-pdf-browser`. An override naming `/usr/bin/chromium-browser` or the old
  `chromium-headless-shell` paths switches PDF output off. A custom launcher must pass file
  descriptors 3 and 4 through.
- **Review the configuration before starting.** The server refuses to start with
  `DISABLE_RATE_LIMIT=true` and `SERVER_MODE=production`. `JWT_COOKIE_SECURE`, `JWT_COOKIE_DOMAIN`
  and `JWT_COOKIE_SAMESITE` are no longer read. The settings `security.session_timeout_hours` and
  `tickets.auto_assign_support` are deleted (`20261031120000_retire_inert_configurations`).

## [1.2.0] - 2026-09-25

### Added

- **SQLite backend.**
  - `DB_DRIVER` selects `mysql` (the default) or `sqlite`, and `DB_PATH` names the SQLite file. An
    empty `DB_PATH`, or one containing `?`, stops startup.
  - SQLite runs on a pure-Go driver with WAL, foreign keys and a busy timeout. Shutdown never
    hangs on a stuck query, transactions follow the request context, and lock errors are retried.
  - The test suites run on the same driver that ships.
  - `docker-compose.sqlite.yml` runs the backend and UI without a MySQL service; `docs/DOCKER.md`
    covers backups.
- **Profile and API Keys settings pages.**
  - **Profile** shows the signed-in account and changes its password. Changing it revokes every
    refresh token, so no session can be renewed; access tokens already issued stay valid until
    they expire.
  - **API Keys** lists your keys, creates new ones (the key is shown exactly once, behind a copy
    button) and revokes them in place.
- `/health` reports `revision`, the commit the running binary was built from.
- **Automated production deploy** (`.github/workflows/deploy.yml`, `scripts/deploy/`).
  - When CI passes on `main`, the tested commit is built and shipped over SSH to a single forced
    command on the server.
  - That command backs up the database, switches releases atomically, waits for `/health` to
    report the new revision, and rolls back on its own if it does not.
  - `scripts/deploy/bootstrap.sh` sets up a server once. Manual rollback and status checks run from
    the Actions tab.
- **Continuous integration on every pull request.**
  - Checks: repository hygiene, backend build/vet/race tests, frontend build/lint/tests, and the
    Playwright end-to-end suite on both MySQL 8 and MariaDB 10.11.
  - `make verify` runs every CI gate except the end-to-end suite. `make e2e` runs that suite
    locally against a throwaway `*_e2e` database on free ports.

### Changed

- **A lead needs either an email address or a phone number.**
  - Before, the API required an email and the lead form required both. A lead with neither gets
    a 400.
  - API consumers can now receive leads with an empty `email`.
  - A lead without an email can't be converted to a customer (customers need one). The conversion
    answers 400 and says to add an email first.
  - Update treats an empty field as untouched.
- **Forms:** when `first_name` and `last_name` are both absent, a single `name` field is split
  onto them, so leads are no longer named `Form <email local part>`. The `name` value is no longer
  copied into the lead's notes.
- **Form definitions** report each field's `max_length` capped at the width of the lead column it
  maps to: email 255, first and last name 100, phone 50, company 200, position 100. Existing forms
  that declared more publish the lower value. Field names and options do not change.
- The AEO documentation screenshots use demo data with GopherCRM as the tracked brand.

### Removed

- `DB_SSL_MODE`. It was read into the configuration but never used.

### Fixed

- **API keys could not be created on MySQL or MariaDB.** The hash column was too narrow and the
  insert failed. `api_keys.key_hash` is now `varchar(128)`.
- **Lead conversion:**
  - a lead whose email already belongs to a customer answers 409 instead of 500;
  - the lead page shows the server's message when a conversion is refused.
- **Form submissions:**
  - a value wider than the column it is stored in is rejected with a field error ("X must be at
    most N characters long") instead of failing with a 500 on MySQL and MariaDB;
  - a pending double opt-in whose stored data holds such a value is refused at confirmation;
  - the raw submission is stored as `MEDIUMTEXT`, so large multi-field submissions fit.
- **Lead notes** are capped at 65,535 bytes, the width of the column.
  - Create and update reject longer notes with a 400.
  - Repeated form submissions still append to a lead's notes. Each appended block is capped at
    16 KiB, marked when cut, and closed by an end line.
  - When space runs out, only complete form blocks are dropped, oldest first. Every other byte is
    kept wherever it sits: staff notes before, between or after blocks, and blocks written before
    this release. Public submissions can therefore never erase what staff wrote.
  - If the kept text leaves no room, a one-line pointer is appended instead, or nothing when not
    even that fits.
  - The full submission always stays with its form.
- **AEO:**
  - a citation URL over 1,024 characters or a host over 255 is skipped and logged, instead of
    losing the whole recorded answer;
  - an engine whose configured name (`AEO_CUSTOM_NAME`, 40 characters) or model id
    (`AEO_*_MODEL`, 120 characters) does not fit its column is left out of runs, with an error log
    naming the setting.
- **Configuration seeding** failed on MySQL (`NO_ZERO_DATE`, Error 1292) for older rows with no
  `created_at`. The whole seeding transaction rolled back on every boot, so new defaults were
  never inserted.
- **Lists:** the sort order survives refetches, and descending order works again. Columns the
  backend cannot sort no longer offer sorting.
- **Rendering errors:** an error in one route shows a recoverable fallback instead of blanking the
  app. Missing or invalid dates render as a placeholder instead of crashing the page.
- **Dependencies:** `@mui/utils` is declared as a direct dependency, so strict installers such as
  pnpm resolve it (#54).

### Upgrade notes

- **Schema changes.** Auto-migration at startup widens two columns on MySQL and MariaDB:
  `api_keys.key_hash` becomes `varchar(128)` and `form_submissions.data` becomes `MEDIUMTEXT`.
  - Existing rows and the unique index are kept; this was verified on MySQL and on MariaDB 10.11.
  - Take a database backup first. The automated deploy does this for you.
  - If you apply SQL by hand, `migrations/20260923120000_widen_api_key_hash.{up,down}.sql` covers
    `key_hash`. `form_submissions.data` is widened by auto-migration only.
- **Configuration.**
  - Remove `DB_SSL_MODE` from your environment if you set it; it has no effect.
  - An unknown `DB_DRIVER` value now stops the server at startup.
  - `cmd/migrate` refuses database commands under SQLite, where the schema comes from
    auto-migration.
- **Docker.** The image now runs in `/data`, which is also where `godotenv` looks for `.env`.

## [1.1.0] - 2026-08-12

### Added

- **Forms module.**
  - Build contact, quote-request and lead-capture forms in the CRM. Embed them on any website with
    one script tag, or share the hosted form page.
  - Submissions create leads, or match existing ones, automatically.
  - **Double opt-in:** the visitor confirms their email through a single-use link.
  - Layered spam protection: honeypot, minimum-fill-time challenge, per-IP rate limits, a per-form
    origin allowlist, and optional reCAPTCHA v3.
  - The embeddable widget has no dependencies, can be themed through CSS custom properties, and is
    served by the backend.
  - Erasing a lead also scrubs its linked form submissions and confirmation tokens.
- **API keys managed in Settings.**
  - AEO provider keys are encrypted at rest (AES-256-GCM), never echoed back by the API, and take
    effect without a restart. Environment variables remain the fallback.
  - The configuration system gained a generic mechanism for sensitive values.
- The AEO prompt-generation engine is selectable per deployment.
- A single AEO prompt can be run on demand from its drawer, including inactive drafts.

### Changed

- AEO answer transcripts render as markdown, with brand mentions still highlighted.
- AEO errors distinguish a missing key from a rejected one.

### Fixed

- Reasoning-class models get enough completion budget to finish their answers, instead of stopping
  mid-sentence.
- Gemini defaults to the rolling `gemini-flash-latest` alias, which fixes 404s from a retired
  pinned model.
- Boot-time configuration seeding no longer overwrites stored values with defaults.
- Public form endpoints answer cross-origin requests with credential-less CORS; every other route
  keeps the strict allowlist.

### Upgrade notes

- **No breaking changes.** Schema additions (the forms tables and configuration flags) apply
  through auto-migration at startup.
- **New optional environment variables:** `PUBLIC_BASE_URL`, `RECAPTCHA_SITE_KEY`,
  `RECAPTCHA_SECRET_KEY` and `RECAPTCHA_MIN_SCORE`. See `.env.example`.

## [1.0.0] - 2026-08-11

First tagged release. It shipped:
- **Answer Engine Optimization (AEO):** brand visibility tracking across six answer engines, with a
  dashboard, prompt transcripts and citation comparisons;
- **task labels**;
- **the Docker Compose stack**, with `CORS_ALLOWED_ORIGINS`.

The GitHub release describes these in full. The entries below were collected before the release
was tagged, and do not cover those three.

### BREAKING

- Deleting a **user, customer or lead** is now irreversible. `DELETE` overwrites every personal
  field on the row before soft-deleting it, so there is nothing left to restore. Use
  `is_active = false` to suspend an account without destroying its data. Deleting a ticket or a
  task is unchanged and remains an ordinary soft delete.
- The erased email address becomes reusable, because the original no longer exists in the table.
  Code that assumed a deleted account's address stayed permanently reserved will behave differently.
- `POST /auth/register` no longer honours a `role` field in the request body. Clients that relied on
  it to provision non-customer accounts must use the admin-guarded `POST /users` or the
  `create-admin` CLI.

### Added

- Right-to-erasure implementation (GDPR Art. 17) in `internal/repository/erasure.go` and
  `erasure_cascade.go`. Personal fields are overwritten in place and the row is soft-deleted in a
  single transaction; the row is kept so foreign keys from tickets and tasks still resolve.
- Replacement email addresses generated from `crypto/rand` in the reserved `.invalid` domain
  (RFC 2606). They are not derived from the original address, so they are neither reversible nor
  linkable.
- Cascading erasure between a converted lead and its customer, in both directions, via
  `leads.customer_id`. Wired through `repository.NewCustomerRepositoryWithLeadErasure`.
- Per-item `SAVEPOINT` isolation in bulk deletion, so a failing item neither commits a half-erased
  live record nor rolls back the rest of the batch. Erasure refuses to run when GORM's nested
  transactions are disabled.
- `scripts/anonymize_legacy_deleted_pii.sql` to remediate rows soft-deleted before erasure existed.
  Manual and irreversible by design; deliberately not wired into auto-migration.
- Test coverage for erasure, atomicity, cross-table PII sweep, bulk isolation and duplicate-email
  semantics: `test/integration/erasure_test.go`, `lead_erasure_test.go`,
  `erasure_atomicity_test.go`, `erasure_pii_sweep_test.go`, `bulk_erasure_test.go`,
  `bulk_erasure_isolation_test.go`, `soft_delete_email_reuse_test.go`.
- Handler unit tests for auth and API keys (`internal/handler/auth_handler_test.go`,
  `apikey_handler_test.go`) and a bulk persistence suite
  (`internal/service/bulk_operation_persistence_test.go`).
- Frontend tests for token storage, route protection and the registration password policy
  (`src/api/client.test.ts`, `src/components/ProtectedRoute.test.tsx`,
  `src/routes/index.test.tsx`, `src/pages/auth/Register.validation.test.tsx`).
- `gocrm-ui/e2e/global-setup.ts` provisions the E2E admin account through the `create-admin` CLI,
  since registration can no longer create an admin.
- **Auth session lifecycle.** Login now issues a rotating refresh token alongside the JWT;
  `POST /auth/refresh` exchanges it (strict rotation — replaying a used token returns 401);
  `POST /auth/logout` revokes the caller's refresh tokens; `POST /auth/change-password` verifies
  the current password and revokes all sessions; `POST /auth/password-reset` +
  `/password-reset/confirm` implement a single-use, 1-hour, anti-enumeration reset flow. Tokens are
  stored only as HMAC-SHA256 hashes. Mail goes through a new `internal/mailer` package — SMTP via
  `SMTP_*` env vars, with a redacting log fallback for development. Erasing a user also purges
  their password-reset tokens.
- **Dashboard analytics.** Eight new admin/sales/support endpoints: grouped counts for leads by
  status, tickets by priority and tasks by status; lead conversions over time
  (`sales-performance`, bucketed in Go so SQLite and MySQL agree); an activity feed synthesized
  from lead/ticket/task events; upcoming tasks; recent tickets; and new leads (scoped per role).
  Plus `GET /tasks/upcoming` for a forward due-date window.
- **Bulk status updates.** `POST /leads|tickets|tasks/bulk/status` (up to 100 ids,
  all-or-nothing in one transaction) wired onto the previously unrouted bulk machinery, with
  per-item authorization mirroring the single-item rules and failing responses that name the
  offending ids.
- **API key management.** `GET` and `PUT /api-keys/{id}` (rename, deactivate, reactivate), an
  optional `expires_at` on creation, and expiry enforced at authentication time.
- **Customer operations.** `GET /customers/export` streams a CSV of all matching customers
  (admin-only, spreadsheet-formula-injection-safe) and `POST /customers/{id}/assign` assigns a
  customer to an active admin/sales user via the new `assigned_to_id` column, which erasure
  deliberately leaves intact.
- A regenerable Swagger 2.0 spec at `api/swagger.json` / `api/swagger.yaml`, built by `make swagger`
  from swag annotations now covering all 43 routed operations. Status codes, permission rules and
  response shapes were derived from the actual code paths, including the unflattering ones (see the
  API-defects list in `docs/ROADMAP.md`).

### Changed

- API keys and refresh tokens belonging to an erased user are purged with the account.
- Deactivation (`is_active = false`) is documented and tested as the reversible alternative to
  deletion; it never touches personal data.
- Frontend tokens are stored in `sessionStorage` by default and only move to `localStorage` when
  "remember me" is selected.
- The registration form's validation now mirrors the server password policy: minimum 10 characters
  with an uppercase letter, a lowercase letter, a digit and a special character.
- The hand-written documentation moved from `doc/` into `docs/`; there is now a single
  documentation directory.
- `remember_me` is gone from the login request body. It was bound and never read — token lifetime
  comes solely from `JWT_EXPIRY_HOURS` — and the frontend's remember-me checkbox only ever chose
  the client-side storage location, which is unchanged. Clients still sending the field are
  unaffected (unknown JSON keys are ignored).
- `PUT /configurations/{key}` binds its value as `json.RawMessage`, making present-vs-absent
  structural: `false`, `0` and `""` are accepted (they already were, but only via a
  version-sensitive quirk of the validator's interface handling), while an absent or `null`
  value is rejected with an explicit 400.
- Password fields in register and user create/update requests declare `min=10` at binding time,
  matching the complexity rule that was already enforced afterwards. 8–9 character passwords were
  always rejected; they are now rejected at validation with an accurate message (and the generated
  spec no longer claims a minimum of 8).
- The old generated `docs/` Swagger package (`docs.go`, `models.go`, `swagger.json`,
  `swagger.yaml`) is gone, along with the direct `swaggo/swag` dependency — it was generated in
  March, imported by nothing, and documented removed behaviour (a client-supplied `role` on
  register, a CSRF token on login). The CLI now runs via `go run` pinned in the Makefile.
- Backend statement coverage is now 46.9% (measured with
  `-coverpkg=./internal/...,./cmd/...`), up from a previously reported 41.3%. The frontend suite is
  now 142 tests across 16 files, all passing, up from a previously reported 100 passing with 11
  failures. Backend tests are clean under `-race`.

### Fixed

- Wrong status codes for missing records, all rooted in string-compared errors or gorm's sentinel
  leaking through unclassified: `DELETE /api-keys/{id}` and `POST /configurations/{key}/reset`
  returned 500 instead of 404 for a missing key, and `PUT /users/{id}` / `PUT /users/me` returned
  500 for a nonexistent user. All three now classify with `errors.Is` sentinels.
- Database failures are no longer masked as 404 on `GET /customers/{id}` (and its update
  pre-check), `GET`/`PUT`/`DELETE /tickets/{id}`, `GET`/`PUT`/`DELETE /tasks/{id}` and
  `GET /configurations/{key}` — a genuine internal error now returns 500 instead of "not found".
- `apperrors.IsNotFound` now actually recognises a raw `gorm.ErrRecordNotFound`, which its doc
  comment had always claimed but its implementation never did — the sentinels share a message but
  not an identity. The per-file workaround helpers this forced are gone.
- Task lists now honour `limit` and `offset`. The handler read `page`/`per_page` while the
  frontend sends `page`/`limit`, so the frontend's page size was silently ignored and any
  `per_page` over 100 fell back to 20 instead of capping. Tasks now use the same
  `ParseOffsetLimit` + `page`-override parsing as every other list; `per_page` is no longer read
  anywhere and the dead `ParsePaginationParams`/`CalculateOffset`/`CalculateTotalPages` helpers
  are removed.
- Bulk create asserted a slice pointer to a slice and panicked on every call, so all bulk create
  paths failed; it also reported rows it had never inserted as successful.
- Editing a task returned 403 for non-admin users when the request merely echoed the task's current
  assignee.
- Duplicate emails now return 409 consistently. Customer creation and update previously returned
  400, and database driver text could leak into the error response.
- Admin routes defined both a static `element` and a `lazy` import, so React Router rendered a
  placeholder and the real Users and Configuration pages were unreachable.
- `make build-tools` wrote `bin/.create-admin` while `make create-admin` executed
  `bin/create-admin`, so the documented two-step sequence always failed.

### Security

- `GET /dashboard/stats` was the only route without a role guard: any authenticated account —
  including the `customer` role that public registration hands out — could read system-wide lead,
  customer, ticket and task counts. It now requires admin, sales or support; the frontend
  dashboard no longer requests stats for customer-role users.
- The sales role had unrestricted write access to every ticket (update, reassign, resolve),
  while support — the role tickets are actually assigned to — was limited to its own assignments.
  Sales is now read-only on tickets: `PUT /tickets/{id}` returns 403.
- `POST /auth/register` accepted a client-supplied role, allowing anyone to create an admin account
  and receive a valid token. The endpoint now always creates a `customer`.
- `GET /customers/:id/tickets` performed no ownership check, letting any authenticated user read
  another customer's tickets. A customer-role user may now only read their own.
- `?limit=0` reached the pagination arithmetic and panicked, turning every list endpoint into a 500
  via a single query parameter.
- An API key whose owner has been erased or deactivated is now rejected at authentication time.

### Known issues

- Application logs record the email address on login and on customer create/update, and issued JWTs
  embed it until they expire. Database erasure does not reach either, so log and token retention
  need their own policy.
- `internal/middleware/csrf.go` is implemented and unit-tested but never installed in `cmd/main.go`,
  so no route currently requires a CSRF token.
- `RateLimitGenerous` (240/min) is defined but never applied; two tiers are active, not three.
- The generic `/bulk/:resource` handlers in `internal/handler/bulk_handler.go` remain unrouted;
  only the entity-specific `bulk/status` endpoints are reachable over HTTP.
- An already-issued JWT cannot be revoked before it expires; logout and refresh rotation revoke
  refresh tokens only, and there is no access-token blocklist.
- Password-reset delivery requires SMTP configuration; without `SMTP_HOST` the reset link is only
  written (redacted) to the application log.
- ESLint reports 40 errors and 137 warnings in the frontend, mostly unused Playwright fixture
  arguments and `any` types. These pre-date this work and were not touched. `tsc -b` is clean.

[Unreleased]: https://github.com/florinel-chis/GopherCRM/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/florinel-chis/GopherCRM/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/florinel-chis/GopherCRM/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/florinel-chis/GopherCRM/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/florinel-chis/GopherCRM/releases/tag/v1.0.0
