package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Atomicity of the erasure.
//
// An erasure is several statements: overwrite the personal-data columns, purge
// whatever must not outlive the person, soft-delete the row. erasure.go promises
// they happen as ONE unit — "the scrub and the soft delete cannot be separated
// by a failure" — and that promise is the whole reason the code wraps them in a
// transaction rather than issuing them in sequence.
//
// A promise like that is only worth what a test makes of it. Every happy-path
// test in this suite passes just as well against an implementation that runs the
// statements one after another with no transaction at all, because on the happy
// path there is nothing to separate. What distinguishes the two implementations
// is a failure AFTER the scrub, and the only way to pin the difference down is
// to cause one.
//
// The tests below do exactly that: they make the final soft delete fail, and
// then assert that the row is still there, still live, and still holding the
// person's ORIGINAL data. Delete the transaction from runErasureUnit (which
// eraseGraph runs every erasure in) and every one of them goes red, because the half-erasure the transaction prevents becomes
// visible: an anonymised, still-live, still-listed account whose credentials
// were purged and whose data is gone, with nothing to say the deletion failed.

// deleteFailure is a controllable, table-scoped failure injected into the DELETE
// path. It is deliberately placed on the LAST statement of an erasure so that
// everything before it — the column scrub, and for a user the credential purge —
// has already been issued and can only be undone by a rollback.
type deleteFailure struct {
	mu    sync.Mutex
	err   error
	calls int
	when  func(call int) bool
}

// Calls reports how many deletes against the target table were intercepted, so a
// test can prove the injection actually fired rather than passing because the
// erasure never got that far.
func (f *deleteFailure) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// failDeletesOn makes soft deletes against one table fail, from inside GORM, at
// the point the erasure issues them.
//
// A callback is used rather than a broken schema because it is surgical: the
// scrub UPDATE, the credential purge and the surrounding transaction all still
// work normally, and only the very last statement of the erasure fails. That
// isolates the property under test — what happens to the work already done when
// a later step of the same erasure fails.
//
// when is called with the 1-based number of the intercepted delete and decides
// whether that one fails; nil fails all of them.
func failDeletesOn(t *testing.T, db *gorm.DB, table string, when func(call int) bool) *deleteFailure {
	t.Helper()

	failure := &deleteFailure{
		err:  errors.New("simulated database failure while soft-deleting " + table),
		when: when,
	}

	const callbackName = "erasure_test:fail_delete"
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if statementTable(tx) != table {
			return
		}
		failure.mu.Lock()
		failure.calls++
		call := failure.calls
		failure.mu.Unlock()

		if failure.when != nil && !failure.when(call) {
			return
		}
		_ = tx.AddError(failure.err)
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Delete().Remove(callbackName))
	})

	return failure
}

func statementTable(tx *gorm.DB) string {
	if tx.Statement == nil {
		return ""
	}
	if tx.Statement.Table != "" {
		return tx.Statement.Table
	}
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return ""
}

// assertUserIsUntouched re-reads the user through an ORDINARY (scoped) query and
// checks that the person is still fully there. Scoped on purpose: if the row had
// been soft-deleted this would not find it at all.
func assertUserIsUntouched(t *testing.T, db *gorm.DB, id uint, email string) {
	t.Helper()

	var user models.User
	require.NoError(t, db.First(&user, id).Error,
		"the row must still be live — a failed erasure that soft-deleted anyway is a silent, irreversible deletion")

	assert.False(t, user.DeletedAt.Valid, "the failed erasure must not have soft-deleted the row")
	assert.Equal(t, email, user.Email, "the address must be back, not a placeholder")
	assert.Equal(t, "Erasure", user.FirstName, "the scrub must have been rolled back")
	assert.Equal(t, "Subject", user.LastName)
	assert.True(t, user.IsActive, "the account must not have been left deactivated by a failed erasure")
	assert.NotNil(t, user.LastLoginAt, "the login bookkeeping must be back too")
	assert.Equal(t, 3, user.FailedLoginAttempts)
	assert.NotEqual(t, "!erased", user.Password, "the password hash must have been restored")
}

// --- Users -------------------------------------------------------------------

// The user erasure is the widest one: it scrubs seven columns, purges two
// credential tables and then soft-deletes. Failing the last step must undo all
// of it. Without the transaction the account would be left anonymised, live and
// locked out, its credentials destroyed, and the caller would be told the
// deletion failed — an unrecoverable state produced by an operation that
// reported an error.
func TestUserErasureRollsBackEverythingWhenTheSoftDeleteFails(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "atomic-user@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-must-survive", Prefix: "gcrm_abc", UserID: user.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: user.ID, TokenHash: "refresh-must-survive", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)

	failure := failDeletesOn(t, db, "users", nil)

	err := userRepo.Delete(user.ID)
	require.Error(t, err, "an erasure whose soft delete fails must not report success")
	require.Equal(t, 1, failure.Calls(), "the failure must have been injected into the erasure's own delete")

	assertUserIsUntouched(t, db, user.ID, "atomic-user@example.com")

	// The credential purge ran BEFORE the failing delete, so it is the clearest
	// evidence of a rollback: without one, the keys would be gone for good while
	// the account they belonged to is still live and unerased.
	var keys int64
	require.NoError(t, db.Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys, "the credential purge must have been rolled back with the rest")

	var tokens int64
	require.NoError(t, db.Model(&models.RefreshToken{}).Where("user_id = ?", user.ID).Count(&tokens).Error)
	assert.Equal(t, int64(1), tokens, "the refresh token purge must have been rolled back with the rest")
}

