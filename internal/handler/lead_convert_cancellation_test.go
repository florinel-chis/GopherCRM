package handler

// Regression root for issue #107: POST /leads/:id/convert reads the lead for
// its ownership check before the service runs. That read is bound to the
// request too, so a client that goes away while the read waits for SQLite's
// single pooled connection releases the handler at once and nothing is
// converted.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

func TestConvertToCustomer_CancelledWhileTheOwnershipReadWaits(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)

	path := filepath.Join(t.TempDir(), "convert_cancellation.db")
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"),
		&gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}))

	admin := models.User{Email: "convert-admin@example.com", Password: "x", FirstName: "Ada", LastName: "Admin",
		Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	lead := models.Lead{FirstName: "Lee", LastName: "Prospect", Email: "lee@example.com",
		Status: models.LeadStatusQualified, OwnerID: admin.ID}
	require.NoError(t, db.Create(&lead).Error)

	leadService := service.NewLeadService(repository.NewLeadRepository(db), repository.NewCustomerRepository(db),
		repository.NewCompanyRepository(db), utils.NewTransactionManager(db))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", admin.ID)
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	router.POST("/leads/:id/convert", NewLeadHandler(leadService).ConvertToCustomer)

	// Take the only connection, then send the request.
	holder := db.Begin()
	require.NoError(t, holder.Error)
	require.NoError(t, holder.Exec("SELECT 1").Error)
	released := false
	release := func() {
		if !released {
			released = true
			_ = holder.Rollback().Error
		}
	}
	t.Cleanup(release)

	waitsBefore := sqlDB.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/leads/%d/convert", lead.ID),
		bytes.NewBufferString(`{"company_name":"Acme"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(done)
	}()

	if !assert.Eventually(t, func() bool { return sqlDB.Stats().WaitCount > waitsBefore },
		10*time.Second, 10*time.Millisecond, "the ownership read never queued for the connection") {
		cancel()
		release()
		<-done
		t.FailNow()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		release()
		<-done
		t.Fatal("the handler kept waiting for the connection after its request was cancelled")
	}
	release()

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var customers int64
	require.NoError(t, db.Model(&models.Customer{}).Count(&customers).Error)
	assert.Zero(t, customers)
	var stored models.Lead
	require.NoError(t, db.First(&stored, lead.ID).Error)
	assert.Equal(t, models.LeadStatusQualified, stored.Status)
}
