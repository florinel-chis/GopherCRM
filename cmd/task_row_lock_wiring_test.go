package main

// Issue #558 through the router production uses: setupDependencies builds the
// task service WithTaskRowLock(txManager), so PUT /tasks/{id} writes inside a
// transaction that reads the task row with a locking read first, and the bulk
// task status batch reads its tasks with a locking read (GetTasksByIDs). The
// SQLite dialector keeps the locking clause on the statement and only drops
// its SQL, so a query callback sees it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
)

func TestTaskRowLockProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)

	admin := &models.User{Email: "admin@task-row-lock-wiring.test", Password: "x", FirstName: "Ada",
		LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	task := &models.Task{Title: "Wiring task", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium,
		AssignedToID: admin.ID}
	require.NoError(t, models.DB.Create(task).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	// Locked reads of the tasks table, recorded only while a request runs.
	var mu sync.Mutex
	recording := false
	lockedTaskReads := 0
	const callback = "test:task_row_lock_wiring"
	require.NoError(t, models.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "tasks" {
			return
		}
		if _, locking := tx.Statement.Clauses["FOR"]; !locking {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if recording {
			lockedTaskReads++
		}
	}))
	t.Cleanup(func() { _ = models.DB.Callback().Query().Remove(callback) })

	token, err := service.NewAuthService(repository.NewUserRepository(models.DB),
		repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT).GenerateJWT(admin)
	require.NoError(t, err)

	serve := func(method, path string, body interface{}) (*httptest.ResponseRecorder, int) {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mu.Lock()
		recording, lockedTaskReads = true, 0
		mu.Unlock()
		router.ServeHTTP(recorder, request)
		mu.Lock()
		defer mu.Unlock()
		recording = false
		return recorder, lockedTaskReads
	}

	recorder, locked := serve(http.MethodPut, fmt.Sprintf("/tasks/%d", task.ID), map[string]interface{}{"status": "in_progress"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.GreaterOrEqual(t, locked, 1, "PUT /tasks/{id} reads the task row with a locking read")

	recorder, locked = serve(http.MethodPost, "/tasks/bulk/status", map[string]interface{}{"task_ids": []uint{task.ID}, "status": "completed"})
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.GreaterOrEqual(t, locked, 1, "POST /tasks/bulk/status reads the tasks with a locking read")

	var stored models.Task
	require.NoError(t, models.DB.First(&stored, task.ID).Error)
	assert.Equal(t, models.TaskStatusCompleted, stored.Status)
}
