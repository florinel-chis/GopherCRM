# GopherCRM development workflow retrospective

Reviewed on 2026-10-23 against main commit `608e441`.

The workflow preserves work and enforces meaningful review, but validation, recovery and
local resource management still dominate delivery time. The next improvements should make
those operations predictable before expanding unattended execution. Replacing the model or
orchestrator has no demonstrated benefit in this evidence.

This is a retrospective and proposed backlog. It does not change execution policy, extend
budgets, resume the queue or authorize publishing reports. The existing
[validation policy](../../docs/VALIDATION_RELEASE.md),
[batch recovery procedure](BATCHES.md#recovery-and-limits) and
[documentation workflow](../../docs/DOCUMENTATION_WORKFLOW.md) remain authoritative.

## Review scope and evidence

The review screened the 20 most recent prior conversation records returned by local task
history, reading their latest available checkpoint summaries. Those records span multiple
projects; they are not 20 GopherCRM executions. The two principal GopherCRM conversations
were examined further across 20 work checkpoints: seven in “Continue implementing GitHub
tickets” and thirteen in “Verify and merge PR #92.” Some returned turns are summarized or
truncated, so their claims were checked against local Git history, runbooks, source and
retained runner journals where available.

| Conversation record | Treatment |
| --- | --- |
| Continue implementing GitHub tickets | Primary: implementation, recovery, disk pressure and #103 handoff. |
| Verify and merge PR #92 | Primary: runner evolution, validation changes and ticket delivery. |
| Review branch against main | Primary: independent review findings and migration acceptance. |
| Create repository contributor guide | Primary: later GopherCRM CI repair and completion checkpoints. |
| Research vibe coding workflows | Background only; external product claims were not revalidated. |
| Identify backend acceptance steps | Secondary: evidence boundaries and preserved handoffs. |
| Review open items and actions | Secondary: isolated fixtures and explicit outstanding acceptance. |
| Review orchestration plan | Secondary: bounded work and resource constraints. |
| Locate architecture documents | Secondary: discoverability and documentation maintenance. |
| Review session handoff | Secondary: context continuity and restart records. |
| Review project status and workflow | Secondary: running-code identity versus documentation claims. |
| Generate AGENTS.md guide | Secondary: later decomposition of a broad refactoring plan. |
| Optimize transcription resource use | Screened; unrelated workload excluded from CRM findings. |
| Split 777.mp3 into 4 parts | Screened; unrelated workload excluded. |
| Capture Go developer work data | Screened; unrelated application excluded. |
| Untitled contributor-guide record for an ecommerce module | Screened; unrelated review excluded. |
| Untitled contributor-guide record for trading repositories | Screened; unrelated review excluded. |
| Untitled server incident record | Screened; unrelated incident excluded. |
| Untitled agent explanation record | Screened; conceptual background excluded from measured results. |
| Untitled commerce module planning record | Screened; unrelated planning excluded. |

Secondary records inform general lessons about handoffs and acceptance; their test results
are not credited to GopherCRM. The current crash-recovery inspection supplies an additional
snapshot of #103. Private logs, credentials and unrelated conversation contents stay outside
this document.

## Delivery time observed in the journals

The following minutes are sums of recorded stage durations, including failed and cancelled
attempts. “Model stages” combines implementation, repair and review; it is not API latency or
billable model time. “Elapsed” runs from admission to the last recorded attempt end. Gaps
between attempts are not measured human effort. These are different tasks and policies,
not a controlled performance comparison.

| Issue | Recorded profile | Model stages | Local validation | CI | Elapsed | Outcome |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| #97 | Older journal without profile field | 15.5 | 45.0 | 37.4 | 108.9 | Merged |
| #98 | Full | 38.9 | 90.0 | 64.6 | 242.9 | Merged after repair |
| #99 | Full | 8.6 | 45.4 | 38.0 | 92.2 | Merged without repair |
| #101 | Contextual | 27.7 | 9.8 | 19.1 | 73.8 | Merged after repair |
| #102 | Full | 29.6 | 82.4 | 39.0 | 219.4 | Merged after recovery and repairs |
| #103 | Full | 14.7 | 242.8 | Not reached | 257.7 | Browser failure, then budget exhaustion |

Even #99's successful first candidate spent about 83 minutes in validation and CI. For #102,
those stages consumed about 121 minutes. Its remaining approximately 68 minutes outside the
listed stages include orchestration, intervention and other unclassified elapsed time; they
cannot all be assigned to active operator work. Faster code generation alone would leave
most of these delays intact.

## What worked and should be retained

**Durable work and evidence made recovery possible.** After the reported computer crash,
#103's clean worktree still held commit `0fd3b155852842add7a52d4a6b851853c52fcc0f`.
All four recorded validation receipts and their output digests matched. `make verify` passed,
as did atomicity, concurrent-rotation and validation acceptance tests on both SQLite and
MySQL. The worker had exited and no PR existed for its branch. Implementation survived even
though completion did not.

**Independent review found defects beyond green suites.** The PR #92 review addressed
account-switch cache leakage, inactive-user authentication and upgrade ordering. The runner
MVP review found gaps in durable result saving, terminal-state recovery and skipped-subtest
detection. Transaction work exposed nested cancellation behavior. Keep independent semantic
review and acceptance tests that inspect actual state changes; test counts alone do not
establish that the intended behavior is covered.

**Focused validation became an explicit policy.** Runner-only, bounded auth and transaction
profiles now have documented checks. They corrected broad validation choices encountered in
#135, #100 and #106. Frozen acceptance requirements, database checks, unknown-path fallback
and full release validation remain necessary controls.

**Completion has stronger meaning.** The runner distinguishes PR-ready from merged work,
checks the tested and reviewed commit before merging, and verifies reviewed documentation
after merge. Stopped work remains visible. Those controls should survive every efficiency
change.

## Lessons and current gaps

### Validation must be planned before expensive execution

The sessions repeatedly discovered an oversized validation plan after implementation or
browsers had already started. However, #103 is not covered by the bounded auth profile:
it changes repository interfaces and token persistence. Its full fallback follows current
policy. Reclassifying it solely because the suite is slow would remove promised coverage.

Preparation should display the expected profile, exact fallback reasons, database coverage
and an estimate based on comparable historical runs. Recompute that plan from the actual
candidate before running expensive checks. A significant expansion should be visible as a
scope and budget decision. Keep policy corrections in separate reviewed changes, with
explicit treatment of already-admitted work.

### A browser timeout is a symptom that needs attribution

#103's browser output records 129 passes, 12 failures, 15 skips and three tests not run over
approximately 3.6 hours. Failures span deals, users, AEO, labels, leads, login and registration;
many involve navigation, setup or locator timeouts. This supports investigating shared test
or host conditions, but does not prove a resource fault or exclude a refresh-token regression.

The previous sessions also required functional-test rate-limit isolation and more stable
fixtures. Maintain separate tests of production rate limits. Prefer test-owned records and
observable readiness to first-row selection, shared mutable fixtures and arbitrary sleeps.
Diagnostic reruns on a clean candidate and, when needed, its base can identify the cause;
they do not replace a required passing suite.

### Recovery still requires too much reconstruction

The final historical message said #103 was implementing. The journal later showed validation
failure, a reserved repair and an expired deadline. Batch status intentionally reports last
observed progress, not a heartbeat. A conversation's last answer therefore cannot establish
current worker liveness.

Recovery should produce one concise record containing the source commit, worker identity
and liveness, last completed check, unresolved invocation, remaining budgets, reusable
receipts and supported next action. Historical failure fields must be distinguished from
the active stop reason; some merged journals retain earlier failures.

Budget handling also needs a clear batch path. The issue `extend-budget` implementation
changes a child journal, while `BatchRunner.check_record` requires its deadline and call
ceiling to match the allocation. Extending only the child cannot satisfy that invariant.
The CLI has no dedicated batch-extension command. Use the documented disposition path or
design an audited operation that updates the relevant records consistently; do not edit
journals or reset spent work to escape a limit.

#103 stopped at 05:01:18 on 23 October, after a deadline of 04:43:29, both Europe/Bucharest.
The runner already caps command timeouts by remaining budget. Investigate this approximately
18-minute wall-clock overshoot under host sleep/resume and delayed scheduling before
asserting that timeout enforcement is absent. The available evidence does not establish
the crash's cause or exact time.

### Readiness needs capacity and ownership information

Several checkpoints reported only 2–3.5 GiB free. The previous session measured four retained
frontend installations at about 1.85 GiB and test results at about 0.68 GiB. Other machine
workloads also consumed substantial space. These are historical snapshots, not current size
measurements or proof that the runner caused the crash.

The runner checks a minimum free-space threshold, but has no automatic retention policy or
memory-pressure monitoring. A static disk floor does not describe how much a full run can
allocate. Track peak disk growth, memory pressure and owned processes during expensive
stages, then size admission headroom from observations. Avoid overlapping heavy browser
runs across independent state directories until measured capacity supports it.

Cleanup itself needs protection: an earlier private helper matched a backup journal as if
it were active and removed active dependencies twice. The recorded fix uses exact journal
matching and locks. Any shared retention tool must preserve those protections and distinguish
rebuildable dependencies from source, active checkpoints and acceptance evidence.

### General operations should not become ticket-specific scripts

The #100 session created temporary helpers to update a checklist and post completion, then
removed them in favor of shared `report-pr`. The same pattern appears in recovery: a useful
operator action should become a tested shared command when it recurs. Ticket-specific
contracts, receipts and logs should remain data.

Structured output also caused avoidable repeats. PR #144 now constrains implementation
claims to frozen acceptance IDs; earlier review-format failures remain recorded. Keep these
schema checks, and distinguish a format failure from an actual code or review rejection.
Any future report-only recovery must preserve the original response, charged call and
verdict rather than invent approval.

### Handoffs and documentation must describe verified state

Repeated session restarts and questions about completed work show the value of a compact
handoff derived from durable state. Separate implemented, locally validated, reviewed,
published, merged and deployed outcomes. A saved plan or a healthy process cannot stand in
for runtime acceptance or deployment evidence.

Documentation review now addresses drift within a PR. The earlier process report also records
an installation that retained an old package when its version was unchanged; source and
installed module hashes had to be compared. Include installed runner identity in operational
handoffs. Preserve historical reports as dated evidence and link to current policy rather
than silently rewriting their old conclusions.

## Improvements already delivered

These should be exercised and measured before commissioning replacements.

| Change | Evidence in merged history | Remaining limitation |
| --- | --- | --- |
| Frozen contracts, evidence receipts, bounded calls and PR-ready completion | PR #125, `b35a487` | They do not make a test oracle sufficient or a failed run complete. |
| Functional E2E rate-limit isolation | PR #126, `5e2ed66` | Other fixture and timeout failures still need attribution. |
| Existing-PR completion and recovery | PR #129, `7b77f0f` | Operational recovery still requires inspection and explicit disposition. |
| Contextual validation and full release gates | PR #132, `a02ca30` | Unknown and shared changes can still require full validation. |
| Bounded batches and runner-only checks | PR #137, `b8ae3d1` | No heartbeat, automatic restart or retention policy. |
| Reviewed documentation and post-merge verification | PR #139, `c6b2d3b` | The impact map remains a maintained heuristic. |
| Bounded auth and transaction validation | PRs #140 and #141, `ce11e66` and `635326a` | Repository/interface changes remain outside bounded auth. |
| Required SQL upgrade and rollback coverage | PR #142, `e1307f5` | Skipped tests cannot substitute for database evidence. |
| Acceptance-ID constraints on implementation reports | PR #144, `b1d9abc` | Invalid or interrupted outputs still need explicit recovery. |
| Validation checkpoint reuse and inspected failure recovery | PR #146, `608e441` | Reuse requires matching inputs and verified artifacts; changed candidates need fresh validation. |

## Proposed delivery order and acceptance

P0 means the next process work before expanding unattended operation. P1 follows that
baseline. Owners below are suggested responsibilities, not assignments or approved tickets.

| Priority | Improvement and suggested owner | Acceptance for a focused change |
| --- | --- | --- |
| P0 | Recovery summary and batch budget procedure — runner maintainer | A single read-only report explains #103 without reconstructing multiple JSON files. Liveness includes process identity and observation time. Recovery tests cover a hard crash, expired batch, reserved repair, missing checkout and lost remote response. Budget recovery preserves calls, repairs and evidence; no child-only edit can silently diverge from its batch. |
| P0 | Resource observations and admission headroom — runner maintainer/operator | Capture bounded resource samples during long checks and retain the last sample after failure. Show disk growth and owned browser/server processes. Define a tested stop condition and machine-wide heavy-run admission rule. Sleep/resume and wall-clock jumps have explicit deadline tests. Unknown telemetry is labelled unknown. |
| P1 | Browser failure diagnosis and deterministic fixtures — frontend/test maintainer | Classify the 12 #103 failures using saved traces and controlled reproductions. Fix demonstrated test or application defects in scoped changes. Repeated clean full runs pass on required engines with existing skips reported explicitly; increasing timeouts alone is not acceptance. |
| P1 | Safe retention preview and explicit apply — runner maintainer/operator | Preview reports reclaimable bytes and exact candidates. Apply rechecks locks, active/backup journal identity and source status. Tests prove active executions, uncommitted work and required receipts survive. Dependency deletion never fabricates reusable validation; any required restart follows the existing evidence rules. |
| P1 | Validation cost preview and selective policy refinement — validation maintainer/reviewer | Show selected checks and reasons before implementation and again before validation. Only propose a narrower token-persistence profile after enumerating callers and regression coverage. Local and CI selection agree; mixed/unknown paths and full releases retain full coverage. A recorded comparison reports both time and defects found. |
| P1 | Shared completion reporting and restart handoff — runner maintainer | Reuse `report`/`report-pr`; generate Markdown/JSON with commit, phase, evidence, limitations and next action. An optional explicitly requested GitHub publisher uses a stable marker and reconciles lost responses without duplicate comments. No ticket-number-specific helper scripts are needed. |
| P2 | Installed-version checks and recurring metrics — runner maintainer | Handoffs identify installed version/module hashes and policy hash. Reports group duration, interventions, repeated checks and disk growth by profile. Installation verification detects stale builds before admitting work. |

Keep these changes in separate reviewable increments. Start with reporting and recovery
semantics, then resource evidence, browser reliability and retention. Policy refinement can
use those observations. Do not bundle unrelated application fixes, a framework replacement
or increased ticket concurrency into this work.

## Immediate continuation for issue 103

1. Preserve commit `0fd3b15`, its clean worktree, validation receipts and failed browser traces.
   Confirm current process ownership and repository identity before any restart.
2. Diagnose the broad timeout pattern and confirm disposable database/browser readiness.
   A product regression remains possible despite the six passing targeted tests.
3. Resolve the exhausted batch through a supported, audited disposition or a separately
   reviewed recovery improvement. A plain resume or child-only budget extension is insufficient.
4. Revalidate under the admitted policy. Verified checks may be reused only where the
   existing recovery rules allow; a changed source candidate invalidates prior approval.
   Complete required browser, database and migration coverage before independent review,
   publication, CI and merge.
5. Produce the shared completion report and verify reviewed documentation after merge.
   Admit the next dependency-ready item only after #103 has a resolved disposition.

## Measure whether the changes help

Use the next five comparable completed runs as an initial operational sample. Record total
elapsed time, per-check duration, first-attempt success, failure category, repeated successful
checks, explicit operator interventions, peak resource use and bytes retained after completion.
Record actual operator minutes with the existing `note` command; leave missing effort unknown.

Report individual runs and medians first. Five heterogeneous runs do not support a reliable
p95 or a causal speedup claim. Compare like profiles and database coverage as the sample grows.
Keep provider-reported cost separate from subscription billing, and distinguish fewer checks
from faster execution of equivalent checks. Review findings and later full-suite failures must
accompany timing results so an apparent improvement cannot hide lost defect detection.

Initial success means crash recovery needs one report, environment stops do not spend source
repair calls, unchanged valid receipts are reused, completed runs have an explicit retention
disposition, and required acceptance/review/CI evidence remains intact. Numerical latency and
storage targets should follow the first measured baseline rather than an invented percentage.

## Evidence locations and documentation impact

The historical process evidence is in [workflow-mvp-process.md](workflow-mvp-process.md),
[BATCHES.md](BATCHES.md), the [runner README](README.md),
[VALIDATION_RELEASE.md](../../docs/VALIDATION_RELEASE.md) and
[DOCUMENTATION_WORKFLOW.md](../../docs/DOCUMENTATION_WORKFLOW.md). Merged commits listed above
are available in repository history. The four primary conversation records are identified by
their titles in the scope table; their contents are contextual evidence, not execution policy.

Timing rows were recomputed from `attempts[].duration_seconds` in private journals under
`~/.local/share/gophercrm-langflow/runner/`: `issue-97-state`, `issue-98-state`,
`issue-99-state`, `forms-atomic-state`, `password-reset-state` and `refresh-rotation-state`,
using each directory's `issue-N.json`. The #103 worker record and browser command log
`refresh-rotation-state/logs/20261022T222427-abcebce9.log` establish the exit and test summary.
Receipt and output hashes were verified during recovery; no tests were rerun for these
historical measurements.

Source inspection used `Runner.run_command` and `extend_budget` in
[engine.py](gophercrm_runner/engine.py), `BatchRunner.check_record` in
[batches.py](gophercrm_runner/batches.py), [process.py](gophercrm_runner/process.py),
[readiness.py](gophercrm_runner/readiness.py) and [cli.py](gophercrm_runner/cli.py).
The batch-budget interaction and sleep/resume explanation need regression experiments;
they are not presented as live reproductions or a proven crash diagnosis.

This retrospective lands alongside the runner 0.8.0 operations implementation. The delivered
recovery commands, resource supervision and retention operations are documented in
[OPERATIONS.md](OPERATIONS.md), with corresponding updates to CHANGELOG, DEVELOPMENT,
BATCHES and the runner README. Product FEATURES and ROADMAP remain accurate because CRM
behavior is unchanged. Validation selection and acceptance gates are preserved. The dated
measurements above describe the earlier runs; they do not claim that the implementation has
already improved live outcomes. The live continuation and next ticket require their own
measured analysis after independent review and validation.
