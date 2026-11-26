package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/database"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testAnchor fixes the end of the backdated window, so runs on different
// days hash alike.
var testAnchor = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

const testScale = "0.05"

// setTestEnv gives config.Load what it needs and pins the settings a
// developer's shell could carry into the run.
func setTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", "sample-data-test-secret-0123456789abcdef")
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("SERVER_MODE", "development")
	t.Setenv("SMTP_HOST", "")
}

// generate runs the command in-process at the test anchor.
func generate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runAt(args, &out, testAnchor)
	return out.String(), err
}

func generateSQLite(t *testing.T, path string, seed int, extra ...string) string {
	t.Helper()
	args := append([]string{"--profile", "halvenmark", "--seed", fmt.Sprint(seed), "--scale", testScale, "--out", path}, extra...)
	out, err := generate(t, args...)
	require.NoError(t, err)
	return out
}

func openSQLite(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := database.Open(&config.DatabaseConfig{Driver: config.DriverSQLite, Path: path})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func count(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := db.Table(table)
	if where != "" {
		q = q.Where(where, args...)
	}
	require.NoError(t, q.Count(&n).Error)
	return n
}

// expectedCounts are the row counts of a halvenmark run at testScale.
func expectedCounts() map[string]int64 {
	p := halvenmarkProfile()
	v := p.Volumes.Scaled(0.05)
	return map[string]int64{
		"users":             int64(len(p.Staff) + 1),
		"leads":             int64(v.Leads),
		"customers":         int64(v.Customers),
		"companies":         int64(v.Companies),
		"deals":             int64(v.Deals),
		"products":          int64(len(p.Products)),
		"quotes":            int64(v.Quotes),
		"tickets":           int64(v.Tickets),
		"tasks":             int64(v.Tasks),
		"labels":            int64(len(p.Labels)),
		"notifications":     int64(v.Notifications),
		"automation_rules":  3,
		"forms":             1,
		"webhook_endpoints": int64(len(p.Webhooks)),
	}
}

// assertScaledCounts checks the exact counts of a testScale run and that
// every status and stage exists.
func assertScaledCounts(t *testing.T, db *gorm.DB) {
	t.Helper()
	want := expectedCounts()
	assert.Equal(t, int64(30), want["leads"], "max(1, round(600*0.05))")
	for table, n := range want {
		assert.Equal(t, n, count(t, db, table, ""), table)
	}
	distinct := func(table, column string) []string {
		var values []string
		require.NoError(t, db.Table(table).Distinct(column).Order(column).Pluck(column, &values).Error)
		return values
	}
	assert.Equal(t, []string{"contacted", "converted", "new", "qualified", "unqualified"}, distinct("leads", "status"))
	assert.Equal(t, []string{"conference", "partner", "referral", "website"}, distinct("leads", "source"))
	assert.Equal(t, []string{"lost", "negotiation", "proposal", "qualification", "won"}, distinct("deals", "stage"))
	assert.Equal(t, []string{"accepted", "draft", "expired", "rejected", "sent"}, distinct("quotes", "status"))
	assert.Equal(t, []string{"closed", "in_progress", "open", "resolved"}, distinct("tickets", "status"))
	assert.Equal(t, []string{"EUR"}, distinct("deals", "currency"))
	assert.Equal(t, []string{"admin", "custom", "customer", "sales", "support"}, distinct("users", "role"))
	assert.Equal(t, int64(2), count(t, db, "users", "role = ? AND role_id IS NOT NULL", models.RoleCustom))
}

// tableCounts is the row count of every table.
func tableCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	counts := map[string]int64{}
	for _, table := range tables {
		counts[table] = count(t, db, table, "")
	}
	return counts
}

// ---- reserved names ---------------------------------------------------------

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@([A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+)`)
	urlPattern   = regexp.MustCompile(`(?i)\bhttps?://([^/\s"'<>?#:\\]+)`)
	phonePattern = regexp.MustCompile(`^(\+44 7700 900\d{3}|\+1 \d{3} 555 01\d{2})$`)
)

// reservedHost reports whether host is one of the RFC 2606 example domains
// (or below one) or under the reserved .example top-level domain.
func reservedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, base := range []string{"example.com", "example.org", "example.net"} {
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return host == "example" || strings.HasSuffix(host, ".example")
}

