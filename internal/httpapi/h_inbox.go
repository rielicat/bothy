package httpapi

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const (
	// inboxSenderAge and inboxSendersShown bound unknown senders: anyone can
	// write to a bot, so only recent ones are worth an admin's look.
	inboxSenderAge    = 30 * 24 * time.Hour
	inboxSendersShown = 50
)

// inboxRoutes is the approvals inbox (v0.2 UI, S12).
func (s *Server) inboxRoutes() []route {
	return []route{{pattern: "GET /api/inbox", access: accessUser, h: s.inbox}}
}

// inbox is GET /api/inbox: pending approvals the caller can see plus, for
// admins, MCP tools and skills waiting for review, people waiting to be let
// in and new group chats.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	ctx := r.Context()
	aps, err := s.d.Approvals.Pending(ctx, a.Viewer)
	if err != nil {
		return err
	}
	items := make([]dto.InboxItem, 0, len(aps))
	for _, ap := range aps {
		items = append(items, approvalItem(ap, a))
	}
	if isAdmin(a) {
		items = append(items, s.mcpItems(ctx)...)
		if items, err = s.skillItems(ctx, items); err != nil {
			return err
		}
		if items, err = s.senderItems(ctx, items); err != nil {
			return err
		}
		if items, err = s.groupItems(ctx, a, items); err != nil {
			return err
		}
	}
	slices.SortStableFunc(items, func(x, y dto.InboxItem) int {
		if x.ForYou != y.ForYou {
			if x.ForYou {
				return -1
			}
			return 1
		}
		return x.CreatedAt.Compare(y.CreatedAt)
	})
	return ok(w, dto.Inbox{Items: items})
}

func approvalItem(ap *core.Approval, a core.Actor) dto.InboxItem {
	it := dto.InboxItem{ID: string(ap.ID), Type: dto.InboxApproval, Question: ap.Card.Question, ForYou: canDecide(ap, a),
		CreatedAt: ap.CreatedAt, Approval: ap}
	if it.Question == "" {
		it.Question = cmp.Or(ap.Card.Title, "Approval") + "?"
	}
	if ap.Card.Kind == core.CardReview {
		it.Type = dto.InboxNoteSuggestion
	}
	if r := ap.Approver; r.Multi() {
		it.Summary = strconv.Itoa(core.AllowVotes(ap.Votes, r)) + " of " + plural(r.Needed(), "approval", "approvals")
	}
	return it
}

// canDecide reports whether the caller may still allow or deny ap; the
// requester of a multi-approver approval only waits (withdrawing is a deny
// the card offers separately).
func canDecide(ap *core.Approval, a core.Actor) bool {
	r := ap.Approver
	switch {
	case r.Team:
		return a.Role.IsAdmin()
	case r.Multi():
		return r.Eligible(a.User, a.Role) && !slices.ContainsFunc(ap.Votes, func(v core.ApprovalVote) bool { return v.User == a.User })
	case r.AdminOnly:
		return a.Role.IsAdmin()
	}
	return r.User != "" && r.User == a.User
}

func (s *Server) mcpItems(ctx context.Context) []dto.InboxItem {
	if s.d.MCP == nil {
		return nil
	}
	var items []dto.InboxItem
	for _, st := range s.d.MCP.Status() {
		if st.Pending == 0 {
			continue
		}
		pins, err := s.d.MCP.Pins(ctx, st.Name)
		if err != nil {
			s.log.Warn("inbox: reading MCP pins failed", "server", st.Name, "err", err)
			continue
		}
		// A changed tool's approved definition stays beside the pending one,
		// so the review can show what changed.
		changed := map[string]bool{}
		var first time.Time
		for _, p := range pins {
			if p.Status == core.PinPending {
				changed[p.Tool] = true
				if first.IsZero() || p.FirstSeen.Before(first) {
					first = p.FirstSeen
				}
			}
		}
		if len(changed) == 0 {
			continue
		}
		pins = slices.DeleteFunc(pins, func(p *core.MCPPin) bool { return !changed[p.Tool] })
		items = append(items, dto.InboxItem{ID: "mcp:" + st.Name, Type: dto.InboxMCPReview,
			Question: "Review changes to " + capitalise(st.Name) + " tools", Summary: plural(len(changed), "tool changed", "tools changed"),
			ForYou: true, CreatedAt: first, MCP: &dto.InboxMCP{Server: st.Name, Tools: pins}})
	}
	return items
}

