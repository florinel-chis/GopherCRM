package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"golang.org/x/crypto/bcrypt"
)

// resetRequestMaxRetries is how often the password-reset issuing transaction
// is replayed after a deadlock, a lock wait timeout or SQLite busy/locked
// (#183 retry rule).
const resetRequestMaxRetries = 3

// RequestPasswordReset starts the reset flow. It NEVER discloses whether the
// account exists: unknown email, inactive account, an owner erased or
// deactivated under the lock, a rollback, an unwired service and a mail
// failure all return nil, so the endpoint's response stays uniform. The raw
// token goes only into the mailed link; the store holds its hash.
//
// The plain email lookup runs first, outside any transaction. The token is then
// issued by one WithTransactionAndRetry transaction (#342,
// resetRequestMaxRetries) that locks the owner row by primary key (`SELECT ...
// FOR UPDATE` on MySQL/MariaDB, a plain read on SQLite) and refuses, writing
// nothing, when that row is missing, erased or inactive. With
// WithAuthIdentities, and while security.sso.allow_password is off (#457, read
// once before the transaction), it then refuses the same way an account that
// has a linked OpenID Connect identity (#444, a plain read of
// user_identities). With the setting on that read is skipped and a linked
// account is treated like a local one. Otherwise the user's
// outstanding tokens are spent and the new one inserted through the
// transaction-bound PasswordResetTokenRepository, in the order users →
// (read) user_identities → password_reset_tokens. The mail goes out once, after the commit, to the
// address of the locked row. A deactivation or erasure that commits before the
// lock therefore issues nothing, and one that commits after it ends the token
// it issued (the erasure deletes it, the confirmation refuses an inactive
// owner). Without a transaction manager nothing is written or sent.
//
// RequestPasswordReset is IssuePasswordReset with its result dropped.
func (s *authService) RequestPasswordReset(email string) error {
	_, _ = s.IssuePasswordReset(email)
	return nil
}

// errPasswordResetNotIssued is the refusal IssuePasswordReset reports when no
// reset link was stored; its authRefusal carries the audit reason.
var errPasswordResetNotIssued = errors.New("password reset not issued")

