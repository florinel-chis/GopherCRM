package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The staff-typed text of deals and quotes linked to an erased person (#245).
//
// Erasing a customer or lead overwrites the title, notes and lost_reason of
// every deal linked to it, the title and notes of every quote addressed to it
// or grown from such a deal, and every line description of those quotes (with
// its product's name), and hard-deletes the notifications about those deals,
// in the same erasure unit. Everything else on those rows, and every row the
// graph does not reach, stays.
//
// The fixtures run on the full production schema with foreign keys on
// (openIdentityErasureDB). The MySQL roots use a disposable schema on
// TEST_MYSQL_DSN, skip without it and fail when the server is unreachable.

func skipBusinessTextWithoutMySQL(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
}

type businessTextFixture struct {
	db           *gorm.DB
	tag          string
	admin, sales *models.User
	seq          int
}

func newBusinessTextFixture(t *testing.T, db *gorm.DB) *businessTextFixture {
	t.Helper()
	tag := nextIdentityTag()
	return &businessTextFixture{
		db:    db,
		tag:   tag,
		admin: seedIdentityStaff(t, db, tag, models.RoleAdmin),
		sales: seedIdentityStaff(t, db, tag, models.RoleSales),
	}
}

func (f *businessTextFixture) next(label string) string {
	f.seq++
	return fmt.Sprintf("%s%d", label, f.seq)
}

func (f *businessTextFixture) customer(t *testing.T, label string, userID *uint) *models.Customer {
	t.Helper()
	label = f.next(label)
	c := &models.Customer{FirstName: "Bt" + label, LastName: "Person" + f.tag, Email: fmt.Sprintf("bt-%s-%s@person.test", label, f.tag),
		Phone: "+1-555-" + f.tag, Address: "1 Bt Lane " + label + " " + f.tag, UserID: userID, AssignedToID: &f.sales.ID}
	require.NoError(t, f.db.Create(c).Error)
	return c
}

func (f *businessTextFixture) lead(t *testing.T, label string, customerID *uint) *models.Lead {
	t.Helper()
	label = f.next(label)
	status := models.LeadStatusNew
	if customerID != nil {
		status = models.LeadStatusConverted
	}
	l := &models.Lead{FirstName: "Bt" + label, LastName: "Person" + f.tag, Email: fmt.Sprintf("bt-lead-%s-%s@person.test", label, f.tag),
		Source: "website", Status: status, OwnerID: f.sales.ID, CustomerID: customerID}
	require.NoError(t, f.db.Create(l).Error)
	return l
}

func (f *businessTextFixture) deal(t *testing.T, label string, ownerID uint, customerID, leadID *uint, deleted bool) *models.Deal {
	t.Helper()
	label = f.next(label)
	d := &models.Deal{Title: "Deal " + label + " for Bt " + f.tag, Stage: models.DealStageLost, Probability: 0, AmountCents: 123400,
		Currency: "EUR", Source: "referral " + label, Notes: "Deal notes " + label + " naming Bt " + f.tag,
		LostReason: "Lost " + label + " because Bt " + f.tag, OwnerID: ownerID, CustomerID: customerID, LeadID: leadID}
	require.NoError(t, f.db.Create(d).Error)
	require.NoError(t, f.db.Create(&models.DealStageChange{DealID: d.ID, ToStage: models.DealStageLost, ChangedByID: ownerID, ChangedAt: time.Now().UTC()}).Error)
	if deleted {
		require.NoError(t, f.db.Delete(&models.Deal{}, d.ID).Error)
	}
	return d
}

func (f *businessTextFixture) product(t *testing.T, label string, deleted bool) *models.Product {
	t.Helper()
	label = f.next(label)
	p := &models.Product{Name: "Product " + label + " " + f.tag, SKU: "BT-" + label + "-" + f.tag, UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(t, f.db.Create(p).Error)
	if deleted {
		require.NoError(t, f.db.Delete(&models.Product{}, p.ID).Error)
	}
	return p
}

func (f *businessTextFixture) quote(t *testing.T, label string, ownerID uint, customerID, dealID *uint, status models.QuoteStatus, deleted bool, products ...*models.Product) *models.Quote {
	t.Helper()
	label = f.next(label)
	now := time.Now().UTC().Truncate(time.Second)
	valid := time.Date(2030, 1, 15, 0, 0, 0, 0, time.Local)
	n := int64(len(products))
	q := &models.Quote{Title: "Quote " + label + " for Bt " + f.tag, Status: status, Currency: "EUR",
		SubtotalCents: 2000 * n, DiscountCents: 100 * n, TotalCents: 1900 * n, ValidUntil: &valid,
		Notes: "Quote notes " + label + " naming Bt " + f.tag, SentAt: &now, ClosedAt: &now, LastEmailedAt: &now,
		DealID: dealID, CustomerID: customerID, OwnerID: ownerID}
	require.NoError(t, f.db.Create(q).Error)
	for i, p := range products {
		require.NoError(t, f.db.Create(&models.QuoteLine{QuoteID: q.ID, ProductID: p.ID, Position: int64(i + 1),
			Description: fmt.Sprintf("Line %d of %s naming Bt %s", i+1, label, f.tag), Quantity: 2, UnitPriceCents: 1000,
			DiscountBP: 500, LineTotalCents: 1900}).Error)
	}
	require.NoError(t, f.db.Create(&models.QuoteStatusChange{QuoteID: q.ID, ToStatus: status, ChangedByID: ownerID, ChangedAt: now}).Error)
	if deleted {
		require.NoError(t, f.db.Delete(&models.Quote{}, q.ID).Error)
	}
	return q
}

func (f *businessTextFixture) dealNotification(t *testing.T, userID, entityID uint, entityType string) *models.Notification {
	t.Helper()
	n := &models.Notification{UserID: userID, Title: "About Bt " + f.tag, Body: "Body naming Bt " + f.tag,
		EntityType: entityType, EntityID: entityID}
	require.NoError(t, f.db.Create(n).Error)
	return n
}

// businessTextValue renders a column value read through a model-free query in
// a form that compares equal across drivers.
func businessTextValue(value interface{}) string {
	if value == nil {
		return "<nil>"
	}
	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return fmt.Sprintf("%v", value)
}

// businessTextRows reads the rows of table with the given ids, soft-deleted
// rows included, keyed by id.
func businessTextRows(t *testing.T, db *gorm.DB, table string, ids ...uint) map[uint]map[string]string {
	t.Helper()
	out := map[uint]map[string]string{}
	if len(ids) == 0 {
		return out
	}
	var rows []map[string]interface{}
	require.NoError(t, db.Table(table).Where("id IN ?", ids).Find(&rows).Error)
	require.Len(t, rows, len(ids), "every %s row is still there", table)
	for _, row := range rows {
		rendered := map[string]string{}
		for column, value := range row {
			rendered[column] = businessTextValue(value)
		}
		var id uint
		_, err := fmt.Sscan(rendered["id"], &id)
		require.NoError(t, err)
		out[id] = rendered
	}
	return out
}

func lineIDsOf(t *testing.T, db *gorm.DB, quoteIDs ...uint) []uint {
	t.Helper()
	var ids []uint
	require.NoError(t, db.Model(&models.QuoteLine{}).Where("quote_id IN ?", quoteIDs).Order("id").Pluck("id", &ids).Error)
	return ids
}

// assertRewritten checks that only the rewritten columns (and updated_at)
// changed, and that they hold the wanted values.
func assertRewritten(t *testing.T, label string, before, after map[string]string, rewritten map[string]string) {
	t.Helper()
	for column, old := range before {
		if want, ok := rewritten[column]; ok {
			assert.Equal(t, want, after[column], "%s: %s", label, column)
			continue
		}
		if column == "updated_at" {
			continue
		}
		assert.Equal(t, old, after[column], "%s keeps %s", label, column)
	}
}

var redactedDeal = map[string]string{"title": "[redacted]", "notes": "", "lost_reason": ""}
var redactedQuote = map[string]string{"title": "[redacted]", "notes": ""}

func assertDealsRedacted(t *testing.T, db *gorm.DB, before map[uint]map[string]string) {
	t.Helper()
	ids := make([]uint, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	after := businessTextRows(t, db, "deals", ids...)
	for id, row := range before {
		assertRewritten(t, fmt.Sprintf("deal %d", id), row, after[id], redactedDeal)
	}
}

// assertQuotesRedacted checks the quotes and every one of their lines.
func assertQuotesRedacted(t *testing.T, db *gorm.DB, before, linesBefore map[uint]map[string]string) {
	t.Helper()
	ids := make([]uint, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	after := businessTextRows(t, db, "quotes", ids...)
	for id, row := range before {
		assertRewritten(t, fmt.Sprintf("quote %d", id), row, after[id], redactedQuote)
	}
	lineIDs := make([]uint, 0, len(linesBefore))
	for id := range linesBefore {
		lineIDs = append(lineIDs, id)
	}
	linesAfter := businessTextRows(t, db, "quote_lines", lineIDs...)
	for id, row := range linesBefore {
		var productID uint
		_, err := fmt.Sscan(row["product_id"], &productID)
		require.NoError(t, err)
		var product models.Product
		require.NoError(t, db.Unscoped().First(&product, productID).Error)
		assertRewritten(t, fmt.Sprintf("quote line %d", id), row, linesAfter[id], map[string]string{"description": product.Name})
	}
}

func assertRowsUnchanged(t *testing.T, db *gorm.DB, table string, before map[uint]map[string]string) {
	t.Helper()
	ids := make([]uint, 0, len(before))
	for id := range before {
		ids = append(ids, id)
	}
	assert.Equal(t, before, businessTextRows(t, db, table, ids...), "%s rows the erasure does not reach are unchanged", table)
}

func idsOfDeals(deals ...*models.Deal) []uint {
	ids := make([]uint, 0, len(deals))
	for _, d := range deals {
		ids = append(ids, d.ID)
	}
	return ids
}

func idsOfQuotes(quotes ...*models.Quote) []uint {
	ids := make([]uint, 0, len(quotes))
	for _, q := range quotes {
		ids = append(ids, q.ID)
	}
	return ids
}

// --- AC: deals of an erased customer ----------------------------------------

func TestCustomerErasureScrubsDealTextSQLite(t *testing.T) {
	runCustomerErasureScrubsDealText(t, false)
}

func TestCustomerErasureScrubsDealTextMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runCustomerErasureScrubsDealText(t, true)
}

func runCustomerErasureScrubsDealText(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	f := newBusinessTextFixture(t, db)
	customer := f.customer(t, "c", nil)
	lead := f.lead(t, "l", nil)
	live := f.deal(t, "live", f.sales.ID, &customer.ID, &lead.ID, false)
	gone := f.deal(t, "gone", f.admin.ID, &customer.ID, nil, true)

	reached := businessTextRows(t, db, "deals", live.ID, gone.ID)
	snapshot := snapshotIdentityDB(t, db)

	require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))

	assertDealsRedacted(t, db, reached)
	after := snapshotIdentityDB(t, db)
	assert.Equal(t, snapshot["deal_stage_changes"], after["deal_stage_changes"], "the stage history is unchanged")
	var stillGone models.Deal
	require.NoError(t, db.Unscoped().First(&stillGone, gone.ID).Error)
	assert.True(t, stillGone.DeletedAt.Valid, "a soft-deleted deal stays soft-deleted")
	var stillLive models.Deal
	require.NoError(t, db.First(&stillLive, live.ID).Error, "a live deal stays live")
	// The lead linked to the deal is a different row and was not erased.
	var keptLead models.Lead
	require.NoError(t, db.First(&keptLead, lead.ID).Error)
	assert.Equal(t, lead.FirstName, keptLead.FirstName)
}

// --- AC: quotes of an erased customer ---------------------------------------

func TestCustomerErasureScrubsQuoteTextSQLite(t *testing.T) {
	runCustomerErasureScrubsQuoteText(t, false)
}

func TestCustomerErasureScrubsQuoteTextMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runCustomerErasureScrubsQuoteText(t, true)
}

