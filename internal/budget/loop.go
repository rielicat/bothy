package budget

import "github.com/bothyhq/bothy/internal/core"

const (
	loopRing        = 16
	sameResultAbort = 3 // identical (tool, args, result) entries
	altWindow       = 8 // A-B-A-B detection window
	sameTextAbort   = 3 // identical assistant text in consecutive steps
)

type loopEntry struct {
	tool         string
	args, result uint64
}

// loopDetector keeps the last 16 completed tool calls (SPEC §5.13).
type loopDetector struct {
	ring  [loopRing]loopEntry
	n     int // entries recorded, capped at loopRing
	head  int // next write position
	text  uint64
	texts int
}

func (d *loopDetector) at(i int) loopEntry { // i = 0 is the newest entry
	return d.ring[(d.head-1-i+2*loopRing)%loopRing]
}

func (d *loopDetector) count(tool string, args uint64, result *uint64) int {
	c := 0
	for i := range d.n {
		e := d.at(i)
		if e.tool == tool && e.args == args && (result == nil || e.result == *result) {
			c++
		}
	}
	return c
}

// observeTool with resultHash 0 is the pre-execution check: it records
// nothing and aborts only when running the call would reach abortAfter.
func (d *loopDetector) observeTool(tool string, args, result uint64, warnAfter, abortAfter int) core.LoopVerdict {
	if warnAfter <= 0 {
		warnAfter = 3
	}
	if abortAfter <= 0 {
		abortAfter = 5
	}
	if result == 0 {
		if d.count(tool, args, nil)+1 >= abortAfter {
			return core.LoopAbort
		}
		return core.LoopOK
	}
	d.ring[d.head] = loopEntry{tool: tool, args: args, result: result}
	d.head = (d.head + 1) % loopRing
	d.n = min(d.n+1, loopRing)
	same := d.count(tool, args, nil)
	if same >= abortAfter || d.count(tool, args, &result) >= sameResultAbort || d.alternating() {
		return core.LoopAbort
	}
	if same >= warnAfter {
		return core.LoopWarn
	}
	return core.LoopOK
}

// alternating reports an A-B-A-B pattern over the last altWindow entries.
func (d *loopDetector) alternating() bool {
	if d.n < altWindow {
		return false
	}
	key := func(e loopEntry) loopEntry { return loopEntry{tool: e.tool, args: e.args} }
	if key(d.at(0)) == key(d.at(1)) {
		return false
	}
	for i := 2; i < altWindow; i++ {
		if key(d.at(i)) != key(d.at(i-2)) {
			return false
		}
	}
	return true
}

// observeText takes the hash of a step's assistant text; 0 (no text) breaks
// the streak.
func (d *loopDetector) observeText(h uint64) core.LoopVerdict {
	switch h {
	case 0:
		d.text, d.texts = 0, 0
	case d.text:
		d.texts++
	default:
		d.text, d.texts = h, 1
	}
	if d.texts >= sameTextAbort {
		return core.LoopAbort
	}
	return core.LoopOK
}
