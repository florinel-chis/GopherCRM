package handler

// The status history of a quote the client answered through the response
// link (#400): the real quote service, the real routes and the real Auth
// middleware over SQLite. The row of the answer has no acting user and source
// client; the staff rows of the same quote read as before.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/middleware"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

const quoteHistoryClientBaseURL = "https://crm.example.test"

// quoteHistoryMailer keeps the bodies of the quote emails, which carry the
// response link.
type quoteHistoryMailer struct {
	mu     sync.Mutex
	bodies []string
}

func (m *quoteHistoryMailer) SendPasswordReset(string, string, string) error { return nil }

func (m *quoteHistoryMailer) Send(_, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, body)
	return nil
}

// lastToken returns the token of the response link in the newest email.
func (m *quoteHistoryMailer) lastToken(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.bodies, "a quote email was sent")
	body := m.bodies[len(m.bodies)-1]
	const marker = "/quote-response?token="
	at := strings.Index(body, marker)
	require.GreaterOrEqual(t, at, 0, "the email carries the response link")
	token := body[at+len(marker):]
	if end := strings.IndexAny(token, " \r\n"); end >= 0 {
		token = token[:end]
	}
	require.NotEmpty(t, token)
	return token
}

type quoteHistoryChain struct {
	router   *gin.Engine
	mailer   *quoteHistoryMailer
	admin    *models.User
	token    string
	customer *models.Customer
	product  *models.Product
}

func newQuoteHistoryChain(t *testing.T) *quoteHistoryChain {
	t.Helper()
	ensureHandlerTestLogger(t)
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard, DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&models.User{}, &models.Company{}, &models.Lead{}, &models.Customer{}, &models.Deal{},
		&models.Product{}, &models.Quote{}, &models.QuoteLine{}, &models.QuoteStatusChange{},
		&models.QuoteResponseToken{}, &models.APIKey{},
	))

	chain := &quoteHistoryChain{mailer: &quoteHistoryMailer{}}
	chain.admin = &models.User{Email: "admin@quote-history.test", Password: "x", FirstName: "Ada", LastName: "Min", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(chain.admin).Error)
	chain.customer = &models.Customer{FirstName: "Cus", LastName: "Tomer", Email: "customer@quote-history.test"}
	require.NoError(t, db.Create(chain.customer).Error)
	chain.product = &models.Product{Name: "Widget", UnitPriceCents: 1000, Currency: "EUR", IsActive: true}
	require.NoError(t, db.Create(chain.product).Error)

	users := repository.NewUserRepository(db)
	svc := service.NewQuoteServiceWithResponseLinks(
		repository.NewQuoteRepository(db), repository.NewProductRepository(db), repository.NewDealRepository(db),
		repository.NewCustomerRepository(db), users, nil, chain.mailer, utils.NewTransactionManager(db),
		repository.NewQuoteResponseTokenRepository(db), quoteHistoryClientBaseURL, "quote-history-chain-secret-0123456789abcdef")

	auth := service.NewAuthService(users, repository.NewAPIKeyRepository(db),
		config.JWTConfig{Secret: "quote-history-chain-jwt-secret-0123456789abcdef", ExpiryHours: 1})
	chain.token, err = auth.GenerateJWT(chain.admin)
	require.NoError(t, err)

	chain.router = gin.New()
	chain.router.Use(middleware.RequestID(), middleware.ErrorHandler())
	api := chain.router.Group("/api/v1")
	SetupQuotePublicRoutes(api.Group(""), NewQuotePublicHandler(svc))
	SetupQuoteRoutes(api.Group("", middleware.Auth(auth)), NewQuoteHandler(svc))
	return chain
}

// do sends one request; token "" sends it unauthenticated.
func (c *quoteHistoryChain) do(t *testing.T, method, path, token, body string, want int) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	require.Equal(t, want, w.Code, "%s %s: %s", method, path, w.Body.String())
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
	return envelope
}

func TestQuoteHistoryClientAnswer(t *testing.T) {
	for decision, wantStatus := range map[string]string{
		models.QuoteResponseAccept:  string(models.QuoteStatusAccepted),
		models.QuoteResponseDecline: string(models.QuoteStatusRejected),
	} {
		t.Run(decision, func(t *testing.T) {
			chain := newQuoteHistoryChain(t)

			// Staff: create, one line, draft to sent, email.
			created := chain.do(t, http.MethodPost, "/api/v1/quotes", chain.token,
				fmt.Sprintf(`{"title":"History","customer_id":%d,"currency":"EUR"}`, chain.customer.ID), http.StatusCreated)
			var quote struct {
				ID uint `json:"id"`
			}
			require.NoError(t, json.Unmarshal(created["data"], &quote))
			require.NotZero(t, quote.ID)
			base := fmt.Sprintf("/api/v1/quotes/%d", quote.ID)
			chain.do(t, http.MethodPost, base+"/lines", chain.token, fmt.Sprintf(`{"product_id":%d,"quantity":1}`, chain.product.ID), http.StatusCreated)
			chain.do(t, http.MethodPost, base+"/status", chain.token, `{"status":"sent"}`, http.StatusOK)
			chain.do(t, http.MethodPost, base+"/email", chain.token, "", http.StatusOK)

			// The client answers through the emailed link, unauthenticated.
			answered := chain.do(t, http.MethodPost, "/api/v1/quotes/public/response", "",
				fmt.Sprintf(`{"token":%q,"decision":%q}`, chain.mailer.lastToken(t), decision), http.StatusOK)
			assert.Contains(t, string(answered["data"]), fmt.Sprintf(`"status":%q`, wantStatus))

			// The history, read on the raw JSON: an absent key and a null
			// differ here.
			history := chain.do(t, http.MethodGet, base+"/history", chain.token, "", http.StatusOK)
			var rows []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(history["data"], &rows))
			require.Len(t, rows, 3, "create, draft to sent, the client's answer")

			for i, want := range []struct{ from, to string }{{"null", `"draft"`}, {`"draft"`, `"sent"`}} {
				row := rows[i]
				assert.Equal(t, want.from, string(row["from_status"]), "row %d", i)
				assert.Equal(t, want.to, string(row["to_status"]), "row %d", i)
				assert.Equal(t, fmt.Sprint(chain.admin.ID), string(row["changed_by_id"]), "staff row %d names the acting user", i)
				assert.Equal(t, `"staff"`, string(row["source"]), "staff row %d", i)
				require.Contains(t, row, "changed_by", "staff row %d carries the user summary", i)
				var summary map[string]interface{}
				require.NoError(t, json.Unmarshal(row["changed_by"], &summary))
				assert.Equal(t, float64(chain.admin.ID), summary["id"])
				assert.Equal(t, "Ada", summary["first_name"])
				assert.NotContains(t, summary, "email")
			}

			client := rows[2]
			assert.Equal(t, `"sent"`, string(client["from_status"]))
			assert.Equal(t, fmt.Sprintf("%q", wantStatus), string(client["to_status"]))
			require.Contains(t, client, "changed_by_id", "the key is present")
			assert.Equal(t, "null", string(client["changed_by_id"]), "a client answer has no acting user")
			assert.Equal(t, `"client"`, string(client["source"]))
			assert.NotContains(t, client, "changed_by", "and no user summary")
		})
	}
}
