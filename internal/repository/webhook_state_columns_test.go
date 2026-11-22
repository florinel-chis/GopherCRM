package repository

// The webhook state of a model (models.Lead.WebhookState and its siblings) is
// the rule of the *.updated event (#408): it must hold exactly the columns the
// model's update writes. This root captures the columns of every UPDATE the
// repository writers run on the model's table and compares them with the
// state's fields, mapped to columns through the GORM schema, so a column added
// to an update without being added to the state fails here.

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// webhookStateIgnoredColumns are written by an update but are not part of any
// state: the key and the bookkeeping timestamps.
var webhookStateIgnoredColumns = map[string]bool{"id": true, "created_at": true, "updated_at": true, "deleted_at": true}

// updateColumnLog records the SET columns of every UPDATE statement run on
// one table while it is attached.
type updateColumnLog struct {
	mu      sync.Mutex
	table   string
	columns map[string]bool
}

var updateSetColumn = regexp.MustCompile("[`\"](\\w+)[`\"]\\s*=")

func captureUpdateColumns(t *testing.T, db *gorm.DB, table string) *updateColumnLog {
	t.Helper()
	log := &updateColumnLog{table: table, columns: map[string]bool{}}
	name := "webhook_state_columns_" + table + "_" + nextTicketDeleteKey()
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != log.table {
			return
		}
		sql := tx.Statement.SQL.String()
		set := strings.Index(sql, " SET ")
		if set < 0 {
			return
		}
		sql = sql[set+len(" SET "):]
		if where := strings.Index(sql, " WHERE "); where >= 0 {
			sql = sql[:where]
		}
		log.mu.Lock()
		defer log.mu.Unlock()
		for _, m := range updateSetColumn.FindAllStringSubmatch(sql, -1) {
			log.columns[m[1]] = true
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
	return log
}

// take returns the columns recorded so far, sorted, and starts a new record.
func (l *updateColumnLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.columns))
	for c := range l.columns {
		out = append(out, c)
	}
	sort.Strings(out)
	l.columns = map[string]bool{}
	return out
}

// webhookStateColumns maps the fields of state (a WebhookState struct) to the
// columns of model through the GORM schema; each state field has the Go name
// of the model field it copies.
func webhookStateColumns(t *testing.T, db *gorm.DB, model, state interface{}) []string {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, db.NamingStrategy)
	require.NoError(t, err)
	stateType := reflect.TypeOf(state)
	columns := make([]string, 0, stateType.NumField())
	for i := 0; i < stateType.NumField(); i++ {
		goName := stateType.Field(i).Name
		field := s.LookUpField(goName)
		require.NotNil(t, field, "state field %s names no field of %s", goName, s.Name)
		require.NotEmpty(t, field.DBName, "state field %s is not a column of %s", goName, s.Name)
		columns = append(columns, field.DBName)
	}
	sort.Strings(columns)
	return columns
}

// webhookStateDrift compares the columns a writer wrote (the bookkeeping
// columns dropped) with the state columns. missingFromState lists written
// columns the state does not hold; unwritten lists state columns the writer
// did not write, and is reported only when exact (an Update method, which must
// write the whole state).
func webhookStateDrift(written, state []string, exact bool) (missingFromState, unwritten []string) {
	inState := map[string]bool{}
	for _, c := range state {
		inState[c] = true
	}
	wrote := map[string]bool{}
	for _, c := range written {
		if webhookStateIgnoredColumns[c] {
			continue
		}
		wrote[c] = true
		if !inState[c] {
			missingFromState = append(missingFromState, c)
		}
	}
	if exact {
		for _, c := range state {
			if !wrote[c] {
				unwritten = append(unwritten, c)
			}
		}
	}
	return missingFromState, unwritten
}

func assertNoWebhookStateDrift(t *testing.T, writer string, written, state []string, exact bool) {
	t.Helper()
	require.NotEmpty(t, written, "%s wrote no UPDATE on the table", writer)
	missing, unwritten := webhookStateDrift(written, state, exact)
	assert.Empty(t, missing, "%s writes columns its WebhookState does not compare: add them to the state", writer)
	assert.Empty(t, unwritten, "%s does not write these WebhookState columns: remove them from the state", writer)
}

