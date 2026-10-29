package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// quoteFixture is one owner, one customer, one deal and one product the quote
// rows of a test can reference with foreign keys on.
type quoteFixture struct {
	owner    *models.User
	customer *models.Customer
	deal     *models.Deal
	product  *models.Product
}

func seedQuoteFixture(t *testing.T, db *gorm.DB, tag string) quoteFixture {
	t.Helper()
	owner := createDealTestUser(t, db, fmt.Sprintf("quote-owner-%s@example.com", tag))
	customer := &models.Customer{FirstName: "Ada", LastName: "Lovelace", Email: fmt.Sprintf("ada-%s@example.com", tag), Company: "Analytical"}
	require.NoError(t, db.Create(customer).Error)
	deal := createDeal(t, db, &models.Deal{Title: "Engine", Stage: models.DealStageProposal, Probability: 40, OwnerID: owner.ID, CustomerID: &customer.ID})
	product := createTestProduct(t, db, &models.Product{Name: "Widget", SKU: "W-" + tag, UnitPriceCents: 1000, IsActive: true})
	return quoteFixture{owner: owner, customer: customer, deal: deal, product: product}
}

func (f quoteFixture) quote(t *testing.T, repo QuoteRepository, status models.QuoteStatus, title string) *models.Quote {
	t.Helper()
	quote := &models.Quote{Title: title, Status: status, Currency: "EUR", DealID: &f.deal.ID, CustomerID: &f.customer.ID, OwnerID: f.owner.ID}
	require.NoError(t, repo.Create(quote))
	return quote
}

func (f quoteFixture) line(t *testing.T, repo QuoteRepository, quoteID uint, position int64, quantity int64) *models.QuoteLine {
	t.Helper()
	_, total := models.ComputeQuoteLineTotal(quantity, f.product.UnitPriceCents, 0)
	line := &models.QuoteLine{QuoteID: quoteID, ProductID: f.product.ID, Position: position, Description: f.product.Name,
		Quantity: quantity, UnitPriceCents: f.product.UnitPriceCents, LineTotalCents: total}
	require.NoError(t, repo.CreateLine(line))
	return line
}

func (f quoteFixture) change(t *testing.T, repo QuoteRepository, quoteID uint, from *models.QuoteStatus, to models.QuoteStatus, at time.Time) *models.QuoteStatusChange {
	t.Helper()
	change := &models.QuoteStatusChange{QuoteID: quoteID, FromStatus: from, ToStatus: to, ChangedByID: f.owner.ID, ChangedAt: at}
	require.NoError(t, repo.CreateStatusChange(change))
	return change
}

func statusPtr(s models.QuoteStatus) *models.QuoteStatus { return &s }

// ClaimStatus matches only while the stored status is still `from`: the second
// of two identical claims finds nothing, and the stamps are written as given.
func TestQuoteRepository_ClaimStatusIsConditional(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "claim")
	quote := f.quote(t, repo, models.QuoteStatusDraft, "Claim")
	now := time.Now().UTC().Truncate(time.Millisecond)

	rows, err := repo.ClaimStatus(quote.ID, models.QuoteStatusDraft, models.QuoteStatusSent, QuoteStatusStamps{SentAt: &now})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.ClaimStatus(quote.ID, models.QuoteStatusDraft, models.QuoteStatusSent, QuoteStatusStamps{SentAt: &now})
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "the row is no longer a draft")

	stored, err := repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QuoteStatusSent, stored.Status)
	require.NotNil(t, stored.SentAt)
	assert.Nil(t, stored.ClosedAt)

	later := now.Add(time.Minute)
	rows, err = repo.ClaimStatus(quote.ID, models.QuoteStatusSent, models.QuoteStatusAccepted, QuoteStatusStamps{SentAt: stored.SentAt, ClosedAt: &later})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	stored, err = repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QuoteStatusAccepted, stored.Status)
	require.NotNil(t, stored.ClosedAt)
	assert.NotNil(t, stored.SentAt, "the send stamp is kept through acceptance")

	// Back to draft clears both stamps (nil is written, not skipped).
	require.NoError(t, db.Model(&models.Quote{}).Where("id = ?", quote.ID).Update("status", models.QuoteStatusSent).Error)
	rows, err = repo.ClaimStatus(quote.ID, models.QuoteStatusSent, models.QuoteStatusDraft, QuoteStatusStamps{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	stored, err = repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.SentAt)
	assert.Nil(t, stored.ClosedAt)
}

