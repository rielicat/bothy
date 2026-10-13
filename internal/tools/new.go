package tools

import (
	"io"
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type RegistryDeps struct {
	Config config.View
	Log    *slog.Logger
}

// Registry implements core.ToolRegistry.
type Registry struct {
	log   *slog.Logger
	state registryState
}

var _ core.ToolRegistry = (*Registry)(nil)

// NewRegistry returns a registry that already holds tool_search and the
// use_tool schema entry.
func NewRegistry(d RegistryDeps) *Registry {
	log := d.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	r := &Registry{log: log}
	r.state.tools = map[string]*entry{}
	for _, t := range []core.Tool{&toolSearch{reg: r}, useTool{}} {
		if err := r.add(t, true); err != nil {
			panic("tools: built-in meta tool rejected: " + err.Error())
		}
	}
	return r
}

// Register implements core.ToolRegistry. The tool's spec is validated and
// normalised (canonical schema, computed hash); a name collision is
// core.ErrConflict, an invalid manifest or schema core.ErrInvalid.
func (r *Registry) Register(t core.Tool) error { return r.add(t, false) }

type BuiltinDeps struct {
	Config    config.View
	Messenger core.Messenger // message tool, bound to env.Chat
	Sandbox   core.Sandbox   // workspace quota check before writes
	Clock     core.Clock
}

// Builtins returns fs_read, fs_write, fs_list, fs_delete, message.
func Builtins(d BuiltinDeps) []core.Tool {
	return []core.Tool{
		fsRead{},
		fsWrite{sandbox: d.Sandbox},
		fsList{},
		fsDelete{},
		message{messenger: d.Messenger},
	}
}
