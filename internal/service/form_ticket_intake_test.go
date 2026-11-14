package service

// #213 form ticket intake roots. They run the real form service over the real
// repositories on the full production schema (models.MigrateDatabase): a
// file-backed SQLite database with one connection, and a disposable MySQL
// schema (utf8mb4_unicode_ci, as the SQL migrations create customers) when
// TEST_MYSQL_DSN is set. Every assertion reads committed rows.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
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

var ticketIntakeSeq atomic.Uint64

const ticketIntakeWaitTimeout = 15 * time.Second

// openTicketIntakeDB opens the full production schema on SQLite or MySQL.
func openTicketIntakeDB(t *testing.T, useMySQL bool) *gorm.DB {
	t.Helper()
	cfg := &gorm.Config{Logger: logger.Discard}
	var db *gorm.DB
	if !useMySQL {
		dsn := filepath.Join(t.TempDir(), "ticket-intake.db") +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		var err error
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
		name := fmt.Sprintf("gocrm_ticket_intake_%d_%d", time.Now().UnixNano(), ticketIntakeSeq.Add(1))
		t.Cleanup(func() {
			_, _ = server.Exec("DROP DATABASE IF EXISTS `" + name + "`")
			_ = server.Close()
		})
		_, err = server.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
		require.NoError(t, err, "TEST_MYSQL_DSN is set but the server is unusable")
		dsnCfg.DBName = name
		db, err = gorm.Open(gormmysql.Open(dsnCfg.FormatDSN()), cfg)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(8)
		sqlDB.SetMaxIdleConns(8)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	previous := models.DB
	models.DB = db
	err := models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)
	return db
}

type ticketIntakeEnv struct {
	t        *testing.T
	db       *gorm.DB
	mailer   *fakeFormMailer
	formRepo repository.FormRepository
	svc      FormService
	owner    *models.User
}

func newTicketIntakeEnv(t *testing.T, db *gorm.DB, wired bool) *ticketIntakeEnv {
	t.Helper()
	if utils.Logger == nil {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	}
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formTestSecret)
	env := &ticketIntakeEnv{t: t, db: db, mailer: &fakeFormMailer{}, formRepo: repository.NewFormRepository(db)}
	env.owner = &models.User{Email: fmt.Sprintf("owner-%d@ticket-intake.test", ticketIntakeSeq.Add(1)), Password: "x",
		FirstName: "Own", LastName: "Er", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(env.owner).Error)
	env.svc = env.build(wired)
	return env
}

// build returns a new service instance on the same database; each instance
// has its own seam.
func (e *ticketIntakeEnv) build(wired bool) FormService {
	txManager := utils.NewTransactionManager(e.db)
	opts := []FormServiceOption{WithFormAutomationEvents(repository.NewAutomationRepository(e.db))}
	if wired {
		opts = append(opts, WithFormTicketIntake(repository.NewFormTicketRepository(e.db), txManager))
	}
	return NewFormService(e.formRepo, repository.NewLeadRepository(e.db), repository.NewUserRepository(e.db), e.mailer,
		txManager, config.FormsConfig{PublicBaseURL: formTestPublicBase}, formTestAPIPrefix, opts...)
}

func (e *ticketIntakeEnv) ticketForm(name string) *models.Form {
	e.t.Helper()
	form := &models.Form{
		Name:   name,
		Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			{Name: "complaint", Label: "Complaint", Type: models.FormFieldTextarea, Required: true},
		},
		NotifyEmails: []string{"staff@ticket-intake.test"},
		DoubleOptIn:  true,
		CreateTicket: true,
	}
	require.NoError(e.t, e.svc.Create(form, e.owner.ID))
	return form
}

func (e *ticketIntakeEnv) customer(email string) *models.Customer {
	e.t.Helper()
	customer := &models.Customer{FirstName: "Cus", LastName: "Tomer", Email: email}
	require.NoError(e.t, e.db.Create(customer).Error)
	return customer
}

func keyPtr(key string) *string { return &key }

func ticketRequest(email string, key *string, complaint string) *PublicSubmissionRequest {
	return &PublicSubmissionRequest{
		Values:         map[string]string{"email": email, "complaint": complaint},
		Challenge:      challengeAged(30 * time.Second),
		PageURL:        "https://customer.example/complaints",
		IdempotencyKey: key,
	}
}

// submit posts one submission through svc and returns the outcome and the
// raw token of the confirmation mail it produced.
func (e *ticketIntakeEnv) submit(svc FormService, form *models.Form, email string, key *string, complaint string) (*SubmitOutcome, string) {
	e.t.Helper()
	address := strings.ToLower(email)
	before := len(e.mailer.to(address))
	outcome, err := svc.SubmitPublic(context.Background(), form.PublicID, ticketRequest(email, key, complaint), submissionMeta())
	require.NoError(e.t, err)
	messages := e.mailer.to(address)
	require.Len(e.t, messages, before+1, "one confirmation mail")
	return outcome, tokenFromLink(e.t, messages[len(messages)-1].Body)
}

