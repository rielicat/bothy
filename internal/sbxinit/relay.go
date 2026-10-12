package sbxinit

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// maxRelayConns bounds the relay's file descriptors well below the box's
// nofile limit of 1024.
const maxRelayConns = 256

// startRelay listens on addr and copies every accepted connection to the unix
// socket sock. The proxy identifies the box by that socket, so nothing about
// the client is forwarded.
func startRelay(addr, sock string) (stop func(), err error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveRelay(ln, sock)
	}()
	return func() {
		ln.Close()
		wg.Wait()
	}, nil
}

func serveRelay(ln net.Listener, sock string) {
	slots := make(chan struct{}, maxRelayConns)
	for {
		c, err := ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			relayConn(c, sock)
		}()
	}
}

type halfCloser interface {
	net.Conn
	CloseWrite() error
}

func relayConn(c net.Conn, sock string) {
	defer c.Close()
	d := net.Dialer{Timeout: 5 * time.Second}
	u, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return
	}
	defer u.Close()
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if hc, ok := dst.(halfCloser); ok {
			_ = hc.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go pump(u, c)
	go pump(c, u)
	<-done
	<-done
}
