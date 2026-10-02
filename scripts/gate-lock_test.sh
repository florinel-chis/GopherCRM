#!/usr/bin/env bash
# Self-test for scripts/gate-lock.sh, on a private lock directory so that it
# never touches the real gate lock. It proves:
#
# 1. a second caller waits until the holder has finished, says who it is
#    waiting for, and the command's exit status comes back unchanged;
# 2. a stale lock (holder pid gone, or no holder recorded within the grace
#    period) is removed and retaken, and two waiters that find the same stale
#    lock never both run;
# 3. GATE_LOCK=0 and CI=true run the command without waiting, while
#    GATE_LOCK=1 under CI=true waits;
# 4. a nested invocation under the same lock (GATE_LOCK_HELD) does not
#    deadlock and takes no second lock;
# 5. waiting beyond GATE_LOCK_TIMEOUT exits 124 without running the command;
# 6. a dry run of make (n in MAKEFLAGS) takes no lock;
# 7. SIGTERM to the wrapper stops the command and releases the lock, and INT
#    and TERM reach a trap inside the command.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gate="$here/gate-lock.sh"
failures=0
fail() {
  echo "gate-lock_test: $1" >&2
  failures=$((failures + 1))
}

tmp=$(mktemp -d)
lock="$tmp/gate.lock"
log="$tmp/log"
trap 'rm -rf "$tmp"' EXIT

# Every call gets the private lock, fast polling and a short timeout; the
# caller's own CI, GATE_LOCK*, and make variables must not leak in. Extra
# NAME=VALUE arguments before the script path override these defaults.
run_gate() {
  env -u CI -u GATE_LOCK -u GATE_LOCK_HELD -u MAKEFLAGS -u MFLAGS \
    GATE_LOCK_DIR="$lock" GATE_LOCK_POLL=0.1 GATE_LOCK_PROGRESS=1 GATE_LOCK_TIMEOUT=10 "$@"
}
wait_for_holder() {
  local i=0
  while [ ! -s "$lock/holder" ] && [ "$i" -lt 100 ]; do
    sleep 0.1
    i=$((i + 1))
  done
  [ -s "$lock/holder" ]
}
seconds() { date +%s; }
wait_up_to() { # seconds pid: waits for the process to exit; 1 when it is still running
  local i=0 limit=$(($1 * 10))
  while kill -0 "$2" 2>/dev/null && [ "$i" -lt "$limit" ]; do
    sleep 0.1
    i=$((i + 1))
  done
  ! kill -0 "$2" 2>/dev/null
}

# --- 1. exclusion, order, holder report, exit status --------------------------
rm -f "$log"
run_gate "$gate" holder-a sh -c "echo a-start >>'$log'; sleep 2.5; echo a-end >>'$log'" 2>"$tmp/a.err" &
a_pid=$!
wait_for_holder || fail "holder-a never wrote its holder file"
if ! command grep -q '^label=holder-a$' "$lock/holder" || ! command grep -q '^pid=[0-9][0-9]*$' "$lock/holder" || ! command grep -q '^start=' "$lock/holder"; then
  fail "holder file lacks pid, start or label: $(cat "$lock/holder")"
fi
set +e
run_gate "$gate" waiter-b sh -c "echo b >>'$log'; exit 3" 2>"$tmp/b.err"
b_rc=$?
set -e
wait "$a_pid" || fail "holder-a exited non-zero"
if [ "$(tr '\n' ' ' <"$log")" != "a-start a-end b " ]; then
  fail "waiter-b did not wait for holder-a: log is '$(tr '\n' ' ' <"$log")'"
fi
[ "$b_rc" -eq 3 ] || fail "waiter-b returned $b_rc, expected the command's 3"
if ! command grep -q 'waiter-b is waiting for the gate lock held by pid [0-9]* (holder-a, started' "$tmp/b.err"; then
  fail "waiter-b did not name the holder while waiting: $(cat "$tmp/b.err")"
fi
if [ "$(command grep -c 'is waiting for the gate lock' "$tmp/b.err")" -lt 2 ]; then
  fail "waiter-b printed fewer than two progress lines over a 2.5s wait at GATE_LOCK_PROGRESS=1: $(cat "$tmp/b.err")"
fi
command grep -q 'waiter-b acquired the gate lock after' "$tmp/b.err" || fail "waiter-b did not report the acquisition: $(cat "$tmp/b.err")"
command grep -q 'waiter-b released the gate lock (exit 3' "$tmp/b.err" || fail "waiter-b did not report the release with exit 3: $(cat "$tmp/b.err")"
[ ! -e "$lock" ] || fail "lock directory still exists after both runs"

