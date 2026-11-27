# Reads and filters

> **For:** Developers changing ordered and paginated reads, list filters and lookup indexes. Part of the [documentation index](../README.md).

## Ordered and paginated reads

**Unpaginated reads must not mix a non-id `ORDER BY` with `FindInBatches`.** GORM's
`FindInBatches` continues each batch with `id > last id of the previous batch`, a cursor that is
only valid when the rows are ordered by id; under any other ordering it skips and repeats rows
across batches. The customer CSV export did exactly that and lost rows past the first 500 (#105).
`CustomerRepository.ListAllForExport` now reads every matching row with one ordered statement whose
order is total: the requested column, then `id`, both in the requested direction, or `id` ascending
without a sort column. That makes duplicate values, NULLs in the legacy nullable `company` column
and MySQL collation ties deterministic, makes `desc` the exact reverse of `asc`, and makes the read
one consistent snapshot on both engines. The result is still fetched in full before the handler
writes a byte. Regression roots: `TestCustomerExportTraversal{SQLite,MySQL}`
(`internal/repository/customer_export_traversal_test.go`) and `TestCustomerExportCSV{SQLite,MySQL}`
(`tests/customer_export_csv_test.go`); the MySQL roots create a disposable schema on
`TEST_MYSQL_DSN`, skip without it and fail when it is unreachable. If a whole-book read ever needs
to be chunked again, continue on a composite `(sort column, id)` keyset that handles NULL
explicitly, never on the primary key alone.
`LeadRepository.ListAllForExport` (`GET /leads/export`, #234) follows the same rule: one statement
over the live leads matching a `LeadExportFilter` (owner, search, exact classification, each
applied only when given), ordered by the validated column and then `leads.id` in the same
direction, or by `id` ascending; converted leads are included and erased ones are not.
`LeadService.ExportForActor` applies the list's scope: an admin gets the search with the sort,
else the classification in id order, else the sort, else id order; a sales user its own leads in
id order with every parameter ignored; any other role `apperrors.ErrForbidden` without a read.
Regression roots: `TestLeadExportTraversal{SQLite,MySQL}`
(`internal/repository/lead_export_traversal_test.go`) and `TestLeadExportCSV{SQLite,MySQL}` and
`TestLeadExportRoles{SQLite,MySQL}` (`tests/lead_export_csv_test.go`), under the same MySQL rules.

**Paginated reads walk a total order (#110).** OFFSET/LIMIT pages are separate statements, so
an `ORDER BY` that leaves rows tied lets the engine return the tied rows in a different order on
each page; MySQL's filesort with a LIMIT does exactly that, and consecutive pages repeated some
rows and never showed others. Every list and search over leads, customers, tasks (including the
label join), tickets, users and companies, and the company customer/lead sub-lists, therefore
ends with the table's id: `orderPage` (`internal/repository/page_order.go`) validates the column
through `utils.ValidateSort`, orders by it and then by id in the same direction, or by id
ascending when no sort is requested; the company list keeps its newest-first default with id
descending after `created_at`. Columns are emitted through `clause.OrderByColumn` qualified with
the table name, so a joined query cannot resolve them against the other table. New paginated
reads should use the same helpers. Regression roots: `TestPaginationDeterminism{SQLite,MySQL}`
(`internal/repository/pagination_determinism_test.go`), where every row shares `created_at` and
every sort column is tied, both directions, with filters, checking every page against the
expected sequence and the count. The Database migrations workflow runs both roots, the MySQL one
on MySQL 8 and MariaDB 10.11, and its checker rejects a skip.

## List filters

**Ticket list filters (#218).** `GET /tickets` takes optional `status` and `priority` query
parameters, each one exact, case-sensitive value of `models.TicketStatus` or
`models.TicketPriority`. The handler validates them with `IsValid` before any query, after the
customer 403: MySQL's default collation is case-insensitive and would match `Open` to `open`. An
unknown value is 400 `VALIDATION_ERROR` (`utils.RespondValidationError`, `error.details` keyed by
the parameter name, the value not echoed) and no service is called. `c.Query` takes the first
value of a repeated parameter, and an empty value counts as absent. Only when a filter is present
does the handler call `TicketService.ListFiltered(filter, offset, limit, sortBy, sortOrder)` with
a `repository.TicketListFilter{AssigneeID, Search, Status, Priority}`, forcing `AssigneeID` to the
caller's own `user_id` for support; without one the existing `List`, `ListSorted`, `Search` and
`ListAssigned` branches run with unchanged arguments. `TicketRepository.ListByFilter` (preloads,
`orderPage`, OFFSET/LIMIT) and `CountByFilter` share one predicate builder,
`whereTicketListFilter`, which ANDs the optional assignee (a non-nil 0 matches no ticket, as in
`TicketAssigneeScope`), status and priority with the parenthesised search OR, so the rows and the
total come from the same filter before offset and limit. The service logs ids, offsets, counts
and presence flags only, never the search term or the raw sort. No schema change or index.
Regression roots: `TestTicketEnumIsValid` (`internal/models`); `TestTicketListFilterHandler`
(`internal/handler`); `TestTicketServiceListFiltered` (`internal/service`);
`TestTicketListFilterRepository{SQLite,MySQL}` (`internal/repository`);
`TestTicketListFilters{SQLite,MySQL}` (`internal/integration`). The MySQL roots use a disposable
`TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable.

**Lead list status filter (#426).** `GET /leads` takes an optional `status` query parameter, one
exact, case-sensitive value of `models.LeadStatus` (`new`, `contacted`, `qualified`, `unqualified`,
`converted`). The `external_id` branch still runs first and overrides it (an invalid status next to
a non-empty `external_id` is not validated). The handler then reads `status` with `c.Query` (first
value of a repeated parameter; empty counts as absent) and validates it with `LeadStatus.IsValid`
before any service call: an unknown value is 400 `VALIDATION_ERROR` with `error.details.status`, the
value not echoed or logged. `IsValid` is the one list: bulk status's `isValidLeadStatus` delegates
to it. Since #618 every request past `external_id` takes ONE path,
`LeadService.ListFiltered(repository.LeadListFilter{OwnerID, Search, Classification, Status},
offset, limit, sortBy, sortOrder)`, and the parameters a caller may use are always ANDed: a caller
without `leads.view` at scope all (sales) gets `OwnerID` forced to its own `user_id`, the status
when given and an empty sort (its search, classification and sort stay ignored); an admin gets
search, classification, status and the sort together. The owner is preloaded on every path.
`LeadRepository.ListByFilter` (`orderPage`, then OFFSET/LIMIT; id ascending without a sort) and
`CountByFilter` share one predicate builder, `whereLeadListFilter`, which ANDs `leads.owner_id`,
`leads.classification` and `leads.status` with the parenthesised search OR over the six searched
columns, so the rows and the total come from the same filter before offset and limit; erased
(soft-deleted) leads stay excluded. The search is a literal substring: `containsPattern`
(`internal/repository/like_pattern.go`) escapes `!`, `%` and `_` with `!`, and every LIKE is
`col LIKE ? ESCAPE '!'` (built by `likeAnyClause`), so `%`, `_`, `!` and a backslash match only
themselves on SQLite, MySQL and MariaDB. `!` rather than a backslash because `'\\'` is one
character on MySQL but two on SQLite, and MySQL treats a backslash as its default LIKE escape.
`LeadRepository.ListAllForExport` builds its WHERE from the same `whereLeadListFilter`, so
`GET /leads/export` matches exactly the rows the admin list would (search, classification and
the sort combined; sales its own leads only). The service logs offset, limit, the owner id and
the presence flags `status_filter`, `search_filter` and `classification_filter` only, never the
search term or the raw sort. `GET /leads/export` keeps ignoring `status`. No schema change.
Regression roots: `TestLeadStatusIsValid` (`internal/models`); `TestLeadListStatusFilterHandler`
(`internal/handler`); `TestLeadServiceListFiltered` (`internal/service`);
`TestLeadListFilterRepository{SQLite,MySQL}` (`internal/repository`). The MySQL root uses a
disposable `TEST_MYSQL_DSN` schema, skips without it and fails when it is unreachable.

**Task list filters (#427).** `GET /tasks` takes optional `status` and `priority` query
parameters, each one exact, case-sensitive value of `models.TaskStatus` (`pending`, `in_progress`,
`completed`, `cancelled`) or `models.TaskPriority` (`low`, `medium`, `high`; `urgent` is a ticket
priority only). The handler reads them with `c.Query` (first value of a repeated parameter; empty
counts as absent, which is what the list page sends for "All") and validates them with
`TaskStatus.IsValid` / `TaskPriority.IsValid` before any service call: an unknown value is 400
`VALIDATION_ERROR` with `error.details` keyed by the offending parameter name(s), the value not
echoed and the warning log carrying only the count. Since #618 every request takes ONE path,
`TaskService.ListFiltered(repository.TaskListFilter{AssigneeID, LabelID, Search, Status,
Priority}, offset, limit, sortBy, sortOrder)`, and the parameters a caller may use are always
ANDed: a caller holding `tasks.view` at scope all (admin) gets `AssigneeID` nil and `label_id`,
`search`, the filters and the normalised sort combined; any other caller gets `AssigneeID` forced
to its own `user_id`, no search, and the sort only together with `label_id` (id ascending
otherwise). `GET /tasks/my` stays on `TaskService.GetByAssignee`, which reads through the same
`ListByFilter`/`CountByFilter` with `AssigneeID` and the preloads `Labels`, `Lead` and `Deal` only
(its `assigned_to` stays zero-valued). `TaskRepository.ListByFilter` (the `taskListPreloads` on
`GET /tasks`, so `assigned_to` is filled on every path; `orderPage`, then OFFSET/LIMIT) and
`CountByFilter` share one predicate builder, `whereTaskListFilter`, which joins `task_labels` only
for a label filter and ANDs `tasks.assigned_to_id`, `tasks.status` and `tasks.priority` with the
parenthesised `(tasks.title LIKE ? ESCAPE '!' OR tasks.description LIKE ? ESCAPE '!')`, every
column qualified and every value bound; the search term is escaped by `containsPattern` as for
leads, so it matches literally. Soft-deleted tasks stay excluded. The service logs offset, limit, the assignee and label ids, the
total and the presence flags `status_filter`, `priority_filter` and `search_filter` only. No
schema change. Regression roots: `TestTaskEnumIsValid` (`internal/models`);
`TestTaskListFilterHandler` (`internal/handler`); `TestTaskServiceListFiltered`
(`internal/service`); `TestTaskListFilterRepository{SQLite,MySQL}` (`internal/repository`). The
MySQL root uses a disposable `TEST_MYSQL_DSN` schema, skips without it and fails when it is
unreachable.

**User list role filter (#428).** `GET /users` takes an optional `role` query parameter, one exact,
case-sensitive value of `models.UserRole` (`admin`, `sales`, `support`, `customer`, `custom`). The
handler reads it with `c.Query` (first value of a repeated parameter; empty counts as absent, a
blank is not trimmed) and validates it with `UserRole.IsValid` before pagination or any service
call: an unknown value is 400 `VALIDATION_ERROR` with `error.details.role`, the value not echoed.
With a role the handler calls `UserService.ListFiltered(repository.UserListFilter{Search, Role},
offset, limit, sortBy, sortOrder)`; without one the existing `Search`, `ListSorted` and `List`
branches run unchanged. `UserRepository.ListByFilter` (`orderPage`, then OFFSET/LIMIT; id
ascending without a sort) and `CountByFilter` share one predicate builder,
`whereUserListFilter`, which ANDs `users.role = ?` with the parenthesised search OR, so the rows
and the total come from the same filter before offset and limit; soft-deleted users stay excluded.
The service logs nothing about the filter. No schema change (`idx_users_role` already exists).
Regression roots: `TestUserRoleIsValid` (`internal/models`); `TestUserListRoleFilterHandler`
(`internal/handler`); `TestUserServiceListFiltered` (`internal/service`);
`TestUserListRoleFilterRepository{SQLite,MySQL}` (`internal/repository`);
`TestUserListRoleFilters{SQLite,MySQL}` (`internal/integration`). The MySQL roots use a disposable
`TEST_MYSQL_DSN` schema, skip without it and fail when it is unreachable.

## Lookup indexes

**Lookup indexes are measured (#109).** `leads.email`, `leads.classification`,
`leads.external_id` and `companies.domain` carry non-unique indexes declared in the model tags and
in `migrations/20261029140000_add_lead_company_lookup_indexes.up.sql`; the plans before and after are in
[../reference/data-model.md](../reference/data-model.md#lookup-indexes-and-query-plans). A predicate that wraps an indexed
column in a function (`LOWER(domain)`) hides it from the index on SQLite and on MySQL/MariaDB;
store the value normalised and compare it plainly instead. `TestLookupIndexesServeLookupsSQLite`
captures the statements the repository really runs and asserts their SQLite plans use the
indexes; `TestLookupIndexesMigration` (fresh up/down, populated upgrade, AutoMigrate first,
rollback and reapply, index parity) is one of the roots the Database migrations workflow requires.
Plain equality relies on every stored domain being lower case: the service normalises on write,
and `models.MigrateDatabase` lower-cases any other row on every start on every driver, which is how
SQLite installs, which never run the SQL migration, get it. It discovers the rows with a plain
non-locking `SELECT id` (binary comparison on MySQL/MariaDB) and updates only those ids, by primary
key in chunks of 500, so a normalised database takes no write and no row lock on startup; a single
predicate `UPDATE` would lock every scanned record under InnoDB REPEATABLE READ on every boot. Roots:
`TestCompanyDomainNormalization{SQLite,MySQL}` (`internal/repository/company_domain_normalization_test.go`).
