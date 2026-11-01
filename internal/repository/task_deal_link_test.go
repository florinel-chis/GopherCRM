package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// openTaskDealRepoDB opens a real database WITH foreign keys, which is what the
// link's refusal of a dangling id rests on: a file-backed SQLite database with
// enforcement on, or a uniquely named disposable schema on the TEST_MYSQL_DSN
// server. The MySQL root skips without the variable and fails when it names an
// unreachable server.
func openTaskDealRepoDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "task_deal.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
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
		name := fmt.Sprintf("gocrm_task_deal_%d", time.Now().UnixNano())
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
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{}, &models.Deal{}, &models.DealStageChange{},
		&models.Label{}, &models.Task{}, &models.Ticket{}, &models.TicketComment{},
		&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{},
		&models.Form{}, &models.FormSubmission{}, &models.FormConfirmationToken{},
		&models.AutomationRule{}, &models.AutomationStep{}, &models.AutomationEvent{}, &models.AutomationRun{},
		&models.AutomationRunStep{}, &models.Notification{},
	))
	return db
}

func seedTaskDealUser(t *testing.T, db *gorm.DB, label string) *models.User {
	t.Helper()
	user := &models.User{Email: fmt.Sprintf("%s-%d@task-deal.test", label, time.Now().UnixNano()), Password: "x", FirstName: "U", LastName: label, Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedTaskDealDeal(t *testing.T, db *gorm.DB, owner *models.User, customerID, leadID *uint) *models.Deal {
	t.Helper()
	deal := &models.Deal{Title: "Deal", Stage: models.DealStageProposal, Currency: "EUR", Probability: 40, OwnerID: owner.ID, CustomerID: customerID, LeadID: leadID}
	require.NoError(t, db.Create(deal).Error)
	return deal
}

func seedTaskDealTask(t *testing.T, db *gorm.DB, assignee *models.User, dealID *uint) *models.Task {
	t.Helper()
	task := &models.Task{Title: "Task", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: assignee.ID, DealID: dealID}
	require.NoError(t, db.Create(task).Error)
	return task
}

// committedTaskDealID is the stored link, as a raw column read: a NULL has to be
// on disk, not merely on the struct.
func committedTaskDealID(t *testing.T, db *gorm.DB, taskID uint) *uint {
	t.Helper()
	var value sql.NullInt64
	require.NoError(t, db.Raw("SELECT deal_id FROM tasks WHERE id = ?", taskID).Row().Scan(&value))
	if !value.Valid {
		return nil
	}
	id := uint(value.Int64)
	return &id
}

func TestTaskDealLinkRepositorySQLite(t *testing.T) {
	runTaskDealLinkRepository(t, openTaskDealRepoDB(t, false))
}

func TestTaskDealLinkRepositoryMySQL(t *testing.T) {
	runTaskDealLinkRepository(t, openTaskDealRepoDB(t, true))
}

func runTaskDealLinkRepository(t *testing.T, db *gorm.DB) {
	repo := NewTaskRepository(db)
	alice := seedTaskDealUser(t, db, "alice")
	bob := seedTaskDealUser(t, db, "bob")
	deal := seedTaskDealDeal(t, db, alice, nil, nil)
	other := seedTaskDealDeal(t, db, bob, nil, nil)

	t.Run("the update path stores and clears deal_id", func(t *testing.T) {
		task := seedTaskDealTask(t, db, alice, nil)
		assert.Nil(t, committedTaskDealID(t, db, task.ID))

		task.DealID = &deal.ID
		require.NoError(t, repo.Update(task))
		stored := committedTaskDealID(t, db, task.ID)
		require.NotNil(t, stored)
		assert.Equal(t, deal.ID, *stored)

		task.DealID = nil
		require.NoError(t, repo.Update(task))
		assert.Nil(t, committedTaskDealID(t, db, task.ID), "Update writes a NULL")

		task.DealID = &other.ID
		require.NoError(t, repo.UpdateWithLabels(task, nil))
		stored = committedTaskDealID(t, db, task.ID)
		require.NotNil(t, stored)
		assert.Equal(t, other.ID, *stored)

		task.DealID = nil
		require.NoError(t, repo.UpdateWithLabels(task, nil))
		assert.Nil(t, committedTaskDealID(t, db, task.ID), "UpdateWithLabels writes a NULL")
	})

	t.Run("every task read loads a live deal", func(t *testing.T) {
		mine := seedTaskDealUser(t, db, "reader")
		task := seedTaskDealTask(t, db, mine, &deal.ID)

		got, err := repo.GetByID(task.ID)
		require.NoError(t, err)
		require.NotNil(t, got.Deal)
		assert.Equal(t, deal.ID, got.Deal.ID)

		page, err := repo.GetByAssignedToID(mine.ID, 0, 10)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.NotNil(t, page[0].Deal, "GetByAssignedToID preloads the deal")
	})

	t.Run("a task read does not load a soft-deleted deal but keeps deal_id", func(t *testing.T) {
		mine := seedTaskDealUser(t, db, "orphan")
		gone := seedTaskDealDeal(t, db, mine, nil, nil)
		task := seedTaskDealTask(t, db, mine, &gone.ID)
		require.NoError(t, db.Delete(&models.Deal{}, gone.ID).Error)

		got, err := repo.GetByID(task.ID)
		require.NoError(t, err)
		assert.Nil(t, got.Deal)
		require.NotNil(t, got.DealID)
		assert.Equal(t, gone.ID, *got.DealID)

		page, err := repo.GetByAssignedToID(mine.ID, 0, 10)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Nil(t, page[0].Deal)
		require.NotNil(t, page[0].DealID)
		assert.Equal(t, gone.ID, *page[0].DealID)
	})

	t.Run("ListByDeal and CountByDeal", func(t *testing.T) {
		listed := seedTaskDealDeal(t, db, alice, nil, nil)

		// Bob's rows come first, so narrowing after the limit would lose Alice's.
		var everyID, aliceIDs []uint
		for i := 0; i < 3; i++ {
			everyID = append(everyID, seedTaskDealTask(t, db, bob, &listed.ID).ID)
		}
		for i := 0; i < 4; i++ {
			id := seedTaskDealTask(t, db, alice, &listed.ID).ID
			everyID = append(everyID, id)
			aliceIDs = append(aliceIDs, id)
		}
		seedTaskDealTask(t, db, alice, &other.ID)
		seedTaskDealTask(t, db, alice, nil)
		deleted := seedTaskDealTask(t, db, alice, &listed.ID)
		require.NoError(t, repo.Delete(deleted.ID))

		ids := func(tasks []models.Task) []uint {
			out := []uint{}
			for _, task := range tasks {
				out = append(out, task.ID)
			}
			return out
		}
		walk := func(assignee *uint, sortBy, sortOrder string) []uint {
			out := []uint{}
			for offset := 0; ; offset += 2 {
				page, err := repo.ListByDeal(listed.ID, assignee, offset, 2, sortBy, sortOrder, "Deal")
				require.NoError(t, err)
				if len(page) == 0 {
					return out
				}
				for _, task := range page {
					require.NotNil(t, task.Deal, "the requested preload is applied")
				}
				out = append(out, ids(page)...)
			}
		}
		reversed := func(in []uint) []uint {
			out := make([]uint, 0, len(in))
			for i := len(in) - 1; i >= 0; i-- {
				out = append(out, in[i])
			}
			return out
		}

		total, err := repo.CountByDeal(listed.ID, nil)
		require.NoError(t, err)
		assert.EqualValues(t, len(everyID), total, "soft-deleted tasks and other deals are not counted")
		total, err = repo.CountByDeal(listed.ID, &alice.ID)
		require.NoError(t, err)
		assert.EqualValues(t, len(aliceIDs), total)

		assert.Equal(t, everyID, walk(nil, "", ""), "no sort is ascending id")

		// Every row ties on status, so only the trailing id orders the pages.
		assert.Equal(t, everyID, walk(nil, "status", "asc"), "a tied column ends in the id")
		assert.Equal(t, reversed(everyID), walk(nil, "status", "desc"), "in the same direction")

		assert.Equal(t, aliceIDs, walk(&alice.ID, "", ""), "the assignee narrows every page")
		first, err := repo.ListByDeal(listed.ID, &alice.ID, 0, 2, "", "")
		require.NoError(t, err)
		assert.Equal(t, aliceIDs[:2], ids(first), "the assignee narrows inside the query, before the limit")

		for _, column := range []string{"deal_id", "description", "id; DROP TABLE tasks"} {
			_, err := repo.ListByDeal(listed.ID, nil, 0, 2, column, "asc")
			assert.Error(t, err, "%q is outside the tasks allowlist", column)
		}
	})

	t.Run("a deal_id naming no deal row is refused by the foreign key", func(t *testing.T) {
		missing := uint(999999)
		before := int64(0)
		require.NoError(t, db.Model(&models.Task{}).Count(&before).Error)

		err := repo.Create(&models.Task{Title: "Dangling", Status: models.TaskStatusPending, Priority: models.TaskPriorityMedium, AssignedToID: alice.ID, DealID: &missing})
		require.Error(t, err)
		assert.True(t, strings.Contains(strings.ToLower(err.Error()), "foreign key"), "got %v", err)

		after := int64(0)
		require.NoError(t, db.Model(&models.Task{}).Count(&after).Error)
		assert.Equal(t, before, after)

		task := seedTaskDealTask(t, db, alice, nil)
		task.DealID = &missing
		require.Error(t, repo.Update(task), "the update path is refused as well")
		assert.Nil(t, committedTaskDealID(t, db, task.ID))
	})
}

// TestTaskDealLinkSurvivesErasure: erasing the person behind a linked deal
// scrubs the person as before. The task, its deal_id and the deal row are
// business records and stay.
func TestTaskDealLinkSurvivesErasure(t *testing.T) {
	db := openTaskDealRepoDB(t, false)
	owner := seedTaskDealUser(t, db, "owner")

	assertSurvives := func(t *testing.T, deal *models.Deal, task *models.Task) {
		t.Helper()
		stored := committedTaskDealID(t, db, task.ID)
		require.NotNil(t, stored, "the task keeps its deal_id")
		assert.Equal(t, deal.ID, *stored)

		var live models.Deal
		require.NoError(t, db.First(&live, deal.ID).Error, "the deal stays live")
		var liveTask models.Task
		require.NoError(t, db.First(&liveTask, task.ID).Error, "the task stays live")

		loaded, err := NewTaskRepository(db).GetByID(task.ID)
		require.NoError(t, err)
		require.NotNil(t, loaded.Deal, "the link still resolves")
		assert.Equal(t, deal.ID, loaded.Deal.ID)
	}

	t.Run("a lead", func(t *testing.T) {
		const email = "erased-lead@task-deal.test"
		lead := &models.Lead{FirstName: "Erased", LastName: "Lead", Email: email, Status: models.LeadStatusNew, OwnerID: owner.ID}
		require.NoError(t, db.Create(lead).Error)
		deal := seedTaskDealDeal(t, db, owner, nil, &lead.ID)
		task := seedTaskDealTask(t, db, owner, &deal.ID)

		require.NoError(t, NewLeadRepository(db).Delete(lead.ID))

		var erased models.Lead
		require.NoError(t, db.Unscoped().First(&erased, lead.ID).Error)
		assert.True(t, erased.DeletedAt.Valid, "the lead is erased")
		assert.NotEqual(t, email, erased.Email, "the address is scrubbed")
		assert.Empty(t, erased.FirstName, "the name is scrubbed")
		assertSurvives(t, deal, task)
	})

	t.Run("a customer", func(t *testing.T) {
		const email = "erased-customer@task-deal.test"
		customer := &models.Customer{FirstName: "Erased", LastName: "Customer", Email: email}
		require.NoError(t, db.Create(customer).Error)
		deal := seedTaskDealDeal(t, db, owner, &customer.ID, nil)
		task := seedTaskDealTask(t, db, owner, &deal.ID)

		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))

		var erased models.Customer
		require.NoError(t, db.Unscoped().First(&erased, customer.ID).Error)
		assert.True(t, erased.DeletedAt.Valid, "the customer is erased")
		assert.NotEqual(t, email, erased.Email, "the address is scrubbed")
		assert.Empty(t, erased.FirstName, "the name is scrubbed")
		assertSurvives(t, deal, task)
	})
}
