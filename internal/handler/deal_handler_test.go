package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Compile-time proof that the shared double still satisfies the interface it
// stands in for; internal/mocks cannot import internal/service itself.
var _ service.DealService = (*mocks.DealService)(nil)

// The `oneof` binding tags on the two request bodies must list exactly
// models.DealStages: a stage added to the model without the tag would be
// refused at the door, and a stage in the tag without the model would reach
// the service. Struct tags cannot reference a constant, so this is what holds
// the two together.
func TestDealStageBindingTagsMatchTheModel(t *testing.T) {
	want := make([]string, 0, len(models.DealStages))
	for _, stage := range models.DealStages {
		want = append(want, string(stage))
	}
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"DealRequest", reflect.TypeOf(DealRequest{})},
		{"DealStageRequest", reflect.TypeOf(DealStageRequest{})},
	} {
		field, ok := tc.typ.FieldByName("Stage")
		if !ok {
			t.Fatalf("%s has no Stage field", tc.name)
		}
		tag := field.Tag.Get("binding")
		i := strings.Index(tag, "oneof=")
		if i < 0 {
			t.Fatalf("%s.Stage has no oneof binding: %q", tc.name, tag)
		}
		got := strings.Fields(tag[i+len("oneof="):])
		assert.Equalf(t, want, got, "%s.Stage oneof tag", tc.name)
	}
}

// The max binding on amount_cents must be models.DealAmountCentsMax, the bound
// the service enforces as well; struct tags cannot reference the constant.
func TestDealAmountBindingTagMatchesTheModel(t *testing.T) {
	field, ok := reflect.TypeOf(DealRequest{}).FieldByName("AmountCents")
	if !ok {
		t.Fatal("DealRequest has no AmountCents field")
	}
	assert.Equal(t, fmt.Sprintf("min=0,max=%d", models.DealAmountCentsMax), field.Tag.Get("binding"))
}

type DealHandlerTestSuite struct {
	suite.Suite
	mockService *mocks.DealService
	handler     *DealHandler
	role        models.UserRole
	userID      uint
	router      *gin.Engine
}

func (suite *DealHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)
}

func (suite *DealHandlerTestSuite) SetupTest() {
	suite.mockService = new(mocks.DealService)
	suite.handler = NewDealHandler(suite.mockService)
	suite.role = models.RoleAdmin
	suite.userID = 1

	suite.router = gin.New()
	suite.router.Use(func(c *gin.Context) {
		c.Set("user_id", suite.userID)
		c.Set("user_role", string(suite.role))
		c.Set("request_id", "test-request-id")
		c.Next()
	})
	// The same minimal bind-error handler the other handler suites install, so
	// a validation failure comes back as 400 rather than an empty 200.
	suite.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	// Mounted through the real route setup so the role guards are part of what
	// is tested, not only the handler bodies.
	SetupDealRoutes(suite.router.Group(""), suite.handler)
}

func (suite *DealHandlerTestSuite) TearDownTest() {
	suite.mockService.AssertExpectations(suite.T())
}

