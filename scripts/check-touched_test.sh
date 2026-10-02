#!/usr/bin/env bash
# Self-test for scripts/check-touched.sh on a throwaway repository, with go and
# npx replaced by shims that record their calls. It proves that the script:
#
# 1. runs nothing when the branch changes nothing that it checks;
# 2. finds the Go packages changed by commits on the branch, by uncommitted
#    edits and by untracked files, keeps test-only packages out of go build,
#    and runs Vitest on the related changed files under gocrm-ui/src;
# 3. passes VERIFY_JOBS on as GOFLAGS -p=N (merged) and --maxWorkers=N;
# 4. reports a failing step in its exit status and stops the Go steps at the
#    first failure while the frontend step still runs;
# 5. works from a git worktree, drops a package whose Go files are all gone,
#    treats a go.mod change as touching every package, and refuses to pretend
#    the Vitest step ran when the tree has no node_modules.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/check-touched.sh"
failures=0
fail() {
  echo "check-touched_test: $1" >&2
  failures=$((failures + 1))
}

tmp=$(mktemp -d)
calls="$tmp/calls"
repo="$tmp/repo"
cleanup() {
  git -C "$repo" worktree remove --force "$tmp/wt" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

# --- shims --------------------------------------------------------------------------
mkdir -p "$tmp/bin"
cat >"$tmp/bin/go" <<EOF
#!/usr/bin/env bash
printf 'go %s\n' "\$*" >>"$calls"
printf 'GOFLAGS=%s\n' "\${GOFLAGS:-}" >>"$calls"
case "\${1:-}" in
  env) echo "-mod=mod" ;;
  list)
    if [ -n "\${CT_FAIL_LIST:-}" ]; then
      echo "go: shim refuses to list" >&2
      exit 1
    fi
    # A cold module cache makes go list report progress on stderr and still
    # succeed; those lines must never be taken for package names.
    echo "go: downloading example.com/dep v1.0.0" >&2
    if [[ \$* == *-f* ]]; then echo example.com/x/a; else printf '%s\n' example.com/x/a example.com/x/b; fi
    ;;
  build) [ -z "\${CT_FAIL_BUILD:-}" ] || exit 1 ;;
  test) [ -z "\${CT_FAIL_TEST:-}" ] || exit 1 ;;
esac
exit 0
EOF
cat >"$tmp/bin/npx" <<EOF
#!/usr/bin/env bash
printf 'npx %s\n' "\$*" >>"$calls"
exit 0
EOF
chmod +x "$tmp/bin/go" "$tmp/bin/npx"

# --- repository ---------------------------------------------------------------------
g() { git -C "$repo" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false "$@"; }
mkdir -p "$repo"
git -C "$repo" init -q
g symbolic-ref HEAD refs/heads/main
mkdir -p "$repo/a" "$repo/b" "$repo/d" "$repo/gocrm-ui/src" "$repo/gocrm-ui/node_modules/.bin"
printf 'module example.com/x\n\ngo 1.25\n' >"$repo/go.mod"
printf 'package a\n' >"$repo/a/a.go"
printf 'package a\n' >"$repo/a/a_test.go"
printf 'package b\n' >"$repo/b/b_test.go"
printf 'package d\n' >"$repo/d/d.go"
printf 'export const x = 1;\n' >"$repo/gocrm-ui/src/x.ts"
printf 'export const y = 1;\n' >"$repo/gocrm-ui/src/y.ts"
printf 'node_modules/\n' >"$repo/.gitignore"
printf '# readme\n' >"$repo/README.md"
printf '#!/bin/sh\nexit 0\n' >"$repo/gocrm-ui/node_modules/.bin/vitest"
chmod +x "$repo/gocrm-ui/node_modules/.bin/vitest"
g add -A
g commit -q -m base
g update-ref refs/remotes/origin/main HEAD

