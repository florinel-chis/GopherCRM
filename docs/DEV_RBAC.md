# Roles and permissions

Role assignment, the storage of custom roles, the admin-only permissions, the role API with the
governed user writes, and the guard that keeps at least one active admin. The permission each
route requires is listed in [AUTHORIZATION.md](AUTHORIZATION.md). Part of the developer guide; the index is in [DEVELOPMENT.md](DEVELOPMENT.md).

## Last-admin guard

Every governed account write (an account create, a role or profile change, an activation or
deactivation, an erasure, the SCIM user and group writes, the OpenID Connect sign-in's account
write) must leave at least one active admin.
`internal/service/last_admin_guard.go` `lockAdminSet` locks the active admin set as the first
statement of the governed lock order and records it as the state before the write;
`internal/service/last_admin_guard.go` `adminSet.checkAfter` then judges the state after the
change and returns `ErrLastAdmin` when the set held an active admin before and none is left. It
counts each changed account by what it is after the write, so an account that gains the admin
role counts as well as one that loses it.

## Roles

**Role assignment** — `POST /auth/register` is public and always creates a `customer`; it ignores
any client-supplied role. Elevated roles are only assignable via `POST /users` and `PUT /users/{id}`
by a caller holding `users.assign_role` (the admin system role does), or the `create-admin` CLI. Never add a client-controlled role to a public endpoint. E2E admin accounts
are seeded by `gocrm-ui/e2e/global-setup.ts`, which shells out to `cmd/create-admin`.

**RBAC persistence (#259 S2, #344)** — the storage of custom roles. The permission catalogue and
its enforcement read it since #347; the role API and the governed user writes of #348 (next
paragraph) are the callers of its repository methods.

