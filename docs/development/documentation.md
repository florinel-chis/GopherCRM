# Documentation maintenance through PR completion

> **For:** Contributors who change behaviour or docs in a pull request. Part of the [documentation index](../README.md).

Documentation is assessed and updated in the same pull request as the behaviour it describes, and
the reviewer checks that assessment before merge. CI publishes a deterministic impact map of the
documents a change may affect; the map tells the author and the reviewer what to assess, and the
author decides what to change.

## Steps in a pull request

1. Find the affected documents. CI publishes them as the `documentation-impact-<run-id>` artifact
   and a job summary; locally, run
   `PYTHONPATH=automation/langflow python3 -m gophercrm_runner.documentation --base <sha> --head <sha>`.
2. Update each affected document in the same pull request, or record in the pull request why it is
   still accurate. Describe the actual behaviour, check dates and counts before changing them, and
   remove a roadmap item only when its missing behaviour is delivered.
3. Add a `CHANGELOG.md` entry under `[Unreleased]`.
4. Run `python3 scripts/ci/check-doc-citations.py`, `python3 scripts/ci/check-user-guide.py` when
   the pages under `docs/administration/` or `docs/integrations/` change, and the tests that read the changed documents. Find those tests with
   `command grep -rl '<NAME>.md' --include='*_test.go' --include='*.py' --include='*.test.ts*' --exclude-dir=node_modules .`
   and run each one it lists.
5. When API annotations change, run `make swagger` to regenerate the reference and
   `make verify-api-docs` to check it.

## What the impact map covers

`automation/langflow/gophercrm_runner/documentation.py` owns the deterministic mapping.
Application changes suggest README, CHANGELOG, FEATURES and ROADMAP, plus relevant feature testing
guides. Schema changes include the data model and upgrade/setup guidance. Configuration
changes include setup and example environment settings. Auth/shared middleware changes include
the developer guide with its DEV_AUTH and DEV_RBAC topic pages and the cross-cutting test guide;
AEO changes include the AEO egress guide. Route, schema and configuration changes
also suggest the fork guides (FORKING, EXTENDING); changes to the forms, automation, ticket and
quote modules suggest USE_CASES, matched on their feature directories and file-name prefixes
(`form_`, `automation`, `ticket`, `quote`), so an unrelated `UserForm.tsx` does not. Workflow and delivery changes include
the general guides, DEVELOPMENT with its DEV_TESTING_CI topic page, VALIDATION_RELEASE, this runbook and the Langflow guides. Changed documentation
is always included; unmapped changes conservatively suggest general guidance.

The plan records absent suggested files too: a reviewer must explain why they are irrelevant
or reject the missing guidance. Documents must be regular committed files, not symlinks.
Large assessments are bounded and require splitting the PR. The generated API reference
(`api/swagger.json`, `api/swagger.yaml` and the HTML page `api/index.html`) is flagged for changes
to its sources: handlers, models, services, `internal/utils/response.go`, the general block in
`cmd/main.go` and the renderer. The annotations remain authoritative: use `make swagger` when they
change, and `make verify-api-docs` (CI runs its checker, `scripts/ci/check-api-docs.py`, on every change) fails while the committed files are stale.
A textual claim does not prove a generator ran.

Inspect a committed candidate without executing a ticket:

```sh
PYTHONPATH=automation/langflow python3 -m gophercrm_runner.documentation \
  --base FULL_BASE_SHA --head FULL_CANDIDATE_SHA
```

CI publishes `documentation-impact-<run-id>` and a job summary for PRs, merge groups and
merged main commits. It uses the same changed-commit inputs as validation. An unavailable
comparison (including unsupported document types or excessive scope) is explicitly labelled
without discarding the validation plan. This artifact identifies what reviewers should assess;
**it is not semantic approval** and needs no model credentials. The pull request's reviewer
checks the documentation. The post-merge CI report does not launch another agent or PR.
