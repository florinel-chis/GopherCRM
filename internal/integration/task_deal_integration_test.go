package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// These suites drive the task-to-deal link (#240) through the real routes,
// services and repositories on SQLite and, when TEST_MYSQL_DSN is set, on a
// disposable MySQL schema. The deal's title and notes are canaries: the title
// may only reach a caller the deal is visible to, the notes nobody.

const (
	taskDealTitle = "deal-title-canary"
	taskDealNotes = "deal-notes-canary"
)

func TestTaskDealReadRoutesSQLite(t *testing.T) { runTaskDealReadRoutes(t, false) }
func TestTaskDealReadRoutesMySQL(t *testing.T)  { runTaskDealReadRoutes(t, true) }

func TestDealTasksEndpointSQLite(t *testing.T) { runDealTasksEndpoint(t, false) }
func TestDealTasksEndpointMySQL(t *testing.T)  { runDealTasksEndpoint(t, true) }

func TestTaskDealLinkIntegrationSQLite(t *testing.T) { runTaskDealLinkIntegration(t, false) }
func TestTaskDealLinkIntegrationMySQL(t *testing.T)  { runTaskDealLinkIntegration(t, true) }

// --- environment ----------------------------------------------------------------

type taskDealEnv struct {
	db     *gorm.DB
	router *gin.Engine

	admin    *models.User
	owner    *models.User // sales, owns the deals the suites link
	other    *models.User // sales, owns none of them
	support  *models.User
	customer *models.User
}

func newTaskDealEnv(t *testing.T, useMySQL bool) *taskDealEnv {
	t.Helper()
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	db := openDashboardDB(t, useMySQL)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
		&models.Deal{}, &models.DealStageChange{}, &models.Ticket{}, &models.Label{}, &models.Task{}))

	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	companyRepo := repository.NewCompanyRepository(db)
	dealRepo := repository.NewDealRepository(db)
	txManager := utils.NewTransactionManager(db)
	leadService := service.NewLeadService(leadRepo, customerRepo, companyRepo, txManager)
	customerService := service.NewCustomerService(customerRepo, userRepo, companyRepo)
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo)
	taskService := service.NewTaskService(repository.NewTaskRepository(db), userRepo, leadRepo, customerRepo, repository.NewLabelRepository(db), dealRepo)
	dealService := service.NewDealService(dealRepo, companyRepo, customerRepo, leadRepo, userRepo, nil, txManager)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && c.Errors[0].Type == gin.ErrorTypeBind {
			utils.RespondValidationError(c, c.Errors[0].Error())
		}
	})
	// Stands in for the auth middleware: the caller is a stored user, and its id
	// and role go into the context exactly as middleware.Auth puts them.
	router.Use(func(c *gin.Context) {
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
	group := router.Group("")
	handler.SetupTaskRoutes(group, handler.NewTaskHandler(taskService))
	handler.SetupDealRoutes(group, handler.NewDealHandler(dealService))
	handler.SetupDashboardRoutes(group, handler.NewDashboardHandler(leadService, customerService, ticketService, taskService, dealService))

	env := &taskDealEnv{db: db, router: router}
	newUser := func(label string, role models.UserRole) *models.User {
		user := &models.User{Email: "td-" + label + "@example.com", Password: "x", FirstName: "F", LastName: label, Role: role, IsActive: true}
		require.NoError(t, db.Create(user).Error)
		return user
	}
	env.admin = newUser("admin", models.RoleAdmin)
	env.owner = newUser("owner", models.RoleSales)
	env.other = newUser("other", models.RoleSales)
	env.support = newUser("support", models.RoleSupport)
	env.customer = newUser("customer", models.RoleCustomer)
	return env
}

func (e *taskDealEnv) deal(t *testing.T, owner *models.User, stage models.DealStage) *models.Deal {
	t.Helper()
	deal := &models.Deal{Title: taskDealTitle, Notes: taskDealNotes, Stage: stage, AmountCents: 1250000, Currency: "EUR", Probability: stage.DefaultProbability(), OwnerID: owner.ID}
	require.NoError(t, e.db.Create(deal).Error)
	return deal
}

// task inserts a task directly: no rule of the service decides a read fixture.
func (e *taskDealEnv) task(t *testing.T, assignee *models.User, title string, dealID *uint, due *time.Time) *models.Task {
	t.Helper()
	task := &models.Task{Title: title, Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: assignee.ID, DealID: dealID, DueDate: due}
	require.NoError(t, e.db.Create(task).Error)
	return task
}

func (e *taskDealEnv) setOwner(t *testing.T, deal *models.Deal, owner *models.User) {
	t.Helper()
	require.NoError(t, e.db.Model(&models.Deal{}).Where("id = ?", deal.ID).Update("owner_id", owner.ID).Error)
}

// storedDealID is the committed link of a task, soft-deleted deals included.
func (e *taskDealEnv) storedDealID(t *testing.T, taskID uint) *uint {
	t.Helper()
	var task models.Task
	require.NoError(t, e.db.First(&task, taskID).Error)
	return task.DealID
}

// taskDealEnvelope is the response, decoded for the assertions.
type taskDealEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta *struct {
		Page       int   `json:"page"`
		PerPage    int   `json:"per_page"`
		Total      int64 `json:"total"`
		TotalPages int64 `json:"total_pages"`
	} `json:"meta"`
}

