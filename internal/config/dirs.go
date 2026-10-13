package config

import (
	"bufio"
	"io"
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
		d.Run = defaultRunDir
	}
	if d.Work == "" {
		d.Work = filepath.Join(d.Data, "work")
		if mt.isMountPoint("/work") {
			d.Work = "/work"
		}
	}
	if d.Keyfile == "" {
		d.Keyfile = filepath.Join(d.Data, "keys", "kek")
		if mt.isMountPoint("/keys") {
			d.Keyfile = "/keys/kek"
		}
	}
	if len(mt) == 0 {
		d.KeyfileColocated = within(d.Data, d.Keyfile)
	} else {
		d.KeyfileColocated = mt.mountOf(d.Data) == mt.mountOf(d.Keyfile)
	}
	return d
}

const defaultRunDir = "/run/bothy"

// resolveRunDir keeps the default /run/bothy where it exists (the images
// create it, compose mounts the run volume there). A native install cannot
// create it as an unprivileged user, so it gets $XDG_RUNTIME_DIR/bothy, else
// <data_dir>/run. An explicit run_dir is kept as is.
func resolveRunDir(run, data string, getenv func(string) string, isDir func(string) bool) string {
	if run != defaultRunDir || isDir(run) {
		return run
	}
	if x := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
		return filepath.Join(x, "bothy")
	}
	return filepath.Join(data, "run")
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
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
