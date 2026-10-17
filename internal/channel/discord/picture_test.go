package discord

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestPicture(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if has, err := f.a.HasPicture(ctx); err != nil || has {
		t.Fatalf("fresh bot: has=%v err=%v", has, err)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := f.a.SetPicture(ctx, core.Picture{PNG: png, JPEG: []byte{0xff, 0xd8}}); err != nil {
		t.Fatal(err)
	}
	if got := f.srv.Avatar(); !bytes.Equal(got, png) {
		t.Fatalf("avatar = %q", got)
	}
	if has, err := f.a.HasPicture(ctx); err != nil || !has {
		t.Fatalf("after set: has=%v err=%v", has, err)
	}
}

func TestPictureSetInThePortalCounts(t *testing.T) {
	f := newFixture(t)
	f.srv.SetAvatar([]byte("portal"))
	if has, err := f.a.HasPicture(context.Background()); err != nil || !has {
		t.Fatalf("has=%v err=%v", has, err)
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
