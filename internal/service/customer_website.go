package service

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
)

// Both customer writes and lead conversion pass through this validation.
func normalizeCustomerWebsite(customer *models.Customer) error {
	customer.Website = strings.TrimSpace(customer.Website)
	if utf8.RuneCountInString(customer.Website) > models.CustomerWebsiteMaxLength {
		return fmt.Errorf("website must be %d characters or fewer: %w", models.CustomerWebsiteMaxLength, apperrors.ErrValidation)
	}
	if customer.Website == "" {
		return nil
	}
	parsed, err := url.Parse(customer.Website)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("website must be an http or https URL: %w", apperrors.ErrValidation)
	}
	return nil
}
