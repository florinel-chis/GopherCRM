package service

import (
	"context"
	"errors"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"
)

// apiKeyGenerateDeps wires Generate's transaction-bound call sequence (#342):
// a mocked TransactionManager that expects one WithTransactionAndRetry on
// context.Background() with apiKeyCreateMaxRetries and runs the callback in a
// real transaction on an empty in-memory SQLite database (the mocks never
// touch it), and repositories that expect to be bound to it through WithTx.
type apiKeyGenerateDeps struct {
	keys  *mocks.APIKeyRepository
	users *mocks.UserRepository
	tm    *mocks.TransactionManager
	svc   APIKeyService
	calls []string
}

func newAPIKeyGenerateDeps(t *testing.T, owner *models.User) *apiKeyGenerateDeps {
	t.Helper()
	d := &apiKeyGenerateDeps{
		keys:  &mocks.APIKeyRepository{},
		users: &mocks.UserRepository{},
		tm:    &mocks.TransactionManager{},
	}
	real := sessionTestTransactionManager(t)
	d.tm.On("WithTransactionAndRetry", context.Background(), mock.Anything, apiKeyCreateMaxRetries).
		Return(func(ctx context.Context, fn func(context.Context) error, retries int) error {
			return real.WithTransactionAndRetry(ctx, fn, retries)
		}).Once()
	d.users.On("WithTx", mock.Anything).Return(d.users).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "users.WithTx")
	})
	d.users.On("GetByID", owner.ID).Return(owner, nil).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "users.GetByID")
	})
	d.keys.On("WithTx", mock.Anything).Return(d.keys).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "api_keys.WithTx")
	})
	d.svc = NewAPIKeyService(d.keys, d.users, d.tm, "test-api-key-secret")
	return d
}

func (d *apiKeyGenerateDeps) assertExpectations(t *testing.T) {
	t.Helper()
	d.tm.AssertExpectations(t)
	d.users.AssertExpectations(t)
	d.keys.AssertExpectations(t)
	assert.Equal(t, []string{"users.WithTx", "users.GetByID", "api_keys.WithTx", "api_keys.Create"}, d.calls,
		"the owner is locked before the transaction-bound insert")
}

func apiKeyTestOwner() *models.User {
	return &models.User{BaseModel: models.BaseModel{ID: 1}, IsActive: true, Role: models.RoleSales}
}

func TestAPIKeyService_Generate(t *testing.T) {
	d := newAPIKeyGenerateDeps(t, apiKeyTestOwner())

	userID := uint(1)
	name := "Test API Key"

	d.keys.On("Create", mock.Anything).Return(nil).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "api_keys.Create")
	})

	key, apiKey, err := d.svc.Generate(userID, name, nil)

	assert.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.NotNil(t, apiKey)
	assert.Equal(t, name, apiKey.Name)
	assert.Equal(t, userID, apiKey.UserID)
	assert.NotEmpty(t, apiKey.KeyHash)
	assert.NotEmpty(t, apiKey.Prefix)
	assert.True(t, len(apiKey.Prefix) == 8)
	assert.Contains(t, key, "gcrm_")

	d.assertExpectations(t)
}

func TestAPIKeyService_Generate_CreateError(t *testing.T) {
	d := newAPIKeyGenerateDeps(t, apiKeyTestOwner())

	userID := uint(1)
	name := "Test API Key"

	d.keys.On("Create", mock.Anything).Return(errors.New("database error")).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "api_keys.Create")
	})

	key, apiKey, err := d.svc.Generate(userID, name, nil)

	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrAccountUnavailable), "an insert failure is not an owner refusal")
	assert.Empty(t, key)
	assert.Nil(t, apiKey)

	d.assertExpectations(t)
}

func TestAPIKeyService_GetByUser(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

	userID := uint(1)
	expectedKeys := []models.APIKey{
		{
			BaseModel: models.BaseModel{ID: 1},
			Name:      "Key 1",
			UserID:    userID,
			IsActive:  true,
		},
		{
			BaseModel: models.BaseModel{ID: 2},
			Name:      "Key 2",
			UserID:    userID,
			IsActive:  false,
		},
	}

	mockAPIKeyRepo.On("GetByUserID", userID).Return(expectedKeys, nil)

	keys, err := apiKeyService.GetByUser(userID)

	assert.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, expectedKeys, keys)

	mockAPIKeyRepo.AssertExpectations(t)
}

