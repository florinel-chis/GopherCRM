package service

// Shared fixtures of the outbound webhook roots (#345): the full production
// schema (openTicketIntakeDB), the real repositories and services, a
// Standard Webhooks verifier written independently of SignWebhook, an HTTPS
// consumer reached through an injected egress guard, an injectable clock and
// the failingWebhookRepository wrapper.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const webhookTestMasterSecret = "webhook-test-master-secret-0123456789"

var webhookTestSeq atomic.Uint64

// ------------------------------------------------------------ verifier ---

// verifyStandardWebhook checks a delivery the way a consumer following the
// Standard Webhooks specification does, without calling SignWebhook: decode
// the key after whsec_, HMAC-SHA256 "<id>.<timestamp>.<body>", compare every
// v1 signature in constant time, and refuse a timestamp outside tolerance.
func verifyStandardWebhook(secret string, header http.Header, body []byte, now time.Time, tolerance time.Duration) error {
	id := header.Get("webhook-id")
	timestamp := header.Get("webhook-timestamp")
	signatures := header.Get("webhook-signature")
	if id == "" || timestamp == "" || signatures == "" {
		return errors.New("missing webhook headers")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("bad timestamp")
	}
	sent := time.Unix(seconds, 0)
	if sent.Before(now.Add(-tolerance)) || sent.After(now.Add(tolerance)) {
		return errors.New("timestamp outside tolerance")
	}
	if !strings.HasPrefix(secret, "whsec_") {
		return errors.New("bad secret")
	}
	key, err := base64.StdEncoding.DecodeString(secret[len("whsec_"):])
	if err != nil {
		return errors.New("bad secret")
	}
	content := id + "." + timestamp + "." + string(body)
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, content)
	expected := mac.Sum(nil)
	for _, candidate := range strings.Fields(signatures) {
		version, value, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			continue
		}
		if hmac.Equal(raw, expected) {
			return nil
		}
	}
	return errors.New("no matching signature")
}

// --------------------------------------------------------------- clock ---

type webhookClock struct {
	mu  sync.Mutex
	now time.Time
}

func newWebhookClock(start time.Time) *webhookClock {
	return &webhookClock{now: start.UTC()}
}

func (c *webhookClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *webhookClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

func (c *webhookClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ------------------------------------------------------------ consumer ---

// consumerRequest is one request the consumer received.
type consumerRequest struct {
	Path   string
	Header http.Header
	Body   []byte
}

// webhookConsumer is an HTTPS server reached as https://example.com:<port>
// (the host its certificate covers) through a guard whose resolver answers
// per host and whose dialer counts every dial and connects to the server.
type webhookConsumer struct {
	server *httptest.Server
	port   string
	roots  *x509.CertPool

	mu       sync.Mutex
	handler  http.HandlerFunc
	requests []consumerRequest
	dials    atomic.Int32
}

func newWebhookConsumer(t *testing.T) *webhookConsumer {
	t.Helper()
	c := &webhookConsumer{}
	c.server = httptest.NewUnstartedServer(http.HandlerFunc(c.serve))
	c.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	c.server.StartTLS()
	t.Cleanup(c.server.Close)
	_, port, err := net.SplitHostPort(c.server.Listener.Addr().String())
	require.NoError(t, err)
	c.port = port
	c.roots = x509.NewCertPool()
	c.roots.AddCert(c.server.Certificate())
	return c
}

func (c *webhookConsumer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.requests = append(c.requests, consumerRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	handler := c.handler
	c.mu.Unlock()
	if handler == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
		return
	}
	handler(w, r)
}

func (c *webhookConsumer) setHandler(handler http.HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = handler
}

func (c *webhookConsumer) received() []consumerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]consumerRequest(nil), c.requests...)
}

// url is the endpoint URL of path on the consumer.
func (c *webhookConsumer) url(path string) string {
	return "https://example.com:" + c.port + path
}

// webhookResolver answers per host; an unknown host is "not found".
type webhookResolver struct {
	answers map[string][]netip.Addr
}

