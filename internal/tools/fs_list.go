package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rielicat/bothy/internal/core"
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
	if in.Recursive {
		err = walkDir(ctx, root, p, func(name string, d fs.DirEntry) error {
			return add(relTo(p, name), d)
		})
	} else {
		var entries []fs.DirEntry
		entries, err = readDir(root, filepath.FromSlash(p))
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

// walkDir visits the tree under dir in lexical order like fs.WalkDir, opening
// every folder with readDir. A folder that is no longer one when it is opened
// (box code swapped it) is listed but not descended into.
func walkDir(ctx context.Context, root *os.Root, dir string, fn func(name string, d fs.DirEntry) error) error {
	entries, err := readDir(root, filepath.FromSlash(dir))
	if err != nil {
		return err
	}
	for _, d := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := path.Join(dir, d.Name())
		if err := fn(name, d); err != nil {
			return err
		}
		if !d.IsDir() {
			continue
		}
		err := walkDir(ctx, root, name, fn)
		var pe *fs.PathError
		if errors.As(err, &pe) && errors.Is(pe.Err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
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
