package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

type ghPhase uint8

const (
	ghStarted    ghPhase = iota // manifest built, handoff page not yet past GitHub
	ghExchanging                // GitHub's code is being redeemed
	ghInstalling                // app created; waiting for GitHub's setup redirect
	ghDone                      // the setup redirect arrived
)

// ghAppFlow is one GitHub App manifest round trip. It lives in memory: a
// restart loses it (an app already created stays pending and the GitHub
// page checks it).
type ghAppFlow struct {
	user      core.UserID
	session   [32]byte // sha256 of the session cookie that started it (the auth store's key)
	stateHash [32]byte // sha256 of the state GitHub echoes back
	// state is the one GitHub was given last: for the handoff page's form,
	// then to resend the install address after Back.
	state      string
	codeHash   [32]byte // sha256 of the code redeemed, so Back to the old redirect resends the install address
	prevState  [32]byte
	phase      ghPhase
	target     core.GitHubAppTarget
	replaces   string
	handoff    *core.GitHubAppHandoff
	app        string // gha_ id once created
	installURL string
	expires    time.Time
}

const (
	ghFlowTTL         = time.Hour // GitHub's manifest codes live an hour too
	maxGHFlows        = 64
	maxGHFlowsPerUser = 4
)

// ghAppFlows keeps flows by the sha256 of their flow cookie, the random
// binding that never appears in a URL.
type ghAppFlows struct {
	clock core.Clock
	mu    sync.Mutex
	m     map[[32]byte]*ghAppFlow
}

func newGHAppFlows(c core.Clock) *ghAppFlows {
	return &ghAppFlows{clock: c, m: map[[32]byte]*ghAppFlow{}}
}

func sha32(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// randomToken is 32 random bytes in base64url.
func randomToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// fresh is the expiry of a flow (re)started now.
func (fs *ghAppFlows) fresh() time.Time { return fs.clock.Now().Add(ghFlowTTL) }

// put stores f under binding, expiring in ghFlowTTL. Expired flows go
// first; a user at maxGHFlowsPerUser loses their own oldest, and only a
// store filled to maxGHFlows loses the oldest of all.
func (fs *ghAppFlows) put(binding string, f *ghAppFlow) {
	now := fs.clock.Now()
	f.expires = now.Add(ghFlowTTL)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	own := 0
	var ownOldest, oldest [32]byte
	var haveOwn, haveAny bool
	for k, g := range fs.m {
		if !now.Before(g.expires) {
			delete(fs.m, k)
			continue
		}
		if g.user == f.user {
			own++
			if !haveOwn || g.expires.Before(fs.m[ownOldest].expires) {
				ownOldest, haveOwn = k, true
			}
		}
		if !haveAny || g.expires.Before(fs.m[oldest].expires) {
			oldest, haveAny = k, true
		}
	}
	switch {
	case own >= maxGHFlowsPerUser:
		delete(fs.m, ownOldest)
	case len(fs.m) >= maxGHFlows:
		delete(fs.m, oldest)
	}
	fs.m[sha32(binding)] = f
}

// get returns a copy of the flow; an expired one counts as missing.
func (fs *ghAppFlows) get(binding string) (ghAppFlow, bool) {
	if binding == "" || len(binding) > 256 {
		return ghAppFlow{}, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, ok := fs.m[sha32(binding)]
	if !ok || !fs.clock.Now().Before(f.expires) {
		return ghAppFlow{}, false
	}
	return *f, true
}

// advance moves the flow from one phase to the next when state is the one
// GitHub was given, applying mut under the lock: each step happens once.
func (fs *ghAppFlows) advance(binding string, from, to ghPhase, state string, mut func(*ghAppFlow)) (ghAppFlow, bool) {
	if binding == "" || len(binding) > 256 || state == "" || len(state) > 256 {
		return ghAppFlow{}, false
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, ok := fs.m[sha32(binding)]
	sh := sha32(state)
	if !ok || f.phase != from || !fs.clock.Now().Before(f.expires) || subtle.ConstantTimeCompare(sh[:], f.stateHash[:]) != 1 {
		return ghAppFlow{}, false
	}
	f.phase = to
	if mut != nil {
		mut(f)
	}
	return *f, true
}

// gaveState reports whether state is the one GitHub was given last, or the
// one before it.
func (f *ghAppFlow) gaveState(state string) bool {
	if state == "" || len(state) > 256 {
		return false
	}
	sh := sha32(state)
	return subtle.ConstantTimeCompare(sh[:], f.stateHash[:]) == 1 || subtle.ConstantTimeCompare(sh[:], f.prevState[:]) == 1
}

func (fs *ghAppFlows) drop(binding string) {
	if binding == "" {
		return
	}
	fs.mu.Lock()
	delete(fs.m, sha32(binding))
	fs.mu.Unlock()
}