func runCustomerErasureScrubsQuoteText(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	f := newBusinessTextFixture(t, db)
	customer := f.customer(t, "c", nil)
	other := f.customer(t, "o", nil)
	deal := f.deal(t, "d", f.sales.ID, &customer.ID, nil, false)
	product := f.product(t, "p", false)
	deletedProduct := f.product(t, "pd", true)

	var quotes []*models.Quote
	for _, status := range models.QuoteStatuses {
		for _, deleted := range []bool{false, true} {
			quotes = append(quotes, f.quote(t, "q"+string(status), f.sales.ID, &customer.ID, nil, status, deleted, product, deletedProduct))
		}
	}
	// Grown from the customer's deal but addressed to another customer.
	quotes = append(quotes, f.quote(t, "viaDeal", f.admin.ID, &other.ID, &deal.ID, models.QuoteStatusSent, false, product))
	// Grown from the deal, with no customer at all.
	quotes = append(quotes, f.quote(t, "dealOnly", f.sales.ID, nil, &deal.ID, models.QuoteStatusDraft, true, deletedProduct))

	ids := idsOfQuotes(quotes...)
	before := businessTextRows(t, db, "quotes", ids...)
	linesBefore := businessTextRows(t, db, "quote_lines", lineIDsOf(t, db, ids...)...)
	require.Len(t, linesBefore, 2*2*len(models.QuoteStatuses)+2)
	snapshot := snapshotIdentityDB(t, db)

	require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))

	assertQuotesRedacted(t, db, before, linesBefore)
	after := snapshotIdentityDB(t, db)
	assert.Equal(t, snapshot["quote_status_changes"], after["quote_status_changes"], "the status history is unchanged")
	assert.Equal(t, snapshot["products"], after["products"], "the catalogue is not touched")
	var otherCustomer models.Customer
	require.NoError(t, db.First(&otherCustomer, other.ID).Error, "the other customer is not erased")
	assert.Equal(t, other.Email, otherCustomer.Email)
}

