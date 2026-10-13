package agent

import (
	_ "embed"
	"strings"
)

var (
	//go:embed CONSTITUTION.md
	constitutionFull string
	//go:embed CONSTITUTION.short.md
	constitutionShort string
	//go:embed SOUL.md
	defaultSoul string
)

// constitutionVersion names the embedded constitution pair. TestConstitutionPinned
// fails when either file changes without a bump, so meta.constitution_version
// and GET /api/constitution always identify the text the model saw.
const constitutionVersion = "1"

// Constitution returns the embedded full constitution for GET /api/constitution.
func (r *Runner) Constitution() (version, text string) {
	return constitutionVersion, constitutionFull
}

// s1 renders the constitution block for a profile plus the administrator's
// standing rules, which may only add constraints.
func s1(p profile, standing []string) string {
	text := constitutionFull
	if p != profileStandard {
		text = constitutionShort
	}
	var rules []string
	for _, s := range standing {
		if s = strings.TrimSpace(s); s != "" {
			rules = append(rules, "- "+s)
		}
	}
	if len(rules) == 0 {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n## Standing rules from the administrator\n" + strings.Join(rules, "\n") + "\n"
}
