package main

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Schema parity between the two ways a MySQL/MariaDB schema is built: the SQL
// files under migrations/ (cmd/migrate up) and the startup auto-migration
// (models.MigrateDatabaseWith). TestSchemaParityMigration builds one fresh
// schema each way and compares them from INFORMATION_SCHEMA:
//
//   - the set of base tables;
//   - every column's COLUMN_TYPE and IS_NULLABLE, keyed by table and column;
//   - the indexes as a multiset of signatures: table, unique or non-unique,
//     the index type when it is not BTREE, and the ordered column list with
//     SUB_PART prefix lengths and descending columns.
//
// Deliberately not compared, left to a later step: column defaults, column
// and table collations, column order, foreign keys, and index and constraint
// names (MySQL names an index that backs a foreign key after the constraint,
// and the two paths name constraints differently). The golang-migrate
// bookkeeping table is excluded.
//
// A difference that exists on purpose or is not fixed yet is listed in
// schemaParityAllowList with its reason; any other difference fails the test,
// and so does an entry that no longer matches a difference on the engine
// under test.

// schemaParityBookkeepingTable is the golang-migrate version table: it exists
// only on the migrations side and is not part of the application schema.
const schemaParityBookkeepingTable = "schema_migrations"

const (
	parityKindTable  = "table"
	parityKindColumn = "column"
	parityKindIndex  = "index"

	parityEngineMySQL   = "mysql"
	parityEngineMariaDB = "mariadb"
	parityEngineBoth    = "both"

	parityAbsent = "absent"
)

// schemaSnapshot is the compared shape of one schema.
type schemaSnapshot struct {
	// Tables holds the base table names.
	Tables map[string]bool
	// Columns maps "table.column" to "<COLUMN_TYPE> NULL|NOT NULL".
	Columns map[string]string
	// Indexes counts each index signature ("<table> <shape>") per table.
	Indexes map[string]int
}

func newSchemaSnapshot() schemaSnapshot {
	return schemaSnapshot{Tables: map[string]bool{}, Columns: map[string]string{}, Indexes: map[string]int{}}
}

// schemaDifference is one difference between the migrations schema and the
// startup schema. Subject is the column name or the index shape; it is empty
// for a table.
type schemaDifference struct {
	Kind       string
	Table      string
	Subject    string
	Migrations string
	Startup    string
}

func (d schemaDifference) String() string {
	switch d.Kind {
	case parityKindTable:
		return fmt.Sprintf("table %s: migrations %s, startup %s", d.Table, d.Migrations, d.Startup)
	case parityKindColumn:
		return fmt.Sprintf("column %s.%s: migrations %s, startup %s", d.Table, d.Subject,
			quoteParitySide(d.Migrations), quoteParitySide(d.Startup))
	default:
		return fmt.Sprintf("index %s %s: migrations %s, startup %s", d.Table, d.Subject, d.Migrations, d.Startup)
	}
}

func quoteParitySide(value string) string {
	if value == parityAbsent {
		return value
	}
	return strconv.Quote(value)
}

// schemaParityAllowance lists one known difference. Engines is mysql, mariadb
// or both; on another engine the entry is ignored.
type schemaParityAllowance struct {
	Kind    string
	Table   string
	Subject string
	Engines string
	Reason  string
}

func (a schemaParityAllowance) String() string {
	return fmt.Sprintf("%s %s %s (%s)", a.Kind, a.Table, a.Subject, a.Engines)
}

func (a schemaParityAllowance) appliesTo(engine string) bool {
	return a.Engines == parityEngineBoth || a.Engines == engine
}

func (a schemaParityAllowance) matches(d schemaDifference) bool {
	return a.Kind == d.Kind && a.Table == d.Table && a.Subject == d.Subject
}

const (
	parityReasonNullability = "The migrations declare the column NOT NULL and the model has no not null tag, so " +
		"startup creates it nullable. Adding the tag would make SQLite auto-migration rebuild the table on " +
		"every existing install; the choice of one schema authority settles it."
	parityReasonFailedLogins = "The migrations declare int NOT NULL and the model field is an untagged int, " +
		"which startup creates as bigint NULL. Adding type and not null tags would make SQLite auto-migration " +
		"rebuild the users table on every existing install; the choice of one schema authority settles it."
	parityReasonPerformanceIndex = "Created only by migration 20250806120001_add_performance_indexes; the " +
		"models carry no matching index tag. Adding the tag would create the index at startup on every SQLite " +
		"install; the choice of one schema authority settles it."
)

