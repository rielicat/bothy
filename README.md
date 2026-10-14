# Bothy

A small, self-hosted AI agent that lives in your chat apps and is secure by
default. One static Go binary, one SQLite file, your own API keys or local
models.

> **Status: v0.1 is in development.** Nothing is released yet: the registry
> images, the install script and the comparison figures below describe the
> v0.1.0 release and its CI-enforced targets. You can build and run it from
> source today ([Quickstart](#quickstart)). The design is in
> [docs/SPEC.md](docs/SPEC.md).

## What it is

Bothy is an always-on personal (or household / small-team) agent. You talk to
it in the web UI, Telegram, Discord or Slack; it answers with any model you
bring, keeps plain-file memory, runs scheduled jobs and a heartbeat, and uses
tools: web fetch and search, workspace files, MCP servers and, when a sandbox
is available, a shell and Python in throwaway containers.

- **One binary, three roles**: `bothy serve` (the gateway), `bothy sandboxd`
  (a sidecar that alone talks to the container runtime) and `bothy sbx-init`
  (PID 1 inside tool boxes). State is one SQLite file plus a few plain files.
- **Bring your own keys**: native Anthropic, OpenAI, Gemini and Ollama clients,
  plus any OpenAI-compatible endpoint (OpenRouter, Groq, xAI, DeepSeek, LM
  Studio, vLLM). Ollama, LM Studio and vLLM are detected automatically (see
  [Local models](#local-models) for Docker on Linux). Works with small local
  models from a 16K context window. On AWS, [Amazon Bedrock](#amazon-bedrock)
  uses the server's own AWS role or SSO profile instead of a key.
- **Chat-native approvals**: risky actions arrive as cards in the chat you are
  using, rendered from the exact action (command, URL, recipient, diff), never
  from the model's prose.
- **Honest costs**: integer micro-dollar metering, a per-call context
  breakdown, hard per-run and daily caps, and a kill switch.

## Why another agent

The popular agents are large, and their safety controls are opt-in. Bothy's
bet is that a personal agent should be small enough for a Raspberry Pi and
safe before you configure anything.

| | Bothy v0.1 (targets) | Hermes Agent | OpenClaw | Grok Bot | Eidon |
|---|---|---|---|---|---|
| Hosting | self-hosted | self-hosted | self-hosted | xAI cloud only | self-hosted |
| Image size | ≤ 30 MB, CI-enforced | 1,121 MB | 1,236 MB | n/a | 1,416 MB, amd64 only |
| Idle memory | ≤ 60 MB with 3 channels, CI-enforced | 1 GB minimum (its docs) | 150–500 MB (unverified, third-party) | n/a | not published |
| Tool sandbox | on by default; no network except a per-box egress proxy | host execution on native installs | off by default | one shared cloud computer per account | bot runs only; kernel-dependent (its docs) |
| Secrets | envelope-encrypted vault, never inside a box | — | store not encrypted at rest (its docs) | n/a (hosted) | encrypted vault |
| Models | any provider or local model, from 16K context | rejects models under 64K context | any | no model choice; requests may route to third-party models (its docs) | 20+ providers |
| License | Apache-2.0 | MIT | MIT | proprietary | AGPL-3.0 |

Competitor figures come from the projects' own documentation and container
registries, read on 2026-10-12. Bothy's figures are budgets that CI enforces from the
first commit; we will publish measured numbers with the release. A pre-release
build measured 9.1–9.8 MB compressed for the gateway image (arm64, amd64,
armv7) and 28 MB idle RSS on linux/arm64 after 10 minutes with Telegram,
Discord and Slack (test servers) connected.

What we do not try to win on: the number of chat platforms and providers,
where Hermes and OpenClaw are far ahead.

## Security by default

- No unauthenticated mode. A one-time setup token is printed at first boot;
  the compose file publishes the port on loopback only, and plain HTTP is
  refused to non-local peers.
- Tools run in throwaway containers with `--network none`, a read-only root,
  no capabilities, uid 10001, and gVisor when it is installed. The gateway
  never holds the Docker socket; a separate sidecar exposes only a fixed,
  hardened container spec. Without a sandbox, shell and Python are simply
  absent: nothing ever falls back to running on the host.
- Outbound traffic from tools goes through a per-box proxy with an allowlist,
  SSRF and DNS-rebinding protection, and a hold-for-approval flow for new
  destinations.
- Group chats and guests are separate principals with no access to the
  owner's memory or vault. A session that has seen private data and untrusted
  content cannot send anything out without approval.
- Append-only, hash-chained audit log. Signed releases with SBOMs and SLSA
  provenance; no telemetry and no auto-update.

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## Quickstart

There is no release yet, so today you build from a checkout; the commands
under [After the v0.1.0 release](#after-the-v010-release) start working with
the first signed release. Either way you open the printed
`http://127.0.0.1:8080/setup#t=…` URL; the setup wizard walks you through the
owner account, a model provider (with a live test call), safety presets, a
daily spend cap and pairing your chat apps by QR code.

### From source

Needs Docker 26 or newer (Docker Desktop works). The image build compiles the
web UI and the binary itself:

```sh
git clone https://github.com/rielicat/bothy && cd bothy
make image IMAGE=bothy:dev
```

**Single container** (chat, channels, web tools and HTTP MCP servers; no shell
or Python tools, because there is no sandbox):

```sh
docker run -d --name bothy -p 127.0.0.1:8080:8080 -v bothy_data:/data bothy:dev
docker logs bothy 2>&1 | grep setup
```

**Compose** (recommended: adds the sandbox sidecar, so the model can run shell,
Python and MCP stdio servers in throwaway boxes). `deploy/compose.source.yaml`
swaps the release images for your local builds. Build both tool box images
(`node` runs npm-based MCP servers) and record them, with the Docker socket's
group as containers see it, in `deploy/.env`, which every later compose
command reads:

```sh
docker buildx build -f deploy/sandbox/Dockerfile --target base -t bothy-sandbox:dev --load .
docker buildx build -f deploy/sandbox/Dockerfile --target node -t bothy-sandbox-node:dev --load .
img() { docker image inspect -f '{{.Id}}' "$1"; }
cat >deploy/.env <<EOF
BOTHY_SBX_IMAGES=base=$(img bothy-sandbox:dev),node=$(img bothy-sandbox-node:dev)
DOCKER_GID=$(docker run --rm --entrypoint stat -v /var/run/docker.sock:/s bothy-sandbox:dev -c %g /s)
EOF
docker compose -f deploy/compose.yaml -f deploy/compose.source.yaml up -d --wait
docker compose -f deploy/compose.yaml -f deploy/compose.source.yaml logs bothy | grep setup
```

Rootless Docker: use `$XDG_RUNTIME_DIR/docker.sock` in place of
`/var/run/docker.sock` above and add `DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock`
to `deploy/.env` (see `deploy/.env.example`). If `DOCKER_GID` is wrong, shell
and Python stay off and `docker compose -f deploy/compose.yaml -f
deploy/compose.source.yaml logs sandboxd` prints the exact value to set.

**Moving from the single container to compose** keeps the `bothy_data`
volume. Stop and remove the old container first (`docker rm -f bothy`), then
start compose: on its first start the gateway moves the vault key from
`/data/keys/kek` to the new `bothy_keys` volume and the workspaces from
`/data/work` to `bothy_work`, and logs both moves. To use a Docker secret for
the key instead, put the existing key in it (`docker cp bothy:/data/keys/kek
bothy_kek` before removing the container); a newly generated key cannot open
the vault.

**Native binary** (Linux, macOS or Windows; on Linux the shell tool uses
bubblewrap when it is installed). Bothy creates the data directory with mode
0700; without `BOTHY_STORAGE__DATA_DIR` it uses `/data`, the image's volume:

```sh
make ui build                            # web UI (Node 24) and bin/bothy
BOTHY_STORAGE__DATA_DIR=~/bothy-data bin/bothy serve
```

### After the v0.1.0 release

```sh
docker run -d --name bothy -p 127.0.0.1:8080:8080 -v bothy_data:/data ghcr.io/rielicat/bothy:0.1.0

curl -fsSLO https://raw.githubusercontent.com/rielicat/bothy/main/deploy/install.sh
sh install.sh --compose ~/bothy          # verifies checksums and Sigstore signatures
cd ~/bothy && docker compose up -d       # add --profile search for SearXNG
docker compose logs bothy | grep setup

sh install.sh                            # native binary into ~/.local/bin/bothy
BOTHY_STORAGE__DATA_DIR=~/bothy-data bothy serve
```

### Local models

The setup wizard looks for Ollama (port 11434), LM Studio (1234) and vLLM
(8000) on this machine and, from a container, on the Docker host through
`host.docker.internal`. Compose maps that name; with `docker run` on Linux add
`--add-host host.docker.internal:host-gateway`. On a Linux host the container
reaches the host through the Docker bridge, not loopback, so the server must
listen beyond `127.0.0.1`: start Ollama with `OLLAMA_HOST=0.0.0.0` (for the
systemd service, `sudo systemctl edit ollama` and add
`Environment=OLLAMA_HOST=0.0.0.0`), turn on LM Studio's "Serve on Local
Network", or run vLLM with `--host 0.0.0.0`, and keep that port closed to the
rest of your network. Docker Desktop on macOS and Windows needs neither step.

### Amazon Bedrock

Bedrock needs no API key: Bothy signs requests with this server's AWS
credentials, resolved through the standard AWS chain (environment, shared
config and credentials files including SSO sessions, web identity / IRSA,
ECS and EKS container credentials, EC2 instance roles over IMDSv2) when a
call needs them, kept in memory only and never shown. The setup wizard
offers "Use this server's AWS access" when they resolve and lists the Claude
and Nova models the account can use; the doctor reports where the
credentials come from (`AWS SSO, profile dev`, `EC2 instance role (IMDSv2)`),
never their values.

```yaml
providers:
  - {id: bedrock, type: bedrock, region: us-west-2}          # profile: optional shared-config profile
models:
  main: [bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0]
```

- **On AWS** (EC2, ECS, EKS), give the instance, task or service account a
  role that allows `bedrock:InvokeModelWithResponseStream` (and
  `bedrock:ListFoundationModels`, `bedrock:ListInferenceProfiles` for the
  model list); no Bothy config beyond the provider is needed. For the gateway
  container on EC2, set the instance metadata hop limit to 2 so it reaches
  IMDSv2.
- **On a laptop**, sign in on the host (`aws sso login --profile dev`). A
  native `bothy serve` reads `~/.aws` itself. With compose, uncomment the
  Bedrock lines of the bothy service in `compose.yaml`: they set `AWS_PROFILE`
  and `AWS_REGION` and mount `~/.aws` read-only into the gateway (on Linux
  also grant uid 65532 read access with the `setfacl` line shown there). Run
  `aws sso login` again on the host when the session expires.

Only the gateway's credential chain may reach the instance metadata service
and the container credential endpoints; tool boxes, `web_fetch` and MCP
servers cannot. `credential_process` profiles are refused, since the program
would run outside Bothy's egress control.

## Development

```sh
make build test lint      # binary, race tests, linters and import rules
make ui gen e2e           # web UI, generated TypeScript types, black-box tests
make e2e-docker           # real compose deployment: gateway, sandboxd, tool boxes
make image budgets        # image size and layout, cold start, idle RSS, token overhead
```

Read [CONTRIBUTING.md](CONTRIBUTING.md) first: module ownership
([OWNERSHIP.md](OWNERSHIP.md)), the import rules, the 14-day dependency
cooldown and the DCO sign-off are checked in CI; the interface change process
and the comment style are upheld in review.

## License

[Apache License 2.0](LICENSE), with a Developer Certificate of Origin for
contributions. See [NOTICE](NOTICE).
