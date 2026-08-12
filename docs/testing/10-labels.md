# Task Labels — Test Cases

Playwright end-to-end cases for task labels: the management screen at `/labels`, the create/edit
dialog with its swatch picker, deletion and its detach cascade, the labels field on the task form
(including inline creation), the chips in the task list and on the task detail page, and the two
ways of filtering the task list by label. Every **Expected** states what the build does **today**;
where the behaviour is surprising a **Known issue** line names it.

**Sources**

- `internal/models/label.go`, `internal/models/task.go`, `internal/models/database.go`
- `internal/handler/label_handler.go`, `internal/handler/task_handler.go`,
  `internal/handler/routes.go`, `cmd/main.go`
- `internal/service/label_service.go`, `internal/service/task_service.go`
- `internal/repository/label_repository.go`, `internal/repository/task_repository.go`
- `gocrm-ui/src/pages/labels/LabelList.tsx`, `gocrm-ui/src/components/LabelChip.tsx`,
  `gocrm-ui/src/components/labelColors.ts`
- `gocrm-ui/src/pages/tasks/TaskForm.tsx`, `TaskList.tsx`, `TaskDetail.tsx`
- `gocrm-ui/src/api/endpoints/labels.ts`, `gocrm-ui/src/api/endpoints/tasks.ts`
- `gocrm-ui/e2e/tests/labels.spec.ts`, `gocrm-ui/e2e/pages/labels.page.ts`,
  `gocrm-ui/e2e/pages/tasks.page.ts`, `gocrm-ui/e2e/screenshots/09-labels.spec.ts`
- `docs/SCREENSHOTS.md` (`docs/screenshots/labels/`)

**Constraints**

- **Labels are hard-deleted and unique by name.** `labelRepository.Delete` runs `Unscoped` inside a
  transaction that first clears `task_labels`, so there is no soft-deleted tombstone holding the
  name. A case that creates a label must therefore use a run-scoped name (`labels.spec.ts` derives
  one from `Date.now()` plus a random suffix) and delete it again, or the shared database — and the
  screenshot captures taken from it — fills up with run debris.
- **`/labels` has no route guard.** `SetupLabelRoutes` (`internal/handler/routes.go:81`) leaves
  `GET /labels` open to every authenticated role, and the SPA route
  (`gocrm-ui/src/routes/index.tsx`) and the sidebar entry (`MainLayout.tsx`) carry no `roles` array.
  Write access is enforced per verb: create and update need admin, sales or support; delete needs
  admin. The page hides the buttons to match (`LabelList.tsx:62-63`).
- **Only the admin account is available to e2e.** Sales and support accounts require an
  admin-authenticated `POST /users` plus a role-login helper that does not exist yet; a *customer*
  is obtainable through public registration, which `registration.spec.ts` already does. Cases that
  need a non-admin, non-customer session are marked *blocked* with that note.
- The label editor validates client-side with zod (`LabelList.tsx:46-51`) before any request is
  made, and the API validates again in `normalizeLabel` (`internal/service/label_service.go:150`).
  A case that expects an HTTP status must state which of the two layers it is exercising.
- Task creation through the UI always needs an assignee (`assigned_to_id` is `binding:"required"`),
  which is why `TasksPage.fillTaskForm` picks one — see TC-TASK-008.

---

## 10.1 Label Management Screen

### TC-LABEL-001 — Load the label list as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one label exists.
- **Steps:**
  1. Navigate to `/labels`.
  2. Wait for the network to settle.
- **Expected:** `GET /api/v1/labels` returns 200 with a bare array (the axios interceptor peels the `{success,data}` envelope), each entry carrying `id`, `name`, `color` and `task_count`. The `h4` heading "Labels", the "Create Label" button and a table with the columns Name, Color, Tasks, Actions are visible; each row shows the label as a coloured chip.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can create a label from the labels management page" asserts the heading, the "Create Label" button and the row; the column headers themselves are not asserted.

