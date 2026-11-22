package rolemap

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatch(t *testing.T) {
	rolesFirst := []Rule{{Claim: ClaimRoles, Value: "r", RoleID: 1}, {Claim: ClaimGroups, Value: "g", RoleID: 2}}
	groupsFirst := []Rule{{Claim: ClaimGroups, Value: "g", RoleID: 2}, {Claim: ClaimRoles, Value: "r", RoleID: 1}}
	groupsRule := []Rule{{Claim: ClaimGroups, Value: "g", RoleID: 2}}
	rolesRule := []Rule{{Claim: ClaimRoles, Value: "r", RoleID: 1}}

	cases := []struct {
		name   string
		rules  []Rule
		groups []string
		roles  []string
		wantID uint
		wantOK bool
	}{
		{"roles rule first wins across claims", rolesFirst, []string{"g"}, []string{"r"}, 1, true},
		{"groups rule first wins across claims", groupsFirst, []string{"g"}, []string{"r"}, 2, true},
		{"letter case differs", groupsRule, []string{"G"}, nil, 0, false},
		{"leading space", groupsRule, []string{" g"}, nil, 0, false},
		{"trailing space", groupsRule, []string{"g "}, nil, 0, false},
		{"kelvin sign is not k", []Rule{{Claim: ClaimGroups, Value: "k", RoleID: 3}}, []string{"K"}, nil, 0, false},
		{"unknown claim skipped", []Rule{{Claim: "email", Value: "x", RoleID: 4}}, []string{"x"}, []string{"x"}, 0, false},
		{"unknown claim skipped before a match", []Rule{{Claim: "email", Value: "x", RoleID: 4}, {Claim: ClaimGroups, Value: "x", RoleID: 5}}, []string{"x"}, []string{"x"}, 5, true},
		{"groups-only call never matches a roles rule", rolesRule, []string{"r"}, nil, 0, false},
		{"roles-only call never matches a groups rule", groupsRule, nil, []string{"g"}, 0, false},
		{"groups-only call matches a groups rule", groupsRule, []string{"g"}, nil, 2, true},
		{"roles-only call matches a roles rule", rolesRule, nil, []string{"r"}, 1, true},
		{"nil rules", nil, []string{"g"}, []string{"r"}, 0, false},
		{"empty rules", []Rule{}, []string{"g"}, []string{"r"}, 0, false},
		{"earlier of two matching rules wins", []Rule{{Claim: ClaimGroups, Value: "a", RoleID: 6}, {Claim: ClaimGroups, Value: "b", RoleID: 7}}, []string{"b", "a"}, nil, 6, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := Match(tc.rules, tc.groups, tc.roles)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantOK, ok)
		})
	}
}

func TestUsesGroups(t *testing.T) {
	assert.True(t, UsesGroups([]Rule{{Claim: ClaimRoles, Value: "r"}, {Claim: ClaimGroups, Value: "g"}}))
	assert.False(t, UsesGroups([]Rule{{Claim: ClaimRoles, Value: "r"}}))
	assert.False(t, UsesGroups(nil))
	assert.False(t, UsesGroups([]Rule{{Claim: "email", Value: "x"}}))
}

func TestRuleJSON(t *testing.T) {
	const text = `{"claim":"groups","value":"g","role_id":2}`
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var rule Rule
	require.NoError(t, dec.Decode(&rule))
	assert.Equal(t, Rule{Claim: ClaimGroups, Value: "g", RoleID: 2}, rule)

	out, err := json.Marshal(Rule{Claim: ClaimGroups, Value: "g", RoleID: 2})
	require.NoError(t, err)
	assert.Equal(t, text, string(out))
}
