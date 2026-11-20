package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// SCIM provider tokens (#468). A token is "scim_" followed by the hex of 32
// random bytes. Only utils.HashAPIKeyHMAC of it is stored, keyed like API
// keys (API_KEY_SECRET, else JWT_SECRET), so the plaintext is shown once, on
// creation. Neither a token nor its hash is ever logged.

// SCIMTokenPrefix starts every SCIM provider token.
const SCIMTokenPrefix = "scim_"

// scimTokenRandomBytes is how many random bytes a token carries.
const scimTokenRandomBytes = 32

// ErrSCIMTokenNameInvalid refuses a name that is blank after trimming or
// longer than 100 characters. It wraps apperrors.ErrValidation.
var ErrSCIMTokenNameInvalid = fmt.Errorf("SCIM token name must be 1 to %d characters: %w",
	models.SCIMTokenNameMaxLength, apperrors.ErrValidation)

// SCIMTokenService manages and checks the SCIM provider tokens.
type SCIMTokenService interface {
	// Create issues a token named name (trimmed, 1 to 100 characters) and
	// returns its plaintext, shown only here, and the stored row.
	Create(ctx context.Context, name string) (string, *models.SCIMToken, error)
	// List returns every token, newest first.
	List(ctx context.Context) ([]models.SCIMToken, error)
	// Revoke revokes the token id and returns it. Revoking a revoked token
	// returns it with its original revoked_at; an unknown id wraps
	// apperrors.ErrNotFound.
	Revoke(ctx context.Context, id uint) (*models.SCIMToken, error)
	// Authenticate checks an Authorization header. It returns the token and
	// "" on success, or nil and the audit reason of the refusal:
	// token_missing, token_invalid or token_revoked.
	Authenticate(ctx context.Context, header string) (*models.SCIMToken, string)
}

type scimTokenService struct {
	repo   repository.SCIMTokenRepository
	secret string
	now    func() time.Time
}

// NewSCIMTokenService returns the token service over repo; secret is the
// API-key HMAC secret (cfg.API.APIKeySecret).
func NewSCIMTokenService(repo repository.SCIMTokenRepository, secret string) SCIMTokenService {
	return &scimTokenService{repo: repo, secret: secret, now: time.Now}
}

func (s *scimTokenService) Create(_ context.Context, name string) (string, *models.SCIMToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > models.SCIMTokenNameMaxLength {
		return "", nil, ErrSCIMTokenNameInvalid
	}
	raw := make([]byte, scimTokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate SCIM token: %w", err)
	}
	plaintext := SCIMTokenPrefix + hex.EncodeToString(raw)
	row := &models.SCIMToken{
		Name:      name,
		TokenHash: utils.HashAPIKeyHMAC(plaintext, s.secret),
		CreatedAt: s.now().UTC(),
	}
	if err := s.repo.Create(row); err != nil {
		return "", nil, fmt.Errorf("store SCIM token: %w", err)
	}
	return plaintext, row, nil
}

func (s *scimTokenService) List(_ context.Context) ([]models.SCIMToken, error) {
	tokens, err := s.repo.List()
	if err != nil {
		return nil, fmt.Errorf("list SCIM tokens: %w", err)
	}
	if tokens == nil {
		tokens = []models.SCIMToken{}
	}
	return tokens, nil
}

func (s *scimTokenService) Revoke(_ context.Context, id uint) (*models.SCIMToken, error) {
	token, err := s.repo.Revoke(id, s.now().UTC())
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("SCIM token %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, fmt.Errorf("revoke SCIM token %d: %w", id, err)
	}
	return token, nil
}

func (s *scimTokenService) Authenticate(_ context.Context, header string) (*models.SCIMToken, string) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return nil, AuthAuditReasonTokenMissing
	}
	presented := header[len(scheme):]
	if presented == "" {
		return nil, AuthAuditReasonTokenMissing
	}
	computed := utils.HashAPIKeyHMAC(presented, s.secret)
	token, err := s.repo.FindByHash(computed)
	if err != nil {
		if !apperrors.IsNotFound(err) {
			// The statement was bound to the computed hash: only the
			// error's type is logged, never its text.
			scimTokenLog().WithField("error_type", fmt.Sprintf("%T", err)).Warn("SCIM token lookup failed")
		}
		return nil, AuthAuditReasonTokenInvalid
	}
	// MySQL and MariaDB compare token_hash under a case-insensitive collation;
	// the row is accepted only when its hash is byte-for-byte the computed one.
	if !hmac.Equal([]byte(token.TokenHash), []byte(computed)) {
		return nil, AuthAuditReasonTokenInvalid
	}
	if token.RevokedAt != nil {
		return nil, AuthAuditReasonTokenRevoked
	}
	now := s.now().UTC()
	if err := s.repo.TouchLastUsed(token.ID, now); err != nil {
		// Best-effort: the answer does not change. Only the error's type is
		// logged, so no driver text can carry a token or a hash.
		scimTokenLog().WithFields(logrus.Fields{"scim_token_id": token.ID, "error_type": fmt.Sprintf("%T", err)}).
			Warn("SCIM token last use could not be recorded")
	} else {
		token.LastUsedAt = &now
	}
	return token, ""
}

// scimTokenLog is the logger of the token service: component scim_token,
// never a token or a hash.
func scimTokenLog() *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithField("component", "scim_token")
}

// IsSCIMTokenNameInvalid reports whether err is a refused token name.
func IsSCIMTokenNameInvalid(err error) bool { return errors.Is(err, ErrSCIMTokenNameInvalid) }