### TC-LABEL-002 — Labels are ordered by name ascending
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; at least three labels whose names sort differently from their creation order.
- **Steps:**
  1. Navigate to `/labels`.
  2. Read the name cell of every row in order.
- **Expected:** The names are in ascending order. The ordering is `ORDER BY name ASC` in the repository (`label_repository.go:94`), not a client-side sort, and the table offers no column sorting.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-003 — The Tasks column counts the tasks carrying the label
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label attached to exactly one task.
- **Steps:**
  1. Navigate to `/labels`.
  2. Read the Tasks cell of the label's row.
- **Expected:** The cell reads `1`. The count comes from a second query joined against `tasks` with `tasks.deleted_at IS NULL` (`label_repository.go:161-187`), so a soft-deleted task stops being counted.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can attach an existing label to a new task"

### TC-LABEL-004 — The Color column shows the swatch and the uppercased hex
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a label created with `#d62728`.
- **Steps:**
  1. Navigate to `/labels`.
  2. Read the Color cell of that row.
- **Expected:** A coloured square (`data-testid="label-swatch-{id}"`) followed by `#D62728`. The value is uppercased for display only (`LabelList.tsx:210`); the stored value keeps the case it was sent with.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can create a label from the labels management page"

### TC-LABEL-005 — Empty state when no labels exist
- **Priority:** P2
- **Type:** functional
- **Preconditions:** No labels at all in the database.
- **Steps:**
  1. Navigate to `/labels`.
- **Expected:** A single row spanning all four columns reading "No labels yet."; the table header is still rendered.
- **Automation:** blocked — the shared database always carries the fixed labels the screenshot suite (`e2e/screenshots/09-labels.spec.ts`) creates, and a spec may not delete labels it did not create. Reachable only against a dedicated empty database.

## 10.2 Creating a Label

### TC-LABEL-006 — Create a label from the management page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped name that does not exist yet.
- **Steps:**
  1. Navigate to `/labels` and click "Create Label".
  2. Type the name into `input[name="name"]`.
  3. Click the preset swatch `button[aria-label="Use color #D62728"]`.
  4. Submit the dialog.
- **Expected:** `POST /api/v1/labels` with `{name, color}` returns 201. The dialog closes, a snackbar reads `Label "<name>" created`, the new row appears with the chosen colour and a task count of `0`, and both the `labels` and `tasks` query caches are invalidated.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can create a label from the labels management page"

### TC-LABEL-007 — A duplicate name is rejected with 409
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a label with a known name already exists.
- **Steps:**
  1. Open the create dialog and enter the same name with a different colour.
  2. Submit.
- **Expected:** `POST /api/v1/labels` returns 409. A snackbar reads "A label with that name already exists". The dialog stays open with the typed name intact so it can be corrected, and reloading `/labels` shows the label exactly once — nothing was written.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "a label name that already exists is rejected"

### TC-LABEL-008 — A name differing only in case is a duplicate
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a label named `Renewal` exists.
- **Steps:**
  1. Open the create dialog, enter `RENEWAL`, submit.
- **Expected:** 409, same snackbar as TC-LABEL-007. The service pre-checks with `LOWER(name) = LOWER(?)` (`label_repository.go:136`) precisely so MySQL (case-insensitive collation) and the SQLite test database agree; without it the E2E result would depend on the backing database.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). Covered at unit level by `internal/service/label_service_test.go`.

### TC-LABEL-009 — Leading and trailing whitespace is trimmed from the name
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog and enter `"  Renewal  "`.
  2. Submit.
- **Expected:** The request body carries `Renewal` (the page trims at `LabelList.tsx:149`), and the service trims again in `normalizeLabel`. The stored name has no padding, so a later `Renewal` is a duplicate.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-010 — A blank name is rejected before any request
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog, leave Name empty (or enter only spaces), submit.
- **Expected:** The field shows "Name is required" and **no** `POST /labels` is issued — zod rejects it client-side. The API would answer 400 `ErrInvalidLabelName` if the request were made by other means.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-011 — A name longer than 50 characters is rejected
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog, enter 51 characters, submit.
- **Expected:** "Name must be 50 characters or fewer" and no request. The limit mirrors the `varchar(50)` column, and the service enforces it again in runes, not bytes (`label_service.go:155`).
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-012 — A malformed hex colour is rejected
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog, enter a valid name, clear the Hex color field and type `red` (then repeat with `#FFF`).
  2. Submit.
