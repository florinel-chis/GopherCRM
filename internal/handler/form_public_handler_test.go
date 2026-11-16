package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// formPublicServiceStub stands in for the form service on the public routes.
// It is hand-written rather than a testify mock because these tests care about
// what the handler passed on and, above all, about what it did NOT call: the
// confirmation page must never spend a token.
//
// The CRM-side methods panic. Reaching one of them from a public route would
// be a wiring mistake worth failing loudly for.
type formPublicServiceStub struct {
	definition    *service.PublicFormDefinition
	definitionErr error
	outcome       *service.SubmitOutcome
	submitErr     error
	confirmErr    error

	definitionCalls int
	submitCalls     int
	confirmCalls    int

	lastPublicID string
	lastOrigin   string
	lastRequest  *service.PublicSubmissionRequest
	lastMeta     service.SubmissionMeta
	lastToken    string
	lastCtx      context.Context
}

var _ service.FormService = (*formPublicServiceStub)(nil)

func (s *formPublicServiceStub) Create(*models.Form, uint) error { panic("not a public route") }
func (s *formPublicServiceStub) GetByID(uint) (*models.Form, error) {
	panic("not a public route")
}

func (s *formPublicServiceStub) List(int, int, string, string, string) ([]models.Form, map[uint]int64, int64, error) {
	panic("not a public route")
}
func (s *formPublicServiceStub) Update(uint, *models.Form) error { panic("not a public route") }
func (s *formPublicServiceStub) Delete(uint) error               { panic("not a public route") }

func (s *formPublicServiceStub) ListSubmissions(uint, int, int, string) ([]models.FormSubmission, int64, error) {
	panic("not a public route")
}

func (s *formPublicServiceStub) GetSubmission(uint) (*models.FormSubmission, error) {
	panic("not a public route")
}

func (s *formPublicServiceStub) ListDeliveries(service.FormMailDeliveryFilter, int, int) ([]models.FormMailDelivery, int64, error) {
	panic("not a public route")
}

func (s *formPublicServiceStub) RetryDelivery(context.Context, uint) (*models.FormMailDelivery, error) {
	panic("not a public route")
}

func (s *formPublicServiceStub) PublicDefinition(publicID, origin string) (*service.PublicFormDefinition, error) {
	s.definitionCalls++
	s.lastPublicID = publicID
	s.lastOrigin = origin
	return s.definition, s.definitionErr
}

func (s *formPublicServiceStub) SubmitPublic(ctx context.Context, publicID string, req *service.PublicSubmissionRequest, meta service.SubmissionMeta) (*service.SubmitOutcome, error) {
	s.submitCalls++
	s.lastCtx = ctx
	s.lastPublicID = publicID
	s.lastRequest = req
	s.lastMeta = meta
	return s.outcome, s.submitErr
}

func (s *formPublicServiceStub) ConfirmSubmission(ctx context.Context, rawToken string) error {
	s.confirmCalls++
	s.lastCtx = ctx
	s.lastToken = rawToken
	return s.confirmErr
}

type FormPublicHandlerTestSuite struct {
	suite.Suite
	stub    *formPublicServiceStub
	handler *FormPublicHandler
	router  *gin.Engine
}

func (suite *FormPublicHandlerTestSuite) SetupSuite() {
	utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"})
	gin.SetMode(gin.TestMode)

	// Submitting and confirming sit on the strict tier (10/min, burst 5), and
	// this suite issues more requests than that from one address. The env var
	// is read when the middleware is built, so it has to be set before the
	// routes are registered in SetupTest.
	suite.Require().NoError(os.Setenv("DISABLE_RATE_LIMIT", "true"))
}

func (suite *FormPublicHandlerTestSuite) TearDownSuite() {
	suite.Require().NoError(os.Unsetenv("DISABLE_RATE_LIMIT"))
}

