package webui

import (
	"compress/gzip"
	"io"
	"net/http"
	"testing"
	"testing/fstest"
)

// The build embeds only the gzip variant of compressible assets (raw copies
// would only serve clients without gzip and add ~0.4 MB to the binary), so
// such a client gets the asset decompressed on the fly.
func TestGzipOnlyAssets(t *testing.T) {
	h := newHandler(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>Bothy</title>")},
		"index.html.gz":       {Data: gz(t, "<!doctype html><title>Bothy</title>")},
		"assets/app-1.js.gz":  {Data: gz(t, "console.log('only gz')")},
		"assets/app-2.css.gz": {Data: gz(t, "body{color:red}")},
	})
	rec := get(t, h, "GET", "/assets/app-1.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip client: %d %v", rec.Code, rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != "console.log('only gz')" {
		t.Fatalf("gzip body = %q", b)
	}
	gzTag := rec.Header().Get("ETag")

	rec = get(t, h, "GET", "/assets/app-1.js", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "console.log('only gz')" {
		t.Fatalf("identity client: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	plainTag := rec.Header().Get("ETag")
	if rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || rec.Header().Get("Vary") != "Accept-Encoding" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || plainTag == "" || plainTag == gzTag {
		t.Fatalf("identity headers: %v (gz tag %q)", rec.Header(), gzTag)
	}
	if rec := get(t, h, "GET", "/assets/app-1.js", map[string]string{"If-None-Match": plainTag}); rec.Code != http.StatusNotModified {
		t.Fatalf("conditional identity GET = %d", rec.Code)
	}
	if rec := get(t, h, "HEAD", "/assets/app-2.css", nil); rec.Code != 200 || rec.Body.Len() != 0 ||
		rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("HEAD = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := get(t, h, "GET", "/assets/app-3.js", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
}