- **Expected:** "Color must be a hex value such as #1F77B4" and no request in both cases. The three-digit shorthand is deliberately refused at every layer: the binding tag pairs `hexcolor` with `len=7` (`label_handler.go:30`) and the service regex demands exactly six digits.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-013 — A free hex colour outside the preset palette is accepted
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog, enter a run-scoped name and type `#123ABC` into the Hex color field.
  2. Read the preview chip, then submit.
- **Expected:** 201; the row shows `#123ABC`. The swatches are suggestions only — the helper text under the field says so — and the preview chip renders the name on that background with black or white text chosen by luminance.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-014 — The create dialog preselects an unused palette colour
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; some but not all of the ten palette colours are already in use.
- **Steps:**
  1. Open the create dialog.
  2. Read the Hex color field.
- **Expected:** The field holds the first palette entry no existing label uses (`nextPaletteColor`, `labelColors.ts`), so consecutive labels do not all come out the same colour. Once the palette is exhausted it cycles.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). The helper itself is unit-tested in `gocrm-ui/src/components/LabelChip.test.tsx`.

### TC-LABEL-015 — Cancelling the editor writes nothing
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Open the create dialog, fill in a name and colour.
  2. Click "Cancel".
  3. Reload `/labels`.
- **Expected:** The dialog closes, no `POST /labels` is issued and the list is unchanged.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/labels.spec.ts` "a label name that already exists is rejected" cancels the dialog and re-asserts the row count; a cancel from a *clean* dialog is not covered. The screenshot capture `docs/screenshots/labels/02-create-dialog.png` is also taken from a filled, never-submitted dialog.

## 10.3 Editing a Label

### TC-LABEL-016 — Rename and recolour a label
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label attached to one task.
- **Steps:**
  1. Navigate to `/labels` and click the edit icon on the label's row.
  2. Replace the name, click a different preset swatch, submit.
  3. Open the task's detail page.
- **Expected:** The dialog opens prefilled with the current name and colour. `PUT /api/v1/labels/{id}` returns 200 and the response carries the refreshed `task_count` (the handler re-reads after the write, `label_handler.go:156`). The row shows the new name and colour, the task count is unchanged at `1` — renaming never detaches anything — and the task detail page shows the new name because tasks carry labels by reference.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can rename and recolour a label"

### TC-LABEL-017 — Recolouring without renaming is not a duplicate
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; any label.
- **Steps:**
  1. Open the edit dialog, change only the colour, submit.
- **Expected:** 200, not 409. The dialog always sends both fields, so the name collides with the label's own row; `ExistsByNameInsensitive` is called with the label's id as `excludeID` (`label_service.go:89`) precisely to allow that.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). Covered at unit level by `internal/service/label_service_test.go`.

### TC-LABEL-018 — Renaming onto another label's name is rejected
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; two labels A and B.
- **Steps:**
  1. Open the edit dialog on A and type B's name.
  2. Submit.
- **Expected:** `PUT /labels/{id}` returns 409, the snackbar reads "A label with that name already exists", the dialog stays open and A keeps its old name after a reload.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

## 10.4 Deleting a Label

### TC-LABEL-019 — The delete dialog states how many tasks are affected
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label attached to exactly one task, and a second one attached to none.
- **Steps:**
  1. Navigate to `/labels` and click the delete icon on the first label.
  2. Read the dialog, cancel, and repeat on the second label.
- **Expected:** The dialog is titled "Delete Label" and reads `Delete the label "<name>"? It will be removed from 1 task. This action cannot be undone.` — singular for one task, `0 tasks` / `2 tasks` otherwise (`LabelList.tsx:325`). The count comes from the list payload, so it is as fresh as the last `GET /labels`.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "deleting a label detaches it from the tasks that carry it" (the "1 task" wording) and "a label no task carries can be deleted" (the "0 tasks" wording)

### TC-LABEL-020 — Deleting a label detaches it from every task
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label attached to a task the spec created.
- **Steps:**
  1. Delete the label from `/labels` and confirm.
  2. Open the task's detail page.
- **Expected:** `DELETE /api/v1/labels/{id}` returns 204 with an empty body, the snackbar reads "Label deleted successfully" and the row disappears. The task itself still exists and renders normally, without the chip: the repository clears `task_labels` and hard-deletes the row in one transaction (`label_repository.go:68-83`), so no orphaned join row is left to break the task's preload.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "deleting a label detaches it from the tasks that carry it"

### TC-LABEL-021 — Deleting an unused label
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label with a task count of `0`.
- **Steps:**
  1. Delete it and confirm.
- **Expected:** 204 and the row disappears. Nothing else changes.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "a label no task carries can be deleted"

### TC-LABEL-022 — Cancelling the delete dialog leaves the label alone
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; any label.
- **Steps:**
  1. Open the delete dialog and click "Cancel".
  2. Reload `/labels`.
- **Expected:** No `DELETE` request is issued and the label is still listed with its task count intact.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). The screenshot suite always cancels this dialog (`e2e/screenshots/09-labels.spec.ts` "05 - delete label confirmation"), but asserts nothing about the absence of the request.

### TC-LABEL-023 — Deletion is permanent and the name becomes reusable
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label just deleted.
- **Steps:**
  1. Create a label with exactly the same name.
- **Expected:** 201, not 409. Labels are hard-deleted (`Unscoped`), so no soft-deleted row keeps the name reserved — the trap documented for users and customers does not apply here.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

## 10.5 Authorization

### TC-LABEL-024 — Every authenticated role can read the label list
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** A customer account, obtained through public registration (`/register`), logged in.
- **Steps:**
  1. Navigate to `/labels`.
- **Expected:** `GET /api/v1/labels` returns 200 and the table renders. `SetupLabelRoutes` installs no `RequireRole` on the GET, and neither the SPA route nor the sidebar entry carries a `roles` array, so the page is reachable by every role.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). Feasible without new infrastructure: unlike sales and support, a customer can be created through public registration.

### TC-LABEL-025 — A customer sees no create, edit or delete affordance
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Customer account logged in.
- **Steps:**
  1. Navigate to `/labels`.
  2. Look for the "Create Label" button and the row action icons.
- **Expected:** None of them are rendered (`canManage`/`canDelete`, `LabelList.tsx:62-63`). The API backs this up: `POST` and `PUT` require admin, sales or support and answer 403 for a customer.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-026 — Sales and support may create and edit but not delete
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales or support account (admin-created via `POST /users`), logged in.
- **Steps:**
  1. Navigate to `/labels`; create a label; edit it; look for the delete icon.
- **Expected:** Create and edit succeed (201 / 200) and the delete icon is absent. A direct `DELETE /api/v1/labels/{id}` returns 403 — deleting detaches the label from every task at once, which is why it is admin-only.
- **Automation:** blocked — no sales/support login helper exists; elevated roles come only from an admin-authenticated `POST /users`. This is the same blocker listed in README.md "Lessons learned" item 8.

### TC-LABEL-027 — Unauthenticated access to the label API is rejected
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** No session.
- **Steps:**
  1. Request `GET /api/v1/labels` with no `Authorization` header.
  2. Navigate to `/labels` in a logged-out browser.
- **Expected:** 401 from the API; the SPA redirects to `/login` because `/labels` sits under the authenticated layout.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

## 10.6 Labels on the Task Form

### TC-LABEL-028 — Attach an existing label while creating a task
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped label exists; task data from `generateTaskData()` plus an assignee.
- **Steps:**
  1. Navigate to `/tasks`, click "Create Task", fill the form.
  2. Open the Labels Autocomplete, type the label name, click the option.
  3. Submit.
- **Expected:** The label appears as a coloured chip inside the field. `POST /api/v1/tasks` carries `label_ids: [id]` and returns 201 with the labels **resolved** in the response (`labels: [{id, name, color}]`), not just the ids. The page navigates to `/tasks`, and `/labels` now counts the task against the label.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can attach an existing label to a new task"

### TC-LABEL-029 — Create a label inline from the task form
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a run-scoped name that matches no existing label.
- **Steps:**
  1. Open the task create form and fill it in.
  2. Type the new name into the Labels field and click the synthetic `Add "<name>"` option.
  3. Submit the task.
- **Expected:** `POST /api/v1/labels` returns 201 with a colour handed out from the preset palette — the user never picks one mid-flow — and the new label is selected immediately, without a trip to `/labels`. The subsequent `POST /tasks` carries its id, and the label then shows a task count of `1`.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "admin can create a label inline from the task form"

### TC-LABEL-030 — The inline-create option is not offered for a name that already exists
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a label named `Renewal` exists.
- **Steps:**
  1. Open the task form and type `renewal` into the Labels field.
- **Expected:** Only the existing `Renewal` option is listed; no `Add "renewal"` entry appears. The comparison is case-insensitive (`TaskForm.tsx` `filterOptions`), which keeps the form from offering a create that the API would answer with 409.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-031 — The edit form preloads the task's current labels
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task carrying one label.
- **Steps:**
  1. Navigate to `/tasks/{id}/edit`.
- **Expected:** The Labels field is prefilled with the task's labels as coloured chips, each with a delete affordance, and the title field holds the stored title.
- **Automation:** automated (partial) — `gocrm-ui/e2e/tests/labels.spec.ts` "editing a task replaces its label set" asserts the prefilled chip before removing it.

### TC-LABEL-032 — Saving a task replaces its whole label set
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task carrying exactly one label.
- **Steps:**
  1. Open `/tasks/{id}/edit`, remove the chip with its delete icon, save.
  2. Open the task detail page.
- **Expected:** `PUT /api/v1/tasks/{id}` carries `label_ids: []` and returns 200 with `labels: []`; the detail page shows no chips. `label_ids` is a pointer server-side (`task_handler.go:47`) so an empty array clears the set while an absent field leaves it untouched — the frontend always sends the field from this form.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "editing a task replaces its label set"

### TC-LABEL-033 — An unknown label id rejects the whole write
- **Priority:** P1
- **Type:** negative
- **Preconditions:** An API client (not the UI) with an admin token.
- **Steps:**
  1. `POST /api/v1/tasks` with a valid body plus `label_ids: [999999]`.
  2. Repeat as `PUT /api/v1/tasks/{id}`.
- **Expected:** 400 with error code `INVALID_REFERENCE` in both cases, and no task is created or modified — the resolve step runs before the write. A bad id in the body is a payload problem, not a missing resource at the requested path, which is why it is 400 rather than 404.
- **Automation:** blocked — not reachable through the UI: the Labels field only offers ids the label list returned. Covered by `internal/service/task_labels_test.go` and `internal/handler/task_labels_handler_test.go`.

## 10.7 Label Chips in the Task List and Detail

### TC-LABEL-034 — Chips render in the task list and on the task detail page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a labelled task the spec created.
- **Steps:**
  1. Navigate to `/tasks` and search for the task's title.
  2. Read the Labels cell of its row.
  3. Open `/tasks/{id}`.
- **Expected:** The row's Labels cell shows a chip per label in the label's own colour; the detail page shows a "Labels" block above "Task Details" with the same chips. The list columns are Title, Labels, Status, Priority, Assigned To, Due Date, Created, Actions (see TC-TASK-001).
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "label chips appear in the task list and on the task detail page"

### TC-LABEL-035 — A task with no labels renders an empty cell and no detail block
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a task with no labels.
- **Steps:**
  1. Find the task in `/tasks` and read its Labels cell.
  2. Open its detail page.
- **Expected:** The cell is empty (the column formatter returns `null` for an empty array) and the detail page renders no "Labels" heading at all, rather than an empty block. Visible in `docs/screenshots/tasks/01-list.png`, where no row on the first page carries a label.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-036 — More than three labels collapse into a "+N" indicator
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a task carrying five labels.
- **Steps:**
  1. Navigate to `/tasks` and read the task's Labels cell.
- **Expected:** Three chips followed by `+2`. The cap keeps the column width predictable (`MAX_VISIBLE_LABEL_CHIPS`, `TaskList.tsx`); the detail page shows all five.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-037 — Chip text colour is chosen for contrast
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; one label on a dark colour (`#1F77B4`) and one on a light colour (`#BCBD22`).
- **Steps:**
  1. Open `/labels` and inspect the computed `color` of each chip.
