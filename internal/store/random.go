package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomToken returns an n-character lowercase alphanumeric token.
func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = tokenAlphabet[int(b)%len(tokenAlphabet)]
	}
	return string(out)
}

// RandomHex returns n random bytes hex-encoded.
func RandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

// RandomBase64 returns n random bytes base64url-encoded (no padding).
func RandomBase64(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// HashToken returns the hex SHA-256 of a secret. API keys and session cookies
// are stored only as hashes.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings without leaking timing information.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
