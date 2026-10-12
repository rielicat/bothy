package sandboxd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredListener(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "s.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if uid, err := peerUID(dialSelf(t, ln, sock)); err != nil || uid != os.Getuid() {
		t.Fatalf("peerUID = %d, %v; want %d", uid, err, os.Getuid())
	}

	// A peer with another uid is dropped before any HTTP is read.
	cl := &credListener{Listener: ln, uid: os.Getuid() + 1, log: slog.New(slog.DiscardHandler)}
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := cl.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("refused peer read: %v, want EOF", err)
	}
	select {
	case <-accepted:
		t.Fatal("a peer with the wrong uid was accepted")
	default:
	}
}

// dialSelf returns the server side of a connection made by this process.
func dialSelf(t *testing.T, ln net.Listener, sock string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
