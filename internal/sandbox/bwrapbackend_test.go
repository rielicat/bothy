package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandbox/bwrap"
)

// bwrapWorks stands in for a host where bubblewrap passes its probe.
func bwrapWorks(context.Context) (bwrap.Options, string, []core.SelfTestCheck, error) {
	return bwrap.Options{Bwrap: "/usr/bin/bwrap", Self: "/usr/local/bin/bothy"}, "bubblewrap 0.11.0",
		[]core.SelfTestCheck{{Name: "bwrap", OK: true}, {Name: "userns", OK: true}}, nil
}

func bwrapMissing(context.Context) (bwrap.Options, string, []core.SelfTestCheck, error) {
	return bwrap.Options{}, "", []core.SelfTestCheck{{Name: "bwrap", Detail: "bwrap not found in PATH"}},
		errors.New("bwrap (bubblewrap >= 0.8) is not installed")
}

// A native install's run directory does not exist yet; boxes must still get
// their egress directories.
func TestBwrapCreatesTheRunDir(t *testing.T) {
	ctx := context.Background()
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none" }})
	run := filepath.Join(shortTempDir(t), "bothy")
	e.c.svc.run = run
	e.c.svc.be = newBwrapBackend(ctx, bwrapWorks, e.work, run)
	if st := e.c.svc.refresh(ctx); !st.Available {
		t.Fatalf("status = %+v", st)
	}
	if err := e.c.Ensure(ctx, e.spec(key1)); err != nil {
		t.Fatalf("Ensure with a fresh run directory: %v", err)
	}
	fi, err := os.Stat(run)
	if err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatalf("run directory = %v, %v", fi, err)
	}
}

func TestBwrapNeedsAWritableRunDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	ro := filepath.Join(shortTempDir(t), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	for name, run := range map[string]string{"cannot create": filepath.Join(ro, "bothy"), "not writable": ro} {
		b := newBwrapBackend(context.Background(), bwrapWorks, t.TempDir(), run)
		if b.st.Available || !strings.Contains(b.st.Reason, "run directory") || !strings.Contains(b.st.Fix, "storage.run_dir") {
			t.Errorf("%s: status = %+v", name, b.st)
		}
		if c, ok := selfTestCheck(b.st, "run_dir"); !ok || c.OK {
			t.Errorf("%s: run_dir check = %+v", name, c)
		}
	}
}

func TestBwrapRunDirMustFitSocketPaths(t *testing.T) {
	run := filepath.Join(shortTempDir(t), strings.Repeat("d", 80))
	b := newBwrapBackend(context.Background(), bwrapWorks, t.TempDir(), run)
	if b.st.Available || !strings.Contains(b.st.Reason, "too long") {
		t.Fatalf("status = %+v", b.st)
	}
}

// In auto mode without bwrap the none status keeps bwrap's fix natively; the
// container image has no bwrap, so there the fix is the compose deployment.
func TestAutoWithoutBwrapKeepsTheRightFix(t *testing.T) {
	ctx := context.Background()
	e, _ := newEnv(t, opts{cfg: func(c *config.Config) { c.Sandbox.Backend = "none" }})
	native := e.c.svc.autoFallback(ctx, "linux", false, bwrapMissing).probe(ctx)
	if native.Backend != "none" || !strings.Contains(native.Fix, "bubblewrap") || !strings.Contains(native.Reason, "not installed") {
		t.Fatalf("native Linux = %+v", native)
	}
	for name, st := range map[string]core.SandboxStatus{
		"container": e.c.svc.autoFallback(ctx, "linux", true, bwrapMissing).probe(ctx),
		"macOS":     e.c.svc.autoFallback(ctx, "darwin", false, bwrapMissing).probe(ctx),
	} {
		if st.Backend != "none" || st.Fix != composeFix || !workingComposeFix(st.Fix) {
			t.Errorf("%s = %+v", name, st)
		}
	}
	if b := e.c.svc.autoFallback(ctx, "linux", false, bwrapWorks); b.name() != "bwrap" {
		t.Fatalf("auto with a working bwrap picked %s", b.name())
	}
}