func (suite *DealHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func validDealBody() gin.H {
	return gin.H{"title": "Acme rollout", "amount_cents": 125000, "currency": "EUR"}
}

// ownDeal is a deal owned by user 1, the suite's default caller.
func ownDeal(id uint) *models.Deal {
	return &models.Deal{BaseModel: models.BaseModel{ID: id}, Title: "Acme rollout", Stage: models.DealStageProposal, Probability: 40, Currency: "EUR", OwnerID: 1}
}

// --- Role matrix ------------------------------------------------------------

// Every route against every role. The mock is primed only where the guard is
// expected to let the request through, so a guard that opened up would fail
// on the unexpected call as well as on the status.
func (suite *DealHandlerTestSuite) TestRoleMatrix() {
	adminAndSales := map[models.UserRole]bool{models.RoleAdmin: true, models.RoleSales: true}
	routes := []struct {
		name    string
		method  string
		path    string
		body    interface{}
		allowed map[models.UserRole]bool
		prime   func()
	}{
		{"list", http.MethodGet, "/deals", nil, adminAndSales, func() {
			suite.mockService.On("List", 0, 20, mock.Anything).Return([]models.Deal{}, int64(0), nil).Once()
		}},
		{"pipeline", http.MethodGet, "/deals/pipeline", nil, adminAndSales, func() {
			suite.mockService.On("Pipeline", mock.Anything, uint(1), suite.role).Return(&models.DealPipeline{Stages: []models.DealPipelineStage{}}, nil).Once()
		}},
		{"get", http.MethodGet, "/deals/5", nil, adminAndSales, func() {
			suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Once()
		}},
		{"create", http.MethodPost, "/deals", validDealBody(), adminAndSales, func() {
			suite.mockService.On("Create", mock.Anything, mock.Anything, uint(1)).Return(nil).Once()
			suite.mockService.On("GetByID", uint(0)).Return(ownDeal(0), nil).Once()
		}},
		{"update", http.MethodPut, "/deals/5", validDealBody(), adminAndSales, func() {
			suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Twice()
			suite.mockService.On("Update", mock.Anything, mock.Anything, uint(1)).Return(nil).Once()
		}},
		{"stage", http.MethodPost, "/deals/5/stage", gin.H{"stage": "won"}, adminAndSales, func() {
			suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Once()
			suite.mockService.On("ChangeStage", uint(5), models.DealStageWon, mock.Anything, mock.Anything, uint(1)).Return(ownDeal(5), nil).Once()
		}},
		{"history", http.MethodGet, "/deals/5/history", nil, adminAndSales, func() {
			suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Once()
			suite.mockService.On("History", uint(5)).Return([]models.DealStageChange{}, nil).Once()
		}},
		{"delete", http.MethodDelete, "/deals/5", nil, map[models.UserRole]bool{models.RoleAdmin: true}, func() {
			suite.mockService.On("Delete", uint(5)).Return(nil).Once()
		}},
		{"company deals", http.MethodGet, "/companies/5/deals", nil, adminAndSales, func() {
			suite.mockService.On("ListByCompany", uint(5), mock.Anything, 0, 20).Return([]models.Deal{}, int64(0), nil).Once()
		}},
		{"customer deals", http.MethodGet, "/customers/5/deals", nil, adminAndSales, func() {
			suite.mockService.On("ListByCustomer", uint(5), mock.Anything, 0, 20).Return([]models.Deal{}, int64(0), nil).Once()
		}},
	}

	for _, route := range routes {
		for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales, models.RoleSupport, models.RoleCustomer} {
			suite.SetupTest()
			suite.role = role
			if route.allowed[role] {
				route.prime()
			}

			w := suite.do(route.method, route.path, route.body)
			if route.allowed[role] {
				assert.NotEqualf(suite.T(), http.StatusForbidden, w.Code, "%s as %s must not be forbidden", route.name, role)
				assert.Lessf(suite.T(), w.Code, 400, "%s as %s: %s", route.name, role, w.Body.String())
			} else {
				assert.Equalf(suite.T(), http.StatusForbidden, w.Code, "%s as %s must be forbidden", route.name, role)
			}
			suite.mockService.AssertExpectations(suite.T())
		}
	}
}

// --- Pipeline ---------------------------------------------------------------

// The filters reach the service as parsed, next to the caller's id and role;
// the owner rule itself is the service's (see TestDealService_PipelineScoping).
func (suite *DealHandlerTestSuite) TestPipeline_PassesTheFiltersAndTheCaller() {
	for _, role := range []models.UserRole{models.RoleAdmin, models.RoleSales} {
		suite.SetupTest()
		suite.role = role
		suite.userID = 7
		suite.mockService.On("Pipeline", mock.MatchedBy(func(f repository.DealPipelineFilter) bool {
			return f.OwnerID != nil && *f.OwnerID == 4 && f.CompanyID != nil && *f.CompanyID == 3
		}), uint(7), role).Return(&models.DealPipeline{Stages: []models.DealPipelineStage{}}, nil).Once()

		w := suite.do(http.MethodGet, "/deals/pipeline?owner_id=4&company_id=3", nil)
		assert.Equalf(suite.T(), http.StatusOK, w.Code, "%s: %s", role, w.Body.String())
		suite.mockService.AssertExpectations(suite.T())
	}

	// No filter at all is two nil pointers.
	suite.SetupTest()
	suite.mockService.On("Pipeline", repository.DealPipelineFilter{}, uint(1), models.RoleAdmin).
		Return(&models.DealPipeline{Stages: []models.DealPipelineStage{}}, nil).Once()
	w := suite.do(http.MethodGet, "/deals/pipeline", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// A malformed or zero id is a 400 before the service is asked, the list's rule.
func (suite *DealHandlerTestSuite) TestPipeline_BadFiltersAre400() {
	for _, query := range []string{"owner_id=abc", "owner_id=0", "owner_id=-1", "company_id=x", "company_id=0", "company_id=1.5"} {
		w := suite.do(http.MethodGet, "/deals/pipeline?"+query, nil)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s: %s", query, w.Body.String())
	}
	suite.mockService.AssertNotCalled(suite.T(), "Pipeline", mock.Anything, mock.Anything, mock.Anything)
}

// The payload is passed through as the service built it: five stages, the
// totals of each, an empty stage as [] rather than null.
func (suite *DealHandlerTestSuite) TestPipeline_ResponseShape() {
	stages := make([]models.DealPipelineStage, 0, len(models.DealStages))
	for _, stage := range models.DealStages {
		stages = append(stages, models.DealPipelineStage{Stage: stage, Totals: []models.DealPipelineTotal{}})
	}
	stages[2] = models.DealPipelineStage{Stage: models.DealStageNegotiation, Count: 2, Totals: []models.DealPipelineTotal{
		{Currency: "CHF", AmountCents: 1, WeightedCents: 1},
		{Currency: "EUR", AmountCents: 100000, WeightedCents: 70000},
	}}
	suite.mockService.On("Pipeline", mock.Anything, uint(1), models.RoleAdmin).Return(&models.DealPipeline{Stages: stages}, nil)

	w := suite.do(http.MethodGet, "/deals/pipeline", nil)
	suite.Require().Equal(http.StatusOK, w.Code, w.Body.String())
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Stages []struct {
				Stage  string                     `json:"stage"`
				Count  int64                      `json:"count"`
				Totals []models.DealPipelineTotal `json:"totals"`
			} `json:"stages"`
		} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &envelope))
	assert.True(suite.T(), envelope.Success)
	names := []string{}
	for _, stage := range envelope.Data.Stages {
		names = append(names, stage.Stage)
	}
	assert.Equal(suite.T(), []string{"qualification", "proposal", "negotiation", "won", "lost"}, names)
	assert.Equal(suite.T(), int64(2), envelope.Data.Stages[2].Count)
	assert.Equal(suite.T(), []models.DealPipelineTotal{
		{Currency: "CHF", AmountCents: 1, WeightedCents: 1},
		{Currency: "EUR", AmountCents: 100000, WeightedCents: 70000},
	}, envelope.Data.Stages[2].Totals)
	assert.Contains(suite.T(), w.Body.String(), `{"stage":"lost","count":0,"totals":[]}`)
}

