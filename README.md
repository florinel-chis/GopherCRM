# GopherCRM

A self-hosted CRM for small teams: sales and support in one app, with lead-capture forms built in.
Go backend, React UI, SQLite or MySQL.

[![CI](https://github.com/florinel-chis/GopherCRM/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/florinel-chis/GopherCRM/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/florinel-chis/GopherCRM)](https://github.com/florinel-chis/GopherCRM/releases/latest)

**Who it's for:** small sales and support teams that want leads, deals and tickets in one place, on
a server they control, without per-seat licence fees.

- **A complete CRM.** Leads that convert into customers, companies, deals on a five-stage pipeline
  with a full stage history, support tickets, tasks with labels, four fixed system roles (Admin,
  Sales, Support, Customer) plus custom roles managed in Settings → Roles.
- **Access control and sign-in.** Custom roles built from the permission catalogue, single sign-on
  through OpenID Connect (for example Microsoft Entra ID or Okta), SCIM 2.0 provisioning of staff
  accounts, signed outbound webhooks, and the sign-in and role audit logs.
- **Lead capture built in.** Build forms in the CRM and embed them on any website with one script
  tag. Submissions become leads, behind layered spam protection (time trap, origin allowlist,
  honeypot, rate limits, optional reCAPTCHA v3) and an optional double opt-in. A double opt-in
  form can instead open a support ticket for a known customer.
- **Easy to run.** A single Go API binary and a static React UI. SQLite needs no database server;
  MySQL 8 and MariaDB 10.11 are tested in CI. Docker Compose files ship for both.
- **AI-visibility reports (AEO).** See whether Anthropic, OpenAI, Gemini, Kimi, Perplexity or any
  OpenAI-compatible endpoint mentions your brand when buyers ask.

![GopherCRM dashboard](docs/img/gophercrm-dashboard.png)

## Features

- **Leads and customers**: lead tracking with conversion to customers, and companies that leads and
  customers link to, with domain de-duplication.
- **Deals**: a five-stage pipeline with integer money per currency, per-stage probabilities and an
  append-only stage history.
- **Products and quotes**: a product catalogue and quotes with per-line tax, server-computed totals,
  a draft → sent → accepted/rejected/expired lifecycle, a print page and a quote email.
- **Tickets and tasks**: support tickets with comments and internal notes, tasks with labels.
- **Forms**: lead-capture and contact forms embedded on any website with one script tag, with
  layered spam protection and an optional double opt-in.
- **Follow-up automation**: admin-defined rules on lead, deal and ticket events or a daily
  schedule, run as persisted runs with a dry run.
- **Access control**: four system roles plus custom roles built from the permission catalogue
  ([docs/AUTHORIZATION.md](docs/AUTHORIZATION.md)), single sign-on through OpenID Connect, SCIM 2.0
  provisioning, API keys and the sign-in and role audit logs.
- **Webhooks**: signed outbound events with a delivery log, a test ping, secret rotation and
  redelivery.
- **AEO**: daily visibility, share-of-voice and citation reports across Anthropic, OpenAI, Gemini,
  Kimi, Perplexity and any OpenAI-compatible endpoint.
- **Privacy and security**: deleting a person erases their personal data (GDPR Art. 17); account
  lockout, password complexity and rate limiting.

The full list, with test coverage and known gaps, is in [docs/FEATURES.md](docs/FEATURES.md).

## Quick start (SQLite, no database server)

Needs Docker with Compose and free ports 3000 and 8080.

```bash
git clone https://github.com/florinel-chis/GopherCRM.git
cd GopherCRM
export JWT_SECRET="$(openssl rand -base64 32)"
docker compose -f docker-compose.sqlite.yml up -d --build
docker compose -f docker-compose.sqlite.yml exec backend create-admin -non-interactive \
  -email admin@example.com -name "Admin" -password 'ChangeMe!2345'
```

Open http://localhost:3000 and sign in as `admin@example.com` / `ChangeMe!2345`, then change the
password under **Settings > Profile**. Port 3000 taken? Put `UI_PORT=3001` in front of the `up`
command and open http://localhost:3001 instead.

To run from source instead, copy `.env.example` to `.env` and replace the example `JWT_SECRET`
first: `Load` (`internal/config/config.go`) refuses the placeholder at startup. The walkthrough is
[docs/SETUP.md](docs/SETUP.md).

The full documentation is listed in the [Documentation](#documentation) section below.

If it's useful, a star helps others find it.

## Running with Docker (MySQL)

The whole stack (MySQL, API, UI) runs in containers, with database data persisted in a named
volume across restarts:

```bash
export JWT_SECRET="$(openssl rand -base64 32)"   # or put it in .env
docker compose up -d --build
docker compose exec backend create-admin          # first admin account
```

UI at http://localhost:3000, API at http://localhost:8080/api/v1. Configuration, persistence and
the SQLite flavor are in [docs/DOCKER.md](docs/DOCKER.md).

## Deploying

To run GopherCRM on a server of your own, [docs/SERVER_PROVISIONING.md](docs/SERVER_PROVISIONING.md)
prepares a fresh Linux server (systemd, MySQL or MariaDB) for the release-tarball deploy, and its
[Reverse proxy and TLS](docs/SERVER_PROVISIONING.md#reverse-proxy-and-tls) section sets up nginx or
Apache 2.4 in front of it. To run it under your own name, with your own branding, configuration and
CI, start with [docs/FORKING.md](docs/FORKING.md).

## Documentation

- [docs/README.md](docs/README.md): the full documentation index, grouped by reader.
- [docs/SETUP.md](docs/SETUP.md): backend and frontend setup from source.
- [docs/DOCKER.md](docs/DOCKER.md): Docker Compose with MySQL or SQLite, backups and upgrades.
- [docs/FORKING.md](docs/FORKING.md): running your own copy, from the first run to staying in sync
  with upstream.
- [docs/user-guide/README.md](docs/user-guide/README.md): the user guide and the administrator
  guide.
- [docs/KNOWN_LIMITATIONS.md](docs/KNOWN_LIMITATIONS.md): what the shipped code does not do yet.
- [api/index.html](api/index.html): the offline API reference.
- [Releases](https://github.com/florinel-chis/GopherCRM/releases): published releases and their
  artifacts.

## API Documentation

All application routes are mounted under `/api/v1` (configurable via `API_PREFIX`). Every endpoint
under the prefix answers with the unified envelope `{ success, data, error, meta }`: `data` holds
the result, `error` the code and message of a refusal, and `meta` the pagination of a list.

The complete reference, with every operation's roles, parameters, side effects, error codes and
schemas, is generated into `api/`:

- `api/index.html`: a static page that works offline, straight from the checkout;
- `api/swagger.yaml` and `api/swagger.json`: the OpenAPI (Swagger 2.0) spec, for client
  generators.

`make swagger` regenerates all three from the handler annotations. How the spec is built is
described in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#api-reference); `make verify-api-docs`
checks it (see [docs/DEV_TESTING_CI.md](docs/DEV_TESTING_CI.md#api-docs-gate)).

The SCIM 2.0 server at `/scim/v2` is not part of the spec; see
[docs/user-guide/permissions-access/scim-provisioning.md](docs/user-guide/permissions-access/scim-provisioning.md).

`GET /health` is served outside the API prefix, needs no authentication and is not in the spec.
Its `webhooks` field is `{"state": "running"}`, or `{"state": "paused", "reason": "secret_unavailable"}`
while the webhook sender holds the deliveries of an endpoint whose signing secret cannot be
opened; the top-level `status` stays `healthy` and the answer stays 200 either way.


## Contributing

Read [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) first — it documents the layering and the patterns
the codebase enforces (unified response envelope, sentinel errors, sort allowlists, role assignment
rules, erasure-on-delete). Then:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

GopherCRM is released under the MIT License. See [LICENSE](LICENSE).

## Support

For support and questions:
- Create an issue in the GitHub repository
- Check the documentation index in [docs/README.md](docs/README.md)
- Review the configuration settings for system behavior customization
