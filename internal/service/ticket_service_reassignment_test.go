package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestTicketServiceUpdateDetachesStaleAssignee pins what the repository is
// handed: GORM's Save writes a preloaded belongs-to id back over the foreign key,
// so an attached user that differs from AssignedToID must not reach it. Each
// case's ticket is the handler's read with the request applied, and existing is
// the service's own, separate read. A user lookup the case does not expect panics
// in the mock.
func TestTicketServiceUpdateDetachesStaleAssignee(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})

	oldUser := &models.User{BaseModel: models.BaseModel{ID: 2}, Email: "old@example.com", Role: models.RoleSupport}
	newUser := &models.User{BaseModel: models.BaseModel{ID: 3}, Email: "new@example.com", Role: models.RoleSupport}
	repoFault := errors.New("repository write failed")

	ticketWith := func(fk *uint, attached *models.User) *models.Ticket {
		return &models.Ticket{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket", Status: models.TicketStatusOpen,
			AssignedToID: fk, AssignedTo: attached}
	}

	cases := []struct {
		name         string
		ticket       *models.Ticket
		existing     *models.Ticket
		lookup       *models.User // the user lookup the case expects, nil for none
		repoErr      error
		wantAtWrite  *models.User // AssignedTo as the repository sees it
		wantFKAtSave *uint
		wantAfter    *models.User // AssignedTo once Update has returned
	}{
		{
			name:         "a validated change detaches the preloaded old assignee and attaches the new one",
			ticket:       ticketWith(uintPtr(3), oldUser),
			existing:     ticketWith(uintPtr(2), oldUser),
			lookup:       newUser,
			wantFKAtSave: uintPtr(3),
			wantAfter:    newUser,
		},
		{
			name:         "the service's read already shows the requested assignee: detached, no lookup",
			ticket:       ticketWith(uintPtr(3), oldUser),
			existing:     ticketWith(uintPtr(3), newUser),
			wantFKAtSave: uintPtr(3),
			wantAfter:    newUser,
		},
		{
			name:         "an unassigned ticket gets the validated assignee attached",
			ticket:       ticketWith(uintPtr(3), nil),
			existing:     ticketWith(nil, nil),
			lookup:       newUser,
			wantFKAtSave: uintPtr(3),
			wantAfter:    newUser,
		},
		{
			name:         "an unassigned handler read and a service read showing the requested user",
			ticket:       ticketWith(uintPtr(3), nil),
			existing:     ticketWith(uintPtr(3), newUser),
			wantFKAtSave: uintPtr(3),
			wantAfter:    newUser,
		},
		{
			name:         "an unchanged assignee stays attached with no lookup",
			ticket:       ticketWith(uintPtr(2), oldUser),
			existing:     ticketWith(uintPtr(2), oldUser),
			wantAtWrite:  oldUser,
			wantFKAtSave: uintPtr(2),
			wantAfter:    oldUser,
		},
		{
			name:     "a nil foreign key detaches an attached user and attaches none, whatever the service read",
			ticket:   ticketWith(nil, oldUser),
			existing: ticketWith(uintPtr(2), oldUser),
		},
		{
			name:         "a repository error on a validated change is returned",
			ticket:       ticketWith(uintPtr(3), oldUser),
			existing:     ticketWith(uintPtr(2), oldUser),
			lookup:       newUser,
			repoErr:      repoFault,
			wantFKAtSave: uintPtr(3),
		},
		{
			name:         "a repository error without a lookup is returned",
			ticket:       ticketWith(uintPtr(3), oldUser),
			existing:     ticketWith(uintPtr(3), newUser),
			repoErr:      repoFault,
			wantFKAtSave: uintPtr(3),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticketRepo := new(mocks.TicketRepository)
			userRepo := new(mocks.UserRepository)
			svc := NewTicketService(ticketRepo, new(mocks.CustomerRepository), userRepo)

			ticketRepo.On("GetByID", uint(1)).Return(tc.existing, nil)
			if tc.lookup != nil {
				userRepo.On("GetByID", tc.lookup.ID).Return(tc.lookup, nil).Once()
			}
			var saved models.Ticket
			ticketRepo.On("Update", mock.Anything).Run(func(args mock.Arguments) {
				saved = *args.Get(0).(*models.Ticket)
			}).Return(tc.repoErr).Once()

			err := svc.Update(tc.ticket)

			ticketRepo.AssertExpectations(t)
			userRepo.AssertExpectations(t)
			assert.Same(t, tc.wantAtWrite, saved.AssignedTo, "AssignedTo at the write")
			assert.Equal(t, tc.wantFKAtSave, saved.AssignedToID, "AssignedToID at the write")
			if tc.lookup == nil {
				userRepo.AssertNotCalled(t, "GetByID", mock.Anything)
			}
			if tc.repoErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.repoErr), "got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Same(t, tc.wantAfter, tc.ticket.AssignedTo, "AssignedTo after the write")
			assert.Equal(t, tc.wantFKAtSave, tc.ticket.AssignedToID, "AssignedToID after the write")
		})
	}

	t.Run("a wrapped repository error still matches", func(t *testing.T) {
		ticketRepo := new(mocks.TicketRepository)
		svc := NewTicketService(ticketRepo, new(mocks.CustomerRepository), new(mocks.UserRepository))
		ticketRepo.On("GetByID", uint(1)).Return(ticketWith(uintPtr(2), oldUser), nil)
		ticketRepo.On("Update", mock.Anything).Return(fmt.Errorf("save: %w", repoFault)).Once()

		err := svc.Update(ticketWith(uintPtr(2), oldUser))
		assert.True(t, errors.Is(err, repoFault), "got %v", err)
	})

	t.Run("a refused assignee never reaches the repository", func(t *testing.T) {
		customerUser := &models.User{BaseModel: models.BaseModel{ID: 4}, Role: models.RoleCustomer}
		salesUser := &models.User{BaseModel: models.BaseModel{ID: 5}, Role: models.RoleSales}
		for _, refusal := range []struct {
			id        uint
			user      *models.User
			lookupErr error
			want      error
		}{
			{id: 999, lookupErr: gorm.ErrRecordNotFound, want: apperrors.ErrAssigneeNotFound},
			{id: 4, user: customerUser, want: apperrors.ErrInvalidAssigneeRole},
			{id: 5, user: salesUser, want: apperrors.ErrInvalidAssigneeRole},
		} {
			ticketRepo := new(mocks.TicketRepository)
			userRepo := new(mocks.UserRepository)
			svc := NewTicketService(ticketRepo, new(mocks.CustomerRepository), userRepo)
			ticketRepo.On("GetByID", uint(1)).Return(ticketWith(uintPtr(2), oldUser), nil)
			userRepo.On("GetByID", refusal.id).Return(refusal.user, refusal.lookupErr).Once()

			err := svc.Update(ticketWith(uintPtr(refusal.id), oldUser))
			assert.True(t, errors.Is(err, refusal.want), "assignee %d: got %v", refusal.id, err)
			ticketRepo.AssertNotCalled(t, "Update", mock.Anything)
		}
	})
}