// And the person must still be erasable afterwards. A rollback that left the row
// scrubbed-but-live would make the retry look like a second erasure of an
// already-anonymous shell — the data would be gone with no record of it ever
// having been erased.
func TestAUserErasureCanBeRetriedAfterAFailedAttempt(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "retry-after-failure@example.com")

	failure := failDeletesOn(t, db, "users", func(call int) bool { return call == 1 })

	require.Error(t, userRepo.Delete(user.ID))
	assertUserIsUntouched(t, db, user.ID, "retry-after-failure@example.com")

	require.NoError(t, userRepo.Delete(user.ID), "the second attempt must succeed")
	require.Equal(t, 2, failure.Calls())

	assertColumnsFreeOf(t, db, "users", "retry-after-failure@example.com", "Erasure", "Subject")
	assert.True(t, fetchErasedUser(t, db, user.ID).DeletedAt.Valid)
}

// --- Customers ---------------------------------------------------------------

// The purest form of the property: a customer erasure is a scrub followed by a
// soft delete with nothing in between, so this test isolates the gap the
// transaction exists to close. If the two statements were issued directly, the
// customer would be sitting in the table right now with a deleted-<random>
// address, no name, no phone and no notes, and still visible in every customer
// list — data destroyed, person not deleted, caller told it failed.
func TestCustomerErasureRollsBackWhenTheSoftDeleteFails(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "atomic-customer@example.com")
	failure := failDeletesOn(t, db, "customers", nil)

	err := customerRepo.Delete(customer.ID)
	require.Error(t, err, "an erasure whose soft delete fails must not report success")
	require.Equal(t, 1, failure.Calls())

	var reloaded models.Customer
	require.NoError(t, db.First(&reloaded, customer.ID).Error, "the row must still be live")
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "atomic-customer@example.com", reloaded.Email)
	assert.Equal(t, "Erasure", reloaded.FirstName)
	assert.Equal(t, "Subject", reloaded.LastName)
	assert.Equal(t, "+40 721 000 111", reloaded.Phone)
	assert.Equal(t, "Subject Industries", reloaded.Company)
	assert.Equal(t, "12 Privacy Lane", reloaded.Address)
	assert.Equal(t, "Prefers to be called on the mobile number above.", reloaded.Notes)
}

// --- Leads and the conversion link -------------------------------------------

func TestLeadErasureRollsBackWhenTheSoftDeleteFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-lead@example.com")

	failure := failDeletesOn(t, db, "leads", nil)

	require.Error(t, leadRepo.Delete(lead.ID))
	require.Equal(t, 1, failure.Calls())

	var reloaded models.Lead
	require.NoError(t, db.First(&reloaded, lead.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "atomic-lead@example.com", reloaded.Email)
	assert.Equal(t, "Ingrid", reloaded.FirstName)
	assert.Equal(t, "Vasilescu", reloaded.LastName)
	assert.Equal(t, "+40 722 333 444", reloaded.Phone)
	assert.Equal(t, "legacy-crm-contact-99182", reloaded.ExternalID)
	assert.Equal(t, "Met Ingrid at the Bucharest expo; call her mobile after 18:00.", reloaded.Notes)
}

// A converted lead and its customer are erased in one cascade, lead first. This
// fails the SECOND half — the customer's soft delete — after the lead has
// already been fully erased. The whole cascade has to come back, or the person
// is left half-erased across two tables: no trace of them on the lead, a
// complete copy of them on the customer, and an error returned to the operator
// who asked for the erasure.
func TestConvertedPairErasureRollsBackWhenTheSecondHalfFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-cascade@example.com")
	customer := convertLead(t, db, lead)

	failure := failDeletesOn(t, db, "customers", nil)

	require.Error(t, leadRepo.Delete(lead.ID),
		"a cascade that cannot finish must not report success")
	require.Equal(t, 1, failure.Calls())

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error,
		"the lead half must be back — it was erased before the failure")
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade@example.com", reloadedLead.Email)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)
	assert.Equal(t, "Met Ingrid at the Bucharest expo; call her mobile after 18:00.", reloadedLead.Notes)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade@example.com", reloadedCustomer.Email)
}

// The same cascade entered from the customer end, failing on the lead half.
func TestCustomerFirstCascadeRollsBackWhenTheLeadHalfFails(t *testing.T) {
	db := setupLeadErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "atomic-cascade-2@example.com")
	customer := convertLead(t, db, lead)

	failure := failDeletesOn(t, db, "leads", nil)

	require.Error(t, customerRepo.Delete(customer.ID))
	require.Equal(t, 1, failure.Calls())

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error,
		"the customer half must be back — it was erased before the failure")
	assert.False(t, reloadedCustomer.DeletedAt.Valid)
	assert.Equal(t, "atomic-cascade-2@example.com", reloadedCustomer.Email)
	assert.Equal(t, "Ingrid", reloadedCustomer.FirstName)

	var reloadedLead models.Lead
	require.NoError(t, db.First(&reloadedLead, lead.ID).Error)
	assert.False(t, reloadedLead.DeletedAt.Valid)
	assert.Equal(t, "Ingrid", reloadedLead.FirstName)
}

// --- Customer writes against an erasure (#256) --------------------------------
//
// PUT /customers/{id} and POST /customers/{id}/assign read the customer, change
// the struct and write it back. With GORM's Save that write fell back to an
// upsert when an erasure had committed in between, which restored the person and
// cleared deleted_at. These roots run the real handler, service and repositories
// over the production schema (leadOwnerEnv: a one-connection SQLite file and a
// disposable schema on TEST_MYSQL_DSN). The fixture links the customer to a
// converted lead and a customer-role login, and every assertion reads committed
// rows outside any transaction.

// customerWriteWait bounds every wait on another connection in the MySQL roots.
const customerWriteWait = 15 * time.Second

type customerWriteEnv struct {
	*leadOwnerEnv
	customers       repository.CustomerRepository
	companies       repository.CompanyRepository
	customerService service.CustomerService
}

func newCustomerWriteEnv(t *testing.T, useMySQL bool) *customerWriteEnv {
	t.Helper()
	base := newLeadOwnerEnv(t, useMySQL)
	customers := repository.NewCustomerRepository(base.db)
	companies := repository.NewCompanyRepository(base.db)
	e := &customerWriteEnv{
		leadOwnerEnv:    base,
		customers:       customers,
		companies:       companies,
		customerService: service.NewCustomerService(customers, base.users, companies),
	}
	e.mountCustomerRoutes(e.customerService)
	return e
}

