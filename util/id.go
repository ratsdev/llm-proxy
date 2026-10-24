package util

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// RandomHex returns a cryptographically random lowercase hex string of the
// requested length. It falls back to zeros only when the system random source
// fails, so identifiers stay structurally valid.
func RandomHex(length int) string {
	raw := make([]byte, (length+1)/2)
	if _, err := rand.Read(raw); err != nil {
		return strings.Repeat("0", length)
	}
	encoded := hex.EncodeToString(raw)
	if len(encoded) > length {
		encoded = encoded[:length]
	}
	return encoded
}
