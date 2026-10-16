package httpapi

import (
	"context"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

// Names the UI shows instead of ids (people, chats, schedules). A namer
// caches lookups for one response; an id it cannot resolve through the
// viewer names nothing, so a foreign row never leaks its title.
type namer struct {
	s         *Server
	ctx       context.Context
	v         core.Viewer
	users     map[core.UserID]string
	sessions  map[core.SessionID]string
	origins   map[core.SessionID]string
	schedules map[core.ScheduleID]string
	idents    map[string]string
	tokens    map[string]string
	spaces    map[core.PrincipalID]*core.Space
	senders   map[core.SessionID]map[core.RunID]string
	prins     map[core.PrincipalID]string
}

// Bothy's own actions and schedule runs have no person behind them.
const (
	bothyName    = "Bothy"
	scheduleName = "Schedule"
	checkInName  = "Check-in"
)

func (s *Server) namer(ctx context.Context, v core.Viewer) *namer {
	return &namer{s: s, ctx: ctx, v: v, users: map[core.UserID]string{}, sessions: map[core.SessionID]string{},
		origins: map[core.SessionID]string{}, schedules: map[core.ScheduleID]string{}, idents: map[string]string{}, tokens: map[string]string{},
		spaces: map[core.PrincipalID]*core.Space{}, senders: map[core.SessionID]map[core.RunID]string{}, prins: map[core.PrincipalID]string{}}
}

// user is a person's display name, else their username.
func (n *namer) user(id core.UserID) string {
	if id == "" {
		return ""
	}
	if name, ok := n.users[id]; ok {
		return name
	}
	name := ""
	if u, err := n.s.d.Store.Users().Get(n.ctx, id); err == nil {
		name = cmpOr(u.DisplayName, u.Username)
	}
	n.users[id] = name
	return name
}

// chat is a session's title as the chat list shows it: the stored title,
// else the start of its first message.
func (n *namer) chat(id core.SessionID) string {
	if id == "" {
		return ""
	}
	if t, ok := n.sessions[id]; ok {
		return t
	}
	t := ""
	if sess, err := n.s.d.Store.Sessions().Get(n.ctx, n.v, id); err == nil {
		t = n.title(sess)
	}
	n.sessions[id] = t
	return t
}

// origin is where a chat lives (web, telegram, slack, discord, api), as
// far as the viewer may open it.
func (n *namer) origin(id core.SessionID) string {
	if id == "" {
		return ""
	}
	n.chat(id)
	return n.origins[id]
}

// title names a session already loaded through the viewer.
func (n *namer) title(sess *core.Session) string {
	n.origins[sess.ID] = sess.Origin
	if t, ok := n.sessions[sess.ID]; ok {
		return t
	}
	t := sess.Title
	if t == "" {
		t = n.firstWords(sess.ID)
	}
	n.sessions[sess.ID] = t
	return t
}

// firstWords is core.DraftTitle of the first message someone wrote, for
// sessions stored before drafts existed.
func (n *namer) firstWords(id core.SessionID) string {
	msgs, err := n.s.d.Store.Messages().Range(n.ctx, n.v, id, 0, titleScan)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Role == core.MsgUser && m.Kind == core.KindNormal && m.Provenance != core.ProvSystem {
			if text := strings.TrimSpace(m.PlainText()); text != "" {
				return core.DraftTitle(n.s.d.Vault.Redactor().Redact(text))
			}
		}
	}
	return ""
}

// schedule is a schedule's name; the built-in heartbeat is the Check-in.
func (n *namer) schedule(id core.ScheduleID) string {
	if id == "" {
		return ""
	}
	if name, ok := n.schedules[id]; ok {
		return name
	}
	name := ""
	if sc, err := n.s.d.Store.Schedules().Get(n.ctx, n.v, id); err == nil {
		name = sc.Name
		if sc.Kind == core.SchedHeartbeat {
			name = checkInName
		}
	}
	n.schedules[id] = name
	return name
}

// identity names a chat-app account by the name the chat app gave it,
// else its handle.
func (n *namer) identity(channel, ext string) string {
	key := channel + ":" + ext
	if name, ok := n.idents[key]; ok {
		return name
	}
	name := ""
	if id, err := n.s.d.Store.Identities().Get(n.ctx, channel, ext); err == nil {
		name = cmpOr(strings.TrimSpace(id.DisplayName), id.Username)
	}
	n.idents[key] = name
	return name
}

// chatPerson names a chat person with their app: "Maya Chen · Slack".
func (n *namer) chatPerson(key string) string {
	ch, ext, ok := strings.Cut(key, ":")
	if !ok {
		return ""
	}
	if name := n.identity(ch, ext); name != "" {
		return name + " · " + appTitle(ch)
	}
	return ""
}