func (r webhookResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if addrs, ok := r.answers[host]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// guard builds the egress guard every runtime test uses: example.com resolves
// to a public address (dialled through to the consumer), and extra maps more
// hosts. AEO policy plays no part in PublicHTTPClient.
func (c *webhookConsumer) guard(extra map[string][]netip.Addr) *aeo.EgressGuard {
	answers := map[string][]netip.Addr{"example.com": {netip.MustParseAddr("93.184.216.34")}}
	for host, addrs := range extra {
		answers[host] = addrs
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		c.dials.Add(1)
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", c.server.Listener.Addr().String())
	}
	return aeo.NewEgressGuard(config.AEOEgressPolicy{}, aeo.WithEgressResolver(webhookResolver{answers: answers}),
		aeo.WithEgressDialer(dial), aeo.WithEgressRootCAs(c.roots))
}

// --------------------------------------------------- failing repository ---

// failingWebhookRepository wraps the real repository: EnqueueDeliveries
// writes the rows and then returns the injected error, so the creating
// transaction must roll back the record and the rows together. failures
// bounds how many calls fail (0 = every call).
type failingWebhookRepository struct {
	repository.WebhookRepository
	err      error
	failures int32
	calls    *atomic.Int32
}

func newFailingWebhookRepository(inner repository.WebhookRepository, err error, failures int32) *failingWebhookRepository {
	return &failingWebhookRepository{WebhookRepository: inner, err: err, failures: failures, calls: &atomic.Int32{}}
}

func (f *failingWebhookRepository) WithTx(tx *gorm.DB) repository.WebhookRepository {
	return &failingWebhookRepository{WebhookRepository: f.WebhookRepository.WithTx(tx), err: f.err, failures: f.failures, calls: f.calls}
}

func (f *failingWebhookRepository) EnqueueDeliveries(ctx context.Context, deliveries []models.WebhookDelivery) error {
	if err := f.WebhookRepository.EnqueueDeliveries(ctx, deliveries); err != nil {
		return err
	}
	n := f.calls.Add(1)
	if f.failures == 0 || n <= f.failures {
		return f.err
	}
	return nil
}

// ---------------------------------------------------------- environment ---

type webhookEnv struct {
	t         *testing.T
	db        *gorm.DB
	repo      repository.WebhookRepository
	box       *utils.SecretBox
	txManager *utils.TransactionManager
	owner     *models.User
	mailer    *fakeFormMailer

	endpoints WebhookService
	leads     LeadService
	customers CustomerService
	tickets   TicketService
	forms     FormService
	bulk      BulkStatusService
	// users erases users ungoverned (Delete); governedUsers through the role
	// governance (DeleteAs).
	users         UserService
	governedUsers UserService

	// erasureListener, when set, replaces the webhook erasure listener the
	// next wire installs on the lead, customer and user repositories (#437).
	erasureListener repository.ErasureListener
}

// newWebhookEnv wires every create path with hooks (nil: unwired).
func newWebhookEnv(t *testing.T, db *gorm.DB, hooks repository.WebhookRepository) *webhookEnv {
	t.Helper()
	if utils.Logger == nil {
		require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	}
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", formTestSecret)
	env := &webhookEnv{t: t, db: db, repo: repository.NewWebhookRepository(db),
		box: utils.NewSecretBox(webhookTestMasterSecret, WebhookSecretContext), txManager: utils.NewTransactionManager(db),
		mailer: &fakeFormMailer{}}
	env.owner = &models.User{Email: fmt.Sprintf("owner-%d@webhooks.test", webhookTestSeq.Add(1)), Password: "x",
		FirstName: "Own", LastName: "Er", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(env.owner).Error)
	env.endpoints = NewWebhookService(env.repo, env.txManager, env.box)
	env.wire(hooks)
	return env
}

// wire (re)builds the create services with hooks.
func (e *webhookEnv) wire(hooks repository.WebhookRepository) {
	db := e.db
	automation := repository.NewAutomationRepository(db)
	// The erasure units of the three repositories write the deleted events
	// through the listener (#437), as cmd/main.go wires them.
	listener := e.erasureListener
	if listener == nil {
		listener = NewWebhookErasureListener(hooks)
	}
	leadRepo := repository.NewLeadRepository(db, repository.WithLeadErasureListener(listener))
	customerRepo := repository.NewCustomerRepository(db, repository.WithCustomerErasureListener(listener))
	userRepo := repository.NewUserRepository(db, repository.WithUserErasureListener(listener))
	companyRepo := repository.NewCompanyRepository(db)

	leadOpts := []LeadServiceOption{WithLeadAutomationEvents(automation)}
	customerOpts := []CustomerServiceOption{}
	ticketOpts := []TicketServiceOption{WithTicketAutomationEvents(automation, e.txManager)}
	formOpts := []FormServiceOption{WithFormAutomationEvents(automation),
		WithFormTicketIntake(repository.NewFormTicketRepository(db), e.txManager)}
	if hooks != nil {
		leadOpts = append(leadOpts, WithLeadWebhooks(hooks))
		customerOpts = append(customerOpts, WithCustomerWebhooks(hooks, e.txManager))
		ticketOpts = append(ticketOpts, WithTicketWebhooks(hooks, e.txManager))
		formOpts = append(formOpts, WithFormWebhooks(hooks))
	}
	e.leads = NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, e.txManager, leadOpts...)
	e.customers = NewCustomerService(customerRepo, userRepo, companyRepo, customerOpts...)
	e.tickets = NewTicketService(repository.NewTicketRepository(db), customerRepo, userRepo, ticketOpts...)
	e.users = NewUserService(userRepo)
	e.governedUsers = NewUserService(userRepo, WithUserRoleGovernance(repository.NewRoleRepository(db),
		repository.NewRoleAuditRepository(db), e.txManager))
	bulkOpts := []BulkStatusServiceOption{WithBulkAutomationEvents(automation)}
	if hooks != nil {
		bulkOpts = append(bulkOpts, WithBulkWebhooks(hooks))
	}
	e.bulk = NewBulkStatusService(repository.NewBulkOperationRepository(db), repository.NewBulkStatusRepository(db),
		e.txManager, utils.Logger, bulkOpts...)
	e.forms = NewFormService(repository.NewFormRepository(db), leadRepo, userRepo, e.mailer, e.txManager,
		config.FormsConfig{PublicBaseURL: formTestPublicBase}, formTestAPIPrefix, formOpts...)
}

