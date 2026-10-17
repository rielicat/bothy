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
Space principals have no memory: `session_search` is hidden from them (not
`SpaceOK`, and `core.Gated` off for "standard" spaces too), and `memory` is
offered to a standard channel only to propose a TEAM change (its own files
get "Channels have no memory of their own…"; restricted channels get
neither); `Snapshot` and `DeltasSince` are empty, and the other methods
return `core.ErrForbidden`.
`Purge` removes a principal's memory directory for user purges.

TEAM.md, what Bothy remembers for everyone, is one file at
`<data>/memory/team/TEAM.md` (cap `memory.team_max_bytes`; `team` can't be a
principal's folder, since principal ids start with `usr_` or `spc_`, so
`Purge` never reaches it). It shares the mutex, atomic write and rollback,
with its history in `team_memory_events`. `ReadTeam` serves every run's S1
and any signed-in user; `WriteTeam` (the web editor) refuses anyone but an
enabled admin, looked up in the store; `Apply` refuses TEAM. The only agent
path is the `memory` tool with `file: "TEAM"`: `Plan` returns an admin-class
action (an admin approves it, floor F3), and `Exec` writes only after
checking the stored approval itself (allowed, allow_once, admin-only, this run,
this fingerprint, decided by an enabled admin) through the unexported
`applyTeam`; the store takes one change per approval, so a replayed call
writes nothing.
Team changes publish `memory.updated` as an admin broadcast (no principal, no
run) and are audited `memory.write` / `memory.edit` with `scope: team`, the
approval and approver, never the content.
