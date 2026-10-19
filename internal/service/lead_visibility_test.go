package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestCanViewLead_Matrix(t *testing.T) {
	owned := &models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 7}

	for _, tc := range []struct {
		name   string
		viewer LeadViewer
		lead   *models.Lead
		want   bool
	}{
		{"admin sees another user's lead", LeadViewer{UserID: 1, Role: models.RoleAdmin}, owned, true},
		{"admin sees a lead that is not loaded", LeadViewer{UserID: 1, Role: models.RoleAdmin}, nil, true},
		{"sales sees its own lead", LeadViewer{UserID: 7, Role: models.RoleSales}, owned, true},
		{"sales does not see another rep's lead", LeadViewer{UserID: 8, Role: models.RoleSales}, owned, false},
		{"sales does not see a lead that is not loaded", LeadViewer{UserID: 7, Role: models.RoleSales}, nil, false},
		{"sales without an id sees no ownerless lead", LeadViewer{Role: models.RoleSales}, &models.Lead{}, false},
		{"support sees no lead, even one it would own", LeadViewer{UserID: 7, Role: models.RoleSupport}, owned, false},
		{"customer sees no lead", LeadViewer{UserID: 7, Role: models.RoleCustomer}, owned, false},
		{"no role sees no lead", LeadViewer{UserID: 7}, owned, false},
	} {
		assert.Equal(t, tc.want, CanViewLead(tc.viewer, tc.lead), tc.name)
	}
}

func TestRedactTaskLead(t *testing.T) {
	leadID := func() *uint { id := uint(3); return &id }
	lead := func() *models.Lead {
		return &models.Lead{BaseModel: models.BaseModel{ID: 3}, Email: "lead@example.com", OwnerID: 7}
	}
	sales := LeadViewer{UserID: 7, Role: models.RoleSales}

	// Visible: untouched.
	task := &models.Task{LeadID: leadID(), Lead: lead()}
	RedactTaskLead(sales, task)
	assert.NotNil(t, task.LeadID)
	assert.NotNil(t, task.Lead)

	// Hidden from another rep and from support: both the id and the data go.
	for _, viewer := range []LeadViewer{{UserID: 8, Role: models.RoleSales}, {UserID: 7, Role: models.RoleSupport}} {
		task = &models.Task{LeadID: leadID(), Lead: lead()}
		RedactTaskLead(viewer, task)
		assert.Nil(t, task.LeadID, "%+v", viewer)
		assert.Nil(t, task.Lead, "%+v", viewer)
	}

	// A lead_id without its lead fails closed for sales, stays for admin.
	task = &models.Task{LeadID: leadID()}
	RedactTaskLead(sales, task)
	assert.Nil(t, task.LeadID)
	task = &models.Task{LeadID: leadID()}
	RedactTaskLead(LeadViewer{UserID: 1, Role: models.RoleAdmin}, task)
	assert.NotNil(t, task.LeadID)

	// A preloaded lead that is not the linked one cannot vouch for the link.
	other := uint(4)
	task = &models.Task{LeadID: &other, Lead: lead()}
	RedactTaskLead(sales, task)
	assert.Nil(t, task.LeadID)
	assert.Nil(t, task.Lead)

	// No link, nil task: nothing to do.
	task = &models.Task{Title: "plain"}
	RedactTaskLead(sales, task)
	assert.Equal(t, "plain", task.Title)
	RedactTaskLead(sales, nil)
}

func TestRedactLeads_AppliesToEveryRow(t *testing.T) {
	mine, theirs := uint(3), uint(4)
	viewer := LeadViewer{UserID: 7, Role: models.RoleSales}

	tasks := []models.Task{
		{LeadID: &mine, Lead: &models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 7}},
		{LeadID: &theirs, Lead: &models.Lead{BaseModel: models.BaseModel{ID: 4}, OwnerID: 8}},
		{},
	}
	RedactTaskLeads(viewer, tasks)
	assert.NotNil(t, tasks[0].Lead)
	assert.Nil(t, tasks[1].LeadID)
	assert.Nil(t, tasks[1].Lead)

	deals := []models.Deal{
		{LeadID: &mine, Lead: &models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 7}},
		{LeadID: &theirs, Lead: &models.Lead{BaseModel: models.BaseModel{ID: 4}, OwnerID: 8}},
	}
	RedactDealLeads(viewer, deals)
	assert.NotNil(t, deals[0].LeadID)
	assert.NotNil(t, deals[0].Lead)
	assert.Nil(t, deals[1].LeadID)
	assert.Nil(t, deals[1].Lead)

	RedactDealLead(viewer, nil)
}
