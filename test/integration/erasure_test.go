package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// These tests pin down the GDPR Article 17 "right to erasure" behaviour of
// deleting a user or a customer.
//
// The contract under test:
//   - deleting scrubs the personal data IN PLACE and only then soft-deletes,
//     so the row survives for foreign-key integrity while the person does not
//     survive in it;
//   - the placeholder address is unique and non-routable, so the unchanged
//     unique index on the email column is satisfied and the original address
//     becomes free again;
//   - deactivation (is_active = false) is a completely different, harmless
//     operation and must never anonymise anything.

const erasedEmailSuffix = "@anonymized.invalid"

func setupErasureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDB(t)
	require.NoError(t, db.AutoMigrate(
		&models.User{},
		&models.Customer{},
		&models.APIKey{},
		&models.RefreshToken{},
		&models.Ticket{},
		&models.Task{},
	))
	return db
}

func seedUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Str0ng!Passw0rd"), bcrypt.MinCost)
	require.NoError(t, err)

	lastLogin := time.Now().Add(-time.Hour)
	user := &models.User{
		Email:               email,
		Password:            string(hash),
		FirstName:           "Erasure",
		LastName:            "Subject",
		Role:                models.RoleSales,
		IsActive:            true,
		LastLoginAt:         &lastLogin,
		FailedLoginAttempts: 3,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func seedCustomer(t *testing.T, db *gorm.DB, email string) *models.Customer {
	t.Helper()
	customer := &models.Customer{
		FirstName:  "Erasure",
		LastName:   "Subject",
		Email:      email,
		Phone:      "+40 721 000 111",
		Company:    "Subject Industries",
		Position:   "Head of Records",
		Address:    "12 Privacy Lane",
		City:       "Bucharest",
		State:      "Bucuresti",
		Country:    "Romania",
		PostalCode: "010101",
		Notes:      "Prefers to be called on the mobile number above.",
	}
	require.NoError(t, db.Create(customer).Error)
	return customer
}

// fetchErased reads a row that is expected to be soft-deleted, bypassing the
// default scope. Ordinary queries cannot see it, which is exactly why the
// erasure has to be verified with Unscoped.
func fetchErasedUser(t *testing.T, db *gorm.DB, id uint) models.User {
	t.Helper()
	var user models.User
	require.NoError(t, db.Unscoped().First(&user, id).Error)
	return user
}

func fetchErasedCustomer(t *testing.T, db *gorm.DB, id uint) models.Customer {
	t.Helper()
	var customer models.Customer
	require.NoError(t, db.Unscoped().First(&customer, id).Error)
	return customer
}

// assertColumnsFreeOf dumps every row of a table as raw column values and fails
// if any of them still contains a forbidden string. This is deliberately
// stricter than checking the fields we know about: it also catches personal
// data left in a column that the erasure code forgot.
func assertColumnsFreeOf(t *testing.T, db *gorm.DB, table string, forbidden ...string) {
	t.Helper()
	var rows []map[string]interface{}
	require.NoError(t, db.Table(table).Unscoped().Find(&rows).Error)

	require.NotEmpty(t, rows, "expected at least the erased row to still exist in %s", table)
	for _, row := range rows {
		for column, value := range row {
			text, ok := value.(string)
			if !ok {
				continue
			}
			for _, needle := range forbidden {
				assert.NotContains(t, strings.ToLower(text), strings.ToLower(needle),
					"%s.%s still holds personal data %q", table, column, needle)
			}
		}
	}
}

// --- Users -------------------------------------------------------------------

func TestUserErasureRemovesPersonalDataFromTheTable(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "erase-me@example.com")
	originalHash := user.Password

	require.NoError(t, userRepo.Delete(user.ID))

	// Nothing anywhere in the users table may still carry the address or name,
	// including the soft-deleted rows an ordinary query would hide.
	assertColumnsFreeOf(t, db, "users", "erase-me@example.com", "Erasure", "Subject", originalHash)
}

