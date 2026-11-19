package database

import "strings"

var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

// ContainsPattern matches a literal substring in a PostgreSQL LIKE or ILIKE predicate.
func ContainsPattern(term string) string {
	return "%" + likeEscaper.Replace(term) + "%"
}
