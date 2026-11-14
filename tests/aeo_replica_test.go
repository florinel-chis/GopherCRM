package tests

// AEO runs across API replicas over HTTP (issue #229): two routers, each with
// its own AEO service (own instance id, engine and connection pool) on one
// SQLite file, behind the real JWT middleware and role guard. While the first
// replica's run is running, POST /aeo/runs and POST /aeo/prompts/{id}/run on
// the second answer 409 CONFLICT with the unchanged envelope and insert nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const aeoReplicaAPISecret = "aeo-replica-api-regression-secret-32-bytes!"

const aeoReplicaAPITimeout = 15 * time.Second

// aeoReplicaGate is an in-process answer engine whose calls wait until the
// test opens the gate or the run is cancelled.
type aeoReplicaGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newAEOReplicaGate() *aeoReplicaGate {
	return &aeoReplicaGate{entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (g *aeoReplicaGate) Name() string  { return "gate" }
func (g *aeoReplicaGate) Model() string { return "gate-model" }

func (g *aeoReplicaGate) Query(ctx context.Context, prompt string) (aeo.ProviderAnswer, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
		return aeo.ProviderAnswer{Text: "Acme answers."}, nil
	case <-ctx.Done():
		return aeo.ProviderAnswer{}, ctx.Err()
	}
}

func (g *aeoReplicaGate) open() { g.once.Do(func() { close(g.release) }) }

func (g *aeoReplicaGate) awaitEntry(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(aeoReplicaAPITimeout):
		t.Fatal("no provider call reached the gate")
	}
}

type aeoReplicaAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type aeoReplicaEnvelope struct {
	Success bool                `json:"success"`
	Data    json.RawMessage     `json:"data"`
	Error   *aeoReplicaAPIError `json:"error"`
}

// aeoReplicaNode is one API replica: its own pool on the shared file, its own
// AEO runtime and router.
type aeoReplicaNode struct {
	db      *gorm.DB
	gate    *aeoReplicaGate
	runtime *service.AEORuntime
	router  *gin.Engine
	auth    service.AuthService
}

func openAEOReplicaAPIDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newAEOReplicaNode(t *testing.T, db *gorm.DB, instanceID string) *aeoReplicaNode {
	t.Helper()
	gate := newAEOReplicaGate()
	providers := []aeo.Provider{gate}
	repo := repository.NewAEORepository(db)
	engine := aeo.NewEngine(repo, providers, aeo.EngineOptions{CleanupTimeout: 2 * time.Second})
	runtime := service.NewAEORuntime(repo, engine, providers, utils.NewTransactionManager(db),
		service.WithAEOInstanceID(instanceID))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), aeoReplicaAPITimeout)
		defer cancel()
		_ = runtime.Shutdown(ctx)
	})
	t.Cleanup(gate.open)

	authService := service.NewAuthService(repository.NewUserRepository(db), repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: aeoReplicaAPISecret, ExpiryHours: 1})

	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.ErrorHandler())
	router.Use(gin.Recovery())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(authService))
	handler.SetupAEORoutes(protected, handler.NewAEOHandler(runtime.Service()))

	return &aeoReplicaNode{db: db, gate: gate, runtime: runtime, router: router, auth: authService}
}

func (n *aeoReplicaNode) post(t *testing.T, bearer, path string) (int, aeoReplicaEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	n.router.ServeHTTP(rec, req)
	var envelope aeoReplicaEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return rec.Code, envelope
}

func aeoReplicaRunCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&models.AEORun{}).Count(&count).Error)
	return count
}

func waitAEOReplicaRunDone(t *testing.T, db *gorm.DB, id uint) models.AEORun {
	t.Helper()
	var run models.AEORun
	require.Eventually(t, func() bool {
		if err := db.First(&run, id).Error; err != nil {
			return false
		}
		return run.Status != models.AEORunStatusRunning
	}, aeoReplicaAPITimeout, 5*time.Millisecond)
	return run
}

func TestAEOReplicaAPISQLite(t *testing.T) {
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", aeoReplicaAPISecret)
	gin.SetMode(gin.TestMode)

	path := filepath.Join(t.TempDir(), "aeo-replica-api.db")
	dbA := openAEOReplicaAPIDB(t, path)
	require.NoError(t, dbA.AutoMigrate(
		&models.User{}, &models.APIKey{},
		&models.AEOProfile{}, &models.AEOPrompt{}, &models.AEORun{}, &models.AEOAnswer{}, &models.AEOCitation{},
	))
	dbB := openAEOReplicaAPIDB(t, path)

	profile := &models.AEOProfile{BrandName: "Acme", OwnedDomains: []string{"acme.example"}}
	profile.ID = 1
	require.NoError(t, dbA.Create(profile).Error)
	prompt := models.AEOPrompt{Text: "Which CRM fits a small team?", IsActive: true}
	require.NoError(t, dbA.Create(&prompt).Error)

	a := newAEOReplicaNode(t, dbA, "replica-api-a")
	b := newAEOReplicaNode(t, dbB, "replica-api-b")

	admin := &models.User{Email: "admin@aeo-replica.test", FirstName: "Api", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, service.NewUserService(repository.NewUserRepository(dbA)).Register(admin, "password123"))
	bearer, err := a.auth.Login(admin.Email, "password123")
	require.NoError(t, err)

	// Replica A starts a run; it stays running at the gate.
	status, accepted := a.post(t, bearer, "/aeo/runs")
	require.Equal(t, http.StatusAccepted, status)
	require.True(t, accepted.Success)
	var run models.AEORun
	require.NoError(t, json.Unmarshal(accepted.Data, &run))
	require.NotZero(t, run.ID)
	a.gate.awaitEntry(t)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(accepted.Data, &raw))
	for _, key := range []string{"owner_instance", "heartbeat_at", "running_lock", "schedule_slot"} {
		assert.NotContains(t, raw, key, "the run JSON is unchanged")
	}

	// Replica B refuses both starts with the unchanged 409 envelope.
	for _, path := range []string{"/aeo/runs", fmt.Sprintf("/aeo/prompts/%d/run", prompt.ID)} {
		status, envelope := b.post(t, bearer, path)
		assert.Equal(t, http.StatusConflict, status, path)
		assert.False(t, envelope.Success, path)
		require.NotNil(t, envelope.Error, path)
		assert.Equal(t, "CONFLICT", envelope.Error.Code, path)
		assert.Equal(t, "an AEO run is already in progress", envelope.Error.Message, path)
	}
	assert.EqualValues(t, 1, aeoReplicaRunCount(t, dbB), "a refused start inserts no row")
	select {
	case <-b.gate.entered:
		t.Fatal("replica B must not query any engine")
	default:
	}

	// Once A's run has finished, B may start one.
	a.gate.open()
	finished := waitAEOReplicaRunDone(t, dbB, run.ID)
	assert.Equal(t, models.AEORunStatusCompleted, finished.Status)
	status, _ = b.post(t, bearer, "/aeo/runs")
	assert.Equal(t, http.StatusAccepted, status)
	b.gate.awaitEntry(t)
	assert.EqualValues(t, 2, aeoReplicaRunCount(t, dbA))
}
