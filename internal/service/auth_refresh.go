package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// errRefreshOwnerRejected marks a rotation refused because of its owner —
// inactive, erased or missing — after the presented token was already
// claimed. It is internal to RefreshAccessToken: the claim is committed as
// containment and the caller receives the generic ErrInvalidRefreshToken.
var errRefreshOwnerRejected = fmt.Errorf("refresh token owner rejected: %w", ErrInvalidRefreshToken)

// errRefreshTokenReuse marks a refresh token presented again after a
// committed rotation had already exchanged it for a successor. It is internal
// to RefreshAccessToken: the revocation of the token's whole family is
// committed and the caller receives the generic ErrInvalidRefreshToken.
var errRefreshTokenReuse = fmt.Errorf("refresh token reused: %w", ErrInvalidRefreshToken)

// RefreshAccessToken validates the presented refresh token and rotates it:
// the presented token is revoked and a fresh JWT + refresh token are issued.
// Every rejection maps to ErrInvalidRefreshToken so callers cannot learn
// whether the token was unknown, revoked, expired, or orphaned.
//
// The rotation is one transaction. The presented token is claimed by a
// conditional update that revokes it only while it is still not revoked and
// strictly unexpired and must affect exactly one row, so two requests that
// both read the token as live cannot both rotate it: the second claim finds
// nothing to revoke and is rejected before any successor exists. The owner is
// read with a locking read before the claim (users row before refresh_tokens
// rows, the order every session transaction follows), and the successor is
// inserted and its access token signed, all inside the same transaction. A failure of the successor
// insert or of the signing rolls the claim back with it, so the presented
// token stays usable and no credentials are returned; nothing is left half
// rotated. An inactive, erased or missing owner is the one rejection that
// commits: the claim stands as the containment revocation of a token whose
// account is dead, no successor is inserted, and the public error is the same
// generic one.
//
// A token that a committed rotation already exchanged for a successor is the
// other rejection that commits: when the live lookup misses, the revoked row
// is read in the same transaction, and when a later row of its family exists
// the owner is locked and every live token of the family is revoked. The
// public error is again the generic one; the audit reason is token_reuse with
// the owner's id. A request that loses the claim to a concurrent rotation is
// not a reuse: it passed the live lookup, so it is refused as before and ends
// nothing.
func (s *authService) RefreshAccessToken(refreshToken string) (*AuthTokens, error) {
	// Every dependency of the transactional flow is required. Running the old
	// sequence of independent writes when one is missing would reintroduce
	// the double-successor and stranded-session failures this flow prevents.
	if s.refreshTokenRepo == nil || s.txManager == nil || refreshToken == "" {
		return nil, fmt.Errorf("refresh not available: %w", ErrInvalidRefreshToken)
	}

	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())

	var (
		tokens        *AuthTokens
		ownerRejected bool
		tokenReused   bool
		ownerReason   string
		tokenOwnerID  uint
	)
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		// The owner repository is bound to a locking handle so its read is
		// `SELECT ... FOR UPDATE` on MySQL (SQLite drops the clause). The
		// handle must be a fresh Session: Clauses alone returns a statement
		// that GORM keeps reusing, and its state would leak into the
		// successor insert that follows on the same transaction.
		rotated, ownerID, err := s.rotateRefreshTokenInTransaction(
			s.refreshTokenRepo.WithTx(tx),
			s.userRepo.WithTx(repository.LockingRead(tx)),
			hash)
		tokenOwnerID = ownerID
		if errors.Is(err, errRefreshTokenReuse) {
			// Commit the family revocation: the refusal stands, and so must
			// the end of every token of the reused session.
			tokenReused = true
			return nil
		}
		if errors.Is(err, errRefreshOwnerRejected) {
			ownerReason, _ = AuthAuditReason(err)
			// Commit the containment: the claim has revoked a token whose
			// owner is gone or disabled, and that revocation must survive
			// the rejection rather than be rolled back with it.
			ownerRejected = true
			return nil
		}
		if err != nil {
			return err
		}
		tokens = rotated
		return nil
	})
	if tokenReused {
		if err != nil {
			// The family revocation did not commit; the next presentation of
			// the token will try again. The caller still learns only that the
			// token was rejected.
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", tokenOwnerID).
				Warn("Failed to commit revocation of a reused refresh token's session")
		} else {
			utils.Logger.WithField("user_id", tokenOwnerID).
				Warn("Refresh token presented again after rotation; its session was revoked")
		}
		return nil, authRefusal(AuthAuditReasonTokenReuse, tokenOwnerID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken))
	}
	if ownerRejected {
		if err != nil {
			// The containment did not commit; the token is still live and the
			// next presentation will try again. The caller still learns only
			// that the token was rejected.
			utils.Logger.WithError(utils.LogError(err)).WithField("user_id", tokenOwnerID).
				Warn("Failed to commit revocation of refresh token with inactive or missing owner")
		}
		utils.Logger.WithField("user_id", tokenOwnerID).
			Warn("Rejected refresh token whose owner is missing or inactive")
		return nil, authRefusal(ownerReason, tokenOwnerID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken))
	}
	if err != nil {
		return nil, withAuthAuditUser(err, tokenOwnerID)
	}

	utils.Logger.WithField("user_id", tokens.User.ID).Info("Refresh token rotated")
	return tokens, nil
}