func (e *ticketIntakeEnv) latestSubmission(formID uint) *models.FormSubmission {
	e.t.Helper()
	var submission models.FormSubmission
	require.NoError(e.t, e.db.Where("form_id = ?", formID).Order("id desc").First(&submission).Error)
	return &submission
}

func (e *ticketIntakeEnv) submission(id uint) *models.FormSubmission {
	e.t.Helper()
	var submission models.FormSubmission
	require.NoError(e.t, e.db.Unscoped().First(&submission, id).Error)
	return &submission
}

func (e *ticketIntakeEnv) count(model interface{}, where string, args ...interface{}) int64 {
	e.t.Helper()
	var n int64
	query := e.db.Unscoped().Model(model)
	if where != "" {
		query = query.Where(where, args...)
	}
	require.NoError(e.t, query.Count(&n).Error)
	return n
}

func (e *ticketIntakeEnv) ticketsOf(customerID uint) []models.Ticket {
	e.t.Helper()
	var tickets []models.Ticket
	require.NoError(e.t, e.db.Unscoped().Where("customer_id = ?", customerID).Order("id").Find(&tickets).Error)
	return tickets
}

func (e *ticketIntakeEnv) keysOfForm(formID uint) []models.FormTicketKey {
	e.t.Helper()
	var keys []models.FormTicketKey
	require.NoError(e.t, e.db.Where("form_id = ?", formID).Order("id").Find(&keys).Error)
	return keys
}

func (e *ticketIntakeEnv) ticketEvents(ticketID uint) []models.AutomationEvent {
	e.t.Helper()
	var events []models.AutomationEvent
	require.NoError(e.t, e.db.Where("entity_type = ? AND entity_id = ? AND event_type = ?",
		models.AutomationEntityTicket, ticketID, models.AutomationEventTicketCreated).Find(&events).Error)
	return events
}

func (e *ticketIntakeEnv) tokenUsed(submissionID uint) bool {
	e.t.Helper()
	// An erasure hard-deletes the tokens; a missing token was never spent here.
	var tokens []models.FormConfirmationToken
	require.NoError(e.t, e.db.Unscoped().Where("submission_id = ?", submissionID).Order("id desc").Find(&tokens).Error)
	return len(tokens) > 0 && tokens[0].UsedAt != nil
}

// seam installs the test hook on one service instance.
func setTicketSeam(svc FormService, seam func(stage string) error) {
	svc.(*formService).ticketIntakeSeam = seam
}

