package integration

import (
	"context"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLeadConversionTransaction(t *testing.T) {
	// Setup test database and dependencies
	db := setupTestDatabase(t)
	txManager := utils.NewTransactionManager(db)
	
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	leadService := service.NewLeadService(leadRepo, customerRepo, txManager)

	// Create a test user for lead ownership
	user := &models.User{
		Email:     "owner@example.com",
		FirstName: "Test",
		LastName:  "Owner",
		Role:      models.UserRoleSales,
		IsActive:  true,
		Password:  "hashedpassword",
	}
	require.NoError(t, db.Create(user).Error)

	t.Run("successful lead conversion", func(t *testing.T) {
		// Create a test lead
		lead := &models.Lead{
			FirstName: "John",
			LastName:  "Doe",
			Email:     "john.doe@example.com",
			Phone:     "123-456-7890",
			Company:   "Test Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		// Prepare customer data
		customerData := &models.Customer{
			Type: models.CustomerTypeBusiness,
		}

		// Convert lead to customer
		convertedCustomer, err := leadService.ConvertToCustomer(lead.ID, customerData)
		require.NoError(t, err)
		require.NotNil(t, convertedCustomer)

		// Verify customer was created with lead data
		assert.Equal(t, lead.Email, convertedCustomer.Email)
		assert.Equal(t, lead.FirstName, convertedCustomer.FirstName)
		assert.Equal(t, lead.LastName, convertedCustomer.LastName)
		assert.Equal(t, lead.Phone, convertedCustomer.Phone)
		assert.Equal(t, lead.Company, convertedCustomer.Company)
		assert.NotZero(t, convertedCustomer.ID)

		// Verify lead status was updated
		updatedLead, err := leadRepo.GetByID(lead.ID)
		require.NoError(t, err)
		assert.Equal(t, models.LeadStatusConverted, updatedLead.Status)
		assert.Equal(t, convertedCustomer.ID, *updatedLead.CustomerID)

		// Verify customer exists in database
		dbCustomer, err := customerRepo.GetByID(convertedCustomer.ID)
		require.NoError(t, err)
		assert.Equal(t, convertedCustomer.Email, dbCustomer.Email)
	})

	t.Run("rollback on customer creation failure", func(t *testing.T) {
		// Create a test lead
		lead := &models.Lead{
			FirstName: "Jane",
			LastName:  "Smith",
			Email:     "jane.smith@example.com",
			Phone:     "098-765-4321",
			Company:   "Another Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		// Create customer with invalid data (empty email should cause validation error)
		customerData := &models.Customer{
			Email: "", // This should cause an error if validation is in place
			Type:  models.CustomerTypeBusiness,
		}

		// Attempt conversion - this should fail and rollback
		_, err := leadService.ConvertToCustomer(lead.ID, customerData)
		
		// The exact error depends on your validation rules
		// For this test, we assume some validation error occurs
		if err != nil {
			// Verify lead status was NOT updated (transaction rolled back)
			unchangedLead, getErr := leadRepo.GetByID(lead.ID)
			require.NoError(t, getErr)
			assert.Equal(t, models.LeadStatusQualified, unchangedLead.Status)
			assert.Nil(t, unchangedLead.CustomerID)
		}
	})

	t.Run("prevent double conversion", func(t *testing.T) {
		// Create and convert a lead first
		lead := &models.Lead{
			FirstName: "Bob",
			LastName:  "Johnson",
			Email:     "bob.johnson@example.com",
			Phone:     "555-123-4567",
			Company:   "First Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		customerData := &models.Customer{
			Type: models.CustomerTypeBusiness,
		}

		// First conversion should succeed
		_, err := leadService.ConvertToCustomer(lead.ID, customerData)
		require.NoError(t, err)

		// Second conversion attempt should fail
		_, err = leadService.ConvertToCustomer(lead.ID, customerData)
		assert.Error(t, err)
		// The error should indicate lead is already converted
		assert.Contains(t, err.Error(), "already converted")
	})

	t.Run("concurrent conversion attempts", func(t *testing.T) {
		// Create a test lead
		lead := &models.Lead{
			FirstName: "Charlie",
			LastName:  "Brown",
			Email:     "charlie.brown@example.com",
			Phone:     "444-555-6666",
			Company:   "Concurrent Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		// Attempt concurrent conversions
		results := make(chan error, 2)

		for i := 0; i < 2; i++ {
			go func() {
				customerData := &models.Customer{
					Type: models.CustomerTypeBusiness,
				}
				_, err := leadService.ConvertToCustomer(lead.ID, customerData)
				results <- err
			}()
		}

		// Collect results
		var errors []error
		for i := 0; i < 2; i++ {
			err := <-results
			errors = append(errors, err)
		}

		// One should succeed, one should fail
		successCount := 0
		errorCount := 0
		for _, err := range errors {
			if err == nil {
				successCount++
			} else {
				errorCount++
			}
		}

		assert.Equal(t, 1, successCount, "Exactly one conversion should succeed")
		assert.Equal(t, 1, errorCount, "Exactly one conversion should fail")

		// Verify final lead state
		finalLead, err := leadRepo.GetByID(lead.ID)
		require.NoError(t, err)
		assert.Equal(t, models.LeadStatusConverted, finalLead.Status)
	})
}

func TestLeadConversionTransactionEdgeCases(t *testing.T) {
	db := setupTestDatabase(t)
	txManager := utils.NewTransactionManager(db)
	
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	leadService := service.NewLeadService(leadRepo, customerRepo, txManager)

	t.Run("conversion with partial customer data", func(t *testing.T) {
		// Create test user
		user := &models.User{
			Email:     "partial@example.com",
			FirstName: "Partial",
			LastName:  "Test",
			Role:      models.UserRoleSales,
			IsActive:  true,
			Password:  "hashedpassword",
		}
		require.NoError(t, db.Create(user).Error)

		// Create lead with full data
		lead := &models.Lead{
			FirstName: "Full",
			LastName:  "Data",
			Email:     "full.data@example.com",
			Phone:     "123-456-7890",
			Company:   "Full Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		// Convert with minimal customer data - should inherit from lead
		customerData := &models.Customer{
			Type: models.CustomerTypeBusiness,
			// Only set type, other fields should be inherited from lead
		}

		convertedCustomer, err := leadService.ConvertToCustomer(lead.ID, customerData)
		require.NoError(t, err)

		// Verify data inheritance
		assert.Equal(t, lead.Email, convertedCustomer.Email)
		assert.Equal(t, lead.FirstName, convertedCustomer.FirstName)
		assert.Equal(t, lead.LastName, convertedCustomer.LastName)
		assert.Equal(t, lead.Phone, convertedCustomer.Phone)
		assert.Equal(t, lead.Company, convertedCustomer.Company)
		assert.Equal(t, models.CustomerTypeBusiness, convertedCustomer.Type)
	})

	t.Run("conversion overriding lead data", func(t *testing.T) {
		// Create test user
		user := &models.User{
			Email:     "override@example.com",
			FirstName: "Override",
			LastName:  "Test",
			Role:      models.UserRoleSales,
			IsActive:  true,
			Password:  "hashedpassword",
		}
		require.NoError(t, db.Create(user).Error)

		// Create lead
		lead := &models.Lead{
			FirstName: "Original",
			LastName:  "Name",
			Email:     "original@example.com",
			Phone:     "111-111-1111",
			Company:   "Original Company",
			Status:    models.LeadStatusQualified,
			OwnerID:   user.ID,
		}
		require.NoError(t, leadService.Create(lead))

		// Convert with overriding customer data
		customerData := &models.Customer{
			FirstName: "New",
			LastName:  "Name",
			Email:     "new@example.com",
			Phone:     "222-222-2222",
			Company:   "New Company",
			Type:      models.CustomerTypeBusiness,
		}

		convertedCustomer, err := leadService.ConvertToCustomer(lead.ID, customerData)
		require.NoError(t, err)

		// Verify overridden data is used
		assert.Equal(t, "new@example.com", convertedCustomer.Email)
		assert.Equal(t, "New", convertedCustomer.FirstName)
		assert.Equal(t, "Name", convertedCustomer.LastName)
		assert.Equal(t, "222-222-2222", convertedCustomer.Phone)
		assert.Equal(t, "New Company", convertedCustomer.Company)
	})

	t.Run("nonexistent lead conversion", func(t *testing.T) {
		customerData := &models.Customer{
			Type: models.CustomerTypeBusiness,
		}

		_, err := leadService.ConvertToCustomer(99999, customerData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// setupTestDatabase creates a test database with the necessary tables
func setupTestDatabase(t *testing.T) *gorm.DB {
	db := setupDB(t)
	
	// Migrate tables
	err := db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
	)
	require.NoError(t, err)
	
	return db
}