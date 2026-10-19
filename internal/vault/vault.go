package vault

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	secretEnvPrefix = "BOTHY_SECRET_"
	maxSecretSize   = 64 << 10
	// kdfMetaKey holds the Argon2id parameters and salt of a passphrase KEK.
	kdfMetaKey = "vault.kek_kdf"
	// minPassphrase applies when the first Unseal sets the passphrase.
	minPassphrase = 12
	// touchEvery throttles last_used_at writes: every Get would otherwise be a
	// database write.
	touchEvery = time.Minute
	// hintMinLen keeps the last-four hint from revealing much of a short value.
	hintMinLen = 12
)

// errBadName never quotes the name: a caller that mixed up arguments could
// otherwise copy a plaintext secret into an error message and the logs.
var errBadName = fmt.Errorf("vault: a secret name is 1-64 characters of a-z 0-9 _ . -: %w", core.ErrInvalid)

func (v *Vault) setSealed(p kekPlan, reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.plan, v.sealed, v.reason = p, true, reason
	clear(v.kek)
	clear(v.dek)
	v.kek, v.dek, v.keyID = nil, nil, 0
}

func (v *Vault) setOpen(p kekPlan, kek, dek []byte, keyID int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.plan, v.sealed, v.reason = p, false, ""
	v.kek, v.dek, v.keyID = kek, dek, keyID
	v.red.Add("vault:kek", kek)
	v.red.Add("vault:dek", dek)
}

// key returns the active DEK and its id, or ErrSealed.
func (v *Vault) key() ([]byte, int64, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealed || v.dek == nil {
		return nil, 0, core.ErrSealed
	}
	return v.dek, v.keyID, nil
}

// loadEnv keeps BOTHY_SECRET_<NAME> values (or the contents of the file named
// by BOTHY_SECRET_<NAME>_FILE) in memory only. A _FILE suffix always means a
// file reference.
func (v *Vault) loadEnv(env []string) {
	files := map[string]string{}
	for _, kv := range env {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		v.envVars[k] = val
		name, ok := strings.CutPrefix(k, secretEnvPrefix)
		if !ok || name == "" {
			continue
		}
		if base, isFile := strings.CutSuffix(name, "_FILE"); isFile && base != "" {
			files[base] = val
			continue
		}
		if val != "" {
			v.envSecs[name] = []byte(val)
		}
	}
	for name, p := range files {
		if _, ok := v.envSecs[name]; ok {
			v.log.Warn("both "+secretEnvPrefix+name+" and its _FILE form are set; using the value", "name", name)
			continue
		}
		b, err := readSecretFile(p)
		if err != nil {
			v.log.Error("cannot read secret file", "env", secretEnvPrefix+name+"_FILE", "err", err)
			continue
		}
		v.envSecs[name] = b
	}
	for name, val := range v.envSecs {
		v.red.Add("env:"+name, val)
	}
	if k := v.envVars[kekEnv]; k != "" {
		v.red.Add("env:"+kekEnv, []byte(k))
	}
}

func readSecretFile(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSecretSize {
		return nil, fmt.Errorf("%s: not a regular file of at most %d bytes", p, maxSecretSize)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	if len(b) == 0 {
		return nil, fmt.Errorf("%s: empty", p)
	}
	return b, nil
}

func (v *Vault) envVar(k string) string { return v.envVars[k] }

// envName is the BOTHY_SECRET_ suffix for a vault name: upper case with '.'
// and '-' replaced by '_'.
func envName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '.' || r == '-':
			return '_'
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		}
		return r
	}, name)
}

// validName mirrors the vault_secrets.name CHECK.
func validName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return false
		}
	}
	return true
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// validScope accepts "global" and "user:<usr_ id>". Spaces have no scope.
func validScope(sc core.Scope) error {
	if sc == core.GlobalScope {
		return nil
	}
	if u, ok := strings.CutPrefix(string(sc), "user:"); ok && core.ValidID(core.PrefixUser, u) {
		return nil
	}
	return fmt.Errorf("vault: scope %q: %w", sc, core.ErrInvalid)
}