// IssuePasswordReset is the reset flow RequestPasswordReset describes, with
// its result (#550): the owner's id and nil once the token transaction has
// committed, whether or not the mail was delivered; otherwise an authRefusal
// around errPasswordResetNotIssued with the audit reason (unavailable,
// account_unknown, account_inactive, account_erased, sso_account or
// internal_error) and the account's id when it is known, 0 otherwise. Every
// outcome is logged here as before; the caller answers the same way whatever
// it returns.
func (s *authService) IssuePasswordReset(email string) (uint, error) {
	logger := utils.Logger.WithField("operation", "RequestPasswordReset")

	if s.resetTokenRepo == nil || s.mailer == nil || s.txManager == nil {
		logger.WithField("error_code", "reset_unwired").Warn("Password reset requested but the reset flow is not wired")
		return 0, authRefusal(AuthAuditReasonUnavailable, 0, errPasswordResetNotIssued)
	}

	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithField("error_code", "unknown_account").Info("Password reset requested for unknown email")
			return 0, authRefusal(AuthAuditReasonAccountUnknown, 0, errPasswordResetNotIssued)
		}
		logger.WithError(utils.LogError(err)).WithField("error_code", "lookup_failed").
			Error("Password reset user lookup failed")
		return 0, authRefusal(AuthAuditReasonInternalError, 0, errPasswordResetNotIssued)
	}
	logger = logger.WithField("user_id", user.ID)
	if !user.IsActive {
		logger.WithField("error_code", "account_inactive").Info("Password reset requested for inactive account; ignored")
		return user.ID, authRefusal(AuthAuditReasonAccountInactive, user.ID, errPasswordResetNotIssued)
	}

	raw, err := generateOpaqueToken()
	if err != nil {
		logger.WithError(utils.LogError(err)).WithField("error_code", "token_generation_failed").
			Error("Failed to generate reset token")
		return user.ID, authRefusal(AuthAuditReasonInternalError, user.ID, errPasswordResetNotIssued)
	}
	tokenHash := hashOpaqueToken(raw, s.sessionTokenSecret())

	if s.resetBeforeLock != nil {
		s.resetBeforeLock(user.ID)
	}

	// The single sign-on password policy is read once, outside the
	// transaction (#457).
	checkIdentity := s.checkSSOIdentity()

	var recipient string
	err = s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		// Owner first, by primary key, never through the email index; a fresh
		// Session so the locking clause does not leak into the token writes.
		owner, err := lockLiveCredentialOwner(
			s.userRepo.WithTx(repository.LockingRead(tx)),
			user.ID)
		if err != nil {
			return err
		}
		// An account linked to single sign-on, while
		// security.sso.allow_password is off (#444, #457): a plain read of
		// user_identities after the owner lock, refused like an unavailable
		// owner, nothing written, no mail.
		if checkIdentity {
			linked, err := s.identities.WithTx(tx).ExistsForUser(owner.ID)
			if err != nil {
				return fmt.Errorf("identity lookup failed: %w", err)
			}
			if linked {
				return &credentialOwnerRefusal{code: AuthAuditReasonSSOAccount}
			}
		}
		resetRepo := s.resetTokenRepo.WithTx(tx)
		// Only the newest link should work; stale outstanding tokens are spent
		// in the same transaction, so a failure keeps them as they were.
		if err := resetRepo.InvalidateAllForUser(owner.ID); err != nil {
			return fmt.Errorf("failed to invalidate previous reset tokens: %w", err)
		}
		token := &models.PasswordResetToken{
			UserID:    owner.ID,
			TokenHash: tokenHash,
			ExpiresAt: time.Now().Add(resetTokenTTL),
		}
		if err := resetRepo.Create(token); err != nil {
			return fmt.Errorf("failed to store reset token: %w", err)
		}
		recipient = owner.Email
		return nil
	}, resetRequestMaxRetries)
	if err != nil {
		var refusal *credentialOwnerRefusal
		if errors.As(err, &refusal) {
			logger.WithField("error_code", refusal.code).Info("Password reset refused under the owner lock")
			return user.ID, authRefusal(refusal.code, user.ID, errPasswordResetNotIssued)
		}
		logger.WithError(utils.LogError(err)).WithField("error_code", "reset_token_rolled_back").
			Error("Password reset issuing transaction rolled back")
		return user.ID, authRefusal(AuthAuditReasonInternalError, user.ID, errPasswordResetNotIssued)
	}

	resetURL := s.appBaseURL + "/reset-password?token=" + url.QueryEscape(raw)
	// The product name is read only now that a mail will go out; a failed read
	// falls back to "GopherCRM" and never blocks the send (#292).
	productName := ResolveMailProductName(s.productName)
	if err := s.mailer.SendPasswordReset(recipient, resetURL, productName); err != nil {
		// Already logged (redacted) by the mailer; swallow to stay uniform.
		logger.WithField("error_code", "mail_failed").Warn("Password reset mail delivery failed")
	}
	return user.ID, nil
}

// resetConfirmMaxRetries is how often the password-reset confirming
// transaction is replayed after a deadlock, a lock wait timeout or SQLite
// busy/locked (#183 retry rule, #407).
const resetConfirmMaxRetries = 3