// mountCustomerRoutes puts the production customer routes in front of
// customerService, behind the real auth and logging middleware.
func (e *customerWriteEnv) mountCustomerRoutes(customerService service.CustomerService) {
	router := gin.New()
	router.Use(middleware.RequestID())
	router.Use(middleware.Logger())
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())
	protected := router.Group("/api/v1")
	protected.Use(middleware.Auth(e.auth))
	handler.SetupCustomerRoutes(protected, handler.NewCustomerHandler(customerService))
	e.router = router
}

// linkedPerson is one person stored three times: a customer, the lead converted
// into it and its customer-role login, all with the same address.
type linkedPerson struct {
	customer *models.Customer
	lead     *models.Lead
	login    *models.User
	company  *models.Company
	email    string
}

// personal lists every value the fixture identifies the person by.
func (p *linkedPerson) personal() []string {
	return []string{
		p.email,
		p.customer.FirstName, p.customer.LastName, p.customer.Phone, p.customer.Company, p.customer.Website,
		p.customer.Position, p.customer.Address, p.customer.City, p.customer.State, p.customer.Country,
		p.customer.PostalCode, p.customer.Notes,
		p.lead.FirstName, p.lead.LastName, p.lead.Phone, p.lead.Company, p.lead.Position, p.lead.Notes,
		p.lead.ExternalID,
		p.login.FirstName, p.login.LastName,
	}
}

func (e *customerWriteEnv) newCompany() *models.Company {
	e.t.Helper()
	seq := leadOwnerNext()
	company := &models.Company{
		Name:   fmt.Sprintf("Linked Company %d", seq),
		Domain: fmt.Sprintf("company-%d.customer-write.test", seq),
	}
	require.NoError(e.t, e.db.Create(company).Error)
	return company
}

// newCustomerRow stores a customer whose every personal column holds a value
// unique to it.
func (e *customerWriteEnv) newCustomerRow(email string, link func(*models.Customer)) *models.Customer {
	e.t.Helper()
	seq := leadOwnerNext()
	customer := &models.Customer{
		FirstName:  fmt.Sprintf("CustFirst%d", seq),
		LastName:   fmt.Sprintf("CustLast%d", seq),
		Email:      email,
		Phone:      fmt.Sprintf("+40 700 %d", seq),
		Company:    fmt.Sprintf("CustCompany%d", seq),
		Website:    fmt.Sprintf("https://cust-%d.customer-write.test", seq),
		Position:   fmt.Sprintf("CustPosition%d", seq),
		Address:    fmt.Sprintf("CustAddress%d", seq),
		City:       fmt.Sprintf("CustCity%d", seq),
		State:      fmt.Sprintf("CustState%d", seq),
		Country:    fmt.Sprintf("CustCountry%d", seq),
		PostalCode: fmt.Sprintf("PC%d", seq),
		Notes:      fmt.Sprintf("CustNotes%d", seq),
	}
	if link != nil {
		link(customer)
	}
	require.NoError(e.t, e.db.Create(customer).Error)
	return customer
}

func (e *customerWriteEnv) newLinkedPerson() *linkedPerson {
	e.t.Helper()
	seq := leadOwnerNext()
	email := fmt.Sprintf("linked-%d@customer-write.test", seq)
	lastLogin := time.Now().Add(-time.Hour).UTC()
	lockedUntil := time.Now().Add(time.Hour).UTC()

	login := &models.User{
		Email:               email,
		Password:            "not-a-usable-hash",
		FirstName:           fmt.Sprintf("LoginFirst%d", seq),
		LastName:            fmt.Sprintf("LoginLast%d", seq),
		Role:                models.RoleCustomer,
		IsActive:            true,
		LastLoginAt:         &lastLogin,
		FailedLoginAttempts: 2,
		LockedUntil:         &lockedUntil,
	}
	require.NoError(e.t, e.db.Create(login).Error)

	company := e.newCompany()
	customer := e.newCustomerRow(email, func(row *models.Customer) {
		row.UserID = &login.ID
		row.AssignedToID = &e.sales.ID
		row.CompanyID = &company.ID
	})

	lead := &models.Lead{
		FirstName:  fmt.Sprintf("LeadFirst%d", seq),
		LastName:   fmt.Sprintf("LeadLast%d", seq),
		Email:      email,
		Phone:      fmt.Sprintf("+40 711 %d", seq),
		Company:    fmt.Sprintf("LeadCompany%d", seq),
		Position:   fmt.Sprintf("LeadPosition%d", seq),
		Status:     models.LeadStatusConverted,
		ExternalID: fmt.Sprintf("lead-external-%d", seq),
		Notes:      fmt.Sprintf("LeadNotes%d", seq),
		OwnerID:    e.sales.ID,
		CustomerID: &customer.ID,
	}
	require.NoError(e.t, e.db.Create(lead).Error)

	return &linkedPerson{customer: customer, lead: lead, login: login, company: company, email: email}
}

// customerErasureRoutes are the three deletes that reach the customer.
var customerErasureRoutes = []struct {
	name  string
	erase func(e *customerWriteEnv, p *linkedPerson) error
}{
	{
		name: "customer delete",
		erase: func(e *customerWriteEnv, p *linkedPerson) error {
			return e.customers.Delete(p.customer.ID)
		},
	},
	{
		name: "converted lead delete",
		erase: func(e *customerWriteEnv, p *linkedPerson) error {
			return e.leadRepo.Delete(p.lead.ID)
		},
	},
	{
		name: "customer login delete",
		erase: func(e *customerWriteEnv, p *linkedPerson) error {
			return e.users.Delete(p.login.ID)
		},
	},
}

// customerServiceErasedAfterRead erases the person after the handler's read.
type customerServiceErasedAfterRead struct {
	service.CustomerService
	erase    func() error
	erased   bool
	eraseErr error
}

