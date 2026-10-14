package service

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func capturePrivacyLogs(t *testing.T) (*bytes.Buffer, *logtest.Hook) {
	t.Helper()
	old := utils.Logger
	logger := logrus.New()
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.DebugLevel)
	hook := logtest.NewLocal(logger)
	utils.Logger = logger
	t.Cleanup(func() { utils.Logger = old })
	return &output, hook
}

func TestServiceLoggingPrivacy_SuccessAndSearch(t *testing.T) {
	const email = "service-email-canary@example.test"
	const text = "service-private-notes-canary"

	t.Run("lead creation retains its ID", func(t *testing.T) {
		output, hook := capturePrivacyLogs(t)
		repo := new(mocks.LeadRepository)
		lead := &models.Lead{Email: email, Notes: text}
		repo.On("Create", lead).Return(nil).Run(func(mock.Arguments) { lead.ID = 41 })
		require.NoError(t, NewLeadService(repo, nil, nil, nil).Create(lead))
		assert.NotContains(t, output.String(), email)
		assert.NotContains(t, output.String(), text)
		assert.Equal(t, "LeadService", hook.LastEntry().Data["service"])
		assert.Equal(t, "Create", hook.LastEntry().Data["method"])
		assert.Equal(t, uint(41), hook.LastEntry().Data["lead_id"])
		repo.AssertExpectations(t)
	})

	t.Run("task creation omits title on both events", func(t *testing.T) {
		output, hook := capturePrivacyLogs(t)
		repo := new(mocks.TaskRepository)
		users := new(mocks.UserRepository)
		task := &models.Task{Title: text, AssignedToID: 9}
		users.On("GetByID", uint(9)).Return(&models.User{IsActive: true}, nil)
		repo.On("Create", task).Return(nil).Run(func(mock.Arguments) { task.ID = 42 })
		require.NoError(t, NewTaskService(repo, users, nil, nil, nil).Create(task))
		assert.NotContains(t, output.String(), text)
		assert.Equal(t, uint(42), hook.LastEntry().Data["task_id"])
		repo.AssertExpectations(t)
		users.AssertExpectations(t)
	})

	t.Run("customer search and export retain counts", func(t *testing.T) {
		output, hook := capturePrivacyLogs(t)
		repo := new(mocks.CustomerRepository)
		customers := []models.Customer{{Email: email, Notes: text}}
		repo.On("Search", email, 0, 10, text, text, []string(nil)).Return(customers, nil)
		repo.On("CountSearch", email).Return(int64(1), nil)
		repo.On("ListAllForExport", email, text, text).Return(customers, nil)
		svc := NewCustomerService(repo, nil, nil)
		_, total, err := svc.Search(email, 0, 10, text, text)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, int64(1), hook.LastEntry().Data["total"])
		_, err = svc.ExportAll(email, text, text)
		require.NoError(t, err)
		assert.Equal(t, 1, hook.LastEntry().Data["count"])
		assert.NotContains(t, output.String(), email)
		assert.NotContains(t, output.String(), text)
		repo.AssertExpectations(t)
	})
}

func TestServiceLoggingPrivacy_ErrorsPreserveReturnedDetails(t *testing.T) {
	const canary = "error-email-canary@example.test"

	t.Run("customer persistence error keeps SQL code", func(t *testing.T) {
		output, hook := capturePrivacyLogs(t)
		repo := new(mocks.CustomerRepository)
		customer := &models.Customer{Email: canary}
		dbError := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry '" + canary + "' for key 'customers.email'"}
		repo.On("GetByEmailUnscoped", canary).Return(nil, gorm.ErrRecordNotFound)
		repo.On("Create", customer).Return(dbError)
		err := NewCustomerService(repo, nil, nil).Create(customer)
		assert.Same(t, dbError, err)
		assert.Contains(t, err.Error(), canary)
		assert.NotContains(t, output.String(), canary)
		assert.Contains(t, output.String(), "mysql_code=1062")
		assert.Equal(t, logrus.ErrorLevel, hook.LastEntry().Level)
		assert.Equal(t, "CustomerService", hook.LastEntry().Data["service"])
		repo.AssertExpectations(t)
	})

	tests := []struct {
		name    string
		service string
		call    func() error
	}{
		{"company domain", "CompanyService", func() error {
			return (&companyService{}).Create(&models.Company{Name: canary, Domain: canary})
		}},
		{"deal stage", "DealService", func() error {
			return (&dealService{}).Create(&models.Deal{Title: canary, Stage: models.DealStage(canary), Currency: "EUR"}, nil, 1)
		}},
		{"form status", "FormService", func() error {
			return (&formService{}).Create(&models.Form{Name: canary, Status: models.FormStatus(canary)}, 1)
		}},
		{"form notification email", "FormService", func() error {
			return (&formService{}).Create(&models.Form{Name: canary, Fields: []models.FormFieldDef{{Name: "email", Type: "email"}}, NotifyEmails: []string{"invalid@" + canary}}, 1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, hook := capturePrivacyLogs(t)
			err := tt.call()
			require.Error(t, err)
			assert.ErrorIs(t, err, apperrors.ErrValidation)
			assert.Contains(t, err.Error(), canary)
			assert.NotContains(t, output.String(), canary)
			assert.Equal(t, logrus.WarnLevel, hook.LastEntry().Level)
			assert.Equal(t, tt.service, hook.LastEntry().Data["service"])
			assert.Equal(t, "Create", hook.LastEntry().Data["method"])
		})
	}
}

func TestAuthLoggingPrivacy_CredentialsAndTokens(t *testing.T) {
	const email = "auth-email-canary@example.test"
	const password = "Auth-password-canary!123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)

	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			output, hook := capturePrivacyLogs(t)
			repo := new(MockUserRepository)
			if success {
				user := &models.User{BaseModel: models.BaseModel{ID: 73}, Email: email, Password: string(hash), Role: models.RoleAdmin, IsActive: true}
				repo.On("GetByEmail", email).Return(user, nil)
				repo.On("UpdateLastLogin", uint(73)).Return(nil)
			} else {
				repo.On("GetByEmail", email).Return(nil, gorm.ErrRecordNotFound)
			}
			svc := NewAuthService(repo, nil, config.JWTConfig{Secret: "test-only-secret", ExpiryHours: 1})
			token, err := svc.Login(email, password)
			if success {
				require.NoError(t, err)
				require.NotEmpty(t, token)
				assert.NotContains(t, output.String(), token)
				assert.Equal(t, uint(73), hook.LastEntry().Data["user_id"])
			} else {
				require.Error(t, err)
				assert.Empty(t, token)
				assert.Equal(t, logrus.WarnLevel, hook.LastEntry().Level)
			}
			assert.NotContains(t, output.String(), email)
			assert.NotContains(t, output.String(), password)
			assert.NotContains(t, output.String(), string(hash))
			assert.Equal(t, "AuthService", hook.LastEntry().Data["service"])
			assert.Equal(t, "Login", hook.LastEntry().Data["method"])
			repo.AssertExpectations(t)
		})
	}
}
