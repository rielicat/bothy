//go:build unix

package sbxinit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// drainGrace is how long run keeps copying output after the command exits.
// Background processes may hold the pipes open forever; once the grace is over
// the pipes are closed so the exec ends (those processes get EPIPE).
const drainGrace = 250 * time.Millisecond

type runOpts struct {
	timeout   time.Duration
	killAfter time.Duration
	argv      []string
}

func parseRunArgs(args []string) (runOpts, error) {
	o := runOpts{killAfter: 5 * time.Second}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--":
			o.argv = args[i+1:]
			if len(o.argv) == 0 || o.argv[0] == "" {
				return o, errors.New("missing command after --")
			}
			if o.timeout <= 0 {
				return o, errors.New("--timeout is required")
			}
			return o, nil
		case "--timeout", "--kill-after":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a duration", a)
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return o, fmt.Errorf("%s: invalid duration %q", a, args[i+1])
			}
			if a == "--timeout" {
				o.timeout = d
			} else {
				o.killAfter = d
			}
			i++
		default:
			return o, fmt.Errorf("unknown argument %q", a)
		}
	}
	return o, errors.New("missing -- before the command")
}

func runMain(args []string, stderr io.Writer) int {
	o, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "sbx-init run:", err)
		return exitInternal
	}
	return runChild(o, os.Stdin, os.Stdout, os.Stderr)
}

// runChild runs o.argv as the leader of a new session with the box rlimits,
// copies its output and enforces the timeout on its whole process group.
func runChild(o runOpts, stdin, stdout, stderr *os.File) int {
	syscall.Umask(0o002)
	if err := setRunLimits(o.timeout, o.killAfter); err != nil {
		fmt.Fprintln(stderr, "sbx-init run: rlimits:", err)
		return exitInternal
	}
	path, err := lookPath(o.argv[0], os.Getenv("PATH"))
	if err != nil {
		fmt.Fprintf(stderr, "sbx-init: %s: %v\n", o.argv[0], err)
		return execExitCode(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(stderr, "sbx-init run:", err)
		return exitInternal
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		fmt.Fprintln(stderr, "sbx-init run:", err)
		return exitInternal
	}
	proc, err := os.StartProcess(path, o.argv, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{stdin, outW, errW},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		fmt.Fprintf(stderr, "sbx-init: %s: %v\n", o.argv[0], err)
		return execExitCode(err)
	}

	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); _, _ = io.Copy(stdout, outR) }()
	go func() { defer copies.Done(); _, _ = io.Copy(stderr, errR) }()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	waited := make(chan *os.ProcessState, 1)
	go func() {
		st, _ := proc.Wait()
		waited <- st
	}()

	pgid := proc.Pid
	timer := time.NewTimer(o.timeout)
	defer timer.Stop()
	var st *os.ProcessState
	timedOut := false
wait:
	for {
		select {
		case st = <-waited:
			break wait
		case s := <-sigs:
			if sig, ok := s.(syscall.Signal); ok {
				_ = syscall.Kill(-pgid, sig)
			}
		case <-timer.C:
			timedOut = true
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			grace := time.NewTimer(o.killAfter)
			select {
			case st = <-waited:
			case <-grace.C:
			}
			grace.Stop()
			// Members that ignored SIGTERM must not outlive a timed-out command.
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			if st == nil {
				st = <-waited
			}
			break wait
		}
	}
	drain(&copies, drainGrace, outR, errR)
	outR.Close()
	errR.Close()
	if timedOut {
		return exitTimeout
	}
	return exitStatus(st)
}

func drain(wg *sync.WaitGroup, grace time.Duration, pipes ...*os.File) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		for _, p := range pipes {
			p.Close()
		}
		<-done
	}
}

func exitStatus(st *os.ProcessState) int {
	if st == nil {
		return exitInternal
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}

func execExitCode(err error) int {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotFound) {
		return exitNotFound
	}
	return exitNoExec
}

// setRunLimits applies the exec rlimits of SPEC §2.7. There is deliberately no
// RLIMIT_NPROC: it counts every process of the uid across boxes, so the pids
// cgroup bounds process counts instead.
func setRunLimits(timeout, killAfter time.Duration) error {
	cpu := uint64(math.Ceil(timeout.Seconds()))
	want := []struct {
		res      int
		cur, max uint64
	}{
		{syscall.RLIMIT_NOFILE, 1024, 1024},
		{syscall.RLIMIT_FSIZE, 1 << 30, 1 << 30},
		{syscall.RLIMIT_CORE, 0, 0},
		{syscall.RLIMIT_CPU, cpu, cpu + uint64(math.Ceil(killAfter.Seconds()))},
	}
	for _, w := range want {
		var old syscall.Rlimit
		if err := syscall.Getrlimit(w.res, &old); err != nil {
			return err
		}
		lim := syscall.Rlimit{Cur: min(w.cur, old.Max), Max: min(w.max, old.Max)}
		if err := syscall.Setrlimit(w.res, &lim); err != nil {
			return err
		}
	}
	return nil
}

var errNotFound = errors.New("command not found")

// lookPath resolves file like a shell would; os/exec is not allowed outside
// the bwrap package.
func lookPath(file, pathEnv string) (string, error) {
	if strings.Contains(file, "/") {
		return file, executable(file)
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, file)
		if executable(p) == nil {
			return p, nil
		}
	}
	return "", errNotFound
}

func executable(p string) error {
	fi, err := os.Stat(p) //nolint:gosec // G703: resolving PATH is the point
	if err != nil {
		return err
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}
