package mocks

import (
	time "time"

	models "github.com/florinel-chis/gophercrm/internal/models"
	mock "github.com/stretchr/testify/mock"
)

// APIKeyService is a mock type for the APIKeyService type
type APIKeyService struct {
	mock.Mock
}

// Generate provides a mock function with given fields: userID, name, expiresAt
func (_m *APIKeyService) Generate(userID uint, name string, expiresAt *time.Time) (string, *models.APIKey, error) {
	ret := _m.Called(userID, name, expiresAt)

	if len(ret) == 0 {
		panic("no return value specified for Generate")
	}

	var r0 string
	var r1 *models.APIKey
	var r2 error
	if rf, ok := ret.Get(0).(func(uint, string, *time.Time) (string, *models.APIKey, error)); ok {
		return rf(userID, name, expiresAt)
	}
	if rf, ok := ret.Get(0).(func(uint, string, *time.Time) string); ok {
		r0 = rf(userID, name, expiresAt)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(uint, string, *time.Time) *models.APIKey); ok {
		r1 = rf(userID, name, expiresAt)
	} else {
		if ret.Get(1) != nil {
			r1 = ret.Get(1).(*models.APIKey)
		}
	}

	if rf, ok := ret.Get(2).(func(uint, string, *time.Time) error); ok {
		r2 = rf(userID, name, expiresAt)
	} else {
		r2 = ret.Error(2)
	}

	return r0, r1, r2
}

// GetByID provides a mock function with given fields: id, userID
func (_m *APIKeyService) GetByID(id uint, userID uint) (*models.APIKey, error) {
	ret := _m.Called(id, userID)

	if len(ret) == 0 {
		panic("no return value specified for GetByID")
	}

	var r0 *models.APIKey
	var r1 error
	if rf, ok := ret.Get(0).(func(uint, uint) (*models.APIKey, error)); ok {
		return rf(id, userID)
	}
	if rf, ok := ret.Get(0).(func(uint, uint) *models.APIKey); ok {
		r0 = rf(id, userID)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*models.APIKey)
		}
	}

	if rf, ok := ret.Get(1).(func(uint, uint) error); ok {
		r1 = rf(id, userID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Update provides a mock function with given fields: id, userID, name, isActive
func (_m *APIKeyService) Update(id uint, userID uint, name *string, isActive *bool) (*models.APIKey, error) {
	ret := _m.Called(id, userID, name, isActive)

	if len(ret) == 0 {
		panic("no return value specified for Update")
	}

	var r0 *models.APIKey
	var r1 error
	if rf, ok := ret.Get(0).(func(uint, uint, *string, *bool) (*models.APIKey, error)); ok {
		return rf(id, userID, name, isActive)
	}
	if rf, ok := ret.Get(0).(func(uint, uint, *string, *bool) *models.APIKey); ok {
		r0 = rf(id, userID, name, isActive)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*models.APIKey)
		}
	}

	if rf, ok := ret.Get(1).(func(uint, uint, *string, *bool) error); ok {
		r1 = rf(id, userID, name, isActive)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetByUser provides a mock function with given fields: userID
func (_m *APIKeyService) GetByUser(userID uint) ([]models.APIKey, error) {
	ret := _m.Called(userID)

	if len(ret) == 0 {
		panic("no return value specified for GetByUser")
	}

	var r0 []models.APIKey
	var r1 error
	if rf, ok := ret.Get(0).(func(uint) ([]models.APIKey, error)); ok {
		return rf(userID)
	}
	if rf, ok := ret.Get(0).(func(uint) []models.APIKey); ok {
		r0 = rf(userID)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.APIKey)
		}
	}

	if rf, ok := ret.Get(1).(func(uint) error); ok {
		r1 = rf(userID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Revoke provides a mock function with given fields: id, userID
func (_m *APIKeyService) Revoke(id uint, userID uint) error {
	ret := _m.Called(id, userID)

	if len(ret) == 0 {
		panic("no return value specified for Revoke")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(uint, uint) error); ok {
		r0 = rf(id, userID)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// List provides a mock function with given fields: userID
func (_m *APIKeyService) List(userID uint) ([]models.APIKey, error) {
	ret := _m.Called(userID)

	if len(ret) == 0 {
		panic("no return value specified for List")
	}

	var r0 []models.APIKey
	var r1 error
	if rf, ok := ret.Get(0).(func(uint) ([]models.APIKey, error)); ok {
		return rf(userID)
	}
	if rf, ok := ret.Get(0).(func(uint) []models.APIKey); ok {
		r0 = rf(userID)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.APIKey)
		}
	}

	if rf, ok := ret.Get(1).(func(uint) error); ok {
		r1 = rf(userID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewAPIKeyService creates a new instance of APIKeyService. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
func NewAPIKeyService(t interface {
	mock.TestingT
	Cleanup(func())
}) *APIKeyService {
	mock := &APIKeyService{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
