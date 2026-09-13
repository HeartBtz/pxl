// Package crypto fournit les primitives cryptographiques de PXL :
//   - Signature et vérification d'URLs privées via HMAC-SHA256
//   - Génération de short IDs cryptographiquement sûrs (base62)
//   - Génération de tokens de suppression
//   - Hachage SHA-256 pour les clés API
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// URLSigner creates and verifies HMAC-SHA256 signed URLs for private images.
type URLSigner struct {
	secret []byte
}

func NewURLSigner(secret string) *URLSigner {
	return &URLSigner{secret: []byte(secret)}
}

// Sign produces a signed URL that expires at the given time.
func (s *URLSigner) Sign(rawURL string, expires time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	q.Set("expires", strconv.FormatInt(expires.Unix(), 10))

	sigData := u.Path + "\n" + strconv.FormatInt(expires.Unix(), 10)
	sig := s.computeHMAC(sigData)
	q.Set("sig", sig)

	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Verify checks that the signed URL is valid and not expired.
func (s *URLSigner) Verify(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	expiresStr := q.Get("expires")
	sig := q.Get("sig")

	if expiresStr == "" || sig == "" {
		return fmt.Errorf("missing signature parameters")
	}

	expiresUnix, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiration: %w", err)
	}
	if time.Now().Unix() > expiresUnix {
		return fmt.Errorf("signed URL has expired")
	}

	sigData := u.Path + "\n" + expiresStr
	expected := s.computeHMAC(sigData)
	if !constantTimeEqual(sig, expected) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func (s *URLSigner) computeHMAC(data string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// HashAPIKey computes SHA256 hash of an API key for storage / lookup.
func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// ExtensionFromMIME maps common image MIME types to file extensions.
func ExtensionFromMIME(mime string) string {
	switch strings.TrimSpace(strings.ToLower(mime)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	case "image/bmp":
		return ".bmp"
	default:
		return ".bin"
	}
}
