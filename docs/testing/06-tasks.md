# Task Management — Test Cases

Playwright end-to-end cases for the Tasks area: the list and calendar views, the create/edit form,
the detail page, deletion, the page-local filter controls, search, sorting, due-date handling, the
"my tasks" endpoint and the bulk status endpoint. Every **Expected** below states what the build
does **today**, including behaviour that is wrong; where the behaviour is defective a **Known
issue** line names it. Several cases therefore assert an unhelpful outcome on purpose — they are
regression anchors, not endorsements.

**Sources**

- `docs/FEATURES.md` section 6 (rows 6.1–6.9) and the Gap Summary (G18, G33, G34)
- `docs/ROADMAP.md`
- `gocrm-ui/src/pages/tasks/TaskList.tsx`, `TaskForm.tsx`, `TaskDetail.tsx`
- `gocrm-ui/src/api/endpoints/tasks.ts`, `gocrm-ui/src/api/client.ts`, `gocrm-ui/src/types/index.ts`
- `gocrm-ui/src/routes/index.tsx`, `gocrm-ui/src/layouts/MainLayout.tsx`,
  `gocrm-ui/src/components/DataTable.tsx`, `gocrm-ui/src/components/form/FormDatePicker.tsx`
- `internal/handler/task_handler.go`, `internal/handler/bulk_handler.go`,
  `internal/handler/routes.go`, `cmd/main.go`
- `internal/service/task_service.go`, `internal/models/task.go`
- `gocrm-ui/e2e/tests/admin-tasks.spec.ts`, `gocrm-ui/e2e/screenshots/06-tasks.spec.ts`,
  `gocrm-ui/e2e/pages/tasks.page.ts`, `gocrm-ui/e2e/fixtures/admin-user.ts`

**Constraints**

- `SetupTaskRoutes` (`internal/handler/routes.go:64`) installs **no** `RequireRole` guard, and the
  `Tasks` sidebar entry (`MainLayout.tsx:77`) carries no `roles` array. Every authenticated role,
  customer included, can open `/tasks`. Role differences are enforced inside the handlers only.
- The non-admin cases need sales, support and customer accounts. They must be created through the
  admin-guarded `POST /users` with `generateUserData()` from `e2e/fixtures/admin-user.ts` (never a
  hardcoded address), and a role-login helper is required — none exists yet, which is why those
  cases are marked *blocked* or *planned* with that note.
- Tasks are **not** subject to GDPR erasure: `TaskHandler.Delete` performs a plain GORM soft delete
  (`internal/service/task_service.go` `Delete`). Even so, a case must only delete tasks it created
  itself, because `TasksPage.deleteTask(0)` targets the first row of a shared list.
- `generateTaskData()` produces `title`, `description`, `priority`, `status` and a **future**
  `dueDate` (`YYYY-MM-DD`). It does not produce an assignee; see TC-TASK-008.

---

## 6.1 View Tasks List

### TC-TASK-001 — Load the task list as admin
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; at least one task exists (create it with `generateTaskData()` plus an assignee, per TC-TASK-007).
- **Steps:**
  1. Navigate to `/tasks`.
  2. Wait for the network to settle.
- **Expected:** `GET /api/v1/tasks?page=1&limit=10` returns 200 with `{ tasks: [...], total: n }`. The `h4` heading "Tasks", the "Create Task" button, the list/calendar toggle and a table with the columns Title, Labels, Status, Priority, Assigned To, Due Date, Created, Actions are visible. The footer reads "1–10 of {total}".
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin can view tasks list page"

### TC-TASK-002 — Assigned To column renders "Unassigned" for every task
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; a task created with an explicit assignee (TC-TASK-007).
- **Steps:**
  1. Navigate to `/tasks`.
  2. Read the "Assigned To" cell of the row for the task just created.
- **Expected:** The cell reads `Unassigned`, even though the API response for that row carries a populated `assigned_to` user object and a non-zero `assigned_to_id`.
- **Known issue:** The backend serialises the assignee as `assigned_to` (`internal/models/task.go:27`), while `transformTaskFromBackend` (`gocrm-ui/src/api/endpoints/tasks.ts:32-41`) overwrites `assigned_to` with the numeric id and never populates the `assignee` key the column reads (`TaskList.tsx:165-174`). Verified against the live API and against the committed capture `docs/screenshots/tasks/01-list.png`, where every row shows "Unassigned". Not recorded in FEATURES.md row 6.1.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-003 — Page through the task list and change the page size
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; more than 10 tasks exist.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the next-page arrow.
  3. Set "Rows per page" to 25.
