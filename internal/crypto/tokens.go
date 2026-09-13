package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"strings"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateShortID generates a cryptographically random base62 string
// of the given length. 8 chars → 62^8 ≈ 218 trillion combinations.
func GenerateShortID(length int) string {
	var sb strings.Builder
	sb.Grow(length)
	max := big.NewInt(int64(len(base62Chars)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// Fallback: extremely unlikely
			sb.WriteByte(base62Chars[0])
			continue
		}
		sb.WriteByte(base62Chars[n.Int64()])
	}
	return sb.String()
}

// GenerateDeleteToken produces a 32-byte random token encoded as base64url.
func GenerateDeleteToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return GenerateShortID(43) // fallback
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// GenerateToken generates a generic random token of the specified byte length.
func GenerateToken(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return GenerateShortID(length)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
