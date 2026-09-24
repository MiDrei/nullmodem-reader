package store

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// randomSuffix returns six characters of lowercase base32, enough to
// keep two replies written within the same second apart.
//
// crypto/rand rather than math/rand: not because the value is a
// secret, but because it needs no seeding and cannot repeat a
// sequence across two processes started at the same moment -- which
// is exactly the collision this suffix exists to prevent.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a queue entry is
		// not worth refusing over a suffix: the timestamp alone still
		// identifies it, at the cost of a same-second collision.
		return "000000"
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))[:6]
}
