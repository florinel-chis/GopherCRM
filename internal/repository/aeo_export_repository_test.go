package repository

// Regression roots for the AEO export reads (#244): ListAnswersForExport,
// ListCitationsByAnswerIDs, ListRunsByIDs and ListPromptTextsByIDs on a
// file-backed SQLite database and, when TEST_MYSQL_DSN is set, on a disposable
// MySQL schema. The MySQL root skips without the variable and fails when the
// server is unreachable.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

func openAEOExportDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard, NowFunc: func() time.Time { return time.Now().UTC() }}
	var db *gorm.DB
	if !useMySQL {
		var err error
		dsn := filepath.Join(t.TempDir(), "aeo_export.db") +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
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
		dsnCfg.Loc = time.UTC
		dsnCfg.Timeout, dsnCfg.ReadTimeout, dsnCfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
		server, err := sql.Open("mysql", dsnCfg.FormatDSN())
		require.NoError(t, err)
		name := fmt.Sprintf("gocrm_aeo_export_%d", time.Now().UnixNano())
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
		sqlDB.SetMaxOpenConns(4)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&models.AEOPrompt{}, &models.AEORun{}, &models.AEOAnswer{}, &models.AEOCitation{}))
	return db
}

func TestAEOExportRepositorySQLite(t *testing.T) {
	runAEOExportRepositoryChecks(t, openAEOExportDB(t, false))
}

func TestAEOExportRepositoryMySQL(t *testing.T) {
	runAEOExportRepositoryChecks(t, openAEOExportDB(t, true))
}

