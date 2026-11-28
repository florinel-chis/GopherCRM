package service

import (
	"github.com/florinel-chis/gophercrm/internal/models"
)

// CanViewCustomer is the customer-visibility policy behind a task's customer
// link, when the link is read back. A customer record is not owner-scoped in
// this system: GET /customers/{id} admits any caller that holds customers.view,
// whatever the scope, so the task link follows the same rule. A viewer that
// holds the key sees the customer; one without it (viewer.Scope "") never does.
func CanViewCustomer(viewer LeadViewer) bool {
	return viewer.Scope == models.PermissionScopeAll || viewer.Scope == models.PermissionScopeOwn
}

// RedactTaskCustomer removes a customer the viewer may not see from a task
// about to be returned. Both the preloaded customer and customer_id go, so
// neither the customer's data nor the fact that the task points at it leaks.
// The decision is re-made on every read, like the lead and deal links. A
// customer_id without its customer (not preloaded, or a preloaded customer
// that is not the linked one) is hidden from a viewer without the key too.
func RedactTaskCustomer(viewer LeadViewer, task *models.Task) {
	if task == nil || (task.CustomerID == nil && task.Customer == nil) {
		return
	}
	if CanViewCustomer(viewer) {
		return
	}
	task.CustomerID, task.Customer = nil, nil
}

// RedactTaskCustomers applies RedactTaskCustomer to every task of a page.
func RedactTaskCustomers(viewer LeadViewer, tasks []models.Task) {
	for i := range tasks {
		RedactTaskCustomer(viewer, &tasks[i])
	}
}