// who names whoever did something: a user id, or a chat actor
// "channel:<app>:<id>" as "Maya Chen · Slack".
func (n *namer) who(by string) string {
	if rest, ok := strings.CutPrefix(by, "channel:"); ok {
		return n.chatPerson(rest)
	}
	return n.user(core.UserID(by))
}

// principal names a principal: its user, "{Name} · {App}" for a teammate,
// "{title} · {App}" for a channel.
func (n *namer) principal(p core.PrincipalID) string {
	if name, ok := n.prins[p]; ok {
		return name
	}
	name, _, app := n.teamOwner(p)
	switch {
	case app != "":
		name += " · " + appTitle(app)
	case name == "":
		if pr, err := n.s.d.Store.Principals().Get(n.ctx, onlyOf(p), p); err == nil && pr.Kind == core.PrincipalUser {
			name = n.user(pr.User)
		}
	}
	n.prins[p] = name
	return name
}

// teamOwner names a team principal for the admins' lists: a teammate's
// name or a channel's title, its kind (teammate | channel) and chat app.
func (n *namer) teamOwner(p core.PrincipalID) (name, kind, app string) {
	pr, err := n.s.d.Store.Principals().Get(n.ctx, onlyOf(p), p)
	if err != nil || !pr.TeamOwned() {
		return "", "", ""
	}
	sp, err := n.s.d.Store.Spaces().Get(n.ctx, onlyOf(p), p)
	if err != nil {
		return pr.DisplayName, "", ""
	}
	if pr.Kind == core.PrincipalPerson {
		if id, err := n.s.d.Store.Identities().ByPrincipal(n.ctx, p); err == nil {
			return cmpOr(n.identity(id.Channel, id.ExternalID), pr.DisplayName), "teammate", sp.Chat.Channel
		}
		return pr.DisplayName, "teammate", sp.Chat.Channel
	}
	return sp.Title, "channel", sp.Chat.Channel
}

// token names the owner of an access token.
func (n *namer) token(id string) string {
	if name, ok := n.tokens[id]; ok {
		return name
	}
	name := ""
	if t, err := n.s.d.Store.Tokens().Get(n.ctx, id); err == nil {
		name = n.user(t.User)
	}
	n.tokens[id] = name
	return name
}

// actor names an audit actor: user:<id>, token:<id>, channel:<name>:<ext>,
// agent:<run> or system.
func (n *namer) actor(a string) string {
	kind, rest, _ := strings.Cut(a, ":")
	switch kind {
	case "user":
		return n.user(core.UserID(rest))
	case "token":
		return n.token(rest)
	case "channel":
		ch, ext, _ := strings.Cut(rest, ":")
		return n.identity(ch, ext)
	}
	return bothyName
}

// sender names a message's sender: web:<user>, api:<token> or
// <channel>:<external id>.
func (n *namer) sender(s string) string {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "web":
		return n.user(core.UserID(rest))
	case "api":
		return n.token(rest)
	}
	return n.identity(kind, rest)
}

func (n *namer) space(p core.PrincipalID) *core.Space {
	if sp, ok := n.spaces[p]; ok {
		return sp
	}
	sp, err := n.s.d.Store.Spaces().Get(n.ctx, n.v, p)
	if err != nil {
		sp = nil
	}
	n.spaces[p] = sp
	return sp
}

// titleScan is how many of a session's first messages are read for its name.
const titleScan = 50

// runSenderScan bounds how far back a group chat is read to find who started
// one of its tasks; older tasks name nobody.
const runSenderScan = 100

// requester names who asked for a run: nobody for schedules and check-ins,
// the account owner in their own chats, the teammate in theirs, the person
// who wrote in a group chat.
func (n *namer) requester(r *core.Run) string {
	if r.Trigger.Scheduled() {
		return ""
	}
	if !core.ValidID(core.PrefixSpace, string(r.Principal)) {
		return n.user(r.User)
	}
	if sp := n.space(r.Principal); sp != nil && sp.Kind == core.SpacePerson {
		if id, err := n.s.d.Store.Identities().ByPrincipal(n.ctx, r.Principal); err == nil {
			return n.identity(id.Channel, id.ExternalID)
		}
		return sp.Title
	}
	byRun, ok := n.senders[r.Session]
	if !ok {
		byRun = map[core.RunID]string{}
		msgs, err := n.s.d.Store.Messages().Page(n.ctx, n.v, r.Session, core.Page{Limit: runSenderScan})
		if err == nil {
			for _, m := range msgs { // newest first: the oldest message of a run wins
				if m.Run != "" && m.Role == core.MsgUser && m.Sender != "" {
					byRun[m.Run] = m.Sender
				}
			}
		}
		n.senders[r.Session] = byRun
	}
	if s := byRun[r.ID]; s != "" {
		return n.sender(s)
	}
	return ""
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
