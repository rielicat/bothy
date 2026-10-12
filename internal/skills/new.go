package skills

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

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
type Library struct {
	store core.Store
	cfg   config.View
	audit core.Auditor
	bus   core.Bus
	clock core.Clock
	log   *slog.Logger
	dir   string // absolute skills directory

	// mu serialises every change to skill rows and directories so a rescan
	// never races an install, an enable or a drift mark.
	mu sync.Mutex
	// enabled counts enabled, hash-matching skills; skill_load and skill_read
	// are offered only while it is non-zero.
	enabled atomic.Int64
	banner  string // text of the active skills_changed banner, "" = none
}

var (
	_ core.Skills        = (*Library)(nil)
	_ core.CheckProvider = (*Library)(nil)
)

// New resolves the skills directory, creates it when missing and runs an
// initial scan so the index is current before the first run.
func New(d Deps) (*Library, error) {
	if d.Store == nil || d.Config == nil {
		return nil, fmt.Errorf("skills: store and config are required: %w", core.ErrInvalid)
	}
	dir := d.Config.Current().Skills.Dir
	if dir == "" {
		if d.WorkDir == "" {
			return nil, fmt.Errorf("skills: neither skills.dir nor a work dir is set: %w", core.ErrInvalid)
		}
		dir = filepath.Join(d.WorkDir, "skills")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return nil, fmt.Errorf("skills: create %s: %w", dir, err)
	}
	l := &Library{store: d.Store, cfg: d.Config, audit: d.Audit, bus: d.Bus, clock: d.Clock, log: d.Log, dir: dir}
	if l.clock == nil {
		l.clock = core.SystemClock{}
	}
	if l.log == nil {
		l.log = slog.New(slog.DiscardHandler)
	}
	l.log = l.log.With("module", "skills")
	if err := l.Rescan(context.Background()); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Library) Tools() []core.Tool { return []core.Tool{loadTool{l}, readTool{l}} }

func (l *Library) Checks() []core.Check { return []core.Check{changedCheck{l}} } // skills.changed (fix: disable)
