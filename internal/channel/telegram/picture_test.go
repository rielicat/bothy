package telegram

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestPicture(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if id, err := e.a.PictureID(ctx); err != nil || id != "" {
		t.Fatalf("fresh bot: id=%q err=%v", id, err)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 'p', 'i', 'c'}
	if err := e.a.SetPicture(ctx, core.Picture{PNG: []byte("png"), JPEG: jpeg}); err != nil {
		t.Fatal(err)
	}
	if got := e.srv.ProfilePhoto(); !bytes.Equal(got, jpeg) {
		t.Fatalf("profile photo = %q", got)
	}
	first, err := e.a.PictureID(ctx)
	if err != nil || first == "" {
		t.Fatalf("after set: id=%q err=%v", first, err)
	}
	if err := e.a.SetPicture(ctx, core.Picture{PNG: []byte("png"), JPEG: jpeg}); err != nil {
		t.Fatal(err)
	}
	if id, err := e.a.PictureID(ctx); err != nil || id == "" || id == first {
		t.Fatalf("after another set: id=%q (was %q) err=%v", id, first, err)
	}
	if err := e.a.SetPicture(ctx, core.Picture{PNG: []byte("png")}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("no JPEG: %v", err)
	}
	if e.srv.Calls("setMyProfilePhoto") != 2 {
		t.Fatalf("setMyProfilePhoto calls = %d", e.srv.Calls("setMyProfilePhoto"))
	}
}

// The bot's id, not its name, says which bot was looked after.
func TestPictureBotID(t *testing.T) {
	e := newEnv(t)
	if id := e.a.BotID(); id != "" {
		t.Fatalf("before getMe: %q", id)
	}
	if _, err := e.a.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if id := e.a.BotID(); id != "7000001" {
		t.Fatalf("bot id = %q", id)
	}
}

func TestPictureSetInBotFatherCounts(t *testing.T) {
	e := newEnv(t)
	e.srv.SetProfilePhoto([]byte{0xff, 0xd8, 0xff})
	first, err := e.a.PictureID(context.Background())
	if err != nil || first == "" {
		t.Fatalf("id=%q err=%v", first, err)
	}
	e.srv.SetProfilePhoto([]byte{0xff, 0xd8, 0xff, 0x01})
	if id, err := e.a.PictureID(context.Background()); err != nil || id == first {
		t.Fatalf("after another photo: id=%q (was %q) err=%v", id, first, err)
	}
}

// A rate limit comes back at once, coded, for the admin waiting on it.
func TestPictureRateLimited(t *testing.T) {
	e := newEnv(t)
	e.srv.RateLimit("setMyProfilePhoto", 3)
	err := e.a.SetPicture(context.Background(), core.Picture{JPEG: []byte{0xff, 0xd8, 0xff}})
	if core.CodeOf(err) != core.FailRateLimited {
		t.Fatalf("err = %v (%q)", err, core.CodeOf(err))
	}
	if n := e.srv.Calls("setMyProfilePhoto"); n != 1 {
		t.Fatalf("retried: %d calls", n)
	}
	if e.srv.ProfilePhoto() != nil {
		t.Fatal("photo set despite the 429")
	}
}
