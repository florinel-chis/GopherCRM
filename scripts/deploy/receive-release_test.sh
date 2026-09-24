#!/usr/bin/env bash
# Tests scripts/deploy/receive-release.sh against a scratch "server": a fake
# root and web root, with systemctl, curl and mysqldump stubbed. The stubbed
# service answers /health with the revision of whatever `current` points at,
# unless a test marks that revision unhealthy.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/receive-release.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

fail() {
  echo "receive-release test: $1" >&2
  failures=$((failures + 1))
}
check() { # check <description> <command...>
  local description=$1
  shift
  "$@" || fail "$description"
}

# --- fake server -----------------------------------------------------------------
setup() {
  rm -rf "$work/srv" "$work/www" "$work/stubs" "$work/state"
  mkdir -p "$work/srv/bin" "$work/srv/env" "$work/www" "$work/stubs" "$work/state"
  printf 'DB_HOST=127.0.0.1\nDB_PORT=3306\nDB_USER=crm\nDB_PASSWORD="s3cret"\nDB_NAME=gophercrm\n' >"$work/srv/env/gophercrm.env"
  # The pre-pipeline layout: a plain binary and a deployed SPA.
  printf '#!/bin/sh\necho legacy\n' >"$work/srv/bin/gophercrm"
  chmod +x "$work/srv/bin/gophercrm"
  echo "legacy spa" >"$work/www/index.html"

  cat >"$work/stubs/systemctl" <<EOF
#!/bin/sh
# restart: the service comes up as whatever release \`current\` points at.
echo "\$*" >>"$work/state/systemctl.log"
revision=\$(cat "$work/srv/current/REVISION")
if [ -e "$work/state/unhealthy-\$revision" ]; then
  echo '{"success":false}' >"$work/state/health.json"
elif [ "\$revision" = legacy ]; then
  echo '{"success":true,"data":{"status":"healthy"}}' >"$work/state/health.json"
else
  echo "{\"success\":true,\"data\":{\"status\":\"healthy\",\"revision\":\"\$revision\"}}" >"$work/state/health.json"
fi
EOF
  printf '#!/bin/sh\ncat "%s/state/health.json"\n' "$work" >"$work/stubs/curl"
  cat >"$work/stubs/mysqldump" <<EOF
#!/bin/sh
[ -e "$work/state/fail-dump" ] && exit 1
defaults=\$(echo "\$1" | sed 's/^--defaults-extra-file=//')
cp "\$defaults" "$work/state/last-defaults"
echo "-- dump of \$(eval echo \\\${\$#})"
EOF
  chmod +x "$work/stubs/"*
}

run() { # run <ssh command> [stdin file]
  local input=${2:-/dev/null}
  PATH="$work/stubs:$PATH" SSH_ORIGINAL_COMMAND=$1 GOPHERCRM_DEPLOY_CONFIG="$work/none.conf" \
    GOPHERCRM_ROOT="$work/srv" GOPHERCRM_WEB_ROOT="$work/www" GOPHERCRM_HEALTH_WAIT=2 \
    GOPHERCRM_KEEP_RELEASES=3 GOPHERCRM_KEEP_BACKUPS=4 \
    "$script" <"$input" >"$work/state/out" 2>&1
}

build() { # build <sha> -> directory holding a well-formed release
  local sha=$1 dir="$work/build-$1"
  rm -rf "$dir"
  mkdir -p "$dir/bin" "$dir/www"
  printf '#!/bin/sh\necho %s\n' "$sha" >"$dir/bin/gophercrm"
  printf '#!/bin/sh\necho admin\n' >"$dir/bin/create-admin"
  echo "spa $sha" >"$dir/www/index.html"
  echo "$sha" >"$dir/REVISION"
  echo "$dir"
}
pack() { # pack <sha> <dir> -> path of a tarball
  tar -czf "$work/$1.tgz" -C "$2" .
  echo "$work/$1.tgz"
}
release() { # release <sha> [omit-path] -> path of a tarball
  local sha=$1 omit=${2:-} dir
  dir=$(build "$sha")
  [ -n "$omit" ] && rm -rf "${dir:?}/$omit"
  pack "$sha" "$dir"
}