// The service's refusal (a role without deal access that got past a guard) is
// a 403, and an unclassified failure a 500.
func (suite *DealHandlerTestSuite) TestPipeline_ServiceErrors() {
	suite.mockService.On("Pipeline", mock.Anything, uint(1), models.RoleAdmin).
		Return(nil, fmt.Errorf("wrapped: %w", apperrors.ErrForbidden)).Once()
	w := suite.do(http.MethodGet, "/deals/pipeline", nil)
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	suite.mockService.On("Pipeline", mock.Anything, uint(1), models.RoleAdmin).
		Return(nil, errors.New("boom")).Once()
	w = suite.do(http.MethodGet, "/deals/pipeline", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
}

// --- Create -----------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestCreate_PassesTheFieldsAndTheCallerAsOwner() {
	probability := 55
	suite.mockService.On("Create", mock.MatchedBy(func(d *models.Deal) bool {
		return d.Title == "Acme rollout" && d.Stage == models.DealStageNegotiation && d.AmountCents == 125000 &&
			d.Currency == "EUR" && d.OwnerID == 1 && d.CompanyID != nil && *d.CompanyID == 3 &&
			d.CustomerID == nil && d.LeadID == nil && d.Source == "referral" &&
			d.ExpectedCloseDate != nil && d.ExpectedCloseDate.Format(models.DealDateLayout) == "2026-12-31"
	}), mock.MatchedBy(func(p *int) bool { return p != nil && *p == 55 }), uint(1)).Return(nil).Run(func(args mock.Arguments) {
		args.Get(0).(*models.Deal).ID = 9
	})
	created := ownDeal(9)
	date, _ := models.ParseDealDate("2026-12-31")
	created.ExpectedCloseDate = &date
	suite.mockService.On("GetByID", uint(9)).Return(created, nil)

	body := validDealBody()
	body["stage"] = "negotiation"
	body["probability"] = probability
	body["company_id"] = 3
	body["customer_id"] = 0 // 0 means no link
	body["source"] = "referral"
	body["expected_close_date"] = "2026-12-31"
	w := suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code, w.Body.String())
	response := decodeResponse(suite.T(), w)
	assert.True(suite.T(), response.Success)
	assert.Contains(suite.T(), w.Body.String(), `"expected_close_date":"2026-12-31"`, "the date is a date on the wire")
}

