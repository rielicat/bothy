package sbxinit

import (
	"fmt"
	"io"
	"os"
)

// Exit codes beyond the child's own, following coreutils timeout(1).
const (
	exitTimeout  = 124
	exitInternal = 125
	exitNoExec   = 126
	exitNotFound = 127
)

// Paths inside a box (SPEC §2.6, §2.8).
const (
	egressSocket = "/run/egress/proxy.sock"
	relayAddr    = "127.0.0.1:3128"
)

const usage = `usage: sbx-init                                  PID 1: reap, relay 127.0.0.1:3128
       sbx-init [--relay <sock>] run --timeout <d> [--kill-after <d>] -- argv...
       sbx-init killall | count
       sbx-init selftest [--runtime runc|runsc] [--canary <nonce>]
`

// Main implements `sbx-init [run|killall|count|selftest] …`; returns the exit code.
func Main(args []string) int {
	return dispatch(args, os.Stdout, os.Stderr)
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return initMain(stderr)
	}
	if args[0] == "--relay" {
		if len(args) < 3 || args[2] != "run" {
			fmt.Fprint(stderr, usage)
			return exitInternal
		}
		stop, err := startRelay(relayAddr, args[1])
		if err != nil {
			fmt.Fprintln(stderr, "sbx-init: relay:", err)
			return exitInternal
		}
		defer stop()
		args = args[2:]
	}
	switch args[0] {
	case "run":
		return runMain(args[1:], stderr)
	case "killall":
		return killallMain(stdout, stderr)
	case "count":
		return countMain(stdout, stderr)
	case "selftest":
		return selftestMain(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return exitInternal
	}
}
