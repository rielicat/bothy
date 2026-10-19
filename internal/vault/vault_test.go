package vault

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

type fixture struct {
	t          *testing.T
	store      core.Store
	dir        string
	cfg        *config.Config
	audit      core.Auditor
	env        []string
	secretsDir string
	colocated  bool
	clock      core.Clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	cfg.Vault.Keyfile = filepath.Join(dir, "keys", "kek")
	return &fixture{t: t, store: memstore.New(), dir: dir, cfg: cfg, audit: &fakes.Auditor{}, secretsDir: filepath.Join(dir, "run-secrets")}
}

func (f *fixture) open() *Vault {
	f.t.Helper()
	v, err := newVault(Deps{
		Store:  f.store,
		Config: config.Static(f.cfg),
		Audit:  f.audit,
		Env:    func() []string { return f.env },
		Clock:  f.clock,
	}, options{
		secretsDir: f.secretsDir,
		resolveDirs: func(c *config.Config) config.Dirs {
			return config.Dirs{Data: f.dir, Keyfile: c.Vault.Keyfile, KeyfileColocated: f.colocated}
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) user() core.UserID {
	f.t.Helper()
	u := &core.User{Username: core.NewID("u"), Role: core.RoleMember}
	if err := f.store.Users().Create(context.Background(), u); err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func (f *fixture) entries() []core.AuditEntry { return f.audit.(*fakes.Auditor).All() }

func mustPut(t *testing.T, v *Vault, sc core.Scope, name, value string) {
	t.Helper()
	if _, err := v.Put(context.Background(), sc, name, []byte(value), "usr_01aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("Put %s/%s: %v", sc, name, err)
	}
}

func mustGet(t *testing.T, v *Vault, sc core.Scope, name string) string {
	t.Helper()
	s, err := v.Get(context.Background(), sc, name, "test")
	if err != nil {
		t.Fatalf("Get %s/%s: %v", sc, name, err)
	}
	return s.Reveal()
}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	if st := v.Status(); st.Sealed || st.KEKSource != srcAutogen || len(st.KEKID) != 8 {
		t.Fatalf("status = %+v", st)
	}
	u := f.user()
	us := core.UserScope(u)
	mustPut(t, v, core.GlobalScope, "openai_key", "sk-global-0123456789abcdefghij")
	mustPut(t, v, us, "openai_key", "sk-user-0123456789abcdefghijkl")
	mustPut(t, v, core.GlobalScope, "only_global", "global-only-value-123")
	if got := mustGet(t, v, core.GlobalScope, "openai_key"); got != "sk-global-0123456789abcdefghij" {
		t.Errorf("global = %q", got)
	}
	if got := mustGet(t, v, us, "openai_key"); got != "sk-user-0123456789abcdefghijkl" {
		t.Errorf("user scope = %q", got)
	}
	if got := mustGet(t, v, us, "only_global"); got != "global-only-value-123" {
		t.Errorf("user scope falls back to global: %q", got)
	}
	if _, err := v.Get(ctx, us, "missing", "test"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
	mustPut(t, v, core.GlobalScope, "openai_key", "sk-global-v2-0123456789abcdefgh")
	if got := mustGet(t, v, core.GlobalScope, "openai_key"); got != "sk-global-v2-0123456789abcdefgh" {
		t.Errorf("after replace = %q", got)
	}
	_, version, _, _, err := f.store.Vault().GetSecret(ctx, core.GlobalScope, "openai_key")
	if err != nil || version != 2 {
		t.Errorf("replace bumps the version: %d %v", version, err)
	}
	metas, err := v.List(ctx, core.GlobalScope)
	if err != nil || len(metas) != 2 {
		t.Fatalf("List = %v %v", metas, err)
	}
	for _, m := range metas {
		if m.Name == "openai_key" && m.Hint != "efgh" {
			t.Errorf("hint = %q", m.Hint)
		}
	}
	if st := v.Status(); st.Secrets != 3 {
		t.Errorf("Status.Secrets = %d, want 3", st.Secrets)
	}
	if red := v.Redactor().Redact("key=sk-user-0123456789abcdefghijkl."); strings.Contains(red, "0123456789") {
		t.Errorf("stored value not registered with the redactor: %q", red)
	}

	var uses int
	for _, e := range f.entries() {
		if strings.Contains(string(e.Detail), "0123456789") {
			t.Errorf("audit detail carries a secret value: %s", e.Detail)
		}
		if e.Action == "secret.use" {
			uses++
			var d map[string]string
			if err := json.Unmarshal(e.Detail, &d); err != nil || d["name"] == "" || d["purpose"] != "test" {
				t.Errorf("secret.use detail = %s", e.Detail)
			}
		}
	}
	if uses != 4 {
		t.Errorf("secret.use recorded %d times, want 4 (one per successful Get)", uses)
	}

	if err := v.Delete(ctx, core.GlobalScope, "only_global", u); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, us, "only_global", "test"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get after Delete = %v", err)
	}
	if err := v.Delete(ctx, core.GlobalScope, "only_global", u); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Delete twice = %v", err)
	}
	// A session or provider that read it a moment ago may still send it.
	if red := v.Redactor().Redact("global-only-value-123"); red != Mask {
		t.Errorf("Delete unmasked the value at once: %q", red)
	}
	actions := strings.Join(f.audit.(*fakes.Auditor).Actions(), ",")
	for _, want := range []string{"vault.put", "secret.use", "vault.delete"} {
		if !strings.Contains(actions, want) {
			t.Errorf("audit actions %s lack %s", actions, want)
		}
	}
}

func TestReopenAndFiles(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	mustPut(t, v, core.GlobalScope, "token", "persisted-value-0001")
	id := v.Status().KEKID
	v2 := f.open()
	if st := v2.Status(); st.Sealed || st.KEKID != id {
		t.Fatalf("reopen status = %+v, want kek %s", st, id)
	}
	if got := mustGet(t, v2, core.GlobalScope, "token"); got != "persisted-value-0001" {
		t.Errorf("after reopen = %q", got)
	}
	if !strings.Contains(v2.Redactor().Redact("x persisted-value-0001 y"), Mask) {
		t.Error("boot does not register stored secrets with the redactor")
	}
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(f.cfg.Vault.Keyfile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("keyfile mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	di, err := os.Stat(filepath.Dir(f.cfg.Vault.Keyfile))
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("key dir mode = %v, %v; want 0700", di.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(filepath.Dir(f.cfg.Vault.Keyfile))
	if len(entries) != 1 {
		t.Errorf("key dir holds %d entries, want only the keyfile", len(entries))
	}
}

func writeKEK(t *testing.T, p string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWrongKEKSeals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	mustPut(t, v, core.GlobalScope, "token", "value-under-kek-one")
	writeKEK(t, f.cfg.Vault.Keyfile, []byte(hex.EncodeToString(newKey())))

	v2 := f.open()
	if !v2.Status().Sealed {
		t.Fatal("a vault opened with the wrong KEK is not sealed")
	}
	if _, err := v2.Get(ctx, core.GlobalScope, "token", "test"); !errors.Is(err, core.ErrSealed) {
		t.Errorf("Get on a sealed vault = %v", err)
	}
	if _, err := v2.Put(ctx, core.GlobalScope, "x", []byte("value"), ""); !errors.Is(err, core.ErrSealed) {
		t.Errorf("Put on a sealed vault = %v", err)
	}
	if err := v2.RotateKEK(ctx, ""); !errors.Is(err, core.ErrSealed) {
		t.Errorf("RotateKEK on a sealed vault = %v", err)
	}
	if err := v2.Unseal(ctx, []byte("a passphrase cannot help")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Unseal of a file-KEK vault = %v", err)
	}
	fs := runChecks(t, v2)
	if fs["vault.sealed"] == nil || !strings.Contains(fs["vault.sealed"].Detail, "kek_check") {
		t.Errorf("vault.sealed finding = %+v", fs["vault.sealed"])
	}
	_, _, _, ct, err := f.store.Vault().GetSecret(ctx, core.GlobalScope, "token")
	if err != nil || bytes.Contains(ct, []byte("value-under-kek-one")) {
		t.Errorf("ciphertext = %q, %v", ct, err)
	}
	if n := v2.Status().Secrets; n != 1 {
		t.Errorf("sealed Status.Secrets = %d, want the 1 stored secret", n)
	}
	if err := v2.Delete(ctx, core.GlobalScope, "token", ""); err != nil {
		t.Errorf("Delete on a sealed vault = %v", err)
	}
	if err := v2.Delete(ctx, core.GlobalScope, "token", ""); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
	if n := v2.Status().Secrets; n != 0 {
		t.Errorf("Status.Secrets after Delete = %d, want 0", n)
	}
}

func TestLostKeyfileIsNotRegenerated(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	mustPut(t, v, core.GlobalScope, "token", "value-0123456789")
	if err := os.Remove(f.cfg.Vault.Keyfile); err != nil {
		t.Fatal(err)
	}
	v2 := f.open()
	if !v2.Status().Sealed {
		t.Fatal("vault unsealed without its keyfile")
	}
	if _, err := os.Stat(f.cfg.Vault.Keyfile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a new keyfile was generated for an existing vault: %v", err)
	}
}

func TestCiphertextBoundToNameAndVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	mustPut(t, v, core.GlobalScope, "alpha", "alpha-secret-value")
	mustPut(t, v, core.GlobalScope, "beta", "beta-secret-value")
	r := f.store.Vault()
	keyID, version, nonce, ct, err := r.GetSecret(ctx, core.GlobalScope, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PutSecret(ctx, core.GlobalScope, "beta", keyID, version, nonce, ct, "", ""); err != nil {
		t.Fatal(err)
	}
	if s, err := v.Get(ctx, core.GlobalScope, "beta", "test"); err == nil {
		t.Errorf("ciphertext moved to another name decrypted to %q", s.Reveal())
	}
	if err := r.PutSecret(ctx, core.GlobalScope, "alpha", keyID, version+1, nonce, ct, "", ""); err != nil {
		t.Fatal(err)
	}
	if s, err := v.Get(ctx, core.GlobalScope, "alpha", "test"); err == nil {
		t.Errorf("ciphertext with another version decrypted to %q", s.Reveal())
	}
	u := f.user()
	if err := r.PutSecret(ctx, core.UserScope(u), "alpha", keyID, version, nonce, ct, "", ""); err != nil {
		t.Fatal(err)
	}
	if s, err := v.Get(ctx, core.UserScope(u), "alpha", "test"); err == nil {
		t.Errorf("ciphertext moved to another scope decrypted to %q", s.Reveal())
	}
}

func TestRotateKEK(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	u := f.user()
	want := map[string]string{"global/a": "value-aaaaaaaaaaaa", "global/b": "value-bbbbbbbbbbbb", "user/a": "value-user-aaaaaaa"}
	mustPut(t, v, core.GlobalScope, "a", want["global/a"])
	mustPut(t, v, core.GlobalScope, "b", want["global/b"])
	mustPut(t, v, core.UserScope(u), "a", want["user/a"])
	oldFile, err := os.ReadFile(f.cfg.Vault.Keyfile)
	if err != nil {
		t.Fatal(err)
	}
	oldID := v.Status().KEKID

	if err := v.RotateKEK(ctx, u); err != nil {
		t.Fatal(err)
	}
	check := func(v *Vault) {
		t.Helper()
		for k, val := range want {
			sc := core.GlobalScope
			if strings.HasPrefix(k, "user/") {
				sc = core.UserScope(u)
			}
			if got := mustGet(t, v, sc, strings.SplitN(k, "/", 2)[1]); got != val {
				t.Errorf("%s = %q, want %q", k, got, val)
			}
		}
	}
	check(v)
	if v.Status().KEKID == oldID {
		t.Error("rotation kept the KEK id")
	}
	newFile, _ := os.ReadFile(f.cfg.Vault.Keyfile)
	if bytes.Equal(newFile, oldFile) {
		t.Error("rotation did not replace the auto-generated keyfile")
	}
	if _, err := os.Stat(f.cfg.Vault.Keyfile + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rotation left %s.new behind: %v", f.cfg.Vault.Keyfile, err)
	}
	check(f.open())
	if !strings.Contains(strings.Join(f.audit.(*fakes.Auditor).Actions(), ","), "vault.rotate") {
		t.Error("rotation not audited")
	}

	// A crash between the commit and the rename leaves the new KEK in .new.
	writeKEK(t, f.cfg.Vault.Keyfile+".new", newFile)
	writeKEK(t, f.cfg.Vault.Keyfile, oldFile)
	v3 := f.open()
	if v3.Status().Sealed {
		t.Fatal("boot did not complete the interrupted rotation")
	}
	check(v3)
	if got, _ := os.ReadFile(f.cfg.Vault.Keyfile); !bytes.Equal(got, newFile) {
		t.Error("boot did not move the rotated keyfile into place")
	}
}

func TestRotateExternalKEKRotatesDEK(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	kek := newKey()
	writeKEK(t, filepath.Join(f.secretsDir, "bothy_kek"), []byte(base64.StdEncoding.EncodeToString(kek)+"\n"))
	v := f.open()
	if st := v.Status(); st.KEKSource != srcDocker || st.KEKID != kekID(kek) {
		t.Fatalf("status = %+v", st)
	}
	mustPut(t, v, core.GlobalScope, "a", "value-for-rotation")
	keyBefore, _, _, _, _ := f.store.Vault().GetSecret(ctx, core.GlobalScope, "a")
	if err := v.RotateKEK(ctx, ""); err != nil {
		t.Fatal(err)
	}
	keyAfter, _, _, _, _ := f.store.Vault().GetSecret(ctx, core.GlobalScope, "a")
	if keyAfter == keyBefore {
		t.Error("the secret was not re-encrypted under a new DEK")
	}
	if v.Status().KEKID != kekID(kek) {
		t.Error("an external KEK changed during rotation")
	}
	if got := mustGet(t, f.open(), core.GlobalScope, "a"); got != "value-for-rotation" {
		t.Errorf("after rotation and reopen = %q", got)
	}
}

func TestKEKSources(t *testing.T) {
	kek := newKey()
	cases := []struct {
		name   string
		setup  func(f *fixture)
		source string
		sealed bool
	}{
		{"docker secret hex", func(f *fixture) {
			writeKEK(t, filepath.Join(f.secretsDir, "bothy_kek"), []byte(hex.EncodeToString(kek)))
		}, srcDocker, false},
		{"systemd credential under auto", func(f *fixture) {
			dir := filepath.Join(f.dir, "creds")
			writeKEK(t, filepath.Join(dir, "bothy_kek"), []byte(base64.RawURLEncoding.EncodeToString(kek)))
			f.env = []string{"CREDENTIALS_DIRECTORY=" + dir}
		}, srcSystemd, false},
		{"systemd named credential", func(f *fixture) {
			dir := filepath.Join(f.dir, "creds")
			writeKEK(t, filepath.Join(dir, "mykek"), kek)
			f.env = []string{"CREDENTIALS_DIRECTORY=" + dir}
			f.cfg.Vault.KEK = "systemd:mykek"
		}, srcSystemd, false},
		{"systemd path traversal", func(f *fixture) {
			f.env = []string{"CREDENTIALS_DIRECTORY=" + f.dir}
			f.cfg.Vault.KEK = "systemd:../kek"
		}, "", true},
		{"file raw bytes", func(f *fixture) {
			p := filepath.Join(f.dir, "kek.bin")
			writeKEK(t, p, kek)
			f.cfg.Vault.KEK = "file:" + p
		}, srcFile, false},
		{"file missing", func(f *fixture) { f.cfg.Vault.KEK = "file:" + filepath.Join(f.dir, "nope") }, srcFile, true},
		{"file malformed", func(f *fixture) {
			p := filepath.Join(f.dir, "kek.txt")
			writeKEK(t, p, []byte("not a key"))
			f.cfg.Vault.KEK = "file:" + p
		}, srcFile, true},
		{"env", func(f *fixture) {
			f.env = []string{"BOTHY_KEK=" + hex.EncodeToString(kek)}
			f.cfg.Vault.KEK = "env"
		}, srcEnv, false},
		{"env unset", func(f *fixture) { f.cfg.Vault.KEK = "env" }, srcEnv, true},
		{"unknown", func(f *fixture) { f.cfg.Vault.KEK = "keychain" }, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			v := f.open()
			st := v.Status()
			if st.KEKSource != c.source || st.Sealed != c.sealed {
				t.Fatalf("status = %+v, want source %q sealed %t", st, c.source, c.sealed)
			}
			if c.sealed {
				return
			}
			if st.KEKID != kekID(kek) {
				t.Errorf("kek id = %s, want %s", st.KEKID, kekID(kek))
			}
			if _, err := os.Stat(f.cfg.Vault.Keyfile); !errors.Is(err, os.ErrNotExist) {
				t.Error("an external KEK source also generated a keyfile")
			}
			mustPut(t, v, core.GlobalScope, "x", "value-for-source")
			if got := mustGet(t, f.open(), core.GlobalScope, "x"); got != "value-for-source" {
				t.Errorf("reopen = %q", got)
			}
		})
	}
}

func TestEnvKEKIgnoredUnlessConfigured(t *testing.T) {
	f := newFixture(t)
	f.env = []string{"BOTHY_KEK=" + hex.EncodeToString(newKey())}
	if st := f.open().Status(); st.KEKSource != srcAutogen {
		t.Errorf("auto picked %q with BOTHY_KEK set, want the keyfile", st.KEKSource)
	}
}

func TestPassphrase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.cfg.Vault.KEK = "passphrase"
	v := f.open()
	if st := v.Status(); !st.Sealed || st.KEKSource != srcPassphrase {
		t.Fatalf("status = %+v", st)
	}
	if _, err := v.Get(ctx, core.GlobalScope, "x", "test"); !errors.Is(err, core.ErrSealed) {
		t.Errorf("Get while sealed = %v", err)
	}
	if err := v.Unseal(ctx, []byte("short")); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Unseal with a short first passphrase = %v", err)
	}
	pass := []byte("correct horse battery staple")
	if err := v.Unseal(ctx, pass); err != nil {
		t.Fatal(err)
	}
	if v.Status().Sealed {
		t.Fatal("still sealed after Unseal")
	}
	mustPut(t, v, core.GlobalScope, "x", "value-behind-passphrase")
	if err := v.Unseal(ctx, []byte("anything")); err != nil {
		t.Errorf("Unseal of an unsealed vault = %v", err)
	}

	v2 := f.open()
	if !v2.Status().Sealed {
		t.Fatal("a passphrase vault is unsealed after restart")
	}
	if err := v2.Unseal(ctx, []byte("wrong horse battery staple")); !errors.Is(err, core.ErrForbidden) {
		t.Errorf("Unseal with a wrong passphrase = %v", err)
	}
	if !v2.Status().Sealed {
		t.Fatal("a wrong passphrase unsealed the vault")
	}
	if err := v2.Unseal(ctx, pass); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, v2, core.GlobalScope, "x"); got != "value-behind-passphrase" {
		t.Errorf("after unseal = %q", got)
	}
	var denied, ok int
	for _, e := range f.entries() {
		if e.Action == "vault.unseal" {
			switch e.Outcome {
			case "denied":
				denied++
			case "ok":
				ok++
			}
		}
	}
	if denied != 1 || ok != 2 {
		t.Errorf("vault.unseal audited ok=%d denied=%d, want 2 and 1", ok, denied)
	}
	if err := v2.RotateKEK(ctx, ""); err != nil {
		t.Fatal(err)
	}
	v3 := f.open()
	if err := v3.Unseal(ctx, pass); err != nil {
		t.Fatalf("unseal after a DEK rotation: %v", err)
	}
	if got := mustGet(t, v3, core.GlobalScope, "x"); got != "value-behind-passphrase" {
		t.Errorf("after rotation = %q", got)
	}
}

func TestEnvSecrets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file := filepath.Join(f.dir, "docker-secret")
	if err := os.WriteFile(file, []byte("from-a-file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = []string{
		"BOTHY_SECRET_DOCS_MCP_TOKEN=env-docs-token-value",
		"BOTHY_SECRET_FILE_KEY_FILE=" + file,
		"BOTHY_SECRET_EMPTY=",
		"OTHER=ignored",
	}
	v := f.open()
	mustPut(t, v, core.GlobalScope, "docs_mcp_token", "vault-docs-token-value")
	u := f.user()
	for _, c := range []struct {
		sc   core.Scope
		name string
		want string
	}{
		{core.GlobalScope, "docs_mcp_token", "env-docs-token-value"},
		{core.UserScope(u), "docs-mcp.token", "env-docs-token-value"},
		{core.GlobalScope, "file.key", "from-a-file-value"},
	} {
		if got := mustGet(t, v, c.sc, c.name); got != c.want {
			t.Errorf("Get(%s, %s) = %q, want %q", c.sc, c.name, got, c.want)
		}
	}
	if _, err := v.Get(ctx, core.GlobalScope, "empty", "test"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("an empty env secret = %v", err)
	}
	metas, err := v.List(ctx, core.GlobalScope)
	if err != nil {
		t.Fatal(err)
	}
	env := 0
	for _, m := range metas {
		if m.Source == "env" {
			env++
		}
	}
	if env != 2 {
		t.Errorf("List shows %d env secrets, want 2: %+v", env, metas)
	}
	if n := len(metas); n < 2 || metas[n-2].Name != "docs_mcp_token" || metas[n-1].Name != "file_key" {
		t.Errorf("env secrets are not listed in name order: %+v", metas)
	}
	for _, s := range []string{"env-docs-token-value", "from-a-file-value"} {
		if red := v.Redactor().Redact(s); red != Mask {
			t.Errorf("env secret not redacted: %q", red)
		}
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	u := f.user()
	mustPut(t, v, core.GlobalScope, "api", "global-api-value")
	mustPut(t, v, core.UserScope(u), "api", "user-api-value")
	mustPut(t, v, core.GlobalScope, "other", "other-value-x")
	for _, c := range []struct {
		ref  string
		u    core.UserID
		want string
	}{
		{"secret:api", "", "global-api-value"},
		{"secret:api", u, "user-api-value"},
		{"secret:other", u, "other-value-x"},
		{"Bearer secret:api", "", "Bearer global-api-value"},
		{"k=secret:api;o=secret:other", "", "k=global-api-value;o=other-value-x"},
	} {
		s, err := v.Resolve(ctx, c.ref, c.u, "provider:test")
		if err != nil || s.Reveal() != c.want {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q", c.ref, c.u, s.Reveal(), err, c.want)
		}
	}
	for _, ref := range []string{"sk-plaintext-0123456789", "", "secret:", "Bearer secret:", "notasecret:api"} {
		_, err := v.Resolve(ctx, ref, "", "provider:test")
		if !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Resolve(%q) = %v, want ErrInvalid", ref, err)
		}
		if err != nil && ref != "" && strings.Contains(err.Error(), ref) {
			t.Errorf("Resolve error echoes its input: %v", err)
		}
	}
	if _, err := v.Resolve(ctx, "secret:missing", "", "x"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Resolve of a missing secret = %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v := f.open()
	for _, sc := range []core.Scope{"", "user:", "user:spc_01aaaaaaaaaaaaaaaaaaaaaaaa", "space:x", "GLOBAL"} {
		if _, err := v.Put(ctx, sc, "name", []byte("value"), ""); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Put scope %q = %v", sc, err)
		}
		if _, err := v.List(ctx, sc); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("List scope %q = %v", sc, err)
		}
	}
	for _, name := range []string{"", "UPPER", "a/b", "a b", strings.Repeat("a", 65)} {
		if _, err := v.Put(ctx, core.GlobalScope, name, []byte("value"), ""); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Put name %q = %v", name, err)
		}
		if _, err := v.Get(ctx, core.GlobalScope, name, "x"); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("Get name %q = %v", name, err)
		}
	}
	if _, err := v.Put(ctx, core.GlobalScope, "empty", nil, ""); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("Put of an empty value = %v", err)
	}
}

