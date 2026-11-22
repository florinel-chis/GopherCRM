// Package rolemap maps the values of an ID-token or SCIM claim to a role id.
// A mapping is an ordered list of rules; the first rule whose claim holds its
// value exactly decides the role. The package has no dependencies beyond the
// standard library.
package rolemap

// Claims a rule may match on.
const (
	ClaimGroups = "groups"
	ClaimRoles  = "roles"
)

// Rule maps one value of a claim to a role id. The JSON shape is the stored
// role mapping format.
type Rule struct {
	Claim  string `json:"claim"`
	Value  string `json:"value"`
	RoleID uint   `json:"role_id"`
}

// Match is the first rule, in stored order, whose claim array holds its value
// exactly (case-sensitive, byte for byte). A rule on the roles claim is tested
// against roles, a rule on the groups claim against groups; a rule on any other
// claim is skipped. Without a match it returns 0 and false.
func Match(rules []Rule, groups, roles []string) (uint, bool) {
	for _, entry := range rules {
		values := groups
		if entry.Claim == ClaimRoles {
			values = roles
		} else if entry.Claim != ClaimGroups {
			continue
		}
		for _, value := range values {
			if value == entry.Value {
				return entry.RoleID, true
			}
		}
	}
	return 0, false
}

// UsesGroups reports whether any rule matches on the groups claim.
func UsesGroups(rules []Rule) bool {
	for _, entry := range rules {
		if entry.Claim == ClaimGroups {
			return true
		}
	}
	return false
}
