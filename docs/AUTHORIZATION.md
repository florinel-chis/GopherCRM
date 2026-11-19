# Authorization

Who may do what in GopherCRM: the permission catalogue, the fixed matrices of the four system
roles, custom roles, and the rules that govern role changes. It describes the code on `main`; the
sign-in design at the end is a note for later work and says so.

The work is tracked in #258 and specified in #259. The source of truth for every table below is
`internal/permissions`; `go test -count=1 ./internal/permissions` fails when this guide and the
package disagree (see the Testing section of [DEVELOPMENT.md](DEVELOPMENT.md)).

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
| Phase 2 | #261 | Single sign-on, identity linking and provisioning (see the design note below) | Open: the SSO settings (`/settings/sso`, `settings.sso_manage`) delivered by #434; the backend OIDC sign-in (`POST /auth/oidc/start`, `POST /auth/oidc/callback`) delivered by #435; the SPA login button, the `/auth/callback` page and Settings > Single sign-on delivered by #436; SCIM is later work |

Since #380 the SPA reads the permissions of `GET /users/me` and shows routes, navigation entries
and buttons by permission key, so a user on a custom role sees what its role grants. Since #381
custom roles are created, cloned, edited and deleted in Settings > Roles (`/settings/roles`,
shown with `roles.view`, written with `roles.manage`) and assigned in the user form, whose role
picker lists the roles of `GET /roles`; the role API stays available for the same operations.

## Permission catalogue

A permission is a leaf of the catalogue tree, keyed `<group>.<action>`. The tree is the
`catalogue` variable in `internal/permissions/catalogue.go`; `permissions.Leaves()` returns the
leaves in display order and `GET /permissions/catalogue` serves the tree. There are 75 leaves in
19 groups. Sixteen can be held at scope `own` as well as `all` (`AllowsOwn`). One,
`tickets.view_own_customer`, is system-only (`SystemOnly`): only the customer system role holds
it, and no custom role can.

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
| `settings.sso_manage` | View and change single sign-on settings | all |
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
admin holds every leaf that is not system-only at `all` (74 grants); sales holds 37, support 17
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
catalogue does not know, a system-only key and a scope the leaf does not allow are dropped when
the set is resolved, and refused with 400 `INVALID_PERMISSION` when a role is written.

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
holding every permission does not.

## Escalation rule

