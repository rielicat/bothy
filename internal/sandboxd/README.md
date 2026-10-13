# internal/sandboxd

`bothy sandboxd`, the sidecar that alone holds the container runtime socket
(SPEC §2.4–§2.10; module M5). It serves the fixed `/v1` API of `proto` on
`/run/bothy/sandboxd.sock` (0660, the run directory's group), accepting only
peers whose `SO_PEERCRED` uid is `BOTHY_SBX_GATEWAY_UID`, decoding every body
with `DisallowUnknownFields` and validating ids against the `proto` regexes.
Boxes are built only from the compiled-in spec in `spec.go` (network none,
read-only rootfs, no capabilities, no-new-privileges, uid 10001, pids/memory/CPU
limits, volume subpaths derived from the request's ids) and images pinned by
digest in `BOTHY_SBX_IMAGES`. Execs run under `sbx-init run`; Kill is
`sbx-init killall` with a container kill-and-remove fallback. A stdio stream
whose gateway side closed is cut after a short grace, so a server that ignores
stdin EOF cannot hold the box. At start and every `BOTHY_SBX_SELFTEST_EVERY` a
canary box runs `sbx-init selftest` (every 30 s instead while the canary cannot
even start, e.g. the engine is unreachable); isolation failures refuse every
new box and exec, and the runtime is chosen per `BOTHY_SBX_RUNTIME` (runsc only
when its unix-socket canary passes). Box containers are named
`<run volume>-<key>`, so installs sharing an engine never replace or remove
each other's boxes (the canary and MCP installer keys are the same
everywhere). It removes this install's orphaned boxes
at start, reaps idle and over-age boxes, and evicts the least recently used
idle box at `BOTHY_SBX_MAX_BOXES`; a box being removed takes no new exec, and a
create for its key waits for the removal. `engine/` is
the hand-written Engine API client, `proto/` the wire types and validators, and
`enginetest/` an in-memory engine for tests.
