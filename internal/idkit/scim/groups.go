package scim

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// ErrInvalidGroupID is a Group id that is not the unpadded base64url
// encoding of a group value.
var ErrInvalidGroupID = errors.New("invalid group id")

// groupAttributePrefix is the schema URN a Group attribute path may carry.
const groupAttributePrefix = GroupSchema + ":"

// GroupMember is one entry of a Group's members: the member User's id.
type GroupMember struct {
	Value string `json:"value"`
}

// GroupMeta is the meta attribute of a Group resource. A group is a value of
// the role mapping, so it has no created or lastModified time.
type GroupMeta struct {
	ResourceType string `json:"resourceType"`
	Location     string `json:"location"`
}

// Group is the core Group resource (RFC 7643 §4.2) as this provider serves
// it: the id, the groups-claim value as displayName, and the members.
// members is never null; WithoutMembers omits the attribute.
type Group struct {
	Schemas     []string       `json:"schemas"`
	ID          string         `json:"id"`
	DisplayName string         `json:"displayName"`
	Members     *[]GroupMember `json:"members,omitempty"`
	Meta        GroupMeta      `json:"meta"`
}

// WithoutMembers returns the group without its members attribute, as
// excludedAttributes=members asks.
func (g Group) WithoutMembers() Group {
	g.Members = nil
	return g
}

// GroupID is the id of the group value: its unpadded base64url encoding, so
// any value is a valid path segment and two values never share an id.
func GroupID(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// ParseGroupID reverses GroupID; anything that is not an unpadded base64url
// string, padding and the standard alphabet included, is ErrInvalidGroupID.
func ParseGroupID(id string) (string, error) {
	if id == "" {
		return "", ErrInvalidGroupID
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	if err != nil {
		return "", ErrInvalidGroupID
	}
	return string(decoded), nil
}

// NewGroup builds the Group resource of value with the members' User ids, in
// the given order, and meta.location under baseURL.
func NewGroup(baseURL, value string, memberIDs []string) Group {
	id := GroupID(value)
	members := make([]GroupMember, 0, len(memberIDs))
	for _, member := range memberIDs {
		members = append(members, GroupMember{Value: member})
	}
	return Group{
		Schemas:     []string{GroupSchema},
		ID:          id,
		DisplayName: value,
		Members:     &members,
		Meta:        GroupMeta{ResourceType: "Group", Location: location(baseURL, "/Groups/"+id)},
	}
}

// NewGroupList is the ListResponse of one page: totalResults is total,
// itemsPerPage the page's length, startIndex the effective start index.
// Resources is never null.
func NewGroupList(total, startIndex int, groups []Group) ListResponse[Group] {
	if groups == nil {
		groups = []Group{}
	}
	return ListResponse[Group]{
		Schemas:      []string{ListResponseSchema},
		TotalResults: total,
		ItemsPerPage: len(groups),
		StartIndex:   startIndex,
		Resources:    groups,
	}
}

// ParseGroupFilter parses the only Group filter this provider supports:
//
//	displayName eq "<value>"
//
// with the grammar of ParseUserFilter: attr is displayName (ASCII
// case-insensitive), optionally prefixed by the core Group schema URN; op is
// eq (ASCII case-insensitive); value is a JSON string literal. It returns the
// decoded value, which the caller compares byte for byte; anything else is
// ErrInvalidFilter.
func ParseGroupFilter(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	attr, rest, ok := strings.Cut(s, " ")
	if !ok {
		return "", ErrInvalidFilter
	}
	if len(attr) > len(groupAttributePrefix) && asciiEqualFold(attr[:len(groupAttributePrefix)], groupAttributePrefix) {
		attr = attr[len(groupAttributePrefix):]
	}
	if !asciiEqualFold(attr, "displayName") {
		return "", ErrInvalidFilter
	}
	return parseEqStringLiteral(rest)
}

// parseEqStringLiteral parses `eq "<JSON string>"` after the attribute.
func parseEqStringLiteral(rest string) (string, error) {
	op, value, ok := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if !ok || !asciiEqualFold(op, "eq") {
		return "", ErrInvalidFilter
	}
	value = strings.TrimLeft(value, " ")
	end := stringLiteralEnd(value)
	if end < 0 || end != len(value) {
		return "", ErrInvalidFilter
	}
	var decoded string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return "", ErrInvalidFilter
	}
	return decoded, nil
}

// groupMembersAttribute is the members attribute with its schema URN.
const groupMembersAttribute = groupAttributePrefix + "members"

// ExcludesMembers reports whether the excludedAttributes parameter raw names
// the members attribute: one of its comma-separated, space-trimmed items is
// members or its core Group URN form, under ASCII-only case folding. Every
// other name is ignored.
func ExcludesMembers(raw string) bool {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if asciiEqualFold(item, "members") || asciiEqualFold(item, groupMembersAttribute) {
			return true
		}
	}
	return false
}

// GroupCreate is the outcome of a Group create body: the externalId
// and displayName it carries, nil when absent or null. At least one is set.
type GroupCreate struct {
	ExternalID  *string
	DisplayName *string
}

// GroupCreateError is a refused Group create body. ScimType is invalidSyntax
// or invalidValue; Members is true when the refusal is a members attribute
// that is neither null nor empty. The message is for logs and tests; it never
// holds a request value.
type GroupCreateError struct {
	ScimType string
	Members  bool
	why      string
}

func (e *GroupCreateError) Error() string { return "scim group create: " + e.ScimType + ": " + e.why }

// ParseGroupCreate parses the body of POST /Groups. Names match under
// ASCII-only case folding. A body that is not one JSON object, or that sends
// externalId, displayName or members twice, is invalidSyntax. externalId or
// displayName that is neither a string nor null, members that is neither
// null nor [], and a body in which neither externalId nor displayName is a
// string are invalidValue. schemas, meta and every other key are ignored.
// Every refusal is a *GroupCreateError.
func ParseGroupCreate(raw []byte) (GroupCreate, error) {
	members, ok := objectMembers(raw)
	if !ok {
		return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidSyntax, why: "the body is not a JSON object"}
	}
	var out GroupCreate
	for _, field := range []struct {
		name   string
		target **string
	}{{"externalId", &out.ExternalID}, {"displayName", &out.DisplayName}} {
		value, present, ok := lookupMember(members, field.name)
		if !ok {
			return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidSyntax, why: "an attribute is sent twice"}
		}
		if !present || isJSONNull(value) {
			continue
		}
		s, ok := decodeString(value)
		if !ok {
			return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidValue, why: "an attribute is not a string"}
		}
		*field.target = &s
	}
	value, present, ok := lookupMember(members, "members")
	if !ok {
		return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidSyntax, why: "an attribute is sent twice"}
	}
	if present && !isNullOrEmptyArray(value) {
		return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidValue, Members: true, why: "members is not empty"}
	}
	if out.ExternalID == nil && out.DisplayName == nil {
		return GroupCreate{}, &GroupCreateError{ScimType: ScimTypeInvalidValue, why: "neither externalId nor displayName is a string"}
	}
	return out, nil
}

// isNullOrEmptyArray reports whether raw is the JSON null or an empty array.
func isNullOrEmptyArray(raw json.RawMessage) bool {
	if isJSONNull(raw) {
		return true
	}
	trimmed := bytes.TrimSpace(raw)
	if jsonKind(trimmed) != '[' {
		return false
	}
	var items []json.RawMessage
	return json.Unmarshal(trimmed, &items) == nil && len(items) == 0
}

// isJSONNull reports whether raw is the JSON null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