// UpdateDraft and UpdateTotals write only while the status is draft, and
// neither touches the status, the totals of the other, or the stamps.
func TestQuoteRepository_DraftWritesAreConditional(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "draft")
	quote := f.quote(t, repo, models.QuoteStatusDraft, "Draft")

	rows, err := repo.UpdateTotals(quote.ID, 2000, 250, 1750)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	rows, err = repo.UpdateTotals(quote.ID, 2000, 250, 1750)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows, "writing the values the row already holds still matches the row")

	quote.Title = "Renamed"
	quote.Notes = "n"
	quote.SubtotalCents, quote.TotalCents = 1, 1 // must not be written by UpdateDraft
	quote.Status = models.QuoteStatusAccepted    // nor this
	rows, err = repo.UpdateDraft(quote)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	stored, err := repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", stored.Title)
	assert.Equal(t, models.QuoteStatusDraft, stored.Status, "UpdateDraft never writes the status")
	assert.Equal(t, int64(2000), stored.SubtotalCents, "UpdateDraft never writes the totals")
	assert.Equal(t, int64(1750), stored.TotalCents)

	now := time.Now().UTC()
	rows, err = repo.ClaimStatus(quote.ID, models.QuoteStatusDraft, models.QuoteStatusSent, QuoteStatusStamps{SentAt: &now})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	rows, err = repo.UpdateTotals(quote.ID, 1, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "a sent quote's totals are locked")
	quote.Title = "Changed after send"
	rows, err = repo.UpdateDraft(quote)
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "a sent quote's fields are locked")
	stored, err = repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", stored.Title)
	assert.Equal(t, int64(1750), stored.TotalCents)
}

