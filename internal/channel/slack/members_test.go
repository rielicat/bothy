package slack

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/rielicat/bothy/internal/channel/slack/slacktest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func authed(f *fixture, enterprise string) {
	f.a.setSelf(&slack.AuthTestResponse{UserID: slacktest.DefaultBotUser, BotID: slacktest.DefaultBotID, User: "bothy",
		TeamID: slacktest.DefaultTeam, Team: "Acme", EnterpriseID: enterprise})
}

func TestCheckMemberMatrix(t *testing.T) {
	f := newFixture(t)
	authed(f, "")
	ctx := context.Background()
	cases := []struct {
		id   string
		user slacktest.User
		want core.MemberStatus
	}{
		{"U0000601", slacktest.User{}, core.MemberYes},
		{"U0000602", slacktest.User{Restricted: true}, core.MemberOutsider},
		{"U0000603", slacktest.User{UltraRestricted: true}, core.MemberOutsider},
		{"U0000604", slacktest.User{Stranger: true}, core.MemberOutsider},
		{"U0000605", slacktest.User{Deleted: true}, core.MemberOutsider},
		{"U0000606", slacktest.User{Bot: true}, core.MemberOutsider},
		{"U0000607", slacktest.User{WorkflowBot: true}, core.MemberOutsider},
		{"U0000608", slacktest.User{TeamID: "T0000999"}, core.MemberOutsider},
		{"U0000609", slacktest.User{TeamID: "T0000999", EnterpriseID: "E0000001"}, core.MemberOutsider},
		{"W0000610", slacktest.User{}, core.MemberYes},
		{"U0000611", slacktest.User{Error: "user_not_found"}, core.MemberOutsider},
		{"USLACKBOT", slacktest.User{}, core.MemberOutsider},
		{"B0000612", slacktest.User{}, core.MemberOutsider},
	}
	for _, c := range cases {
		f.srv.SetUserFlags(c.id, c.user)
		m, err := f.a.CheckMember(ctx, c.id, nil)
		if err != nil || m.Status != c.want || m.Team != "Acme" {
			t.Errorf("CheckMember(%s %+v) = %+v, %v; want %s in Acme", c.id, c.user, m, err, c.want)
		}
	}
	if f.srv.UserInfoCalls("USLACKBOT")+f.srv.UserInfoCalls("B0000612") != 0 {
		t.Error("Slackbot or a bot id was looked up")
	}

	f.srv.SetUserFlags("U0000613", slacktest.User{Error: "fatal_error"})
	if m, err := f.a.CheckMember(ctx, "U0000613", nil); err == nil {
		t.Fatalf("an API error became a verdict: %+v", m)
	}
	f.srv.SetUserFlags("U0000613", slacktest.User{})
	if m, err := f.a.CheckMember(ctx, "U0000613", nil); err != nil || m.Status != core.MemberYes {
		t.Fatalf("after the error = %+v, %v", m, err)
	}
	if n := f.srv.UserInfoCalls("U0000613"); n != 2 {
		t.Fatalf("users.info called %d times, want 2: errors aren't cached", n)
	}
}

func TestCheckMemberSameEnterprise(t *testing.T) {
	f := newFixture(t)
	authed(f, "E0000001")
	f.srv.SetUserFlags("U0000620", slacktest.User{TeamID: "T0000999", EnterpriseID: "E0000001"})
	if m, err := f.a.CheckMember(context.Background(), "U0000620", nil); err != nil || m.Status != core.MemberYes {
		t.Fatalf("another workspace of the same org = %+v, %v", m, err)
	}
	if st := f.a.Status(); st.Team != "Acme" || !st.Org {
		t.Fatalf("status = %+v, want team Acme on an org", st)
	}
	if got := f.a.MemberScope(); got != slacktest.DefaultTeam+"/E0000001" {
		t.Fatalf("MemberScope = %q", got)
	}
}

func TestCheckMemberBeforeAuthIsAnError(t *testing.T) {
	f := newFixture(t)
	if m, err := f.a.CheckMember(context.Background(), "U0000630", nil); !errors.Is(err, errNoWorkspace) {
		t.Fatalf("CheckMember before auth.test = %+v, %v", m, err)
	}
	if f.srv.UserInfoCalls("U0000630") != 0 {
		t.Fatal("users.info was called before the workspace was known")
	}
}

