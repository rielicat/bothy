# internal/mcp

`mcp.Manager` implements `core.MCP` with the official go-sdk client (SPEC
§2.11, §4.14, §12.2 M7). `New` turns `mcp.servers` into a server table
(re-validating packages and commands: exact versions, integrity, no `npx`,
`uvx`, `bunx` or `pipx run`); `Start` discovers every enabled server once,
records each tool definition as a pin (sha256 over the canonical
`{name,title,description,inputSchema,outputSchema,annotations}`) and registers
only tools whose live hash equals the approved pin, as lazy
`mcp__<server>__<tool>` adapters (sanitised, and over 64 characters
truncated to 55 + `_` + 8 hex of the full name). Unseen definitions are
`pending` (`mcp.pin_pending`, banner `mcp_pins`); a changed definition of an
approved tool unregisters it, is audited as `mcp.changed` with a field diff,
and `Pins` returns both versions for review. `Approve` sets the class (raised,
never lowered, by annotations), `field_map` (`recipient`, `url`, `host` JSON
pointers copied into the Action) and the private flag, or disables a tool.

HTTP servers use Streamable HTTP (falling back to legacy SSE) through
`Egress.SystemClient` for the exact `host:port`; private literals need
`allow_private`, and header `secret:` references resolve with `Vault.Resolve`
per billing user (global only for spaces) into a header transport that never
logs them. Stdio servers run only in sandbox boxes: `Install` (async) builds
`mcp/<server>/<install_id>` in an installer box limited to the package
registry (`npm install|ci --ignore-scripts`, `uv pip compile --generate-hashes`
then `install --require-hashes`), verifies the integrity, records the lock
(TOFU) and the tree hash; discovery re-hashes the tree (drift: install
`drifted`, server disabled, banner `mcp_drifted`) and lists tools from a
short-lived discovery box. Runtime instances are per (server, principal),
started lazily over `Sandbox.Stdio`, read-only tree, server egress list,
`NoGrants`, no secrets, stopped after 10 minutes idle and restarted with 1 s →
60 s backoff. Cancelled calls send `notifications/cancelled`. `Checks`:
`mcp.pending_pins`, `mcp.drifted` (fix: disable).
