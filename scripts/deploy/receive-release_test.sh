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

release() { # release <sha> [omit-path] -> path of a tarball
  local sha=$1 omit=${2:-} dir="$work/build-$1"
  rm -rf "$dir"
  mkdir -p "$dir/bin" "$dir/www"
  printf '#!/bin/sh\necho %s\n' "$sha" >"$dir/bin/gophercrm"
  printf '#!/bin/sh\necho admin\n' >"$dir/bin/create-admin"
  echo "spa $sha" >"$dir/www/index.html"
  echo "$sha" >"$dir/REVISION"
  [ -n "$omit" ] && rm -rf "${dir:?}/$omit"
  tar -czf "$work/$sha.tgz" -C "$dir" .
  echo "$work/$sha.tgz"
}

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
check "the dump gets the credentials from the env file" grep -qx 'password=s3cret' "$work/state/last-defaults"
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

if [ "$failures" -gt 0 ]; then
  echo "receive-release test: $failures check(s) failed" >&2
  exit 1
fi
echo "receive-release test passed: deploy, legacy adoption, health rollback, rollback/status, rejected releases, backup failure, retention."
