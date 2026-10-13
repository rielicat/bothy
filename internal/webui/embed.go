package webui

import "embed"

// dist is the `make ui` output. A committed placeholder index.html keeps the
// embed compiling on machines without node. "all:" keeps Rollup chunks whose
// names start with "_"; dotfiles are skipped when the handler indexes dist.
//
//go:embed all:dist
var dist embed.FS