func runAEOExportRepositoryChecks(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := &aeoRepository{db: db}

	// Count the SELECT statements the export reads issue.
	var statements int64
	require.NoError(t, db.Callback().Query().After("gorm:query").
		Register("test:aeo_export_statements", func(*gorm.DB) { atomic.AddInt64(&statements, 1) }))
	countStatements := func(read func()) int64 {
		before := atomic.LoadInt64(&statements)
		read()
		return atomic.LoadInt64(&statements) - before
	}

	// Prompts: one live, one soft-deleted.
	livePrompt := models.AEOPrompt{Text: "Live prompt text", IsActive: true}
	require.NoError(t, db.Create(&livePrompt).Error)
	deletedPrompt := models.AEOPrompt{Text: "Deleted prompt text", IsActive: true}
	require.NoError(t, db.Create(&deletedPrompt).Error)
	require.NoError(t, db.Delete(&deletedPrompt).Error)

	// Runs: two live, one soft-deleted.
	base := time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)
	newRun := func(trigger string) models.AEORun {
		run := models.AEORun{Trigger: trigger, Status: models.AEORunStatusCompleted, StartedAt: base.Add(-time.Hour), TotalQueries: 3}
		require.NoError(t, db.Create(&run).Error)
		return run
	}
	runOne := newRun(models.AEOTriggerManual)
	runTwo := newRun(models.AEOTriggerScheduled)
	runGone := newRun(models.AEOTriggerManual)
	require.NoError(t, db.Delete(&runGone).Error)

	newAnswer := func(runID, promptID uint, createdAt time.Time, mentions map[string]int) models.AEOAnswer {
		answer := models.AEOAnswer{
			RunID:              runID,
			PromptID:           promptID,
			Provider:           "anthropic",
			Model:              "model-a",
			AnswerText:         "text",
			FirstMentionPos:    -1,
			CompetitorMentions: mentions,
		}
		answer.CreatedAt = createdAt
		require.NoError(t, db.Omit("Citations").Create(&answer).Error)
		return answer
	}
	from, to := base, base.Add(24*time.Hour)
	// Inserted out of created_at order so id order and time order differ. a2
	// ties a1 on created_at; a5 sits exactly at to (excluded from the window);
	// a3 sits exactly at from (included) and a6 ties it; a7 is soft-deleted.
	a1 := newAnswer(runOne.ID, livePrompt.ID, base.Add(time.Hour), map[string]int{"Globex": 2})
	a2 := newAnswer(runOne.ID, deletedPrompt.ID, base.Add(time.Hour), nil)
	a5 := newAnswer(runOne.ID, livePrompt.ID, to, nil)
	a4 := newAnswer(runOne.ID, livePrompt.ID, from.Add(-time.Second), nil)
	a3 := newAnswer(runTwo.ID, livePrompt.ID, from, nil)
	a6 := newAnswer(runOne.ID, livePrompt.ID, from, nil)
	a7 := newAnswer(runOne.ID, livePrompt.ID, base.Add(2*time.Hour), nil)
	require.NoError(t, db.Delete(&a7).Error)

	newCitation := func(answerID uint, domain string) models.AEOCitation {
		citation := models.AEOCitation{AnswerID: answerID, URL: "https://" + domain + "/page", Domain: domain}
		require.NoError(t, db.Create(&citation).Error)
		return citation
	}
	// cY has the lowest citation id but belongs to the later answer.
	cY := newCitation(a2.ID, "y.example")
	cX1 := newCitation(a1.ID, "x1.example")
	cX2 := newCitation(a1.ID, "x2.example")
	cGone := newCitation(a3.ID, "gone.example")
	require.NoError(t, db.Delete(&cGone).Error)

	ids := func(answers []models.AEOAnswer) []uint {
		out := make([]uint, 0, len(answers))
		for _, answer := range answers {
			out = append(out, answer.ID)
		}
		return out
	}

	t.Run("window answers", func(t *testing.T) {
		answers, err := repo.ListAnswersForExport(ctx, AEOExportFilter{From: from, To: to}, 100)
		require.NoError(t, err)
		assert.Equal(t, []uint{a3.ID, a6.ID, a1.ID, a2.ID}, ids(answers),
			"[from, to), created_at then id, soft-deleted excluded")
		for _, answer := range answers {
			assert.Empty(t, answer.Citations, "citations are not loaded")
			assert.NotNil(t, answer.CompetitorMentions)
		}
		assert.Equal(t, map[string]int{"Globex": 2}, answers[2].CompetitorMentions)
		assert.True(t, answers[2].CreatedAt.Equal(base.Add(time.Hour)))
	})

	t.Run("run answers and limit", func(t *testing.T) {
		runID := runOne.ID
		answers, err := repo.ListAnswersForExport(ctx, AEOExportFilter{RunID: &runID, From: from, To: to}, 100)
		require.NoError(t, err)
		assert.Equal(t, []uint{a4.ID, a6.ID, a1.ID, a2.ID, a5.ID}, ids(answers), "run equality ignores the window")

		limited, err := repo.ListAnswersForExport(ctx, AEOExportFilter{RunID: &runID}, 2)
		require.NoError(t, err)
		assert.Equal(t, []uint{a4.ID, a6.ID}, ids(limited))

		unknown := uint(999999)
		none, err := repo.ListAnswersForExport(ctx, AEOExportFilter{RunID: &unknown}, 100)
		require.NoError(t, err)
		assert.NotNil(t, none)
		assert.Empty(t, none)
	})

	t.Run("citations by answer", func(t *testing.T) {
		citations, err := repo.ListCitationsByAnswerIDs(ctx, []uint{a2.ID, a3.ID, a1.ID, a1.ID})
		require.NoError(t, err)
		got := make([]uint, 0, len(citations))
		for _, citation := range citations {
			got = append(got, citation.ID)
		}
		assert.Equal(t, []uint{cX1.ID, cX2.ID, cY.ID}, got, "answer_id then id; soft-deleted excluded")

		var empty []models.AEOCitation
		issued := countStatements(func() {
			empty, err = repo.ListCitationsByAnswerIDs(ctx, nil)
		})
		require.NoError(t, err)
		assert.Zero(t, issued, "an empty list issues no statement")
		assert.NotNil(t, empty)
		assert.Empty(t, empty)
	})

	t.Run("citations are read in chunks of 500 and keep their order", func(t *testing.T) {
		// An answer whose id sorts after 998 unknown ids, so its citation is
		// read by the third statement.
		far := models.AEOAnswer{RunID: runTwo.ID, PromptID: livePrompt.ID, Provider: "openai", Model: "m", FirstMentionPos: -1}
		far.ID = 5000
		far.CreatedAt = base.Add(-48 * time.Hour)
		require.NoError(t, db.Omit("Citations").Create(&far).Error)
		cFar := newCitation(far.ID, "far.example")

		answerIDs := []uint{far.ID, a2.ID, a1.ID}
		for id := uint(2001); id <= 2998; id++ {
			answerIDs = append(answerIDs, id)
		}
		require.Len(t, answerIDs, 1001)
		assert.Len(t, aeoExportIDChunks(answerIDs), 3)

		var citations []models.AEOCitation
		var err error
		issued := countStatements(func() {
			citations, err = repo.ListCitationsByAnswerIDs(ctx, answerIDs)
		})
		require.NoError(t, err)
		assert.EqualValues(t, 3, issued, "1,001 ids are read in three statements")
		got := make([]uint, 0, len(citations))
		for _, citation := range citations {
			got = append(got, citation.ID)
		}
		assert.Equal(t, []uint{cX1.ID, cX2.ID, cY.ID, cFar.ID}, got)

		require.NoError(t, db.Unscoped().Delete(&cFar).Error)
		require.NoError(t, db.Unscoped().Delete(&far).Error)
	})

	t.Run("runs by id", func(t *testing.T) {
		runs, err := repo.ListRunsByIDs(ctx, []uint{runTwo.ID, runGone.ID, runOne.ID, runOne.ID})
		require.NoError(t, err)
		require.Len(t, runs, 2, "soft-deleted runs are excluded, duplicates read once")
		assert.Equal(t, runOne.ID, runs[0].ID)
		assert.Equal(t, runTwo.ID, runs[1].ID)
		assert.Equal(t, models.AEOTriggerScheduled, runs[1].Trigger)

		var issued int64
		issued = countStatements(func() {
			runs, err = repo.ListRunsByIDs(ctx, []uint{})
		})
		require.NoError(t, err)
		assert.Zero(t, issued)
		assert.Empty(t, runs)
	})

	t.Run("prompt texts include soft-deleted prompts", func(t *testing.T) {
		texts, err := repo.ListPromptTextsByIDs(ctx, []uint{deletedPrompt.ID, livePrompt.ID, 999999})
		require.NoError(t, err)
		assert.Equal(t, map[uint]string{
			livePrompt.ID:    "Live prompt text",
			deletedPrompt.ID: "Deleted prompt text",
		}, texts)

		issued := countStatements(func() {
			texts, err = repo.ListPromptTextsByIDs(ctx, nil)
		})
		require.NoError(t, err)
		assert.Zero(t, issued)
		assert.NotNil(t, texts)
		assert.Empty(t, texts)
	})

	t.Run("a cancelled context aborts the read", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := repo.ListAnswersForExport(cancelled, AEOExportFilter{From: from, To: to}, 10)
		assert.Error(t, err)
	})
}

func TestAEOExportIDChunks(t *testing.T) {
	assert.Nil(t, aeoExportIDChunks(nil))
	assert.Equal(t, [][]uint{{1, 2, 3}}, aeoExportIDChunks([]uint{3, 1, 2, 3}))
	ids := make([]uint, 0, 1200)
	for id := 1200; id >= 1; id-- {
		ids = append(ids, uint(id))
	}
	chunks := aeoExportIDChunks(ids)
	require.Len(t, chunks, 3)
	assert.Len(t, chunks[0], 500)
	assert.Len(t, chunks[1], 500)
	assert.Len(t, chunks[2], 200)
	assert.Equal(t, uint(1), chunks[0][0])
	assert.Equal(t, uint(501), chunks[1][0])
	assert.Equal(t, uint(1200), chunks[2][199])
}
