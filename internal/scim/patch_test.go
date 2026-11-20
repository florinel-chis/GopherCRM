package scim

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

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
		{"externalId", `{"Operations":[{"op":"replace","path":"externalId","value":"x"}]}`, ScimTypeInvalidPath},
		{"displayName", `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`, ScimTypeInvalidPath},
		{"name.formatted", `{"Operations":[{"op":"replace","path":"name.formatted","value":"x"}]}`, ScimTypeInvalidPath},
		{"path-less externalId", `{"Operations":[{"op":"add","value":{"active":true,"externalId":"x"}}]}`, ScimTypeInvalidPath},
		{"other filter", `{"Operations":[{"op":"replace","path":"emails[type eq \"home\"].value","value":"x"}]}`, ScimTypeInvalidPath},
		{"URN prefix", `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:core:2.0:User:active","value":true}]}`, ScimTypeInvalidPath},
		{"extension URN", `{"Operations":[{"op":"replace","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department","value":"x"}]}`, ScimTypeInvalidPath},
		{"padded path", `{"Operations":[{"op":"replace","path":" active","value":true}]}`, ScimTypeInvalidPath},
		{"Kelvin sign is not k", "{\"Operations\":[{\"op\":\"replace\",\"path\":\"name.familyNameK\",\"value\":\"x\"}]}", ScimTypeInvalidPath},
		{"Unicode-folded path", "{\"Operations\":[{\"op\":\"replace\",\"path\":\"uſername\",\"value\":\"x\"}]}", ScimTypeInvalidPath},
		{"remove without path", `{"Operations":[{"op":"remove","value":{"active":true}}]}`, ScimTypeNoTarget},
		{"active yes", `{"Operations":[{"op":"replace","path":"active","value":"yes"}]}`, ScimTypeInvalidValue},
		{"active null", `{"Operations":[{"op":"replace","path":"active","value":null}]}`, ScimTypeInvalidValue},
		{"active number", `{"Operations":[{"op":"replace","value":{"active":1}}]}`, ScimTypeInvalidValue},
		{"no value", `{"Operations":[{"op":"replace","path":"active"}]}`, ScimTypeInvalidValue},
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
		{"remove unknown path", `{"Operations":[{"op":"remove","path":"externalId"}]}`, ScimTypeInvalidPath},
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
