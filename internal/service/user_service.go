package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// isNotFound reports whether a repository error means "there is no such row".
// Repositories hand gorm's own sentinel straight back to us, so the helper
// must cover it alongside the apperrors sentinels; apperrors.IsNotFound does.
func isNotFound(err error) bool {
	return apperrors.IsNotFound(err)
}

type userService struct {
	userRepo repository.UserRepository
	// governance is set by WithUserRoleGovernance; nil leaves the actor-taking
	// writes as plain as Register, Update and Delete.
	governance *roleGovernance
}

// UserServiceOption configures NewUserService beyond the user repository.
type UserServiceOption func(*userService)

func NewUserService(userRepo repository.UserRepository, opts ...UserServiceOption) UserService {
	s := &userService{userRepo: userRepo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *userService) Register(user *models.User, password string) error {
	// Check if user already exists. The lookup must be unscoped: the unique index
	// on users.email is not scoped to deleted_at, so a soft-deleted row still
	// reserves the address and the insert below would be rejected by the database
	// even though a scoped lookup reports the address as free.
	existing, _ := s.userRepo.GetByEmailUnscoped(user.Email)
	if existing != nil {
		return fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.Password = string(hashedPassword)

	return s.userRepo.Create(user)
}

func (s *userService) GetByID(id uint) (*models.User, error) {
	return s.userRepo.GetByID(id)
}

func (s *userService) GetByEmail(email string) (*models.User, error) {
	return s.userRepo.GetByEmail(email)
}

func (s *userService) Update(id uint, updates map[string]interface{}) (*models.User, error) {
	user, err := s.userRepo.GetByID(id)
	if err != nil {
		// The repository hands gorm's sentinel back unchanged, and that value
		// matches none of ours by identity. Translate it here, as Delete does,
		// so the caller can tell "no such user" from a database failure and
		// answer 404 instead of 500.
		if isNotFound(err) {
			return nil, fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}

	// Only the fields the caller supplied are written, through a conditional
	// update of the live row. A full save of the struct read above would put
	// back every column as it was at that read — an older password hash, or
	// an account erased in the meantime (GORM's Save upserts when its UPDATE
	// matches nothing).
	fields := map[string]interface{}{}

	// Check if email is being updated and if it's already taken
	if newEmail, ok := updates["email"].(string); ok && newEmail != user.Email {
		// Unscoped for the same reason as in Register: a soft-deleted row still
		// holds the address in the database unique index.
		existing, _ := s.userRepo.GetByEmailUnscoped(newEmail)
		if existing != nil {
			return nil, fmt.Errorf("user with this email already exists: %w", apperrors.ErrDuplicateEmail)
		}
		user.Email = newEmail
		fields["email"] = newEmail
	}

	// Apply other updates
	if firstName, ok := updates["first_name"].(string); ok {
		user.FirstName = firstName
		fields["first_name"] = firstName
	}
	if lastName, ok := updates["last_name"].(string); ok {
		user.LastName = lastName
		fields["last_name"] = lastName
	}
	if role, ok := updates["role"].(models.UserRole); ok {
		user.Role = role
		fields["role"] = role
	}
	if isActive, ok := updates["is_active"].(bool); ok {
		user.IsActive = isActive
		fields["is_active"] = isActive
	}

	// A "password" key is deliberately ignored: a password changes only through
	// AuthService.ChangePassword or the reset, which revoke the refresh tokens.
	if err := s.userRepo.UpdateFields(id, fields); err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}

	return user, nil
}

// CheckOwnEmailChange decides whether an account may change its own login
// email with the password it supplied. The login email is where a password
// reset is sent, so a stolen session or API key must not be able to move it:
// the change needs the account's current password, as a password change does.
//
// The comparison with the stored email is the one Update makes, so a body
// that repeats the stored email (the user form sends it on every save) needs
// no password, and then the supplied value is not looked at. A wrong password
// is not counted toward the login lockout, as on a password change.
func (s *userService) CheckOwnEmailChange(id uint, email, currentPassword string) error {
	if email == "" {
		return nil
	}
	user, err := s.userRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
		}
		return err
	}
	if email == user.Email {
		return nil
	}
	if currentPassword == "" {
		return apperrors.ErrCurrentPasswordRequired
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(currentPassword)); err != nil {
		return apperrors.ErrCurrentPasswordIncorrect
	}
	return nil
}

// Delete erases the user's personal data and then soft-deletes the row. It is a
// GDPR Article 17 erasure and it is IRREVERSIBLE: the email is replaced with a
// non-routable placeholder, the names are blanked, the password hash is made
// unusable, and the user's API keys and refresh tokens are destroyed so no
// credential outlives the account. See userRepository.Delete for the details.
//
// This is NOT how you suspend someone. The non-destructive path is deactivation
// — Update(id, map[string]interface{}{"is_active": false}) — which keeps every
// field intact and can be undone. Reach for Delete only when the data itself
// must go.
//
// The existence check is not a nicety. Both statements the repository runs
// match by primary key, and matching zero rows is not an error in SQL, so
// without it an erasure of a user that does not exist — a typo in an ID, a
// replayed request, an already-erased account — would return success. An
// operator would then record a completed Article 17 erasure that never
// happened, which is precisely the kind of thing a supervisory authority asks
// to see evidence of. Not found must say so, like the customer path does.
func (s *userService) Delete(id uint) error {
	if _, err := s.userRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("user %d not found: %w", id, apperrors.ErrNotFound)
		}
		// A transient database failure is not a "no such user"; report it as it
		// is rather than telling the caller the erasure had nothing to do.
		return err
	}

	return s.userRepo.Delete(id)
}

func (s *userService) List(offset, limit int) ([]models.User, int64, error) {
	users, err := s.userRepo.List(offset, limit)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.userRepo.Count()
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (s *userService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error) {
	users, err := s.userRepo.ListSorted(offset, limit, sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.userRepo.Count()
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (s *userService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error) {
	users, err := s.userRepo.Search(query, offset, limit, sortBy, sortOrder)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.userRepo.CountSearch(query)
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}
