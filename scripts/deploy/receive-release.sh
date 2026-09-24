#!/usr/bin/env bash
# Receives and activates a GopherCRM release on the production server.
#
# It is the only thing the CI deploy key may run: authorized_keys pins the key
# to this script with `restrict,command="…/receive-release"`, and the caller's
# requested command arrives in SSH_ORIGINAL_COMMAND:
#
#   deploy <sha>   read a release tarball from stdin and activate it
#   rollback       re-activate the previous release
#   status         print the active and the kept releases
#
# A release tarball holds bin/gophercrm, bin/create-admin, www/ (the built SPA)
# and REVISION (the commit sha). Activation:
#   1. unpack into releases/<sha> and check the contents,
#   2. back up the database (the deploy aborts if the backup fails),
#   3. point `current` at the release (atomic symlink swap) and sync the SPA,
#   4. restart the service and wait until /health reports the new revision,
#   5. on any failure after the switch, re-activate the previous release.
#
# Paths and names come from the environment so the script can be tested
# against a scratch directory (scripts/deploy/receive-release_test.sh).
set -euo pipefail
umask 022

# Server-specific settings (the web root, for instance) live next to the
# script on the server, written by scripts/deploy/bootstrap.sh, so none of them
# is kept in the repository.
CONFIG=${GOPHERCRM_DEPLOY_CONFIG:-/srv/gophercrm/deploy/receive-release.conf}
if [ -f "$CONFIG" ]; then
  # shellcheck source=/dev/null
  . "$CONFIG"
fi

