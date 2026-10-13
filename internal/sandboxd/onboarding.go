package sandboxd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// socketOnboarding turns an engine connection failure into a plain reason and
// the exact fix line (SPEC §2.10 "Docker socket onboarding").
func socketOnboarding(sock string, err error) (reason, fix string) {
	fi, serr := os.Stat(sock)
	if serr != nil {
		return fmt.Sprintf("cannot find the container runtime socket at %s (%v)", sock, serr),
			"mount the Docker socket into sandboxd (DOCKER_SOCK in deploy/.env) and run: docker compose up -d"
	}
	if !errors.Is(err, fs.ErrPermission) && !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
		return fmt.Sprintf("cannot reach the container runtime at %s: %v", sock, err),
			"check that Docker is running on the host"
	}
	uid, gid, _ := fileOwner(fi)
	reason = fmt.Sprintf("cannot access %s (owner uid %d gid %d, mode %04o)", sock, uid, gid, fi.Mode().Perm())
	if rootlessSocket(sock, uid, fi.Mode().Perm()) {
		// Group membership is not enough for a per-user rootless socket.
		return reason, fmt.Sprintf("set SANDBOXD_USER=%d:%d and DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock in deploy/.env and run: docker compose up -d", uid, gid)
	}
	return reason, fmt.Sprintf("Set DOCKER_GID=%d in deploy/.env and run: docker compose up -d", gid)
}

func rootlessSocket(sock string, uid int, perm fs.FileMode) bool {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" && strings.HasPrefix(sock, xdg+"/") {
		return true
	}
	return strings.HasPrefix(sock, "/run/user/") || (uid != 0 && (perm == 0o600 || perm == 0o660))
}
