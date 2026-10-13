package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// Usage reports a workspace's size from the periodic quota scan; a workspace
// the scan has not seen yet is measured on first use.
func (s *service) Usage(_ context.Context, ws core.WorkspaceID) (int64, int64, error) {
	if ws == "" || !proto.ValidWorkspace(string(ws)) {
		return 0, 0, fmt.Errorf("sandbox: %w: workspace %q", core.ErrInvalid, ws)
	}
	quota := int64(s.cfg.Current().Sandbox.WorkspaceQuotaMB) << 20
	s.mu.Lock()
	used, ok := s.usage[ws]
	s.mu.Unlock()
	if !ok {
		var err error
		if used, err = dirSize(filepath.Join(s.work, "ws", string(ws))); err != nil {
			return 0, quota, err
		}
		s.mu.Lock()
		s.usage[ws] = used
		s.mu.Unlock()
	}
	return used, quota, nil
}

// scanQuota measures every workspace under <work>/ws.
func (s *service) scanQuota() {
	entries, err := os.ReadDir(filepath.Join(s.work, "ws"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("quota scan failed", "err", err)
		}
		return
	}
	sizes := map[core.WorkspaceID]int64{}
	for _, e := range entries {
		if !e.IsDir() || !proto.ValidWorkspace(e.Name()) {
			continue
		}
		n, err := dirSize(filepath.Join(s.work, "ws", e.Name()))
		if err != nil {
			s.log.Warn("quota scan failed", "workspace", e.Name(), "err", err)
			continue
		}
		sizes[core.WorkspaceID(e.Name())] = n
	}
	s.mu.Lock()
	s.usage = sizes
	s.mu.Unlock()
}

// dirSize sums regular file sizes without following symlinks (box content is
// untrusted). A missing directory is empty.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if errors.Is(err, fs.ErrPermission) && d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
