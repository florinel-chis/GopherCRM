package service

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// The governed user writes (#259 S4, #348): every role, role_id, activation
// and erasure write of the user routes, and every write to another account.
// Each is one transaction, replayed on a deadlock, that locks
//
//	the active admin set → actor and target (ascending id) → the custom
//	roles of actor, target and the requested role (ascending id)
//
// re-reads all of them under the locks and then refuses, in this order: an
// actor that lacks a key the write needs (ErrForbidden), ErrRoleEscalation,
// ErrCustomerLoginRole, ErrLastAdmin. A missing target is ErrNotFound and a
// missing requested role ErrRoleNotFound, both before the refusals. A changed
// role or role_id commits with its user_role_changed audit row.

// WithUserRoleGovernance turns the actor-taking writes of the user service
// into governed transactions. The user repository must implement
// repository.UserRoleMaintenance, as the production one does.
func WithUserRoleGovernance(roleRepo repository.RoleRepository, auditRepo repository.RoleAuditRepository, txManager *utils.TransactionManager) UserServiceOption {
	return func(s *userService) {
		s.governance = &roleGovernance{users: s.userRepo, roles: roleRepo, audit: auditRepo, txManager: txManager}
	}
}

// RoleNeedsAssignPermission reports whether giving the role to an account
// needs users.assign_role: every role but customer does.
func RoleNeedsAssignPermission(role models.UserRole) bool {
	return role != models.RoleCustomer
}

// CheckRoleAssignment is the consistency rule of role and role_id: role
// custom names a role, every other role names none. A violation is
// ErrInvalidRoleAssignment.
func CheckRoleAssignment(role models.UserRole, roleID *uint) error {
	if role == models.RoleCustom {
		if roleID == nil || *roleID == 0 {
			return fmt.Errorf("role custom needs role_id: %w", apperrors.ErrInvalidRoleAssignment)
		}
		return nil
	}
	if roleID != nil {
		return fmt.Errorf("role_id goes with role custom only: %w", apperrors.ErrInvalidRoleAssignment)
	}
	return nil
}

// requestedRole resolves the role a write asks for against the locked role
// rows. A role_id no row has is ErrRoleNotFound; one naming a system role is
// ErrInvalidRoleAssignment, since a system role is assigned by its name.
func requestedRole(role models.UserRole, roleID *uint, roles map[uint]*models.Role) (*models.Role, error) {
	if role != models.RoleCustom {
		return nil, nil
	}
	custom := roles[*roleID]
	if custom == nil {
		return nil, fmt.Errorf("role %d: %w", *roleID, apperrors.ErrRoleNotFound)
	}
	if custom.SystemKey != nil {
		return nil, fmt.Errorf("role %d is a system role: %w", *roleID, apperrors.ErrInvalidRoleAssignment)
	}
	return custom, nil
}

// assignedRoleName is the role_name snapshot of a user's role change: the
// custom role's name, or the system role's key.
func assignedRoleName(role models.UserRole, custom *models.Role) string {
	if custom != nil {
		return custom.Name
	}
	return string(role)
}

// isLastActiveAdmin reports whether the locked active admin set is this
// account alone.
func isLastActiveAdmin(admins []models.User, id uint) bool {
	return len(admins) == 1 && admins[0].ID == id
}

