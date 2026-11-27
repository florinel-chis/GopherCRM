# Sign-in audit

The sign-in audit records how people and integrations sign in, and why a sign-in was refused. It
also records password changes, password reset requests and completions, logouts, and the creation
and revocation of API keys. Use it to find out why someone cannot sign in, to spot repeated
refusals from one network, and to see when a key was created or revoked. The technical reference
is the [sign-in audit log](../development/authorization.md#sign-in-audit-log) section of development/authorization.md.

## Open the audit

Open Settings → Sign-in audit (`/settings/sign-in-audit`). The screen is shown to users whose role
holds the `audit.sign_ins_view` permission. The same rows are available through the API with
`GET /api/v1/audit/sign-ins`. Rows are listed newest first, a page at a time.

Narrow the list with the filters; a changed filter starts again on the first page.

| Filter | What it keeps |
|---|---|
| Method | Rows of one method (see [Methods and reason codes](#methods-and-reason-codes)). |
| Outcome | Successes or refusals. |
| Reason | Rows with one reason code. |
| User id | Rows about one account, by its numeric id. |
| From | Rows created at or after this time. |
| To | Rows created before this time. |

## What each column holds

| Column | Meaning |
|---|---|
| Time | When it happened. For a row that counts several refusals, the time of the first one. |
| Method | How the person or integration signed in, or which credential step the row records. |
| Outcome | Success or refused. |
| Reason | The reason code of a refusal; empty (shown as -) on success. |
| Attempts | 1, or for counted refusals how many there were (see below). |
| User id | The account the row is about, when the account was found; empty otherwise, for example for an unknown email address. |
| Client network | The network the request came from: the client address cut to its /24 (IPv4) or /48 (IPv6) network. Behind a reverse proxy this is right only when `TRUSTED_PROXIES` names the proxy. |
| Browser | The browser and operating system family, for example "Firefox on Linux", from a fixed list; empty without a user agent. |
| Request id | The request's id, to find the matching lines in the server log; empty when the request carried no usable id. |
| Subject reference | Only for a single sign-on refusal decided after the provider's ID token was verified: a fixed code computed from the person's identity at the provider. The same person always gives the same code, so repeated refusals of one person can be grouped without storing who it is. It changes when `API_KEY_SECRET` (or `JWT_SECRET` when that is unset) changes. |

The API returns a few fields the screen does not show: the single sign-on attempt id, the id of
the API key a key creation, revocation or re-enable row concerns, and the `roles` and `groups`
values the role mapping used at a single sign-on sign-in.

**Never stored.** A row never holds an email address, a name, the person's identity at the
provider, a password, a token or key, the full IP address or the full user agent.

**Counted refusals.** Refused password sign-ins, refused refreshes, refused API keys and refused
SCIM authentications do not write one row each. They are counted in one row per method, reason,
account, client network and minute, and the Attempts column (`attempts` in the API) says how many
there were. That row keeps the time, request id and browser of the first refusal of that minute.
Every other outcome writes a row of its own with Attempts 1.

## How long rows are kept

Rows are deleted when they are older than `AUTH_AUDIT_RETENTION_DAYS` days, set in the server's
environment: 90 days by default, at most 36500. An empty, zero, negative or unreadable value keeps
the default of 90, so a typo can neither keep the rows forever nor stop the clean-up; a larger value
is lowered to 36500. Deleting (erasing) a user does not remove their rows: they hold only ids, a
network and a browser family, and they age out.

## Methods and reason codes

Each method below lists the reason codes a refused row of that method can carry. A successful row
has outcome `success` and no reason; a refused one has outcome `refused` and one of these codes.

**`password`**: a password sign-in.

- `invalid_credentials`: an unknown email address (no user id) or a wrong password.
- `account_locked`: the account is locked after too many failed sign-ins.
- `account_inactive`: the account is deactivated.
- `account_erased`: the account was deleted.
- `password_changed`: the password changed while the sign-in was being checked.
- `sso_account`: the account signs in with single sign-on and password sign-in is off for it.
- `internal_error`: the server failed.

**`oidc`**: a single sign-on sign-in. For troubleshooting, see the
[single sign-on troubleshooting table](single-sign-on.md#troubleshooting).

- `state_invalid`: the sign-in link is unknown, used or expired; start the sign-in again.
- `binding_missing`: the browser did not send back the sign-in's binding, for example another browser finished it.
- `binding_mismatch`: the browser's binding does not belong to this sign-in.
- `issuer_changed`: the issuer setting changed while the sign-in was in progress.
- `id_token_missing`: the provider returned no ID token.
- `id_token_invalid`: the ID token's signature or contents did not verify.
- `nonce_mismatch`: the ID token was not issued for this sign-in.
- `claims_invalid`: the ID token's claims could not be read.
- `group_overage`: the provider left the groups out of the token, and it is not an Entra ID tenant
  whose groups GopherCRM can read from Microsoft Graph.
- `groups_malformed`: the groups claim is not a list of strings.
- `roles_malformed`: the roles claim is not a list of strings.
- `subject_invalid`: the person's identifier at the provider is empty, too long or holds characters it may not.
- `issuer_invalid`: the ID token's issuer is too long.
- `groups_unavailable`: the groups could not be read from Microsoft Graph or the provider.
- `userinfo_subject_mismatch`: the provider's user information is about another person.
- `no_mapping_match`: no role mapping row matches the person's roles or groups.
- `role_missing`: the role a mapping row names no longer exists.
- `identity_mismatch`: a stored identity differs from the provider's in case or spacing.
- `identity_taken`: the identity was linked by another sign-in at the same moment.
- `account_erased`: the linked account was deleted.
- `account_inactive`: the linked account is deactivated.
- `customer_login_role`: the account is a customer account, which cannot sign in this way.
- `last_admin`: the mapping would take the role away from the last active administrator.
- `email_missing`: the ID token carries no email address.
- `email_taken`: another account already uses the email address.
- `not_configured`: single sign-on is switched on but a required setting is empty.
- `discovery_failed`: the issuer could not be reached or discovered.
- `token_exchange_failed`: the provider refused the client secret or certificate.
- `sso_disabled`: single sign-on is switched off.
- `internal_error`: the server failed.

**`refresh`**: a session renewing itself with its refresh token.

- `token_invalid`: the refresh token is unknown, revoked or expired, or another tab used it first.
- `token_reuse`: a refresh token that was already exchanged came back; every refresh token of that
  session is revoked.
- `account_inactive`: the account is deactivated.
- `account_erased`: the account was deleted.
- `unavailable`: the server could not renew sessions at that moment.

**`api_key`**: a request made with an API key that was refused. Successful requests are not
recorded.

- `key_invalid`: no live key matches.
- `key_revoked`: the key is revoked.
- `key_expired`: the key has expired.
- `account_inactive`: the key's owner is deactivated.
- `account_erased`: the key's owner was deleted.
- `customer_login_role`: the key's owner is a customer account.

**`scim`**: the identity provider's SCIM client (see [SCIM provisioning](scim-provisioning.md)).
A successful create, change or deactivation of an account writes a success row.

- `token_missing`: the request carried no SCIM token.
- `token_invalid`: no SCIM token matches.
- `token_revoked`: the SCIM token is revoked.

**`password_change`**: a user changing their own password.

- `invalid_credentials`: the current password was wrong.
- `session_stale`: the session sent with the change is not one of the user's live sessions.
- `account_inactive`: the account is deactivated.
- `account_erased`: the account was deleted.
- `internal_error`: the server failed.

**`reset_request`**: a request for a password reset link. The person always sees the same answer.

- `account_unknown`: no account has that email address.
- `account_inactive`: the account is deactivated.
- `account_erased`: the account was deleted.
- `sso_account`: the account signs in with single sign-on and password sign-in is off for it.
- `unavailable`: password reset is not set up on this server.
- `internal_error`: the server failed.

**`reset_confirm`**: setting a new password from a reset link.

- `token_invalid`: the link is unknown, used or expired.
- `account_inactive`: the account is deactivated.
- `account_erased`: the account was deleted.
- `sso_account`: the account signs in with single sign-on and password sign-in is off for it.
- `unavailable`: password reset is not set up on this server.
- `internal_error`: the server failed.

**`logout`**: a sign-out, including a sign-out the identity provider started.

- `internal_error`: the server failed.

**`api_key_create`** and **`api_key_revoke`**: an API key was created or revoked (see
[API keys](../integrations/api-keys.md)). These rows are successes only.

**`api_key_enable`**: an attempt to switch a revoked API key on again, always refused.

- `key_revoked`: the key is revoked, and revocation is final.
