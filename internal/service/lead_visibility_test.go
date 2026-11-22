package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
)

func TestCanViewLead_Matrix(t *testing.T) {
	owned := &models.Lead{BaseModel: models.BaseModel{ID: 3}, OwnerID: 7}

	for _, tc := range []struct {
		name   string
		viewer LeadViewer
		lead   *models.Lead
		want   bool
	}{
		{"admin sees another user's lead", LeadViewer{UserID: 1, Scope: models.PermissionScopeAll}, owned, true},
		{"admin sees a lead that is not loaded", LeadViewer{UserID: 1, Scope: models.PermissionScopeAll}, nil, true},
		{"sales sees its own lead", LeadViewer{UserID: 7, Scope: models.PermissionScopeOwn}, owned, true},
		{"sales does not see another rep's lead", LeadViewer{UserID: 8, Scope: models.PermissionScopeOwn}, owned, false},
		{"sales does not see a lead that is not loaded", LeadViewer{UserID: 7, Scope: models.PermissionScopeOwn}, nil, false},
		{"sales without an id sees no ownerless lead", LeadViewer{Scope: models.PermissionScopeOwn}, &models.Lead{}, false},
		{"support sees no lead, even one it would own", LeadViewer{UserID: 7, Scope: ""}, owned, false},
		{"customer sees no lead", LeadViewer{UserID: 7, Scope: ""}, owned, false},
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
	sales := LeadViewer{UserID: 7, Scope: models.PermissionScopeOwn}

	// Visible: untouched.
	task := &models.Task{LeadID: leadID(), Lead: lead()}
	RedactTaskLead(sales, task)
	assert.NotNil(t, task.LeadID)
	assert.NotNil(t, task.Lead)

	// Hidden from another rep and from support: both the id and the data go.
	for _, viewer := range []LeadViewer{{UserID: 8, Scope: models.PermissionScopeOwn}, {UserID: 7, Scope: ""}} {
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
	RedactTaskLead(LeadViewer{UserID: 1, Scope: models.PermissionScopeAll}, task)
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
	viewer := LeadViewer{UserID: 7, Scope: models.PermissionScopeOwn}

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

// TestCanViewLead_CustomRoleOwnScope: a custom role decides lead visibility
// by the scope its stored grants hold leads.view at, read from the user
// repository, exactly as the system matrix does for admin and sales.
func TestCanViewLead_CustomRoleOwnScope(t *testing.T) {
	const actorID = uint(7)
	roleID := uint(4)
	customRole := func(grants ...models.RolePermission) *models.User {
		return &models.User{BaseModel: models.BaseModel{ID: actorID}, Role: models.RoleCustom, RoleID: &roleID, IsActive: true,
			CustomRole: &models.Role{ID: roleID, Name: "Custom", Permissions: grants}}
	}
	mine := &models.Lead{BaseModel: models.BaseModel{ID: 31}, OwnerID: actorID}
	theirs := &models.Lead{BaseModel: models.BaseModel{ID: 32}, OwnerID: 8}
	leads := func() *mocks.LeadRepository {
		repo := new(mocks.LeadRepository)
		repo.On("GetByID", mine.ID).Return(mine, nil)
		repo.On("GetByID", theirs.ID).Return(theirs, nil)
		return repo
	}
	users := func(user *models.User) *mocks.UserRepository {
		repo := new(mocks.UserRepository)
		repo.On("GetByID", actorID).Return(user, nil)
		return repo
	}

	t.Run("leads.view at own sees only its own leads", func(t *testing.T) {
		userRepo := users(customRole(models.RolePermission{RoleID: roleID, PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeOwn}))
		viewer, err := leadViewerOf(userRepo, actorID)
		require.NoError(t, err)
		assert.Equal(t, LeadViewer{UserID: actorID, Scope: models.PermissionScopeOwn}, viewer)
		assert.True(t, CanViewLead(viewer, mine))
		assert.False(t, CanViewLead(viewer, theirs))
		assert.False(t, CanViewLead(viewer, nil))

		got, err := checkLeadLink(leads(), userRepo, actorID, mine.ID)
		require.NoError(t, err)
		assert.Equal(t, mine, got)
		got, err = checkLeadLink(leads(), userRepo, actorID, theirs.ID)
		assert.ErrorIs(t, err, apperrors.ErrLeadNotFound)
		assert.Nil(t, got)
	})

	t.Run("leads.view at all sees another owner's lead", func(t *testing.T) {
		userRepo := users(customRole(models.RolePermission{RoleID: roleID, PermissionKey: permissions.LeadsView, Scope: models.PermissionScopeAll}))
		viewer, err := leadViewerOf(userRepo, actorID)
		require.NoError(t, err)
		assert.Equal(t, models.PermissionScopeAll, viewer.Scope)
		assert.True(t, CanViewLead(viewer, theirs))
		got, err := checkLeadLink(leads(), userRepo, actorID, theirs.ID)
		require.NoError(t, err)
		assert.Equal(t, theirs, got)
	})

	t.Run("without leads.view it sees neither", func(t *testing.T) {
		userRepo := users(customRole(models.RolePermission{RoleID: roleID, PermissionKey: permissions.TasksView, Scope: models.PermissionScopeAll}))
		viewer, err := leadViewerOf(userRepo, actorID)
		require.NoError(t, err)
		assert.Equal(t, models.PermissionScope(""), viewer.Scope)
		assert.False(t, CanViewLead(viewer, mine))
		assert.False(t, CanViewLead(viewer, theirs))
		for _, id := range []uint{mine.ID, theirs.ID} {
			_, err := checkLeadLink(leads(), userRepo, actorID, id)
			assert.ErrorIs(t, err, apperrors.ErrLeadNotFound)
		}
	})
}
