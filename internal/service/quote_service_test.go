package service

// Regression roots for the quotes module (issue #158). Every root runs the
// real service over the real repositories on a real database — in-memory
// SQLite with the production one-connection pool, and a disposable MySQL
// schema when TEST_MYSQL_DSN is set — and every assertion reads committed rows
// outside any transaction. The helpers here are shared with
// quote_status_regression_test.go and quote_email_test.go.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// quoteTestTimeout bounds every wait. It is generous for a loaded CI host but
// far below the check timeout, so a hung transaction fails the scenario
// instead of the package.
const quoteTestTimeout = 20 * time.Second

var quoteTestSeq atomic.Uint64

func quoteTestGormConfig() *gorm.Config {
	cfg := &gorm.Config{Logger: logger.Discard}
	cfg.DisableForeignKeyConstraintWhenMigrating = true
	return cfg
}

// migrateQuoteTestSchema creates the tables the quote flows read or write,
// plus the ones the deal and customer repositories preload. Foreign keys are
// not created: the rows are the service's responsibility here.
func migrateQuoteTestSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{},
		&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{},
	))
}

// openQuoteTestSQLite is an in-memory database with the pool pinned to one
// connection, exactly as a SQLite deployment runs.
func openQuoteTestSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), quoteTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateQuoteTestSchema(t, db)
	return db
}

// openQuoteTestMySQL creates a uniquely named disposable schema on the server
// identified by TEST_MYSQL_DSN and drops it afterwards. Without the variable
// the root skips; a configured but unreachable server fails.
func openQuoteTestMySQL(t *testing.T, maxConns int) *gorm.DB {
	t.Helper()
	raw := os.Getenv("TEST_MYSQL_DSN")
	if raw == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, quoteTestTimeout, quoteTestTimeout
	server, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	name := fmt.Sprintf("gocrm_quotes_%d_%d", time.Now().UnixNano(), quoteTestSeq.Add(1))
	t.Cleanup(func() {
		_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = server.Close()
	})
	_, err = server.Exec("CREATE DATABASE `" + name + "`")
	require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), quoteTestGormConfig())
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	// Registered after the DROP cleanup, so it runs first and the schema is
	// dropped only once its last connection is closed.
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateQuoteTestSchema(t, db)
	return db
}

// quoteTestMail is one message the test mailer was asked to deliver.
type quoteTestMail struct {
	To, Subject, Body string
}

// quoteTestMailer stands in for the SMTP transport. beforeSend runs with no
// lock held, so a scenario can commit a competing transition from inside the
// send — the moment the service holds no transaction.
type quoteTestMailer struct {
	mu         sync.Mutex
	sent       []quoteTestMail
	err        error
	beforeSend func(to, subject, body string) error
}

func (m *quoteTestMailer) SendPasswordReset(to, resetURL string) error {
	return m.Send(to, "password reset", resetURL)
}

func (m *quoteTestMailer) Send(to, subject, body string) error {
	m.mu.Lock()
	hook, err := m.beforeSend, m.err
	m.mu.Unlock()
	if hook != nil {
		if hookErr := hook(to, subject, body); hookErr != nil {
			return hookErr
		}
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, quoteTestMail{To: to, Subject: subject, Body: body})
	return nil
}

func (m *quoteTestMailer) messages() []quoteTestMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]quoteTestMail, len(m.sent))
	copy(out, m.sent)
	return out
}

