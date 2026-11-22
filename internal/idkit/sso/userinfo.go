package sso

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	idkitoidc "github.com/florinel-chis/gophercrm/internal/idkit/oidc"
)

// Failures of the userinfo group resolution. Like the Graph errors
// (internal/idkit/entra) they never carry a URL, a token, a status text or a
// response body.
var (
	ErrUserInfoNoAccessToken = errors.New("userinfo: no access token")
	ErrUserInfoEndpoint      = errors.New("userinfo: endpoint refused")
	ErrUserInfoRequest       = errors.New("userinfo: request failed")
	ErrUserInfoStatus        = errors.New("userinfo: unexpected status")
	ErrUserInfoContentType   = errors.New("userinfo: not a JSON response")
	ErrUserInfoTooLarge      = errors.New("userinfo: response too large")
	ErrUserInfoBody          = errors.New("userinfo: malformed response")
	ErrUserInfoSubject       = errors.New("userinfo: subject mismatch")
)

// UserInfoResolver reads the groups of an ID token without a groups claim
// from the issuer's userinfo endpoint.
type UserInfoResolver struct {
	// Timeout is the one deadline of the request.
	Timeout time.Duration
	// Client sends the request (the issuer client: no redirects, no proxy).
	Client *http.Client
}

// Groups reads the groups member of the issuer's userinfo response with the
// access token of the code exchange: one GET to the discovered
// userinfo_endpoint (an absolute https URL without credentials), under one
// deadline, with a body of at most UserInfoMaxBytes of application/json
// (signed application/jwt userinfo is not supported). The response's sub
// must equal the ID token's subject byte for byte; only groups is read, an
// absent groups member is an empty list. provider.UserInfo is not used: it
// reads the body without a bound and puts it into its errors. The token is
// only put into the request header; it is never logged, stored or put into
// an error.
func (r UserInfoResolver) Groups(ctx context.Context, provider *gooidc.Provider, accessToken, subject string) ([]string, error) {
	if accessToken == "" {
		return nil, ErrUserInfoNoAccessToken
	}
	endpoint := provider.UserInfoEndpoint()
	parsed, err := url.Parse(endpoint)
	if endpoint == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, ErrUserInfoEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrUserInfoRequest
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	client := r.Client
	if client == nil {
		client = idkitoidc.NewHTTPClient(nil)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrUserInfoRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUserInfoStatus
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, ErrUserInfoContentType
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, UserInfoMaxBytes+1))
	if err != nil {
		return nil, ErrUserInfoRequest
	}
	if len(body) > UserInfoMaxBytes {
		return nil, ErrUserInfoTooLarge
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		return nil, ErrUserInfoBody
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, ErrUserInfoBody
	}
	rawSub, found := members["sub"]
	var sub string
	if !found || !strings.HasPrefix(strings.TrimSpace(string(rawSub)), `"`) || json.Unmarshal(rawSub, &sub) != nil || sub != subject {
		return nil, ErrUserInfoSubject
	}
	rawGroups, found := members["groups"]
	if !found {
		return []string{}, nil
	}
	groups, ok := idkitoidc.StrictStringArray(rawGroups)
	if !ok {
		return nil, ErrUserInfoBody
	}
	return groups, nil
}
