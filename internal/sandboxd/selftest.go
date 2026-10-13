package sandboxd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const runscFix = `register runsc with --host-uds=open in /etc/docker/daemon.json: { "runtimes": { "runsc": { "path": "/usr/local/bin/runsc", "runtimeArgs": ["--host-uds=open"] } } } and restart Docker`

// canaryKey is the box key of the self-test canary (SPEC §2.9).
var canaryKey = core.DerivedID(core.PrefixBox, "canary")

// selfTestRetry is the self-test interval while the canary could not even run
// (engine unreachable, images missing): such failures are often transient.
// Isolation failures keep the normal interval.
const selfTestRetry = 30 * time.Second

// selfTest runs the engine checks and the canary, chooses the runtime and
// publishes the result as the current health.
func (d *daemon) selfTest(ctx context.Context) {
	d.testMu.Lock()
	defer d.testMu.Unlock()
	tctx, cancel := context.WithTimeout(ctx, 15*time.Minute) // includes image pulls
	defer cancel()
	h, rt, builtin := d.runSelfTest(tctx)
	if ctx.Err() != nil {
		// Shutting down: the aborted run says nothing about the engine.
		return
	}
	d.hmu.Lock()
	wasOK := d.health.OK
	d.health, d.runtime, d.seccompBuiltin = h, rt, builtin
	d.hmu.Unlock()
	switch {
	case h.OK && !wasOK:
		d.log.Info("self-test passed", "runtime", h.Runtime, "engine", h.Engine)
	case !h.OK:
		d.log.Error("self-test failed: exec refused", "reason", h.Reason, "fix", h.Fix)
	}
}

func (d *daemon) runSelfTest(ctx context.Context) (h proto.Health, runtime string, seccompBuiltin bool) {
	h = proto.Health{Version: proto.Version, MaxBoxes: d.cfg.MaxBoxes, SelfTest: []proto.Check{}}
	add := func(name string, ok bool, detail string) {
		h.SelfTest = append(h.SelfTest, proto.Check{Name: name, OK: ok, Detail: detail})
	}
	fail := func(reason, fix string) (proto.Health, string, bool) {
		h.OK, h.Reason, h.Fix = false, reason, fix
		return h, runtime, seccompBuiltin
	}

	server, err := d.eng.Negotiate(ctx)
	if err != nil {
		reason, fix := socketOnboarding(d.eng.Socket(), err)
		add(proto.CheckEngine, false, reason)
		return fail(reason, fix)
	}
	add(proto.CheckEngine, true, "connected to "+d.eng.Socket())
	if v, err := d.eng.Version(ctx); err == nil {
		h.Engine = "docker " + v.Version
	}
	h.API = d.eng.APIVersion()
	if engine.CompareVersions(server, engine.MinAPI) < 0 {
		add(proto.CheckEngineAPI, false, "engine API "+server+" is older than "+engine.MinAPI)
		return fail("Docker 26+ required (engine API "+server+" has no volume subpaths)", "upgrade Docker to 26 or newer")
	}
	h.Subpath = true
	add(proto.CheckEngineAPI, true, "engine API "+server)
	info, err := d.eng.Info(ctx)
	if err != nil {
		return fail("engine info failed: "+err.Error(), "")
	}
	for _, opt := range info.SecurityOptions {
		switch {
		case opt == "name=rootless", opt == "name=userns":
			// Both map box uid 10001 to an unprivileged host uid.
			h.Rootless = true
		case strings.HasPrefix(opt, "name=seccomp") && strings.Contains(opt, "profile=unconfined"):
			seccompBuiltin = true
		}
	}
	if err := d.ensureImages(ctx); err != nil {
		add(proto.CheckImages, false, err.Error())
		return fail(err.Error(), "check BOTHY_SBX_IMAGES and that the engine can pull from the registry")
	}
	add(proto.CheckImages, true, fmt.Sprintf("%d profile image(s) present", len(d.cfg.Images)))

	_, runscRegistered := info.Runtimes["runsc"]
	runcName := "" // the engine default
	for _, n := range []string{"runc", "io.containerd.runc.v2"} {
		if _, ok := info.Runtimes[n]; ok {
			runcName = n
			break
		}
	}
	var checks []proto.Check
	switch {
	case d.cfg.Runtime == "runsc" && !runscRegistered:
		add(proto.CheckRunsc, false, "runsc is not registered with the engine")
		h.Runtime = "runsc"
		return fail("BOTHY_SBX_RUNTIME=runsc but runsc is not registered", runscFix)
	case d.cfg.Runtime == "runsc" || (d.cfg.Runtime == "auto" && runscRegistered):
		checks, err = d.canary(ctx, "runsc", "runsc", seccompBuiltin)
		if err == nil && allOK(checks) {
			h.Runtime, runtime = "runsc", "runsc"
			break
		}
		detail := canaryFailure(checks, err)
		if d.cfg.Runtime == "runsc" {
			h.Runtime = "runsc"
			add(proto.CheckRunsc, false, detail)
			h.SelfTest = append(h.SelfTest, checks...)
			return fail("the runsc canary failed: "+detail, runscFix)
		}
		add(proto.CheckRunsc, false, detail+"; using runc. To use runsc, "+runscFix)
		fallthrough
	default:
		h.Runtime, runtime = "runc", runcName
		checks, err = d.canary(ctx, runcName, "runc", seccompBuiltin)
	}
	if err != nil {
		add(proto.CheckCanary, false, err.Error())
		return fail("the canary box could not run: "+err.Error(), "")
	}
	add(proto.CheckCanary, true, "canary ran under "+h.Runtime)
	h.SelfTest = append(h.SelfTest, checks...)
	if bad := failedIsolation(checks); len(bad) > 0 {
		return fail("box isolation self-test failed: "+strings.Join(bad, ", "),
			"run `docker info` and check the seccomp, no-new-privileges and read-only rootfs support of the engine")
	}
	h.OK = true
	return h, runtime, seccompBuiltin
}

