# GopherCRM documentation

This page lists every document in `docs/`, grouped by audience folder, with one line on what each
one answers: using the web app, administering it, running it, integrating another system with it,
contributing to the code, and maintaining releases.

To run GopherCRM, pick the deployment first:

| Deployment | When to choose it | Prerequisites | Guide |
|---|---|---|---|
| Local evaluation | trying GopherCRM on one machine, or developing it | Docker with Compose, or Go 1.25 and Node.js 22 | [operations/docker.md](operations/docker.md#sqlite-flavor-no-database-server), [development/local-setup.md](development/local-setup.md) |
| Production Compose | one host running the Compose stack behind your own TLS proxy | Docker Compose 2.24.4 or later, a TLS reverse proxy, the public URLs | [operations/docker.md](operations/docker.md#production-override-example) |
| Systemd server from a published release | a Linux server running a published release tarball on MySQL or MariaDB | systemd, MySQL 8 or MariaDB 10.11, nginx or Apache 2.4, root SSH access | [operations/systemd.md](operations/systemd.md) |
| Fork's automated release pipeline | your fork builds, publishes and deploys its own releases through the shipped workflows | a server provisioned as in operations/systemd.md, a `gh` login with admin rights, `main` protected | [development/forking.md](development/forking.md#52-release-tarball-and-systemd-through-the-shipped-workflows) |

## Use GopherCRM

- [User guide](user-guide/README.md): the user guide index, for people who use the web app day to
  day and for the administrators who set it up for them.
- [Changelog](../CHANGELOG.md): what changed in each release, and what is not released yet.

## Administration

- [Administrator guide to permissions and access](administration/overview.md):
  roles and permissions, single sign-on, API keys, the sign-in audit, webhooks and an FAQ.
- [Roles and permissions](administration/roles-and-permissions.md): the system roles, custom roles and
  what each permission opens.
- [Settings and configuration](administration/settings-and-configuration.md): the administration
  screens and the settings an administrator can change in the web app.
- [Single sign-on](administration/single-sign-on.md): single sign-on through OpenID
  Connect, with one identity provider at a time.
- [Microsoft Entra ID how-to](administration/how-to-microsoft-entra-id.md): a
  walk-through that connects GopherCRM to Microsoft Entra ID.
- [Testing single sign-on](administration/testing-single-sign-on.md): verifying
  single sign-on against your own Microsoft Entra ID tenant before rolling it out.
- [SCIM provisioning](administration/scim-provisioning.md): GopherCRM as a SCIM 2.0
  provider, so an identity provider can create, update and deactivate staff accounts.
- [Sign-in audit](administration/sign-in-audit.md): opening the sign-in audit, what each column holds,
  how long rows are kept, and the methods and reason codes.
- [Permissions and access FAQ](administration/faq.md): short answers about single sign-on and
  passwords, the last administrator, API key roles and deleting single sign-on users.

## Operations

- [Docker Compose guide](operations/docker.md): running GopherCRM with Docker Compose, its Compose settings,
  the SQLite flavor and running Compose beyond localhost.
- [Server provisioning](operations/systemd.md): preparing a fresh Linux server, once, before the
  first release-tarball deploy.
- [Backup and restore](operations/backup-and-restore.md): backing up and restoring the database, for MySQL or
  MariaDB and for SQLite, from source, with Compose and on a systemd server.
- [Upgrading](operations/upgrades.md): the upgrade order, the SQL migrations on MySQL and MariaDB and what
  each release changes in a SQLite file.
- [AEO outbound security](operations/aeo-egress.md): the outbound trust policy for AEO provider endpoints,
  the migration steps for operators and how provider failures are reported.
- [Reverse proxy and TLS](operations/systemd.md#reverse-proxy-and-tls): nginx or Apache in front of the
  backend and the web app, with TLS.
- [Email](reference/configuration.md#mail-smtp): the SMTP settings for outgoing mail.
- [Secrets](reference/configuration.md#secrets-and-rotation): the secrets and what rotating them does.
- [Troubleshooting](development/local-setup.md#common-issues): common issues and their fixes.

## Integrations

- [First API request](integrations/README.md): signing in and calling the API with a token or an
  API key.
- [OpenAPI reference](../api/index.html): the API reference, readable offline in a browser. The
  same Swagger 2.0 specification is in [swagger.yaml](../api/swagger.yaml) and
  [swagger.json](../api/swagger.json).
- [API keys](integrations/api-keys.md): creating and revoking API keys for
  scripts and integrations.
- [Webhooks](integrations/webhooks.md): outbound webhooks that tell another
  system when a record changes in GopherCRM.

## Development

- [Developer guide](development/README.md): commands, layering, the package map and the conventions the
  code base enforces, with an index of the topic pages below.
- [Local setup from source](development/local-setup.md): setting up the backend and the frontend for local
  development.
- [Authorization reference](development/authorization.md): who may do what: the permission catalogue, the
  system role matrices, custom roles and the rules for role changes.
- [Transactions](development/transactions.md): how the transaction manager behaves on every driver, the
  service transaction unit and which entry points run on the request context.
- [Authentication internals](development/auth.md): password sign-in, sessions and refresh tokens, API keys,
  single sign-on, the SCIM provider and the web client session.
- [Roles internals](development/roles.md): role assignment, custom roles, the role API and the guard that
  keeps at least one active admin.
- [Entity rules](development/entities.md): tasks and tickets, leads and companies with their lock
  order, and the bulk status endpoints.
- [Quotes](development/quotes.md): quotes, their links, the quote PDF and its smoke recipe.
- [Mail](development/mail.md): the mail transport, ticket comment email and the mail product name.
- [Reads and filters](development/reads-and-filters.md): ordered and paginated reads, list filters and
  lookup indexes.
- [Erasure internals](development/erasure.md): what deleting a user, customer or lead erases and keeps, and
  the operator procedures.
- [Background runtimes](development/background-jobs.md): the work that outlives a request, with one page
  per runtime.
- [AEO runtime](development/aeo-runtime.md): the AEO background work, its shutdown, and the runtime
  validation and candidate inspection.
- [Automation runtime](development/automation-runtime.md): the follow-up automation worker.
- [Outbound webhooks](development/outbound-webhooks.md): outbound webhook delivery.
- [Form background work](development/form-background-work.md): the form retention purge, form mail,
  form attribution and form ticket intake.
- [Testing and CI](development/testing.md): where the tests live and how they run, the CI jobs and their
  gates, the documentation screenshots, and the documentation and API docs checks.
- [Extending recipes](development/extending.md): adding or removing an entity, a route, a migration, a settings
  key or a module.
- [Concurrency and lock order](development/concurrency.md): the row-lock order and the retry rule of every write
  that locks rows of more than one table.
- [Forking guide](development/forking.md): running GopherCRM under your own name: the first run, rebranding,
  configuration, deployment, CI on a fork and staying in sync with upstream.
- [Sample data](development/sample-data.md): generating a database filled with a fictional business for demos,
  screenshots and local development (`make sample-data`).
- [Documentation workflow](development/documentation.md): how documentation is assessed and updated in
  the same pull request as the behaviour it describes.
- [End-to-end suite](../gocrm-ui/e2e/README.md): the Playwright end-to-end suite and how to run it
  against a real backend.

## Maintainers

- [Validation and release](maintainers/validation-and-releases.md): which checks a change runs, and how releases are
  validated and deployed.

## Reference

- [Capabilities overview](reference/capabilities.md): what GopherCRM does in each area, in plain language,
  from leads and deals to forms, automation and privacy.
- [Configuration reference](reference/configuration.md): every backend environment variable, the secrets and
  what rotating them does, the public URLs, TLS, trusted proxies and CORS, and the build-time
  `VITE_*` values.
- [Data model](reference/data-model.md): the tables the migrations and the SQLite schema step create, with the
  rules the backend applies to them.
- [Known limitations](reference/limitations.md): what the shipped code does not do, or does only under
  conditions: token lifetime, sessions, mail without SMTP, replicas, rate limits and retention.
- [Roadmap](reference/roadmap.md): ideas that are not implemented today.
- [Feature and test-coverage matrix](reference/features.md): every feature, its tests, known gaps and issues.
- [Use-case recipes](reference/use-cases.md): worked recipes for concrete deployments, with what the code
  does and the gaps of each.
- [Screenshots](reference/screenshots.md): reference images of every user-facing screen.

## Testing

- [Test-case catalog](testing/README.md): the observed behaviour of every user-facing feature.
