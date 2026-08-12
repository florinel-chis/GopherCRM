package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"

	"github.com/florinel-chis/gophercrm/internal/config"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
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

func (s *formPublicServiceStub) PublicDefinition(publicID, origin string) (*service.PublicFormDefinition, error) {
	s.definitionCalls++
	s.lastPublicID = publicID
	s.lastOrigin = origin
	return s.definition, s.definitionErr
}

func (s *formPublicServiceStub) SubmitPublic(publicID string, req *service.PublicSubmissionRequest, meta service.SubmissionMeta) (*service.SubmitOutcome, error) {
	s.submitCalls++
	s.lastPublicID = publicID
	s.lastRequest = req
	s.lastMeta = meta
	return s.outcome, s.submitErr
}

func (s *formPublicServiceStub) ConfirmSubmission(rawToken string) error {
	s.confirmCalls++
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