- **Models and order.** `models.Role`, `RolePermission` and `RoleAuditEntry` (`roles`,
  `role_permissions`, `role_audit_log`, see [datamodel.md](datamodel.md#role-model)) are registered
  in `autoMigrateModels` ahead of `User`, because `users.role_id` references `roles`
  (`fk_users_custom_role`, ON DELETE RESTRICT; plus `idx_users_role` and `idx_users_role_id`). On
  SQLite the foreign key rebuilds `users` under `database.Migrate`; the indexes survive.
- **The JSON.** `User.RoleID` is `json:"role_id,omitempty"`, so a NULL value adds no key and a
  system-role user's answer is unchanged; `CustomRole` is `json:"-"`. Since #348 `role_id` is a
  documented property and `models.RoleCustom` (`"custom"`) is a typed `UserRole` constant, the
  fifth value of the swag enum and of the closed set `scripts/ci/check-api-docs.py` collects by
  type. A `custom` user holds only the permissions of its role row (#347); `POST /users` and
  `PUT /users/{id}` create one (`role = custom` with `role_id`).
- **The seed.** `seedSystemRoles` runs in `MigrateDatabase` after `normalizeCompanyDomains` on every
  start and driver. It reads `SELECT system_key FROM roles WHERE system_key IS NOT NULL` (plain,
  non-locking) and inserts only the missing rows of `models.SystemRoleSeeds` (#259's names and
  descriptions, the same literals as the SQL up) with `ON CONFLICT DO NOTHING`, so a replica booting
  at the same time is not an error (an insert that fails, for example as the victim of a lock
  conflict with that replica, is accepted when a re-read then finds the row). An up-to-date database takes that one SELECT and no write; an
  existing system row is never updated, so its name and description are not rewritten. It logs
  nothing. When a custom role already holds the `name_key` of a missing system row, the start fails
  before writing anything with `seed system roles: roles.name_key "<key>" is held by a custom role;
  rename that role and restart`. Recovery: rename that custom role, for example `UPDATE roles SET
  name = 'Sales team', name_key = 'sales team' WHERE id = <id>`, and restart.
  `models.MigrateDatabaseWith(db)` runs the same steps on an explicit handle (tests of two pools).
- **`RoleRepository`** (`internal/repository/role_repository.go`). `Create` and `Update` trim the
  name, refuse a blank one, one over 100 characters or a description over 500 with
  `apperrors.ErrValidation` before any statement, and set `name_key = models.RoleNameKey(name)`
  (trimmed, lower-cased). `Create` refuses a non-nil `SystemKey` (`ErrValidation`), so
  `idx_roles_name_key` is the only unique index a repository write can hit and a duplicate key is
  `apperrors.ErrRoleNameTaken`. `Update` writes `name`, `name_key`, `description` and `updated_at`
  only, never `system_key`; refusing writes to system rows is the role service's job (owner
  decision 2, `SYSTEM_ROLE_IMMUTABLE`). `ReplacePermissions(roleID, permissions)` locks the role (`FOR UPDATE` on
  MySQL/MariaDB), deletes its rows and inserts the new set in one transaction (a savepoint under
  `WithTx`): a failure after the delete keeps the previous set, an empty slice leaves none, a
  missing role is `gorm.ErrRecordNotFound`. `GetByID` returns the permissions ordered by
  `permission_key`. `LockByIDs(ids)` (#348) locks role rows by primary key in ascending id and reads
  their permissions with a locking read too. `List(offset, limit, sortBy, sortOrder)` returns a page (each role with its permissions) and the total from the
  same predicate, ordered by `utils.AllowedSortColumns["roles"]` (`id`, `name`, `created_at`,
  `updated_at`) and then `roles.id` in the same direction (`orderPage`), or by id ascending; another
  column is an error before any query. `Delete(id)` locks the role, deletes its permissions and then
  the role in one transaction; the foreign-key refusal of a role still referenced by a user, live or
  erased (MySQL 1451, SQLite `FOREIGN KEY constraint failed`, classified by `isForeignKeyRefusal` in
  `foreign_key.go`), is `apperrors.ErrRoleInUse` and rolls the permission delete back.
- **User repository.** `UpdateFields` accepts `role_id` (a value or nil). `GetByID` of a user with
  `role_id` NULL is still exactly one SELECT; a custom-role user also gets `CustomRole` with its
  permissions. The optional interface `repository.UserRoleMaintenance` (the `TicketLockingReader`
  pattern, so the hand-written mocks are unchanged) adds `LockActiveAdmins`, which discovers ids with
  a plain `SELECT id … WHERE role = 'admin' AND is_active AND deleted_at IS NULL ORDER BY id`, then
  locks those rows by primary key only (`WHERE id IN (…) ORDER BY id`, `FOR UPDATE` on MySQL/MariaDB)
  and returns the locked rows that still match, in id order: no locking statement filters on role,
  so no lock is taken through `idx_users_role`. `ClearRoleIDOfErased(roleID)` sets `role_id` NULL on
  the soft-deleted users of that role and returns the count, after which the role can be deleted.
  #348 adds `LockLiveByIDs` (the actor and the target, by primary key in id order),
  `LockLiveRoleHolders` (the live users of a role, a locking read by `role_id`) and
  `CountLiveRoleHolders` (the `user_count` of the role API).
- **Migration.** `20261119120000_add_roles_and_permissions` (#259 planned `20261101120000`, which
  `add_task_deal_link` already holds). Its down half sets `role = 'support'` and `is_active = 0` on
  every live custom-role user before it drops anything (owner decision 8): irreversible for those
  users, so take a backup before rolling back on MySQL/MariaDB. SQLite has no down path: roll back by
  restoring the pre-upgrade backup. An older binary started on the upgraded file would keep the role
  tables and `fk_users_custom_role` (its AutoMigrate drops nothing), and every `custom` user would
  match no role guard of that binary and get 403 on every guarded route.
- **Erasure.** `roles`, `role_permissions` and `role_audit_log` are not touched by the erasure:
  role names carry no personal text by rule, and audit rows hold ids, role names, permission keys
  and scopes only: the writers of #348 store a role's name, description and permissions, or a
  user's `role` and `role_id`, and nothing else. An erased user keeps its `role_id`, which keeps
  the foreign key in place until the role delete runs `ClearRoleIDOfErased`.
- **Collation.** On MySQL/MariaDB `roles.name_key` keeps `utf8mb4_unicode_ci`, so names differing
  only by accents or trailing spaces also collide there; SQLite compares binary. No collation guard
  is added (pending the owner's answer).

Regression roots: `TestRoleSchemaSQLite`, `TestRoleSQLiteUpgradeFromV120`, `TestUserRoleIDJSON`,
`TestRoleNameKey`, `TestRoleFieldsAreInTheAPIReference` (`internal/models`);
`TestRoleRepository{SQLite,MySQL}`, `TestForeignKeyRefusalClassifier`,
`TestSystemRoleSeed{SQLite,MySQL}` (statements captured by GORM callbacks; the MySQL root also
starts two pools at once), `TestUserRepositoryRoleColumns{SQLite,MySQL}` (`internal/repository`);
`TestRoleMigrationFiles` (it held the newest-migration assertion until #345 moved it to
`TestWebhookMigrationFiles`),
`TestMigrationRolesSchema` and `TestMigrationRolesDownDemotesCustomUsers` (`cmd/migrate`). The
SQLite roots use the production connector over a file under `t.TempDir()`; the MySQL roots a
disposable `TEST_MYSQL_DSN` schema, skipped without the variable (the acceptance run sets it and
fails on a skip). Operator follow-up (owner decision 9): register the migration and seed roots in
`.github/workflows/migrations.yml` and the repository roots in `scripts/ci/check-go.py`.

**Admin-only permissions (#524)** — `settings.sso_manage` and `settings.scim_manage` are
`AdminOnly` leaves of the catalogue: the admin matrix holds them (`adminGrants` skips only
`SystemOnly` leaves), and `Node.CustomRoleMayHold` keeps them out of every custom role, in
`ForCustomRole`, `validateRolePermissions` (400 `INVALID_PERMISSION`), the catalogue's
`selectable` and therefore a clone of Admin. `models.AdminOnlyPermissionKeys` lists the same keys
for the startup data step `stripAdminOnlyRolePermissions` (`internal/models`, after
`seedSystemRoles`): one plain SELECT finds the custom roles holding either key; each is then
changed in its own transaction on the migration handle, which locks the `roles` row, reads its
stored rows, deletes the two keys and, only when the DELETE removed a row, writes one
`role_updated` audit row with `auth_method` `migration` and `actor_user_id` NULL, whose states hold
the stored rows sorted by key. The SQL migration `20261129120001_admin_only_sso_scim_permissions`
does the same on MySQL/MariaDB (`UTC_TIMESTAMP(3)`, keys ordered `utf8mb4_bin`); its down half
restores nothing. Since the admin matrix holds keys no custom role can hold, an admin account is
always above a custom-role caller under the escalation rule. Regression roots:
`TestAdminOnlyPermissionStrip{SQLite,MySQL}` (`internal/repository`; the MySQL root also starts
two pools at once), `TestStripAdminOnlyRolePermissionsSkipsStrippedRole` (`internal/models`),
`TestAdminOnlyPermissionsMigration{Files,}` (`cmd/migrate`), `TestAdminOnlySettingsPermissionsSQLite`
and the admin-only cases of `TestRoleAPIRejectsInvalidPermissionsSQLite` (`internal/integration`).

**Role API and governed user writes (#259 S4, #348)** — `internal/service/role_service.go` serves
`GET /permissions/catalogue`, `/roles` (list, create, get, update, delete), `POST /roles/{id}/clone`
and `GET /roles/audit`; `internal/service/user_governed_writes.go` holds the actor-taking user
writes (`CreateAs`, `UpdateAs`, `SetActiveAs`, `DeleteAs`) that `POST /users`, `PUT /users/{id}`,
activate, deactivate and `DELETE /users/{id}` call. What both share is in
`internal/service/role_governance.go`.

- **One transaction per write.** Each write is a `WithTransactionAndRetry` root through the `WithTx`
  repositories that locks the active admin set (user writes only), the actor and the target in
  ascending id, then the custom roles in ascending id, reads all of them again under the locks and
  only then decides; the audit row is inserted in the same transaction. Lock order and retry are in
  [CONCURRENCY.md](CONCURRENCY.md) (flow "RBAC user-role and role writes").
- **The escalation rule** is one function, `checkEscalation`: a permission set exceeds the actor's
  when one of its grants is not covered (`own` is below `all`, a missing key below `own`); a system
  admin is exempt. A role write checks the requested set (create, update, clone) and, on update and
  delete, the role's stored set, so a manager can neither grant above its own permissions nor edit
  or delete a role that is above them. A user write checks the target as it is now and the role it
  is to receive, on the caller's own account too. The `customer` system role stands for the empty
  set there (`escalationSet`): it holds the one system-only key, which no custom role can hold, so
  it never counts as above a manager. A custom role holding `users.create` alone can create a
  customer login, and one holding `users.update`, `users.activate` or `users.delete` can manage one.
  A customer login with a linked customer record (a `customers` row naming it in `user_id`, erased
  rows included) asks for one key more (#378): erasing it needs `customers.delete`, changing its
  email needs `customers.update`. `governedTx.keysFor` adds the key from the stored role and email,
  after a plain read of `customers` inside the transaction.
- **Order of the refusals.** User writes: `NOT_FOUND`, `ROLE_NOT_FOUND`, `FORBIDDEN` (the actor lost
  the route's key while the request waited, or lacks the customer key a write to a customer login
  with a linked customer record needs), `ROLE_ESCALATION`, `CUSTOMER_LOGIN_ROLE`, `LAST_ADMIN`.
  `PUT` and `DELETE /roles/{id}`: `ROLE_NOT_FOUND`, `FORBIDDEN`, `SYSTEM_ROLE_IMMUTABLE`,
  `ROLE_ESCALATION` for the stored set, then on `PUT` `INVALID_PERMISSION` and `ROLE_ESCALATION` for
  the requested set, on `DELETE` `ROLE_IN_USE` (after the escalation check, so a manager below the
  role does not learn its `user_count`). A refusal writes nothing.
- **Which user writes are governed.** Every `PUT /users/{id}` on another account (profile fields
  included: changing a superior's email would redirect its password reset) and every one that
  writes `role`, `role_id` or `is_active`, the caller's own account included. A profile-only update
  of the caller's own account and `PUT /users/me` stay the plain `UserService.Update`; both own-account
  paths run `UserService.CheckOwnEmailChange` first (#394: an own email change needs
  `current_password`, 400 `CURRENT_PASSWORD_REQUIRED` or `INVALID_CURRENT_PASSWORD`). `Register`,
  `Update` and `Delete` keep their behaviour for the non-HTTP writers (`cmd/create-admin`, public
  registration); a user service built without `service.WithUserRoleGovernance` runs the actor-taking
  methods as those plain writes, which is what the older suites rely on.
- **Deactivation ends every credential** (#526). `service.WithUserCredentialRevocation(apiKeyRepo,
  refreshTokenRepo)` (`cmd/main.go` passes both) lets a governed write with `is_active` false
  (`SetActiveAs`, `UpdateAs`; also on an account already inactive) call `revokeAllCredentials`
  after its `users` write and before the audit row. A governed service built without it refuses
  such a write with an error (500 at the handler) after the refusals above and before any write.
  The ungoverned fallback (a service without `WithUserRoleGovernance`) writes `is_active` only and
  revokes nothing; production always wires governance.
- **Audit.** `role_audit_log` gets one row per role create, clone (`role_created`), update and
  delete, and per change of a user's `role` or `role_id` (`user_role_changed`, also for the role a
  new account is created with). Since #552 `governedWrite` also records, after the `users` write
  and `revokeAllCredentials` and in this order, `user_email_changed` when `*change.email !=
  target.Email`, `user_activated` or `user_deactivated` when `is_active` differs from the locked
  row (`userActiveAuditState`, `{"is_active": …}`), then `user_role_changed`; an erasure records
  `user_erased` after `tx.users.Delete` returned, in the same root, so a graph-change restart or
  a deadlock replay commits one row. Account rows have `role_id` NULL, `role_name` '' and NULL
  states except the activation rows; no address is stored. `auth_method` is the request's `service.AuthMethodContextKey` value
  (`bearer` or `api_key`), `oidc` (`service.AuthMethodOIDC`) for the role an OpenID Connect
  sign-in applied to its own account (#435), or `scim` (`service.AuthMethodSCIM`) for a SCIM user
  or group write (#470, #471, #504, #506), whose `actor_user_id` is NULL. The OIDC sign-in, the
  caller's own profile-only email change and the customer and lead erasure cascades write no
  account row (see [AUTHORIZATION.md](AUTHORIZATION.md#audit-trail)). The log lines of these paths carry ids and action
  names only. Regression roots: `TestAccountAudit{SQLite,MySQL}` (`internal/integration`).

Regression roots (`internal/integration`, on the real Auth, routes, handlers and repositories):
`TestRoleAPI{SQLite,MySQL}`, `TestRoleAPIRejectsInvalidPermissionsSQLite`,
`TestRoleAPIDeleteAssignedRoleSQLite`, `TestLastAdminInvariant{SQLite,MySQL}`,
`TestLastAdminConcurrent{SQLite,MySQL}`, `TestLastAdminAPISQLite`, `TestRoleEscalationRefused`,
`TestRoleEscalationConcurrent{SQLite,MySQL}`, `TestCustomerLoginCannotReceiveCustomRole`,
`TestRoleAuditAtomic{SQLite,MySQL}`, `TestRoleChangeLogsOmitPersonalData`, `TestRoleAuditAPISQLite`,
`TestUserRoleAssignmentAPISQLite`, `TestLinkedCustomerLoginWrites{SQLite,MySQL}`;
`TestGovernedWritesToLinkedCustomerLogin` (`internal/service`); and
`TestUserWritesAreRoleGovernedOnTheRealRouter` (`cmd`), which shows that `setupRouter` wires the
governance option.