// schemaParityAllowList holds the differences between the two schema paths
// that exist today, each with its reason.
var schemaParityAllowList = []schemaParityAllowance{
	{Kind: parityKindColumn, Table: "api_keys", Subject: "is_active", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "api_keys", Subject: "user_id", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "configurations", Subject: "is_read_only", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "configurations", Subject: "is_sensitive", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "configurations", Subject: "is_system", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "leads", Subject: "owner_id", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "refresh_tokens", Subject: "is_revoked", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "tasks", Subject: "assigned_to_id", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "tickets", Subject: "customer_id", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "users", Subject: "is_active", Engines: parityEngineBoth, Reason: parityReasonNullability},
	{Kind: parityKindColumn, Table: "users", Subject: "failed_login_attempts", Engines: parityEngineBoth, Reason: parityReasonFailedLogins},
	{Kind: parityKindIndex, Table: "api_keys", Subject: "non-unique(is_active,user_id)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "leads", Subject: "non-unique(owner_id,status)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "tasks", Subject: "non-unique(assigned_to_id,status)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "tickets", Subject: "non-unique(assigned_to_id,status)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "tickets", Subject: "non-unique(customer_id,status)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "users", Subject: "non-unique(is_active,role)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
	{Kind: parityKindIndex, Table: "users", Subject: "non-unique(last_login_at)", Engines: parityEngineBoth, Reason: parityReasonPerformanceIndex},
}

// diffSchemas lists every difference between the two snapshots, sorted by
// rendered line. Columns and indexes of a table that exists on one side only
// are covered by the table line and not repeated.
func diffSchemas(migrations, startup schemaSnapshot) []schemaDifference {
	var diffs []schemaDifference
	presence := func(ok bool) string {
		if ok {
			return "present"
		}
		return parityAbsent
	}
	for table := range unionKeys(migrations.Tables, startup.Tables) {
		if migrations.Tables[table] != startup.Tables[table] {
			diffs = append(diffs, schemaDifference{Kind: parityKindTable, Table: table,
				Migrations: presence(migrations.Tables[table]), Startup: presence(startup.Tables[table])})
		}
	}
	onBothSides := func(table string) bool { return migrations.Tables[table] && startup.Tables[table] }

	for key := range unionKeys(migrations.Columns, startup.Columns) {
		table, column, _ := strings.Cut(key, ".")
		if !onBothSides(table) {
			continue
		}
		left, inLeft := migrations.Columns[key]
		right, inRight := startup.Columns[key]
		if inLeft && inRight && left == right {
			continue
		}
		if !inLeft {
			left = parityAbsent
		}
		if !inRight {
			right = parityAbsent
		}
		diffs = append(diffs, schemaDifference{Kind: parityKindColumn, Table: table, Subject: column,
			Migrations: left, Startup: right})
	}

	for signature := range unionKeys(migrations.Indexes, startup.Indexes) {
		table, shape, _ := strings.Cut(signature, " ")
		if !onBothSides(table) {
			continue
		}
		left, right := migrations.Indexes[signature], startup.Indexes[signature]
		if left == right {
			continue
		}
		diffs = append(diffs, schemaDifference{Kind: parityKindIndex, Table: table, Subject: shape,
			Migrations: strconv.Itoa(left), Startup: strconv.Itoa(right)})
	}

	sort.Slice(diffs, func(i, j int) bool { return diffs[i].String() < diffs[j].String() })
	return diffs
}

func unionKeys[V any](a, b map[string]V) map[string]struct{} {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}

// schemaParityReport is the outcome of comparing two snapshots against an
// allow-list on one engine. Every field is a sorted list of rendered lines.
type schemaParityReport struct {
	Unlisted []string
	Stale    []string
	Invalid  []string
}

func (r schemaParityReport) err() error {
	if len(r.Unlisted) == 0 && len(r.Stale) == 0 && len(r.Invalid) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("the SQL migrations and the startup auto-migration build different schemas")
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:", title)
		for _, line := range lines {
			b.WriteString("\n  " + line)
		}
	}
	section("allow-list entries without a reason or with an unknown kind or engine", r.Invalid)
	section("differences not on the allow-list (fix the model or the migration, or allow-list it with a reason)", r.Unlisted)
	section("stale allow-list entries that match no difference on this engine (remove them)", r.Stale)
	return errors.New(b.String())
}

