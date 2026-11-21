package service

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// QuoteEmailSubject is the subject of a quote email: the quote number and the
// product name (#292), which the caller resolves with ResolveMailProductName
// (the stored general.company_name, else "GopherCRM"). No request value
// reaches a header, and the mailer refuses CR and LF regardless.
func QuoteEmailSubject(quoteID uint, productName string) string {
	return fmt.Sprintf("Quote %s from %s", models.QuoteNumber(quoteID), productName)
}

// Email is the flow of the design. The latest history id is read first, then
// the quote; the recipient is the linked customer's address as it is now,
// never a copy; the mail goes out with no transaction held; then one
// conditional update stamps last_emailed_at, and matches nothing if a
// revision to draft committed after the marker was read. Reading the marker
// before the quote means a revision that commits between the two reads is
// also refused: the rendering that went out may predate it. The logs carry
// the quote id and the customer id only: never the address, the name or the
// body.
//
// A sent quote also gets a client response link (#382), minted in one short
// transaction after the recipient checks and before the send; see
// mintResponseLink. An accepted quote is emailed without one. Only after the
// mailer reports success are the links of earlier emails invalidated; see
// invalidateEarlierResponseLinks.
//
// The quote's PDF is attached (#384) when PDF output is on and the mailer can
// carry an attachment. It is rendered after the recipient checks and before
// the response link is minted, with no transaction held, in at most
// QuotePDFRenderTimeout. When PDF output is off, the mailer has no attachment
// support, or the render fails or runs out of time, the mail goes out through
// Send exactly as before and one warn line says why, with the quote id and
// the customer id only. So the call takes at most QuotePDFRenderTimeout plus
// the mailer's send timeout.
func (s *quoteService) Email(id uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "Email")

	// The marker read before everything else: a sent→draft row with a higher
	// id means the quote was revised after this point, whether while the
	// mail was in flight or between this read and the read of the quote. A
	// quote that does not exist has no history, so the marker is 0 and the
	// read of the quote answers not found as before.
	latestHistoryID, err := s.quoteRepo.LatestStatusChangeID(id)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

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

	if s.mailer == nil {
		err := fmt.Errorf("no mailer is configured: %w", apperrors.ErrMailDelivery)
		logger.WithError(utils.LogError(err)).Error("Quote email failed")
		return nil, err
	}
	// The product name is read now, with no transaction held; a failed read
	// falls back to "GopherCRM" and never blocks the send (#292).
	productName := ResolveMailProductName(s.currency)
	body := RenderQuoteEmail(quote)
	attachments, pdf := s.quoteEmailPDF(id, logger)
	var tokenID uint
	if s.mintsResponseLink(quote) {
		link, minted, err := s.mintResponseLink(id, latestHistoryID)
		if err != nil {
			logServiceOutcome(logger, err, "Quote email refused")
			return nil, err
		}
		tokenID = minted
		body += renderQuoteResponseLink(link)
	}
	subject := QuoteEmailSubject(quote.ID, productName)
	if pdf != nil {
		err = attachments.SendWithAttachment(to, subject, body, mailer.Attachment{
			Filename:    quotePDFFilename(quote.ID),
			ContentType: "application/pdf",
			Data:        pdf,
		})
	} else {
		err = s.mailer.Send(to, subject, body)
	}
	if err != nil {
		// The mailer's own error is kept for errors.Is but not logged here:
		// a transport error may quote the address. Nothing is invalidated, so
		// the link of the previous successful email stays usable. A response
		// token minted above stays stored, unused: its plain value was never
		// delivered, and the next successful send stamps it.
		logger.Error("Quote email failed: mail delivery error")
		return nil, fmt.Errorf("quote %d: %w: %w", id, apperrors.ErrMailDelivery, err)
	}

	if tokenID != 0 {
		s.invalidateEarlierResponseLinks(id, tokenID, logger)
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

// quoteEmailPDF renders the PDF the email attaches. It returns the mailer's
// attachment side and the file, or nil bytes when the mail is to go out
// text-only: PDF output is off, the mailer cannot attach, or the render failed
// or timed out. Each of those is one warn line with a reason and the ids the
// logger already carries; the render's own failure is not an error of the
// email.
func (s *quoteService) quoteEmailPDF(id uint, logger *logrus.Entry) (mailer.AttachmentSender, []byte) {
	const message = "Quote emailed without a PDF attachment"
	if !s.PDFAvailable() {
		logger.WithField("reason", "pdf_off").Warn(message)
		return nil, nil
	}
	attachments, ok := s.mailer.(mailer.AttachmentSender)
	if !ok {
		logger.WithField("reason", "mailer_without_attachments").Warn(message)
		return nil, nil
	}
	pdf, reason, err := s.renderQuotePDF(id)
	if err != nil || len(pdf) == 0 {
		if reason == "" {
			reason = quotePDFReasonRenderer
		}
		logger.WithField("reason", reason).Warn(message)
		return nil, nil
	}
	return attachments, pdf
}

// mintsResponseLink reports whether the email of this quote carries a client
// response link: the service has a token store and a base URL, the quote is
// sent (an accepted quote is emailed without a link), and its valid_until has
// not passed. A sent quote past its valid_until is still emailed, as before,
// but without a link: the link would be dead on arrival, and nothing is
// stored for it.
func (s *quoteService) mintsResponseLink(quote *models.Quote) bool {
	if s.tokenRepo == nil || s.appBaseURL == "" || quote.Status != models.QuoteStatusSent {
		return false
	}
	return quote.ValidUntil == nil || !dateBefore(*quote.ValidUntil, s.now())
}

// mintResponseLink stores a fresh response token for the quote and returns
// the link that carries its plain value, with the id of the new row. One
// short transaction, in the order quotes → quote_response_tokens: the locking
// read of the quote; the re-check that it is still sent
// (apperrors.ErrQuoteNotSent otherwise); a plain read of the quote's customer
// as live, the first consistent read after the lock, so on MySQL its snapshot
// is taken after an erasure that held the quote has committed
// (apperrors.ErrQuoteNoRecipient for a quote without a customer or with an
// erased one, a customer erased while the PDF was rendered included); the
// re-check that no history row was written since the marker the caller read
// before the quote (apperrors.ErrQuoteChangedDuringSend: a revision and
// re-send in between would otherwise mail the old rendering with a live link
// to the new quote); and the insert. The customer read takes no lock, so it
// never waits on an erasure that holds the customer row. The mint stamps no
// earlier row: the links of earlier emails die only after this email was
// delivered (invalidateEarlierResponseLinks), so a failed send leaves the
// previous link usable. Only the hash is stored; the plain token and the link
// reach no log field and no error text.
func (s *quoteService) mintResponseLink(id, latestHistoryID uint) (string, uint, error) {
	plain, err := generateOpaqueToken()
	if err != nil {
		return "", 0, err
	}
	token := &models.QuoteResponseToken{QuoteID: id, TokenHash: hashOpaqueToken(plain, s.tokenSecret)}
	err = s.inQuoteCustomerTokenTransaction(func(repo repository.QuoteRepository, customers repository.CustomerRepository, tokens repository.QuoteResponseTokenRepository) error {
		locked, err := repo.GetByIDForUpdate(id)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
			}
			return err
		}
		if locked.Status != models.QuoteStatusSent {
			return fmt.Errorf("quote %d is %s: %w", id, locked.Status, apperrors.ErrQuoteNotSent)
		}
		if locked.CustomerID == nil {
			return fmt.Errorf("quote %d has no customer: %w", id, apperrors.ErrQuoteNoRecipient)
		}
		if _, err := customers.GetByID(*locked.CustomerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("customer %d of quote %d is gone: %w", *locked.CustomerID, id, apperrors.ErrQuoteNoRecipient)
			}
			return err
		}
		latest, err := repo.LatestStatusChangeID(id)
		if err != nil {
			return err
		}
		if latest != latestHistoryID {
			return fmt.Errorf("quote %d changed before the send: %w", id, apperrors.ErrQuoteChangedDuringSend)
		}
		token.ExpiresAt = quoteResponseExpiry(locked.ValidUntil)
		return tokens.Create(token)
	})
	if err != nil {
		return "", 0, err
	}
	return s.appBaseURL + "/quote-response?token=" + url.QueryEscape(plain), token.ID, nil
}