// nextSelfTest is the delay before the next periodic self-test.
func (d *daemon) nextSelfTest() time.Duration {
	d.hmu.RLock()
	defer d.hmu.RUnlock()
	canaryRan := slices.ContainsFunc(d.health.SelfTest, func(c proto.Check) bool {
		return slices.Contains(proto.IsolationChecks, c.Name)
	})
	if d.health.OK || canaryRan {
		return d.cfg.SelfTestEvery
	}
	return min(selfTestRetry, d.cfg.SelfTestEvery)
}

func allOK(cs []proto.Check) bool {
	return len(cs) > 0 && !slices.ContainsFunc(cs, func(c proto.Check) bool { return !c.OK })
}

// failedIsolation lists failed (or missing) isolation probes.
func failedIsolation(cs []proto.Check) []string {
	var bad []string
	for _, name := range proto.IsolationChecks {
		i := slices.IndexFunc(cs, func(c proto.Check) bool { return c.Name == name })
		if i < 0 || !cs[i].OK {
			bad = append(bad, name)
		}
	}
	return bad
}

func canaryFailure(cs []proto.Check, err error) string {
	if err != nil {
		return err.Error()
	}
	var bad []string
	for _, c := range cs {
		if !c.OK {
			bad = append(bad, c.Name+" ("+c.Detail+")")
		}
	}
	return "failed checks: " + strings.Join(bad, ", ")
}

func (d *daemon) ensureImages(ctx context.Context) error {
	for _, profile := range slices.Sorted(maps.Keys(d.cfg.Images)) {
		ref := d.cfg.Images[profile]
		err := d.eng.ImageInspect(ctx, ref)
		if err == nil {
			continue
		}
		if !errors.Is(err, engine.ErrNotFound) {
			return fmt.Errorf("image %s (%s): %w", profile, ref, err)
		}
		if !strings.Contains(ref, "@") {
			return fmt.Errorf("image %s (%s) is not present and a bare image id cannot be pulled", profile, ref)
		}
		d.log.Info("pulling image", "profile", profile, "ref", ref)
		if err := d.eng.ImagePull(ctx, ref); err != nil {
			return fmt.Errorf("image %s: %w", profile, err)
		}
	}
	return nil
}