func (m *quoteTestMailer) reset(err error, hook func(to, subject, body string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent, m.err, m.beforeSend = nil, err, hook
}

// quoteTestEnv is the real service over one database with one owner, one
// customer, one open deal of that customer and one EUR product.
type quoteTestEnv struct {
	t        *testing.T
	db       *gorm.DB
	svc      *quoteService
	repo     repository.QuoteRepository
	mailer   *quoteTestMailer
	owner    *models.User
	customer *models.Customer
	deal     *models.Deal
	product  *models.Product
}

func newQuoteTestEnv(t *testing.T, db *gorm.DB) *quoteTestEnv {
	t.Helper()
	if utils.Logger == nil {
		utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	}
	seq := quoteTestSeq.Add(1)
	env := &quoteTestEnv{t: t, db: db, mailer: &quoteTestMailer{}, repo: repository.NewQuoteRepository(db)}
	env.svc = env.service(env.repo)

	env.owner = &models.User{Email: fmt.Sprintf("quote-owner-%d@example.com", seq), Password: "x", FirstName: "Quota", LastName: "Owner", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(env.owner).Error)
	// Distinctive names: the logging root asserts they never reach a log line.
	env.customer = &models.Customer{FirstName: "Zsofia-Canary", LastName: "Marinescu-Canary", Email: fmt.Sprintf("zsofia-canary-%d@example.com", seq), Company: "Analytical Engines"}
	require.NoError(t, db.Create(env.customer).Error)
	env.deal = &models.Deal{Title: "Engine", Stage: models.DealStageProposal, Probability: 40, Currency: "EUR", OwnerID: env.owner.ID, CustomerID: &env.customer.ID}
	require.NoError(t, db.Create(env.deal).Error)
	env.product = env.createProduct("Widget", 1000, "EUR")
	return env
}

// service wires a quote service over the given quote repository and the real
// repositories for everything else; the regression roots pass a wrapped one.
func (e *quoteTestEnv) service(quotes repository.QuoteRepository) *quoteService {
	return NewQuoteService(quotes, repository.NewProductRepository(e.db), repository.NewDealRepository(e.db),
		repository.NewCustomerRepository(e.db), repository.NewUserRepository(e.db), nil, e.mailer,
		utils.NewTransactionManager(e.db)).(*quoteService)
}

func (e *quoteTestEnv) createProduct(name string, price int64, currency string) *models.Product {
	e.t.Helper()
	product := &models.Product{Name: name, SKU: name, UnitPriceCents: price, Currency: currency, IsActive: true}
	require.NoError(e.t, e.db.Create(product).Error)
	return product
}

// newQuote creates a draft through the service (so it has its create history
// row), adds `lines` lines of the EUR product, and then moves the row straight
// to `status` with the matching stamps and one history row, bypassing the
// transition rules: the roots exercise those rules from here.
func (e *quoteTestEnv) newQuote(status models.QuoteStatus, lines int, opts ...func(*models.Quote)) *models.Quote {
	e.t.Helper()
	quote := &models.Quote{Title: "Engine licences", Currency: "EUR", DealID: &e.deal.ID, CustomerID: &e.customer.ID, OwnerID: e.owner.ID}
	for _, opt := range opts {
		opt(quote)
	}
	require.NoError(e.t, e.svc.Create(quote, e.owner.ID, models.RoleSales))
	for i := 0; i < lines; i++ {
		_, err := e.svc.AddLine(quote.ID, models.QuoteLineInput{ProductID: e.product.ID, Quantity: int64(i + 2)})
		require.NoError(e.t, err)
	}
	if status != models.QuoteStatusDraft {
		now := e.svc.stamp()
		from := models.QuoteStatusDraft
		updates := map[string]interface{}{"status": status, "sent_at": now}
		if status.IsClosed() {
			from = models.QuoteStatusSent
			updates["closed_at"] = now
		}
		require.NoError(e.t, e.db.Model(&models.Quote{}).Where("id = ?", quote.ID).Updates(updates).Error)
		require.NoError(e.t, e.repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID: quote.ID, FromStatus: &from, ToStatus: status, ChangedByID: e.owner.ID, ChangedAt: now,
		}))
	}
	return e.quote(quote.ID)
}

// quote reads the committed quote with its lines.
func (e *quoteTestEnv) quote(id uint) *models.Quote {
	e.t.Helper()
	quote, err := e.svc.GetByID(id)
	require.NoError(e.t, err)
	return quote
}

func (e *quoteTestEnv) lines(id uint) []models.QuoteLine {
	e.t.Helper()
	lines, err := e.repo.ListLines(id)
	require.NoError(e.t, err)
	return lines
}

