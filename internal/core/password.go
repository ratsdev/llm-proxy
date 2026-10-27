package core

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	// MinPasswordLength is the shortest password accepted for a console account.
	MinPasswordLength  = 8
	passwordIterations = 210_000

	passwordSaltBytes = 16
	passwordKeyBytes  = 32
)

// hashPassword derives a PBKDF2-HMAC-SHA256 verifier. The encoding is
// self-describing so the iteration count can be raised later without breaking
// existing records.
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive password verifier: %w", err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		passwordIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// HashPassword exposes password hashing to administrative tooling.
func HashPassword(password string) (string, error) {
	return hashPassword(password)
}

// verifyPassword compares a plaintext password with a stored verifier in
// constant time.
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// VerifyPassword exposes password verification to the console handlers.
func VerifyPassword(encoded, password string) bool {
	return verifyPassword(encoded, password)
}