func TestWebhookStateCoversUpdateColumns(t *testing.T) {
	db := migrateIdentityErasureDB(t, testsupport.Open(t, testsupport.SQLite, "webhook_state_columns"))
	now := time.Now().UTC()

	owner := &models.User{Email: "state-owner@example.com", Password: "x", FirstName: "Owner", LastName: "One", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	other := &models.User{Email: "state-other@example.com", Password: "x", FirstName: "Other", LastName: "Two", Role: models.RoleSupport, IsActive: true}
	require.NoError(t, db.Create(other).Error)
	company := &models.Company{Name: "State Co", Domain: "state.example"}
	require.NoError(t, db.Create(company).Error)
	customer := &models.Customer{FirstName: "Cus", LastName: "Tomer", Email: "state-customer@example.com", CompanyID: &company.ID}
	require.NoError(t, db.Create(customer).Error)
	lead := &models.Lead{FirstName: "Le", LastName: "Ad", Email: "state-lead@example.com", OwnerID: owner.ID, CompanyID: &company.ID}
	require.NoError(t, db.Create(lead).Error)
	due := now.Add(48 * time.Hour)
	ticket := &models.Ticket{Title: "State", Description: "Ticket", CustomerID: customer.ID, AssignedToID: &owner.ID, DueAt: &due}
	require.NoError(t, db.Create(ticket).Error)

	leadState := webhookStateColumns(t, db, &models.Lead{}, models.LeadWebhookState{})
	customerState := webhookStateColumns(t, db, &models.Customer{}, models.CustomerWebhookState{})
	ticketState := webhookStateColumns(t, db, &models.Ticket{}, models.TicketWebhookState{})

	leads := captureUpdateColumns(t, db, "leads")
	customers := captureUpdateColumns(t, db, "customers")
	tickets := captureUpdateColumns(t, db, "tickets")

	t.Run("leadRepository.Update writes exactly the lead state", func(t *testing.T) {
		stored, err := NewLeadRepository(db).GetByID(lead.ID)
		require.NoError(t, err)
		stored.Notes = "changed"
		require.NoError(t, NewLeadRepository(db).Update(stored))
		assertNoWebhookStateDrift(t, "leadRepository.Update", leads.take(), leadState, true)
	})

	t.Run("customerRepository.Update writes exactly the customer state", func(t *testing.T) {
		stored, err := NewCustomerRepository(db).GetByID(customer.ID)
		require.NoError(t, err)
		stored.Notes = "changed"
		require.NoError(t, NewCustomerRepository(db).Update(stored))
		assertNoWebhookStateDrift(t, "customerRepository.Update", customers.take(), customerState, true)
	})

	t.Run("ticketRepository.Update writes exactly the ticket state", func(t *testing.T) {
		stored, err := NewTicketRepository(db).GetByID(ticket.ID)
		require.NoError(t, err)
		stored.Resolution = "changed"
		require.NoError(t, NewTicketRepository(db).Update(stored))
		assertNoWebhookStateDrift(t, "ticketRepository.Update", tickets.take(), ticketState, true)
	})

	t.Run("leadRepository.ConvertToCustomer writes only lead state columns", func(t *testing.T) {
		require.NoError(t, NewLeadRepository(db).ConvertToCustomer(lead.ID, customer.ID))
		assertNoWebhookStateDrift(t, "leadRepository.ConvertToCustomer", leads.take(), leadState, false)
	})

	t.Run("automationRepository.SetLeadOwner writes only lead state columns", func(t *testing.T) {
		require.NoError(t, NewAutomationRepository(db).SetLeadOwner(lead.ID, other.ID, now))
		assertNoWebhookStateDrift(t, "automationRepository.SetLeadOwner", leads.take(), leadState, false)
	})

	t.Run("automationRepository.SetTicketAssignee writes only ticket state columns", func(t *testing.T) {
		require.NoError(t, NewAutomationRepository(db).SetTicketAssignee(ticket.ID, other.ID, now))
		assertNoWebhookStateDrift(t, "automationRepository.SetTicketAssignee", tickets.take(), ticketState, false)
	})

	t.Run("extra written column is reported", func(t *testing.T) {
		written := append(append([]string{}, leadState...), "updated_at", "first_responded_at")
		missing, unwritten := webhookStateDrift(written, leadState, true)
		assert.Equal(t, []string{"first_responded_at"}, missing, "a written column the state lacks is reported")
		assert.Empty(t, unwritten)
	})

	t.Run("unwritten state column is reported", func(t *testing.T) {
		written := []string{"id", "updated_at"}
		for _, c := range ticketState {
			if c != "due_at" {
				written = append(written, c)
			}
		}
		missing, unwritten := webhookStateDrift(written, ticketState, true)
		assert.Empty(t, missing)
		assert.Equal(t, []string{"due_at"}, unwritten, "a state column the update does not write is reported")
	})
}
