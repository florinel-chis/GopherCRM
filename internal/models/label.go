package models

// Label is a free-form, colored tag that can be attached to tasks so they can
// be grouped ad hoc without introducing a project hierarchy.
//
// Labels are hard-deleted rather than soft-deleted. They carry no personal
// data, so the right-to-erasure machinery does not apply to them, and hard
// deletion sidesteps the trap documented for users and customers: a unique
// index on `name` is not scoped to `deleted_at`, so a soft-deleted label would
// keep its name reserved forever.
type Label struct {
	BaseModel
	Name  string `gorm:"not null;type:varchar(50);uniqueIndex" json:"name"`
	Color string `gorm:"not null;type:varchar(7)" json:"color"`
	// Tasks is the inverse side of Task.Labels. It exists so the repository can
	// manage the join table through GORM's association API; it is never
	// serialized, both to keep the payload small and to avoid a cycle with
	// Task.Labels.
	Tasks []Task `gorm:"many2many:task_labels" json:"-"`
	// TaskCount is how many live tasks carry the label. It is computed by the
	// repository, not stored: `gorm:"-"` keeps it out of both the schema and
	// every generated statement.
	TaskCount int64 `gorm:"-" json:"task_count"`
}