- **Expected:** Step 2 issues `GET /api/v1/tasks?page=2&limit=10`; step 3 issues `page=1&limit=25` (the handler converts `page` to an offset at `task_handler.go:199-201`). The footer count updates and no request is sent with `limit=0`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-004 — Switch to the calendar view
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a task whose due date falls in the current month.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the calendar toggle button (second button of the `ToggleButtonGroup`).
  3. Click the task chip inside the day cell matching its due date.
- **Expected:** A month grid headed `MMMM yyyy` with Previous / Today / Next buttons replaces the table. Only the tasks already fetched for the current page are placed on the grid — the calendar issues no separate request, so a task due this month but sitting on page 2 does not appear. Clicking the chip navigates to `/tasks/{id}`.
- **Known issue:** The calendar renders the current page of results rather than the month's tasks (`TaskList.tsx:249-256`), which makes it silently incomplete. Not listed in FEATURES.md.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-005 — Support user sees only tasks assigned to them
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Admin creates a support user with `generateUserData()` (role forced to `support`) and two tasks — one assigned to that user, one assigned to the admin. Support user logged in.
- **Steps:**
  1. Log in as the support user.
  2. Navigate to `/tasks`.
- **Expected:** `GET /api/v1/tasks` returns 200 but is narrowed to `GetByAssignee(currentUserID, …)` (`task_handler.go:230-231`). Only the support user's own task is listed; `total` counts only their tasks. The "Create Task" button and the per-row edit and delete icons are still rendered — the frontend applies no role gating on this page.
- **Automation:** blocked — needs a non-admin role-login helper; `e2e/helpers/admin-auth.ts` only handles the seeded admin

### TC-TASK-006 — Customer role can open the Tasks page
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A customer account (self-registered via `/register`, which always yields `customer`). Customer logged in.
- **Steps:**
  1. Log in as the customer.
  2. Observe the sidebar.
  3. Click "Tasks".
- **Expected:** "Tasks" is present in the sidebar (`MainLayout.tsx:77` declares no `roles`), `/tasks` renders, and `GET /api/v1/tasks` returns 200 with an empty task array for a customer who has never been assigned one. The "Create Task" button is visible even though `POST /tasks` will refuse the role (see TC-TASK-015).
- **Known issue:** Contrast with Tickets, whose sidebar entry and routes are role-gated. Tasks are visible to a role that cannot meaningfully use them.
- **Automation:** blocked — needs a customer-role login helper

---

## 6.2 Create Task

### TC-TASK-007 — Create a task with every form field populated
- **Priority:** P0
- **Type:** functional
- **Preconditions:** Admin logged in; task data from `generateTaskData()`.
- **Steps:**
  1. Navigate to `/tasks` and click "Create Task".
  2. Fill Title and Description.
  3. Select a Priority and a Status.
  4. Fill the Due Date input (`input[name="due_date"]`, `yyyy-MM-dd`).
  5. Open "Assign To (Optional)" and pick the first option.
  6. Click "Create Task".
- **Expected:** `POST /api/v1/tasks` returns **201**. The success snackbar reads "Task created successfully" and the app navigates to `/tasks`. The response body's `data.id` is the new task id.
- **Automation:** automated — `gocrm-ui/e2e/screenshots/06-tasks.spec.ts` "01 - tasks list" (via its `createTask` / `selectFirstAssignee` helpers)

### TC-TASK-008 — Submitting without an assignee is rejected despite the "(Optional)" label
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Fill Title, Description and Due Date; leave "Assign To (Optional)" untouched.
  3. Click "Create Task".
- **Expected:** `assigned_to_id` is omitted from the request body, `POST /api/v1/tasks` returns **400**, the error snackbar reads "Failed to create task" and the browser stays on `/tasks/new`. No task is created.
- **Known issue:** `CreateTaskRequest.AssignedToID` is `binding:"required"` (`internal/handler/task_handler.go:29`) while the field is labelled "Assign To (Optional)" (`TaskForm.tsx:216`) and typed `assigned_to?: number` (`api/endpoints/tasks.ts:19`). `e2e/screenshots/06-tasks.spec.ts` documents the same contradiction in a code comment. Consequently `admin-tasks.spec.ts` "admin can create a new task successfully" and "admin can create task with minimal required data" assert `201` while never selecting an assignee — those two tests cannot pass against the current backend.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended; the two existing create tests need the assignee step added, and this case pins the 400)

### TC-TASK-009 — Title is required
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Click "Create Task" with an empty Title.
- **Expected:** The zod resolver blocks submission (`taskSchema.title.min(1, 'Title is required')`, `TaskForm.tsx:26`). "Title is required" appears as helper text, no `POST /tasks` request is made and the URL stays `/tasks/new`.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin sees validation errors for invalid task data"

