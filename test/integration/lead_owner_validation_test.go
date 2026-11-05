package integration

// Regression roots of issue #255 (with #185): POST /leads, PUT /leads/{id} and
// POST /leads/{id}/assign refuse an owner that is not an active admin or sales
// user with 400 INVALID_REFERENCE and write nothing. Everything runs through
// the real SetupLeadRoutes, auth middleware, handler, service and repositories
// over the production schema: a SQLite file with foreign keys on and the
// one-connection pool, and a disposable schema on the TEST_MYSQL_DSN server (the
// MySQL roots skip without the variable and fail when the server is
// unreachable). Every assertion reads committed rows outside any transaction.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const (
	leadOwnerFieldMessage = "owner_id must name an active admin or sales user"
	leadUserFieldMessage  = "user_id must name an active admin or sales user"
	leadOwnerUnknownID    = uint(999999)
)

var leadOwnerSeq atomic.Uint64

func leadOwnerNext() uint64 { return leadOwnerSeq.Add(1) }

type leadOwnerEnv struct {
	t          *testing.T
	db         *gorm.DB
	router     *gin.Engine
	auth       service.AuthService
	users      repository.UserRepository
	leadRepo   repository.LeadRepository
	automation repository.AutomationRepository
	admin      *models.User
	sales      *models.User
	otherSales *models.User
	support    *models.User
	customer   *models.User
}

// openLeadOwnerDB opens the database and migrates it the way the application
// does. A failed insert is the proof that the foreign key is really enforced.
func openLeadOwnerDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	var db *gorm.DB
	if useMySQL {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		cfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		cfg.DBName = ""
		cfg.ParseTime = true
		cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", cfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_lead_owner_%d_%d", time.Now().UnixNano(), leadOwnerNext())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		cfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		dsn := filepath.Join(t.TempDir(), "lead_owner.db") +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		var err error
		db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}

	previous := models.DB
	models.DB = db
	t.Cleanup(func() { models.DB = previous })
	require.NoError(t, models.MigrateDatabase())
	return db
}

func newLeadOwnerEnv(t *testing.T, useMySQL bool) *leadOwnerEnv {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)

	db := openLeadOwnerDB(t, useMySQL)
	users := repository.NewUserRepository(db)
	env := &leadOwnerEnv{
		t:          t,
		db:         db,
		users:      users,
		leadRepo:   repository.NewLeadRepository(db),
		automation: repository.NewAutomationRepository(db),
		auth: service.NewAuthService(users, repository.NewAPIKeyRepository(db), config.JWTConfig{
			Secret:      "lead-owner-validation-regression-secret",
			ExpiryHours: 1,
		}),
	}
	env.router = env.buildRouter(users)

	env.admin = env.newUser("admin", models.RoleAdmin)
	env.sales = env.newUser("sales", models.RoleSales)
	env.otherSales = env.newUser("other-sales", models.RoleSales)
	env.support = env.newUser("support", models.RoleSupport)
	env.customer = env.newUser("customer", models.RoleCustomer)

	// The check under test must not be standing in for a missing constraint.
	bad := &models.Lead{FirstName: "No", LastName: "Owner", Email: "fk-probe@example.com", OwnerID: leadOwnerUnknownID}
	require.Error(t, db.Create(bad).Error, "the fixture must enforce the owner foreign key")
	return env
}

// buildRouter mounts the production lead routes with the owner lookup going
// through users; the auth middleware keeps its own repository.
func (e *leadOwnerEnv) buildRouter(users repository.UserRepository) *gin.Engine {
	leadService := service.NewLeadService(e.leadRepo, repository.NewCustomerRepository(e.db),
		repository.NewCompanyRepository(e.db), users, utils.NewTransactionManager(e.db),
		service.WithLeadAutomationEvents(e.automation))

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(e.auth))
	handler.SetupLeadRoutes(protected, handler.NewLeadHandler(leadService))
	return router
}