func (s *customerServiceErasedAfterRead) GetByID(id uint) (*models.Customer, error) {
	customer, err := s.CustomerService.GetByID(id)
	if err == nil && !s.erased {
		s.erased = true
		s.eraseErr = s.erase()
	}
	return customer, err
}

// customerRepoErasedAfterRead erases the person after the service's read.
type customerRepoErasedAfterRead struct {
	repository.CustomerRepository
	erase    func() error
	erased   bool
	eraseErr error
}

func (r *customerRepoErasedAfterRead) GetByID(id uint) (*models.Customer, error) {
	customer, err := r.CustomerRepository.GetByID(id)
	if err == nil && !r.erased {
		r.erased = true
		r.eraseErr = r.erase()
	}
	return customer, err
}

func customerWritePath(id uint) string {
	return fmt.Sprintf("/customers/%d", id)
}

func customerAssignWritePath(id uint) string {
	return fmt.Sprintf("/customers/%d/assign", id)
}

// customerWriteBody is a PUT body with a value unique to this request in every
// text field.
func customerWriteBody() map[string]interface{} {
	seq := leadOwnerNext()
	return map[string]interface{}{
		"first_name":  fmt.Sprintf("BodyFirst%d", seq),
		"last_name":   fmt.Sprintf("BodyLast%d", seq),
		"email":       fmt.Sprintf("body-%d@customer-write.test", seq),
		"phone":       fmt.Sprintf("+40 722 %d", seq),
		"company":     fmt.Sprintf("BodyCompany%d", seq),
		"website":     fmt.Sprintf("https://body-%d.customer-write.test", seq),
		"position":    fmt.Sprintf("BodyPosition%d", seq),
		"address":     fmt.Sprintf("BodyAddress%d", seq),
		"city":        fmt.Sprintf("BodyCity%d", seq),
		"state":       fmt.Sprintf("BodyState%d", seq),
		"country":     fmt.Sprintf("BodyCountry%d", seq),
		"postal_code": fmt.Sprintf("BPC%d", seq),
		"notes":       fmt.Sprintf("BodyNotes%d", seq),
	}
}

func (e *customerWriteEnv) storedCustomer(id uint) models.Customer {
	e.t.Helper()
	var customer models.Customer
	require.NoError(e.t, e.db.Unscoped().First(&customer, id).Error)
	return customer
}

// rawCustomer reads the whole row as raw column values, so that comparing two
// reads covers every column, the ones this test does not name included.
func (e *customerWriteEnv) rawCustomer(id uint) map[string]interface{} {
	e.t.Helper()
	row := map[string]interface{}{}
	require.NoError(e.t, e.db.Table("customers").Where("id = ?", id).Take(&row).Error)
	return row
}

// assertCustomerErased checks the customer part of the erased state: every
// column in the Scrub map of customerErasurePlan.
func (e *customerWriteEnv) assertCustomerErased(id uint) {
	e.t.Helper()
	customer := e.storedCustomer(id)
	assert.True(e.t, customer.DeletedAt.Valid, "customer %d must stay soft-deleted", id)
	assert.True(e.t, strings.HasSuffix(customer.Email, ".invalid"), "customer %d email must be a placeholder", id)
	for column, value := range map[string]string{
		"first_name":  customer.FirstName,
		"last_name":   customer.LastName,
		"phone":       customer.Phone,
		"company":     customer.Company,
		"website":     customer.Website,
		"position":    customer.Position,
		"address":     customer.Address,
		"city":        customer.City,
		"state":       customer.State,
		"country":     customer.Country,
		"postal_code": customer.PostalCode,
		"notes":       customer.Notes,
	} {
		assert.Empty(e.t, value, "customers.%s of customer %d", column, id)
	}
}

// assertLinkedRowsErased checks the erased state of all three rows, every
// column in the Scrub maps of the three plans.
func (e *customerWriteEnv) assertLinkedRowsErased(p *linkedPerson) {
	e.t.Helper()
	e.assertCustomerErased(p.customer.ID)

	var lead models.Lead
	require.NoError(e.t, e.db.Unscoped().First(&lead, p.lead.ID).Error)
	assert.True(e.t, lead.DeletedAt.Valid, "the lead must be soft-deleted")
	assert.True(e.t, strings.HasSuffix(lead.Email, ".invalid"), "the lead email must be a placeholder")
	for column, value := range map[string]string{
		"first_name":  lead.FirstName,
		"last_name":   lead.LastName,
		"phone":       lead.Phone,
		"company":     lead.Company,
		"position":    lead.Position,
		"notes":       lead.Notes,
		"external_id": lead.ExternalID,
	} {
		assert.Empty(e.t, value, "leads.%s", column)
	}

	var login models.User
	require.NoError(e.t, e.db.Unscoped().First(&login, p.login.ID).Error)
	assert.True(e.t, login.DeletedAt.Valid, "the login must be soft-deleted")
	assert.True(e.t, strings.HasSuffix(login.Email, ".invalid"), "the login email must be a placeholder")
	assert.Empty(e.t, login.FirstName, "users.first_name")
	assert.Empty(e.t, login.LastName, "users.last_name")
	assert.Equal(e.t, "!erased", login.Password, "users.password")
	assert.False(e.t, login.IsActive, "users.is_active")
	assert.Equal(e.t, 0, login.FailedLoginAttempts, "users.failed_login_attempts")
	assert.Nil(e.t, login.LastLoginAt, "users.last_login_at")
	assert.Nil(e.t, login.LockedUntil, "users.locked_until")
}

// assertPersonErased adds to the three rows that no live row holds the address.
func (e *customerWriteEnv) assertPersonErased(p *linkedPerson) {
	e.t.Helper()
	e.assertLinkedRowsErased(p)
	for _, model := range []interface{}{&models.Customer{}, &models.Lead{}, &models.User{}} {
		var live int64
		require.NoError(e.t, e.db.Model(model).Where("email = ?", p.email).Count(&live).Error)
		assert.Zero(e.t, live, "no live %T may carry the original address", model)
	}
}

