package forms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewRecaptchaVerifierDefaults(t *testing.T) {
	v := NewRecaptchaVerifier("secret", 0.5)

	if v.endpoint != defaultRecaptchaEndpoint {
		t.Errorf("endpoint = %q, want the default verification endpoint", v.endpoint)
	}
	if v.client == nil || v.client.Timeout != 5*time.Second {
		t.Errorf("client timeout = %v, want 5s", v.client.Timeout)
	}
}

func TestRecaptchaVerifyPostsCredentials(t *testing.T) {
	var (
		gotMethod      string
		gotContentType string
		gotForm        url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		writeVerifyResponse(w, `{"success":true,"score":0.9}`)
	}))
	defer srv.Close()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(srv.URL))
	ok, err := v.Verify(context.Background(), "client-token", "203.0.113.7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("ok = false, want true for score 0.9 against threshold 0.5")
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.HasPrefix(gotContentType, "application/x-www-form-urlencoded") {
		t.Errorf("content type = %q, want form encoding", gotContentType)
	}
	for field, want := range map[string]string{
		"secret":   "server-secret",
		"response": "client-token",
		"remoteip": "203.0.113.7",
	} {
		if got := gotForm.Get(field); got != want {
			t.Errorf("form field %q = %q, want %q", field, got, want)
		}
	}
}

func TestRecaptchaVerifyOmitsEmptyRemoteIP(t *testing.T) {
	var hasRemoteIP bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		_, hasRemoteIP = r.PostForm["remoteip"]
		writeVerifyResponse(w, `{"success":true,"score":1}`)
	}))
	defer srv.Close()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(srv.URL))
	if _, err := v.Verify(context.Background(), "client-token", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasRemoteIP {
		t.Error("remoteip was sent although the caller had no address")
	}
}

func TestRecaptchaVerifyScoreThreshold(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		minScore float64
		want     bool
	}{
		{name: "above threshold", body: `{"success":true,"score":0.9}`, minScore: 0.5, want: true},
		{name: "on threshold", body: `{"success":true,"score":0.5}`, minScore: 0.5, want: true},
		{name: "below threshold", body: `{"success":true,"score":0.1}`, minScore: 0.5, want: false},
		{name: "not successful", body: `{"success":false,"score":0.9}`, minScore: 0.5, want: false},
		{name: "failed with error codes", body: `{"success":false,"error-codes":["timeout-or-duplicate"]}`, minScore: 0.5, want: false},
		{name: "no score reported", body: `{"success":true}`, minScore: 0, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeVerifyResponse(w, tc.body)
			}))
			defer srv.Close()

			v := NewRecaptchaVerifier("server-secret", tc.minScore, WithRecaptchaEndpoint(srv.URL))
			ok, err := v.Verify(context.Background(), "client-token", "203.0.113.7")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.want {
				t.Errorf("ok = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestRecaptchaVerifyServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(srv.URL))
	ok, err := v.Verify(context.Background(), "client-token", "203.0.113.7")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if ok {
		t.Error("ok = true, want false when verification failed")
	}
}

func TestRecaptchaVerifyMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeVerifyResponse(w, `not json`)
	}))
	defer srv.Close()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(srv.URL))
	ok, err := v.Verify(context.Background(), "client-token", "")
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if ok {
		t.Error("ok = true, want false when the response could not be read")
	}
}

func TestRecaptchaVerifyUnreachableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL
	srv.Close()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(endpoint))
	ok, err := v.Verify(context.Background(), "client-token", "")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if ok {
		t.Error("ok = true, want false when the endpoint is unreachable")
	}
}

func TestRecaptchaVerifyHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		writeVerifyResponse(w, `{"success":true,"score":0.9}`)
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	v := NewRecaptchaVerifier("server-secret", 0.5, WithRecaptchaEndpoint(srv.URL))
	if _, err := v.Verify(ctx, "client-token", ""); err == nil {
		t.Fatal("expected the cancelled context to abort the request")
	}
}

func writeVerifyResponse(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
