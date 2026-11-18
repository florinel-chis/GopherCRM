// quote-response-link emails a sent quote of the disposable browser-test
// database through the real quote service, with a mailer that keeps the
// message in memory instead of sending it, and prints the client response
// link of that message. The browser suite needs the link and has no other way
// to get it: the log-only mailer never writes a mail body, and no HTTP route
// returns the token. The command is not part of the server binary and adds no
// route.
//
// It refuses to run outside scripts/e2e/run.sh (which exports E2E_UI_PORT)
// and, on MySQL, against a database whose name does not end in _e2e: emailing
// a quote invalidates the link of its previous email and stamps the quote as
// emailed, which must never happen to a real quote.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
)

const responseLinkPath = "/quote-response?token="

var (
	errNotE2ERun      = errors.New("refusing to run outside the e2e runner: E2E_UI_PORT is not set (use make e2e)")
	errNotE2EDatabase = errors.New("refusing the database: on mysql its name must end in _e2e")
	errNoLink         = errors.New("the quote was not emailed with a response link")
)

// capturingMailer keeps the messages it is asked to send and sends nothing.
type capturingMailer struct {
	bodies []string
}

var _ mailer.Mailer = (*capturingMailer)(nil)

func (m *capturingMailer) Send(_, _, body string) error {
	m.bodies = append(m.bodies, body)
	return nil
}

func (m *capturingMailer) SendPasswordReset(string, string, string) error { return nil }

// checkE2ETarget is the guard applied before any connection is opened.
func checkE2ETarget(cfg *config.DatabaseConfig) error {
	if strings.TrimSpace(os.Getenv("E2E_UI_PORT")) == "" {
		return errNotE2ERun
	}
	// The rule of scripts/e2e/run.sh: the e2e MySQL database is dropped on
	// every run, so its name ends in _e2e. A SQLite run uses a fresh file.
	if cfg.Driver == config.DriverMySQL && !strings.HasSuffix(cfg.Name, "_e2e") {
		return errNotE2EDatabase
	}
	return nil
}

// newQuoteService wires the quote service as cmd/main.go does, with the given
// mailer in place of the application's.
func newQuoteService(db *gorm.DB, cfg *config.Config, m mailer.Mailer) service.QuoteService {
	configService := service.NewConfigurationService(repository.NewConfigurationRepository(db),
		utils.NewSecretBox(cfg.API.APIKeySecret, "configuration-secret"))
	tokenSecret := cfg.API.APIKeySecret
	if tokenSecret == "" {
		tokenSecret = cfg.JWT.Secret
	}
	return service.NewQuoteServiceWithResponseLinks(
		repository.NewQuoteRepository(db), repository.NewProductRepository(db), repository.NewDealRepository(db),
		repository.NewCustomerRepository(db), repository.NewUserRepository(db), configService, m,
		utils.NewTransactionManager(db), repository.NewQuoteResponseTokenRepository(db),
		cfg.App.BaseURL, tokenSecret)
}

// responseLink is the link line of a quote mail body, or "" without one.
func responseLink(body, baseURL string) string {
	prefix := strings.TrimRight(baseURL, "/") + responseLinkPath
	for _, field := range strings.Fields(body) {
		if strings.HasPrefix(field, prefix) && len(field) > len(prefix) {
			return field
		}
	}
	return ""
}

// emitLink emails the quote and writes its response link, and nothing else,
// to stdout.
func emitLink(db *gorm.DB, cfg *config.Config, quoteID uint, stdout io.Writer) error {
	captured := &capturingMailer{}
	if _, err := newQuoteService(db, cfg, captured).Email(quoteID); err != nil {
		return fmt.Errorf("quote %d was not emailed: %w", quoteID, err)
	}
	if len(captured.bodies) != 1 {
		return fmt.Errorf("quote %d: %w", quoteID, errNoLink)
	}
	link := responseLink(captured.bodies[0], cfg.App.BaseURL)
	if link == "" {
		return fmt.Errorf("quote %d: %w", quoteID, errNoLink)
	}
	_, err := fmt.Fprintln(stdout, link)
	return err
}

type openDatabase func(*config.DatabaseConfig) (*gorm.DB, error)

func run(args []string, stdout io.Writer, open openDatabase) (err error) {
	flags := flag.NewFlagSet("quote-response-link", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	quoteID := flags.Uint("quote-id", 0, "id of the sent quote to email")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *quoteID == 0 {
		return errors.New("-quote-id is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := checkE2ETarget(&cfg.Database); err != nil {
		return err
	}
	if utils.Logger == nil {
		utils.InitLogger(&cfg.Logging)
	}
	// Stdout carries the link and nothing else.
	utils.Logger.SetOutput(os.Stderr)

	db, err := open(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			dbErr = sqlDB.Close()
		}
		if dbErr != nil && err == nil {
			err = fmt.Errorf("failed to close database: %w", dbErr)
		}
	}()

	return emitLink(db, cfg, *quoteID, stdout)
}

func main() {
	// The application's loggers write to os.Stdout; the link is the only
	// thing this command may print there, so they are pointed at stderr.
	stdout := os.Stdout
	os.Stdout = os.Stderr
	if err := run(os.Args[1:], stdout, database.Open); err != nil {
		fmt.Fprintln(os.Stderr, "quote-response-link:", err)
		os.Exit(1)
	}
}