### TC-TASK-010 — Create tasks at each priority
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. For each of low, medium, high: open `/tasks/new`, fill the form (including an assignee), select the priority and save.
  2. Return to `/tasks`.
- **Expected:** Each `POST /api/v1/tasks` returns 201 and the stored `priority` matches the selection. The list shows a Low (grey), Medium (blue) and High (red) chip respectively. Omitting priority entirely defaults to `medium` (`task_service.go:36-38`).
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin can create tasks with different priorities" (currently missing the assignee step, see TC-TASK-008)

### TC-TASK-011 — Status chosen on the create form is discarded
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Fill the form and set Status to "In Progress".
  3. Save, then open the new task's detail page.
- **Expected:** `POST /api/v1/tasks` returns 201 and the created task's status is **`pending`**, not `in_progress`. The detail page shows a "Pending" chip and a "Start Task" button.
- **Known issue:** `TaskHandler.Create` hard-codes `Status: models.TaskStatusPending` (`task_handler.go:96`) and ignores the request field, but the create form offers all four statuses (`TaskForm.tsx:186-190`) and `generateTaskData()` even randomises one. The field is decorative on create.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-012 — A due date in the past is accepted
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Fill Title and an assignee; set Due Date to yesterday.
  3. Save and open the task detail page.
- **Expected:** No client-side rejection — `FormDatePicker` only emits a `min` attribute when a `minDate` prop is supplied and `TaskForm` supplies none (`TaskForm.tsx:200-204`, `FormDatePicker.tsx:47-50`) — and the backend performs no due-date validation. `POST /tasks` returns 201. The detail page shows a red "Overdue" chip and renders the due date in the error colour (`TaskDetail.tsx:122`, `138-144`).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-013 — Clearing the due date blocks submission
- **Priority:** P2
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Fill Title, then clear the Due Date input.
  3. Click "Create Task".
- **Expected:** `FormDatePicker` sets the field to `null`, `z.date()` rejects it, an inline error is shown under "Due Date" and no request is sent. The URL stays `/tasks/new`.
- **Known issue:** The model's `DueDate` is a nullable pointer and the API accepts a task with no due date, so the UI is stricter than the API here. A task created without a due date is not renderable in the list — see TC-TASK-034.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-014 — A non-admin cannot pick an assignee, so the create form is unusable for them
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A sales or support user created via admin `POST /users` with `generateUserData()`; that user logged in.
- **Steps:**
  1. Navigate to `/tasks/new`.
  2. Open the "Assign To (Optional)" autocomplete.
  3. Fill Title and Due Date and click "Create Task".
- **Expected:** `TaskForm` unconditionally issues `GET /api/v1/users?is_active=true` (`TaskForm.tsx:76-79`), which is admin-only (`routes.go:13`), so the request returns **403** and the autocomplete shows "No options". The submitted body has no `assigned_to_id`, so `POST /tasks` returns **400** with "Failed to create task". A sales or support user therefore cannot create a task through the UI at all, even though `TaskHandler.Create` explicitly permits both roles (`task_handler.go:69-72`).
- **Known issue:** Not recorded in FEATURES.md row 6.2. The backend intends non-admins to self-assign; the form has no "assign to me" affordance.
- **Automation:** blocked — needs a non-admin role-login helper

### TC-TASK-015 — Customer role is refused task creation by the API
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Customer account logged in.
- **Steps:**
  1. Navigate to `/tasks` and click "Create Task" (the button is rendered for every role).
  2. Fill Title and Due Date and submit.
- **Expected:** `POST /api/v1/tasks` returns **403** with the message "Only admin, support, and sales users can create tasks" (`task_handler.go:70`). The UI shows the generic "Failed to create task" snackbar — the server's message is not surfaced — and stays on `/tasks/new`.
- **Known issue:** The route is reachable and the button visible for a role the handler always refuses; the frontend applies no `requiredRole` to the `tasks/*` routes (`routes/index.tsx:97-111`).
- **Automation:** blocked — needs a customer-role login helper

