package service

// Regression roots for issue #104: converting a lead preserves its company
// link and enforces the administrator's lead-conversion settings
// (leads.conversion.allowed_statuses, require_notes, auto_assign_owner) on the
// server, before anything is written. Every root runs the real service over
// the real repositories and the real configuration service, on in-memory
// SQLite with the production one-connection pool and on a disposable MySQL
// schema when TEST_MYSQL_DSN is set, and every assertion reads committed rows.

import (
	"errors"
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLeadConversionRulesSQLite(t *testing.T) {
	runLeadConversionRuleScenarios(t, openQuoteTestSQLite(t))
}

func TestLeadConversionRulesMySQL(t *testing.T) {
	runLeadConversionRuleScenarios(t, openQuoteTestMySQL(t, 2))
}

// errConvertWriteFailed is the failure injected into the lead write that
// follows the customer insert.
var errConvertWriteFailed = errors.New("injected lead write failure")

// failingConvertLeadRepo makes the second write of a conversion — marking
// the lead converted — fail, after the customer row has been inserted in the
// same transaction.
type failingConvertLeadRepo struct {
	repository.LeadRepository
}

func (r failingConvertLeadRepo) WithTx(tx *gorm.DB) repository.LeadRepository {
	return failingConvertLeadRepo{r.LeadRepository.WithTx(tx)}
}

func (r failingConvertLeadRepo) ConvertToCustomer(uint, uint) error {
	return errConvertWriteFailed
}

type leadConversionEnv struct {
	t        *testing.T
	db       *gorm.DB
	config   ConfigurationService
	leads    repository.LeadRepository
	svc      LeadService
	failing  LeadService
	owner    models.User
	company  models.Company
	sequence *int
}

// bind returns the environment reporting to the subtest t, so a failed
// helper assertion stops the subtest and not its parent.
func (e *leadConversionEnv) bind(t *testing.T) *leadConversionEnv {
	bound := *e
	bound.t = t
	return &bound
}

func newLeadConversionEnv(t *testing.T, db *gorm.DB) *leadConversionEnv {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&models.Configuration{}))
	configRepo := repository.NewConfigurationRepository(db)
	require.NoError(t, configRepo.InitializeDefaults())
	config := NewConfigurationService(configRepo, nil)

	leads := repository.NewLeadRepository(db)
	customers := repository.NewCustomerRepository(db)
	companies := repository.NewCompanyRepository(db)
	txManager := utils.NewTransactionManager(db)

	env := &leadConversionEnv{
		t:      t,
		db:     db,
		config: config,
		leads:  leads,
		svc:    NewLeadService(leads, customers, companies, txManager, WithLeadConversionRules(config)),
		failing: NewLeadService(failingConvertLeadRepo{leads}, customers, companies, txManager,
			WithLeadConversionRules(config)),
		sequence: new(int),
	}
	env.owner = models.User{Email: "conversion-owner@example.com", Password: "x", FirstName: "Sam", LastName: "Seller", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(&env.owner).Error)
	env.company = models.Company{Name: "Acme Holdings", Domain: "acme-holdings.example"}
	require.NoError(t, db.Create(&env.company).Error)
	return env
}

// newLead stores a lead with the given status, owned by env.owner and linked
// to companyID (nil = no link).
func (e *leadConversionEnv) newLead(status models.LeadStatus, companyID *uint) *models.Lead {
	e.t.Helper()
	*e.sequence++
	lead := &models.Lead{
		FirstName: "Lee",
		LastName:  fmt.Sprintf("Prospect%d", *e.sequence),
		Email:     fmt.Sprintf("lead-%d@example.com", *e.sequence),
		Company:   "Acme (free text)",
		Status:    status,
		OwnerID:   e.owner.ID,
		CompanyID: companyID,
	}
	require.NoError(e.t, e.db.Create(lead).Error)
	return lead
}

func (e *leadConversionEnv) set(key string, value interface{}) {
	e.t.Helper()
	require.NoError(e.t, e.config.Set(key, value))
}

func (e *leadConversionEnv) reset() {
	e.t.Helper()
	e.set("leads.conversion.allowed_statuses", []interface{}{"qualified", "contacted"})
	e.set("leads.conversion.require_notes", false)
	e.set("leads.conversion.auto_assign_owner", true)
}

