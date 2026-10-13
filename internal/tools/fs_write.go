package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	fsWriteName = "fs_write"
	devNull     = "/dev/null" // unified-diff name for a missing side
)

const (
	modeOverwrite = "overwrite"
	modeCreate    = "create"
	modeAppend    = "append"
)

type fsWrite struct{ sandbox core.Sandbox }

type fsWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

func (fsWrite) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        fsWriteName,
			Description: "Write a text file in your workspace, creating parent folders. Modes: overwrite (default), create (fails if it exists), append.",
			Schema: objectSchema(`"path":{"type":"string","description":"Workspace-relative path"},`+
				`"content":{"type":"string"},`+
				`"mode":{"type":"string","enum":["overwrite","create","append"]}`, "path", "content"),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Write file", SideEffect: core.SELocalWrite, FSPaths: []string{"**"},
			Core: true, Timeout: 30 * time.Second, MaxOutputKB: 4,
		},
	}
}

// Plan reads the current file to put an exact unified diff on the Action.
func (fsWrite) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in fsWriteArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	p, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	if p == "." {
		return nil, fmt.Errorf("%w: path must name a file", core.ErrInvalid)
	}
	switch in.Mode {
	case "":
		in.Mode = modeOverwrite
	case modeOverwrite, modeCreate, modeAppend:
	default:
		return nil, fmt.Errorf("%w: mode must be overwrite, create or append", core.ErrInvalid)
	}
	if len(in.Content) > maxWriteSize {
		return nil, fmt.Errorf("%w: content is larger than %s; write it in parts with mode append", core.ErrInvalid, humanSize(maxWriteSize))
	}
	root, err := openRoot(env)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	cur, err := readExisting(root, p)
	if err != nil {
		return nil, err
	}
	if cur.exists && in.Mode == modeCreate {
		return nil, fmt.Errorf("%w: %s already exists; use mode overwrite or append", core.ErrInvalid, p)
	}
	a, err := newAction(fsWriteName, core.ActFSWrite, core.SELocalWrite, fsWriteArgs{Path: p, Content: in.Content, Mode: in.Mode})
	if err != nil {
		return nil, err
	}
	a.Paths = []string{p}
	oldName := "a/" + p
	if !cur.exists {
		oldName = devNull
	}
	switch {
	case cur.exists && cur.data == nil:
		a.Diff = fmt.Sprintf("--- %s\n+++ b/%s\n(diff omitted: the file is %s; %s %s)\n",
			oldName, p, humanSize(cur.size), in.Mode, humanSize(int64(len(in.Content))))
	case in.Mode == modeAppend:
		a.Diff = unifiedDiff(oldName, "b/"+p, cur.data, append(cur.data[:len(cur.data):len(cur.data)], in.Content...))
	default:
		a.Diff = unifiedDiff(oldName, "b/"+p, cur.data, []byte(in.Content))
	}
	return a, nil
}

type existing struct {
	exists bool
	size   int64
	data   []byte // nil when the file is too large to diff
}

// readExisting reads the current file for the diff; a directory or special
// file in the way is an error.
func readExisting(root *os.Root, p string) (existing, error) {
	st, err := root.Stat(filepath.FromSlash(p))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return existing{data: []byte{}}, nil
	case err != nil:
		return existing{}, fsErr(p, err)
	case st.IsDir():
		return existing{}, fmt.Errorf("%w: %s is a directory", core.ErrInvalid, p)
	case !st.Mode().IsRegular():
		return existing{}, fmt.Errorf("%w: %s is not a regular file", core.ErrInvalid, p)
	case st.Size() > maxDiffInput:
		return existing{exists: true, size: st.Size()}, nil
	}
	b, err := root.ReadFile(filepath.FromSlash(p))
	if err != nil {
		return existing{}, fsErr(p, err)
	}
	if b == nil {
		b = []byte{}
	}
	return existing{exists: true, size: st.Size(), data: b}, nil
}

func (w fsWrite) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in fsWriteArgs
	if err := payloadOf(a, &in); err != nil {
		return nil, err
	}
	p, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	root, err := openRoot(env)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel := filepath.FromSlash(p)
	var oldSize int64
	perm := fileMode
	if st, err := root.Stat(rel); err == nil && st.Mode().IsRegular() {
		oldSize, perm = st.Size(), st.Mode().Perm()
	}
	growth := int64(len(in.Content))
	if in.Mode != modeAppend {
		growth -= oldSize
	}
	if err := w.checkQuota(ctx, env, growth); err != nil {
		return nil, err
	}
	if dir := path.Dir(p); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), dirMode); err != nil {
			return nil, fsErr(dir, err)
		}
	}
	verb := "Wrote"
	switch in.Mode {
	case modeCreate:
		err = writeNew(root, rel, in.Content)
		verb = "Created"
	case modeAppend:
		err = appendTo(root, rel, in.Content)
		verb = "Appended"
	default:
		err = replace(root, rel, in.Content, perm)
	}
	if err != nil {
		return nil, fsErr(p, err)
	}
	return &core.ToolResult{Content: fmt.Sprintf("%s %d bytes to %s.", verb, len(in.Content), p)}, nil
}

// checkQuota refuses writes that would take the workspace over
// sandbox.workspace_quota_mb (from the sandbox's periodic scan).
func (w fsWrite) checkQuota(ctx context.Context, env *core.ToolEnv, growth int64) error {
	sb := w.sandbox
	if sb == nil {
		sb = env.Sandbox
	}
	if sb == nil || growth <= 0 {
		return nil
	}
	used, quota, err := sb.Usage(ctx, env.Principal.Workspace)
	if err != nil {
		return fmt.Errorf("workspace quota check failed: %w", err)
	}
	if quota > 0 && used+growth > quota {
		return fmt.Errorf("workspace is over its quota (%s of %s used); delete files first", humanSize(used), humanSize(quota))
	}
	return nil
}

func writeNew(root *os.Root, rel, content string) error {
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	return finish(f, content)
}

func appendTo(root *os.Root, rel, content string) error {
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
	if err != nil {
		return err
	}
	return finish(f, content)
}

// replace writes a sibling temp file and renames it over the target, so a
// failed write never leaves a half-written file behind. The new file keeps
// the old one's permission bits (an overwritten script stays executable).
func replace(root *os.Root, rel, content string, perm fs.FileMode) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+"."+hex.EncodeToString(rnd[:])+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	if perm != fileMode {
		if err := f.Chmod(perm); err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
			return err
		}
	}
	if err := finish(f, content); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

func finish(f *os.File, content string) error {
	_, werr := io.WriteString(f, content)
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}