func (e *leadOwnerEnv) leadService() service.LeadService {
	return service.NewLeadService(e.leadRepo, repository.NewCustomerRepository(e.db),
		repository.NewCompanyRepository(e.db), e.users, utils.NewTransactionManager(e.db),
		service.WithLeadAutomationEvents(e.automation))
}

func (e *leadOwnerEnv) newUser(label string, role models.UserRole) *models.User {
	e.t.Helper()
	seq := leadOwnerNext()
	user := &models.User{
		Email:     fmt.Sprintf("%s-%d@lead-owner.test", label, seq),
		Password:  "not-a-usable-hash",
		FirstName: "First" + label,
		LastName:  "Last" + label,
		Role:      role,
		IsActive:  true,
	}
	require.NoError(e.t, e.db.Create(user).Error)
	return user
}

func (e *leadOwnerEnv) deactivate(user *models.User) {
	e.t.Helper()
	require.NoError(e.t, e.db.Model(&models.User{}).Where("id = ?", user.ID).Update("is_active", false).Error)
}

// erase erases the user through the repository and checks the row survives to
// satisfy the foreign key, which is why this case needs its own check.
func (e *leadOwnerEnv) erase(user *models.User) {
	e.t.Helper()
	require.NoError(e.t, e.users.Delete(user.ID))
	var row models.User
	require.NoError(e.t, e.db.Unscoped().First(&row, user.ID).Error, "the erased row stays for the foreign key")
	require.True(e.t, row.DeletedAt.Valid)
}

func (e *leadOwnerEnv) newLead(ownerID uint) *models.Lead {
	e.t.Helper()
	seq := leadOwnerNext()
	lead := &models.Lead{
		FirstName: "Lee",
		LastName:  fmt.Sprintf("Prospect%d", seq),
		Email:     fmt.Sprintf("lead-%d@lead-owner.test", seq),
		Status:    models.LeadStatusQualified,
		Notes:     "original notes",
		OwnerID:   ownerID,
	}
	require.NoError(e.t, e.db.Create(lead).Error)
	return lead
}

func (e *leadOwnerEnv) createBody(owner interface{}) map[string]interface{} {
	body := map[string]interface{}{
		"first_name": "New",
		"last_name":  "Lead",
		"email":      fmt.Sprintf("created-%d@lead-owner.test", leadOwnerNext()),
	}
	if owner != nil {
		body["owner_id"] = owner
	}
	return body
}

// send issues one request. caller nil sends no credential; body may be nil, a
// string sent verbatim, or a value encoded as JSON.
func (e *leadOwnerEnv) send(method, path string, caller *models.User, body interface{}) (int, utils.APIResponse) {
	e.t.Helper()
	var payload []byte
	switch value := body.(type) {
	case nil:
	case string:
		payload = []byte(value)
	default:
		encoded, err := json.Marshal(value)
		require.NoError(e.t, err)
		payload = encoded
	}
	request := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(payload))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if caller != nil {
		token, err := e.auth.GenerateJWT(caller)
		require.NoError(e.t, err)
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	var envelope utils.APIResponse
	require.NoError(e.t, json.Unmarshal(recorder.Body.Bytes(), &envelope), "body: %s", recorder.Body.String())
	return recorder.Code, envelope
}

func leadPath(id uint) string       { return fmt.Sprintf("/leads/%d", id) }
func leadAssignPath(id uint) string { return fmt.Sprintf("/leads/%d/assign", id) }

func (e *leadOwnerEnv) committedLead(id uint) models.Lead {
	e.t.Helper()
	var lead models.Lead
	require.NoError(e.t, e.db.First(&lead, id).Error)
	return lead
}

func (e *leadOwnerEnv) leadEvents(id uint) []models.AutomationEvent {
	e.t.Helper()
	var events []models.AutomationEvent
	require.NoError(e.t, e.db.Where("entity_type = ? AND entity_id = ?", models.AutomationEntityLead, id).Order("id ASC").Find(&events).Error)
	return events
}