func (e *customerWriteEnv) assertNotFoundAnswer(status int, envelope utils.APIResponse) {
	e.t.Helper()
	require.Equal(e.t, http.StatusNotFound, status, "%+v", envelope.Error)
	require.NotNil(e.t, envelope.Error)
	assert.Equal(e.t, utils.ErrCodeNotFound, envelope.Error.Code)
	assert.Equal(e.t, "Customer not found", envelope.Error.Message)
}

// --- AC-1: a PUT whose customer is erased after the handler's read -------------

func TestCustomerUpdateLostToErasureSQLite(t *testing.T) {
	runCustomerUpdateLostToErasure(t, false)
}

func TestCustomerUpdateLostToErasureMySQL(t *testing.T) {
	runCustomerUpdateLostToErasure(t, true)
}

func runCustomerUpdateLostToErasure(t *testing.T, useMySQL bool) {
	e := newCustomerWriteEnv(t, useMySQL)
	for _, route := range customerErasureRoutes {
		t.Run(route.name, func(t *testing.T) {
			e.t = t
			p := e.newLinkedPerson()
			racing := &customerServiceErasedAfterRead{
				CustomerService: e.customerService,
				erase:           func() error { return route.erase(e, p) },
			}
			e.mountCustomerRoutes(racing)

			status, envelope := e.send(http.MethodPut, customerWritePath(p.customer.ID), e.admin, customerWriteBody())

			require.True(t, racing.erased, "the erasure must have run after the handler's read")
			require.NoError(t, racing.eraseErr)
			e.assertNotFoundAnswer(status, envelope)
			e.assertPersonErased(p)
		})
	}
}

// --- AC-2: an assign whose customer is erased after the service's read ---------

func TestCustomerAssignLostToErasureSQLite(t *testing.T) {
	runCustomerAssignLostToErasure(t, false)
}

func TestCustomerAssignLostToErasureMySQL(t *testing.T) {
	runCustomerAssignLostToErasure(t, true)
}

func runCustomerAssignLostToErasure(t *testing.T, useMySQL bool) {
	e := newCustomerWriteEnv(t, useMySQL)
	for _, route := range customerErasureRoutes {
		t.Run(route.name, func(t *testing.T) {
			e.t = t
			p := e.newLinkedPerson()
			racing := &customerRepoErasedAfterRead{
				CustomerRepository: e.customers,
				erase:              func() error { return route.erase(e, p) },
			}
			e.mountCustomerRoutes(service.NewCustomerService(racing, e.users, e.companies))

			status, envelope := e.send(http.MethodPost, customerAssignWritePath(p.customer.ID), e.admin,
				map[string]interface{}{"user_id": e.otherSales.ID})

			require.True(t, racing.erased, "the erasure must have run after the service's read")
			require.NoError(t, racing.eraseErr)
			e.assertNotFoundAnswer(status, envelope)
			e.assertPersonErased(p)

			stored := e.storedCustomer(p.customer.ID)
			require.NotNil(t, stored.AssignedToID, "assigned_to_id keeps its stored value")
			assert.Equal(t, e.sales.ID, *stored.AssignedToID)
		})
	}
}

// --- AC-3: a write arriving after the erasure committed ------------------------

func TestCustomerWriteAfterErasureSQLite(t *testing.T) {
	runCustomerWriteAfterErasure(t, false)
}

func TestCustomerWriteAfterErasureMySQL(t *testing.T) {
	runCustomerWriteAfterErasure(t, true)
}

func runCustomerWriteAfterErasure(t *testing.T, useMySQL bool) {
	e := newCustomerWriteEnv(t, useMySQL)
	for _, route := range customerErasureRoutes {
		t.Run(route.name, func(t *testing.T) {
			e.t = t
			p := e.newLinkedPerson()
			id := p.customer.ID
			readStale := func() *models.Customer {
				t.Helper()
				stale, err := e.customers.GetByID(id)
				require.NoError(t, err)
				return stale
			}
			staleRepo, staleService, staleAfterReuse, staleServiceAfterReuse := readStale(), readStale(), readStale(), readStale()

			require.NoError(t, route.erase(e, p))
			erasedRow := e.rawCustomer(id)

			assert.ErrorIs(t, e.customers.Update(staleRepo), gorm.ErrRecordNotFound)
			assert.ErrorIs(t, e.customerService.Update(staleService), apperrors.ErrNotFound)
			_, err := e.customerService.Assign(id, e.otherSales.ID)
			assert.ErrorIs(t, err, apperrors.ErrNotFound)
			status, envelope := e.send(http.MethodPut, customerWritePath(id), e.admin, customerWriteBody())
			e.assertNotFoundAnswer(status, envelope)
			e.assertPersonErased(p)
			assert.Equal(t, erasedRow, e.rawCustomer(id), "nothing may touch the erased row")

			// The address is free again and now belongs to somebody else.
			replacement := e.newCustomerRow(p.email, nil)
			replacementRow := e.rawCustomer(replacement.ID)

			assert.ErrorIs(t, e.customers.Update(staleAfterReuse), gorm.ErrRecordNotFound,
				"the row matched nothing, so the unique index has nothing to refuse")
			assert.ErrorIs(t, e.customerService.Update(staleServiceAfterReuse), apperrors.ErrDuplicateEmail)

			assert.Equal(t, replacementRow, e.rawCustomer(replacement.ID), "the new customer's row is unchanged")
			assert.Equal(t, erasedRow, e.rawCustomer(id), "the erased row stays erased")
			e.assertLinkedRowsErased(p)
		})
	}
}

// --- AC-4: writes and erasures running at the same time ------------------------