// A name an hour old still names someone, but only a verdict under five
// minutes old answers CheckMember.
func TestMembershipCacheIsSeparateFromNames(t *testing.T) {
	clock := fakes.NewClock(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	f := newFixture(t, func(_ *config.Slack, d *core.AdapterDeps) { d.Clock = clock })
	authed(f, "")
	ctx := context.Background()
	const id = "U0000640"
	if display, _ := f.a.userNames(ctx, id); display != "User "+id {
		t.Fatalf("userNames = %q", display)
	}
	if m, err := f.a.CheckMember(ctx, id, nil); err != nil || m.Status != core.MemberYes || f.srv.UserInfoCalls(id) != 1 {
		t.Fatalf("CheckMember right after the name lookup = %+v, %v with %d calls; want the shared answer", m, err, f.srv.UserInfoCalls(id))
	}
	clock.Advance(4 * time.Minute)
	if _, err := f.a.CheckMember(ctx, id, nil); err != nil || f.srv.UserInfoCalls(id) != 1 {
		t.Fatalf("a 4-minute verdict was not reused (%d calls, %v)", f.srv.UserInfoCalls(id), err)
	}
	clock.Advance(2 * time.Minute)
	f.srv.SetUserFlags(id, slacktest.User{Restricted: true})
	if m, err := f.a.CheckMember(ctx, id, nil); err != nil || m.Status != core.MemberOutsider || f.srv.UserInfoCalls(id) != 2 {
		t.Fatalf("a 6-minute verdict answered: %+v, %v, %d calls", m, err, f.srv.UserInfoCalls(id))
	}
	f.a.userNames(ctx, id)
	if f.srv.UserInfoCalls(id) != 2 {
		t.Fatal("the name cache was not filled by the membership lookup")
	}

	// Another workspace's token makes every verdict stale.
	f.a.setSelf(&slack.AuthTestResponse{TeamID: "T0000999", Team: "Other"})
	if m, _ := f.a.CheckMember(ctx, id, nil); m.Team != "Other" || f.srv.UserInfoCalls(id) != 3 {
		t.Fatalf("after a workspace change = %+v with %d calls", m, f.srv.UserInfoCalls(id))
	}
}

func TestMessagesFromAppsAreDropped(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel, user = "C0000650", "U0000650"
	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "from a workflow", Bot: true, Mention: true})
	f.srv.Inject(slacktest.Incoming{ChatID: channel, SenderID: user, Text: "from an app", AppBot: true})
	f.srv.InjectText(channel, user, "from a person", false)
	if in := f.sink.message(t); in.Text != "from a person" {
		t.Fatalf("got %+v, want messages from apps dropped", in)
	}
}

func TestInspectChat(t *testing.T) {
	f := newFixture(t)
	authed(f, "")
	ctx := context.Background()
	f.srv.SetChannel("C0000660", "eng")
	f.srv.SetChannelInfo("C0000660", slacktest.ChannelInfo{Members: 23})
	info, err := f.a.InspectChat(ctx, core.ChatRef{Channel: name, ChatID: "C0000660", Kind: core.ChatGroup})
	if err != nil || info != (core.ChatInfo{TeamID: slacktest.DefaultTeam, TeamName: "Acme", Title: "#eng", Members: 23}) {
		t.Fatalf("InspectChat = %+v, %v", info, err)
	}
	if got := f.a.channelTitle(ctx, "C0000660"); got != "#eng" || f.callsTo("conversations.info") != 1 {
		t.Fatalf("channelTitle = %q after %d calls, want the inspected title", got, f.callsTo("conversations.info"))
	}
	f.srv.SetChannel("C0000661", "partners")
	f.srv.SetChannelInfo("C0000661", slacktest.ChannelInfo{Members: 9, ExtShared: true})
	if info, err := f.a.InspectChat(ctx, core.ChatRef{Channel: name, ChatID: "C0000661"}); err != nil || !info.External {
		t.Fatalf("Slack Connect channel = %+v, %v", info, err)
	}
	f.srv.SetChannel("C0000662", "org-wide")
	f.srv.SetChannelInfo("C0000662", slacktest.ChannelInfo{Shared: true, ConnectedTeams: []string{"T1"}})
	if info, _ := f.a.InspectChat(ctx, core.ChatRef{Channel: name, ChatID: "C0000662"}); info.External {
		t.Fatal("a channel shared within one org is not external")
	}
}
