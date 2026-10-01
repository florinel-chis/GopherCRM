#!/usr/bin/env bash
# Self-test for the e2e runner, without touching any database or server:
#
# 1. The guard of run.sh: the runner drops its database on every run, so it
#    must refuse any name that does not end in _e2e before it connects
#    anywhere, and must accept a proper _e2e name.
# 2. The driver switch: E2E_DB_DRIVER accepts mysql (the default) and sqlite
#    only; the plan (E2E_PLAN_ONLY=1, printed before any tool, database or
#    server is used) shows the sqlite mode on a new '?'-free file, with no
#    database reset, no mysql client and no database name, and the mysql mode
#    unchanged.
# 3. The reset step itself, not just its announcement: the runner is started
#    for real (E2E_PLAN_ONLY unset or 0, which must not take the plan-only
#    path) with mysql, go, npx and curl replaced by shims that record each
#    call and fail. In sqlite mode the run reaches `go build` without a single
#    mysql call; in mysql mode the first call is the mysql reset of gocrm_e2e
#    and the build is never reached. Either way nothing is built or started.
# 4. The .env reader (dotenv.sh) on the cases it has to get right.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
runner="$here/run.sh"
failures=0
fail() {
  echo "selftest: $1" >&2
  failures=$((failures + 1))
}

# --- guard -------------------------------------------------------------------
# E2E_CAFFEINATED skips the macOS keep-awake re-exec; DB_HOST points at a
# documentation address (TEST-NET-1) so nothing real is reachable even if a
# check below is wrong.
for name in gocrm gophercrm gocrm_e2e_copy e2e production "gocrm_e2e; DROP DATABASE gocrm"; do
  set +e
  output=$(E2E_CAFFEINATED=1 E2E_DB_NAME="$name" DB_HOST=192.0.2.1 "$runner" 2>&1)
  code=$?
  set -e
  if [ "$code" -ne 2 ] || [[ $output != *"refusing database"* ]]; then
    fail "run.sh accepted database name '$name' (exit $code)"
  fi
done

# A valid name must pass the guard. The run then stops at the next check,
# the missing JWT secret, before any tool or database is used; the empty
# temporary directory as repo root keeps .env from supplying one.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/scripts/e2e"
cp "$here/run.sh" "$here/dotenv.sh" "$tmp/scripts/e2e/"
set +e
output=$(env -u JWT_SECRET E2E_CAFFEINATED=1 E2E_DB_NAME=gocrm_e2e DB_HOST=192.0.2.1 "$tmp/scripts/e2e/run.sh" 2>&1)
code=$?
set -e
if [ "$code" -ne 2 ] || [[ $output == *"refusing database"* ]] || [[ $output != *"JWT_SECRET is not set"* ]]; then
  fail "run.sh did not accept the valid name gocrm_e2e (exit $code): $output"
fi

# --- driver switch -------------------------------------------------------------
# Plan runs use the copy in $tmp, whose empty root has no .env, and fixed ports
# so no port probing happens.
plan() {
  env -u DB_PATH -u DB_NAME E2E_CAFFEINATED=1 E2E_PLAN_ONLY=1 JWT_SECRET=selftest-secret-selftest-secret-selftest \
    E2E_API_PORT=1 E2E_UI_PORT=2 DB_HOST=192.0.2.1 "$@" "$tmp/scripts/e2e/run.sh" 2>&1
}

for driver in postgres MYSQL "sqlite;rm" " "; do
  set +e
  output=$(env E2E_CAFFEINATED=1 E2E_DB_DRIVER="$driver" DB_HOST=192.0.2.1 "$runner" 2>&1)
  code=$?
  set -e
  if [ "$code" -ne 2 ] || [[ $output != *"unsupported E2E_DB_DRIVER"* ]]; then
    fail "run.sh accepted E2E_DB_DRIVER '$driver' (exit $code): $output"
  fi
done

# sqlite: a database name that mysql mode would refuse is irrelevant here,
# because nothing is dropped; the plan must not reset, name or need MySQL.
set +e
output=$(plan E2E_DB_DRIVER=sqlite E2E_DB_NAME=production)
code=$?
set -e
db_path=$(sed -n 's/^e2e plan: db_path=//p' <<<"$output")
if [ "$code" -ne 0 ] ||
  [[ $output != *"e2e plan: driver=sqlite"* ]] ||
  [[ $output != *"e2e plan: db_reset=none"* ]] ||
  [[ $output != *"e2e plan: tools=go npx curl"* ]] ||
  [[ $output == *mysql* ]] || [[ $output == *production* ]] || [[ $output == *db_name* ]]; then
  fail "sqlite plan is wrong (exit $code): $output"
