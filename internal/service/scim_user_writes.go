package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-playground/validator/v10"
)

// SCIM 2.0 user create and replace (#470), patch (#471) and delete (#504).
// Each is one governed transaction that keeps the lock order of the OIDC
// sign-in:
//
//	admin set → users → roles → user_identities (create and replace)
//	admin set → users → roles → refresh_tokens  (patch, a deactivating replace)
//	admin set → users → refresh_tokens          (delete)
//
// The role always comes from the single sign-on role mapping: the first entry
// with claim roles, in stored order, whose value equals one of the request's
// roles values exactly. A mapping to the customer system role is refused,
// because the SCIM Users resource serves staff and custom accounts only. A
// change of role or role_id commits its user_role_changed audit row with
// auth_method scim and actor_user_id NULL; after the commit one successful
// auth_audit_log row with method scim names the account (create, replace or
// patch). A write that sets active false deprovisions: in the same
// transaction it increments users.token_version, which ends every access
// token, and revokes every refresh token of the account. A refusal writes
// nothing, audit rows included. Delete deactivates the same way and never
// erases; on an already inactive account it writes nothing.

// Refusals of a SCIM user write. The handler answers ErrSCIMInvalidValue with
// 400 invalidValue and ErrSCIMUniqueness with 409 uniqueness.
var (
	ErrSCIMInvalidValue = errors.New("scim: invalid value")
	ErrSCIMUniqueness   = errors.New("scim: uniqueness")
	// ErrSCIMLastAdmin is the last-admin refusal; it is ErrSCIMInvalidValue
	// to errors.Is.
	ErrSCIMLastAdmin = fmt.Errorf("scim: the last active admin must stay an active admin: %w", ErrSCIMInvalidValue)
)

// errSCIMWritesNotWired is a wiring fault (500): NewSCIMUserService was built
// without WithSCIMUserWrites, or without its refresh token repository for a
// patch, a deactivating replace or a delete (#504).
var errSCIMWritesNotWired = errors.New("scim user writes need WithSCIMUserWrites")

// Bounds of the SCIM user attributes.
const (
	scimUserNameMaxBytes = 255
	scimNamePartMaxRunes = 100
)

// scimEmailValidator holds the email rule POST /users binds with.
var scimEmailValidator = validator.New()

// SCIMUserInput is what a SCIM User create or replace body carries. Absent
// strings are "". Active nil leaves is_active as it is on replace (true on
// create). Roles holds the value of every roles entry; nil when absent.
// ExternalID is read by Create only.
type SCIMUserInput struct {
	UserName   string
	ExternalID string
	GivenName  string
	FamilyName string
	Active     *bool
	Roles      []string
}

// SCIMUserPatch is a parsed SCIM PatchOp on a User (#471); nil fields are
// untouched.
type SCIMUserPatch = scim.UserPatch

// scimUserWrites is what the writes need besides the read half.
type scimUserWrites struct {
	users         *userService
	identities    repository.UserIdentityRepository
	refreshTokens repository.RefreshTokenRepository
	audit         AuthAuditService
}

// WithSCIMUserWrites enables Create, Replace, Patch and Delete over the
// governed user writes, the identity repository, the refresh tokens a
// deactivation revokes and the sign-in audit log. A nil refreshTokens refuses
// Patch, Delete and a deactivating Replace as a wiring fault.
func WithSCIMUserWrites(userRepo repository.UserRepository, roleRepo repository.RoleRepository, roleAuditRepo repository.RoleAuditRepository,
	identities repository.UserIdentityRepository, refreshTokens repository.RefreshTokenRepository, txManager *utils.TransactionManager,
	audit AuthAuditService) SCIMUserServiceOption {
	return func(s *scimUserService) {
		s.writes = &scimUserWrites{
			users:         &userService{userRepo: userRepo, governance: &roleGovernance{users: userRepo, roles: roleRepo, audit: roleAuditRepo, txManager: txManager}},
			identities:    identities,
			refreshTokens: refreshTokens,
			audit:         audit,
		}
	}
}

// deprovisionFields adds the session end of a deactivation to the users
// write of a SCIM write: when the request sets active false (even on an
// already inactive row), token_version becomes the locked row's value plus
// one, which refuses every access token issued before. The caller revokes the
// refresh tokens in the same transaction, after the users write.
func deprovisionFields(fields map[string]interface{}, locked *models.User, deactivates bool) {
	if deactivates {
		fields["token_version"] = locked.TokenVersion + 1
	}
}

// parseSCIMUserID parses the id of a Users resource; anything but a positive
// integer is a not-found.
func parseSCIMUserID(rawID string) (uint, error) {
	parsed, err := strconv.ParseUint(rawID, 10, strconv.IntSize)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("scim user not found: %w", apperrors.ErrNotFound)
	}
	return uint(parsed), nil
}