func (suite *FormPublicHandlerTestSuite) SetupTest() {
	suite.stub = &formPublicServiceStub{
		definition: &service.PublicFormDefinition{
			Name:     "Contact us",
			PublicID: "pub-key",
			Fields: []models.FormFieldDef{
				{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			},
			SubmitAction:  models.FormSubmitActionMessage,
			Challenge:     "MTIz.abcdef",
			HoneypotField: "website_url_confirm",
		},
		outcome: &service.SubmitOutcome{
			Action:  models.FormSubmitActionMessage,
			Message: "Thank you.",
		},
	}
	suite.handler = NewFormPublicHandler(suite.stub, config.FormsConfig{
		PublicBaseURL: "https://crm.example.com",
	}, "/api/v1")

	suite.router = gin.New()
	SetupFormPublicRoutes(suite.router.Group("/api/v1"), suite.handler)
}

func (suite *FormPublicHandlerTestSuite) do(method, path string, body interface{}) *httptest.ResponseRecorder {
	var reader *bytes.Buffer
	if body != nil {
		encoded, err := json.Marshal(body)
		suite.Require().NoError(err)
		reader = bytes.NewBuffer(encoded)
	} else {
		reader = bytes.NewBuffer(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *FormPublicHandlerTestSuite) decode(w *httptest.ResponseRecorder) utils.APIResponse {
	var response utils.APIResponse
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	return response
}

func TestFormPublicHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(FormPublicHandlerTestSuite))
}

func (suite *FormPublicHandlerTestSuite) TestEmbedScriptIsServedWithCachingHeaders() {
	w := suite.do(http.MethodGet, "/api/v1/forms/public/embed.js", nil)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal("application/javascript; charset=utf-8", w.Header().Get("Content-Type"))
	suite.Equal("public, max-age=3600", w.Header().Get("Cache-Control"))
	suite.NotEmpty(w.Body.Bytes())
	suite.Equal(formEmbedJS, w.Body.Bytes())
}

func (suite *FormPublicHandlerTestSuite) TestDefinitionReturnsEnvelope() {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/pub-key", nil)
	req.Header.Set("Origin", "https://customer.example")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(1, suite.stub.definitionCalls)
	suite.Equal("pub-key", suite.stub.lastPublicID)
	suite.Equal("https://customer.example", suite.stub.lastOrigin)

	response := suite.decode(w)
	suite.True(response.Success)
	data, ok := response.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal("Contact us", data["name"])
	suite.Equal("MTIz.abcdef", data["challenge"])
	suite.Equal("website_url_confirm", data["honeypot_field"])
}

func (suite *FormPublicHandlerTestSuite) TestDefinitionFallsBackToRefererForOrigin() {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/pub-key", nil)
	req.Header.Set("Referer", "https://customer.example/contact")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal("https://customer.example/contact", suite.stub.lastOrigin)
}

func (suite *FormPublicHandlerTestSuite) TestDefinitionUnknownKeyIs404() {
	suite.stub.definition = nil
	suite.stub.definitionErr = fmt.Errorf("form not found: %w", apperrors.ErrNotFound)

	w := suite.do(http.MethodGet, "/api/v1/forms/public/nope", nil)

	suite.Equal(http.StatusNotFound, w.Code)
	response := suite.decode(w)
	suite.False(response.Success)
	suite.Require().NotNil(response.Error)
	suite.Equal(utils.ErrCodeNotFound, response.Error.Code)
}

func (suite *FormPublicHandlerTestSuite) TestSubmitHappyPath() {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions",
		bytes.NewBufferString(`{"values":{"email":"visitor@example.com"},"consent":true,"challenge":"MTIz.abcdef","website_url_confirm":"","page_url":"https://customer.example/contact"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://customer.example")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(1, suite.stub.submitCalls)
	suite.Require().NotNil(suite.stub.lastRequest)
	suite.Equal("visitor@example.com", suite.stub.lastRequest.Values["email"])
	suite.True(suite.stub.lastRequest.Consent)
	suite.Equal("MTIz.abcdef", suite.stub.lastRequest.Challenge)
	suite.Equal("https://customer.example/contact", suite.stub.lastRequest.PageURL)
	suite.Equal("https://customer.example", suite.stub.lastMeta.Origin)
	suite.Equal("Mozilla/5.0", suite.stub.lastMeta.UserAgent)
	suite.NotEmpty(suite.stub.lastMeta.IP)

	response := suite.decode(w)
	suite.True(response.Success)
	data, ok := response.Data.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal(models.FormSubmitActionMessage, data["action"])
	suite.Equal("Thank you.", data["message"])
	suite.Equal(false, data["pending_confirmation"])
}

func (suite *FormPublicHandlerTestSuite) TestSubmitCarriesTheHoneypotValue() {
	w := suite.do(http.MethodPost, "/api/v1/forms/public/pub-key/submissions", gin.H{
		"values":              gin.H{"email": "visitor@example.com"},
		"challenge":           "MTIz.abcdef",
		"website_url_confirm": "http://spam.example",
	})

	suite.Equal(http.StatusOK, w.Code)
	suite.Require().NotNil(suite.stub.lastRequest)
	suite.Equal("http://spam.example", suite.stub.lastRequest.Honeypot)
}

func (suite *FormPublicHandlerTestSuite) TestSubmitTruncatesTheUserAgent() {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions",
		bytes.NewBufferString(`{"values":{},"challenge":"MTIz.abcdef"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", strings.Repeat("ä", 400))
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(255, len([]rune(suite.stub.lastMeta.UserAgent)))
}

func (suite *FormPublicHandlerTestSuite) TestSubmitRejectsOversizedBody() {
	// A single field far beyond the cap: the reader must stop it before the
	// body is ever parsed.
	body := fmt.Sprintf(`{"values":{"message":%q},"challenge":"MTIz.abcdef"}`, strings.Repeat("x", 70<<10))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusRequestEntityTooLarge, w.Code)
	suite.Equal(0, suite.stub.submitCalls)
	response := suite.decode(w)
	suite.False(response.Success)
	suite.Require().NotNil(response.Error)
	suite.Equal(utils.ErrCodeValidation, response.Error.Code)
}

func (suite *FormPublicHandlerTestSuite) TestSubmitRejectsMalformedBody() {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions",
		bytes.NewBufferString(`{"values":`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusBadRequest, w.Code)
	suite.Equal(0, suite.stub.submitCalls)
}

func (suite *FormPublicHandlerTestSuite) TestSubmitSurfacesFieldErrors() {
	suite.stub.outcome = nil
	suite.stub.submitErr = service.FieldErrors{
		"email":     "Email is required",
		"challenge": "the form session is missing; reload the page and try again",
	}

	w := suite.do(http.MethodPost, "/api/v1/forms/public/pub-key/submissions", gin.H{
		"values": gin.H{},
	})

	suite.Equal(http.StatusBadRequest, w.Code)
	response := suite.decode(w)
	suite.False(response.Success)
	suite.Require().NotNil(response.Error)
	suite.Equal(utils.ErrCodeValidation, response.Error.Code)

	details, ok := response.Error.Details.(map[string]interface{})
	suite.Require().True(ok)
	suite.Equal("Email is required", details["email"])
	suite.Equal("the form session is missing; reload the page and try again", details["challenge"])
}

func (suite *FormPublicHandlerTestSuite) TestSubmitUnknownFormIs404() {
	suite.stub.outcome = nil
	suite.stub.submitErr = fmt.Errorf("form not found: %w", apperrors.ErrNotFound)

	w := suite.do(http.MethodPost, "/api/v1/forms/public/nope/submissions", gin.H{
		"values": gin.H{"email": "visitor@example.com"},
	})

	suite.Equal(http.StatusNotFound, w.Code)
}

func (suite *FormPublicHandlerTestSuite) TestConfirmPageNeverSpendsTheToken() {
	token := `a"><script>alert(1)</script>`
	w := suite.do(http.MethodGet,
		"/api/v1/forms/public/confirm?token="+url.QueryEscape(token), nil)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(formPageContentType, w.Header().Get("Content-Type"))
	// The whole point of the GET/POST split: a link preview or a mail scanner
	// that fetches this URL must not confirm anything.
	suite.Equal(0, suite.stub.confirmCalls)

	body := w.Body.String()
	suite.Contains(body, `action="/api/v1/forms/public/confirm"`)
	suite.Contains(body, `name="token"`)
	suite.Contains(body, `a&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`)
	suite.NotContains(body, `<script>alert(1)</script>`)
}

func (suite *FormPublicHandlerTestSuite) TestConfirmPageWithoutTokenShowsTheInvalidPage() {
	w := suite.do(http.MethodGet, "/api/v1/forms/public/confirm", nil)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(0, suite.stub.confirmCalls)
	suite.Contains(w.Body.String(), "invalid")
}

func (suite *FormPublicHandlerTestSuite) TestConfirmAcceptsFormEncodedToken() {
	form := url.Values{"token": {"raw-token"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/confirm",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(formPageContentType, w.Header().Get("Content-Type"))
	suite.Equal(1, suite.stub.confirmCalls)
	suite.Equal("raw-token", suite.stub.lastToken)
	suite.Contains(w.Body.String(), "Email confirmed")
}

func (suite *FormPublicHandlerTestSuite) TestConfirmAcceptsJSONToken() {
	w := suite.do(http.MethodPost, "/api/v1/forms/public/confirm", gin.H{"token": "raw-token"})

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(1, suite.stub.confirmCalls)
	suite.Equal("raw-token", suite.stub.lastToken)
}

func (suite *FormPublicHandlerTestSuite) TestConfirmRejectedTokenShowsTheInvalidPage() {
	suite.stub.confirmErr = service.ErrInvalidConfirmationToken

	w := suite.do(http.MethodPost, "/api/v1/forms/public/confirm", gin.H{"token": "spent"})

	// Deliberately 200: the page is the answer, and the status says nothing
	// about whether the token existed.
	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(1, suite.stub.confirmCalls)
	suite.Contains(strings.ToLower(w.Body.String()), "invalid")
}

func (suite *FormPublicHandlerTestSuite) TestConfirmWithoutTokenDoesNotReachTheService() {
	w := suite.do(http.MethodPost, "/api/v1/forms/public/confirm", gin.H{})

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(0, suite.stub.confirmCalls)
	suite.Contains(strings.ToLower(w.Body.String()), "invalid")
}

func (suite *FormPublicHandlerTestSuite) TestViewPageLoadsTheEmbedScript() {
	w := suite.do(http.MethodGet, "/api/v1/forms/public/pub-key/view", nil)

	suite.Equal(http.StatusOK, w.Code)
	suite.Equal(formPageContentType, w.Header().Get("Content-Type"))

	body := w.Body.String()
	suite.Contains(body, `src="/api/v1/forms/public/embed.js"`)
	suite.Contains(body, `data-form-key="pub-key"`)
	// The page is a shell; nothing about the form is fetched server-side.
	suite.Equal(0, suite.stub.definitionCalls)
}

func (suite *FormPublicHandlerTestSuite) TestViewPageEscapesTheKey() {
	w := suite.do(http.MethodGet, `/api/v1/forms/public/`+url.PathEscape(`"><b>`)+`/view`, nil)

	suite.Equal(http.StatusOK, w.Code)
	suite.NotContains(w.Body.String(), `<b>`)
}

// The prefix is configurable, so the pages must build their links from it
// rather than from a hard-coded "/api/v1".
func TestFormPublicPagesFollowTheConfiguredAPIPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewFormPublicHandler(&formPublicServiceStub{}, config.FormsConfig{}, "crm/")
	router := gin.New()
	SetupFormPublicRoutes(router.Group("/crm"), handler)

	req := httptest.NewRequest(http.MethodGet, "/crm/forms/public/confirm?token=abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Body.String(); !strings.Contains(got, `action="/crm/forms/public/confirm"`) {
		t.Errorf("confirm page does not post to the configured prefix: %s", got)
	}
}

// The embed script is compiled into the binary, so its contract with the API
// is checked here rather than in a browser: the attribute it reads, the
// honeypot key it falls back to, the challenge it echoes and the style tag it
// deduplicates by id.
func TestFormEmbedScriptContract(t *testing.T) {
	script := string(formEmbedJS)

	for _, needle := range []string{
		"data-form-key",
		"website_url_confirm",
		"honeypot_field",
		"challenge",
		"captcha_token",
		"page_url",
		"gcrm-form-styles",
		"--gcrm-accent",
		"--gcrm-bg",
		"--gcrm-text",
		"--gcrm-radius",
		"--gcrm-font",
		"document.currentScript",
		"/forms/public/embed.js",
		"pending_confirmation",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("embed script does not mention %q", needle)
		}
	}

	// The honeypot has to stay off-screen rather than hidden: a bot that skips
	// display:none inputs is precisely the one this layer catches, and a real
	// visitor must never tab into it or have it autofilled.
	for _, needle := range []string{
		".gcrm-trap",
		"position: absolute",
		"left: -9999px",
		"tabIndex = -1",
		"autocomplete",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("embed script honeypot is missing %q", needle)
		}
	}
}

// requestMarker tags a request context so a test can tell that the service
// received the request's own context rather than a fresh one (issue #107).
type requestMarker struct{}

func (suite *FormPublicHandlerTestSuite) TestSubmitAndConfirmRunOnTheRequestContext() {
	ctx := context.WithValue(context.Background(), requestMarker{}, "submit")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/pub-key/submissions",
		bytes.NewBufferString(`{"values":{"email":"visitor@example.com"},"challenge":"MTIz.abcdef"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	suite.router.ServeHTTP(httptest.NewRecorder(), req)

	suite.Require().Equal(1, suite.stub.submitCalls)
	suite.Require().NotNil(suite.stub.lastCtx)
	suite.Equal("submit", suite.stub.lastCtx.Value(requestMarker{}))

	ctx = context.WithValue(context.Background(), requestMarker{}, "confirm")
	form := url.Values{"token": {"raw-token"}}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/confirm",
		strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	suite.router.ServeHTTP(httptest.NewRecorder(), req)

	suite.Require().Equal(1, suite.stub.confirmCalls)
	suite.Equal("confirm", suite.stub.lastCtx.Value(requestMarker{}))
}

// ---------------------------------------------------------------------------
// #340: the hosted view's per-render idempotency key
// ---------------------------------------------------------------------------

// hostedViewKeyAttr extracts the data-idempotency-key attribute of the script
// tag, whatever its value; the tests then check the value's shape.
var hostedViewKeyAttr = regexp.MustCompile(`<script [^>]*data-idempotency-key="([^"]*)"`)

// hostedViewHexKey is the exact shape of a rendered key.
var hostedViewHexKey = regexp.MustCompile(`^[0-9a-f]{32}$`)

// hostedViewAnyHexKey finds a rendered key anywhere in captured log output.
var hostedViewAnyHexKey = regexp.MustCompile(`[0-9a-f]{32}`)

// hostedViewLogs routes the shared logger into a buffer for one test.
func hostedViewLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "debug", Format: "json"}))
	var logs bytes.Buffer
	utils.Logger.SetOutput(&logs)
	t.Cleanup(func() { utils.Logger.SetOutput(os.Stdout) })
	return &logs
}

