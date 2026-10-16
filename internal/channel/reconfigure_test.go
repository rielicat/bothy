package channel

import (
	"context"
	"net/http"
	"testing"

	"github.com/rielicat/bothy/internal/channel/discord"
	"github.com/rielicat/bothy/internal/channel/slack"
	"github.com/rielicat/bothy/internal/channel/telegram"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// cmd/bothy builds each adapter once at boot, so one that cannot take a new
// config would ignore a chat app turned on in Settings until a restart.
func TestBuiltInAdaptersFollowConfig(t *testing.T) {
	c := config.Default()
	d := core.AdapterDeps{
		Secret: func(context.Context, string) (core.Secret, error) { return core.Secret{}, core.ErrNotFound },
		HTTP:   func(core.SystemDest) *http.Client { return http.DefaultClient },
	}
	for _, mk := range []func() (core.Channel, error){
		func() (core.Channel, error) { return telegram.New(c.Channels.Telegram, d) },
		func() (core.Channel, error) { return discord.New(c.Channels.Discord, d) },
		func() (core.Channel, error) { return slack.New(c.Channels.Slack, d) },
	} {
		ch, err := mk()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ch.(Reconfigurer); !ok {
			t.Errorf("the %s adapter does not implement Reconfigurer", ch.Name())
		}
	}
}
