package httpapi

import (
	"archive/tar"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const maxPersona = 16 << 10

var memoryFiles = []core.MemoryFile{core.FileMemory, core.FileUser, core.FileHeartbeat}

func memFile(name string) (core.MemoryFile, error) {
	n := strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".MD"))
	if f := core.MemoryFile(n); slices.Contains(memoryFiles, f) {
		return f, nil
	}
	return "", errNotFound
}

func (s *Server) memoryCap(f core.MemoryFile) int {
	m := s.cfg().Memory
	switch f {
	case core.FileUser:
		return m.UserMaxBytes
	case core.FileHeartbeat:
		return m.HeartbeatMaxBytes
	}
	return m.MemoryMaxBytes
}

// memPrincipal reads ?principal= (default: the caller's own) and checks it
// against the Viewer, so foreign principals are 404.
func memPrincipal(r *http.Request, a core.Actor) (core.PrincipalID, error) {
	p := core.PrincipalID(r.URL.Query().Get("principal"))
	if p == "" {
		p = core.UserPrincipal(a.User)
	}
	if !a.Viewer.Can(p) {
		return "", errNotFound
	}
	return p, nil
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	f, err := memFile(r.PathValue("file"))
	if err != nil {
		return err
	}
	p, err := memPrincipal(r, a)
	if err != nil {
		return err
	}
	content, etag, err := s.d.Memory.Read(r.Context(), a.Viewer, p, f)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", quoteETag(etag))
	return ok(w, dto.MemoryDoc{File: f, Content: content, ETag: etag, CapBytes: s.memoryCap(f)})
}

func quoteETag(t string) string { return `"` + t + `"` }

func unquoteETag(t string) string {
	t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "W/"))
	return strings.Trim(t, `"`)
}

func (s *Server) putMemory(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	f, err := memFile(r.PathValue("file"))
	if err != nil {
		return err
	}
	p, err := memPrincipal(r, a)
	if err != nil {
		return err
	}
	match := r.Header.Get("If-Match")
	if match == "" {
		return newError(http.StatusPreconditionRequired, "bad_request", "If-Match with the document's etag is required")
	}
	var req dto.MemoryPutReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if !utf8.ValidString(req.Content) {
		return badRequest("content must be UTF-8")
	}
	if c := s.memoryCap(f); c > 0 && len(req.Content) > c {
		return badRequest("%s.md is limited to %d bytes (%d given)", f, c, len(req.Content))
	}
	etag, err := s.d.Memory.Write(r.Context(), a.Viewer, p, f, req.Content, unquoteETag(match), a.User)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", quoteETag(etag))
	return ok(w, dto.MemoryDoc{File: f, Content: req.Content, ETag: etag, CapBytes: s.memoryCap(f)})
}

func (s *Server) memoryHistory(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	f, err := memFile(r.PathValue("file"))
	if err != nil {
		return err
	}
	p, err := memPrincipal(r, a)
	if err != nil {
		return err
	}
	pg, err := page(r)
	if err != nil {
		return err
	}
	evs, err := s.d.Memory.History(r.Context(), a.Viewer, p, f, pg)
	if err != nil {
		return err
	}
	return ok(w, listOf(evs, pg, func(e *core.MemoryEvent) int64 { return e.ID }))
}

// memoryExport streams a tar of the three memory files and every memory
// event (events.jsonl, oldest first) as an attachment.
func (s *Server) memoryExport(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	p, err := memPrincipal(r, a)
	if err != nil {
		return err
	}
	ctx := r.Context()
	type doc struct {
		f       core.MemoryFile
		content string
	}
	var docs []doc
	var events []*core.MemoryEvent
	for _, f := range memoryFiles {
		content, _, err := s.d.Memory.Read(ctx, a.Viewer, p, f)
		if err != nil {
			return err
		}
		docs = append(docs, doc{f, content})
		evs, err := s.allHistory(ctx, a.Viewer, p, f)
		if err != nil {
			return err
		}
		events = append(events, evs...)
	}
	slices.SortFunc(events, func(x, y *core.MemoryEvent) int { return cmp.Compare(x.ID, y.ID) })
	var jsonl strings.Builder
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		jsonl.Write(b)
		jsonl.WriteByte('\n')
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-tar")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "bothy-memory-" + string(p) + ".tar"}))
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	tw := tar.NewWriter(w)
	now := s.now()
	add := func(name, body string) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := io.WriteString(tw, body)
		return err
	}
	for _, d := range docs {
		if err := add(string(d.f)+".md", d.content); err != nil {
			return aborted(err)
		}
	}
	if err := add("events.jsonl", jsonl.String()); err != nil {
		return aborted(err)
	}
	if err := tw.Close(); err != nil {
		return aborted(err)
	}
	return nil
}

func (s *Server) allHistory(ctx context.Context, v core.Viewer, p core.PrincipalID, f core.MemoryFile) ([]*core.MemoryEvent, error) {
	var out []*core.MemoryEvent
	pg := core.Page{Limit: 500}
	for {
		evs, err := s.d.Memory.History(ctx, v, p, f, pg)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
		if len(evs) < pg.Limit || evs[len(evs)-1].ID <= 1 {
			return out, nil
		}
		next := evs[len(evs)-1].ID
		if pg.Before != 0 && next >= pg.Before {
			return out, nil
		}
		pg.Before = next
	}
}

// personaRoot opens the data dir; agent.persona_file must be a plain file
// name inside it.
func (s *Server) personaRoot() (*os.Root, string, error) {
	name := s.cfg().Agent.PersonaFile
	if name == "" {
		name = "SOUL.md"
	}
	if name != filepath.Base(name) || name == "." || name == ".." || s.d.DataDir == "" {
		return nil, "", newError(http.StatusServiceUnavailable, "unavailable", "agent.persona_file must be a file name inside the data directory")
	}
	root, err := os.OpenRoot(s.d.DataDir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}

func (s *Server) getPersona(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	root, name, err := s.personaRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return ok(w, map[string]string{"content": ""})
	}
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPersona*4))
	if err != nil {
		return err
	}
	return ok(w, map[string]string{"content": string(b)})
}

func (s *Server) putPersona(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	var req struct {
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if len(req.Content) > maxPersona {
		return badRequest("the persona is limited to %d bytes", maxPersona)
	}
	if !utf8.ValidString(req.Content) {
		return badRequest("content must be UTF-8")
	}
	root, name, err := s.personaRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := writeAtomic(root, name, []byte(req.Content)); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(req.Content))
	s.audit(r, a, "persona.update", name, "ok", map[string]any{"bytes": len(req.Content), "sha256": hex.EncodeToString(sum[:8])})
	return noContent(w)
}

// writeAtomic writes name inside root via a temp file, fsync and rename, 0600.
func writeAtomic(root *os.Root, name string, data []byte) error {
	tmp := "." + name + "." + requestID() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}

func (s *Server) constitution(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	v, text := s.d.Runner.Constitution()
	return ok(w, map[string]string{"version": v, "content": text})
}