A caller that is not on the admin system role cannot grant permissions above its own (create,
update or clone a role, assign a role, create a user with a role), cannot edit or delete a role
whose permissions are above its own, and cannot edit, activate, deactivate or erase a user whose
permissions are above its own. `own` is below `all`, and a missing key is below `own`. The
refusal is 403 `ROLE_ESCALATION`, and the rule applies to the caller's own account too. The
customer system role counts as the empty set here, so a customer login is never above a manager.

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
(`auth_method`: `bearer` or `api_key`, or `oidc` for the role an OpenID Connect sign-in applied
to its own account, #435), the action and the state before and after, as ids, role
names, permission keys and scopes only. `GET /roles/audit` lists the rows, newest first, and
needs `roles.audit_view`.

The three rules above are decided inside one transaction per write, after the rows are locked.
The lock order is in [CONCURRENCY.md](CONCURRENCY.md#rbac-user-role-and-role-writes), flow "RBAC
user-role and role writes"; the order of the refusals, the error codes and the persistence layer
are in [DEVELOPMENT.md](DEVELOPMENT.md), "RBAC persistence" and "Role API and governed user
writes".

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
sent to. It cannot change its own role that way: `PUT /users/me` has
no role field, and `PUT /users/{id}` on its own id ignores `role`, `role_id` and `is_active`
from a caller without `users.assign_role` and `users.activate`. It can create further keys,
though, and those never expire unless `expires_at` is sent: whoever holds a leaked key can mint
more keys for the same user.

**Rotate.** Create the successor with `POST /api-keys`, authenticated with the current key or
signed in as the integration user; deploy it; then revoke the old key with
`DELETE /api-keys/{id}` (or `PUT /api-keys/{id}` with `is_active: false`). Both keys work until
the old one is revoked. `expires_at` at creation forces a rotation date; it cannot be changed
afterwards.

**Revoke.** `DELETE /api-keys/{id}` by the owner answers 200 and the key's next request is 401
`UNAUTHORIZED`. Revocation is a flag, not a deletion: the owner, or any other key of the same
user, can set the key active again with `PUT /api-keys/{id}`. An administrator cannot list, read
or revoke another user's keys (403 `FORBIDDEN`); the administrator's switch is
`POST /users/{id}/deactivate`, which stops every key of that user at its next request (401).
After a suspected leak, deactivate the user, or revoke every key of the user and not only the
leaked one, then issue a new key.

`TestLeastPrivilegeIntegrationUserSQLite` and `TestLeastPrivilegeIntegrationUserMySQL` in
`internal/integration` build this setup through the three routes and prove each statement of
this section with the key: the two creates, 403 on the other routes, the lead and assignee
rules, rotation, revocation and deactivation.

## Phase 2 design note

This section is a design note for #261. Since #434 the single sign-on settings exist: six
reserved configuration entries, the `OIDC_*` environment fallbacks behind them, the admin-only
`GET/PUT /settings/sso` pair guarded by `settings.sso_manage`, and the `SSOConfig` accessor that
returns the effective values:

- `security.sso.enabled` (`OIDC_ENABLED`); a read error keeps SSO off.
- `security.sso.issuer_url` (`OIDC_ISSUER_URL`), an https issuer.
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
[SPA sign-in and settings](#spa-sign-in-and-settings-436) below). SCIM is not implemented, and
Okta's userinfo groups for thin tokens are not read; those are later work. The rest of the note records
the conditions phase 2 has to meet, which the sign-in below follows.

### OIDC sign-in (#435)

**Flow.** `POST /auth/oidc/start` (no body, strict `/auth` tier, no authentication) re-reads the
settings, discovers the issuer, stores one single-use `oidc_login_attempts` row (the HMAC of a
fresh state keyed like the other opaque tokens, the nonce, the PKCE verifier and the issuer,
valid ten minutes) and answers `{authorization_url}`. The SPA sends the browser there; the issuer
redirects back to the redirect URI `APP_BASE_URL/auth/callback`, which must be registered at the
provider exactly. The SPA then posts the `code` and `state` to `POST /auth/oidc/callback`, which
spends the attempt (its own committed statement: a state works once, even when the sign-in is
then refused), refuses an attempt started against another issuer than the one configured now,
exchanges the code with the verifier, and checks the ID token's signature, `iss`, `aud`, `exp`
and nonce. Success answers the same `token`, `refresh_token` and `user` as `POST /auth/login`;
no cookie is set. Single sign-on off is 404; on but unconfigured, undiscoverable (including a
redirect or a timeout; the issuer client follows no redirect and ignores `HTTP(S)_PROXY`) or with
a failing token endpoint is 503 `SSO_UNAVAILABLE`; every refusal is the same 401 "Single sign-on
failed", with the reason logged as an `error_code` only.

**Identity.** The identity is the exact `(iss, sub)` pair in `user_identities` (unique,
`utf8mb4_bin` on MySQL/MariaDB, BINARY on SQLite). A `sub` that is empty, longer than 255 bytes,
not printable ASCII, or has leading or trailing whitespace is refused on every engine, because
the binary collation still pads trailing spaces; a stored row is also compared byte for byte.
`email`, `preferred_username` and `name` never link or authorize.

**Mapping.** The role is the FIRST entry of the role mapping, in stored order, whose claim
(`groups` or `roles`) holds its value exactly, case-sensitively. Each claim must be absent or a
JSON array of strings. No match is 401 with nothing written. A token whose `_claim_names` has a
`groups` key (the Entra ID group overage) is refused, never read as "no groups". The mapping is
re-applied on every sign-in through the governed role write (the mapping is the administrator's
grant, so the actor-key and escalation checks do not apply): the last active admin cannot be
demoted (401, nothing written, no session), a custom role is not given to a customer login, and
a changed role commits one `role_audit_log` row with `auth_method` `oidc` and the signed-in user
as actor. An unchanged role writes no row.

**JIT provisioning.** An unknown identity creates an active user with the mapped role, the
`email` claim as given and `given_name`/`family_name` (cut to 100 bytes), and a password that is
the hash of discarded random bytes, so password login cannot succeed for it. It needs an `email`
claim; when any account, live or erased, already holds that address in any letter case, the
sign-in is refused and nothing is linked to that account (`LOWER(email)` on every engine, so
SQLite refuses what the MySQL/MariaDB collation refuses). A concurrent first sign-in of the same
address or identity loses on the unique index and rolls back whole.

**Erasure.** Erasing a user keeps its `user_identities` row, linked to the erased user, so that
person's next sign-in is 401 rather than a fresh account. Deactivation also answers 401.

**Break-glass.** Password login is unchanged and stays available while single sign-on is on.

**Example: Okta.** An OIDC web application with the authorization code grant and PKCE; sign-in
redirect URI `https://crm.example.com/auth/callback` (APP_BASE_URL `https://crm.example.com`). On
the custom authorization server add a `groups` claim to the ID token, filtered to the CRM groups
(for example "Starts with `crm-`"). Configure, through `/settings/sso` or the environment:

```
OIDC_ENABLED=true
OIDC_ISSUER_URL=https://<org>.okta.com/oauth2/default
OIDC_CLIENT_ID=<client id>
OIDC_CLIENT_SECRET=<client secret>
OIDC_SCOPES=openid profile email groups
OIDC_ROLE_MAPPING=[{"claim":"groups","value":"crm-admins","role_id":1},{"claim":"groups","value":"crm-sales","role_id":2}]
```

Okta group names are the values the filtered claim carries; keep them stable. A flow that returns
a thin ID token without groups is not supported yet (the userinfo endpoint is not read).

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
out and sends `_claim_names` instead; such a sign-in is refused (401), so prefer app roles. Role
ids are those of `GET /roles`.

### SPA sign-in and settings (#436)

Settings > Single sign-on (`/settings/sso`, shown and opened only with `settings.sso_manage`)
edits `GET`/`PUT /settings/sso`: the Enabled switch, the issuer URL, the client ID, the scopes
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
and sends the browser to the returned authorization URL, and a failed start shows "Single sign-on
is unavailable. Sign in with your password." The issuer redirects to the public `/auth/callback`
page, which reads `code` and `state` once, removes them from the address bar with a replace
navigation, posts them once to `POST /auth/oidc/callback` and stores the session in
sessionStorage, as a password login without "Remember me" does (no remember-me choice survives
the redirect). Any failure, and an `error` parameter or a missing code or state, shows the one
generic message "Single sign-on failed. Try again or sign in with your password." with a link
back to the sign-in page; the page never shows or logs the code, the state or the tokens.
Password login stays available as break-glass.

### Design conditions

**Sign-in.** OIDC authorization code flow with PKCE, against one or more configured issuers (an
Okta org or custom authorization server, an Entra ID tenant). The ID token is validated against
the issuer's published keys (issuer, audience, expiry, nonce).

**Identity linking.** A `user_identities` table links an external identity to a `users` row. It
is keyed by `(iss, sub)`, never by email: an email address can be reassigned or changed at the
provider, and matching on it would let one person take over another's account. The same holds
for `preferred_username` and the display name: none of them links or authorizes.

**JIT provisioning.** A first sign-in with an unknown `(iss, sub)` creates the account. It is a
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
  are `bearer`, `api_key` and, since #435, `oidc` (`AuthMethodBearer`, `AuthMethodAPIKey`,
  `AuthMethodOIDC` in `internal/service/interfaces.go`); a SCIM call would add `scim`, not a
  column.
- `role_audit_log.actor_user_id` is nullable, so a change made by a provisioning client that is
  not a user can be recorded without inventing an account.
- Permissions are resolved per request from the user's role, so a role mapped at sign-in needs
  no new enforcement path.