run_with_config() { # like run, but with the config file under $work/conf
  PATH="$work/stubs:$PATH" SSH_ORIGINAL_COMMAND=$1 GOPHERCRM_DEPLOY_CONFIG="$work/conf/receive-release.conf" \
    GOPHERCRM_ROOT="$work/srv" GOPHERCRM_HEALTH_WAIT=2 "$script" </dev/null >"$work/state/out" 2>&1
}
owner_and_mode() { stat -c '%u %a' "$1" 2>/dev/null || stat -f '%u %Lp' "$1"; }

active() { basename "$(readlink "$work/srv/current")"; }
spa() { cat "$work/www/index.html"; }

# --- tests -------------------------------------------------------------------------

setup
check "first deploy succeeds" run "deploy aaaaaaa" "$(release aaaaaaa)"
check "first deploy activates the release" [ "$(active)" = aaaaaaa ]
check "the SPA is synced" [ "$(spa)" = "spa aaaaaaa" ]
check "the old layout is kept as release 'legacy'" [ -x "$work/srv/releases/legacy/bin/gophercrm" ]
check "the legacy SPA is kept for rollback" [ "$(cat "$work/srv/releases/legacy/www/index.html")" = "legacy spa" ]
check "bin/gophercrm follows current" [ "$("$work/srv/bin/gophercrm")" = aaaaaaa ]
check "a database backup is written" test -n "$(ls "$work/srv/backups/" | grep -e "-aaaaaaa.sql.gz$")"
check "the dump gets the credentials from the env file" grep -qx 'password="s3cret"' "$work/state/last-defaults"
check "the previous release is recorded" [ "$(cat "$work/srv/releases/.previous")" = legacy ]

check "second deploy succeeds" run "deploy bbbbbbb" "$(release bbbbbbb)"
check "second deploy is active" [ "$(active)" = bbbbbbb ]

touch "$work/state/unhealthy-ccccccc"
if run "deploy ccccccc" "$(release ccccccc)"; then fail "an unhealthy release must fail the deploy"; fi
check "a failed health check rolls back" [ "$(active)" = bbbbbbb ]
check "the rollback restores the previous SPA" [ "$(spa)" = "spa bbbbbbb" ]
check "the failure is reported" grep -q "rolled back to bbbbbbb" "$work/state/out"

check "rollback command succeeds" run rollback
check "rollback re-activates the previous release" [ "$(active)" = aaaaaaa ]
check "rolling back twice returns" run rollback
check "the second rollback swaps back" [ "$(active)" = bbbbbbb ]

check "status succeeds" run status
check "status names the active release" grep -qx "active: bbbbbbb" "$work/state/out"

if run "deploy ddddddd" "$(release ddddddd bin/gophercrm)"; then fail "a release without its binary must be rejected"; fi
check "a malformed release changes nothing" [ "$(active)" = bbbbbbb ]
check "a malformed release is not kept" [ ! -e "$work/srv/releases/ddddddd" ]

if run "deploy eeeeeee" "$(release fffffff)"; then fail "a release whose REVISION differs must be rejected"; fi
check "a mismatched release changes nothing" [ "$(active)" = bbbbbbb ]

if run "deploy not-a-sha"; then fail "a non-sha argument must be rejected"; fi
if run "reboot"; then fail "unknown commands must be rejected"; fi

touch "$work/state/fail-dump"
restarts_before=$(wc -l <"$work/state/systemctl.log")
if run "deploy 1111111" "$(release 1111111)"; then fail "a failed backup must stop the deploy"; fi
rm "$work/state/fail-dump"
check "a failed backup changes nothing" [ "$(active)" = bbbbbbb ]
check "a failed backup restarts nothing" [ "$(wc -l <"$work/state/systemctl.log")" -eq "$restarts_before" ]

