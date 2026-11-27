# GopherCRM user guide

This guide is for people who use the GopherCRM web app day to day, and for the administrators who
set it up for them. Installation, development and the API are described elsewhere: the
[setup walkthrough](../development/local-setup.md), the [developer guide](../development/README.md) and the
[documentation index](../README.md), which lists the API reference.

## Everyday work

- [Getting started](getting-started.md): signing in to an existing installation, the main screens
  and what to do first in each role.
- [Everyday sales work](sales-walkthrough.md): from a new lead through conversion, a company and a
  deal to a quote you print or download as a PDF.
- [Everyday support work](support-walkthrough.md): opening a ticket, comments and internal notes,
  and moving it through to its resolution.
- [Tasks and labels](tasks-and-labels.md): creating and working on tasks, and grouping and
  filtering them with labels.

## Settings

- [Settings and configuration](../administration/settings-and-configuration.md): the administration screens and the
  permission each needs, the configuration tabs, and the lead conversion settings.

## Forms

- [Forms](forms.md): what a form submission creates, and how a form that opens support tickets
  titles them with an answer (`ticket_title_field`).

## Answer engine visibility

- [Answer Engine Optimization (AEO)](aeo.md): tracking how answer engines mention your brand,
  provider keys, the egress policy, the daily schedule and what a run costs.

## Integrations

- [Webhooks](../integrations/webhooks.md): signed requests to another system when a record is
  created, updated or deleted.
- [API keys](../integrations/api-keys.md): creating, using and revoking your own API keys.

## Permissions and access

- [Overview](../administration/overview.md): the administrator guide to who may do what.
- [Roles and permissions](../administration/roles-and-permissions.md): the system roles, custom
  roles and the role audit.
- [Single sign-on](../administration/single-sign-on.md): signing in through an OpenID Connect
  identity provider.
- [SCIM provisioning](../administration/scim-provisioning.md): letting an identity provider
  create, update and deactivate accounts.
- [Sign-in audit](../administration/sign-in-audit.md): the record of sign-in outcomes.

## Privacy

- [Privacy and erasure](privacy-and-erasure.md): what deleting a user, customer or lead erases,
  and what is kept.
