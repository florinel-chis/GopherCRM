package service

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Logout revokes refresh tokens for the authenticated user. With an empty
// refreshToken every session of the user is revoked; with a specific one only
// that token — and only if it actually belongs to the user, so an
// authenticated caller cannot use logout to revoke someone else's session.
// Unknown or foreign tokens are ignored: logout is idempotent and discloses
// nothing about token validity.
//
// Every logout also ends the access JWTs issued to the user before it (#236):
// users.token_version is incremented by one, with or without a refresh token
// and for Bearer and API-key callers alike. The increment and the refresh
// revocation are one transaction that locks the users row (`SELECT ... FOR
// UPDATE` on MySQL) before it reads or writes any refresh_tokens row, the
// order every session transaction follows; a failure of either rolls both
// back. Logout is refused with an error when the transaction manager or the
// refresh-token repository is not wired, instead of revoking half a session.
// A user the scoped locking read cannot find (erased meanwhile) has nothing
// left to end: Logout returns nil and writes nothing.
func (s *authService) Logout(userID uint, refreshToken string) error {
	if s.refreshTokenRepo == nil || s.txManager == nil {
		return errors.New("logout requires the session store")
	}

	var presentedHash string
	if refreshToken != "" {
		presentedHash = hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	}

	var ended bool
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		ended, err = s.logoutInTransaction(
			s.userRepo.WithTx(repository.LockingRead(tx)),
			s.refreshTokenRepo.WithTx(tx),
			userID, presentedHash)
		return err
	})
	if errors.Is(err, errLogoutOwnerGone) {
		return nil
	}
	if err != nil {
		return err
	}
	if ended {
		utils.Logger.WithField("user_id", userID).WithField("operation", "Logout").
			Info("Logout ended the access tokens issued before it")
	}
	return nil
}

// logoutInTransaction performs the logout on transaction-bound repositories;
// userRepo is bound to a locking handle. It reports whether the user was found
// and the counter incremented. Any error discards every write.
func (s *authService) logoutInTransaction(
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	userID uint, presentedHash string,
) (bool, error) {
	// Owner first, before any refresh_tokens row: a rotation of the same user
	// locks the owner before its claim, so taking the rows in the other order
	// here could deadlock against it on MySQL.
	owner, err := userRepo.GetByID(userID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("logout owner lookup failed: %w", err)
	}

	if presentedHash == "" {
		if err := refreshRepo.RevokeAllForUser(userID); err != nil {
			return false, fmt.Errorf("failed to revoke sessions: %w", err)
		}
	} else {
		stored, err := refreshRepo.GetByTokenHash(presentedHash)
		switch {
		case err != nil && !apperrors.IsNotFound(err):
			return false, fmt.Errorf("logout lookup failed: %w", err)
		case err != nil:
			// Unknown, revoked or expired: nothing to revoke.
		case stored.UserID != userID:
			utils.Logger.WithField("user_id", userID).WithField("token_owner", stored.UserID).
				Warn("Logout presented a refresh token belonging to another user; ignoring")
		default:
			if err := refreshRepo.RevokeByTokenHash(presentedHash); err != nil {
				return false, fmt.Errorf("failed to revoke session: %w", err)
			}
		}
	}

	if err := userRepo.UpdateFields(userID, map[string]interface{}{
		"token_version": owner.TokenVersion + 1,
	}); err != nil {
		if apperrors.IsNotFound(err) {
			// Erased after the locking read (only possible where the read
			// takes no lock): roll the revocation back and report nothing.
			return false, errLogoutOwnerGone
		}
		return false, fmt.Errorf("failed to end access tokens at logout: %w", err)
	}
	return true, nil
}

// errLogoutOwnerGone rolls back a logout whose user vanished between the
// owner read and the increment; Logout turns it into a nil result.
var errLogoutOwnerGone = errors.New("logout owner no longer exists")
