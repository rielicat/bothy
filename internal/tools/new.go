package tools

import (
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type RegistryDeps struct {
	Config config.View
	Log    *slog.Logger
}

// Registry implements core.ToolRegistry.
type Registry struct{ core.ToolRegistry }

func NewRegistry(d RegistryDeps) *Registry { return &Registry{} }

func (r *Registry) Register(t core.Tool) error { return nil }

type BuiltinDeps struct {
	Config    config.View
	Messenger core.Messenger // message tool, bound to env.Chat
	Sandbox   core.Sandbox   // workspace quota check before writes
	Clock     core.Clock
}

// Builtins returns fs_read, fs_write, fs_list, fs_delete, message.
func Builtins(d BuiltinDeps) []core.Tool { return nil }