// ConfirmPasswordReset spends a reset token and sets the new password. All
// token rejections map to ErrInvalidResetToken; a password that fails the
// complexity policy or cannot be hashed is reported as such.
//
// The new password is validated and hashed before anything is written, so a
// weak or unhashable password never spends the token: the user retries with
// the same link. The token lookup, its conditional claim, the owner check, the
// password and lockout update and the end of every credential
// (revokeAllCredentials: token_version incremented, every refresh token and
// every API key revoked) are then one transaction. The claim stamps the token only while it is still
// unused and strictly unexpired and must affect exactly one row, so two
// requests that both read the token as spendable cannot both succeed: the
// second claim finds nothing to stamp and is rejected. A failure after the
// claim — the password update, the credential revocation — rolls the whole unit
// back, token included, so the link stays usable for a retry instead of being
// spent on a reset that never happened. An inactive, erased or missing owner
// is rejected inside the transaction and leaves the token unspent as well; the
// owner is read with a locking read so that decision rests on the current
// committed account, not on the transaction's snapshot. With
// WithAuthIdentities, while security.sso.allow_password is off (#457, read
// once before the transaction), an owner that has a linked OpenID Connect
// identity (#444) is refused the same way, after the owner check and before
// any write: the token stays unspent and the answer is ErrInvalidResetToken.
// With the setting on the identity read is skipped.
//
// The transaction claims the token before it locks the owner, the reverse of
// the order an erasure and a reset request take, so it can lose a deadlock or
// a lock wait against either. It is therefore replayed up to
// resetConfirmMaxRetries times (#407). Every attempt is a fresh root
// transaction that redoes the lookup, the claim, the owner lock and the
// writes on current committed rows: a replay that finds the token gone or
// spent, or its owner erased, ends in ErrInvalidResetToken like any other
// refusal. When the bound is exhausted the last error is returned as it is
// and nothing is written; the link is still valid then.
//
// ConfirmPasswordReset is CompletePasswordReset without the owner's id.
func (s *authService) ConfirmPasswordReset(token, newPassword string) error {
	_, err := s.CompletePasswordReset(token, newPassword)
	return err
}

// CompletePasswordReset is the confirmation ConfirmPasswordReset describes
// and returns the owner's id on success (#550). Each refusal still matches
// ErrInvalidResetToken with the same message, and carries the audit reason
// (unavailable, token_invalid, account_erased, account_inactive or
// sso_account) and the owner's id once a stored token was found: an unknown,
// spent or expired token is a lookup miss and names no account.
func (s *authService) CompletePasswordReset(token, newPassword string) (uint, error) {
	// Every dependency of the transactional flow is required. Running the old
	// sequence of separate writes when one is missing would reintroduce the
	// burnt-link and double-spend failures this flow exists to prevent.
	if s.resetTokenRepo == nil || s.refreshTokenRepo == nil || s.apiKeyRepo == nil || s.txManager == nil {
		return 0, authRefusal(AuthAuditReasonUnavailable, 0, fmt.Errorf("reset not available: %w", ErrInvalidResetToken))
	}
	if token == "" {
		return 0, authRefusal(AuthAuditReasonTokenInvalid, 0, fmt.Errorf("reset not available: %w", ErrInvalidResetToken))
	}

	// A weak password must NOT spend the token: the user retries with the
	// same link instead of having to request a new one.
	if err := utils.ValidatePasswordComplexity(newPassword); err != nil {
		return 0, err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt refuses inputs over 72 bytes; nothing has been written.
		return 0, fmt.Errorf("failed to hash password: %w", err)
	}

	hash := hashOpaqueToken(token, s.sessionTokenSecret())

	// The single sign-on password policy is read once, outside the
	// transaction (#457).
	checkIdentity := s.checkSSOIdentity()

	var userID uint
	err = s.txManager.WithTransactionAndRetry(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		var identities repository.UserIdentityRepository
		if checkIdentity {
			identities = s.identities.WithTx(tx)
		}
		// The owner repository is bound to a locking handle so its read is
		// `SELECT ... FOR UPDATE` on MySQL (SQLite drops the clause). The
		// handle must be a fresh Session: Clauses alone returns a statement
		// that GORM keeps reusing, so the owner lookup would leave its table
		// and conditions on it and the following Save would then build an
		// invalid query (SQLite reports an ambiguous `users.id`).
		userID, err = s.confirmPasswordResetInTransaction(
			s.resetTokenRepo.WithTx(tx), s.userRepo.WithTx(repository.LockingRead(tx)), s.refreshTokenRepo.WithTx(tx),
			s.apiKeyRepo.WithTx(tx), identities, hash, string(hashed))
		return err
	}, resetConfirmMaxRetries)
	if err != nil {
		return 0, err
	}

	utils.Logger.WithField("user_id", userID).WithField("operation", "ConfirmPasswordReset").
		Info("Password reset completed; all sessions, earlier access tokens and API keys ended")
	return userID, nil
}