// leadOwnerWrites is what a refused request must leave exactly as it was.
type leadOwnerWrites struct {
	leads, events int64
	owner         uint
	notes         string
}

func (e *leadOwnerEnv) writes(leadID uint) leadOwnerWrites {
	e.t.Helper()
	var current leadOwnerWrites
	require.NoError(e.t, e.db.Unscoped().Model(&models.Lead{}).Count(&current.leads).Error)
	require.NoError(e.t, e.db.Model(&models.AutomationEvent{}).Count(&current.events).Error)
	if leadID != 0 {
		lead := e.committedLead(leadID)
		current.owner, current.notes = lead.OwnerID, lead.Notes
	}
	return current
}

func (e *leadOwnerEnv) assertInvalidReference(status int, envelope utils.APIResponse, field, message string) {
	e.t.Helper()
	require.Equal(e.t, http.StatusBadRequest, status, "%+v", envelope.Error)
	require.NotNil(e.t, envelope.Error)
	assert.Equal(e.t, apperrors.CodeInvalidReference, envelope.Error.Code)
	assert.Equal(e.t, message, envelope.Error.Message)
	details, ok := envelope.Error.Details.(map[string]interface{})
	require.True(e.t, ok, "details: %+v", envelope.Error.Details)
	assert.Equal(e.t, field, details["field"])
}

// assertOwnerRefusedOnEveryRoute drives the three writers with ownerID and
// checks each refusal, and that it wrote nothing.
func (e *leadOwnerEnv) assertOwnerRefusedOnEveryRoute(ownerID uint) {
	e.t.Helper()
	lead := e.newLead(e.sales.ID)
	before := e.writes(lead.ID)

	status, envelope := e.send(http.MethodPost, "/leads", e.admin, e.createBody(ownerID))
	e.assertInvalidReference(status, envelope, "owner_id", leadOwnerFieldMessage)
	assert.Equal(e.t, before, e.writes(lead.ID), "POST /leads wrote something")

	status, envelope = e.send(http.MethodPut, leadPath(lead.ID), e.admin, map[string]interface{}{"owner_id": ownerID, "notes": "must not stick"})
	e.assertInvalidReference(status, envelope, "owner_id", leadOwnerFieldMessage)
	assert.Equal(e.t, before, e.writes(lead.ID), "PUT /leads/{id} wrote something")

	status, envelope = e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": ownerID})
	e.assertInvalidReference(status, envelope, "user_id", leadUserFieldMessage)
	assert.Equal(e.t, before, e.writes(lead.ID), "POST /leads/{id}/assign wrote something")
}

// --- AC-1: an owner id that names no user ------------------------------------

func TestLeadOwnerUnknownRejectedSQLite(t *testing.T) { runLeadOwnerUnknownRejected(t, false) }
func TestLeadOwnerUnknownRejectedMySQL(t *testing.T)  { runLeadOwnerUnknownRejected(t, true) }

func runLeadOwnerUnknownRejected(t *testing.T, useMySQL bool) {
	e := newLeadOwnerEnv(t, useMySQL)
	e.assertOwnerRefusedOnEveryRoute(leadOwnerUnknownID)

	lead := e.newLead(e.sales.ID)
	before := e.writes(lead.ID)

	status, envelope := e.send(http.MethodPost, "/leads", e.admin, e.createBody(0))
	e.assertInvalidReference(status, envelope, "owner_id", leadOwnerFieldMessage)
	status, envelope = e.send(http.MethodPut, leadPath(lead.ID), e.admin, map[string]interface{}{"owner_id": 0, "notes": "must not stick"})
	e.assertInvalidReference(status, envelope, "owner_id", leadOwnerFieldMessage)

	// 0 is not a user id at all on the route where the field is required.
	status, envelope = e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": 0})
	require.Equal(t, http.StatusBadRequest, status)
	require.NotNil(t, envelope.Error)
	assert.Equal(t, utils.ErrCodeValidation, envelope.Error.Code)

	assert.Equal(t, before, e.writes(lead.ID))
}

