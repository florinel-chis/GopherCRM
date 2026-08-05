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

## API defects found while annotating the handlers

Surfaced by the swagger-annotation audit (every claim below was traced to the code path); the
generated spec documents the behaviour as it is, not as intended. Each needs a small fix:

- **`GET /dashboard/stats` has no role guard** — the only route group without `RequireRole`; a
  self-registered `customer` account can read system-wide lead/customer/ticket/task counts that
  `GET /customers` refuses to expose to the same role
- **`DELETE /api-keys/{id}` on a missing key returns 500, not 404** — the handler compares
  `err.Error() == "api key not found"`, a string nothing produces (the repo returns
  `gorm.ErrRecordNotFound`); dead branch kept green by mock-driven tests
- **`POST /configurations/{key}/reset` on an unknown key returns 500, not 404** — the service
  returns the raw repo error while the handler string-matches `"configuration not found: <key>"`,
  which only `Set` wraps
- **`PUT /users/{id}` and `PUT /users/me` return 500 for a nonexistent user** — only
  `ErrDuplicateEmail` is classified; not-found falls into the internal-error branch
  (`DELETE /users/{id}` classifies it correctly)
- **Reads that swallow real errors as 404** — `GET /customers/{id}`, `GET /tickets/{id}`,
  `GET /tasks/{id}`, `DELETE /tickets/{id}` and `DELETE /tasks/{id}` map every service error to
  "not found", so a database failure is indistinguishable from a missing record
- **Sales role has unrestricted ticket read/write** — `GET`/`PUT /tickets/{id}` special-case only
  customer and support, so sales falls through to full access including reassignment
- **`PUT /configurations/{key}` cannot set falsy values** — `Value interface{}` with
  `binding:"required"` rejects `false`, `0` and `""`, so a boolean config cannot be turned off
- **Tasks paginate differently from every other list** — `page`/`per_page` via
  `ParsePaginationParams` (out-of-range falls back to 20) instead of `offset`/`limit` via
  `ParseOffsetLimit` (caps at 100)
- **`remember_me` on login is accepted but ignored** — token lifetime always comes from
  `JWT_EXPIRY_HOURS`
- **String-compared errors** in the API-key and configuration handlers violate the project's
  sentinel-error rule and caused two of the defects above