// scanReservedNames walks every string column of every table and returns a
// line per value that names a non-reserved email or URL host, a
// non-reserved companies.domain, or a phone column outside the two fiction
// formats.
func scanReservedNames(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	sort.Strings(tables)
	var problems []string
	for _, table := range tables {
		rows, err := db.Table(table).Rows()
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			for i, column := range columns {
				var text string
				switch v := values[i].(type) {
				case string:
					text = v
				case []byte:
					text = string(v)
				default:
					continue
				}
				where := table + "." + column
				for _, m := range emailPattern.FindAllStringSubmatch(text, -1) {
					if !reservedHost(m[1]) {
						problems = append(problems, fmt.Sprintf("%s: email host %s", where, m[1]))
					}
				}
				for _, m := range urlPattern.FindAllStringSubmatch(text, -1) {
					if !reservedHost(m[1]) {
						problems = append(problems, fmt.Sprintf("%s: URL host %s", where, m[1]))
					}
				}
				if table == "companies" && column == "domain" && text != "" && !reservedHost(text) {
					problems = append(problems, fmt.Sprintf("%s: domain %s", where, text))
				}
				if strings.Contains(column, "phone") && text != "" && !phonePattern.MatchString(text) {
					problems = append(problems, fmt.Sprintf("%s: phone %s", where, text))
				}
			}
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return problems
}

// ---- sign-in ----------------------------------------------------------------

func signIn(t *testing.T, db *gorm.DB, email, password string) error {
	t.Helper()
	cfg, err := config.Load()
	require.NoError(t, err)
	auth := service.NewAuthServiceWithSessions(
		repository.NewUserRepository(db), repository.NewAPIKeyRepository(db), repository.NewRefreshTokenRepository(db),
		repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), mailer.NewFromConfig(config.SMTPConfig{}),
		cfg.JWT, cfg.App.BaseURL, cfg.API.APIKeySecret)
	token, err := auth.Login(email, password)
	if err == nil && token == "" {
		return fmt.Errorf("empty token for %s", email)
	}
	return err
}

// ---- determinism ------------------------------------------------------------