// --- AC-2: a deactivated user ------------------------------------------------

func TestLeadOwnerDeactivatedRejectedSQLite(t *testing.T) { runLeadOwnerDeactivatedRejected(t, false) }
func TestLeadOwnerDeactivatedRejectedMySQL(t *testing.T)  { runLeadOwnerDeactivatedRejected(t, true) }

func runLeadOwnerDeactivatedRejected(t *testing.T, useMySQL bool) {
	e := newLeadOwnerEnv(t, useMySQL)
	for _, role := range []models.UserRole{models.RoleSales, models.RoleAdmin} {
		gone := e.newUser("deactivated", role)
		e.deactivate(gone)
		e.assertOwnerRefusedOnEveryRoute(gone.ID)
	}
}

// --- AC-3: a user erased through UserRepository.Delete -----------------------

func TestLeadOwnerErasedRejectedSQLite(t *testing.T) { runLeadOwnerErasedRejected(t, false) }
func TestLeadOwnerErasedRejectedMySQL(t *testing.T)  { runLeadOwnerErasedRejected(t, true) }

func runLeadOwnerErasedRejected(t *testing.T, useMySQL bool) {
	e := newLeadOwnerEnv(t, useMySQL)
	erased := e.newUser("erased", models.RoleSales)
	e.erase(erased)
	e.assertOwnerRefusedOnEveryRoute(erased.ID)
}

// --- AC-4: an active user who may not own a lead -----------------------------

func TestLeadOwnerWrongRoleRejectedSQLite(t *testing.T) { runLeadOwnerWrongRoleRejected(t, false) }
func TestLeadOwnerWrongRoleRejectedMySQL(t *testing.T)  { runLeadOwnerWrongRoleRejected(t, true) }

func runLeadOwnerWrongRoleRejected(t *testing.T, useMySQL bool) {
	e := newLeadOwnerEnv(t, useMySQL)
	e.assertOwnerRefusedOnEveryRoute(e.support.ID)
	e.assertOwnerRefusedOnEveryRoute(e.customer.ID)
}

// --- AC-5: valid owners ------------------------------------------------------

func TestLeadOwnerValidAcceptedSQLite(t *testing.T) { runLeadOwnerValidAccepted(t, false) }
func TestLeadOwnerValidAcceptedMySQL(t *testing.T)  { runLeadOwnerValidAccepted(t, true) }

func (e *leadOwnerEnv) assertOneEvent(leadID uint, eventsBefore int, eventType string) {
	e.t.Helper()
	events := e.leadEvents(leadID)
	require.Len(e.t, events, eventsBefore+1, "exactly one new event")
	last := events[len(events)-1]
	assert.Equal(e.t, eventType, last.EventType)
	assert.Equal(e.t, models.AutomationOriginUser, last.Origin)
}

func runLeadOwnerValidAccepted(t *testing.T, useMySQL bool) {
	e := newLeadOwnerEnv(t, useMySQL)

	for _, owner := range []*models.User{e.admin, e.sales} {
		status, envelope := e.send(http.MethodPost, "/leads", e.admin, e.createBody(owner.ID))
		require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
		created := uint(dataMap(envelope)["id"].(float64))
		assert.Equal(t, owner.ID, e.committedLead(created).OwnerID)
		e.assertOneEvent(created, 0, models.AutomationEventLeadCreated)

		updated := e.newLead(e.otherSales.ID)
		status, envelope = e.send(http.MethodPut, leadPath(updated.ID), e.admin, map[string]interface{}{"owner_id": owner.ID})
		require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
		assert.Equal(t, owner.ID, e.committedLead(updated.ID).OwnerID)
		e.assertOneEvent(updated.ID, 0, models.AutomationEventLeadUpdated)

		assigned := e.newLead(e.otherSales.ID)
		status, envelope = e.send(http.MethodPost, leadAssignPath(assigned.ID), e.admin, map[string]interface{}{"user_id": owner.ID})
		require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
		assert.Equal(t, float64(owner.ID), dataMap(envelope)["owner_id"])
		assert.Equal(t, owner.ID, e.committedLead(assigned.ID).OwnerID)
		e.assertOneEvent(assigned.ID, 0, models.AutomationEventLeadUpdated)
	}

	for _, body := range []map[string]interface{}{e.createBody(nil), e.createBody(e.sales.ID)} {
		status, envelope := e.send(http.MethodPost, "/leads", e.sales, body)
		require.Equal(t, http.StatusCreated, status, "%+v", envelope.Error)
		created := uint(dataMap(envelope)["id"].(float64))
		assert.Equal(t, e.sales.ID, e.committedLead(created).OwnerID)
		e.assertOneEvent(created, 0, models.AutomationEventLeadCreated)
	}
}

