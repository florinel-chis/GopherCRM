package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/go-sql-driver/mysql"
)

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

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Setup database connection for migrate library
	db, err := sql.Open("mysql", cfg.Database.DSN())
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

	case "create":
		if *name == "" {
			log.Fatal("Name must be specified with create command")
		}
		createNewMigration(*name, *migrateDir)
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

// createNewMigration creates a new migration file pair
func createNewMigration(name, dir string) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create migrations directory: %v", err)
		}
	}

	// Generate timestamp-based version
	version := time.Now().Format("20060102150405")

	// Create up migration file
	upFile := fmt.Sprintf("%s/%s_%s.up.sql", dir, version, name)
	if err := os.WriteFile(upFile, []byte(fmt.Sprintf("-- Migration: %s\n-- Created: %s\n\n-- Add your SQL statements here\n", name, time.Now().Format(time.RFC3339))), 0644); err != nil {
		log.Fatalf("Failed to create up migration file: %v", err)
	}

	// Create down migration file
	downFile := fmt.Sprintf("%s/%s_%s.down.sql", dir, version, name)
	if err := os.WriteFile(downFile, []byte(fmt.Sprintf("-- Rollback: %s\n-- Created: %s\n\n-- Add your rollback SQL statements here\n", name, time.Now().Format(time.RFC3339))), 0644); err != nil {
		log.Fatalf("Failed to create down migration file: %v", err)
	}

	fmt.Printf("Created migration files:\n")
	fmt.Printf("  %s\n", upFile)
	fmt.Printf("  %s\n", downFile)
}