// endpoint registers an endpoint through the service and returns it with its
// secret.
func (e *webhookEnv) endpoint(url string, active bool, types ...string) *models.WebhookEndpointCreated {
	e.t.Helper()
	created, err := e.endpoints.CreateEndpoint(context.Background(), WebhookEndpointInput{URL: url, EventTypes: types, IsActive: &active})
	require.NoError(e.t, err)
	return created
}

func (e *webhookEnv) runtime(clients WebhookClientFactory, opts ...WebhookRuntimeOption) *WebhookRuntime {
	return NewWebhookRuntime(e.repo, clients, e.box, opts...)
}

func (e *webhookEnv) uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@webhooks.test", prefix, webhookTestSeq.Add(1))
}

func (e *webhookEnv) newLead() *models.Lead {
	return &models.Lead{FirstName: "Webhook", LastName: "Lead", Email: e.uniqueEmail("lead"), OwnerID: e.owner.ID, Status: models.LeadStatusNew}
}

func (e *webhookEnv) newCustomer() *models.Customer {
	return &models.Customer{FirstName: "Webhook", LastName: "Customer", Email: e.uniqueEmail("customer")}
}

// seedCustomer writes a customer directly (no webhook).
func (e *webhookEnv) seedCustomer() *models.Customer {
	e.t.Helper()
	customer := e.newCustomer()
	require.NoError(e.t, e.db.Create(customer).Error)
	return customer
}

// seedLead writes a lead directly (no webhook).
func (e *webhookEnv) seedLead() *models.Lead {
	e.t.Helper()
	lead := e.newLead()
	require.NoError(e.t, e.db.Create(lead).Error)
	return lead
}

func (e *webhookEnv) newTicket(customerID uint) *models.Ticket {
	return &models.Ticket{Title: "Webhook ticket", Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: customerID}
}

