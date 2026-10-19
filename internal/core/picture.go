package core

import (
	"context"
	"errors"
)

// Picture is Bothy's picture in the chat apps: the built-in default or one an
// admin uploaded, always a square, opaque 1024 px image.
type Picture struct {
	PNG  []byte `json:"-"`
	JPEG []byte `json:"-"` // the same picture, for chat apps that take only JPEG (Telegram)
	// Custom is true when an admin replaced the default.
	Custom bool `json:"custom"`
	// Version changes whenever the picture does (a hash of PNG).
	Version string `json:"version"`
}

// AgentPicture keeps Bothy's picture (internal/picture): the admin's upload in
// the data dir, else the embedded default.
type AgentPicture interface {
	Current(ctx context.Context) (Picture, error)
	// Set checks an uploaded PNG, JPEG or WebP, crops it to a centred square,
	// scales it and re-encodes it (dropping its metadata), then keeps it.
	// A file that is not a usable picture is ErrInvalid with a plain message.
	Set(ctx context.Context, upload []byte) (Picture, error)
	// Reset goes back to the default picture.
	Reset(ctx context.Context) (Picture, error)
}

// PictureSetter is implemented by adapters that can change the bot's own
// picture (Telegram, Discord). Slack lets only a person change an app's icon.
type PictureSetter interface {
	// BotID is the bot's own user id on the platform, "" until it is known.
	// Unlike its name it tells bots apart: Discord bot names repeat.
	BotID() string
	// PictureID names the picture the bot shows now, whoever set it: the
	// platform's id for that upload, "" when the bot has none. Any change
	// of picture changes it.
	PictureID(ctx context.Context) (string, error)
	// SetPicture replaces the bot's picture. A platform that limits how
	// often it may change answers with an error whose FailureCode is
	// FailRateLimited.
	SetPicture(ctx context.Context, p Picture) error
}

// PictureOutcome is what applying the picture did in one chat app.
type PictureOutcome string

const (
	PictureUpdated PictureOutcome = "updated" // the chat app shows the picture now
	PictureFailed  PictureOutcome = "failed"  // it couldn't be changed; Err says why
	PictureManual  PictureOutcome = "manual"  // only a person can change it there (Slack)
)

// PictureResult is ChannelHub.ApplyPicture's answer for one chat app.
type PictureResult struct {
	App     string
	Outcome PictureOutcome
	// Err is why it failed: ErrNotConnected, or the platform's error (see
	// CodeOf for rate limits).
	Err error
}

// ErrNotConnected: the chat app is turned on but not connected right now.
var ErrNotConnected = errors.New("chat app not connected")