type failingAuditor struct{ fakes.Auditor }

func (*failingAuditor) Record(context.Context, core.AuditEntry) error { return errors.New("disk full") }

func TestAuditFailureWithholdsSecret(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	mustPut(t, v, core.GlobalScope, "x", "value-needs-audit")
	v.audit = &failingAuditor{}
	if s, err := v.Get(context.Background(), core.GlobalScope, "x", "test"); err == nil {
		t.Errorf("Get returned %q although its audit failed", s.Reveal())
	}
}

// TestNoAuditorWithholdsSecrets: a vault built without an auditor cannot
// record secret.use, so it releases neither vault nor env secrets.
func TestNoAuditorWithholdsSecrets(t *testing.T) {
	f := newFixture(t)
	f.env = []string{"BOTHY_SECRET_FROM_ENV=env-value-0123"}
	f.audit = nil
	v := f.open()
	mustPut(t, v, core.GlobalScope, "x", "value-needs-audit")
	for _, name := range []string{"x", "from_env"} {
		if s, err := v.Get(context.Background(), core.GlobalScope, name, "test"); err == nil {
			t.Errorf("Get(%s) returned %q without an auditor", name, s.Reveal())
		}
	}
	if _, err := v.Resolve(context.Background(), "secret:x", "", "test"); err == nil {
		t.Error("Resolve succeeded without an auditor")
	}
}

