package models

import (
	"time"
	
	"golang.org/x/crypto/bcrypt"
)

type UserRole string

const (
	RoleAdmin    UserRole = "admin"
	RoleSales    UserRole = "sales"
	RoleSupport  UserRole = "support"
	RoleCustomer UserRole = "customer"
)

type User struct {
	BaseModel
	Email        string   `gorm:"uniqueIndex;not null;type:varchar(255)" json:"email"`
	Password     string   `gorm:"not null;type:varchar(255)" json:"-"`
	FirstName    string   `gorm:"not null;type:varchar(100)" json:"first_name"`
	LastName     string   `gorm:"not null;type:varchar(100)" json:"last_name"`
	Role         UserRole `gorm:"not null;default:'customer';type:varchar(20)" json:"role"`
	IsActive     bool     `gorm:"default:true" json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	
	Leads     []Lead     `gorm:"foreignKey:OwnerID" json:"-"`
	Tasks     []Task     `gorm:"foreignKey:AssignedToID" json:"-"`
	APIKeys   []APIKey   `gorm:"foreignKey:UserID" json:"-"`
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