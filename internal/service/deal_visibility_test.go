package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestCanViewDeal(t *testing.T) {
	owned := &models.Deal{BaseModel: models.BaseModel{ID: 88}, OwnerID: 7}
	tests := []struct {
		name   string
		viewer LeadViewer
		deal   *models.Deal
		want   bool
	}{
		{"admin sees another user's deal", LeadViewer{UserID: 1, Role: models.RoleAdmin}, owned, true},
		{"admin sees a nil deal", LeadViewer{UserID: 1, Role: models.RoleAdmin}, nil, true},
		{"sales sees its own deal", LeadViewer{UserID: 7, Role: models.RoleSales}, owned, true},
		{"sales does not see another rep's deal", LeadViewer{UserID: 8, Role: models.RoleSales}, owned, false},
		{"sales does not see a nil deal", LeadViewer{UserID: 7, Role: models.RoleSales}, nil, false},
		{"sales without an id sees no ownerless deal", LeadViewer{Role: models.RoleSales}, &models.Deal{}, false},
		{"support sees no deal, even one it would own", LeadViewer{UserID: 7, Role: models.RoleSupport}, owned, false},
		{"customer sees no deal", LeadViewer{UserID: 7, Role: models.RoleCustomer}, owned, false},
		{"an empty role sees no deal", LeadViewer{UserID: 7}, owned, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanViewDeal(tt.viewer, tt.deal))
		})
	}
}

func TestRedactTaskDeal(t *testing.T) {
	dealID, otherID := uint(88), uint(89)
	deal := func() *models.Deal { return &models.Deal{BaseModel: models.BaseModel{ID: dealID}, OwnerID: 7} }
	admin := LeadViewer{UserID: 1, Role: models.RoleAdmin}
	owner := LeadViewer{UserID: 7, Role: models.RoleSales}

	t.Run("a viewer who may see the deal keeps both", func(t *testing.T) {
		for _, viewer := range []LeadViewer{admin, owner} {
			task := &models.Task{DealID: &dealID, Deal: deal()}
			RedactTaskDeal(viewer, task)
			assert.NotNil(t, task.DealID, viewer.Role)
			assert.NotNil(t, task.Deal, viewer.Role)
		}
	})

	t.Run("a viewer who may not loses both", func(t *testing.T) {
		for _, viewer := range []LeadViewer{
			{UserID: 8, Role: models.RoleSales},
			{UserID: 7, Role: models.RoleSupport},
			{UserID: 7, Role: models.RoleCustomer},
			{UserID: 7},
		} {
			task := &models.Task{DealID: &dealID, Deal: deal()}
			RedactTaskDeal(viewer, task)
			assert.Nil(t, task.DealID, "%+v", viewer)
			assert.Nil(t, task.Deal, "%+v", viewer)
		}
	})

	t.Run("a deal that is not loaded hides the link from all but admins", func(t *testing.T) {
		task := &models.Task{DealID: &dealID}
		RedactTaskDeal(owner, task)
		assert.Nil(t, task.DealID)

		task = &models.Task{DealID: &dealID}
		RedactTaskDeal(admin, task)
		assert.NotNil(t, task.DealID, "an admin keeps the id of a deal that was deleted")
	})

	t.Run("a stale copy cannot vouch for another link", func(t *testing.T) {
		task := &models.Task{DealID: &otherID, Deal: deal()}
		RedactTaskDeal(owner, task)
		assert.Nil(t, task.DealID)
		assert.Nil(t, task.Deal)
	})

	t.Run("a loaded deal without a link goes too", func(t *testing.T) {
		task := &models.Task{Deal: deal()}
		RedactTaskDeal(owner, task)
		assert.Nil(t, task.Deal)
	})

	t.Run("nil and unlinked tasks are left alone", func(t *testing.T) {
		RedactTaskDeal(owner, nil)
		task := &models.Task{Title: "Unlinked"}
		RedactTaskDeal(owner, task)
		assert.Equal(t, "Unlinked", task.Title)
	})

	t.Run("a page is redacted row by row", func(t *testing.T) {
		tasks := []models.Task{
			{DealID: &dealID, Deal: deal()},
			{DealID: &otherID, Deal: &models.Deal{BaseModel: models.BaseModel{ID: otherID}, OwnerID: 9}},
		}
		RedactTaskDeals(owner, tasks)
		assert.NotNil(t, tasks[0].DealID)
		assert.NotNil(t, tasks[0].Deal)
		assert.Nil(t, tasks[1].DealID)
		assert.Nil(t, tasks[1].Deal)
	})
}
