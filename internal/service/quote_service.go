package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

type quoteService struct {
	quoteRepo    repository.QuoteRepository
	productRepo  repository.ProductRepository
	dealRepo     repository.DealRepository
	customerRepo repository.CustomerRepository
	userRepo     repository.UserRepository
	currency     DealCurrencySource
	mailer       mailer.Mailer
	txManager    *utils.TransactionManager
	now          func() time.Time
}

// NewQuoteService wires the quotes module. currency is the configuration read
// behind the default currency (deals.default_currency); m delivers the quote
// email and may be nil, in which case every email is refused as a delivery
// failure.
func NewQuoteService(
	quoteRepo repository.QuoteRepository,
	productRepo repository.ProductRepository,
	dealRepo repository.DealRepository,
	customerRepo repository.CustomerRepository,
	userRepo repository.UserRepository,
	currency DealCurrencySource,
	m mailer.Mailer,
	txManager *utils.TransactionManager,
) QuoteService {
	return &quoteService{
		quoteRepo:    quoteRepo,
		productRepo:  productRepo,
		dealRepo:     dealRepo,
		customerRepo: customerRepo,
		userRepo:     userRepo,
		currency:     currency,
		mailer:       m,
		txManager:    txManager,
		now:          time.Now,
	}
}

// stamp is the time written to sent_at, closed_at, last_emailed_at and
// quote_status_changes.changed_at, always in UTC like GORM's own timestamps:
// SQLite stores these columns as text with the offset, so mixed offsets would
// compare and sort wrongly.
func (s *quoteService) stamp() time.Time {
	return s.now().UTC()
}

// inTransaction runs fn with the repositories bound to one transaction.
func (s *quoteService) inTransaction(fn func(repo repository.QuoteRepository, products repository.ProductRepository, customers repository.CustomerRepository, users repository.UserRepository, deals repository.DealRepository) error) error {
	if s.txManager == nil {
		return utils.ErrNoTransaction
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		return fn(s.quoteRepo.WithTx(tx), s.productRepo.WithTx(tx), s.customerRepo.WithTx(tx), s.userRepo.WithTx(tx), s.dealRepo.WithTx(tx))
	})
}

func (s *quoteService) Create(quote *models.Quote, actorID uint, role models.UserRole) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "quote"), "QuoteService", "Create")

	if quote.Currency == "" {
		quote.Currency = defaultDealCurrency(s.currency)
	}
	if err := validateQuote(quote); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote rejected")
		return err
	}
	if quote.DealID == nil && quote.CustomerID == nil {
		err := fmt.Errorf("deal_id or customer_id is required: %w", apperrors.ErrValidation)
		logger.WithError(utils.LogError(err)).Warn("Quote rejected")
		return err
	}
	if role != models.RoleAdmin && quote.OwnerID != actorID {
		err := fmt.Errorf("sales users may only own their own quotes: %w", apperrors.ErrForbidden)
		logger.WithError(utils.LogError(err)).Warn("Quote rejected")
		return err
	}

	customerGiven := quote.CustomerID != nil
	if quote.DealID != nil {
		deal, err := s.dealRepo.GetByID(*quote.DealID)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown deal_id %d: %w", *quote.DealID, apperrors.ErrDealNotFound)
			}
			return err
		}
		// A sales user may only quote the deals it owns; the answer is the
		// one GET /deals/:id gives, a 403 rather than a 404.
		if role != models.RoleAdmin && deal.OwnerID != actorID {
			return fmt.Errorf("deal %d belongs to another owner: %w", deal.ID, apperrors.ErrForbidden)
		}
		if deal.Stage.IsClosed() {
			return fmt.Errorf("deal %d is %s: %w", deal.ID, deal.Stage, apperrors.ErrDealClosed)
		}
		if customerGiven {
			if deal.CustomerID != nil && *deal.CustomerID != *quote.CustomerID {
				return fmt.Errorf("customer_id %d is not the customer of deal %d: %w", *quote.CustomerID, deal.ID, apperrors.ErrValidation)
			}
		} else {
			// Possibly nil: a deal with only a lead or a company gives a
			// quote without a recipient, which the email then refuses.
			quote.CustomerID = deal.CustomerID
		}
	}
	if customerGiven {
		if _, err := s.customerRepo.GetByID(*quote.CustomerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown customer_id %d: %w", *quote.CustomerID, apperrors.ErrCustomerNotFound)
			}
			return err
		}
	}
	if _, err := s.userRepo.GetByID(quote.OwnerID); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("unknown owner_id %d: %w", quote.OwnerID, apperrors.ErrAssigneeNotFound)
		}
		return err
	}

	// A new quote is a draft with nothing on it, whatever the caller set.
	quote.Status = models.QuoteStatusDraft
	quote.SubtotalCents, quote.DiscountCents, quote.TotalCents = 0, 0, 0
	quote.SentAt, quote.ClosedAt, quote.LastEmailedAt = nil, nil, nil
	quote.Lines = nil

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository) error {
		if err := repo.Create(quote); err != nil {
			return err
		}
		return repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID:     quote.ID,
			FromStatus:  nil,
			ToStatus:    models.QuoteStatusDraft,
			ChangedByID: actorID,
			ChangedAt:   s.stamp(),
		})
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("quote_id", quote.ID).Info("Quote created successfully")
	return nil
}

