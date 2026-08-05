# Roadmap

Unprioritized ideas that are **not** implemented today. This list is the salvaged remainder of two
pre-implementation build checklists (`tasks.md` and `uitasks.md`) that were removed once the
features they planned had shipped; only items verified as still missing are kept here. Shipped
functionality and its test coverage are tracked in [FEATURES.md](FEATURES.md).

## Frontend

- **Kanban board for tasks** — drag-and-drop status board alongside the existing task list
- **Saved filters** — persist per-user filter presets on the list pages
- **Reports and analytics** — sales pipeline, lead conversion rate over time, ticket metrics,
  user activity
- **Data export** — CSV/PDF export from the list views
- **Accessibility pass** — keyboard navigation and screen reader review against WCAG 2.1 AA

## Backend and delivery

- **Serve the OpenAPI spec** — the spec is now generated from handler annotations into `api/`
  (`make swagger`); serving it (e.g. gin-swagger with a Swagger UI route) remains unimplemented
- **Containerization** — Dockerfile and compose setup for backend + frontend + MySQL
- **CI pipeline** — build, lint, unit tests, and E2E on pull requests

## Follow-ups from the backend build-out

The audit-defect list and its loose ends are done: dashboard role guard, sentinel-error
classification everywhere, sales read-only on tickets (backend and frontend), standardised
pagination, strict configuration typing, and — instead of deleting the frontend functions that
called missing routes — the routes now exist (auth session lifecycle, dashboard analytics, bulk
status updates, API-key management, customer export/assign, upcoming tasks). Still open:

- **UI pages for the new auth flows** — `authApi.changePassword`, `requestPasswordReset` and
  `resetPassword` are real endpoints now, but no page calls them; the reset email links to
  `/reset-password?token=...`, which has no route in the SPA yet.
- **Access-token blocklist** — logout/rotation revoke refresh tokens only; an issued JWT stays
  valid until expiry.
- **Concurrent-refresh stampede** — several simultaneous 401s can race the refresh interceptor;
  with strict rotation the losers get logged out. A shared in-flight refresh promise would fix it.
- **Production SMTP** — password-reset delivery falls back to a redacted log line unless
  `SMTP_*` is configured.
- **Sales ticket navigation** — the backend allows sales to read tickets, but the nav item is
  hidden from them (deep links work); widen the nav or narrow the read routes, a product call.