// deliveries returns the rows of one event type and record, in id order.
func (e *webhookEnv) deliveries(eventType string, recordID uint) []models.WebhookDelivery {
	e.t.Helper()
	var rows []models.WebhookDelivery
	require.NoError(e.t, e.db.Where("event_type = ? AND record_id = ?", eventType, recordID).Order("id").Find(&rows).Error)
	return rows
}

func (e *webhookEnv) allDeliveries() int64 {
	e.t.Helper()
	var n int64
	require.NoError(e.t, e.db.Model(&models.WebhookDelivery{}).Count(&n).Error)
	return n
}

func (e *webhookEnv) count(model interface{}, where string, args ...interface{}) int64 {
	e.t.Helper()
	var n int64
	query := e.db.Unscoped().Model(model)
	if where != "" {
		query = query.Where(where, args...)
	}
	require.NoError(e.t, query.Count(&n).Error)
	return n
}

func (e *webhookEnv) delivery(id uint) models.WebhookDelivery {
	e.t.Helper()
	var row models.WebhookDelivery
	require.NoError(e.t, e.db.Where("id = ?", id).Take(&row).Error)
	return row
}

// leadForm publishes a create_lead form without double opt-in.
func (e *webhookEnv) leadForm() *models.Form {
	e.t.Helper()
	form := &models.Form{Name: "Webhook contact", Status: models.FormStatusPublished, Fields: contactFormFields(),
		CreateLead: true, DefaultOwnerID: e.owner.ID}
	require.NoError(e.t, e.forms.Create(form, e.owner.ID))
	return form
}

// ticketForm publishes a create_ticket form (double opt-in).
func (e *webhookEnv) ticketForm() *models.Form {
	e.t.Helper()
	form := &models.Form{Name: "Webhook complaints", Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			{Name: "complaint", Label: "Complaint", Type: models.FormFieldTextarea, Required: true},
		},
		DoubleOptIn: true, CreateTicket: true}
	require.NoError(e.t, e.forms.Create(form, e.owner.ID))
	return form
}

// submitLead submits the contact form for email.
func (e *webhookEnv) submitLead(form *models.Form, email string) error {
	req := validSubmission()
	req.Values["email"] = email
	_, err := e.forms.SubmitPublic(context.Background(), form.PublicID, req, submissionMeta())
	return err
}

// pendingTicket submits the ticket form and returns the raw confirmation
// token mailed for it.
func (e *webhookEnv) pendingTicket(form *models.Form, email, key string) string {
	e.t.Helper()
	before := len(e.mailer.to(email))
	_, err := e.forms.SubmitPublic(context.Background(), form.PublicID, ticketRequest(email, keyPtr(key), "Webhook complaint"), submissionMeta())
	require.NoError(e.t, err)
	messages := e.mailer.to(email)
	require.Len(e.t, messages, before+1)
	return tokenFromLink(e.t, messages[len(messages)-1].Body)
}

// newestTicketOf returns the newest ticket of a customer, or nil.
func (e *webhookEnv) newestTicketOf(customerID uint) *models.Ticket {
	e.t.Helper()
	var tickets []models.Ticket
	require.NoError(e.t, e.db.Where("customer_id = ?", customerID).Order("id desc").Limit(1).Find(&tickets).Error)
	if len(tickets) == 0 {
		return nil
	}
	return &tickets[0]
}

// seedTicket writes a ticket of a new customer directly (no webhook).
func (e *webhookEnv) seedTicket(status models.TicketStatus) *models.Ticket {
	e.t.Helper()
	ticket := e.newTicket(e.seedCustomer().ID)
	ticket.Status = status
	require.NoError(e.t, e.db.Create(ticket).Error)
	return ticket
}

// updatedDeliveries counts the rows of every *.updated event type.
func (e *webhookEnv) updatedDeliveries() int64 {
	e.t.Helper()
	return e.count(&models.WebhookDelivery{}, "event_type LIKE ?", "%.updated")
}

// clearDeliveries removes every delivery row.
func (e *webhookEnv) clearDeliveries() {
	e.t.Helper()
	require.NoError(e.t, e.db.Where("1 = 1").Delete(&models.WebhookDelivery{}).Error)
}

// injectedFailure is the error the failing wrapper returns.
var injectedFailure = errors.New("injected webhook outbox failure")
