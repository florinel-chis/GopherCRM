# Frequently asked questions

Back to the [overview](overview.md).

## Can some people use single sign-on and others a password?

Yes. Accounts created by a single sign-on sign-in sign in through the identity provider; all other
accounts keep their password. Whether accounts created by single sign-on may also use a password is
the setting **Allow password sign-in and password reset for accounts linked to single sign-on**
(`security.sso.allow_password`), off by default. See
[Mixed accounts](single-sign-on.md#mixed-accounts).

## Someone already has a password account. Can they switch to single sign-on?

No. Switching an existing password account to single sign-on in place is not supported.
GopherCRM never links accounts by email address, so a single sign-on sign-in with an address that
another account uses is refused (`email_taken`). Recommended: the person keeps signing in with the
password account, or uses single sign-on under a separate identity whose email address no
GopherCRM account uses.

Deleting the password account is not a migration; it erases the person: every personal field is
overwritten (the email address becomes a random `.invalid` placeholder), the account's API keys,
refresh tokens and password-reset tokens are purged, and it cannot be undone; see
[Privacy: deleting a person erases their data](../privacy-and-erasure.md). Only after such a
deletion does the person's next single sign-on sign-in create a new, separate account, which
inherits nothing: the records the old account owned or was assigned (leads, tasks, tickets, deals)
stay with the erased row.

## How do I remove someone who signs in with single sign-on?

Remove them at the identity provider: remove their assignment to the application, or the app role
or group that the role mapping uses. Their next sign-in is refused. Without SCIM, their current
session ends at most 24 hours after their last sign-in at the provider (an access token already
issued stays valid until it expires); SCIM deprovisioning, or **Deactivate** in GopherCRM, ends it
at once. A sign-out at the provider also ends it at once, when the provider sends the `sid` claim
and GopherCRM's front-channel logout URL is registered there (see
[Single sign-on](single-sign-on.md#what-happens-at-sign-in)); removing an assignment alone does not
sign the person out at the provider.

If **Allow password sign-in and password reset for accounts linked to single sign-on** is on, also
deactivate the account in GopherCRM (Users → the user → **Deactivate**, or
`POST /users/{id}/deactivate`), because a password they set keeps working after the provider
removes them. Deactivating is a good idea in any case: it also revokes their API keys at once, for
good.

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

SAML is not supported: single sign-on uses OpenID Connect only. SCIM 2.0 is partly supported: an
identity provider can create, update and deactivate accounts and manage the members of the groups
the role mapping names, but SCIM never erases an account. See [SCIM provisioning](scim-provisioning.md)
for the details.