func redactID(sc core.Scope, name string) string { return string(sc) + "/" + name }

func hint(value []byte) string {
	if utf8.RuneCount(value) < hintMinLen {
		return ""
	}
	r := []rune(string(value))
	return string(r[len(r)-4:])
}

func actor(by core.UserID) string {
	if by == "" {
		return "system"
	}
	return "user:" + string(by)
}

func (v *Vault) record(ctx context.Context, action, actor, target, outcome string, detail map[string]any) error {
	if v.audit == nil {
		return nil
	}
	var ip string
	if o, ok := core.AuditOriginFrom(ctx); ok {
		actor, ip = cmp.Or(o.Actor, actor), o.IP
		if len(o.Detail) > 0 {
			merged := maps.Clone(o.Detail)
			maps.Copy(merged, detail)
			detail = merged
		}
	}
	d, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return v.audit.Record(ctx, core.AuditEntry{At: v.clock.Now(), Actor: actor, Action: action, Target: target, Outcome: outcome, IP: ip, Detail: d})
}

// loadSecrets counts the stored secrets and, when the vault is open,
// registers every plaintext with the redactor. Scopes are global plus each
// user's: the only scopes Put accepts.
func (v *Vault) loadSecrets(ctx context.Context) {
	_, _, keyErr := v.key()
	scopes := []core.Scope{core.GlobalScope}
	users, err := v.store.Users().List(ctx)
	if err != nil {
		v.log.Error("vault: listing users for the redactor", "err", err)
	}
	for _, u := range users {
		scopes = append(scopes, core.UserScope(u.ID))
	}
	n := 0
	for _, sc := range scopes {
		metas, err := v.store.Vault().ListSecrets(ctx, sc)
		if err != nil {
			v.log.Error("vault: listing secrets for the redactor", "scope", sc, "err", err)
			continue
		}
		for _, m := range metas {
			n++
			if keyErr != nil {
				continue
			}
			val, err := v.decrypt(ctx, sc, m.Name)
			if err != nil {
				v.log.Error("vault: secret cannot be decrypted", "scope", sc, "name", m.Name, "err", err)
				continue
			}
			v.red.Add(redactID(sc, m.Name), val)
		}
	}
	v.mu.Lock()
	v.count = n
	v.mu.Unlock()
}

func (v *Vault) decrypt(ctx context.Context, sc core.Scope, name string) ([]byte, error) {
	dek, active, err := v.key()
	if err != nil {
		return nil, err
	}
	keyID, version, nonce, ct, err := v.store.Vault().GetSecret(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	if keyID != active {
		return nil, fmt.Errorf("vault: secret %q is encrypted under retired key %d", name, keyID)
	}
	pt, err := open(dek, nonce, ct, secretAAD(sc, name, version))
	if err != nil {
		return nil, fmt.Errorf("vault: secret %q: %w", name, err)
	}
	return pt, nil
}

// Get resolves BOTHY_SECRET_<NAME> first, then sc, falling back from a user
// scope to global. Each use is audited as secret.use before the value is
// returned, so an audit failure withholds the secret.
func (v *Vault) Get(ctx context.Context, sc core.Scope, name, purpose string) (core.Secret, error) {
	if err := validScope(sc); err != nil {
		return core.Secret{}, err
	}
	if !validName(name) {
		return core.Secret{}, errBadName
	}
	// Without an auditor a use could not be recorded, so nothing is released.
	if v.audit == nil {
		return core.Secret{}, errors.New("vault: no auditor to record secret.use")
	}
	if val, ok := v.envSecs[envName(name)]; ok {
		if err := v.record(ctx, "secret.use", "system", name, "ok",
			map[string]any{"name": name, "purpose": purpose, "source": "env"}); err != nil {
			return core.Secret{}, err
		}
		return core.NewSecret(bytes.Clone(val)), nil
	}
	v.keyMu.RLock()
	defer v.keyMu.RUnlock()
	if _, _, err := v.key(); err != nil {
		return core.Secret{}, err
	}
	found := sc
	val, err := v.decrypt(ctx, sc, name)
	if errors.Is(err, core.ErrNotFound) && sc != core.GlobalScope {
		found = core.GlobalScope
		val, err = v.decrypt(ctx, found, name)
	}
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return core.Secret{}, fmt.Errorf("vault: secret %q: %w", name, core.ErrNotFound)
		}
		return core.Secret{}, err
	}
	if err := v.record(ctx, "secret.use", "system", name, "ok",
		map[string]any{"name": name, "purpose": purpose, "scope": string(found), "source": "vault"}); err != nil {
		return core.Secret{}, err
	}
	v.touch(ctx, found, name)
	return core.NewSecret(val), nil
}

