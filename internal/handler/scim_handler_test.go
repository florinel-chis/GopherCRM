package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC 7643 §7: the characteristics every attribute definition carries and
// the values each may take.
var (
	scimAttributeTypes = map[string]bool{"string": true, "boolean": true, "decimal": true, "integer": true,
		"dateTime": true, "binary": true, "reference": true, "complex": true}
	scimMutability = map[string]bool{"readOnly": true, "readWrite": true, "immutable": true, "writeOnly": true}
	scimReturned   = map[string]bool{"always": true, "never": true, "default": true, "request": true}
	scimUniqueness = map[string]bool{"none": true, "server": true, "global": true}
)

func getSCIMDocument(t *testing.T, router *gin.Engine, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, strings.HasPrefix(w.Header().Get("Content-Type"), "application/scim+json"), w.Header().Get("Content-Type"))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
	return doc
}

// assertSCIMAttribute checks one attribute definition and its
// sub-attributes, and returns the sub-attribute names.
func assertSCIMAttribute(t *testing.T, attr map[string]any, path string) []string {
	t.Helper()
	for _, key := range []string{"name", "type", "multiValued", "description", "required", "mutability", "returned", "uniqueness"} {
		assert.Contains(t, attr, key, "%s has %s", path, key)
	}
	typ, _ := attr["type"].(string)
	assert.True(t, scimAttributeTypes[typ], "%s type %q", path, typ)
	assert.IsType(t, true, attr["multiValued"], path)
	assert.IsType(t, true, attr["required"], path)
	assert.NotEmpty(t, attr["description"], path)
	assert.True(t, scimMutability[attr["mutability"].(string)], "%s mutability", path)
	assert.True(t, scimReturned[attr["returned"].(string)], "%s returned", path)
	assert.True(t, scimUniqueness[attr["uniqueness"].(string)], "%s uniqueness", path)
	if typ == "string" {
		assert.IsType(t, true, attr["caseExact"], "%s caseExact", path)
	} else {
		assert.NotContains(t, attr, "caseExact", "%s: caseExact only on strings", path)
	}
	var names []string
	if typ == "complex" {
		subs, ok := attr["subAttributes"].([]any)
		require.True(t, ok, "%s has subAttributes", path)
		for _, raw := range subs {
			sub := raw.(map[string]any)
			assert.NotEqual(t, "complex", sub["type"], "%s: no complex sub-attribute", path)
			assertSCIMAttribute(t, sub, path+"."+sub["name"].(string))
			names = append(names, sub["name"].(string))
		}
	} else {
		assert.NotContains(t, attr, "subAttributes", "%s: subAttributes only on complex", path)
	}
	sort.Strings(names)
	return names
}

