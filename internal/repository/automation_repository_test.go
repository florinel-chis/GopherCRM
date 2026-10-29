package repository

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openAutomationRepoDB opens a private SQLite database or a uniquely named
// disposable schema on the TEST_MYSQL_DSN server, with the automation tables
// and the records they refer to. The MySQL root skips without the variable
// and fails when it names an unreachable server.
func openAutomationRepoDB(t *testing.T, useMySQL bool, maxConns int) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	var db *gorm.DB
	if !useMySQL {
		var err error
		db, err = gorm.Open(sqlite.Open(fmt.Sprintf("file:automation_repo_%d?mode=memory&cache=shared", time.Now().UnixNano())), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	} else {
		raw := os.Getenv("TEST_MYSQL_DSN")
		if raw == "" {
			t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
		}
		dsnCfg, err := mysqldriver.ParseDSN(raw)
		require.NoError(t, err)
		dsnCfg.DBName = ""
		dsnCfg.ParseTime = true
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_automation_repo_%d", time.Now().UnixNano())
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "`")
		require.NoError(t, err, "TEST_MYSQL_DSN is configured but the server is unavailable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(maxConns)
		sqlDB.SetMaxIdleConns(maxConns)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.Deal{}, &models.DealStageChange{},
		&models.Label{}, &models.Task{}, &models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{}, &models.TicketComment{},
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{}, &models.AutomationRun{},
		&models.AutomationRunStep{}, &models.Notification{},
	))
	return db
}

