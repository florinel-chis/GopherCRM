package integration

import (
	"context"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The external identity mappings of a customer (#212) identify the person in
// another system, so the whole-schema sweep hunts for the person's external
// id too. For each erasure root — the customer, the lead converted into it and
// its customer-role login — the external id is seeded into
// customer_external_ids, the sweep sees it there before the erasure, and no
// table holds it afterwards.
func TestCustomerExternalIDErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	roots := []struct {
		name  string
		erase func(db *gorm.DB, login *models.User, customer *models.Customer, lead *models.Lead) error
	}{
		{"customer", func(db *gorm.DB, _ *models.User, customer *models.Customer, _ *models.Lead) error {
			return repository.NewCustomerRepository(db).Delete(customer.ID)
		}},
		{"converted_lead", func(db *gorm.DB, _ *models.User, _ *models.Customer, lead *models.Lead) error {
			return repository.NewLeadRepository(db).Delete(lead.ID)
		}},
		{"customer_login", func(db *gorm.DB, login *models.User, _ *models.Customer, _ *models.Lead) error {
			return repository.NewUserRepository(db).Delete(login.ID)
		}},
	}
	for _, root := range roots {
		root := root
		t.Run(root.name, func(t *testing.T) {
			db := setupFullSchemaDB(t)
			subject := newErasureSubject("extid-" + root.name)
			bystander := newErasureSubject("extid-bystander-" + root.name)
			login, customer, lead := seedCustomerWithLogin(t, db, subject)
			other := bystander.asCustomer(t, db)

			ctx := context.Background()
			mappings := repository.NewCustomerExternalIDRepository(db)
			require.NoError(t, mappings.Create(ctx, &models.CustomerExternalID{CustomerID: customer.ID, Source: "storefront", Scope: "", ExternalID: subject.ExternalID}))
			require.NoError(t, mappings.Create(ctx, &models.CustomerExternalID{CustomerID: customer.ID, Source: "erp", Scope: subject.LastName, ExternalID: "erp-" + subject.ExternalID}))
			require.NoError(t, mappings.Create(ctx, &models.CustomerExternalID{CustomerID: other.ID, Source: "storefront", Scope: "", ExternalID: bystander.ExternalID}))

			needles := append(subject.identifiers(), subject.ExternalID)
			before := tablesHolding(t, db, needles)
			require.Contains(t, before, "customer_external_ids", "the sweep must see the external id before the erasure")
			require.Contains(t, tablesHolding(t, db, []string{subject.ExternalID}), "customer_external_ids")

			require.NoError(t, root.erase(db, login, customer, lead))

			assertNoPersonalDataAnywhere(t, db, needles)
			var left int64
			require.NoError(t, db.Model(&models.CustomerExternalID{}).Where("customer_id = ?", customer.ID).Count(&left).Error)
			assert.Zero(t, left, "the erased customer's mappings are hard-deleted")

			// The sweep still works: the bystander's mapping is found.
			assert.Contains(t, tablesHolding(t, db, []string{bystander.ExternalID}), "customer_external_ids",
				"another customer's mapping is untouched")
		})
	}
}
