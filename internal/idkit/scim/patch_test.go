package scim

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// Microsoft's documented PATCH bodies (Entra ID app provisioning:
// "customize-application-attributes" and
// "application-provisioning-config-problem-scim-compatibility").
const (
	msSingleAppRoleAssignment            = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"roles","value":[{"value":"{\"id\":\"06b07648-ecfe-589f-9d2f-6325724a46ee\",\"value\":\"25\",\"displayName\":\"Role1234\"}"}]}]}`
	msAppRoleAssignmentsComplex          = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"roles","value":[{"value":"{\"id\":\"06b07648-ecfe-589f-9d2f-6325724a46ee\",\"value\":\"Admin\",\"displayName\":\"Admin\"}"},{"value":"{\"id\":\"06b07648-ecfe-599f-9d2f-6325724a46ee\",\"value\":\"User\",\"displayName\":\"User\"}"}]}]}`
	msAssertiveAppRoleAssignmentsComplex = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"roles","value":[{"primary":false,"type":"WindowsAzureActiveDirectoryRole","display":"User","value":"User"},{"primary":false,"type":"WindowsAzureActiveDirectoryRole","display":"Test","value":"Test"}]}]}`
	msReplaceMultipleWithoutFlag         = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"displayName","value":"Pvlo"},{"op":"Replace","path":"emails[type eq \"work\"].value","value":"TestBcwqnm@test.microsoft.com"},{"op":"Replace","path":"name.givenName","value":"Gtfd"},{"op":"Replace","path":"name.familyName","value":"Pkqf"},{"op":"Replace","path":"externalId","value":"Eqpj"},{"op":"Replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:employeeNumber","value":"Eqpj"}]}`
	msReplaceMultipleWithFlag            = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"emails[type eq \"work\"].value","value":"TestMhvaes@test.microsoft.com"},{"op":"replace","value":{"displayName":"Bjfe","name.givenName":"Kkom","name.familyName":"Unua","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:employeeNumber":"Aklq"}}]}`
	msAddNickNameWithoutFlag             = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"nickName","value":"Babs"}]}`
	msAddNickNameWithFlag                = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"nickName","value":"Babs"}]}`
)

func TestParsePatch(t *testing.T) {
	yes, no := true, false
	accepted := []struct {
		name string
		body string
		want UserPatch
	}{
		{"entra active string False", `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
			UserPatch{Active: &no}},
		{"active string TRUE", `{"Operations":[{"op":"replace","path":"active","value":"TRUE"}]}`, UserPatch{Active: &yes}},
		{"active boolean", `{"Operations":[{"op":"add","path":"ACTIVE","value":false}]}`, UserPatch{Active: &no}},
		{"okta path-less", `{"Operations":[{"op":"replace","value":{"active":true}}]}`, UserPatch{Active: &yes}},
		{"entra value object", `{"Operations":[{"op":"Add","value":{"name.givenName":"Ann","name.familyName":"Lee"}}]}`,
			UserPatch{GivenName: strPtr("Ann"), FamilyName: strPtr("Lee")}},
		{"dotted keys after the name object", `{"Operations":[{"op":"replace","value":{"name.givenName":"Late","name":{"givenName":"Early","familyName":"Fam","formatted":"ignored"}}}]}`,
			UserPatch{GivenName: strPtr("Late"), FamilyName: strPtr("Fam")}},
		{"name object keeps absent parts", `{"Operations":[{"op":"replace","path":"name","value":{"familyName":"Only"}}]}`,
			UserPatch{FamilyName: strPtr("Only")}},
		{"userName", `{"Operations":[{"op":"replace","path":"userName","value":"a@x.test"}]}`, UserPatch{UserName: strPtr("a@x.test")}},
		{"work email filter", `{"Operations":[{"op":"replace","path":"emails[TYPE EQ \"WORK\"].value","value":"w@x.test"}]}`,
			UserPatch{Email: strPtr("w@x.test")}},
		{"emails primary wins", `{"Operations":[{"op":"replace","path":"emails","value":[{"value":"w@x.test","type":"work"},{"value":"p@x.test","type":"home","primary":true}]}]}`,
			UserPatch{Email: strPtr("p@x.test")}},
		{"emails work wins", `{"Operations":[{"op":"replace","path":"emails","value":[{"value":"h@x.test","type":"home"},{"value":"w@x.test","type":"Work","display":"x"}]}]}`,
			UserPatch{Email: strPtr("w@x.test")}},
		{"emails sole entry", `{"Operations":[{"op":"add","value":{"emails":[{"value":"s@x.test"}]}}]}`, UserPatch{Email: strPtr("s@x.test")}},
		{"userName wins over emails", `{"Operations":[{"op":"replace","path":"userName","value":"u@x.test"},{"op":"replace","path":"emails","value":[{"value":"e@x.test"}]}]}`,
			UserPatch{UserName: strPtr("u@x.test")}},
		{"roles", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"CRM.Sales","primary":true},{"value":"Other"}]}]}`,
			UserPatch{Roles: &[]string{"CRM.Sales", "Other"}}},
		{"empty roles", `{"Operations":[{"op":"replace","path":"roles","value":[]}]}`, UserPatch{Roles: &[]string{}}},
		{"remove name", `{"Operations":[{"op":"remove","path":"name"}]}`, UserPatch{GivenName: strPtr(""), FamilyName: strPtr("")}},
		{"remove givenName", `{"Operations":[{"op":"Remove","path":"name.givenName","value":"ignored"}]}`, UserPatch{GivenName: strPtr("")}},
		{"Microsoft SingleAppRoleAssignment role as JSON text", msSingleAppRoleAssignment, UserPatch{Roles: &[]string{"25"}}},
		{"Microsoft AppRoleAssignmentsComplex roles as JSON text", msAppRoleAssignmentsComplex, UserPatch{Roles: &[]string{"Admin", "User"}}},
		{"Microsoft AssertiveAppRoleAssignmentsComplex plain roles", msAssertiveAppRoleAssignmentsComplex, UserPatch{Roles: &[]string{"User", "Test"}}},
		{"Microsoft replace multiple attributes without the flag", msReplaceMultipleWithoutFlag,
			UserPatch{Email: strPtr("TestBcwqnm@test.microsoft.com"), GivenName: strPtr("Gtfd"), FamilyName: strPtr("Pkqf")}},
		{"Microsoft replace multiple attributes with the flag", msReplaceMultipleWithFlag,
			UserPatch{Email: strPtr("TestMhvaes@test.microsoft.com"), GivenName: strPtr("Kkom"), FamilyName: strPtr("Unua")}},
		{"Microsoft nickName add without the flag", msAddNickNameWithoutFlag, UserPatch{}},
		{"Microsoft nickName add with the flag", msAddNickNameWithFlag, UserPatch{}},
		{"role text that is not JSON as sent", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"{not json"}]}]}`,
			UserPatch{Roles: &[]string{"{not json"}}},
		{"role text without an inner value as sent", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"{\"id\":\"x\"}"}]}]}`,
			UserPatch{Roles: &[]string{`{"id":"x"}`}}},
		{"role text with a duplicated inner value as sent", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"{\"value\":\"a\",\"value\":\"b\"}"}]}]}`,
			UserPatch{Roles: &[]string{`{"value":"a","value":"b"}`}}},
		{"role text with a numeric inner value as sent", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"{\"value\":5}"}]}]}`,
			UserPatch{Roles: &[]string{`{"value":5}`}}},
		{"role text with trailing data as sent", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"{\"value\":\"a\"} x"}]}]}`,
			UserPatch{Roles: &[]string{`{"value":"a"} x`}}},
		{"path-less role as JSON text", `{"Operations":[{"op":"add","value":{"roles":[{"value":"{\"VALUE\":\"Admin\"}"}]}}]}`,
			UserPatch{Roles: &[]string{"Admin"}}},
		{"unstored externalId", `{"Operations":[{"op":"replace","path":"externalId","value":"x"}]}`, UserPatch{}},
		{"unstored displayName", `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`, UserPatch{}},
		{"unstored name.formatted", `{"Operations":[{"op":"replace","path":"name.formatted","value":"x"}]}`, UserPatch{}},
		{"unstored title", `{"Operations":[{"op":"replace","path":"title","value":"Engineer"}]}`, UserPatch{}},
		{"unstored title with a non-string value", `{"Operations":[{"op":"add","path":"TITLE","value":{"x":[1]}}]}`, UserPatch{}},
		{"unstored work phone", `{"Operations":[{"op":"replace","path":"phoneNumbers[type eq \"work\"].value","value":"+1 555"}]}`, UserPatch{}},
		{"unstored upper-case phone path", `{"Operations":[{"op":"replace","path":"PHONENUMBERS[type eq \"work\"].value","value":"+1 555"}]}`, UserPatch{}},
		{"unstored phoneNumbers list", `{"Operations":[{"op":"add","path":"phoneNumbers","value":[{"value":"+1 555","type":"work"}]}]}`, UserPatch{}},
		{"unstored work postal code", `{"Operations":[{"op":"replace","path":"addresses[type eq \"work\"].postalCode","value":"12345"}]}`, UserPatch{}},
		{"unstored enterprise attribute", `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department","value":"x"}]}`, UserPatch{}},
		{"unstored enterprise URN as a path-less key", `{"Operations":[{"op":"replace","value":{"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User":{"employeeNumber":"1","manager":{"value":"m"}}}}]}`, UserPatch{}},
		{"path-less unstored keys next to stored ones", `{"Operations":[{"op":"add","value":{"active":true,"externalId":"x","nickName":"n","addresses.locality":"y"}}]}`,
			UserPatch{Active: &yes}},
		{"remove unstored title", `{"Operations":[{"op":"remove","path":"title"}]}`, UserPatch{}},
		{"remove unstored externalId", `{"Operations":[{"op":"remove","path":"externalId"}]}`, UserPatch{}},
		{"last write wins", `{"Operations":[{"op":"replace","path":"active","value":false},{"op":"replace","value":{"active":"true"}}]}`,
			UserPatch{Active: &yes}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			got, err := ParseUserPatch([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	refused := []struct {
		name, body, scimType string
	}{
		{"not an object", `[]`, ScimTypeInvalidSyntax},
		{"no Operations", `{"schemas":[]}`, ScimTypeInvalidSyntax},
		{"empty Operations", `{"Operations":[]}`, ScimTypeInvalidSyntax},
		{"Operations not an array", `{"Operations":{}}`, ScimTypeInvalidSyntax},
		{"operation not an object", `{"Operations":["replace"]}`, ScimTypeInvalidSyntax},
		{"no op", `{"Operations":[{"path":"active","value":true}]}`, ScimTypeInvalidSyntax},
		{"op not a string", `{"Operations":[{"op":1,"path":"active","value":true}]}`, ScimTypeInvalidSyntax},
		{"unknown op", `{"Operations":[{"op":"move","path":"active","value":true}]}`, ScimTypeInvalidSyntax},
		{"path not a string", `{"Operations":[{"op":"replace","path":1,"value":true}]}`, ScimTypeInvalidSyntax},
		{"path-less value not an object", `{"Operations":[{"op":"replace","value":true}]}`, ScimTypeInvalidSyntax},
		{"password", `{"Operations":[{"op":"replace","path":"password","value":"x"}]}`, ScimTypeInvalidPath},
		{"groups", `{"Operations":[{"op":"add","path":"groups","value":[{"value":"g"}]}]}`, ScimTypeInvalidPath},
		{"id", `{"Operations":[{"op":"replace","path":"id","value":"x"}]}`, ScimTypeInvalidPath},
		{"meta", `{"Operations":[{"op":"replace","path":"meta","value":{}}]}`, ScimTypeInvalidPath},
		{"path-less password next to active", `{"Operations":[{"op":"replace","value":{"active":true,"password":"x"}}]}`, ScimTypeInvalidPath},
		{"path-less unknown key next to unstored ones", `{"Operations":[{"op":"add","value":{"externalId":"x","displayName":"d","password":"x"}}]}`, ScimTypeInvalidPath},
		{"name prefix without a separator", `{"Operations":[{"op":"replace","path":"phoneNumbersX","value":"x"}]}`, ScimTypeInvalidPath},
		{"addresses prefix without a separator", `{"Operations":[{"op":"replace","path":"addressesfoo","value":"x"}]}`, ScimTypeInvalidPath},
		{"enterprise URN prefix without a colon", `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:UserX","value":"x"}]}`, ScimTypeInvalidPath},
		{"path-less name prefix without a separator", `{"Operations":[{"op":"replace","value":{"phoneNumbersX":"x"}}]}`, ScimTypeInvalidPath},
		{"path-less addresses prefix without a separator", `{"Operations":[{"op":"replace","value":{"addressesfoo":"x"}}]}`, ScimTypeInvalidPath},
		{"path-less enterprise URN prefix without a colon", `{"Operations":[{"op":"replace","value":{"urn:ietf:params:scim:schemas:extension:enterprise:2.0:UserX":{}}}]}`, ScimTypeInvalidPath},
		{"padded unstored path", `{"Operations":[{"op":"replace","path":"title ","value":"x"}]}`, ScimTypeInvalidPath},
		{"remove of an unknown path", `{"Operations":[{"op":"remove","path":"password"}]}`, ScimTypeInvalidPath},
		{"other filter", `{"Operations":[{"op":"replace","path":"emails[type eq \"home\"].value","value":"x"}]}`, ScimTypeInvalidPath},
		{"URN prefix", `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:core:2.0:User:active","value":true}]}`, ScimTypeInvalidPath},
		{"padded path", `{"Operations":[{"op":"replace","path":" active","value":true}]}`, ScimTypeInvalidPath},
		{"Kelvin sign is not k", "{\"Operations\":[{\"op\":\"replace\",\"path\":\"name.familyNameK\",\"value\":\"x\"}]}", ScimTypeInvalidPath},
		{"Unicode-folded path", "{\"Operations\":[{\"op\":\"replace\",\"path\":\"uſername\",\"value\":\"x\"}]}", ScimTypeInvalidPath},
		{"remove without path", `{"Operations":[{"op":"remove","value":{"active":true}}]}`, ScimTypeNoTarget},
		{"active yes", `{"Operations":[{"op":"replace","path":"active","value":"yes"}]}`, ScimTypeInvalidValue},
		{"active null", `{"Operations":[{"op":"replace","path":"active","value":null}]}`, ScimTypeInvalidValue},
		{"active number", `{"Operations":[{"op":"replace","value":{"active":1}}]}`, ScimTypeInvalidValue},
		{"no value", `{"Operations":[{"op":"replace","path":"active"}]}`, ScimTypeInvalidValue},
		{"unstored title without a value", `{"Operations":[{"op":"replace","path":"title"}]}`, ScimTypeInvalidValue},
		{"roles entry value not a string", `{"Operations":[{"op":"replace","path":"roles","value":[{"value":{"value":"Admin"}}]}]}`, ScimTypeInvalidValue},
		{"roles entry not an object", `{"Operations":[{"op":"replace","path":"roles","value":["{\"value\":\"Admin\"}"]}]}`, ScimTypeInvalidValue},
		{"userName number", `{"Operations":[{"op":"replace","path":"userName","value":1}]}`, ScimTypeInvalidValue},
		{"name not an object", `{"Operations":[{"op":"replace","path":"name","value":"x"}]}`, ScimTypeInvalidValue},
		{"name part null", `{"Operations":[{"op":"replace","path":"name","value":{"givenName":null}}]}`, ScimTypeInvalidValue},
		{"empty emails", `{"Operations":[{"op":"replace","path":"emails","value":[]}]}`, ScimTypeInvalidValue},
		{"emails without a winner", `{"Operations":[{"op":"replace","path":"emails","value":[{"value":"a@x.test"},{"value":"b@x.test"}]}]}`, ScimTypeInvalidValue},
		{"emails value not a string", `{"Operations":[{"op":"replace","path":"emails","value":[{"value":1}]}]}`, ScimTypeInvalidValue},
		{"roles not an array", `{"Operations":[{"op":"replace","path":"roles","value":"CRM.Sales"}]}`, ScimTypeInvalidValue},
		{"roles entry without value", `{"Operations":[{"op":"replace","path":"roles","value":[{"display":"x"}]}]}`, ScimTypeInvalidValue},
		{"remove active", `{"Operations":[{"op":"remove","path":"active"}]}`, ScimTypeInvalidValue},
		{"remove userName", `{"Operations":[{"op":"remove","path":"userName"}]}`, ScimTypeInvalidValue},
		{"remove emails", `{"Operations":[{"op":"remove","path":"emails"}]}`, ScimTypeInvalidValue},
		{"remove work email", `{"Operations":[{"op":"remove","path":"emails[type eq \"work\"].value"}]}`, ScimTypeInvalidValue},
		{"remove roles", `{"Operations":[{"op":"remove","path":"roles"}]}`, ScimTypeInvalidValue},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			got, err := ParseUserPatch([]byte(tc.body))
			assert.Equal(t, UserPatch{}, got)
			var patchErr *PatchError
			require.True(t, errors.As(err, &patchErr), "a *PatchError: %v", err)
			assert.Equal(t, tc.scimType, patchErr.ScimType)
		})
	}
}
