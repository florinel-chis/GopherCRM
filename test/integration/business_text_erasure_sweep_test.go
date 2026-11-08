package integration

import (
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Staff-typed text naming an erased person (#245).
//
// Deal and quote text is typed by staff and routinely names the client. The
// erasure of a customer or lead now overwrites the text of the deals and
// quotes linked to the person and hard-deletes the notifications about those
// deals. These sweeps seed the person's identifiers into every one of those
// fields, prove the sweep sees them before the erasure, and then require that
// no identifier survives anywhere in the database.

type namedBusinessText struct {
	deal         *models.Deal
	quote        *models.Quote
	product      *models.Product
	notification *models.Notification
}

// seedNamedDealAndQuote writes a deal linked to the customer and/or lead with
// the person in its title, notes and lost_reason, a quote on it driven through
// sent and accepted by QuoteService with the person in its title, notes and a
// line description, and a notification about the deal naming the person.
func seedNamedDealAndQuote(t *testing.T, db *gorm.DB, subject erasureSubject, owner *models.User, customerID, leadID *uint) namedBusinessText {
	t.Helper()
	deal := &models.Deal{
		Title: "Renewal for " + subject.FirstName, Stage: models.DealStageProposal, Probability: 40, AmountCents: 730000,
		Currency: "EUR", OwnerID: owner.ID, CustomerID: customerID, LeadID: leadID,
		Notes:      "Call " + subject.LastName + " on " + subject.Phone,
		LostReason: "Write to " + subject.Email,
	}
	require.NoError(t, db.Create(deal).Error)

	product := &models.Product{Name: "Fleet maintenance plan", SKU: "FMP-" + fmt.Sprint(deal.ID), UnitPriceCents: 50000, Currency: "EUR", IsActive: true}
	require.NoError(t, db.Create(product).Error)

	quotes := service.NewQuoteService(
		repository.NewQuoteRepository(db), repository.NewProductRepository(db), repository.NewDealRepository(db),
		repository.NewCustomerRepository(db), repository.NewUserRepository(db), nil, nil, utils.NewTransactionManager(db),
	)
	quote := &models.Quote{
		Title: "Offer for " + subject.LastName, Currency: "EUR", DealID: &deal.ID, OwnerID: owner.ID,
		Notes: "Attention " + subject.FirstName + ", " + subject.Email,
	}
	require.NoError(t, quotes.Create(quote, owner.ID, models.RoleSales))
	description := "Plan for " + subject.FirstName + " " + subject.Phone
	_, err := quotes.AddLine(quote.ID, models.QuoteLineInput{ProductID: product.ID, Quantity: 3, DiscountBP: 500, Description: &description})
	require.NoError(t, err)
	_, err = quotes.ChangeStatus(quote.ID, models.QuoteStatusSent, owner.ID)
	require.NoError(t, err)
	_, err = quotes.ChangeStatus(quote.ID, models.QuoteStatusAccepted, owner.ID)
	require.NoError(t, err)

	notification := &models.Notification{
		UserID: owner.ID, Title: "Deal moved: Renewal for " + subject.FirstName, Body: "Ring " + subject.LastName + " on " + subject.Phone,
		EntityType: models.AutomationEntityDeal, EntityID: deal.ID,
	}
	require.NoError(t, db.Create(notification).Error)

	var stored models.Quote
	require.NoError(t, db.First(&stored, quote.ID).Error)
	return namedBusinessText{deal: deal, quote: &stored, product: product, notification: notification}
}

// assertBusinessTextRedacted checks the kept deal and quote hold the redacted
// values, with their stage, amount, status, totals and history unchanged, and
// that the notification about the deal is gone.
func assertBusinessTextRedacted(t *testing.T, db *gorm.DB, seeded namedBusinessText) {
	t.Helper()
	var deal models.Deal
	require.NoError(t, db.Unscoped().First(&deal, seeded.deal.ID).Error)
	assert.Equal(t, "[redacted]", deal.Title)
	assert.Empty(t, deal.Notes)
	assert.Empty(t, deal.LostReason)
	assert.Equal(t, models.DealStageProposal, deal.Stage)
	assert.Equal(t, int64(730000), deal.AmountCents)
	assert.Equal(t, seeded.deal.OwnerID, deal.OwnerID)

	var quote models.Quote
	require.NoError(t, db.Unscoped().Preload("Lines").First(&quote, seeded.quote.ID).Error)
	assert.Equal(t, "[redacted]", quote.Title)
	assert.Empty(t, quote.Notes)
	assert.Equal(t, models.QuoteStatusAccepted, quote.Status)
	assert.Equal(t, seeded.quote.TotalCents, quote.TotalCents)
	assert.Equal(t, seeded.quote.SubtotalCents, quote.SubtotalCents)
	require.Len(t, quote.Lines, 1)
	assert.Equal(t, seeded.product.Name, quote.Lines[0].Description)
	assert.Equal(t, int64(3), quote.Lines[0].Quantity)

	var history int64
	require.NoError(t, db.Model(&models.QuoteStatusChange{}).Where("quote_id = ?", seeded.quote.ID).Count(&history).Error)
	assert.EqualValues(t, 3, history, "create, sent, accepted")

	var notifications int64
	require.NoError(t, db.Model(&models.Notification{}).Where("id = ?", seeded.notification.ID).Count(&notifications).Error)
	assert.Zero(t, notifications, "the notification about the deal is hard-deleted")
}

func requireSweepSeesNamedBusinessText(t *testing.T, db *gorm.DB, subject erasureSubject, personTable string) {
	t.Helper()
	holding := tablesHolding(t, db, subject.identifiers())
	for _, table := range []string{personTable, "deals", "quotes", "quote_lines", "notifications"} {
		require.Contains(t, holding, table, "the sweep must see the person in %s before the erasure", table)
	}
	columns := map[string]bool{}
	for _, hit := range sweepDatabaseFor(t, db, subject.identifiers()) {
		columns[hit.Table+"."+hit.Column] = true
	}
	for _, column := range []string{"deals.title", "deals.notes", "deals.lost_reason", "quotes.title", "quotes.notes",
		"quote_lines.description", "notifications.title", "notifications.body"} {
		require.True(t, columns[column], "the sweep must see the person in %s before the erasure", column)
	}
}

func singleConnection(t *testing.T, db *gorm.DB) {
	t.Helper()
	// The quote service runs transactions; on an in-memory database a second
	// pooled connection would be a second, empty database.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
}

func TestCustomerErasureWithNamedDealAndQuoteTextLeavesNoPersonalData(t *testing.T) {
	db := setupFullSchemaDB(t)
	singleConnection(t, db)
	subject := newErasureSubject("named-deal-customer")
	bystander := newErasureSubject("named-deal-bystander")

	owner := seedLeadOwner(t, db)
	customer := subject.asCustomer(t, db)
	seeded := seedNamedDealAndQuote(t, db, subject, owner, &customer.ID, nil)
	other := bystander.asCustomer(t, db)
	seedNamedDealAndQuote(t, db, bystander, owner, &other.ID, nil)

	requireSweepSeesNamedBusinessText(t, db, subject, "customers")

	require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
	assertBusinessTextRedacted(t, db, seeded)
	// The other customer's deal and quote text is untouched.
	requireSweepSeesNamedBusinessText(t, db, bystander, "customers")
}

func TestLeadErasureWithNamedDealAndQuoteTextLeavesNoPersonalData(t *testing.T) {
	db := setupFullSchemaDB(t)
	singleConnection(t, db)
	subject := newErasureSubject("named-deal-lead")

	owner := seedLeadOwner(t, db)
	lead := subject.asLead(t, db, owner.ID)
	seeded := seedNamedDealAndQuote(t, db, subject, owner, nil, &lead.ID)

	requireSweepSeesNamedBusinessText(t, db, subject, "leads")

	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
	assertBusinessTextRedacted(t, db, seeded)
}