func (e *taskDealEnv) call(t *testing.T, as *models.User, method, path string, body interface{}) (int, taskDealEnvelope, string) {
	t.Helper()
	reader := bytes.NewBuffer(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewBuffer(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", strconv.FormatUint(uint64(as.ID), 10))
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var envelope taskDealEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	return w.Code, envelope, w.Body.String()
}

// listedTasks decodes the tasks of a {tasks,total} payload or of a bare array.
func listedTasks(t *testing.T, data json.RawMessage) []map[string]interface{} {
	t.Helper()
	var page struct {
		Tasks []map[string]interface{} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &page); err == nil && page.Tasks != nil {
		return page.Tasks
	}
	var array []map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &array), string(data))
	return array
}

func findTask(tasks []map[string]interface{}, id uint) map[string]interface{} {
	for _, task := range tasks {
		if task["id"] == float64(id) {
			return task
		}
	}
	return nil
}

// assertLinkShown holds a task object to deal_id and exactly the three summary
// fields of the deal.
func assertLinkShown(t *testing.T, task map[string]interface{}, deal *models.Deal, context string) {
	t.Helper()
	assert.Equal(t, float64(deal.ID), task["deal_id"], context)
	assert.Equal(t, map[string]interface{}{
		"id":    float64(deal.ID),
		"title": deal.Title,
		"stage": string(deal.Stage),
	}, task["deal"], context)
}

func assertLinkHidden(t *testing.T, task map[string]interface{}, context string) {
	t.Helper()
	assert.NotContains(t, task, "deal_id", context)
	assert.NotContains(t, task, "deal", context)
}

// --- every read route -------------------------------------------------------------