// confirmPasswordResetInTransaction performs the reset on transaction-bound
// repositories and returns the owner's id. Any error discards every write,
// the token claim included. identities is nil when the service was built
// without WithAuthIdentities or security.sso.allow_password is on; otherwise
// an owner with a linked identity is refused (#444, #457).
func (s *authService) confirmPasswordResetInTransaction(
	resetRepo repository.PasswordResetTokenRepository,
	userRepo repository.UserRepository,
	refreshRepo repository.RefreshTokenRepository,
	apiKeyRepo repository.APIKeyRepository,
	identities repository.UserIdentityRepository,
	hash, passwordHash string,
) (uint, error) {
	// The lookup already excludes used and expired tokens; the claim below
	// re-applies both predicates as part of its own UPDATE.
	stored, err := resetRepo.GetByTokenHash(hash)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return 0, authRefusal(AuthAuditReasonTokenInvalid, 0, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
		return 0, fmt.Errorf("reset token lookup failed: %w", err)
	}

	// Defence in depth in case a repository implementation stops filtering.
	if stored.UsedAt != nil || !stored.ExpiresAt.After(time.Now()) {
		return 0, authRefusal(AuthAuditReasonTokenInvalid, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
	}

	// Claim the token before reading anything else. The claim is the one
	// write competing requests contend on: whoever stamps the row first owns
	// the reset, and every other request finds no row to stamp — even one
	// that passed the lookup a moment ago, or one whose token expired between
	// the lookup and this statement.
	if err := resetRepo.MarkUsed(stored.ID); err != nil {
		if apperrors.IsNotFound(err) {
			return 0, authRefusal(AuthAuditReasonTokenInvalid, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
		return 0, fmt.Errorf("failed to mark reset token used: %w", err)
	}

	// A token must never outlive its owner. The owner is read with a locking
	// read: on MySQL a plain SELECT inside this REPEATABLE READ transaction
	// would answer from the snapshot taken at the token lookup, so an account
	// deactivated or erased a moment after that lookup would still look active
	// — and the full-row update below would write that stale state back,
	// reactivating it. The locking read returns the current committed row and
	// holds its lock until commit, so nothing changes it underneath the update.
	// The scoped lookup makes an erased (soft-deleted) owner a not-found; a
	// deactivated one is refused the same way. The rollback that follows hands
	// the claim back, so the committed state is exactly what it was.
	user, err := userRepo.GetByID(stored.UserID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return 0, authRefusal(AuthAuditReasonAccountErased, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
		return 0, fmt.Errorf("reset token owner lookup failed: %w", err)
	}
	if !user.IsActive {
		return 0, authRefusal(AuthAuditReasonAccountInactive, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
	}
	// An account linked to single sign-on, while security.sso.allow_password
	// is off (#444, #457); a plain read after the owner lock. The rollback
	// hands the claim back, as for the other owner refusals.
	if identities != nil {
		linked, err := identities.ExistsForUser(user.ID)
		if err != nil {
			return 0, fmt.Errorf("reset token owner identity lookup failed: %w", err)
		}
		if linked {
			return 0, authRefusal(AuthAuditReasonSSOAccount, stored.UserID, fmt.Errorf("reset token rejected: %w", ErrInvalidResetToken))
		}
	}

	// Ownership of the mailbox has just been proven; clear any lockout so the
	// user can sign in with the new password immediately. Only these columns
	// are written, and only while the row is live.
	if err := userRepo.UpdateFields(user.ID, map[string]interface{}{
		"password":              passwordHash,
		"failed_login_attempts": 0,
		"locked_until":          nil,
	}); err != nil {
		return 0, fmt.Errorf("failed to update password: %w", err)
	}

	// Every credential ends with the old password: the session generation is
	// incremented, which ends every access JWT issued before the reset
	// (#236), and every refresh token and API key is revoked; the row is
	// locked, so the value read above is current. A revocation that fails is
	// a failure of the reset itself: the password and the claim are rolled
	// back with it.
	if err := revokeAllCredentials(credentialStores{
		users: userRepo, refreshTokens: refreshRepo, apiKeys: apiKeyRepo,
	}, user, time.Now()); err != nil {
		return 0, fmt.Errorf("failed to end credentials after password reset: %w", err)
	}
	return user.ID, nil
}
