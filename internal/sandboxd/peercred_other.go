//go:build !linux

package sandboxd

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (int, error) { return -1, errors.New("SO_PEERCRED is Linux-only") }
