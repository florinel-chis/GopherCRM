package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The erasure of a customer hard-deletes its external identity mappings (#212)
// in the same unit, whichever root reaches it: DELETE /customers/{id}
// (CustomerRepository.Delete), the erasure of a lead converted into the
// customer, and the erasure of the customer's customer-role login. The
// fixtures run on the full production schema with foreign keys on; the MySQL
// root uses a disposable TEST_MYSQL_DSN schema, skips without it and fails
// when the server is unreachable.

func TestCustomerExternalIDErasureSQLite(t *testing.T) {
	runCustomerExternalIDErasure(t, openIdentityErasureDB(t, false))
}

func TestCustomerExternalIDErasureMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runCustomerExternalIDErasure(t, openIdentityErasureDB(t, true))
}

// externalIDPerson is one person stored as a customer, a lead converted into
// it and its customer-role login, with two mappings.
type externalIDPerson struct {
	tag      string
	customer *models.Customer
	lead     *models.Lead
	login    *models.User
	triples  [][3]string
}

func seedExternalIDPerson(t *testing.T, db *gorm.DB, owner *models.User) *externalIDPerson {
	t.Helper()
	tag := nextIdentityTag()
	p := &externalIDPerson{tag: tag}
	p.login = &models.User{Email: "extid-login-" + tag + "@person.test", Password: "$2a$10$x", FirstName: "Ext" + tag,
		LastName: "Login", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, db.Create(p.login).Error)
	p.customer = &models.Customer{FirstName: "Ext" + tag, LastName: "Person", Email: "extid-customer-" + tag + "@person.test",
		UserID: &p.login.ID}
	require.NoError(t, db.Create(p.customer).Error)
	p.lead = &models.Lead{FirstName: "Ext" + tag, LastName: "Person", Email: "extid-lead-" + tag + "@person.test",
		OwnerID: owner.ID, Status: models.LeadStatusConverted, CustomerID: &p.customer.ID}
	require.NoError(t, db.Create(p.lead).Error)
	p.triples = [][3]string{{"storefront", "site-" + tag, "EXT-" + tag}, {"erp", "", "erp-" + tag}}
	for _, triple := range p.triples {
		require.NoError(t, NewCustomerExternalIDRepository(db).Create(context.Background(),
			&models.CustomerExternalID{CustomerID: p.customer.ID, Source: triple[0], Scope: triple[1], ExternalID: triple[2]}))
	}
	return p
}

func mappingsOf(t *testing.T, db *gorm.DB, customerID uint) int64 {
	t.Helper()
	return countExternalIDs(t, db, "customer_id = ?", customerID)
}

// assertMappingsErased checks the person's rows are gone, the triples are
// free for another customer, and the bystander's mapping stayed.
func assertMappingsErased(t *testing.T, db *gorm.DB, p *externalIDPerson, bystander *externalIDPerson, reuser *models.Customer) {
	t.Helper()
	assert.Zero(t, mappingsOf(t, db, p.customer.ID), "the erased customer's mappings are hard-deleted")
	for _, triple := range p.triples {
		assert.Zero(t, countExternalIDs(t, db, "source = ? AND scope = ? AND external_id = ?", triple[0], triple[1], triple[2]))
	}
	assert.EqualValues(t, len(bystander.triples), mappingsOf(t, db, bystander.customer.ID), "other customers keep their mappings")

	var erased models.Customer
	require.NoError(t, db.Unscoped().First(&erased, p.customer.ID).Error)
	assert.True(t, erased.DeletedAt.Valid, "the customer is erased")

	repo := NewCustomerExternalIDRepository(db)
	for _, triple := range p.triples {
		require.NoError(t, repo.Create(context.Background(), &models.CustomerExternalID{
			CustomerID: reuser.ID, Source: triple[0], Scope: triple[1], ExternalID: triple[2]}),
			"the triple can be mapped again to another customer")
		found, err := repo.FindCustomerByIdentity(context.Background(), triple[0], triple[1], triple[2])
		require.NoError(t, err)
		require.NotNil(t, found)
		assert.Equal(t, reuser.ID, found.ID)
	}
}

// failOn registers a Delete callback on db that fails the statements on table
// while armed.
func failOn(t *testing.T, db *gorm.DB, name, table string, sentinel error) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != table {
			return
		}
		calls.Add(1)
		_ = tx.AddError(sentinel)
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(name) })
	return calls
}