# --- 2. stale locks ------------------------------------------------------------
sh -c 'exit 0' &
dead_pid=$!
wait "$dead_pid"
mkdir "$lock"
printf 'pid=%s\nstart=earlier\nlabel=crashed\ncwd=/nowhere\ncmd=sleep\n' "$dead_pid" >"$lock/holder"
set +e
run_gate GATE_LOCK_TIMEOUT=5 "$gate" retaker true 2>"$tmp/stale.err"
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "stale lock with a dead pid was not retaken (exit $rc): $(cat "$tmp/stale.err")"
command grep -q "removed a stale gate lock (pid $dead_pid is gone; it ran crashed)" "$tmp/stale.err" || fail "stale lock removal was not reported: $(cat "$tmp/stale.err")"
[ ! -e "$lock" ] || fail "lock directory still exists after the stale retake"

mkdir "$lock" # no holder file at all
before=$(seconds)
set +e
run_gate GATE_LOCK_TIMEOUT=5 GATE_LOCK_GRACE=1 "$gate" retaker true 2>"$tmp/empty.err"
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "lock directory without a holder was not retaken (exit $rc): $(cat "$tmp/empty.err")"
command grep -q 'removed a stale gate lock (no holder recorded for 1s)' "$tmp/empty.err" || fail "empty lock removal was not reported: $(cat "$tmp/empty.err")"
[ $(($(seconds) - before)) -le 4 ] || fail "empty lock took $(($(seconds) - before))s to retake at GATE_LOCK_GRACE=1"
[ ! -e "$lock" ] || fail "lock directory still exists after the empty retake"

# --- 2b. a stale lock that cannot be removed ---------------------------------------
# The holder is alive when the waiter starts and dies once the lock directory
# is read-only: the waiter still reads the holder file and judges its pid
# dead, but cannot rename it. It must say so, keep polling at GATE_LOCK_POLL
# and give up at the timeout, instead of retrying without a pause. Root is
# not stopped by directory permissions; there the holder file gets an
# immutable flag, or the case is skipped when the file system has none.
immobilise() {
  if [ "$(id -u)" != 0 ]; then
    chmod a-w "$lock"
  else
    chattr +i "$lock/holder" 2>/dev/null || chflags uchg "$lock/holder" 2>/dev/null
  fi
}
mobilise() {
  chmod u+w "$lock" 2>/dev/null || true
  chattr -i "$lock/holder" 2>/dev/null || chflags nouchg "$lock/holder" 2>/dev/null || true
}
sleep 30 &
sleeper=$!
mkdir "$lock"
printf 'pid=%s\nstart=earlier\nlabel=doomed\ncwd=/nowhere\ncmd=sleep\n' "$sleeper" >"$lock/holder"
rm -f "$tmp/stuck.out"
before=$(seconds)
run_gate GATE_LOCK_TIMEOUT=2 "$gate" stuck sh -c "echo ran >'$tmp/stuck.out'" 2>"$tmp/stuck.err" &
stuck_pid=$!
i=0
while ! command grep -q 'stuck is waiting' "$tmp/stuck.err" 2>/dev/null && [ "$i" -lt 50 ]; do
  sleep 0.1
  i=$((i + 1))
done
if immobilise; then
  kill "$sleeper"
  wait "$sleeper" 2>/dev/null || true
  if wait_up_to 6 "$stuck_pid"; then
    set +e
    wait "$stuck_pid"
    stuck_rc=$?
    set -e
    [ "$stuck_rc" -eq 124 ] || fail "a stale lock that cannot be removed gave exit $stuck_rc, expected 124: $(cat "$tmp/stuck.err")"
    [ $(($(seconds) - before)) -le 4 ] || fail "the waiter took $(($(seconds) - before))s to give up at GATE_LOCK_TIMEOUT=2 on an immovable stale lock"
  else
    pkill -KILL -P "$stuck_pid" 2>/dev/null || true
    kill -KILL "$stuck_pid" 2>/dev/null || true
    fail "the waiter did not give up within 6s on a stale lock it cannot remove (GATE_LOCK_TIMEOUT=2): $(head -c 2000 "$tmp/stuck.err")"
  fi
  command grep -q "stuck cannot remove stale lock held by pid $sleeper (doomed," "$tmp/stuck.err" || fail "the immovable stale lock was not reported: $(head -c 2000 "$tmp/stuck.err")"
  command grep -q 'stuck gave up after' "$tmp/stuck.err" || fail "no timeout line for the immovable stale lock: $(head -c 2000 "$tmp/stuck.err")"
  [ ! -e "$tmp/stuck.out" ] || fail "the command ran although the stale lock could not be removed"
  [ "$(command grep -c . "$tmp/stuck.err")" -le 30 ] || fail "the waiter printed $(command grep -c . "$tmp/stuck.err") lines over a 2s wait on an immovable stale lock: $(head -c 2000 "$tmp/stuck.err")"
