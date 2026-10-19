package service

import (
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomerWebsiteValidation(t *testing.T) {
	for _, value := range []string{"", "  ", "http://example.com", " https://example.com/about?q=1#team ", "https://example.com/" + strings.Repeat("a", models.CustomerWebsiteMaxLength-len("https://example.com/"))} {
		t.Run(value, func(t *testing.T) {
			customer := &models.Customer{Website: value}
			require.NoError(t, normalizeCustomerWebsite(customer))
			assert.Equal(t, strings.TrimSpace(value), customer.Website)
		})
	}
	for _, value := range []string{"example.com", "javascript:alert(1)", "ftp://example.com", "https:///path", "https://:443", "https://bad host", "https://example.com/" + strings.Repeat("a", models.CustomerWebsiteMaxLength-len("https://example.com/")+1)} {
		t.Run(value, func(t *testing.T) {
			customer := &models.Customer{Website: value}
			// Invalid values must be rejected before any repository write, including conversion.
			s := NewCustomerService(nil, nil, nil)
			assert.ErrorIs(t, s.Create(customer), apperrors.ErrValidation)
			assert.ErrorIs(t, s.Update(customer), apperrors.ErrValidation)
			leadService := NewLeadService(nil, nil, nil, nil)
			_, err := leadService.ConvertToCustomer(1, customer)
			assert.ErrorIs(t, err, apperrors.ErrValidation)
		})
	}
}
