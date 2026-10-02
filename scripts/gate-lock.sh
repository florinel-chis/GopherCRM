#!/usr/bin/env bash
# Machine-wide mutual exclusion for the full gates: make verify, verify-backend,
# verify-frontend and e2e. Two gates on one machine slow each other down until
# tests time out for load alone, so every gate runs under this lock, across
# terminals and sessions.
#
#   scripts/gate-lock.sh <label> <command> [args...]
#
# The lock is a directory: mkdir creates it or fails, atomically, on any file
# system and without flock(1), which macOS lacks. The file `holder` inside
# records the owner's pid, start time, label, directory and command, so a
# waiter can say who it is waiting for.
#
# Environment:
#   GATE_LOCK          0 runs the command without the lock. Meant for CI, where
#                      every job has a machine of its own; CI=true is treated
#                      the same unless GATE_LOCK=1 insists on the lock.
#   GATE_LOCK_DIR      the lock directory (default /tmp/gophercrm-gate.lock).
#                      A fixed path on purpose: TMPDIR is per user and per login
#                      session on macOS, which would give two sessions two locks.
#   GATE_LOCK_TIMEOUT  seconds to wait for the lock (default 1800). On expiry
#                      the command does not run and the exit status is 124.
#   GATE_LOCK_PROGRESS seconds between progress lines while waiting (default 30)
#   GATE_LOCK_POLL     seconds between attempts (default 1; fractions allowed)
#   GATE_LOCK_GRACE    seconds a lock directory may exist without a holder file
#                      before it counts as stale (default 10; the self-test
#                      shortens it)
#   GATE_LOCK_HELD     set by this script for the command it runs. A nested
#                      invocation that finds its own lock directory here runs
#                      the command directly instead of waiting for its parent;
#                      this is how `make verify` holds one lock while the
#                      verify-* targets it runs take none.
#
# A lock whose holder pid is gone is stale (the holder was killed with SIGKILL,
# or the machine rebooted); it is removed and retaken. A dry run of make (`-n`
# in MAKEFLAGS) takes no lock either: it runs nothing that needs one and must
# never wait behind a real gate.
#
# Signals: INT, TERM and HUP are forwarded to the command, whose exit status
# the script returns; the lock is released on exit however it ends.
set -uo pipefail

usage() {
  echo "usage: scripts/gate-lock.sh <label> <command> [args...]" >&2
  exit 2
}
[ $# -ge 2 ] || usage
label=$1
shift
case "$label" in -* | "") usage ;; esac

lock_dir=${GATE_LOCK_DIR:-/tmp/gophercrm-gate.lock}
timeout=${GATE_LOCK_TIMEOUT:-1800}
progress=${GATE_LOCK_PROGRESS:-30}
poll=${GATE_LOCK_POLL:-1}
grace=${GATE_LOCK_GRACE:-10}
for setting in "GATE_LOCK_TIMEOUT=$timeout" "GATE_LOCK_PROGRESS=$progress" "GATE_LOCK_GRACE=$grace"; do
  case "${setting#*=}" in
    '' | *[!0-9]*)
      echo "gate-lock: ${setting%%=*} must be a whole number of seconds, got '${setting#*=}'" >&2
      exit 2
      ;;
  esac
done
case "$poll" in
  '' | *[!0-9.]* | . | *.*.*)
    echo "gate-lock: GATE_LOCK_POLL must be a number of seconds, got '$poll'" >&2
    exit 2
    ;;
esac

# `make -n` puts n among the short flags of MAKEFLAGS: the first word when it
# does not start with a dash ("n", "nk"), or a dashed word when long options
# come first (" --no-print-directory -n"). Words after "--" are variable
# overrides, not flags.
make_dry_run() {
  local word first=1
  for word in ${MAKEFLAGS:-}; do
    case "$word" in
      --) return 1 ;;
      --*) ;;
      -*) [[ $word == *n* ]] && return 0 ;;
      *) [ "$first" = 1 ] && [[ $word == *n* ]] && return 0 ;;
    esac
    first=0
  done
  return 1
}

case "${GATE_LOCK:-}" in
  0) exec "$@" ;;
  1) ;;
  '') case "${CI:-}" in true | 1) exec "$@" ;; esac ;;
  *)
    echo "gate-lock: GATE_LOCK must be 0 or 1, got '$GATE_LOCK'" >&2
    exit 2
    ;;
