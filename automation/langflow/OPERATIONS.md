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

Heavy runner commands (`make verify*`, `make e2e`, builds, Go and npm commands, and the Python
auth/transaction/migration check entrypoints) acquire one
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
selected spec or unqualified shared path still forces full coverage. Token-repository changes
are not reclassified as service-only auth merely to shorten a run. Version 0.8.1 adds a separate
auth-persistence profile with candidate test-availability and shared-interface scope proofs,
real SQL boundaries and focused auth browsers; see the [validation policy](../../docs/VALIDATION_RELEASE.md).

History reads exact issue/PR journal filenames and ignores backups. It groups completed
validation durations by profile and policy, preserves failures and reused-check counts, and
shows individual runs. Forecasts remain unknown when the configured state's matching history
is insufficient. A median is descriptive; differing tasks, machines, database coverage and
interventions prevent a causal speedup claim. Use `note` for actual operator minutes.

Readiness reports the package version, aggregate Python-module digest and comparison with
the trusted checkout's runner source. A known mismatch blocks new paid execution. Missing
checkout source is reported as unknown. After installing a reviewed runner version, verify
this identity and restart any long-lived UI process that still has older modules loaded.

## Inspect the candidate before validation

Set `"validation_review_required": true` in a fresh policy before admission. After
implementation commits, the runner saves `validation_inspection` with the exact
head/base/policy, commands and named-test identities, then stops before dependency
installation or checks. This is an operator hold, not a source failure or a request for
user authorization; no repair call is spent. Inspect the committed diff and complete plan:

```sh
gophercrm-runner confirm-validation --config /absolute/private/config.json \
  --issue 111 --head FULL_HEAD_SHA --plan-hash INSPECTED_HASH --batch aeo-runtime \
  --reason 'Inspected exact diff and every check; bounded AEO profile'
gophercrm-runner start-batch --config /absolute/private/config.json \
  --batch aeo-runtime --resume
```

Omit `--batch` for standalone work; use `--pr` for PR completion. Confirmation requires
the exclusive execution lock, active candidate and unchanged plan. Changed candidates,
bases, contracts, policies, commands or named-test identities require fresh inspection.
Original deadlines, calls, repair rounds and receipts remain intact. If selection
unexpectedly requires full coverage, do not confirm it: inspect scope or use the documented
fresh-policy migration. Explicit full/release policy remains available. The default is
false, preserving previously admitted configuration hashes.

Version 0.8.2 adds this hold and the shared AEO profile in
[VALIDATION_RELEASE](../../docs/VALIDATION_RELEASE.md). Install only reviewed main, verify
module identity, then prepare a fresh application state after the prerequisite merges.

Version 0.8.3 adds a committed proof for comment-only edits to the root `.env.example`.
Include that literal documentation path before freezing when its guidance changes. Active
settings, unsupported syntax and missing proofs retain full fallback; inspect the actual
plan before continuing. Install reviewed main and verify module identity as above.

## Retire a ticket closed outside the runner

A ticket execution remains admitted when its GitHub issue is closed by work this execution
did not merge, for example a maintainer merge or a separate `complete-pr` completion of the
same branch at a later head. `run` refuses such a journal because no runner merge was
confirmed, so the active marker and admission slot stay occupied. Inspect the journal, the
issue and the closing pull requests, then retire the execution:

```sh
gophercrm-runner retire-issue --config /absolute/private/config.json --issue 158 \
  --reason 'Closed by PR #159, merged through complete-pr in another state directory'
```

Use `retire-issue` only for an unfinished execution (any stage except `merged`, `pr_ready`,
`abandoned` and `retired`) whose issue GitHub reports as `CLOSED`. An open issue is refused;
resume it with `run`. A missing journal, a legacy journal or a journal admitted under a
different policy is refused. Use `abandon-issue` instead for a merged ticket whose post-merge
documentation verification failed, and `abandon-pr` for an explicit PR completion. A running
provider invocation requires `--processes-stopped` after the orphaned process has been
inspected and stopped; the acknowledgement is recorded as for `abandon-issue`.

The command reads `gh issue view --json state,stateReason,closedByPullRequestsReferences` and
reads each closing pull request of the configured repository, together with any pull request
the journal records, through `gh pr view`. The journal records `stage: retired`,
`completed_at`, a redacted `retirements` entry and `remote_disposition` with the issue state,
its state reason and each pull request's number, state, head and merge commit. The disposition
is marked `validated_by_this_execution: false`: the closing work was not tested, reviewed or
merged by this execution. The admission slot and active marker are released and the queue is
stopped; resume it explicitly with `resume-queue`. A repeated call returns the recorded
disposition without a second entry, and a later `run` reports the retirement.

The command never closes, reopens, comments on or edits issues or pull requests. It never
deletes the worktree, branch, receipts, artifacts or spent budgets, never refunds provider
calls and never relabels the execution as merged.

## Maintenance outside executions

Changes to the runner itself, its validation gates and fixes for CI flakes are maintainer work.
They are prepared as ordinary pull requests and merged through `complete-pr`, never admitted as
ticket executions, because implementation agents cannot edit the protected runner, workflow and
validation paths. When such a pull request closes an issue that a ticket execution still holds,
retire that execution as described above.

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

Both preview and apply refuse a state directory while a batch owns it, including a failed or
stopped batch awaiting recovery. They become available there after the batch completes or is
explicitly abandoned. Do not abandon an execution merely to clean dependencies: use the
configuration of another completed, unowned state directory to reclaim its eligible trees,
or restore disk headroom elsewhere before resuming the owned batch.

## Documentation decisions and rollout

When a reviewed policy correction requires a fresh completion, preserve the old journals,
receipts, spent calls and absolute deadline. Version 0.8.4 permits `max_repair_rounds: 0` in
the fresh policy when no repair rounds remain. A failed check or rejected review then stops
before reserving a repair or calling an implementation provider; no gate is waived. Record
the old and remaining allowances in the migration evidence. Do not edit an admitted policy.

The `check-go.py` profiles (AEO, transaction and migration) remove application
`DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`,
`DB_NAME`, `DB_DRIVER` and `DB_PATH` only from their Go subprocesses. They retain
`TEST_MYSQL_DSN` for disposable SQL fixtures. This prevents inherited server settings from
polluting configuration-default tests locally and in CI; browser/server settings are unchanged.

These commands were introduced in 0.8.0 as the operational portion of the
[2026-10-23 retrospective](retrospective-2026-10-23.md), without changing validation selection.
The subsequent #103 continuation was stopped because its old policy still selected unrelated
entity browsers. Version 0.8.1 separately adds the bounded persistence policy described above.
Use a fresh explicit completion state under the reviewed policy; retain old evidence and spent
budgets, and never relabel an interrupted check. This correction neither establishes the cause
of the earlier timeouts nor claims #103's acceptance. Record the focused continuation's outcome.

The 0.8.0 rollout updated this runbook, the runner README, batch operations, DEVELOPMENT and
CHANGELOG. The 0.8.1 policy correction also updates VALIDATION_RELEASE and the FEATURES CI
description. CRM behavior is unchanged. DOCUMENTATION_WORKFLOW still governs review and post-merge
verification. Report publication remains an explicit operator action; no external notifications
or automatic GitHub comments are introduced.
