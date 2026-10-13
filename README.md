# Bothy

A small, self-hosted AI agent that lives in your chat apps and is secure by
default. One static Go binary, one SQLite file, your own API keys or local
models.

> **Status: v0.1 is in development.** Nothing is released yet: the image
> names, install commands and numbers below describe the v0.1.0 release and
> its CI-enforced targets, not something you can download today. The design is
> in [docs/SPEC.md](docs/SPEC.md).

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
  Studio, vLLM). Ollama and LM Studio are detected automatically. Works with
  small local models from a 16K context window.
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
first commit; we will publish measured numbers with the release.

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

**Single container** (chat, channels, web tools and HTTP MCP servers; no shell
or Python tools, because there is no sandbox):

```sh
docker run -d --name bothy -p 127.0.0.1:8080:8080 -v bothy_data:/data ghcr.io/rielicat/bothy:0.1.0
docker logs bothy 2>&1 | grep setup     # open the printed http://127.0.0.1:8080/setup#t=… URL
```

**Compose** (recommended: adds the sandbox sidecar for shell, Python and MCP
stdio servers, and optionally SearXNG for web search):

```sh
curl -fsSLO https://raw.githubusercontent.com/rielicat/bothy/main/deploy/install.sh
sh install.sh --compose ~/bothy          # verifies checksums and Sigstore signatures
cd ~/bothy && docker compose up -d       # add --profile search for SearXNG
docker compose logs bothy | grep setup
```

**Native binary** (Linux, macOS or Windows; on Linux the shell tool uses
bubblewrap when it is installed):

```sh
sh install.sh                            # installs ~/.local/bin/bothy
bothy serve
```

The setup wizard then walks you through the owner account, a model provider
(with a live test call), safety presets, a daily spend cap and pairing your
chat apps by QR code.

## Development

```sh
make build test lint      # binary, race tests, linters and import rules
make ui gen e2e           # web UI, generated TypeScript types, black-box tests
```

Read [CONTRIBUTING.md](CONTRIBUTING.md) first: module ownership
([OWNERSHIP.md](OWNERSHIP.md)), the interface change process, the comment
style and the 14-day dependency cooldown are all checked in CI.

## License

[Apache License 2.0](LICENSE), with a Developer Certificate of Origin for
contributions. See [NOTICE](NOTICE).
