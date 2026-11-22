// Package scimtest is a SCIM client for tests. It sends requests straight to
// an http.Handler with a bearer token and the SCIM media type, and decodes
// the answers, including the RFC 7644 §3.12 error body.
//
// Only tests import this package.
package scimtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
)

// Client sends SCIM requests to Handler.
type Client struct {
	Handler http.Handler
	// Token is sent as "Authorization: Bearer <Token>" when non-empty.
	Token string
	// Prepare, when set, runs on every request just before it is served,
	// for fields such as RemoteAddr or extra headers.
	Prepare func(*http.Request)
}

// New returns a client for handler that authenticates with token.
func New(handler http.Handler, token string) *Client {
	return &Client{Handler: handler, Token: token}
}

// Do sends one request to target (a path with an optional query) and
// returns the answer. A non-empty body is sent with the SCIM content type;
// every request accepts the SCIM content type.
func (c *Client) Do(method, target, body string) *Response {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Accept", scim.ContentType)
	if body != "" {
		request.Header.Set("Content-Type", scim.ContentType)
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Prepare != nil {
		c.Prepare(request)
	}
	recorder := httptest.NewRecorder()
	c.Handler.ServeHTTP(recorder, request)
	return &Response{Status: recorder.Code, Header: recorder.Header(), Body: recorder.Body.Bytes()}
}

// Response is one answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Decode unmarshals the body into v.
func (r *Response) Decode(v interface{}) error {
	return json.Unmarshal(r.Body, v)
}

// Error is the RFC 7644 §3.12 error body. Status is the HTTP status code as
// a string.
type Error struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType"`
	Detail   string   `json:"detail"`
}

// SCIMError decodes the body as a SCIM error. The second result is false
// when the body is not JSON or does not name the error schema.
func (r *Response) SCIMError() (Error, bool) {
	var body Error
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return Error{}, false
	}
	for _, schema := range body.Schemas {
		if schema == scim.ErrorSchema {
			return body, true
		}
	}
	return Error{}, false
}
