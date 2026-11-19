package models

import "testing"

func TestTaskEnumIsValid(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		cases := map[TaskStatus]bool{
			TaskStatusPending:    true,
			TaskStatusInProgress: true,
			TaskStatusCompleted:  true,
			TaskStatusCancelled:  true,
			"":                   false,
			"Completed":          false,
			"PENDING":            false,
			"done":               false,
			"urgent":             false,
		}
		for value, want := range cases {
			if got := value.IsValid(); got != want {
				t.Errorf("TaskStatus(%q).IsValid() = %v, want %v", value, got, want)
			}
		}
	})

	t.Run("priority", func(t *testing.T) {
		cases := map[TaskPriority]bool{
			TaskPriorityLow:    true,
			TaskPriorityMedium: true,
			TaskPriorityHigh:   true,
			"":                 false,
			"HIGH":             false,
			"Low":              false,
			"urgent":           false,
			"done":             false,
		}
		for value, want := range cases {
			if got := value.IsValid(); got != want {
				t.Errorf("TaskPriority(%q).IsValid() = %v, want %v", value, got, want)
			}
		}
	})
}
