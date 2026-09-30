package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ConfigDealsDefaultCurrency is the configuration key a new deal's currency
// comes from when the request sends none. Seeded to EUR, admin-editable.
const ConfigDealsDefaultCurrency = "deals.default_currency"

// DealCurrencySource is the one configuration read the deals module makes.
// ConfigurationService satisfies it; tests hand in a stub.
type DealCurrencySource interface {
	GetString(key string) (string, error)
}

// dealCurrencyPattern is the shape of an ISO 4217 code as this API takes it:
// exactly three upper-case ASCII letters.
var dealCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type dealService struct {
	dealRepo     repository.DealRepository
	companyRepo  repository.CompanyRepository
	customerRepo repository.CustomerRepository
	leadRepo     repository.LeadRepository
	userRepo     repository.UserRepository
	currency     DealCurrencySource
	txManager    *utils.TransactionManager
	now          func() time.Time
}

func NewDealService(
	dealRepo repository.DealRepository,
	companyRepo repository.CompanyRepository,
	customerRepo repository.CustomerRepository,
	leadRepo repository.LeadRepository,
	userRepo repository.UserRepository,
	currency DealCurrencySource,
	txManager *utils.TransactionManager,
) DealService {
	return &dealService{
		dealRepo:     dealRepo,
		companyRepo:  companyRepo,
		customerRepo: customerRepo,
		leadRepo:     leadRepo,
		userRepo:     userRepo,
		currency:     currency,
		txManager:    txManager,
		now:          time.Now,
	}
}

// stamp is the time written to closed_at and deal_stage_changes.changed_at,
// always in UTC like GORM's created_at/updated_at: SQLite stores these columns
// as text with the offset, so mixed offsets would compare and sort wrongly.
func (s *dealService) stamp() time.Time {
	return s.now().UTC()
}

func (s *dealService) Create(deal *models.Deal, probability *int, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("deal_title", deal.Title), "DealService", "Create")

	if deal.Stage == "" {
		deal.Stage = models.DealStageQualification
	}
	if deal.Currency == "" {
		deal.Currency = s.defaultCurrency()
	}
	if err := s.validate(deal, probability); err != nil {
		logger.WithError(err).Warn("Deal rejected")
		return err
	}
	if err := s.checkLinks(deal, nil); err != nil {
		logger.WithError(err).Warn("Deal rejected")
		return err
	}

	// A create is a transition from nowhere into the stage: the same rules
	// decide probability, closed_at and lost_reason, and the same history row
	// is written, with from_stage NULL.
	applyDealTransition(deal, deal.Stage, probability, deal.LostReason, s.stamp())

	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.dealRepo.WithTx(tx)
		if err := repo.Create(deal); err != nil {
			return err
		}
		return repo.CreateStageChange(&models.DealStageChange{
			DealID:      deal.ID,
			FromStage:   nil,
			ToStage:     deal.Stage,
			ChangedByID: actorID,
			ChangedAt:   s.stamp(),
		})
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("deal_id", deal.ID).Info("Deal created successfully")
	return nil
}

func (s *dealService) GetByID(id uint) (*models.Deal, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("deal_id", id), "DealService", "GetByID")

	deal, err := s.dealRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Deal not found")
			return nil, fmt.Errorf("deal %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return deal, nil
}

// Update saves the deal. The stored row is re-read for its stage: a difference
// is a transition and gets the history row; the same stage keeps closed_at as
// it is and only applies an explicit probability. The links and the owner are
// checked only where they differ from the stored row (see checkLinks).
func (s *dealService) Update(deal *models.Deal, probability *int, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("deal_id", deal.ID), "DealService", "Update")

	stored, err := s.GetByID(deal.ID)
	if err != nil {
		return err
	}
	if deal.Stage == "" {
		deal.Stage = stored.Stage
	}
	if deal.Currency == "" {
		deal.Currency = stored.Currency
	}
	if err := s.validate(deal, probability); err != nil {
		logger.WithError(err).Warn("Deal rejected")
		return err
	}
	if err := s.checkLinks(deal, stored); err != nil {
		logger.WithError(err).Warn("Deal rejected")
		return err
	}

	from := stored.Stage
	changed := deal.Stage != from
	if changed {
		applyDealTransition(deal, deal.Stage, probability, deal.LostReason, s.stamp())
	} else {
		// Same stage: closed_at stands, an explicit probability is applied
		// (the closed stages still pin theirs), and lost_reason only survives
		// on a lost deal.
		deal.ClosedAt = stored.ClosedAt
		if probability != nil {
			deal.Probability = *probability
		}
		deal.Probability = pinnedProbability(deal.Stage, deal.Probability)
		if deal.Stage != models.DealStageLost {
			deal.LostReason = ""
		}
	}

	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.dealRepo.WithTx(tx)
		if err := repo.Update(deal); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return repo.CreateStageChange(&models.DealStageChange{
			DealID:      deal.ID,
			FromStage:   &from,
			ToStage:     deal.Stage,
			ChangedByID: actorID,
			ChangedAt:   s.stamp(),
		})
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Deal updated successfully")
	return nil
}