func isDeadlock(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

// waitForStatement polls information_schema.PROCESSLIST until another
// connection of this schema runs a statement starting with prefix on two
// consecutive polls.
func waitForStatement(t *testing.T, db *gorm.DB, prefix string) {
	t.Helper()
	deadline := time.Now().Add(ticketIntakeWaitTimeout)
	consecutive := 0
	for time.Now().Before(deadline) {
		var seen int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND INFO LIKE ?`, prefix+"%").Scan(&seen).Error)
		if seen > 0 {
			consecutive++
			if consecutive >= 2 {
				return
			}
		} else {
			consecutive = 0
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no statement %q was seen waiting on two consecutive polls", prefix)
}

// ---------------------------------------------------------------- AC-5 ---

func TestFormTicketIntakeConfirmedSQLite(t *testing.T) {
	runFormTicketIntakeConfirmed(t, openTicketIntakeDB(t, false))
}

func TestFormTicketIntakeConfirmedMySQL(t *testing.T) {
	runFormTicketIntakeConfirmed(t, openTicketIntakeDB(t, true))
}

func runFormTicketIntakeConfirmed(t *testing.T, db *gorm.DB) {
	env := newTicketIntakeEnv(t, db, true)
	customer := env.customer("ana.confirmed@ticket-intake.test")
	form := env.ticketForm("Complaints desk")
	complaint := "Distinct-Complaint-Value-3141 about the invoice"

	_, token := env.submit(env.svc, form, "Ana.Confirmed@ticket-intake.test", keyPtr("Order-77"), complaint)
	pending := env.latestSubmission(form.ID)
	assert.Equal(t, models.FormSubmissionPending, pending.Status)
	assert.Equal(t, "Order-77", pending.IdempotencyKey)
	assert.Empty(t, env.ticketsOf(customer.ID), "no ticket before the confirmation")
	assert.Empty(t, env.keysOfForm(form.ID))

	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))

	tickets := env.ticketsOf(customer.ID)
	require.Len(t, tickets, 1, "exactly one ticket")
	ticket := tickets[0]
	assert.Equal(t, "Form: Complaints desk", ticket.Title)
	assert.Equal(t, fmt.Sprintf("Submitted through form %d, submission %d.", form.ID, pending.ID), ticket.Description)
	assert.Equal(t, models.TicketStatusOpen, ticket.Status)
	assert.Equal(t, models.TicketPriorityMedium, ticket.Priority)
	assert.Nil(t, ticket.AssignedToID)
	assert.Nil(t, ticket.DueAt)
	assert.Nil(t, ticket.FirstRespondedAt)
	assert.Equal(t, customer.ID, ticket.CustomerID)
	for _, text := range []string{ticket.Title, ticket.Description} {
		assert.NotContains(t, text, "Distinct-Complaint-Value")
		assert.NotContains(t, strings.ToLower(text), "ana.confirmed")
		assert.NotContains(t, text, "Order-77")
	}

	keys := env.keysOfForm(form.ID)
	require.Len(t, keys, 1)
	assert.Equal(t, "Order-77", keys[0].IdempotencyKey)
	assert.Equal(t, ticket.ID, keys[0].TicketID)
	assert.Equal(t, pending.ID, keys[0].SubmissionID)
	assert.Equal(t, customer.ID, keys[0].CustomerID)

	confirmed := env.submission(pending.ID)
	assert.Equal(t, models.FormSubmissionConfirmed, confirmed.Status)
	require.NotNil(t, confirmed.TicketID)
	assert.Equal(t, ticket.ID, *confirmed.TicketID)
	assert.Equal(t, models.FormTicketOutcomeCreated, confirmed.TicketOutcome)
	assert.Nil(t, confirmed.LeadID, "no lead")
	assert.True(t, env.tokenUsed(pending.ID))

	events := env.ticketEvents(ticket.ID)
	require.Len(t, events, 1)
	assert.Equal(t, models.AutomationOriginForm, events[0].Origin)
	assert.Nil(t, events[0].ActorID)
	assert.Equal(t, string(models.TicketStatusOpen), events[0].ToValue)

	// A long form name is cut to 255 runes.
	long := env.ticketForm(strings.Repeat("é", 254))
	_, token = env.submit(env.svc, long, "ana.confirmed@ticket-intake.test", nil, "x")
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	tickets = env.ticketsOf(customer.ID)
	require.Len(t, tickets, 2)
	assert.Equal(t, 255, len([]rune(tickets[1].Title)))
	assert.True(t, strings.HasPrefix(tickets[1].Title, "Form: "))
}

// ---------------------------------------------------------------- AC-6 ---

func TestFormTicketIntakePendingSpamAndKeyless(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	env := newTicketIntakeEnv(t, db, true)
	customer := env.customer("keyless@ticket-intake.test")
	form := env.ticketForm("Keyless complaints")

	// A pending submission stores the key and creates nothing.
	env.submit(env.svc, form, "keyless@ticket-intake.test", keyPtr("never-confirmed"), "pending only")
	pending := env.latestSubmission(form.ID)
	assert.Equal(t, "never-confirmed", pending.IdempotencyKey)
	assert.Equal(t, "", pending.TicketOutcome)
	assert.Empty(t, env.ticketsOf(customer.ID))
	assert.Empty(t, env.keysOfForm(form.ID))

	// Spam stores '' and creates nothing.
	spam := ticketRequest("keyless@ticket-intake.test", keyPtr("spam-key"), "spam")
	spam.Honeypot = "bot"
	_, err := env.svc.SubmitPublic(context.Background(), form.PublicID, spam, submissionMeta())
	require.NoError(t, err)
	spamRow := env.latestSubmission(form.ID)
	assert.Equal(t, models.FormSubmissionSpam, spamRow.Status)
	assert.Equal(t, "", spamRow.IdempotencyKey)
	assert.Empty(t, env.ticketsOf(customer.ID))
	assert.Empty(t, env.keysOfForm(form.ID))

	// A form without create_ticket ignores a sent key.
	plain := &models.Form{Name: "Plain", Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}}}
	require.NoError(t, env.svc.Create(plain, env.owner.ID))
	_, err = env.svc.SubmitPublic(context.Background(), plain.PublicID, &PublicSubmissionRequest{
		Values: map[string]string{"email": "keyless@ticket-intake.test"}, Challenge: challengeAged(30 * time.Second),
		IdempotencyKey: keyPtr("ignored-key"),
	}, submissionMeta())
	require.NoError(t, err)
	plainRow := env.latestSubmission(plain.ID)
	assert.Equal(t, models.FormSubmissionReceived, plainRow.Status)
	assert.Equal(t, "", plainRow.IdempotencyKey)
	assert.Equal(t, "", plainRow.TicketOutcome)
	assert.Nil(t, plainRow.TicketID)
	// An opt-in form without create_ticket ignores it too.
	optIn := &models.Form{Name: "Opt-in plain", Status: models.FormStatusPublished, DoubleOptIn: true,
		Fields: []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}}}
	require.NoError(t, env.svc.Create(optIn, env.owner.ID))
	_, err = env.svc.SubmitPublic(context.Background(), optIn.PublicID, &PublicSubmissionRequest{
		Values: map[string]string{"email": "keyless@ticket-intake.test"}, Challenge: challengeAged(30 * time.Second),
		IdempotencyKey: keyPtr("ignored-too"),
	}, submissionMeta())
	require.NoError(t, err)
	assert.Equal(t, "", env.latestSubmission(optIn.ID).IdempotencyKey)

	// A keyless confirmation uses submission-<id>.
	_, token := env.submit(env.svc, form, "keyless@ticket-intake.test", nil, "first keyless")
	first := env.latestSubmission(form.ID)
	assert.Equal(t, "", first.IdempotencyKey)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	keys := env.keysOfForm(form.ID)
	require.Len(t, keys, 1)
	assert.Equal(t, fmt.Sprintf("submission-%d", first.ID), keys[0].IdempotencyKey)
	assert.Equal(t, models.FormTicketOutcomeCreated, env.submission(first.ID).TicketOutcome)

	// A second keyless submission after the first was confirmed gets its own
	// pending row and, once confirmed, its own ticket.
	_, token = env.submit(env.svc, form, "keyless@ticket-intake.test", nil, "second keyless")
	second := env.latestSubmission(form.ID)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, models.FormSubmissionPending, second.Status)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	assert.Len(t, env.ticketsOf(customer.ID), 2)
	keys = env.keysOfForm(form.ID)
	require.Len(t, keys, 2)
	assert.Equal(t, fmt.Sprintf("submission-%d", second.ID), keys[1].IdempotencyKey)

	// The never-confirmed pending row still created nothing.
	assert.Equal(t, models.FormSubmissionPending, env.submission(pending.ID).Status)
	assert.Equal(t, "", env.submission(pending.ID).TicketOutcome)

	// The service refuses a malformed key from any caller, without echoing it.
	_, err = env.svc.SubmitPublic(context.Background(), form.PublicID, ticketRequest("keyless@ticket-intake.test", keyPtr("submission-9"), "x"), submissionMeta())
	var fieldErrors FieldErrors
	require.True(t, errors.As(err, &fieldErrors))
	assert.Contains(t, fieldErrors, "idempotency_key")
	assert.NotContains(t, err.Error(), "submission-9")
}

// ---------------------------------------------------------------- AC-7 ---

func TestFormTicketIntakeDuplicateKeySQLite(t *testing.T) {
	runFormTicketIntakeDuplicateKey(t, openTicketIntakeDB(t, false))
}

func TestFormTicketIntakeDuplicateKeyMySQL(t *testing.T) {
	runFormTicketIntakeDuplicateKey(t, openTicketIntakeDB(t, true))
}

func runFormTicketIntakeDuplicateKey(t *testing.T, db *gorm.DB) {
	env := newTicketIntakeEnv(t, db, true)
	customer := env.customer("repeat@ticket-intake.test")
	form := env.ticketForm("Repeat complaints")

	firstOutcome, token := env.submit(env.svc, form, "repeat@ticket-intake.test", keyPtr("K-dup"), "first try")
	first := env.latestSubmission(form.ID)
	firstConfirm := env.svc.ConfirmSubmission(context.Background(), token)
	require.NoError(t, firstConfirm)

	secondOutcome, token := env.submit(env.svc, form, "repeat@ticket-intake.test", keyPtr("K-dup"), "retry")
	second := env.latestSubmission(form.ID)
	secondConfirm := env.svc.ConfirmSubmission(context.Background(), token)

	assert.Equal(t, firstOutcome, secondOutcome, "SubmitPublic answers exactly as for a first submission")
	assert.Equal(t, firstConfirm, secondConfirm, "ConfirmSubmission answers the same")

	tickets := env.ticketsOf(customer.ID)
	require.Len(t, tickets, 1, "no second ticket")
	assert.Len(t, env.keysOfForm(form.ID), 1)

	stored := env.submission(second.ID)
	assert.Equal(t, models.FormSubmissionConfirmed, stored.Status)
	require.NotNil(t, stored.TicketID)
	assert.Equal(t, tickets[0].ID, *stored.TicketID)
	assert.Equal(t, models.FormTicketOutcomeDuplicate, stored.TicketOutcome)
	assert.Equal(t, "K-dup", stored.IdempotencyKey)
	assert.Equal(t, models.FormTicketOutcomeCreated, env.submission(first.ID).TicketOutcome)
	assert.Len(t, env.ticketEvents(tickets[0].ID), 1, "a duplicate records no event")
}

// ---------------------------------------------------------------- AC-8 ---

func TestFormTicketIntakeKeyConflictSQLite(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	env := newTicketIntakeEnv(t, db, true)
	first := env.customer("first.conflict@ticket-intake.test")
	second := env.customer("second.conflict@ticket-intake.test")
	form := env.ticketForm("Conflict complaints")

	firstOutcome, firstToken := env.submit(env.svc, form, "first.conflict@ticket-intake.test", keyPtr("shared-key"), "a")
	firstRow := env.latestSubmission(form.ID)
	secondOutcome, secondToken := env.submit(env.svc, form, "second.conflict@ticket-intake.test", keyPtr("shared-key"), "b")
	secondRow := env.latestSubmission(form.ID)
	assert.Equal(t, firstOutcome, secondOutcome)

	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), firstToken))
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), secondToken), "the public answer is unchanged")

	assertKeyConflict(t, env, form, first, second, firstRow, secondRow)
}

func assertKeyConflict(t *testing.T, env *ticketIntakeEnv, form *models.Form, winner, loser *models.Customer, winnerRow, loserRow *models.FormSubmission) {
	t.Helper()
	assert.Len(t, env.ticketsOf(winner.ID), 1)
	assert.Empty(t, env.ticketsOf(loser.ID), "the loser's ticket insert was rolled back")
	assert.EqualValues(t, 1, env.count(&models.Ticket{}, ""), "exactly one ticket")
	keys := env.keysOfForm(form.ID)
	require.Len(t, keys, 1, "exactly one key row")
	assert.Equal(t, winner.ID, keys[0].CustomerID)

	stored := env.submission(winnerRow.ID)
	assert.Equal(t, models.FormTicketOutcomeCreated, stored.TicketOutcome)
	conflict := env.submission(loserRow.ID)
	assert.Equal(t, models.FormSubmissionConfirmed, conflict.Status)
	assert.Equal(t, models.FormTicketOutcomeKeyConflict, conflict.TicketOutcome)
	assert.Nil(t, conflict.TicketID)
}

func TestFormTicketIntakeKeyConflictMySQL(t *testing.T) {
	db := openTicketIntakeDB(t, true)
	env := newTicketIntakeEnv(t, db, true)
	first := env.customer("first.race@ticket-intake.test")
	second := env.customer("second.race@ticket-intake.test")
	form := env.ticketForm("Racing complaints")

	parkedSvc := env.build(true)
	_, firstToken := env.submit(parkedSvc, form, "first.race@ticket-intake.test", keyPtr("race-key"), "a")
	firstRow := env.latestSubmission(form.ID)
	_, secondToken := env.submit(env.svc, form, "second.race@ticket-intake.test", keyPtr("race-key"), "b")
	secondRow := env.latestSubmission(form.ID)

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	setTicketSeam(parkedSvc, func(stage string) error {
		if stage == formTicketStageAfterKey {
			once.Do(func() {
				close(parked)
				<-release
			})
		}
		return nil
	})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	firstDone := make(chan error, 1)
	go func() { firstDone <- parkedSvc.ConfirmSubmission(context.Background(), firstToken) }()
	select {
	case <-parked:
	case <-time.After(ticketIntakeWaitTimeout):
		t.Fatal("the first confirmation never reached its key insert")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- env.svc.ConfirmSubmission(context.Background(), secondToken) }()
	waitForStatement(t, db, "INSERT INTO `form_ticket_keys`")

	releaseOnce.Do(func() { close(release) })
	firstErr := <-firstDone
	secondErr := <-secondDone
	require.NoError(t, firstErr)
	require.NoError(t, secondErr, "the waiting insert gets ErrFormTicketKeyTaken, not 1213")
	assert.False(t, isDeadlock(secondErr))

	assertKeyConflict(t, env, form, first, second, firstRow, secondRow)
}

// ---------------------------------------------------------------- AC-9 ---

func TestFormTicketIntakeNoCustomer(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	env := newTicketIntakeEnv(t, db, true)
	known := env.customer("known@ticket-intake.test")
	form := env.ticketForm("Unknown complaints")

	knownOutcome, knownToken := env.submit(env.svc, form, "known@ticket-intake.test", keyPtr("known-key"), "a")
	knownErr := env.svc.ConfirmSubmission(context.Background(), knownToken)
	require.NoError(t, knownErr)

	unknownOutcome, token := env.submit(env.svc, form, "stranger@ticket-intake.test", keyPtr("stranger-key"), "b")
	row := env.latestSubmission(form.ID)
	unknownErr := env.svc.ConfirmSubmission(context.Background(), token)
	assert.Equal(t, knownOutcome, unknownOutcome, "the submit answer is unchanged")
	assert.Equal(t, knownErr, unknownErr, "the confirmation answer is unchanged")

	stored := env.submission(row.ID)
	assert.Equal(t, models.FormSubmissionConfirmed, stored.Status)
	assert.Equal(t, models.FormTicketOutcomeNoCustomer, stored.TicketOutcome)
	assert.Nil(t, stored.TicketID)
	assert.Len(t, env.ticketsOf(known.ID), 1)
	assert.EqualValues(t, 1, env.count(&models.Ticket{}, ""), "no ticket for the stranger")
	assert.Len(t, env.keysOfForm(form.ID), 1, "no key row for the stranger")
}

func TestFormTicketIntakeUnwired(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	env := newTicketIntakeEnv(t, db, false)
	customer := env.customer("unwired@ticket-intake.test")
	form := env.ticketForm("Unwired complaints")

	_, token := env.submit(env.svc, form, "unwired@ticket-intake.test", keyPtr("unwired-key"), "a")
	row := env.latestSubmission(form.ID)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token), "the public confirmation still succeeds")

	stored := env.submission(row.ID)
	assert.Equal(t, models.FormSubmissionConfirmed, stored.Status)
	assert.Equal(t, models.FormTicketOutcomeUnavailable, stored.TicketOutcome)
	assert.Nil(t, stored.TicketID)
	assert.True(t, env.tokenUsed(row.ID), "the claim committed")
	assert.Empty(t, env.ticketsOf(customer.ID))
	assert.Empty(t, env.keysOfForm(form.ID))
}

// --------------------------------------------------------------- AC-10 ---

func TestFormTicketIntakeAtomicitySQLite(t *testing.T) {
	runFormTicketIntakeAtomicity(t, openTicketIntakeDB(t, false))
}

func TestFormTicketIntakeAtomicityMySQL(t *testing.T) {
	runFormTicketIntakeAtomicity(t, openTicketIntakeDB(t, true))
}

func runFormTicketIntakeAtomicity(t *testing.T, db *gorm.DB) {
	env := newTicketIntakeEnv(t, db, true)
	customer := env.customer("atomic@ticket-intake.test")
	form := env.ticketForm("Atomic complaints")

	for i, stage := range []string{formTicketStageAfterKey, formTicketStageAfterEvent, formTicketStageAfterSubmission, formTicketStageAfterMail} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			_, token := env.submit(env.svc, form, "atomic@ticket-intake.test", keyPtr(fmt.Sprintf("atomic-%d", i)), "x")
			row := env.latestSubmission(form.ID)
			ticketsBefore := env.count(&models.Ticket{}, "")
			keysBefore := env.count(&models.FormTicketKey{}, "")
			eventsBefore := env.count(&models.AutomationEvent{}, "entity_type = ?", models.AutomationEntityTicket)
			mailBefore := env.count(&models.FormMailDelivery{}, "submission_id = ?", row.ID)
			sentBefore := len(env.mailer.to("staff@ticket-intake.test"))

			reached := false
			setTicketSeam(env.svc, func(s string) error {
				if s == stage {
					reached = true
					return errors.New("injected fault")
				}
				return nil
			})
			err := env.svc.ConfirmSubmission(context.Background(), token)
			setTicketSeam(env.svc, nil)
			require.Error(t, err)
			require.True(t, reached, "the fault fired at %s", stage)

			assert.Equal(t, ticketsBefore, env.count(&models.Ticket{}, ""), "the ticket rolled back")
			assert.Equal(t, keysBefore, env.count(&models.FormTicketKey{}, ""), "the key row rolled back")
			assert.Equal(t, eventsBefore, env.count(&models.AutomationEvent{}, "entity_type = ?", models.AutomationEntityTicket), "the event rolled back")
			assert.Equal(t, mailBefore, env.count(&models.FormMailDelivery{}, "submission_id = ?", row.ID), "the mail delivery rows rolled back")
			assert.Equal(t, sentBefore, len(env.mailer.to("staff@ticket-intake.test")), "no mail went out")
			stored := env.submission(row.ID)
			assert.Equal(t, models.FormSubmissionPending, stored.Status, "the confirmation rolled back")
			assert.Nil(t, stored.ConfirmedAt)
			assert.Equal(t, "", stored.TicketOutcome)
			assert.Nil(t, stored.TicketID)
			assert.False(t, env.tokenUsed(row.ID), "the token claim rolled back")

			// The same link confirms once the fault is gone.
			require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
			stored = env.submission(row.ID)
			assert.Equal(t, models.FormTicketOutcomeCreated, stored.TicketOutcome)
			assert.Equal(t, ticketsBefore+1, env.count(&models.Ticket{}, ""))
			assert.Equal(t, keysBefore+1, env.count(&models.FormTicketKey{}, ""))
			assert.Greater(t, env.count(&models.FormMailDelivery{}, "submission_id = ?", row.ID), mailBefore)
		})
	}
	assert.Len(t, env.ticketsOf(customer.ID), 4)
}

// --------------------------------------------------------------- AC-11 ---

// assertNoKeyOfErasedCustomer is the invariant: no committed key row names the
// erased customer or one of its tickets.
func assertNoKeyOfErasedCustomer(t *testing.T, env *ticketIntakeEnv, customerID uint) {
	t.Helper()
	assert.EqualValues(t, 0, env.count(&models.FormTicketKey{}, "customer_id = ?", customerID), "no key row names the erased customer")
	var ticketIDs []uint
	require.NoError(t, env.db.Unscoped().Model(&models.Ticket{}).Where("customer_id = ?", customerID).Pluck("id", &ticketIDs).Error)
	if len(ticketIDs) > 0 {
		assert.EqualValues(t, 0, env.count(&models.FormTicketKey{}, "ticket_id IN ?", ticketIDs), "no key row names a ticket of the erased customer")
	}
}

func customerErased(t *testing.T, env *ticketIntakeEnv, id uint) bool {
	t.Helper()
	var customer models.Customer
	require.NoError(t, env.db.Unscoped().First(&customer, id).Error)
	return customer.DeletedAt.Valid
}

func TestFormTicketIntakeVsCustomerErasureSQLite(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	env := newTicketIntakeEnv(t, db, true)
	customers := repository.NewCustomerRepository(db)
	form := env.ticketForm("Erasure complaints")

	t.Run("confirmation first", func(t *testing.T) {
		customer := env.customer("confirm.first@ticket-intake.test")
		_, token := env.submit(env.svc, form, "confirm.first@ticket-intake.test", keyPtr("erase-1"), "x")
		row := env.latestSubmission(form.ID)
		require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
		require.EqualValues(t, 1, env.count(&models.FormTicketKey{}, "customer_id = ?", customer.ID))

		require.NoError(t, customers.Delete(customer.ID))
		assert.True(t, customerErased(t, env, customer.ID))
		assertNoKeyOfErasedCustomer(t, env, customer.ID)
		assert.Equal(t, "", env.submission(row.ID).IdempotencyKey, "the submission's key is scrubbed")
	})

	t.Run("erasure first", func(t *testing.T) {
		customer := env.customer("erase.first@ticket-intake.test")
		_, token := env.submit(env.svc, form, "erase.first@ticket-intake.test", keyPtr("erase-2"), "x")
		row := env.latestSubmission(form.ID)
		require.NoError(t, customers.Delete(customer.ID))

		err := env.svc.ConfirmSubmission(context.Background(), token)
		if err != nil {
			assert.True(t, errors.Is(err, ErrInvalidConfirmationToken), "an erased address's link is invalid: %v", err)
		} else {
			assert.Equal(t, models.FormTicketOutcomeNoCustomer, env.submission(row.ID).TicketOutcome)
		}
		assert.Empty(t, env.ticketsOf(customer.ID))
		assertNoKeyOfErasedCustomer(t, env, customer.ID)
	})
}

func TestFormTicketIntakeVsCustomerErasureMySQL(t *testing.T) {
	db := openTicketIntakeDB(t, true)
	env := newTicketIntakeEnv(t, db, true)
	customers := repository.NewCustomerRepository(db)
	form := env.ticketForm("Erasure race complaints")

	t.Run("confirmation holds the customer, erasure waits", func(t *testing.T) {
		customer := env.customer("race.confirm@ticket-intake.test")
		parkedSvc := env.build(true)
		_, token := env.submit(parkedSvc, form, "race.confirm@ticket-intake.test", keyPtr("race-erase-1"), "x")
		row := env.latestSubmission(form.ID)

		parked := make(chan struct{})
		release := make(chan struct{})
		var once, releaseOnce sync.Once
		setTicketSeam(parkedSvc, func(stage string) error {
			if stage == formTicketStageAfterKey {
				once.Do(func() {
					close(parked)
					<-release
				})
			}
			return nil
		})
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		confirmDone := make(chan error, 1)
		go func() { confirmDone <- parkedSvc.ConfirmSubmission(context.Background(), token) }()
		select {
		case <-parked:
		case <-time.After(ticketIntakeWaitTimeout):
			t.Fatal("the confirmation never reached its key insert")
		}

		eraseDone := make(chan error, 1)
		go func() { eraseDone <- customers.Delete(customer.ID) }()
		waitForStatement(t, db, "UPDATE `customers`")

		releaseOnce.Do(func() { close(release) })
		confirmErr := <-confirmDone
		eraseErr := <-eraseDone

		if confirmErr != nil {
			require.True(t, isDeadlock(confirmErr), "only a 1213 is an expected confirmation failure: %v", confirmErr)
			assert.False(t, env.tokenUsed(row.ID), "the rolled-back confirmation wrote nothing")
			assert.Empty(t, env.ticketsOf(customer.ID))
		}
		if eraseErr != nil {
			require.True(t, isDeadlock(eraseErr), "only a 1213 is an expected erasure failure: %v", eraseErr)
			assert.False(t, customerErased(t, env, customer.ID), "the rolled-back erasure wrote nothing")
			var stored models.Customer
			require.NoError(t, db.First(&stored, customer.ID).Error)
			assert.Equal(t, "race.confirm@ticket-intake.test", stored.Email)
		} else {
			assertNoKeyOfErasedCustomer(t, env, customer.ID)
		}
		if confirmErr == nil && eraseErr == nil {
			assert.Len(t, env.ticketsOf(customer.ID), 1, "both committed: the ticket stays, its key row is purged")
		}
	})

	t.Run("erasure holds the customer and tokens, confirmation waits", func(t *testing.T) {
		customer := env.customer("race.erase@ticket-intake.test")
		_, token := env.submit(env.svc, form, "race.erase@ticket-intake.test", keyPtr("race-erase-2"), "x")
		row := env.latestSubmission(form.ID)

		erased := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		eraseDone := make(chan error, 1)
		go func() {
			eraseDone <- db.Transaction(func(tx *gorm.DB) error {
				if err := customers.WithTx(tx).Delete(customer.ID); err != nil {
					return err
				}
				close(erased)
				<-release
				return nil
			})
		}()
		select {
		case <-erased:
		case err := <-eraseDone:
			t.Fatalf("the erasure ended before parking: %v", err)
		case <-time.After(ticketIntakeWaitTimeout):
			t.Fatal("the erasure never finished its writes")
		}

		confirmDone := make(chan error, 1)
		go func() { confirmDone <- env.svc.ConfirmSubmission(context.Background(), token) }()
		waitForStatement(t, db, "UPDATE `form_confirmation_tokens`")

		releaseOnce.Do(func() { close(release) })
		eraseErr := <-eraseDone
		confirmErr := <-confirmDone

		if eraseErr != nil {
			require.True(t, isDeadlock(eraseErr), "only a 1213 is an expected erasure failure: %v", eraseErr)
			assert.False(t, customerErased(t, env, customer.ID), "the rolled-back erasure wrote nothing")
		} else {
			assert.True(t, customerErased(t, env, customer.ID))
		}
		if confirmErr != nil {
			if isDeadlock(confirmErr) {
				assert.Empty(t, env.ticketsOf(customer.ID), "the rolled-back confirmation wrote nothing")
			} else {
				assert.True(t, errors.Is(confirmErr, ErrInvalidConfirmationToken), "the erased address's link is invalid: %v", confirmErr)
			}
		} else if eraseErr == nil {
			assert.Equal(t, models.FormTicketOutcomeNoCustomer, env.submission(row.ID).TicketOutcome)
		}
		if eraseErr == nil {
			assert.Empty(t, env.ticketsOf(customer.ID), "no ticket was opened for the erased customer")
		}
		assertNoKeyOfErasedCustomer(t, env, customer.ID)
	})
}

// --------------------------------------------------------------- AC-12 ---

type ticketIntakeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *ticketIntakeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *ticketIntakeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFormTicketIntakeLogsOmitPersonalData(t *testing.T) {
	db := openTicketIntakeDB(t, false)
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	logs := &ticketIntakeLogBuffer{}
	utils.Logger.SetOutput(logs)
	t.Cleanup(func() {
		utils.Logger.SetOutput(os.Stdout)
		_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	})

	env := newTicketIntakeEnv(t, db, true)
	const (
		formName  = "Canary-Form-Name-9001"
		email     = "canary.person@ticket-intake.test"
		other     = "canary.other@ticket-intake.test"
		stranger  = "canary.stranger@ticket-intake.test"
		key       = "Canary-Key-77"
		complaint = "Canary-Complaint-Value-4242"
	)
	env.customer(email)
	env.customer(other)
	form := env.ticketForm(formName)

	// created, duplicate, key_conflict, no_customer, a refused key and a
	// rolled-back confirmation.
	_, token := env.submit(env.svc, form, email, keyPtr(key), complaint)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	_, token = env.submit(env.svc, form, email, keyPtr(key), complaint)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	_, token = env.submit(env.svc, form, other, keyPtr(key), complaint)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	_, token = env.submit(env.svc, form, stranger, keyPtr(key+"-s"), complaint)
	require.NoError(t, env.svc.ConfirmSubmission(context.Background(), token))
	_, err := env.svc.SubmitPublic(context.Background(), form.PublicID, ticketRequest(email, keyPtr("submission-"+key), complaint), submissionMeta())
	require.Error(t, err)
	_, token = env.submit(env.svc, form, email, keyPtr(key+"-fault"), complaint)
	setTicketSeam(env.svc, func(stage string) error {
		if stage == formTicketStageAfterEvent {
			return errors.New("injected fault")
		}
		return nil
	})
	require.Error(t, env.svc.ConfirmSubmission(context.Background(), token))
	setTicketSeam(env.svc, nil)

	var ticket models.Ticket
	require.NoError(t, db.Order("id").First(&ticket).Error)

	text := logs.String()
	require.Contains(t, text, "ticket_outcome", "the intake logs its outcome")
	require.Contains(t, text, models.FormTicketOutcomeDuplicate)
	require.Contains(t, text, models.FormTicketOutcomeKeyConflict)
	require.Contains(t, text, models.FormTicketOutcomeNoCustomer)
	for _, forbidden := range []string{
		key, email, other, stranger, complaint, formName,
		strings.ToLower(key), strings.ToLower(formName),
		ticket.Title, ticket.Description,
	} {
		assert.NotContains(t, text, forbidden, "a log line carries %q", forbidden)
	}
}
