# Roles and permissions

Back to the [overview](overview.md).

Every user has exactly one role, and the role is the complete list of what that user may do. A
permission is named `<group>.<action>`, for example `leads.view`. GopherCRM refuses any action
whose permission the role does not hold (the answer is 403 `FORBIDDEN`). A change to a role
applies to its users from their next request; nobody has to sign in again.

## The four system roles

Admin, Sales, Support and Customer ship with GopherCRM. They are fixed: they cannot be edited or
deleted (the API answers 409 `SYSTEM_ROLE_IMMUTABLE`), but each can be cloned into a custom role
that you then change. Admin holds 75 permissions, Sales 37, Support 17 and Customer 4.

In the table, "all" means every record and "own" means only the records the user owns or is
assigned. A dash means the role holds nothing in that group. The full list, one permission per
row, is the System-role matrix in [AUTHORIZATION.md](../../AUTHORIZATION.md#system-role-matrix).

| Group | Admin | Sales | Support | Customer |
|---|---|---|---|---|
| `users` | everything | - | - | - |
| `roles` | everything | - | - | - |
| `settings` | everything, including `settings.sso_manage` and `settings.scim_manage` | - | - | - |
| `audit` | everything (`audit.sign_ins_view`) | - | - | - |
| `leads` | everything | `leads.view`, `leads.update`, `leads.delete`, `leads.convert`, `leads.export` own; `leads.create` all | - | - |
| `customers` | everything | `customers.view`, `customers.create`, `customers.update`, `customers.assign` all | `customers.view` all | - |
| `companies` | everything | `companies.view`, `companies.create`, `companies.update` all | `companies.view` all | - |
| `deals` | everything | `deals.view`, `deals.update` own; `deals.create` all | - | - |
| `products` | everything | `products.view` all | - | - |
| `quotes` | everything | `quotes.view`, `quotes.update`, `quotes.delete` own; `quotes.create` all | - | - |
| `tickets` | everything except `tickets.view_own_customer` | `tickets.view` all | `tickets.view`, `tickets.update`, `tickets.comment` own; `tickets.create` all | `tickets.view_own_customer` |
| `tasks` | everything | `tasks.view`, `tasks.update` own; `tasks.create` all | `tasks.view`, `tasks.update` own; `tasks.create` all | `tasks.view`, `tasks.update` own |
| `labels` | everything | `labels.view`, `labels.create`, `labels.update` all | `labels.view`, `labels.create`, `labels.update` all | `labels.view` all |
| `dashboard` | `dashboard.view` | `dashboard.view` | `dashboard.view` | - |
| `automation` | everything | `automation.runs_view` own | - | - |
| `notifications` | `notifications.use` | `notifications.use` | `notifications.use` | - |
| `aeo` | everything | `aeo.view`, `aeo.manage` | `aeo.view` | - |
| `forms` | everything | `forms.view`, `forms.manage` | `forms.view` | - |
| `webhooks` | everything | - | - | - |
| `assignment` | everything | `assignment.customer_assignee`, `assignment.form_owner` | `assignment.ticket_assignee` | - |

"Everything" means every permission of the group, for all records. `tickets.view_own_customer`
(the tickets of the customer record linked to a customer login) belongs to the Customer role only;
no other role, and no custom role, can hold it. Likewise `settings.sso_manage` (single sign-on
settings) and `settings.scim_manage` (SCIM provider tokens) belong to the Admin role only: no
custom role can hold them, the role editor does not offer them, and a clone of Admin leaves them
out. The `assignment` permissions say who can be picked:
only a user holding `assignment.ticket_assignee` can be assigned a ticket, for example.

## Custom roles

Roles are managed in Settings → Roles (`/settings/roles`). You see the list when you hold
`roles.view`; creating, cloning, editing and deleting need `roles.manage`. The list shows each
role's type (System or Custom), how many users hold it and how many permissions it has.

### Create a role

1. Open Settings → Roles and click **New role**.
2. Enter a **Name** (it must be unique) and, if you like, a **Description**.
3. Under **Permissions**, tick what the role may do. The permissions are grouped as in the table
   above; ticking a group's box ticks every permission in it.
4. For a ticked permission that can be limited, a scope list appears next to it. Choose **All**
   for every record or **Own** for only the records the user owns or is assigned.
5. Click **Save**.

### Clone a role

Cloning is the quickest way to start from a system role.

1. In Settings → Roles, click **Clone** on the role's row (or open a system role and click
   **Clone**).