// --- AC: lead erasure and the cascade --------------------------------------

func TestLeadErasureScrubsDealAndQuoteTextSQLite(t *testing.T) {
	runLeadErasureScrubsDealAndQuoteText(t, false)
}

func TestLeadErasureScrubsDealAndQuoteTextMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runLeadErasureScrubsDealAndQuoteText(t, true)
}

func runLeadErasureScrubsDealAndQuoteText(t *testing.T, useMySQL bool) {
	unconverted := func(t *testing.T, erase func(db *gorm.DB, leadID uint) error) {
		db := openIdentityErasureDB(t, useMySQL)
		f := newBusinessTextFixture(t, db)
		lead := f.lead(t, "l", nil)
		product := f.product(t, "p", false)
		live := f.deal(t, "live", f.sales.ID, nil, &lead.ID, false)
		gone := f.deal(t, "gone", f.sales.ID, nil, &lead.ID, true)
		q1 := f.quote(t, "q1", f.sales.ID, nil, &live.ID, models.QuoteStatusAccepted, false, product)
		q2 := f.quote(t, "q2", f.sales.ID, nil, &gone.ID, models.QuoteStatusDraft, true, product)
		bystanderLead := f.lead(t, "b", nil)
		bystanderDeal := f.deal(t, "bd", f.sales.ID, nil, &bystanderLead.ID, false)
		bystanderQuote := f.quote(t, "bq", f.sales.ID, nil, &bystanderDeal.ID, models.QuoteStatusSent, false, product)

		deals := businessTextRows(t, db, "deals", live.ID, gone.ID)
		quotes := businessTextRows(t, db, "quotes", q1.ID, q2.ID)
		lines := businessTextRows(t, db, "quote_lines", lineIDsOf(t, db, q1.ID, q2.ID)...)
		untouchedDeals := businessTextRows(t, db, "deals", bystanderDeal.ID)
		untouchedQuotes := businessTextRows(t, db, "quotes", bystanderQuote.ID)
		untouchedLines := businessTextRows(t, db, "quote_lines", lineIDsOf(t, db, bystanderQuote.ID)...)

		require.NoError(t, erase(db, lead.ID))

		assertDealsRedacted(t, db, deals)
		assertQuotesRedacted(t, db, quotes, lines)
		assertRowsUnchanged(t, db, "deals", untouchedDeals)
		assertRowsUnchanged(t, db, "quotes", untouchedQuotes)
		assertRowsUnchanged(t, db, "quote_lines", untouchedLines)
	}

	t.Run("admin_erases_an_unconverted_lead", func(t *testing.T) {
		unconverted(t, func(db *gorm.DB, id uint) error { return NewLeadRepository(db).Delete(id) })
	})
	t.Run("non_admin_erases_an_unconverted_lead", func(t *testing.T) {
		unconverted(t, func(db *gorm.DB, id uint) error { return NewLeadRepository(db).DeleteUnconverted(id) })
	})

	// One person as a customer-role login, a customer and two converted leads.
	type graph struct {
		f                *businessTextFixture
		login            *models.User
		customer         *models.Customer
		leadA, leadB     *models.Lead
		deals            []*models.Deal
		quotes           []*models.Quote
		bystanderDeal    *models.Deal
		bystanderQuote   *models.Quote
		bystanderProduct *models.Product
	}
	seedGraph := func(t *testing.T, db *gorm.DB) *graph {
		f := newBusinessTextFixture(t, db)
		g := &graph{f: f}
		g.login = &models.User{Email: "bt-login-" + f.tag + "@person.test", Password: "$2a$10$personhash", FirstName: "BtLogin",
			LastName: "Person" + f.tag, Role: models.RoleCustomer, IsActive: true}
		require.NoError(t, db.Create(g.login).Error)
		g.customer = f.customer(t, "c", &g.login.ID)
		g.leadA = f.lead(t, "la", &g.customer.ID)
		g.leadB = f.lead(t, "lb", &g.customer.ID)
		product := f.product(t, "p", false)
		g.deals = []*models.Deal{
			f.deal(t, "onCustomer", f.sales.ID, &g.customer.ID, nil, false),
			f.deal(t, "onLeadA", f.sales.ID, nil, &g.leadA.ID, false),
			f.deal(t, "onLeadB", f.admin.ID, nil, &g.leadB.ID, true),
		}
		g.quotes = []*models.Quote{
			f.quote(t, "toCustomer", f.sales.ID, &g.customer.ID, nil, models.QuoteStatusRejected, false, product),
			f.quote(t, "onLeadA", f.sales.ID, nil, &g.deals[1].ID, models.QuoteStatusExpired, false, product),
			f.quote(t, "onLeadB", f.sales.ID, nil, &g.deals[2].ID, models.QuoteStatusSent, true, product),
		}
		other := f.customer(t, "other", nil)
		g.bystanderProduct = product
		g.bystanderDeal = f.deal(t, "other", f.sales.ID, &other.ID, nil, false)
		g.bystanderQuote = f.quote(t, "other", f.sales.ID, &other.ID, &g.bystanderDeal.ID, models.QuoteStatusAccepted, false, product)
		return g
	}
	cascade := func(t *testing.T, erase func(db *gorm.DB, g *graph) error) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedGraph(t, db)
		quoteIDs := idsOfQuotes(g.quotes...)
		deals := businessTextRows(t, db, "deals", idsOfDeals(g.deals...)...)
		quotes := businessTextRows(t, db, "quotes", quoteIDs...)
		lines := businessTextRows(t, db, "quote_lines", lineIDsOf(t, db, quoteIDs...)...)
		untouchedDeals := businessTextRows(t, db, "deals", g.bystanderDeal.ID)
		untouchedQuotes := businessTextRows(t, db, "quotes", g.bystanderQuote.ID)

		require.NoError(t, erase(db, g))

		assertDealsRedacted(t, db, deals)
		assertQuotesRedacted(t, db, quotes, lines)
		assertRowsUnchanged(t, db, "deals", untouchedDeals)
		assertRowsUnchanged(t, db, "quotes", untouchedQuotes)
	}

	t.Run("converted_lead_reaches_every_lead_and_customer", func(t *testing.T) {
		cascade(t, func(db *gorm.DB, g *graph) error { return NewLeadRepository(db).Delete(g.leadA.ID) })
	})
	t.Run("customer_role_login_reaches_its_customers_and_their_leads", func(t *testing.T) {
		cascade(t, func(db *gorm.DB, g *graph) error { return NewUserRepository(db).Delete(g.login.ID) })
	})
	t.Run("refused_non_admin_erasure_of_a_converted_lead_changes_nothing", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		g := seedGraph(t, db)
		before := snapshotIdentityDB(t, db)
		err := NewLeadRepository(db).DeleteUnconverted(g.leadA.ID)
		require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin)
		err = db.Transaction(func(tx *gorm.DB) error { return NewLeadRepository(db).WithTx(tx).DeleteUnconverted(g.leadB.ID) })
		require.ErrorIs(t, err, ErrConvertedLeadNeedsAdmin)
		assert.Equal(t, before, snapshotIdentityDB(t, db), "a refused erasure leaves every deal and quote unchanged")
	})
}

