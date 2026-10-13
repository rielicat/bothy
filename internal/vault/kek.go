package vault

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KEK sources as reported in core.VaultStatus.KEKSource and vault_keys.kek_source.
const (
	srcDocker     = "docker_secret"
	srcSystemd    = "systemd"
	srcFile       = "file"
	srcPassphrase = "passphrase"
	srcEnv        = "env"
	srcAutogen    = "autogen"
)

const (
	kekSecretName = "bothy_kek"
	kekEnv        = "BOTHY_KEK"
	maxKEKFile    = 4096
)

// kekPlan is where the KEK comes from, resolved from vault.kek before any
// key material is read.
type kekPlan struct {
	source string
	path   string // file-backed sources
}

// planKEK maps vault.kek to a source. "auto" prefers a Docker secret, then a
// systemd credential, then the auto-generated keyfile; BOTHY_KEK is used only
// with an explicit "env", so a stray environment variable never silently
// becomes the KEK.
func (v *Vault) planKEK(spec string) (kekPlan, error) {
	switch {
	case spec == "" || spec == "auto":
		if p := filepath.Join(v.opts.secretsDir, kekSecretName); fileExists(p) {
			return kekPlan{source: srcDocker, path: p}, nil
		}
		if dir := v.envVar("CREDENTIALS_DIRECTORY"); dir != "" {
			if p := filepath.Join(dir, kekSecretName); fileExists(p) {
				return kekPlan{source: srcSystemd, path: p}, nil
			}
		}
		return kekPlan{source: srcAutogen, path: v.keyfile}, nil
	case strings.HasPrefix(spec, "file:"):
		p := strings.TrimPrefix(spec, "file:")
		if p == "" {
			return kekPlan{}, errors.New("vault.kek file: needs a path")
		}
		return kekPlan{source: srcFile, path: p}, nil
	case strings.HasPrefix(spec, "systemd:"):
		name := strings.TrimPrefix(spec, "systemd:")
		dir := v.envVar("CREDENTIALS_DIRECTORY")
		if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
			return kekPlan{}, fmt.Errorf("vault.kek systemd: bad credential name %q", name)
		}
		if dir == "" {
			return kekPlan{}, errors.New("vault.kek systemd: CREDENTIALS_DIRECTORY is not set (LoadCredential= in the unit)")
		}
		return kekPlan{source: srcSystemd, path: filepath.Join(dir, name)}, nil
	case spec == srcPassphrase:
		return kekPlan{source: srcPassphrase}, nil
	case spec == srcEnv:
		return kekPlan{source: srcEnv}, nil
	}
	return kekPlan{}, fmt.Errorf("vault.kek: unknown source %q (auto | file:/path | systemd:<name> | passphrase | env)", spec)
}

// loadKEK reads the KEK for a non-passphrase plan. create allows generating
// the auto keyfile; it is only set on first boot, so a lost keyfile is never
// silently replaced by a new key that cannot open the existing vault.
func (v *Vault) loadKEK(p kekPlan, create bool) ([]byte, error) {
	switch p.source {
	case srcEnv:
		val := v.envVar(kekEnv)
		if val == "" {
			return nil, errors.New(kekEnv + " is not set")
		}
		return parseKEK([]byte(val))
	case srcAutogen:
		k, err := readKEKFile(p.path)
		if errors.Is(err, fs.ErrNotExist) && create {
			return generateKeyfile(p.path)
		}
		return k, err
	}
	return readKEKFile(p.path)
}

func readKEKFile(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxKEKFile {
		return nil, fmt.Errorf("%s: not a KEK file", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	k, err := parseKEK(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return k, nil
}

// generateKeyfile writes a new random KEK as hex to p (0600, directory 0700)
// through a temp file and a no-clobber link, so a crash never leaves a
// truncated keyfile and a concurrent writer is never overwritten.
func generateKeyfile(p string) ([]byte, error) {
	k := newKey()
	if err := writeKeyfile(p, k, false); err != nil {
		return nil, err
	}
	return k, nil
}

func writeKeyfile(p string, k []byte, replace bool) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".kek-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp, p)
	}
	return os.Link(tmp, p)
}

// syncDir makes a new directory entry durable before its only other copy is
// deleted; platforms that cannot sync a directory are left as they are.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
