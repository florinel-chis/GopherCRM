package main

import (
	"database/sql"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pelicroftWindowTables are the tables whose *_at columns the generator
// moves into the history.
var pelicroftWindowTables = []string{
	"users", "forms", "products", "labels", "automation_rules", "automation_steps", "companies",
	"customers", "leads", "deals", "deal_stage_changes", "quotes", "quote_lines",
	"quote_status_changes", "quote_response_tokens", "tickets", "ticket_comments", "tasks",
	"notifications", "form_submissions", "form_confirmation_tokens", "form_ticket_keys",
	"form_mail_deliveries", "automation_events", "automation_runs", "automation_run_steps",
}

// pelicroftRelativeColumns are relative to their record, checked apart.
var pelicroftRelativeColumns = map[string]bool{
	"tickets.due_at":                      true,
	"form_confirmation_tokens.expires_at": true,
}

// pelicroftRunTimeColumns are stamped at run time by the real path; each
// must hold data, so the window check cannot pass vacuously.
var pelicroftRunTimeColumns = []string{
	"form_submissions.confirmed_at", "form_confirmation_tokens.used_at",
	"form_mail_deliveries.sent_at", "form_mail_deliveries.last_attempt_at",
	"tickets.first_responded_at", "automation_events.processed_at",
	"automation_runs.claimed_at", "automation_runs.finished_at",
	"automation_run_steps.executed_at",
}

func pelicroftDigest(t *testing.T, db *gorm.DB) string {
	t.Helper()
	return digestWith(t, db, []digestQuery{
		{"users", "email, role, created_at"},
		{"form_submissions", "form_id, email, status, spam_reason, ticket_outcome, ticket_id, created_at"},
		{"leads", "first_name, last_name, email, status, owner_id, created_at"},
		{"companies", "name, domain, created_at"},
		{"customers", "email, company_id, created_at"},
		{"deals", "title, stage, amount_cents, currency, created_at"},
		{"products", "name, sku, unit_price_cents"},
		{"quotes", "status, total_cents, (SELECT COUNT(*) FROM quote_lines WHERE quote_lines.quote_id = quotes.id), created_at"},
		{"tickets", "title, status, priority, assigned_to_id, (SELECT COUNT(*) FROM ticket_comments WHERE ticket_comments.ticket_id = tickets.id), created_at"},
		{"tasks", "title, status, assigned_to_id, due_date, created_at"},
		{"labels", "name"},
	})
}

func formID(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	var form models.Form
	require.NoError(t, db.Where("name = ?", name).First(&form).Error, name)
	return form.ID
}

func userID(t *testing.T, db *gorm.DB, email string) uint {
	t.Helper()
	var user models.User
	require.NoError(t, db.Where("email = ?", email).First(&user).Error, email)
	return user.ID
}

func ruleID(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	var rule models.AutomationRule
	require.NoError(t, db.Where("name = ?", name).First(&rule).Error, name)
	return rule.ID
}

var spamPlannedPattern = regexp.MustCompile(regexp.QuoteMeta(spamPlannedLabel) + ` (\d+)`)

func plannedSpam(t *testing.T, out string) int64 {
	t.Helper()
	m := spamPlannedPattern.FindStringSubmatch(out)
	require.NotNil(t, m, "the summary reports the planned spam")
	n, err := strconv.ParseInt(m[1], 10, 64)
	require.NoError(t, err)
	return n
}

// triageTasks maps each task the triage rule made to the ticket its run
// ran on.
func triageTasks(t *testing.T, db *gorm.DB) map[uint]uint {
	t.Helper()
	triage := ruleID(t, db, "Triage contact-form tickets")
	var runs []models.AutomationRun
	require.NoError(t, db.Where("rule_id = ?", triage).Find(&runs).Error)
	ticketOf := map[uint]uint{}
	for _, run := range runs {
		ticketOf[run.ID] = run.EntityID
	}
	var steps []models.AutomationRunStep
	require.NoError(t, db.Where("step_type = ? AND outcome = ?", models.AutomationStepCreateTask, models.AutomationOutcomeSucceeded).Find(&steps).Error)
	tasks := map[uint]uint{}
	for _, st := range steps {
		ticket, ok := ticketOf[st.RunID]
		if !ok {
			continue
		}
		id, ok := detailID(st.Details, "task_id")
		require.True(t, ok, "run step %d names its task", st.ID)
		tasks[id] = ticket
	}
	return tasks
}

// ---- assertions shared by the SQLite and MySQL roots ------------------------

func assertPelicroftFormTickets(t *testing.T, db *gorm.DB) {
	contact := formID(t, db, pelicroftContactForm)
	tickets := count(t, db, "tickets", "")
	require.Positive(t, tickets)

	assert.Zero(t, count(t, db, "tickets",
		"(SELECT COUNT(*) FROM form_ticket_keys k WHERE k.ticket_id = tickets.id AND k.form_id = ?) <> 1", contact),
		"tickets without exactly one Contact us key")
	assert.Equal(t, tickets, count(t, db, "form_ticket_keys",
		"form_id = ? AND EXISTS (SELECT 1 FROM form_submissions s WHERE s.id = form_ticket_keys.submission_id AND s.form_id = ? AND s.ticket_id = form_ticket_keys.ticket_id AND s.ticket_outcome = ?)",
		contact, contact, models.FormTicketOutcomeCreated), "keys whose submission opened their ticket")
	assert.Equal(t, tickets, count(t, db, "form_submissions", "form_id = ? AND ticket_outcome = ?", contact, models.FormTicketOutcomeCreated))
	assert.Equal(t, tickets, count(t, db, "tickets", "title = ?", "Form: Contact us"))

	type pair struct{ Status, Outcome string }
	var pairs []pair
	require.NoError(t, db.Table("form_submissions").Select("status, COALESCE(ticket_outcome, '') AS outcome").
		Where("form_id = ?", contact).Group("status, COALESCE(ticket_outcome, '')").Scan(&pairs).Error)
	allowed := map[pair]bool{{"confirmed", "created"}: true, {"confirmed", "no_customer"}: true, {"spam", ""}: true}
	for _, p := range pairs {
		assert.True(t, allowed[p], "Contact us (status, outcome) %v", p)
	}
	assert.Positive(t, count(t, db, "form_submissions", "form_id = ? AND status = ? AND ticket_outcome = ?",
		contact, models.FormSubmissionConfirmed, models.FormTicketOutcomeNoCustomer))
	assert.Zero(t, count(t, db, "form_submissions", "form_id = ? AND ticket_outcome = ? AND ticket_id IS NOT NULL",
		contact, models.FormTicketOutcomeNoCustomer))

	others := []uint{formID(t, db, pelicroftNewsletterForm), formID(t, db, pelicroftWholesaleForm)}
	assert.Positive(t, count(t, db, "form_submissions", "form_id IN ?", others))
	assert.Zero(t, count(t, db, "form_submissions", "form_id IN ? AND status <> ?", others, models.FormSubmissionReceived))
	assert.Zero(t, count(t, db, "form_submissions", "status = ?", models.FormSubmissionPending))
	assert.Zero(t, count(t, db, "form_confirmation_tokens", "used_at IS NULL"))

	var statuses []string
	require.NoError(t, db.Table("tickets").Distinct("status").Order("status").Pluck("status", &statuses).Error)
	assert.Equal(t, []string{"closed", "in_progress", "open", "resolved"}, statuses)

	var list []models.Ticket
	require.NoError(t, db.Preload("Comments").Find(&list).Error)
	roles := map[uint]models.UserRole{}
	var users []models.User
	require.NoError(t, db.Find(&users).Error)
	for _, u := range users {
		roles[u.ID] = u.Role
	}
	for _, ticket := range list {
		assert.True(t, len(ticket.Comments) >= 1 && len(ticket.Comments) <= 6, "ticket %d has %d comments", ticket.ID, len(ticket.Comments))
		for _, c := range ticket.Comments {
			role := roles[c.AuthorUserID]
			assert.True(t, role == models.RoleAdmin || role == models.RoleSupport, "comment %d by %s", c.ID, role)
			assert.False(t, c.CreatedAt.Before(ticket.CreatedAt), "comment %d older than ticket %d", c.ID, ticket.ID)
		}
		if assert.NotNil(t, ticket.AssignedToID, "ticket %d", ticket.ID) {
			assert.Equal(t, models.RoleSupport, roles[*ticket.AssignedToID], "ticket %d assignee", ticket.ID)
		}
	}
}

func assertPelicroftSpam(t *testing.T, db *gorm.DB, out string) {
	contact := formID(t, db, pelicroftContactForm)
	for _, reason := range []string{models.FormSpamReasonHoneypot, models.FormSpamReasonDomain, models.FormSpamReasonTimeTrap} {
		assert.Positive(t, count(t, db, "form_submissions", "form_id = ? AND status = ? AND spam_reason = ?",
			contact, models.FormSubmissionSpam, reason), reason)
	}
	spam := "SELECT id FROM form_submissions WHERE status = 'spam'"
	assert.Zero(t, count(t, db, "form_submissions", "status = ? AND ticket_id IS NOT NULL", models.FormSubmissionSpam))
	assert.Zero(t, count(t, db, "form_ticket_keys", "submission_id IN ("+spam+")"))
	assert.Zero(t, count(t, db, "form_confirmation_tokens", "submission_id IN ("+spam+")"))
	assert.Equal(t, plannedSpam(t, out), count(t, db, "form_submissions", "status = ?", models.FormSubmissionSpam))
	assert.Equal(t, plannedSpam(t, out), count(t, db, "form_submissions", "form_id = ? AND status = ?", contact, models.FormSubmissionSpam))
}

func assertPelicroftWholesale(t *testing.T, db *gorm.DB) {
	p := pelicroftProfile()
	vol := p.Volumes.Scaled(0.05)
	wholesale := formID(t, db, pelicroftWholesaleForm)
	assert.Equal(t, int64(vol.WholesaleEnquiries), count(t, db, "leads", ""))
	assert.Zero(t, count(t, db, "leads",
		"NOT EXISTS (SELECT 1 FROM form_submissions s WHERE s.lead_id = leads.id AND s.form_id = ?)", wholesale),
		"leads without a Wholesale enquiry submission")

	var converted []models.Lead
	require.NoError(t, db.Where("status = ?", models.LeadStatusConverted).Find(&converted).Error)
	assert.Len(t, converted, vol.ConvertedLeads)
	for _, lead := range converted {
		require.NotNil(t, lead.CustomerID, "lead %d", lead.ID)
		var customer models.Customer
		require.NoError(t, db.First(&customer, *lead.CustomerID).Error, "live customer of lead %d", lead.ID)
		assert.Equal(t, lead.Email, customer.Email)
		assert.NotNil(t, customer.CompanyID, "customer %d belongs to a company", customer.ID)
	}
	assert.Equal(t, int64(vol.Customers), count(t, db, "customers", ""))

	assert.Positive(t, count(t, db, "deals", ""))
	assert.Positive(t, count(t, db, "quotes", ""))
	skus := make([]string, 0, len(p.Products))
	for _, def := range p.Products {
		skus = append(skus, def.SKU)
	}
	assert.Len(t, p.Products, 40)
	assert.Equal(t, int64(len(p.Products)), count(t, db, "products", ""))
	assert.Positive(t, count(t, db, "quote_lines", ""))
	assert.Zero(t, count(t, db, "quote_lines", "product_id NOT IN (SELECT id FROM products WHERE sku IN ?)", skus))
}

func assertPelicroftAutomation(t *testing.T, db *gorm.DB) {
	assert.Positive(t, count(t, db, "automation_runs", ""))
	assert.Zero(t, count(t, db, "automation_runs", "status <> ?", models.AutomationRunCompleted))
	assert.Zero(t, count(t, db, "automation_events", "processed_at IS NULL"))

	tickets := count(t, db, "tickets", "")
	manager := userID(t, db, pelicroftStoreManager)
	tasks := triageTasks(t, db)
	assert.Equal(t, int(tickets), len(tasks), "succeeded create_task steps of the triage rule")
	perTicket := map[uint]int{}
	ids := make([]uint, 0, len(tasks))
	for task, ticket := range tasks {
		perTicket[ticket]++
		ids = append(ids, task)
	}
	assert.Len(t, perTicket, int(tickets), "one triage task per ticket")
	assert.Equal(t, int64(len(ids)), count(t, db, "tasks", "id IN ? AND assigned_to_id = ?", ids, manager))
	assert.Zero(t, count(t, db, "tasks", "assigned_to_id = ? AND id NOT IN ?", manager, ids), "manual tasks of the store manager")

	wholesale := ruleID(t, db, "Tell wholesale about new enquiries")
	leads := count(t, db, "leads", "")
	assert.Equal(t, leads, count(t, db, "notifications",
		"entity_type = ? AND run_id IN (SELECT id FROM automation_runs WHERE rule_id = ?)", models.AutomationEntityLead, wholesale))
	var entities []uint
	require.NoError(t, db.Table("notifications").Distinct("entity_id").Where("entity_type = ?", models.AutomationEntityLead).Pluck("entity_id", &entities).Error)
	assert.Len(t, entities, int(leads), "one notification per wholesale lead")
}

func assertPelicroftTimestamps(t *testing.T, db *gorm.DB) {
	from := testAnchor.AddDate(0, -backdateMonths, 0)
	seen := map[string]int{}
	for _, table := range pelicroftWindowTables {
		columns, err := db.Migrator().ColumnTypes(table)
		require.NoError(t, err, table)
		for _, column := range columns {
			name := column.Name()
			if !strings.HasSuffix(name, "_at") || pelicroftRelativeColumns[table+"."+name] {
				continue
			}
			var values []sql.NullTime
			require.NoError(t, db.Table(table).Pluck(name, &values).Error, table+"."+name)
			seen[table+"."+name] += 0
			for _, v := range values {
				if !v.Valid {
					continue
				}
				seen[table+"."+name]++
				assert.False(t, v.Time.Before(from) || v.Time.After(testAnchor), "%s.%s %s outside the window", table, name, v.Time.UTC())
			}
		}
	}
	for _, column := range pelicroftRunTimeColumns {
		n, enumerated := seen[column]
		assert.True(t, enumerated, "%s read from the schema", column)
		assert.Positive(t, n, "%s holds data", column)
	}

	var tickets []models.Ticket
	require.NoError(t, db.Find(&tickets).Error)
	ticketAt := map[uint]time.Time{}
	months := map[string]bool{}
	for _, ticket := range tickets {
		ticketAt[ticket.ID] = ticket.CreatedAt
		months[ticket.CreatedAt.UTC().Format("2006-01")] = true
		if ticket.DueAt != nil {
			assert.False(t, ticket.DueAt.Before(ticket.CreatedAt) || ticket.DueAt.After(ticket.CreatedAt.Add(72*time.Hour)), "ticket %d due_at", ticket.ID)
		}
	}
	assert.GreaterOrEqual(t, len(months), 12, "tickets cover at least 12 months")

	var submissions []models.FormSubmission
	require.NoError(t, db.Find(&submissions).Error)
	submissionAt := map[uint]time.Time{}
	for _, s := range submissions {
		submissionAt[s.ID] = s.CreatedAt
		if s.TicketID != nil {
			assert.False(t, ticketAt[*s.TicketID].Before(s.CreatedAt), "ticket %d older than submission %d", *s.TicketID, s.ID)
		}
		if s.ConfirmedAt != nil {
			assert.False(t, s.ConfirmedAt.Before(s.CreatedAt), "submission %d confirmed before it was made", s.ID)
		}
	}
	var tokens []models.FormConfirmationToken
	require.NoError(t, db.Find(&tokens).Error)
	require.NotEmpty(t, tokens)
	for _, token := range tokens {
		created := submissionAt[token.SubmissionID]
		assert.True(t, token.ExpiresAt.After(created) && !token.ExpiresAt.After(created.Add(48*time.Hour)), "token %d expires_at", token.ID)
		if token.UsedAt != nil {
			assert.False(t, token.UsedAt.Before(created), "token %d used before its submission", token.ID)
		}
	}

	var leads []models.Lead
	require.NoError(t, db.Find(&leads).Error)
	leadAt := map[uint]time.Time{}
	for _, lead := range leads {
		leadAt[lead.ID] = lead.CreatedAt
		if lead.CustomerID != nil {
			var customer models.Customer
			require.NoError(t, db.First(&customer, *lead.CustomerID).Error)
			assert.False(t, customer.CreatedAt.Before(lead.CreatedAt), "customer %d older than lead %d", customer.ID, lead.ID)
		}
	}
	for _, s := range submissions {
		if s.LeadID != nil {
			assert.False(t, leadAt[*s.LeadID].Before(s.CreatedAt), "lead %d older than submission %d", *s.LeadID, s.ID)
		}
	}
	entityAt := func(entityType string, id uint) time.Time {
		at, ok := map[string]map[uint]time.Time{models.AutomationEntityTicket: ticketAt, models.AutomationEntityLead: leadAt}[entityType][id]
		assert.True(t, ok, "%s %d exists", entityType, id)
		return at
	}

	var events []models.AutomationEvent
	require.NoError(t, db.Find(&events).Error)
	for _, ev := range events {
		assert.False(t, ev.CreatedAt.Before(entityAt(ev.EntityType, ev.EntityID)), "event %d older than its record", ev.ID)
		if assert.NotNil(t, ev.ProcessedAt) {
			assert.False(t, ev.ProcessedAt.Before(ev.CreatedAt), "event %d processed before it was made", ev.ID)
		}
	}
	var runs []models.AutomationRun
	require.NoError(t, db.Find(&runs).Error)
	type span struct{ from, to time.Time }
	runSpan := map[uint]span{}
	for _, run := range runs {
		assert.False(t, run.CreatedAt.Before(entityAt(run.EntityType, run.EntityID)), "run %d older than its record", run.ID)
		require.NotNil(t, run.ClaimedAt, "run %d", run.ID)
		require.NotNil(t, run.FinishedAt, "run %d", run.ID)
		assert.False(t, run.ClaimedAt.Before(run.CreatedAt), "run %d claimed before it was made", run.ID)
		assert.False(t, run.FinishedAt.Before(*run.ClaimedAt), "run %d finished before it was claimed", run.ID)
		runSpan[run.ID] = span{run.CreatedAt, *run.FinishedAt}
	}
	var steps []models.AutomationRunStep
	require.NoError(t, db.Find(&steps).Error)
	for _, st := range steps {
		s := runSpan[st.RunID]
		assert.False(t, st.ExecutedAt.Before(s.from) || st.ExecutedAt.After(s.to), "run step %d outside its run", st.ID)
	}

	for taskID, ticketID := range triageTasks(t, db) {
		var task models.Task
		require.NoError(t, db.First(&task, taskID).Error)
		created := ticketAt[ticketID]
		assert.False(t, task.CreatedAt.Before(created), "triage task %d older than ticket %d", taskID, ticketID)
		if assert.NotNil(t, task.DueDate) {
			assert.False(t, task.DueDate.Before(created) || task.DueDate.After(created.AddDate(0, 0, 3)), "triage task %d due date", taskID)
		}
	}
	var notifications []models.Notification
	require.NoError(t, db.Where("run_id IS NOT NULL").Find(&notifications).Error)
	require.NotEmpty(t, notifications)
	for _, n := range notifications {
		assert.False(t, n.CreatedAt.Before(entityAt(n.EntityType, n.EntityID)), "notification %d older than its record", n.ID)
	}
}

func assertPelicroftSeasonality(t *testing.T, db *gorm.DB) {
	from := testAnchor.AddDate(0, -backdateMonths, 0)
	holiday := func(at time.Time) bool { m := at.UTC().Month(); return m == time.November || m == time.December }
	var holidayDays, otherDays float64
	for d := from; d.Before(testAnchor); d = d.AddDate(0, 0, 1) {
		if holiday(d) {
			holidayDays++
		} else {
			otherDays++
		}
	}
	require.Positive(t, holidayDays)
	var times []time.Time
	require.NoError(t, db.Table("form_submissions").Where("form_id = ?", formID(t, db, pelicroftContactForm)).Pluck("created_at", &times).Error)
	var inHoliday, inOther float64
	for _, at := range times {
		if at.Before(from) || !at.Before(testAnchor) {
			continue
		}
		if holiday(at) {
			inHoliday++
		} else {
			inOther++
		}
	}
	require.Positive(t, inOther)
	assert.GreaterOrEqual(t, inHoliday/holidayDays, 2*inOther/otherDays,
		"November and December daily rate (%v in %v days) at least twice the rest (%v in %v days)", inHoliday, holidayDays, inOther, otherDays)
}

func assertPelicroftOffByDefault(t *testing.T, db *gorm.DB, out string) {
	assert.Zero(t, count(t, db, "webhook_deliveries", ""))
	assert.Zero(t, count(t, db, "ticket_mail_deliveries", ""))
	assert.Positive(t, count(t, db, "form_mail_deliveries", ""))
	assert.Zero(t, count(t, db, "form_mail_deliveries", "status <> ?", models.FormMailStatusSent))
	assert.Zero(t, count(t, db, "forms", "captcha_enabled = ?", true))

	var rules []models.AutomationRule
	require.NoError(t, db.Preload("Steps").Find(&rules).Error)
	disabled := 0
	for _, rule := range rules {
		if !rule.Enabled {
			disabled++
			continue
		}
		for _, step := range rule.Steps {
			assert.NotEqual(t, models.AutomationStepSendEmail, step.StepType, rule.Name)
			assert.NotEqual(t, models.AutomationStepWait, step.StepType, rule.Name)
		}
	}
	assert.Positive(t, disabled, "a disabled rule exists")

	assert.Zero(t, count(t, db, "configurations", "is_sensitive = ? AND value IS NOT NULL AND value <> ''", true),
		"sensitive configuration values")
	var name models.Configuration
	require.NoError(t, db.Where(&models.Configuration{Key: "general.company_name"}).First(&name).Error)
	assert.Equal(t, "Pelicroft Home", name.Value)

	assert.NotContains(t, out, "token=")
	assert.NotContains(t, out, "/forms/public/confirm")
}

// ---- roots --------------------------------------------------------------------

func TestSampleDataPelicroftDeterministic(t *testing.T) {
	setTestEnv(t)
	dir := t.TempDir()
	run := func(name string, seed int) (map[string]int64, string) {
		path := filepath.Join(dir, name)
		generateProfileSQLite(t, "pelicroft", path, seed)
		db := openSQLite(t, path)
		return tableCounts(t, db), pelicroftDigest(t, db)
	}
	countsA, hashA := run("a.db", 1)
	countsB, hashB := run("b.db", 1)
	assert.Equal(t, countsA, countsB, "per-table counts of two seed-1 runs")
	assert.Equal(t, hashA, hashB, "selected fields of two seed-1 runs")
	_, hashC := run("c.db", 2)
	assert.NotEqual(t, hashA, hashC, "seed 2 differs from seed 1")
}

func TestSampleDataPelicroftReservedDomains(t *testing.T) {
	setTestEnv(t)
	path := filepath.Join(t.TempDir(), "reserved.db")
	generateProfileSQLite(t, "pelicroft", path, 1)
	db := openSQLite(t, path)

	assert.Empty(t, scanReservedNames(t, db))
	// The scan saw the form data and the other kinds of value.
	assert.Positive(t, count(t, db, "form_submissions", "data LIKE ?", "%@%"))
	assert.Positive(t, count(t, db, "form_submissions", "referrer LIKE ?", "https://%"))
	assert.Positive(t, count(t, db, "customers", "phone <> ''"))
	assert.Positive(t, count(t, db, "companies", "domain LIKE ?", "%.example"))
}

func TestSampleDataPelicroftScale(t *testing.T) {
	setTestEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var connections atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	host, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)

	path := filepath.Join(t.TempDir(), "scale.db")
	out := generateProfileSQLite(t, "pelicroft", path, 1)
	db := openSQLite(t, path)

	t.Run("form_tickets", func(t *testing.T) { assertPelicroftFormTickets(t, db) })
	t.Run("spam", func(t *testing.T) { assertPelicroftSpam(t, db, out) })
	t.Run("wholesale", func(t *testing.T) { assertPelicroftWholesale(t, db) })
	t.Run("automation", func(t *testing.T) { assertPelicroftAutomation(t, db) })
	t.Run("timestamps", func(t *testing.T) { assertPelicroftTimestamps(t, db) })
	t.Run("seasonality", func(t *testing.T) { assertPelicroftSeasonality(t, db) })
	t.Run("off_by_default", func(t *testing.T) {
		assertPelicroftOffByDefault(t, db, out)
		assert.Zero(t, connections.Load(), "connections to the SMTP listener")
	})
}

func TestSampleDataPelicroftSignIn(t *testing.T) {
	setTestEnv(t)
	path := filepath.Join(t.TempDir(), "signin.db")
	out := generateProfileSQLite(t, "pelicroft", path, 1)
	p := pelicroftProfile()

	assert.Equal(t, 1, strings.Count(out, "admin@pelicroft.example"), out)
	assert.Equal(t, 1, strings.Count(out, p.DemoPassword), out)
	assert.NotContains(t, out, "$2a$", "no password hash")

	db := openSQLite(t, path)
	var users []models.User
	require.NoError(t, db.Order("id").Find(&users).Error)
	require.Len(t, users, len(p.Staff)+1)
	for _, u := range users {
		assert.NoError(t, signIn(t, db, u.Email, p.DemoPassword), "%s (%s)", u.Email, u.Role)
	}
	assert.Equal(t, int64(1), count(t, db, "users", "role = ? AND role_id IS NOT NULL", models.RoleCustom))
	assert.Positive(t, count(t, db, "tickets", "customer_id IN (SELECT id FROM customers WHERE user_id IS NOT NULL)"),
		"the customer login's shopper record has contact-form tickets")
}