// --- AC: rows the graph does not reach --------------------------------------

func TestBusinessTextErasureLeavesUnrelatedRecordsSQLite(t *testing.T) {
	runBusinessTextErasureLeavesUnrelatedRecords(t, false)
}

func TestBusinessTextErasureLeavesUnrelatedRecordsMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runBusinessTextErasureLeavesUnrelatedRecords(t, true)
}

func runBusinessTextErasureLeavesUnrelatedRecords(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	f := newBusinessTextFixture(t, db)
	product := f.product(t, "p", false)

	customer := f.customer(t, "c", nil)
	lead := f.lead(t, "l", nil)
	reachedDeal := f.deal(t, "reached", f.sales.ID, &customer.ID, nil, false)

	// Rows of another customer and another lead.
	otherCustomer := f.customer(t, "oc", nil)
	otherLead := f.lead(t, "ol", nil)
	otherCustomerDeal := f.deal(t, "ocd", f.sales.ID, &otherCustomer.ID, nil, false)
	otherLeadDeal := f.deal(t, "old", f.sales.ID, nil, &otherLead.ID, true)
	otherCustomerQuote := f.quote(t, "ocq", f.sales.ID, &otherCustomer.ID, &otherCustomerDeal.ID, models.QuoteStatusAccepted, false, product)
	otherLeadQuote := f.quote(t, "olq", f.sales.ID, nil, &otherLeadDeal.ID, models.QuoteStatusDraft, false, product)
	// A quote with neither a reached customer_id nor a reached deal_id.
	looseQuote := f.quote(t, "loose", f.sales.ID, &otherCustomer.ID, nil, models.QuoteStatusSent, true, product)

	// Deals and quotes owned by a staff user who is erased: owner_id is never
	// followed.
	owner := seedIdentityStaff(t, db, f.tag+"o", models.RoleSales)
	ownedDeal := f.deal(t, "owned", owner.ID, &otherCustomer.ID, nil, false)
	ownedQuote := f.quote(t, "owned", owner.ID, &otherCustomer.ID, &ownedDeal.ID, models.QuoteStatusSent, false, product)

	// Notifications about unreached deals, addressed to a user who stays.
	notifications := []*models.Notification{
		f.dealNotification(t, f.admin.ID, otherCustomerDeal.ID, models.AutomationEntityDeal),
		f.dealNotification(t, f.admin.ID, otherLeadDeal.ID, models.AutomationEntityDeal),
		f.dealNotification(t, f.admin.ID, ownedDeal.ID, models.AutomationEntityDeal),
	}

	dealIDs := idsOfDeals(otherCustomerDeal, otherLeadDeal, ownedDeal)
	quoteIDs := idsOfQuotes(otherCustomerQuote, otherLeadQuote, looseQuote, ownedQuote)
	notificationIDs := []uint{notifications[0].ID, notifications[1].ID, notifications[2].ID}
	deals := businessTextRows(t, db, "deals", dealIDs...)
	quotes := businessTextRows(t, db, "quotes", quoteIDs...)
	lines := businessTextRows(t, db, "quote_lines", lineIDsOf(t, db, quoteIDs...)...)
	notes := businessTextRows(t, db, "notifications", notificationIDs...)

	require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))
	require.NoError(t, NewLeadRepository(db).Delete(lead.ID))
	require.NoError(t, NewUserRepository(db).Delete(owner.ID))

	assertRowsUnchanged(t, db, "deals", deals)
	assertRowsUnchanged(t, db, "quotes", quotes)
	assertRowsUnchanged(t, db, "quote_lines", lines)
	assertRowsUnchanged(t, db, "notifications", notes)

	// The reached deal is redacted, so the erasures did run.
	var redacted models.Deal
	require.NoError(t, db.First(&redacted, reachedDeal.ID).Error)
	assert.Equal(t, RedactedBusinessTitle, redacted.Title)
}