- **Expected:** White text on the dark background, black on the light one — whichever of black/white yields the higher WCAG contrast ratio against the label colour (`contrastingTextColor`, `labelColors.ts`). An unparseable colour degrades to dark text.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended). The luminance maths and the palette's 4.5:1 guarantee are unit-tested in `gocrm-ui/src/components/LabelChip.test.tsx`.

## 10.8 Filtering Tasks by Label

### TC-LABEL-038 — Clicking a chip in the list filters by that label
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a labelled task visible in the list.
- **Steps:**
  1. Navigate to `/tasks` and locate the labelled row.
  2. Click the chip inside the Labels cell.
- **Expected:** `GET /api/v1/tasks?...&label_id={id}` is issued and every returned row carries the label. The click does **not** navigate to the task: the chip stops propagation so a chip click is a filter action while a row click is still a drill-down. The active filter is echoed next to the filter controls as a chip with a clear affordance (`data-testid="active-label-filter"`).
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "clicking a label chip in the list filters the tasks by that label"

### TC-LABEL-039 — The label dropdown filters the list and can be cleared
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a labelled task.
- **Steps:**
  1. Navigate to `/tasks`, open the "Label" Autocomplete and pick the label.
  2. Click the delete icon on the "Filtered by" chip.
- **Expected:** Step 1 issues `GET /tasks?...&label_id={id}` and the labelled task is listed. Step 2 removes the chip and empties the Label field. No request is made on clear: the query key returns to the one already fetched on page load, so TanStack Query serves the cached unfiltered page — assert the rendered controls, not the wire.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "the label filter dropdown narrows the list and can be cleared"

