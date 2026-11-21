# SCIM provisioning

Back to the [overview](overview.md).

GopherCRM is a SCIM 2.0 provider. Your identity provider (for example Microsoft Entra ID or Okta)
can create, update and deactivate staff accounts in GopherCRM before the people sign in, and can
manage the members of the groups your role mapping names. This page
describes what GopherCRM does today. Single sign-on itself is described in
[Single sign-on](single-sign-on.md).

## What SCIM does

- **Create.** The provider creates a staff account (active unless it sends `active` false) with the
  role the single sign-on role mapping gives it. The account has no usable password.
- **Update.** The provider changes the user name (the email address), the given and family name,
  the role and the active state.
- **Deactivate.** Setting `active` to false (through PATCH or PUT), or a DELETE of the user,
  deactivates the account and ends its sessions at once: every access token is refused from that moment, and every refresh token and
  every API key is revoked. Setting `active` to true again reactivates the account, but the old
  sessions and keys do not come back; the person signs in again and issues new keys.
  SCIM deactivation is the immediate path; without it, a single sign-on session ends 24 hours
  after the sign-in at the provider.
- **Never delete.** SCIM never erases or deletes an account. A DELETE from the provider deactivates
  the account and keeps its data. Erasure stays the delete in GopherCRM, done by an administrator.
- **Groups.** The groups are the `groups` values of the single sign-on role mapping. The provider
  reads them and changes their members; a member change sets the member's role from its groups.
- **Which accounts the provider sees.** The provider sees and changes only the accounts SCIM
  created and the accounts linked to the single sign-on issuer by a sign-in, including deactivated
  ones (served with `active` false). An account created locally in GopherCRM, for example the
  local administrator, is never listed, read or changed through SCIM: the Users routes answer 404
  for it, and naming it as a group member is refused with 400 `invalidValue`. A POST whose user
  name is such an account's address answers 409 `uniqueness` and changes nothing. Customer
  accounts and erased accounts are never served either. Changing the issuer in Settings → Single
  sign-on hides the accounts linked only under the old issuer; while accounts are linked under the
  current issuer, the settings ask for confirmation first and report how many account links stop
  matching.

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
| `GET /scim/v2/Schemas` | The User and Group schemas. |
| `GET /scim/v2/ResourceTypes` | The User and Group resource types. |
| `GET /scim/v2/Users` | Lists the accounts SCIM created or that are linked to the single sign-on issuer. |
| `POST /scim/v2/Users` | Creates an account. |
| `GET /scim/v2/Users/{id}` | Reads one account. |
| `PUT /scim/v2/Users/{id}` | Replaces an account's attributes. |
| `PATCH /scim/v2/Users/{id}` | Changes some of an account's attributes. |
| `DELETE /scim/v2/Users/{id}` | Deactivates an account and never erases it. |
| `GET /scim/v2/Groups` | Lists the groups of the role mapping. |
| `GET /scim/v2/Groups/{id}` | Reads one group and its members. |
| `PATCH /scim/v2/Groups/{id}` | Adds, removes or replaces a group's members. |
| `POST /scim/v2/Groups` | Answers 405: groups cannot be created through SCIM. |
| `DELETE /scim/v2/Groups/{id}` | Answers 405: groups cannot be deleted through SCIM. |

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
- **Role.** The role always comes from the single sign-on role mapping. A Users write (create,
  PUT or PATCH with `roles`) uses the first mapping row with claim `roles` whose value equals one
  of the `roles` values the provider sends, exactly; it never reads groups. A create or PUT with no
  matching row, or one whose row maps to Customer, is refused with 400 `invalidValue`. A PATCH that
  does not send `roles` keeps the account's role. A Users write that would demote or deactivate the
  last active administrator, a DELETE included, is refused with 400 `invalidValue`. A group patch
  uses only the rows with claim `groups` (see Groups). The two kinds set the same role, so the
  last write wins: a Users write with `roles` overrides the role a group patch gave, and the next
  group patch for that person overrides it again. Do not mix `roles` and `groups` rows in the
  mapping unless you mean to.
- **Groups.** Each distinct value of a mapping row with claim `groups` is one group; its
  `displayName` is that value, matched exactly, and its `id` is derived from it. Roles rows are not
  groups. Groups come from the mapping: they cannot be created, renamed or deleted through SCIM,
  so a POST or DELETE answers 405, a PUT gets the router's plain-text 404, and a PATCH of
  `displayName` or any attribute other than `members` is refused with 400 `invalidPath`. A group
  removed from the mapping answers 404. Listing accepts only the filter `displayName eq "<value>"`;
  any other filter is refused with 400 `invalidFilter`. Members are listed by their SCIM user id;
  a member value that is not the id of an account the provider sees (an unknown, customer, erased
  or locally created account) is refused with 400 `invalidValue` and nothing changes. A deactivated
  account can be added. A membership of a locally created account stored before is not listed; a
  PATCH that removes it deletes only the membership and leaves the account and its role as they
  are.
