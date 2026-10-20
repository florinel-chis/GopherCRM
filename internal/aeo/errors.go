package aeo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"unicode/utf8"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Failure categories. They are the only provider-failure detail that is
// persisted (in AEOAnswer.Error), logged or returned to API callers, together
// with the numeric HTTP status. Remote response bodies, SDK error strings,
// request URLs and headers are never carried over: redacting arbitrary remote
// text cannot be done reliably, so it is dropped instead.
const (
	FailureHTTPStatus              = "http_status"
	FailureTimeout                 = "timeout"
	FailureCanceled                = "canceled"
	FailureEndpointNotPermitted    = "endpoint_not_permitted"
	FailureDestinationNotPermitted = "destination_not_permitted"
	FailureRedirectNotFollowed     = "redirect_not_followed"
	FailureDNS                     = "dns"
	FailureTLS                     = "tls"
	FailureConnection              = "connection"
	FailureInvalidResponse         = "invalid_response"
	FailureEmptyResponse           = "empty_response"
	FailurePanic                   = "panic"
	FailureUnknown                 = "request_failed"
)

var failureMessages = map[string]string{
	FailureTimeout:                 "request timed out",
	FailureCanceled:                "request canceled",
	FailureEndpointNotPermitted:    "endpoint not permitted by the AEO egress policy",
	FailureDestinationNotPermitted: "destination address not permitted by the AEO egress policy",
	FailureRedirectNotFollowed:     "redirect response not followed",
	FailureDNS:                     "DNS resolution failed",
	FailureTLS:                     "TLS verification failed",
	FailureConnection:              "connection failed",
	FailureInvalidResponse:         "invalid provider response",
	FailureEmptyResponse:           "empty provider response",
	FailurePanic:                   "provider panicked",
	FailureUnknown:                 "provider request failed",
}

// maxProviderErrorLength bounds every provider error string.
const maxProviderErrorLength = 160

// errEmptyResponse marks an SDK call that returned neither a value nor an error.
var errEmptyResponse = errors.New("aeo: empty provider response")

// ProviderError is the sanitized form of every provider failure. Its message
// is built only from the engine name, a fixed category text and the numeric
// HTTP status. Unwrap exposes only trusted sentinels (context.Canceled,
// context.DeadlineExceeded and the egress errors), never the SDK error, so no
// formatting or unwrapping can reach provider-controlled text.
type ProviderError struct {
	Provider   string
	Category   string
	StatusCode int
	cause      error
}

func (e *ProviderError) Error() string {
	message, ok := failureMessages[e.Category]
	if e.Category == FailureHTTPStatus {
		message, ok = httpStatusMessage(e.StatusCode), true
	}
	if !ok {
		message = failureMessages[FailureUnknown]
	}
	text := safeProviderName(e.Provider) + ": " + message
	if e.StatusCode > 0 {
		text += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	return truncateRunes(text, maxProviderErrorLength)
}

// LogMessage lets utils.LogError log the same safe text.
func (e *ProviderError) LogMessage() string { return e.Error() }

func (e *ProviderError) Unwrap() error { return e.cause }

func httpStatusMessage(status int) string {
	switch {
	case status == 401 || status == 403:
		return "authentication rejected by the provider"
	case status == 404:
		return "endpoint or model not found at the provider"
	case status == 429:
		return "rate limited by the provider"
	case status >= 400 && status < 500:
		return "request rejected by the provider"
	case status >= 500 && status < 600:
		return "provider unavailable"
	default:
		return "unexpected HTTP status from the provider"
	}
}

// ProviderFailureCategory returns the category of a sanitized provider error,
// or "" when err is not one.
func ProviderFailureCategory(err error) string {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Category
	}
	return ""
}

// SanitizeProviderError converts any provider failure into a *ProviderError.
// It accepts SDK errors, transport errors and arbitrary errors from custom
// Provider implementations alike; none of their text survives. A nil error
// stays nil.
func SanitizeProviderError(provider string, err error) (sanitized error) {
	if err == nil {
		return nil
	}
	name := safeProviderName(provider)
	// A hostile error type could panic in Unwrap or As; that is a failure too.
	defer func() {
		if recover() != nil {
			sanitized = &ProviderError{Provider: name, Category: FailureUnknown}
		}
	}()

	var existing *ProviderError
	if errors.As(err, &existing) && existing != nil {
		clean := &ProviderError{Provider: name, Category: existing.Category, cause: existing.cause}
		if _, known := failureMessages[clean.Category]; !known && clean.Category != FailureHTTPStatus {
			clean.Category, clean.cause = FailureUnknown, nil
		}
		if existing.StatusCode >= 100 && existing.StatusCode <= 599 {
			clean.StatusCode = existing.StatusCode
		}
		return clean
	}
	return classifyProviderError(name, err)
}

func classifyProviderError(name string, err error) *ProviderError {
	failure := func(category string, cause error) *ProviderError {
		return &ProviderError{Provider: name, Category: category, cause: cause}
	}

	switch {
	case errors.Is(err, context.Canceled):
		return failure(FailureCanceled, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return failure(FailureTimeout, context.DeadlineExceeded)
	case errors.Is(err, ErrEndpointNotPermitted):
		return failure(FailureEndpointNotPermitted, ErrEndpointNotPermitted)
	case errors.Is(err, ErrDestinationNotPermitted):
		return failure(FailureDestinationNotPermitted, ErrDestinationNotPermitted)
	case errors.Is(err, errEmptyResponse):
		return failure(FailureEmptyResponse, nil)
	}

	var redirect *redirectError
	if errors.As(err, &redirect) {
		result := failure(FailureRedirectNotFollowed, ErrRedirectNotFollowed)
		result.StatusCode = redirect.statusCode
		return result
	}
	if errors.Is(err, ErrRedirectNotFollowed) {
		return failure(FailureRedirectNotFollowed, ErrRedirectNotFollowed)
	}
	if status := sdkHTTPStatus(err); status != 0 {
		result := failure(FailureHTTPStatus, nil)
		if status >= 100 && status <= 599 {
			result.StatusCode = status
		}
		return result
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return failure(FailureDNS, nil)
	}
	if isTLSFailure(err) {
		return failure(FailureTLS, nil)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return failure(FailureTimeout, nil)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return failure(FailureConnection, nil)
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return failure(FailureInvalidResponse, nil)
	}
	return failure(FailureUnknown, nil)
}

func isTLSFailure(err error) bool {
	var verification *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	return errors.As(err, &verification) || errors.As(err, &hostname) || errors.As(err, &authority) ||
		errors.As(err, &invalid) || errors.As(err, &record) || errors.As(err, &alert)
}

// sdkHTTPStatus reads the status the SDKs attach to API errors.
func sdkHTTPStatus(err error) int {
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) && openaiErr != nil {
		return openaiErr.StatusCode
	}
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) && anthropicErr != nil {
		return anthropicErr.StatusCode
	}
	return 0
}

// safeProviderName keeps an engine name printable and bounded: it appears in
// every error string, and the custom engine's name is operator-chosen.
func safeProviderName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '/', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	clean := strings.TrimSpace(truncateRunes(b.String(), models.AEOAnswerProviderMaxLength))
	if clean == "" {
		return "provider"
	}
	return clean
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