// scimInvalid is an invalidValue refusal; why is for logs and tests only.
func scimInvalid(why string) error {
	return fmt.Errorf("%s: %w", why, ErrSCIMInvalidValue)
}

// checkSCIMProfile validates userName and the name parts.
func checkSCIMProfile(in SCIMUserInput) error {
	if in.UserName == "" || len(in.UserName) > scimUserNameMaxBytes {
		return scimInvalid("userName is empty or too long")
	}
	if utf8.RuneCountInString(in.GivenName) > scimNamePartMaxRunes || utf8.RuneCountInString(in.FamilyName) > scimNamePartMaxRunes {
		return scimInvalid("a name part is too long")
	}
	return nil
}

// checkSCIMEmail is the address format rule of POST /users.
func checkSCIMEmail(email string) error {
	if scimEmailValidator.Var(email, "email") != nil {
		return scimInvalid("userName is not an email address")
	}
	return nil
}

// scimMappedRole resolves the locked role row the mapping names. A missing
// row and the customer system role are invalidValue.
func scimMappedRole(row *models.Role) (models.UserRole, *uint, *models.Role, error) {
	role, roleID, custom, err := oidcMappedRole(row)
	if err != nil {
		return "", nil, nil, scimInvalid("the mapped role does not exist")
	}
	if role == models.RoleCustomer {
		return "", nil, nil, scimInvalid("the mapped role is customer")
	}
	return role, roleID, custom, nil
}

// recordSuccess writes the successful auth_audit_log row of a write, after
// its commit. A failed write is logged by the audit service and never changes
// the answer.
func (w *scimUserWrites) recordSuccess(ctx context.Context, userID uint) {
	if w.audit == nil {
		return
	}
	w.audit.Record(ctx, AuthAuditEntry{Method: AuthAuditMethodSCIM, Outcome: AuthAuditOutcomeSuccess, UserID: userID})
}

