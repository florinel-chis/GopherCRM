package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// LeadVisibilityIntegrationTestSuite drives the task and deal endpoints through
// the real services and repositories with distinct owners and roles, and
// checks that a lead is only ever linked by, and shown to, a caller allowed to
// see it: admin every lead, sales its own, support none.
type LeadVisibilityIntegrationTestSuite struct {
	suite.Suite
	db      *gorm.DB
	router  *gin.Engine
	admin   *models.User
	alice   *models.User // sales, owns alicesLead
	bob     *models.User // sales, owns no lead at the start
	support *models.User

	alicesLead *models.Lead
}

// alicesLeadEmail is the lead data that must not reach anybody but Alice and
// the admin.
const alicesLeadEmail = "alices-prospect@example.com"

func (s *LeadVisibilityIntegrationTestSuite) SetupTest() {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file:lead_visibility?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	s.Require().NoError(err)
	all := []interface{}{&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{},
		&models.Label{}, &models.Task{}, &models.Deal{}, &models.DealStageChange{}}
	s.Require().NoError(db.Migrator().DropTable(append([]interface{}{"task_labels"}, all...)...))
	s.Require().NoError(db.AutoMigrate(all...))
	s.db = db
	s.T().Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	newUser := func(email string, role models.UserRole) *models.User {
		user := &models.User{Email: email, Password: "x", FirstName: "F", LastName: string(role), Role: role, IsActive: true}
		s.Require().NoError(db.Create(user).Error)
		return user
	}
	s.admin = newUser("lv-admin@example.com", models.RoleAdmin)
	s.alice = newUser("lv-alice@example.com", models.RoleSales)
	s.bob = newUser("lv-bob@example.com", models.RoleSales)
	s.support = newUser("lv-support@example.com", models.RoleSupport)
	s.alicesLead = &models.Lead{FirstName: "Prospect", LastName: "Alice", Email: alicesLeadEmail, OwnerID: s.alice.ID, Status: models.LeadStatusNew}
	s.Require().NoError(db.Create(s.alicesLead).Error)

	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	taskService := service.NewTaskService(repository.NewTaskRepository(db), userRepo, leadRepo, customerRepo, repository.NewLabelRepository(db))
	dealService := service.NewDealService(repository.NewDealRepository(db), repository.NewCompanyRepository(db), customerRepo,
		leadRepo, userRepo, nil, utils.NewTransactionManager(db))

	s.router = gin.New()
	s.router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	// Stands in for the auth middleware: the caller is a stored user, and its
	// id and role go into the context exactly as middleware.Auth puts them.
	s.router.Use(func(c *gin.Context) {
		id, err := strconv.ParseUint(c.GetHeader("X-Test-User"), 10, 32)
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		user, err := userRepo.GetByID(uint(id))
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("user_id", user.ID)
		c.Set("user_role", string(user.Role))
		c.Next()
	})
	handler.SetupTaskRoutes(s.router.Group(""), handler.NewTaskHandler(taskService))
	handler.SetupDealRoutes(s.router.Group(""), handler.NewDealHandler(dealService))
}

func (s *LeadVisibilityIntegrationTestSuite) call(as *models.User, method, path string, body interface{}) (int, map[string]interface{}, string) {
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		s.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", strconv.FormatUint(uint64(as.ID), 10))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	var envelope map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, envelope, w.Body.String()
}

func (s *LeadVisibilityIntegrationTestSuite) count(model interface{}) int64 {
	var n int64
	s.Require().NoError(s.db.Model(model).Count(&n).Error)
	return n
}

func (s *LeadVisibilityIntegrationTestSuite) storedTask(id uint) models.Task {
	var task models.Task
	s.Require().NoError(s.db.First(&task, id).Error)
	return task
}

func (s *LeadVisibilityIntegrationTestSuite) storedDeal(id uint) models.Deal {
	var deal models.Deal
	s.Require().NoError(s.db.First(&deal, id).Error)
	return deal
}

func dataID(envelope map[string]interface{}) uint {
	data, _ := envelope["data"].(map[string]interface{})
	id, _ := data["id"].(float64)
	return uint(id)
}

