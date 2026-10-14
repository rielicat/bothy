package github

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pkcs1PEM(k *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func TestParsePrivateKey(t *testing.T) {
	k := testKey(t)
	got, err := parsePrivateKey(pkcs1PEM(k))
	if err != nil || !got.Equal(k) {
		t.Fatalf("PKCS#1: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil || !got.Equal(k) {
		t.Fatalf("PKCS#8: %v", err)
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ec)
	for name, b := range map[string][]byte{
		"not pem":   []byte("ghp_notakey"),
		"ec key":    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"cert":      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}),
		"bad bytes": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1, 2, 3}}),
	} {
		if _, err := parsePrivateKey(b); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestAppJWT(t *testing.T) {
	k := testKey(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	tok, err := appJWT(k, "12345", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(hdr) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("header %s", hdr)
	}
	var claims map[string]any
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != float64(12345) || claims["iat"] != float64(now.Add(-time.Minute).Unix()) ||
		claims["exp"] != float64(now.Add(9*time.Minute).Unix()) {
		t.Errorf("claims %v", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature: %v", err)
	}
	// A client id is a string issuer.
	tok, _ = appJWT(k, "Iv1.ab1112223334445c", now)
	raw, _ = base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if !strings.Contains(string(raw), `"iss":"Iv1.ab1112223334445c"`) {
		t.Errorf("client id claims %s", raw)
	}
	if _, err := appJWT(nil, "1", now); err == nil {
		t.Error("nil key signed")
	}
}
