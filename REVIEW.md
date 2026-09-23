# Review policy

Every change reaches `main` through a pull request with green CI and the code owner's approval.
Reviewers, human or automated, work through the passes below and label each finding
**Important** or **Nit**. A finding never approves or blocks a pull request on its own; the code
owner decides.

## Passes

- **Bugs:** logic errors, broken edge cases, regressions, error paths that turn a failure into a
  success or a "not found".
- **Security:** authentication and role checks (admin, sales, support, customer), input
  validation, sort and filter allowlists, secrets or personal data in logs or responses, the
  public forms surface (`/forms/public/*`: CORS, rate limits, spam layers).
- **Data:** erasure keeps its guarantees (personal fields overwritten before soft delete, tokens
  purged), migrations that are safe on existing rows, and behaviour that holds on MySQL 8 and
  MariaDB 10.11 and not only on the SQLite test database.
- **Contract:** the change matches its plan. Public API responses keep the `{success, data, error,
  meta}` envelope. The published contract of live public forms (field names, select options,
  public ids) is unchanged unless the change says so explicitly.
- **Tests:** the change is proven by a test that fails without it. Assertions check real state;
  `expect(true)` or a skipped branch is not a test.

## What Important means here

Reserve **Important** for findings that would break behaviour, leak data, weaken an authorization
or spam check, break a live consumer of the public forms API, or pass on SQLite while failing on
MySQL or MariaDB. Everything else is a **Nit**; keep Nits few.

## Skip

Generated files (`api/swagger.*`, `internal/mocks/`, lockfiles), formatting that the linters
already enforce, and the deliberate behaviours documented in the code (for example the
success-shaped response for spam submissions and the generic 401 for locked or deactivated
accounts).
