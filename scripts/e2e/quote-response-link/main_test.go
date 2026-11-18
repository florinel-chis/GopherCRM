package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	testBaseURL = "http://localhost:15173"
	testSecret  = "quote-response-link-test-secret-0123456789"
)

// linkTestEnv is one shared in-memory SQLite database with the application
// schema. The test keeps its own handle open so the data outlives the handle
// run() opens and closes.
type linkTestEnv struct {
	t     *testing.T
	dsn   string
	db    *gorm.DB
	cfg   *config.Config
	owner *models.User
}

func openLinkTestHandle(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	return db
}

func newLinkTestEnv(t *testing.T) *linkTestEnv {
	t.Helper()
	// The environment run() reads through config.Load, as scripts/e2e/run.sh
	// pins it for a SQLite run.
	t.Setenv("JWT_SECRET", testSecret)
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("DB_DRIVER", config.DriverSQLite)
	t.Setenv("APP_BASE_URL", testBaseURL)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("E2E_UI_PORT", "15173")
	cfg, err := config.Load()
	require.NoError(t, err)
	if utils.Logger == nil {
		require.NoError(t, utils.InitLogger(&cfg.Logging))
	}
	utils.Logger.SetOutput(io.Discard)

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db := openLinkTestHandle(t, dsn)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, models.MigrateDatabaseWith(db))

	env := &linkTestEnv{t: t, dsn: dsn, db: db, cfg: cfg}
	env.owner = &models.User{Email: "owner@example.com", Password: "x", FirstName: "Quota", LastName: "Owner", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(env.owner).Error)
	return env
}

// open is the database opener handed to run(): a second handle on the same
// in-memory database.
func (e *linkTestEnv) open(*config.DatabaseConfig) (*gorm.DB, error) {
	return openLinkTestHandle(e.t, e.dsn), nil
}

// quote creates a quote with one line through the real service and, when
// asked, moves it to sent.
func (e *linkTestEnv) quote(sent bool) *models.Quote {
	e.t.Helper()
	customer := &models.Customer{FirstName: "Ada", LastName: "Client", Email: fmt.Sprintf("client-%t@example.com", sent)}
	require.NoError(e.t, e.db.Create(customer).Error)
	product := &models.Product{Name: "Widget", SKU: fmt.Sprintf("W-%t", sent), UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(e.t, e.db.Create(product).Error)

	svc := newQuoteService(e.db, e.cfg, &capturingMailer{})
	quote := &models.Quote{Title: "Engine licences", Currency: "EUR", CustomerID: &customer.ID, OwnerID: e.owner.ID}
	require.NoError(e.t, svc.Create(quote, e.owner.ID, models.RoleAdmin))
	_, err := svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: models.QuoteQuantityFromUnits(2)})
	require.NoError(e.t, err)
	if sent {
		_, err = svc.ChangeStatus(quote.ID, models.QuoteStatusSent, e.owner.ID)
		require.NoError(e.t, err)
	}
	return quote
}

func (e *linkTestEnv) tokenCount(quoteID uint) int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Model(&models.QuoteResponseToken{}).Where("quote_id = ?", quoteID).Count(&count).Error)
	return count
}

