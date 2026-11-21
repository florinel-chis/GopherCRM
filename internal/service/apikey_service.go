package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAPIKeyIssuanceUnavailable is returned by Generate on a service built
// without the user repository or the transaction manager: a key is never
// inserted without the owner lock (#342). Nothing is written.
var ErrAPIKeyIssuanceUnavailable = errors.New("api key issuance is not available")

// ErrAPIKeyRevoked is returned by Update when is_active=true is asked of a
// revoked key: revocation is final. Nothing is written.
var ErrAPIKeyRevoked = errors.New("api key is revoked")

// ErrAPIKeyExpiryTooLate is returned by Generate when the requested expiry is
// later than models.APIKeyExpiryCap(now): a key lives at most one year (#532).
// Nothing is written.
var ErrAPIKeyExpiryTooLate = errors.New("api key expiry is more than one year ahead")

// ErrAPIKeyCustomerAccount is returned by Generate when the owner, read under
// the owner lock, is a customer account: customer accounts hold no API keys
// (#532). Nothing is written.
var ErrAPIKeyCustomerAccount = errors.New("customer accounts cannot hold api keys")

// apiKeyCreateMaxRetries is how often the API-key issuing transaction is
// replayed after a deadlock, a lock wait timeout or SQLite busy/locked (#183
// retry rule).
const apiKeyCreateMaxRetries = 3

type apiKeyService struct {
	apiKeyRepo repository.APIKeyRepository
	// userRepo and txManager issue a key only inside one transaction that
	// first locks the owner row by primary key (#342).
	userRepo     repository.UserRepository
	txManager    repository.TransactionManager
	apiKeySecret string
	// beforeLock is an instance-local seam that runs before the issuing
	// transaction begins. Tests commit a competing change there; nil in
	// production.
	beforeLock func(userID uint)
	// beforeWrite is an instance-local seam that runs in Update between the
	// ownership read and the first write. Tests commit a competing revocation
	// there; nil in production.
	beforeWrite func(id uint)
}

// NewAPIKeyService builds the API key service. userRepo and txManager must be
// built on the same database handle as apiKeyRepo: Generate locks the owner
// through userRepo and inserts through apiKeyRepo inside one transaction of
// txManager. With either nil, Generate refuses with
// ErrAPIKeyIssuanceUnavailable.
func NewAPIKeyService(
	apiKeyRepo repository.APIKeyRepository,
	userRepo repository.UserRepository,
	txManager repository.TransactionManager,
	apiKeySecret string,
) APIKeyService {
	return &apiKeyService{apiKeyRepo: apiKeyRepo, userRepo: userRepo, txManager: txManager, apiKeySecret: apiKeySecret}
}

// Generate issues a key to userID. The insert runs in one
// WithTransactionAndRetry transaction (apiKeyCreateMaxRetries) that first
// reads the owner by primary key with a locking read (`SELECT ... FOR UPDATE`
// on MySQL/MariaDB, a plain read on SQLite) and refuses a missing, erased
// (soft-deleted) or inactive owner with an error matching
// ErrAccountUnavailable, writing nothing. A customer owner, read under the
// same lock, is refused with ErrAPIKeyCustomerAccount (#532). Lock order
// users → api_keys: an erasure that commits first makes the request refuse,
// and one that commits after it purges the key it issued.
//
// A nil expiresAt becomes models.APIKeyExpiryCap(now); one later than that is
// refused with ErrAPIKeyExpiryTooLate before the transaction (#532).
func (s *apiKeyService) Generate(userID uint, name string, expiresAt *time.Time) (string, *models.APIKey, error) {
	log := utils.Logger.WithField("user_id", userID).WithField("operation", "GenerateAPIKey")
	if s.userRepo == nil || s.txManager == nil {
		log.WithField("error_code", "issuance_unwired").Error("API key issuance is not wired")
		return "", nil, ErrAPIKeyIssuanceUnavailable
	}

	expiryCap := models.APIKeyExpiryCap(time.Now().UTC())
	if expiresAt == nil {
		expiresAt = &expiryCap
	} else if expiresAt.After(expiryCap) {
		log.WithField("error_code", "expiry_too_late").Warn("API key not issued: expiry more than one year ahead")
		return "", nil, ErrAPIKeyExpiryTooLate
	}

	// Generate random key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", nil, err
	}

	key := hex.EncodeToString(keyBytes)
	prefix := key[:8]
	keyHash := utils.HashAPIKeyHMAC(key, s.apiKeySecret)

	if s.beforeLock != nil {
		s.beforeLock(userID)
	}

	var created *models.APIKey
	err := s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		// Owner first, by primary key, on a fresh Session of the locking
		// handle so the clause does not leak into the insert.
		owner, err := lockLiveCredentialOwner(
			s.userRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})),
			userID)
		if err != nil {
			return err
		}
		// Checked under the lock, so a role change that commits first is
		// seen here.
		if owner.Role == models.RoleCustomer {
			return ErrAPIKeyCustomerAccount
		}
		apiKey := &models.APIKey{
			Name:      name,
			KeyHash:   keyHash,
			Prefix:    prefix,
			UserID:    userID,
			ExpiresAt: expiresAt,
		}
		if err := s.apiKeyRepo.WithTx(tx).Create(apiKey); err != nil {
			return fmt.Errorf("failed to store api key: %w", err)
		}
		created = apiKey
		return nil
	}, apiKeyCreateMaxRetries)
	if err != nil {
		var refusal *credentialOwnerRefusal
		if errors.As(err, &refusal) {
			log.WithField("error_code", refusal.code).Warn("API key refused under the owner lock")
		} else if errors.Is(err, ErrAPIKeyCustomerAccount) {
			log.WithField("error_code", "customer_account").Warn("API key refused under the owner lock")
		} else {
			log.WithError(utils.LogError(err)).WithField("error_code", "api_key_insert_failed").
				Error("API key issuing transaction rolled back")
		}
		return "", nil, err
	}

	// Return the full key only once
	fullKey := fmt.Sprintf("gcrm_%s", key)
	return fullKey, created, nil
}

