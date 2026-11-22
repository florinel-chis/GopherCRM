package scim

import (
	"encoding/json"
	"strings"
)

// Operations of a GroupPatchOp.
const (
	GroupPatchAdd     = "add"
	GroupPatchRemove  = "remove"
	GroupPatchReplace = "replace"
)

// GroupPatchOp is one operation of a Group patch, in canonical
// lower case. Values are the member ids it names, as sent. All is set only on
// a remove of path members without a value: every member goes.
type GroupPatchOp struct {
	Op     string
	Values []string
	All    bool
}

// GroupPatch is the outcome of a SCIM PatchOp request on a Group: its
// operations, in request order.
type GroupPatch struct {
	Ops []GroupPatchOp
}

// groupMembersPath is the one attribute a Group patch may change.
const groupMembersPath = "members"

// ParseGroupPatch parses a SCIM PatchOp request body on a Group (RFC 7644
// §3.5.2). It supports, with op matched under ASCII-only case folding:
//
//   - add and replace with path members and an array of member objects;
//   - add and replace without a path, whose value is {"members": [...]};
//   - remove with path members[value eq "<id>"];
//   - remove with path members and an array of member objects (Entra ID), or
//     without a value, which removes every member.
//
// A member object's value must be a JSON string; its other keys (display,
// $ref, type) are ignored. A missing or empty Operations array, or a
// malformed body, is invalidSyntax; any other path or attribute (displayName,
// externalId, ...) is invalidPath; remove without a path is noTarget; a
// member value that is not a string is invalidValue. Every refusal is a
// *PatchError; no message holds a request value.
func ParseGroupPatch(raw []byte) (GroupPatch, error) {
	members, ok := objectMembers(raw)
	if !ok {
		return GroupPatch{}, patchSyntax("the body is not a JSON object")
	}
	opsRaw, present, ok := lookupMember(members, "Operations")
	if !ok || !present || jsonKind(opsRaw) != '[' {
		return GroupPatch{}, patchSyntax("Operations is missing or not an array")
	}
	var operations []json.RawMessage
	if err := json.Unmarshal(opsRaw, &operations); err != nil || len(operations) == 0 {
		return GroupPatch{}, patchSyntax("Operations is empty or malformed")
	}
	patch := GroupPatch{Ops: make([]GroupPatchOp, 0, len(operations))}
	for _, operation := range operations {
		op, err := parseGroupOperation(operation)
		if err != nil {
			return GroupPatch{}, err
		}
		patch.Ops = append(patch.Ops, op...)
	}
	return patch, nil
}

func parseGroupOperation(raw json.RawMessage) ([]GroupPatchOp, error) {
	members, ok := objectMembers(raw)
	if !ok {
		return nil, patchSyntax("an operation is not an object")
	}
	opRaw, present, ok := lookupMember(members, "op")
	if !ok || !present {
		return nil, patchSyntax("an operation has no op")
	}
	opName, ok := decodeString(opRaw)
	if !ok {
		return nil, patchSyntax("op is not a string")
	}
	var op string
	switch {
	case asciiEqualFold(opName, GroupPatchAdd):
		op = GroupPatchAdd
	case asciiEqualFold(opName, GroupPatchReplace):
		op = GroupPatchReplace
	case asciiEqualFold(opName, GroupPatchRemove):
		op = GroupPatchRemove
	default:
		return nil, patchSyntax("op is not add, replace or remove")
	}
	pathRaw, hasPath, ok := lookupMember(members, "path")
	if !ok {
		return nil, patchSyntax("an operation has more than one path")
	}
	path := ""
	if hasPath {
		if path, ok = decodeString(pathRaw); !ok {
			return nil, patchSyntax("path is not a string")
		}
	}
	value, hasValue, ok := lookupMember(members, "value")
	if !ok {
		return nil, patchSyntax("an operation has more than one value")
	}

	if op == GroupPatchRemove {
		if !hasPath {
			return nil, patchNoTarget("remove has no path")
		}
		if asciiEqualFold(path, groupMembersPath) {
			if !hasValue || jsonKind(value) == 'n' {
				return []GroupPatchOp{{Op: GroupPatchRemove, All: true}}, nil
			}
			values, err := decodeGroupMembers(value)
			if err != nil {
				return nil, err
			}
			return []GroupPatchOp{{Op: GroupPatchRemove, Values: values}}, nil
		}
		id, ok := parseMemberFilterPath(path)
		if !ok {
			return nil, patchPath("unsupported path")
		}
		return []GroupPatchOp{{Op: GroupPatchRemove, Values: []string{id}}}, nil
	}
	if hasPath {
		if !asciiEqualFold(path, groupMembersPath) {
			return nil, patchPath("unsupported path")
		}
		if !hasValue {
			return nil, patchValue("the operation has no value")
		}
		values, err := decodeGroupMembers(value)
		if err != nil {
			return nil, err
		}
		return []GroupPatchOp{{Op: op, Values: values}}, nil
	}
	// Path-less: the value is an object whose only supported key is members.
	if !hasValue {
		return nil, patchSyntax("a path-less operation has no value")
	}
	entries, ok := objectMembers(value)
	if !ok {
		return nil, patchSyntax("a path-less value is not an object")
	}
	var ops []GroupPatchOp
	for _, entry := range entries {
		if !asciiEqualFold(entry.name, groupMembersPath) {
			return nil, patchPath("unsupported attribute")
		}
		values, err := decodeGroupMembers(entry.value)
		if err != nil {
			return nil, err
		}
		ops = append(ops, GroupPatchOp{Op: op, Values: values})
	}
	return ops, nil
}

// decodeGroupMembers reads the value of every entry of a members array.
func decodeGroupMembers(raw json.RawMessage) ([]string, error) {
	if jsonKind(raw) != '[' {
		return nil, patchValue("members is not an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, patchValue("members is malformed")
	}
	values := make([]string, 0, len(entries))
	for _, entry := range entries {
		members, ok := objectMembers(entry)
		if !ok {
			return nil, patchValue("a members entry is not an object")
		}
		valueRaw, present, ok := lookupMember(members, "value")
		if !ok || !present {
			return nil, patchValue("a members entry has no value")
		}
		value, ok := decodeString(valueRaw)
		if !ok {
			return nil, patchValue("a members value is not a string")
		}
		values = append(values, value)
	}
	return values, nil
}

// parseMemberFilterPath parses members[value eq "<id>"]: members and value
// under ASCII-only case folding, eq as in ParseUserFilter, the id a JSON
// string literal.
func parseMemberFilterPath(path string) (string, bool) {
	const prefix = groupMembersPath + "["
	if len(path) <= len(prefix)+1 || !asciiEqualFold(path[:len(prefix)], prefix) || !strings.HasSuffix(path, "]") {
		return "", false
	}
	inner := strings.TrimSpace(path[len(prefix) : len(path)-1])
	attr, rest, ok := strings.Cut(inner, " ")
	if !ok || !asciiEqualFold(attr, "value") {
		return "", false
	}
	id, err := parseEqStringLiteral(rest)
	if err != nil {
		return "", false
	}
	return id, true
}
