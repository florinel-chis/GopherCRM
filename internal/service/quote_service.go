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
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
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

	// The client response link (#382). tokenRepo is nil on a service built
	// with NewQuoteService: the email then carries no link and both public
	// methods answer apperrors.ErrQuoteResponseRejected.
	tokenRepo   repository.QuoteResponseTokenRepository
	appBaseURL  string
	tokenSecret string

	// PDF output (#384), set by WithQuotePDF. nil is off: the email goes out
	// text-only, RenderPDF answers apperrors.ErrQuotePDFUnavailable and
	// GetRenderView refuses every credential.
	pdf *quotePDF

	// webhooks is the outbound webhook emitter (#518), set by
	// WithQuoteWebhooks: Create emits quote.created; Update, the line writes,
	// ChangeStatus and RespondPublic quote.updated; Delete quote.deleted, each
	// in the write's transaction. NewQuoteService sets it to webhook.Nop; with
	// Nop every path behaves as without webhooks.
	webhooks webhook.Emitter
}

// QuoteServiceOption configures NewQuoteService and
// NewQuoteServiceWithResponseLinks beyond their required dependencies.
type QuoteServiceOption func(*quoteService)

// WithQuoteWebhooks makes the quote writes emit their webhook events through e
// (#518), in the write's transaction: Create emits quote.created after the
// draft history row; Update emits quote.updated when the state written differs
// from the quote read under lock; AddLine and DeleteLine always emit it;
// UpdateLine emits it when the line written differs from the line read under
// the quote lock; ChangeStatus and RespondPublic emit it for every transition
// that commits; Delete runs in one transaction with a locking read and emits
// quote.deleted for every active endpoint subscribed to it (#531). Email, the
// response-token and render-credential writes emit nothing. A nil e keeps
// webhook.Nop.
func WithQuoteWebhooks(e webhook.Emitter) QuoteServiceOption {
	return func(s *quoteService) {
		if e != nil {
			s.webhooks = e
		}
	}
}

// webhooksWired reports whether the quote writes emit webhook events. A
// service built without NewQuoteService (nil emitter) is unwired.
func (s *quoteService) webhooksWired() bool {
	return s.webhooks != nil && s.webhooks != webhook.Nop
}

// quoteEvents writes the quote events of one transaction: the bound emitter,
// or webhook.NopTx on an unwired service, and the transaction's context.
type quoteEvents struct {
	ctx context.Context
	tx  webhook.TxEmitter
}

func (e quoteEvents) created(id uint) error { return e.tx.Created(e.ctx, webhook.Quote, id) }
func (e quoteEvents) updated(id uint) error { return e.tx.Updated(e.ctx, webhook.Quote, id) }
func (e quoteEvents) deleted(id uint) error { return e.tx.Deleted(e.ctx, webhook.Quote, id) }

// events binds the service's emitter to tx.
func (s *quoteService) events(ctx context.Context, tx *gorm.DB) quoteEvents {
	if !s.webhooksWired() {
		return quoteEvents{ctx: ctx, tx: webhook.NopTx}
	}
	return quoteEvents{ctx: ctx, tx: s.webhooks.Bind(tx)}
}

