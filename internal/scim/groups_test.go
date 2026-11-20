package scim

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSCIMGroupResource(t *testing.T) {
	group := NewGroup("https://crm.example.test/", "Sales", []string{"3", "7"})
	raw, err := json.Marshal(group)
	require.NoError(t, err)
	id := GroupID("Sales")
	assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"id":"`+id+`","displayName":"Sales",
		"members":[{"value":"3"},{"value":"7"}],
		"meta":{"resourceType":"Group","location":"https://crm.example.test/scim/v2/Groups/`+id+`"}}`, string(raw))

	empty, err := json.Marshal(NewGroup("", "Admins", nil))
	require.NoError(t, err)
	assert.Contains(t, string(empty), `"members":[]`, "members is never null")
	assert.NotContains(t, string(empty), "created", "a group has no created time")
	assert.NotContains(t, string(empty), "lastModified")

	list, err := json.Marshal(NewGroupList(5, 3, nil))
	require.NoError(t, err)
	assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":5,"itemsPerPage":0,"startIndex":3,"Resources":[]}`, string(list))
	page := NewGroupList(2, 1, []Group{group})
	assert.Equal(t, 1, page.ItemsPerPage)
}

func TestSCIMGroupID(t *testing.T) {
	for _, value := range []string{"Sales", "sales", "CRM Admins/EU", "a+b?c#d", "Ünïcødé", strings.Repeat("x", 256), " padded "} {
		id := GroupID(value)
		assert.NotContains(t, id, "=", "unpadded")
		assert.NotContains(t, id, "/", "url-safe")
		assert.NotContains(t, id, "+", "url-safe")
		back, err := ParseGroupID(id)
		require.NoError(t, err, value)
		assert.Equal(t, value, back)
	}
	assert.NotEqual(t, GroupID("Sales"), GroupID("sales"))
	for _, bad := range []string{"", "U2FsZXM=", "U2FsZXM==", "a", "!!!", "U2Fs ZXM", "U2FsZXN"} {
		_, err := ParseGroupID(bad)
		assert.ErrorIs(t, err, ErrInvalidGroupID, bad)
	}
}

