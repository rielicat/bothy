package channel

import (
	"context"
	"fmt"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// refreshCards edits the full cards of a multi-approver approval after a vote
// that did not resolve it, so every approver sees the new count. Truncated
// cards show no count and stay as they are.
func (h *Hub) refreshCards(ctx context.Context, ap *core.Approval) {
	refs, err := h.d.Approvals.MessageRefs(ctx, ap.ID)
	if err != nil {
		h.log.Warn("channel: listing approval cards failed", "approval", ap.ID, "err", err)
		return
	}
	for _, r := range refs {
		a := h.live(r.Ref.Chat.Channel)
		if a == nil || r.Truncated {
			continue
		}
		msg, truncated := h.renderCard(ap, a.ch.Caps())
		if truncated {
			continue
		}
		msg.Chat = r.Ref.Chat
		if err := a.ch.Edit(ctx, r.Ref, msg); err != nil {
			h.log.Warn("channel: updating approval card failed", "approval", ap.ID, "err", err)
		}
	}
}

// approvalProgress is the card's vote count, e.g. "1 of 2 approvals".
func approvalProgress(ap *core.Approval) string {
	return fmt.Sprintf("%d of %d approvals", core.AllowVotes(ap.Votes, ap.Approver), ap.Approver.Needed())
}

// approvalLines is the card's approvals row (docs/design/ui.md §8.5): the
// count, every vote with its voter, and who else may still approve.
func (h *Hub) approvalLines(ap *core.Approval) []string {
	lines := []string{"**Approvals:** " + approvalProgress(ap)}
	t := ap.Approvals
	if t == nil {
		return lines
	}
	voted := map[core.UserID]bool{}
	for _, v := range t.Votes {
		voted[v.User] = true
		verb := " approved"
		if v.Choice == core.Deny {
			verb = " denied"
		}
		line := v.Name + verb
		if c := oneLine(v.Comment); c != "" {
			line += ": “" + c + "”"
		}
		lines = append(lines, plainLine(line))
	}
	var rest []string
	for _, p := range t.Eligible {
		if !voted[p.User] {
			rest = append(rest, p.Name)
		}
	}
	if t.AnyAdmin {
		rest = append(rest, "any admin")
	}
	if len(rest) > 0 {
		lines = append(lines, plainLine("Waiting for "+orList(rest)+"."))
	}
	return lines
}

// allowedBy names the users whose allow votes approved ap ("Alice and Bob"),
// the card's reader as "you" ("You and Bob").
func (h *Hub) allowedBy(ctx context.Context, ap *core.Approval, reader func(core.UserID, string) bool) string {
	var names []string
	seen := map[core.UserID]bool{}
	for _, v := range ap.Votes {
		if v.Choice != core.AllowOnce || seen[v.User] {
			continue
		}
		seen[v.User] = true
		name := h.userName(ctx, v.User)
		if reader(v.User, "") {
			name = "you"
			if len(names) == 0 {
				name = "You"
			}
		}
		names = append(names, name)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// oneLine keeps an approver's reason on its card line: a line of its own
// in it could pass for another vote.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