run() {
  rm -f "$calls"
  (cd "${RUN_DIR:-$repo}" && env -u VERIFY_JOBS -u GOFLAGS -u CT_FAIL_BUILD -u CT_FAIL_TEST -u CT_FAIL_LIST PATH="$tmp/bin:$PATH" "$@" "$script" 2>&1)
}
# Both newlines are added: command substitution drops the file's last one.
recorded() { [ -f "$calls" ] && [[ $'\n'$(cat "$calls")$'\n' == *$'\n'"$1"$'\n'* ]]; }
calls_text() { cat "$calls" 2>/dev/null || echo "(no calls)"; }

# --- 1. nothing touched --------------------------------------------------------------
set +e
output=$(run)
rc=$?
set -e
if [ "$rc" -ne 0 ] || [[ $output != *"nothing to run"* ]] || [ -f "$calls" ]; then
  fail "a clean main did not report nothing to run (exit $rc): $output; calls: $(calls_text)"
fi
printf 'more\n' >>"$repo/README.md"
set +e
output=$(run)
rc=$?
set -e
if [ "$rc" -ne 0 ] || [[ $output != *"nothing to run"* ]] || [ -f "$calls" ]; then
  fail "a README edit was not ignored (exit $rc): $output; calls: $(calls_text)"
fi
g checkout -q -- README.md

# --- 2. committed, uncommitted and untracked changes ---------------------------------
g checkout -q -b topic
printf 'package a\n\nvar A = 1\n' >"$repo/a/a.go"
g commit -q -am "change a"
printf 'package b\n\nfunc b() {}\n' >"$repo/b/b_test.go" # uncommitted, test-only package
mkdir -p "$repo/c"
printf 'package c\n' >"$repo/c/c.go" # untracked
printf 'export const x = 2;\n' >"$repo/gocrm-ui/src/x.ts" # uncommitted
printf 'export const w = 1;\n' >"$repo/gocrm-ui/src/w.tsx" # untracked
printf 'more\n' >>"$repo/README.md"
set +e
output=$(run)
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "the touched run failed (exit $rc): $output; calls: $(calls_text)"
recorded "go build ./a ./c" || fail "go build did not get exactly the changed packages with sources: $(calls_text)"
recorded "go vet ./a ./b ./c" || fail "go vet did not get every changed package: $(calls_text)"
recorded "go test -race -count=1 ./a ./b ./c" || fail "go test did not get every changed package: $(calls_text)"
recorded "npx vitest related src/w.tsx src/x.ts --run --passWithNoTests" || fail "vitest related did not get the changed frontend sources: $(calls_text)"
recorded "GOFLAGS=" || fail "GOFLAGS was set without VERIFY_JOBS: $(calls_text)"
if command grep -q 'GOFLAGS=.' "$calls"; then
  fail "GOFLAGS was set without VERIFY_JOBS: $(calls_text)"
fi
[[ $output == *"go build rc=0"*"go vet rc=0"*"go test rc=0"*"vitest related rc=0"*"exit 0"* ]] || fail "the summary lines are missing: $output"

# --- 3. VERIFY_JOBS ------------------------------------------------------------------
set +e
output=$(run VERIFY_JOBS=2)
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "the VERIFY_JOBS=2 run failed (exit $rc): $output"
recorded "GOFLAGS=-mod=mod -p=2" || fail "GOFLAGS did not merge -p=2 with the existing flags: $(calls_text)"
recorded "npx vitest related src/w.tsx src/x.ts --run --passWithNoTests --maxWorkers=2" || fail "vitest did not get --maxWorkers=2: $(calls_text)"
set +e
output=$(run VERIFY_JOBS=many)
rc=$?
set -e
[ "$rc" -eq 2 ] && [[ $output == *"VERIFY_JOBS must be a positive integer"* ]] || fail "VERIFY_JOBS=many was accepted (exit $rc): $output"

