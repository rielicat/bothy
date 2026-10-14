# internal/agent

`agent.Runner` implements `core.Runner` (SPEC §4.13, §5). Every session has a
worker goroutine that runs its turns, resumed parked runs and compactions one
at a time and exits after 5 minutes idle. `Submit` validates the request
against its session, checks the daily cap, then starts a turn, merges a DM
message into the 1.2 s coalescing window, injects a steer message into the
active interactive run (same user and provenance only, so an untrusted
participant never rides on the owner's run), or queues it; `agent.queue_depth`
bounds all of these (`ErrBusy`). Runs hold a per-user and then a global
semaphore only while streaming or executing tools and release both while
waiting for an approval or parked.

The loop (`loop.go`) assembles a cache-stable request (`assemble.go`,
`snapshot.go`): tools and S2 (persona, facts, USER.md, MEMORY.md, pins, skills
index) are frozen per epoch in the session row, S1 is the embedded
`CONSTITUTION.md` (full or short by model profile) plus the administrator's
standing rules, and each user turn carries a persisted `<turn-context>` with
time, sender trust, memory deltas, new pins, blocks from `Deps.TurnContext`
hooks (the knowledge module's project notes; a hook's `Commit` runs only once
the turn is stored, its taint legs join the session's), budget and trifecta
warnings and spotlighted client instructions. History is re-ordered so every
tool call is followed by exactly one result (a deterministic "Not executed" result is
synthesized, never stored), untrusted text is wrapped in `<untrusted … id=…>`
blocks whose ids are an HMAC of their position under `meta.agent.spotlight_key`
(stable bytes for prompt caching, unpredictable to the content's author), and
a hard fit keeps every request within window − reserve for the model the
provider facade actually picks. Tool call ids are made unique when the
assistant message is stored, since providers without ids number them per
response. `/v1` ephemeral history keeps only text and attachments, with the
request's provenance. Compaction (`compact.go`) folds everything before the
last `keep_recent_turns` user turns into a validated six-heading summary,
auto-pins standing instructions and falls back to dropping turns when the
summarizer fails twice; an automatic compaction with nothing to fold (one
long turn) leaves the epoch alone, and the always-lazy builtins (`fs_delete`,
`net_allow`, `skill_read`, `schedule_*`) are never promoted into the tools
block.

Tool calls (`tools.go`, `approval.go`) follow SPEC §5.7: `use_tool` unwrap,
availability, `Plan` (behind the registry's schema check), the loop pre-check, `Policy.Decide`,
approvals (allow once/session/always, API deny or bounded wait, park for
scheduled runs with a placeholder result), parallel execution of auto-allowed
non-exec reads (decided with the taint the batch could reach), output capped
head 60% / tail 40% with the full redacted output spilled to
`.bothy/out/<tcl>.txt`, taint, audit. Card grants are created only for
choices the decision offered, and an unexpected choice counts as a denial.
`Resume` treats its argument as a signal only: it re-reads the approval from
the approvals store, flips the parked run to queued in one store transaction
and runs the stored Action exactly once, provided it matches the planned tool
call, appending the outcome as an `<approved-action>` message. `Stop` never
waits on the run: it cancels, kills the session box, cancels approvals, clears
the queue (including resumes not yet handed to the worker) and abandons a run
that has not exited 5 s later. The kill switch persists in `meta.paused`;
`StopAll` covers every session with queued work and a queued resume that
starts while paused stops instead. `Recover` (called by cmd/bothy before any
intake, and again, as a no-op, by `Run`) marks interrupted runs at boot,
cancels their approvals, notifies their chats and resumes parked runs whose
approval was decided while the gateway was down; at shutdown, runs it
interrupts cancel their own approvals. A zero field in requested limits means
the trigger's default, never unlimited.

Streamed text (`run.delta`, `run.thinking`) is coalesced to 50 ms and tool
progress (`run.tool_output`) to 100 ms, and each flush publishes only what
`streamCut` releases: text up to whitespace, short of any tail a registered
secret could still complete (`PartialAt` of the vault's redactor), so a
secret or link split across flushes is redacted and neutralized whole on the
bus and in the sink. An exposed session's sink gets links neutralized by the
same rules as the channel router's (`core.NewLinkFilter`: bare domains and
IPv6 hosts included, code left alone),
since a `/v1` client renders the text itself. A resumed heartbeat run's sink holds back a
`HEARTBEAT_OK` answer like the scheduler's.

The embedded `CONSTITUTION.md`, `CONSTITUTION.short.md` and default `SOUL.md`
(written to `<data>/<agent.persona_file>` at construction when missing) are
product prompts: changing either constitution requires bumping
`constitutionVersion` (a test pins their hash), which is recorded in
`meta.constitution_version`.
