package webui

import "embed"

// dist is the `make ui` output. A committed placeholder index.html keeps the
// embed compiling on machines without node.
//
//go:embed dist
var dist embed.FS
