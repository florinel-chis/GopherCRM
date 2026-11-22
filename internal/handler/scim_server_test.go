package handler

import (
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSCIMRoutesMatchServer: the gin routes SetupSCIMRoutes registers under
// /scim/v2 are exactly server.Routes(), with {id} written as :id, so a route
// dropped from either side fails here.
func TestSCIMRoutesMatchServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	setupSCIMTestRoutes(router.Group(scim.BasePath), &stubSCIMUsers{}, &stubSCIMGroups{}, "https://crm.example.test")

	var registered []string
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, scim.BasePath+"/") {
			registered = append(registered, route.Method+" "+route.Path)
		}
	}
	var expected []string
	for _, route := range server.Routes() {
		expected = append(expected, route.Method+" "+scim.BasePath+strings.ReplaceAll(route.Path, "{id}", ":id"))
	}
	require.Len(t, expected, 14)
	sort.Strings(registered)
	sort.Strings(expected)
	assert.Equal(t, expected, registered)
}
