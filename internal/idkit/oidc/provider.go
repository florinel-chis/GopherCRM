package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// ProviderCacheTTL is how long a discovered issuer is kept.
const ProviderCacheTTL = 24 * time.Hour

// ProviderKey identifies a cached discovery: the configured issuer URL,
// whether the application signing keys are used and, only then, the client
// id they are read for.
type ProviderKey struct {
	Issuer   string
	AppKeys  bool
	ClientID string
}

// Discovery is one discovered issuer: the go-oidc provider and, in app-key
// mode, the key set of jwks_uri?appid=<client id> with the signing
// algorithms the discovery document allows.
type Discovery struct {
	Key       ProviderKey
	Provider  *gooidc.Provider
	appKeySet gooidc.KeySet
	appAlgs   []string
	fetchedAt time.Time
}

// signingAlgs are the ID token algorithms go-oidc verifies; a discovery
// document's list is filtered to them the way go-oidc filters it for
// provider.Verifier.
var signingAlgs = map[string]bool{
	gooidc.RS256: true, gooidc.RS384: true, gooidc.RS512: true,
	gooidc.ES256: true, gooidc.ES384: true, gooidc.ES512: true,
	gooidc.PS256: true, gooidc.PS384: true, gooidc.PS512: true,
	gooidc.EdDSA: true,
}

// Verifier checks ID tokens for the current client id against the entry's
// keys: the provider's own key set, or the application signing keys in
// app-key mode. It is built per call, never cached.
func (d *Discovery) Verifier(clientID string) *gooidc.IDTokenVerifier {
	if d.appKeySet == nil {
		return d.Provider.Verifier(&gooidc.Config{ClientID: clientID})
	}
	return gooidc.NewVerifier(d.Key.Issuer, d.appKeySet, &gooidc.Config{ClientID: clientID, SupportedSigningAlgs: d.appAlgs})
}

// ProviderCache holds at most one discovered issuer. mu is held while a miss
// fetches, so concurrent misses wait for one discovery.
type ProviderCache struct {
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	entry *Discovery
}

// NewProviderCache is an empty cache that discovers through client and
// reads the time from now.
func NewProviderCache(client *http.Client, now func() time.Time) *ProviderCache {
	return &ProviderCache{client: client, now: now}
}

// Get returns the discovery of key. The discovery document and the signing
// keys are kept for ProviderCacheTTL per key; a different key replaces the
// entry, and go-oidc fetches the keys again when a token names an unknown key
// id. A failed discovery (unreachable, redirect, timeout, an issuer mismatch)
// is not kept, leaves the cached entry as it was and is returned.
func (c *ProviderCache) Get(ctx context.Context, key ProviderKey) (*Discovery, error) {
	ctx = gooidc.ClientContext(ctx, c.client)
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entry; entry != nil && entry.Key == key && c.now().Before(entry.fetchedAt.Add(ProviderCacheTTL)) {
		return entry, nil
	}
	entry, err := c.fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	c.entry = entry
	return entry, nil
}

// Clear drops the cached entry.
func (c *ProviderCache) Clear() {
	c.mu.Lock()
	c.entry = nil
	c.mu.Unlock()
}

// Cached is the cached entry, nil when there is none.
func (c *ProviderCache) Cached() *Discovery {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entry
}

// fetch runs the discovery of key.Issuer and, in app-key mode, prepares the
// key set of jwks_uri?appid=<client id>.
func (c *ProviderCache) fetch(ctx context.Context, key ProviderKey) (*Discovery, error) {
	provider, err := gooidc.NewProvider(ctx, key.Issuer)
	if err != nil {
		return nil, err
	}
	entry := &Discovery{Key: key, Provider: provider, fetchedAt: c.now()}
	if !key.AppKeys {
		return entry, nil
	}
	var document struct {
		JWKSURI    string   `json:"jwks_uri"`
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := provider.Claims(&document); err != nil {
		return nil, err
	}
	jwksURL, err := url.Parse(document.JWKSURI)
	if err != nil || document.JWKSURI == "" {
		return nil, errors.New("the discovery document has no usable jwks_uri")
	}
	query := jwksURL.Query()
	query.Set("appid", key.ClientID)
	jwksURL.RawQuery = query.Encode()
	entry.appKeySet = gooidc.NewRemoteKeySet(gooidc.ClientContext(context.Background(), c.client), jwksURL.String())
	for _, alg := range document.Algorithms {
		if signingAlgs[alg] {
			entry.appAlgs = append(entry.appAlgs, alg)
		}
	}
	return entry, nil
}