// ChangeStage is the stage endpoint. The same stage again returns the deal
// as it is and writes nothing at all.
func (s *dealService) ChangeStage(id uint, stage models.DealStage, probability *int, lostReason *string, actorID uint) (*models.Deal, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"deal_id": id,
		"stage":   stage,
	}), "DealService", "ChangeStage")

	if !stage.IsValid() {
		return nil, fmt.Errorf("unknown deal stage %q: %w", stage, apperrors.ErrValidation)
	}
	if probability != nil && (*probability < 0 || *probability > 100) {
		return nil, fmt.Errorf("probability must be between 0 and 100: %w", apperrors.ErrValidation)
	}

	deal, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if deal.Stage == stage {
		logger.Info("Deal already in the requested stage; nothing written")
		return deal, nil
	}

	reason := deal.LostReason
	if lostReason != nil {
		reason = *lostReason
	}
	if len([]rune(reason)) > models.DealLostReasonMaxLength {
		return nil, fmt.Errorf("lost_reason is longer than %d characters: %w", models.DealLostReasonMaxLength, apperrors.ErrValidation)
	}

	from := deal.Stage
	applyDealTransition(deal, stage, probability, reason, s.stamp())

	err = s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.dealRepo.WithTx(tx)
		if err := repo.Update(deal); err != nil {
			return err
		}
		return repo.CreateStageChange(&models.DealStageChange{
			DealID:      deal.ID,
			FromStage:   &from,
			ToStage:     stage,
			ChangedByID: actorID,
			ChangedAt:   s.stamp(),
		})
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("from_stage", from).Info("Deal stage changed")
	return s.GetByID(id)
}

func (s *dealService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("deal_id", id), "DealService", "Delete")

	if err := s.dealRepo.Delete(id); err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Deal not found")
			return fmt.Errorf("deal %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Deal deleted successfully")
	return nil
}

func (s *dealService) List(offset, limit int, filter repository.DealListFilter) ([]models.Deal, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
		"stage":  filter.Stage,
		"open":   filter.Open,
	}), "DealService", "List")

	deals, total, err := s.dealRepo.List(offset, limit, filter)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if deals == nil {
		deals = []models.Deal{}
	}
	return deals, total, nil
}

func (s *dealService) ListByCompany(companyID uint, ownerID *uint, offset, limit int) ([]models.Deal, int64, error) {
	if _, err := s.companyRepo.GetByID(companyID); err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("company %d not found: %w", companyID, apperrors.ErrNotFound)
		}
		return nil, 0, err
	}
	return s.List(offset, limit, repository.DealListFilter{CompanyID: &companyID, OwnerID: ownerID})
}

func (s *dealService) ListByCustomer(customerID uint, ownerID *uint, offset, limit int) ([]models.Deal, int64, error) {
	if _, err := s.customerRepo.GetByID(customerID); err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("customer %d not found: %w", customerID, apperrors.ErrNotFound)
		}
		return nil, 0, err
	}
	return s.List(offset, limit, repository.DealListFilter{CustomerID: &customerID, OwnerID: ownerID})
}

func (s *dealService) History(dealID uint) ([]models.DealStageChange, error) {
	if _, err := s.GetByID(dealID); err != nil {
		return nil, err
	}
	changes, err := s.dealRepo.ListStageChanges(dealID)
	if err != nil {
		return nil, err
	}
	if changes == nil {
		changes = []models.DealStageChange{}
	}
	return changes, nil
}

