# Backup and restore

> **For:** Operators backing up and restoring a GopherCRM database. Part of the [documentation index](../README.md).

This page is the one place for backing up and restoring the database, per deployment. The
deployment guides keep their own commands and link here. Back up before every upgrade:
[upgrades.md](upgrades.md) gives the order.

## MySQL or MariaDB

### Release tarball on a systemd server

Before every deploy `receive-release` writes `/srv/gophercrm/backups/<UTC timestamp>-<sha>.sql.gz`
with `mysqldump` and the `DB_*` values of the environment file (`backup_database` in
`scripts/deploy/receive-release.sh`); [systemd.md](systemd.md#backups)
has the directory and retention details. A failed dump aborts the deploy before anything changes.

These dumps are taken only when a deploy runs, and they stay on the same server. Schedule your own
`mysqldump` for regular backups and copy the files to another machine.

Restoring is manual, for example `gunzip -c <file> | mysql -h 127.0.0.1 -u gophercrm -p gophercrm` with the service stopped.

### Compose MySQL stack

MySQL data lives in the named volume `db_data`, mounted at `/var/lib/mysql`. It survives
`docker compose stop`/`start` **and** `docker compose down`; `docker compose down -v` destroys the
volume, and that is the only way the data is lost ([docker.md](docker.md#data-persistence)).
No guide in this repository gives a backup or restore command for this stack's database yet.

## SQLite

### The database is three files

SQLite runs in WAL mode, so the database is `gophercrm.db` **plus** `gophercrm.db-wal` and
`gophercrm.db-shm`. A graceful shutdown checkpoints the WAL back into the main file; a process
killed outright leaves it in place, which is normal and recovers on the next open.

**Never copy a live database file.** A bare `cp` of `gophercrm.db` while the backend is running
yields a stale or torn copy. WAL mode lets a *separate* reader, such as the `VACUUM INTO`
snapshot below or a `sqlite3` shell, read without being blocked by the backend's writes.

### From source

Either stop the backend and copy all three files, or take an online snapshot with
`sqlite3 gophercrm.db "VACUUM INTO 'backup.db'"` (shown for the default `DB_PATH`).

### Compose SQLite stack

The volume's full Docker name is `gophercrm-sqlite_gophercrm-sqlite-data` (project name plus
volume name), which is what a throwaway container mounts. Two safe options:

```bash
# 1. Offline copy: stop the app, then copy the file and its WAL siblings.
#    The glob assumes the database exists — on a stack that has never booted
#    there is nothing to copy and cp reports "no such file".
docker compose -f docker-compose.sqlite.yml stop backend
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data -v "$PWD:/backup" \
  alpine:3.20 sh -c 'cp -a /data/gophercrm.db* /backup/'
docker compose -f docker-compose.sqlite.yml start backend
```

```bash
# 2. Online snapshot, nothing stopped: VACUUM INTO writes one consistent file
#    (the sqlite3 CLI comes from the throwaway container, not the app image).
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data -v "$PWD:/backup" \
  alpine:3.20 sh -c "apk add --no-cache sqlite >/dev/null && \
    sqlite3 /data/gophercrm.db \"VACUUM INTO '/backup/gophercrm-backup.db'\""
```

The database file lives in the named volume `gophercrm-sqlite-data` mounted at `/data`. It
survives `down` and is destroyed only by `docker compose -f docker-compose.sqlite.yml down -v`.

### Restoring SQLite

Restore with the backend stopped, and match the restore to the backup:

- **Offline copy (option 1):** put back `gophercrm.db` together with the
  `gophercrm.db-wal` and `gophercrm.db-shm` copied with it, replacing all
  three. The WAL can hold committed transactions that are not yet in the main
  file — the backend can exit without a final checkpoint (`main` (`cmd/main.go`)
  calls `models.CloseDatabase` only after a clean drain) — so deleting it, or mixing it with another backup's main file, loses data.
- **`VACUUM INTO` snapshot (option 2):** it is one self-contained file. Put it
  back as `gophercrm.db` (`/data/gophercrm.db` in the Compose volume) and delete any `-wal`/`-shm` left
  next to it, which belong to the database you are replacing.

In the Compose SQLite stack, a file written by a root container lands root-owned — the throwaway
container runs as root, the backend runs as `gophercrm` (uid 10001) — so hand the restored files
back before starting the backend, in the same root container that put them in place:

```bash
docker run --rm -v gophercrm-sqlite_gophercrm-sqlite-data:/data \
  alpine:3.20 sh -c 'chown 10001 /data/gophercrm.db*'
```

Without it the backend opens the database read-only and fails on the first
write ("attempt to write a readonly database").

## Secrets and restored backups

A restored database needs the secrets it was written with: `API_KEY_SECRET` (or `JWT_SECRET` when
it is unset) keys the API keys, tokens and encrypted settings stored in it, as
[../reference/configuration.md](../reference/configuration.md#secrets-and-rotation) lists. A backup also brings back older
`token_version` values, so access tokens ended after it was taken pass again until they expire.