func TestUserErasureKeepsTheRowSoftDeletedWithAPlaceholderEmail(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "still-there@example.com")
	require.NoError(t, userRepo.Delete(user.ID))

	// The row must survive so foreign keys pointing at it stay valid.
	erased := fetchErasedUser(t, db, user.ID)
	assert.True(t, erased.DeletedAt.Valid, "the row must remain, soft-deleted")
	assert.Equal(t, user.ID, erased.ID)

	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix),
		"expected an %s placeholder, got %q", erasedEmailSuffix, erased.Email)
	assert.NotContains(t, erased.Email, "still-there",
		"the placeholder must not be derived from the original address")

	// A scoped lookup must not resurrect it.
	_, err := userRepo.GetByEmail(erased.Email)
	assert.Error(t, err)
}

func TestErasedUserEmailCanBeRegisteredAgain(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)

	original := &models.User{
		Email:     "reusable@example.com",
		FirstName: "First",
		LastName:  "Tenant",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, userService.Register(original, "Str0ng!Passw0rd"))
	require.NoError(t, userRepo.Delete(original.ID))

	// The address is no longer held by anything, so the same person (or anyone
	// else) can register it again. Before erasure the unique index kept it
	// locked forever.
	replacement := &models.User{
		Email:     "reusable@example.com",
		FirstName: "Second",
		LastName:  "Tenant",
		Role:      models.RoleCustomer,
		IsActive:  true,
	}
	require.NoError(t, userService.Register(replacement, "Str0ng!Passw0rd"),
		"the erased address must be free for re-registration")
	assert.NotZero(t, replacement.ID)
	assert.NotEqual(t, original.ID, replacement.ID)

	found, err := userRepo.GetByEmail("reusable@example.com")
	require.NoError(t, err)
	assert.Equal(t, replacement.ID, found.ID)
	assert.Equal(t, "Second", found.FirstName)
}

func TestUserErasureScrubsEveryPersonalFieldAndDisablesThePassword(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "scrub-me@example.com")
	lockedUntil := time.Now().Add(time.Hour)
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", user.ID).
		Update("locked_until", &lockedUntil).Error)

	require.NoError(t, userRepo.Delete(user.ID))

	erased := fetchErasedUser(t, db, user.ID)
	assert.Empty(t, erased.FirstName)
	assert.Empty(t, erased.LastName)
	assert.Nil(t, erased.LastLoginAt)
	assert.Nil(t, erased.LockedUntil)
	assert.Zero(t, erased.FailedLoginAttempts)
	assert.False(t, erased.IsActive)

	// The stored hash must be unusable: bcrypt has to reject the real password
	// and anything else. A retained hash is both personal data and crackable.
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(erased.Password), []byte("Str0ng!Passw0rd")),
		"the original password must no longer verify")
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(erased.Password), []byte("")),
		"the erased hash must not verify an empty password either")
}

func TestUserErasureDestroysCredentialsThatWouldOutliveTheAccount(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "credentials@example.com")
	survivor := seedUser(t, db, "survivor@example.com")

	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-erased", Prefix: "gcrm_abc", UserID: user.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-survivor", Prefix: "gcrm_xyz", UserID: survivor.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: user.ID, TokenHash: "refresh-erased", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: survivor.ID, TokenHash: "refresh-survivor", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)

	require.NoError(t, userRepo.Delete(user.ID))

	// Gone for good — not merely soft-deleted, or the secret would still be
	// stored and the key would still hash-match on lookup.
	var keys int64
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Zero(t, keys, "API keys of an erased user must not survive")

	var tokens int64
	require.NoError(t, db.Unscoped().Model(&models.RefreshToken{}).Where("user_id = ?", user.ID).Count(&tokens).Error)
	assert.Zero(t, tokens, "refresh tokens of an erased user must not survive")

	// Another user's credentials must be untouched.
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", survivor.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys)
	require.NoError(t, db.Unscoped().Model(&models.RefreshToken{}).Where("user_id = ?", survivor.ID).Count(&tokens).Error)
	assert.Equal(t, int64(1), tokens)
}

