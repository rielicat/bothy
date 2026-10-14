package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Config   config.View
	Redactor *Redactor
	Audit    core.Auditor
	Env      func() []string // os.Environ: BOTHY_SECRET_<NAME>[_FILE], BOTHY_KEK
	Clock    core.Clock
	Log      *slog.Logger
}

// Vault implements core.Vault and core.CheckProvider.
//
// One active 32-byte DEK, wrapped by the KEK, encrypts every secret with
// AES-256-GCM under a random nonce and the AAD scope‖0‖name‖0‖version. A KEK
// that fails kek_check leaves the vault sealed (fail closed); a passphrase
// KEK leaves it sealed until Unseal.
type Vault struct {
	store core.Store
	cfg   config.View
	red   *Redactor
	audit core.Auditor
	clock core.Clock
	log   *slog.Logger
	opts  options

	envVars map[string]string
	envSecs map[string][]byte // BOTHY_SECRET_<NAME> values by <NAME>

	keyfile   string
	colocated bool
	legacyKey string // config.Dirs.LegacyKeyfile

	// keyMu is held exclusively by Unseal and RotateKEK, which replace key
	// material, and shared by every operation that encrypts or decrypts, so a
	// Put can never land a row under a DEK that a concurrent rotation retired.
	keyMu sync.RWMutex

	mu      sync.RWMutex
	plan    kekPlan
	sealed  bool
	reason  string // why sealed, for logs and the vault.sealed finding
	kek     []byte
	dek     []byte
	keyID   int64
	count   int // secrets stored in the vault
	touched map[string]time.Time

	aws awsState // aws-sm references: chains, cached values, uses
}

var (
	_ core.Vault         = (*Vault)(nil)
	_ core.CheckProvider = (*Vault)(nil)
)

// options are the host paths tests replace.
type options struct {
	secretsDir  string
	resolveDirs func(*config.Config) config.Dirs
}

func defaultOptions() options {
	return options{secretsDir: "/run/secrets", resolveDirs: config.ResolveDirs}
}

func New(d Deps) (*Vault, error) { return newVault(d, defaultOptions()) }

func newVault(d Deps, o options) (*Vault, error) {
	if d.Store == nil {
		return nil, errors.New("vault: no store")
	}
	v := &Vault{
		store: d.Store, cfg: d.Config, red: d.Redactor, audit: d.Audit, clock: d.Clock, log: d.Log, opts: o,
		envVars: map[string]string{}, envSecs: map[string][]byte{}, touched: map[string]time.Time{},
	}
	v.aws.init()
	if v.red == nil {
		v.red = NewRedactor()
	}
	if v.clock == nil {
		v.clock = core.SystemClock{}
	}
	if v.log == nil {
		v.log = slog.New(slog.DiscardHandler)
	}
	cfg := config.Default()
	if d.Config != nil {
		cfg = d.Config.Current()
	}
	dirs := o.resolveDirs(cfg)
	v.keyfile, v.colocated, v.legacyKey = dirs.Keyfile, dirs.KeyfileColocated, dirs.LegacyKeyfile
	if d.Env != nil {
		v.loadEnv(d.Env())
	}
	ctx := context.Background()
	if err := v.boot(ctx, cfg.Vault.KEK); err != nil {
		return nil, err
	}
	v.loadSecrets(ctx)
	return v, nil
}

