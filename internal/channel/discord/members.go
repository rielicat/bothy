package discord

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// maxMemberGuilds bounds the member lookups one check makes.
	maxMemberGuilds  = 10
	noAccessDetail   = "can't check who is in the server"
	noAccessLogEvery = time.Hour
)

var (
	_ core.MemberChecker = (*adapter)(nil)
	_ core.ChatInspector = (*adapter)(nil)
	_ core.TeamLister    = (*adapter)(nil)
)

// noteGuild keeps the servers the bot is in from GUILD_CREATE, GUILD_UPDATE
// and GUILD_DELETE. A GUILD_DELETE marked unavailable is an outage, not a
// removal.
func (a *adapter) noteGuild(ev gateway.EventData) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.guilds == nil {
		a.guilds = map[snowflake.ID]core.TeamInfo{}
	}
	switch e := ev.(type) {
	case gateway.EventGuildCreate:
		a.guilds[e.ID] = core.TeamInfo{ID: e.ID.String(), Name: e.Name}
	case gateway.EventGuildUpdate:
		a.guilds[e.ID] = core.TeamInfo{ID: e.ID.String(), Name: e.Name}
	case gateway.EventGuildDelete:
		if !e.Unavailable {
			delete(a.guilds, e.ID)
		}
	}
}

// Teams lists the servers the bot is in, by name.
func (a *adapter) Teams() []core.TeamInfo {
	a.mu.Lock()
	out := make([]core.TeamInfo, 0, len(a.guilds))
	for _, g := range a.guilds {
		out = append(out, g)
	}
	a.mu.Unlock()
	slices.SortFunc(out, func(x, y core.TeamInfo) int { return cmp.Or(cmp.Compare(x.Name, y.Name), cmp.Compare(x.ID, y.ID)) })
	return out
}

func (a *adapter) guildName(id snowflake.ID) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.guilds[id].Name
}

// MemberScope is empty: the hub adds the servers with an enabled channel.
func (a *adapter) MemberScope() string { return "" }

// CheckMember looks for id among the members of teams, the servers where
// Bothy is on in a channel. Bots and members who haven't passed the
// server's screening are not members. With no servers, or none that let
// the bot look, Discord can't tell.
func (a *adapter) CheckMember(ctx context.Context, id string, teams []string) (core.Membership, error) {
	user, ok := parseSnowflake(id)
	if !ok {
		return core.Membership{Status: core.MemberOutsider}, nil
	}
	var guilds []snowflake.ID
	for _, t := range teams {
		g, err := snowflake.Parse(t)
		if err != nil || slices.Contains(guilds, g) {
			continue
		}
		if guilds = append(guilds, g); len(guilds) == maxMemberGuilds {
			break
		}
	}
	if len(guilds) == 0 {
		return core.Membership{Status: core.MemberUnknown}, nil
	}
	looked := false
	for _, g := range guilds {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		m, err := a.client().Member(lctx, g, user)
		cancel()
		switch {
		case err == nil:
			looked = true
			if !m.User.Bot && !m.Pending {
				a.setNoAccess(false)
				return core.Membership{Status: core.MemberYes, Team: a.guildName(g)}, nil
			}
		case statusCode(err) == http.StatusNotFound:
			looked = true
		case statusCode(err) == http.StatusForbidden || jsonCode(err) == rest.JSONErrorCodeMissingAccess:
		default:
			return core.Membership{}, a.apiErr("member lookup", err)
		}
	}
	if !looked {
		a.setNoAccess(true)
		return core.Membership{Status: core.MemberUnknown}, nil
	}
	a.setNoAccess(false)
	return core.Membership{Status: core.MemberOutsider}, nil
}

// setNoAccess shows on the status page that the member lookup is refused
// everywhere, logging it at most once an hour.
func (a *adapter) setNoAccess(on bool) {
	now := a.clock.Now()
	a.mu.Lock()
	a.noAccess = on
	warn := on && (a.noAccessLogged.IsZero() || now.Sub(a.noAccessLogged) >= noAccessLogEvery)
	if warn {
		a.noAccessLogged = now
	}
	a.mu.Unlock()
	if warn {
		a.log.Warn("discord: every server refused the member lookup; people fall back to being let in by an admin")
	}
}

// InspectChat describes a channel: its server, the server's name and its
// approximate member count.
func (a *adapter) InspectChat(ctx context.Context, chat core.ChatRef) (core.ChatInfo, error) {
	id, err := snowflake.Parse(chat.ChatID)
	if err != nil {
		return core.ChatInfo{}, fmt.Errorf("discord: channel id %q: %w", chat.ChatID, core.ErrInvalid)
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	ch, err := a.client().Channel(lctx, id)
	if err != nil {
		return core.ChatInfo{}, a.apiErr("channel lookup", err)
	}
	gc, ok := ch.(discord.GuildChannel)
	if !ok {
		return core.ChatInfo{}, fmt.Errorf("discord: %s is not a server channel: %w", chat.ChatID, core.ErrInvalid)
	}
	info := core.ChatInfo{TeamID: gc.GuildID().String(), Title: "#" + gc.Name()}
	g, err := a.client().Guild(lctx, gc.GuildID(), true)
	if err != nil {
		return core.ChatInfo{}, a.apiErr("server lookup", err)
	}
	info.TeamName, info.Members = g.Name, g.ApproximateMemberCount
	return info, nil
}

func parseSnowflake(s string) (snowflake.ID, bool) {
	id, err := snowflake.Parse(s)
	return id, err == nil
}

func jsonCode(err error) rest.JSONErrorCode {
	var re *rest.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}
