package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// overCapCheck reports memory files larger than their configured cap (after
// a cap was lowered, or a hand edit on disk). Every agent add to such a file
// fails until it is shortened, so the owner should hear about it.
type overCapCheck struct{ s *Service }

func (overCapCheck) ID() string { return "memory.over_cap" }

func (c overCapCheck) Run(ctx context.Context) []core.Finding {
	dirs, err := os.ReadDir(c.s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []core.Finding{c.finding(core.SevLow, "Cannot read the memory directory", err.Error())}
	}
	var over []string
	for _, d := range dirs {
		p := core.PrincipalID(d.Name())
		if !d.IsDir() {
			continue
		}
		if _, err := c.s.checkPrincipal(ctx, p); err != nil {
			continue
		}
		for _, f := range files {
			info, err := os.Stat(filepath.Join(c.s.dir, d.Name(), fileName(f)))
			if err != nil {
				continue
			}
			if limit := c.s.capOf(f); info.Size() > int64(limit) {
				over = append(over, fmt.Sprintf("%s/%s: %d of %d bytes", p, fileName(f), info.Size(), limit))
			}
		}
	}
	if info, err := os.Stat(c.s.path(teamDir, core.FileTeam)); err == nil {
		if limit := c.s.capOf(core.FileTeam); info.Size() > int64(limit) {
			over = append(over, fmt.Sprintf("team/TEAM.md: %d of %d bytes", info.Size(), limit))
		}
	}
	if len(over) == 0 {
		return nil
	}
	slices.Sort(over)
	f := c.finding(core.SevLow, "Memory files are over their size cap",
		"The agent cannot add to these files until they are shortened: "+strings.Join(over, "; ")+".")
	f.Fix = "/memory"
	return []core.Finding{f}
}

func (c overCapCheck) finding(sev core.Severity, title, detail string) core.Finding {
	return core.Finding{ID: c.ID(), Area: "memory", Title: title, Detail: detail, Severity: sev}
}

func (overCapCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }
