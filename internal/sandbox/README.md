# internal/sandbox

The gateway side of tool execution (SPEC §2, §4.7; module M5). `sandbox.Client`
implements `core.Sandbox` in front of one backend chosen at boot by
`sandbox.backend`: **sandboxd** (the compose sidecar, spoken to over
`/run/bothy/sandboxd.sock` with the fixed `proto` schema), **bwrap** (native
Linux, see `bwrap/`) or **none** (every exec method returns
`core.ErrNoSandbox`; nothing ever runs on the host). `auto` prefers a sandboxd
socket, then bwrap, then none, and keeps watching for the socket; a native
none status keeps bwrap's reason and fix, while the container image (no bwrap)
points at the compose deployment. Fix texts never name `deploy/compose.yaml`,
which keeps digest placeholders and does not exist in an install.sh
deployment. The bwrap backend creates the run directory (0750; config falls
back from `/run/bothy` to `$XDG_RUNTIME_DIR/bothy` or `<data_dir>/run` on
native installs) and is unavailable, with a fix line, when the gateway cannot
write it or its socket paths would be too long. `Ensure`
creates the directories a box mounts (`ws/<id>` and MCP trees 2770 group
10001, `skills/`, `egress/<h>` 0750) and opens the box's egress socket before
creating the box; `Exec` and `Stdio` bring back a box that is gone (reaped or
evicted by sandboxd, killed and removed, or idle-reaped here) by repeating the
last `Ensure` for its key, whose spec is kept for a day after an idle reap.
`Run` polls sandboxd's health (an auto-mode none backend only watches for the
socket; none and bwrap are probed once) and publishes `sandbox.status` events,
`no_sandbox` / `sandbox_not_isolated` banners (an admin broadcast plus one per
member user principal, since members do not receive `sandbox.status`; synced
on every status change and, for users created or promoted since, once a
minute, without reading the users table while no banner is shown) and the
`sandbox.selftest_failed` audit entry, destroys boxes idle past
`sandbox.idle_timeout` (closing their egress sockets) and scans workspace sizes
for `Usage`. The package also provides the `shell` and `python` tools and the
`sandbox.*` / `docker.socket_access` doctor checks (`sandbox.bwrap_limits`
notes that bwrap jails have rlimits but no memory or pids limits).
`docker_test.go` (`-tags docker`) and `runsc_test.go` (`-tags runsc`) are the
acceptance suites against a real engine; run them with
`deploy/sandbox/test/acceptance.sh`.