// awaitBlockedCustomerUpdate returns once an UPDATE of the customers table is
// seen executing on another connection on two consecutive polls of
// information_schema.PROCESSLIST, which means it is waiting for the row lock.
func awaitBlockedCustomerUpdate(t *testing.T, db *gorm.DB, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(customerWriteWait)
	for seen := 0; seen < 2; {
		var waiting int64
		require.NoError(t, db.Raw(`SELECT COUNT(*)
			FROM information_schema.PROCESSLIST
			WHERE DB = DATABASE() AND ID <> CONNECTION_ID() AND COMMAND IN ('Query', 'Execute')
			AND INFO LIKE 'UPDATE%customers%'`).Scan(&waiting).Error)
		if waiting > 0 {
			seen++
		} else {
			seen = 0
		}
		if time.Now().After(deadline) {
			t.Fatalf("no UPDATE of customers was seen waiting for the row lock within %s", customerWriteWait)
		}
		select {
		case err := <-done:
			t.Fatalf("the second writer finished without waiting for the row lock: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func awaitCustomerWriter(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(customerWriteWait):
		t.Fatalf("the writer did not finish within %s of the commit", customerWriteWait)
		return nil
	}
}

func TestCustomerWriteConcurrentWithErasureMySQL(t *testing.T) {
	e := newCustomerWriteEnv(t, true)

	t.Run("an update blocked behind an uncommitted erasure", func(t *testing.T) {
		e.t = t
		p := e.newLinkedPerson()
		stale, err := e.customers.GetByID(p.customer.ID)
		require.NoError(t, err)

		tx := e.db.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		require.NoError(t, e.customers.WithTx(tx).Delete(p.customer.ID))

		done := make(chan error, 1)
		go func() { done <- e.customers.Update(stale) }()
		awaitBlockedCustomerUpdate(t, e.db, done)

		require.NoError(t, tx.Commit().Error)
		assert.ErrorIs(t, awaitCustomerWriter(t, done), gorm.ErrRecordNotFound)
		e.assertPersonErased(p)
	})

	t.Run("an erasure blocked behind an uncommitted update", func(t *testing.T) {
		e.t = t
		p := e.newLinkedPerson()
		edited, err := e.customers.GetByID(p.customer.ID)
		require.NoError(t, err)
		seq := leadOwnerNext()
		edited.Email = fmt.Sprintf("edited-%d@customer-write.test", seq)
		edited.Phone = fmt.Sprintf("+40 733 %d", seq)
		edited.Notes = fmt.Sprintf("EditedNotes%d", seq)

		tx := e.db.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		require.NoError(t, e.customers.WithTx(tx).Update(edited))

		done := make(chan error, 1)
		go func() { done <- e.customers.Delete(p.customer.ID) }()
		awaitBlockedCustomerUpdate(t, e.db, done)

		require.NoError(t, tx.Commit().Error)
		require.NoError(t, awaitCustomerWriter(t, done))
		e.assertPersonErased(p)

		var survivors int64
		require.NoError(t, e.db.Unscoped().Model(&models.Customer{}).
			Where("email = ? OR phone = ? OR notes = ?", edited.Email, edited.Phone, edited.Notes).
			Count(&survivors).Error)
		assert.Zero(t, survivors, "the erasure must scrub the values the update committed")
	})

	t.Run("a write inside a transaction whose snapshot predates the erasure", func(t *testing.T) {
		e.t = t
		p := e.newLinkedPerson()
		stale, err := e.customers.GetByID(p.customer.ID)
		require.NoError(t, err)

		tx := e.db.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		var seen models.Customer
		require.NoError(t, tx.First(&seen, p.customer.ID).Error, "this read fixes the snapshot")

		require.NoError(t, e.customers.Delete(p.customer.ID))

		var liveInSnapshot int64
		require.NoError(t, tx.Model(&models.Customer{}).Where("id = ?", p.customer.ID).Count(&liveInSnapshot).Error)
		require.Equal(t, int64(1), liveInSnapshot, "a plain read in this transaction must still see the row as live")

		assert.ErrorIs(t, e.customers.WithTx(tx).Update(stale), gorm.ErrRecordNotFound,
			"the write matched no row and must not report a false success")

		require.NoError(t, tx.Commit().Error)
		e.assertPersonErased(p)
	})
}

func TestCustomerWriteConcurrentWithErasureSQLite(t *testing.T) {
	e := newCustomerWriteEnv(t, false)

	type outcome struct {
		writer string
		err    error
	}

	for round := 0; round < 20; round++ {
		p := e.newLinkedPerson()
		stale, err := e.customerService.GetByID(p.customer.ID)
		require.NoError(t, err)

		start := make(chan struct{})
		results := make(chan outcome, 3)
		go func() {
			<-start
			results <- outcome{"update", e.customerService.Update(stale)}
		}()
		go func() {
			<-start
			_, assignErr := e.customerService.Assign(p.customer.ID, e.otherSales.ID)
			results <- outcome{"assign", assignErr}
		}()
		go func() {
			<-start
			results <- outcome{"erase", e.customers.Delete(p.customer.ID)}
		}()
		close(start)

		timeout := time.NewTimer(5 * time.Second)
		for finished := 0; finished < 3; finished++ {
			select {
			case result := <-results:
				if result.writer == "erase" {
					require.NoError(t, result.err, "round %d: the erasure must succeed", round)
				} else if result.err != nil {
					require.ErrorIs(t, result.err, apperrors.ErrNotFound, "round %d: %s", round, result.writer)
				}
			case <-timeout.C:
				t.Fatalf("round %d did not finish within 5 seconds", round)
			}
		}
		timeout.Stop()

		e.assertCustomerErased(p.customer.ID)
	}
}

// --- AC-5: a live customer -----------------------------------------------------

func TestCustomerUpdateLiveRowSQLite(t *testing.T) {
	runCustomerUpdateLiveRow(t, false)
}

func TestCustomerUpdateLiveRowMySQL(t *testing.T) {
	runCustomerUpdateLiveRow(t, true)
}

func runCustomerUpdateLiveRow(t *testing.T, useMySQL bool) {
	e := newCustomerWriteEnv(t, useMySQL)
	p := e.newLinkedPerson()
	id := p.customer.ID
	before := e.storedCustomer(id)
	otherCompany := e.newCompany()

	frozen := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	clock := frozen
	repo := repository.NewCustomerRepository(e.db.Session(&gorm.Session{
		NowFunc: func() time.Time { return clock },
	}))

	var affected []int64
	const callbackName = "customer_write_test:rows_affected"
	require.NoError(t, e.db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if statementTable(tx) == "customers" {
			affected = append(affected, tx.RowsAffected)
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, e.db.Callback().Update().Remove(callbackName))
	})

	stale, err := repo.GetByID(id)
	require.NoError(t, err)
	require.NotNil(t, stale.User, "the read preloads the login")
	require.NotNil(t, stale.CompanyRecord, "the read preloads the company")

	seq := leadOwnerNext()
	stale.FirstName = fmt.Sprintf("NewFirst%d", seq)
	stale.LastName = fmt.Sprintf("NewLast%d", seq)
	stale.Email = fmt.Sprintf("new-%d@customer-write.test", seq)
	stale.Phone = fmt.Sprintf("+40 744 %d", seq)
	stale.Company = fmt.Sprintf("NewCompany%d", seq)
	stale.Website = fmt.Sprintf("https://new-%d.customer-write.test", seq)
	stale.Position = fmt.Sprintf("NewPosition%d", seq)
	stale.Address = fmt.Sprintf("NewAddress%d", seq)
	stale.City = fmt.Sprintf("NewCity%d", seq)
	stale.State = fmt.Sprintf("NewState%d", seq)
	stale.Country = fmt.Sprintf("NewCountry%d", seq)
	stale.PostalCode = fmt.Sprintf("NPC%d", seq)
	stale.Notes = fmt.Sprintf("NewNotes%d", seq)
	stale.CompanyID = &otherCompany.ID
	stale.AssignedToID = &e.otherSales.ID

	// None of these may reach the database.
	stale.CreatedAt = frozen.Add(-24 * time.Hour)
	stale.UserID = nil
	stale.DeletedAt = gorm.DeletedAt{Time: frozen, Valid: true}
	stale.User.FirstName = "StaleLoginName"
	stale.CompanyRecord.Name = "Stale company name"

	require.NoError(t, repo.Update(stale))
	assert.True(t, stale.UpdatedAt.Equal(frozen), "the struct takes the clock's value, got %s", stale.UpdatedAt)

	stored := e.storedCustomer(id)
	assert.Equal(t, id, stored.ID)
	assert.Equal(t, stale.FirstName, stored.FirstName)
	assert.Equal(t, stale.LastName, stored.LastName)
	assert.Equal(t, stale.Email, stored.Email)
	assert.Equal(t, stale.Phone, stored.Phone)
	assert.Equal(t, stale.Company, stored.Company)
	assert.Equal(t, stale.Website, stored.Website)
	assert.Equal(t, stale.Position, stored.Position)
	assert.Equal(t, stale.Address, stored.Address)
	assert.Equal(t, stale.City, stored.City)
	assert.Equal(t, stale.State, stored.State)
	assert.Equal(t, stale.Country, stored.Country)
	assert.Equal(t, stale.PostalCode, stored.PostalCode)
	assert.Equal(t, stale.Notes, stored.Notes)
	require.NotNil(t, stored.CompanyID)
	assert.Equal(t, otherCompany.ID, *stored.CompanyID)
	require.NotNil(t, stored.AssignedToID)
	assert.Equal(t, e.otherSales.ID, *stored.AssignedToID)
	assert.True(t, stored.UpdatedAt.Equal(frozen), "the row takes the clock's value, got %s", stored.UpdatedAt)
	assert.True(t, stored.CreatedAt.Equal(before.CreatedAt), "created_at stays, got %s", stored.CreatedAt)
	require.NotNil(t, stored.UserID, "user_id stays")
	assert.Equal(t, p.login.ID, *stored.UserID)
	assert.False(t, stored.DeletedAt.Valid, "deleted_at stays")

	var login models.User
	require.NoError(t, e.db.First(&login, p.login.ID).Error)
	assert.Equal(t, p.login.FirstName, login.FirstName, "a stale preloaded User writes nothing to users")
	var company models.Company
	require.NoError(t, e.db.First(&company, p.company.ID).Error)
	assert.Equal(t, p.company.Name, company.Name, "a stale preloaded CompanyRecord writes nothing to companies")

	// A preloaded association that names another row does not move the key.
	stale.User = &models.User{BaseModel: models.BaseModel{ID: e.support.ID}}
	stale.CompanyRecord = p.company
	require.NoError(t, repo.Update(stale))
	stored = e.storedCustomer(id)
	require.NotNil(t, stored.UserID)
	assert.Equal(t, p.login.ID, *stored.UserID)
	require.NotNil(t, stored.CompanyID)
	assert.Equal(t, otherCompany.ID, *stored.CompanyID)

	// Empty and NULL are values too.
	clock = frozen.Add(time.Hour)
	stale.Website = ""
	stale.CompanyID = nil
	stale.AssignedToID = nil
	require.NoError(t, repo.Update(stale))
	stored = e.storedCustomer(id)
	assert.Empty(t, stored.Website)
	assert.Nil(t, stored.CompanyID)
	assert.Nil(t, stored.AssignedToID)
	assert.True(t, stored.UpdatedAt.Equal(clock), "got %s", stored.UpdatedAt)

	// The same values again under the same clock. MySQL reports zero changed
	// rows, and that is still a success.
	affected = nil
	unchanged := e.rawCustomer(id)
	require.NoError(t, repo.Update(stale))
	require.Len(t, affected, 1, "the identical write must have been issued")
	if useMySQL {
		assert.Equal(t, int64(0), affected[0], "MySQL must report zero changed rows for the identical write")
	}
	assert.Equal(t, unchanged, e.rawCustomer(id))

	// The same values under a later clock: updated_at follows the clock.
	clock = frozen.Add(2 * time.Hour)
	require.NoError(t, repo.Update(stale))
	assert.True(t, stale.UpdatedAt.Equal(clock), "got %s", stale.UpdatedAt)
	stored = e.storedCustomer(id)
	assert.True(t, stored.UpdatedAt.Equal(clock), "got %s", stored.UpdatedAt)

	// An id with no row is not found and inserts nothing.
	var rowsBefore, rowsAfter int64
	require.NoError(t, e.db.Unscoped().Model(&models.Customer{}).Count(&rowsBefore).Error)
	for _, unknownID := range []uint{0, 99999999} {
		ghost := *stale
		ghost.ID = unknownID
		ghost.Email = fmt.Sprintf("ghost-%d@customer-write.test", leadOwnerNext())
		assert.ErrorIs(t, repo.Update(&ghost), gorm.ErrRecordNotFound, "id %d", unknownID)
	}
	require.NoError(t, e.db.Unscoped().Model(&models.Customer{}).Count(&rowsAfter).Error)
	assert.Equal(t, rowsBefore, rowsAfter, "an unknown id must not insert a row")
}

// --- AC-6: the unique email index ----------------------------------------------

func TestCustomerUpdateDuplicateEmailSQLite(t *testing.T) {
	runCustomerUpdateDuplicateEmail(t, false)
}

func TestCustomerUpdateDuplicateEmailMySQL(t *testing.T) {
	runCustomerUpdateDuplicateEmail(t, true)
}

func runCustomerUpdateDuplicateEmail(t *testing.T, useMySQL bool) {
	e := newCustomerWriteEnv(t, useMySQL)
	first := e.newCustomerRow(fmt.Sprintf("first-%d@customer-write.test", leadOwnerNext()), nil)
	second := e.newCustomerRow(fmt.Sprintf("second-%d@customer-write.test", leadOwnerNext()), nil)
	firstRow, secondRow := e.rawCustomer(first.ID), e.rawCustomer(second.ID)

	// Straight into the index: this path has no service pre-check.
	clash, err := e.customers.GetByID(first.ID)
	require.NoError(t, err)
	clash.Email = second.Email
	clash.Notes = "must not stick"
	assert.ErrorIs(t, e.customers.Update(clash), apperrors.ErrDuplicateEmail)
	assert.Equal(t, firstRow, e.rawCustomer(first.ID))
	assert.Equal(t, secondRow, e.rawCustomer(second.ID))

	status, envelope := e.send(http.MethodPut, customerWritePath(first.ID), e.admin,
		map[string]interface{}{"email": second.Email, "notes": "must not stick"})
	require.Equal(t, http.StatusConflict, status, "%+v", envelope.Error)
	require.NotNil(t, envelope.Error)
	assert.Equal(t, utils.ErrCodeConflict, envelope.Error.Code)
	assert.Equal(t, firstRow, e.rawCustomer(first.ID))
	assert.Equal(t, secondRow, e.rawCustomer(second.ID))
}

// --- AC-7: what a lost write logs ----------------------------------------------

// assertLostWriteLog checks the captured utils.Logger output of one lost write:
// a Warn line with the customer id and wantMessage, no Error line, and none of
// the forbidden values anywhere.
func assertLostWriteLog(t *testing.T, text string, customerID uint, wantMessage string, forbidden []string) {
	t.Helper()
	found := false
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		var entry map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		assert.NotContains(t, []string{"error", "fatal", "panic"}, entry["level"], "line: %v", entry["msg"])
		if entry["level"] == "warning" && entry["msg"] == wantMessage && entry["customer_id"] == float64(customerID) {
			found = true
		}
	}
	assert.True(t, found, "a Warn line %q with customer_id %d must record the loss", wantMessage, customerID)
	for _, value := range forbidden {
		assert.NotContains(t, text, value, "the log must not hold a personal or request value")
	}
}

