package refine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestExtract(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Six times seven.\nFinal answer: 42", "42"},
		{"Final answer: 41\nOn reflection:\nFinal answer: 42", "42"},
		{"**Final answer:** Paris", "Paris"},
		{"**Final Answer**: Paris.", "Paris"},
		{"## final answer: yes", "yes"},
		{"> Final answer: $x^2$", "x^2"},
		{`Final answer: \boxed{28}`, "28"},
		{`Final answer: $\boxed{\frac{1}{2}}$`, `\frac{1}{2}`},
		{"Final answer:\n```\nls -la\n```", "```\nls -la\n```"},
		{"Final answer: Wait for it...", "Wait for it..."},
		{"The final answer: 42", ""},
		{"Final answers: 42", ""},
		{"No marker here", ""},
		{"", ""},
	} {
		if got := Extract(tc.in); got != tc.want {
			t.Errorf("Extract(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  42  ", "42"},
		{"3.500", "3.5"},
		{"2.0", "2"},
		{"10.00 USD", "10 usd"},
		{"1,000.50", "1,000.5"},
		{"-2.10", "-2.1"},
		{"100", "100"},
		{"1.05", "1.05"},
		{"v1.2.0", "v1.2.0"},
		{"2026.10.14", "2026.10.14"},
		{`1\,000`, "1000"},
		{`x\quad y`, "x y"},
		{`a\!b\;c\:d`, "abcd"},
		{"{42}", "42"},
		{"{{42}}", "{42}"},
		{"{a} + {b}", "{a} + {b}"},
		{"The   Quick\n\tFox", "the quick fox"},
		{"Paris", "paris"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if Normalize(Extract("Final answer: 3.50")) != Normalize(Extract(`**Final answer:** {3.5}`)) {
		t.Error("equal answers in different dress must normalize alike")
	}
}

func TestVote(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		winner  int
		votes   []core.RefineVote
	}{
		{"plurality", []string{"41", "42", "42"}, 1,
			[]core.RefineVote{{Answer: "42", Count: 2, Drafts: []int{1, 2}}, {Answer: "41", Count: 1, Drafts: []int{0}}}},
		{"tie goes to the earliest draft", []string{"b", "a", "a", "b"}, 0,
			[]core.RefineVote{{Answer: "b", Count: 2, Drafts: []int{0, 3}}, {Answer: "a", Count: 2, Drafts: []int{1, 2}}}},
		{"empty answers do not vote", []string{"", "", "7"}, 2,
			[]core.RefineVote{{Answer: "7", Count: 1, Drafts: []int{2}}}},
		{"nothing voted", []string{"", ""}, -1, []core.RefineVote{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, v := Vote(tc.answers)
			if w != tc.winner || !reflect.DeepEqual(v, tc.votes) {
				t.Fatalf("Vote(%q) = %d %+v, want %d %+v", tc.answers, w, v, tc.winner, tc.votes)
			}
		})
	}
}

func TestConfirmed(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"Verdict: correct\nThe arithmetic checks out.", true},
		{"\n  **Verdict:** Correct.", true},
		{"Verdict: correct, with one stylistic note.", true},
		{"Verdict: needs changes\n- 6 × 7 is 42", false},
		{"Verdict: incorrect", false},
		{"Verdict: correctness unclear", false},
		{"Looks good.\nVerdict: correct", false},
		{"", false},
	} {
		if got := Confirmed(tc.in); got != tc.want {
			t.Errorf("Confirmed(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMessage(t *testing.T) {
	wrap := func(kind, text string) string { return "<" + kind + ">" + text + "</" + kind + ">" }
	c := Call{Role: core.RefineCorrect, Trace: "draft text", Critique: "critique text"}
	got := Message(c, Quotes(c), wrap)
	for _, want := range []string{"[Bothy refine · corrector]", "<draft>draft text</draft>", "<critique>critique text</critique>",
		"<instructions>\n" + strings.TrimSpace(correctorPrompt) + "\n</instructions>"} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "<draft>") > strings.Index(got, "<critique>") || strings.Index(got, "<critique>") > strings.Index(got, "<instructions>") {
		t.Errorf("want draft, critique, then instructions:\n%s", got)
	}
	for _, r := range []core.RefineRole{core.RefineContinue, core.RefineCritique, core.RefineCorrect} {
		p := Prompt(r)
		if strings.TrimSpace(p) == "" {
			t.Fatalf("%s: empty prompt", r)
		}
		if r != core.RefineCritique && !strings.Contains(p, "Final answer:") {
			t.Errorf("%s prompt must ask for a final answer line", r)
		}
	}
	if !strings.Contains(Prompt(core.RefineCritique), "Verdict: correct") {
		t.Error("the critic must be able to confirm a draft")
	}
	if Prompt(core.RefineDraft) != "" || len(Quotes(Call{Role: core.RefineCritique})) != 1 {
		t.Error("drafts have no role prompt; a critique quotes only the draft")
	}
}

func TestStrip(t *testing.T) {
	for in, want := range map[string]string{
		"Six times seven is 42.\nFinal answer: 42":         "Six times seven is 42.",
		"Final answer: 42":                                 "42",
		"Paris is the capital.\n\n**Final answer:** Paris": "Paris is the capital.",
		"Run this:\nFinal answer:\n```\nls -la\n```":       "Run this:",
		"No marker here.":                                  "No marker here.",
	} {
		if got := Strip(in); got != want {
			t.Errorf("Strip(%q) = %q, want %q", in, got, want)
		}
	}
}