func (v *Vault) touch(ctx context.Context, sc core.Scope, name string) {
	now := v.clock.Now()
	k := redactID(sc, name)
	v.mu.Lock()
	last, ok := v.touched[k]
	if ok && now.Sub(last) < touchEvery {
		v.mu.Unlock()
		return
	}
	v.touched[k] = now
	v.mu.Unlock()
	if err := v.store.Vault().TouchSecret(ctx, sc, name, now); err != nil {
		v.log.Warn("vault: recording last use", "name", name, "err", err)
	}
}

// Resolve substitutes every "secret:<name>" reference in ref (user scope of u,
// then global; u == "" means global only) and every "aws-sm:" reference (AWS
// Secrets Manager; the same for every u, since it names a company secret
// that the configuration, not the user, chose). Input without a reference
// is rejected and never echoed: it may itself be a plaintext secret. A
// resolved value is never resolved again, so a vault value that looks like
// a reference stays text.
func (v *Vault) Resolve(ctx context.Context, ref string, u core.UserID, purpose string) (core.Secret, error) {
	sc := core.GlobalScope
	if u != "" {
		sc = core.UserScope(u)
	}
	spans, err := findRefs(ref)
	if err != nil {
		return core.Secret{}, err
	}
	if len(spans) == 0 {
		return core.Secret{}, fmt.Errorf("vault: not a secret:<name> or aws-sm:<secret> reference: %w", core.ErrInvalid)
	}
	var out []byte
	last := 0
	for _, sp := range spans {
		var s core.Secret
		if sp.aws != nil {
			s, err = v.getAWS(ctx, *sp.aws, purpose)
		} else {
			s, err = v.Get(ctx, sc, sp.name, purpose)
		}
		if err != nil {
			return core.Secret{}, err
		}
		out = append(out, ref[last:sp.start]...)
		out = append(out, s.Bytes()...)
		last = sp.end
	}
	return core.NewSecret(append(out, ref[last:]...)), nil
}

// refSpan is one reference inside a Resolve input.
type refSpan struct {
	start, end int
	name       string               // secret:<name>
	aws        *config.AWSSecretRef // aws-sm:…
}

// findRefs lists the references in s in order. "secret:" inside an aws-sm
// reference (a secret ARN has ":secret:") is part of that reference.
func findRefs(s string) ([]refSpan, error) {
	const marker = "secret:"
	awsRefs, err := config.FindAWSSecretRefs(s)
	if err != nil {
		return nil, fmt.Errorf("vault: %s: %w", err.Error(), core.ErrInvalid)
	}
	var out []refSpan
	for _, a := range awsRefs {
		out = append(out, refSpan{start: a.Start, end: a.End, aws: &a.Ref})
	}
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], marker)
		if j < 0 {
			break
		}
		start := i + j
		if k := slices.IndexFunc(awsRefs, func(a config.AWSSecretRefAt) bool { return start >= a.Start && start < a.End }); k >= 0 {
			i = awsRefs[k].End
			continue
		}
		if start > 0 && (isNameByte(s[start-1]) || isUpper(s[start-1])) {
			i = start + len(marker)
			continue
		}
		end := start + len(marker)
		for end < len(s) && isNameByte(s[end]) {
			end++
		}
		name := s[start+len(marker) : end]
		if name == "" {
			return nil, fmt.Errorf("vault: empty secret reference: %w", core.ErrInvalid)
		}
		out = append(out, refSpan{start: start, end: end, name: name})
		i = end
	}
	slices.SortFunc(out, func(a, b refSpan) int { return a.start - b.start })
	// Resolve splices the spans in order; overlapping ones would slice backwards.
	for i := 1; i < len(out); i++ {
		if out[i].start < out[i-1].end {
			return nil, fmt.Errorf("vault: overlapping secret references: %w", core.ErrInvalid)
		}
	}
	return out, nil
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// plainErr is an ErrInvalid whose message is a sentence for the person
// unlocking the keys, without the sentinel's word.
type plainErr string