// assertNoLead checks a response body carries neither the lead's data nor a
// link to it.
func (s *LeadVisibilityIntegrationTestSuite) assertNoLead(body, context string) {
	s.NotContains(body, alicesLeadEmail, context)
	s.NotContains(body, `"lead_id"`, context)
	s.NotContains(body, `"lead":`, context)
}

// --- Tasks --------------------------------------------------------------------

// The reported support exploit: a self-assigned task linked to a lead. It is
// refused like a missing lead and no task is written.
func (s *LeadVisibilityIntegrationTestSuite) TestTaskCreate_SupportCannotLinkALead() {
	status, _, body := s.call(s.support, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Probe", "assigned_to_id": s.support.ID, "lead_id": s.alicesLead.ID,
	})
	s.Equal(http.StatusNotFound, status, body)
	s.NotContains(body, alicesLeadEmail)
	s.Equal(int64(0), s.count(&models.Task{}), "a refused create writes no task")

	// Indistinguishable from an id that matches no lead at all.
	missingStatus, _, _ := s.call(s.support, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Probe", "assigned_to_id": s.support.ID, "lead_id": 999999,
	})
	s.Equal(missingStatus, status)
}

func (s *LeadVisibilityIntegrationTestSuite) TestTaskCreate_SalesOnlyWithItsOwnLead() {
	status, _, body := s.call(s.bob, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Poach", "assigned_to_id": s.bob.ID, "lead_id": s.alicesLead.ID,
	})
	s.Equal(http.StatusNotFound, status, body)
	s.Equal(int64(0), s.count(&models.Task{}))

	status, envelope, body := s.call(s.alice, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Call my prospect", "assigned_to_id": s.alice.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	s.Contains(body, fmt.Sprintf(`"lead_id":%d`, s.alicesLead.ID), "the owner sees her lead on the response")
	stored := s.storedTask(dataID(envelope))
	s.Require().NotNil(stored.LeadID)
	s.Equal(s.alicesLead.ID, *stored.LeadID)
}

// An update that would point a task at a hidden lead is refused and changes
// nothing, not even the other fields of the same request.
func (s *LeadVisibilityIntegrationTestSuite) TestTaskUpdate_CannotRelinkToAHiddenLead() {
	task := &models.Task{Title: "Bob's task", AssignedToID: s.bob.ID, Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium}
	s.Require().NoError(s.db.Create(task).Error)

	status, _, body := s.call(s.bob, http.MethodPut, fmt.Sprintf("/tasks/%d", task.ID), map[string]interface{}{
		"title": "Hijacked", "lead_id": s.alicesLead.ID,
	})
	s.Equal(http.StatusNotFound, status, body)
	s.NotContains(body, alicesLeadEmail)
	stored := s.storedTask(task.ID)
	s.Nil(stored.LeadID, "a refused update leaves the link as it was")
	s.Equal("Bob's task", stored.Title, "and the rest of the task too")
}