else
  echo "gate-lock_test: skipping the immovable stale lock case: cannot make the holder file immovable as root on this file system" >&2
  kill "$sleeper"
  wait "$sleeper" 2>/dev/null || true
  wait "$stuck_pid" 2>/dev/null || true
fi
mobilise
rm -rf "$lock"

# --- 2c. two waiters find the same dead holder -----------------------------------
# slow's ps takes a second, so it judges the dead pid gone only after quick
# has removed the stale claim, taken the lock and started its command. slow
# must notice that what it would remove is quick's claim, leave it and wait:
# the two commands must not overlap, and only one stale removal may happen.
sh -c 'exit 0' &
dead_pid=$!
wait "$dead_pid"
mkdir "$lock"
printf 'pid=%s\nstart=earlier\nlabel=crashed\ncwd=/nowhere\ncmd=sleep\n' "$dead_pid" >"$lock/holder"
mkdir "$tmp/slowbin"
printf '#!/bin/sh\nsleep 1\nexec %s "$@"\n' "$(command -v ps)" >"$tmp/slowbin/ps"
chmod +x "$tmp/slowbin/ps"
rm -f "$log"
run_gate PATH="$tmp/slowbin:$PATH" "$gate" slow sh -c "echo slow-start >>'$log'; echo slow-end >>'$log'" 2>"$tmp/slow.err" &
slow_pid=$!
sleep 0.2
run_gate "$gate" quick sh -c "echo quick-start >>'$log'; sleep 1.5; echo quick-end >>'$log'" 2>"$tmp/quick.err" &
quick_pid=$!
sleep 0.4
quick_wrapper=$(sed -n 's/^pid=//p' "$lock/holder" 2>/dev/null || true)
set +e
wait "$slow_pid"
slow_rc=$?
wait "$quick_pid"
quick_rc=$?
set -e
[ "$slow_rc" -eq 0 ] && [ "$quick_rc" -eq 0 ] || fail "the racing waiters exited $slow_rc (slow) and $quick_rc (quick): $(cat "$tmp/slow.err" "$tmp/quick.err")"
if [ "$(tr '\n' ' ' <"$log")" != "quick-start quick-end slow-start slow-end " ]; then
  fail "the two commands overlapped after a shared stale lock: log is '$(tr '\n' ' ' <"$log")'; slow: $(cat "$tmp/slow.err"); quick: $(cat "$tmp/quick.err")"
fi
[ "$(cat "$tmp/slow.err" "$tmp/quick.err" | command grep -c 'removed a stale gate lock')" -eq 1 ] || fail "expected exactly one stale removal across both waiters: $(cat "$tmp/slow.err" "$tmp/quick.err")"
command grep -q "slow found the stale gate lock retaken by pid $quick_wrapper meanwhile" "$tmp/slow.err" || fail "slow did not report quick's takeover: $(cat "$tmp/slow.err")"
command grep -q "slow is waiting for the gate lock held by pid $quick_wrapper (quick," "$tmp/slow.err" || fail "slow did not wait for quick after the shared stale lock: $(cat "$tmp/slow.err")"
[ ! -e "$lock" ] || fail "lock directory still exists after the racing waiters"
[ -z "$(ls -d "$tmp"/gate.lock* 2>/dev/null)" ] || fail "lock directories left behind by the racing waiters: $(ls -d "$tmp"/gate.lock*)"

