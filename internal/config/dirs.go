package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func resolveDirs(c *Config, mounted func(string) bool) Dirs {
	d := Dirs{Data: c.Storage.DataDir, Work: c.Storage.WorkDir, Run: c.Storage.RunDir, Keyfile: c.Vault.Keyfile}
	if d.Data == "" {
		d.Data = "/data"
	}
	if d.Work == "" {
		d.Work = filepath.Join(d.Data, "work")
		if mounted("/work") {
			d.Work = "/work"
		}
	}
	if d.Keyfile == "" {
		d.Keyfile = filepath.Join(d.Data, "keys", "kek")
		if mounted("/keys") {
			d.Keyfile = "/keys/kek"
		}
	}
	d.KeyfileColocated = within(d.Data, d.Keyfile)
	return d
}

// within approximates "same volume" as "under the data dir". TEMPORARY: the
// full implementation compares mount points.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isMountPoint reports whether dir is a mount point per /proc/self/mountinfo
// (always false where that file does not exist).
func isMountPoint(dir string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	return mountinfoHas(bufio.NewScanner(f), dir)
}

func mountinfoHas(sc *bufio.Scanner, dir string) bool {
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		if unescapeMountinfo(fields[4]) == dir {
			return true
		}
	}
	return false
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
