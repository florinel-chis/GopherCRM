# Known limitations

> **For:** Operators and developers who need to know where the shipped code stops. Part of the [documentation index](README.md).

What the shipped code does not do, or does only under conditions. Ideas that are not implemented are in
[ROADMAP.md](ROADMAP.md).

- **Access tokens end early only on logout, password change, password reset, deactivation and a
  SCIM group change that lowers the role.**
  These increment the account's `users.token_version`, which every JWT carries, so every access
  token issued before them is refused at once (erasure rejects it too). Password change, password
  reset and deactivation (by an administrator or through SCIM) also revoke every API key of the
  account for good, and reactivating an account brings none of its earlier credentials back;
  logout leaves API keys alone. A SCIM group change that leaves an account in no mapped group
  deactivates it the same way; one that gives it a role lacking a permission the old one had ends
  its access and refresh tokens, not its API keys. Refresh rotation
  does not: the access token it replaces stays valid until `JWT_EXPIRY_HOURS` elapses, and so does
  every token of an account whose role changes any other way. There is no per-token denylist.
- **Session handling has known gaps.** Browser tabs sharing a remembered session coordinate their
  refreshes through the Web Locks API (#184), which browsers offer only in secure contexts: served
  over plain HTTP (the bundled nginx without TLS) each tab locks on its own, and two tabs
  refreshing at the same moment can still sign each other out when the losing tab's rejection
  arrives before the winner stored the successor. Other open tabs learn about a sign-in or
  sign-out only on their next request. Login itself is not affected: a password change, reset,
  deactivation or erasure that commits before a login takes the owner lock refuses that login, and
  one that commits after it ends the session the login issued (#336, see `docs/CONCURRENCY.md`).
- **Outbound email needs SMTP configuration.** Without `SMTP_HOST` set, every mail — the
  password-reset link (redacted), the forms mail, the quote email and the `send_email` step of an
  automation rule — goes to the application log instead of a mailbox, and the sending code cannot
  tell: `POST /api/v1/quotes/:id/email` still answers `200` and stamps `last_emailed_at`, and an
  automation email step is logged as `succeeded`, although nothing was delivered. Form mail and
  ticket comment mail (`email_customer`) are the exception: each delivery is recorded `log_only`,
  not `sent`. Fine for development, useless in production.
- **Quote PDF output needs a browser on the server.** Without a headless browser named by
  `QUOTE_PDF_CHROME_PATH`, and in the two development Compose stacks, PDF output is off and quote
  emails are text only; setup is in
  [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#quote-pdf-output).
- **Form mail is retried by hand only.** Every form mail (confirmation, follow-up, one notification
  per address) is recorded and shown on the submission and in `GET /api/v1/forms/deliveries`;
  admin and sales retry a failed, log-only or stuck one with
  `POST /api/v1/forms/deliveries/{id}/retry`. Nothing retries automatically, and `sent` means the
  relay accepted the message: bounces are not tracked.
- **The automation worker is replica-safe on MySQL/MariaDB, with conditions.** Every replica with
  `AUTOMATION_ENABLED=true` (see `.env.example`) may run the worker against one shared database
  (#237): each run claim carries the replica's instance id and a heartbeat lease, every later
  write is fenced on that owner, and a run whose lease is older than 3 minutes (a crashed or
  paused worker) is recovered by the next tick of any worker, without a restart. A replica with
  the worker off serves the API and records events but neither executes nor recovers runs.
  Replica clocks must agree to well under 90 seconds, and an upgrade stops every replica before
  starting the new version (an older binary resets live runs at boot). Several processes on one
  SQLite file are still not supported. An email whose worker paused between its final owner check
  and the relay call can still go out once after another worker recorded it `email_unconfirmed`;
  it is never resent. Processed events and finished runs are kept; there is no retention pruning
  yet.
- **AEO is replica-safe on MySQL/MariaDB, with conditions.** One AEO run is admitted across every
  replica sharing the database and the daily scheduled run starts once per slot (#229). A run of a
  crashed or killed process is recovered up to 3 minutes after the crash, at the next boot or start
  attempt; until then a manual run answers 409. Every replica must use the same `TZ` and
  `AEO_SCHEDULE_HOUR`, replica clocks must be synchronised to well under 90 seconds, and an
  upgrade stops every replica before starting the new version (mixed versions are not supported).
  Several processes on one SQLite file are not supported.
- Authentication is header-only (Bearer or ApiKey); no cookie credential is accepted, so CSRF does
  not apply. Any future cookie session must ship with session-bound CSRF protection (see #261).
- **Rate limiting does not distinguish reads from writes on authenticated routes.**
  `RateLimitModerate` (120/min, burst 30) is applied once to the whole protected group in
  `setupDependencies` (`cmd/main.go`) and covers reads and writes alike. It is charged before
  authentication (#466), so requests with refused or missing credentials on protected routes
  are charged too.
  `RateLimitStrict` (10/min, burst 5) guards the `/auth`
  group, the public form submit and confirm routes (`SetupFormPublicRoutes`) and the public quote
  response routes `/quotes/public/response` (`SetupQuotePublicRoutes`);
  `RateLimitGenerous` (240/min, burst 40) is applied only to the public form *reads*
  (`embed.js`, the definition and the hosted view) and to `/quotes/public/render`.
  `DISABLE_RATE_LIMIT=true` lifts the strict tier and the two SCIM limits on `/scim/v2` (the SCIM
  token limit and the SCIM refusal limit); the checks live inside `RateLimitStrict` and
  `NewSCIMRateLimits`. The moderate and generous tiers still apply to test traffic.
- **Rate limits are per process and per middleware instance.** Clients are counted per IPv4
  address and per IPv6 /64 network. Each instance tracks at most 10,000 clients; when it is full, a
  new client is still admitted and the least recently active record is dropped (that client
  starts again with a fresh burst). Idle records are also reclaimed after five idle minutes once
  the bucket has refilled. Limits are not shared between replicas, and behind a proxy they depend
  on `TRUSTED_PROXIES`. `DISABLE_RATE_LIMIT=true` stops startup when `SERVER_MODE=production`. See
  [DEVELOPMENT.md](DEVELOPMENT.md) for details.
- **Bulk operations are status-only.** Only `POST /leads|tickets|tasks/bulk/status` exist (see the
  [API reference](../api/index.html)); there is no bulk create, update or delete.
- **Spam and unconfirmed form submissions are kept until you set a retention.** With
  `FORM_RETENTION_DAYS` set (3 days at least, off by default), those with no linked lead are removed
  permanently at that age, at boot and then every 24 hours, so the first boot removes the existing
  backlog. Until a separate frontend task merges, the form-delete dialogs still say "Collected
  submissions are retained", which is wrong for those rows.
- **Sign-in audit rows are kept for `AUTH_AUDIT_RETENTION_DAYS`.** Every successful password
  sign-in, single sign-on callback and successful refresh writes one `auth_audit_log` row, so does every password change
  (`password_change`), password reset request (`reset_request`), reset completion
  (`reset_confirm`) and logout (`logout`, #550), so does every API key creation
  (`api_key_create`), revocation (`api_key_revoke`) and refused re-enable of a revoked key
  (`api_key_enable`, #551, with the key's id), and refused API keys (#466), refused password
  sign-ins, refused refreshes and refused SCIM authentications (#553) are counted in one row per
  method, reason, account, client network and minute (`attempts`); rows are kept 90
  days by default (#454). The rows hold no email, name, subject or full IP address, and erasure leaves them
  to age out.
- **Erasure does not reach logs or issued tokens.** Service logs omit email, name, title and
  search fields; shared helpers omit argument/result/response payloads, and HTTP logs omit query
  strings. Quoted values in logged errors are redacted, and SMTP errors retain only the protocol
  code. Other raw diagnostics, request IDs, client IPs, Origin/User-Agent headers and mailer
  recipient metadata can still carry personal data. Older logs and issued JWTs that embed email
  also remain outside database erasure, so log and token retention need their own policy.