// --- AC-6: who may call the assign route -------------------------------------

func TestLeadAssignPermissionsSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	valid := map[string]interface{}{"user_id": e.otherSales.ID}
	invalid := map[string]interface{}{"user_id": leadOwnerUnknownID}
	foreign := e.newLead(e.otherSales.ID)

	t.Run("no credential", func(t *testing.T) {
		status, _ := e.send(http.MethodPost, leadAssignPath(foreign.ID), nil, valid)
		assert.Equal(t, http.StatusUnauthorized, status)
	})
	t.Run("roles without lead access", func(t *testing.T) {
		before := e.writes(foreign.ID)
		for _, caller := range []*models.User{e.support, e.customer} {
			status, _ := e.send(http.MethodPost, leadAssignPath(foreign.ID), caller, valid)
			assert.Equal(t, http.StatusForbidden, status)
		}
		assert.Equal(t, before, e.writes(foreign.ID))
	})
	t.Run("a sales user on another owner's lead", func(t *testing.T) {
		before := e.writes(foreign.ID)
		for _, body := range []map[string]interface{}{valid, invalid} {
			status, envelope := e.send(http.MethodPost, leadAssignPath(foreign.ID), e.sales, body)
			require.Equal(t, http.StatusForbidden, status, "visibility answers before the owner check")
			assert.Equal(t, utils.ErrCodeForbidden, envelope.Error.Code)
		}
		assert.Equal(t, before, e.writes(foreign.ID))
	})
	t.Run("a lead that never existed", func(t *testing.T) {
		for _, caller := range []*models.User{e.admin, e.sales} {
			status, envelope := e.send(http.MethodPost, leadAssignPath(4242424), caller, valid)
			require.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, utils.ErrCodeNotFound, envelope.Error.Code)
		}
	})
	t.Run("a lead erased through LeadRepository.Delete", func(t *testing.T) {
		erased := e.newLead(e.sales.ID)
		require.NoError(t, e.leadRepo.Delete(erased.ID))
		for _, caller := range []*models.User{e.admin, e.sales} {
			status, _ := e.send(http.MethodPost, leadAssignPath(erased.ID), caller, valid)
			assert.Equal(t, http.StatusNotFound, status)
		}
	})
	t.Run("a non-numeric id", func(t *testing.T) {
		status, envelope := e.send(http.MethodPost, "/leads/not-a-number/assign", e.admin, valid)
		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, utils.ErrCodeBadRequest, envelope.Error.Code)
	})
	t.Run("a missing or malformed body", func(t *testing.T) {
		lead := e.newLead(e.sales.ID)
		before := e.writes(lead.ID)
		for _, tc := range []struct {
			name string
			body interface{}
			code string
		}{
			{"no body", nil, utils.ErrCodeBadRequest},
			{"truncated JSON", `{"user_id":`, utils.ErrCodeBadRequest},
			{"wrong type", `{"user_id":"seven"}`, utils.ErrCodeBadRequest},
			{"negative", `{"user_id":-1}`, utils.ErrCodeBadRequest},
			{"no user_id", `{}`, utils.ErrCodeValidation},
		} {
			status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, tc.body)
			require.Equal(t, http.StatusBadRequest, status, tc.name)
			assert.Equal(t, tc.code, envelope.Error.Code, tc.name)
		}
		assert.Equal(t, before, e.writes(lead.ID))
	})
	t.Run("an admin on any lead", func(t *testing.T) {
		lead := e.newLead(e.otherSales.ID)
		status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": e.sales.ID})
		require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
		assert.Equal(t, e.sales.ID, e.committedLead(lead.ID).OwnerID)
	})
	t.Run("a sales user on its own lead", func(t *testing.T) {
		lead := e.newLead(e.sales.ID)
		status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.sales, map[string]interface{}{"user_id": e.admin.ID})
		require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
		assert.Equal(t, e.admin.ID, e.committedLead(lead.ID).OwnerID)
	})
}

