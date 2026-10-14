//go:build unix

package sbxinit

import (
	"fmt"
	"io"
	"os"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// mountidsMain prints the identity of each path, one per line, for
// sandboxd's bind-mount check: a path that is a symlink or not a directory
// fails the whole command.
func mountidsMain(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprint(stderr, usage)
		return exitInternal
	}
	for _, p := range paths {
		fi, err := os.Lstat(p) //nolint:gosec // G703: sandboxd passes the box's fixed mount targets
		if err != nil {
			fmt.Fprintln(stderr, "sbx-init mountids:", err)
			return exitInternal
		}
		id, ok := proto.MountID(fi)
		if !ok || !fi.IsDir() {
			fmt.Fprintf(stderr, "sbx-init mountids: %s is not a directory\n", p)
			return exitInternal
		}
		fmt.Fprintln(stdout, id)
	}
	return 0
}