// boot resolves the KEK and opens the active DEK. Only store failures are
// returned; every KEK problem leaves the vault sealed with a reason, so the
// gateway still starts and shows the vault_sealed banner.
func (v *Vault) boot(ctx context.Context, spec string) error {
	plan, err := v.planKEK(spec)
	if err != nil {
		v.setSealed(plan, "configuration: "+err.Error())
		return nil //nolint:nilerr // sealed, not fatal: see above
	}
	v.plan = plan
	if plan.source != srcEnv && v.envVar(kekEnv) != "" {
		v.log.Warn("BOTHY_KEK is set but vault.kek is not \"env\"; it is ignored")
	}
	id, wrapped, check, _, err := v.store.Vault().ActiveKey(ctx)
	first := errors.Is(err, core.ErrNotFound)
	if err != nil && !first {
		return fmt.Errorf("vault: %w", err)
	}
	if plan.source == srcPassphrase {
		v.setSealed(plan, "waiting for the passphrase (Settings → Vault → Unseal)")
		return nil
	}
	if plan.source == srcAutogen && !first {
		v.adoptLegacyKey(check)
	}
	kek, err := v.loadKEK(plan, first)
	if err != nil {
		v.setSealed(plan, "KEK unavailable: "+err.Error())
		return nil //nolint:nilerr // sealed, not fatal: see above
	}
	if first {
		dek := newKey()
		wrapped, err := wrapBlob(kek, dek)
		if err != nil {
			return fmt.Errorf("vault: %w", err)
		}
		check, err := wrapBlob(kek, []byte(kekCheckText))
		if err != nil {
			return fmt.Errorf("vault: %w", err)
		}
		if id, err = v.store.Vault().PutKey(ctx, wrapped, check, plan.source); err != nil {
			return fmt.Errorf("vault: %w", err)
		}
		v.setOpen(plan, kek, dek, id)
		v.log.Info("vault initialised", "kek_source", plan.source, "kek_id", kekID(kek))
		return nil
	}
	if !kekMatches(kek, check) && plan.source == srcAutogen {
		kek = v.recoverRotation(kek, check)
	}
	if !kekMatches(kek, check) {
		reason := "the KEK from " + plan.source + " does not match this vault (kek_check failed)"
		// Following the kek_colocated advice with a fresh secret lands here;
		// say where the key that does open the vault is.
		for _, p := range []string{v.keyfile, v.legacyKey} {
			if k, err := readKEKFile(p); plan.source != srcAutogen && p != "" && err == nil && kekMatches(k, check) {
				reason += "; the auto-generated key in " + p + " opens it: provide that key instead"
				break
			}
		}
		v.setSealed(plan, reason)
		v.log.Error("vault sealed: KEK does not match kek_check", "kek_source", plan.source, "kek_id", kekID(kek))
		return nil
	}
	dek, err := unwrapBlob(kek, wrapped)
	if err != nil {
		v.setSealed(plan, "the wrapped DEK could not be decrypted")
		return nil //nolint:nilerr // sealed, not fatal: see above
	}
	v.setOpen(plan, kek, dek, id)
	return nil
}

// adoptLegacyKey moves the auto KEK from <data>/keys/kek to the keyfile when
// a /keys volume appeared after the vault was created without one (a single
// container moved to compose). Only a key that opens this vault is moved.
func (v *Vault) adoptLegacyKey(check []byte) {
	if v.legacyKey == "" || v.legacyKey == v.keyfile || fileExists(v.keyfile) {
		return
	}
	k, err := readKEKFile(v.legacyKey)
	if err != nil || !kekMatches(k, check) {
		return
	}
	if err := writeKeyfile(v.keyfile, k, false); err != nil {
		v.log.Error("vault: could not move the KEK to the keys volume", "from", v.legacyKey, "to", v.keyfile, "err", err)
		return
	}
	syncDir(filepath.Dir(v.keyfile))
	if err := os.Remove(v.legacyKey); err != nil {
		v.log.Error("vault: the KEK was copied to the keys volume; delete the old copy", "path", v.legacyKey, "err", err)
		return
	}
	v.log.Warn("vault: moved the KEK off the data volume", "from", v.legacyKey, "to", v.keyfile)
}

// recoverRotation finishes a RotateKEK that committed the new key to the
// database but crashed before renaming <keyfile>.new into place.
func (v *Vault) recoverRotation(kek, check []byte) []byte {
	next := v.keyfile + ".new"
	k, err := readKEKFile(next)
	if err != nil || !kekMatches(k, check) {
		return kek
	}
	if err := renameKeyfile(next, v.keyfile); err != nil {
		v.log.Error("vault: could not move the rotated KEK into place", "err", err)
	} else {
		v.log.Warn("vault: completed an interrupted KEK rotation", "keyfile", v.keyfile)
	}
	return k
}
