package shared

import "errors"

// ErrLastLoginMethod — the change would leave a passwordless account (made through an outside app,
// 0.4.2 1b-2) with no way to log in: its last linked app can't be unlinked or moved away.
var ErrLastLoginMethod = errors.New("the last way to log in to an account without a password")
