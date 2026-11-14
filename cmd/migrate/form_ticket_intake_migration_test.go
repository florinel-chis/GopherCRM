package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// #213: 20261115120000_add_form_ticket_intake adds forms.create_ticket,
// form_submissions.idempotency_key, ticket_id, ticket_outcome and
// idx_form_submissions_ticket_id, and creates form_ticket_keys (idempotency_key
// utf8mb4_bin, unique idx_form_ticket_keys_identity, three plain indexes), each
// only when missing. The down half removes them, each only when present.

const (
	formTicketIntakeVersion         = 20261115120000
	formTicketIntakePreviousVersion = aeoRunOwnershipVersion
	formTicketIntakeName            = "20261115120000_add_form_ticket_intake"
	formTicketKeysTable             = "form_ticket_keys"
)

var formTicketSubmissionColumns = []string{"idempotency_key", "ticket_id", "ticket_outcome"}

// TestFormTicketIntakeMigrationFiles needs no database: the pair exists, every
// statement is guarded, the table matches the model, the migration is the
// newest of the directory and latestMigrationVersion names it.
func TestFormTicketIntakeMigrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	up, err := os.ReadFile(filepath.Join(dir, formTicketIntakeName+".up.sql"))
	require.NoError(t, err)
	down, err := os.ReadFile(filepath.Join(dir, formTicketIntakeName+".down.sql"))
	require.NoError(t, err)
	upSQL, downSQL := strings.ToLower(string(up)), strings.ToLower(string(down))

	for _, fragment := range []string{
		"'alter table forms add column create_ticket tinyint(1) not null default 0'",
		"'alter table form_submissions add column idempotency_key varchar(128) not null default '''''",
		"'alter table form_submissions add column ticket_id bigint unsigned null'",
		"'alter table form_submissions add column ticket_outcome varchar(20) not null default '''''",
		"'create index idx_form_submissions_ticket_id on form_submissions (ticket_id)'",
	} {
		assert.Containsf(t, upSQL, fragment, "up declares %s", fragment)
	}
	assert.Contains(t, upSQL, "table_name = 'forms' and column_name = 'create_ticket'), 'select 1', 'alter table", "create_ticket is added only when missing")
	for _, column := range formTicketSubmissionColumns {
		assert.Contains(t, upSQL, "table_name = 'form_submissions' and column_name = '"+column+"'), 'select 1', 'alter table", "%s is added only when missing", column)
		assert.Contains(t, downSQL, "column_name = '"+column+"'), 'alter table form_submissions drop column "+column+"', 'select 1'", "%s is dropped only when present", column)
	}
	assert.Contains(t, upSQL, "index_name = 'idx_form_submissions_ticket_id'), 'select 1', 'create index")
	assert.Contains(t, downSQL, "index_name = 'idx_form_submissions_ticket_id'), 'drop index idx_form_submissions_ticket_id on form_submissions', 'select 1'")
	assert.Contains(t, downSQL, "column_name = 'create_ticket'), 'alter table forms drop column create_ticket', 'select 1'")

	assert.Contains(t, upSQL, "create table if not exists form_ticket_keys (", "the table is created only when missing")
	body := strings.SplitN(upSQL, "create table if not exists form_ticket_keys", 2)[1]
	for _, column := range []string{
		"id bigint unsigned not null auto_increment",
		"created_at datetime(3) null",
		"updated_at datetime(3) null",
		"form_id bigint unsigned not null",
		"idempotency_key varchar(128) character set utf8mb4 collate utf8mb4_bin not null",
		"ticket_id bigint unsigned not null",
		"submission_id bigint unsigned not null",
		"customer_id bigint unsigned not null",
		"primary key (id)",
		"unique key idx_form_ticket_keys_identity (form_id, idempotency_key)",
		"key idx_form_ticket_keys_ticket_id (ticket_id)",
		"key idx_form_ticket_keys_submission_id (submission_id)",
		"key idx_form_ticket_keys_customer_id (customer_id)",
	} {
		assert.Containsf(t, body, column, "form_ticket_keys declares %s", column)
	}
	assert.Equal(t, 1, strings.Count(body, "unique key"), "the identity index is the only unique one")
	assert.NotContains(t, body, "deleted_at", "key rows are hard-deleted")
	assert.NotContains(t, body, "foreign key", "no foreign key")
	for _, forbidden := range []string{"email", "title", "description", "data"} {
		assert.NotContains(t, body, forbidden, "the table holds no %s", forbidden)
	}

	assert.Equal(t, 5, strings.Count(upSQL, "execute stmt;"))
	assert.Equal(t, 5, strings.Count(downSQL, "execute stmt;"))
	assert.NotContains(t, upSQL, "drop ", "the up half drops nothing")
	assert.NotContains(t, upSQL, "\nupdate ", "no backfill")
	assert.NotContains(t, downSQL, "add column", "the down half adds nothing")
	assert.Contains(t, downSQL, "drop table if exists form_ticket_keys;")
	assert.Less(t, strings.Index(downSQL, "drop index idx_form_submissions_ticket_id"), strings.Index(downSQL, "drop column ticket_id"),
		"the index goes before its column")

	// The model declares the same names and widths.
	field, ok := reflectField(models.FormTicketKey{}, "IdempotencyKey")
	require.True(t, ok)
	assert.Contains(t, field, "type:varchar(128)")
	assert.Contains(t, field, "uniqueIndex:idx_form_ticket_keys_identity")
	field, ok = reflectField(models.FormSubmission{}, "TicketID")
	require.True(t, ok)
	assert.Contains(t, field, "index")
	field, ok = reflectField(models.Form{}, "CreateTicket")
	require.True(t, ok)
	assert.Contains(t, field, "default:false")

	// The newest migration of the directory, and the version the fresh and
	// upgrade roots expect (moved here from TestAEORunOwnershipMigrationFiles).
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	newest := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") && entry.Name() > newest {
			newest = entry.Name()
		}
	}
	assert.Equal(t, formTicketIntakeName+".up.sql", newest, "the newest migration")
	version, err := strconv.Atoi(strings.SplitN(newest, "_", 2)[0])
	require.NoError(t, err)
	assert.Equal(t, formTicketIntakeVersion, version)
	assert.Equal(t, formTicketIntakeVersion, latestMigrationVersion)
	assert.Greater(t, formTicketIntakeVersion, formTicketIntakePreviousVersion)

	source, err := os.ReadFile("main_test.go")
	require.NoError(t, err)
	assert.Contains(t, string(source), `"`+formTicketKeysTable+`"`, "main_test.go's expected-table list names the table")
}