// hostedViewRouter mounts the public routes on a stub service, so any service
// call the hosted view makes would be counted.
func hostedViewRouter(t *testing.T, random io.Reader) (*gin.Engine, *formPublicServiceStub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stub := &formPublicServiceStub{}
	h := NewFormPublicHandler(stub, config.FormsConfig{}, "/api/v1")
	if random != nil {
		h.random = random
	}
	router := gin.New()
	SetupFormPublicRoutes(router.Group("/api/v1"), h)
	return router, stub
}

func hostedViewGet(router *gin.Engine, publicKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/"+url.PathEscape(publicKey)+"/view", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// AC-1: every render carries its own valid key, escaped, with no service call.
func TestHostedViewRendersPerRenderIdempotencyKey(t *testing.T) {
	logs := hostedViewLogs(t)
	router, stub := hostedViewRouter(t, nil)
	const publicKey = "pub-key-hosted-340"

	keys := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		w := hostedViewGet(router, publicKey)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, formPageContentType, w.Header().Get("Content-Type"))

		body := w.Body.String()
		assert.Contains(t, body, `data-form-key="`+publicKey+`"`)
		match := hostedViewKeyAttr.FindStringSubmatch(body)
		require.Len(t, match, 2, "the script tag carries data-idempotency-key")
		key := match[1]
		assert.Regexp(t, hostedViewHexKey, key, "32 lowercase hex characters")
		assert.True(t, service.ValidFormIdempotencyKey(key), "the submit endpoint accepts the rendered key")
		assert.Equal(t, 1, strings.Count(body, "data-idempotency-key="), "exactly one key per render")
		keys = append(keys, key)
	}
	assert.NotEqual(t, keys[0], keys[1], "two renders of the same form get different keys")

	// The view is a shell: it makes no service call of any kind.
	assert.Zero(t, stub.definitionCalls)
	assert.Zero(t, stub.submitCalls)
	assert.Zero(t, stub.confirmCalls)

	// The keys are never logged.
	for _, key := range keys {
		assert.NotContains(t, logs.String(), key)
	}

	// The value is HTML-escaped on insertion, like every other dynamic value.
	page := string(formHostedViewPage("/api/v1/forms/public/embed.js", "k", `a"><script>alert(1)</script>`))
	assert.Contains(t, page, `data-idempotency-key="a&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`)
	assert.NotContains(t, page, `<script>alert(1)</script>`)

	// An empty key omits the attribute rather than rendering an empty one.
	assert.NotContains(t, string(formHostedViewPage("/api/v1/forms/public/embed.js", "k", "")), "data-idempotency-key")
}

