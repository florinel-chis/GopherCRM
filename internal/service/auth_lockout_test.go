package service

import (
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// Copies keep in-memory mutation from masquerading as a persisted update.
type lockoutUserRepository struct {
	MockUserRepository
	stored models.User
	writes int
	logins int
}

func (r *lockoutUserRepository) GetByEmail(string) (*models.User, error) {
	copy := r.stored
	return &copy, nil
}

func (r *lockoutUserRepository) Update(user *models.User) error {
	r.stored = *user
	r.writes++
	return nil
}

func (r *lockoutUserRepository) UpdateLastLogin(uint) error {
	r.logins++
	return nil
}

func lockoutService(t *testing.T, until time.Time) (*authService, *lockoutUserRepository) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("Correct123!"), bcrypt.MinCost)
	require.NoError(t, err)
	repo := &lockoutUserRepository{stored: models.User{
		BaseModel: models.BaseModel{ID: 1}, Email: "lockout@example.test",
		Password: string(hash), IsActive: true, Role: models.RoleCustomer,
		FailedLoginAttempts: MaxFailedLoginAttempts, LockedUntil: &until,
	}}
	svc := NewAuthService(repo, nil, config.JWTConfig{Secret: "lockout-test-secret", ExpiryHours: 1}).(*authService)
	return svc, repo
}

func TestAuthLockoutExpiredFailureWindow(t *testing.T) {
	now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	svc, repo := lockoutService(t, now.Add(-time.Minute))
	svc.loginTime = func() time.Time { return now }
	for attempt := 1; attempt <= MaxFailedLoginAttempts; attempt++ {
		token, err := svc.Login(repo.stored.Email, "wrong")
		require.EqualError(t, err, "invalid credentials")
		require.Empty(t, token)
		require.Equal(t, attempt, repo.stored.FailedLoginAttempts)
		require.Equal(t, attempt, repo.writes)
		if attempt < MaxFailedLoginAttempts {
			require.Nil(t, repo.stored.LockedUntil)
		} else {
			require.NotNil(t, repo.stored.LockedUntil)
			require.Equal(t, now.Add(AccountLockDuration), *repo.stored.LockedUntil)
		}
	}
	require.Zero(t, repo.logins)
	// A correct password cannot bypass the newly established lock.
	token, err := svc.Login(repo.stored.Email, "Correct123!")
	require.Error(t, err)
	require.Empty(t, token)
	require.Equal(t, MaxFailedLoginAttempts, repo.writes)
}

func TestAuthLockoutExpiryBoundary(t *testing.T) {
	boundary := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			svc, repo := lockoutService(t, boundary)
			svc.loginTime = func() time.Time { return boundary.Add(delta) }
			token, err := svc.Login(repo.stored.Email, "wrong")
			require.Error(t, err)
			require.Empty(t, token)
			if delta < 0 {
				require.Zero(t, repo.writes)
				require.Equal(t, MaxFailedLoginAttempts, repo.stored.FailedLoginAttempts)
				require.Equal(t, boundary, *repo.stored.LockedUntil)
			} else {
				require.Equal(t, 1, repo.writes)
				require.Equal(t, 1, repo.stored.FailedLoginAttempts)
				require.Nil(t, repo.stored.LockedUntil)
			}
		})
	}
}

func TestAuthLockoutSuccessResetsPersistedState(t *testing.T) {
	now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, afterFailure := range []bool{false, true} {
		svc, repo := lockoutService(t, now)
		svc.loginTime = func() time.Time { return now }
		if afterFailure {
			_, err := svc.Login(repo.stored.Email, "wrong")
			require.Error(t, err)
		}
		token, err := svc.Login(repo.stored.Email, "Correct123!")
		require.NoError(t, err)
		require.NotEmpty(t, token)
		require.Zero(t, repo.stored.FailedLoginAttempts)
		require.Nil(t, repo.stored.LockedUntil)
		require.Equal(t, 1, repo.logins)
		require.GreaterOrEqual(t, repo.writes, 1)
	}
}