// assertSCIMListResponse checks a discovery list of two resources (User,
// then Group since #506) and returns them in order.
func assertSCIMListResponse(t *testing.T, doc map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	assert.Equal(t, []any{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, doc["schemas"])
	assert.EqualValues(t, 2, doc["totalResults"])
	assert.EqualValues(t, 2, doc["itemsPerPage"])
	assert.EqualValues(t, 1, doc["startIndex"])
	resources, ok := doc["Resources"].([]any)
	require.True(t, ok, "Resources")
	require.Len(t, resources, 2)
	return resources[0].(map[string]any), resources[1].(map[string]any)
}

func TestSCIMDiscoveryDocuments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const base = "https://crm.example.test"
	router := gin.New()
	SetupSCIMRoutes(router.Group("/scim/v2"), NewSCIMHandler(base+"/"), NewSCIMUserHandler(nil, base))

	t.Run("ServiceProviderConfig", func(t *testing.T) {
		doc := getSCIMDocument(t, router, "/scim/v2/ServiceProviderConfig?attributes=patch")
		assert.Equal(t, []any{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"}, doc["schemas"])
		assert.Equal(t, map[string]any{"supported": true}, doc["patch"])
		assert.Equal(t, map[string]any{"supported": false, "maxOperations": float64(0), "maxPayloadSize": float64(0)}, doc["bulk"])
		assert.Equal(t, map[string]any{"supported": true, "maxResults": float64(100)}, doc["filter"])
		for _, feature := range []string{"changePassword", "sort", "etag"} {
			assert.Equal(t, map[string]any{"supported": false}, doc[feature], feature)
		}
		schemes, ok := doc["authenticationSchemes"].([]any)
		require.True(t, ok)
		require.Len(t, schemes, 1)
		scheme := schemes[0].(map[string]any)
		assert.Equal(t, "oauthbearertoken", scheme["type"])
		assert.NotEmpty(t, scheme["name"])
		assert.NotEmpty(t, scheme["description"])
		assert.Equal(t, true, scheme["primary"])
		assert.Equal(t, map[string]any{"resourceType": "ServiceProviderConfig", "location": base + "/scim/v2/ServiceProviderConfig"}, doc["meta"])
	})

	t.Run("Schemas", func(t *testing.T) {
		schema, groupSchema := assertSCIMListResponse(t, getSCIMDocument(t, router, "/scim/v2/Schemas?filter=x"))
		assert.Equal(t, []any{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, schema["schemas"])
		assert.Equal(t, "urn:ietf:params:scim:schemas:core:2.0:User", schema["id"])
		assert.Equal(t, "User", schema["name"])
		assert.NotEmpty(t, schema["description"])
		meta := schema["meta"].(map[string]any)
		assert.Equal(t, "Schema", meta["resourceType"])
		assert.Equal(t, base+"/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:User", meta["location"])

		attributes := map[string]map[string]any{}
		for _, raw := range schema["attributes"].([]any) {
			attr := raw.(map[string]any)
			attributes[attr["name"].(string)] = attr
		}
		require.Len(t, attributes, 4)
		subs := map[string][]string{}
		for name, attr := range attributes {
			subs[name] = assertSCIMAttribute(t, attr, name)
		}

		userName := attributes["userName"]
		assert.Equal(t, "string", userName["type"])
		assert.Equal(t, true, userName["required"])
		assert.Equal(t, false, userName["caseExact"])
		assert.Equal(t, "readWrite", userName["mutability"])
		assert.Equal(t, "default", userName["returned"])
		assert.Equal(t, "server", userName["uniqueness"])
		assert.Equal(t, false, userName["multiValued"])

		assert.Equal(t, "complex", attributes["name"]["type"])
		assert.Equal(t, []string{"familyName", "givenName"}, subs["name"])

		emails := attributes["emails"]
		assert.Equal(t, "complex", emails["type"])
		assert.Equal(t, true, emails["multiValued"])
		assert.Equal(t, []string{"primary", "type", "value"}, subs["emails"])
		for _, raw := range emails["subAttributes"].([]any) {
			sub := raw.(map[string]any)
			switch sub["name"] {
			case "type":
				assert.Equal(t, []any{"work", "home", "other"}, sub["canonicalValues"])
			case "primary":
				assert.Equal(t, "boolean", sub["type"])
			case "value":
				assert.Equal(t, "string", sub["type"])
			}
		}

		assert.Equal(t, "boolean", attributes["active"]["type"])
		assert.Equal(t, false, attributes["active"]["multiValued"])

		// The core Group schema (#506).
		assert.Equal(t, []any{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, groupSchema["schemas"])
		assert.Equal(t, "urn:ietf:params:scim:schemas:core:2.0:Group", groupSchema["id"])
		assert.Equal(t, "Group", groupSchema["name"])
		assert.NotEmpty(t, groupSchema["description"])
		assert.Equal(t, map[string]any{"resourceType": "Schema", "location": base + "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group"}, groupSchema["meta"])
		groupAttributes := map[string]map[string]any{}
		for _, raw := range groupSchema["attributes"].([]any) {
			attr := raw.(map[string]any)
			groupAttributes[attr["name"].(string)] = attr
		}
		require.Len(t, groupAttributes, 2)
		groupSubs := map[string][]string{}
		for name, attr := range groupAttributes {
			groupSubs[name] = assertSCIMAttribute(t, attr, "Group."+name)
		}
		displayName := groupAttributes["displayName"]
		assert.Equal(t, "string", displayName["type"])
		assert.Equal(t, false, displayName["multiValued"])
		assert.Equal(t, true, displayName["required"])
		assert.Equal(t, true, displayName["caseExact"])
		assert.Equal(t, "readOnly", displayName["mutability"])
		assert.Equal(t, "default", displayName["returned"])
		assert.Equal(t, "server", displayName["uniqueness"])
		members := groupAttributes["members"]
		assert.Equal(t, "complex", members["type"])
		assert.Equal(t, true, members["multiValued"])
		assert.Equal(t, false, members["required"])
		assert.Equal(t, "readWrite", members["mutability"])
		assert.Equal(t, "default", members["returned"])
		assert.Equal(t, "none", members["uniqueness"])
		assert.Equal(t, []string{"value"}, groupSubs["members"])
		value := members["subAttributes"].([]any)[0].(map[string]any)
		assert.Equal(t, "string", value["type"])
		assert.Equal(t, true, value["caseExact"])
		assert.Equal(t, "immutable", value["mutability"])
	})

	t.Run("ResourceTypes", func(t *testing.T) {
		resourceType, groupType := assertSCIMListResponse(t, getSCIMDocument(t, router, "/scim/v2/ResourceTypes?count=0"))
		assert.Equal(t, []any{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, resourceType["schemas"])
		assert.Equal(t, "User", resourceType["id"])
		assert.Equal(t, "User", resourceType["name"])
		assert.Equal(t, "/Users", resourceType["endpoint"])
		assert.Equal(t, "urn:ietf:params:scim:schemas:core:2.0:User", resourceType["schema"])
		assert.Equal(t, map[string]any{"resourceType": "ResourceType", "location": base + "/scim/v2/ResourceTypes/User"}, resourceType["meta"])

		assert.Equal(t, []any{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, groupType["schemas"])
		assert.Equal(t, "Group", groupType["id"])
		assert.Equal(t, "Group", groupType["name"])
		assert.NotEmpty(t, groupType["description"])
		assert.Equal(t, "/Groups", groupType["endpoint"])
		assert.Equal(t, "urn:ietf:params:scim:schemas:core:2.0:Group", groupType["schema"])
		assert.Equal(t, map[string]any{"resourceType": "ResourceType", "location": base + "/scim/v2/ResourceTypes/Group"}, groupType["meta"])
	})
}
