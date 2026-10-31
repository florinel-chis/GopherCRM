package models

// Label is a free-form, colored tag that can be attached to tasks so they can
// be grouped ad hoc without introducing a project hierarchy.
//
// Labels are hard-deleted rather than soft-deleted. They carry no personal
// data, so the right-to-erasure machinery does not apply to them, and hard
// deletion sidesteps the trap documented for users and customers: a unique
// index on `name` is not scoped to `deleted_at`, so a soft-deleted label would
// keep its name reserved forever.
//
// @Description A coloured tag that can be attached to tasks. Labels are hard-deleted.
type Label struct {
	BaseModel
	// Display name, trimmed, 1 to 50 characters and unique case-insensitively.
	Name string `gorm:"not null;type:varchar(50);uniqueIndex" json:"name" example:"Follow-up" maxLength:"50"`
	// Colour as #RRGGBB (six hex digits behind a hash).
	Color string `gorm:"not null;type:varchar(7)" json:"color" example:"#FF8800"`
	// Tasks is the inverse side of Task.Labels. It exists so the repository can
	// manage the join table through GORM's association API; it is never
	// serialized, both to keep the payload small and to avoid a cycle with
	// Task.Labels.
	Tasks []Task `gorm:"many2many:task_labels" json:"-"`
	// Number of live (not soft-deleted) tasks carrying the label, computed on
	// read and never stored. Filled in by GET /labels and PUT /labels/{id}; 0
	// in the POST /labels response and in labels embedded in a task. Read-only.
	TaskCount int64 `gorm:"-" json:"task_count" example:"4" readonly:"true"`
}