# --- 4. failures ---------------------------------------------------------------------
set +e
output=$(run CT_FAIL_TEST=1)
rc=$?
set -e
[ "$rc" -eq 1 ] || fail "a failing go test did not give exit 1 (exit $rc): $output"
[[ $output == *"go test rc=1"* ]] || fail "the failing step is not named: $output"
recorded "npx vitest related src/w.tsx src/x.ts --run --passWithNoTests" || fail "the frontend step did not run after a Go failure: $(calls_text)"
set +e
output=$(run CT_FAIL_BUILD=1)
rc=$?
set -e
[ "$rc" -eq 1 ] || fail "a failing go build did not give exit 1 (exit $rc): $output"
if recorded "go vet ./a ./b ./c" || recorded "go test -race -count=1 ./a ./b ./c"; then
  fail "vet or test ran on code that does not build: $(calls_text)"
fi

# --- 5. from a worktree ---------------------------------------------------------------
g worktree add -q "$tmp/wt" -b wt-topic main
wt="$tmp/wt"
gw() { git -C "$wt" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false "$@"; }
gw rm -q d/d.go
printf 'package a\n\nfunc TestNothing() {}\n' >"$wt/a/a_test.go"
gw commit -q -am "drop d, touch a test"
set +e
output=$(RUN_DIR="$wt" run)
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "the worktree run failed (exit $rc): $output; calls: $(calls_text)"
recorded "go build ./a" || fail "worktree: go build did not get ./a alone: $(calls_text)"
recorded "go test -race -count=1 ./a" || fail "worktree: the deleted package d was not dropped: $(calls_text)"
[[ $output == *"no frontend source changed"* ]] || fail "worktree: unexpected frontend step: $output"
if command grep -q "^npx " "$calls" 2>/dev/null; then fail "worktree: npx was called without frontend changes: $(calls_text)"; fi

printf '\nrequire example.com/y v1.0.0\n' >>"$wt/go.mod"
set +e
output=$(RUN_DIR="$wt" run)
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "the go.mod run failed (exit $rc): $output"
[[ $output == *"every Go package counts as touched"* ]] || fail "a go.mod change was not widened: $output"
recorded "go build example.com/x/a" || fail "go.mod: build list did not come from go list: $(calls_text)"
recorded "go vet example.com/x/a example.com/x/b" || fail "go.mod: vet list did not come from go list: $(calls_text)"
# A go list that fails must not pass for "nothing to run".
set +e
output=$(RUN_DIR="$wt" run CT_FAIL_LIST=1)
rc=$?
set -e
if [ "$rc" -ne 2 ] || [[ $output != *"go list ./... failed"* ]] || [[ $output != *"shim refuses to list"* ]] || [[ $output == *"nothing to run"* ]]; then
  fail "a failing go list after a go.mod change did not give exit 2 with its message (exit $rc): $output"
fi
if recorded "go build example.com/x/a" || command grep -q '^go vet\|^go test' "$calls" 2>/dev/null; then
  fail "go.mod: build, vet or test ran although go list failed: $(calls_text)"
fi
gw checkout -q -- go.mod

printf 'export const q = 1;\n' >"$wt/gocrm-ui/src/q.ts"
set +e
output=$(RUN_DIR="$wt" run)
rc=$?
set -e
if [ "$rc" -ne 2 ] || [[ $output != *"has no vitest"* ]]; then
  fail "a worktree without node_modules did not refuse the Vitest step with exit 2 (exit $rc): $output"
fi
if command grep -q "^npx " "$calls" 2>/dev/null; then fail "npx ran without node_modules: $(calls_text)"; fi

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "check-touched self-test passed: a clean branch runs nothing; committed, uncommitted and untracked Go and frontend changes map to build (sources only), vet, test -race and vitest related; VERIFY_JOBS reaches GOFLAGS and --maxWorkers; failures set the exit status and stop the Go steps; it works from a worktree, drops deleted packages, widens on go.mod and refuses a missing node_modules."
