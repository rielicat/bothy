# internal/sbxinit

`sbx-init`, PID 1 and exec wrapper inside every tool box (SPEC §2.7, §2.10;
module M5; standard library only). With no arguments it reaps orphans, relays
`127.0.0.1:3128` to the box's egress socket `/run/egress/proxy.sock` and exits
on SIGTERM. `run --timeout <d> [--kill-after 5s] -- argv…` starts the command
as a new session with umask 002 and the box rlimits (NOFILE 1024, FSIZE 1 GiB,
CORE 0, CPU = timeout; deliberately no NPROC), copies its output and, on
timeout, SIGTERMs then SIGKILLs the process group and exits 124; output pipes
are released shortly after the command exits so background processes cannot
hold an exec open. `killall` is `kill(-1, SIGKILL)` (which spares PID 1),
`count` counts live processes from `/proc`, and `selftest` prints the canary
checks as JSON. `--relay <sock> run …` is the bwrap mode, where it is not PID 1.
