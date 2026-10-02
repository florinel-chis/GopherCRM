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
#                      every job has a machine of its own; CI=true (or 1) and
#                      GITHUB_ACTIONS=true are treated the same, with one line
#                      on stderr saying so, unless GATE_LOCK=1 insists on the
#                      lock.
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
# or the machine rebooted); it is removed and retaken. A stale lock this user
# cannot remove (taken under another account in a sticky /tmp, say) is
# reported and waited out until the timeout. A dry run of make (`-n`
# in MAKEFLAGS) takes no lock either: it runs nothing that needs one and must
# never wait behind a real gate.
#
# Signals: INT, TERM and HUP are forwarded to the command's process group,
# traps inside the command included, and the script returns the command's
# exit status; the lock is released on exit however it ends.
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
# come first (" --no-print-directory -n"). Command-line variable overrides
# follow the flags, after "--" when there are flags ("n -- SPECS=x") and bare
# when there are none ("SPECS=x" in make 3.81); a word with "=" ends the scan.
make_dry_run() {
  local word first=1
  for word in ${MAKEFLAGS:-}; do
    case "$word" in
      --) return 1 ;;
      --*) ;;
      *=*) return 1 ;;
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
  '')
    for ci_var in CI GITHUB_ACTIONS; do
      case "${!ci_var:-}" in
        true | 1)
          echo "gate-lock: $label runs without the gate lock ($ci_var=${!ci_var}; GATE_LOCK=1 would take it)" >&2
          exec "$@"
          ;;
      esac
    done
    ;;
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
command_line=$*
now() { date +%s; }
holder_field() { sed -n "s/^$1=//p" "${2:-$holder_file}" 2>/dev/null | head -n 1; }
holder_alive() { kill -0 "$1" 2>/dev/null || ps -p "$1" >/dev/null 2>&1; }
holder_summary() {
  local pid
  pid=$(holder_field pid)
  if [ -z "$pid" ]; then
    # Between mkdir and the holder file being written, or during a release.
    echo "a holder not yet recorded (the lock is being taken or released)"
  else
    echo "pid $pid ($(holder_field label), started $(holder_field start), in $(holder_field cwd))"
  fi
}
write_holder() {
  printf 'pid=%s\nstart=%s\nlabel=%s\ncwd=%s\ncmd=%s\n' \
    "$$" "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$label" "$PWD" "$command_line" >"$1"
}
# Taking over a lock directory that already exists: the holder file is written
# under a private name and linked to its final one. link(2) fails when the
# name is taken, so of several claimants exactly one succeeds and nobody
# overwrites a holder another process has just recorded.
claim() {
  local mine="$lock_dir/holder.$$"
  if write_holder "$mine" 2>/dev/null && ln "$mine" "$holder_file" 2>/dev/null; then
    rm -f "$mine"
    return 0
  fi
  rm -f "$mine"
  return 1
}
# A stale claim is removed in two steps that keep the lock directory in place,
# so that no mkdir can slip in meanwhile. The holder file is renamed first,
# which is exclusive (of several waiters that found the same dead pid, one
# rename succeeds), and the renamed file is then checked: if it still names
# the pid judged dead, the claim is gone and the caller may claim the lock; if
# it names another pid, a competitor took the lock over between this waiter's
# liveness check and its rename, the file is linked back under its name and
# this waiter keeps waiting. The only other way into a directory that has no
# holder file is a claim after GATE_LOCK_GRACE seconds without one, and the
# file is back long before that.
#
# Returns 0 when the stale claim is gone, 1 when there was nothing to remove
# (the holder released, or the file was a competitor's and is back in place)
# and 2 when the holder file cannot be renamed, with the reason in reap_error:
# a lock directory this user cannot write to, as with a lock taken under
# another account in a sticky /tmp, or an immutable flag.
reap_error=
reap() {
  local dead=$1 moved="$lock_dir/holder.dead.$$" pid
  if ! reap_error=$(mv "$holder_file" "$moved" 2>&1); then
    [ -e "$holder_file" ] || return 1
    return 2
  fi
  pid=$(holder_field pid "$moved")
  if [ "$pid" = "$dead" ] && ! { [ -n "$pid" ] && holder_alive "$pid"; }; then
    rm -f "$moved"
    return 0
  fi
  if ln "$moved" "$holder_file" 2>/dev/null; then
    echo "gate-lock: $label found the stale gate lock retaken by pid $pid meanwhile and keeps waiting" >&2
  else
    echo "gate-lock: $label moved the holder file of pid $pid aside and cannot put it back; that gate may run unlocked ($lock_dir)" >&2
  fi
  rm -f "$moved"
  return 1
}

