# Frequently asked questions

Back to the [overview](overview.md).

## Can some people use single sign-on and others a password?

Yes. Accounts created by a single sign-on sign-in sign in through the identity provider; all other
accounts keep their password. Whether accounts created by single sign-on may also use a password is
the setting **Allow password sign-in and password reset for accounts linked to single sign-on**
(`security.sso.allow_password`), off by default. See
[Mixed accounts](single-sign-on.md#mixed-accounts).

## Someone already has a password account. Can they switch to single sign-on?

Not with the same account. GopherCRM never links accounts by email address, so a single sign-on
sign-in with an address that an existing account uses is refused. The person keeps using the
password account, or you delete that account and the person's next single sign-on sign-in creates
a new one. A deleted account keeps no usable email address, so the new sign-in is not blocked by it.

## How do I remove someone who signs in with single sign-on?

Remove them at the identity provider: remove their assignment to the application, or the app role
or group that the role mapping uses. Their next sign-in is refused.

If **Allow password sign-in and password reset for accounts linked to single sign-on** is on, also
deactivate the account in GopherCRM (Users → the user → **Deactivate**, or
`POST /users/{id}/deactivate`), because a password they set keeps working after the provider
removes them. Deactivating is a good idea in any case: it also ends the access of their API keys at
once.

## What protects us from losing the last administrator?

GopherCRM refuses any role change, deactivation or deletion that would leave no active user on the
Admin role (409 `LAST_ADMIN`). A single sign-on sign-in cannot do it either: if the mapping would
move the last administrator to another role, that sign-in is refused. Keep at least one local
administrator account with a password, so you can still sign in when the provider is down.

## Can I give an API key its own role?

No. An API key acts with the permissions of the user who created it. For an integration, create a
dedicated user with a custom role that holds only what the integration needs, and create the key as
that user. See [Assign a role to an API key](roles-and-permissions.md#assign-a-role-to-an-api-key).

## I changed a single sign-on user's role by hand and it changed back. Why?

The role mapping is applied again at every single sign-on sign-in. A role you set by hand on an
account linked to single sign-on lasts only until that person's next sign-in, when the first
matching mapping row sets it again. Change the person's app role or group at the provider, or the
mapping, instead.

## What happens when a single sign-on user is deleted?

Deleting a user erases their personal data, but GopherCRM keeps the link to their identity at the
provider. If that person tries to sign in again, the sign-in is refused (401); it never creates a
fresh account. The same holds for a deactivated account until you activate it again.

## Does GopherCRM support SCIM or SAML?

SAML: no. Single sign-on uses OpenID Connect only. SCIM 2.0: partly. An identity provider holding a
SCIM token can create accounts, and read, update and deactivate the accounts SCIM created or that
are linked to the provider (never an account created locally in GopherCRM), with the role taken
from the single sign-on role mapping, so an account can exist before the person's first sign-in. When the
provider deactivates an account, the person is signed out everywhere at once; reactivating it
lets them sign in again, but not with the old sessions. A SCIM DELETE deactivates the account the
same way: SCIM never erases accounts: delete people in GopherCRM when their data must go. The
provider can also read the groups of the role mapping and change their members; a member change
sets the person's role from their groups, and a change that would leave no active administrator is
refused. Groups cannot be created, renamed or deleted through SCIM. The provider's attribute
mappings must send only the user name, active state, given and family name, work email and roles;
an update that also sends, for example, `externalId` or `displayName` is refused, so remove those
mappings at the provider. An administrator creates the provider's SCIM token under Settings →
SCIM tokens, where the token is shown once, and revokes it there; the provider is refused at once.
Without SCIM, accounts are created at the first sign-in.
