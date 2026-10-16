package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// DeltasSince renders the net change of MEMORY.md and USER.md since event
// afterID as "+ entry" / "- entry" lines per file. It diffs the content before
// the first event against the content after the last one, so the text stays
// bounded by the file caps however many writes happened. HEARTBEAT.md is not
// part of the model's snapshot and is left out, but its events still advance
// lastID.
func (s *Service) DeltasSince(ctx context.Context, p core.PrincipalID, afterID int64) (string, int64, error) {
	if s.isChannel(ctx, p) {
		return "", afterID, nil
	}
	if _, err := s.checkPrincipal(ctx, p); err != nil {
		return "", afterID, err
	}
	evs, err := s.store.MemoryEvents().Since(ctx, p, afterID)
	if err != nil {
		return "", afterID, fmt.Errorf("memory: deltas: %w", err)
	}
	if len(evs) == 0 {
		return "", afterID, nil
	}
	slices.SortFunc(evs, func(a, b *core.MemoryEvent) int { return cmp.Compare(a.ID, b.ID) })
	last := evs[len(evs)-1].ID
	var b strings.Builder
	for _, f := range []core.MemoryFile{core.FileMemory, core.FileUser} {
		var first, final *core.MemoryEvent
		for _, e := range evs {
			if e.File != f {
				continue
			}
			if first == nil {
				first = e
			}
			final = e
		}
		if first == nil {
			continue
		}
		removed, added := lineDiff(first.Before, final.After)
		if len(removed) == 0 && len(added) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s changed:\n", fileName(f))
		for _, l := range removed {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range added {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String(), last, nil
}

// lineDiff returns the entries only in before and only in after, as
// multisets, each in file order. A replace shows as one removal and one
// addition.
func lineDiff(before, after string) (removed, added []string) {
	count := map[string]int{}
	for _, l := range entries(after) {
		count[l]++
	}
	for _, l := range entries(before) {
		if count[l] > 0 {
			count[l]--
			continue
		}
		removed = append(removed, l)
	}
	for _, l := range entries(after) {
		if count[l] > 0 {
			count[l]--
			added = append(added, l)
		}
	}
	return removed, added
}

func entries(content string) []string {
	var out []string
	for _, l := range splitLines(content) {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}
