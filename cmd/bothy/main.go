// Command bothy is the single binary: gateway, sandbox sidecar and CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bothyhq/bothy/internal/sandboxd"
	"github.com/bothyhq/bothy/internal/sbxinit"
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
  vault            vault admin: rotate-kek (gateway stopped) | status
  config           check | defaults | effective
  audit            verify | archive --before <date> (gateway stopped)
  backup           write a consistent backup to <data>/backups
  healthcheck      exit 0 when /healthz answers (container HEALTHCHECK)
  version          print version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	code := 0
	switch cmd {
	case "serve":
		err = serve(ctx, args)
	case "sandboxd":
		err = sandboxd.Main(ctx, args, os.Environ)
	case "sbx-init":
		os.Exit(sbxinit.Main(args))
	case "doctor":
		code, err = doctorCmd(ctx, args, false)
	case "security-audit":
		code, err = doctorCmd(ctx, args, true)
	case "setup-token", "user", "vault", "config", "audit", "backup":
		err = adminCmd(ctx, cmd, args)
	case "healthcheck":
		err = healthcheck(ctx)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bothy:", err)
		os.Exit(1)
	}
	os.Exit(code)
}
