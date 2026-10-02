#!/usr/bin/env bash
# Pins the command lines the Makefile gates run, through `make -n`:
#
# 1. without VERIFY_JOBS, verify-backend-steps and verify-frontend-steps print
#    exactly the commands CI has always run;
# 2. VERIFY_JOBS=N prefixes the Go commands with GOFLAGS="<existing> -p=N" and
#    appends --maxWorkers=N to Vitest; a value that is not a positive integer
#    is refused;
# 3. verify, verify-backend, verify-frontend and e2e go through
#    scripts/gate-lock.sh, and a dry run of them prints the steps without
#    waiting for a lock somebody else holds.
#
# The Go package lists are passed on the command line so that `go list` never
# runs (the hygiene job in CI has no Go toolchain), and `go env GOFLAGS` is
# answered by a shim that reports an existing flag to prove the merge.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"
failures=0
fail() {
  echo "makefile_test: $1" >&2
  failures=$((failures + 1))
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
cat >"$tmp/bin/go" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = env ] && [ "${2:-}" = GOFLAGS ]; then
  echo "-mod=mod"
  exit 0
fi
echo "go shim: unexpected call: $*" >&2
exit 1
EOF
chmod +x "$tmp/bin/go"

# Every make runs without the caller's gate or job settings, and with a private
# lock directory that a live holder occupies: a dry run that waited for it
# would time out and fail.
lock="$tmp/gate.lock"
mkdir "$lock"
printf 'pid=%s\nstart=now\nlabel=occupant\ncwd=%s\ncmd=sleep\n' "$$" "$tmp" >"$lock/holder"
dry() {
  env -u VERIFY_JOBS -u GOFLAGS -u CI -u GATE_LOCK -u GATE_LOCK_HELD -u MAKEFLAGS -u MFLAGS \
    PATH="$tmp/bin:$PATH" GATE_LOCK_DIR="$lock" GATE_LOCK_TIMEOUT=1 GATE_LOCK_POLL=0.1 \
    make -n "$@" GO_PKGS='./a ./b' GO_BUILD_PKGS='./a'
}
expect_output() {
  local what=$1 expected=$2
  shift 2
  local output rc
  set +e
  output=$(dry "$@" 2>&1)
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [ "$output" != "$expected" ]; then
    fail "$what (exit $rc): expected
$expected
got
$output"
  fi
}

# --- 1. the CI command lines, unchanged ------------------------------------------
expect_output "verify-backend-steps without VERIFY_JOBS" \
  'go build ./a
go vet ./a ./b
go test -race ./a ./b' verify-backend-steps
expect_output "verify-frontend-steps without VERIFY_JOBS" \
  'cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- --run' verify-frontend-steps

# --- 2. VERIFY_JOBS ----------------------------------------------------------------
expect_output "verify-backend-steps with VERIFY_JOBS=2" \
  'GOFLAGS="-mod=mod -p=2" go build ./a
GOFLAGS="-mod=mod -p=2" go vet ./a ./b
GOFLAGS="-mod=mod -p=2" go test -race ./a ./b' verify-backend-steps VERIFY_JOBS=2
expect_output "verify-frontend-steps with VERIFY_JOBS=2" \
  'cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- --run --maxWorkers=2' verify-frontend-steps VERIFY_JOBS=2
for bad in 0 -1 two 2.5 "2 3"; do
  set +e
  output=$(dry verify-backend-steps VERIFY_JOBS="$bad" 2>&1)
  rc=$?
  set -e
  if [ "$rc" -eq 0 ] || [[ $output != *"VERIFY_JOBS must be a positive integer, got '$bad'"* ]]; then
    fail "VERIFY_JOBS='$bad' was accepted (exit $rc): $output"
  fi
done

# --- 3. the lock around the gates, and dry runs that do not wait -----------------
# $(MAKE) expands to the running make's own path, which the lock lines echo.
make_path=$(printf 'x:\n\t@echo $(MAKE)\n' | make -f - x)
expect_output "verify-backend under the lock" \
  "scripts/gate-lock.sh verify-backend $make_path verify-backend-steps
go build ./a
go vet ./a ./b
go test -race ./a ./b" verify-backend
expect_output "verify-frontend under the lock" \
  "scripts/gate-lock.sh verify-frontend $make_path verify-frontend-steps
cd gocrm-ui && npm ci && npm run build && npm run lint && npm test -- --run" verify-frontend
expect_output "e2e under the lock" \
  'scripts/gate-lock.sh e2e env E2E_DB_DRIVER="sqlite" scripts/e2e/run.sh e2e/tests/login.spec.ts' e2e E2E_DB_DRIVER=sqlite SPECS=e2e/tests/login.spec.ts
set +e
output=$(dry verify 2>&1)
rc=$?
set -e
if [ "$rc" -ne 0 ] ||
  [[ $output != "scripts/gate-lock.sh verify $make_path verify-hygiene verify-backend verify-frontend"* ]] ||
  [[ $output != *"scripts/gate-lock.sh verify-backend $make_path verify-backend-steps"* ]] ||
  [[ $output != *"go test -race ./a ./b"* ]] ||
  [[ $output != *"npm test -- --run"* ]] ||
  [[ $output != *"scripts/gate-lock_test.sh"* ]]; then
  fail "make -n verify does not show one lock around hygiene, backend and frontend (exit $rc): $output"
fi
command grep -q '^label=occupant$' "$lock/holder" || fail "a dry run disturbed the occupied lock"

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "Makefile self-test passed: the verify-*-steps command lines are unchanged without VERIFY_JOBS; VERIFY_JOBS=N adds GOFLAGS -p=N (merged) and Vitest --maxWorkers=N and refuses other values; verify, verify-backend, verify-frontend and e2e run under scripts/gate-lock.sh and their dry runs never wait."
