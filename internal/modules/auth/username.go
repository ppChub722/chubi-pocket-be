package auth

import (
	"regexp"
	"strings"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,50}$`)

// ErrInvalidUsername — a 400 on username (register, PUT /users/me).
var ErrInvalidUsername = apierr.Invalid("username must be 3-50 characters of A-Z a-z 0-9 _ - . and cannot start or end with a dot or hold two dots in a row",
	"username", apierr.FieldInvalid, "format", "username")

// ValidUsername — A-Z a-z 0-9 _ - . , 3-50 characters; a dot never first, last or doubled. Case counts:
// "Alice" and "alice" are two names.
func ValidUsername(s string) bool {
	return usernameRe.MatchString(s) &&
		!strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".") && !strings.Contains(s, "..")
}
