//go:build unix

package sbxinit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

// initMain is PID 1 of a box. HostConfig.Init is false so that this process,
// and only it, survives `sbx-init killall`. The kernel ignores signals PID 1
// has no handler for, so SIGTERM is handled explicitly.
func initMain(stderr io.Writer) int {
	syscall.Umask(0o002)
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	stop, err := startRelay(relayAddr, egressSocket)
	if err != nil {
		// The box still works without egress; clients fail closed.
		fmt.Fprintln(stderr, "sbx-init: relay:", err)
	} else {
		defer stop()
	}
	reap()
	for s := range sigs {
		switch s {
		case syscall.SIGCHLD:
			reap()
		case syscall.SIGTERM, syscall.SIGINT:
			return 0
		}
	}
	return 0
}

// reap collects every exited child; SIGCHLD coalesces, so one signal may
// stand for many orphans re-parented to PID 1.
func reap() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func killallMain(stdout, stderr io.Writer) int {
	n, err := countProcs("/proc", os.Getpid(), os.Getuid())
	if err != nil {
		fmt.Fprintln(stderr, "sbx-init killall:", err)
		return exitInternal
	}
	// kill(-1) spares PID 1 and the caller, so the box survives.
	if err := syscall.Kill(-1, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintln(stderr, "sbx-init killall:", err)
		return 1
	}
	fmt.Fprintln(stdout, strconv.Itoa(n))
	return 0
}

func countMain(stdout, stderr io.Writer) int {
	n, err := countProcs("/proc", os.Getpid(), -1)
	if err != nil {
		fmt.Fprintln(stderr, "sbx-init count:", err)
		return exitInternal
	}
	fmt.Fprintln(stdout, strconv.Itoa(n))
	return 0
}
