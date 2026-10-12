package mcp

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// toASCII lower-cases a host name and punycode-encodes (RFC 3492) every label
// holding non-ASCII characters, so Actions and approval cards show
// "xn--pple-43d.com" rather than a homograph. It applies no UTS #46 mapping
// beyond lower-casing: x/net/idna is not among this module's dependencies, and
// the punycode form is what the policy and card renderer compare and show.
func toASCII(host string) (string, error) {
	if !utf8.ValidString(host) {
		return "", errors.New("host is not valid UTF-8")
	}
	labels := strings.Split(strings.ToLower(host), ".")
	for i, l := range labels {
		if isASCII(l) {
			continue
		}
		enc, err := punycode(l)
		if err != nil {
			return "", err
		}
		labels[i] = "xn--" + enc
	}
	return strings.Join(labels, "."), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

const (
	pcBase        = 36
	pcTMin        = 1
	pcTMax        = 26
	pcSkew        = 38
	pcDamp        = 700
	pcInitialBias = 72
	pcInitialN    = 128
	pcMaxInt      = 1<<31 - 1
)

var errPunycodeOverflow = errors.New("punycode: label too long")

func punycode(label string) (string, error) {
	input := []rune(label)
	var out strings.Builder
	for _, r := range input {
		if r < utf8.RuneSelf {
			out.WriteRune(r)
		}
	}
	basic := out.Len()
	h := basic
	if basic > 0 {
		out.WriteByte('-')
	}
	n, delta, bias := pcInitialN, 0, pcInitialBias
	for h < len(input) {
		m := pcMaxInt
		for _, r := range input {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if (m - n) > (pcMaxInt-delta)/(h+1) {
			return "", errPunycodeOverflow
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range input {
			if int(r) < n {
				if delta++; delta < 0 {
					return "", errPunycodeOverflow
				}
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := pcBase; ; k += pcBase {
				t := k - bias
				if t < pcTMin {
					t = pcTMin
				} else if t > pcTMax {
					t = pcTMax
				}
				if q < t {
					break
				}
				out.WriteByte(pcDigit(t + (q-t)%(pcBase-t)))
				q = (q - t) / (pcBase - t)
			}
			out.WriteByte(pcDigit(q))
			bias = pcAdapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return out.String(), nil
}

func pcAdapt(delta, numPoints int, first bool) int {
	if first {
		delta /= pcDamp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((pcBase-pcTMin)*pcTMax)/2 {
		delta /= pcBase - pcTMin
		k += pcBase
	}
	return k + (pcBase-pcTMin+1)*delta/(delta+pcSkew)
}

func pcDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}
