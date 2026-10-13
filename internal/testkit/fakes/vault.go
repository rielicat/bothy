package fakes

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

var (
	_ core.Vault    = (*Vault)(nil)
	_ core.Redactor = (*Redactor)(nil)
)

// Mask replaces every registered value in Redactor output.
const Mask = "[redacted]"

// Vault is an in-memory core.Vault with an exact-match Redactor. Seal() makes
// Get/Resolve return core.ErrSealed.
//
// It never reads BOTHY_SECRET_* env vars. Every successful Get or Resolve
// appends "<name>|<purpose>" to Uses (the fake of the secret.use audit).
type Vault struct {
	core.Vault
	Uses []string

	mu      sync.Mutex
	secrets map[core.Scope]map[string]vaultEntry
	sealed  bool
	red     *Redactor
}

type vaultEntry struct {
	value   []byte
	updated time.Time
}

// NewVault returns an unsealed vault holding secrets. A key "name" is stored
// in the global scope; "<scope>/<name>" (e.g. "user:usr_…/github") in that scope.
func NewVault(secrets map[string]string) *Vault {
	v := &Vault{secrets: map[core.Scope]map[string]vaultEntry{}, red: NewRedactor()}
	for k, val := range secrets {
		sc, name := core.GlobalScope, k
		if s, n, ok := strings.Cut(k, "/"); ok {
			sc, name = core.Scope(s), n
		}
		v.put(sc, name, []byte(val))
	}
	return v
}

func (v *Vault) put(sc core.Scope, name string, value []byte) {
	if v.secrets == nil {
		v.secrets = map[core.Scope]map[string]vaultEntry{}
	}
	if v.red == nil {
		v.red = NewRedactor()
	}
	if v.secrets[sc] == nil {
		v.secrets[sc] = map[string]vaultEntry{}
	}
	v.secrets[sc][name] = vaultEntry{value: bytes.Clone(value), updated: time.Now()}
	v.red.Add(string(sc)+"/"+name, value)
}

// Seal makes Get and Resolve fail with core.ErrSealed until Unseal.
func (v *Vault) Seal() { v.mu.Lock(); v.sealed = true; v.mu.Unlock() }

func (v *Vault) Put(_ context.Context, sc core.Scope, name string, value []byte, _ core.UserID) (core.SecretMeta, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.put(sc, name, value)
	return meta(sc, name, v.secrets[sc][name]), nil
}

// Get looks in sc and, for a user scope, falls back to the global scope.
func (v *Vault) Get(_ context.Context, sc core.Scope, name, purpose string) (core.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sealed {
		return core.Secret{}, core.ErrSealed
	}
	e, ok := v.lookup(sc, name)
	if !ok {
		return core.Secret{}, fmt.Errorf("secret %q: %w", name, core.ErrNotFound)
	}
	v.Uses = append(v.Uses, name+"|"+purpose)
	return core.NewSecret(bytes.Clone(e.value)), nil
}

func (v *Vault) lookup(sc core.Scope, name string) (vaultEntry, bool) {
	if e, ok := v.secrets[sc][name]; ok {
		return e, true
	}
	e, ok := v.secrets[core.GlobalScope][name]
	return e, ok
}

