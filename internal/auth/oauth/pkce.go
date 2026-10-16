package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewPKCE returns an RFC 7636 code verifier (32 random bytes, 43 base64url
// characters) and its S256 challenge.
func NewPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, Challenge(verifier)
}

// Challenge is the S256 code challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
