package service

// The deal events (#515): deal.created, deal.updated and deal.deleted are
// written by every deal write path (DealService Create, Update, ChangeStage
// and Delete, and the automation assign_owner step on a deal run) inside the
// writing transaction, one pending row per active subscribed endpoint, on
// SQLite and on a disposable TEST_MYSQL_DSN schema (the MySQL roots skip
// without the variable and fail when it is unreachable).
//
// The MySQL schema is opened with loc=Local, as production opens it
// (internal/config): expected_close_date is a DATE stored as local midnight,
// and under the driver's default loc=UTC it would not round-trip, so an
// update that sends the stored values would look like a change.

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDealWebhookFanOutSQLite(t *testing.T) {
	runDealWebhookFanOut(t, openTicketIntakeDB(t, false))
}

func TestDealWebhookFanOutMySQL(t *testing.T) {
	runDealWebhookFanOut(t, openDealWebhookMySQLDB(t))
}

func TestDealWebhookAtomicitySQLite(t *testing.T) {
	runDealWebhookAtomicity(t, openTicketIntakeDB(t, false))
}

func TestDealWebhookAtomicityMySQL(t *testing.T) {
	runDealWebhookAtomicity(t, openDealWebhookMySQLDB(t))
}

var webhookDealTypes = []string{"deal.created", "deal.updated", "deal.deleted"}

// openDealWebhookMySQLDB is the MySQL branch of openTicketIntakeDB with
// loc=Local, the location production's DSN uses.
func openDealWebhookMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	dsnCfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	dsnCfg.DBName = ""
	dsnCfg.ParseTime = true
	dsnCfg.Loc = time.Local
	dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	server, err := sql.Open("mysql", dsnCfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_deal_webhooks_%d_%d", time.Now().UnixNano(), ticketIntakeSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	dsnCfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)
	return db
}

// storedWebhookDeal is a fresh read of the row, soft-deleted included.
func storedWebhookDeal(t *testing.T, db *gorm.DB, id uint) models.Deal {
	t.Helper()
	var deal models.Deal
	require.NoError(t, db.Unscoped().First(&deal, id).Error)
	return deal
}

// dealCloseDate is a calendar date as the handler parses expected_close_date.
func dealCloseDate(t *testing.T, value string) *time.Time {
	t.Helper()
	date, err := models.ParseDealDate(value)
	require.NoError(t, err)
	return &date
}

// seedDeal writes a deal directly (no webhook, no automation event).
func (e *webhookEnv) seedDeal(stage models.DealStage) *models.Deal {
	e.t.Helper()
	deal := &models.Deal{Title: "Seeded deal", Stage: stage, AmountCents: 5000, Currency: "EUR",
		Probability: stage.DefaultProbability(), OwnerID: e.owner.ID}
	require.NoError(e.t, e.db.Create(deal).Error)
	require.NoError(e.t, e.db.Create(&models.DealStageChange{DealID: deal.ID, ToStage: stage, ChangedByID: e.owner.ID,
		ChangedAt: time.Now().UTC()}).Error)
	return deal
}

// dealAsHandlerSends is the deal PUT /deals/{id} hands the service for a body
// that repeats the stored values: the service's read without associations,
// with expected_close_date re-parsed from its YYYY-MM-DD form.
func dealAsHandlerSends(t *testing.T, env *webhookEnv, id uint) *models.Deal {
	t.Helper()
	deal, err := env.deals.GetByID(id)
	require.NoError(t, err)
	deal.Owner, deal.Company, deal.Customer, deal.Lead = nil, nil, nil, nil
	if deal.ExpectedCloseDate != nil {
		deal.ExpectedCloseDate = dealCloseDate(t, *models.FormatDealDate(deal.ExpectedCloseDate))
	}
	return deal
}

func runDealWebhookFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	admin := env.owner.ID
	all := webhook.AllEventTypes()
	require.Len(t, all, 12)
	var others []string
	for _, eventType := range all {
		if eventType[:5] != "deal." {
			others = append(others, eventType)
		}
	}
	require.Len(t, others, 9)
	// a: everything. b: the deal types only. c: every type but the deal
	// ones. d: inactive with everything.
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, webhookDealTypes...)
	c := env.endpoint("https://example.com/c", true, others...)
	d := env.endpoint("https://example.com/d", false, all...)
	noRowFor := func(t *testing.T, endpoints ...uint) {
		t.Helper()
		for _, id := range endpoints {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", id), "endpoint %d", id)
		}
	}

	// A won deal: closed_at and expected_close_date are both set.
	deal := &models.Deal{Title: "Webhook deal", Stage: models.DealStageWon, AmountCents: 125000, Currency: "EUR",
		ExpectedCloseDate: dealCloseDate(t, "2026-12-31"), Notes: "first", OwnerID: admin}

	t.Run("Create writes deal.created for each subscribed active endpoint", func(t *testing.T) {
		env.clearDeliveries()
		require.NoError(t, env.deals.Create(deal, nil, admin))
		require.NotZero(t, deal.ID)
		assertFanOut(t, env, "deal.created", deal.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, c.ID, d.ID)
		stored := storedWebhookDeal(t, db, deal.ID)
		require.NotNil(t, stored.ClosedAt)
		require.NotNil(t, stored.ExpectedCloseDate)
	})

	t.Run("an Update that stores the same values writes no row", func(t *testing.T) {
		env.clearDeliveries()
		same := dealAsHandlerSends(t, env, deal.ID)
		require.NotNil(t, same.ClosedAt)
		require.Equal(t, "2026-12-31", *models.FormatDealDate(same.ExpectedCloseDate))
		require.NoError(t, env.deals.Update(same, nil, false, admin))
		assert.Zero(t, env.allDeliveries(), "the stored values are no update")
		assert.EqualValues(t, 1, env.count(&models.AutomationEvent{}, "entity_type = ? AND entity_id = ? AND event_type = ?",
			models.AutomationEntityDeal, deal.ID, models.AutomationEventDealUpdated), "the automation event is still recorded")
	})

	t.Run("an Update that changes notes writes deal.updated", func(t *testing.T) {
		env.clearDeliveries()
		changed := dealAsHandlerSends(t, env, deal.ID)
		changed.Notes = "second"
		require.NoError(t, env.deals.Update(changed, nil, false, admin))
		assert.Equal(t, "second", storedWebhookDeal(t, db, deal.ID).Notes)
		assertFanOut(t, env, "deal.updated", deal.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, c.ID, d.ID)
	})

	t.Run("an Update to another stage writes deal.updated", func(t *testing.T) {
		env.clearDeliveries()
		moved := dealAsHandlerSends(t, env, deal.ID)
		moved.Stage = models.DealStageNegotiation
		require.NoError(t, env.deals.Update(moved, nil, false, admin))
		stored := storedWebhookDeal(t, db, deal.ID)
		assert.Equal(t, models.DealStageNegotiation, stored.Stage)
		assert.Nil(t, stored.ClosedAt, "leaving won clears closed_at")
		assertFanOut(t, env, "deal.updated", deal.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("ChangeStage writes deal.updated", func(t *testing.T) {
		env.clearDeliveries()
		_, err := env.deals.ChangeStage(deal.ID, models.DealStageProposal, nil, nil, admin)
		require.NoError(t, err)
		assert.Equal(t, models.DealStageProposal, storedWebhookDeal(t, db, deal.ID).Stage)
		assertFanOut(t, env, "deal.updated", deal.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())

		env.clearDeliveries()
		_, err = env.deals.ChangeStage(deal.ID, models.DealStageProposal, nil, nil, admin)
		require.NoError(t, err)
		assert.Zero(t, env.allDeliveries(), "the same stage again writes nothing")
	})

	t.Run("the automation assign_owner step on a deal run writes deal.updated", func(t *testing.T) {
		// The service writes above recorded automation events; drop them so
		// the dispatch below starts only the run of the seeded deal.
		require.NoError(t, db.Where("1 = 1").Delete(&models.AutomationEvent{}).Error)
		auto := newAutomationEnv(t, db)
		auto.rebuildWith(WithAutomationWebhooks(webhook.NewEmitter(env.repo)))
		rule := auto.createRule(models.AutomationEntityDeal, models.AutomationEventDealCreated, models.AutomationTrigger{}, assignStep(auto.other.ID))
		seeded := env.seedDeal(models.DealStageQualification)
		require.NotEqual(t, auto.other.ID, seeded.OwnerID)
		runOnce := func() {
			t.Helper()
			run := auto.startEntityRun(rule, models.AutomationEntityDeal, seeded.ID)
			auto.executeDue()
			assert.Equal(t, models.AutomationRunCompleted, auto.run(run.ID).Status)
			steps := auto.stepsOf(run.ID)
			require.Len(t, steps, 1)
			assert.Equal(t, models.AutomationOutcomeSucceeded, steps[0].Outcome)
		}

		env.clearDeliveries()
		runOnce()
		assert.Equal(t, auto.other.ID, storedWebhookDeal(t, db, seeded.ID).OwnerID)
		assertFanOut(t, env, "deal.updated", seeded.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries())

		env.clearDeliveries()
		runOnce()
		assert.Zero(t, env.allDeliveries(), "the owner the deal already has is no update")
	})

	t.Run("Delete writes deal.deleted for every subscribed endpoint", func(t *testing.T) {
		env.clearDeliveries()
		// e never received an event for the deal; it gets the deleted one.
		e := env.endpoint("https://example.com/e", true, "deal.deleted")
		require.NoError(t, env.deals.Delete(deal.ID))
		assert.True(t, storedWebhookDeal(t, db, deal.ID).DeletedAt.Valid, "soft-deleted")
		assertFanOut(t, env, "deal.deleted", deal.ID, a.ID, b.ID, e.ID)
		assert.Equal(t, int64(3), env.allDeliveries())
		noRowFor(t, c.ID, d.ID)
	})

	t.Run("an unwired deal service writes no row", func(t *testing.T) {
		env.clearDeliveries()
		plain := newWebhookEnv(t, db, nil)
		fresh := &models.Deal{Title: "Unwired deal", Currency: "EUR", Notes: "x", OwnerID: plain.owner.ID}
		require.NoError(t, plain.deals.Create(fresh, nil, plain.owner.ID))
		changed, err := plain.deals.GetByID(fresh.ID)
		require.NoError(t, err)
		changed.Owner = nil
		changed.Notes = "y"
		require.NoError(t, plain.deals.Update(changed, nil, false, plain.owner.ID))
		_, err = plain.deals.ChangeStage(fresh.ID, models.DealStageWon, nil, nil, plain.owner.ID)
		require.NoError(t, err)
		require.NoError(t, plain.deals.Delete(fresh.ID))
		assert.True(t, storedWebhookDeal(t, db, fresh.ID).DeletedAt.Valid)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

// runDealWebhookAtomicity: an outbox insert that fails rolls the deal write
// back with it.
func runDealWebhookAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	all := webhook.AllEventTypes()
	env.endpoint("https://example.com/hook", true, all...)
	env.endpoint("https://example.com/hook/second", true, all...)
	admin := env.owner.ID
	env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))
	history := func(dealID uint) int64 {
		return env.count(&models.DealStageChange{}, "deal_id = ?", dealID)
	}

	t.Run("Create", func(t *testing.T) {
		deals := env.count(&models.Deal{}, "")
		changes := env.count(&models.DealStageChange{}, "")
		deal := &models.Deal{Title: "Lost deal", Currency: "EUR", OwnerID: admin}
		require.ErrorIs(t, env.deals.Create(deal, nil, admin), injectedFailure)
		assert.Equal(t, deals, env.count(&models.Deal{}, ""), "no deal row")
		assert.Equal(t, changes, env.count(&models.DealStageChange{}, ""), "no stage-history row")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("Update", func(t *testing.T) {
		deal := env.seedDeal(models.DealStageQualification)
		before := storedWebhookDeal(t, db, deal.ID)
		changed := dealAsHandlerSends(t, env, deal.ID)
		changed.Notes = "lost"
		changed.Stage = models.DealStageProposal
		require.ErrorIs(t, env.deals.Update(changed, nil, false, admin), injectedFailure)
		after := storedWebhookDeal(t, db, deal.ID)
		assert.Equal(t, before.WebhookState(), after.WebhookState(), "the deal rolled back")
		assert.Equal(t, int64(1), history(deal.ID), "no stage-history row")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("ChangeStage", func(t *testing.T) {
		deal := env.seedDeal(models.DealStageQualification)
		before := storedWebhookDeal(t, db, deal.ID)
		_, err := env.deals.ChangeStage(deal.ID, models.DealStageWon, nil, nil, admin)
		require.ErrorIs(t, err, injectedFailure)
		after := storedWebhookDeal(t, db, deal.ID)
		assert.Equal(t, before.WebhookState(), after.WebhookState(), "the deal rolled back")
		assert.Equal(t, int64(1), history(deal.ID), "no stage-history row")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Delete", func(t *testing.T) {
		deal := env.seedDeal(models.DealStageQualification)
		require.ErrorIs(t, env.deals.Delete(deal.ID), injectedFailure)
		assert.False(t, storedWebhookDeal(t, db, deal.ID).DeletedAt.Valid, "the deal stays live")
		assert.Zero(t, env.allDeliveries())
	})
}