func (e plainErr) Error() string { return string(e) }
func (e plainErr) Unwrap() error { return core.ErrInvalid }

func plainInvalid(msg string) error { return plainErr(msg) }

func (v *Vault) Put(ctx context.Context, sc core.Scope, name string, value []byte, by core.UserID) (core.SecretMeta, error) {
	if err := validScope(sc); err != nil {
		return core.SecretMeta{}, err
	}
	if !validName(name) {
		return core.SecretMeta{}, errBadName
	}
	if len(value) == 0 || len(value) > maxSecretSize {
		return core.SecretMeta{}, fmt.Errorf("vault: secret value must be 1 to %d bytes: %w", maxSecretSize, core.ErrInvalid)
	}
	v.keyMu.RLock()
	defer v.keyMu.RUnlock()
	dek, keyID, err := v.key()
	if err != nil {
		return core.SecretMeta{}, err
	}
	h := hint(value)
	created := false
	err = v.store.Tx(ctx, func(tx core.Store) error {
		_, version, _, _, err := tx.Vault().GetSecret(ctx, sc, name)
		switch {
		case errors.Is(err, core.ErrNotFound):
			created = true
		case err != nil:
			return err
		}
		nonce, ct, err := seal(dek, value, secretAAD(sc, name, version+1))
		if err != nil {
			return err
		}
		return tx.Vault().PutSecret(ctx, sc, name, keyID, version+1, nonce, ct, h, by)
	})
	if err != nil {
		return core.SecretMeta{}, err
	}
	v.red.Retire(redactID(sc, name))
	v.red.Add(redactID(sc, name), value)
	v.gen.Add(1)
	if created {
		v.mu.Lock()
		v.count++
		v.mu.Unlock()
	}
	meta := core.SecretMeta{Scope: sc, Name: name, Source: "vault", Hint: h, UpdatedAt: time.UnixMilli(v.clock.Now().UnixMilli()).UTC()}
	if err := v.record(ctx, "vault.put", actor(by), redactID(sc, name), "ok",
		map[string]any{"scope": string(sc), "name": name, "created": created}); err != nil {
		return meta, err
	}
	return meta, nil
}

func (v *Vault) Delete(ctx context.Context, sc core.Scope, name string, by core.UserID) error {
	if err := validScope(sc); err != nil {
		return err
	}
	if strings.HasPrefix(name, config.AWSSecretPrefix) {
		return fmt.Errorf("vault: an aws-sm reference is not stored in the vault; remove it from the configuration or the GitHub connection that uses it: %w", core.ErrInvalid)
	}
	if !validName(name) {
		return errBadName
	}
	if err := v.store.Vault().DeleteSecret(ctx, sc, name); err != nil {
		return err
	}
	v.red.Retire(redactID(sc, name))
	v.gen.Add(1)
	v.mu.Lock()
	v.count = max(v.count-1, 0)
	delete(v.touched, redactID(sc, name))
	v.mu.Unlock()
	return v.record(ctx, "vault.delete", actor(by), redactID(sc, name), "ok", map[string]any{"scope": string(sc), "name": name})
}

// Generation changes whenever a stored key is put or deleted, so a holder of
// resolved values (the providers) knows to read them again.
func (v *Vault) Generation() uint64 { return v.gen.Load() }

