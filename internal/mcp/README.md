# internal/mcp

`mcp.Manager` implements `core.MCP` with the official go-sdk client (SPEC
§2.11, §4.14, §12.2 M7). `New` turns `mcp.servers` into a server table
(re-validating packages and commands: exact versions, integrity, no `npx`,
`uvx`, `bunx`, `pipx run` or their npm/pnpm/yarn/bun/uv equivalents); `Start`
discovers every enabled server once (a failed discovery is retried with 1 to
30 minute backoff), records each tool definition as a pin (sha256 over the
canonical `{name,title,description,inputSchema,outputSchema,annotations}`)
and registers only tools whose live hash equals the approved pin (one
`ReplaceSource` per change), as lazy `mcp__<server>__<tool>` adapters unless
the server's `core` globs pin them into the core block (sanitised, and over 64 characters
truncated to 55 + `_` + 8 hex of the full name). Unseen definitions are
`pending` (`mcp.pin_pending`, banner `mcp_pins`); a changed definition of an
approved tool unregisters it, is audited as `mcp.changed` with a field diff,
and `Pins` returns both versions for review. `Approve` sets the class (raised,
never lowered, by annotations), `field_map` (`recipient`, `url`, `host` JSON
pointers copied into the Action; values the Action cannot carry, and keys
that differ from a mapped one only in case, are refused) and the private
flag, or disables a tool; an approval naming the reviewed `hash` fails with
`ErrConflict` once the latest pin differs. Pins outlive a server leaving the
config (an entry an edit made invalid comes back approved); only `Forget`, an
admin's removal through the API, deletes them. Adapters re-check on every call that their
approved definition is still registered and the server ready, so a call held
across an approval wait never reaches a tool disabled or changed meanwhile.

HTTP servers use Streamable HTTP (falling back to legacy SSE) through
`Egress.SystemClient` for the exact `host:port`; private literals need
`allow_private` (a name egress refuses for resolving privately gets the same
`private_address` code), plain http that sends a key asks egress for
`PrivateOnly`, and header `secret:` references resolve with `Vault.Resolve`
per billing user (global only for spaces) into a header transport that never
logs them. Discovery resolves them globally, then with the personal keys of
the admin `DiscoverWith` names for that exact config entry (meta
`mcp.discovery_key.<server>`); its session only lists tools, so that key
never makes anyone else's calls. Non-streaming responses are capped at 16 MB and definitions at
64 KB. Stdio servers run only in sandbox boxes, each removed before it is
created so a stale box is never reused: `Install` (async) builds
`mcp/<server>/<install_id>` in an installer box limited to the package
registry (`npm install|ci --ignore-scripts`, `uv pip compile --generate-hashes`
then `install --require-hashes`), verifies the integrity, records the lock
(TOFU; a reinstall must reproduce it exactly) and the tree hash. A failed
install is retried with the discovery backoff, and at once when a discovery
finds it failed (start, re-enable, sandbox back), but not when the package or
lock doesn't match its pins. Discovery re-hashes the tree (drift: install
`drifted`, server disabled, banner `mcp_drifted`) and lists tools from a short-lived discovery box. Runtime
instances are per (server, principal), started lazily over `Sandbox.Stdio`,
read-only tree, server egress list, `NoGrants`, no secrets, stopped after 10
minutes idle and restarted with 1 s → 60 s backoff. Cancelled calls send
`notifications/cancelled`. `Checks`: `mcp.pending_pins`, `mcp.drifted` (fix:
disable).
