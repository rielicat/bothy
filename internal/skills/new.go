package skills

import (
	"log/slog"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store   core.Store
	Config  config.View
	Audit   core.Auditor
	Bus     core.Bus // skills_changed banner
	WorkDir string   // resolved work dir; skills.dir "" = <WorkDir>/skills
	Clock   core.Clock
	Log     *slog.Logger
}

// Library implements core.Skills and core.CheckProvider.
type Library struct{ core.Skills }

func New(d Deps) (*Library, error) { return &Library{}, nil }

func (l *Library) Tools() []core.Tool   { return nil }
func (l *Library) Checks() []core.Check { return nil } // skills.changed (fix: disable)
