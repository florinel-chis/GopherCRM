#!/usr/bin/env bash
# Self-test for the e2e runner, without touching any database or server:
#
# 1. The guard of run.sh: the runner drops its database on every run, so it
#    must refuse any name that does not end in _e2e before it connects
#    anywhere, and must accept a proper _e2e name.
# 2. The .env reader (dotenv.sh) on the cases it has to get right.
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
echo "e2e runner self-test passed: guard refuses non-test databases and accepts _e2e; .env reader handles export, '=', quotes, comments and CRLF."