// evaluateSchemaParity compares the snapshots on engine and applies the
// entries of allow that apply to that engine.
func evaluateSchemaParity(migrations, startup schemaSnapshot, engine string, allow []schemaParityAllowance) schemaParityReport {
	var report schemaParityReport
	var active []schemaParityAllowance
	for _, entry := range allow {
		switch {
		case strings.TrimSpace(entry.Reason) == "":
			report.Invalid = append(report.Invalid, entry.String()+": empty reason")
			continue
		case entry.Kind != parityKindTable && entry.Kind != parityKindColumn && entry.Kind != parityKindIndex:
			report.Invalid = append(report.Invalid, entry.String()+": unknown kind")
			continue
		case entry.Engines != parityEngineMySQL && entry.Engines != parityEngineMariaDB && entry.Engines != parityEngineBoth:
			report.Invalid = append(report.Invalid, entry.String()+": unknown engine")
			continue
		}
		if entry.appliesTo(engine) {
			active = append(active, entry)
		}
	}

	used := make([]bool, len(active))
	for _, diff := range diffSchemas(migrations, startup) {
		listed := false
		for i, entry := range active {
			if entry.matches(diff) {
				used[i] = true
				listed = true
			}
		}
		if !listed {
			report.Unlisted = append(report.Unlisted, diff.String())
		}
	}
	for i, entry := range active {
		if !used[i] {
			report.Stale = append(report.Stale, entry.String())
		}
	}
	sort.Strings(report.Invalid)
	sort.Strings(report.Stale)
	return report
}

func TestSchemaParityAllowList(t *testing.T) {
	base := func() schemaSnapshot {
		s := newSchemaSnapshot()
		s.Tables["users"] = true
		s.Columns["users.id"] = "bigint unsigned NOT NULL"
		s.Columns["users.failed_login_attempts"] = "int NOT NULL"
		s.Indexes["users unique(id)"] = 1
		s.Indexes["users non-unique(last_login_at)"] = 1
		return s
	}
	startupWithKnownDiffs := func() schemaSnapshot {
		s := base()
		s.Columns["users.failed_login_attempts"] = "bigint NULL"
		delete(s.Indexes, "users non-unique(last_login_at)")
		return s
	}
	allow := []schemaParityAllowance{
		{Kind: parityKindColumn, Table: "users", Subject: "failed_login_attempts", Engines: parityEngineBoth, Reason: "known"},
		{Kind: parityKindIndex, Table: "users", Subject: "non-unique(last_login_at)", Engines: parityEngineMySQL, Reason: "known"},
	}

	t.Run("listed", func(t *testing.T) {
		report := evaluateSchemaParity(base(), startupWithKnownDiffs(), parityEngineMySQL, allow)
		assert.NoError(t, report.err())
	})

	t.Run("renders_each_kind", func(t *testing.T) {
		startup := startupWithKnownDiffs()
		startup.Tables["extra"] = true
		startup.Columns["extra.id"] = "bigint NULL"
		startup.Columns["users.nickname"] = "varchar(20) NULL"
		startup.Indexes["users unique(nickname(10),id DESC)"] = 1
		report := evaluateSchemaParity(base(), startup, parityEngineMySQL, nil)
		assert.Equal(t, []string{
			`column users.failed_login_attempts: migrations "int NOT NULL", startup "bigint NULL"`,
			`column users.nickname: migrations absent, startup "varchar(20) NULL"`,
			`index users non-unique(last_login_at): migrations 1, startup 0`,
			`index users unique(nickname(10),id DESC): migrations 0, startup 1`,
			`table extra: migrations absent, startup present`,
		}, report.Unlisted)
		assert.Empty(t, report.Stale)
		assert.Empty(t, report.Invalid)
	})

	t.Run("unlisted", func(t *testing.T) {
		startup := startupWithKnownDiffs()
		startup.Columns["users.id"] = "bigint unsigned NULL"
		err := evaluateSchemaParity(base(), startup, parityEngineMySQL, allow).err()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `column users.id: migrations "bigint unsigned NOT NULL", startup "bigint unsigned NULL"`)
		assert.NotContains(t, err.Error(), "failed_login_attempts")
	})

	t.Run("stale", func(t *testing.T) {
		stale := append([]schemaParityAllowance{}, allow...)
		stale = append(stale, schemaParityAllowance{Kind: parityKindColumn, Table: "users", Subject: "id",
			Engines: parityEngineBoth, Reason: "no longer differs"})
		report := evaluateSchemaParity(base(), startupWithKnownDiffs(), parityEngineMySQL, stale)
		assert.Equal(t, []string{"column users id (both)"}, report.Stale)
		require.Error(t, report.err())
		assert.Contains(t, report.err().Error(), "stale allow-list entries")
		assert.Contains(t, report.err().Error(), "column users id (both)")
		assert.Empty(t, report.Unlisted)
	})

	t.Run("engine_scoped", func(t *testing.T) {
		// On MariaDB the mysql-only index entry is ignored: the index difference
		// is unlisted there, and the entry is not reported stale.
		report := evaluateSchemaParity(base(), startupWithKnownDiffs(), parityEngineMariaDB, allow)
		assert.Equal(t, []string{`index users non-unique(last_login_at): migrations 1, startup 0`}, report.Unlisted)
		assert.Empty(t, report.Stale)

		// An entry for the engine under test that matches nothing is stale.
		identical := evaluateSchemaParity(base(), base(), parityEngineMySQL, allow)
		assert.Equal(t, []string{
			"column users failed_login_attempts (both)",
			"index users non-unique(last_login_at) (mysql)",
		}, identical.Stale)
		mariadb := evaluateSchemaParity(base(), base(), parityEngineMariaDB, allow)
		assert.Equal(t, []string{"column users failed_login_attempts (both)"}, mariadb.Stale)
	})

	t.Run("empty_reason", func(t *testing.T) {
		invalid := append([]schemaParityAllowance{}, allow...)
		invalid[0].Reason = "  "
		report := evaluateSchemaParity(base(), startupWithKnownDiffs(), parityEngineMySQL, invalid)
		assert.Equal(t, []string{"column users failed_login_attempts (both): empty reason"}, report.Invalid)
		// The rejected entry lists nothing, so its difference is unlisted too.
		assert.Contains(t, report.Unlisted,
			`column users.failed_login_attempts: migrations "int NOT NULL", startup "bigint NULL"`)
		require.Error(t, report.err())
		assert.Contains(t, report.err().Error(), "empty reason")
	})

	t.Run("real_allow_list_is_well_formed", func(t *testing.T) {
		seen := map[string]bool{}
		for _, entry := range schemaParityAllowList {
			assert.NotEmpty(t, strings.TrimSpace(entry.Reason), entry.String())
			assert.False(t, seen[entry.String()], "duplicate entry %s", entry)
			seen[entry.String()] = true
		}
		report := evaluateSchemaParity(newSchemaSnapshot(), newSchemaSnapshot(), parityEngineMySQL, schemaParityAllowList)
		assert.Empty(t, report.Invalid)
	})
}

