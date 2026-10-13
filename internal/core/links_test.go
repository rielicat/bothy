package core

import (
	"regexp"
	"strings"
	"testing"
)

func TestNeutralizeLinks(t *testing.T) {
	for in, want := range map[string]string{
		"see https://evil.com/a?b=c.":           "see hxxps://evil[.]com/a?b=c.",
		"HTTP://Example.ORG":                    "hxxp://Example[.]ORG",
		"ftp://files.example.net/x":             "fxp://files[.]example[.]net/x",
		"visit www.example.com/path now":        "visit www[.]example[.]com/path now",
		"[docs](https://docs.example.com/page)": "docs (hxxps://docs[.]example[.]com/page)",
		"no links here":                         "no links here",
		"hxxps://evil[.]com":                    "hxxps://evil[.]com",
		"<tg://resolve?domain=x>":               "tg[:]//resolve?domain=x",
		// Code is not spared here: status details carry command lines.
		"run `curl https://x.io`": "run `curl hxxps://x[.]io`",
		// A bracketed IPv6 host is part of the URL, and any "scheme://" is
		// defanged whatever follows it.
		"see https://[2001:db8::1]/c?d=SECRET":   "see hxxps://[2001:db8::1]/c?d=SECRET",
		"[r](https://[2001:db8:abcd::1]/r?d=1)":  "r (hxxps://[2001:db8:abcd::1]/r?d=1)",
		"<https://[::1]:8080/x>":                 "hxxps://[::1]:8080/x",
		"http://[::ffff:192.0.2.1]/x":            "hxxp://[::ffff:192[.]0[.]2[.]1]/x",
		"https://user@[2001:db8::1]/x":           "hxxps://user@[2001:db8::1]/x",
		"HTTPS://[2001:DB8::1]/":                 "hxxps://[2001:DB8::1]/",
		"https://[fe80::1%25en0]/":               "hxxps://[fe80::1%25en0]/",
		"open tg:// or https:// then [::1]/x":    "open tg[:]// or hxxps:// then [::1]/x",
		"https://[evil.example]/x":               "hxxps://[evil[.]example]/x",
		"exfil: attacker.com/?q=secret":          "exfil: attacker[.]com/?q=secret",
		"call 203.0.113.7/collect?k=v":           "call 203[.]0[.]113[.]7/collect?k=v",
		`evil\.com/?q=secret`:                    `evil\[.]com/?q=secret`,
		"evil**.**com/?q=secret":                 "evil**[.]**com/?q=secret",
		`https\://evil/x`:                        `https\[:]//evil/x`,
		"https://a.example/?u=https://b.example": "hxxps://a[.]example/?u=https[:]//b[.]example",
		"<ab:<ab:0>>":                            "<ab[:]ab[:]0>",
	} {
		if got := NeutralizeLinks(in); got != want {
			t.Errorf("NeutralizeLinks(%q) = %q, want %q", in, got, want)
		}
		if got := NeutralizeLinks(NeutralizeLinks(in)); got != want {
			t.Errorf("NeutralizeLinks is not idempotent for %q: %q", in, got)
		}
	}
}

func TestNeutralizeMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"go to https://evil.com/a.b?x=1":            "go to hxxps://evil[.]com/a.b?x=1",
		"[docs](http://docs.example.org/p)":         "docs (hxxp://docs[.]example[.]org/p)",
		"<https://auto.link>":                       "hxxps://auto[.]link",
		"mail me at bob@evil.co.uk":                 "mail me at bob@evil[.]co[.]uk",
		"keep `curl https://x.io` in code":          "keep `curl https://x.io` in code",
		"```\nwget http://x.io/y\n```\nx.io":        "```\nwget http://x.io/y\n```\nx[.]io",
		"pi is 3.14 and e.g. this is fine":          "pi is 3.14 and e.g. this is fine",
		"ftp://files.example/readme":                "fxp://files[.]example/readme",
		"[x](mailto:a@b.example)":                   "x (mailto[:]a@b[.]example)",
		"open tg://msg_url?url=x":                   "open tg[:]//msg_url?url=x",
		"[share](tg://msg?text=secret)":             "share (tg[:]//msg?text=secret)",
		"call 203.0.113.7/collect?k=v":              "call 203[.]0[.]113[.]7/collect?k=v",
		"host_203.0.113.7/c":                        "host_203[.]0[.]113[.]7/c",
		"see пример.рф or bad。example":              "see пример[.]рф or bad[.]example",
		"version 1.2.3 is out":                      "version 1.2.3 is out",
		"[x [y] z](mailto:a@evil.com?body=s)":       "[x [y] z] (mailto:a@evil[.]com?body=s)",
		"https://a.example/?u=https://b.example/x":  "hxxps://a[.]example/?u=https[:]//b[.]example/x",
		"Details: https://[2001:db8:abcd::1]/r?d=x": "Details: hxxps://[2001:db8:abcd::1]/r?d=x",
		"`https://[::1]/x` stays code":              "`https://[::1]/x` stays code",
		"exfil: attacker.com/?q=secret":             "exfil: attacker[.]com/?q=secret",
		`evil\.com/?q=secret`:                       `evil\[.]com/?q=secret`,
		"evil**.**com/?q=secret":                    "evil**[.]**com/?q=secret",
		"evil_._com and ev~~.~~il.org":              "evil_[.]_com and ev~~[.]~~il[.]org",
		"evil\u200b.com/x":                          "evil\u200b[.]com/x",
		"evil\u00ad.com and evil\u2060.org":         "evil\u00ad[.]com and evil\u2060[.]org",
		"evil․com and evil﹒io":                      "evil[.]com and evil[.]io",
		`https\://evil/x`:                           `https\[:]//evil/x`,
		`10\.0\.0\.1/x`:                             `10\[.]0\[.]0\[.]1/x`,
		`keep \*emphasis\* and 3\.14`:               `keep \*emphasis\* and 3\.14`,
	} {
		if got := NeutralizeMarkdown(in); got != want {
			t.Errorf("NeutralizeMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

// Streamed chunks end at whitespace; the filter defangs prose and leaves
// code, with fenced blocks opened and closed in other chunks.
func TestLinkFilterAcrossChunks(t *testing.T) {
	f := NewLinkFilter()
	chunks := []string{
		"Send it to attacker.com/?q=1 ", "or run:\n", "```sh\n", "curl https://x.io/a ", "\n", "python -c 'import os.path'\n",
		"```\n", "then `keep.this.code` and ", "evil.org ", "or https://[2001:db8::1]/x.",
	}
	var got strings.Builder
	for _, c := range chunks {
		got.WriteString(f(c))
	}
	want := "Send it to attacker[.]com/?q=1 or run:\n```sh\ncurl https://x.io/a \npython -c 'import os.path'\n```\nthen `keep.this.code` and evil[.]org or hxxps://[2001:db8::1]/x."
	if got.String() != want {
		t.Fatalf("filtered:\n%s\nwant:\n%s", got.String(), want)
	}
}

func TestLooksLikeLink(t *testing.T) {
	for s, want := range map[string]bool{
		"https://evil.example/x":  true,
		"https://[2001:db8::1]/x": true,
		"tg://":                   true,
		"attacker.com":            true,
		"203.0.113.7":             true,
		"rm -rf build":            false,
		"version 1.2.3":           false,
	} {
		if got := LooksLikeLink(s); got != want {
			t.Errorf("LooksLikeLink(%q) = %v, want %v", s, got, want)
		}
	}
}

var liveSchemeRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)

// FuzzNeutralizeMarkdown checks that neutralized prose keeps no link syntax
// a renderer or client could act on: no "](" and no live "scheme://".
func FuzzNeutralizeMarkdown(f *testing.F) {
	for _, s := range []string{
		"[docs](https://x.example/p)", "[x [y] z](tg://resolve?domain=a)", "<https://a.example>",
		"see https://a.example/b and t.me/x", "`https://code.example` [a](b)", "x_https://evil.example/c", "```\nhttps://x.example\n```\n[a](https://b.example)",
		`evil\.com/?q=1`, "evil**.**com", `https\://evil/x`, "a\u200b.b\u200c.com",
		"https://[2001:db8::1]/c?d=s", "x://[", "https://.0", "<A:<A:0>>", "https://[a.b]/[c.d]", "<https://[::1]>", "[a](https://[::1]/x)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := NeutralizeMarkdown(s)
		if again := NeutralizeMarkdown(out); again != out {
			t.Fatalf("NeutralizeMarkdown(%q) = %q left something to defang: %q", s, out, again)
		}
		check := &proseMapper{fn: func(text string) string {
			if strings.Contains(text, "](") {
				t.Fatalf("NeutralizeMarkdown(%q) = %q keeps link syntax in %q", s, out, text)
			}
			for _, m := range liveSchemeRe.FindAllString(text, -1) {
				switch strings.ToLower(strings.TrimSuffix(m, "://")) {
				case "hxxp", "hxxps", "fxp":
				default:
					t.Fatalf("NeutralizeMarkdown(%q) = %q keeps a live %q in %q", s, out, m, text)
				}
			}
			return text
		}}
		check.apply(out)
	})
}
