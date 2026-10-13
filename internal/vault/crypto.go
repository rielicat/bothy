package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/rielicat/bothy/internal/core"
)

const (
	keySize      = 32
	kekCheckText = "bothy-kek-check"
)

var errDecrypt = errors.New("vault: decryption failed")

func newKey() []byte {
	k := make([]byte, keySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("vault: key must be %d bytes", keySize)
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// seal encrypts with a fresh random 96-bit nonce.
func seal(key, plaintext, aad []byte) (nonce, ct []byte, err error) {
	a, err := gcm(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, a.Seal(nil, nonce, plaintext, aad), nil
}

func open(key, nonce, ct, aad []byte) ([]byte, error) {
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != a.NonceSize() {
		return nil, errDecrypt
	}
	pt, err := a.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errDecrypt
	}
	return pt, nil
}

// wrapBlob is the vault_keys encoding: nonce || AES-256-GCM(kek, plaintext).
func wrapBlob(kek, plaintext []byte) ([]byte, error) {
	nonce, ct, err := seal(kek, plaintext, nil)
	if err != nil {
		return nil, err
	}
	return append(nonce, ct...), nil
}

func unwrapBlob(kek, blob []byte) ([]byte, error) {
	a, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	if len(blob) < a.NonceSize() {
		return nil, errDecrypt
	}
	return open(kek, blob[:a.NonceSize()], blob[a.NonceSize():], nil)
}

// kekMatches reports whether kek decrypts the stored kek_check.
func kekMatches(kek, check []byte) bool {
	pt, err := unwrapBlob(kek, check)
	return err == nil && string(pt) == kekCheckText
}

// secretAAD binds a ciphertext to its row: scope || 0 || name || 0 || version.
func secretAAD(sc core.Scope, name string, version int) []byte {
	b := make([]byte, 0, len(sc)+len(name)+12)
	b = append(b, sc...)
	b = append(b, 0)
	b = append(b, name...)
	b = append(b, 0)
	return strconv.AppendInt(b, int64(version), 10)
}

func kekID(kek []byte) string {
	sum := sha256.Sum256(kek)
	return hex.EncodeToString(sum[:])[:8]
}

// parseKEK accepts a KEK file or env value as 64 hex characters or base64 of
// 32 bytes (surrounding whitespace ignored), or exactly 32 raw bytes.
func parseKEK(b []byte) ([]byte, error) {
	s := strings.TrimSpace(string(b))
	if k, err := hex.DecodeString(s); err == nil && len(k) == keySize {
		return k, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil && len(k) == keySize {
			return k, nil
		}
	}
	if len(b) == keySize {
		return append([]byte(nil), b...), nil
	}
	return nil, errors.New("a KEK must be 32 bytes: 64 hex characters, base64, or raw")
}

// Argon2id parameters for passphrase KEKs: the OWASP minimum Bothy also uses
// for passwords (D19).
const (
	argonMemoryKiB = 19456
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
)

// kdfParams is stored in meta under kdfMetaKey; it carries the salt and cost
// so a later release can raise the cost without breaking existing vaults.
func kdfParams(salt []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s", argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt))
}

func deriveKEK(passphrase []byte, params string) ([]byte, error) {
	parts := strings.Split(params, "$")
	if len(parts) != 5 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return nil, fmt.Errorf("vault: unsupported KDF parameters %q", params)
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 16 || p == 0 {
		return nil, fmt.Errorf("vault: bad KDF cost %q", parts[3])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return nil, fmt.Errorf("vault: bad KDF salt")
	}
	return argon2.IDKey(passphrase, salt, t, m, p, keySize), nil
}
