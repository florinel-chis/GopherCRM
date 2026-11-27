# GopherCRM documentation

This page lists every document in `docs/`, grouped by what you want to do, with one line on what
each one answers. Pick the route that matches your task: using the web app, installing and running
it, integrating another system with it, or contributing to the code.

To run GopherCRM, pick the deployment first:

| Deployment | When to choose it | Prerequisites | Guide |
|---|---|---|---|
| Local evaluation | trying GopherCRM on one machine, or developing it | Docker with Compose, or Go 1.25 and Node.js 22 | [DOCKER.md](DOCKER.md#sqlite-flavor-no-database-server), [SETUP.md](SETUP.md) |
| Production Compose | one host running the Compose stack behind your own TLS proxy | Docker Compose 2.24.4 or later, a TLS reverse proxy, the public URLs | [DOCKER.md](DOCKER.md#production-override-example) |
| Systemd server from a published release | a Linux server running a published release tarball on MySQL or MariaDB | systemd, MySQL 8 or MariaDB 10.11, nginx or Apache 2.4, root SSH access | [SERVER_PROVISIONING.md](SERVER_PROVISIONING.md) |
| Fork's automated release pipeline | your fork builds, publishes and deploys its own releases through the shipped workflows | a server provisioned as in SERVER_PROVISIONING.md, a `gh` login with admin rights, `main` protected | [FORKING.md](FORKING.md#52-release-tarball-and-systemd-through-the-shipped-workflows) |

## Use GopherCRM

- [Capabilities overview](CAPABILITIES.md): what GopherCRM does in each area, in plain language,
  from leads and deals to forms, automation and privacy.
- [User guide](user-guide/README.md): the user guide index, for people who use the web app day to
  day and for the administrators who set it up for them.
- [Administrator guide to permissions and access](user-guide/permissions-access/overview.md):
  roles and permissions, single sign-on, API keys, the sign-in audit, webhooks and an FAQ.
- [Screenshots](SCREENSHOTS.md): reference images of every user-facing screen.
- [Known limitations](KNOWN_LIMITATIONS.md): what the shipped code does not do, or does only under
  conditions: token lifetime, sessions, mail without SMTP, replicas, rate limits and retention.
- [Roadmap](ROADMAP.md): ideas that are not implemented today.
- [Changelog](../CHANGELOG.md): what changed in each release, and what is not released yet.

## Install and operate

- [Local setup from source](SETUP.md): setting up the backend and the frontend for local
  development.
- [Docker Compose guide](DOCKER.md): running GopherCRM with Docker Compose, its Compose settings,
  the SQLite flavor and running Compose beyond localhost.
- [Configuration reference](CONFIGURATION.md): every backend environment variable, the secrets and
  what rotating them does, the public URLs, TLS, trusted proxies and CORS, and the build-time
  `VITE_*` values.
- [Backup and restore](BACKUP_RESTORE.md): backing up and restoring the database, for MySQL or
  MariaDB and for SQLite, from source, with Compose and on a systemd server.
- [Upgrading](UPGRADING.md): the upgrade order, the SQL migrations on MySQL and MariaDB and what
  each release changes in a SQLite file.
- [Server provisioning](SERVER_PROVISIONING.md): preparing a fresh Linux server, once, before the
  first release-tarball deploy.
- [AEO outbound security](AEO_SECURITY.md): the outbound trust policy for AEO provider endpoints,
  the migration steps for operators and how provider failures are reported.
- [Forking guide](FORKING.md): running GopherCRM under your own name: the first run, rebranding,
  configuration, deployment, CI on a fork and staying in sync with upstream.
- [Use-case recipes](USE_CASES.md): worked recipes for concrete deployments, with what the code
  does and the gaps of each.
- [Single sign-on](user-guide/permissions-access/single-sign-on.md): single sign-on through OpenID
  Connect, with one identity provider at a time.
- [Microsoft Entra ID how-to](user-guide/permissions-access/how-to-microsoft-entra-id.md): a
  walk-through that connects GopherCRM to Microsoft Entra ID.
- [Testing single sign-on](user-guide/permissions-access/testing-single-sign-on.md): verifying
  single sign-on against your own Microsoft Entra ID tenant before rolling it out.

## Integrate

- [OpenAPI reference](../api/index.html): the API reference, readable offline in a browser. The
  same Swagger 2.0 specification is in [swagger.yaml](../api/swagger.yaml) and
  [swagger.json](../api/swagger.json).
- [SCIM provisioning](user-guide/permissions-access/scim-provisioning.md): GopherCRM as a SCIM 2.0
  provider, so an identity provider can create, update and deactivate staff accounts.
- [API keys](user-guide/permissions-access/api-keys.md): creating and revoking API keys for
  scripts and integrations.
- [Webhooks](user-guide/permissions-access/webhooks.md): outbound webhooks that tell another
  system when a record changes in GopherCRM.
- [Authorization reference](AUTHORIZATION.md): who may do what: the permission catalogue, the
  system role matrices, custom roles and the rules for role changes.

## Contribute

- [Developer guide](DEVELOPMENT.md): commands, layering, the package map and the conventions the
  code base enforces, with an index of the topic pages below.
- [Transactions](DEV_TRANSACTIONS.md): how the transaction manager behaves on every driver, the
  service transaction unit and which entry points run on the request context.
- [Authentication internals](DEV_AUTH.md): password sign-in, sessions and refresh tokens, API keys,
  single sign-on, the SCIM provider and the web client session.
- [Roles internals](DEV_RBAC.md): role assignment, custom roles, the role API and the guard that
  keeps at least one active admin.
- [Entity rules](DEV_ENTITIES.md): the rules of the business entities, ordered and paginated reads,
  list filters and the bulk status endpoints.
- [Erasure internals](DEV_ERASURE.md): what deleting a user, customer or lead erases and keeps, and
  the operator procedures.
- [Background runtimes](DEV_RUNTIMES.md): the work that outlives a request: the AEO and automation
  runtimes, outbound webhooks and the form background work.
- [Testing and CI](DEV_TESTING_CI.md): where the tests live and how they run, the CI jobs and their
  gates, the documentation screenshots, and the documentation and API docs checks.
- [Extending recipes](EXTENDING.md): adding or removing an entity, a route, a migration, a settings
  key or a module.
- [Concurrency and lock order](CONCURRENCY.md): the row-lock order and the retry rule of every write
  that locks rows of more than one table.
- [Sample data](SAMPLE_DATA.md): generating a database filled with a fictional business for demos,
  screenshots and local development (`make sample-data`).
- [Feature and test-coverage matrix](FEATURES.md): every feature, its tests, known gaps and issues.
- [Data model](datamodel.md): the tables the migrations and the SQLite schema step create, with the
  rules the backend applies to them.
- [Test-case catalog](testing/README.md): the observed behaviour of every user-facing feature.
- [End-to-end suite](../gocrm-ui/e2e/README.md): the Playwright end-to-end suite and how to run it
  against a real backend.
- [Validation and release](VALIDATION_RELEASE.md): which checks a change runs, and how releases are
  validated and deployed.
- [Documentation workflow](DOCUMENTATION_WORKFLOW.md): how documentation is assessed and updated in
  the same pull request as the behaviour it describes.
