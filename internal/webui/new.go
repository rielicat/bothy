package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// Handler serves hashed assets as immutable, index.html as no-store, and falls
// back to index.html for unknown non-/api, non-/v1 paths.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees dist/ exists
	}
	return newHandler(sub)
}

type asset struct {
	name  string // path inside the FS
	ctype string
	etag  string
	gz    string // name of the pre-gzipped variant, "" when absent
	gzTag string
}

type handler struct {
	fsys   fs.FS
	assets map[string]*asset // URL path without the leading slash
}

// newHandler indexes fsys once: content types and ETags are computed up
// front, file bodies are streamed from the embedded FS on each request so
// they are never copied onto the heap.
func newHandler(fsys fs.FS) *handler {
	h := &handler{fsys: fsys, assets: map[string]*asset{}}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".gz") {
			return nil
		}
		a := &asset{name: p, ctype: contentType(p), etag: etagOf(fsys, p)}
		if _, err := fs.Stat(fsys, p+".gz"); err == nil {
			a.gz, a.gzTag = p+".gz", etagOf(fsys, p+".gz")
		}
		h.assets[p] = a
		return nil
	})
	return h
}

func etagOf(fsys fs.FS, name string) string {
	f, err := fsys.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return ""
	}
	return `"` + hex.EncodeToString(sum.Sum(nil)[:12]) + `"`
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "api" || p == "v1" || strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "v1/") {
		http.NotFound(w, r)
		return
	}
	a, ok := h.assets[p]
	switch {
	case ok && p != "index.html":
	case strings.HasPrefix(p, "assets/") || path.Ext(p) != "" && p != "index.html":
		// A missing script or stylesheet must not receive index.html as its body.
		http.NotFound(w, r)
		return
	default:
		if a, ok = h.assets["index.html"]; !ok {
			http.NotFound(w, r)
			return
		}
	}
	h.serve(w, r, a, strings.HasPrefix(p, "assets/"))
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, a *asset, immutable bool) {
	hdr := w.Header()
	hdr.Set("Content-Type", a.ctype)
	hdr.Set("X-Content-Type-Options", "nosniff")
	name, tag := a.name, a.etag
	if a.gz != "" {
		hdr.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			name, tag = a.gz, a.gzTag
			hdr.Set("Content-Encoding", "gzip")
		}
	}
	switch {
	case a.name == "index.html":
		hdr.Set("Cache-Control", "no-store")
	case immutable:
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		hdr.Set("Cache-Control", "no-cache")
	}
	if tag != "" {
		hdr.Set("ETag", tag)
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		hdr.Del("ETag")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, f)
		}
		return
	}
	http.ServeContent(w, r, "", time.Time{}, rs)
}

// acceptsGzip reports whether Accept-Encoding lists gzip with a non-zero q.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.TrimSpace(enc) != "*" {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}
