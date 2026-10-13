package config

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// mounts is the list of mount points from /proc/self/mountinfo; nil where that
// file does not exist (macOS, Windows, tests).
type mounts []string

func resolveDirs(c *Config, mt mounts) Dirs {
	d := Dirs{Data: c.Storage.DataDir, Work: c.Storage.WorkDir, Run: c.Storage.RunDir, Keyfile: c.Vault.Keyfile}
	if d.Data == "" {
		d.Data = "/data"
	}
	if d.Run == "" {
		d.Run = "/run/bothy"
	}
	if d.Work == "" {
		d.Work = filepath.Join(d.Data, "work")
		if mt.isMountPoint("/work") {
			d.Work, d.LegacyWork = "/work", d.Work
		}
	}
	if d.Keyfile == "" {
		d.Keyfile = filepath.Join(d.Data, "keys", "kek")
		if mt.isMountPoint("/keys") {
			d.Keyfile, d.LegacyKeyfile = "/keys/kek", d.Keyfile
		}
	}
	if len(mt) == 0 {
		d.KeyfileColocated = within(d.Data, d.Keyfile)
	} else {
		d.KeyfileColocated = mt.mountOf(d.Data) == mt.mountOf(d.Keyfile)
	}
	return d
}

// rename is os.Rename; tests make it fail across devices.
var rename = os.Rename

// MoveLegacyWork moves the workspaces of a single-container install from
// <data>/work into a /work volume mounted since (the move to compose), when
// that volume is still empty. On one filesystem the entries are renamed;
// across volumes the tree is copied into a temporary directory inside /work
// and renamed into place only once complete, so a failed copy leaves
// <data>/work as it was. It reports whether anything moved.
func MoveLegacyWork(d Dirs) (bool, error) {
	if d.LegacyWork == "" || d.LegacyWork == d.Work {
		return false, nil
	}
	src, err := os.ReadDir(d.LegacyWork)
	if err != nil || len(src) == 0 {
		return false, nil //nolint:nilerr // nothing to move
	}
	if dst, err := os.ReadDir(d.Work); err != nil || len(dst) > 0 {
		return false, err
	}
	from := func(i int) string { return filepath.Join(d.LegacyWork, src[i].Name()) }
	if rename(from(0), filepath.Join(d.Work, src[0].Name())) == nil {
		for i := 1; i < len(src); i++ {
			if err := rename(from(i), filepath.Join(d.Work, src[i].Name())); err != nil {
				return true, err
			}
		}
		return true, os.Remove(d.LegacyWork)
	}
	tmp, err := os.MkdirTemp(d.Work, ".moving-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	for i := range src {
		if err := copyTree(from(i), filepath.Join(tmp, src[i].Name())); err != nil {
			return false, fmt.Errorf("copy %s to %s: %w", from(i), d.Work, err)
		}
	}
	for _, e := range src {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(d.Work, e.Name())); err != nil {
			return true, err
		}
	}
	return true, os.RemoveAll(d.LegacyWork)
}

// copyTree copies files, directories and symlinks with their permission bits;
// other file types (sockets, FIFOs) are left behind.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			if err := os.Mkdir(dst, 0o700); err != nil {
				return err
			}
			return os.Chmod(dst, mode.Perm()|mode&(fs.ModeSetgid|fs.ModeSticky))
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case mode.IsRegular():
			return copyFile(p, dst, mode.Perm())
		}
		return nil
	})
}

func copyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// within is the "same volume" guess when no mount table is available.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (mt mounts) isMountPoint(dir string) bool {
	dir = filepath.Clean(dir)
	for _, m := range mt {
		if m == dir {
			return true
		}
	}
	return false
}

// mountOf returns the longest mount point containing p (lexically; the paths
// may not exist yet).
func (mt mounts) mountOf(p string) string {
	p = filepath.Clean(p)
	best := ""
	for _, m := range mt {
		prefix := strings.TrimSuffix(m, "/") + "/"
		if (p == m || strings.HasPrefix(p, prefix)) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

func readMounts() mounts {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseMountinfo(f)
}

func parseMountinfo(r io.Reader) mounts {
	var out mounts
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 5 {
			out = append(out, filepath.Clean(unescapeMountinfo(fields[4])))
		}
	}
	return out
}

// unescapeMountinfo decodes the kernel's octal escapes (\040 for space, etc.).
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
