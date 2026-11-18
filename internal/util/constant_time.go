package util

import (
	"crypto/sha256"
	"crypto/subtle"
)

// ConstantTimeStringEqual compares two secrets without leaking length through timing.
// Both values are hashed to a fixed-size digest before constant-time comparison.
func ConstantTimeStringEqual(a, b string) bool {
	ad := sha256.Sum256([]byte(a))
	bd := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ad[:], bd[:]) == 1
}