// NewQuoteService wires the quotes module. currency is the configuration read
// behind the default currency (deals.default_currency) and, through
// ResolveMailProductName, the product name of the quote email subject
// (general.company_name, #292; nil means "GopherCRM"); m delivers the quote
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
	opts ...QuoteServiceOption,
) QuoteService {
	s := &quoteService{
		quoteRepo:    quoteRepo,
		productRepo:  productRepo,
		dealRepo:     dealRepo,
		customerRepo: customerRepo,
		userRepo:     userRepo,
		currency:     currency,
		mailer:       m,
		txManager:    txManager,
		now:          time.Now,
		webhooks:     webhook.Nop,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewQuoteServiceWithResponseLinks is NewQuoteService plus the client response
// link (#382): the email of a sent quote carries
// <appBaseURL>/quote-response?token=..., whose HMAC-SHA256 hash (keyed by
// tokenSecret) is stored through tokenRepo, and GetPublicResponse and
// RespondPublic answer it. NewQuoteService stays for callers that send no
// link; it refuses the two public methods instead of running them without a
// token store.
func NewQuoteServiceWithResponseLinks(
	quoteRepo repository.QuoteRepository,
	productRepo repository.ProductRepository,
	dealRepo repository.DealRepository,
	customerRepo repository.CustomerRepository,
	userRepo repository.UserRepository,
	currency DealCurrencySource,
	m mailer.Mailer,
	txManager *utils.TransactionManager,
	tokenRepo repository.QuoteResponseTokenRepository,
	appBaseURL string,
	tokenSecret string,
	opts ...QuoteServiceOption,
) QuoteService {
	svc := NewQuoteService(quoteRepo, productRepo, dealRepo, customerRepo, userRepo, currency, m, txManager, opts...).(*quoteService)
	svc.tokenRepo = tokenRepo
	svc.appBaseURL = strings.TrimRight(strings.TrimSpace(appBaseURL), "/")
	svc.tokenSecret = tokenSecret
	return svc
}

// stamp is the time written to sent_at, closed_at, last_emailed_at and
// quote_status_changes.changed_at, always in UTC like GORM's own timestamps:
// SQLite stores these columns as text with the offset, so mixed offsets would
// compare and sort wrongly.
func (s *quoteService) stamp() time.Time {
	return s.now().UTC()
}

// inTransaction runs fn with the repositories and the quote events bound to
// one transaction.
func (s *quoteService) inTransaction(fn func(repo repository.QuoteRepository, products repository.ProductRepository, customers repository.CustomerRepository, users repository.UserRepository, deals repository.DealRepository, events quoteEvents) error) error {
	if s.txManager == nil {
		return utils.ErrNoTransaction
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		return fn(s.quoteRepo.WithTx(tx), s.productRepo.WithTx(tx), s.customerRepo.WithTx(tx), s.userRepo.WithTx(tx), s.dealRepo.WithTx(tx), s.events(ctx, tx))
	})
}

// inQuoteTokenTransaction runs fn with the quote repository and the response
// token repository bound to one transaction. tokens is nil on a service
// without response links. Every caller touches the quotes row first, with a
// locking read, and the token rows after it (quotes → quote_response_tokens).
func (s *quoteService) inQuoteTokenTransaction(fn func(repo repository.QuoteRepository, tokens repository.QuoteResponseTokenRepository, events quoteEvents) error) error {
	return s.inQuoteCustomerTokenTransaction(func(repo repository.QuoteRepository, _ repository.CustomerRepository, tokens repository.QuoteResponseTokenRepository, events quoteEvents) error {
		return fn(repo, tokens, events)
	})
}

// inQuoteCustomerTokenTransaction is inQuoteTokenTransaction that also hands
// over the customer repository bound to the same transaction. Its callers
// read the quote's customer with a plain read only, as the first consistent
// read after the locking read of the quote: the erasure of a customer locks
// customers before quotes, so a locking customer read under the quote lock
// would invert that order. events writes the quote's webhook events (#518):
// a caller emits after its history row, so the plain read of the webhook
// endpoints comes after the customer read.
func (s *quoteService) inQuoteCustomerTokenTransaction(fn func(repo repository.QuoteRepository, customers repository.CustomerRepository, tokens repository.QuoteResponseTokenRepository, events quoteEvents) error) error {
	if s.txManager == nil {
		return utils.ErrNoTransaction
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var tokens repository.QuoteResponseTokenRepository
		if s.tokenRepo != nil {
			tokens = s.tokenRepo.WithTx(tx)
		}
		return fn(s.quoteRepo.WithTx(tx), s.customerRepo.WithTx(tx), tokens, s.events(ctx, tx))
	})
}

func (s *quoteService) Create(quote *models.Quote, actorID uint, role models.UserRole) error {
	isAdmin := role == models.RoleAdmin
	return s.CreateWithPolicy(quote, actorID, QuoteCreatePolicy{AnyOwner: isAdmin, AnyDeal: isAdmin})
}

func (s *quoteService) CreateWithPolicy(quote *models.Quote, actorID uint, policy QuoteCreatePolicy) error {
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
	if !policy.AnyOwner && quote.OwnerID != actorID {
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
		if !policy.AnyDeal && deal.OwnerID != actorID {
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
	quote.TaxCents, quote.GrossTotalCents = 0, 0
	quote.SentAt, quote.ClosedAt, quote.LastEmailedAt = nil, nil, nil
	quote.Lines = nil

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository, events quoteEvents) error {
		if err := repo.Create(quote); err != nil {
			return err
		}
		if err := repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID:     quote.ID,
			FromStatus:  nil,
			ToStatus:    models.QuoteStatusDraft,
			ChangedByID: &actorID,
			Source:      models.QuoteChangeSourceStaff,
			ChangedAt:   s.stamp(),
		}); err != nil {
			return err
		}
		return events.created(quote.ID)
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

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, customers repository.CustomerRepository, users repository.UserRepository, deals repository.DealRepository, events quoteEvents) error {
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
		// quote.updated only when a stored value changed (#518): the
		// locked row with the columns UpdateDraft wrote applied.
		if locked.WebhookState() == draftWritten(locked, quote).WebhookState() {
			return nil
		}
		return events.updated(quote.ID)
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote update refused")
		return err
	}

	logger.Info("Quote updated successfully")
	return nil
}

