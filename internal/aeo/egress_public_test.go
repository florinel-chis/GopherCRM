package aeo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/config"
)

// #345: (*EgressGuard).PublicHTTPClient, the webhook sender's only outbound
// path. It reuses the dial-time checks but never the AEO trust.

const publicHookHost = "hooks.example.com"

func publicGuard(t *testing.T, fi *fakeInternet, allowed, local string) *EgressGuard {
	t.Helper()
	return fi.guard(t, allowed, local)
}

func postThrough(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"id":"evt_1"}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return resp, err
}

func TestEgressPublicClient(t *testing.T) {
	fi := newFakeInternet(t, publicHookHost, "llm.example.com")
	_, serverPort, err := net.SplitHostPort(fi.server.Listener.Addr().String())
	require.NoError(t, err)
	hookURL := func(host string) string {
		return "https://" + host + ":" + serverPort + "/hook"
	}

	t.Run("a public https URL is reached through a validated literal", func(t *testing.T) {
		fi.reset()
		fi.mu.Lock()
		fi.answers[publicHookHost] = []netip.Addr{publicTestAddr}
		fi.mu.Unlock()
		client, err := publicGuard(t, fi, "", "").PublicHTTPClient(hookURL(publicHookHost))
		require.NoError(t, err)
		resp, err := postThrough(t, client, hookURL(publicHookHost))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		lookups, dials, requests := fi.snapshot()
		assert.Equal(t, []string{publicHookHost}, lookups, "the host is resolved on the connection")
		require.Len(t, dials, 1)
		assert.True(t, strings.HasPrefix(dials[0], publicTestAddr.String()+":"), "only the validated literal is dialled: %s", dials[0])
		require.Len(t, requests, 1)
		assert.Equal(t, "/hook", requests[0].Path)
	})

	t.Run("URLs refused before any network I/O", func(t *testing.T) {
		guard := publicGuard(t, fi, "", "")
		for _, raw := range []string{
			"http://" + publicHookHost + "/hook",
			"https://user:pass@" + publicHookHost + "/hook",
			"https://" + publicHookHost + "/hook?token=1",
			"https://" + publicHookHost + "/hook?",
			"https://" + publicHookHost + "/hook#frag",
			"ftp://" + publicHookHost + "/hook",
			"/relative",
			"",
		} {
			fi.reset()
			client, err := guard.PublicHTTPClient(raw)
			assert.Nil(t, client, raw)
			assert.ErrorIs(t, err, ErrEndpointNotPermitted, raw)
			assertNoTraffic(t, fi)
			assert.ErrorIs(t, CheckPublicURL(raw), ErrEndpointNotPermitted, raw)
		}
		var nilGuard *EgressGuard
		_, err := nilGuard.PublicHTTPClient(hookURL(publicHookHost))
		assert.ErrorIs(t, err, ErrEndpointNotPermitted)
	})

	t.Run("non-public answers refuse the connection without dialling", func(t *testing.T) {
		for _, answer := range [][]string{
			{"127.0.0.1"}, {"::1"}, {"10.0.0.5"}, {"172.16.5.4"}, {"192.168.1.1"}, {"100.64.0.1"},
			{"169.254.169.254"}, {"169.254.1.1"}, {"fe80::1"}, {"fd00:ec2::254"}, {"fc00::1"}, {"fd12::1"},
			{"224.0.0.1"}, {"ff02::1"}, {"0.0.0.0"}, {"::"}, {"::ffff:10.0.0.5"}, {"::ffff:127.0.0.1"},
			{"100.100.100.200"}, {"168.63.129.16"},
			{"93.184.216.34", "10.0.0.5"}, {"10.0.0.5", "93.184.216.34"},
		} {
			fi.reset()
			resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs(answer...)}}
			dialer := &recordingDialer{}
			guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial), WithEgressRootCAs(fi.roots))
			client, err := guard.PublicHTTPClient(hookURL(publicHookHost))
			require.NoError(t, err)
			_, err = postThrough(t, client, hookURL(publicHookHost))
			require.Error(t, err, "%v", answer)
			assert.ErrorIs(t, err, ErrDestinationNotPermitted, "%v", answer)
			assert.Empty(t, dialer.dialed(), "%v is never dialled", answer)
			assert.Equal(t, 1, resolver.lookups())
		}
	})

	t.Run("IPv4-mapped public answers are dialled as IPv4", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs("::ffff:93.184.216.34")}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		conn, err := guard.dialContext(egressRule{AEOEndpoint: config.AEOEndpoint{Scheme: "https", Host: publicHookHost, Port: "443"}})(
			context.Background(), "tcp", publicHookHost+":443")
		require.NoError(t, err)
		_ = conn.Close()
		assert.Equal(t, []string{"93.184.216.34:443"}, dialer.dialed())
	})

	t.Run("IP-literal URLs: public ones pass the URL check, private ones do not", func(t *testing.T) {
		for _, raw := range []string{"https://10.0.0.5/hook", "https://127.0.0.1/hook", "https://[::1]/hook", "https://169.254.169.254/latest",
			"https://[fd00:ec2::254]/x", "https://100.64.0.1/x", "https://0.0.0.0/x"} {
			assert.ErrorIs(t, CheckPublicURL(raw), ErrDestinationNotPermitted, raw)
		}
		assert.NoError(t, CheckPublicURL("https://93.184.216.34/hook"))
		assert.NoError(t, CheckPublicURL("https://hooks.example.com/hook"))

		// A literal is still judged at dial time.
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressDialer(dialer.dial))
		client, err := guard.PublicHTTPClient("https://10.0.0.5/hook")
		require.NoError(t, err)
		_, err = postThrough(t, client, "https://10.0.0.5/hook")
		assert.ErrorIs(t, err, ErrDestinationNotPermitted)
		assert.Empty(t, dialer.dialed())
	})

	t.Run("AEO allowlists and local exceptions never widen it", func(t *testing.T) {
		fi.reset()
		fi.mu.Lock()
		fi.answers["llm.example.com"] = parseAddrs("10.1.2.3")
		fi.mu.Unlock()
		origin := config.AEOEndpoint{Scheme: "https", Host: "llm.example.com", Port: serverPort}
		allowed := config.AEOEgressOrigin{Provider: config.AEOEgressCustom, AEOEndpoint: origin}
		local := config.AEOEgressOrigin{Provider: config.AEOEgressCustom, AEOEndpoint: origin, Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
		policy := config.AEOEgressPolicy{AllowedOrigins: []config.AEOEgressOrigin{allowed}, LocalOrigins: []config.AEOEgressOrigin{local}}
		guard := NewEgressGuard(policy, WithEgressResolver(fi), WithEgressDialer(fi.dial), WithEgressRootCAs(fi.roots))
		client, err := guard.PublicHTTPClient(hookURL("llm.example.com"))
		require.NoError(t, err)
		_, err = postThrough(t, client, hookURL("llm.example.com"))
		assert.ErrorIs(t, err, ErrDestinationNotPermitted, "the local exception does not open 10/8 for a public client")
		_, dials, requests := fi.snapshot()
		assert.Empty(t, dials)
		assert.Empty(t, requests)
		fi.mu.Lock()
		fi.answers["llm.example.com"] = []netip.Addr{publicTestAddr}
		fi.mu.Unlock()
	})

	t.Run("redirects are never followed and ambient proxies are ignored", func(t *testing.T) {
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
		t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
		fi.reset()
		fi.mu.Lock()
		fi.answers[publicHookHost] = []netip.Addr{publicTestAddr}
		fi.mu.Unlock()
		fi.setHandler(func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Path == "/hook" {
				w.Header().Set("Location", "https://"+publicHookHost+"/elsewhere")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		client, err := publicGuard(t, fi, "", "").PublicHTTPClient(hookURL(publicHookHost))
		require.NoError(t, err)
		_, err = postThrough(t, client, hookURL(publicHookHost))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRedirectNotFollowed)
		status, ok := RedirectStatus(err)
		assert.True(t, ok)
		assert.Equal(t, http.StatusFound, status)
		_, dials, requests := fi.snapshot()
		require.Len(t, requests, 1, "the Location target receives no request")
		assert.Equal(t, "/hook", requests[0].Path)
		require.Len(t, dials, 1, "the proxy variables are ignored: the validated literal is dialled directly")
		assert.True(t, strings.HasPrefix(dials[0], publicTestAddr.String()+":"))
		_, ok = RedirectStatus(errors.New("other"))
		assert.False(t, ok)
	})

	t.Run("each connection resolves the host again", func(t *testing.T) {
		resolver := &scriptedResolver{answers: [][]netip.Addr{parseAddrs("93.184.216.34"), parseAddrs("127.0.0.1")}}
		dialer := &recordingDialer{}
		guard := NewEgressGuard(config.AEOEgressPolicy{}, WithEgressResolver(resolver), WithEgressDialer(dialer.dial))
		dial := guard.dialContext(egressRule{AEOEndpoint: config.AEOEndpoint{Scheme: "https", Host: publicHookHost, Port: "443"}})
		conn, err := dial(context.Background(), "tcp", publicHookHost+":443")
		require.NoError(t, err)
		_ = conn.Close()
		_, err = dial(context.Background(), "tcp", publicHookHost+":443")
		assert.ErrorIs(t, err, ErrDestinationNotPermitted, "a rebinding second answer is refused")
		assert.Equal(t, 2, resolver.lookups())
		assert.Equal(t, []string{"93.184.216.34:443"}, dialer.dialed())
	})
}
