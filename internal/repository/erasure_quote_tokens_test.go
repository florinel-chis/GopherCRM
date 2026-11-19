package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The client response links of an erased customer's quotes (#395).
//
// Erasing a customer, from whichever root reaches it, stamps used_at on the
// unused quote_response_tokens rows of the quotes addressed to that customer,
// inside the erasure unit, and leaves decision NULL. A link mailed before the
// erasure is then refused like a used one while the quote keeps its status.
// Rows the client already spent, the tokens of other customers' quotes and of
// a quote that only shares the erased customer's deal are not touched.
//
// The fixtures run on the full production schema with foreign keys on
// (openIdentityErasureDB); token rows are inserted directly. The MySQL roots
// use a disposable schema on TEST_MYSQL_DSN, skip without it and fail when
// the server is unreachable.

var quoteTokenSeq int

// quoteToken inserts one response token row for the quote.
func (f *businessTextFixture) quoteToken(t *testing.T, quoteID uint, expiresAt, usedAt *time.Time, decision *string) *models.QuoteResponseToken {
	t.Helper()
	quoteTokenSeq++
	token := &models.QuoteResponseToken{QuoteID: quoteID, TokenHash: fmt.Sprintf("hash-%s-%d", f.tag, quoteTokenSeq),
		ExpiresAt: expiresAt, UsedAt: usedAt, Decision: decision}
	require.NoError(t, f.db.Create(token).Error)
	return token
}

// quoteTokenTable is every column of every quote_response_tokens row, keyed
// by id.
func quoteTokenTable(t *testing.T, db *gorm.DB) map[uint]map[string]string {
	t.Helper()
	var ids []uint
	require.NoError(t, db.Model(&models.QuoteResponseToken{}).Order("id").Pluck("id", &ids).Error)
	return businessTextRows(t, db, "quote_response_tokens", ids...)
}

func assertQuoteTokenUsable(t *testing.T, db *gorm.DB, token *models.QuoteResponseToken, why string) {
	t.Helper()
	found, err := NewQuoteResponseTokenRepository(db).FindUsableByHash(token.TokenHash, time.Now())
	require.NoError(t, err, why)
	assert.Equal(t, token.ID, found.ID, why)
	assert.Nil(t, found.UsedAt, why)
	assert.Nil(t, found.Decision, why)
}

// assertQuoteTokenInvalidated checks the stamp, that nothing else on the row
// moved, and that neither the read nor the claim accepts the row any more.
func assertQuoteTokenInvalidated(t *testing.T, db *gorm.DB, before map[string]string, token *models.QuoteResponseToken, why string) {
	t.Helper()
	after := businessTextRows(t, db, "quote_response_tokens", token.ID)[token.ID]
	assert.NotEqual(t, "<nil>", after["used_at"], "%s: used_at is stamped", why)
	assert.Equal(t, "<nil>", after["decision"], "%s: decision stays NULL", why)
	for _, column := range []string{"id", "quote_id", "token_hash", "expires_at", "created_at"} {
		assert.Equal(t, before[column], after[column], "%s keeps %s", why, column)
	}
	repo := NewQuoteResponseTokenRepository(db)
	_, err := repo.FindUsableByHash(token.TokenHash, time.Now())
	assert.Truef(t, errors.Is(err, gorm.ErrRecordNotFound), "%s: the read refuses the link, got %v", why, err)
	err = repo.Claim(token.ID, models.QuoteResponseAccept, time.Now())
	assert.Truef(t, errors.Is(err, gorm.ErrRecordNotFound), "%s: the claim refuses the link, got %v", why, err)
	assert.Equal(t, after, businessTextRows(t, db, "quote_response_tokens", token.ID)[token.ID], "%s: the refused claim wrote nothing", why)
}

func TestErasureInvalidatesQuoteResponseTokensSQLite(t *testing.T) {
	runErasureInvalidatesQuoteResponseTokens(t, false)
}

func TestErasureInvalidatesQuoteResponseTokensMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	runErasureInvalidatesQuoteResponseTokens(t, true)
}