### TC-TASK-016 — Lead and customer links are not reachable from the UI
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks/new` and inspect every field.
  2. Open an existing task's edit form and inspect every field.
- **Expected:** The form exposes Title, Description, Status, Priority, Due Date and Assign To only. There is no lead or customer picker, even though `CreateTaskRequest`/`UpdateTaskRequest` accept `lead_id` and `customer_id` and the service validates them and rejects setting both (`task_service.go:52-73`, `apperrors.ErrTaskLeadCustomerConflict` → 400). FEATURES.md row 6.2 describes the form as having a "related lead/customer" field; it does not.
- **Known issue:** FEATURES.md row 6.2 overstates the form. The lead/customer conflict rule can only be exercised through the API.
- **Automation:** blocked — UI does not expose the relation; API-level coverage lives in `internal/integration/task_integration_test.go`

---

## 6.3 Edit Task

### TC-TASK-017 — Admin edits a task's title
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task created by the test (TC-TASK-007).
- **Steps:**
  1. Navigate to `/tasks` and click the edit (pencil) icon on that task's row.
  2. Clear the Title and type a new one.
  3. Click "Update Task".
- **Expected:** `PUT /api/v1/tasks/{id}` returns 200, the snackbar reads "Task updated successfully" and the app navigates back to `/tasks` with the new title in the row.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin can edit an existing task"

### TC-TASK-018 — Drive a task through pending → in_progress → completed from the detail page
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a freshly created task (status `pending`).
- **Steps:**
  1. Open `/tasks/{id}`.
  2. Click "Start Task".
  3. Click "Complete".
- **Expected:** Each click issues `PUT /api/v1/tasks/{id}` with only `{status}` and returns 200 with the snackbar "Task status updated successfully". After step 2 the header chip reads "In Progress" (amber) and a "Task started" entry appears in Status History; after step 3 it reads "Completed" (green), the "Complete" and "Cancel Task" buttons disappear and a "Task completed" entry appears. `completed_at` is synthesised client-side from `updated_at` (`api/endpoints/tasks.ts:39`) — the API has no such column.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended; the existing "admin can track task progress through status changes" only performs the pending → in_progress step through the edit form)

### TC-TASK-019 — Non-admin editing their own task while echoing the current assignee succeeds
- **Priority:** P0
- **Type:** regression
- **Preconditions:** Admin creates a support user and a task assigned to that user; support user logged in.
- **Steps:**
  1. Open `/tasks/{id}/edit` for the task assigned to the support user.
  2. Change the Description only.
  3. Click "Update Task".
- **Expected:** `PUT /api/v1/tasks/{id}` returns **200**, not 403, even when the body repeats the current `assigned_to_id`. `TaskHandler.Update` only treats an assignee change as a reassignment (`task_handler.go:443`).
- **Known issue:** This was G30 in the FEATURES.md Gap Summary — non-admins used to be 403'd for editing their own task. The Go regression test is `TestUpdateTask_NonAdminSameAssignee_Success`; there is no E2E equivalent.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended; needs a non-admin role-login helper)

### TC-TASK-020 — Non-admin reassigning a task to another user is forbidden
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Admin creates a support user plus a second user, and a task assigned to the support user; support user logged in.
- **Steps:**
  1. Issue `PUT /api/v1/tasks/{id}` with `{"assigned_to_id": <other user id>}` using the authenticated request context.
- **Expected:** **403** with "Only admins can reassign tasks" (`task_handler.go:445`). The stored assignee is unchanged.
- **Known issue:** Not reproducible through the UI: the edit form's assignee autocomplete is empty for non-admins (TC-TASK-014), so this must be driven at the API level.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-021 — Admin reassigns a task to another user
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task created by the test; a second active user created via `POST /users` with `generateUserData()`.
- **Steps:**
  1. Open `/tasks/{id}/edit`.
  2. Open "Assign To (Optional)" and select the second user.
  3. Click "Update Task".
- **Expected:** `PUT /api/v1/tasks/{id}` returns 200 and the stored `assigned_to_id` is the second user's id. The list still shows "Unassigned" in the Assigned To column (TC-TASK-002), so the change must be asserted on the response body, not on the table.
- **Known issue:** The edit form does not pre-select the current assignee, because `TaskForm.tsx:119-121` reads `task.assignee`, a key the transform never populates. The autocomplete is blank when editing an assigned task, and leaving it blank sends no `assigned_to_id`, which the handler treats as "no reassignment" (`task_handler.go:443`) — so the assignee survives, by luck rather than design.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-022 — A completed task cannot be moved to another status
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; a task created by the test and driven to `completed` (TC-TASK-018).
- **Steps:**
  1. Open `/tasks/{id}/edit`.
  2. Change Status to "Pending".
  3. Click "Update Task".
- **Expected:** `PUT /api/v1/tasks/{id}` returns **400** ("cannot change status of completed task", `task_service.go:188-191` → `RespondBadRequest`). The snackbar reads "Failed to update task" and the browser stays on the edit form. Re-saving the same task without touching Status returns 200, because the rule only fires when the target status differs from `completed`.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-023 — Clearing the description does not clear it
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; a task created with a non-empty description.
- **Steps:**
  1. Open `/tasks/{id}/edit`.
  2. Clear the Description textarea.
  3. Click "Update Task", then open `/tasks/{id}`.
- **Expected:** `PUT /api/v1/tasks/{id}` returns **200** with the success snackbar, but the description is unchanged — the detail page still shows the original text.
- **Known issue:** `TaskHandler.Update` applies a field only when it is non-empty (`if req.Description != ""`, `task_handler.go:429-431`); the same applies to Title. There is no way to blank either field through the API. The UI reports success regardless.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-024 — Mark a task complete from the list row menu
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; a pending task created by the test.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Open the row overflow menu and click "Mark as Complete".
- **Expected:** `PUT /api/v1/tasks/{id}` with `{status:"completed"}` returns 200, the snackbar reads "Task marked as completed", the tasks query is invalidated and the row's Status chip becomes "Completed". The menu item is hidden when the selected task is already completed (`TaskList.tsx:434`).
- **Known issue:** The overflow menu is wired to `selectedTask`, which is only set by `handleMenuOpen`, and the `IconButton` that would open it is itself rendered inside the `actions` slot and guarded by `selectedTask &&` (`TaskList.tsx:417-422`) — the menu cannot be opened from a cold page load. Exercising this path may require the calendar/detail route instead.
- **Automation:** blocked — the overflow menu is not reachable through normal interaction

---

## 6.4 Delete Task

### TC-TASK-025 — Admin deletes a task
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task created by this test (never a pre-existing row).
- **Steps:**
  1. Navigate to `/tasks` and locate the created task's row.
  2. Click the delete (bin) icon.
  3. Confirm in the "Delete Task" dialog.
- **Expected:** `DELETE /api/v1/tasks/{id}` returns **204**. The snackbar reads "Task deleted successfully", the dialog closes, the tasks query is invalidated and the row is gone from the list; `total` drops by one.
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin can delete a task"

### TC-TASK-026 — Delete confirmation can be dismissed without deleting
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; at least one task exists.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the delete icon on the first row.
  3. Click "Cancel" in the dialog.
- **Expected:** The dialog is titled "Delete Task" and its body reads `Are you sure you want to delete the task "<title>"? This action cannot be undone.` Cancelling hides the dialog, sends no `DELETE` request and leaves the row in place.
- **Automation:** automated — `gocrm-ui/e2e/screenshots/06-tasks.spec.ts` "05 - delete confirmation"

### TC-TASK-027 — Non-admin sees the delete control but is refused by the API
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** Support user logged in; a task assigned to them.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the delete icon on their own task and confirm.
- **Expected:** The delete icon is rendered (the list passes `onDelete` unconditionally, `TaskList.tsx:414`). `DELETE /api/v1/tasks/{id}` returns **403** with "Only administrators can delete tasks" (`task_handler.go:500`) — the role check runs before the id is even parsed. The snackbar reads "Failed to delete task", the dialog stays open and the task survives.
- **Known issue:** Frontend and backend disagree: the backend is admin-only, the UI offers the control to every role.
- **Automation:** blocked — needs a non-admin role-login helper

### TC-TASK-028 — Deletion is a plain soft delete, and the detail page of a deleted task spins forever
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; a task created and then deleted by this test (TC-TASK-025), with its id captured from the create response.
- **Steps:**
  1. Navigate directly to `/tasks/{deleted id}`.
- **Expected:** `GET /api/v1/tasks/{id}` returns **404** ("Task not found"). The page renders the `Loading` spinner indefinitely, because `TaskDetail` returns `<Loading/>` whenever `task` is falsy and never handles the error branch (`TaskDetail.tsx:118-120`). No error message and no redirect. Unlike users, customers and leads, no personal data is overwritten — the row keeps its title and description behind `deleted_at` (FEATURES.md row 6.4; `bulk_erasure_test.go` `TestBulkDeleteTasksAndTicketsRemainPlainSoftDeletes`).
- **Known issue:** A 404 on the detail route is indistinguishable from a slow load.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

---

## 6.5 Filter by Status / Priority

### TC-TASK-029 — The Status dropdown does not filter anything
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; tasks in at least two different statuses exist (a fresh `pending` task plus one completed via TC-TASK-018).
- **Steps:**
  1. Navigate to `/tasks` and record the footer total and the visible Status chips.
  2. Select Status = "Completed".
  3. Wait for the refetch and re-read the table.
- **Expected:** A request goes out as `GET /api/v1/tasks?page=1&limit=10&status=completed`, returns 200, and the result set is **identical** to the unfiltered one: the same total and the same mix of Pending and Completed rows. The dropdown shows "Completed" but the table is unchanged.
- **Known issue:** `TaskHandler.List` reads only `page`, `offset`, `limit`, `sort_by`, `sort_order` and `search` (`task_handler.go:194-223`) — `status` is ignored server-side. Unlike the ticket list, `TaskList` also performs **no** client-side narrowing: it renders `data?.data` straight into `DataTable`, which does not filter (`TaskList.tsx:404`, `components/DataTable.tsx`). Confirmed live: `GET /tasks` and `GET /tasks?status=completed&priority=high` both return `total: 39` with mixed statuses. FEATURES.md row 6.5 and gap **G34** call this "client-side" filtering; that is inaccurate for tasks — no filtering happens anywhere. The existing spec "admin can filter tasks by status" only asserts `count >= 0`, so it passes without ever checking that filtering occurred.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended; the existing test must be strengthened to assert the no-op rather than a tautology)

### TC-TASK-030 — The Priority dropdown does not filter anything
- **Priority:** P1
- **Type:** regression
- **Preconditions:** Admin logged in; tasks of at least two priorities exist (TC-TASK-010).
- **Steps:**
  1. Navigate to `/tasks` and record the visible Priority chips.
  2. Select Priority = "High".
- **Expected:** `GET /api/v1/tasks?…&priority=high` returns 200 and Low, Medium and High rows all remain on screen; the total is unchanged.
- **Known issue:** Same root cause as TC-TASK-029 — `priority` is not read by the handler and the page performs no local filtering. G34.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-031 — Combining search with a filter drops neither, because only search is honoured
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; a task titled `SearchTask_<timestamp>` in status `pending`.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Type the unique title fragment into "Search tasks...".
  3. Select Status = "Completed".
- **Expected:** The request carries both `search` and `status`; the response is filtered by `search` alone, so the `pending` task remains visible despite the "Completed" filter.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

---

## 6.6 Search Tasks

### TC-TASK-032 — Admin searches tasks by title
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; a task created by the test with a unique title such as `SearchTask_<timestamp>`.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Type the unique fragment into the "Search tasks..." field.
- **Expected:** Each keystroke resets `page` to 1 and issues `GET /api/v1/tasks?…&search=<fragment>` (there is no debounce). The response contains the matching task and the table narrows to it. Search takes precedence over `sort_by` (`task_handler.go:232-236`).
- **Automation:** automated — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` "admin can search tasks" (currently asserts nothing about the result set; strengthening is tracked under TC-TASK-033)