// List returns names and hints, never values. Environment secrets apply to
// every scope but are listed once, under the global scope, by the lower-cased
// variable suffix. The global scope also lists the aws-sm references the
// server makes (company secrets, so never under a user's scope), with no
// hint: their values are not Bothy's to show.
func (v *Vault) List(ctx context.Context, sc core.Scope) ([]core.SecretMeta, error) {
	if err := validScope(sc); err != nil {
		return nil, err
	}
	out, err := v.store.Vault().ListSecrets(ctx, sc)
	if err != nil {
		return nil, err
	}
	if sc == core.GlobalScope {
		for _, name := range slices.Sorted(maps.Keys(v.envSecs)) {
			out = append(out, core.SecretMeta{Scope: core.GlobalScope, Name: strings.ToLower(name), Source: "env", Hint: hint(v.envSecs[name])})
		}
		out = append(out, v.awsMetas(ctx)...)
	}
	if out == nil {
		out = []core.SecretMeta{}
	}
	return out, nil
}

// Unseal opens a passphrase vault. The first Unseal sets the passphrase: it
// creates the salt and the DEK. A wrong passphrase is ErrForbidden.
func (v *Vault) Unseal(ctx context.Context, passphrase []byte) error {
	v.keyMu.Lock()
	defer v.keyMu.Unlock()
	v.mu.RLock()
	plan, sealed, reason := v.plan, v.sealed, v.reason
	v.mu.RUnlock()
	if !sealed {
		return nil
	}
	if plan.source != srcPassphrase {
		v.log.Warn("vault: unseal refused", "reason", reason)
		return plainInvalid("A passphrase can't unlock these saved keys: they are locked because Bothy's key file is missing or doesn't match. " +
			"Put the key file Bothy was set up with back on the server, then restart Bothy.")
	}
	if len(passphrase) == 0 {
		return plainInvalid("Enter the passphrase.")
	}
	id, wrapped, check, _, err := v.store.Vault().ActiveKey(ctx)
	if errors.Is(err, core.ErrNotFound) {
		return v.initPassphrase(ctx, plan, passphrase)
	}
	if err != nil {
		return err
	}
	params, err := v.store.Meta().Get(ctx, kdfMetaKey)
	if err != nil {
		return fmt.Errorf("vault: passphrase parameters: %w", err)
	}
	kek, err := deriveKEK(passphrase, params)
	if err != nil {
		return err
	}
	if !kekMatches(kek, check) {
		if rerr := v.record(ctx, "vault.unseal", "system", "vault", "denied", map[string]any{"reason": "wrong passphrase"}); rerr != nil {
			v.log.Error("vault: auditing a failed unseal", "err", rerr)
		}
		return fmt.Errorf("vault: wrong passphrase: %w", core.ErrForbidden)
	}
	dek, err := unwrapBlob(kek, wrapped)
	if err != nil {
		return fmt.Errorf("vault: the wrapped DEK could not be decrypted: %w", err)
	}
	v.setOpen(plan, kek, dek, id)
	v.loadSecrets(ctx)
	return v.record(ctx, "vault.unseal", "system", "vault", "ok", map[string]any{"kek_id": kekID(kek)})
}

func (v *Vault) initPassphrase(ctx context.Context, plan kekPlan, passphrase []byte) error {
	if utf8.RuneCount(passphrase) < minPassphrase {
		return plainInvalid(fmt.Sprintf("This sets the passphrase for the saved keys, so it needs at least %d characters.", minPassphrase))
	}
	params := kdfParams(newKey()[:argonSaltLen])
	kek, err := deriveKEK(passphrase, params)
	if err != nil {
		return err
	}
	dek := newKey()
	wrapped, err := wrapBlob(kek, dek)
	if err != nil {
		return err
	}
	check, err := wrapBlob(kek, []byte(kekCheckText))
	if err != nil {
		return err
	}
	var id int64
	if err := v.store.Tx(ctx, func(tx core.Store) error {
		if err := tx.Meta().Set(ctx, kdfMetaKey, params); err != nil {
			return err
		}
		id, err = tx.Vault().PutKey(ctx, wrapped, check, srcPassphrase)
		return err
	}); err != nil {
		return err
	}
	v.setOpen(plan, kek, dek, id)
	return v.record(ctx, "vault.unseal", "system", "vault", "ok", map[string]any{"kek_id": kekID(kek), "initialised": true})
}