func (s *Server) skillItems(ctx context.Context, items []dto.InboxItem) ([]dto.InboxItem, error) {
	if s.d.Skills == nil {
		return items, nil
	}
	all, err := s.d.Skills.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		it := dto.InboxItem{ID: "skill:" + m.Name, Type: dto.InboxSkillReview, ForYou: true, CreatedAt: m.UpdatedAt, Skill: m}
		if m.Status == core.SkillChanged || m.Status == core.SkillPending {
			it.Changes = s.skillChanges(ctx, m)
		}
		switch m.Status {
		case core.SkillChanged:
			it.Question, it.Summary = "Review changes to the “"+m.Name+"” skill", "Changed since it was approved"
		case core.SkillPending:
			it.Question = "Review the new “" + m.Name + "” skill"
			if by := s.userName(ctx, m.InstalledBy); by != "" {
				it.Summary = "Added by " + by
			}
		default:
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

// senderItems lists people waiting for an admin, who wrote to Bothy in a
// DM within the last 30 days: on a chat app that can't say who is in the
// team, or where it can't vouch for someone it once did (Discord).
func (s *Server) senderItems(ctx context.Context, items []dto.InboxItem) ([]dto.InboxItem, error) {
	ids, err := s.d.Store.Identities().List(ctx, "")
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-inboxSenderAge)
	ids = slices.DeleteFunc(ids, func(id *core.ChannelIdentity) bool {
		waiting := id.Status == core.IdentityPending ||
			id.Status == core.IdentityTeammate && id.Via == core.ViaMembership
		return !waiting || id.Member != core.MemberUnknown || id.DMChat == "" || id.LastSeen.Before(since)
	})
	slices.SortFunc(ids, func(x, y *core.ChannelIdentity) int { return y.LastSeen.Compare(x.LastSeen) })
	for _, id := range ids[:min(len(ids), inboxSendersShown)] {
		items = append(items, dto.InboxItem{ID: "sender:" + id.Channel + ":" + id.ExternalID, Type: dto.InboxUnknownSender,
			Question: "Someone new messaged Bothy on " + appTitle(id.Channel), Summary: senderName(id), ForYou: true,
			CreatedAt: id.FirstSeen, Sender: id})
	}
	return items, nil
}

// groupItems lists group chats Bothy was added to that no admin has turned
// on or off yet (admins only).
func (s *Server) groupItems(ctx context.Context, a core.Actor, items []dto.InboxItem) ([]dto.InboxItem, error) {
	v, err := s.adminViewer(ctx, a)
	if err != nil {
		return nil, err
	}
	sps, err := s.d.Store.Spaces().List(ctx, v)
	if err != nil {
		return nil, err
	}
	for _, sp := range sps {
		if sp.Kind != core.SpaceGroup || !sp.ReviewedAt.IsZero() {
			continue
		}
		by := ""
		if sp.AddedBy != "" {
			by = s.namer(ctx, v).identity(sp.Chat.Channel, sp.AddedBy)
		}
		it := dto.InboxItem{ID: "group:" + string(sp.Principal), Type: dto.InboxGroupChat,
			Question: "Bothy was added to " + sp.Title + " in " + appTitle(sp.Chat.Channel),
			ForYou:   true, CreatedAt: sp.CreatedAt, Group: &dto.InboxGroup{Space: *sp, AddedBy: by}}
		if by != "" {
			it.Summary = "Added by " + by
		}
		items = append(items, it)
	}
	return items, nil
}

func senderName(id *core.ChannelIdentity) string {
	name := cmp.Or(strings.TrimSpace(id.DisplayName), id.ExternalID)
	if id.Username != "" {
		name += " (@" + id.Username + ")"
	}
	return name
}

func appTitle(channel string) string {
	switch channel {
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "slack":
		return "Slack"
	}
	return capitalise(channel)
}

func capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
