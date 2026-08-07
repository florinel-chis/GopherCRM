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
