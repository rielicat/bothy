# internal/sandbox

The gateway side of tool execution (SPEC §2, §4.7; module M5). `sandbox.Client`
implements `core.Sandbox` in front of one backend chosen at boot by
`sandbox.backend`: **sandboxd** (the compose sidecar, spoken to over
`/run/bothy/sandboxd.sock` with the fixed `proto` schema), **bwrap** (native
Linux, see `bwrap/`) or **none** (every exec method returns
`core.ErrNoSandbox`; nothing ever runs on the host). `auto` prefers a sandboxd
socket, then bwrap, then none, and keeps watching for the socket. `Ensure`
creates the directories a box mounts (`ws/<id>` and MCP trees 2770 group
10001, `skills/`, `egress/<h>` 0750) and opens the box's egress socket before
creating the box; `Exec` and `Stdio` recreate a box sandboxd lost (reaped,
evicted, killed and removed) from the last `Ensure`. `Run` polls health and
publishes `sandbox.status` events, `no_sandbox` / `sandbox_not_isolated`
banners (an admin broadcast plus one per member user principal, since members
do not receive `sandbox.status`) and the `sandbox.selftest_failed` audit entry,
destroys boxes idle past `sandbox.idle_timeout` (closing their egress sockets)
and scans workspace sizes for `Usage`. The package also provides the `shell`
and `python` tools and the `sandbox.*` / `docker.socket_access` doctor checks.
`docker_test.go` (`-tags docker`) and `runsc_test.go` (`-tags runsc`) are the
acceptance suites against a real engine; run them with
`deploy/sandbox/test/acceptance.sh`.