// The purge is unconditional: if it cannot run, the erasure FAILS. It used to
// be guarded by a HasTable probe that reported "no such table" for a transient
// query failure just as readily as for a genuinely absent table — so a blip
// skipped the purge and the transaction still committed, leaving an anonymised
// user whose API keys went on authenticating. A database that cannot delete the
// credentials must abort the whole erasure instead.
func TestUserErasureFailsAndRollsBackWhenCredentialsCannotBePurged(t *testing.T) {
	db := setupDB(t)
	// Deliberately incomplete schema: refresh_tokens is missing, so the purge
	// statement errors out exactly like a failing query would.
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.APIKey{}))
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "purge-failure@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-must-survive-the-rollback", Prefix: "gcrm_abc",
		UserID: user.ID, IsActive: true,
	}).Error)

	err := userRepo.Delete(user.ID)
	require.Error(t, err, "an erasure that cannot purge credentials must not report success")

	// Everything must be back as it was: a half-done erasure that anonymises the
	// person but keeps their working credentials is the worst possible outcome.
	var reloaded models.User
	require.NoError(t, db.First(&reloaded, user.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid, "the failed erasure must have rolled back")
	assert.Equal(t, "purge-failure@example.com", reloaded.Email)
	assert.Equal(t, "Erasure", reloaded.FirstName)

	var keys int64
	require.NoError(t, db.Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys, "the rollback must leave the credential row untouched")
}

// Repositories publish WithTx, so the handle a repository holds may already BE a
// transaction. Delete used to call Begin on it unconditionally, which is invalid
// on a transaction handle and failed outright. It must now join the caller's
// transaction instead.
func TestUserErasureRunsInsideACallerSuppliedTransaction(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "caller-tx@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-in-caller-tx", Prefix: "gcrm_abc",
		UserID: user.ID, IsActive: true,
	}).Error)

	// A caller composing the erasure with its own work in a single unit of work.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := userRepo.WithTx(tx).Delete(user.ID); err != nil {
			return err
		}
		return tx.Create(&models.Task{
			Title:        "Confirm the erasure with the data subject",
			Status:       models.TaskStatusPending,
			Priority:     models.TaskPriorityHigh,
			AssignedToID: user.ID,
		}).Error
	}))

	erased := fetchErasedUser(t, db, user.ID)
	assert.True(t, erased.DeletedAt.Valid)
	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix))
	assert.Empty(t, erased.FirstName)

	var keys int64
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Zero(t, keys, "the credential purge must run inside the caller's transaction too")
}

// ...and having joined it, the erasure must share its fate. If Delete quietly
// opened and committed a transaction of its own, the caller's rollback would
// leave the person erased anyway — an irreversible side effect of an operation
// the caller abandoned.
func TestUserErasureRollsBackWithTheCallersTransaction(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "aborted-tx@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-survives-abort", Prefix: "gcrm_abc",
		UserID: user.ID, IsActive: true,
	}).Error)

	abort := errors.New("caller aborted the unit of work")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := userRepo.WithTx(tx).Delete(user.ID); err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)

	var reloaded models.User
	require.NoError(t, db.First(&reloaded, user.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "aborted-tx@example.com", reloaded.Email)
	assert.Equal(t, "Erasure", reloaded.FirstName)

	var keys int64
	require.NoError(t, db.Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys)
}

func TestCustomerErasureRunsInsideACallerSuppliedTransaction(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "customer-caller-tx@example.com")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return customerRepo.WithTx(tx).Delete(customer.ID)
	}))

	erased := fetchErasedCustomer(t, db, customer.ID)
	assert.True(t, erased.DeletedAt.Valid)
	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix))
	assert.Empty(t, erased.Notes)
}

func TestCustomerErasureRollsBackWithTheCallersTransaction(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "customer-aborted-tx@example.com")

	abort := errors.New("caller aborted the unit of work")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := customerRepo.WithTx(tx).Delete(customer.ID); err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)

	var reloaded models.Customer
	require.NoError(t, db.First(&reloaded, customer.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid)
	assert.Equal(t, "customer-aborted-tx@example.com", reloaded.Email)
	assert.Equal(t, "Prefers to be called on the mobile number above.", reloaded.Notes)
}

