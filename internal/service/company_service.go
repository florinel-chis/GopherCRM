package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type companyService struct {
	companyRepo repository.CompanyRepository
	userRepo    repository.UserRepository
	txManager   *utils.TransactionManager
}

func NewCompanyService(companyRepo repository.CompanyRepository, userRepo repository.UserRepository, txManager *utils.TransactionManager) CompanyService {
	return &companyService{
		companyRepo: companyRepo,
		userRepo:    userRepo,
		txManager:   txManager,
	}
}

func (s *companyService) Create(company *models.Company) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_name", company.Name), "CompanyService", "Create")

	if err := s.prepare(company); err != nil {
		logger.WithError(err).Warn("Company rejected")
		return err
	}

	if err := s.companyRepo.Create(company); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("company_id", company.ID).Info("Company created successfully")
	return nil
}

func (s *companyService) GetByID(id uint) (*models.Company, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_id", id), "CompanyService", "GetByID")

	company, err := s.companyRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Company not found")
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
		logger.WithError(err).Warn("Company rejected")
		return err
	}

	if err := s.companyRepo.Update(company); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Company updated successfully")
	return nil
}

// Delete soft-deletes the company after clearing company_id on every lead,
// customer and deal that points at it, all in ONE transaction: a company must
// never vanish behind a link that still names it, and a failure halfway must
// leave the links exactly as they were.
func (s *companyService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("company_id", id), "CompanyService", "Delete")

	if _, err := s.companyRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Company not found")
			return fmt.Errorf("company %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		repo := s.companyRepo.WithTx(tx)
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
		"offset":     offset,
		"limit":      limit,
		"search":     search,
		"sort_by":    sortBy,
		"sort_order": sortOrder,
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
