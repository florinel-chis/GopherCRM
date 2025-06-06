// Code generated by mockery v2.53.4. DO NOT EDIT.

package mocks

import (
	models "github.com/florinel-chis/gophercrm/internal/models"
	mock "github.com/stretchr/testify/mock"
)

// TicketRepository is an autogenerated mock type for the TicketRepository type
type TicketRepository struct {
	mock.Mock
}

// Count provides a mock function with no fields
func (_m *TicketRepository) Count() (int64, error) {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Count")
	}

	var r0 int64
	var r1 error
	if rf, ok := ret.Get(0).(func() (int64, error)); ok {
		return rf()
	}
	if rf, ok := ret.Get(0).(func() int64); ok {
		r0 = rf()
	} else {
		r0 = ret.Get(0).(int64)
	}

	if rf, ok := ret.Get(1).(func() error); ok {
		r1 = rf()
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// CountByAssignedToID provides a mock function with given fields: assignedToID
func (_m *TicketRepository) CountByAssignedToID(assignedToID uint) (int64, error) {
	ret := _m.Called(assignedToID)

	if len(ret) == 0 {
		panic("no return value specified for CountByAssignedToID")
	}

	var r0 int64
	var r1 error
	if rf, ok := ret.Get(0).(func(uint) (int64, error)); ok {
		return rf(assignedToID)
	}
	if rf, ok := ret.Get(0).(func(uint) int64); ok {
		r0 = rf(assignedToID)
	} else {
		r0 = ret.Get(0).(int64)
	}

	if rf, ok := ret.Get(1).(func(uint) error); ok {
		r1 = rf(assignedToID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// CountByCustomerID provides a mock function with given fields: customerID
func (_m *TicketRepository) CountByCustomerID(customerID uint) (int64, error) {
	ret := _m.Called(customerID)

	if len(ret) == 0 {
		panic("no return value specified for CountByCustomerID")
	}

	var r0 int64
	var r1 error
	if rf, ok := ret.Get(0).(func(uint) (int64, error)); ok {
		return rf(customerID)
	}
	if rf, ok := ret.Get(0).(func(uint) int64); ok {
		r0 = rf(customerID)
	} else {
		r0 = ret.Get(0).(int64)
	}

	if rf, ok := ret.Get(1).(func(uint) error); ok {
		r1 = rf(customerID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Create provides a mock function with given fields: ticket
func (_m *TicketRepository) Create(ticket *models.Ticket) error {
	ret := _m.Called(ticket)

	if len(ret) == 0 {
		panic("no return value specified for Create")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*models.Ticket) error); ok {
		r0 = rf(ticket)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Delete provides a mock function with given fields: id
func (_m *TicketRepository) Delete(id uint) error {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for Delete")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(uint) error); ok {
		r0 = rf(id)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// GetByAssignedToID provides a mock function with given fields: assignedToID, offset, limit
func (_m *TicketRepository) GetByAssignedToID(assignedToID uint, offset int, limit int) ([]models.Ticket, error) {
	ret := _m.Called(assignedToID, offset, limit)

	if len(ret) == 0 {
		panic("no return value specified for GetByAssignedToID")
	}

	var r0 []models.Ticket
	var r1 error
	if rf, ok := ret.Get(0).(func(uint, int, int) ([]models.Ticket, error)); ok {
		return rf(assignedToID, offset, limit)
	}
	if rf, ok := ret.Get(0).(func(uint, int, int) []models.Ticket); ok {
		r0 = rf(assignedToID, offset, limit)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.Ticket)
		}
	}

	if rf, ok := ret.Get(1).(func(uint, int, int) error); ok {
		r1 = rf(assignedToID, offset, limit)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetByCustomerID provides a mock function with given fields: customerID, offset, limit
func (_m *TicketRepository) GetByCustomerID(customerID uint, offset int, limit int) ([]models.Ticket, error) {
	ret := _m.Called(customerID, offset, limit)

	if len(ret) == 0 {
		panic("no return value specified for GetByCustomerID")
	}

	var r0 []models.Ticket
	var r1 error
	if rf, ok := ret.Get(0).(func(uint, int, int) ([]models.Ticket, error)); ok {
		return rf(customerID, offset, limit)
	}
	if rf, ok := ret.Get(0).(func(uint, int, int) []models.Ticket); ok {
		r0 = rf(customerID, offset, limit)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.Ticket)
		}
	}

	if rf, ok := ret.Get(1).(func(uint, int, int) error); ok {
		r1 = rf(customerID, offset, limit)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetByID provides a mock function with given fields: id
func (_m *TicketRepository) GetByID(id uint) (*models.Ticket, error) {
	ret := _m.Called(id)

	if len(ret) == 0 {
		panic("no return value specified for GetByID")
	}

	var r0 *models.Ticket
	var r1 error
	if rf, ok := ret.Get(0).(func(uint) (*models.Ticket, error)); ok {
		return rf(id)
	}
	if rf, ok := ret.Get(0).(func(uint) *models.Ticket); ok {
		r0 = rf(id)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*models.Ticket)
		}
	}

	if rf, ok := ret.Get(1).(func(uint) error); ok {
		r1 = rf(id)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// List provides a mock function with given fields: offset, limit
func (_m *TicketRepository) List(offset int, limit int) ([]models.Ticket, error) {
	ret := _m.Called(offset, limit)

	if len(ret) == 0 {
		panic("no return value specified for List")
	}

	var r0 []models.Ticket
	var r1 error
	if rf, ok := ret.Get(0).(func(int, int) ([]models.Ticket, error)); ok {
		return rf(offset, limit)
	}
	if rf, ok := ret.Get(0).(func(int, int) []models.Ticket); ok {
		r0 = rf(offset, limit)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.Ticket)
		}
	}

	if rf, ok := ret.Get(1).(func(int, int) error); ok {
		r1 = rf(offset, limit)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// Update provides a mock function with given fields: ticket
func (_m *TicketRepository) Update(ticket *models.Ticket) error {
	ret := _m.Called(ticket)

	if len(ret) == 0 {
		panic("no return value specified for Update")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*models.Ticket) error); ok {
		r0 = rf(ticket)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// NewTicketRepository creates a new instance of TicketRepository. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewTicketRepository(t interface {
	mock.TestingT
	Cleanup(func())
}) *TicketRepository {
	mock := &TicketRepository{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
