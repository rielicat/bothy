package agent

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/rielicat/bothy/internal/core"
)

// boxGroup is the gid shared by the gateway and box processes (SPEC §2.3), so
// both can read and write workspace files.
const boxGroup = 10001

var workspaceDirs = []string{".bothy", ".bothy/in", ".bothy/out", ".bothy/tmp"}

func (r *Runner) workspaceDir(p *core.Principal) string {
	if r.d.WorkDir == "" || p == nil || !core.ValidID(core.PrefixWorkspace, string(p.Workspace)) {
		return ""
	}
	return filepath.Join(r.d.WorkDir, "ws", string(p.Workspace))
}

// ensureWorkspace creates <work>/ws/<id>/ and .bothy/{in,out,tmp} (2770,
// group 10001) so fs_* tools work with every sandbox backend.
func (r *Runner) ensureWorkspace(p *core.Principal) error {
	dir := r.workspaceDir(p)
	if dir == "" {
		return nil
	}
	if err := mkdirShared(dir); err != nil {
		return err
	}
	for _, sub := range workspaceDirs {
		if err := mkdirShared(filepath.Join(dir, filepath.FromSlash(sub))); err != nil {
			return err
		}
	}
	return nil
}

// workspaceOnce creates the workspace before a run's first tool call. It is
// called from the run goroutine only (never from parallel tool executions).
func (r *Runner) workspaceOnce(rs *runState) {
	if rs.wsReady {
		return
	}
	if err := r.ensureWorkspace(rs.principal); err != nil {
		r.log.Warn("workspace directories", "principal", rs.principal.ID, "err", err)
		return
	}
	rs.wsReady = true
}

func mkdirShared(dir string) error {
	err := os.Mkdir(dir, 0o770|os.ModeSetgid)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o770|os.ModeSetgid); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	case errors.Is(err, fs.ErrExist):
		return nil
	case err != nil:
		return err
	}
	if err := os.Chmod(dir, 0o770|os.ModeSetgid); err != nil {
		return err
	}
	// Without the group (native installs, tests) the chown fails with EPERM;
	// the directory still works for the gateway's own fs_* tools.
	if err := os.Lchown(dir, -1, boxGroup); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}

// spill writes the full redacted output of a tool call to
// .bothy/out/<tcl>.txt and returns that workspace-relative path.
func (r *Runner) spill(p *core.Principal, callID, content string) (string, error) {
	dir := r.workspaceDir(p)
	if dir == "" {
		return "", errors.New("no workspace")
	}
	if err := r.ensureWorkspace(p); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	rel := path.Join(".bothy", "out", callID+".txt")
	if err := root.WriteFile(rel, []byte(content), 0o660); err != nil {
		return "", err
	}
	return rel, nil
}