// committedLead re-reads the lead outside any transaction.
func (e *leadConversionEnv) committedLead(id uint) models.Lead {
	e.t.Helper()
	var lead models.Lead
	require.NoError(e.t, e.db.First(&lead, id).Error)
	return lead
}

// customersWithEmail counts committed customer rows, soft-deleted included.
func (e *leadConversionEnv) customersWithEmail(email string) int64 {
	e.t.Helper()
	var count int64
	require.NoError(e.t, e.db.Unscoped().Model(&models.Customer{}).Where("email = ?", email).Count(&count).Error)
	return count
}

func (e *leadConversionEnv) committedCustomer(id uint) models.Customer {
	e.t.Helper()
	var customer models.Customer
	require.NoError(e.t, e.db.First(&customer, id).Error)
	return customer
}

// assertRefused checks that a rule refusal is a validation error naming the
// field, and that nothing was written.
func (e *leadConversionEnv) assertRefused(err error, field string, lead *models.Lead) {
	e.t.Helper()
	require.Error(e.t, err)
	assert.True(e.t, errors.Is(err, apperrors.ErrValidation), "a rule refusal is a validation error, got %v", err)
	var ruleErr *LeadConversionRuleError
	require.True(e.t, errors.As(err, &ruleErr), "a rule refusal carries LeadConversionRuleError, got %v", err)
	assert.Equal(e.t, field, ruleErr.Field)
	assert.Zero(e.t, e.customersWithEmail(lead.Email), "a refused conversion writes no customer")
	stored := e.committedLead(lead.ID)
	assert.Equal(e.t, lead.Status, stored.Status, "a refused conversion leaves the lead status alone")
	assert.Nil(e.t, stored.CustomerID)
}

