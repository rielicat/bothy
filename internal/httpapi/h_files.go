package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/httpapi/dto"
)

const (
	maxFileName = 128
	// maxListing bounds a directory listing: boxes can create files far
	// faster than a browser can render them.
	maxListing = 5000
)

// sanitizeName keeps a file's base name to letters, digits, '.', '_', '-'
// and spaces, with no leading dots, so it is safe as a single path element.
func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(' ')
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if len(out) > maxFileName {
		ext := path.Ext(out)
		if len(ext) > 16 {
			ext = ""
		}
		base := strings.TrimSuffix(out, ext)
		for len(base)+len(ext) > maxFileName {
			_, size := utf8.DecodeLastRuneInString(base)
			base = base[:len(base)-size]
		}
		out = base + ext
	}
	if out == "" || out == "_" {
		return "file"
	}
	return out
}

// openWorkspace opens <WorkDir>/ws/<id> as an os.Root, creating it when
// create is set. Directories are 0770; under the setgid /work tree they
// inherit group 10001 so boxes can read them.
func (s *Server) openWorkspace(ws core.WorkspaceID, create bool) (*os.Root, error) {
	dir := s.workspaceDir(ws)
	if dir == "" {
		return nil, fmt.Errorf("workspace: %w", core.ErrNotFound)
	}
	if create {
		if err := os.MkdirAll(dir, 0o770); err != nil { //nolint:gosec // group 10001 shares workspaces with tool boxes (SPEC §2.3)
			return nil, err
		}
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("workspace: %w", core.ErrNotFound)
	}
	return root, err
}

// upload streams a multipart "file" part into the session principal's
// workspace at .bothy/in/<upl>/<name>. The session_id field must precede the
// file part (or be given as ?session_id=) so nothing is buffered.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("expected multipart/form-data")
	}
	ctx := r.Context()
	sid := core.SessionID(r.URL.Query().Get("session_id"))
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return badRequest("missing file part")
		}
		if err != nil {
			return badRequest("malformed multipart body: %v", err)
		}
		switch part.FormName() {
		case "session_id":
			b, err := io.ReadAll(io.LimitReader(part, 64))
			if err != nil {
				return badRequest("malformed session_id")
			}
			sid = core.SessionID(strings.TrimSpace(string(b)))
			continue
		case "file":
		default:
			continue
		}
		if sid == "" {
			return badRequest("session_id must come before the file part")
		}
		sess, err := s.session(ctx, a, sid)
		if err != nil {
			return err
		}
		pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, sess.Principal)
		if err != nil {
			return err
		}
		up, err := s.storeUpload(r, pr, sess.ID, part)
		if err != nil {
			return err
		}
		return ok(w, dto.Upload{ID: up.ID, Name: up.Name, MIME: up.MIME, Size: up.Size})
	}
}

func (s *Server) storeUpload(r *http.Request, pr *core.Principal, sid core.SessionID, part io.Reader) (*core.Upload, error) {
	ctx := r.Context()
	maxSize := s.uploadLimit()
	var used, quota int64
	if s.d.Sandbox != nil {
		var err error
		if used, quota, err = s.d.Sandbox.Usage(ctx, pr.Workspace); err == nil && quota > 0 && used >= quota {
			return nil, badRequest("workspace quota exceeded")
		}
	}
	name := "file"
	if p, ok := part.(interface{ FileName() string }); ok {
		name = sanitizeName(p.FileName())
	}
	root, err := s.openWorkspace(pr.Workspace, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	id := core.NewID(core.PrefixUpload)
	dir := path.Join(".bothy", "in", id)
	if err := root.MkdirAll(dir, 0o770); err != nil {
		return nil, err
	}
	rel := path.Join(dir, name)
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o660)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(part, head)
	head = head[:n]
	written, err := io.Copy(f, io.MultiReader(bytes.NewReader(head), io.LimitReader(part, maxSize+1-int64(n))))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	fail := func(e error) (*core.Upload, error) {
		_ = root.RemoveAll(dir)
		return nil, e
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return fail(newError(http.StatusRequestEntityTooLarge, "bad_request", fmt.Sprintf("uploads are limited to %d MB", maxSize>>20)))
		}
		return fail(err)
	}
	if written > maxSize {
		return fail(newError(http.StatusRequestEntityTooLarge, "bad_request", fmt.Sprintf("uploads are limited to %d MB", maxSize>>20)))
	}
	if quota > 0 && used+written > quota {
		return fail(badRequest("workspace quota exceeded"))
	}
	up := &core.Upload{ID: id, Principal: pr.ID, Session: sid, Name: name, MIME: sniffType(name, head), Size: written, Ref: rel,
		CreatedAt: s.now()}
	if err := s.d.Store.Uploads().Create(ctx, up); err != nil {
		return fail(err)
	}
	return up, nil
}

// sniffType trusts the content, not the client. The extension only refines
// a generic verdict (plain text to text/csv, binary to a specific type), and
// never into a type a browser would execute.
func sniffType(name string, head []byte) string {
	t := http.DetectContentType(head)
	byExt := mime.TypeByExtension(path.Ext(name))
	switch {
	case byExt == "", strings.Contains(byExt, "html"), strings.Contains(byExt, "javascript"), strings.Contains(byExt, "svg"):
		return t
	case t == "application/octet-stream":
		return byExt
	case strings.HasPrefix(t, "text/plain") && strings.HasPrefix(byExt, "text/"):
		return byExt
	}
	return t
}

type fileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Dir     bool      `json:"dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// workspaceFiles lists a directory (JSON) or downloads a file from the
// principal's workspace through os.Root, which refuses "..", absolute paths
// and symlinks that leave the workspace.
func (s *Server) workspaceFiles(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	q := r.URL.Query()
	pid := core.PrincipalID(q.Get("principal"))
	if pid == "" {
		pid = core.UserPrincipal(a.User)
	}
	pr, err := s.d.Store.Principals().Get(ctx, a.Viewer, pid)
	if err != nil {
		return err
	}
	rel := path.Clean("/" + q.Get("path"))[1:]
	if rel == "" {
		rel = "."
	}
	root, err := s.openWorkspace(pr.Workspace, false)
	if errors.Is(err, core.ErrNotFound) && rel == "." {
		return ok(w, []fileEntry{})
	}
	if err != nil {
		return err
	}
	defer root.Close()
	// O_NONBLOCK: boxes write this tree, and opening a FIFO they planted
	// would otherwise block this goroutine until something writes to it.
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// Missing, unreadable and escaping paths all look the same.
		return errNotFound
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.IsDir() {
		des, err := f.ReadDir(maxListing + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(des) > maxListing {
			des = des[:maxListing]
			w.Header().Set("X-Bothy-Truncated", "true")
		}
		out := make([]fileEntry, 0, len(des))
		for _, de := range des {
			info, err := de.Info()
			if err != nil {
				continue
			}
			out = append(out, fileEntry{Name: de.Name(), Path: path.Join(rel, de.Name()), Dir: de.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
		}
		slices.SortFunc(out, func(x, y fileEntry) int { return strings.Compare(x.Path, y.Path) })
		return ok(w, out)
	}
	if !fi.Mode().IsRegular() {
		return errNotFound
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": sanitizeName(fi.Name())}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}