// --- AC-7: a sales user hands its lead over ----------------------------------

func TestLeadAssignSalesHandOffSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	lead := e.newLead(e.sales.ID)

	status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.sales, map[string]interface{}{"user_id": e.otherSales.ID})
	require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
	assert.Equal(t, e.otherSales.ID, e.committedLead(lead.ID).OwnerID)

	status, _ = e.send(http.MethodGet, leadPath(lead.ID), e.sales, nil)
	assert.Equal(t, http.StatusForbidden, status, "the previous owner has lost the lead")
	status, _ = e.send(http.MethodPost, leadAssignPath(lead.ID), e.sales, map[string]interface{}{"user_id": e.sales.ID})
	assert.Equal(t, http.StatusForbidden, status, "the previous owner cannot take it back")
	assert.Equal(t, e.otherSales.ID, e.committedLead(lead.ID).OwnerID)

	status, _ = e.send(http.MethodGet, leadPath(lead.ID), e.otherSales, nil)
	assert.Equal(t, http.StatusOK, status)
}

// --- AC-8: an update that does not change a departed owner -------------------

func TestLeadUpdateUnchangedOwnerNotRevalidatedSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	for name, depart := range map[string]func(*models.User){"deactivated": e.deactivate, "erased": e.erase} {
		t.Run(name, func(t *testing.T) {
			owner := e.newUser(name, models.RoleSales)
			lead := e.newLead(owner.ID)
			depart(owner)

			status, envelope := e.send(http.MethodPut, leadPath(lead.ID), e.admin, map[string]interface{}{"notes": "edited without owner_id"})
			require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
			stored := e.committedLead(lead.ID)
			assert.Equal(t, "edited without owner_id", stored.Notes)
			assert.Equal(t, owner.ID, stored.OwnerID)

			status, envelope = e.send(http.MethodPut, leadPath(lead.ID), e.admin, map[string]interface{}{"owner_id": owner.ID, "notes": "edited with the same owner_id"})
			require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
			stored = e.committedLead(lead.ID)
			assert.Equal(t, "edited with the same owner_id", stored.Notes)
			assert.Equal(t, owner.ID, stored.OwnerID)
		})
	}
}

// --- AC-9: an assignment always checks the user it names ---------------------

func TestLeadAssignRevalidatesUnchangedOwnerSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)

	owner := e.newUser("departed", models.RoleSales)
	lead := e.newLead(owner.ID)
	e.deactivate(owner)
	before := e.writes(lead.ID)
	status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": owner.ID})
	e.assertInvalidReference(status, envelope, "user_id", leadUserFieldMessage)
	assert.Equal(t, before, e.writes(lead.ID))

	active := e.newLead(e.sales.ID)
	status, envelope = e.send(http.MethodPost, leadAssignPath(active.ID), e.admin, map[string]interface{}{"user_id": e.sales.ID})
	require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)
	assert.Equal(t, e.sales.ID, e.committedLead(active.ID).OwnerID)
}

// --- AC-10: role rules answer before the owner check -------------------------