// Erasing somebody who is not there is not an erasure. Both statements match by
// primary key and matching nothing is not an SQL error, so the service has to
// say so itself — otherwise an operator records a completed Article 17 erasure
// that never happened.
func TestErasingAUserThatDoesNotExistReportsNotFound(t *testing.T) {
	db := setupErasureDB(t)
	userService := service.NewUserService(repository.NewUserRepository(db))

	err := userService.Delete(99999)

	require.Error(t, err, "erasing a nonexistent user must not report success")
	assert.True(t, apperrors.IsNotFound(err), "expected the not-found sentinel, got %v", err)
}

func TestErasingACustomerThatDoesNotExistReportsNotFound(t *testing.T) {
	db := setupErasureDB(t)
	customerService := service.NewCustomerService(
		repository.NewCustomerRepository(db),
		repository.NewUserRepository(db),
	)

	err := customerService.Delete(99999)

	require.Error(t, err)
	assert.True(t, apperrors.IsNotFound(err), "expected the not-found sentinel, got %v", err)
}

// The service reports the missing row (above); the repository below it stays
// silent, because "erase the row with this id" is satisfied when there is no
// such row. What it must NOT do is touch anybody else. Both statements of an
// erasure select by primary key, and a broadened or dropped WHERE clause would
// turn one erasure request into a mass anonymisation that no test above would
// notice — the erased person's own assertions would all still pass.
func TestErasingAUserIDThatDoesNotExistTouchesNobodyElse(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	first := seedUser(t, db, "bystander-1@example.com")
	second := seedUser(t, db, "bystander-2@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-bystander", Prefix: "gcrm_bys", UserID: first.ID, IsActive: true,
	}).Error)

	require.NoError(t, userRepo.Delete(99999), "there was nothing to erase, which is not a failure")

	for _, id := range []uint{first.ID, second.ID} {
		var user models.User
		require.NoError(t, db.First(&user, id).Error, "an unrelated account was erased")
		assert.False(t, user.DeletedAt.Valid)
		assert.Equal(t, "Erasure", user.FirstName)
		assert.NotContains(t, user.Email, erasedEmailSuffix)
		assert.True(t, user.IsActive)
	}

	var keys int64
	require.NoError(t, db.Model(&models.APIKey{}).Where("user_id = ?", first.ID).Count(&keys).Error)
	assert.Equal(t, int64(1), keys, "an unrelated account's credentials were purged")
}

func TestErasingACustomerIDThatDoesNotExistTouchesNobodyElse(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	first := seedCustomer(t, db, "customer-bystander-1@example.com")
	second := seedCustomer(t, db, "customer-bystander-2@example.com")

	require.NoError(t, customerRepo.Delete(99999))

	for _, id := range []uint{first.ID, second.ID} {
		var customer models.Customer
		require.NoError(t, db.First(&customer, id).Error, "an unrelated customer was erased")
		assert.False(t, customer.DeletedAt.Valid)
		assert.Equal(t, "Erasure", customer.FirstName)
		assert.Equal(t, "Prefers to be called on the mobile number above.", customer.Notes)
	}
}

func TestErasingALeadIDThatDoesNotExistTouchesNobodyElse(t *testing.T) {
	db := setupLeadErasureDB(t)
	leadRepo := repository.NewLeadRepository(db)

	owner := seedLeadOwner(t, db)
	lead := seedLead(t, db, owner.ID, "lead-bystander@example.com")
	customer := seedCustomer(t, db, "lead-bystander-customer@example.com")

	require.NoError(t, leadRepo.Delete(99999))

	var reloaded models.Lead
	require.NoError(t, db.First(&reloaded, lead.ID).Error, "an unrelated lead was erased")
	assert.Equal(t, "Ingrid", reloaded.FirstName)
	assert.Equal(t, "lead-bystander@example.com", reloaded.Email)

	var reloadedCustomer models.Customer
	require.NoError(t, db.First(&reloadedCustomer, customer.ID).Error)
	assert.Equal(t, "lead-bystander-customer@example.com", reloadedCustomer.Email)
}

