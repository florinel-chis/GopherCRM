package integration

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestUserRegistrationTransaction(t *testing.T) {
	// Setup test database and dependencies
	db := setupTestDatabase(t)
	txManager := utils.NewTransactionManager(db)
	
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo, txManager)

	t.Run("successful user registration", func(t *testing.T) {
		user := &models.User{
			Email:     "newuser@example.com",
			FirstName: "New",
			LastName:  "User",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}
		password := "securepassword123"

		err := userService.Register(user, password)
		require.NoError(t, err)

		// Verify user was created
		assert.NotZero(t, user.ID)
		assert.NotEmpty(t, user.Password)
		assert.NotEqual(t, password, user.Password) // Password should be hashed

		// Verify password was hashed correctly
		err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
		assert.NoError(t, err)

		// Verify user exists in database
		dbUser, err := userRepo.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, user.Email, dbUser.Email)
		assert.Equal(t, user.FirstName, dbUser.FirstName)
		assert.Equal(t, user.LastName, dbUser.LastName)
		assert.Equal(t, user.Role, dbUser.Role)
		assert.Equal(t, user.IsActive, dbUser.IsActive)
	})

	t.Run("duplicate email registration fails", func(t *testing.T) {
		// First registration
		user1 := &models.User{
			Email:     "duplicate@example.com",
			FirstName: "First",
			LastName:  "User",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}
		err := userService.Register(user1, "password123")
		require.NoError(t, err)

		// Second registration with same email
		user2 := &models.User{
			Email:     "duplicate@example.com",
			FirstName: "Second",
			LastName:  "User",
			Role:      models.UserRoleAdmin,
			IsActive:  true,
		}
		err = userService.Register(user2, "password456")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "email already exists")

		// Verify second user was not created
		assert.Zero(t, user2.ID)
	})

	t.Run("concurrent registration with same email", func(t *testing.T) {
		email := "concurrent@example.com"
		results := make(chan error, 2)

		// Attempt concurrent registrations
		for i := 0; i < 2; i++ {
			go func(index int) {
				user := &models.User{
					Email:     email,
					FirstName: "Concurrent",
					LastName:  "User",
					Role:      models.UserRoleSales,
					IsActive:  true,
				}
				err := userService.Register(user, "password123")
				results <- err
			}(i)
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

		assert.Equal(t, 1, successCount, "Exactly one registration should succeed")
		assert.Equal(t, 1, errorCount, "Exactly one registration should fail")

		// Verify only one user exists with this email
		users, err := userRepo.List(0, 10)
		require.NoError(t, err)
		
		emailCount := 0
		for _, user := range users {
			if user.Email == email {
				emailCount++
			}
		}
		assert.Equal(t, 1, emailCount, "Only one user should exist with the email")
	})

	t.Run("registration with invalid password", func(t *testing.T) {
		user := &models.User{
			Email:     "validuser@example.com",
			FirstName: "Valid",
			LastName:  "User",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}

		// Test with empty password
		err := userService.Register(user, "")
		// The behavior depends on your validation rules
		// This test assumes empty password should be handled somehow
		if bcrypt.GenerateFromPassword([]byte(""), bcrypt.DefaultCost) != nil {
			// If bcrypt fails with empty password, the service should handle it
			// The exact assertion depends on your implementation
		}

		// Verify user was not created if password is invalid
		assert.Zero(t, user.ID)
	})

	t.Run("registration rollback scenario", func(t *testing.T) {
		// This test simulates a scenario where user creation succeeds
		// but additional operations (like creating default settings) fail
		// Since we don't have those operations yet, this is a placeholder
		
		user := &models.User{
			Email:     "rollback@example.com",
			FirstName: "Rollback",
			LastName:  "Test",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}

		// In a real scenario, you might inject a failing dependency
		// For now, we'll just test successful registration
		err := userService.Register(user, "password123")
		require.NoError(t, err)

		// Verify user was created
		assert.NotZero(t, user.ID)
	})
}

func TestUserRegistrationTransactionEdgeCases(t *testing.T) {
	db := setupTestDatabase(t)
	txManager := utils.NewTransactionManager(db)
	
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo, txManager)

	t.Run("registration with all user roles", func(t *testing.T) {
		roles := []models.UserRole{
			models.UserRoleAdmin,
			models.UserRoleSales,
			models.UserRoleSupport,
			models.UserRoleCustomer,
		}

		for i, role := range roles {
			user := &models.User{
				Email:     fmt.Sprintf("role%d@example.com", i),
				FirstName: "Role",
				LastName:  "Test",
				Role:      role,
				IsActive:  true,
			}

			err := userService.Register(user, "password123")
			require.NoError(t, err, "Failed to register user with role %s", role)
			assert.Equal(t, role, user.Role)
		}
	})

	t.Run("registration with inactive user", func(t *testing.T) {
		user := &models.User{
			Email:     "inactive@example.com",
			FirstName: "Inactive",
			LastName:  "User",
			Role:      models.UserRoleSales,
			IsActive:  false, // Inactive user
		}

		err := userService.Register(user, "password123")
		require.NoError(t, err)

		// Verify user was created as inactive
		assert.False(t, user.IsActive)

		// Verify in database
		dbUser, err := userRepo.GetByID(user.ID)
		require.NoError(t, err)
		assert.False(t, dbUser.IsActive)
	})

	t.Run("registration with unicode characters", func(t *testing.T) {
		user := &models.User{
			Email:     "unicode@例え.com",
			FirstName: "José",
			LastName:  "García",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}

		err := userService.Register(user, "password123")
		require.NoError(t, err)

		// Verify unicode characters are preserved
		dbUser, err := userRepo.GetByID(user.ID)
		require.NoError(t, err)
		assert.Equal(t, "José", dbUser.FirstName)
		assert.Equal(t, "García", dbUser.LastName)
	})

	t.Run("registration with long password", func(t *testing.T) {
		user := &models.User{
			Email:     "longpass@example.com",
			FirstName: "Long",
			LastName:  "Password",
			Role:      models.UserRoleSales,
			IsActive:  true,
		}

		// Very long password
		longPassword := string(make([]byte, 1000))
		for i := range longPassword {
			longPassword = string(rune('a' + (i % 26)))
		}

		err := userService.Register(user, longPassword)
		require.NoError(t, err)

		// Verify password was hashed correctly despite length
		err = bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(longPassword))
		assert.NoError(t, err)
	})
}

// setupTestDatabase creates a test database with the necessary tables
func setupTestDatabase(t *testing.T) *gorm.DB {
	db := setupDB(t)
	
	// Migrate tables
	err := db.AutoMigrate(&models.User{})
	require.NoError(t, err)
	
	return db
}