package main

// The password reset queue through the router production uses:
// setupDependencies builds the queue over the auth service and the sign-in
// audit writer, hands it to the auth handler, starts it before the routes are
// served and stops it through the combined drain handle. With cfg.SMTP
// pointed at a relay that holds back its greeting, POST /auth/password-reset
// still answers at once; once the relay is released the queued link is
// mailed. Over a SQLite file opened by the real connector.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// awaitResetRequestRow waits until the reset queue has written the
// reset_request audit row of userID and returns it. The worker writes the row
// after the reset was issued and its mail sent, so the row marks the request
// as handled.
func awaitResetRequestRow(t *testing.T, userID uint) models.AuthAuditLog {
	t.Helper()
	var row models.AuthAuditLog
	require.Eventually(t, func() bool {
		return models.DB.Where("method = ? AND user_id = ?", service.AuthAuditMethodResetRequest, userID).
			Order("id DESC").Take(&row).Error == nil
	}, 5*time.Second, 10*time.Millisecond, "the queued reset request of user %d wrote its audit row", userID)
	return row
}

// heldRelay accepts SMTP connections on 127.0.0.1 and holds back the 220
// greeting until release is called; then it serves them as productNameRelay.
type heldRelay struct {
	*productNameRelay
	listener net.Listener
	released chan struct{}
	release  func()
	accepted chan struct{}
	done     chan struct{}
}

func startHeldRelay(t *testing.T) (*heldRelay, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	relay := &heldRelay{productNameRelay: &productNameRelay{}, listener: listener,
		released: make(chan struct{}), accepted: make(chan struct{}, 16), done: make(chan struct{})}
	var once sync.Once
	relay.release = func() { once.Do(func() { close(relay.released) }) }
	go func() {
		defer close(relay.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case relay.accepted <- struct{}{}:
			default:
			}
			<-relay.released
			relay.serve(conn)
		}
	}()
	t.Cleanup(func() {
		relay.release()
		_ = listener.Close()
		<-relay.done
	})
	return relay, listener.Addr().(*net.TCPAddr).Port
}

func TestPasswordResetQueueProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)
	relay, port := startHeldRelay(t)
	boot.cfg.SMTP.Host = "127.0.0.1"
	boot.cfg.SMTP.Port = port

	user := &models.User{Email: "queued@reset-queue-wiring.test", Password: "x", FirstName: "Queue", LastName: "User",
		Role: models.RoleSales, IsActive: true}
	require.NoError(t, models.DB.Create(user).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	var stopOnce sync.Once
	var stopErr error
	stopRouter := func() error {
		stopOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
			defer cancel()
			stopErr = stop(ctx)
		})
		return stopErr
	}
	t.Cleanup(func() {
		relay.release()
		_ = stopRouter()
	})

	request := httptest.NewRequest(http.MethodPost, boot.cfg.API.Prefix+"/auth/password-reset",
		strings.NewReader(`{"email":"`+user.Email+`"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		router.ServeHTTP(recorder, request)
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-answered:
	case <-timer.C:
		t.Fatal("POST /auth/password-reset did not answer within 2 s while the relay held the delivery")
	}
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var envelope struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	assert.Equal(t, "If an account exists for that email, a password reset link has been sent", envelope.Data.Message)

	// The worker has reached the relay and waits for its greeting.
	select {
	case <-relay.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued reset never connected to the relay")
	}
	relay.release()

	require.Eventually(t, func() bool {
		for _, subject := range relay.subjects(t) {
			if strings.HasPrefix(subject, "Reset your ") && strings.HasSuffix(subject, " password") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the queued reset mail reached the relay")
	var unused int64
	require.NoError(t, models.DB.Model(&models.PasswordResetToken{}).Where("user_id = ? AND used_at IS NULL", user.ID).Count(&unused).Error)
	assert.EqualValues(t, 1, unused, "one unused reset token is stored")
	awaitResetRequestRow(t, user.ID)

	require.NoError(t, stopRouter(), "the drain handle stops the queue with every other runtime")
	require.NoError(t, relay.listener.Close())
	select {
	case <-relay.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay listener did not close")
	}
	_, err := net.DialTimeout("tcp", relay.listener.Addr().String(), time.Second)
	assert.Error(t, err, "nothing listens on the relay address any more")
}