// AC-2: the hosted view is never stored by a cache.
func TestHostedViewIsNotCached(t *testing.T) {
	hostedViewLogs(t)

	router, _ := hostedViewRouter(t, nil)
	w := hostedViewGet(router, "pub-key")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Regexp(t, hostedViewKeyAttr, w.Body.String())

	// The page without a key is not cacheable either.
	router, _ = hostedViewRouter(t, hostedViewFailingReader{})
	w = hostedViewGet(router, "pub-key")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	// The embed script keeps its own public caching.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/embed.js", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	assert.Equal(t, "public, max-age=3600", recorder.Header().Get("Cache-Control"))
}

// hostedViewFailingReader is a random source that has nothing to give.
type hostedViewFailingReader struct{}

func (hostedViewFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

// hostedViewShortReader returns fewer bytes than asked and then EOF, which
// io.ReadFull must treat as a failure rather than render a short key.
type hostedViewShortReader struct{ served bool }

func (r *hostedViewShortReader) Read(p []byte) (int, error) {
	if r.served {
		return 0, io.EOF
	}
	r.served = true
	n := copy(p, []byte{0xab, 0xcd, 0xef})
	return n, nil
}

// AC-3: a failing random source still serves the shell, without the attribute,
// and logs one key-free Warn line.
func TestHostedViewWithoutRandomnessOmitsKey(t *testing.T) {
	const publicKey = "pub-key-secret-marker-340"

	for name, source := range map[string]io.Reader{
		"read error": hostedViewFailingReader{},
		"short read": &hostedViewShortReader{},
	} {
		t.Run(name, func(t *testing.T) {
			logs := hostedViewLogs(t)
			router, stub := hostedViewRouter(t, source)

			w := hostedViewGet(router, publicKey)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, formPageContentType, w.Header().Get("Content-Type"))
			body := w.Body.String()
			assert.Contains(t, body, `src="/api/v1/forms/public/embed.js"`)
			assert.Contains(t, body, `data-form-key="`+publicKey+`"`)
			assert.NotContains(t, body, "data-idempotency-key")
			assert.Zero(t, stub.definitionCalls+stub.submitCalls+stub.confirmCalls)

			output := logs.String()
			assert.NotContains(t, output, publicKey, "the form's public key is not logged")
			assert.NotRegexp(t, hostedViewAnyHexKey, output, "no idempotency key is logged")

			var warnings []map[string]interface{}
			scanner := bufio.NewScanner(strings.NewReader(output))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var entry map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(line), &entry), "log lines are JSON")
				if entry["level"] == "warning" {
					warnings = append(warnings, entry)
				}
			}
			require.LessOrEqual(t, len(warnings), 1, "at most one Warn line")
			require.Len(t, warnings, 1, "the failure is logged")
			warning := warnings[0]
			assert.Equal(t, "hosted_view_key", warning["operation"])
			assert.Equal(t, "random_unavailable", warning["error_code"])
			for field := range warning {
				switch field {
				case "level", "msg", "time", "operation", "error_code":
				default:
					t.Errorf("the Warn line carries an unexpected field %q", field)
				}
			}
		})
	}
}

