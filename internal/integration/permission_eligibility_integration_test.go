package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
)

func TestPermissionEligibilitySQLite(t *testing.T) {
	runPermissionEligibility(t, false)
}

func TestPermissionEligibilityMySQL(t *testing.T) {
	runPermissionEligibility(t, true)
}

// runPermissionEligibility (#259 AC-20): who may be a ticket assignee, a
// customer assignee, a form's default owner and a lead owner follows
// permissions. The system roles answer exactly as before; a custom role
// qualifies only with the matching assignment.* key. The write-side lead link
// on a task and a deal follows the actor's leads.view scope, and automation
// treats an active custom-role user as staff and a customer login as
// unavailable.
func runPermissionEligibility(t *testing.T, useMySQL bool) {
	env := newPermissionEnv(t, useMySQL)
	admin := env.user("admin", models.RoleAdmin)
	customer := env.customer(nil)

	// candidate is one user to make an assignee or owner of.
	type candidate struct {
		name string
		user *models.User
		// What the user is eligible for.
		ticket, customer, formOwner bool
	}
	everyKey := env.role("Every assignment", "assignment.ticket_assignee", "assignment.customer_assignee", "assignment.form_owner")
	// A broad role without a single assignment.* key: plenty of permissions
	// on the records themselves do not make an eligible assignee or owner.
	noKey := env.role("No assignment", "tickets.view", "tickets.update", "tickets.create", "customers.view", "customers.update",
		"customers.assign", "leads.view", "leads.create", "leads.update", "forms.view", "forms.manage")
	candidates := []candidate{
		{"admin", admin, true, true, true},
		{"sales", env.user("sales", models.RoleSales), false, true, true},
		{"support", env.user("support", models.RoleSupport), true, false, false},
		{"customer", env.user("customer", models.RoleCustomer), false, false, false},
		{"custom with every assignment key", env.customUser("eligible", everyKey), true, true, true},
		{"custom without an assignment key", env.customUser("ineligible", noKey), false, false, false},
		{"custom with assignment.ticket_assignee only", env.customUser("ticket-only", env.role("Ticket", "assignment.ticket_assignee")), true, false, false},
		{"custom with assignment.customer_assignee only", env.customUser("customer-only", env.role("Customer", "assignment.customer_assignee")), false, true, false},
		{"custom with assignment.form_owner only", env.customUser("owner-only", env.role("Owner", "assignment.form_owner")), false, false, true},
	}

	for _, c := range candidates {
		t.Run("ticket assignee: "+c.name, func(t *testing.T) {
			ticket := &models.Ticket{Title: "Eligibility", Description: "Assignee check", Status: models.TicketStatusOpen,
				Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &c.user.ID}
			err := env.ticketService.Create(ticket)
			// Reassigning an existing ticket goes through the same rule.
			existing := env.ticket(admin, customer, models.TicketStatusOpen)
			existing.AssignedToID = &c.user.ID
			updateErr := env.ticketService.Update(existing)
			if c.ticket {
				require.NoError(t, err)
				require.NoError(t, updateErr)
				return
			}
			assert.ErrorIs(t, err, apperrors.ErrInvalidAssigneeRole)
			assert.ErrorIs(t, updateErr, apperrors.ErrInvalidAssigneeRole)
			var count int64
			require.NoError(t, env.db.Model(&models.Ticket{}).Where("assigned_to_id = ?", c.user.ID).Count(&count).Error)
			assert.Zero(t, count, "a refused assignee is written on no ticket")
		})

		t.Run("customer assignee: "+c.name, func(t *testing.T) {
			record := env.customer(nil)
			assigned, err := env.customerService.Assign(record.ID, c.user.ID)
			if c.customer {
				require.NoError(t, err)
				require.NotNil(t, assigned.AssignedToID)
				assert.Equal(t, c.user.ID, *assigned.AssignedToID)
				return
			}
			assert.ErrorIs(t, err, apperrors.ErrInvalidCustomerAssignee)
			var row models.Customer
			require.NoError(t, env.db.First(&row, record.ID).Error)
			assert.Nil(t, row.AssignedToID, "a refused assignee is not written")
		})

		t.Run("form default owner: "+c.name, func(t *testing.T) {
			form := &models.Form{Name: fmt.Sprintf("Form %d", env.next()), SubmitAction: "message", CreateLead: true,
				DefaultOwnerID: c.user.ID,
				Fields:         []models.FormFieldDef{{Name: "email", Label: "Email", Type: "email", Required: true}}}
			err := env.formService.Create(form, admin.ID)
			if c.formOwner {
				require.NoError(t, err)
				return
			}
			var fields service.FieldErrors
			require.True(t, errors.As(err, &fields), "want a field refusal, got %v", err)
			assert.Equal(t, "leads can only be owned by an admin or sales user", fields["default_owner_id"])
		})

		t.Run("lead owner: "+c.name, func(t *testing.T) {
			lead := &models.Lead{FirstName: "Owned", LastName: "Lead", Email: fmt.Sprintf("owned-%d@permissions.test", env.next()),
				Status: models.LeadStatusNew, OwnerID: c.user.ID}
			err := env.leadService.Create(lead)
			// Handing an existing lead over goes through the same rule.
			existing := env.lead(admin, models.LeadStatusNew, nil)
			_, assignErr := env.leadService.Assign(context.Background(), existing.ID, c.user.ID,
				service.LeadViewer{UserID: admin.ID, Scope: models.PermissionScopeAll})
			if c.formOwner {
				require.NoError(t, err)
				require.NoError(t, assignErr)
				return
			}
			assert.ErrorIs(t, err, apperrors.ErrInvalidLeadOwner)
			assert.ErrorIs(t, assignErr, apperrors.ErrInvalidLeadOwner)
			var count int64
			require.NoError(t, env.db.Model(&models.Lead{}).Where("owner_id = ?", c.user.ID).Count(&count).Error)
			assert.Zero(t, count, "a refused owner owns no lead")
		})
	}

	t.Run("the write-side lead link follows the actor's leads.view scope", func(t *testing.T) {
		role := env.role("Lead link")
		actor := env.customUser("linker", role)
		ownLead := env.lead(actor, models.LeadStatusNew, nil)
		foreignLead := env.lead(admin, models.LeadStatusNew, nil)

		taskWith := func(lead *models.Lead) error {
			task := &models.Task{Title: fmt.Sprintf("Linked task %d", env.next()), Status: models.TaskStatusPending,
				Priority: models.TaskPriorityMedium, AssignedToID: actor.ID, LeadID: &lead.ID}
			return env.taskService.CreateWithLabels(task, nil, actor.ID)
		}
		dealWith := func(lead *models.Lead) error {
			deal := &models.Deal{Title: fmt.Sprintf("Linked deal %d", env.next()), Stage: models.DealStageQualification,
				AmountCents: 1000, Currency: "EUR", OwnerID: actor.ID, LeadID: &lead.ID}
			return env.dealService.Create(deal, nil, actor.ID)
		}

		for _, c := range []struct {
			scope        string
			grants       []string
			own, foreign bool
		}{
			{"all: any lead", []string{"leads.view"}, true, true},
			{"own: only its own lead", []string{"leads.view:own"}, true, false},
			{"absent: no lead at all", []string{"tasks.create", "deals.create", "leads.create"}, false, false},
		} {
			env.setGrants(role, c.grants...)
			for _, link := range []struct {
				kind  string
				write func(*models.Lead) error
			}{{"task", taskWith}, {"deal", dealWith}} {
				for _, target := range []struct {
					name    string
					lead    *models.Lead
					allowed bool
				}{{"its own lead", ownLead, c.own}, {"another owner's lead", foreignLead, c.foreign}} {
					err := link.write(target.lead)
					if target.allowed {
						assert.NoError(t, err, "%s: a %s may link %s", c.scope, link.kind, target.name)
					} else {
						assert.ErrorIs(t, err, apperrors.ErrLeadNotFound, "%s: a %s linking %s is refused as lead not found", c.scope, link.kind, target.name)
					}
				}
			}
		}

		// The system roles, unchanged: admin any lead, sales its own, support none.
		sales := env.user("link-sales", models.RoleSales)
		support := env.user("link-support", models.RoleSupport)
		salesLead := env.lead(sales, models.LeadStatusNew, nil)
		link := func(actorID uint, lead *models.Lead) error {
			task := &models.Task{Title: fmt.Sprintf("System task %d", env.next()), Status: models.TaskStatusPending,
				Priority: models.TaskPriorityMedium, AssignedToID: actorID, LeadID: &lead.ID}
			return env.taskService.CreateWithLabels(task, nil, actorID)
		}
		assert.NoError(t, link(admin.ID, salesLead))
		assert.NoError(t, link(sales.ID, salesLead))
		assert.ErrorIs(t, link(sales.ID, foreignLead), apperrors.ErrLeadNotFound)
		assert.ErrorIs(t, link(support.ID, salesLead), apperrors.ErrLeadNotFound)
	})

	t.Run("automation: a custom-role user is staff, a customer login is unavailable", func(t *testing.T) {
		staff := env.customUser("recipient", env.role("Recipient"))
		login := env.user("portal", models.RoleCustomer)
		inactive := env.customUser("inactive", env.role("Inactive"))
		require.NoError(t, env.userRepo.UpdateFields(inactive.ID, map[string]interface{}{"is_active": false}))

		ruleFor := func(recipient *models.User) *models.AutomationRule {
			return &models.AutomationRule{Name: fmt.Sprintf("Notify %d", env.next()), EntityType: "lead", TriggerType: "lead_created",
				Steps: []models.AutomationStep{{StepType: models.AutomationStepNotify,
					Config: models.AutomationStepConfig{Title: "Follow up", Recipient: models.AutomationRecipientUser, UserID: recipient.ID}}}}
		}

		// Validation.
		accepted := ruleFor(staff)
		require.NoError(t, env.automationService.CreateRule(accepted, admin.ID), "an active custom-role user is a staff recipient")
		for name, recipient := range map[string]*models.User{"a customer login": login, "a deactivated custom-role user": inactive} {
			err := env.automationService.CreateRule(ruleFor(recipient), admin.ID)
			assert.ErrorIs(t, err, apperrors.ErrAssigneeNotFound, "%s is refused as a recipient", name)
		}

		// Execution: one pending run per recipient, on rules written past the
		// validation, so the engine itself has to decide.
		lead := env.lead(admin, models.LeadStatusNew, nil)
		runFor := func(recipient *models.User) *models.AutomationRun {
			rule := ruleFor(recipient)
			rule.Version = 1
			rule.CreatedByID = &admin.ID
			require.NoError(t, env.automationRepo.CreateRule(rule))
			return env.automationRun(rule, lead)
		}
		staffRun, loginRun := runFor(staff), runFor(login)
		require.NoError(t, env.automationEngine.ExecuteDue(context.Background()))

		notifications := func(user *models.User) int64 {
			var count int64
			require.NoError(t, env.db.Model(&models.Notification{}).Where("user_id = ?", user.ID).Count(&count).Error)
			return count
		}
		assert.EqualValues(t, 1, notifications(staff), "the custom-role user was notified")
		assert.Zero(t, notifications(login), "the customer login was not notified")
		for name, run := range map[string]*models.AutomationRun{"staff": staffRun, "login": loginRun} {
			var stored models.AutomationRun
			require.NoError(t, env.db.First(&stored, run.ID).Error)
			assert.NotEqual(t, models.AutomationRunPending, stored.Status, "the %s run was executed", name)
		}

		// A ticket rule's assign_owner step needs assignment.ticket_assignee.
		assignRule := func(target *models.User) *models.AutomationRule {
			return &models.AutomationRule{Name: fmt.Sprintf("Assign %d", env.next()), EntityType: "ticket", TriggerType: "ticket_created",
				Steps: []models.AutomationStep{{StepType: models.AutomationStepAssignOwner,
					Config: models.AutomationStepConfig{UserID: target.ID}}}}
		}
		withKey := env.customUser("ticket-assignee", env.role("Assignee", "assignment.ticket_assignee"))
		require.NoError(t, env.automationService.CreateRule(assignRule(withKey), admin.ID))
		assert.ErrorIs(t, env.automationService.CreateRule(assignRule(staff), admin.ID), apperrors.ErrAssigneeNotFound,
			"a custom-role user without assignment.ticket_assignee cannot be a ticket rule's assignee")
	})
}