// canary runs `sbx-init selftest` in a throwaway box whose egress socket is a
// listener of sandboxd's own that answers with a nonce, so probe 7 proves
// that 127.0.0.1:3128 reaches exactly this box's socket.
func (d *daemon) canary(ctx context.Context, engineRuntime, label string, seccompBuiltin bool) ([]proto.Check, error) {
	nonce, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	stop, err := d.canaryListener(nonce)
	if err != nil {
		return nil, fmt.Errorf("canary egress socket: %w", err)
	}
	defer stop()
	name := d.containerName(canaryKey)
	_ = d.eng.ContainerRemove(ctx, name)
	profile := d.cfg.canaryProfile()
	cfg := boxSpec(specInput{
		Key: canaryKey, Profile: profile, Limits: proto.Limits{MemoryMB: 128, CPUs: 0.5, Pids: 64},
		Image: d.cfg.Images[profile], Runtime: engineRuntime, WorkVolume: d.cfg.WorkVolume, RunVolume: d.cfg.RunVolume,
		SeccompBuiltin: seccompBuiltin, Canary: true,
	})
	id, err := d.createAndStart(ctx, name, cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := d.eng.ContainerRemove(rctx, id); err != nil {
			d.log.Warn("removing the canary box failed", "err", err)
		}
	}()
	ectx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, stderr, exit, err := d.execCollect(ectx, id, []string{sbxInit, "selftest", "--runtime", label, "--canary", nonce}, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("sbx-init selftest: %w", err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("sbx-init selftest exited %d: %s", exit, strings.TrimSpace(stderr))
	}
	var checks []proto.Check
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return nil, fmt.Errorf("sbx-init selftest output: %w", err)
	}
	return checks, nil
}

// canaryListener serves the canary box's egress socket. Only the canary box
// mounts this directory, so it is world-accessible: sandboxd is not in the
// box's group 10001 and cannot hand it over.
func (d *daemon) canaryListener(nonce string) (stop func(), err error) {
	if err := ensureEgressRoot(d.cfg.RunDir); err != nil {
		return nil, err
	}
	dir := filepath.Join(d.cfg.RunDir, proto.EgressSubpath(canaryKey))
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) { //nolint:gosec // G301: see above
		return nil, err
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: see above
		return nil, err
	}
	sock := filepath.Join(dir, "proxy.sock")
	_ = os.Remove(sock)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o666); err != nil { //nolint:gosec // G302: see above
		ln.Close()
		return nil, err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Write([]byte("bothy-canary " + nonce + "\n"))
			c.Close()
		}
	}()
	return func() {
		ln.Close()
		wg.Wait()
		_ = os.Remove(sock)
	}, nil
}

// ensureEgressRoot creates <run>/egress, where the gateway creates every
// box's socket directory, 0770 in the run directory's group: sandboxd may run
// as another uid than the gateway (rootless Docker's SANDBOXD_USER), and a
// directory only sandboxd could write would refuse every box.
func ensureEgressRoot(runDir string) error {
	dir := filepath.Join(runDir, "egress")
	if err := os.Mkdir(dir, 0o770); err != nil { //nolint:gosec // G301: shared with the gateway through the group
		if errors.Is(err, fs.ErrExist) {
			return nil // the gateway's, or ours from an earlier run
		}
		return err
	}
	if fi, err := os.Stat(runDir); err == nil {
		if _, gid, ok := fileOwner(fi); ok {
			if err := os.Lchown(dir, -1, gid); err != nil {
				return fmt.Errorf("chgrp %s: %w", dir, err)
			}
		}
	}
	return os.Chmod(dir, 0o770) //nolint:gosec // G302: see above
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
