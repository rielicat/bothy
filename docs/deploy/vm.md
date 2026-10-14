# Single VM with Docker Compose

The gateway and the sandbox sidecar from [`deploy/compose.yaml`](../../deploy/compose.yaml)
on one Linux VM, on any cloud. This is the simplest recipe and the only one
that gives you shell, Python and MCP stdio tools without extra work.

## Pick the OS and Docker

- **Ubuntu 24.04 or 22.04 LTS, Debian 12 or 13**, with Docker Engine from
  [Docker's apt repository](https://docs.docker.com/engine/install/ubuntu/)
  (remove the distribution's `docker.io` first). The sandbox needs Docker 25
  or newer (engine API 1.44); Docker 26+ (API 1.45) adds volume subpaths and
  gVisor:

  ```sh
  docker version --format '{{.Server.APIVersion}}'   # 1.44 or higher
  ```

- **Amazon Linux 2023** ships Docker 25 (the October 2026 ECS-optimized
  AL2023 AMI has 25.0.16, per its
  [changelog](https://github.com/aws/amazon-ecs-ami/blob/main/CHANGELOG.md)).
  The sandbox works there: without volume subpaths, sandboxd binds the
  workspace and egress directories from the volumes' host paths and checks
  every box's mounts, by device and inode, against its own mounts of the run
  volume and, read-only, the work volume (the compose file mounts both)
  before the box is used. Boxes
  run under runc only (no gVisor), and `bothy doctor` shows
  `sandbox.bind_mounts` (info).
- Size: 2 GB of RAM covers the gateway (256 MB cap) and a few boxes; each
  running box may use up to `sandbox.limits.memory_mb` (512 MB by default).
  Encrypt the disk (EBS encryption on AWS).

## Install

```sh
curl -fsSLO https://raw.githubusercontent.com/rielicat/bothy/main/deploy/install.sh
sudo install -d -o "$USER" /opt/bothy
sh install.sh --compose /opt/bothy   # verifies checksums and Sigstore signatures; needs cosign
```

`install.sh` writes `/opt/bothy/compose.yaml` with digest-pinned images and a
`.env` with the Docker socket path and its group (`DOCKER_SOCK`,
`DOCKER_GID`) detected for this host, rootless Docker included.

## The vault key

Give Bothy its KEK as a Docker secret, and keep a copy in your secret store
before the first start:

```sh
cd /opt/bothy
(umask 077 && openssl rand -hex 32 > bothy_kek)
aws secretsmanager create-secret --name bothy/kek --secret-string file://bothy_kek   # or your own store
sudo chown 65532:65532 bothy_kek && sudo chmod 0400 bothy_kek
cat > compose.override.yaml <<'EOF'
services:
  bothy:
    secrets: [bothy_kek]
secrets:
  bothy_kek:
    file: ./bothy_kek
EOF
docker compose up -d --wait
docker compose logs bothy | grep setup
docker compose exec bothy /bothy vault status   # "kek_source": "docker_secret"
```

Compose reads `compose.override.yaml` automatically, and `install.sh` never
touches it, so upgrades keep it.

**Restarts.** The compose file sets `restart: unless-stopped`, so with
`systemctl enable docker` Bothy comes back after a reboot. Add a unit only if
you want the key off the disk: this one fetches it at boot into `/run`
(memory) and starts compose. It needs the AWS CLI and `secretsmanager:GetSecretValue`
on `bothy/kek` for the instance role, and `file: /run/bothy-kek/bothy_kek` in
`compose.override.yaml`. On other clouds replace the fetch with
`systemd-creds decrypt /etc/credstore.encrypted/bothy_kek /run/bothy-kek/bothy_kek`.

```ini
# /etc/systemd/system/bothy.service
[Unit]
Description=Bothy (docker compose)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/bothy
RuntimeDirectory=bothy-kek
RuntimeDirectoryMode=0700
ExecStartPre=/bin/sh -c 'umask 077 && aws secretsmanager get-secret-value --secret-id bothy/kek --query SecretString --output text > /run/bothy-kek/bothy_kek && chown 65532:65532 /run/bothy-kek/bothy_kek'
ExecStart=/usr/bin/docker compose up -d --wait
ExecStop=/usr/bin/docker compose down

[Install]
WantedBy=multi-user.target
```

## Hardening

- **Inbound traffic:** allow nothing but SSH from your admin network, or
  nothing at all with SSM. Docker inserts its own iptables rules for published
  ports, which bypass UFW and firewalld; the compose file publishes
  `127.0.0.1:8080` only. Keep it that way.
- **Docker access:** members of the `docker` group are root on the host. Keep
  it to admins, or run [rootless Docker](https://docs.docker.com/engine/security/rootless/)
  (`install.sh` then writes `DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock` and
  `DOCKER_GID=0`).
- **gVisor (optional, Docker 26+):** install `runsc` and register it with
  `"runtimeArgs": ["--host-uds=open"]` in `/etc/docker/daemon.json`
  ([SPEC §2.9](../SPEC.md#29-gvisor-runsc)); sandboxd uses it when its canary
  passes.
- **Instance metadata (EC2):** require IMDSv2. The gateway runs in a bridge
  network, one hop from the instance, so it needs a PUT response hop limit of 2
  to reach the instance role:

  ```sh
  aws ec2 modify-instance-metadata-options --instance-id i-0123456789abcdef0 \
    --http-tokens required --http-put-response-hop-limit 2 --http-endpoint enabled
  ```

  Every container on a Docker network can then reach the role. On this host
  that is the gateway and, if enabled, SearXNG; sandboxd and the boxes have no
  network. Keep the role to the Bedrock policy below.
- Turn on unattended OS updates.

## Models

**On EC2, Amazon Bedrock works with no keys.** Attach an instance profile with
[`deploy/aws/iam-bedrock-policy.json`](../../deploy/aws/iam-bedrock-policy.json)
(replace `<REGION>` and `<ACCOUNT_ID>`; keep one profile ARN and one model ARN
per model). It allows `bedrock:InvokeModel` and
`bedrock:InvokeModelWithResponseStream` (`ConverseStream` needs the latter)
only through the listed inference profiles, and
`bedrock:ListInferenceProfiles` and `bedrock:ListFoundationModels`, which do
not support resource-level permissions, for the model menu. For a `global.`
profile also list the region-less model ARN
`arn:aws:bedrock:::foundation-model/<model>`
([AWS: inference profile prerequisites](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-profiles-prereq.html)).

In setup choose "Use this server's AWS access". To configure it in code
instead, add to `compose.override.yaml`:

```yaml
services:
  bothy:
    environment:
      BOTHY_PROVIDERS: '[{"id": "bedrock", "type": "bedrock", "region": "us-east-1"}]'
      BOTHY_MODELS__MAIN: '["bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0"]'
      BOTHY_MODELS__FAST: '["bedrock/us.anthropic.claude-haiku-4-5-20251001-v1:0"]'
```

Bedrock enables a third-party model on its first invocation in the account.
Anthropic models also need a one-time use case form, and the first call needs
AWS Marketplace permissions
([AWS: model access](https://docs.aws.amazon.com/bedrock/latest/userguide/model-access.html)).
Do that once as an admin in the Bedrock console, so Bothy's role needs nothing
beyond the policy above.

**Elsewhere,** put provider API keys in the vault (Settings → Models), or for
Bedrock from outside AWS mount a read-only `~/.aws` into the gateway and set
`AWS_PROFILE`.

## Reach the UI

- SSH: `ssh -L 8080:127.0.0.1:8080 admin@host`, then open `http://127.0.0.1:8080`.
- AWS SSM, with no inbound port (Session Manager plugin installed locally,
  SSM Agent 2.3.672.0 or later on the instance;
  [AWS: port forwarding](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html#sessions-start-port-forwarding)):

  ```sh
  aws ssm start-session --target i-0123456789abcdef0 \
    --document-name AWS-StartPortForwardingSession \
    --parameters '{"portNumber":["8080"],"localPortNumber":["8080"]}'
  ```

- Tailscale: `tailscale serve --bg 8080` on the VM serves
  `https://<machine>.<tailnet>.ts.net`. Tell Bothy the URL and trust the proxy
  as Bothy sees it, which is the compose network's gateway:

  ```sh
  docker network inspect bothy_default -f '{{(index .IPAM.Config 0).Gateway}}'   # e.g. 172.18.0.1
  ```

  ```yaml
  services:
    bothy:
      environment:
        BOTHY_SERVER__PUBLIC_URL: https://bothy-vm.example-tailnet.ts.net
        BOTHY_SERVER__TRUSTED_PROXIES: '["172.18.0.1/32"]'
  ```

SSH and SSM arrive as local peers, so first-run setup works through them.
Tailscale forwards the client's tailnet address, and a forwarded address never
counts as local: run first-run setup through SSH or SSM.

## Backups and upgrades

```sh
docker compose exec bothy /bothy backup            # prints /data/backups/bothy-<time>.tar
docker compose cp bothy:/data/backups/bothy-<time>.tar .
```

Snapshot the disk as well (Data Lifecycle Manager on AWS). To upgrade, back up,
then:

```sh
sh install.sh --version X.Y.Z --compose /opt/bothy
cd /opt/bothy && docker compose pull && docker compose up -d --wait
```

## Without Docker: the native binary under systemd

On Linux the binary sandboxes tools with bubblewrap when `bwrap` 0.8+ is
installed and unprivileged user namespaces are allowed (Ubuntu 24.04 restricts
them through AppArmor; `bothy doctor` prints the fix). The KEK can then be a
systemd credential, encrypted with the host key or TPM2 (systemd 250+):

```sh
sudo sh install.sh --dir /usr/local/bin
(umask 077 && openssl rand -hex 32 > kek.hex)   # store a copy in your secret store
sudo install -d -m 0700 /etc/credstore.encrypted
sudo systemd-creds encrypt --name=bothy_kek kek.hex /etc/credstore.encrypted/bothy_kek && shred -u kek.hex
```

```ini
# /etc/systemd/system/bothy.service
[Unit]
Description=Bothy gateway
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/bothy serve
DynamicUser=yes
StateDirectory=bothy
StateDirectoryMode=0700
RuntimeDirectory=bothy
Environment=BOTHY_STORAGE__DATA_DIR=/var/lib/bothy
LoadCredentialEncrypted=bothy_kek:/etc/credstore.encrypted/bothy_kek
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

With `vault.kek: auto` Bothy finds `$CREDENTIALS_DIRECTORY/bothy_kek`. The
native gateway listens on `127.0.0.1:8080`; reach it the same ways as above.

## Not tested here

The compose deployment itself is covered by `make e2e-docker`. The Docker 25
mode ran as the Docker acceptance suite with sandboxd limited to engine API
1.44 on Docker Desktop 27.4, not on Amazon Linux 2023's Docker 25. The two
systemd units, Tailscale itself (only the proxy-header handling was tested)
and the EC2 hop-limit behaviour were not run as part of this recipe.
