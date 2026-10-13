package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const fsDeleteName = "fs_delete"

type fsDelete struct{}

type fsDeleteArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

func (fsDelete) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        fsDeleteName,
			Description: "Delete a file or, with recursive, a folder in your workspace. This cannot be undone.",
			Schema: objectSchema(`"path":{"type":"string","description":"Workspace-relative path"},`+
				`"recursive":{"type":"boolean","description":"Required to delete a non-empty folder"}`, "path"),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Delete file", SideEffect: core.SEIrreversible, FSPaths: []string{"**"},
			Timeout: 30 * time.Second, MaxOutputKB: 4,
		},
	}
}

// Plan shows a regular text file's removal as a diff, so the card says what
// is lost; folders list their entry count instead.
func (fsDelete) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in fsDeleteArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	p, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	if p == "." {
		return nil, fmt.Errorf("%w: refusing to delete the workspace itself", core.ErrInvalid)
	}
	root, err := openRoot(env)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel := filepath.FromSlash(p)
	st, err := root.Lstat(rel)
	if err != nil {
		return nil, fsErr(p, err)
	}
	var diff string
	switch {
	case st.IsDir():
		entries, err := fs.ReadDir(root.FS(), p)
		if err != nil {
			return nil, fsErr(p, err)
		}
		if len(entries) > 0 && !in.Recursive {
			return nil, fmt.Errorf("%w: %s is a folder with %d entries; set recursive to delete it", core.ErrInvalid, p, len(entries))
		}
		diff = fmt.Sprintf("--- a/%s/\n+++ %s\n(folder with %d top-level entries)\n", p, devNull, len(entries))
	case st.Mode().IsRegular() && st.Size() <= maxDiffInput:
		b, err := root.ReadFile(rel)
		if err != nil {
			return nil, fsErr(p, err)
		}
		diff = unifiedDiff("a/"+p, devNull, b, nil)
	}
	a, err := newAction(fsDeleteName, core.ActFSDelete, core.SEIrreversible, fsDeleteArgs{Path: p, Recursive: in.Recursive})
	if err != nil {
		return nil, err
	}
	a.Paths = []string{p}
	a.Diff = diff
	return a, nil
}

func (fsDelete) Exec(_ context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in fsDeleteArgs
	if err := payloadOf(a, &in); err != nil {
		return nil, err
	}
	p, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	if p == "." {
		return nil, fmt.Errorf("%w: refusing to delete the workspace itself", core.ErrInvalid)
	}
	root, err := openRoot(env)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel := filepath.FromSlash(p)
	if in.Recursive {
		err = root.RemoveAll(rel)
	} else {
		err = root.Remove(rel)
	}
	if err != nil {
		return nil, fsErr(p, err)
	}
	return &core.ToolResult{Content: "Deleted " + p + "."}, nil
}
