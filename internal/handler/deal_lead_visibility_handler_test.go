package handler

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// dealLinkedToLead is a deal owned by ownerID and linked to lead 30, owned by
// leadOwnerID, preloaded as the repository returns it.
func dealLinkedToLead(ownerID, leadOwnerID uint) *models.Deal {
	deal := ownDeal(5)
	deal.OwnerID = ownerID
	leadID := uint(30)
	deal.LeadID = &leadID
	deal.Lead = &models.Lead{BaseModel: models.BaseModel{ID: 30}, Email: hiddenLeadEmail, OwnerID: leadOwnerID}
	return deal
}

func newDealLeadRouter(svc *mocks.DealService, role models.UserRole, userID uint) *gin.Engine {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		c.Next()
	})
	SetupDealRoutes(router.Group(""), NewDealHandler(svc))
	return router
}

// A sales user's own deal, linked to another rep's lead (by an admin, or
// before the lead changed hands), is returned without the lead on every route
// that returns a deal.
func TestDealRoutes_HideALeadTheOwnerMayNotSee(t *testing.T) {
	for _, route := range []struct {
		name   string
		method string
		path   string
		body   interface{}
		prime  func(svc *mocks.DealService)
	}{
		{"get", http.MethodGet, "/deals/5", nil, func(svc *mocks.DealService) {
			svc.On("GetByID", uint(5)).Return(dealLinkedToLead(42, 9), nil).Once()
		}},
		{"list", http.MethodGet, "/deals", nil, func(svc *mocks.DealService) {
			svc.On("List", 0, 20, mock.Anything).Return([]models.Deal{*dealLinkedToLead(42, 9)}, int64(1), nil).Once()
		}},
		{"company deals", http.MethodGet, "/companies/3/deals", nil, func(svc *mocks.DealService) {
			svc.On("ListByCompany", uint(3), mock.Anything, 0, 20).Return([]models.Deal{*dealLinkedToLead(42, 9)}, int64(1), nil).Once()
		}},
		{"customer deals", http.MethodGet, "/customers/3/deals", nil, func(svc *mocks.DealService) {
			svc.On("ListByCustomer", uint(3), mock.Anything, 0, 20).Return([]models.Deal{*dealLinkedToLead(42, 9)}, int64(1), nil).Once()
		}},
		{"update", http.MethodPut, "/deals/5", validDealBody(), func(svc *mocks.DealService) {
			svc.On("GetByID", uint(5)).Return(dealLinkedToLead(42, 9), nil).Once()
			// The stored link is passed on untouched (absent keeps it).
			svc.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
				return d.LeadID != nil && *d.LeadID == 30 && d.Lead == nil
			}), mock.Anything, false, uint(42)).Return(nil).Once()
			svc.On("GetByID", uint(5)).Return(dealLinkedToLead(42, 9), nil).Once()
		}},
		{"stage", http.MethodPost, "/deals/5/stage", gin.H{"stage": "won"}, func(svc *mocks.DealService) {
			svc.On("GetByID", uint(5)).Return(dealLinkedToLead(42, 9), nil).Once()
			svc.On("ChangeStage", uint(5), models.DealStageWon, mock.Anything, mock.Anything, uint(42)).Return(dealLinkedToLead(42, 9), nil).Once()
		}},
		{"create", http.MethodPost, "/deals", validDealBody(), func(svc *mocks.DealService) {
			svc.On("Create", mock.Anything, mock.Anything, uint(42)).Return(nil).Once()
			svc.On("GetByID", uint(0)).Return(dealLinkedToLead(42, 9), nil).Once()
		}},
	} {
		svc := new(mocks.DealService)
		route.prime(svc)

		w := serveJSON(newDealLeadRouter(svc, models.RoleSales, 42), route.method, route.path, route.body)
		require.Lessf(t, w.Code, 300, "%s: %s", route.name, w.Body.String())
		assert.NotContains(t, w.Body.String(), hiddenLeadEmail, route.name)
		assert.NotContains(t, w.Body.String(), `"lead_id"`, route.name)
		assert.NotContains(t, w.Body.String(), `"lead":`, route.name)
		svc.AssertExpectations(t)
	}
}

// A lead_id in the body reaches the service flagged as sent, even when it is
// the stored (hidden) link; absent, it is not. The refusal is the same 400
// INVALID_REFERENCE for the stored id and any other hidden one.
func TestDealUpdate_SentLeadIDIsFlaggedEvenWhenItIsTheStoredLink(t *testing.T) {
	var codes []string
	for _, sent := range []uint{30, 31} {
		svc := new(mocks.DealService)
		svc.On("GetByID", uint(5)).Return(dealLinkedToLead(42, 9), nil).Once()
		svc.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
			return d.LeadID != nil && *d.LeadID == sent
		}), mock.Anything, true, uint(42)).Return(fmt.Errorf("unknown lead_id %d: %w", sent, apperrors.ErrLeadNotFound)).Once()

		body := validDealBody()
		body["lead_id"] = sent
		w := serveJSON(newDealLeadRouter(svc, models.RoleSales, 42), http.MethodPut, "/deals/5", body)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.NotContains(t, w.Body.String(), hiddenLeadEmail)
		codes = append(codes, decodeResponse(t, w).Error.Code)
		svc.AssertExpectations(t)
	}
	assert.Equal(t, apperrors.CodeInvalidReference, codes[0])
	assert.Equal(t, codes[0], codes[1], "the stored hidden id answers like any other hidden id")
}

func TestDealGet_ShowsTheLeadToItsOwnerAndToAdmins(t *testing.T) {
	for _, tc := range []struct {
		role   models.UserRole
		userID uint
		deal   *models.Deal
	}{
		{models.RoleSales, 42, dealLinkedToLead(42, 42)},
		{models.RoleAdmin, 1, dealLinkedToLead(42, 9)},
	} {
		svc := new(mocks.DealService)
		svc.On("GetByID", uint(5)).Return(tc.deal, nil).Once()

		w := serveJSON(newDealLeadRouter(svc, tc.role, tc.userID), http.MethodGet, "/deals/5", nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"lead_id":30`, tc.role)
		assert.Contains(t, w.Body.String(), hiddenLeadEmail, tc.role)
	}
}