### TC-LABEL-040 — The label filter overrides an active search term
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a labelled task and at least one other task carrying the same label but not matching the search term.
- **Steps:**
  1. Navigate to `/tasks` and search for the first task's title.
  2. Click its label chip.
- **Expected:** The result set is **all** tasks carrying the label, not the intersection with the search term. Applying the label filter empties the search box and disables it, with the helper text "Clear the label filter to search", so the rendered controls never claim a narrowing the server is not applying. Removing the label filter re-enables the box.
- **Known issue:** `TaskHandler.List` branches on `label_id` before `search` (`internal/handler/task_handler.go` `List`), so a request carrying both silently drops the search — the two filters cannot be combined server-side. The precedence is documented in the swagger annotation for the endpoint. The frontend mirrors it rather than contradicting it (`TaskList.tsx` `handleLabelFilterChange`); it is still a server-side limitation, not a feature.
- **Automation:** automated — `gocrm-ui/e2e/tests/labels.spec.ts` "clicking a label chip in the list filters the tasks by that label". The search-box half is covered at unit level by `gocrm-ui/src/pages/tasks/TaskList.test.tsx`.

### TC-LABEL-041 — Applying a label filter returns to page 1
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more than one page of tasks.
- **Steps:**
  1. Navigate to `/tasks` and go to page 2.
  2. Pick a label in the Label filter.
