package service

// The company events (#517): company.created, company.updated and
// company.deleted are written by CompanyService Create, Update and Delete
// inside the writing transaction, one pending row per active subscribed
// endpoint, on SQLite and on a disposable TEST_MYSQL_DSN schema opened with
// loc=Local (the MySQL roots skip without the variable and fail when it is
// unreachable). company.deleted follows the #531 rule: every active endpoint
// subscribed to it, whether or not it received an earlier event for the
// company. Clearing company_id on the linked leads, customers and deals writes
// no updated event.

import (
	"fmt"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCompanyWebhookFanOutSQLite(t *testing.T) {
	runCompanyWebhookFanOut(t, openTicketIntakeDB(t, false))
}

func TestCompanyWebhookFanOutMySQL(t *testing.T) {
	runCompanyWebhookFanOut(t, openDealWebhookMySQLDB(t))
}

func TestCompanyWebhookAtomicitySQLite(t *testing.T) {
	runCompanyWebhookAtomicity(t, openTicketIntakeDB(t, false))
}

func TestCompanyWebhookAtomicityMySQL(t *testing.T) {
	runCompanyWebhookAtomicity(t, openDealWebhookMySQLDB(t))
}

// storedWebhookCompany is a fresh read of the row, soft-deleted included.
func storedWebhookCompany(t *testing.T, db *gorm.DB, id uint) models.Company {
	t.Helper()
	var company models.Company
	require.NoError(t, db.Unscoped().First(&company, id).Error)
	return company
}

// seedCompany writes a company owned by the env owner directly (no webhook),
// with every text column set.
func (e *webhookEnv) seedCompany() *models.Company {
	e.t.Helper()
	n := webhookTestSeq.Add(1)
	owner := e.owner.ID
	company := &models.Company{Name: fmt.Sprintf("Seeded company %d", n), Domain: fmt.Sprintf("seeded-%d.example", n),
		Website: "https://seeded.example", Industry: "Manufacturing", EmployeeRange: "11-50", Phone: "+1 555 0100",
		Address: "1 Way", City: "Springfield", State: "IL", Country: "USA", PostalCode: "62701", Notes: "n", OwnerID: &owner}
	require.NoError(e.t, e.db.Create(company).Error)
	return company
}

// linkToCompany points a live lead, customer and deal at the company,
// directly, and returns them.
func (e *webhookEnv) linkToCompany(companyID uint) (*models.Lead, *models.Customer, *models.Deal) {
	e.t.Helper()
	lead := e.newLead()
	lead.CompanyID = &companyID
	require.NoError(e.t, e.db.Create(lead).Error)
	customer := e.newCustomer()
	customer.CompanyID = &companyID
	require.NoError(e.t, e.db.Create(customer).Error)
	deal := e.seedDeal(models.DealStageProposal)
	require.NoError(e.t, e.db.Model(&models.Deal{}).Where("id = ?", deal.ID).Update("company_id", companyID).Error)
	return lead, customer, deal
}

// companyLinks re-reads the company_id of the lead, customer and deal, outside
// any transaction.
func companyLinks(t *testing.T, db *gorm.DB, lead *models.Lead, customer *models.Customer, deal *models.Deal) [3]*uint {
	t.Helper()
	var l models.Lead
	require.NoError(t, db.Unscoped().First(&l, lead.ID).Error)
	var c models.Customer
	require.NoError(t, db.Unscoped().First(&c, customer.ID).Error)
	var d models.Deal
	require.NoError(t, db.Unscoped().First(&d, deal.ID).Error)
	return [3]*uint{l.CompanyID, c.CompanyID, d.CompanyID}
}

// companyAsHandlerSends is the company PUT /companies/{id} hands the service
// for a body that repeats the stored values: the service's read (owner
// preloaded, counts filled), with the owner record dropped as the handler
// drops it.
func companyAsHandlerSends(t *testing.T, env *webhookEnv, id uint) *models.Company {
	t.Helper()
	company, err := env.companies.GetByID(id)
	require.NoError(t, err)
	company.Owner = nil
	return company
}

func runCompanyWebhookFanOut(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	env.wire(env.repo)
	all := webhook.AllEventTypes()
	require.Len(t, all, 21)
	var allButDeleted []string
	for _, eventType := range all {
		if eventType != "company.deleted" {
			allButDeleted = append(allButDeleted, eventType)
		}
	}
	require.Len(t, allButDeleted, 20)
	// a: everything. b: company.deleted only, so it never receives a created
	// or updated event. c: every type but company.deleted. d: inactive,
	// everything.
	a := env.endpoint("https://example.com/a", true, all...)
	b := env.endpoint("https://example.com/b", true, "company.deleted")
	c := env.endpoint("https://example.com/c", true, allButDeleted...)
	d := env.endpoint("https://example.com/d", false, all...)
	noRowFor := func(t *testing.T, endpoints ...uint) {
		t.Helper()
		for _, id := range endpoints {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "endpoint_id = ?", id), "endpoint %d", id)
		}
	}

	t.Run("Create writes company.created", func(t *testing.T) {
		env.clearDeliveries()
		owner := env.owner.ID
		company := &models.Company{Name: "Webhook company", Domain: "https://WWW.Webhook-Company.example/", OwnerID: &owner}
		require.NoError(t, env.companies.Create(company))
		require.NotZero(t, company.ID)
		assert.Equal(t, "webhook-company.example", storedWebhookCompany(t, db, company.ID).Domain)
		assertFanOut(t, env, "company.created", company.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, b.ID, d.ID)
	})

	t.Run("a refused Create writes no row", func(t *testing.T) {
		env.clearDeliveries()
		taken := env.seedCompany()
		companies := env.count(&models.Company{}, "")
		require.ErrorIs(t, env.companies.Create(&models.Company{Name: "Duplicate", Domain: taken.Domain}), apperrors.ErrDuplicateCompanyDomain)
		assert.Equal(t, companies, env.count(&models.Company{}, ""))
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("an unchanged update writes no row", func(t *testing.T) {
		env.clearDeliveries()
		company := env.seedCompany()
		env.linkToCompany(company.ID)
		same := companyAsHandlerSends(t, env, company.ID)
		require.NotNil(t, same.OwnerID)
		require.Equal(t, int64(1), same.LeadCount, "the counts are filled")
		require.NoError(t, env.companies.Update(same))
		assert.Zero(t, env.allDeliveries(), "the stored values are no update")
	})

	t.Run("a changed text field writes company.updated", func(t *testing.T) {
		env.clearDeliveries()
		company := env.seedCompany()
		changed := companyAsHandlerSends(t, env, company.ID)
		changed.City = "Shelbyville"
		require.NoError(t, env.companies.Update(changed))
		assert.Equal(t, "Shelbyville", storedWebhookCompany(t, db, company.ID).City)
		assertFanOut(t, env, "company.updated", company.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
		noRowFor(t, b.ID, d.ID)
	})

	t.Run("clearing and setting owner_id writes company.updated", func(t *testing.T) {
		company := env.seedCompany()

		env.clearDeliveries()
		cleared := companyAsHandlerSends(t, env, company.ID)
		cleared.OwnerID = nil
		require.NoError(t, env.companies.Update(cleared))
		assert.Nil(t, storedWebhookCompany(t, db, company.ID).OwnerID)
		assertFanOut(t, env, "company.updated", company.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())

		env.clearDeliveries()
		set := companyAsHandlerSends(t, env, company.ID)
		owner := env.owner.ID
		set.OwnerID = &owner
		require.NoError(t, env.companies.Update(set))
		require.NotNil(t, storedWebhookCompany(t, db, company.ID).OwnerID)
		assertFanOut(t, env, "company.updated", company.ID, a.ID, c.ID)
		assert.Equal(t, int64(2), env.allDeliveries())
	})

	t.Run("an update of a company deleted meanwhile answers not-found and writes nothing", func(t *testing.T) {
		env.clearDeliveries()
		company := env.seedCompany()
		changed := companyAsHandlerSends(t, env, company.ID)
		changed.City = "Gone"
		require.NoError(t, db.Delete(&models.Company{}, company.ID).Error)
		err := env.companies.Update(changed)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Equal(t, "Springfield", storedWebhookCompany(t, db, company.ID).City)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Delete writes company.deleted only, for every subscribed endpoint", func(t *testing.T) {
		env.clearDeliveries()
		company := env.seedCompany()
		lead, customer, deal := env.linkToCompany(company.ID)
		require.NoError(t, env.companies.Delete(company.ID))
		assert.True(t, storedWebhookCompany(t, db, company.ID).DeletedAt.Valid, "soft-deleted")
		assert.Equal(t, [3]*uint{nil, nil, nil}, companyLinks(t, db, lead, customer, deal), "the links are cleared")
		// b never received an event for the company; it gets the deleted one.
		assertFanOut(t, env, "company.deleted", company.ID, a.ID, b.ID)
		assert.Equal(t, int64(2), env.allDeliveries(), "no other row")
		for _, eventType := range []string{"lead.updated", "customer.updated", "deal.updated"} {
			assert.Zero(t, env.count(&models.WebhookDelivery{}, "event_type = ?", eventType), eventType)
		}
		noRowFor(t, c.ID, d.ID)
	})

	t.Run("deleting a missing company is not-found and writes no row", func(t *testing.T) {
		env.clearDeliveries()
		err := env.companies.Delete(999999)
		require.Error(t, err)
		assert.ErrorIs(t, err, apperrors.ErrNotFound)
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("an unwired company service writes no row", func(t *testing.T) {
		env.clearDeliveries()
		plain := newWebhookEnv(t, db, nil)
		fresh := &models.Company{Name: fmt.Sprintf("Unwired company %d", webhookTestSeq.Add(1))}
		require.NoError(t, plain.companies.Create(fresh))
		changed := companyAsHandlerSends(t, plain, fresh.ID)
		changed.Notes = "renamed"
		require.NoError(t, plain.companies.Update(changed))
		require.NoError(t, plain.companies.Delete(fresh.ID))
		assert.True(t, storedWebhookCompany(t, db, fresh.ID).DeletedAt.Valid)
		assert.Zero(t, env.allDeliveries(), "no outbox row without the option")
	})
}

// runCompanyWebhookAtomicity: an outbox insert that fails rolls the company
// write back with it.
func runCompanyWebhookAtomicity(t *testing.T, db *gorm.DB) {
	env := newWebhookEnv(t, db, nil)
	all := webhook.AllEventTypes()
	env.endpoint("https://example.com/hook", true, all...)
	env.endpoint("https://example.com/hook/second", true, all...)
	env.wire(newFailingWebhookRepository(env.repo, injectedFailure, 0))

	t.Run("Create", func(t *testing.T) {
		companies := env.count(&models.Company{}, "")
		company := &models.Company{Name: "Lost company", Domain: "lost-company.example"}
		require.ErrorIs(t, env.companies.Create(company), injectedFailure)
		assert.Zero(t, company.ID, "the id is reset")
		assert.Equal(t, companies, env.count(&models.Company{}, ""), "no company row")
		assert.Zero(t, env.allDeliveries(), "every delivery row rolled back")
	})

	t.Run("Update", func(t *testing.T) {
		company := env.seedCompany()
		before := storedWebhookCompany(t, db, company.ID)
		changed := companyAsHandlerSends(t, env, company.ID)
		changed.Name = "Lost name"
		changed.OwnerID = nil
		require.ErrorIs(t, env.companies.Update(changed), injectedFailure)
		after := storedWebhookCompany(t, db, company.ID)
		assert.Equal(t, before.WebhookState(), after.WebhookState(), "the company rolled back")
		assert.Zero(t, env.allDeliveries())
	})

	t.Run("Delete", func(t *testing.T) {
		company := env.seedCompany()
		lead, customer, deal := env.linkToCompany(company.ID)
		require.ErrorIs(t, env.companies.Delete(company.ID), injectedFailure)
		assert.False(t, storedWebhookCompany(t, db, company.ID).DeletedAt.Valid, "the company stays live")
		id := company.ID
		assert.Equal(t, [3]*uint{&id, &id, &id}, companyLinks(t, db, lead, customer, deal), "the links stay")
		assert.Zero(t, env.allDeliveries())
	})
}