// defaultCurrency reads deals.default_currency. The configuration API stores
// any string, so the value is trimmed and upper-cased here and, if it still is
// not a three-letter code (or cannot be read at all), the shipped EUR is used
// and the fact logged: a deal must never be refused because of a setting.
func (s *dealService) defaultCurrency() string {
	if s.currency == nil {
		return models.DealDefaultCurrency
	}
	value, err := s.currency.GetString(ConfigDealsDefaultCurrency)
	if err != nil {
		utils.Logger.WithError(err).WithField("config_key", ConfigDealsDefaultCurrency).
			Warn("Default deal currency could not be read; using the shipped default")
		return models.DealDefaultCurrency
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	if !dealCurrencyPattern.MatchString(value) {
		utils.Logger.WithField("config_key", ConfigDealsDefaultCurrency).WithField("value", value).
			Warn("Configured default deal currency is not a three-letter code; using the shipped default")
		return models.DealDefaultCurrency
	}
	return value
}

// validate runs the domain rules on the fields. Length bounds on the other
// text columns are the handler's job, with the column widths as the source of
// truth; the rules here are the ones that interpret the value. The links are
// checkLinks' job.
func (s *dealService) validate(deal *models.Deal, probability *int) error {
	deal.Title = strings.TrimSpace(deal.Title)
	if deal.Title == "" {
		return fmt.Errorf("deal title is required: %w", apperrors.ErrValidation)
	}
	if len([]rune(deal.Title)) > models.DealTitleMaxLength {
		return fmt.Errorf("deal title is longer than %d characters: %w", models.DealTitleMaxLength, apperrors.ErrValidation)
	}
	if !deal.Stage.IsValid() {
		return fmt.Errorf("unknown deal stage %q: %w", deal.Stage, apperrors.ErrValidation)
	}
	if deal.AmountCents < 0 {
		return fmt.Errorf("amount_cents must not be negative: %w", apperrors.ErrValidation)
	}
	if !dealCurrencyPattern.MatchString(deal.Currency) {
		return fmt.Errorf("currency must be a three-letter upper-case ISO 4217 code: %w", apperrors.ErrValidation)
	}
	if probability != nil && (*probability < 0 || *probability > 100) {
		return fmt.Errorf("probability must be between 0 and 100: %w", apperrors.ErrValidation)
	}
	if deal.OwnerID == 0 {
		return fmt.Errorf("owner_id is required: %w", apperrors.ErrValidation)
	}
	return nil
}

// checkLinks holds the owner and the optional links to live rows. On a create
// (stored nil) every link the deal carries is checked. On an update only a
// link that differs from the stored row is: the handler leaves a link the
// request did not set exactly as stored, and such a link is not re-checked,
// so a deal keeps its links to erased records (erasure soft-deletes the row)
// and stays editable. A link the request cleared is nil and needs no check;
// one set to another id must name a live row, as on a create.
func (s *dealService) checkLinks(deal, stored *models.Deal) error {
	var previousCompany, previousCustomer, previousLead *uint
	ownerChanged := true
	if stored != nil {
		previousCompany, previousCustomer, previousLead = stored.CompanyID, stored.CustomerID, stored.LeadID
		ownerChanged = deal.OwnerID != stored.OwnerID
	}

	if ownerChanged {
		if _, err := s.userRepo.GetByID(deal.OwnerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown owner_id %d: %w", deal.OwnerID, apperrors.ErrAssigneeNotFound)
			}
			return err
		}
	}
	if linkChanged(deal.CompanyID, previousCompany) {
		if _, err := s.companyRepo.GetByID(*deal.CompanyID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown company_id %d: %w", *deal.CompanyID, apperrors.ErrCompanyNotFound)
			}
			return err
		}
	}
	if linkChanged(deal.CustomerID, previousCustomer) {
		if _, err := s.customerRepo.GetByID(*deal.CustomerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown customer_id %d: %w", *deal.CustomerID, apperrors.ErrCustomerNotFound)
			}
			return err
		}
	}
	if linkChanged(deal.LeadID, previousLead) {
		if _, err := s.leadRepo.GetByID(*deal.LeadID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown lead_id %d: %w", *deal.LeadID, apperrors.ErrLeadNotFound)
			}
			return err
		}
	}
	return nil
}

// linkChanged reports whether next is a link that has to be checked: one that
// is set and is not the one already stored. A cleared link (nil) never is.
func linkChanged(next, previous *uint) bool {
	if next == nil {
		return false
	}
	return previous == nil || *next != *previous
}

// applyDealTransition moves deal into `to` and applies every rule that hangs
// off a stage change, in one place for create, update and the stage endpoint:
//
//   - probability: won is 100 and lost is 0 whatever was sent; an open stage
//     takes the explicit value when there is one, else its default;
//   - closed_at: set to now on entering won or lost (including won → lost),
//     cleared on entering an open stage;
//   - lost_reason: kept only on lost, cleared otherwise.
func applyDealTransition(deal *models.Deal, to models.DealStage, probability *int, lostReason string, now time.Time) {
	deal.Stage = to

	if probability != nil {
		deal.Probability = *probability
	} else {
		deal.Probability = to.DefaultProbability()
	}
	deal.Probability = pinnedProbability(to, deal.Probability)

	if to.IsClosed() {
		closedAt := now
		deal.ClosedAt = &closedAt
	} else {
		deal.ClosedAt = nil
	}

	if to == models.DealStageLost {
		deal.LostReason = strings.TrimSpace(lostReason)
	} else {
		deal.LostReason = ""
	}
}

// pinnedProbability is the probability the closed stages force: 100 for won,
// 0 for lost; any other stage keeps the given value.
func pinnedProbability(stage models.DealStage, probability int) int {
	switch stage {
	case models.DealStageWon:
		return 100
	case models.DealStageLost:
		return 0
	default:
		return probability
	}
}
