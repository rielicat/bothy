//go:build unix

package proto

import (
	"fmt"
	"io/fs"
	"syscall"
)

// MountID is a directory's "dev:ino" identity: what `sbx-init mountids`
// prints for a box's mount points and what sandboxd compares them with.
func MountID(fi fs.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), true
}