// Rows soft-deleted by an older release still hold everything: back then a
// deletion only hid the person. Those rows are the reason the scrub is Unscoped
// — a scoped UPDATE would match nothing, report success, and leave the personal
// data of everyone deleted before this feature shipped in the database forever
// (see scripts/anonymize_legacy_deleted_pii.sql, which exists for exactly the
// rows this path has to be able to fix).
func TestErasingAUserThatWasAlreadySoftDeletedStillScrubsIt(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "legacy-soft-deleted@example.com")
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "legacy-hash", Prefix: "gcrm_leg", UserID: user.ID, IsActive: true,
	}).Error)

	// A plain soft delete, the way the previous release deleted people.
	require.NoError(t, db.Delete(&models.User{}, user.ID).Error)
	legacy := fetchErasedUser(t, db, user.ID)
	require.Equal(t, "legacy-soft-deleted@example.com", legacy.Email,
		"the fixture must reproduce the old behaviour: hidden, not erased")

	require.NoError(t, userRepo.Delete(user.ID))

	assertColumnsFreeOf(t, db, "users", "legacy-soft-deleted@example.com", "Erasure", "Subject")
	erased := fetchErasedUser(t, db, user.ID)
	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix))
	assert.True(t, erased.DeletedAt.Valid)

	// The credentials the old deletion left behind go too.
	var keys int64
	require.NoError(t, db.Unscoped().Model(&models.APIKey{}).Where("user_id = ?", user.ID).Count(&keys).Error)
	assert.Zero(t, keys)
}

func TestErasingACustomerThatWasAlreadySoftDeletedStillScrubsIt(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "legacy-customer@example.com")
	require.NoError(t, db.Delete(&models.Customer{}, customer.ID).Error)
	require.Equal(t, "legacy-customer@example.com", fetchErasedCustomer(t, db, customer.ID).Email)

	require.NoError(t, customerRepo.Delete(customer.ID))

	assertColumnsFreeOf(t, db, "customers",
		"legacy-customer@example.com", "Erasure", "Subject", "+40 721 000 111", "Prefers to be called")
	assert.True(t, strings.HasSuffix(fetchErasedCustomer(t, db, customer.ID).Email, erasedEmailSuffix))
}

// Defence in depth for the credential purge: even a key that somehow outlives
// its owner — restored from a backup, written by an older release — must not
// authenticate.
func TestAPIKeyOfAnErasedUserCannotAuthenticate(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)

	const apiKeySecret = "test-api-key-secret"
	authService := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{
		Secret:      "test-secret-key-at-least-32-characters-long",
		ExpiryHours: 24,
	}, apiKeySecret)

	user := seedUser(t, db, "keyholder@example.com")
	const rawKey = "0123456789abcdef0123456789abcdef"
	createKey := func() {
		require.NoError(t, db.Create(&models.APIKey{
			Name:     "ci",
			KeyHash:  utils.HashAPIKeyHMAC(rawKey, apiKeySecret),
			Prefix:   rawKey[:8],
			UserID:   user.ID,
			IsActive: true,
		}).Error)
	}
	createKey()

	// Sanity check: it works while the account is live.
	authenticated, err := authService.ValidateAPIKey(rawKey)
	require.NoError(t, err)
	require.Equal(t, user.ID, authenticated.ID)

	require.NoError(t, userRepo.Delete(user.ID))

	// The purge removed it, so it cannot even be found...
	_, err = authService.ValidateAPIKey(rawKey)
	assert.Error(t, err)

	// ...and if a copy of it comes back from somewhere, it still must not work.
	createKey()
	authenticated, err = authService.ValidateAPIKey(rawKey)
	assert.Error(t, err, "a key whose owner has been erased must not authenticate")
	assert.Nil(t, authenticated)
}

