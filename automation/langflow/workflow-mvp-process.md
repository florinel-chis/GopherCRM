# Workflow MVP process and lessons — 2026-10-19

This increment applies the local development-workflow improvement plan to a bounded backend
workflow. It keeps Langflow 1.12.6 and the existing Claude CLI, adds no provider dependency,
and preserves all six application CI gates. The full comparison study remains deferred.

## Refined prompt used for execution

> Finish the active ticket #95 through validation, independent review, CI and merge, then stop
> its queue. Preserve its evidence and use the observed failures as an operational baseline.
>
> Implement the smallest coherent workflow MVP in an isolated maintainer worktree: explicit
> PR-ready completion, stop-after-current, versioned recovery, read-only readiness, frozen task
> criteria and guidance, candidate-bound check/review evidence, durable call/time limits, and
> readable reports. Preserve mandatory validation, protected paths and exact-commit controls.
> Remove the proven duplicate dependency install and isolate verification from application DB
> settings. Keep credentials and raw evidence private.
>
> Write behavioral regressions, observe failures before fixes, run required checks, and obtain
> one independent whole-branch review. Fix material findings and retest. Record decisions,
> interventions, actual results and limitations; model claims alone do not complete criteria.
>
> Use the updated workflow for exactly one next eligible ticket, initially #96, with a frozen
> backend contract and explicit PR completion target. Finish local tests, independent review,
> publication and CI, then stop at PR-ready without merging. Document lessons and ranked
> improvements. Do not start the remaining queue or claim the 48-run comparison was performed.

## Evidence and release sequence

1. Captured a deterministic baseline: 29 runner tests passed, zero skipped, and the exported
   workflow matched. Recorded runtime versions, limits and unknown measurements privately.