func (s *apiKeyService) GetByUser(userID uint) ([]models.APIKey, error) {
	return s.apiKeyRepo.GetByUserID(userID)
}

// ownedKey loads a key and asserts that userID owns it. It is the single place
// the ownership rule for a single key is expressed, so the fetch, update and
// revoke paths cannot drift apart. The repository returns gorm's own sentinel
// unwrapped, so the translation to apperrors.ErrNotFound happens here and
// callers can classify with errors.Is instead of matching on a message.
func (s *apiKeyService) ownedKey(id uint, userID uint) (*models.APIKey, error) {
	apiKey, err := s.apiKeyRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("api key %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}

	if apiKey.UserID != userID {
		return nil, fmt.Errorf("api key %d belongs to another user: %w", id, apperrors.ErrForbidden)
	}

	return apiKey, nil
}

func (s *apiKeyService) GetByID(id uint, userID uint) (*models.APIKey, error) {
	apiKey, err := s.ownedKey(id, userID)
	if err != nil {
		return nil, err
	}

	// The repository preloads the owner. Drop it: the caller is the owner by
	// definition here, so it carries no information, and shipping a whole user
	// record inside every key payload would make this endpoint's shape differ
	// from the list endpoint's for no reason.
	apiKey.User = models.User{}
	return apiKey, nil
}

// apiKeyRevoked reports whether a key is revoked: inactive, or stamped with
// revoked_at. An inactive row without revoked_at (revoked before the column
// existed) counts as revoked too.
func apiKeyRevoked(apiKey *models.APIKey) bool {
	return !apiKey.IsActive || apiKey.RevokedAt != nil
}

// Update applies only the fields the caller actually sent, each with a
// statement that writes only that column, so a concurrent revocation is never
// overwritten. isActive=false revokes the key like Revoke. isActive=true does
// not re-enable anything: on a live key it changes nothing, and on a revoked
// key it is refused with ErrAPIKeyRevoked and nothing is written, the name
// included. When isActive is true the rename applies only to a live key, so a
// revocation that commits between the ownership read and the rename also
// ends in ErrAPIKeyRevoked with the row unchanged. Expiry is unaffected: it
// is checked independently at authentication time and cannot be cleared
// through this path.
func (s *apiKeyService) Update(id uint, userID uint, name *string, isActive *bool) (*models.APIKey, error) {
	apiKey, err := s.ownedKey(id, userID)
	if err != nil {
		return nil, err
	}

	switchOn := isActive != nil && *isActive
	if switchOn && apiKeyRevoked(apiKey) {
		return nil, ErrAPIKeyRevoked
	}

	if s.beforeWrite != nil {
		s.beforeWrite(id)
	}

	if name != nil {
		renamed, err := s.apiKeyRepo.Rename(id, userID, *name, switchOn)
		if err != nil {
			return nil, err
		}
		if switchOn && renamed == 0 {
			// Owned when read, so a zero count means the key was revoked
			// (or removed) since: answer as for a revoked key, unchanged.
			return nil, ErrAPIKeyRevoked
		}
	}
	if isActive != nil && !*isActive {
		if _, err := s.apiKeyRepo.Revoke(id, userID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}

	updated, err := s.ownedKey(id, userID)
	if err != nil {
		return nil, err
	}
	if switchOn && apiKeyRevoked(updated) {
		return nil, ErrAPIKeyRevoked
	}
	updated.User = models.User{}
	return updated, nil
}

// Revoke switches the key off and stamps revoked_at with one conditional
// UPDATE. It is idempotent: on a key already revoked nothing changes and the
// first revoked_at is kept; an inactive key without revoked_at gets stamped.
func (s *apiKeyService) Revoke(id uint, userID uint) error {
	if _, err := s.ownedKey(id, userID); err != nil {
		return err
	}
	_, err := s.apiKeyRepo.Revoke(id, userID, time.Now().UTC())
	return err
}

func (s *apiKeyService) List(userID uint) ([]models.APIKey, error) {
	return s.apiKeyRepo.GetByUserID(userID)
}
