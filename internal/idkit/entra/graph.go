package entra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit/oidc"
)

// Microsoft Graph group resolution of an Entra ID group overage.
const (
	// GraphBaseURL is the Microsoft Graph origin of the global cloud.
	GraphBaseURL = "https://graph.microsoft.com"
	// GraphTimeout is ONE deadline over every Graph page of a sign-in.
	GraphTimeout = 5 * time.Second
	// GraphScope is the delegated permission a sign-in requests for an
	// Entra ID issuer whose role mapping uses groups: User.Read, the
	// least-privileged permission for /me/getMemberObjects.
	GraphScope = "https://graph.microsoft.com/User.Read"
	// PageMaxBytes bounds one getMemberObjects page.
	PageMaxBytes = 1 << 20
	// DefaultMaxPages is the page count EffectiveMaxPages falls back to.
	DefaultMaxPages = 10
	// MaxPagesLimit is the largest page count EffectiveMaxPages keeps.
	MaxPagesLimit = 50

	memberObjectsPath = "/v1.0/me/getMemberObjects"
	memberObjectsBody = `{"securityEnabledOnly":true}`
)

// Failures of the Microsoft Graph group resolution. They never carry a URL,
// a token, a status text or a response body.
var (
	ErrNoAccessToken = errors.New("graph: no access token")
	ErrRequest       = errors.New("graph: request failed")
	ErrStatus        = errors.New("graph: unexpected status")
	ErrBody          = errors.New("graph: malformed response")
	ErrTooLarge      = errors.New("graph: response too large")
	ErrNextLink      = errors.New("graph: next link refused")
	ErrTooManyPages  = errors.New("graph: too many pages")
)

// EffectiveMaxPages keeps a page count of 1..MaxPagesLimit and replaces
// anything else (0, negative, too large) with DefaultMaxPages.
func EffectiveMaxPages(raw int) int {
	if raw < 1 || raw > MaxPagesLimit {
		return DefaultMaxPages
	}
	return raw
}

// GroupResolver reads the security-group object ids of the signed-in person
// from Microsoft Graph.
type GroupResolver struct {
	// BaseURL is the Graph origin (GraphBaseURL in production); every
	// request goes to an https URL on its host and port.
	BaseURL string
	// Timeout is the one deadline over every page (GraphTimeout in
	// production).
	Timeout time.Duration
	// Client sends the requests; nil sends nothing and fails with
	// ErrRequest.
	Client *http.Client
}

// memberObjectsPage is one page of getMemberObjects.
type memberObjectsPage struct {
	Value    json.RawMessage `json:"value"`
	NextLink string          `json:"@odata.nextLink"`
}

// MemberObjects posts /v1.0/me/getMemberObjects with securityEnabledOnly
// and the access token of the code exchange, and follows @odata.nextLink.
// One deadline (Timeout) covers every page; at most EffectiveMaxPages(maxPages)
// requests are sent, each to an absolute https URL without user info on the
// host and port of BaseURL; each page is read up to PageMaxBytes and its
// value must be an array of strings. The token is only put into the request
// header; it is never put into an error.
func (r GroupResolver) MemberObjects(ctx context.Context, accessToken string, maxPages int) ([]string, error) {
	if accessToken == "" {
		return nil, ErrNoAccessToken
	}
	if r.Client == nil {
		return nil, ErrRequest
	}
	maxPages = EffectiveMaxPages(maxPages)
	base, err := url.Parse(r.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, ErrNextLink
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	ids := []string{}
	next := strings.TrimRight(r.BaseURL, "/") + memberObjectsPath
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, ErrTooManyPages
		}
		if !sameOrigin(next, base) {
			return nil, ErrNextLink
		}
		values, nextLink, err := memberObjectsRequest(ctx, r.Client, next, accessToken)
		if err != nil {
			return nil, err
		}
		ids = append(ids, values...)
		if nextLink == "" {
			return ids, nil
		}
		next = nextLink
	}
}

// sameOrigin accepts an absolute https URL without user info whose host,
// port included, is base's.
func sameOrigin(raw string, base *url.URL) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return false
	}
	return strings.EqualFold(hostPort(parsed), hostPort(base))
}

// hostPort is the URL's host with its port, the https default made
// explicit, so https://h and https://h:443 compare equal.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func memberObjectsRequest(ctx context.Context, client *http.Client, target, accessToken string) ([]string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(memberObjectsBody))
	if err != nil {
		return nil, "", ErrRequest
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.Do(request)
	if err != nil {
		return nil, "", ErrRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", ErrStatus
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, PageMaxBytes+1))
	if err != nil {
		return nil, "", ErrRequest
	}
	if len(body) > PageMaxBytes {
		return nil, "", ErrTooLarge
	}
	var page memberObjectsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", ErrBody
	}
	values, ok := oidc.StrictStringArray(page.Value)
	if !ok {
		return nil, "", ErrBody
	}
	return values, page.NextLink, nil
}
