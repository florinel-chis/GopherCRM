package service

import (
	"fmt"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// QuoteEmailSubject is the fixed subject of a quote email: no request value
// reaches a header, and the mailer refuses CR and LF regardless.
func QuoteEmailSubject(quoteID uint) string {
	return fmt.Sprintf("Quote %s from GopherCRM", models.QuoteNumber(quoteID))
}

// Email is the flow of the design. The quote and the latest history id are
// read first; the recipient is the linked customer's address as it is now,
// never a copy; the mail goes out with no transaction held; then one
// conditional update stamps last_emailed_at, and matches nothing if a
// revision to draft committed in between. The logs carry the quote id and the
// customer id only: never the address, the name or the body.
func (s *quoteService) Email(id uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "Email")

	quote, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if quote.Status != models.QuoteStatusSent && quote.Status != models.QuoteStatusAccepted {
		err := fmt.Errorf("quote %d is %s: %w", id, quote.Status, apperrors.ErrQuoteNotSent)
		logger.WithError(utils.LogError(err)).Warn("Quote email refused")
		return nil, err
	}
	if quote.CustomerID == nil {
		err := fmt.Errorf("quote %d has no customer: %w", id, apperrors.ErrQuoteNoRecipient)
		logger.WithError(utils.LogError(err)).Warn("Quote email refused")
		return nil, err
	}
	logger = logger.WithField("customer_id", *quote.CustomerID)

	// A live read: an erased customer is soft-deleted and so not found, and an
	// address changed since the quote was written is the one that is used.
	customer, err := s.customerRepo.GetByID(*quote.CustomerID)
	if err != nil {
		if isNotFound(err) {
			err := fmt.Errorf("customer %d of quote %d is gone: %w", *quote.CustomerID, id, apperrors.ErrQuoteNoRecipient)
			logger.WithError(utils.LogError(err)).Warn("Quote email refused")
			return nil, err
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	to := strings.TrimSpace(customer.Email)
	if to == "" {
		err := fmt.Errorf("customer %d of quote %d has no email address: %w", customer.ID, id, apperrors.ErrQuoteNoRecipient)
		logger.WithError(utils.LogError(err)).Warn("Quote email refused")
		return nil, err
	}

	// The marker read before the send: a sent→draft row with a higher id
	// means the quote was revised while the mail was in flight.
	latestHistoryID, err := s.quoteRepo.LatestStatusChangeID(id)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	if s.mailer == nil {
		err := fmt.Errorf("no mailer is configured: %w", apperrors.ErrMailDelivery)
		logger.WithError(utils.LogError(err)).Error("Quote email failed")
		return nil, err
	}
	if err := s.mailer.Send(to, QuoteEmailSubject(quote.ID), RenderQuoteEmail(quote)); err != nil {
		// The mailer's own error is kept for errors.Is but not logged here:
		// a transport error may quote the address.
		logger.Error("Quote email failed: mail delivery error")
		return nil, fmt.Errorf("quote %d: %w: %w", id, apperrors.ErrMailDelivery, err)
	}

	rows, err := s.quoteRepo.StampEmailed(id, latestHistoryID, s.stamp())
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if rows != 1 {
		err := fmt.Errorf("quote %d was revised during the send: %w", id, apperrors.ErrQuoteChangedDuringSend)
		logger.WithError(utils.LogError(err)).Warn("Quote email not recorded")
		return nil, err
	}

	logger.Info("Quote emailed")
	return s.GetByID(id)
}

// RenderQuoteEmail is the plaintext body: the number, the title, every line
// with its description, quantity, unit price, discount and total, the three
// totals with the currency, the validity date and the notes. It is a rendering
// of the quote, not the printable document, and it carries no personal data of
// its own: the recipient is only the envelope.
func RenderQuoteEmail(quote *models.Quote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Quote %s\n", quote.Number())
	fmt.Fprintf(&b, "Title: %s\n", quote.Title)
	fmt.Fprintf(&b, "Currency: %s\n", quote.Currency)
	if quote.ValidUntil != nil {
		fmt.Fprintf(&b, "Valid until: %s\n", *models.FormatDealDate(quote.ValidUntil))
	} else {
		b.WriteString("Valid until: not set\n")
	}
	b.WriteString("\nLines:\n")
	if len(quote.Lines) == 0 {
		b.WriteString("(no lines)\n")
	}
	for i, line := range quote.Lines {
		fmt.Fprintf(&b, "%d. %s | quantity %d x %s | discount %s%% | line total %s\n",
			i+1, line.Description, line.Quantity,
			FormatMinorUnits(line.UnitPriceCents, quote.Currency),
			FormatBasisPoints(line.DiscountBP),
			FormatMinorUnits(line.LineTotalCents, quote.Currency))
	}
	fmt.Fprintf(&b, "\nSubtotal: %s\n", FormatMinorUnits(quote.SubtotalCents, quote.Currency))
	fmt.Fprintf(&b, "Discount: %s\n", FormatMinorUnits(quote.DiscountCents, quote.Currency))
	fmt.Fprintf(&b, "Total: %s\n", FormatMinorUnits(quote.TotalCents, quote.Currency))
	if notes := strings.TrimSpace(quote.Notes); notes != "" {
		fmt.Fprintf(&b, "\nNotes:\n%s\n", notes)
	}
	return b.String()
}

// FormatMinorUnits renders an amount in minor units as major.minor with two
// decimals and the currency code (1250 EUR → "12.50 EUR"). Every supported
// code is treated as having two minor digits; that is the same simplification
// the UI's formatter makes.
func FormatMinorUnits(cents int64, currency string) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, cents/100, cents%100, currency)
}

// FormatBasisPoints renders basis points as a percentage with two decimals
// (1250 → "12.50").
func FormatBasisPoints(bp int64) string {
	return fmt.Sprintf("%d.%02d", bp/100, bp%100)
}
