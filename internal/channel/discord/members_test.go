package discord

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/discord/discordtest"
	"github.com/rielicat/bothy/internal/core"
)

const (
	guildB = "1100000000000000010"
	guildC = "1100000000000000011"
	maya   = "1100000000000000020"
)

func TestCheckMember(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	waitFor(t, 5*time.Second, func() bool { return len(f.a.Teams()) == 1 }, "the default server from GUILD_CREATE")
	ctx := context.Background()
	g := discordtest.DefaultGuild
	check := func(teams ...string) core.Membership {
		t.Helper()
		m, err := f.a.CheckMember(ctx, maya, teams)
		if err != nil {
			t.Fatalf("CheckMember(%v) = %v", teams, err)
		}
		return m
	}

	if m := check(); m.Status != core.MemberUnknown {
		t.Fatalf("no servers = %+v, want unknown", m)
	}
	f.srv.SetMember(g, maya, discordtest.Member{})
	if m := check(g, g); m != (core.Membership{Status: core.MemberYes, Team: "Bothy Test"}) {
		t.Fatalf("member = %+v", m)
	}
	f.srv.SetMember(g, maya, discordtest.Member{Pending: true})
	if m := check(g); m.Status != core.MemberOutsider {
		t.Fatalf("pending screening = %+v, want outsider", m)
	}
	f.srv.SetMember(g, maya, discordtest.Member{Bot: true})
	if m := check(g); m.Status != core.MemberOutsider {
		t.Fatalf("bot = %+v, want outsider", m)
	}

	// 404 in the first server, member in the second.
	f.srv.SetMember(g, maya, discordtest.Member{Status: http.StatusNotFound})
	f.srv.SetMember(guildB, maya, discordtest.Member{})
	if m := check(g, guildB); m.Status != core.MemberYes {
		t.Fatalf("member of the second server = %+v", m)
	}

	f.srv.SetMember(g, maya, discordtest.Member{Status: http.StatusForbidden})
	f.srv.SetMember(guildB, maya, discordtest.Member{Status: http.StatusForbidden})
	if m := check(g, guildB); m.Status != core.MemberUnknown || f.a.Status().Detail != noAccessDetail {
		t.Fatalf("no access anywhere = %+v with status %+v", m, f.a.Status())
	}
	f.srv.SetMember(guildC, maya, discordtest.Member{Status: http.StatusNotFound})
	if m := check(g, guildB, guildC); m.Status != core.MemberOutsider || f.a.Status().Detail != "" {
		t.Fatalf("403 and 404 mixed = %+v with status %+v, want outsider", m, f.a.Status())
	}
	if m, err := f.a.CheckMember(ctx, "not-a-snowflake", []string{g}); err != nil || m.Status != core.MemberOutsider {
		t.Fatalf("bad id = %+v, %v", m, err)
	}
}

func TestCheckMemberLooksAtTenServersAtMost(t *testing.T) {
	f := newFixture(t)
	var teams []string
	for i := range 12 {
		teams = append(teams, "11000000000000001"+string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	before := len(f.srv.Requests())
	if m, err := f.a.CheckMember(context.Background(), maya, teams); err != nil || m.Status != core.MemberOutsider {
		t.Fatalf("CheckMember = %+v, %v", m, err)
	}
	if n := len(f.srv.Requests()) - before; n != maxMemberGuilds {
		t.Fatalf("%d member lookups, want %d", n, maxMemberGuilds)
	}
}

func TestGuildsFromGateway(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	waitFor(t, 5*time.Second, func() bool { return len(f.a.Teams()) == 1 }, "GUILD_CREATE")
	f.srv.AddGuild(discordtest.Guild{ID: guildB, Name: "Acme"})
	waitFor(t, 5*time.Second, func() bool { return len(f.a.Teams()) == 2 }, "the second GUILD_CREATE")
	want := []core.TeamInfo{{ID: guildB, Name: "Acme"}, {ID: discordtest.DefaultGuild, Name: "Bothy Test"}}
	if got := f.a.Teams(); !slices.Equal(got, want) {
		t.Fatalf("Teams = %+v", got)
	}
	f.srv.RemoveGuild(guildB)
	waitFor(t, 5*time.Second, func() bool { return len(f.a.Teams()) == 1 }, "GUILD_DELETE")
	if f.a.MemberScope() != "" {
		t.Fatal("Discord's scope is the enabled servers, added by the hub")
	}
}

func TestInboundTeamAndInspectChat(t *testing.T) {
	f := newFixture(t)
	f.start(t, stateConnected)
	const channel = "1100000000000000030"
	f.srv.AddGuild(discordtest.Guild{ID: guildB, Name: "Acme", Members: 42})
	f.srv.SetChannelGuild(channel, guildB)
	f.srv.InjectText(channel, maya, "hi", true)
	if in := f.sink.message(t); in.Team != guildB {
		t.Fatalf("inbound = %+v, want team %s", in, guildB)
	}
	dm := f.srv.DMChannel(maya)
	f.srv.InjectText(dm, maya, "hello", false)
	if in := f.sink.message(t); in.Team != "" {
		t.Fatalf("DM inbound team = %q", in.Team)
	}
	info, err := f.a.InspectChat(context.Background(), core.ChatRef{Channel: name, ChatID: channel, Kind: core.ChatGroup})
	if err != nil || info != (core.ChatInfo{TeamID: guildB, TeamName: "Acme", Title: "#general", Members: 42}) {
		t.Fatalf("InspectChat = %+v, %v", info, err)
	}
}