func (suite *DealHandlerTestSuite) TestCreate_ProbabilityAbsentIsPassedAsNil() {
	suite.mockService.On("Create", mock.Anything, mock.MatchedBy(func(p *int) bool { return p == nil }), uint(1)).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(ownDeal(0), nil)

	w := suite.do(http.MethodPost, "/deals", validDealBody())
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

func (suite *DealHandlerTestSuite) TestCreate_OwnerRules() {
	// Sales gets itself when nothing is sent...
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("Create", mock.MatchedBy(func(d *models.Deal) bool { return d.OwnerID == 42 }), mock.Anything, uint(42)).Return(nil).Twice()
	suite.mockService.On("GetByID", uint(0)).Return(ownDeal(0), nil).Times(3)
	w := suite.do(http.MethodPost, "/deals", validDealBody())
	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	// ...may name itself explicitly...
	body := validDealBody()
	body["owner_id"] = 42
	w = suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	// ...and may not hand the deal to somebody else.
	body["owner_id"] = 7
	w = suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	// Admin may.
	suite.role = models.RoleAdmin
	suite.userID = 1
	suite.mockService.On("Create", mock.MatchedBy(func(d *models.Deal) bool { return d.OwnerID == 7 }), mock.Anything, uint(1)).Return(nil).Once()
	w = suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	// owner_id 0 is not an owner.
	body["owner_id"] = 0
	w = suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// Malformed bodies never reach the service: no expectation is primed.
func (suite *DealHandlerTestSuite) TestCreate_BindingRejectsBadBodies() {
	for name, body := range map[string]gin.H{
		"missing title":       {"amount_cents": 1},
		"negative amount":     {"title": "X", "amount_cents": -1},
		"lowercase currency":  {"title": "X", "currency": "eur"},
		"two-letter currency": {"title": "X", "currency": "EU"},
		"digits in currency":  {"title": "X", "currency": "E1R"},
		"probability 101":     {"title": "X", "probability": 101},
		"probability -1":      {"title": "X", "probability": -1},
		"unknown stage":       {"title": "X", "stage": "closed"},
		"stage case":          {"title": "X", "stage": "Won"},
		"bad date":            {"title": "X", "expected_close_date": "31/12/2026"},
		"datetime not date":   {"title": "X", "expected_close_date": "2026-12-31T00:00:00Z"},
	} {
		w := suite.do(http.MethodPost, "/deals", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
}

// One value per column, each one character over its width, and notes one byte
// over. The limits come from the model constants that a test in package
// models holds to the declared column widths.
func (suite *DealHandlerTestSuite) TestCreate_ValuesLongerThanTheirColumnAre400() {
	over := func(n int) string { return strings.Repeat("x", n+1) }
	for field, value := range map[string]string{
		"title":       over(models.DealTitleMaxLength),
		"lost_reason": over(models.DealLostReasonMaxLength),
		"source":      over(models.DealSourceMaxLength),
		"notes":       over(models.DealNotesMaxBytes),
	} {
		body := validDealBody()
		body[field] = value
		w := suite.do(http.MethodPost, "/deals", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s over its limit must be 400", field)
		assert.Containsf(suite.T(), strings.ToLower(w.Body.String()), field, "the message names the field")
	}

	// Multibyte text is measured in characters, not bytes, for the varchar
	// columns: 200 two-byte characters fit varchar(200).
	suite.mockService.On("Create", mock.Anything, mock.Anything, uint(1)).Return(nil)
	suite.mockService.On("GetByID", uint(0)).Return(ownDeal(0), nil)
	body := validDealBody()
	body["title"] = strings.Repeat("é", models.DealTitleMaxLength)
	w := suite.do(http.MethodPost, "/deals", body)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)
}

func (suite *DealHandlerTestSuite) TestCreate_UnknownReferencesAreInvalidReference() {
	for name, sentinel := range map[string]error{
		"owner":    apperrors.ErrAssigneeNotFound,
		"company":  apperrors.ErrCompanyNotFound,
		"customer": apperrors.ErrCustomerNotFound,
		"lead":     apperrors.ErrLeadNotFound,
	} {
		suite.SetupTest()
		suite.mockService.On("Create", mock.Anything, mock.Anything, uint(1)).
			Return(fmt.Errorf("unknown %s_id 9: %w", name, sentinel)).Once()

		w := suite.do(http.MethodPost, "/deals", validDealBody())
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, name)
		assert.Equalf(suite.T(), apperrors.CodeInvalidReference, decodeResponse(suite.T(), w).Error.Code, name)
		suite.mockService.AssertExpectations(suite.T())
	}
}

func (suite *DealHandlerTestSuite) TestCreate_ValidationSentinelIs400AndOtherFailuresAre500() {
	suite.mockService.On("Create", mock.Anything, mock.Anything, uint(1)).
		Return(fmt.Errorf("currency must be a three-letter upper-case ISO 4217 code: %w", apperrors.ErrValidation)).Once()
	w := suite.do(http.MethodPost, "/deals", validDealBody())
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "ISO 4217")

	suite.mockService.On("Create", mock.Anything, mock.Anything, uint(1)).Return(errors.New("db down")).Once()
	w = suite.do(http.MethodPost, "/deals", validDealBody())
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down")
}

// --- List -------------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestList_PassesEveryFilterAndReturnsTheArrayWithMeta() {
	suite.mockService.On("List", 20, 10, mock.MatchedBy(func(f repository.DealListFilter) bool {
		return f.Search == "acme" && f.Stage == models.DealStageProposal && f.Open &&
			f.CompanyID != nil && *f.CompanyID == 3 && f.CustomerID != nil && *f.CustomerID == 4 &&
			f.OwnerID != nil && *f.OwnerID == 7 && f.SortBy == "amount_cents" && f.SortOrder == "asc"
	})).Return([]models.Deal{*ownDeal(1)}, int64(31), nil)

	w := suite.do(http.MethodGet, "/deals?page=3&limit=10&search=acme&stage=proposal&open=true&company_id=3&customer_id=4&owner_id=7&sort_by=amount_cents&sort_order=asc", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())

	var response struct {
		Success bool          `json:"success"`
		Data    []models.Deal `json:"data"`
		Meta    utils.APIMeta `json:"meta"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(suite.T(), response.Success)
	suite.Require().Len(response.Data, 1)
	assert.Equal(suite.T(), 3, response.Meta.Page)
	assert.Equal(suite.T(), 10, response.Meta.PerPage)
	assert.Equal(suite.T(), int64(31), response.Meta.Total)
	assert.Equal(suite.T(), int64(4), response.Meta.TotalPages)
}

func (suite *DealHandlerTestSuite) TestList_SalesIsAlwaysNarrowedToItself() {
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("List", 0, 20, mock.MatchedBy(func(f repository.DealListFilter) bool {
		return f.OwnerID != nil && *f.OwnerID == 42
	})).Return([]models.Deal{}, int64(0), nil).Twice()

	w := suite.do(http.MethodGet, "/deals", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	// Asking for somebody else's deals does not widen the scope.
	w = suite.do(http.MethodGet, "/deals?owner_id=7", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Admin without owner_id sees everything.
	suite.role = models.RoleAdmin
	suite.mockService.On("List", 0, 20, mock.MatchedBy(func(f repository.DealListFilter) bool {
		return f.OwnerID == nil
	})).Return([]models.Deal{}, int64(0), nil).Once()
	w = suite.do(http.MethodGet, "/deals", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *DealHandlerTestSuite) TestList_BadFiltersAre400() {
	for name, query := range map[string]string{
		"unknown sort":      "sort_by=owner_id",
		"injected sort":     "sort_by=title+DESC%2C+(SELECT+1)",
		"unknown stage":     "stage=closed",
		"open not a bool":   "open=maybe",
		"company_id text":   "company_id=abc",
		"customer_id zero":  "customer_id=0",
		"owner_id negative": "owner_id=-1",
	} {
		w := suite.do(http.MethodGet, "/deals?"+query, nil)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
}

func (suite *DealHandlerTestSuite) TestList_ServiceFailureIsAServerError() {
	suite.mockService.On("List", 0, 20, mock.Anything).Return(nil, int64(0), errors.New("db down"))

	w := suite.do(http.MethodGet, "/deals", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "db down")
}

// --- Get and visibility -----------------------------------------------------

func (suite *DealHandlerTestSuite) TestGet_NotFoundAndBadID() {
	suite.mockService.On("GetByID", uint(404)).Return(nil, fmt.Errorf("deal 404 not found: %w", apperrors.ErrNotFound))

	w := suite.do(http.MethodGet, "/deals/404", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	w = suite.do(http.MethodGet, "/deals/abc", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// Sales on somebody else's deal is a 403, the answer GET /leads/:id gives, on
// every per-deal route; the admin sees it.
func (suite *DealHandlerTestSuite) TestSalesMayOnlyTouchItsOwnDeals() {
	other := ownDeal(5)
	other.OwnerID = 7
	suite.role = models.RoleSales
	suite.userID = 42

	for _, route := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodGet, "/deals/5", nil},
		{http.MethodPut, "/deals/5", validDealBody()},
		{http.MethodPost, "/deals/5/stage", gin.H{"stage": "won"}},
		{http.MethodGet, "/deals/5/history", nil},
	} {
		suite.mockService.On("GetByID", uint(5)).Return(other, nil).Once()
		w := suite.do(route.method, route.path, route.body)
		assert.Equalf(suite.T(), http.StatusForbidden, w.Code, "%s %s", route.method, route.path)
		assert.Contains(suite.T(), w.Body.String(), "your own deals")
	}
	suite.mockService.AssertNotCalled(suite.T(), "Update", mock.Anything, mock.Anything, mock.Anything)
	suite.mockService.AssertNotCalled(suite.T(), "ChangeStage", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	suite.mockService.AssertNotCalled(suite.T(), "History", mock.Anything)

	suite.role = models.RoleAdmin
	suite.mockService.On("GetByID", uint(5)).Return(other, nil).Once()
	w := suite.do(http.MethodGet, "/deals/5", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

// --- Update -----------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestUpdate_ReplacesTheFieldsAndKeepsTheOwner() {
	existing := ownDeal(5)
	existing.OwnerID = 7
	existing.Notes = "keep?"
	three := uint(3)
	existing.CompanyID = &three
	existing.Owner = &models.User{BaseModel: models.BaseModel{ID: 7}}
	suite.mockService.On("GetByID", uint(5)).Return(existing, nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
		// PUT is the new state of the fields: notes not sent means cleared.
		// The owner and the links were not sent, and those survive; the
		// preloaded owner copy is dropped.
		return d.ID == 5 && d.Title == "New" && d.Notes == "" && d.CompanyID != nil && *d.CompanyID == 3 && d.OwnerID == 7 &&
			d.Owner == nil && d.Stage == models.DealStageWon && d.Currency == "" && d.AmountCents == 0
	}), mock.MatchedBy(func(p *int) bool { return p == nil }), uint(1)).Return(nil)

	w := suite.do(http.MethodPut, "/deals/5", gin.H{"title": "New", "stage": "won"})
	assert.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())
}

// The links follow the scalar rule of company_id on leads: absent keeps, 0
// clears, a value sets.
func (suite *DealHandlerTestSuite) TestUpdate_LinksAbsentKeepZeroClearsValueSets() {
	fresh := func() *models.Deal {
		d := ownDeal(5)
		three, four := uint(3), uint(4)
		d.CompanyID, d.CustomerID = &three, &four
		return d
	}

	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
		return d.CompanyID != nil && *d.CompanyID == 3 && d.CustomerID != nil && *d.CustomerID == 4 && d.LeadID == nil
	}), mock.Anything, uint(1)).Return(nil).Once()
	w := suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme"})
	assert.Equal(suite.T(), http.StatusOK, w.Code, "absent keeps")

	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
		return d.CompanyID == nil && d.CustomerID != nil && *d.CustomerID == 4
	}), mock.Anything, uint(1)).Return(nil).Once()
	w = suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme", "company_id": 0})
	assert.Equal(suite.T(), http.StatusOK, w.Code, "0 clears")

	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool {
		return d.CompanyID != nil && *d.CompanyID == 9 && d.LeadID != nil && *d.LeadID == 11
	}), mock.Anything, uint(1)).Return(nil).Once()
	w = suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme", "company_id": 9, "lead_id": 11})
	assert.Equal(suite.T(), http.StatusOK, w.Code, "a value sets")
}

func (suite *DealHandlerTestSuite) TestUpdate_OwnerRules() {
	fresh := func() *models.Deal {
		d := ownDeal(5)
		d.OwnerID = 42
		return d
	}

	// Sales may not reassign to somebody else...
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Once()
	w := suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme", "owner_id": 8})
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)

	// ...but may keep naming itself.
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool { return d.OwnerID == 42 }), mock.Anything, uint(42)).Return(nil).Once()
	w = suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme", "owner_id": 42})
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	// Admin reassigns.
	suite.role = models.RoleAdmin
	suite.userID = 1
	suite.mockService.On("GetByID", uint(5)).Return(fresh(), nil).Twice()
	suite.mockService.On("Update", mock.MatchedBy(func(d *models.Deal) bool { return d.OwnerID == 8 }), mock.Anything, uint(1)).Return(nil).Once()
	w = suite.do(http.MethodPut, "/deals/5", gin.H{"title": "Acme", "owner_id": 8})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
}

func (suite *DealHandlerTestSuite) TestUpdate_NotFoundReferencesAndTooLong() {
	suite.mockService.On("GetByID", uint(404)).Return(nil, fmt.Errorf("deal 404 not found: %w", apperrors.ErrNotFound)).Once()
	w := suite.do(http.MethodPut, "/deals/404", validDealBody())
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Once()
	suite.mockService.On("Update", mock.Anything, mock.Anything, uint(1)).
		Return(fmt.Errorf("unknown lead_id 9: %w", apperrors.ErrLeadNotFound)).Once()
	body := validDealBody()
	body["lead_id"] = 9
	w = suite.do(http.MethodPut, "/deals/5", body)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Equal(suite.T(), apperrors.CodeInvalidReference, decodeResponse(suite.T(), w).Error.Code)

	// Bounds are checked before the lookup, so nothing is primed for id 6.
	body = validDealBody()
	body["source"] = strings.Repeat("s", models.DealSourceMaxLength+1)
	w = suite.do(http.MethodPut, "/deals/6", body)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// --- Stage ------------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestChangeStage_PassesTheBodyThroughAndReturnsTheMovedDeal() {
	moved := ownDeal(5)
	moved.Stage = models.DealStageLost
	moved.Probability = 0
	moved.LostReason = "no budget"
	suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil).Times(3)

	// Stage only: nil probability, nil reason.
	suite.mockService.On("ChangeStage", uint(5), models.DealStageNegotiation,
		mock.MatchedBy(func(p *int) bool { return p == nil }),
		mock.MatchedBy(func(r *string) bool { return r == nil }), uint(1)).Return(ownDeal(5), nil).Once()
	w := suite.do(http.MethodPost, "/deals/5/stage", gin.H{"stage": "negotiation"})
	assert.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())

	// Explicit probability and reason are passed as pointers.
	suite.mockService.On("ChangeStage", uint(5), models.DealStageLost,
		mock.MatchedBy(func(p *int) bool { return p != nil && *p == 0 }),
		mock.MatchedBy(func(r *string) bool { return r != nil && *r == "no budget" }), uint(1)).Return(moved, nil).Once()
	w = suite.do(http.MethodPost, "/deals/5/stage", gin.H{"stage": "lost", "probability": 0, "lost_reason": "no budget"})
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"lost_reason":"no budget"`)
	assert.Contains(suite.T(), w.Body.String(), `"probability":0`)

	// A validation sentinel from the service is a 400.
	suite.mockService.On("ChangeStage", uint(5), models.DealStageWon, mock.Anything, mock.Anything, uint(1)).
		Return(nil, fmt.Errorf("probability must be between 0 and 100: %w", apperrors.ErrValidation)).Once()
	w = suite.do(http.MethodPost, "/deals/5/stage", gin.H{"stage": "won"})
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// Bad stage bodies never reach the service: nothing is primed.
func (suite *DealHandlerTestSuite) TestChangeStage_BindingRejectsBadBodies() {
	for name, body := range map[string]gin.H{
		"missing stage":   {},
		"unknown stage":   {"stage": "closed"},
		"stage case":      {"stage": "Won"},
		"probability 101": {"stage": "proposal", "probability": 101},
		"reason too long": {"stage": "lost", "lost_reason": strings.Repeat("r", models.DealLostReasonMaxLength+1)},
	} {
		w := suite.do(http.MethodPost, "/deals/5/stage", body)
		assert.Equalf(suite.T(), http.StatusBadRequest, w.Code, "%s: %s", name, w.Body.String())
	}
}

func (suite *DealHandlerTestSuite) TestChangeStage_UnknownDealIs404() {
	suite.mockService.On("GetByID", uint(404)).Return(nil, fmt.Errorf("deal 404 not found: %w", apperrors.ErrNotFound))
	w := suite.do(http.MethodPost, "/deals/404/stage", gin.H{"stage": "won"})
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
}

// --- History ----------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestHistory_ReturnsTheRowsAsGivenWithTheUser() {
	from := models.DealStageQualification
	suite.mockService.On("GetByID", uint(5)).Return(ownDeal(5), nil)
	suite.mockService.On("History", uint(5)).Return([]models.DealStageChange{
		{ID: 1, DealID: 5, FromStage: nil, ToStage: models.DealStageQualification, ChangedByID: 1, ChangedBy: &models.User{BaseModel: models.BaseModel{ID: 1}, Email: "admin@example.com", FirstName: "Ad", LastName: "Min"}},
		{ID: 2, DealID: 5, FromStage: &from, ToStage: models.DealStageProposal, ChangedByID: 1},
	}, nil)

	w := suite.do(http.MethodGet, "/deals/5/history", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var response struct {
		Data []map[string]interface{} `json:"data"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	suite.Require().Len(response.Data, 2)
	assert.Nil(suite.T(), response.Data[0]["from_stage"], "the creating row carries a null from_stage")
	assert.Equal(suite.T(), "qualification", response.Data[0]["to_stage"])
	user, _ := response.Data[0]["changed_by"].(map[string]interface{})
	suite.Require().NotNil(user)
	assert.Equal(suite.T(), "admin@example.com", user["email"])
	assert.Equal(suite.T(), "Ad", user["first_name"])
	assert.NotContains(suite.T(), w.Body.String(), `"password"`)
	assert.Equal(suite.T(), "qualification", response.Data[1]["from_stage"])
}

// --- Delete -----------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestDelete_SuccessNotFoundAndFailure() {
	suite.mockService.On("Delete", uint(5)).Return(nil).Once()
	w := suite.do(http.MethodDelete, "/deals/5", nil)
	assert.Equal(suite.T(), http.StatusNoContent, w.Code)

	suite.mockService.On("Delete", uint(404)).Return(fmt.Errorf("deal 404 not found: %w", apperrors.ErrNotFound)).Once()
	w = suite.do(http.MethodDelete, "/deals/404", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)

	suite.mockService.On("Delete", uint(6)).Return(errors.New("tx failed")).Once()
	w = suite.do(http.MethodDelete, "/deals/6", nil)
	assert.Equal(suite.T(), http.StatusInternalServerError, w.Code)
	assert.NotContains(suite.T(), w.Body.String(), "tx failed")

	w = suite.do(http.MethodDelete, "/deals/abc", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

// --- Sub-lists --------------------------------------------------------------

func (suite *DealHandlerTestSuite) TestSubLists_SalesIsNarrowedAdminIsNotAndMissingParentsAre404() {
	suite.role = models.RoleSales
	suite.userID = 42
	suite.mockService.On("ListByCompany", uint(5), mock.MatchedBy(func(ownerID *uint) bool {
		return ownerID != nil && *ownerID == 42
	}), 0, 20).Return([]models.Deal{}, int64(0), nil).Once()
	w := suite.do(http.MethodGet, "/companies/5/deals", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	suite.mockService.On("ListByCustomer", uint(6), mock.MatchedBy(func(ownerID *uint) bool {
		return ownerID != nil && *ownerID == 42
	}), 0, 20).Return([]models.Deal{}, int64(0), nil).Once()
	w = suite.do(http.MethodGet, "/customers/6/deals", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	suite.role = models.RoleAdmin
	suite.mockService.On("ListByCompany", uint(5), mock.MatchedBy(func(ownerID *uint) bool {
		return ownerID == nil
	}), 10, 10).Return([]models.Deal{*ownDeal(3)}, int64(11), nil).Once()
	w = suite.do(http.MethodGet, "/companies/5/deals?page=2&limit=10", nil)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.Contains(suite.T(), w.Body.String(), `"total":11`)
	assert.Contains(suite.T(), w.Body.String(), `"page":2`)

	suite.mockService.On("ListByCompany", uint(404), mock.Anything, 0, 20).
		Return(nil, int64(0), fmt.Errorf("company 404 not found: %w", apperrors.ErrNotFound)).Once()
	w = suite.do(http.MethodGet, "/companies/404/deals", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "Company not found")

	suite.mockService.On("ListByCustomer", uint(404), mock.Anything, 0, 20).
		Return(nil, int64(0), fmt.Errorf("customer 404 not found: %w", apperrors.ErrNotFound)).Once()
	w = suite.do(http.MethodGet, "/customers/404/deals", nil)
	assert.Equal(suite.T(), http.StatusNotFound, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "Customer not found")

	w = suite.do(http.MethodGet, "/companies/abc/deals", nil)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
}

func TestDealHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(DealHandlerTestSuite))
}

// Through the production error handler, not the suite's minimal one: an
// out-of-range probability is a number, so its message is a value bound and
// not a length.
func TestCreateDeal_ProbabilityOutOfRangeHasANumericMessage(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	svc := new(mocks.DealService)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	SetupDealRoutes(router.Group(""), NewDealHandler(svc))

	for probability, want := range map[int]string{
		101: "Probability must be at most 100",
		-1:  "Probability must be at least 0",
	} {
		body, err := json.Marshal(gin.H{"title": "X", "probability": probability})
		assert.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/deals", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		var resp struct {
			Success bool `json:"success"`
			Error   struct {
				Code    string            `json:"code"`
				Message string            `json:"message"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		assert.False(t, resp.Success)
		assert.Equal(t, utils.ErrCodeValidation, resp.Error.Code)
		assert.Equal(t, "Validation failed", resp.Error.Message)
		assert.Equal(t, want, resp.Error.Details["Probability"], w.Body.String())
	}
	svc.AssertExpectations(t)
}

// amount_cents is bounded at 10^12 cents, so the pipeline sums cannot
// overflow a BIGINT. One cent more
// is refused by the binding on create and on update, before the service is
// reached; the bound itself is accepted.
func TestDeal_AmountAboveTheBoundIsRefusedWithANumericMessage(t *testing.T) {
	const maxAmount = int64(1_000_000_000_000)
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	svc := new(mocks.DealService)
	router := gin.New()
	router.Use(middleware.RequestID(), middleware.ErrorHandler())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	SetupDealRoutes(router.Group(""), NewDealHandler(svc))

	// Primed with Maybe so that a missing bound shows up as a wrong status,
	// not as a panic on an unexpected call.
	svc.On("GetByID", uint(5)).Return(ownDeal(5), nil).Maybe()
	svc.On("GetByID", uint(0)).Return(nil, apperrors.ErrNotFound).Maybe()
	svc.On("Create", mock.Anything, mock.Anything, uint(1)).Return(nil).Maybe()
	svc.On("Update", mock.Anything, mock.Anything, uint(1)).Return(nil).Maybe()

	send := func(method, path string, amount int64) *httptest.ResponseRecorder {
		body, err := json.Marshal(gin.H{"title": "X", "amount_cents": amount})
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/deals"},
		{http.MethodPut, "/deals/5"},
	} {
		w := send(route.method, route.path, maxAmount+1)
		assert.Equalf(t, http.StatusBadRequest, w.Code, "%s %s: %s", route.method, route.path, w.Body.String())
		var resp struct {
			Error struct {
				Code    string            `json:"code"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		assert.Equal(t, utils.ErrCodeValidation, resp.Error.Code)
		assert.Equal(t, "AmountCents must be at most 1000000000000", resp.Error.Details["AmountCents"], w.Body.String())
	}
	svc.AssertNotCalled(t, "Create", mock.Anything, mock.Anything, mock.Anything)
	svc.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything)

	w := send(http.MethodPost, "/deals", maxAmount)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	svc.AssertCalled(t, "Create", mock.MatchedBy(func(d *models.Deal) bool { return d.AmountCents == maxAmount }), mock.Anything, uint(1))
}
