package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testFS(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>Bothy</title>")},
		"index.html.gz":           {Data: gz(t, "<!doctype html><title>Bothy</title>")},
		"assets/app-1a2b3c.js":    {Data: []byte("console.log('hi')")},
		"assets/app-1a2b3c.js.gz": {Data: gz(t, "console.log('hi')")},
		"assets/app-9z8y.css":     {Data: []byte("body{}")},
		"manifest.webmanifest":    {Data: []byte(`{"name":"Bothy"}`)},
	}
}

func get(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUnderscoreChunksServedDotfilesHidden(t *testing.T) {
	fsys := testFS(t)
	fsys["assets/_commonjsHelpers-4f2a.js"] = &fstest.MapFile{Data: []byte("export{}")}
	fsys[".DS_Store"] = &fstest.MapFile{Data: []byte("junk")}
	fsys["assets/.hidden.js"] = &fstest.MapFile{Data: []byte("junk")}
	h := newHandler(fsys)
	if rec := get(t, h, "GET", "/assets/_commonjsHelpers-4f2a.js", nil); rec.Code != 200 {
		t.Fatalf("underscore chunk = %d", rec.Code)
	}
	for _, p := range []string{"/.DS_Store", "/assets/.hidden.js"} {
		if rec := get(t, h, "GET", p, nil); rec.Code != 404 {
			t.Fatalf("%s = %d, want 404", p, rec.Code)
		}
	}
}

func TestAssetsGzipETagAndCaching(t *testing.T) {
	h := newHandler(testFS(t))
	rec := get(t, h, "GET", "/assets/app-1a2b3c.js", map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip asset: %d %v", rec.Code, rec.Header())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q", got)
	}
	if rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers = %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != "console.log('hi')" {
		t.Fatalf("body = %q", b)
	}
	gzTag := rec.Header().Get("ETag")

	rec = get(t, h, "GET", "/assets/app-1a2b3c.js", map[string]string{"Accept-Encoding": "gzip;q=0"})
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "console.log('hi')" {
		t.Fatalf("identity fallback: %v %q", rec.Header(), rec.Body.String())
	}
	plainTag := rec.Header().Get("ETag")
	if gzTag == "" || plainTag == "" || gzTag == plainTag {
		t.Fatalf("etags gz=%q plain=%q must differ per representation", gzTag, plainTag)
	}
	rec = get(t, h, "GET", "/assets/app-1a2b3c.js", map[string]string{"If-None-Match": plainTag})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d", rec.Code)
	}
	rec = get(t, h, "GET", "/assets/app-9z8y.css", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" || rec.Header().Get("Vary") != "" {
		t.Fatalf("css without .gz: %d %v", rec.Code, rec.Header())
	}
}

func TestSPAFallback(t *testing.T) {
	h := newHandler(testFS(t))
	for _, p := range []string{"/", "/index.html", "/chat/ses_01", "/settings/general", "/approvals"} {
		rec := get(t, h, "GET", p, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Bothy</title>") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s cache-control = %q", p, rec.Header().Get("Cache-Control"))
		}
	}
	for _, p := range []string{"/assets/missing.js", "/api/sessions", "/v1/models", "/api", "/favicon.ico"} {
		if rec := get(t, h, "GET", p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", p, rec.Code)
		}
	}
	if rec := get(t, h, "POST", "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
	rec := get(t, h, "HEAD", "/assets/app-9z8y.css", nil)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %q", rec.Code, rec.Body.String())
	}
	rec = get(t, h, "GET", "/manifest.webmanifest", nil)
	if rec.Header().Get("Content-Type") != "application/manifest+json" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest headers = %v", rec.Header())
	}
	if rec := get(t, h, "GET", "/../../etc/passwd", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Bothy") {
		t.Fatalf("traversal must stay inside the FS: %d", rec.Code)
	}
}

func TestEmbeddedPlaceholder(t *testing.T) {
	rec := get(t, Handler(), "GET", "/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Bothy</title>") {
		t.Fatalf("placeholder: %d %q", rec.Code, rec.Body.String())
	}
}
