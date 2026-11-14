package integration

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// #213: a person who confirmed a create_ticket submission is stored in the
// submission (values, address, idempotency key), in the form_ticket_keys row
// of the ticket it opened, and in the customer it matched. Erasing the
// customer, its customer-role login, or a lead of the same address leaves no
// key row and no submission key linkable to the person, and the submitted
// value is found in no table, the tickets included.

const formTicketSweepSecret = "form-ticket-sweep-regression-secret-32ch"

type formTicketSweepMailer struct {
	mu     sync.Mutex
	bodies map[string][]string
}

func (m *formTicketSweepMailer) SendPasswordReset(string, string, string) error { return nil }

func (m *formTicketSweepMailer) Send(to, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bodies == nil {
		m.bodies = map[string][]string{}
	}
	m.bodies[to] = append(m.bodies[to], body)
	return nil
}

func (m *formTicketSweepMailer) token(t *testing.T, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	bodies := m.bodies[to]
	require.NotEmpty(t, bodies, "a confirmation mail went to %s", to)
	body := bodies[len(bodies)-1]
	index := strings.Index(body, "https://")
	require.GreaterOrEqual(t, index, 0)
	link := body[index:]
	if end := strings.IndexAny(link, " \n\t"); end >= 0 {
		link = link[:end]
	}
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	return parsed.Query().Get("token")
}

// openFormTicketSweepDB is the full schema on a file-backed SQLite database
// with the production one-connection pool, so the service's transactions and
// the sweep see the same database.
func openFormTicketSweepDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "sweep.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)
	return db
}

// formTicketSweepCase is one person with a confirmed ticket submission.
type formTicketSweepCase struct {
	db         *gorm.DB
	subject    erasureSubject
	customer   *models.Customer
	submission *models.FormSubmission
	ticket     *models.Ticket
	key        string
	value      string
}

// seedConfirmedTicketSubmission runs the real submission and confirmation of
// a create_ticket form for the subject's address.
func seedConfirmedTicketSubmission(t *testing.T, tag string, withLogin bool) *formTicketSweepCase {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formTicketSweepSecret)

	db := openFormTicketSweepDB(t)
	subject := newErasureSubject(tag)
	c := &formTicketSweepCase{
		db:      db,
		subject: subject,
		// A client key that carries the person, as a careless integrator's
		// would: the erasure must not leave it linkable.
		key:   "order-" + strings.ToLower(subject.LastName),
		value: "Textarea-canary-" + tag + ": the parcel arrived broken",
	}
	c.customer = subject.asCustomer(t, db)
	if withLogin {
		login := subject.asUser(t, db)
		require.NoError(t, db.Model(login).Update("role", models.RoleCustomer).Error)
		require.NoError(t, db.Model(c.customer).Update("user_id", login.ID).Error)
	}

	admin := &models.User{Email: "admin-" + tag + "@staff.test", Password: "x", FirstName: "Staff", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(admin).Error)
	mail := &formTicketSweepMailer{}
	txManager := utils.NewTransactionManager(db)
	formService := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db), repository.NewUserRepository(db),
		mail, txManager, config.FormsConfig{PublicBaseURL: "https://crm.example.test"}, "/api/v1",
		service.WithFormAutomationEvents(repository.NewAutomationRepository(db)),
		service.WithFormTicketIntake(repository.NewFormTicketRepository(db), txManager))

	form := &models.Form{Name: "Complaints " + tag, Status: models.FormStatusPublished, DoubleOptIn: true, CreateTicket: true,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			{Name: "complaint", Label: "Complaint", Type: models.FormFieldTextarea, Required: true},
		}}
	require.NoError(t, formService.Create(form, admin.ID))

	key := c.key
	_, err := formService.SubmitPublic(context.Background(), form.PublicID, &service.PublicSubmissionRequest{
		Values:         map[string]string{"email": subject.Email, "complaint": c.value},
		Challenge:      forms.NewChallenge([]byte(formTicketSweepSecret), time.Now().Add(-30*time.Second)),
		IdempotencyKey: &key,
	}, service.SubmissionMeta{IP: "203.0.113.9", UserAgent: "Sweep/1.0"})
	require.NoError(t, err)
	require.NoError(t, formService.ConfirmSubmission(context.Background(), mail.token(t, strings.ToLower(subject.Email))))

	var submission models.FormSubmission
	require.NoError(t, db.Where("form_id = ?", form.ID).Order("id desc").First(&submission).Error)
	require.Equal(t, models.FormTicketOutcomeCreated, submission.TicketOutcome)
	require.NotNil(t, submission.TicketID)
	c.submission = &submission
	var ticket models.Ticket
	require.NoError(t, db.First(&ticket, *submission.TicketID).Error)
	c.ticket = &ticket
	return c
}

