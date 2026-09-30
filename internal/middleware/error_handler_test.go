package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Ensure logger is initialised for tests that use GetLogger via error_handler.
	if utils.Logger == nil {
		_ = utils.InitLogger(&config.LoggingConfig{Level: "error", Format: "text"})
	}
}

func setupErrorHandlerRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID()) // error handler uses GetLogger which reads request_id
	r.Use(ErrorHandler())
	return r
}

func TestErrorHandler_NoErrors(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/ok", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestErrorHandler_PublicError(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/public-err", func(c *gin.Context) {
		c.Status(http.StatusBadRequest)
		_ = c.Error(errors.New("bad input")).SetType(gin.ErrorTypePublic)
	})

	req, _ := http.NewRequest("GET", "/public-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestErrorHandler_PublicErrorDefaultsTo400WhenStatusOK(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/pub-default", func(c *gin.Context) {
		// Don't explicitly set status — leave it at default 200
		_ = c.Error(errors.New("oops")).SetType(gin.ErrorTypePublic)
	})

	req, _ := http.NewRequest("GET", "/pub-default", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// ErrorHandler should fall back to 400 when status is still 200
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestErrorHandler_PrivateError_Returns500(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/internal-err", func(c *gin.Context) {
		_ = c.Error(errors.New("database connection lost")).SetType(gin.ErrorTypePrivate)
	})

	req, _ := http.NewRequest("GET", "/internal-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "INTERNAL_ERROR")
	// Must not leak internal error details
	assert.NotContains(t, w.Body.String(), "database connection lost")
}

func TestErrorHandler_BindingError_GenericFormat(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/bind-err", func(c *gin.Context) {
		_ = c.Error(errors.New("cannot parse JSON")).SetType(gin.ErrorTypeBind)
	})

	req, _ := http.NewRequest("GET", "/bind-err", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid request format")
}

func TestErrorHandler_AlreadyWritten(t *testing.T) {
	r := setupErrorHandlerRouter()
	r.GET("/written", func(c *gin.Context) {
		c.JSON(http.StatusCreated, gin.H{"created": true})
		// Even though we add an error, the response is already written
		_ = c.Error(errors.New("late error"))
	})

	req, _ := http.NewRequest("GET", "/written", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "created")
}

func TestValidationMessage_BoundsFollowTheFieldKind(t *testing.T) {
	type payload struct {
		Name        string  `validate:"min=2,max=5"`
		Probability *int    `validate:"omitempty,min=0,max=100"`
		Count       int     `validate:"min=1,max=10"`
		Ratio       float64 `validate:"max=1.5"`
		Quantity    uint    `validate:"max=3"`
		Score       int     `validate:"gte=0,lte=10"`
	}
	intOf := func(v int) *int { return &v }
	v := validator.New()

	messageFor := func(p payload, field string) string {
		t.Helper()
		err := v.Struct(p)
		var ve validator.ValidationErrors
		if !errors.As(err, &ve) {
			t.Fatalf("expected validation errors, got %v", err)
		}
		for _, fe := range ve {
			if fe.Field() == field {
				return validationMessage(fe)
			}
		}
		t.Fatalf("no error for field %s in %v", field, err)
		return ""
	}
	valid := payload{Name: "abc", Probability: intOf(50), Count: 5, Ratio: 1, Quantity: 1, Score: 5}

	tests := []struct {
		name   string
		mutate func(*payload)
		field  string
		want   string
	}{
		{"string max keeps the length wording", func(p *payload) { p.Name = "abcdef" }, "Name", "Name must be at most 5 characters long"},
		{"string min keeps the length wording", func(p *payload) { p.Name = "a" }, "Name", "Name must be at least 2 characters long"},
		{"int pointer max is a value bound", func(p *payload) { p.Probability = intOf(101) }, "Probability", "Probability must be at most 100"},
		{"int pointer min is a value bound", func(p *payload) { p.Probability = intOf(-1) }, "Probability", "Probability must be at least 0"},
		{"int max is a value bound", func(p *payload) { p.Count = 11 }, "Count", "Count must be at most 10"},
		{"int min is a value bound", func(p *payload) { p.Count = 0 }, "Count", "Count must be at least 1"},
		{"float max is a value bound", func(p *payload) { p.Ratio = 2 }, "Ratio", "Ratio must be at most 1.5"},
		{"uint max is a value bound", func(p *payload) { p.Quantity = 4 }, "Quantity", "Quantity must be at most 3"},
		{"lte is unchanged", func(p *payload) { p.Score = 11 }, "Score", "Score must be less than or equal to 10"},
		{"gte is unchanged", func(p *payload) { p.Score = -1 }, "Score", "Score must be greater than or equal to 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.mutate(&p)
			assert.Equal(t, tc.want, messageFor(p, tc.field))
		})
	}
}