// rotateRefreshTokenInTransaction performs the rotation on transaction-bound
// repositories: lookup, locking owner read, conditional claim, owner verdict,
// successor insert and access token signing. It returns the owner id it learned from
// the token, if any, so the caller can log a rejection. Any error other than
// errRefreshOwnerRejected discards every write, the claim included.
func (s *authService) rotateRefreshTokenInTransaction(
	refreshRepo repository.RefreshTokenRepository,
	userRepo repository.UserRepository,
	hash string,
) (*AuthTokens, uint, error) {
	// The lookup already excludes revoked and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := refreshRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return s.refuseRevokedRefreshToken(refreshRepo, userRepo, hash)
		}
		return nil, 0, fmt.Errorf("refresh token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.Revoked || !stored.ExpiresAt.After(time.Now()) {
		return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// A single sign-on session can be refreshed only until ssoSessionMaxAge
	// after its sign-in, whatever expiry the row carries (rows issued before
	// the cap existed carry a later one). The refusal is the plain error, so
	// the transaction rolls back and nothing is written.
	if ssoSessionExpired(stored.SignInMethod, stored.SessionStartedAt, time.Now()) {
		return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	}

	// Lock the owner before claiming the token. Every transaction that
	// writes both an account and its refresh tokens takes the users row
	// first and the refresh_tokens rows second (the password change locks the
	// owner and then revokes every token; the reset does the same); claiming
	// first here would let a rotation of one token and a password change of
	// its owner each hold the lock the other waits for. The locking read also
	// returns the current committed row: on MySQL a plain SELECT inside this
	// REPEATABLE READ transaction would answer from the snapshot taken at the
	// token lookup, so an account deactivated or erased a moment after that
	// lookup would still look active and receive a fresh session. The scoped
	// lookup makes an erased (soft-deleted) owner a not-found; a deactivated
	// one is refused the same way. A lookup failure aborts before anything is
	// written, leaving the token usable once the database recovers.
	user, err := userRepo.GetByID(stored.UserID)
	ownerRejected := ""
	if err != nil {
		if !apperrors.IsNotFound(err) {
			return nil, stored.UserID, fmt.Errorf("refresh token owner lookup failed: %w", err)
		}
		ownerRejected = AuthAuditReasonAccountErased
	} else if !user.IsActive {
		ownerRejected = AuthAuditReasonAccountInactive
	}

	// Claim the token. The claim is the one write competing requests contend
	// on: whoever revokes the row first owns the rotation, and every other
	// request finds no row to revoke — even one that passed the lookup a
	// moment ago, or one whose token expired or was revoked between the
	// lookup and this statement.
	if err := refreshRepo.ClaimForRotation(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return nil, stored.UserID, fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
		}
		return nil, stored.UserID, fmt.Errorf("failed to claim refresh token for rotation: %w", err)
	}

	// A token must never outlive or outrank its owner. A missing, erased or
	// inactive owner is reported as errRefreshOwnerRejected so the caller
	// commits the claim as containment and issues nothing.
	if ownerRejected != "" {
		return nil, stored.UserID, authRefusal(ownerRejected, stored.UserID, errRefreshOwnerRejected)
	}

	// The successor is inserted and its access token signed inside the
	// transaction, so a failure of either rolls the claim back: the presented
	// token stays usable and the caller receives no credentials at all.
	// The successor continues the presented token's session unchanged.
	session := refreshSession{FamilyID: stored.FamilyID, SignInMethod: stored.SignInMethod, ProviderSessionHash: stored.ProviderSessionHash}
	if stored.SessionStartedAt != nil {
		session.StartedAt = *stored.SessionStartedAt
	}
	newRefreshToken, err := s.issueRefreshToken(refreshRepo, user.ID, session)
	if err != nil {
		return nil, stored.UserID, err
	}

	accessToken, err := s.refreshedAccessTokenFor(user)
	if err != nil {
		return nil, stored.UserID, fmt.Errorf("failed to sign access token during rotation: %w", err)
	}

	return &AuthTokens{AccessToken: accessToken, RefreshToken: newRefreshToken, User: user}, stored.UserID, nil
}

