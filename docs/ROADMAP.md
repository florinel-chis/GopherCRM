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

- **Serve the OpenAPI spec** — either wire a Swagger UI route and add a regeneration step, or drop
  the unused generated `docs/` package and its `swaggo/swag` dependency
- **Containerization** — Dockerfile and compose setup for backend + frontend + MySQL
- **CI pipeline** — build, lint, unit tests, and E2E on pull requests
