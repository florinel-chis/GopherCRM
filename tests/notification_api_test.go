package tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func contextBackground() context.Context { return context.Background() }

// AC-13: admin, sales and support users see only their own notifications,
// newest first and paginated; the unread count is theirs; marking one read
// is 404 for anyone else's; read-all marks only their own; customers get 403.
func TestNotificationAPISQLite(t *testing.T) {
	h := newAutomationAPIHarness(t)
	sales, support, admin := h.users["sales"], h.users["support"], h.users["admin"]
	seed := func(user *models.User, n int) []models.Notification {
		out := make([]models.Notification, 0, n)
		for i := 0; i < n; i++ {
			notification := models.Notification{UserID: user.ID, Title: fmt.Sprintf("Note %d for %s", i, user.Role), EntityType: "lead", EntityID: uint(i + 1)}
			require.NoError(t, h.db.Create(&notification).Error)
			out = append(out, notification)
		}
		return out
	}
	salesNotes := seed(sales, 25)
	supportNotes := seed(support, 2)
	seed(admin, 1)

	t.Run("each user lists only its own, newest first, paginated", func(t *testing.T) {
		rec := h.request(http.MethodGet, "/api/v1/notifications", "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var page []models.Notification
		h.data(rec, &page)
		require.Len(t, page, 20)
		assert.EqualValues(t, 25, h.decode(rec).Meta.Total)
		assert.Equal(t, salesNotes[24].ID, page[0].ID, "newest first")
		for _, notification := range page {
			assert.Equal(t, sales.ID, notification.UserID)
		}
		rec = h.request(http.MethodGet, "/api/v1/notifications?page=2&limit=20", "sales", nil)
		h.data(rec, &page)
		assert.Len(t, page, 5)

		rec = h.request(http.MethodGet, "/api/v1/notifications", "support", nil)
		h.data(rec, &page)
		assert.Len(t, page, 2)
		rec = h.request(http.MethodGet, "/api/v1/notifications", "admin", nil)
		h.data(rec, &page)
		assert.Len(t, page, 1)
		rec = h.request(http.MethodGet, "/api/v1/notifications?unread=sometimes", "admin", nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unread count, mark read and read-all stay within the caller's rows", func(t *testing.T) {
		rec := h.request(http.MethodGet, "/api/v1/notifications/unread-count", "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var count struct {
			Unread int64 `json:"unread"`
		}
		h.data(rec, &count)
		assert.EqualValues(t, 25, count.Unread)

		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/notifications/%d/read", salesNotes[0].ID), "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var marked models.Notification
		h.data(rec, &marked)
		assert.NotNil(t, marked.ReadAt)
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/notifications/%d/read", salesNotes[0].ID), "sales", nil)
		assert.Equal(t, http.StatusOK, rec.Code, "marking again is a no-op")
		rec = h.request(http.MethodPost, fmt.Sprintf("/api/v1/notifications/%d/read", salesNotes[1].ID), "support", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, "somebody else's notification is not found")
		rec = h.request(http.MethodPost, "/api/v1/notifications/999999/read", "sales", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)

		rec = h.request(http.MethodGet, "/api/v1/notifications/unread-count", "sales", nil)
		h.data(rec, &count)
		assert.EqualValues(t, 24, count.Unread)
		rec = h.request(http.MethodGet, "/api/v1/notifications?unread=true", "sales", nil)
		assert.EqualValues(t, 24, h.decode(rec).Meta.Total)

		rec = h.request(http.MethodPost, "/api/v1/notifications/read-all", "sales", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var all struct {
			Marked int64 `json:"marked"`
		}
		h.data(rec, &all)
		assert.EqualValues(t, 24, all.Marked)
		rec = h.request(http.MethodGet, "/api/v1/notifications/unread-count", "sales", nil)
		h.data(rec, &count)
		assert.Zero(t, count.Unread)
		rec = h.request(http.MethodGet, "/api/v1/notifications/unread-count", "support", nil)
		h.data(rec, &count)
		assert.EqualValues(t, len(supportNotes), count.Unread, "read-all touched nobody else's rows")
	})

	t.Run("customers get 403", func(t *testing.T) {
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/notifications"},
			{http.MethodGet, "/api/v1/notifications/unread-count"},
			{http.MethodPost, "/api/v1/notifications/1/read"},
			{http.MethodPost, "/api/v1/notifications/read-all"},
		} {
			rec := h.request(route.method, route.path, "customer", nil)
			assert.Equalf(t, http.StatusForbidden, rec.Code, "%s %s", route.method, route.path)
		}
	})
}
