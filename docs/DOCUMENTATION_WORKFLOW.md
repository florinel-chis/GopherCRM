# Documentation maintenance through PR completion

Documentation is assessed and updated in the same PR as the behavior it describes.
The independent reviewer checks that assessment before merge. The local runner then
verifies that the reviewed documentation landed before completing the item or advancing
a prepared batch. It never repairs documentation by writing directly to main.

## Working prompt and MVP

> For every ticket or existing PR, identify documentation affected by the actual diff.
> Update relevant product, API, configuration and operational guidance in the same PR.
> Preserve accurate documents with a specific explanation rather than mechanical edits.
> Require independent review of each decision, bind evidence to the tested commit, and
> verify the reviewed document contents after merge. Preserve frozen task scope and
> existing review/CI gates. Reuse existing model calls and focused validation. Record
> limitations and lessons instead of claiming that a path map proves semantic accuracy.

The MVP implements this inside the existing Langflow ticket and PR engines. Newly
generated configurations select `"documentation_mode": "reviewed"`. An omitted field
uses `legacy` to preserve historical execution schemas and policy hashes. Existing
installations must explicitly select the new mode in a new configuration/state directory
after finishing active executions; setup does not overwrite their policies. Historical
approvals are not relabelled as documentation-reviewed approvals.

## Assess, update, review, verify

1. During task preparation, include the relevant documentation paths in the task's
   `scope_paths`. The implementation prompt includes preliminary suggestions from those
   paths. Suggestions never expand frozen scope. If required edits fall outside it,
   correct and explicitly refresh preparation before continuing.
2. Implement behavior, regression coverage and necessary documentation together. Use
   actual behavior and supported test evidence. Do not refresh dates or coverage counts
   without checking them. Remove a roadmap item only when its missing behavior is delivered.
3. Validation computes an impact plan from the exact base/head diff and records document
   Git blob identities, including additions and removals. A separate immutable receipt
   binds that plan to the same execution, policy and validation attempt as the tests.
4. The existing independent review call reads the code and relevant documents and gives
   every planned document a disposition with a specific explanation:

   | Disposition | Meaning |
   |---|---|
   | `updated` | A real committed change is accurate, including a justified removal. |
   | `already-accurate` | The existing unchanged document already describes the behavior. |
   | `no-impact` | The unchanged or absent document does not need a change for this PR; explain why. |
   | `needs-update` | Documentation is missing or stale; reject approval and describe the repair. |

   Missing, duplicate, unknown or contradictory dispositions cannot approve a PR.
   Necessary corrections outside the suggested map are also blocking findings; after
   the repair changes those documents, they appear in the next candidate's plan.
   A real gap follows the existing bounded repair, retest and review cycle. The normal
   success path adds no model invocation solely for documentation.
5. Publication and merge recheck candidate-bound evidence. New ticket PR descriptions
   include a bounded count of documentation decisions; free-form reasons stay in the
   private report so they cannot inject GitHub issue-closing keywords. Existing-PR completion preserves its admitted
   title/body and exposes decisions through its result and private report instead.
6. After GitHub confirms the merge, the runner reads that exact merge commit and compares
   its relevant documentation blobs with the approved candidate. A mismatch stops the
   handoff and records a recoverable failure. It does not start another ticket or guess
   a correction. Lost-response reconciliation works after the execution deadline without
   another merge or model call.

If verification fails, first inspect the retained failure. Restoring missing local evidence
and repeating the original execution can verify the existing merge without merging again.
When the mismatch requires a separate correction, explicitly dispose of the failed local
execution instead of editing its journal:

```sh
gophercrm-runner abandon-issue --config /absolute/private/config.json --issue 123 \
  --reason 'Inspected unverified merged documentation; prepare a separate correction'
```

This command only applies to a merged ticket whose documentation verification failed. It
retains failures, receipts and spent budgets, marks the execution **abandoned, not validated**,
releases its active marker and leaves the queue stopped. If a provider invocation remains
unresolved, inspect and stop its processes first, then pass `--processes-stopped`. Existing PR
completions use `abandon-pr`. A batch owner must first be recovered if necessary and explicitly
abandoned using the [batch procedure](../automation/langflow/BATCHES.md); an unverified child
never advances the batch. Disposition does not edit GitHub or resume the next ticket.

## What the impact map covers

`automation/langflow/gophercrm_runner/documentation.py` owns the deterministic mapping.
Application changes suggest README, CHANGELOG, FEATURES and ROADMAP, plus relevant feature testing
guides. Schema changes include the data model and upgrade/setup guidance. Configuration
changes include setup and example environment settings. Auth/shared middleware and AEO
changes include their operational/security guidance. Workflow and delivery changes include
the general guides, DEVELOPMENT, VALIDATION_RELEASE, this runbook and the Langflow guides. Changed documentation
is always included; unmapped changes conservatively suggest general guidance.

The plan records absent suggested files too: a reviewer must explain why they are irrelevant
or reject the missing guidance. Documents must be regular committed files, not symlinks.
Large assessments are bounded and require splitting the PR. Generated Swagger outputs are
flagged for handler changes. Their source annotations remain authoritative: use `make swagger`
when they change. Agents do not acquire shell permission through this workflow; if generator
work cannot be completed within the registered execution, a maintainer must prepare the output
before approval. A textual claim does not prove a generator ran.

Inspect a committed candidate without executing a ticket:

```sh
PYTHONPATH=automation/langflow python3 -m gophercrm_runner.documentation \
  --base FULL_BASE_SHA --head FULL_CANDIDATE_SHA
```

CI publishes `documentation-impact-<run-id>` and a job summary for PRs, merge groups and
merged main commits. It uses the same changed-commit inputs as validation. An unavailable
comparison (including unsupported document types or excessive scope) is explicitly labelled
without discarding the validation plan. This artifact identifies what reviewers should assess;
**it is not semantic approval** and needs no model credentials. Human-authored PRs still need
their reviewer to check documentation; the strict structured gate applies to the local runner
with reviewed mode enabled. The post-merge CI report does not launch another agent or PR.

## Validation and observed lessons

Workflow-only changes use hygiene, runner tests, dependency setup and health smoke under
the [contextual policy](VALIDATION_RELEASE.md). Documentation maintenance does not independently
trigger the full application suite. Releases retain their complete validation matrix.

The first implementation exercises real Git commits and merges with deterministic external
boundaries. Regression cases cover false or missing decisions, rejected gaps and repair,
scope violations, receipt tampering, promotion of an already-reviewed ticket, exact merge
comparison and lost responses after deadline expiry. These are workflow tests, not evidence
that an LLM will always notice stale prose.

The motivating failure was a workflow change whose runbook was updated while FEATURES,
DEVELOPMENT and operator-local instructions still described the older test policy. This
change updates those descriptions and maps future workflow changes to the same guides.
Independent review identified two additional failure paths. Public summaries now use trusted
counts rather than reviewer-authored prose, which could contain GitHub closing keywords or
exceed body limits. An explicit disposition now releases an inspected failed post-merge ticket
without claiming success, preserving the failure and keeping the queue stopped.

Reusing existing model calls avoids turning documentation into another expensive independent
pipeline. Preserving existing PR body identity also avoids invalidating an admitted review
merely to append a generated status section.

Potential improvements after observing real executions:

- Measure which documents the map misses or suggests unnecessarily; refine the map from
  those observations rather than adding every document to every PR.
- Add safe registered generator commands and output-drift checks for generated references.
- Add incremental Markdown link/anchor checks that distinguish new breakage from existing debt.
- Consider a periodic documentation audit separately; this MVP does not install a schedule or
  scan unrelated historic claims on every PR.
