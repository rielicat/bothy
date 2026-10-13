package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const fsListName = "fs_list"

var errListFull = errors.New("listing full")

type fsList struct{}

type fsListArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

func (fsList) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        fsListName,
			Description: "List files in a workspace folder (default: the workspace root). Folders end with /.",
			Schema: objectSchema(`"path":{"type":"string","description":"Workspace-relative folder"},` +
				`"recursive":{"type":"boolean"}`),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "List files", SideEffect: core.SERead, FSPaths: []string{"**"},
			SpaceOK: true, Core: true, Timeout: 30 * time.Second, MaxOutputKB: 64,
		},
	}
}

func (fsList) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in fsListArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if in.Path == "" {
		in.Path = "."
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
	st, err := root.Stat(filepath.FromSlash(p))
	if err != nil {
		return nil, fsErr(p, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a folder; use fs_read", core.ErrInvalid, p)
	}
	a, err := newAction(fsListName, core.ActFSRead, core.SERead, fsListArgs{Path: p, Recursive: in.Recursive})
	if err != nil {
		return nil, err
	}
	a.Paths = []string{p}
	return a, nil
}

func (fsList) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in fsListArgs
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
	var lines []string
	full := false
	add := func(rel string, d fs.DirEntry) error {
		if len(lines) >= listCap {
			full = true
			return errListFull
		}
		lines = append(lines, describe(rel, d))
		return nil
	}
	fsys := root.FS()
	if in.Recursive {
		err = fs.WalkDir(fsys, p, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if name == p {
				return nil
			}
			return add(relTo(p, name), d)
		})
	} else {
		var entries []fs.DirEntry
		entries, err = fs.ReadDir(fsys, p)
		for _, d := range entries {
			if add(d.Name(), d) != nil {
				break
			}
		}
	}
	if err != nil && !errors.Is(err, errListFull) {
		return nil, fsErr(p, err)
	}
	if len(lines) == 0 {
		return &core.ToolResult{Content: p + " is empty."}, nil
	}
	out := strings.Join(lines, "\n")
	if full {
		out += fmt.Sprintf("\n[… stopped after %d entries]", listCap)
	}
	return &core.ToolResult{Content: out, Truncated: full}, nil
}

func relTo(base, name string) string {
	if base == "." {
		return name
	}
	return strings.TrimPrefix(name, base+"/")
}

func describe(rel string, d fs.DirEntry) string {
	switch {
	case d.IsDir():
		return rel + "/"
	case d.Type()&fs.ModeSymlink != 0:
		return rel + "@"
	}
	if info, err := d.Info(); err == nil {
		return rel + "  " + humanSize(info.Size())
	}
	return rel
}