func (e *quoteTestEnv) history(id uint) []models.QuoteStatusChange {
	e.t.Helper()
	rows, err := e.repo.ListStatusChanges(id)
	require.NoError(e.t, err)
	return rows
}

// snapshot is the committed state of the quote row and its lines, rendered so
// that two reads can be compared for "nothing changed".
func (e *quoteTestEnv) snapshot(id uint) string {
	e.t.Helper()
	var quoteRows, lineRows []map[string]interface{}
	require.NoError(e.t, e.db.Table("quotes").Where("id = ?", id).Find(&quoteRows).Error)
	require.NoError(e.t, e.db.Table("quote_lines").Where("quote_id = ?", id).Order("id").Find(&lineRows).Error)
	encoded, err := json.Marshal(map[string]interface{}{"quote": quoteRows, "lines": lineRows})
	require.NoError(e.t, err)
	return string(encoded)
}

// assertTotalsMatchLines is the invariant every committed quote must hold:
// its totals are the sums over its committed lines, and a sent quote has at
// least one.
func (e *quoteTestEnv) assertTotalsMatchLines(id uint) *models.Quote {
	e.t.Helper()
	quote := e.quote(id)
	lines := e.lines(id)
	subtotal, discount, total := models.ComputeQuoteTotals(lines)
	assert.Equal(e.t, subtotal, quote.SubtotalCents, "subtotal of quote %d", id)
	assert.Equal(e.t, discount, quote.DiscountCents, "discount of quote %d", id)
	assert.Equal(e.t, total, quote.TotalCents, "total of quote %d", id)
	if quote.Status == models.QuoteStatusSent {
		assert.NotEmpty(e.t, lines, "no committed sent quote may have zero lines")
	}
	return quote
}

func int64Ptr(v int64) *int64 { return &v }

// dateDaysFromNow is a valid_until value `days` from today, as the API stores it.
func dateDaysFromNow(t *testing.T, days int) *time.Time {
	t.Helper()
	date, err := models.ParseDealDate(time.Now().AddDate(0, 0, days).Format("2006-01-02"))
	require.NoError(t, err)
	return &date
}

// ---------------------------------------------------------------- AC-4 ------

