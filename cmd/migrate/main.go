package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// commandCreate authors a new migration file pair; every other command talks to
// a database.
const commandCreate = "create"

// ErrSQLiteUnsupported is returned for commands that would execute the files in
// migrations/, which are MySQL DDL.
var ErrSQLiteUnsupported = errors.New("SQL migrations are MySQL-only; SQLite schemas are created by auto-migration at startup")

// checkCommandSupported reports whether command can run against driver. The
// "create" case is stated here so the whole policy reads in one place, even
// though main short-circuits it earlier: authoring a file needs no
// configuration and therefore no driver.
func checkCommandSupported(command, driver string) error {
	if command == commandCreate {
		return nil
	}
	if driver == config.DriverSQLite {
		return ErrSQLiteUnsupported
	}
	return nil
}

// migrationDSN ensures the MySQL DSN enables multiStatements, which
// golang-migrate requires to execute multi-statement migration files.
func migrationDSN(dsn string) string {
	if strings.Contains(dsn, "multiStatements=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&multiStatements=true"
	}
	return dsn + "?multiStatements=true"
}

func main() {
	// Parse command line flags
	var (
		command    = flag.String("command", "up", "Migration command: up, down, goto, drop, force, version, create")
		steps      = flag.Int("steps", 0, "Number of steps to migrate (0 means all)")
		version    = flag.Uint("version", 0, "Migrate to specific version")
		name       = flag.String("name", "", "Name for new migration (used with create command)")
		migrateDir = flag.String("dir", "migrations", "Directory containing migration files")
		verbose    = flag.Bool("verbose", false, "Enable verbose logging")
	)
	flag.Parse()

	// Authoring a migration only touches the filesystem, so it runs before any
	// configuration or database is required.
	if *command == commandCreate {
		if *name == "" {
			log.Fatal("Name must be specified with create command")
		}
		upPath, downPath, err := createNewMigration(*name, *migrateDir)
		if err != nil {
			log.Fatalf("Failed to create migration: %v", err)
		}
		fmt.Printf("Created migration files:\n  %s\n  %s\n", upPath, downPath)
		return
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// The files in migrations/ are MySQL DDL; SQLite deployments get their
	// schema from auto-migration when the server starts.
	if err := checkCommandSupported(*command, cfg.Database.Driver); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Setup database connection for migrate library
	db, err := sql.Open("mysql", migrationDSN(cfg.Database.DSN()))
	if err != nil {
		log.Fatalf("Failed to open database connection: %v", err)
	}
	defer db.Close()

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		log.Fatalf("Failed to create database driver: %v", err)
	}

	// Create migrate instance
	m, err := migrate.NewWithDatabaseInstance(
		fmt.Sprintf("file://%s", *migrateDir),
		"mysql",
		driver,
	)
	if err != nil {
		log.Fatalf("Failed to create migrate instance: %v", err)
	}
	defer m.Close()

	if *verbose {
		m.Log = &Logger{}
	}

	// Execute command
	switch *command {
	case "up":
		if *steps > 0 {
			fmt.Printf("Applying %d migrations...\n", *steps)
			err = m.Steps(*steps)
		} else {
			fmt.Println("Applying all pending migrations...")
			err = m.Up()
		}

	case "down":
		if *steps > 0 {
			fmt.Printf("Rolling back %d migrations...\n", *steps)
			err = m.Steps(-*steps)
		} else {
			fmt.Println("Rolling back all migrations...")
			err = m.Down()
		}

	case "goto":
		fmt.Printf("Migrating to version %d...\n", *version)
		err = m.Migrate(*version)

	case "drop":
		fmt.Println("Dropping database schema...")
		err = m.Drop()

	case "force":
		fmt.Printf("Forcing database to version %d...\n", *version)
		err = m.Force(int(*version))

	case "version":
		ver, dirty, verErr := m.Version()
		if verErr != nil {
			if verErr == migrate.ErrNilVersion {
				fmt.Println("Database version: No migrations applied")
			} else {
				log.Fatalf("Failed to get database version: %v", verErr)
			}
		} else {
			status := "clean"
			if dirty {
				status = "dirty"
			}
			fmt.Printf("Database version: %d (%s)\n", ver, status)
		}
		return

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", *command)
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  up      - Apply pending migrations (default)")
		fmt.Fprintln(os.Stderr, "  down    - Rollback migrations")
		fmt.Fprintln(os.Stderr, "  goto    - Migrate to specific version")
		fmt.Fprintln(os.Stderr, "  drop    - Drop entire database schema")
		fmt.Fprintln(os.Stderr, "  force   - Force database to specific version")
		fmt.Fprintln(os.Stderr, "  version - Show current database version")
		fmt.Fprintln(os.Stderr, "  create  - Create new migration files")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Handle migration result
	if err != nil {
		if err == migrate.ErrNoChange {
			fmt.Println("No changes to apply")
		} else {
			log.Fatalf("Migration failed: %v", err)
		}
	} else {
		fmt.Println("Migration completed successfully!")
	}
}

// Logger implements migrate.Logger interface
type Logger struct{}

func (l *Logger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

func (l *Logger) Verbose() bool {
	return true
}

// migrationFilePattern is the file name layout golang-migrate reads:
// <version>_<name>.up.sql and <version>_<name>.down.sql.
var migrationFilePattern = regexp.MustCompile(`^([0-9]+)_([A-Za-z0-9_]+)\.(up|down)\.sql$`)

// migrationNamePattern is the set of names create accepts, so that the files
// it writes always match migrationFilePattern.
var migrationNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// migrationVersion is one version found in a migrations directory, with the
// file names that carry it.
type migrationVersion struct {
	Version uint64
	Names   []string
	Up      []string
	Down    []string
}

// readMigrationVersions parses the migration file names in dir and returns the
// versions in the order os.ReadDir lists them, plus one problem per file or
// version that breaks the layout rules:
//   - a .sql file whose name does not match <version>_<name>.{up,down}.sql
//     (golang-migrate would skip it without a word);
//   - a version used under more than one name;
//   - a version without exactly one .up.sql and one .down.sql file;
//   - a version lower than or equal to an earlier, different version in
//     listing order, so numeric and lexical order must agree.
//
// Directories and files not ending in .sql are ignored. A missing directory
// holds no migrations and is not an error.
func readMigrationVersions(dir string) ([]migrationVersion, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read migrations directory: %w", err)
	}

	var (
		problems []string
		versions []migrationVersion
		index    = make(map[uint64]int)
		last     uint64
		seen     bool
	)
	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(fileName, ".sql") {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(fileName)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s: name does not match <version>_<name>.up.sql or <version>_<name>.down.sql", fileName))
			continue
		}
		version, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: version %s is not a valid unsigned 64-bit number", fileName, m[1]))
			continue
		}

		// The up and down files of one version are listed next to each
		// other, so an equal adjacent version is the same migration and only
		// a step down is out of order.
		if seen && version < last {
			problems = append(problems, fmt.Sprintf("%s: version %d is listed after version %d; versions must increase in file name order", fileName, version, last))
		}
		last, seen = version, true

		i, ok := index[version]
		if !ok {
			i = len(versions)
			index[version] = i
			versions = append(versions, migrationVersion{Version: version})
		}
		v := &versions[i]
		if !containsString(v.Names, m[2]) {
			v.Names = append(v.Names, m[2])
		}
		if m[3] == "up" {
			v.Up = append(v.Up, fileName)
		} else {
			v.Down = append(v.Down, fileName)
		}
	}

	for _, v := range versions {
		if len(v.Names) > 1 {
			problems = append(problems, fmt.Sprintf("version %d is used by more than one name: %s", v.Version, strings.Join(v.Names, ", ")))
		}
		switch len(v.Up) {
		case 0:
			problems = append(problems, fmt.Sprintf("version %d has no .up.sql file (found %s)", v.Version, strings.Join(v.Down, ", ")))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("version %d has more than one .up.sql file: %s", v.Version, strings.Join(v.Up, ", ")))
		}
		switch len(v.Down) {
		case 0:
			problems = append(problems, fmt.Sprintf("version %d has no .down.sql file (found %s)", v.Version, strings.Join(v.Up, ", ")))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("version %d has more than one .down.sql file: %s", v.Version, strings.Join(v.Down, ", ")))
		}
	}
	return versions, problems, nil
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// nextMigrationVersion returns the version a new migration in dir takes: the
// newest existing version + 1. Only a directory without migrations (missing
// or empty) falls back to the current UTC time as YYYYMMDDHHMMSS. It fails
// when the existing files break the layout rules of readMigrationVersions.
func nextMigrationVersion(dir string) (uint64, error) {
	versions, problems, err := readMigrationVersions(dir)
	if err != nil {
		return 0, err
	}
	if len(problems) > 0 {
		return 0, fmt.Errorf("migrations in %s need fixing first:\n  %s", dir, strings.Join(problems, "\n  "))
	}
	if len(versions) == 0 {
		return strconv.ParseUint(time.Now().UTC().Format("20060102150405"), 10, 64)
	}
	var newest uint64
	for _, v := range versions {
		if v.Version > newest {
			newest = v.Version
		}
	}
	if newest == math.MaxUint64 {
		return 0, fmt.Errorf("newest migration version %d leaves no room for a next one", newest)
	}
	return newest + 1, nil
}