func TestQuoteResponseLinkPrintsUsableLink(t *testing.T) {
	env := newLinkTestEnv(t)
	quote := env.quote(true)

	var stdout bytes.Buffer
	require.NoError(t, run([]string{"-quote-id", fmt.Sprint(quote.ID)}, &stdout, env.open))

	// Stdout is exactly one line: the link on APP_BASE_URL.
	output := stdout.String()
	require.True(t, strings.HasSuffix(output, "\n"))
	require.Equal(t, 1, strings.Count(output, "\n"), "one line")
	link := strings.TrimSuffix(output, "\n")
	prefix := testBaseURL + responseLinkPath
	require.True(t, strings.HasPrefix(link, prefix), "the link is built on APP_BASE_URL")
	token := strings.TrimPrefix(link, prefix)
	require.NotEmpty(t, token)
	assert.NotContains(t, token, " ")

	// The token is the one the quote service answers, and it works once.
	svc := newQuoteService(env.db, env.cfg, &capturingMailer{})
	view, err := svc.GetPublicResponse(token)
	require.NoError(t, err)
	assert.Equal(t, models.QuoteNumber(quote.ID), view.Number)
	assert.Equal(t, models.QuoteStatusSent, view.Status)

	view, err = svc.RespondPublic(token, models.QuoteResponseAccept)
	require.NoError(t, err)
	assert.Equal(t, models.QuoteStatusAccepted, view.Status)
	_, err = svc.GetPublicResponse(token)
	assert.True(t, errors.Is(err, apperrors.ErrQuoteResponseRejected), "a spent link is dead: %v", err)
}

func TestQuoteResponseLinkRefusesDraftQuote(t *testing.T) {
	env := newLinkTestEnv(t)
	quote := env.quote(false)

	var stdout bytes.Buffer
	err := run([]string{"-quote-id", fmt.Sprint(quote.ID)}, &stdout, env.open)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrQuoteNotSent), "%v", err)
	assert.Empty(t, stdout.String(), "no link is printed")
	assert.Zero(t, env.tokenCount(quote.ID), "and none is stored")

	// A mail that carries no link is not a success either.
	assert.Empty(t, responseLink("Quote Q-1\nTotal 10.00 EUR\n", testBaseURL))
	assert.Empty(t, responseLink(testBaseURL+responseLinkPath+"\n", testBaseURL), "an empty token is no link")
}

func TestQuoteResponseLinkRefusesNonE2EDatabase(t *testing.T) {
	neverOpened := func(*config.DatabaseConfig) (*gorm.DB, error) {
		t.Fatal("the database was opened")
		return nil, nil
	}
	base := func(t *testing.T) {
		t.Setenv("JWT_SECRET", testSecret)
		t.Setenv("APP_BASE_URL", testBaseURL)
		t.Setenv("LOG_LEVEL", "error")
		t.Setenv("E2E_UI_PORT", "15173")
		t.Setenv("DB_DRIVER", config.DriverMySQL)
		t.Setenv("DB_NAME", "gocrm_e2e")
	}

	t.Run("mysql database not ending in _e2e", func(t *testing.T) {
		base(t)
		for _, name := range []string{"gophercrm", "gocrm_e2e_copy", "e2e"} {
			t.Setenv("DB_NAME", name)
			var stdout bytes.Buffer
			err := run([]string{"-quote-id", "1"}, &stdout, neverOpened)
			assert.ErrorIs(t, err, errNotE2EDatabase, name)
			assert.Empty(t, stdout.String())
		}
	})

	t.Run("outside the e2e runner", func(t *testing.T) {
		base(t)
		for _, driver := range []string{config.DriverMySQL, config.DriverSQLite} {
			t.Setenv("DB_DRIVER", driver)
			t.Setenv("E2E_UI_PORT", "")
			var stdout bytes.Buffer
			err := run([]string{"-quote-id", "1"}, &stdout, neverOpened)
			assert.ErrorIs(t, err, errNotE2ERun, driver)
			assert.Empty(t, stdout.String())
		}
	})

	t.Run("an e2e mysql database passes the guard", func(t *testing.T) {
		base(t)
		cfg, err := config.Load()
		require.NoError(t, err)
		assert.NoError(t, checkE2ETarget(&cfg.Database))
		opened := errors.New("opened")
		err = run([]string{"-quote-id", "1"}, &bytes.Buffer{}, func(*config.DatabaseConfig) (*gorm.DB, error) {
			return nil, opened
		})
		assert.ErrorIs(t, err, opened, "the guard lets the e2e database through to the connection")
	})

	t.Run("a quote id is required", func(t *testing.T) {
		base(t)
		assert.Error(t, run(nil, &bytes.Buffer{}, neverOpened))
	})
}