# A crafted archive must not reach outside the release: links are refused.
mkdir -p "$work/secret"
echo "private" >"$work/secret/data"
chmod 600 "$work/secret/data"
dir=$(build 7777777)
ln -s "$work/secret/data" "$dir/bin/evil"
if run "deploy 7777777" "$(pack 7777777 "$dir")"; then fail "a release with a linked binary must be rejected"; fi
check "a linked binary changes nothing" [ "$(active)" = bbbbbbb ]
check "a linked binary never has its target's mode changed" [ "$(owner_and_mode "$work/secret/data" | cut -d' ' -f2)" = 600 ]
check "a release with links is not kept" [ ! -e "$work/srv/releases/7777777" ] && [ ! -e "$work/srv/releases/.incoming-7777777" ]
dir=$(build 8888888)
rm -rf "$dir/www"
ln -s "$work/secret" "$dir/www"
if run "deploy 8888888" "$(pack 8888888 "$dir")"; then fail "a release with a linked www must be rejected"; fi
check "a linked www never reaches the web root" [ ! -e "$work/www/data" ]
check "the failure names links" grep -q "links or special files" "$work/state/out"

check "redeploying the live revision succeeds" run "deploy bbbbbbb" "$(release bbbbbbb)"
check "redeploying the live revision says so" grep -q "already live" "$work/state/out"
check "redeploying the live revision keeps it in place" [ -x "$work/srv/releases/bbbbbbb/bin/gophercrm" ]

if run "deploy 9999999 junk" "$(release 9999999)"; then fail "extra arguments must be rejected"; fi
check "extra arguments change nothing" [ "$(active)" = bbbbbbb ]

for sha in 2222222 3333333 4444444 5555555 6666666; do
  sleep 1 # distinct modification times for the retention order
  check "deploy $sha" run "deploy $sha" "$(release $sha)"
done
kept=$(ls -1 "$work/srv/releases" | grep -vx legacy | grep -cv '^\.')
check "at most KEEP_RELEASES releases besides legacy are kept (kept $kept)" [ "$kept" -le 3 ]
check "the active release is kept" [ -d "$work/srv/releases/6666666" ]
check "the previous release is kept" [ -d "$work/srv/releases/5555555" ]
check "legacy is always kept" [ -d "$work/srv/releases/legacy" ]
check "old backups are pruned" [ "$(ls -1 "$work/srv/backups" | wc -l)" -le 4 ]

# The env file may leave out optional keys, and passwords may hold option-file
# syntax: the dump credentials must still come out right.
setup
printf 'DB_USER=crm\nDB_PASSWORD="p#ss\\w\"rd"\nDB_NAME=gophercrm\n' >"$work/srv/env/gophercrm.env"
check "a deploy with a minimal env file succeeds" run "deploy abcdef1" "$(release abcdef1)"
check "empty keys are left out of the dump options" [ -z "$(grep -E '^(host|port)=' "$work/state/last-defaults")" ]
check "the password is quoted and escaped" grep -qxF 'password="p#ss\\w\"rd"' "$work/state/last-defaults"

# A fresh server without the pre-pipeline binary.
setup
rm "$work/srv/bin/gophercrm"
check "a first deploy on a fresh server succeeds" run "deploy abcdef2" "$(release abcdef2)"
check "no legacy release is invented" [ ! -e "$work/srv/releases/legacy" ]
if run rollback; then fail "rollback without a previous release must fail"; fi
check "a refused rollback changes nothing" [ "$(active)" = abcdef2 ]

# Directories the script trusts must not be writable by anyone else.
setup
chmod g+w "$work/srv"
if run status; then fail "a group-writable root must be refused"; fi
check "the refusal says why" grep -q "writable by group or others" "$work/state/out"
chmod g-w "$work/srv"
mkdir -p "$work/srv/backups"
chmod 777 "$work/srv/backups"
if run status; then fail "a world-writable backups directory must be refused"; fi
chmod 700 "$work/srv/backups"
mkdir -p "$work/conf"
printf 'GOPHERCRM_WEB_ROOT=%q\n' "$work/www" >"$work/conf/receive-release.conf"
chmod 666 "$work/conf/receive-release.conf"
if GOPHERCRM_DEPLOY_CONFIG="$work/conf/receive-release.conf" run_with_config status; then
  fail "a writable config must not be sourced"
fi
chmod 644 "$work/conf/receive-release.conf"
check "a private config is sourced" run_with_config status

if [ "$failures" -gt 0 ]; then
  echo "receive-release test: $failures check(s) failed" >&2
  exit 1
fi
echo "receive-release test passed: deploy, legacy adoption, health rollback, rollback/status, rejected releases, links, same-revision redeploy, backup failure and options, retention, ownership checks."