func TestCustomerLostWriteLogsOmitPersonalData(t *testing.T) {
	e := newCustomerWriteEnv(t, false)

	var output bytes.Buffer
	capture := logrus.New()
	capture.SetOutput(&output)
	capture.SetFormatter(&logrus.JSONFormatter{})
	capture.SetLevel(logrus.DebugLevel)
	previous := utils.Logger
	utils.Logger = capture
	t.Cleanup(func() { utils.Logger = previous })

	for _, route := range customerErasureRoutes {
		t.Run(route.name+" PUT", func(t *testing.T) {
			e.t = t
			p := e.newLinkedPerson()
			racing := &customerServiceErasedAfterRead{
				CustomerService: e.customerService,
				erase:           func() error { return route.erase(e, p) },
			}
			e.mountCustomerRoutes(racing)
			body := customerWriteBody()
			forbidden := p.personal()
			for _, value := range body {
				forbidden = append(forbidden, value.(string))
			}

			output.Reset()
			status, envelope := e.send(http.MethodPut, customerWritePath(p.customer.ID), e.admin, body)
			require.NoError(t, racing.eraseErr)
			e.assertNotFoundAnswer(status, envelope)
			assertLostWriteLog(t, output.String(), p.customer.ID, "Customer update lost: no live customer", forbidden)
		})

		t.Run(route.name+" assign", func(t *testing.T) {
			e.t = t
			p := e.newLinkedPerson()
			racing := &customerRepoErasedAfterRead{
				CustomerRepository: e.customers,
				erase:              func() error { return route.erase(e, p) },
			}
			e.mountCustomerRoutes(service.NewCustomerService(racing, e.users, e.companies))

			output.Reset()
			status, envelope := e.send(http.MethodPost, customerAssignWritePath(p.customer.ID), e.admin,
				map[string]interface{}{"user_id": e.otherSales.ID})
			require.NoError(t, racing.eraseErr)
			e.assertNotFoundAnswer(status, envelope)
			assertLostWriteLog(t, output.String(), p.customer.ID, "Customer assignment lost: no live customer", p.personal())
		})
	}
}
