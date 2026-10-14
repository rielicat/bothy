package vault

import (
	"context"

	"github.com/rielicat/bothy/internal/core"
)

type check struct {
	id  string
	run func() []core.Finding
	ctx func(context.Context) []core.Finding
}

func (c check) ID() string { return c.id }

func (c check) Run(ctx context.Context) []core.Finding {
	if c.ctx != nil {
		return c.ctx(ctx)
	}
	return c.run()
}

func (c check) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }

// Checks returns vault.kek_colocated, vault.kek_env, vault.sealed and
// secrets.aws_access (whether the AWS credentials for aws-sm references
// resolve; it never reads a value).
func (v *Vault) Checks() []core.Check {
	return []core.Check{
		check{id: "vault.kek_colocated", run: v.checkColocated},
		check{id: "vault.kek_env", run: v.checkEnv},
		check{id: "vault.sealed", run: v.checkSealed},
		check{id: "secrets.aws_access", ctx: v.checkAWSAccess},
	}
}

func (v *Vault) checkColocated() []core.Finding {
	if !v.Status().Colocated {
		return nil
	}
	return []core.Finding{{
		ID: "vault.kek_colocated", Area: "vault", Severity: core.SevMedium, Security: true,
		Title:  "The vault key is stored next to the database",
		Detail: "The auto-generated KEK at " + v.keyfile + " is on the same volume as bothy.db, so a copy of that volume holds both the encrypted secrets and the key.",
		Fix: "Mount a separate volume at /keys (the compose file does); Bothy moves this key there on its next start. Or give the same key " +
			"as a Docker secret: docker cp <container>:" + v.keyfile + " bothy_kek, mount it as /run/secrets/bothy_kek, restart, then delete " +
			v.keyfile + ". A new key in the secret would seal the vault.",
	}}
}

func (v *Vault) checkEnv() []core.Finding {
	if v.Status().KEKSource != srcEnv {
		return nil
	}
	return []core.Finding{{
		ID: "vault.kek_env", Area: "vault", Severity: core.SevHigh, Security: true,
		Title:  "The vault key comes from an environment variable",
		Detail: "vault.kek is \"env\": BOTHY_KEK is visible to anything that can read the process environment (docker inspect, /proc, crash reports).",
		Fix:    "Use a Docker secret (/run/secrets/bothy_kek), a systemd credential, or vault.kek: file:/path.",
	}}
}

func (v *Vault) checkSealed() []core.Finding {
	v.mu.RLock()
	sealed, reason := v.sealed, v.reason
	v.mu.RUnlock()
	if !sealed {
		return nil
	}
	return []core.Finding{{
		ID: "vault.sealed", Area: "vault", Severity: core.SevHigh,
		Title:  "The vault is sealed",
		Detail: "Secrets cannot be read: " + reason + ".",
		Fix:    "/settings/vault",
	}}
}