// invalidateEarlierResponseLinks runs after the mailer reported success for
// the email that carried token newestID: one short transaction, in the order
// quotes → quote_response_tokens, that locks the quote and stamps used_at
// (decision NULL) on its unused rows with a lower id. Rows of later emails
// stay usable, so of two overlapping emails of one quote the one that
// finishes last never kills the newer link. A quote deleted meanwhile ends
// the step with nothing to do. The mail has already gone out, so a failure is
// not an error of the email: one warn line with the ids the logger carries,
// the new link stays usable, and the links of earlier emails survive until
// the next successful send.
func (s *quoteService) invalidateEarlierResponseLinks(id, newestID uint, logger *logrus.Entry) {
	now := s.stamp()
	err := s.inQuoteTokenTransaction(func(repo repository.QuoteRepository, tokens repository.QuoteResponseTokenRepository) error {
		if _, err := repo.GetByIDForUpdate(id); err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		return tokens.InvalidateOlderForQuote(id, newestID, now)
	})
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote email: earlier response links not invalidated")
	}
}

// quoteResponseExpiry is when a response token stops working: the start of
// the day after valid_until in the server's local zone, stored in UTC, so the
// link works through the whole valid_until day, the same day dateBefore
// allows a staff acceptance. nil without a valid_until: the link then lives
// as long as the quote is sent.
func quoteResponseExpiry(validUntil *time.Time) *time.Time {
	if validUntil == nil {
		return nil
	}
	expiry := time.Date(validUntil.Year(), validUntil.Month(), validUntil.Day()+1, 0, 0, 0, 0, time.Local).UTC()
	return &expiry
}