// Every bound a line request can break is refused as a validation failure
// before anything is written: the quote row and its lines are byte for byte
// what they were. The gross is checked before the discount arithmetic, so the
// largest quantity times the largest price — 10^18, still an int64 — never
// gets multiplied by a basis-point factor.
func TestQuoteLineLimitsRejectedWithoutWrites(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)
	expensive := env.createProduct("Everything", models.QuoteAmountCentsMax, "EUR")
	quote := env.newQuote(models.QuoteStatusDraft, 1)
	lineID := quote.Lines[0].ID
	before := env.snapshot(quote.ID)

	refused := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.Truef(t, errors.Is(err, apperrors.ErrValidation), "want a validation failure, got %v", err)
		assert.Falsef(t, errors.Is(err, apperrors.ErrQuoteLocked), "a bound failure is not a lock: %v", err)
	}

	for _, tc := range []struct {
		name  string
		input models.QuoteLineInput
	}{
		{"quantity below 1", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 0}},
		{"quantity above the bound", models.QuoteLineInput{ProductID: env.product.ID, Quantity: models.QuoteLineQuantityMax + 1}},
		{"negative discount", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1, DiscountBP: -1}},
		{"discount above 100%", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1, DiscountBP: models.QuoteDiscountBPMax + 1}},
		{"negative price override", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1, UnitPriceCents: int64Ptr(-1)}},
		{"price override above the bound", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1, UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax + 1)}},
		{"gross above the bound", models.QuoteLineInput{ProductID: expensive.ID, Quantity: 2}},
		{"gross just above the bound through an override", models.QuoteLineInput{ProductID: env.product.ID, Quantity: 2, UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax/2 + 1)}},
		{"maximal quantity times maximal price with a full discount", models.QuoteLineInput{ProductID: expensive.ID, Quantity: models.QuoteLineQuantityMax, DiscountBP: models.QuoteDiscountBPMax}},
	} {
		t.Run("add: "+tc.name, func(t *testing.T) {
			_, err := env.svc.AddLine(quote.ID, tc.input)
			refused(t, err)
			assert.Equal(t, before, env.snapshot(quote.ID), "no row of quotes or quote_lines may change")
		})
	}

	for _, tc := range []struct {
		name  string
		patch models.QuoteLinePatch
	}{
		{"quantity below 1", models.QuoteLinePatch{Quantity: int64Ptr(0)}},
		{"quantity above the bound", models.QuoteLinePatch{Quantity: int64Ptr(models.QuoteLineQuantityMax + 1)}},
		{"negative discount", models.QuoteLinePatch{DiscountBP: int64Ptr(-1)}},
		{"discount above 100%", models.QuoteLinePatch{DiscountBP: int64Ptr(models.QuoteDiscountBPMax + 1)}},
		{"negative price", models.QuoteLinePatch{UnitPriceCents: int64Ptr(-1)}},
		{"price above the bound", models.QuoteLinePatch{UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax + 1)}},
		{"position below 1", models.QuoteLinePatch{Position: int64Ptr(0)}},
		{"gross above the bound", models.QuoteLinePatch{Quantity: int64Ptr(2), UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax)}},
		{"maximal quantity times maximal price with a full discount", models.QuoteLinePatch{Quantity: int64Ptr(models.QuoteLineQuantityMax), UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax), DiscountBP: int64Ptr(models.QuoteDiscountBPMax)}},
	} {
		t.Run("update: "+tc.name, func(t *testing.T) {
			_, err := env.svc.UpdateLine(quote.ID, lineID, tc.patch)
			refused(t, err)
			assert.Equal(t, before, env.snapshot(quote.ID))
		})
	}

	t.Run("the quote total may not exceed the bound", func(t *testing.T) {
		capped := env.newQuote(models.QuoteStatusDraft, 0)
		// One line of the bound less 500: fine on its own.
		_, err := env.svc.AddLine(capped.ID, models.QuoteLineInput{ProductID: expensive.ID, Quantity: 1, UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax - 500)})
		require.NoError(t, err)
		assert.Equal(t, models.QuoteAmountCentsMax-500, env.quote(capped.ID).TotalCents)
		cappedBefore := env.snapshot(capped.ID)

		// A second line whose own gross is tiny but takes the total over.
		_, err = env.svc.AddLine(capped.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1})
		refused(t, err)
		assert.Equal(t, cappedBefore, env.snapshot(capped.ID))

		// Exactly the bound is allowed, one more cent is not.
		first := env.lines(capped.ID)[0].ID
		_, err = env.svc.UpdateLine(capped.ID, first, models.QuoteLinePatch{UnitPriceCents: int64Ptr(models.QuoteAmountCentsMax)})
		require.NoError(t, err)
		assert.Equal(t, models.QuoteAmountCentsMax, env.quote(capped.ID).TotalCents)
		atBound := env.snapshot(capped.ID)
		_, err = env.svc.AddLine(capped.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1, UnitPriceCents: int64Ptr(1)})
		refused(t, err)
		assert.Equal(t, atBound, env.snapshot(capped.ID))
		env.assertTotalsMatchLines(capped.ID)
	})

	t.Run("a quote holds at most 100 lines", func(t *testing.T) {
		full := env.newQuote(models.QuoteStatusDraft, 0)
		lines := make([]models.QuoteLine, 0, models.QuoteLinesMax)
		for i := 0; i < models.QuoteLinesMax; i++ {
			_, total := models.ComputeQuoteLineTotal(1, 100, 0)
			lines = append(lines, models.QuoteLine{QuoteID: full.ID, ProductID: env.product.ID, Position: int64(i + 1), Description: "Widget", Quantity: 1, UnitPriceCents: 100, LineTotalCents: total})
		}
		require.NoError(t, db.Create(&lines).Error)
		rows, err := env.repo.UpdateTotals(full.ID, 100*100, 0, 100*100)
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		fullBefore := env.snapshot(full.ID)

		_, err = env.svc.AddLine(full.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1})
		refused(t, err)
		assert.Equal(t, fullBefore, env.snapshot(full.ID))
		assert.Len(t, env.lines(full.ID), models.QuoteLinesMax)

		// Editing one of the hundred is still allowed: the limit is on the count.
		_, err = env.svc.UpdateLine(full.ID, lines[0].ID, models.QuoteLinePatch{Quantity: int64Ptr(2)})
		require.NoError(t, err)
		env.assertTotalsMatchLines(full.ID)
	})

	// The first quote never changed through all of it.
	assert.Equal(t, before, env.snapshot(quote.ID))
}