func (s *userService) CreateAs(ctx context.Context, actor Actor, user *models.User, password string) error {
	if s.governance == nil {
		return s.Register(user, password)
	}
	if err := CheckRoleAssignment(user.Role, user.RoleID); err != nil {
		return err
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	return s.governance.run(ctx, func(tx *governedTx) error {
		// A replayed attempt inserts a fresh row.
		user.ID = 0
		user.Password = string(hashedPassword)

		if _, err := tx.userRoles.LockActiveAdmins(); err != nil {
			return err
		}
		users, err := tx.lockUsers(actor.UserID)
		if err != nil {
			return err
		}
		actorUser := users[actor.UserID]
		var actorRoleID *uint
		if actorUser != nil {
			actorRoleID = actorUser.RoleID
		}
		roles, err := tx.lockRoles(actorRoleID, user.RoleID)
		if err != nil {
			return err
		}
		custom, err := requestedRole(user.Role, user.RoleID, roles)
		if err != nil {
			return err
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		keys := []string{permissions.UsersCreate}
		if RoleNeedsAssignPermission(user.Role) {
			keys = append(keys, permissions.UsersAssignRole)
		}
		if err := acting.requireKeys(keys...); err != nil {
			return err
		}
		if err := checkEscalation(acting, escalationSet(user.Role, custom)); err != nil {
			return err
		}

		// Unscoped, as in Register: an erased row still holds its address in
		// the unique index.
		if existing, _ := tx.users.GetByEmailUnscoped(user.Email); existing != nil {
			return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		if err := tx.users.Create(user); err != nil {
			return err
		}
		return tx.record(actor, models.RoleAuditUserRoleChanged, user.RoleID, assignedRoleName(user.Role, custom), &user.ID,
			nil, userRoleAuditState{Role: user.Role, RoleID: user.RoleID})
	})
}

// governedChange is what one governed write to an existing account asks for.
type governedChange struct {
	// keys the actor must still hold, whatever the change.
	keys []string
	// profile fields, written as they are.
	email, firstName, lastName *string
	// role and roleID are set together when the role is written.
	role     *models.UserRole
	roleID   *uint
	isActive *bool
	// erase runs the erasure instead of an update.
	erase bool
}

func (s *userService) UpdateAs(ctx context.Context, actor Actor, id uint, updates map[string]interface{}) (*models.User, error) {
	if s.governance == nil {
		return s.Update(id, updates)
	}
	change := governedChange{}
	if id != actor.UserID {
		change.keys = append(change.keys, permissions.UsersUpdate)
	}
	if email, ok := updates["email"].(string); ok {
		change.email = &email
	}
	if firstName, ok := updates["first_name"].(string); ok {
		change.firstName = &firstName
	}
	if lastName, ok := updates["last_name"].(string); ok {
		change.lastName = &lastName
	}
	roleID, _ := updates["role_id"].(*uint)
	if role, ok := updates["role"].(models.UserRole); ok {
		if err := CheckRoleAssignment(role, roleID); err != nil {
			return nil, err
		}
		change.role, change.roleID = &role, roleID
		change.keys = append(change.keys, permissions.UsersAssignRole)
	} else if roleID != nil {
		return nil, fmt.Errorf("role_id without role custom: %w", apperrors.ErrInvalidRoleAssignment)
	}
	if isActive, ok := updates["is_active"].(bool); ok {
		change.isActive = &isActive
		change.keys = append(change.keys, permissions.UsersActivate)
	}
	return s.governedWrite(ctx, actor, id, change)
}

func (s *userService) SetActiveAs(ctx context.Context, actor Actor, id uint, active bool) (*models.User, error) {
	if s.governance == nil {
		return s.Update(id, map[string]interface{}{"is_active": active})
	}
	return s.governedWrite(ctx, actor, id, governedChange{keys: []string{permissions.UsersActivate}, isActive: &active})
}

func (s *userService) DeleteAs(ctx context.Context, actor Actor, id uint) error {
	if s.governance == nil {
		return s.Delete(id)
	}
	_, err := s.governedWrite(ctx, actor, id, governedChange{keys: []string{permissions.UsersDelete}, erase: true})
	return err
}

// governedWrite is the one transaction of every governed write to an existing
// account: update, activation, deactivation and erasure.
func (s *userService) governedWrite(ctx context.Context, actor Actor, id uint, change governedChange) (*models.User, error) {
	var result *models.User
	err := s.governance.run(ctx, func(tx *governedTx) error {
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		users, err := tx.lockUsers(actor.UserID, id)
		if err != nil {
			return err
		}
		actorUser, target := users[actor.UserID], users[id]
		if target == nil {
			return fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
		}
		var actorRoleID *uint
		if actorUser != nil {
			actorRoleID = actorUser.RoleID
		}
		roles, err := tx.lockRoles(actorRoleID, target.RoleID, change.roleID)
		if err != nil {
			return err
		}
		var custom *models.Role
		if change.role != nil {
			if custom, err = requestedRole(*change.role, change.roleID, roles); err != nil {
				return err
			}
		}
		acting, err := tx.actor(actorUser, roles)
		if err != nil {
			return err
		}
		keys, err := tx.keysFor(target, change)
		if err != nil {
			return err
		}
		if err := acting.requireKeys(keys...); err != nil {
			return err
		}

		// The escalation rule, twice: the target as it is now, and the role
		// it is to receive. On the actor's own account the first is its own
		// set and never exceeds; the second is checked all the same.
		var targetRole *models.Role
		if target.RoleID != nil {
			targetRole = roles[*target.RoleID]
		}
		if err := checkEscalation(acting, escalationSet(target.Role, targetRole)); err != nil {
			return err
		}
		if change.role != nil {
			if err := checkEscalation(acting, escalationSet(*change.role, custom)); err != nil {
				return err
			}
			if *change.role == models.RoleCustom && target.Role == models.RoleCustomer {
				return fmt.Errorf("user %d: %w", id, apperrors.ErrCustomerLoginRole)
			}
		}

		// The last-admin invariant: the write must not empty the active
		// admin set, which this transaction holds locked.
		leavesAdmins := change.erase ||
			(change.role != nil && *change.role != models.RoleAdmin) ||
			(change.isActive != nil && !*change.isActive)
		if leavesAdmins && isLastActiveAdmin(admins, target.ID) {
			return fmt.Errorf("user %d: %w", id, apperrors.ErrLastAdmin)
		}

		if change.erase {
			// The erasure stays one savepoint-bracketed unit of this
			// transaction.
			return tx.users.Delete(id)
		}

		fields := map[string]interface{}{}
		if change.email != nil && *change.email != target.Email {
			if existing, _ := tx.users.GetByEmailUnscoped(*change.email); existing != nil {
				return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
			}
			fields["email"] = *change.email
		}
		if change.firstName != nil {
			fields["first_name"] = *change.firstName
		}
		if change.lastName != nil {
			fields["last_name"] = *change.lastName
		}
		roleChanged := false
		if change.role != nil {
			roleChanged = *change.role != target.Role || !sameRoleID(change.roleID, target.RoleID)
			fields["role"] = *change.role
			if change.roleID != nil {
				fields["role_id"] = *change.roleID
			} else {
				fields["role_id"] = nil
			}
		}
		if change.isActive != nil {
			fields["is_active"] = *change.isActive
		}
		if err := tx.users.UpdateFields(id, fields); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		if roleChanged {
			if err := tx.record(actor, models.RoleAuditUserRoleChanged, change.roleID, assignedRoleName(*change.role, custom), &id,
				userRoleAuditState{Role: target.Role, RoleID: target.RoleID},
				userRoleAuditState{Role: *change.role, RoleID: change.roleID}); err != nil {
				return err
			}
		}
		if result, err = tx.users.GetByID(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// keysFor is the keys a governed write to target needs: the keys of the change
// and, for a customer login with a linked customer record, the customer key of
// what the write reaches. Erasing such a login erases its customer records
// with it, so it needs customers.delete; changing its email needs
// customers.update. The link is read from the stored role and email, with a
// plain read inside the transaction.
func (tx *governedTx) keysFor(target *models.User, change governedChange) ([]string, error) {
	keys := append([]string{}, change.keys...)
	if target.Role != models.RoleCustomer {
		return keys, nil
	}
	changesEmail := !change.erase && change.email != nil && *change.email != target.Email
	if !change.erase && !changesEmail {
		return keys, nil
	}
	linked, err := tx.userRoles.HasLinkedCustomerRecord(target.ID)
	if err != nil {
		return nil, err
	}
	if !linked {
		return keys, nil
	}
	if change.erase {
		return append(keys, permissions.CustomersDelete), nil
	}
	return append(keys, permissions.CustomersUpdate), nil
}

// oidcSignIn is what one OpenID Connect sign-in (#435) asks the governed
// write for: the verified (issuer, subject) pair, the role the first matching
// mapping entry names, the profile of a first sign-in, and the two halves the
// user service does not own (the identity table and the session issuing).
type oidcSignIn struct {
	issuer, subject string
	roleID          uint
	// email, firstName and lastName are used by a first sign-in only.
	email, firstName, lastName string
	// passwordHash is the bcrypt hash of discarded random bytes, so the
	// provisioned account has no usable password.
	passwordHash string
	identities   repository.UserIdentityRepository
	// issue issues the session on the transaction handle from the row as the
	// transaction wrote it.
	issue func(handle *gorm.DB, user *models.User) (*AuthTokens, error)
	// beforeInsert is a test seam that runs on a first sign-in after the
	// identity lookup and the email check and before the users insert; nil
	// in production.
	beforeInsert func()
	// scimSubject is the Entra ID oid of the token, set by the callback only
	// when the issuer is Entra ID and the token's tid equals the configured
	// issuer's tenant segment; empty otherwise. An unknown (iss, sub) whose
	// (iss, scimSubject) is an identity SCIM created signs in as that account
	// and links (iss, sub) to it.
	scimSubject string
	// beforeSCIMLink is a test seam that runs when the SCIM identity matched,
	// after the plain reads and before the users lock; nil in production.
	beforeSCIMLink func()
}

// ApplyOIDCSignIn is the one transaction of an OIDC sign-in. It takes the
// governed lock order and continues it:
//
//	admin set → users → roles → user_identities → refresh_tokens
//
// The identity is read first with a plain read (it names the users row to
// lock). A known identity re-applies the mapped role with the rules of
// governedWrite minus the actor-key and escalation checks (the mapping is the
// administrator's grant): ErrCustomerLoginRole and ErrLastAdmin refuse, and a
// changed role commits its user_role_changed audit row with auth_method oidc.
// An unknown identity whose Entra ID oid (scimSubject) is the subject of a
// source=scim identity under the same issuer takes the known-identity steps
// for that account and then inserts its (iss, sub) link; that second plain
// read also names the users row to lock. Any other unknown identity
// provisions an active user with the mapped role, its identity row and its
// audit row, refused when any user, erased ones included, holds the address
// in any letter case. Email never links an account. The session is issued last,
// inside the same transaction. Every refusal is ErrSSOSignInFailed (through
// oidcRefuse) and writes nothing; a refusal of a linked account names its
// user for the audit row (#454).
func (s *userService) ApplyOIDCSignIn(ctx context.Context, in oidcSignIn) (*AuthTokens, error) {
	if s.governance == nil {
		return nil, errRoleGovernanceUnavailable
	}
	var tokens *AuthTokens
	err := s.governance.run(ctx, func(tx *governedTx) error {
		admins, err := tx.userRoles.LockActiveAdmins()
		if err != nil {
			return err
		}
		identities := in.identities.WithTx(tx.handle)
		identity, err := identities.GetByIssuerSubject(in.issuer, in.subject)
		if err != nil && !apperrors.IsNotFound(err) {
			return err
		}
		if identity != nil && (identity.Issuer != in.issuer || identity.Subject != in.subject) {
			// The PAD SPACE collation of MySQL/MariaDB matched a value that
			// differs in trailing spaces: never the same identity.
			return oidcRefuse("identity_mismatch")
		}

		// signInExisting signs in as the linked account userID: it locks the
		// users row, refuses an erased or deactivated account, re-applies the
		// mapped role with its audit row and reads the row back. A known
		// identity and a SCIM-created identity found by its oid share it.
		signInExisting := func(userID uint) (*models.User, error) {
			users, err := tx.lockUsers(userID)
			if err != nil {
				return nil, err
			}
			target := users[userID]
			if target == nil {
				return nil, oidcRefusalForUser(oidcRefuse("account_erased"), userID)
			}
			if !target.IsActive {
				return nil, oidcRefusalForUser(oidcRefuse("account_inactive"), target.ID)
			}
			mapped := in.roleID
			roles, err := tx.lockRoles(target.RoleID, &mapped)
			if err != nil {
				return nil, err
			}
			role, roleID, custom, err := oidcMappedRole(roles[mapped])
			if err != nil {
				return nil, err
			}
			if role == models.RoleCustom && target.Role == models.RoleCustomer {
				return nil, oidcRefusalForUser(oidcRefuseWith("customer_login_role", apperrors.ErrCustomerLoginRole), target.ID)
			}
			if role != models.RoleAdmin && isLastActiveAdmin(admins, target.ID) {
				return nil, oidcRefusalForUser(oidcRefuseWith("last_admin", apperrors.ErrLastAdmin), target.ID)
			}
			if role != target.Role || !sameRoleID(roleID, target.RoleID) {
				fields := map[string]interface{}{"role": role, "role_id": nil}
				if roleID != nil {
					fields["role_id"] = *roleID
				}
				if err := tx.users.UpdateFields(target.ID, fields); err != nil {
					if isNotFound(err) {
						return nil, oidcRefusalForUser(oidcRefuse("account_erased"), userID)
					}
					return nil, err
				}
				if err := tx.record(Actor{UserID: target.ID, AuthMethod: AuthMethodOIDC}, models.RoleAuditUserRoleChanged,
					roleID, assignedRoleName(role, custom), &target.ID,
					userRoleAuditState{Role: target.Role, RoleID: target.RoleID},
					userRoleAuditState{Role: role, RoleID: roleID}); err != nil {
					return nil, err
				}
			}
			user, err := tx.users.GetByID(target.ID)
			if err != nil {
				if isNotFound(err) {
					return nil, oidcRefusalForUser(oidcRefuse("account_erased"), userID)
				}
				return nil, err
			}
			return user, nil
		}

		// scimLink is the identity SCIM created for this person under the
		// same issuer, found by the Entra ID oid the callback vetted. Only a
		// byte-exact source=scim row counts; anything else falls through to
		// provisioning below.
		var scimLink *models.UserIdentity
		if identity == nil && in.scimSubject != "" {
			row, err := identities.GetByIssuerSubject(in.issuer, in.scimSubject)
			if err != nil && !apperrors.IsNotFound(err) {
				return err
			}
			if row != nil && row.Issuer == in.issuer && row.Subject == in.scimSubject &&
				row.Source == models.UserIdentitySourceSCIM {
				scimLink = row
			}
		}

		var user *models.User
		if identity != nil {
			if user, err = signInExisting(identity.UserID); err != nil {
				return err
			}
		} else if scimLink != nil {
			if in.beforeSCIMLink != nil {
				in.beforeSCIMLink()
			}
			if user, err = signInExisting(scimLink.UserID); err != nil {
				return err
			}
			if err := identities.Create(&models.UserIdentity{UserID: user.ID, Issuer: in.issuer, Subject: in.subject,
				Source: models.UserIdentitySourceOIDC}); err != nil {
				if errors.Is(err, repository.ErrUserIdentityTaken) {
					return oidcRefusalForUser(oidcRefuseWith("identity_taken", err), user.ID)
				}
				return err
			}
		} else {
			if in.email == "" || len(in.email) > oidcEmailMaxBytes {
				return oidcRefuse("email_missing")
			}
			mapped := in.roleID
			roles, err := tx.lockRoles(&mapped)
			if err != nil {
				return err
			}
			role, roleID, custom, err := oidcMappedRole(roles[mapped])
			if err != nil {
				return err
			}
			taken, err := tx.users.EmailExistsFoldedUnscoped(in.email)
			if err != nil {
				return err
			}
			if taken {
				// Never linked to the existing account, live or erased.
				return oidcRefuse("email_taken")
			}
			if in.beforeInsert != nil {
				in.beforeInsert()
			}
			// A replayed attempt inserts a fresh row.
			created := &models.User{
				Email:     in.email,
				Password:  in.passwordHash,
				FirstName: in.firstName,
				LastName:  in.lastName,
				Role:      role,
				RoleID:    roleID,
				IsActive:  true,
			}
			if err := tx.users.Create(created); err != nil {
				if errors.Is(err, apperrors.ErrDuplicateEmail) {
					return oidcRefuseWith("email_taken", err)
				}
				return err
			}
			if err := identities.Create(&models.UserIdentity{UserID: created.ID, Issuer: in.issuer, Subject: in.subject,
				Source: models.UserIdentitySourceOIDC}); err != nil {
				if errors.Is(err, repository.ErrUserIdentityTaken) {
					return oidcRefuseWith("identity_taken", err)
				}
				return err
			}
			if err := tx.record(Actor{UserID: created.ID, AuthMethod: AuthMethodOIDC}, models.RoleAuditUserRoleChanged,
				roleID, assignedRoleName(role, custom), &created.ID,
				nil, userRoleAuditState{Role: role, RoleID: roleID}); err != nil {
				return err
			}
			if user, err = tx.users.GetByID(created.ID); err != nil {
				return err
			}
		}

		issued, err := in.issue(tx.handle, user)
		if err != nil {
			return err
		}
		tokens = issued
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tokens, nil
}

// oidcMappedRole resolves the locked role row a mapping entry names: a system
// role is assigned by its key with role_id NULL, a custom role as custom with
// its id. A missing row refuses the sign-in.
func oidcMappedRole(row *models.Role) (models.UserRole, *uint, *models.Role, error) {
	if row == nil {
		return "", nil, nil, oidcRefuse("role_missing")
	}
	if row.SystemKey != nil {
		role := models.UserRole(*row.SystemKey)
		if !role.IsValid() || role == models.RoleCustom {
			return "", nil, nil, oidcRefuse("role_missing")
		}
		return role, nil, nil, nil
	}
	id := row.ID
	return models.RoleCustom, &id, row, nil
}
