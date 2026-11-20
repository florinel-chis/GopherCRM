package scim

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestWriteError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	WriteError(c, http.StatusUnauthorized, `Authentication "failed"`)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, ContentType, w.Header().Get("Content-Type"))
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401","detail":"Authentication \"failed\""}`, w.Body.String())
}

func TestDiscoveryLocations(t *testing.T) {
	for _, base := range []string{"https://crm.example.test", "https://crm.example.test/", "https://crm.example.test//"} {
		assert.Equal(t, "https://crm.example.test/scim/v2/ServiceProviderConfig", NewServiceProviderConfig(base).Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/ResourceTypes/User", NewResourceTypes(base).Resources[0].Meta.Location, base)
		assert.Equal(t, "https://crm.example.test/scim/v2/Schemas/"+UserSchema, NewSchemas(base).Resources[0].Meta.Location, base)
	}
	assert.Equal(t, MaxResults, NewServiceProviderConfig("").Filter.MaxResults)
	assert.Equal(t, 1, NewSchemas("").TotalResults)
	assert.Equal(t, 1, NewResourceTypes("").StartIndex)
}
