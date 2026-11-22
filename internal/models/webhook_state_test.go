package models

import (
	"testing"
	"time"
)

func uintPtr(v uint) *uint { return &v }

func TestWebhookStateNullableIDs(t *testing.T) {
	cases := []struct {
		name  string
		a, b  *uint
		equal bool
	}{
		{"nil and nil are equal", nil, nil, true},
		{"nil and set differ", nil, uintPtr(3), false},
		{"set and nil differ", uintPtr(3), nil, false},
		{"different values differ", uintPtr(3), uintPtr(4), false},
		{"distinct pointers to one value are equal", uintPtr(3), uintPtr(3), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leadA, leadB := &Lead{CompanyID: tc.a, CustomerID: tc.a}, &Lead{CompanyID: tc.b, CustomerID: tc.b}
			if got := leadA.WebhookState() == leadB.WebhookState(); got != tc.equal {
				t.Errorf("lead states equal = %v, want %v", got, tc.equal)
			}
			custA, custB := &Customer{AssignedToID: tc.a}, &Customer{AssignedToID: tc.b}
			if got := custA.WebhookState() == custB.WebhookState(); got != tc.equal {
				t.Errorf("customer states equal = %v, want %v", got, tc.equal)
			}
			ticketA, ticketB := &Ticket{AssignedToID: tc.a}, &Ticket{AssignedToID: tc.b}
			if got := ticketA.WebhookState() == ticketB.WebhookState(); got != tc.equal {
				t.Errorf("ticket states equal = %v, want %v", got, tc.equal)
			}
			for _, pair := range [][2]*Deal{
				{{CompanyID: tc.a}, {CompanyID: tc.b}},
				{{CustomerID: tc.a}, {CustomerID: tc.b}},
				{{LeadID: tc.a}, {LeadID: tc.b}},
			} {
				if got := pair[0].WebhookState() == pair[1].WebhookState(); got != tc.equal {
					t.Errorf("deal states equal = %v, want %v", got, tc.equal)
				}
			}
			for _, pair := range [][2]*Task{
				{{LeadID: tc.a}, {LeadID: tc.b}},
				{{CustomerID: tc.a}, {CustomerID: tc.b}},
				{{DealID: tc.a}, {DealID: tc.b}},
			} {
				if got := pair[0].WebhookState() == pair[1].WebhookState(); got != tc.equal {
					t.Errorf("task states equal = %v, want %v", got, tc.equal)
				}
			}
			companyA, companyB := &Company{OwnerID: tc.a}, &Company{OwnerID: tc.b}
			if got := companyA.WebhookState() == companyB.WebhookState(); got != tc.equal {
				t.Errorf("company states equal = %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestWebhookStateTicketDueAt(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	instant := time.Date(2026, 11, 20, 17, 0, 0, 123456789, time.UTC)
	inZone := instant.In(zone)
	later := instant.Add(time.Nanosecond)
	now := time.Now() // carries a monotonic reading

	cases := []struct {
		name  string
		a, b  *time.Time
		equal bool
	}{
		{"nil and nil are equal", nil, nil, true},
		{"nil and set differ", nil, &instant, false},
		{"same instant in UTC and another location is equal", &instant, &inZone, true},
		{"different instants differ", &instant, &later, false},
		{"monotonic reading is ignored", &now, timePtr(now.Round(0)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := &Ticket{DueAt: tc.a}, &Ticket{DueAt: tc.b}
			if got := a.WebhookState() == b.WebhookState(); got != tc.equal {
				t.Errorf("ticket states equal = %v, want %v", got, tc.equal)
			}
			// The deal's two nullable instants follow the same rule.
			for _, pair := range [][2]*Deal{
				{{ExpectedCloseDate: tc.a}, {ExpectedCloseDate: tc.b}},
				{{ClosedAt: tc.a}, {ClosedAt: tc.b}},
			} {
				if got := pair[0].WebhookState() == pair[1].WebhookState(); got != tc.equal {
					t.Errorf("deal states equal = %v, want %v", got, tc.equal)
				}
			}
			// So does the task's due date.
			taskA, taskB := &Task{DueDate: tc.a}, &Task{DueDate: tc.b}
			if got := taskA.WebhookState() == taskB.WebhookState(); got != tc.equal {
				t.Errorf("task states equal = %v, want %v", got, tc.equal)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time { return &t }