// assertFormTicketKeysBinary checks the key column compares exactly. Used by
// the fresh and upgrade roots of main_test.go too.
func assertFormTicketKeysBinary(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	var collation string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(COLLATION_NAME, '') FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'form_ticket_keys' AND COLUMN_NAME = 'idempotency_key'`, schema).Scan(&collation))
	assert.Equal(t, "utf8mb4_bin", collation, "form_ticket_keys.idempotency_key compares exactly")
}

// formTicketIntakeObjects is the part of the schema the migration owns.
type formTicketIntakeObjects struct {
	Forms       []string
	Submissions []string
	Indexes     []string
	Keys        tableSchema
}

func snapshotFormTicketIntake(t *testing.T, tdb *migrationTestDB) formTicketIntakeObjects {
	t.Helper()
	out := formTicketIntakeObjects{
		Forms:       ownedColumns(snapshotTable(t, tdb, "forms"), []string{"create_ticket"}),
		Submissions: ownedColumns(snapshotTable(t, tdb, "form_submissions"), formTicketSubmissionColumns),
		Indexes:     []string{},
	}
	for _, index := range snapshotTable(t, tdb, "form_submissions").Indexes {
		if strings.HasPrefix(index, "idx_form_submissions_ticket_id ") {
			out.Indexes = append(out.Indexes, index)
		}
	}
	if contains(listTables(t, tdb.sql, tdb.name), formTicketKeysTable) {
		out.Keys = snapshotTable(t, tdb, formTicketKeysTable)
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// assertFormTicketKeyIndexEnforced proves the identity index is unique and
// case-sensitive.
func assertFormTicketKeyIndexEnforced(t *testing.T, tdb *migrationTestDB, base int) {
	t.Helper()
	insert := "INSERT INTO form_ticket_keys (form_id, idempotency_key, ticket_id, submission_id, customer_id) VALUES (?, ?, ?, ?, ?)"
	_, err := tdb.sql.Exec(insert, base, "Key-A", 1, 1, 1)
	require.NoError(t, err)
	_, err = tdb.sql.Exec(insert, base, "key-a", 2, 2, 2)
	require.NoError(t, err, "keys differing in case are distinct")
	_, err = tdb.sql.Exec(insert, base, "Key-A", 3, 3, 3)
	require.Error(t, err, "the same key twice on one form is refused")
	_, err = tdb.sql.Exec(insert, base+1, "Key-A", 4, 4, 4)
	require.NoError(t, err, "the same key on another form is distinct")
}

func TestFormTicketIntakeMigration(t *testing.T) {
	baseDSN := resolveTestMySQLBaseDSN()
	if baseDSN == "" {
		t.Skip("no reachable MySQL/MariaDB server")
	}
	previousDB := models.DB
	t.Cleanup(func() { models.DB = previousDB })

	// The parity reference: what MigrateDatabase produces on top of the
	// previous SQL head.
	reference := openMigrationTestDB(t, baseDSN, "fti_ref")
	require.NoError(t, reference.m.Migrate(formTicketIntakePreviousVersion))
	empty := snapshotFormTicketIntake(t, reference)
	assert.Empty(t, empty.Forms)
	assert.Empty(t, empty.Submissions)
	assert.Empty(t, empty.Indexes)
	assert.Empty(t, empty.Keys.Columns)
	refDB, err := gorm.Open(gormmysql.Open(reference.dsn), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	if pool, err := refDB.DB(); err == nil {
		defer pool.Close()
	}
	models.DB = refDB
	require.NoError(t, models.MigrateDatabase())
	want := snapshotFormTicketIntake(t, reference)
	require.Len(t, want.Forms, 1)
	assert.Contains(t, want.Forms[0], "nullable=NO")
	assert.Contains(t, want.Forms[0], "default=0")
	require.Len(t, want.Submissions, 3)
	require.Len(t, want.Indexes, 1)
	assert.Contains(t, want.Indexes[0], "(ticket_id) non_unique=1")
	assert.Contains(t, want.Keys.Indexes, "idx_form_ticket_keys_identity (form_id,idempotency_key) non_unique=0")
	assert.Contains(t, want.Keys.Indexes, "idx_form_ticket_keys_ticket_id (ticket_id) non_unique=1")
	assert.Contains(t, want.Keys.Indexes, "idx_form_ticket_keys_submission_id (submission_id) non_unique=1")
	assert.Contains(t, want.Keys.Indexes, "idx_form_ticket_keys_customer_id (customer_id) non_unique=1")
	assert.Empty(t, want.Keys.ForeignKeys, "no foreign key")
	assertFormTicketKeysBinary(t, reference.sql, reference.name)

	t.Run("Fresh", func(t *testing.T) {
		tdb := openMigrationTestDB(t, baseDSN, "fti_fresh")
		require.NoError(t, tdb.m.Migrate(formTicketIntakePreviousVersion))
		_, err := tdb.sql.Exec("INSERT INTO forms (name, public_id, status, submit_action, create_lead) VALUES ('Kept form', 'fti-kept-form', 'published', 'message', 1)")
		require.NoError(t, err)

		require.NoError(t, tdb.m.Migrate(formTicketIntakeVersion))
		assert.Equal(t, want, snapshotFormTicketIntake(t, tdb), "the SQL migration produces what MigrateDatabase produces")
		assertFormTicketKeysBinary(t, tdb.sql, tdb.name)
		var createTicket int
		require.NoError(t, tdb.sql.QueryRow("SELECT create_ticket FROM forms WHERE public_id = 'fti-kept-form'").Scan(&createTicket))
		assert.Equal(t, 0, createTicket, "an existing form reads create_ticket false")
		assertFormTicketKeyIndexEnforced(t, tdb, 100)

		// Down, then up again.
		require.NoError(t, tdb.m.Steps(-1))
		version, dirty, err := tdb.m.Version()
		require.NoError(t, err)
		assert.False(t, dirty)
		assert.Equal(t, uint(formTicketIntakePreviousVersion), version)
		gone := snapshotFormTicketIntake(t, tdb)
		assert.Empty(t, gone.Forms)
		assert.Empty(t, gone.Submissions)
		assert.Empty(t, gone.Indexes)
		assert.NotContains(t, listTables(t, tdb.sql, tdb.name), formTicketKeysTable)
		var forms int
		require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM forms").Scan(&forms))
		assert.Equal(t, 1, forms, "rollback keeps the forms")

		require.NoError(t, tdb.m.Migrate(formTicketIntakeVersion))
		assert.Equal(t, want, snapshotFormTicketIntake(t, tdb))
		require.ErrorIs(t, tdb.m.Migrate(formTicketIntakeVersion), migrate.ErrNoChange)

		// The down half is guarded: with the table and a column already gone
		// it still succeeds.
		_, err = tdb.sql.Exec("DROP TABLE form_ticket_keys")
		require.NoError(t, err)
		_, err = tdb.sql.Exec("ALTER TABLE form_submissions DROP COLUMN ticket_outcome")
		require.NoError(t, err)
		require.NoError(t, tdb.m.Steps(-1), "the drops run only when present")
		gone = snapshotFormTicketIntake(t, tdb)
		assert.Empty(t, gone.Submissions)
	})

	for _, autoMigrateFirst := range []bool{false, true} {
		autoMigrateFirst := autoMigrateFirst
		t.Run(fmt.Sprintf("Upgrade/AutoMigrateFirst=%t", autoMigrateFirst), func(t *testing.T) {
			tdb := openMigrationTestDB(t, baseDSN, "fti_mig")
			require.NoError(t, tdb.m.Migrate(formTicketIntakePreviousVersion))
			appDB, err := gorm.Open(gormmysql.Open(tdb.dsn), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			pool, err := appDB.DB()
			require.NoError(t, err)
			defer pool.Close()

			if autoMigrateFirst {
				require.NoError(t, appDB.AutoMigrate(&models.Form{}, &models.FormSubmission{}, &models.FormTicketKey{}))
				_, err = tdb.sql.Exec("INSERT INTO form_ticket_keys (form_id, idempotency_key, ticket_id, submission_id, customer_id, created_at, updated_at) VALUES (9, 'kept', 9, 9, 9, NOW(), NOW())")
				require.NoError(t, err)
			}

			require.NoError(t, tdb.m.Migrate(formTicketIntakeVersion), "the migration must apply (autoMigrateFirst=%t)", autoMigrateFirst)
			version, dirty, err := tdb.m.Version()
			require.NoError(t, err)
			assert.False(t, dirty)
			assert.Equal(t, uint(formTicketIntakeVersion), version)
			if autoMigrateFirst {
				var count int
				require.NoError(t, tdb.sql.QueryRow("SELECT COUNT(*) FROM form_ticket_keys WHERE idempotency_key = 'kept'").Scan(&count))
				assert.Equal(t, 1, count, "a table AutoMigrate created first keeps its rows")
			} else {
				assert.Equal(t, want, snapshotFormTicketIntake(t, tdb), "the SQL migration and MigrateDatabase produce the same schema")
			}

			// The application's startup on top converges on the reference,
			// including the binary collation of a table AutoMigrate created.
			models.DB = appDB
			require.NoError(t, models.MigrateDatabase())
			assert.Equal(t, want, snapshotFormTicketIntake(t, tdb), "MigrateDatabase converges on the reference")
			assertFormTicketKeysBinary(t, tdb.sql, tdb.name)
			assertFormTicketKeyIndexEnforced(t, tdb, 200)
			require.ErrorIs(t, tdb.m.Migrate(formTicketIntakeVersion), migrate.ErrNoChange)

			// Single-step rollback, then reapply.
			require.NoError(t, tdb.m.Steps(-1))
			assert.NotContains(t, listTables(t, tdb.sql, tdb.name), formTicketKeysTable)
			gone := snapshotFormTicketIntake(t, tdb)
			assert.Empty(t, gone.Forms)
			assert.Empty(t, gone.Submissions)
			assert.Empty(t, gone.Indexes)
			require.NoError(t, tdb.m.Migrate(formTicketIntakeVersion))
			assert.Equal(t, want, snapshotFormTicketIntake(t, tdb))
			assertFormTicketKeysBinary(t, tdb.sql, tdb.name)
		})
	}
}