func runChecks(t *testing.T, p core.CheckProvider) map[string]*core.Finding {
	t.Helper()
	out := map[string]*core.Finding{}
	for _, c := range p.Checks() {
		for _, fd := range c.Run(context.Background()) {
			if fd.ID != c.ID() {
				t.Errorf("check %s produced finding %s", c.ID(), fd.ID)
			}
			out[fd.ID] = &fd
		}
		if err := c.Fix(context.Background(), core.Finding{ID: c.ID()}); !errors.Is(err, core.ErrNotImplemented) {
			t.Errorf("%s Fix = %v", c.ID(), err)
		}
	}
	return out
}

func TestChecks(t *testing.T) {
	f := newFixture(t)
	f.colocated = true
	fs := runChecks(t, f.open())
	if c := fs["vault.kek_colocated"]; c == nil || c.Severity != core.SevMedium || !c.Security {
		t.Errorf("kek_colocated = %+v", c)
	}
	if fs["vault.kek_env"] != nil || fs["vault.sealed"] != nil {
		t.Errorf("unexpected findings %v", fs)
	}

	f = newFixture(t)
	f.colocated = true
	f.env = []string{"BOTHY_KEK=" + hex.EncodeToString(newKey())}
	f.cfg.Vault.KEK = "env"
	fs = runChecks(t, f.open())
	if c := fs["vault.kek_env"]; c == nil || c.Severity != core.SevHigh || !c.Security {
		t.Errorf("kek_env = %+v", c)
	}
	if fs["vault.kek_colocated"] != nil {
		t.Error("kek_colocated fired for an env KEK")
	}

	f = newFixture(t)
	f.cfg.Vault.KEK = "passphrase"
	fs = runChecks(t, f.open())
	if c := fs["vault.sealed"]; c == nil || c.Severity != core.SevHigh {
		t.Errorf("sealed = %+v", c)
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New without a store succeeded")
	}
}

