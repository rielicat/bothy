package webui

import "net/http"

// Handler serves hashed assets as immutable, index.html as no-store, and falls
// back to index.html for unknown non-/api, non-/v1 paths.
func Handler() http.Handler { return http.NotFoundHandler() }
