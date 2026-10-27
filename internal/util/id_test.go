package util

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestRandomHexLengthAndAlphabet(t *testing.T) {
	t.Parallel()

	for _, length := range []int{1, 2, 7, 32, 48} {
		value, err := RandomHex(length)
		if err != nil {
			t.Fatalf("RandomHex(%d) error = %v", length, err)
		}
		if len(value) != length {
			t.Errorf("RandomHex(%d) returned %d characters", length, len(value))
		}
		if _, err := hex.DecodeString(value); err != nil && length%2 == 0 {
			t.Errorf("RandomHex(%d) = %q, want hex", length, value)
		}
	}
}

func TestRandomHexIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 64)
	for index := 0; index < 64; index++ {
		value, err := RandomHex(16)
		if err != nil {
			t.Fatalf("RandomHex() error = %v", err)
		}
		if seen[value] {
			t.Fatalf("RandomHex() repeated %q", value)
		}
		seen[value] = true
	}
}

func TestRandomHexRejectsNonPositiveLength(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, -1} {
		value, err := RandomHex(length)
		if err != nil || value != "" {
			t.Errorf("RandomHex(%d) = %q, %v; want empty and no error", length, value, err)
		}
	}
}

func TestCorrelationHexLength(t *testing.T) {
	t.Parallel()

	if value := CorrelationHex(24); len(value) != 24 {
		t.Fatalf("CorrelationHex(24) = %q, want 24 characters", value)
	}
	if value := CorrelationHex(-3); value != "" {
		t.Fatalf("CorrelationHex(-3) = %q, want empty", value)
	}
	if strings.Trim(CorrelationHex(16), "0123456789abcdef") != "" {
		t.Fatal("CorrelationHex() returned a non-hex value")
	}
}
