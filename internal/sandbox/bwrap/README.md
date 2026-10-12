# internal/sandbox/bwrap

The native Linux backend (SPEC §2.12). Every exec is a fresh bubblewrap jail:
`--clearenv` plus the box environment, all namespaces unshared with nested user
namespaces disabled, uid/gid 10001, no capabilities, read-only `/usr`, the
session workspace bound at `/workspace`, the box's egress socket directory
read-only at `/run/egress`, and the bothy binary itself running
`sbx-init --relay … run` inside. A seccomp program built with
`go-seccomp-bpf` (never loaded into the gateway) is handed to bwrap on fd 3:
default allow, EPERM for the dangerous syscalls and socket families, and a
kill for any foreign architecture. `exec.Cmd.Env` is always set explicitly so
the gateway's environment never leaks. This is the only package allowed to
import `os/exec`. `jail_test.go` (`-tags bwrap`, Linux with bwrap) checks the
clean environment, relay-only networking, denied ptrace, timeouts and Kill;
`MODE=bwrap deploy/sandbox/test/acceptance.sh` runs it in a container.
