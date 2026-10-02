#!/usr/bin/env bash
# The quick check for a branch in progress: build, vet and test with the race
# detector only the Go packages the branch touches, and run the Vitest files
# related to the frontend sources it touches. `make check-touched` runs it.
#
# "Touched" means changed between the merge base of HEAD and origin/main
# (CHECK_BASE overrides the ref) and the working tree: committed on the branch,
# staged, unstaged or untracked. Dependants of a changed package are not
# rebuilt; the full gate (make verify) covers them. A change to go.mod or
# go.sum touches every Go package.
#
# Works from a git worktree. The Vitest part needs node_modules in gocrm-ui of
# the tree it runs in. VERIFY_JOBS=N applies as in the Makefile: GOFLAGS gets
# -p=N and Vitest --maxWorkers=N.
#
# Exit status: 0 when every step passed or nothing had to run; 1 when a step
# failed; 2 when the checks could not run at all.
set -uo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "check-touched: not inside a git repository" >&2
  exit 2
}
cd "$repo_root" || exit 2
base_ref=${CHECK_BASE:-origin/main}
base=$(git merge-base HEAD "$base_ref" 2>/dev/null) || {
  echo "check-touched: no merge base of HEAD and $base_ref (run git fetch origin, or set CHECK_BASE)" >&2
  exit 2
}

changed=$( { git diff --name-only "$base" -- && git ls-files --others --exclude-standard; } | sort -u) || {
  echo "check-touched: git could not list the changed files" >&2
  exit 2
}

if [ -n "${VERIFY_JOBS:-}" ]; then
  case "$VERIFY_JOBS" in
    '' | 0* | *[!0-9]*)
      echo "check-touched: VERIFY_JOBS must be a positive integer, got '$VERIFY_JOBS'" >&2
      exit 2
      ;;
  esac
  existing=$(go env GOFLAGS 2>/dev/null || true)
  export GOFLAGS="${existing:+$existing }-p=$VERIFY_JOBS"
  vitest_jobs="--maxWorkers=$VERIFY_JOBS"
else
  vitest_jobs=
fi

# --- which Go packages and frontend files ----------------------------------------
go_dirs=
mod_changed=
ui_files=()
contains_line() { [[ $'\n'"$1"$'\n' == *$'\n'"$2"$'\n'* ]]; }
while IFS= read -r file; do
  [ -n "$file" ] || continue
  case "$file" in
    go.mod | go.sum) mod_changed=1 ;;
    gocrm-ui/src/*.ts | gocrm-ui/src/*.tsx)
      [ -f "$file" ] && ui_files+=("${file#gocrm-ui/}")
      ;;
    gocrm-ui/*) ;;
    *.go)
      dir=$(dirname "$file")
      contains_line "$go_dirs" "$dir" || go_dirs="${go_dirs:+$go_dirs$'\n'}$dir"
      ;;
  esac
done <<<"$changed"

go_pkgs=()
go_build_pkgs=()
if [ -n "$mod_changed" ]; then
  echo "check-touched: go.mod or go.sum changed; every Go package counts as touched"
  while IFS= read -r pkg; do
    [ -n "$pkg" ] && go_pkgs+=("$pkg")
  done < <(go list ./... 2>/dev/null | grep -v /gocrm-ui/)
  while IFS= read -r pkg; do
    [ -n "$pkg" ] && go_build_pkgs+=("$pkg")
  done < <(go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... 2>/dev/null | grep -v /gocrm-ui/)
else
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    has_go=
    has_source=
    for f in "$dir"/*.go; do
      [ -e "$f" ] || continue
      has_go=1
      case "$f" in *_test.go) ;; *) has_source=1 ;; esac
    done
    # A directory whose Go files were all deleted is no package any more.
    [ -n "$has_go" ] || continue
    pkg="./$dir"
    [ "$dir" = . ] && pkg=.
    go_pkgs+=("$pkg")
    # go build refuses a package named explicitly that has only test files.
    [ -n "$has_source" ] && go_build_pkgs+=("$pkg")
  done < <(printf '%s\n' "$go_dirs" | sort)
fi

echo "check-touched: base $(git rev-parse --short "$base") (merge base of HEAD and $base_ref), $(printf '%s\n' "$changed" | command grep -c .) changed path(s)"
if [ ${#go_pkgs[@]} -eq 0 ] && [ ${#ui_files[@]} -eq 0 ]; then
  echo "check-touched: no Go package and no frontend source changed; nothing to run"
  exit 0
fi

# --- run -------------------------------------------------------------------------
status=0
summary=
step() {
  local name=$1 rc
  shift
  echo "check-touched: $name: $*"
  "$@"
  rc=$?
  echo "check-touched: $name rc=$rc"
  summary="${summary:+$summary, }$name rc=$rc"
  [ "$rc" -eq 0 ] || status=1
  return "$rc"
}

if [ ${#go_pkgs[@]} -gt 0 ]; then
  echo "check-touched: Go packages: ${go_pkgs[*]}${GOFLAGS:+ (GOFLAGS=$GOFLAGS)}"
  go_ok=1
  if [ ${#go_build_pkgs[@]} -gt 0 ]; then
    step "go build" go build "${go_build_pkgs[@]}" || go_ok=
  else
    echo "check-touched: go build: only test-only packages changed; nothing to build"
  fi
  # vet and test only make sense on code that builds.
  if [ -n "$go_ok" ]; then
    step "go vet" go vet "${go_pkgs[@]}" &&
      step "go test" go test -race -count=1 "${go_pkgs[@]}"
  fi
else
  echo "check-touched: no Go package changed"
fi

if [ ${#ui_files[@]} -gt 0 ]; then
  echo "check-touched: frontend sources: ${ui_files[*]}"
  if [ ! -x gocrm-ui/node_modules/.bin/vitest ]; then
    echo "check-touched: gocrm-ui/node_modules has no vitest; run npm ci in gocrm-ui of this tree first" >&2
    summary="${summary:+$summary, }vitest not run"
    status=2
  else
    echo "check-touched: vitest related: npx vitest related ${ui_files[*]} --run --passWithNoTests ${vitest_jobs:+$vitest_jobs}"
    (cd gocrm-ui && npx vitest related "${ui_files[@]}" --run --passWithNoTests ${vitest_jobs:+"$vitest_jobs"})
    rc=$?
    echo "check-touched: vitest related rc=$rc"
    summary="${summary:+$summary, }vitest related rc=$rc"
    [ "$rc" -eq 0 ] || status=1
  fi
else
  echo "check-touched: no frontend source changed"
fi

echo "check-touched: ${summary:-nothing ran}; exit $status"
exit "$status"