// --- AC: one unit -----------------------------------------------------------

func TestBusinessTextErasureAtomicSQLite(t *testing.T) { runBusinessTextErasureAtomic(t, false) }

func TestBusinessTextErasureAtomicMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runBusinessTextErasureAtomic(t, true)
}

func runBusinessTextErasureAtomic(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	f := newBusinessTextFixture(t, db)
	customer := f.customer(t, "c", nil)
	product := f.product(t, "p", false)
	deal := f.deal(t, "d", f.sales.ID, &customer.ID, nil, false)
	quote := f.quote(t, "q", f.sales.ID, &customer.ID, &deal.ID, models.QuoteStatusAccepted, false, product)
	notification := f.dealNotification(t, f.admin.ID, deal.ID, models.AutomationEntityDeal)

	var line models.QuoteLine
	require.NoError(t, db.Where("quote_id = ?", quote.ID).First(&line).Error)
	seededText := []string{deal.Title, deal.Notes, deal.LostReason, quote.Title, quote.Notes, line.Description,
		notification.Title, notification.Body, customer.Email, customer.Address, customer.FirstName}
	before := snapshotIdentityDB(t, db)

	// The quote_lines scrub is a Model/Update write, so the failure is
	// injected on the Update processor of this test's own handle.
	const name = "business_text_erasure_test:fail_quote_lines"
	sentinel := errors.New("injected failure scrubbing quote lines")
	var mu sync.Mutex
	calls := 0
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "quote_lines" {
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		_ = tx.AddError(sentinel)
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })

	err := NewCustomerRepository(db).Delete(customer.ID)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, calls, "the failure was injected into the quote_lines scrub")
	assert.Contains(t, err.Error(), "quote_lines")
	for _, text := range seededText {
		assert.NotContains(t, err.Error(), text, "the error names tables and ids only")
	}
	require.NoError(t, db.Callback().Update().Remove(name))
	assert.Equal(t, before, snapshotIdentityDB(t, db), "a failed erasure leaves the customer, deals, quotes, lines and notifications unchanged")

	// A WithTx erasure whose caller rolls back leaves them unchanged too.
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, NewCustomerRepository(db).WithTx(tx).Delete(customer.ID))
	var inside models.Deal
	require.NoError(t, tx.First(&inside, deal.ID).Error)
	require.Equal(t, RedactedBusinessTitle, inside.Title, "the scrub ran inside the caller's transaction")
	require.NoError(t, tx.Rollback().Error)
	assert.Equal(t, before, snapshotIdentityDB(t, db), "a rolled-back caller keeps nothing of the erasure")
}

