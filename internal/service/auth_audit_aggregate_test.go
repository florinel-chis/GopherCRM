package service

// Issue #466: a refused API key adds to one auth_audit_log row per reason,
// user id, client network and UTC minute instead of writing a row of its own.
// TestAuthAuditAPIKeyAggregation{SQLite,MySQL} run one table of checks through
// the real audit writer and repository, with the clock pinned through the
// service's now, on a SQLite file opened by the production connector and on a
// schema built by the real migrations (TEST_MYSQL_DSN, also pointed at
// MariaDB 10.11).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// authAuditClock is a settable clock for the audit writer.
type authAuditClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *authAuditClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *authAuditClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// apiKeyRefusal is the error ValidateAPIKey returns for reason and owner.
func apiKeyRefusal(reason string, userID uint) error {
	return authRefusal(reason, userID, errors.New("invalid API key"))
}

func authAuditIPCtx(requestID, ip string) context.Context {
	return utils.WithClientInfo(utils.WithRequestID(context.Background(), requestID), ip, authAuditTestUA)
}

func runAuthAuditAPIKeyAggregation(t *testing.T, db *gorm.DB) {
	output := captureAuthAuditLogs(t)
	clock := &authAuditClock{}
	svc := NewAuthAuditService(repository.NewAuthAuditRepository(db)).(*authAuditService)
	svc.now = clock.Now
	minute := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	// apiKeyRows returns the api_key rows of network created in the minute
	// starting at start.
	apiKeyRows := func(t *testing.T, network string, start time.Time) []models.AuthAuditLog {
		t.Helper()
		var rows []models.AuthAuditLog
		require.NoError(t, db.Where("method = ? AND client_network = ? AND created_at >= ? AND created_at < ?",
			AuthAuditMethodAPIKey, network, start, start.Add(time.Minute)).Order("id").Find(&rows).Error)
		return rows
	}
	failedLines := func(from int) int {
		n := 0
		for _, line := range authAuditLogLines(output, from) {
			if line["error_code"] == "audit_write_failed" {
				n++
			}
		}
		return n
	}

	t.Run("one_row_per_network_and_minute", func(t *testing.T) {
		clock.Set(minute.Add(10 * time.Second))
		var firstID uint
		for i := 0; i < 500; i++ {
			ip := fmt.Sprintf("198.51.100.%d", i%250+1)
			id := svc.RecordAPIKeyFailure(authAuditIPCtx(fmt.Sprintf("req-agg-%d", i), ip), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
			require.NotZero(t, id)
			if i == 0 {
				firstID = id
			}
			require.Equal(t, firstID, id, "every refusal returns the bucket row's id")
		}
		rows := apiKeyRows(t, "198.51.100.0/24", minute)
		require.Len(t, rows, 1, "500 refusals from one /24 in one minute are one row")
		row := rows[0]
		assert.Equal(t, firstID, row.ID)
		assert.EqualValues(t, 500, row.Attempts)
		assert.Equal(t, "198.51.100.0/24", row.ClientNetwork)
		assert.Equal(t, "req-agg-0", row.RequestID, "the first refusal's request id")
		assert.Equal(t, AuthAuditOutcomeRefused, row.Outcome)
		assert.Equal(t, AuthAuditReasonKeyInvalid, row.Reason)
		assert.Nil(t, row.UserID)
		assert.Equal(t, authAuditTestFamily, row.UserAgentFamily)
		assert.True(t, minute.Add(10*time.Second).Equal(row.CreatedAt.UTC()), "created_at is the first refusal's")
		require.NotNil(t, row.AggregateKey)
		assert.Equal(t, "api_key|key_invalid|0|198.51.100.0/24|2026-11-25T12:00", *row.AggregateKey)

		// The last instant of the minute still counts in it.
		clock.Set(minute.Add(59*time.Second + 999*time.Millisecond))
		assert.Equal(t, firstID, svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-last", "198.51.100.9"), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0)))
		rows = apiKeyRows(t, "198.51.100.0/24", minute)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 501, rows[0].Attempts)

		// The next minute starts a new row.
		next := minute.Add(time.Minute)
		clock.Set(next)
		nextID := svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-next", "198.51.100.9"), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
		require.NotZero(t, nextID)
		assert.NotEqual(t, firstID, nextID)
		rows = apiKeyRows(t, "198.51.100.0/24", next)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 1, rows[0].Attempts)
		assert.Equal(t, "req-agg-next", rows[0].RequestID)
		assert.EqualValues(t, 501, apiKeyRows(t, "198.51.100.0/24", minute)[0].Attempts, "the previous minute is unchanged")
	})

	t.Run("another_network_gets_its_own_row", func(t *testing.T) {
		clock.Set(minute.Add(20 * time.Second))
		id := svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-other", "203.0.113.5"), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
		require.NotZero(t, id)
		rows := apiKeyRows(t, "203.0.113.0/24", minute)
		require.Len(t, rows, 1)
		assert.Equal(t, id, rows[0].ID)
		assert.EqualValues(t, 1, rows[0].Attempts)
		assert.EqualValues(t, 501, apiKeyRows(t, "198.51.100.0/24", minute)[0].Attempts, "the other network's row is unchanged")
	})

	t.Run("ipv6_48_shares_a_row", func(t *testing.T) {
		clock.Set(minute.Add(30 * time.Second))
		first := svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-v6a", "2001:db8:1234:1::1"), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
		second := svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-v6b", "2001:db8:1234:ffff::2"), apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
		require.NotZero(t, first)
		assert.Equal(t, first, second, "two addresses of one /48 share the row")
		network := utils.ClientNetwork("2001:db8:1234:1::1")
		require.NotEmpty(t, network)
		rows := apiKeyRows(t, network, minute)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 2, rows[0].Attempts)
		assert.Equal(t, "req-agg-v6a", rows[0].RequestID)
	})

	t.Run("reason_and_user_split_rows", func(t *testing.T) {
		const userA, userB = 4660001, 4660002
		at := minute.Add(40 * time.Second)
		clock.Set(at)
		ip := "192.0.2.44"
		cases := []struct {
			reason string
			userID uint
		}{
			{AuthAuditReasonKeyRevoked, userA},
			{AuthAuditReasonKeyRevoked, userB},
			{AuthAuditReasonKeyExpired, userA},
		}
		ids := map[uint]bool{}
		for i, c := range cases {
			for n := 0; n < 2; n++ {
				id := svc.RecordAPIKeyFailure(authAuditIPCtx(fmt.Sprintf("req-agg-split-%d-%d", i, n), ip), apiKeyRefusal(c.reason, c.userID))
				require.NotZero(t, id)
				ids[id] = true
			}
		}
		assert.Len(t, ids, 3, "one row per (reason, user id)")
		rows := apiKeyRows(t, "192.0.2.0/24", minute)
		require.Len(t, rows, 3)
		for i, row := range rows {
			assert.Equal(t, cases[i].reason, row.Reason)
			require.NotNil(t, row.UserID)
			assert.Equal(t, cases[i].userID, *row.UserID)
			assert.EqualValues(t, 2, row.Attempts)
		}
	})

	t.Run("password_and_refresh_stay_one_row_each", func(t *testing.T) {
		clock.Set(minute.Add(45 * time.Second))
		ctx := authAuditIPCtx("req-agg-mixed", "198.51.100.200")
		var before int64
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Count(&before).Error)
		ids := map[uint]bool{}
		for i := 0; i < 3; i++ {
			ids[svc.RecordPassword(ctx, nil, errLoginInvalidCredentials)] = true
			ids[svc.RecordRefresh(ctx, nil, ErrInvalidRefreshToken)] = true
		}
		delete(ids, 0)
		assert.Len(t, ids, 6, "six rows")
		var rows []models.AuthAuditLog
		require.NoError(t, db.Where("method IN ? AND request_id = ?", []string{AuthAuditMethodPassword, AuthAuditMethodRefresh}, "req-agg-mixed").Find(&rows).Error)
		require.Len(t, rows, 6)
		for _, row := range rows {
			assert.EqualValues(t, 1, row.Attempts)
			assert.Nil(t, row.AggregateKey)
			assert.Equal(t, "198.51.100.0/24", row.ClientNetwork)
		}
		var after int64
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Count(&after).Error)
		assert.Equal(t, before+6, after)
	})

	t.Run("nil_error_writes_nothing", func(t *testing.T) {
		var before, after int64
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Count(&before).Error)
		assert.Zero(t, svc.RecordAPIKeyFailure(authAuditIPCtx("req-agg-nil", "198.51.100.1"), nil))
		require.NoError(t, db.Model(&models.AuthAuditLog{}).Count(&after).Error)
		assert.Equal(t, before, after)
	})

	t.Run("concurrent_refusals_one_row", func(t *testing.T) {
		bucket := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
		clock.Set(bucket.Add(5 * time.Second))
		from := output.Len()
		const workers, perWorker = 8, 25
		var wg sync.WaitGroup
		start := make(chan struct{})
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start
				for i := 0; i < perWorker; i++ {
					svc.RecordAPIKeyFailure(authAuditIPCtx(fmt.Sprintf("req-agg-c%d-%d", w, i), fmt.Sprintf("198.18.0.%d", w+1)),
						apiKeyRefusal(AuthAuditReasonKeyInvalid, 0))
				}
			}(w)
		}
		close(start)
		wg.Wait()
		failures := failedLines(from)
		rows := apiKeyRows(t, "198.18.0.0/24", bucket)
		require.Len(t, rows, 1, "one row however the inserts raced")
		assert.GreaterOrEqual(t, rows[0].Attempts, uint(1))
		assert.Equal(t, workers*perWorker, int(rows[0].Attempts)+failures,
			"every refusal is counted or logged as a failed write")
		if db.Dialector.Name() == "sqlite" {
			assert.Zero(t, failures, "SQLite serialises the writes: no failure")
		} else {
			t.Logf("%s: %d of %d concurrent writes failed", db.Dialector.Name(), failures, workers*perWorker)
		}
	})
}

func TestAuthAuditAPIKeyAggregationSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditAPIKeyAggregation(t, db)

	// A table as #454's AutoMigrate created it gains both columns and the
	// unique index; its row reads attempts 1 and no key.
	t.Run("upgrade_from_454_table", func(t *testing.T) {
		cfg := &config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Join(t.TempDir(), "upgrade.db")}
		old, err := database.Open(cfg)
		require.NoError(t, err)
		sqlDB, err := old.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		for _, statement := range []string{
			"CREATE TABLE `auth_audit_log` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime NOT NULL,`method` varchar(16) NOT NULL,`outcome` varchar(16) NOT NULL,`reason` varchar(64) NOT NULL,`user_id` integer,`attempt_id` integer,`request_id` varchar(64) NOT NULL,`subject_ref` varchar(64),`claim_roles` text,`claim_groups` text,`client_network` varchar(64) NOT NULL,`user_agent_family` varchar(64) NOT NULL)",
			"CREATE INDEX `idx_auth_audit_log_created_at` ON `auth_audit_log`(`created_at`)",
			"CREATE INDEX `idx_auth_audit_log_user_id` ON `auth_audit_log`(`user_id`)",
			"CREATE INDEX `idx_auth_audit_log_subject_ref` ON `auth_audit_log`(`subject_ref`)",
			"INSERT INTO auth_audit_log (created_at, method, outcome, reason, request_id, client_network, user_agent_family) VALUES ('2026-11-24 10:00:00', 'api_key', 'refused', 'key_invalid', 'req-old', '192.0.2.0/24', '')",
		} {
			require.NoError(t, old.Exec(statement).Error, statement)
		}
		require.NoError(t, models.MigrateDatabaseWith(old))
		migrator := old.Migrator()
		assert.True(t, migrator.HasColumn(&models.AuthAuditLog{}, "attempts"))
		assert.True(t, migrator.HasColumn(&models.AuthAuditLog{}, "aggregate_key"))
		assert.True(t, migrator.HasIndex(&models.AuthAuditLog{}, "idx_auth_audit_log_aggregate_key"))
		var rows []models.AuthAuditLog
		require.NoError(t, old.Find(&rows).Error)
		require.Len(t, rows, 1)
		assert.EqualValues(t, 1, rows[0].Attempts)
		assert.Nil(t, rows[0].AggregateKey)
		assert.Equal(t, "req-old", rows[0].RequestID)
	})
}

func TestAuthAuditAPIKeyAggregationMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 8)
	runAuthAuditAPIKeyAggregation(t, db)
}
