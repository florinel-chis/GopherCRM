package service

// Shared fixture of the ticket automation roots (#242). It extends the
// automation fixture (real engine, services and repositories over a real
// SQLite file or a disposable MySQL schema) with support users, a customer
// carrying canary values and the ticket service wired with
// WithTicketAutomationEvents, exactly as cmd/main.go wires it.

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Canary values of the ticket fixture: none of them may reach an event, a
// log line or a rendered notification.
const (
	ticketCanaryTitle       = "TICKET-TITLE-CANARY printer on fire"
	ticketCanaryDescription = "TICKET-DESC-CANARY Agnieszka called twice"
	ticketCanaryResolution  = "TICKET-RESOLUTION-CANARY replaced the fuser"
	ticketCanaryCustomer    = "Agnieszka-Canary"
)

type ticketAutomationEnv struct {
	*automationEnv
	support  *models.User
	support2 *models.User
	customer *models.Customer
	tickets  repository.TicketRepository

	// ticketSvc is the ticket service wired with WithTicketAutomationEvents;
	// svc (promoted from automationEnv) stays the automation service.
	ticketSvc TicketService
}

func newTicketAutomationEnv(t *testing.T, db *gorm.DB) *ticketAutomationEnv {
	t.Helper()
	env := newAutomationEnv(t, db)
	te := &ticketAutomationEnv{automationEnv: env, tickets: repository.NewTicketRepository(db)}
	te.support = env.createUser("support", models.RoleSupport, true)
	te.support2 = env.createUser("support2", models.RoleSupport, true)
	seq := automationTestSeq.Add(1)
	te.customer = &models.Customer{FirstName: ticketCanaryCustomer, LastName: "Kowalska-Canary",
		Email: fmt.Sprintf("agnieszka-canary-%d@example.com", seq)}
	require.NoError(t, db.Create(te.customer).Error)
	te.ticketSvc = NewTicketService(te.tickets, env.customers, env.users, WithTicketAutomationEvents(env.repo, env.txManager))
	return te
}

// createTicket inserts a ticket directly, so no event is recorded for the
// fixture itself.
func (e *ticketAutomationEnv) createTicket(assignee *models.User, status models.TicketStatus) *models.Ticket {
	e.t.Helper()
	ticket := &models.Ticket{Title: ticketCanaryTitle, Description: ticketCanaryDescription, Resolution: ticketCanaryResolution,
		Status: status, Priority: models.TicketPriorityHigh, CustomerID: e.customer.ID}
	if assignee != nil {
		id := assignee.ID
		ticket.AssignedToID = &id
	}
	require.NoError(e.t, e.db.Create(ticket).Error)
	return ticket
}

// bareTicket reads the stored ticket row without associations.
func (e *ticketAutomationEnv) bareTicket(id uint) *models.Ticket {
	e.t.Helper()
	var ticket models.Ticket
	require.NoError(e.t, e.db.Unscoped().First(&ticket, id).Error)
	return &ticket
}

func (e *ticketAutomationEnv) ticketEvents(id uint) []models.AutomationEvent {
	e.t.Helper()
	return e.eventsOf(models.AutomationEntityTicket, id)
}

func (e *ticketAutomationEnv) ticketRule(triggerType string, trigger models.AutomationTrigger, steps ...models.AutomationStep) *models.AutomationRule {
	e.t.Helper()
	return e.createRule(models.AutomationEntityTicket, triggerType, trigger, steps...)
}

func ticketNotifyStep(recipient string, userID uint) models.AutomationStep {
	cfg := models.AutomationStepConfig{Recipient: recipient, UserID: userID}
	cfg.Title = "TICKET-NOTIFY-CANARY {{ticket.number}} is {{ticket.status}}"
	cfg.Body = "TICKET-NOTIFY-BODY-CANARY priority {{ticket.priority}} for {{owner.first_name}}"
	return models.AutomationStep{StepType: models.AutomationStepNotify, Config: cfg}
}

func ticketTaskStep(recipient string, userID uint, dueInDays int) models.AutomationStep {
	cfg := models.AutomationStepConfig{Recipient: recipient, UserID: userID, DueInDays: dueInDays}
	cfg.Title = "Check ticket {{ticket.number}}"
	cfg.Description = "Ticket {{ticket.id}} is {{ticket.status}} ({{ticket.priority}})"
	return models.AutomationStep{StepType: models.AutomationStepCreateTask, Config: cfg}
}

func ticketEmailStep(recipient string, userID uint) models.AutomationStep {
	return models.AutomationStep{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
		Recipient: recipient, UserID: userID, Subject: "TICKET-EMAIL-CANARY {{ticket.number}}", Body: "TICKET-EMAIL-BODY-CANARY {{ticket.status}}",
	}}
}

// statementLog records the SQL of every query and update the database runs,
// in order, while it is attached.
type statementLog struct {
	mu   sync.Mutex
	sqls []string
}

func captureStatements(t *testing.T, db *gorm.DB) *statementLog {
	t.Helper()
	log := &statementLog{}
	record := func(tx *gorm.DB) {
		log.mu.Lock()
		defer log.mu.Unlock()
		log.sqls = append(log.sqls, tx.Statement.SQL.String())
	}
	name := fmt.Sprintf("ticket_automation_capture_%d", automationTestSeq.Add(1))
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name+":query", record))
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(name+":update", record))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name+":create", record))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name + ":query")
		_ = db.Callback().Update().Remove(name + ":update")
		_ = db.Callback().Create().Remove(name + ":create")
	})
	return log
}

func (l *statementLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sqls = nil
}

// index returns the position of the first statement matching pattern, or -1.
func (l *statementLog) index(pattern *regexp.Regexp) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, sql := range l.sqls {
		if pattern.MatchString(sql) {
			return i
		}
	}
	return -1
}

func (l *statementLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.sqls, "\n")
}

var (
	// ticketLockingRead matches the locking read of a ticket on MySQL.
	ticketLockingRead = regexp.MustCompile("(?is)^SELECT .*FROM `tickets`.*FOR UPDATE")
	// ticketReadSQL matches any read of the tickets table.
	ticketReadSQL = regexp.MustCompile("(?is)^SELECT .*FROM [`\"]?tickets[`\"]?")
	// ticketWriteSQL matches an update of the tickets table.
	ticketWriteSQL = regexp.MustCompile("(?is)^UPDATE [`\"]?tickets[`\"]?")
	// ticketEventInsert matches the insert of an automation event.
	ticketEventInsert = regexp.MustCompile("(?is)^INSERT INTO [`\"]?automation_events[`\"]?")
)
