package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const fsReadName = "fs_read"

type fsRead struct{}

type fsReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (fsRead) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        fsReadName,
			Description: "Read a text file in your workspace (up to 256 KB per call; use offset/limit for line ranges).",
			Schema: objectSchema(`"path":{"type":"string","description":"Workspace-relative path"},`+
				`"offset":{"type":"integer","minimum":1,"description":"First line, 1-based"},`+
				`"limit":{"type":"integer","minimum":1,"description":"Maximum number of lines"}`, "path"),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Read file", SideEffect: core.SERead, FSPaths: []string{"**"}, Taint: core.TaintPrivate,
			SpaceOK: true, Core: true, Timeout: 30 * time.Second, MaxOutputKB: readCap >> 10,
		},
	}
}

func (fsRead) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in fsReadArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	p, err := cleanPath(in.Path)
	if err != nil {
		return nil, err
	}
	if in.Offset < 0 || in.Limit < 0 {
		return nil, fmt.Errorf("%w: offset and limit must be positive", core.ErrInvalid)
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
	if st.IsDir() {
		return nil, fmt.Errorf("%w: %s is a directory; use fs_list", core.ErrInvalid, p)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", core.ErrInvalid, p)
	}
	a, err := newAction(fsReadName, core.ActFSRead, core.SERead, fsReadArgs{Path: p, Offset: in.Offset, Limit: in.Limit})
	if err != nil {
		return nil, err
	}
	a.Paths = []string{p}
	return a, nil
}

func (fsRead) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in fsReadArgs
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
	f, st, err := openRegular(root, filepath.FromSlash(p), os.O_RDONLY, 0)
	if errors.Is(err, errNotRegular) {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if err != nil {
		return nil, fsErr(p, err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, sniffLen)
	head, err := br.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, fsErr(p, err)
	}
	if isBinary(head) {
		return &core.ToolResult{Content: fmt.Sprintf("%s is a binary file (%s, %s); not shown.", p, humanSize(st.Size()), http.DetectContentType(head))}, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if in.Offset == 0 && in.Limit == 0 {
		return readWhole(br, p, st.Size())
	}
	return readLines(ctx, br, p, max(in.Offset, 1), in.Limit)
}

func readWhole(r io.Reader, p string, size int64) (*core.ToolResult, error) {
	b, err := io.ReadAll(io.LimitReader(r, readCap+1))
	if err != nil {
		return nil, fsErr(p, err)
	}
	if len(b) <= readCap {
		return &core.ToolResult{Content: string(b)}, nil
	}
	b = cutAtLine(b[:readCap])
	return &core.ToolResult{
		Content:   string(b) + fmt.Sprintf("\n[… truncated at %s of %s; read more with offset/limit]", humanSize(int64(len(b))), humanSize(size)),
		Truncated: true,
	}, nil
}

func readLines(ctx context.Context, r *bufio.Reader, p string, offset, limit int) (*core.ToolResult, error) {
	var out bytes.Buffer
	line, last := 0, 0
	truncated, more := false, false
	for {
		// Skipping to a far offset reads the whole file; honour the timeout.
		if line%4096 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		keep := 0
		if line+1 >= offset {
			keep = readCap - out.Len() + 1
		}
		b, n, err := readLine(r, keep)
		if n > 0 {
			line++
			switch {
			case line < offset:
			case limit > 0 && line >= offset+limit:
				more = true
			case n > readCap-out.Len():
				if out.Len() == 0 {
					out.Write(cutAtLine(b[:min(len(b), readCap)]))
					last = line
				}
				truncated, more = true, true
			default:
				out.Write(b)
				last = line
			}
		}
		if more || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fsErr(p, err)
		}
	}
	content := out.String()
	switch {
	case last == 0:
		content = fmt.Sprintf("[%s has %d lines; nothing at line %d]", p, line, offset)
	case truncated:
		content += fmt.Sprintf("\n[… stopped in line %d at 256 KB; continue with offset %d]", last, last+1)
	case more:
		content += fmt.Sprintf("\n[… more lines follow; continue with offset %d]", last+1)
	}
	return &core.ToolResult{Content: content, Truncated: truncated}, nil
}

// readLine reads one line (newline included) but keeps at most keep bytes of
// it, so a huge single-line file never sits in memory; n is the full length.
func readLine(r *bufio.Reader, keep int) (kept []byte, n int, err error) {
	for {
		frag, err := r.ReadSlice('\n')
		n += len(frag)
		if room := keep - len(kept); room > 0 {
			kept = append(kept, frag[:min(room, len(frag))]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return kept, n, err
		}
	}
}

// cutAtLine drops a trailing partial line (or partial rune) after a cap.
func cutAtLine(b []byte) []byte {
	if i := bytes.LastIndexByte(b, '\n'); i > 0 {
		return b[:i+1]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b
}
