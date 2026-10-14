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
// admins, MCP tools and skills waiting for review and unknown senders, and
// new group chats for their owners and admins.
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
	}
	if items, err = s.groupItems(ctx, a, items); err != nil {
		return err
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
		pending := slices.DeleteFunc(pins, func(p *core.MCPPin) bool { return p.Status != core.PinPending })
		if len(pending) == 0 {
			continue
		}
		first := pending[0].FirstSeen
		for _, p := range pending {
			if p.FirstSeen.Before(first) {
				first = p.FirstSeen
			}
		}
		items = append(items, dto.InboxItem{ID: "mcp:" + st.Name, Type: dto.InboxMCPReview,
			Question: "Review changes to " + capitalise(st.Name) + " tools", Summary: plural(len(pending), "tool changed", "tools changed"),
			ForYou: true, CreatedAt: first, MCP: &dto.InboxMCP{Server: st.Name, Tools: pending}})
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
		switch m.Status {
		case core.SkillChanged:
			it.Question, it.Summary = "Review changes to the “"+m.Name+"” skill", "Changed since it was approved"
		case core.SkillPending:
			it.Question = "Review the new “" + m.Name + "” skill"
			if m.InstalledBy != "" {
				it.Summary = "Added by " + s.userName(ctx, m.InstalledBy)
			}
		default:
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

// senderItems lists people who wrote to Bothy in a DM without being known,
// were told how to get access, and wrote within the last 30 days.
func (s *Server) senderItems(ctx context.Context, items []dto.InboxItem) ([]dto.InboxItem, error) {
	ids, err := s.d.Store.Identities().List(ctx, core.IdentityPending)
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-inboxSenderAge)
	ids = slices.DeleteFunc(ids, func(id *core.ChannelIdentity) bool {
		return id.DMChat == "" || id.LastNotice.IsZero() || id.LastSeen.Before(since)
	})
	slices.SortFunc(ids, func(x, y *core.ChannelIdentity) int { return y.LastSeen.Compare(x.LastSeen) })
	for _, id := range ids[:min(len(ids), inboxSendersShown)] {
		items = append(items, dto.InboxItem{ID: "sender:" + id.Channel + ":" + id.ExternalID, Type: dto.InboxUnknownSender,
			Question: "Someone new messaged Bothy on " + appTitle(id.Channel), Summary: senderName(id), ForYou: true,
			CreatedAt: id.FirstSeen, Sender: id})
	}
	return items, nil
}

// groupItems lists group chats Bothy was added to that nobody has turned
// on or looked at since (a space's first change records that).
func (s *Server) groupItems(ctx context.Context, a core.Actor, items []dto.InboxItem) ([]dto.InboxItem, error) {
	v := a.Viewer
	if isAdmin(a) {
		av, err := s.adminViewer(ctx, a)
		if err != nil {
			return nil, err
		}
		v = av
	}
	sps, err := s.d.Store.Spaces().List(ctx, v)
	if err != nil {
		return nil, err
	}
	for _, sp := range sps {
		if sp.Kind != "group" || sp.Enabled || !sp.UpdatedAt.Equal(sp.CreatedAt) {
			continue
		}
		by := s.userName(ctx, sp.Owner)
		items = append(items, dto.InboxItem{ID: "group:" + string(sp.Principal), Type: dto.InboxGroupChat,
			Question: "Bothy was added to " + sp.Title + " in " + appTitle(sp.Chat.Channel), Summary: "Added by " + by,
			ForYou: isAdmin(a) || sp.Owner == a.User, CreatedAt: sp.CreatedAt, Group: &dto.InboxGroup{Space: *sp, AddedBy: by}})
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
