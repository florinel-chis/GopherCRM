package repository

// Business text of an erased person (#245).
//
// Deals and quotes are business records: the rows, their links, stages,
// amounts, statuses, totals and history survive the erasure of the customer
// or lead they belong to. Their free text does not. A deal's title, notes and
// lost_reason, a quote's title and notes and each quote line's description
// are typed by staff and routinely name the client ("Renewal for Ada
// Lovelace", "call her on +44 ..."). There is no reliable way to tell a name
// from content, so the reached fields are overwritten wholesale:
//
//   - deals whose customer_id is an erased customer or whose lead_id is an
//     erased lead: title "[redacted]", notes and lost_reason "";
//   - quotes whose customer_id is an erased customer, or whose deal_id is a
//     deal scrubbed above (whatever customer they name): title "[redacted]",
//     notes "";
//   - every line of those quotes: description = the name of its product
//     (soft-deleted products included), the text the line had when it was
//     added unless staff overrode it;
//   - notifications about those deals (entity_type "deal"), whose rendered
//     title or body may carry deal.title: hard-deleted, whatever the
//     recipient.
//
// And one thing that is not text (#395): the unused client response links of
// the quotes ADDRESSED to an erased customer are killed. Every
// quote_response_tokens row of such a quote with used_at NULL gets used_at
// stamped and keeps decision NULL, exactly as a newer email or a staff status
// change invalidates a link, so a link mailed before the erasure is refused
// like a used or expired one and the quote stays in its status. A quote
// reached only through a scrubbed deal and addressed to another customer
// keeps its link: it was mailed to that other customer. The rows themselves
// stay (a hash, ids and timestamps, no personal data).
//
// Every row is reached live or soft-deleted, in every status, and keeps its
// own deletion state. owner_id is never followed: erasing a staff user leaves
// the deals and quotes they own alone.
//
// The step runs once per erasure unit, after the whole identity graph has been
// traversed, so it sees every customer and lead the cascade reached whichever
// entry point (customer, lead, customer-role login) it was entered from. A
// failure propagates and rolls the unit back. A table absent from the schema
// (fixtures that migrate a subset) is skipped through tx.Migrator().HasTable;
// errors name tables and ids only.

