package utils

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestIDContext(t *testing.T) {
	assert.Equal(t, "", RequestIDFromContext(context.Background()), "no value is the empty string")
	ctx := WithRequestID(context.Background(), "req-1")
	assert.Equal(t, "req-1", RequestIDFromContext(ctx))
	type otherKey string
	shadow := context.WithValue(context.Background(), otherKey("request_id"), "spoof")
	assert.Equal(t, "", RequestIDFromContext(shadow), "only the package's own key counts")
}

func TestClientNetwork(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{
		{"198.51.100.77", "198.51.100.0/24"},
		{"192.0.2.1", "192.0.2.0/24"},
		{" 10.1.2.3 ", "10.1.2.0/24"},
		{"::ffff:198.51.100.77", "198.51.100.0/24"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1::/48"},
		{"2001:db8:abcd:12::1", "2001:db8:abcd::/48"},
		{"fe80::1%eth0", "fe80::/48"},
		{"::1", "::/48"},
		{"", ""},
		{"not-an-ip", ""},
		{"198.51.100.77:8080", ""},
		{"300.1.1.1", ""},
	} {
		assert.Equalf(t, tc.want, ClientNetwork(tc.ip), "ClientNetwork(%q)", tc.ip)
	}
}

func TestUserAgentFamily(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "Chrome on Windows"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.2903.70", "Edge on Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OPR/115.0.0.0", "Opera on macOS"},
		{"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0", "Firefox on Linux"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7_1) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15", "Safari on macOS"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1", "Safari on iOS"},
		{"Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1", "Safari on iOS"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.81 Mobile Safari/537.36", "Chrome on Android"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "Chrome on ChromeOS"},
		{"curl/8.7.1", "curl on Other"},
		{"python-requests/2.32.3", "Other on Other"},
		{"<script>alert(1)</script>", "Other on Other"},
		{"", ""},
		{"   ", ""},
	} {
		assert.Equalf(t, tc.want, UserAgentFamily(tc.ua), "UserAgentFamily(%q)", tc.ua)
	}

	// Property: whatever the input, the output is "" or one browser of the
	// fixed list on one system of the fixed list, at most 64 bytes.
	browsers := map[string]bool{"Edge": true, "Opera": true, "Firefox": true, "Chrome": true, "Safari": true, "curl": true, "Other": true}
	systems := map[string]bool{"iOS": true, "Android": true, "ChromeOS": true, "Windows": true, "macOS": true, "Linux": true, "Other": true}
	inputs := []string{"x", "Edg/ Windows", "Firefox/1 Android", strings.Repeat("Chrome/ ", 1000), "Safari/ on Linux on Windows",
		"\x00\xff", "Mozilla/5.0 (" + strings.Repeat("A", 5000) + ")", "OPR/ CrOS iPhone", "Other on Other"}
	for i := 0; i < 256; i++ {
		inputs = append(inputs, string([]byte{byte(i), 'C', 'r', 'O', 'S', byte(255 - i)}))
	}
	for _, ua := range inputs {
		got := UserAgentFamily(ua)
		assert.LessOrEqual(t, len(got), 64)
		if strings.TrimSpace(ua) == "" {
			assert.Equal(t, "", got)
			continue
		}
		parts := strings.SplitN(got, " on ", 2)
		if assert.Len(t, parts, 2, got) {
			assert.Truef(t, browsers[parts[0]], "browser %q is from the fixed list", parts[0])
			assert.Truef(t, systems[parts[1]], "system %q is from the fixed list", parts[1])
		}
	}
}

func TestClientInfoContext(t *testing.T) {
	assert.Equal(t, ClientInfo{}, ClientInfoFromContext(context.Background()), "no value is the zero value")
	//nolint:staticcheck // a nil context must not panic
	assert.Equal(t, ClientInfo{}, ClientInfoFromContext(nil))
	ctx := WithClientInfo(WithRequestID(context.Background(), "req-1"), "198.51.100.77", "curl/8.7.1")
	assert.Equal(t, ClientInfo{IP: "198.51.100.77", UserAgent: "curl/8.7.1"}, ClientInfoFromContext(ctx))
	assert.Equal(t, "req-1", RequestIDFromContext(ctx), "the request id survives")
	type otherKey string
	shadow := context.WithValue(context.Background(), otherKey("client_info"), ClientInfo{IP: "spoof"})
	assert.Equal(t, ClientInfo{}, ClientInfoFromContext(shadow), "only the package's own key counts")
}

func TestSafeRequestID(t *testing.T) {
	assert.Equal(t, "req-454", SafeRequestID("req-454"))
	assert.Equal(t, "a.b_c:d-1", SafeRequestID("a.b_c:d-1"))
	assert.Equal(t, strings.Repeat("a", 64), SafeRequestID(strings.Repeat("a", 64)))
	for _, bad := range []string{"", strings.Repeat("a", 65), "has space", "user@example.com", "line\nbreak", "ünï"} {
		assert.Equalf(t, "", SafeRequestID(bad), "SafeRequestID(%q)", bad)
	}
}
