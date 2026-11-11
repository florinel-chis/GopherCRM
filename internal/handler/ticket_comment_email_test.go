package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// POST /tickets/{id}/comments with email_customer (#219): the handler's checks
// and its error mapping, over a mocked TicketService.

type ticketMailCtxKey struct{}

// serveTicketComment runs one request through CreateComment as the given
// caller. The request context carries a marker so the test can see that the
// service receives c.Request.Context().
func serveTicketComment(t *testing.T, svc *mocks.TicketService, userID uint, role models.UserRole, ticketID string, body []byte) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewTicketHandler(svc)
	router.POST("/tickets/:id/comments", func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("user_role", string(role))
		h.CreateComment(c)
	})
	req := httptest.NewRequest(http.MethodPost, "/tickets/"+ticketID+"/comments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ticketMailCtxKey{}, "request-marker"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var envelope map[string]interface{}
	if w.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	}
	return w, envelope
}

func ticketCommentBody(t *testing.T, fields map[string]interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	return raw
}

func assertNoServiceWrite(t *testing.T, svc *mocks.TicketService) {
	t.Helper()
	svc.AssertNotCalled(t, "CreateComment", mock.Anything)
	svc.AssertNotCalled(t, "CreateCommentWithCustomerEmail", mock.Anything, mock.Anything)
}

func TestTicketCommentEmailHandlerFlag(t *testing.T) {
	assigned := uint(2)
	ticket := &models.Ticket{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1", AssignedToID: &assigned}

	t.Run("email_customer with is_internal is 400 and writes nothing", func(t *testing.T) {
		for _, caller := range []struct {
			id   uint
			role models.UserRole
		}{{1, models.RoleAdmin}, {2, models.RoleSupport}} {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			w, envelope := serveTicketComment(t, svc, caller.id, caller.role, "1",
				ticketCommentBody(t, map[string]interface{}{"body": "Reply", "is_internal": true, "email_customer": true}))
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			apiErr := envelope["error"].(map[string]interface{})
			assert.Equal(t, utils.ErrCodeValidation, apiErr["code"])
			details, ok := apiErr["details"].(map[string]interface{})
			require.True(t, ok, "details is an object")
			assert.Contains(t, details, "email_customer")
			assertNoServiceWrite(t, svc)
			svc.AssertCalled(t, "GetByID", uint(1))
		}
	})

	t.Run("the blank-body check runs first", func(t *testing.T) {
		svc := &mocks.TicketService{}
		svc.On("GetByID", uint(1)).Return(ticket, nil)
		w, envelope := serveTicketComment(t, svc, 1, models.RoleAdmin, "1",
			ticketCommentBody(t, map[string]interface{}{"body": "  ", "is_internal": true, "email_customer": true}))
		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, utils.ErrCodeBadRequest, envelope["error"].(map[string]interface{})["code"])
		assertNoServiceWrite(t, svc)
	})

	t.Run("an invalid id is refused before the flag", func(t *testing.T) {
		svc := &mocks.TicketService{}
		w, _ := serveTicketComment(t, svc, 1, models.RoleAdmin, "abc",
			ticketCommentBody(t, map[string]interface{}{"body": "Reply", "is_internal": true, "email_customer": true}))
		assert.Equal(t, http.StatusBadRequest, w.Code)
		svc.AssertNotCalled(t, "GetByID", mock.Anything)
		assertNoServiceWrite(t, svc)
	})

	t.Run("an unknown ticket is 404 before the flag", func(t *testing.T) {
		svc := &mocks.TicketService{}
		svc.On("GetByID", uint(9)).Return(nil, fmt.Errorf("ticket 9 not found: %w", apperrors.ErrNotFound))
		w, _ := serveTicketComment(t, svc, 1, models.RoleAdmin, "9",
			ticketCommentBody(t, map[string]interface{}{"body": "Reply", "is_internal": true, "email_customer": true}))
		assert.Equal(t, http.StatusNotFound, w.Code)
		assertNoServiceWrite(t, svc)
	})

	t.Run("customer, sales and unassigned support keep their 403 without reading the body", func(t *testing.T) {
		// Not valid JSON: a handler that read the body would answer 400.
		garbage := []byte(`{"email_customer": true, "is_internal": true`)
		for _, caller := range []struct {
			id   uint
			role models.UserRole
		}{{10, models.RoleCustomer}, {4, models.RoleSales}, {7, models.RoleSupport}} {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			w, _ := serveTicketComment(t, svc, caller.id, caller.role, "1", garbage)
			assert.Equal(t, http.StatusForbidden, w.Code, "role %s", caller.role)
			assertNoServiceWrite(t, svc)
		}
	})

	t.Run("flag absent or false calls CreateComment with no delivery key", func(t *testing.T) {
		for _, fields := range []map[string]interface{}{
			{"body": "Reply"},
			{"body": "Reply", "email_customer": false},
			{"body": "Note", "is_internal": true, "email_customer": false},
		} {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			svc.On("CreateComment", mock.AnythingOfType("*models.TicketComment")).Return(nil).Run(func(args mock.Arguments) {
				args.Get(0).(*models.TicketComment).ID = 100
			})
			w, envelope := serveTicketComment(t, svc, 1, models.RoleAdmin, "1", ticketCommentBody(t, fields))
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			data := envelope["data"].(map[string]interface{})
			_, present := data["delivery"]
			assert.False(t, present, "no delivery key without the flag")
			svc.AssertNotCalled(t, "CreateCommentWithCustomerEmail", mock.Anything, mock.Anything)
			svc.AssertNumberOfCalls(t, "CreateComment", 1)
		}
	})

	t.Run("flag true calls CreateCommentWithCustomerEmail with the request context", func(t *testing.T) {
		for _, caller := range []struct {
			id   uint
			role models.UserRole
		}{{1, models.RoleAdmin}, {2, models.RoleSupport}} {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			sentAt := time.Date(2026, 11, 13, 9, 30, 1, 0, time.UTC)
			var seenMarker interface{}
			svc.On("CreateCommentWithCustomerEmail", mock.Anything, mock.MatchedBy(func(cm *models.TicketComment) bool {
				return cm.TicketID == 1 && cm.AuthorUserID == caller.id && cm.Body == "Reply" && !cm.IsInternal
			})).Return(nil).Run(func(args mock.Arguments) {
				seenMarker = args.Get(0).(context.Context).Value(ticketMailCtxKey{})
				cm := args.Get(1).(*models.TicketComment)
				cm.ID = 101
				cm.Delivery = &models.TicketMailDelivery{ID: 5, TicketID: 1, CommentID: 101, CustomerID: 3,
					Status: models.TicketMailStatusSent, SentAt: &sentAt}
			})
			w, envelope := serveTicketComment(t, svc, caller.id, caller.role, "1",
				ticketCommentBody(t, map[string]interface{}{"body": "Reply", "email_customer": true}))
			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			assert.Equal(t, "request-marker", seenMarker, "the service gets c.Request.Context()")
			data := envelope["data"].(map[string]interface{})
			delivery, ok := data["delivery"].(map[string]interface{})
			require.True(t, ok, "the 201 carries the delivery")
			assert.Equal(t, models.TicketMailStatusSent, delivery["status"])
			assert.EqualValues(t, 101, delivery["comment_id"])
			svc.AssertNotCalled(t, "CreateComment", mock.Anything)
		}
	})

	t.Run("a failed or skipped mail still answers 201", func(t *testing.T) {
		for _, status := range []string{models.TicketMailStatusFailed, models.TicketMailStatusSkipped, models.TicketMailStatusLogOnly} {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			svc.On("CreateCommentWithCustomerEmail", mock.Anything, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
				args.Get(1).(*models.TicketComment).Delivery = &models.TicketMailDelivery{Status: status}
			})
			w, envelope := serveTicketComment(t, svc, 1, models.RoleAdmin, "1",
				ticketCommentBody(t, map[string]interface{}{"body": "Reply", "email_customer": true}))
			require.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, status, envelope["data"].(map[string]interface{})["delivery"].(map[string]interface{})["status"])
		}
	})
}

