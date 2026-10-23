# Runner recovery and resource operations

Use these commands with the same private configuration as the original execution. Reports
do not resume work, publish messages or turn incomplete evidence into acceptance. Version
0.8.0 adds these operations without changing frozen validation policies.

## Inspect an interrupted execution

```sh
gophercrm-runner handoff --config /absolute/private/config.json --issue 103
gophercrm-runner handoff --config /absolute/private/config.json --issue 103 --markdown
gophercrm-runner report --config /absolute/private/config.json --issue 103 --markdown
gophercrm-runner report-pr --config /absolute/private/config.json --pr 123 --markdown
```

Without an issue/PR, `handoff` reads the active marker. JSON includes the candidate and
checkout state, installed module identity, worker observation, heartbeat, current check,
receipt integrity, last resource observations, budgets and next action. Markdown provides
a shorter restart/completion summary using the same report data. Saved failure history stays
visible but does not become an active stop reason after a terminal outcome.

Worker observations compare the PID's process-start identity, rather than assuming that a
stored PID still identifies the worker. Unknown or inaccessible processes are reported as
not observed; that is not permission to kill a PID. A heartbeat is written every five seconds
while the detached worker is alive. Check its timestamp and identity before starting another
worker. Foreground executions still have the existing execution lock.

Receipt integrity is not approval or a promise of reuse. Resume still verifies source, base,
policy, contract, commands, original validation checkout and exact named-test coverage. A
changed candidate needs fresh checks and independent review. Missing or changed evidence
follows the existing validation-restart procedure.

## Extend an inspected batch budget

```sh
gophercrm-runner extend-batch-budget --config /absolute/private/config.json \
  --batch original-batch --seconds 7200 --calls 2 \
  --reason 'Inspected the failed run and approved another bounded attempt'
gophercrm-runner start-batch --config /absolute/private/config.json \
  --batch original-batch --resume
```

This explicitly adds to the original deadlines and call ceilings of both the batch and its
current child allocation. It preserves the immutable plan, spent calls, reserved repair round,
source and receipts. An extension does not resume execution. Adding two hours to an execution
that expired yesterday still leaves it expired; choose the increment after inspecting the
persisted deadline. There is no automatic extension or refund for downtime.

The runner requires the original owner and policy, a nonterminal child, no unresolved provider
invocation and the exclusive execution lock. A durable intent records both journal images
before either changes. After interruption, the next operation under that batch's lock completes
the update only if each journal still matches its recorded before/after image. Unexpected edits
stop recovery. Never repair the JSON by hand or extend only a child of an owned batch.

This command does not refresh a changed main, task contract or policy. Use the existing
[batch disposition procedure](BATCHES.md#recovery-and-limits) when those inputs changed.
`recover-batch --processes-stopped` remains the separate acknowledgement for an inspected
orphaned provider invocation.

## Observe resource use and enforce deadlines

Heavy runner commands (`make verify*`, `make e2e`, builds, Go and npm commands) acquire one
shared per-user machine lock. Another state directory cannot start overlapping heavy work;
it stops with an environment failure rather than spending a code-repair call. Dependency
cleanup takes the same lock. Independent shell commands outside the runner are not governed
by this lock. Go/npm/npx version probes do not acquire it or replace resource observations.

Private `logs/*.resources.json` files retain the first/latest observation, a tail of at most
60 samples, minimum free disk and maximum observed RSS summed across owned processes.
Samples are taken at command boundaries and approximately every 15 seconds. They contain
process IDs, executable basenames and resource counts, not command arguments or credentials.
RSS can double-count shared pages. Linux available memory and macOS free/inactive/compressed
pages are observations, not interchangeable measures of memory pressure. Unsupported fields
remain unavailable.

The configured free-disk floor is enforced during long checks as well as admission. Low disk
stops the command group and preserves diagnostics. An unavailable disk observation is recorded
and stops validation because the floor cannot be enforced. Missing load or memory observations
remain unknown. Memory measurements currently inform
operator decisions; they do not impose an uncalibrated memory limit. Use historical growth
to set `min_free_disk_mb` before preparing a new batch; existing policies remain frozen.

Command supervision rechecks the absolute execution deadline at most every two seconds of
scheduled execution, including after host sleep. A monotonic limit also bounds each command
if wall time moves backward. A process cannot be supervised while the host is suspended;
after resumption, expired work is terminated rather than accepted late. Timeout/cancellation
terminates the owned process group. A validation command timeout stops for diagnosis instead
of automatically asking a model to change source.

## Preview validation and inspect historical costs

```sh
gophercrm-runner validation-preview --config /absolute/private/config.json \
  --manifest /absolute/private/task.json
gophercrm-runner validation-preview --config /absolute/private/config.json \
  --base FULL_BASE_SHA --head FULL_CANDIDATE_SHA
gophercrm-runner history --config /absolute/private/config.json
gophercrm-runner history --config /absolute/private/config.json \
  --root /absolute/private/runner --include-siblings
```

Prepared batch previews include each issue's provisional selection and registered checks.
Before expensive validation, the journal records the actual candidate's selection and exact
commands. The frozen acceptance checks and database matrix remain mandatory. A missing
selected spec or unknown/shared path still forces full coverage. Token-repository changes
are not reclassified as bounded auth merely to shorten a run.

History reads exact issue/PR journal filenames and ignores backups. It groups completed
validation durations by profile and policy, preserves failures and reused-check counts, and
shows individual runs. Forecasts remain unknown when the configured state's matching history
is insufficient. A median is descriptive; differing tasks, machines, database coverage and
interventions prevent a causal speedup claim. Use `note` for actual operator minutes.

Readiness reports the package version, aggregate Python-module digest and comparison with
the trusted checkout's runner source. A known mismatch blocks new paid execution. Missing
checkout source is reported as unknown. After installing a reviewed runner version, verify
this identity and restart any long-lived UI process that still has older modules loaded.

## Reclaim completed dependencies

```sh
gophercrm-runner retention --config /absolute/private/config.json
# Inspect the returned paths and byte estimate, then explicitly apply that exact plan:
gophercrm-runner retention --config /absolute/private/config.json \
  --apply --plan-hash HASH_FROM_PREVIEW
```

Only ignored `gocrm-ui/node_modules` directories in recognized validation worktrees of merged
executions are eligible. The journal must record a merge commit and the checkout head must
belong to its recorded attempts. An active marker, unfinished reference, dirty checkout,
symlinked path or tracked dependency file prevents removal. Backup journals never authorize
cleanup. Source, `.git`, databases, receipts, browser traces and logs are retained.

Apply rechecks the selection and directory identities while holding the execution and shared
heavy-command locks. A changed selection requires another preview. The platform must support
symlink-safe recursive deletion. Reclaimed bytes are logical file sizes, not a guarantee about
filesystem free-space changes. No background cleanup is installed.

## Documentation decisions and rollout

These commands implement the operational portion of the
[2026-10-23 retrospective](retrospective-2026-10-23.md). Browser failures still require
controlled diagnosis; this release does not label #103's timeouts as infrastructure faults,
relax its full validation or claim its acceptance. The first live continuation and subsequent
ticket should record their measured outcomes before further policy changes.

Updated guides: this runbook, the runner README, batch operations, DEVELOPMENT and CHANGELOG.
Product FEATURES and ROADMAP remain accurate because CRM behavior is unchanged. The validation
policy still selects the same gates; DOCUMENTATION_WORKFLOW still governs review and post-merge
verification. Report publication remains an explicit operator action; no external notifications
or automatic GitHub comments are introduced.
