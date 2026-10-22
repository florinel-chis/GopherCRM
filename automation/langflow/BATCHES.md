# Prepared unattended batches (MVP)

The batch supervisor processes **one to three explicitly prepared backend items**
sequentially. It freezes each new ticket at admission, runs the existing tests,
independent Claude review, bounded repairs, CI and exact-head squash merge, then
verifies the merge before admitting the next item. Installing this version neither
selects tickets nor starts the audit queue. A live two-ticket canary remains a
separate operator decision; deterministic fixture success is not a live reliability claim.

With `documentation_mode: "reviewed"`, every child also records documentation impact and
explicit independent review decisions. Relevant document contents must match the approved
candidate at the merge commit before the next admission. Include required doc paths in each
manifest's scope; suggestions do not widen it. The existing calls and budgets own this work.
See [documentation maintenance](../../docs/DOCUMENTATION_WORKFLOW.md).

## Prepare and preview

Use the same private configuration and environment as ordinary ticket execution.
Keep `completion_target: "merge"`. Every issue must already appear in that
configuration and have an operator-authored task manifest and registered named
tests. Frontend/manual evidence is unsupported. Dependencies must identify already
merged PRs and their full merge SHAs; do not invent merge SHAs for future items.

Create an absolute-path JSON specification outside the repository:

```json
{
  "schema_version": 1,
  "id": "backend-canary",
  "initial_base": "REPLACE_WITH_THE_FULL_CURRENT_MAIN_SHA",
  "max_items": 2,
  "max_provider_calls": 12,
  "timeout_seconds": 43200,
  "entries": [
    {
      "kind": "issue",
      "issue": 100,
      "manifest": "/absolute/private/tasks/100/task.json",
      "source_paths": ["internal/service/auth_service.go", "internal/service/auth_service_test.go"]
    },
    {
      "kind": "issue",
      "issue": 106,
      "manifest": "/absolute/private/tasks/106/task.json",
      "source_paths": ["internal/repository/customer_repository.go"]
    }
  ]
}
```

The numbers and paths above illustrate the format; they are **not a prepared or
approved batch**. Inspect every requirement and relevant source before selecting
items. `source_paths` are literal Git files/directories, including paths expected
to be absent. Their Git object IDs guard preparation assumptions: a prior merge
that changes a guarded object stops the next admission. Guard all code relevant
to the task's assumptions, without guarding unrelated areas unnecessarily.
Manifests and specifications must use absolute paths without symlinks.

```sh
gophercrm-runner prepare-batch --config /absolute/private/config.json --spec /absolute/private/batch.json
gophercrm-runner run-batch --config /absolute/private/config.json --batch backend-canary
```

Preparation records immutable issue/manifest, source object, guidance and complete
configuration/check-policy hashes. Preview resolves **all** entries without model
calls. No existing journal or pre-frozen ticket contract may be adopted. Correct
invalid inputs and prepare a new batch ID; active contracts are never silently
refreshed. Changes to any configuration field require a new prepared batch.

An existing open, same-repository backend PR can be the **first** entry instead:
`{"kind":"pr","pr":123,"head":"FULL_CURRENT_HEAD_SHA"}`. Its base must equal
`initial_base`. The normal existing-PR completion workflow validates and reviews
it. Arbitrary PRs are never discovered or adopted automatically. Later entries
must be new tickets because existing branches cannot anticipate a prior squash merge.

## Start, observe and stop

```sh
# Explicit background execution; survives closing the initiating terminal/UI.
gophercrm-runner start-batch --config /absolute/private/config.json --batch backend-canary
gophercrm-runner status-batch --config /absolute/private/config.json --batch backend-canary
gophercrm-runner stop-batch --config /absolute/private/config.json --batch backend-canary

# Foreground alternative, through the same serialized Langflow graph:
gophercrm-runner run-batch --config /absolute/private/config.json --batch backend-canary --execute
```

The detached child inherits the existing private environment. Its standard input
is disconnected and its log has mode `0600` under the private state directory.
Start reports a launch reservation, PID and log path; inspect status/logs for
actual progress or startup failure. There is **no automatic restart**. An abrupt
process death may leave the last worker state as `running`; timestamps and logs
show the last observed progress, not a heartbeat. Restart explicitly after inspection.

Import [flows/batch.json](flows/batch.json) in Langflow. Supply the local policy
path and prepared ID. **Preview** is the default. **Status** reads the same durable
journal and displays current entry/phase, index, accepted base, call budget,
remaining wall time, last progress, stop reason and worker log. Use a separate
Status invocation to inspect an executing batch. **Execute** runs the same graph
as the CLI; use `start-batch` when execution must outlive the UI request.