// refuseRevokedRefreshToken handles a presented token that is not live. It
// reads the revoked row in the rotation's transaction (on MySQL the same
// snapshot as the live lookup). An unknown token, a token without a family,
// or the newest token of its family (revoked by logout, a password change,
// deactivation or containment) is the generic refusal and writes nothing. A
// revoked token with a later row in its family was already rotated: the owner
// is locked first (users before refresh_tokens, the order of every session
// transaction), every live token of the family is revoked, and
// errRefreshTokenReuse tells the caller to commit. A missing owner does not
// stop the revocation; any other failure aborts with nothing written.
func (s *authService) refuseRevokedRefreshToken(
	refreshRepo repository.RefreshTokenRepository,
	userRepo repository.UserRepository,
	hash string,
) (*AuthTokens, uint, error) {
	rejected := fmt.Errorf("refresh token rejected: %w", ErrInvalidRefreshToken)
	stored, err := refreshRepo.GetRevokedByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, 0, rejected
		}
		return nil, 0, fmt.Errorf("refresh token lookup failed: %w", err)
	}
	if stored.FamilyID == "" {
		return nil, 0, rejected
	}
	rotated, err := refreshRepo.HasLaterInFamily(stored)
	if err != nil {
		return nil, 0, fmt.Errorf("refresh token family lookup failed: %w", err)
	}
	if !rotated {
		return nil, 0, rejected
	}

	if _, err := userRepo.GetByID(stored.UserID); err != nil && !apperrors.IsNotFound(err) {
		return nil, stored.UserID, fmt.Errorf("refresh token owner lookup failed: %w", err)
	}
	if err := refreshRepo.RevokeFamily(stored.UserID, stored.FamilyID); err != nil {
		return nil, stored.UserID, fmt.Errorf("failed to revoke reused refresh token session: %w", err)
	}
	return nil, stored.UserID, authRefusal(AuthAuditReasonTokenReuse, stored.UserID, errRefreshTokenReuse)
}

// InvalidateRefreshToken revokes the presented token unconditionally.
// Prefer Logout for request-scoped revocation, which is owner-checked.
func (s *authService) InvalidateRefreshToken(refreshToken string) error {
	if s.refreshTokenRepo == nil || refreshToken == "" {
		return fmt.Errorf("refresh not available: %w", ErrInvalidRefreshToken)
	}
	hash := hashOpaqueToken(refreshToken, s.sessionTokenSecret())
	return s.refreshTokenRepo.RevokeByTokenHash(hash)
}