func TestSCIMParseGroupFilter(t *testing.T) {
	accepted := map[string]string{
		`displayName eq "Sales"`:      "Sales",
		`DISPLAYNAME EQ "Sales"`:      "Sales",
		`  displayName   eq   "a b" `: "a b",
		`urn:ietf:params:scim:schemas:core:2.0:Group:displayName eq "X"`: "X",
		`URN:IETF:PARAMS:SCIM:SCHEMAS:CORE:2.0:GROUP:displayname eq "X"`: "X",
		`displayName eq "q\"uoteé"`:                                      "q\"uoteé",
		`displayName eq ""`:                                              "",
	}
	for raw, want := range accepted {
		got, err := ParseGroupFilter(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{
		"", `displayName`, `displayName eq`, `displayName eq Sales`, `displayName co "Sales"`, `displayName ne "Sales"`,
		`userName eq "Sales"`, `externalId eq "x"`, `displayName eq "a" and displayName eq "b"`, `(displayName eq "a")`,
		`displayName eq "a" x`, `displayName eq 5`, `displayName eq null`, `urn:ietf:params:scim:schemas:core:2.0:User:displayName eq "a"`,
		`displayName eq "unterminated`, `displayNaMe eq "a"extra`, "displayNameK eq \"a\"",
	} {
		_, err := ParseGroupFilter(raw)
		assert.ErrorIs(t, err, ErrInvalidFilter, raw)
	}
}

func TestSCIMParseGroupPatch(t *testing.T) {
	accepted := []struct {
		name string
		body string
		want []GroupPatchOp
	}{
		{"add members", `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"3","display":"Ann","$ref":"https://x/Users/3"}]}]}`,
			[]GroupPatchOp{{Op: GroupPatchAdd, Values: []string{"3"}}}},
		{"entra Add case", `{"Operations":[{"op":"Add","path":"MEMBERS","value":[{"value":"3"},{"value":"4"}]}]}`,
			[]GroupPatchOp{{Op: GroupPatchAdd, Values: []string{"3", "4"}}}},
		{"replace members empty", `{"Operations":[{"op":"Replace","path":"members","value":[]}]}`,
			[]GroupPatchOp{{Op: GroupPatchReplace, Values: []string{}}}},
		{"path-less add", `{"Operations":[{"op":"add","value":{"members":[{"value":"5"}]}}]}`,
			[]GroupPatchOp{{Op: GroupPatchAdd, Values: []string{"5"}}}},
		{"path-less replace", `{"Operations":[{"op":"REPLACE","value":{"Members":[{"value":"5"}]}}]}`,
			[]GroupPatchOp{{Op: GroupPatchReplace, Values: []string{"5"}}}},
		{"remove filter", `{"Operations":[{"op":"remove","path":"members[value eq \"7\"]"}]}`,
			[]GroupPatchOp{{Op: GroupPatchRemove, Values: []string{"7"}}}},
		{"remove filter any case", `{"Operations":[{"op":"Remove","path":"Members[VALUE EQ \"7\"]"}]}`,
			[]GroupPatchOp{{Op: GroupPatchRemove, Values: []string{"7"}}}},
		{"remove all", `{"Operations":[{"op":"remove","path":"members"}]}`,
			[]GroupPatchOp{{Op: GroupPatchRemove, All: true}}},
		{"entra remove with value", `{"Operations":[{"op":"Remove","path":"members","value":[{"value":"8"}]}]}`,
			[]GroupPatchOp{{Op: GroupPatchRemove, Values: []string{"8"}}}},
		{"ordered ops", `{"Operations":[{"op":"add","path":"members","value":[{"value":"2"}]},{"op":"remove","path":"members[value eq \"1\"]"}]}`,
			[]GroupPatchOp{{Op: GroupPatchAdd, Values: []string{"2"}}, {Op: GroupPatchRemove, Values: []string{"1"}}}},
		{"non-numeric value is the service's call", `{"Operations":[{"op":"add","path":"members","value":[{"value":"abc"}]}]}`,
			[]GroupPatchOp{{Op: GroupPatchAdd, Values: []string{"abc"}}}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			got, err := ParseGroupPatch([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Ops)
		})
	}

	refused := []struct {
		name, body, scimType string
	}{
		{"not an object", `[]`, ScimTypeInvalidSyntax},
		{"malformed", `{"Operations":[`, ScimTypeInvalidSyntax},
		{"no operations", `{"schemas":[]}`, ScimTypeInvalidSyntax},
		{"empty operations", `{"Operations":[]}`, ScimTypeInvalidSyntax},
		{"operations not an array", `{"Operations":{}}`, ScimTypeInvalidSyntax},
		{"unknown op", `{"Operations":[{"op":"move","path":"members","value":[]}]}`, ScimTypeInvalidSyntax},
		{"op not a string", `{"Operations":[{"op":1,"path":"members","value":[]}]}`, ScimTypeInvalidSyntax},
		{"path-less without value", `{"Operations":[{"op":"add"}]}`, ScimTypeInvalidSyntax},
		{"displayName path", `{"Operations":[{"op":"replace","path":"displayName","value":"New"}]}`, ScimTypeInvalidPath},
		{"externalId path", `{"Operations":[{"op":"add","path":"externalId","value":"x"}]}`, ScimTypeInvalidPath},
		{"path-less displayName", `{"Operations":[{"op":"replace","value":{"displayName":"New"}}]}`, ScimTypeInvalidPath},
		{"path-less members and displayName", `{"Operations":[{"op":"replace","value":{"members":[],"displayName":"New"}}]}`, ScimTypeInvalidPath},
		{"filter on add", `{"Operations":[{"op":"add","path":"members[value eq \"1\"]","value":[]}]}`, ScimTypeInvalidPath},
		{"remove displayName", `{"Operations":[{"op":"remove","path":"displayName"}]}`, ScimTypeInvalidPath},
		{"remove bad filter", `{"Operations":[{"op":"remove","path":"members[display eq \"1\"]"}]}`, ScimTypeInvalidPath},
		{"remove filter unquoted", `{"Operations":[{"op":"remove","path":"members[value eq 1]"}]}`, ScimTypeInvalidPath},
		{"remove without path", `{"Operations":[{"op":"remove","value":[{"value":"1"}]}]}`, ScimTypeNoTarget},
		{"member value number", `{"Operations":[{"op":"add","path":"members","value":[{"value":3}]}]}`, ScimTypeInvalidValue},
		{"member value null", `{"Operations":[{"op":"add","path":"members","value":[{"value":null}]}]}`, ScimTypeInvalidValue},
		{"member without value", `{"Operations":[{"op":"add","path":"members","value":[{"display":"x"}]}]}`, ScimTypeInvalidValue},
		{"member not an object", `{"Operations":[{"op":"add","path":"members","value":["3"]}]}`, ScimTypeInvalidValue},
		{"members not an array", `{"Operations":[{"op":"add","path":"members","value":{"value":"3"}}]}`, ScimTypeInvalidValue},
		{"add without value", `{"Operations":[{"op":"add","path":"members"}]}`, ScimTypeInvalidValue},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := ParseGroupPatch([]byte(tc.body))
			require.Error(t, err)
			var patchErr *PatchError
			require.True(t, errors.As(err, &patchErr))
			assert.Equal(t, tc.scimType, patchErr.ScimType)
			for _, secret := range []string{"New", `"x"`, "display"} {
				assert.NotContains(t, err.Error(), secret, "no request value in the message")
			}
		})
	}
}
