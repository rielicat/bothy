package config

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var pollInterval = 5 * time.Second

type stamp struct {
	exists bool
	mod    int64
	size   int64
}

func statStamp(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{exists: true, mod: fi.ModTime().UnixNano(), size: fi.Size()}
}

// watchFiles reloads when either config file's mtime or size changes, and on
// SIGHUP, until ctx is done. Reload errors surface through Effective().Errors.
func (m *Manager) watchFiles(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	t := time.NewTicker(m.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			_ = m.Reload(ctx)
		case <-t.C:
			st := m.st.Load()
			if statStamp(m.file) != st.fileStamp || statStamp(m.uiPath) != st.uiStamp {
				_ = m.Reload(ctx)
			}
		}
	}
}
