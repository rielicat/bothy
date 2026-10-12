package events

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	defaultRing      = 4096
	defaultSubBuffer = 256
)

type Options struct {
	Ring  int // default 4096
	Clock core.Clock
}

// Bus implements core.Bus.
type Bus struct {
	clock core.Clock
	boot  string

	mu      sync.Mutex
	seq     uint64
	ring    []core.Event // circular; the newest event is at head-1
	head    int
	count   int
	subs    map[*subscription]struct{}
	banners map[bannerKey]core.Event
}

type bannerKey struct {
	principal core.PrincipalID
	code      string
}

func New(o Options) *Bus {
	if o.Ring <= 0 {
		o.Ring = defaultRing
	}
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return &Bus{
		clock:   o.Clock,
		boot:    hex.EncodeToString(b[:]),
		ring:    make([]core.Event, o.Ring),
		subs:    map[*subscription]struct{}{},
		banners: map[bannerKey]core.Event{},
	}
}
