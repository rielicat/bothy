package sbxinit

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// countProcs counts live processes under procDir other than PID 1 and self.
// With uid >= 0 only processes whose real uid matches are counted (those the
// caller may signal). Zombies are dead already and are not counted.
func countProcs(procDir string, self, uid int) (int, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue // exited meanwhile
		}
		if st := procState(stat); st == 'Z' || st == 'X' || st == 0 {
			continue
		}
		if uid >= 0 {
			status, err := os.ReadFile(filepath.Join(dir, "status"))
			if err != nil {
				continue
			}
			if u, ok := statusField(string(status), "Uid"); !ok || firstField(u) != strconv.Itoa(uid) {
				continue
			}
		}
		n++
	}
	return n, nil
}

// procState returns the state letter of /proc/<pid>/stat. The command name
// may contain spaces and parentheses, so parse after the last ')'.
func procState(stat []byte) byte {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return 0
	}
	return stat[i+2]
}

// statusField returns the value of "Name:\tvalue" in /proc/<pid>/status.
func statusField(status, name string) (string, bool) {
	for line := range strings.SplitSeq(status, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && k == name {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}
