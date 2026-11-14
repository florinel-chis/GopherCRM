package service

// Regression roots of #182 for CompanyService.Delete: inside its transaction
// the delete locks the live company (CompanyRepository.LockLive) before
// UnlinkLeads, UnlinkCustomers, UnlinkDeals and the soft delete, and a company
// that vanished between the plain pre-read and that lock is
// apperrors.ErrNotFound with nothing written.

import (
	"errors"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCompanyServiceDeleteLocksCompanyFirst(t *testing.T) {
	txManager := utils.NewTransactionManager(openQuoteTestSQLite(t))

	newRepo := func(calls *[]string, lockErr error) *mocks.CompanyRepository {
		repo := new(mocks.CompanyRepository)
		repo.On("GetByID", uint(9)).Return(&models.Company{BaseModel: models.BaseModel{ID: 9}}, nil)
		repo.On("WithTx", mock.Anything).Return(repo)
		record := func(name string) func(mock.Arguments) {
			return func(mock.Arguments) { *calls = append(*calls, name) }
		}
		repo.On("LockLive", uint(9)).Return(lockErr).Run(record("LockLive"))
		repo.On("UnlinkLeads", uint(9)).Return(nil).Run(record("UnlinkLeads")).Maybe()
		repo.On("UnlinkCustomers", uint(9)).Return(nil).Run(record("UnlinkCustomers")).Maybe()
		repo.On("UnlinkDeals", uint(9)).Return(nil).Run(record("UnlinkDeals")).Maybe()
		repo.On("Delete", uint(9)).Return(nil).Run(record("Delete")).Maybe()
		return repo
	}

	t.Run("the company is locked before every unlink and the soft delete", func(t *testing.T) {
		var calls []string
		repo := newRepo(&calls, nil)
		require.NoError(t, NewCompanyService(repo, nil, txManager).Delete(9))
		assert.Equal(t, []string{"LockLive", "UnlinkLeads", "UnlinkCustomers", "UnlinkDeals", "Delete"}, calls)
	})

	t.Run("a company the lock does not find is not found, and nothing is written", func(t *testing.T) {
		var calls []string
		repo := newRepo(&calls, gorm.ErrRecordNotFound)
		err := NewCompanyService(repo, nil, txManager).Delete(9)
		require.Error(t, err)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound), "got %v", err)
		assert.Equal(t, []string{"LockLive"}, calls)
		for _, method := range []string{"UnlinkLeads", "UnlinkCustomers", "UnlinkDeals", "Delete"} {
			repo.AssertNotCalled(t, method, mock.Anything)
		}
	})

	t.Run("a lock failure is not a miss and writes nothing", func(t *testing.T) {
		var calls []string
		repo := newRepo(&calls, errors.New("lock wait aborted"))
		err := NewCompanyService(repo, nil, txManager).Delete(9)
		require.Error(t, err)
		assert.False(t, apperrors.IsNotFound(err))
		assert.Equal(t, []string{"LockLive"}, calls)
	})
}

// TestCompanyServiceDeleteRacedAfterPreReadSQLite soft-deletes the company
// between the delete's plain pre-read and its transaction through the
// instance-local afterDeletePreRead seam, with the real repositories.
func TestCompanyServiceDeleteRacedAfterPreReadSQLite(t *testing.T) {
	f := newLeadCompanyFixture(t, openAutomationSQLite(t))
	company := f.company()
	lead := f.lead(company)
	customer := f.env.createCustomer()
	require.NoError(t, f.db.Model(&models.Customer{}).Where("id = ?", customer.ID).Update("company_id", company.ID).Error)
	deal := f.env.createDeal(f.env.sales, models.DealStageQualification, 1000, "EUR", &customer.ID, nil)
	require.NoError(t, f.db.Model(&models.Deal{}).Where("id = ?", deal.ID).Update("company_id", company.ID).Error)

	// The racing delete is a plain soft delete of the company row: it leaves
	// the links in place, so any unlink by the service under test would show.
	f.companySvc.afterDeletePreRead = func(id uint) {
		require.NoError(t, f.db.Delete(&models.Company{}, id).Error)
	}

	err := f.companySvc.Delete(company.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperrors.ErrNotFound), "got %v", err)

	storedLead := f.storedLead(lead.ID)
	require.NotNil(t, storedLead.CompanyID, "no unlink was issued")
	assert.Equal(t, company.ID, *storedLead.CompanyID)
	storedCustomer := f.storedCustomer(customer.ID)
	require.NotNil(t, storedCustomer.CompanyID)
	assert.Equal(t, company.ID, *storedCustomer.CompanyID)
	var storedDeal models.Deal
	require.NoError(t, f.db.Unscoped().First(&storedDeal, deal.ID).Error)
	require.NotNil(t, storedDeal.CompanyID)
	assert.Equal(t, company.ID, *storedDeal.CompanyID)
}
