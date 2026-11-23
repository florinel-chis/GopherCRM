package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"golang.org/x/crypto/bcrypt"
)

// ChangePassword re-authenticates with the current password, applies the
// complexity policy to the new one and replaces it, ending every credential
// of the user (revokeAllCredentials) except the session being renewed.
//
// refreshToken names the caller's current browser session. When it is given,
// that session is renewed: the token is claimed and a replacement refresh
// token and access token are returned. It must be a live token of this user;
// anything else (unknown, revoked, expired, foreign, or a single sign-on
// session at or past ssoSessionMaxAge after its sign-in) is ErrSessionStale
// and nothing is written. The replacement starts a new family; a single
// sign-on session keeps its sign-in method and start, so the renewal keeps
// its 24-hour limit, while any other session becomes a password session
// starting now. When it is empty — API-key callers, scripts — the
// password is changed, every refresh token and every API key (the caller's
// own key included) is revoked and no credentials are returned (nil tokens,
// nil error).
//
// The slow bcrypt work runs first, outside any transaction. The writes are one
// transaction that locks the owner row before touching refresh tokens (the
// order every session transaction follows): the locked row must still carry
// the hash the current password was verified against, so of two concurrent
// changes only one can succeed; then the presented token is claimed, the
// password written, every credential ended by revokeAllCredentials
// (token_version incremented, every refresh token and every API key revoked)
// and the successor issued and signed. Any failure before the commit rolls
// all of it back: the password is unchanged and every session and key is as
// it was. (A failed COMMIT is the exception every transaction has: its
// outcome is ambiguous, utils.ErrCommitFailed, and the caller reports it as
// an error.) Every access token issued before the change, the caller's own
// included, is refused once it commits; the renewed access token is signed
// with the new value and keeps working (#236).
func (s *authService) ChangePassword(userID uint, currentPassword, newPassword, refreshToken string) (*AuthTokens, error) {
	if s.refreshTokenRepo == nil || s.txManager == nil || s.apiKeyRepo == nil {
		return nil, errors.New("password change requires the session store")
	}

	user, err := s.userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		utils.Logger.WithField("user_id", userID).Warn("Password change rejected - wrong current password")
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, userID, ErrInvalidCurrentPassword)
	}
	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return nil, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}
	verifiedHash := user.Password

	var presentedHash string
	if refreshToken != "" {
		presentedHash = hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	}

	var tokens *AuthTokens
	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		tokens, err = s.changePasswordInTransaction(
			s.userRepo.WithTx(repository.LockingRead(tx)),
			s.refreshTokenRepo.WithTx(tx), s.apiKeyRepo.WithTx(tx),
			userID, verifiedHash, string(hashed), presentedHash)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrInvalidCurrentPassword) {
			utils.Logger.WithField("user_id", userID).Warn("Password change rejected - password changed concurrently")
		}
		return nil, err
	}

	logger := utils.Logger.WithField("user_id", userID).WithField("operation", "ChangePassword")
	if tokens != nil {
		logger.Info("Password changed; current session renewed, other sessions, earlier access tokens and API keys ended")
	} else {
		logger.Info("Password changed; all sessions, earlier access tokens and API keys ended")
	}
	return tokens, nil
}

// changePasswordInTransaction performs the password change on
// transaction-bound repositories; userRepo is bound to a locking handle. Any
// error discards every write.
func (s *authService) changePasswordInTransaction(
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	apiKeyRepo repository.APIKeyRepository,
	userID uint, verifiedHash, newHash, presentedHash string,
) (*AuthTokens, error) {
	// Owner first: SELECT ... FOR UPDATE on MySQL/MariaDB (SQLite drops the
	// clause and serialises writers instead).
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("password change owner lookup failed: %w", err)
	}
	if !owner.IsActive {
		return nil, authRefusal(AuthAuditReasonAccountInactive, userID, ErrAccountUnavailable)
	}
	// The current password was verified against verifiedHash outside the
	// transaction. If the locked row carries another hash, a concurrent
	// change committed first and the caller's "current" password is no
	// longer current.
	if owner.Password != verifiedHash {
		return nil, authRefusal(AuthAuditReasonInvalidCredentials, userID, ErrInvalidCurrentPassword)
	}

	// renewOIDCStart is the start of the presented single sign-on session,
	// nil when the presented session is not one; renewProviderSession is its
	// provider session reference.
	var renewOIDCStart *time.Time
	var renewProviderSession string
	if presentedHash != "" {
		stored, err := refreshRepo.GetByTokenHash(presentedHash)
		if err != nil {
			if apperrors.IsNotFound(err) {
				return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
			}
			return nil, fmt.Errorf("password change session lookup failed: %w", err)
		}
		if stored.UserID != userID {
			return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
		}
		// A single sign-on session past its limit is not live either; it is
		// refused before the claim, so nothing is written.
		if ssoSessionExpired(stored.SignInMethod, stored.SessionStartedAt, time.Now()) {
			return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
		}
		if stored.SignInMethod == models.RefreshTokenSignInOIDC {
			renewOIDCStart = stored.SessionStartedAt
			renewProviderSession = stored.ProviderSessionHash
		}
		if err := refreshRepo.ClaimForRotation(stored.ID); err != nil {
			if apperrors.IsNotFound(err) {
				return nil, authRefusal(AuthAuditReasonSessionStale, userID, ErrSessionStale)
			}
			return nil, fmt.Errorf("failed to claim the current session: %w", err)
		}
	}

	if err := userRepo.UpdateFields(userID, map[string]interface{}{
		"password": newHash,
	}); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, authRefusal(AuthAuditReasonAccountErased, userID, ErrAccountUnavailable)
		}
		return nil, fmt.Errorf("failed to update password: %w", err)
	}

	// Every credential ends with the old password: the session generation is
	// incremented, so every access JWT issued before the change — the
	// caller's own included — is refused from the commit on (#236), and every
	// refresh token and API key is revoked. The owner row is locked, so its
	// token_version is current. A revocation that fails is a failure of the
	// change itself and rolls the password back with it. The successor below
	// is issued afterwards and signed from the new generation.
	if err := revokeAllCredentials(credentialStores{
		users: userRepo, refreshTokens: refreshRepo, apiKeys: apiKeyRepo,
	}, owner, time.Now()); err != nil {
		return nil, fmt.Errorf("failed to end credentials after password change: %w", err)
	}

	if presentedHash == "" {
		return nil, nil
	}

	owner.Password = newHash
	// The successor starts a new family, as its access token does. A renewed
	// single sign-on session keeps its sign-in method and start, so its
	// expiry stays capped at that start + ssoSessionMaxAge; any other
	// session becomes a password session starting now.
	session, err := newRefreshSession(models.RefreshTokenSignInPassword)
	if err != nil {
		return nil, err
	}
	if renewOIDCStart != nil {
		session.SignInMethod = models.RefreshTokenSignInOIDC
		session.StartedAt = *renewOIDCStart
		session.ProviderSessionHash = renewProviderSession
	}
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, userID, session)
	if err != nil {
		return nil, err
	}
	accessToken, err := s.accessTokenFor(owner)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token during password change: %w", err)
	}
	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: owner}, nil
}
