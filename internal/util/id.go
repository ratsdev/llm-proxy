package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// RandomHex returns length lowercase hex characters derived from crypto/rand.
//
// It reports an error instead of returning a placeholder. Callers use the
// result as a credential (API key, console session token) or as an identifier a
// client may trust, so a silent degradation would turn a failed entropy source
// into an authentication bypass. Identifiers that only correlate protocol
// events use CorrelationHex instead.
func RandomHex(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	raw := make([]byte, (length+1)/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random identifier: %w", err)
	}
	return hex.EncodeToString(raw)[:length], nil
}

// CorrelationHex is RandomHex for identifiers that only correlate one reply
// (chatcmpl-, msg_, toolu_ and friends). A failed entropy source must not abort
// a response that is otherwise usable, so it degrades to zeroes; unlike
// RandomHex, a value from this function is never accepted as a credential.
func CorrelationHex(length int) string {
	value, err := RandomHex(length)
	if err != nil || len(value) != length {
		return strings.Repeat("0", max(length, 0))
	}
	return value
}
