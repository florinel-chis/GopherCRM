# Review policy

Changes reach `main` through a pull request with green CI and the code owner's approval
(`.github/CODEOWNERS`). Reviewers, human or automated, work through the passes below and label
each finding **Important** or **Nit**. A finding never approves or blocks a pull request on its
own; the code owner decides.

## Passes

- **Bugs:** logic errors, broken edge cases, regressions, error paths that turn a failure into a
  success or a "not found".
- **Security:** authentication and role checks (admin, sales, support, customer), input
  validation, sort and filter allowlists, secrets or personal data in logs or responses, the
  public forms surface (`/forms/public/*`: CORS, rate limits, spam layers).
- **Data:** erasure keeps its guarantees (personal fields overwritten before soft delete, tokens
  purged), migrations that are safe on existing rows, and behaviour that holds on MySQL 8 and
  MariaDB 10.11 and not only on the SQLite test database.
- **Contract:** the change does what the intent and plan in the pull request description say,
  and nothing else. Public API responses keep the `{success, data, error, meta}` envelope. The
  published contract of live public forms (field names, select options, public ids) is unchanged
  unless the change says so explicitly.
- **Tests:** the change is proven by a test that fails without it. Assertions check real state;
  `expect(true)` or a skipped branch is not a test. New or changed behaviour brings its tests in
  the same pull request: Go tests for backend behaviour, Vitest for components, and an e2e spec
  (`make e2e`) for a user-visible flow. Destructive e2e steps act only on records the test created.

## What Important means here

Reserve **Important** for findings that would break behaviour, leak data, weaken an authorization
or spam check, break a live consumer of the public forms API, pass on SQLite while failing on
MySQL or MariaDB, weaken a CI or delivery gate (a check that can no longer fail, a skipped
test, a widened size limit), or add or change behaviour without the tests that prove it. Everything else is a **Nit**; keep Nits few.

## Skip

Generated files (`api/swagger.*`, lockfiles), formatting (no formatter is enforced in CI yet), and
the deliberate behaviours documented in the code (for example the success-shaped response for
spam submissions and the generic 401 for locked or deactivated accounts).

Mocks are not skipped: several under `internal/mocks/` are edited by hand despite their generated
header, and a wrong mock lets a test pass for the wrong reason.
