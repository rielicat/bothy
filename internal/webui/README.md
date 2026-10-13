# internal/webui

`webui.Handler` serves the SPA that `make ui` builds into `dist/` and embeds
with `go:embed` (a committed placeholder `index.html` keeps the build working
without Node). Files are indexed once at startup (content type, ETag per
representation) and streamed from the embedded FS, never copied to the heap.
When a `<file>.gz` sibling exists and the client accepts gzip, the compressed
file is sent with `Content-Encoding: gzip` and `Vary: Accept-Encoding`.
Hashed `/assets/*` are `public, max-age=31536000, immutable`, `index.html` is
`no-store`, conditional and range requests go through `http.ServeContent`.
Unknown paths without an extension fall back to `index.html` (history
router); missing assets and anything under `/api` or `/v1` are 404 so a
script tag never receives HTML.
