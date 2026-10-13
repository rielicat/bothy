package telegram

import (
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
)

func TestRender(t *testing.T) {
	for _, tc := range []struct{ in, html string }{
		{"plain & <tags>", "plain &amp; &lt;tags&gt;"},
		{"**bold** and *it* and _it_", "<b>bold</b> and <i>it</i> and <i>it</i>"},
		{"snake_case_name stays", "snake_case_name stays"},
		{"2 * 3 * 4", "2 * 3 * 4"},
		{"`a<b`", "<code>a&lt;b</code>"},
		{"``a ` b``", "<code>a ` b</code>"},
		{"**bold `code`**", "<b>bold code</b>"},
		{"[site](https://example.com/?a=1&b=\"2\")", `<a href="https://example.com/?a=1&amp;b=&quot;2&quot;">site</a>`},
		{"[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{"[**b**](https://e.com)", `<a href="https://e.com"><b>b</b></a>`},
		{"# Title", "<b>Title</b>"},
		{"- one\n* two", "• one\n• two"},
		{"\\*not italic\\*", "*not italic*"},
		{"```go\nfmt.Println(\"<x>\")\n```", `<pre><code class="language-go">fmt.Println(&quot;&lt;x&gt;&quot;)</code></pre>`},
		{"````\n```\ninner\n```\n````", "<pre>```\ninner\n```</pre>"},
		{"~~~\nno close", "<pre>no close</pre>"},
		{"**unclosed", "**unclosed"},
	} {
		html, _ := render(tc.in)
		if html != tc.html {
			t.Errorf("render(%q) = %q, want %q", tc.in, html, tc.html)
		}
	}
}

var renderCorpus = []string{
	"", "<", ">", "&", "&amp;", "&#0;", "</b>", "<b>", "**<i>**", "**a *b* c**", "*a **b** c*",
	"[a](https://ok.com/\"><script>)", "[a [b] c](https://e.com)", "[unterminated](https://e.com",
	"`a</code>`", "```\n</pre><b>\n```", "``` js\n<x>\n```", "_a_b_", "__", "****", "* * *",
	"`` ` ``", "[`x`](https://e.com)", "**[x](https://e.com)**", "# **h** `c`", "- *x* `y`",
	"\\", "\\\\*", "a\nb\n\n```\n", "emoji 🙂 **ok**", "tab\there", "\u202emirror",
}

func TestRenderAlwaysValidHTML(t *testing.T) {
	for _, in := range renderCorpus {
		checkRender(t, in)
	}
}

func FuzzRender(f *testing.F) {
	for _, in := range renderCorpus {
		f.Add(in)
	}
	f.Fuzz(checkRender)
}

// checkRender asserts Telegram accepts the HTML and shows exactly the plain
// text the renderer reports.
func checkRender(t *testing.T, in string) {
	if !strings.ContainsRune(in, 0) && len(in) < 10000 {
		html, plain := render(in)
		visible, err := telegramtest.Visible(html, "HTML")
		if err != nil {
			t.Fatalf("render(%q) = %q: %v", in, html, err)
		}
		if visible != plain {
			t.Fatalf("render(%q): visible %q != plain %q", in, visible, plain)
		}
		if utf16Len(plain) > utf16Len(in) {
			t.Fatalf("render(%q) made the visible text longer: %q", in, plain)
		}
	}
}