// ---------------------------------------------------------------- AC-11 -----

// claimRaceRepository flips the quote's status on the transaction's own
// handle right after the locking read, standing in for a competing writer
// that got between the read and the claim. Real locking makes that impossible
// on MySQL; the point is that the claim itself refuses it.
type claimRaceRepository struct {
	repository.QuoteRepository
	tx     *gorm.DB
	flipTo models.QuoteStatus
	fired  *atomic.Bool
}

func (r *claimRaceRepository) WithTx(tx *gorm.DB) repository.QuoteRepository {
	return &claimRaceRepository{QuoteRepository: r.QuoteRepository.WithTx(tx), tx: tx, flipTo: r.flipTo, fired: r.fired}
}

func (r *claimRaceRepository) GetByIDForUpdate(id uint) (*models.Quote, error) {
	quote, err := r.QuoteRepository.GetByIDForUpdate(id)
	if err == nil && r.tx != nil && r.fired.CompareAndSwap(false, true) {
		if flipErr := r.tx.Model(&models.Quote{}).Where("id = ?", id).Update("status", r.flipTo).Error; flipErr != nil {
			return nil, flipErr
		}
	}
	return quote, err
}

// failingHistoryRepository refuses to write the history row, so the create
// and the transition must roll back everything they wrote before it.
type failingHistoryRepository struct {
	repository.QuoteRepository
}

func (r *failingHistoryRepository) WithTx(tx *gorm.DB) repository.QuoteRepository {
	return &failingHistoryRepository{QuoteRepository: r.QuoteRepository.WithTx(tx)}
}

func (r *failingHistoryRepository) CreateStatusChange(*models.QuoteStatusChange) error {
	return errors.New("injected: history write failed")
}