func (s *quoteService) GetByID(id uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "GetByID")

	quote, err := s.quoteRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Quote not found")
			return nil, fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return quote, nil
}

// Update saves the editable fields of a draft. The stored row is re-read with
// a locking read inside the transaction, so a transition committed since the
// handler read the quote is seen and refused; the write itself is conditional
// on the status as well.
func (s *quoteService) Update(quote *models.Quote) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", quote.ID), "QuoteService", "Update")

	if err := validateQuote(quote); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote rejected")
		return err
	}

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, customers repository.CustomerRepository, users repository.UserRepository, deals repository.DealRepository) error {
		locked, err := lockDraft(repo, quote.ID)
		if err != nil {
			return err
		}
		// The deal link is not editable: it is the deal the quote grew from.
		quote.DealID = locked.DealID
		if quote.Currency != locked.Currency {
			count, err := repo.CountLines(quote.ID)
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("quote %d has %d lines priced in %s: %w", quote.ID, count, locked.Currency, apperrors.ErrQuoteCurrencyLocked)
			}
		}
		if linkChanged(quote.CustomerID, locked.CustomerID) {
			if _, err := customers.GetByID(*quote.CustomerID); err != nil {
				if isNotFound(err) {
					return fmt.Errorf("unknown customer_id %d: %w", *quote.CustomerID, apperrors.ErrCustomerNotFound)
				}
				return err
			}
			if locked.DealID != nil {
				deal, err := deals.GetByID(*locked.DealID)
				if err != nil && !isNotFound(err) {
					return err
				}
				if err == nil && deal.CustomerID != nil && *deal.CustomerID != *quote.CustomerID {
					return fmt.Errorf("customer_id %d is not the customer of deal %d: %w", *quote.CustomerID, deal.ID, apperrors.ErrValidation)
				}
			}
		}
		if quote.OwnerID != locked.OwnerID {
			if _, err := users.GetByID(quote.OwnerID); err != nil {
				if isNotFound(err) {
					return fmt.Errorf("unknown owner_id %d: %w", quote.OwnerID, apperrors.ErrAssigneeNotFound)
				}
				return err
			}
		}
		rows, err := repo.UpdateDraft(quote)
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("quote %d is no longer a draft: %w", quote.ID, apperrors.ErrQuoteLocked)
		}
		return nil
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote update refused")
		return err
	}

	logger.Info("Quote updated successfully")
	return nil
}

func (s *quoteService) Delete(id uint, draftOnly bool) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "Delete")

	quote, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if draftOnly && quote.Status != models.QuoteStatusDraft {
		err := fmt.Errorf("quote %d is %s: %w", id, quote.Status, apperrors.ErrQuoteLocked)
		logger.WithError(utils.LogError(err)).Warn("Quote delete refused")
		return err
	}
	if err := s.quoteRepo.Delete(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Quote deleted successfully")
	return nil
}