func TestTicketCommentEmailHandlerErrorMapping(t *testing.T) {
	ticket := &models.Ticket{BaseModel: models.BaseModel{ID: 1}, Title: "Ticket 1"}
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"ticket not found", fmt.Errorf("ticket 1 not found: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"author not found", fmt.Errorf("author not found: %w", apperrors.ErrNotFound), http.StatusNotFound, utils.ErrCodeNotFound},
		{"unwired", service.ErrTicketCommentMailUnavailable, http.StatusInternalServerError, utils.ErrCodeInternal},
		{"not allowed", service.ErrTicketCommentEmailNotAllowed, http.StatusInternalServerError, utils.ErrCodeInternal},
		{"nested", utils.ErrTransactionRootRequired, http.StatusInternalServerError, utils.ErrCodeInternal},
		{"other", errors.New("database is down"), http.StatusInternalServerError, utils.ErrCodeInternal},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := &mocks.TicketService{}
			svc.On("GetByID", uint(1)).Return(ticket, nil)
			svc.On("CreateCommentWithCustomerEmail", mock.Anything, mock.Anything).Return(tc.err)
			w, envelope := serveTicketComment(t, svc, 1, models.RoleAdmin, "1",
				ticketCommentBody(t, map[string]interface{}{"body": "Reply", "email_customer": true}))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Equal(t, tc.code, envelope["error"].(map[string]interface{})["code"])
			svc.AssertNotCalled(t, "CreateComment", mock.Anything)
			if tc.status == http.StatusInternalServerError {
				assert.NotContains(t, w.Body.String(), tc.err.Error(), "a 500 never echoes the error")
			}
		})
	}
}
