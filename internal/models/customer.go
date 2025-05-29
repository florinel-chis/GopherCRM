package models

type Customer struct {
	BaseModel
	FirstName    string   `gorm:"not null;type:varchar(100)" json:"first_name"`
	LastName     string   `gorm:"not null;type:varchar(100)" json:"last_name"`
	Email        string   `gorm:"uniqueIndex;not null;type:varchar(255)" json:"email"`
	Phone        string   `gorm:"type:varchar(50)" json:"phone"`
	Company      string   `gorm:"type:varchar(200)" json:"company"`
	Position     string   `gorm:"type:varchar(100)" json:"position"`
	Address      string   `gorm:"type:varchar(255)" json:"address"`
	City         string   `gorm:"type:varchar(100)" json:"city"`
	State        string   `gorm:"type:varchar(100)" json:"state"`
	Country      string   `gorm:"type:varchar(100)" json:"country"`
	PostalCode   string   `gorm:"type:varchar(20)" json:"postal_code"`
	Notes        string   `gorm:"type:text" json:"notes"`
	UserID       *uint    `json:"user_id,omitempty"`
	User         *User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	
	Tickets      []Ticket `gorm:"foreignKey:CustomerID" json:"tickets,omitempty"`
}