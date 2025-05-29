package models

type LeadStatus string

const (
	LeadStatusNew         LeadStatus = "new"
	LeadStatusContacted   LeadStatus = "contacted"
	LeadStatusQualified   LeadStatus = "qualified"
	LeadStatusUnqualified LeadStatus = "unqualified"
	LeadStatusConverted   LeadStatus = "converted"
)

type Lead struct {
	BaseModel
	FirstName   string     `gorm:"not null;type:varchar(100)" json:"first_name"`
	LastName    string     `gorm:"not null;type:varchar(100)" json:"last_name"`
	Email       string     `gorm:"not null;type:varchar(255)" json:"email"`
	Phone       string     `gorm:"type:varchar(50)" json:"phone"`
	Company     string     `gorm:"type:varchar(200)" json:"company"`
	Position    string     `gorm:"type:varchar(100)" json:"position"`
	Source      string     `gorm:"type:varchar(100)" json:"source"`
	Status      LeadStatus `gorm:"not null;default:'new';type:varchar(20)" json:"status"`
	Notes       string     `gorm:"type:text" json:"notes"`
	OwnerID     uint       `json:"owner_id"`
	Owner       User       `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	CustomerID  *uint      `json:"customer_id,omitempty"`
	Customer    *Customer  `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
}