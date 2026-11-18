package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

// UserRole is an account's role: one of the four system roles, whose
// permissions are fixed in code, or custom for an account whose permissions
// come from a custom role (users.role_id). Accounts created through public
// registration are always customer.
//
// @Description Role of an account: one of the four system roles (admin, sales, support, customer), whose permissions are fixed, or custom for an account whose permissions come from the custom role its role_id names. Public registration always creates customer accounts.
type UserRole string

const (
	RoleAdmin    UserRole = "admin"
	RoleSales    UserRole = "sales"
	RoleSupport  UserRole = "support"
	RoleCustomer UserRole = "customer"
	// RoleCustom is the role of an account on a custom role. It is a typed
	// constant, so the API reference lists it with the four system roles;
	// POST /users and PUT /users/{id} accept it together with role_id.
	RoleCustom UserRole = "custom"
)

// User is a login account: staff (admin, sales, support) or a customer's own
// login. The password hash and the failed-login counter are never serialised.
//
// @Description A login account: staff (admin, sales, support) or a customer's own login. The password hash and the failed-login counter are never returned.
type User struct {
	BaseModel
	// Login email address, unique among all accounts, including erased ones
	// (erasure replaces the address, which frees it for reuse).
	Email    string `gorm:"uniqueIndex;not null;type:varchar(255)" json:"email" format:"email" example:"jane.doe@example.com"`
	Password string `gorm:"not null;type:varchar(255)" json:"-"`
	// Given name.
	FirstName string `gorm:"not null;type:varchar(100)" json:"first_name" example:"Jane"`
	// Family name.
	LastName string `gorm:"not null;type:varchar(100)" json:"last_name" example:"Doe"`
	// Role of the account: a system role, or custom for an account on a custom
	// role (see role_id). Changed through PUT /users/{id} and set by POST /users,
	// by a caller holding users.assign_role.
	Role UserRole `gorm:"not null;default:'customer';type:varchar(20);index:idx_users_role" json:"role" example:"sales"`
	// Id of the account's custom role. Present exactly when role is custom;
	// omitted for an account on a system role.
	RoleID *uint `gorm:"index:idx_users_role_id" json:"role_id,omitempty" example:"7"`
	// CustomRole is the role RoleID names (fk_users_custom_role, ON DELETE
	// RESTRICT). Filled by UserRepository.GetByID; never serialised.
	CustomRole *Role `gorm:"foreignKey:RoleID;constraint:OnDelete:RESTRICT" json:"-"`
	// False for a deactivated account: it cannot log in, and its bearer tokens and
	// API keys are rejected. Changed by a caller holding users.activate.
	IsActive bool `gorm:"default:true" json:"is_active" example:"true"`
	// Time of the last successful password login (RFC 3339). Absent when the
	// account has never logged in. Server-maintained, read-only.
	LastLoginAt         *time.Time `json:"last_login_at,omitempty" example:"2026-10-30T08:15:00Z" readonly:"true"`
	FailedLoginAttempts int        `gorm:"default:0" json:"-"`
	// End of the login lockout set after 5 consecutive failed logins (15 minutes,
	// RFC 3339). Absent when no lock is recorded; it may lie in the past, since a
	// lapsed lock is cleared only by the next login attempt. Server-maintained,
	// read-only.
	LockedUntil *time.Time `json:"locked_until,omitempty" example:"2026-10-30T08:30:00Z" readonly:"true"`
	// TokenVersion is the session generation of the account (#236). Every access
	// JWT carries it as the token_version claim, and a token whose claim differs
	// from the stored value is refused. Logout, a password change and a password
	// reset increment it, which ends every access token issued before them.
	// Server-maintained and never serialised.
	TokenVersion uint `gorm:"not null;default:0" json:"-"`

	Leads   []Lead   `gorm:"foreignKey:OwnerID" json:"-"`
	Tasks   []Task   `gorm:"foreignKey:AssignedToID" json:"-"`
	APIKeys []APIKey `gorm:"foreignKey:UserID" json:"-"`
}

func (u *User) FullName() string {
	return u.FirstName + " " + u.LastName
}

// SetPassword hashes and sets the user's password
func (u *User) SetPassword(password string) error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.Password = string(hashedPassword)
	return nil
}
