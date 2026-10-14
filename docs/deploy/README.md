# Deploying Bothy in your cloud

Four recipes, one image. Each runs the signed release image
`ghcr.io/rielicat/bothy` (uid 65532, port 8080, data under `/data`) and keeps
the same rules: provider keys and channel tokens stay in the platform's secret
store, where Bothy reads them itself ([below](#platform-secrets)), or in
Bothy's vault, whose key comes from that store as a file; and the web UI is
reached privately.

| Recipe | Shell, Python, MCP stdio | Vault key (KEK) | Private access | Use it when |
|---|---|---|---|---|
| [Single VM with compose](vm.md) (any cloud) | Yes (Docker 25+; gVisor needs 26+) | Docker secret file, or systemd credential for the native binary | SSH tunnel, Tailscale, SSM port forwarding | You want everything, with the least to run. Recommended. |
| [AWS ECS on EC2](aws-ecs.md) | Yes, also on the ECS-optimized AMI's Docker 25 (runc only) | Secrets Manager, written to a file by an init container | SSM port forwarding, internal ALB | You already run ECS and want task roles, CloudWatch logs and ECS deploys. |
| [AWS EKS](aws-eks.md) | No by default (EKS nodes run containerd); optional sidecar on Docker 26+ nodes | Kubernetes Secret or the Secrets Store CSI driver, mounted as a file | `kubectl port-forward`, internal ALB | Your platform is Kubernetes. |
| [AWS Fargate](fargate.md) | Never (single-container mode) | Secrets Manager, written to a file by an init container | Internal ALB | You accept chat, channels, web and HTTP MCP tools without a sandbox. |

Without a sandbox, Bothy works but `shell`, `python` and MCP stdio servers are
absent and a red banner says so. Nothing ever falls back to running on the
host.

The recipes assume a published release. Until then, build the images from
source (see the [README](../../README.md#from-source)), push them to your
registry and use those digests.

## Security model

**The vault key.** Bothy encrypts every stored secret with a data key wrapped
by a key encryption key (KEK). With `vault.kek: auto` it looks for, in order:
a Docker or Podman secret at `/run/secrets/bothy_kek`, a systemd credential
`$CREDENTIALS_DIRECTORY/bothy_kek`, then an auto-generated keyfile on the
separate `/keys` volume (or next to the database, flagged, when there is no
such volume). `vault.kek: file:/path` names a file, `passphrase`
keeps the vault sealed after every start until an admin unlocks it in the UI,
and `env` (`BOTHY_KEK`) works but is flagged high by `bothy security-audit`.

In the cloud:

- Generate the KEK once (`openssl rand -hex 32`, 64 hex characters) and keep it
  in the platform's secret store: AWS Secrets Manager, a Kubernetes Secret, a
  Docker secret file on the VM.
- Give it to Bothy as a file. Never put it in an environment variable of a task
  definition, a manifest or a compose file. ECS can only inject secrets as
  environment variables, so the ECS and Fargate recipes fetch it with an init
  container instead ([why](aws-ecs.md#the-vault-key)).
- The key must exist before the first start and must never change silently: a
  new key cannot open an existing vault, and Bothy then stays sealed (fail
  closed). To move an existing install, put its current key in the store
  (`docker run --rm -v bothy_keys:/k:ro busybox cat /k/kek`).
- `bothy vault status` shows the source (`docker_secret`, `systemd`, `file`,
  ...) and the key id, never the key.

**Cloud credentials.** On AWS, use the platform role: the EC2 instance role,
the ECS task role, or IRSA on EKS. Bothy resolves them with the standard AWS
credential chain and stores nothing. Scope the role to the models you use
([`deploy/aws/iam-bedrock-policy.json`](../../deploy/aws/iam-bedrock-policy.json))
and the secrets it may read
([`deploy/aws/iam-secrets-policy.json`](../../deploy/aws/iam-secrets-policy.json)).

<a id="platform-secrets"></a>**Platform secrets instead of the vault.** Keep
the provider keys, channel tokens, search keys, MCP headers and GitHub tokens
in AWS Secrets Manager, as your other services do, and reference them where
Bothy's configuration takes a secret: `aws-sm:<name or ARN>`, with
`#<json key>` for one value of a JSON secret. Bothy fetches them with the
platform role when they are first used, through its egress control to
`secretsmanager.<region>.amazonaws.com` only, keeps them in memory for
`secrets.aws.cache_ttl` (5 minutes) and never writes them to disk, the
database or a backup. Rotation needs no restart: a new version is read when
the cached value expires. Admins see the references in Settings → Vault
(source `aws-sm`, no hint), `bothy doctor` checks the role resolves
(`secrets.aws_access`), and each use is audited as `secret.use` with the
reference, never the value.

```sh
aws secretsmanager create-secret --name bothy/providers \
  --secret-string file://providers.json   # {"anthropic_api_key": "...", "slack_bot_token": "..."}
```

```yaml
secrets:
  aws: {region: us-east-1}          # BOTHY_SECRETS__AWS__REGION=us-east-1
providers:
  - {id: anthropic, type: anthropic, api_key: "aws-sm:bothy/providers#anthropic_api_key"}
channels:
  slack: {enabled: true, bot_token: "aws-sm:bothy/providers#slack_bot_token", app_token: "aws-sm:bothy/providers#slack_app_token"}
```

[`iam-secrets-policy.json`](../../deploy/aws/iam-secrets-policy.json) allows `secretsmanager:GetSecretValue` on `bothy/*`. Secrets
encrypted with the default `aws/secretsmanager` key need nothing more; for a
customer managed key also allow `kms:Decrypt` on that key with the condition
`"kms:ViaService": "secretsmanager.<region>.amazonaws.com"`. Where the VPC
reaches Secrets Manager through an interface endpoint with private DNS, set
`secrets.aws.allow_private: true` (`BOTHY_SECRETS__AWS__ALLOW_PRIVATE=true`):
Bothy refuses private addresses for public service names otherwise. The vault
still exists and its key still matters for whatever is saved in the UI (a key
pasted in setup, a GitHub connection's token), so keep the KEK in the secret
store as below; with every secret referenced this way, a lost KEK loses
nothing that the secret store does not hold.

**The sandbox.** `bothy sandboxd` is the only process that holds the container
runtime socket, which is equivalent to root on that host. It has no network,
serves a fixed-schema API on a unix socket, accepts only the gateway's uid and
builds every box from a compiled-in spec (no network, read-only root, no
capabilities, uid 10001, digest-pinned images). Prefer rootless Docker or
gVisor (`runsc`) where you can. Where mounting the runtime socket is not
acceptable, run without the sandbox.

## Reaching the UI privately

Bothy refuses to be exposed carelessly. Its exposure gate decides by the peer
address, never the Host header:

- Plain HTTP is served only to local peers: loopback, the container's default
  gateway (where Docker's published-port traffic comes from) and
  `server.local_cidrs`. LAN peers get plain HTTP only with the locked
  `server.insecure_lan`; public peers never do.
- Over TLS (`server.tls`, or a proxy listed in `server.trusted_proxies` that
  sends `X-Forwarded-Proto: https`) every peer passes the gate, and every route
  still requires a login.
- First-run setup works only from a local peer.

| Method | Recipes | How |
|---|---|---|
| SSH tunnel | VM | `ssh -L 8080:127.0.0.1:8080 <host>`, open `http://127.0.0.1:8080` |
| AWS SSM port forwarding | VM, ECS | no inbound port at all; see [vm.md](vm.md#reach-the-ui) |
| Tailscale | VM | `tailscale serve` in front of `127.0.0.1:8080` |
| `kubectl port-forward` | EKS | `kubectl -n bothy port-forward svc/bothy 8080:8080` |
| Internal ALB with TLS | ECS, EKS, Fargate | HTTPS listener with an ACM certificate, Bothy login behind it, optional ALB OIDC or Cognito login in front |

Never publish the port to the internet without TLS and authentication in
front of it. Bothy refuses plain HTTP from such peers (403, or 421 for an
unknown Host), but the port should not be reachable in the first place.

**Behind a proxy or ALB**, set:

- `server.public_url` to the HTTPS URL users open (it also allows that Host).
- `server.trusted_proxies` to the proxy's own addresses only, for an ALB the
  CIDRs of subnets that hold nothing but the ALB. A trusted peer's
  `X-Forwarded-For` names the client in logs and rate limits and its
  `X-Forwarded-Proto: https` counts as TLS, so any other host in those ranges
  could forge both. A forwarded address never counts as local, whatever it
  says, so it cannot open first-run setup.

**ALB health checks** use plain HTTP from the ALB's own address and send the
target's IP as the Host header. Bothy answers `GET` and `HEAD` on `/healthz`
and `/readyz` before the Host check and the exposure gate, so keep the target
group's default success code `200` and use `/healthz` (`/readyz` also turns 503
while the database or configuration is not ready). Every other path still gets
421 for an unknown Host and 403 for plain HTTP.

**First-run setup through an ALB** is refused, whatever client address the
ALB forwards. Run setup through a tunnel that reaches the gateway directly:
SSH, SSM port forwarding or `kubectl port-forward`; on Fargate, SSM port
forwarding through an instance in the VPC ([fargate.md](fargate.md#steps),
step 7).

## Backups

`bothy backup` writes a consistent tarball to `<data>/backups`: a snapshot of
the database plus memory files, `SOUL.md` and the config files. It never
contains the KEK, the workspaces or the values of `aws-sm` references, only
the references. Restore by stopping Bothy and unpacking the
tarball into the data directory. The image is distroless, so run the binary by
path:

| Recipe | Back up | Copy out |
|---|---|---|
| VM | `docker compose exec bothy /bothy backup` | `docker compose cp bothy:/data/backups/<file> .` |
| ECS | on the instance: `docker exec <bothy container> /bothy backup` | `docker cp <bothy container>:/data/backups/<file> .` |
| EKS, Fargate | `kubectl exec` (EKS); not available on Fargate | restore from volume snapshots |

Also snapshot the volumes: EBS snapshots with Data Lifecycle Manager or AWS
Backup for VMs, ECS instances and EKS volumes, AWS Backup for EFS. A snapshot
of a running SQLite database in WAL mode is crash-consistent; take `bothy
backup` first so the snapshot also holds a clean copy.

Keep the KEK in the secret store with its own backup and access policy. The
backup without the KEK holds only sealed secrets; the KEK without the backup is
useless. Store them apart.

## Upgrades

There is no auto-update. Every release is a signed tag; images are multi-arch,
signed with cosign (keyless, by the release workflow) and carry SLSA
provenance.

1. Read the release notes, then take a backup (above).
2. Pin the new version by digest. `sh install.sh --version X.Y.Z --compose DIR`
   verifies the signed compose bundle, whose `image:` lines carry the digests.
   For ECS and Kubernetes, copy those digests or read them with
   `docker buildx imagetools inspect ghcr.io/rielicat/bothy:X.Y.Z --format '{{json .Manifest}}' | jq -r .digest`.
3. Verify what you pin:

   ```sh
   cosign verify ghcr.io/rielicat/bothy@sha256:<digest> \
     --certificate-identity https://github.com/rielicat/bothy/.github/workflows/release.yml@refs/tags/vX.Y.Z \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   gh attestation verify oci://ghcr.io/rielicat/bothy@sha256:<digest> --repo rielicat/bothy
   ```

4. Roll forward by changing the digest. Database migrations run at start and
   take a backup first; to roll back after a migration, restore the backup
   from step 1 with the old image.

Use the same version for the gateway, sandboxd and the sandbox images.

## What was tested

With throwaway scripts on Docker 27.4 (Docker Desktop, linux/arm64) and
gateway and sandbox images built from this repository:

- The ECS layout without supplementary groups (gateway `65532:10001`, sandboxd
  `65532:65532`, KEK file written by a root init container that keeps only
  `CHOWN`): a box ran a command, wrote to the workspace and reached
  `example.com` through the egress proxy. With the gateway as `65532:65532`
  boxes fail closed.
- `disableNetworking` maps to Docker's `NetworkDisabled`: the container gets
  loopback only, like `network_mode: none`.
- The Kubernetes layout (read-only root, fsGroup-owned volume root, KEK from a
  secret-style symlinked file, sandbox `none`) and the Fargate layout (access
  point root `65532` `0700`, KEK and `/tmp` prepared by the init container):
  healthy, vault open with source `file`, no `data.permissions` finding, and
  `bothy backup` works in the Kubernetes layout.
- The exposure gate as an ALB meets it (the `httpapi` tests and a native
  gateway on macOS): 200 for `GET`/`HEAD` `/healthz` and `/readyz` with the
  target IP as Host over plain HTTP from the VPC, 421 for any other path with
  that Host, 403 for plain HTTP from the VPC, 200 through a trusted proxy with
  `X-Forwarded-Proto: https`; setup through the proxy 403, also when the
  forwarded address is loopback or in `server.local_cidrs`.
- The sandbox on Docker 25 (API 1.44): the Docker acceptance suite with
  sandboxd limited to API 1.44 (`API_LIMIT=1.44 deploy/sandbox/test/acceptance.sh`)
  on Docker Desktop 27.4, including a workspace swapped for a symlink, which
  sandboxd refuses. Not run on a real Docker 25 engine.
- Manifests: both ECS task definitions pass the AWS CLI's parameter
  validation for `RegisterTaskDefinition`; the Kubernetes manifests render with
  `kubectl kustomize` and decode strictly into the `k8s.io/api` v0.34 types;
  all JSON parses.

Not tested against real AWS accounts: the ECS agent's handling of these task
definitions, IAM policy evaluation, IRSA, ALB, EFS and the EKS sandboxd
overlay. Each recipe lists its own gaps.
