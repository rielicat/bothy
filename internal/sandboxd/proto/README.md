# internal/sandboxd/proto

The fixed-schema wire format between the gateway and sandboxd (SPEC §2.5),
plus the validators both sides apply: box key, workspace and MCP tree id
regexes, argv/env/cwd/stdin limits, the forbidden environment keys, limit
clamping, the derived egress subpath (`egress/` + 32 hex of sha256(key)) and the
self-test check names. It imports only the standard library, so `sbx-init`
can share the check names without growing.