// digest hashes the selected fields of a generated database in id order.
func digest(t *testing.T, db *gorm.DB) string {
	t.Helper()
	queries := []struct{ table, columns string }{
		{"users", "email, role, created_at"},
		{"leads", "first_name, last_name, email, status, source, owner_id, created_at"},
		{"companies", "name, domain, created_at"},
		{"customers", "email, company_id, created_at"},
		{"deals", "title, stage, amount_cents, currency, created_at"},
		{"products", "name, unit_price_cents"},
		{"quotes", "status, total_cents, (SELECT COUNT(*) FROM quote_lines WHERE quote_lines.quote_id = quotes.id), created_at"},
		{"tickets", "title, status, (SELECT COUNT(*) FROM ticket_comments WHERE ticket_comments.ticket_id = tickets.id), created_at"},
		{"tasks", "title, status, due_date, created_at"},
		{"labels", "name"},
	}
	h := sha256.New()
	for _, q := range queries {
		rows, err := db.Table(q.table).Select(q.columns).Order("id").Rows()
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			values := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range values {
				ptrs[i] = &values[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			fmt.Fprint(h, q.table)
			for _, v := range values {
				switch x := v.(type) {
				case time.Time:
					fmt.Fprintf(h, "|%s", x.UTC().Format(time.RFC3339Nano))
				case []byte:
					fmt.Fprintf(h, "|%s", x)
				default:
					fmt.Fprintf(h, "|%v", x)
				}
			}
			fmt.Fprintln(h)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestSampleDataDeterministic(t *testing.T) {
	setTestEnv(t)
	dir := t.TempDir()
	run := func(name string, seed int) (map[string]int64, string) {
		path := filepath.Join(dir, name)
		generateSQLite(t, path, seed)
		db := openSQLite(t, path)
		return tableCounts(t, db), digest(t, db)
	}
	countsA, hashA := run("a.db", 1)
	countsB, hashB := run("b.db", 1)
	assert.Equal(t, countsA, countsB, "per-table counts of two seed-1 runs")
	assert.Equal(t, hashA, hashB, "selected fields of two seed-1 runs")
	_, hashC := run("c.db", 2)
	assert.NotEqual(t, hashA, hashC, "seed 2 differs from seed 1")
}

// ---- reserved domains and phones --------------------------------------------

func TestSampleDataReservedDomains(t *testing.T) {
	setTestEnv(t)
	path := filepath.Join(t.TempDir(), "reserved.db")
	generateSQLite(t, path, 1)
	db := openSQLite(t, path)

	t.Run("generated data", func(t *testing.T) {
		assert.Empty(t, scanReservedNames(t, db))
		// The scan saw the data: emails, URLs, domains and phones exist.
		assert.Positive(t, count(t, db, "leads", "phone <> ''"))
		assert.Positive(t, count(t, db, "customers", "website LIKE ?", "https://%"))
		assert.Positive(t, count(t, db, "companies", "domain LIKE ?", "%.example"))
	})

	t.Run("scanner reports real names", func(t *testing.T) {
		require.NoError(t, db.Create(&models.Lead{FirstName: "Real", LastName: "Host", Email: "x@example.co",
			Phone: "+44 20 7946 0000", Status: models.LeadStatusNew, OwnerID: 1}).Error)
		require.NoError(t, db.Create(&models.Company{Name: "Real Domain", Domain: "real-shop.com",
			Website: "https://shop.example.co.uk/home"}).Error)
		problems := strings.Join(scanReservedNames(t, db), "\n")
		assert.Contains(t, problems, "leads.email: email host example.co")
		assert.Contains(t, problems, "leads.phone: phone +44 20 7946 0000")
		assert.Contains(t, problems, "companies.domain: domain real-shop.com")
		assert.Contains(t, problems, "companies.website: URL host shop.example.co.uk")
	})
}

// ---- refusals ----------------------------------------------------------------

func TestSampleDataRefusals(t *testing.T) {
	setTestEnv(t)

	t.Run("production mode", func(t *testing.T) {
		t.Setenv("SERVER_MODE", "production")
		for _, force := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "prod", "out.db")
			args := []string{"--profile", "halvenmark", "--seed", "1", "--scale", testScale, "--out", path}
			if force {
				args = append(args, "--force")
			}
			_, err := generate(t, args...)
			require.Error(t, err, "force=%v", force)
			assert.Contains(t, err.Error(), "production", "force=%v", force)
			assert.NoFileExists(t, path)
			assert.NoDirExists(t, filepath.Dir(path))
		}
	})

	t.Run("existing data", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.db")
		generateSQLite(t, path, 1)
		db := openSQLite(t, path)
		users, leads := count(t, db, "users", ""), count(t, db, "leads", "")
		// One more lead, so a rebuild is visible in the count.
		require.NoError(t, db.Create(&models.Lead{FirstName: "Kept", LastName: "Row", Email: "kept@example.com",
			Status: models.LeadStatusNew, OwnerID: 1}).Error)
		leads++

		_, err := generate(t, "--profile", "halvenmark", "--seed", "1", "--scale", testScale, "--out", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--force")
		assert.Equal(t, users, count(t, db, "users", ""))
		assert.Equal(t, leads, count(t, db, "leads", ""))

		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())

		generateSQLite(t, path, 1, "--force")
		fresh := openSQLite(t, path)
		for table, n := range expectedCounts() {
			assert.Equal(t, n, count(t, fresh, table, ""), table)
		}
		assert.Zero(t, count(t, fresh, "leads", "email = ?", "kept@example.com"))
	})

	t.Run("arguments", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "args.db")
		cases := map[string][]string{
			"both out and dsn":  {"--profile", "halvenmark", "--out", out, "--dsn", "u:p@tcp(127.0.0.1:1)/gocrm_x"},
			"neither":           {"--profile", "halvenmark", "--seed", "1"},
			"unknown profile":   {"--profile", "nosuch", "--out", out},
			"scale zero":        {"--profile", "halvenmark", "--out", out, "--scale", "0"},
			"scale negative":    {"--profile", "halvenmark", "--out", out, "--scale", "-1"},
			"scale above ten":   {"--profile", "halvenmark", "--out", out, "--scale", "11"},
			"stray argument":    {"--profile", "halvenmark", "--out", out, "extra"},
			"dsn without a db":  {"--profile", "halvenmark", "--dsn", "u:p@tcp(127.0.0.1:1)/"},
			"dsn not tcp-based": {"--profile", "halvenmark", "--dsn", "u:p@unix(/tmp/x.sock)/gocrm_x"},
		}
		for name, args := range cases {
			_, err := generate(t, args...)
			assert.Error(t, err, name)
		}
		_, err := generate(t, "--profile", "nosuch", "--out", out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "halvenmark", "the error lists the valid profiles")
		_, err = generate(t, "--profile", "halvenmark", "--out", out, "--scale", "11")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--scale")
		assert.NoFileExists(t, out)

		// The upper bound is inclusive and the guard is the only refusal:
		// a valid scale on the same arguments succeeds.
		_, err = generate(t, "--profile", "halvenmark", "--out", out, "--scale", testScale)
		require.NoError(t, err)
	})
}

// ---- volumes, business rules, timestamps and off-by-default state ------------

