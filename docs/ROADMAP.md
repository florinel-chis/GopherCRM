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

## Loose ends from the API-defect fix round

The defect list surfaced by the swagger-annotation audit has been fixed (dashboard role guard,
sentinel-error classification across api-keys/configurations/users/customers/tickets/tasks,
sales made read-only on tickets, task pagination standardised on `offset`/`limit` + `page`,
dead `remember_me` removed). One item on that list turned out not to reproduce: falsy
configuration values (`false`, `0`, `""`) were already accepted — the validator's interface
handling treats a non-nil zero value as present — but the handler now uses `json.RawMessage`
so present-vs-absent is structural rather than a version-sensitive validator quirk. What remains:

- **Frontend ticket routes are not role-gated** — the Tickets nav item is hidden from sales, but
  `/tickets/:id/edit` is deep-linkable and `TicketDetail` renders Edit/Delete buttons
  unconditionally; a sales user now gets a 403 toast on save. Gate the routes or buttons by role.
- **`models.Configuration.SetValue` silently coerces type mismatches** — a non-bool sent to a
  boolean config becomes `"false"`, a non-string to a string config becomes `""`, instead of
  erroring.
- **Dead frontend API functions call routes that do not exist** — `tasksApi` hits
  `/tasks/upcoming` and `dashboardApi` hits `/dashboard/upcoming-tasks`; neither route is
  registered, both would 404. No page calls them today — implement the routes or delete the
  functions.