// StampEmailed matches while the quote is sent or accepted and no history row
// moved it to draft after the marker the sender read; a revision followed by a
// fresh send still refuses the stale marker.
func TestQuoteRepository_StampEmailedRefusesANewerRevisionToDraft(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "stamp")
	quote := f.quote(t, repo, models.QuoteStatusSent, "Stamp")
	base := time.Now().UTC().Truncate(time.Second)
	f.change(t, repo, quote.ID, nil, models.QuoteStatusDraft, base)
	sent := f.change(t, repo, quote.ID, statusPtr(models.QuoteStatusDraft), models.QuoteStatusSent, base.Add(time.Second))

	marker, err := repo.LatestStatusChangeID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, sent.ID, marker)

	rows, err := repo.StampEmailed(quote.ID, marker, base.Add(2*time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	stored, err := repo.GetByID(quote.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.LastEmailedAt)
	first := *stored.LastEmailedAt

	// A revision to draft and a fresh send after the marker: the status is
	// sent again, but the stale marker must not stamp.
	f.change(t, repo, quote.ID, statusPtr(models.QuoteStatusSent), models.QuoteStatusDraft, base.Add(3*time.Second))
	resent := f.change(t, repo, quote.ID, statusPtr(models.QuoteStatusDraft), models.QuoteStatusSent, base.Add(4*time.Second))
	rows, err = repo.StampEmailed(quote.ID, marker, base.Add(5*time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 0, rows, "a newer draft row defeats the stale marker")
	stored, err = repo.GetByID(quote.ID)
	require.NoError(t, err)
	assert.True(t, stored.LastEmailedAt.Equal(first), "last_emailed_at is unchanged")

	rows, err = repo.StampEmailed(quote.ID, resent.ID, base.Add(6*time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows, "the fresh marker stamps")

	// An accepted quote stamps; a draft, rejected or expired one does not.
	for status, want := range map[models.QuoteStatus]int64{
		models.QuoteStatusAccepted: 1,
		models.QuoteStatusDraft:    0,
		models.QuoteStatusRejected: 0,
		models.QuoteStatusExpired:  0,
	} {
		require.NoError(t, db.Model(&models.Quote{}).Where("id = ?", quote.ID).Update("status", status).Error)
		rows, err = repo.StampEmailed(quote.ID, resent.ID, base.Add(7*time.Second))
		require.NoError(t, err)
		assert.EqualValuesf(t, want, rows, "status %s", status)
	}

	// A quote with no history at all: the marker is 0.
	bare := f.quote(t, repo, models.QuoteStatusSent, "Bare")
	marker, err = repo.LatestStatusChangeID(bare.ID)
	require.NoError(t, err)
	assert.Zero(t, marker)
}

// A line is addressed by its quote: another quote's line is a not-found on
// read and on delete, lines list in position order and the next position is
// max+1.
func TestQuoteRepository_LinesBelongToTheirQuote(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "lines")
	first := f.quote(t, repo, models.QuoteStatusDraft, "First")
	second := f.quote(t, repo, models.QuoteStatusDraft, "Second")

	position, err := repo.NextLinePosition(first.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, position, "an empty quote starts at 1")
	a := f.line(t, repo, first.ID, 2, 1)
	b := f.line(t, repo, first.ID, 1, 3)
	position, err = repo.NextLinePosition(first.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, position)

	lines, err := repo.ListLines(first.ID)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, []uint{b.ID, a.ID}, []uint{lines[0].ID, lines[1].ID}, "ordered by position")
	count, err := repo.CountLines(first.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	count, err = repo.CountLinesForUpdate(second.ID)
	require.NoError(t, err)
	assert.Zero(t, count)

	_, err = repo.GetLine(second.ID, a.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "a line of another quote is not found: %v", err)
	err = repo.DeleteLine(second.ID, a.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "nor deletable: %v", err)
	count, err = repo.CountLines(first.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "the line is still there")

	line, err := repo.GetLine(first.ID, a.ID)
	require.NoError(t, err)
	line.Quantity, line.DiscountBP = 4, 1250
	_, line.LineTotalCents = models.ComputeQuoteLineTotal(line.Quantity, line.UnitPriceCents, line.DiscountBP)
	require.NoError(t, repo.UpdateLine(line))
	line.DiscountBP = 0
	require.NoError(t, repo.UpdateLine(line), "Save writes a zero discount back")
	stored, err := repo.GetLine(first.ID, a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 4, stored.Quantity)
	assert.Zero(t, stored.DiscountBP)

	require.NoError(t, repo.DeleteLine(first.ID, a.ID))
	assert.True(t, errors.Is(repo.DeleteLine(first.ID, a.ID), gorm.ErrRecordNotFound), "lines are hard-deleted, so a second delete finds nothing")
	lines, err = repo.ListLines(first.ID)
	require.NoError(t, err)
	assert.Len(t, lines, 1)

	// The quote's lines ride along on GetByID, in position order, with the
	// summaries preloaded.
	loaded, err := repo.GetByID(first.ID)
	require.NoError(t, err)
	assert.Len(t, loaded.Lines, 1)
	require.NotNil(t, loaded.Owner)
	require.NotNil(t, loaded.Customer)
	require.NotNil(t, loaded.Deal)
	assert.Equal(t, "Engine", loaded.Deal.Title)
}

// The history lists oldest first with the acting user, and the latest id is
// the marker the email stamp compares against.
func TestQuoteRepository_StatusChangesOldestFirstAndLatestID(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "history")
	quote := f.quote(t, repo, models.QuoteStatusSent, "History")
	base := time.Now().UTC().Truncate(time.Second)

	// Inserted out of chronological order on purpose.
	middle := f.change(t, repo, quote.ID, statusPtr(models.QuoteStatusDraft), models.QuoteStatusSent, base.Add(time.Second))
	oldest := f.change(t, repo, quote.ID, nil, models.QuoteStatusDraft, base)
	newest := f.change(t, repo, quote.ID, statusPtr(models.QuoteStatusSent), models.QuoteStatusAccepted, base.Add(2*time.Second))
	other := f.quote(t, repo, models.QuoteStatusDraft, "Other")
	f.change(t, repo, other.ID, nil, models.QuoteStatusDraft, base.Add(3*time.Second))

	changes, err := repo.ListStatusChanges(quote.ID)
	require.NoError(t, err)
	require.Len(t, changes, 3, "only this quote's rows")
	assert.Equal(t, []uint{oldest.ID, middle.ID, newest.ID}, []uint{changes[0].ID, changes[1].ID, changes[2].ID})
	assert.Nil(t, changes[0].FromStatus)
	require.NotNil(t, changes[0].ChangedBy, "the acting user is preloaded")
	assert.Equal(t, f.owner.ID, changes[0].ChangedBy.ID)

	latest, err := repo.LatestStatusChangeID(quote.ID)
	require.NoError(t, err)
	assert.Equal(t, newest.ID, latest, "the highest id, not the latest changed_at")
}

// The owner predicate is part of the query, so a page of one still reports
// the owner's whole total; the other filters narrow the same way; the sort
// column is allowlisted; a deleted quote vanishes from reads while its lines
// and history stay in their tables.
func TestQuoteRepository_ListScopesOwnerBeforePaginationAndDeleteIsSoft(t *testing.T) {
	db := setupQuoteRepoDB(t)
	repo := NewQuoteRepository(db)
	f := seedQuoteFixture(t, db, "list")
	otherOwner := createDealTestUser(t, db, "quote-other-list@example.com")
	otherCustomer := &models.Customer{FirstName: "Bob", LastName: "Builder", Email: "bob-list@example.com"}
	require.NoError(t, db.Create(otherCustomer).Error)

	for i := 0; i < 3; i++ {
		f.quote(t, repo, models.QuoteStatusDraft, fmt.Sprintf("Mine %d", i))
	}
	sent := f.quote(t, repo, models.QuoteStatusSent, "Mine sent")
	theirs := &models.Quote{Title: "Theirs", Status: models.QuoteStatusDraft, Currency: "EUR", CustomerID: &otherCustomer.ID, OwnerID: otherOwner.ID}
	require.NoError(t, repo.Create(theirs))

	quotes, total, err := repo.List(0, 1, QuoteListFilter{OwnerID: &f.owner.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total, "the total counts the owner's quotes, not the page")
	require.Len(t, quotes, 1)
	assert.Equal(t, f.owner.ID, quotes[0].OwnerID)
	assert.Nil(t, quotes[0].Lines, "lists carry no lines")
	require.NotNil(t, quotes[0].Owner, "but the summaries are preloaded")

	_, total, err = repo.List(0, 100, QuoteListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 5, total, "an admin's unscoped list")

	quotes, _, err = repo.List(0, 100, QuoteListFilter{Status: models.QuoteStatusSent})
	require.NoError(t, err)
	require.Len(t, quotes, 1)
	assert.Equal(t, sent.ID, quotes[0].ID)
	quotes, _, err = repo.List(0, 100, QuoteListFilter{DealID: &f.deal.ID, OwnerID: &otherOwner.ID})
	require.NoError(t, err)
	assert.Empty(t, quotes, "the deal filter and the owner filter combine")
	quotes, _, err = repo.List(0, 100, QuoteListFilter{CustomerID: &otherCustomer.ID})
	require.NoError(t, err)
	require.Len(t, quotes, 1)
	assert.Equal(t, "Theirs", quotes[0].Title)

	titles := func(sortBy, order string) []string {
		quotes, _, err := repo.List(0, 100, QuoteListFilter{OwnerID: &f.owner.ID, SortBy: sortBy, SortOrder: order})
		require.NoError(t, err)
		out := make([]string, 0, len(quotes))
		for _, quote := range quotes {
			out = append(out, quote.Title)
		}
		return out
	}
	assert.Equal(t, []string{"Mine 0", "Mine 1", "Mine 2", "Mine sent"}, titles("title", "asc"))
	assert.Equal(t, []string{"Mine sent", "Mine 2", "Mine 1", "Mine 0"}, titles("id", "desc"))
	for _, column := range []string{"owner_id", "subtotal_cents", "title; DROP TABLE quotes;--"} {
		_, _, err := repo.List(0, 10, QuoteListFilter{SortBy: column})
		assert.Errorf(t, err, "%q must be refused", column)
	}

	// Soft delete: gone from reads, lines and history kept.
	line := f.line(t, repo, sent.ID, 1, 2)
	f.change(t, repo, sent.ID, nil, models.QuoteStatusDraft, time.Now().UTC())
	require.NoError(t, repo.Delete(sent.ID))
	assert.True(t, errors.Is(repo.Delete(sent.ID), gorm.ErrRecordNotFound))
	_, err = repo.GetByID(sent.ID)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	_, total, err = repo.List(0, 100, QuoteListFilter{OwnerID: &f.owner.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	var lineCount, historyCount int64
	require.NoError(t, db.Model(&models.QuoteLine{}).Where("id = ?", line.ID).Count(&lineCount).Error)
	require.NoError(t, db.Model(&models.QuoteStatusChange{}).Where("quote_id = ?", sent.ID).Count(&historyCount).Error)
	assert.EqualValues(t, 1, lineCount, "the lines stay in their table")
	assert.EqualValues(t, 1, historyCount, "and so does the history")
	var unscoped models.Quote
	require.NoError(t, db.Unscoped().First(&unscoped, sent.ID).Error)
	assert.NotNil(t, unscoped.DeletedAt)
}

// sqlRecorder keeps every statement GORM renders, so the locking reads can
// be checked for the clause the dialector emits.
type sqlRecorder struct {
	logger.Interface
	statements []string
}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.statements = append(r.statements, sql)
}

func (r *sqlRecorder) matching(fragment string) []string {
	var out []string
	for _, statement := range r.statements {
		if strings.Contains(statement, fragment) {
			out = append(out, statement)
		}
	}
	return out
}

// The locking reads carry FOR UPDATE on MySQL and nothing on SQLite, whose
// dialector drops the clause; the plain reads carry it on neither. Rendered
// in dry-run mode, so no server is needed.
func TestQuoteRepository_LockingReadsCarryForUpdateOnMySQLOnly(t *testing.T) {
	render := func(t *testing.T, dialector gorm.Dialector) *sqlRecorder {
		t.Helper()
		recorder := &sqlRecorder{Interface: logger.Discard}
		// No ping: the MySQL DSN points nowhere, and nothing is executed.
		db, err := gorm.Open(dialector, &gorm.Config{DryRun: true, Logger: recorder, DisableAutomaticPing: true})
		require.NoError(t, err)
		repo := NewQuoteRepository(db)
		_, _ = repo.GetByIDForUpdate(7)
		_, _ = repo.CountLinesForUpdate(7)
		_, _ = repo.CountLines(7)
		_, _ = repo.GetByID(7)
		_, _ = repo.ListLines(7)
		return recorder
	}

	mysqlSQL := render(t, gormmysql.New(gormmysql.Config{DSN: "nobody:nothing@tcp(127.0.0.1:1)/dryrun?parseTime=true", SkipInitializeWithVersion: true}))
	locking := mysqlSQL.matching("FOR UPDATE")
	require.Len(t, locking, 2, "exactly the two locking reads carry FOR UPDATE on MySQL:\n%s", strings.Join(mysqlSQL.statements, "\n"))
	assert.Contains(t, locking[0], "FROM `quotes`")
	assert.Contains(t, locking[1], "FROM `quote_lines`")
	assert.Contains(t, locking[1], "count(*)")
	for _, statement := range mysqlSQL.statements {
		if strings.Contains(statement, "FOR UPDATE") {
			continue
		}
		assert.NotContains(t, statement, "FOR", "a plain read must not lock: %s", statement)
	}

	sqliteSQL := render(t, sqlite.Open("file:quotes_dryrun?mode=memory&cache=shared"))
	assert.Empty(t, sqliteSQL.matching("FOR UPDATE"), "the SQLite dialector drops the clause:\n%s", strings.Join(sqliteSQL.statements, "\n"))
	assert.NotEmpty(t, sqliteSQL.matching("FROM `quote_lines`"), "the reads were rendered")
}