func TestSampleDataScale(t *testing.T) {
	setTestEnv(t)
	// An SMTP server the environment points at: the generator must never
	// connect to it.
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
	generateSQLite(t, path, 1)
	db := openSQLite(t, path)

	assertScaledCounts(t, db)
	assertBusinessRules(t, db)
	assertTimestamps(t, db)
	assertOffByDefault(t, db)
	assert.Zero(t, connections.Load(), "connections to the SMTP listener")
}

func assertBusinessRules(t *testing.T, db *gorm.DB) {
	t.Helper()

	t.Run("converted leads", func(t *testing.T) {
		var leads []models.Lead
		require.NoError(t, db.Where("status = ?", models.LeadStatusConverted).Find(&leads).Error)
		assert.Len(t, leads, halvenmarkProfile().Volumes.Scaled(0.05).ConvertedLeads)
		for _, lead := range leads {
			require.NotNil(t, lead.CustomerID, "lead %d", lead.ID)
			var customer models.Customer
			require.NoError(t, db.First(&customer, *lead.CustomerID).Error, "live customer of lead %d", lead.ID)
			assert.Equal(t, lead.Email, customer.Email)
			assert.False(t, customer.CreatedAt.Before(lead.CreatedAt), "customer %d older than lead %d", customer.ID, lead.ID)
		}
		assert.Zero(t, count(t, db, "leads", "status <> ? AND customer_id IS NOT NULL", models.LeadStatusConverted))
	})

	t.Run("quote totals", func(t *testing.T) {
		var quotes []models.Quote
		require.NoError(t, db.Preload("Lines").Find(&quotes).Error)
		require.NotEmpty(t, quotes)
		for _, q := range quotes {
			assert.True(t, len(q.Lines) >= 1 && len(q.Lines) <= 4, "quote %d has %d lines", q.ID, len(q.Lines))
			var sum int64
			for _, line := range q.Lines {
				sum += line.LineTotalCents
			}
			assert.Equal(t, sum, q.TotalCents, "quote %d", q.ID)
			if q.Status == models.QuoteStatusAccepted {
				require.NotNil(t, q.ValidUntil)
				require.NotNil(t, q.ClosedAt)
				assert.False(t, q.ValidUntil.Before(q.ClosedAt.Truncate(24*time.Hour)), "quote %d accepted after valid_until", q.ID)
			}
		}
	})

	t.Run("deals", func(t *testing.T) {
		var deals []models.Deal
		require.NoError(t, db.Find(&deals).Error)
		min, max := halvenmarkProfile().DealAmountMinCents, halvenmarkProfile().DealAmountMaxCents
		for _, d := range deals {
			switch d.Stage {
			case models.DealStageWon:
				assert.Equal(t, 100, d.Probability, "deal %d", d.ID)
				assert.NotNil(t, d.ClosedAt, "deal %d", d.ID)
			case models.DealStageLost:
				assert.Equal(t, 0, d.Probability, "deal %d", d.ID)
				assert.NotNil(t, d.ClosedAt, "deal %d", d.ID)
				assert.NotEmpty(t, d.LostReason, "deal %d", d.ID)
			default:
				assert.Nil(t, d.ClosedAt, "deal %d", d.ID)
			}
			assert.True(t, d.AmountCents >= min && d.AmountCents <= max, "deal %d amount %d", d.ID, d.AmountCents)
			var changes []models.DealStageChange
			require.NoError(t, db.Where("deal_id = ?", d.ID).Order("id").Find(&changes).Error)
			require.NotEmpty(t, changes, "deal %d has a stage change row", d.ID)
			assert.Equal(t, d.Stage, changes[len(changes)-1].ToStage)
			for _, c := range changes {
				assert.False(t, c.ChangedAt.Before(d.CreatedAt), "stage change %d older than deal %d", c.ID, d.ID)
			}
		}
	})

	t.Run("lead owners are sales", func(t *testing.T) {
		assert.Zero(t, count(t, db, "leads", "owner_id NOT IN (SELECT id FROM users WHERE role = ?)", models.RoleSales))
	})

	t.Run("ticket comments", func(t *testing.T) {
		var tickets []models.Ticket
		require.NoError(t, db.Preload("Comments").Find(&tickets).Error)
		internal := 0
		for _, ticket := range tickets {
			assert.True(t, len(ticket.Comments) >= 1 && len(ticket.Comments) <= 8, "ticket %d has %d comments", ticket.ID, len(ticket.Comments))
			for _, c := range ticket.Comments {
				assert.False(t, c.CreatedAt.Before(ticket.CreatedAt), "comment %d older than ticket %d", c.ID, ticket.ID)
				if c.IsInternal {
					internal++
				}
			}
		}
		assert.Positive(t, internal, "some comments are internal notes")
		assert.Positive(t, count(t, db, "tickets", "customer_id IN (SELECT id FROM customers WHERE user_id IS NOT NULL)"),
			"the customer login's customer has tickets")
		assert.Zero(t, count(t, db, "tickets", "assigned_to_id NOT IN (SELECT id FROM users WHERE role = ?)", models.RoleSupport))
	})

	t.Run("task labels", func(t *testing.T) {
		assert.Positive(t, count(t, db, "task_labels", ""))
	})
}