- **Group membership and roles.** After a PATCH changes a group's members, each person added or
  removed gets the role of the first mapping row with claim `groups` whose value is one of the
  person's groups after the change, as single sign-on does with the `groups` claim. An active
  person left in no mapped group is deactivated, as `active` false does: they are signed out
  everywhere at once, their API keys are revoked, and their role stays as it was. A person who is
  already deactivated stays as they are. When the new role lacks a permission the old one had, the
  person is signed out at once and signs in again with the new role; their API keys keep working
  with the new role. Editing the role mapping recomputes no role by itself: a
  person's role follows a mapping change only at the next group patch that adds or removes that
  person. A group patch may hand the administrator role over to another active account, for
  example by replacing the members of an administrator group, or by adding the new administrator
  and removing the old one in one PATCH. Only a result that leaves no active administrator is
  refused, with 400 `mutability`, and then nothing changes; a deactivated administrator does not
  count as an active one.
- **Delete.** A DELETE answers 204 with no body and deactivates the account as `active` false
  does: every session ends, and the email address, name, role and single sign-on link stay. A
  DELETE of an account that is already deactivated answers 204 and changes nothing. An account the
  provider does not see, a locally created account included, answers 404 and is not changed.
- **Duplicates.** An email address or an `externalId` that another account already holds is
  refused with 409 `uniqueness`.
- **Rate limit.** Two limits apply. The SCIM token limit counts the requests of each SCIM
  provider token, whatever address they come from: by default 600 a minute with a burst of 60.
  `SCIM_RATE_LIMIT_PER_MINUTE` sets the requests a minute (maximum 6000) and
  `SCIM_RATE_LIMIT_BURST` the burst (maximum 1000).
  The SCIM refusal limit counts requests without a valid token per client address: 10 a minute
  with a burst of 5. Above either limit the provider gets 429 and retries later; a refusal over
  the SCIM refusal limit writes no sign-in audit row.

## What is not served

- Groups are served, but only as the role mapping defines them. Creating a group (POST) or deleting
  one (DELETE) answers 405 with a SCIM error and an `Allow` header; replacing one (PUT) is not
  routed and gets the router's plain-text answer `404 page not found`. Renaming a group
  (`displayName`) is refused with 400 `invalidPath`. Add or rename groups in the role mapping in
  Settings → Single sign-on instead.
- Bulk operations (/scim/v2/Bulk), sorting and ETags are not served.
- To remove a person, have the provider deactivate them; a provider DELETE deactivates too. Erase
  the account in GopherCRM if the data must go. Removing a person from a group never deactivates
  or erases them.

## Audit

- A create, and a role change made through SCIM, write a role audit row `user_role_changed` with
  `auth_method` `scim` and no actor. See the [audit trail](roles-and-permissions.md#audit-trail).
- A replace or patch that changes `active` writes a role audit row `user_activated` or
  `user_deactivated`, and one that changes `userName` writes `user_email_changed`, also with
  `auth_method` `scim` and no actor. A DELETE of an active account writes one `user_deactivated`
  row. A deactivation of an account that is already inactive, and a create, write no such row.
- Every successful SCIM write records a sign-in audit row with method `scim` and outcome `success`,
  except a DELETE of an account that is already deactivated, which changes nothing and records
  nothing.
- A request with a refused token records method `scim` with the reason `token_missing`,
  `token_invalid` or `token_revoked`.
- A group patch writes one role audit row `user_role_changed` (`auth_method` `scim`, no actor)
  for each member whose role changes and one `user_deactivated` row for each member it
  deactivates, and, after it succeeds, one sign-in audit row with method
  `scim` and outcome `success` for each person added to or removed from the group. A group patch
  that changes no member records nothing.
- A refused write (400 or 409) records nothing.

Sign-in audit rows are listed in Settings → Sign-in audit (`/settings/sign-in-audit`), role audit
rows in Settings → Role audit (`/settings/role-audit`).

## Microsoft Entra ID: the first single sign-on of a SCIM-created account

Single sign-on recognises a person by the exact pair of issuer and `sub` (the identifier in the
provider's ID token). A SCIM create stores a link with the `externalId` as that identifier.

Entra ID's default attribute mapping sends the user's `objectId` as `externalId`, but the `sub` in
an Entra ID token is pairwise: it is different for every application and does not equal the
`objectId`. GopherCRM therefore also reads two other claims of an Entra ID token: `oid` (the
person's object id) and `tid` (the tenant id). When a person's first single sign-on carries a
`sub` that no account holds yet, GopherCRM signs them in as the account SCIM created for them when
all of these hold:

- the issuer configured in Settings → Single sign-on is Entra ID's tenant-specific issuer,
  `https://login.microsoftonline.com/<tenant id>/v2.0`;
- the token's `tid` equals the `<tenant id>` in that issuer, exactly (letter case included);
- the token's `oid` equals the `externalId` SCIM stored for an account under that issuer.

The account must be active and not erased. GopherCRM then stores the token's `sub` as a second
link of that account, so every later sign-in finds the account by its `sub` directly. The account's
`externalId`, as the provider reads it, stays the `objectId`.

When any of these does not hold, the first sign-in is handled as before: it creates a new account
or is refused. That is the case for a token from another tenant, a token without `oid` or `tid`, an
issuer that is not Entra ID (or the multi-tenant `common` or `organizations` issuer), and an
`objectId` that no SCIM create stored. A deactivated SCIM-created account is refused with the
reason `account_inactive`, an erased one with `account_erased`; neither gets a new account.

GopherCRM never links accounts by email address. A first sign-in that matches no SCIM link but
carries the email address of an existing account is refused: the person sees "Single sign-on
failed. Try again or sign in with your password." and the sign-in audit shows the reason
`email_taken`. Other providers link only when the `externalId` they send equals the `sub` their ID
tokens carry for that person.
