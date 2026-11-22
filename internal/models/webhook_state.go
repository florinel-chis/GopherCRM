package models

import "time"

// The WebhookState types are the rule of the *.updated webhook events (#408):
// an update path compares the record's state before and after its write and
// emits the event only when the two differ. Each state holds exactly the
// columns the model's update paths write, minus id, created_at, updated_at and
// deleted_at, under the Go name of the model field it copies.
//
// A column added to a repository update must be added to its state here too:
// TestWebhookStateCoversUpdateColumns (internal/repository) captures the
// columns each update writes and fails when the two drift apart.
//
// The states are compared with ==. Nullable ids are copied by value (0 for
// NULL: auto-increment ids start at 1), never as pointers, and a nullable
// instant is copied as UnixNano plus a set flag, so the same moment in another
// location is equal.

// WebhookTime is a nullable instant in a comparable form.
type WebhookTime struct {
	Set      bool
	UnixNano int64
}

func webhookTime(t *time.Time) WebhookTime {
	if t == nil {
		return WebhookTime{}
	}
	return WebhookTime{Set: true, UnixNano: t.UnixNano()}
}

func webhookID(id *uint) uint {
	if id == nil {
		return 0
	}
	return *id
}

// LeadWebhookState is the state lead.updated compares: the columns
// leadRepository.Update (Save) writes, which include those of
// leadRepository.ConvertToCustomer and the automation SetLeadOwner.
type LeadWebhookState struct {
	FirstName      string
	LastName       string
	Email          string
	Phone          string
	Company        string
	Position       string
	Source         string
	Status         LeadStatus
	Classification LeadClassification
	ExternalID     string
	Notes          string
	OwnerID        uint
	CustomerID     uint
	CompanyID      uint
}

// WebhookState returns the lead's state for the lead.updated event.
func (l *Lead) WebhookState() LeadWebhookState {
	return LeadWebhookState{
		FirstName:      l.FirstName,
		LastName:       l.LastName,
		Email:          l.Email,
		Phone:          l.Phone,
		Company:        l.Company,
		Position:       l.Position,
		Source:         l.Source,
		Status:         l.Status,
		Classification: l.Classification,
		ExternalID:     l.ExternalID,
		Notes:          l.Notes,
		OwnerID:        l.OwnerID,
		CustomerID:     webhookID(l.CustomerID),
		CompanyID:      webhookID(l.CompanyID),
	}
}

// CustomerWebhookState is the state customer.updated compares: the fifteen
// columns customerRepository.Update writes.
type CustomerWebhookState struct {
	FirstName    string
	LastName     string
	Email        string
	Phone        string
	Company      string
	Website      string
	Position     string
	Address      string
	City         string
	State        string
	Country      string
	PostalCode   string
	Notes        string
	CompanyID    uint
	AssignedToID uint
}

// WebhookState returns the customer's state for the customer.updated event.
func (c *Customer) WebhookState() CustomerWebhookState {
	return CustomerWebhookState{
		FirstName:    c.FirstName,
		LastName:     c.LastName,
		Email:        c.Email,
		Phone:        c.Phone,
		Company:      c.Company,
		Website:      c.Website,
		Position:     c.Position,
		Address:      c.Address,
		City:         c.City,
		State:        c.State,
		Country:      c.Country,
		PostalCode:   c.PostalCode,
		Notes:        c.Notes,
		CompanyID:    webhookID(c.CompanyID),
		AssignedToID: webhookID(c.AssignedToID),
	}
}

// TicketWebhookState is the state ticket.updated compares: the columns
// ticketRepository.Update (Save, first_responded_at omitted) writes, which
// include the one the automation SetTicketAssignee writes.
type TicketWebhookState struct {
	Title        string
	Description  string
	Status       TicketStatus
	Priority     TicketPriority
	CustomerID   uint
	AssignedToID uint
	Resolution   string
	DueAt        WebhookTime
}

// WebhookState returns the ticket's state for the ticket.updated event.
func (t *Ticket) WebhookState() TicketWebhookState {
	return TicketWebhookState{
		Title:        t.Title,
		Description:  t.Description,
		Status:       t.Status,
		Priority:     t.Priority,
		CustomerID:   t.CustomerID,
		AssignedToID: webhookID(t.AssignedToID),
		Resolution:   t.Resolution,
		DueAt:        webhookTime(t.DueAt),
	}
}

// DealWebhookState is the state deal.updated compares (#515): the columns
// dealRepository.Update (Save) writes, which include the one the automation
// SetDealOwner writes. ExpectedCloseDate and ClosedAt compare as instants.
type DealWebhookState struct {
	Title             string
	Stage             DealStage
	AmountCents       int64
	Currency          string
	Probability       int
	ExpectedCloseDate WebhookTime
	ClosedAt          WebhookTime
	LostReason        string
	Source            string
	Notes             string
	CompanyID         uint
	CustomerID        uint
	LeadID            uint
	OwnerID           uint
}

// WebhookState returns the deal's state for the deal.updated event.
func (d *Deal) WebhookState() DealWebhookState {
	return DealWebhookState{
		Title:             d.Title,
		Stage:             d.Stage,
		AmountCents:       d.AmountCents,
		Currency:          d.Currency,
		Probability:       d.Probability,
		ExpectedCloseDate: webhookTime(d.ExpectedCloseDate),
		ClosedAt:          webhookTime(d.ClosedAt),
		LostReason:        d.LostReason,
		Source:            d.Source,
		Notes:             d.Notes,
		CompanyID:         webhookID(d.CompanyID),
		CustomerID:        webhookID(d.CustomerID),
		LeadID:            webhookID(d.LeadID),
		OwnerID:           d.OwnerID,
	}
}