esac
if [ "${GATE_LOCK_HELD:-}" = "$lock_dir" ] || make_dry_run; then
  exec "$@"
fi

parent_dir=$(dirname "$lock_dir")
mkdir -p "$parent_dir" 2>/dev/null
if ! [ -d "$parent_dir" ] || ! [ -w "$parent_dir" ]; then
  echo "gate-lock: cannot create a lock in $parent_dir (not a writable directory); set GATE_LOCK_DIR" >&2
  exit 2
fi

holder_file=$lock_dir/holder
now() { date +%s; }
holder_field() { sed -n "s/^$1=//p" "$holder_file" 2>/dev/null | head -n 1; }
holder_alive() { kill -0 "$1" 2>/dev/null || ps -p "$1" >/dev/null 2>&1; }
holder_summary() {
  local pid
  pid=$(holder_field pid)
  echo "pid ${pid:-unknown} ($(holder_field label), started $(holder_field start), in $(holder_field cwd))"
}
# Renaming first makes the removal exclusive: of several waiters that found the
# same stale lock, one mv succeeds and the others fall through to mkdir, so
# nobody removes a lock a competitor has just taken.
retake() {
  local stale="$lock_dir.stale.$$"
  if mv "$lock_dir" "$stale" 2>/dev/null; then
    echo "gate-lock: $label removed a stale gate lock ($1)" >&2
    rm -rf "$stale"
  fi
}

start=$(now)
last_report=$start
announced=
missing_since=
while ! mkdir "$lock_dir" 2>/dev/null; do
  pid=$(holder_field pid)
  if [ -n "$pid" ]; then
    missing_since=
    if ! holder_alive "$pid"; then
      retake "pid $pid is gone; it ran $(holder_field label)"
      continue
    fi
  else
    # A lock directory without a holder file was created a moment ago and is
    # about to be written, or its owner died between the two steps.
    [ -n "$missing_since" ] || missing_since=$(now)
    if [ $(($(now) - missing_since)) -ge "$grace" ]; then
      retake "no holder recorded for ${grace}s"
      continue
    fi
  fi
  elapsed=$(($(now) - start))
  if [ "$elapsed" -ge "$timeout" ]; then
    echo "gate-lock: $label gave up after ${elapsed}s (GATE_LOCK_TIMEOUT=$timeout): $lock_dir is held by $(holder_summary)" >&2
    exit 124
  fi
  if [ -z "$announced" ] || [ $(($(now) - last_report)) -ge "$progress" ]; then
    echo "gate-lock: $label is waiting for the gate lock held by $(holder_summary); ${elapsed}s of ${timeout}s" >&2
    announced=1
    last_report=$(now)
  fi
  sleep "$poll"
done

printf 'pid=%s\nstart=%s\nlabel=%s\ncwd=%s\ncmd=%s\n' \
  "$$" "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$label" "$PWD" "$*" >"$holder_file"
if [ -n "$announced" ]; then
  echo "gate-lock: $label acquired the gate lock after $(($(now) - start))s" >&2
else
  echo "gate-lock: $label holds the gate lock ($lock_dir, pid $$)" >&2
fi

release() {
  if [ "$(holder_field pid)" = "$$" ]; then
    rm -rf "$lock_dir"
  fi
}
child=
forward() { [ -n "$child" ] && kill "-$1" "$child" 2>/dev/null; }
trap release EXIT
trap 'forward INT' INT
trap 'forward TERM' TERM
trap 'forward HUP' HUP

# The command runs as a background job so that a signal reaches the trap at
# once instead of after the command ends; the redirection keeps its stdin,
# which bash would otherwise replace with /dev/null for a background job.
export GATE_LOCK_HELD=$lock_dir
"$@" <&0 &
child=$!
wait "$child"
rc=$?
# A forwarded signal interrupts wait before the command has exited; wait again
# until it has, so that its own exit status is the one reported.
while kill -0 "$child" 2>/dev/null; do
  wait "$child"
  rc=$?
done
echo "gate-lock: $label released the gate lock (exit $rc after $(($(now) - start))s)" >&2
exit "$rc"
