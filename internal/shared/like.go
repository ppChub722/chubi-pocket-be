package shared

import "strings"

// EscapeLike makes % and _ in user input match literally inside a LIKE /
// ILIKE pattern (Postgres' default escape character is backslash).
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
