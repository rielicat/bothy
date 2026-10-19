# Bothy v0.1: implementation spec

**Status:** final architecture for v0.1, 2026-10-12. This is the single source of truth for the skeleton commit and for the 12 parallel implementers. **v0.2** (accepted brief `docs/design/v0.2.md`, 2026-10-14) is folded in: §14 summarizes it, and the affected sections carry "v0.2" notes. Base: the "lean" proposal (judged best by all three judges), with every fatal flaw and must-fix item from the judge reports resolved, and the best ideas grafted from "secure" and "ux".

**Module:** `github.com/rielicat/bothy` · **License:** Apache-2.0 + DCO · **Go:** `go 1.26`, `toolchain go1.27.2`, `CGO_ENABLED=0`.

**How to read this.** Prose is short. Go code, SQL, YAML and route tables are normative and are copied into the skeleton as-is. All Go listings (§2.5, §4, §7.4, §8.4, §12.1) were compiled together as one module (`go vet`, `go build`, cross-compiled for linux/arm64 and windows), and the skeleton wiring was booted (it serves `/healthz` and shuts down cleanly on SIGTERM). The DDL in §6 was applied to SQLite 3.53 with smoke tests (FTS after VACUUM, append-only triggers, one-active-run index, grant checks). The YAML in §7.1 decodes strictly into the §7.4 struct. Items marked *(verify)* must be checked when the skeleton is built; none of them changes an interface.

**Words.** *Gateway* = `bothy serve`. *Box* = a throwaway tool container (or bwrap jail). *Principal* = the owner of sessions, workspace, memory and grants: an *account*'s own data, a *channel* (a group chat) or a *teammate* (a person in a chat app); channels and teammates are *spaces* that belong to the team and have no owner. *Card* = an approval card rendered from a structured Action. *Lead* = the integrator who writes the skeleton and owns frozen files.

---

## 1. Overview and key decisions

Bothy is one static Go binary with three roles chosen by subcommand: `serve` (the gateway), `sandboxd` (a sidecar that alone talks to the container runtime) and `sbx-init` (PID 1 and exec wrapper inside boxes). State is one SQLite file plus a few plain files under `/data`. No other service is required. Tools never run in the gateway process, except workspace file tools confined with `os.Root`. When no sandbox is available, exec-class tools are absent, a red banner says why, and nothing ever falls back to host execution.

| # | Decision | Rationale |
|---|---|---|
| D1 | One binary: `serve`, `sandboxd`, `sbx-init`, `doctor`, `security-audit`, `setup-token`, `user`, `vault`, `config`, `audit`, `backup`, `healthcheck [sandboxd]` (gateway `/healthz` at `server.listen`, over TLS when `server.tls` is set, or the sidecar's socket), `version`. `help`/`-h`/`--help` exit 0; an unknown command or malformed admin command exits 2 before any state is opened | One image, one SBOM, one signature, no version skew between gateway and sidecar. |
| D2 | One frozen `internal/core` package holds every cross-module type and interface. Modules import only `core`, `config`, the stdlib and their own approved deps. `cmd/bothy` wires concrete types. | No import cycles; one rule for 12 agents; fakes are trivial. |
| D3 | Hand-written LLM clients: Anthropic Messages, OpenAI Responses (the default for type `openai`) and Chat Completions (+ every OpenAI-compatible base URL), chosen per provider by `providers[].api`, Gemini `streamGenerateContent`, **native Ollama `/api/chat`** (so `num_ctx` is honoured) and, in v0.2, Amazon Bedrock `ConverseStream`, whose mapping is hand-written while the AWS SDK is used only for credential resolution, SigV4 signing and event-stream decoding, with SDK retries off (§14.1). Golden fixtures are copied from vendor docs and SDK test data, not invented. | SDKs retry implicitly (hiding double billing from the meter), read env vars, bypass our dialer and add several MB. We call about six endpoints. OpenAI defaults to Responses because Chat Completions refuses function tools while GPT-5.4 and later reason, and GPT-6 Astra and GPT-6.1 Sol, which do not accept `reasoning_effort: "none"`, cannot call tools there at all. |
| D4 | Channel libraries: **disgo** (Discord gateway) and **slack-go** (Socket Mode). **Hand-written Telegram** long-poll client. | No real API keys exist for testing; the two stateful WebSocket protocols are the riskiest hand-writes. Telegram is plain JSON over HTTP. |
| D5 | Official MCP `go-sdk` v1.8.0, client side only. | Two spec eras (2026-07-28 and older); conformance is not worth owning. |
| D6 | `modernc.org/sqlite` v1.60.1 (SQLite 3.53.4), WAL, FTS5, STRICT tables, `messages.id INTEGER PRIMARY KEY` as FTS content rowid. Startup refuses SQLite < 3.53.4. | Pure Go. INTEGER PK survives VACUUM / VACUUM INTO, which renumbers implicit rowids. |
| D7 | Stdlib `net/http` ServeMux patterns; hand-written migrator on `PRAGMA user_version`; hand-written SQL. No chi, goose or sqlc. | Fewer deps and tools. Schema is frozen; only the lead adds `0002_*.sql`. |
| D8 | Config: one YAML schema; layers defaults < `bothy.yaml` < env < **`bothy.ui.yaml`** (sparse overlay written by the web UI, 0600, audited). Per-key effective source with file:line. Bad YAML boots **safe mode**, never exit 1. | Inspectable, GitOps-friendly, no hidden DB state. |
| D9 | Web streaming is SSE + REST, not WebSocket. | No WS origin surface; proxies work; `Last-Event-ID` resume. |
| D10 | **Sandbox = `sandboxd` sidecar** with a fixed-schema API on a unix socket. Boxes run `--network none`, no DNS, read-only rootfs, uid 10001, and reach the network only through a **per-box unix-socket egress proxy** (the socket is the identity; no tokens in the box). Native Linux: `bwrap` backend. Neither: exec disabled (fail closed). | The gateway never holds the runtime socket; a compromised gateway can only request the compiled-in hardened spec. |
| D11 | sandboxd uses a hand-written Docker Engine API client (~13 endpoints; API ≥ 1.45 for volume subpaths, 1.44 with checked bind mounts). | `moby/client` drags in containerd/OCI/otel. |
| D12 | Kill semantics: every exec runs under `sbx-init run --timeout`; `/stop` execs `sbx-init killall` (kill(-1, SIGKILL) as the box uid, PID 1 survives), falling back to container kill + recreate. | Docker has no exec-kill API. |
| D13 | Plan/Exec split: `Tool.Plan` returns a structured `Action`; policy, cards, audit and loop detection read only the Action; `Exec` runs exactly that Action (persisted for parked runs). | Cards cannot be forged by model prose. |
| D14 | Policy order: **floors → rules → grants → first contact → class defaults**. Floors (trifecta, financial, admin/self-modification, restricted spaces, tainted memory writes, URL provenance) cannot be overridden. Grants are always narrow. | Fixes grant-before-trifecta. |
| D15 | Principals are an **account** (web user), a **channel** (group chat) or a **teammate** (a person in a chat app). Channels and teammates belong to the team and have no owner: no account's memory or vault, their own workspace and daily line, approvals to chat approvers and admins. People in chat apps are never linked to accounts. | Chat participants must not reach anyone's private data, and a bot shared by a team isn't one person's. |
| D16 | Every by-ID repository read takes a `core.Viewer` and returns `ErrNotFound` across principals; IDOR suites at store and HTTP layers. | Multi-user isolation enforced at the data layer. |
| D17 | No secret ever enters a box (MCP stdio included). Credentialed integrations use Streamable HTTP MCP with vault-injected headers. Credential-injecting proxy is P1. | P0 baseline #3. |
| D18 | Money is integer micro-USD (`core.Micros`). Prices are micros per 1M tokens. | No float drift in caps. |
| D19 | Argon2id m=19 MiB, t=2, p=1, one global hashing slot. | OWASP minimum within the RSS budget. |
| D20 | Audit log: append-only triggers + sha256 hash chain; no online prune; offline archive inserts an anchor row. | Tamper-evident at negligible cost; host root can still rewrite it, so external anchoring stays P2. |
| D21 | Web UI: Svelte 5 runes + Vite 8 + Tailwind 4 SPA, history router, hand-written escape-all markdown renderer (no images, no raw HTML), external CSS only (CSP without `unsafe-inline`). Only `svelte` ships at runtime. First screen JS ≤ 120 KB gz; route groups load as lazy chunks, all JS ≤ 260 KB gz. | Size and an exfiltration control in one. |
| D22 | Run semaphores (global 4, per user 2) gate LLM streaming and tool execution only; they are **released while waiting for approval or parked**. | Pending approvals cannot starve other runs. |
| D23 | Approval defaults: interactive 15 min then deny; scheduled/heartbeat 2 min then **park** (24 h); API tokens `on_approval: deny` (opt-in `wait`, never park). | No stalled overnight jobs; no silent auto-approve. |
| D24 | Cut from v0.1: passkeys/OIDC/TOTP, OS keychain and SOPS KEK sources, WASM Python, credential-injecting proxy, vector memory, age-encrypted export, Landlock. See §13. Sign-in with Google and GitHub came back later with hand-written HTTP and no new dependency (§15); generic OIDC stays cut. | Bytes and deps out of proportion to v0.1 value. |

---

## 2. Deployment topology

### 2.1 Modes

| Mode | Processes | Exec tools (`shell`, `python`, MCP stdio) | Everything else |
|---|---|---|---|
| **Single container** (`docker run -p 127.0.0.1:8080:8080 -v bothy_data:/data ghcr.io/rielicat/bothy:0.1.0`) | `bothy serve` | **Absent.** Red banner "No sandbox: shell, python and MCP stdio are off. Use the compose file." Doctor finding `sandbox.unavailable` (medium). The system facts block tells the model. | Chat, channels, `fs_*` tools (`os.Root`), `web_fetch`/`web_search`, HTTP MCP, memory, scheduler, approvals |
| **Compose** (recommended) | `bothy serve` + `bothy sandboxd` | Per-session boxes once sandboxd's self-test passes; runsc when registered and probed | Same |
| **Native Linux binary** | `bothy serve` | `bwrap` backend when `bwrap` ≥ 0.8 exists and the user-namespace probe passes; else absent | Same |
| **Native macOS/Windows** | `bothy serve` | Absent (P2: VM tier) | Same |

**Install channels:** `docker run` / `docker compose` (images linux/amd64 and linux/arm64; linux/armv7 gateway image best-effort; sandbox images amd64/arm64), static binaries for linux/darwin/windows amd64+arm64 from goreleaser, `deploy/install.sh` (downloads, verifies checksum and cosign signature, installs to `~/.local/bin`), and a Homebrew tap. There is no auto-update code; only signed tagged releases.

`sandbox.backend: auto` probes sandboxd socket → bwrap → none. The result is published as `sandbox.status`, shown in the UI header chip, `/api/sandbox`, doctor, and the S2 facts block.

**Directory fallbacks for single-container mode.** `storage.work_dir: ""` means auto: `/work` if it is a mount point (checked via `/proc/self/mountinfo`), else `<data_dir>/work`. `vault.keyfile: ""` means auto: `/keys/kek` if `/keys` is a mount point, else `<data_dir>/keys/kek` (doctor: `vault.kek_colocated`, medium). `config.ResolveDirs` returns these as `config.Dirs`, plus `LegacyWork` and `LegacyKeyfile`: the `<data_dir>` paths that a mounted `/work` or `/keys` replaced ("" otherwise), so a single container moved to compose (what doctor recommends) keeps its key and workspaces:
- `config.MoveLegacyWork(dirs)`, called from `cmd/bothy` `buildBase`, moves the entries of `<data_dir>/work` into `/work` when that volume is still empty: renamed on one filesystem; across volumes copied through `os.Root` into a temporary directory inside `/work` and renamed into place only once complete, so a failed copy leaves `<data_dir>/work` as it was.
- At boot the vault moves `<data>/keys/kek` to `/keys/kek` when `/keys` has just been mounted (no keyfile there yet) and that key opens the vault (`kek_check`); a key that does not is left where it is. When a configured source (a Docker secret, say) does not match the vault, the sealed reason names the working auto-generated keyfile ("…; the auto-generated key in <path> opens it: provide that key instead"). The `vault.kek_colocated` fix says to mount a separate volume at `/keys` (Bothy moves the key there on its next start), or to give the same key as a Docker secret (`docker cp <container>:<keyfile> bothy_kek`, mount it as `/run/secrets/bothy_kek`, restart, then delete the old keyfile), since a new key in the secret would seal the vault.

`storage.run_dir` keeps the default `/run/bothy` where it exists (the images create it, compose mounts the run volume there). A native install cannot create it as an unprivileged user, so when the default does not exist `config.ResolveDirs` uses `$XDG_RUNTIME_DIR/bothy`, else `<data_dir>/run`; an explicit `run_dir` is kept as is. The config default stays `/run/bothy`.

**Fix texts for the sandbox** point at the README / `install.sh` compose flow (`sh install.sh --compose ~/bothy`, then `docker compose up -d` in `~/bothy`), never at `docker compose -f deploy/compose.yaml up -d`: `deploy/compose.yaml` keeps digest placeholders until release, and an `install.sh` deployment has no `deploy/` directory. An unreachable sandboxd gets "start the sandboxd sidecar with `docker compose up -d` in your compose directory; `docker compose logs sandboxd` says why it stopped". Fixes that name a CLI command (`bothy backup`, `bothy security-audit --fix`, `bothy config check`) add the container form, e.g. `bothy config check (Docker: docker compose exec bothy /bothy config check)`, since the distroless image has no `bothy` on `PATH`.

### 2.2 Compose file (`deploy/compose.yaml`, lead-owned)

Volume names are fixed because sandboxd mounts subpaths of them by engine name (on Docker 25, binds of the same directories under the host paths the engine reports for those names, §2.6).

```yaml
name: bothy
x-hardening: &hardening
  read_only: true
  cap_drop: [ALL]
  security_opt: ["no-new-privileges:true"]
  restart: unless-stopped
services:
  bothy:
    <<: *hardening
    image: ghcr.io/rielicat/bothy:0.1.0@sha256:<digest>
    command: ["serve"]
    user: "65532:65532"
    group_add: ["10001"]                 # shared workspace group with tool boxes
    ports: ["127.0.0.1:8080:8080"]       # loopback only: Docker bypasses UFW
    environment:
      BOTHY_DEPLOYMENT: container        # listen 0.0.0.0:8080 inside; exposure gate still applies
      BOTHY_SANDBOX__BACKEND: sandboxd
      GOMEMLIMIT: 48MiB
      # Amazon Bedrock uses this server's AWS access; Bothy never stores AWS keys.
      # On an AWS host the role works with no config here: an ECS task role, EKS
      # IRSA / Pod Identity, or an EC2 instance role (set the metadata hop limit
      # to 2 so containers reach IMDSv2; that opens IMDS to the host's other
      # containers too). On a laptop, sign in on the host (aws sso login
      # --profile <name>), name the profile and region below and mount ~/.aws
      # read-only (the second volumes line replaces the first). Bothy cannot save
      # a renewed SSO token there: run aws sso login on the host again when the
      # doctor asks, or add the token cache writable to that volumes list:
      # "${HOME}/.aws/sso/cache:/home/nonroot/.aws/sso/cache". Linux: let uid
      # 65532 read the files (and write sso/cache when it is mounted writable):
      # setfacl -R -m u:65532:rX ~/.aws && setfacl -R -d -m u:65532:rX ~/.aws
      # AWS_PROFILE: my-sso-profile
      # AWS_REGION: us-west-2
      # HOME: /home/nonroot
    volumes: [data:/data, keys:/keys, work:/work, run:/run/bothy]
    # volumes: [data:/data, keys:/keys, work:/work, run:/run/bothy, "${HOME}/.aws:/home/nonroot/.aws:ro"]
    tmpfs: ["/tmp:size=32m,mode=1777,nosuid,nodev,noexec"]
    mem_limit: 256m
    pids_limit: 256
    extra_hosts: ["host.docker.internal:host-gateway"]   # Ollama / LM Studio on the host
    healthcheck: {test: ["CMD", "/bothy", "healthcheck"], interval: 30s, timeout: 3s, retries: 3, start_period: 30s, start_interval: 1s}
    # secrets: [bothy_kek]               # optional Docker secret KEK: also uncomment the top-level secrets block
  sandboxd:
    <<: *hardening
    image: ghcr.io/rielicat/bothy:0.1.0@sha256:<digest>
    command: ["sandboxd"]
    network_mode: none                   # sandboxd itself has no network
    user: "${SANDBOXD_USER:-65532:65532}"
    group_add: ["${DOCKER_GID:-999}", "65532"]   # the socket's group inside containers; Docker Desktop and rootless Docker: DOCKER_GID=0
    environment:
      BOTHY_SBX_ENGINE: unix:///var/run/docker.sock
      BOTHY_SBX_WORK_VOLUME: bothy_work
      BOTHY_SBX_RUN_VOLUME: bothy_run
      BOTHY_SBX_IMAGES: "base=ghcr.io/rielicat/bothy-sandbox:0.1.0@sha256:<d1>,node=ghcr.io/rielicat/bothy-sandbox-node:0.1.0@sha256:<d2>"
      BOTHY_SBX_RUNTIME: auto            # auto | runsc | runc
      BOTHY_SBX_MAX_BOXES: "8"
      BOTHY_SBX_GATEWAY_UID: "65532"
    volumes:
      - ${DOCKER_SOCK:-/var/run/docker.sock}:/var/run/docker.sock
      - run:/run/bothy
      - work:/work:ro                    # checks box mounts on Docker 25, which has no volume subpaths
    mem_limit: 64m
    pids_limit: 64
    healthcheck: {test: ["CMD", "/bothy", "healthcheck", "sandboxd"], interval: 30s, timeout: 3s, retries: 3, start_period: 30s, start_interval: 1s}
  searxng:                              # optional: docker compose --profile search up -d
    <<: *hardening
    profiles: ["search"]
    image: docker.io/searxng/searxng@sha256:<digest>
    cap_add: [SETUID, SETGID]
    tmpfs: ["/tmp"]
    environment:
      SEARXNG_SECRET: "${SEARXNG_SECRET:-}"   # install.sh writes a random value into the .env next to this file
    volumes:
      - ./searxng/settings.yml:/etc/searxng/settings.yml:ro   # search.formats [html, json], limiter off, no image proxy
      # (verify) whether the image's entrypoint still needs a writable /etc/searxng (uwsgi.ini); if so, add a tmpfs
volumes:
  data: {name: bothy_data}
  keys: {name: bothy_keys}
  work: {name: bothy_work}
  run:  {name: bothy_run}
# Docker secret KEK, mounted at /run/secrets/bothy_kek (uncomment with the
# bothy service's secrets line). The file holds 64 hex characters
# (openssl rand -hex 32) and must be readable by uid 65532 inside the
# container: chown 65532:65532 bothy_kek && chmod 0400 bothy_kek.
# On an existing install the vault is sealed under the current key, so a new
# one leaves it sealed: put that key in the file instead, e.g.
#   docker run --rm -v bothy_keys:/k:ro busybox cat /k/kek > bothy_kek
# secrets:
#   bothy_kek:
#     file: ./bothy_kek
```

`deploy/.env.example` documents `DOCKER_GID`, the socket's group as containers see it (rootful Docker on Linux: `stat -L -c %g /var/run/docker.sock`; Docker Desktop on macOS, Windows and Linux: `0`, the socket's group inside its VM; rootless Docker: `0`, because containers share dockerd's user namespace, where the socket's owner is root, so the socket shows as `root:root` inside), `DOCKER_SOCK` (rootless Docker: `$XDG_RUNTIME_DIR/docker.sock`), `SANDBOXD_USER` (rarely changed: rootless Docker works with the default plus `DOCKER_GID=0`, not with `SANDBOXD_USER=<host uid:gid>`) and `SEARXNG_SECRET`. `install.sh --compose` detects the socket (`DOCKER_HOST`, the docker context, `$XDG_RUNTIME_DIR/docker.sock`) and rootless or Docker Desktop, and writes `DOCKER_SOCK` and `DOCKER_GID` into the `.env` next to `compose.yaml`. The bothy service's commented `secrets: [bothy_kek]` line goes with the commented top-level `secrets: bothy_kek: file: ./bothy_kek` block, whose comment says how to put the existing key in that file. `deploy/searxng/settings.yml` (lead) enables the JSON output format that `web_search` needs (SearXNG answers `format=json` with 403 otherwise).

### 2.3 Image layout and uids (`deploy/Dockerfile`, lead-owned)

- Build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$V" ./cmd/bothy` after `make ui` (pre-gzipped assets into `internal/webui/dist`).
- Final stage: `gcr.io/distroless/static-debian13:nonroot@sha256:<digest>` (the sandbox images are trixie too; *verify* the tag, fall back to `static-debian12`), binary at `/bothy`, `ENTRYPOINT ["/bothy"]`, `USER 65532:65532`, `HEALTHCHECK --interval=30s --timeout=3s --retries=3 --start-period=30s --start-interval=1s CMD ["/bothy", "healthcheck"]` (same in `deploy/Dockerfile.release`; compose overrides it for sandboxd with `healthcheck sandboxd`).
- Directories baked into the image (named volumes copy ownership on first mount, so permissions are deterministic whichever container mounts first):

| Path | Owner | Mode | Purpose |
|---|---|---|---|
| `/data` | 65532:65532 | 0700 | DB, memory files, SOUL.md, picture.png, config files, setup token, backups |
| `/keys` | 65532:65532 | 0700 | auto-generated KEK (separate volume from the DB) |
| `/work` | 65532:10001 | 2770 | `ws/<id>/` workspaces, `skills/`, `mcp/<server>/<install>/` |
| `/run/bothy` | 65532:65532 | 0770 | `sandboxd.sock` (0660), `egress/<h>/proxy.sock` |

| Identity | uid:gid | Notes |
|---|---|---|
| gateway | 65532:65532 + supplementary 10001 | Process umask `007`; `/data` files created with explicit 0600/0700; workspace files 0660, dirs 2770 |
| sandboxd | 65532:65532 + docker gid + 65532 | Only holder of the runtime socket |
| tool box processes | 10001:10001 | No caps, no setuid binaries in the image, umask 002 |

### 2.4 Process roles

- **Gateway (`serve`).** Auth, policy, approvals, vault, LLM calls, scheduler, SQLite, audit, egress proxy, channels, HTTP. Holds no runtime socket. Mounts `/work` (for `fs_*` tools and workspace pre-creation) and `/run/bothy` (egress sockets).
- **sandboxd.** Serves HTTP/1.1 + JSON on `/run/bothy/sandboxd.sock` (mode 0660, group 65532). Checks `SO_PEERCRED` uid == `BOTHY_SBX_GATEWAY_UID` when available (doctor reports `peercred=false` otherwise). The container spec is compiled in (§2.6); the gateway chooses only a key, a profile name, a workspace id, an MCP install id and clamped limits. Images come only from `BOTHY_SBX_IMAGES`, pinned by digest. Unknown JSON fields → 400.
- **sbx-init (PID 1 in every box).** ~250 lines, stdlib only, static, ~1.5 MB, copied to `/opt/bothy/sbx-init` in sandbox images. Reaps zombies, relays TCP `127.0.0.1:3128` ↔ `/run/egress/proxy.sock`, and provides the exec wrapper (§2.7). `HostConfig.Init` is **false**: sbx-init must be PID 1 so `killall` spares exactly it.

### 2.5 Gateway ↔ sandboxd protocol

Versioned `/v1`, fixed schema, wire types in `internal/sandboxd/proto` (M5):

| Method + path | Request → response |
|---|---|
| `GET /v1/health` | → `proto.Health` (engine, API ≥ 1.44, runtime, rootless, subpath, mount mode, self-test results, ok) |
| `POST /v1/selftest` | re-run canaries → `proto.Health` |
| `POST /v1/boxes` | `proto.CreateReq` → `proto.CreateResp`. Idempotent per key. Limits clamped. Refused (503 `unavailable`) when the self-test failed. Refused (503 `busy`) at `BOTHY_SBX_MAX_BOXES` after evicting the least-recently-used idle box fails. |
| `POST /v1/boxes/{key}/exec` | `proto.ExecReq` → NDJSON `proto.Frame` stream |
| `POST /v1/boxes/{key}/stdio` | Upgrade `bothy-stdio/1` + `proto.StdioReq` → raw duplex stream (MCP stdio) |
| `GET /v1/boxes/{key}/stderr` | → last 64 KiB of the stdio process stderr (text) |
| `POST /v1/boxes/{key}/kill` | → `proto.KillResp` (§2.7) |
| `DELETE /v1/boxes/{key}` | stop and remove; 204 |
| `GET /v1/boxes` | → `[]proto.BoxInfo` (gateway reconciles at boot) |

sandboxd reaps boxes idle for more than `BOTHY_SBX_IDLE` (default 10m) and caps lifetime at `BOTHY_SBX_MAX_LIFETIME` (default 4h). At start it removes every container labelled `bothy.box=1` (orphans of a previous sandboxd process).

```go
// Package proto is the fixed-schema gateway <-> sandboxd wire format (HTTP/1.1
// + JSON over /run/bothy/sandboxd.sock). Both sides decode with
// DisallowUnknownFields. Owner: M5 (client and server are in the same module).
package proto

const Version = "1" // path prefix /v1

// Validation (sandboxd rejects with 400 otherwise):
//   Key       ^box_[0-9a-z]{26}$
//   Workspace ^ws_[0-9a-z]{26}$ or ""
//   MCPTree   ^mcp/[a-z0-9][a-z0-9-]{0,31}/in_[0-9a-z]{26}$ or ""
//   Profile   one of the image keys in BOTHY_SBX_IMAGES
//   Limits    clamped to [64,2048] MB, [0.1,2] CPUs, [16,512] pids
//
// Subpaths are derived by sandboxd, never supplied: workspace = "ws/<Workspace>"
// on the work volume; egress = "egress/" + hex(sha256(Key))[:32] on the run
// volume. The gateway pre-creates both directories before calling Create.

// Mount modes reported in Health.Mounts.
const (
	MountsSubpath = "subpath" // volume subpaths (engine API >= 1.45, Docker 26+)
	MountsBind    = "bind"    // bind mounts of the volumes' host paths, verified after start (API 1.44, Docker 25)
)

type Health struct {
	Version  string  `json:"version"`
	API      string  `json:"api"`              // engine API version negotiated, must be >= 1.44
	Engine   string  `json:"engine"`           // "docker 27.4.1"
	Runtime  string  `json:"runtime"`          // runsc | runc
	Rootless bool    `json:"rootless"`         // from /info SecurityOptions
	Subpath  bool    `json:"subpath"`          // volume subpath supported
	Mounts   string  `json:"mounts,omitempty"` // MountsSubpath | MountsBind, once the engine checks passed
	OK       bool    `json:"ok"`               // false => gateway treats exec as unavailable
	Reason   string  `json:"reason,omitempty"`
	Fix      string  `json:"fix,omitempty"`
	SelfTest []Check `json:"self_test"`
	Boxes    int     `json:"boxes"`
	MaxBoxes int     `json:"max_boxes"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Limits struct {
	MemoryMB int     `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	Pids     int     `json:"pids"`
}

type CreateReq struct {
	Key       string `json:"key"`
	Profile   string `json:"profile"`
	Workspace string `json:"workspace"`   // mounted rw at /workspace from work volume subpath ws/<id>
	MCPTree   string `json:"mcp_tree"`    // work volume subpath, mounted at /opt/mcp
	MCPTreeRW bool   `json:"mcp_tree_rw"` // installer box only
	Limits    Limits `json:"limits"`
}

type CreateResp struct {
	ID      string `json:"id"`
	Created bool   `json:"created"` // false = already running (idempotent per key)
	Runtime string `json:"runtime"`
}

type ExecReq struct {
	Argv      []string          `json:"argv"` // <= 256 entries, <= 256 KiB total
	Cwd       string            `json:"cwd"`  // relative, cleaned, under /workspace
	Env       map[string]string `json:"env"`
	StdinB64  string            `json:"stdin_b64"`
	TimeoutMS int               `json:"timeout_ms"` // 1..max_exec (default cap 30 min)
	MaxOutput int               `json:"max_output"` // bytes kept per stream
}

// Exec response: Content-Type application/x-ndjson, one Frame per line:
//
//	{"s":1,"d":"<b64>"} stdout   {"s":2,"d":"<b64>"} stderr
//	last line: {"exit":0,"ms":41,"truncated":false,"timed_out":false,"killed":false}
type Frame struct {
	Stream    int    `json:"s,omitempty"`
	Data      string `json:"d,omitempty"`
	Exit      *int   `json:"exit,omitempty"`
	Ms        int64  `json:"ms,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Killed    bool   `json:"killed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Stdio: POST /v1/boxes/{key}/stdio with "Connection: Upgrade", "Upgrade:
// bothy-stdio/1" and a StdioReq body; 101 then a raw full-duplex byte stream
// (stdin -> process, process stdout -> client; stderr goes to a 64 KiB ring
// readable at GET /v1/boxes/{key}/stderr). One stdio stream per box.
type StdioReq struct {
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

type KillResp struct {
	Killed    int  `json:"killed"`    // processes reported by sbx-init killall
	Recreated bool `json:"recreated"` // fallback path: container killed and removed
}

type BoxInfo struct {
	Key      string `json:"key"`
	ID       string `json:"id"`
	Profile  string `json:"profile"`
	Created  int64  `json:"created_ms"`
	LastUsed int64  `json:"last_used_ms"`
	Runtime  string `json:"runtime"`
}

type Error struct {
	Code    string `json:"code"` // bad_request | not_found | busy | engine | unavailable
	Message string `json:"message"`
}
```

### 2.6 Box container spec (compiled into sandboxd; `internal/sandboxd/spec.go`)

Engine API field names; illustrative Go (the engine client's own structs are M5's). Only the marked inputs come from the request; everything else is constant.

```go
memBytes := int64(clamp(req.Limits.MemoryMB, 64, 2048)) << 20 // input
hc := HostConfig{
	NetworkMode:    "none",
	ReadonlyRootfs: true,
	CapDrop:        []string{"ALL"},
	SecurityOpt:    []string{"no-new-privileges:true"}, // default seccomp + AppArmor profiles stay on
	Init:           false,                              // sbx-init is PID 1
	IpcMode:        "private",
	PidsLimit:      clamp(req.Limits.Pids, 16, 512),            // input
	Memory:         memBytes,
	MemorySwap:     memBytes, // equal to Memory: no swap
	NanoCPUs:       int64(clampF(req.Limits.CPUs, 0.1, 2) * 1e9), // input
	Ulimits:        []Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}, {Name: "core", Soft: 0, Hard: 0}},
	Tmpfs: map[string]string{
		"/tmp":      "rw,nosuid,nodev,size=256m",
		"/home/sbx": "rw,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700",
	},
	Runtime:   runtime, // "runsc" only when registered AND the UDS canary passed (§2.9)
	LogConfig: LogConfig{Type: "none"}, // tool output never lands in host logs
	Mounts: []Mount{
		{Type: "volume", Source: workVol, Target: "/workspace", // only when req.Workspace != ""
			VolumeOptions: &VolumeOptions{Subpath: "ws/" + req.Workspace, NoCopy: true}},
		{Type: "volume", Source: runVol, Target: "/run/egress", ReadOnly: true,
			VolumeOptions: &VolumeOptions{Subpath: "egress/" + hex(sha256(req.Key))[:32], NoCopy: true}},
		{Type: "volume", Source: workVol, Target: "/skills", ReadOnly: true,
			VolumeOptions: &VolumeOptions{Subpath: "skills", NoCopy: true}},
		{Type: "volume", Source: workVol, Target: "/opt/mcp", ReadOnly: !req.MCPTreeRW, // only when req.MCPTree != ""
			VolumeOptions: &VolumeOptions{Subpath: req.MCPTree, NoCopy: true}},
	},
	RestartPolicy: RestartPolicy{Name: "no"},
	AutoRemove:    true,
}
cfg := Config{
	Image: images[req.Profile], User: "10001:10001", WorkingDir: "/workspace",
	Cmd:  []string{"/opt/bothy/sbx-init"},
	Env: []string{"HOME=/home/sbx", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin",
		"HTTP_PROXY=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128",
		"http_proxy=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
		"NO_PROXY=", "NODE_USE_ENV_PROXY=1", "PIP_DISABLE_PIP_VERSION_CHECK=1", "GIT_TERMINAL_PROMPT=0"},
	Labels: map[string]string{"bothy.box": "1", "bothy.key": req.Key, "bothy.profile": req.Profile,
		"bothy.run_volume": runVol}, // scopes orphan cleanup to this install's boxes
}
name := runVol + "-" + req.Key // container name
```

**Container names** are `<BOTHY_SBX_RUN_VOLUME>-<key>`. Box keys are unique only within one install (the canary key `DerivedID("box","canary")` and the MCP installer and discovery keys are derived, so they are the same in every install), and Create replaces a container by name on a clash while Destroy of an unregistered key removes by name. Scoping the name to the run volume (keys never contain `-`, so the name is exactly unique) lets several installs or test runs share one engine provided their run volumes differ; orphan cleanup at start lists only containers labelled with this install's `bothy.run_volume`.

Volume subpaths need Engine API ≥ 1.45 (Docker 26+), and the subpath directory must already exist (moby#47842), so **the gateway creates `ws/<id>` (2770, group 10001) and `egress/<h>` (0750, group 10001) before calling Create**. Docker resolves subpath symlinks safely; sandboxd additionally rejects any key/workspace/tree id not matching the regexes in `proto`.

**Bind-mount mode (API 1.44, Docker 25: the ECS-optimized AMI, Amazon Linux 2023).** Each mount becomes `{Type: "bind", Source: <host path>/<same subpath>, BindOptions: {NonRecursive: true}}` with the same target and read-only flag, where `<host path>` is the volume's `Mountpoint` from `GET /volumes/{name}` (driver `local` required, absolute and clean). The engine resolves a bind source on its host and follows symlinks, and the gateway can write both volumes, so a directory swapped for a symlink while the box starts could otherwise mount any host path. Before the box is registered or returned (it has run only sbx-init as PID 1), sandboxd runs `sbx-init mountids <targets…>` in it and compares each mount's `dev:ino` with the directory it finds at that subpath through its own mounts of the volumes, looked up with `os.Root` so it never leaves them: the run directory and `BOTHY_SBX_WORK_DIR` (default `/work`, mounted read-only into sandboxd; both must be mount points). Any mismatch or error removes the container and answers 502 `engine` ("bind mount check failed: …"); the canary goes through the same check on every self-test. gVisor virtualizes device and inode numbers, so this mode is runc only: `auto` skips a registered runsc and `BOTHY_SBX_RUNTIME=runsc` fails closed. `Health.Mounts` is `bind`, `Subpath` false, the self-test adds a `bind_mounts` check with both host paths, and doctor reports `sandbox.bind_mounts` (info). API < 1.44 → `Health.OK=false`, reason "Docker 25+ required", exec unavailable. Binaries built with `-tags sbxtesthooks` (the acceptance suite's `API_LIMIT=1.44`) honour `BOTHY_SBX_TEST_API_LIMIT` to run this mode on a newer engine; release builds ignore it. Podman 5 compatibility is *(verify)* in week 1 and labelled beta.

**Sandbox images** (`deploy/sandbox/`, M5; digest-pinned in releases):
- `bothy-sandbox` (base): debian trixie-slim, python3, python3-venv, uv, bash, coreutils, git, curl, ca-certificates, jq, ripgrep, unzip, procps, sbx-init; user `sbx` 10001. ~106 MiB compressed (measured 105.8 MiB on linux/arm64; the apt layer is ~173 MB unpacked and uv ~48 MB).
- `bothy-sandbox-node`: base + Node 24 LTS + npm. ~153 MiB compressed (measured 152.5 MiB on linux/arm64).
- Both: all setuid/setgid bits stripped (`find / -perm /6000 -type f -exec chmod a-s {} +`); `/etc/gitconfig` sets `core.fsmonitor=false`, `core.hooksPath=/dev/null`, `core.pager=cat`, `protocol.file.allow=never`, `protocol.ext.allow=never`, `safe.directory=/workspace/*`.

### 2.7 Exec, timeout and kill (D12)

`sbx-init` subcommands (inside the box, all run as uid 10001):

| Command | Behaviour |
|---|---|
| `sbx-init` (no args) | PID 1: reap children; run the TCP→UDS relay on `127.0.0.1:3128`; exit on SIGTERM. |
| `sbx-init run --timeout <d> --kill-after 5s -- argv…` | `setsid` child, set rlimits (NOFILE 1024, FSIZE 1 GiB, CORE 0, CPU = timeout; no RLIMIT_NPROC: it counts every process and thread of the host uid, i.e. all boxes together in container mode and the gateway itself in bwrap mode, so process counts are bounded by the pids cgroup instead), wait. On timeout: SIGTERM the process group, SIGKILL after kill-after, exit 124. Background processes that escape the group survive the exec (dev servers work) until `/stop`, `killall` or box reaping. |
| `sbx-init killall` | `kill(-1, SIGKILL)`: signals every process in the box's PID namespace the caller may signal except PID 1 and itself. Prints the count. |
| `sbx-init count` | Prints the number of processes other than PID 1 and itself (reads `/proc`). |
| `sbx-init mountids <path>…` | Prints each path's `dev:ino`, one per line; a symlink or non-directory fails it (bind-mount mode, §2.6). |
| `sbx-init selftest` | Runs canaries (§2.10), prints JSON. |
| `sbx-init --relay <sock> run …` | bwrap mode: starts the relay in-process, then behaves like `run`. |

Every `POST /exec` becomes a Docker exec of `["/opt/bothy/sbx-init","run","--timeout",N,"--kill-after","5s","--",argv…]` with `User: "10001:10001"`, `WorkingDir: /workspace/<cwd>`, env = request env (validated) over the container env. sandboxd demultiplexes the Docker stream (8-byte headers), keeps `max_output` bytes per stream, then drains and discards up to 64 MiB more before killing.

`POST /kill`: exec `sbx-init killall`, then `sbx-init count`; if count > 0 after 1 s or either exec fails, `ContainerKill(SIGKILL)` + remove (`recreated: true`; the next Ensure recreates the box; the workspace persists). The gateway calls Kill from `/stop` without waiting on the run goroutine. Acceptance: a double-forked `setsid sleep 600` is gone within 1 s (§12).

### 2.8 Networks and egress

- Boxes are `--network none`: only `lo`, no route, no resolver. DNS exfiltration is impossible by construction.
- **The socket is the identity.** `Egress.OpenBox(key)` creates `/run/bothy/egress/<h>/proxy.sock` (0660, group 10001, dir 0750) where `<h>` = `hex(sha256(key))[:32]`; that directory is subpath-mounted read-only into exactly one box. The proxy knows the box from the listener that accepted the connection. No tokens enter the box.
- sbx-init relays `127.0.0.1:3128` to that socket. Clients honouring `HTTP(S)_PROXY` work (pip, uv, npm, curl, git, requests, Node 24 with `NODE_USE_ENV_PROXY`). Others fail closed.
- The proxy (`internal/egress`, in the gateway) speaks HTTP CONNECT and absolute-form HTTP. Per connection:
  1. **Port** must be in `egress.ports` (default `[443]`; `80` only with `allow_plain_http`).
  2. **Host** normalised: lowercase, IDNA → punycode; IP literals denied unless an exact `ip:port` grant exists.
  3. **Denylist** `egress.deny` beats everything below. **Allowlist** = `egress.allow` ∪ enabled presets ∪ box `Extra` (MCP server egress list, installer registries) ∪ active domain grants from `Grants().Active(principal, session, binding.Schedule)` whose `MaxSE` ≥ `external_send` (unless `NoGrants`; a box can upload as well as download, so a read-class domain grant never opens the box proxy; `NarrowGrant` gives `net` actions `external_send`, and so do `net_allow` and Settings → Sandbox "Allow this site") ∪ the in-memory allow_once set of the bound tool call (an allow_once admits `host:port` only until that tool call's exec returns and the runner calls `Egress.Unbind`; an approval resolved after that admits only the connection that was held). A grant or allow_once for `pypi.org:<port>` also admits `files.pythonhosted.org:<port>`, the host pip and uv download what pypi.org links to, so installing a package raises one card; the reverse does not hold. Patterns: exact host, or `*.suffix` (only in `egress.allow` or admin-created principal grants). A host under a **shared-tenant suffix** (`egress.shared_tenant_suffixes`: `.amazonaws.com`, `.cloudfront.net`, `.googleusercontent.com`, `.blob.core.windows.net`, `.workers.dev`, `.pages.dev`, `.ngrok-free.app`, `.github.io`) is allowed only by an admin principal grant or `egress.allow`, never by a session grant.
  4. **Trifecta:** if the session is Exposed (§5.3), session and principal grants are suspended; only `egress.allow`, presets and box `Extra` apply.
  5. **Not allowed:** if a tool call is bound (between `Bind` and `Unbind`), the binding is interactive, the session is not Exposed and `hold_for_approval > 0`, hold the CONNECT up to 45 s and request an approval (tool call id = the bound exec's id + `#net`, Action `{kind: net, host, port}`, card "Sandbox wants to reach pypi.org:443", choices from `Policy.Decide` on the binding's user, role, trigger, chat and provenance: allow once / allow for session / deny, or `[allow_once, deny]` plus the untrusted-origin warning when an untrusted message started the run, F6). Nothing is held while no tool call is bound: a background process an exec left running gets the 403. Otherwise reply `403` with body `bothy: egress to <host>:<port> denied (<reason>). Ask the user or call net_allow.`, publish `egress.blocked`, audit `egress.deny`.
  6. **Resolve** with the gateway resolver (TTL clamped ≤ 60 s), check **every** returned address against the blocklist after `Unmap()`, dial the first allowed **IP literal**; `net.Dialer.Control` re-checks the final address (defeats rebinding).
  7. **SNI check:** for CONNECT, peek the TLS ClientHello (`tls.Server` with `GetConfigForClient` capturing `ServerName` on a tee'd reader, then replay); SNI must equal the CONNECT host or the tunnel is closed.
  8. **Byte caps** per session: 10 MiB up, 200 MiB down (`egress.caps`); exceeding either closes the connection.
  9. One `egress_log` row per connection (batched), with decision, bytes and duration; after `Unbind` the run stays bound, so later connections are still logged against it. `Egress.TakeActivity(key)` reports bytes moved since the last call (the runner takes it after every tool call to taint that call's output, §5.3).
- **Blocklist** (`net/netip`, every address `Unmap()`ed; embedded IPv4 extracted and re-checked for `::ffff:0:0/96`, `64:ff9b::/96`, `2002::/16`, `2001::/32`): `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`, `::/128`, `::1/128`, `fc00::/7`, `fe80::/10`, `ff00::/8`, `100::/64`, `2001:db8::/32`. Cloud metadata (`169.254.169.254`, `fd00:ec2::254`) and the container-credential addresses (`169.254.170.2`, `169.254.170.23`, `fd00:ec2::23`, v0.2) are blocked even for `allow_private` destinations.
- **Three dialers, all logged** (plus the v0.2 credentials-only client below):
  - *box* (above).
  - *public* (`Egress.PublicClient`, used by `web_fetch`): any public address, same blocklist, pinned dial, redirects re-checked per hop (≤ 5), no cookies, no `Authorization`, GET only, `Proxy: nil`.
  - *system* (`Egress.SystemClient(SystemDest)`): providers, channel APIs, search APIs, HTTP MCP servers. Only the exact `host:port` list of the destination; private addresses only with `AllowPrivate` (admin-set, exact host:port); `Proxy: nil`; TLS ≥ 1.2; no implicit retries. v0.2 destinations: `github` (the `github.api_base` host plus `codeload.github.com`, or `codeload.<host>` for GitHub Enterprise; `AllowPrivate` for a non-default api_base), `release:github` (the `releases.github_api` host), and per bedrock provider the runtime and control-plane hosts (or the https VPC endpoint) plus exactly the STS/SSO/sign-in hosts its AWS config needs.
  - *credentials* (v0.2, `Egress.CredentialsClient(relativeURI, fullURI)`): used only by the Bedrock provider's AWS credential chain. It dials fixed addresses only: `169.254.169.254:80` and `[fd00:ec2::254]:80` for the IMDSv2 token (PUT) and role-credential paths (IMDSv1, user data and the identity document are refused), and the exact container-credential path on `169.254.170.2:80` or the `AWS_CONTAINER_CREDENTIALS_FULL_URI` host. No redirects, a 1 MiB body cap, every connection or refusal logged as `system`/`credentials:aws`. Boxes, tools, `web_fetch` and MCP never get it; tests prove none of them reaches a metadata or container-credential address.

### 2.9 gVisor (runsc)

Tool boxes connect to a host UDS on a bind mount, which gVisor allows only with `--host-uds=open` (connect-only; never `all`). The documented `/etc/docker/daemon.json`:

```json
{ "runtimes": { "runsc": { "path": "/usr/local/bin/runsc", "runtimeArgs": ["--host-uds=open"] } } }
```

At start and on every self-test, sandboxd runs the canary box under runsc (when registered) and checks that `127.0.0.1:3128` reaches the canary box's own egress socket, which sandboxd itself creates and listens on (canary key `DerivedID("box","canary")`). `BOTHY_SBX_RUNTIME`:
- `auto`: runsc if registered **and** the UDS canary passes; otherwise runc, with doctor finding `sandbox.runsc_host_uds` (low) printing the daemon.json fix.
- `runsc`: refuse to serve (fail closed, `Health.OK=false`) when the canary fails.
- `runc`: never runsc.

### 2.10 Self-test canaries and the fail-closed matrix

sandboxd runs a canary box at start, every 10 minutes and on `POST /v1/selftest`. Inside it `sbx-init selftest` checks:

| # | Probe | Must be |
|---|---|---|
| 1 | TCP connect `1.1.1.1:443` | fails (ENETUNREACH) |
| 2 | resolve `example.com` | fails |
| 3 | `/proc/self/status` | runc: `CapEff: 0`, `NoNewPrivs: 1`, `Seccomp: 2`. runsc: `CapEff: 0` only; gVisor's Sentry is the syscall boundary and does not apply Docker's OCI seccomp profile unless runsc runs with `--oci-seccomp` *(verify flag and what gVisor reports in `/proc/self/status`)*, so the other two fields are reported, not required |
| 4 | write to `/usr/bin/x` | EROFS |
| 5 | `/proc/self/mountinfo` | no `docker.sock`, no `/run/bothy` root, only the expected mounts |
| 6 | uid/gid | 10001/10001 |
| 7 | `127.0.0.1:3128` | reaches the canary listener |

Any failure of 1–6 → `Health.OK=false`, Create refused, red banner `sandbox_not_isolated`, doctor critical. Failure of 7 alone → runtime fallback per §2.9.

| Condition | Effect |
|---|---|
| No sandboxd socket / health unreachable | Exec tools absent from `ForRun`; MCP stdio servers state `no_sandbox`; banner `no_sandbox` (warn); doctor medium |
| Self-test isolation failure | As above but banner `sandbox_not_isolated` (error); doctor critical |
| Engine API 1.44 (no subpaths) | Bind-mount mode (§2.6): runc only, every mount checked by `dev:ino` before use; doctor `sandbox.bind_mounts` (info) |
| Engine API < 1.44 | Exec unavailable; fix text "Docker 25+ required" |
| Docker socket not accessible | sandboxd logs and reports `reason` with the exact fix line (below); doctor `docker.socket_gid` |
| runc on rootful Docker without userns-remap | Allowed; doctor low "prefer rootless Docker or runsc" |
| Box creation fails mid-run | Tool result is an error; model told exec is unavailable; never host fallback |

**Docker socket onboarding.** At start sandboxd `stat`s the socket. If `connect` fails with EACCES it reports: `cannot access /var/run/docker.sock (owner uid U gid G, mode M). Set DOCKER_GID=G in the .env next to compose.yaml and run: docker compose up -d` (`install.sh` writes that `.env` next to `compose.yaml` in its own directory; a checkout keeps both in `deploy/`, so `deploy/.env` is not the general answer). Rootless Docker shares dockerd's user namespace, so inside the container the socket is `root:root` and this line applies with `G=0` (`install.sh` writes `DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock` and `DOCKER_GID=0`). Only a socket that still appears owned by a non-root uid with mode 0600 or 0660 (or sits under `$XDG_RUNTIME_DIR` or `/run/user/`) gets the variant `set SANDBOXD_USER=<uid>:<gid> and DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock in the .env next to compose.yaml and run: docker compose up -d`.

### 2.11 MCP stdio servers in boxes

- **Config** requires `package: {ecosystem: npm|pypi, name, version (exact), integrity}`. Validation rejects `latest`, ranges, git refs, and any `command` starting with `npx`, `uvx`, `pipx run` or `bunx` (no runtime fetches).
- **Install** (admin action, `POST /api/mcp/{server}/install`): an installer box (key `DerivedID("box","mcp-install:"+server)`, profile per config, `MCPTree = mcp/<server>/<install_id>` mounted **rw**, no workspace, `Extra` = registry hosts only: `registry.npmjs.org:443` or `pypi.org:443` + `files.pythonhosted.org:443`, `NoGrants`).
  - npm: if a lock was recorded, `npm ci --ignore-scripts --no-audit --no-fund --prefix /opt/mcp` from it; else `npm install --ignore-scripts --no-audit --no-fund --save-exact --prefix /opt/mcp <name>@<version>`, verify the top-level `integrity`, record `package-lock.json` as the lock (trust on first install).
  - pypi: with a recorded lock, `uv pip install --no-deps --require-hashes -r lock.txt --target /opt/mcp`; else `uv pip compile --generate-hashes` for `<name>==<version>`, record it, then install from it.
  - The gateway pre-creates `<work>/mcp/<server>/<install_id>/` (2770, group 10001) before the installer box mounts it, then hashes the installed tree (`mcp_installs.tree_hash`, sha256 over sorted relative paths, modes and contents) and re-checks it on every `Start`; drift → status `drifted`, server disabled, banner.
- **Runtime:** one instance per (server, principal), box key `DerivedID("box","mcp:"+server+":"+principal)`, `MCPTree` mounted **read-only** (the server cannot rewrite itself), workspace mounted only when `workspace: principal`, `Extra` = the server's `egress` list, `NoGrants`. Started lazily on first tool call through `Sandbox.Ensure` + `Sandbox.Stdio`, wired to the go-sdk as `&mcp.IOTransport{Reader: rwc, Writer: rwc}` (the stream closes once); stopped after 10 minutes idle; restarted with backoff 1 s → 60 s. Tool lists come from a short-lived discovery instance (box key `DerivedID("box","mcp-discover:"+server)`, no workspace, same egress) run at `Start`, `Install` and `Reload`, which updates pins and is then destroyed; pins are per server, shared by all principals.
- **HTTP servers:** `StreamableClientTransport` (legacy `SSEClientTransport` fallback for old servers) with `HTTPClient` always set from `Egress.SystemClient(SystemDest{Name: "mcp:<server>", Hosts: [url host:port], AllowPrivate: allow_private})` (a nil client would fall back to `http.DefaultClient`). One client session per (server, billing user); header refs, and refs in the query of the server's `url` (`?api_key=secret:<name>`), resolve with `Vault.Resolve(ref, user)` (user scope, then global), so household members can hold their own tokens for the same server. Runs of space principals resolve in the global scope only (spaces have no vault scope). The refs are resolved again at every tool call (each a `secret.use`): a session made with other values is closed without a request (its close would send the old key, in the address too) and a new one made, so a key replaced in the vault is sent at the next call, and a deleted one fails the call (`key_missing`) and closes the session. A 401 or 403 from the server drops the session (status code `key_rejected` when it happens at discovery). An error that quotes the resolved address shows the configured one. **Discovery** (at `Start`, `Install` and `Reload`) has no billing user, so it resolves every reference in the global scope only; a header whose secret has no global value is left out of the discovery session, so a server whose people each use their own key still lists its tools (a per-user value only overrides a global one for that user's own calls), and the status says which header was left out when the server then refuses. `MCPStatus.code` names the fix for an error state: `key_missing`, `key_locked`, `key_rejected`, `private_address`, `not_installed`.
- **Tool names** are `mcp__<server>__<tool>`; a name over 64 characters becomes its first 55 characters + `_` + the first 8 hex of sha256(full name).
- **No secret is ever passed to a stdio server.** Integrations needing credentials use HTTP MCP with vault-injected headers.
- MCP stdio is unavailable (state `no_sandbox`) without a sandbox; restricted spaces never see MCP tools.

### 2.12 bwrap backend (native Linux)

```
bwrap --clearenv \
  --setenv PATH /usr/local/bin:/usr/bin:/bin --setenv HOME /home/sbx --setenv LANG C.UTF-8 \
  --setenv HTTP_PROXY http://127.0.0.1:3128 --setenv HTTPS_PROXY http://127.0.0.1:3128 \
  --setenv http_proxy http://127.0.0.1:3128 --setenv https_proxy http://127.0.0.1:3128 \
  --unshare-all --unshare-user --disable-userns --uid 10001 --gid 10001 \
  --die-with-parent --new-session --cap-drop ALL \
  --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/lib /lib --ro-bind-try /usr/lib64 /lib64 \
  --ro-bind /etc/ssl /etc/ssl --ro-bind-try /etc/alternatives /etc/alternatives \
  --proc /proc --dev /dev --tmpfs /tmp --tmpfs /home/sbx \
  --bind $WORK/ws/<id> /workspace --chdir /workspace --ro-bind-try $WORK/skills /skills \
  --ro-bind $RUN/egress/<h> /run/egress --ro-bind <self exe> /opt/bothy/bothy \
  --seccomp 3 \
  -- /opt/bothy/bothy sbx-init --relay /run/egress/proxy.sock run --timeout 120s --kill-after 5s -- <argv>
```

- `exec.Cmd.Env` is always set explicitly (never nil, which would inherit the gateway environment including `BOTHY_SECRET_*`).
- Seccomp filter (fd 3, passed via `exec.Cmd.ExtraFiles`) generated with `github.com/elastic/go-seccomp-bpf`: `Policy.Assemble()` (no filter is loaded into the gateway) → `golang.org/x/net/bpf.Assemble` → each `RawInstruction` written as a native-endian `struct sock_filter` (u16 code, u8 jt, u8 jf, u32 k); default action allow, listed syscalls `ActionErrno` (EPERM), arch-checked, x32 blocked; the `socket` family rule uses `NamesWithCondtions` (sic, the library's spelling) on argument 0. Denied: deny `ptrace`, `process_vm_readv/writev`, `bpf`, `perf_event_open`, `userfaultfd`, `io_uring_*`, `keyctl`, `add_key`, `request_key`, `mount`, `umount2`, `pivot_root`, `chroot`, `unshare`, `setns`, `kexec_*`, `*_module`, `acct`, `swapon/off`, `reboot`, `open_by_handle_at`, `personality` (except 0/0xffffffff), and `socket` families other than `AF_UNIX`/`AF_INET`/`AF_INET6`.
- Limits: rlimits via `sbx-init run`; memory/pids only when cgroup v2 delegation is available (doctor warns otherwise).
- Probe at start: `bwrap --unshare-all --unshare-user --disable-userns … true`. Ubuntu's userns AppArmor restriction makes it fail → exec unavailable, doctor prints the fix. The probe also creates the run dir (0750; nothing else creates it on a native install, §2.1), requires it to be writable, and requires `<run>/egress/<h>/proxy.sock` to fit in 107 bytes (`sun_path`); otherwise exec is unavailable with a `run_dir` self-test entry and the fix "set BOTHY_STORAGE__RUN_DIR (or storage.run_dir in bothy.yaml) to a short directory this user can write, such as $XDG_RUNTIME_DIR/bothy, and restart". In `auto` mode on native Linux the resulting `none` status keeps bwrap's reason and fix (install bubblewrap, allow user namespaces, fix `run_dir`); the container image (no bwrap) and macOS get the compose fix of §2.1.
- `os/exec` is banned in gateway packages by a CI check (`scripts/check-imports.sh`), except `internal/sandbox/bwrap`.
- Per-session "boxes" in bwrap mode are per-exec jails sharing the session workspace; `Kill` kills the process tree of every active jail for the key.

---

## 3. Go package layout

Owner tags refer to §12. **S** = skeleton/lead (frozen for module agents). Each module directory gets a one-paragraph `README.md`.

```
cmd/bothy/                 main.go (dispatch), wire.go (construction order), cli.go, umask_*.go          [S]
cmd/sbx-init/              main.go: os.Exit(sbxinit.Main(os.Args[1:]))                                   [S]
internal/core/             every shared type and interface (§4); small pure helpers implemented,
                           links.go (NeutralizeLinks, NeutralizeMarkdown, NewLinkFilter, LooksLikeLink),
                           v0.2: failure.go (FailureCode, FailureCoder, CodeOf, TransportFailure)          [S]
internal/config/           schema (§7), loader, env overlay, bothy.ui.yaml writer, effective sources    [S, implemented]
internal/events/           core.Bus: 4096-entry ring, boot id, non-blocking fan-out                     [S, implemented]
internal/deps/             deps.go (//go:build deps) blank-imports every go.mod dependency              [S]
internal/testkit/          shared fakes and harnesses (§12.1)                                           [S]
internal/testkit/memstore/ in-memory core.Store (all 24 repos) passing conformance.Store                [S]
internal/testkit/mockllm/  scriptable OpenAI-compatible HTTP server with fault injection               [S]
internal/testkit/knowledgetest/ in-memory core.KnowledgeRepo passing conformance.Knowledge (v0.2)       [S]
internal/httpapi/dto/      REST request/response shapes (§8.4); source for types.gen.ts                 [S]
internal/store/            core.Store on modernc sqlite: Open, pragmas, version gate, migrator, repos   [M1]
internal/store/migrations/ 0001_init.sql (§6); v0.2: 0002_repos, 0003_knowledge, 0004_approval_votes,
                           0005_approval_comments, 0006_must_change_password, 0007_v02_fixes (§14.6),
                           0008_external_secret_refs (§14.8), 0009_ui_state (§14.7),
                           0010_knowledge_stemming, 0011_sign_ins (§15), 0012_github_apps (§14.3),
                           0013_team_access (§6), 0014_team_memory (§6)                                 [S]
internal/vault/            core.Vault, core.Redactor, KEK sources, envelope crypto, aws-sm references   [M1]
internal/vault/awssm/      v0.2: AWS Secrets Manager GetSecretValue, hand-signed (SigV4) (§14.8)        [M1]
internal/audit/            core.Auditor: single writer, hash chain, Verify, offline Archive             [M1]
internal/provider/         core.LLM facade: chains, retries, breaker, estimator, catalog, probe, detect  [M2]
internal/provider/anthropic|openai|gemini|ollama/   wire clients (openai also = openai_compat)           [M2]
internal/provider/bedrock/ v0.2: Converse(Stream) client, AWS credential chain, model listing (§14.1)    [M2]
internal/provider/catalog/ catalog.json (embedded) + loader                                             [M2]
internal/agent/            core.Runner: session workers, run loop, assembly, compaction, stop, resume   [M3]
internal/agent/refine/     v0.2: refine engine, answer normalization, embedded role prompts (§14.2)     [M3]
internal/budget/           core.Budget: meters, daily caps, sender limiter, loop detector               [M3]
internal/policy/           core.Policy, core.Approvals, card renderer, button MAC                       [M4]
internal/tools/            core.ToolRegistry, tool_search/use_tool, fs_read/write/list/delete, message  [M4]
internal/sandbox/          core.Sandbox: sandboxd client, bwrap backend, none; shell/python tools       [M5]
internal/sandboxd/         sidecar server, Docker Engine client, compiled spec, reaper, self-test       [M5]
internal/sandboxd/proto/   wire types (§2.5)                                                           [M5]
internal/sbxinit/          PID 1, relay, run/killall/count/selftest (stdlib only)                       [M5]
internal/egress/           core.Egress: proxy, dialers, SSRF guard, caps, hold-for-approval;
                           web_fetch, web_search, net_allow tools                                       [M6]
internal/mcp/              core.MCP: go-sdk client, stdio-in-box + Streamable HTTP, install, pinning    [M7]
internal/skills/           core.Skills: SKILL.md scanner, index, zip install; skill_load/skill_read     [M7]
internal/memory/           core.Memory: MEMORY/USER/HEARTBEAT files, events, deltas; memory,
                           session_search tools                                                         [M8]
internal/scheduler/        core.Scheduler: timer loop, gronx, heartbeat; schedule_* tools               [M8]
internal/github/           v0.2 F4: core.GitHub, repo_* and github_* tools, workflow dispatch;
                           githubtest fake GitHub API (§14.3)                                           [F4]
internal/knowledge/        v0.2 F6: core.Knowledge: FTS5 index queue, knowledge_search, notes,
                           turn-context hook (§14.4)                                                    [knowledge]
internal/release/          v0.2 F5: release_deploy/release_status, run follower, token GitHub client;
                           actionstest fake Actions API (§14.5)                                         [release]
internal/picture/          core.AgentPicture: embedded default picture (rendered from the mascot by
                           make icons), upload checks, crop and scale, <data>/picture.png (§4.20)       [picture]
internal/channel/          core.ChannelHub: gatekeeping, membership, commands, streamer, card delivery  [M9]
internal/channel/telegram/ Telegram adapter (hand-written) + telegramtest fake Bot API server           [M9]
internal/channel/discord/  Discord adapter (disgo) + discordtest fake                                   [M10]
internal/channel/slack/    Slack adapter (slack-go socketmode) + slacktest fake                         [M10]
internal/auth/             core.Auth                                                                    [M11]
internal/httpapi/          routes, middleware, SSE, OpenAI-compatible endpoints                         [M11]
internal/doctor/           check runner, text/JSON rendering                                            [M11]
internal/webui/            go:embed dist (pre-gzipped), SPA fallback, cache headers                     [M11]
web/                       Svelte 5 SPA (src/lib/api/types.gen.ts is [S], generated)                    [M12]
deploy/                    Dockerfile, compose.yaml, compose.source.yaml (compose on images built
                           from source), .env.example, install.sh, searxng/settings.yml                 [S]
deploy/sandbox/            sandbox image Dockerfiles, gitconfig                                         [M5]
tools/                     separate go.mod: tygo config [S]; catalog-gen (catwalk, plus the embedded
                           catalog-gen/bedrock.json overlay) [M2]
e2e/                       black-box suite (phase 3); dockerrelease.go + docker/compose.test.yaml
                           drive a real compose deployment (tags e2e,docker)                            [S]
scripts/                   check-imports.sh, check-ownership.sh, budgets/*, e2e-docker.sh               [S]
```

**Import rules (CI-enforced by `scripts/check-imports.sh`):**
- `internal/core` imports only the standard library.
- Module packages import `core`, `config`, the stdlib, their own subpackages and their approved deps (§11). They never import another module's package. Exceptions: `httpapi` may import `doctor` and `dto` (same module / skeleton), adapters may import nothing from `internal/channel`, and tests may import `internal/testkit/**` and `internal/events`.
- Only `cmd/bothy` imports concrete module packages.
- `os/exec` only in `internal/sandbox/bwrap`. `unsafe` nowhere outside deps.
- No provider SDK, no `net/http.DefaultClient` in gateway packages (all clients come from `core.Egress`). v0.2 exception: `internal/provider/bedrock` may import exactly the AWS SDK packages for credentials, SigV4 and event-stream framing (§11.1); `internal/provider` tests may import the event-stream package; `internal/vault/awssm` may import `aws` (the credential type) and `aws/signer/v4` (SigV4), and `internal/vault` tests `aws`. The vault never imports the provider module: `cmd/bothy` hands it the bedrock provider's credential chain (`vault.SetAWS`, §14.8).
- v0.2 modules `internal/github` (owner tag F4), `internal/knowledge` (owner tag knowledge) and `internal/release` (owner tag release) import only the stdlib, `core` and `config`; `cmd/bothy` connects them (the GitHub service is the release module's `Connections` dispatcher; knowledge consumes `repo.checkout`).
- `internal/picture` (owner tag picture) imports only the stdlib, `core`, `config` and `golang.org/x/image` (`draw`, `webp`); `cmd/bothy` hands it to the hub and the HTTP API as `core.AgentPicture`.

---

## 4. Core types and interfaces (`internal/core`)

This is the complete package, copied verbatim into the skeleton, except `links.go`, whose exported API alone is listed (§4.15). It compiles on its own (stdlib only). The skeleton also implements the small pure helpers it contains (`NewID`, `DerivedID`, `ValidID`, `Micros`, `Price.Cost`, `ParseModelRef`, `Usage.Add`, `SideEffect` text marshalling, `Taint` helpers, `Viewer.Can`, `Actor.HasScope`, `Secret`, `Message.PlainText`, `ChatRef.Key`, `CanonicalJSON`, `ActionFingerprint`, the link neutralizer of §4.15) with unit tests. Everything else is an interface implemented by a module. Changes after the skeleton go through the lead as an ICR (§12.3).

Conventions that every implementer relies on:
- **Viewer scoping.** Every by-ID repository read takes a `Viewer`; rows outside `Viewer.Principals` are `ErrNotFound` (never `ErrForbidden`, to avoid existence oracles). `SystemViewer()` is for internal callers only; a CI grep forbids it in `internal/httpapi`. Internal callers that act on behalf of a chat user (the hub's `/stop`, `/new`, `/pin`) authorize first (sender is the principal's user or an admin) and then use `SystemViewer()`.
- **Hashing.** Anything hashed or compared across modules (Action payloads, fingerprints, tool spec hashes, MCP pins, audit rows) goes through `core.CanonicalJSON` first.
- **IDs.** `NewID(prefix)` everywhere; box keys are `DerivedID("box", "session:"+sessionID)` and `DerivedID("box", "mcp:"+server+":"+principalID)` so restarts find the same box.
- **Errors.** Modules wrap sentinels with `%w`; the HTTP layer maps `ErrNotFound`→404, `ErrForbidden`→403, `ErrConflict`→409, `ErrInvalid`→400, `ErrBusy`→429 `busy`, `ErrPaused`→503 `paused`, `*BudgetError`→429 `budget_exceeded`, `ErrSealed`→503 `sealed`, `ErrNoSandbox`→503 `no_sandbox`.
- **Time.** Persisted as unix ms; zero `time.Time` ↔ 0.
- **Events.** Each `EventType` has exactly one payload type (table in §8.2). Payloads are redacted before publishing.

### 4.1 `ids.go`: identifiers, money, clock, roles, principals, viewers, errors

```go
// Package core holds every cross-module type and interface. It imports only the
// standard library. It is frozen after the skeleton commit: changes go through
// the lead as an interface change request (ICR).
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type (
	UserID      string // usr_<26>
	PrincipalID string // user principals reuse the UserID string; spaces are spc_<26>
	SessionID   string // ses_<26>
	RunID       string // run_<26>
	ApprovalID  string // apr_<26>
	ScheduleID  string // sch_<26>
	WorkspaceID string // ws_<26>
	GrantID     string // grt_<26>
)

// ID prefixes. Every generated id is "<prefix>_<26 lowercase Crockford base32>".
const (
	PrefixUser      = "usr"
	PrefixSpace     = "spc"
	PrefixSession   = "ses"
	PrefixRun       = "run"
	PrefixApproval  = "apr"
	PrefixSchedule  = "sch"
	PrefixWorkspace = "ws"
	PrefixGrant     = "grt"
	PrefixToolCall  = "tcl"
	PrefixLLMCall   = "llm"
	PrefixToken     = "tok"
	PrefixBox       = "box"
	PrefixInstall   = "in"
	PrefixUpload    = "upl"
)

const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewID returns prefix + "_" + 26 chars: 48-bit unix ms then 80 random bits.
// Ids sort by creation time.
func NewID(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	return prefix + "_" + encode128(b)
}

// DerivedID returns a stable id of the given prefix for a purpose string. Used
// for sandbox box keys so the same session or MCP instance maps to the same box.
func DerivedID(prefix, purpose string) string {
	sum := sha256.Sum256([]byte(purpose))
	var b [16]byte
	copy(b[:], sum[:16])
	return prefix + "_" + encode128(b)
}

func encode128(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[0:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}

// ValidID reports whether s is "<prefix>_" followed by 26 Crockford chars.
func ValidID(prefix, s string) bool {
	rest, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(rest) != 26 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if !strings.ContainsRune(crockford, rune(rest[i])) {
			return false
		}
	}
	return true
}

// Micros is millionths of a US dollar. All money math is integer.
type Micros int64

func (m Micros) USD() float64 { return float64(m) / 1e6 }

func (m Micros) String() string { return fmt.Sprintf("$%.4f", m.USD()) }

// MicrosFromUSD converts a config value such as 5.00 into Micros, rounding half up.
func MicrosFromUSD(usd float64) Micros { return Micros(math.Round(usd * 1e6)) }

// Clock is injected wherever "now" matters. Timer-heavy tests use testing/synctest.
type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) IsAdmin() bool { return r == RoleOwner || r == RoleAdmin }

type PrincipalKind string

const (
	PrincipalUser   PrincipalKind = "user"   // a web account's own data
	PrincipalSpace  PrincipalKind = "space"  // a channel (group chat), owned by the team; restricted unless standard
	PrincipalPerson PrincipalKind = "person" // a teammate in a chat app, owned by the team; always standard
)

// Principal owns sessions, workspace, memory, grants and schedules. Channels
// and teammates are owned by the team (User ""): neither has a vault scope,
// a channel has no memory files, and a channel gets read-only tools unless
// its ToolProfile is "standard".
type Principal struct {
	ID          PrincipalID   `json:"id"`
	Kind        PrincipalKind `json:"kind"`
	User        UserID        `json:"user_id"` // user principal: that account; "" for team-owned principals (channels, teammates)
	DisplayName string        `json:"display_name"`
	Workspace   WorkspaceID   `json:"workspace_id"`
	ToolProfile string        `json:"tool_profile"` // "standard" | "restricted" (channels default restricted)
	CreatedAt   time.Time     `json:"created_at"`
}

func (p Principal) Restricted() bool { return p.Kind == PrincipalSpace && p.ToolProfile != "standard" }

// TeamOwned reports a channel's or a teammate's principal: no account owns it.
func (p Principal) TeamOwned() bool { return p.Kind == PrincipalSpace || p.Kind == PrincipalPerson }

// ChatActor is the audit actor of a person in a chat app.
func ChatActor(channel, externalID string) string { return "channel:" + channel + ":" + externalID }

// UserPrincipal returns the principal id of a user's own data.
func UserPrincipal(u UserID) PrincipalID { return PrincipalID(u) }

// Viewer scopes repository reads. Repositories return ErrNotFound for any row
// whose principal is not in Principals, unless System is set. HTTP handlers
// build Viewers with Auth.ViewerFor and never set System.
//
// Admin is set by Auth.ViewerFor for owner/admin users. Repositories ignore it
// except in exactly two places: approvals whose approver is AdminOnly or Team
// (ApprovalRepo.Get/Pending/Recent also return those), and egress rows of the
// system dialer (principal_id ""). It never grants access to message content.
// Team-owned principals (channels, teammates) are in no Viewer built for a
// person, so their chats stay their own.
//
// ApprovalRepo.Get/Pending/Recent also return multi-approver approvals that
// name User in ApproverReq.Users or name a role the viewer may hold (member
// for anyone, owner/admin when Admin is set): the approvers of another
// principal's request must see its card to vote on it.
type Viewer struct {
	User       UserID        `json:"user_id"`
	Principals []PrincipalID `json:"principals"`
	Admin      bool          `json:"admin"`
	System     bool          `json:"-"`
}

// SystemViewer is for internal callers only (scheduler, runner, egress, doctor).
func SystemViewer() Viewer { return Viewer{System: true} }

func (v Viewer) Can(p PrincipalID) bool {
	if v.System {
		return true
	}
	for _, x := range v.Principals {
		if x == p {
			return true
		}
	}
	return false
}

type Trigger string

const (
	TrigMessage   Trigger = "message" // web UI or channel message
	TrigSchedule  Trigger = "schedule"
	TrigHeartbeat Trigger = "heartbeat"
	TrigAPI       Trigger = "api" // OpenAI-compatible endpoint
	// TrigResume appears only in RunStartedData when a parked run continues.
	// Run.Trigger, PolicyInput.Trigger and ToolEnv.Trigger always keep the run's
	// original trigger, so floors such as F3 still apply after a resume.
	TrigResume Trigger = "resume"
)

func (t Trigger) Interactive() bool { return t == TrigMessage }

func (t Trigger) Scheduled() bool { return t == TrigSchedule || t == TrigHeartbeat }

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid")
	ErrBusy            = errors.New("session queue full")
	ErrNoSandbox       = errors.New("sandbox unavailable")
	ErrStopped         = errors.New("run stopped")
	ErrPaused          = errors.New("kill switch engaged")
	ErrApprovalExpired = errors.New("approval expired")
	ErrParked          = errors.New("run parked awaiting approval")
	ErrSealed          = errors.New("vault sealed")
	ErrNotImplemented  = errors.New("not implemented")
)
```

### 4.2 `message.go`: messages, parts, pins

```go
package core

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Provenance says who authored content. Anything not ProvUser/ProvSystem/ProvTool
// is untrusted and is wrapped in spotlight delimiters when sent to a model.
type Provenance string

const (
	ProvUser      Provenance = "user"      // the human who owns the session's principal (or the space owner)
	ProvUntrusted Provenance = "untrusted" // other senders, group participants, web/MCP output, API client "system"
	ProvTool      Provenance = "tool"      // trusted tool output (own workspace reads, exit codes)
	ProvSystem    Provenance = "system"    // Bothy-authored notices, turn-context, summaries
)

type MsgRole string

const (
	MsgUser      MsgRole = "user"
	MsgAssistant MsgRole = "assistant"
	MsgTool      MsgRole = "tool"
)

type MsgKind string

const (
	KindNormal  MsgKind = "normal"
	KindSummary MsgKind = "summary" // compaction output, first message of an epoch
	KindSteer   MsgKind = "steer"   // user message injected at a step boundary of a running run
	KindNotice  MsgKind = "notice"  // Bothy-authored (stopped, budget, loop); never sent to a model
)

type PartType string

const (
	PartText       PartType = "text"
	PartImage      PartType = "image"
	PartFile       PartType = "file"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	PartThinking   PartType = "thinking"
	PartContext    PartType = "context" // persisted <turn-context> block of a user message
)

type Part struct {
	Type       PartType    `json:"t"`
	Text       string      `json:"text,omitempty"`
	Blob       *Blob       `json:"blob,omitempty"` // image or file
	ToolCall   *ToolCall   `json:"call,omitempty"`
	ToolResult *ToolResult `json:"result,omitempty"`
	// Sig is provider-opaque data that must round-trip verbatim (Anthropic thinking
	// signature, Gemini thoughtSignature, OpenAI Responses encrypted reasoning).
	// SigKind is the ProviderKind that produced it, or a client's own kind where
	// one provider kind has two wire APIs ("openai_responses"); the provider facade
	// strips it when the next call goes to a client of another kind.
	Sig     string       `json:"sig,omitempty"`
	SigKind ProviderKind `json:"sig_kind,omitempty"`
	Refine  *RefineTrace `json:"refine,omitempty"` // PartRefine only
	// State and Approval are set on tool_call parts by API reads and never
	// stored: where the call stands, and the approval it waited on.
	State    ToolState  `json:"state,omitempty"`
	Approval ApprovalID `json:"approval_id,omitempty"`
}

// ToolState is where a transcript tool call stands. A call is done only once
// its result exists, so a pending action never reads as finished.
type ToolState string

const (
	ToolRunning         ToolState = "running"
	ToolWaitingApproval ToolState = "waiting_approval"
	ToolDone            ToolState = "done"
	ToolFailed          ToolState = "failed"
	ToolDenied          ToolState = "denied"  // an approver said no
	ToolExpired         ToolState = "expired" // nobody answered in time, or the task stopped first
	ToolBlocked         ToolState = "blocked" // refused by policy, never asked
)

// Blob points at a file in the principal's workspace. Data is filled by the
// agent just before a provider call and is never persisted.
type Blob struct {
	MIME string `json:"mime"`
	Name string `json:"name,omitempty"`
	Ref  string `json:"ref"` // workspace-relative path, e.g. ".bothy/in/<msg>/photo.jpg"
	Size int64  `json:"size"`
	Data []byte `json:"-"`
}

type ToolCall struct {
	ID   string          `json:"id"` // provider id, or "call_<n>" when the provider sends none
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"` // a JSON object after repair
}

type ToolResult struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Content   string `json:"content"` // redacted, truncated head+tail
	IsError   bool   `json:"is_error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	FullRef   string `json:"full_ref,omitempty"`  // workspace path of the full redacted output
	Untrusted bool   `json:"untrusted,omitempty"` // wrapped in <untrusted id=…> when sent to a model
}

type Message struct {
	ID         int64      `json:"id"`
	Session    SessionID  `json:"session_id"`
	Seq        int64      `json:"seq"`
	Run        RunID      `json:"run_id,omitempty"`
	Role       MsgRole    `json:"role"`
	Kind       MsgKind    `json:"kind"`
	Parts      []Part     `json:"parts"`
	Provenance Provenance `json:"provenance"`
	Sender     string     `json:"sender,omitempty"` // "telegram:12345", "web:usr_…", "api:tok_…"
	Tokens     int        `json:"tokens"`           // estimate, calibrated
	CreatedAt  time.Time  `json:"created_at"`
}

// PlainText returns the concatenated text parts (no tool JSON). Stored in
// messages.text for FTS.
func (m *Message) PlainText() string {
	var s string
	for _, p := range m.Parts {
		if p.Type == PartText {
			if s != "" {
				s += "\n"
			}
			s += p.Text
		}
	}
	return s
}

// Redacted returns a copy of m with every text, tool argument and tool result
// masked by r (nil masks nothing); m is unchanged. With keepSigned, thinking
// that carries a signature keeps its text: the provider checks the two
// together when the step is sent back.
func (m *Message) Redacted(r Redactor, keepSigned bool) *Message

// TitleChars is how much of a chat's first message names the chat until its
// generated title arrives.
const TitleChars = 48

// DraftTitle is that name: the text on one line, cut to TitleChars characters.
func DraftTitle(text string) string {
	s := strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}), " ")
	if r := []rune(s); len(r) > TitleChars {
		s = strings.TrimSpace(string(r[:TitleChars]))
	}
	return s
}

type Pin struct {
	ID        int64     `json:"id"`
	Session   SessionID `json:"session_id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"` // "user" | "auto" (standing instruction found by compaction)
	MsgSeq    int64     `json:"msg_seq,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
```

### 4.3 `provider.go`: models, requests, streaming, the LLM facade

```go
package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Slot string

const (
	SlotMain Slot = "main" // agent turns
	SlotFast Slot = "fast" // compaction, titles, heartbeat triage
)

type ProviderKind string

const (
	KindAnthropic    ProviderKind = "anthropic"
	KindOpenAI       ProviderKind = "openai"
	KindGemini       ProviderKind = "gemini"
	KindOllama       ProviderKind = "ollama"        // native /api/chat (num_ctx honoured)
	KindOpenAICompat ProviderKind = "openai_compat" // LM Studio, vLLM, OpenRouter, Groq, xAI, DeepSeek…
	KindBedrock      ProviderKind = "bedrock"       // Amazon Bedrock ConverseStream; AWS default credential chain, no API key
)

// ModelRef is "<provider id>/<model>". The model part may itself contain "/".
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

func ParseModelRef(s string) (ModelRef, error) {
	p, m, ok := strings.Cut(s, "/")
	if !ok || p == "" || m == "" {
		return ModelRef{}, errors.New("model ref must be <provider>/<model>")
	}
	return ModelRef{Provider: p, Model: m}, nil
}

// Price is USD micros per 1M tokens. Tiers reprice the whole request once input
// passes a threshold (xAI bills 2x above 200K).
type Price struct {
	InPerM         Micros      `json:"in_per_m"`
	OutPerM        Micros      `json:"out_per_m"`
	CacheReadPerM  Micros      `json:"cache_read_per_m"`
	CacheWritePerM Micros      `json:"cache_write_per_m"`
	Tiers          []PriceTier `json:"tiers,omitempty"`
	Known          bool        `json:"known"`
}

// PriceTier multipliers are permille (2000 = 2x). Permille applies to every
// component whose own multiplier is 0 (Gemini Pro and OpenAI bill 2x input
// but 1.5x output above their thresholds).
type PriceTier struct {
	AboveInput         int `json:"above_input"`
	Permille           int `json:"permille"`
	OutPermille        int `json:"out_permille,omitempty"`
	CacheReadPermille  int `json:"cache_read_permille,omitempty"`
	CacheWritePermille int `json:"cache_write_permille,omitempty"`
}

// Cost is exact integer math, rounding each component half up. Input excludes
// cache reads/writes (providers report them separately; adapters normalise).
// Each component takes the largest of 1000 and its tier multiplier over the
// tiers whose AboveInput the prompt (input + cache read + cache write)
// exceeds, so a discount tier never lowers a price.
func (p Price) Cost(u Usage) Micros {
	in, out, cr, cw := int64(1000), int64(1000), int64(1000), int64(1000)
	total := u.Input + u.CacheRead + u.CacheWrite
	for _, t := range p.Tiers {
		if total > t.AboveInput {
			in = max(in, int64(t.Permille))
			out = max(out, int64(cmp.Or(t.OutPermille, t.Permille)))
			cr = max(cr, int64(cmp.Or(t.CacheReadPermille, t.Permille)))
			cw = max(cw, int64(cmp.Or(t.CacheWritePermille, t.Permille)))
		}
	}
	part := func(tokens int, perM Micros, mul int64) int64 {
		return (int64(tokens)*int64(perM)*mul + 500_000_000) / 1_000_000_000
	}
	return Micros(part(u.Input, p.InPerM, in) + part(u.Output, p.OutPerM, out) +
		part(u.CacheRead, p.CacheReadPerM, cr) + part(u.CacheWrite, p.CacheWritePerM, cw))
}

type CacheMode uint8

const (
	CacheNone     CacheMode = iota
	CacheExplicit           // Anthropic cache_control breakpoints
	CacheImplicit           // OpenAI/Gemini/DeepSeek automatic prefix caching
)

type ModelInfo struct {
	Ref       ModelRef  `json:"ref"`
	Display   string    `json:"display"`
	Context   int       `json:"context"` // effective window (Ollama: the num_ctx we send: the configured context, else min(trained, max(16384, served context from /api/ps)))
	MaxOutput int       `json:"max_output"`
	Tools     bool      `json:"tools"`
	Vision    bool      `json:"vision"`
	Thinking  bool      `json:"thinking"`
	Cache     CacheMode `json:"cache"`
	Local     bool      `json:"local"`
	Price     Price     `json:"price"`
	Source    string    `json:"source"` // catalog | config | discovery

	// Plain names for the UI (v0.2): DisplayName "Claude Sonnet 4.5",
	// ShortName "Sonnet 4.5" for narrow screens, ProviderName "Anthropic".
	// Recommended marks the provider's suggested main model and
	// QuickTasksDefault its suggested quick tasks model, among the models
	// listed together.
	DisplayName       string `json:"display_name,omitempty"`
	ShortName         string `json:"short_name,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	Recommended       bool   `json:"recommended,omitempty"`
	QuickTasksDefault bool   `json:"quick_tasks_default,omitempty"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema object
}

type SystemBlock struct {
	Text       string
	CacheBreak bool // Anthropic cache_control breakpoint after this block
}

type Request struct {
	Model       string
	System      []SystemBlock
	Tools       []ToolDef
	ToolsCache  bool // breakpoint after the last tool (Anthropic)
	Messages    []Message
	CacheTail   bool // rolling breakpoint on the last message (Anthropic)
	MaxTokens   int
	Temperature *float64
	ToolChoice  string // "auto" | "none" | "required" (force some tool call)
	Stop        []string
	NumCtx      int // Ollama native only
	// CacheKey is stable for every call of one session and opaque (a hash,
	// never an id); the OpenAI Responses client sends it as
	// prompt_cache_key. "" sends none.
	CacheKey string
}

type StreamEventType uint8

const (
	StreamTextDelta StreamEventType = iota + 1
	StreamThinkingDelta
	StreamToolCallStart // Call has ID and Name
	StreamToolCallDone  // Call complete with repaired Args
	StreamUsage
)

type StreamEvent struct {
	Type  StreamEventType
	Text  string
	Call  *ToolCall
	Usage *Usage
}

// Usage: Input excludes cache reads and writes.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Reasoning  int `json:"reasoning"`
}

func (u Usage) Add(o Usage) Usage {
	return Usage{u.Input + o.Input, u.Output + o.Output, u.CacheRead + o.CacheRead,
		u.CacheWrite + o.CacheWrite, u.Reasoning + o.Reasoning}
}

func (u Usage) TotalInput() int { return u.Input + u.CacheRead + u.CacheWrite }

type StopReason string

const (
	StopEnd       StopReason = "end_turn"
	StopTool      StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
)

type Response struct {
	Message        Message // assistant role, parts assembled (text, thinking+sig, tool calls)
	Usage          Usage
	UsageEstimated bool // provider reported none; estimator filled it
	Stop           StopReason
	Model          string
}

// Provider is one wire protocol plus credentials. It never retries, never reads
// env vars, and uses only the http.Client given by Egress.SystemClient. One
// exception: a bedrock provider's AWS default credential chain reads the
// AWS_* variables and ~/.aws by design, and fetches instance or container
// credentials only through Egress's credentials-only client.
type Provider interface {
	ID() string
	Kind() ProviderKind
	Models(ctx context.Context) ([]ModelInfo, error) // config + discovery
	Stream(ctx context.Context, req *Request, on func(StreamEvent) error) (*Response, error)
}

type ErrorKind string

const (
	ErrKindAuth           ErrorKind = "auth"
	ErrKindRateLimit      ErrorKind = "rate_limit"
	ErrKindOverloaded     ErrorKind = "overloaded"
	ErrKindContextLength  ErrorKind = "context_length"
	ErrKindInvalidRequest ErrorKind = "invalid_request"
	ErrKindContentFilter  ErrorKind = "content_filter"
	ErrKindNetwork        ErrorKind = "network"
	ErrKindServer         ErrorKind = "server"
)

type ProviderError struct {
	Provider   string
	Kind       ErrorKind
	Status     int
	RetryAfter time.Duration
	Msg        string // redacted provider message
	Err        error
}

// ErrModelUnsupported, in a ProviderError's chain, says the model cannot
// serve the request as built (function tools over an API that cannot call
// them for that model). The error stays invalid_request for callers of one
// model; the facade tries the next model of the chain instead, as it does
// for a model without tool support.
var ErrModelUnsupported = errors.New("model cannot serve this request")

func (e *ProviderError) Error() string { return e.Provider + ": " + string(e.Kind) + ": " + e.Msg }
func (e *ProviderError) Unwrap() error { return e.Err }
func (e *ProviderError) Retryable() bool {
	switch e.Kind {
	case ErrKindRateLimit, ErrKindOverloaded, ErrKindNetwork, ErrKindServer:
		return true
	}
	return false
}

type Attempt struct {
	Ref     ModelRef      `json:"ref"`
	Err     string        `json:"err,omitempty"`
	Kind    ErrorKind     `json:"kind,omitempty"`
	Latency time.Duration `json:"latency"`
}

// Breakdown is estimated tokens per prompt region, scaled so the sum equals the
// provider-reported total input.
type Breakdown struct {
	Constitution int `json:"constitution"`
	Persona      int `json:"persona"`
	Memory       int `json:"memory"`
	Skills       int `json:"skills"`
	Pins         int `json:"pins"`
	Tools        int `json:"tools"`
	Summary      int `json:"summary"`
	History      int `json:"history"`
	ToolResults  int `json:"tool_results"`
	TurnContext  int `json:"turn_context"`
	Total        int `json:"total"`
	Window       int `json:"window"`
	Reserve      int `json:"reserve"`
	Cacheable    int `json:"cacheable"` // tokens in the frozen prefix (tools + system)
}

type CallInfo struct {
	ID             string        `json:"id"` // llm_<26>
	Ref            ModelRef      `json:"ref"`
	Slot           Slot          `json:"slot"`
	Purpose        string        `json:"purpose"` // agent|compaction|title|heartbeat|probe|passthrough|notes
	Step           int           `json:"step"`
	Attempts       []Attempt     `json:"attempts"`
	Usage          Usage         `json:"usage"`
	UsageEstimated bool          `json:"usage_estimated"`
	Cost           Micros        `json:"cost_micros"`
	PriceKnown     bool          `json:"price_known"`
	TTFT           time.Duration `json:"ttft"`
	Latency        time.Duration `json:"latency"`
	Breakdown      Breakdown     `json:"breakdown"`
	PrefixHash     string        `json:"prefix_hash"` // sha256(tools bytes || system bytes), hex[:16]
	Stop           StopReason    `json:"stop"`
	Error          string        `json:"error,omitempty"`
}

// BuildFunc assembles the request for the concrete model picked. The facade calls
// it again on fallback so context is re-fitted to that model's window.
type BuildFunc func(m ModelInfo) (*Request, error)

type ProbeConfig struct {
	Kind         ProviderKind `json:"kind"`
	BaseURL      string       `json:"base_url"`
	APIKey       Secret       `json:"-"`          // transient; never persisted by Probe
	SecretRef    string       `json:"secret_ref"` // alternative to APIKey: "secret:<name>"
	Model        string       `json:"model"`
	AllowPrivate bool         `json:"allow_private"`
	API          string       `json:"api,omitempty"`     // openai, openai_compat: auto | responses | chat ("" = auto), as providers[].api
	Region       string       `json:"region,omitempty"`  // bedrock: AWS region; "" = the AWS config's default
	Profile      string       `json:"profile,omitempty"` // bedrock: shared-config profile; "" = AWS_PROFILE or default
}

type ProbeResult struct {
	OK            bool          `json:"ok"`
	Latency       time.Duration `json:"latency"`
	ToolCalling   bool          `json:"tool_calling"` // a forced one-tool call round-tripped
	UsageReported bool          `json:"usage_reported"`
	Context       int           `json:"context"` // effective window
	Models        []string      `json:"models"`
	Cost          Micros        `json:"cost_micros"`
	API           string        `json:"api,omitempty"` // openai, openai_compat: the API tested, "responses" or "chat"
	Detail        string        `json:"detail"`        // plain-language failure or hint (e.g. OLLAMA_CONTEXT_LENGTH)

	Choices     []ModelChoice `json:"choices,omitempty"`     // Models with display names, for a model picker
	Credentials string        `json:"credentials,omitempty"` // bedrock: where the AWS credentials came from, never their values

	Model string      `json:"model,omitempty"` // the model the probe exercised
	Code  FailureCode `json:"code,omitempty"`  // why the check failed (OK false)
	Error string      `json:"error,omitempty"` // the raw, redacted message behind Code
}

// ModelChoice is one entry of a model picker, with the plain names and
// suggestions of ModelInfo.
type ModelChoice struct {
	ID                string `json:"id"`
	Display           string `json:"display"`
	DisplayName       string `json:"display_name,omitempty"`
	ShortName         string `json:"short_name,omitempty"`
	ProviderName      string `json:"provider_name,omitempty"`
	Recommended       bool   `json:"recommended,omitempty"`
	QuickTasksDefault bool   `json:"quick_tasks_default,omitempty"`
}

type Detection struct {
	Kind    ProviderKind `json:"kind"`
	BaseURL string       `json:"base_url"`
	EnvVar  string       `json:"env_var,omitempty"` // e.g. ANTHROPIC_API_KEY; the value never leaves the process
	Models  []string     `json:"models,omitempty"`
	Note    string       `json:"note,omitempty"`
	Region  string       `json:"region,omitempty"`  // bedrock: the AWS config's region, "" when it names none
	Profile string       `json:"profile,omitempty"` // bedrock: the AWS_PROFILE in use, "" for the default chain
	Source  string       `json:"source,omitempty"`  // bedrock: where the AWS credentials came from in plain words ("instance role bothy-prod"), never their values
	Problem string       `json:"problem,omitempty"` // bedrock: "signin_expired" | "no_credentials" when AWS settings give no access yet

	Choices    []ModelChoice `json:"choices,omitempty"`     // Models with plain names and suggestions
	SecretRef  string        `json:"secret_ref,omitempty"`  // a provider key already saved in the global vault: "secret:<name>"
	SecretHint string        `json:"secret_hint,omitempty"` // its last four characters ("" for short values)
}

type ProviderHealth struct {
	ID        string       `json:"id"`
	Kind      ProviderKind `json:"kind"`
	State     string       `json:"state"` // ok | degraded | down | auth_failed
	LastError string       `json:"last_error,omitempty"`
	Since     time.Time    `json:"since"`
	// Source says in plain words where the credentials come from ("Saved
	// key", "AWS Secrets Manager", "This server's AWS access (SSO profile
	// demo)"), never what they are; "" when the provider needs none.
	Source string `json:"source,omitempty"`
}

// LLM is the fallback-aware facade the agent uses. It owns retries, circuit
// breakers and the fallback chain; it never switches model after the first
// streamed event.
type LLM interface {
	Complete(ctx context.Context, slot Slot, override *ModelRef, build BuildFunc, on func(StreamEvent) error) (*Response, CallInfo, error)
	// Passthrough calls exactly one model, no fallback, for /v1 passthrough mode.
	Passthrough(ctx context.Context, ref ModelRef, req *Request, on func(StreamEvent) error) (*Response, CallInfo, error)
	Chain(slot Slot, override *ModelRef) []ModelInfo // resolved chain; [0] drives context sizing
	Model(ref ModelRef) (ModelInfo, bool)
	Models(ctx context.Context) []ModelInfo
	Estimate(text string, ref ModelRef) int // bytes/3.6 times the per-model EWMA correction
	Calibrate(ref ModelRef, estimated, actual int)
	Probe(ctx context.Context, cfg ProbeConfig) (ProbeResult, error)
	Detect(ctx context.Context) []Detection
	Health() []ProviderHealth
}
```

`Request.Stop` is ignored by the OpenAI Responses client: that API has no stop sequences. `Request.CacheKey`: the agent sets `"bothy-"+hex(sha256("bothy prompt cache\0"+session id))[:32]` on every call of a session, refine drafts included, so the key is stable across turns and never reveals the session id; compaction, titles, notes drafts and `/v1` passthrough send none. The Responses client sends it as `prompt_cache_key` (omitted when empty); the other clients ignore it.

`failure.go` (v0.2 UI, docs/design/ui.md §17 S13) gives every failed check a code the web UI picks its sentence from; the raw, redacted message travels next to it (`ProbeResult.Code`/`Error`, `GitHubProbe.Code`, and the error code of a failed `POST /api/channels/{name}/test`). Provider, channel and GitHub errors implement `FailureCoder`; `TransportFailure` classifies a failure to reach a service at all.

```go
package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"syscall"
)

// FailureCode names why a provider, channel or GitHub check failed, so the
// web UI picks its sentence from a code instead of parsing messages (v0.2
// UI §5.6.4). The raw message always travels next to it.
type FailureCode string

const (
	FailKeyRejected      FailureCode = "key_rejected"       // the key or token was not accepted
	FailNoModelAccess    FailureCode = "no_model_access"    // the key works but cannot use this model
	FailOutOfCredit      FailureCode = "out_of_credit"      // the account has no credit or quota left
	FailRateLimited      FailureCode = "rate_limited"       // the service is limiting requests
	FailProviderError    FailureCode = "provider_error"     // the service failed on its side (5xx, overloaded)
	FailTimeout          FailureCode = "timeout"            // no answer in time
	FailUnreachable      FailureCode = "unreachable"        // connection refused, unknown host, no route
	FailPrivateAddress   FailureCode = "private_address"    // egress refused a private address without allow_private
	FailNoModels         FailureCode = "no_models"          // the server answers but lists no models
	FailTLS              FailureCode = "tls"                // certificate or TLS handshake problem
	FailNotAModelServer  FailureCode = "not_a_model_server" // something answered that does not speak the model API
	FailAWSNoCredentials FailureCode = "aws_no_credentials" // the AWS credential chain resolved nothing
	FailAWSDenied        FailureCode = "aws_denied"         // AWS refused this identity
	FailAWSNoModels      FailureCode = "aws_no_models"      // no Claude or Nova model is enabled in the region
	FailNotFound         FailureCode = "not_found"          // a cloud secret store has no such secret (v0.2, §14.8)
	FailJSONKeyMissing   FailureCode = "json_key_missing"   // the secret is a JSON object without the referenced key
	FailNotJSON          FailureCode = "not_json"           // a JSON key was referenced but the secret is not a JSON object
	FailUnknown          FailureCode = "unknown"
)

// FailureCoder is an error that knows its FailureCode.
type FailureCoder interface{ FailureCode() FailureCode }

// CodeOf returns the code err carries (FailureCoder), else its
// TransportFailure, else "".
func CodeOf(err error) FailureCode {
	var fc FailureCoder
	if errors.As(err, &fc) {
		if c := fc.FailureCode(); c != "" {
			return c
		}
	}
	return TransportFailure(err)
}

// privateRanges are the ranges egress admits only with allow_private; a
// refusal for one of them is fixed by allowing that exact address.
var privateRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.168.0.0/16", "::1/128", "fc00::/7", "fe80::/10"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// blockedRangeRe reads the range out of egress's refusal ("destination
// address blocked: … (address in blocked range 127.0.0.0/8)").
var blockedRangeRe = regexp.MustCompile(`blocked range ([0-9a-fA-F.:]+/[0-9]+)`)

// TransportFailure classifies a failure to reach a service at all: timeout,
// tls, private_address or unreachable. It returns "" for anything else (an
// HTTP answer, a malformed reply), which the caller classifies itself.
func TransportFailure(err error) FailureCode {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return FailTimeout
	case strings.Contains(msg, "destination address blocked"):
		if m := blockedRangeRe.FindStringSubmatch(msg); m != nil {
			if p, perr := netip.ParsePrefix(m[1]); perr == nil {
				for _, r := range privateRanges {
					if r == p.Masked() {
						return FailPrivateAddress
					}
				}
			}
		}
		return FailUnreachable
	case isTLS(err, msg):
		return FailTLS
	case strings.Contains(msg, "destination not allowed"), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.ECONNRESET):
		return FailUnreachable
	}
	var de *net.DNSError
	var oe *net.OpError
	if errors.As(err, &de) || errors.As(err, &oe) {
		return FailUnreachable
	}
	// Messages carried as text only (a redacted provider error) still name
	// the condition.
	for _, s := range []string{"i/o timeout", "deadline exceeded", "Client.Timeout"} {
		if strings.Contains(msg, s) {
			return FailTimeout
		}
	}
	for _, s := range []string{"connection refused", "no such host", "no route to host", "network is unreachable",
		"connection reset", "server misbehaving"} {
		if strings.Contains(msg, s) {
			return FailUnreachable
		}
	}
	return ""
}

func isTLS(err error, msg string) bool {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var he x509.HostnameError
	var cv *tls.CertificateVerificationError
	var rh tls.RecordHeaderError
	if errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &he) || errors.As(err, &cv) || errors.As(err, &rh) {
		return true
	}
	return strings.Contains(msg, "x509: ") || strings.Contains(msg, "tls: ")
}
```

### 4.4 `tool.go`: side effects, taint, manifests, actions, tools, registry

```go
package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CanonicalJSON re-encodes JSON (a json.RawMessage, a []byte, or any
// JSON-marshalable value) with object keys sorted, no insignificant whitespace,
// numbers kept verbatim and HTML escaping off. Action.Payload, Fingerprint,
// ToolSpec.Hash, MCP pin hashes and the audit chain all hash these bytes, so
// every module agrees on them.
func CanonicalJSON(v any) ([]byte, error) {
	var raw []byte
	switch x := v.(type) {
	case json.RawMessage:
		raw = x
	case []byte:
		raw = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ActionFingerprint is hex sha256(tool || 0x00 || canonical payload).
func ActionFingerprint(tool string, canonicalPayload []byte) string {
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write(canonicalPayload)
	return hex.EncodeToString(h.Sum(nil))
}

// SideEffect classes. The zero value is invalid so a missing manifest field is
// rejected at Register instead of silently meaning "read".
type SideEffect uint8

const (
	SERead SideEffect = iota + 1
	SELocalWrite
	SEExternalSend
	SEIrreversible
	SEFinancial
	SEAdmin // self-modification: config, policy, prompts, skills, MCP, schedules, auth
)

var seNames = [...]string{"", "read", "local_write", "external_send", "irreversible", "financial", "admin"}

func (s SideEffect) String() string {
	if int(s) < len(seNames) {
		return seNames[s]
	}
	return fmt.Sprintf("side_effect(%d)", s)
}

func (s SideEffect) Valid() bool { return s >= SERead && s <= SEAdmin }

func (s SideEffect) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("invalid side effect %d", s)
	}
	return []byte(s.String()), nil
}

func (s *SideEffect) UnmarshalText(b []byte) error {
	v, err := ParseSideEffect(string(b))
	*s = v
	return err
}

func ParseSideEffect(name string) (SideEffect, error) {
	for i := 1; i < len(seNames); i++ {
		if seNames[i] == name {
			return SideEffect(i), nil
		}
	}
	return 0, fmt.Errorf("unknown side effect %q", name)
}

// Taint legs of the lethal trifecta, persisted per session as a bitmask.
type Taint uint8

const (
	TaintPrivate   Taint = 1 << iota // private data entered context (memory snapshot, private reads)
	TaintUntrusted                   // untrusted content entered context
	TaintSend                        // an exfiltration-capable capability is enabled (informational leg)
)

// Exposed is private+untrusted. In this state every external_send or
// irreversible action is forced to Ask (the trifecta floor).
func (t Taint) Exposed() bool { return t&TaintPrivate != 0 && t&TaintUntrusted != 0 }

func (t Taint) Has(x Taint) bool { return t&x == x }

type Manifest struct {
	Title       string        `json:"title"` // card title, e.g. "Run shell command"
	SideEffect  SideEffect    `json:"side_effect"`
	Exec        bool          `json:"exec"`     // needs a sandbox; hidden when none is available
	FSPaths     []string      `json:"fs_paths"` // workspace-relative globs it may touch
	Domains     []string      `json:"domains"`  // static egress domains; dynamic ones go in Action
	Secrets     []string      `json:"secrets"`  // vault names used gateway-side only
	Messaging   bool          `json:"messaging"`
	Taint       Taint         `json:"taint"`              // legs added to the session when it runs
	Triggers    []Trigger     `json:"triggers,omitempty"` // empty = all; schedule tools = {message}
	MinRole     Role          `json:"min_role,omitempty"` // empty = any
	SpaceOK     bool          `json:"space_ok"`           // offered to restricted space principals
	Core        bool          `json:"core"`               // schema always in the tools block; else via tool_search
	Timeout     time.Duration `json:"timeout"`
	MaxOutputKB int           `json:"max_output_kb"`
	AlwaysAsk   bool          `json:"always_ask,omitempty"` // every call is a card (policy floor): no preset, rule or grant allows it, no allow_session
}

type ToolSpec struct {
	Def      ToolDef  `json:"def"`
	Source   string   `json:"source"` // "builtin" | "mcp:<server>" | "skill:<name>"
	Manifest Manifest `json:"manifest"`
	Hash     string   `json:"hash"` // sha256 of canonical Def JSON
}

type ActionKind string

const (
	ActExec     ActionKind = "exec"
	ActFSRead   ActionKind = "fs.read"
	ActFSWrite  ActionKind = "fs.write"
	ActFSDelete ActionKind = "fs.delete"
	ActHTTP     ActionKind = "http" // gateway-side fetch
	ActNet      ActionKind = "net"  // egress grant request (net_allow, proxy hold-for-approval)
	ActSend     ActionKind = "send" // message to a chat
	ActMCP      ActionKind = "mcp"  // MCP tool call
	ActMemory   ActionKind = "memory"
	ActSchedule ActionKind = "schedule"
	ActSkill    ActionKind = "skill"  // skill_load / skill_read
	ActSearch   ActionKind = "search" // web_search, session_search
	ActGeneric  ActionKind = "generic"
	// ActRelease dispatches a deploy workflow (release_deploy). Policy gives
	// it its own floor: never auto, never via rules or grants, only in
	// interactive runs, approved by the environment's approvers.
	ActRelease ActionKind = "release"
)

// Action is the structured, system-normalized form of a tool call. Policy,
// approval cards, audit and loop detection consume only this, and Exec receives
// exactly the Action that was decided (and, for parked runs, persisted).
type Action struct {
	Tool        string          `json:"tool"`
	Kind        ActionKind      `json:"kind"`
	SideEffect  SideEffect      `json:"side_effect"` // may escalate above Manifest.SideEffect, never lower
	Argv        []string        `json:"argv,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	Method      string          `json:"method,omitempty"`
	URL         string          `json:"url,omitempty"`
	Host        string          `json:"host,omitempty"` // punycode, lowercase
	Port        int             `json:"port,omitempty"`
	Paths       []string        `json:"paths,omitempty"`
	Diff        string          `json:"diff,omitempty"` // unified diff for fs.write, capped 64KB
	Recipient   string          `json:"recipient,omitempty"`
	Domains     []string        `json:"domains,omitempty"`
	MCPServer   string          `json:"mcp_server,omitempty"`
	MCPTool     string          `json:"mcp_tool,omitempty"`
	Payload     json.RawMessage `json:"payload"`     // normalized args (CanonicalJSON), executed verbatim
	Fingerprint string          `json:"fingerprint"` // ActionFingerprint(Tool, Payload)
}

// Exec actions: Payload is {"argv":[…],"cwd":"…"} (shell: ["bash","-lc",cmd];
// python adds "code" and argv ["python3",".bothy/tmp/<fingerprint[:16]>.py"],
// Exec writes the code there first), so JSON pointers such as "/argv/2" in
// rules and grants address the command line and cards show the exact code.

// ToolEnv carries per-call context. Every capability a tool may use is here or
// injected into the tool's constructor; tools never reach for globals.
type ToolEnv struct {
	User        UserID
	Role        Role
	Principal   Principal
	Session     SessionID
	Run         RunID
	CallID      string // tcl_<26>
	Trigger     Trigger
	Schedule    ScheduleID
	Workspace   string // absolute dir; tools open it with os.OpenRoot
	BoxKey      string // box_<26> for this session
	Sandbox     Sandbox
	HTTP        *http.Client // egress-guarded public client scoped to this session (web_fetch)
	Chat        *ChatRef     // originating chat; the message tool can only reply here
	Taint       Taint
	Interactive bool
	Selection   Selection       // the run's tool selection (tool_search passes it to Registry.Search)
	Approval    ApprovalID      // the approval this call runs under, if any (set for Exec)
	Emit        func(EventData) // tool.output progress; non-blocking, rate-limited by the runner
}

type Tool interface {
	Spec() ToolSpec
	// Plan validates and normalizes args into an Action. It may read (workspace
	// files via os.Root, the DB) but must not write, send, exec or dial, and is
	// deterministic for the same input and state (conformance-tested).
	Plan(ctx context.Context, env *ToolEnv, args json.RawMessage) (*Action, error)
	Exec(ctx context.Context, env *ToolEnv, a *Action) (*ToolResult, error)
}

// Gated is optionally implemented by tools whose availability depends on
// runtime state the registry cannot see (message: env.Chat != nil; skill_load:
// at least one enabled skill; MCP tools: server ready). ForRun, Available and
// Search skip a tool whose Enabled returns false.
type Gated interface {
	Enabled(env *ToolEnv) bool
}

// ToolProvider is implemented by modules that contribute tools. cmd/bothy
// registers them at boot.
type ToolProvider interface{ Tools() []Tool }

type Selection struct {
	SandboxOK  bool
	SmallModel bool // compact profile: <= 8 core tools, short descriptions
	Degraded   bool // degraded profile: <= 5 core tools, no skills index
	Paused     bool // kill switch: read-only tools only
}

type ToolRegistry interface {
	Register(t Tool) error // name collision, invalid manifest or schema => error
	UnregisterSource(source string)
	// ReplaceSource swaps every tool registered under source for tools in one
	// step, so a run never sees the source half re-registered. Tools that fail
	// validation, name another source or collide are skipped and reported in
	// the (joined) error; the others are registered.
	ReplaceSource(source string, tools []Tool) error
	// Get returns the tool behind JSON Schema validation of its arguments
	// (before Plan; violations wrap ErrInvalid) and normalisation of the
	// planned Action (canonical payload, fingerprint, side effect never below
	// the manifest). Callers need no validator of their own (§5.7 b).
	Get(name string) (Tool, bool)
	// ForRun returns the core tools for the tools block plus tool_search/use_tool
	// when lazy tools exist, filtered by trigger, role, principal kind, sandbox and
	// Selection, sorted by name. Bytes must be identical for identical inputs.
	ForRun(env *ToolEnv, sel Selection) []ToolSpec
	// Available reports whether a (possibly lazy) tool is callable in this run.
	Available(name string, env *ToolEnv, sel Selection) bool
	Search(query string, env *ToolEnv, sel Selection, limit int) []ToolSpec
	All() []ToolSpec
}
```

### 4.5 `policy.go`: decisions, grants, cards, approvals

```go
package core

import (
	"context"
	"time"
)

type Verdict string

const (
	VerdictAuto Verdict = "auto"
	VerdictAsk  Verdict = "ask"
	VerdictDeny Verdict = "deny"
)

type TimeoutAction string

const (
	TimeoutDeny TimeoutAction = "deny"
	TimeoutPark TimeoutAction = "park"
)

type Choice string

const (
	AllowOnce    Choice = "allow_once"
	AllowSession Choice = "allow_session"
	// AllowAlways creates the same narrow grant as AllowSession but with
	// GrantPrincipal scope, so it persists across sessions until revoked.
	// Offered only where AllowSession is, and never for irreversible actions.
	AllowAlways Choice = "allow_always"
	Deny        Choice = "deny"
)

type PolicyInput struct {
	User       UserID // the principal's account; "" for team-owned principals
	Role       Role
	Principal  Principal
	Session    SessionID
	Schedule   ScheduleID
	Trigger    Trigger
	Action     *Action
	Spec       ToolSpec
	Taint      Taint
	Provenance Provenance // provenance of the message that started the run
	Chat       *ChatRef
}

// ApproverReq says who may resolve an approval.
//
// A multi-approver approval (Users or Roles set, User empty, AdminOnly false)
// is resolved by votes: it is allowed once Needed() distinct eligible users
// have voted allow_once, and denied by the first eligible deny. Requester's
// allow never counts; the requester may still deny (withdraw). Eligibility is
// evaluated when the vote is cast, so a disabled or demoted account loses it.
type ApproverReq struct {
	User      UserID `json:"user_id"`    // the principal's account; empty when AdminOnly, Team or multi-approver
	AdminOnly bool   `json:"admin_only"` // admin-class actions: any owner/admin
	// Team: a channel's or teammate's request. Any enabled owner or admin on
	// the web, or a teammate an admin marked "Can approve" pressing a recorded
	// chat card, may resolve it.
	Team      bool     `json:"team,omitempty"`
	Users     []UserID `json:"users,omitempty"`     // multi-approver: users named by id
	Roles     []Role   `json:"roles,omitempty"`     // multi-approver: any user holding at least one of these roles
	Required  int      `json:"required,omitempty"`  // multi-approver: distinct allow votes needed (0 means 1)
	Requester UserID   `json:"requester,omitempty"` // multi-approver: whose allow never counts; "" for a teammate's request
	// RequesterIdentity is "<channel>:<external id>" of the chat sender whose
	// message started a team-owned principal's run; set by Approvals.Request.
	// Only this sender may withdraw (deny) from chat without Can approve.
	RequesterIdentity string `json:"requester_identity,omitempty"`
}

// Multi reports whether the approval is resolved by votes.
func (r ApproverReq) Multi() bool {
	return !r.AdminOnly && !r.Team && (len(r.Users) > 0 || len(r.Roles) > 0)
}

// Decider is who resolved an approval: a web account or a chat teammate.
type Decider struct {
	User     UserID // web
	Identity string // chat: "<channel>:<external id>"
}

// Needed is the number of distinct allow votes a multi-approver approval needs.
func (r ApproverReq) Needed() int { return max(r.Required, 1) }

// Eligible reports whether a user with this role may vote allow on a
// multi-approver approval. Roles rank owner > admin > member: naming a role
// admits that role and every higher one. Account state (disabled) is the
// caller's to check.
func (r ApproverReq) Eligible(u UserID, role Role) bool {
	if !r.Multi() || u == "" || u == r.Requester {
		return false
	}
	for _, x := range r.Users {
		if x == u {
			return true
		}
	}
	for _, x := range r.Roles {
		if rank := roleRank(x); rank > 0 && roleRank(role) >= rank {
			return true
		}
	}
	return false
}

func roleRank(r Role) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember:
		return 1
	}
	return 0
}

// ApprovalVote is one approver's vote on a multi-approver approval. A user
// votes at most once per approval.
type ApprovalVote struct {
	Approval ApprovalID `json:"approval_id"`
	User     UserID     `json:"user_id"`
	Choice   Choice     `json:"choice"` // allow_once | deny
	Via      string     `json:"via"`    // web | telegram | discord | slack
	At       time.Time  `json:"at"`
	Comment  string     `json:"comment,omitempty"` // the voter's reason, e.g. for a deny
}

// AllowVotes counts the distinct allow votes that count towards Needed().
func AllowVotes(votes []ApprovalVote, r ApproverReq) int {
	seen := map[UserID]bool{}
	for _, v := range votes {
		if v.Choice == AllowOnce && v.User != r.Requester {
			seen[v.User] = true
		}
	}
	return len(seen)
}

type Decision struct {
	Verdict   Verdict       `json:"verdict"`
	Rule      string        `json:"rule"`   // "floor:financial" | "floor:trifecta" | "rule:<id>" | "grant:<id>" | "first_contact:domain" | "default:external_send"
	Reason    string        `json:"reason"` // safe to show the model and the user
	Floor     bool          `json:"floor"`
	Approver  ApproverReq   `json:"approver"`
	Timeout   time.Duration `json:"timeout"`
	OnTimeout TimeoutAction `json:"on_timeout"`
	Choices   []Choice      `json:"choices"` // AllowSession omitted when no narrow grant can be formed or when Exposed
	Warnings  []string      `json:"warnings,omitempty"`
}

type GrantScope string

const (
	GrantSession   GrantScope = "session"
	GrantSchedule  GrantScope = "schedule"
	GrantPrincipal GrantScope = "principal" // persistent; created by admins in the web UI, or narrow-only via an allow_always card choice
)

type GrantKind string

const (
	GrantTool      GrantKind = "tool"      // Tool + Pattern (JSON pointer -> glob); never a whole tool
	GrantDomain    GrantKind = "domain"    // Value "host:port"; "*.suffix:443" only for GrantPrincipal by admins
	GrantRecipient GrantKind = "recipient" // Value = ChatRef.Key() or MCP recipient field value
)

type Grant struct {
	ID               GrantID           `json:"id"`
	Principal        PrincipalID       `json:"principal_id"`
	Scope            GrantScope        `json:"scope"`
	ScopeID          string            `json:"scope_id"` // session or schedule id; "" for principal
	Kind             GrantKind         `json:"kind"`
	Tool             string            `json:"tool,omitempty"`
	Pattern          map[string]string `json:"pattern,omitempty"` // JSON pointer into Action.Payload -> glob
	Value            string            `json:"value,omitempty"`
	MaxSE            SideEffect        `json:"max_se"`
	AllowWhenTainted bool              `json:"allow_when_tainted"` // only valid with Kind recipient and an exact Value
	Uses             int               `json:"uses"`
	MaxUses          int               `json:"max_uses"` // 0 = unlimited
	CreatedBy        UserID            `json:"created_by"`
	// CreatedByIdentity: a chat approver created it from a card
	// ("<channel>:<external id>"); CreatedBy is then "".
	CreatedByIdentity string    `json:"created_by_identity,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"` // zero = never
}

type Policy interface {
	// Decide order: floors -> rules -> grants -> first contact -> class defaults.
	// No rule or grant overrides a floor (sole exception: AllowWhenTainted
	// recipient grant vs the trifecta floor).
	Decide(ctx context.Context, in PolicyInput) Decision
	// NarrowGrant builds the grant that "allow for session" creates for an Action,
	// or ok=false when no narrow grant exists (then AllowSession is not offered).
	NarrowGrant(a *Action, p PrincipalID, scope GrantScope, scopeID string, by Decider) (g Grant, ok bool)
	Grant(ctx context.Context, g Grant) (GrantID, error)
	Revoke(ctx context.Context, v Viewer, id GrantID, by UserID) error
	// NextTaint is pure. boxEgressed reports that the session box moved bytes
	// through the egress proxy during this call (output becomes untrusted).
	NextTaint(prev Taint, spec ToolSpec, a *Action, res *ToolResult, boxEgressed bool) Taint
}

// CardKind tells renderers which layout a card uses (v0.2 S2).
type CardKind string

const (
	CardAction      CardKind = "action"
	CardPullRequest CardKind = "pull_request" // repo_open_pr
	CardDeploy      CardKind = "deploy"       // release_deploy
	CardReview      CardKind = "review"       // a suggested text change (project notes)
)

// CardTone colours the primary button: destructive for irreversible and
// financial actions, except deploys, whose safeguard is their approvers.
type CardTone string

const (
	ToneDefault     CardTone = "default"
	ToneDestructive CardTone = "destructive"
)

type CardField struct {
	Label string `json:"label"` // plain, e.g. "File", "Command", "To"
	Value string `json:"value"` // verbatim; renderers escape and visualize bidi/zero-width chars
	Mono  bool   `json:"mono"`
}

// Card is rendered by the policy module from the Action only, never from model
// prose. Channel adapters format it; they never add content. Fields, Warnings
// and the plain-copy fields below (v0.2 S2) are the words every surface shows.
type Card struct {
	Title    string      `json:"title"` // the tool's title, e.g. "Delete file"
	Risk     SideEffect  `json:"risk"`
	Fields   []CardField `json:"fields"`
	Code     string      `json:"code,omitempty"` // exact argv / JSON args
	Diff     string      `json:"diff,omitempty"`
	Warnings []string    `json:"warnings,omitempty"`
	Context  string      `json:"context"` // "Telegram · DM · session “Q3 report”"
	Choices  []Choice    `json:"choices"`
	Expires  time.Time   `json:"expires"`
	WebURL   string      `json:"web_url"` // public_url + /approvals/<id>

	// Plain copy, set on every card rendered since v0.2; cards stored before
	// that lack it, hence omitempty.
	Question     string   `json:"question,omitempty"`      // "Delete notes/staging.md?"
	Consequence  string   `json:"consequence,omitempty"`   // "This can't be undone."
	PrimaryLabel string   `json:"primary_label,omitempty"` // allow_once's verb ("Delete file"), else "Allow for this chat"
	Ing          string   `json:"ing,omitempty"`           // "deleting notes/staging.md", as in "You approved …"
	Kind         CardKind `json:"kind,omitempty"`
	Tone         CardTone `json:"tone,omitempty"`
}

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalAllowed   ApprovalStatus = "allowed"
	ApprovalDenied    ApprovalStatus = "denied"
	ApprovalExpired   ApprovalStatus = "expired"
	ApprovalParked    ApprovalStatus = "parked"
	ApprovalCancelled ApprovalStatus = "cancelled"
)

type Approval struct {
	ID         ApprovalID     `json:"id"`
	Run        RunID          `json:"run_id"`
	Session    SessionID      `json:"session_id"`
	Principal  PrincipalID    `json:"principal_id"`
	ToolCallID string         `json:"tool_call_id"`
	Action     *Action        `json:"action"`
	Decision   Decision       `json:"decision"`
	Card       Card           `json:"card"`
	Status     ApprovalStatus `json:"status"`
	Choice     Choice         `json:"choice,omitempty"`
	Approver   ApproverReq    `json:"approver"`
	Chat       *ChatRef       `json:"chat,omitempty"` // originating chat
	OnTimeout  TimeoutAction  `json:"on_timeout"`
	DecidedBy  UserID         `json:"decided_by,omitempty"`
	// DecidedByIdentity: a chat teammate decided ("<channel>:<external
	// id>"); DecidedBy is then "".
	DecidedByIdentity string    `json:"decided_by_identity,omitempty"`
	DecidedVia        string    `json:"decided_via,omitempty"` // web|telegram|discord|slack|timeout|stop (CancelRun)
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ParkUntil         time.Time `json:"park_until,omitempty"`
	DecidedAt         time.Time `json:"decided_at,omitempty"`
	// DecidedByName is DecidedBy's display name; the HTTP API fills it on reads.
	DecidedByName string `json:"decided_by_name,omitempty"`
	// Votes of a multi-approver approval, oldest first. Approvals.Get, Pending,
	// Recent and Resolve fill it; repositories leave it empty.
	Votes []ApprovalVote `json:"votes,omitempty"`
	// Comment is the deciding approver's reason (Resolution.Comment).
	Comment string `json:"comment,omitempty"`
	// Approvals (multi-approver only) and Requester carry display names for
	// cards (v0.2 S4); they are filled like Votes.
	Approvals *ApprovalTally  `json:"approvals,omitempty"`
	Requester *ApprovalPerson `json:"requester,omitempty"`
}

// ApprovalPerson names who asked on an approval card: an account, or a
// person or channel in a chat app.
type ApprovalPerson struct {
	User     UserID `json:"user"`
	Name     string `json:"name"`               // display name, else username (chat: display name, else handle)
	App      string `json:"app,omitempty"`      // the chat app of a teammate or channel requester
	Chat     string `json:"chat,omitempty"`     // a channel's request, whoever asked: the channel's title ("#eng")
	Identity string `json:"identity,omitempty"` // "<channel>:<external id>", so chat cards can mention the real profile
	Handle   string `json:"handle,omitempty"`   // the chat requester's @handle, when they have one
	// Kind is whose request it is: RequesterTeammate (a teammate's own
	// chat) or RequesterChannel; "" for an account's.
	Kind string `json:"kind,omitempty"`
	// Scheduled: nobody asked; the teammate's or channel's schedule
	// started the run, and Name is that teammate or channel.
	Scheduled bool `json:"scheduled,omitempty"`
}

// ApprovalPerson kinds.
const (
	RequesterTeammate = "teammate"
	RequesterChannel  = "channel"
)

// ApprovalTally is where a multi-approver approval stands.
type ApprovalTally struct {
	Required int              `json:"required"` // distinct approvals needed, the requester's never counting
	Votes    []ApprovalBallot `json:"votes"`    // oldest first
	// Eligible lists the enabled users other than the requester who may
	// approve, leaving out admins and owners when AnyAdmin already covers them.
	Eligible []ApprovalPerson `json:"eligible"`
	AnyAdmin bool             `json:"any_admin"` // every admin may approve through a role
}

// ApprovalBallot is one vote with the voter's name.
type ApprovalBallot struct {
	User    UserID    `json:"user"`
	Name    string    `json:"name"`
	Choice  Choice    `json:"choice"` // allow_once | deny
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

type ApprovalRequest struct {
	Run        RunID
	Session    SessionID
	Principal  PrincipalID
	ToolCallID string
	Action     *Action
	Spec       ToolSpec
	Decision   Decision // from Policy.Decide (Verdict Ask)
	Chat       *ChatRef
	Context    string // card context line, built by the caller from session/channel
	Sender     string // RunRequest.Sender of the run ("" for schedules)
}

type Resolution struct {
	ID         ApprovalID
	Choice     Choice
	By         UserID
	ByIdentity string // a chat press by a teammate ("<channel>:<external id>"); By is then ""
	ByRole     Role
	Via        string  // web | telegram | discord | slack
	ViaMsg     *MsgRef // required for chat channels: must be a recorded card message
	Comment    string
}

type Approvals interface {
	// Request persists, renders the Card and publishes approval.requested with
	// Principal = r.Principal; for AdminOnly approvals it also publishes a copy
	// with Principal "" (admin broadcast) so every admin's inbox sees it, and
	// for multi-approver approvals a copy to each eligible approver's own
	// principal. approval.resolved and approval.voted are published the same way.
	Request(ctx context.Context, r ApprovalRequest) (*Approval, error)
	// Await blocks until resolved, timed out, or ctx done. Timeout: deny =>
	// ErrApprovalExpired; park => ErrParked (status parked, ParkUntil set).
	Await(ctx context.Context, id ApprovalID) (Choice, error)
	// Resolve is a CAS from pending|parked. It checks approver authority, that a
	// chat press comes from a recorded card message, and that a truncated card is
	// only ever denied from chat. Publishes approval.resolved. On a
	// multi-approver approval it records r.By's vote (approval.voted) and
	// resolves only on a deny or the Needed()th distinct allow; the returned
	// approval may still be pending.
	Resolve(ctx context.Context, r Resolution) (*Approval, error)
	CancelRun(ctx context.Context, run RunID) error
	// RenderCard renders without persisting (policy preview, tests).
	RenderCard(r ApprovalRequest) Card
	Get(ctx context.Context, v Viewer, id ApprovalID) (*Approval, error)
	Pending(ctx context.Context, v Viewer) ([]*Approval, error)        // pending + parked
	Recent(ctx context.Context, v Viewer, p Page) ([]*Approval, error) // decided, newest first; Before = created_at ms
	// ButtonData returns "a:<id>:<o|s|a|d>:<mac8>" (<= 64 bytes, Telegram limit);
	// the mac is HMAC-SHA256(server key, id|choice) truncated to 8 base32 chars.
	ButtonData(id ApprovalID, c Choice) string
	ParseButtonData(data string) (ApprovalID, Choice, error)                            // verifies the mac
	AddMessageRef(ctx context.Context, id ApprovalID, ref MsgRef, truncated bool) error // a card (kind card)
	// AddMessage records a chat message of any kind: a link-only card, or
	// the requester's waiting line (kind notice, which only withdraws).
	AddMessage(ctx context.Context, id ApprovalID, m CardMessage) error
	MessageRefs(ctx context.Context, id ApprovalID) ([]CardMessage, error)
	// OnResolved registers a callback; the runner uses it to resume parked runs.
	OnResolved(fn func(a *Approval))
}

type CardMessage struct {
	Ref       MsgRef `json:"ref"`
	Truncated bool   `json:"truncated"`
	Kind      string `json:"kind"` // card | link (a link-only card) | notice (the requester's waiting line)
}

// CardMessage kinds; Messages lists cards, then links, then notices.
const (
	CardMsgCard   = "card"
	CardMsgLink   = "link"
	CardMsgNotice = "notice"
)
```

### 4.6 `channel.go`: chats, inbound/outbound, adapters, the hub

```go
package core

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type ChatKind string

const (
	ChatDM    ChatKind = "dm"
	ChatGroup ChatKind = "group" // groups, Discord guild channels, Slack channels
	ChatWeb   ChatKind = "web"
	ChatAPI   ChatKind = "api"
)

type ChatRef struct {
	Channel  string   `json:"channel"` // "telegram" | "discord" | "slack" | "web" | "api"
	ChatID   string   `json:"chat_id"`
	ThreadID string   `json:"thread_id,omitempty"` // Slack thread ts, Telegram topic, Discord thread
	Kind     ChatKind `json:"kind"`
}

// Key is the session lookup key for channel sessions.
func (c ChatRef) Key() string { return c.Channel + ":" + c.ChatID + ":" + c.ThreadID }

type Attachment struct {
	Name     string `json:"name"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	FetchRef string `json:"fetch_ref"` // adapter-private (Telegram file_id, Slack url_private, Discord CDN url)
}

type Inbound struct {
	Chat ChatRef
	// MsgID must be unique per adapter (it keys inbound_dedup): Telegram
	// "<chat_id>:<message_id>", Slack "<channel>:<ts>", Discord the message
	// snowflake. Edits of an existing message are not redelivered as new.
	MsgID          string
	SenderID       string // platform user id
	SenderName     string
	SenderUsername string
	SenderIsBot    bool
	Text           string // mention of the bot stripped
	Mentioned      bool   // DM, @mention, reply-to-bot, or slash command
	Attachments    []Attachment
	At             time.Time
	// ChatTitle is the group's title as the platform reports it (names a
	// newly sighted space, and renames one still under its fallback name);
	// "" when unknown.
	ChatTitle string
	// AddedBy is the platform user id that added the bot to this group,
	// when the platform reports it (Telegram my_chat_member.from); ""
	// otherwise. A group Inbound with AddedBy and no text or attachments only
	// announces the group.
	AddedBy string
	// Team is a group chat's team when the platform says: the Discord guild
	// id; "" otherwise.
	Team string
	// Forwarded marks content the sender relayed from someone else
	// (Telegram forward_origin or via_bot). The hub treats it as untrusted
	// even from the teammate whose DM it is, and never as a command.
	Forwarded bool
	// Private marks a message only its sender saw (a Slack slash command).
	// In a group it never creates a space, since slash commands work in
	// channels the bot is not in. The hub answers its commands, and the
	// notice that Bothy is off there, to the sender alone where they typed
	// it (PrivateReplier), else in their DM; anyone it would not answer in
	// that group hears in their DM.
	Private bool
	// ReplyRef is adapter-private: how a Private message in a group is
	// answered to its sender alone (a Slack slash command's response_url).
	// It is a credential for posting there: never logged, stored or put in
	// a run.
	ReplyRef string `json:"-"`
}

type MsgRef struct {
	Chat ChatRef `json:"chat"`
	ID   string  `json:"id"`
}

type ButtonStyle uint8

const (
	BtnDefault ButtonStyle = iota
	BtnPrimary
	BtnDanger
)

type Button struct {
	Label string      `json:"label"`
	Data  string      `json:"data,omitempty"` // callback payload <= 64 bytes; produced only by Approvals.ButtonData or the router ("s:<run>" stop)
	URL   string      `json:"url,omitempty"`  // link button (no callback)
	Style ButtonStyle `json:"style"`
}

// OutMessage text is the Bothy markdown subset: paragraphs, **bold**, *italic*,
// `code`, fenced code, lists, [text](url), headings rendered as bold. No images,
// no raw HTML. The router has already split it to fit Caps.MaxLen and
// neutralized links for exposed sessions. Agent output never carries Buttons.
type OutMessage struct {
	Chat    ChatRef
	Text    string
	Buttons [][]Button
	ReplyTo string
	Silent  bool
	// Mentions are the platform user ids whose "<app>://user?id=…" link in
	// Text shows as that person's mention (Slack <@U…>, Discord's pill).
	// Only the hub's approval lines set it: the same link in model text
	// stays a plain link, so nothing Bothy reads can make it ping someone.
	Mentions []string
}

type ActionEvent struct {
	Chat     ChatRef
	Msg      MsgRef
	SenderID string
	Data     string
	AckRef   string // adapter-private: callback_query id, interaction token, Slack response_url
}

type ChannelCaps struct {
	Edit            bool          `json:"edit"`
	Buttons         bool          `json:"buttons"`
	Threads         bool          `json:"threads"`
	Typing          bool          `json:"typing"`
	MaxLen          int           `json:"max_len"`           // visible characters per message after rendering
	EditMinInterval time.Duration `json:"edit_min_interval"` // Telegram 1s, Discord 1s, Slack 1.2s
	// CommandPrefix is what a person types before a chat command's name:
	// "/bothy " on Slack, whose one slash command takes the rest as its
	// text; "" means "/" (Telegram, and Discord's plain-text commands).
	CommandPrefix string `json:"command_prefix,omitempty"`
	// PrivatePosts is how many posts one Private message's ReplyRef takes
	// (PrivateReplier; Slack's response_url takes 5); 0 means no limit. An
	// answer that needs more goes to the sender's DM whole.
	PrivatePosts int `json:"private_posts,omitempty"`
}

// PrivateReplier is implemented by adapters that can answer a Private
// message where it was typed, seen by its sender alone (Slack: an ephemeral
// reply through Inbound.ReplyRef). markdown is one piece within
// Caps.MaxLen, already redacted. Errors never contain the ReplyRef.
type PrivateReplier interface {
	ReplyPrivate(ctx context.Context, in Inbound, markdown string) error
}

type InboundSink interface {
	OnMessage(ctx context.Context, in Inbound) // must not block on agent work
	OnAction(ctx context.Context, ev ActionEvent)
}

type ChannelStatus struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	State    string    `json:"state"` // connecting | connected | backoff | auth_failed | disabled
	Detail   string    `json:"detail,omitempty"`
	Identity string    `json:"identity,omitempty"` // "@my_bothy_bot"
	Since    time.Time `json:"since"`
	Team     string    `json:"team,omitempty"` // Slack: the workspace name (auth.test); Discord: "" (several servers)
	Org      bool      `json:"org,omitempty"`  // Slack: installed org-wide on Enterprise Grid
	// MemberCheck (Discord): "ok", or "no_access" when every server refused
	// the last member lookup, so only people an admin lets in get in.
	MemberCheck string `json:"member_check,omitempty"`
	// IdleServers (Discord, connected): servers Bothy is in where no channel
	// is on, whose members therefore can't use it.
	IdleServers int `json:"idle_servers,omitempty"`
	// AppID (Slack, once connected): the app's id, for links to its settings
	// page (where only a person can change its icon).
	AppID string `json:"app_id,omitempty"`
}

// MemberStatus is what a chat app says about whether a person belongs to
// the team Bothy serves there.
type MemberStatus string

const (
	MemberUnchecked MemberStatus = ""         // never asked
	MemberUnknown   MemberStatus = "unknown"  // the platform can't tell (Telegram; Discord with no known server or no access)
	MemberYes       MemberStatus = "member"   // in the workspace or an enabled server
	MemberOutsider  MemberStatus = "outsider" // guest, other company, bot, deactivated, not in an enabled server
)

// How long membership verdicts count (SPEC §5.1 step 3). In verdicts.go,
// which the generated web types skip.
const (
	MemberTTL     = time.Hour        // a verdict older than this is checked again before use
	MemberStale   = 4 * time.Hour    // a member verdict this young still counts for chats and schedules when the platform doesn't answer
	ApproverFresh = 2 * time.Hour    // full approver cards need a verdict this young; older gets a link-only card
	ApproverStale = 15 * time.Minute // a press falls back to a verdict this young when its recheck fails
)

type Membership struct {
	Status MemberStatus
	Team   string // the workspace or server's name for copy; "" when unknown or several
}

// MemberChecker is implemented by adapters whose platform can say whether a
// person belongs to the team Bothy serves (Slack, Discord).
type MemberChecker interface {
	// CheckMember asks about externalID. teams are the Discord guild ids of
	// the group chats Bothy is turned on in (Slack checks its own workspace).
	// An error means the platform did not answer; it is never a verdict.
	CheckMember(ctx context.Context, externalID string, teams []string) (Membership, error)
	// MemberScope names what membership means right now (Slack: team and
	// enterprise ids); when it changes, every verdict on this chat app is stale.
	MemberScope() string
}

// ChatInfo describes a group chat for the admins' turn-on choice.
type ChatInfo struct {
	TeamID, TeamName, Title string
	Members                 int  // 0 = unknown
	External                bool // people from other companies are in it (Slack Connect)
}

// ChatInspector is implemented by adapters that can describe a group chat.
type ChatInspector interface {
	InspectChat(ctx context.Context, chat ChatRef) (ChatInfo, error)
}

type TeamInfo struct{ ID, Name string }

// TeamLister: Discord lists the servers it is in (GUILD_CREATE), so the hub
// can warn about servers where no channel is on.
type TeamLister interface{ Teams() []TeamInfo }

// Channel adapters never do auth, policy, splitting or neutralization: the
// router does. Adapters escape for their dialect and always disable link
// previews/unfurls (Telegram link_preview_options.is_disabled, Slack
// unfurl_links/unfurl_media=false, Discord flags SUPPRESS_EMBEDS).
type Channel interface {
	Name() string
	Caps() ChannelCaps
	Run(ctx context.Context, sink InboundSink) error // blocks; reconnects internally with backoff
	Send(ctx context.Context, m OutMessage) (MsgRef, error)
	Edit(ctx context.Context, ref MsgRef, m OutMessage) error
	Delete(ctx context.Context, ref MsgRef) error
	Typing(ctx context.Context, chat ChatRef) error
	Ack(ctx context.Context, ev ActionEvent, toast string) error // within 3s
	OpenDM(ctx context.Context, externalUserID string) (ChatRef, error)
	Fetch(ctx context.Context, a Attachment, max int64) (io.ReadCloser, error)
	SelfTest(ctx context.Context) (identity string, err error) // getMe / users/@me / auth.test
	Status() ChannelStatus
}

// Cards on channels without Caps.Buttons (P1 email, Signal) are always sent
// truncated as text with the Card.WebURL link only; they can never approve.

// Messenger sends a system-originated message (message tool, schedule delivery,
// hello DM). Bound to explicit chats; the message tool only passes env.Chat.
type Messenger interface {
	Send(ctx context.Context, chat ChatRef, markdown string) (MsgRef, error)
}

// ChannelHub is the channel router. It owns adapters, inbound gatekeeping,
// commands, run streaming to chats and approval card delivery.
type ChannelHub interface {
	InboundSink
	Messenger
	Add(ch Channel)
	Run(ctx context.Context) error
	Status() []ChannelStatus
	Test(ctx context.Context, name string) (string, error)
	// PrincipalChat is where Bothy's own messages to a principal go: an
	// admitted teammate's DM (opened when unknown), an enabled channel's chat;
	// false for web accounts, which hear on the web only.
	PrincipalChat(ctx context.Context, p PrincipalID) (ChatRef, bool)
	// TeamActive is nil when a team-owned principal may run a schedule now;
	// otherwise why not (blocked, no longer in the team, channel off, or a
	// membership verdict older than MemberStale that couldn't be renewed).
	TeamActive(ctx context.Context, p PrincipalID) error
	// Greet sends a teammate their pending greeting (hello or moved) once.
	Greet(ctx context.Context, channel, externalID string) error
	// TellApprover tells a teammate an admin made them an approver.
	TellApprover(ctx context.Context, channel, externalID string) error
	// StreamTo returns a sink that delivers a run to a chat: mode "stream"
	// (throttled edits) or "final" (one message at run end; schedules).
	StreamTo(chat ChatRef, mode string) RunSink
	// ApplyPicture gives every turned-on chat app Bothy's picture p, the one
	// just set, replacing whatever picture its bot has, and says per app what
	// happened (sorted by app). Each app gets the picture Bothy holds when its
	// turn comes, so of two changes at once the later stays. Apps without a
	// PictureSetter are PictureManual.
	ApplyPicture(ctx context.Context, p Picture) []PictureResult
}

// AdapterDeps is what channel adapters may use. Adapters import only core,
// config and their protocol library.
type AdapterDeps struct {
	Secret   func(ctx context.Context, ref string) (Secret, error) // resolves "secret:<name>" (global scope) and "aws-sm:<secret>"
	HTTP     func(d SystemDest) *http.Client                       // Egress.SystemClient
	Meta     MetaRepo                                              // long-poll offsets, resume cursors (keys "channel.<name>.*")
	StateDir string                                                // <data>/channels/<name>/ (0700): P1 adapters with their own state (Matrix crypto store, WhatsApp device store)
	Clock    Clock
	Log      *slog.Logger
}
```

### 4.7 `sandbox.go`: boxes and egress

```go
package core

import (
	"context"
	"io"
	"net/http"
	"time"
)

type SelfTestCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type SandboxStatus struct {
	Backend   string          `json:"backend"` // sandboxd | bwrap | none
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"` // why not available, plain language
	Fix       string          `json:"fix,omitempty"`    // copyable command or doc anchor
	Runtime   string          `json:"runtime"`          // runsc | runc | bwrap
	Engine    string          `json:"engine,omitempty"` // "docker 27.4.1 (api 1.47)"
	Rootless  bool            `json:"rootless"`
	SelfTest  []SelfTestCheck `json:"self_test"`
	CheckedAt time.Time       `json:"checked_at"`
}

type BoxProfile string

const (
	ProfileBase BoxProfile = "base" // python3, bash, git, curl, uv
	ProfileNode BoxProfile = "node" // base + Node 24 LTS
)

type BoxLimits struct {
	MemoryMB int     `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	Pids     int     `json:"pids"`
}

type BoxSpec struct {
	Key       string // box_<26>; DerivedID("box", "session:"+id) or ("mcp:"+server+":"+principal)
	Profile   BoxProfile
	Workspace WorkspaceID // mounted rw at /workspace; "" = none (MCP servers without workspace access)
	MCPTree   string      // "mcp/<server>/<install_id>" mounted at /opt/mcp; "" = none
	MCPTreeRW bool        // true only for the installer box
	Limits    BoxLimits
	Egress    BoxEgress
}

type BoxEgress struct {
	Session   SessionID
	Principal PrincipalID
	Extra     []string // host:port always allowed for this box (MCP server egress list, installer registries)
	NoGrants  bool     // ignore session/principal grants (MCP and installer boxes)
}

type ExecRequest struct {
	Argv      []string
	Stdin     []byte
	Cwd       string            // relative to /workspace, cleaned
	Env       map[string]string // keys ^[A-Z_][A-Z0-9_]{0,63}$; no LD_*, PATH, HOME, *_PROXY, BASH_ENV, NODE_OPTIONS, PYTHONPATH
	Timeout   time.Duration
	MaxOutput int // bytes kept per stream; the rest is drained and dropped
}

type ExecResult struct {
	Exit      int           `json:"exit"`
	Stdout    []byte        `json:"-"`
	Stderr    []byte        `json:"-"`
	Truncated bool          `json:"truncated"`
	TimedOut  bool          `json:"timed_out"`
	Killed    bool          `json:"killed"` // by /stop
	Duration  time.Duration `json:"duration"`
}

// Sandbox is the gateway-side client for the active backend (sandboxd, bwrap or
// none). With backend none every method except Status, Usage and Watch returns
// ErrNoSandbox.
type Sandbox interface {
	Status(ctx context.Context) SandboxStatus
	// Ensure creates the directories the box mounts (work volume: ws/<id> and,
	// when set, the MCPTree dir, both 2770 group 10001, plus skills/ if missing;
	// run volume: egress/<h> 0750 group 10001), opens the box's egress socket via
	// Egress.OpenBox, then creates the box (idempotent per Key).
	Ensure(ctx context.Context, spec BoxSpec) error
	// Usage reports a workspace's size against sandbox.workspace_quota_mb from
	// the periodic quota scan (works with every backend, including none; fs_write
	// refuses writes over quota).
	Usage(ctx context.Context, ws WorkspaceID) (usedBytes, quotaBytes int64, err error)
	Exec(ctx context.Context, key string, req ExecRequest, onOut func(stream int, b []byte)) (*ExecResult, error)
	Stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error)
	// Kill SIGKILLs every process in the box except its init (sbx-init killall);
	// falls back to removing the box. The workspace persists. p99 < 1s.
	Kill(ctx context.Context, key string) error
	Destroy(ctx context.Context, key string) error // also calls Egress.CloseBox(key)
	Watch(fn func(SandboxStatus))                  // status changes (self-test failures, sandboxd restarts)
}

// SystemDest describes a gateway-originated destination (provider, channel API,
// search API, remote MCP server). Only these exact host:port pairs are dialable
// by the returned client.
type SystemDest struct {
	Name  string   // "provider:anthropic", "channel:telegram", "mcp:docs", "search:brave"
	Hosts []string // exact "host:port"
	// AllowPrivate admits RFC1918, ULA, CGNAT, loopback and link-local addresses
	// for these exact hosts (Ollama on the host or LAN, the searxng service).
	// Cloud metadata addresses stay blocked.
	AllowPrivate bool
}

// EgressBinding carries what the proxy needs to log a connection and, for an
// unknown destination, to build the hold-for-approval PolicyInput.
type EgressBinding struct {
	Run         RunID
	ToolCallID  string
	User        UserID // the principal's account; "" for team-owned principals
	Role        Role
	Sender      string // RunRequest.Sender, so a team request names who asked
	Trigger     Trigger
	Schedule    ScheduleID // schedule-scoped (pre-authorized) domain grants apply at the proxy
	Interactive bool       // hold unknown CONNECTs for approval (egress.hold_for_approval) instead of failing
	Chat        *ChatRef   // for the hold-for-approval card
	Context     string
	Provenance  Provenance // of the message that started the run: floor F6 applies to held connections
}

type Egress interface {
	// OpenBox creates /run/bothy/egress/<h>/proxy.sock for the box (the socket is
	// the box identity) and returns the volume subpath "egress/<h>".
	OpenBox(ctx context.Context, key string, e BoxEgress) (subpath string, err error)
	CloseBox(key string)
	// Bind attaches the current run/tool call to a box before each exec, for
	// logging, taint and hold-for-approval.
	Bind(key string, b EgressBinding)
	// Unbind ends toolCallID's binding once its exec returns: its allow_once
	// admissions go, and later connections (background processes) are no
	// longer held for approval. The run stays bound for logging.
	Unbind(key, toolCallID string)
	// TakeActivity reports whether the box moved bytes since the last call.
	TakeActivity(key string) bool
	// PublicClient is the session-scoped client for gateway-side tools
	// (web_fetch): any public address, SSRF-guarded, pinned DNS, per-hop checks.
	PublicClient(s SessionID, p PrincipalID) *http.Client
	// SystemClient is for providers, channels, search APIs and HTTP MCP servers.
	SystemClient(d SystemDest) *http.Client
}
```

### 4.8 `store.go`: entities and repositories

```go
package core

import (
	"context"
	"encoding/json"
	"time"
)

// Page is keyset pagination: Before is an exclusive upper bound on the row's
// sort key (seq, id or unix ms depending on the method); 0 = newest.
type Page struct {
	Before int64
	Limit  int // 0 = repository default (50), max 500
}

type User struct {
	ID           UserID    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         Role      `json:"role"`
	PWHash       string    `json:"-"` // PHC $argon2id$v=19$m=19456,t=2,p=1$…
	Timezone     string    `json:"timezone"`
	Disabled     bool      `json:"disabled"`
	FailedLogins int       `json:"-"`
	LockedUntil  time.Time `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// MustChangePassword: the account has a one-time password an admin
	// handed out; its owner must choose their own before anything else.
	MustChangePassword bool `json:"must_change_password,omitempty"`

	// LastLoginAt is the last sign-in by any method; zero = never.
	// UserRepo.SignedIn writes it, Update leaves it alone.
	LastLoginAt time.Time `json:"last_login_at"`
}

// Space is a channel (kind "group") or a teammate's own space (kind
// "person"). Both are owned by the team.
type Space struct {
	Principal PrincipalID `json:"principal_id"`
	Kind      string      `json:"kind"` // "group" | "person"
	// Chat of a person space is the teammate's external id or DM; DMs route
	// by identity, never by it.
	Chat        ChatRef   `json:"chat"`
	Team        string    `json:"team_id,omitempty"`   // group: the Discord guild id; "" when unknown
	TeamName    string    `json:"team_name,omitempty"` // group: the server or workspace name, for admins
	AddedBy     string    `json:"added_by,omitempty"`  // group: platform id of whoever added Bothy, when reported
	Title       string    `json:"title"`
	Members     int       `json:"members"`  // group: people in the chat or server as last reported; 0 = unknown
	External    bool      `json:"external"` // group: Slack says people from other companies are in it
	Enabled     bool      `json:"enabled"`  // person: false while the teammate is blocked
	MentionOnly bool      `json:"mention_only"`
	ReviewedAt  time.Time `json:"reviewed_at,omitzero"` // group: an admin turned it on or ignored it; zero = still in Approvals
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Space kinds.
const (
	SpaceGroup  = "group"
	SpacePerson = "person"
)

type IdentityStatus string

const (
	IdentityPending  IdentityStatus = "pending"  // not let in: waiting for an admin, or not a member
	IdentityTeammate IdentityStatus = "teammate" // Bothy accepts them; they have a person space
	IdentityBlocked  IdentityStatus = "blocked"
)

// AdmittedVia is how a teammate got in.
type AdmittedVia string

const (
	ViaMembership AdmittedVia = "membership" // the platform said they are in the team
	ViaAdmin      AdmittedVia = "admin"      // an admin let them in (or the 0013 migration did)
)

// Greetings a teammate is still owed.
const (
	GreetHello = "hello"
	GreetMoved = "moved" // migrated from a linked account
)

type ChannelIdentity struct {
	Channel     string         `json:"channel"`
	ExternalID  string         `json:"external_id"`
	Username    string         `json:"username"`
	DisplayName string         `json:"display_name"`
	Status      IdentityStatus `json:"status"`
	Principal   PrincipalID    `json:"principal_id,omitempty"` // teammate: their person space
	Via         AdmittedVia    `json:"admitted_via"`
	CanApprove  bool           `json:"can_approve"` // an admin marked them "Can approve"
	Member      MemberStatus   `json:"membership"`
	MemberAt    time.Time      `json:"membership_at"`
	DMChat      string         `json:"dm_chat_id,omitempty"`
	FirstSeen   time.Time      `json:"first_seen_at"`
	LastSeen    time.Time      `json:"last_seen_at"`
	LastNotice  time.Time      `json:"-"`
	Greet       string         `json:"-"` // "" | hello | moved: the greeting still owed
	// FirstText is the start of a pending sender's first message, shown to
	// admins in the approvals inbox; it is dropped once the status changes.
	FirstText string `json:"first_text,omitempty"`
}

// Key is "<channel>:<external id>", as RunRequest.Sender and approval
// requester identities carry it.
func (c ChannelIdentity) Key() string { return c.Channel + ":" + c.ExternalID }

type AuthSession struct {
	IDHash      []byte
	User        UserID
	CSRF        string
	IP, UA      string
	CreatedAt   time.Time
	LastSeen    time.Time
	IdleExpires time.Time
	Expires     time.Time
}

type TokenRecord struct {
	ID         string    `json:"id"` // tok_<26>, public part of bothy_pat_<id>_<secret>_<crc6>
	User       UserID    `json:"user_id"`
	Name       string    `json:"name"`
	SecretHash []byte    `json:"-"`
	Scopes     []string  `json:"scopes"`      // chat | llm | read | admin
	OnApproval string    `json:"on_approval"` // deny | wait
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsed   time.Time `json:"last_used_at"`
	Expires    time.Time `json:"expires_at"`
	Revoked    time.Time `json:"revoked_at"`
}

type Session struct {
	ID             SessionID   `json:"id"`
	Principal      PrincipalID `json:"principal_id"`
	Origin         string      `json:"origin"`      // web|telegram|discord|slack|api|schedule
	ChannelKey     string      `json:"channel_key"` // ChatRef.Key() for channel sessions; "" otherwise
	Chat           *ChatRef    `json:"chat,omitempty"`
	Title          string      `json:"title"`
	ModelSlot      Slot        `json:"model_slot"`
	ModelOverride  string      `json:"model_override,omitempty"` // "provider/model"
	SystemSnapshot string      `json:"-"`                        // frozen S2 block text
	ToolsSnapshot  []string    `json:"-"`                        // frozen core tool names
	SnapshotHash   string      `json:"-"`                        // sha256 of tools+system bytes at snapshot time
	Summary        string      `json:"-"`
	SummaryUpto    int64       `json:"summary_upto"` // last message seq folded into Summary
	Epoch          int         `json:"epoch"`        // incremented by each compaction
	MemorySeen     int64       `json:"-"`            // last memory_events id folded into the snapshot
	PinsSeen       int64       `json:"-"`            // last pin id folded into the snapshot
	Taint          Taint       `json:"taint"`
	Hidden         bool        `json:"hidden"` // ephemeral API sessions
	Archived       bool        `json:"archived"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	LastMessageAt  time.Time   `json:"last_message_at"`
}

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunParked          RunStatus = "parked"
	RunDone            RunStatus = "done"
	RunStopped         RunStatus = "stopped"
	RunFailed          RunStatus = "failed"
	RunBudgetExceeded  RunStatus = "budget_exceeded"
	RunLoopDetected    RunStatus = "loop_detected"
	RunInterrupted     RunStatus = "interrupted" // process restarted mid-run
	RunSkipped         RunStatus = "skipped"     // schedule fired while target session queue was full
)

func (s RunStatus) Terminal() bool {
	switch s {
	case RunQueued, RunRunning, RunWaitingApproval, RunParked:
		return false
	}
	return true
}

type Run struct {
	ID         RunID       `json:"id"`
	Session    SessionID   `json:"session_id"`
	Principal  PrincipalID `json:"principal_id"`
	User       UserID      `json:"user_id"` // the principal's account; "" for team-owned principals
	Trigger    Trigger     `json:"trigger"`
	Schedule   ScheduleID  `json:"schedule_id,omitempty"`
	Status     RunStatus   `json:"status"`
	Error      string      `json:"error,omitempty"`
	Steps      int         `json:"steps"`
	ToolCalls  int         `json:"tool_calls"`
	Usage      Usage       `json:"usage"`
	Cost       Micros      `json:"cost_micros"`
	Models     []string    `json:"models"`
	Limits     RunLimits   `json:"limits"`
	ParkedOn   ApprovalID  `json:"parked_on,omitempty"`
	StoppedBy  UserID      `json:"stopped_by,omitempty"`
	Refined    bool        `json:"refined"` // refine mode wrote the answer
	CreatedAt  time.Time   `json:"created_at"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt time.Time   `json:"finished_at"`
}

type RunFilter struct {
	Session  SessionID
	Schedule ScheduleID
	Status   RunStatus
	Page     Page
}

type ToolCallStatus string

const (
	TCPlanned  ToolCallStatus = "planned"
	TCWaiting  ToolCallStatus = "waiting"
	TCApproved ToolCallStatus = "approved"
	TCDenied   ToolCallStatus = "denied"
	TCRunning  ToolCallStatus = "running"
	TCOK       ToolCallStatus = "ok"
	TCError    ToolCallStatus = "error"
	TCTimeout  ToolCallStatus = "timeout"
	TCStopped  ToolCallStatus = "stopped"
	TCInvalid  ToolCallStatus = "invalid" // unknown tool or schema-invalid args; for refused args the record's Action is a generic one holding them (redacted), so the task can name what was asked
)

type ToolCallRecord struct {
	ID            string         `json:"id"` // tcl_<26>
	Run           RunID          `json:"run_id"`
	Session       SessionID      `json:"session_id"`
	Principal     PrincipalID    `json:"principal_id"`
	Step          int            `json:"step"`
	CallID        string         `json:"call_id"` // provider call id
	Tool          string         `json:"tool"`
	Source        string         `json:"source"`
	SideEffect    SideEffect     `json:"side_effect"`
	Action        *Action        `json:"action,omitempty"`
	Verdict       Verdict        `json:"verdict"`
	Rule          string         `json:"rule"`
	Approval      ApprovalID     `json:"approval_id,omitempty"`
	Status        ToolCallStatus `json:"status"`
	ResultPreview string         `json:"result_preview"` // <= 2KB, redacted
	ResultBytes   int            `json:"result_bytes"`
	FullRef       string         `json:"full_ref,omitempty"`
	Untrusted     bool           `json:"untrusted"`
	Duration      time.Duration  `json:"duration"`
	CreatedAt     time.Time      `json:"created_at"`
	FinishedAt    time.Time      `json:"finished_at"`
}

type ScheduleKind string

const (
	SchedCron      ScheduleKind = "cron"      // 5-field cron (gronx), timezone-aware
	SchedHeartbeat ScheduleKind = "heartbeat" // interval "30m", ±10% jitter, active hours
	SchedOnce      ScheduleKind = "once"      // RFC3339
)

type Schedule struct {
	ID          ScheduleID   `json:"id"`
	Principal   PrincipalID  `json:"principal_id"`
	Name        string       `json:"name"`
	Kind        ScheduleKind `json:"kind"`
	Spec        string       `json:"spec"`
	TZ          string       `json:"tz"`
	ActiveHours string       `json:"active_hours,omitempty"` // "08:00-22:00" in TZ
	Prompt      string       `json:"prompt"`
	Session     SessionID    `json:"session_id,omitempty"` // "" = dedicated schedule session
	Deliver     *ChatRef     `json:"deliver,omitempty"`    // nil = ChannelHub.PrincipalChat (a teammate's DM, a channel's chat; the web for accounts); {channel:"web"} = web only, valid for user principals only
	Slot        Slot         `json:"slot"`
	Limits      RunLimits    `json:"limits"`
	MissedRun   string       `json:"missed_run"` // skip | once
	Enabled     bool         `json:"enabled"`
	CreatedBy   string       `json:"created_by"` // "user:<id>" | "run:<id>" (agent-created after admin approval) | "system" (heartbeat)
	Failures    int          `json:"failures"`   // consecutive; 3 => auto-disabled
	NextRunAt   time.Time    `json:"next_run_at"`
	LastRunAt   time.Time    `json:"last_run_at"`
	LastStatus  RunStatus    `json:"last_status,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type UsageQuery struct {
	User    UserID // "" = all users (admin)
	From    time.Time
	To      time.Time
	GroupBy string // day | model | user (who paid: the account, else the teammate or channel) | schedule | session
}

type UsageRow struct {
	Key        string `json:"key"`
	Cost       Micros `json:"cost_micros"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
	Calls      int    `json:"calls"`
	PriceKnown bool   `json:"price_known"` // false if any call in the bucket had an unknown price
}

type AuditEntry struct {
	ID      int64           `json:"id"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor"`  // user:<id> | agent:<run> | system | channel:<name>:<ext> | token:<id>
	Action  string          `json:"action"` // see §10 audit action list
	Target  string          `json:"target"`
	Outcome string          `json:"outcome"` // ok | denied | error
	Session SessionID       `json:"session_id,omitempty"`
	Run     RunID           `json:"run_id,omitempty"`
	IP      string          `json:"ip,omitempty"`
	Detail  json.RawMessage `json:"detail"` // redacted JSON; never message content
}

// AuditOrigin is who and where a change came from, for an audit row written
// further down than the request: the vault records its own key changes, and
// the API passes the token, client address and source along with ctx.
type AuditOrigin struct {
	Actor  string         // token:<id>, user:<id> or channel:<app>:<id>; "" keeps the writer's
	IP     string         // client address
	Detail map[string]any // merged into the row's detail, such as {"source": "env:OPENAI_API_KEY"}
	Name   string         // display name of a chat actor for banners ("Maya Chen · Slack")
}

type auditOriginKey struct{}

// WithAuditOrigin returns ctx carrying o.
func WithAuditOrigin(ctx context.Context, o AuditOrigin) context.Context {
	return context.WithValue(ctx, auditOriginKey{}, o)
}

// AuditOriginFrom returns the origin ctx carries, if any.
func AuditOriginFrom(ctx context.Context) (AuditOrigin, bool) {
	o, ok := ctx.Value(auditOriginKey{}).(AuditOrigin)
	return o, ok
}

type AuditFilter struct {
	Actor        string
	ActionPrefix string
	From, To     time.Time
	Page         Page
}

type EgressRecord struct {
	ID        int64         `json:"id"`
	At        time.Time     `json:"at"`
	Dialer    string        `json:"dialer"` // box | public | system
	Session   SessionID     `json:"session_id,omitempty"`
	Principal PrincipalID   `json:"principal_id,omitempty"`
	Run       RunID         `json:"run_id,omitempty"`
	Dest      string        `json:"dest"` // SystemDest.Name for system dialer
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	IP        string        `json:"ip,omitempty"`
	Decision  string        `json:"decision"` // allow | deny | held_allow | held_deny
	Reason    string        `json:"reason,omitempty"`
	Up        int64         `json:"bytes_up"`
	Down      int64         `json:"bytes_down"`
	Duration  time.Duration `json:"duration"`
}

type MCPPinStatus string

const (
	PinPending    MCPPinStatus = "pending"
	PinApproved   MCPPinStatus = "approved"
	PinSuperseded MCPPinStatus = "superseded"
	PinDisabled   MCPPinStatus = "disabled"
)

type MCPPin struct {
	Server     string            `json:"server"`
	Tool       string            `json:"tool"`
	Hash       string            `json:"hash"` // sha256 canonical {name,title,description,inputSchema,outputSchema,annotations}
	Definition json.RawMessage   `json:"definition"`
	Status     MCPPinStatus      `json:"status"`
	SideEffect SideEffect        `json:"side_effect"`
	FieldMap   map[string]string `json:"field_map,omitempty"` // {"recipient":"/to","url":"/url"}
	Private    bool              `json:"private"`             // output counts as private data
	ApprovedBy UserID            `json:"approved_by,omitempty"`
	FirstSeen  time.Time         `json:"first_seen_at"`
	ApprovedAt time.Time         `json:"approved_at"`
}

type MCPInstall struct {
	Server    string    `json:"server"`
	InstallID string    `json:"install_id"` // in_<26>; volume subpath mcp/<server>/<install_id>
	Version   string    `json:"version"`
	LockHash  string    `json:"lock_hash"` // sha256 of the recorded lockfile (TOFU)
	Lock      []byte    `json:"-"`         // package-lock.json or hashed requirements.txt
	TreeHash  string    `json:"tree_hash"`
	Status    string    `json:"status"` // installing | ready | failed | drifted
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type SkillStatus string

const (
	SkillPending  SkillStatus = "pending" // new: disabled until an admin enables
	SkillEnabled  SkillStatus = "enabled"
	SkillDisabled SkillStatus = "disabled"
	SkillChanged  SkillStatus = "changed" // on-disk hash != approved hash: excluded until re-approved
)

type SkillMeta struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Path         string      `json:"path"`
	Hash         string      `json:"hash"`
	ApprovedHash string      `json:"approved_hash"`
	Status       SkillStatus `json:"status"`
	Tokens       int         `json:"tokens"`
	InstalledBy  UserID      `json:"installed_by,omitempty"`
	ApprovedBy   UserID      `json:"approved_by,omitempty"`
	InstalledAt  time.Time   `json:"installed_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// SkillFile is one file of a skill's approved snapshot. Content is kept
// for text files within the snapshot caps; Text is false for the others,
// which only their hash and size describe.
type SkillFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Text    bool   `json:"text"`
	Content string `json:"content,omitempty"`
}

type MemoryFile string

const (
	FileMemory    MemoryFile = "MEMORY"
	FileUser      MemoryFile = "USER"
	FileHeartbeat MemoryFile = "HEARTBEAT"
	// FileTeam is TEAM.md, remembered for everyone: one file for the whole
	// team at <data>/memory/team/TEAM.md, never a principal's.
	FileTeam MemoryFile = "TEAM"
)

type MemoryEvent struct {
	ID         int64       `json:"id"`
	Principal  PrincipalID `json:"principal_id"`
	File       MemoryFile  `json:"file"`
	Op         string      `json:"op"` // add | replace | remove | edit (web editor)
	Before     string      `json:"before"`
	After      string      `json:"after"`
	Run        RunID       `json:"run_id,omitempty"`
	Actor      string      `json:"actor"` // agent:<run> | user:<id>
	Provenance Provenance  `json:"provenance"`
	At         time.Time   `json:"at"`
	// Approval is the admin's approval an agent change to TEAM.md ran under;
	// team rows have no Principal.
	Approval ApprovalID `json:"approval_id,omitempty"`
}

type SearchHit struct {
	Session SessionID `json:"session_id"`
	Title   string    `json:"title"`
	Seq     int64     `json:"seq"`
	Snippet string    `json:"snippet"`
	At      time.Time `json:"at"`
	Rank    float64   `json:"rank"`
	Trusted bool      `json:"trusted"` // provenance user/system; untrusted hits taint the session
}

type Upload struct {
	ID        string      `json:"id"` // upl_<26>
	Principal PrincipalID `json:"principal_id"`
	Session   SessionID   `json:"session_id"`
	Name      string      `json:"name"`
	MIME      string      `json:"mime"`
	Size      int64       `json:"size"`
	Ref       string      `json:"ref"` // workspace-relative: ".bothy/in/<upl>/<name>"
	CreatedAt time.Time   `json:"created_at"`
}

type SecretMeta struct {
	Scope     Scope     `json:"scope"`
	Name      string    `json:"name"`
	Source    string    `json:"source"` // vault | env | aws-sm (a reference in the configuration; never a value)
	Hint      string    `json:"hint"`   // last 4 chars; always "" for aws-sm
	UpdatedAt time.Time `json:"updated_at"`
	LastUsed  time.Time `json:"last_used_at"`
}

// ---- repositories ----

type MetaRepo interface {
	Get(ctx context.Context, k string) (string, error) // ErrNotFound
	Set(ctx context.Context, k, v string) error
	Delete(ctx context.Context, k string) error
}

type UserRepo interface {
	Create(ctx context.Context, u *User) error // also creates the user principal + workspace id
	Get(ctx context.Context, id UserID) (*User, error)
	ByUsername(ctx context.Context, name string) (*User, error)
	List(ctx context.Context) ([]*User, error)
	Update(ctx context.Context, u *User) error
	// SetProfile writes only u's DisplayName, Timezone and UpdatedAt: what
	// people change about themselves (PATCH /api/me) must never write back a
	// role, disabled flag or password hash that an admin or a reset changed
	// meanwhile.
	SetProfile(ctx context.Context, u *User) error
	RecordLogin(ctx context.Context, id UserID, ok bool, lockUntil time.Time) error
	// SignedIn records a successful sign-in at at: LastLoginAt, and the
	// failure count and lock cleared.
	SignedIn(ctx context.Context, id UserID, at time.Time) error
	Count(ctx context.Context) (int, error)
	// Delete removes the users row (user purge). Foreign keys cascade to its
	// principal, sessions (messages, pins, runs, approvals), grants,
	// schedules, memory events, uploads, auth sessions and tokens. Team-owned
	// principals are never touched. audit_log is untouched.
	Delete(ctx context.Context, id UserID) error
}

type PrincipalRepo interface {
	Get(ctx context.Context, v Viewer, id PrincipalID) (*Principal, error)
	ForUser(ctx context.Context, u UserID) ([]*Principal, error) // the user's own principal
	// Team lists the team-owned principals (channels and teammates), oldest first.
	Team(ctx context.Context) ([]*Principal, error)
	Update(ctx context.Context, p *Principal) error
	// Delete removes a team-owned principal and, through foreign keys, its
	// space, sessions, schedules, grants, memory events and uploads; a user
	// principal is refused (ErrInvalid). An identity pointing at it must be
	// moved off first: SET NULL would break "teammate ⇒ principal".
	Delete(ctx context.Context, id PrincipalID) error
}

type SpaceRepo interface {
	Create(ctx context.Context, s *Space, p *Principal) error // creates the space principal too
	ByChat(ctx context.Context, channel, chatID string) (*Space, error)
	Get(ctx context.Context, v Viewer, id PrincipalID) (*Space, error)
	List(ctx context.Context, v Viewer) ([]*Space, error)
	Update(ctx context.Context, s *Space) error // never kind, channel or chat
}

type IdentityRepo interface {
	// Touch inserts an unknown sender as pending with membership id.Member,
	// or refreshes a known one's names and last_seen only; returns the row.
	Touch(ctx context.Context, id *ChannelIdentity) (*ChannelIdentity, error)
	Get(ctx context.Context, channel, externalID string) (*ChannelIdentity, error)
	Update(ctx context.Context, id *ChannelIdentity) error // every mutable column; first text kept only while pending
	// SetDMChat records the identity's DM chat and nothing else, so the hub
	// can remember it without a read-modify-write that could undo a block,
	// an approver change or a verdict committed meanwhile.
	SetDMChat(ctx context.Context, channel, externalID, chatID string) error
	// ByPrincipal returns the identity of a teammate's person space (ErrNotFound when none).
	ByPrincipal(ctx context.Context, p PrincipalID) (*ChannelIdentity, error)
	List(ctx context.Context, status IdentityStatus) ([]*ChannelIdentity, error) // "" = all
	Delete(ctx context.Context, channel, externalID string) error
	// ResetMembership makes every verdict on channel stale (membership_at 0).
	ResetMembership(ctx context.Context, channel string) error
}

type AuthSessionRepo interface {
	Create(ctx context.Context, s *AuthSession) error
	Get(ctx context.Context, idHash []byte) (*AuthSession, error)
	Touch(ctx context.Context, idHash []byte, lastSeen, idleExpires time.Time) error
	Delete(ctx context.Context, idHash []byte) error
	DeleteUser(ctx context.Context, u UserID) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

type TokenRepo interface {
	Create(ctx context.Context, t *TokenRecord) error
	Get(ctx context.Context, id string) (*TokenRecord, error)
	List(ctx context.Context, u UserID) ([]*TokenRecord, error)
	Revoke(ctx context.Context, u UserID, id string, at time.Time) error // ErrNotFound across users
	Touch(ctx context.Context, id string, at time.Time) error
}

type SessionFilter struct {
	Principal     PrincipalID // "" = all principals in the Viewer
	Archived      bool
	IncludeHidden bool
	NonEmpty      bool   // only sessions holding at least one message
	Query         string // title substring
	Page          Page   // Before = last_message_at unix ms
}

type SessionRepo interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, v Viewer, id SessionID) (*Session, error)
	ByChannelKey(ctx context.Context, p PrincipalID, key string) (*Session, error) // open (non-archived) only
	List(ctx context.Context, v Viewer, f SessionFilter) ([]*Session, error)
	Update(ctx context.Context, s *Session) error
	SetTaint(ctx context.Context, id SessionID, t Taint) error
	Archive(ctx context.Context, v Viewer, id SessionID) error
	AddURLs(ctx context.Context, id SessionID, urls []string, source string) error // URL provenance: search | user
	HasURL(ctx context.Context, id SessionID, url string) (bool, error)
}

type MessageRepo interface {
	Append(ctx context.Context, m *Message) error                                                   // assigns ID and Seq atomically; fills messages.text from PlainText
	Range(ctx context.Context, v Viewer, s SessionID, fromSeq int64, limit int) ([]*Message, error) // seq >= fromSeq ascending
	Page(ctx context.Context, v Viewer, s SessionID, p Page) ([]*Message, error)                    // newest first, Before = seq
	Search(ctx context.Context, v Viewer, query string, limit int) ([]SearchHit, error)             // FTS5 MATCH over Viewer's principals, bm25 order
}

type PinRepo interface {
	Add(ctx context.Context, p *Pin) error
	List(ctx context.Context, v Viewer, s SessionID) ([]*Pin, error)
	Delete(ctx context.Context, v Viewer, s SessionID, id int64) error
}

type RunRepo interface {
	Create(ctx context.Context, r *Run) error // runs_one_active index => ErrConflict
	Update(ctx context.Context, r *Run) error
	Get(ctx context.Context, v Viewer, id RunID) (*Run, error)
	List(ctx context.Context, v Viewer, f RunFilter) ([]*Run, error)
	MarkInterrupted(ctx context.Context, now time.Time) ([]*Run, error) // boot: queued|running|waiting_approval -> interrupted; returns the rows changed
	Parked(ctx context.Context) ([]*Run, error)
	AddLLMCall(ctx context.Context, run RunID, p PrincipalID, u UserID, c CallInfo) error
	LLMCalls(ctx context.Context, v Viewer, run RunID) ([]CallInfo, error)
	AddToolCall(ctx context.Context, tc *ToolCallRecord) error
	UpdateToolCall(ctx context.Context, tc *ToolCallRecord) error
	ToolCalls(ctx context.Context, v Viewer, run RunID) ([]*ToolCallRecord, error)
}

type ApprovalRepo interface {
	Create(ctx context.Context, a *Approval) error
	Get(ctx context.Context, v Viewer, id ApprovalID) (*Approval, error)
	// Decide is a CAS: it updates only when status is one of from. Returns false
	// when another resolver won. A chat decider is stored as "chat:<identity>".
	Decide(ctx context.Context, id ApprovalID, from []ApprovalStatus, to ApprovalStatus, c Choice, by Decider, via string, at time.Time) (bool, error)
	// CountOpen counts the principal's pending and parked approvals.
	CountOpen(ctx context.Context, p PrincipalID) (int, error)
	Park(ctx context.Context, id ApprovalID, until time.Time) error
	Pending(ctx context.Context, v Viewer) ([]*Approval, error)
	Recent(ctx context.Context, v Viewer, p Page) ([]*Approval, error) // not pending/parked, newest first; Before = created_at ms
	Due(ctx context.Context, now time.Time) ([]*Approval, error)       // pending past expires_at, parked past park_until
	ByRun(ctx context.Context, run RunID) ([]*Approval, error)
	// Seen reports whether an allowed approval of this principal targeted the
	// recipient (Action.Recipient) or host (Action.Host); used by the policy
	// first-contact step. Empty arguments never match.
	Seen(ctx context.Context, p PrincipalID, recipient, host string) (bool, error)
	AddMessage(ctx context.Context, id ApprovalID, m CardMessage) error
	Messages(ctx context.Context, id ApprovalID) ([]CardMessage, error)
	// AddVote records one vote on a multi-approver approval: ErrConflict when
	// the user already voted on it, ErrInvalid for an unknown approval or a
	// choice other than allow_once or deny.
	AddVote(ctx context.Context, v ApprovalVote) error
	Votes(ctx context.Context, id ApprovalID) ([]ApprovalVote, error) // oldest first
	// SetComment records the deciding approver's reason (Approval.Comment).
	SetComment(ctx context.Context, id ApprovalID, comment string) error
}

type GrantRepo interface {
	Add(ctx context.Context, g *Grant) error
	// Active returns unexpired, unrevoked grants for the principal that apply to
	// the session and/or schedule (plus principal-scoped grants).
	Active(ctx context.Context, p PrincipalID, s SessionID, sch ScheduleID, now time.Time) ([]*Grant, error)
	Use(ctx context.Context, id GrantID) error
	List(ctx context.Context, v Viewer, scope GrantScope, scopeID string) ([]*Grant, error)
	Revoke(ctx context.Context, v Viewer, id GrantID, at time.Time) error
}

type ScheduleRepo interface {
	Upsert(ctx context.Context, s *Schedule) error
	Get(ctx context.Context, v Viewer, id ScheduleID) (*Schedule, error)
	List(ctx context.Context, v Viewer) ([]*Schedule, error)
	Delete(ctx context.Context, v Viewer, id ScheduleID) error
	NextDue(ctx context.Context) (*Schedule, error) // enabled, min next_run_at; ErrNotFound when none
	SetRun(ctx context.Context, id ScheduleID, last, next time.Time, status RunStatus, failures int, enabled bool) error
	// PrincipalsWithEnabled lists the principals that have an enabled schedule.
	PrincipalsWithEnabled(ctx context.Context) ([]PrincipalID, error)
}

// UsageRepo bills spending to principals; a user principal's id is the user's.
type UsageRepo interface {
	AddSpend(ctx context.Context, p PrincipalID, day string, c CallInfo) error // spend_daily UPSERT
	Spend(ctx context.Context, p PrincipalID, day string) (Micros, error)      // p == "" => global total
	// TeamSpend is the day's total of every team-owned principal (spc_ ids).
	TeamSpend(ctx context.Context, day string) (Micros, error)
	Report(ctx context.Context, q UsageQuery) ([]UsageRow, error) // aggregates llm_calls
}

type AuditRepo interface {
	// Append is called only by the audit writer, which computes the chain.
	Append(ctx context.Context, e *AuditEntry, prevHash, hash []byte) (int64, error)
	Head(ctx context.Context) (id int64, hash []byte, err error)
	Range(ctx context.Context, fromID, toID int64, fn func(e *AuditEntry, prevHash, hash []byte) error) error
	Query(ctx context.Context, f AuditFilter) ([]*AuditEntry, error)
}

type EgressRepo interface {
	Add(ctx context.Context, rs []EgressRecord) error // batched by the egress module
	// Query returns rows of the Viewer's principals (s "" = all of them), plus
	// system-dialer rows (principal_id "") when v.Admin. Before = id.
	Query(ctx context.Context, v Viewer, s SessionID, p Page) ([]*EgressRecord, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
}

type MCPRepo interface {
	UpsertPin(ctx context.Context, p *MCPPin) (MCPPinStatus, error) // unseen hash => pending; same as approved => approved
	ApprovePins(ctx context.Context, server string, approve map[string]MCPPin, by UserID, at time.Time) error
	Pins(ctx context.Context, server string) ([]*MCPPin, error) // latest per tool
	Approved(ctx context.Context, server string) (map[string]*MCPPin, error)
	DisableTool(ctx context.Context, server, tool string) error
	PutInstall(ctx context.Context, in *MCPInstall) error
	Install(ctx context.Context, server string) (*MCPInstall, error) // current ready (or latest) install
}

type SkillRepo interface {
	Upsert(ctx context.Context, s *SkillMeta) error
	Get(ctx context.Context, name string) (*SkillMeta, error)
	List(ctx context.Context) ([]*SkillMeta, error)
	Delete(ctx context.Context, name string) error // with its approved files
	// SetApproved replaces the snapshot of the files an admin approved
	// (ErrNotFound for an unknown skill); Approved reads it back by path.
	SetApproved(ctx context.Context, name string, files []SkillFile) error
	Approved(ctx context.Context, name string) ([]SkillFile, error)
}

type MemoryEventRepo interface {
	Add(ctx context.Context, e *MemoryEvent) (int64, error)
	Since(ctx context.Context, p PrincipalID, afterID int64) ([]*MemoryEvent, error)
	List(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, page Page) ([]*MemoryEvent, error)
}

// TeamMemoryEventRepo is the history of TEAM.md (table team_memory_events).
// It belongs to no principal, so it is not Viewer-scoped: every signed-in
// user may read what Bothy remembers for everyone.
type TeamMemoryEventRepo interface {
	Add(ctx context.Context, e *MemoryEvent) (int64, error)      // ErrConflict for an approval already used
	List(ctx context.Context, page Page) ([]*MemoryEvent, error) // newest first; Before = id
}

type VaultRepo interface {
	ActiveKey(ctx context.Context) (id int64, wrapped, check []byte, source string, err error) // ErrNotFound on first boot
	PutKey(ctx context.Context, wrapped, check []byte, source string) (int64, error)           // deactivates the previous key
	PutSecret(ctx context.Context, sc Scope, name string, keyID int64, version int, nonce, ct []byte, hint string, by UserID) error
	GetSecret(ctx context.Context, sc Scope, name string) (keyID int64, version int, nonce, ct []byte, err error)
	DeleteSecret(ctx context.Context, sc Scope, name string) error
	ListSecrets(ctx context.Context, sc Scope) ([]SecretMeta, error)
	TouchSecret(ctx context.Context, sc Scope, name string, at time.Time) error
	// Rewrap runs fn for every secret in one transaction (KEK/DEK rotation).
	Rewrap(ctx context.Context, fn func(sc Scope, name string, keyID int64, version int, nonce, ct []byte) (newKeyID int64, newNonce, newCT []byte, err error)) error
}

type UploadRepo interface {
	Create(ctx context.Context, u *Upload) error
	Get(ctx context.Context, v Viewer, id string) (*Upload, error)
}

type DedupRepo interface {
	Seen(ctx context.Context, channel, msgID string, at time.Time) (bool, error) // insert-or-report
	Prune(ctx context.Context, before time.Time) (int64, error)
}

type Store interface {
	Meta() MetaRepo
	Users() UserRepo
	Principals() PrincipalRepo
	Spaces() SpaceRepo
	Identities() IdentityRepo
	AuthSessions() AuthSessionRepo
	Tokens() TokenRepo
	Sessions() SessionRepo
	Messages() MessageRepo
	Pins() PinRepo
	Runs() RunRepo
	Approvals() ApprovalRepo
	Grants() GrantRepo
	Schedules() ScheduleRepo
	Usage() UsageRepo
	Audit() AuditRepo
	Egress() EgressRepo
	MCP() MCPRepo
	Skills() SkillRepo
	MemoryEvents() MemoryEventRepo
	TeamMemoryEvents() TeamMemoryEventRepo
	Vault() VaultRepo
	Dedup() DedupRepo
	Uploads() UploadRepo
	// Tx runs fn on the single writer connection inside BEGIN IMMEDIATE.
	Tx(ctx context.Context, fn func(Store) error) error
	// Backup writes <dir>/bothy-<UTC yyyymmddThhmmss>.tar (0600) holding a
	// VACUUM INTO snapshot of the DB plus memory/**, knowledge/** (project
	// notes), SOUL.md, picture.png, bothy.yaml and bothy.ui.yaml from the DB's
	// directory.
	// It never includes the KEK (/keys) or workspaces. Returns the path.
	// Restore = stop, untar into /data.
	Backup(ctx context.Context, dir string) (string, error)
	SQLiteVersion() string
	Close() error
}
```

`core.SignInStore` (§15, `signin.go`) is optional like `ReleaseStore`: the SQLite store and memstore implement `SignIns() SignInRepo` outside `Store`, so other stores need not change. `SignInRepo` is not Viewer-scoped (like `UserRepo` and `IdentityRepo` it serves authentication before any Viewer exists): `Create` (`ErrConflict` when the provider account is linked to anyone or the user already has a link for that provider; `ErrInvalid` for an unknown user or provider, an empty subject or over-long fields), `Get(provider, subject)`, `ForUser` (ordered by provider, never nil), `Touch` (last used, email and login) and `Delete(user, provider)` (`ErrNotFound`, also for another user's link). `Users().Delete` cascades to the links.

### 4.9 `vault.go`: secrets, vault, redactor

```go
package core

import "context"

// Scope is "global" or "user:<id>". Spaces have no vault scope.
type Scope string

const GlobalScope Scope = "global"

func UserScope(u UserID) Scope { return Scope("user:" + string(u)) }

// Secret never prints its value through fmt, JSON or slog.
type Secret struct{ v []byte }

func NewSecret(b []byte) Secret               { return Secret{v: b} }
func (s Secret) Reveal() string               { return string(s.v) }
func (s Secret) Bytes() []byte                { return s.v }
func (s Secret) IsZero() bool                 { return len(s.v) == 0 }
func (s Secret) String() string               { return "[redacted]" }
func (s Secret) GoString() string             { return "[redacted]" }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

type VaultStatus struct {
	Sealed    bool   `json:"sealed"`     // passphrase KEK not yet entered
	KEKSource string `json:"kek_source"` // docker_secret | systemd | file | passphrase | env | autogen
	KEKID     string `json:"kek_id"`     // sha256(kek)[:8] hex
	Colocated bool   `json:"colocated"`  // KEK file on the same volume as the DB
	Secrets   int    `json:"secrets"`
	// External counts the configured references to cloud secret stores by
	// source ("aws-sm"). Their values are fetched when used, kept in memory
	// for secrets.aws.cache_ttl and never stored. (v0.2, §14.8)
	External map[string]int `json:"external,omitempty"`
}

// Redactor masks exact secret values (and their base64, base64url, hex and
// percent-encoded forms, for values >= 8 bytes) plus token patterns: sk-…,
// sk-ant-…, xox[abpr]-…, xapp-…, ghp_/gho_/github_pat_…, AIza…, JWTs,
// bothy_pat_…. A value of the form "Bearer <token>" (also Basic, Token, Bot,
// ApiKey, Key; token >= 8 bytes) masks the token alone too. The vault keeps a
// key it replaces or deletes masked (vault.Redactor.Retire) until that name
// changes again, since a session or provider may still send it.
// Applied to logs, audit detail, tool results, stored messages,
// channel output, SSE payloads and model input. Redact and RedactBytes return
// their input unchanged when nothing is masked; RedactBytes may return its
// argument, so callers that keep or modify the result copy it first.
type Redactor interface {
	Redact(s string) string
	RedactBytes(b []byte) []byte
	Add(id string, value []byte) // vault plaintexts, setup token, session cookies in flight
	Remove(id string)
}

// Not part of Redactor: vault.Redactor (and testkit/fakes.Redactor) also
// implement PartialAt(s string) int, which returns where the earliest suffix
// of s starts that is a proper prefix of a registered value or of one of its
// encodings (len(s) when none). Streamers (agent run.delta, run.thinking and
// run.tool_output; the channel hub's live messages) find it by a type
// assertion on interface{ PartialAt(string) int } and hold that tail back, so
// redacting each published piece masks exactly what redacting the whole
// stream would, even for a secret that contains spaces.

type Vault interface {
	Put(ctx context.Context, sc Scope, name string, value []byte, by UserID) (SecretMeta, error)
	// Get resolves BOTHY_SECRET_<NAME> env first, then the given scope; with a user
	// scope it falls back to global. Every use is audited as secret.use(name, purpose).
	Get(ctx context.Context, sc Scope, name, purpose string) (Secret, error)
	// Resolve handles config references "secret:<name>" (user scope, then global;
	// u == "" => global only, used for providers, channels, search and space
	// principals) and (v0.2, §14.8) "aws-sm:<secret id or ARN>[#<json key>]"
	// (AWS Secrets Manager, read with the server's AWS credentials; company-wide,
	// so the same for every u). A ref embedded in a header value ("Bearer secret:x")
	// resolves to the whole header with the ref substituted. Plain strings are
	// rejected for secret-typed fields. Every use is audited as secret.use.
	Resolve(ctx context.Context, ref string, u UserID, purpose string) (Secret, error)
	Delete(ctx context.Context, sc Scope, name string, by UserID) error
	// List returns names, source and hint, never values; the global scope also
	// lists the configuration's aws-sm references (source "aws-sm", no hint).
	List(ctx context.Context, sc Scope) ([]SecretMeta, error)
	Unseal(ctx context.Context, passphrase []byte) error
	RotateKEK(ctx context.Context, by UserID) error
	Status() VaultStatus
	Redactor() Redactor
}
```

### 4.10 `events.go`: the event bus and payloads

v0.2 events defined next to their payloads: `run.refine` (`refine.go`, §4.16), `repo.checkout` (`github.go`, §4.17) and `knowledge.updated` (`knowledge.go`, §4.18). `approval.requested`, `approval.voted` and `approval.resolved` of a multi-approver approval are also published to each eligible approver's own principal.

```go
package core

import "time"

type EventType string

const (
	EvRunQueued         EventType = "run.queued"
	EvRunStarted        EventType = "run.started"
	EvRunStatus         EventType = "run.status"
	EvRunDelta          EventType = "run.delta"    // coalesced by the runner to <= 20/s per run
	EvRunThinking       EventType = "run.thinking" // coalesced like run.delta
	EvToolCall          EventType = "run.tool_call"
	EvToolOutput        EventType = "run.tool_output" // <= 8KB chunk, <= 10/s per call
	EvToolResult        EventType = "run.tool_result"
	EvRunUsage          EventType = "run.usage"
	EvRunParked         EventType = "run.parked"
	EvRunFinished       EventType = "run.finished"
	EvApprovalRequested EventType = "approval.requested"
	EvApprovalResolved  EventType = "approval.resolved"
	EvApprovalVoted     EventType = "approval.voted" // ApprovalData: a vote that did not resolve a multi-approver approval
	EvReleaseDispatched EventType = "release.dispatched"
	EvReleaseFinished   EventType = "release.finished"
	EvInboxChanged      EventType = "inbox.changed" // someone waiting to be let in, a new group chat, or a person's access changed; refetch GET /api/inbox
	EvMessageCreated    EventType = "message.created"
	EvSessionUpdated    EventType = "session.updated"
	EvMemoryUpdated     EventType = "memory.updated"
	EvEgressBlocked     EventType = "egress.blocked"
	EvScheduleRun       EventType = "schedule.run"
	EvBudgetWarning     EventType = "budget.warning"
	EvChannelStatus     EventType = "channel.status"  // admin broadcast
	EvSandboxStatus     EventType = "sandbox.status"  // admin broadcast
	EvMCPPinPending     EventType = "mcp.pin_pending" // admin broadcast
	EvConfigChanged     EventType = "config.changed"  // admin broadcast
	EvBanner            EventType = "banner"          // admin broadcast or per principal
)

// EventData is implemented only by the payload types below; each EventType has
// exactly one payload type (see the table in SPEC §8.2).
type EventData interface{ eventData() }

type Event struct {
	Seq       uint64      `json:"seq"`
	Type      EventType   `json:"type"`
	Principal PrincipalID `json:"principal_id,omitempty"` // "" = admin broadcast
	Session   SessionID   `json:"session_id,omitempty"`
	Run       RunID       `json:"run_id,omitempty"`
	At        time.Time   `json:"at"`
	Data      EventData   `json:"data"`
}

type RunQueuedData struct {
	Position int `json:"position"`
}
type RunStartedData struct {
	Trigger Trigger `json:"trigger"`
	Model   string  `json:"model"`
}
type RunStatusData struct {
	Status string `json:"status"` // thinking | tool | waiting_approval | compacting | queued
	Detail string `json:"detail,omitempty"`
}
type DeltaData struct {
	Text string `json:"text"`
}
type ToolCallData struct {
	CallID  string  `json:"call_id"` // tcl_<26>
	Tool    string  `json:"tool"`
	Title   string  `json:"title"`
	Action  *Action `json:"action"`
	Verdict Verdict `json:"verdict"`
	Rule    string  `json:"rule"`
	// ProviderID is the model's own id of the call (the persisted
	// tool_call part's id), so a client matches live rows to the
	// transcript exactly.
	ProviderID string `json:"provider_call_id,omitempty"`
}
type ToolOutputData struct {
	CallID string `json:"call_id"`
	Stream int    `json:"stream"` // 1 stdout, 2 stderr
	Chunk  string `json:"chunk"`  // redacted
}
type ToolResultData struct {
	CallID    string         `json:"call_id"`
	Status    ToolCallStatus `json:"status"`
	Preview   string         `json:"preview"` // <= 2KB, redacted
	Bytes     int            `json:"bytes"`
	Ms        int64          `json:"ms"`
	Truncated bool           `json:"truncated"`
	Untrusted bool           `json:"untrusted"`
}
type UsageData struct {
	Call     CallInfo `json:"call"`
	RunCost  Micros   `json:"run_cost_micros"`
	DayCost  Micros   `json:"day_cost_micros"`
	DayLimit Micros   `json:"day_limit_micros"`
}
type RunParkedData struct {
	Approval ApprovalID `json:"approval_id"`
}
type RunFinishedData struct {
	Status RunStatus  `json:"status"`
	Error  string     `json:"error,omitempty"`
	Budget BudgetKind `json:"budget,omitempty"` // which limit stopped the run when Status is budget_exceeded
	Steps  int        `json:"steps"`
	Usage  Usage      `json:"usage"`
	Cost   Micros     `json:"cost_micros"`
}
type ApprovalData struct {
	Approval *Approval `json:"approval"`
}
type ApprovalResolvedData struct {
	Approval ApprovalID     `json:"approval_id"`
	Status   ApprovalStatus `json:"status"`
	Choice   Choice         `json:"choice,omitempty"`
	By       UserID         `json:"by,omitempty"`
	// ByIdentity: a chat teammate decided ("<channel>:<external id>"); By is then "".
	ByIdentity string `json:"by_identity,omitempty"`
	Via        string `json:"via"`
	Comment    string `json:"comment,omitempty"` // the deciding approver's reason
}

// ReleaseDispatchedData: GitHub accepted a deploy's workflow_dispatch.
type ReleaseDispatchedData struct {
	Deploy      string `json:"deploy_id"`
	Environment string `json:"environment"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	URL         string `json:"url,omitempty"` // the workflow run's page, when identified
}

// ReleaseFinishedData: Bothy stopped following a deploy, or GitHub refused
// to start it.
type ReleaseFinishedData struct {
	Deploy      string        `json:"deploy_id"`
	Environment string        `json:"environment"`
	Repo        string        `json:"repo"`
	Ref         string        `json:"ref"`
	Status      DeployStatus  `json:"status"` // succeeded | failed | unknown
	OK          bool          `json:"ok"`
	URL         string        `json:"url,omitempty"`
	Duration    time.Duration `json:"duration"` // the run's own start to finish when known
	// FailedStep names the workflow step a failed run stopped at, as written
	// in the repository's workflow file (show it verbatim, never as markup).
	FailedStep string `json:"failed_step,omitempty"`
}

// InboxChangedData names the approvals-inbox item that appeared or changed,
// "sender:<channel>:<external id>" or "group:<principal>" (dto.InboxItem.ID).
type InboxChangedData struct {
	Item string `json:"item"`
}
type MessageData struct {
	Message *Message `json:"message"`
}
type SessionData struct {
	Title string `json:"title"`
	Taint Taint  `json:"taint"`
	Epoch int    `json:"epoch"`
}
type MemoryData struct {
	File    MemoryFile `json:"file"`
	EventID int64      `json:"event_id"`
}
type EgressBlockedData struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Reason string `json:"reason"`
}
type ScheduleRunData struct {
	Schedule ScheduleID `json:"schedule_id"`
	Status   RunStatus  `json:"status"`
}
type BudgetWarningData struct {
	User  UserID `json:"user_id"` // the account whose cap it is; "" for the global cap or a team-owned principal
	Spent Micros `json:"spent_micros"`
	Limit Micros `json:"limit_micros"`
}
type ChannelStatusData struct {
	Status ChannelStatus `json:"status"`
}
type SandboxStatusData struct {
	Status SandboxStatus `json:"status"`
}
type MCPPinData struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}
type ConfigChangedData struct {
	Paths           []string `json:"paths"`
	RestartRequired []string `json:"restart_required"`
}
type BannerData struct {
	Level string `json:"level"` // info | warn | error
	// Code: no_sandbox | sandbox_not_isolated | config_error | provider_auth |
	// provider_key_unread | paused | mcp_pins | mcp_drifted | budget_80 |
	// vault_sealed | safe_mode | restart_required | skills_changed |
	// unknown_sender (someone is waiting to be let in) | new_channel |
	// team_access (the 0013 migration) | chat_budget_80
	Code   string `json:"code"`
	Text   string `json:"text"`
	Action string `json:"action,omitempty"` // SPA route, e.g. "/settings/sandbox"
	Clear  bool   `json:"clear,omitempty"`  // true removes the banner with this Code
	// By, ByName and At say who raised it and when (paused: who paused: a
	// user id, or a chat actor "channel:<app>:<id>").
	By     string    `json:"by,omitempty"`
	ByName string    `json:"by_name,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// PausedBanner is the paused banner, naming who paused and when.
func PausedBanner(by string, name string, at time.Time) BannerData {
	text := "Bothy is paused by an admin."
	if name != "" {
		text = "Bothy is paused by " + name + "."
	}
	return BannerData{Level: "error", Code: "paused", Text: text, Action: "/settings/security", By: by, ByName: name, At: at}
}

func (RunQueuedData) eventData()        {}
func (RunStartedData) eventData()       {}
func (RunStatusData) eventData()        {}
func (DeltaData) eventData()            {}
func (ToolCallData) eventData()         {}
func (ToolOutputData) eventData()       {}
func (ToolResultData) eventData()       {}
func (UsageData) eventData()            {}
func (RunParkedData) eventData()        {}
func (RunFinishedData) eventData()      {}
func (ApprovalData) eventData()         {}
func (ApprovalResolvedData) eventData() {}
func (MessageData) eventData()          {}
func (SessionData) eventData()          {}
func (MemoryData) eventData()           {}
func (EgressBlockedData) eventData()    {}
func (ScheduleRunData) eventData()      {}
func (BudgetWarningData) eventData()    {}
func (ChannelStatusData) eventData()    {}
func (SandboxStatusData) eventData()    {}
func (MCPPinData) eventData()           {}
func (ConfigChangedData) eventData()    {}
func (BannerData) eventData()           {}

func (ReleaseDispatchedData) eventData() {}
func (ReleaseFinishedData) eventData()   {}
func (InboxChangedData) eventData()      {}

type Filter struct {
	Principals []PrincipalID // events for these principals
	Admin      bool          // also admin broadcasts (Principal == "")
	Session    SessionID     // optional narrowing
	Types      []EventType   // optional narrowing
	System     bool          // everything (internal subscribers only)
}

type Subscription interface {
	C() <-chan Event
	Dropped() uint64 // > 0 => the consumer fell behind and must resync
	Close()
}

type Bus interface {
	Publish(e Event) Event // assigns Seq and At; never blocks
	Subscribe(f Filter, buf int) Subscription
	// Since replays events with Seq > after from the 4096-entry ring.
	// complete=false means the gap is outside the ring: the client must resync.
	Since(after uint64, f Filter) (evs []Event, complete bool)
	BootID() string // random per process; SSE ids are "<boot>-<seq>"
	// Banners returns the active banner events matching f: the bus keeps the
	// latest EvBanner per (Principal, Code) and drops it on Clear. dto.Me and the
	// SSE hello use it, so banners survive page reloads.
	Banners(f Filter) []Event
}
```

### 4.11 `budget.go`: limits, meters, loop detection

```go
package core

import (
	"context"
	"time"
)

type RunLimits struct {
	MaxSteps  int           `json:"max_steps"`
	MaxTokens int           `json:"max_tokens"` // cumulative input+output across the run
	MaxTime   time.Duration `json:"max_time"`   // wall clock, approval waits excluded
	MaxCost   Micros        `json:"max_cost_micros"`
}

type BudgetKind string

const (
	BudgetSteps     BudgetKind = "steps"
	BudgetTokens    BudgetKind = "tokens"
	BudgetTime      BudgetKind = "time"
	BudgetRunCost   BudgetKind = "run_cost"
	BudgetDailyCost BudgetKind = "daily_cost"
	BudgetRate      BudgetKind = "rate"
)

type BudgetError struct {
	Kind  BudgetKind
	Limit int64
	Used  int64
	// Scope of a daily_cost error: which cap ran out.
	Scope string // personal | chat | global
}

// BudgetError scopes of the daily caps.
const (
	BudgetPersonal = "personal" // the account's or teammate's own cap
	BudgetChat     = "chat"     // the share of the daily limit chat apps may use together
	BudgetGlobal   = "global"
)

func (e *BudgetError) Error() string { return "budget exceeded: " + string(e.Kind) }

type LoopVerdict uint8

const (
	LoopOK LoopVerdict = iota
	LoopWarn
	LoopAbort
)

type RunTotals struct {
	Steps     int           `json:"steps"`
	ToolCalls int           `json:"tool_calls"`
	Usage     Usage         `json:"usage"`
	Cost      Micros        `json:"cost_micros"`
	Active    time.Duration `json:"active"`
}

type RunMeter interface {
	Step() error // *BudgetError
	// BeforeCall projects estInput + m.MaxOutput against run tokens, run cost and
	// the principal's, the chat share's and the global daily caps. Unknown
	// prices skip $ checks only.
	BeforeCall(ctx context.Context, estInput int, m ModelInfo) error
	// AfterCall records spend_daily and run totals; emits budget.warning at 80%.
	AfterCall(ctx context.Context, c CallInfo) error
	ObserveTool(tool string, argsHash, resultHash uint64) LoopVerdict
	ObserveText(hash uint64) LoopVerdict // identical assistant text 3 steps in a row => abort
	Pause()                              // approval wait: wall clock stops
	Resume()
	Deadline() time.Time
	Totals() RunTotals
	Remaining() RunLimits
}

// Budget bills by principal: a user principal's id is its user's, and a
// team-owned principal (channel or teammate) pays its own line.
type Budget interface {
	StartRun(run RunID, p PrincipalID, l RunLimits) RunMeter
	// CheckDaily fails with *BudgetError(daily_cost) when the principal's
	// own cap, the chat share (team-owned principals) or the global cap is
	// exhausted; used before accepting a run.
	CheckDaily(ctx context.Context, p PrincipalID) error
	// RecordSpend is for calls outside a run (passthrough, probes, notes): it
	// writes the llm_calls row (run_id NULL) and spend_daily.
	RecordSpend(ctx context.Context, p PrincipalID, c CallInfo) error
	AllowSender(key string) (ok bool, retryAfter time.Duration) // token bucket, LRU 10k keys
	Daily(ctx context.Context, p PrincipalID) (spent, limit Micros, err error)
	DefaultLimits(t Trigger) RunLimits
}
```

### 4.12 `auth.go`: actors, tokens, identities

```go
package core

import (
	"context"
	"time"
)

// Actor is the authenticated caller of an HTTP request.
type Actor struct {
	User    UserID   `json:"user_id"`
	Role    Role     `json:"role"`
	Via     string   `json:"via"` // cookie | token
	TokenID string   `json:"token_id,omitempty"`
	Scopes  []string `json:"scopes,omitempty"` // token scopes: chat | llm | read | admin; cookies have all of the role
	CSRF    string   `json:"-"`
	Viewer  Viewer   `json:"-"`
	// OnApproval and RatePerMin come from the token (cookie actors: "deny", 0).
	OnApproval string `json:"-"`
	RatePerMin int    `json:"-"`
	// MustChangePassword (cookie actors): until the one-time password is
	// replaced, only signing out and choosing a new password are allowed.
	MustChangePassword bool `json:"-"`
}

func (a Actor) HasScope(s string) bool {
	if a.Via == "cookie" {
		return s != "admin" || a.Role.IsAdmin()
	}
	for _, x := range a.Scopes {
		if x == s {
			return true
		}
	}
	return false
}

type TokenSpec struct {
	Name       string        `json:"name"`
	Scopes     []string      `json:"scopes"`
	TTL        time.Duration `json:"ttl"`         // default 90 days; 0 rejected
	OnApproval string        `json:"on_approval"` // deny (default) | wait
	RatePerMin int           `json:"rate_per_min"`
}

// IdentityResolution is what Bothy knows about a chat sender.
type IdentityResolution struct {
	Status     IdentityStatus // "" unknown | pending | teammate | blocked
	Principal  PrincipalID    // teammate: their person space
	Via        AdmittedVia
	CanApprove bool
	Member     MemberStatus
	MemberAt   time.Time
	SpaceOn    bool // teammate: their person space is enabled
	Lost       bool // set by Admit: this verdict made an admitted person an outsider
}

// Admitted: a teammate whose space is on and whose platform said member, or
// whom an admin let in where the platform can't tell.
func (r IdentityResolution) Admitted() bool {
	if r.Status != IdentityTeammate || !r.SpaceOn {
		return false
	}
	switch r.Member {
	case MemberYes:
		return true
	case MemberUnknown:
		return r.Via == ViaAdmin
	}
	return false
}

type Auth interface {
	NeedsSetup(ctx context.Context) (bool, error)
	// EnsureSetupToken creates the one-time token when no owner exists; written to
	// <data>/setup-token (0600) and printed once in the boot banner.
	EnsureSetupToken(ctx context.Context) (plain string, created bool, err error)
	RotateSetupToken(ctx context.Context) (string, error)
	ClaimSetup(ctx context.Context, token string) (grantCookie string, err error) // constant-time; 30 min grant
	CreateOwner(ctx context.Context, grantCookie, username, password, timezone string) (cookie string, a Actor, err error)
	CompleteSetup(ctx context.Context, a Actor) error // deletes token file; setup routes then 404

	Login(ctx context.Context, username, password, ip string) (cookie string, a Actor, err error)
	FromCookie(ctx context.Context, cookie string) (Actor, error)
	FromBearer(ctx context.Context, token string) (Actor, error)
	Logout(ctx context.Context, cookie string) error
	ViewerFor(ctx context.Context, u UserID) (Viewer, error) // own principal; Admin = role is owner/admin

	// Passwords (CreateOwner, CreateUser, SetPassword, ResetPassword) must be at
	// least auth.password_min_length and not in the embedded list of 10k common
	// passwords (internal/auth, server-side only) => ErrInvalid with a message.
	CreateUser(ctx context.Context, by Actor, username, password, displayName string, role Role) (*User, error)
	UpdateUser(ctx context.Context, by Actor, u *User) error // role/disabled/display name; disabling revokes sessions and tokens
	SetPassword(ctx context.Context, u UserID, old, new string) error
	// ResetPassword is for the offline CLI (`bothy user reset-password`): no old
	// password, revokes the user's sessions, audited auth.password_changed.
	ResetPassword(ctx context.Context, u UserID, new string) error
	CreateToken(ctx context.Context, by Actor, spec TokenSpec) (plain string, rec *TokenRecord, err error)
	RevokeToken(ctx context.Context, by Actor, id string) error

	// Admit records the platform's verdict on a sender (SPEC §5.1 step 3)
	// and makes a member who is pending a teammate, creating their person
	// space the first time (audited identity.teammate). An outsider keeps
	// their status and chats but loses Can approve. Blocked stays blocked.
	// It never moves last_seen: only the person's own messages do.
	Admit(ctx context.Context, ident ChannelIdentity, m Membership) (IdentityResolution, error)
	// NoticeSender reports whether to send ident a refusal or waiting line:
	// at most once per 24 h per identity (records last_notice_at).
	NoticeSender(ctx context.Context, ident ChannelIdentity) (bool, error)
	ResolveIdentity(ctx context.Context, channel, externalID string) (IdentityResolution, error)
}
```

v0.2 UI (docs/design/ui.md §17 S7, S9): `auth.Service` has three methods outside `core.Auth` that `httpapi` reaches by interface assertion. `CreateOwnerNamed` is `CreateOwner` with a display name (trimmed, at most 64 printable characters; empty means the username). `CreateUserOneTime` creates an account with a generated one-time password (four or more groups of four Crockford characters, 80+ bits, returned once) and `ResetUserPassword` gives another account a new one (the owner may reset anyone else, admins only members; a turned-off account → 400 *Turn the account on first. A turned-off account can't sign in.*; the account's sessions end; the owner's own password is reset only with `bothy user reset-password`). Both set `User.MustChangePassword`; cookie actors carry it as `Actor.MustChangePassword` (§8.1), and `SetPassword` then accepts an empty old password, refuses the one-time password itself as the new one, and clears the flag. Audit details add `one_time_password` (true when generated) on `user.create` and `one_time` on `auth.password_changed`. A password typed by the admin keeps the v0.1 behaviour (no flag).

Sign in with Google and GitHub (§15) adds more `auth.Service` extras outside `core.Auth`, reached the same way: `SetSignIn` (the vault and `egress.SystemClient`, set by `cmd/bothy` once they exist), `SignInOptions`, `StartSignIn`, `StartLink`, `FinishSignIn`, `Unlink`, `MySignIns`, `SignInStatus`, `CheckSignIn` and `PinGitHubOrgs`. An account made by provider sign-in stores `pw_hash = "!"`, which no PHC string parses as: password sign-in for it fails exactly like an unknown username, dummy derivation and lockout counting included, until an admin gives it a password. `auth.login` and `auth.login_failed` details carry `method` (`password`, `google`, `github`).

### 4.13 `agent.go`: the runner

```go
package core

import (
	"context"
	"time"
)

// RunSink receives a run's events in order (channel streamer, OpenAI SSE
// writer). OnEvent must not block for long; the runner drops nothing but the
// sink must buffer.
type RunSink interface{ OnEvent(e Event) }

type RunRequest struct {
	Session    SessionID
	Principal  PrincipalID
	User       UserID // the principal's account; "" for team-owned principals
	Trigger    Trigger
	Schedule   ScheduleID
	Slot       Slot   // "" = the session's ModelSlot (schedules and /v1 bothy-fast set it)
	Input      []Part // attachments arrive as image/file parts whose Blob is already in the workspace
	Provenance Provenance
	Sender     string   // "<chat app>:<external id>" of a chat sender; "web:…" / "api:…" otherwise
	SenderName string   // the chat sender's display name, for the turn context
	Chat       *ChatRef // originating chat (message tool target, card routing)
	Sink       RunSink  // may be nil (web runs use the bus)
	Limits     *RunLimits
	// Instructions are /v1 client "system" messages, rendered as untrusted
	// "client instructions" inside the turn-context; never part of S1/S2.
	Instructions string
	// API runs: OnApproval "deny" (default) or "wait" (up to ApprovalWait).
	OnApproval   string
	ApprovalWait time.Duration
	// Ephemeral carries client history for stateless /v1 requests. It is
	// appended to the hidden api session before Input.
	Ephemeral   []Message
	MaxOutput   int      // /v1 max_tokens cap per step; 0 = model default
	Temperature *float64 // /v1 client temperature; nil = model default
	Stop        []string // /v1 stop sequences
	// Refine asks for refine mode on this turn's final answer; nil follows
	// the session's setting, then agent.refine.enabled (message and api
	// triggers only).
	Refine *bool
}

type SubmitResult struct {
	Run     RunID `json:"run_id,omitempty"`
	Queued  bool  `json:"queued"`  // waiting for the session's active run
	Steered bool  `json:"steered"` // injected into the active run at the next step
}

type RunInfo struct {
	Run       RunID       `json:"run_id"`
	Session   SessionID   `json:"session_id"`
	Principal PrincipalID `json:"principal_id"`
	Status    RunStatus   `json:"status"`
	Started   time.Time   `json:"started_at"`
	Queued    int         `json:"queued"`
}

type Runner interface {
	// Submit returns ErrBusy when the session mailbox is full and ErrPaused when
	// the kill switch is engaged. A ChatAPI (/v1) request is never queued
	// behind other work: while the session has a run it gets ErrBusy, since
	// its caller streams only the run it starts.
	Submit(ctx context.Context, r RunRequest) (SubmitResult, error)
	// Stop cancels the active run, SIGKILLs the session box, cancels its pending
	// approvals and clears its queue. It never waits on the run goroutine. A
	// chat caller passes by "" and its actor through WithAuditOrigin.
	Stop(ctx context.Context, v Viewer, s SessionID, by UserID) (bool, error)
	StopAll(ctx context.Context, by UserID) (int, error) // by "" from chat, as Stop
	// StopPrincipal stops every active run of p and cancels its pending and
	// parked approvals, which ends parked runs through the cancelled path
	// (blocking someone, losing membership, turning a channel off). The
	// actor comes from WithAuditOrigin. It returns how many runs it ended.
	StopPrincipal(ctx context.Context, p PrincipalID) (int, error)
	// SetPaused persists meta.paused, publishes the banner and audits; it stops
	// nothing. Callers run StopAll and report its count (§5.11). A chat
	// approver pauses with by "" and its actor through WithAuditOrigin.
	SetPaused(ctx context.Context, paused bool, by UserID) error
	Paused() bool
	// PausedBy is who engaged the kill switch and when: a user id or a chat
	// actor ("channel:<app>:<id>"); zero while it is off, and for a pause
	// recorded before who and when were kept.
	PausedBy() (by string, at time.Time)
	// Resume continues a parked run after its approval resolved: allowed =>
	// execute the stored Action exactly once and continue the loop; denied =>
	// record the denial and continue the loop; expired/cancelled => run stopped
	// with a notice. It is a no-op when the approval's run is not parked.
	Resume(ctx context.Context, a *Approval) error
	Active() []RunInfo
	ResolveSession(ctx context.Context, p PrincipalID, chat ChatRef) (*Session, error) // open session by ChatRef.Key()
	NewSession(ctx context.Context, p PrincipalID, origin string, chat *ChatRef) (*Session, error)
	// Fork creates the clean fork of §5.3: no history, taint 0, a system note
	// linking the old session. For a channel session it archives the old one and
	// moves the channel key to the fork, so the chat continues in the fork.
	Fork(ctx context.Context, v Viewer, s SessionID, by UserID) (*Session, error)
	Compact(ctx context.Context, v Viewer, s SessionID) error
	ContextPreview(ctx context.Context, v Viewer, s SessionID) (*Breakdown, error)
	// Continue is "+10 steps" after a steps budget stop. The new run is
	// message-triggered but keeps the provenance of the run it continues, so
	// F6 still applies when an untrusted message started that run.
	Continue(ctx context.Context, v Viewer, run RunID, by UserID) (SubmitResult, error)
	Constitution() (version, text string) // embedded CONSTITUTION.md (full variant) for GET /api/constitution
}
```

### 4.14 `services.go`: memory, skills, MCP, scheduler, audit, doctor

```go
package core

import (
	"context"
	"io"
	"time"
)

// ---- memory ----

type MemoryOp struct {
	File MemoryFile `json:"file"`
	Op   string     `json:"op"`            // add | replace | remove
	Old  string     `json:"old,omitempty"` // exact existing entry text (replace/remove)
	New  string     `json:"new,omitempty"`
}

type MemorySnapshot struct {
	Memory  string `json:"memory"`
	User    string `json:"user"`
	LastID  int64  `json:"last_id"` // last memory_events id folded in
	Private bool   `json:"private"` // non-empty => session starts with TaintPrivate
}

// Memory files live in <data>/memory/<principal>/{MEMORY,USER,HEARTBEAT}.md.
// Caps: memory.memory_max_bytes, user_max_bytes, heartbeat_max_bytes. Space
// principals have no memory: Snapshot returns an empty snapshot and
// Apply/Read/Write/History return ErrForbidden for them.
//
// TEAM.md, remembered for everyone, is one file at <data>/memory/team/TEAM.md
// (cap memory.team_max_bytes) that every run's S1 carries, whatever the
// principal. Only an admin's edit (WriteTeam) or a memory tool call with file
// TEAM that an admin approved changes it; Apply refuses it.
type Memory interface {
	ToolProvider // memory, session_search
	Snapshot(ctx context.Context, p PrincipalID) (MemorySnapshot, error)
	// Apply enforces byte caps (error text tells the model the remaining bytes),
	// writes atomically (temp+rename, 0600), records memory_events, audits.
	Apply(ctx context.Context, p PrincipalID, run RunID, op MemoryOp, prov Provenance) (*MemoryEvent, error)
	Read(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile) (content, etag string, err error)
	Write(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, content, etag string, by UserID) (string, error) // ErrConflict on etag mismatch
	DeltasSince(ctx context.Context, p PrincipalID, afterID int64) (text string, lastID int64, err error)
	History(ctx context.Context, v Viewer, p PrincipalID, f MemoryFile, page Page) ([]*MemoryEvent, error)
	ReadTeam(ctx context.Context) (content, etag string, err error)
	// WriteTeam: ErrForbidden unless by is an enabled admin; ErrConflict on
	// etag mismatch. Audited memory.edit with scope team.
	WriteTeam(ctx context.Context, content, etag string, by UserID) (string, error)
	TeamHistory(ctx context.Context, page Page) ([]*MemoryEvent, error) // newest first
	Search(ctx context.Context, v Viewer, query string, limit int) ([]SearchHit, error)
	// Purge deletes every memory file of a principal (DELETE /api/users/{id}?purge=true);
	// its memory_events rows go with the principal (ON DELETE CASCADE).
	Purge(ctx context.Context, p PrincipalID) error
}

// ---- skills ----

type Skills interface {
	ToolProvider                           // skill_load, skill_read
	Index(ctx context.Context) []SkillMeta // enabled and hash-matching only, by name
	Load(ctx context.Context, name string) (body string, files []string, err error)
	ReadFile(ctx context.Context, name, rel string) ([]byte, error) // os.Root confined, 256KB cap
	List(ctx context.Context) ([]*SkillMeta, error)                 // all, for admins
	// SetEnabled with on approves the last scanned tree; hash, when non-empty,
	// must equal the SkillMeta.Hash the admin reviewed, otherwise ErrConflict.
	SetEnabled(ctx context.Context, name string, on bool, hash string, by UserID) error
	Install(ctx context.Context, zip io.ReaderAt, size int64, by UserID) (*SkillMeta, error) // status pending; never executes anything
	Remove(ctx context.Context, name string, by UserID) error
	Rescan(ctx context.Context) error // hash drift => status changed
}

// ---- MCP ----

type MCPStatus struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"` // stdio | http
	State     string    `json:"state"`     // disabled | installing | connecting | ready | error | no_sandbox | pins_pending
	Protocol  string    `json:"protocol"`  // negotiated spec revision
	Err       string    `json:"error,omitempty"`
	Tools     int       `json:"tools"`
	Pending   int       `json:"pending"`
	Since     time.Time `json:"since"`
}

type MCPToolApproval struct {
	SideEffect SideEffect        `json:"side_effect"`
	FieldMap   map[string]string `json:"field_map,omitempty"`
	Private    bool              `json:"private"`
	Disable    bool              `json:"disable,omitempty"` // true => pin status disabled (MCPRepo.DisableTool); other fields ignored
	// Hash is the definition hash the admin reviewed. When set, Approve fails
	// with ErrConflict unless it is still the tool's latest pin.
	Hash string `json:"hash,omitempty"`
}

type MCP interface {
	// Start connects enabled servers and registers approved, hash-matching
	// tools. Discovery has no billing user: HTTP header secret refs resolve in
	// the global vault scope only; one without a global value is left out of
	// the discovery session (§2.11).
	Start(ctx context.Context) error
	Status() []MCPStatus
	// Pins: per tool the server offers now, the pin of the definition it
	// offers (an older one after a rollback; the newest per tool before the
	// first discovery), plus the approved pin beside a pending one for diffs.
	Pins(ctx context.Context, server string) ([]*MCPPin, error)
	Approve(ctx context.Context, server string, tools map[string]MCPToolApproval, by UserID) error
	Install(ctx context.Context, server string, by UserID) error // stdio only, installer box
	Reload(ctx context.Context, server string) error
	Close() error
}

// ---- scheduler ----

type Scheduler interface {
	ToolProvider // schedule_list, schedule_create, schedule_delete (Triggers: message only)
	// Run fires due schedules and keeps one heartbeat schedule (name
	// "heartbeat", kind heartbeat, created_by "system") for the owner's principal
	// in sync with scheduler.heartbeat (hot reload enables/disables it).
	Run(ctx context.Context) error
	Upsert(ctx context.Context, v Viewer, s *Schedule, by string) error // validates spec+tz (empty tz = v.User's own), computes NextRunAt, wakes loop
	Delete(ctx context.Context, v Viewer, id ScheduleID, by UserID) error
	RunNow(ctx context.Context, v Viewer, id ScheduleID, by UserID) (RunID, error)
	Preview(kind ScheduleKind, spec, tz string, n int) ([]time.Time, error)
	// Describe renders a spec in plain English for dto.SchedulePreviewResp.Human,
	// e.g. "Weekdays at 08:00 (Europe/London)".
	Describe(kind ScheduleKind, spec, tz string) (string, error)
	// SetPaused is a scheduler-only pause, independent of the kill switch:
	// the scheduler stops firing while Runner.Paused() is true whatever this
	// says, and nothing in the gateway calls it.
	SetPaused(paused bool)
}

// ---- audit ----

type Auditor interface {
	// Record redacts Detail, chains sha256(prev_hash || canonical row) and appends.
	// auth.*, config.*, approval.* and secret.* are written synchronously; others
	// are batched (flush <= 250ms) and flushed on shutdown.
	Record(ctx context.Context, e AuditEntry) error
	Verify(ctx context.Context) (badID int64, err error) // 0 = chain intact
	Flush(ctx context.Context) error
}

// ---- doctor ----

type Severity string

const (
	SevInfo     Severity = "info"
	SevLow      Severity = "low"
	SevMedium   Severity = "medium"
	SevHigh     Severity = "high"
	SevCritical Severity = "critical"
)

type Finding struct {
	ID       string   `json:"id"` // stable: "vault.kek_colocated", "sandbox.unavailable"
	Area     string   `json:"area"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Fix      string   `json:"fix,omitempty"` // copyable command or UI route
	Severity Severity `json:"severity"`
	Security bool     `json:"security"` // included in security-audit
	AutoFix  bool     `json:"auto_fix"`
}

type Check interface {
	ID() string
	Run(ctx context.Context) []Finding
	Fix(ctx context.Context, f Finding) error // ErrNotImplemented when !AutoFix
}

type CheckProvider interface{ Checks() []Check }
```

### 4.15 `links.go`: link neutralization

Pure helpers, implemented in the skeleton's package and unit- and fuzz-tested there (`links_test.go`). They are the only link neutralizer: the channel router (M9) and the agent's run sink (M3, which also feeds `/v1`) both call them instead of keeping copies, so the surfaces cannot drift apart (§5.3, threat row 7). Only the exported API is listed; the implementation is in `internal/core/links.go`.

```go
// LooksLikeLink reports whether a chat client could turn part of s into a
// link: a "scheme://" URL, a bare host name or an IPv4 address.
func LooksLikeLink(s string) bool

// NeutralizeLinks defangs every link in s, code included
// ("https://evil.com/x" becomes "hxxps://evil[.]com/x"), so nothing in it is
// clickable. It is idempotent.
func NeutralizeLinks(s string) string

// NeutralizeMarkdown defangs the links in md's prose and leaves fenced code
// blocks and inline code spans untouched, so code keeps its dots
// ("os.path").
func NeutralizeMarkdown(md string) string

// NewLinkFilter returns NeutralizeMarkdown for a stream: each call takes the
// next chunk and returns it neutralized. Chunks must end at whitespace so no
// link is split; the fenced-block state and the start of the current line
// carry over from one chunk to the next.
func NewLinkFilter() func(chunk string) string
```

What counts as a link: markdown links and autolinks (a nested autolink such as `<a:<b:c>>` is broken too), every `scheme://` whatever follows it (a URL with a bracketed IPv6 host such as `https://[2001:db8::1]/x` included), bare host names (internationalized names and the dot look-alikes that IDNA or NFKC map to `.`), IPv4 addresses, and links that only form once markdown is rendered (backslash escapes, emphasis around a dot, invisible format characters). `http`/`https`/`ftp` become `hxxp`/`hxxps`/`fxp` with dots as `[.]`; other schemes (`tg://`, `intent://`) get `[:]//`; a URL nested in a query is defanged too.


### 4.16 `refine.go`: refine mode traces (v0.2 F3)

The refine engine's types (§14.2). A refined answer stores its trace in a `PartRefine` part, which history drops so it never reaches a model; `run.refine` events go to the bus only.

```go
package core

// Refine mode (v0.2 F3): a final answer is drafted N times and each draft is
// taken through D rounds of continue → critique → correct before a plurality
// vote over the normalized final answers picks the reply.

// EvRunRefine reports refinement progress (bus only; sinks never get it).
const EvRunRefine EventType = "run.refine"

// RefineStatusDetail is the run.status detail (status "thinking") while a
// final answer is being refined; sinks see it, since run.refine is bus only.
const RefineStatusDetail = "Refining the answer"

// PartRefine is the trace of the refinement that produced an assistant
// message. It is stored with the message and never sent to a model.
const PartRefine PartType = "refine"

type RefineRole string

const (
	RefineDraft    RefineRole = "draft"
	RefineContinue RefineRole = "continue"
	RefineCritique RefineRole = "critique"
	RefineCorrect  RefineRole = "correct"
)

// A session's own refine setting is the meta value RefineMetaKey(session):
// RefineOn or RefineOff. Without one the session follows agent.refine.enabled.
const (
	RefineOn  = "on"
	RefineOff = "off"
)

func RefineMetaKey(s SessionID) string { return "agent.refine." + string(s) }

// RefineEntry is one model call of a refinement.
type RefineEntry struct {
	Draft     int        `json:"draft"` // 0-based; draft 0 is the run's own answer step
	Round     int        `json:"round"` // 0 for drafts, 1..depth for the rounds
	Role      RefineRole `json:"role"`
	Text      string     `json:"text"`             // redacted; long texts keep their head and tail
	Answer    string     `json:"answer,omitempty"` // normalized final answer (draft, continue, correct)
	Confirmed bool       `json:"confirmed,omitempty"`
	Abridged  bool       `json:"abridged,omitempty"` // the quoted draft was shortened to fit the model's window
	CallID    string     `json:"call_id,omitempty"`  // llm_calls id
	Model     string     `json:"model,omitempty"`
	Cost      Micros     `json:"cost_micros"`
	Error     string     `json:"error,omitempty"`
}

type RefineVote struct {
	Answer string `json:"answer"` // normalized
	Count  int    `json:"count"`
	Drafts []int  `json:"drafts"` // ascending
}

type RefineTrace struct {
	Drafts    int  `json:"drafts"` // configured breadth N
	Depth     int  `json:"depth"`  // configured depth D
	Rounds    int  `json:"rounds"` // rounds completed
	EarlyStop bool `json:"early_stop"`
	// Fallback says why refinement ended before its last round, showing the
	// best answer so far: budget | time | new_message | stopped | failed.
	Fallback string        `json:"fallback,omitempty"`
	Winner   int           `json:"winner"`           // draft shown as the reply
	Answer   string        `json:"answer,omitempty"` // the winner's normalized final answer
	Votes    []RefineVote  `json:"votes"`            // most votes first, ties by earliest draft
	Entries  []RefineEntry `json:"entries,omitempty"`
	Calls    int           `json:"calls"` // model calls made for this answer, the first draft included
	Cost     Micros        `json:"cost_micros"`
}

// RefineData is the payload of run.refine: phase "start" (Drafts, Depth,
// MaxCalls), "entry" (one finished call) and "done" (the trace without its
// entries; the persisted message carries all of it).
type RefineData struct {
	Phase    string       `json:"phase"`
	Drafts   int          `json:"drafts,omitempty"`
	Depth    int          `json:"depth,omitempty"`
	MaxCalls int          `json:"max_calls,omitempty"`
	Entry    *RefineEntry `json:"entry,omitempty"`
	Trace    *RefineTrace `json:"trace,omitempty"`
}

func (RefineData) eventData() {}

// RefineMaxCalls bounds the model calls of one refined answer: every draft
// makes one call and up to three per round.
func RefineMaxCalls(drafts, depth int) int { return drafts * (1 + 3*depth) }
```

### 4.17 `github.go`: GitHub connections and repositories (v0.2 F4)

`GitHubStore` stays outside `Store` (the SQLite store's `DB.GitHub()`, `memstore.NewGitHub()`, checked by `conformance.GitHubStore`). `EvRepoCheckout` is the single checkout event: the knowledge module indexes it (§14.3, §14.4). `GitHubAppMaker` is not part of `GitHub`: the HTTP API finds it by a type assertion on `Deps.GitHub`, as it does for the auth extras. The app record is `GitHubOwnApp` because `GitHubApp` already names the App auth kind.

```go
package core

import (
	"context"
	"errors"
	"time"
)

// GitHub connections and repositories (v0.2 F4). A connection's personal
// access token or GitHub App private key lives in the vault; the rows below
// hold only its "secret:<name>" reference, and only the gateway resolves it.

const PrefixGitHubConn = "ghc"

type GitHubAuth string

const (
	GitHubPAT GitHubAuth = "pat" // fine-grained (or classic) personal access token
	GitHubApp GitHubAuth = "app" // GitHub App: installation tokens minted on demand
)

type GitHubConnection struct {
	ID             string     `json:"id"` // ghc_<26>
	Auth           GitHubAuth `json:"auth"`
	Account        string     `json:"account"`      // the token's user, or the installation's account
	AccountType    string     `json:"account_type"` // User | Organization
	AppID          string     `json:"app_id,omitempty"`
	AppSlug        string     `json:"app_slug,omitempty"`
	InstallationID int64      `json:"installation_id,omitempty"`
	SecretRef      string     `json:"secret_ref"`  // "secret:<name>" of the PAT or the App private key
	OwnsSecret     bool       `json:"owns_secret"` // stored by Connect, so Disconnect deletes it
	Status         string     `json:"status"`      // ok | error
	Error          string     `json:"error,omitempty"`
	TokenExpires   time.Time  `json:"token_expires_at"` // PAT expiry as GitHub reports it; zero = none
	CheckedAt      time.Time  `json:"checked_at"`
	CreatedBy      UserID     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GitHubRepo is a repository a connection can reach. Only Allowed ones are
// offered to the agent. Owner and name are unique case-insensitively across
// connections and keep GitHub's spelling. LastIndexedSHA is never stored:
// indexes are per principal, so GET /api/github/repos fills in the commit
// the caller's own index of the repository last read (IndexedRepo.IndexedSHA).
type GitHubRepo struct {
	Connection     string    `json:"connection_id"`
	GitHubID       int64     `json:"github_id"`
	Owner          string    `json:"owner"`
	Name           string    `json:"name"`
	DefaultBranch  string    `json:"default_branch"`
	Private        bool      `json:"private"`
	Allowed        bool      `json:"allowed"`
	LastIndexedSHA string    `json:"last_indexed_sha,omitempty"` // the caller's own index state
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (r GitHubRepo) FullName() string { return r.Owner + "/" + r.Name }

// GitHubStore persists connections, repositories (migration 0002) and
// Bothy-made apps (0012). It is not part of Store: *store.DB and memstore
// provide it through GitHub().
type GitHubStore interface {
	PutConnection(ctx context.Context, c *GitHubConnection) error         // insert or replace by ID
	Connection(ctx context.Context, id string) (*GitHubConnection, error) // ErrNotFound
	Connections(ctx context.Context) ([]*GitHubConnection, error)         // oldest first
	// DeleteConnection deletes its repos and clears the connection of the app
	// that held it; ErrNotFound.
	DeleteConnection(ctx context.Context, id string) error
	// PutRepo inserts or updates by owner/name (case-insensitive). A repo
	// another connection allows is ErrConflict (one it no longer allows moves
	// over, keeping created_at); an unknown connection ErrInvalid.
	PutRepo(ctx context.Context, r *GitHubRepo) error
	Repo(ctx context.Context, owner, name string) (*GitHubRepo, error)   // case-insensitive; ErrNotFound
	Repos(ctx context.Context, connection string) ([]*GitHubRepo, error) // "" = every connection; by owner, name
	// PutApp inserts or replaces by ID, keeping created_at and created_by.
	// An app_id or connection another app holds is ErrConflict; an unknown
	// connection ErrInvalid. The derived fields are never stored.
	PutApp(ctx context.Context, a *GitHubOwnApp) error
	App(ctx context.Context, id string) (*GitHubOwnApp, error)                 // ErrNotFound
	Apps(ctx context.Context) ([]*GitHubOwnApp, error)                         // oldest first
	AppByConnection(ctx context.Context, connID string) (*GitHubOwnApp, error) // ErrNotFound
	DeleteApp(ctx context.Context, id string) error                            // ErrNotFound
}

const PrefixGitHubApp = "gha"

// GitHubOwnApp is a GitHub App Bothy created from a manifest (migration 0012).
// Its private key and client secret live in the global vault scope; the row
// holds their references. Pending until installed, connected while a
// connection uses it, retired once that connection is gone or the admin
// started over (secrets deleted; kept so Settings can say where to delete it
// on GitHub, which Bothy never does itself).
type GitHubOwnApp struct {
	ID              string    `json:"id"` // gha_<26>
	AppID           int64     `json:"app_id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Owner           string    `json:"owner"`
	OwnerID         int64     `json:"owner_id"`   // GitHub's id of the owner account, which a rename keeps and a transfer changes (GET /app); installations must be on it
	OwnerType       string    `json:"owner_type"` // Organization | User
	URL             string    `json:"html_url"`   // <web> + GitHub's path for it: /apps/<slug>, or /github-apps/<slug> on Enterprise Server
	ClientID        string    `json:"client_id"`
	KeyRef          string    `json:"key_ref"`           // "secret:github_<id>_key"; "" once retired
	ClientSecretRef string    `json:"client_secret_ref"` // "secret:github_<id>_client_secret"; kept for a possible GitHub sign-in, unused today
	Connection      string    `json:"connection_id,omitempty"`
	Replaces        string    `json:"replaces,omitempty"` // connection swapped out once the app is installed
	RetiredAt       time.Time `json:"retired_at"`
	CreatedBy       UserID    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Filled in by the GitHub service from the state and URL; never stored.
	State       string `json:"state"`        // pending | connected | retired
	InstallURL  string `json:"install_url"`  // <URL>/installations/new
	SettingsURL string `json:"settings_url"` // <web>/organizations/<owner>/settings/apps/<slug> or <web>/settings/apps/<slug>
}

// GitHubAppTarget is where a manifest flow creates the app.
type GitHubAppTarget struct {
	Org      string // organization login; "" = the admin's personal account
	Name     string // "" = "Bothy (<Org, or Fallback>)", cut to GitHub's 34 characters; "… 2" and on while an app Bothy recorded has it
	Fallback string // the admin's username, naming a personal account's app
}

// GitHubAppHandoff is what the handoff page posts to GitHub.
type GitHubAppHandoff struct {
	Action    string // https://<web>/organizations/<org>/settings/apps/new or …/settings/apps/new, without query
	WebOrigin string // scheme://host[:port] of Action, for the page's form-action
	Manifest  string // JSON for the form field "manifest"
	Name      string // the proposed app name
	Org       string // "" for a personal account
}

// ErrAppExchange marks a CreateApp failure on GitHub's side: the code was
// refused or had expired, GitHub failed or was out of reach, or its answer
// held no app Bothy can use. CreateApp's other failures once the code is
// spent are Bothy's own (vault, store).
var ErrAppExchange = errors.New("redeeming the new app's code")

// GitHubAppMaker is the extra *github.Service implements: Bothy's own
// GitHub App created from a manifest and connected through its installation
// (§14.3). Every method needs an admin caller.
type GitHubAppMaker interface {
	// AppManifest builds the manifest for an app reached back at origin
	// (Bothy's own address); ErrInvalid for a bad target or origin.
	AppManifest(ctx context.Context, t GitHubAppTarget, origin string) (*GitHubAppHandoff, error)
	// CreateApp redeems the code GitHub's redirect carried and keeps the new
	// app's keys. ErrSealed before redeeming while the vault is locked;
	// ErrAppExchange when GitHub's side failed.
	CreateApp(ctx context.Context, code string, t GitHubAppTarget, replaces string, by UserID) (*GitHubOwnApp, error)
	// InstallApp verifies installationID belongs to the app and connects it.
	InstallApp(ctx context.Context, id string, installationID int64, by UserID) (*GitHubConnection, error)
	// CheckApp looks for the app's installation on its owner and connects
	// it; nil, nil when it is not installed there yet.
	CheckApp(ctx context.Context, id string, by UserID) (*GitHubConnection, error)
	Apps(ctx context.Context) ([]*GitHubOwnApp, error)                          // oldest first
	ConnectionApp(ctx context.Context, connID string) (*GitHubOwnApp, error)    // ErrNotFound
	RetireApp(ctx context.Context, id string, by UserID) (*GitHubOwnApp, error) // ErrConflict while connected
	DeleteApp(ctx context.Context, id string, by UserID) error                  // ErrConflict unless retired
}

// GitHubCredentials is a connect or test request. Exactly one of Token,
// PrivateKey or SecretRef carries the secret.
type GitHubCredentials struct {
	Auth           GitHubAuth
	Token          Secret // PAT; Connect stores it in the global vault scope
	PrivateKey     Secret // App private key (PEM); Connect stores it in the global vault scope
	SecretRef      string // an existing "secret:<name>" instead (e.g. BOTHY_SECRET_<NAME>)
	AppID          string
	InstallationID int64 // App: 0 picks the app's only installation
}

type GitHubInstallation struct {
	ID                  int64  `json:"id"`
	Account             string `json:"account"`
	AccountType         string `json:"account_type"`
	RepositorySelection string `json:"repository_selection"` // all | selected
}

type GitHubRepoAccess struct {
	GitHubID      int64  `json:"github_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description,omitempty"` // repository-authored text
	Allowed       bool   `json:"allowed"`
	Connection    string `json:"connection_id,omitempty"` // the connection that holds it, if any
}

type GitHubRateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Resource  string    `json:"resource,omitempty"`
}

// GitHubProbe is what a credential test found. OK is false with Error set
// when GitHub refused the credentials or could not be reached.
type GitHubProbe struct {
	OK             bool                 `json:"ok"`
	Error          string               `json:"error,omitempty"`
	Code           FailureCode          `json:"code,omitempty"` // why it failed, next to Error
	Auth           GitHubAuth           `json:"auth"`
	Account        string               `json:"account"`
	AccountType    string               `json:"account_type"`
	AppSlug        string               `json:"app_slug,omitempty"`
	InstallationID int64                `json:"installation_id,omitempty"`
	Installations  []GitHubInstallation `json:"installations,omitempty"` // App: set when one must be chosen
	Repos          []GitHubRepoAccess   `json:"repos"`
	Truncated      bool                 `json:"truncated"` // more repositories than were listed
	TokenExpires   time.Time            `json:"token_expires_at"`
	RateLimit      GitHubRateLimit      `json:"rate_limit"`
}

// GitHub manages connections for the HTTP API and contributes the repo_* and
// github_* tools. Every method needs an admin caller except Repos.
type GitHub interface {
	ToolProvider
	// Test checks credentials without storing anything. Only malformed input
	// is an error (ErrInvalid); GitHub's answer goes into the probe.
	Test(ctx context.Context, c GitHubCredentials) (*GitHubProbe, error)
	// Connect tests, stores the secret in the vault and allowlists repos
	// (each must be reachable through the credentials).
	Connect(ctx context.Context, c GitHubCredentials, repos []string, by UserID) (*GitHubConnection, error)
	Connections(ctx context.Context) ([]*GitHubConnection, error)
	Connection(ctx context.Context, id string) (*GitHubConnection, []*GitHubRepo, error)
	// Probe re-tests a stored connection, records its status and lists the
	// repositories it reaches with their allowlist state.
	Probe(ctx context.Context, id string) (*GitHubProbe, error)
	// SetRepos replaces the connection's allowlist.
	SetRepos(ctx context.Context, id string, repos []string, by UserID) ([]*GitHubRepo, error)
	// Disconnect deletes the connection, its repositories and a secret it owns.
	Disconnect(ctx context.Context, id string, by UserID) error
	Repos(ctx context.Context) ([]*GitHubRepo, error) // allowed repositories of every connection
	// Workflows lists the GitHub Actions workflows of an allowed repository
	// (ErrNotFound for any other), for picking a deploy workflow.
	Workflows(ctx context.Context, repo string) ([]GitHubWorkflow, error)
	// BranchHead is the commit the default branch of an allowed repository
	// points at now (ErrNotFound for any other).
	BranchHead(ctx context.Context, repo string) (string, error)
	// Prefetch checks allowed repositories out into env's workspace on their
	// default branch, skipping any already checked out there, so Knowledge
	// reads them (EvRepoCheckout) without waiting for a chat to need them.
	// Failures are logged and audited, not returned.
	Prefetch(ctx context.Context, env *ToolEnv, repos []string)
}

// GitHubWorkflow is one workflow file of a repository. Name is written by
// whoever can push to it.
type GitHubWorkflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`  // .github/workflows/deploy.yml
	State string `json:"state"` // active | disabled_manually | disabled_inactivity | …
}

// EvRepoCheckout is published (Principal = the workspace's principal;
// Session and Run name the run that asked, when one did) when repo_checkout
// has written a repository into a workspace. The knowledge module indexes it.
const EvRepoCheckout EventType = "repo.checkout"

// RepoCheckoutData: Dir holds Repo's files at commit SHA. Consumers also
// accept an absolute path inside the principal's workspace or the path boxes
// see (/workspace/...).
type RepoCheckoutData struct {
	Repo      string      `json:"repo"` // owner/name
	Ref       string      `json:"ref"`
	SHA       string      `json:"sha"`
	Workspace WorkspaceID `json:"workspace_id"`
	Dir       string      `json:"dir"` // workspace-relative: repos/<owner>/<name>
	Files     int         `json:"files"`
}

func (RepoCheckoutData) eventData() {}
```

### 4.18 `knowledge.go`: repository index and project notes (v0.2 F6)

`KnowledgeRepo` is not part of `Store`; the store exposes it as `(*store.DB).Knowledge()` and `knowledgetest` implements it in memory, both checked by `conformance.Knowledge` (§14.4).

```go
package core

import (
	"context"
	"time"
)

// ---- knowledge (v0.2 F6): repository index and project notes ----

// EvKnowledgeUpdated reports an index or notes state change of one repo. The
// knowledge module indexes the checkouts EvRepoCheckout announces.
const EvKnowledgeUpdated EventType = "knowledge.updated"

type KnowledgeUpdatedData struct {
	Repo    IndexedRepo `json:"repo"`
	Deleted bool        `json:"deleted,omitempty"`
}

func (KnowledgeUpdatedData) eventData() {}

// Sources of an indexed directory.
const (
	KnowledgeGitHub    = "github"    // a repository checkout (repo.checkout)
	KnowledgeWorkspace = "workspace" // any directory of the principal's workspace
)

// IndexedRepo.Status values.
const (
	IndexQueued  = "queued"
	IndexRunning = "indexing"
	IndexReady   = "ready"
	IndexFailed  = "error"
)

// NotesState.Status values.
const (
	NotesNone     = "none"
	NotesDrafting = "drafting"
	NotesReady    = "ready"
	NotesFailed   = "error"
)

// IndexedRepo is one indexed directory of a principal's workspace, named like
// a repository. Its project notes live outside every repository, in
// <data>/knowledge/<principal>/<owner>/<repo>/NOTES.md.
type IndexedRepo struct {
	ID        int64       `json:"id"`
	Principal PrincipalID `json:"principal_id"`
	Repo      string      `json:"repo"`   // "owner/name", lowercase
	Source    string      `json:"source"` // github | workspace
	Dir       string      `json:"dir"`    // workspace-relative directory of the indexed copy
	// SHA is the commit of the latest requested index ("" for a plain
	// directory); chunks carry the commit they were indexed at.
	SHA       string     `json:"sha"`
	Status    string     `json:"status"` // queued | indexing | ready | error
	Error     string     `json:"error,omitempty"`
	Files     int        `json:"files"`
	Chunks    int        `json:"chunks"`
	Bytes     int64      `json:"bytes"`
	Skipped   int        `json:"skipped"`   // text files left out by a size or count cap
	Truncated bool       `json:"truncated"` // a cap stopped the walk before the end
	IndexedAt time.Time  `json:"indexed_at"`
	Notes     NotesState `json:"notes"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// IndexedSHA is the commit the latest finished index read ("" until one
	// finished); UpdateIndex writes it.
	IndexedSHA string `json:"indexed_sha"`

	// Filled on read, never stored. Progress counts the files read so far
	// while the repo is queued or indexing; Summary is the first sentence
	// of the notes as plain text ("" without notes); Stale says the
	// repository's default branch has moved past IndexedSHA on GitHub.
	Progress *IndexProgress `json:"progress,omitempty"`
	Summary  string         `json:"summary"`
	Stale    bool           `json:"stale,omitempty"`
}

// IndexProgress is how far a running index got. Total is an estimate: the
// previous index's file count, else a count of the directory's files.
type IndexProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type NotesState struct {
	Status string `json:"status"` // none | drafting | ready | error
	Error  string `json:"error,omitempty"`
	// Untrusted: part of the notes came from repository content or from an
	// agent run holding untrusted content, and no user has saved them since.
	Untrusted bool      `json:"untrusted"`
	UpdatedAt time.Time `json:"updated_at"`
}

// KnowledgeChunk is one indexed slice of a file: a markdown section, a
// top-level declaration or a window of plain text.
type KnowledgeChunk struct {
	ID        int64  `json:"id"`
	SHA       string `json:"sha"`
	Path      string `json:"path"` // slash-separated, relative to IndexedRepo.Dir
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Kind      string `json:"kind"` // heading | func | method | type | class | header | text | …
	Lang      string `json:"lang"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Terms     string `json:"-"` // extra search words (identifier parts); indexed, never shown
}

type KnowledgeFile struct {
	Path   string `json:"path"`
	Hash   string `json:"hash"` // chunker version + sha256 of the content
	Size   int64  `json:"size"`
	Lang   string `json:"lang"`
	Chunks int    `json:"chunks"`
}

// KnowledgeFileChange replaces one file's chunks (Chunks may be empty) or,
// with Delete, removes the file from the index.
type KnowledgeFileChange struct {
	File   KnowledgeFile
	Chunks []KnowledgeChunk
	Delete bool
}

type KnowledgeQuery struct {
	Text      string      `json:"text"`
	Principal PrincipalID `json:"principal_id,omitempty"` // "" = every principal of the Viewer
	Repo      int64       `json:"repo_id,omitempty"`      // 0 = every repo
	Limit     int         `json:"limit,omitempty"`        // 0 = 8, at most 50
}

// KnowledgeHit is a ranked search result. KnowledgeRepo.Search returns the
// whole chunk in Snippet; core.Knowledge narrows it to the lines around the
// match and sets Citation ("owner/repo@sha:path#L10-L42").
type KnowledgeHit struct {
	RepoID    int64       `json:"repo_id"`
	Repo      string      `json:"repo"`
	Principal PrincipalID `json:"principal_id"`
	SHA       string      `json:"sha"`
	Path      string      `json:"path"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Kind      string      `json:"kind"`
	Lang      string      `json:"lang"`
	Title     string      `json:"title"`
	Snippet   string      `json:"snippet"`
	Citation  string      `json:"citation"`
	Rank      float64     `json:"rank"` // bm25: lower is better
}

// KnowledgeTouch records that a session worked on a repo and which notes
// version its turn-context already carries.
type KnowledgeTouch struct {
	Repo          int64     `json:"repo_id"`
	TouchedAt     time.Time `json:"touched_at"`
	InjectedEpoch int       `json:"injected_epoch"` // -1 = never injected
	InjectedHash  string    `json:"injected_hash"`
}

type KnowledgeNotes struct {
	RepoID    int64     `json:"repo_id"`
	Repo      string    `json:"repo"`
	Content   string    `json:"content"`
	ETag      string    `json:"etag"` // sha256 of Content; "" when there are no notes yet
	CapBytes  int       `json:"cap_bytes"`
	Status    string    `json:"status"`
	Untrusted bool      `json:"untrusted"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NotesVersion is one change to a repo's project notes: the text before
// and after it, who made it and, for Bothy's edits, the chat and run it was
// made in.
type NotesVersion struct {
	ID      int64     `json:"id"`
	Actor   string    `json:"actor"` // NotesByBothy | NotesByUser
	User    UserID    `json:"user_id,omitempty"`
	Session SessionID `json:"session_id,omitempty"`
	Run     RunID     `json:"run_id,omitempty"`
	Before  string    `json:"before"`
	After   string    `json:"after"`
	At      time.Time `json:"at"`
}

// NotesVersion.Actor values.
const (
	NotesByBothy = "bothy"
	NotesByUser  = "user"
)

type KnowledgeIndexRequest struct {
	Dir  string `json:"dir"`  // workspace-relative directory; "" or "." = the whole workspace
	Repo string `json:"repo"` // "owner/name"; "" = "local/<directory name>"
	SHA  string `json:"sha"`  // optional commit id shown in citations
}

// KnowledgeRepo stores the index (SQLite FTS5 in internal/store). Reads take
// a Viewer and answer ErrNotFound outside its principals; writes by id are
// for the knowledge service, which checks access first.
type KnowledgeRepo interface {
	// Upsert inserts the row of (Principal, Repo), or updates only Source,
	// Dir, SHA, Status and Error of an existing one, and writes the stored
	// row back into r.
	Upsert(ctx context.Context, r *IndexedRepo) error
	Get(ctx context.Context, v Viewer, id int64) (*IndexedRepo, error)
	ByName(ctx context.Context, v Viewer, p PrincipalID, repo string) (*IndexedRepo, error)
	List(ctx context.Context, v Viewer, p PrincipalID) ([]*IndexedRepo, error) // p "" = all of the Viewer's; by repo name
	// UpdateIndex writes the index state of r.ID (Status, Error, Files,
	// Chunks, Bytes, Skipped, Truncated, IndexedAt, IndexedSHA) and
	// r.UpdatedAt back; the requested SHA changes only through Upsert, and
	// UpdateNotes writes only the notes state, so concurrent writers never
	// undo each other.
	UpdateIndex(ctx context.Context, r *IndexedRepo) error
	UpdateNotes(ctx context.Context, id int64, n NotesState) error
	Delete(ctx context.Context, v Viewer, id int64) error                     // with its files, chunks and touches
	Files(ctx context.Context, v Viewer, repo int64) ([]KnowledgeFile, error) // by path
	Chunks(ctx context.Context, v Viewer, repo int64, path string) ([]KnowledgeChunk, error)
	// Apply runs every change in one transaction; new chunks get sha.
	Apply(ctx context.Context, repo int64, sha string, changes []KnowledgeFileChange) error
	SetSHA(ctx context.Context, repo int64, sha string) error // relabels every chunk of the repo
	// Search ANDs the words of q.Text and falls back to ORing them when
	// nothing matches all; hostile input never fails. Best rank first.
	Search(ctx context.Context, v Viewer, q KnowledgeQuery) ([]KnowledgeHit, error)
	Touch(ctx context.Context, s SessionID, repo int64, at time.Time) error
	Touched(ctx context.Context, v Viewer, s SessionID) ([]KnowledgeTouch, error) // most recent first
	MarkInjected(ctx context.Context, s SessionID, repo int64, epoch int, hash string) error
}

// TurnContextHook adds a block to the <turn-context> of a user turn
// (SPEC §5.6). The turn-context is persisted with the turn, so a block stays
// byte-identical in later prompts; the runner calls Commit once the turn is
// stored, which is when the block counts as delivered.
type TurnContextHook interface {
	TurnContext(ctx context.Context, in TurnContextInput) (TurnContextBlock, error)
}

type TurnContextInput struct {
	Principal Principal
	Session   SessionID
	Epoch     int
}

type TurnContextBlock struct {
	Text   string
	Taint  Taint // legs the block adds to the session
	Commit func(ctx context.Context) error
}

// Knowledge indexes repository checkouts (and any workspace directory) and
// keeps per-repo project notes.
type Knowledge interface {
	ToolProvider // knowledge_search, notes
	TurnContextHook
	Repos(ctx context.Context, v Viewer, p PrincipalID) ([]*IndexedRepo, error)
	Repo(ctx context.Context, v Viewer, id int64) (*IndexedRepo, error)
	Files(ctx context.Context, v Viewer, id int64) ([]KnowledgeFile, error) // what the index holds, by path
	// Index queues an index of a directory of p's workspace and returns the
	// queued row; indexing runs in the background (knowledge.updated).
	Index(ctx context.Context, v Viewer, p PrincipalID, req KnowledgeIndexRequest, by UserID) (*IndexedRepo, error)
	Reindex(ctx context.Context, v Viewer, id int64, by UserID) (*IndexedRepo, error)
	Search(ctx context.Context, v Viewer, q KnowledgeQuery) ([]KnowledgeHit, error)
	Notes(ctx context.Context, v Viewer, id int64) (*KnowledgeNotes, error)
	// WriteNotes saves notes a user wrote (ErrConflict when etag is stale);
	// they count as trusted from then on.
	WriteNotes(ctx context.Context, v Viewer, id int64, content, etag string, by UserID) (*KnowledgeNotes, error)
	// DraftNotes regenerates the notes with the fast model in the background.
	DraftNotes(ctx context.Context, v Viewer, id int64, by UserID) (*IndexedRepo, error)
	// NotesHistory lists the notes' changes, newest first; NotesVersion
	// reads one (ErrNotFound for an id the repo's history does not hold).
	NotesHistory(ctx context.Context, v Viewer, id int64) ([]NotesVersion, error)
	NotesVersion(ctx context.Context, v Viewer, id, version int64) (*NotesVersion, error)
	Delete(ctx context.Context, v Viewer, id int64, by UserID) error // index and notes
	Purge(ctx context.Context, p PrincipalID) error                  // every index row and notes file of a principal (user purge)
}
```

### 4.19 `release.go`: release deploys (v0.2 F5)

`ReleaseStore` stays outside `Store`; the GitHub service (for allowlisted repositories) and `release.GitHub` (for the rest) implement `WorkflowDispatcher` (§14.5).

```go
package core

import (
	"context"
	"time"
)

// PrefixDeploy is the id prefix of deploy records (dep_<26>).
const PrefixDeploy = "dep"

// WorkflowDispatcher starts GitHub Actions workflow runs and reads their
// status for release_deploy and release_status. It runs in the gateway with
// the GitHub token from the vault; the token never enters a box.
type WorkflowDispatcher interface {
	// Dispatch fires workflow_dispatch and returns the run it started. An
	// error means GitHub did not accept the dispatch; a run with ID 0 means it
	// did but the run could not be identified.
	Dispatch(ctx context.Context, d WorkflowDispatch) (*WorkflowRun, error)
	// RunStatus returns ErrNotFound for a run GitHub does not know.
	RunStatus(ctx context.Context, repo string, runID int64) (*WorkflowRun, error)
}

type WorkflowDispatch struct {
	Repo     string            // owner/name
	Workflow string            // workflow file name (deploy.yml) or numeric id
	Ref      string            // branch or tag
	Inputs   map[string]string // workflow_dispatch inputs
}

// WorkflowRun holds only structured fields of a GitHub Actions run: names and
// titles are authored by whoever can push to the repository, so they are left
// out to keep the result trusted.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Repo       string    `json:"repo"`
	Number     int       `json:"number,omitempty"`
	Status     string    `json:"status"`               // queued | in_progress | completed | waiting | requested | pending
	Conclusion string    `json:"conclusion,omitempty"` // success | failure | cancelled | timed_out | action_required | neutral | skipped | stale | startup_failure
	HeadSHA    string    `json:"head_sha,omitempty"`
	URL        string    `json:"url,omitempty"` // web page of the run
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Done reports whether the run has finished.
func (r *WorkflowRun) Done() bool { return r != nil && r.Status == "completed" }

type DeployStatus string

const (
	DeployDispatched DeployStatus = "dispatched" // GitHub accepted workflow_dispatch; the run is not identified yet
	DeployRunning    DeployStatus = "running"
	DeploySucceeded  DeployStatus = "succeeded"
	DeployFailed     DeployStatus = "failed"  // the run finished with a conclusion other than success
	DeployUnknown    DeployStatus = "unknown" // no longer followed: releases.follow_for elapsed or the run was not found
)

// Terminal reports whether Bothy has stopped following the deploy.
func (s DeployStatus) Terminal() bool {
	return s == DeploySucceeded || s == DeployFailed || s == DeployUnknown
}

// Deploy is one approved release_deploy and what became of its workflow run.
type Deploy struct {
	ID          string       `json:"id"` // dep_<26>
	Environment string       `json:"environment"`
	Repo        string       `json:"repo"`
	Workflow    string       `json:"workflow"`
	Ref         string       `json:"ref"`
	Principal   PrincipalID  `json:"principal_id"`
	Session     SessionID    `json:"session_id"`
	Run         RunID        `json:"run_id,omitempty"` // the agent run that dispatched it
	Approval    ApprovalID   `json:"approval_id,omitempty"`
	RequestedBy UserID       `json:"requested_by"` // the account; a teammate's deploy: their chat actor "channel:<app>:<id>"
	ApprovedBy  []UserID     `json:"approved_by"`
	Chat        *ChatRef     `json:"chat,omitempty"` // originating chat; the outcome is posted there
	Status      DeployStatus `json:"status"`
	WorkflowRun int64        `json:"workflow_run_id,omitempty"`
	RunURL      string       `json:"run_url,omitempty"`
	Conclusion  string       `json:"conclusion,omitempty"`
	Error       string       `json:"error,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	FinishedAt  time.Time    `json:"finished_at"`
	// Via is who dispatched the deploy, so the follower reads its run
	// through the same credentials whatever the allowlists say later. A
	// deploy recorded before Via was kept has "" and follows today's choice.
	Via string `json:"via,omitempty"` // DeployViaConnection | DeployViaToken
	// SHA is the commit the workflow run built, once the run is known.
	// Subject is that commit's first line, cleaned to one plain line: it is
	// written by whoever can push to the repository, so it is shown only as
	// quoted text and never goes into cards, notices or prompts.
	SHA     string `json:"sha,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// Deploy.Via values.
const (
	DeployViaConnection = "connection" // the GitHub connection that allowlists the repository
	DeployViaToken      = "token"      // releases.github_token
)

type DeployFilter struct {
	Environment string // "" = all
	Open        bool   // only deploys still followed (dispatched or running)
	Page        Page   // Before = created_at unix ms
}

// ReleaseRepo stores deploy records. Get and List return rows of the
// Viewer's principals; viewers with Admin also see everyone's (deploys are
// operational records, never message content).
type ReleaseRepo interface {
	Create(ctx context.Context, d *Deploy) error // assigns ID when empty
	Update(ctx context.Context, d *Deploy) error // ErrNotFound for a missing row
	Get(ctx context.Context, v Viewer, id string) (*Deploy, error)
	List(ctx context.Context, v Viewer, f DeployFilter) ([]*Deploy, error) // newest first
}

// ReleaseStore is implemented by the stores that keep deploy records (the
// SQLite store and memstore). It stays outside Store so other Store
// implementations need not change.
type ReleaseStore interface {
	Releases() ReleaseRepo
}
```

### 4.20 `picture.go`: Bothy's picture in the chat apps

`internal/picture` implements `AgentPicture` (the embedded default rendered from the mascot, `web/public/icon-maskable.svg`, or `<data>/picture.png`); the Telegram and Discord adapters implement `PictureSetter`; `ChannelHub.ApplyPicture` (§4.6) and the hub's first connection use them (§5.1, §8.3 `/api/agent/picture`).

```go
package core

import (
	"context"
	"errors"
)

// Picture is Bothy's picture in the chat apps: the built-in default or one an
// admin uploaded, always a square, opaque 1024 px image.
type Picture struct {
	PNG  []byte `json:"-"`
	JPEG []byte `json:"-"` // the same picture, for chat apps that take only JPEG (Telegram)
	// Custom is true when an admin replaced the default.
	Custom bool `json:"custom"`
	// Version changes whenever the picture does (a hash of PNG).
	Version string `json:"version"`
}

// AgentPicture keeps Bothy's picture (internal/picture): the admin's upload in
// the data dir, else the embedded default.
type AgentPicture interface {
	Current(ctx context.Context) (Picture, error)
	// Set checks an uploaded PNG, JPEG or WebP, crops it to a centred square,
	// scales it and re-encodes it (dropping its metadata), then keeps it.
	// A file that is not a usable picture is ErrInvalid with a plain message.
	Set(ctx context.Context, upload []byte) (Picture, error)
	// Reset goes back to the default picture.
	Reset(ctx context.Context) (Picture, error)
}

// PictureSetter is implemented by adapters that can change the bot's own
// picture (Telegram, Discord). Slack lets only a person change an app's icon.
type PictureSetter interface {
	// BotID is the bot's own user id on the platform, "" until it is known.
	// Unlike its name it tells bots apart: Discord bot names repeat.
	BotID() string
	// PictureID names the picture the bot shows now, whoever set it: the
	// platform's id for that upload, "" when the bot has none. Any change
	// of picture changes it.
	PictureID(ctx context.Context) (string, error)
	// SetPicture replaces the bot's picture. A platform that limits how
	// often it may change answers with an error whose FailureCode is
	// FailRateLimited.
	SetPicture(ctx context.Context, p Picture) error
}

// PictureOutcome is what applying the picture did in one chat app.
type PictureOutcome string

const (
	PictureUpdated PictureOutcome = "updated" // the chat app shows the picture now
	PictureFailed  PictureOutcome = "failed"  // it couldn't be changed; Err says why
	PictureManual  PictureOutcome = "manual"  // only a person can change it there (Slack)
)

// PictureResult is ChannelHub.ApplyPicture's answer for one chat app.
type PictureResult struct {
	App     string
	Outcome PictureOutcome
	// Err is why it failed: ErrNotConnected, or the platform's error (see
	// CodeOf for rate limits).
	Err error
}

// ErrNotConnected: the chat app is turned on but not connected right now.
var ErrNotConnected = errors.New("chat app not connected")
```

---

## 5. Agent run lifecycle

### 5.1 Inbound from a chat channel (router, M9)

People in chat apps are not linked to web accounts. A chat app either says who belongs to the team Bothy serves (Slack: its workspace or Enterprise Grid org; Discord: the servers where a channel is on) or it can't (Telegram), and an admin lets people in there. Someone in the team is a **teammate**: they get a person space of their own (§5.2). Channels are spaces that belong to the team and have no owner.

| Chat | Chat app | Who Bothy answers | Principal | Provenance |
|---|---|---|---|---|
| DM | Slack | admitted teammates: Slack says member | the teammate's person space | `ProvUser` (forwarded: `ProvUntrusted`) |
| DM | Discord | admitted teammates: member of a server with a channel on, or let in by an admin while Discord can't tell | person space | same |
| DM | Telegram | people an admin let in | person space | same |
| Channel | Slack | admitted teammates, channel on, mentioned if mention-only | the channel's space | `ProvUntrusted` |
| Channel | Discord | admitted teammates (this server or another with a channel on) | channel space | `ProvUntrusted` |
| Channel | Telegram | anyone not blocked, group on | channel space | `ProvUntrusted` |

Outsiders get one line a day in DMs and silence in channels. Blocked people get silence everywhere. People waiting to be let in get one line a day plus approver DMs, unless `unknown_senders: ignore`. When an admin lets them in or blocks them (`inbox.changed`), the hub edits those DMs to *"✓ Maya (@maya) on Telegram was let in."* or *"✗ … was blocked."*, without the button (best effort: it remembers the DMs in memory for 7 days).

1. **Adapter → `Hub.OnMessage`.** Adapters normalise platform updates into `core.Inbound` and never block (256-slot buffer per adapter; overflow drops and counts a metric). Slack drops messages from bots and apps (`bot_id`, `bot_profile`). Discord sets `Inbound.Team` to the guild id.
2. **Dedup:** `Store.Dedup().Seen(channel, msgID)`; repeats are dropped (Telegram redelivery, Slack retries).
3. **Identity and membership:** `Identities().Touch` (a new sender is `pending`; on a chat app with no `MemberChecker` its verdict starts as `unknown`; a DM's chat is remembered), then `Auth.ResolveIdentity`.
   - `blocked` → drop silently.
   - **Sender limit first:** `Budget.AllowSender(channel+":"+sender)` (10/min, burst 5) runs before any membership lookup. Over it, an admitted teammate's DM gets one *"Bothy will read new messages again in about 60 s."* per window; anyone else, and every group, silence. In a group the limiter runs only for a message Bothy would answer (the channel is on, and mentioned when mention-only), so chatter costs nothing.
   - **Membership** (adapters implementing `core.MemberChecker`, Slack and Discord): verdicts are `member`, `outsider`, `unknown` or unchecked, kept on the identity with their time. A verdict younger than `MemberTTL` (1 h) is used as is; an older one is asked again off the dispatcher, in order per sender, through a per-adapter limiter (30 lookups a minute, 4 in flight). When the platform doesn't answer (or lookups are throttled, or one failed for this sender in the last minute), a member verdict younger than `MemberStale` (4 h) still counts; otherwise a DM gets *"Bothy can't check your access right now. Try again in a few minutes."* at most once an hour and a channel stays silent. `Auth.Admit` records each verdict: a `pending` member becomes a teammate with a person space (audited `identity.teammate`), an outsider keeps their status and chats but loses Can approve, and someone who was admitted and is now an outsider is **lost**: their runs stop (`Runner.StopPrincipal`) and admins' inboxes are told. `unknown` keeps a membership teammate's member verdict for 4 h.
   - **Scope:** what membership means on an adapter is `MemberScope()` (Slack: team and enterprise ids) plus the Discord servers with a channel on. When it changes (another workspace's token; the last channel in a server turned on or off) every verdict on that adapter becomes stale at once (`ResetMembership`, `meta["member_scope:<channel>"]`). The first scope seen resets nothing.
   - **Sweep:** every 15 min the hub renews, through the same limiter, verdicts older than 45 min of teammates who can approve or own an enabled schedule (at most 100 a pass). When Discord connects, channels recorded before Bothy knew their server get it from `InspectChat`.
   - **Admitted** (`IdentityResolution.Admitted`): a teammate whose person space is on and whose verdict is `member`, or whom an admin let in while the verdict is `unknown`.
   - **DM:** admitted → the teammate's chat (a pending greeting is sent first, once: the hello, or for accounts linked before 0013 the "moved" greeting, *"Hi Maya, this chat is no longer tied to your Bothy account, so your earlier chats and notes stay in Bothy on the web. Ask me anything; when something needs approval, I'll ask an admin and tell you here."*). Outsider → *"Bothy works with members of {workspace}."* (Slack) or *"Bothy works only with members of its team."* at most once a day per identity (`Auth.NoticeSender`), in either `unknown_senders` mode; no admin notice, no inbox item, no model call. Anyone else (Telegram, Discord without a verdict, or a membership teammate Discord can no longer vouch for) **waits**: their first words are kept for the inbox item (once, one line, redacted), admins' inboxes are told, and with `pair` (the default) they get *"You'll be able to use Bothy once an admin lets you in."* at most once a day while approvers hear about it (web banner `unknown_sender`; approver DMs at most 3 an hour in fixed windows, the next window's first DM counting those held back, and none spent while there is no approver to tell). With `ignore` nobody is told, but they are still listed in Approvals. **No LLM call is ever made for someone who isn't admitted.**
   - **Group:** the chat must be an **enabled space** (step 7). On Slack and Discord only admitted teammates are answered; guests, other companies, bots and Slackbot get silence. On Telegram anyone not blocked may talk, as untrusted.
   - **Private, in a group** (`Inbound.Private`: only the sender saw it, a Slack slash command, which works in channels the bot is not in) → never creates a space. Bothy answers it **privately**: where it was typed, seen by the sender alone, through the adapter's `core.PrivateReplier` and `Inbound.ReplyRef` (Slack: an ephemeral reply through the command's `response_url`, good for 30 minutes and five posts (`ChannelCaps.PrivatePosts`), sent only to `hooks.slack.com` and kept out of logs, errors, the database and runs); when the adapter can't, there is no reference or the post fails (logged without it), in the sender's DM (the hub opens it with `Channel.OpenDM` and remembers it), where an answer about a channel Bothy is on in starts by naming it, since "this chat" or "here" would be the DM (*"In #eng: Stopped."*; a longer answer on the lines after *"In #eng:"*), and the *chat* refusal drops "here" (*"In #eng: Only someone who can approve can use /bothy new."*). An answer goes to one place: one that needs more posts than the reference takes goes to the DM whole. Only a post that fails partway leaves the first pieces shown where it was typed, and the DM gets the whole answer too. Where the group has no enabled space, within the sender limit, an admitted teammate asking for help (`/help`, `/start`) gets the help list and nothing else, and `/stopall` (the kill switch, for approvers in any chat) and `/resume` are answered as anywhere, every time; anything else gets *"Bothy is off in #eng. Message Bothy directly, or ask an admin to turn #eng on."* (*"this channel"* when unnamed), or in the DM *"Bothy is off in #eng. You can message Bothy here, or ask an admin to turn #eng on."* (*"that channel"*), at most once per sender and channel an hour. Anyone else gets what a DM from them would, in their DM. In an enabled space it goes on as any message (step 7): free text starts a task in the channel, and a command's answer (help, status, usage, pins, model, refine, refusals, *"There's no /word command."*) is given privately. What needs buttons, later edits or a lasting record (waiting and outsider lines, approval cards, *Cancel request*, runs) never goes privately. A slash command typed in the bot's own DM is answered there; one typed in a DM between two people (Slack sends that conversation's id, which the bot cannot post in) is moved by the adapter to the sender's own DM with the bot (`conversations.open`) before it reaches the hub.
4. **(Folded into step 3.)**
5. **Command fast path** (before any queue; forwarded content is never a command). Access: *chat* is the DM's teammate, or in a channel someone who can approve (admitted, verdict under 4 h); *anyone* is anyone Bothy answers in that chat; *approver* is an admitted teammate who can approve, in any chat; *web* only replies where to do it.

| Command | Access | Effect |
|---|---|---|
| `/stop` | anyone | `Runner.Stop` for this chat's session (§5.11), audited as the chat actor. Reply "Stopped." |
| `/stopall` | approver (hidden) | kill switch on (`Runner.SetPaused(true, "")`, then `StopAll`) with the chat actor as the audit origin; the banner names "{Name} · {App}" and `meta.paused_by` stores `channel:<app>:<id>`; the reply reports how many runs it stopped and where Bothy is resumed, since chat approvers can't (*"Paused. Stopped 2 tasks. An admin can resume Bothy on the web, under Settings → Security."*). Refusal: *"Only someone who can approve can pause Bothy."* |
| `/resume` | web (hidden) | *"Resume Bothy on the web, under Settings → Security."* |
| `/new` | chat | archive the chat's session; next message starts a fresh one. The reply says the previous conversation is archived and that Activity in the web UI still opens it |
| `/status` | anyone | in plain words: the chat's task (*"Working on a task for 2 min 10 s. 1 more message is waiting."*, *"Waiting for approval for 3 min."*), today's spending of the chat's principal against its daily limit (*"Today: $0.87 of the $2.00 daily limit."*; money as ui.md §14.2), *"Refine is on for this conversation, so answers are slower and cost more. /refine off turns it off."* while refine is on, and *"Running code is off on this server."* only when it is |
| `/pin <text>`, `/unpin <n>` | chat | manage session pins |
| `/pins` | anyone | list session pins |
| `/compact` | chat | `Runner.Compact`, in the background so the chat's later messages are not held behind it: replies "Older messages were summarised." when done; while a task runs it says it will summarise them when that task is done and waits up to 15 min, then says it is still waiting; a second `/compact` for the same session is refused while one is in flight |
| `/model <model name>`, `/model`, `/model default` | chat | set, show or clear the session model override. Models are named in plain words (`ModelInfo.DisplayName`); setting takes a display or short name (case-insensitive, the first configured provider winning a shared name) or, as a fallback, a `provider/model` ref. Bare `/model` only shows; clearing takes `default` (or `clear`, `reset`) |
| `/usage` | anyone | today's spending and this session's cost, money as in `/status` |
| `/refine [on\|off\|default]` | chat | v0.2: set this session's refine mode; `/refine` alone shows the setting with the typical cost (`core.RefineTypicalCalls`). Also in the Telegram menu and as a Slack `/bothy` subcommand |
| `/help` | anyone | *"Commands:"*, then one line per command, the command and what it does in plain words (*"/stop – stop the current task"*); on Slack the command is in code (*"`/bothy stop` – stop the current task"*), while elsewhere it stays plain text, which Telegram makes tappable (it makes nothing in code tappable); hidden ones are not listed |
| `/pair` | anyone (hidden) | *"Bothy doesn't use link codes any more. Write here to ask it anything."* |
| `/start` | anyone (hidden) | in a DM, the hello; in a channel, the help text. A `/start <payload>` is plain `/start` |

   Every reply, notice and help line names a command as the chat app types it (`ChannelCaps.CommandPrefix`): on Slack, where `/bothy` is the only slash command, *"/bothy stop"*; on Telegram, and on Discord, which reads commands from plain text, *"/stop"*. The examples here use the plain form. The *chat* refusal is *"Only someone who can approve can use /{cmd} here."* There is no `/approve CODE` command: approvals happen only through card buttons or the web UI. A message that is only `/word` (Telegram may add `@bot`; word characters only) naming no command gets *"There's no /word command. /help lists the commands."* and never reaches the runner. Any other text starting with a slash and forwarded content are ordinary messages.
6. **Button presses → `Hub.OnAction`.** Data `a:<approval>:<o|s|a|d>:<mac>` → `Approvals.ParseButtonData` (MAC check), the pressed message must be one the approval recorded (§5.9). The person whose message started the run may always deny, which **withdraws** the request (any approver kind, from any recorded message of it, including the waiting line's *Cancel request*). Otherwise: on the waiting line *"Only the person who asked can cancel this."*; admin-only, multi-approver (release) and account requests *"Open in Bothy to approve."*; a presser who isn't a teammate marked Can approve *"Only an approver can approve this."*. On Slack and Discord the press first syncs the membership scope as a message does (step 3), so a scope change (another workspace's token, the last channel of a server turned off) makes the presser's verdict stale at once; a verdict over an hour old is asked again within the press's 2.5 s budget; when that fails, only a verdict under 15 min (`ApproverStale`) counts (*"Bothy couldn't check that you can approve. Try again, or open it in Bothy."*). Truncated cards approve nothing (*"Open in web to approve."*). `Approvals.Resolve` gets `ByIdentity` = `<app>:<id>` and checks it all again from the store, including, for a member verdict (`membership = member`: the chat app vouches for them), that it is under 2 h old (`ApproverFresh`, as for a full card); a chat approver can't always-allow for a channel (*"Only an admin on the web can always allow this for a channel."*). The adapter `Ack`s within 3 s with a toast ("Approved.", "Request cancelled.", "Already decided."). Data `s:<run>` → Stop: the teammate whose chat it is, someone Bothy answers in the channel, or an approver (*"Only the person this chat belongs to or someone who can approve can stop it."*).
7. **Principal and gating:**
   - DM from an admitted teammate → their person space principal.
   - Group message → the chat must be an **enabled space** (`Spaces().ByChat`); otherwise ignored. The first sighting creates a disabled space owned by nobody (`Space.AddedBy` keeps the platform's adder, `Space.Team` the Discord guild), named by `Inbound.ChatTitle` (or "<App> group <chat id>"), and `InspectChat` fills in its server, member count and Slack Connect sharing. Admins get the `new_channel` banner and an inbox item; chat approvers a DM (at most 3 an hour): *"Bothy was added to “<title>” in <App>. It stays silent there until an admin turns it on in Bothy."* A sighting skipped by the sender limit is retried with the group's next message; a `Private` message never creates one. A space still under its fallback name is renamed, its principal's display name too, from the first later message that carries a `ChatTitle`; a title an admin set is never overwritten, and neither touches `reviewed_at`. With `mention_only` (default), the message must mention or reply to the bot. Every channel message is `ProvUntrusted`. In a channel, a message from another sender never steers a run already going: it queues as its own turn.
   - **Forwarded content** (`Inbound.Forwarded`: Telegram `forward_origin`, `forward_date` or `via_bot`) is `ProvUntrusted` whoever forwards it, the teammate whose DM it is included. It is never treated as a command (step 5), and mentions inside it do not address the bot.
8. **Session:** `Runner.ResolveSession(principal, chat)` keyed by `ChatRef.Key()` (one open session per chat or thread per principal). If `agent.session_idle_reset` (default 0 = never) has elapsed since the last message, the old session is archived first.
9. **Attachments:** the hub fetches each attachment with `Channel.Fetch` (≤ `server.upload_max_mb`, default 20 MB; larger ones are skipped with a note) into `<work>/ws/<workspace>/.bothy/in/<msgid>/<name>` (0660, name sanitized) and passes `Part{Type: image|file, Blob{Ref, MIME, Size}}`.
10. **Submit:** `Runner.Submit(RunRequest{User: "" for team principals, Sender: "<app>:<id>", SenderName, … Sink: Hub.StreamTo(chat,"stream")})`.

**The bot's picture.** When a chat app whose adapter implements `core.PictureSetter` (Telegram, Discord) connects and its bot's id (`PictureSetter.BotID`: Telegram's numeric user id, Discord's snowflake; names can't tell bots apart, since Discord bot names repeat) differs from `meta["picture:<channel>"]`, the hub asks `PictureID` (Telegram: the newest profile photo's `file_unique_id`; Discord: the avatar hash; `""` for none); a bot with no picture gets Bothy's current one (`AgentPicture.Current`: the admin's, else the default). A picture someone set in BotFather or the developer portal is never replaced on connect. The meta value is the bot id alone for a bot whose picture is its own, so later connections and boots don't ask again, or `<bot id> <version> <picture id>` once Bothy gave the bot `Picture.Version`, read back as that `PictureID`. On connect, a bot with such a record whose version is no longer Bothy's (a new built-in default after an upgrade, or an admin's change while the app was off or down) and which still shows that picture id gets Bothy's current picture; one whose picture id changed was changed by someone else, and its record becomes the bare id. A picture id the platform won't give back after a change records the bare id too: Bothy never replaces what it can't recognise. A record from before this tracking (a bare id) is left alone; an admin sends the picture again from Settings. A failed check or upload is logged and tried on the next connection. `ApplyPicture` (an admin changing or resetting the picture, §8.3) replaces the picture of every turned-on Telegram and Discord bot, in parallel and 30 s each: `updated`, or `failed` with `ErrNotConnected` when the app isn't connected or the platform's error (Discord allows a few avatar changes an hour: `rate_limited`); Slack, where only a person can change the app icon, is `manual`. A per-adapter lock keeps a first-connection check and admins' changes from crossing, and each sends the picture Bothy holds once it has the lock (`AgentPicture.Current`, not the caller's), so of two changes at once the later is the one the chat apps keep.

Web (`POST /api/sessions/{id}/messages`) and API (`/v1/chat/completions`) skip steps 1–8 with an authenticated actor; web uploads are already in the workspace (`POST /api/uploads`). Web accounts have no chats in chat apps.

### 5.2 Principals and spaces

| Principal | Created | Memory | Vault | Tools | Approvals go to | Billing | Run slots | Deleting data |
|---|---|---|---|---|---|---|---|---|
| account (`usr_…`) | with the user | MEMORY/USER/HEARTBEAT files | `user:<id>` then `global` | all available | the user on the web (an admin for admin-class) | the user's daily cap | per user | purging the user |
| channel (space, kind `group`) | first sighting of a group chat (off until an admin turns it on) | none | `global` only | `restricted` (default): `SpaceOK && SideEffect == read` only; `standard` (admin opt-in): normal policy, with untrusted messages asking for everything above `read` | chat approvers' DMs and admins on the web; never the channel | its own line, within the chat share | per principal | an admin deletes the channel once it is off (`DELETE /api/spaces/{id}`) |
| teammate (space, kind `person`) | `Auth.Admit` (member) or an admin's Let in | MEMORY/USER files (no HEARTBEAT) | `global` only | `standard`, with floor FP (§5.8) | chat approvers' DMs and admins on the web | `member_daily_usd` or `per_user_daily_usd["<app>:<id>"]`, within the chat share | per principal | *Delete their data* once blocked or no longer in the team |

Every principal also reads TEAM.md, what Bothy remembers for everyone, in S1 (§5.6, §14.9): one file for the whole team, changed only by an admin on the web or by a `memory` call with file `TEAM` that an admin approved.

Team-owned principals (`Principal.User == ""`: channels and teammates) belong to no `core.Viewer`: their chats, runs, memory, uploads and transcripts are not on the web, admins included. Admins see their group chats, grants, schedules (on or off, delete) and spending, and audit rows of what they read outside Bothy (`tool.read`). `SpaceOK` read tools are `fs_read`, `fs_list`, `web_search`, `web_fetch`, `skill_load`, `skill_read`, `tool_search`, `use_tool`; `tool_search`/`use_tool` are offered only when lazy tools are reachable (§5.6), and `web_search` only with a search provider, so a restricted channel with no search provider and no skills is offered just `fs_list`, `fs_read` and `web_fetch`. Restricted channels have no `memory`, `session_search`, `fs_write`, `shell`, `python`, MCP or `schedule_*` tools; a standard channel gets `memory` only to propose a change to team memory (file `TEAM`, an admin approves it; its own files are refused with *"Channels have no memory of their own. To remember something for everyone, use file TEAM; an admin approves it."*) and never `session_search`; every principal's workspace is its own (`ws_…`). v0.2: `knowledge_search` and `notes` (reads; edits escalate to `local_write`) are `SpaceOK`; no GitHub tool is offered to a restricted channel; `release_deploy` is denied for channels and needs one more vote for a teammate (§5.8).

### 5.3 Taint and the trifecta state machine

`sessions.taint` is a bitmask (`core.Taint`), monotonic within a session (only a clean fork resets it):

| Leg | Set when |
|---|---|
| `TaintPrivate` | session snapshot includes non-empty MEMORY/USER (`MemorySnapshot.Private`; TEAM.md in S1 adds no leg: only admins put text there, and every session reads it); a tool with `Manifest.Taint` ⊇ Private runs (`fs_read`, `session_search`, MCP tools approved as `private`; v0.2: `github_issues`, `github_pr`, `github_runs`, `repo_changes`, `repo_checkout`, `knowledge_search`, `notes`); injected project notes (§5.6) |
| `TaintUntrusted` | a `ProvUntrusted` message enters the session; a tool with Taint ⊇ Untrusted runs (`web_fetch`, `web_search`, MCP tools unless `trusted_output`; v0.2: `github_issues`, `github_pr`, `github_runs`, `repo_changes`, `repo_checkout` (which also marks the workspace untrusted), `knowledge_search`, and a `notes` read of untrusted notes; their results are untrusted and spotlighted, while `repo_list` is trusted metadata); untrusted project notes enter the turn-context (§5.6); the box moved bytes through the proxy since the previous tool call, background processes included (`Egress.TakeActivity` is taken after every tool call and never cleared before one): that call's output is untrusted; an exec or read-only fs tool reads from a workspace marked untrusted (below); a `session_search` hit with `Trusted=false` |
| `TaintSend` | the run's toolset contains any `external_send` capability or the box has any egress grant (informational, for the UI badge) |

`Taint.Exposed()` (Private and Untrusted) triggers the **trifecta floor**: every `external_send` or `irreversible` action asks (no "allow for session" offered), session/principal egress grants are suspended at the proxy, `web_fetch` follows the URL-provenance rule, links in agent output are neutralized, and the UI badge turns red with a "Fork clean" button (`POST /api/sessions/{id}/fork` → `Runner.Fork`: a new session with no history, taint 0 and a system note linking the old one, "Clean fork of “<title>” (<id>). History and taint were not carried over; the workspace files are shared, so reading ones that came from untrusted sources taints this session too."; a channel session's chat moves to the fork).

**Workspace provenance.** The workspace belongs to the principal and is shared by all its sessions (chats, forks, schedules), and its files carry no provenance of their own. The runner marks it untrusted, permanently, in meta key `agent.workspace_untrusted.<ws>` (`"1"`; a failed read counts as marked) when its box egresses, when untrusted output is spilled into `.bothy/out`, when a session holding Untrusted runs an exec or writing fs tool, or when a `ProvUntrusted` message brings files. From then on the output of exec and read-only fs tools from that workspace is untrusted in every session of the principal, clean forks included, and parallel reads are decided with that taint.

**Link neutralization** (channel output, the agent's `Sink` and therefore `/v1` replies, while Exposed; one implementation, `core.NeutralizeLinks` / `NeutralizeMarkdown` / `NewLinkFilter`, §4.15) works on the text as it renders: it defangs scheme URLs (`hxxps://evil[.]com/…`), scheme URLs with a bracketed IPv6 host (`hxxps://[2001:db8::1]/…`), every `scheme://` whatever follows it, nested autolinks (`<a:<b:c>>`), `www.` hosts, bare domains (`attacker.com/?q=…`), IPv4 runs of 4 or more dotted parts, hosts written with the U+2024 or U+FE52 dot look-alikes, and hosts or `scheme:` colons hidden by markdown backslash escapes (`evil\.com`, `https\://`), emphasis (`evil**.**com`, `_`, `~`) or invisible format characters; the defang is applied to the source text. Inline code and fenced blocks are left alone (the agent sink tracks fences across chunks). The neutralizer is idempotent. The web renderer never links bare domains. In a chat app, the first reply of a conversation that had links neutralized ends with one note saying why and how to get clickable links back without loosening the rule: *"Links here are shown as plain text because this chat has seen private information and content from outside. /new starts a fresh conversation with clickable links."* The hub remembers the conversations it told for 7 days, in memory, so a restart may repeat the note once.

`Policy.NextTaint` is pure and is applied by the runner after every tool result and inbound message; changes publish `session.updated`.

### 5.4 Submit, session workers, busy handling, semaphores

- **Worker per session:** created lazily, exits after 5 minutes idle, mailbox `agent.queue_depth` (8). Full → `ErrBusy` ("Busy. Send /stop to interrupt.").
- **Idle coalescing:** a DM message to an idle session waits `agent.coalesce_window` (1.2 s) for more messages; all are merged into one turn.
- **Busy, `agent.busy_mode: steer`** (default): the message is persisted immediately as `KindSteer` (with its own turn-context) and injected at the next step boundary, before the next LLM call. The chat gets one short reply per run, "Got it, adding that to the current task.", sent by the hub only (the runner sends none). If the run is `waiting_approval`, the reply is "Bothy will read this after you answer the approval above." and the steer is held until resolution. A stopped run takes no steers, and neither does a run that has started finishing (§5.5 step 10): the message queues for its own run.
- **Busy, `queue`:** messages wait and are coalesced into one user turn when the run ends. A queued request whose sink got `run.queued` always gets a final event: when its turn is merged into an earlier queued turn, the notice "Merged into the reply above." plus `run.finished` (`done`); when `/stop` drops it, `run.finished` (`stopped`).
- **ChatAPI requests** (`/v1`) are never queued: while the session has a run, Submit returns `ErrBusy` (§8.5), because the caller streams only the run it starts.
- **Abandon:** when a stuck worker is abandoned (§5.11), its queue and pending turn move to the session's fresh worker (during shutdown they are failed as in §5.15).
- **Semaphores:** global `agent.max_concurrent_runs` (4) and per-user `agent.max_runs_per_user` (2). The per-user slots are keyed by the user for accounts and by the principal for teammates and channels, so two teammates never share one. They gate **LLM streaming and tool execution only**. A run **releases both while `waiting_approval` or `parked`** and re-acquires them (per-user first, then global, so a user at their limit never holds a global slot) before continuing. Queued runs publish `run.queued{position}` and channels show "Queued (1 ahead)".
- **Steering in channels:** a run of a channel takes steers only from the sender whose message started it (`RunRequest.Sender`); anyone else's message queues as its own turn, with its own requester.
- **Kill switch:** when `Runner.Paused()`, Submit returns `ErrPaused` ("Bothy is paused by an admin.").
- **Daily cap:** `Budget.CheckDaily(principal)` before creating a run (the principal's own cap, the chat share for teammates and channels, the global cap, §5.12); exhausted → system notice with reset time, no run row.

### 5.5 The run loop

1. **Start.** Insert `runs` (status `queued` until both semaphores are held, then `running`; `limits` snapshot: interactive or scheduled defaults, overridden by schedule or request). `Budget.StartRun`. Publish `run.started`. The hub's `StreamTo` sink (not the runner) renews the typing indicator every 4 s while `run.status` is `thinking` and sends a placeholder ("Working…" with a Stop button) at the first delta or after 1.5 s.
2. **Persist the user turn.** Input parts + one `PartContext` part (§5.6 turn-context), provenance from the request. Every message the runner stores (this one, assistant messages, tool results, notices) is masked first (`Message.Redacted`; signed thinking keeps its text, which the provider checks against the signature): a key pasted into a chat never reaches the history, FTS or the API, even after it is deleted from the vault. Masking for good uses `vault.Redactor.ForScope(principal's user scope)`: shared, environment and the vault's own keys and the principal's own personal keys, never another person's, so a member's personal key such as "deploy" cannot erase that word from everyone's chats (a space's messages use shared keys only). `message.created`, model input and every read path (`GET /api/sessions/{id}/messages`, search snippets, `GET /api/sessions/{id}/pins`) mask with the full redactor, which is reversible. Pins are masked the same way when added (web and the chat `/pin`). Attachments are already `Blob` parts in the workspace (hub or `POST /api/uploads`); the runner only loads `Blob.Data` before each provider call. URLs in `ProvUser` text are recorded with `Sessions().AddURLs(…, "user")` (URL provenance, F8). Before the first tool call the runner creates `<work>/ws/<id>/` and `.bothy/{in,out,tmp}` (2770, group 10001) so `fs_*` tools work with every sandbox backend.
3. **Assemble** the request for the head of the slot's chain (§5.6). If the estimate exceeds `trigger × window − reserve` (or a price tier threshold), compact first (§5.10). The catalog carries long-context tiers (catalog-gen `tiers.json`, §12 M2), so for Gemini Pro and xAI models compaction triggers at 200K tokens and for OpenAI's 1M-window models at 272K, instead of 0.70 × window.
4. **Pre-call checks:** `RunMeter.Step()`, then `BeforeCall(estimate, model)` (run tokens, run $, user and global daily $ with projection).
5. **Stream:** `LLM.Complete(slot, override, build, on)`. Text deltas are coalesced (50 ms) into `run.delta` on the bus and forwarded to the `Sink`; thinking deltas go to the UI only (`agent.show_thinking`). Tool-call deltas are assembled by the provider client. Every `run.delta`, `run.thinking` and `run.tool_output` flush publishes only up to the last whitespace short of a tail a registered secret could still be completing (`PartialAt`, §4.9), and redacts what it publishes; the rest waits for the next flush or the end of the call (a whitespace-free run of 2 KiB or more is cut after the last character no credential token contains). The bus therefore never receives raw per-flush text, and a secret or token split across flushes is masked whole. When the session is Exposed the sink's copy also has links neutralized outside code (§5.3).
6. **Persist the assistant message** (text, thinking + signature parts, tool calls). `RunRepo.AddLLMCall(CallInfo)` (with breakdown and `prefix_hash`), `RunMeter.AfterCall` (spend_daily, 80% warning), `LLM.Calibrate`. Publish `run.usage`. A `prefix_hash` change between two step calls of the same session, epoch and model is logged as a cache-stability bug unless S1 changed too (standing rules or team memory, both legitimate, §5.6); refine critique and revision calls are recorded with the facade's hash of what they sent but are not checked. The agent and the facade hash with the same `core.PrefixHash`.
7. **No tool calls** → step 10. **Refine (v0.2, §14.2):** when refine mode is on for the run (`RunRequest.Refine`, then the session's meta key `agent.refine.<session>`, then `agent.refine.enabled`; config and session apply to `message` and `api` triggers only), each step's text is held back until a tool call shows the step is not the final answer. A final answer (text, no tool calls) becomes draft 0; `drafts − 1` more drafts repeat the step's request with `ToolChoice: none` (tools block kept, so the cached prefix is unchanged, except on Bedrock, where `none` sends no tools at all and the drafts share no cached prefix with the tool-using step: §14.1); each draft gets `depth` rounds of continue → critique → correct, skipping the correction when the critic confirms and the continued text states a final answer; then a plurality vote over normalized `Final answer:` lines (ties to the earliest draft), stopping early once all drafts (two or more) agree. At most 4 calls are in flight; they are metered as purpose `agent` at the same step, do not count as steps, and `BeforeCall` counts calls in flight. Refinement stops 5 s before the time limit, on a new message or on stop, keeping the best answer so far; only the winner is streamed, without its closing `Final answer:` section (`refine.Strip`; a reply that is only that section keeps the answer), and its message carries a `PartRefine` trace. A steer pending when the final answer arrives skips refinement for that step, and a message whose refine toggle disagrees with the active run is queued as its own turn rather than folded in as a steer.
8. **Tool calls** → §5.7 for each, in model order. Consecutive calls that are all `read`, all auto-allowed and not exec run in parallel (≤ `agent.parallel_read_tools`, 4); everything else is sequential.
9. **Loop:** drain steer messages into history, re-check budgets, go to 3. The tools block and S2 never change mid-epoch; S1 changes only when the standing rules or team memory change (step 6, §5.6).
10. **Finish:** the run stops taking steers as soon as it starts finishing. Final status (`done`, `stopped`, `failed`, `budget_exceeded`, `loop_detected`). Update run totals. Publish `run.finished`. The sink finalizes (last edit, overflow split, Stop button removed, optional usage footer "· 3 steps · $0.01"). Budget stops add a notice; a steps stop offers **Continue** (`Runner.Continue`: new run, same session, +10 steps; the continued run is message-triggered but keeps the provenance of the run it continues, so F6 still applies to a run an untrusted message started). On `budget_exceeded`, `loop_detected`, `failed` or `interrupted`, if a steer arrived after the last completed model call (an assembly that was never sent does not count as read), the session gets the notice *"Bothy stopped before reading your latest message. It stays in this chat, so your next message picks it up."* **Titles (v0.2 UI, S14):** the first stored turn of an untitled, non-hidden session sets `Title` to `core.DraftTitle` of the first message someone wrote (redacted; turns with provenance `system`, such as Bothy's own notices and resumed actions, never name a chat) and publishes `session.updated`. After the first run that ends `done` (an earlier parked, failed or stopped one leaves it to the next; the runner remembers in memory which titles are still drafts), a fast-slot call (≤ 300 tokens, metered) with the prompt *"Write a title of at most six words for this conversation, in plain words and sentence case, with no quotes, emoji or full stop. Reply with the title only."* replaces only that draft, never a name someone chose; `cleanTitle` keeps one line, drops control, format and symbol characters (emoji), surrounding quotes and a trailing full stop or exclamation mark, and cuts at 80 characters.
11. **Drain** the queue (busy mode `queue`) and release the worker.

### 5.6 Context assembly (cache-stable layout)

```
[tools]   core tool specs from ToolRegistry.ForRun, sorted, frozen per epoch       ← breakpoint 1 (ToolsCache)
[S1]      CONSTITUTION.md (embedded, versioned) + admin standing rules
          + <team-memory> (TEAM.md, read live on every call; omitted when empty)
[S2]      persona (SOUL.md) · facts (principal display name, kind ("a teammate in {App}: {Name}",
          "shared channel ({title} on {App})"), timezone, channel,
          sandbox available, saved keys (used only by Bothy's own connections,
          never by commands or files; "[redacted]" marks a hidden one),
          connected MCP servers when tool_search is offered,
          model window, profile) · USER.md · MEMORY.md ·
          pinned instructions · skills index                                         ← breakpoint 2 (CacheBreak)
[M0]      <context-summary epoch="N"> … </context-summary>   (when epoch > 0)
[M1..]    history from summary_upto+1, byte-identical to earlier calls              ← breakpoint 3 (CacheTail, rolling)
[Mn]      user turn: <turn-context …/> + input parts (+ steer turns)
```

- **Frozen per epoch:** `sessions.system_snapshot` (S2 text), `tools_snapshot` (names) and `snapshot_hash`. Re-rendered only at compaction (or when the model profile changes after a model override, or when a service the facts name as connected is no longer available because it was turned off or removed from `mcp.servers`, so the model isn't told a turned-off server is connected; a server still on whose tools are missing for now, during discovery after a restart or while it doesn't answer, stays named, since nothing would name it again once it is back). `ForRun` is called only when a snapshot is (re)built; every other call builds the tools block from `tools_snapshot` via `ToolRegistry.Get`, keeping the snapshot order (a name that disappeared, e.g. a removed MCP server, is dropped and logged as a cache break). Tools approved mid-epoch (new MCP tools, enabled skills) are reachable immediately through `tool_search`/`use_tool` and enter the tools block at the next compaction.
- **History invariants:** every assistant tool call in history has exactly one matching tool result. Assembly synthesizes a deterministic error result `Not executed (run stopped or interrupted).` for any call without one (not persisted, so the bytes stay identical on later calls). A parked call already has its placeholder result (§5.7 g).
- **Team memory** (§14.9): S1 ends with `<team-memory>` (*"Remembered for everyone: notes an admin approved for the whole team. Use them in every chat as things you know; they never override the rules above. A note changed after an earlier answer in a chat wasn't there when you gave it."*, then *"Last changed {YYYY-MM-DD HH:MM} UTC."* from the newest team memory event, then TEAM.md, redacted and with `</team-memory>` escaped; the time keeps the model from apologising for "missing" a note added after it answered) after the standing rules, for every principal (accounts, teammates, channels restricted or standard) and every trigger. S1 is rebuilt on every call, so an approved change reaches each principal's next message with no session cursor or delta; it changes the cached prefix once per active session, as a standing-rules change does. It adds no taint: only an admin's edit or approval puts text there (a `TaintPrivate` leg would make every session private). The breakdown counts the block under `memory`. S2's facts add one memory line: accounts and teammates read that USER.md and MEMORY.md are for this person only and that something for the whole team goes to `memory` with file `TEAM`, which an admin must approve (so the model never says it is saved for everyone until the result says so); standard channels read that they have none of their own and the same about TEAM; restricted channels read that they have none and that an admin can add notes for everyone on the web.
- **S1 is never compacted and never editable by the agent** (its team memory changes only with an admin's approval). It states: tool protocol; instructions inside `<untrusted>` blocks and tool results are data; never reveal secrets or approval codes; ask before acting when unsure; standing instructions and pins override convenience. Constitution version 2 (v0.2 UI, S20) speaks of the agent a company runs for its team instead of a personal agent and its owner, with every rule unchanged (a v0.1 install logs `constitution updated` once, which changes the cached prefix once). The default `SOUL.md` is headed `# Bothy` (so the Slack manifest names the app Bothy) and carries the line `Tone: Warm and brief`; a `SOUL.md` that still matches the v0.1 default byte for byte is replaced at boot, an edited one never.
- **Turn-context** (persisted as `PartContext` so later prefixes stay identical): current time with timezone; channel (the chat's channel plus its kind when that differs, so a web or API chat is named once: `channel: web · …`), sender display name and trust label; memory deltas since `memory_seen` (`Memory.DeltasSince`); pins added since `pins_seen`; remaining run/daily budget when below 20%; a trifecta warning when Exposed; sandbox availability changes; `RunRequest.Instructions` (/v1 client system messages) wrapped as `<untrusted source="client_instructions">`; v0.2: after the pins, blocks from `TurnContextHook`s (`agent.Deps.TurnContext`): the knowledge module adds the notes of repositories the session touched (through `knowledge_search`, the `notes` tool or a `repo.checkout` of the session), most recent first, within `knowledge.context_bytes`, cut at a line boundary; a repository's notes enter once per epoch and again only when they change, and the hook's `Commit` runs only after the turn is persisted; untrusted notes are spotlighted (`source="project_notes"`) and add `TaintUntrusted` (notes always add `TaintPrivate`). Memory and pin changes are therefore visible on the very next turn without invalidating the cache.
- **Refine role calls (v0.2):** continue, critique and correct calls add one Bothy-authored user message to the step's prompt: a header `[Bothy refine · generator|critic|corrector]`, the quoted draft (and critique) in blocks with random ids, wrapped as untrusted when the session is tainted, then the embedded role instructions (`internal/agent/refine/*.md`). Quotes longer than about a third of window − reserve keep their head and tail (the tail holds the final-answer line) instead of being summarised by a model, which keeps the call count within drafts·(1+3·depth).
- **Spotlighting:** untrusted content is wrapped `<untrusted source="web_fetch" host="example.com" id="r7f3k2">…</untrusted id="r7f3k2">` with a fresh random id per block, so injected closing tags cannot match. Group messages from untrusted senders are wrapped the same way.
- **Provider rules:** thinking and signature parts whose `SigKind` differs from the target client's (its provider kind, or `openai_responses` for the OpenAI Responses client) are stripped, and the Responses client also replays only signatures whose provider id matches its own; images are dropped (with a note) for non-vision models; consecutive messages of the same API role are merged by adapters; Ollama gets `NumCtx` = the effective window.
- **Token estimate:** bytes/3.6 × per-model EWMA correction from reported usage. `Breakdown` per region, scaled so its sum equals reported input.
- **Model profiles** by effective window (`ModelInfo.Context`):

| Window | Profile | Constitution | Core tools | Skills index | Tool output cap | Compaction trigger | Reserve |
|---|---|---|---|---|---|---|---|
| ≥ 32K | standard | full (~900 tok) | full set (≤ 1.5K tok) | ≤ 600 tok | `tool_output_tokens` (4000) or 10% of window | 0.70 | min(8192, 12% window) |
| 16K–32K | compact | short (≤ 400 tok) | ≤ 8: `shell`, `fs_read`, `fs_write`, `web_search`, `web_fetch`, `memory`, `tool_search`, `use_tool` | names only | 1000 tok | 0.60 | 2048 |
| 8K–16K | degraded | short | ≤ 5: `shell`, `fs_read`, `fs_write`, `web_search`, `memory` | none | 500 tok | 0.55 | 1024 |
| < 8K | text-only | short | none (banner "model too small for tools") | none | — | 0.55 | 1024 |

  Standard core set: `fs_read`, `fs_write`, `fs_list`, `shell`, `python`, `web_fetch`, `web_search`, `memory`, `session_search`, `message` (only when the run has a chat), `skill_load` (when any skill is enabled), `tool_search`, `use_tool` (both only when a lazy tool is reachable for the run). Always lazy (reached via `tool_search`/`use_tool`): `fs_delete`, `net_allow`, `skill_read`, `schedule_*`, every MCP tool unless its server's `core` globs pin it. v0.2 (`core.Gated` tools): `repo_list` is core while some repository is allowlisted and the other GitHub tools (`repo_checkout`, `repo_changes`, `repo_open_pr`, `github_issues`, `github_pr`, `github_comment`, `github_runs`) are lazy; `knowledge_search` and `notes` are core while the run's principal has an indexed repository and `knowledge.enabled` is on; `release_deploy` (message trigger only) and `release_status` are core while `releases.environments` is not empty. A session's frozen tool snapshot keeps the tools it was built with, so gated tools appear in sessions snapshotted after the gate opens (or after the next compaction). Tools filtered out by sandbox state, trigger, role or principal kind are absent from both.

  Acceptance: a 16K model's assembled prompt never exceeds window − reserve (§12, M3).

### 5.7 Tool call pipeline

For each tool call:

a. **Resolve** the tool in this run (`ToolRegistry.Available`). `use_tool{name,args}` is never planned or executed itself: the runner unwraps it here and every later step (validation, Plan, policy, `tool_calls.tool`, audit) uses the target tool and its args. `tool_search{query}` is an ordinary `read` tool (Action kind `search`) whose Exec calls `Registry.Search(query, env, env.Selection, 8)` and returns name, description and schema per hit. Unknown or unavailable → error result *"Unknown tool X. Use tool_search."* (counts as a step; `tool_calls.status=invalid`).
b. **Validate** args against the JSON Schema (`google/jsonschema-go`, already a go-sdk dependency). The registry does this inside the tool `ToolRegistry.Get` returns, before `Plan`, and wraps violations in `ErrInvalid`; the runner does not validate again. Invalid → error result with the schema error (`tool_calls.status=invalid`).
c. **Plan** → `Action` (pure). Plan error → error result. Insert `tool_calls` (status `planned`), publish `run.tool_call`.
d. **Loop check:** `RunMeter.ObserveTool(tool, fnv64(canonical payload), 0)` pre-check (§5.13).
e. **Decide:** `Policy.Decide(PolicyInput)` (§5.8).
f. **Deny** → result *"Denied by policy (<rule>): <reason>"*; audit `tool.denied`.
g. **Ask** → `Approvals.Request` (card rendered from the Action, §5.9). Run status `waiting_approval`, `RunMeter.Pause()`, **release semaphores**, `Approvals.Await`:
   - `allow_once` → continue.
   - `allow_session` → `Policy.NarrowGrant(action, principal, session)` + `Policy.Grant`, continue. For a scheduled run (schedule or heartbeat trigger) the grant is scoped to the schedule instead (`scope: schedule`), since its next run starts in a new chat; its button reads "Allow for this schedule" on the web and in chat apps.
   - `allow_always` → `Policy.NarrowGrant(action, principal, GrantPrincipal, "")` + `Policy.Grant`, continue. The grant is listed and revocable under Settings → Grants and audited as `grant.create` with `via: card`.
   - `deny` → a result that says who decided, read back from the approval (`Approvals.Get`): *"The user denied this action."* when the run's user denied it; *"An approver, “{name}”, denied this action."* when someone else did (an admin, another approver; display name, else username, quoted like the reason because anyone can set their own name); *"The user cancelled the request before it was approved."* when the requester of a multi-approver approval, or the chat sender whose message started the run, withdrew it. A chat approver who denied is named by their chat app's display name, else handle (*"An approver, “Maya Chen”, denied this action."*). The approver's reason (`Approval.Comment`) follows as *"Their reason: “…”"*, on one line, at most 300 characters, with hidden characters removed and quotes and angle brackets replaced so it cannot close the quotation or open a tag. Without a recorded decider the result is *"This action was denied."* A resumed parked call records its denial the same way.
   - timeout with `deny` → result *"Nobody answered the approval in time, so this action did not run."* (a multi-approver request with some approvals: *"Only {n} of the {k} approvals needed came in before the request expired, so this action did not run."*, likewise for a parked run, and its audit row reads "Didn't get enough approvals in time to …"); the card is updated to "Expired".
   - timeout with `park` → approval `parked` (park_until = now + `park_ttl`, 24 h), run `parked`, `run.parked` published, worker released, user told "Bothy stopped waiting. Approve within 24 h to let it carry on." (the time is `park_ttl`) The approval parks before the run row does, so after persisting `runs.status=parked` the runner re-reads the approval and resumes the run at once if it was resolved meanwhile (`Resume`'s compare-and-swap keeps the action to one execution). At park time the runner persists a placeholder tool result for the call (*"Parked: waiting for approval of <card title>. It has not run."*) so the session history stays valid for providers while other runs use the session. When resolved, `Approvals.OnResolved` → `Runner.Resume(approval)`: the **same run** goes back to `running` in the session worker (published as `run.started` with trigger `resume`; `runs.trigger` keeps its original value), re-reads the persisted Action and executes it exactly once (allowed) or records the denial. The outcome is appended as a user-role message with provenance `system`: `<approved-action tool="…" call="…">` + result (spotlight-wrapped when untrusted), or the denial text; then the loop continues. Events go to the bus and to a new sink: `Hub.StreamTo(schedule.Deliver or PrincipalChat, "final")` for scheduled runs of teammates and channels, `Hub.StreamTo(session.Chat, "stream")` for channel sessions, the bus only for web. If the session has another active run, the resume is queued behind it. Unanswered parked approvals expire → run `stopped` with notice.
   - API runs (`OnApproval: deny`, default) never wait: result *"Requires approval, unavailable via API."* With `wait`, they wait up to `api_wait` (120 s) and then deny; they never park.
   - Re-acquire semaphores, `RunMeter.Resume()`.
h. **Execute** with a per-tool timeout context (`Manifest.Timeout`, default 120 s; exec tools use `sandbox.exec_timeout`). Before every exec (not once per run) the runner calls `Sandbox.Ensure`, so a box the idle reaper removed during an approval wait is reopened, then `Egress.Bind(boxKey, {run, callID, user, role, trigger, schedule, interactive, chat, context, provenance})` on it; when the exec returns it calls `Egress.Unbind(boxKey, callID)`, which ends that call's allow_once admissions and holds (§2.8). After every tool call it takes `Egress.TakeActivity(boxKey)` (§5.3). `tool.output` chunks (≤ 8 KB, ≤ 10/s, tail kept) are published for shell.
i. **Post-process:** `Redactor.Redact`; truncate to the profile cap as head 60% + tail 40% with `[… N bytes elided; full output at .bothy/out/<tcl>.txt]` (full redacted output written to the workspace, `FullRef`); `Untrusted` from manifest taint, box activity or a workspace marked untrusted (§5.3); `Policy.NextTaint` → persist if changed (also for a stopped call whose partial result is persisted, such as a killed exec's stdout). Persist the tool-result message, finalize `tool_calls`, publish `run.tool_result`, `RunMeter.ObserveTool(…, resultHash)`, audit `tool.exec` (tool, fingerprint, verdict, status; never content).

### 5.8 Policy decision procedure (M4)

`Decide` evaluates in this order and stops at the first verdict. **F = floor** (cannot be overridden by rules or grants).

| Step | Check | Verdict |
|---|---|---|
| F1 | Tool not available for this trigger, role, principal kind, sandbox state or kill switch | deny |
| F2 | `SideEffect == financial` | deny, unless a config rule sets financial → ask (admin); never auto |
| F3 | `SideEffect == admin` (self-modification: schedules, config, skills, MCP, policy, auth; team memory: a `memory` call with `file: "TEAM"` is admin-class whatever its Action declares, `actionClass`) | ask, `AdminOnly` approver, no allow_session; **deny** for `schedule`, `heartbeat`, `api` triggers; team memory: also deny when the session has `TaintUntrusted` and `memory.tainted_writes: deny` (`floor:tainted_memory`), and the card carries *"This chat has seen content from outside your team. Check that a person asked for this."* when the session holds outside content but is not Exposed (Exposed gets the usual exposure warning) |
| F4 | Restricted channel and `SideEffect > read` | deny |
| FR | v0.2 release floor (`floor:release`), for tool `release_deploy` and any Action of kind `release`: deny for `schedule`, `heartbeat` and `api` triggers, for channels (*"deploys can't be requested from a channel, where who asked isn't clear"*), for a payload that does not match its `releases.environments` entry (repo, workflow, inputs, `ref_pattern`), and when fewer enabled users than needed can approve besides the requester. A teammate's deploy needs one more vote than the environment's `required` (`Requester` ""), since nobody on the web asked | otherwise ask with a multi-approver `ApproverReq` (`Users`, `Roles`, `Required`, `Requester`); rules can only deny, grants are never consulted, only `[allow_once, deny]` are offered |
| F5 | Session Exposed and `SideEffect ∈ {external_send, irreversible}` | ask, no allow_session; exception: an `AllowWhenTainted` recipient grant matching `Action.Recipient` exactly → auto |
| F6 | Run started by a `ProvUntrusted` message and `SideEffect ≥ local_write` | ask (the team's approvers; no grant choices) |
| F7 | `Kind == memory` and session has `TaintUntrusted` (v0.2: also project-notes edits by the `notes` tool, which carry Kind `memory`, SideEffect `local_write` and a diff on the card; team memory is decided at F3, which already asks an admin) | ask (`memory.tainted_writes: ask`) or deny (`deny`) |
| F8 | `web_fetch` in an Exposed session for a URL not in `session_urls` (from search results or user messages) | ask |
| F9 | `net_allow` | ask (never auto, never via rules); allow_session grants exactly `host:port` |
| FA | v0.2 `floor:always_ask`: the tool's `Manifest.AlwaysAsk` (`repo_open_pr`) | ask with `[allow_once, deny]` whatever the preset; a deny rule still denies; no grant answers it, `AllowWhenTainted` included; the first-contact warning is carried onto the card, also when an earlier floor (e.g. F5) asks first |
| FP | `floor:teammate`, last of the floors: a teammate's (person principal's) action above `read`, except the `memory` tool on their own files (TEAM is decided at F3) and `repo_checkout`'s `local_write` (checking out keeps edits; discarding them is `irreversible` and asks) | ask the team's approvers with grant choices; an earlier approval's grant answers it, a deny rule still denies, a rule `auto` never lifts it; a first contact adds its warning |
| R | Config `policy.rules`, first match (tool glob, argv0, domains, paths, JSON-pointer `match`, side effect, triggers) | rule verdict (rules can make things stricter or auto, but not past floors) |
| G | Active grants for (principal, session, schedule) whose kind/tool/pattern/value match the Action and `MaxSE ≥ SideEffect` | auto (`grant:<id>`, `Grants().Use`) |
| C | First contact: `send`, `mcp` with a mapped recipient, or `http` with a non-GET method, to a recipient/domain that is not the run's originating chat and not seen in an active grant, `egress.allow`, or an earlier allowed approval of this principal (`Approvals().Seen`) | ask (presets careful and balanced) |
| D | Class default from the preset, overridden by `policy.defaults`; its reason names the preset and class in words (*"the Careful safety setting asks before Bothy can send data out"*), or, when `policy.defaults` sets the class, *"the setting for this kind of action asks before Bothy can …"* | auto / ask / deny |

F1 is enforced in two places: `Decide` checks trigger, role, principal kind and the kill switch (`meta.paused`, read failures count as paused); sandbox availability is enforced by `ToolRegistry.Available` at §5.7 a, so the runner never reaches `Decide` for an exec tool without a sandbox.

Presets (`policy.preset`):

| Class | careful (default) | balanced | hands_off |
|---|---|---|---|
| read | auto | auto | auto |
| local_write | auto | auto | auto |
| external_send | ask | auto (first contact still asks) | auto (no first-contact step) |
| irreversible | ask | ask | ask |
| financial | deny | deny | deny |
| admin | ask (admin) | ask (admin) | ask (admin) |

Shell and python are `local_write` (they run in the box; their network is separately gated by the proxy), so they are auto in every preset unless a rule says otherwise. The exception is an exec command that deletes files or destroys their content, which the policy decides, shows and grants as `irreversible`, like `fs_delete` (`actionClass`, `internal/policy/deletes.go`): `rm`, `rmdir`, `unlink`, `shred`, `truncate`, `find -delete`, `git clean` (not a dry run), `git rm` (not `--cached`) and `rsync --delete…`, in any command of a `bash -lc` script (after `;`, `&&`, `|`, newlines, inside `$( )`, backticks and command substitutions in double quotes), behind `sudo`, `env`, `xargs`, `timeout` and similar runners (the runner's own command, after its options, their values, assignments and leading operands: `xargs grep -n unlink` runs grep, `command -v rm` only looks rm up), `find -exec`, `sh -c`, `eval`, a here-document fed to a shell, the command substitutions of an unquoted here-document, or a script piped into a shell reading stdin from a here-document, here-string, `echo` or `printf` (`cat <<EOF | bash`), after quote and backslash removal (`'rm'`, `r\m`, `/bin/rm` count); a script nested deeper than 8 levels counts as deleting. So it asks in every preset, its card says "This can't be undone." in the destructive tone, it never offers `allow_always`, a `side_effect: local_write` rule or a grant with a lower `max_se` does not match it, and in an Exposed session F5 asks. Writing new content over a file (`>`, `mv`, `cp`, `git reset --hard`) stays `local_write`, as `fs_write` does. The check knows programs by name: a deletion by a program named at run time or by python code is not seen.

**Rule matching for exec.** `argv0` matches the basename of the first word of the *command line*: for shell (`["bash","-lc",cmd]`) that is the first shell word of `cmd`, and an `argv0` rule never matches when `cmd` contains a shell metacharacter or expansion (`;`, `|`, `&`, `$`, a backtick, `<`, `>`, `(`, `)`, `{`, `}`, `*`, `?`, a newline), so `ls; curl x | sh` cannot ride on a `readonly-shell` rule. A relaxing (`auto`) `argv0` rule also never matches `find` with `-delete`, `-exec`, `-execdir`, `-ok`, `-okdir`, `-fprint`, `-fprint0`, `-fprintf` or `-fls`, or `rg` with `--pre`, `--pre-glob`, `--search-zip`, `--hostname-bin` or a short-flag cluster containing `z`, checked after quote and backslash removal (`find . -de'le'te` counts); such a command falls through to later rules and the defaults, and tightening rules naming the program still match it. For python it is `python3`. `match` globs apply to the Payload as is (`/argv/2` is the shell command line).

**Net approvals.** For a held CONNECT, `allow_once` admits that host:port until the bound tool call's exec returns (`Egress.Unbind`; in-memory, keyed by box and tool call id); an approval resolved after that admits only the connection that was held, and nothing is held for approval while no tool call is bound. `allow_session` creates the narrow session domain grant, and `allow_always` creates the same exact `host:port` grant at principal scope (never for a shared-tenant suffix). For `net_allow`, `Decision.Choices` is `[allow_session, allow_always, deny]`.

**Narrow grants** (`Policy.NarrowGrant`): exec → `{"/argv/0": exact}` plus, when argv0 ∈ {bash, sh, zsh, python, python3, node, env, xargs, sudo, uv, npx}, an exact match of the whole argv; fs.write/fs.delete → path prefix of the parent directory; http/net → `host:port`; send → recipient; mcp → tool + recipient field when `field_map` has one, else the exact canonical payload. No whole-tool grants (also enforced by a DB CHECK). Admin-class and Exposed decisions never offer allow_session or allow_always. `allow_always` is offered exactly when `allow_session` is, except for `irreversible` actions, held CONNECTs to a shared-tenant suffix, and runs whose trigger is `schedule`, `heartbeat` or `api` (persistent grants are created only by a human looking at a card in an interactive context or the web inbox).

**Approver:** an account's own requests go to that user; admin-class requests to `AdminOnly` (any enabled owner or admin, on the web); a channel's or teammate's requests to `Team`: any enabled owner or admin on the web, or a teammate an admin marked **Can approve** pressing a recorded chat card while they are admitted (§5.1 step 6). Chat approvers never decide admin-class requests or release votes (they get a link-only card for a team's admin-class request, except in the requester's own DM, which gets only the waiting line). The person whose message started a team run may always withdraw it. A chat approver's grant (allow_session, allow_always) is created by them (`grants.created_by = "chat:<app>:<id>"`, audited as the chat actor), never `allow_always` for a channel (only an admin on the web can). A team principal may have at most 5 open team approvals; the next request fails with `ErrBusy` (*"Bothy is still waiting on earlier requests from this chat"*). Approvals are resolved only from the web UI with a cookie session or from a recorded chat card; bearer tokens can never resolve approvals (an API client must not approve its own run's actions).

**Plain warnings (v0.2 UI, S2).** `Decision.Warnings` are sentences in the UI's voice (docs/design/ui.md §14), with every identifier passed through `inline()` (hidden characters and line breaks made visible): first contact *"Bothy hasn't sent anything to {recipient} before."*, *"Bothy hasn't sent data to {host} before."*, *"This is the first pull request Bothy opens in {repo} for you."* (*"… for this group chat."* for a channel, *"… for {Name}."* for a teammate; first contact is per principal, so another person's pull requests in the repository don't count), and *"Bothy can't tell who this goes to."* / *"Bothy can't tell which website this sends data to."* when the recipient or host is unknown; Exposed *"This chat has seen private information and content from outside. Check carefully before letting anything leave."*, on every ask in an Exposed session except file and memory actions (kinds `fs.read`, `fs.write`, `fs.delete`, `memory` below `external_send`), where nothing can leave: discarding edits or deleting a file asks at F5 because it can't be undone, and its reason says so; untrusted origin *"This request came from content Bothy read (a web page, repository or message), not from a person."*

**Egress holds.** The proxy builds its approval with `Policy.Decide` on the `net_allow` spec and an Action `{tool: net_allow, kind: net, side_effect: external_send, host, port}` using the `EgressBinding` (user, role, trigger, chat, provenance); it then sets `Decision.Timeout = egress.hold_for_approval` and `OnTimeout = deny` and offers `[allow_once, allow_session, allow_always, deny]` (allow_always subject to the §5.8 narrow-grant rules). For a run started by a `ProvUntrusted` message, F6 gives `[allow_once, deny]` plus the untrusted-origin warning. The run stays `running` (the exec is blocked inside the box) and keeps its semaphores for that bounded wait.

### 5.9 Approvals: cards, routing, timeouts

- **Card rendering (M4)** from the Action only: title from `Manifest.Title`; fields such as Command (argv joined with shell quoting, mono), Folder, URL (full, host shown as punycode and bolded), Recipient (system-resolved label), MCP server/tool with pretty-printed args, Network; Websites (every `Action.Domains` entry of a non-HTTP, non-net action, mono), except on the GitHub tools' cards (`repo_checkout`, `repo_open_pr`, `github_comment`), which name the repository and whose host is Bothy's own GitHub API setting rather than anything the model chose, so a pull request card is Branch · Title · Changes as in ui.md §8.2; `Diff` (unified, ≤ 64 KB, web shows all, chat shows ≤ 3 KB; an `fs_write` card also shows the written content, hidden characters made visible, whenever its diff is empty, omitted (over 4 MB), binary, truncated, or a path contains a line break, so no card approves content nobody saw; a long one is deny-only in chat); Warnings (trifecta, first contact, untrusted origin); Context line; Choices; Expires; WebURL. Bidi controls and zero-width characters are rendered visibly (`⟨U+202E⟩`). Model prose never appears; any model rationale is shown in the web UI only, collapsed under "Model's reason (unverified)".
- **Plain copy (v0.2 UI, S2; `internal/policy/copy.go`).** `renderCard` also writes, from the Action, the tool's spec and the decision only (model prose has no way in), the words every surface shows: `question` (*"Delete notes/staging.md?"*), `consequence` (*"This can't be undone."* for irreversible actions; *"This updates {environment} for everyone using it."* for deploys; *"Money will leave your account. This can't be undone."* for financial ones), `primary_label` (allow_once's verb, e.g. *"Delete file"*, else *"Allow for this chat"* when only allow_session is offered), `ing` (*"deleting notes/staging.md"*, as in "You approved …"), `kind` (`action`; `pull_request` for `repo_open_pr`; `deploy` for `release_deploy`; `review` for project-notes edits) and `tone` (`destructive` for irreversible and financial actions and `schedule_delete`, never for deploys, whose safeguard is their approvers; otherwise `default`). Each shipped tool has its own copy, with fallbacks by action kind for MCP tools (the server's name and the scalar arguments its pinned schema declares, with humanised labels), admin-class and unknown tools. Field labels are plain (File, Content, Changes, Command, In, Script, Website, To, Message, Environment, Version, Workflow, Inputs, Repository, …); the v0.1 Tool, Requested by and Approvers fields are gone (who asked and who approves come from `Approval.Requester` and `Approval.Approvals`). `Code` is left out only where a field already shows the exact action (the shell command, a message's text, a `github_comment` body). A value inside a sentence goes through `inline()`, so it can never break the line or hide characters. These fields are `omitempty`: cards stored before v0.2 lack them, and renderers fall back to the title.
- **Routing (M9):** the hub subscribes to `approval.requested` and delivers by principal and approver; every approval is always in the web inbox (SSE; `AdminOnly` and `Team` approvals reach every admin through the admin-broadcast copy). Chat approvers are the teammates marked Can approve whose chat app is on and person space enabled, who aren't outsiders; a verdict older than `ApproverFresh` (2 h, Slack and Discord) gets a link-only card instead of a full one.

  | Principal | Approver | Chat approvers' DMs | The chat that asked (its own app only) |
  |---|---|---|---|
  | account | any | none: accounts hear on the web only (`policy.approval.notify` is not used) | none |
  | teammate or channel | Team | the full card (kind `card`) to each fresh chat approver, a link-only card (kind `link`) to stale ones; an approver's own request from their own DM gets the card there once and no waiting line | *"Waiting for approval: {question without its ?}."*, or *"This needs an admin's approval in Bothy."* when no approver got a full card; both with **Cancel request** |
  | teammate or channel | AdminOnly | a link-only card to each chat approver | *"This needs an admin's approval in Bothy."* with **Cancel request** |
  | teammate | multi-approver (release) | none | *"Waiting for {N other people\|someone else} to approve {ing}."* with **Cancel request** |

  A **link-only card** says *"A request from chat apps needs an admin. Admins can approve it in Bothy."* with an *Open in Bothy* button to `Card.WebURL` (without a public URL: *"…can approve it in Bothy under Approvals."* and no button): no question, no context. Each message is recorded with its kind (`Approvals.AddMessage`: `card`, `link` or `notice`; link cards and waiting lines count as truncated); a waiting line only withdraws. `resyncCards` delivers pending approvals with no recorded message at all. A card that does not fit the channel (MaxLen or button limits) is sent **truncated**: Deny button + "Open in web to approve" link button only. `Card.WebURL` is `server.public_url + /approvals/<id>`; with no `public_url` it is empty, the link button is omitted and the text says "Open Bothy's web UI → Approvals" (platforms reject loopback URLs in buttons).
- **Chat card format (v0.2 UI, S2; `internal/channel/cards.go`):** the question first, in bold (link-like words such as file names go in code spans, never tappable; Telegram cannot nest code in bold or italics, so its renderer closes the formatting before a code span and reopens it after); the consequence; on a team card, who asked as the platform's own mention, so the approver sees the real profile whatever name was chosen (Slack *"<@U123> asked on Slack."* or *"… asked in #eng."*, Discord *"<@id> asked on Discord."*, Telegram a `tg://user` link with the name, plus *(@handle)* when there is one; when a schedule started the run nobody asked, so it reads *"From #eng on Slack."* or *"From <@U123>'s schedule on Slack."*, from `requester.scheduled`); the card carries that id in `OutMessage.Mentions`, and the Slack, Discord and Telegram renderers turn a `slack://user`, `discord://user` or `tg://user` link into a mention only for the ids listed there (Telegram renders no other `tg://` link), so the same link in model text, or in a page or message Bothy read, stays a plain link and can't ping anyone from Bothy's name; *"{Name} asked."* on multi-approver cards; each warning as a line starting with the warning sign `⚠`; the plain fields; an approvals row on multi-approver cards (*"**Approvals:** 1 of 2 approvals"*, each vote with the voter's name and reason, *"Waiting for X, Y or any admin."*); the Code block only where it is the action itself (a command, a script, a file's content) or where the fields show only some arguments (MCP, admin-class and unknown tools, and cards without plain copy), so a built-in tool's action JSON (hashes, base commits, as for `repo_checkout` and `repo_open_pr`) stays behind the web UI's Details; the diff block; the context line in italics, its link-like words in code spans; the expiry in the web's words (*"Expires in 14 min."*, *"Expires in 2 h."*, *"Expires in less than a minute."*). The context line of a team card is whose chat it is (*"Maya Chen's direct messages on Slack"*, *"#eng on Slack"*), never the session's title. Buttons: allow_once shows the card's verb, then *Allow for this chat*, *Always allow*, *Deny* (a teammate's card: *Allow in {Name}'s chat*, *Always allow for {Name}*, the name cut at 24 characters; a channel's card has no grant buttons); the danger style only when `tone` is destructive, and Deny is never styled. A decided card is edited to its decided line, which replaces the question: ✓ when approved, ✗ when denied or expired, and the card's reader (the identity whose DM it is) named "You", without where they decided, e.g. *"✓ You approved deleting `notes/a.md`."*, *"✗ Sam Patel denied deleting `notes/a.md` in Slack. “reason”"*, *"✓ Ada and you approved deploying acme/web to staging."*, *"✗ Nobody answered in time, so Bothy didn't go ahead."*, and *"✗ Cancelled by Mark."* when the person who asked withdrew. A grant read outside the chat the request came from names whose chat it covers: *"✓ You approved writing `notes/a.md`, and similar requests in Maya Chen's chat."* (*"… in #eng."* for a channel), *"… and similar requests for Maya Chen from now on."*; in that chat it is *"this chat"*. A link-only card becomes *"Decided in Bothy."* (*"Nobody answered in time."* when it expired), never what was asked or the approver's reason. The recorded waiting line is edited to the outcome too, naming the decider and where: a DM keeps the approver's reason, a group never shows it, and in their own DM the person who asked reads "You" (*"✗ You cancelled the request."*); a request stopped with its run reads *"✗ Stopped."*; when the line could not be sent, the outcome is posted for allowed, denied and expired requests.
- **Reasons:** a decision or vote may carry a reason (`ApprovalResolveReq.comment`, `Resolution.Comment`). The deciding approver's reason is stored on the approval (`approvals.comment`, `ApprovalRepo.SetComment`, `Approval.Comment`), sent with `approval.resolved` (`comment`) and, on a deny, quoted in the denied tool result the agent reads (§5.7 g), so its reply can pass it on in any channel; each voter's reason is stored with the vote (`approval_votes.comment`, `ApprovalVote.Comment`) and shown in `Approval.Approvals.votes` and `GET /api/approvals/{id}/votes`.
- **Button data:** `a:<approval id>:<o|s|a|d>:<mac>` (a = allow_always), ≤ 64 bytes; MAC = HMAC-SHA256(meta `approval_mac_key`, id|choice) truncated to 8 base32 chars. Agent output and the `message` tool can never carry buttons. Approval ids and MACs never enter model context.
- **Resolve** is a CAS (`ApprovalRepo.Decide` from pending|parked). Double resolves → exactly one wins; the loser gets "Already decided". All delivered cards are edited to the outcome.
- **Multi-approver approvals (v0.2, release floor):** an approval whose `ApproverReq` sets `Users` or `Roles` (and not `AdminOnly`) needs `Required` distinct approvers (0 means 1), never the requester (roles rank owner > admin > member; a role admits higher ones). `Resolve` records a vote: the status check, the vote, the recount of still-eligible approvers (disabled or demoted voters stop counting) and the decision run in one transaction. The requester's allow is refused, but the requester may deny to withdraw; one vote per user; one deny denies; the k-th allow approves; every vote is audited (`approval.vote`, ok or denied), except the requester's deny, which withdraws the request and is the one `approval.decide` row with `withdrawn: true`, and a vote that does not decide publishes `approval.voted`. Votes are cast on the web; a teammate's deploy shows only a waiting line in their chat (cards sent to chat apps before 0013 still show "N of K approvals" and are edited after each `approval.voted`). Deploy cards, on the web as in chat apps, carry Environment, Version (the ref), Workflow and Inputs (hidden characters made visible), name the repository in the question, and have no context line, since the requester's session title is private to them; who asked and who may approve come from `Approval.Requester` and `Approval.Approvals` (v0.2 UI, S4: `required`, `votes` with names and reasons, `eligible` = enabled users other than the requester who may vote now, leaving out admins and owners when `any_admin` covers them, `any_admin` = a roles entry of admin or member), filled by `Approvals.Get`, `Pending`, `Recent` and `Resolve` and on `approval.requested` and `approval.voted`. `Approval.Requester` is set on every approval (`Approver.Requester`, else `Approver.User`, else the run's user). Timeouts and parking are unchanged. The card of a deploy request warns its approvers when another deploy to that environment is still running (*"A deploy to {env} is already running."*, *"{n} deploys to {env} are already running."*) or waiting for approval (*"Another deploy to {env} is waiting for approval."*, *"{n} other deploys to {env} are waiting for approval."*).
- **Pull request cards (v0.2):** `repo_open_pr` is an `ActSend` action with Recipient `github:<owner>/<repo>` (first contact per repository; recipient grants); its Diff starts with a format-patch style header (Repository, `Branch: head → base`, Title, the description indented 4 spaces and clipped to 1 KB) followed by the full git-style diff. The payload carries `base_sha` and each file's path, op and sha256 (not the content); Exec re-hashes and sends only bytes whose sha256 matches; a card over 64 KB is refused at Plan. `github_comment` is `ActSend` with the same recipient.
- **Timeouts** (`policy.approval`): interactive `15m` → `deny`; scheduled/heartbeat `2m` → `park` (`park_ttl` 24 h); API per token. The expiry sweeper (`policy.Approvals.Run`, every 30 s) handles due approvals after restarts. At boot the runner calls `Approvals.CancelRun` for every run it marked `interrupted` (their pending approvals become `cancelled`, cards edited); parked approvals survive.

### 5.10 Compaction

Triggered when the pre-call estimate crosses the profile trigger, on a `context_length` provider error (then retried once), after a run that ends above 60% of the window, or by `/compact`.

1. Choose the split: keep the last `keep_recent_turns` (4) user turns; never separate a tool call from its result; pinned items are not history (they live in S2).
2. Elide tool results older than the kept turns to `[elided 12KB → .bothy/out/x.txt]` before summarizing.
3. Summarize messages `summary_upto+1 … split` with the **fast** slot (fallback main) into ≤ `summary_max_tokens` (1200) with exactly these headings: `## Goals`, `## Decisions`, `## Facts`, `## Open tasks`, `## Standing instructions`, `## Files`.
4. **Validate:** all headings present, and every user sentence matched by the standing-instruction detector (imperatives such as "don't … until", "never", "always", "from now on", "stop …ing", "only … if") appears verbatim under Standing instructions. On failure, retry once with the missing quotes listed.
5. **Auto-pin** (when `auto_pin`): detected standing instructions that are not already pins become pins (`source=auto`) and a notice is posted: *"Pinned “don't act until I say” so Bothy keeps it in mind."*
6. **Fallback** if the summarizer fails twice: drop the oldest non-pinned turns until the budget fits, insert a notice *"Older messages were set aside because Bothy couldn't summarise them."* (after auto-pinning detected instructions). The run never fails because of compaction, including on 16K models.
7. **New epoch:** `epoch++`, `summary`, `summary_upto`, refresh memory snapshot (`memory_seen`), merge pins (`pins_seen`), re-render S2 and the tools block for the current registry and profile (lazy tools used at least twice in the previous epoch are promoted into the block, within the profile's cap), update `snapshot_hash`, publish `session.updated`. Old messages stay in the DB and FTS.
8. Cost is metered on the run with `purpose=compaction`.

S1 is outside compaction by construction; session taint and policy state live in the DB.

### 5.11 `/stop` and the kill switch

`Runner.Stop(session)` runs on the caller's goroutine and never waits on the worker, the LLM or a semaphore:
1. cancel the run context with cause `ErrStopped` (the provider stream aborts; tools see `ctx.Done()`);
2. `Sandbox.Kill(boxKey)` (§2.7);
3. `Approvals.CancelRun(run)` (cards edited to "Cancelled");
4. in-flight MCP calls get `notifications/cancelled`;
5. clear the session mailbox (each dropped request whose sink got `run.queued` gets `run.finished` with `stopped`); mark the run `stopped` (`stopped_by`), audit `run.stop`, reply "Stopped." in the chat; the session gets the notice *"You stopped Bothy."*
`/stop` also cancels a finished run's post-run compaction (§5.10; also cancelled by `StopAll` and shutdown, and skipped while paused), and returns true when it cancelled that or dropped accepted messages, even with no active run. If the run goroutine has not exited after 5 s it is abandoned (sink detached, later emits ignored; its queue and pending turn move to the fresh worker, §5.4). Targets: cancel path p99 < 100 ms; processes gone < 1 s.

Kill switch: `/stopall` or `POST /api/admin/pause {paused:true}` → `Runner.SetPaused(true)`, then `StopAll` (every active run stopped, every session box killed), whose count the caller reports. `Runner.SetPaused` only persists `meta.paused` and, since v0.2 UI, `meta.paused_by` (who and when; both survive restart, and pausing again keeps the first), publishes the banner `paused` (`core.PausedBanner`, naming who paused) and audits. The scheduler fires nothing while `Runner.Paused()`, which is its only kill-switch input: `POST /api/admin/pause` does not call `Scheduler.SetPaused`, an independent scheduler-only pause that nothing in the gateway uses. New runs get `ErrPaused`; read-only HTTP keeps working. From a chat app, a teammate who can approve engages it with `/stopall` (no user: the audit origin is the chat actor, the banner names "{Name} · {App}" and `meta.paused_by` stores `channel:<app>:<id>`); only admins disengage it, on the web (`POST /api/admin/pause {paused:false}`); `/resume` in chat says where.

### 5.12 Budgets

| Limit | Interactive | Scheduled | Enforcement |
|---|---|---|---|
| steps / run | 25 | 15 | `Step()` before each LLM call |
| tokens / run (input+output) | 400K | 150K | `BeforeCall` projection |
| wall time / run (approval waits excluded) | 10 min | 15 min | run deadline |
| $ / run | none | $0.25 | `BeforeCall` projection |
| $ / day | global $5.00; members and people in chat apps $2.00; overrides by username or `<app>:<id>`; chat apps together 60% of the global cap | same | `CheckDaily` + projection; 80% warning |
| heartbeat $ / day | — | $0.25 | scheduler precheck; skip when exhausted |
| sender rate | 10/min, burst 5 | — | hub |
| API token rate | `rate_per_min` (30) | — | middleware |

Unknown prices skip $ checks but not token checks, and the UI marks costs "price unknown". Local models cost $0. On a trip: run status `budget_exceeded`, a system notice naming the limit in plain words (§5.17), audit `budget.exceeded`.

- **Caps by principal:** spending is billed to the principal (`spend_daily.principal_id`; an account's principal id is its user's). A run is checked against up to three caps and the error names which one tripped (`BudgetError.Scope`: `personal`, `chat`, `global`):
  - *own*: for an account, `per_user_daily_usd[username]`, else `member_daily_usd` for a member in multi mode (owners and admins have none); for a teammate, `per_user_daily_usd["<app>:<id>"]`, else `member_daily_usd` in either mode; a channel has none;
  - *chat share*: teammates and channels together may spend `budgets.chat_share_pct` (default 60, 1–100; 100 = no separate share) of `daily_usd` (`UsageRepo.TeamSpend`), so chat apps can't use up what accounts on the web need; at `budgets.warn_ratio` (80%) admins get the `chat_budget_80` banner event (*"Chat apps have used 80% of today's $X.XX limit."*, the share actually used, action `/activity/spending`) once a day; nothing stops yet, so the v0.2 UI shows it not as a banner but as Spending's line, from `GET /api/usage/today` (`chat_spent_micros`, `chat_limit_micros`);
  - *global*: the sum over everyone (`UsageRepo.Spend(p="")`).
  `Budget.Daily` reports the smallest remaining of these. A teammate whose own cap passes `budgets.warn_ratio` gets one DM a day in that cap's figures (the `budget.warning` event's spent and limit, not `Daily`'s, which may be another cap): *"You've used 85% of your $2.00 daily limit in Bothy."*
- **Heartbeat cap:** before firing a heartbeat schedule the scheduler sums today's cost of runs of that schedule (`UsageRepo.Report{GroupBy: schedule}`, which joins `llm_calls` to `runs`) and skips the tick when ≥ `heartbeat_daily_usd`.
- **Calls outside runs** (passthrough, probes): `Budget.RecordSpend` writes the `llm_calls` row (`run_id` NULL) and `spend_daily`. Inside runs the runner writes `llm_calls` (`RunRepo.AddLLMCall`) and `RunMeter.AfterCall` writes `spend_daily`.
- **API token rate** (`rate_per_min`) is a token bucket local to `httpapi` keyed by token id (burst = max(1, rate/6)); `Budget.AllowSender` is only for chat senders.

### 5.13 Loop detection

Ring of the last 16 `(tool, fnv64(args), fnv64(result))`:
- same `(tool,args)` 3× → `LoopWarn`: a note is appended to the tool result *"You repeated an identical call 3 times; change approach or answer the user."*
- same `(tool,args)` 5×, same `(tool,args,result)` 3×, A-B-A-B over 8 entries, 5 consecutive tool errors, or identical assistant text in 3 consecutive steps → `LoopAbort`, status `loop_detected`, notice *"Bothy stopped because it kept repeating the same step. Try asking in a different way."*

### 5.14 Provider fallback (M2)

- Chain from `models.<slot>` (or the session/request override as a single-element chain followed by the slot chain).
- Per candidate, **before the first streamed event**: retryable errors (429, 408, 5xx, network, overloaded) retry once on the same candidate after `250ms·2^n ±20%` jitter, honouring `Retry-After` up to 20 s; then the next candidate. `auth` errors mark the provider `auth_failed` (banner `provider_auth`, once) and move on without a retry; billing failures count as `auth` (Anthropic `billing_error` or an `invalid_request_error` about the credit balance; OpenAI `insufficient_quota`, `credit_balance_exhausted`, `organization_spend_limit_exceeded`, `project_spend_limit_exceeded` and `organization_usage_limit_exceeded`; Gemini 402). OpenAI Responses in-stream `response.failed` and `error` events are classified like HTTP errors (codes such as `server_error` are retryable before the first event; the policy codes `bio_policy`, `cyber_policy`, `misalignment_policy_violation` and `image_content_policy_violation` are `content_filter`). `context_length` returns to the runner (compaction then one retry). `invalid_request` fails the step.
- **Bedrock (v0.2):** an auth failure on a bedrock provider (access denied, token or signature errors) also drops its cached AWS credentials, so the next call resolves them again; the `provider_auth` banner says "The <id> provider cannot use this server's AWS credentials. The doctor says what to fix." The AWS SDK's own retries are off. Converse has no tool choice `none`: `ToolChoice: none` omits `toolConfig` even with tool history, whose `toolUse`/`toolResult` blocks are then rendered as text (§14.1); `required` degrades to auto on models that reject a forced tool choice.
- Circuit breaker: 3 consecutive failures skip the provider for 60 s.
- Candidates without tool support are skipped when the request has tools. Likewise, an error wrapping `core.ErrModelUnsupported` (Chat Completions refuses function tools for the model even with `reasoning_effort: "none"`) moves to the next candidate before the first event without counting against the provider's breaker; alone in a chain (or as the last candidate) it returns as `invalid_request`.
- **Never switch after the first event:** partial text is kept, the step fails with *"Model error mid-reply."* and a Retry hint.
- `build(ModelInfo)` is called per candidate so context is re-fitted to its window and capabilities.
- Every attempt goes into `CallInfo.Attempts`; providers never retry internally, with two exceptions. Each is a capability downgrade after an unbilled 400 that arrives before any event: the request is resent once, both requests appear as one `Attempt`, and the outcome is kept in memory only, so a restart costs one more refused request.
  - **Chat Completions, tools while reasoning:** a request with tools that the server refuses because the model reasons (OpenAI's 400 "Function tools with reasoning_effort are not supported … set reasoning_effort to 'none'", matched by its key terms) is resent with `reasoning_effort: "none"`. The outcome is remembered per (provider, model) in a bounded map (256 entries, oldest dropped first; a provider's entries are cleared by a change to its config, all by a restart). Later calls with tools then send `"none"` directly, or, when `"none"` was refused too, fail as `invalid_request` on the first refusal without sending the second request. Doctor: `providers.chat_tools`.
  - **Responses, temperature:** `temperature` is never sent to a model the catalog marks as reasoning (thinking), and `top_p` never at all. A model that refuses a temperature anyway (a 400 `unsupported_parameter` or `unsupported_value` naming `temperature`, before any event) is sent once more without it and remembered (at most 64 models), so later calls leave it out. Downgrades stack (temperature, then encrypted reasoning), each kind at most once per call.
  - **Responses, encrypted reasoning:** a model that refuses `include: ["reasoning.encrypted_content"]` ("Encrypted content is not supported with this model", e.g. a fine-tune the catalog does not know as non-reasoning) is resent without the `include` and replayed reasoning, and the model is remembered (at most 64 models). `invalid_encrypted_content` (reasoning bound to another OpenAI organization, e.g. after a key change) resends without the reasoning of messages created before that moment, and that cutoff stays in the client. Both are cleared by a restart or a change to the provider's config.

### 5.15 Concurrency and recovery

- One active run per session (worker), plus the DB guard `runs_one_active`.
- Goroutine bound: active session workers (idle-reaped) + adapters (≤ 2 each) + scheduler (1) + audit writer (1) + approval sweeper (1) + egress log flusher (1) + sandbox watcher (1) + SSE connections (≤ 4 per user).
- Boot (`agent.Runner.Run`): `RunRepo.MarkInterrupted` turns `queued`/`running`/`waiting_approval` into `interrupted` and returns those runs; for each, `Approvals.CancelRun` and, for channel sessions, a notice through the hub's `Messenger` (the runner gets the hub via `SetHub` after construction, §12.1): *"Bothy restarted before it finished this task. Send “retry” to continue."*; `parked` runs stay parked; pending approvals past expiry are expired by the sweeper; boxes are reconciled (`GET /v1/boxes`, orphans destroyed).
- Shutdown: stop intake, give runs 10 s to reach a step boundary, mark the rest `interrupted`, flush audit, close the DB. An interrupted run of a channel session sends *"Bothy restarted before it finished this task. Send “retry” to continue."* to its chat through `Messenger.Send` (as boot recovery does; the run's own sink is stopped with the hub). Turns that were queued or in their coalescing window are kept as user messages, with their taint and URL provenance, followed by the notice *"Bothy restarted before it got to this message. Send “retry” to continue."*, which is also sent to their chat; their sinks get `run.finished` (`interrupted`).

### 5.16 Scheduled and heartbeat runs (M8 + M3)

- A schedule run uses the schedule's target session (`Schedule.Session`, a running conversation) or, when that is empty, a **fresh session per run** (`Runner.NewSession(principal, "schedule", nil)`, title "<schedule name> · <local date>"), so independent runs never accumulate history and each run has its own transcript. Busy target → queued; full queue → run recorded `skipped`. The run's input is the schedule prompt with provenance `user` (heartbeat: §below).
- Limits = schedule limits (defaults `budgets.scheduled`). Pre-authorized grants (`scope=schedule`) apply. `schedule_*` tools are absent (`Manifest.Triggers = [message]`), and F3 denies all admin-class actions: cron cannot create cron.
- Approvals park (§5.9). Delivery: a teammate's or channel's schedule (`deliver: preferred`, nil) posts to `Hub.PrincipalChat` (the teammate's DM, the channel's chat), or to a chat set on the schedule only when it is that principal's own; an account's schedule stays on the web (accounts have no chat), so naming a chat app for one is refused (*"deliver: an account's results stay in Bothy, so its schedules can't post to {app}; leave deliver out"*), and `scheduler.heartbeat.deliver` naming a chat app leaves the owner's check-in without one. `deliver: web` is for accounts only (*"deliver: web is only for accounts on the web; use preferred"*). Before firing a team schedule the scheduler asks `Hub.TeamActive`: a blocked teammate, one no longer in the team, a membership verdict too old to renew, or a channel turned off skip the tick without counting a failure. A delivered result opens with a line naming where it came from, since nobody asked for it in that chat: *"From the schedule “Weekly PR digest” · ask Bothy here to change or stop it"* for a teammate's or channel's schedule (the web's *"… · change it under Schedules"* and *"From your check-in · …"* remain for accounts), in italics; the final-mode sink reads the run's trigger, principal and schedule from the store. A `HEARTBEAT_OK` reply sends nothing, header included. Admins list team schedules (`GET /api/team/schedules`) and may turn them on or off or delete them; everything else is changed by asking Bothy in that chat.
- `schedule_create` plans an Action with every default resolved. With no set chat (`deliver: preferred`) it also carries `owner`, `owner_kind` and `usual_chat` for the card only: *"Bothy on the web"* for an account, *"{Name}'s messages on {App}"* for a teammate, *"{title} on {App}"* for a channel. Its result tells the model where runs will post (*"Results go to {Name}'s messages on {App}."*, *"Results stay in Bothy on the web."*). F3's reason reads *"creating a schedule needs an admin's approval"* (`schedule_delete`: *"deleting a schedule …"*; other admin-class tools: *"changes to how Bothy works need an admin's approval"*).
- Heartbeat (`scheduler.heartbeat`, off by default): one schedule of kind `heartbeat` for the owner's principal, kept in sync by the scheduler (`every` ± 10% jitter, `active_hours` in the owner's own time zone; `deliver` may be `preferred`, `web` or a channel name, and in every case the check-in stays on the web, since accounts have no chat), fast slot; a config change or the owner changing their time zone (`PATCH /api/me` calls `ResyncHeartbeat`) re-syncs it. Memory files are not in the workspace, so the scheduler inlines the file: the prompt is *"Here is HEARTBEAT.md. If nothing needs the user's attention, reply exactly HEARTBEAT_OK."* followed by the content of `HEARTBEAT.md` (`Memory.Read`, `SystemViewer`). An empty or whitespace-only HEARTBEAT.md skips the tick with no LLM call. Delivery is suppressed when the final text starts with `HEARTBEAT_OK` (emphasis, blockquote or code ticks around the token are tolerated by the scheduler, the runner and the channel alike), including for a resumed (previously parked) heartbeat run: the runner wraps the resume sink so a `HEARTBEAT_OK` answer is not delivered then either; capped by `heartbeat_daily_usd` (§5.12). Members may create their own `heartbeat`-kind schedules in the Schedules UI; a check-in needs an account on the web (*"a check-in needs an account on the web"*), and teammates have no HEARTBEAT.md.
- Missed runs at boot: `missed_run: skip` (default) records nothing; `once` fires one catch-up run if missed by < 1 h. Three consecutive failures auto-disable the schedule and notify the owner (an account in the chat of its last failed task, *"The schedule “{name}” was turned off because its last {n} tasks failed. Turn it back on under Schedules once it's fixed."*; a team schedule's chat hears *"The schedule “{name}” was turned off because its last {n} tasks failed. An admin can turn it back on in Bothy once it's fixed."*). Runs started with Run now don't count toward the three: trying a schedule out never turns it off.

### 5.17 Run-end notices (v0.2 UI, `internal/agent/notices.go`)

Notices follow the UI's error formula (docs/design/ui.md §14.4): what happened, said once in plain words, amounts as money. The raw provider or budget message stays only in `Run.Error` and `run.finished.error`, which the UI shows under Details.

- **Model failure:** *"{Provider} had a problem answering."* when one provider was tried; *"No AI model could answer right now."* when the fallback chain ran out; when every attempt failed on auth, *"{Provider} stopped accepting Bothy's key."* for admins and *"Bothy can't reach its AI model right now. An admin has been told."* for members. `{Provider}` is the provider type's company (Anthropic, OpenAI, Google Gemini, Ollama, Amazon Bedrock) or a well-known OpenAI-compatible host's (OpenRouter, Groq, xAI, DeepSeek, Mistral, Together AI, Fireworks AI), else "The AI model".
- **Other ends:** failed *"Bothy couldn't finish this task."*; loop *"Bothy stopped because it kept repeating the same step. Try asking in a different way."*; steps *"Bothy paused after {n} steps to check in."*; run cost *"Bothy stopped because this task reached its {$x} limit."*; daily limit, at run end and when `Submit` refuses, *"Bothy has reached today's {$x} spending limit. It can start again at midnight ({time zone})."*; time *"Bothy stopped because this task ran out of time."*; tokens *"Bothy stopped because this task reached its length limit."*; restarts *"Bothy restarted before it finished this task. Send “retry” to continue."*; stop *"You stopped Bothy."*
- The fork, compaction, auto-pin, unknown model override, missing model, text-only model and parked notices carry no session ids, slots, token counts or taint words (e.g. *"The model chosen for this chat isn't available any more, so Bothy used the main model."*, *"No AI model is connected yet. Connect one in Settings → Models."*).

---

## 6. SQLite schema (`internal/store/migrations/0001_init.sql`)

**Open (skeleton-implemented in `store.Open`, then owned by M1):**
- Driver `modernc.org/sqlite`; file `<data>/bothy.db`, created 0600; `-wal`/`-shm` 0600.
- Pragmas on every connection: `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`, `cache_size=-2048`, `mmap_size=0`, `temp_store=MEMORY`, `trusted_schema=OFF`, `cell_size_check=ON`; `SQLITE_DBCONFIG_DEFENSIVE` via the DSN parameter `_defensive=1` (modernc ≥ v1.57.0 applies it right after open, before the pragmas). Pragmas go in `_pragma=` DSN parameters so every pooled connection gets them.
- One writer connection (`BEGIN IMMEDIATE` for `Tx`) and two read-only reader connections (`query_only=1`).
- `SELECT sqlite_version()` must be ≥ 3.53.4, else refuse to start with a clear error.
- Migrator: `PRAGMA user_version` holds the applied version; embedded `migrations/NNNN_*.sql` applied in order, each in one transaction. When any migration is pending and the DB is non-empty, first `VACUUM INTO <data>/backups/pre-<ver>-<ts>.db` (0600), keeping the newest `storage.backups_keep` (3).
- Every migration runs on a dedicated writer connection with `PRAGMA foreign_keys=OFF` (it cannot change inside a transaction), so rebuilding a parent table never cascades into its children. `PRAGMA foreign_key_check` is counted per (table, parent, key) before and after: a migration that adds a violation rolls back, leaving `user_version` unchanged, while violations already there before it don't block it. If turning foreign keys back on fails, the connection is discarded, never pooled. Migration DML therefore never relies on cascades: it deletes and repoints children itself.
- Only the lead adds migrations (`0002_*.sql` onward); the schema is frozen for module agents.

```sql
-- 0001_init.sql. Times: unix ms INTEGER (0 = unset). Money: micro-USD INTEGER.
-- JSON: TEXT with json_valid checks. Applied in one transaction; the migrator
-- sets PRAGMA user_version = 1 afterwards.

CREATE TABLE meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
) STRICT, WITHOUT ROWID;
-- keys: install_id (store, first open), setup_token_hash, setup_token_exp, setup_grant_hash,
--       setup_grant_exp, setup_done (auth), paused (runner), approval_mac_key (policy, first
--       boot, 32 random bytes hex), constitution_version (runner), agent.spotlight_key (runner,
--       first use, 32 random bytes hex), agent.workspace_untrusted.<ws> (runner, "1" once the
--       workspace holds files from untrusted sources; never cleared),
--       channel.telegram.offset, channel.discord.resume (adapters; JSON session_id, seq, url)

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  display_name  TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL CHECK (role IN ('owner','admin','member')),
  pw_hash       TEXT NOT NULL,
  timezone      TEXT NOT NULL DEFAULT 'UTC',
  disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX users_one_owner ON users(role) WHERE role = 'owner';

CREATE TABLE principals (
  id           TEXT PRIMARY KEY,               -- user principals: same string as users.id
  kind         TEXT NOT NULL CHECK (kind IN ('user','space')),
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,  -- owner for spaces
  display_name TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL UNIQUE,
  tool_profile TEXT NOT NULL CHECK (tool_profile IN ('standard','restricted')),  -- no default: users insert 'standard', spaces 'restricted'
  created_at   INTEGER NOT NULL,
  CHECK (kind = 'space' OR id = user_id)
) STRICT;
CREATE INDEX principals_user ON principals(user_id);

CREATE TABLE spaces (
  principal_id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('group','guest')),
  channel      TEXT NOT NULL,
  chat_id      TEXT NOT NULL,
  chat_kind    TEXT NOT NULL CHECK (chat_kind IN ('dm','group')),
  title        TEXT NOT NULL DEFAULT '',
  owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  enabled      INTEGER NOT NULL DEFAULT 0,
  mention_only INTEGER NOT NULL DEFAULT 1,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (channel, chat_id)
) STRICT;

CREATE TABLE channel_identities (
  channel        TEXT NOT NULL,
  external_id    TEXT NOT NULL,
  username       TEXT NOT NULL DEFAULT '',
  display_name   TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('pending','linked','guest','blocked')),
  user_id        TEXT REFERENCES users(id) ON DELETE SET NULL,
  principal_id   TEXT REFERENCES principals(id) ON DELETE SET NULL,
  dm_chat_id     TEXT NOT NULL DEFAULT '',
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  last_notice_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (channel, external_id),
  CHECK (status <> 'linked' OR user_id IS NOT NULL),
  CHECK (status <> 'guest' OR principal_id IS NOT NULL)
) STRICT, WITHOUT ROWID;
CREATE INDEX channel_identities_user ON channel_identities(user_id);

CREATE TABLE pairing_codes (
  code_hash   BLOB PRIMARY KEY,                -- sha256(normalized code)
  purpose     TEXT NOT NULL CHECK (purpose IN ('link_identity','approve_sender')),
  user_id     TEXT REFERENCES users(id) ON DELETE CASCADE,
  channel     TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  used_at     INTEGER NOT NULL DEFAULT 0,
  attempts    INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX pairing_codes_identity ON pairing_codes(channel, external_id) WHERE purpose = 'approve_sender';

CREATE TABLE auth_sessions (
  id_hash         BLOB PRIMARY KEY,            -- sha256(cookie value)
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf            TEXT NOT NULL,
  ip              TEXT NOT NULL DEFAULT '',
  ua              TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  last_seen_at    INTEGER NOT NULL,
  idle_expires_at INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX auth_sessions_user ON auth_sessions(user_id);
CREATE INDEX auth_sessions_exp ON auth_sessions(expires_at);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,               -- tok_<26>
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  secret_hash  BLOB NOT NULL UNIQUE,           -- sha256(secret); 256-bit secret, no KDF needed
  scopes       TEXT NOT NULL CHECK (json_valid(scopes)),
  on_approval  TEXT NOT NULL DEFAULT 'deny' CHECK (on_approval IN ('deny','wait')),
  rate_per_min INTEGER NOT NULL DEFAULT 30,
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  expires_at   INTEGER NOT NULL,
  revoked_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX api_tokens_user ON api_tokens(user_id);

CREATE TABLE sessions (
  id              TEXT PRIMARY KEY,
  principal_id    TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  origin          TEXT NOT NULL CHECK (origin IN ('web','telegram','discord','slack','api','schedule')),
  channel_key     TEXT,
  chat            TEXT CHECK (chat IS NULL OR json_valid(chat)),
  title           TEXT NOT NULL DEFAULT '',
  model_slot      TEXT NOT NULL DEFAULT 'main',
  model_override  TEXT NOT NULL DEFAULT '',
  system_snapshot TEXT NOT NULL DEFAULT '',
  tools_snapshot  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tools_snapshot)),
  snapshot_hash   TEXT NOT NULL DEFAULT '',
  summary         TEXT NOT NULL DEFAULT '',
  summary_upto    INTEGER NOT NULL DEFAULT 0,
  epoch           INTEGER NOT NULL DEFAULT 0,
  memory_seen     INTEGER NOT NULL DEFAULT 0,
  pins_seen       INTEGER NOT NULL DEFAULT 0,
  taint           INTEGER NOT NULL DEFAULT 0,
  hidden          INTEGER NOT NULL DEFAULT 0,
  archived        INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  last_message_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX sessions_channel_key ON sessions(principal_id, channel_key)
  WHERE channel_key IS NOT NULL AND archived = 0;
CREATE INDEX sessions_recent ON sessions(principal_id, archived, hidden, last_message_at DESC);
-- Hidden one-shot api sessions still due for retention (store/retention.go).
CREATE INDEX sessions_hidden_api ON sessions(max(created_at, updated_at, last_message_at))
  WHERE hidden = 1 AND origin = 'api' AND archived = 0;

CREATE TABLE session_urls (                      -- URL provenance for web_fetch in exposed sessions
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  url_hash   BLOB NOT NULL,                      -- sha256(normalized URL)
  source     TEXT NOT NULL CHECK (source IN ('search','user')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, url_hash)
) STRICT, WITHOUT ROWID;

-- INTEGER PRIMARY KEY is required: FTS5 external content keys on it, and the
-- implicit rowid of a TEXT-PK table can change under VACUUM / VACUUM INTO.
CREATE TABLE messages (
  id          INTEGER PRIMARY KEY,
  session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  run_id      TEXT,
  role        TEXT NOT NULL CHECK (role IN ('user','assistant','tool')),
  kind        TEXT NOT NULL DEFAULT 'normal' CHECK (kind IN ('normal','summary','steer','notice')),
  provenance  TEXT NOT NULL CHECK (provenance IN ('user','untrusted','tool','system')),
  sender      TEXT NOT NULL DEFAULT '',
  parts       TEXT NOT NULL CHECK (json_valid(parts)),
  text        TEXT NOT NULL DEFAULT '',          -- PlainText() projection for FTS (no tool JSON)
  tokens      INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  UNIQUE (session_id, seq)
) STRICT;

CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF text ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
  INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TABLE pins (
  id         INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  text       TEXT NOT NULL CHECK (length(text) <= 2000),
  source     TEXT NOT NULL CHECK (source IN ('user','auto')),
  msg_seq    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX pins_session ON pins(session_id, id);

CREATE TABLE schedules (
  id           TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('cron','heartbeat','once')),
  spec         TEXT NOT NULL,
  tz           TEXT NOT NULL DEFAULT 'UTC',
  active_hours TEXT NOT NULL DEFAULT '',
  prompt       TEXT NOT NULL,
  session_id   TEXT REFERENCES sessions(id) ON DELETE SET NULL,
  deliver      TEXT CHECK (deliver IS NULL OR json_valid(deliver)),
  model_slot   TEXT NOT NULL DEFAULT 'main',
  limits       TEXT NOT NULL CHECK (json_valid(limits)),
  missed_run   TEXT NOT NULL DEFAULT 'skip' CHECK (missed_run IN ('skip','once')),
  enabled      INTEGER NOT NULL DEFAULT 1,
  created_by   TEXT NOT NULL,
  failures     INTEGER NOT NULL DEFAULT 0,
  next_run_at  INTEGER NOT NULL DEFAULT 0,
  last_run_at  INTEGER NOT NULL DEFAULT 0,
  last_status  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX schedules_due ON schedules(next_run_at) WHERE enabled = 1;

CREATE TABLE runs (
  id                 TEXT PRIMARY KEY,
  session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  principal_id       TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  user_id            TEXT NOT NULL,
  trigger            TEXT NOT NULL CHECK (trigger IN ('message','schedule','heartbeat','api','resume')),
  schedule_id        TEXT REFERENCES schedules(id) ON DELETE SET NULL,
  status             TEXT NOT NULL CHECK (status IN ('queued','running','waiting_approval','parked','done',
                       'stopped','failed','budget_exceeded','loop_detected','interrupted','skipped')),
  error              TEXT NOT NULL DEFAULT '',
  steps              INTEGER NOT NULL DEFAULT 0,
  tool_calls         INTEGER NOT NULL DEFAULT 0,
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros        INTEGER NOT NULL DEFAULT 0,
  models             TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
  limits             TEXT NOT NULL CHECK (json_valid(limits)),
  parked_on          TEXT NOT NULL DEFAULT '',
  stopped_by         TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  started_at         INTEGER NOT NULL DEFAULT 0,
  finished_at        INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX runs_session ON runs(session_id, created_at DESC);
CREATE INDEX runs_schedule ON runs(schedule_id, created_at DESC) WHERE schedule_id IS NOT NULL;
CREATE INDEX runs_principal_time ON runs(principal_id, created_at DESC);
CREATE INDEX runs_open ON runs(status) WHERE status IN ('queued','running','waiting_approval','parked');
-- DB guard for "one active run per session" (the in-memory worker is the primary guard).
CREATE UNIQUE INDEX runs_one_active ON runs(session_id) WHERE status IN ('running','waiting_approval');

CREATE TABLE llm_calls (
  id                 TEXT PRIMARY KEY,
  run_id             TEXT REFERENCES runs(id) ON DELETE CASCADE,  -- NULL for passthrough/probe
  principal_id       TEXT NOT NULL DEFAULT '',
  user_id            TEXT NOT NULL,
  step               INTEGER NOT NULL DEFAULT 0,
  purpose            TEXT NOT NULL CHECK (purpose IN ('agent','compaction','title','heartbeat','probe','passthrough')),
  provider           TEXT NOT NULL,
  model              TEXT NOT NULL,
  slot               TEXT NOT NULL DEFAULT '',
  attempts           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(attempts)),
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
  usage_estimated    INTEGER NOT NULL DEFAULT 0,
  cost_micros        INTEGER NOT NULL DEFAULT 0,
  price_known        INTEGER NOT NULL DEFAULT 1,
  breakdown          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(breakdown)),
  prefix_hash        TEXT NOT NULL DEFAULT '',
  ttft_ms            INTEGER NOT NULL DEFAULT 0,
  latency_ms         INTEGER NOT NULL DEFAULT 0,
  stop_reason        TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL
) STRICT;
CREATE INDEX llm_calls_run ON llm_calls(run_id, step);
CREATE INDEX llm_calls_user_time ON llm_calls(user_id, created_at);
CREATE INDEX llm_calls_time ON llm_calls(created_at);

CREATE TABLE tool_calls (
  id             TEXT PRIMARY KEY,               -- tcl_<26>
  run_id         TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  session_id     TEXT NOT NULL,
  principal_id   TEXT NOT NULL,
  step           INTEGER NOT NULL,
  call_id        TEXT NOT NULL,
  tool           TEXT NOT NULL,
  source         TEXT NOT NULL,
  side_effect    TEXT NOT NULL,
  action         TEXT CHECK (action IS NULL OR json_valid(action)),
  verdict        TEXT NOT NULL CHECK (verdict IN ('auto','ask','deny','')),
  rule           TEXT NOT NULL DEFAULT '',
  approval_id    TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL CHECK (status IN ('planned','waiting','approved','denied','running','ok',
                   'error','timeout','stopped','invalid')),
  result_preview TEXT NOT NULL DEFAULT '',
  result_bytes   INTEGER NOT NULL DEFAULT 0,
  full_ref       TEXT NOT NULL DEFAULT '',
  untrusted      INTEGER NOT NULL DEFAULT 0,
  duration_ms    INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  finished_at    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX tool_calls_run ON tool_calls(run_id, step);

CREATE TABLE approvals (
  id           TEXT PRIMARY KEY,
  run_id       TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  session_id   TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  tool_call_id TEXT NOT NULL,
  action       TEXT NOT NULL CHECK (json_valid(action)),
  decision     TEXT NOT NULL CHECK (json_valid(decision)),
  card         TEXT NOT NULL CHECK (json_valid(card)),
  approver     TEXT NOT NULL CHECK (json_valid(approver)),
  chat         TEXT CHECK (chat IS NULL OR json_valid(chat)),
  status       TEXT NOT NULL CHECK (status IN ('pending','allowed','denied','expired','parked','cancelled')),
  choice       TEXT NOT NULL DEFAULT '',
  on_timeout   TEXT NOT NULL CHECK (on_timeout IN ('deny','park')),
  decided_by   TEXT NOT NULL DEFAULT '',
  decided_via  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  park_until   INTEGER NOT NULL DEFAULT 0,
  decided_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX approvals_open ON approvals(principal_id, status) WHERE status IN ('pending','parked');
CREATE INDEX approvals_due ON approvals(expires_at) WHERE status = 'pending';
CREATE INDEX approvals_parked ON approvals(park_until) WHERE status = 'parked';
-- First contact (Approvals.Seen); the expressions match store/runs.go.
CREATE INDEX approvals_seen_recipient ON approvals(principal_id, json_extract(action, '$.recipient')) WHERE status = 'allowed';
CREATE INDEX approvals_seen_host ON approvals(principal_id, json_extract(action, '$.host')) WHERE status = 'allowed';
CREATE INDEX approvals_run ON approvals(run_id);

CREATE TABLE approval_messages (                 -- chat messages that carry a card; presses must come from these
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  channel     TEXT NOT NULL,
  chat_id     TEXT NOT NULL,
  thread_id   TEXT NOT NULL DEFAULT '',
  message_id  TEXT NOT NULL,
  truncated   INTEGER NOT NULL DEFAULT 0,        -- 1 => deny-only from chat
  PRIMARY KEY (approval_id, channel, chat_id, message_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE grants (
  id                 TEXT PRIMARY KEY,
  principal_id       TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  scope              TEXT NOT NULL CHECK (scope IN ('session','schedule','principal')),
  scope_id           TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL CHECK (kind IN ('tool','domain','recipient')),
  tool               TEXT NOT NULL DEFAULT '',
  pattern            TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(pattern)),
  value              TEXT NOT NULL DEFAULT '',
  max_se             TEXT NOT NULL,
  allow_when_tainted INTEGER NOT NULL DEFAULT 0,
  uses               INTEGER NOT NULL DEFAULT 0,
  max_uses           INTEGER NOT NULL DEFAULT 0,
  created_by         TEXT NOT NULL,
  created_at         INTEGER NOT NULL,
  expires_at         INTEGER NOT NULL DEFAULT 0,
  revoked_at         INTEGER NOT NULL DEFAULT 0,
  CHECK ((scope = 'principal') = (scope_id = '')),
  CHECK (kind <> 'tool' OR (tool <> '' AND pattern <> '{}')),   -- never a whole-tool grant
  CHECK (allow_when_tainted = 0 OR kind = 'recipient')
) STRICT;
CREATE INDEX grants_lookup ON grants(principal_id, scope, scope_id) WHERE revoked_at = 0;

CREATE TABLE vault_keys (
  id          INTEGER PRIMARY KEY,
  wrapped_dek BLOB NOT NULL,                     -- nonce || AES-256-GCM(KEK, DEK)
  kek_check   BLOB NOT NULL,                     -- nonce || AES-256-GCM(KEK, "bothy-kek-check")
  kek_source  TEXT NOT NULL,
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX vault_keys_active ON vault_keys(active) WHERE active = 1;

CREATE TABLE vault_secrets (
  scope        TEXT NOT NULL,
  name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64 AND name NOT GLOB '*[^a-z0-9_.-]*'),
  key_id       INTEGER NOT NULL REFERENCES vault_keys(id),
  version      INTEGER NOT NULL,
  nonce        BLOB NOT NULL,
  ciphertext   BLOB NOT NULL,                    -- AAD = scope || 0x00 || name || 0x00 || version
  hint         TEXT NOT NULL DEFAULT '',
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (scope, name)
) STRICT, WITHOUT ROWID;

CREATE TABLE mcp_tool_pins (
  server        TEXT NOT NULL,
  tool          TEXT NOT NULL,
  hash          TEXT NOT NULL,
  definition    TEXT NOT NULL CHECK (json_valid(definition)),
  status        TEXT NOT NULL CHECK (status IN ('pending','approved','superseded','disabled')),
  side_effect   TEXT NOT NULL DEFAULT 'external_send',
  field_map     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(field_map)),
  private       INTEGER NOT NULL DEFAULT 0,
  approved_by   TEXT NOT NULL DEFAULT '',
  first_seen_at INTEGER NOT NULL,
  approved_at   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server, tool, hash)
) STRICT, WITHOUT ROWID;
CREATE INDEX mcp_pins_status ON mcp_tool_pins(server, status);

CREATE TABLE mcp_installs (
  install_id TEXT PRIMARY KEY,                   -- in_<26>
  server     TEXT NOT NULL,
  version    TEXT NOT NULL,
  lock_hash  TEXT NOT NULL DEFAULT '',
  lock       BLOB,
  tree_hash  TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL CHECK (status IN ('installing','ready','failed','drifted')),
  error      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX mcp_installs_server ON mcp_installs(server, created_at DESC);

CREATE TABLE skills (
  name          TEXT PRIMARY KEY,
  description   TEXT NOT NULL,
  path          TEXT NOT NULL,
  hash          TEXT NOT NULL,                   -- sha256 over the sorted file tree
  approved_hash TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL CHECK (status IN ('pending','enabled','disabled','changed')),
  tokens        INTEGER NOT NULL DEFAULT 0,
  installed_by  TEXT NOT NULL DEFAULT '',
  approved_by   TEXT NOT NULL DEFAULT '',
  installed_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE memory_events (
  id           INTEGER PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  file         TEXT NOT NULL CHECK (file IN ('MEMORY','USER','HEARTBEAT')),
  op           TEXT NOT NULL CHECK (op IN ('add','replace','remove','edit')),
  before       TEXT NOT NULL DEFAULT '',
  after        TEXT NOT NULL DEFAULT '',
  run_id       TEXT NOT NULL DEFAULT '',
  actor        TEXT NOT NULL,
  provenance   TEXT NOT NULL DEFAULT 'user',
  created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX memory_events_p ON memory_events(principal_id, id);

CREATE TABLE spend_daily (
  user_id       TEXT NOT NULL,                   -- billing user (space owner for spaces)
  day           TEXT NOT NULL,                   -- YYYY-MM-DD in config timezone
  cost_micros   INTEGER NOT NULL DEFAULT 0,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  calls         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, day)
) STRICT, WITHOUT ROWID;

CREATE TABLE inbound_dedup (
  channel TEXT NOT NULL,
  msg_id  TEXT NOT NULL,
  seen_at INTEGER NOT NULL,
  PRIMARY KEY (channel, msg_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE uploads (
  id           TEXT PRIMARY KEY,                 -- upl_<26>
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  session_id   TEXT NOT NULL,
  name         TEXT NOT NULL,
  mime         TEXT NOT NULL,
  size         INTEGER NOT NULL,
  ref          TEXT NOT NULL,                    -- workspace-relative path
  created_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE egress_log (
  id           INTEGER PRIMARY KEY,
  ts           INTEGER NOT NULL,
  dialer       TEXT NOT NULL CHECK (dialer IN ('box','public','system')),
  dest         TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  principal_id TEXT NOT NULL DEFAULT '',
  run_id       TEXT NOT NULL DEFAULT '',
  host         TEXT NOT NULL,
  port         INTEGER NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  decision     TEXT NOT NULL CHECK (decision IN ('allow','deny','held_allow','held_deny')),
  reason       TEXT NOT NULL DEFAULT '',
  bytes_up     INTEGER NOT NULL DEFAULT 0,
  bytes_down   INTEGER NOT NULL DEFAULT 0,
  duration_ms  INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX egress_log_ts ON egress_log(ts);
CREATE INDEX egress_log_session ON egress_log(session_id, ts);

CREATE TABLE audit_log (
  id         INTEGER PRIMARY KEY,
  ts         INTEGER NOT NULL,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  outcome    TEXT NOT NULL DEFAULT 'ok' CHECK (outcome IN ('ok','denied','error')),
  session_id TEXT NOT NULL DEFAULT '',
  run_id     TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(detail)),
  prev_hash  BLOB NOT NULL,
  hash       BLOB NOT NULL                       -- sha256(prev_hash || canonical JSON of the row without hashes)
) STRICT;
CREATE INDEX audit_ts ON audit_log(ts);
CREATE INDEX audit_action ON audit_log(action, ts);
CREATE INDEX audit_actor ON audit_log(actor, ts);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
```

**Session search query** (`MessageRepo.Search`):

```sql
SELECT m.session_id, s.title, m.seq, snippet(messages_fts, 0, '[', ']', '…', 12) AS snip,
       m.created_at, bm25(messages_fts) AS rank, m.provenance IN ('user','system') AS trusted
FROM messages_fts
JOIN messages m ON m.id = messages_fts.rowid
JOIN sessions s ON s.id = m.session_id
WHERE messages_fts MATCH ?1 AND s.principal_id IN (/* viewer principals */) AND s.hidden = 0
ORDER BY rank LIMIT ?2;
```

User input to `MATCH` is converted to a quoted-phrase/AND query (never raw FTS syntax).

**Repository ↔ table map:** Meta→`meta`; Users→`users` (+ `principals` row on create); Principals→`principals`; Spaces→`spaces` (+ `principals`); Identities→`channel_identities`; AuthSessions→`auth_sessions`; Tokens→`api_tokens`; Sessions→`sessions`, `session_urls`; Messages→`messages`, `messages_fts`; Pins→`pins`; Runs→`runs`, `llm_calls`, `tool_calls`; Approvals→`approvals`, `approval_messages`; Grants→`grants`; Schedules→`schedules`; Usage→`spend_daily` (caps) and `llm_calls` (reports); Audit→`audit_log`; Egress→`egress_log`; MCP→`mcp_tool_pins`, `mcp_installs`; Skills→`skills`; MemoryEvents→`memory_events`; TeamMemoryEvents→`team_memory_events`; Vault→`vault_keys`, `vault_secrets`; Dedup→`inbound_dedup`; Uploads→`uploads`.

**Retention** (`DB.RunRetention`, hourly):
- `egress_log` older than `storage.retention.egress_log` (720h).
- `inbound_dedup` older than 24 h.
- expired `auth_sessions`.
- `runs` (with cascading `llm_calls`, `tool_calls`, `approvals`) only when `storage.retention.runs` > 0 (default 0 = keep).
- v0.2 UI (S14): `web` sessions older than 24 h that hold no messages, runs, pins or schedule (archived or not, never hidden ones) are deleted with their meta key `agent.refine.<id>`; the candidate query joins `principals CROSS JOIN sessions` so it stays on the `sessions_recent` index (`last_message_at = 0`) instead of reading every session row.
- Hidden `api` sessions idle for 7 days (by `max(created_at, updated_at, last_message_at)`) are archived and their messages deleted; archiving also clears the session's `system_snapshot`, `tools_snapshot`, `snapshot_hash` and `summary`. Only unarchived hidden `api` sessions are visited, through the `sessions_hidden_api` index on that same expression, so the hourly pass never scans every session.
- `audit_log` is never pruned online. `bothy audit archive --before <date>` runs with the gateway stopped: it exports the range to JSONL (0600), rebuilds `audit_log` without the triggers in a transaction, deletes the range, re-creates the triggers, and inserts an anchor row (`action=audit.archived`, detail = range, count and the last archived hash) chained to the remaining log.

**v0.2 migrations** (§14; the lead adds them, each its own file, `0001_init.sql` never changes). Migrations run 1..N without gaps, so a v0.1 database (user_version 1) gets 0002–0014 in order after the pre-migration backup, and the result matches a fresh database's schema. An older binary refuses the upgraded database (e.g. "schema version 7 is newer than this binary supports (1)").

- `0002_repos.sql` (F4): `github_connections` (id `ghc_…`, auth pat|app, App rows need `app_id` and `installation_id` > 0, `secret_ref LIKE 'secret:_%'`, status ok|error) and `repos` (primary key `(owner, name)` `COLLATE NOCASE`, connection FK `ON DELETE CASCADE`, `allowed`, `last_indexed_sha`, dropped again by 0007), STRICT.
- `0003_knowledge.sql` (F6): `knowledge_repos` (UNIQUE principal + repo, index and notes state), `knowledge_files` (repo + path; hash = chunker version + sha256), `knowledge_chunks` (INTEGER PRIMARY KEY as the external content of `knowledge_fts(path, title, body, terms)` with ai/ad triggers and an au trigger on path/title/body/terms only, so relabelling a chunk's sha does not rewrite FTS rows), `knowledge_sessions` (session, repo, touched_at, injected epoch and hash). It also rebuilds `llm_calls` with identical columns, indexes and rowids so the purpose CHECK accepts `notes`.
- `0004_approval_votes.sql` (F5): `approval_votes` (one vote per approver and approval) and `release_deploys`.
- `0005_approval_comments.sql` (v0.2 UI, S4 and S12): `approvals.comment`, `approval_votes.comment` (the approver's reason) and `channel_identities.first_text` (the start of a pending sender's first message; the store keeps it only while the status is `pending`), all `TEXT NOT NULL DEFAULT ''`.
- `0006_must_change_password.sql` (v0.2 UI, S9): `users.must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1))`; the store's user columns include it.
- `0007_v02_fixes.sql`: `release_deploys.via` (`''` | `connection` | `token`, CHECKed; `''` for deploys recorded before it), `knowledge_repos.indexed_sha` (backfilled from `sha` on `ready` rows) and the drop of `repos.last_indexed_sha` (index state is per principal, §14.3, §14.4).
- `0008_external_secret_refs.sql` (§14.8): `github_connections.secret_ref` also accepts `aws-sm:_%`; SQLite cannot change a CHECK in place, so the table is rebuilt, and `repos` with it so its foreign key follows.
- `0009_ui_state.sql` (v0.2 UI, §14.7): `users.last_login_at`, `preferred_chat` and `approvals_web_only`, `runs.refined`, `release_deploys.sha` and `subject`, and the `skill_approved_files` table.
- `0010_knowledge_stemming.sql`: `knowledge_fts` is created again with `tokenize='porter unicode61 remove_diacritics 2'` (FTS5 cannot change a tokenizer in place) and rebuilt from `knowledge_chunks`; the 0003 triggers keep feeding it. Knowledge search then matches other forms of a word ("redirects" finds `redirect`).
- `0011_sign_ins.sql` (§15): `sign_ins` (provider `google`|`github`, subject, user FK `ON DELETE CASCADE`, email, login, created_at, last_used_at; primary key `(provider, subject)`, UNIQUE `(user_id, provider)`), STRICT, WITHOUT ROWID. There is no token column: Bothy never keeps a provider token.
- `0012_github_apps.sql` (§14.3): `github_apps`, the GitHub Apps Bothy created from a manifest. The private key and client secret live in the global vault scope; the row keeps only their references, cleared when the app is retired, and stays as a reminder until an admin dismisses it. `connection_id` is `ON DELETE SET NULL`, so a later rebuild of `github_connections` must rebuild this table too, as 0008 did for `repos`:

  ```sql
  CREATE TABLE github_apps (
    id                TEXT PRIMARY KEY,                                  -- gha_<26>
    app_id            INTEGER NOT NULL UNIQUE CHECK (app_id > 0),        -- GitHub's id
    slug              TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 100),
    name              TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    owner             TEXT NOT NULL CHECK (length(owner) BETWEEN 1 AND 39),
    owner_id          INTEGER NOT NULL CHECK (owner_id > 0),             -- GitHub's account id: installations must be on it
    owner_type        TEXT NOT NULL CHECK (owner_type IN ('Organization','User')),
    html_url          TEXT NOT NULL CHECK (html_url LIKE 'https://_%' OR html_url LIKE 'http://_%'), -- <web> + GitHub's path: /apps/<slug>, /github-apps/<slug> on GHES
    client_id         TEXT NOT NULL DEFAULT '',
    key_ref           TEXT NOT NULL DEFAULT '' CHECK (key_ref = '' OR key_ref LIKE 'secret:_%'),
    client_secret_ref TEXT NOT NULL DEFAULT '' CHECK (client_secret_ref = '' OR client_secret_ref LIKE 'secret:_%'),
    connection_id     TEXT UNIQUE REFERENCES github_connections(id) ON DELETE SET NULL,
    replaces          TEXT NOT NULL DEFAULT '',                          -- connection to swap out once installed
    retired_at        INTEGER NOT NULL DEFAULT 0,
    created_by        TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    CHECK (retired_at > 0 OR key_ref <> ''),
    CHECK (retired_at = 0 OR (connection_id IS NULL AND key_ref = '' AND client_secret_ref = '' AND replaces = ''))
  ) STRICT;
  ```
- `0013_team_access.sql` (team access): people in chat apps stop being linked to accounts and channels stop having owners.
  - `users` loses `preferred_chat` and `approvals_web_only`; `pairing_codes` is dropped.
  - `principals.user_id` may be NULL for team-owned principals (`kind` `user` | `space` | `person`; a user principal must have a user, a team one must not).
  - `spaces`: `kind` `group` | `person` (guest spaces become person spaces), no `owner_user_id`; new `team_id`, `team_name`, `added_by`, `members`, `external` and `reviewed_at` (0 until an admin turns the channel on or off; a group once turned on gets its `updated_at`). Person spaces are `standard`.
  - `channel_identities`: `status` `pending` | `teammate` | `blocked` (no `user_id`); `principal_id` (a teammate must have one), `admitted_via` (`membership` | `admin`), `can_approve` (teammates only), `membership` (`''` | `unknown` | `member` | `outsider`) with `membership_at`, and `greet` (`''` | `hello` | `moved`). Linked identities of enabled accounts become teammates let in by an admin, owners and admins with Can approve, greeted "moved" on their next message; a linked account's earlier guest space on that chat is reused as its person space; identities of disabled accounts are blocked, keeping that reused guest space (when there is one) as their principal so *Delete their data* still reaches it; enabled guests become teammates; guests whose space was off and blocked guests are blocked. Telegram rows get `membership = 'unknown'`, Slack and Discord `''`.
  - `approvals` parked or pending on a space owner become `Team` (`approver.team`, `user_id` ""); `decided_by` and `grants.created_by` may be `chat:<app>:<id>`; `approval_messages.kind` (`card` | `link` | `notice`).
  - `runs.user_id` and `llm_calls.user_id` of team principals become `''` (passthrough keeps its user); `spend_daily` is keyed by `principal_id`; sessions of user principals lose their chat (`channel_key`, `chat` NULL); user schedules delivering to a chat deliver to the web; a channel schedule with no set chat is turned off with deliver NULL; a teammate's former guest schedule delivering to the web gets deliver NULL.
  - A temporary `m_rows` table CHECKs that sessions, messages, runs, grants, schedules, memory events, approvals, uploads and llm_calls keep their row counts across the rebuild.
  - `meta.team_access_report` (`at`, `linked`, `approvers`, `disabled_accounts`, `guests`, `telegram_guests`, `guests_blocked`, `schedules_paused`), written only when the migration moved someone (a new install, or one where nobody was linked or a guest, gets none; a report whose counts are all 0 is deleted at boot unseen): at boot `serve` audits it once as `migration.team_access` (actor `system`) and, while it is under 7 days old and no admin has changed anyone in People (any `PATCH /api/identities` deletes it), publishes the admin banner `team_access` (*"People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve in People."*, action `/settings/people`), which the v0.2 UI shows as a notice on People and in Approvals rather than a banner, since nothing stops.
- `0014_team_memory.sql` (team memory, §14.9): `team_memory_events` (id, `op` add|replace|remove|edit, before, after, run_id, `approval_id` (the admin's approval an agent change ran under; `''` for an admin's own edit), actor, provenance, created_at), STRICT, with a unique index on a non-empty `approval_id`: an approval is good for one change, so a replayed call can't put back a note removed since. It is the history of `<data>/memory/team/TEAM.md`, which belongs to no principal: no foreign key, so a user purge never touches it (`memory_events` rows go with their principal).
- Meta key `agent.refine.<session>` (`on` | `off`): the session's refine setting (runner, hub, httpapi), alongside the keys listed in the 0001 comment. Meta key `member_scope:<channel>`: what membership meant on that chat app when its verdicts were given (§5.1 step 3). Meta key `picture:<channel>`: the bot id (`PictureSetter.BotID`) whose picture the hub already looked after on that chat app, followed by the picture version Bothy gave it and the `PictureID` it showed as when Bothy gave one (§5.1, the bot's picture).
- Refine calls are recorded in `llm_calls` with purpose `agent` (same step; their trace entries carry the `call_id`); a later migration could add a `refine` purpose.

```sql
-- 0002_repos.sql: GitHub connections and their repositories (v0.2 F4). Times:
-- unix ms INTEGER (0 = unset). The PAT or App private key lives in the vault;
-- secret_ref only names it.

CREATE TABLE github_connections (
  id               TEXT PRIMARY KEY,               -- ghc_<26>
  auth             TEXT NOT NULL CHECK (auth IN ('pat','app')),
  account          TEXT NOT NULL DEFAULT '',
  account_type     TEXT NOT NULL DEFAULT '',
  app_id           TEXT NOT NULL DEFAULT '',
  app_slug         TEXT NOT NULL DEFAULT '',
  installation_id  INTEGER NOT NULL DEFAULT 0,
  secret_ref       TEXT NOT NULL CHECK (secret_ref LIKE 'secret:_%'),
  owns_secret      INTEGER NOT NULL DEFAULT 0 CHECK (owns_secret IN (0,1)),
  status           TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','error')),
  error            TEXT NOT NULL DEFAULT '',
  token_expires_at INTEGER NOT NULL DEFAULT 0,
  checked_at       INTEGER NOT NULL DEFAULT 0,
  created_by       TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  CHECK (auth = 'pat' OR (app_id <> '' AND installation_id > 0))
) STRICT;

CREATE TABLE repos (
  owner            TEXT NOT NULL COLLATE NOCASE,
  name             TEXT NOT NULL COLLATE NOCASE,
  connection_id    TEXT NOT NULL REFERENCES github_connections(id) ON DELETE CASCADE,
  github_id        INTEGER NOT NULL DEFAULT 0,
  default_branch   TEXT NOT NULL DEFAULT '',
  private          INTEGER NOT NULL DEFAULT 1 CHECK (private IN (0,1)),
  allowed          INTEGER NOT NULL DEFAULT 0 CHECK (allowed IN (0,1)),
  last_indexed_sha TEXT NOT NULL DEFAULT '',    -- knowledge index (F6)
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (owner, name),                    -- one connection per repository, whatever the case
  CHECK (owner <> '' AND name <> '')
) STRICT, WITHOUT ROWID;
CREATE INDEX repos_connection ON repos(connection_id);
```

```sql
-- 0003_knowledge.sql (v0.2 F6): the repository index and the metadata of
-- per-repo project notes. Notes text lives in
-- <data>/knowledge/<principal>/<owner>/<repo>/NOTES.md, never in a repo.

CREATE TABLE knowledge_repos (
  id               INTEGER PRIMARY KEY,
  principal_id     TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  repo             TEXT NOT NULL CHECK (repo GLOB '?*/?*' AND length(repo) <= 201),  -- "owner/name", lowercase
  source           TEXT NOT NULL CHECK (source IN ('github','workspace')),
  dir              TEXT NOT NULL,                  -- workspace-relative directory of the indexed copy
  sha              TEXT NOT NULL DEFAULT '',       -- commit of the latest requested index
  status           TEXT NOT NULL CHECK (status IN ('queued','indexing','ready','error')),
  error            TEXT NOT NULL DEFAULT '',
  files            INTEGER NOT NULL DEFAULT 0,
  chunks           INTEGER NOT NULL DEFAULT 0,
  bytes            INTEGER NOT NULL DEFAULT 0,
  skipped          INTEGER NOT NULL DEFAULT 0,
  truncated        INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0,1)),
  indexed_at       INTEGER NOT NULL DEFAULT 0,
  notes_status     TEXT NOT NULL DEFAULT 'none' CHECK (notes_status IN ('none','drafting','ready','error')),
  notes_error      TEXT NOT NULL DEFAULT '',
  notes_untrusted  INTEGER NOT NULL DEFAULT 0 CHECK (notes_untrusted IN (0,1)),
  notes_updated_at INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  UNIQUE (principal_id, repo)
) STRICT;

-- One row per indexed file; hash drives incremental re-indexing.
CREATE TABLE knowledge_files (
  repo_id INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  path    TEXT NOT NULL,
  hash    TEXT NOT NULL,                           -- chunker version + sha256 of the bytes
  size    INTEGER NOT NULL,
  lang    TEXT NOT NULL DEFAULT '',
  chunks  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, path)
) STRICT, WITHOUT ROWID;

-- INTEGER PRIMARY KEY: the FTS5 external content keys on it (see messages).
CREATE TABLE knowledge_chunks (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  sha        TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL,
  start_line INTEGER NOT NULL CHECK (start_line >= 1),
  end_line   INTEGER NOT NULL CHECK (end_line >= start_line),
  kind       TEXT NOT NULL,                        -- heading | func | method | type | class | header | text | …
  lang       TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL,
  terms      TEXT NOT NULL DEFAULT ''              -- identifier parts (camelCase split) for recall
) STRICT;
CREATE INDEX knowledge_chunks_file ON knowledge_chunks(repo_id, path);

CREATE VIRTUAL TABLE knowledge_fts USING fts5(
  path, title, body, terms, content='knowledge_chunks', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER knowledge_chunks_ai AFTER INSERT ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(rowid, path, title, body, terms) VALUES (new.id, new.path, new.title, new.body, new.terms);
END;
CREATE TRIGGER knowledge_chunks_ad AFTER DELETE ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, path, title, body, terms) VALUES ('delete', old.id, old.path, old.title, old.body, old.terms);
END;
-- Relabelling sha after a re-index must not rewrite the FTS rows.
CREATE TRIGGER knowledge_chunks_au AFTER UPDATE OF path, title, body, terms ON knowledge_chunks BEGIN
  INSERT INTO knowledge_fts(knowledge_fts, rowid, path, title, body, terms) VALUES ('delete', old.id, old.path, old.title, old.body, old.terms);
  INSERT INTO knowledge_fts(rowid, path, title, body, terms) VALUES (new.id, new.path, new.title, new.body, new.terms);
END;

-- Repos a session worked on, and the notes version its turn-context carries.
CREATE TABLE knowledge_sessions (
  session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  repo_id        INTEGER NOT NULL REFERENCES knowledge_repos(id) ON DELETE CASCADE,
  touched_at     INTEGER NOT NULL,
  injected_epoch INTEGER NOT NULL DEFAULT -1,
  injected_hash  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (session_id, repo_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX knowledge_sessions_repo ON knowledge_sessions(repo_id);

-- llm_calls.purpose gains 'notes' (project notes drafts, metered outside any
-- run). SQLite cannot alter a CHECK, so the table is rebuilt with the same
-- columns and indexes; no table, trigger or view references it. rowid is
-- copied because per-run listings break ties on it.
CREATE TABLE llm_calls_v3 (
  id                 TEXT PRIMARY KEY,
  run_id             TEXT REFERENCES runs(id) ON DELETE CASCADE,  -- NULL for passthrough/probe/notes
  principal_id       TEXT NOT NULL DEFAULT '',
  user_id            TEXT NOT NULL,
  step               INTEGER NOT NULL DEFAULT 0,
  purpose            TEXT NOT NULL CHECK (purpose IN ('agent','compaction','title','heartbeat','probe','passthrough','notes')),
  provider           TEXT NOT NULL,
  model              TEXT NOT NULL,
  slot               TEXT NOT NULL DEFAULT '',
  attempts           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(attempts)),
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
  usage_estimated    INTEGER NOT NULL DEFAULT 0,
  cost_micros        INTEGER NOT NULL DEFAULT 0,
  price_known        INTEGER NOT NULL DEFAULT 1,
  breakdown          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(breakdown)),
  prefix_hash        TEXT NOT NULL DEFAULT '',
  ttft_ms            INTEGER NOT NULL DEFAULT 0,
  latency_ms         INTEGER NOT NULL DEFAULT 0,
  stop_reason        TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL
) STRICT;
INSERT INTO llm_calls_v3 (rowid, id, run_id, principal_id, user_id, step, purpose, provider, model, slot, attempts,
  input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, usage_estimated, cost_micros,
  price_known, breakdown, prefix_hash, ttft_ms, latency_ms, stop_reason, error, created_at)
SELECT rowid, id, run_id, principal_id, user_id, step, purpose, provider, model, slot, attempts,
  input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, usage_estimated, cost_micros,
  price_known, breakdown, prefix_hash, ttft_ms, latency_ms, stop_reason, error, created_at
FROM llm_calls;
DROP TABLE llm_calls;
ALTER TABLE llm_calls_v3 RENAME TO llm_calls;
CREATE INDEX llm_calls_run ON llm_calls(run_id, step);
CREATE INDEX llm_calls_user_time ON llm_calls(user_id, created_at);
CREATE INDEX llm_calls_time ON llm_calls(created_at);
```

```sql
-- 0004_approval_votes.sql (v0.2 F5): votes of multi-approver approvals and the
-- deploy records of release_deploy. Times: unix ms INTEGER (0 = unset).

CREATE TABLE approval_votes (                    -- no FK to users: a purged voter's vote stays on record
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL,
  choice      TEXT NOT NULL CHECK (choice IN ('allow_once','deny')),
  via         TEXT NOT NULL,
  at          INTEGER NOT NULL,
  PRIMARY KEY (approval_id, user_id)             -- one vote per approver
) STRICT, WITHOUT ROWID;

CREATE TABLE release_deploys (
  id              TEXT PRIMARY KEY,
  environment     TEXT NOT NULL,
  repo            TEXT NOT NULL,
  workflow        TEXT NOT NULL,
  ref             TEXT NOT NULL,
  principal_id    TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  session_id      TEXT NOT NULL,
  run_id          TEXT NOT NULL DEFAULT '',
  approval_id     TEXT NOT NULL DEFAULT '',
  requested_by    TEXT NOT NULL,
  approved_by     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(approved_by) AND json_type(approved_by) = 'array'),
  chat            TEXT CHECK (chat IS NULL OR json_valid(chat)),
  status          TEXT NOT NULL CHECK (status IN ('dispatched','running','succeeded','failed','unknown')),
  workflow_run_id INTEGER NOT NULL DEFAULT 0,
  run_url         TEXT NOT NULL DEFAULT '',
  conclusion      TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  finished_at     INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX release_deploys_recent ON release_deploys(created_at);
CREATE INDEX release_deploys_open ON release_deploys(created_at) WHERE status IN ('dispatched','running');
```

```sql
-- 0005_approval_comments.sql (v0.2 UI): the reason an approver gives with a
-- decision or a vote, and the start of a pending sender's first message for
-- the approvals inbox (cleared once the sender is approved or blocked).

ALTER TABLE approvals ADD COLUMN comment TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_votes ADD COLUMN comment TEXT NOT NULL DEFAULT '';
ALTER TABLE channel_identities ADD COLUMN first_text TEXT NOT NULL DEFAULT '';
```

```sql
-- 0006_must_change_password.sql (v0.2 UI): an account an admin created or
-- reset carries a one-time password until its owner chooses their own.

ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1));
```

```sql
-- 0007_v02_fixes.sql: state kept with what it belongs to.

-- The follower reads a run's status through the dispatcher that started it,
-- even after the repository joined or left a connection's allowlist; '' is a
-- deploy recorded before this column, followed through today's choice.
ALTER TABLE release_deploys ADD COLUMN via TEXT NOT NULL DEFAULT '' CHECK (via IN ('', 'connection', 'token'));

-- Indexes are per principal, so the commit a repository was last indexed at
-- is too: knowledge_repos.sha is the latest requested commit, indexed_sha the
-- one the latest finished index read. The shared repos.last_indexed_sha told
-- every user what someone else had indexed.
ALTER TABLE knowledge_repos ADD COLUMN indexed_sha TEXT NOT NULL DEFAULT '';
UPDATE knowledge_repos SET indexed_sha = sha WHERE status = 'ready';
ALTER TABLE repos DROP COLUMN last_indexed_sha;
```

---

## 7. Config

### 7.1 Full file (`<data>/bothy.yaml`)

Zero-config works with no file at all. This example decodes strictly (`KnownFields(true)`) into the `config.Config` struct in §7.4; that is a skeleton unit test.

```yaml
# <data>/bothy.yaml: every key is optional; the values shown are the built-in
# defaults unless marked "example". `bothy config defaults` prints this file;
# `bothy config check` validates one; `bothy config effective` prints the merged
# result with a source comment on every line.
version: 1
mode: single                      # single | multi (multi enables users, roles, per-user caps)
timezone: UTC                     # new accounts' zone and spend days; the wizard sets the owner's browser zone
telemetry: false                  # reserved; no phone-home code exists

server:
  listen: "127.0.0.1:8080"        # the image sets BOTHY_DEPLOYMENT=container => default 0.0.0.0:8080
  public_url: ""                  # e.g. https://bothy.example.com; used for cards, cookies, Origin checks
  allowed_hosts: []               # extra Host values; always allowed: public_url host, localhost, 127.0.0.1, [::1]
  trusted_proxies: []             # CIDRs whose X-Forwarded-For/-Proto are honoured; loopback is NOT implied
  local_cidrs: []                 # extra peers treated as local (default: loopback + the container's default gateway)
  insecure_lan: false             # LOCKED: allow plain HTTP from RFC1918/ULA peers (env/file only)
  tls: {cert_file: "", key_file: ""}
  max_body_mb: 1
  upload_max_mb: 20

storage:
  data_dir: /data
  work_dir: ""                    # "" = /work if it is a mount point, else <data_dir>/work
  run_dir: /run/bothy             # where it does not exist (native installs): $XDG_RUNTIME_DIR/bothy, else <data_dir>/run
  backups_keep: 3
  retention: {egress_log: 720h, runs: 0s, inbound_dedup: 24h}

log: {level: info, format: text}  # format: text | json

auth:
  session_idle: 168h
  session_max: 720h
  password_min_length: 12
  lockout: {max_failures: 5, base: 1m, max: 1h}     # per username and per IP, exponential
  argon2: {memory_kib: 19456, iterations: 2, parallelism: 1}
  pairing_ttl: 15m                # no longer used: link codes are gone
  setup_token_ttl: 24h
  admin_can_read_chats: false     # LOCKED: admins never see other users' message content
  sign_in:                        # sign in with Google or GitHub next to passwords (Settings → Security; SPEC §15)
    allow: []                     # who may sign in: addresses (pat@gmail.com) and domains (acme.com, Google Workspace only), lower case, at most 200
    allow_new: false              # true: someone on the list without an account gets a member account (team mode only)
    google:
      enabled: false              # needs client_id, client_secret, a non-empty allow list and an https server.public_url
      client_id: ""               # an OAuth client of type "Web application" in Google Cloud
      client_secret: ""           # secret:<name>; the UI saves it as secret:google_sign_in_client_secret
      api_base: ""                # LOCKED: tests only; a loopback http(s) server answering Google's endpoints
    github:
      enabled: false              # needs client_id, client_secret, an address in the allow list or orgs, and an https server.public_url
      client_id: ""               # a GitHub OAuth app (Settings → Developer settings → OAuth Apps)
      client_secret: ""           # secret:<name>; the UI saves it as secret:github_sign_in_client_secret
      orgs: []                    # active members of these GitHub organizations may also sign in
      org_ids: {}                 # written by Bothy as orgs are saved in Settings → Security: lower-cased name → GitHub id ({acme-inc: 9919}), which a membership must match; a name without one is matched by name, ids of names not in orgs are ignored
      api_base: ""                # LOCKED: tests only

vault:
  kek: auto                       # auto | file:/path | systemd:<name> | passphrase | env (BOTHY_KEK; flagged high)
  keyfile: ""                     # auto-generated KEK; "" = /keys/kek if /keys is a mount point, else <data_dir>/keys/kek

secrets:                          # v0.2: cloud secret stores that secret fields may reference instead of the vault
  aws:                            # "aws-sm:<secret name or ARN>[#<json key>][?region=<r>&profile=<p>]"
    region: ""                    # required for references without ?region= or an ARN
    profile: ""                   # shared-config profile; "" = AWS_PROFILE or the default chain
    cache_ttl: 5m                 # how long a fetched value is reused (memory only); 1s to 24h
    allow_private: false          # secretsmanager.<region>.amazonaws.com may resolve to a private address (VPC endpoint)

providers:                        # examples; the wizard writes these. api_key accepts only secret:<name> or aws-sm:<secret>
  - {id: anthropic, type: anthropic, api_key: "secret:anthropic_api_key"}
  - id: openai
    type: openai
    base_url: "https://api.openai.com/v1"
    api: auto                     # auto | responses | chat (openai, openai_compat); auto = responses for openai, chat for openai_compat
    api_key: "secret:openai_api_key"
    enabled: false
  - {id: gemini, type: gemini, api_key: "secret:gemini_api_key", enabled: false}
  - id: ollama
    type: ollama                  # native /api/chat; num_ctx = models[].context, else min(trained, max(16384, served context from /api/ps))
    base_url: "http://host.docker.internal:11434"
    allow_private: true           # this exact host:port only; metadata IPs always blocked
    discover: true                # /api/tags + /api/show
    models:
      - {id: "qwen3:14b", context: 16384, max_output: 2048, tools: true}
  - id: openrouter
    type: openai_compat
    base_url: "https://openrouter.ai/api/v1"
    api_key: "secret:openrouter_api_key"
    enabled: false
  - id: bedrock
    type: bedrock                 # Amazon Bedrock: the AWS default credential chain (role, SSO profile, env), never an api_key
    region: us-west-2             # required for bedrock
    profile: ""                   # shared-config profile; "" = AWS_PROFILE or the default chain
    discover: true                # ListInferenceProfiles + ListFoundationModels: the ids this account can invoke
    enabled: false

models:                           # example chains: "<provider id>/<model>", in fallback order
  main: [anthropic/claude-sonnet-4-6, ollama/qwen3:14b]
  fast: [anthropic/claude-haiku-4-5, ollama/qwen3:4b]

pricing:                          # USD per 1M tokens; overrides the embedded catalog. Optional
                                  # tiers, keyed by the prompt size they apply above:
                                  #   tiers: {"200000": {permille: 2000, out_permille: 1500}}
                                  # without them the catalog's tiers apply to these prices
  "ollama/qwen3:14b": {in: 0, out: 0}

agent:
  name: Bothy                     # what it calls itself in chats; 1-40 characters, one line
  tone: warm                      # warm | direct | formal
  persona_file: SOUL.md           # a Markdown file name (*.md, no path) in data_dir; admin-editable only
  standing_rules: []              # admin-only lines appended to the constitution block
  busy_mode: steer                # steer | queue
  coalesce_window: 1200ms
  queue_depth: 8
  max_concurrent_runs: 4
  max_runs_per_user: 2
  tool_output_tokens: 4000
  parallel_read_tools: 4
  show_thinking: false
  session_idle_reset: 0s          # 0 = never start a new channel session automatically
  refine:                         # v0.2: drafts, critique and a vote for final answers; per message or session too
    enabled: false
    drafts: 3                     # 1..8 independent drafts
    depth: 2                      # 1..4 rounds of continue → critique → correct per draft
    early_stop: true              # stop deepening once every draft agrees
    critic_slot: main             # main | fast

context:
  reserve_output: auto            # auto = min(8192, 12% of window), floor 1024; or an integer
  compact_below: 32768            # windows below this use the compact profile
  degraded_below: 16384           # windows below this use the degraded profile
  min_context: 8192               # below this: text-only (no tools)
  compaction: {trigger: 0.70, compact_trigger: 0.60, keep_recent_turns: 4, summary_max_tokens: 1200, auto_pin: true}

budgets:
  daily_usd: 5.00                 # global; 0 = unlimited (security-audit finding)
  member_daily_usd: 2.00          # each member in multi mode, and each person in a chat app in either mode
  per_user_daily_usd: {}          # example: {alice: 3.00, "telegram:5001": 4.00} (a username, or <app>:<id> for a person in a chat app)
  chat_share_pct: 60              # share of daily_usd that people and channels in chat apps may use together; 100 = no separate share
  warn_ratio: 0.8
  run:       {max_steps: 25, max_tokens: 400000, max_time: 10m, max_usd: 0}
  scheduled: {max_steps: 15, max_tokens: 150000, max_time: 15m, max_usd: 0.25}
  heartbeat_daily_usd: 0.25
  sender_rate: {per_minute: 10, burst: 5}
  loop: {warn_after: 3, abort_after: 5}

policy:
  preset: careful                 # careful | balanced | hands_off
  defaults: {}                    # example: {external_send: ask}; overrides the preset per class
  rules:                          # first match wins; cannot override floors
    - {id: readonly-shell, tool: shell, argv0: [ls, cat, head, tail, grep, rg, wc, pwd, find], verdict: auto}
    - {id: wiki, tool: web_fetch, domains: ["*.wikipedia.org"], verdict: auto}
    - {id: no-pipe-to-shell, tool: shell, match: {"/argv/2": "*curl*|*sh*"}, verdict: ask, reason: "pipes a download into a shell"}
  approval:
    interactive_timeout: 15m
    interactive_on_timeout: deny  # deny | park
    scheduled_timeout: 2m
    scheduled_on_timeout: park
    park_ttl: 24h
    api_wait: 120s                # tokens with on_approval: wait
    notify: []                    # no longer used: web accounts hear about approvals on the web
  trifecta: {enforce: true}       # LOCKED

sandbox:
  backend: auto                   # auto | sandboxd | bwrap | none
  socket: /run/bothy/sandboxd.sock
  profile: base                   # base | node
  limits: {memory_mb: 512, cpus: 1.0, pids: 256}
  exec_timeout: 120s
  output_kb: 64
  workspace_quota_mb: 2048
  idle_timeout: 10m

egress:
  allow: []                       # host or host:port; "*.suffix" allowed here (admin)
  deny: []                        # always denied, beats allow and grants
  presets: {python_packages: false, npm_packages: false}
  ports: [443]
  allow_plain_http: false         # also allows port 80
  hold_for_approval: 45s          # 0 = off (immediate 403)
  caps: {session_up_mb: 10, session_down_mb: 200}
  shared_tenant_suffixes: [.amazonaws.com, .cloudfront.net, .googleusercontent.com, .blob.core.windows.net,
                           .workers.dev, .pages.dev, .ngrok-free.app, .github.io]
  dns: {servers: [], max_ttl: 60s}

search:
  provider: none                  # searxng | brave | tavily | none (the wizard sets it)
  searxng_url: "http://searxng:8080"
  searxng_allow_private: true     # the compose service is on a private network; this exact host:port only
  brave_api_key: "secret:brave_api_key"
  tavily_api_key: "secret:tavily_api_key"
  max_results: 8

web_fetch: {max_bytes: 2097152, timeout: 20s, max_redirects: 5}

channels:
  unknown_senders: pair           # pair (admins can let people in) | ignore (no reply or notice; still listed in Approvals); Telegram and Discord
  usage_footer: false
  telegram: {enabled: false, token: "secret:telegram_bot_token", groups: {mention_only: true},
             api_base: "https://api.telegram.org"}                           # api_base fields are LOCKED; http only for a loopback host
  discord:  {enabled: false, token: "secret:discord_bot_token", application_id: "", groups: {mention_only: true},
             api_base: "https://discord.com/api/v10", gateway_url: ""}
  slack:    {enabled: false, app_token: "secret:slack_app_token", bot_token: "secret:slack_bot_token", groups: {mention_only: true},
             api_base: "https://slack.com/api/"}

github:                           # v0.2: connections and the repository allowlist live in the database (Settings → Connections)
  api_base: "https://api.github.com"   # LOCKED; GitHub Enterprise Server: https://<host>/api/v3; http only for a loopback host
  checkout: {max_mb: 256, max_files: 20000}

mcp:
  servers:                        # examples
    - name: docs
      transport: http
      url: "https://mcp.example.com/mcp"   # a key in the query must be a reference (?api_key=secret:<name>); plain http only for loopback or allow_private when it sends a key
      headers: {Authorization: "Bearer secret:docs_mcp_token"}
      side_effects: {default: read}
    - name: notes
      enabled: false
      transport: stdio
      profile: node
      package: {ecosystem: npm, name: "@modelcontextprotocol/server-filesystem", version: "2026.7.1", integrity: "sha512-…"}
      command: ["node", "node_modules/@modelcontextprotocol/server-filesystem/dist/index.js", "/workspace/notes"]
      egress: []                  # runtime host[:port] allowlist for this server's box
      workspace: principal        # none | principal (mount the caller's workspace at /workspace)
      side_effects: {default: local_write, "read_*": read, "list_*": read}
      private: ["read_*"]         # outputs that count as private data
      trusted_output: true
      core: []                    # tool globs pinned into the core tools block; every other MCP tool is lazy

memory: {memory_max_bytes: 4096, user_max_bytes: 2048, heartbeat_max_bytes: 2048, team_max_bytes: 4096, tainted_writes: ask}   # tainted_writes LOCKED: ask | deny
skills: {dir: "", index_budget_tokens: 600}                                   # "" = <work_dir>/skills

scheduler:
  enabled: true
  missed_run: skip                # skip | once
  heartbeat: {enabled: false, every: 30m, active_hours: "08:00-22:00", slot: fast, deliver: preferred}

api: {openai_compat: true, passthrough: true}

knowledge:                        # v0.2: Bothy's own index of checked-out repos and its project notes
  enabled: true
  auto_notes: true                # draft NOTES.md after a repo's first index (fast slot)
  max_file_kb: 256
  max_files: 20000
  max_repo_mb: 64
  notes_max_bytes: 16384
  context_bytes: 2048             # notes of touched repos per turn-context; 0 = none

releases:                         # v0.2: release_deploy dispatches a GitHub Actions workflow once approvers agree
  github_token: "secret:github_token"   # only for repos on no GitHub connection's allowlist
  github_api: "https://api.github.com"  # LOCKED
  poll_interval: 15s
  follow_for: 6h
  environments: []                # example: [{name: staging, repo: acme/web, workflow: deploy.yml, ref_pattern: main,
                                  #   inputs: {target: staging}, approvers: [admin], required: 2}]
```

### 7.2 Layers, env and secrets

**Precedence**, lowest to highest: built-in defaults < `bothy.yaml` (`$BOTHY_CONFIG` or `<data>/bothy.yaml`) < environment < `<data>/bothy.ui.yaml` (UI overrides).

- **Env names:** `BOTHY_` + upper-cased path with `__` between segments: `BOTHY_BUDGETS__DAILY_USD=2`, `BOTHY_SERVER__PUBLIC_URL=…`. List items keyed by `id`/`name` are addressable: `BOTHY_PROVIDERS__OLLAMA__BASE_URL=…`, `BOTHY_MCP__SERVERS__DOCS__URL=…`. Lists and maps take JSON: `BOTHY_MODELS__MAIN='["openai/gpt-5.1"]'`.
- **Merging:** deep merge; lists of objects with `id`/`name` merge by key; scalar lists replace.
- **Secret fields** (`secret:"true"` tag: `api_key`, `token`, `app_token`, `bot_token`, `brave_api_key`, `tavily_api_key`, `github_token`, `client_secret`, header values) accept only `secret:<name>` and (v0.2, §14.8) `aws-sm:<secret name or ARN>[#<json key>][?region=<r>&profile=<p>]` references, optionally embedded in a header string (`"Bearer secret:docs_mcp_token"`). Plaintext is a validation error: *"Move this secret to the vault: Settings → Vault, or `BOTHY_SECRET_DOCS_MCP_TOKEN`."* A malformed aws-sm reference is reported without echoing it; an aws-sm reference without `?region=` or an ARN needs `secrets.aws.region` (the error is reported on that key, and `Set` refuses the change).
- **Secrets from env:** `BOTHY_SECRET_<NAME>` or `BOTHY_SECRET_<NAME>_FILE` (Docker secrets) satisfy `secret:<name>` with source `env`; held in memory only, never written. `<NAME>` is the vault name upper-cased with `.` and `-` replaced by `_` (`docs_mcp_token` → `BOTHY_SECRET_DOCS_MCP_TOKEN`). Env secrets apply to every scope lookup (they shadow vault entries of the same name). Well-known keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `GROQ_API_KEY`, `XAI_API_KEY`, `DEEPSEEK_API_KEY`, `TELEGRAM_BOT_TOKEN`) are only *offered* by the wizard ("Found ANTHROPIC_API_KEY. Import into the vault?"); never used silently.
- **Locks:** fields tagged `locked:"true"` (`server.listen`, `server.tls`, `server.trusted_proxies`, `server.local_cidrs`, `server.insecure_lan`, `storage.*` dirs, `vault.*`, `sandbox.socket`, `auth.admin_can_read_chats`, `policy.trifecta`, `memory.tainted_writes`, `skills.dir`, `channels.*.api_base`, `channels.discord.gateway_url`, `auth.sign_in.*.api_base`) are never settable from the UI layer. `BOTHY_CONFIG_LOCK="providers.*,policy.*"` additionally locks globs (GitOps); locked keys show as "locked" in the UI and the UI layer is ignored for them.
- **Server-written fields** (`server:"true"`: `auth.sign_in.github.org_ids`) hold values Bothy works out itself. They may sit in `bothy.ui.yaml`, `bothy.yaml` or the environment, but `Set` and `Reset` refuse them, at their own path and through any parent path whose value would set them, unless the context carries `config.ServerWrite`, which only the code that computed the value passes (`PATCH /api/sign-in/github`). Setting or resetting a parent without it leaves their UI value in place.
- **Validation per key:** a key that fails validation falls back to the next lower valid layer and is reported in `Effective.Errors` (banner `config_error` + doctor). For example `agent.persona_file` must be a bare Markdown file name in `data_dir` (`*.md`, no path separators; `config.ValidPersonaFile`), since `PUT /api/persona` overwrites it next to `bothy.yaml`, the database, the setup token and the KEK: anything else is rejected and falls back to `SOUL.md`, and the persona endpoints return 503 if an invalid value still reaches them. Messages carry source and suggestions: `bothy.yaml:42: models.main[0]: unknown provider "antropic" (did you mean "anthropic"?)`.
- **Safe mode:** if `bothy.yaml` cannot be parsed, the gateway boots with defaults + env + ui, `SafeMode=true`, banner `safe_mode`, and only auth, setup, config, doctor and health routes active (agent, channels, scheduler off): `httpapi` answers every other `/api` and `/v1` route with 503 `{"error":{"code":"unavailable","message":"safe mode: fix bothy.yaml"}}`, and `cmd/bothy` does not start the hub, scheduler or MCP loops (§12.1). Fixing the file and `Reload` (mtime poll or SIGHUP) leaves safe mode only after a restart. It never exits 1 for config errors. An invalid `server.*` falls back to `127.0.0.1:8080`.
- **Hot reload:** fields tagged `reload:"hot"` apply immediately (watchers get `old, cur`); others require restart and are listed in `Effective.Restart` with a banner offering **Restart** (`POST /api/admin/restart` drains, then `syscall.Exec` of `os.Executable()`, which works in distroless; on Windows, which has no exec(2), it starts a fresh process with the same arguments, environment and standard streams (`os.StartProcess`) and exits). `bothy.yaml` is polled by mtime every 5 s and on SIGHUP.

### 7.3 UI writes and the effective-source view

- `config.Manager.Set` validates each change against the fully merged result, rejects locked keys, server-written keys (§7.2) and secret literals, lets a `config.Check` on the context (`config.WithCheck`: the config in effect and the one the change would leave) refuse it before anything is written, as `Reset` does too (`httpapi` passes one for bearer tokens, §8.3), writes the sparse `bothy.ui.yaml` atomically (temp file, fsync, rename, 0600), audits each path as `config.set` with old and new values (secret references only, never values) through the `OnSet` hook, and notifies watchers. `Reset` deletes a key from `bothy.ui.yaml` (`config.reset`). Paths address list items by id/name: setting `providers.<id>` or `mcp.servers.<name>` to an object creates or replaces that item; setting it to `null` removes the UI-layer item. The config package has no bus: `cmd/bothy` publishes `config.changed` from a `Watch` callback using `config.Diff(old, cur)` and `Effective().Restart`, plus the `restart_required` banner.
- `GET /api/config` returns `config.Effective`: for every leaf, the effective value, its source (`default`, `file` with file:line, `env` with variable name, `ui` with user and time), the shadowed lower layers with their values, `locked`, `hot` and `secret` flags.
- The UI shows a source chip per field (`default`, `file · bothy.yaml:61`, `env · BOTHY_BUDGETS__DAILY_USD`, `UI · alice · 2h ago`, `locked`); hovering shows shadowed values; **Reset** removes the UI override and previews the fallback value; a UI override that shadows env or file shows a warning chip ("overrides env value 5"). Boot logs list shadowing overrides. Settings → Advanced renders the merged YAML with source comments and offers `bothy.ui.yaml` for download.
- CLI: `bothy config check [file]`, `bothy config defaults` (the built-in defaults, with the container `server.listen` default under `BOTHY_DEPLOYMENT=container`), `bothy config effective` (the merged config as YAML with a source comment per value).

### 7.4 `internal/config` (skeleton-implemented, frozen)

`schema.go`:

```go
package config

import "time"

// Tags: yaml = key name; reload:"hot" fields apply without restart (default
// "restart"); secret:"true" fields accept only "secret:<name>" references;
// locked:"true" fields are never settable from the UI layer;
// server:"true" fields are written by Bothy itself: Set and Reset refuse
// them, and every path above them, unless the context carries ServerWrite.

type Config struct {
	Version   int    `yaml:"version"`
	Mode      string `yaml:"mode" reload:"hot"` // single | multi
	Timezone  string `yaml:"timezone" reload:"hot"`
	Telemetry bool   `yaml:"telemetry"` // reserved; no phone-home code exists

	Server    Server            `yaml:"server"`
	Storage   Storage           `yaml:"storage"`
	Log       Log               `yaml:"log" reload:"hot"`
	Auth      Auth              `yaml:"auth"`
	Vault     VaultCfg          `yaml:"vault"`
	Secrets   SecretsCfg        `yaml:"secrets" reload:"hot"` // v0.2: aws-sm references (§14.8)
	Providers []Provider        `yaml:"providers" reload:"hot"` // the provider facade and channel hub re-read Current() and restart clients on config.changed
	Models    Models            `yaml:"models" reload:"hot"`
	Pricing   map[string]PriceO `yaml:"pricing" reload:"hot"`
	Agent     Agent             `yaml:"agent" reload:"hot"`
	Context   ContextCfg        `yaml:"context" reload:"hot"`
	Budgets   Budgets           `yaml:"budgets" reload:"hot"`
	Policy    PolicyCfg         `yaml:"policy" reload:"hot"`
	Sandbox   SandboxCfg        `yaml:"sandbox"`
	Egress    EgressCfg         `yaml:"egress" reload:"hot"`
	Search    Search            `yaml:"search" reload:"hot"`
	WebFetch  WebFetch          `yaml:"web_fetch" reload:"hot"`
	Channels  Channels          `yaml:"channels"`
	GitHub    GitHubCfg         `yaml:"github" reload:"hot"`
	MCP       MCPCfg            `yaml:"mcp" reload:"hot"`
	Memory    MemoryCfg         `yaml:"memory" reload:"hot"`
	Skills    SkillsCfg         `yaml:"skills" reload:"hot"`
	Scheduler SchedulerCfg      `yaml:"scheduler" reload:"hot"`
	API       APICfg            `yaml:"api" reload:"hot"`
	Knowledge KnowledgeCfg      `yaml:"knowledge" reload:"hot"`
	Releases  ReleasesCfg       `yaml:"releases" reload:"hot"`
}

type Server struct {
	Listen         string   `yaml:"listen" locked:"true"`
	PublicURL      string   `yaml:"public_url"`
	AllowedHosts   []string `yaml:"allowed_hosts" reload:"hot"`
	TrustedProxies []string `yaml:"trusted_proxies" locked:"true"`
	LocalCIDRs     []string `yaml:"local_cidrs" locked:"true"`
	InsecureLAN    bool     `yaml:"insecure_lan" locked:"true"`
	TLS            TLS      `yaml:"tls" locked:"true"`
	MaxBodyMB      int      `yaml:"max_body_mb"`
	UploadMaxMB    int      `yaml:"upload_max_mb"`
}

type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type Storage struct {
	DataDir     string    `yaml:"data_dir" locked:"true"`
	WorkDir     string    `yaml:"work_dir" locked:"true"`
	RunDir      string    `yaml:"run_dir" locked:"true"`
	BackupsKeep int       `yaml:"backups_keep" reload:"hot"`
	Retention   Retention `yaml:"retention" reload:"hot"`
}

type Retention struct {
	EgressLog    Duration `yaml:"egress_log"`
	Runs         Duration `yaml:"runs"` // 0 = keep forever
	InboundDedup Duration `yaml:"inbound_dedup"`
}

type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

type Auth struct {
	SessionIdle       Duration `yaml:"session_idle" reload:"hot"`
	SessionMax        Duration `yaml:"session_max" reload:"hot"`
	PasswordMinLength int      `yaml:"password_min_length" reload:"hot"`
	Lockout           Lockout  `yaml:"lockout" reload:"hot"`
	Argon2            Argon2   `yaml:"argon2"`
	PairingTTL        Duration `yaml:"pairing_ttl" reload:"hot"`
	SetupTokenTTL     Duration `yaml:"setup_token_ttl"`
	AdminCanReadChats bool     `yaml:"admin_can_read_chats" reload:"hot" locked:"true"`
}

type Lockout struct {
	MaxFailures int      `yaml:"max_failures"`
	Base        Duration `yaml:"base"`
	Max         Duration `yaml:"max"`
}

type Argon2 struct {
	MemoryKiB   uint32 `yaml:"memory_kib"`
	Iterations  uint32 `yaml:"iterations"`
	Parallelism uint8  `yaml:"parallelism"`
}

type VaultCfg struct {
	KEK     string `yaml:"kek" locked:"true"`     // auto | file:/path | systemd:<name> | passphrase | env
	Keyfile string `yaml:"keyfile" locked:"true"` // autogen target
}

// SecretsCfg configures the cloud secret stores that secret fields may
// reference instead of the vault (v0.2, §14.8).
type SecretsCfg struct {
	AWS SecretsAWS `yaml:"aws"`
}

type SecretsAWS struct {
	Region       string   `yaml:"region"`        // required for references without ?region= or an ARN
	Profile      string   `yaml:"profile"`       // shared-config profile; "" = AWS_PROFILE or the default chain
	CacheTTL     Duration `yaml:"cache_ttl"`     // how long a fetched value is reused (memory only)
	AllowPrivate bool     `yaml:"allow_private"` // an interface VPC endpoint with private DNS
}

type Provider struct {
	ID           string            `yaml:"id"`
	Type         string            `yaml:"type"` // anthropic | openai | gemini | ollama | openai_compat | bedrock
	BaseURL      string            `yaml:"base_url"`
	API          string            `yaml:"api"` // openai, openai_compat: auto | responses | chat; "" = auto (responses for openai, chat for openai_compat)
	APIKey       string            `yaml:"api_key" secret:"true"`
	AllowPrivate bool              `yaml:"allow_private"` // exact host:port of base_url only
	Discover     bool              `yaml:"discover"`
	Enabled      *bool             `yaml:"enabled"` // nil = true
	Headers      map[string]string `yaml:"headers" secret:"true"`
	Models       []ProviderModel   `yaml:"models"`
	Region       string            `yaml:"region"`  // bedrock only (required): the AWS region, e.g. us-west-2
	Profile      string            `yaml:"profile"` // bedrock only: shared-config profile; "" = AWS_PROFILE or the default chain
}

type ProviderModel struct {
	ID        string `yaml:"id"`
	Context   int    `yaml:"context"`
	MaxOutput int    `yaml:"max_output"`
	Tools     *bool  `yaml:"tools"`
	Vision    bool   `yaml:"vision"`
}

type Models struct {
	Main []string `yaml:"main"` // "provider/model" fallback chain
	Fast []string `yaml:"fast"`
}

type PriceO struct { // USD per 1M tokens
	In         float64               `yaml:"in"`
	Out        float64               `yaml:"out"`
	CacheRead  float64               `yaml:"cache_read"`
	CacheWrite float64               `yaml:"cache_write"`
	Tiers      map[string]PriceTierO `yaml:"tiers"` // keyed by the prompt size they apply above ("200000"); unset keeps the catalog's
}

// PriceTierO is core.PriceTier: multipliers in permille (2000 = 2x) of the
// whole request once its prompt exceeds the tier's key; 0 = permille.
type PriceTierO struct {
	Permille           int `yaml:"permille"`
	OutPermille        int `yaml:"out_permille"`
	CacheReadPermille  int `yaml:"cache_read_permille"`
	CacheWritePermille int `yaml:"cache_write_permille"`
}

type Agent struct {
	Name              string   `yaml:"name"` // what Bothy calls itself in chats
	Tone              string   `yaml:"tone"` // warm | direct | formal
	PersonaFile       string   `yaml:"persona_file"`
	StandingRules     []string `yaml:"standing_rules"`
	BusyMode          string   `yaml:"busy_mode"` // steer | queue
	CoalesceWindow    Duration `yaml:"coalesce_window"`
	QueueDepth        int      `yaml:"queue_depth"`
	MaxConcurrentRuns int      `yaml:"max_concurrent_runs"`
	MaxRunsPerUser    int      `yaml:"max_runs_per_user"`
	ToolOutputTokens  int      `yaml:"tool_output_tokens"`
	ParallelReadTools int      `yaml:"parallel_read_tools"`
	ShowThinking      bool     `yaml:"show_thinking"`
	SessionIdleReset  Duration `yaml:"session_idle_reset"` // 0 = never
	Refine            Refine   `yaml:"refine"`
}

// Refine is refine mode (v0.2 F3): off by default; a message or a session
// can turn it on or off.
type Refine struct {
	Enabled    bool   `yaml:"enabled"`
	Drafts     int    `yaml:"drafts"` // breadth: independent drafts of the final answer
	Depth      int    `yaml:"depth"`  // rounds of continue → critique → correct per draft
	EarlyStop  bool   `yaml:"early_stop"`
	CriticSlot string `yaml:"critic_slot"` // main | fast
}

type ContextCfg struct {
	ReserveOutput string     `yaml:"reserve_output"` // "auto" or an integer
	CompactBelow  int        `yaml:"compact_below"`
	DegradedBelow int        `yaml:"degraded_below"`
	MinContext    int        `yaml:"min_context"`
	Compaction    Compaction `yaml:"compaction"`
}

type Compaction struct {
	Trigger          float64 `yaml:"trigger"`
	CompactTrigger   float64 `yaml:"compact_trigger"`
	KeepRecentTurns  int     `yaml:"keep_recent_turns"`
	SummaryMaxTokens int     `yaml:"summary_max_tokens"`
	AutoPin          bool    `yaml:"auto_pin"`
}

type Budgets struct {
	DailyUSD          float64            `yaml:"daily_usd"`
	MemberDailyUSD    float64            `yaml:"member_daily_usd"`   // each member in multi mode, and every person in a chat app
	PerUserDailyUSD   map[string]float64 `yaml:"per_user_daily_usd"` // username, or "<app>:<external id>" for a person in a chat app -> cap
	ChatSharePct      int                `yaml:"chat_share_pct"`     // share of daily_usd chat apps' people and channels may use together; 100 = no separate share
	WarnRatio         float64            `yaml:"warn_ratio"`
	Run               RunBudget          `yaml:"run"`
	Scheduled         RunBudget          `yaml:"scheduled"`
	HeartbeatDailyUSD float64            `yaml:"heartbeat_daily_usd"`
	SenderRate        Rate               `yaml:"sender_rate"`
	Loop              Loop               `yaml:"loop"`
}

// ChatShareUSD is daily_usd × chat_share_pct / 100, false without a separate
// share (no daily_usd, or 100); the budget service and GET /api/usage/today
// both use it.
func (b Budgets) ChatShareUSD() (float64, bool)

type RunBudget struct {
	MaxSteps  int      `yaml:"max_steps"`
	MaxTokens int      `yaml:"max_tokens"`
	MaxTime   Duration `yaml:"max_time"`
	MaxUSD    float64  `yaml:"max_usd"` // 0 = no per-run $ cap
}

type Rate struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

type Loop struct {
	WarnAfter  int `yaml:"warn_after"`
	AbortAfter int `yaml:"abort_after"`
}

type PolicyCfg struct {
	Preset   string            `yaml:"preset"`   // careful | balanced | hands_off
	Defaults map[string]string `yaml:"defaults"` // class -> auto|ask|deny; overrides the preset
	Rules    []Rule            `yaml:"rules"`
	Approval Approval          `yaml:"approval"`
	Trifecta Trifecta          `yaml:"trifecta" locked:"true"`
}

type Rule struct {
	ID         string            `yaml:"id"`
	Tool       string            `yaml:"tool"` // glob
	Argv0      []string          `yaml:"argv0"`
	Domains    []string          `yaml:"domains"`
	Paths      []string          `yaml:"paths"`
	Match      map[string]string `yaml:"match"` // JSON pointer -> glob over Action.Payload
	SideEffect string            `yaml:"side_effect"`
	Triggers   []string          `yaml:"triggers"`
	Verdict    string            `yaml:"verdict"` // auto | ask | deny
	Reason     string            `yaml:"reason"`
}

type Approval struct {
	InteractiveTimeout   Duration `yaml:"interactive_timeout"`
	InteractiveOnTimeout string   `yaml:"interactive_on_timeout"` // deny | park
	ScheduledTimeout     Duration `yaml:"scheduled_timeout"`
	ScheduledOnTimeout   string   `yaml:"scheduled_on_timeout"`
	ParkTTL              Duration `yaml:"park_ttl"`
	APIWait              Duration `yaml:"api_wait"`
	Notify               []string `yaml:"notify"` // no longer used: web accounts hear on the web only (kept so old files load)
}

type Trifecta struct {
	Enforce bool `yaml:"enforce"`
}

type SandboxCfg struct {
	Backend          string    `yaml:"backend"` // auto | sandboxd | bwrap | none
	Socket           string    `yaml:"socket" locked:"true"`
	Profile          string    `yaml:"profile" reload:"hot"`
	Limits           BoxLimits `yaml:"limits" reload:"hot"`
	ExecTimeout      Duration  `yaml:"exec_timeout" reload:"hot"`
	OutputKB         int       `yaml:"output_kb" reload:"hot"`
	WorkspaceQuotaMB int       `yaml:"workspace_quota_mb" reload:"hot"`
	IdleTimeout      Duration  `yaml:"idle_timeout" reload:"hot"`
}

type BoxLimits struct {
	MemoryMB int     `yaml:"memory_mb"`
	CPUs     float64 `yaml:"cpus"`
	Pids     int     `yaml:"pids"`
}

type EgressCfg struct {
	Allow                []string      `yaml:"allow"` // host or host:port; "*.suffix" allowed here (admin)
	Deny                 []string      `yaml:"deny"`
	Presets              EgressPresets `yaml:"presets"`
	Ports                []int         `yaml:"ports"`
	AllowPlainHTTP       bool          `yaml:"allow_plain_http"`
	HoldForApproval      Duration      `yaml:"hold_for_approval"` // 0 = off
	Caps                 EgressCaps    `yaml:"caps"`
	SharedTenantSuffixes []string      `yaml:"shared_tenant_suffixes"`
	DNS                  DNS           `yaml:"dns"`
}

type EgressPresets struct {
	PythonPackages bool `yaml:"python_packages"` // pypi.org, files.pythonhosted.org
	NPMPackages    bool `yaml:"npm_packages"`    // registry.npmjs.org
}

type EgressCaps struct {
	SessionUpMB   int `yaml:"session_up_mb"`
	SessionDownMB int `yaml:"session_down_mb"`
}

type DNS struct {
	Servers []string `yaml:"servers"` // "" = system resolver
	MaxTTL  Duration `yaml:"max_ttl"`
}

type Search struct {
	Provider            string `yaml:"provider"` // searxng | brave | tavily | none
	SearxngURL          string `yaml:"searxng_url"`
	SearxngAllowPrivate bool   `yaml:"searxng_allow_private"` // SystemDest.AllowPrivate for searxng_url's exact host:port
	BraveAPIKey         string `yaml:"brave_api_key" secret:"true"`
	TavilyAPIKey        string `yaml:"tavily_api_key" secret:"true"`
	MaxResults          int    `yaml:"max_results"`
}

type WebFetch struct {
	MaxBytes     int      `yaml:"max_bytes"`
	Timeout      Duration `yaml:"timeout"`
	MaxRedirects int      `yaml:"max_redirects"`
}

type Channels struct {
	UnknownSenders string   `yaml:"unknown_senders" reload:"hot"` // pair (admins can let people in) | ignore (no reply or notice; still in Approvals); Telegram and Discord
	UsageFooter    bool     `yaml:"usage_footer" reload:"hot"`
	Telegram       Telegram `yaml:"telegram" reload:"hot"`
	Discord        Discord  `yaml:"discord" reload:"hot"`
	Slack          Slack    `yaml:"slack" reload:"hot"`
}

type Groups struct {
	MentionOnly bool `yaml:"mention_only"`
}

type Telegram struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token" secret:"true"`
	Groups  Groups `yaml:"groups"`
	APIBase string `yaml:"api_base" locked:"true"` // https://api.telegram.org (self-hosted Bot API servers, tests)
}

type Discord struct {
	Enabled       bool   `yaml:"enabled"`
	Token         string `yaml:"token" secret:"true"`
	ApplicationID string `yaml:"application_id"`
	Groups        Groups `yaml:"groups"`
	APIBase       string `yaml:"api_base" locked:"true"`    // https://discord.com/api/v10 (tests)
	GatewayURL    string `yaml:"gateway_url" locked:"true"` // "" = from GET /gateway/bot (tests)
}

type Slack struct {
	Enabled  bool   `yaml:"enabled"`
	AppToken string `yaml:"app_token" secret:"true"` // xapp-
	BotToken string `yaml:"bot_token" secret:"true"` // xoxb-
	Groups   Groups `yaml:"groups"`
	APIBase  string `yaml:"api_base" locked:"true"` // https://slack.com/api/ (tests)
}

type MCPCfg struct {
	Servers []MCPServer `yaml:"servers"`
}

type MCPServer struct {
	Name          string            `yaml:"name"` // ^[a-z0-9][a-z0-9-]{0,31}$
	Enabled       *bool             `yaml:"enabled"`
	Transport     string            `yaml:"transport"` // stdio | http
	URL           string            `yaml:"url"`
	Headers       map[string]string `yaml:"headers" secret:"true"`
	AllowPrivate  bool              `yaml:"allow_private"`
	Profile       string            `yaml:"profile"` // base | node
	Package       *MCPPackage       `yaml:"package"`
	Command       []string          `yaml:"command"`      // relative to /opt/mcp after install
	Egress        []string          `yaml:"egress"`       // runtime host[:port] allowlist for the box
	Workspace     string            `yaml:"workspace"`    // none | principal
	SideEffects   map[string]string `yaml:"side_effects"` // glob -> class; "default" key
	Private       []string          `yaml:"private"`      // tool globs whose output is private data
	TrustedOutput bool              `yaml:"trusted_output"`
	Core          []string          `yaml:"core"` // tool globs pinned into the core tools block (default: lazy)
}

type MCPPackage struct {
	Ecosystem string `yaml:"ecosystem"` // npm | pypi
	Name      string `yaml:"name"`
	Version   string `yaml:"version"` // exact
	Integrity string `yaml:"integrity"`
}

type MemoryCfg struct {
	MemoryMaxBytes    int    `yaml:"memory_max_bytes"`
	UserMaxBytes      int    `yaml:"user_max_bytes"`
	HeartbeatMaxBytes int    `yaml:"heartbeat_max_bytes"`
	TeamMaxBytes      int    `yaml:"team_max_bytes"`               // TEAM.md: remembered for everyone
	TaintedWrites     string `yaml:"tainted_writes" locked:"true"` // ask | deny
}

type SkillsCfg struct {
	Dir               string `yaml:"dir" locked:"true"`
	IndexBudgetTokens int    `yaml:"index_budget_tokens"`
}

type SchedulerCfg struct {
	Enabled   bool      `yaml:"enabled"`
	MissedRun string    `yaml:"missed_run"` // skip | once
	Heartbeat Heartbeat `yaml:"heartbeat"`
}

type Heartbeat struct {
	Enabled     bool     `yaml:"enabled"`
	Every       Duration `yaml:"every"`
	ActiveHours string   `yaml:"active_hours"`
	Slot        string   `yaml:"slot"`
	Deliver     string   `yaml:"deliver"` // preferred | web | <channel name>
}

// GitHubCfg holds the operator-level GitHub settings; connections and the
// repository allowlist live in the database (Settings > Connections).
type GitHubCfg struct {
	// APIBase is locked because a non-default base (GitHub Enterprise Server,
	// tests) may sit on a private network, which the gateway then dials.
	APIBase  string         `yaml:"api_base" locked:"true"` // https://api.github.com; Enterprise Server: https://<host>/api/v3
	Checkout GitHubCheckout `yaml:"checkout"`
}

type GitHubCheckout struct {
	MaxMB    int `yaml:"max_mb"`    // unpacked size of one checkout; the download is capped at the same size
	MaxFiles int `yaml:"max_files"` // files of one checkout
}

type APICfg struct {
	OpenAICompat bool `yaml:"openai_compat"`
	Passthrough  bool `yaml:"passthrough"`
}

// ReleasesCfg configures release_deploy: named environments whose deploys
// dispatch a GitHub Actions workflow after their approvers agree.
type ReleasesCfg struct {
	GitHubToken  string       `yaml:"github_token" secret:"true"` // fine-grained token with Actions read and write on the repos below
	GitHubAPI    string       `yaml:"github_api" locked:"true"`   // https://api.github.com; GitHub Enterprise Server: https://<host>/api/v3
	PollInterval Duration     `yaml:"poll_interval"`              // how often a dispatched run is checked
	FollowFor    Duration     `yaml:"follow_for"`                 // stop following a run after this long
	Environments []ReleaseEnv `yaml:"environments"`
}

type ReleaseEnv struct {
	Name       string            `yaml:"name"`        // ^[a-z0-9][a-z0-9_-]{0,31}$
	Repo       string            `yaml:"repo"`        // owner/name
	Workflow   string            `yaml:"workflow"`    // file name under .github/workflows (deploy.yml) or a numeric id
	RefPattern string            `yaml:"ref_pattern"` // glob over the branch or tag to deploy: main, release/*, v*
	Inputs     map[string]string `yaml:"inputs"`      // fixed workflow_dispatch inputs
	Approvers  []string          `yaml:"approvers"`   // user ids (usr_…) or roles (owner, admin, member; a role admits higher ones)
	Required   int               `yaml:"required"`    // distinct approvers other than the requester: 1 or 2 (0 = 1)
}

// Duration unmarshals "15m", "720h", "1200ms"; marshals back the same way.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	*d = Duration(v)
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }
```

`knowledge.go` (v0.2) holds the knowledge section's type and validation:

```go
type KnowledgeCfg struct {
	Enabled bool `yaml:"enabled"`
	// AutoNotes drafts a repo's NOTES.md after its first index (fast slot,
	// metered against the principal's user).
	AutoNotes     bool `yaml:"auto_notes"`
	MaxFileKB     int  `yaml:"max_file_kb"`     // larger files are skipped
	MaxFiles      int  `yaml:"max_files"`       // per repo
	MaxRepoMB     int  `yaml:"max_repo_mb"`     // indexed bytes per repo
	NotesMaxBytes int  `yaml:"notes_max_bytes"` // per NOTES.md
	ContextBytes  int  `yaml:"context_bytes"`   // notes summaries per turn-context; 0 = none
}
```

**v0.2 validation** (`validate.go`, `knowledge.go`, `releases.go`):
- `providers.*.type` enum gains `bedrock`. A bedrock provider requires `region` in AWS region shape (`config.ValidAWSRegion`), refuses `api_key` and `headers` (requests are SigV4-signed), takes only an https `base_url` (VPC endpoint) and checks `profile` against the shared-config name charset (`config.ValidAWSProfile`); `region` and `profile` are refused on every other type. `api` stays openai/openai_compat only.
- `agent.refine`: `drafts` 1..8, `depth` 1..4, `critic_slot` main | fast.
- `agent.name` (v0.2 UI, hot): 1–40 runes, no leading or trailing space, no control or format characters, no `<` or `>` (`config.ValidAgentName`); `agent.tone` enum `warm` | `direct` | `formal`.
- `github` (hot): `api_base` a required http(s) URL (locked), `checkout.max_mb` 1..8192, `checkout.max_files` 1..1000000.
- `knowledge` (hot): `max_file_kb` 1..4096, `max_files` 1..1000000, `max_repo_mb` 1..4096, `notes_max_bytes` 1024..1 MiB, `context_bytes` 0..65536.
- `releases` (hot): `github_token` is a secret reference (default `secret:github_token`); `github_api` (locked) is a required http(s) URL and http only for a loopback address (`config.PlainHTTPAllowed`); `poll_interval` and `follow_for` positive; each environment has a unique `name` (`^[a-z0-9][a-z0-9_-]{0,31}$`), `repo` owner/name, `workflow` a `.yml`/`.yaml` file name or numeric id, a `ref_pattern` glob, at most a bounded number of `inputs` with valid names, `approvers` as user ids (`usr_…`) or roles (owner, admin, member; no duplicates) and `required` 0..2 (0 means 1), with enough listed users when only users are named.

`api.go` (bodies shown are placeholders; the skeleton ships the real implementation with tests for: precedence matrix, file:line provenance, id-keyed env overrides, locked keys rejected, secret literals rejected, safe mode on broken YAML, atomic 0600 writes, hot vs restart classification, `BOTHY_CONFIG_LOCK` globs):

```go
// Package config loads the layered configuration: defaults < bothy.yaml < env <
// bothy.ui.yaml. It is fully implemented in the skeleton and frozen.
package config

import (
	"context"
	"encoding/json"
	"time"
)

type Layer string

const (
	LayerDefault Layer = "default"
	LayerFile    Layer = "file" // <data>/bothy.yaml or $BOTHY_CONFIG
	LayerEnv     Layer = "env"  // BOTHY_<PATH> with "__" between segments
	LayerUI      Layer = "ui"   // <data>/bothy.ui.yaml, written by the web UI
)

type Source struct {
	Layer  Layer     `json:"layer"`
	File   string    `json:"file,omitempty"`
	Line   int       `json:"line,omitempty"`
	EnvVar string    `json:"env_var,omitempty"`
	By     string    `json:"by,omitempty"` // UI layer: "user:<id>"
	At     time.Time `json:"at,omitzero"` // UI layer write time
}

type Shadow struct {
	Source Source          `json:"source"`
	Value  json.RawMessage `json:"value"` // secret refs shown as refs, never resolved
}

type Entry struct {
	Path     string          `json:"path"` // dotted; list items by id/name: "providers.ollama.base_url"
	Value    json.RawMessage `json:"value"`
	Source   Source          `json:"source"`
	Shadowed []Shadow        `json:"shadowed"`
	Locked   bool            `json:"locked"` // locked:"true" tag or BOTHY_CONFIG_LOCK glob
	Hot      bool            `json:"hot"`    // applies without restart
	Secret   bool            `json:"secret"`
}

type FieldError struct {
	Path    string `json:"path"`
	Source  Source `json:"source"`
	Message string `json:"message"` // includes "did you mean …" suggestions
}

type Effective struct {
	Entries  []Entry      `json:"entries"`
	Errors   []FieldError `json:"errors"` // a key failing validation falls back to the next lower valid layer
	SafeMode bool         `json:"safe_mode"`
	Restart  []string     `json:"restart_required"` // restart-only paths changed since boot
}

type Options struct {
	DataDir  string             // default /data; config file <DataDir>/bothy.yaml unless File set
	File     string             // $BOTHY_CONFIG
	Env      func() []string    // os.Environ in production
	Defaults func(*Config)      // optional extra defaulting (binary vs image listen address)
	OnSet    func(ChangeRecord) // audit hook (config.set / config.reset)
}

type ChangeRecord struct {
	Path   string          `json:"path"`
	Old    json.RawMessage `json:"old"`
	New    json.RawMessage `json:"new"` // null for reset
	By     string          `json:"by"`
	Action string          `json:"action"` // set | reset
}

// View is what modules depend on (fakeable with config.Static).
type View interface {
	Current() *Config // immutable snapshot; never mutate
	Watch(fn func(old, cur *Config)) (cancel func())
}

// Admin is the config surface used by the HTTP API (implemented by *Manager).
type Admin interface {
	View
	Effective() Effective
	Set(ctx context.Context, changes map[string]json.RawMessage, by string) (restart []string, err error)
	Reset(ctx context.Context, path, by string) error
	Schema() json.RawMessage
}

// Manager implements Admin.
type Manager struct{ _ [0]func() }

// Load never fails on a bad file: YAML parse errors put the manager in safe
// mode (defaults + env + ui) with Errors set. It fails only on unreadable data dir.
func Load(ctx context.Context, o Options) (*Manager, error) { return &Manager{}, nil }

func (m *Manager) Current() *Config                                { return nil }
func (m *Manager) Watch(fn func(old, cur *Config)) (cancel func()) { return func() {} }
func (m *Manager) Effective() Effective                            { return Effective{} }

// Set validates every change against the fully merged result, rejects locked and
// secret-literal values, writes bothy.ui.yaml atomically (temp, fsync, rename,
// 0600), calls OnSet per path, then notifies watchers.
func (m *Manager) Set(ctx context.Context, changes map[string]json.RawMessage, by string) (restart []string, err error) {
	return nil, nil
}

func (m *Manager) Reset(ctx context.Context, path, by string) error { return nil }

// Schema returns a JSON Schema (draft 2020-12) generated from Config tags, for
// UI forms.
func (m *Manager) Schema() json.RawMessage { return nil }

// SetOnSet installs the audit hook after the auditor exists.
func (m *Manager) SetOnSet(fn func(ChangeRecord)) {}

// Reload re-reads bothy.yaml (polled every 5s by mtime and on SIGHUP).
func (m *Manager) Reload(ctx context.Context) error { return nil }

// Default returns the built-in defaults (the values in SPEC §7.1).
func Default() *Config { return &Config{} }

// Diff returns the dotted leaf paths whose values differ (for config.changed).
func Diff(old, cur *Config) []string { return nil }

// Dirs are the resolved storage paths. storage.work_dir "" and vault.keyfile ""
// mean auto (SPEC §2.1: /work or /keys/kek when that directory is a mount point
// per /proc/self/mountinfo, else under data_dir). cmd/bothy and the vault use
// ResolveDirs; nothing else interprets the raw values.
type Dirs struct {
	Data, Work, Run, Keyfile string
	KeyfileColocated         bool // the KEK file lives on the same volume as the DB
	// LegacyWork and LegacyKeyfile are the single-container paths under Data
	// that the auto work dir and keyfile replaced because /work or /keys is
	// mounted; "" otherwise. MoveLegacyWork and the vault move them over.
	LegacyWork, LegacyKeyfile string
}

// ResolveDirs also moves the default run_dir /run/bothy, where it does not
// exist, to $XDG_RUNTIME_DIR/bothy or <data_dir>/run.
func ResolveDirs(c *Config) Dirs { return Dirs{} }

// MoveLegacyWork moves the workspaces of a single-container install from
// <data>/work into a /work volume mounted since (the move to compose), when
// that volume is still empty. On one filesystem the entries are renamed;
// across volumes the tree is copied into a temporary directory inside /work
// and renamed into place only once complete, so a failed copy leaves
// <data>/work as it was. It reports whether anything moved.
func MoveLegacyWork(d Dirs) (bool, error) { return false, nil }

// ValidPersonaFile reports whether name may be agent.persona_file: a bare
// Markdown file name. PUT /api/persona overwrites that file in the data dir,
// which also holds bothy.yaml, bothy.db, the setup token and the KEK.
func ValidPersonaFile(name string) bool { return false }

// Static wraps a fixed Config as a View for tests.
func Static(c *Config) View { return staticView{c} }

type staticView struct{ c *Config }

func (s staticView) Current() *Config                             { return s.c }
func (s staticView) Watch(func(old, cur *Config)) (cancel func()) { return func() {} }
```

Implementation notes for the lead: `Watch` callbacks run synchronously after a successful `Set`/`Reload`, in registration order, and must not block; parse each file layer into `yaml.Node` (go.yaml.in/yaml/v3) to keep line numbers; build a path-indexed map per layer; merge into `map[string]any`; decode the merged tree into `Config` with `KnownFields(true)`; record per-leaf provenance. Defaults live in `Default()` (the values in §7.1). `BOTHY_DEPLOYMENT=container` changes the default `server.listen` to `0.0.0.0:8080` (`bothy config defaults` applies it too). Pricing tiers validate as follows: `pricing.<ref>.tiers` keys must be non-negative integers (the prompt size, input + cache read + cache write, in tokens; an item error otherwise), `permille` must be 1000–100000, and `out_permille`, `cache_read_permille` and `cache_write_permille` 0 (= `permille`) or 1000–100000, since `core.Price.Cost` ignores multipliers below 1x. `PriceO` holds a map, so it is not comparable with `==`. `providers.*.api` is the enum `""`, `auto`, `responses`, `chat`; set on a provider of another type than `openai` or `openai_compat` it is an error and falls back to `""`. Providers stay `reload:"hot"`, so changing `api` rebuilds that provider's client.

---

## 8. HTTP API, streaming and auth (M11)

### 8.1 Middleware chain and auth mechanics

Order: request id → panic recovery → security headers → **Host check** → **exposure gate** → body limit → **query-credential rejection** → authenticate → **CSRF** → authorize (role, scope, Viewer) → per-token rate limit → handler. Handlers have the shape `func(w http.ResponseWriter, r *http.Request, a core.Actor) error` and always pass `a.Viewer` to services.

- **Security headers** on every response: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`, `Permissions-Policy: camera=(), microphone=(), geolocation=()`, `Cache-Control: no-store` on `/api/*` and `/v1/*`, HSTS when TLS. **No CORS headers are ever emitted.**
- **Host check:** `Host` must be in `allowed_hosts` ∪ {public_url host, `localhost`, `127.0.0.1`, `[::1]`} (with port) → else **421** (DNS-rebinding defence). Exactly `GET`/`HEAD` `/healthz` and `/readyz` skip the Host check and the exposure gate: load balancer health checks send the target IP as Host, over plain HTTP from the balancer's address and without cookies, and those two routes disclose nothing.
- **Exposure gate (peer IP, never the Host header):** the client IP is `RemoteAddr`, or the right-most untrusted `X-Forwarded-For` hop only when the peer is in `trusted_proxies`. A request from a trusted proxy that carries any forwarding header (`Forwarded`, `X-Forwarded-*`, `X-Real-IP`) is never *local*, whatever address it names: every host in those ranges can write any address there. The forwarded address still names the client for logs, audit and rate limits, and is classed *lan* or *public* by its address; a trusted peer sending no forwarding header (an ssh tunnel ending on the proxy's host) is classed by its socket address. Classes: *local* = loopback, the container's default gateway (read from `/proc/net/route`; that is where Docker's published-port traffic arrives from), `local_cidrs`; *lan* = RFC1918/ULA; *public* = everything else. Plain HTTP is served to local peers, to lan peers only with `insecure_lan` (locked, env/file only), and never to public peers (403 page explaining Tailscale/Caddy/TLS). Over TLS (native or `X-Forwarded-Proto: https` from a trusted proxy) all classes pass. Loopback is still authenticated like any client.
- **Query-credential rejection:** any `/api` or `/v1` request with a query parameter named `token`, `access_token`, `api_key`, `key`, `auth`, `password`, `cookie` or `gatewayUrl` → **400**. Credentials are never read from query strings (CVE-2026-25253 class).
- **Cookie:** 32 random bytes, stored as sha256. Name `__Host-bothy` (`Secure`) when TLS or trusted-proxy https; `bothy_session` on local plain HTTP. `HttpOnly; SameSite=Strict; Path=/`. Idle 7 days, absolute 30 days, rotated on login and privilege change; logout deletes the row; disabling a user deletes all their sessions and revokes tokens. The setup grant cookie `bothy_setup` lasts 30 min. The sign-in cookie of a Google or GitHub round trip (§15) is `__Host-bothy_signin` under TLS, `bothy_signin` otherwise: `HttpOnly; SameSite=Lax; Path=/`, 10 minutes, holding only the flow's random state; Lax because it must come back on the provider's top-level redirect, which Strict drops. The callback clears it. A refused provider sign-in sets the refusal cookie `__Host-bothy_signin_who` under TLS, `bothy_signin_who` otherwise: `SameSite=Lax; Path=/`, 60 seconds, not `HttpOnly`, holding the refused email or login (URL-escaped) for the sign-in page's sentence; the page reads it once and deletes it. The GitHub App flow cookie (§14.3) is `__Host-bothy_ghapp` under TLS, `bothy_ghapp` otherwise: `HttpOnly; SameSite=Lax; Path=/`, one hour, holding only a manifest round trip's random binding (kept in memory as its sha256, never put in a URL); the flow it names also holds the sha256 of the session cookie that started it, so GitHub's cross-site redirects, which arrive without the Strict session cookie, resume that session only while it is still the same active admin's.
- **CSRF:** Go's `http.CrossOriginProtection` (Sec-Fetch-Site / Origin vs Host; Go 1.25+) wraps **every** unsafe `/api` request, including login, setup and bearer requests (non-browser clients send neither header and pass). Cookie-authenticated unsafe requests additionally need `X-CSRF-Token` equal to the session's token (constant-time; returned by `/api/auth/me`; the setup grant has its own token from `/api/setup/claim`) **and** SameSite=Strict.
- **API tokens:** `Authorization: Bearer bothy_pat_<26-char id>_<43-char base62 secret>_<6-char base62 CRC32>`, where the id field is the 26 Crockford chars of `tok_<26>` without the `tok_` prefix; parse by fixed field lengths after the `bothy_pat_` prefix, never by splitting on `_`. The CRC (over `bothy_pat_<id>_<secret>`) lets secret scanners recognise the format. Stored as sha256 of the secret; compared in constant time after an id lookup. Scopes: `chat` (sessions API + `/v1` agent models), `llm` (`/v1` passthrough), `read` (GET `/api/*` + SSE), `admin` (admin routes). Default TTL 90 days; tokens without expiry are rejected, and `ttl` (nanoseconds) must be 1 minute to 400 days. Losing the admin role revokes the account's `admin`-scoped tokens, so a later promotion doesn't wake them. `/v1/*` accepts only bearer tokens; `/api/*` accepts cookies or bearer tokens with a matching scope.
- **Login:** one global Argon2id slot; unknown usernames run a dummy hash (equal timing); lockout per username and per IP (5 failures → 1 m, doubling to 1 h). Per-username failures are counted in memory for known and unknown names alike, with the same forgiveness after a quiet hour, so lockouts never reveal which usernames exist; `users.failed_logins` and `locked_until` only carry a known account's active lock across restarts; `subtle.ConstantTimeCompare` for setup token, CSRF and tokens. Provider sign-in (§15) has no password to guess and no lockout; its starts are limited to 20 per minute per client (an IPv4 address or an IPv6 /64), in a bucket of their own. The right password for a turned-off account gets 401 *this account is turned off* (audited `auth.login_failed` with `reason: disabled`) and counts toward no lockout. The password limiter and lockout stay keyed by the exact address, since a lockout on a /64 would lock out a whole office behind it, while a start costs only a provider round trip, so starts are keyed by /64 and one host can't dodge their limit by moving through the addresses of its prefix.
- **Setup token:** created at first boot while no owner exists: 32 random bytes, base32, 24 h (re-minted at boot if expired), stored as sha256 in `meta`, written to `<data>/setup-token` (0600) and printed (stderr) when it is minted (v0.2 UI, S1) as `Open <url>/setup#t=<code>`, followed by ` on this server` unless `public_url` is set, then `or paste the setup code <code>` and `(also in <data>/setup-token; valid for 24 hours)`, the validity in words; `<url>` is `public_url`, else the listen address dialled from the host (`https` when `server.tls` is set). `bothy setup-token` prints the same banner to stderr and only the code to stdout, for scripts. Later boots while setup is pending print `Open <url>/setup and paste the setup code from <data>/setup-token` and `(or print a new one with: bothy setup-token)` without the code. `serve` logs `gateway starting listen=… url=…` on every boot. `bothy setup-token` rotates it (Auth never caches the hash, so a rotation by the CLI process is seen at once). The SPA reads it from the URL fragment, posts it to `/api/setup/claim`, then removes it with `history.replaceState`. Setup routes answer non-local peers with 403 and a fix line (`ssh -L 8080:127.0.0.1:8080 <host>`, then open the printed URL locally).
- **One-time passwords (v0.2 UI, S9):** a browser session of an account with `must_change_password` (`Actor.MustChangePassword`) gets 403 `password_change_required` (fix `/password`) on every route except `GET /api/auth/me`, `POST /api/auth/logout` and `POST /api/auth/password`. Bearer tokens are not affected.
- **File downloads** (`/api/workspace/files`): `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Content-Type: application/octet-stream` and `Content-Security-Policy: sandbox`.
- **GitHub App handoff page** (`GET /api/github/apps/handoff`, §14.3): the one page whose form leaves Bothy. It replaces the policy for that response only: `Content-Security-Policy: default-src 'none'; script-src 'nonce-<n>'; style-src 'nonce-<n>'; form-action 'self' <GitHub web origin>; base-uri 'none'; frame-ancestors 'none'` (a fresh 16-byte nonce per response; the origin re-serialised from the parsed action URL; `'self'` because browsers apply `form-action` to the redirects answering the post, and GitHub may send the browser straight back to Bothy) and `Referrer-Policy: strict-origin`, so the form post's `Origin` header is real rather than `null` (GitHub already has that origin in the manifest). The global policy, with `form-action 'self'`, is unchanged everywhere else. The page auto-submits only on a fresh navigation and keeps a visible Continue on GitHub button, so Back from GitHub is not a trap.
- **Error envelope:** `{"error":{"code":"…","message":"…","fix":"…"}}` (`dto.Error`). A `forbidden`, `conflict` or `bad_request` message from a core sentinel error leaves out the sentinel's own word (`forbidden: admin only` → `admin only`); approval refusals name no approval id (`already decided`, `you already voted`). `sealed`, `no_sandbox` and `budget_exceeded` carry plain copy, and their `fix` is a web route: `/settings/security/keys`, `/settings/security`, and `/settings/general` for the daily limit or `/settings/advanced` for a task's limits. CSRF refusals use the generic code `forbidden`; the web client matches the message text `X-CSRF-Token` to refetch the token and retry once after another tab rotated the session (a distinct `csrf` code would be cleaner; not done in v0.1). Bodies capped at `max_body_mb` (1 MB) except uploads (`upload_max_mb`, 20 MB, streamed to the workspace).

### 8.2 Web streaming protocol (SSE)

`GET /api/events` (cookie or bearer `read`). Response headers `Content-Type: text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no`. Frames:

```
id: <boot>-<seq>
event: <core.EventType>
data: <JSON core.Event>

```

- Every stream starts with `retry: 3000` (the browser's reconnect delay), then the server sends `event: hello` with `{"boot":"…","seq":N,"paused":false,"sandbox":true,"banners":[BannerData…]}` (banners from `Bus.Banners` under the connection's filter). For a fresh client (no `Last-Event-ID`) `seq` is the bus head, from `events.Bus.Seq()`: a method of the concrete bus, chosen over changing the frozen `core.Bus` interface, which `httpapi` reaches through an interface assertion (events between the last matching event and the head cannot match the filter, so nothing is skipped).
- `: ping` comment every 15 s; writes flushed per event with a write deadline (`http.ResponseController`).
- **Resume:** the browser sends `Last-Event-ID`; same boot and within the ring → replay via `Bus.Since`; otherwise `event: resync` (`data: {}`) and the client refetches sessions, active runs and approvals over REST. A per-connection buffer of 256; overflow → `resync` and close. At most 4 SSE connections per user (oldest closed); the evicted stream gets `: closed: this user has too many open streams` and `retry: 60000` before it closes, so tabs past the cap cannot reconnect in a storm. The web UI shares one `EventSource` per user per browser across tabs: a Web Lock elects a leader tab that holds the stream and relays frames, `hello`, status and `closed` over a `BroadcastChannel`; the lock and channel names include the user id, so another user's tabs in the same browser never share it. On leader handoff every tab refetches once. A joining tab gets the leader's current `hello` addressed to it alone, and the leader keeps that `hello` current with `banner` and `sandbox.status` events. An explicit sign-out, or a cookie that now belongs to another user, is relayed so every tab of that user closes the stream and signs out; a plain 401 is not relayed.
- **ACL:** `Filter{Principals: actor.Viewer.Principals, Admin: actor.Role.IsAdmin()}`. Admins receive admin broadcasts (Principal "") but **not** other principals' content (`admin_can_read_chats` is locked false).
- **Coalescing:** `run.delta`/`run.thinking` ≤ 20/s per run; `run.tool_output` ≤ 10/s per call, ≤ 8 KB per event, redacted. Each flush of these three is released only up to the last whitespace short of a possible partial secret (§5.5 step 5; a partial word is held back, at most 2 KiB before a cut at a non-token character) and redacted on its own, so redaction always sees whole secrets and tokens and the joined stream never reveals one.

| Event | Payload (`data.data`) | Scope |
|---|---|---|
| `run.queued` | `RunQueuedData{position}` | principal |
| `run.started` | `RunStartedData{trigger, model}` | principal |
| `run.status` | `RunStatusData{status: thinking\|tool\|waiting_approval\|compacting\|queued, detail}` | principal |
| `run.delta` | `DeltaData{text}` | principal |
| `run.thinking` | `DeltaData{text}` | principal |
| `run.tool_call` | `ToolCallData{call_id, tool, title, action, verdict, rule}` | principal |
| `run.tool_output` | `ToolOutputData{call_id, stream, chunk}` | principal |
| `run.tool_result` | `ToolResultData{call_id, status, preview, bytes, ms, truncated, untrusted}` | principal |
| `run.usage` | `UsageData{call: CallInfo (incl. breakdown), run_cost_micros, day_cost_micros, day_limit_micros}` | principal |
| `run.parked` | `RunParkedData{approval_id}` | principal |
| `run.finished` | `RunFinishedData{status, error, steps, usage, cost_micros}` | principal |
| `run.refine` (v0.2) | `RefineData{phase: start\|entry\|done, drafts, depth, max_calls (start), entry (entry), trace (done, entries omitted)}` | principal (bus only: never sent to chat sinks) |
| `approval.requested` | `ApprovalData{approval}` (with card; v0.2 UI: `approval.requester`, and `approval.approvals` on multi-approver ones) | principal; `AdminOnly` ones also admin; multi-approver ones also each eligible approver's principal |
| `approval.voted` (v0.2) | `ApprovalData{approval}` (with votes, `approvals` and `requester`): a vote that did not resolve a multi-approver approval | principal and each eligible approver's principal |
| `approval.resolved` | `ApprovalResolvedData{approval_id, status, choice, by, via, comment}` (`comment`: the deciding approver's reason, v0.2 UI) | principal; `AdminOnly` ones also admin; multi-approver ones also each eligible approver's principal |
| `message.created` | `MessageData{message}` | principal |
| `session.updated` | `SessionData{title, taint, epoch}` | principal |
| `memory.updated` | `MemoryData{file, event_id}` | principal; file `TEAM` (remembered for everyone): admin broadcast, without the run |
| `egress.blocked` | `EgressBlockedData{host, port, reason}` | principal |
| `schedule.run` | `ScheduleRunData{schedule_id, status}` | principal |
| `budget.warning` | `BudgetWarningData{user_id, spent_micros, limit_micros}` | principal |
| `repo.checkout` (v0.2) | `RepoCheckoutData{repo, ref, sha, workspace_id, dir, files}`: `repo_checkout` wrote a repository into the workspace; the knowledge module indexes it | principal (Session and Run set when a run asked) |
| `knowledge.updated` (v0.2) | `KnowledgeUpdatedData{repo: IndexedRepo, deleted}`: an index or notes state change | principal |
| `release.dispatched` (v0.2 UI) | `ReleaseDispatchedData{deploy_id, environment, repo, ref, url}`: GitHub accepted a recorded deploy | the deploy's principal and admin (Session and Run set; an admin who asked gets both copies, so clients dedupe by `deploy_id`) |
| `release.finished` (v0.2 UI) | `ReleaseFinishedData{deploy_id, environment, repo, ref, status: succeeded\|failed\|unknown, ok, url, duration, failed_step}`: following ended, or GitHub refused the dispatch (`ok` false, no `url`) | as `release.dispatched` |
| `inbox.changed` (v0.2 UI) | `InboxChangedData{item}`: someone waiting to be let in, a person an admin changed, or a group chat item (`dto.InboxItem.id`); refetch `GET /api/inbox` | admin |
| `channel.status` | `ChannelStatusData{status}` | admin |
| `sandbox.status` | `SandboxStatusData{status}` | admin (members get `banner`) |
| `mcp.pin_pending` | `MCPPinData{server, tools}` | admin |
| `config.changed` | `ConfigChangedData{paths, restart_required}` | admin |
| `banner` | `BannerData{level, code, text, action, clear}`; codes include `unknown_sender` (someone is waiting to be let in), `new_channel` (Bothy was added to a group chat), `team_access` (the 0013 report, §6) and `chat_budget_80` (§5.12) | admin or principal |

SSE-only control frames: `hello`, `resync`.

### 8.3 Routes (Go 1.22+ ServeMux patterns)

Auth column: **none**; **setup** (setup grant cookie, local peers only, 404 once setup is complete); **user** (cookie or bearer `chat`); **user (cookie)** (a signed-in browser session only; bearer tokens → 403); **read** (cookie or bearer `read`); **admin** (owner/admin cookie or bearer `admin`); **admin (cookie)** (owner/admin browser session only; bearer tokens → 403); **bearer:x** (`/v1`, token scope x). "Own" means filtered by the actor's Viewer. Types refer to `core` and `dto` (§4, §8.4). Paginated lists use `?before=&limit=` and return `dto.List[T]`.

| Method + path | Auth | Request | Response |
|---|---|---|---|
| `GET /healthz` | none (no Host check or exposure gate) | — | `200 ok` (text) |
| `GET /readyz` | none (no Host check or exposure gate) | — | `200 {"ok":true}` / `503 {"ok":false}` (DB, migrations, config) — no versions |
| `GET /api/setup/state` | none (local peers) | — | `dto.SetupState` (v0.2 UI: `install` is `compose`, `docker`, `binary` or `unknown`, from `KUBERNETES_SERVICE_HOST`/`ECS_CONTAINER_METADATA_URI(_V4)` → unknown, `BOTHY_DEPLOYMENT` not `container` → binary, `sandbox.backend: sandboxd` → compose, else docker) |
| `POST /api/setup/claim` | none (local, rate-limited 5/min) | `dto.SetupClaimReq` | `dto.SetupClaimResp` + `bothy_setup` cookie |
| `POST /api/setup/owner` | setup | `dto.SetupOwnerReq` (v0.2 UI: optional `display_name`) | `dto.Me` + session cookie |
| `POST /api/setup/complete` | admin (during setup) | — | `204`; deletes the token file; setup routes 404 afterwards |
| `POST /api/auth/login` | none (rate-limited) | `dto.LoginReq` | `dto.Me` + cookie |
| `POST /api/auth/logout` | user | — | `204` |
| `GET /api/auth/me` | user | — | `dto.Me` (v0.2 UI: `paused_by`, `paused_by_name`, `paused_at` while paused) |
| `GET /api/me` | user | — | `dto.Profile` (v0.2 UI; `h_uistate.go`: the caller's id, username, display name and time zone) |
| `PATCH /api/me` | user (cookie only) | `dto.ProfilePatch` | `dto.Profile` (v0.2 UI: anyone renames themselves (at most 64 printable characters without format characters, as for admin renames, and not another person's display name or username: 400) and sets their own time zone (an IANA name; empty, `Local` or unknown: 400; the owner's change re-syncs their heartbeat); decoded strictly, so an unknown field such as the old `approvals_in` → 400; audited `user.profile`. Bearer tokens → 403, like voting) |
| `POST /api/auth/password` | user (cookie) | `dto.PasswordReq` | `204` (rotates the session). v0.2 UI: `old` may be empty while `must_change_password` is set, which this clears; the one-time password itself is refused as the new one (400) |
| `GET /api/auth/methods` | none (safe mode too) | — | `dto.SignInMethods` (§15: the providers turned on and usable now; `[]` during setup, in safe mode, or when none is) |
| `POST /api/auth/oauth/{provider}/start` | none (cross-origin protection still applies; 20/min per IPv4 address or IPv6 /64) | `dto.SignInStartReq` | `dto.SignInStartResp` + the sign-in cookie. `{provider}` is `google` or `github` (else 404); 409 `wrong_address` (`fix` = the `public_url` origin) when the request's host or scheme differs from `public_url`; 409 `sign_in_unavailable` when the provider is off or not usable |
| `GET /api/auth/oauth/{provider}/callback` | none (safe mode too) | query `code`, `state`, `error` | always `303` to a fixed path of the web app: the flow's `next` with the session cookie on success, `/settings/account?connected=<p>` after a link, else `/login?signin_error=<code>&provider=<p>[&next=…]` (`/settings/account?…` for a link). Clears the sign-in cookie; never echoes the code or state. `not_allowed` and `unverified` name the refused email or login only in the 60-second refusal cookie (§8.1), never in the address, so a crafted link can't make the page name anyone; any other outcome clears a leftover refusal cookie |
| `GET /api/me/sign-ins` | user (cookie) | — | `dto.MySignIns` (§15) |
| `POST /api/me/sign-ins/{provider}` | user (cookie; 20/min per IPv4 address or IPv6 /64) | — | `dto.SignInStartResp` + the sign-in cookie; 409 when the person already connected that provider; the flow is bound to this session |
| `DELETE /api/me/sign-ins/{provider}` | user (cookie) | — | `204`; 404 when not connected; 409 for the last way into an account without a password |
| `GET /api/sign-in` | admin | — | `dto.SignInAdmin` |
| `PATCH /api/sign-in` | admin (cookie) | `dto.SignInListPatch` | `dto.SignInAdmin`; writes `auth.sign_in.allow` (trimmed, lower-cased, "@acme.com" read as the domain, repeats dropped) and `auth.sign_in.allow_new`; 400 with `fields` from `config.FieldErrors` |
| `PATCH /api/sign-in/{provider}` | admin (cookie) | `dto.SignInPatch` | `dto.SignInAdmin`; a typed client secret is saved in the global vault as `<provider>_sign_in_client_secret` (or the next free `_<n>` when an env secret shadows it) and the config gets the `secret:` reference; a refused config change takes the vault write back; `orgs` for Google → 400. GitHub `orgs` are pinned as they are saved: each name without a pin is looked up (`GET /orgs/{org}` on api.github.com, no token, through `egress.SystemClient`; one 30-second deadline for all of them) and `auth.sign_in.github.org_ids` is written through `config.ServerWrite` as lower-cased name → id for the names saved: pins of names still listed are kept, those of removed names dropped; an unknown name is 400 with a field error on `auth.sign_in.github.orgs` ("GitHub has no organization called X."), GitHub unreachable is 502 `provider_error`, GitHub limiting requests (403 with `X-RateLimit-Remaining: 0`, or 429) is 502 `provider_rate_limited`, and nothing is saved in any of these cases |
| `POST /api/sign-in/{provider}/check` | admin | `dto.SignInCheckReq` | `dto.SignInCheck`: trades a made-up code with the given (or saved) client id and secret; `ok` when the provider accepted the client, else `code` (`key_rejected`, `provider_error`, transport codes) |
| `GET /api/tokens` | user (cookie) | — | `[]core.TokenRecord` (own) |
| `POST /api/tokens` | user (cookie) | `core.TokenSpec` | `dto.TokenCreateResp` (plaintext once) |
| `DELETE /api/tokens/{id}` | user (cookie) | — | `204`; 404 for another person's token or one already revoked |
| `GET /api/users` | admin | — | `[]core.User` (v0.2 UI: `last_login_at`, the last sign-in by any method; zero = never) |
| `POST /api/users` | admin (cookie; multi mode) | `dto.UserCreateReq` | `dto.UserCreateResp` (v0.2 UI: `core.User` plus `one_time_password` when no password was sent; that account has `must_change_password`) |
| `PATCH /api/users/{id}` | admin; a role change cookie only | `dto.UserPatchReq` | `core.User`; only the owner changes another admin (403 otherwise); turning an account off also stops its runs, queued turns and open approvals (`Runner.StopPrincipal`) |
| `POST /api/users/{id}/password` | admin (cookie) | — | `dto.PasswordResetResp` (v0.2 UI: a new one-time password, shown once; ends that user's sessions; the owner resets anyone else, admins only members, 403 otherwise; `h_onboarding.go`) |
| `DELETE /api/users/{id}` | admin; `purge=true` cookie only | `?purge=true` (optional) | `204` (disable + revoke sessions and tokens + stop its runs, as PATCH does; owner cannot be deleted, and only the owner deletes another admin). With `purge=true`: also destroys the user's boxes, deletes their vault user-scope secrets, grants, sign-in links (§15), memory files, workspaces and sessions, plus (v0.2) their project notes, knowledge index rows and GitHub checkout data under `<data>/github/checkouts/<ws>` (audit rows are kept, with the user id); this is the "deleting a principal wipes its credentials" guarantee and is covered by E19 |
| `GET /api/identities` | admin | `?status=` | `[]core.ChannelIdentity` |
| `PATCH /api/identities/{channel}/{external}` | admin (cookie) | `dto.IdentityPatchReq{status?: teammate\|blocked, can_approve?}` | `dto.Teammate` (`auth.Service.SetTeammate`: let in someone pending whose chat app can't tell (`membership = unknown`), or a membership teammate Discord can no longer vouch for; unblock; block; Can approve (a teammate, not an outsider). Refusals are 400 in plain words: *"{App} decides who can use Bothy there. They get in when they message Bothy, if they're in the team."*, *"{App} says they aren't in the team."*, *"Let them in first."*, *"They aren't in the team any more."*. Afterwards: pending → teammate greets them (`Hub.Greet`); blocking stops their runs (`Runner.StopPrincipal`); Can approve on tells them (`Hub.TellApprover`); `inbox.changed`; and the 0013 report and its `team_access` banner go. Audited `identity.let_in`, `identity.unblock`, `identity.block`, `identity.approver`) |
| `GET /api/teammates` | admin | — | `[]dto.Teammate`: teammates and blocked people, approvers first, then by last active, then blocked; each with today's spending and `left` when the chat app says they're no longer in the team |
| `DELETE /api/teammates/{channel}/{external}` | admin (cookie) | — | `204`: deletes the data of someone blocked or no longer in the team (400 *"Block them first."* otherwise): their runs stop, then their chats, runs, boxes, schedules, grants, memory, project notes (`Knowledge.Purge`) and files are purged through a viewer of that principal alone, and `Auth.ForgetTeammate` moves the identity off the principal and deletes the principal in one transaction (a blocked person stays blocked); audited `identity.forget` with the counts |
| `GET /api/spaces` | admin | — | `[]dto.SpaceView`: group chats with their tool profile |
| `PATCH /api/spaces/{id}` | admin; turning a channel on or setting `tool_profile: standard` cookie only | `dto.SpacePatchReq` | `dto.SpaceView`; group chats only (a person space → 404); `enabled` and `tool_profile` change in one transaction, any `enabled` change sets `reviewed_at`, turning a channel off stops its runs, and the hub's list of servers with a channel on is refreshed; audited `space.update`. Turning a channel on lets in everyone Bothy answers there (on Discord, every member of the server), so like `PATCH /api/identities` it needs a signed-in admin: a bearer token gets 403 for `enabled: true` or `tool_profile: standard`, and may still rename, change `mention_only`, turn a channel off or make it `restricted` |
| `DELETE /api/spaces/{id}` | admin (cookie) | — | `204`: deletes a channel that is off (400 *"Turn it off first."* otherwise; a person space → 404): its runs stop, then its chats, runs, boxes, schedules, grants, memory, project notes and files are purged through a viewer of that principal alone, and `Principals().Delete` removes the principal with everything that cascades from it; the hub's list of servers with a channel on is refreshed and `inbox.changed` sent; audited `space.delete` with the title, chat app and counts. If Bothy is still in the chat, its next message there records the chat again as new (§5.1) |
| `GET /api/team/schedules` | admin | — | `[]dto.TeamSchedule`: teammates' and channels' schedules with their owner's name, kind (`teammate` \| `channel`) and chat app |
| `GET /api/sessions` | user | `?principal=&archived=&q=&before=&limit=` | `dto.List[core.Session]` (v0.2 UI: only sessions holding a message, `SessionFilter.NonEmpty`; an untitled one carries its draft title) |
| `POST /api/sessions` | user | `dto.SessionCreateReq` | `core.Session` (`Runner.NewSession`, origin `web`). v0.2 UI: with `message` the first message is checked before anything is stored and submitted in the same request → `202 dto.SessionCreated` (the session plus `run`); a refused submit archives the new session and returns the error |
| `GET /api/sessions/{id}` | user | — | `dto.SessionDetail` (v0.2: `refine` is the session's own setting, `on`, `off` or `""`) |
| `PATCH /api/sessions/{id}` | user | `dto.SessionPatchReq` | `core.Session` (v0.2: `refine` `on`, `off` or `""` to follow `agent.refine.enabled`; anything else 400) |
| `DELETE /api/sessions/{id}` | user | — | `204` (archive) |
| `GET /api/sessions/{id}/messages` | user | `?before=<seq>&limit=` | `dto.MessagePage` (v0.2 UI: tool_call parts carry `state` and `approval_id`, derived on read; `approvals` lists the approvals of the page's calls, decided ones and egress holds (`<call>#net`) included, with `decided_by_name`) |
| `POST /api/sessions/{id}/messages` | user | `dto.SendReq` | `202 core.SubmitResult` (v0.2: `refine` true or false for this message; omitted follows the session, then `agent.refine.enabled`) |
| `POST /api/sessions/{id}/stop` | user | — | `dto.StopResp` |
| `POST /api/sessions/{id}/compact` | user | — | `202` |
| `GET /api/sessions/{id}/context` | user | — | `core.Breakdown` (preview of the next call) |
| `GET /api/sessions/{id}/refine-estimate` | user | — | `dto.RefineEstimate` (v0.2 UI: `usd` = the average cost of the chat's last 3 priced answer steps × `multiplier`, left out without a priced answer; `multiplier` = drafts × (1 + 3 × rounds), rounds = depth, or (depth+1)/2 with early stop) |
| `POST /api/sessions/{id}/fork` | user | — | `core.Session` (`Runner.Fork`: clean fork, taint 0) |
| `GET /api/sessions/{id}/pins` | user | — | `[]core.Pin` |
| `POST /api/sessions/{id}/pins` | user | `dto.PinReq` | `core.Pin` |
| `DELETE /api/sessions/{id}/pins/{pin}` | user | — | `204` |
| `POST /api/uploads` | user | multipart `file`, field `session_id` (v0.2 UI: or `principal_id`, as a form field or query parameter, for a chat not stored yet) | `dto.Upload` (streamed to `<work>/ws/<principal workspace>/.bothy/in/<upl>/<sanitized name>`, 0660, ≤ `upload_max_mb`; `uploads` row) |
| `GET /api/runs` | user | `?session=&schedule=&status=&before=&limit=` | `dto.List[dto.RunView]` (v0.2 UI: `requested_by_name`, `chat_title`, `schedule_name`, `origin` (web, slack, telegram, discord, api or schedule), `reason` (the plain end of "Failed: …" or "Stopped …") and `refined`) |
| `GET /api/runs/{id}` | user | — | `dto.RunDetail` (v0.2: approvals carry their votes; `refine` is the run's last refine trace; v0.2 UI: `run` is a `dto.RunView`) |
| `GET /api/refine` | user | — | `dto.RefineInfo{enabled, drafts, depth, early_stop, critic_slot, max_calls, multiplier}` (v0.2; `max_calls` = drafts·(1+3·depth), for the composer's cost note; `multiplier` as in the refine estimate, for chats not stored yet) |
| `POST /api/runs/{id}/continue` | user (cookie only) | — | `202 core.SubmitResult` (bearer tokens → 403: a continued run is interactive and keeps the provenance of the run it continues) |
| `GET /api/approvals` | user | `?status=pending\|parked\|recent&before=&limit=` | `dto.List[core.Approval]` (`Approvals.Pending` filtered, `Next` 0; or `Approvals.Recent`, `Next` = last `created_at` unix ms; v0.2 UI: `decided_by_name`, `requester`, `approvals`, `comment`) |
| `GET /api/approvals/{id}` | user | — | `core.Approval` (v0.2: also multi-approver approvals the caller can vote on; `votes` filled) |
| `POST /api/approvals/{id}` | user (cookie only; approver) | `dto.ApprovalResolveReq` | `core.Approval` (bearer tokens → 403). v0.2 multi-approver approvals: records a vote and may return it still pending; only `allow_once`/`deny` (else 400); 403 when the requester allows or the caller is not an approver; 409 when the caller already voted or it is decided |
| `GET /api/approvals/{id}/votes` | user | — | `dto.ApprovalVotes` (v0.2; 400 for a single-approver approval, 404 when the caller cannot see it; v0.2 UI: each vote's `comment`) |
| `GET /api/inbox` | user | — | `dto.Inbox` (v0.2 UI, S12; `h_inbox.go`, appended in `New` like the GitHub and knowledge routes: what waits for a decision, §9.3) |
| `GET /api/grants` | user (own) / admin (everyone's, teammates' and channels' included) | `?scope=&scope_id=` | `[]dto.GrantView` (with `principal_name` and `created_by_name`, a chat approver as "{Name} · {App}") |
| `POST /api/grants` | admin | `dto.GrantCreateReq` (principal scope only) | `core.Grant` |
| `DELETE /api/grants/{id}` | user (own) / admin | — | `204` |
| `POST /api/policy/preview` | admin | `dto.PolicyPreviewReq` | `dto.PolicyPreviewResp` (Plan + Decide, no side effects) |
| `GET /api/events` | read | `Last-Event-ID` header | SSE (§8.2) |
| `GET /api/search` | user | `?q=&limit=` (per group, default 20, max 100) | `dto.SearchResp{chats, notes, code}` (v0.2 UI, replacing `[]core.SearchHit`: own principals; one row per chat, its best message, deleted chats left out; project-notes sections holding every word, else any word; knowledge code hits; plain-text snippets with UTF-16 `ranges`) |
| `GET /api/memory/{file}` | user | `?principal=` (default own) | `dto.MemoryDoc` |
| `PUT /api/memory/{file}` | user | `If-Match: <etag>` + `dto.MemoryPutReq` | `dto.MemoryDoc` / `409` |
| `GET /api/memory/{file}/history` | user | `?before=&limit=` | `dto.List[dto.MemoryChange]` (the events with `actor_name` for a person's edit and `chat {id, title}` for the chat Bothy made the change in, when the caller can open it; otherwise `run_id` is blank too and `actor` is just `agent:`) |
| `GET /api/memory/team` | user (any signed-in user; read tokens too) | — | `dto.MemoryDoc` with `file: "TEAM"`, `ETag` header (§14.9) |
| `PUT /api/memory/team` | admin (cookie with CSRF, or admin token, like other config writes) | `If-Match: <etag>` (`428` without) + `dto.MemoryPutReq` | `dto.MemoryDoc` / `409` stale / `400` over `memory.team_max_bytes` / `403` (`Memory.WriteTeam` checks the role again); audited `memory.edit` with `scope: team` |
| `GET /api/memory/team/history` | user | `?before=&limit=` | `dto.List[dto.MemoryChange]` like personal history; `run_id`, `chat` and the run in `actor` only for a chat the caller can open. The literal `team` routes win over `{file}`, which never accepts TEAM |
| `GET /api/memory/export` | user | `?principal=` (default own) | `application/x-tar` of MEMORY.md, USER.md, HEARTBEAT.md and `events.jsonl` (`Content-Disposition: attachment`, `CSP: sandbox`) |
| `GET /api/persona` | user | — | `{"content":"…"}` (reads `<data>/<agent.persona_file>`; the runner writes the default SOUL.md at boot when missing) |
| `PUT /api/persona` | admin | `{"content":"…"}` | `204` (atomic write 0600, ≤ 16 KB, audited `persona.update`; used at the next snapshot) |
| `GET /api/constitution` | user | — | `{"version":"…","content":"…"}` (`Runner.Constitution`, read-only) |
| `GET /api/agent/picture` | user | — | `image/png`, 1024 px: the admin's picture or the default (`AgentPicture.Current`), `ETag: "<version>"`, `Cache-Control: private, no-cache` (304 on `If-None-Match`); `?download=1` adds `Content-Disposition: attachment; filename=<agent.name>-picture.png` (the name kept to letters, digits, `-` and `_`; "Bothy" when nothing is left) |
| `GET /api/agent/picture/info` | user | — | `dto.PictureInfo` (`custom`: an admin replaced the default; `version` changes with the picture, for the image address) |
| `PUT /api/agent/picture` | admin | the image as the raw body, or a multipart form's `file` part; PNG, JPEG or WebP, ≤ 5 MB, ≥ 128 and ≤ 8000 px a side and at most 160 MB to decode (counting what the decoder keeps beside the image, such as a progressive JPEG's coefficients or an interlaced PNG's passes; the refusal suggests under 2500 px a side, which always fits), whatever type the client declares (`internal/picture` sniffs it, reads the size before decoding, crops the centre square, turns it upright, flattens it onto white, scales it to 1024 px and re-encodes it without metadata) | `dto.PictureChange` after `ChannelHub.ApplyPicture` (a minute at most, not cancelled by the client leaving); 400 with a plain sentence for a refused file, 413 over 5 MB; audited `picture.update` (bytes, version, each app's result) |
| `DELETE /api/agent/picture` | admin | — | `dto.PictureChange`: back to the default (`picture.png` removed), applied like `PUT`; audited `picture.reset` |
| `GET /api/skills` | user (enabled) / admin (all) | — | `[]core.SkillMeta` |
| `GET /api/skills/{name}` | user | — | `dto.SkillDetail` |
| `POST /api/skills` | admin | multipart zip (≤ 5 MB, ≤ 200 files) | `core.SkillMeta` (status pending) |
| `PATCH /api/skills/{name}` | admin | `dto.SkillPatchReq` | `core.SkillMeta` |
| `DELETE /api/skills/{name}` | admin | — | `204` |
| `GET /api/skills/{name}/files/{path...}` | user (enabled) / admin (all) | — | the file as `text/plain; charset=utf-8` with `Content-Security-Policy: sandbox` and `nosniff` (v0.2 UI: read through the skill's `os.Root`; 400 for a path that is not clean and relative (`..`, absolute, backslash), a special file or one over 256 KB; 404 for an unknown or, for members, disabled skill, a symlinked component or a missing file; 415 for a file that is not UTF-8 text) |
| `POST /api/skills/rescan` | admin | — | `[]core.SkillMeta` |
| `GET /api/schedules` | user | — | `[]core.Schedule` (own) |
| `POST /api/schedules` | user (own principals; humans need no approval: the self-modification lock targets the agent, whose `schedule_*` tools are admin-class under F3; members stay bounded by their daily cap) | `dto.ScheduleUpsertReq` | `core.Schedule` (`Scheduler.Upsert` assigns the id and gives an empty `tz` the caller's own time zone, as `schedule_create` does, then each `grants[]` entry goes through `Policy.Grant` with scope `schedule`, `scope_id` = the id, `principal_id` = the schedule's principal; whole-tool or admin/financial `max_se` grants → 400; non-empty `grants` from an API token → 403, since pre-authorized grants answer approvals in advance and need a browser session; audited `schedule.create`) |
| `GET /api/schedules/{id}` | user | — | `core.Schedule` |
| `PATCH /api/schedules/{id}` | user (admins also for team schedules) | `dto.ScheduleUpsertReq` | `core.Schedule` (the schedule's grants are replaced: old ones revoked, new ones granted as above; non-empty `grants` from an API token → 403). A team schedule takes only `{"enabled": bool}`; anything else → 400 *"Only turning it on or off can be changed here."* |
| `DELETE /api/schedules/{id}` | user (admins also for team schedules) | — | `204` |
| `POST /api/schedules/{id}/run` | user | — | `202 {"run_id":"…"}` (the run is the schedule owner's; `Scheduler.RunNow` audits `schedule.run` with the user who pressed it and the run) |
| `GET /api/schedules/{id}/runs` | user | `?before=&limit=` | `dto.List[dto.RunView]` |
| `POST /api/schedules/preview` | user | `dto.SchedulePreviewReq` | `dto.SchedulePreviewResp` (an empty `tz` is the caller's own time zone, as on save) |
| `GET /api/mcp` | admin | — | `[]core.MCPStatus` |
| `GET /api/mcp/{server}/pins` | admin | — | `[]core.MCPPin`: the definition each tool the server offers now has (a rollback to an earlier, superseded definition is pending again and listed; a tool it stopped offering is not), pending ones with the approved definition for diffing; `[]` for an unknown server |
| `POST /api/mcp/{server}/approve` | admin | `dto.MCPApproveReq` | `204`; approves or turns off the definition the server offers now; 404 for a tool it doesn't offer, 409 *"this action changed after it was shown. Review its change first"* when `hash` is not that definition |
| `POST /api/mcp/resolve` | admin | `MCPResolveReq{name, version?}` | `MCPResolveResp{name, version, integrity, command?}`: the exact version (latest when omitted), npm's `dist.integrity` (sha512) and the `command` that runs the package's `bin` from `/opt/mcp` (`["node", "node_modules/<name>/<path>"]`; with several, the one named like the package without its scope, else the first by name; none when it has no `bin` or only paths that leave its folder), fetched from `registry.npmjs.org` through the egress system client; nothing is installed. `400` for a name or version that isn't an npm one, `404` when npm has no such package or version |
| `POST /api/mcp/{server}/install` | admin | — | `202` |
| `POST /api/mcp/{server}/reload` | admin | — | `202` |
| `GET /api/providers` | admin | — | `[]core.ProviderHealth` (v0.2 UI: `source`, where the credentials come from in plain words: "Saved key", "Environment variable", "AWS Secrets Manager", or for bedrock "This server's AWS access (<how it signs in>)" once the chain has resolved; never the credentials) |
| `POST /api/providers/test` | admin | `dto.ProviderTestReq` | `core.ProbeResult` (v0.2 bedrock: `region` (AWS region shape) and optional `profile`; 400 for a bad region or profile, for `api_key`/`secret_ref` with bedrock and for region/profile on other kinds; the result carries `choices` and `credentials`. v0.2 UI: `model` (a probe without a model tests the recommended one), `code` (`core.FailureCode`) and the raw redacted `error` when it failed, and `choices` with plain names and suggestions) |
| `POST /api/providers/detect` | admin | — | `[]core.Detection` (v0.2: the bedrock detection, "Use this server's AWS access", comes first when the AWS chain resolves. v0.2 UI: `choices` with plain names, and provider keys already saved in the global vault as `{kind, base_url, secret_ref: "secret:<name>", secret_hint: <last 4>, note}`, matched by name prefix) |
| `POST /api/providers/connect` | admin | `dto.ProviderConnectReq` | `dto.ProviderConnectResp` (v0.2 UI, S6; `h_onboarding.go`: saves a pasted key or a well-known env var as `<id>_api_key` (or the next `_N` name a `BOTHY_SECRET_` variable does not shadow), or uses an existing `secret_ref`, then writes the provider and both slots, the chosen models first, in one `Config.Set`; without `id`, the kind's default id, or the next free one when another server holds it; a refused config write deletes a newly saved key and puts a replaced one back) |
| `GET /api/models` | user | — | `[]core.ModelInfo` (v0.2 UI: `display_name`, `short_name`, `provider_name`, and one `recommended` and one `quick_tasks_default` per provider) |
| `GET /api/github/repos` | user | — | `[]core.GitHubRepo` (v0.2: allowlisted repositories of every connection; `last_indexed_sha` is the commit the caller's own principal's GitHub-source index of the repository last read, omitted when none; 503 without a GitHub service, as every `/api/github` route) |
| `POST /api/github/repos/read` | user | `dto.GitHubReposReq` | `202 {"repos": [...]}` (checks the named allowlisted repositories, or all of them when `repos` is empty, out into the caller's own workspace in the background so Knowledge indexes them for that principal; existing checkouts are left alone; 400 when none of them is allowlisted) |
| `GET /api/github/repos/{owner}/{repo}/workflows` | admin | — | `[]core.GitHubWorkflow` (v0.2 UI: the repository's `.github/workflows/` files through its connection, for the environment dialog; 404 for a repository no connection allows; names cleaned to one plain line) |
| `GET /api/github/connections` | admin | — | `[]core.GitHubConnection` |
| `POST /api/github/connections/test` | admin | `dto.GitHubConnectReq` | `core.GitHubProbe` (stores nothing; an App with several installations returns them to choose from) |
| `POST /api/github/connections` | admin | `dto.GitHubConnectReq` | `core.GitHubConnection` (400 with GitHub's reason; 409 when a repo belongs to another connection that still allows it; 503 `sealed` when the vault is sealed; the allowed repositories are then read into the admin's own workspace as `POST /api/github/repos/read` does) |
| `GET /api/github/connections/{id}` | admin | — | `dto.GitHubConnectionDetail` (`app` set when Bothy created the connection's app) |
| `POST /api/github/connections/{id}/test` | admin | — | `core.GitHubProbe` (live re-test; records the status; lists reachable repos with `allowed`/`connection_id`) |
| `PUT /api/github/connections/{id}/repos` | admin | `dto.GitHubReposReq` | `[]core.GitHubRepo` (replaces the allowlist; empty allows none; newly allowed repositories are read into the admin's own workspace) |
| `DELETE /api/github/connections/{id}` | admin | — | `204` (removes the connection, its repos and the vault secret it created; retires the app Bothy made for it, §14.3) |
| `GET /api/github/apps` | admin | — | `[]core.GitHubOwnApp` (apps Bothy created, oldest first, with `state` pending\|connected\|retired and GitHub links) |
| `POST /api/github/apps` | admin (cookie only) | `dto.GitHubAppStartReq` | `dto.GitHubAppStartResp` and the `bothy_ghapp` flow cookie (§8.1) (400 bad account or target; 409 `conflict` while an app is pending; 409 `wrong_address` with the `public_url` origin as fix when opened elsewhere; 503 `sealed`) |
| `POST /api/github/apps/{id}/check` | admin | — | `dto.GitHubAppCheck` (connects the app's installation when it finds one; 400 with GitHub's reason; 409 for a retired app) |
| `POST /api/github/apps/{id}/retire` | admin | — | `core.GitHubOwnApp` ("Start over": deletes its keys, keeps the row; 409 while connected) |
| `DELETE /api/github/apps/{id}` | admin | — | `204` (dismisses a retired app's reminder; 409 otherwise) |
| `GET /api/github/apps/handoff` | none (safe mode too; flow cookie + session) | — | HTML page posting the manifest to GitHub, with its own CSP (§8.1); `303` to the GitHub page `?github_app=expired` without a matching flow, or `unavailable` |
| `GET /api/github/apps/created` | none (safe mode too; flow cookie) | GitHub's `code`, `state` | `303` to the app's install page (`<page>/installations/new?state=<new>`) once the code is redeemed; the same address again (Back, a reload) sends the browser there too, after waiting up to 30 s while the first request still redeems the code, or lands where a failed first request landed (`check` while it is still redeeming); the flow's state with another code (a second app made from GitHub's form) lands `not_finished`; otherwise `303 /settings/connections/github?github_app=not_finished\|exchange_failed\|keep_failed\|sealed[&apps=<org>\|me]` or `unavailable` (`exchange_failed` when GitHub's side failed: the code refused or expired, GitHub failing or out of reach, an answer without a usable app; `keep_failed` when Bothy couldn't keep the keys) |
| `GET /api/github/apps/installed` | none (safe mode too; flow cookie) | GitHub's `installation_id`, `setup_action`, `state` | `303 /settings/connections/github?github_app=installed\|requested\|updated\|check\|install_failed\|unavailable`; never JSON, never a code, state, id or error text in the address |
| `GET /api/knowledge/repos` | user | `?principal=` | `dto.List[core.IndexedRepo]` (v0.2; all of the caller's principals by default. v0.2 UI: `progress` while queued or indexing, `summary`, and `stale` when a GitHub repository's default branch has moved past `indexed_sha`, also on `GET …/{id}`) |
| `POST /api/knowledge/repos` | user | `dto.KnowledgeIndexReq` | `202 core.IndexedRepo` (status queued; 400 for a directory outside the workspace, a bad repo name or sha; 409 when a default `local/<name>` names another folder) |
| `GET /api/knowledge/repos/{id}` | user | — | `core.IndexedRepo` |
| `DELETE /api/knowledge/repos/{id}` | user | — | `204` (the index and the notes) |
| `GET /api/knowledge/repos/{id}/files` | user | — | `dto.List[core.KnowledgeFile]` (every indexed file of the repo) |
| `POST /api/knowledge/repos/{id}/reindex` | user | — | `202 core.IndexedRepo` |
| `GET /api/knowledge/repos/{id}/notes` | user | — | `core.KnowledgeNotes` + `ETag` (`""` when there are no notes) |
| `PUT /api/knowledge/repos/{id}/notes` | user | `If-Match` + `dto.KnowledgeNotesPutReq` | `core.KnowledgeNotes` + `ETag` (428 without If-Match, 409 stale, 400 over the cap; empty content deletes; a user save makes the notes trusted) |
| `POST /api/knowledge/repos/{id}/notes/draft` | user | — | `202 core.IndexedRepo` (409 while a draft runs) |
| `GET /api/knowledge/repos/{id}/notes/history` | user | — | `dto.List[dto.NotesChange]` (v0.2 UI: the notes' changes, newest first, at most 50 kept; 404 like the repo) |
| `GET /api/knowledge/repos/{id}/notes/history/{version}` | user | — | `dto.NotesChange` |
| `GET /api/knowledge/search` | user | `?q=&repo_id=&principal=&limit=` (1..50, default 20) | `dto.KnowledgeSearchResp` |
| `GET /api/releases` | user | — | `dto.Releases` (v0.2: environments and the newest 20 deploys: own, everyone's for admins; each environment also carries `last`, its newest deploy, and `running`, its deploys still followed, whoever asked, so every viewer reads what the environment is doing; another person's deploy comes without `session_id`, `run_id` and `chat`, which the viewer couldn't open. `release_status` likewise reports every deploy of an environment. v0.2 UI: deploys are `dto.DeployView`s with `requested_by_name` (a teammate's deploy: "Maya Chen · Slack"), `approved_by_names`, `short_sha` and the commit `subject`) |
| `GET /api/releases/deploys` | user | `?environment=&before=&limit=` | `dto.List[dto.DeployView]` (paged by `created_at` ms) |
| `GET /api/channels` | admin | — | `[]core.ChannelStatus` (Discord also says whether member lookups work, `member_check` `ok` \| `no_access`, and in how many servers no channel is on, `idle_servers`, so the UI writes its own sentences rather than reading `detail`; Slack names its app, `app_id` from the Socket Mode hello, so the UI can link to `https://api.slack.com/apps/<app_id>/general`) |
| `POST /api/channels/{name}/test` | admin | — | `dto.ChannelTestResp` (v0.2 UI: a failed test answers 502 with a `core.FailureCode` as the error code and the raw message, e.g. `key_rejected` for a refused Slack, Discord or Telegram token) |
| `POST /api/search/test` | admin | `dto.SearchTestReq` | `dto.SearchTestResp` (v0.2 UI: one search with unsaved settings, through the same egress path and checks as the `web_search` tool; a pasted key is used for this check only, redacted from the answer and never stored; without one the saved key is used. 400 for a bad provider or address; a failed search is 200 with `ok: false`, a `core.FailureCode` (`key_rejected`, `rate_limited`, `provider_error`, transport codes) and the raw redacted message) |
| `GET /api/channels/slack/manifest` | admin | — | `text/yaml` app manifest |
| `GET /api/channels/discord/invite` | admin | — | `dto.DiscordInvite` |
| `GET /api/config` | admin | `?schema=1` | `dto.ConfigView` |
| `PATCH /api/config` | admin; a change that could let people in cookie only | `dto.ConfigPatchReq` | `dto.ConfigPatchResp` / `400` with `config.FieldError`s; server-written keys (§7.2: `auth.sign_in.github.org_ids`) are refused (400) at their own path and inside the value of any parent path; only `PATCH /api/sign-in/github` writes it (§15). Like letting in one person (`PATCH /api/identities`), a change that could let people in needs a signed-in admin: a bearer token gets 403, judged on the config the change would leave (`config.WithCheck`, so through any parent path too), when any `auth.*` value would change (sign-in, its list and organizations, passwords, lockout, sessions), when team mode would turn on while `auth.sign_in.allow_new` is on, or when a chat app would turn on or, while on, get another token reference (Slack admits every member of its token's workspace). Every other key, and turning a chat app off, stays open to admin tokens |
| `DELETE /api/config/{path...}` | admin; as `PATCH`, a reset that could let people in cookie only | — | `204` (reset UI override; a server-written key below the path keeps its value); `400` for a server-written key itself; 403 for a bearer token when the value it falls back to would be refused by `PATCH` |
| `GET /api/vault/status` | admin | — | `core.VaultStatus` (`new_passphrase` while a passphrase vault waits for its first passphrase, which its first unseal chooses) |
| `POST /api/vault/import` | admin; a chat app's token cookie only | `dto.VaultImportReq` | `core.SecretMeta` (copies one well-known env key, e.g. `ANTHROPIC_API_KEY`, into the global vault; the value never leaves the process; audited `vault.put`); a bearer token gets 403 for the name a chat app's token setting names (`secret:<name>`), as for `PUT` |
| `GET /api/vault` | admin (`?scope=global\|user:<id>`) / user (own scope) | — | `[]core.SecretMeta` |
| `PUT /api/vault/{scope}/{name}` | admin / user (own scope); a chat app's token cookie only | `dto.VaultPutReq` | `core.SecretMeta`; the global secret a chat app's token setting names (`channels.telegram.token`, `channels.discord.token`, `channels.slack.app_token`/`bot_token`: `secret:<name>`) is written only by a signed-in admin, since another token can let people in (another Slack workspace's members); a bearer token gets 403, also when `POST /api/providers/connect` would save under that name |
| `DELETE /api/vault/{scope}/{name}` | admin / user (own scope) | — | `204` |
| `POST /api/vault/unseal` | admin | `dto.UnsealReq` | `core.VaultStatus`; an open vault has nothing to unlock: its status, no passphrase check and no `vault.unseal` row |
| `GET /api/usage` | user (own) / admin (all, `?user=`) | `?from=&to=&group_by=day\|model\|user\|schedule\|session` | `dto.UsageResp` (v0.2 UI: rows are `dto.UsageRowView` with a `name`: the person, the schedule or the chat title through the caller's own view, `""` for day and model; `group_by=day` keys are UTC dates, as the store documents; spend caps use days in the configured `timezone`, so the two can differ near midnight) |
| `GET /api/usage/today` | user | — | `dto.UsageToday` (`timezone` is the configured one, whose midnight resets the daily limits, which the UI names in the daily-limit stop; admins also get `chat_spent_micros` and `chat_limit_micros` when chat apps have a share of the daily limit, `Budgets.ChatShareUSD`: what people and channels in chat apps spent today together, `UsageRepo.TeamSpend`, and that share, which Spending shows in place of a `chat_budget_80` banner) |
| `GET /api/audit` | admin | `?actor=&action=&from=&to=&before=&limit=` | `dto.List[dto.AuditRow]` (v0.2 UI: `actor_name` and a one-line plain `summary`). Another person's schedule rows leave out its `name`, `spec` and `tz` and read "a schedule", as chats are named only through the reader's own view; an older row without `principal_id` (written by the handler) goes by its actor: the user, or the owner of the API token it names, and a row whose actor names no one Bothy can find reads "a schedule" too. A schedule change made over HTTP is one row, written by the scheduler with the request's actor, address and `grants` count |
| `GET /api/audit/verify` | admin | — | `dto.AuditVerify` |
| `GET /api/egress` | user | `?session=&before=&limit=` | `dto.List[dto.EgressRow]` (own principals; admins also see system-dialer rows; v0.2 UI: `outcome` `reached`, `allowed_unreachable` or `blocked`) |
| `GET /api/sandbox` | user | — | `dto.SandboxInfo` (`UsedMB`/`QuotaMB` from `Sandbox.Usage` of the caller's own workspace) |
| `GET /api/workspace/files` | user | `?principal=&path=` | directory listing JSON or file download (`Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`) via `os.Root` |
| `POST /api/admin/pause` | admin | `dto.PauseReq` | `204` (v0.2 UI: the `paused` banner names who paused and when, `core.PausedBanner`) |
| `POST /api/admin/restart` | admin | — | `202` (calls `Deps.Restart`; `cmd/bothy` drains every loop, then re-execs itself) |
| `POST /api/admin/backup` | admin | — | `{"path":"…"}` (`Store.Backup(<data>/backups)`, audited `backup.create`) |
| `GET /api/doctor` | admin | — | `dto.DoctorReport` |
| `POST /api/security-audit` | admin | `dto.SecurityAuditReq` | `dto.DoctorReport` |
| `GET /v1/models` | bearer:chat or llm | — | OpenAI list (§8.5) |
| `POST /v1/chat/completions` | bearer:chat (agent) / llm (passthrough) | OpenAI request | OpenAI response / SSE |
| `GET /{path...}` | none | — | SPA: hashed `/assets/*` with `Cache-Control: public, max-age=31536000, immutable`, served from the embedded `.gz` variant with `Content-Encoding: gzip` (unzipped on the fly, bounded by the recorded size, for clients without gzip); `index.html` `no-store`; unknown non-API paths → `index.html` |

### 8.4 DTOs (`internal/httpapi/dto`, skeleton-owned, frozen)

JSON conventions: field names are snake_case as tagged; times are RFC 3339 strings; `time.Duration` fields are integer nanoseconds (Go's default, mapped to `number` by tygo); money is integer micro-USD in fields ending `_micros`.

`make gen` runs tygo (tools module) over `internal/core` and `internal/httpapi/dto` and writes `web/src/lib/api/types.gen.ts`; CI fails if it is stale. `tools/tygo.yaml` maps `SideEffect`, and since the v0.2 UI `FailureCode` and `InstallKind`, to string-literal unions.

```go
// Package dto holds the JSON shapes of the REST API that are not core types.
// Frozen in the skeleton; web/src/lib/api/types.gen.ts is generated from it and
// from internal/core by tygo (make gen).
package dto

import (
	"encoding/json"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Error is the envelope for every non-2xx /api response.
type Error struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"` // bad_request | unauthorized | forbidden | password_change_required | not_found | conflict | busy | paused | budget_exceeded | rate_limited | sealed | no_sandbox | unavailable | internal; a failed POST /api/channels/{name}/test (502) carries a core.FailureCode
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type List[T any] struct {
	Items []T   `json:"items"`
	Next  int64 `json:"next,omitempty"` // pass as ?before= for the next page; 0 = end
}

// ---- setup / auth ----

type SetupState struct {
	NeedsSetup bool        `json:"needs_setup"`
	Claimed    bool        `json:"claimed"`        // a setup grant cookie is present and valid
	CSRF       string      `json:"csrf,omitempty"` // the claimed grant's CSRF token (like dto.Me.CSRF); empty unless Claimed
	OwnerSet   bool        `json:"owner_set"`
	Sandbox    bool        `json:"sandbox_available"`
	Install    InstallKind `json:"install,omitempty"` // always set; picks the command that prints a new setup code
	Version    string      `json:"-"`                 // never disclosed pre-auth
}

type SetupClaimReq struct {
	Token string `json:"token"`
}

type SetupClaimResp struct {
	CSRF string `json:"csrf"`
}

type SetupOwnerReq struct {
	DisplayName string `json:"display_name,omitempty"` // "" = the username
	Username    string `json:"username"`
	Password    string `json:"password"`
	Timezone    string `json:"timezone"`
}

type LoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Me struct {
	User       core.User         `json:"user"`
	CSRF       string            `json:"csrf"`
	Mode       string            `json:"mode"` // single | multi
	Principals []*core.Principal `json:"principals"`
	Paused     bool              `json:"paused"`
	// PausedBy, PausedByName and PausedAt say who paused Bothy and when,
	// while it is paused (empty for a pause recorded before they were kept).
	// PausedBy is a user id, or "channel:<app>:<id>" for an approver's
	// /stopall in a chat app.
	PausedBy     string            `json:"paused_by,omitempty"`
	PausedByName string            `json:"paused_by_name,omitempty"`
	PausedAt     time.Time         `json:"paused_at,omitzero"`
	Sandbox      bool              `json:"sandbox_available"`
	Banners      []core.BannerData `json:"banners"`
	Access       Access            `json:"access"` // how this request reached the server
}

// Profile is GET and PATCH /api/me: what people set about themselves.
type Profile struct {
	User        core.UserID `json:"user_id"`
	Username    string      `json:"username"`
	DisplayName string      `json:"display_name"`
	// Timezone is the person's IANA time zone, which their new schedules
	// and their chats with Bothy on the web use.
	Timezone string `json:"timezone"`
}

// ProfilePatch changes the caller's own profile; absent fields stay.
type ProfilePatch struct {
	DisplayName *string `json:"display_name,omitempty"`
	Timezone    *string `json:"timezone,omitempty"`
}

// Access is the caller's remote-access status for Settings > General.
type Access struct {
	Peer string `json:"peer"` // local | lan | public (exposure-gate class of the client address)
	TLS  bool   `json:"tls"`  // native TLS or X-Forwarded-Proto https from a trusted proxy
}

type PasswordReq struct {
	Old string `json:"old"` // may be "" while the user's must_change_password is set
	New string `json:"new"`
}

type TokenCreateResp struct {
	Token string            `json:"token"` // plaintext, shown once
	Meta  *core.TokenRecord `json:"meta"`
}

type UserCreateReq struct {
	Username    string    `json:"username"`
	Password    string    `json:"password,omitempty"` // "" = generate a one-time password (UserCreateResp)
	DisplayName string    `json:"display_name"`
	Role        core.Role `json:"role"`
}

type UserPatchReq struct {
	DisplayName *string    `json:"display_name,omitempty"`
	Role        *core.Role `json:"role,omitempty"`
	Disabled    *bool      `json:"disabled,omitempty"`
}

// IdentityPatchReq is an admin's change to a person in a chat app; absent
// fields stay.
type IdentityPatchReq struct {
	Status     *core.IdentityStatus `json:"status,omitempty"` // teammate (let in, unblock) | blocked
	CanApprove *bool                `json:"can_approve,omitempty"`
}

// Teammate is a person in a chat app as the People page lists them.
type Teammate struct {
	Channel    string              `json:"channel"`
	ExternalID string              `json:"external_id"` // also the budget key "<channel>:<external_id>"
	Name       string              `json:"name"`        // display name, else @handle, else "Someone"
	Handle     string              `json:"handle,omitempty"`
	Status     core.IdentityStatus `json:"status"` // teammate | blocked
	CanApprove bool                `json:"can_approve"`
	Left       bool                `json:"left"` // the chat app says they're no longer in the team
	LastSeen   time.Time           `json:"last_seen_at"`
	SpentToday core.Micros         `json:"spent_today_micros"`
	Principal  core.PrincipalID    `json:"principal_id,omitempty"`
}

// SpaceView is a group chat with its tool profile.
type SpaceView struct {
	core.Space
	ToolProfile string `json:"tool_profile"`
}

// TeamSchedule is a teammate's or a channel's schedule in the admins' list.
type TeamSchedule struct {
	ID         core.ScheduleID `json:"id"`
	Name       string          `json:"name"`
	Owner      string          `json:"owner"`      // teammate name or channel title
	OwnerKind  string          `json:"owner_kind"` // teammate | channel
	App        string          `json:"app"`
	Kind       string          `json:"kind"` // cron | once
	Spec       string          `json:"spec"`
	TZ         string          `json:"tz"`
	NextRun    time.Time       `json:"next_run_at,omitzero"`
	LastStatus core.RunStatus  `json:"last_status,omitempty"`
	Enabled    bool            `json:"enabled"`
}

// GrantView is a grant with the names of whom it covers and who made it.
type GrantView struct {
	core.Grant
	PrincipalName string `json:"principal_name"`
	CreatedByName string `json:"created_by_name"`
}

type SpacePatchReq struct {
	Title       *string `json:"title,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	MentionOnly *bool   `json:"mention_only,omitempty"`
	ToolProfile *string `json:"tool_profile,omitempty"` // restricted | standard (admin)
}

// ---- sessions / messages / runs ----

type SessionCreateReq struct {
	Principal core.PrincipalID `json:"principal_id,omitempty"` // default: caller's own
	Title     string           `json:"title,omitempty"`
	// Message, when set, is sent as the first message in the same request, so
	// a chat is stored only together with its first message (202 SessionCreated).
	Message *SendReq `json:"message,omitempty"`
}

type SessionPatchReq struct {
	Title         *string `json:"title,omitempty"`
	ModelOverride *string `json:"model_override,omitempty"` // "" clears
	Refine        *string `json:"refine,omitempty"`         // "on" | "off"; "" clears (follow agent.refine.enabled)
}

type SessionDetail struct {
	Session core.Session     `json:"session"`
	Pins    []*core.Pin      `json:"pins"`
	Active  *core.RunInfo    `json:"active,omitempty"`
	Pending []*core.Approval `json:"pending_approvals"`
	Refine  string           `json:"refine"` // the session's refine setting: "on" | "off" | "" (follows agent.refine.enabled)
}

type MessagePage struct {
	Messages []*core.Message `json:"messages"` // oldest first within the page; tool_call parts carry state and approval_id
	HasMore  bool            `json:"has_more"`
	// Approvals the page's tool calls asked for, pending or decided, so a
	// decided card stays in the transcript after a reload.
	Approvals []*core.Approval `json:"approvals"`
}

type SendReq struct {
	Text    string   `json:"text"`
	Uploads []string `json:"uploads,omitempty"` // upl_<26> ids from POST /api/uploads
	Refine  *bool    `json:"refine,omitempty"`  // refine this turn's answer; omitted = the session's setting
}

type Upload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

type PinReq struct {
	Text   string `json:"text,omitempty"`
	MsgSeq int64  `json:"msg_seq,omitempty"`
}

type StopResp struct {
	Stopped bool `json:"stopped"`
}

type RunDetail struct {
	Run       *RunView               `json:"run"`
	LLMCalls  []core.CallInfo        `json:"llm_calls"`
	ToolCalls []*core.ToolCallRecord `json:"tool_calls"`
	Approvals []*core.Approval       `json:"approvals"`
	Refine    *core.RefineTrace      `json:"refine,omitempty"` // the run's last refined answer; entries name their llm_calls
}

// RefineInfo is what the composer needs to offer refine mode and say what it
// costs: the defaults, and the most model calls one refined answer takes.
type RefineInfo struct {
	Enabled    bool      `json:"enabled"` // agent.refine.enabled: on for messages and sessions that do not say
	Drafts     int       `json:"drafts"`
	Depth      int       `json:"depth"`
	EarlyStop  bool      `json:"early_stop"`
	CriticSlot core.Slot `json:"critic_slot"`
	MaxCalls   int       `json:"max_calls"` // drafts × (1 + 3 × depth), the answer step included
	// Multiplier is about how many normal replies a refined one costs, early
	// stop considered: the refine estimate of a chat with no priced answer
	// yet, or of a new chat not stored until its first message.
	Multiplier float64 `json:"multiplier"`
}

type ApprovalResolveReq struct {
	Choice  core.Choice `json:"choice"`
	Comment string      `json:"comment,omitempty"`
}

type GrantCreateReq struct { // admin, persistent principal grants only
	Principal core.PrincipalID  `json:"principal_id"`
	Kind      core.GrantKind    `json:"kind"`
	Tool      string            `json:"tool,omitempty"`
	Pattern   map[string]string `json:"pattern,omitempty"`
	Value     string            `json:"value,omitempty"`
	MaxSE     core.SideEffect   `json:"max_se"`
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

type PolicyPreviewReq struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Session core.SessionID  `json:"session_id,omitempty"` // taint and grants context; default: a clean session
	Trigger core.Trigger    `json:"trigger,omitempty"`
}

type PolicyPreviewResp struct {
	Action   *core.Action  `json:"action"`
	Decision core.Decision `json:"decision"`
	Card     *core.Card    `json:"card,omitempty"` // when the verdict is ask
}

// ---- memory / skills / schedules ----

type MemoryDoc struct {
	File     core.MemoryFile `json:"file"` // MEMORY | USER | HEARTBEAT; TEAM from /api/memory/team
	Content  string          `json:"content"`
	ETag     string          `json:"etag"`
	CapBytes int             `json:"cap_bytes"`
}

type MemoryPutReq struct {
	Content string `json:"content"` // If-Match header carries the etag
}

// MemoryChange is one change in a memory file's history
// (GET /api/memory/{file}/history), with names for the person and chat.
type MemoryChange struct {
	core.MemoryEvent
	ActorName string    `json:"actor_name,omitempty"` // the person's name, for actor user:<id>
	Chat      *ChatLink `json:"chat,omitempty"`       // the chat Bothy made the change in, when the caller can open it
}

type SkillDetail struct {
	Meta  *core.SkillMeta `json:"meta"`
	Body  string          `json:"body"`
	Files []string        `json:"files"`
}

type SkillPatchReq struct {
	Enabled bool   `json:"enabled"`
	Hash    string `json:"hash,omitempty"` // SkillMeta.Hash of the reviewed files
}

type SchedulePreviewReq struct {
	Kind core.ScheduleKind `json:"kind"`
	Spec string            `json:"spec"`
	TZ   string            `json:"tz"`
}

type SchedulePreviewResp struct {
	Next  []time.Time `json:"next"`  // 5 occurrences
	Human string      `json:"human"` // "Weekdays at 08:00 (Europe/London)"
}

type ScheduleUpsertReq struct {
	Schedule core.Schedule `json:"schedule"`
	Grants   []core.Grant  `json:"grants"` // pre-authorized scopes (scope=schedule); narrow only
}

// ---- providers / channels / mcp / sandbox ----

type ProviderTestReq struct {
	Kind         core.ProviderKind `json:"kind"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKey       string            `json:"api_key,omitempty"` // write-only, used for this probe only; the wizard stores it with PUT /api/vault/global/<name>
	SecretRef    string            `json:"secret_ref,omitempty"`
	Model        string            `json:"model"`
	AllowPrivate bool              `json:"allow_private"`
	API          string            `json:"api,omitempty"`     // openai, openai_compat: auto | responses | chat, as providers[].api
	Region       string            `json:"region,omitempty"`  // bedrock: AWS region; "" = the server's AWS config
	Profile      string            `json:"profile,omitempty"` // bedrock: shared-config profile; "" = AWS_PROFILE or the default chain
}

type ChannelTestResp struct {
	Identity string `json:"identity"`
}

// SearchTestReq is POST /api/search/test: one search with these settings
// before they are saved. Without api_key the saved key is used; a private
// SearXNG address needs search.searxng_allow_private, as for the tool.
type SearchTestReq struct {
	Provider   string `json:"provider"`              // searxng | brave | tavily
	SearxngURL string `json:"searxng_url,omitempty"` // searxng
	APIKey     string `json:"api_key,omitempty"`     // brave, tavily; write-only, used for this check only
}

// SearchTestResp says whether the search worked; a failure carries why
// (Code) and the raw message (Error).
type SearchTestResp struct {
	OK    bool             `json:"ok"`
	Code  core.FailureCode `json:"code,omitempty"`
	Error string           `json:"error,omitempty"`
}

type DiscordInvite struct {
	URL string `json:"url"`
}

type MCPApproveReq struct {
	Tools map[string]core.MCPToolApproval `json:"tools"`
}

// MCPResolveReq names an npm package to pin; Version "" means the latest.
type MCPResolveReq struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// MCPResolveResp is the exact version and the registry's integrity hash to
// pin, so the admin never types a hash.
type MCPResolveResp struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

type SandboxInfo struct {
	Status  core.SandboxStatus `json:"status"`
	Compose string             `json:"compose_hint,omitempty"` // shown when unavailable
	QuotaMB int                `json:"quota_mb"`
	UsedMB  int                `json:"used_mb"`
}

// ---- config / vault / usage / audit / admin ----

type ConfigView struct {
	config.Effective
	Schema json.RawMessage `json:"schema,omitempty"`
}

type ConfigPatchReq struct {
	Set map[string]json.RawMessage `json:"set"` // dotted path -> JSON value
}

type ConfigPatchResp struct {
	RestartRequired []string `json:"restart_required"`
}

type VaultPutReq struct {
	Value string `json:"value"` // write-only
}

type VaultImportReq struct {
	EnvVar string `json:"env_var"` // one of the well-known names in SPEC §7.2; anything else => 400
	Name   string `json:"name"`    // vault name in the global scope, e.g. "anthropic_api_key"
}

type UnsealReq struct {
	Passphrase string `json:"passphrase"`
}

type UsageResp struct {
	Rows   []UsageRowView `json:"rows"`
	Totals core.UsageRow  `json:"totals"`
}

type UsageToday struct {
	Spent       core.Micros `json:"spent_micros"`
	Limit       core.Micros `json:"limit_micros"`
	GlobalSpent core.Micros `json:"global_spent_micros"` // admins only
	GlobalLimit core.Micros `json:"global_limit_micros"`
	Timezone    string      `json:"timezone"` // the configured zone, whose midnight resets the daily limits
}

type AuditVerify struct {
	OK    bool  `json:"ok"`
	BadID int64 `json:"bad_id,omitempty"`
}

type PauseReq struct {
	Paused bool `json:"paused"`
}

type SecurityAuditReq struct {
	Fix bool `json:"fix"`
}

type DoctorReport struct {
	Findings []core.Finding `json:"findings"`
	Fixed    []string       `json:"fixed,omitempty"`
}
```

v0.2 adds three files to the package (`github.go`, `knowledge.go`, `releases.go`), and the v0.2 UI server work (docs/design/ui.md §17) three more (`inbox.go`, `setup.go`, `activity.go`); `httpapi.Deps` gains `GitHub core.GitHub`, `Knowledge core.Knowledge` and `Deploys core.ReleaseRepo`, and every `/api/github` and `/api/knowledge` route answers 503 `unavailable` when its service is not wired (without `Deploys`, `GET /api/releases` lists no deploys and `GET /api/releases/deploys` answers `ErrNotImplemented`).

```go
// github.go
// ---- github (v0.2 F4) ----

// GitHubConnectReq is the body of POST /api/github/connections (connect) and
// POST /api/github/connections/test (probe only, nothing stored). Give
// exactly one of Token (auth "pat"), PrivateKey (auth "app", the .pem GitHub
// generated) or SecretRef (an existing "secret:<name>"). Token and PrivateKey
// are write-only: Connect stores them in the global vault scope and no
// response ever carries them.
type GitHubConnectReq struct {
	Auth           core.GitHubAuth `json:"auth"`
	Token          string          `json:"token,omitempty"`
	PrivateKey     string          `json:"private_key,omitempty"`
	SecretRef      string          `json:"secret_ref,omitempty"`
	AppID          string          `json:"app_id,omitempty"`
	InstallationID int64           `json:"installation_id,omitempty"` // App: 0 = its only installation
	Repos          []string        `json:"repos,omitempty"`           // connect: owner/name to allowlist
}

// GitHubReposReq replaces a connection's allowlist (PUT
// /api/github/connections/{id}/repos); an empty list allows none.
type GitHubReposReq struct {
	Repos []string `json:"repos"`
}

// GitHubConnectionDetail is GET /api/github/connections/{id}: the stored
// connection and every repository it holds, allowed or not.
type GitHubConnectionDetail struct {
	Connection *core.GitHubConnection `json:"connection"`
	Repos      []*core.GitHubRepo     `json:"repos"`
	App        *core.GitHubOwnApp     `json:"app,omitempty"` // set when Bothy created the connection's app
}

// GitHubAppStartReq starts creating Bothy's own GitHub App (POST
// /api/github/apps).
type GitHubAppStartReq struct {
	Account  string `json:"account"`            // organization | personal
	Org      string `json:"org,omitempty"`      // organization: its login
	Name     string `json:"name,omitempty"`     // "" = "Bothy (<org or your username>)"
	Replaces string `json:"replaces,omitempty"` // connection to swap out once the app is installed
}

// GitHubAppStartResp is where the browser goes next: a same-origin page
// that posts the manifest to GitHub.
type GitHubAppStartResp struct {
	HandoffURL string `json:"handoff_url"`
}

// GitHubAppCheck is POST /api/github/apps/{id}/check: whether the app is
// installed, and the connection its installation made.
type GitHubAppCheck struct {
	Installed  bool                   `json:"installed"`
	Connection *core.GitHubConnection `json:"connection,omitempty"`
}

// knowledge.go
// ---- knowledge (v0.2 F6) ----

// KnowledgeIndexReq queues the index of a workspace directory
// (POST /api/knowledge/repos; 202 with the core.IndexedRepo).
type KnowledgeIndexReq struct {
	Principal core.PrincipalID `json:"principal_id,omitempty"` // default: the caller's own principal
	Dir       string           `json:"dir"`                    // workspace-relative; "" = the whole workspace
	Repo      string           `json:"repo,omitempty"`         // "owner/name"; default "local/<directory name>"
	SHA       string           `json:"sha,omitempty"`          // optional commit id for citations
}

type KnowledgeSearchResp struct {
	Items []core.KnowledgeHit `json:"items"`
}

// NotesChange is one change in a repo's notes history
// (GET /api/knowledge/repos/{id}/notes/history[/{version}]), with names
// for the people and chats it mentions.
type NotesChange struct {
	core.NotesVersion
	Name string    `json:"name,omitempty"` // the person's name, for actor user
	Chat *ChatLink `json:"chat,omitempty"` // the chat Bothy made the change in, when the caller can open it
}

// ChatLink names a chat for a link to it.
type ChatLink struct {
	ID    core.SessionID `json:"id"`
	Title string         `json:"title"`
}

// KnowledgeNotesPutReq replaces a repo's notes; If-Match carries the etag
// from GET (`""` while there are no notes yet). Empty content removes them.
type KnowledgeNotesPutReq struct {
	Content string `json:"content"`
}

// releases.go
// ---- multi-approver approvals and releases (v0.2 F5) ----

// ApprovalVotes is GET /api/approvals/{id}/votes: the vote state of a
// multi-approver approval as the caller sees it ("1 of 2 approvals").
type ApprovalVotes struct {
	Approval      core.ApprovalID     `json:"approval_id"`
	Status        core.ApprovalStatus `json:"status"`
	Required      int                 `json:"required"`  // distinct approvals needed
	Approvals     int                 `json:"approvals"` // allow votes so far
	Votes         []ApprovalVoteView  `json:"votes"`     // oldest first
	Approvers     string              `json:"approvers"` // who can still approve, the caller as "you", e.g. "you, Bob or any admin"
	Requester     core.UserID         `json:"requester"`
	RequesterName string              `json:"requester_name"`
	// CanVote: the caller may still allow or deny (eligible approver, not the
	// requester, not voted yet, approval open). CanWithdraw: the caller asked
	// and may still deny.
	CanVote     bool        `json:"can_vote"`
	CanWithdraw bool        `json:"can_withdraw"`
	YourVote    core.Choice `json:"your_vote,omitempty"`
}

type ApprovalVoteView struct {
	User    core.UserID `json:"user_id"`
	Name    string      `json:"name"`
	Choice  core.Choice `json:"choice"` // allow_once | deny
	Via     string      `json:"via"`    // web | telegram | discord | slack
	At      time.Time   `json:"at"`
	Comment string      `json:"comment,omitempty"`
}

// Releases is GET /api/releases: the configured environments and the newest
// deploys the caller may see (their own; everyone's for admins).
type Releases struct {
	Environments []ReleaseEnvironment `json:"environments"`
	Deploys      []DeployView         `json:"deploys"`
}

// DeployView is a deploy record with names instead of user ids. Subject
// (core.Deploy) is the commit's subject line as its author wrote it: show
// it as quoted text, never as a heading, a question or a link.
type DeployView struct {
	core.Deploy
	RequestedByName string   `json:"requested_by_name"`
	ApprovedByNames []string `json:"approved_by_names"`
	ShortSHA        string   `json:"short_sha"` // the first 7 characters of SHA
}

type ReleaseEnvironment struct {
	Name       string            `json:"name"`
	Repo       string            `json:"repo"`
	Workflow   string            `json:"workflow"`
	RefPattern string            `json:"ref_pattern"`
	Inputs     map[string]string `json:"inputs"`
	Required   int               `json:"required"` // approvers besides the requester
	Approvers  []ReleaseApprover `json:"approvers"`
}

type ReleaseApprover struct {
	Kind string `json:"kind"` // user | role
	ID   string `json:"id"`   // user id or role name
	Name string `json:"name"` // display name ("" for a user that no longer exists), or the role
}

// inbox.go
// ---- approvals inbox (v0.2 UI, docs/design/ui.md §8.8) ----

// InboxType is the kind of an approvals-inbox item.
type InboxType string

const (
	InboxApproval       InboxType = "approval"        // an action card (Approval)
	InboxNoteSuggestion InboxType = "note_suggestion" // a project notes edit waiting for approval (Approval, card kind review)
	InboxMCPReview      InboxType = "mcp_review"      // admins: tools of an MCP server waiting for review (MCP)
	InboxSkillReview    InboxType = "skill_review"    // admins: a new or changed skill (Skill)
	InboxUnknownSender  InboxType = "unknown_sender"  // admins: someone new wrote to Bothy on a chat app (Sender)
	InboxGroupChat      InboxType = "group_chat"      // Bothy was added to a group chat (Group)
)

// Inbox is GET /api/inbox: what is waiting for a decision, the caller's
// items first ("For you"), then those waiting for others, oldest first.
// Each item is decided through the endpoint its type already has:
// POST /api/approvals/{id}; POST /api/mcp/{server}/approve; PATCH
// /api/skills/{name}; PATCH /api/identities/{channel}/{external}
// (status teammate lets someone in, blocked blocks them); PATCH
// /api/spaces/{id} (enabled true turns a group chat on; any change records
// it as reviewed).
type Inbox struct {
	Items []InboxItem `json:"items"`
}

type InboxItem struct {
	// ID is the approval id, or "mcp:<server>", "skill:<name>",
	// "sender:<channel>:<external_id>", "group:<space principal id>".
	ID   string    `json:"id"`
	Type InboxType `json:"type"`
	// Question is the item's title in plain copy, e.g. "Review changes to
	// Linear actions"; Summary the facts beside it ("2 actions changed",
	// "Jordan K (@jordan_k)", "Added by Sam Patel").
	Question  string    `json:"question"`
	Summary   string    `json:"summary,omitempty"`
	ForYou    bool      `json:"for_you"` // the caller can decide it now
	CreatedAt time.Time `json:"created_at"`

	Approval *core.Approval        `json:"approval,omitempty"`
	MCP      *InboxMCP             `json:"mcp,omitempty"`
	Skill    *core.SkillMeta       `json:"skill,omitempty"`
	Changes  *SkillChanges         `json:"skill_changes,omitempty"` // with Skill: what changed since it was approved
	Sender   *core.ChannelIdentity `json:"sender,omitempty"`        // first_text holds the start of their message
	Group    *InboxGroup           `json:"group,omitempty"`
}

// SkillChanges is how a skill's files differ from the ones an admin
// approved (every file is added for a skill never approved).
type SkillChanges struct {
	Files []SkillFileChange `json:"files"`
	// NoBaseline: the skill was approved before Bothy kept approved files,
	// so only that it changed is known.
	NoBaseline bool `json:"no_baseline,omitempty"`
	// Truncated: more changed files or text than one review shows; the
	// skill's page has every file.
	Truncated bool `json:"truncated,omitempty"`
}

type SkillFileChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // added | removed | changed
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Opaque: not text, or too large to show, so neither side is included.
	Opaque bool `json:"opaque,omitempty"`
}

type InboxMCP struct {
	Server string         `json:"server"`
	Tools  []*core.MCPPin `json:"tools"` // each changed tool's pending definition and, when it had one, its approved one
}

type InboxGroup struct {
	Space   core.Space `json:"space"`
	AddedBy string     `json:"added_by"` // the name of whoever added Bothy, when the chat app said; else ""
}

// setup.go
// ---- people and models setup (v0.2 UI) ----

// InstallKind is how this server runs (SetupState.Install): compose, docker,
// binary, or unknown where none of their commands applies (Kubernetes, ECS).
type InstallKind string

// UserCreateResp is POST /api/users: the new account and, when the request
// left the password empty, the one-time password to hand to its owner
// (shown once; they choose their own at the first sign-in).
type UserCreateResp struct {
	core.User
	OneTimePassword string `json:"one_time_password,omitempty"`
}

// PasswordResetResp is POST /api/users/{id}/password: the account's new
// one-time password, shown once.
type PasswordResetResp struct {
	OneTimePassword string `json:"one_time_password"`
}

// ProviderConnectReq is POST /api/providers/connect: one call that saves the
// key, writes providers[id] and sets the model slots in one config change.
// Give at most one of APIKey (pasted; saved in the global vault as
// "<id>_api_key"), SecretRef (a saved key, "secret:<name>", from a detection)
// or EnvVar (a detected well-known key variable, copied into the vault).
// Bedrock takes none of them.
type ProviderConnectReq struct {
	ID           string            `json:"id,omitempty"` // providers[].id; "" = from kind and address (anthropic, openrouter, ollama, lmstudio, custom, …)
	Kind         core.ProviderKind `json:"kind"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKey       string            `json:"api_key,omitempty"` // write-only
	SecretRef    string            `json:"secret_ref,omitempty"`
	EnvVar       string            `json:"env_var,omitempty"`
	AllowPrivate bool              `json:"allow_private"`         // a detected local server, or an address the person allowed
	API          string            `json:"api,omitempty"`         // openai, openai_compat: auto | responses | chat
	Region       string            `json:"region,omitempty"`      // bedrock
	Profile      string            `json:"profile,omitempty"`     // bedrock
	Model        string            `json:"model,omitempty"`       // main model id; "" leaves both slots as they are
	QuickModel   string            `json:"quick_model,omitempty"` // quick tasks model id; "" = Model
}

// ProviderConnectResp says what POST /api/providers/connect wrote.
type ProviderConnectResp struct {
	Provider        string           `json:"provider"`       // the providers[].id written
	Main            string           `json:"main,omitempty"` // "<provider>/<model>", now first in models.main
	Fast            string           `json:"fast,omitempty"` // now first in models.fast
	Secret          *core.SecretMeta `json:"secret,omitempty"`
	RestartRequired []string         `json:"restart_required"`
}

// signin.go
// ---- sign in with Google and GitHub (§15) ----

type SignInMethods struct {
	Providers []core.SignInOption `json:"providers"`
}

type SignInStartReq struct {
	Next string `json:"next,omitempty"` // where to land after signing in; validated server-side
}

type SignInStartResp struct {
	RedirectURL string `json:"redirect_url"` // the provider's authorize address
}

type MySignIns struct {
	Providers   []core.SignInOption `json:"providers"`    // turned on now; the person may connect these
	Linked      []*core.SignIn      `json:"linked"`       // their links, including providers since turned off
	HasPassword bool                `json:"has_password"` // false for accounts made by provider sign-in
}

type SignInAdmin struct {
	PublicURL string                      `json:"public_url"`
	Address   string                      `json:"address"`   // ok | missing | not_https
	TeamMode  bool                        `json:"team_mode"` // allow_new takes effect only in team mode
	Allow     []string                    `json:"allow"`     // auth.sign_in.allow, shared by both providers
	AllowNew  bool                        `json:"allow_new"`
	Providers []core.SignInProviderStatus `json:"providers"`
}

type SignInListPatch struct {
	Allow    *[]string `json:"allow,omitempty"`
	AllowNew *bool     `json:"allow_new,omitempty"`
}

type SignInPatch struct {
	Enabled      *bool     `json:"enabled,omitempty"`
	ClientID     *string   `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"` // write-only; "" keeps the saved one
	Orgs         *[]string `json:"orgs,omitempty"`          // GitHub only; pinned by id as they are saved
}

type SignInCheckReq struct {
	ClientID     string `json:"client_id,omitempty"`     // "" = the saved one
	ClientSecret string `json:"client_secret,omitempty"` // "" = the saved one
}

type SignInCheck struct {
	OK    bool             `json:"ok"`
	Code  core.FailureCode `json:"code,omitempty"`
	Error string           `json:"error,omitempty"` // raw message, for Details
}

// activity.go
// ---- names, activity and search (v0.2 UI, docs/design/ui.md §17) ----

// SessionCreated answers POST /api/sessions: the session, plus the submitted
// run when the request carried the first message.
type SessionCreated struct {
	core.Session
	Run *core.SubmitResult `json:"run,omitempty"`
}

// RunView is a run with the names shown instead of its ids.
type RunView struct {
	core.Run
	RequestedByName string `json:"requested_by_name"` // who asked; "" for schedules and check-ins
	ChatTitle       string `json:"chat_title"`        // as the chat list shows it
	ScheduleName    string `json:"schedule_name"`     // "" unless a schedule started it; the heartbeat is "Check-in"
	// Origin is where the task came from: web, slack, telegram, discord
	// (the chat it ran in), api or schedule.
	Origin string `json:"origin"`
	// Reason completes the outcome of a task that did not finish, after
	// "Failed: " ("the AI model didn't answer") or "Stopped " ("by Maya
	// Chen"); "" when there is nothing to add.
	Reason string `json:"reason,omitempty"`
}

// UsageRowView is a usage bucket with its name: the person (group_by user),
// schedule (schedule) or chat (session). Day and model buckets, and keys
// that no longer resolve, have none. A person bucket of a teammate or a
// channel says so in Kind and names its chat app.
type UsageRowView struct {
	core.UsageRow
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // user | teammate | channel
	App  string `json:"app,omitempty"`
}

// AuditRow is an audit entry as the log lists it.
type AuditRow struct {
	core.AuditEntry
	ActorName string `json:"actor_name"` // "Maya Chen", "Bothy" (agent and system)
	Summary   string `json:"summary"`    // one plain line: "Approved deleting notes/staging.md"
}

// EgressOutcome is a website access row's decision and connection result in
// one word.
type EgressOutcome string

const (
	EgressReached            EgressOutcome = "reached"
	EgressAllowedUnreachable EgressOutcome = "allowed_unreachable" // allowed, but the site did not answer
	EgressBlocked            EgressOutcome = "blocked"
)

// EgressRow is a website access row with its merged outcome.
type EgressRow struct {
	core.EgressRecord
	Outcome EgressOutcome `json:"outcome"`
}

// RefineEstimate is GET /api/sessions/{id}/refine-estimate: what refining the
// next reply of the chat would cost, for the composer's cost line.
type RefineEstimate struct {
	// USD is the estimated cost of one refined reply, from the chat's recent
	// answer steps; absent when the chat has no priced answer yet.
	USD        *float64 `json:"usd,omitempty"`
	Multiplier float64  `json:"multiplier"` // a refined reply costs about this many normal ones
	Drafts     int      `json:"drafts"`
}

// TextRange marks a match in a snippet: [Start, End) in UTF-16 code units,
// the indices of a JavaScript string.
type TextRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SearchResp is GET /api/search: plain-text snippets with their matches,
// grouped by where they were found.
type SearchResp struct {
	Chats []ChatSearchHit `json:"chats"`
	Notes []NoteSearchHit `json:"notes"`
	Code  []CodeSearchHit `json:"code"`
}

// ChatSearchHit is the best-matching message of one chat.
type ChatSearchHit struct {
	Session core.SessionID `json:"session_id"`
	Title   string         `json:"title"` // as the chat list shows it
	Seq     int64          `json:"seq"`
	At      time.Time      `json:"at"`
	Snippet string         `json:"snippet"`
	Ranges  []TextRange    `json:"ranges"`
	Trusted bool           `json:"trusted"` // written by the person or Bothy, not by others or tools
}

// NoteSearchHit is a matching section of a repository's project notes.
type NoteSearchHit struct {
	RepoID  int64       `json:"repo_id"`
	Repo    string      `json:"repo"`  // owner/name
	Title   string      `json:"title"` // the section's heading; "" before the first heading
	Snippet string      `json:"snippet"`
	Ranges  []TextRange `json:"ranges"`
}

// CodeSearchHit is a matching chunk of an indexed repository; Snippet is its
// first matching line.
type CodeSearchHit struct {
	RepoID    int64       `json:"repo_id"`
	Repo      string      `json:"repo"`
	Path      string      `json:"path"`
	StartLine int         `json:"start_line"`
	EndLine   int         `json:"end_line"`
	Citation  string      `json:"citation"` // owner/repo@sha:path#La-Lb
	Snippet   string      `json:"snippet"`
	Ranges    []TextRange `json:"ranges"`
}
```

`picture.go` (Bothy's picture in the chat apps, §8.3 `/api/agent/picture`); `httpapi.Deps` gains `Picture core.AgentPicture`, and the routes answer 503 `unavailable` without it. `detail` is phrased by the server (`pictureDetail`): "Updated in Telegram." / "In Slack, upload it under App icon in Slack's app settings." / "Discord only allows a few picture changes an hour. Try again later." / "{App} is limiting changes right now. Try again later." / "{App} isn't connected right now, so its picture wasn't changed. Try again once it's back." / "Couldn't change the picture in {App}. Try again later."

```go
// PictureInfo is GET /api/agent/picture/info: whether an admin replaced the
// default picture, and a version that changes with the picture (add it to
// the image address so the browser fetches the new one).
type PictureInfo struct {
	Custom  bool   `json:"custom"`
	Version string `json:"version"`
}

// PictureChange answers PUT and DELETE /api/agent/picture: the picture now
// in use and what happened in each chat app that is turned on.
type PictureChange struct {
	Picture PictureInfo        `json:"picture"`
	Apps    []PictureAppResult `json:"apps"`
}

// PictureAppResult is one chat app's outcome. Detail is one plain sentence
// the UI can show as it is: why it failed and what to do, or where a person
// changes the picture (Slack).
type PictureAppResult struct {
	App    string              `json:"app"`    // telegram | discord | slack
	Result core.PictureOutcome `json:"result"` // updated | failed | manual
	Detail string              `json:"detail"`
}
```

### 8.5 OpenAI-compatible endpoint

**`GET /v1/models`** → `{"object":"list","data":[{"id":"bothy","object":"model","created":<boot unix>,"owned_by":"bothy"}, {"id":"bothy-fast",…}, …]}`. Passthrough ids (`anthropic/claude-sonnet-4-6`, `ollama/qwen3:14b`) are listed only for tokens with scope `llm` and when `api.passthrough` is on.

**`POST /v1/chat/completions`, model `bothy` (main slot) or `bothy-fast` (fast slot), scope `chat`:**
- **Session mapping:** header `X-Bothy-Session: ses_…` (must be visible to the token user's Viewer, else 404) → server-side history; only the **last** user message is used; response header `X-Bothy-History: server`. Without the header → a hidden `api` session (origin `api`, retained 7 days) built from the request: client `system` messages become `RunRequest.Instructions`, rendered as untrusted "client instructions" inside the turn-context (they never replace the constitution); prior `user`/`assistant` turns become `Ephemeral` history with the token user's provenance; client `tool` messages → 400. The model id selects `RunRequest.Slot` (`bothy` = main, `bothy-fast` = fast), with or without the session header. Trigger `api`, so F3 denies admin-class actions and approvals follow the token's `on_approval`.
- **Rejected with 400** (`{"error":{"message","type":"invalid_request_error","param","code":"unsupported_in_agent_mode"}}`): `tools`, `tool_choice`, `functions`, `function_call`, `n>1`, `logprobs`, `audio`, `prediction`, `response_format`. Use passthrough for client tools.
- **Honoured:** `stream`, `stream_options.include_usage`, `max_tokens` / `max_completion_tokens` (caps each step's output), `temperature`, `stop`, `user` (audit only), `metadata` (ignored). Other parameters are ignored and listed in `X-Bothy-Ignored`.
- **Images:** `image_url` with `data:` URIs only (≤ 5 MB); remote URLs → 400 (fetching them would be agent-controlled egress).
- **Streaming:** `chat.completion.chunk` frames, then `data: [DONE]`. `delta.content` carries the assistant text of all steps joined by a blank line. Tool activity is not in `content`; with `X-Bothy-Events: 1` chunks carry an extra `bothy` object (`{"event":"run.tool_call","tool":"web_search"}`) that standard clients ignore. While waiting on an approval (token `on_approval: wait`), the stream sends `: waiting-approval` comments for up to `api_wait`, then the action is denied; API runs never park.
- **Final:** `finish_reason` `stop` | `length` (steps/token budget) | `content_filter` (refusal). `usage` sums every internal call (`prompt_tokens` includes cached; `prompt_tokens_details.cached_tokens` set). Headers `X-Bothy-Run-Id`, `X-Bothy-Session`, `X-Bothy-Cost-Micros`.
- **Disconnect** cancels the run unless `X-Bothy-Detach: 1`.
- **Refine (v0.2):** request header `X-Bothy-Refine: on|off` sets `RunRequest.Refine` (anything else → 400 `invalid_request` with `param` `X-Bothy-Refine`). While refinement runs a streaming response sends `: refining` comments so idle timeouts do not drop it; only the winning answer is streamed.
- **Large bodies** (agent mode and passthrough): a body over 1 MiB (by `Content-Length`, or once a chunked body passes 1 MiB) first takes the single global large-body slot, waiting up to 10 s, else 429 `rate_limited` with `Retry-After: 10`; one such body already outweighs `GOMEMLIMIT`, so they queue instead of adding up. The body is decoded once (top-level keys are scanned without copying values) and raw messages are dropped after parsing.
- **Busy session:** the runner never queues a `/v1` (ChatAPI) request behind other work and returns `ErrBusy` instead (§5.4), which maps to 429 `busy` with `Retry-After: 1` (nothing was consumed, so a retry is safe). If another run started first and took the message as a steer, the reply is 409 `steered`; an unexpected queued result is 409 `queued`. Neither 409 is retryable: the message is already in the session.
- **Errors:** budget → 429 `{"error":{"type":"insufficient_quota","code":"budget_exceeded"}}`; rate limit → 429 + `Retry-After`; paused → 503; provider failure after fallback → 502 `upstream_error`.

**Passthrough, model `<provider>/<model>`, scope `llm`:** the OpenAI request is translated to `core.Request` (so Anthropic, Gemini and Ollama models work from any OpenAI client) and the response translated back. Client `tools`/`tool_choice` pass through and come back as standard `tool_calls`. No Bothy tools, memory, constitution or fallback (`LLM.Passthrough`). Metered (`purpose=passthrough`) against the token user's daily cap; audit `api.passthrough` with token counts only.

### 8.6 Channel setup helpers (M11)

**Slack app manifest** (`GET /api/channels/slack/manifest`; `{{name}}` = agent name from SOUL.md, default "Bothy"):

```yaml
display_information:
  name: "{{name}}"
  description: Self-hosted assistant (Bothy)
features:
  app_home: {home_tab_enabled: false, messages_tab_enabled: true, messages_tab_read_only_enabled: false}
  bot_user: {display_name: "{{name}}", always_online: true}
  slash_commands:
    - {command: /bothy, description: "Talk to {{name}} (stop, new, status, help)", should_escape: false}
oauth_config:
  scopes:
    bot: [app_mentions:read, channels:history, channels:read, groups:history, groups:read, im:history, im:read,
          im:write, chat:write, commands, users:read, files:read, reactions:write]
settings:
  event_subscriptions:
    bot_events: [app_mention, message.im, message.channels, message.groups]   # channel messages only matter for spaces with mention_only off; duplicates of app_mention are dropped by inbound dedup (same ts)
  interactivity: {is_enabled: true}
  org_deploy_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
```

The wizard opens `https://api.slack.com/apps?new_app=1&manifest_yaml=<urlencoded manifest>`; the user then creates an app-level token with `connections:write` (`xapp-…`) and installs the app (`xoxb-…`). Agent output always sets `unfurl_links: false, unfurl_media: false`.

`app_home` opens the app's Messages tab for writing, which is where people message Bothy directly; Slack makes that tab read-only for an app created without it (*"Sending messages to this app has been turned off."*). For an app created from an earlier manifest, turn on **Allow users to send Slash commands and messages from the messages tab** under App Home → Show Tabs.

`channels:read` and `groups:read` let the adapter name channels (`conversations.info`, cached like `users.info`; `Inbound.ChatTitle` = `"#name"`; a slash command's `channel_name` when it is not a placeholder such as `directmessage` or `privategroup`). An app created from an earlier manifest lacks them: add the two scopes under OAuth & Permissions (or update the app from the current manifest) and click **Reinstall to Workspace**; the bot token stays the same. Until then the lookups fail with `missing_scope`, the adapter logs one warning an hour, and channels are named by their id ("Slack group C05S04V0YRG"); once names arrive, spaces still under that fallback name take them (§5.1 step 7).

**Discord invite URL** (`GET /api/channels/discord/invite`): `https://discord.com/oauth2/authorize?client_id=<application_id>&scope=bot%20applications.commands&permissions=274877975552` (View Channels 1024 + Send Messages 2048 + Read Message History 65536 + Send Messages in Threads 274877906944). The Message Content privileged intent must be enabled in the developer portal; the wizard says so and the adapter reports `auth_failed` with that hint when the gateway closes with 4014.

---

## 9. Web UI screens and flows (M12)

Every screen, state, string and component of the web UI is specified in `docs/design/ui.md`, the accepted v0.2 redesign: navigation (§4), setup and sign in (§5), the app shell (§6), chat (§7), approvals and releases (§8), Knowledge (§9), Activity (§10), Settings and Connections (§11, §12), empty, loading and error states and phones (§13), the copy guide (§14), components (§15) and accessibility (§16). Where it and this section disagree, `docs/design/ui.md` wins. This section keeps what the rest of the spec relies on: the stack and its gates, the routes, the server contracts the screens use, and the markdown renderer, which is a security control.

### 9.1 Stack and gates

Svelte 5 (runes), Vite 8, Tailwind 4 (`@tailwindcss/vite`), plain SPA (no SvelteKit). Runtime dependency: `svelte` only. Hand-written pieces: history router, API client over `types.gen.ts` (adds `X-CSRF-Token`, maps `dto.Error`), one `EventSource` store with resync, markdown renderer, SVG bar chart, QR encoder (byte mode, versions 1–6, ECC M; lazy chunk), 18 inline SVG icons. The entry holds chat, the shell and the primitives they use, as one file; setup and sign in, the approvals inbox, Knowledge, Activity and Settings are lazy chunks behind dynamic imports (`web/src/lazy`), loaded while the browser is idle once someone is signed in and as soon as a link to them is hovered, touched or focused. Vite's module preload inserts `<link rel="modulepreload">` from script, never inline code. Svelte compiles with `css: 'external'`; markup has no `style="…"` attributes (classes or `style:` directives, which go through CSSOM), so the CSP needs no `unsafe-inline`. The design tokens in `web/src/app.css` reset Tailwind's colours, type sizes, radii and shadows, so only the tokens of `docs/design/ui.md` §3 compile. Light and dark themes; works at 360 px; PWA manifest and icons (no offline mode).

Gates: `npm run check` (svelte-check, then `scripts/ui-lint.sh` for the class and markup rules of ui.md §18.2 G5), `npm test` (vitest, including `src/copy.test.ts` for the copy guide), `npm run smoke` (production build under the gateway's CSP with the fixture gateway in `web/mock`: no CSP violations, page errors or other origins; axe on every route in both themes; no sideways scroll at 360 px; one left edge at 1280 px), and `scripts/budgets/ui-size.sh`: the first screen (the scripts `index.html` loads, their static imports and its stylesheets) ≤ 120 KB gz of JS and ≤ 20 KB gz of CSS, and all JS chunks together ≤ 280 KB gz (260 KB before v0.2 sign-in and GitHub App creation, which are lazy settings code; the first-screen budget did not move), measured on the embedded `.js.gz`/`.css.gz` files (the build keeps only the gzip variant of compressible assets, and `vite preview` serves them), no inline scripts or styles in the built `index.html`, zero requests to other origins.

### 9.2 Routes

`/setup`, `/login`, `/password`, `/chat`, `/chat/:id`, `/approvals`, `/approvals/:id`, `/knowledge`, `/knowledge/search`, `/knowledge/memory`, `/knowledge/skills`, `/knowledge/skills/:name`, `/knowledge/projects/:owner/:repo`, `/activity`, `/activity/tasks/:id`, `/activity/schedules`, `/activity/schedules/:id` (`new`, `checkin`), `/activity/releases`, `/activity/spending`, `/activity/audit`, `/settings`, `/settings/:group`, `/settings/:group/:item` (ui.md §4.5). v0.1 addresses (`/runs/:id`, `/schedules…`, `/memory`, `/usage`, `/audit`, the old `/settings/:page` names) redirect with `history.replaceState`, so links already sent to chat apps keep working; `Card.WebURL` still points at `/approvals/:id`.

### 9.3 Approvals and the inbox

Cards render only from the server's `core.Card` (plain copy, S2) and the structured `Action`; fields that leave Bothy or can't be undone stay on the card in full, verbatim, with bidi and zero-width characters made visible and domains in punycode (ui.md §8). Buttons are real `<button>` elements built from `card.choices`; a multi-approver card shows the tally and names from `Approval.approvals` and `GET /api/approvals/{id}/votes`, and the requester gets no decision buttons. Keyboard flow (ui.md §8.8, §16): J/K move, Enter opens, A/S/D decide in the inbox or a focused card; a held key decides one item (repeats are ignored and the next decision waits for the key to be released); focus moves to the next waiting item and a polite live region announces the result. Resolving anywhere updates every surface live (`approval.resolved`, `approval.voted`).

**The inbox (S12).** `GET /api/inbox` returns `dto.Inbox{items}`, the caller's items first (`for_you`: they can still decide), then oldest first. Item types and how each is decided, all through existing endpoints: `approval` and `note_suggestion` (an approval whose card kind is `review`; `summary` "n of k approvals" on multi-approver ones) → `POST /api/approvals/{id}`; for admins only, `mcp_review` (`mcp:<server>`: servers with pending pins of tools they offer now, each changed tool's approved pin kept beside the pending one; question *"Review {Server}'s new actions"* / *"n new actions"* when none had an approved definition, *"Review changes to {Server} actions"* / *"n actions changed"* when all did, else *"Review {Server}'s actions"* / *"n new actions, m changed"*) → `POST /api/mcp/{server}/approve`; `skill_review` (`skill:<name>`: skills `changed` or `pending`, `SkillMeta` plus `skill_changes`, the files that differ from the approved snapshot (§14.7), each file through `GET /api/skills/{name}/files/{path...}`) → `PATCH /api/skills/{name}`; `unknown_sender` (`sender:<channel>:<external_id>`: people waiting for an admin who wrote in a DM within 30 days: pending, or a membership teammate Discord can no longer vouch for, with verdict `unknown`; at most 50, newest first, with `first_text`; no notice needs to have been sent) → `PATCH /api/identities/{channel}/{external}` (Let in, Block); and `group_chat` (`group:<space principal>`: groups Bothy was added to that no admin has turned on or off, `reviewed_at` zero; *"Added by {name}"* when `added_by` names a known identity) → `PATCH /api/spaces/{id}`. Team approvals (`approver.team`) reach admins through `Viewer.Admin` and are for them (`for_you`); their row summary is "{Name} · {App}". Clients refetch the inbox on `approval.requested`, `approval.voted`, `approval.resolved`, `mcp.pin_pending`, `inbox.changed` and the banner codes `skills_changed`, `mcp_pins` and `unknown_sender` (still published; the inbox replaces them in the v0.2 UI).

### 9.4 Setup, sign in and people

The setup code is read from `#t=…`, posted to `/api/setup/claim` and removed with `history.replaceState`; without one, the gate shows the command for the install `GET /api/setup/state` reports (ui.md §5.3). The 10k common-password list lives server-side in `internal/auth`, so the bundle stays small; its rejection is shown next to the field. An account an admin adds gets a one-time password from the server (`POST /api/users` without a password); `Me.user.must_change_password` keeps that person on `/password` until they choose their own (ui.md §5.9, §11.5). When an admin turned on Google or GitHub sign-in (§15), the sign-in page shows "Continue with Google" / "Continue with GitHub" above the form; Account → Sign-in methods connects and disconnects them, and Security → Sign-in holds the shared list of who can sign in and a page per provider (ui.md §5.9, §11.2, §11.6). Account is name and time zone only: chat apps are not linked to accounts. People → In chat apps (admins, `GET /api/teammates`) lists teammates and blocked people with Can approve, today's spending against their limit (`budgets.per_user_daily_usd["<app>:<id>"]`), the chat share, Block, Unblock and Delete their data (ui.md §11.5).

### 9.5 Markdown renderer (security control)

Renders headings, paragraphs, emphasis, inline/fenced code, lists, tables, blockquotes, links. Escapes all HTML; never renders images (image syntax becomes a text link "image: host/…"); links get `rel="noopener noreferrer nofollow"` and `target="_blank"`; `javascript:`/`data:`/`vbscript:` links become text; in Exposed sessions, links to non-allowlisted domains render inert (host shown, copy button; v0.1 renders every link inert there, since members cannot read `egress.allow`). Only explicit links and scheme URLs are ever linked: bare domains, and hosts hidden by markdown escapes, emphasis or invisible characters (`evil\.com`, `evil**.**com`), stay plain text. Shared by chat, cards and the SKILL.md viewer. Headings map down so a reply never shows one above 15 px (ui.md §3.2).

### 9.6 Settings and configuration

Config keys, provenance (§7.3: UI, file, env, default), env-var names and the effective YAML appear only in Settings → Advanced (ui.md §11.7), each field with "Changed · Reset to default" or "Set by the server environment". Every other settings page writes through `PATCH /api/config` with plain labels and saves per row (ui.md §11.1). "Allow this site" (Activity → Audit log → Website access, and Settings → Security → Websites Bothy can reach) creates a persistent principal-scope exact `host:port` domain grant with `max_se` `external_send` (the box proxy ignores read-class domain grants, §2.8; revocable under Security → Allowed without asking). The rules tester in Advanced plans a sample call with `POST /api/policy/preview` and shows the Action and Decision.

---

## 10. Security controls (P0 baseline → mechanism → module)

| # | Baseline item (brief §6) | Mechanism | Module |
|---|---|---|---|
| 1 | No unauthenticated mode; loopback publish; refuse insecure exposure; setup token; Argon2id; lockout; constant-time | Every route except health, setup (local peers, 404 after completion) and static assets requires an Actor; loopback is not trusted. Compose publishes `127.0.0.1:8080`. Exposure gate by **peer IP** (§8.1): plain HTTP only for local peers (LAN only with locked `insecure_lan`), public peers need TLS; a forwarded address never makes a request local. Host allowlist → 421 (`GET`/`HEAD` `/healthz` and `/readyz` exempt). Setup token 256-bit, single claim, 24 h, file 0600, delivered in the URL fragment. Argon2id m=19 MiB t=2 p=1, one hashing slot, dummy hash for unknown users, per-user and per-IP exponential lockout. `subtle.ConstantTimeCompare` for all secrets. | httpapi, auth, deploy |
| 2 | Web hardening: Origin/CSRF/SameSite, no tokens in query strings, CSP, no wildcard CORS, loopback not trusted, mDNS off | SSE instead of WebSocket. `http.CrossOriginProtection` + synchroniser `X-CSRF-Token` + SameSite=Strict; `__Host-` cookie under TLS. Credential-named query params → 400; the SPA never reads tokens or gateway URLs from `location` except the one-time setup fragment, which it strips. Strict CSP without `unsafe-inline`, `frame-ancestors 'none'`. No CORS headers at all. `trusted_proxies` explicit. No mDNS code. | httpapi, auth, web |
| 3 | Envelope-encrypted vault; KEK sources; keys only in the gateway; redaction; write-only UI; 0600 files | One active 32-byte DEK wrapped (AES-256-GCM) by the KEK; secrets AES-256-GCM with random 96-bit nonces and AAD `scope‖0‖name‖0‖version`; `kek_check` detects a wrong KEK (fail closed, vault sealed). KEK sources: Docker secret `/run/secrets/bothy_kek` → systemd `$CREDENTIALS_DIRECTORY/bothy_kek` → `file:` → passphrase (Argon2id-derived, vault sealed until unlocked in the UI) → auto keyfile in the separate `/keys` volume (colocated fallback flagged medium) → `env` (`BOTHY_KEK`, flagged high). Secrets reach only provider, channel, search and HTTP-MCP clients in the gateway; the sandbox protocol has no field that can carry them; MCP stdio gets none. v0.2: `aws-sm:` references are fetched by the gateway from AWS Secrets Manager with the bedrock credential chain, through egress to the regional endpoint only, cached in memory, never stored, redacted and audited per use (§14.8). Redactor: exact values plus base64/base64url/hex/percent-encoded variants and token patterns, applied to logs (slog handler), audit detail, tool results, stored messages (before the write, §5.5 step 2), model input, channel output and SSE. API never returns values. Umask 007; `/data` files 0600, dirs 0700. | vault, audit, sandbox, egress |
| 4 | Sandbox: non-root, cap_drop ALL, no-new-privileges, read-only rootfs + tmpfs, default seccomp/AppArmor, limits; workspace only; no docker.sock in the gateway; network none / proxy only; approvals stay on | Compiled-in spec (§2.6): uid 10001, `CapDrop: ALL`, `no-new-privileges`, read-only rootfs, tmpfs `/tmp` and home, default seccomp/AppArmor, pids/memory/CPU/nofile limits, `--network none`, `LogConfig none`. Mounts: own workspace subpath rw, skills ro, own egress socket dir ro, MCP tree ro (Docker 25: binds of the same directories, checked by device and inode before the box is used). The runtime socket lives only in sandboxd (no network, fixed schema, regex-validated ids). runsc auto when probed. Self-test canaries gate every box. Policy has no "in a container" bypass. bwrap backend: `--clearenv`, `--unshare-all --disable-userns`, seccomp filter, rlimits. | sandbox, sandboxd, sbxinit, policy |
| 5 | Egress: default deny, private ranges blocked, proxy DNS with pinning, no sandbox DNS, byte caps, every destination logged | Per-box unix-socket proxy (socket = identity); port allowlist (443); allowlist from config, presets (off), box extras and narrow grants; shared-tenant suffixes admin-only; IP literals only by exact grant; full netip blocklist with `Unmap` and embedded-IPv4 re-check; resolve-then-dial pinned IP with `Control` re-check; SNI must equal the CONNECT host; redirects re-checked per hop (public dialer); asymmetric caps (10 MiB up / 200 MiB down); `egress_log` row per connection; three dialers (box, public, system), the system dialer limited to exact configured `host:port`; v0.2: a credentials-only client for the Bedrock AWS credential chain (IMDSv2 token and role paths, the exact container-credential path, logged as `credentials:aws`), and `169.254.170.2`, `169.254.170.23` and `fd00:ec2::23` blocked for every other dialer even with `allow_private` (§2.8). | egress |
| 6 | Capability manifests for every tool, skill and MCP tool; side-effect class; deny by default; system-rendered cards | `core.Manifest` is required and validated at `Register` (zero side effect rejected, schema must compile). MCP tools are not exposed until an admin approves them with a class (config `side_effects` default `external_send`; annotations may only upgrade; `field_map` exposes recipient/URL fields for first-contact checks). Skills add no tools and never execute anything. Cards are rendered by the policy module from the Action only; channel renderers escape everything. | tools, mcp, skills, policy, channel |
| 7 | Trifecta state machine; messaging confined to origin chat; first contact needs approval | `sessions.taint` P/U/S bits (§5.3); Exposed ⇒ external_send/irreversible always ask, session egress grants suspended, URL provenance for `web_fetch`, link neutralization as rendered (scheme URLs, scheme URLs with a bracketed IPv6 host, every `scheme://` defanged whatever follows it, nested autolinks broken, `www.` hosts, bare domains, IPv4 runs of 4+ parts, U+2024/U+FE52 dot look-alikes, hosts and `scheme:` colons hidden by markdown escapes, emphasis or invisible characters; code left alone; one implementation in `internal/core/links.go`, §4.15, §5.3) in channel output and the agent sink; box egress taints exec output, and a workspace marked untrusted taints what exec and read-only fs tools read from it in every session of the principal. Forwarded chat content is untrusted whoever forwards it. The `message` tool has no recipient parameter (uses `env.Chat`). First contact (new recipient or non-GET domain) asks. Clean fork resets taint. | policy, agent, tools, egress, channel, web |
| 8 | Close exfiltration channels | No remote images anywhere (renderer drops them; CSP `img-src 'self' data:`). Previews off: Telegram `link_preview_options.is_disabled`, Slack `unfurl_links/unfurl_media:false`, Discord `flags: SUPPRESS_EMBEDS`. Links neutralized in Exposed sessions (`hxxps://evil[.]com/…`, `evil[.]com` for bare and markdown-obfuscated domains in chat and `/v1`, code left alone; inert in web, which never links bare domains). Agent output never carries buttons. Shared-tenant hosts need admin grants. Upload caps. | channel/*, web, egress |
| 9 | Self-modification lock | The model has no tools for config, policy, persona, constitution, skills, MCP or auth. `schedule_create/delete` are `admin` class (F3: admin approver, never auto, denied in scheduled/heartbeat/API runs; `Triggers: [message]`). fs tools are rooted at the principal workspace via `os.Root`. Every UI/API change is audited. | tools, policy, scheduler, httpapi |
| 10 | Cost and abuse caps + kill switch | Run steps/tokens/time/$ meters, daily caps (global, per user, heartbeat), sender and token rate limits, loop detector, `/stop`, `/stopall` / `POST /api/admin/pause` (persisted). | budget, agent, channel, httpapi |
| 11 | Append-only audit log | `audit_log` with UPDATE/DELETE abort triggers and a sha256 hash chain (`prev_hash`, `hash` over canonical JSON); single writer; `GET /api/audit/verify` and `bothy audit verify`; no online prune; offline archive inserts an anchor row. | audit, store |
| 12 | Our supply chain | Distroless static nonroot, digest-pinned bases; goreleaser + cosign keyless + syft SBOM + SLSA provenance; `go.sum` committed; **14-day cooldown** for new dependency versions (CI checks publish times via proxy.golang.org and the npm registry); govulncheck; `npm ci --ignore-scripts`; Renovate `minimumReleaseAge: 14 days`; MCP packages exact version + integrity + recorded lock, installed with `--ignore-scripts` into a read-only tree; skill hash pinning; safe `/etc/gitconfig` in sandbox images; `os/exec` import ban. | deploy, CI, mcp, skills, sandbox images |
| 13 | SECURITY.md with SLA and private channel | GitHub private advisories + `security@bothy.dev` (*verify domain*); acknowledge within 72 h, fix critical/high within 14 days; supported versions; coordinated disclosure. | repo root |
| — | Multi-principal isolation | Viewer-scoped repositories (IDOR suites in M1 and M11); team-owned principals (channels, teammates) belong to no viewer, so their chats, runs, memory, uploads and transcripts are not on the web, admins included (`adminViewer` serves only group chats, grants, inbox group items, team schedules and naming); no account's memory or vault for them; per-principal workspaces and boxes; a teammate approves only when an admin marked them Can approve and their chat app still vouches for them, never admin-class requests or release votes, never always-allow for a channel; people in chat apps are never linked to accounts; reads by team principals outside Bothy are audited (`tool.read`); `admin_can_read_chats` locked off. | store, auth, agent, channel, policy, httpapi |
| — | Prompt-injection containment (v0.1 level) | Provenance on every message, spotlight delimiters with random ids, deterministic floors; quarantined LLM is P1. | agent, policy |
| — | v0.2 repositories, knowledge and releases (§14) | GitHub: the PAT or App key stays in the vault and travels only through `SystemClient` to the API host; App installation tokens are cached in memory and registered with the redactor; the archive redirect is fetched without the token and only from configured hosts; extraction goes through `os.Root` with caps on size, files and decompressed bytes, drops links, devices, `.git` and control or bidi names, and swaps folders in atomically; manifests and pristine archives live under `<data>/github/checkouts`, outside every box, and are purged with the user. Knowledge: read-only `os.Root` walk opening only regular files with `O_NONBLOCK`, bounded ignore-rule work, notes kept per principal, untrusted notes spotlighted and tainting. Releases: the release floor (§5.8), votes decided in one transaction, the release token sent only to `releases.github_api` (https, or http for loopback only). Bedrock: AWS credentials never stored or shown; `credential_process` profiles refused. | github, knowledge, release, policy, provider |
| — | Compaction keeps safety | Constitution and standing rules in S1 outside history; pins in S2; validated summaries; auto-pinned standing instructions; taint and grants in the DB. | agent |
| — | Provider sign-in (§15) | Authorization code flow with PKCE S256; a random 32-byte state kept server-side as sha256, single use, 10 minutes, and required to equal an HttpOnly SameSite=Lax cookie; a Google nonce checked in constant time; one exact redirect address per provider built from `public_url` (never `Host`), and the flow must match the callback's provider. The code is traded server-side through `egress.SystemClient` (`oauth2.googleapis.com`, `github.com`, `api.github.com`; no redirects followed, capped bodies, 15 s per call). The Google id token is read only from that client-authenticated TLS answer and every claim that matters is checked. The shared sign-in list is checked at every sign-in and link: verified addresses only, compared as ASCII (non-ASCII or control characters match nothing); a domain only through Google's `hd`, never for GitHub (GitHub confirms an address once and never re-checks it); GitHub organizations pinned by id; public mail, relay and noreply domains refused as domains. Disabled accounts refused; no email matching to existing accounts; new accounts are members without a password and with a display name nobody else uses. Tokens are never stored (GitHub's is revoked at once); audit rows carry provider, reason, account and address, never a code, state or token. The client secret lives in the vault or behind an env or `aws-sm` reference, write-only in the UI. Starts limited to 20/min per IPv4 address or IPv6 /64; at most 32 flows in progress per client, 1024 in all. The refused account reaches the sign-in page in a 60-second cookie, never the address. `api_base` test overrides are locked and loopback only. | auth, httpapi, config, store, web |

**Audit actions** (`audit_log.action`): `auth.setup_claim`, `auth.owner_created`, `auth.login`, `auth.login_failed`, `auth.lockout`, `auth.logout`, `auth.password_changed`, `token.create`, `token.revoke`, `user.create`, `user.update`, `identity.teammate` (a member check let someone in; actor and target are the identity), `identity.let_in`, `identity.unblock`, `identity.block`, `identity.approver` (detail `can_approve`, and `via: membership` when the membership check took it away), `identity.forget` (with the purge counts), `space.update` (detail `enabled` when an admin turned a channel on or off), `space.delete` (a channel and its data: title, chat app and the purge counts), `tool.read` (a teammate's or channel's read outside Bothy: tool and target, against the principal), `migration.team_access` (the 0013 report, once), rows from before 0013 may carry `pairing.create`, `pairing.redeem`, `pairing.burn`, `identity.link`, `identity.guest`, `identity.unlink`; `config.set`, `config.reset`, `persona.update`, `picture.update` and `picture.reset` (version and each chat app's result, never the image), `vault.put`, `vault.delete` (written by the vault itself for every caller; an API change adds the token or user, client address and source through `core.WithAuditOrigin`), `vault.rotate`, `vault.unseal`, `secret.use` (name + purpose), `tool.exec`, `tool.denied`, `approval.request`, `approval.decide` (a multi-approver requester's deny carries `withdrawn`; a chat approver's actor is `channel:<app>:<id>`), `approval.expire`, `grant.create`, `grant.revoke`, `egress.allow` (first contact / held approvals), `egress.deny`, `memory.write`, `memory.edit` (both with `scope: team`, target `memory:team/TEAM`, for TEAM.md; an agent write adds `approval_id`, `approved_by` and `provenance`; never the content), `skill.install`, `skill.enable`, `skill.disable`, `skill.changed`, `mcp.install`, `mcp.approve`, `mcp.changed`, `schedule.create`, `schedule.update`, `schedule.delete`, `schedule.run` (Run now: who pressed it, with the run), `run.stop`, `budget.exceeded`, `admin.pause`, `admin.resume`, `admin.restart`, `backup.create`, `sandbox.selftest_failed`, `api.passthrough`, `audit.archived`; v0.2: `github.connect` (carries `method`: `token`, `app` or `app_manifest`, and for an app Bothy made `app`, `app_name` and `reinstalled` when a new installation moved the connection), `github.repos`, `github.disconnect` (carries `app` when it retired an app Bothy made), `github.checkout`, `github.pull_request`, `github.comment`, `github.app_create` (ok, with `app_id`, `slug`, `name`, `owner`, `owner_type`, `permissions` and any `extra_permissions` or `replaces`; or error, with `owner` and the redacted reason; never a code, key or secret), `github.app_retire` (`slug`, `name`, `owner`), `github.app_replace` (error only: moving to an app made to replace a connection failed and waits for the next check; `slug`, `name`, `replaces` and the redacted reason), `knowledge.index`, `knowledge.delete`, `knowledge.notes_draft`, `knowledge.notes_edit` (user save), `knowledge.notes_write` (agent edit), `approval.vote` (ok or denied), `release.dispatch` (ok, error or denied), `release.finish`; §15: `signin.link` (ok or denied, with `provider`, `email` or `login`, and a denial's `reason`) and `signin.unlink` (`provider`); `auth.login` and `auth.login_failed` carry `method` (`password`, `google`, `github`) and a provider failure's `reason`; `user.create` carries `via` (`google`, `github`) for an account sign-in made. Details never include message content or secret values.

**Doctor / security-audit checks** (stable ids; `security` ones run in `security-audit`; `fix` = auto-fixable):

| Id | Severity | Security | Fix |
|---|---|---|---|
| `sqlite.version` | critical | | |
| `data.permissions` (files 0600, dirs 0700) | high | ✓ | ✓ chmod |
| `vault.kek_colocated` | medium | ✓ | |
| `vault.kek_env` | high | ✓ | |
| `vault.sealed` | high | | |
| `server.exposure` (non-loopback host listen, no TLS, no proxy) | high | ✓ | |
| `server.exposure` (container mode: `0.0.0.0` listen, no https `public_url`) | medium | ✓ | |
| `server.insecure_lan` | medium | ✓ | |
| `sandbox.unavailable` | medium | ✓ | |
| `sandbox.not_isolated` | critical | ✓ | |
| `sandbox.rootful_runc` | low | ✓ | |
| `sandbox.runsc_host_uds` | low | | |
| `sandbox.bind_mounts` (Docker 25 bind-mount mode) | info | | |
| `docker.socket_access` | medium | | |
| `egress.wildcards` / `egress.shared_tenant_allowed` | medium | ✓ | |
| `egress.presets_enabled` | low | ✓ | |
| `policy.hands_off` / `policy.auto_external_rules` | medium | ✓ | |
| `budgets.unlimited` | medium | ✓ | |
| `tokens.admin_scope` / `tokens.expired_unrevoked` | low | ✓ | ✓ revoke expired |
| `auth.sign_in` (§15: Google or GitHub sign-in is on, but its client secret is missing, or the saved keys are locked (medium; titles "… client secret is missing" / "… saved keys are locked"); GitHub sign-in organizations without a pinned id, named in the detail (low; title "GitHub organizations aren't confirmed", detail ending "Open Settings → Security → GitHub sign-in and press Save so Bothy can confirm the organizations."); fix `/settings/security/<provider>-sign-in`) | medium / low | ✓ | |
| `mcp.pending_pins` / `mcp.drifted` | medium | ✓ | ✓ disable drifted |
| `mcp.http_plaintext` (http:// non-private URL) | high | ✓ | |
| `skills.changed` | medium | ✓ | ✓ disable |
| `providers.auth_failed` / `channels.auth_failed` | medium | | |
| `providers.ollama_context` (an Ollama chain model's window was capped below its trained maximum because its context is not configured) | info | | |
| `providers.chat_tools` (a model whose tool calls run with `reasoning_effort: "none"` over Chat Completions) | low | | |
| `providers.chat_tools` (a model that cannot call tools over Chat Completions at all) | medium | | |
| `providers.bedrock_credentials` (v0.2: a bedrock provider's AWS credential source, never values) | info | | |
| `providers.bedrock_credentials` (a bedrock provider cannot get AWS credentials; fix `aws sso login --profile <p>` when a profile is set) | high | | |
| `github.connection_error` (v0.2: the last call or test of a GitHub connection failed) | medium | | |
| `github.token_expiring` (a connection's token expires within 7 days) | low | | |
| `github.unreadable` (the connections cannot be read) | medium | | |
| `releases.github_token` (v0.2: an environment whose repository no GitHub connection serves, and `releases.github_token` does not resolve) | medium | | |
| `backups.none_recent` | low | | |
| `config.errors` / `config.safe_mode` | high | | |

Owners: `sqlite.*`, `data.*`, `backups.*`, `vault.*` M1; `providers.*` M2; `policy.*`, `budgets.*` M4; `sandbox.*`, `docker.*` M5; `egress.*`, `mcp.http_plaintext` M6; `mcp.pending_pins`, `mcp.drifted`, `skills.*` M7; `channels.*` M9; `server.*`, `tokens.*`, `auth.sign_in` (auth) and `config.*` (`doctor.ConfigChecks`) M11; v0.2: `github.*` F4 (check `github.connections`), `releases.*` release. `providers.auth_failed` says "rejects its AWS credentials" for a bedrock provider. `server.exposure` is also raised when `BOTHY_DEPLOYMENT=container`, the listen address is `0.0.0.0` and no `public_url` with https is set (see the userland-proxy risk in §13.1), as **medium**: the gateway must listen on `0.0.0.0` inside the container and cannot see the address Docker publishes the port on, so the stock compose file (loopback publish) could never clear a high finding. Its fix says to keep the port published on 127.0.0.1 only, as the compose file does, and that terminating TLS (`server.tls`, or Caddy/Tailscale in `server.trusted_proxies` with an https `public_url`) also clears it. A non-loopback host listener without TLS or a proxy stays high.

---

## 11. Dependencies

### 11.1 Go (`go.mod`, the complete set; only the lead edits it)

| Module @ version | Used by | Why |
|---|---|---|
| `modernc.org/sqlite` v1.60.1 (SQLite 3.53.4, FTS5, `_defensive` DSN parameter since v1.57.0) | store | Pure-Go SQLite; nothing else fits CGO_ENABLED=0 with FTS5 |
| `modernc.org/libc` v1.77.1 (exact; CI asserts `go list -m modernc.org/libc`) | store | modernc requires an exact libc pin |
| `github.com/modelcontextprotocol/go-sdk` v1.8.0 | mcp; `github.com/google/jsonschema-go` (its dependency) used by tools for argument validation | MCP client for both spec eras; `IOTransport`, `StreamableClientTransport`, `SSEClientTransport` |
| `github.com/disgoorg/disgo` v0.19.6 | channel/discord | Maintained Discord gateway; caches disabled, minimal intents |
| `github.com/slack-go/slack` v0.30.1 | channel/slack | Socket Mode + Web API; configurable API URL for fakes |
| `github.com/gorilla/websocket` v1.5.3 | channel/discord, channel/slack (types only) | Already linked by disgo and slack-go; their dialer options take `*websocket.Dialer` and close codes arrive as `*websocket.CloseError` |
| `go.yaml.in/yaml/v3` (newest tag ≥ 14 days old; *verify*) | config | `yaml.Node` line numbers for provenance |
| `golang.org/x/crypto` (*verify*) | auth, vault | `argon2` only |
| `golang.org/x/net` (*verify*) | egress, policy, sandbox/bwrap | `html` (web_fetch text extraction), `idna` (punycode), `bpf` (serializing the seccomp program) |
| `github.com/adhocore/gronx` v1.20.5 | scheduler | Cron parsing and next tick with timezone; zero deps |
| `golang.org/x/image` v0.42.0 (published 2026-06-08) | picture | `webp` to read uploaded WebP pictures, `draw` for Catmull-Rom scaling; the default picture is rendered by `make icons` (Playwright's Chromium) and needs neither |
| `github.com/elastic/go-seccomp-bpf` v1.6.0 or newer (*verify* newest tag ≥ 14 days old) | sandbox/bwrap | `Policy.Assemble()` builds an arch-checked program without loading it (§2.12) |
| `github.com/aws/aws-sdk-go-v2` v1.47.1, `…/aws/protocol/eventstream` v1.7.20, `…/config` v1.33.6, `…/credentials` v1.20.6, `…/feature/ec2/imds` v1.20.1 (v0.2; indirect: `service/sso`, `ssooidc`, `sts`, `signin`, `internal/*`, `github.com/aws/smithy-go` v1.28.1; all published 2026-08-26..09-24) | provider/bedrock (eventstream also in `internal/provider` tests); vault/awssm: `aws` and `aws/signer/v4` only (§14.8) | The AWS default credential chain (env, shared files with SSO and `aws login`, assumed roles, web identity, ECS/EKS container and EC2 IMDSv2 credentials), SigV4 signing and event-stream decoding; `scripts/check-imports.sh` allows exactly these packages |

`internal/deps/deps.go` (`//go:build deps`) blank-imports each module so `go mod tidy` keeps them before modules use them. Stdlib covers HTTP/TLS, JSON, AES-GCM, HMAC, `os.Root`, `net/netip`, `log/slog`, `embed`, `testing/synctest`, `http.CrossOriginProtection`. Sign in with Google and GitHub (§15) adds no dependency: PKCE, the authorize addresses, the code exchange and the id token claims are hand-written over `net/http` and `encoding/json`.

**`tools/go.mod`** (separate module, not linked into the binary): `github.com/gzuidhof/tygo` (*verify*) for `types.gen.ts` [S]; `charm.land/catwalk` v0.52.69 for `tools/catalog-gen`, which (v0.2) also reads its embedded `bedrock.json` overlay (AWS Price List prices, `geo_price`) and needs catwalk's `anthropic.json` among its inputs, and writes `internal/provider/catalog/catalog.json` at release time [M2].

**Deliberately not used:**

| Not used | Instead | Reason |
|---|---|---|
| anthropic-sdk-go, openai-go, google.golang.org/genai | hand-written clients (~3.2K LOC incl. Ollama and the OpenAI Responses client) | implicit retries and env pickup, bypass of our dialer, size |
| telego | hand-written Telegram client (~700 LOC) | plain JSON long-poll; full control of preview flags and parse mode |
| coder/websocket | (disgo and slack-go bring gorilla/websocket) | no own WS code |
| chi, koanf, goose, sqlc, gocron | ServeMux, own loader, own migrator, hand SQL, own timer loop | fewer deps and tools; chi v5.4.0 is inside its cooldown |
| moby/client | hand-written Engine client in sandboxd (~400 LOC) | containerd/OCI/otel weight; only sandboxd needs it |
| AWS SDK service clients (`service/bedrockruntime`, `service/bedrock`) and SDK retries | hand-written Converse/ConverseStream mapping and model listing over the signed HTTP client; retries off | the facade owns fallback; the service clients add weight and implicit retries |
| otel, bubbletea/huh, age, wazero, go-landlock, sqlite-vec | — | P1/P2 or unnecessary (§13) |

### 11.2 npm (`web/package.json`, exact pins, lockfile committed, `npm ci --ignore-scripts`)

| Package | Version | Note |
|---|---|---|
| `svelte` | 5.57.2 | only runtime dependency |
| `vite` | 8.3.4 | build |
| `tailwindcss`, `@tailwindcss/vite` | 4.3.3 | external CSS |
| `@sveltejs/vite-plugin-svelte` | the Vite-8-compatible release, pinned exactly (*verify*) | build |
| `typescript`, `svelte-check` | pinned exactly (*verify*) | typecheck (dev) |
| `vitest`, `@testing-library/svelte`, `jsdom` | pinned exactly (*verify*) | unit tests (dev) |
| `@playwright/test` | pinned exactly (*verify*) | phase-3 UI smoke (dev, lead) |

No router, markdown, chart, icon, date or QR libraries.

### 11.3 Size and memory estimates (CI-enforced from the skeleton on)

| Part | Stripped estimate (linux/amd64, `-trimpath -s -w`) |
|---|---|
| Go runtime + net/http + TLS + JSON | ~6.5 MB |
| modernc sqlite + libc | ~6.5 MB |
| disgo + gorilla/websocket | ~2 MB |
| slack-go | ~1.5 MB |
| MCP go-sdk + jsonschema | ~1.2 MB |
| yaml, x/net, x/crypto, gronx, seccomp-bpf | ~1.2 MB |
| Bothy code + catalog.json + pre-gzipped UI (only the `.gz` variant of compressible assets is embedded; `index.html` stays raw) | ~4.5 MB |
| Web UI inside that, gzip (`scripts/budgets/ui-size.sh`) | first screen ≤ 120 KB JS and ≤ 20 KB CSS, all JS chunks ≤ 280 KB; measured 99,682 B JS (theme.js and one module) and 11,626 B CSS for the first screen, 249,606 B JS in 10 files (the single bundle before route chunks: 236,142 B) |
| **Binary** | **~23 MB → ~9 MB gzip** (v0.1) |
| v0.2 measured (placeholder UI) | linux/amd64 22.92 MB → 26.56 MB (+3.65 MB: AWS SDK ~2.4 MB, GitHub, knowledge, releases and refine ~1.2 MB), gzip 9.03 → 10.31 MB; linux/arm64 21.23 → 24.51 MB, gzip 8.27 → 9.42 MB; with the UI built, linux/amd64 25.5 MiB, gzip 10.0 MiB; gateway image (linux/arm64) 10.2 MiB compressed, against 9.1 MiB for v0.1 and the 30 MB budget; idle RSS with three fake channels 40.4 MiB on darwin/arm64 (v0.1: 35.1 MiB), cold start median 58 ms; with the redesigned UI and aws-sm references merged, linux/amd64 26.1 MiB, gzip 10.3 MiB, linux/arm64 24.1 MiB, gzip 9.4 MiB, gateway image (linux/arm64) 10.6 MiB compressed, idle RSS 40.9 MiB after 10 minutes, cold start median 69 ms, fixed token overhead 1,951; with the UI state work (migration 0009) merged, linux/amd64 26.3 MiB, gzip 10.4 MiB, linux/arm64 24.2 MiB, gzip 9.5 MiB, gateway image (linux/arm64) 10.6 MiB compressed, idle RSS 41.0 MiB after 10 minutes, cold start median 71 ms, fixed token overhead 1,951 |
| **Gateway image** (distroless static ≈ 1 MB) | **~10–11 MB compressed** (budget 30, CI warns at 20); measured 9.1 MiB (arm64), 9.8 MiB (amd64), 9.3 MiB (armv7) |
| Sandbox images (compose only) | measured 105.8 MiB (base) and 152.5 MiB (node) compressed on linux/arm64, against the original ~50 and ~80 MB estimates (apt layer ~173 MB unpacked, uv ~48 MB); no hard budget |

Idle RSS with 3 channels connected: Go runtime ~8 MB, SQLite page cache 2 MB × 3 connections, disgo (caches off) + slack-go + Telegram poller ~5 MB, event ring 4096 entries ~2 MB, one shared transport per dialer, a 48 MiB soft heap limit (`serve` applies `debug.SetMemoryLimit(48 MiB)` when `GOMEMLIMIT` is unset, so `docker run`, native and Homebrew installs match compose, which still sets `GOMEMLIMIT=48MiB` explicitly) ⇒ **~30–40 MB expected**. CI boots the binary against the fake platform servers without `GOMEMLIMIT`, waits 10 minutes, and fails above 60 MB. Cold start to `/healthz` ≤ 500 ms. Fixed token overhead (S1 + core tool schemas, standard profile) ≤ 2.5K estimated tokens; core tool schemas alone ≤ 1.5K.

**Measured (E18):** cold start to `/healthz` median 23 ms (linux/arm64, in a container; 45 ms native darwin/arm64); idle RSS 28.4 MiB peak after 10 minutes with the Telegram, Discord and Slack fakes connected (35.1 MiB darwin/arm64), the gateway container 30.0 MiB after 10 minutes idle; fixed token overhead 1943 without exec tools (S1 929 + core tools 1014) and 2190 with `shell`/`python` (core tools 1261), counted from the first agent request mockllm records.

---

## 12. Parallel work breakdown

### 12.1 Skeleton commit (lead, before fan-out)

Exit criteria: `go build ./... && go vet ./... && go test ./...` green on linux/amd64 and linux/arm64 (and `GOOS=windows go build ./...`); `bothy serve` boots against an empty data dir, prints the setup banner and answers `/healthz` and `/readyz`; `make ui` builds the scaffold; CI (below) green.

**Files and what they contain:**

| Path | Contents | State after skeleton |
|---|---|---|
| `go.mod`, `go.sum` | `module github.com/rielicat/bothy`, `go 1.26`, `toolchain go1.27.2`, every §11.1 dependency | frozen (lead only) |
| `internal/deps/deps.go` | `//go:build deps` blank imports of every dependency | frozen |
| `internal/core/*.go` | §4 verbatim + tests for the pure helpers | frozen |
| `internal/config/*.go` | §7.4, **fully implemented** with tests | frozen |
| `internal/events/*.go` | `core.Bus`: ring 4096, boot id, `Publish` never blocks (per-subscriber buffered channel; on overflow increments `Dropped` and stops delivering until the subscriber reads `Dropped` and resyncs), `Since`, filter matching per `core.Filter`; **fully implemented** with race tests | frozen |
| `internal/store/migrations/0001_init.sql` | §6 | frozen |
| `internal/store/*.go` | `Open` (pragmas, version gate, pre-migration backup, migrator) **implemented**; repositories stubbed (`core.Store` embedded nil) | → M1 |
| `internal/httpapi/dto/dto.go` | §8.4 | frozen |
| `web/src/lib/api/types.gen.ts` | generated by `make gen` | frozen (regenerated by lead) |
| every module package | `doc.go` + `new.go` with the exact constructor, `Deps` struct and stub types below | → owner |
| `internal/httpapi` stub | `Serve` listens on `server.listen` and serves only `/healthz`, `/readyz` until M11 replaces it | → M11 |
| `internal/testkit/**` | shared fakes and harnesses below, **implemented**, including `memstore` (an in-memory `core.Store` with Viewer scoping, `Decide` CAS, the `runs_one_active` conflict and keyset paging) and the `conformance.Store` suite that both memstore and M1's SQLite store must pass, so module tests written against memstore hold against the real store | frozen |
| `cmd/bothy/*.go` | dispatch, `wire.go` construction order, CLI stubs | lead (CLI filled in phase 3 on module APIs; module agents never edit `cmd/`) |
| `cmd/sbx-init/main.go` | one line | frozen |
| `web/` scaffold | `package.json` (exact pins), `vite.config.ts` (outDir `../internal/webui/dist`, `css: 'external'`), Tailwind entry, `App.svelte` shell, router stub, API client stub, SSE store stub | → M12 |
| `internal/webui/dist/index.html` | committed placeholder so `go:embed` compiles without node | build output |
| `deploy/Dockerfile`, `deploy/Dockerfile.release`, `deploy/compose.yaml`, `deploy/compose.source.yaml` (overrides the images with ones built from the checkout; the sandbox image is passed by image id in `BOTHY_SBX_IMAGES`), `deploy/.env.example`, `deploy/install.sh`, `deploy/searxng/settings.yml` | §2.1–2.3 | lead |
| `.goreleaser.yaml`, `.github/workflows/release.yml`, `.github/workflows/release-publish.yml` | A `v*` tag builds a draft release. goreleaser builds the static binaries and `sbx-init` (linux amd64/arm64, not archived); `dockers_v2` builds the gateway image (amd64/arm64/armv7) and the `sandbox` and `sandbox-node` images (amd64/arm64) from `deploy/sandbox/Dockerfile` with build arg `SBXINIT=release` (the `sbxinit-release` stage copies the release's signed binary from `$TARGETPLATFORM/sbx-init` instead of compiling it again) and `--target base` or `node`, pushing version tags only; SBOMs for every image; `docker_signs` cosign-signs every pushed manifest; the Homebrew cask is rendered only (`skip_upload: true`) and attached to the draft. The release workflow then attests SLSA build provenance for `bothy`, `bothy-sandbox` and `bothy-sandbox-node` (digests read from the manifest JSON) and builds the digest-pinned compose bundle (`scripts/release/compose-bundle.sh`). When a maintainer publishes the draft (`release: released`, prereleases excluded), `release-publish.yml` moves `latest` for the three images with `docker buildx imagetools create` (same digest, signatures and attestations) and commits the cask to the tap, so an unreviewed build is never what `latest` or `brew install` gets | lead |
| `Makefile` | `build`, `test` (`-race`), `ui`, `gen`, `lint`, `e2e`, `e2e-docker` (`scripts/e2e-docker.sh`), `image`, `budgets` | lead |
| `.github/workflows/ci.yml` | vet (also `-tags 'e2e budgets docker'`), staticcheck, `golangci-lint`, `go test -race ./...`, govulncheck, `scripts/check-imports.sh`, `scripts/check-ownership.sh`, `make gen` drift check, UI build + size gate, `e2e`, `e2e-docker` job (`make e2e-docker`: real compose deployment and the docker-tagged scenarios), budgets job (image size, RSS without `GOMEMLIMIT`, cold start, token overhead), on pull requests the dependency cooldown and the DCO sign-off check (`scripts/check-dco.sh`) | lead |
| `scripts/check-imports.sh` | §3 import rules, `os/exec` ban, `SystemViewer` ban in httpapi, no `http.DefaultClient` in `internal/` | lead |
| `scripts/check-ownership.sh` | fails a module PR touching paths outside its `OWNERSHIP.md` entry | lead |
| `OWNERSHIP.md`, `CONTRIBUTING.md` (ICR process, comment style, 14-day cooldown), `SECURITY.md`, `LICENSE`, `NOTICE`, `README.md`, `docs/SPEC.md` (this file) | | lead |
| `e2e/` | harness: builds the binary, temp data dir, starts mockllm and fake platform servers, helpers to drive the HTTP API and SSE; `e2e/dockerrelease.go`, `e2e/docker/compose.test.yaml` and `scripts/e2e-docker.sh` for the docker-tagged compose tests (§12.4) | lead (scenarios in phase 3) |

**Construction and wiring (`cmd/bothy/wire.go`, verbatim in the skeleton):**

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/rielicat/bothy/internal/agent"
	"github.com/rielicat/bothy/internal/audit"
	"github.com/rielicat/bothy/internal/auth"
	"github.com/rielicat/bothy/internal/budget"
	"github.com/rielicat/bothy/internal/channel"
	"github.com/rielicat/bothy/internal/channel/discord"
	"github.com/rielicat/bothy/internal/channel/slack"
	"github.com/rielicat/bothy/internal/channel/telegram"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/doctor"
	"github.com/rielicat/bothy/internal/egress"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/httpapi"
	"github.com/rielicat/bothy/internal/mcp"
	"github.com/rielicat/bothy/internal/memory"
	"github.com/rielicat/bothy/internal/policy"
	"github.com/rielicat/bothy/internal/provider"
	"github.com/rielicat/bothy/internal/sandbox"
	"github.com/rielicat/bothy/internal/scheduler"
	"github.com/rielicat/bothy/internal/skills"
	"github.com/rielicat/bothy/internal/store"
	"github.com/rielicat/bothy/internal/tools"
	"github.com/rielicat/bothy/internal/vault"
	"github.com/rielicat/bothy/internal/webui"
)

// app holds every constructed component; built in dependency order.
type app struct {
	cfg       *config.Manager
	log       *slog.Logger
	clock     core.Clock
	db        *store.DB
	redactor  *vault.Redactor
	audit     *audit.Writer
	vault     *vault.Vault
	bus       *events.Bus
	auth      *auth.Service
	policy    *policy.Engine
	approvals *policy.Approvals
	egress    *egress.Service
	sandbox   *sandbox.Client
	llm       *provider.Facade
	budget    *budget.Service
	memory    *memory.Service
	skills    *skills.Library
	registry  *tools.Registry
	mcp       *mcp.Manager
	runner    *agent.Runner
	hub       *channel.Hub
	scheduler *scheduler.Service
	doctor    *doctor.Runner
	api       *httpapi.Server
	restart   chan struct{} // POST /api/admin/restart
}

var errRestart = errors.New("restart requested")

func build(ctx context.Context) (*app, error) {
	setUmask() // 007: /data files are created 0600/0700 explicitly; workspace files 0660/2770
	a := &app{clock: core.SystemClock{}, restart: make(chan struct{}, 1)}
	var err error
	dataDir, defaulted := dataDir()
	if a.cfg, err = config.Load(ctx, config.Options{DataDir: dataDir, File: os.Getenv("BOTHY_CONFIG"), Env: os.Environ}); err != nil {
		return nil, dataDirErr(err, dataDir, defaulted)
	}
	c := a.cfg.Current()
	dirs := config.ResolveDirs(c) // "auto" work_dir, keyfile and run_dir resolved once (§2.1)
	a.redactor = vault.NewRedactor()
	a.log = slog.New(vault.NewSlogHandler(newLogHandler(c.Log), a.redactor))
	if moved, err := config.MoveLegacyWork(dirs); err != nil {
		a.log.Error("could not move the workspaces to the work volume; copy them by hand", "from", dirs.LegacyWork, "to", dirs.Work, "err", err)
	} else if moved {
		a.log.Warn("moved the workspaces to the work volume", "from", dirs.LegacyWork, "to", dirs.Work)
	}
	a.bus = events.New(events.Options{Ring: 4096, Clock: a.clock})
	if a.db, err = store.Open(ctx, store.Options{Path: filepath.Join(dirs.Data, "bothy.db"), WorkDir: dirs.Work,
		BackupDir: filepath.Join(dirs.Data, "backups"), BackupsKeep: c.Storage.BackupsKeep, Clock: a.clock, Log: a.log}); err != nil {
		return nil, dataDirErr(err, dataDir, defaulted)
	}
	if a.audit, err = audit.New(audit.Deps{Store: a.db, Redactor: a.redactor, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.cfg.SetOnSet(func(r config.ChangeRecord) { auditConfig(ctx, a.audit, r) })
	a.cfg.Watch(func(old, cur *config.Config) { publishConfigChanged(a.bus, a.cfg, old, cur) })
	publishConfigBanners(a.bus, a.cfg.Effective())
	if a.vault, err = vault.New(vault.Deps{Store: a.db, Config: a.cfg, Redactor: a.redactor, Audit: a.audit, Env: os.Environ, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.auth, err = auth.New(auth.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, Redactor: a.redactor, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.policy, a.approvals, err = policy.New(policy.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.egress, err = egress.New(egress.Deps{Config: a.cfg, Store: a.db, Vault: a.vault, Approvals: a.approvals, Policy: a.policy,
		Bus: a.bus, Audit: a.audit, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.sandbox, err = sandbox.New(ctx, sandbox.Deps{Config: a.cfg, Egress: a.egress, Store: a.db, Bus: a.bus, Audit: a.audit,
		WorkDir: dirs.Work, RunDir: dirs.Run, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.llm, err = provider.New(provider.Deps{Config: a.cfg, Vault: a.vault, Egress: a.egress, Env: os.Environ, Clock: a.clock, Log: a.log, Bus: a.bus}); err != nil {
		return nil, err
	}
	if a.budget, err = budget.New(budget.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.memory, err = memory.New(memory.Deps{Store: a.db, Config: a.cfg, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.skills, err = skills.New(skills.Deps{Store: a.db, Config: a.cfg, Audit: a.audit, Bus: a.bus, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.registry = tools.NewRegistry(tools.RegistryDeps{Config: a.cfg, Log: a.log})
	if a.mcp, err = mcp.New(mcp.Deps{Config: a.cfg, Store: a.db, Sandbox: a.sandbox, Egress: a.egress, Registry: a.registry,
		Vault: a.vault, Bus: a.bus, Audit: a.audit, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.runner, err = agent.New(agent.Deps{Store: a.db, Config: a.cfg, LLM: a.llm, Tools: a.registry, Policy: a.policy,
		Approvals: a.approvals, Budget: a.budget, Memory: a.memory, Skills: a.skills, Sandbox: a.sandbox, Egress: a.egress,
		Redactor: a.redactor, Bus: a.bus, Audit: a.audit, DataDir: dirs.Data, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	if a.hub, err = channel.New(channel.Deps{Config: a.cfg, Auth: a.auth, Runner: a.runner, Approvals: a.approvals, Budget: a.budget,
		Sandbox: a.sandbox, Store: a.db, Bus: a.bus, Audit: a.audit, Redactor: a.redactor, WorkDir: dirs.Work, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	a.runner.SetHub(a.hub) // breaks the runner <-> hub cycle: resumed-run sinks and restart notices
	adapterDeps := func(name string) core.AdapterDeps {
		return core.AdapterDeps{
			Secret: func(ctx context.Context, ref string) (core.Secret, error) {
				return a.vault.Resolve(ctx, ref, "", "channel:"+name) // empty user: global scope only
			},
			HTTP:     func(d core.SystemDest) *http.Client { return a.egress.SystemClient(d) },
			Meta:     a.db.Meta(),
			StateDir: filepath.Join(dirs.Data, "channels", name), // created by adapters that need it (P1)
			Clock:    a.clock,
			Log:      a.log.With("channel", name),
		}
	}
	for _, mk := range []func() (core.Channel, error){
		func() (core.Channel, error) { return telegram.New(c.Channels.Telegram, adapterDeps("telegram")) },
		func() (core.Channel, error) { return discord.New(c.Channels.Discord, adapterDeps("discord")) },
		func() (core.Channel, error) { return slack.New(c.Channels.Slack, adapterDeps("slack")) },
	} {
		ch, err := mk()
		if errors.Is(err, core.ErrNotImplemented) {
			continue // skeleton stubs
		}
		if err != nil {
			return nil, err
		}
		a.hub.Add(ch) // disabled channels report state "disabled"
	}
	if a.scheduler, err = scheduler.New(scheduler.Deps{Store: a.db, Config: a.cfg, Runner: a.runner, Hub: a.hub, Memory: a.memory,
		Bus: a.bus, Audit: a.audit, Clock: a.clock, Log: a.log}); err != nil {
		return nil, err
	}
	// Late tool registration: every provider's tools, then builtins. Snapshots are
	// frozen per session, so registration must finish before the API serves.
	// MCP tools register themselves later through Deps.Registry (lazy tools).
	var all []core.Tool
	all = append(all, tools.Builtins(tools.BuiltinDeps{Config: a.cfg, Messenger: a.hub, Sandbox: a.sandbox, Clock: a.clock})...)
	for _, p := range []core.ToolProvider{a.sandbox, a.egress, a.memory, a.skills, a.scheduler} {
		all = append(all, p.Tools()...)
	}
	for _, t := range all {
		if err := a.registry.Register(t); err != nil {
			return nil, err
		}
	}
	checks := doctor.ConfigChecks(a.cfg) // config.errors, config.safe_mode
	for _, p := range []core.CheckProvider{a.db, a.vault, a.auth, a.policy, a.egress, a.sandbox, a.llm, a.memory, a.skills, a.mcp, a.hub} {
		checks = append(checks, p.Checks()...)
	}
	a.doctor = doctor.New(checks)
	a.api, err = httpapi.New(httpapi.Deps{Config: a.cfg, Store: a.db, Auth: a.auth, Vault: a.vault, Bus: a.bus, Runner: a.runner,
		Approvals: a.approvals, Policy: a.policy, Tools: a.registry, LLM: a.llm, Budget: a.budget, Memory: a.memory, Skills: a.skills, MCP: a.mcp,
		Scheduler: a.scheduler, Hub: a.hub, Sandbox: a.sandbox, Egress: a.egress, Doctor: a.doctor, Audit: a.audit,
		WebUI: webui.Handler(), Version: version, DataDir: dirs.Data, WorkDir: dirs.Work, Restart: a.requestRestart,
		Clock: a.clock, Log: a.log})
	return a, err
}

func (a *app) requestRestart() {
	select {
	case a.restart <- struct{}{}:
	default: // one pending request is enough
	}
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: bothy serve\n\nRuns the gateway. Settings come from <data>/bothy.yaml and BOTHY_* variables;\nthe data directory is $BOTHY_STORAGE__DATA_DIR (default /data).\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errUsage
	}
	setMemoryLimit()
	a, err := build(ctx)
	if err != nil {
		return err
	}
	c := a.cfg.Current()
	switch tok, created, err := a.auth.EnsureSetupToken(ctx); {
	case err != nil:
		a.log.Error("setup token", "err", err)
	case created:
		printSetupBanner(os.Stderr, c, tok)
	default:
		if pending, err := a.auth.NeedsSetup(ctx); err == nil && pending {
			printSetupPending(os.Stderr, c)
		}
	}
	a.log.Info("gateway starting", "listen", c.Server.Listen, "url", baseURL(c), "version", version)
	// Every resolution is forwarded; Resume is a no-op unless the run is parked.
	a.approvals.OnResolved(func(ap *core.Approval) { _ = a.runner.Resume(context.WithoutCancel(ctx), ap) })
	// Before any intake: a run submitted while recovery is pending would be
	// marked interrupted as a leftover of the previous process.
	if err := a.runner.Recover(ctx); err != nil {
		a.log.Error("boot recovery", "err", err)
	}
	loops := []func(context.Context) error{
		a.audit.Run, a.approvals.Run, a.egress.Run, a.sandbox.Run, a.runner.Run,
		func(ctx context.Context) error { return a.db.RunRetention(ctx, a.cfg) }, a.api.Serve,
	}
	if !a.cfg.Effective().SafeMode { // safe mode: no chat intake, schedules or MCP (§7.2)
		loops = append(loops, a.hub.Run, a.scheduler.Run, a.mcp.Start)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-a.restart:
			cancel(errRestart)
		case <-ctx.Done():
		}
	}()
	var wg sync.WaitGroup
	for _, run := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				cancel(err)
			}
		}()
	}
	wg.Wait()
	_ = a.mcp.Close()
	_ = a.audit.Flush(context.Background())
	_ = a.db.Close()
	cause := context.Cause(ctx)
	if errors.Is(cause, errRestart) {
		return reexec() // syscall.Exec of os.Executable() (works in distroless); Windows starts a fresh process
	}
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}
```

```go
// Command bothy is the single binary: gateway, sandbox sidecar and CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/rielicat/bothy/internal/sandboxd"
	"github.com/rielicat/bothy/internal/sbxinit"
)

var version = "dev" // -ldflags "-X main.version=…"

const usage = `usage: bothy <command> [flags]

commands:
  serve            run the gateway (default)
  sandboxd         run the sandbox sidecar (needs the container runtime socket)
  sbx-init         sandbox PID 1 / exec wrapper (inside tool boxes and bwrap)
  doctor           run health checks            [--json]
  security-audit   run security checks          [--fix] [--json]
  setup-token      print a fresh one-time setup token (only while no owner exists)
  user             user admin: reset-password <name> | list
  vault            vault admin: rotate-kek (gateway stopped) | status
  config           check [file] | defaults | effective
  audit            verify | archive --before <date> (gateway stopped)
  backup           write a consistent backup to <data>/backups
  healthcheck      exit 0 when /healthz answers (container HEALTHCHECK) [sandboxd]
  version          print version

The data directory is $BOTHY_STORAGE__DATA_DIR (default /data).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run dispatches one invocation and returns the exit code: 0 ok (and help),
// 1 failure, 2 usage. Usage errors are caught before any state is opened, so a
// mistyped command never creates a data dir, database or KEK.
func run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	cmd, args := "serve", argv
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	code := 0
	switch cmd {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		err = serve(ctx, args)
	case "sandboxd":
		err = sandboxd.Main(ctx, args, os.Environ)
	case "sbx-init":
		return sbxinit.Main(args)
	case "doctor":
		code, err = doctorCmd(ctx, args, false)
	case "security-audit":
		code, err = doctorCmd(ctx, args, true)
	case "setup-token", "user", "vault", "config", "audit", "backup":
		if slices.ContainsFunc(args, isHelpFlag) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		err = runAdmin(ctx, cmd, args, stdout, stderr)
	case "healthcheck":
		err = healthcheck(ctx, args)
	case "version":
		fmt.Fprintln(stdout, version)
	default:
		fmt.Fprintf(stderr, "bothy: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprint(stderr, usage)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "bothy:", err)
		return 1
	}
	return code
}

func isHelpFlag(s string) bool { return s == "-h" || s == "-help" || s == "--help" }
```

```go
// ===== cmd/bothy/cli.go =====
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// dataDir is $BOTHY_STORAGE__DATA_DIR, else /data; dataDirErr adds a
// BOTHY_STORAGE__DATA_DIR hint when that default fails outside a container.
func dataDir() (dir string, defaulted bool)                     { … }
func dataDirErr(err error, dir string, defaulted bool) error    { … }

// defaultMemLimit is the soft heap limit the footprint budget assumes (SPEC
// §11.3). serve applies it unless GOMEMLIMIT is set, so `docker run`, native
// and Homebrew installs behave like the compose deployment.
const defaultMemLimit = 48 << 20

func setMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemLimit)
	}
}

func newLogHandler(l config.Log) slog.Handler {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(l.Level))
	o := &slog.HandlerOptions{Level: lvl}
	if l.Format == "json" {
		return slog.NewJSONHandler(os.Stderr, o)
	}
	return slog.NewTextHandler(os.Stderr, o)
}

func auditConfig(ctx context.Context, w core.Auditor, r config.ChangeRecord) {
	d, _ := json.Marshal(r)
	_ = w.Record(ctx, core.AuditEntry{At: time.Now(), Actor: r.By, Action: "config." + r.Action, Target: r.Path, Outcome: "ok", Detail: d})
}

func publishConfigChanged(bus core.Bus, cfg config.Admin, old, cur *config.Config) {
	eff := cfg.Effective()
	bus.Publish(core.Event{Type: core.EvConfigChanged, Data: core.ConfigChangedData{Paths: config.Diff(old, cur), RestartRequired: eff.Restart}})
	if len(eff.Restart) > 0 {
		bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "warn", Code: "restart_required",
			Text: "Some changes need a restart.", Action: "/settings/general"}})
	}
	publishConfigBanners(bus, eff)
}

func publishConfigBanners(bus core.Bus, eff config.Effective) {
	b := core.BannerData{Level: "error", Code: "config_error", Action: "/settings/advanced",
		Text: "Some config values are invalid and fell back to the next layer.", Clear: len(eff.Errors) == 0}
	bus.Publish(core.Event{Type: core.EvBanner, Data: b})
	if eff.SafeMode {
		bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "error", Code: "safe_mode", Action: "/settings/advanced",
			Text: "bothy.yaml could not be parsed: safe mode (chat, channels and schedules are off until a restart)."}})
	}
}

func printSetupBanner(w io.Writer, c *config.Config, token string) {
	fmt.Fprintf(w, "\n  Bothy is ready for setup.\n  Open %s/setup#t=%s\n  (token also in %s/setup-token; valid %s)\n\n",
		baseURL(c), token, c.Storage.DataDir, c.Auth.SetupTokenTTL.D())
}

// printSetupPending is the boot notice while setup is unfinished and the
// token minted earlier is still valid; the token itself is not repeated.
func printSetupPending(w io.Writer, c *config.Config) {
	fmt.Fprintf(w, "\n  Bothy is waiting for setup.\n  Open %s/setup and paste the token from %s/setup-token\n  (or print a fresh one with: bothy setup-token)\n\n",
		baseURL(c), c.Storage.DataDir)
}

// baseURL is public_url, else the listen address dialled from this host
// (https when server.tls is set; wildcard binds dialled on loopback).
func baseURL(c *config.Config) string { … }

// healthcheck probes the listener serve opened: server.listen and server.tls
// come from the same layered config (bothy.yaml, env, UI overrides). `healthcheck
// sandboxd` probes the sidecar instead, which shares the image (and so its
// HEALTHCHECK) but serves no HTTP port.
func healthcheck(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "sandboxd":
		// Connecting is the signal: sandboxd closes connections from any uid
		// but the gateway's, which a probe running as SANDBOXD_USER may not be.
		c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", envOr("BOTHY_SBX_SOCKET", "/run/bothy/sandboxd.sock"))
		if err != nil {
			return err
		}
		return c.Close()
	case len(args) > 0:
		return fmt.Errorf("healthcheck takes no arguments, or sandboxd")
	}
	url, useTLS := probeTarget(ctx)
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if useTLS {
		// The certificate names the public host, not the loopback address
		// probed here; the probe checks liveness, not identity.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback probe of our own listener
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil) //nolint:gosec // loopback probe from server.listen
	resp, err := (&http.Client{Transport: tr}).Do(req)                             //nolint:gosec // loopback probe from server.listen
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

// probeTarget is the loopback URL of server.listen (and whether server.tls is
// on) from config.Load; should the config not load, BOTHY_SERVER__LISTEN or
// the built-in default.
func probeTarget(ctx context.Context) (url string, useTLS bool) { … }

// doctorCmd builds the app without starting loops, runs checks, prints them.
// The CLI run also prints a stderr note: it cannot see the running gateway's
// state, so checks such as providers.auth_failed and channels.auth_failed
// never fire there; the web UI runs them live.
func doctorCmd(ctx context.Context, args []string, security bool) (int, error) {
	return 0, core.ErrNotImplemented // lead, phase 3: build() + doctor.Runner.Run/Fix + doctor.Print
}

// runAdmin (admin.go, phase 3) validates each admin command against the
// adminOps table (command, subcommand, argument count) before buildBase, so
// a mistyped command creates no data dir, database or KEK; then it runs the
// module APIs: setup-token (Auth.RotateSetupToken), user (Auth.ResetPassword,
// Users().List), vault (Vault.RotateKEK, Vault.Status), audit
// (Auditor.Verify, audit.Archive), backup (Store.Backup). config is handled
// without buildBase: `check [file]` (config.Load), `defaults` (config.Default
// plus the container listen default) and `effective` (YAML with a source
// comment per value). A default /data that fails outside a container adds a
// BOTHY_STORAGE__DATA_DIR hint (dataDirErr).
func runAdmin(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) error {
	return core.ErrNotImplemented
}

// ===== cmd/bothy/umask_unix.go =====
//go:build unix

package main

import (
	"os"
	"syscall"
)

func setUmask() { syscall.Umask(0o007) }

func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}

// ===== cmd/bothy/umask_other.go =====
//go:build !unix

package main

func setUmask() {}

// reexec has no exec(2) to use here, so it starts a fresh copy and lets this
// process exit; serve has already closed the listener and the database.
func reexec() error { return respawn() }

// ===== cmd/bothy/respawn.go =====
package main

import "os"

// respawn starts os.Executable() with this process's arguments, environment
// and standard streams, and does not wait for it.
func respawn() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p, err := os.StartProcess(exe, os.Args, &os.ProcAttr{Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	if err != nil {
		return err
	}
	return p.Release()
}
```

**Module constructors (`new.go` in each package; signatures and `Deps` are frozen, bodies are stubs the owner replaces).** Stub types embed the core interface they implement (a nil interface: calling an unimplemented method panics, which is intended until the module lands) and add their extra methods (`Run`, `Tools`, `Checks`). The listing shows compile stubs; in the actual skeleton `store.Open` and `events.New` are real implementations and the `httpapi` stub serves `/healthz` and `/readyz`:

```go
// ===== internal/store/new.go =====
// Package store implements core.Store on modernc.org/sqlite. Owner: M1.
package store

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Options struct {
	Path        string // <data>/bothy.db
	BackupDir   string // <data>/backups
	WorkDir     string // resolved storage.work_dir; data.permissions skips it (shared with the sandbox uid)
	BackupsKeep int
	Clock       core.Clock
	Log         *slog.Logger
}

// DB implements core.Store and core.CheckProvider.
type DB struct {
	core.Store // skeleton stub: nil; M1 replaces with real fields
}

// Open sets pragmas, refuses SQLite < 3.53.4, takes a VACUUM INTO backup when
// migrations are pending, then applies embedded migrations (skeleton-implemented).
func Open(ctx context.Context, o Options) (*DB, error) { return &DB{}, nil }

// RunRetention prunes egress_log, inbound_dedup, expired auth sessions and old
// runs per config every hour. Blocks until ctx is done.
func (db *DB) RunRetention(ctx context.Context, cfg config.View) error {
	<-ctx.Done()
	return nil
}

func (db *DB) Checks() []core.Check { return nil }

// Boot-path methods implemented in the skeleton (the rest of core.Store is M1's).
func (db *DB) Meta() core.MetaRepo { return nil }
func (db *DB) Close() error        { return nil }

// ===== internal/vault/new.go =====
// Package vault implements core.Vault (envelope encryption) and core.Redactor. Owner: M1.
package vault

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// Redactor implements core.Redactor. Created first: the logger, audit and vault use it.
type Redactor struct{ core.Redactor }

func NewRedactor() *Redactor { return &Redactor{} }

// NewSlogHandler wraps h so the message and every string attribute pass through
// r.Redact (the skeleton stub returns h unchanged until M1 lands).
func NewSlogHandler(h slog.Handler, r *Redactor) slog.Handler { return h }

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
type Vault struct{ core.Vault }

func New(d Deps) (*Vault, error) { return &Vault{}, nil }

func (v *Vault) Checks() []core.Check { return nil }

// ===== internal/audit/new.go =====
// Package audit is the single writer of the hash-chained audit log. Owner: M1.
package audit

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Redactor core.Redactor
	Clock    core.Clock
	Log      *slog.Logger
}

// Writer implements core.Auditor.
type Writer struct{ core.Auditor }

func New(d Deps) (*Writer, error) { return &Writer{}, nil }

// Run flushes batched entries every 250ms until ctx is done, then flushes once more.
func (w *Writer) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (w *Writer) Flush(ctx context.Context) error { return nil }

// ===== internal/events/new.go =====
// Package events is the in-process bus with a replay ring. Skeleton-implemented, frozen.
package events

import "github.com/rielicat/bothy/internal/core"

type Options struct {
	Ring  int // default 4096
	Clock core.Clock
}

// Bus implements core.Bus.
type Bus struct{ core.Bus }

func New(o Options) *Bus { return &Bus{} }

// Seq returns the newest Seq assigned, 0 before the first Publish. Not part
// of the frozen core.Bus: httpapi reaches it through an interface assertion
// for the hello of a fresh SSE stream (§8.2).
func (b *Bus) Seq() uint64 { return 0 }

// ===== internal/auth/new.go =====
// Package auth implements core.Auth. Owner: M11.
package auth

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Config   config.View
	Audit    core.Auditor
	Bus      core.Bus
	Redactor core.Redactor
	DataDir  string // setup-token file location
	Clock    core.Clock
	Log      *slog.Logger
}

// Service implements core.Auth and core.CheckProvider.
type Service struct{ core.Auth }

func New(d Deps) (*Service, error) { return &Service{}, nil }

func (s *Service) Checks() []core.Check { return nil }

// EnsureSetupToken is a boot-path stub (no token until M11 lands).
func (s *Service) EnsureSetupToken(ctx context.Context) (string, bool, error) { return "", false, nil }

// ===== internal/policy/new.go =====
// Package policy implements core.Policy, core.Approvals and the card renderer. Owner: M4.
package policy

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Engine implements core.Policy and core.CheckProvider.
type Engine struct{ core.Policy }

// Approvals implements core.Approvals.
type Approvals struct{ core.Approvals }

func New(d Deps) (*Engine, *Approvals, error) { return &Engine{}, &Approvals{}, nil }

// Run expires due approvals every 30s until ctx is done.
func (a *Approvals) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (e *Engine) Checks() []core.Check { return nil }

func (a *Approvals) OnResolved(fn func(*core.Approval)) {}

// ===== internal/egress/new.go =====
// Package egress implements the egress proxy, SSRF guard, dialers and the
// web_fetch / web_search / net_allow tools. Owner: M6.
package egress

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Store     core.Store
	Vault     core.Vault // search API keys
	Approvals core.Approvals
	Policy    core.Policy // net_allow grants
	Bus       core.Bus
	Audit     core.Auditor
	RunDir    string // /run/bothy
	Clock     core.Clock
	Log       *slog.Logger
}

// Service implements core.Egress, core.ToolProvider and core.CheckProvider.
type Service struct{ core.Egress }

func New(d Deps) (*Service, error) { return &Service{}, nil }

// Run batches egress_log writes until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *Service) Tools() []core.Tool   { return nil }
func (s *Service) Checks() []core.Check { return nil }

// ===== internal/sandbox/new.go =====
// Package sandbox implements core.Sandbox (sandboxd client, bwrap, none) and the
// shell/python tools. Owner: M5.
package sandbox

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config  config.View
	Egress  core.Egress
	Store   core.Store
	Bus     core.Bus
	Audit   core.Auditor
	WorkDir string // /work
	RunDir  string // /run/bothy
	Clock   core.Clock
	Log     *slog.Logger
}

// Client implements core.Sandbox, core.ToolProvider and core.CheckProvider.
type Client struct{ core.Sandbox }

// New probes backends in order sandboxd -> bwrap -> none (per sandbox.backend).
func New(ctx context.Context, d Deps) (*Client, error) { return &Client{}, nil }

// Run watches sandboxd health, reaps idle boxes and scans workspace quotas.
func (c *Client) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (c *Client) Tools() []core.Tool   { return nil }
func (c *Client) Checks() []core.Check { return nil }

// ===== internal/sandboxd/new.go =====
// Package sandboxd is the sidecar that alone holds the container runtime socket. Owner: M5.
package sandboxd

import "context"

// Main runs `bothy sandboxd`. Configuration comes only from BOTHY_SBX_* env.
func Main(ctx context.Context, args []string, env func() []string) error {
	<-ctx.Done()
	return nil
}

// ===== internal/sbxinit/new.go =====
// Package sbxinit is PID 1 and the exec wrapper inside tool boxes. Stdlib only. Owner: M5.
package sbxinit

// Main implements `sbx-init [run|killall|count|selftest] …`; returns the exit code.
func Main(args []string) int { return 0 }

// ===== internal/provider/new.go =====
// Package provider implements core.LLM over hand-written Anthropic, OpenAI,
// Gemini, Ollama and OpenAI-compatible clients. Owner: M2.
package provider

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config config.View
	Vault  core.Vault
	Egress core.Egress     // SystemClient per provider base URL
	Env    func() []string // Detect only: names of well-known key env vars
	Clock  core.Clock
	Log    *slog.Logger
	Bus    core.Bus // optional: provider_auth banner on a rejected key (§5.14)
}

// Facade implements core.LLM and core.CheckProvider.
type Facade struct{ core.LLM }

func New(d Deps) (*Facade, error) { return &Facade{}, nil }

func (f *Facade) Checks() []core.Check { return nil }

// ===== internal/budget/new.go =====
// Package budget implements core.Budget. Owner: M3.
package budget

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Service implements core.Budget.
type Service struct{ core.Budget }

func New(d Deps) (*Service, error) { return &Service{}, nil }

// ===== internal/memory/new.go =====
// Package memory implements core.Memory and the memory/session_search tools. Owner: M8.
package memory

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store   core.Store
	Config  config.View
	Bus     core.Bus
	Audit   core.Auditor
	DataDir string // files under <data>/memory/<principal>/
	Clock   core.Clock
	Log     *slog.Logger
}

// Service implements core.Memory and core.CheckProvider.
type Service struct{ core.Memory }

func New(d Deps) (*Service, error) { return &Service{}, nil }

func (s *Service) Tools() []core.Tool   { return nil }
func (s *Service) Checks() []core.Check { return nil }

// ===== internal/skills/new.go =====
// Package skills implements core.Skills (SKILL.md loader) and skill tools. Owner: M7.
package skills

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store   core.Store
	Config  config.View
	Audit   core.Auditor
	Bus     core.Bus // skills_changed banner
	WorkDir string   // resolved work dir; skills.dir "" = <WorkDir>/skills
	Clock   core.Clock
	Log     *slog.Logger
}

// Library implements core.Skills and core.CheckProvider.
type Library struct{ core.Skills }

func New(d Deps) (*Library, error) { return &Library{}, nil }

func (l *Library) Tools() []core.Tool   { return nil }
func (l *Library) Checks() []core.Check { return nil } // skills.changed (fix: disable)

// ===== internal/tools/new.go =====
// Package tools implements core.ToolRegistry, tool_search/use_tool, and the
// fs_* and message builtins. Owner: M4.
package tools

import (
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type RegistryDeps struct {
	Config config.View
	Log    *slog.Logger
}

// Registry implements core.ToolRegistry.
type Registry struct{ core.ToolRegistry }

func NewRegistry(d RegistryDeps) *Registry { return &Registry{} }

func (r *Registry) Register(t core.Tool) error { return nil }

type BuiltinDeps struct {
	Config    config.View
	Messenger core.Messenger // message tool, bound to env.Chat
	Sandbox   core.Sandbox   // workspace quota check before writes
	Clock     core.Clock
}

// Builtins returns fs_read, fs_write, fs_list, fs_delete, message.
func Builtins(d BuiltinDeps) []core.Tool { return nil }

// ===== internal/mcp/new.go =====
// Package mcp implements core.MCP over the official go-sdk client. Owner: M7.
package mcp

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config   config.View
	Store    core.Store
	Sandbox  core.Sandbox
	Egress   core.Egress
	Registry core.ToolRegistry
	Vault    core.Vault
	Bus      core.Bus
	Audit    core.Auditor
	WorkDir  string // resolved work dir: install trees under <WorkDir>/mcp/<server>/<install_id>
	Clock    core.Clock
	Log      *slog.Logger
}

// Manager implements core.MCP and core.CheckProvider.
type Manager struct{ core.MCP }

func New(d Deps) (*Manager, error) { return &Manager{}, nil }

func (m *Manager) Checks() []core.Check { return nil }

func (m *Manager) Start(ctx context.Context) error { return nil }
func (m *Manager) Close() error                    { return nil }

// ===== internal/agent/new.go =====
// Package agent implements core.Runner: session workers, the run loop, context
// assembly, compaction, stop and resume. Owner: M3.
package agent

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store     core.Store
	Config    config.View
	LLM       core.LLM
	Tools     core.ToolRegistry
	Policy    core.Policy
	Approvals core.Approvals
	Budget    core.Budget
	Memory    core.Memory
	Skills    core.Skills
	Sandbox   core.Sandbox
	Egress    core.Egress
	Redactor  core.Redactor
	Bus       core.Bus
	Audit     core.Auditor
	DataDir   string // SOUL.md
	WorkDir   string // workspaces
	Clock     core.Clock
	Log       *slog.Logger
}

// Runner implements core.Runner.
type Runner struct{ core.Runner }

func New(d Deps) (*Runner, error) { return &Runner{}, nil }

// SetHub is called once by cmd/bothy after the hub exists (the hub depends on
// the runner). The runner uses it for sinks of resumed parked runs
// (Hub.StreamTo) and for restart notices (Messenger.Send); before SetHub, or
// with a nil hub in tests, those deliveries are skipped.
func (r *Runner) SetHub(h core.ChannelHub) {}

// Run performs boot recovery (MarkInterrupted, resumable parked runs) and reaps
// idle session workers until ctx is done; then drains for up to 10s.
func (r *Runner) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// ===== internal/scheduler/new.go =====
// Package scheduler implements core.Scheduler and the schedule_* tools. Owner: M8.
package scheduler

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Runner core.Runner
	Hub    core.ChannelHub // delivery sinks
	Memory core.Memory     // HEARTBEAT.md
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Service implements core.Scheduler.
type Service struct{ core.Scheduler }

func New(d Deps) (*Service, error) { return &Service{}, nil }

func (s *Service) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (s *Service) Tools() []core.Tool { return nil }

// ===== internal/channel/new.go =====
// Package channel is the channel router (core.ChannelHub). Owner: M9.
package channel

import (
	"context"
	"log/slog"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Config    config.View
	Auth      core.Auth
	Runner    core.Runner
	Approvals core.Approvals
	Budget    core.Budget
	Sandbox   core.Sandbox // /status only
	Store     core.Store
	Bus       core.Bus
	Audit     core.Auditor
	Redactor  core.Redactor
	WorkDir   string // attachments are written to <WorkDir>/ws/<workspace>/.bothy/in/
	Clock     core.Clock
	Log       *slog.Logger
}

// Hub implements core.ChannelHub and core.CheckProvider.
type Hub struct{ core.ChannelHub }

func New(d Deps) (*Hub, error) { return &Hub{}, nil }

func (h *Hub) Checks() []core.Check { return nil }

func (h *Hub) Add(ch core.Channel) {}

func (h *Hub) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// ===== internal/channel/telegram/new.go =====
// Package telegram implements core.Channel for Telegram. Owner: M9.
package telegram

import (
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Telegram, d core.AdapterDeps) (core.Channel, error) {
	return nil, core.ErrNotImplemented
}

// ===== internal/channel/discord/new.go =====
// Package discord implements core.Channel for Discord. Owner: M10.
package discord

import (
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Discord, d core.AdapterDeps) (core.Channel, error) {
	return nil, core.ErrNotImplemented
}

// ===== internal/channel/slack/new.go =====
// Package slack implements core.Channel for Slack. Owner: M10.
package slack

import (
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Slack, d core.AdapterDeps) (core.Channel, error) {
	return nil, core.ErrNotImplemented
}

// ===== internal/doctor/new.go =====
// Package doctor runs core.Check sets for `bothy doctor`, `bothy security-audit`
// and /api/doctor. Owner: M11.
package doctor

import (
	"context"
	"io"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Runner struct{ checks []core.Check }

func New(checks []core.Check) *Runner { return &Runner{checks: checks} }

// ConfigChecks returns config.errors and config.safe_mode (from Effective()),
// which no module owns because internal/config is skeleton code.
func ConfigChecks(cfg config.Admin) []core.Check { return nil }

// Run executes every check (security-only when security is true), sorted by severity.
func (r *Runner) Run(ctx context.Context, security bool) []core.Finding { return nil }

// Fix applies AutoFix findings and returns the fixed ids.
func (r *Runner) Fix(ctx context.Context, fs []core.Finding) []string { return nil }

// Print renders findings as text (exit code 1 when any >= high) or JSON.
func Print(w io.Writer, fs []core.Finding, asJSON bool) int { return 0 }

// ===== internal/httpapi/new.go =====
// Package httpapi serves every route in SPEC §8. Owner: M11.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/doctor"
)

type Deps struct {
	Config    config.Admin
	Store     core.Store
	Auth      core.Auth
	Vault     core.Vault
	Bus       core.Bus
	Runner    core.Runner
	Approvals core.Approvals
	Policy    core.Policy
	Tools     core.ToolRegistry // POST /api/policy/preview plans sample calls
	LLM       core.LLM
	Budget    core.Budget
	Memory    core.Memory
	Skills    core.Skills
	MCP       core.MCP
	Scheduler core.Scheduler
	Hub       core.ChannelHub
	Sandbox   core.Sandbox
	Egress    core.Egress
	Doctor    *doctor.Runner
	Audit     core.Auditor
	WebUI     http.Handler
	Version   string
	DataDir   string // persona file (agent.persona_file), backups
	WorkDir   string // uploads and workspace downloads (os.Root under <WorkDir>/ws/<id>)
	Restart   func() // POST /api/admin/restart: asks cmd/bothy to drain and re-exec
	Clock     core.Clock
	Log       *slog.Logger
}

type Server struct{ d Deps }

func New(d Deps) (*Server, error) { return &Server{d: d}, nil }

// Handler is the full middleware-wrapped mux (used by tests and Serve). The
// skeleton serves only the health endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

// Serve listens on server.listen (TLS when configured) until ctx is done, then
// shuts down gracefully within 10s.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{Addr: s.d.Config.Current().Server.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ===== internal/webui/new.go =====
// Package webui serves the embedded SPA from dist/ (pre-gzipped). Owner: M11.
package webui

import "net/http"

// Handler serves hashed assets as immutable, index.html as no-store, and falls
// back to index.html for unknown non-/api, non-/v1 paths.
func Handler() http.Handler { return http.NotFoundHandler() }
```

**Shared test kit (`internal/testkit`, implemented by the lead; API below):**

```go
// ===== internal/testkit/fakes/fakes.go =====
// Package fakes holds in-memory implementations of core interfaces for unit tests.
package fakes

import (
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// Clock is a manually advanced core.Clock (use testing/synctest for timers).
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(t time.Time) *Clock        { return &Clock{now: t} }
func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// Auditor records entries; Verify always reports an intact chain.
type Auditor struct {
	mu      sync.Mutex
	Entries []core.AuditEntry
}

// Vault is an in-memory core.Vault with an exact-match Redactor. Seal() makes
// Get/Resolve return core.ErrSealed.
type Vault struct{ core.Vault }

func NewVault(secrets map[string]string) *Vault { return &Vault{} }

// Policy returns Verdicts[tool] (default Default) with Rule "fake".
type Policy struct {
	core.Policy
	Verdicts map[string]core.Verdict
	Default  core.Verdict
}

// Approvals resolves every request immediately with Choice (or blocks until
// Resolve when Manual is true). Records requests. A chat Sender on a team,
// admin-only or multi-approver request becomes Approver.RequesterIdentity
// and Requester (identity and app only), as the policy module records it.
type Approvals struct {
	core.Approvals
	Choice   core.Choice
	Manual   bool
	Requests []core.ApprovalRequest
}

// LLM replays scripted turns: each Step streams Text and/or ToolCalls and
// returns Usage; Err fails the call. Requests records every BuildFunc result.
type LLM struct {
	core.LLM
	Steps    []LLMStep
	Requests []*core.Request
	Info     core.ModelInfo
}

type LLMStep struct {
	Text      []string // streamed as deltas
	ToolCalls []core.ToolCall
	Usage     core.Usage
	Err       error
	Block     chan struct{} // if set, blocks after the first delta until closed or ctx done
}

// Sink records run events.
type Sink struct {
	mu     sync.Mutex
	Events []core.Event
}

func (s *Sink) OnEvent(e core.Event) { s.mu.Lock(); s.Events = append(s.Events, e); s.mu.Unlock() }

// Runner records Submit/Stop/StopAll/StopPrincipal/Resume calls and returns
// Result (or Err) from Submit; Paused/SetPaused toggle a flag (PausedByV: the
// user, or the audit origin's chat actor); ResolveSession/NewSession/Fork
// create sessions in Store (a memstore) when set.
type Runner struct {
	core.Runner
	Store          core.Store
	Result         core.SubmitResult
	Err            error
	Submits        []core.RunRequest
	Stops          []core.SessionID
	Resumed        []core.ApprovalID
	PausedV        bool
	PausedByV      string
	StopPrincipals []core.PrincipalID
}

// Hub records Messenger sends and StreamTo calls; StreamTo returns a *Sink.
// PrincipalChats maps team-owned principals to their chat (a teammate's DM,
// a channel's chat); TeamActiveErr says why one may not run a schedule now.
// Greet and TellApprover record "<channel>:<external id>".
type Hub struct {
	core.ChannelHub
	Sent           []string // "<chat key>|<markdown>"
	Streams        map[string]*Sink
	PrincipalChats map[core.PrincipalID]core.ChatRef
	TeamActiveErr  map[core.PrincipalID]error
}

// Auth resolves identities from Identities (key "<channel>:<external id>"),
// builds Viewers from Viewers, and records Admit and NoticeSender calls; a
// member Admit lets in becomes an admitted teammate (Principal from
// Principals, when set).
type Auth struct {
	core.Auth
	Identities map[string]core.IdentityResolution
	Viewers    map[core.UserID]core.Viewer
	Notices    []core.ChannelIdentity
	Principals map[string]core.PrincipalID
	Admitted   []string // "<channel>:<external id>|<verdict>" per Admit call
}

// Budget allows everything unless Deny is set; StartRun returns a meter that
// enforces only the given RunLimits steps and records AfterCall infos.
type Budget struct {
	core.Budget
	Deny  *core.BudgetError
	Calls []core.CallInfo
}

// Registry is a map-backed core.ToolRegistry with the real filtering rules
// reduced to: trigger, principal kind (SpaceOK), sandbox (Exec), Gated.
type Registry struct {
	core.ToolRegistry
	Tools map[string]core.Tool
}

// Memory, Skills, MCP and Scheduler embed their interface and implement the
// read paths over in-memory maps (files, skill bodies, statuses, schedules).
type Memory struct {
	core.Memory
	Files map[core.PrincipalID]map[core.MemoryFile]string
}

type Skills struct {
	core.Skills
	Metas  []core.SkillMeta
	Bodies map[string]string
}

type MCP struct {
	core.MCP
	StatusV []core.MCPStatus
}

type Scheduler struct {
	core.Scheduler
	Schedules map[core.ScheduleID]*core.Schedule
}

// ===== internal/testkit/memstore/memstore.go =====
// Package memstore is an in-memory core.Store for module unit tests while M1's
// SQLite store is being built. It passes conformance.Store (Viewer scoping =>
// ErrNotFound, ApprovalRepo.Decide CAS, the runs_one_active ErrConflict,
// keyset paging, FTS replaced by case-insensitive substring search, audit
// append-only). Tx runs fn under one mutex.
package memstore

import "github.com/rielicat/bothy/internal/core"

func New() core.Store { return nil }

// ===== internal/testkit/channeltest/channeltest.go =====
// Package channeltest provides a fake core.Channel that records sends/edits and
// lets tests inject inbound messages and button presses.
package channeltest

import "github.com/rielicat/bothy/internal/core"

type Channel struct {
	core.Channel
	NameV  string
	CapsV  core.ChannelCaps
	Sent   []core.OutMessage
	Edited []core.OutMessage
	Acks   []string
}

// New returns a fake with Telegram-like caps unless overridden.
func New(name string) *Channel { return &Channel{NameV: name} }

// Inject delivers an inbound message to the sink passed to Run.
func (c *Channel) Inject(in core.Inbound) {}

// Press delivers a button press for a previously sent message.
func (c *Channel) Press(msg core.MsgRef, senderID, data string) {}

// Checker (checker.go) is a Channel whose platform can say who belongs to
// the team (core.MemberChecker), as Slack and Discord can: Members answers
// by external id (a missing id is an outsider), MemberErr fails every
// lookup, Scope is what MemberScope reports, Gate holds lookups in flight,
// and MemberCalls lists the lookups made.
type Checker struct {
	*Channel
	Members   map[string]core.Membership
	MemberErr error
	Scope     string
	Gate      chan struct{}
}

// ===== internal/testkit/sandboxtest/sandboxtest.go =====
// Package sandboxtest provides fake core.Sandbox and core.Egress.
package sandboxtest

import (
	"context"
	"net/http"

	"github.com/rielicat/bothy/internal/core"
)

type Sandbox struct {
	core.Sandbox
	StatusV core.SandboxStatus
	// Exec handles every exec; default echoes argv to stdout with exit 0.
	ExecFunc func(ctx context.Context, key string, req core.ExecRequest) (*core.ExecResult, error)
	Killed   []string
	Ensured  []core.BoxSpec
}

type Egress struct {
	core.Egress
	Client   *http.Client // returned by PublicClient and SystemClient (point it at an httptest server)
	Activity map[string]bool
	Bound    []core.EgressBinding
	Unbound  []string // tool call ids, in Unbind order
}

// UnboundCalls returns a copy of Unbound.
func (e *Egress) UnboundCalls() []string

// ===== internal/testkit/conformance/conformance.go =====
// Package conformance holds suites every implementation must pass.
package conformance

import (
	"encoding/json"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

type ToolCase struct {
	Name    string
	Env     *core.ToolEnv
	Args    json.RawMessage
	WantErr bool
}

// Tool checks: valid Manifest (non-zero side effect, Title set, schema compiles,
// name ^[a-zA-Z0-9_-]{1,64}$); Plan is deterministic (two calls give identical
// Action JSON and Fingerprint); Plan performs no side effects (env.Sandbox,
// env.HTTP and env.Emit are trapped and must not be called); Action.SideEffect
// >= Manifest.SideEffect; Action.Tool == spec name.
func Tool(t *testing.T, tool core.Tool, cases []ToolCase) { t.Helper() }

// Channel checks an adapter against its fake platform server: Send/Edit/Delete
// round-trip, previews disabled on every send, MaxLen respected, Ack within 3s.
func Channel(t *testing.T, ch core.Channel, platform PlatformFake) { t.Helper() }

// Store runs the repository contract against a fresh store per subtest:
// round trips for every method, the IDOR table (foreign Viewer => ErrNotFound
// on every by-ID read), Decide CAS under 50 goroutines, runs_one_active =>
// ErrConflict, paging order, MarkInterrupted, Approvals.Seen, Grants.Active
// expiry/revocation, audit Append/Range ordering. memstore and M1 both run it.
func Store(t *testing.T, open func(t *testing.T) core.Store) { t.Helper() }

// PlatformFake is implemented by telegramtest, discordtest and slacktest.
type PlatformFake interface {
	InjectText(chatID, senderID, text string, mention bool)
	InjectButton(chatID, msgID, senderID, data string)
	SentTexts(chatID string) []string
	PreviewsDisabled() bool // true when every outgoing message disabled previews/unfurls/embeds
}

// ===== internal/testkit/mockllm/mockllm.go =====
// Package mockllm is a scriptable OpenAI-compatible server (/v1/models,
// /v1/chat/completions and /v1/responses, with and without stream) for agent
// tests and E2E.
package mockllm

import (
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type Rule struct {
	MatchUser       string          `yaml:"match_user"`        // regexp on the last user text
	MatchToolResult string          `yaml:"match_tool_result"` // regexp on the last tool result
	MatchSystem     string          `yaml:"match_system"`      // regexp on the system prompt (e.g. compaction requests)
	Reasoning       string          `yaml:"reasoning"`         // Responses only: a reasoning item with this summary
	Text            []string        `yaml:"text"`              // streamed chunks
	ToolCalls       []core.ToolCall `yaml:"tool_calls"`
	Usage           *core.Usage     `yaml:"usage"`    // default: estimated from bytes
	NoUsage         bool            `yaml:"no_usage"` // omit usage (compat quirk)
	Fault           string          `yaml:"fault"`    // "" | 429 | 500 | overflow | midstream | slow_ttft | malformed | tools_reasoning | tools_responses_only
	Delay           time.Duration   `yaml:"delay"`    // between chunks
	Times           int             `yaml:"times"`    // 0 = unlimited
}

type Server struct{}

func New(rules []Rule, models []core.ModelInfo) *Server { return &Server{} }
func Load(path string) (*Server, error)                 { return &Server{}, nil } // YAML script
func (s *Server) URL() string                           { return "" }             // base URL ending in /v1
func (s *Server) Requests() [][]byte                    { return nil }            // raw request bodies, in order
func (s *Server) Close()                                {}
```

Platform fakes (fake Telegram Bot API server, fake Discord gateway + REST, fake Slack Socket Mode + Web API) are built by the adapter owners in `<adapter>test` subpackages, implement `conformance.PlatformFake`, and are reused by the E2E suite and the RSS budget job.

### 12.2 Modules (12 agents, disjoint ownership)

| # | Module | Owns (exclusive) |
|---|---|---|
| M1 | Store, vault, audit | `internal/store/**` except `migrations/0001_init.sql`; `internal/vault/**`; `internal/audit/**` |
| M2 | Providers | `internal/provider/**`; `tools/catalog-gen/**` |
| M3 | Agent runtime, budget | `internal/agent/**`; `internal/budget/**` |
| M4 | Policy, approvals, tool registry, core builtins | `internal/policy/**`; `internal/tools/**` |
| M5 | Sandbox | `internal/sandbox/**`; `internal/sandboxd/**`; `internal/sbxinit/**`; `deploy/sandbox/**` |
| M6 | Egress, web tools | `internal/egress/**` |
| M7 | MCP, skills | `internal/mcp/**`; `internal/skills/**` |
| M8 | Memory, scheduler | `internal/memory/**`; `internal/scheduler/**` |
| M9 | Channel router, Telegram | `internal/channel/*.go` (package `channel` only); `internal/channel/telegram/**` |
| M10 | Discord, Slack | `internal/channel/discord/**`; `internal/channel/slack/**` |
| M11 | Auth, HTTP API, doctor, web embed | `internal/auth/**`; `internal/httpapi/**` except `dto/`; `internal/doctor/**`; `internal/webui/*.go` |
| M12 | Web UI | `web/**` except `web/src/lib/api/types.gen.ts` |
| F4 (v0.2) | GitHub and repositories | `internal/github/**` |
| knowledge (v0.2) | Knowledge | `internal/knowledge/**`; `internal/testkit/knowledgetest/**` |
| release (v0.2) | Releases | `internal/release/**` (OWNERSHIP.md defaults it to S) |
| S | Lead | everything else (§12.1); v0.2 testkit additions: `memstore.NewGitHub`, `conformance.GitHubStore`, `conformance.Knowledge`, approval-vote and deploy conformance; store migrator tests start from `0001_init.sql` alone and no longer assume a single migration |

Every module: implements its `new.go` signatures; ships `README.md`; `go test -race` of its packages passes standalone with testkit fakes; contributes `Checks()` where listed; no network in unit tests (httptest only); golangci-lint clean; no new dependencies.

---

#### M1 · Store, vault, audit

- **Implements:** `core.Store` and all 24 repositories in hand-written SQL (writer `Tx` with `BEGIN IMMEDIATE`, two readers); Viewer scoping on every by-ID read; FTS search with the §6 query and safe MATCH quoting; `Backup` (tarball with a VACUUM INTO snapshot, 0600; §4.8); `RunRetention`; `vault.NewSlogHandler`; `Checks` (`sqlite.version`, `data.permissions` with fix, `backups.none_recent`). `vault.Redactor` (exact + encoded variants + patterns), `vault.Vault` (KEK sources and sealing, DEK wrap, AES-256-GCM with AAD, env overlay `BOTHY_SECRET_*[_FILE]`, `Resolve("secret:x")` with header-embedded refs, `RotateKEK`, `Unseal`, `secret.use` audit, `Checks`: `vault.kek_colocated`, `vault.kek_env`, `vault.sealed`). `audit.Writer` (canonical JSON, chain, sync vs batched actions, `Verify`) and `audit.Archive(ctx, dbPath, before)` for the offline CLI.
- **Consumes:** core, config.
- **Acceptance tests:**
  - `conformance.Store` passes against a temp DB (the same suite memstore passes); every repository method round-trips.
  - **IDOR suite:** for every by-ID method, a Viewer without the principal gets `ErrNotFound` (table-driven over all repos).
  - FTS: finds a message; stays correct after `VACUUM` and after restoring a `VACUUM INTO` copy; never returns hidden or foreign sessions; hostile MATCH input (`"`, `NEAR(`, `*`) does not error.
  - `runs_one_active` → `ErrConflict`; approval `Decide` CAS: 50 concurrent deciders, exactly one wins.
  - Audit: UPDATE/DELETE abort; a tampered row makes `Verify` return its id; archive + anchor keeps `Verify` green.
  - Migrator: refuses SQLite < 3.53.4 (injected version func); writes the pre-migration backup; idempotent.
  - 200 concurrent writers: no `SQLITE_BUSY`; 1K message appends < 1 s; 10K-message search < 50 ms.
  - Vault: round trip; wrong KEK → sealed with `kek_check` failure; ciphertext moved to another name or version fails (AAD); rotation keeps every secret readable; values never appear in a captured slog stream; redactor masks raw, base64, base64url, hex and percent-encoded forms inside JSON; files 0600.

#### M2 · Providers

- **Implements:** `provider.Facade` (`core.LLM`): chain resolution, retries/backoff/`Retry-After`, circuit breaker, no-switch-after-first-event, `Passthrough`, estimator + EWMA calibration, catalog (embedded `catalog.json` with long-context tiers + `pricing` overrides, whose own `tiers` replace the catalog's and which otherwise keep them), `Probe` (1-token completion + forced tool call through the selected OpenAI API, sent together for a hosted endpoint and one after the other for a local one; a model whose provider refuses the forced `tool_choice` is asked again unforced, and later probes of that model skip forcing, reported in `ProbeResult.API`, context discovery, cost), `Detect` (Ollama at `host.docker.internal:11434`/`localhost:11434`/`ollama:11434`, LM Studio at `localhost:1234` and `host.docker.internal:1234`, vLLM at `localhost:8000` and `host.docker.internal:8000`, well-known env var names), `Health`, `Checks` (`providers.auth_failed`, `providers.ollama_context`, `providers.chat_tools`, v0.2 `providers.bedrock_credentials`). v0.2 catalog: `catalog.Model.geo_price` and a `bedrock` provider generated from `tools/catalog-gen/bedrock.json`, matched by kind; cross-region profile ids and ARNs map to base model ids; `global.` ids use `price`, other profiles and in-region use `geo_price`; Claude windows and capabilities come from the Anthropic entries. Clients: `anthropic` (Messages SSE, tools, thinking + signature round-trip, `cache_control` from `CacheBreak`/`ToolsCache`/`CacheTail`, usage with cache read/write), `openai` (two clients chosen by `providers[].api`, `auto` being Responses for `openai` and Chat Completions for `openai_compat`. Responses: `POST /responses` with `store: false`, the encrypted-reasoning round trip as thinking parts with SigKind `openai_responses` scoped to the provider id, typed SSE events keyed by output index, usage with cached, cache-write and reasoning tokens, `response.incomplete`/`response.failed`/`error` mapping, the encrypted-content and temperature resends of §5.14 (no `temperature` for models the catalog marks as reasoning, never `top_p`), `prompt_cache_key` from `Request.CacheKey`. Chat Completions SSE for OpenAI and every `openai_compat` base: the reasoning-off fallback with `EffortMemory` (§5.14), `stream_options.include_usage`, `max_tokens` vs `max_completion_tokens`, tool-call deltas, missing usage → estimate, missing call ids → `call_<n>`, whole-argument tool calls, `reasoning_content` (sent back to the same provider only, for DeepSeek thinking mode), `reasoning_tokens` reported beside `completion_tokens` (xAI) counted as Output, OpenAI's billing codes (§5.14) → `auth`, `<tool_call>{…}</tool_call>` text extraction; anthropic maps an `invalid_request_error` about the credit balance (and `billing_error`) to `auth`), `gemini` (`streamGenerateContent?alt=sse`, key in `x-goog-api-key` header, functionCall, thoughtSignature; `skip_thought_signature_validator` on unsigned foreign calls in the current turn; `MALFORMED_FUNCTION_CALL`/`UNEXPECTED_TOOL_CALL` map to a retryable server error; 402 → `auth`), `ollama` (`/api/chat` NDJSON with `options.num_ctx`, `/api/tags`, `/api/show` for context length). All clients use `Egress.SystemClient`, never retry (except the two capability resends of §5.14), never read env, keep keys in headers only. `tools/catalog-gen`: reads catwalk's provider JSON; a cache-read price above the input price is invalid and is dropped (or the `in_cached` read price is used instead); long-context tiers come from the embedded `tools/catalog-gen/tiers.json` overlay (provider id, model-id regexp, tiers, source and date), since catwalk has none, covering Gemini Pro, xAI, OpenAI's 1M-window models and their OpenRouter mirrors (`TestCommittedCatalogMatchesOverlay` keeps `catalog.json` and the overlay in step).
- **Consumes:** config, `core.Vault`, `core.Egress`.
- **Acceptance tests:**
  - Golden SSE fixtures per provider, **copied from vendor documentation and official SDK test data** (with attribution in `testdata/README.md`): text, parallel tool calls, thinking/signature, usage incl. cache, every `ErrorKind` mapping, a mid-stream error.
  - Fallback on 429/500 before the first event; no fallback after it; `build` called with each candidate's `ModelInfo`; `context_length` surfaced, not retried by the facade; breaker opens after 3 failures.
  - Cost math exact to the micro (tiers included); unknown price → `PriceKnown=false`.
  - A recorder asserts API keys never appear in URLs or logs; clients have `Proxy == nil`, TLS ≥ 1.2; a CI grep confirms no provider SDK import.
  - Ollama: `num_ctx` sent equals the configured context, else min(trained, max(16384, served context from /api/ps)); Probe sends that `num_ctx`, explains the cap, and reports "4,096 context" with the `OLLAMA_CONTEXT_LENGTH` hint when the served context is small. No extra config key: `providers[].models[].context` lifts the cap.
  - Thinking parts with a `SigKind` other than the target client's are stripped; same-role messages merged.
  - OpenAI Responses: golden fixtures (text, function calls incl. parallel, encrypted reasoning round trip, usage with cached and reasoning tokens, incomplete, failed, error events); the request prefix, `prompt_cache_key` included, is byte-stable across turns; reasoning and item ids replay only to the same SigKind and provider; reasoning that nothing in its reply follows is left out; the encrypted-content resends happen once, are remembered, and do not happen for unrelated 400s or after the first event.
  - Chat Completions reasoning fallback: one resend with `reasoning_effort: "none"`, the outcome cached per (provider, model), no resend after the first event, none for unrelated 400s.

#### M3 · Agent runtime, budget

- **Implements:** v0.2 adds `internal/agent/refine` (engine, answer normalization, embedded generator/critic/corrector prompts; §14.2), the runner's refine hooks and `Deps.TurnContext` hooks (§5.6). `agent.Runner` (`core.Runner`): session workers, coalescing, steer and queue modes, semaphores released during approval waits, run loop (§5.5), context assembly and profiles (§5.6), tool pipeline (§5.7), park/resume (placeholder result at park, `<approved-action>` message at resume, resume sinks via `SetHub`), history invariants (synthesized results for dangling calls), compaction (§5.10), stop/kill switch (§5.11), boot recovery (interrupted runs: cancel approvals, notify chats), graceful-shutdown notices (§5.15), `Fork`, `Continue`, titles, workspace directory creation and workspace provenance (§5.3), URL provenance for user messages, link neutralization of the sink's copy through the core neutralizer (`core.NewLinkFilter`, §4.15), the embedded `CONSTITUTION.md` (full and short variants, versioned in `meta.constitution_version`, exposed by `Constitution()`) and the default `SOUL.md` written at boot when missing. `budget.Service` (`core.Budget`): meters, daily caps with per-user overrides, sender token buckets (LRU 10k), loop detector, defaults per trigger.
- **Consumes:** every core service interface (via testkit fakes and `memstore`).
- **Acceptance tests** (memstore, fakes.LLM, sandboxtest, fakes.Policy, fakes.Approvals, fakes.Hub; `testing/synctest` for time):
  - A session with a parked call and a dangling (interrupted) call still produces provider-valid requests (every tool call has one result); a resumed parked run executes once and its result arrives as an `<approved-action>` message.
  - **Prefix stability:** tools + S1 + S2 bytes identical across 5 turns including a memory write and a new pin (they appear in turn-context); `prefix_hash` changes only after compaction or when standing rules or team memory change (S1), which is not reported as a cache-stability bug.
  - Constitution present after 10 forced compactions; a pinned "don't act until I say" survives verbatim; a standing instruction in history is auto-pinned and announced.
  - Summarizer failing twice → truncation fallback with notice; run completes. A 16K-window model: every assembled request ≤ window − reserve; breakdown sums equal the total.
  - `/stop` while the LLM is blocked mid-stream and while a tool sleeps: `Stop` returns < 100 ms; `Sandbox.Kill` called; run `stopped`.
  - 50 concurrent Submits to one session → exactly one active run; overflow → `ErrBusy`; steer message injected at the next step; queue mode coalesces.
  - With global semaphore 1: run A waiting for approval does not block run B.
  - Park → new Runner on the same DB → resolve → Resume executes the stored Action exactly once.
  - Every budget path ends with the right status; loop warn then abort; exposed session forces the sink to neutralize links.
  - Tool output > cap is truncated head/tail with the spill file written and `FullRef` set; untrusted results are spotlight-wrapped with unique ids.

#### M4 · Policy, approvals, tool registry, core builtins

- **Implements:** `policy.Engine` (`core.Policy`: §5.8 procedure, presets, rules with globs/argv0 (command-line first word, metacharacter guard, the `find`/`rg` write and exec flags refused for relaxing rules)/domains/paths/JSON-pointer matchers, grants, first contact (grants, `egress.allow`, `Approvals().Seen`, origin chat), floors, `NarrowGrant`, `NextTaint`), `policy.Approvals` (`core.Approvals`: persistence, card rendering (§5.9), `Await` with timeouts, park, CAS `Resolve` with authority/message/truncation checks, `ButtonData` MAC with `meta.approval_mac_key` created on first boot, `AddMessageRef`, sweeper `Run`, `OnResolved`, `RenderCard`, `Recent`, admin-broadcast copies for `AdminOnly`). `tools.Registry` (`core.ToolRegistry`: manifest/schema validation, deterministic `ForRun` per profile honouring `core.Gated`, `Available`, `Search` (name/description keyword ranking), the `tool_search` tool (uses `env.Selection`) and the `use_tool` schema entry, which the runner unwraps). Builtins: `fs_read` (ranges, 256 KB cap, binary detection), `fs_write` (Action with unified diff; create/overwrite/append), `fs_list`, `fs_delete` (irreversible); all four open workspace paths with `O_NONBLOCK` and check the type on the open fd, since box processes keep running between Plan and Exec and a checked path can become a FIFO; `message` (origin chat only, `SELocalWrite`, `Messaging`). `Checks`: `policy.hands_off`, `policy.auto_external_rules`, `budgets.unlimited`.
- **Consumes:** config, `core.Store`, `core.Bus`, `core.Auditor`, `core.Messenger`, `core.Sandbox` (quota check only).
- **Acceptance tests:**
  - Decision table ≥ 150 cases over (class × preset × trigger × principal kind × taint × provenance × rules × grants); no rule or grant beats a floor; the `AllowWhenTainted` exception works only for exact recipients.
  - `NarrowGrant` never yields a whole-tool grant; `bash -c` grants match only the exact argv; an `argv0: [ls]` rule matches `ls -la` but not `ls; rm -rf x`, `ls $(curl …)` or `ls | sh`.
  - A shell command that deletes files (`rm x && ls`, `find . -delete`, `xargs rm`, `bash -c 'rm x'`, `'rm' x`) is decided as `irreversible` in every preset, with no `allow_always` and a "This can't be undone." card; `rm` as an argument or in quotes (`grep -rn rm .`, `echo "rm x"`) and a here-document written to a file are not.
  - First contact: a recipient approved once (allowed approval) is not first contact again; the run's own chat never is.
  - Approvals: concurrent double resolve → one wins; a press from an unrecorded message, a non-approver, or "approve" on a truncated card is rejected; MAC tamper rejected; button data ≤ 64 bytes; expiry → deny or park per decision; park TTL expiry.
  - Card fuzz: Action fields with markdown, HTML, U+202E, zero-width chars and fake button text never change card structure; bidi/zero-width visualised; punycode shown.
  - Registry: identical `ForRun` bytes for identical inputs; core schemas ≤ 1,500 estimated tokens (standard) and the compact/degraded sets match §5.6; exec tools absent without sandbox; `schedule_*` absent for schedule triggers; restricted spaces get read-only tools only.
  - fs tools: `os.Root` blocks `../`, absolute paths and symlink escapes; every builtin passes `conformance.Tool`.

#### M5 · Sandbox

- **Implements:** `sandbox.Client` (`core.Sandbox`): backend probe (sandboxd → bwrap → none), sandboxd client (`proto`), directory pre-creation in `Ensure` (`ws/<id>` and MCP trees 2770 g10001, `skills/`, `egress/<h>` 0750 g10001), `Usage` from the quota scan (every backend), `Destroy` → `Egress.CloseBox`, health watch and `Watch` callbacks (`Run` polls only sandboxd's health; an auto-mode `none` backend only watches for the sandboxd socket; `none` and `bwrap` are probed once, at start), `no_sandbox` / `sandbox_not_isolated` banners (an admin broadcast plus one per member user principal, since members get `banner` rather than `sandbox.status`: synced on every status change and once a minute for users created or promoted since, without reading the users table while no banner is shown; members promoted to admin, disabled or deleted lose their per-principal banner), idle reaping, quota scan, `Checks` (`sandbox.unavailable`, `sandbox.not_isolated`, `sandbox.rootful_runc`, `sandbox.bind_mounts`, `sandbox.runsc_host_uds`, `docker.socket_access`); `internal/sandbox/bwrap` (§2.12); tools `shell` (`bash -lc` via `ExecRequest`, `local_write`, `Exec`) and `python` (writes the script to `.bothy/tmp/` then runs `python3`). `internal/sandboxd`: server (`proto`, `DisallowUnknownFields`, regex validation, peercred check), hand-written Engine API client (ping, version, info, image inspect/pull by digest, container create/start/kill/remove/list, exec create/start with hijack and stream demux, exec inspect), compiled spec (§2.6), reaper, max-boxes LRU, runtime selection and UDS canary (§2.9), self-test (§2.10), socket onboarding messages. `internal/sbxinit` (§2.7). `deploy/sandbox/` images.
- **Consumes:** config, `core.Egress`, `core.Store`, `core.Bus`, `core.Auditor`.
- **Acceptance tests:**
  - Unit: spec test asserts every hardening field of §2.6; unknown JSON fields, bad key/workspace/tree ids, `..` in cwd, forbidden env keys → 400; limits clamped; frame decoding fuzzed.
  - Linux CI job with Docker 27 (`-tags docker`): self-test passes; `curl https://example.com` fails without a grant and succeeds with one; `getent hosts example.com` fails; `curl http://169.254.169.254` fails; a double-forked `setsid sleep 600` is gone < 1 s after Kill; an exec timeout returns `timed_out` and exit 124; a fork bomb is contained by pids; `/run/bothy` and `docker.sock` are not visible in the box; uid is 10001; files written by the box are readable by the gateway (group 10001) and vice versa; a second session's workspace is invisible.
  - runsc job (`-tags runsc`, daemon.json with `--host-uds=open`): egress works; without the flag `auto` falls back to runc and `runsc` mode refuses.
  - bwrap job: environment is clean (no `BOTHY_*`), network only via relay, `ptrace` denied.
  - `shell` and `python` pass `conformance.Tool`.

#### M6 · Egress, web tools

- **Implements:** `egress.Service` (`core.Egress`): per-box unix listeners, CONNECT + absolute-form proxy (§2.8 steps 1–9), hold-for-approval (`Policy.Decide` on the `net_allow` spec + `core.Approvals`, §5.8 "Egress holds", with the binding's provenance; allow_once scoped to the bound tool call and ended by `Unbind`), trifecta grant suspension (reads session taint), pinned resolver, blocklist, SNI check, caps, batched `egress_log`, `TakeActivity`, `PublicClient` (redirect re-checks, no cookies/auth headers), `SystemClient`. Tools: `web_fetch` (GET only, HTML → text via `x/net/html`, `web_fetch.max_bytes`, Taint Untrusted, Action with full URL and punycode host), `web_search` (SearXNG JSON, Brave, Tavily via `SystemClient`, SearXNG with `AllowPrivate` from `search.searxng_allow_private`; records result URLs in `session_urls`; Taint Untrusted), `net_allow` (Action `{kind: net, host, port}`, `external_send`, F9). `Checks`: `egress.wildcards`, `egress.shared_tenant_allowed`, `egress.presets_enabled`, `mcp.http_plaintext`.
- **Consumes:** config, `core.Store`, `core.Vault`, `core.Approvals`, `core.Policy`, `core.Bus`, `core.Auditor`.
- **Acceptance tests:** SSRF table: `127.0.0.1`, `0x7f000001`, `2130706433`, `017700000001`, `[::1]`, `[::ffff:127.0.0.1]`, `[::ffff:169.254.169.254]`, `64:ff9b::a9fe:a9fe`, `2002:a9fe:a9fe::`, `2001:0:…` Teredo, `100.64.0.1`, `198.18.0.1`; DNS rebinding (fake resolver answers public then private) dials the pinned IP; redirect to a private address refused; CONNECT to port 22 refused; IP literal without grant refused; SNI ≠ CONNECT host closed; shared-tenant host not allowed by a session grant; Exposed session ignores session grants; over-cap upload aborted; hold-for-approval allows on approve, 403 on timeout and immediately when Exposed or non-interactive; every connection logged; `web_fetch` sends no `Authorization`/`Cookie`; IDN homograph shown as punycode in the Action; tools pass `conformance.Tool`.

#### M7 · MCP, skills

- **Implements:** `mcp.Manager` (`core.MCP`): config → servers; Streamable HTTP via go-sdk with `Egress.SystemClient` and vault-injected headers, one session per (server, billing user), legacy SSE fallback; stdio via `Sandbox.Stdio` + `IOTransport`; discovery instances for tool lists (§2.11); install flow (§2.11) with TOFU locks and tree hashing; per-(server, principal) stdio instances with idle stop and backoff; `Approve` with `Disable`; tool-name truncation rule; canonical hashing and pinning (pending/approved/changed→disabled; a changed definition is suggested with the class approved for the earlier one; a new definition of a turned-off tool is turned off too, never offered for review as new; removing a server from the config deletes its pins, so one added again under that name is reviewed afresh), `mcp.pin_pending` events; tool adapters `mcp__<server>__<tool>` (lazy unless pinned to core by config; Action from `field_map`; class from approval; Taint Untrusted unless `trusted_output`, Private when approved private); cancellation; `Checks` (`mcp.pending_pins`, `mcp.drifted` with fix). `skills.Library` (`core.Skills`): scan `<work>/skills/*/SKILL.md`, YAML frontmatter (`name`, `description`, optional `license`, `allowed-tools` advisory), tree hash, status machine, index within budget, `Load`/`ReadFile` via `os.Root` (256 KB), zip install (zip-slip and size limits, never executes anything), rescan drift → `changed` (banner `skills_changed`, audit `skill.changed`); tools `skill_load` (`core.Gated`: only when a skill is enabled), `skill_read`; `Checks`: `skills.changed` (fix: disable).
- **Consumes:** config, `core.Store`, `core.Sandbox`, `core.Egress`, `core.ToolRegistry`, `core.Vault`, `core.Bus`, `core.Auditor`.
- **Acceptance tests:** in-process go-sdk servers (old and new spec era) over pipes and httptest: new tool → pending, not exposed; approve → callable; changed description → disabled + event + diff; unpinned package / `npx` command rejected at config validation; stdio refused without sandbox; HTTP MCP to a private IP refused without `allow_private`; header secret resolved and never logged; cancellation propagates; tree drift → `drifted`. Skills: oversize index degrades to names; `../` and symlink reads blocked; zip-slip blocked; a skill whose SKILL.md says "run this install command" causes zero `Sandbox` calls; hash drift → excluded and banner.

#### M8 · Memory, scheduler

- **Implements:** `memory.Service` (`core.Memory`): files under `<data>/memory/<principal>/` (0600, atomic temp+rename), ops with byte caps and helpful errors, `memory_events`, ETags, `DeltasSince` text, history, `Search` (delegates to `Messages().Search`); tools `memory` (Action kind `memory`, provenance recorded; F7) and `session_search` (Taint Private, + Untrusted on untrusted hits). Spaces have no memory: neither tool is `SpaceOK`. Team memory (TEAM.md, `ReadTeam`/`WriteTeam`/`TeamHistory`, the tool's admin-class `file: "TEAM"`): §14.9. `scheduler.Service` (`core.Scheduler`): single timer on `NextDue`, gronx with timezones and DST, heartbeat (owner schedule kept in sync with config, in the owner's time zone, HEARTBEAT.md inlined into the prompt, empty file = no call, jitter, active hours, `HEARTBEAT_OK` suppression, daily cap from `UsageRepo.Report`), once, missed-run policy, auto-disable after 3 failures, fires nothing while `Runner.Paused()` (the kill switch; `SetPaused` is an independent scheduler-only pause), `Preview` (+ human description), delivery via `Hub.StreamTo(…, "final")`; tools `schedule_list` (read), `schedule_create`, `schedule_delete` (admin class, `Triggers: [message]`).
- **Consumes:** config, `core.Store`, `core.Runner`, `core.ChannelHub`, `core.Memory`, `core.Bus`, `core.Auditor` (memstore, fakes.Runner, fakes.Hub in tests).
- **Acceptance tests:** cap enforcement with remaining-bytes message; concurrent `Apply` serialized; ETag conflict → `ErrConflict`; delta text after add/replace/remove; DST spring-forward and fall-back (Europe/London March/October) next-run correctness; `once` missed-run fires exactly once; paused scheduler fires nothing and resumes; heartbeat output `HEARTBEAT_OK` is not delivered; 3 failures disable and notify; tools pass `conformance.Tool`.

#### M9 · Channel router, Telegram

- **Implements:** `channel.Hub` (`core.ChannelHub`): adapter lifecycle, §5.1 pipeline (dedup, identity, unknown-sender flow, rate limits, command fast path, button handling, space gating, provenance, session resolution, attachment download into the workspace, Submit), streamer (`StreamTo`: typing renewal and placeholder, buffer deltas, edit no faster than `EditMinInterval`, split at `MaxLen` on paragraph/line boundaries without breaking code fences, status lines in the words of the web's activity lines (ui.md §7.4: "Running `pip install pandas`…", "Deleting `notes/a.md`…", "Searching the web for “pandas”…", else the tool's title; identifiers in code spans; links in a query neutralized like the reply's) and "Refining the answer…" while a refine-mode answer is being refined (`RefineStatusDetail`, cleared by the answer's first delta), and none while a step waits for approval (its card or waiting line in the chat says so), Stop button `s:<run>`, final edit, link neutralization when Exposed through the core neutralizer (`core.NeutralizeMarkdown`/`NeutralizeLinks`, §4.15)), approval card delivery and updates (§5.9 routing, link-only cards, recorded waiting lines, truncation, `AddMessage`), `Messenger.Send`, `PrincipalChat`, `TeamActive`, `Greet`, `TellApprover`, membership checks (§5.1 step 3: `MemberChecker`, freshness windows, scope resets, the 15-minute sweep, Discord server backfill), the bot's picture (§5.1: first connection, `ApplyPicture`), `Test`, `Checks` (`channels.auth_failed`). Telegram adapter: `getUpdates` long poll (offset in meta), `Inbound.Forwarded` from `forward_origin`, `forward_date` or `via_bot`, `sendMessage`/`editMessageText` with HTML parse mode and escaping (a code span inside bold or italics closes them and reopens them after it, since Telegram nests no code in them), `link_preview_options.is_disabled`, inline keyboards, `answerCallbackQuery`, `sendChatAction`, `getFile`, `setMyCommands`, `getMe`, `getChat`/`getChatMemberCount` (`InspectChat`), `getUserProfilePhotos`/`setMyProfilePhoto` (`PictureSetter`: a static JPEG uploaded as multipart, never retried), 429 `retry_after`. `telegramtest` fake Bot API server.
- **Consumes:** config, `core.Auth`, `core.Runner`, `core.Approvals`, `core.Budget`, `core.Sandbox` (status), `core.Store`, `core.Bus`, `core.Auditor`, `core.Redactor`, `core.AgentPicture`; adapters get `core.AdapterDeps` (memstore, fakes.Auth, fakes.Runner, fakes.Budget in tests).
- **Acceptance tests** (channeltest + telegramtest, synctest for timing; the hub harness uses the real `auth.Service` over memstore): a member's DM makes them a teammate, greeted once; someone waiting gets one line per 24 h and **zero** Runner submissions, approvers a DM (≤ 3 an hour); an outsider gets one line a day; blocked sender silent; group message without mention ignored; an off channel ignored; a lookup failure falls back to a 4-hour verdict; `/stop` handled while the session worker or a membership lookup is busy; a non-approver's or departed approver's button rejected with a toast; a channel's card goes to chat approvers' DMs and the channel gets a waiting line its requester can cancel; truncated card offers only Deny + Open in web; ≤ 1 edit per interval; a 10K-char reply splits correctly; HTML injection escaped; previews disabled on every send; redelivered update deduplicated; Telegram adapter passes `conformance.Channel`.

#### M10 · Discord, Slack

- **Implements:** Discord adapter on disgo: gateway with intents `GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT`, caches disabled, resume handled by disgo, REST create/edit/delete with `flags: SUPPRESS_EMBEDS`, components (buttons) and interaction responses (deferred update ≤ 3 s), typing, DM channel open, per-route 429 handling, mention detection, invite URL builder, the bot's avatar (`PictureSetter`: `GET`/`PATCH /users/@me`, the PATCH outside disgo's limiter; 429 and `AVATAR_RATE_LIMIT` are `rate_limited`). Slack adapter on slack-go `socketmode`: `apps.connections.open`, envelope ack ≤ 3 s, `app_mention` + `message.im`, threads as `ThreadID`, `block_actions` → `ActionEvent`, `chat.postMessage`/`chat.update` with Block Kit buttons and `unfurl_links/unfurl_media: false`, mrkdwn escaping, `/bothy` slash command, `auth.test`. (The Slack app manifest and Discord invite URL are generated by M11 from the templates in §8.6.) `discordtest` and `slacktest` fakes. Each adapter talks to its library through a package-private interface so unit tests can fake it even where the library resists URL overrides.
- **Consumes:** config, `core.AdapterDeps`.
- **Acceptance tests:** fake gateway: reconnect/resume after a forced close; interaction acked < 3 s; mention vs non-mention; embeds suppressed. Slack fake: envelope ack timing; reconnect on `disconnect`; button → `ActionEvent` with `AckRef`; unfurls disabled; the §8.6 manifest's scopes are sufficient for every call the adapter makes (test asserts the set). Both pass `conformance.Channel`; idle allocation benchmark ≤ 3 MB per adapter.

#### M11 · Auth, HTTP API, doctor, web embed

- **Implements:** `auth.Service` (`core.Auth`: setup token lifecycle, claim grant, owner creation, Argon2id with one slot and dummy hash, lockout, cookies and rotation, CSRF tokens, PAT format with CRC, scopes, `Admit` (membership verdicts; teammates and their person spaces), `NoticeSender` (24 h), `ResolveIdentity`, `ViewerFor` (sets `Viewer.Admin`), `ResetPassword`, the embedded common-password list; outside `core.Auth`: `SetTeammate` and `ForgetTeammate`, `Checks`: `server.exposure`, `server.insecure_lan`, `tokens.*`). `httpapi.Server`: middleware (§8.1), every route in §8.3, SSE (§8.2, hello with `Bus.Banners`), OpenAI endpoints (§8.5) with request/response translation for passthrough, Slack manifest and Discord invite (§8.6, golden tests), uploads and workspace file download under `Deps.WorkDir`, persona file under `Deps.DataDir`, vault status/import, policy preview, per-token rate limiter, safe-mode route gating, restart via `Deps.Restart`. `doctor.Runner`, `doctor.ConfigChecks` + `Print`. `webui.Handler` (only the `.gz` variant of compressible assets is embedded, `index.html` stays raw; served with `Content-Encoding: gzip`, unzipped on the fly, bounded by the recorded size, for clients without gzip; ETag, immutable caching, SPA fallback).
- **Consumes:** every core interface, `config.Admin`, `dto` (memstore and the testkit fakes for every service in tests).
- **Acceptance tests:** route authz matrix (anonymous, member, admin, each token scope) for every route, including `POST /api/approvals/{id}` → 403 for every bearer token and safe mode → 503 outside the allowed routes; IDOR suite through HTTP (foreign session/run/approval/schedule/memory ids → 404); setup routes 404 after completion and 403 from non-local peers; setup token single use; login timing equal for unknown users (±10%); lockout; cookie flags under TLS and plain local HTTP; missing CSRF header → 403; cross-site `Sec-Fetch-Site` → 403; query-string token → 400; wrong Host → 421 (health probes → 200); public peer over plain HTTP → 403, LAN peer → 403 unless `insecure_lan`; `X-Forwarded-For` honoured only from trusted proxies, and a loopback, IPv4-mapped or `local_cidrs` address in `X-Forwarded-For` or `Forwarded` from a trusted proxy never opens setup or plain HTTP; CSP and nosniff on every response; no CORS header ever; SSE: resume without gaps, `resync` across boots, member never receives another principal's events, admin never receives others' `run.delta`; `/v1/chat/completions` stream parsed by a recorded official-client fixture; `tools` → 400 in agent mode; passthrough tool round-trip; budget → 429 OpenAI shape.

#### M12 · Web UI

- **Implements:** every screen in `docs/design/ui.md` (§9); router; API client; SSE store with resync; markdown renderer; card component; SVG charts; QR encoder; setup; responsive layout; PWA manifest.
- **Consumes:** `types.gen.ts`, §8; develops against a `web/mock/` dev server that replays fixtures.
- **Acceptance tests:** vitest: renderer XSS corpus (no executable output, no `<img>`, `javascript:` links inert, neutralization in Exposed sessions); card renders only DTO fields and visualises bidi; SSE store resync behaviour; router. Build gates: first-screen JS ≤ 120 KB gz and CSS ≤ 20 KB gz, all JS chunks ≤ 280 KB gz, no inline scripts or style attributes in the built HTML, zero external origins. Keyboard-only approval flow; works at 360 px; axe checks on chat, approvals and settings.

### 12.3 Rules for parallel agents

- Code only against `internal/core`, `internal/config` and testkit. Never import another module's package (CI).
- Touch only owned paths (CI ownership check). `go.mod`, `core`, `config`, `events`, `dto`, migrations, testkit, `cmd/` and `deploy/` (except `deploy/sandbox/`) change only through the lead.
- **Interface change request (ICR):** open an issue titled `ICR: <package>.<symbol>` with the proposed diff and rationale; meanwhile code against a local adapter in your own package. The lead batches approved ICRs daily into a skeleton patch that every worktree rebases onto.
- Schema changes: request `0002_*.sql` from the lead; never edit `0001`.
- New dependency: request from the lead (14-day cooldown applies).
- Comments: only the non-obvious why; match file density (CONTRIBUTING).

### 12.4 Phase 3: merge, integrate, E2E (lead)

**Merge order** (each merge keeps `make test` green; stubs remain until replaced): M1 → M2 → M4 → M6 → M5 → M8 → M7 → M3 → M9 → M10 → M11 → M12. Then the lead fills the CLI commands (`doctor`, `security-audit`, `setup-token`, `user`, `vault`, `config`, `audit`, `backup`) on top of module APIs.

**E2E suite** (`e2e/`, real binary, mockllm as an `openai_compat` provider, fake platform servers wired in through the locked `channels.*.api_base` / `gateway_url` keys set by env; a `docker`-tagged variant runs real sandboxd):

- **`make e2e`** (`go test -tags e2e ./e2e/...`) runs the native binary with no Docker. Scenarios that need exec tools without Docker (E16) use a **fake sandboxd** that speaks the §2.5 `/v1` protocol on a short unix socket, so the gateway offers `shell`; it records calls and never executes. e2e may not import `internal/sandboxd/proto` (or a SQLite driver or the MCP SDK; `scripts/check-imports.sh`), so such wire structs, the hand-written MCP server and similar test doubles are kept locally in `e2e/`.
- **Docker-tagged** (`go test -tags 'e2e docker' ./e2e/`, e.g. `-run TestDockerSandbox` or `-run 'TestVaultCanarySandbox|TestPrincipalIsolationSandbox'`): the real gateway and sandboxd run in containers built from the checkout (the gateway image laid out as in §2.3, the box image from `deploy/sandbox/Dockerfile --target base`, passed to sandboxd by image id in `BOTHY_SBX_IMAGES`), sharing the work and run volumes as compose does. This also works on Docker Desktop, where the gateway cannot run on the host because sandboxd's unix socket lives in a VM volume; the test drives the gateway over a published 127.0.0.1 port and the gateway reaches the in-process mockllm through `host.docker.internal` (on Linux, a relay on the stack network's gateway address). `BOTHY_E2E_DOCKER_PREFIX` names and labels (`bothy.test=<prefix>`) every container, volume, network and image, all removed afterwards (boxes are found by their `bothy.run_volume` label); `BOTHY_E2E_KEEP_IMAGES=1` keeps the images; `BOTHY_E2E_NET=0` skips E12, which needs outbound `example.com:443` from the gateway container.
- **`make e2e-docker`** (`scripts/e2e-docker.sh`) builds the gateway and both sandbox images under the prefix (`BOTHY_E2E_IMAGE` / `BOTHY_E2E_SANDBOX_IMAGES` test existing ones instead; `KEEP_IMAGES=1` keeps them) and runs every docker-tagged test, including the real compose deployment (`TestComposeShell`, `TestComposeSocketOnboarding`, and `TestComposeIdleRSS` when `BOTHY_RSS_WAIT` is set) through `deploy/compose.yaml` plus `e2e/docker/compose.test.yaml`.

| # | Scenario | Pass condition |
|---|---|---|
| E1 | Zero-config first run | setup fragment token → owner → provider test against mockllm → first chat streams over SSE with usage and breakdown |
| E2 | Shell approval (docker) | rule makes `shell` ask → card over SSE → approve → box exec → result → final answer |
| E3 | Deny and park | interactive deny path; scheduled run parks → approve via web → resumes and executes once (non-Docker form: a rule makes `fs_write` ask; docker form: `shell`) |
| E4 | Telegram | an admin lets the person in (Can approve) → hello → DM → streamed edits → approval button in the same DM → result; previews disabled |
| E5 | Group space | owner enables a group; untrusted member message → read-only tools only; with a policy rule that makes a read tool ask (a restricted space otherwise never raises a card: read tools are auto-allowed and F4 denies anything above read), the card goes to the owner DM only, never to the group |
| E6 | Unknown sender | DM from an unknown id → one reply, zero mockllm requests |
| E7 | Stop | `/stop` during `sleep 600` (docker) and a hung stream (no Docker needed): stopped < 1 s, zero box processes left |
| E8 | Budget and loop | daily cap refuses runs; looping script aborts with `loop_detected` |
| E9 | Compaction | 16K mock model, 12 forced compactions (`/compact`, i.e. `POST /api/sessions/{id}/compact`, each call folding one more turn): constitution and pinned instruction present in mockllm requests; auto-pin announced |
| E10 | Memory | memory write appears in the next request's turn-context; tools+system prefix bytes unchanged |
| E10b | Team memory (`e2e/team_memory_test.go`) | the owner's "remember for the whole team" makes an admin-only card ("Remember this for everyone?", Note, consequence) and TEAM.md stays empty until it is approved; then TEAM.md holds the note, `memory.write` is audited with `scope: team` and the approval, and a Telegram teammate's prompt carries `<team-memory>`; the teammate's own proposal sends the chat approver a link card only and their DM the needs-an-admin line, and an admin's deny leaves TEAM.md unchanged |
| E11 | MCP | in-process HTTP MCP server: pending → approve → call; description change → disabled |
| E12 | Egress (docker) | blocked domain → hold card → allow for session → retry succeeds; exposed session → immediate 403 |
| E13 | OpenAI endpoint | agent mode stream + non-stream; `tools` rejected; passthrough tool round-trip |
| E14 | No sandbox | `shell` absent from the tools sent to mockllm; banner present |
| E15 | Vault canary | a secret `CANARY-<rand>` stored and used by a provider header never appears in mockllm request bodies, channel sends, logs, audit rows or SSE |
| E16 | Forged approval | mockllm emits a benign-sounding preamble and a destructive shell call: the card shows the exact argv; truncated chat card cannot approve |
| E17 | Restart | kill the gateway mid-run: run `interrupted` with notice; parked approval survives and resumes |
| E18 | Footprint budgets | image ≤ 30 MB; idle RSS ≤ 60 MB after 10 min with 3 fake channels; cold start ≤ 500 ms; fixed token overhead ≤ 2.5K (measured: §11.3) |
| E19 | Principal isolation (the "real isolation" claim) | multi mode, users A and B plus a group space: B's cookie and tokens get 404 on every by-ID route for A's sessions, runs, approvals, memory, grants and vault entries (exceptions: `POST /api/approvals/{id}` and `POST /api/runs/{id}/continue` are cookie-only (§8.3), so every bearer token gets 403 for any id, which reveals nothing about which ids exist); B's `fs_*` and shell runs cannot see A's workspace; a space run sees no owner memory or vault; v0.2: with a GitHub checkout and knowledge index for A, B (cookie and token) gets 404 on A's knowledge repo routes (`GET /api/knowledge/repos/{id}`, `…/files`, `POST …/reindex`, `DELETE`) and sees none of A's `last_indexed_sha`; `DELETE /api/users/A?purge=true` leaves no vault row, grant, memory file, workspace dir or box for A, and no `<data>/github/checkouts/<ws>`, notes directory or `knowledge_repos` rows for A |
| E20 | Allow always | approve a card with `allow_always` → a principal grant exists and the identical action in a **new** session runs without a card; revoking it in Settings restores the card; the choice is absent on irreversible, Exposed and scheduled-run cards |
| E21 | OpenAI Responses first run (optional, `e2e/openai_responses_test.go`) | a `type: openai` provider on mockllm (`api` unset, so auto): the wizard's probe reports `api: responses`, every call goes to `/v1/responses` with `store: false` and the encrypted-reasoning `include`, and a memory tool loop sends the reasoning item, message, function call and its output back in order with the earlier input byte-identical |

---

## 13. Risks, cuts and deferrals

### 13.1 Risks

| Risk | Impact | Mitigation / verification |
|---|---|---|
| sandboxd holds the runtime socket; on rootful Docker a sandboxd compromise is host root | high, inherent to option (a) | sandboxd has no network, a fixed schema with regex-validated ids, no caller-chosen image/mount/caps, peercred check, ~1.5K auditable lines. Docs and doctor steer to rootless Docker or runsc. P1 alternative backend: socketless uid-per-lease sidecar (from the "secure" proposal) for hosts that refuse a socket mount. |
| Kill/timeout semantics under runsc (`kill(-1)`) or exotic engines | stop slower than target | container kill + recreate fallback; E7 runs under runc and runsc in CI. |
| gVisor needs `--host-uds=open` for the egress socket | egress broken under runsc | UDS canary; `auto` falls back to runc with a fix line; `runsc` mode fails closed; CI job with the documented daemon.json. |
| Volume subpaths need Docker 26+ / API 1.45; Docker 25 binds host paths, which the engine resolves following symlinks; Podman compat API unverified | a gateway that swaps a directory for a symlink could mount a host path into a box on Docker 25; no exec on older engines | Docker 25: sandboxd checks every mount's device and inode against its own view of the volume before the box is used, removes a box that fails and the canary takes the same path; runc only there. Older engines: exec unavailable with "Docker 25+ required"; Podman labelled beta after a week-1 check. |
| Rootless Docker socket permissions | onboarding friction | `DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock` + `DOCKER_GID=0` (containers share dockerd's user namespace, so the socket is `root:root` inside), written by `install.sh`; exact fix lines from sandboxd and doctor (§2.10). |
| Hand-written provider clients drift from vendor APIs; fixtures may encode misunderstandings | broken streams | fixtures copied from vendor docs and SDK test data; opt-in nightly contract job with maintainer keys; each client ≤ ~800 lines behind `core.Provider` so an SDK swap stays possible. |
| disgo/slack-go behaviour behind fakes (no real keys) | reconnect bugs in production | package-private client interfaces for unit fakes; protocol-level fakes; nightly contract job; libraries are widely used. |
| CONNECT passthrough allows domain fronting on CDNs | exfiltration via an allowlisted CDN host | SNI = CONNECT host check; shared-tenant suffixes admin-only; Exposed sessions suspend grants; small upload cap. TLS inspection is P1 (with the credential-injecting proxy). |
| Tools that ignore `HTTP(S)_PROXY` | feature failures, not leaks | proxy env set for common runtimes (`NODE_USE_ENV_PROXY`); fail closed. |
| Small local models handle `tool_search`/`use_tool` poorly | low task success at 16K | compact profile keeps the most useful tools in the block; Probe reports tool-calling reliability before the user commits; tools used twice are promoted into the block at the next compaction. |
| Approval fatigue | rubber-stamping | shell auto inside the box; narrow allow-for-session grants; pre-authorized schedule grants; presets; batched approvals are P1. |
| Footprint targets are estimates | marketing risk | CI budget job from the skeleton on; publish only measured numbers. |
| One SQLite writer under heavy runs | latency | deltas are never persisted; egress log batched; measure with 4 concurrent runs in the budgets job. |
| Kubernetes pods share a netns (`network_mode: none` impossible) | no k8s exec | k8s exec unsupported in v0.1; Helm with NetworkPolicy is P1. |
| Docker's userland proxy (`docker-proxy`) can make remote clients appear to come from the bridge gateway IP (ports published on `0.0.0.0` with IPv6, or iptables disabled), which the exposure gate classes as *local* | plain HTTP served to remote peers | compose publishes loopback only; auth is still required for every route; doctor `server.exposure` flags container mode on `0.0.0.0` without an https `public_url` (medium, since the gateway cannot see the publish address; a non-loopback host listener without TLS or a proxy is high); docs say to front remote access with TLS (Caddy/Tailscale). |
| Logout does not close the SSE streams of the logged-out session; they end only at the periodic `stillAuthorized` recheck | a stream outlives its session for up to one recheck interval | the web UI closes the stream itself on sign-out (§8.2); closing a session's streams server-side at logout is a planned httpapi hardening. |
| Policy rules are first-match (§5.8), so an `auto` rule listed before a tightening rule wins | an admin's rule order can relax more than intended | the shipped rule list puts no auto rule over a dangerous form (`find`/`rg` write and exec flags never match a relaxing rule); evaluating `ask`/`deny` rules before `auto` rules is under consideration. |
| Parallel-build integration drift | rework | compiled skeleton, frozen core/dto, shared fakes and conformance suites, ownership CI, daily ICR batches, defined merge order. |

### 13.2 Cut from v0.1 (with reason)

| Cut | Reason | When |
|---|---|---|
| Passkeys/WebAuthn, TOTP; generic OIDC providers (Okta, Entra, Keycloak) | dependency weight; Argon2id + lockout meets the baseline, and Google and GitHub sign-in (§15) cover most teams | P1 |
| OS keychain and SOPS/age KEK sources | CGO/dbus or extra deps; Docker secret / systemd / file / passphrase cover containers. SOPS users can use `sops exec-env … 'bothy serve'` with `BOTHY_SECRET_*` | P1 |
| WASM Python (wazero + CPython-WASI) for single-container exec | ~25 MB blob breaks the image budget; single-container mode fails closed instead | P1 optional download |
| Credential-injecting / TLS-inspecting egress proxy | MITM CA plumbing | P1 |
| Secrets for MCP stdio servers | violates "keys only in the gateway" without the injecting proxy | P1 (with the proxy) |
| Remote MCP OAuth 2.1 (PKCE, resource indicators, CIMD, RFC 9207) | go-sdk client OAuth is experimental | P1 |
| Vector memory (sqlite-vec), learning loop, subagents, multi-bot principals, quarantined LLM | scope and cost | P1 |
| Matrix, WhatsApp, Signal, email | `core.Channel` + `AdapterDeps` already fit; each is a new package plus a line in `wire.go` | P1 |
| Browser sidecar, voice, image generation | not core footprint | P1 compose profiles |
| OpenClaw/Hermes importers, MCP server mode, webhooks/RSS/email triggers | adoption features after the core is stable | P1 |
| OTel export | built-in run trace covers v0.1 | P1 |
| age-encrypted export | `bothy backup` writes a 0600 tarball; encryption needs age | P1 |
| Batched approvals, Web Push | UX polish | P1 |
| Landlock-only backend, microVM tier, encrypted DB at rest, external audit anchoring | kernel fix unconfirmed / KVM not universal / P2 in the brief | P2 |
| Parallel execution of non-read tools | deterministic approvals | P1 for more classes |

**Explicitly never:** host-exec fallback, unauthenticated mode, the runtime socket in the gateway, telemetry on by default, an open skill marketplace, paywalled auth features.

### 13.3 How judge findings were resolved (traceability)

| Finding (judge) | Resolution in this spec |
|---|---|
| Core did not compile (`EvUsage` collision, unexported `ackToken`, no deps.go) | stream constants renamed `Stream*`; `ActionEvent.AckRef` exported; `internal/deps/deps.go`; core compile-checked (§4) |
| No exec kill/timeout mechanism | `sbx-init run --timeout`, `killall`, kill+recreate fallback (§2.7) |
| runsc needs `--host-uds` | UDS canary, runtime policy, daemon.json fix (§2.9) |
| `expose_secrets` puts secrets in boxes | removed; HTTP MCP with vault headers (§2.11, D17) |
| Semaphore held during approval waits | released while waiting/parked (D22, §5.4) |
| Exposure guard trusted the Host header | peer-IP gate + separate Host allowlist (§8.1) |
| Grants evaluated before trifecta; whole-tool grants | floors first; narrow grants + DB CHECK (§5.8, §6) |
| bwrap env leak, no seccomp/userns lock | `--clearenv`, explicit env, `--disable-userns`, seccomp (§2.12) |
| No egress port allowlist; incomplete blocklist; registries on by default | ports `[443]`, full blocklist, SNI check, presets off (§2.8) |
| `/approve CODE` fallback | removed; MAC-bound buttons from recorded messages; truncated cards deny-only (§5.9) |
| Groups ran with the owner's memory | space principals (§5.2) |
| FTS on implicit rowid (secure, ux) | `messages.id INTEGER PRIMARY KEY` (§6) |
| UI overrides hidden in the DB | `bothy.ui.yaml` overlay (§7) |
| Ollama `/v1` ignores context | native Ollama client with `num_ctx` (D3) |
| Hand-written stateful channel protocols untestable | disgo + slack-go; Telegram hand-written (D4) |
| Overloaded policy/tools module | tools moved to their natural owners (§12.2) |
| Fakes built in parallel | testkit + mockllm in the skeleton (§12.1) |
| `any` placeholders in frozen interfaces | none; event payloads use the closed `EventData` interface (§4.10) |
| Scheduled approvals stalled or denied | park by default with 24 h TTL (D23) |
| Docker socket gid / rootless friction | sandboxd fix lines; `DOCKER_GID=0` with `DOCKER_SOCK` for rootless Docker and Docker Desktop, written by `install.sh` (§2.2, §2.10) |
| Consistency review: modules could not be tested in parallel without a store; runner/hub cycle; parked/interrupted tool calls broke provider history; admin-only approvals invisible to admins; unowned routes and checks | `testkit/memstore` + `conformance.Store` + service fakes (§12.1); `Runner.SetHub`; placeholder results, synthesized results and `<approved-action>` messages (§5.6, §5.7); `Viewer.Admin` + admin-broadcast copies; `Runner.Fork`/`Constitution`, `Sandbox.Usage`, vault status/import routes, `doctor.ConfigChecks`, skills checks |

---

## 14. v0.2 (`docs/design/v0.2.md`)

The accepted v0.2 brief extends this spec; where they disagree the brief wins, and the decisions below record how it was implemented. The v0.1 security invariants all hold: no secret in a box, deterministic policy beats model judgment, cards render from structured actions, fail closed, principal isolation. F2 (OpenAI Responses) is folded into D3, §4.3, §5.14 and §12 M2; F8 (cloud deployment) lives in `docs/deploy/`; F7 (onboarding and UI) is the UI redesign of `docs/design/ui.md`, whose server work (§17 there, S1–S20) §14.7 records.

### 14.1 F1 Amazon Bedrock

- Provider `type: bedrock` with `region` (required) and optional `profile`; model ids are Bedrock model or inference-profile ids (`us.anthropic.…`, `global.anthropic.…`, `us.amazon.nova-…`, ARNs). `internal/provider/bedrock` hand-writes the ConverseStream mapping: text, system blocks, tools (several calls per turn), tool results, images, `cachePoint` blocks where the model takes them (Claude 3.5 Haiku, 3.7 Sonnet and Claude 4+ on system, messages and tools; Nova on system and messages, its tail point only after a message that ends in text; at most 4, earliest system ones dropped first), Claude reasoning with its signature (SigKind `bedrock`, sent back only to Claude models and opaque application profiles; redacted reasoning supported), Nova's `<thinking>` preamble turned into an unsigned thinking part, usage with cache reads and writes, stop reasons and error mapping (throttling/quota → rate_limit, not-ready/unavailable → overloaded, access/token/signature → auth, validation → invalid_request or context_length; 12-digit account ids removed from messages). The AWS SDK is used only for credentials, SigV4 and event-stream decoding, with retries off (§11.1).
- Credentials: the full default chain; never stored, shown or sent into a box; `credential_process` profiles are refused (the program would run outside egress). Instance and container requests use `Egress.CredentialsClient` (§2.8); STS, SSO and sign-in calls use a system client for exactly the hosts the AWS config needs; the SDK's container provider is given that client explicitly. Credentials are renewed five minutes before they expire and dropped on an auth failure (§5.14).
- Probe resolves the credentials and reports their source (`ProbeResult.Credentials`, never values), lists invocable Claude and Nova models (ListFoundationModels + ListInferenceProfiles, paginated; legacy models last) as `ProbeResult.Choices` with plain names ("Claude Sonnet 4.6", the geography such as "(US, legacy)" appended only when the same model appears twice) and suggestions (`recommended`, `quick_tasks_default`: the newest Sonnet and Haiku, the region's own geography first, then global), and tests the recommended model by default, named in `ProbeResult.Model` (v0.2 UI, S3, S5). The credential source is said in plain words that never contain a credential (detect, probe and the doctor alike): "SSO profile acme-dev", "instance role bothy-prod" (the role name read from the IMDS listing, which egress allows), "IRSA role bothy" (or "IRSA role"), "ECS task role", "EKS Pod Identity role", "container role", "access keys in the environment", "access keys in profile dev", "AWS sign-in profile me", "role deploy assumed with SSO profile admin". A failed probe carries a `core.FailureCode` (`aws_no_credentials`, `aws_denied`, `aws_no_models`, …). Detect offers "Use this server's AWS access" first (within 4 s when the environment or `~/.aws` suggests AWS, else within the local-detection timeout for IMDS), with `choices`; when the environment or `~/.aws` suggests AWS but no credentials resolve, it is still offered, with `problem` (`signin_expired` when an SSO sign-in ended, else `no_credentials`, which includes running out of time); v0.2 UI: detect also lists provider keys already saved in the global vault (`secret_ref`, `secret_hint`), matched by name prefix (anthropic, openai, gemini, google, openrouter, groq, xai, deepseek). `/api/models` lists only ids the account can invoke. An `allow_private` VPC endpoint does not make models local (free).
- Refine drafts (`ToolChoice: none`) omit `toolConfig` even with tool history: the history's `toolUse`/`toolResult` blocks go as text (`[called tool X with {…}]`, `[result of tool X]`), the signed reasoning of a message whose calls became text is left out, roles still alternate, and such drafts share no cached prefix with the tool-using step (§5.5 step 7).
- Doctor: `providers.bedrock_credentials`. Compose documents the two setups (role on an AWS host with the metadata hop limit at 2 for containers on EC2; on a laptop `AWS_PROFILE`, `AWS_REGION`, `HOME` and a read-only `~/.aws` mount with the `setfacl` line Linux needs; SSO renewals are run on the host).

### 14.2 F3 Refine mode

- `agent.refine {enabled: false, drafts: 3, depth: 2, early_stop: true, critic_slot: main}`; per message (`SendReq.refine`, `X-Bothy-Refine`), per session (meta `agent.refine.<session>`, `PATCH /api/sessions/{id}` `refine`, `/refine`) and the config default; precedence request → session → config, and config and session apply to `message` and `api` triggers only (schedules and heartbeats refine only when their request asks). `RefineMaxCalls(drafts, depth) = drafts·(1+3·depth)` (21 by default) is what `GET /api/refine` reports for the cost note.
- Engine (`internal/agent/refine`, embedded `generator.md`, `critic.md`, `corrector.md`): N drafts, D rounds per draft of continue → critique → correct, the critic able to confirm a draft ("Verdict: correct"), then a plurality vote. `Extract` takes the text after the last `Final answer:` line (markdown around the label tolerated, a lone `\boxed{}` or `$…$` wrapper and a trailing full stop removed); `Normalize` trims, strips LaTeX spacing tokens, collapses whitespace, unwraps one brace layer, drops trailing decimal zeros and lowercases; empty answers are ignored and ties go to the earliest draft. Answers are taken from the redacted text, capped at 256 bytes, and a trace at 128 KiB. Runner behaviour: §5.5 step 7 and §5.6; drafts use the model's default temperature (reasoning models reject others), and the corrector is skipped when the critic confirms a draft that states a final answer.
- Trace: `core.RefineTrace` in the answer's `PartRefine` part (history drops it), `RunDetail.refine`, and `run.refine` events; fallback reasons `budget`, `time`, `new_message`, `stopped`, `failed`. A refined message's refine toggle is not stored on the run, so a parked run that resumes follows the session or config setting.

### 14.3 F4 GitHub and repositories

- Connections (`internal/github`, migration 0002): a fine-grained PAT or a GitHub App (RS256 JWT from the private key, installation tokens minted on demand, cached until five minutes before expiry, replaced once on a 401, registered with the redactor). The secret goes into the global vault scope (or an existing `secret:<name>`); the row keeps only the reference. Every call goes through `SystemClient` for the `github` destination (§2.8); the token is sent only to the API host; the tarball redirect is fetched without it and only from configured hosts; `Link` pagination, `x-ratelimit-*`/`retry-after` and 401/403/404/409/422/5xx map to plain messages; request URLs are removed from transport errors. Connection health is recorded by the calls themselves, including a 401 or 404 while minting an App's installation token (a key GitHub no longer accepts, an uninstalled App), so Settings shows the broken connection without a manual test. A repository a connection no longer allows moves to the connection that asks for it, keeping `created_at`. Index state is per principal: `GitHubRepo.LastIndexedSHA` is never stored (`core.GitHubStore` has no `SetIndexed`), and `GET /api/github/repos` fills it with the caller's own `IndexedRepo.IndexedSHA` of a GitHub-source index of that repository.
- Tools (gated on an allowlisted repository, never offered to restricted spaces; §5.6): `repo_list` (core, trusted metadata: the account GitHub shows on what Bothy writes, `@<account>` for a token or `@<app_slug>[bot]` for an App, named once or per repository when connections differ; for each checkout its commit, that it is plain files without git, and counts of changed files not yet sent and of files already sent with their pull request numbers, never paths, from scans bounded to 8 s per call; its description says a checkout is not a git repository and that every pull request is a new branch that is never updated); `repo_checkout` (local_write; resolves the ref to a commit, extracts the tarball through `os.Root` into `repos/<owner>/<name>` with caps `github.checkout.max_mb`/`max_files` and a decompressed-bytes cap, drops links, devices, `.git` and unsafe names, swaps the folder in atomically, refuses to overwrite edits unless `discard_changes`, which makes it an irreversible card (files a pull request already sent and unchanged since are on GitHub and do not count); keeps the manifest and pristine archive under `<data>/github/checkouts/<ws>/…`; a new checkout starts a manifest with nothing sent; taints Private|Untrusted and marks the workspace untrusted; publishes `repo.checkout`); `repo_changes` (diff against the manifest, honouring `.gitignore`, skipping `.git`, bounded scans; files a pull request already sent and unchanged since are listed apart with its number and no diff, and a file changed again is marked with the pull request that last carried it); `repo_open_pr` (`AlwaysAsk`, §5.8 FA and §5.9; Git Data API blobs, a tree on the checked-out commit, a commit, a branch and the PR, deleting the branch if the PR is refused; always a new branch, and an open PR is never updated; after the PR opens, the manifest records each file's op, sha256, mode and PR number (`sent`), and later calls leave those files out unless they changed again or are named in `paths`; the result names the acting account); `github_issues`, `github_pr`, `github_runs` (read; content is Private|Untrusted and spotlighted; no free-text issue search so qualifiers cannot reach other repositories; one pull request also lists each reviewer's latest verdict (a later comment does not undo an approval or a change request) and who is still asked to review, plus review bodies with `comments`) and `github_comment` (external_send). Purging a user removes their checkout data.
- The service also implements `core.WorkflowDispatcher` for allowlisted repositories (`Serves`), which the release module uses (§14.5). `Dispatch`, `Serves` and every agent tool keep the allowlist gate; `RunStatus` and `FailedStep` also read a repository its connection no longer allows (the follower reads a deploy's run through the dispatcher that started it), and one no connection holds is refused. `FailedStep` (v0.2 UI, S17) names the first step with conclusion `failure` or `timed_out` from `GET runs/{id}/jobs?filter=latest`, for `release.finished`.
- **Bothy's own GitHub App** (`internal/github/apps.go`, migration `0012_github_apps.sql`, `core.GitHubAppMaker`, routes §8.3): the primary way to connect. Bothy builds a manifest (`name` default "Bothy (<org or username>)" cut to GitHub's 34 characters, "Bothy (<org>) 2" and on while an app Bothy recorded has the name, since names are unique on GitHub, `url` and the redirect and setup URLs on `server.public_url` or the request's own origin, `public: false`, `setup_on_update: true`, no `hook_attributes`, `default_events: []`, no `callback_urls`) with exactly these `default_permissions`, the least the tools and releases call:

  | Permission | Level | Why |
  |---|---|---|
  | `metadata` | read | required with any repository permission; `GET /installation/repositories`, `GET /repos/{o}/{r}` |
  | `contents` | write | commits (sha media type) and tarballs for `repo_checkout`, `BranchHead`, `CommitSubject`; Git Data API blobs, trees, commits, refs and the branch delete for `repo_open_pr` |
  | `pull_requests` | write | `github_pr` reads; `POST /pulls`; a `github_comment` on a pull request |
  | `issues` | write | `github_issues` and a pull request's conversation; a `github_comment` on an issue |
  | `actions` | write | workflows and runs (`github_runs`, releases); `workflow_dispatch` for `release_deploy` |

  Never `administration`, `secrets`, `workflows`, `environments`, `deployments`, `checks`, `statuses`, `pages`, `members` or any organization or account permission. Without `workflows`, GitHub refuses a branch whose commit changes `.github/workflows/*`, so a pull request that edits CI fails with GitHub's reason: CI definitions are a privilege-escalation path. The browser posts the manifest from a handoff page (§8.1) to `<web>/settings/apps/new` or `<web>/organizations/<org>/settings/apps/new` (`<web>` from `github.api_base`: github.com for `api.github.com`, the Enterprise host otherwise, the same origin for a loopback test base). GitHub's redirect brings a one-hour code that redeems the app's private key without authentication: Bothy redeems it once (`POST /app-manifests/{code}/conversions` through the `github` SystemClient, no Authorization header), only for the browser and live admin session that started the flow (state, flow cookie, session hash, at most an hour), never while the vault is sealed, and never echoes it. The private key goes to `secret:github_<gha id>_key` and the client secret (kept for a possible GitHub sign-in, unused today) to `secret:github_<gha id>_client_secret` in the global scope; the webhook secret is not decoded. The app's page is `<web>` plus the path of GitHub's `html_url` when that path is the slug's page (`/apps/<slug>`, or `/github-apps/<slug>` on Enterprise Server), whatever host GitHub names, since Bothy may reach GitHub by another name; otherwise it is derived (`/apps/<slug>` on github.com and a loopback base, `/github-apps/<slug>` elsewhere) and a warning logged. The install page (`<page>/installations/new`) and the settings link derive from it. Once the code is spent, only an answer whose id, slug, owner (login, id, type) or key is off is refused; the name is put on one line and cut to 100 characters (the slug when empty) and a client id that isn't printable ASCII is dropped, so a surprise there never costs the key. A permission GitHub granted beyond the manifest is audited and logged, not refused. The install page sends the browser to the setup URL with `installation_id`, which Bothy uses only for the flow that made the app, after `GET /app/installations/{id}` with the app's JWT confirms it is the app's (another app's installation is not found) and on the account that owns it now, by account id, which a rename keeps: `GET /app` with the same JWT gives the current owner, and an app transferred to another account on GitHub has its owner (login, id, type) recorded again, so the new owner's installation counts and a former owner's or any other account's never does (the app is private, so only its owner should be able to install it; the check holds if that changes); it then becomes an App connection with `OwnsSecret` (the app's key). A setup redirect without such a flow only lands on the GitHub page, which checks the app with the admin's session (`POST /api/github/apps/{id}/check`: only an installation on the current owner's account id counts). A second check or a concurrent callback returns the same connection; a new installation of the app (uninstalled and installed again) moves the connection once GitHub confirms it the same way. An app made to replace a connection (`replaces`) takes over the repositories that connection allows and its installation reaches, then disconnects it (retiring an older Bothy-made app). If that fails (GitHub or the store), both connections stay, the app keeps `replaces` and the next check or setup redirect tries again; the GitHub page says Bothy still uses the old connection, with Try again. Starting is refused while another app is pending (409), from an address other than `public_url` (409 `wrong_address`: the flow cookie is host-only) and while the vault is sealed. "Start over" and disconnecting retire the app: both secrets are deleted and the row stays as a reminder with the app's settings link until an admin dismisses it. Bothy never deletes apps on GitHub. Flows live in memory (64, at most 4 per admin, one hour): a restart before the code is redeemed lands `not_finished`, whose copy says to delete the app on GitHub; after it the pending app completes through the check. Automatic checks use a cached app source, so the key is read at most every five minutes. Changing `public_url` later only breaks the install and update redirects (the app keeps its setup URL; the admin can edit it on GitHub, and the check recovers either way).

### 14.4 F6 Knowledge

- `internal/knowledge` (migration 0003, config `knowledge`): an index queue (two workers) fed by `repo.checkout` (a `workspace_id` other than the principal's is ignored; a session-scoped event counts as touching the repository), `POST /api/knowledge/repos` (any workspace directory, default name `local/<dir>`) and boot recovery. The walk is read-only through `os.Root`, never follows symlinks, opens only regular files with `O_NONBLOCK`, skips VCS metadata, vendored and build directories, lockfiles, minified, generated (by name and header), binary, `.gitignore`d and `linguist-generated`/`linguist-vendored` paths, bounds ignore-rule work (10000 patterns, 50M matching steps) and enforces the size caps; a file is re-chunked only when its chunker version plus sha256 changes. Chunking: markdown by headings (fences and front matter respected), Go, Python, JS/TS, Rust, Java family, C/C++, Ruby, PHP, shell, Lua, Elixir and SQL by top-level declarations (methods titled `Parent: signature`), TOML/INI sections, YAML top-level keys, Makefile and Dockerfile sections, paragraphs otherwise.
- `knowledge_search {query, repo?, limit?}` quotes every word (no FTS syntax injection), ANDs them and falls back to OR, matches other forms of each word (porter stemming since 0010: "redirects" finds `redirect`), ranks with bm25 weighting path and title, narrows each hit to the lines around the match and cites `owner/repo@sha:path#Lx-Ly`; results are Private|Untrusted. A finished index records the commit it read as the principal's `IndexedSHA` (`knowledge_repos.indexed_sha`, written by `KnowledgeRepo.UpdateIndex`; a failed index keeps the previous one).
- Project notes live at `<data>/knowledge/<principal>/<owner>/<repo>/NOTES.md`, not the brief's `<data>/knowledge/<owner>/<repo>/NOTES.md`: notes come from one principal's workspace and sessions, so sharing them would let one user's tainted session write text injected into another's (principal isolation); two users who check out the same repository keep separate indexes and notes. After the first index a fast-slot draft (purpose `notes`, metered, redacted input and output, marked untrusted) reads the README, build files, two CI workflows and the top-level layout, and never overwrites edits saved meanwhile. The `notes` tool reads, appends to a section or replaces exact text; edits are Kind `memory`, SideEffect `local_write`, with the repository named in the card diff (F7 applies). A user save through the API makes the notes trusted. Turn-context injection: §5.6. Bothy never writes into a repository. Purge removes a user's notes; index rows cascade with the principal; backups carry `knowledge/**`.

### 14.5 F5 Releases and approval flows

- `releases {github_token, github_api, poll_interval, follow_for, environments[]}` (§7.1, §7.4). `release_deploy {environment, ref?}` (irreversible, message trigger only, core while environments exist) plans an `ActRelease` action whose payload is what approvers see and exactly what runs (environment, repository, workflow, ref — defaulting to `ref_pattern` when it names one ref, otherwise matching the glob — and the fixed inputs). Policy: the release floor (§5.8 FR) and multi-approver votes (§5.9). Exec refuses unless the environment is unchanged and this very call's approval was allowed by at least `required` approvers other than the requester whose votes still satisfy the environment's current approvers and `required` from enabled accounts; then it dispatches and records a `core.Deploy` (also when GitHub refuses, as failed); the dispatch and the record survive a cancelled tool call.
- Dispatch: a repository on a GitHub connection's allowlist goes through that connection (`release.Deps.Connections`, chosen per deploy and recorded in `Deploy.Via`; the follower and `release_status` read the run through that dispatcher even after the allowlist changes, and rows with `''` follow the current choice); others use `release.GitHub` with `releases.github_token` against `releases.github_api`. Both send `workflow_dispatch` with `return_run_details` (asking again without it after a 422 that names it; finding the newest `workflow_dispatch` run of the ref created in the last 10 s after a 204 — two dispatches of one ref within seconds can be confused), keep only checked run fields and build run links from the web base. A follower polls open deploys every `poll_interval` and right after a dispatch, picks them up after a restart, ends them on completion, an unknown run or `follow_for`, posts the outcome as a Bothy notice to the session and the originating chat, and audits `release.finish`. v0.2 UI (S17): `release.dispatched {deploy_id, environment, repo, ref, url?}` is published after GitHub accepts a recorded deploy, and `release.finished {deploy_id, environment, repo, ref, status, ok, url?, duration, failed_step?}` when following ends, or when GitHub refused the dispatch (`ok` false, no `url`); both go to the deploy's principal and as an admin broadcast, with session and run set. `duration` is the run's own `created_at` to `updated_at`, else how long Bothy followed it. `failed_step` comes from the optional `release.StepReader` of the dispatcher that started the deploy (`release.GitHub` and `github.Service.FailedStep`, asserted in `cmd/bothy`), cleaned by `stepName` (one line, no control or format characters, at most 100 runes), and kept out of notices, since step names are written by whoever can push to the repository. Outcome notices are plain copy: *"Deployed acme/web to staging in 3 min. View it on GitHub: <url>"*. `release_status {environment?}` (read) lists environments, approvers and recent deploys.
- Refinements of the brief: `release_deploy` is also denied from group spaces (who asked is ambiguous there); the requester may deny to withdraw; `required: 0` means 1. Approvals still use `policy.approval.interactive_timeout`; a plain admin can see but not vote on an approval whose only approver role is owner.

### 14.6 Integration notes

- One checkout event: `repo.checkout` (`RepoCheckoutData`), published by F4 and consumed by F6 (F6 had defined its own `repo.checked_out`, which no module published; it was folded into `repo.checkout`).
- Migrations 0002 (F4), 0003 (F6), 0004 (F5), 0005 (approval comments, S4/S12), 0006 (one-time passwords, S9), 0007 (deploy `via`, per-principal index state), 0008 (aws-sm GitHub connection refs, §14.8) and 0009 (UI state, §14.7) apply in order on a fresh database and on a v0.1 database (§6); the store's migrator tests start from `0001_init.sql` alone, and `TestIndexStateMigration` upgrades a version-4 database.
- `cmd/bothy` builds the GitHub, knowledge and release services, registers their tools and checks, passes `GitHub`, `Knowledge` and `Deploys` to the HTTP API, runs `knowledge.Run` in every mode and `release.Run` outside safe mode, and gives the runner the knowledge turn-context hook.

### 14.7 v0.2 UI server work (`docs/design/ui.md` §17)

The redesigned UI relies on S1–S20 of its plan; the shapes are in §4, §8.3 and §8.4, and server copy follows its copy guide (ui.md §14: plain words, no ids or config keys in the main flow, the error formula). Decisions:

- **S1, S7:** `SetupState.install` and the setup banner (§8.1, §8.3); `SetupOwnerReq.display_name`. `POST /api/setup/complete` still answers 404 once setup is done (setup routes 404 after completion), so the UI treats a 404 on re-entering the last step as done.
- **S2, S4:** plain card copy, chat cards and reasons (§5.8, §5.9); `Approval.Approvals`, `Requester` and `Comment`. The deploy card's Version field shows the ref only (no short sha or commit subject: a commit subject is written by whoever can push).
- **S3, S5, S6, S13:** Bedrock wording (§14.1); plain model names (the catalog's, then the vendor's own, then built from the id: `qwen3:14b` → "Qwen3 14B") and one `recommended` and one `quick_tasks_default` model per provider, chosen heuristically from catalog names (the newest Sonnet and Haiku, the newest plain GPT and its mini, the newest Gemini Pro and Flash-Lite, the largest local model up to 40B for both slots, else the first and the cheapest priced model); one-call connect (§8.3); `core.FailureCode` on probes, GitHub probes and channel tests (§4.3). A probe's raw `error` is redacted with account ids removed.
- **S8:** runs, usage rows, approvals and audit rows carry names (`dto.RunView`, `dto.UsageRowView`, `decided_by_name`, `dto.AuditRow`). Chats and schedules are named through the caller's own view, so their titles never reach anyone else, admins reading everyone's usage or the audit log included; people are named by display name, else username; an audit row about an approval or tool call is phrased from that record, read through the admin view; an id the view cannot resolve names nothing. `requested_by_name` is empty for schedules and check-ins, the account owner in their own chats, the teammate in theirs, and in a channel the person who wrote, found among the newest 100 messages of the session (older tasks name nobody); team chats are not on the web, so only an admin's audit and usage views name them. The built-in heartbeat's `schedule_name` is "Check-in".
- **S9:** one-time passwords (§4.12, §8.1, §8.3).
- **S10:** the refine estimate counts early stop as half the rounds ((depth+1)/2) and does not discount a fast critic slot, so it errs high; a chat with no priced answer, or one not stored yet (`GET /api/refine`), gets the multiplier alone.
- **S11:** tool states are derived on every read, never stored: an open approval means `waiting_approval` (also an open egress hold, `<call>#net`, while the call runs); a call is `done` only once its result exists; a denial without a card is `blocked`; a cancelled approval reads `expired`. `approval_id` is the call's own approval, else its open hold, else its latest hold.
- **S12:** the approvals inbox (§9.3) and `inbox.changed`. A pending sender's `first_text` is the start of their first message that is not a command (Telegram's `/start`), cleaned to one line and cut; the store keeps it only while the identity is pending. With `channels.unknown_senders: ignore` nothing is stored or published.
- **S14:** a chat is stored together with its first message (`POST /api/sessions` with `message`); session lists hide sessions without messages and retention deletes empty web sessions after a day (§6); untitled chats carry a draft title until the generated one arrives (§5.5).
- **S15:** search snippets are plain text (Markdown removed, table separator rows dropped) with match ranges in UTF-16 code units (JavaScript string indices); chats return one row each (their best message) and deleted chats are not found; code hits come from `Knowledge.Search`.
- **S16:** each audit row has one plain `summary` from a server-side phrase table (`audit_summary.go`, `phrases.go`, after ui.md §7.4); egress rows merge decision and connection result into `outcome`.
- **S17:** release events (§14.5, §8.2).
- **S20:** company wording in the constitution (version 2) and the default persona (§5.6).
- **UI state (fallbacks removed):** the server now carries what the redesigned screens used to guess or hide; migration 0009 adds `users.last_login_at`, `preferred_chat` (≤ 32) and `approvals_web_only`, `runs.refined`, `release_deploys.sha` and `subject` (≤ 400), and `skill_approved_files` (STRICT, cascading with the skill), and `TestUIStateMigration` upgrades a version-8 database.
  - *Profile:* `GET`/`PATCH /api/me` (`dto.Profile`, `auth.Service.UpdateProfile`): anyone renames themselves (the principal's display name follows, as for admin renames; no control or format characters, and not a name or username someone else has, so nobody can pass as a colleague in approval cards) and sets their own time zone. Since 0013 the profile is name and time zone only: the preferred chat and approvals-on-the-web-only settings were removed with linking. A new account starts in the configured `timezone`; the person's zone then serves their chats (turn context) and is the default for the schedules they make (form, API, `schedule_create`) and the zone of the owner's heartbeat, while daily limits keep counting days in the configured one. The change is written with `UserRepo.SetProfile`, which touches only the profile columns, so a rename racing an admin's role change, turn-off or password reset cannot undo it.
  - *Persona:* `agent.name` (default "Bothy"; 1–40 runes, no control or format characters, no `<` or `>`) and `agent.tone` (`warm` | `direct` | `formal`) are hot and go into the turn context's facts ("Your name", "Tone"), never into the constitution.
  - *Sign-ins:* `UserRepo.SignedIn` records `last_login_at` on each password sign-in (and clears the failure count and lock); `Update` leaves it alone.
  - *Credentials:* `ProviderHealth.source` (§8.3); the bedrock source comes from `Chain.Source()`, which reads what the chain already resolved and never calls AWS.
  - *Pause:* `Runner.PausedBy()` and `meta.paused_by` (`<user id> <unix ms>`) keep the first pause's who and when across restarts and repeated pauses; the banner text is "Bothy is paused by <name>." and `dto.Me` carries the same fields for a client that missed the event.
  - *Project notes:* each change (a user save, Bothy's `notes` edits with their chat and run, a draft) appends `{before, after}` to `history.jsonl` beside `NOTES.md` (0600, newest 50 entries and at most 4 MiB kept, damaged lines skipped, deleted with the repo); a save that changes nothing adds nothing. History is read through the caller's view like the notes; the chat link is named only when the caller can open that chat. `summary` is the first sentence of the notes as plain text (≤ 160 runes); `progress` counts files read against an estimate (the last index's count, else the directory's files counted up to the index's file cap), published at most once a second; `stale` compares a GitHub index's `indexed_sha` with the default branch head (`GitHub.BranchHead`, cached per repository for 10 minutes, a failure for 1, at most 20 lookups and 4 s per listing).
  - *Skills:* enabling a skill stores its approved snapshot in the database (`SkillRepo.SetApproved`: SKILL.md and every listed file with its hash and size, text kept when it is UTF-8 within 256 KB per file and 1 MiB in all), out of the skills directory so whoever can write there cannot change it. The snapshot is the bytes the approval's own hash walk read (`treehash.SumRootFiles`), so a file swapped while an admin approves cannot become the baseline. The inbox's `skill_review` item carries `skill_changes` (added, removed and changed files with both sides as text, at most 50 files and 512 KiB, else `truncated`; `no_baseline` for skills approved before 0009). Skill files are served inert (§8.3).
  - *GitHub:* `GitHub.Workflows` lists an allowlisted repository's workflow files (every page, `.github/workflows/` paths only, names cleaned); any other repository is `ErrNotFound`, as for `BranchHead`.
  - *Web search:* `POST /api/search/test` runs `egress.TestSearch`, the tool's own request path and checks, with search failures as `core.FailureCode`s.
  - *Tasks:* `RunView.origin` comes from the run's trigger and session channel; `reason` from who stopped it ("by you", "by <name>", "while it waited for approval") or a plain phrase mapped from the run's error (never the error itself, which can hold a provider's message); `Run.Refined` is set when refine mode wrote the answer.
  - *Deploys:* `Deploy.SHA` is recorded at dispatch and when the follower first sees the run's `head_sha`; `Subject` is read once through the dispatcher's optional `release.CommitReader` (`github.Service.CommitSubject`, `release.GitHub.CommitSubject`), cleaned to one plain line of at most 120 runes. The subject is written by whoever can push, so it appears only in deploy lists as quoted, inert text: never in cards, notices, events, `release_status` or prompts; the deploy card's Version field still shows the ref only (S2).

### 14.8 External secret references (AWS Secrets Manager)

A company can keep Bothy's secrets in its own secret store and have Bothy fetch them itself, instead of copying them into the vault.

- **Grammar** (`config.ParseAWSSecretRef`, `FindAWSSecretRefs`): `aws-sm:<secret name or ARN>[#<json key>][?region=<region>&profile=<profile>]`; the key and the query may come in either order. Names use AWS's characters plus `!` (secrets other services manage, `rds!cluster-…`); a name never contains `:`, an ARN must be a Secrets Manager secret ARN in the partition of its region (`aws`, `aws-cn` for `cn-*`, `aws-us-gov` for `us-gov-*`; the isolated partitions are refused) and brings its region. A marker preceded by a letter, digit, `_`, `.` or `-` is part of a longer word, as with `secret:`, and so is one right after a `secret:` marker (`secret:aws-sm:x` names the vault secret `aws-sm`, as before v0.2); `secret:` inside an aws-sm reference (an ARN's `:secret:`) belongs to it. Accepted wherever `secret:<name>` is: every `secret:"true"` field including header values (provider `api_key`/`headers`, channel tokens, search keys, MCP headers, `releases.github_token`), `POST /api/providers/test` and `/connect` `secret_ref` (connect returns `{source: "aws-sm"}` meta without a vault lookup), and GitHub connections' `secret_ref` (migration 0008 rebuilds `github_connections`, and `repos` against it so the foreign-key cascade survives, with `CHECK (secret_ref LIKE 'secret:_%' OR secret_ref LIKE 'aws-sm:_%')` and `CHECK (owns_secret = 0 OR secret_ref LIKE 'secret:_%')`: Bothy never owns an AWS secret).
- **Config** `secrets.aws {region, profile, cache_ttl: 5m, allow_private: false}` (hot). Validation: region and profile shapes, `cache_ttl` 1s–24h, and `secrets.aws.region` required when any reference lacks both `?region=` and an ARN. Per reference, region = `?region=` → ARN → `secrets.aws.region`; profile = `?profile=` → `secrets.aws.profile` → `AWS_PROFILE`/default chain.
- **Resolution** (`internal/vault/external.go`, `internal/vault/awssm`): one `GetSecretValue` (AWSCURRENT) per secret, JSON 1.1 over HTTPS, signed by hand with SigV4 like the bedrock client; no SDK service client, no SDK retries, no redirects. Credentials: the bedrock provider's `Chain` (`provider.Facade.AWSCredentials`, handed over by `cmd/bothy` through `vault.SetAWS` because the vault is built before egress and the providers), one per region and profile: the full default chain, STS/SSO/sign-in through a system client for exactly the hosts the AWS config needs, IMDSv2 and container credentials through `Egress.CredentialsClient`, `credential_process` refused, renewal five minutes early, dropped when AWS rejects them (ExpiredToken, UnrecognizedClient). Secrets Manager itself: `Egress.SystemClient(SystemDest{Name: "vault:aws-sm", Hosts: ["secretsmanager.<region>.<suffix>:443"], AllowPrivate: secrets.aws.allow_private})`; a client without its own transport is refused. Offline CLI commands (no `SetAWS`) fail such references with a plain error; they resolve while the vault is sealed (they need no KEK).
- **Values:** the whole `SecretString`/`SecretBinary` cached in memory per region|profile|secret for `cache_ttl`, one fetch for concurrent uses (detached from a caller that gives up, 20 s), failures not cached, expired values dropped from the cache; never written to disk, the database or a backup. With `#key` the value is the key's string, or the compact JSON of a non-string value; `null` counts as missing. The redactor holds the whole value and each key read, under ids carrying the `VersionId`, for the current and the previous version (a rotation's old value may still sit in recent output); a rotated secret is read when its cached value expires. Every use (cached ones too) is audited `secret.use` before release, target the canonical reference, detail `{ref, purpose, source: "aws-sm", scope: "global", region, version, cached}`; a failed use is audited with outcome `error` and its `code`; an audit failure withholds the value; no auditor, no value.
- **Failures** are `vault.ExternalError` (a `core.FailureCoder`) with plain messages that name the reference (account ids and role sessions removed) and what to do: `aws_no_credentials` (sign in / attach a role), `aws_denied` (AccessDenied, KMS DecryptionFailure: allow `secretsmanager:GetSecretValue` and `kms:Decrypt`), `not_found` (wraps `core.ErrNotFound`; also a secret scheduled for deletion), `json_key_missing` (wraps `ErrNotFound`), `not_json` (wraps `ErrInvalid`), `rate_limited`, `provider_error`, transport codes (`private_address` names `secrets.aws.allow_private`), and a missing region (`ErrInvalid`). The provider probe reports the code (`ProbeResult.Code`).
- **Scope and visibility:** the references are company-wide: chosen by admins in the configuration or a GitHub connection, the same value whoever's request resolves them, and a vault value that looks like a reference is never resolved again (no user-to-company indirection). `List(global)` adds each distinct reference of the configuration and of the GitHub connections as `SecretMeta{scope: global, name: <canonical ref>, source: "aws-sm", hint: "", updated_at: last fetch, last_used_at}`; user scopes never list them (so a purge never touches them); `Delete` refuses them; `Status().External["aws-sm"]` counts them. `bothy vault list` shows the configuration's references offline. Any admin can thus reference any secret the server's AWS identity may read, and point a provider at an address of their choosing, so that identity's policy bounds the reach: scope it to Bothy's secrets (`deploy/aws/iam-secrets-policy.json`), and on a laptop prefer a profile made for Bothy over a personal one.
- **Doctor** `secrets.aws_access` (vault checks): for each region and profile the references use, `Chain.Describe` within 15 s: info "AWS secret references sign in with <source>" or high with the plain cause and `aws sso login --profile <p>` as the fix; high when references have no region. It never reads a value.
- **Choices beyond the brief:** the configuration key is top-level `secrets.aws`, not under `vault` (`vault.*` is locked and read at boot, while these settings are hot); `secrets.aws.allow_private` admits an interface VPC endpoint whose private DNS name resolves to a private address; only `AWSCURRENT` is read (no `version_stage` or `version_id` parameters); failed uses are audited too, with outcome `error` and their code.
- Tests: `internal/vault/awssm` (request shape and signature, string and binary, error mapping, no redirects, credential and transport failures), `internal/vault/external_test.go` against an httptest Secrets Manager behind an egress stand-in that admits only the requested hosts (plain and JSON secrets, missing key, not JSON, not found, denied, caching, TTL, rotation, single flight, redaction, audit, IDOR, egress destinations, sealed vault, doctor), `internal/config/secretref_test.go`, `internal/store` (migration 0008), `e2e/aws_secrets_test.go` (no credentials: listing, member isolation, doctor, probe code, audit) and `e2e/live_aws_secrets_test.go` (tags `e2e,live`: a JSON key of a real secret resolves and its value appears nowhere else; skips without credentials).

### 14.9 Team memory ("Remembered for everyone")

Personal memory belongs to one principal, so a note one person asked Bothy to keep never reached a teammate in another chat, and the only team-wide text was the admin's standing rules. Team memory is one note file for the whole team that only humans approve, so one person's tainted chat still can't plant text in everyone's context.

- **Storage** (`internal/memory/team.go`): `<data>/memory/team/TEAM.md`, `core.FileTeam`, capped by `memory.team_max_bytes` (default 4096, minimum 256), with the personal files' mutex, atomic write and rollback; its history is `team_memory_events` (§6, 0014), not `memory_events`. `team` can't be a principal's folder (ids start `usr_`/`spc_`), so `Purge` never touches it; backups include it with the rest of `memory/`. The `memory.over_cap` check covers it.
- **Reading:** every run's S1 ends with `<team-memory>` after the standing rules, for every principal and trigger, read on each call (§5.6). Trusted like the standing rules (no taint), redacted, counted as memory in the breakdown.
- **Agent writes:** the `memory` tool takes `file: "TEAM"`. `Plan` dry-runs it (cap, exact entry; near misses may name entries, since the text is in every prompt) and returns an `admin` Action; policy decides any `memory` call on TEAM as admin-class (F3, §5.8): one admin approves it once (`AdminOnly`, `[allow_once, deny]`), never from a schedule, check-in or API run, and never from chat (`authorizeChat` admits only Team requests; chat approvers get a link card, the chat that asked the needs-an-admin line, §5.9). `Exec` writes only after checking the stored approval itself (`ToolEnv.Approval`: allowed, allow_once, `AdminOnly`, this run, this Action's fingerprint, decided by an enabled admin, not used before: the store allows one row per approval) and records the approval, `agent:<run>` and provenance (`untrusted` when the session holds outside content). `Apply` refuses TEAM. Accounts, teammates and standard channels may propose; restricted channels can't (no `memory` tool). The tool description and S2's facts tell the model that USER/MEMORY are for this person only and that a TEAM change needs an admin's approval, so it never says the note is saved for everyone until the result says so.
- **Card** (no new `CardKind`; recognised by tool `memory` + file `TEAM`, in `policy/copy.go` and `web/src/lib/cardcopy.ts`): add "Remember this for everyone?" / "Remember for everyone", change "Change something Bothy remembers for everyone?" / "Save change", remove "Forget this for everyone?" / "Forget for everyone"; consequence "Bothy will use this in every chat, for everyone." (remove: "Bothy will stop using this in every chat."); fields Note (and Was); no In row, no code, default tone; risk `admin`. Warnings: the exposure line when Exposed, *"This chat has seen content from outside your team. Check that a person asked for this."* when the chat holds outside content but isn't Exposed, the untrusted-origin line for a run an untrusted message started; `memory.tainted_writes: deny` denies it in a chat holding outside content.
- **Admin editing:** `GET /api/memory/team` (anyone signed in), `PUT` (admin, If-Match), `GET /api/memory/team/history` (§8.3); `Memory.WriteTeam` refuses anyone but an enabled admin. The web shows it as "Remembered for everyone" above "About you" on Knowledge → Memory, read-only for members (`docs/design/ui.md` §9.4).
- **Events and audit:** `memory.updated` with file `TEAM` is an admin broadcast without the run; `memory.write` / `memory.edit` carry `scope: team`, target `memory:team/TEAM`, and for agent writes `approval_id`, `approved_by` and `provenance`, never the content; activity and audit lines say "Remembered something for everyone", "Edited what Bothy remembers for everyone".
- Tests: `internal/memory/team_test.go` (caps, atomicity, rollback, concurrency, admin-only edits, the tool proposing and refusing every approval that isn't exactly its own), `internal/policy/team_memory_test.go`, `internal/agent/teammemory_test.go` (S1 for every principal kind and trigger, redacted with `</team-memory>` escaped, no taint, breakdown, no cache-bug report), `internal/httpapi/teammemory_test.go`, E10b; web: `lib/cardcopy.test.ts`, `lib/activity.test.ts`, `ApprovalCard.test.ts` and `Memory.test.ts` against the mock's `/api/memory/team` (scenarios `team-memory-empty`, `team-memory-ask`).

## 15. Sign in with Google and GitHub

People can sign in with a Google or GitHub account next to their username and password. Password sign-in stays (it is the owner's fallback), and setup is unchanged. There is no new dependency: `internal/auth/oauth` (PKCE, authorize addresses, the code exchange, the claim and list checks; stdlib only) does the protocol, `internal/auth` runs the flows, and every request goes through `egress.SystemClient`.

**Configuration** (`auth.sign_in`, §7.1; Settings → Security → Sign-in):

- `allow`: one list of who may sign in, shared by both providers. Each entry is a domain (`acme.com`) or an exact address (`pat@gmail.com`), stored lower-cased, at most 200, no repeats. A domain lets in Google Workspace accounts of that domain only; GitHub accounts need their exact address on the list, or an organization. Domains are checked with the same rule as everywhere (`[a-z0-9-]` labels, at least two). A well-known public mail domain, relay or noreply domain (`gmail.com`, `googlemail.com`, `outlook.com`, `hotmail.com`, `live.com`, `msn.com`, `yahoo.com`, `icloud.com`, `me.com`, `mac.com`, `aol.com`, `proton.me`, `protonmail.com`, `gmx.com`, `mail.com`, `yandex.com`, `qq.com`, `users.noreply.github.com`, `duck.com`, `privaterelay.appleid.com`, `pm.me`, `fastmail.com`, `hey.com`, `yahoo.co.uk`, `yahoo.co.jp`, `hotmail.co.uk`, `hotmail.fr`, `outlook.fr`, `live.co.uk`, `gmx.de`, `gmx.net`, `web.de`, `mail.ru`, `163.com`, `126.com`, `naver.com`, `zoho.com`, `tutanota.com`, `yandex.ru`, `rambler.ru`, `libero.it`, `orange.fr`, `free.fr`, `t-online.de`, `btinternet.com`, `comcast.net`, `verizon.net`, `att.net`) is refused as a domain: "anyone can get an address at <domain>; list each person's address instead". Exact addresses at those domains are fine. An address has exactly one `@`, a printable local part without spaces (at most 64) and a valid domain.
- `allow_new`: someone the rules let in (on the list, or a member of a listed GitHub organization) who has no link gets a new member account the first time they sign in. It takes effect only in team mode (`mode: multi`); validation does not tie it to the mode, the admin page says so (and, when GitHub organizations are set, that their members get an account too).
- `google` and `github`: `enabled`, `client_id` (printable, no spaces, at most 256), `client_secret` (a `secret:<name>` or `aws-sm:` reference; the UI saves it as `secret:<provider>_sign_in_client_secret`) and the locked, loopback-only `api_base` for tests. `github.orgs` (at most 20 organization names) is an optional extra rule: active members of any of them may also sign in. `github.org_ids` maps each organization's lower-cased name to its GitHub id, written only by `PATCH /api/sign-in/github` (a server-written key, §7.2: the generic config API refuses it through any path); validation requires lower-case organization names and positive ids, and entries for names not in `orgs` are ignored (that save drops them from `bothy.ui.yaml`).
- Every problem of an enabled provider is reported on its `enabled` key, so a broken combination falls back to off: a missing client id or secret, an empty list (Google; GitHub needs an exact address on the list or `orgs`, so a list of domains alone doesn't do), and a `server.public_url` that is empty or plain http to a non-loopback host. `Set` therefore refuses switching a provider on half-configured, emptying the list (or, for GitHub, leaving only domains on it) while a provider relies on it, and moving `public_url` to plain http while a provider is on.
- Each provider's redirect address is `<public_url origin>/api/auth/oauth/<provider>/callback` (`config.SignInRedirect`); only the scheme and host of `public_url` count.

**Who gets in.** The list is checked again at every sign-in and at every link, so someone taken off it can no longer sign in, even with a linked account. Taking someone off the list stops their next sign-in but does not end the sessions they already have; to sign them out now, turn their account off in People. Addresses and `hd` values from a provider that hold non-ASCII bytes or control characters match nothing; the rest are compared with ASCII lower-casing (`strings.EqualFold` would fold U+212A KELVIN SIGN to `k` and U+017F long s to `s`).

- Google: only a verified email (`email_verified` true or `"true"`) ever passes. An address entry matches when the email equals it, ignoring ASCII case. A domain entry matches only a Google Workspace account of that domain: the id token's `hd` equals the entry (Google's own guidance; `hd` is the Workspace organization's primary domain, so secondary-domain addresses match too). Anyone can create a consumer Google account for a work address, and such an account has no `hd`, so an admin lists that person's address instead.
- GitHub: allowed when any verified address from `GET /user/emails` equals an address entry, or when the person is an active member of a listed organization (`GET /user/memberships/orgs/{org}` with their own token, `state: active`, and `organization.id` equal to `org_ids` under its lower-cased name when there is one). Domain entries never match GitHub accounts: GitHub confirms an address once and never re-checks it, so former staff keep a confirmed company address, and anyone who can read a shared inbox, help desk or mailing-list address at the domain can confirm it. Unverified addresses never count. When the addresses cannot be read and the list holds an exact address, the sign-in fails as "unavailable" rather than "not allowed". A 403 on a membership (OAuth App access restrictions, or SAML SSO the person has not authorized) is `org_access`; a 403 with `X-RateLimit-Remaining: 0`, or a 429, is GitHub limiting requests, which fails as "unavailable". The link keeps the listed address that let the person in, else the primary verified one, so admins can see why they got in.
- GitHub organizations are pinned by numeric id, keyed by name, never by position: saving them in Settings → Security looks up each name without a pin (`GET /orgs/{org}`, one 30-second deadline for the whole save) and writes `github.org_ids` with the names saved; a pin already held under the same name is kept, so saving again never moves it to whoever took a renamed organization's old name (to pin one afresh, remove it, save, then add it back; a pin written in bothy.yaml must be removed there, since the saved settings cannot drop it), and pins of removed names are dropped. A membership whose `organization.id` differs from the pin under its name counts as not a member. A name without a pin (bothy.yaml edited by hand; renaming an entry there leaves its pin under the old name, which never applies to the new one) is still matched by name, and the `auth.sign_in` check names it as not confirmed yet and asks for a save. The lookups go without a token, 60 an hour per address: GitHub limiting them is its own error, and the page says to try again in an hour.
- Nobody matches: refused with `not_allowed`; the sign-in page says "<email or login> isn't on this Bothy's sign-in list. Ask an admin to add you." and never shows the list. The account comes from the refusal cookie (§8.1), read once, and is shown only when it looks like an address or a login; otherwise, and for a crafted link without the cookie, the sentence says "That Google account …".

**Google identity comes from the id token of the token answer; no userinfo call and no JWKS fetch.** OIDC Core 1.0 §3.1.3.7 (6) and Google's OpenID Connect guide let TLS server validation stand in for the signature check when the token comes straight from the token endpoint and the client authenticated with its secret, which is the only place Bothy reads it. Bothy still checks `iss` (`https://accounts.google.com` or `accounts.google.com`), `aud` (a string or an array) contains the client id, `azp` (when present) equals it, `exp` (60 s leeway), `iat` (at most 5 minutes ahead), `nonce` (constant time), `sub` (1–255 characters), `email`, `email_verified` and `hd`. Egress then needs one host, `oauth2.googleapis.com:443`; userinfo would add a host and lose the nonce binding, and JWKS would add a host, key caching and rotation, and RS256 code for no gain on a channel that is already client-authenticated over TLS. Verifying against `www.googleapis.com/oauth2/v3/certs` is a possible later hardening step.

**GitHub** uses an OAuth App: scopes `read:org user:email`, PKCE S256, `allow_signup=false`. The identity is `GET /user` (numeric id as the subject, login, name). The person's token is revoked right after the checks, whatever their outcome (`DELETE /applications/{client_id}/token`, basic auth, in the background, best effort). Hosts: `github.com:443` and `api.github.com:443`.

**Flows** (`auth.Service`: `StartSignIn`, `StartLink`, `FinishSignIn`, `Unlink`):

1. The sign-in page asks `GET /api/auth/methods` and shows "Continue with Google" / "Continue with GitHub" above the password form, only for providers that are on and usable (client secret readable, saved keys unlocked, a usable web address).
2. `POST /api/auth/oauth/{p}/start` stores a flow in memory under the sha256 of a random 32-byte state (10 minutes, single use; at most 32 flows per client, an IPv4 address or an IPv6 /64, which loses its own oldest first, and 1024 in all, where the oldest of all goes only when many clients fill the store) with the PKCE verifier, a nonce, the exact redirect address, the client id the browser is sent to, a validated `next` path and the client that started it; it sets the sign-in cookie to the state and returns the provider's address, which the web app opens. A start from a host or scheme other than `public_url` answers 409 `wrong_address` instead of a round trip that would lose the cookie.
3. The provider sends the browser to the callback. A missing, replayed or expired flow is `expired` (not audited: anyone can hit the callback); a cookie that differs from the state, or a flow of the other provider, is `expired` and audited; `error=access_denied` is `cancelled`. The configuration must still name the same client and redirect address. The client secret is resolved (audited `secret.use`, purpose `sign-in:<p>`), the code traded and the identity checked against the list.
4. Sign-in: a linked account signs in (refused when turned off), its link is touched and the session is the same as after a password sign-in (cookie, CSRF token, `last_login_at`), audited `auth.login` with `method`. Without a link: with `allow_new` in team mode a member account is made and linked in one transaction (a concurrent callback for the same person gets the same account), otherwise `no_account`.
5. Link (Account → Sign-in methods → Connect): started by a signed-in browser session (the cookie and CSRF token; bearer tokens 403) and bound to the sha256 of that session's cookie, since the Strict session cookie never reaches the callback. The callback refuses when that session ended or belongs to someone else (`signed_out`), when the provider account is someone else's (`taken`) or when the person already has another account of that provider (`already_linked`); the same account again is fine. Audited `signin.link`.
6. Unlink (Disconnect) deletes the person's link; the last way into an account without a password is refused (409: an admin gives it a password first). Audited `signin.unlink`.

Failure codes for the web app: `cancelled`, `expired`, `not_allowed` (the account in the refusal cookie), `unverified` (Google hasn't verified the address, checked before the list and given for listed and unlisted addresses alike; the account in the refusal cookie), `org_access`, `no_account`, `disabled`, `taken`, `already_linked`, `signed_out`, `unavailable` (turned off, misconfigured, or the provider failed).

**Accounts made by sign-in** are members. The username comes from the email's local part (Google) or the login (GitHub): lower case, other characters as `-`, at most 27, then `-2` … `-99` or four random characters when taken; an empty result is `user`. The display name is the provider's profile name when it is valid and nobody here goes by it (it is chosen by the person, and approval cards name who asked), else the username. The time zone is the workspace's. `pw_hash` is `"!"`. Disabling an account keeps its links (turning it back on restores sign-in); purge deletes them with the users row.

**Nothing secret is kept.** `sign_ins` (§6, `0011_sign_ins.sql`) holds provider, subject, user, email, login and times. Audit rows name the provider, the reason, the account (`user:<id>` when linked, else `google:<email>` or `github:<login>`) and the client address, never a code, state or token. Provider errors never carry a token. The client secret is write-only in the UI and only sent to its provider.

**Tests and stubs.** `internal/auth/oauth/oauthtest` fakes both providers on loopback (client credentials, single-use codes, the exact redirect address, PKCE S256, id tokens, the GitHub API and revocation) for the unit, httpapi and e2e tests (`NewGoogle`, `NewGitHub`), and for local stubs without a `testing.TB` (`StartGoogle`, `StartGitHub`, with `Listen(addr)` for a fixed port and `Handler()`). `auth.sign_in.<p>.api_base` points Bothy at them; it is locked (file or env only) and accepted only for a loopback host, checked by config validation and again by `oauth.*Endpoints`.

**Out of scope:** generic OIDC (Okta, Entra, Keycloak), GitHub Enterprise Server, SCIM or automatic deprovisioning (existing sessions live until they expire or the account is turned off), mapping groups or teams to roles, admins unlinking other people's providers, passkeys and TOTP.