import (
	"fmt"
	"sort"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// RedactedBusinessTitle replaces the title of a deal or quote reached by an
// erasure.
const RedactedBusinessTitle = "[redacted]"

// sortedIDs returns the keys of a visited set in ascending order, so the
// statements of the step are deterministic.
func sortedIDs(set map[uint]struct{}) []uint {
	ids := make([]uint, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// scrubBusinessText overwrites the staff-typed text of the deals and quotes
// linked to the customers and leads the cascade erased. See the file comment.
func (c *erasureCascade) scrubBusinessText(tx *gorm.DB) error {
	customerIDs := sortedIDs(c.customers)
	leadIDs := sortedIDs(c.leads)
	if len(customerIDs) == 0 && len(leadIDs) == 0 {
		return nil
	}

	dealIDs, err := scrubReachedDeals(tx, customerIDs, leadIDs)
	if err != nil {
		return err
	}
	if err := purgeDealNotifications(tx, dealIDs); err != nil {
		return err
	}
	quoteIDs, err := scrubReachedQuotes(tx, customerIDs, dealIDs)
	if err != nil {
		return err
	}
	if err := scrubQuoteLines(tx, quoteIDs); err != nil {
		return err
	}
	return invalidateErasedCustomerQuoteLinks(tx, customerIDs, quoteIDs)
}

// invalidateErasedCustomerQuoteLinks stamps the unused response tokens of the
// quotes addressed to an erased customer (#395). scrubbedQuoteIDs are the
// quotes scrubReachedQuotes just updated by primary key, so the unit already
// holds the lock on each of those quotes rows (quotes → quote_response_tokens,
// the order of every other writer of the table); the quotes addressed to an
// erased customer are the subset read here. The tokens are read with a plain
// SELECT and stamped by primary key: no statement locks through the quote_id
// index, so no gap is held against the mint of another quote.
//
// The reads answer from the snapshot of the erasure's transaction. A token
// minted after that snapshot (a quote email in flight) is not seen and not
// stamped; a token the client spent after it is read as unused and left alone
// by the used_at IS NULL predicate of the stamp.
func invalidateErasedCustomerQuoteLinks(tx *gorm.DB, customerIDs, scrubbedQuoteIDs []uint) error {
	if len(customerIDs) == 0 || len(scrubbedQuoteIDs) == 0 || !tx.Migrator().HasTable(&models.QuoteResponseToken{}) {
		return nil
	}
	var quoteIDs []uint
	err := tx.Unscoped().Model(&models.Quote{}).
		Where("id IN ? AND customer_id IN ?", scrubbedQuoteIDs, customerIDs).
		Order("id").Pluck("id", &quoteIDs).Error
	if err != nil {
		return fmt.Errorf("listing quotes %v addressed to erased customers %v: %w", scrubbedQuoteIDs, customerIDs, err)
	}
	tokenIDs, err := invalidateQuoteResponseTokens(tx, quoteIDs, time.Now())
	if err != nil {
		return fmt.Errorf("invalidating quote_response_tokens %v of quotes %v: %w", tokenIDs, quoteIDs, err)
	}
	return nil
}

// scrubReachedDeals redacts every deal linked to an erased customer or lead and
// returns their ids.
func scrubReachedDeals(tx *gorm.DB, customerIDs, leadIDs []uint) ([]uint, error) {
	if !tx.Migrator().HasTable(&models.Deal{}) {
		return nil, nil
	}
	query := tx.Unscoped().Model(&models.Deal{})
	switch {
	case len(customerIDs) > 0 && len(leadIDs) > 0:
		query = query.Where("customer_id IN ? OR lead_id IN ?", customerIDs, leadIDs)
	case len(customerIDs) > 0:
		query = query.Where("customer_id IN ?", customerIDs)
	default:
		query = query.Where("lead_id IN ?", leadIDs)
	}
	var dealIDs []uint
	if err := query.Order("id").Pluck("id", &dealIDs).Error; err != nil {
		return nil, fmt.Errorf("listing deals of erased customers %v and leads %v: %w", customerIDs, leadIDs, err)
	}
	if len(dealIDs) == 0 {
		return nil, nil
	}
	err := tx.Unscoped().Model(&models.Deal{}).Where("id IN ?", dealIDs).
		Updates(map[string]interface{}{
			"title":       RedactedBusinessTitle,
			"notes":       "",
			"lost_reason": "",
		}).Error
	if err != nil {
		return nil, fmt.Errorf("scrubbing deals %v: %w", dealIDs, err)
	}
	return dealIDs, nil
}

// purgeDealNotifications hard-deletes the notifications about the scrubbed
// deals: an automation step renders deal.title into them.
func purgeDealNotifications(tx *gorm.DB, dealIDs []uint) error {
	if len(dealIDs) == 0 || !tx.Migrator().HasTable(&models.Notification{}) {
		return nil
	}
	if err := tx.Unscoped().Where("entity_type = ? AND entity_id IN ?", models.AutomationEntityDeal, dealIDs).
		Delete(&models.Notification{}).Error; err != nil {
		return fmt.Errorf("purging notifications of deals %v: %w", dealIDs, err)
	}
	return nil
}

// scrubReachedQuotes redacts every quote addressed to an erased customer or
// grown from a scrubbed deal, and returns their ids.
func scrubReachedQuotes(tx *gorm.DB, customerIDs, dealIDs []uint) ([]uint, error) {
	if (len(customerIDs) == 0 && len(dealIDs) == 0) || !tx.Migrator().HasTable(&models.Quote{}) {
		return nil, nil
	}
	query := tx.Unscoped().Model(&models.Quote{})
	switch {
	case len(customerIDs) > 0 && len(dealIDs) > 0:
		query = query.Where("customer_id IN ? OR deal_id IN ?", customerIDs, dealIDs)
	case len(customerIDs) > 0:
		query = query.Where("customer_id IN ?", customerIDs)
	default:
		query = query.Where("deal_id IN ?", dealIDs)
	}
	var quoteIDs []uint
	if err := query.Order("id").Pluck("id", &quoteIDs).Error; err != nil {
		return nil, fmt.Errorf("listing quotes of erased customers %v and deals %v: %w", customerIDs, dealIDs, err)
	}
	if len(quoteIDs) == 0 {
		return nil, nil
	}
	err := tx.Unscoped().Model(&models.Quote{}).Where("id IN ?", quoteIDs).
		Updates(map[string]interface{}{
			"title": RedactedBusinessTitle,
			"notes": "",
		}).Error
	if err != nil {
		return nil, fmt.Errorf("scrubbing quotes %v: %w", quoteIDs, err)
	}
	return quoteIDs, nil
}

// scrubQuoteLines resets the description of every line of the scrubbed quotes
// to the name of its product. products is read without a deleted_at filter,
// so a line priced from a since-deleted product gets that product's name too;
// product_id is NOT NULL with a foreign key, so every line has one.
func scrubQuoteLines(tx *gorm.DB, quoteIDs []uint) error {
	if len(quoteIDs) == 0 || !tx.Migrator().HasTable(&models.QuoteLine{}) {
		return nil
	}
	err := tx.Model(&models.QuoteLine{}).Where("quote_id IN ?", quoteIDs).
		Update("description", gorm.Expr("(SELECT products.name FROM products WHERE products.id = quote_lines.product_id)")).Error
	if err != nil {
		return fmt.Errorf("scrubbing quote_lines of quotes %v: %w", quoteIDs, err)
	}
	return nil
}