func (s *quoteService) List(offset, limit int, filter repository.QuoteListFilter) ([]models.Quote, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "QuoteService", "List")
	if filter.Status.IsValid() {
		logger = logger.WithField("status", filter.Status)
	}

	quotes, total, err := s.quoteRepo.List(offset, limit, filter)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if quotes == nil {
		quotes = []models.Quote{}
	}
	return quotes, total, nil
}

func (s *quoteService) ListByDeal(dealID uint, ownerID *uint, offset, limit int, sortBy, sortOrder string) ([]models.Quote, int64, error) {
	deal, err := s.dealRepo.GetByID(dealID)
	if err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("deal %d not found: %w", dealID, apperrors.ErrNotFound)
		}
		return nil, 0, err
	}
	if ownerID != nil && deal.OwnerID != *ownerID {
		return nil, 0, fmt.Errorf("deal %d belongs to another owner: %w", dealID, apperrors.ErrForbidden)
	}
	return s.List(offset, limit, repository.QuoteListFilter{DealID: &dealID, OwnerID: ownerID, SortBy: sortBy, SortOrder: sortOrder})
}

func (s *quoteService) ListByCustomer(customerID uint, ownerID *uint, offset, limit int, sortBy, sortOrder string) ([]models.Quote, int64, error) {
	if _, err := s.customerRepo.GetByID(customerID); err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("customer %d not found: %w", customerID, apperrors.ErrNotFound)
		}
		return nil, 0, err
	}
	return s.List(offset, limit, repository.QuoteListFilter{CustomerID: &customerID, OwnerID: ownerID, SortBy: sortBy, SortOrder: sortOrder})
}

// AddLine is the sequence of the design: lock the quote, check it is a draft,
// read the product, copy name and price, compute the line, insert it and
// rewrite the totals conditionally — all in one transaction, so a refusal at
// any step leaves no row changed.
func (s *quoteService) AddLine(quoteID uint, input models.QuoteLineInput) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"quote_id":   quoteID,
		"product_id": input.ProductID,
	}), "QuoteService", "AddLine")

	if input.ProductID == 0 {
		return nil, fmt.Errorf("product_id is required: %w", apperrors.ErrValidation)
	}
	if err := validateLineBounds(&input.Quantity, &input.DiscountBP, input.UnitPriceCents, input.Description); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote line rejected")
		return nil, err
	}

	err := s.inTransaction(func(repo repository.QuoteRepository, products repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository) error {
		quote, err := lockDraft(repo, quoteID)
		if err != nil {
			return err
		}
		product, err := products.GetByID(input.ProductID)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown product_id %d: %w", input.ProductID, apperrors.ErrProductNotFound)
			}
			return err
		}
		if !product.IsActive {
			return fmt.Errorf("product %d is inactive: %w", product.ID, apperrors.ErrProductNotFound)
		}
		if product.Currency != quote.Currency {
			return fmt.Errorf("product %d is priced in %s, the quote in %s: %w", product.ID, product.Currency, quote.Currency, apperrors.ErrQuoteCurrencyMismatch)
		}
		count, err := repo.CountLines(quoteID)
		if err != nil {
			return err
		}
		if count >= int64(models.QuoteLinesMax) {
			return fmt.Errorf("quote %d already has %d lines: %w", quoteID, models.QuoteLinesMax, apperrors.ErrValidation)
		}

		line := &models.QuoteLine{
			QuoteID:        quoteID,
			ProductID:      product.ID,
			Description:    product.Name,
			Quantity:       input.Quantity,
			UnitPriceCents: product.UnitPriceCents,
			DiscountBP:     input.DiscountBP,
		}
		if input.UnitPriceCents != nil {
			line.UnitPriceCents = *input.UnitPriceCents
		}
		if input.Description != nil && strings.TrimSpace(*input.Description) != "" {
			line.Description = strings.TrimSpace(*input.Description)
		}
		// The position is assigned before the line is computed: computeLine
		// re-checks every bound of the assembled line, position included.
		position, err := repo.NextLinePosition(quoteID)
		if err != nil {
			return err
		}
		line.Position = position
		if err := computeLine(line); err != nil {
			return err
		}
		if err := repo.CreateLine(line); err != nil {
			return err
		}
		return recomputeTotals(repo, quoteID)
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote line refused")
		return nil, err
	}

	logger.Info("Quote line added")
	return s.GetByID(quoteID)
}

