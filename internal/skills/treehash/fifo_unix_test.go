//go:build unix

package treehash

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSumRefusesSpecialFiles(t *testing.T) {
	dir := build(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o640); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := Sum(dir); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Sum with a fifo = %v, want ErrUnsupported", err)
	}
}
