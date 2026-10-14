# internal/sbxinit

`sbx-init`, PID 1 and exec wrapper inside every tool box (SPEC §2.7, §2.10;
module M5; standard library only). With no arguments it reaps orphans, relays
`127.0.0.1:3128` to the box's egress socket `/run/egress/proxy.sock` and exits
on SIGTERM. `run --timeout <d> [--kill-after 5s] -- argv…` starts the command
as a new session with umask 002 and the box rlimits (NOFILE 1024, FSIZE 1 GiB,
CORE 0, CPU = timeout; deliberately no NPROC), copies its output and, on
timeout, SIGTERMs then SIGKILLs the process group and exits 124. Shortly
after the command exits, output pipes still held by background processes are
handed to a detached `sbx-init drain`, so they cannot hold the exec open and
keep running (writes still succeed) until `/stop`, `killall` or box reaping. `killall` is `kill(-1, SIGKILL)` (which spares PID 1),
`count` counts live processes from `/proc`, `selftest` prints the canary
checks as JSON, and `mountids <path>…` prints each directory's `dev:ino` for
sandboxd's bind-mount check (a symlink or non-directory fails it). `--relay <sock> run …` is the bwrap mode, where it is not PID 1.
