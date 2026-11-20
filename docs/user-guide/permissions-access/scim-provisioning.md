# SCIM provisioning

Back to the [overview](overview.md).

GopherCRM is a SCIM 2.0 provider. Your identity provider (for example Microsoft Entra ID or Okta)
can create, update and deactivate staff accounts in GopherCRM before the people sign in. This page
describes what GopherCRM does today. Single sign-on itself is described in
[Single sign-on](single-sign-on.md).

## What SCIM does

- **Create.** The provider creates a staff account (active unless it sends `active` false) with the
  role the single sign-on role mapping gives it. The account has no usable password.
- **Update.** The provider changes the user name (the email address), the given and family name,
  the role and the active state.
- **Deactivate.** Setting `active` to false (through PATCH or PUT) deactivates the account and ends
  its sessions at once: every access token is refused from that moment and every refresh token is
  revoked. Setting `active` to true again reactivates the account, but the old sessions do not come
  back; the person signs in again.
- **Never delete.** SCIM never erases or deletes an account. Erasure stays the delete in GopherCRM,
  done by an administrator.
- **Which accounts the provider sees.** Staff accounts, including deactivated ones (served with
  `active` false). Customer accounts and erased accounts are never served: the provider gets a
  404 for them.

## Base URL

The SCIM base URL is `PUBLIC_BASE_URL/scim/v2`: the public address of the GopherCRM backend
followed by `/scim/v2`, for example https://crm.example.com/scim/v2. The links GopherCRM returns in
each user's meta.location are built from the same setting. When the web application and the
backend share one address, the base URL is that address followed by `/scim/v2`.

## Tokens

The provider authenticates with a SCIM token that you create in GopherCRM.

1. Open Settings → SCIM tokens (`/settings/scim-tokens`). The page needs the `settings.scim_manage`
   permission, which only the Admin role holds.
2. Click **Create token** and give the token a name of 1 to 100 characters, for example the name of
   the provider.
3. The token (`scim_` followed by 64 hexadecimal characters) is shown once, in the "Your new SCIM
   token" dialog, with a copy button. Copy it now: it cannot be shown again.
4. Paste it into the provider as the secret token or bearer token, next to the base URL:
   - Microsoft Entra ID: under Provisioning > Admin credentials, the base URL goes in "Tenant URL"
     and the token in "Secret Token".
   - Okta: the base URL goes in "SCIM connector base URL", and the authentication mode is HTTP
     Header, with the token as the Bearer value.

To revoke a token, use the Revoke action on its row and confirm. A revoked token is refused at
once and stays in the list. Rotating `API_KEY_SECRET` (or `JWT_SECRET`, when
`API_KEY_SECRET` is not set) invalidates every SCIM token; create new ones afterwards.

## Supported operations

| Operation | What it does |
| --- | --- |
| `GET /scim/v2/ServiceProviderConfig` | Describes what the provider supports. |
| `GET /scim/v2/Schemas` | The User schema. |
| `GET /scim/v2/ResourceTypes` | The User resource type. |
| `GET /scim/v2/Users` | Lists staff accounts. |
| `POST /scim/v2/Users` | Creates an account. |
| `GET /scim/v2/Users/{id}` | Reads one account. |
| `PUT /scim/v2/Users/{id}` | Replaces an account's attributes. |
| `PATCH /scim/v2/Users/{id}` | Changes some of an account's attributes. |

- **Listing.** The only filter accepted is `userName eq "<value>"`, which providers use to look a
  person up before creating them. Any other filter is refused with 400 `invalidFilter`. Paging uses
  `startIndex` and `count`; at most 100 accounts are returned per page.
- **Create.** The request must carry an `externalId`, and single sign-on must have an issuer
  configured. GopherCRM links the new account to that issuer, with the `externalId` as the
  person's identifier at the provider, and gives the account an unusable password.
- **Replace and change.** PUT ignores `externalId`. PATCH refuses `externalId`, `displayName` and
  every other attribute it does not list with 400 `invalidPath`. Limit the provider's attribute
  mappings to the user name, active state, given and family name, work email and roles, as
  [Single sign-on](single-sign-on.md) says.
- **Role.** The role always comes from the single sign-on role mapping: GopherCRM uses the first
  mapping row with claim `roles` whose value equals one of the `roles` values the provider sends,
  exactly. A create or PUT with no matching row, or one whose row maps to Customer, is refused with
  400 `invalidValue`. A PATCH that does not send `roles` keeps the account's role. A change that
  would demote or deactivate the last active administrator is refused with 400 `invalidValue`.
- **Duplicates.** An email address or an `externalId` that another account already holds is
  refused with 409 `uniqueness`.
- **Rate limit.** SCIM requests share a strict limit of 10 a minute per client address, with a
  burst of 5. Above it the provider gets 429 and retries later.

## What is not served

As of this version, GopherCRM does not serve DELETE /scim/v2/Users/{id} or /scim/v2/Groups. A
request to either gets the router's plain-text answer `404 page not found`, not a SCIM error.

- Turn group provisioning off at the provider (provision users only). Roles come from the
  `roles` attribute and the role mapping, not from pushed groups.
- To remove a person, have the provider deactivate them. Erase the account in GopherCRM if the
  data must go.

## Audit

- A create, and a role change made through SCIM, write a role audit row `user_role_changed` with
  `auth_method` `scim` and no actor. See the [audit trail](roles-and-permissions.md#audit-trail).
- Every successful SCIM write records a sign-in audit row with method `scim` and outcome `success`.
- A request with a refused token records method `scim` with the reason `token_missing`,
  `token_invalid` or `token_revoked`.
- A refused write (400 or 409) records nothing.

Sign-in audit rows are listed in Settings → Sign-in audit (`/settings/sign-in-audit`).

## Known limitation: Microsoft Entra ID and the first single sign-on

Single sign-on recognises a person only by the exact pair of issuer and `sub` (the identifier in
the provider's ID token). A SCIM create stores that link with the `externalId` as the `sub`.

Entra ID's default attribute mapping sends the user's `objectId` as `externalId`, but the `sub` in
an Entra ID token is pairwise: it is different for every application and does not equal the
`objectId`. The link created through SCIM therefore never matches, and the person's first single
sign-on is handled as a first sign-in. When that sign-in would otherwise succeed (the ID token
carries an email address and a role mapping row matches), GopherCRM finds the email address
already held by the SCIM-created account and refuses: the person sees "Single sign-on failed. Try
again or sign in with your password." and the sign-in audit shows the reason `email_taken`.
GopherCRM never links accounts by email address.

Provisioning again does not repair the link, because PUT ignores `externalId` and PATCH refuses
it. The link works only when the provider's `externalId` equals the `sub` its ID tokens carry for
that person.
