package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

const (
	saltLen       = 16
	keyLen        = 32
	maxPassword   = 1024
	argonVersion  = argon2.Version
	maxMemoryKiB  = 256 * 1024
	maxIterations = 16
)

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func paramsFrom(c config.Argon2) argonParams {
	p := argonParams{memory: c.MemoryKiB, time: c.Iterations, threads: c.Parallelism}
	if p.memory < 8*1024 {
		p.memory = 19456
	}
	if p.time == 0 {
		p.time = 2
	}
	if p.threads == 0 {
		p.threads = 1
	}
	return p
}

// hasher owns the single global Argon2id slot (D19): at most one hash runs at
// a time so concurrent logins cannot multiply the 19 MiB working set.
type hasher struct {
	slot   chan struct{}
	params argonParams
	// dummySalt and dummyKey let logins for unknown usernames spend exactly
	// the time of a real verification without revealing that the user is
	// missing.
	dummySalt []byte
	dummyKey  []byte
}

func newHasher(p argonParams) *hasher {
	h := &hasher{slot: make(chan struct{}, 1), params: p, dummySalt: make([]byte, saltLen), dummyKey: make([]byte, keyLen)}
	_, _ = rand.Read(h.dummySalt)
	_, _ = rand.Read(h.dummyKey)
	return h
}

func (h *hasher) acquire(ctx context.Context) error {
	select {
	case h.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hasher) release() { <-h.slot }

func derive(pw string, salt []byte, p argonParams) []byte {
	return argon2.IDKey([]byte(pw), salt, p.time, p.memory, p.threads, keyLen)
}

// hash returns a PHC string: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>.
func (h *hasher) hash(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	k := derive(pw, salt, h.params)
	h.release()
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argonVersion, h.params.memory, h.params.time,
		h.params.threads, enc.EncodeToString(salt), enc.EncodeToString(k)), nil
}

// verifyHeld checks pw against a stored PHC string using the parameters
// recorded in it, so hashes made before a parameter change stay valid. The
// caller holds the slot.
func (h *hasher) verifyHeld(phc, pw string) (bool, error) {
	p, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(derive(pw, salt, p), want) == 1, nil
}

// dummyHeld spends the time of a verification; the caller holds the slot.
func (h *hasher) dummyHeld(pw string) {
	subtle.ConstantTimeCompare(derive(pw, h.dummySalt, h.params), h.dummyKey)
}

var errPHC = errors.New("auth: malformed password hash")

func parsePHC(s string) (argonParams, []byte, []byte, error) {
	var p argonParams
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argonVersion) {
		return p, nil, nil, errPHC
	}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return p, nil, nil, errPHC
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return p, nil, nil, errPHC
		}
		switch k {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.time = uint32(n)
		case "p":
			if n > 255 {
				return p, nil, nil, errPHC
			}
			p.threads = uint8(n)
		default:
			return p, nil, nil, errPHC
		}
	}
	// A tampered row must not be able to demand gigabytes of memory.
	if p.memory == 0 || p.memory > maxMemoryKiB || p.time == 0 || p.time > maxIterations || p.threads == 0 {
		return p, nil, nil, errPHC
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, errPHC
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, errPHC
	}
	return p, salt, key, nil
}

// checkPassword enforces auth.password_min_length and the common-password
// list. Messages are shown to the user verbatim.
func checkPassword(pw string, minLen int) error {
	if minLen <= 0 {
		minLen = 12
	}
	if len(pw) > maxPassword {
		return fmt.Errorf("%w: password is longer than %d bytes", core.ErrInvalid, maxPassword)
	}
	if !utf8.ValidString(pw) {
		return fmt.Errorf("%w: password is not valid UTF-8", core.ErrInvalid)
	}
	if utf8.RuneCountInString(pw) < minLen {
		return fmt.Errorf("%w: password must be at least %d characters", core.ErrInvalid, minLen)
	}
	if isCommonPassword(pw) {
		return fmt.Errorf("%w: this password is too common or predictable; a few unrelated words work well", core.ErrInvalid)
	}
	return nil
}
