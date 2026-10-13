package agent

import (
	"context"
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

// Workspace provenance. The workspace belongs to the principal and is shared
// by all its sessions (chats, forks, schedules), and its files carry no
// provenance of their own. Once untrusted content may have been written to it
// the mark is set for good, and from then on whatever a tool reads out of it
// is untrusted in every session, so a clean fork or another chat cannot take
// it back in as trusted text.

// workspaceUntrusted reports whether the principal's workspace is marked; a
// failed read counts as marked.
func (r *Runner) workspaceUntrusted(ctx context.Context, p *core.Principal) bool {
	if p == nil || p.Workspace == "" {
		return false
	}
	r.mu.Lock()
	known := r.wsMarks[p.Workspace]
	r.mu.Unlock()
	if known {
		return true
	}
	v, err := r.d.Store.Meta().Get(context.WithoutCancel(ctx), metaWorkspaceUntrusted+string(p.Workspace))
	switch {
	case errors.Is(err, core.ErrNotFound):
		return false
	case err != nil:
		r.log.Warn("workspace provenance unreadable; treating it as untrusted", "workspace", p.Workspace, "err", err)
		return true
	case v != "1":
		return false
	}
	r.mu.Lock()
	r.wsMarks[p.Workspace] = true
	r.mu.Unlock()
	return true
}

// markWorkspaceUntrusted records that untrusted content may now sit in the
// principal's workspace.
func (r *Runner) markWorkspaceUntrusted(ctx context.Context, p *core.Principal, why string) {
	if p == nil || p.Workspace == "" || r.workspaceUntrusted(ctx, p) {
		return
	}
	if err := r.d.Store.Meta().Set(context.WithoutCancel(ctx), metaWorkspaceUntrusted+string(p.Workspace), "1"); err != nil {
		r.log.Error("mark workspace untrusted", "workspace", p.Workspace, "err", err)
	}
	// Cached even when the write failed, so this process keeps treating it
	// as untrusted.
	r.mu.Lock()
	r.wsMarks[p.Workspace] = true
	r.mu.Unlock()
	r.log.Info("workspace now holds untrusted content", "workspace", p.Workspace, "why", why)
}

// readsWorkspace: exec tools print whatever the box reads, and read-only
// fs tools return file contents and names.
func readsWorkspace(spec core.ToolSpec, a *core.Action) bool {
	m := spec.Manifest
	return m.Exec || len(m.FSPaths) > 0 && a != nil && a.SideEffect == core.SERead
}

// writesWorkspace: exec tools and writing fs tools.
func writesWorkspace(spec core.ToolSpec, a *core.Action) bool {
	m := spec.Manifest
	return m.Exec || len(m.FSPaths) > 0 && a != nil && a.SideEffect >= core.SELocalWrite
}

// untrustedFiles reports whether a request from someone other than the
// user brought files, which the hub saved into the workspace.
func untrustedFiles(reqs []core.RunRequest) bool {
	for _, q := range reqs {
		if q.Provenance != core.ProvUntrusted {
			continue
		}
		for _, p := range q.Input {
			if p.Blob != nil {
				return true
			}
		}
	}
	return false
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