// --- AC: schemas without the deal tables ------------------------------------

func openBusinessTextSubsetDB(t *testing.T, extra ...interface{}) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subset.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrate := []interface{}{&models.User{}, &models.Customer{}, &models.Lead{}, &models.FormSubmission{},
		&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{}}
	require.NoError(t, db.AutoMigrate(append(migrate, extra...)...))
	return db
}

func TestBusinessTextErasureWithoutDealTables(t *testing.T) {
	absent := func(t *testing.T, db *gorm.DB, tables ...interface{}) {
		t.Helper()
		for _, model := range tables {
			require.False(t, db.Migrator().HasTable(model), "the fixture must not have %T", model)
		}
	}
	seed := func(t *testing.T, db *gorm.DB, tag string) (*models.User, *models.Customer, *models.Lead) {
		t.Helper()
		owner := seedIdentityStaff(t, db, tag, models.RoleSales)
		customer := &models.Customer{FirstName: "Subset" + tag, LastName: "Person", Email: "subset-c-" + tag + "@person.test", Phone: "+1-" + tag}
		require.NoError(t, db.Create(customer).Error)
		lead := &models.Lead{FirstName: "Subset" + tag, LastName: "Lead", Email: "subset-l-" + tag + "@person.test", OwnerID: owner.ID, Status: models.LeadStatusNew}
		require.NoError(t, db.Create(lead).Error)
		return owner, customer, lead
	}
	assertPersonErased := func(t *testing.T, db *gorm.DB, customerID, leadID uint) {
		t.Helper()
		var c models.Customer
		require.NoError(t, db.Unscoped().First(&c, customerID).Error)
		assert.True(t, c.DeletedAt.Valid)
		assert.Empty(t, c.FirstName)
		assert.True(t, strings.HasSuffix(c.Email, "@"+anonymizedEmailDomain))
		var l models.Lead
		require.NoError(t, db.Unscoped().First(&l, leadID).Error)
		assert.True(t, l.DeletedAt.Valid)
		assert.Empty(t, l.FirstName)
		assert.True(t, strings.HasSuffix(l.Email, "@"+anonymizedEmailDomain))
	}

	t.Run("no_deal_quote_product_or_notification_tables", func(t *testing.T) {
		db := openBusinessTextSubsetDB(t)
		absent(t, db, &models.Deal{}, &models.Quote{}, &models.QuoteLine{}, &models.Product{}, &models.Notification{})
		_, customer, lead := seed(t, db, nextIdentityTag())
		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))
		require.NoError(t, NewLeadRepository(db).Delete(lead.ID))
		assertPersonErased(t, db, customer.ID, lead.ID)
	})

	t.Run("deals_without_quote_product_or_notification_tables", func(t *testing.T) {
		db := openBusinessTextSubsetDB(t, &models.Deal{})
		require.True(t, db.Migrator().HasTable(&models.Deal{}))
		absent(t, db, &models.Quote{}, &models.QuoteLine{}, &models.Product{}, &models.Notification{})
		owner, customer, lead := seed(t, db, nextIdentityTag())
		onCustomer := &models.Deal{Title: "Subset customer deal", Notes: "n", LostReason: "r", Currency: "EUR", OwnerID: owner.ID, CustomerID: &customer.ID}
		onLead := &models.Deal{Title: "Subset lead deal", Notes: "n", LostReason: "r", Currency: "EUR", OwnerID: owner.ID, LeadID: &lead.ID}
		untouched := &models.Deal{Title: "Subset other deal", Notes: "n", LostReason: "r", Currency: "EUR", OwnerID: owner.ID}
		for _, d := range []*models.Deal{onCustomer, onLead, untouched} {
			require.NoError(t, db.Create(d).Error)
		}
		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))
		require.NoError(t, NewLeadRepository(db).Delete(lead.ID))
		assertPersonErased(t, db, customer.ID, lead.ID)
		for _, id := range []uint{onCustomer.ID, onLead.ID} {
			var d models.Deal
			require.NoError(t, db.First(&d, id).Error)
			assert.Equal(t, RedactedBusinessTitle, d.Title, "deal %d", id)
			assert.Empty(t, d.Notes, "deal %d", id)
			assert.Empty(t, d.LostReason, "deal %d", id)
		}
		var kept models.Deal
		require.NoError(t, db.First(&kept, untouched.ID).Error)
		assert.Equal(t, "Subset other deal", kept.Title)
	})
}