// createNewMigration writes an empty migration pair named name into dir and
// returns the two paths. Nothing is written when name is not made of letters,
// digits and underscores, when the existing files break the layout rules, or
// when a target file already exists.
func createNewMigration(name, dir string) (upPath, downPath string, err error) {
	if !migrationNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("migration name %q may contain only letters, digits and underscores", name)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("create migrations directory: %w", err)
	}

	// The version is the newest existing version + 1, so a new migration
	// always sorts after every migration a database may already have applied.
	version, err := nextMigrationVersion(dir)
	if err != nil {
		return "", "", err
	}

	created := time.Now().Format(time.RFC3339)
	upPath = filepath.Join(dir, fmt.Sprintf("%d_%s.up.sql", version, name))
	downPath = filepath.Join(dir, fmt.Sprintf("%d_%s.down.sql", version, name))

	if err := writeNewFile(upPath, fmt.Sprintf("-- Migration: %s\n-- Created: %s\n\n-- Add your SQL statements here\n", name, created)); err != nil {
		return "", "", fmt.Errorf("create up migration file: %w", err)
	}
	if err := writeNewFile(downPath, fmt.Sprintf("-- Rollback: %s\n-- Created: %s\n\n-- Add your rollback SQL statements here\n", name, created)); err != nil {
		_ = os.Remove(upPath)
		return "", "", fmt.Errorf("create down migration file: %w", err)
	}
	return upPath, downPath, nil
}

// writeNewFile writes content to path and refuses to overwrite an existing file.
func writeNewFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
