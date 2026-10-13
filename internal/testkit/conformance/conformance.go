// Package conformance holds suites every implementation must pass.
package conformance

import (
	"encoding/json"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

type ToolCase struct {
	Name    string
	Env     *core.ToolEnv
	Args    json.RawMessage
	WantErr bool
}

// Tool checks: valid Manifest (non-zero side effect, Title set, schema compiles,
// name ^[a-zA-Z0-9_-]{1,64}$); Plan is deterministic (two calls give identical
// Action JSON and Fingerprint); Plan performs no side effects (env.Sandbox,
// env.HTTP and env.Emit are trapped and must not be called); Action.SideEffect
// >= Manifest.SideEffect; Action.Tool == spec name.
//
// Details: the schema must be a "type": "object" JSON Schema; triggers and
// MinRole must be known values. A case with a nil Env gets an interactive
// owner env with a fresh temp workspace. Sandbox.Status and Sandbox.Usage are
// reads and pass through to the case's sandbox; every other Sandbox method
// trips the trap. Plan must not change the workspace tree either. A WantErr
// case passes when its args fail schema validation (the runner validates
// before Plan) or both Plan calls fail; every other case's args must match
// the schema, and its Action must have a Kind, a canonical Payload and
// Fingerprint == core.ActionFingerprint(Tool, Payload). At least one case
// must succeed. A panic in Plan is a failure.
func Tool(t *testing.T, tool core.Tool, cases []ToolCase) {
	t.Helper()
	runTool(tReporter{t}, tool, cases)
}

// Channel checks an adapter against its fake platform server: Send/Edit/Delete
// round-trip, previews disabled on every send, MaxLen respected, Ack within 3s.
//
// The suite runs ch.Run until it reports state "connected" (10s), then checks
// that an injected mentioned DM reaches the sink normalized (channel, chat,
// sender, text, Mentioned, distinct MsgIDs); that Send, Edit and Delete show
// up in platform.SentTexts; that a message of exactly Caps.MaxLen characters
// arrives intact as one message; that a button press reaches the sink and is
// acknowledged within 3s of the press; and finally that PreviewsDisabled holds
// for everything sent. Run must return within 5s of its context ending.
func Channel(t *testing.T, ch core.Channel, platform PlatformFake) {
	t.Helper()
	runChannel(tReporter{t}, ch, platform)
}

// PlatformFake is implemented by telegramtest, discordtest and slacktest.
type PlatformFake interface {
	// InjectText delivers a message from senderID in chatID the way the
	// platform would; mention addresses the bot (a DM always does).
	InjectText(chatID, senderID, text string, mention bool)
	// InjectButton presses the button carrying data on the bot's message msgID.
	InjectButton(chatID, msgID, senderID, data string)
	// SentTexts returns what the bot's messages in chatID show now, oldest
	// first: an edit replaces its message's text and a delete removes it.
	SentTexts(chatID string) []string
	PreviewsDisabled() bool // true when every outgoing message disabled previews/unfurls/embeds
}

// PlatformIDs is optionally implemented by a PlatformFake whose platform needs
// particular id shapes. Without it the Channel suite uses "424242" as both the
// sender and the DM chat id, as Telegram private chats have.
type PlatformIDs interface {
	ConformanceIDs() (dmChatID, senderID string)
}
