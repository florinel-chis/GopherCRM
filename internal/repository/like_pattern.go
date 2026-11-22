package repository

import "strings"

// likeEscape is the escape character of every LIKE built from containsPattern;
// the statement must say `col LIKE ? ESCAPE '!'`.
//
// It is not a backslash on purpose: '\\' is one character on MySQL/MariaDB but
// two on SQLite, and MySQL treats a backslash as its default LIKE escape. With
// ESCAPE '!' all three engines read a backslash in the term literally.
const likeEscape = "!"

// likePatternEscaper prefixes the escape character and the two LIKE wildcards
// with the escape character.
var likePatternEscaper = strings.NewReplacer(
	likeEscape, likeEscape+likeEscape,
	"%", likeEscape+"%",
	"_", likeEscape+"_",
)

// containsPattern returns the LIKE pattern that matches s as a literal
// substring: s with !, % and _ escaped by likeEscape, wrapped in %.
func containsPattern(s string) string {
	return "%" + likePatternEscaper.Replace(s) + "%"
}

// likeAnyClause returns the parenthesised OR of `column LIKE ? ESCAPE '!'`
// over columns, one bound placeholder per column, for a containsPattern value.
func likeAnyClause(columns ...string) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = column + " LIKE ? ESCAPE '" + likeEscape + "'"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}
