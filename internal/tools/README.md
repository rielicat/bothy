# internal/tools (M4)

`tools.Registry` implements `core.ToolRegistry`; `tools.Builtins` returns the
workspace and messaging builtins.

**Registry.** `Register` validates every manifest (name, description,
source `builtin`/`mcp:<server>`/`skill:<name>`, title, side-effect class,
triggers, role, taint bits) and compiles the JSON Schema with
`google/jsonschema-go` (object type, known type names, no remote `$ref`); the
stored spec has a canonical schema and `Hash` = sha256 of the canonical
`ToolDef`. `ForRun` filters by trigger, role, principal kind (restricted
spaces get `SpaceOK` read tools only), sandbox (`Exec`), kill switch
(read-only) and `core.Gated`, then picks the core set of the model profile
(standard: manifests marked `Core`; compact and degraded: the fixed lists of
SPEC §5.6), adds `tool_search`/`use_tool` when lazy tools remain, and sorts by
name, so identical inputs give identical bytes. `Search` ranks available
tools by keyword hits in name, description and title. `Get` returns the tool
behind a wrapper that validates arguments against the compiled schema before
`Plan` (SPEC §5.7 b; the runner has no validator of its own) and normalises
the planned Action (canonical payload, fingerprint, side effect never below
the manifest). The registry owns `tool_search` (a read tool whose `Exec`
searches with the run's `Selection`) and the `use_tool` schema entry, which
the runner unwraps; planning or executing `use_tool` itself is an error.

**Builtins.** `fs_read` (line ranges, 256 KB cap, binary detection, taints
the session Private), `fs_write` (create/overwrite/append, Action carries an
exact unified diff, quota checked through `Sandbox.Usage`, atomic replace
that keeps the file's permission bits),
`fs_list`, `fs_delete` (irreversible, lazy, diff of what is removed) and
`message` (`core.Gated` on a channel chat, no recipient parameter: it replies
only in `env.Chat` and refuses to send a planned Action to any other chat).
All file access goes through `os.Root` on the principal's workspace, so `..`,
absolute paths and symlinks leading outside are refused at both Plan and Exec,
and errors name paths relative to the workspace, never the host directory.