# --- 3, 5, 6. bypasses, timeout and dry run against a live holder --------------
# holder-c would sleep for a minute; it is terminated once the cases are done.
run_gate "$gate" holder-c sleep 60 2>/dev/null &
c_pid=$!
wait_for_holder || fail "holder-c never wrote its holder file"
c_wrapper_pid=$(sed -n 's/^pid=//p' "$lock/holder")
for bypass in "GATE_LOCK=0" "CI=true" "CI=1" "MAKEFLAGS=n" "MAKEFLAGS=nk" "MAKEFLAGS= --no-print-directory -n" "MAKEFLAGS=n -- VERIFY_JOBS=2" "MAKEFLAGS=n -- SPECS=e2e/tests/login.spec.ts"; do
  before=$(seconds)
  set +e
  output=$(run_gate "$bypass" "$gate" bypasser sh -c 'echo ran; echo "held=${GATE_LOCK_HELD:-}"' 2>&1)
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [ "$output" != $'ran\nheld=' ] || [ $(($(seconds) - before)) -gt 1 ]; then
    fail "'$bypass' did not run the command directly (exit $rc, $(($(seconds) - before))s): $output"
  fi
  command grep -q '^label=holder-c$' "$lock/holder" || fail "'$bypass' disturbed the live lock"
done
# Entries hold one or two NAME=VALUE assignments separated by "|", because a
# value may itself contain spaces.
# "SPECS=…login…" and "GO_BUILD_PKGS=…" are what make 3.81 puts in MAKEFLAGS
# for a plain `make e2e SPECS=…`: variable overrides, not an -n.
for waits in "CI=true|GATE_LOCK=1" "MAKEFLAGS=k" "MAKEFLAGS=-- VERIFY_JOBS=n" "MAKEFLAGS= --no-print-directory -k" "MAKEFLAGS=SPECS=e2e/tests/login.spec.ts" "MAKEFLAGS=GO_BUILD_PKGS=./internal/utils" "MAKEFLAGS= -- GO_PKGS=./internal/utils"; do
  IFS='|' read -r first_var second_var <<<"$waits"
  before=$(seconds)
  set +e
  output=$(run_gate "$first_var" ${second_var:+"$second_var"} GATE_LOCK_TIMEOUT=1 "$gate" timed-out sh -c 'echo ran' 2>&1)
  rc=$?
  set -e
  if [ "$rc" -ne 124 ] || [[ $output == *ran* ]] || [[ $output != *"timed-out gave up after"*"(GATE_LOCK_TIMEOUT=1)"*"held by pid $(sed -n 's/^pid=//p' "$lock/holder") (holder-c,"* ]]; then
    fail "'$waits' did not time out with 124 naming holder-c (exit $rc): $output"
  fi
  [ $(($(seconds) - before)) -le 3 ] || fail "'$waits' took $(($(seconds) - before))s to time out at GATE_LOCK_TIMEOUT=1"
done
set +e
output=$(run_gate GATE_LOCK=2 "$gate" bad true 2>&1)
rc=$?
set -e
[ "$rc" -eq 2 ] && [[ $output == *"GATE_LOCK must be 0 or 1"* ]] || fail "GATE_LOCK=2 was accepted (exit $rc): $output"
command grep -q '^label=holder-c$' "$lock/holder" || fail "holder-c lost the lock during the cases above"
kill -TERM "$c_wrapper_pid"
set +e
wait "$c_pid"
c_rc=$?
set -e
[ "$c_rc" -eq 143 ] || fail "holder-c exited $c_rc after SIGTERM, expected 143"
[ ! -e "$lock" ] || fail "lock directory still exists after holder-c was terminated"

# --- 4. re-entrancy -------------------------------------------------------------
rm -f "$log"
set +e
output=$(run_gate GATE_LOCK_TIMEOUT=3 "$gate" outer sh -c "'$gate' inner sh -c 'echo inner-ran >>\"$log\"; cat \"$lock/holder\"'" 2>&1)
rc=$?
set -e
[ "$rc" -eq 0 ] || fail "nested invocation deadlocked or failed (exit $rc): $output"
[ "$(cat "$log" 2>/dev/null)" = inner-ran ] || fail "nested command did not run: $output"
[[ $output == *"label=outer"* ]] || fail "the outer invocation does not own the lock during the nested one: $output"
[ "$(command grep -c 'holds the gate lock' <<<"$output")" -eq 1 ] || fail "expected exactly one acquisition for outer and inner: $output"
[[ $output != *"inner is waiting"* ]] || fail "the nested invocation waited for its parent: $output"
[ ! -e "$lock" ] || fail "lock directory still exists after the nested run"

# --- 7. SIGTERM to the wrapper ----------------------------------------------------
run_gate "$gate" holder-d sh -c "echo \$\$ >'$tmp/child.pid'; exec sleep 30" 2>"$tmp/d.err" &
d_pid=$!
wait_for_holder || fail "holder-d never wrote its holder file"
i=0
while [ ! -s "$tmp/child.pid" ] && [ "$i" -lt 50 ]; do
  sleep 0.1
  i=$((i + 1))
