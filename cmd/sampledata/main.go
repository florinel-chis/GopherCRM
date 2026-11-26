// Command sampledata builds a fresh database filled with a fictional business,
// for demos, screenshots and local development. It runs the server's own
// auto-migration and configuration defaults and then writes every record
// through the shipped services, so the business rules (roles, ownership, lead
// conversion, quote totals, deal stages) hold exactly as they do for data
// entered through the API.
//
//	go run ./cmd/sampledata --profile halvenmark --seed 1 --out sample-data/halvenmark.db
//	go run ./cmd/sampledata --profile halvenmark --seed 1 --dsn 'user:pass@tcp(127.0.0.1:3306)/gophercrm_demo'
//
// It reads .env and the environment like the server (JWT_SECRET is required
// and should match the server's) and refuses to run when SERVER_MODE is
// production. The database location comes from --out or --dsn only; DB_*
// settings are ignored.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sampledata:", err)
		os.Exit(1)
	}
}

// run is the command; the anchor of the backdated history is the start of
// the current UTC day, so two runs on the same day write the same data.
func run(args []string, stdout io.Writer) error {
	return runAt(args, stdout, time.Time{})
}

type options struct {
	profile *Profile
	seed    int64
	out     string
	dsn     string
	scale   float64
	force   bool
}

func parseOptions(args []string) (*options, error) {
	fs := flag.NewFlagSet("sampledata", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "profile to generate ("+strings.Join(profileNames(), ", ")+")")
	seed := fs.Int64("seed", 1, "random seed; the same seed and day give the same data")
	out := fs.String("out", "", "SQLite database file to create")
	dsn := fs.String("dsn", "", "MySQL/MariaDB DSN user:pass@tcp(host:port)/dbname; the database is created when missing")
	scale := fs.Float64("scale", 1, "multiplier of the scaled volumes, greater than 0 and at most 10")
	force := fs.Bool("force", false, "delete an existing database (the --out file, or DROP the --dsn database) and build it again")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w (usage: --profile <name> --seed <n> (--out <file> | --dsn <dsn>) [--scale f] [--force])", err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if (*out == "") == (*dsn == "") {
		return nil, errors.New("exactly one of --out or --dsn is required")
	}
	if !(*scale > 0 && *scale <= 10) {
		return nil, errors.New("--scale must be greater than 0 and at most 10")
	}
	p, err := lookupProfile(*profile)
	if err != nil {
		return nil, err
	}
	return &options{profile: p, seed: *seed, out: *out, dsn: *dsn, scale: *scale, force: *force}, nil
}

// runAt is run with an explicit anchor; tests pass a fixed one.
func runAt(args []string, stdout io.Writer, anchor time.Time) (err error) {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}

	// The same configuration the server and cmd/create-admin load: .env in
	// the working directory, then the environment.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	// Before any file or database is touched, whatever --force says.
	if strings.EqualFold(strings.TrimSpace(cfg.Server.Mode), "production") {
		return errors.New("refusing to generate sample data: SERVER_MODE is production")
	}

	if err := utils.InitLogger(&config.LoggingConfig{Level: "warn", Format: "text"}); err != nil {
		return err
	}
	utils.Logger.SetOutput(os.Stderr)

	if anchor.IsZero() {
		anchor = time.Now().UTC().Truncate(24 * time.Hour)
	}
	anchor = anchor.UTC()

	dbCfg, err := databaseConfig(opts)
	if err != nil {
		return err
	}
	if dbCfg.Driver == config.DriverSQLite {
		if err := prepareSQLite(dbCfg.Path, opts.force); err != nil {
			return err
		}
	} else if err := prepareMySQL(*dbCfg, opts.force); err != nil {
		return err
	}

	db, err := database.Open(dbCfg)
	if err != nil {
		return err
	}
	// Errors only: GORM's slow-query lines would otherwise interleave with
	// the summary on stdout.
	db.Logger = db.Logger.LogMode(gormlogger.Error)
	models.DB = db
	defer func() {
		if closeErr := models.CloseDatabase(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close database: %w", closeErr)
		}
	}()

	if err := refuseExistingData(db, opts.force); err != nil {
		return err
	}

	// What the server does at boot.
	if err := models.MigrateDatabaseWith(db); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := repository.NewConfigurationRepository(db).InitializeDefaults(); err != nil {
		return fmt.Errorf("failed to initialize default configurations: %w", err)
	}

	gen := newGenerator(db, buildServices(db, cfg), opts.profile, opts.seed, opts.scale, anchor)
	if err := gen.run(); err != nil {
		return err
	}
	counts, err := countTables(db)
	if err != nil {
		return err
	}
	// Close before reporting success: on SQLite the close checkpoints the
	// write-ahead log into the database file.
	if err := models.CloseDatabase(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}
	printSummary(stdout, opts, dbCfg, counts)
	return nil
}

