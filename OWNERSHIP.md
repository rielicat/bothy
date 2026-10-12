# Ownership

Bothy v0.1 is built by twelve module agents working in parallel on disjoint
paths, plus the lead, who owns the skeleton and integrates (SPEC §12.2). Each
module may change only the paths listed for it. Everything not listed belongs
to the lead (`S`): `go.mod`, `internal/core`, `internal/config`,
`internal/events`, `internal/httpapi/dto`, the migrations, `internal/testkit`,
`cmd/`, `deploy/` (except `deploy/sandbox/`), CI, scripts and docs. Changes
there go through an interface change request (see CONTRIBUTING.md).

| Module | Scope | Owns (exclusive) |
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
| S | Lead | everything else |

## How the check works

`scripts/check-ownership.sh` reads the block below. A module branch is named
`m<N>/<topic>` (for example `m3/run-loop`); CI derives the module from the
branch name, or from `BOTHY_MODULE` / `--module`, and fails when the branch
changes a path outside that module's entry. Any other branch is a lead branch
and may touch everything. The script also fails if two modules claim the same
tracked file.

Pattern syntax: `dir/**` matches everything below `dir`; otherwise `*` matches
within one path segment; a leading `!` excludes paths from that module's
positive patterns.

```ownership
M1   internal/store/**
M1   !internal/store/migrations/0001_init.sql
M1   internal/vault/**
M1   internal/audit/**
M2   internal/provider/**
M2   tools/catalog-gen/**
M3   internal/agent/**
M3   internal/budget/**
M4   internal/policy/**
M4   internal/tools/**
M5   internal/sandbox/**
M5   internal/sandboxd/**
M5   internal/sbxinit/**
M5   deploy/sandbox/**
M6   internal/egress/**
M7   internal/mcp/**
M7   internal/skills/**
M8   internal/memory/**
M8   internal/scheduler/**
M9   internal/channel/*.go
M9   internal/channel/README.md
M9   internal/channel/telegram/**
M10  internal/channel/discord/**
M10  internal/channel/slack/**
M11  internal/auth/**
M11  internal/httpapi/**
M11  !internal/httpapi/dto/**
M11  internal/doctor/**
M11  internal/webui/*.go
M11  internal/webui/README.md
M12  web/**
M12  !web/src/lib/api/types.gen.ts
```