// hostedViewMailer captures every mail the form service sends.
type hostedViewMailer struct {
	mu   sync.Mutex
	sent []hostedViewMail
}

type hostedViewMail struct {
	To   string
	Body string
}

func (m *hostedViewMailer) SendPasswordReset(string, string, string) error { return nil }

func (m *hostedViewMailer) Send(to, _ string, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, hostedViewMail{To: to, Body: body})
	return nil
}

// newestConfirmationToken returns the token of the newest confirmation link
// mailed to address.
func (m *hostedViewMailer) newestConfirmationToken(t *testing.T, address string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	const marker = "/forms/public/confirm?token="
	for i := len(m.sent) - 1; i >= 0; i-- {
		mail := m.sent[i]
		if !strings.EqualFold(mail.To, address) {
			continue
		}
		index := strings.Index(mail.Body, marker)
		if index == -1 {
			continue
		}
		start := strings.LastIndex(mail.Body[:index], "http")
		require.NotEqual(t, -1, start, "the confirmation link is absolute")
		link := mail.Body[start:]
		if end := strings.IndexAny(link, " \r\n\t"); end != -1 {
			link = link[:end]
		}
		parsed, err := url.Parse(link)
		require.NoError(t, err)
		token := parsed.Query().Get("token")
		require.NotEmpty(t, token)
		return token
	}
	t.Fatalf("no confirmation link was mailed to the submitter")
	return ""
}