// A link an admin made, a reassignment and a change of lead owner are all
// decided again on every read: the assignee who may not see the lead never
// gets it, on the detail or any listing, while the admin always does.
func (s *LeadVisibilityIntegrationTestSuite) TestTaskReads_HideHistoricalLinks() {
	// The admin links Alice's lead to a task for Bob: allowed for the admin.
	status, envelope, body := s.call(s.admin, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Admin-made link", "assigned_to_id": s.bob.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	s.Contains(body, alicesLeadEmail, "the admin sees the lead it linked")
	taskID := dataID(envelope)

	for _, path := range []string{fmt.Sprintf("/tasks/%d", taskID), "/tasks", "/tasks/my", "/tasks?sort_by=title"} {
		status, _, body = s.call(s.bob, http.MethodGet, path, nil)
		s.Require().Equal(http.StatusOK, status, "%s: %s", path, body)
		s.Contains(body, "Admin-made link", path)
		s.assertNoLead(body, "bob: "+path)
	}

	// Bob can still work on the task; the link stays stored, and the response
	// does not reveal it.
	status, _, body = s.call(s.bob, http.MethodPut, fmt.Sprintf("/tasks/%d", taskID), map[string]interface{}{"title": "Done by Bob"})
	s.Require().Equal(http.StatusOK, status, body)
	s.assertNoLead(body, "bob's update response")
	stored := s.storedTask(taskID)
	s.Equal("Done by Bob", stored.Title)
	s.Require().NotNil(stored.LeadID)
	s.Equal(s.alicesLead.ID, *stored.LeadID)

	// Admin reads and the admin's listings keep it.
	status, _, body = s.call(s.admin, http.MethodGet, fmt.Sprintf("/tasks/%d", taskID), nil)
	s.Require().Equal(http.StatusOK, status)
	s.Contains(body, alicesLeadEmail)
	status, _, body = s.call(s.admin, http.MethodGet, "/tasks", nil)
	s.Require().Equal(http.StatusOK, status)
	s.Contains(body, alicesLeadEmail, "admin listings carry the lead")

	// Reassignment to support: support gets the task, never the lead.
	status, _, body = s.call(s.admin, http.MethodPut, fmt.Sprintf("/tasks/%d", taskID), map[string]interface{}{"assigned_to_id": s.support.ID})
	s.Require().Equal(http.StatusOK, status, body)
	status, _, body = s.call(s.support, http.MethodGet, fmt.Sprintf("/tasks/%d", taskID), nil)
	s.Require().Equal(http.StatusOK, status, body)
	s.assertNoLead(body, "support after reassignment")
}

// The update oracle: the assignee of a task carrying a hidden link must not be
// able to find that link by sending lead ids. The stored hidden id is refused
// exactly like another hidden lead and like a missing one, and no probe writes
// anything. The owner of the lead may still send it back unchanged.
func (s *LeadVisibilityIntegrationTestSuite) TestTaskUpdate_SendingTheHiddenStoredLeadIsNoOracle() {
	otherLead := &models.Lead{FirstName: "Other", LastName: "Lead", Email: "other-hidden@example.com", OwnerID: s.alice.ID, Status: models.LeadStatusNew}
	s.Require().NoError(s.db.Create(otherLead).Error)

	for _, assignee := range []*models.User{s.bob, s.support} {
		status, envelope, body := s.call(s.admin, http.MethodPost, "/tasks", map[string]interface{}{
			"title": "Admin-made link", "assigned_to_id": assignee.ID, "lead_id": s.alicesLead.ID,
		})
		s.Require().Equal(http.StatusCreated, status, body)
		taskID := dataID(envelope)

		var answers []string
		for _, probe := range []uint{s.alicesLead.ID, otherLead.ID, 999999} {
			status, envelope, body = s.call(assignee, http.MethodPut, fmt.Sprintf("/tasks/%d", taskID), map[string]interface{}{
				"title": "Probe", "lead_id": probe,
			})
			s.Equalf(http.StatusNotFound, status, "%s probing lead %d: %s", assignee.Email, probe, body)
			s.NotContains(body, alicesLeadEmail)
			errBody, _ := envelope["error"].(map[string]interface{})
			answers = append(answers, fmt.Sprintf("%d %v", status, errBody["code"]))

			stored := s.storedTask(taskID)
			s.Equal("Admin-made link", stored.Title, "a refused probe writes nothing")
			s.Require().NotNil(stored.LeadID)
			s.Equal(s.alicesLead.ID, *stored.LeadID)
		}
		s.Equal(answers[0], answers[1], "%s: the stored hidden id answers like another hidden lead", assignee.Email)
		s.Equal(answers[0], answers[2], "%s: ...and like a missing one", assignee.Email)

		// Leaving lead_id out still lets the assignee work on the task.
		status, _, body = s.call(assignee, http.MethodPut, fmt.Sprintf("/tasks/%d", taskID), map[string]interface{}{"title": "Worked on"})
		s.Require().Equal(http.StatusOK, status, body)
		s.assertNoLead(body, assignee.Email+" update without lead_id")
	}

	// Alice owns the lead: echoing it back on her own task is accepted.
	status, envelope, body := s.call(s.alice, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Mine", "assigned_to_id": s.alice.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	taskID := dataID(envelope)
	status, _, body = s.call(s.alice, http.MethodPut, fmt.Sprintf("/tasks/%d", taskID), map[string]interface{}{
		"title": "Still mine", "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusOK, status, body)
	s.Equal("Still mine", s.storedTask(taskID).Title)
}

// A link Alice legitimately made stops showing once the lead is hers no
// longer, and shows to whoever owns the lead now.
func (s *LeadVisibilityIntegrationTestSuite) TestTaskReads_FollowLeadOwnershipChanges() {
	status, envelope, body := s.call(s.alice, http.MethodPost, "/tasks", map[string]interface{}{
		"title": "Mine for now", "assigned_to_id": s.alice.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	taskID := dataID(envelope)

	s.Require().NoError(s.db.Model(&models.Lead{}).Where("id = ?", s.alicesLead.ID).Update("owner_id", s.bob.ID).Error)

	status, _, body = s.call(s.alice, http.MethodGet, fmt.Sprintf("/tasks/%d", taskID), nil)
	s.Require().Equal(http.StatusOK, status, body)
	s.assertNoLead(body, "alice after the lead moved to bob")
}

// --- Deals --------------------------------------------------------------------

// The reported sales exploit: another rep's lead linked to an owned deal.
func (s *LeadVisibilityIntegrationTestSuite) TestDealWrites_SalesCannotLinkAnotherRepsLead() {
	status, envelope, body := s.call(s.bob, http.MethodPost, "/deals", map[string]interface{}{"title": "Poach", "lead_id": s.alicesLead.ID})
	s.Equal(http.StatusBadRequest, status, body)
	errBody, _ := envelope["error"].(map[string]interface{})
	s.Equal("INVALID_REFERENCE", errBody["code"], "the same answer as an unknown lead_id")
	s.NotContains(body, alicesLeadEmail)
	s.Equal(int64(0), s.count(&models.Deal{}))
	s.Equal(int64(0), s.count(&models.DealStageChange{}))

	status, envelope, body = s.call(s.bob, http.MethodPost, "/deals", map[string]interface{}{"title": "Bob's deal"})
	s.Require().Equal(http.StatusCreated, status, body)
	dealID := dataID(envelope)

	status, _, body = s.call(s.bob, http.MethodPut, fmt.Sprintf("/deals/%d", dealID), map[string]interface{}{"title": "Relinked", "lead_id": s.alicesLead.ID})
	s.Equal(http.StatusBadRequest, status, body)
	stored := s.storedDeal(dealID)
	s.Nil(stored.LeadID, "a refused update leaves the link as it was")
	s.Equal("Bob's deal", stored.Title)

	// Alice links her own lead to her own deal.
	status, _, body = s.call(s.alice, http.MethodPost, "/deals", map[string]interface{}{"title": "Alice's deal", "lead_id": s.alicesLead.ID})
	s.Require().Equal(http.StatusCreated, status, body)
	s.Contains(body, alicesLeadEmail, "the owner sees her lead")
}

// An admin-made link on Bob's deal is hidden from Bob on every deal route and
// appears for him as soon as the lead becomes his.
func (s *LeadVisibilityIntegrationTestSuite) TestDealReads_HideHistoricalLinks() {
	status, envelope, body := s.call(s.admin, http.MethodPost, "/deals", map[string]interface{}{
		"title": "Admin-linked", "owner_id": s.bob.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	dealID := dataID(envelope)

	for _, route := range []struct {
		method, path string
		body         interface{}
	}{
		{http.MethodGet, fmt.Sprintf("/deals/%d", dealID), nil},
		{http.MethodGet, "/deals", nil},
		{http.MethodPut, fmt.Sprintf("/deals/%d", dealID), map[string]interface{}{"title": "Edited by Bob"}},
		{http.MethodPost, fmt.Sprintf("/deals/%d/stage", dealID), map[string]interface{}{"stage": "proposal"}},
	} {
		status, _, body = s.call(s.bob, route.method, route.path, route.body)
		s.Require().Equalf(http.StatusOK, status, "%s %s: %s", route.method, route.path, body)
		s.assertNoLead(body, "bob: "+route.method+" "+route.path)
	}
	stored := s.storedDeal(dealID)
	s.Equal("Edited by Bob", stored.Title, "the owner can still work the deal")
	s.Require().NotNil(stored.LeadID, "the link itself is kept")

	status, _, body = s.call(s.admin, http.MethodGet, fmt.Sprintf("/deals/%d", dealID), nil)
	s.Require().Equal(http.StatusOK, status)
	s.Contains(body, alicesLeadEmail, "the admin sees every link")

	s.Require().NoError(s.db.Model(&models.Lead{}).Where("id = ?", s.alicesLead.ID).Update("owner_id", s.bob.ID).Error)
	status, _, body = s.call(s.bob, http.MethodGet, fmt.Sprintf("/deals/%d", dealID), nil)
	s.Require().Equal(http.StatusOK, status)
	s.Contains(body, alicesLeadEmail, "the new owner of the lead sees it")
}

// The update oracle on deals: Bob owns a deal an admin linked to Alice's lead.
// Sending that lead_id back is the same 400 INVALID_REFERENCE as another
// hidden lead or a missing one, and nothing is written. Alice may echo her own
// lead back on her own deal.
func (s *LeadVisibilityIntegrationTestSuite) TestDealUpdate_SendingTheHiddenStoredLeadIsNoOracle() {
	otherLead := &models.Lead{FirstName: "Other", LastName: "Lead", Email: "other-hidden@example.com", OwnerID: s.alice.ID, Status: models.LeadStatusNew}
	s.Require().NoError(s.db.Create(otherLead).Error)

	status, envelope, body := s.call(s.admin, http.MethodPost, "/deals", map[string]interface{}{
		"title": "Admin-linked", "owner_id": s.bob.ID, "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusCreated, status, body)
	dealID := dataID(envelope)
	history := s.count(&models.DealStageChange{})

	var answers []string
	for _, probe := range []uint{s.alicesLead.ID, otherLead.ID, 999999} {
		status, envelope, body = s.call(s.bob, http.MethodPut, fmt.Sprintf("/deals/%d", dealID), map[string]interface{}{
			"title": "Probe", "stage": "proposal", "lead_id": probe,
		})
		s.Equalf(http.StatusBadRequest, status, "probing lead %d: %s", probe, body)
		s.NotContains(body, alicesLeadEmail)
		errBody, _ := envelope["error"].(map[string]interface{})
		s.Equal("INVALID_REFERENCE", errBody["code"])
		answers = append(answers, fmt.Sprintf("%d %v", status, errBody["code"]))

		stored := s.storedDeal(dealID)
		s.Equal("Admin-linked", stored.Title, "a refused probe writes nothing")
		s.Equal(models.DealStageQualification, stored.Stage)
		s.Require().NotNil(stored.LeadID)
		s.Equal(s.alicesLead.ID, *stored.LeadID)
	}
	s.Equal(answers[0], answers[1], "the stored hidden id answers like another hidden lead")
	s.Equal(answers[0], answers[2], "...and like a missing one")
	s.Equal(history, s.count(&models.DealStageChange{}), "no probe writes a history row")

	status, envelope, body = s.call(s.alice, http.MethodPost, "/deals", map[string]interface{}{"title": "Alice's deal", "lead_id": s.alicesLead.ID})
	s.Require().Equal(http.StatusCreated, status, body)
	aliceDeal := dataID(envelope)
	status, _, body = s.call(s.alice, http.MethodPut, fmt.Sprintf("/deals/%d", aliceDeal), map[string]interface{}{
		"title": "Alice's deal, edited", "lead_id": s.alicesLead.ID,
	})
	s.Require().Equal(http.StatusOK, status, body)
	s.Contains(body, alicesLeadEmail, "the owner keeps seeing her lead")
	s.Equal("Alice's deal, edited", s.storedDeal(aliceDeal).Title)
}

func TestLeadVisibilityIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(LeadVisibilityIntegrationTestSuite))
}
