package forms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultRecaptchaEndpoint is the reCAPTCHA v3 server-side verification URL.
const defaultRecaptchaEndpoint = "https://www.google.com/recaptcha/api/siteverify"

// recaptchaTimeout bounds the whole verification exchange. A public form
// submission waits on this, so it stays short — a slow captcha service must not
// hold a visitor's browser open.
const recaptchaTimeout = 5 * time.Second

// recaptchaMaxResponseBytes caps how much of the verification response is read.
// The real payload is a few hundred bytes; anything larger is a broken or
// hostile endpoint.
const recaptchaMaxResponseBytes = 64 << 10

// RecaptchaVerifier checks client-side captcha tokens against the verification
// service. It is safe for concurrent use.
type RecaptchaVerifier struct {
	secret   string
	minScore float64
	endpoint string
	client   *http.Client
}

// RecaptchaOption customises a verifier at construction time.
type RecaptchaOption func(*RecaptchaVerifier)

// WithRecaptchaEndpoint overrides the verification URL. Production uses the
// default; this exists so the verifier can be pointed at a local stub.
func WithRecaptchaEndpoint(endpoint string) RecaptchaOption {
	return func(v *RecaptchaVerifier) {
		if endpoint != "" {
			v.endpoint = endpoint
		}
	}
}

// WithRecaptchaHTTPClient overrides the HTTP client, including its timeout.
func WithRecaptchaHTTPClient(client *http.Client) RecaptchaOption {
	return func(v *RecaptchaVerifier) {
		if client != nil {
			v.client = client
		}
	}
}

// NewRecaptchaVerifier builds a verifier for the given server-side secret. A
// token passes when the service accepts it and its score reaches minScore.
func NewRecaptchaVerifier(secret string, minScore float64, opts ...RecaptchaOption) *RecaptchaVerifier {
	v := &RecaptchaVerifier{
		secret:   secret,
		minScore: minScore,
		endpoint: defaultRecaptchaEndpoint,
		client:   &http.Client{Timeout: recaptchaTimeout},
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// Verify reports whether the token is genuine and scored well enough.
// remoteIP is optional and omitted from the request when empty.
//
// A transport, status or decode failure returns (false, err): the caller
// decides how to treat an unreachable verification service, and must never read
// the error as a pass.
func (v *RecaptchaVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	form := url.Values{}
	form.Set("secret", v.secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("build captcha verification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("call captcha verification service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("captcha verification service returned status %d", resp.StatusCode)
	}

	var payload struct {
		Success bool    `json:"success"`
		Score   float64 `json:"score"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, recaptchaMaxResponseBytes)).Decode(&payload); err != nil {
		return false, fmt.Errorf("decode captcha verification response: %w", err)
	}

	return payload.Success && payload.Score >= v.minScore, nil
}