const (
	hostedViewSecret    = "hosted-view-idempotency-secret-32-characters"
	hostedViewSubmitter = "visitor@hosted-view.test"
)

type hostedViewEnv struct {
	db     *gorm.DB
	router *gin.Engine
	mailer *hostedViewMailer
	form   *models.Form
}

// newHostedViewEnv wires the real form service with ticket intake over a
// one-connection in-memory SQLite database built by models.MigrateDatabase,
// seeds a live customer with the submitter's address and publishes a
// double-opt-in create_ticket form.
func newHostedViewEnv(t *testing.T) *hostedViewEnv {
	t.Helper()
	require.NoError(t, utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"}))
	gin.SetMode(gin.TestMode)
	// Set before SetupFormPublicRoutes builds the strict tier, which reads it.
	t.Setenv("DISABLE_RATE_LIMIT", "true")
	t.Setenv("API_KEY_SECRET", "")
	t.Setenv("JWT_SECRET", hostedViewSecret)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	previous := models.DB
	models.DB = db
	err = models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	txManager := utils.NewTransactionManager(db)
	mail := &hostedViewMailer{}
	svc := service.NewFormService(repository.NewFormRepository(db), repository.NewLeadRepository(db),
		repository.NewUserRepository(db), mail, txManager,
		config.FormsConfig{PublicBaseURL: "https://crm.hosted-view.test"}, "/api/v1",
		service.WithFormTicketIntake(repository.NewFormTicketRepository(db), txManager))

	owner := &models.User{Email: "owner@hosted-view.test", Password: "x", FirstName: "Own", LastName: "Er",
		Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(owner).Error)
	require.NoError(t, db.Create(&models.Customer{FirstName: "Vis", LastName: "Itor", Email: hostedViewSubmitter}).Error)

	form := &models.Form{
		Name:   "Hosted complaints",
		Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{
			{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			{Name: "complaint", Label: "Complaint", Type: models.FormFieldTextarea, Required: true},
		},
		DoubleOptIn:  true,
		CreateTicket: true,
	}
	require.NoError(t, svc.Create(form, owner.ID))
	require.NotEmpty(t, form.PublicID)

	router := gin.New()
	SetupFormPublicRoutes(router.Group("/api/v1"), NewFormPublicHandler(svc, config.FormsConfig{}, "/api/v1"))
	return &hostedViewEnv{db: db, router: router, mailer: mail, form: form}
}

// render GETs the hosted view and returns its data-idempotency-key.
func (e *hostedViewEnv) render(t *testing.T) string {
	t.Helper()
	w := hostedViewGet(e.router, e.form.PublicID)
	require.Equal(t, http.StatusOK, w.Code)
	match := hostedViewKeyAttr.FindStringSubmatch(w.Body.String())
	require.Len(t, match, 2, "the hosted view renders a key")
	require.Regexp(t, hostedViewHexKey, match[1])
	return match[1]
}

// submitAndConfirm posts one submission carrying key, as the embed script
// will once it sends the attribute, then confirms it through the mailed link.
func (e *hostedViewEnv) submitAndConfirm(t *testing.T, key string) {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"values":          map[string]string{"email": hostedViewSubmitter, "complaint": "The parcel never arrived."},
		"consent":         true,
		"challenge":       forms.NewChallenge([]byte(hostedViewSecret), time.Now().Add(-30*time.Second)),
		"page_url":        "https://crm.hosted-view.test/api/v1/forms/public/" + e.form.PublicID + "/view",
		"idempotency_key": key,
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/"+e.form.PublicID+"/submissions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	token := e.mailer.newestConfirmationToken(t, hostedViewSubmitter)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/forms/public/confirm",
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Email confirmed")
}

func (e *hostedViewEnv) submissions(t *testing.T) []models.FormSubmission {
	t.Helper()
	var rows []models.FormSubmission
	require.NoError(t, e.db.Where("form_id = ?", e.form.ID).Order("id").Find(&rows).Error)
	return rows
}

func (e *hostedViewEnv) ticketCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Unscoped().Model(&models.Ticket{}).Count(&n).Error)
	return n
}