// The same guard covers the non-destructive path: deactivating an account must
// take its API keys out of service too, not just its password.
func TestAPIKeyOfADeactivatedUserCannotAuthenticate(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)

	const apiKeySecret = "test-api-key-secret"
	authService := service.NewAuthService(userRepo, apiKeyRepo, config.JWTConfig{
		Secret:      "test-secret-key-at-least-32-characters-long",
		ExpiryHours: 24,
	}, apiKeySecret)

	user := seedUser(t, db, "suspended-keyholder@example.com")
	const rawKey = "fedcba9876543210fedcba9876543210"
	require.NoError(t, db.Create(&models.APIKey{
		Name:     "ci",
		KeyHash:  utils.HashAPIKeyHMAC(rawKey, apiKeySecret),
		Prefix:   rawKey[:8],
		UserID:   user.ID,
		IsActive: true,
	}).Error)

	require.NoError(t, db.Model(&models.User{}).Where("id = ?", user.ID).
		Update("is_active", false).Error)

	authenticated, err := authService.ValidateAPIKey(rawKey)
	assert.Error(t, err, "a key of a deactivated user must not authenticate")
	assert.Nil(t, authenticated)
}

func TestUserErasureDoesNotCascadeAwayBusinessRecords(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	user := seedUser(t, db, "owner@example.com")
	task := &models.Task{
		Title:        "Follow up on the renewal",
		Status:       models.TaskStatusPending,
		Priority:     models.TaskPriorityHigh,
		AssignedToID: user.ID,
	}
	require.NoError(t, db.Create(task).Error)

	require.NoError(t, userRepo.Delete(user.ID))

	// The task is business history and must be untouched...
	var reloaded models.Task
	require.NoError(t, db.First(&reloaded, task.ID).Error)
	assert.Equal(t, "Follow up on the renewal", reloaded.Title)
	assert.Equal(t, user.ID, reloaded.AssignedToID)

	// ...and its foreign key must still resolve to a real (anonymised) row.
	referenced := fetchErasedUser(t, db, reloaded.AssignedToID)
	assert.Equal(t, user.ID, referenced.ID)
	assert.True(t, strings.HasSuffix(referenced.Email, erasedEmailSuffix))
}

func TestUserDeactivationDoesNotAnonymiseAnything(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)

	user := seedUser(t, db, "suspended@example.com")
	originalHash := user.Password

	// Deactivation is the non-destructive path: it suspends the account and
	// nothing else.
	updated, err := userService.Update(user.ID, map[string]interface{}{"is_active": false})
	require.NoError(t, err)
	assert.False(t, updated.IsActive)

	var reloaded models.User
	require.NoError(t, db.First(&reloaded, user.ID).Error)
	assert.False(t, reloaded.DeletedAt.Valid, "deactivation must not delete the row")
	assert.Equal(t, "suspended@example.com", reloaded.Email, "deactivation must not touch the email")
	assert.Equal(t, "Erasure", reloaded.FirstName)
	assert.Equal(t, "Subject", reloaded.LastName)
	assert.Equal(t, originalHash, reloaded.Password, "deactivation must not destroy the password hash")
	assert.NotNil(t, reloaded.LastLoginAt)

	// It is reversible, unlike erasure.
	reactivated, err := userService.Update(user.ID, map[string]interface{}{"is_active": true})
	require.NoError(t, err)
	assert.True(t, reactivated.IsActive)
	assert.Equal(t, "suspended@example.com", reactivated.Email)
}

func TestSuccessiveUserErasuresProduceDistinctPlaceholders(t *testing.T) {
	db := setupErasureDB(t)
	userRepo := repository.NewUserRepository(db)

	placeholders := make(map[string]struct{})
	for i := 0; i < 5; i++ {
		user := seedUser(t, db, fmt.Sprintf("bulk-%d@example.com", i))
		require.NoError(t, userRepo.Delete(user.ID),
			"erasure %d failed — a colliding placeholder would violate the unique index", i)

		erased := fetchErasedUser(t, db, user.ID)
		require.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix))
		_, seen := placeholders[erased.Email]
		assert.False(t, seen, "placeholder %q was reused; the unique index forbids that", erased.Email)
		placeholders[erased.Email] = struct{}{}
	}
	assert.Len(t, placeholders, 5)
}

// --- Customers ---------------------------------------------------------------