ROOT=${GOPHERCRM_ROOT:-/srv/gophercrm}
WEB_ROOT=${GOPHERCRM_WEB_ROOT:-/var/www/gophercrm}
ENV_FILE=${GOPHERCRM_ENV_FILE:-$ROOT/env/gophercrm.env}
SERVICE=${GOPHERCRM_SERVICE:-gophercrm}
HEALTH_URL=${GOPHERCRM_HEALTH_URL:-http://127.0.0.1:8080/health}
HEALTH_WAIT=${GOPHERCRM_HEALTH_WAIT:-60}
KEEP_RELEASES=${GOPHERCRM_KEEP_RELEASES:-5}
KEEP_BACKUPS=${GOPHERCRM_KEEP_BACKUPS:-10}
MAX_RELEASE_BYTES=${GOPHERCRM_MAX_RELEASE_BYTES:-209715200} # 200 MiB

RELEASES=$ROOT/releases
BACKUPS=$ROOT/backups
CURRENT=$ROOT/current

log() { echo "receive-release: $*"; }
die() { echo "receive-release: $*" >&2; exit 1; }

mkdir -p "$RELEASES" "$BACKUPS"

# One deploy at a time (flock is util-linux: always on the server; the test
# suite may run where it is missing).
exec 9>"$ROOT/deploy.lock"
if command -v flock >/dev/null; then
  flock -n 9 || die "another deploy is running"
fi

active_revision() {
  if [ -L "$CURRENT" ]; then
    basename "$(readlink "$CURRENT")"
  fi
}

previous_revision() {
  [ -f "$RELEASES/.previous" ] && cat "$RELEASES/.previous" || true
}

# The layout before the first automated deploy had the binary directly in
# bin/. Keep it as the "legacy" release so the first deploy can roll back to
# it, and turn bin/* into links that follow `current`.
adopt_legacy_layout() {
  if [ -L "$CURRENT" ] || [ ! -f "$ROOT/bin/gophercrm" ] || [ -L "$ROOT/bin/gophercrm" ]; then
    return
  fi
  log "adopting the pre-pipeline layout as release 'legacy'"
  mkdir -p "$RELEASES/legacy/bin" "$RELEASES/legacy/www"
  cp -p "$ROOT/bin/gophercrm" "$RELEASES/legacy/bin/gophercrm"
  if [ -f "$ROOT/bin/create-admin" ]; then
    cp -p "$ROOT/bin/create-admin" "$RELEASES/legacy/bin/create-admin"
  fi
  if [ -d "$WEB_ROOT" ]; then
    rsync -a "$WEB_ROOT/" "$RELEASES/legacy/www/"
  fi
  echo legacy >"$RELEASES/legacy/REVISION"
  ln -sfn "$RELEASES/legacy" "$CURRENT"
  link_binaries
}

link_binaries() {
  mkdir -p "$ROOT/bin"
  ln -sfn "$CURRENT/bin/gophercrm" "$ROOT/bin/gophercrm"
  ln -sfn "$CURRENT/bin/create-admin" "$ROOT/bin/create-admin"
}

# Points `current` at a release with an atomic rename and syncs its SPA.
activate() {
  local revision=$1
  ln -sfn "$RELEASES/$revision" "$CURRENT.next"
  mv -Tf "$CURRENT.next" "$CURRENT" 2>/dev/null || { rm -f "$CURRENT"; mv -f "$CURRENT.next" "$CURRENT"; }
  link_binaries
  mkdir -p "$WEB_ROOT"
  rsync -a --delete "$RELEASES/$revision/www/" "$WEB_ROOT/"
}

# Restarts the service and waits for /health to report the given revision. The
# pre-pipeline binary ('legacy') predates the revision field, so for it a
# healthy status is enough.
restart_and_check() {
  local revision=$1 waited=0 expect
  expect="\"revision\":\"$revision\""
  [ "$revision" = legacy ] && expect='"status":"healthy"'
  systemctl restart "$SERVICE"
  while [ "$waited" -lt "$HEALTH_WAIT" ]; do
    if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q "$expect"; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

# Reads one KEY=VALUE from the service environment file.
env_value() {
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1 | sed "s/^[\"']//; s/[\"']\$//"
}

backup_database() {
  local revision=$1 stamp defaults target
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  target="$BACKUPS/$stamp-$revision.sql.gz"
  defaults=$(mktemp)
  chmod 600 "$defaults"
  {
    echo "[client]"
    echo "user=$(env_value DB_USER)"
    echo "password=$(env_value DB_PASSWORD)"
    echo "host=$(env_value DB_HOST)"
    echo "port=$(env_value DB_PORT)"
  } >"$defaults"
  if ! mysqldump --defaults-extra-file="$defaults" --single-transaction --routines --triggers \
    "$(env_value DB_NAME)" | gzip >"$target"; then
    rm -f "$defaults" "$target"
    return 1
  fi
  rm -f "$defaults"
  log "database backed up to $target"
}

# all_but_newest N prints its input (oldest first) without the last N lines.
all_but_newest() {
  awk -v keep="$1" '{ line[NR] = $0 } END { for (i = 1; i <= NR - keep; i++) print line[i] }'
}

# Keeps the active and the previous release, 'legacy', and the newest others
# up to KEEP_RELEASES in total; keeps the newest KEEP_BACKUPS backups.
prune() {
  local active previous others
  active=$(active_revision)
  previous=$(previous_revision)
  others=$((KEEP_RELEASES - 2 > 0 ? KEEP_RELEASES - 2 : 0))
  ls -1tr "$RELEASES" | awk -v a="$active" -v p="$previous" \
    '$0 !~ /^\./ && $0 != "legacy" && $0 != a && $0 != p' |
    all_but_newest "$others" |
    while read -r old; do rm -rf "${RELEASES:?}/$old"; done
  ls -1tr "$BACKUPS" | all_but_newest "$KEEP_BACKUPS" |
    while read -r old; do rm -f "${BACKUPS:?}/$old"; done
}

deploy() {
  local revision=$1 staging
  [[ $revision =~ ^[0-9a-f]{7,40}$ ]] || die "not a commit sha: '$revision'"
  adopt_legacy_layout

  staging="$RELEASES/.incoming-$revision"
  rm -rf "$staging"
  mkdir -p "$staging"
  head -c "$MAX_RELEASE_BYTES" | tar -xzf - -C "$staging" || { rm -rf "$staging"; die "could not unpack the release"; }
  for required in bin/gophercrm www/index.html REVISION; do
    [ -e "$staging/$required" ] || { rm -rf "$staging"; die "release is missing $required"; }
  done
  [ "$(cat "$staging/REVISION")" = "$revision" ] || { rm -rf "$staging"; die "REVISION does not match $revision"; }
  chmod 755 "$staging/bin/"*

  backup_database "$revision" || { rm -rf "$staging"; die "database backup failed; nothing changed"; }

  local previous
  previous=$(active_revision)
  rm -rf "${RELEASES:?}/$revision"
  mv "$staging" "$RELEASES/$revision"
  activate "$revision"
  if restart_and_check "$revision"; then
    [ -n "$previous" ] && [ "$previous" != "$revision" ] && echo "$previous" >"$RELEASES/.previous"
    log "revision $revision is live"
    prune
    return 0
  fi

  if [ -n "$previous" ] && [ "$previous" != "$revision" ]; then
    log "revision $revision failed its health check; rolling back to $previous" >&2
    activate "$previous"
    restart_and_check "$previous" || die "rollback to $previous also failed its health check"
    die "deploy of $revision rolled back to $previous"
  fi
  die "revision $revision failed its health check and there is no previous release"
}

rollback() {
  local current previous
  current=$(active_revision)
  previous=$(previous_revision)
  [ -n "$previous" ] && [ -d "$RELEASES/$previous" ] || die "no previous release to roll back to"
  activate "$previous"
  restart_and_check "$previous" || die "rolled back to $previous but it failed its health check"
  echo "$current" >"$RELEASES/.previous"
  log "rolled back from $current to $previous"
}

status() {
  echo "active: $(active_revision)"
  echo "previous: $(previous_revision)"
  echo "kept releases:"
  ls -1t "$RELEASES" | grep -v '^\.' | sed 's/^/  /'
}

read -r -a request <<<"${SSH_ORIGINAL_COMMAND:-status}"
case "${request[0]:-}" in
  deploy) deploy "${request[1]:-}" ;;
  rollback) rollback ;;
  status) status ;;
  *) die "unknown command '${request[0]:-}' (expected deploy <sha>, rollback or status)" ;;
esac