func TestLeadOwnerRoleRulesPrecedeOwnerCheckSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	own := e.newLead(e.sales.ID)
	foreign := e.newLead(e.otherSales.ID)
	beforeOwn, beforeForeign := e.writes(own.ID), e.writes(foreign.ID)

	for _, ownerID := range []uint{e.otherSales.ID, leadOwnerUnknownID} {
		status, _ := e.send(http.MethodPost, "/leads", e.sales, e.createBody(ownerID))
		assert.Equal(t, http.StatusForbidden, status, "POST naming %d", ownerID)
	}
	for _, ownerID := range []uint{e.sales.ID, e.otherSales.ID, leadOwnerUnknownID} {
		status, _ := e.send(http.MethodPut, leadPath(own.ID), e.sales, map[string]interface{}{"owner_id": ownerID, "notes": "must not stick"})
		assert.Equal(t, http.StatusForbidden, status, "PUT on its own lead naming %d", ownerID)
	}
	for _, body := range []map[string]interface{}{
		{"notes": "must not stick"},
		{"owner_id": e.sales.ID},
		{"owner_id": leadOwnerUnknownID},
	} {
		status, _ := e.send(http.MethodPut, leadPath(foreign.ID), e.sales, body)
		assert.Equal(t, http.StatusForbidden, status, "PUT on another owner's lead: %v", body)
	}

	assert.Equal(t, beforeOwn, e.writes(own.ID))
	assert.Equal(t, beforeForeign, e.writes(foreign.ID))
}

// --- AC-11: a failed lookup is not a bad reference ---------------------------

var errOwnerLookupFailed = errors.New("driver: bad connection to the users table")

// failingOwnerLookup fails the service's owner read on every handle.
type failingOwnerLookup struct {
	repository.UserRepository
}

func (r failingOwnerLookup) WithTx(*gorm.DB) repository.UserRepository { return r }

func (r failingOwnerLookup) GetByID(uint) (*models.User, error) { return nil, errOwnerLookupFailed }

func TestLeadOwnerLookupFailureIsServerErrorSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	e.router = e.buildRouter(failingOwnerLookup{e.users})
	lead := e.newLead(e.sales.ID)
	before := e.writes(lead.ID)

	assertInternal := func(status int, envelope utils.APIResponse) {
		t.Helper()
		require.Equal(t, http.StatusInternalServerError, status)
		require.NotNil(t, envelope.Error)
		assert.Equal(t, utils.ErrCodeInternal, envelope.Error.Code)
		assert.Equal(t, "An unexpected error occurred", envelope.Error.Message)
		assert.Equal(t, before, e.writes(lead.ID))
	}

	status, envelope := e.send(http.MethodPost, "/leads", e.admin, e.createBody(e.otherSales.ID))
	assertInternal(status, envelope)
	status, envelope = e.send(http.MethodPut, leadPath(lead.ID), e.admin, map[string]interface{}{"owner_id": e.otherSales.ID, "notes": "must not stick"})
	assertInternal(status, envelope)
	status, envelope = e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": e.otherSales.ID})
	assertInternal(status, envelope)
}

// --- AC-14: what a refusal logs ----------------------------------------------