// Create provisions an account and its identity under the effective single
// sign-on issuer, with externalId as the subject.
func (s *scimUserService) Create(ctx context.Context, in SCIMUserInput) (*SCIMUserRecord, error) {
	if s.writes == nil {
		return nil, errSCIMWritesNotWired
	}
	cfg, err := s.issuers.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	if !validOIDCSubject(in.ExternalID) {
		return nil, scimInvalid("externalId is missing or invalid")
	}
	if err := checkSCIMProfile(in); err != nil {
		return nil, err
	}
	issuer := cfg.IssuerURL
	if issuer == "" || len(issuer) > oidcIssuerMaxBytes {
		return nil, scimInvalid("no usable single sign-on issuer")
	}
	mapped, matched := oidcMatchRole(cfg.RoleMapping, nil, in.Roles)
	if !matched {
		return nil, scimInvalid("no roles mapping entry matches")
	}
	passwordHash, err := oidcUnusablePasswordHash()
	if err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	var created *models.User
	err = s.writes.users.governance.run(ctx, func(tx *governedTx) error {
		if _, err := tx.userRoles.LockActiveAdmins(); err != nil {
			return err
		}
		roles, err := tx.lockRoles(&mapped)
		if err != nil {
			return err
		}
		role, roleID, custom, err := scimMappedRole(roles[mapped])
		if err != nil {
			return err
		}
		taken, err := tx.users.EmailExistsFoldedUnscoped(in.UserName)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
		}
		identities := s.writes.identities.WithTx(tx.handle)
		identity, err := identities.GetByIssuerSubject(issuer, in.ExternalID)
		if err != nil && !apperrors.IsNotFound(err) {
			return err
		}
		if identity != nil {
			return fmt.Errorf("externalId is linked: %w", ErrSCIMUniqueness)
		}
		// After the uniqueness checks on purpose: the MySQL/MariaDB collation
		// finds a padded or accented variant of a stored address first.
		if err := checkSCIMEmail(in.UserName); err != nil {
			return err
		}
		// A replayed attempt inserts a fresh row.
		user := &models.User{
			Email:     in.UserName,
			Password:  passwordHash,
			FirstName: in.GivenName,
			LastName:  in.FamilyName,
			Role:      role,
			RoleID:    roleID,
			IsActive:  active,
		}
		if err := tx.users.Create(user); err != nil {
			if errors.Is(err, apperrors.ErrDuplicateEmail) {
				return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
			}
			return err
		}
		if !active {
			// The is_active column's default:true replaces a false on
			// insert; write it explicitly so the row is what was asked.
			if err := tx.users.UpdateFields(user.ID, map[string]interface{}{"is_active": false}); err != nil {
				return err
			}
		}
		if err := identities.Create(&models.UserIdentity{UserID: user.ID, Issuer: issuer, Subject: in.ExternalID}); err != nil {
			if errors.Is(err, repository.ErrUserIdentityTaken) {
				return fmt.Errorf("externalId is linked: %w", ErrSCIMUniqueness)
			}
			return err
		}
		if err := tx.record(Actor{AuthMethod: AuthMethodSCIM}, models.RoleAuditUserRoleChanged,
			roleID, assignedRoleName(role, custom), &user.ID,
			nil, userRoleAuditState{Role: role, RoleID: roleID}); err != nil {
			return err
		}
		if created, err = tx.users.GetByID(user.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.writes.recordSuccess(ctx, created.ID)
	return &SCIMUserRecord{User: *created, ExternalID: in.ExternalID}, nil
}

// Replace overwrites the visible user rawID names. externalId is ignored: the
// link set at creation never changes. Active false deprovisions (#471).
func (s *scimUserService) Replace(ctx context.Context, rawID string, in SCIMUserInput) (*SCIMUserRecord, error) {
	deactivates := in.Active != nil && !*in.Active
	if s.writes == nil || (deactivates && s.writes.refreshTokens == nil) {
		return nil, errSCIMWritesNotWired
	}
	id, err := parseSCIMUserID(rawID)
	if err != nil {
		return nil, err
	}
	if err := checkSCIMProfile(in); err != nil {
		return nil, err
	}
	cfg, err := s.issuers.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	mapped, matched := oidcMatchRole(cfg.RoleMapping, nil, in.Roles)
	if !matched {
		return nil, scimInvalid("no roles mapping entry matches")
	}

	var result *models.User
	err = s.writes.users.governance.run(ctx, func(tx *governedTx) error {
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		users, err := tx.lockUsers(id)
		if err != nil {
			return err
		}
		target := users[id]
		if target == nil || target.Role == models.RoleCustomer {
			return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
		}
		roles, err := tx.lockRoles(target.RoleID, &mapped)
		if err != nil {
			return err
		}
		role, roleID, custom, err := scimMappedRole(roles[mapped])
		if err != nil {
			return err
		}
		if (role != models.RoleAdmin || deactivates) && isLastActiveAdmin(admins, target.ID) {
			return fmt.Errorf("user %d: %w", id, ErrSCIMLastAdmin)
		}

		fields := map[string]interface{}{
			"first_name": in.GivenName,
			"last_name":  in.FamilyName,
			"role":       role,
			"role_id":    nil,
		}
		if roleID != nil {
			fields["role_id"] = *roleID
		}
		if in.UserName != target.Email {
			taken, err := s.repo.WithTx(tx.handle).EmailTakenByOther(in.UserName, id)
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
			}
			if err := checkSCIMEmail(in.UserName); err != nil {
				return err
			}
			fields["email"] = in.UserName
		}
		if in.Active != nil {
			fields["is_active"] = *in.Active
		}
		deprovisionFields(fields, target, deactivates)
		if err := tx.users.UpdateFields(id, fields); err != nil {
			if errors.Is(err, apperrors.ErrDuplicateEmail) {
				return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
			}
			if isNotFound(err) {
				return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		if deactivates {
			if err := s.writes.refreshTokens.WithTx(tx.handle).RevokeAllForUser(id); err != nil {
				return fmt.Errorf("revoke the sessions of scim user %d: %w", id, err)
			}
		}
		if role != target.Role || !sameRoleID(roleID, target.RoleID) {
			if err := tx.record(Actor{AuthMethod: AuthMethodSCIM}, models.RoleAuditUserRoleChanged,
				roleID, assignedRoleName(role, custom), &id,
				userRoleAuditState{Role: target.Role, RoleID: target.RoleID},
				userRoleAuditState{Role: role, RoleID: roleID}); err != nil {
				return err
			}
		}
		if result, err = tx.users.GetByID(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.writes.recordSuccess(ctx, result.ID)
	records, err := s.withExternalIDs(ctx, []models.User{*result})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

// Patch applies a SCIM PatchOp (#471) to the visible user rawID names, with
// the checks of Replace for the attributes it touches. The patch is applied
// to the row locked in the transaction. Untouched roles keep the stored role
// and write no role audit row; active false deprovisions as in Replace.
func (s *scimUserService) Patch(ctx context.Context, rawID string, p SCIMUserPatch) (*SCIMUserRecord, error) {
	if s.writes == nil || s.writes.refreshTokens == nil {
		return nil, errSCIMWritesNotWired
	}
	id, err := parseSCIMUserID(rawID)
	if err != nil {
		return nil, err
	}
	var mapped *uint
	if p.Roles != nil {
		cfg, err := s.issuers.SSOConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the single sign-on settings: %w", err)
		}
		roleID, matched := oidcMatchRole(cfg.RoleMapping, nil, *p.Roles)
		if !matched {
			return nil, scimInvalid("no roles mapping entry matches")
		}
		mapped = &roleID
	}
	deactivates := p.Active != nil && !*p.Active

	var result *models.User
	err = s.writes.users.governance.run(ctx, func(tx *governedTx) error {
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		users, err := tx.lockUsers(id)
		if err != nil {
			return err
		}
		target := users[id]
		if target == nil || target.Role == models.RoleCustomer {
			return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
		}
		roles, err := tx.lockRoles(target.RoleID, mapped)
		if err != nil {
			return err
		}
		role, roleID := target.Role, target.RoleID
		var custom *models.Role
		if mapped != nil {
			if role, roleID, custom, err = scimMappedRole(roles[*mapped]); err != nil {
				return err
			}
		}

		// The resulting profile: userName is the email, and a userName the
		// patch sets wins over its emails.
		after := SCIMUserInput{UserName: target.Email, GivenName: target.FirstName, FamilyName: target.LastName}
		switch {
		case p.UserName != nil:
			after.UserName = *p.UserName
		case p.Email != nil:
			after.UserName = *p.Email
		}
		if p.GivenName != nil {
			after.GivenName = *p.GivenName
		}
		if p.FamilyName != nil {
			after.FamilyName = *p.FamilyName
		}
		if err := checkSCIMProfile(after); err != nil {
			return err
		}
		if (role != models.RoleAdmin || deactivates) && isLastActiveAdmin(admins, target.ID) {
			return fmt.Errorf("user %d: %w", id, ErrSCIMLastAdmin)
		}

		fields := map[string]interface{}{}
		if p.GivenName != nil {
			fields["first_name"] = after.GivenName
		}
		if p.FamilyName != nil {
			fields["last_name"] = after.FamilyName
		}
		if mapped != nil {
			fields["role"] = role
			fields["role_id"] = nil
			if roleID != nil {
				fields["role_id"] = *roleID
			}
		}
		if after.UserName != target.Email {
			taken, err := s.repo.WithTx(tx.handle).EmailTakenByOther(after.UserName, id)
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
			}
			if err := checkSCIMEmail(after.UserName); err != nil {
				return err
			}
			fields["email"] = after.UserName
		}
		if p.Active != nil {
			fields["is_active"] = *p.Active
		}
		deprovisionFields(fields, target, deactivates)
		if len(fields) > 0 {
			if err := tx.users.UpdateFields(id, fields); err != nil {
				if errors.Is(err, apperrors.ErrDuplicateEmail) {
					return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
				}
				if isNotFound(err) {
					return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
				}
				return err
			}
		}
		if deactivates {
			if err := s.writes.refreshTokens.WithTx(tx.handle).RevokeAllForUser(id); err != nil {
				return fmt.Errorf("revoke the sessions of scim user %d: %w", id, err)
			}
		}
		if role != target.Role || !sameRoleID(roleID, target.RoleID) {
			if err := tx.record(Actor{AuthMethod: AuthMethodSCIM}, models.RoleAuditUserRoleChanged,
				roleID, assignedRoleName(role, custom), &id,
				userRoleAuditState{Role: target.Role, RoleID: target.RoleID},
				userRoleAuditState{Role: role, RoleID: roleID}); err != nil {
				return err
			}
		}
		if result, err = tx.users.GetByID(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.writes.recordSuccess(ctx, result.ID)
	records, err := s.withExternalIDs(ctx, []models.User{*result})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

// Delete deactivates the visible user rawID names (#504) the way active false
// does: in one governed transaction it sets is_active false, increments
// token_version and revokes every refresh token. It never erases: no
// deleted_at, and email, names, role, role_id and the identity link stay. An
// already inactive account is a success that writes nothing, audit rows
// included (unlike Patch, which bumps token_version again). The last active
// admin is ErrSCIMLastAdmin. No role is written, so no role row is locked.
func (s *scimUserService) Delete(ctx context.Context, rawID string) error {
	if s.writes == nil || s.writes.refreshTokens == nil {
		return errSCIMWritesNotWired
	}
	id, err := parseSCIMUserID(rawID)
	if err != nil {
		return err
	}
	changed := true
	err = s.writes.users.governance.run(ctx, func(tx *governedTx) error {
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		users, err := tx.lockUsers(id)
		if err != nil {
			return err
		}
		target := users[id]
		if target == nil || target.Role == models.RoleCustomer {
			return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
		}
		if !target.IsActive {
			changed = false
			return nil
		}
		if isLastActiveAdmin(admins, target.ID) {
			return fmt.Errorf("user %d: %w", id, ErrSCIMLastAdmin)
		}
		fields := map[string]interface{}{"is_active": false}
		deprovisionFields(fields, target, true)
		if err := tx.users.UpdateFields(id, fields); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		if err := s.writes.refreshTokens.WithTx(tx.handle).RevokeAllForUser(id); err != nil {
			return fmt.Errorf("revoke the sessions of scim user %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.writes.recordSuccess(ctx, id)
	}
	return nil
}