func runErasureInvalidatesQuoteResponseTokens(t *testing.T, useMySQL bool) {
	db := openIdentityErasureDB(t, useMySQL)
	future := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	t.Run("customer root stamps the unused tokens and leaves the quote sent", func(t *testing.T) {
		f := newBusinessTextFixture(t, db)
		customer := f.customer(t, "c", nil)
		product := f.product(t, "p", false)
		open := f.quote(t, "open", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, product)
		dated := f.quote(t, "dated", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, product)
		noExpiry := f.quoteToken(t, open.ID, nil, nil, nil)
		withExpiry := f.quoteToken(t, dated.ID, &future, nil, nil)
		assertQuoteTokenUsable(t, db, noExpiry, "before the erasure")
		assertQuoteTokenUsable(t, db, withExpiry, "before the erasure")

		tokensBefore := businessTextRows(t, db, "quote_response_tokens", noExpiry.ID, withExpiry.ID)
		quotesBefore := businessTextRows(t, db, "quotes", open.ID, dated.ID)
		historyBefore := snapshotIdentityDB(t, db)["quote_status_changes"]

		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))

		assertQuoteTokenInvalidated(t, db, tokensBefore[noExpiry.ID], noExpiry, "token without expiry")
		assertQuoteTokenInvalidated(t, db, tokensBefore[withExpiry.ID], withExpiry, "token with a future expiry")
		quotesAfter := businessTextRows(t, db, "quotes", open.ID, dated.ID)
		for id, row := range quotesBefore {
			// Only the text the scrub owns moved: status, sent_at, closed_at
			// and the totals are among the columns that must be equal.
			assertRewritten(t, fmt.Sprintf("quote %d", id), row, quotesAfter[id], redactedQuote)
			assert.Equal(t, string(models.QuoteStatusSent), quotesAfter[id]["status"])
		}
		assert.Equal(t, historyBefore, snapshotIdentityDB(t, db)["quote_status_changes"], "the status history is unchanged")
	})

	t.Run("spent tokens and the tokens of quotes addressed to others are untouched", func(t *testing.T) {
		f := newBusinessTextFixture(t, db)
		customer := f.customer(t, "c", nil)
		other := f.customer(t, "o", nil)
		deal := f.deal(t, "d", f.sales.ID, &customer.ID, nil, false)
		product := f.product(t, "p", false)
		own := f.quote(t, "own", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, product)
		foreign := f.quote(t, "foreign", f.sales.ID, &other.ID, nil, models.QuoteStatusSent, false, product)
		viaDeal := f.quote(t, "viaDeal", f.sales.ID, &other.ID, &deal.ID, models.QuoteStatusSent, false, product)

		answeredAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		accept := models.QuoteResponseAccept
		spent := f.quoteToken(t, own.ID, nil, &answeredAt, &accept)
		unused := f.quoteToken(t, own.ID, nil, nil, nil)
		foreignToken := f.quoteToken(t, foreign.ID, &future, nil, nil)
		viaDealToken := f.quoteToken(t, viaDeal.ID, nil, nil, nil)

		unusedBefore := businessTextRows(t, db, "quote_response_tokens", unused.ID)[unused.ID]
		kept := businessTextRows(t, db, "quote_response_tokens", spent.ID, foreignToken.ID, viaDealToken.ID)

		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID))

		assertQuoteTokenInvalidated(t, db, unusedBefore, unused, "the erased customer's unused token")
		assertRowsUnchanged(t, db, "quote_response_tokens", kept)
		assertQuoteTokenUsable(t, db, foreignToken, "another customer's quote keeps its link")
		assertQuoteTokenUsable(t, db, viaDealToken, "a quote of the erased customer's deal addressed to another customer keeps its link")
		var row models.QuoteResponseToken
		require.NoError(t, db.First(&row, spent.ID).Error)
		require.NotNil(t, row.Decision)
		assert.Equal(t, models.QuoteResponseAccept, *row.Decision, "the client's answer is kept")
	})

	t.Run("a customer without a sent quote leaves the table as it was", func(t *testing.T) {
		f := newBusinessTextFixture(t, db)
		bare := f.customer(t, "bare", nil)
		drafter := f.customer(t, "drafter", nil)
		product := f.product(t, "p", false)
		f.quote(t, "draft", f.sales.ID, &drafter.ID, nil, models.QuoteStatusDraft, false, product)
		before := quoteTokenTable(t, db)

		require.NoError(t, NewCustomerRepository(db).Delete(bare.ID))
		require.NoError(t, NewCustomerRepository(db).Delete(drafter.ID))

		assert.Equal(t, before, quoteTokenTable(t, db), "no token row was written, stamped or removed")
	})

	t.Run("the converted lead and the customer login reach the same tokens", func(t *testing.T) {
		roots := map[string]func(f *businessTextFixture) (uint, func() error){
			"converted lead": func(f *businessTextFixture) (uint, func() error) {
				customer := f.customer(t, "c", nil)
				lead := f.lead(t, "l", &customer.ID)
				return customer.ID, func() error { return NewLeadRepository(db).Delete(lead.ID) }
			},
			"customer login": func(f *businessTextFixture) (uint, func() error) {
				login := &models.User{Email: fmt.Sprintf("login-%s@person.test", f.tag), Password: "$2a$10$loginhash",
					FirstName: "Bt", LastName: "Login" + f.tag, Role: models.RoleCustomer, IsActive: true}
				require.NoError(t, db.Create(login).Error)
				customer := f.customer(t, "c", &login.ID)
				return customer.ID, func() error { return NewUserRepository(db).Delete(login.ID) }
			},
		}
		for name, build := range roots {
			f := newBusinessTextFixture(t, db)
			customerID, erase := build(f)
			quote := f.quote(t, "q", f.sales.ID, &customerID, nil, models.QuoteStatusSent, false, f.product(t, "p", false))
			token := f.quoteToken(t, quote.ID, &future, nil, nil)
			assertQuoteTokenUsable(t, db, token, name+": before the erasure")
			before := businessTextRows(t, db, "quote_response_tokens", token.ID)[token.ID]

			require.NoError(t, erase(), name)

			assertQuoteTokenInvalidated(t, db, before, token, name)
			var customer models.Customer
			require.NoError(t, db.Unscoped().First(&customer, customerID).Error)
			assert.Truef(t, customer.DeletedAt.Valid, "%s: the customer was erased", name)
		}
	})

	t.Run("a rolled-back erasure hands the link back", func(t *testing.T) {
		f := newBusinessTextFixture(t, db)
		customer := f.customer(t, "c", nil)
		quote := f.quote(t, "q", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, f.product(t, "p", false))
		token := f.quoteToken(t, quote.ID, nil, nil, nil)
		before := quoteTokenTable(t, db)

		tx := db.Begin()
		require.NoError(t, tx.Error)
		require.NoError(t, NewCustomerRepository(db).WithTx(tx).Delete(customer.ID))
		var inside models.QuoteResponseToken
		require.NoError(t, tx.First(&inside, token.ID).Error)
		assert.NotNil(t, inside.UsedAt, "the stamp is part of the caller's transaction")
		require.NoError(t, tx.Rollback().Error)

		assert.Equal(t, before, quoteTokenTable(t, db), "the rollback undid the stamp")
		assertQuoteTokenUsable(t, db, token, "after the rollback")
		var live models.Customer
		require.NoError(t, db.First(&live, customer.ID).Error, "the customer is live again")
		assert.Equal(t, customer.Email, live.Email)
	})
}