func seedAutomationUser(t *testing.T, db *gorm.DB, label string, role models.UserRole) *models.User {
	t.Helper()
	user := &models.User{Email: fmt.Sprintf("%s-%d@repo.test", label, time.Now().UnixNano()), Password: "x", FirstName: "U", LastName: label, Role: role, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedAutomationRule(t *testing.T, db *gorm.DB, entityType string) *models.AutomationRule {
	t.Helper()
	rule := &models.AutomationRule{Name: "Rule", EntityType: entityType, TriggerType: entityType + "_created", Enabled: true, Version: 1}
	require.NoError(t, NewAutomationRepository(db).CreateRule(rule))
	return rule
}

func seedAutomationRun(t *testing.T, db *gorm.DB, rule *models.AutomationRule, entityID uint, status string) *models.AutomationRun {
	t.Helper()
	run := &models.AutomationRun{RuleID: rule.ID, RuleVersion: rule.Version, EntityType: rule.EntityType, EntityID: entityID,
		DedupeKey: fmt.Sprintf("test:%d:%d:%d", rule.ID, entityID, time.Now().UnixNano()), Status: status, NextPosition: 1}
	inserted, err := NewAutomationRepository(db).CreateRunIgnoringDuplicate(run)
	require.NoError(t, err)
	require.True(t, inserted)
	return run
}

// AC-11 (repository half): the run list applies the role scope inside the
// SQL, before offset and limit — the admin scope returns every run, the
// sales scope only runs whose lead or deal the caller currently owns, with
// the total counting the same filtered set — and sorting is limited to the
// allowlist id, created_at, updated_at, status and resume_at.

func TestAutomationRunListScopeSQLite(t *testing.T) {
	runAutomationRunListScope(t, openAutomationRepoDB(t, false, 1))
}

func TestAutomationRunListScopeMySQL(t *testing.T) {
	runAutomationRunListScope(t, openAutomationRepoDB(t, true, 2))
}

func runAutomationRunListScope(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	sales := seedAutomationUser(t, db, "sales", models.RoleSales)
	other := seedAutomationUser(t, db, "other", models.RoleSales)
	leadRule := seedAutomationRule(t, db, models.AutomationEntityLead)
	dealRule := seedAutomationRule(t, db, models.AutomationEntityDeal)

	newLead := func(owner *models.User) *models.Lead {
		lead := &models.Lead{FirstName: "L", LastName: "D", Email: fmt.Sprintf("l-%d@repo.test", time.Now().UnixNano()), OwnerID: owner.ID}
		require.NoError(t, db.Create(lead).Error)
		return lead
	}
	newDeal := func(owner *models.User) *models.Deal {
		deal := &models.Deal{Title: "D", Stage: models.DealStageProposal, Currency: "EUR", Probability: 40, OwnerID: owner.ID}
		require.NoError(t, db.Create(deal).Error)
		return deal
	}
	myLead, theirLead := newLead(sales), newLead(other)
	myDeal, theirDeal := newDeal(sales), newDeal(other)
	reassigned := newLead(sales)
	erasedLead := newLead(sales)

	mine := []uint{
		seedAutomationRun(t, db, leadRule, myLead.ID, models.AutomationRunPending).ID,
		seedAutomationRun(t, db, leadRule, myLead.ID, models.AutomationRunCompleted).ID,
		seedAutomationRun(t, db, dealRule, myDeal.ID, models.AutomationRunWaiting).ID,
	}
	theirs := []uint{
		seedAutomationRun(t, db, leadRule, theirLead.ID, models.AutomationRunPending).ID,
		seedAutomationRun(t, db, dealRule, theirDeal.ID, models.AutomationRunPending).ID,
	}
	// A run on a lead that changed hands after the run started belongs to the
	// current owner, and a run on an erased lead to nobody.
	moved := seedAutomationRun(t, db, leadRule, reassigned.ID, models.AutomationRunPending).ID
	require.NoError(t, db.Model(&models.Lead{}).Where("id = ?", reassigned.ID).Update("owner_id", other.ID).Error)
	gone := seedAutomationRun(t, db, leadRule, erasedLead.ID, models.AutomationRunCancelled).ID
	require.NoError(t, db.Delete(&models.Lead{}, erasedLead.ID).Error)
	all := len(mine) + len(theirs) + 2

	ids := func(runs []models.AutomationRun) map[uint]bool {
		set := map[uint]bool{}
		for _, run := range runs {
			set[run.ID] = true
		}
		return set
	}

	t.Run("admin scope returns every run", func(t *testing.T) {
		runs, total, err := repo.ListRuns(0, 100, AutomationRunListFilter{}, AutomationRunScope{})
		require.NoError(t, err)
		assert.EqualValues(t, all, total)
		assert.Len(t, runs, all)
	})

	t.Run("sales scope returns the runs on the records the caller owns now", func(t *testing.T) {
		scope := AutomationRunScope{OwnerID: &sales.ID}
		runs, total, err := repo.ListRuns(0, 100, AutomationRunListFilter{}, scope)
		require.NoError(t, err)
		assert.EqualValues(t, len(mine), total, "the total counts the filtered set")
		got := ids(runs)
		for _, id := range mine {
			assert.Truef(t, got[id], "run %d is mine", id)
		}
		for _, id := range append(theirs, moved, gone) {
			assert.Falsef(t, got[id], "run %d is not mine", id)
		}

		otherScope := AutomationRunScope{OwnerID: &other.ID}
		runs, total, err = repo.ListRuns(0, 100, AutomationRunListFilter{}, otherScope)
		require.NoError(t, err)
		assert.EqualValues(t, len(theirs)+1, total)
		assert.True(t, ids(runs)[moved], "the reassigned lead's run follows the lead")

		// The scope applies before offset and limit: page one of the sales
		// scope holds one of the caller's runs and the total is unchanged.
		runs, total, err = repo.ListRuns(0, 1, AutomationRunListFilter{SortBy: "id", SortOrder: "asc"}, scope)
		require.NoError(t, err)
		assert.EqualValues(t, len(mine), total)
		require.Len(t, runs, 1)
		assert.Equal(t, mine[0], runs[0].ID)
		runs, _, err = repo.ListRuns(2, 1, AutomationRunListFilter{SortBy: "id", SortOrder: "asc"}, scope)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, mine[2], runs[0].ID)

		visible, err := repo.RunInScope(mine[0], scope)
		require.NoError(t, err)
		assert.True(t, visible)
		visible, err = repo.RunInScope(theirs[0], scope)
		require.NoError(t, err)
		assert.False(t, visible)
	})

	t.Run("filters combine with the scope", func(t *testing.T) {
		scope := AutomationRunScope{OwnerID: &sales.ID}
		runs, total, err := repo.ListRuns(0, 100, AutomationRunListFilter{Status: models.AutomationRunPending}, scope)
		require.NoError(t, err)
		assert.EqualValues(t, 1, total)
		require.Len(t, runs, 1)
		assert.Equal(t, mine[0], runs[0].ID)
		entityID := myDeal.ID
		runs, _, err = repo.ListRuns(0, 100, AutomationRunListFilter{EntityType: models.AutomationEntityDeal, EntityID: &entityID}, AutomationRunScope{})
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.Equal(t, mine[2], runs[0].ID)
	})

	t.Run("sorting is limited to the allowlist", func(t *testing.T) {
		for _, column := range []string{"id", "created_at", "updated_at", "status", "resume_at"} {
			for _, direction := range []string{"asc", "desc"} {
				runs, _, err := repo.ListRuns(0, 100, AutomationRunListFilter{SortBy: column, SortOrder: direction}, AutomationRunScope{})
				require.NoErrorf(t, err, "%s %s", column, direction)
				assert.Len(t, runs, all)
			}
		}
		runs, _, err := repo.ListRuns(0, 100, AutomationRunListFilter{SortBy: "id", SortOrder: "asc"}, AutomationRunScope{})
		require.NoError(t, err)
		for i := 1; i < len(runs); i++ {
			assert.Greater(t, runs[i].ID, runs[i-1].ID)
		}
		for _, column := range []string{"rule_id", "entity_id", "dedupe_key", "id; DROP TABLE automation_runs"} {
			_, _, err := repo.ListRuns(0, 100, AutomationRunListFilter{SortBy: column}, AutomationRunScope{})
			assert.Errorf(t, err, "%q must be refused", column)
		}
	})
}

// G1: every run read by the run API carries its rule's current name — a
// renamed rule its new name, a soft-deleted rule its last name flagged
// rule_deleted — read with one extra query over the page's distinct rule
// ids, whatever the scope, filter, sort and page.

func TestAutomationRunRuleNamesSQLite(t *testing.T) {
	runAutomationRunRuleNames(t, openAutomationRepoDB(t, false, 1))
}

func TestAutomationRunRuleNamesMySQL(t *testing.T) {
	runAutomationRunRuleNames(t, openAutomationRepoDB(t, true, 2))
}

func runAutomationRunRuleNames(t *testing.T, db *gorm.DB) {
	repo := NewAutomationRepository(db)
	sales := seedAutomationUser(t, db, "sales", models.RoleSales)
	renamed := seedAutomationRule(t, db, models.AutomationEntityLead)
	deleted := seedAutomationRule(t, db, models.AutomationEntityLead)
	kept := seedAutomationRule(t, db, models.AutomationEntityLead)
	require.NoError(t, db.Model(&models.AutomationRule{}).Where("id = ?", renamed.ID).Update("name", "Renamed rule").Error)
	require.NoError(t, db.Model(&models.AutomationRule{}).Where("id = ?", deleted.ID).Update("name", "Deleted rule").Error)
	require.NoError(t, db.Model(&models.AutomationRule{}).Where("id = ?", kept.ID).Update("name", "Kept rule").Error)
	require.NoError(t, repo.DeleteRule(deleted.ID))

	lead := &models.Lead{FirstName: "L", LastName: "N", Email: fmt.Sprintf("names-%d@repo.test", time.Now().UnixNano()), OwnerID: sales.ID}
	require.NoError(t, db.Create(lead).Error)
	want := map[uint]struct {
		name    string
		deleted bool
	}{}
	for i := 0; i < 20; i++ {
		rule := []*models.AutomationRule{renamed, deleted, kept}[i%3]
		run := seedAutomationRun(t, db, rule, lead.ID, models.AutomationRunCompleted)
		switch rule.ID {
		case renamed.ID:
			want[run.ID] = struct {
				name    string
				deleted bool
			}{"Renamed rule", false}
		case deleted.ID:
			want[run.ID] = struct {
				name    string
				deleted bool
			}{"Deleted rule", true}
		default:
			want[run.ID] = struct {
				name    string
				deleted bool
			}{"Kept rule", false}
		}
	}
	check := func(t *testing.T, runs []models.AutomationRun) {
		t.Helper()
		for _, run := range runs {
			assert.Equalf(t, want[run.ID].name, run.RuleName, "run %d", run.ID)
			assert.Equalf(t, want[run.ID].deleted, run.RuleDeleted, "run %d", run.ID)
			assert.Nilf(t, run.Rule, "run %d loads no rule association", run.ID)
		}
	}

	t.Run("every scope, filter, sort and page carries the names", func(t *testing.T) {
		scopes := []AutomationRunScope{{}, {OwnerID: &sales.ID}}
		for _, scope := range scopes {
			runs, total, err := repo.ListRuns(0, 100, AutomationRunListFilter{}, scope)
			require.NoError(t, err)
			assert.EqualValues(t, 20, total)
			require.Len(t, runs, 20)
			check(t, runs)
		}
		ruleID := deleted.ID
		runs, total, err := repo.ListRuns(0, 100, AutomationRunListFilter{RuleID: &ruleID}, AutomationRunScope{OwnerID: &sales.ID})
		require.NoError(t, err)
		assert.EqualValues(t, 7, total, "the deleted rule's runs are still listed by its id")
		check(t, runs)
		for _, column := range []string{"id", "created_at", "updated_at", "status", "resume_at"} {
			for _, direction := range []string{"asc", "desc"} {
				runs, total, err := repo.ListRuns(5, 5, AutomationRunListFilter{SortBy: column, SortOrder: direction}, AutomationRunScope{})
				require.NoErrorf(t, err, "%s %s", column, direction)
				assert.EqualValues(t, 20, total)
				require.Len(t, runs, 5)
				check(t, runs)
			}
		}
	})

	t.Run("the detail carries the name of a live and of a deleted rule", func(t *testing.T) {
		for id := range want {
			run, err := repo.GetRunByID(id)
			require.NoError(t, err)
			assert.Equal(t, want[id].name, run.RuleName)
			assert.Equal(t, want[id].deleted, run.RuleDeleted)
		}
	})

	t.Run("a rule row that is gone leaves the name empty and flags it deleted", func(t *testing.T) {
		gone := seedAutomationRule(t, db, models.AutomationEntityLead)
		run := seedAutomationRun(t, db, gone, lead.ID, models.AutomationRunCompleted)
		require.NoError(t, db.Unscoped().Delete(&models.AutomationRule{}, gone.ID).Error)
		got, err := repo.GetRunByID(run.ID)
		require.NoError(t, err)
		assert.Empty(t, got.RuleName)
		assert.True(t, got.RuleDeleted)
		require.NoError(t, db.Delete(&models.AutomationRun{}, run.ID).Error)
	})

	t.Run("a page costs one count, one page query and one name query", func(t *testing.T) {
		var statements int
		count := func(*gorm.DB) { statements++ }
		require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:count_run_names_query", count))
		require.NoError(t, db.Callback().Row().After("gorm:row").Register("test:count_run_names_row", count))
		defer func() {
			_ = db.Callback().Query().Remove("test:count_run_names_query")
			_ = db.Callback().Row().Remove("test:count_run_names_row")
		}()
		runs, _, err := repo.ListRuns(0, 20, AutomationRunListFilter{}, AutomationRunScope{})
		require.NoError(t, err)
		require.Len(t, runs, 20)
		assert.Equal(t, 3, statements, "no query per run")
	})
}

func TestAutomationRepositoryConditionalUpdates(t *testing.T) {
	db := openAutomationRepoDB(t, false, 1)
	repo := NewAutomationRepository(db)
	rule := seedAutomationRule(t, db, models.AutomationEntityLead)
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)

	event := &models.AutomationEvent{EntityType: "lead", EntityID: 1, EventType: "lead_created", Origin: "user"}
	require.NoError(t, repo.RecordEvent(event))
	rows, err := repo.ClaimEvent(event.ID, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.ClaimEvent(event.ID, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "a claimed event is not claimed again")

	run := seedAutomationRun(t, db, rule, 1, models.AutomationRunPending)
	rows, err = repo.AdvanceRun(run.ID, 1, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 2}, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "a pending run cannot be advanced")
	rows, err = repo.ClaimRun(run.ID, models.AutomationRunPending, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.AdvanceRun(run.ID, 2, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 3}, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "the wrong position matches nothing")
	resumeAt := now.Add(time.Hour)
	rows, err = repo.AdvanceRun(run.ID, 1, AutomationRunAdvance{Status: models.AutomationRunWaiting, NextPosition: 2, ResumeAt: &resumeAt}, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.ClaimRun(run.ID, models.AutomationRunWaiting, now.Add(30*time.Minute))
	require.NoError(t, err)
	assert.Zero(t, rows, "a waiting run is not claimed before resume_at")
	due, err := repo.ListClaimableRuns(now.Add(30*time.Minute), 10)
	require.NoError(t, err)
	assert.Empty(t, due)
	due, err = repo.ListClaimableRuns(now.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Len(t, due, 1)
	rows, err = repo.ClaimRun(run.ID, models.AutomationRunWaiting, now.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.CancelRun(run.ID, models.AutomationCancelManual, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.AdvanceRun(run.ID, 2, AutomationRunAdvance{Status: models.AutomationRunRunning, NextPosition: 3}, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "a cancelled run cannot be advanced")
	rows, err = repo.CancelRun(run.ID, models.AutomationCancelManual, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "a cancelled run is not cancelled again")

	passOn := "2026-10-28"
	rows, err = repo.AdvanceScheduleCursor(rule.ID, nil, 0, passOn, 500, nil, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.AdvanceScheduleCursor(rule.ID, nil, 0, passOn, 1000, nil, now)
	require.NoError(t, err)
	assert.Zero(t, rows, "a stale (pass_on, cursor) pair matches nothing")
	rows, err = repo.AdvanceScheduleCursor(rule.ID, &passOn, 500, passOn, 700, &passOn, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	stored, err := repo.GetRuleByID(rule.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 700, stored.ScheduleCursor)
	require.NotNil(t, stored.ScheduleCompletedOn)
	assert.Equal(t, passOn, *stored.ScheduleCompletedOn)
}
