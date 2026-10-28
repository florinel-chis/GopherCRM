package service

import (
	"fmt"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

type productService struct {
	productRepo repository.ProductRepository
	currency    DealCurrencySource
}

// NewProductService wires the catalogue service. currency is the configuration
// read behind the default currency (deals.default_currency); nil means the
// shipped EUR.
func NewProductService(productRepo repository.ProductRepository, currency DealCurrencySource) ProductService {
	return &productService{productRepo: productRepo, currency: currency}
}

func (s *productService) Create(product *models.Product) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "product"), "ProductService", "Create")

	if product.Currency == "" {
		product.Currency = defaultDealCurrency(s.currency)
	}
	if err := validateProduct(product); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Product rejected")
		return err
	}
	if err := s.productRepo.Create(product); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("product_id", product.ID).Info("Product created successfully")
	return nil
}

func (s *productService) GetByID(id uint) (*models.Product, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("product_id", id), "ProductService", "GetByID")

	product, err := s.productRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Product not found")
			return nil, fmt.Errorf("product %d not found: %w", id, apperrors.ErrProductNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return product, nil
}

// Update saves the product as given; the handler has already merged the
// request onto the stored row. Quote lines that copied the old name or price
// are deliberately left alone.
func (s *productService) Update(product *models.Product) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("product_id", product.ID), "ProductService", "Update")

	if _, err := s.GetByID(product.ID); err != nil {
		return err
	}
	if err := validateProduct(product); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Product rejected")
		return err
	}
	if err := s.productRepo.Update(product); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Product updated successfully")
	return nil
}

func (s *productService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("product_id", id), "ProductService", "Delete")

	if err := s.productRepo.Delete(id); err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Product not found")
			return fmt.Errorf("product %d not found: %w", id, apperrors.ErrProductNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Product deleted successfully")
	return nil
}

func (s *productService) List(offset, limit int, filter repository.ProductListFilter) ([]models.Product, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "ProductService", "List")

	products, total, err := s.productRepo.List(offset, limit, filter)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if products == nil {
		products = []models.Product{}
	}
	return products, total, nil
}

// validateProduct runs the domain rules on the fields. The widths are the
// model's constants, which a test holds to the declared column widths.
func validateProduct(product *models.Product) error {
	product.Name = strings.TrimSpace(product.Name)
	product.SKU = strings.TrimSpace(product.SKU)
	if product.Name == "" {
		return fmt.Errorf("product name is required: %w", apperrors.ErrValidation)
	}
	if len([]rune(product.Name)) > models.ProductNameMaxLength {
		return fmt.Errorf("product name is longer than %d characters: %w", models.ProductNameMaxLength, apperrors.ErrValidation)
	}
	if len([]rune(product.SKU)) > models.ProductSKUMaxLength {
		return fmt.Errorf("sku is longer than %d characters: %w", models.ProductSKUMaxLength, apperrors.ErrValidation)
	}
	if len(product.Description) > models.ProductDescriptionMaxBytes {
		return fmt.Errorf("description is longer than %d bytes: %w", models.ProductDescriptionMaxBytes, apperrors.ErrValidation)
	}
	if product.UnitPriceCents < 0 {
		return fmt.Errorf("unit_price_cents must not be negative: %w", apperrors.ErrValidation)
	}
	if product.UnitPriceCents > models.QuoteAmountCentsMax {
		return fmt.Errorf("unit_price_cents must be at most %d: %w", models.QuoteAmountCentsMax, apperrors.ErrValidation)
	}
	if !dealCurrencyPattern.MatchString(product.Currency) {
		return fmt.Errorf("currency must be a three-letter upper-case ISO 4217 code: %w", apperrors.ErrValidation)
	}
	return nil
}

// defaultDealCurrency reads deals.default_currency the way the deal service
// does: a trimmed, upper-cased three-letter code, else the shipped EUR with a
// warning. Products and quotes take the same default as deals.
func defaultDealCurrency(source DealCurrencySource) string {
	if source == nil {
		return models.DealDefaultCurrency
	}
	value, err := source.GetString(ConfigDealsDefaultCurrency)
	if err != nil {
		utils.Logger.WithError(utils.LogError(err)).WithField("config_key", ConfigDealsDefaultCurrency).
			Warn("Default currency could not be read; using the shipped default")
		return models.DealDefaultCurrency
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	if !dealCurrencyPattern.MatchString(value) {
		utils.Logger.WithField("config_key", ConfigDealsDefaultCurrency).
			Warn("Configured default currency is not a three-letter code; using the shipped default")
		return models.DealDefaultCurrency
	}
	return value
}