done
child_pid=$(cat "$tmp/child.pid" 2>/dev/null || true)
# The signal goes to the wrapper itself, whose pid the holder file records
# ($d_pid is the test's own background subshell around it).
kill -TERM "$(sed -n 's/^pid=//p' "$lock/holder")"
set +e
wait "$d_pid"
d_rc=$?
set -e
[ "$d_rc" -eq 143 ] || fail "wrapper exited $d_rc after SIGTERM, expected 143 (the command's)"
sleep 0.2
if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
  kill -KILL "$child_pid" 2>/dev/null || true
  fail "the command (pid $child_pid) survived SIGTERM to the wrapper"
fi
[ -z "$child_pid" ] && fail "the command never recorded its pid"
[ ! -e "$lock" ] || fail "lock directory still exists after SIGTERM"

# --- 7b. INT and TERM reach a trap in the command ---------------------------------
# A shell step that traps the signal (scripts/e2e/run.sh stops its backend
# from an INT trap) must see it: the trap writes a marker and exits with the
# signal's conventional status, which the wrapper reports. The INT case failed
# until the command got a process group of its own: bash starts a background
# job with SIGINT ignored, and the command inherited that across exec.
for case in INT:130 TERM:143; do
  sig=${case%%:*}
  code=${case#*:}
  rm -f "$tmp/child.pid" "$tmp/trap.$sig"
  # Started under job control, as a terminal or make starts it: a background
  # job of this script without job control would itself have SIGINT ignored
  # and could not trap it, whatever it does for its command.
  set -m
  run_gate "$gate" holder-e sh -c "trap 'echo trapped >\"$tmp/trap.$sig\"; exit $code' $sig; echo \$\$ >'$tmp/child.pid'; while :; do sleep 0.1; done" 2>"$tmp/e.err" &
  e_pid=$!
  set +m
  wait_for_holder || fail "holder-e never wrote its holder file ($sig)"
  i=0
  while [ ! -s "$tmp/child.pid" ] && [ "$i" -lt 50 ]; do
    sleep 0.1
    i=$((i + 1))
  done
  child_pid=$(cat "$tmp/child.pid" 2>/dev/null || true)
  wrapper_pid=$(sed -n 's/^pid=//p' "$lock/holder")
  kill "-$sig" "$wrapper_pid"
  if ! wait_up_to 5 "$e_pid"; then
    fail "the wrapper (pid $wrapper_pid) did not finish within 5s of SIG$sig; the command never saw the signal"
    kill -KILL "$child_pid" "$wrapper_pid" 2>/dev/null || true
    pkill -KILL -P "$child_pid" 2>/dev/null || true
  fi
  set +e
  wait "$e_pid"
  e_rc=$?
  set -e
  [ "$e_rc" -eq "$code" ] || fail "wrapper exited $e_rc after SIG$sig, expected the trap's $code"
  [ -s "$tmp/trap.$sig" ] || fail "the command's $sig trap did not run"
  if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
    kill -KILL "$child_pid" 2>/dev/null || true
    fail "the command (pid $child_pid) survived SIG$sig to the wrapper"
  fi
  command grep -q "holder-e released the gate lock (exit $code" "$tmp/e.err" || fail "the release line after SIG$sig is missing or wrong: $(cat "$tmp/e.err")"
  [ ! -e "$lock" ] || fail "lock directory still exists after SIG$sig"
  rm -rf "$lock"
done

# --- usage ----------------------------------------------------------------------
for args in "" "only-label" "-x true"; do
  set +e
  # shellcheck disable=SC2086
  run_gate "$gate" $args >/dev/null 2>&1
  rc=$?
  set -e
  [ "$rc" -eq 2 ] || fail "arguments '$args' were accepted (exit $rc)"
done
set +e
output=$(run_gate GATE_LOCK_TIMEOUT=soon "$gate" x true 2>&1)
rc=$?
set -e
[ "$rc" -eq 2 ] && [[ $output == *"GATE_LOCK_TIMEOUT must be a whole number"* ]] || fail "GATE_LOCK_TIMEOUT=soon was accepted (exit $rc): $output"

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "gate-lock self-test passed: a second caller waits and names the holder; stale locks (dead pid, no holder) are retaken and two waiters over one stale lock never both run; GATE_LOCK=0, CI=true and make -n skip the lock while GATE_LOCK=1 insists; a nested call under the same lock does not wait; the timeout exits 124; SIGTERM stops the command and releases the lock; INT and TERM reach a trap inside the command."
