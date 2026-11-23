# Roles and Permissions — Test Cases

Cases for custom roles and permissions (#259, #348): the role API behind Settings > Roles, the
permission catalogue, the own and all scopes, the escalation rule, the role assignment of a user,
the last-admin rule and the role audit log with its screen (#552). The role list and editor
screens, and a cloned role reaching its holder, are TC-SET-035 in [08-settings.md](08-settings.md);
the role picker of the user form is TC-USER-041 and TC-USER-042 in [07-users.md](07-users.md).
This file does not repeat them.

Every **Expected** states what the build does **today**, traced to the handlers, services and
repositories; every automated case names the exact test that pins it. The Go roots run on
in-memory SQLite; the `*MySQL` roots need `TEST_MYSQL_DSN` and skip without it.

23 cases: 20 automated (18 Go, 2 Playwright), 3 planned, 0 blocked.
Priorities: 11 P0, 10 P1, 2 P2.

## Sources

- `internal/handler/routes.go` `SetupRoleRoutes`, `internal/permissions/routes.go` `RouteKeys`
- `internal/handler/role_handler.go` (`RoleHandler.Catalogue`, `RoleHandler.ListAudit`,
  `respondRoleGovernanceError`)
- `internal/service/role_service.go` (`roleService.Create`, `roleService.Update`,
  `roleService.Clone`, `roleService.Delete`, `validateRolePermissions`)
- `internal/service/role_governance.go` (`checkEscalation`, `permissionsExceed`, `escalationSet`)
- `internal/service/user_governed_writes.go` (`CheckRoleAssignment`, `userService.UpdateAs`),
  `internal/service/last_admin_guard.go` (`adminSet.checkAfter`)
- `internal/permissions/catalogue.go` (`Catalogue`, `NewSet`, `Node.CustomRoleMayHold`),
  `internal/permissions/system_roles.go` (`ForSystemRole`, `ForCustomRole`, `ForUser`)
- `internal/models/role.go` (`PermissionScopeOwn`, `PermissionScopeAll`, `RoleNameKey`),
  `internal/repository/role_repository.go`, `internal/repository/role_audit_repository.go`
- `internal/errors/errors.go` (`ErrRoleNameTaken`, `ErrRoleInUse`, `ErrSystemRoleImmutable`,
  `ErrRoleEscalation`, `ErrLastAdmin`, `ErrInvalidRoleAssignment`, `ErrCustomerLoginRole`)
- `gocrm-ui/src/pages/settings/RoleAudit.tsx`, `gocrm-ui/src/pages/settings/RoleEditor.tsx`,
  `gocrm-ui/src/pages/settings/CloneRoleDialog.tsx`, `gocrm-ui/src/pages/settings/RoleList.tsx`,
  `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/layouts/MainLayout.tsx`
- `gocrm-ui/e2e/tests/roles.spec.ts`, `gocrm-ui/e2e/tests/role-audit.spec.ts`,
  `gocrm-ui/e2e/helpers/sso.ts`
- `internal/integration/role_api_integration_test.go`,
  `internal/integration/role_escalation_integration_test.go`,
  `internal/integration/role_last_admin_integration_test.go`,
  `internal/integration/role_audit_integration_test.go`,
  `internal/integration/permission_next_request_integration_test.go`,
  `internal/service/role_governance_test.go`, `internal/permissions/catalogue_test.go`,
  `cmd/system_role_route_matrix_test.go`
- `docs/user-guide/permissions-access/roles-and-permissions.md`

## Constraints

- **Only the admin system role holds the role keys.** `roles.view`, `roles.manage` and
  `roles.audit_view` are in no other system matrix; a custom role may hold them. `GET /roles`,
  `GET /roles/:id` and `GET /permissions/catalogue` open with `roles.view` or `users.assign_role`;
  every write needs `roles.manage`; `GET /roles/audit` needs `roles.audit_view`.
- **The four system roles are fixed.** Admin, Sales, Support and Customer answer with the code
  matrix (`internal/permissions/system_roles.go` `ForSystemRole`), never with stored rows; they
  can be cloned, not edited or deleted.
- **Every role write is one transaction.** It locks the actor's users row, then the role rows in
  ascending id, re-reads the actor's permissions and decides only then; the audit row commits with
  the change, and a refusal writes neither.
- **The admin system role is exempt from the escalation rule**; a custom role holding every key it
  may hold is not.
- **Accounts for planned browser cases** come from `APIClient.createUser` (a system role only: it
  sends no `role_id`) and `APIClient.post('/users', ...)` for a custom role, both in
  `gocrm-ui/e2e/helpers/sso.ts`; `APIClient.login` signs such an account in through the API. A
  browser session signs in through the login form, as `gocrm-ui/e2e/tests/role-audit.spec.ts`
  does. Specs restore what they change: users are erased before the roles they hold, since a held
  role cannot be deleted.

## Role API: create, rename, clone, delete

### TC-ROLE-001 — Create a custom role
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Bearer token of the seeded admin; no role named "Pipeline keeper".
- **Steps:**
  1. `POST /roles` with `{"name": "Pipeline keeper", "description": "Keeps the pipeline",
     "permissions": [{"key": "leads.view", "scope": "own"}, {"key": "deals.view"}]}`.
  2. `GET /roles/<id>` and `GET /roles?limit=100`.
- **Expected:** Step 1: **201** in the unified envelope with `system_key` null, `is_system` false,
  `user_count` 0 and the permissions sorted by key (`deals.view` at `all`, since an omitted scope
  is `all`, and `leads.view` at `own`); one `role_created` audit row. Step 2: **200**; the list
  holds the four system roles and the new one, Admin with its 76 code-matrix permissions. A name
  that is blank after trimming or longer than 100 characters, or a description over 500, is
  **400** `VALIDATION_ERROR`; an unknown id on `GET /roles/:id` is **404** `ROLE_NOT_FOUND`, a
  non-numeric one **400** `BAD_REQUEST`.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` and `TestRoleAPIMySQL` (subtests "create" and "get and list");
  `internal/repository/role_repository_test.go` `TestRoleRepositorySQLite`

### TC-ROLE-002 — A role name another role has is ROLE_NAME_TAKEN
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin token; a custom role "Pipeline owner".
- **Steps:**
  1. `POST /roles` with the name `"  pipeline OWNER "`.
  2. `POST /roles` with the name `"sales"`.
  3. `PUT /roles/<another custom role>` and `POST /roles/<id>/clone` with the name "Pipeline owner".
- **Expected:** Each answers **409** `ROLE_NAME_TAKEN` ("A role with this name already exists"):
  names compare trimmed and lower-cased (`internal/models/role.go` `RoleNameKey`), system roles
  included. No role and no audit row is written. The editor and the clone dialog show "Another
  role already has this name" on the name field (TC-SET-035).
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` (subtest "duplicate name"); `internal/models/role_test.go` `TestRoleNameKey`

### TC-ROLE-003 — Rename a custom role and replace its permissions
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin token; the custom role of TC-ROLE-001.
- **Steps:**
  1. `PUT /roles/<id>` with `{"name": "Pipeline owner", "permissions": [{"key": "deals.view",
     "scope": "own"}, {"key": "quotes.view"}]}` and no `description`.
  2. `PUT /roles/<id>` with only a `name`.
- **Expected:** Step 1: **200**; the name and the permission set are replaced (`deals.view` at
  `own`, `quotes.view` at `all`) and the omitted description is kept; one `role_updated` audit
  row with the before and after state. Step 2: **200**; an omitted `permissions` field keeps the
  stored set, while `"permissions": []` would leave the role without any. A missing id is **404**
  `ROLE_NOT_FOUND`. The holders' next request already uses the new set (TC-SET-035).
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` and `TestRoleAPIMySQL` (subtest "update renames and replaces the
  permissions"); `internal/integration/permission_next_request_integration_test.go`
  `TestPermissionChangeNextRequestSQLite`

### TC-ROLE-004 — Clone a system role or a custom role
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin token; a custom role; the seeded system roles.
- **Steps:**
  1. `POST /roles/<custom id>/clone` with `{"name": "Pipeline copy"}`.
  2. `POST /roles/<Sales id>/clone` with `{"name": "Sales copy"}`.
  3. `POST /roles/<Customer id>/clone` and `POST /roles/<Admin id>/clone`, each with a new name.
- **Expected:** Each answers **201** with a new custom role (`system_key` null) and a
  `role_created` audit row; the source's description is not copied. Step 1: the source's stored
  set. Step 2: the Sales code matrix, `leads.view` at `own` included. Step 3: keys a custom role
  cannot hold are dropped (`internal/permissions/system_roles.go` `ForCustomRole`): the Customer
  copy holds `labels.view`, `tasks.update` at `own` and `tasks.view` at `own`, without
  `tickets.view_own_customer`; the Admin copy holds neither `settings.sso_manage` nor
  `settings.scim_manage`. A missing source is **404** `ROLE_NOT_FOUND`; a taken name is
  TC-ROLE-002.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` and `TestRoleAPIMySQL` (subtest "clone"),
  `TestAdminOnlySettingsPermissionsSQLite`

### TC-ROLE-005 — Delete a custom role nobody holds
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin token; a custom role without live holders.
- **Steps:**
  1. `DELETE /roles/<id>`.
  2. `DELETE /roles/<id>` again.
- **Expected:** Step 1: **204** with no body; the role and its `role_permissions` rows are gone
  and one `role_deleted` audit row holds the before state (after state null). Step 2: **404**
  `ROLE_NOT_FOUND`. An erased holder does not block the delete: its `role_id` is cleared in the
  same transaction.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` (subtest "delete"), `TestRoleAPIDeleteAssignedRoleSQLite` (subtest "an
  erased holder does not block and loses its role_id")

### TC-ROLE-006 — Deleting a role that users hold is ROLE_IN_USE
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin token; a custom role held by two live users; a second role held by one
  deactivated user.
- **Steps:**
  1. `DELETE /roles/<first id>`.
  2. `DELETE /roles/<second id>`.
- **Expected:** **409** `ROLE_IN_USE` ("The role is still assigned to users") with
  `details.user_count` 2, then 1: a deactivated holder counts. The role, its permissions and the
  audit log are unchanged. An assignment that commits while the delete runs is refused by the
  foreign key and answers the same **409**, never a 500 (`internal/service/role_service.go`
  `roleService.Delete`). In the UI the confirmation stays open with the holder count (TC-SET-035).
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPIDeleteAssignedRoleSQLite`

### TC-ROLE-007 — System roles cannot be edited or deleted
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin token.
- **Steps:**
  1. `PUT /roles/<Sales id>` with `{"name": "Sales renamed"}`.
  2. `DELETE /roles/<Sales id>`.
- **Expected:** Both answer **409** `SYSTEM_ROLE_IMMUTABLE` ("A system role cannot be edited or
  deleted; clone it instead"), also for the admin; the row and the audit log are unchanged.
  Cloning the same role succeeds (TC-ROLE-004). The editor opens a system role read-only with a
  Clone button (TC-SET-035).
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` and `TestRoleAPIMySQL` (subtest "system roles are immutable")

### TC-ROLE-008 — An invalid permission list is INVALID_PERMISSION
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin token; a custom role "Existing" holding `leads.view` at `own`.
- **Steps:**
  1. `POST /roles` and `PUT /roles/<Existing id>`, each with one of: an unknown key (`leads.fly`),
     `tickets.view_own_customer`, `settings.sso_manage`, `settings.scim_manage`, `roles.manage` at
     `own`, `leads.view` at scope `some`, `leads.view` named twice.
  2. The same requests with a valid list.
- **Expected:** Step 1: **400** `INVALID_PERMISSION` with `details.keys` listing every offending
  key, sorted; nothing is written and "Existing" keeps its name and permissions
  (`internal/service/role_service.go` `validateRolePermissions`). Step 2: **201** and **200**. The
  editor shows "These permissions cannot be saved: <keys>".
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPIRejectsInvalidPermissionsSQLite`; `internal/service/role_governance_test.go`
  `TestValidateRolePermissions`

## Permission catalogue and scopes

### TC-ROLE-009 — Read the permission catalogue
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin token.
- **Steps:**
  1. `GET /permissions/catalogue`.
- **Expected:** **200** with the groups in display order (`internal/permissions/catalogue.go`
  `Catalogue`), each with `selectable` false, empty `scopes` and its permissions as `children`.
  A permission that may be held at `own` lists `scopes` `["all", "own"]` (`leads.view`); an
  all-only one lists none (`roles.manage`). `tickets.view_own_customer` (system-only),
  `settings.sso_manage` and `settings.scim_manage` (admin-only) are `selectable` false
  (`internal/permissions/catalogue.go` `Node.CustomRoleMayHold`); every other permission is true.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` (subtest "catalogue"); `internal/permissions/catalogue_test.go`
  `TestCatalogueIsAWellFormedTree`, `TestCatalogueScopes`

### TC-ROLE-010 — Own and all scopes, and what a stored set keeps
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin token; a sales user owning a lead; a custom role held by a second user
  through one access token.
- **Steps:**
  1. Give the role `leads.view` at `all`; as the holder `GET /leads/<the sales user's lead>`.
  2. Change the role to `leads.view` at `own`; repeat the request with the same token.
  3. Store rows the catalogue does not allow (an unknown key, `own` on `customers.export`, a scope
     that is neither value, `tickets.view_own_customer`) and read the holder's permissions.
- **Expected:** Step 1: **200**. Step 2: **403** `FORBIDDEN`: scope own,
  `internal/models/role.go` `PermissionScopeOwn`, reaches only the records the account owns or
  is assigned, and scope all, `internal/models/role.go` `PermissionScopeAll`, every record; the
  change applies to the next request. Step 3: the resolved set leaves those rows out:
  `internal/permissions/catalogue.go` `NewSet` drops an unknown key and a scope the leaf does not
  allow, and `internal/permissions/system_roles.go` `ForCustomRole` drops system-only and
  admin-only keys.
- **Automation:** automated (backend) —
  `internal/integration/permission_next_request_integration_test.go`
  `TestPermissionChangeNextRequestSQLite` and `TestPermissionChangeNextRequestMySQL` (subtest "a
  narrowed scope applies to the next request too"); `internal/permissions/catalogue_test.go`
  `TestForUserResolution`

## Who may manage roles

### TC-ROLE-011 — Sales, support and customer are refused on every role route
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Tokens of a sales, a support and a customer user; a custom role "Guarded".
- **Steps:**
  1. As each, call `GET /permissions/catalogue`, `GET /roles`, `POST /roles`, `GET /roles/audit`,
     `GET /roles/<id>`, `PUT /roles/<id>`, `DELETE /roles/<id>` and `POST /roles/<id>/clone`.
- **Expected:** All 24 requests answer **403** `FORBIDDEN`; no role and no audit row is written.
  These routes were added after the system-role route matrix was frozen, so they are listed in
  `cmd/system_role_route_matrix_test.go` `routeMatrixAfterFreeze` and held to the router and to
  `internal/permissions/routes.go` `RouteKeys` rather than replayed from the golden.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAPISQLite` and `TestRoleAPIMySQL` (subtest "sales, support and customer are refused on
  all eight routes"); `cmd/system_role_route_matrix_test.go`
  `TestRouteMatrixCoversEveryProtectedRoute`; `internal/permissions/catalogue_test.go`
  `TestRouteTables`

### TC-ROLE-012 — Without roles.view the role screens are not offered
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** A sales and a support user created in-test with `APIClient.createUser`.
- **Steps:**
  1. Sign in as the sales user through the login form and open Settings in the navigation.
  2. Open `/settings/roles`, `/settings/roles/new` and `/settings/roles/<Sales id>` directly.
  3. Repeat as the support user.
- **Expected:** Step 1: Settings lists Profile but neither "Roles" (`roles.view`) nor "Role audit"
  (`roles.audit_view`). Step 2: each lands on `/unauthorized` (`ProtectedRoute` with
  `requiredPermission` `roles.view`, and `roles.manage` for `/settings/roles/new`) and no
  `GET /roles` request is made. Step 3: the same.
- **Automation:** planned — `gocrm-ui/e2e/tests/roles.spec.ts` (extended); Go:
  `internal/integration/role_api_integration_test.go` `TestRoleAPISQLite`; Vitest
  `gocrm-ui/src/routes/index.permissions.test.tsx`,
  `gocrm-ui/src/layouts/MainLayout.permissions.test.tsx`

### TC-ROLE-013 — A role manager cannot reach above its own permissions (ROLE_ESCALATION)
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A custom role "Manager" holding `roles.view`, `roles.manage`, `users.view`,
  `users.create`, `users.update`, `users.assign_role`, `users.activate`, `users.delete`,
  `leads.view` at `own` and `customers.view`, held by a user; a role "Above" holding
  `customers.view` and `deals.view`; a role "Within" holding `customers.view` and `leads.view` at
  `own`.
- **Steps:**
  1. As the manager, `POST /roles` with `customers.view` and `deals.view`; then with `leads.view`
     at `all`.
  2. `PUT /roles/<Within id>` adding `deals.view`; `PUT` and `DELETE` on "Above".
  3. `POST /roles/<Above id>/clone`.
  4. `PUT /users/<a sales user>` with `{"role": "custom", "role_id": <Above id>}`; as a user
     holding `users.update` and `users.assign_role`, `PUT /users/<itself>` with
     `{"role": "admin"}`.
  5. The passing twins: the same requests within the manager's permissions.
- **Expected:** Steps 1 to 4: **403** `ROLE_ESCALATION` ("The change reaches permissions above
  your own"), nothing written, no audit row. A key the manager lacks exceeds it, and so does
  scope all over the manager's own: `internal/service/role_governance.go` `permissionsExceed`.
  Update and delete are checked against the stored set too, so renaming "Above" with an empty
  list is refused; the delete of a held role above the manager is refused before the holders
  are counted and the body has no `user_count`. The Customer system role counts as the empty set
  (`internal/service/role_governance.go` `escalationSet`). Step 5: **201**, **200** or **204**.
- **Automation:** automated (backend) — `internal/integration/role_escalation_integration_test.go`
  `TestRoleEscalationRefused`; `internal/service/role_governance_test.go` `TestPermissionsExceed`,
  `TestCheckEscalation`, `TestEscalationSet`

### TC-ROLE-014 — The escalation rule reads the permissions committed when the write decides
- **Priority:** P1
- **Type:** concurrency
- **Preconditions:** A custom role "Manager" holding `users.activate` and `leads.view`, held by a
  user admitted for `POST /users/<target>/deactivate`; the target on a role holding `leads.view`.
- **Steps:**
  1. Between the request's admission and its decision, commit one change: none; the target
     promoted to sales; the target's role widened with `deals.view`; `leads.view` taken from the
     manager's role; `users.activate` taken from the manager's role.
- **Expected:** Without a change the request answers **200**. When the target was promoted, its
  role widened or the manager put below it, the request answers **403** `ROLE_ESCALATION`; when
  the manager lost the route's key, **403** `FORBIDDEN`. A refused request writes nothing: the
  check runs inside the transaction after the locks, on the rows as they are then.
- **Automation:** automated (backend) — `internal/integration/role_escalation_integration_test.go`
  `TestRoleEscalationConcurrentSQLite` and `TestRoleEscalationConcurrentMySQL`

### TC-ROLE-015 — The role editor shows a refused escalation to a custom role manager
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** The admin creates through the API a custom role "Role keeper" holding
  `roles.view`, `roles.manage` and `leads.view` at `own`, and a user on it with
  `APIClient.post('/users', ...)` (`role` custom, `role_id`).
- **Steps:**
  1. Sign in as that user through the login form and open `/settings/roles/new`.
  2. Name the role, tick "View deals" and save.
  3. In the list, press Clone on the Admin row and confirm with a new name.
- **Expected:** Step 2: `POST /roles` answers **403** `ROLE_ESCALATION`, the editor stays open and
  shows the message "The change reaches permissions above your own". Step 3: `POST
  /roles/<Admin id>/clone` answers **403** `ROLE_ESCALATION` and the dialog shows the same
  message. No role is written. The spec erases the user, then deletes "Role keeper".
- **Automation:** planned — `gocrm-ui/e2e/tests/roles.spec.ts` (extended); Go:
  `internal/integration/role_escalation_integration_test.go` `TestRoleEscalationRefused`

## Assigning roles to users

### TC-ROLE-016 — An inconsistent role pair is INVALID_ROLE_ASSIGNMENT
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin token; a custom role; a sales user.
- **Steps:**
  1. `POST /users` and `PUT /users/<id>` with `role` custom and no `role_id`; with `role` sales
     and a `role_id`; with `role` customer and a `role_id`.
  2. `PUT /users/<id>` with only a `role_id`; with `role` custom and the Sales role's id.
  3. `POST /users` and `PUT /users/<id>` with `role` custom and `role_id` 999999.
- **Expected:** Steps 1 and 2: **400** `INVALID_ROLE_ASSIGNMENT` ("role custom requires role_id,
  and role_id is accepted with role custom only"; `internal/service/user_governed_writes.go`
  `CheckRoleAssignment`); a system role is assigned by its key. Step 3: **404** `ROLE_NOT_FOUND`.
  Nothing is written and no audit row is added. A consistent pair answers **201** or **200**
  with one `user_role_changed` row; moving back to sales clears `role_id`. Without
  `users.assign_role` a create can only make a customer (other roles **403** `FORBIDDEN`) and an
  update ignores `role` and `role_id`.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestUserRoleAssignmentAPISQLite`; `internal/service/role_governance_test.go`
  `TestCheckRoleAssignment`

### TC-ROLE-017 — A customer login cannot receive a custom role
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin token; a custom role; a customer user and a sales user.
- **Steps:**
  1. `PUT /users/<customer id>` with `{"role": "custom", "role_id": <id>}`.
  2. The same body for the sales user.
- **Expected:** Step 1: **409** `CUSTOMER_LOGIN_ROLE` ("A customer login cannot receive a custom
  role"); role, `role_id` and the audit log are unchanged. Step 2: **200**, the user is on the
  custom role and one audit row is added. The user form side is TC-USER-042.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestCustomerLoginCannotReceiveCustomRole`

## The last active admin

### TC-ROLE-018 — The last active admin cannot be demoted, deactivated or erased (LAST_ADMIN)
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Exactly one active admin (a deactivated and an erased admin may exist); custom
  roles holding every key a custom role may hold, only `users.activate`, only `users.delete`.
- **Steps:**
  1. As the admin, `PUT /users/<itself>` with `{"role": "sales"}`, then with `{"is_active": false}`.
  2. As the admin, `POST /users/<itself>/deactivate` and `DELETE /users/<itself>`.
  3. As each custom role, deactivate or erase the admin.
  4. Add a second active admin and repeat a demotion, a deactivation and an erasure.
- **Expected:** Step 1: **409** `LAST_ADMIN` ("The last active administrator cannot be demoted,
  deactivated or erased"); a demotion to a custom role is refused the same way. Step 2: **400**
  `BAD_REQUEST`: an administrator cannot deactivate or erase itself. Step 3: **403**
  `ROLE_ESCALATION`. In steps 1 to 3 the admin row and the audit log are unchanged. Step 4:
  **200**, **200** and **204**; one active admin remains. A deactivated admin does not count and
  can be erased.
- **Automation:** automated (backend) — `internal/integration/role_last_admin_integration_test.go`
  `TestLastAdminInvariantSQLite`, `TestLastAdminInvariantMySQL`, `TestLastAdminAPISQLite`

### TC-ROLE-019 — Two competing changes never remove both admins
- **Priority:** P0
- **Type:** concurrency
- **Preconditions:** Exactly two active admins, x and y.
- **Steps:**
  1. Release together: x demotes y and y demotes x; x demotes y and y deactivates x; x demotes y
     and y erases x; x demotes itself and y deactivates itself.
- **Expected:** In every pair exactly one request commits; the other answers **403** `FORBIDDEN`
  (its actor was the one changed) or **409** `LAST_ADMIN` and writes nothing. One active admin
  remains and only the committed change is audited.
- **Automation:** automated (backend) — `internal/integration/role_last_admin_integration_test.go`
  `TestLastAdminConcurrentSQLite` and `TestLastAdminConcurrentMySQL`

## Role audit

### TC-ROLE-020 — Read the role audit log through the API
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin token; two role creates, an update, an assignment and a delete done.
- **Steps:**
  1. `GET /roles/audit`; with `limit=500`; with `limit=2&offset=2`.
  2. With `sort_by` `id`, `created_at` or `action`, and with `sort_by=role_name;DROP`.
  3. With `role_id`, with `target_user_id`, with both, with `role_id=999999`.
  4. As a custom role holding `roles.view` and `roles.manage` only; then with `roles.audit_view`.
- **Expected:** Step 1: **200**, newest first (id breaks ties), 20 per page by default, `limit`
  capped at 100; rows carry `actor_user_id`, `auth_method` (`bearer`), `action`, `role_id`,
  `role_name`, `target_user_id` and the before/after state. Step 2: allowlisted columns order the
  rows, then by id; any other value is ignored. Step 3: the filters narrow alone and together.
  Step 4: **403** `FORBIDDEN`, then **200**. Every row commits in the transaction of its change,
  and a failed audit insert rolls the change back; account rows (activation, deactivation,
  erasure, login-email change) store ids and `is_active` only.
- **Automation:** automated (backend) — `internal/integration/role_api_integration_test.go`
  `TestRoleAuditAPISQLite`; `internal/integration/role_audit_integration_test.go`
  `TestRoleAuditAtomicSQLite`, `TestRoleAuditAtomicMySQL`, `TestAccountAuditSQLite`,
  `TestAccountAuditMySQL`, `TestRoleChangeLogsOmitPersonalData`

### TC-ROLE-021 — The Role audit screen lists a user's account changes
- **Priority:** P1
- **Type:** functional
- **Preconditions:** A sales user created in-test through the API by the admin; its email
  changed, then deactivated, activated and erased.
- **Steps:**
  1. Sign in as the admin, open Settings and choose "Role audit" in the navigation.
- **Expected:** `/settings/role-audit` opens (`gocrm-ui/src/pages/settings/RoleAudit.tsx`) and
  `GET /roles/audit` answers **200**. The rows whose Target user is that user's id show, newest
  first, "Account erased", "Account activated", "Account deactivated" and "Email changed", each
  with method "Session" (the `bearer` label). The table never shows an email or a person's name.
- **Automation:** automated — `gocrm-ui/e2e/tests/role-audit.spec.ts` "lists the email change,
  deactivation, activation and erasure of a user"; Vitest
  `gocrm-ui/src/pages/settings/RoleAudit.test.tsx`

### TC-ROLE-022 — A sales user is not offered the Role audit screen
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** A sales user created in-test with `APIClient.createUser`.
- **Steps:**
  1. Sign in as the sales user through the login form and open Settings in the navigation.
- **Expected:** Settings shows Profile and no "Role audit" entry (`roles.audit_view`).
- **Automation:** automated — `gocrm-ui/e2e/tests/role-audit.spec.ts` "a sales user is not shown
  the entry"

### TC-ROLE-023 — The Role audit screen shows role writes with the role name
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin token; a role named in-test with a unique suffix.
- **Steps:**
  1. Through the API create the role, rename it, then delete it.
  2. Open `/settings/role-audit`.
- **Expected:** The three newest rows read "Role deleted", "Role updated" and "Role created" with
  method "Session", the admin's id as acting user, "—" as target user and the role's name at the
  time of each change. A row with an unknown action or method shows the stored value.
- **Automation:** planned — `gocrm-ui/e2e/tests/role-audit.spec.ts` (extended); Go:
  `internal/integration/role_api_integration_test.go` `TestRoleAuditAPISQLite`