func (s *quoteService) UpdateLine(quoteID, lineID uint, patch models.QuoteLinePatch) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"quote_id": quoteID,
		"line_id":  lineID,
	}), "QuoteService", "UpdateLine")

	if err := validateLineBounds(patch.Quantity, patch.DiscountBP, patch.UnitPriceCents, patch.Description); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote line rejected")
		return nil, err
	}
	if patch.Position != nil && *patch.Position < 1 {
		return nil, fmt.Errorf("position must be at least 1: %w", apperrors.ErrValidation)
	}

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository) error {
		if _, err := lockDraft(repo, quoteID); err != nil {
			return err
		}
		line, err := repo.GetLine(quoteID, lineID)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("line %d is not on quote %d: %w", lineID, quoteID, apperrors.ErrQuoteLineNotFound)
			}
			return err
		}
		if patch.Quantity != nil {
			line.Quantity = *patch.Quantity
		}
		if patch.DiscountBP != nil {
			line.DiscountBP = *patch.DiscountBP
		}
		if patch.UnitPriceCents != nil {
			line.UnitPriceCents = *patch.UnitPriceCents
		}
		if patch.Description != nil && strings.TrimSpace(*patch.Description) != "" {
			line.Description = strings.TrimSpace(*patch.Description)
		}
		if patch.Position != nil {
			line.Position = *patch.Position
		}
		if err := computeLine(line); err != nil {
			return err
		}
		if err := repo.UpdateLine(line); err != nil {
			return err
		}
		return recomputeTotals(repo, quoteID)
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote line update refused")
		return nil, err
	}

	logger.Info("Quote line updated")
	return s.GetByID(quoteID)
}

func (s *quoteService) DeleteLine(quoteID, lineID uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"quote_id": quoteID,
		"line_id":  lineID,
	}), "QuoteService", "DeleteLine")

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository) error {
		if _, err := lockDraft(repo, quoteID); err != nil {
			return err
		}
		if err := repo.DeleteLine(quoteID, lineID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("line %d is not on quote %d: %w", lineID, quoteID, apperrors.ErrQuoteLineNotFound)
			}
			return err
		}
		return recomputeTotals(repo, quoteID)
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote line delete refused")
		return nil, err
	}

	logger.Info("Quote line deleted")
	return s.GetByID(quoteID)
}

// ChangeStatus is the transition of the design. Inside one transaction: a
// locking read of the quote, the transition check against the status it
// holds now, the conditional claim (WHERE status = from), then — for
// draft→sent — the line count read with a locking read after the claim, and
// the history row. A zero count rolls the claim back. Of two competing
// transitions exactly one claims the row: the other blocks on the lock,
// re-reads the winner's status and finds no allowed move from it.
func (s *quoteService) ChangeStatus(id uint, to models.QuoteStatus, actorID uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"quote_id": id,
		"status":   to,
	}), "QuoteService", "ChangeStatus")

	if !to.IsValid() {
		return nil, fmt.Errorf("unknown quote status %q: %w", to, apperrors.ErrValidation)
	}

	var from models.QuoteStatus
	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository) error {
		quote, err := repo.GetByIDForUpdate(id)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
			}
			return err
		}
		from = quote.Status
		if !from.CanTransitionTo(to) {
			return fmt.Errorf("quote %d cannot move from %s to %s: %w", id, from, to, apperrors.ErrInvalidQuoteTransition)
		}
		now := s.stamp()
		if to == models.QuoteStatusAccepted && quote.ValidUntil != nil && dateBefore(*quote.ValidUntil, s.now()) {
			return fmt.Errorf("quote %d was valid until %s: %w", id, *models.FormatDealDate(quote.ValidUntil), apperrors.ErrQuoteExpired)
		}

		stamps := repository.QuoteStatusStamps{SentAt: quote.SentAt, ClosedAt: quote.ClosedAt}
		switch {
		case to == models.QuoteStatusSent:
			stamps.SentAt, stamps.ClosedAt = &now, nil
		case to == models.QuoteStatusDraft:
			stamps.SentAt, stamps.ClosedAt = nil, nil
		case to.IsClosed():
			stamps.ClosedAt = &now
		}

		rows, err := repo.ClaimStatus(id, from, to, stamps)
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("quote %d is no longer %s: %w", id, from, apperrors.ErrInvalidQuoteTransition)
		}
		if to == models.QuoteStatusSent {
			count, err := repo.CountLinesForUpdate(id)
			if err != nil {
				return err
			}
			if count == 0 {
				return fmt.Errorf("quote %d has no lines: %w", id, apperrors.ErrQuoteHasNoLines)
			}
		}
		return repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID:     id,
			FromStatus:  &from,
			ToStatus:    to,
			ChangedByID: actorID,
			ChangedAt:   now,
		})
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote transition refused")
		return nil, err
	}

	logger.WithField("from_status", from).Info("Quote status changed")
	return s.GetByID(id)
}

