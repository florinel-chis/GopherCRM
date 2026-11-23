# Permissions and access: overview

This guide is for the administrator of a GopherCRM installation. It explains who can do what,
how to change that, and how people can sign in through your company's identity provider. It
also covers the API keys, sign-in audit and webhooks screens. It describes what GopherCRM does
today. The technical reference behind it is
[AUTHORIZATION.md](../../AUTHORIZATION.md).

**Roles.** Every user has exactly one role. Four system roles ship with GopherCRM: Admin, Sales,
Support and Customer. They are fixed: you cannot edit or delete them, but you can clone one and
change the copy. A role you create or clone is a custom role. Roles are managed in
Settings → Roles.

**Permissions.** A role is a list of permissions. Each permission allows one kind of action on one
area of the CRM, for example "View leads" (`leads.view`) or "Change settings"
(`settings.update`). Some permissions can be held for all records or only for your own records
(the records you own or are assigned). Anything a role does not list is refused.

**Single sign-on.** GopherCRM can let people sign in with an OpenID Connect identity provider such
as Microsoft Entra ID or Okta. The first sign-in creates the account, and the provider's groups or
app roles decide the person's GopherCRM role at every sign-in. Password sign-in stays available
for local accounts. Single sign-on is set up in Settings → Single sign-on.

## Pages in this guide

- [Roles and permissions](roles-and-permissions.md): the system roles, custom roles, assigning a
  role, API keys, the rules that protect the last administrator, and the audit trail.
- [Single sign-on](single-sign-on.md): how sign-in through an identity provider works, the
  settings, mixed password and single sign-on accounts, and troubleshooting.
- [How to: Microsoft Entra ID](how-to-microsoft-entra-id.md): a step-by-step setup for Microsoft
  Entra ID.
- [SCIM provisioning](scim-provisioning.md): how an identity provider creates and manages accounts through SCIM 2.0.
- [Testing single sign-on](testing-single-sign-on.md): how to test single sign-on against your own
  Entra ID tenant before rolling it out, what to check and where to read the result.
- [API keys](api-keys.md): creating, using and revoking your own API keys.
- [Sign-in audit](sign-in-audit.md): the record of sign-ins and refusals, what each column holds,
  how long rows are kept, and every method and reason code.
- [Webhooks](webhooks.md): sending signed events to other systems, the test ping, secret rotation,
  the delivery log and redelivery, pausing, and how the receiving system verifies a request.
- [Frequently asked questions](faq.md): short answers to common questions.
