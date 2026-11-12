package main

// Issue #292 through the router production uses: setupDependencies builds
// configService before authService and wires WithAuthProductName(configService),
// WithTicketProductName(configService) and the quote service's configService
// argument. With cfg.SMTP pointed at an in-process relay on 127.0.0.1 and
// general.company_name updated after boot, the password reset, quote and
// ticket reply mails carry the stored name in their subject. Over a SQLite
// file opened by the real connector.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
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
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// productNameRelay is a minimal SMTP relay on 127.0.0.1 that accepts every
// message and keeps its DATA payload.
type productNameRelay struct {
	mu       sync.Mutex
	messages []string
}

func startProductNameRelay(t *testing.T) (*productNameRelay, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	relay := &productNameRelay{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			relay.serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return relay, listener.Addr().(*net.TCPAddr).Port
}

func (r *productNameRelay) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	write := func(lines ...string) bool {
		_, err := conn.Write([]byte(strings.Join(lines, "\r\n") + "\r\n"))
		return err == nil
	}
	if !write("220 relay.test ESMTP") {
		return
	}
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.TrimRight(raw, "\r\n"))
		if i := strings.IndexAny(verb, " :"); i >= 0 {
			verb = verb[:i]
		}
		ok := true
		switch verb {
		case "EHLO":
			ok = write("250-relay.test", "250 SIZE 1048576")
		case "HELO", "MAIL", "RCPT", "RSET", "NOOP":
			ok = write("250 2.0.0 Ok")
		case "DATA":
			if !write("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var data strings.Builder
			for {
				chunk, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if chunk == ".\r\n" {
					break
				}
				data.WriteString(chunk)
			}
			r.mu.Lock()
			r.messages = append(r.messages, data.String())
			r.mu.Unlock()
			ok = write("250 2.0.0 Ok: queued")
		case "QUIT":
			_ = write("221 2.0.0 Bye")
			return
		default:
			ok = write("502 5.5.2 Command not implemented")
		}
		if !ok {
			return
		}
	}
}

// subjects returns the decoded Subject header of every message received.
func (r *productNameRelay) subjects(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	messages := append([]string(nil), r.messages...)
	r.mu.Unlock()
	var out []string
	for _, message := range messages {
		headers, _, _ := strings.Cut(message, "\r\n\r\n")
		var raw string
		inSubject := false
		for _, line := range strings.Split(headers, "\r\n") {
			switch {
			case strings.HasPrefix(line, "Subject: "):
				raw, inSubject = strings.TrimPrefix(line, "Subject: "), true
			case inSubject && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
				raw += line
			default:
				inSubject = false
			}
		}
		decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
		require.NoError(t, err)
		out = append(out, decoded)
	}
	return out
}

func TestMailProductNameProductionWiring(t *testing.T) {
	boot := bootAutomation(t, false)
	relay, port := startProductNameRelay(t)
	boot.cfg.SMTP.Host = "127.0.0.1"
	boot.cfg.SMTP.Port = port

	admin := &models.User{Email: "admin@product-name-wiring.test", Password: "x", FirstName: "Ada", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	customer := &models.Customer{FirstName: "Cora", LastName: "Customer", Email: "cora@product-name-wiring.test"}
	require.NoError(t, models.DB.Create(customer).Error)
	ticket := &models.Ticket{Title: "Wired ticket", Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &admin.ID}
	require.NoError(t, models.DB.Create(ticket).Error)
	sentAt := time.Now().UTC()
	quote := &models.Quote{Title: "Wired quote", Status: models.QuoteStatusSent, Currency: "EUR",
		CustomerID: &customer.ID, OwnerID: admin.ID, SentAt: &sentAt}
	require.NoError(t, models.DB.Create(quote).Error)

	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	t.Cleanup(stopBackground)
	router, stop := setupRouter(backgroundCtx, boot.cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), automationBootTimeout)
		defer cancel()
		_ = stop(ctx)
	})

	// The name is changed after boot, as an administrator would: nothing is
	// cached at startup, every send reads it.
	configService := service.NewConfigurationService(repository.NewConfigurationRepository(models.DB), nil)
	require.NoError(t, configService.Set(service.ConfigGeneralCompanyName, "Acme Ltd"))

	auth := service.NewAuthService(repository.NewUserRepository(models.DB), repository.NewAPIKeyRepository(models.DB), boot.cfg.JWT)
	token, err := auth.GenerateJWT(admin)
	require.NoError(t, err)

	call := func(method, path string, body interface{}, bearer bool) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			payload, err := json.Marshal(body)
			require.NoError(t, err)
			reader = bytes.NewReader(payload)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, boot.cfg.API.Prefix+path, reader)
		request.Header.Set("Content-Type", "application/json")
		if bearer {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	response := call(http.MethodPost, "/auth/password-reset", map[string]string{"email": admin.Email}, false)
	require.Equal(t, http.StatusOK, response.Code, "body: %s", response.Body.String())

	response = call(http.MethodPost, fmt.Sprintf("/quotes/%d/email", quote.ID), nil, true)
	require.Equal(t, http.StatusOK, response.Code, "body: %s", response.Body.String())

	response = call(http.MethodPost, fmt.Sprintf("/tickets/%d/comments", ticket.ID),
		map[string]interface{}{"body": "Wired reply", "email_customer": true}, true)
	require.Equal(t, http.StatusCreated, response.Code, "body: %s", response.Body.String())

	subjects := relay.subjects(t)
	assert.ElementsMatch(t, []string{
		"Reset your Acme Ltd password",
		fmt.Sprintf("Quote %s from Acme Ltd", models.QuoteNumber(quote.ID)),
		fmt.Sprintf("Ticket %s from Acme Ltd", models.TicketNumber(ticket.ID)),
	}, subjects, "each mail went through the relay once with the stored name")

	var stored models.TicketMailDelivery
	require.NoError(t, models.DB.Where("ticket_id = ?", ticket.ID).Take(&stored).Error)
	assert.Equal(t, models.TicketMailStatusSent, stored.Status, "the SMTP mailer is wired, so the ticket mail is sent")
}