### TC-TASK-033 — Search with no matches empties the table
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Type a string that cannot match, e.g. `zzz_no_such_task_<timestamp>`.
- **Expected:** 200 with `total: 0`, the table body is empty and the footer reads "0–0 of 0". Clearing the field restores the full list.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-034 — Search is silently ignored for non-admin users
- **Priority:** P1
- **Type:** negative
- **Preconditions:** A support user with at least two assigned tasks with distinct titles; support user logged in.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Search for a fragment matching exactly one of the two tasks.
- **Expected:** The request carries `search`, returns 200, but **both** tasks remain listed: the non-admin branch calls `GetByAssignee` before the search branch is ever reached (`task_handler.go:230-231`), so `search`, `sort_by` and `sort_order` are all discarded for non-admins. The search box gives no indication that it did nothing.
- **Known issue:** Documented in the swagger description for `GET /tasks` but not in FEATURES.md row 6.6.
- **Automation:** blocked — needs a non-admin role-login helper

---

## 6.7 Sort Tasks

### TC-TASK-035 — Sort the list by an allowlisted column
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; at least three tasks with different titles and due dates.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the "Title" column header, then click it again.
  3. Click the "Due Date" header.
- **Expected:** Each click issues `GET /api/v1/tasks?…&sort_by=title&sort_order=asc|desc` (then `sort_by=due_date`) and resets `page` to 1. The rows reorder accordingly. Allowed columns are `created_at`, `updated_at`, `title`, `status`, `priority`, `due_date` (`task_handler.go:207-214`).
- **Known issue:** FEATURES.md row 6.7 is *partial* — there is no E2E sort coverage for tasks (compare gap **G21** for leads).
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-036 — Sorting by "Assigned To" is accepted by the UI and ignored by the API
- **Priority:** P2
- **Type:** negative
- **Preconditions:** Admin logged in; several tasks with different assignees.
- **Steps:**
  1. Navigate to `/tasks`.
  2. Click the "Assigned To" column header.