func TestAPIKeyService_Revoke(t *testing.T) {
	t.Run("successful revocation", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)

		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: apiKeyID},
			UserID:    userID,
			IsActive:  true,
		}

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(apiKey, nil)
		mockAPIKeyRepo.On("Revoke", apiKeyID, userID, mock.MatchedBy(func(at time.Time) bool {
			return !at.IsZero() && at.Location() == time.UTC
		})).Return(int64(1), nil)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.NoError(t, err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// The conditional statement changes nothing on a key revoked before; the
	// call still succeeds, so revoking is idempotent.
	t.Run("revoking a revoked key succeeds", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		revokedAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		mockAPIKeyRepo.On("GetByID", uint(1)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 1}, UserID: 1, RevokedAt: &revokedAt,
		}, nil)
		mockAPIKeyRepo.On("Revoke", uint(1), uint(1), mock.Anything).Return(int64(0), nil)

		assert.NoError(t, apiKeyService.Revoke(1, 1))
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("unauthorized revocation", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(2) // Different user

		apiKey := &models.APIKey{
			BaseModel: models.BaseModel{ID: apiKeyID},
			UserID:    1, // Different from requesting user
			IsActive:  true,
		}

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(apiKey, nil)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden),
			"ownership violation must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// The repository hands gorm's sentinel back unwrapped; the service is the
	// layer that must translate it into something a handler can classify.
	t.Run("api key not found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(nil, gorm.ErrRecordNotFound)

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound),
			"missing key must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	// A lookup that failed for any other reason is an infrastructure failure and
	// must not be laundered into a not-found.
	t.Run("lookup failure is not a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		apiKeyID := uint(1)
		userID := uint(1)

		mockAPIKeyRepo.On("GetByID", apiKeyID).Return(nil, errors.New("connection refused"))

		err := apiKeyService.Revoke(apiKeyID, userID)

		assert.Error(t, err)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_GetByID(t *testing.T) {
	t.Run("owner gets their own key", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			Name:      "Production Key",
			UserID:    7,
			IsActive:  true,
			// The repository preloads the owner; the service must not leak it.
			User: models.User{BaseModel: models.BaseModel{ID: 7}, Email: "owner@example.com"},
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		key, err := apiKeyService.GetByID(3, 7)

		assert.NoError(t, err)
		assert.NotNil(t, key)
		assert.Equal(t, uint(3), key.ID)
		assert.Equal(t, "Production Key", key.Name)
		assert.Empty(t, key.User.Email, "the owner record must not ride along on the key payload")
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("another user's key is forbidden", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    99,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		key, err := apiKeyService.GetByID(3, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden),
			"ownership violation must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("missing key is a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)

		key, err := apiKeyService.GetByID(404, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound),
			"missing key must be classifiable with errors.Is, got %v", err)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("lookup failure is not a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(3)).Return(nil, errors.New("connection refused"))

		key, err := apiKeyService.GetByID(3, 7)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		assert.False(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_Update(t *testing.T) {
	t.Run("rename only writes the name", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(3)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 3}, Name: "Old Name", UserID: 7, IsActive: true,
		}, nil).Once()
		mockAPIKeyRepo.On("Rename", uint(3), uint(7), "New Name", false).Return(int64(1), nil).Once()
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 3}, Name: "New Name", UserID: 7, IsActive: true,
		}, nil).Once()

		name := "New Name"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.NoError(t, err)
		assert.Equal(t, "New Name", key.Name)
		assert.True(t, key.IsActive)
		mockAPIKeyRepo.AssertExpectations(t)
		mockAPIKeyRepo.AssertNotCalled(t, "Revoke", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("is_active false revokes and leaves the name alone", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		revokedAt := time.Now().UTC()
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 3}, Name: "Keep Me", UserID: 7, IsActive: true,
		}, nil).Once()
		mockAPIKeyRepo.On("Revoke", uint(3), uint(7), mock.Anything).Return(int64(1), nil).Once()
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 3}, Name: "Keep Me", UserID: 7, IsActive: false, RevokedAt: &revokedAt,
		}, nil).Once()

		active := false
		key, err := apiKeyService.Update(3, 7, nil, &active)

		assert.NoError(t, err)
		assert.Equal(t, "Keep Me", key.Name)
		assert.False(t, key.IsActive)
		assert.NotNil(t, key.RevokedAt)
		mockAPIKeyRepo.AssertExpectations(t)
		mockAPIKeyRepo.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	// Revocation is final: is_active=true on a revoked key (stamped, or an
	// inactive row from before revoked_at existed) writes nothing, the name
	// included.
	for _, tc := range []struct {
		label  string
		stored models.APIKey
	}{
		{"a revoked key cannot be switched on again", models.APIKey{IsActive: false, RevokedAt: func() *time.Time { at := time.Now().UTC(); return &at }()}},
		{"an inactive key without revoked_at cannot be switched on again", models.APIKey{IsActive: false}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			mockAPIKeyRepo := new(MockAPIKeyRepository)
			apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

			stored := tc.stored
			stored.ID, stored.UserID, stored.Name = 3, 7, "Revoked Key"
			mockAPIKeyRepo.On("GetByID", uint(3)).Return(&stored, nil).Once()

			active := true
			name := "Renamed"
			key, err := apiKeyService.Update(3, 7, &name, &active)

			assert.ErrorIs(t, err, ErrAPIKeyRevoked)
			assert.Nil(t, key)
			mockAPIKeyRepo.AssertExpectations(t)
			mockAPIKeyRepo.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			mockAPIKeyRepo.AssertNotCalled(t, "Revoke", mock.Anything, mock.Anything, mock.Anything)
		})
	}

	t.Run("is_active true with a name renames only a live key", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(3)).Return(&models.APIKey{
			BaseModel: models.BaseModel{ID: 3}, Name: "Old", UserID: 7, IsActive: true,
		}, nil).Once()
		// Revoked between the read and the rename: the live-only rename
		// changes nothing.
		mockAPIKeyRepo.On("Rename", uint(3), uint(7), "Renamed", true).Return(int64(0), nil).Once()

		active := true
		name := "Renamed"
		key, err := apiKeyService.Update(3, 7, &name, &active)

		assert.ErrorIs(t, err, ErrAPIKeyRevoked)
		assert.Nil(t, key)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("is_active true on a live key writes nothing", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		live := &models.APIKey{BaseModel: models.BaseModel{ID: 3}, Name: "Live", UserID: 7, IsActive: true}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(live, nil).Twice()

		active := true
		key, err := apiKeyService.Update(3, 7, nil, &active)

		assert.NoError(t, err)
		assert.True(t, key.IsActive)
		mockAPIKeyRepo.AssertExpectations(t)
		mockAPIKeyRepo.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockAPIKeyRepo.AssertNotCalled(t, "Revoke", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("another user's key is forbidden", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    99,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)

		name := "Hijacked"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrForbidden))
		mockAPIKeyRepo.AssertNotCalled(t, "Rename", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("missing key is a not-found", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		mockAPIKeyRepo.On("GetByID", uint(404)).Return(nil, gorm.ErrRecordNotFound)

		name := "New Name"
		key, err := apiKeyService.Update(404, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound))
		mockAPIKeyRepo.AssertExpectations(t)
	})

	t.Run("persist failure is surfaced", func(t *testing.T) {
		mockAPIKeyRepo := new(MockAPIKeyRepository)
		apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

		stored := &models.APIKey{
			BaseModel: models.BaseModel{ID: 3},
			UserID:    7,
			IsActive:  true,
		}
		mockAPIKeyRepo.On("GetByID", uint(3)).Return(stored, nil)
		mockAPIKeyRepo.On("Rename", uint(3), uint(7), "New Name", false).Return(int64(0), errors.New("connection refused"))

		name := "New Name"
		key, err := apiKeyService.Update(3, 7, &name, nil)

		assert.Error(t, err)
		assert.Nil(t, key)
		assert.False(t, errors.Is(err, apperrors.ErrNotFound))
		mockAPIKeyRepo.AssertExpectations(t)
	})
}

