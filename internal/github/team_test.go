package github

import (
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestTeamRunsSayWhoAsked(t *testing.T) {
	maya := &core.ToolEnv{Run: "run_1", Principal: core.Principal{Kind: core.PrincipalPerson, DisplayName: "Maya Chen"},
		Chat: &core.ChatRef{Channel: "slack"}}
	if got := askedBy(maya); got != "Asked by Maya Chen on Slack." {
		t.Fatalf("a teammate's pull request footer = %q", got)
	}
	eng := &core.ToolEnv{Principal: core.Principal{Kind: core.PrincipalSpace, DisplayName: "#eng"}}
	if got := askedBy(eng); got != "Asked by #eng." {
		t.Fatalf("a channel's scheduled pull request footer = %q", got)
	}
	user := &core.ToolEnv{User: "usr_1", Run: "run_2", Principal: core.Principal{Kind: core.PrincipalUser, DisplayName: "Olive"}}
	if got := askedBy(user); got != "" {
		t.Fatalf("an account's pull request footer = %q", got)
	}
	if prefetchActor(maya) != "agent:run_1" || prefetchActor(user) != "user:usr_1" {
		t.Fatalf("prefetch actors = %q, %q", prefetchActor(maya), prefetchActor(user))
	}
}
