# vault

`core.Vault` and `core.Redactor` (owner: M1). One active 32-byte DEK, wrapped
with AES-256-GCM by the KEK, encrypts each secret with AES-256-GCM under a random
96-bit nonce and the AAD `scope‖0‖name‖0‖version`, so a ciphertext copied to
another row does not decrypt. `vault.kek` picks the KEK: `auto` uses the Docker
secret `/run/secrets/bothy_kek`, else `$CREDENTIALS_DIRECTORY/bothy_kek`, else an
auto-generated keyfile (0600 in a 0700 directory, generated only on first boot);
`file:/path`, `systemd:<name>`, `passphrase` (Argon2id; sealed until `Unseal`, the
first of which sets it) and `env` (`BOTHY_KEK`, only when chosen explicitly) are
the alternatives. A KEK that fails `kek_check` leaves the vault sealed instead of
stopping the gateway. `BOTHY_SECRET_<NAME>` and `BOTHY_SECRET_<NAME>_FILE` shadow
vault entries in every scope and are held in memory only; `Resolve` substitutes
`secret:<name>` references, also inside header values, and rejects plain strings
without echoing them. Every use is audited as `secret.use` before the value is
released; when that audit fails, or the vault has no auditor, nothing is. `RotateKEK` re-encrypts all secrets under a new DEK in one transaction
and, for the auto keyfile, a new KEK written through `<keyfile>.new`. The
`Redactor` masks registered values (and, from 8 bytes, their hex, percent,
base64/base64url and JSON-escaped forms) plus well-known token patterns;
`NewSlogHandler` applies it to every log message and attribute. `Checks` reports
`vault.kek_colocated`, `vault.kek_env` and `vault.sealed`.