// databaseConfig builds the connection settings from the flags alone; DB_*
// values from the environment are ignored.
func databaseConfig(opts *options) (*config.DatabaseConfig, error) {
	if opts.out != "" {
		return &config.DatabaseConfig{Driver: config.DriverSQLite, Path: filepath.Clean(opts.out)}, nil
	}
	parsed, err := mysqldriver.ParseDSN(opts.dsn)
	if err != nil {
		// The parse error can quote the DSN, password included.
		return nil, errors.New("--dsn is not a valid MySQL DSN (user:pass@tcp(host:port)/dbname)")
	}
	if parsed.Net != "" && parsed.Net != "tcp" {
		return nil, errors.New("--dsn must use tcp(host:port)")
	}
	if !databaseNamePattern.MatchString(parsed.DBName) {
		return nil, errors.New("--dsn must name a database of letters, digits and underscores")
	}
	host, port := "127.0.0.1", 3306
	if parsed.Addr != "" {
		h, p, err := net.SplitHostPort(parsed.Addr)
		if err != nil {
			return nil, errors.New("--dsn address must be host:port")
		}
		host = h
		if port, err = strconv.Atoi(p); err != nil {
			return nil, errors.New("--dsn port must be a number")
		}
	}
	return &config.DatabaseConfig{
		Driver: config.DriverMySQL, Host: host, Port: port,
		User: parsed.User, Password: parsed.Passwd, Name: parsed.DBName,
	}, nil
}

var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func prepareSQLite(path string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create the output directory: %w", err)
	}
	if !force {
		return nil
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove %s: %w", name, err)
		}
	}
	return nil
}

// prepareMySQL creates the named database when it is missing; with force it
// drops and recreates it. Only that database is dropped.
func prepareMySQL(cfg config.DatabaseConfig, force bool) (err error) {
	name := "`" + cfg.Name + "`"
	server := cfg
	server.Name = ""
	db, err := database.Open(&server)
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			if closeErr := sqlDB.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	}()
	if force {
		if err := db.Exec("DROP DATABASE IF EXISTS " + name).Error; err != nil {
			return fmt.Errorf("failed to drop the database: %w", err)
		}
	}
	if err := db.Exec("CREATE DATABASE IF NOT EXISTS " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		return fmt.Errorf("failed to create the database: %w", err)
	}
	return nil
}

// refuseExistingData refuses a database that already has users unless
// --force was given; nothing has been written at that point.
func refuseExistingData(db *gorm.DB, force bool) error {
	if force || !db.Migrator().HasTable("users") {
		return nil
	}
	var users int64
	if err := db.Table("users").Count(&users).Error; err != nil {
		return err
	}
	if users > 0 {
		return fmt.Errorf("the database already holds %d users; pass --force to delete it and generate it again", users)
	}
	return nil
}

// summaryTables are the tables the summary counts, in print order.
var summaryTables = []string{
	"users", "companies", "leads", "customers", "deals", "deal_stage_changes", "products",
	"quotes", "quote_lines", "tickets", "ticket_comments", "tasks", "labels", "task_labels",
	"notifications", "automation_rules", "forms", "webhook_endpoints",
}

func countTables(db *gorm.DB) (map[string]int64, error) {
	counts := map[string]int64{}
	for _, table := range summaryTables {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil {
			return nil, err
		}
		counts[table] = n
	}
	return counts, nil
}

func printSummary(w io.Writer, opts *options, dbCfg *config.DatabaseConfig, counts map[string]int64) {
	target := dbCfg.Path
	if dbCfg.Driver == config.DriverMySQL {
		target = "MySQL database " + dbCfg.Name
	}
	fmt.Fprintf(w, "Generated profile %s (seed %d, scale %g) into %s\n\n", opts.profile.Name, opts.seed, opts.scale, target)
	for _, table := range summaryTables {
		fmt.Fprintf(w, "  %-20s %6d\n", table, counts[table])
	}
	fmt.Fprintf(w, "\nAdmin sign-in:  %s\n", opts.profile.AdminEmail())
	fmt.Fprintf(w, "Demo password:  %s  (local-demo value shared by every generated account; never use it elsewhere)\n", opts.profile.DemoPassword)
}
