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
`secret:<name>` and `aws-sm:<secret name or ARN>[#<json key>][?region=&profile=]`
references, also inside header values, and rejects plain strings without
echoing them. An `aws-sm` reference is read from AWS Secrets Manager
(`awssm`: one hand-signed `GetSecretValue`) with the bedrock provider's AWS
credential chain and egress, which `cmd/bothy` hands over with `SetAWS` once
they exist; the egress system client admits exactly
`secretsmanager.<region>.amazonaws.com:443` (`secrets.aws.allow_private` for an
interface VPC endpoint). Values are cached in memory for `secrets.aws.cache_ttl`
(one fetch for concurrent uses, expired values dropped), never stored, and
registered with the redactor (the whole value and each JSON key read, for the
current and the previous version); every use, cached or failed, is audited as
`secret.use` with the reference. Failures carry `ExternalError` with a
`core.FailureCode` (`aws_no_credentials`, `aws_denied`, `not_found`,
`json_key_missing`, `not_json`, …) and a plain message. The references are
company-wide: the same for every user, listed (source `aws-sm`, no hint) only
under the global scope, and a vault value that looks like one is never resolved
again. They work while the vault is sealed. Every use is audited as `secret.use` before the value is
released; when that audit fails, or the vault has no auditor, nothing is. `RotateKEK` re-encrypts all secrets under a new DEK in one transaction
and, for the auto keyfile, a new KEK written through `<keyfile>.new`. The
`Redactor` masks registered values (and, from 8 bytes, their hex, percent,
base64/base64url and JSON-escaped forms) plus well-known token patterns;
`PartialAt` tells a streaming consumer where a value may still be arriving at
the end of its text, so it can hold that tail back (the agent's `run.delta`
and tool output, the channel streamer). `NewSlogHandler` applies it to every log message and attribute. `Checks` reports
`vault.kek_colocated`, `vault.kek_env`, `vault.sealed` and `secrets.aws_access`
(whether the AWS credentials for the configured references resolve; it never
reads a value).
