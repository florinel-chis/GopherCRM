# Authorization

Who may do what in GopherCRM: the permission catalogue, the fixed matrices of the four system
roles, custom roles, and the rules that govern role changes. It describes the code on `main`; the
sign-in design at the end is a note for later work and says so.

The work is tracked in #258 and specified in #259. The source of truth for every table below is
`internal/permissions`; `go test -count=1 ./internal/permissions` fails when this guide and the
package disagree (see the Testing section of [DEVELOPMENT.md](DEVELOPMENT.md)). The administrator
guide, in plain language and task by task, is
[user-guide/permissions-access/overview.md](user-guide/permissions-access/overview.md).

## Phased plan

Phase 1 (#259) replaces the role checks with permissions and adds custom roles, in six slices.
Phase 2 (#261) is single sign-on and provisioning.

| Slice | Ticket | Delivers | Status |
|---|---|---|---|
| S1 | #322 | Tests only: the frozen record of what every protected route answers to each system role (`cmd/testdata/system_role_route_matrix.json`) | Merged as #369 |
| S2 | #344 | Persistence: the `roles`, `role_permissions` and `role_audit_log` tables, `users.role_id`, the seeded system rows, `RoleRepository` | Merged as #359 |
| S3 | #347 | The permission catalogue, the system-role matrices, `permissions.RouteKeys` and deny-by-default enforcement on every protected route | Merged as #370 |
| S4 | #348 | The role API (`/roles`, `/permissions/catalogue`, `/roles/audit`), the last-admin invariant, the escalation rule and the audit trail | Merged as #372 |
| S5 | #349 | This guide, with the phase 2 design note | This document |
| S6 | #260 | The SPA: role and permission screens in Settings, permission-driven navigation and guards | Navigation, route guards and page buttons delivered by #380; the role list, the role editor and the role picker of the user form delivered by #381 |
| Phase 2 | #261 | Single sign-on, identity linking and provisioning (see the design note below) | Open: the SSO settings (`/settings/sso`, `settings.sso_manage`) delivered by #434; the backend OIDC sign-in (`POST /auth/oidc/start`, `POST /auth/oidc/callback`) delivered by #435; the SPA login button, the `/auth/callback` page and Settings > Single sign-on delivered by #436; the first SCIM 2.0 slice (provider tokens, `/scim/v2` authentication and discovery, `settings.scim_manage`) delivered by #468; the SCIM user read (`GET /scim/v2/Users`, `GET /scim/v2/Users/{id}`) delivered by #469; the SCIM user create and replace (`POST /scim/v2/Users`, `PUT /scim/v2/Users/{id}`) delivered by #470; the SCIM user patch (`PATCH /scim/v2/Users/{id}`) delivered by #471; the SCIM user delete, a deactivation (`DELETE /scim/v2/Users/{id}`), delivered by #504; the SCIM Groups read and members patch (`GET /scim/v2/Groups`, `GET`/`PATCH /scim/v2/Groups/{id}`) delivered by #506 |

Since #380 the SPA reads the permissions of `GET /users/me` and shows routes, navigation entries
and buttons by permission key, so a user on a custom role sees what its role grants. It reads
`GET /users/me` again when the window regains focus, after a role is saved in the role editor
and after the signed-in user's own account is saved in the user form (#566). Since #381
custom roles are created, cloned, edited and deleted in Settings > Roles (`/settings/roles`,
shown with `roles.view`, written with `roles.manage`) and assigned in the user form, whose role
picker lists the roles of `GET /roles`; the role API stays available for the same operations.

## Permission catalogue

A permission is a leaf of the catalogue tree, keyed `<group>.<action>`. The tree is the
`catalogue` variable in `internal/permissions/catalogue.go`; `permissions.Leaves()` returns the
leaves in display order and `GET /permissions/catalogue` serves the tree. There are 77 leaves in
20 groups. Sixteen can be held at scope `own` as well as `all` (`AllowsOwn`). One,
`tickets.view_own_customer`, is system-only (`SystemOnly`): only the customer system role holds
it, and no custom role can. Two, `settings.sso_manage` and `settings.scim_manage`, are admin-only
(`AdminOnly`): only the Admin system role can hold them, and no custom role can
(`Node.CustomRoleMayHold`).

| Permission | Title | Scopes |
|---|---|---|
| `users.view` | View users | all |
| `users.create` | Create users | all |
| `users.update` | Edit users | all |
| `users.delete` | Delete users | all |
| `users.activate` | Activate and deactivate users | all |
| `users.assign_role` | Assign roles to users | all |
| `roles.view` | View roles | all |
| `roles.manage` | Create, edit and delete roles | all |
| `roles.audit_view` | View the role audit log | all |
| `settings.view` | View settings | all |
| `settings.update` | Change settings | all |
| `settings.sso_manage` | View and change single sign-on settings | all (admin-only) |
| `settings.scim_manage` | Create, list and revoke SCIM provider tokens | all (admin-only) |
| `audit.sign_ins_view` | View the sign-in audit log | all |
| `leads.view` | View leads | all, own |
| `leads.create` | Create leads | all |
| `leads.update` | Edit leads | all, own |
| `leads.delete` | Delete leads | all, own |
| `leads.erase_converted` | Delete converted leads | all |
| `leads.convert` | Convert leads to customers | all, own |
| `leads.assign_owner` | Give a lead to another owner | all |
| `leads.export` | Export leads | all, own |
| `customers.view` | View customers | all |
| `customers.create` | Create customers | all |
| `customers.update` | Edit customers | all |
| `customers.delete` | Delete customers | all |
| `customers.export` | Export customers | all |
| `customers.assign` | Assign customers | all |
| `companies.view` | View companies | all |
| `companies.create` | Create companies | all |
| `companies.update` | Edit companies | all |
| `companies.delete` | Delete companies | all |
| `companies.assign_owner` | Give a company to another owner | all |
| `deals.view` | View deals | all, own |
| `deals.create` | Create deals | all |
| `deals.update` | Edit deals and change their stage | all, own |
| `deals.delete` | Delete deals | all |
| `deals.assign_owner` | Give a deal to another owner | all |
| `products.view` | View products | all |
| `products.create` | Create products | all |
| `products.update` | Edit products | all |
| `products.delete` | Delete products | all |
| `quotes.view` | View quotes | all, own |
| `quotes.create` | Create quotes | all |
| `quotes.update` | Edit, send and email quotes | all, own |
| `quotes.delete` | Delete quotes | all, own |
| `quotes.assign_owner` | Give a quote to another owner | all |
| `tickets.view` | View tickets | all, own |
| `tickets.create` | Create tickets | all |
| `tickets.update` | Edit tickets | all, own |
| `tickets.delete` | Delete tickets | all |
| `tickets.comment` | Comment on tickets | all, own |
| `tickets.view_own_customer` | View the tickets of the linked customer record | all (system-only) |
| `tasks.view` | View tasks | all, own |
| `tasks.create` | Create tasks | all |
| `tasks.update` | Edit tasks | all, own |
| `tasks.delete` | Delete tasks | all |
| `tasks.assign_others` | Assign tasks to other users | all |
| `labels.view` | View labels | all |
| `labels.create` | Create labels | all |
| `labels.update` | Edit labels | all |
| `labels.delete` | Delete labels | all |
| `dashboard.view` | View the dashboard | all |
| `automation.rules_manage` | Manage automation rules | all |
| `automation.runs_view` | View automation runs | all, own |
| `automation.runs_cancel` | Cancel automation runs | all |
| `notifications.use` | Receive and read notifications | all |
| `aeo.view` | View reports | all |
| `aeo.manage` | Manage the profile, prompts and runs | all |
| `aeo.delete_prompts` | Delete prompts | all |
| `forms.view` | View forms and submissions | all |
| `forms.manage` | Create and edit forms | all |
| `forms.delete` | Delete forms | all |
| `webhooks.manage` | Manage webhook endpoints and read deliveries | all |
| `assignment.customer_assignee` | Can be assigned customers | all |
| `assignment.ticket_assignee` | Can be assigned tickets | all |
| `assignment.form_owner` | Can own leads and be a form's default owner | all |

## System-role matrix

The four system roles are fixed matrices in code (`systemRoles` in
`internal/permissions/system_roles.go`, read through `permissions.ForSystemRole`), pinned by
`internal/permissions/testdata/system_roles.golden.json`. They are not stored in
`role_permissions`, and they can be neither edited nor deleted (409 `SYSTEM_ROLE_IMMUTABLE`).
admin holds every leaf that is not system-only at `all` (76 grants); sales holds 37, support 17
and customer 4. A cell is the scope the role holds the key at, or `-` when it does not hold it.

| Permission | admin | sales | support | customer |
|---|---|---|---|---|
| `users.view` | all | - | - | - |
| `users.create` | all | - | - | - |
| `users.update` | all | - | - | - |
| `users.delete` | all | - | - | - |
| `users.activate` | all | - | - | - |
| `users.assign_role` | all | - | - | - |
| `roles.view` | all | - | - | - |
| `roles.manage` | all | - | - | - |
| `roles.audit_view` | all | - | - | - |
| `settings.view` | all | - | - | - |
| `settings.update` | all | - | - | - |
| `settings.sso_manage` | all | - | - | - |
| `settings.scim_manage` | all | - | - | - |
| `audit.sign_ins_view` | all | - | - | - |
| `leads.view` | all | own | - | - |
| `leads.create` | all | all | - | - |
| `leads.update` | all | own | - | - |
| `leads.delete` | all | own | - | - |
| `leads.erase_converted` | all | - | - | - |
| `leads.convert` | all | own | - | - |
| `leads.assign_owner` | all | - | - | - |
| `leads.export` | all | own | - | - |
| `customers.view` | all | all | all | - |
| `customers.create` | all | all | - | - |
| `customers.update` | all | all | - | - |
| `customers.delete` | all | - | - | - |
| `customers.export` | all | - | - | - |
| `customers.assign` | all | all | - | - |
| `companies.view` | all | all | all | - |
| `companies.create` | all | all | - | - |
| `companies.update` | all | all | - | - |
| `companies.delete` | all | - | - | - |
| `companies.assign_owner` | all | - | - | - |
| `deals.view` | all | own | - | - |
| `deals.create` | all | all | - | - |
| `deals.update` | all | own | - | - |
| `deals.delete` | all | - | - | - |
| `deals.assign_owner` | all | - | - | - |
| `products.view` | all | all | - | - |
| `products.create` | all | - | - | - |
| `products.update` | all | - | - | - |
| `products.delete` | all | - | - | - |
| `quotes.view` | all | own | - | - |
| `quotes.create` | all | all | - | - |
| `quotes.update` | all | own | - | - |
| `quotes.delete` | all | own | - | - |
| `quotes.assign_owner` | all | - | - | - |
| `tickets.view` | all | all | own | - |
| `tickets.create` | all | - | all | - |
| `tickets.update` | all | - | own | - |
| `tickets.delete` | all | - | - | - |
| `tickets.comment` | all | - | own | - |
| `tickets.view_own_customer` | - | - | - | all |
| `tasks.view` | all | own | own | own |
| `tasks.create` | all | all | all | - |
| `tasks.update` | all | own | own | own |
| `tasks.delete` | all | - | - | - |
| `tasks.assign_others` | all | - | - | - |
| `labels.view` | all | all | all | all |
| `labels.create` | all | all | all | - |
| `labels.update` | all | all | all | - |
| `labels.delete` | all | - | - | - |
| `dashboard.view` | all | all | all | - |
| `automation.rules_manage` | all | - | - | - |
| `automation.runs_view` | all | own | - | - |
| `automation.runs_cancel` | all | - | - | - |
| `notifications.use` | all | all | all | - |
| `aeo.view` | all | all | all | - |
| `aeo.manage` | all | all | - | - |
| `aeo.delete_prompts` | all | - | - | - |
| `forms.view` | all | all | all | - |
| `forms.manage` | all | all | - | - |
| `forms.delete` | all | - | - | - |
| `webhooks.manage` | all | - | - | - |
| `assignment.customer_assignee` | all | all | - | - |
| `assignment.ticket_assignee` | all | - | all | - |
| `assignment.form_owner` | all | all | - | - |

## Custom roles and scopes

A custom role is a row of `roles` with its grants in `role_permissions` (tables in
[datamodel.md](datamodel.md#role-model)). A user on one has `role = custom` and a `role_id`, and
holds exactly what the role row grants (`permissions.ForUser`, `ForCustomRole`): a stored key the
catalogue does not know, a system-only key, an admin-only key (`settings.sso_manage`,
`settings.scim_manage`) and a scope the leaf does not allow are dropped when the set is resolved,
and refused with 400 `INVALID_PERMISSION` when a role is written. Cloning the Admin role
therefore leaves the two admin-only keys out. On every start the application removes both keys
from any custom role that still holds them, and the SQL migration
`20261129120001_admin_only_sso_scim_permissions` does the same; each changed role gets one
`role_updated` audit row (see Audit trail).

Each grant has a scope:

- `all`: the permission applies to every record.
- `own`: it applies to the records the caller owns or is assigned. Only the sixteen leaves
  marked `all, own` above accept it; their handlers narrow by owner or assignee.

A system role can be cloned into a custom role (`POST /roles/{id}/clone`). A customer login
cannot receive a custom role (409 `CUSTOMER_LOGIN_ROLE`). The permission set is resolved by
`middleware.Auth` on every request, for `Authorization: Bearer` and `ApiKey` credentials alike, so
a changed role applies from the caller's next request. `GET /users/me` returns `role_name` and
`permissions`.

`POST /leads/{id}/assign` asks a custom role for `leads.update`, whose scope decides the leads it
may assign, and for `leads.assign_owner` when `user_id` is not the caller: without that key the
answer is 403 "You can only assign leads to yourself", and assigning a lead to oneself needs
`leads.update` alone. The four system roles are answered on this route as before permissions
existed: a sales user hands its own lead to a colleague although the sales role does not hold
`leads.assign_owner`. A clone of the sales role is a custom role, so it hands a lead to a colleague
through this route only once `leads.assign_owner` is ticked on it
(`cmd/lead_assign_permission_test.go`, `TestLeadAssignRequiresAssignOwnerKey`).

## Deny by default

Every protected route is either in `permissions.RouteKeys` (`internal/permissions/routes.go`),
which names the key or keys that open it, or in `permissions.SelfServiceRoutes`; never in both.
The route files guard with `middleware.RequirePermission` and `middleware.RequireAnyPermission`,
and a caller that does not hold the key is answered 403 `FORBIDDEN`. The permission check comes
before any ownership check, so a caller without the key is refused on its own records too. A role
with no permissions is refused on every route of `RouteKeys`
(`TestZeroPermissionRoleIsDeniedEverywhere` and `TestEveryRouteRequiresItsOwnKey` in `cmd`).

The ten self-service routes need authentication only:

| Route | Purpose |
|---|---|
| `GET /users/me`, `PUT /users/me` | The caller's own profile |
| `POST /api-keys`, `GET /api-keys`, `GET /api-keys/:id`, `PUT /api-keys/:id`, `DELETE /api-keys/:id` | The caller's own API keys |
| `GET /configurations/ui` | The settings the SPA needs to render |
| `POST /auth/logout`, `POST /auth/change-password` | The caller's own session and password |

Adding a route or a permission: [EXTENDING.md](EXTENDING.md#10-add-a-role-or-a-permission),
section 10.

## Service-boundary adapter

Some services still decide by a `models.UserRole` argument. For those calls
`permissions.RoleForCall` translates the caller, per call site, from exactly one key through the
`callRoles` table. A system role passes unchanged. Any other caller is mapped by the scope it
holds the key at; a key outside the table is an error (`ErrNoCallRole`), and so is a key the
caller does not hold where the table names no role for that case (`ErrNotHeld`).

| Key | Held at `all` | Held at `own` | Not held | Used by |
|---|---|---|---|---|
| `leads.erase_converted` | admin | (no `own` scope) | sales | Lead delete: erasing a converted lead |
| `deals.view` | admin | sales | error | Deal pipeline, dashboard pipeline, the deal-link viewer |
| `leads.update` | admin | sales | error | Bulk lead status |
| `tickets.update` | admin | support | error | Bulk ticket status |
| `tasks.update` | admin | sales | error | Bulk task status |
| `leads.view` | admin | sales | support | The lead viewer of `CanViewLead` and of lead-link redaction |
| `leads.export` | admin | sales | error | Which leads the CSV export holds |
| `automation.runs_view` | admin | sales | error | The automation run viewer |

## Last-admin invariant

No role change, deactivation or erasure can leave the installation without an active admin: a
write that would is refused with 409 `LAST_ADMIN`, also when the last admin demotes or
deactivates itself. Only active, non-erased users on the admin system role count; a custom role
holding every permission does not. A concurrent handover of the admin role (a SCIM group patch
that demotes one admin and promotes another) cannot strand the installation either: the locked
row of the account being changed counts as an admin before the write whenever it is one (#513).

## Escalation rule

A caller that is not on the admin system role cannot grant permissions above its own (create,
update or clone a role, assign a role, create a user with a role), cannot edit or delete a role
whose permissions are above its own, and cannot edit, activate, deactivate or erase a user whose
permissions are above its own. `own` is below `all`, and a missing key is below `own`. The
refusal is 403 `ROLE_ESCALATION`, and the rule applies to the caller's own account too. The
customer system role counts as the empty set here, so a customer login is never above a manager.
The admin system role holds the two admin-only keys, which no custom role can hold, so an admin
account is always above a custom-role caller: only an admin can assign the admin role or edit,
activate, deactivate or erase an admin account (#524).

A customer login with a linked customer record (a `customers` row whose `user_id` is that login,
erased rows included) asks for the customer key next to the user key: erasing it
(`DELETE /users/{id}`) needs `customers.delete` as well as `users.delete`, because the customer
record is erased with it, and changing its email (`PUT /users/{id}`) needs `customers.update` as
well as `users.update`. Without the customer key the answer is 403 `FORBIDDEN` and nothing is
written. Its other fields, a customer login without a linked customer record and the four system
roles are answered as before. Covered by `TestLinkedCustomerLoginWrites{SQLite,MySQL}`
(`internal/integration`) and `TestGovernedWritesToLinkedCustomerLogin` (`internal/service`).

## Audit trail

Every role create, clone, update and delete, and every change of a user's `role` or `role_id`,
writes one `role_audit_log` row in the transaction of the change; a failed insert rolls the
change back. A row holds the acting user (`actor_user_id`), how the request authenticated
(`auth_method`: `bearer` or `api_key`, `oidc` for the role an OpenID Connect sign-in applied
to its own account, #435, or `scim` for a SCIM 2.0 user create, replace, patch or delete, #470,
#471 and #504, or a SCIM group patch, #506, whose `actor_user_id` is NULL because no user acts, or `migration` for a startup or
migration data step, #524, also with `actor_user_id` NULL), the action and the state before and
after, as ids, role names, permission keys and scopes only.

Since #552 the same table records account changes, with `role_id` NULL, an empty `role_name` and
the account in `target_user_id`:

- `user_activated` and `user_deactivated` (before and after hold `{"is_active": …}` only) when
  `is_active` changes;
- `user_erased` (no state) when an account is erased;
- `user_email_changed` (no state, and neither the old nor the new address in any column) when the
  login email changes.

The writers are the governed user writes (`PUT /users/{id}` with a changed email or `is_active`,
`POST /users/{id}/activate` and `/deactivate`, `DELETE /users/{id}`; method `bearer` or `api_key`,
the caller as actor) and the SCIM writes (`PUT` and `PATCH /scim/v2/Users/{id}`, a `DELETE
/scim/v2/Users/{id}` of an active account, which writes `user_deactivated`, and a group patch that
deactivates members, one `user_deactivated` row each; method `scim`, actor NULL). A row is written
only when the value changes: a repeated deactivation, an activation of an active account, the
stored email sent again, a SCIM create and every refusal write none. In one write the rows come
after the `users` write and the revocation, in the order email, activation, role. Three paths are
left alone on purpose and write no account row:

- an OpenID Connect sign-in, which never activates, deactivates, erases or changes an email (its
  `user_role_changed` rows with method `oidc` are unchanged);
- the caller's own profile-only email change (`PUT /users/me`, or `PUT /users/{own id}` without
  `role` or `is_active`), which stays the plain update behind the current-password check (#394); a
  `PUT` on the caller's own id that also carries `role` or `is_active` is governed and writes the
  row;
- the erasure of a customer login through `DELETE /customers/{id}` or a lead erasure cascade, which
  is not a governed write. The `migration` rows of #524 record
the role's stored permission rows, which can include a key the catalogue no longer resolves;
the role API records the resolved set. `GET /roles/audit` lists the rows, newest first, and
needs `roles.audit_view`.

The three rules above are decided inside one transaction per write, after the rows are locked.
The lock order is in [CONCURRENCY.md](CONCURRENCY.md#rbac-user-role-and-role-writes), flow "RBAC
user-role and role writes"; the order of the refusals, the error codes and the persistence layer
are in [DEVELOPMENT.md](DEVELOPMENT.md), "RBAC persistence" and "Role API and governed user
writes".

## Sign-in audit log

Since #454 every sign-in outcome is recorded in `auth_audit_log`, after the sign-in's own
transaction has ended. Each `POST /auth/oidc/callback`, and each `POST /auth/login` and
`POST /auth/refresh` that passes request validation and succeeds, writes one row of its own
(`attempts` 1) as one `INSERT`. Refusals are counted instead: a request whose `ApiKey`
credential is refused (#466), a refused `POST /auth/login` or `POST /auth/refresh` and a refused
SCIM authentication on `/scim/v2` (#553) add to the row of their method, reason, `user_id` (or
none), client network and UTC minute, whose `attempts` is how many there were. The first refusal
of that bucket writes the row; every later one adds 1 to `attempts` in the same statement
(`INSERT ... ON DUPLICATE KEY UPDATE` on MySQL and MariaDB, `ON CONFLICT ... DO UPDATE` on
SQLite, on a unique internal `aggregate_key`), so the row keeps the first refusal's
`request_id`, `user_agent_family` and `created_at`. A refused SCIM authentication (#468) has
method `scim`, `user_id` NULL and reason `token_missing`, `token_invalid` or `token_revoked`.
Since #470 a successful SCIM user create
or replace, since #471 a successful patch and since #504 a delete that deactivates, writes one row (method `scim`, outcome `success`, reason empty, `user_id` the account),
after its transaction; since #506 a successful group patch writes one such row per account added
to or removed from the group; a refused one (400 or 409), and a delete of an already inactive account,
writes none. Since #550 each `POST /auth/change-password`, `POST /auth/password-reset`,
`POST /auth/password-reset/confirm` and `POST /auth/logout` that reaches the service writes one
row of its own (methods `password_change`, `reset_request`, `reset_confirm` and `logout`,
`attempts` 1) after the service returns: a request refused by the authentication middleware or
a rate tier, a binding 400 or a new password refused by the length or complexity check writes
none. Since #551 a `POST /api-keys` answered 201, a revocation answered 200 (`DELETE /api-keys/:id`,
an idempotent repeat included, or `PUT /api-keys/:id` with `is_active` false, with or without a
new name) and a `PUT /api-keys/:id` with `is_active` true answered 409 because the key is revoked
each write one row (methods `api_key_create`, `api_key_revoke` and `api_key_enable`, `attempts`
1) after the service returns, with the caller's `user_id` and the key's id in `api_key_id`; a
rename alone, `is_active` true on a live key, a 400, 401, 403, 404 or 500 and every `GET` write
none. Any other request made with an API key, and any other SCIM request, writes nothing, and neither does a refused Bearer token on the API, a 400,
`POST /auth/oidc/start` or a rate-limited request. A failed write is logged once
(`component` `auth_audit`, `error_code` `audit_write_failed`) and never changes the answer.
Answers, status codes and messages are the same as before.

**Columns.** `id`; `created_at` (UTC, milliseconds); `method` (`password`, `oidc`, `refresh`,
`api_key`, `scim`, `password_change`, `reset_request`, `reset_confirm`, `logout`,
`api_key_create`, `api_key_revoke`, `api_key_enable`); `outcome` (`success` or `refused`); `reason` (empty on success); `user_id` (the
account, when the account row was found, else NULL); `attempt_id` (the `oidc_login_attempts` row
a callback claimed, else NULL); `api_key_id` (the `api_keys` row an `api_key_create`,
`api_key_revoke` or `api_key_enable` row concerns, else NULL; no foreign key, and never the key,
its prefix, its hash or its name); `request_id` (the request's `X-Request-ID`, stored only when it
matches `^[A-Za-z0-9._:-]{1,64}$`, else empty); `subject_ref` (see below, else NULL);
`claim_roles` and `claim_groups` (JSON arrays of the `roles` and `groups` values the role mapping
used, after a Microsoft Graph or userinfo resolution; at most 100 values of at most 256 bytes
each; NULL when the callback did not get that far or the claim was malformed); `client_network`
(the client address as Gin resolves it behind `TRUSTED_PROXIES`, cut to `a.b.c.0/24` for IPv4
and to the `/48` prefix for IPv6, empty when it does not parse); `user_agent_family`
(`<browser> on <OS>` from fixed lists: Edge, Opera, Firefox, Chrome, Safari, curl or Other, on
iOS, Android, ChromeOS, Windows, macOS, Linux or Other; empty without a `User-Agent`);
`attempts` (1, or for an `api_key` row the number of refusals it counts); `aggregate_key`
(internal and never returned or logged: `api_key|<reason>|<user id or 0>|<client network>|<UTC
minute>` on an `api_key` row, NULL on every other row; unique, and NULLs never collide). There is
no foreign key and no soft delete.

**Never stored.** An email address, a name, a subject (`sub`), a password, an access, refresh,
ID or API token, an authorization code, a state, a nonce, a PKCE verifier, the full IP address
or the full user agent. Neither are they logged.

**Reason codes.**

- `password`: `invalid_credentials` (unknown email, `user_id` NULL; or a wrong password),
  `account_locked`, `account_inactive`, `account_erased`, `password_changed` (the password
  changed between the check and the owner lock), `sso_account` (an account linked to single
  sign-on while `allow_password` is off), `internal_error`.
- `refresh`: `token_invalid` (unknown, revoked, expired, or a claim lost to a concurrent
  rotation of the same token), `token_reuse` (#547: a token already exchanged for a successor,
  presented again; every refresh token of its family is revoked, the row carries the owner's
  `user_id`), `account_inactive`, `account_erased`, `unavailable` (the 503
  `SESSION_REFRESH_UNAVAILABLE` path).
- `api_key`: `key_invalid` (no live key with that hash), `key_revoked` (a revoked key,
  `is_active` false, recorded with the owner's `user_id`), `key_expired` (past `expires_at`, or,
  for a key stored without one, more than one year after `created_at`), `account_inactive`,
  `account_erased` (the owner row is gone), `customer_login_role` (a key owned by a customer
  account, #532). A key that is both revoked and expired is recorded
  as `key_revoked`.
- `scim` (#468): `token_missing` (no `Authorization` header, another scheme than `Bearer`, or an
  empty token), `token_invalid` (no SCIM token has that hash: an unknown token, a user's JWT or
  API key, a token altered in case or padding) and `token_revoked` (a revoked SCIM token). The
  row never names the token.
- `oidc`: the `error_code` of the refusal (`state_invalid`, `binding_missing`, `binding_mismatch`,
  `issuer_changed`, `id_token_missing`,
  `id_token_invalid`, `nonce_mismatch`, `claims_invalid`, `group_overage`, `groups_malformed`,
  `roles_malformed`, `subject_invalid`, `issuer_invalid`, `groups_unavailable`,
  `userinfo_subject_mismatch`, `no_mapping_match`, `role_missing`, `identity_mismatch`,
  `account_erased`, `account_inactive`, `customer_login_role`, `last_admin`, `email_missing`,
  `email_taken`, `identity_taken`), the 503 codes `not_configured`, `discovery_failed` and
  `token_exchange_failed`, `sso_disabled` (404) and `internal_error` (500). A refusal of a
  linked account (`account_erased`, `account_inactive`, `customer_login_role`, `last_admin`)
  carries that account's `user_id`.
- `password_change` (#550, always the caller's `user_id`): `invalid_credentials` (a wrong
  current password, or one changed concurrently), `session_stale` (the `refresh_token` is not a
  live session of the caller; 409 `SESSION_STALE`), `account_inactive`, `account_erased`,
  `internal_error`.
- `reset_request` (#550, the answer is the same 200 in every case): success with the account's
  `user_id` once a link was stored, whether or not the mail was delivered; `account_unknown` (no
  account has that email, `user_id` NULL), `account_inactive`, `account_erased`, `sso_account`
  (an account linked to single sign-on while `allow_password` is off), `unavailable` (the reset
  flow is not wired, `user_id` NULL) and `internal_error`.
- `reset_confirm` (#550): `token_invalid`, `account_inactive`, `account_erased`, `sso_account`,
  `unavailable` and `internal_error`. An unknown, spent or expired token carries no `user_id`;
  `token_invalid` names the owner only when a stored token was found but another confirmation
  claimed it first. The owner refusals carry the owner's `user_id`.
- `logout` (#550, the caller's `user_id`): success, or `internal_error` (the 500).
- `api_key_create` and `api_key_revoke` (#551, the caller's `user_id` and the key's
  `api_key_id`): success only.
- `api_key_enable` (#551, the caller's `user_id` and the key's `api_key_id`): `key_revoked`
  only (the 409 for a revoked key).

**Subject reference.** A single sign-on refusal decided after the ID token verified and its nonce
matched stores `subject_ref`, the hex HMAC-SHA256 under `API_KEY_SECRET` (or `JWT_SECRET` when it
is unset) of `auth-audit-subject:` + the byte length of the issuer + `:` + the issuer + the
subject. The same identity always gives the same reference, so repeated refusals of one person
can be grouped without storing who it is; whoever holds the secret and knows a candidate
`(iss, sub)` can confirm it. A success, or a refusal before verification, stores NULL. Rotating the
secret changes every future reference.

**Retention.** Rows older than `AUTH_AUDIT_RETENTION_DAYS` (environment only, default 90; an
unset, empty, zero, negative or unparseable value keeps 90, at most 36500) are deleted by the
stale bulk-operation sweep, at boot and every 15 minutes on every replica, in chunks of 500.
Erasure of a user does not touch the rows: they hold only ids, a network prefix and a family
string, and they age out.

**How many rows.** Since #466 the moderate tier (120 requests a minute, burst 30, per client
address and per process) is charged before authentication on every protected route, so a
throttled request gets a 429 without a key lookup or a write, and refused or missing credentials
count against the same budget as authenticated traffic. The refusals that reach the key check
add to one row per reason, account, client network and UTC minute, however many addresses of
that `/24` or `/48` and however many replicas they reach: a burst of bogus keys from one network
writes at most one `key_invalid` row a minute, and its `attempts` still counts every refusal.
Refused password sign-ins and refreshes (#553) are counted the same way, one row per method,
reason, account, client network and UTC minute; successful ones and single sign-on callbacks
are one row per request. All of them pass the strict tier on `/auth` first (10 requests a
minute, burst 5, per client address, applied before the handler, so a 429 writes nothing).
Refused SCIM authentications are counted per reason, client network and UTC minute as well
(#553), behind the SCIM refusal limit on `/scim/v2` (10 a minute, burst 5, per client address; a
refusal over it is answered 429 and writes nothing). Password change, reset request, reset completion and
logout rows (#550) are one per request as well: the strict `/auth` tier bounds the two reset
routes, the authenticated tier bounds `POST /auth/change-password` and `POST /auth/logout`.
`DISABLE_RATE_LIMIT=true` lifts the strict tier and a
`gophercrm_e2e` build with `GOPHERCRM_E2E_DISABLE_RATE_LIMIT=true` lifts every tier; both are
test-only. With `DISABLE_RATE_LIMIT` or the e2e switch set, `SERVER_MODE=production` stops startup.

**Reading the log.** Since #455 `GET /audit/sign-ins` returns the rows, guarded by
`audit.sign_ins_view`; among the system roles only admin holds it, and a custom role may. The
order is `created_at` descending, then `id` descending; pagination is `offset`/`limit`
(`utils.ParseOffsetLimit`: default 20, at most 100) with `meta.total` the filtered count. The
filters are `method`, `outcome`, `reason` (each matched exactly and case-sensitively against the
values above, so `PASSWORD` is refused on every database), `user_id` (a positive integer), `from`
(inclusive) and `to` (exclusive), both RFC 3339 date-times compared in UTC. An absent or empty
value is no filter; an unknown value, a `user_id` that is not a positive integer, a `from` or `to`
that does not parse, or a `from` that is not before `to` is a 400 `VALIDATION_ERROR` with
`error.details` keyed by the parameter (`to` for the range) and the value not echoed. Each row
carries exactly the stored columns: `id`, `created_at`, `method`, `outcome`, `reason`, `user_id`,
`attempt_id`, `request_id`, `subject_ref`, `claim_roles`, `claim_groups` (decoded to arrays),
`client_network`, `user_agent_family` and `attempts`; the nullable ones are `null`, `aggregate_key`
is not returned, and nothing is joined (no email, no name). An `api_key` row with `attempts`
above 1 holds the first refusal of its minute: a later refusal of that minute cannot be found by
its request id, and `created_at` is when the bucket's first refusal came, not its last. The SPA shows the same rows, with the same filters, under Settings > Sign-in
audit (`/settings/sign-in-audit`, shown and opened only with `audit.sign_ins_view`).

**Debugging a refused sign-in.** A person reports "Single sign-on failed" (401). The response does
not say why, on purpose. Ask for the time and, when the SPA or a proxy sets one, the
`X-Request-ID`; then (Settings > Sign-in audit and `GET /audit/sign-ins` filter by method,
outcome, reason, user and time range, so the SQL below is optional):

1. Find the row: `SELECT * FROM auth_audit_log WHERE request_id = 'req-7f3a' ORDER BY id DESC;`
   or, without a request id, the `oidc` rows around the reported time from the person's
   `client_network`.
2. Read `reason`. `no_mapping_match` with `claim_groups` `["3f2a…", "77c1…"]` means the issuer sent
   those groups and none is in the role mapping: add the right group object id under Settings >
   Single sign-on, or assign the person to a mapped group at the issuer. `groups_unavailable`
   points at Microsoft Graph or the userinfo endpoint; `nonce_mismatch` or `state_invalid` at a
   reused or stale browser tab; `account_inactive` with a `user_id` at a deactivated account.
3. The `oidc_sign_in` log lines of the same request carry the same `request_id`, the
   `attempt_id` and, on the `Single sign-on refused` line, `audit_id`, the row's `id`.
4. To see whether the same person keeps failing, group by `subject_ref`:
   `SELECT subject_ref, reason, COUNT(*) FROM auth_audit_log WHERE method = 'oidc' AND outcome = 'refused' GROUP BY subject_ref, reason;`

## Which events end which credentials

An account holds three kinds of credential: access tokens (JWTs, refused once the account's
`token_version` moves past the value they carry), refresh tokens and API keys. Since #526 one
service step, `revokeAllCredentials` (`internal/service/credential_revocation.go`), ends all
three inside the transaction of a password change, a password reset, an administrator's
deactivation, a SCIM deactivation and a SCIM group patch that leaves an active account in no
mapped group (#530): it increments `token_version`, revokes every refresh token and revokes every
API key (`is_active` false, `revoked_at` stamped; a key revoked earlier keeps its first
`revoked_at`). Its first two steps are `endSessions`, which a SCIM group patch also runs alone
when it gives an account a role that lacks a permission the old one had: `token_version` and
every refresh token, API keys untouched. A failure of any step rolls the whole write back. Logout
keeps its own, narrower revocation.

| Event | Access tokens | Refresh tokens | API keys |
|---|---|---|---|
| Logout (`POST /auth/logout`) | Every earlier one ends | The presented one, or every one without a `refresh_token` | Not affected |
| Refresh rotation (`POST /auth/refresh`) | None end; the replaced one stays valid until it expires | The presented one is spent and replaced | Not affected |
| Single sign-on session limit (an `oidc` session at `session_started_at + 24` hours, #548) | None end; the last one issued stays valid until it expires | The session can no longer be rotated (reason `token_invalid`) or renewed by a password change (`session_stale`); nothing is written | Not affected |
| Refresh token presented again after its exchange (`POST /auth/refresh`, reason `token_reuse`) | None end; those already issued stay valid until they expire | Every one of the presented token's session (its family), the successors included; other sessions are not affected | Not affected |
| Password change (`POST /auth/change-password`) | Every earlier one ends, the caller's included; the renewed session's new token works | Every one, except the renewed session's successor | Every one, for good, the key that made the request included |
| Password reset (`POST /auth/password-reset/confirm`) | Every earlier one ends | Every one | Every one, for good |
| Administrator deactivation (`POST /users/{id}/deactivate`, `PUT /users/{id}` with `is_active` false) | Every earlier one ends | Every one | Every one, for good |
| SCIM `active` false (`PATCH`, `PUT`) or `DELETE /scim/v2/Users/{id}` | Every earlier one ends | Every one | Every one, for good |
| SCIM group patch (`PATCH /scim/v2/Groups/{id}`) leaving an active account in no mapped group | Every earlier one ends | Every one | Every one, for good |
| SCIM group patch that gives an account a role lacking a permission the old one had | Every earlier one ends | Every one | Not affected: each request re-reads the owner, so a key acts with the new role |
| Reactivation (`POST /users/{id}/activate`, `is_active` or SCIM `active` true) | Nothing comes back | Nothing comes back | Nothing comes back |
| Erasure (`DELETE /users/{id}`) | Refused with the erased account | Deleted | Deleted |
| Any other role change (an administrator's change, a SCIM Users write, a SCIM group patch that raises the role or changes it without losing a permission) | None yet: the new role applies from the next request | None yet | None yet |

The writes take `users → refresh_tokens → api_keys` (see [CONCURRENCY.md](CONCURRENCY.md)). A
server whose user service is not built with `WithUserCredentialRevocation`, or whose SCIM writes
lack the API key repository, refuses a deactivation with 500 and writes nothing; a SCIM group
patch without the refresh token or API key repository (`WithSCIMGroupWrites`) is refused with 500
before it reads anything. A SCIM `DELETE`
of an already inactive account writes nothing. Regression roots:
`TestCredentialRevocation{SQLite,MySQL}` (`internal/service/auth_credential_revocation_test.go`),
`TestCredentialRevocationProductionWiring` (`cmd/credential_revocation_wiring_test.go`) and
`TestAPIKeyRepository_RevokeAllForUser`.

## SCIM provisioning

Since #468 GopherCRM serves the first slice of a SCIM 2.0 provider (RFC 7643, RFC 7644) at
`/scim/v2`, outside the API prefix, so an identity provider such as Entra ID or Okta can be
pointed at `PUBLIC_BASE_URL/scim/v2`. #468 brought the tokens, the authentication and the
discovery documents; #469 brought the read of the Users resource (see [Users read](#users-read-469)
below) and #470 its create and replace (see
[Users create and replace](#users-create-and-replace-470)) and #471 its patch, which also
deprovisions (see [Users patch](#users-patch-471)), and #504 its delete, a deactivation that never
erases (see [Users delete](#users-delete-504)), and #506 the Groups read and members patch (see
[Groups](#groups-506)). `/Schemas/{id}`,
`/ResourceTypes/{id}` and a SCIM-shaped answer for unknown `/scim/v2` paths are later slices; until then an unknown path gets the
router's default 404, and a panic under `/scim/v2` the API's 500 envelope.

**Tokens.** A SCIM provider token belongs to the installation, not to a user. It is
`scim_` followed by the hex of 32 random bytes, shown once when it is created; `scim_tokens`
stores only its HMAC-SHA256 under `API_KEY_SECRET` (or `JWT_SECRET` when it is unset), in the
`hmac$<hex>` form of API keys, with a name, `created_at`, `last_used_at` and `revoked_at`.
Rotating that secret invalidates every token. A token is revoked, never deleted.

**Admin API.** Three routes under the API prefix, each guarded by `settings.scim_manage` (an
admin-only key: only the admin system role holds it, and no custom role can since #524):

- `GET /settings/scim/tokens` lists every token, revoked ones included, newest first.
- `POST /settings/scim/tokens` with `{"name": "..."}` (trimmed, 1 to 100 characters, else 400
  `VALIDATION_ERROR` with `details.name`) answers 201 with `{token, scim_token}`; `token` is the
  plaintext, never returned again.
- `DELETE /settings/scim/tokens/:id` revokes and answers 200 with the token; revoking a revoked
  token keeps its first `revoked_at`. An id that is not a positive integer is 400, an unknown id
  404.

A token's JSON is `id`, `name`, `created_at`, `last_used_at` and `revoked_at`: never the token
and never its hash. The SPA screen Settings > SCIM tokens (`/settings/scim-tokens`, #490) lists,
creates and revokes them; its route and navigation entry need `settings.scim_manage`, and a new
token is shown once, in a dialog, and kept nowhere else.

**Authentication.** Every `/scim/v2` request needs `Authorization: Bearer <token>` with a SCIM
token (the scheme word is case-insensitive, the token is not trimmed). A user's JWT or API key is
not a SCIM token and is refused, and a SCIM token is not accepted by the API. A successful
request updates `last_used_at` best-effort (a failed write is logged without the token and never
changes the answer) and writes no audit row. Every refusal is the same 401 whatever its reason,
and is counted in the sign-in audit row of its reason, client network and UTC minute (#553),
method `scim`, reason `token_missing`, `token_invalid` or `token_revoked` (see [Sign-in audit log](#sign-in-audit-log)). No token or hash is logged.

**Rate limit.** `/scim/v2` has two limits, applied inside the token authentication, per process,
whatever the HTTP method (#555). The SCIM token limit counts every request whose token is
accepted per token id, whatever the client address: `SCIM_RATE_LIMIT_PER_MINUTE` a minute
(default 600, maximum 6000) with a burst of `SCIM_RATE_LIMIT_BURST` (default 60, maximum 1000);
an unset, unparseable or below-1 value takes the default. Over it the request is answered 429,
reaches no handler and writes no audit row; accepted requests never count against the client
address. The SCIM refusal limit counts every refused request (missing, invalid or revoked token)
per client address: 10 requests a minute, burst 5. Within it a refusal is the 401 below with its
audit row; over it the answer is 429 and no audit row is written. Refusals never count against a
token. The refusal limit is judged after the token lookup, so a throttled refusal still costs one
indexed lookup; in exchange, refusals from an address a provider shares never hold back a valid
token. Both 429s use the SCIM error shape. `DISABLE_RATE_LIMIT=true` and the `gophercrm_e2e`
build switch lift both limits as they lift the `/auth` tier (the server refuses to start in
production mode with either one set).

**Errors.** Errors of the SCIM group use the RFC 7644 §3.12 shape with the content type
`application/scim+json`, for example
`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication failed"}`;
`status` is the HTTP status as a string. The 429 has `"status":"429"`. A 400 of the Users read
and writes also carries `scimType` (`invalidFilter`, `invalidValue` or `invalidSyntax`), and the
409 of a write carries `uniqueness`. The Groups add `mutability` (see [Groups](#groups-506)) and
a 405 without `scimType`.

**Discovery.** With a valid token, three documents answer 200 with `application/scim+json`;
query parameters are ignored and `meta.location` starts with `PUBLIC_BASE_URL/scim/v2`:

- `GET /scim/v2/ServiceProviderConfig`: `patch` supported, `filter` supported with `maxResults`
  100, `bulk` (with `maxOperations` and `maxPayloadSize` 0), `changePassword`, `sort` and `etag`
  not supported, and one `oauthbearertoken` authentication scheme.
- `GET /scim/v2/Schemas`: a `ListResponse` (`totalResults` 2, `startIndex` 1) with the core User
  schema (`urn:ietf:params:scim:schemas:core:2.0:User`): `userName` (required, not case-exact,
  unique on the server), `name` (`givenName`, `familyName`), `emails` (multi-valued: `value`,
  `type` with canonical values `work`, `home`, `other`, and `primary`) and `active`, each with
  every RFC 7643 §7 characteristic. Its `meta.location` is
  `PUBLIC_BASE_URL/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:User`. Since #506 the
  core Group schema (`urn:ietf:params:scim:schemas:core:2.0:Group`) follows: `displayName`
  (required, case-exact, read-only, unique on the server) and `members` (multi-valued, read-write,
  one sub-attribute `value`, case-exact and immutable).
- `GET /scim/v2/ResourceTypes`: a `ListResponse` with the `User` resource type, endpoint
  `/Users`, schema the User URN, `meta.location` `PUBLIC_BASE_URL/scim/v2/ResourceTypes/User`,
  then (#506) the `Group` resource type, endpoint `/Groups`, schema the Group URN,
  `meta.location` `PUBLIC_BASE_URL/scim/v2/ResourceTypes/Group`. The ServiceProviderConfig is
  unchanged: RFC 7643 §5 lists no resources, and its `patch` and `filter` cover the Groups.

The advertised `patch` feature is the Users patch below; the Users read, create, replace and
patch are served. These routes are outside the OpenAPI document (whose base path is the API prefix), like
`GET /health`. The SPA's nginx does not proxy `/scim/v2`: SCIM clients talk to the backend origin
directly.

### Users read (#469)

Two routes, behind the same rate limits and token authentication, answer with
`application/scim+json`:

- `GET /scim/v2/Users/{id}` answers 200 with one User resource, or 404 with the SCIM error body
  (`"detail":"User not found"`, the id is not echoed) when the id is not a positive decimal
  integer, names no user, or names an erased or customer account or an account not linked to the
  issuer.
- `GET /scim/v2/Users` answers a `ListResponse` with `totalResults`, `itemsPerPage` (the number
  of resources returned), `startIndex` and `Resources` (always an array, `[]` when empty), in
  ascending id order.

**Visibility.** SCIM serves and changes managed accounts only (#529). An account is managed when
its row is live (erasure soft-deletes, so an erased account is never served), its role is not
`customer` (the staff roles and `custom`), and it has at least one `user_identities` row whose
`issuer` equals the effective single sign-on issuer byte for byte, whatever its `source` (`scim`
from a SCIM create, `oidc` from a sign-in). The repository applies this in SQL
(`SCIMUserRepository`, an `EXISTS` on `user_identities` with a `LENGTH` guard, since the
`utf8mb4_bin` PAD SPACE collation alone would also match an issuer with a trailing space), so
`totalResults` and paging count managed accounts only. With no issuer configured no account is
managed. Every other account, an account created locally (such as the local administrator) or
linked only under another issuer included, is treated as an unknown id: absent from the list and
from a `userName eq` filter, 404 on read, replace, patch and delete, and refused as a group member.
Changing the issuer hides the accounts linked only under the old one, so `PUT /settings/sso` asks
for `confirm_issuer_change` first while links exist under the current issuer. A deactivated managed account
is served with `"active":false`.

**Resource.** `schemas` (the core User URN), `id` (the user id as a string), `externalId` (see
below), `userName` (the email), `name.givenName`/`name.familyName` (first and last name), one
`emails` entry (`value` the email, `type` `work`, `primary` true), `active` (`is_active`) and
`meta` (`resourceType` `User`, `created` and `lastModified` in RFC 3339 UTC, `location`
`PUBLIC_BASE_URL/scim/v2/Users/{id}`). No role, password or other attribute is served, and
`attributes`/`excludedAttributes` are ignored.

**externalId** is the subject of the user's `user_identities` row under the effective single
sign-on issuer (Settings > Single sign-on, else `OIDC_ISSUER_URL`; whether sign-on is enabled does
not matter), read once per request. The stored issuer must equal the configured one byte for
byte, judged in Go on the returned row: `user_identities` is `utf8mb4_bin` PAD SPACE on
MySQL/MariaDB, so an issuer differing only by a trailing space would otherwise match. With two
rows under that issuer the lowest id wins. Without a configured issuer, or without such a row,
`externalId` is omitted.

**Paging** (RFC 7644 §3.4.2.4). `startIndex` is 1-based; absent or below 1 it is 1. `count`
absent is 100; negative it is 0, above 100 it is 100, and 0 returns only `totalResults`. A
`startIndex` or `count` that is not a base-10 integer is 400 with `"scimType":"invalidValue"`.

**Filter.** The only filter is `userName eq "<value>"`: the attribute `userName`, optionally
prefixed by `urn:ietf:params:scim:schemas:core:2.0:User:`, and the operator `eq`, both
case-insensitive, then a JSON string literal (`\"` and `\u` escapes work), separated by one or
more spaces. Both the Entra ID shape (`filter=userName+eq+"a@contoso.test"`) and the Okta shape
(`filter=userName%20eq%20%22...%22&startIndex=1&count=100`) are accepted. Anything else is 400
with `"scimType":"invalidFilter"` and nothing is read: an empty filter, another attribute
(`externalId`, `emails.value`, `id`), another operator (`ne`, `co`, `sw`, `pr`), `and`/`or`/`not`,
parentheses, an unquoted or non-string value, or trailing tokens.

**userName matching.** The database is asked for candidates with
`LOWER(email) = LOWER(?)`, and every returned row is then judged in Go with ASCII-only case
folding: the same byte length, and every byte equal after mapping only `A`-`Z` to `a`-`z`. An
ASCII letter-case variant (`JOSE@X.TEST`) matches, as the schema's "matched case-insensitively"
says. `users.email` is `utf8mb4_unicode_ci` on MySQL/MariaDB (accent-insensitive, PAD SPACE), so
the database also returns padded (`"jose@x.test "`), accented (`josé@x.test`) and Unicode-fold
variants (`joſe@x.test` with U+017F LONG S, `Kate@x.test` with U+212A KELVIN SIGN); the judge
refuses them all, deliberately not with Unicode case folding. Paging then applies to the judged
rows. Errors of the read are logged without the filter value, an email, a name or a subject.

Regression roots of the read: see the end of the next section.

### Users create and replace (#470)

Two more routes behind the same rate limits and token authentication:

- `POST /scim/v2/Users` creates a staff or custom account linked to the identity provider and
  answers 201 with the User resource, a `Location` header equal to its `meta.location`, and
  `application/scim+json`.
- `PUT /scim/v2/Users/{id}` replaces the name, email, role and (when sent) `active` of a managed
  account and answers 200 with the resource.

**Body.** Any request `Content-Type` is accepted (`application/scim+json` or
`application/json`). The body is read up to 64 KiB and must be one JSON object. The attributes
read are `userName` (string, stored as the email), `externalId` (string), `name.givenName` and
`name.familyName` (strings), `active` (a JSON boolean only; the string form some providers send
is not accepted yet) and `roles` (absent, or an array of objects whose `value` is a string;
`display`, `type` and `primary` are ignored). Everything else is ignored, including `schemas`,
`emails`, `password` and extension schemas: a sent password is never used. `roles` is accepted on
write but neither served on the resource nor advertised in the discovery documents.

**Linking.** The created account gets one `user_identities` row: the effective single sign-on
issuer (whether sign-on is enabled does not matter) with `externalId` as the subject and
`source` `scim` (#505). A later OIDC sign-in with the same issuer and a `sub` equal to
`externalId` uses this account instead of provisioning one. Entra ID sends the `objectId` as
`externalId` while its `sub` is pairwise, so for an Entra ID issuer an unknown `sub` also finds
this account through the token's `oid` when its `tid` equals the configured issuer's tenant
segment (see **Identity** under OpenID Connect sign-in); any other provider must send as
`externalId` the value its ID tokens carry as `sub`.
`externalId` must be 1 to 255 printable ASCII bytes without leading or trailing whitespace. On
replace, `externalId` is ignored: the link set at creation never changes.

**Role.** The role always comes from the single sign-on role mapping: the first entry with claim
`roles`, in stored order, whose value equals one of the sent `roles` values exactly (letter case
included). Entries with claim `groups` never match. A system role is stored by its key with
`role_id` NULL, a custom role as `custom` with its id. A mapping that names no role row, or names
the `customer` system role, is refused: the SCIM Users resource serves staff and custom accounts
only, so a created customer could never be read back.

**Account.** A created account is active unless `active` is `false`, and its password is the
bcrypt hash of random bytes that are discarded, so it cannot sign in with a password. On replace
an absent `name` or sub-attribute clears it (PUT replaces), and an absent `active` leaves
`is_active` as it is. Since #471 `active: false` deprovisions as a patch does (see
[Users patch](#users-patch-471)): in the same transaction it increments the account's
`token_version`, revokes every refresh token and, since #526, every API key of the account, so
its sessions and keys end at once.

**Answers.** Error details are fixed and never echo a submitted value.

| Condition | Answer |
|---|---|
| Body over 64 KiB, not one JSON object, trailing data, or a read attribute of the wrong JSON type | 400 `invalidSyntax`, nothing is read |
| `externalId` absent or invalid (create); `userName` empty or over 255 bytes; a name part over 100 characters; no issuer configured or one over 512 bytes (create); no `roles` mapping entry matches; the mapped role is missing or `customer`; `userName` not an email address | 400 `invalidValue` |
| The last active admin would lose the admin role or be deactivated (replace) | 400 `invalidValue` |
| `userName` held by another account, live or erased, in any letter case; on create also an `externalId` already linked under the issuer | 409 `uniqueness` |
| Replace of an id that is not a positive integer, names no user, or names an erased or customer account or an account not linked to the issuer | 404 `User not found` |

The address format rule (the one `POST /users` applies) runs after the uniqueness checks, so on
MySQL and MariaDB, whose collation equates padded and accented variants with a stored address,
`"jose@x.test "` and `josé@x.test` answer 409 when `jose@x.test` exists; on SQLite a padded
address fails the format rule with 400. Either way nothing is written.

**Transaction and audit.** Each write is one governed transaction in the lock order of the OIDC
sign-in (`admin set → users → roles → user_identities`, see [CONCURRENCY.md](CONCURRENCY.md),
flow "SCIM user create and replace"). A create, and a replace that changes the role or
`role_id`, writes one `role_audit_log` row (`user_role_changed`, `auth_method` `scim`,
`actor_user_id` NULL); a replace that keeps the role writes none. Since #552 a replace that changes
`userName` writes `user_email_changed`, and one that changes `active` writes `user_activated` or
`user_deactivated`, the same way and before the role row; a create writes no such row. After the commit one
`auth_audit_log` row (method `scim`, outcome `success`) names the account. A refusal writes
nothing.

Regression roots: `TestSCIMRoutesWired`, `TestSCIMStrictTierWired`, `TestSCIMUsersWired` and
`TestSCIMUserWritesWired` (`cmd`), `TestSCIMAuth` (`internal/middleware`),
`TestSCIMTokenHandler`, `TestSCIMDiscoveryDocuments`, `TestSCIMUserHandler` and
`TestSCIMUserWriteHandler` (`internal/handler`), `TestSCIMTokenStore{SQLite,MySQL}`,
`TestSCIMUsersSQLite`/`TestSCIMUsersMySQL` and `TestSCIMUserWritesSQLite`/`TestSCIMUserWritesMySQL`
(`internal/service`; the MySQL roots also run on MariaDB and prove that the collations return
the variants the Go judges drop, and that the padded and accented variants of a stored address
are refused as taken), `TestParseUserFilter`, `TestUserNameMatches`, `TestUserResource`,
`TestWriteErrorType` and `TestWriteErrorTypeWriteTypes` (`internal/scim`),
`TestSCIMTokenSchemaSQLite` (`internal/models`) and `TestSCIMTokensMigration{Files,}`
(`cmd/migrate`). The patch (#471) adds `TestSCIMUserPatchWired` (`cmd`),
`TestSCIMUserPatchHandler` (`internal/handler`), `TestSCIMUserPatchSQLite`/`TestSCIMUserPatchMySQL`
(`internal/service`, also run on MariaDB), `TestParsePatch` (`internal/scim`) and
`TestConcurrencyDocSCIMUserPatch` (`internal/config`). The delete (#504) adds
`TestSCIMUserDeleteWired` (`cmd`), `TestSCIMUserDeleteHandler` (`internal/handler`) and
`TestSCIMUserDeleteSQLite`/`TestSCIMUserDeleteMySQL` (`internal/service`, also run on MariaDB),
and extends `TestConcurrencyDocSCIMUserPatch`.
The managed-account rule (#529) is proven by the local-account, other-issuer, padded-issuer,
empty-issuer and `oidc`-link subtests of `TestSCIMUsers{SQLite,MySQL}`,
`TestSCIMUserWrites{SQLite,MySQL}`, `TestSCIMUserPatch{SQLite,MySQL}`,
`TestSCIMUserDelete{SQLite,MySQL}` and `TestSCIMGroups{SQLite,MySQL}` (`internal/service`, the
MySQL roots also on MariaDB) and by `TestSCIMUsersWired`, `TestSCIMUserPatchWired`,
`TestSCIMUserDeleteWired` and `TestSCIMGroupsWired` (`cmd`).

### Users patch (#471)

`PATCH /scim/v2/Users/{id}`, behind the same rate limits and token authentication, applies an
RFC 7644 §3.5.2 PatchOp to a managed account and answers 200 with the User resource and
`application/scim+json`.

**Body.** One JSON object of at most 64 KiB, any `Content-Type`. `schemas` is ignored.
`Operations` must be a non-empty array; each operation is an object with a string `op` (`add`,
`replace` or `remove`, in any ASCII letter case, so Entra ID's `Replace` and `Add` pass), an
optional string `path` and a `value`. `add` and `replace` behave the same.

**Paths.** Attribute names match with ASCII-only case folding, without trimming and without a
schema URN prefix:

| Path | Value of `add`/`replace` | `remove` |
|---|---|---|
| `active` | a JSON boolean, or the string `true` or `false` in any ASCII case (Entra ID sends `"False"`) | 400 `invalidValue` |
| `userName` | a string, stored as the email | 400 `invalidValue` |
| `name` | an object; the given `givenName`/`familyName` are set, absent ones stay | clears both parts |
| `name.givenName`, `name.familyName` | a string | clears the part |
| `emails` | a non-empty array of objects with a string `value`; the entry with `primary: true` wins, then the first with `type` `work`, then a sole entry | 400 `invalidValue` |
| `emails[type eq "work"].value` | a string | 400 `invalidValue` |
| `roles` | an array of objects with a string `value` | 400 `invalidValue` |

Without a `path` (Okta's form, and Entra ID's value-object form), an `add` or `replace` value must
be an object whose keys are judged as paths by the same rules, for example
`{"op":"replace","value":{"active":false}}` or `{"op":"Add","value":{"name.givenName":"Ann"}}`;
dotted `name.*` keys apply after a `name` object in the same value. Unknown sub-attributes inside
`name`, `emails` and `roles` entries are ignored, as on PUT. Operations apply in array order and
the last write to an attribute wins. When any operation sets `userName`, `emails` values are still
validated but do not change the email, because `userName` is the email.

**Checks.** Only the touched attributes are checked, with the rules of PUT: `userName` and the
name parts on their resulting values, the address uniqueness and format only when the email
changes. `roles` resolves through the single sign-on role mapping as on PUT; an untouched role is
kept as stored, without re-mapping and without an audit row. The patch is applied to the row
locked in the transaction.

**Answers.** Error details are fixed and never echo a path or a value.

| Condition | Answer |
|---|---|
| Body over 64 KiB, not one JSON object, trailing data, no or an empty `Operations` array, an operation that is not an object, an `op` other than add/replace/remove, a non-string `path`, a path-less value that is not an object | 400 `invalidSyntax` |
| A path or path-less key outside the table (`externalId`, `displayName`, `name.formatted`, another filter, an extension URN) | 400 `invalidPath` |
| `remove` without a `path` | 400 `noTarget` |
| A value of the wrong type (`null` included), `remove` of `active`, `userName`, `emails` or `roles`, `emails` without a winning entry, and every `invalidValue` of PUT for the touched attributes | 400 `invalidValue` |
| The last active admin would lose the admin role or be deactivated | 400 `invalidValue` (`The last active admin must stay an active admin`) |
| A changed `userName` held by another account, live or erased | 409 `uniqueness` |
| An id that is not a positive integer, names no user, or names an erased or customer account or an account not linked to the issuer | 404 `User not found` |

**Session end.** A patch that sets `active` false (also on an already inactive account), like a
PUT with `"active": false`, deprovisions at once: after the `users` write the same transaction
increments `token_version`, so every access token issued before is refused with 401, and revokes
every `refresh_tokens` row and every API key of the account (#526, see
[Which events end which credentials](#which-events-end-which-credentials)). `active` true only
sets `is_active`: the account can sign in again, but no earlier access token, refresh token or
API key comes back. The administrator deactivation (`POST /users/{id}/deactivate`) ends the same
credentials.

**Transaction and audit.** One governed transaction in the order
`admin set → users → roles → refresh_tokens → api_keys` (see [CONCURRENCY.md](CONCURRENCY.md),
flow "SCIM user patch"). A changed role or `role_id` writes one `role_audit_log` row (`user_role_changed`,
`auth_method` `scim`, `actor_user_id` NULL); since #552 a changed `userName` writes
`user_email_changed` and a changed `active` `user_activated` or `user_deactivated`, the same way and
before the role row. `active` false on an inactive account writes no row (it still increments
`token_version`). After the commit one `auth_audit_log` row (method
`scim`, outcome `success`) names the account. A refusal writes nothing: no `users`,
`refresh_tokens`, `api_keys`, `role_audit_log` or `auth_audit_log` change.

**Provider mappings.** The default attribute mappings of Entra ID and Okta send more than this
list, for example `externalId` or `displayName` on update, and every such operation is refused
whole with 400 `invalidPath`. Trim the provider's attribute mappings to `userName`, `active`,
`name.givenName`, `name.familyName`, the work email and `roles` before turning provisioning on.

Regression roots: see the end of the previous section.

### Users delete (#504)

`DELETE /scim/v2/Users/{id}`, behind the same rate limits and token authentication, deactivates a
managed account the way a patch with `active` false does, and never erases it. The request body is
not read.

**Answers.**

| Condition | Answer |
|---|---|
| An active managed account, deactivated | 204, empty body |
| An already inactive managed account | 204, empty body; nothing is written |
| The last active admin | 400 `invalidValue` (`The last active admin must stay an active admin`) |
| An id that is not a positive integer, names no user, or names an erased or customer account or an account not linked to the issuer | 404 `User not found` |

**What it writes.** On an active account one `users` write sets `is_active` false; then, in the
same transaction, `token_version` becomes the locked value plus one, so every access token issued
before is refused with 401, and every `refresh_tokens` row and every API key of the account is
revoked (#526). The email,
names, role, `role_id`, the `user_identities` link and `deleted_at` (NULL) stay as they were: the
erasure of `DELETE /users/{id}` is never called. A later PATCH or PUT with `active` true
reactivates the account, without bringing back an earlier session or API key.

**Idempotence.** A repeat on the now inactive account answers 204 and commits nothing: no
`token_version` increment, no revocation, no audit row. This differs on purpose from a PATCH with
`active` false, which increments `token_version` again on an inactive row. Every refresh and
bearer check already refuses an inactive owner.

**Transaction and audit.** One governed transaction in the order
`admin set → users → refresh_tokens → api_keys → role_audit_log`, a prefix-compatible subsequence of the patch order; no
`roles` row is locked because no role is written (see [CONCURRENCY.md](CONCURRENCY.md), flow
"SCIM user patch"). The deactivation of an active account writes one `role_audit_log` row
`user_deactivated` (`auth_method` `scim`, `actor_user_id` NULL, #552). After the commit of a deactivation one
`auth_audit_log` row (method `scim`, outcome `success`) names the account. A refusal writes
nothing.

Regression roots: see the end of [Users create and replace](#users-create-and-replace-470).

### Groups (#506)

A SCIM Group is a distinct value of a role-mapping row with claim `groups` in the effective single
sign-on settings, read on every request; roles rows are not groups. `displayName` is the value,
compared byte for byte, and the `id` its unpadded base64url encoding. Groups come from the mapping
and cannot be created, renamed or deleted through SCIM. Membership is stored in
`scim_group_members` (`group_value` exact: `utf8mb4_bin` on MySQL/MariaDB, `BINARY` on SQLite;
unique per group and user; `user_id` references `users` with `ON DELETE RESTRICT`).

| Route | Answer |
|---|---|
| `GET /scim/v2/Groups` | 200 `ListResponse`, ascending by value, paged by `startIndex`/`count`; the only filter is `displayName eq "<value>"`, anything else 400 `invalidFilter` |
| `GET /scim/v2/Groups/{id}` | 200 Group with its managed members (live, not customer, linked to the issuer), id ascending; an id that does not decode or names no current group is 404 `Group not found` |
| `PATCH /scim/v2/Groups/{id}` | 200 Group; see below |
| `POST /scim/v2/Groups`, `DELETE /scim/v2/Groups/{id}` | 405 SCIM error with `Allow: GET` or `Allow: GET, PATCH` |

`PUT /scim/v2/Groups/{id}` is not routed (the router's 404).

**Patch.** The operations (`add`, `remove`, `replace`, any ASCII case) apply in order to the member
set: `add`/`replace` with path `members` or path-less with `{"members": [...]}`, `remove` with
`members[value eq "<id>"]`, or path `members` with a list or without a value (every member). A
`displayName` or any other path is 400 `invalidPath`, a remove without a path `noTarget`. Every id
an `add` or `replace` names must be a managed account (a deactivated one may be added), otherwise
the whole patch is 400 `invalidValue`, as for an unknown id; removing a non-member is a no-op, and a
member row is removed even when its account is not managed. Such a leftover row of an account that
is not managed (for example a local account's row stored before #529) is only deleted: the account
gets no role change, no `users` write and no audit row, and the last-admin rule does not count it as
losing the admin role.

**Roles.** Each added or removed managed account gets the role of the sign-in's first-match rule
on the groups claim, over its groups after the change (`oidcMatchRole` with the groups only); a
mapping to a missing role row or to the customer role refuses the patch with `invalidValue`. Since
#530 an active account left in no mapped group is deactivated instead: `is_active` false, its role
and `role_id` kept, no `user_role_changed` row, and `revokeAllCredentials` ends every access token,
refresh token and API key, as a SCIM deactivation does; an account already deactivated gets no
`users` write, only its membership row and its `auth_audit_log` row. A role that lacks a
permission the old one had (the old role's permission set holds a grant the new one does not
cover, so a move between sales and support counts too) ends the account's sessions through
`endSessions`, active or not: `token_version` + 1 and every refresh token revoked. Its API keys
keep working, with the new role. A raised or unchanged role ends nothing. A later reactivation is
an explicit administrator or SCIM `active` true write; the account's next group patch recomputes
its role. A group patch maps the groups claim only and a Users write the roles claim
only, so whichever writes last sets the role. Membership rows outlive mapping edits: removing,
re-adding or remapping a group value recomputes no role until that account's next group patch.
The sign-in does not read `scim_group_members`, and erasure leaves the rows in place.

**Last admin.** The rule judges the result: with at least one active admin before the patch (the
locked admins plus every touched account whose locked row is an active admin, so an admin that a
concurrent handover created while the patch waited still counts), the
patch is refused only when none is left after it (the locked admins the patch does not touch, the
touched ones that stay admin and are not deactivated, and the touched active accounts that become
admin; an account the patch deactivates is no longer an active admin), with 400
`mutability` and `The last active admin must stay an active admin`. A handover to another active
account passes, as a replace or as one patch that adds the new admin and removes the old one, also
when the old admin is left in no mapped group and so deactivated; removing the only active admin
from its only mapped group, and a replace with a deactivated account, are refused. The Users writes keep answering their last-admin
refusal with `invalidValue`.

**Transaction and audit.** One governed transaction, `admin set → users → roles →
scim_group_members → refresh_tokens → api_keys → role_audit_log` (see [CONCURRENCY.md](CONCURRENCY.md),
flow "SCIM group patch"); the membership writes, the `users` writes, the credential revocations
and the role audit rows commit together or not at all; each role change writes `user_role_changed` with `auth_method` `scim` and a NULL actor,
each member the patch deactivates one `user_deactivated` row the same way (#552), and after the commit one `auth_audit_log` success row (method `scim`) names each managed account whose
membership changed. A refusal, and a patch that changes nothing, write nothing.

Regression roots: `TestSCIMGroupsWired` (`cmd`), `TestSCIMGroupHandler` and
`TestSCIMDiscoveryDocuments` (`internal/handler`), `TestSCIMGroupsSQLite`/`TestSCIMGroupsMySQL`
(`internal/service`, also run on MariaDB, with the `last_admin_race` subtests),
`TestSCIMGroupResource`, `TestSCIMGroupID`, `TestSCIMParseGroupFilter` and
`TestSCIMParseGroupPatch` (`internal/scim`), `TestSCIMGroupMemberSchemaSQLite`
(`internal/models`) and `TestSCIMGroupMembersMigration{Files,}` (`cmd/migrate`).

## Least-privilege integration user

An API key has no scopes of its own. It acts with the permissions its owner holds at the moment
of each request (see [Custom roles and scopes](#custom-roles-and-scopes)), so a least-privilege
credential is a dedicated user on a custom role that holds only the keys the integration needs.
Use one user per integration: the role is then the complete list of what that integration can do,
and one integration can be stopped without touching another.

**Setup.** Three calls:

1. `POST /roles` with the name and the permission keys creates the custom role. The caller needs
   `roles.manage`.
2. `POST /users` with `role: custom` and the `role_id` of that role creates the integration user.
   The caller needs `users.create` and `users.assign_role`; a caller that is not on the admin
   system role is also bound by the [escalation rule](#escalation-rule). The password sent here
   is the integration user's login: keep it in the secret store.
3. `POST /api-keys` always creates a key for the caller, so it is sent signed in as the
   integration user. The plaintext key is in the answer's `key` field, once.

```bash
curl -s -X POST "$CRM/api/v1/roles" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ticket intake","permissions":[{"key":"customers.create"},{"key":"tickets.create"}]}'
curl -s -X POST "$CRM/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"intake@example.com","password":"<from the secret store>","first_name":"Ticket","last_name":"Intake","role":"custom","role_id":<id of the role>}'
curl -s -X POST "$CRM/api/v1/api-keys" -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"intake"}'
```

**Which keys.**

| Integration | Keys on the role | The request must |
|---|---|---|
| Ticket intake that creates customers and tickets | `customers.create`, `tickets.create` | Name `assigned_to_id` in `POST /tickets` |
| The same, looking a customer up before creating it | Add `customers.view` | (as above) |
| Form handler writing leads that the integration user owns | `leads.create`, `assignment.form_owner` | Send no `owner_id` in `POST /leads` |
| Form handler writing leads for a named owner | `leads.create`, `leads.assign_owner` | Name `owner_id` in `POST /leads` |

- *Tickets.* `POST /tickets` without `assigned_to_id` assigns the ticket to the caller, and an
  assignee must hold `assignment.ticket_assignee`. The integration user does not, so the request
  is answered 400 `BAD_REQUEST` and no ticket is written. Name the `assigned_to_id` of a user
  that holds `assignment.ticket_assignee` (the support and admin system roles do). The
  alternative is to grant the integration role `assignment.ticket_assignee`, which also makes the
  integration user a valid assignee for everyone else.
- *Reading.* With `customers.create` and `tickets.create` alone the integration cannot read
  anything, the records it created included. A second `POST /customers` with an email that
  exists is 409 `CONFLICT` and the answer does not carry the existing customer's id, so an
  integration that has to find the customer first also needs `customers.view`, which exists at
  scope `all` only.
- *Leads.* The CRM's own embedded forms post to the public `/forms/public` routes and need no
  key at all. An external form handler that writes through `POST /leads` needs `leads.create`
  and an eligible owner. With `leads.create` alone the lead would be owned by the caller, who
  cannot own leads: 400 `INVALID_REFERENCE`. Either grant `assignment.form_owner`, and the
  integration user owns the leads it creates when the body has no `owner_id`; or grant
  `leads.assign_owner`, and the body must name the `owner_id` of a user that holds
  `assignment.form_owner` (without `owner_id` the answer is 400 `BAD_REQUEST`).

Every other protected route answers such a key 403 `FORBIDDEN`: the user and role routes, the
exports and the listings among them. A key granted to the role later applies from the key's next
request, and so does a key taken away.

**What stays open.** The ten self-service routes of [Deny by default](#deny-by-default) need
authentication only, so the integration user can read and edit its own profile and create, list,
update and revoke its own API keys. Changing its own email needs the account's current password
in `current_password` (#394), so a leaked key alone cannot move the address a password reset is
sent to. A changed email also ends the reset links already issued for the account (#554). It cannot change its own role that way: `PUT /users/me` has
no role field, and `PUT /users/{id}` on its own id ignores `role`, `role_id` and `is_active`
from a caller without `users.assign_role` and `users.activate`. A request authenticated with an
API key cannot create keys or switch one on: `POST /api-keys`, and `PUT /api-keys/{id}` with
`is_active: true`, answer 403 `FORBIDDEN` to it (#525). Only a signed-in session (password or
single sign-on) creates keys, and only with a sign-in within the last 15 minutes: otherwise, and
for a token from `POST /auth/refresh`, it answers 403 `REAUTHENTICATION_REQUIRED` (#532). A key
expires one year after creation unless an earlier `expires_at` is sent, and never later. Customer
accounts cannot hold keys (403 `FORBIDDEN`).

**Rotate.** Create the successor with `POST /api-keys`, signed in as the integration user; deploy
it; then revoke the old key with `DELETE /api-keys/{id}` (or `PUT /api-keys/{id}` with
`is_active: false`), from either key or the session. Both keys work until the old one is
revoked. Every key expires at most one year after creation (one year when `expires_at` is not
sent), so rotate at least once a year; `expires_at` at creation forces an earlier rotation date and
cannot be changed afterwards. Creating the successor needs a sign-in within the last 15 minutes.

**Revoke.** `DELETE /api-keys/{id}` by the owner answers 200 and the key's next request is 401
`UNAUTHORIZED`. The row is kept with `is_active: false` and `revoked_at` stamped, and revocation
is final: `PUT /api-keys/{id}` with `is_active: true` answers 409 `CONFLICT` and changes nothing
(403 `FORBIDDEN` from a request authenticated with an API key). An administrator cannot list, read
or revoke another user's keys (403 `FORBIDDEN`); the administrator's switch is
`POST /users/{id}/deactivate`, which revokes every key of that user for good (its next request is
401, and `POST /users/{id}/activate` brings none back, #526). Changing or resetting the
integration user's password revokes every key of it too, so issue a new key afterwards. After a
suspected leak, deactivate the user, or revoke every key of the user and not only the leaked one,
then issue a new key.

`TestLeastPrivilegeIntegrationUserSQLite` and `TestLeastPrivilegeIntegrationUserMySQL` in
`internal/integration` build this setup through the three routes and prove each statement of
this section with the key: the two creates, 403 on the other routes, the lead and assignee
rules, rotation, revocation and deactivation.

## Phase 2 design note

This section is a design note for #261. Since #434 the single sign-on settings exist: seven
reserved configuration entries (the seventh, `allow_password`, since #457), the `OIDC_*` environment fallbacks behind them, the admin-only
`GET/PUT /settings/sso` pair guarded by `settings.sso_manage`, and the `SSOConfig` accessor that
returns the effective values:

- `security.sso.enabled` (`OIDC_ENABLED`); a read error keeps SSO off.
- `security.sso.allow_password` (`OIDC_ALLOW_PASSWORD`, #457), default false; a read error
  keeps it off. It decides whether an account linked to single sign-on may also use password
  sign-in and password reset (see "Passwords for linked accounts" under
  [OIDC sign-in](#oidc-sign-in-435) below). While it is on, a password keeps working after the identity provider removes the person
  or the mapped role, so offboarding must also deactivate the account in GopherCRM.
- `security.sso.issuer_url` (`OIDC_ISSUER_URL`), an https issuer of at most 512 bytes, the width
  of `user_identities.issuer`; a longer `OIDC_ISSUER_URL` is treated as unset. A save whose
  effective issuer differs from the current one while `user_identities` rows (any source, erased
  accounts included) exist under the current one answers 409 `ISSUER_CHANGE_UNCONFIRMED` with
  `details.affected_links`, the number of those rows, and stores nothing unless the body carries
  `"confirm_issuer_change": true`. Clearing the stored issuer so a different `OIDC_ISSUER_URL`
  applies is a change too.
- `security.sso.client_id` (`OIDC_CLIENT_ID`).
- `security.sso.client_secret` (`OIDC_CLIENT_SECRET`), encrypted at rest and never returned.
- `security.sso.scopes` (`OIDC_SCOPES`), default `openid profile email`.
- `security.sso.role_mapping` (`OIDC_ROLE_MAPPING`), encrypted at rest; an unreadable stored
  mapping is empty, never the environment one.

The generic `/configurations` endpoints neither list nor write those entries, whatever the
case or padding of the key. Since #435 the backend signs in with those settings (see
[OIDC sign-in](#oidc-sign-in-435) below): the authorization code flow with PKCE, identity
linking by `(iss, sub)` in `user_identities`, JIT provisioning and the role mapping re-applied
on every sign-in. Since #436 the SPA offers the sign-in (see
[SPA sign-in and settings](#spa-sign-in-and-settings-436) below). Since #451 an ID token without
a `groups` claim has its groups read from the issuer's userinfo endpoint when the mapping uses
groups (see Mapping below). Since #468 the first SCIM 2.0 slice exists: provider tokens and the
`/scim/v2` authentication and discovery documents (see [SCIM provisioning](#scim-provisioning));
#469 delivers the SCIM user read (`GET /scim/v2/Users` and `/Users/{id}`) and #470 the create
and replace (`POST /scim/v2/Users`, `PUT /scim/v2/Users/{id}`), and #471 the patch
(`PATCH /scim/v2/Users/{id}`), whose `active` false deactivates the account and ends its sessions (and, since #526, its API keys);
#504 the delete (`DELETE /scim/v2/Users/{id}`), the same deactivation, which never erases;
#506 the Groups (`GET /scim/v2/Groups`, `GET` and `PATCH /scim/v2/Groups/{id}`), the groups-claim
values of the role mapping, whose members patch recomputes the members' roles. SCIM bulk
operations, `/Schemas/{id}`, `/ResourceTypes/{id}` and creating, renaming or deleting a group
through SCIM are not implemented. The rest of the note
records the conditions phase 2 has to meet, which the sign-in below follows.

### OIDC sign-in (#435)

**Flow.** `POST /auth/oidc/start` (no body, strict `/auth` tier, no authentication) re-reads the
settings, discovers the issuer, stores one single-use `oidc_login_attempts` row (the HMAC of a
fresh state keyed like the other opaque tokens, the nonce, the PKCE verifier and the issuer,
valid ten minutes) and answers `{authorization_url}`. The SPA sends the browser there; the issuer
redirects back to the redirect URI `APP_BASE_URL/auth/callback`, which must be registered at the
provider exactly. The SPA then posts the `code` and `state` to `POST /auth/oidc/callback`, which
spends the attempt (its own committed statement: a state works once, even when the sign-in is
then refused), refuses a callback without the sign-in cookie or with another one (below),
refuses an attempt started against another issuer than the one configured now,
exchanges the code with the verifier, and checks the ID token's signature, `iss`, `aud`, `exp`
and nonce. Success answers the same `token`, `refresh_token` and `user` as `POST /auth/login`.
Single sign-on off is 404; on but unconfigured, undiscoverable (including a
redirect or a timeout; the issuer client follows no redirect and ignores `HTTP(S)_PROXY`) or with
a failing token endpoint is 503 `SSO_UNAVAILABLE`; every refusal is the same 401 "Single sign-on
failed", with the reason logged as an `error_code` only.

**Sign-in cookie (#528).** A sign-in finishes only in the browser that started it. A 200 from
`POST /auth/oidc/start` sets the cookie `gophercrm_oidc_state`, holding the state of the returned
URL: `Path=<API_PREFIX>/auth/oidc` (it reaches only start and callback), `Max-Age=600` (the
lifetime of the attempt), `HttpOnly`, `SameSite=Lax`, no `Domain`, and sent only over HTTPS
when `APP_BASE_URL` is `https`. A refused start sets no cookie. The callback spends the attempt first
and then compares the cookie with the posted state: no cookie is refused as `binding_missing`,
a different value (the cookie of another sign-in) as `binding_mismatch`, both with the same 401
and an audit row naming the spent attempt. A later start in the same browser replaces the
cookie, so only the latest sign-in started there can finish. A successful callback clears the
cookie (`Max-Age=0`); a refused one leaves it. The cookie authenticates nothing on its own and
no other route reads it. Neither its value nor the state is logged. Because the browser must
store and send it, the SPA and the API must be served from the same site (the same registrable
domain and scheme), as the shipped nginx proxy serves them from one origin; the SPA sends
credentials on these two calls only.

**Attempt hygiene and logs (#453).** A used or expired attempt has its verifier and nonce
cleared: the claim (a plain read by the state hash, then one conditional `UPDATE` by id) blanks
the nonce and the PKCE verifier in the same statement that spends the row, and a callback that
finds the row expired blanks them without spending it. When that clearing fails the callback
still answers the same 401 as for an unknown state. The stale bulk-operation sweep (at boot and
every 15 minutes, on every replica) prunes sign-in attempts older than one day; the sign-in
itself deletes nothing. Every `oidc_sign_in` log line carries the `request_id` of the HTTP
request (from `X-Request-ID` when the client sends one, logged only as a field) and the
`attempt_id` of the attempt row, `0` before the row exists or has been claimed (an unknown, used
or expired state). Since #454 the `Single sign-on refused` line, the `Single sign-on unavailable`
line of a 503 and the success line also carry `audit_id`, the id of the callback's
[sign-in audit](#sign-in-audit-log) row (`0` when that write failed).

**Identity.** The identity is the exact `(iss, sub)` pair in `user_identities` (unique,
`utf8mb4_bin` on MySQL/MariaDB, BINARY on SQLite). A `sub` that is empty, longer than 255 bytes,
not printable ASCII, or has leading or trailing whitespace is refused on every engine, because
the binary collation still pads trailing spaces; a stored row is also compared byte for byte.
`email`, `preferred_username` and `name` never link or authorize.

An unknown `(iss, sub)` can still name an existing account in one case (#505): the configured
issuer is Entra ID (host `login.microsoftonline.com`), the token's `tid` is a string equal byte
for byte to the issuer's tenant segment (the first path segment of
`https://login.microsoftonline.com/<tenant id>/v2.0`; no case folding, no trimming), and the
token's `oid` is a valid subject equal byte for byte to the subject of a `user_identities` row
with `source` `scim` under the same issuer, that is the `externalId` a SCIM create stored. The
sign-in then takes the known-identity path for that account (refused as `account_erased` or
`account_inactive` when the account is erased or deactivated, the mapping re-applied) and inserts
the `(iss, sub)` link with `source` `oidc` in the same transaction, so the next sign-in matches
`(iss, sub)` directly. A non-string `oid` or `tid`, another tenant, a non-Entra issuer, a
multi-tenant issuer (`common`, `organizations`) or an `oid` that matches a row a sign-in wrote
falls through to JIT provisioning unchanged. A concurrent first sign-in that committed the same
`(iss, sub)` first makes the insert fail on the unique index (`identity_taken`, nothing written).

**Mapping.** The role is the FIRST entry of the role mapping, in stored order, whose claim
(`groups` or `roles`) holds its value exactly, case-sensitively. Each claim must be absent or a
JSON array of strings. No match is 401 with nothing written. A token whose `_claim_names` has a
`groups` key (a group overage, #450) is handled three ways: when the mapping has no `groups`
entry the overage is ignored and `roles` decides; when the mapping has a `groups` entry and the
issuer is Entra ID (host `login.microsoftonline.com`), the group object ids are read from
Microsoft Graph (see the Entra ID example) and go through the same first-match mapping, any Graph
failure refusing with `groups_unavailable`; for any other issuer it is refused
(`group_overage`), never read as "no groups". `_claim_sources` is never read.

When the mapping has a `groups` entry, the ID token has no `groups` key at all (an empty `[]` is
a present claim) and no Entra ID overage is being resolved through Graph, the groups are read
from the issuer's userinfo endpoint (#451), after every other token check has passed. The
callback sends one `GET` to the `userinfo_endpoint` from discovery with the access token of the
same code exchange, within 5 seconds, through the issuer client (no redirect followed, no proxy),
and reads at most 1 MiB. The endpoint must be an absolute `https` URL without credentials, and
the answer must be a 200 `application/json` object: signed userinfo (`application/jwt`) is not
supported. The userinfo `sub` must be a string equal to the ID token's `sub`, byte for byte;
otherwise the sign-in is refused as `userinfo_subject_mismatch`. Only the `groups` member is
read (email, names, `roles` and everything else come from the ID token or not at all); it must
be an array of strings, and when it is absent the person has no groups, so a `roles` entry may
still match or the result is `no_mapping_match`. Any other failure (no endpoint in discovery, an
`http` endpoint, a non-200 answer or a redirect, the timeout, an oversized, non-JSON or malformed
body, a `groups` that is not an array of strings) refuses with `groups_unavailable`. The access
token is never logged or stored and the answer is not cached. A mapping without a `groups` entry
never calls userinfo, and an Entra overage still goes to Graph only, so a callback waits on at
most one of the two.

This also applies to Entra ID: Entra leaves the `groups` claim out for a person in no groups, or
when the groups claim is not configured on the app registration. With a `groups` entry in the
mapping such a person's sign-in now calls Entra's userinfo endpoint
(`https://graph.microsoft.com/oidc/userinfo`), and a failure there (for example an access token
issued for another API audience) refuses the sign-in as `groups_unavailable`, even when a
`roles` entry would have matched. An administrator who maps both groups and app roles should
know that a group-less person now depends on userinfo.

The mapping is re-applied on every sign-in through the governed role write (the mapping is the administrator's
grant, so the actor-key and escalation checks do not apply): the last active admin cannot be
demoted (401, nothing written, no session), a custom role is not given to a customer login, and
a changed role commits one `role_audit_log` row with `auth_method` `oidc` and the signed-in user
as actor. An unchanged role writes no row.

**Session limit (#548).** The refresh session a single sign-on sign-in starts (`sign_in_method`
`oidc`) ends at `session_started_at + 24` hours, a fixed limit with no setting, so the mapping is
re-applied at least once a day. Every refresh token of the session expires by then; a presented
`oidc` token at or past that instant, or one without a start (rows issued before the limit
existed carry a later `expires_at`), is the generic refresh refusal, audited `token_invalid` with
the owner's id, and nothing is written. An access token issued by the last allowed refresh stays
valid until its own expiry (`JWT_EXPIRY_HOURS`). A password change that renews the session keeps
the `oidc` method and start, so the limit carries over; past it the change is `409 SESSION_STALE`
(`session_stale`) before anything is written. Password sessions keep `REFRESH_TOKEN_EXPIRY_DAYS`.
Ending a session at once is SCIM deactivation or **Deactivate**.

**JIT provisioning.** An unknown identity that no SCIM link names (see **Identity**) creates an active user with the mapped role, the
`email` claim as given and `given_name`/`family_name` (cut to 100 bytes; when both are absent,
`name` split at the first whitespace into a first name and the rest as the last name, #453), and
a password that is
the hash of discarded random bytes, so password login cannot succeed for it. It needs an `email`
claim; when any account, live or erased, already holds that address in any letter case, the
sign-in is refused and nothing is linked to that account (`LOWER(email)` on every engine, so
SQLite refuses what the MySQL/MariaDB collation refuses). A concurrent first sign-in of the same
address or identity loses on the unique index and rolls back whole.

**Passwords for linked accounts (#444, #457).** An account with a `user_identities` row, which
a SCIM create or the first OIDC sign-in creates (an email never links an existing account) and erasure keeps,
is an account linked to single sign-on. Whether it may also use a password is the setting
`allow_password` in `/settings/sso` (`security.sso.allow_password`, environment fallback
`OIDC_ALLOW_PASSWORD`, default off; an unreadable stored value is off).

- **Off (the default).** `POST /auth/password-reset` issues nothing for a linked account (no
  token, no mail, the same 200 message), `POST /auth/password-reset/confirm` refuses its token
  with the generic 400 and leaves it unspent, and `POST /auth/login` answers the generic 401
  "Invalid email or password" even for the right password, writing nothing (no `last_login_at`,
  no lockout change, no refresh token). The identity provider's offboarding and the role mapping
  then decide who signs in.
- **On.** A linked account uses password sign-in and password reset exactly like a local one.
  **Warning:** a password keeps working after the person is removed from the identity provider
  or loses the mapped role, so offboarding must also deactivate the account in GopherCRM.

Accounts with no identity row are never affected. The setting is read once per request, before
the transaction; switching it off does not end sessions or refresh tokens issued before, and a
password set while it was on stops working at the next password login. Changes to it are not
audited yet (no SSO setting change writes an audit row).

**`email_verified`.** JIT provisioning does not require `email_verified=true` and does not read
the claim; the `email` claim is stored as given. Entra ID usually leaves the claim out of its ID
tokens, the email never links or authorizes an account, and while `allow_password` is off (the
default) reset is refused for a linked account, so an unverified address does not lead to a
password.

**Erasure.** Erasing a user keeps its `user_identities` row, linked to the erased user, so that
person's next sign-in is 401 rather than a fresh account. Deactivation also answers 401.

**Break-glass.** Password login stays available while single sign-on is on for local accounts
(those with no identity row), whatever `allow_password` says. An account linked to single sign-on
starts with no usable password; it can set and use one only while `allow_password` is on.

**Example: Okta.** Live Okta verification is manual; these are the steps.

1. In the Okta Admin Console, under Applications, create an app integration: sign-in method
   **OIDC - OpenID Connect**, application type **Web Application**, grant type **Authorization
   Code** (GopherCRM always sends PKCE S256 and the client secret).
2. Set the **Sign-in redirect URI** to `https://crm.example.com/auth/callback` (APP_BASE_URL
   `https://crm.example.com` followed by `/auth/callback`, exactly). Note the client id and client
   secret.
3. Under **Assignments**, assign the people or the groups who may sign in. Okta refuses the others
   before they reach the CRM.
4. Have the groups put into a `groups` claim:
   - **Org authorization server** (issuer `https://<org>.okta.com`): on the app's **Sign On** tab,
     edit the **Groups claim filter**: claim name `groups`, filter for example "Starts with"
     `crm-`. Okta puts the matching group names into the ID token; where it returns a thin token
     instead, GopherCRM reads them from the userinfo endpoint (below).
   - **Custom authorization server** (issuer `https://<org>.okta.com/oauth2/default` or another
     server): under Security > API > Authorization Servers > Claims add a claim named `groups`,
     value type Groups, the same filter, included in the ID token (or in userinfo only).
5. Request the `groups` scope: put it into `OIDC_SCOPES` (GopherCRM does not add it).
6. On the GopherCRM side configure, through `/settings/sso` (Settings > Single sign-on) or the
   environment, a role mapping by group name:

```
OIDC_ENABLED=true
OIDC_ISSUER_URL=https://<org>.okta.com/oauth2/default
OIDC_CLIENT_ID=<client id>
OIDC_CLIENT_SECRET=<client secret>
OIDC_SCOPES=openid profile email groups
OIDC_ROLE_MAPPING=[{"claim":"groups","value":"crm-admins","role_id":1},{"claim":"groups","value":"crm-sales","role_id":2}]
```

Okta group names are the values the filtered claim carries; keep them stable. For the org server
use `OIDC_ISSUER_URL=https://<org>.okta.com`. When Okta returns a thin ID token without a `groups`
claim, the callback reads `groups` from Okta's userinfo endpoint with the access token of the
sign-in (#451, see Mapping above): the userinfo `sub` must equal the ID token's
(`userinfo_subject_mismatch` otherwise), only `groups` is used, and any failure refuses as
`groups_unavailable`. Okta answers userinfo as plain JSON; signed `application/jwt` userinfo is
not supported.

**Example: Entra ID.** An app registration with a Web redirect URI
`https://crm.example.com/auth/callback`, a client secret, and app roles (for example `CRM.Admin`,
`CRM.Sales`) assigned to users or groups in the enterprise application. Map the `roles` claim:
app roles have no overage limit. Configure:

```
OIDC_ENABLED=true
OIDC_ISSUER_URL=https://login.microsoftonline.com/<tenant id>/v2.0
OIDC_CLIENT_ID=<application (client) id>
OIDC_CLIENT_SECRET=<client secret value>
OIDC_ROLE_MAPPING=[{"claim":"roles","value":"CRM.Admin","role_id":1},{"claim":"roles","value":"CRM.Sales","role_id":2}]
```

A mapping on `groups` uses group object ids. Above 200 groups Entra ID leaves the `groups` claim
out and sends `_claim_names` instead (the group overage). Since #450 the CRM then reads the
person's groups from Microsoft Graph:

- It happens only for an Entra ID issuer whose host is exactly `login.microsoftonline.com` and a
  role mapping with at least one `groups` entry, after the ID token, nonce, subject and issuer
  checks have passed. Without a `groups` entry the overage is ignored and no Graph call is made.
- For such a configuration `POST /auth/oidc/start` adds the scope
  `https://graph.microsoft.com/GroupMember.Read.All` (once, after the configured scopes; it is not
  stored in the settings). That is the delegated Microsoft Graph permission
  `GroupMember.Read.All`, which needs admin consent in the tenant: add it to the app registration
  and grant consent, or users of an overflowing group set cannot sign in.
- The callback sends `POST /v1.0/me/getMemberObjects` to `https://graph.microsoft.com` with
  `{"securityEnabledOnly": true}`, so only security groups count, and follows `@odata.nextLink`
  only to `https` URLs on that same host. Only the returned object ids are used, never group
  names.
- One 5-second budget covers every page of a sign-in, at most `OIDC_GRAPH_MAX_PAGES` pages are
  read (environment only, 1..50, default 10; any other value means 10), no redirect is followed
  and no proxy is used. A non-200 answer, a redirect, a malformed or oversized (over 1 MiB) page,
  a foreign next link, too many pages or the timeout refuse the sign-in (401, logged as
  `groups_unavailable`, nothing written). Graph groups that match no entry are
  `no_mapping_match`.
- The access token from the same code exchange is used for these requests only; it is never
  stored or logged, and Graph results are not cached.
- Sovereign clouds (`login.microsoftonline.us`, `login.chinacloudapi.cn`) are not supported: their
  overage is refused as `group_overage` because their Graph host differs.

Without an overage, a `groups` mapping and an ID token without a `groups` claim (a person in no
groups, or no groups claim configured) make the callback read Entra's userinfo endpoint instead
(see Mapping above); a failure there refuses the sign-in even when a `roles` entry would match.

App roles remain the recommended setup: they have no overage limit, need no extra permission and
no Graph call. Role ids are those of `GET /roles`.

### SPA sign-in and settings (#436)

Settings > Single sign-on (`/settings/sso`, shown and opened only with `settings.sso_manage`)
edits `GET`/`PUT /settings/sso`: the Enabled switch, the "Allow password sign-in and password
reset for accounts linked to single sign-on" switch (#457) with an always-shown offboarding
warning, the issuer URL, the client ID, the scopes
(one field, separated by spaces), the client secret and the role mapping. Each field says when
its value comes from the environment or the default. The client secret is write-only: the page
shows only "A client secret is set" or "No client secret is set", the input always starts empty,
a typed secret is sent once and cleared after the save, "Clear client secret" sends an empty
secret, and a save without either keeps the stored one. The role mapping rows (claim `groups` or
`roles`, a value, a role from the role list) are kept and sent in the order shown, and the first
matching row wins; a row whose role the list does not name, or every row when the role list
cannot be loaded, keeps its role id ("Role #id"). `PUT /settings/sso` is a full replacement, so
the first save stores every value shown, environment-derived ones included: from then on a
change to the matching `OIDC_*` variable has no effect on a saved field.

The login page reads `sso_enabled` from the public `GET /auth/registration` probe it already
makes, so no second request spends the strict `/auth` budget. Only when it is true does it show
"Sign in with single sign-on" below the password form; the button calls `POST /auth/oidc/start`
(with `withCredentials`, as the callback post does, so the browser keeps and returns the sign-in
cookie when the SPA and the API run on different ports of one site, as in development; #528)
and sends the browser to the returned authorization URL, and a failed start shows "Single sign-on
is unavailable. Sign in with your password." The issuer redirects to the public `/auth/callback`
page, which reads `code` and `state` once, removes them from the address bar with a replace
navigation, posts them once to `POST /auth/oidc/callback` and stores the session in
sessionStorage, as a password login without "Remember me" does (no remember-me choice survives
the redirect). Any failure, and an `error` parameter or a missing code or state, shows the one
generic message "Single sign-on failed. Try again or sign in with your password." with a link
back to the sign-in page; the page never shows or logs the code, the state or the tokens.
Password login stays available as break-glass.

### Browser tests against a local test issuer (#446)

`gocrm-ui/e2e/tests/sso.spec.ts` drives the real browser round trip (login page, issuer page,
`/auth/callback`, the app) against a disposable OpenID Connect issuer,
`scripts/e2e/oidc-issuer`. `make e2e` builds it, starts it on `https://127.0.0.1:<E2E_OIDC_PORT>`
(by default the first free port from 18291) and stops it when the run ends; the spec configures
single sign-on through Settings > Single sign-on with that issuer, a per-run client secret and a
`roles` mapping (`crm-admin` to Admin, `crm-sales` to Sales), and its `afterAll` switches single
sign-on off again and deletes the users it created. Run it alone with
`make e2e E2E_DB_DRIVER=sqlite SPECS="e2e/tests/sso.spec.ts"`; outside `make e2e` (no
`E2E_OIDC_ISSUER_URL`) it skips. Like every browser spec it runs in CI only for a release, on
MySQL, MariaDB and SQLite, so it reads and writes through the API only.

The issuer registers one client (`gophercrm-e2e`) whose only redirect URI is the SPA's
`/auth/callback`. Its authorization endpoint refuses, with a 400 page and never a redirect, an
unknown client, any other `redirect_uri`, a missing code challenge or a method other than `S256`,
and a missing `state` or `nonce`; a valid request shows a small form (subject, email, names, roles,
groups). Its token endpoint requires the client secret (basic or post), the same `redirect_uri`
and a verifier whose S256 hash is the challenge, spends a code once, and returns an RS256 ID token
with the authorization request's nonce. Its own checks are covered by
`go test ./scripts/e2e/oidc-issuer` and by the spec's last scenario, which acts as a client
itself.

The issuer serves the run's per-run test certificate (the one the AEO provider fixture uses). Only
the `-tags=gophercrm_e2e` backend can trust it, and only with `GOPHERCRM_E2E_AEO_TLS=1`:
`aeo.InstallE2ETestRoots`, called once at startup right after the configuration is loaded,
installs it as Go's fallback roots and refuses (the process exits before opening the database or
a listener) when `SERVER_MODE` is `production`, whether it comes from the environment or from
`.env`. A normal build has no such switch. The check against a live Entra ID tenant stays manual.

### Design conditions

**Sign-in.** OIDC authorization code flow with PKCE, against one or more configured issuers (an
Okta org or custom authorization server, an Entra ID tenant). The ID token is validated against
the issuer's published keys (issuer, audience, expiry, nonce).

**Identity linking.** A `user_identities` table links an external identity to a `users` row. It
is keyed by `(iss, sub)`, never by email: an email address can be reassigned or changed at the
provider, and matching on it would let one person take over another's account. The same holds
for `preferred_username` and the display name: none of them links or authorizes. The one
exception to "keyed by `(iss, sub)`" is an Entra ID first sign-in (#505): its pairwise `sub`
never equals the `objectId` SCIM provisioning stores, so an unknown `(iss, sub)` whose `tid` is
the configured tenant and whose `oid` is the subject of a SCIM-written identity under that issuer
signs in as that account and links the `(iss, sub)` pair. It is still never matched by email.

**JIT provisioning.** A first sign-in with an unknown `(iss, sub)` that no SCIM link names creates the account. It is a
least-privilege account: it gets the role the mapping below yields and nothing more, and an
identity that matches no mapping gets no access (deny by default, as in phase 1).

**Role mapping.** An IdP group or app role maps to a role id (a system role or a custom role),
in a table an administrator edits. The mapping is by the provider's stable identifier, not by a
display name:

- Entra ID sends groups as object ids, at most 200 in a token. Above that the groups claim is
  left out and the token carries the group overage claim (`_claim_names`) instead; that case has
  to be handled explicitly, never read as "no groups" or as "every group". An app role arrives in
  the `roles` claim and has no overage, so app roles are the preferred mapping there.
- Okta sends a groups claim, filtered in the application's configuration, in the ID token; where
  the flow returns a thin token the groups are read from the userinfo endpoint.

The mapping is applied again on every sign-in. A mapped role change goes through the same
governed write as a manual one, so the last-admin invariant and the audit trail apply to it: a
change at the provider can never leave zero active admins.

**SCIM 2.0 (optional).** Provisioning and deprovisioning pushed by the provider, under its own
bearer credential: not a user's API key and not a JWT. Deprovisioning deactivates; it does not
erase.

**Local login.** Local password login is kept as the break-glass path, for at least one admin
account, so a provider outage or a wrong mapping cannot lock the installation out. The last-admin
invariant already protects that account.

**Sessions.** Authentication is header-only today (`Authorization: Bearer` or `ApiKey`), no
cookie carries a credential, and the server ships no CSRF protection; the unused CSRF middleware
was removed in #363 (ticket #362). If phase 2 introduces a cookie session, it must ship with
session-bound CSRF protection in the same change.

**Seams already in place.**

- `role_audit_log.auth_method` records how the acting request authenticated. Its values today
  are `bearer`, `api_key`, since #435 `oidc`, since #470 `scim` and since #524 `migration`
  (`AuthMethodBearer`, `AuthMethodAPIKey`, `AuthMethodOIDC`, `AuthMethodSCIM`,
  `AuthMethodMigration` in `internal/service/interfaces.go`); SCIM added a value, not a column.
- `role_audit_log.actor_user_id` is nullable, so a change made by a provisioning client that is
  not a user is recorded without inventing an account: a SCIM write stores NULL (#470), and so
  does a data step (#524).
- Permissions are resolved per request from the user's role, so a role mapped at sign-in needs
  no new enforcement path.