func (c *formTicketSweepCase) linkNeedles() []string {
	return []string{c.value, c.key}
}

// assertSeeded proves the sweep sees the copies the erasure has to remove.
func (c *formTicketSweepCase) assertSeeded(t *testing.T) {
	t.Helper()
	tables := tablesHolding(t, c.db, c.linkNeedles())
	assert.Contains(t, tables, "form_submissions")
	assert.Contains(t, tables, "form_ticket_keys")
	assert.NotContains(t, tables, "tickets", "the ticket text holds no submitted value")
	var keys int64
	require.NoError(t, c.db.Model(&models.FormTicketKey{}).Count(&keys).Error)
	assert.EqualValues(t, 1, keys)
}

func (c *formTicketSweepCase) assertNothingLinkable(t *testing.T) {
	t.Helper()
	assertNoPersonalDataAnywhere(t, c.db, c.linkNeedles())
	var keys int64
	require.NoError(t, c.db.Model(&models.FormTicketKey{}).Count(&keys).Error)
	assert.Zero(t, keys, "no form_ticket_keys row survives")
	var stored models.FormSubmission
	require.NoError(t, c.db.Unscoped().First(&stored, c.submission.ID).Error)
	assert.Equal(t, "", stored.IdempotencyKey, "the submission keeps no key")
	var ticket models.Ticket
	require.NoError(t, c.db.Unscoped().First(&ticket, c.ticket.ID).Error)
	assert.Equal(t, c.ticket.Title, ticket.Title, "the ticket is a business record and stays")
}

func TestFormTicketIntakeErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	t.Run("customer", func(t *testing.T) {
		c := seedConfirmedTicketSubmission(t, "ftcust", false)
		c.assertSeeded(t)
		require.NoError(t, repository.NewCustomerRepository(c.db).Delete(c.customer.ID))
		c.assertNothingLinkable(t)
		assertNoPersonalDataAnywhere(t, c.db, append(c.subject.identifiers(), c.linkNeedles()...))
	})

	t.Run("customer-role login", func(t *testing.T) {
		c := seedConfirmedTicketSubmission(t, "ftlogin", true)
		c.assertSeeded(t)
		var login models.User
		require.NoError(t, c.db.Where("email = ?", c.subject.Email).First(&login).Error)
		require.NoError(t, repository.NewUserRepository(c.db).Delete(login.ID))
		c.assertNothingLinkable(t)
		assertNoPersonalDataAnywhere(t, c.db, append(c.subject.identifiers(), c.linkNeedles()...))
	})

	t.Run("lead of the same address", func(t *testing.T) {
		c := seedConfirmedTicketSubmission(t, "ftlead", false)
		c.assertSeeded(t)
		var owner models.User
		require.NoError(t, c.db.Where("role = ?", models.RoleAdmin).First(&owner).Error)
		lead := c.subject.asLead(t, c.db, owner.ID)
		require.NoError(t, repository.NewLeadRepository(c.db).Delete(lead.ID))
		// The customer is a separate record nobody asked to erase; what links
		// the submission and its key to the person is gone.
		c.assertNothingLinkable(t)
		hits := sweepDatabaseFor(t, c.db, []string{c.subject.Email})
		for _, hit := range hits {
			assert.Equal(t, "customers", hit.Table, "only the unerased customer still holds the address: %s", hit)
		}
		assert.Equal(t, fmt.Sprintf("Submitted through form %d, submission %d.", c.submission.FormID, c.submission.ID), c.ticket.Description)
	})
}