// Delete soft-deletes a quote. With webhooks wired (#518) it runs in one
// transaction: a locking read of the live quote, the draft-only check on that
// row, the soft delete and quote.deleted for every active endpoint subscribed
// to it (#531), so a delete that lost a race to a transition or another
// delete emits nothing. Unwired, it keeps the plain read before the delete.
func (s *quoteService) Delete(id uint, draftOnly bool) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "Delete")

	if s.webhooksWired() {
		err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository, events quoteEvents) error {
			locked, err := repo.GetByIDForUpdate(id)
			if err != nil {
				if isNotFound(err) {
					return fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
				}
				return err
			}
			if draftOnly && locked.Status != models.QuoteStatusDraft {
				return fmt.Errorf("quote %d is %s: %w", id, locked.Status, apperrors.ErrQuoteLocked)
			}
			if err := repo.Delete(id); err != nil {
				if isNotFound(err) {
					return fmt.Errorf("quote %d not found: %w", id, apperrors.ErrQuoteNotFound)
				}
				return err
			}
			return events.deleted(id)
		})
		if err != nil {
			logServiceOutcome(logger, err, "Quote delete refused")
			return err
		}
		logger.Info("Quote deleted successfully")
		return nil
	}

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
	if err := validateLineBounds(&input.Quantity, &input.DiscountBP, input.UnitPriceCents, input.Description, &input.TaxRateBP); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote line rejected")
		return nil, err
	}

	err := s.inTransaction(func(repo repository.QuoteRepository, products repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository, events quoteEvents) error {
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
			TaxRateBP:      input.TaxRateBP,
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
		if err := recomputeTotals(repo, quoteID); err != nil {
			return err
		}
		// The line set changed (#518).
		return events.updated(quoteID)
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

	if err := validateLineBounds(patch.Quantity, patch.DiscountBP, patch.UnitPriceCents, patch.Description, patch.TaxRateBP); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote line rejected")
		return nil, err
	}
	if patch.Position != nil && *patch.Position < 1 {
		return nil, fmt.Errorf("position must be at least 1: %w", apperrors.ErrValidation)
	}

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository, events quoteEvents) error {
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
		before := line.WebhookState()
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
		if patch.TaxRateBP != nil {
			line.TaxRateBP = *patch.TaxRateBP
		}
		if err := computeLine(line); err != nil {
			return err
		}
		if err := repo.UpdateLine(line); err != nil {
			return err
		}
		if err := recomputeTotals(repo, quoteID); err != nil {
			return err
		}
		// quote.updated only when a stored line value changed (#518).
		if line.WebhookState() == before {
			return nil
		}
		return events.updated(quoteID)
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

	err := s.inTransaction(func(repo repository.QuoteRepository, _ repository.ProductRepository, _ repository.CustomerRepository, _ repository.UserRepository, _ repository.DealRepository, events quoteEvents) error {
		if _, err := lockDraft(repo, quoteID); err != nil {
			return err
		}
		if err := repo.DeleteLine(quoteID, lineID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("line %d is not on quote %d: %w", lineID, quoteID, apperrors.ErrQuoteLineNotFound)
			}
			return err
		}
		if err := recomputeTotals(repo, quoteID); err != nil {
			return err
		}
		// The line set changed (#518).
		return events.updated(quoteID)
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
// re-reads the winner's status and finds no allowed move from it. A move away
// from sent also stamps the quote's unused response tokens in the same
// transaction (#382), so a mailed link stays dead after sent→draft→sent until
// the next email mints a new one.
func (s *quoteService) ChangeStatus(id uint, to models.QuoteStatus, actorID uint) (*models.Quote, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"quote_id": id,
		"status":   to,
	}), "QuoteService", "ChangeStatus")

	if !to.IsValid() {
		return nil, fmt.Errorf("unknown quote status %q: %w", to, apperrors.ErrValidation)
	}

	var from models.QuoteStatus
	err := s.inQuoteTokenTransaction(func(repo repository.QuoteRepository, tokens repository.QuoteResponseTokenRepository, events quoteEvents) error {
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
		if from == models.QuoteStatusSent && tokens != nil {
			if err := tokens.InvalidateForQuote(id, now); err != nil {
				return err
			}
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
		if err := repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID:     id,
			FromStatus:  &from,
			ToStatus:    to,
			ChangedByID: &actorID,
			Source:      models.QuoteChangeSourceStaff,
			ChangedAt:   now,
		}); err != nil {
			return err
		}
		// Every committed transition changes the status (#518).
		return events.updated(id)
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

// draftWritten is the quote as UpdateDraft leaves it: the locked row with the
// columns UpdateDraft writes taken from the caller's quote. The totals, the
// status and the stamps stay the locked row's; the deal link is the locked
// one already (Update forces it).
func draftWritten(locked, quote *models.Quote) *models.Quote {
	written := *locked
	written.Title = quote.Title
	written.Currency = quote.Currency
	written.ValidUntil = quote.ValidUntil
	written.Notes = quote.Notes
	written.DealID = quote.DealID
	written.CustomerID = quote.CustomerID
	written.OwnerID = quote.OwnerID
	return &written
}

// recomputeTotals is the last step of every line write: the lines are re-read
// in the transaction, summed (net, tax and gross), bounded, and written WHERE
// the status is still draft. A gross total over the cap or a lost draft rolls
// the whole write back. The cap is on the gross total (#324); the net total is
// never larger, because tax is never negative.
func recomputeTotals(repo repository.QuoteRepository, quoteID uint) error {
	lines, err := repo.ListLines(quoteID)
	if err != nil {
		return err
	}
	totals := models.SumQuoteTotals(lines)
	if totals.Gross > models.QuoteAmountCentsMax {
		return fmt.Errorf("quote gross total would exceed %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	rows, err := repo.UpdateTotals(quoteID, totals)
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("quote %d is no longer a draft: %w", quoteID, apperrors.ErrQuoteLocked)
	}
	return nil
}

// validateLineBounds checks the bounds of the values a line request carries
// (nil means "not sent"). Everything here is a validation failure. The
// quantity must lie within 0.001..1,000,000 (1..10^9 thousandths); the
// handler checks the same bound when it decodes the JSON number, but a
// QuoteQuantity built with QuoteQuantityFromMilli carries no check.
func validateLineBounds(quantity *models.QuoteQuantity, discountBP, unitPriceCents *int64, description *string, taxRateBP *int64) error {
	if quantity != nil && !quantity.InBounds() {
		return fmt.Errorf("quantity must be from 0.001 to %d: %w", models.QuoteLineQuantityMax, apperrors.ErrValidation)
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
	if taxRateBP != nil && (*taxRateBP < 0 || *taxRateBP > models.QuoteTaxRateBPMax) {
		return fmt.Errorf("tax_rate_bp must be between 0 and %d: %w", models.QuoteTaxRateBPMax, apperrors.ErrValidation)
	}
	return nil
}

// computeLine re-checks the bounds on the assembled line and fills its total
// and then its tax. models.ComputeQuoteLineAmounts computes the rounded gross
// and the line total in 128-bit integer arithmetic (#343) and reports a
// rounded gross above the cap, which is refused before anything is written.
func computeLine(line *models.QuoteLine) error {
	if err := validateLineBounds(&line.Quantity, &line.DiscountBP, &line.UnitPriceCents, &line.Description, &line.TaxRateBP); err != nil {
		return err
	}
	if line.Position < 1 {
		return fmt.Errorf("position must be at least 1: %w", apperrors.ErrValidation)
	}
	_, lineTotal, ok := models.ComputeQuoteLineAmounts(line.Quantity, line.UnitPriceCents, line.DiscountBP)
	if !ok {
		return fmt.Errorf("quantity × unit_price_cents must be at most %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	line.LineTotalCents = lineTotal
	line.TaxCents = models.ComputeQuoteLineTax(line.LineTotalCents, line.TaxRateBP)
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
	apperrors.ErrQuoteNotSent, apperrors.ErrQuoteChangedDuringSend, apperrors.ErrQuoteResponseRejected,
	apperrors.ErrQuoteRenderRejected,
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