func runTaskDealReadRoutes(t *testing.T, useMySQL bool) {
	e := newTaskDealEnv(t, useMySQL)
	deal := e.deal(t, e.owner, models.DealStageProposal)
	label := &models.Label{Name: "Deal work", Color: "#112233"}
	require.NoError(t, e.db.Create(label).Error)

	due := time.Now().Add(48 * time.Hour)
	own := map[*models.User]*models.Task{}
	for _, user := range []*models.User{e.admin, e.owner, e.other, e.support, e.customer} {
		task := e.task(t, user, "Linked task of "+user.LastName, &deal.ID, &due)
		require.NoError(t, e.db.Model(task).Association("Labels").Append(label))
		own[user] = task
	}

	byID := func(user *models.User) string { return fmt.Sprintf("/tasks/%d", own[user].ID) }
	labelFilter := fmt.Sprintf("/tasks?label_id=%d", label.ID)

	cases := []struct {
		name    string
		user    *models.User
		visible bool
	}{
		{"admin", e.admin, true},
		{"sales owner", e.owner, true},
		{"another sales user", e.other, false},
		{"support", e.support, false},
		{"customer", e.customer, false},
	}
	for _, tc := range cases {
		task := own[tc.user]
		check := func(t *testing.T, context, body string, object map[string]interface{}) {
			t.Helper()
			require.NotNil(t, object, "%s: the caller's own task must be in the answer: %s", context, body)
			if tc.visible {
				assertLinkShown(t, object, deal, context)
			} else {
				assertLinkHidden(t, object, context)
				assert.NotContains(t, body, taskDealTitle, context)
				assert.NotContains(t, body, `"deal_id"`, context)
				assert.NotContains(t, body, `"deal":`, context)
			}
			assert.NotContains(t, body, taskDealNotes, context)
			assert.NotContains(t, body, "amount_cents", context)
		}

		lists := []string{labelFilter, "/tasks", "/tasks/my", "/tasks/upcoming"}
		if tc.user == e.admin {
			lists = append(lists, "/tasks?search=Linked", "/tasks?sort_by=title&sort_order=asc")
		}
		if tc.user != e.customer {
			lists = append(lists, "/dashboard/upcoming-tasks?limit=50")
		}
		for _, path := range lists {
			context := tc.name + " GET " + path
			status, envelope, body := e.call(t, tc.user, http.MethodGet, path, nil)
			require.Equal(t, http.StatusOK, status, "%s: %s", context, body)
			check(t, context, body, findTask(listedTasks(t, envelope.Data), task.ID))
		}

		single := func(method string, payload interface{}) {
			context := tc.name + " " + method + " " + byID(tc.user)
			status, envelope, body := e.call(t, tc.user, method, byID(tc.user), payload)
			require.Equal(t, http.StatusOK, status, "%s: %s", context, body)
			var object map[string]interface{}
			require.NoError(t, json.Unmarshal(envelope.Data, &object), body)
			check(t, context, body, object)
		}
		single(http.MethodGet, nil)
		single(http.MethodPut, gin.H{"description": "Edited by " + tc.name})
	}

	// A write that never named deal_id kept every link, for whoever made it.
	for user, task := range own {
		stored := e.storedDealID(t, task.ID)
		require.NotNil(t, stored, user.LastName)
		assert.Equal(t, deal.ID, *stored, user.LastName)
	}

	status, _, body := e.call(t, e.customer, http.MethodGet, "/dashboard/upcoming-tasks", nil)
	assert.Equal(t, http.StatusForbidden, status, "customer on the dashboard")
	assert.NotContains(t, body, taskDealTitle)

	// POST carries the summary to the two callers the deal is visible to.
	for _, tc := range []struct {
		name string
		user *models.User
	}{{"admin", e.admin}, {"sales owner", e.owner}} {
		status, envelope, body := e.call(t, tc.user, http.MethodPost, "/tasks",
			gin.H{"title": "Created by " + tc.name, "assigned_to_id": tc.user.ID, "deal_id": deal.ID})
		require.Equal(t, http.StatusCreated, status, "%s: %s", tc.name, body)
		var object map[string]interface{}
		require.NoError(t, json.Unmarshal(envelope.Data, &object), body)
		assertLinkShown(t, object, deal, "POST by "+tc.name)
		assert.NotContains(t, body, taskDealNotes)
	}

	before := int64(0)
	require.NoError(t, e.db.Model(&models.Task{}).Count(&before).Error)
	status, _, _ = e.call(t, e.customer, http.MethodPost, "/tasks",
		gin.H{"title": "Refused", "assigned_to_id": e.customer.ID, "deal_id": deal.ID})
	assert.Equal(t, http.StatusForbidden, status, "customer POST")

	// A deal hidden from the caller answers like a missing one.
	for _, user := range []*models.User{e.other, e.support} {
		for _, dealID := range []uint{deal.ID, 999999} {
			status, envelope, body := e.call(t, user, http.MethodPost, "/tasks",
				gin.H{"title": "Refused", "assigned_to_id": user.ID, "deal_id": dealID})
			assert.Equal(t, http.StatusNotFound, status, body)
			require.NotNil(t, envelope.Error, body)
			assert.Equal(t, "NOT_FOUND", envelope.Error.Code)
			assert.NotContains(t, body, taskDealTitle)
		}
	}
	after := int64(0)
	require.NoError(t, e.db.Model(&models.Task{}).Count(&after).Error)
	assert.Equal(t, before, after, "a refused POST writes nothing")
}