start=$(now)
last_report=$start
announced=
missing_since=
stuck=
stuck_announced=
# Every pass through the loop that does not take the lock ends in the timeout
# check, the progress line and the sleep: a stale lock that cannot be removed
# is reported and waited out until the timeout, never retried without pause.
while :; do
  if mkdir "$lock_dir" 2>/dev/null; then
    write_holder "$holder_file"
    break
  fi
  stuck=
  outcome=1
  pid=$(holder_field pid)
  if [ -n "$pid" ]; then
    missing_since=
    if ! holder_alive "$pid"; then
      dead_label=$(holder_field label)
      reap "$pid"
      outcome=$?
      if [ "$outcome" -eq 0 ] && claim; then
        echo "gate-lock: $label removed a stale gate lock (pid $pid is gone; it ran $dead_label)" >&2
        break
      fi
    fi
  else
    # A lock directory without a holder file was created a moment ago and is
    # about to be written, or its owner died between the two steps.
    [ -n "$missing_since" ] || missing_since=$(now)
    if [ $(($(now) - missing_since)) -ge "$grace" ]; then
      # A holder file without a pid line is junk from outside and goes the
      # same way as a dead holder's.
      outcome=0
      if [ -e "$holder_file" ]; then
        reap ""
        outcome=$?
      fi
      if [ "$outcome" -eq 0 ] && claim; then
        echo "gate-lock: $label removed a stale gate lock (no holder recorded for ${grace}s)" >&2
        break
      fi
    fi
  fi
  if [ "$outcome" -eq 2 ]; then
    stuck="cannot remove stale lock held by $(holder_summary): $reap_error"
  else
    stuck_announced=
  fi
  elapsed=$(($(now) - start))
  if [ "$elapsed" -ge "$timeout" ]; then
    echo "gate-lock: $label gave up after ${elapsed}s (GATE_LOCK_TIMEOUT=$timeout): ${stuck:-$lock_dir is held by $(holder_summary)}" >&2
    exit 124
  fi
  if [ -z "$announced" ] || [ $(($(now) - last_report)) -ge "$progress" ] || { [ -n "$stuck" ] && [ -z "$stuck_announced" ]; }; then
    if [ -n "$stuck" ]; then
      echo "gate-lock: $label $stuck; ${elapsed}s of ${timeout}s" >&2
      stuck_announced=1
    else
      echo "gate-lock: $label is waiting for the gate lock held by $(holder_summary); ${elapsed}s of ${timeout}s" >&2
    fi
    announced=1
    last_report=$(now)
  fi
  sleep "$poll"
done

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
forward() { [ -n "$child" ] && kill "-$1" -- "-$child" 2>/dev/null; }
trap release EXIT
trap 'forward INT' INT
trap 'forward TERM' TERM
trap 'forward HUP' HUP

# The command runs as a background job so that a signal reaches the trap at
# once instead of after the command ends. It is started under job control
# (set -m) so that it gets a process group of its own with the default signal
# dispositions: without job control bash starts a background job with SIGINT
# and SIGQUIT ignored, the command inherits that across exec, and a forwarded
# INT would never arrive. Signals are forwarded to the whole group, so the
# helpers a command starts get them too. Job control goes off again right
# after the start, so bash prints no job status lines; the redirection keeps
# the command's stdin explicit (bash leaves it alone under job control and
# would replace it with /dev/null otherwise). A command that reads from the
# terminal would be stopped (SIGTTIN) in its own group; the gates do not.
export GATE_LOCK_HELD=$lock_dir
set -m
"$@" <&0 &
child=$!
set +m
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