func runCustomerExternalIDErasure(t *testing.T, db *gorm.DB) {
	owner := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSales)

	roots := []struct {
		name  string
		erase func(p *externalIDPerson) error
	}{
		{"customer", func(p *externalIDPerson) error { return NewCustomerRepository(db).Delete(p.customer.ID) }},
		{"converted_lead", func(p *externalIDPerson) error { return NewLeadRepository(db).Delete(p.lead.ID) }},
		{"customer_login", func(p *externalIDPerson) error { return NewUserRepository(db).Delete(p.login.ID) }},
	}
	for _, root := range roots {
		root := root
		t.Run(root.name, func(t *testing.T) {
			person := seedExternalIDPerson(t, db, owner)
			bystander := seedExternalIDPerson(t, db, owner)
			reuser := seedExternalIDCustomer(t, db, "reuse")
			require.EqualValues(t, 2, mappingsOf(t, db, person.customer.ID))

			require.NoError(t, root.erase(person))
			assertMappingsErased(t, db, person, bystander, reuser)
		})
	}

	t.Run("failure_after_the_mapping_delete_keeps_every_mapping", func(t *testing.T) {
		person := seedExternalIDPerson(t, db, owner)
		before := snapshotIdentityDB(t, db)
		sentinel := errors.New("injected failure soft-deleting the customer")
		// The soft delete of the customer row runs after the plan's AfterScrub,
		// that is after the mappings were deleted.
		calls := failOn(t, db, "customer_external_id_erasure_test:fail_customer_soft_delete", "customers", sentinel)

		err := NewCustomerRepository(db).Delete(person.customer.ID)
		require.ErrorIs(t, err, sentinel)
		require.Positive(t, calls.Load(), "the failure was injected")
		require.NoError(t, db.Callback().Delete().Remove("customer_external_id_erasure_test:fail_customer_soft_delete"))
		assert.Equal(t, before, snapshotIdentityDB(t, db), "a failed erasure leaves every row, the mappings included, in place")
		assert.EqualValues(t, 2, mappingsOf(t, db, person.customer.ID))
	})

	t.Run("withtx_caller_rollback_keeps_every_mapping", func(t *testing.T) {
		person := seedExternalIDPerson(t, db, owner)
		before := snapshotIdentityDB(t, db)
		rollback := errors.New("caller rolls back")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := NewCustomerRepository(db).WithTx(tx).Delete(person.customer.ID); err != nil {
				return err
			}
			var inside int64
			if err := tx.Model(&models.CustomerExternalID{}).Where("customer_id = ?", person.customer.ID).Count(&inside).Error; err != nil {
				return err
			}
			if inside != 0 {
				return fmt.Errorf("the erasure inside the transaction left %d mappings", inside)
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		assert.Equal(t, before, snapshotIdentityDB(t, db), "a rolled-back caller keeps every row")
		assert.EqualValues(t, 2, mappingsOf(t, db, person.customer.ID))
	})

	t.Run("mapping_delete_failure_rolls_back_and_names_table_and_id_only", func(t *testing.T) {
		person := seedExternalIDPerson(t, db, owner)
		before := snapshotIdentityDB(t, db)
		sentinel := errors.New("injected failure deleting mappings")
		name := "customer_external_id_erasure_test:fail_mapping_delete"
		calls := failOn(t, db, name, "customer_external_ids", sentinel)

		err := NewCustomerRepository(db).Delete(person.customer.ID)
		require.ErrorIs(t, err, sentinel)
		require.EqualValues(t, 1, calls.Load(), "the failure was injected into the mapping delete")
		require.NoError(t, db.Callback().Delete().Remove(name))
		message := err.Error()
		assert.Contains(t, message, "customer_external_ids")
		assert.Contains(t, message, fmt.Sprintf("customer %d", person.customer.ID))
		for _, triple := range person.triples {
			for _, value := range triple {
				if value != "" {
					assert.NotContains(t, message, value, "the error names the table and the customer id only")
				}
			}
		}
		assert.NotContains(t, message, person.customer.Email)
		assert.False(t, strings.Contains(message, person.customer.FirstName))
		assert.Equal(t, before, snapshotIdentityDB(t, db), "the erasure rolled back")
	})
}

// TestCustomerExternalIDErasureWithoutTable erases on a schema that has no
// customer_external_ids table: the migrator probe skips the purge.
func TestCustomerExternalIDErasureWithoutTable(t *testing.T) {
	// Tickets and their comments are in the subset because a login's erasure
	// redacts the comments it wrote.
	db := openBusinessTextSubsetDB(t, &models.Ticket{}, &models.TicketComment{})
	require.False(t, db.Migrator().HasTable(&models.CustomerExternalID{}), "the fixture must not have the table")
	tag := nextIdentityTag()
	owner := seedIdentityStaff(t, db, tag, models.RoleSales)

	erased := func(t *testing.T, customerID uint) {
		t.Helper()
		var c models.Customer
		require.NoError(t, db.Unscoped().First(&c, customerID).Error)
		assert.True(t, c.DeletedAt.Valid)
		assert.Empty(t, c.FirstName)
		assert.True(t, strings.HasSuffix(c.Email, "@"+anonymizedEmailDomain))
	}
	newPerson := func(label string) (*models.User, *models.Customer, *models.Lead) {
		login := &models.User{Email: "nt-login-" + label + tag + "@person.test", Password: "x", FirstName: "Nt", LastName: "Login",
			Role: models.RoleCustomer, IsActive: true}
		require.NoError(t, db.Create(login).Error)
		customer := &models.Customer{FirstName: "Nt" + label, LastName: "Person", Email: "nt-" + label + tag + "@person.test", UserID: &login.ID}
		require.NoError(t, db.Create(customer).Error)
		lead := &models.Lead{FirstName: "Nt" + label, LastName: "Lead", Email: "nt-lead-" + label + tag + "@person.test",
			OwnerID: owner.ID, Status: models.LeadStatusConverted, CustomerID: &customer.ID}
		require.NoError(t, db.Create(lead).Error)
		return login, customer, lead
	}

	_, c1, _ := newPerson("a")
	require.NoError(t, NewCustomerRepository(db).Delete(c1.ID))
	erased(t, c1.ID)

	_, c2, l2 := newPerson("b")
	require.NoError(t, NewLeadRepository(db).Delete(l2.ID))
	erased(t, c2.ID)

	u3, c3, _ := newPerson("c")
	require.NoError(t, NewUserRepository(db).Delete(u3.ID))
	erased(t, c3.ID)
}
