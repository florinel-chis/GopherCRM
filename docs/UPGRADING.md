# Upgrading and schema migrations

> **For:** Operators upgrading a GopherCRM installation to a newer release. Part of the [documentation index](README.md).

This page is the one place for the upgrade order and the schema migrations. The deployment
guides keep their own commands and link here: [SETUP.md](SETUP.md) (from source),
[DOCKER.md](DOCKER.md) (Compose) and [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) (systemd
server from a published release). What each release changes is in `CHANGELOG.md`.

## Upgrade order

1. **Back up.** Take a backup of the database first, as [BACKUP_RESTORE.md](BACKUP_RESTORE.md)
   describes for your deployment. On SQLite the schema advances only through the auto-migration
   that runs at startup, which is not reversible, so the backup is the only way back.
2. **Replace the binary or the image.** On a systemd server the Deploy workflow does it through
   `receive-release` ([SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#bootstrap-and-first-deploy));
   with Compose, rebuild the images ([DOCKER.md](DOCKER.md#rebuilding-after-code-changes)); from
   source, build or run the new checkout ([SETUP.md](SETUP.md#start)). With several backend
   replicas, stop every replica before starting the new version: mixed versions are not supported
   ([FORKING.md §5.4](FORKING.md#54-worker-topology)).
3. **Start.** The backend runs `models.MigrateDatabase` (GORM auto-migration) at every start
   (`main` (`cmd/main.go`)), so the first start creates the tables and later starts add new
   columns and tables. On SQLite this is the whole schema step: the SQL files in `migrations/`
   are written for MySQL and are never applied there.
4. **On MySQL or MariaDB, apply `migrations/`.** The SQL half is applied with golang-migrate
   through `bin/migrate -command up`, after the new version has started. The release tarball ships
   `bin/migrate` and the `migrations/` directory; on a systemd server the exact command, run as
   the service user with the service environment, is in
   [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#schema-migrations). The backend image carries
   no `migrate` tool and no `migrations/` directory: it ships only `gophercrm` and
   `create-admin` (the `COPY --from=build` line of `Dockerfile`).
5. **Check `/health` from outside.** `GET /health` answers with the `revision` the build was made
   from; check it as visitors reach it, through the proxy
   ([SERVER_PROVISIONING.md](SERVER_PROVISIONING.md#verify-the-deploy)).

Version numbering of SQL migrations in a fork, the rule that an upstream migration older
than your newest fork migration is skipped, and the populated-database upgrade rehearsal are in
[FORKING.md §7.4](FORKING.md#74-migration-versions).

## A failed SQL migration

A failed migration leaves the schema dirty. golang-migrate then records the failed version as
dirty and refuses every further `up` until it is resolved. Keep the backup taken before the
upgrade and inspect the schema. Either restore the backup ([BACKUP_RESTORE.md](BACKUP_RESTORE.md)),
or revert the partial changes of the failed migration by hand and run the same `bin/migrate`
command with `-command force -version <V>` in place of `-command up`, where `<V>` is the last version that applied cleanly (the one before the dirty version).
Force the failed version itself only if you completed its statements by hand; forcing it
otherwise skips it for good. Then rerun the command with `-command up`.

## SQLite release by release

These notes apply to the Compose SQLite image and to a backend binary run from source alike.
Take a backup before upgrading ([BACKUP_RESTORE.md](BACKUP_RESTORE.md#sqlite)). The schema
advances only through auto-migration on startup, which is not reversible (the `migrations/` SQL
is MySQL-only).

The release after 1.2.0 adds the company link to `leads` and `customers`, and the SQLite driver
adds a foreign key by rebuilding the table — drop and copy. The two rebuilds run one after the
other, each in its own transaction, so the first start needs free space for a full copy of one
table at a time — in the `/data` volume for the image, in the directory of `DB_PATH` for a
binary — while the WAL file accumulates both rebuilds until the next checkpoint. Auto-migration
runs with foreign-key enforcement switched off, as SQLite prescribes for table rebuilds, checks
every foreign key once it is done and refuses to start if the check finds a dangling reference.
A refused start has already committed the schema change; only the data violation is left. The
startup log (`docker compose -f docker-compose.sqlite.yml logs backend` for the image) names the
tables involved as `child -> parent`; either restore the backup or fix those rows, then start
again. The 1.2.0 image or binary still opens the upgraded file — it ignores the extra tables and
columns — so the image or binary can be rolled back even though the schema is not.

The deals release adds the `deals` and `deal_stage_changes` tables only: it rebuilds no existing
table, and the previous image or binary still opens the file. The quotes release does the same
with `products`, `quotes`, `quote_lines` and `quote_status_changes`: four new tables, no
existing table touched, and the previous image or binary still opens the file.

The task-to-deal link release does touch one: it adds `tasks.deal_id` and rebuilds `tasks` —
drop and copy, in one transaction with enforcement off — to add `fk_tasks_deal`, so the first
start needs free space (the `/data` volume, or the directory of `DB_PATH`) for a full copy of
`tasks`, and the WAL file holds that copy until the next checkpoint. Every existing task keeps
`deal_id` NULL. The previous image or binary still opens the upgraded file: it ignores the extra
column, so it neither shows nor removes a stored link, and the link is back when the newer
release returns.
