package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestTicketCreateCommentFirstResponse pins which repository method a comment
// reaches (#278): a non-internal comment whose author, as the service reads
// it, is admin or support goes through CreateCommentWithFirstResponse; an
// internal note, or an author of any other role, through the plain
// CreateComment. The author's role comes from the service's own read, never
// from the comment. A repository error from either method is returned.
func TestTicketCreateCommentFirstResponse(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})

	const ticketID, authorID = uint(18), uint(5)
	ticket := &models.Ticket{BaseModel: models.BaseModel{ID: ticketID}, Title: "Ticket", Status: models.TicketStatusOpen}
	repoFault := errors.New("insert failed")

	cases := []struct {
		name       string
		role       models.UserRole
		internal   bool
		wantStamps bool
	}{
		{name: "admin reply", role: models.RoleAdmin, wantStamps: true},
		{name: "support reply", role: models.RoleSupport, wantStamps: true},
		{name: "admin internal note", role: models.RoleAdmin, internal: true},
		{name: "support internal note", role: models.RoleSupport, internal: true},
		{name: "sales author", role: models.RoleSales},
		{name: "customer author", role: models.RoleCustomer},
		{name: "sales internal note", role: models.RoleSales, internal: true},
		{name: "author with an unknown role", role: models.UserRole("auditor")},
		{name: "author with no role", role: models.UserRole("")},
	}

	newService := func(role models.UserRole) (TicketService, *mocks.TicketRepository, *mocks.UserRepository) {
		ticketRepo := new(mocks.TicketRepository)
		userRepo := new(mocks.UserRepository)
		ticketRepo.On("GetByID", ticketID).Return(ticket, nil)
		userRepo.On("GetByID", authorID).Return(&models.User{BaseModel: models.BaseModel{ID: authorID}, Role: role}, nil)
		return NewTicketService(ticketRepo, new(mocks.CustomerRepository), userRepo), ticketRepo, userRepo
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, fault := range []error{nil, repoFault} {
				svc, ticketRepo, userRepo := newService(tc.role)
				want, other := "CreateComment", "CreateCommentWithFirstResponse"
				if tc.wantStamps {
					want, other = other, want
				}
				ticketRepo.On(want, mock.MatchedBy(func(c *models.TicketComment) bool {
					return c.TicketID == ticketID && c.AuthorUserID == authorID && c.IsInternal == tc.internal
				})).Return(fault).Once()

				comment := &models.TicketComment{TicketID: ticketID, AuthorUserID: authorID, Body: "Hello", IsInternal: tc.internal}
				err := svc.CreateComment(comment)

				ticketRepo.AssertExpectations(t)
				userRepo.AssertExpectations(t)
				ticketRepo.AssertNotCalled(t, other, mock.Anything)
				if fault != nil {
					require.Error(t, err, "a repository error is not reported as success")
					assert.True(t, errors.Is(err, repoFault), "got %v", err)
					continue
				}
				require.NoError(t, err)
				assert.Equal(t, authorID, comment.UserID)
				assert.Equal(t, "Hello", comment.Content)
			}
		})
	}

	t.Run("the role on the read wins over the caller's assumptions", func(t *testing.T) {
		// A staff account demoted to sales before the comment is written is
		// treated as sales: the role is read by the service.
		svc, ticketRepo, _ := newService(models.RoleSales)
		ticketRepo.On("CreateComment", mock.Anything).Return(nil).Once()
		comment := &models.TicketComment{TicketID: ticketID, AuthorUserID: authorID, Body: "Hi",
			Author: &models.User{BaseModel: models.BaseModel{ID: authorID}, Role: models.RoleAdmin}}
		require.NoError(t, svc.CreateComment(comment))
		ticketRepo.AssertNotCalled(t, "CreateCommentWithFirstResponse", mock.Anything)
		assert.Equal(t, models.RoleSales, comment.Author.Role)
	})

	t.Run("a missing ticket or author writes nothing", func(t *testing.T) {
		ticketRepo := new(mocks.TicketRepository)
		ticketRepo.On("GetByID", ticketID).Return(nil, errors.New("record not found"))
		svc := NewTicketService(ticketRepo, new(mocks.CustomerRepository), new(mocks.UserRepository))
		require.Error(t, svc.CreateComment(&models.TicketComment{TicketID: ticketID, AuthorUserID: authorID, Body: "x"}))
		ticketRepo.AssertNotCalled(t, "CreateComment", mock.Anything)
		ticketRepo.AssertNotCalled(t, "CreateCommentWithFirstResponse", mock.Anything)

		ticketRepo = new(mocks.TicketRepository)
		userRepo := new(mocks.UserRepository)
		ticketRepo.On("GetByID", ticketID).Return(ticket, nil)
		userRepo.On("GetByID", authorID).Return(nil, errors.New("lookup failed"))
		svc = NewTicketService(ticketRepo, new(mocks.CustomerRepository), userRepo)
		require.Error(t, svc.CreateComment(&models.TicketComment{TicketID: ticketID, AuthorUserID: authorID, Body: "x"}))
		ticketRepo.AssertNotCalled(t, "CreateComment", mock.Anything)
		ticketRepo.AssertNotCalled(t, "CreateCommentWithFirstResponse", mock.Anything)
	})
}
