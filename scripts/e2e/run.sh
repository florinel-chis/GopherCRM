#!/usr/bin/env bash
# Runs the Playwright e2e suite against a real backend and a freshly reset
# MySQL or MariaDB database. Used by `make e2e` locally and by the CI e2e job, so both
# exercise exactly the same steps.
#
#   scripts/e2e/run.sh                               # whole suite
#   scripts/e2e/run.sh e2e/tests/admin-leads.spec.ts # selected specs (paths relative to gocrm-ui/)
#
# Environment (each falls back to the value in .env, then to a default):
#   DB_HOST, DB_PORT, DB_USER, DB_PASSWORD   MySQL or MariaDB server and credentials
#   JWT_SECRET, API_KEY_SECRET               passed through to the backend
#   E2E_DB_NAME    database to reset and use (default gocrm_e2e; must end in _e2e)
#   E2E_API_PORT   backend port (default: first free port from 18091)
#   E2E_UI_PORT    Vite port    (default: first free port from 15173)
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
db_name=${E2E_DB_NAME:-gocrm_e2e}

# The database is dropped and recreated on every run. Refuse any name that is
# not unmistakably a test database, before anything else happens.
if ! [[ $db_name =~ ^[A-Za-z0-9_]+_e2e$ ]]; then
  echo "e2e: refusing database '$db_name': the e2e database is dropped on every run, so its name must end in _e2e" >&2
  exit 2
fi

# Keep a laptop awake for the length of the run: an idle sleep in the middle
# of the suite shows up as a cascade of page-load timeouts, not as a failure
# of anything under test. caffeinate only exists on macOS.
if [ "$(uname -s)" = Darwin ] && [ -z "${E2E_CAFFEINATED:-}" ] && command -v caffeinate >/dev/null; then
  E2E_CAFFEINATED=1 exec caffeinate -i "$0" "$@"
fi

# Fill unset variables from .env (see dotenv.sh); the environment always wins.
# shellcheck source=scripts/e2e/dotenv.sh
. "$repo_root/scripts/e2e/dotenv.sh"
load_dotenv "$repo_root/.env" DB_HOST DB_PORT DB_USER DB_PASSWORD JWT_SECRET API_KEY_SECRET
: "${DB_HOST:=127.0.0.1}" "${DB_PORT:=3306}" "${DB_USER:=gophercrm}" "${DB_PASSWORD:=}"
if [ -z "${JWT_SECRET:-}" ]; then
  echo "e2e: JWT_SECRET is not set (environment or .env)" >&2
  exit 2
fi
export DB_HOST DB_PORT DB_USER DB_PASSWORD JWT_SECRET

for tool in mysql go npx curl; do
  command -v "$tool" >/dev/null || { echo "e2e: '$tool' is required but not installed" >&2; exit 2; }
done

port_in_use() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}
free_port() {
  local port=$1
  while port_in_use "$port"; do port=$((port + 1)); done
  echo "$port"
}
api_port=${E2E_API_PORT:-$(free_port 18091)}
ui_port=${E2E_UI_PORT:-$(free_port 15173)}
[ "$ui_port" = "$api_port" ] && ui_port=$(free_port $((api_port + 1)))

# Pin everything the backend (and create-admin, run by Playwright's global
# setup) would otherwise take from a developer's .env. godotenv never
# overrides a variable that is set, even to an empty value, so these win.
# The run must be MySQL, on the default API prefix, and must never send real
# mail, call a paid answer engine or require reCAPTCHA.
export DB_DRIVER=mysql DB_NAME="$db_name" API_PREFIX=/api/v1 SERVER_MODE=development \
  TRUSTED_PROXIES= \
  SMTP_HOST= SMTP_USER= SMTP_PASSWORD= \
  RECAPTCHA_SITE_KEY= RECAPTCHA_SECRET_KEY= \
  AEO_SCHEDULE_ENABLED=false \
  ANTHROPIC_API_KEY= OPENAI_API_KEY= GEMINI_API_KEY= MOONSHOT_API_KEY= PERPLEXITY_API_KEY= \
  AEO_CUSTOM_BASE_URL= AEO_CUSTOM_API_KEY= \
  APP_BASE_URL="http://localhost:$ui_port" PUBLIC_BASE_URL="http://localhost:$api_port"

echo "e2e: resetting database $db_name on $DB_HOST:$DB_PORT"
MYSQL_PWD=$DB_PASSWORD mysql --protocol=TCP --connect-timeout=10 -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -e \
  "DROP DATABASE IF EXISTS \`$db_name\`; CREATE DATABASE \`$db_name\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

work_dir=$(mktemp -d)
# Playwright empties test-results/ when it starts, so the backend logs into the
# work directory and the log is copied into test-results/ on exit.
results_dir="$repo_root/gocrm-ui/test-results"
backend_log="$work_dir/e2e-backend.log"
backend_pid=""

cleanup() {
  if [ -n "$backend_pid" ] && kill -0 "$backend_pid" 2>/dev/null; then
    kill "$backend_pid" 2>/dev/null || true
    wait "$backend_pid" 2>/dev/null || true
  fi
  if [ -f "$backend_log" ]; then
    mkdir -p "$results_dir"
    cp "$backend_log" "$results_dir/e2e-backend.log"
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT

# A built binary rather than `go run`: go run starts the server as a child
# process that outlives a kill of the parent.
echo "e2e: building backend"
(cd "$repo_root" && go build -o "$work_dir/gophercrm-e2e" ./cmd)

echo "e2e: starting backend on :$api_port (log copied to gocrm-ui/test-results/e2e-backend.log on exit)"
(
  cd "$repo_root"
  export SERVER_PORT=$api_port DISABLE_RATE_LIMIT=true \
    CORS_ALLOWED_ORIGINS="http://localhost:$ui_port,http://127.0.0.1:$ui_port"
  exec "$work_dir/gophercrm-e2e"
) >"$backend_log" 2>&1 &
backend_pid=$!

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$api_port/health" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    echo "e2e: backend exited during startup; last log lines:" >&2
    tail -n 30 "$backend_log" >&2
    exit 1
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$api_port/health" >/dev/null || {
  echo "e2e: backend did not become healthy within 60 s; last log lines:" >&2
  tail -n 30 "$backend_log" >&2
  exit 1
}

echo "e2e: running Playwright (UI on :$ui_port)"
status=0
(
  cd "$repo_root/gocrm-ui"
  # The admin account is seeded by global setup through `go run ./cmd/create-admin`,
  # which inherits the variables pinned above and so writes into the e2e database.
  export E2E_UI_PORT=$ui_port PLAYWRIGHT_HTML_OPEN=never \
    VITE_API_BASE_URL="http://localhost:$api_port/api/v1"
  # No retries: a test that only passes on a second attempt is reported as a
  # failure, not hidden. With no retries, "on-first-retry" traces would never
  # be recorded, so failures keep theirs instead.
  npx playwright test --retries=0 --trace=retain-on-failure --reporter=line,html "$@"
) || status=$?
exit "$status"
