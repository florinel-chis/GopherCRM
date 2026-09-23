#!/usr/bin/env bash
# Checks the safety guard of scripts/e2e/run.sh without touching any database:
# the e2e runner drops its database on every run, so it must refuse any name
# that does not end in _e2e, before it connects anywhere.
set -euo pipefail

runner="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/run.sh"
failures=0

for name in gocrm gophercrm gocrm_e2e_copy e2e production "gocrm_e2e; DROP DATABASE gocrm"; do
  set +e
  output=$(E2E_DB_NAME="$name" DB_HOST=192.0.2.1 "$runner" 2>&1)
  code=$?
  set -e
  if [ "$code" -ne 2 ] || [[ $output != *"refusing database"* ]]; then
    echo "selftest: run.sh accepted database name '$name' (exit $code)" >&2
    failures=$((failures + 1))
  fi
done

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "e2e runner guard refuses non-test database names."