// --- GET /deals/{id}/tasks --------------------------------------------------------

func runDealTasksEndpoint(t *testing.T, useMySQL bool) {
	e := newTaskDealEnv(t, useMySQL)
	deal := e.deal(t, e.owner, models.DealStageProposal)
	otherDeal := e.deal(t, e.other, models.DealStageProposal)

	// The other user's rows come first, so narrowing after the limit would show
	// the owner too few of its own.
	var every, ownersTasks []uint
	for i := 0; i < 3; i++ {
		every = append(every, e.task(t, e.other, fmt.Sprintf("Other %d", i), &deal.ID, nil).ID)
	}
	for i := 0; i < 3; i++ {
		id := e.task(t, e.owner, fmt.Sprintf("Owner %d", i), &deal.ID, nil).ID
		every = append(every, id)
		ownersTasks = append(ownersTasks, id)
	}
	e.task(t, e.owner, "On another deal", &otherDeal.ID, nil)
	e.task(t, e.owner, "Unlinked", nil, nil)
	deleted := e.task(t, e.owner, "Deleted", &deal.ID, nil)
	require.NoError(t, e.db.Delete(&models.Task{}, deleted.ID).Error)

	path := fmt.Sprintf("/deals/%d/tasks", deal.ID)

	idsOf := func(tasks []map[string]interface{}) []uint {
		ids := []uint{}
		for _, task := range tasks {
			ids = append(ids, uint(task["id"].(float64)))
		}
		return ids
	}
	page := func(t *testing.T, as *models.User, target string) (taskDealEnvelope, []map[string]interface{}, int64) {
		t.Helper()
		status, envelope, body := e.call(t, as, http.MethodGet, target, nil)
		require.Equal(t, http.StatusOK, status, "%s: %s", target, body)
		assert.True(t, envelope.Success, body)
		var data struct {
			Tasks []map[string]interface{} `json:"tasks"`
			Total *int64                   `json:"total"`
		}
		require.NoError(t, json.Unmarshal(envelope.Data, &data), body)
		require.NotNil(t, data.Tasks, "tasks is an array, never null: %s", body)
		require.NotNil(t, data.Total, "the envelope carries total: %s", body)
		assert.NotContains(t, body, taskDealNotes)
		return envelope, data.Tasks, *data.Total
	}

	t.Run("an admin gets every live task of the deal", func(t *testing.T) {
		envelope, tasks, total := page(t, e.admin, path)
		assert.Equal(t, every, idsOf(tasks))
		assert.EqualValues(t, len(every), total)
		require.NotNil(t, envelope.Meta)
		assert.EqualValues(t, len(every), envelope.Meta.Total)
		assert.Equal(t, 1, envelope.Meta.Page)
		assert.Equal(t, 20, envelope.Meta.PerPage)
		assert.EqualValues(t, 1, envelope.Meta.TotalPages)
		for _, task := range tasks {
			assertLinkShown(t, task, deal, "admin")
		}
	})

	t.Run("the pages walk a total order", func(t *testing.T) {
		envelope, first, total := page(t, e.admin, path+"?limit=4&sort_by=status&sort_order=asc")
		assert.EqualValues(t, len(every), total)
		require.NotNil(t, envelope.Meta)
		assert.EqualValues(t, 2, envelope.Meta.TotalPages)
		envelope, second, _ := page(t, e.admin, path+"?limit=4&page=2&sort_by=status&sort_order=asc")
		assert.Equal(t, 2, envelope.Meta.Page)
		assert.Equal(t, every, append(idsOf(first), idsOf(second)...), "status ties on every row; the id orders them")
	})

	t.Run("the sales owner gets only the tasks assigned to it", func(t *testing.T) {
		envelope, tasks, total := page(t, e.owner, path)
		assert.Equal(t, ownersTasks, idsOf(tasks))
		assert.EqualValues(t, len(ownersTasks), total, "total counts only those")
		require.NotNil(t, envelope.Meta)
		assert.EqualValues(t, len(ownersTasks), envelope.Meta.Total)
		for _, task := range tasks {
			assertLinkShown(t, task, deal, "owner")
		}

		_, firstPage, totalOfPage := page(t, e.owner, path+"?limit=2")
		assert.Equal(t, ownersTasks[:2], idsOf(firstPage), "narrowed before the limit")
		assert.EqualValues(t, len(ownersTasks), totalOfPage)
	})

	t.Run("a sales owner with no task on its deal gets an empty page", func(t *testing.T) {
		empty := e.deal(t, e.owner, models.DealStageWon)
		e.task(t, e.other, "Not the owner's", &empty.ID, nil)
		_, tasks, total := page(t, e.owner, fmt.Sprintf("/deals/%d/tasks", empty.ID))
		assert.Empty(t, tasks)
		assert.Zero(t, total)
	})

	t.Run("a closed deal lists like an open one", func(t *testing.T) {
		won := e.deal(t, e.owner, models.DealStageWon)
		id := e.task(t, e.owner, "On a won deal", &won.ID, nil).ID
		_, tasks, _ := page(t, e.owner, fmt.Sprintf("/deals/%d/tasks", won.ID))
		assert.Equal(t, []uint{id}, idsOf(tasks))
	})

	t.Run("refusals", func(t *testing.T) {
		gone := e.deal(t, e.owner, models.DealStageProposal)
		e.task(t, e.owner, "On a deleted deal", &gone.ID, nil)
		require.NoError(t, e.db.Delete(&models.Deal{}, gone.ID).Error)

		for _, tc := range []struct {
			name   string
			user   *models.User
			target string
			status int
			code   string
		}{
			{"another sales user's deal", e.other, path, http.StatusForbidden, "FORBIDDEN"},
			{"support", e.support, path, http.StatusForbidden, "FORBIDDEN"},
			{"customer", e.customer, path, http.StatusForbidden, "FORBIDDEN"},
			{"a missing deal, admin", e.admin, "/deals/999999/tasks", http.StatusNotFound, "NOT_FOUND"},
			{"a missing deal, sales", e.owner, "/deals/999999/tasks", http.StatusNotFound, "NOT_FOUND"},
			{"a soft-deleted deal, admin", e.admin, fmt.Sprintf("/deals/%d/tasks", gone.ID), http.StatusNotFound, "NOT_FOUND"},
			{"a soft-deleted deal, owner", e.owner, fmt.Sprintf("/deals/%d/tasks", gone.ID), http.StatusNotFound, "NOT_FOUND"},
			{"a bad id", e.admin, "/deals/abc/tasks", http.StatusBadRequest, "BAD_REQUEST"},
			{"a sort column outside the allowlist", e.admin, path + "?sort_by=description", http.StatusBadRequest, "BAD_REQUEST"},
			{"an injected sort column", e.admin, path + "?sort_by=id%3BDROP", http.StatusBadRequest, "BAD_REQUEST"},
		} {
			status, envelope, body := e.call(t, tc.user, http.MethodGet, tc.target, nil)
			assert.Equal(t, tc.status, status, "%s: %s", tc.name, body)
			require.NotNil(t, envelope.Error, "%s: %s", tc.name, body)
			assert.Equal(t, tc.code, envelope.Error.Code, tc.name)
			assert.NotContains(t, body, taskDealTitle, tc.name)
		}
	})
}

