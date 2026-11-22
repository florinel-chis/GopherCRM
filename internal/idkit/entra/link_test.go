package entra

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntraLinkHint(t *testing.T) {
	const (
		issuer = "https://login.microsoftonline.com/" + testTenant + "/v2.0"
		oid    = "0b9f6c2e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	)
	cases := []struct {
		name                  string
		issuer, host, tid, id string
		want                  string
	}{
		{"match", issuer, IssuerHost, testTenant, oid, oid},
		{"other_tenant", issuer, IssuerHost, "11111111-2222-4333-8444-555555555555", oid, ""},
		{"tenant_other_case", "https://login.microsoftonline.com/6F0C3A1E-0505-4D2B-9A11-000000000505/v2.0", IssuerHost, testTenant, oid, ""},
		{"empty_tid", issuer, IssuerHost, "", oid, ""},
		{"empty_oid", issuer, IssuerHost, testTenant, "", ""},
		{"oid_leading_space", issuer, IssuerHost, testTenant, " " + oid, ""},
		{"non_entra_host", "https://idp.example.test/" + testTenant + "/v2.0", IssuerHost, testTenant, oid, ""},
		{"empty_host", issuer, "", testTenant, oid, ""},
		{"issuer_without_tenant", "https://login.microsoftonline.com", IssuerHost, testTenant, oid, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LinkHint(tc.issuer, tc.host, tc.tid, tc.id))
		})
	}
}
