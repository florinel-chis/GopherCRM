# GopherCRM Screenshot Catalog

Reference images of every user-facing screen in the React frontend (`gocrm-ui/`). Each image was
captured from the running application — a MySQL-backed backend and the Vite dev server — by the
Playwright suite in `gocrm-ui/e2e/screenshots/`, at a 1440x900 viewport with `deviceScaleFactor: 2`.
41 captures, all regenerated 2026-08-08 when tasks gained labels.

The captures record the application as it actually is. Where a screen is a placeholder or a control
does not exist yet, the image shows that rather than a mock-up, and the difference is noted in the
caption or under [Known gaps](#known-gaps).

Sections below map to [docs/FEATURES.md](FEATURES.md), which carries the feature and test-coverage
detail for each area.

## Regenerating

The suite drives the real API, so the backend must be running first. From the repository root:

```bash
DISABLE_RATE_LIMIT=true SERVER_PORT=8090 go run cmd/main.go
```

Then, from `gocrm-ui/`:

```bash
npm run screenshots
```

The Vite dev server is started automatically by the `webServer` block in
`playwright.config.screenshots.ts` (an already-running one is reused), and the admin account the
specs log in as is seeded by `e2e/global-setup.ts`. Images are written straight to
`docs/screenshots/<area>/`, overwriting the previous run.

The suite creates records through the UI so lists and detail pages are not empty. It never confirms
a delete dialog — deleting a user, customer or lead is an irreversible erasure (see FEATURES.md
section 12), and deleting a label detaches it from every task at once.

The label captures reuse fixed names (`Onboarding`, `Escalation`, `Follow-up`) and create them only
when missing. Labels are unique by name and hard deleted rather than soft deleted, so run-scoped
names would pile up on the very screen being photographed.

## Authentication

FEATURES.md section 1.

![Login page](screenshots/auth/01-login.png)
The login form, with the email, password and "remember me" controls.

![Login validation errors](screenshots/auth/02-login-validation.png)
Client-side validation on submit of an empty login form.

![Registration form](screenshots/auth/03-register.png)
The public registration form, filled in. Registration always creates a `customer` — a
client-supplied role is ignored.

![Registration validation errors](screenshots/auth/04-register-validation.png)
The password-policy messages: minimum 10 characters with uppercase, lowercase, digit and special
character.

## Dashboard

FEATURES.md section 2.

![Dashboard overview](screenshots/dashboard/01-overview.png)
Full-page dashboard with the five stat cards (total leads, total customers, open tickets, pending
tasks, conversion rate), the sales chart, Quick Actions and Upcoming Tasks, over seeded data.

## Leads

FEATURES.md section 3.

![Leads list](screenshots/leads/01-list.png)
The leads table with company, contact, email, phone, status, classification, source and created
date.

![Create lead form](screenshots/leads/02-create.png)
The create form, filled in and not submitted.

![Lead detail](screenshots/leads/03-detail.png)
A single lead's detail page.

![Edit lead form](screenshots/leads/04-edit.png)
The edit form populated from the stored record.

![Delete lead confirmation](screenshots/leads/05-delete-confirm.png)
The delete confirmation dialog. Confirming erases the lead's personal data and cascades to the
customer it was converted into.

![Lead search](screenshots/leads/06-search.png)
The list filtered by a search term, which matches across name, email, company, phone and notes.

## Customers

FEATURES.md section 4.

![Customers list](screenshots/customers/01-list.png)
The customers table with name, email, company, revenue and status.

![Create customer form](screenshots/customers/02-create.png)
The create form with contact details and full address, filled in and not submitted.

![Customer detail](screenshots/customers/03-detail.png)
A single customer's detail page.

![Edit customer form](screenshots/customers/04-edit.png)
The edit form populated from the stored record.

![Delete customer confirmation](screenshots/customers/05-delete-confirm.png)
The admin-only delete confirmation. Confirming erases personal data and cascades to the lead the
customer was converted from.

## Tickets

FEATURES.md section 5.

![Tickets list](screenshots/tickets/01-list.png)
The tickets table with ticket number, subject, status, priority, customer and assignee.

![Create ticket form](screenshots/tickets/02-create.png)
The create form with subject, description, priority, status and customer picker. Only admin and
support may create tickets.

![Ticket detail](screenshots/tickets/03-detail.png)
A single ticket's detail page, including the description panel.

![Edit ticket form](screenshots/tickets/04-edit.png)
The edit form; support users may only update tickets assigned to them, and sales is read-only.

![Delete ticket confirmation](screenshots/tickets/05-delete-confirm.png)
The delete confirmation dialog. Ticket deletion is admin-only and an ordinary soft delete.

## Tasks

FEATURES.md section 6.

![Tasks list](screenshots/tasks/01-list.png)
The tasks table with title, labels, status, priority, assignee, due date and created date, plus the
search box and the status, priority and label filters. The rows on this page carry no labels; see
[Labels](#labels) for the same list filtered down to a labelled task.

![Create task form](screenshots/tasks/02-create.png)
The create form with title, description, status, priority, due date, assignee and the Labels
multi-select. "Assign To" is captioned "(Optional)" but the API rejects a task without an assignee.

![Task detail](screenshots/tasks/03-detail.png)
A single task's detail page.

![Edit task form](screenshots/tasks/04-edit.png)
The edit form populated from the stored record. Only admins may reassign a task.

![Delete task confirmation](screenshots/tasks/05-delete-confirm.png)
The delete confirmation dialog. Task deletion is admin-only.

## Labels

Free-form colored labels on tasks; related to FEATURES.md section 6 (Task Management).

![Labels list](screenshots/labels/01-list.png)
The label management screen at `/labels`: name chip, hex colour with a swatch, the number of tasks
carrying the label, and the edit and delete actions. Deleting is admin-only.

![Create label dialog](screenshots/labels/02-create-dialog.png)
The create dialog, filled in and not submitted. The colour comes either from the preset swatches or
from the free `#RRGGBB` field below them; the preview chip shows the automatic light/dark text
colour that the chosen background produces.

![Labels on the task form](screenshots/labels/03-task-form.png)
The task form's Labels field with two labels attached. Typing a name that does not exist yet offers
an `Add "…"` option that creates the label inline, without leaving the form.

![Tasks filtered by a label](screenshots/labels/04-list-filtered.png)
The task list filtered to one label, chosen from the Label dropdown. The active filter is echoed as
a chip with a clear affordance, and the chips in the Labels column filter the list when clicked.

![Delete label confirmation](screenshots/labels/05-delete-confirm.png)
The delete confirmation, which states how many tasks the label will be removed from. Labels are hard
deleted along with their task links; the tasks themselves are untouched.

## Users

FEATURES.md section 7.

![Users list](screenshots/users/01-list.png)
The admin-only users table with role and active status, sorted newest first.

![Create user form](screenshots/users/02-create.png)
The create form including the role selector. This screen and the `create-admin` CLI are the only
ways to obtain an elevated role.

![User detail](screenshots/users/03-detail.png)
A single user's detail page.

![Edit user form](screenshots/users/04-edit.png)
The edit form; role and active status are admin-only fields.

![Delete user confirmation](screenshots/users/05-delete-confirm.png)
The delete confirmation dialog, naming the account. Confirming erases personal data and purges the
account's API keys and refresh tokens.

## Settings

Profile relates to FEATURES.md 7.9, API Keys to section 8, and Configuration to section 9.

![Profile settings](screenshots/settings/01-profile.png)
The Profile screen as a user finds it: a heading only. The view/edit form for one's own profile is
not built yet, although the backend `/users/me` endpoints exist.

![API keys settings](screenshots/settings/02-api-keys.png)
The API Keys screen as a user finds it: a heading only. The backend supports generating, listing
and revoking keys, but the page exposes no controls for it yet.

![Configuration settings](screenshots/settings/04-configuration.png)
The admin-only Configuration screen, with per-category tabs and their value counts and the loaded
configuration table.

## Error screens

FEATURES.md section 11 (role-based access and error handling).

![Access denied](screenshots/misc/01-unauthorized.png)
The Access Denied screen shown when an authenticated user opens a route their role does not allow.

![Not found](screenshots/misc/02-not-found.png)
The 404 screen for an unknown client-side route.

## Known gaps

Screens with no image because the UI does not exist yet:

- **API key generation and revocation** (FEATURES.md 8.1, 8.3) — the API Keys page has no create or
  revoke control, so no key-created or revoke-confirmation capture can be produced.
- **Own-profile edit** (7.9) — the Profile page renders a heading only.
- **Configuration edit and reset** (9.2, 9.3) — the Configuration screen is captured read-only;
  there is no capture of an edit or reset interaction.
- **Password reset** — `/reset-password?token=...` links are emailed, but the SPA route does not
  exist (see [ROADMAP.md](ROADMAP.md)).
- **Bulk status updates** (FEATURES.md section 10) — the endpoints are routed, but no frontend
  screen drives them.

The `settings/` file numbering skips `03`: the suite defines three settings captures, named
`01-profile`, `02-api-keys` and `04-configuration`. Nothing is missing between them.