Starting a new batch explicitly admits that selected batch despite an earlier
global queue stop. `stop-batch` and the existing `stop-after-current` finish the
admitted item and prevent the next admission, including already-preloaded queue
entries. A completed batch leaves the global queue stopped. Shared execution and
launch locks prevent concurrent CLI/UI workers. Durable batch ownership also
prevents standalone ticket/PR execution from taking over after a crash.

## Recovery and limits

Only this batch's verified squash merges may advance its accepted base. The
handoff rechecks candidate-bound tests and review receipts, PR identity, merge
parent and tree. An unrelated main update stops admission. GitHub branch
protection/merge queues remain responsible for atomically preventing main from
advancing during the remote merge request; a mismatched result cannot advance
this local batch.

Provider calls are reserved durably before invocation. Each admitted execution
receives the smaller of its ordinary call/time limits and the remaining batch
limits. Downtime counts against the deadline; retries never reset calls, time or
repair rounds. There are no automatic budget extensions. Repeating execution
first reconciles a lost merge response read-only, even after expiry, without
issuing another merge. A failed/stopped batch requires explicit `--resume` to
perform new work:

```sh
gophercrm-runner start-batch --config /absolute/private/config.json --batch backend-canary --resume
```

After a hard crash with an unresolved provider invocation, inspect and stop the
orphaned provider/test processes first. Then record that acknowledgement:

```sh
gophercrm-runner recover-batch --config /absolute/private/config.json --batch backend-canary \
  --processes-stopped --reason 'Inspected and stopped the orphaned execution processes'
```

Recovery does not kill arbitrary stored PIDs, refund calls, invent finish times,
refresh inputs or resume execution. The original gates still apply on `--resume`.
For an inspected validation misclassification in the current child, use
[`repair-validation`](README.md#inspected-validation-misclassification) with
`--batch ORIGINAL_BATCH_ID` as well as the child's `--issue` or `--pr` and exact head.
The standalone form is refused while a batch owns the queue; resume the same batch afterward.
If correction requires a new plan or budget, deliberately end the batch with
`abandon-batch --config ... --batch ... --reason '...'`. An unfinished child stays
active with its evidence and limits for ordinary operator recovery. Complete it
before preparing new work. Unresolved provider calls must be acknowledged before
abandonment. Historical plans, allocations and spent work remain intact.

The MVP performs **no automatic cleanup**. It retains source/worktrees, dependency
installs, databases, receipts and logs, and keeps the existing disk threshold.
Disk shortage stops execution for inspection; it never deletes the main
checkout's dependencies. No frontend evidence, parallel execution, release,
deployment, branch-rule changes or external notifications are included.

## Development record and lessons

Issue [#135](https://github.com/florinel-chis/GopherCRM/issues/135) follows the
single-ticket #99 run. The refined prompt was: add a bounded supervisor around
the existing gates, freeze only at admission against verified main, preserve
budgets and evidence across interruption, default to preview, and leave live
execution disabled until specific prepared tickets are selected.

The implementation keeps task/PR execution in the existing engines. The new layer
owns immutable preparation, sequential admission, allocation, merge handoff and
background lifecycle. Tests use real temporary Git worktrees/commits and real
Langflow serialization with deterministic external command boundaries. Detached
worker tests run an actual child process with a fake provider.

Lessons from implementation:

- The durable allocation must precede worktree/contract creation. A crash between
  allocation and the secondary active marker needs explicit reconciliation.
- A terminal child journal does not prove that batch advancement was saved. The
  handoff must be idempotent, and cleanup after a terminal batch must be repeatable.
- Sharing an execution lock requires same-task nesting; a competing async task
  must not clear the original task's ownership when its lock acquisition fails.
- A detached worker needs a startup barrier so it cannot race its parent's lock,
  and separate launch ownership so double-clicks cannot start duplicate workers.
- Independent review found that a delayed resume could erase a newer stop. A
  launch-time control snapshot now travels through Python/Langflow startup;
  stop tokens change atomically, and a later stop takes precedence over resume.
- Source guards deliberately trade throughput for inspectable assumptions. Tasks
  that depend on earlier changes should be prepared after those changes merge.
- The first #135 completion run exposed an overbroad full-suite fallback for
  automation-only edits. The user stopped that approach. The runner-only profile
  now selects workflow tests and health smoke locally and in CI; application and
  full-release coverage remain separately defined in the validation runbook.

Potential follow-ups, after a separately selected live canary: add a worker
heartbeat/process-identity display; measure time per phase before overlapping CI;
add conservative completed-dependency cleanup with explicit ownership proofs;
and design frontend evidence support separately. None is enabled by this MVP.
