package scim

import (
	"bytes"
	"encoding/json"
	"io"
)

// UserPatch is the outcome of a SCIM PatchOp request on a User (#471): the
// attributes the operations set, in their final state. nil means untouched.
// UserName and Email both name the account's email; Email (from emails) is
// nil whenever an operation set UserName, because userName is the email.
type UserPatch struct {
	UserName   *string
	Email      *string
	GivenName  *string
	FamilyName *string
	Active     *bool
	Roles      *[]string
}

// PatchError is a refused PatchOp request. ScimType is the RFC 7644 §3.12
// error type the answer carries (invalidSyntax, invalidPath, noTarget or
// invalidValue). The message is for logs and tests; it never holds a request
// value.
type PatchError struct {
	ScimType string
	why      string
}

func (e *PatchError) Error() string { return "scim patch: " + e.ScimType + ": " + e.why }

func patchSyntax(why string) error   { return &PatchError{ScimType: ScimTypeInvalidSyntax, why: why} }
func patchPath(why string) error     { return &PatchError{ScimType: ScimTypeInvalidPath, why: why} }
func patchValue(why string) error    { return &PatchError{ScimType: ScimTypeInvalidValue, why: why} }
func patchNoTarget(why string) error { return &PatchError{ScimType: ScimTypeNoTarget, why: why} }

// patchTarget is an attribute path the patch supports.
type patchTarget int

const (
	targetNone patchTarget = iota
	targetActive
	targetUserName
	targetName
	targetGivenName
	targetFamilyName
	targetEmails
	targetWorkEmail
	targetRoles
)

// patchPaths maps each supported path, in its canonical spelling, to its
// target. A path matches under ASCII-only case folding, without trimming and
// without a schema URN prefix.
var patchPaths = []struct {
	path   string
	target patchTarget
}{
	{"active", targetActive},
	{"userName", targetUserName},
	{"name", targetName},
	{"name.givenName", targetGivenName},
	{"name.familyName", targetFamilyName},
	{"emails", targetEmails},
	{`emails[type eq "work"].value`, targetWorkEmail},
	{"roles", targetRoles},
}

// ignoredPatchNames are User attributes the CRM does not store. An add or
// replace on one of them, or on a path that starts with one of
// ignoredPatchMultiValued followed by "[" or ".", or on the enterprise
// extension, is accepted and changes nothing; so is a remove. Names match
// under ASCII-only case folding, without trimming.
var ignoredPatchNames = []string{
	"externalId", "displayName", "nickName", "profileUrl", "title", "userType",
	"preferredLanguage", "locale", "timezone",
	"name.formatted", "name.middleName", "name.honorificPrefix", "name.honorificSuffix",
}

var ignoredPatchMultiValued = []string{
	"phoneNumbers", "addresses", "ims", "photos", "entitlements", "x509Certificates",
}

// enterpriseUserURN is the enterprise extension schema; the URN itself and
// every path below it ("<URN>:department") are ignored.
const enterpriseUserURN = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

// isIgnoredPatchPath reports whether path names an attribute the CRM does not
// store, so that an operation on it is accepted and skipped.
func isIgnoredPatchPath(path string) bool {
	for _, name := range ignoredPatchNames {
		if asciiEqualFold(path, name) {
			return true
		}
	}
	for _, name := range ignoredPatchMultiValued {
		if asciiEqualFold(path, name) {
			return true
		}
		if len(path) > len(name) && asciiEqualFold(path[:len(name)], name) &&
			(path[len(name)] == '[' || path[len(name)] == '.') {
			return true
		}
	}
	if asciiEqualFold(path, enterpriseUserURN) {
		return true
	}
	n := len(enterpriseUserURN)
	return len(path) > n && asciiEqualFold(path[:n], enterpriseUserURN) && path[n] == ':'
}

func lookupPatchPath(path string) patchTarget {
	for _, p := range patchPaths {
		if asciiEqualFold(path, p.path) {
			return p.target
		}
	}
	return targetNone
}

// member is one name/value pair of a JSON object, in document order.
type member struct {
	name  string
	value json.RawMessage
}