2. Finished [#95 / PR #124](https://github.com/florinel-chis/GopherCRM/pull/124), merged as
   `52dd572542531188fb80ea9fc87506d94f5241de`, with all six CI checks passing. Its final local
   validation passed `make verify`, SQLite and MySQL browser suites (each 142 passed, 15 skipped),
   and non-skipped fresh/upgrade/rollback migration coverage. Independent review approved the
   final candidate. The old queue stopped before #96.
3. Implemented completion/recovery, readiness/environment isolation, contracts/evidence, then
   limits/reporting and verification parity in separate focused commits. Used real temporary Git
   worktrees with deterministic provider/GitHub boundaries and the real serialized LFX graph.
4. Independent whole-branch review identified four material gaps: non-atomic completed model results, active terminal journals skipped by queue recovery, skipped subtests hidden by parent passes, and missing browser JWT readiness. Added reproductions before fixes, including the real serialized graph and a real Go/Testify suite. The maintainer PR records final checks and fix results. After delivery, install the
   reviewed version and use a new private state directory for one #96 canary, eight CLI calls,
   four elapsed hours, three repairs, and a PR-ready target. Any extension must be explicit.
5. Record actual canary results in [tracker #94](https://github.com/florinel-chis/GopherCRM/issues/94).
   This pre-rollout report does not claim that future run has succeeded. Keeping outcomes in
   the tracker avoids advancing main with a documentation-only commit that would invalidate
   the canary's freshly tested base.

The process ledger and raw logs remain local. Two schema tests were authored before their
implementation but initially executed afterward; they were subsequently run against the exact
baseline module and failed there. That proves regression coverage, not test-first execution.

## Coverage of the source plan

| Source area | MVP coverage and limit |
| --- | --- |
| T1 baseline | Captured revision, versions, invariants and unknowns. One operational canary protocol; no paired benchmark. |
| T2 completion/recovery | PR-ready never merges; old omitted completion defaults remain merge. Queue drain, lost remote responses, exact legacy backup and unsupported newer schemas tested. Unfinished legacy adoption is refused. |
| T3 readiness | Shared CLI/graph preflight checks tools/auth, private state, space, Chromium, disposable DB connectivity and migration tests. Metadata can be unknown. Probe only executes `SELECT 1`. |
| T4 contracts | Strict operator manifests, named checks, dependency merge/ancestry proof, cycle checks, scope and immutable input hashes. Refresh preserves history and invalidates evidence. No planning-model call required. |
| T5 provider/context | Same Claude permissions, explicit bounded guidance, configured versus observed model/usage, versioned prompt metadata. Separate model settings per role are deferred. |
| T6 acceptance | Exact named Go JSON pass events and private digest-checked receipts; criterion-by-criterion independent review. Generic passing tests and implementation claims cannot substitute. Manual/UI evidence profiles fail explicitly; their full implementation is deferred. |
| T7 budgets/metrics | Durable reservations, elapsed deadline including downtime, bounded subprocesses, failure attempts, explicit extensions and operator minutes. Cost may be partial/unavailable; no hard money/token guarantee. Operator pause duration is not inferred. |
| T8 verification | Shared nonmutating `make verify-runner`, merge-group coverage, one frontend install through `make verify`, fresh validation checkout, all six core CI gates retained. |
| T9 evaluation | One production-repository operational canary only. The isolated 48-run paired study, seven-day observation and efficiency/adoption thresholds are not completed or established. |

## Observed lessons and potential improvements

- **Environment failures wasted coding work.** Inherited DB host/port values broke tests that
  intentionally exercise defaults and spent two repair rounds. The MVP sanitizes only the
  application verification environment, retains migration credentials, and classifies known
  infrastructure failures separately. Required DB checks still run with the disposable profile.
- **Passing tests were insufficient.** Independent review found explicit-link and customer-only
  task-update oracles after broad tests passed. Added targeted SQLite/MySQL regressions and
  uniform association replacement. Frozen criteria and named evidence improve traceability;
  they cannot prove that a test's assertions are sufficient, so review remains mandatory.
- **Guidance must be supplied deliberately.** Safe mode does not load repository instructions.
  The MVP snapshots selected guidance; ignored operator-local `AGENTS.md` must be present in
  the trusted checkout and is labelled separately from committed documents.
- **Dependency cleanup caused an operator error.** A private cleanup helper mistook a backup
  journal for the active one and deleted active `node_modules` twice. Fixed exact journal-name
  matching and lock behavior with regressions; cleanup now requires an explicit apply step
  after preview. No background cleanup runs. Source, evidence and databases were preserved.
- **Validation consumes substantial disk and time.** The runner now avoids a duplicate `npm ci`
  and checks free space before admission and each fresh checkout. Full checks remain mandatory.
  Retention/cleanup should be a separate reviewed improvement with active-work protection.
- **Interrupted results need durable boundaries.** Save successful implementation completion and
  review verdicts before finalizing them. Never reroll a persisted rejection or refund an
  interrupted call. A hard crash with a potentially live provider process stops for inspection.
- **Local gates exceed current remote protection.** The observed branch configuration required
  five core checks, omitted SQLite, allowed stale bases, and did not enforce rules for admins.
  The runner still requires six checks and a current base. Propose a separate reviewed policy
  change for parity and atomic base enforcement; no remote protection settings changed here.
- **Efficiency remains unmeasured.** Historical active human minutes and attributable cost were
  incomplete. Provider-reported cost is not subscription billing. A single different ticket
  cannot establish a causal latency, cost or effort improvement.

Next priorities: complete and inspect the one canary; reconcile remote protection with the
runner's gates; add safe evidence/dependency retention; then implement manual/UI evidence and
broader task examples only when needed. Decide whether to fund the paired study before changing
providers, introducing more orchestration, or claiming broad adoption.
# Existing-PR completion follow-up (2026-10-20)

The original pilot stopped at PR-ready. The next increment adds a separate,
explicitly pinned PR completion workflow, available both in Langflow and through
`complete-pr`. It verifies reusable ticket evidence or runs fresh validation and
independent review, repairs findings within durable limits, waits for CI, then
merges the exact approved head. This does not restart the remediation issue queue.
New configurations now target merge, with preview as the initial execution mode;
existing configurations and the optional PR-ready stop are preserved.
See the [operator instructions](README.md#complete-existing-prs-through-merge).

The first independent review rejected the implementation for two concrete
recovery gaps: a repair interrupted by cancellation or a depleted provider budget
could not resume, and an unsuccessful PR completion could permanently retain the
queue slot. The fixes add charged repair resumption, an explicit no-change retry,
audited abandonment, preflight slot cleanup, and a visible queue-blocking error.
The next review extended the recovery cases to external merges at another head,
orphaned provider calls and crashes before the first journal write. These now have
explicit abandonment dispositions; an orphaned process requires an audited
operator acknowledgement that it was inspected and stopped. External merges
never become fabricated runner validation, and spent calls remain spent.
A further review distinguished recovering a crashed execution from intentionally
abandoning it. `recover-pr` now resolves inspected orphaned calls for a normal
resume, or releases a journal-less admission for a later fresh start. Abandonment
remains a separate, deliberate terminal disposition.
Review also exposed a distinction between observing a remote merge and proving
workflow completion: even an identical head is an external result if the runner
never reached its approved merge transition. Reconciliation now verifies the
recorded transition and receipts before completing a linked ticket. Other merges
are recorded without crediting acceptance, while releasing the queue slot.
Additional fault cases covered rejected protected-file edits and dirty repairs
superseded by a main advance. Repair restart now retains the rejected worktree
and retries from a clean checkout, while refresh retains superseded dirty trees.
External merge reconciliation also waits for orphaned-process acknowledgement
before releasing admission. These recoveries preserve spent budgets.
Missing candidate or source-ticket directories during merge reconciliation now
produce an unverified evidence error that explicit abandonment can dispose.
Non-cancellation verification failures cannot bypass proof checks or indefinitely
prevent an audited release.
Regression tests exercise these failure sequences, in addition to stale inputs,
evidence tampering, receipt reuse, protected repair paths and lost push/merge
responses. The initial full repository verification passed Go build/vet/race,
frontend build/lint and 653 frontend tests (one existing TODO).

Lessons: terminal and failure transitions need explicit operator recovery paths;
an empty queue must not hide blocked work; reuse must verify immutable evidence,
not just a previous green status. New PRs are pinned by an operator, but their
build scripts still execute locally: worktrees do not supply OS isolation.
Concurrent main changes still require server-side branch protection for atomic
enforcement, plus fresh local validation after an explicit candidate refresh.
Fork intake and automatic conflict resolution remain outside this increment.

