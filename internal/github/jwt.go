package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// parsePrivateKey reads the PEM private key GitHub generates for an App
// (PKCS#1 "RSA PRIVATE KEY"; PKCS#8 is accepted too).
func parsePrivateKey(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%w: the App private key is not PEM (paste the .pem file GitHub generated)", core.ErrInvalid)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: the App private key does not parse: %w", core.ErrInvalid, err)
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: the App private key does not parse: %w", core.ErrInvalid, err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: GitHub App keys are RSA, got %T", core.ErrInvalid, k)
		}
		return rk, nil
	}
	return nil, fmt.Errorf("%w: unexpected PEM block %q in the App private key", core.ErrInvalid, block.Type)
}

// appJWT signs the RS256 JWT GitHub accepts for App endpoints: iat 60 s in
// the past against clock drift, exp 9 minutes ahead (GitHub caps it at 10),
// iss the app id (a JSON number) or client id (a string).
func appJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("github: no App private key")
	}
	var iss any = appID
	if n, err := strconv.ParseInt(appID, 10, 64); err == nil {
		iss = n
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": iss,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign App JWT: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