// The API's token, client address and source reach the vault's own rows.
func TestKeyChangesCarryTheRequestOrigin(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	u := f.user()
	ctx := core.WithAuditOrigin(context.Background(), core.AuditOrigin{Actor: "token:tok_1", IP: "203.0.113.7", Detail: map[string]any{"source": "env:OPENAI_API_KEY"}})
	if _, err := v.Put(ctx, core.GlobalScope, "openai_key", []byte("sk-origin-0123456789abcdefghij"), u); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(context.Background(), core.GlobalScope, "openai_key", u); err != nil {
		t.Fatal(err)
	}
	var put, del *core.AuditEntry
	for _, e := range f.entries() {
		switch e.Action {
		case "vault.put":
			put = &e
		case "vault.delete":
			del = &e
		}
	}
	if put == nil || put.Actor != "token:tok_1" || put.IP != "203.0.113.7" || !strings.Contains(string(put.Detail), `"source":"env:OPENAI_API_KEY"`) {
		t.Fatalf("put row = %+v (%s)", put, put.Detail)
	}
	if del == nil || del.Actor != "user:"+string(u) || del.IP != "" {
		t.Fatalf("delete row without an origin = %+v", del)
	}
}

// A replaced or deleted key stays masked while what read it a moment ago may
// still send it; only the one before the last change is let go.
func TestReplacedAndDeletedKeysStayMasked(t *testing.T) {
	f := newFixture(t)
	v := f.open()
	ctx := context.Background()
	put := func(val string) {
		t.Helper()
		if _, err := v.Put(ctx, core.GlobalScope, "mcp_echo_header", []byte(val), ""); err != nil {
			t.Fatal(err)
		}
	}
	masked := func(val string) bool { return v.Redactor().Redact("got "+val) == "got "+Mask }
	put("Bearer qa-good-value-0001")
	put("Bearer qa-good-value-0002")
	if !masked("Bearer qa-good-value-0001") || !masked("Bearer qa-good-value-0002") {
		t.Fatal("a replaced key was unmasked at once")
	}
	put("Bearer qa-good-value-0003")
	if masked("Bearer qa-good-value-0001") || !masked("Bearer qa-good-value-0002") {
		t.Fatal("want the key before the last change masked, the one before that let go")
	}
	if err := v.Delete(ctx, core.GlobalScope, "mcp_echo_header", ""); err != nil {
		t.Fatal(err)
	}
	if !masked("Bearer qa-good-value-0003") || masked("Bearer qa-good-value-0002") {
		t.Fatal("a deleted key must stay masked in place of the replaced one")
	}
}
