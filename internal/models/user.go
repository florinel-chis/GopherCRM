package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

// UserRole is an account's role. It decides which operations and records the
// account may reach; every operation lists the roles it admits. Accounts
// created through public registration are always customer.
//
// @Description Role of an account. It decides which operations and records the account may reach; every operation lists the roles it admits. Public registration always creates customer accounts.
type UserRole string

const (
	RoleAdmin    UserRole = "admin"
	RoleSales    UserRole = "sales"
	RoleSupport  UserRole = "support"
	RoleCustomer UserRole = "customer"
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
	// Role of the account; only an admin can change it (PUT /users/{id}, POST /users).
	Role UserRole `gorm:"not null;default:'customer';type:varchar(20)" json:"role" example:"sales"`
	// False for a deactivated account: it cannot log in, and its bearer tokens and
	// API keys are rejected. Changed only by an admin.
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
