package slack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/rielicat/bothy/internal/core"
)

// memberTTL is how long a users.info verdict answers CheckMember. It is
// kept apart from the hour-long name cache: a name may be an hour old, a
// membership verdict may not.
const memberTTL = 5 * time.Minute

var (
	_ core.MemberChecker = (*adapter)(nil)
	_ core.ChatInspector = (*adapter)(nil)

	errNoWorkspace = errors.New("slack: Slack hasn't said which workspace this is yet")
)

type memberEntry struct {
	verdict   core.MemberStatus
	team      string
	fetchedAt time.Time
}

type memberCache struct {
	mu sync.Mutex
	m  map[string]memberEntry
}

func (c *memberCache) get(id string, now time.Time) (memberEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	return e, ok && now.Sub(e.fetchedAt) < memberTTL
}

func (c *memberCache) put(id string, e memberEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= namesMax {
		c.m = make(map[string]memberEntry)
	}
	c.m[id] = e
}

func (c *memberCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// flight shares one users.info call between userNames and CheckMember
// asking about the same person at once.
type flight struct {
	done chan struct{}
	u    *slack.User
	err  error
}

type flights struct {
	mu sync.Mutex
	m  map[string]*flight
}

// userInfo calls users.info once per id at a time and fills both caches
// from the answer.
func (a *adapter) userInfo(ctx context.Context, id string) (*slack.User, error) {
	a.inflight.mu.Lock()
	if f, ok := a.inflight.m[id]; ok {
		a.inflight.mu.Unlock()
		select {
		case <-f.done:
			return f.u, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	if a.inflight.m == nil {
		a.inflight.m = make(map[string]*flight)
	}
	a.inflight.m[id] = f
	a.inflight.mu.Unlock()

	f.u, f.err = a.fetchUser(ctx, id)
	a.inflight.mu.Lock()
	delete(a.inflight.m, id)
	a.inflight.mu.Unlock()
	close(f.done)
	return f.u, f.err
}

func (a *adapter) fetchUser(ctx context.Context, id string) (*slack.User, error) {
	api, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	u, err := api.GetUserInfoContext(lctx, id)
	cancel()
	now := a.clock.Now()
	if err != nil {
		if slackCode(err) == "missing_scope" {
			a.warnScope(err)
		}
		return nil, err
	}
	a.names.put(id, nameEntry{handle: u.Name, display: either(either(u.Profile.DisplayName, u.RealName), u.Name)}, now.Add(nameTTL))
	team, enterprise, teamName := a.workspace()
	if team != "" || enterprise != "" {
		a.members.put(id, memberEntry{verdict: verdict(u, team, enterprise), team: teamName, fetchedAt: now})
	}
	return u, nil
}

// verdict: members of this workspace, or of any workspace in the same
// Enterprise Grid org, who are full members and people. Guests, Slack
// Connect strangers, deactivated accounts and bots are outsiders.
func verdict(u *slack.User, team, enterprise string) core.MemberStatus {
	inTeam := team != "" && u.TeamID == team || enterprise != "" && u.Enterprise.EnterpriseID == enterprise
	if inTeam && !u.IsBot && !u.IsWorkflowBot && !u.Deleted && !u.IsRestricted && !u.IsUltraRestricted && !u.IsStranger {
		return core.MemberYes
	}
	return core.MemberOutsider
}

// CheckMember asks Slack whether id is a full member of the workspace the
// bot token belongs to. teams is unused: Slack has one workspace per token.
func (a *adapter) CheckMember(ctx context.Context, id string, _ []string) (core.Membership, error) {
	team, enterprise, teamName := a.workspace()
	if team == "" && enterprise == "" {
		return core.Membership{}, errNoWorkspace
	}
	if id == "USLACKBOT" || !strings.HasPrefix(id, "U") && !strings.HasPrefix(id, "W") {
		return core.Membership{Status: core.MemberOutsider, Team: teamName}, nil
	}
	if e, ok := a.members.get(id, a.clock.Now()); ok {
		return core.Membership{Status: e.verdict, Team: e.team}, nil
	}
	if _, err := a.userInfo(ctx, id); err != nil {
		if slackCode(err) == "user_not_found" {
			return core.Membership{Status: core.MemberOutsider, Team: teamName}, nil
		}
		return core.Membership{}, a.apiErr("users.info", botKey, err)
	}
	if e, ok := a.members.get(id, a.clock.Now()); ok {
		return core.Membership{Status: e.verdict, Team: e.team}, nil
	}
	// The token changed workspaces while users.info ran.
	return core.Membership{}, errNoWorkspace
}

// MemberScope changes when the bot token moves to another workspace or org.
func (a *adapter) MemberScope() string {
	team, enterprise, _ := a.workspace()
	return team + "/" + enterprise
}

// InspectChat describes a channel for the admins' turn-on choice. External
// is a Slack Connect channel: people from other companies are in it.
func (a *adapter) InspectChat(ctx context.Context, chat core.ChatRef) (core.ChatInfo, error) {
	api, err := a.client(ctx)
	if err != nil {
		return core.ChatInfo{}, err
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	ch, err := api.GetConversationInfoContext(lctx, &slack.GetConversationInfoInput{ChannelID: chat.ChatID, IncludeNumMembers: true})
	cancel()
	if err != nil {
		if slackCode(err) == "missing_scope" {
			a.warnScope(err)
		}
		return core.ChatInfo{}, a.apiErr("conversations.info", botKey, err)
	}
	team, _, teamName := a.workspace()
	info := core.ChatInfo{TeamID: team, TeamName: teamName, Members: ch.NumMembers,
		External: ch.IsExtShared || ch.IsShared && len(ch.ConnectedTeamIDs) > 1}
	if !ch.IsIM && !ch.IsMpIM {
		info.Title = hashName(ch.Name)
		a.channels.put(chat.ChatID, nameEntry{display: info.Title}, a.clock.Now().Add(nameTTL))
	}
	return info, nil
}

func (a *adapter) workspace() (team, enterprise, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfTeam, a.selfEnterprise, a.teamName
}
