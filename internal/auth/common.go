package auth

import (
	_ "embed"
	"strings"
	"sync"
)

// commonPasswords is one lowercase entry per line. It is matched against the
// password and against its "core": lower-cased, leetspeak undone, and with
// leading and trailing digits and punctuation removed, so "Dragon2024!" and
// "dr4g0n!!" both hit "dragon". The list stays server-side so the web bundle
// remains small.
//
//go:embed common_passwords.txt
var commonPasswords string

var commonSet = sync.OnceValue(func() map[string]struct{} {
	m := make(map[string]struct{}, 2048)
	for _, line := range strings.Split(commonPasswords, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			m[strings.ToLower(line)] = struct{}{}
		}
	}
	return m
})

var leet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "8", "b", "9", "g",
	"@", "a", "$", "s", "!", "i", "|", "i", "+", "t")

const keyboardRows = "1234567890qwertyuiopasdfghjklzxcvbnmabcdefghijklmnopqrstuvwxyz"

func isCommonPassword(pw string) bool {
	set := commonSet()
	lower := strings.ToLower(pw)
	for _, c := range candidates(lower) {
		if c == "" {
			continue
		}
		if _, ok := set[c]; ok {
			return true
		}
		if trivial(c) {
			return true
		}
	}
	return trivial(lower)
}

func candidates(lower string) []string {
	squashed := strings.Join(strings.Fields(lower), "")
	core := strings.TrimFunc(lower, notLetter)
	out := []string{lower, squashed, core, strings.Join(strings.Fields(core), ""), leet.Replace(core), leet.Replace(strings.TrimFunc(squashed, notAlnum))}
	if u := unit(squashed); u != squashed {
		out = append(out, u, strings.TrimFunc(u, notLetter))
	}
	return out
}

func notLetter(r rune) bool { return !(r >= 'a' && r <= 'z' || r > 127) }

func notAlnum(r rune) bool { return notLetter(r) && !(r >= '0' && r <= '9') }

// unit returns the shortest string whose repetition is s ("abcabcabc" -> "abc").
func unit(s string) string {
	for n := 1; n <= len(s)/2; n++ {
		if len(s)%n == 0 && strings.Repeat(s[:n], len(s)/n) == s {
			return s[:n]
		}
	}
	return s
}

// trivial reports a single repeated character, a run along the keyboard or
// the alphabet (forwards or backwards), or a repetition of such a run.
func trivial(s string) bool {
	if len(s) < 4 {
		return len(s) > 0 && len(s) == strings.Count(s, s[:1])
	}
	u := unit(s)
	if len(u) <= 2 {
		return true
	}
	return strings.Contains(keyboardRows, u) || strings.Contains(reverse(keyboardRows), u)
}

func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