- **Expected:** `DataTable` marks the column sorted and issues `sort_by=assignee`. `assignee` is not in the allowlist, so the handler blanks `sortBy` (`task_handler.go:216-218`) and falls back to the unsorted `List`. The header shows a sort arrow while the row order is unchanged. No 400 is returned.
- **Known issue:** Every column is sortable by default in `DataTable` (`sortable !== false`), including ones the API cannot sort on.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

---

## 6.8 Due Date Management

### TC-TASK-037 — Due date round-trips through create, list and detail
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; `generateTaskData()` (future `dueDate`).
- **Steps:**
  1. Create the task with that due date.
  2. Read the "Due Date" cell on `/tasks`.
  3. Open `/tasks/{id}`.
  4. Open `/tasks/{id}/edit`.
- **Expected:** The form posts `due_date` as a full ISO-8601 timestamp (`TaskForm.tsx:128`, midnight local time). The list renders `MMM dd, yyyy`; the detail page renders `MMM dd, yyyy (in N months)`; the edit form's date input is repopulated as `yyyy-MM-dd` with the same calendar day.
- **Known issue:** FEATURES.md row 6.8 notes there is no dedicated E2E due-date test; the previously claimed "manage due dates" / "date validation" spec titles do not exist.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended)

### TC-TASK-038 — A task with no due date breaks list rendering
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in. Create a task via the API (`POST /api/v1/tasks` with `title` and `assigned_to_id` only, no `due_date`) using the authenticated `request` fixture, so it lands on the first page of the list.
- **Steps:**
  1. Navigate to `/tasks`.