func TestLeadOwnerRefusalLogsOmitPersonalData(t *testing.T) {
	e := newLeadOwnerEnv(t, false)

	var output bytes.Buffer
	capture := logrus.New()
	capture.SetOutput(&output)
	capture.SetFormatter(&logrus.JSONFormatter{})
	capture.SetLevel(logrus.DebugLevel)
	previous := utils.Logger
	utils.Logger = capture
	t.Cleanup(func() { utils.Logger = previous })

	type namedUser struct {
		id                     uint
		reason                 string
		email, first, lastName string
	}
	seed := func(role models.UserRole, reason string, depart func(*models.User)) namedUser {
		seq := leadOwnerNext()
		user := &models.User{
			Email:     fmt.Sprintf("canary-mail-%d@lead-owner.test", seq),
			Password:  "not-a-usable-hash",
			FirstName: fmt.Sprintf("CanaryFirst%d", seq),
			LastName:  fmt.Sprintf("CanaryLast%d", seq),
			Role:      role,
			IsActive:  true,
		}
		require.NoError(t, e.db.Create(user).Error)
		named := namedUser{user.ID, reason, user.Email, user.FirstName, user.LastName}
		if depart != nil {
			depart(user)
		}
		return named
	}
	named := []namedUser{
		seed(models.RoleSales, "inactive", e.deactivate),
		seed(models.RoleSupport, "role", nil),
		seed(models.RoleCustomer, "role", nil),
		seed(models.RoleSales, "unknown", e.erase),
		{id: leadOwnerUnknownID, reason: "unknown"},
	}

	for _, user := range named {
		lead := e.newLead(e.sales.ID)
		for _, tc := range []struct {
			route  string
			method string
			path   string
			body   map[string]interface{}
			leadID uint
		}{
			{"create", http.MethodPost, "/leads", e.createBody(user.id), 0},
			{"update", http.MethodPut, leadPath(lead.ID), map[string]interface{}{"owner_id": user.id}, lead.ID},
			{"assign", http.MethodPost, leadAssignPath(lead.ID), map[string]interface{}{"user_id": user.id}, lead.ID},
		} {
			output.Reset()
			status, _ := e.send(tc.method, tc.path, e.admin, tc.body)
			require.Equal(t, http.StatusBadRequest, status, tc.route)

			text := output.String()
			for _, personal := range []string{user.email, user.first, user.lastName} {
				if personal != "" {
					assert.NotContains(t, text, personal, "%s logged personal data", tc.route)
				}
			}

			wantError := fmt.Sprintf("lead owner %d refused (%s): invalid lead owner", user.id, user.reason)
			var serviceLine, handlerLine map[string]interface{}
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				var entry map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
				if entry["msg"] != "Lead owner refused" {
					continue
				}
				if _, isHandler := entry["handler"]; isHandler {
					handlerLine = entry
				} else {
					serviceLine = entry
				}
			}
			require.NotNil(t, serviceLine, "%s: the service logs the refusal", tc.route)
			require.NotNil(t, handlerLine, "%s: the handler logs the refusal", tc.route)

			assert.Equal(t, float64(user.id), serviceLine["owner_id"], tc.route)
			assert.Equal(t, user.reason, serviceLine["reason"], tc.route)
			assert.Equal(t, wantError, handlerLine["error"], tc.route)
			if tc.leadID != 0 {
				assert.Equal(t, float64(tc.leadID), serviceLine["lead_id"], tc.route)
				assert.Equal(t, float64(tc.leadID), handlerLine["lead_id"], tc.route)
			} else {
				assert.NotContains(t, serviceLine, "lead_id", tc.route)
				assert.NotContains(t, handlerLine, "lead_id", tc.route)
				assert.Equal(t, float64(user.id), handlerLine["owner_id"], tc.route)
			}
		}
	}
}

// --- AC-16: a converted lead can still be assigned ---------------------------

func TestLeadAssignConvertedLeadSQLite(t *testing.T) {
	e := newLeadOwnerEnv(t, false)
	lead := e.newLead(e.sales.ID)
	customer, err := e.leadService().ConvertToCustomer(context.Background(), lead.ID, &models.Customer{})
	require.NoError(t, err)

	status, envelope := e.send(http.MethodPost, leadAssignPath(lead.ID), e.admin, map[string]interface{}{"user_id": e.otherSales.ID})
	require.Equal(t, http.StatusOK, status, "%+v", envelope.Error)

	stored := e.committedLead(lead.ID)
	assert.Equal(t, e.otherSales.ID, stored.OwnerID)
	assert.Equal(t, models.LeadStatusConverted, stored.Status)
	require.NotNil(t, stored.CustomerID)
	assert.Equal(t, customer.ID, *stored.CustomerID)
}