- **Expected:** The request carries `page=1` alongside `label_id` (`handleLabelFilterChange` resets the page), so the filtered result set is never shown from an offset that belongs to the unfiltered list.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

### TC-LABEL-042 — A label_id matching no label yields an empty page, not an error
- **Priority:** P2
- **Type:** negative
- **Preconditions:** An API client with an admin token.
- **Steps:**
  1. `GET /api/v1/tasks?label_id=999999`.
  2. `GET /api/v1/tasks?label_id=abc` and `?label_id=0`.
- **Expected:** Step 1 returns 200 with `tasks: []` and `total: 0` — no 404. In step 2 the unparseable and non-positive values are ignored entirely and the full list comes back, matching how `sort_by` handles a value it does not recognise.
- **Automation:** blocked — not reachable through the UI: the filter only offers ids from the label list. The ignored-value half is covered by `internal/handler/task_labels_handler_test.go` `TestList_MalformedLabelIDIsIgnored`.

### TC-LABEL-043 — Non-admin roles keep their assignee scoping under a label filter
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support account with one labelled task assigned to it, and another user's task carrying the same label.
- **Steps:**
  1. Log in as the support user, navigate to `/tasks` and filter by the label.
- **Expected:** Only the support user's own task is listed. The label filter narrows the same list rather than replacing the assignee scoping (`ListByLabelForAssignee`), and it is the one non-admin path that also honours `sort_by`/`sort_order`.
- **Automation:** blocked — no sales/support login helper exists (README.md "Lessons learned" item 8). Covered at unit level by `internal/service/task_labels_test.go`.

