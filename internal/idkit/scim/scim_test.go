package scim

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusUnauthorized, `Authentication "failed"`)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, ContentType, w.Result().Header.Get("Content-Type"))
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication \"failed\""}`, w.Body.String())
}

func TestDiscoveryLocations(t *testing.T) {
	for _, base := range []string{"https://crm.example.test", "https://crm.example.test/", "https://crm.example.test//"} {
		assert.Equal(t, "https://crm.example.test/scim/v2/ServiceProviderConfig", NewServiceProviderConfig(base).Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/ResourceTypes/User", NewResourceTypes(base).Resources[0].Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/Schemas/"+UserSchema, NewSchemas(base).Resources[0].Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/ResourceTypes/Group", NewResourceTypes(base).Resources[1].Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/Schemas/"+GroupSchema, NewSchemas(base).Resources[1].Meta.Location, base)
	}
	assert.Equal(t, MaxResults, NewServiceProviderConfig("").Filter.MaxResults)
	assert.Equal(t, 2, NewSchemas("").TotalResults, "User and Group")
	assert.Equal(t, 2, NewResourceTypes("").TotalResults, "User and Group")
	assert.Equal(t, "/Groups", NewResourceTypes("").Resources[1].Endpoint)
	assert.Equal(t, GroupSchema, NewResourceTypes("").Resources[1].Schema)
	assert.Equal(t, 1, NewResourceTypes("").StartIndex)
}
