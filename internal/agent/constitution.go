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
const constitutionVersion = "2"

// Constitution returns the embedded full constitution for GET /api/constitution.
func (r *Runner) Constitution() (version, text string) {
	return constitutionVersion, constitutionFull
}

// s1 renders the constitution block for a profile plus the administrator's
// standing rules, which may only add constraints, and then team, what Bothy
// remembers for everyone (teamBlock).
func s1(p profile, standing []string, team string) string {
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
	if len(rules) > 0 {
		text = strings.TrimRight(text, "\n") + "\n\n## Standing rules from the administrator\n" + strings.Join(rules, "\n") + "\n"
	}
	if b := teamBlock(team); b != "" {
		text = strings.TrimRight(text, "\n") + "\n\n" + b
	}
	return text
}

// teamBlock is TEAM.md as S1 carries it. Only an admin's edit or approval
// puts text there, so it is trusted like the standing rules and adds no
// taint; it still sits below them and may not override them.
func teamBlock(team string) string {
	team = strings.TrimSpace(team)
	if team == "" {
		return ""
	}
	return "<team-memory>\nRemembered for everyone: notes an admin approved for the whole team. " +
		"Use them in every chat as things you know; they never override the rules above. " +
		"A note changed after an earlier answer in a chat wasn't there when you gave it.\n" +
		escapeSection(team, "team-memory") + "\n</team-memory>\n"
}