// Exactly five moves are allowed. Every other pair, the same status included,
// is INVALID_STATUS_TRANSITION and writes nothing; an allowed move claims the
// row, stamps it and appends one history row in the same transaction; a send
// without lines rolls the claim back; acceptance is refused once valid_until
// has passed; and the history reads oldest first.
func TestQuoteStatusTransitionMatrix(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)

	allowed := map[[2]models.QuoteStatus]bool{
		{models.QuoteStatusDraft, models.QuoteStatusSent}:    true,
		{models.QuoteStatusSent, models.QuoteStatusAccepted}: true,
		{models.QuoteStatusSent, models.QuoteStatusRejected}: true,
		{models.QuoteStatusSent, models.QuoteStatusExpired}:  true,
		{models.QuoteStatusSent, models.QuoteStatusDraft}:    true,
	}
	assert.Equal(t, 400, apperrors.New(apperrors.CodeInvalidStatusTransition, "").HTTPStatus,
		"INVALID_STATUS_TRANSITION is answered with 400")

	moves := 0
	for _, from := range models.QuoteStatuses {
		for _, to := range models.QuoteStatuses {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				quote := env.newQuote(from, 1)
				historyBefore := env.history(quote.ID)
				before := env.snapshot(quote.ID)

				moved, err := env.svc.ChangeStatus(quote.ID, to, env.owner.ID)
				if !allowed[[2]models.QuoteStatus{from, to}] {
					require.Error(t, err)
					assert.Truef(t, errors.Is(err, apperrors.ErrInvalidQuoteTransition), "%s -> %s: %v", from, to, err)
					assert.Equal(t, before, env.snapshot(quote.ID), "a refused transition writes nothing")
					assert.Len(t, env.history(quote.ID), len(historyBefore), "a refused transition appends no history")
					return
				}
				moves++
				require.NoError(t, err)
				assert.Equal(t, to, moved.Status)
				committed := env.quote(quote.ID)
				assert.Equal(t, to, committed.Status)
				history := env.history(quote.ID)
				require.Len(t, history, len(historyBefore)+1, "one history row per transition")
				last := history[len(history)-1]
				require.NotNil(t, last.FromStatus)
				assert.Equal(t, from, *last.FromStatus)
				assert.Equal(t, to, last.ToStatus)
				assert.Equal(t, env.owner.ID, last.ChangedByID)
				switch {
				case to == models.QuoteStatusSent:
					assert.NotNil(t, committed.SentAt, "sent sets sent_at")
					assert.Nil(t, committed.ClosedAt)
				case to == models.QuoteStatusDraft:
					assert.Nil(t, committed.SentAt, "sent->draft clears sent_at")
					assert.Nil(t, committed.ClosedAt, "sent->draft clears closed_at")
				case to.IsClosed():
					assert.NotNil(t, committed.ClosedAt, "%s sets closed_at", to)
					assert.NotNil(t, committed.SentAt, "the send stamp is kept")
				}
			})
		}
	}
	assert.Equal(t, 5, moves)

	t.Run("draft->sent without lines rolls the claim back", func(t *testing.T) {
		empty := env.newQuote(models.QuoteStatusDraft, 0)
		before := env.snapshot(empty.ID)
		_, err := env.svc.ChangeStatus(empty.ID, models.QuoteStatusSent, env.owner.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteHasNoLines), "%v", err)
		assert.Equal(t, before, env.snapshot(empty.ID), "the claim is rolled back")
		committed := env.quote(empty.ID)
		assert.Equal(t, models.QuoteStatusDraft, committed.Status)
		assert.Nil(t, committed.SentAt)
		assert.Len(t, env.history(empty.ID), 1, "only the create row")

		// With a line it goes through.
		_, err = env.svc.AddLine(empty.ID, models.QuoteLineInput{ProductID: env.product.ID, Quantity: 1})
		require.NoError(t, err)
		_, err = env.svc.ChangeStatus(empty.ID, models.QuoteStatusSent, env.owner.ID)
		require.NoError(t, err)
	})

	t.Run("accepted is refused after valid_until", func(t *testing.T) {
		expired := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, -1) })
		before := env.snapshot(expired.ID)
		_, err := env.svc.ChangeStatus(expired.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrQuoteExpired), "%v", err)
		assert.Equal(t, before, env.snapshot(expired.ID))
		assert.Len(t, env.history(expired.ID), 2)
		// Expiry guards acceptance only: the quote can still be rejected or
		// marked expired.
		moved, err := env.svc.ChangeStatus(expired.ID, models.QuoteStatusExpired, env.owner.ID)
		require.NoError(t, err)
		assert.Equal(t, models.QuoteStatusExpired, moved.Status)

		today := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 0) })
		_, err = env.svc.ChangeStatus(today.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.NoError(t, err, "valid until today is still valid today")
		tomorrow := env.newQuote(models.QuoteStatusSent, 1, func(q *models.Quote) { q.ValidUntil = dateDaysFromNow(t, 1) })
		_, err = env.svc.ChangeStatus(tomorrow.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.NoError(t, err)
	})

	t.Run("the claim is a conditional update", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusSent, 1)
		before := env.snapshot(quote.ID)
		hooked := &claimRaceRepository{QuoteRepository: env.repo, flipTo: models.QuoteStatusRejected, fired: &atomic.Bool{}}
		racy := env.service(hooked)
		_, err := racy.ChangeStatus(quote.ID, models.QuoteStatusAccepted, env.owner.ID)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrInvalidQuoteTransition), "a claim that matched no row is a lost race: %v", err)
		assert.True(t, hooked.fired.Load(), "the hook must have fired after the locking read")
		assert.Equal(t, before, env.snapshot(quote.ID), "the transaction, flip included, is rolled back")
		assert.Len(t, env.history(quote.ID), 2)
	})

	t.Run("a failed history write rolls the transition back", func(t *testing.T) {
		quote := env.newQuote(models.QuoteStatusDraft, 1)
		before := env.snapshot(quote.ID)
		broken := env.service(&failingHistoryRepository{QuoteRepository: env.repo})
		_, err := broken.ChangeStatus(quote.ID, models.QuoteStatusSent, env.owner.ID)
		require.Error(t, err)
		assert.Equal(t, before, env.snapshot(quote.ID), "the claim and the stamps are rolled back with the history")
		assert.Equal(t, models.QuoteStatusDraft, env.quote(quote.ID).Status)
	})

	t.Run("the history reads oldest first", func(t *testing.T) {
		clock := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
		env.svc.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
		t.Cleanup(func() { env.svc.now = time.Now })

		journey := env.newQuote(models.QuoteStatusDraft, 1)
		for _, to := range []models.QuoteStatus{models.QuoteStatusSent, models.QuoteStatusDraft, models.QuoteStatusSent, models.QuoteStatusAccepted} {
			_, err := env.svc.ChangeStatus(journey.ID, to, env.owner.ID)
			require.NoError(t, err, "-> %s", to)
		}
		history := env.history(journey.ID)
		require.Len(t, history, 5)
		var tos []models.QuoteStatus
		var froms []string
		for i, row := range history {
			tos = append(tos, row.ToStatus)
			if row.FromStatus == nil {
				froms = append(froms, "<nil>")
			} else {
				froms = append(froms, string(*row.FromStatus))
			}
			if i > 0 {
				assert.False(t, row.ChangedAt.Before(history[i-1].ChangedAt), "row %d is not older than row %d", i, i-1)
				assert.Greater(t, row.ID, history[i-1].ID)
			}
			if assert.NotNil(t, row.ChangedBy, "the acting user is preloaded") {
				assert.Equal(t, env.owner.ID, row.ChangedBy.ID)
			}
		}
		assert.Equal(t, []models.QuoteStatus{"draft", "sent", "draft", "sent", "accepted"}, tos)
		assert.Equal(t, []string{"<nil>", "draft", "sent", "draft", "sent"}, froms)
		final := env.quote(journey.ID)
		assert.Equal(t, models.QuoteStatusAccepted, final.Status)
		require.NotNil(t, final.SentAt)
		require.NotNil(t, final.ClosedAt)
		assert.True(t, final.ClosedAt.After(*final.SentAt))
	})
}

