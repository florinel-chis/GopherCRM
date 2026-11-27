# GopherCRM documentation

This page lists every document in `docs/`, grouped by reader, with one line on what each one
answers. Start with one of the three paths below. If you already know your role, go to its section.

To run GopherCRM, pick the deployment first:

| Deployment | When to choose it | Prerequisites | Guide |
|---|---|---|---|
| Local evaluation | trying GopherCRM on one machine, or developing it | Docker with Compose, or Go 1.25 and Node.js 22 | [DOCKER.md](DOCKER.md#sqlite-flavor-no-database-server), [SETUP.md](SETUP.md) |
| Production Compose | one host running the Compose stack behind your own TLS proxy | Docker Compose 2.24.4 or later, a TLS reverse proxy, the public URLs | [DOCKER.md](DOCKER.md#production-override-example) |
| Systemd server from a published release | a Linux server running a published release tarball on MySQL or MariaDB | systemd, MySQL 8 or MariaDB 10.11, nginx or Apache 2.4, root SSH access | [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) |
| Fork's automated release pipeline | your fork builds, publishes and deploys its own releases through the shipped workflows | a server provisioned as in SERVER_PROVISIONING.md, a `gh` login with admin rights, `main` protected | [FORKING.md](FORKING.md#52-release-tarball-and-systemd-through-the-shipped-workflows) |

## Start here

- **What can it do**: [CAPABILITIES.md](CAPABILITIES.md) describes, in plain language, what
  GopherCRM does in each area, from leads and deals to forms, automation and privacy.
- **Try it locally**: run the SQLite stack in
  [DOCKER.md](DOCKER.md#sqlite-flavor-no-database-server), a complete Compose stack with no database
  server, or follow [SETUP.md](SETUP.md) to run the backend and the frontend from source.
- **Deploy to a server**: [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) prepares a Linux server
  for the release-tarball deploy. The deployment shapes are compared in
  [FORKING.md section 5](FORKING.md#5-deployment-shapes), and DOCKER.md has a
  [production override example](DOCKER.md#production-override-example) for Compose.
- **Start developing**: [SETUP.md](SETUP.md) for the local setup, then
  [DEVELOPMENT.md](DEVELOPMENT.md) for the commands, the layering and the conventions the code
  base enforces.

## Using GopherCRM

- [user-guide/README.md](user-guide/README.md): the user guide index, for people who use the web
  app day to day and for the administrators who set it up for them.
- [user-guide/permissions-access/overview.md](user-guide/permissions-access/overview.md): the
  administrator guide: roles and permissions, single sign-on, API keys, the sign-in audit, webhooks
  and an FAQ.

## Running it: install and operate

- [SETUP.md](SETUP.md): setting up the backend and the frontend for local development.
- [DOCKER.md](DOCKER.md): running GopherCRM with Docker Compose, its Compose settings, the SQLite
  flavor and running Compose beyond localhost.
- [CONFIGURATION.md](CONFIGURATION.md): every backend environment variable, the secrets and what
  rotating them does, the public URLs, TLS, trusted proxies and CORS, and the build-time `VITE_*`
  values.
- [BACKUP_RESTORE.md](BACKUP_RESTORE.md): backing up and restoring the database, for MySQL or
  MariaDB and for SQLite, from source, with Compose and on a systemd server.
- [UPGRADING.md](UPGRADING.md): the upgrade order, the SQL migrations on MySQL and MariaDB and
  what each release changes in a SQLite file.
- [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md): preparing a fresh Linux server, once, before
  the first release-tarball deploy.
- [AEO_SECURITY.md](AEO_SECURITY.md): the outbound trust policy for AEO provider endpoints, the
  migration steps for operators and how provider failures are reported.
- [VALIDATION_RELEASE.md](VALIDATION_RELEASE.md): which checks a change runs, and how releases
  are validated and deployed.
- [user-guide/permissions-access/single-sign-on.md](user-guide/permissions-access/single-sign-on.md):
  single sign-on through OpenID Connect, with one identity provider at a time.
- [user-guide/permissions-access/how-to-microsoft-entra-id.md](user-guide/permissions-access/how-to-microsoft-entra-id.md):
  a walk-through that connects GopherCRM to Microsoft Entra ID.
- [user-guide/permissions-access/testing-single-sign-on.md](user-guide/permissions-access/testing-single-sign-on.md):
  verifying single sign-on against your own Microsoft Entra ID tenant before rolling it out.
- [user-guide/permissions-access/scim-provisioning.md](user-guide/permissions-access/scim-provisioning.md):
  GopherCRM as a SCIM 2.0 provider, so an identity provider can create, update and deactivate
  staff accounts.

## Forking and customising

- [FORKING.md](FORKING.md): running GopherCRM under your own name: the first run, rebranding,
  configuration, deployment, CI on a fork and staying in sync with upstream.
- [USE_CASES.md](USE_CASES.md): worked recipes for concrete deployments, with what the code does
  today and the gaps of each.

## Developing

- [SAMPLE_DATA.md](SAMPLE_DATA.md): generating a database filled with a fictional business for
  demos, screenshots and local development (`make sample-data`).
- [DEVELOPMENT.md](DEVELOPMENT.md): the developer guide: commands, layering, the package map and
  the conventions the code base enforces, with an index of the topic pages below.
- [DEV_TRANSACTIONS.md](DEV_TRANSACTIONS.md): how the transaction manager behaves on every driver,
  the service transaction unit and which entry points run on the request context.
- [DEV_AUTH.md](DEV_AUTH.md): password sign-in, sessions and refresh tokens, API keys, single
  sign-on, the SCIM provider and the web client session.
- [DEV_RBAC.md](DEV_RBAC.md): role assignment, custom roles, the role API and the guard that keeps
  at least one active admin.
- [DEV_ENTITIES.md](DEV_ENTITIES.md): the rules of the business entities, ordered and paginated
  reads, list filters and the bulk status endpoints.
- [DEV_ERASURE.md](DEV_ERASURE.md): what deleting a user, customer or lead erases and keeps, and
  the operator procedures.
- [DEV_RUNTIMES.md](DEV_RUNTIMES.md): the work that outlives a request: the AEO and automation
  runtimes, outbound webhooks and the form background work.
- [DEV_TESTING_CI.md](DEV_TESTING_CI.md): where the tests live and how they run, the CI jobs and
  their gates, and the documentation and API docs checks.
- [EXTENDING.md](EXTENDING.md): recipes for adding or removing an entity, a route, a migration, a
  settings key or a module.
- [CONCURRENCY.md](CONCURRENCY.md): the row-lock order and the retry rule of every write that
  locks rows of more than one table.
- [AUTHORIZATION.md](AUTHORIZATION.md): who may do what: the permission catalogue, the system role
  matrices, custom roles and the rules for role changes.
- [testing/README.md](testing/README.md): the test-case catalog, documenting the observed
  behaviour of every user-facing feature.
- [../gocrm-ui/e2e/README.md](../gocrm-ui/e2e/README.md): the Playwright end-to-end suite and how
  to run it against a real backend.
- [DOCUMENTATION_WORKFLOW.md](DOCUMENTATION_WORKFLOW.md): how documentation is assessed and
  updated in the same pull request as the behaviour it describes.

## Reference

- [../api/index.html](../api/index.html): the OpenAPI reference, readable offline in a browser.
- [../api/swagger.yaml](../api/swagger.yaml) and [../api/swagger.json](../api/swagger.json): the
  same Swagger 2.0 specification as YAML and JSON.
- [FEATURES.md](FEATURES.md): the test-coverage matrix for contributors: every feature, its tests,
  known gaps and issues.
- [datamodel.md](datamodel.md): the tables the migrations and the SQLite schema step create, with
  the rules the backend applies to them.
- [SCREENSHOTS.md](SCREENSHOTS.md): reference images of every user-facing screen.
- [../CHANGELOG.md](../CHANGELOG.md): what changed in each release, and what is not released yet.
- [ROADMAP.md](ROADMAP.md): ideas that are not implemented today.
- [KNOWN_LIMITATIONS.md](KNOWN_LIMITATIONS.md): what the shipped code does not do, or does only under
  conditions: token lifetime, sessions, mail without SMTP, replicas, rate limits and retention.

## Historical material (not current)

`docs/plans/` and `docs/specs/` are local working papers. They are not part of the repository,
they are never committed and they are not current, so a checkout does not contain them. The
current behaviour is described in the pages above.