// objectMembers decodes raw as one JSON object and returns its members in
// document order; ok is false when raw is not an object.
func objectMembers(raw json.RawMessage) ([]member, bool) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	var members []member
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := token.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		members = append(members, member{name: name, value: value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return members, true
}

// lookupMember returns the value of the one member whose name equals name
// under ASCII-only case folding. present is false when there is none; ok is
// false when more than one member matches.
func lookupMember(members []member, name string) (value json.RawMessage, present, ok bool) {
	for _, m := range members {
		if asciiEqualFold(m.name, name) {
			if present {
				return nil, true, false
			}
			value, present = m.value, true
		}
	}
	return value, present, true
}

// jsonKind is the first byte of a JSON value (after whitespace), or 0.
func jsonKind(raw json.RawMessage) byte {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

// decodeString decodes raw as a JSON string; ok is false for any other type,
// null included.
func decodeString(raw json.RawMessage) (string, bool) {
	if jsonKind(raw) != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// patchState collects the operations' writes.
type patchState struct {
	patch       UserPatch
	userNameSet bool
}

// ParseUserPatch parses a SCIM PatchOp request body on a User (RFC 7644
// §3.5.2) into the attributes it sets. It supports add, replace and remove
// (op matched with ASCII-only case folding, so Entra ID's "Replace" passes)
// on the paths active, userName, name, name.givenName, name.familyName,
// emails, emails[type eq "work"].value and roles, both with a path and in the
// path-less form whose value is an object of such paths (Okta, and Entra ID's
// value-object form). Operations on attributes the CRM does not store
// (isIgnoredPatchPath: externalId, displayName, title, phone numbers,
// addresses, the enterprise extension and the like) are accepted and change
// nothing, with a path or as a path-less key; with a path, add and replace
// still need a value. A role may be sent plain or as a JSON object serialised
// into its value string (see decodeRoles). Operations apply in array order;
// the last write to an attribute wins. Every other path is refused with
// invalidPath. Every refusal is a *PatchError carrying its scimType.
func ParseUserPatch(raw []byte) (UserPatch, error) {
	members, ok := objectMembers(raw)
	if !ok {
		return UserPatch{}, patchSyntax("the body is not a JSON object")
	}
	opsRaw, present, ok := lookupMember(members, "Operations")
	if !ok || !present || jsonKind(opsRaw) != '[' {
		return UserPatch{}, patchSyntax("Operations is missing or not an array")
	}
	var operations []json.RawMessage
	if err := json.Unmarshal(opsRaw, &operations); err != nil || len(operations) == 0 {
		return UserPatch{}, patchSyntax("Operations is empty or malformed")
	}
	state := &patchState{}
	for _, operation := range operations {
		if err := state.apply(operation); err != nil {
			return UserPatch{}, err
		}
	}
	if state.userNameSet {
		state.patch.Email = nil
	}
	return state.patch, nil
}

func (s *patchState) apply(raw json.RawMessage) error {
	members, ok := objectMembers(raw)
	if !ok {
		return patchSyntax("an operation is not an object")
	}
	opRaw, present, ok := lookupMember(members, "op")
	if !ok || !present {
		return patchSyntax("an operation has no op")
	}
	op, ok := decodeString(opRaw)
	if !ok {
		return patchSyntax("op is not a string")
	}
	remove := false
	switch {
	case asciiEqualFold(op, "add"), asciiEqualFold(op, "replace"):
	case asciiEqualFold(op, "remove"):
		remove = true
	default:
		return patchSyntax("op is not add, replace or remove")
	}
	pathRaw, hasPath, ok := lookupMember(members, "path")
	if !ok {
		return patchSyntax("an operation has more than one path")
	}
	path := ""
	if hasPath {
		if path, ok = decodeString(pathRaw); !ok {
			return patchSyntax("path is not a string")
		}
	}
	value, hasValue, ok := lookupMember(members, "value")
	if !ok {
		return patchSyntax("an operation has more than one value")
	}

	if remove {
		if !hasPath {
			return patchNoTarget("remove has no path")
		}
		target := lookupPatchPath(path)
		if target == targetNone {
			if isIgnoredPatchPath(path) {
				return nil
			}
			return patchPath("unsupported path")
		}
		return s.remove(target)
	}
	if hasPath {
		target := lookupPatchPath(path)
		if target == targetNone && !isIgnoredPatchPath(path) {
			return patchPath("unsupported path")
		}
		if !hasValue {
			return patchValue("the operation has no value")
		}
		if target == targetNone {
			return nil
		}
		return s.set(target, value)
	}
	// Path-less: the value is an object whose keys are paths. Dotted name.*
	// keys apply after a name object.
	if !hasValue {
		return patchSyntax("a path-less operation has no value")
	}
	entries, ok := objectMembers(value)
	if !ok {
		return patchSyntax("a path-less value is not an object")
	}
	var later []member
	for _, entry := range entries {
		target := lookupPatchPath(entry.name)
		switch target {
		case targetNone:
			if isIgnoredPatchPath(entry.name) {
				continue
			}
			return patchPath("unsupported attribute")
		case targetGivenName, targetFamilyName:
			later = append(later, entry)
			continue
		}
		if err := s.set(target, entry.value); err != nil {
			return err
		}
	}
	for _, entry := range later {
		if err := s.set(lookupPatchPath(entry.name), entry.value); err != nil {
			return err
		}
	}
	return nil
}

func (s *patchState) remove(target patchTarget) error {
	empty := func() *string { v := ""; return &v }
	switch target {
	case targetName:
		s.patch.GivenName, s.patch.FamilyName = empty(), empty()
	case targetGivenName:
		s.patch.GivenName = empty()
	case targetFamilyName:
		s.patch.FamilyName = empty()
	default:
		return patchValue("the attribute cannot be removed")
	}
	return nil
}

func (s *patchState) set(target patchTarget, raw json.RawMessage) error {
	switch target {
	case targetActive:
		active, ok := decodeActive(raw)
		if !ok {
			return patchValue("active is not a boolean")
		}
		s.patch.Active = &active
	case targetUserName:
		userName, ok := decodeString(raw)
		if !ok {
			return patchValue("userName is not a string")
		}
		s.patch.UserName = &userName
		s.userNameSet = true
	case targetGivenName, targetFamilyName:
		part, ok := decodeString(raw)
		if !ok {
			return patchValue("a name part is not a string")
		}
		if target == targetGivenName {
			s.patch.GivenName = &part
		} else {
			s.patch.FamilyName = &part
		}
	case targetName:
		members, ok := objectMembers(raw)
		if !ok {
			return patchValue("name is not an object")
		}
		for _, sub := range []struct {
			name string
			dst  **string
		}{{"givenName", &s.patch.GivenName}, {"familyName", &s.patch.FamilyName}} {
			value, present, ok := lookupMember(members, sub.name)
			if !ok {
				return patchValue("a name part is given twice")
			}
			if !present {
				continue
			}
			part, ok := decodeString(value)
			if !ok {
				return patchValue("a name part is not a string")
			}
			*sub.dst = &part
		}
	case targetEmails:
		email, err := decodeEmails(raw)
		if err != nil {
			return err
		}
		s.patch.Email = &email
	case targetWorkEmail:
		email, ok := decodeString(raw)
		if !ok {
			return patchValue("the work email is not a string")
		}
		s.patch.Email = &email
	case targetRoles:
		roles, err := decodeRoles(raw)
		if err != nil {
			return err
		}
		s.patch.Roles = &roles
	default:
		return patchPath("unsupported path")
	}
	return nil
}

// decodeActive accepts a JSON boolean or the strings "true" and "false" in
// any ASCII case (Entra ID sends "False").
func decodeActive(raw json.RawMessage) (bool, bool) {
	switch jsonKind(raw) {
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return false, false
		}
		return b, true
	case '"':
		s, ok := decodeString(raw)
		if !ok {
			return false, false
		}
		switch {
		case asciiEqualFold(s, "true"):
			return true, true
		case asciiEqualFold(s, "false"):
			return false, true
		}
	}
	return false, false
}

// decodeEmails picks the address of a non-empty emails array: the entry with
// primary true, then the first with type work, then a sole entry.
func decodeEmails(raw json.RawMessage) (string, error) {
	if jsonKind(raw) != '[' {
		return "", patchValue("emails is not an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return "", patchValue("emails is empty")
	}
	values := make([]string, len(entries))
	primary, work := -1, -1
	for i, entry := range entries {
		members, ok := objectMembers(entry)
		if !ok {
			return "", patchValue("an emails entry is not an object")
		}
		valueRaw, present, ok := lookupMember(members, "value")
		if !ok || !present {
			return "", patchValue("an emails entry has no value")
		}
		if values[i], ok = decodeString(valueRaw); !ok {
			return "", patchValue("an emails value is not a string")
		}
		if primaryRaw, present, ok := lookupMember(members, "primary"); ok && present && primary < 0 {
			var isPrimary bool
			if jsonKind(primaryRaw) == 't' && json.Unmarshal(primaryRaw, &isPrimary) == nil && isPrimary {
				primary = i
			}
		}
		if typeRaw, present, ok := lookupMember(members, "type"); ok && present && work < 0 {
			if kind, ok := decodeString(typeRaw); ok && asciiEqualFold(kind, "work") {
				work = i
			}
		}
	}
	switch {
	case primary >= 0:
		return values[primary], nil
	case work >= 0:
		return values[work], nil
	case len(values) == 1:
		return values[0], nil
	}
	return "", patchValue("no primary or work address")
}

// decodeRoles reads the string value of every entry of a roles array. When
// that string is exactly one JSON object with a single member "value" (ASCII
// folding) holding a JSON string, the role is that inner string: Entra ID
// sends an app role this way without aadOptscim062020
// ("value":"{\"id\":...,\"value\":\"Admin\",...}"). Any other string is the
// role as sent.
func decodeRoles(raw json.RawMessage) ([]string, error) {
	if jsonKind(raw) != '[' {
		return nil, patchValue("roles is not an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, patchValue("roles is malformed")
	}
	roles := make([]string, 0, len(entries))
	for _, entry := range entries {
		members, ok := objectMembers(entry)
		if !ok {
			return nil, patchValue("a roles entry is not an object")
		}
		valueRaw, present, ok := lookupMember(members, "value")
		if !ok || !present {
			return nil, patchValue("a roles entry has no value")
		}
		role, ok := decodeString(valueRaw)
		if !ok {
			return nil, patchValue("a roles value is not a string")
		}
		roles = append(roles, roleText(role))
	}
	return roles, nil
}

// roleText returns the inner value of a role sent as a JSON object in text,
// or the text itself.
func roleText(text string) string {
	members, ok := objectMembers(json.RawMessage(text))
	if !ok {
		return text
	}
	inner, present, ok := lookupMember(members, "value")
	if !ok || !present {
		return text
	}
	if role, ok := decodeString(inner); ok {
		return role
	}
	return text
}
