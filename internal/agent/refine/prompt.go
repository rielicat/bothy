package refine

import (
	_ "embed"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

var (
	//go:embed generator.md
	generatorPrompt string
	//go:embed critic.md
	criticPrompt string
	//go:embed corrector.md
	correctorPrompt string
)

// RoleName is the prompt role behind a call: the generator continues a
// draft, the critic reviews it, the corrector rewrites it.
func RoleName(r core.RefineRole) string {
	switch r {
	case core.RefineContinue:
		return "generator"
	case core.RefineCritique:
		return "critic"
	case core.RefineCorrect:
		return "corrector"
	}
	return "draft"
}

// Prompt returns a role's embedded instructions; drafts have none, since
// they are the run's own answer step repeated.
func Prompt(r core.RefineRole) string {
	switch r {
	case core.RefineContinue:
		return generatorPrompt
	case core.RefineCritique:
		return criticPrompt
	case core.RefineCorrect:
		return correctorPrompt
	}
	return ""
}

// Quote is one text a role call works on.
type Quote struct {
	Kind string // draft | critique
	Text string
}

// Quotes lists the texts a call quotes, in prompt order.
func Quotes(c Call) []Quote {
	q := []Quote{{Kind: "draft", Text: c.Trace}}
	if c.Role == core.RefineCorrect {
		q = append(q, Quote{Kind: "critique", Text: c.Critique})
	}
	return q
}

// Message renders the user-role message appended to the conversation for a
// continue, critique or correct call: a header naming the role, the quoted
// texts, each wrapped by wrap (which must give every block a closing tag its
// content cannot forge), and the role's instructions.
func Message(c Call, quotes []Quote, wrap func(kind, text string) string) string {
	var b strings.Builder
	b.WriteString("[Bothy refine · " + RoleName(c.Role) + "]\n\n")
	for _, q := range quotes {
		b.WriteString(wrap(q.Kind, q.Text))
		b.WriteString("\n\n")
	}
	b.WriteString("<instructions>\n")
	b.WriteString(strings.TrimSpace(Prompt(c.Role)))
	b.WriteString("\n</instructions>")
	return b.String()
}

// Confirmed reports whether a critique opens with "Verdict: correct" (the
// first non-empty line, Markdown emphasis tolerated).
func Confirmed(critique string) bool {
	for _, line := range strings.Split(critique, "\n") {
		s := strings.ToLower(strings.Trim(strings.TrimSpace(line), "*_`#> "))
		if s == "" {
			continue
		}
		rest, ok := strings.CutPrefix(s, "verdict")
		if !ok {
			return false
		}
		rest = strings.TrimLeft(rest, ":*_` ")
		after, ok := strings.CutPrefix(rest, "correct")
		if !ok {
			return false
		}
		next, _ := utf8.DecodeRuneInString(after)
		return after == "" || !unicode.IsLetter(next)
	}
	return false
}