// --- the whole story --------------------------------------------------------------

func runTaskDealLinkIntegration(t *testing.T, useMySQL bool) {
	e := newTaskDealEnv(t, useMySQL)
	deal := e.deal(t, e.owner, models.DealStageProposal)

	readObject := func(t *testing.T, as *models.User, method, path string, payload interface{}) map[string]interface{} {
		t.Helper()
		status, envelope, body := e.call(t, as, method, path, payload)
		require.Equal(t, http.StatusOK, status, "%s %s: %s", method, path, body)
		var object map[string]interface{}
		require.NoError(t, json.Unmarshal(envelope.Data, &object), body)
		return object
	}
	keysOf := func(object map[string]interface{}) []string {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys
	}

	status, envelope, body := e.call(t, e.owner, http.MethodPost, "/tasks", gin.H{"title": "Own task", "assigned_to_id": e.owner.ID})
	require.Equal(t, http.StatusCreated, status, body)
	var created struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal(envelope.Data, &created), body)
	assertLinkHidden(t, readObject(t, e.owner, http.MethodGet, fmt.Sprintf("/tasks/%d", created.ID), nil), "not linked yet")
	taskPath := fmt.Sprintf("/tasks/%d", created.ID)

	// A sales user links its own deal to its own task.
	assertLinkShown(t, readObject(t, e.owner, http.MethodPut, taskPath, gin.H{"deal_id": deal.ID}), deal, "the PUT answer")
	assertLinkShown(t, readObject(t, e.owner, http.MethodGet, taskPath, nil), deal, "the next read")

	// The link stops showing after the deal changes owner, and the stored link
	// does not change; it shows again when the deal returns.
	e.setOwner(t, deal, e.other)
	assertLinkHidden(t, readObject(t, e.owner, http.MethodGet, taskPath, nil), "after the deal changed owner")
	stored := e.storedDealID(t, created.ID)
	require.NotNil(t, stored)
	assert.Equal(t, deal.ID, *stored)
	assertLinkShown(t, readObject(t, e.admin, http.MethodGet, taskPath, nil), deal, "an admin keeps seeing it")

	e.setOwner(t, deal, e.owner)
	assertLinkShown(t, readObject(t, e.owner, http.MethodGet, taskPath, nil), deal, "after the deal returned")

	// A clear of a link hidden from the caller answers like a clear of an
	// unlinked task, and severs nothing.
	e.setOwner(t, deal, e.other)
	hiddenClear := readObject(t, e.owner, http.MethodPut, taskPath, gin.H{"deal_id": 0})
	assertLinkHidden(t, hiddenClear, "a clear of a hidden link")
	stored = e.storedDealID(t, created.ID)
	require.NotNil(t, stored, "the stored link is unchanged")
	assert.Equal(t, deal.ID, *stored)

	unlinked := e.task(t, e.owner, "Unlinked", nil, nil)
	plainClear := readObject(t, e.owner, http.MethodPut, fmt.Sprintf("/tasks/%d", unlinked.ID), gin.H{"deal_id": 0})
	assert.Equal(t, keysOf(plainClear), keysOf(hiddenClear), "the two clears are indistinguishable by their keys")

	// A sent id of the hidden deal is refused, also when it is the stored link.
	status, envelope, body = e.call(t, e.owner, http.MethodPut, taskPath, gin.H{"deal_id": deal.ID})
	assert.Equal(t, http.StatusNotFound, status, body)
	require.NotNil(t, envelope.Error, body)
	assert.Equal(t, "NOT_FOUND", envelope.Error.Code)

	// A visible clear does what it says.
	e.setOwner(t, deal, e.owner)
	assertLinkHidden(t, readObject(t, e.owner, http.MethodPut, taskPath, gin.H{"deal_id": 0}), "a clear of a visible link")
	assert.Nil(t, e.storedDealID(t, created.ID), "a NULL is committed")
	assertLinkShown(t, readObject(t, e.owner, http.MethodPut, taskPath, gin.H{"deal_id": deal.ID}), deal, "linked again")

	// After the deal is soft-deleted the task and its stored deal_id remain. An
	// admin receives the id without the object; a sales user receives neither.
	status, _, body = e.call(t, e.admin, http.MethodDelete, fmt.Sprintf("/deals/%d", deal.ID), nil)
	require.Equal(t, http.StatusNoContent, status, body)

	stored = e.storedDealID(t, created.ID)
	require.NotNil(t, stored, "the task keeps its deal_id")
	assert.Equal(t, deal.ID, *stored)

	forAdmin := readObject(t, e.admin, http.MethodGet, taskPath, nil)
	assert.Equal(t, float64(deal.ID), forAdmin["deal_id"], "an admin keeps the id")
	assert.NotContains(t, forAdmin, "deal", "and gets no object for a deal that is gone")

	assertLinkHidden(t, readObject(t, e.owner, http.MethodGet, taskPath, nil), "sales after the deal was deleted")
}
