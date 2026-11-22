package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

type companyService struct {
	companyRepo repository.CompanyRepository
	userRepo    repository.UserRepository
	txManager   *utils.TransactionManager

	// Instance-local test seams (#182), nil in production: afterDeletePreRead
	// runs between Delete's plain pre-read and its transaction, afterDeleteLock
	// right after the company lock, before the unlinks.
	afterDeletePreRead func(id uint)
	afterDeleteLock    func(id uint)
	// afterDeleteUnlink runs after the unlinks and the soft delete, before the
	// commit, so a test can park the delete while it holds its locks.
	afterDeleteUnlink func(id uint)

	// webhooks is the outbound webhook emitter (#517), set by
	// WithCompanyWebhooks: Create emits company.created, Update
	// company.updated when a stored value changed, and Delete company.deleted,
	// each in the company's transaction. NewCompanyService sets it to
	// webhook.Nop; with Nop every path behaves as without webhooks.
	webhooks webhook.Emitter
}

// CompanyServiceOption configures NewCompanyService beyond its required
// dependencies.
type CompanyServiceOption func(*companyService)

// WithCompanyWebhooks makes the company writes emit their webhook events
// through e (#517), in the write's transaction: Create inserts the company and
// emits company.created; Update locks the company row first
// (CompanyLockingReader) and emits company.updated when the WebhookState
// written differs from the locked row's; Delete emits company.deleted for every
// active endpoint subscribed to it (#531) after the unlinks and the soft
// delete, and no updated event for the leads, customers and deals it unlinks.
// A nil e keeps webhook.Nop.
func WithCompanyWebhooks(e webhook.Emitter) CompanyServiceOption {
	return func(s *companyService) {
		if e != nil {
			s.webhooks = e
		}
	}
}

// webhooksWired reports whether the company writes emit webhook events. A
// service built without NewCompanyService (nil emitter) is unwired.
func (s *companyService) webhooksWired() bool {
	return s.webhooks != nil && s.webhooks != webhook.Nop
}

// errCompanyLockingReadUnavailable is returned by a wired Update whose company
// repository offers no locking read; nothing is written.
var errCompanyLockingReadUnavailable = errors.New("company repository has no locking read")

func NewCompanyService(companyRepo repository.CompanyRepository, userRepo repository.UserRepository, txManager *utils.TransactionManager, opts ...CompanyServiceOption) CompanyService {
	s := &companyService{
		companyRepo: companyRepo,
		userRepo:    userRepo,
		txManager:   txManager,
		webhooks:    webhook.Nop,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *companyService) Create(company *models.Company) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "company"), "CompanyService", "Create")

	if err := s.prepare(company); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Company rejected")
		return err
	}

	var err error
	if s.webhooksWired() {
		err = s.createWithWebhook(company)
	} else {
		err = s.companyRepo.Create(company)
	}
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("company_id", company.ID).Info("Company created successfully")
	return nil
}

// createWithWebhook inserts the company and emits company.created in one
// transaction (#517): the company and its outbox rows commit together or not
// at all. On failure company.ID is reset to 0, as nothing was stored.
func (s *companyService) createWithWebhook(company *models.Company) error {
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		if err := s.companyRepo.WithTx(tx).Create(company); err != nil {
			return err
		}
		return s.webhooks.Bind(tx).Created(ctx, webhook.Company, company.ID)
	})
	if err != nil {
		company.ID = 0
	}
	return err
}

func (s *companyService) GetByID(id uint) (*models.Company, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_id", id), "CompanyService", "GetByID")

	company, err := s.companyRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Company not found")
			return nil, fmt.Errorf("company %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return company, nil
}

func (s *companyService) Update(company *models.Company) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_id", company.ID), "CompanyService", "Update")

	if err := s.prepare(company); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Company rejected")
		return err
	}

	var err error
	if s.webhooksWired() {
		err = s.updateWithWebhook(company)
	} else {
		err = s.companyRepo.Update(company)
	}
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Company updated successfully")
	return nil
}

// updateWithWebhook is the wired update (#517): one transaction that reads
// the live company under lock (CompanyLockingReader), writes it and emits
// company.updated when the written WebhookState differs from the locked
// row's, so an update that sends the stored values emits nothing. A company
// missing or deleted by the time the lock is taken is apperrors.ErrNotFound
// with nothing written.
func (s *companyService) updateWithWebhook(company *models.Company) error {
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.companyRepo.WithTx(tx)
		locker, ok := repo.(repository.CompanyLockingReader)
		if !ok {
			return errCompanyLockingReadUnavailable
		}
		locked, err := locker.GetByIDForUpdate(company.ID)
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("company %d not found: %w", company.ID, apperrors.ErrNotFound)
			}
			return err
		}
		if err := repo.Update(company); err != nil {
			return err
		}
		if locked.WebhookState() == company.WebhookState() {
			return nil
		}
		return s.webhooks.Bind(tx).Updated(ctx, webhook.Company, company.ID)
	})
}