func TestAPIKeyService_Generate_WithExpiry(t *testing.T) {
	d := newAPIKeyGenerateDeps(t, apiKeyTestOwner())

	expires := time.Now().Add(48 * time.Hour)
	d.keys.On("Create", mock.MatchedBy(func(k *models.APIKey) bool {
		return k.ExpiresAt != nil && k.ExpiresAt.Equal(expires)
	})).Return(nil).Run(func(mock.Arguments) {
		d.calls = append(d.calls, "api_keys.Create")
	})

	key, apiKey, err := d.svc.Generate(1, "Expiring Key", &expires)

	assert.NoError(t, err)
	assert.NotEmpty(t, key)
	assert.NotNil(t, apiKey.ExpiresAt)
	assert.True(t, apiKey.ExpiresAt.Equal(expires))
	d.assertExpectations(t)
}

func TestAPIKeyService_List(t *testing.T) {
	mockAPIKeyRepo := new(MockAPIKeyRepository)
	apiKeyService := NewAPIKeyService(mockAPIKeyRepo, nil, nil, "test-api-key-secret")

	userID := uint(1)
	now := time.Now()
	expectedKeys := []models.APIKey{
		{
			BaseModel:  models.BaseModel{ID: 1},
			Name:       "Production Key",
			UserID:     userID,
			IsActive:   true,
			LastUsedAt: &now,
		},
		{
			BaseModel: models.BaseModel{ID: 2},
			Name:      "Development Key",
			UserID:    userID,
			IsActive:  false,
		},
	}

	mockAPIKeyRepo.On("GetByUserID", userID).Return(expectedKeys, nil)

	keys, err := apiKeyService.List(userID)

	assert.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, expectedKeys, keys)

	mockAPIKeyRepo.AssertExpectations(t)
}
