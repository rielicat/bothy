package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHealthcheckSandboxd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandboxd runs only on Linux; unix sockets are enough to test the probe")
	}
	dir, err := os.MkdirTemp("", "hc") // short: unix socket paths are limited to ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "sandboxd.sock")
	t.Setenv("BOTHY_SBX_SOCKET", sock)
	// The image's HEALTHCHECK probes the gateway's HTTP port, which sandboxd
	// does not serve; the sandboxd variant must not depend on it.
	t.Setenv("BOTHY_SERVER__LISTEN", "127.0.0.1:1")
	ctx := context.Background()

	if err := healthcheck(ctx, []string{"sandboxd"}); err == nil {
		t.Fatal("healthy without a socket")
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := healthcheck(ctx, []string{"sandboxd"}); err != nil {
		t.Fatalf("listening sandboxd reported unhealthy: %v", err)
	}
	if err := healthcheck(ctx, []string{"bogus"}); err == nil {
		t.Fatal("unknown argument accepted")
	}
}