- **Expected:** `due_date` is `omitempty` on the model (`internal/models/task.go:25`) so the field is absent from the response. The Due Date column formatter calls `format(new Date(undefined), …)` (`TaskList.tsx:179`), which throws `RangeError: Invalid time value`; the ErrorBoundary catches it and the table does not render. The calendar view fails the same way through `parseISO` (`TaskList.tsx:253`).
- **Known issue:** The frontend `Task` type declares `due_date: string` as non-optional while the API treats it as nullable. Not recorded in FEATURES.md.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended; the fixture task must be deleted in an `afterAll` so the page is left usable)

---

## 6.9 My Tasks

### TC-TASK-039 — No UI view calls GET /tasks/my
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in.
- **Steps:**
  1. Navigate through the sidebar and the Tasks page looking for a "My Tasks" view.
  2. Record every task-related request the SPA makes.
- **Expected:** No "My Tasks" navigation entry, route or filter exists, and `GET /api/v1/tasks/my` is never requested — the endpoint has no caller anywhere in `gocrm-ui/src`. For non-admins, `/tasks` is already implicitly "my tasks" (TC-TASK-005), which is why the dedicated view was never built.
- **Known issue:** Gap **G18** ("No E2E test for 'My Tasks' / 'My Tickets' views") cannot be closed at the UI level, because there is no view to test. FEATURES.md row 6.9 describes it as a feature; only the endpoint exists.
- **Automation:** blocked — no UI surface; the endpoint is covered by `task_handler_test.go` `TestGetMyTasks_NonAdminCanAccess` and `internal/integration/task_integration_test.go` `TestMyTasks`

### TC-TASK-040 — GET /tasks/my returns only the caller's tasks, admins included
- **Priority:** P2
- **Type:** rbac
- **Preconditions:** Admin logged in; at least one task assigned to the admin and one assigned to another user.
- **Steps:**
  1. Issue `GET /api/v1/tasks/my` with the admin's bearer token via the `request` fixture.
  2. Issue `GET /api/v1/tasks` with the same token.
- **Expected:** Step 1 returns 200 with only the admin's own tasks — `ListMyTasks` calls `GetByAssignee` unconditionally and does not special-case admins (`task_handler.go:288`). Step 2 returns every task. The two totals differ. This closes **G18** at the API level.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-041 — GET /tasks/upcoming excludes overdue and completed tasks and clamps the window
- **Priority:** P2
- **Type:** functional
- **Preconditions:** Admin logged in; three tasks created by the test — one due in 3 days, one due yesterday, one due in 3 days and then completed.
- **Steps:**
  1. `GET /api/v1/tasks/upcoming` (no parameters).
  2. `GET /api/v1/tasks/upcoming?days=500`.
  3. `GET /api/v1/tasks/upcoming?days=abc`.
