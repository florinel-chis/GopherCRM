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
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-playground/validator/v10"
)

// SCIM 2.0 user create and replace (#470). Both are one governed transaction
// that keeps the lock order of the OIDC sign-in:
//
//	admin set → users → roles → user_identities
//
// The role always comes from the single sign-on role mapping: the first entry
// with claim roles, in stored order, whose value equals one of the request's
// roles values exactly. A mapping to the customer system role is refused,
// because the SCIM Users resource serves staff and custom accounts only. A
// change of role or role_id commits its user_role_changed audit row with
// auth_method scim and actor_user_id NULL; after the commit one successful
// auth_audit_log row with method scim names the account. A refusal writes
// nothing, audit rows included.

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
// without WithSCIMUserWrites.
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

// scimUserWrites is what the writes need besides the read half.
type scimUserWrites struct {
	users      *userService
	identities repository.UserIdentityRepository
	audit      AuthAuditService
}

// WithSCIMUserWrites enables Create and Replace over the governed user
// writes, the identity repository and the sign-in audit log.
func WithSCIMUserWrites(userRepo repository.UserRepository, roleRepo repository.RoleRepository, roleAuditRepo repository.RoleAuditRepository,
	identities repository.UserIdentityRepository, txManager *utils.TransactionManager, audit AuthAuditService) SCIMUserServiceOption {
	return func(s *scimUserService) {
		s.writes = &scimUserWrites{
			users:      &userService{userRepo: userRepo, governance: &roleGovernance{users: userRepo, roles: roleRepo, audit: roleAuditRepo, txManager: txManager}},
			identities: identities,
			audit:      audit,
		}
	}
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
// link set at creation never changes.
func (s *scimUserService) Replace(ctx context.Context, rawID string, in SCIMUserInput) (*SCIMUserRecord, error) {
	if s.writes == nil {
		return nil, errSCIMWritesNotWired
	}
	parsed, err := strconv.ParseUint(rawID, 10, strconv.IntSize)
	if err != nil || parsed == 0 {
		return nil, fmt.Errorf("scim user not found: %w", apperrors.ErrNotFound)
	}
	id := uint(parsed)
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
		deactivates := in.Active != nil && !*in.Active
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
		if err := tx.users.UpdateFields(id, fields); err != nil {
			if errors.Is(err, apperrors.ErrDuplicateEmail) {
				return fmt.Errorf("userName is taken: %w", ErrSCIMUniqueness)
			}
			if isNotFound(err) {
				return fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
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
