# internal/memory

`memory.Service` implements `core.Memory` (SPEC §4.14, §5.6) with the `memory`
and `session_search` tools and the `memory.over_cap` doctor check (owner: M8).
Each user principal has `MEMORY.md`, `USER.md` and `HEARTBEAT.md` under
`<data>/memory/<principal>/`, written atomically (temp file, fsync, rename;
0600 files in a 0700 directory) under one mutex that also covers the
`memory_events` row and the snapshot read, so concurrent `Apply` calls never
lose an update and `Snapshot().LastID` always matches the files returned; if
the event row cannot be stored the file is rolled back. Agent ops edit one
entry (a non-blank line, compared trimmed) at a time: `add` appends, `replace`
and `remove` need the exact entry, and an op that would grow a file past
`memory.*_max_bytes` fails with the bytes still free (shrinking ops always
pass, so a file left over a lowered cap can be trimmed). A near miss in the
tool's `Plan` names candidate entries (and quotes `HEARTBEAT.md`) only when the
session is already tainted private, because that error text bypasses policy
and taint tracking. The web editor
writes whole files guarded by a sha256 ETag (`core.ErrConflict` when stale).
`DeltasSince` renders the net `- entry` / `+ entry` change of `MEMORY.md` and
`USER.md` since an event id, so the turn-context stays bounded by the caps.
Every change publishes `memory.updated` and is audited (`memory.write`,
`memory.edit`; never the content). The `memory` tool dry-runs in `Plan` so a
bad op fails before any approval card and records provenance `untrusted` when
the session holds untrusted content (F7 gates it); `session_search` searches
only the run's own principal and marks the result untrusted when any hit is.
Space principals have no memory: both tools are hidden from them (not
`SpaceOK`, and `core.Gated` off for "standard" spaces too), `Snapshot` and
`DeltasSince` are empty, and the other methods return `core.ErrForbidden`.
`Purge` removes a principal's memory directory for user purges.