func TestCustomerErasureRemovesPersonalDataFromTheTable(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "erase-customer@example.com")
	require.NoError(t, customerRepo.Delete(customer.ID))

	assertColumnsFreeOf(t, db, "customers",
		"erase-customer@example.com", "Erasure", "Subject", "+40 721 000 111",
		"Subject Industries", "Head of Records", "12 Privacy Lane", "Bucharest",
		"Bucuresti", "Romania", "010101", "Prefers to be called")
}

func TestCustomerErasureKeepsTheRowSoftDeletedWithAPlaceholderEmail(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "customer-still-there@example.com")
	require.NoError(t, customerRepo.Delete(customer.ID))

	erased := fetchErasedCustomer(t, db, customer.ID)
	assert.True(t, erased.DeletedAt.Valid, "the row must remain, soft-deleted")
	assert.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix),
		"expected an %s placeholder, got %q", erasedEmailSuffix, erased.Email)
	assert.NotContains(t, erased.Email, "customer-still-there")

	assert.Empty(t, erased.FirstName)
	assert.Empty(t, erased.LastName)
	assert.Empty(t, erased.Phone)
	assert.Empty(t, erased.Company)
	assert.Empty(t, erased.Position)
	assert.Empty(t, erased.Address)
	assert.Empty(t, erased.City)
	assert.Empty(t, erased.State)
	assert.Empty(t, erased.Country)
	assert.Empty(t, erased.PostalCode)
	assert.Empty(t, erased.Notes)
}

func TestErasedCustomerEmailCanBeUsedAgain(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)
	userRepo := repository.NewUserRepository(db)
	customerService := service.NewCustomerService(customerRepo, userRepo)

	original := &models.Customer{FirstName: "First", LastName: "Tenant", Email: "reusable-customer@example.com"}
	require.NoError(t, customerService.Create(original))
	require.NoError(t, customerRepo.Delete(original.ID))

	replacement := &models.Customer{FirstName: "Second", LastName: "Tenant", Email: "reusable-customer@example.com"}
	require.NoError(t, customerService.Create(replacement),
		"the erased address must be free for re-use")
	assert.NotZero(t, replacement.ID)
	assert.NotEqual(t, original.ID, replacement.ID)
}

func TestCustomerErasureDoesNotCascadeAwayTickets(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	customer := seedCustomer(t, db, "ticket-owner@example.com")
	ticket := &models.Ticket{
		Title:       "Cannot log in",
		Description: "Reported by phone.",
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityHigh,
		CustomerID:  customer.ID,
	}
	require.NoError(t, db.Create(ticket).Error)

	require.NoError(t, customerRepo.Delete(customer.ID))

	var reloaded models.Ticket
	require.NoError(t, db.First(&reloaded, ticket.ID).Error)
	assert.Equal(t, "Cannot log in", reloaded.Title)
	assert.Equal(t, customer.ID, reloaded.CustomerID)

	referenced := fetchErasedCustomer(t, db, reloaded.CustomerID)
	assert.Equal(t, customer.ID, referenced.ID)
	assert.True(t, strings.HasSuffix(referenced.Email, erasedEmailSuffix))
}

func TestSuccessiveCustomerErasuresProduceDistinctPlaceholders(t *testing.T) {
	db := setupErasureDB(t)
	customerRepo := repository.NewCustomerRepository(db)

	placeholders := make(map[string]struct{})
	for i := 0; i < 5; i++ {
		customer := seedCustomer(t, db, fmt.Sprintf("bulk-customer-%d@example.com", i))
		require.NoError(t, customerRepo.Delete(customer.ID),
			"erasure %d failed — a colliding placeholder would violate the unique index", i)

		erased := fetchErasedCustomer(t, db, customer.ID)
		require.True(t, strings.HasSuffix(erased.Email, erasedEmailSuffix),
			"expected an %s placeholder, got %q", erasedEmailSuffix, erased.Email)
		_, seen := placeholders[erased.Email]
		assert.False(t, seen, "placeholder %q was reused", erased.Email)
		placeholders[erased.Email] = struct{}{}
	}
	assert.Len(t, placeholders, 5)
}
