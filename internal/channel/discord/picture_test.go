package discord

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/discord/discordtest"
	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestPicture(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if id, err := f.a.PictureID(ctx); err != nil || id != "" {
		t.Fatalf("fresh bot: id=%q err=%v", id, err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := f.a.SetPicture(ctx, core.Picture{PNG: png, JPEG: []byte{0xff, 0xd8}}); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.Avatar(); !bytes.Equal(got, png) {
		t.Fatalf("avatar = %q", got)
	}
	first, err := f.a.PictureID(ctx)
	if err != nil || first == "" {
		t.Fatalf("after set: id=%q err=%v", first, err)
	}
	if err := f.a.SetPicture(ctx, core.Picture{PNG: append(png, '2'), JPEG: []byte{0xff, 0xd8}}); err != nil {
		t.Fatal(err)
	}
	if id, err := f.a.PictureID(ctx); err != nil || id == "" || id == first {
		t.Fatalf("after another set: id=%q (was %q) err=%v", id, first, err)
	}
}

// The bot's id, not its name, says which bot was looked after: Discord bot
// names repeat.
func TestPictureBotID(t *testing.T) {
	f := newFixture(t)
	if id := f.a.BotID(); id != "" {
		t.Fatalf("before connecting: %q", id)
	}
	if _, err := f.a.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if id := f.a.BotID(); id != discordtest.DefaultBotID {
		t.Fatalf("bot id = %q", id)
	}
}

func TestPictureSetInThePortalCounts(t *testing.T) {
	f := newFixture(t)
	f.srv.SetAvatar([]byte("portal"))
	if id, err := f.a.PictureID(context.Background()); err != nil || id == "" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

// Both of Discord's answers for "too many avatar changes" are a rate limit,
// returned at once rather than waited out.
func TestPictureRateLimited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pic := core.Picture{PNG: []byte("\x89PNG\r\n\x1a\nfake")}
	f.srv.SlowAvatarChanges(true)
	if err := f.a.SetPicture(ctx, pic); core.CodeOf(err) != core.FailRateLimited {
		t.Fatalf("too fast: %v (%q)", err, core.CodeOf(err))
	}
	f.srv.SlowAvatarChanges(false)
	f.srv.RateLimitNext("PATCH", "/api/v10/users/@me", 3600)
	start := time.Now()
	if err := f.a.SetPicture(ctx, pic); core.CodeOf(err) != core.FailRateLimited {
		t.Fatalf("429: %v (%q)", err, core.CodeOf(err))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("waited %v for the rate limit", d)
	}
	if f.srv.Avatar() != nil {
		t.Fatal("avatar changed despite the refusals")
	}
	if err := f.a.SetPicture(ctx, pic); err != nil {
		t.Fatalf("after the limit: %v", err)
	}
}

func TestPictureRejectedToken(t *testing.T) {
	f := newFixture(t, func(_ *config.Discord, d *core.AdapterDeps) {
		d.Secret = func(context.Context, string) (core.Secret, error) { return core.NewSecret([]byte("wrong")), nil }
	})
	if err := f.a.SetPicture(context.Background(), core.Picture{PNG: []byte("png")}); !isAuthErr(err) {
		t.Fatalf("err = %v", err)
	}
}