// RotateKEK replaces the DEK and re-encrypts every secret in one transaction.
// With the auto-generated keyfile the KEK is replaced too: the new keyfile is
// written to <keyfile>.new first and renamed after the commit (boot finishes
// the rename if it was interrupted). Externally managed KEKs (Docker secret,
// systemd, file, env, passphrase) stay as they are; only the DEK rotates.
func (v *Vault) RotateKEK(ctx context.Context, by core.UserID) error {
	v.keyMu.Lock()
	defer v.keyMu.Unlock()
	v.mu.RLock()
	plan, oldKEK, oldDEK, oldID, sealed := v.plan, v.kek, v.dek, v.keyID, v.sealed
	v.mu.RUnlock()
	if sealed || oldDEK == nil {
		return core.ErrSealed
	}
	kek := oldKEK
	next := v.keyfile + ".new"
	if plan.source == srcAutogen {
		kek = newKey()
		if err := writeKeyfile(next, kek, true); err != nil {
			return fmt.Errorf("vault: writing the new keyfile: %w", err)
		}
	}
	dek := newKey()
	wrapped, err := wrapBlob(kek, dek)
	if err != nil {
		return err
	}
	check, err := wrapBlob(kek, []byte(kekCheckText))
	if err != nil {
		return err
	}
	var id int64
	err = v.store.Tx(ctx, func(tx core.Store) error {
		var err error
		if id, err = tx.Vault().PutKey(ctx, wrapped, check, plan.source); err != nil {
			return err
		}
		return tx.Vault().Rewrap(ctx, func(sc core.Scope, name string, keyID int64, version int, nonce, ct []byte) (int64, []byte, []byte, error) {
			if keyID != oldID {
				return 0, nil, nil, fmt.Errorf("vault: secret %q is encrypted under retired key %d", name, keyID)
			}
			aad := secretAAD(sc, name, version)
			pt, err := open(oldDEK, nonce, ct, aad)
			if err != nil {
				return 0, nil, nil, fmt.Errorf("vault: secret %q: %w", name, err)
			}
			n, c, err := seal(dek, pt, aad)
			clear(pt)
			return id, n, c, err
		})
	})
	if err != nil {
		if plan.source == srcAutogen {
			_ = os.Remove(next)
		}
		return err
	}
	if plan.source == srcAutogen {
		if err := renameKeyfile(next, v.keyfile); err != nil {
			v.log.Error("vault: the rotated KEK is in "+next+"; move it to "+v.keyfile+" before restarting", "err", err)
		}
	}
	v.mu.Lock()
	v.kek, v.dek, v.keyID = kek, dek, id
	v.mu.Unlock()
	v.red.Add("vault:kek", kek)
	v.red.Add("vault:dek", dek)
	return v.record(ctx, "vault.rotate", actor(by), "vault", "ok",
		map[string]any{"kek_source": plan.source, "kek_id": kekID(kek), "kek_replaced": plan.source == srcAutogen})
}

func renameKeyfile(from, to string) error {
	if err := os.Rename(from, to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (v *Vault) Status() core.VaultStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	refs := v.awsRefs(ctx)
	cancel()
	v.mu.RLock()
	defer v.mu.RUnlock()
	st := core.VaultStatus{
		Sealed:    v.sealed,
		KEKSource: v.plan.source,
		Colocated: v.plan.source == srcAutogen && v.colocated,
		Secrets:   v.count + len(v.envSecs),
	}
	if len(refs) > 0 {
		st.External = map[string]int{awsSource: len(refs)}
	}
	if v.kek != nil {
		st.KEKID = kekID(v.kek)
	}
	return st
}

func (v *Vault) Redactor() core.Redactor { return v.red }
