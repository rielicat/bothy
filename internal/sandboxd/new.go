package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/sandboxd/engine"
)

// Main runs `bothy sandboxd`. Configuration comes only from BOTHY_SBX_* env.
func Main(ctx context.Context, args []string, env func() []string) error {
	if len(args) > 0 {
		return errors.New("sandboxd takes no arguments; configure it with BOTHY_SBX_* environment variables")
	}
	if runtime.GOOS != "linux" {
		// Peer credentials, shared named volumes and the egress sockets all
		// assume a Linux engine host.
		return errors.New("sandboxd runs only on Linux")
	}
	cfg, err := ParseConfig(env())
	if err != nil {
		return err
	}
	cli, err := engine.New(cfg.Engine)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "sandboxd")
	ln, err := listenSocket(cfg.Socket, cfg.RunDir)
	if err != nil {
		return err
	}
	return Serve(ctx, cfg, cli, &credListener{Listener: ln, uid: cfg.GatewayUID, log: log}, log)
}

// Serve runs the API on ln against eng until ctx ends. Main adds the real
// engine client, the socket and the peer-credential check; tests pass an
// enginetest.Fake.
func Serve(ctx context.Context, cfg Config, eng Engine, ln net.Listener, log *slog.Logger) error {
	return newDaemon(cfg, eng, log, time.Now).serve(ctx, ln)
}

// serve runs the API on ln until ctx ends, plus the start-up cleanup, the
// periodic self-test and the reaper.
func (d *daemon) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           d.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.background(ctx)
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	wg.Wait()
	d.removeAll(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (d *daemon) background(ctx context.Context) {
	d.removeOrphans(ctx)
	d.selfTest(ctx)
	reap := time.NewTicker(30 * time.Second)
	defer reap.Stop()
	test := time.NewTimer(d.nextSelfTest())
	defer test.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reap.C:
			d.reap(ctx)
		case <-test.C:
			d.selfTest(ctx)
			test.Reset(d.nextSelfTest())
		}
	}
}

// listenSocket replaces a stale socket and opens it 0660 to the run
// directory's group (65532 in the image layout), which the gateway shares.
func listenSocket(path, runDir string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil { //nolint:gosec // G302: the gateway connects through the shared group
		ln.Close()
		return nil, err
	}
	if fi, err := os.Stat(runDir); err == nil {
		if _, gid, ok := fileOwner(fi); ok {
			if err := os.Lchown(path, -1, gid); err != nil {
				ln.Close()
				return nil, fmt.Errorf("chgrp %s: %w", path, err)
			}
		}
	}
	return ln, nil
}

// credListener drops connections whose SO_PEERCRED uid is not the gateway's.
type credListener struct {
	net.Listener
	uid int
	log *slog.Logger
}

func (l *credListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(c)
		if err == nil && uid == l.uid {
			return c, nil
		}
		l.log.Warn("refused connection", "peer_uid", uid, "err", err)
		c.Close()
	}
}