// ---------------------------------------------------------------- AC-8 ------

// The create writes the quote and its first history row in one transaction: a
// failed history write leaves no quote behind.
func TestQuoteCreateWritesQuoteAndHistoryTogether(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)

	quote := env.newQuote(models.QuoteStatusDraft, 0)
	history := env.history(quote.ID)
	require.Len(t, history, 1)
	assert.Nil(t, history[0].FromStatus)
	assert.Equal(t, models.QuoteStatusDraft, history[0].ToStatus)
	assert.Equal(t, env.owner.ID, history[0].ChangedByID)
	assert.Equal(t, models.QuoteStatusDraft, quote.Status)
	assert.Zero(t, quote.TotalCents)

	var quotesBefore int64
	require.NoError(t, db.Model(&models.Quote{}).Count(&quotesBefore).Error)
	broken := env.service(&failingHistoryRepository{QuoteRepository: env.repo})
	err := broken.Create(&models.Quote{Title: "Half written", Currency: "EUR", CustomerID: &env.customer.ID, OwnerID: env.owner.ID}, env.owner.ID, models.RoleSales)
	require.Error(t, err)
	var quotesAfter int64
	require.NoError(t, db.Model(&models.Quote{}).Count(&quotesAfter).Error)
	assert.Equal(t, quotesBefore, quotesAfter, "no quote without its history row")
	var orphan int64
	require.NoError(t, db.Model(&models.Quote{}).Where("title = ?", "Half written").Count(&orphan).Error)
	assert.Zero(t, orphan)
}