2. Enter the **Name of the new role** and confirm. The copy holds the same permissions, except
   those only a system role can hold (a clone of Admin leaves out `settings.sso_manage` and
   `settings.scim_manage`), and opens in the editor.
3. Change the permissions and click **Save**.

A clone of Sales is a custom role: it behaves exactly as its ticked permissions say, so it can,
for example, hand a lead to a colleague only once `leads.assign_owner` is ticked on it.

### Edit a role

1. In Settings → Roles, click **Edit** on a custom role's row (system roles have no Edit button).
2. Change the name, description or permissions and click **Save**. The users holding the role get
   the change on their next request.

### Delete a role

1. In Settings → Roles, click **Delete** on a custom role's row and confirm.
2. A role that users still hold cannot be deleted: give those users another role first. System
   roles cannot be deleted.

## Assign a role to a user

1. Open Users, then create a user or edit an existing one. You need `users.assign_role` to see
   the **Role** field.
2. Pick a system role or a custom role in **Role** and save.

A customer login (a user on the Customer role) cannot be given a custom role: the form shows "A
customer login cannot be given a custom role." (409 `CUSTOMER_LOGIN_ROLE`). Users who sign in
with single sign-on get their role from the provider at every sign-in; see
[Single sign-on](single-sign-on.md).

## Assign a role to an API key

An API key has no role and no permissions of its own. It acts with the permissions its owner holds
at the moment of each request. To give an integration only what it needs:

1. Create a custom role that holds only the permissions the integration needs (see
   [Create a role](#create-a-role)).
2. Create a dedicated user for the integration, one user per integration, and give it that role.
3. Sign in as that user and create the API key (Settings → API Keys, or `POST /api-keys`). A
   key is always created for the user who is signed in.

To stop the integration, deactivate its user: Users → the user → **Deactivate**, or
`POST /users/{id}/deactivate`. That stops every key of the user at its next request. An
administrator cannot list or revoke another user's keys. The worked example, including which
permissions a ticket or lead integration needs, is "Least-privilege integration user" in
[AUTHORIZATION.md](../../AUTHORIZATION.md#least-privilege-integration-user).

## Rules that protect the installation

**The last administrator.** GopherCRM never lets the installation end up without an active user on
the Admin role. A role change, deactivation or deletion that would remove the last one is refused
with 409 `LAST_ADMIN`, also when that administrator tries it on their own account. Only active
users on the Admin system role count; a custom role that holds every permission does not.

**No escalation.** A user who is not on the Admin role cannot give anyone more than they hold
themselves. They cannot create, edit or clone a role with permissions above their own, assign such
a role, or edit, activate, deactivate or delete a user whose permissions are above their own. "Own"
counts as less than "all". The refusal is 403 `ROLE_ESCALATION`, and the rule also applies to
their own account. Because the Admin role holds two permissions no custom role can hold, an
administrator's account is always above a user on a custom role: only an administrator can give
someone the Admin role, or edit, deactivate or delete an administrator.

## Audit trail

Every role create, clone, edit and delete, and every change of a user's role, writes one row to the
role audit log. A row records who made the change, how that request was authenticated, what was
done, and the state before and after.

The SPA has no screen for this log. Read it through the API with `GET /roles/audit`, which lists
the rows newest first and needs `roles.audit_view`. The `auth_method` field says how the change
was made:

| `auth_method` | Meaning |
|---|---|
| `bearer` | A person signed in to the SPA (or a client using a sign-in token) made the change. |
| `api_key` | The change was made with an API key. |
| `oidc` | A single sign-on sign-in applied the role from the provider's mapping to the person's own account. |
| `migration` | A data step of an upgrade changed a custom role (for example, removing the admin-only permissions); no person is recorded. |

Changes to the single sign-on settings are not audited yet. Sign-in attempts are recorded
separately, in Settings → Sign-in audit (`/settings/sign-in-audit`, needs
`audit.sign_ins_view`).