// renderQuoteResponseLink is the final block of the email of a sent quote.
func renderQuoteResponseLink(link string) string {
	return "\nTo accept or decline this quote, open this link (it works once):\n" + link + "\n"
}

// RenderQuoteEmail is the plaintext body: the number, the title, every line
// with its description, quantity (without trailing zeros), unit price,
// discount, total, tax rate and
// tax (also on a 0 percent line), then the subtotal, the discount, the net
// total, the tax and the gross total with the currency (#324), the validity
// date and the notes. It is a rendering of the quote, not the printable
// document, and it carries no personal data of its own: the recipient is only
// the envelope.
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
		// The quantity prints without trailing zeros (#343): 3, 1.5, 0.001.
		fmt.Fprintf(&b, "%d. %s | quantity %s x %s | discount %s%% | line total %s | tax %s%% %s\n",
			i+1, line.Description, line.Quantity.String(),
			FormatMinorUnits(line.UnitPriceCents, quote.Currency),
			FormatBasisPoints(line.DiscountBP),
			FormatMinorUnits(line.LineTotalCents, quote.Currency),
			FormatBasisPoints(line.TaxRateBP),
			FormatMinorUnits(line.TaxCents, quote.Currency))
	}
	fmt.Fprintf(&b, "\nSubtotal: %s\n", FormatMinorUnits(quote.SubtotalCents, quote.Currency))
	fmt.Fprintf(&b, "Discount: %s\n", FormatMinorUnits(quote.DiscountCents, quote.Currency))
	fmt.Fprintf(&b, "Net total: %s\n", FormatMinorUnits(quote.TotalCents, quote.Currency))
	fmt.Fprintf(&b, "Tax: %s\n", FormatMinorUnits(quote.TaxCents, quote.Currency))
	fmt.Fprintf(&b, "Gross total: %s\n", FormatMinorUnits(quote.GrossTotalCents, quote.Currency))
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
