package sandboxd

import "testing"

func TestIsMountPoint(t *testing.T) {
	if !isMountPoint("/") || !isMountPoint("/proc") {
		t.Fatal("/ and /proc are mount points")
	}
	if isMountPoint(t.TempDir()) {
		t.Fatal("a fresh temp directory is not a mount point")
	}
}
