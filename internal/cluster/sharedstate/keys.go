package sharedstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrUnavailable is reported while the shared state cannot be reached. Callers
// treat it like any other failure and fall back to their node-local behaviour.
var ErrUnavailable = errors.New("sharedstate: shared state unavailable")

// HashPart turns identifiers into a fixed-length key segment.
//
// Several identifiers must not appear in key names: API keys are secrets, and
// session keys can be derived from request content, and the shared store may be
// inspected by whoever operates the database, so key names only ever carry this
// digest. 128 bits keep collisions out of reach while halving the key size.
func HashPart(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}