func (s *quoteService) History(quoteID uint) ([]models.QuoteStatusChange, error) {
	if _, err := s.GetByID(quoteID); err != nil {
		return nil, err
	}
	changes, err := s.quoteRepo.ListStatusChanges(quoteID)
	if err != nil {
		return nil, err
	}
	if changes == nil {
		changes = []models.QuoteStatusChange{}
	}
	return changes, nil
}

// lockDraft is the first step of every write to a quote or its lines: the
// locking read, then the draft check.
func lockDraft(repo repository.QuoteRepository, id uint) (*models.Quote, error) {
	quote, err := repo.GetByIDForUpdate(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
		}
		return nil, err
	}
	if quote.Status != models.QuoteStatusDraft {
		return nil, fmt.Errorf("quote %d is %s: %w", id, quote.Status, apperrors.ErrQuoteLocked)
	}
	return quote, nil
}

// recomputeTotals is the last step of every line write: the lines are re-read
// in the transaction, summed, bounded, and written WHERE the status is still
// draft. A total over the cap or a lost draft rolls the whole write back.
func recomputeTotals(repo repository.QuoteRepository, quoteID uint) error {
	lines, err := repo.ListLines(quoteID)
	if err != nil {
		return err
	}
	subtotal, discount, total := models.ComputeQuoteTotals(lines)
	if total > models.QuoteAmountCentsMax {
		return fmt.Errorf("quote total would exceed %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	rows, err := repo.UpdateTotals(quoteID, subtotal, discount, total)
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("quote %d is no longer a draft: %w", quoteID, apperrors.ErrQuoteLocked)
	}
	return nil
}

// validateLineBounds checks the bounds of the values a line request carries
// (nil means "not sent"). Everything here is a validation failure.
func validateLineBounds(quantity, discountBP, unitPriceCents *int64, description *string) error {
	if quantity != nil && (*quantity < 1 || *quantity > models.QuoteLineQuantityMax) {
		return fmt.Errorf("quantity must be between 1 and %d: %w", models.QuoteLineQuantityMax, apperrors.ErrValidation)
	}
	if discountBP != nil && (*discountBP < 0 || *discountBP > models.QuoteDiscountBPMax) {
		return fmt.Errorf("discount_bp must be between 0 and %d: %w", models.QuoteDiscountBPMax, apperrors.ErrValidation)
	}
	if unitPriceCents != nil && (*unitPriceCents < 0 || *unitPriceCents > models.QuoteAmountCentsMax) {
		return fmt.Errorf("unit_price_cents must be between 0 and %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	if description != nil && len([]rune(strings.TrimSpace(*description))) > models.QuoteLineDescriptionMaxLength {
		return fmt.Errorf("description is longer than %d characters: %w", models.QuoteLineDescriptionMaxLength, apperrors.ErrValidation)
	}
	return nil
}

// computeLine re-checks the bounds on the assembled line and fills its total.
// The gross is checked against the cap BEFORE any discount arithmetic: a
// maximal quantity times a maximal price is 10^18 and still an int64, but
// multiplied by a basis-point factor it would not be.
func computeLine(line *models.QuoteLine) error {
	if err := validateLineBounds(&line.Quantity, &line.DiscountBP, &line.UnitPriceCents, &line.Description); err != nil {
		return err
	}
	if line.Position < 1 {
		return fmt.Errorf("position must be at least 1: %w", apperrors.ErrValidation)
	}
	gross := line.Quantity * line.UnitPriceCents
	if gross > models.QuoteAmountCentsMax {
		return fmt.Errorf("quantity × unit_price_cents must be at most %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	_, line.LineTotalCents = models.ComputeQuoteLineTotal(line.Quantity, line.UnitPriceCents, line.DiscountBP)
	return nil
}

// validateQuote runs the domain rules on the quote's own fields. Length bounds
// on notes are the handler's job, with the column widths as the source of
// truth; the links are Create's and Update's.
func validateQuote(quote *models.Quote) error {
	quote.Title = strings.TrimSpace(quote.Title)
	if quote.Title == "" {
		return fmt.Errorf("quote title is required: %w", apperrors.ErrValidation)
	}
	if len([]rune(quote.Title)) > models.QuoteTitleMaxLength {
		return fmt.Errorf("quote title is longer than %d characters: %w", models.QuoteTitleMaxLength, apperrors.ErrValidation)
	}
	if len(quote.Notes) > models.QuoteNotesMaxBytes {
		return fmt.Errorf("notes are longer than %d bytes: %w", models.QuoteNotesMaxBytes, apperrors.ErrValidation)
	}
	if !dealCurrencyPattern.MatchString(quote.Currency) {
		return fmt.Errorf("currency must be a three-letter upper-case ISO 4217 code: %w", apperrors.ErrValidation)
	}
	if quote.OwnerID == 0 {
		return fmt.Errorf("owner_id is required: %w", apperrors.ErrValidation)
	}
	if quote.DealID != nil && *quote.DealID == 0 {
		return fmt.Errorf("deal_id must be a positive integer: %w", apperrors.ErrValidation)
	}
	if quote.CustomerID != nil && *quote.CustomerID == 0 {
		return fmt.Errorf("customer_id must be a positive integer: %w", apperrors.ErrValidation)
	}
	return nil
}

// dateBefore reports whether the calendar date of validUntil, read in its own
// location (the date that was stored, whichever engine read it back), lies
// before the calendar date of now in the local zone.
func dateBefore(validUntil, now time.Time) bool {
	local := now.In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	date := time.Date(validUntil.Year(), validUntil.Month(), validUntil.Day(), 0, 0, 0, 0, time.Local)
	return date.Before(today)
}

// quoteRefusals are the sentinels a quote write may end in that are the
// caller's doing, not a failure of the service.
var quoteRefusals = []error{
	apperrors.ErrValidation, apperrors.ErrForbidden,
	apperrors.ErrQuoteNotFound, apperrors.ErrQuoteLineNotFound, apperrors.ErrProductNotFound,
	apperrors.ErrCustomerNotFound, apperrors.ErrAssigneeNotFound, apperrors.ErrDealNotFound,
	apperrors.ErrQuoteLocked, apperrors.ErrQuoteCurrencyLocked, apperrors.ErrQuoteCurrencyMismatch,
	apperrors.ErrInvalidQuoteTransition, apperrors.ErrQuoteHasNoLines, apperrors.ErrQuoteExpired,
}

// logServiceOutcome logs a refused write at warn level when it is a domain
// refusal the handler will classify, and as a failure otherwise. The error
// text carries ids and statuses only, never a title or a name.
func logServiceOutcome(logger *logrus.Entry, err error, refused string) {
	for _, sentinel := range quoteRefusals {
		if errors.Is(err, sentinel) {
			logger.WithError(utils.LogError(err)).Warn(refused)
			return
		}
	}
	utils.LogServiceResponse(logger, err)
}
