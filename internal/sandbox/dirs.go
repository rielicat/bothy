package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// sharedDir is 2770: setgid so files the gateway and boxes create stay in
// the shared group 10001.
const sharedDir = 0o770 | fs.ModeSetgid

// noGroup leaves directory groups alone (bwrap maps the box uid onto the
// gateway's own, so no shared group is needed).
const noGroup = -1

// mkdirGroup creates dir (and missing parents below root) with mode, then
// enforces mode and, when gid >= 0, the group on every directory it created,
// and on dir itself when it already existed and fixExisting is set. Explicit
// chmod because the process umask (007) would otherwise drop bits.
func mkdirGroup(root, dir string, mode fs.FileMode, gid int, fixExisting bool) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sandbox: %s is outside %s", dir, root)
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, mode); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if err := fixDir(cur, mode, gid); err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			// Nothing on these volumes is a symlink unless something hostile
			// put it there; never follow it.
			return fmt.Errorf("sandbox: %s is not a directory", cur)
		}
		if cur == dir && fixExisting {
			if err := fixDir(cur, mode, gid); err != nil {
				return err
			}
		}
	}
	return nil
}

func fixDir(p string, mode fs.FileMode, gid int) error {
	if gid >= 0 {
		if err := os.Lchown(p, -1, gid); err != nil {
			return fmt.Errorf("sandbox: chgrp %d %s: %w", gid, p, err)
		}
	}
	// chown clears setgid on some systems, so chmod last.
	return os.Chmod(p, mode)
}

// maxSocketPath is the longest unix socket path Linux accepts: sun_path holds
// 108 bytes including the terminating NUL.
const maxSocketPath = 107

const runDirFix = "set BOTHY_STORAGE__RUN_DIR (or storage.run_dir in bothy.yaml) to a short directory this user can write, " +
	"such as $XDG_RUNTIME_DIR/bothy, and restart"

// prepareRunDir creates the run directory, which nothing else creates on a
// native install, and checks that the gateway can make the boxes' egress
// sockets in it. In the images and under compose it already exists.
func prepareRunDir(run string) error {
	if err := os.MkdirAll(run, 0o750); err != nil {
		return fmt.Errorf("cannot create the run directory: %w", err)
	}
	probe, err := os.MkdirTemp(run, ".probe-")
	if err != nil {
		return fmt.Errorf("the run directory %s is not writable: %w", run, err)
	}
	_ = os.Remove(probe)
	if n := len(filepath.Join(run, filepath.FromSlash(proto.EgressSubpath(canaryKey)), "proxy.sock")); n > maxSocketPath {
		return fmt.Errorf("the run directory %s is too long for the boxes' egress sockets (%d > %d bytes)", run, n, maxSocketPath)
	}
	return nil
}

// boxDirs creates what a box mounts (core.Sandbox.Ensure): ws/<id> and the
// MCP tree (2770), skills/ (2770), and egress/<h> (0750) on the run volume.
func boxDirs(work, run string, spec core.BoxSpec, gid int) error {
	if spec.Workspace != "" {
		if err := mkdirGroup(work, filepath.Join(work, "ws", string(spec.Workspace)), sharedDir, gid, true); err != nil {
			return err
		}
	}
	if spec.MCPTree != "" {
		if err := mkdirGroup(work, filepath.Join(work, filepath.FromSlash(spec.MCPTree)), sharedDir, gid, true); err != nil {
			return err
		}
	}
	// skills/ and egress/ belong to the skills and egress modules once they
	// exist; only create them.
	if err := mkdirGroup(work, filepath.Join(work, "skills"), sharedDir, gid, false); err != nil {
		return err
	}
	// egress/ is shared with sandboxd (group 65532), which keeps its canary
	// socket there.
	if err := mkdirGroup(run, filepath.Join(run, "egress"), 0o770, noGroup, false); err != nil {
		return err
	}
	return mkdirGroup(run, filepath.Join(run, filepath.FromSlash(proto.EgressSubpath(spec.Key))), 0o750, gid, true)
}
