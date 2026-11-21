package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// TestFormPublicPagesSendBrowserHeaders pins the browser headers of every HTML
// page the public form routes serve, and that the JSON routes carry none.
func TestFormPublicPagesSendBrowserHeaders(t *testing.T) {
	utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "json"})
	gin.SetMode(gin.TestMode)
	// The confirmation routes sit on the strict tier; the cases below issue
	// more requests than its burst from one address.
	t.Setenv("DISABLE_RATE_LIMIT", "true")

	confirmPage := map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
	}
	hostedPage := map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self' https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/; frame-src https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/; connect-src 'self' https://www.google.com/recaptcha/; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
	}

	cases := []struct {
		name       string
		method     string
		path       string
		form       url.Values
		confirmErr error
		heading    string
		headers    map[string]string
	}{
		{name: "hosted form page", method: http.MethodGet, path: "/api/v1/forms/public/pub-key/view", heading: "/api/v1/forms/public/embed.js", headers: hostedPage},
		{name: "confirm page with a token", method: http.MethodGet, path: "/api/v1/forms/public/confirm?token=abc", heading: "<h1>Confirm your email address</h1>", headers: confirmPage},
		{name: "confirm page without a token", method: http.MethodGet, path: "/api/v1/forms/public/confirm", heading: "<h1>This link is no longer valid</h1>", headers: confirmPage},
		{name: "confirm with an empty token", method: http.MethodPost, path: "/api/v1/forms/public/confirm", form: url.Values{"token": {""}}, heading: "<h1>This link is no longer valid</h1>", headers: confirmPage},
		{name: "confirm with a rejected token", method: http.MethodPost, path: "/api/v1/forms/public/confirm", form: url.Values{"token": {"abc"}}, confirmErr: service.ErrInvalidConfirmationToken, heading: "<h1>This link is no longer valid</h1>", headers: confirmPage},
		{name: "confirm success", method: http.MethodPost, path: "/api/v1/forms/public/confirm", form: url.Values{"token": {"abc"}}, heading: "<h1>Email confirmed</h1>", headers: confirmPage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &formPublicServiceStub{confirmErr: tc.confirmErr}
			router := gin.New()
			SetupFormPublicRoutes(router.Group("/api/v1"), NewFormPublicHandler(stub, config.FormsConfig{}, "/api/v1"))

			var req *http.Request
			if tc.form != nil {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, formPageContentType, w.Header().Get("Content-Type"))
			assert.Contains(t, w.Body.String(), tc.heading)
			for name, value := range tc.headers {
				assert.Equal(t, value, w.Header().Get(name), name)
			}
			if tc.headers["X-Frame-Options"] == "" {
				// The hosted form page may be framed by other sites.
				assert.Empty(t, w.Header().Values("X-Frame-Options"))
				assert.NotContains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors")
				assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			}
		})
	}

	t.Run("JSON definition carries no content policy", func(t *testing.T) {
		stub := &formPublicServiceStub{definition: &service.PublicFormDefinition{
			Name:     "Contact us",
			PublicID: "pub-key",
			Fields: []models.FormFieldDef{
				{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true},
			},
			SubmitAction: models.FormSubmitActionMessage,
		}}
		router := gin.New()
		SetupFormPublicRoutes(router.Group("/api/v1"), NewFormPublicHandler(stub, config.FormsConfig{}, "/api/v1"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/forms/public/pub-key", nil))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Empty(t, w.Header().Values("Content-Security-Policy"))
	})
}