// Resolve substitutes the "secret:<name>" reference inside ref with the value
// (user scope of u first, then global; u == "" means global only).
func (v *Vault) Resolve(_ context.Context, ref string, u core.UserID, purpose string) (core.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sealed {
		return core.Secret{}, core.ErrSealed
	}
	start := strings.Index(ref, "secret:")
	if start < 0 {
		// The input may itself be a plaintext secret, so it is never echoed.
		return core.Secret{}, fmt.Errorf("%w: not a secret:<name> reference", core.ErrInvalid)
	}
	nameStart := start + len("secret:")
	end := nameStart
	for end < len(ref) && isNameByte(ref[end]) {
		end++
	}
	if end == nameStart {
		return core.Secret{}, fmt.Errorf("%w: empty secret name", core.ErrInvalid)
	}
	name := ref[nameStart:end]
	sc := core.GlobalScope
	if u != "" {
		sc = core.UserScope(u)
	}
	e, ok := v.lookup(sc, name)
	if !ok {
		return core.Secret{}, fmt.Errorf("secret %q: %w", name, core.ErrNotFound)
	}
	v.Uses = append(v.Uses, name+"|"+purpose)
	out := make([]byte, 0, len(ref)-len(name)-len("secret:")+len(e.value))
	out = append(out, ref[:start]...)
	out = append(out, e.value...)
	out = append(out, ref[end:]...)
	return core.NewSecret(out), nil
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

func (v *Vault) Delete(_ context.Context, sc core.Scope, name string, _ core.UserID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.secrets[sc][name]; !ok {
		return fmt.Errorf("secret %q: %w", name, core.ErrNotFound)
	}
	delete(v.secrets[sc], name)
	v.red.Remove(string(sc) + "/" + name)
	return nil
}

// List returns the scope's secrets sorted by name; Hint is the last 4 bytes.
func (v *Vault) List(_ context.Context, sc core.Scope) ([]core.SecretMeta, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]core.SecretMeta, 0, len(v.secrets[sc]))
	for name, e := range v.secrets[sc] {
		out = append(out, meta(sc, name, e))
	}
	slices.SortFunc(out, func(a, b core.SecretMeta) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func meta(sc core.Scope, name string, e vaultEntry) core.SecretMeta {
	hint := e.value
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	return core.SecretMeta{Scope: sc, Name: name, Source: "vault", Hint: string(hint), UpdatedAt: e.updated}
}

// Unseal accepts any passphrase.
func (v *Vault) Unseal(context.Context, []byte) error {
	v.mu.Lock()
	v.sealed = false
	v.mu.Unlock()
	return nil
}

func (v *Vault) RotateKEK(context.Context, core.UserID) error { return nil }

func (v *Vault) Status() core.VaultStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, m := range v.secrets {
		n += len(m)
	}
	return core.VaultStatus{Sealed: v.sealed, KEKSource: "file", KEKID: "00000000", Secrets: n}
}

func (v *Vault) Redactor() core.Redactor {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.red == nil {
		v.red = NewRedactor()
	}
	return v.red
}

// Redactor is an exact-match core.Redactor: it masks registered values only,
// without the encoded variants or token patterns of the real one.
type Redactor struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func NewRedactor() *Redactor { return &Redactor{values: map[string][]byte{}} }

func (r *Redactor) Add(id string, value []byte) {
	if len(value) == 0 {
		return
	}
	r.mu.Lock()
	if r.values == nil {
		r.values = map[string][]byte{}
	}
	r.values[id] = bytes.Clone(value)
	r.mu.Unlock()
}

func (r *Redactor) Remove(id string) {
	r.mu.Lock()
	delete(r.values, id)
	r.mu.Unlock()
}

func (r *Redactor) Redact(s string) string { return string(r.RedactBytes([]byte(s))) }

// RedactBytes masks longer values first so a value containing another is
// masked whole.
func (r *Redactor) RedactBytes(b []byte) []byte {
	r.mu.RLock()
	vals := make([][]byte, 0, len(r.values))
	for _, v := range r.values {
		vals = append(vals, v)
	}
	r.mu.RUnlock()
	slices.SortFunc(vals, func(a, b []byte) int { return cmp.Compare(len(b), len(a)) })
	out := bytes.Clone(b)
	for _, v := range vals {
		out = bytes.ReplaceAll(out, v, []byte(Mask))
	}
	return out
}

// PartialAt mirrors vault.Redactor.PartialAt for the raw values this fake
// masks: where the earliest suffix of s that a longer value starts with
// begins, or len(s).
func (r *Redactor) PartialAt(s string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range len(s) {
		for _, v := range r.values {
			if len(v) > len(s)-i && strings.HasPrefix(string(v), s[i:]) {
				return i
			}
		}
	}
	return len(s)
}
