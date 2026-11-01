package models

import (
	"encoding/json"
	"time"
)

// TaskStatus is the workflow status of a task. A new task always starts as
// pending, and a completed task cannot be moved to any other status.
//
// @Description Workflow status of a task. A new task always starts as pending; a completed task cannot be moved to any other status.
type TaskStatus string

// TaskPriority is the priority of a task; medium when not given on create.
//
// @Description Priority of a task; medium when not given on create.
type TaskPriority string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusCancelled  TaskStatus = "cancelled"

	TaskPriorityLow    TaskPriority = "low"
	TaskPriorityMedium TaskPriority = "medium"
	TaskPriorityHigh   TaskPriority = "high"
)

// @Description A to-do item assigned to one user, optionally linked to a lead or a customer (never both) and to a deal, and carrying labels.
type Task struct {
	BaseModel
	// Short summary of the task. Always present. The column is 255 characters wide; the API does not check the length before the write.
	Title string `gorm:"not null;type:varchar(255)" json:"title" example:"Call back about the renewal"`
	// Free-text details; an empty string when none were given.
	Description string `gorm:"type:text" json:"description" example:"Customer asked for a quote on the 50-seat plan."`
	// Workflow status. New tasks always start as pending; a completed task cannot move to another status.
	Status TaskStatus `gorm:"not null;default:'pending';type:varchar(20)" json:"status" example:"pending"`
	// Priority; medium when not given on create.
	Priority TaskPriority `gorm:"not null;default:'medium';type:varchar(20)" json:"priority" example:"medium"`
	// Due date and time (RFC 3339). Omitted when the task has no due date; once set it cannot be cleared through PUT /tasks/{id}.
	DueDate *time.Time `json:"due_date,omitempty" example:"2026-11-05T09:00:00Z"`
	// ID of the user the task is assigned to. Non-admins may only create tasks for themselves and only admins may change it.
	AssignedToID uint `json:"assigned_to_id" example:"7"`
	// The assignee's user record. Always present in the JSON. GET /tasks/{id}, PUT /tasks/{id}, GET /tasks/upcoming and the admin and label-filtered GET /tasks listings fill it in with the User object (see the User schema). POST /tasks, GET /tasks/my and the non-admin GET /tasks without label_id return a zero-valued placeholder instead (id 0, every string empty, role "" included, timestamps 0001-01-01T00:00:00Z), which is not a valid User, so this property is documented as a plain object. Use assigned_to_id.
	AssignedTo User `gorm:"foreignKey:AssignedToID" json:"assigned_to,omitempty" swaggertype:"object"`
	// ID of the linked lead. Omitted when the task has no lead link, and also when the caller may not see that lead (admins see every lead, sales users only the leads they own, every other role none); this is decided on every read. A task links a lead or a customer, never both.
	LeadID *uint `json:"lead_id,omitempty" example:"12"`
	// The linked lead, under the same visibility rule as lead_id: omitted whenever lead_id is omitted.
	Lead *Lead `gorm:"foreignKey:LeadID" json:"lead,omitempty"`
	// ID of the linked customer. Omitted when the task has no customer link. A task links a lead or a customer, never both.
	CustomerID *uint `json:"customer_id,omitempty" example:"3"`
	// The linked customer. Only loaded by GET /tasks/{id} and PUT /tasks/{id} (and omitted from the PUT response when that request changed customer_id); omitted everywhere else.
	Customer *Customer `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
	// ID of the linked deal, independent of the lead or customer link. Omitted when the task has no deal link, and also when the caller may not see that deal (admins see every deal, sales users only the deals they own, every other role none); this is decided on every read. An admin still receives it after the deal was deleted; every other role does not.
	DealID *uint `gorm:"index" json:"deal_id,omitempty" example:"88"`
	// Deal is what the visibility decision and DealSummary are made from. It is
	// never serialised itself: a deal carries its amount, notes, owner, lead and
	// customer, and a task discloses none of them.
	Deal *Deal `gorm:"foreignKey:DealID" json:"-"`
	// Summary of the linked deal, under the same visibility rule as deal_id: omitted whenever deal_id is omitted, and also when the deal was deleted.
	DealSummary *QuoteDealSummary `gorm:"-" json:"deal,omitempty"`
	// Labels attached to the task. Omitted when the task has none, and not loaded (so omitted) by GET /tasks/upcoming. The embedded labels carry task_count 0.
	Labels []Label `gorm:"many2many:task_labels" json:"labels,omitempty"`
}

// MarshalJSON replaces the preloaded deal by its summary. The summary comes
// from the preloaded Deal only, so a task whose deal is not loaded, or was
// removed by the redaction, has no deal key whatever DealSummary holds.
func (t Task) MarshalJSON() ([]byte, error) {
	type plain Task
	out := plain(t)
	out.DealSummary = nil
	if t.DealID != nil && t.Deal != nil {
		out.DealSummary = &QuoteDealSummary{ID: t.Deal.ID, Title: t.Deal.Title, Stage: t.Deal.Stage}
	}
	return json.Marshal(out)
}