func assertTimestamps(t *testing.T, db *gorm.DB) {
	t.Helper()
	from := testAnchor.AddDate(0, -backdateMonths, 0)
	months := map[string]bool{}
	for _, table := range []string{"leads", "deals", "quotes", "tickets"} {
		var times []time.Time
		require.NoError(t, db.Table(table).Pluck("created_at", &times).Error)
		own := map[string]bool{}
		for _, at := range times {
			assert.False(t, at.Before(from) || at.After(testAnchor), "%s created_at %s outside the window", table, at)
			months[at.UTC().Format("2006-01")] = true
			own[at.UTC().Format("2006-01")] = true
		}
		if table == "leads" {
			assert.GreaterOrEqual(t, len(own), 12, "leads cover at least 12 months")
		}
	}
	assert.GreaterOrEqual(t, len(months), 12, "leads, deals, quotes and tickets cover at least 12 months")

	open := []models.TaskStatus{models.TaskStatusPending, models.TaskStatusInProgress}
	assert.Positive(t, count(t, db, "tasks", "status IN ? AND due_date < ?", open, testAnchor), "overdue tasks")
	assert.Positive(t, count(t, db, "tasks", "status IN ? AND due_date > ?", open, testAnchor), "upcoming tasks")
	assert.Positive(t, count(t, db, "tasks", "status = ?", models.TaskStatusCompleted), "completed tasks")
}

func assertOffByDefault(t *testing.T, db *gorm.DB) {
	t.Helper()
	var endpoints []models.WebhookEndpoint
	require.NoError(t, db.Find(&endpoints).Error)
	require.NotEmpty(t, endpoints)
	for _, e := range endpoints {
		assert.False(t, e.IsActive, "endpoint %d", e.ID)
		assert.True(t, strings.HasPrefix(e.URL, "https://") && reservedHost(urlPattern.FindStringSubmatch(e.URL)[1]), e.URL)
	}
	assert.Zero(t, count(t, db, "automation_events", "processed_at IS NULL"), "unprocessed automation events")
	assert.Zero(t, count(t, db, "automation_runs", ""), "automation runs")
	assert.Zero(t, count(t, db, "webhook_deliveries", ""), "webhook deliveries")

	var enabled []models.AutomationRule
	require.NoError(t, db.Preload("Steps").Where("enabled = ?", true).Find(&enabled).Error)
	require.Len(t, enabled, 1)
	for _, step := range enabled[0].Steps {
		assert.NotEqual(t, models.AutomationStepSendEmail, step.StepType)
	}
	assert.Zero(t, count(t, db, "configurations", "is_sensitive = ? AND value IS NOT NULL AND value <> ''", true),
		"sensitive configuration values")
	var name models.Configuration
	require.NoError(t, db.Where(&models.Configuration{Key: "general.company_name"}).First(&name).Error)
	assert.Equal(t, "Halvenmark Commerce Studio", name.Value)
}

// ---- sign-in -------------------------------------------------------------------

func TestSampleDataSignIn(t *testing.T) {
	setTestEnv(t)
	path := filepath.Join(t.TempDir(), "signin.db")
	out := generateSQLite(t, path, 1)
	p := halvenmarkProfile()

	assert.Equal(t, 1, strings.Count(out, "admin@halvenmark.example"), out)
	assert.Equal(t, 1, strings.Count(out, p.DemoPassword), out)
	assert.NotContains(t, out, webhook.WebhookSecretPrefix)
	assert.NotContains(t, out, "$2a$", "no password hash")

	db := openSQLite(t, path)
	var users []models.User
	require.NoError(t, db.Order("id").Find(&users).Error)
	require.Len(t, users, len(p.Staff)+1)
	for _, u := range users {
		assert.NoError(t, signIn(t, db, u.Email, p.DemoPassword), "%s (%s)", u.Email, u.Role)
	}
	assert.Error(t, signIn(t, db, users[0].Email, "Wrong-Password-2026!"), "a wrong password is refused")
}
