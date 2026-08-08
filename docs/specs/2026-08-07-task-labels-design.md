# Task labels — design

2026-08-07. Free-form colored labels on tasks so they can be grouped ad hoc
(e.g. by initiative) without introducing a project hierarchy.

## Data model

- New entity `Label`: `BaseModel`, `Name` (`varchar(50)`, required, unique
  index), `Color` (`varchar(7)`, required, `#RRGGBB`).
- `Task.Labels []Label` via GORM `many2many:task_labels` join table.
- `&Label{}` added to `MigrateDatabase()`.
- **Labels hard-delete** (`Unscoped`), clearing their `task_labels` rows in the
  same transaction. Labels carry no PII, and hard deletion sidesteps the
  documented soft-delete-vs-unique-index trap on `name`.
- Name uniqueness: DB unique index is the backstop; the service additionally
  does a case-insensitive existence check (`LOWER(name) = LOWER(?)`) so MySQL
  (CI collation) and SQLite (CS) behave identically. Names are trimmed.

## API

All under the authenticated `/api/v1` group, unified `APIResponse` envelope.

| Route | Auth | Behaviour |
|---|---|---|
| `GET /labels` | any authenticated | all labels, ordered by name; each with `task_count` |
| `POST /labels` | admin, sales, support | create `{name, color}`; 409 on duplicate name |
| `PUT /labels/:id` | admin, sales, support | rename / recolor; 409 on duplicate |
| `DELETE /labels/:id` | admin | hard delete + detach from all tasks |

Task changes:

- `POST /tasks` and `PUT /tasks/:id` accept optional `label_ids []uint`;
  unknown ids → 400 `INVALID_REFERENCE`. `PUT` with `label_ids` replaces the
  set; omitted field leaves labels untouched.
- Task responses (get/list/my) include `labels`.
- `GET /tasks?label_id=N` filters; combinable with sort. New repository/service
  methods (`ListByLabel`, `CountByLabel`, …) — existing interface method
  signatures stay unchanged so current test suites keep compiling.
- New sentinel `ErrDuplicateLabelName` → 409; duplicate detection reuses
  `duplicate_key.go` driver checks (MySQL 1062 / SQLite message).
- Swagger annotations on every new/changed handler; `make swagger` regenerated.

## Frontend (gocrm-ui)

- `types`: `Label {id, name, color, task_count?}`; `Task.labels?: Label[]`.
- `api/endpoints/labels.ts`: CRUD; `tasks.ts` passes `label_ids` and
  `label_id` filter through.
- `LabelChip` shared component: colored MUI chip, text color picked by
  luminance for contrast.
- **TaskForm**: multiple-select Autocomplete of labels rendering colored
  chips, with inline creation ("Add \"xyz\"") that POSTs the new label with
  an auto-assigned color from a preset palette.
- **TaskList**: labels column with chips; clicking a chip filters the list by
  that label; a label filter dropdown sits next to the existing filters with a
  visible clear affordance.
- **TaskDetail**: label chips shown.
- **Label management page** at `/labels` (nav entry): table of name / color
  swatch / task count, create+edit dialog with a preset color-swatch picker
  (free hex input as fallback), delete confirm dialog that states how many
  tasks the label will be removed from. Route gated like task pages (any
  authenticated role except customer? — match tasks: authenticated; delete
  button only for admin, create/edit hidden for customer role).

## Tests

- Go: service + handler unit tests (testify, mocks), repository integration
  tests on SQLite (join semantics, hard delete, filter).
- Vitest: LabelChip contrast + labels endpoint module.
- Playwright: `e2e/tests/labels.spec.ts` — create label, create task with
  labels, chips visible in list/detail, filter by label, edit color, delete
  label detaches it. Screenshot suite gains `09-labels.spec.ts`; task captures
  re-run since the list gains a column; `docs/SCREENSHOTS.md` updated.

## Out of scope

Label descriptions, per-user labels, labels on other entities, bulk label
assignment, label merge.

## Lessons learned (2026-08-08 retrospective)

Written after the feature shipped, was reviewed, and was verified end-to-end in
the docker stack.

1. **Hard delete was the right call, and cheap to prove.** The repository
   integration tests show a deleted label's name is immediately reusable and
   its `task_labels` rows are gone. With the usual soft delete, the unique
   `name` index would have collided with `deleted_at IS NULL` semantics — the
   same NULL-distinct trap already documented for emails — for zero benefit,
   since labels hold no personal data.
2. **Declare honest limitations instead of faking combinations.** The server
   cannot combine `label_id` with `search`; the label filter wins. Rather than
   letting the UI display both as active while one is silently ignored, the
   search box is disabled (with helper text) while a label filter is applied,
   and the swagger annotation states the precedence. An API consumer reading
   the spec and a user reading the screen now see the same truth.
3. **Inline creation needs a concurrent-duplicate path.** With the labels list
   cached for minutes, "Add \"X\"" can race a label created elsewhere and come
   back 409. Surfacing that as an error dead-ends a user who did nothing
   wrong; the form now refetches and selects the existing label. Any
   create-from-autocomplete flow should treat 409 as "select, don't fail".
4. **A cleared collection serialises as an absent key.** `Task.Labels` uses
   `json:"labels,omitempty"`, so a task whose labels were just cleared returns
   no `labels` field at all. The frontend transform normalises absence to
   `[]`; every other consumer must do the same or treat the two as equal.
5. **Deployment verification must speak browser, not curl.** The feature's
   API worked perfectly by curl while every browser login failed: the CORS
   middleware rejects unlisted `Origin` headers with a bare 403 that never
   reaches the request logger, and curl sends no Origin. The gap only
   surfaced because the dockerised UI was exercised through a real browser
   as the final step — which is why that step exists.
6. **Keeping interface signatures stable paid off.** All label-filtered
   listing went into new repository/service methods instead of widening
   existing ones. The entire pre-existing backend test suite compiled and
   passed untouched, which made every remaining failure informative.