func runLeadConversionRuleScenarios(t *testing.T, db *gorm.DB) {
	root := newLeadConversionEnv(t, db)

	t.Run("seeded defaults allow qualified and contacted and refuse new", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusNew, nil)
		_, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		env.assertRefused(err, "status", lead)
		var ruleErr *LeadConversionRuleError
		require.True(t, errors.As(err, &ruleErr))
		assert.Equal(t, []string{"qualified", "contacted"}, ruleErr.AllowedStatuses)

		for _, status := range []models.LeadStatus{models.LeadStatusQualified, models.LeadStatusContacted} {
			lead := env.newLead(status, nil)
			customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
			require.NoError(t, err, "status %s is allowed by default", status)
			assert.NotZero(t, customer.ID)
		}
	})

	t.Run("an administrator's allowed statuses replace the defaults", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		env.set("leads.conversion.allowed_statuses", []interface{}{"unqualified"})

		qualified := env.newLead(models.LeadStatusQualified, nil)
		_, err := env.svc.ConvertToCustomer(qualified.ID, &models.Customer{})
		env.assertRefused(err, "status", qualified)

		unqualified := env.newLead(models.LeadStatusUnqualified, nil)
		customer, err := env.svc.ConvertToCustomer(unqualified.ID, &models.Customer{})
		require.NoError(t, err)
		assert.Equal(t, models.LeadStatusConverted, env.committedLead(unqualified.ID).Status)
		assert.Equal(t, unqualified.Email, env.committedCustomer(customer.ID).Email)
	})

	t.Run("required notes refuse a blank note and keep a given one", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		env.set("leads.conversion.require_notes", true)

		for _, notes := range []string{"", "   \n\t"} {
			lead := env.newLead(models.LeadStatusQualified, nil)
			_, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{Notes: notes})
			env.assertRefused(err, "notes", lead)
		}

		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{Notes: "Signed the pilot order"})
		require.NoError(t, err)
		assert.Equal(t, "Signed the pilot order", env.committedCustomer(customer.ID).Notes)
	})

	t.Run("notes are optional when not required", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		assert.Empty(t, env.committedCustomer(customer.ID).Notes)
	})

	t.Run("owner assignment sets assigned_to_id and never user_id", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		stored := env.committedCustomer(customer.ID)
		require.NotNil(t, stored.AssignedToID, "auto_assign_owner is on by default")
		assert.Equal(t, env.owner.ID, *stored.AssignedToID)
		assert.Nil(t, stored.UserID, "user_id is the customer's own login, never the lead owner")
	})

	t.Run("disabled owner assignment leaves assigned_to_id unset", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		env.set("leads.conversion.auto_assign_owner", false)
		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		stored := env.committedCustomer(customer.ID)
		assert.Nil(t, stored.AssignedToID)
		assert.Nil(t, stored.UserID)
	})

	t.Run("the company link is copied to the customer", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		companyID := env.company.ID
		lead := env.newLead(models.LeadStatusQualified, &companyID)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		stored := env.committedCustomer(customer.ID)
		require.NotNil(t, stored.CompanyID)
		assert.Equal(t, env.company.ID, *stored.CompanyID)
		assert.Equal(t, "Acme (free text)", stored.Company, "the free-text company is still copied")
	})

	t.Run("a lead without a company link converts without one", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		customer, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)
		assert.Nil(t, env.committedCustomer(customer.ID).CompanyID)
	})

	t.Run("a stale company link is dropped and the conversion succeeds", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		gone := models.Company{Name: "Gone Ltd", Domain: "gone.example"}
		require.NoError(t, db.Create(&gone).Error)
		goneID := gone.ID
		lead := env.newLead(models.LeadStatusQualified, &goneID)
		// Soft-delete the company behind the service's back, leaving the
		// lead's link in place (the company service would have cleared it).
		require.NoError(t, db.Delete(&models.Company{}, gone.ID).Error)

		neverID := gone.ID + 1000
		neverLead := env.newLead(models.LeadStatusQualified, &neverID)

		for _, l := range []*models.Lead{lead, neverLead} {
			customer, err := env.svc.ConvertToCustomer(l.ID, &models.Customer{})
			require.NoError(t, err)
			assert.Nil(t, env.committedCustomer(customer.ID).CompanyID, "a link to a company that is not live is not carried over")
			assert.Equal(t, models.LeadStatusConverted, env.committedLead(l.ID).Status)
		}
	})

	t.Run("an already converted lead is refused and writes nothing", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		lead := env.newLead(models.LeadStatusQualified, nil)
		first, err := env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.NoError(t, err)

		_, err = env.svc.ConvertToCustomer(lead.ID, &models.Customer{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrLeadConverted), "got %v", err)
		assert.Equal(t, int64(1), env.customersWithEmail(lead.Email))
		stored := env.committedLead(lead.ID)
		require.NotNil(t, stored.CustomerID)
		assert.Equal(t, first.ID, *stored.CustomerID)
	})

	t.Run("a failed lead write rolls the customer back", func(t *testing.T) {
		env := root.bind(t)
		env.reset()
		companyID := env.company.ID
		lead := env.newLead(models.LeadStatusQualified, &companyID)
		customer, err := env.failing.ConvertToCustomer(lead.ID, &models.Customer{Notes: "kept nowhere"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, errConvertWriteFailed), "got %v", err)
		assert.Nil(t, customer)
		assert.Zero(t, env.customersWithEmail(lead.Email), "the customer insert is rolled back")
		stored := env.committedLead(lead.ID)
		assert.Equal(t, models.LeadStatusQualified, stored.Status)
		assert.Nil(t, stored.CustomerID)
		require.NotNil(t, stored.CompanyID)
		assert.Equal(t, env.company.ID, *stored.CompanyID)
	})
}

// brokenConversionSettings fails every lead-conversion settings read, the way
// a database outage would.
type brokenConversionSettings struct {
	ConfigurationService
}

func (brokenConversionSettings) GetLeadConversionStatuses() ([]string, error) {
	return nil, errDatabase
}

// A settings read that fails refuses the conversion before anything is
// written, instead of converting under a guessed policy.
func TestLeadConversionSettingsReadFailureWritesNothing(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newLeadConversionEnv(t, db)
	svc := NewLeadService(env.leads, repository.NewCustomerRepository(db), repository.NewCompanyRepository(db),
		utils.NewTransactionManager(db), WithLeadConversionRules(brokenConversionSettings{}))
	lead := env.newLead(models.LeadStatusQualified, nil)

	_, err := svc.ConvertToCustomer(lead.ID, &models.Customer{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errDatabase), "got %v", err)
	assert.False(t, errors.Is(err, apperrors.ErrValidation), "an outage is not a validation failure")
	assert.Zero(t, env.customersWithEmail(lead.Email))
	assert.Equal(t, models.LeadStatusQualified, env.committedLead(lead.ID).Status)
}