// AC-4: the rendered key, resubmitted and confirmed twice, opens one ticket;
// keys of two separate renders open two.
func TestHostedFormResubmissionWithSameKeyOpensOneTicket(t *testing.T) {
	t.Run("same render", func(t *testing.T) {
		env := newHostedViewEnv(t)
		key := env.render(t)

		env.submitAndConfirm(t, key)
		env.submitAndConfirm(t, key)

		assert.Equal(t, int64(1), env.ticketCount(t), "one ticket for one rendered key")
		rows := env.submissions(t)
		require.Len(t, rows, 2, "two submission rows")
		for _, row := range rows {
			assert.Equal(t, models.FormSubmissionConfirmed, row.Status)
			assert.Equal(t, key, row.IdempotencyKey)
			require.NotNil(t, row.TicketID)
		}
		assert.Equal(t, models.FormTicketOutcomeCreated, rows[0].TicketOutcome)
		assert.Equal(t, models.FormTicketOutcomeDuplicate, rows[1].TicketOutcome)
		assert.Equal(t, *rows[0].TicketID, *rows[1].TicketID, "the resubmission links the same ticket")
	})

	t.Run("separate renders", func(t *testing.T) {
		env := newHostedViewEnv(t)
		first := env.render(t)
		second := env.render(t)
		require.NotEqual(t, first, second)

		env.submitAndConfirm(t, first)
		env.submitAndConfirm(t, second)

		assert.Equal(t, int64(2), env.ticketCount(t), "one ticket per render")
		rows := env.submissions(t)
		require.Len(t, rows, 2, "two submission rows")
		assert.Equal(t, first, rows[0].IdempotencyKey)
		assert.Equal(t, second, rows[1].IdempotencyKey)
		for _, row := range rows {
			assert.Equal(t, models.FormTicketOutcomeCreated, row.TicketOutcome)
			require.NotNil(t, row.TicketID)
		}
		assert.NotEqual(t, *rows[0].TicketID, *rows[1].TicketID)
	})
}
