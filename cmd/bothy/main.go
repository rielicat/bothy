// Command bothy is the single binary: gateway, sandbox sidecar and CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/rielicat/bothy/internal/sandboxd"
	"github.com/rielicat/bothy/internal/sbxinit"
)

var version = "dev" // -ldflags "-X main.version=…"

const usage = `usage: bothy <command> [flags]

commands:
  serve            run the gateway (default)
  sandboxd         run the sandbox sidecar (needs the container runtime socket)
  sbx-init         sandbox PID 1 / exec wrapper (inside tool boxes and bwrap)
  doctor           run health checks            [--json]
  security-audit   run security checks          [--fix] [--json]
  setup-token      print a fresh one-time setup token (only while no owner exists)
  user             user admin: reset-password <name> | list
  vault            secrets: set <name> (value on stdin) | list | delete <name> | status | rotate-kek (gateway stopped)
  config           check [file] | defaults | effective
  audit            verify | archive --before <date> (gateway stopped)
  backup           write a consistent backup to <data>/backups
  healthcheck      exit 0 when /healthz answers (container HEALTHCHECK) [sandboxd]
  version          print version

The data directory is $BOTHY_STORAGE__DATA_DIR (default /data).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run dispatches one invocation and returns the exit code: 0 ok (and help),
// 1 failure, 2 usage. Usage errors are caught before any state is opened, so a
// mistyped command never creates a data dir, database or KEK.
func run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	cmd, args := "serve", argv
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	code := 0
	switch cmd {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		err = serve(ctx, args)
	case "sandboxd":
		err = sandboxd.Main(ctx, args, os.Environ)
	case "sbx-init":
		return sbxinit.Main(args)
	case "doctor":
		code, err = doctorCmd(ctx, args, false)
	case "security-audit":
		code, err = doctorCmd(ctx, args, true)
	case "setup-token", "user", "vault", "config", "audit", "backup":
		if slices.ContainsFunc(args, isHelpFlag) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		err = runAdmin(ctx, cmd, args, stdout, stderr)
	case "healthcheck":
		err = healthcheck(ctx, args)
	case "version":
		fmt.Fprintln(stdout, version)
	default:
		fmt.Fprintf(stderr, "bothy: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprint(stderr, usage)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "bothy:", err)
		return 1
	}
	return code
}

func isHelpFlag(s string) bool { return s == "-h" || s == "-help" || s == "--help" }
