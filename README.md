# GopherCRM

A self-hosted CRM for small teams: sales and support in one app, with lead-capture forms built in.
Go backend, React UI, SQLite or MySQL.

[![CI](https://github.com/florinel-chis/GopherCRM/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/florinel-chis/GopherCRM/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/florinel-chis/GopherCRM)](https://github.com/florinel-chis/GopherCRM/releases/latest)

**Who it's for:** small sales and support teams that want leads, deals and tickets in one place, on
a server they control, without per-seat licence fees.

![GopherCRM dashboard](docs/img/gophercrm-dashboard.png)

## Features

- **Sales in one place**: track leads, convert them into customers, group people under companies
  and follow every deal through a five-stage pipeline, with its full stage history.
- **Quotes your customers can read**: a product catalogue, quotes with tax and totals worked out
  for you, a printable page and a quote email, from draft to accepted or rejected.
- **Support that stays in context**: tickets linked to the customer, with replies and internal
  notes, and tasks with labels.
- **Leads from your website**: forms you build in the CRM and embed with one script tag, with
  built-in spam protection and an optional double opt-in; a form can also open a support ticket.
- **Follow-ups that run themselves**: rules that act on lead, deal and ticket events or on a daily
  schedule, with a dry run to see what they would do first.
- **Control over who sees what**: four system roles plus your own custom roles, single sign-on
  through OpenID Connect, SCIM provisioning of staff accounts, API keys, signed webhooks and audit
  logs of sign-ins and role changes.
- **Know whether AI assistants mention you**: daily reports of how Anthropic, OpenAI, Gemini, Kimi,
  Perplexity or any OpenAI-compatible endpoint answer about your brand.
- **Your data on your server**: one Go binary and a static web UI on SQLite, MySQL or MariaDB;
  deleting a person erases their personal data (GDPR Art. 17), with account lockout and rate
  limiting built in.

The full list, with test coverage and known gaps, is in [docs/FEATURES.md](docs/FEATURES.md).

## Quick start (SQLite, no database server)

Needs Docker with Compose and free ports 3000 and 8080.

```bash
git clone https://github.com/florinel-chis/GopherCRM.git
cd GopherCRM
[ -f .env ] || printf 'JWT_SECRET=%s\nAPI_KEY_SECRET=%s\n' "$(openssl rand -base64 32)" "$(openssl rand -base64 32)" > .env
docker compose -f docker-compose.sqlite.yml up -d --build
docker compose -f docker-compose.sqlite.yml exec backend create-admin -non-interactive \
  -email admin@example.com -name "Admin" -password 'ChangeMe!2345'
```

The third line writes two random secrets, `JWT_SECRET` and `API_KEY_SECRET`, to `.env` in the
checkout, and only when no `.env` exists yet. Compose reads that file; it is gitignored and never
copied into an image. Keep it across restarts and upgrades: a new `JWT_SECRET` signs everyone out,
and a new `API_KEY_SECRET` invalidates the API keys, the stored AI provider keys and the webhook
signing secrets.

Open http://localhost:3000 and sign in as `admin@example.com` / `ChangeMe!2345`. That password is a
local demo value: change it at once under **Settings > Profile**. Port 3000 taken? Put
`UI_PORT=3001` in front of the `up` command and open http://localhost:3001 instead.

To run from source instead, start from `.env.example`. If there is no `.env` yet, copy
`.env.example` to `.env` and replace the example `JWT_SECRET`: `Load` (`internal/config/config.go`)
refuses the placeholder at startup. If the quick start already wrote `.env`, do not copy over it:
add the `.env.example` settings you need to it and keep its two secrets. The walkthrough is
[docs/SETUP.md](docs/SETUP.md).

The full documentation is listed in the [Documentation](#documentation) section below.

If it's useful, a star helps others find it.

## Running with Docker (MySQL)

The whole stack (MySQL, API, UI) runs in containers, with database data persisted in a named
volume across restarts:

```bash
[ -f .env ] || printf 'JWT_SECRET=%s\nAPI_KEY_SECRET=%s\n' "$(openssl rand -base64 32)" "$(openssl rand -base64 32)" > .env
docker compose up -d --build
docker compose exec backend create-admin          # first admin account
```

The first line is the quick start's `.env` step: it writes the two secrets once, and the same file
has to be kept across restarts and upgrades.

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

The API reference is generated into `api/`. [api/index.html](api/index.html) is a static page that
works offline; GitHub shows its source, so download it or open it from a checkout.
[api/swagger.yaml](api/swagger.yaml) and [api/swagger.json](api/swagger.json) are the Swagger 2.0
spec, for client generators. Conventions and regeneration are in
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md#api-reference).

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

- Questions and bugs: open an issue at https://github.com/florinel-chis/GopherCRM/issues.
- Security problems: report them as [SECURITY.md](SECURITY.md) describes.
- Documentation: the index is [docs/README.md](docs/README.md).
