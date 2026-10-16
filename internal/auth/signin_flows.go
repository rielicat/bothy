package auth

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// SignInIntent says what a provider round trip is for.
type SignInIntent string

const (
	IntentSignIn SignInIntent = "signin" // sign in, or join with allow_new
	IntentLink   SignInIntent = "link"   // a signed-in person connects the provider to their account
)

// flow is one provider round trip in progress. It lives only in memory: a
// restart loses it and the person starts again.
type flow struct {
	provider core.SignInProvider
	intent   SignInIntent
	verifier string
	nonce    string
	redirect string
	clientID string // the client the browser was sent to; it must still be configured at the callback
	next     string // signin: validated path to land on
	// link: who started it, and the sha256 of the session cookie they started
	// it with. The callback never sees that cookie (SameSite=Strict), so the
	// hash is what binds the round trip to a live session.
	user        core.UserID
	sessionHash []byte
	client      string // who started it: an IPv4 address or an IPv6 /64
	expires     time.Time
}

const (
	flowTTL  = 10 * time.Minute
	maxFlows = 1024
	// maxClientFlows bounds one client's flows in progress, so a single
	// address can't push everyone else's out of the store.
	maxClientFlows = 32
)

// flowStore keeps flows by the sha256 of their state, single use.
type flowStore struct {
	clock core.Clock
	mu    sync.Mutex
	m     map[string]*flow
}

func newFlowStore(c core.Clock) *flowStore { return &flowStore{clock: c, m: map[string]*flow{}} }

// put stores f under state. Expired flows go first; then a client at
// maxClientFlows loses its own oldest flow, and only a store filled to
// maxFlows by many clients drops the oldest of all, so a flood of starts
// can neither grow memory nor cut short other people's sign-ins.
func (fs *flowStore) put(state string, f *flow) {
	now := fs.clock.Now()
	f.expires = now.Add(flowTTL)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	own := 0
	var ownOldest, oldest string
	for k, g := range fs.m {
		if !now.Before(g.expires) {
			delete(fs.m, k)
			continue
		}
		if g.client == f.client {
			own++
			if ownOldest == "" || g.expires.Before(fs.m[ownOldest].expires) {
				ownOldest = k
			}
		}
		if oldest == "" || g.expires.Before(fs.m[oldest].expires) {
			oldest = k
		}
	}
	switch {
	case own >= maxClientFlows:
		delete(fs.m, ownOldest)
	case len(fs.m) >= maxFlows:
		delete(fs.m, oldest)
	}
	fs.m[flowKey(state)] = f
}

// take removes and returns the flow for state; nil when there is none or it
// expired.
func (fs *flowStore) take(state string) *flow {
	if state == "" || len(state) > 256 {
		return nil
	}
	k := flowKey(state)
	fs.mu.Lock()
	f := fs.m[k]
	delete(fs.m, k)
	fs.mu.Unlock()
	if f == nil || !fs.clock.Now().Before(f.expires) {
		return nil
	}
	return f
}

func (fs *flowStore) len() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.m)
}

func flowKey(state string) string { return hex.EncodeToString(sha(state)) }
