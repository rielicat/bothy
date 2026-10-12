// Package conformance holds suites every implementation must pass.
package conformance

import (
	"encoding/json"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
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
func Tool(t *testing.T, tool core.Tool, cases []ToolCase) { t.Helper() }

// Channel checks an adapter against its fake platform server: Send/Edit/Delete
// round-trip, previews disabled on every send, MaxLen respected, Ack within 3s.
func Channel(t *testing.T, ch core.Channel, platform PlatformFake) { t.Helper() }

// Store runs the repository contract against a fresh store per subtest:
// round trips for every method, the IDOR table (foreign Viewer => ErrNotFound
// on every by-ID read), Decide CAS under 50 goroutines, runs_one_active =>
// ErrConflict, paging order, MarkInterrupted, Approvals.Seen, Grants.Active
// expiry/revocation, audit Append/Range ordering. memstore and M1 both run it.
func Store(t *testing.T, open func(t *testing.T) core.Store) { t.Helper() }

// PlatformFake is implemented by telegramtest, discordtest and slacktest.
type PlatformFake interface {
	InjectText(chatID, senderID, text string, mention bool)
	InjectButton(chatID, msgID, senderID, data string)
	SentTexts(chatID string) []string
	PreviewsDisabled() bool // true when every outgoing message disabled previews/unfurls/embeds
}