// Delete soft-deletes the company after clearing company_id on every lead,
// customer and deal that points at it, all in ONE transaction: a company must
// never vanish behind a link that still names it, and a failure halfway must
// leave the links exactly as they were.
//
// Lock order (#182): the transaction first locks the live company row
// (CompanyRepository.LockLive, FOR UPDATE by primary key on MySQL/MariaDB),
// then unlinks leads, customers and deals, then soft-deletes — the shared
// order companies -> leads -> customers -> deals that lead conversion and lead
// update follow too. A conversion or update that linked the company and
// committed first is unlinked here; one that waits on the company lock finds
// it deleted. A company deleted between the plain pre-read and the lock is
// apperrors.ErrNotFound with nothing written.
//
// With webhooks wired (#517) the transaction writes company.deleted for every
// active endpoint subscribed to it after the soft delete; the unlinked leads,
// customers and deals get no updated event.
func (s *companyService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_id", id), "CompanyService", "Delete")

	if _, err := s.companyRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Company not found")
			return fmt.Errorf("company %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}
	if s.afterDeletePreRead != nil {
		s.afterDeletePreRead(id)
	}

	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.companyRepo.WithTx(tx)
		if err := repo.LockLive(id); err != nil {
			if isNotFound(err) {
				logger.Warn("Company deleted before its lock was taken")
				return fmt.Errorf("company %d not found: %w", id, apperrors.ErrNotFound)
			}
			logger.WithError(utils.LogError(err)).Error("Failed to lock company")
			return err
		}
		if s.afterDeleteLock != nil {
			s.afterDeleteLock(id)
		}
		if err := repo.UnlinkLeads(id); err != nil {
			return err
		}
		if err := repo.UnlinkCustomers(id); err != nil {
			return err
		}
		if err := repo.UnlinkDeals(id); err != nil {
			return err
		}
		if err := repo.Delete(id); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("company %d not found: %w", id, apperrors.ErrNotFound)
			}
			return err
		}
		if s.webhooksWired() {
			if err := s.webhooks.Bind(tx).Deleted(ctx, webhook.Company, id); err != nil {
				return err
			}
		}
		if s.afterDeleteUnlink != nil {
			s.afterDeleteUnlink(id)
		}
		return nil
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Company deleted successfully")
	return nil
}

func (s *companyService) List(offset, limit int, search, sortBy, sortOrder string) ([]models.Company, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "CompanyService", "List")

	companies, total, err := s.companyRepo.List(offset, limit, search, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if companies == nil {
		companies = []models.Company{}
	}

	logger.WithField("total", total).Info("Companies listed successfully")
	return companies, total, nil
}

func (s *companyService) ListCustomers(companyID uint, offset, limit int) ([]models.Customer, int64, error) {
	if _, err := s.GetByID(companyID); err != nil {
		return nil, 0, err
	}
	customers, total, err := s.companyRepo.ListCustomers(companyID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	if customers == nil {
		customers = []models.Customer{}
	}
	return customers, total, nil
}

func (s *companyService) ListLeads(companyID uint, ownerID *uint, offset, limit int) ([]models.Lead, int64, error) {
	if _, err := s.GetByID(companyID); err != nil {
		return nil, 0, err
	}
	leads, total, err := s.companyRepo.ListLeads(companyID, ownerID, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	if leads == nil {
		leads = []models.Lead{}
	}
	return leads, total, nil
}

// prepare normalises the company in place and runs every rule that needs the
// database: the owner has to be a live account, and the domain has to be free
// among live companies. The stored value is exactly what was validated.
func (s *companyService) prepare(company *models.Company) error {
	if err := normalizeCompany(company); err != nil {
		return err
	}

	if company.OwnerID != nil {
		if _, err := s.userRepo.GetByID(*company.OwnerID); err != nil {
			if isNotFound(err) {
				return fmt.Errorf("unknown owner_id %d: %w", *company.OwnerID, apperrors.ErrAssigneeNotFound)
			}
			return err
		}
	}

	if company.Domain != "" {
		exists, err := s.companyRepo.ExistsByDomain(company.Domain, company.ID)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("domain %q is already used by another company: %w", company.Domain, apperrors.ErrDuplicateCompanyDomain)
		}
	}

	return nil
}

// normalizeCompany trims the name, normalises the domain and checks the
// website. Length bounds are the handler's job (with the column widths as the
// source of truth); the rules here are the ones that change or interpret the
// value.
func normalizeCompany(company *models.Company) error {
	company.Name = strings.TrimSpace(company.Name)
	if company.Name == "" {
		return fmt.Errorf("company name is required: %w", apperrors.ErrValidation)
	}
	if len([]rune(company.Name)) > models.CompanyNameMaxLength {
		return fmt.Errorf("company name is longer than %d characters: %w", models.CompanyNameMaxLength, apperrors.ErrValidation)
	}

	domain, err := NormalizeCompanyDomain(company.Domain)
	if err != nil {
		return err
	}
	company.Domain = domain

	company.Website = strings.TrimSpace(company.Website)
	if company.Website != "" {
		parsed, err := url.Parse(company.Website)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("website must be an http or https URL: %w", apperrors.ErrValidation)
		}
	}

	return nil
}

// NormalizeCompanyDomain reduces whatever was typed into the domain field to a
// bare host: lower case, no scheme, no path, query or fragment, no leading
// "www.", no trailing dot. "https://WWW.Acme.com/about" and "acme.com" are the
// same company, and the duplicate check would miss that unless both are stored
// the same way. An empty input stays empty; anything that is not a host after
// the reduction (spaces, a port, an @, an empty remainder) is a validation
// error.
func NormalizeCompanyDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return "", nil
	}
	if i := strings.Index(domain, "://"); i >= 0 {
		domain = domain[i+3:]
	}
	if i := strings.IndexAny(domain, "/?#"); i >= 0 {
		domain = domain[:i]
	}
	domain = strings.TrimPrefix(domain, "www.")
	domain = strings.TrimSuffix(domain, ".")

	if domain == "" {
		return "", fmt.Errorf("%q is not a domain: %w", raw, apperrors.ErrValidation)
	}
	for _, r := range domain {
		if r == '.' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return "", fmt.Errorf("%q is not a domain: %w", raw, apperrors.ErrValidation)
	}
	return domain, nil
}
