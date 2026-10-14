package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

var errBindCheck = errors.New("bind mount check")

// bindFix is the fix line of every bind-mount mode failure.
func bindFix(workDir string) string {
	return "mount the work volume into sandboxd read-only at " + workDir + " (compose: add work:" + workDir + ":ro to the sandboxd service's volumes) " +
		"and keep both volumes on the local driver, then run: docker compose up -d; or upgrade Docker to 26 or newer"
}

// resolveBindRoots asks the engine where the work and run volumes live, and
// checks that sandboxd has its own mounts of both for verifyBinds.
func (d *daemon) resolveBindRoots(ctx context.Context) (*bindRoots, error) {
	var roots bindRoots
	for _, v := range []struct {
		name string
		dst  *string
	}{{d.cfg.WorkVolume, &roots.work}, {d.cfg.RunVolume, &roots.run}} {
		vol, err := d.eng.VolumeInspect(ctx, v.name)
		if err != nil {
			return nil, fmt.Errorf("volume %s: %w", v.name, err)
		}
		if vol.Driver != "local" {
			return nil, fmt.Errorf("volume %s uses the %q driver, not local", v.name, vol.Driver)
		}
		if !path.IsAbs(vol.Mountpoint) || path.Clean(vol.Mountpoint) != vol.Mountpoint || vol.Mountpoint == "/" {
			return nil, fmt.Errorf("volume %s has an unusable mount point %q", v.name, vol.Mountpoint)
		}
		*v.dst = vol.Mountpoint
	}
	for _, dir := range []string{d.cfg.WorkDir, d.cfg.RunDir} {
		if !d.mounted(dir) {
			return nil, fmt.Errorf("%s is not a mount point in sandboxd", dir)
		}
	}
	return &roots, nil
}

// verifyBinds runs before a bind-mode box is used: every mount must be the
// very directory (device and inode) that sandboxd finds at that subpath in
// its own mounts of the volumes. The engine resolves a bind source on the
// host and follows symlinks, so a directory swapped for a symlink while the
// box started would otherwise put any host path in the box; os.Root keeps
// sandboxd's own lookup inside the volume. Until then the box has run only
// sbx-init as PID 1, which touches none of these mounts.
func (d *daemon) verifyBinds(ctx context.Context, id string, in specInput) error {
	ms := boxMounts(in)
	argv := []string{sbxInit, "mountids"}
	for _, m := range ms {
		argv = append(argv, m.target)
	}
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, stderr, exit, err := d.execCollect(ectx, id, argv, 16<<10)
	if err != nil {
		return fmt.Errorf("sbx-init mountids: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("sbx-init mountids exited %d: %s", exit, strings.TrimSpace(stderr))
	}
	got := strings.Fields(out)
	if len(got) != len(ms) {
		return fmt.Errorf("sbx-init mountids printed %d ids for %d mounts", len(got), len(ms))
	}
	for i, m := range ms {
		root := d.cfg.RunDir
		if m.work {
			root = d.cfg.WorkDir
		}
		want, err := dirID(root, m.sub)
		if err != nil {
			return fmt.Errorf("%s: %w", m.target, err)
		}
		if got[i] != want {
			return fmt.Errorf("%s is not the directory sandboxd sees at %s", m.target, path.Join(root, m.sub))
		}
	}
	return nil
}

// dirID is the identity of directory sub under root, looked up without
// leaving root.
func dirID(root, sub string) (string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	f, err := r.Open(sub)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	id, ok := proto.MountID(fi)
	if !ok || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", sub)
	}
	return id, nil
}

// isMountPoint reports whether dir is a mount point of this process.
func isMountPoint(dir string) bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 4 && unescape.Replace(f[4]) == dir {
			return true
		}
	}
	return false
}