fi
if [[ $db_path != /*/e2e-sqlite.db ]] || [[ $db_path == *"?"* ]] || [[ $db_path == "$tmp"/* ]]; then
  fail "sqlite DB_PATH '$db_path' is not an absolute, '?'-free file in a fresh temporary directory"
fi
if [ -n "$db_path" ] && [ -e "$(dirname "$db_path")" ]; then
  fail "sqlite plan run left its temporary directory $(dirname "$db_path") behind"
fi

# mysql, explicit and by default: unchanged.
for driver_env in "E2E_DB_DRIVER=mysql" "E2E_DB_DRIVER="; do
  set +e
  output=$(plan "$driver_env")
  code=$?
  set -e
  if [ "$code" -ne 0 ] ||
    [[ $output != *"e2e plan: driver=mysql"* ]] ||
    [[ $output != *"e2e plan: db_name=gocrm_e2e db_host=192.0.2.1:3306"* ]] ||
    [[ $output != *"e2e plan: db_reset=drop-create"* ]] ||
    [[ $output != *"e2e plan: tools=mysql go npx curl"* ]] ||
    [[ $output == *db_path* ]]; then
    fail "mysql plan ($driver_env) is wrong (exit $code): $output"
  fi
done

# --- reset step ----------------------------------------------------------------
# The plan only announces db_reset; this runs the runner up to the reset
# decision and watches what it executes. The shims shadow every tool the runner
# requires, record their name and arguments, and exit 1, so a run stops at the
# first tool it calls after the reset decision: the mysql reset in mysql mode,
# `go build` in sqlite mode. Nothing is built, no server starts, and the
# recorded calls show which of the two happened.
shims="$tmp/bin"
calls="$tmp/calls"
mkdir -p "$shims"
for tool in mysql go npx curl; do
  cat >"$shims/$tool" <<EOF
#!/usr/bin/env bash
printf '%s\n' "$tool \$*" >>"$calls"
exit 1
EOF
  chmod +x "$shims/$tool"
done
run_to_reset() {
  rm -f "$calls"
  env -u DB_PATH -u DB_NAME E2E_CAFFEINATED=1 PATH="$shims:$PATH" \
    JWT_SECRET=selftest-secret-selftest-secret-selftest \
    E2E_API_PORT=1 E2E_UI_PORT=2 DB_HOST=192.0.2.1 "$@" "$tmp/scripts/e2e/run.sh" 2>&1
}
# A recorded call starts a line; the leading newline makes the first line match too.
called() {
  [ -f "$calls" ] && [[ $'\n'$(cat "$calls") == *$'\n'"$1 "* ]]
}

# sqlite, with E2E_PLAN_ONLY unset and set to 0 (only 1 means plan-only): the
# reset is skipped, mysql is never called, and the run gets as far as the build.
for plan_only in "" 0; do
  set +e
  output=$(run_to_reset E2E_PLAN_ONLY="$plan_only" E2E_DB_DRIVER=sqlite E2E_DB_NAME=production)
  code=$?
  set -e
  if [[ $output == *"e2e plan:"* ]]; then
    fail "E2E_PLAN_ONLY='$plan_only' took the plan-only path: $output"
  fi
  if [ "$code" -eq 0 ] || ! called go ||
    [[ $output != *"using a new SQLite file"* ]] || [[ $output != *"e2e: building backend"* ]] ||
    [[ $output == *"resetting database"* ]]; then
    fail "sqlite run (E2E_PLAN_ONLY='$plan_only') did not reach the build with the reset skipped (exit $code): $output"
  fi
  if called mysql; then
    fail "sqlite run (E2E_PLAN_ONLY='$plan_only') called mysql: $(cat "$calls")"
  fi
done

# mysql: the same run must call mysql to reset gocrm_e2e and, because the shim
# fails, never reach the build. This also proves the shims see the calls.
set +e
output=$(run_to_reset E2E_DB_DRIVER=mysql E2E_DB_NAME=gocrm_e2e)
code=$?
set -e
if [ "$code" -eq 0 ] || ! called mysql || called go ||
  [[ $output != *"resetting database gocrm_e2e on 192.0.2.1:3306"* ]] ||
  [[ $output == *"e2e: building backend"* ]] ||
  [[ $(cat "$calls") != *"DROP DATABASE IF EXISTS \`gocrm_e2e\`"* ]]; then
  fail "mysql run did not stop at the mysql reset of gocrm_e2e (exit $code): $output; calls: $(cat "$calls" 2>/dev/null)"
fi

# --- .env reader ---------------------------------------------------------------
# shellcheck source=scripts/e2e/dotenv.sh
. "$here/dotenv.sh"
envfile="$tmp/test.env"
printf '%s\r\n' '# a comment' 'export DB_HOST=db.internal' 'DB_PORT=3307 # inline comment' \
  'DB_USER="quoted # not a comment"' 'DB_PASSWORD=ends-in-equals=' 'IGNORED_KEY=nope' \
  'API_KEY_SECRET=from-file' >"$envfile"
printf '%s' 'JWT_SECRET=abc==' >>"$envfile" # last line without a newline
(
  unset DB_HOST DB_PORT DB_USER DB_PASSWORD JWT_SECRET IGNORED_KEY
  export API_KEY_SECRET=from-environment
  load_dotenv "$envfile" DB_HOST DB_PORT DB_USER DB_PASSWORD JWT_SECRET API_KEY_SECRET
  expect() {
    if [ "${!1:-<unset>}" != "$2" ]; then
      echo "selftest: dotenv $1: expected '$2', got '${!1:-<unset>}'" >&2
      exit 1
    fi
  }
  expect DB_HOST db.internal
  expect DB_PORT 3307
  expect DB_USER 'quoted # not a comment'
  expect DB_PASSWORD 'ends-in-equals='
  expect JWT_SECRET 'abc=='
  expect API_KEY_SECRET from-environment
  expect IGNORED_KEY '<unset>'
) || failures=$((failures + 1))

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "e2e runner self-test passed: guard refuses non-test databases and accepts _e2e; E2E_DB_DRIVER takes mysql (default, unchanged) or sqlite (new file, no reset, no mysql); only E2E_PLAN_ONLY=1 is plan-only; a sqlite run reaches the build without calling mysql and a mysql run resets gocrm_e2e first; .env reader handles export, '=', quotes, comments and CRLF."