## 10.9 Navigation

### TC-LABEL-044 — The sidebar carries a Labels entry for every role
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in (repeat as a customer for the role half).
- **Steps:**
  1. Read the sidebar.
  2. Click "Labels".
- **Expected:** A "Labels" entry with a tag icon sits between "Tasks" and "Users" and navigates to `/labels`. It carries no `roles` array, so it is shown to every authenticated role, including customers — for whom the destination page is read-only rather than hidden.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended)

## 10.10 Payload limits and task-form gating

### TC-LABEL-045 — A customer is offered no inline label creation on the task form
- **Priority:** P1
- **Type:** rbac
- **Preconditions:** A customer account (public registration) logged in, on `/tasks/new` or the edit form of a task assigned to them.
- **Steps:**
  1. Open the "Labels" field and type a name that matches no existing label.
- **Expected:** No `Add "..."` option appears and the field's helper text reads "Pick from the existing labels" instead of "Type a new name to create a label on the fly". Existing labels are still offered and can still be attached — only creation is withheld, mirroring `POST /labels` (admin, sales, support) and the `canManage` gating on the label management page. Neither the task routes nor the task API carry a role guard, so the form itself is reachable by a customer; the gating is on the create affordance alone.
- **Automation:** planned — `gocrm-ui/e2e/tests/labels.spec.ts` (extended; needs the customer session of TC-LABEL-024). Covered at unit level by `gocrm-ui/src/pages/tasks/TaskForm.test.tsx`.

### TC-LABEL-046 — label_ids is capped at 100 ids per request
- **Priority:** P2
- **Type:** negative
- **Preconditions:** An API client (not the UI) with a token that can write tasks.
- **Steps:**
  1. `POST /api/v1/tasks` with a valid body plus 101 `label_ids`.
  2. Repeat as `PUT /api/v1/tasks/{id}`.
  3. Repeat step 1 with `label_ids: [0]`.
- **Expected:** 400 validation errors in all three cases, raised by the binding tags before any service or repository call, so no label lookup and no `IN` clause is built. Exactly 100 ids is accepted. The bound matches the one the bulk-status endpoints enforce and keeps a single request from expanding into an arbitrarily large `IN` clause.
- **Automation:** blocked — not reachable through the UI: the Labels field only offers ids the label list returned. Covered by `internal/handler/task_labels_handler_test.go` (`TestCreate_RejectsMoreThanOneHundredLabelIDs`, `TestCreate_AcceptsExactlyOneHundredLabelIDs`, `TestUpdate_RejectsMoreThanOneHundredLabelIDs`, `TestCreate_RejectsZeroLabelID`, `TestUpdate_RejectsZeroLabelID`).
