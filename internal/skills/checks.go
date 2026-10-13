package skills

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/skills/treehash"
)

const changedID = "skills.changed"

// changedCheck reports skills marked changed plus enabled skills whose files
// no longer hash to the approved tree. Run only reads; Fix rescans (marking
// drifted skills changed) and disables every changed skill.
type changedCheck struct{ l *Library }

func (changedCheck) ID() string { return changedID }

func (c changedCheck) Run(ctx context.Context) []core.Finding {
	rows, err := c.l.store.Skills().List(ctx)
	if err != nil {
		return []core.Finding{{ID: changedID, Area: "skills", Title: "Skills could not be checked", Detail: err.Error(),
			Severity: core.SevMedium, Security: true}}
	}
	var names []string
	for _, s := range rows {
		switch s.Status {
		case core.SkillChanged:
			names = append(names, s.Name)
		case core.SkillEnabled:
			if sum, err := treehash.Sum(c.l.skillDir(s.Name)); err != nil || sum != s.ApprovedHash {
				names = append(names, s.Name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return []core.Finding{{
		ID: changedID, Area: "skills", Title: "Skill files changed since approval",
		Detail: fmt.Sprintf("%s: the files on disk no longer match the approved hash, so these skills are excluded from runs.",
			strings.Join(names, ", ")),
		Fix:      "Review them under Settings → Skills and re-enable, or let doctor disable them",
		Severity: core.SevMedium, Security: true, AutoFix: true,
	}}
}

func (c changedCheck) Fix(ctx context.Context, f core.Finding) error {
	if f.ID != changedID {
		return fmt.Errorf("skills: finding %q: %w", f.ID, core.ErrNotImplemented)
	}
	if err := c.l.Rescan(ctx); err != nil {
		return err
	}
	return c.l.disableChanged(ctx)
}

func (l *Library) disableChanged(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.store.Skills().List(ctx)
	if err != nil {
		return err
	}
	for _, s := range rows {
		if s.Status != core.SkillChanged {
			continue
		}
		s.Status, s.UpdatedAt = core.SkillDisabled, l.clock.Now()
		if err := l.store.Skills().Upsert(ctx, s); err != nil {
			return err
		}
		l.record(ctx, "system", "skill.disable", s.Name, map[string]any{"reason": "doctor fix: " + changedID})
	}
	return l.refreshLocked(ctx)
}
