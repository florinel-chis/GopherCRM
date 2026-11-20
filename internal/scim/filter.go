package scim

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalidFilter is a filter outside the one supported shape,
// userName eq "<value>" (400, scimType invalidFilter).
var ErrInvalidFilter = errors.New("unsupported filter")

// ErrInvalidPaging is a startIndex or count that is not a base-10 integer
// (400, scimType invalidValue).
var ErrInvalidPaging = errors.New("startIndex and count must be integers")

// userAttributePrefix is the schema URN an attribute path may carry.
const userAttributePrefix = UserSchema + ":"

// ParseUserFilter parses the only filter this provider supports (#469):
//
//	<attr> <op> <value>
//
// attr is userName (ASCII case-insensitive), optionally prefixed by the core
// User schema URN; op is eq (ASCII case-insensitive); value is a JSON string
// literal, decoded with encoding/json. Tokens are separated by one or more
// spaces and whitespace around the expression is ignored. It returns the
// decoded value; anything else, other attributes, operators, logical
// expressions, parentheses, unquoted or non-string values and trailing tokens
// included, is ErrInvalidFilter.
func ParseUserFilter(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	attr, rest, ok := strings.Cut(s, " ")
	if !ok {
		return "", ErrInvalidFilter
	}
	if len(attr) > len(userAttributePrefix) && asciiEqualFold(attr[:len(userAttributePrefix)], userAttributePrefix) {
		attr = attr[len(userAttributePrefix):]
	}
	if !asciiEqualFold(attr, "userName") {
		return "", ErrInvalidFilter
	}
	return parseEqStringLiteral(rest)
}

// stringLiteralEnd returns the length of the JSON string literal s starts
// with, or -1 when s does not start with a closed one.
func stringLiteralEnd(s string) int {
	if !strings.HasPrefix(s, `"`) {
		return -1
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// UserNameMatches is the userName judge of #469: requested equals stored
// under ASCII-only case folding. Both must have the same byte length, and
// every byte must be equal after mapping only 'A'-'Z' to 'a'-'z'; every other
// byte, each byte of a multi-byte UTF-8 sequence included, must be identical.
// It deliberately is not strings.EqualFold: Unicode simple folding treats
// U+212A KELVIN SIGN as k and U+017F LONG S as s, and the database collation
// of users.email (utf8mb4_unicode_ci, accent-insensitive, PAD SPACE) already
// matches padded and accented variants that this judge refuses.
func UserNameMatches(stored, requested string) bool {
	return asciiEqualFold(stored, requested)
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// Paging is a normalised RFC 7644 §3.4.2.4 page request: StartIndex is
// 1-based and at least 1, Count is 0..MaxResults.
type Paging struct {
	StartIndex int
	Count      int
}

// ParsePaging normalises the startIndex and count query parameters. present
// says whether each was sent. An absent or < 1 startIndex is 1; an absent
// count is MaxResults, a negative one 0 and one above MaxResults MaxResults.
// A value that is not a base-10 integer is ErrInvalidPaging; an integer out
// of range is clamped like any other.
func ParsePaging(startIndex string, startPresent bool, count string, countPresent bool) (Paging, error) {
	page := Paging{StartIndex: 1, Count: MaxResults}
	if startPresent {
		n, err := parsePagingInt(startIndex)
		if err != nil {
			return Paging{}, err
		}
		if n > 1 {
			page.StartIndex = n
		}
	}
	if countPresent {
		n, err := parsePagingInt(count)
		if err != nil {
			return Paging{}, err
		}
		switch {
		case n < 0:
			page.Count = 0
		case n < MaxResults:
			page.Count = n
		}
	}
	return page, nil
}

// parsePagingInt parses a base-10 integer, clamping one out of the int range.
func parsePagingInt(raw string) (int, error) {
	n, err := strconv.ParseInt(raw, 10, strconv.IntSize)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) {
			// ParseInt returned the nearest bound.
			return int(n), nil
		}
		return 0, ErrInvalidPaging
	}
	return int(n), nil
}