// createParitySchema creates an empty database with the character set and
// collation scripts/create_database.sql uses, and drops it at cleanup.
func createParitySchema(t *testing.T, server *sql.DB, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("gocrm_%s_%d", prefix, time.Now().UnixNano())
	_, err := server.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = server.Exec("DROP DATABASE IF EXISTS " + name) })
	return name
}

// readSchemaSnapshot reads the compared shape of schema from INFORMATION_SCHEMA.
func readSchemaSnapshot(t *testing.T, db *sql.DB, schema string) schemaSnapshot {
	t.Helper()
	snapshot := newSchemaSnapshot()

	tables, err := db.Query(`SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'`, schema)
	require.NoError(t, err)
	for tables.Next() {
		var table string
		require.NoError(t, tables.Scan(&table))
		if table != schemaParityBookkeepingTable {
			snapshot.Tables[table] = true
		}
	}
	require.NoError(t, tables.Err())
	require.NoError(t, tables.Close())

	columns, err := db.Query(`SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE
		FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ?`, schema)
	require.NoError(t, err)
	for columns.Next() {
		var table, column, columnType, nullable string
		require.NoError(t, columns.Scan(&table, &column, &columnType, &nullable))
		if !snapshot.Tables[table] {
			continue
		}
		null := "NULL"
		if nullable == "NO" {
			null = "NOT NULL"
		}
		snapshot.Columns[table+"."+column] = columnType + " " + null
	}
	require.NoError(t, columns.Err())
	require.NoError(t, columns.Close())

	type indexShape struct {
		table, indexType string
		unique           bool
		columns          []string
	}
	indexes := map[string]*indexShape{}
	stats, err := db.Query(`SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART, INDEX_TYPE, COLLATION
		FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, schema)
	require.NoError(t, err)
	for stats.Next() {
		var table, index, indexType string
		var nonUnique int64
		var column, collation sql.NullString
		var subPart sql.NullInt64
		require.NoError(t, stats.Scan(&table, &index, &nonUnique, &column, &subPart, &indexType, &collation))
		if !snapshot.Tables[table] {
			continue
		}
		key := table + "\x00" + index
		shape, ok := indexes[key]
		if !ok {
			shape = &indexShape{table: table, indexType: indexType, unique: nonUnique == 0}
			indexes[key] = shape
		}
		part := "<expression>"
		if column.Valid {
			part = column.String
		}
		if subPart.Valid {
			part += fmt.Sprintf("(%d)", subPart.Int64)
		}
		if collation.Valid && collation.String == "D" {
			part += " DESC"
		}
		shape.columns = append(shape.columns, part)
	}
	require.NoError(t, stats.Err())
	require.NoError(t, stats.Close())

	for _, shape := range indexes {
		kind := "non-unique"
		if shape.unique {
			kind = "unique"
		}
		if !strings.EqualFold(shape.indexType, "BTREE") {
			kind += " " + strings.ToLower(shape.indexType)
		}
		snapshot.Indexes[shape.table+" "+kind+"("+strings.Join(shape.columns, ",")+")"]++
	}
	return snapshot
}

// parityEngine reports mariadb or mysql for the server behind db.
func parityEngine(t *testing.T, db *sql.DB) string {
	t.Helper()
	var version string
	require.NoError(t, db.QueryRow("SELECT VERSION()").Scan(&version))
	if strings.Contains(strings.ToLower(version), "mariadb") {
		return parityEngineMariaDB
	}
	return parityEngineMySQL
}

func TestSchemaParityMigration(t *testing.T) {
	baseDSN := testMySQLBaseDSN(t)

	server, err := sql.Open("mysql", migrationDSN(baseDSN))
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	engine := parityEngine(t, server)

	// Schema A: every SQL migration.
	migrationsSchema := createParitySchema(t, server, "parity_sql")
	migrationsDB, err := sql.Open("mysql", requireMySQLTestDSN(t, baseDSN, migrationsSchema))
	require.NoError(t, err)
	driver, err := mysql.WithInstance(migrationsDB, &mysql.Config{})
	require.NoError(t, err)
	migrationsPath, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsPath, "mysql", driver)
	require.NoError(t, err)
	// Registered after the DROP cleanup, so it runs first.
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, m.Up())

	// Schema B: the startup path.
	startupSchema := createParitySchema(t, server, "parity_boot")
	startupDB, err := gorm.Open(gormmysql.Open(requireMySQLTestDSN(t, baseDSN, startupSchema)),
		&gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	startupSQL, err := startupDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = startupSQL.Close() })
	require.NoError(t, models.MigrateDatabaseWith(startupDB))

	// Both snapshots are taken before any subtest runs, so detects (which
	// alters schema B) cannot change what parity compares, whatever the order
	// or -run filter.
	migrationsSnapshot := readSchemaSnapshot(t, server, migrationsSchema)
	startupSnapshot := readSchemaSnapshot(t, server, startupSchema)
	require.NotEmpty(t, migrationsSnapshot.Tables)
	require.NotEmpty(t, startupSnapshot.Tables)

	t.Run("parity", func(t *testing.T) {
		report := evaluateSchemaParity(migrationsSnapshot, startupSnapshot, engine, schemaParityAllowList)
		require.NoError(t, report.err(), "engine %s", engine)
	})

	t.Run("detects", func(t *testing.T) {
		for _, stmt := range []string{
			"CREATE TABLE parity_probe_only (id bigint)",
			"ALTER TABLE companies ADD COLUMN parity_probe int NULL",
			"ALTER TABLE companies MODIFY name varchar(201) NOT NULL",
			"ALTER TABLE companies MODIFY city varchar(100) NOT NULL",
			"CREATE INDEX parity_probe_idx ON companies (city)",
		} {
			_, err := startupSQL.Exec(stmt)
			require.NoError(t, err, stmt)
		}
		altered := readSchemaSnapshot(t, server, startupSchema)
		report := evaluateSchemaParity(migrationsSnapshot, altered, engine, schemaParityAllowList)

		intType := "int"
		if engine == parityEngineMariaDB {
			intType = "int(11)"
		}
		want := []string{
			fmt.Sprintf(`column companies.parity_probe: migrations absent, startup "%s NULL"`, intType),
			`column companies.name: migrations "varchar(200) NOT NULL", startup "varchar(201) NOT NULL"`,
			`column companies.city: migrations "varchar(100) NULL", startup "varchar(100) NOT NULL"`,
			`index companies non-unique(city): migrations 0, startup 1`,
			`table parity_probe_only: migrations absent, startup present`,
		}
		for _, line := range want {
			assert.Contains(t, report.Unlisted, line)
		}
		assert.ElementsMatch(t, want, report.Unlisted,
			"the allow-list must hide none of the injected differences and nothing else may differ")
		assert.Empty(t, report.Stale)
		assert.Empty(t, report.Invalid)
		err := report.err()
		require.Error(t, err)
		for _, line := range want {
			assert.Contains(t, err.Error(), line)
		}
	})
}