// --- AC: notifications about reached deals ----------------------------------

func TestBusinessTextErasurePurgesDealNotificationsSQLite(t *testing.T) {
	runBusinessTextErasurePurgesDealNotifications(t, false)
}

func TestBusinessTextErasurePurgesDealNotificationsMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runBusinessTextErasurePurgesDealNotifications(t, true)
}

func runBusinessTextErasurePurgesDealNotifications(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	f := newBusinessTextFixture(t, db)
	customer := f.customer(t, "c", nil)
	lead := f.lead(t, "l", nil)
	other := f.customer(t, "o", nil)
	onCustomer := f.deal(t, "c", f.sales.ID, &customer.ID, nil, false)
	onLead := f.deal(t, "l", f.sales.ID, nil, &lead.ID, true)
	unreached := f.deal(t, "u", f.sales.ID, &other.ID, nil, false)

	purged := []*models.Notification{
		f.dealNotification(t, f.admin.ID, onCustomer.ID, models.AutomationEntityDeal),
		f.dealNotification(t, f.sales.ID, onCustomer.ID, models.AutomationEntityDeal),
		f.dealNotification(t, f.admin.ID, onLead.ID, models.AutomationEntityDeal),
	}
	// Another entity type that happens to carry a reached deal's id; the id
	// must not be the erased lead's, whose own notifications the lead erasure
	// purges.
	require.NotEqual(t, lead.ID, onLead.ID)
	kept := []*models.Notification{
		f.dealNotification(t, f.admin.ID, unreached.ID, models.AutomationEntityDeal),
		f.dealNotification(t, f.admin.ID, onLead.ID, models.AutomationEntityLead),
	}
	keptRows := businessTextRows(t, db, "notifications", kept[0].ID, kept[1].ID)

	// The lead's deal is reached through a lead erasure, the customer's deal
	// through a customer erasure.
	require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))
	for _, n := range purged[:2] {
		assert.Zero(t, countRows(t, db, &models.Notification{}, "id = ?", n.ID), "notification %d about a reached deal is gone", n.ID)
	}
	assert.EqualValues(t, 1, countRows(t, db, &models.Notification{}, "id = ?", purged[2].ID), "the lead's deal is not reached yet")

	require.NoError(t, NewLeadRepository(db).Delete(lead.ID))
	assert.Zero(t, countRows(t, db, &models.Notification{}, "id = ?", purged[2].ID), "a soft-deleted reached deal's notification is gone")
	assertRowsUnchanged(t, db, "notifications", keptRows)
}
