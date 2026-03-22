package mocks

import (
	models "github.com/florinel-chis/gophercrm/internal/models"
	mock "github.com/stretchr/testify/mock"
)

// APIKeyService is a mock type for the APIKeyService type
type APIKeyService struct {
	mock.Mock
}

// Generate provides a mock function with given fields: userID, name
func (_m *APIKeyService) Generate(userID uint, name string) (string, *models.APIKey, error) {
	ret := _m.Called(userID, name)

	if len(ret) == 0 {
		panic("no return value specified for Generate")
	}

	var r0 string
	var r1 *models.APIKey
	var r2 error
	if rf, ok := ret.Get(0).(func(uint, string) (string, *models.APIKey, error)); ok {
		return rf(userID, name)
	}
	if rf, ok := ret.Get(0).(func(uint, string) string); ok {
		r0 = rf(userID, name)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(uint, string) *models.APIKey); ok {
		r1 = rf(userID, name)
	} else {
		if ret.Get(1) != nil {
			r1 = ret.Get(1).(*models.APIKey)
		}
	}

	if rf, ok := ret.Get(2).(func(uint, string) error); ok {
		r2 = rf(userID, name)
	} else {
		r2 = ret.Error(2)
	}

	return r0, r1, r2
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