// The stamp must not lock through idx_quote_response_tokens_quote_id. Under
// REPEATABLE READ an UPDATE filtered on quote_id locks the gaps around the
// erased customer's quote ids (and, for a quote with no token, the gap where
// its entries would go), and the mint of a link for any other quote in that
// range then waits for the erasure to commit. While the erasure is still
// open, another session inserts a token for a quote between the erased
// customer's two quotes and for one above them; with primary-key writes both
// go through at once.
func TestErasureQuoteTokenWritesLeaveGapsFreeMySQL(t *testing.T) {
	skipBusinessTextWithoutMySQL(t)
	db := openIdentityErasureDB(t, true)
	f := newBusinessTextFixture(t, db)
	customer := f.customer(t, "c", nil)
	other := f.customer(t, "o", nil)
	product := f.product(t, "p", false)
	q1 := f.quote(t, "q1", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, product)
	q2 := f.quote(t, "q2", f.sales.ID, &other.ID, nil, models.QuoteStatusSent, false, product)
	q3 := f.quote(t, "q3", f.sales.ID, &customer.ID, nil, models.QuoteStatusSent, false, product)
	require.Less(t, q1.ID, q2.ID)
	require.Less(t, q2.ID, q3.ID)
	token := f.quoteToken(t, q1.ID, nil, nil, nil)

	// A session of its own for the competing inserts, with a short lock wait:
	// a blocked insert fails with 1205 instead of hanging on the open erasure.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "SET SESSION innodb_lock_wait_timeout = 2")
	require.NoError(t, err)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	// Registered so a failed assertion below does not leave the erasure open
	// and the schema drop waiting on it; a no-op after the commit.
	open := true
	t.Cleanup(func() {
		if open {
			_ = tx.Rollback().Error
		}
	})
	require.NoError(t, NewCustomerRepository(db).WithTx(tx).Delete(customer.ID))
	var stamped models.QuoteResponseToken
	require.NoError(t, tx.First(&stamped, token.ID).Error)
	require.NotNil(t, stamped.UsedAt, "the open erasure stamped the token of Q1")

	insert := func(quoteID uint, label string) {
		t.Helper()
		started := time.Now()
		_, err := conn.ExecContext(ctx,
			"INSERT INTO quote_response_tokens (created_at, updated_at, quote_id, token_hash) VALUES (UTC_TIMESTAMP(), UTC_TIMESTAMP(), ?, ?)",
			quoteID, fmt.Sprintf("gap-%s-%s", label, f.tag))
		var mysqlErr *mysqldriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1205 {
			t.Fatalf("the insert for %s waited out the lock timeout: the open erasure holds a gap of the quote_id index", label)
		}
		require.NoErrorf(t, err, "the insert for %s", label)
		assert.Lessf(t, time.Since(started), 2*time.Second, "the insert for %s did not wait on the erasure", label)
	}
	insert(q2.ID, "a quote between the erased customer's quotes")
	insert(q3.ID+1000, "a quote above the erased customer's quotes")

	require.NoError(t, tx.Commit().Error)
	open = false

	var count int64
	require.NoError(t, db.Model(&models.QuoteResponseToken{}).Where("used_at IS NULL").Count(&count).Error)
	assert.EqualValues(t, 2, count, "the two competing rows are committed and unused")
	var after models.QuoteResponseToken
	require.NoError(t, db.First(&after, token.ID).Error)
	assert.NotNil(t, after.UsedAt, "and the erased customer's token is stamped")
	assert.Nil(t, after.Decision)
}
