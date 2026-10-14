package bedrock

import "strings"

const (
	novaOpen  = "<thinking>"
	novaClose = "</thinking>"
)

// novaThinking moves the <thinking>…</thinking> preamble that Nova
// understanding models write into a text block (before tool calls, mostly)
// out of the reply text and into a thinking part, across delta boundaries.
type novaThinking struct {
	state int // novaDeciding, novaInside, novaAfter, novaText
	buf   string
}

const (
	novaDeciding = iota // the block may still start with <thinking>
	novaInside          // inside the tags
	novaAfter           // after </thinking>, dropping the newline that follows
	novaText            // plain text from here on
)

func (n *novaThinking) feed(s string, thinking, text func(string) error) error {
	for s != "" {
		switch n.state {
		case novaDeciding:
			n.buf += s
			s = ""
			t := strings.TrimLeft(n.buf, " \t\r\n")
			switch {
			case strings.HasPrefix(t, novaOpen):
				n.state, n.buf, s = novaInside, "", t[len(novaOpen):]
			case strings.HasPrefix(novaOpen, t):
				// Too short to tell yet.
			default:
				n.state, s, n.buf = novaText, n.buf, ""
			}
		case novaInside:
			n.buf += s
			s = ""
			if i := strings.Index(n.buf, novaClose); i >= 0 {
				if err := thinking(n.buf[:i]); err != nil {
					return err
				}
				n.state, s, n.buf = novaAfter, n.buf[i+len(novaClose):], ""
				continue
			}
			// Hold back what could be the start of </thinking>.
			keep := 0
			for k := min(len(n.buf), len(novaClose)-1); k > 0; k-- {
				if strings.HasSuffix(n.buf, novaClose[:k]) {
					keep = k
					break
				}
			}
			if err := thinking(n.buf[:len(n.buf)-keep]); err != nil {
				return err
			}
			n.buf = n.buf[len(n.buf)-keep:]
		case novaAfter:
			s = strings.TrimLeft(s, " \t\r\n")
			if s != "" {
				n.state = novaText
			}
		case novaText:
			err := text(s)
			s = ""
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// flush ends the block: text that never became a <thinking> preamble is
// text, an unterminated preamble stays thinking.
func (n *novaThinking) flush(thinking, text func(string) error) error {
	buf := n.buf
	n.buf = ""
	switch n.state {
	case novaDeciding:
		return text(buf)
	case novaInside:
		return thinking(buf)
	}
	return nil
}