- **Expected:** A bare array in `data`, ordered by due date ascending. Step 1 uses a 7-day window from *now*, so it contains the first task and excludes both the overdue one and the completed one (`task_handler.go:321-354`). Step 2 clamps to 90 days rather than returning 400; step 3 falls back to 7. At most 100 tasks are returned.
- **Known issue:** `tasksApi.getUpcomingTasks` exists in `gocrm-ui/src/api/endpoints/tasks.ts:104` but has no caller — the dashboard's "Upcoming Tasks" panel uses `GET /dashboard/upcoming-tasks` instead (`gocrm-ui/src/api/endpoints/dashboard.ts:61`). The two endpoints are unrelated code paths.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-042 — The user detail page's task list is not filtered by that user
- **Priority:** P1
- **Type:** negative
- **Preconditions:** Admin logged in; at least two users, each with at least one task assigned.
- **Steps:**
  1. Navigate to `/users/{id}` for one of them.
  2. Open the tasks tab.
- **Expected:** `GET /api/v1/tasks?assigned_to={id}` returns 200 with **every** task in the system, because `assigned_to` is not a parameter the handler reads. The tab therefore lists other users' tasks as if they belonged to this user.
- **Known issue:** `UserDetail.tsx:94-97` sends a filter the API ignores; same root cause as TC-TASK-029. Not recorded in FEATURES.md.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-users.spec.ts` (extended)

---

## POST /tasks/bulk/status

### TC-TASK-043 — Admin bulk-updates several tasks in one call
- **Priority:** P1
- **Type:** functional
- **Preconditions:** Admin logged in; three pending tasks created by the test, ids captured.
- **Steps:**
  1. `POST /api/v1/tasks/bulk/status` with `{"task_ids":[a,b,c],"status":"in_progress"}` via the `request` fixture.
  2. Reload `/tasks`.
- **Expected:** 200 with a `BulkStatusUpdateResult` payload; all three rows show the "In Progress" chip. The write is a single all-or-nothing transaction.
- **Known issue:** No UI calls this route — `tasksApi.bulkUpdateStatus` (`api/endpoints/tasks.ts:100`) has no caller and the task list has no multi-select. Per the repository's standing directive the endpoint function is intended contract, not dead code; the missing piece is the UI.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-044 — Bulk status rejects malformed batches
- **Priority:** P1
- **Type:** validation
- **Preconditions:** Admin logged in.
- **Steps:**
  1. POST with `{"task_ids":[],"status":"pending"}`.
  2. POST with 101 ids.
  3. POST with `{"task_ids":[<valid id>],"status":"archived"}`.
- **Expected:** All three return **400** from the binding tags `required,min=1,max=100,dive,gt=0` and `oneof=pending in_progress completed cancelled` (`bulk_handler.go:382-385`). Nothing is written.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-045 — A batch naming a missing or already-completed task fails whole and names the ids
- **Priority:** P0
- **Type:** negative
- **Preconditions:** Admin logged in; one pending task and one completed task created by the test; one id known not to exist.
- **Steps:**
  1. POST `{"task_ids":[<pending id>, 999999999],"status":"in_progress"}`.
  2. POST `{"task_ids":[<pending id>, <completed id>],"status":"pending"}`.
  3. Re-read the pending task.
- **Expected:** Step 1 returns **404** and step 2 returns **400**; in both cases `error.details` names the offending ids (`respondBulkStatusError`, `bulk_handler.go:520-543`). Step 3 shows the pending task still `pending` — the transaction rolled back, so a partially valid batch writes nothing.
- **Automation:** planned — `gocrm-ui/e2e/tests/admin-tasks.spec.ts` (extended, API-level via `request` fixture)

### TC-TASK-046 — Bulk status is not role-gated at the route, only per item
- **Priority:** P0
- **Type:** rbac
- **Preconditions:** A support user with one assigned task; a second task assigned to someone else; support user's token available.
- **Steps:**
  1. POST `{"task_ids":[<own task id>],"status":"in_progress"}` as the support user.
  2. POST `{"task_ids":[<own task id>, <other user's task id>],"status":"in_progress"}` as the support user.
- **Expected:** Step 1 returns **200** — unlike the ticket variant, `BulkUpdateTaskStatus` performs no role pre-check and binds the body first (`bulk_handler.go:494-500`), so every authenticated role including `customer` reaches the service. Step 2 returns **403** with the other user's task id listed in `error.details`, and the caller's own task is left untouched.
- **Known issue:** The asymmetry with `BulkUpdateTicketStatus`, which rejects `customer` and `sales` before reading the body, is deliberate but undocumented in FEATURES.md.
- **Automation:** blocked — needs a non-admin role-login helper to obtain a support token
