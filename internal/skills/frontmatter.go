package skills

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	skillFile      = "SKILL.md"
	maxNameLen     = 64
	maxDescription = 1024
)

var nameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// validName reports whether s is a skill name: lower-case letters, digits and
// single hyphens, at most 64 bytes. Names double as directory names, so this
// is also what keeps them free of path separators and dot segments.
func validName(s string) bool { return len(s) <= maxNameLen && nameRe.MatchString(s) }

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	License     string `yaml:"license"`
	// AllowedTools is advisory only: it is parsed so a malformed value is
	// reported, but it never widens or narrows what a run may call.
	AllowedTools yaml.Node `yaml:"allowed-tools"`
}

var errNoFrontmatter = errors.New("SKILL.md must start with a YAML frontmatter block (---)")

// parseSkill splits SKILL.md into its validated frontmatter and the body that
// follows it.
func parseSkill(data []byte) (frontmatter, string, error) {
	var fm frontmatter
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return fm, "", errNoFrontmatter
	}
	var head, body []byte
	if h, b, found := bytes.Cut(rest, []byte("\n---\n")); found {
		head, body = h, b
	} else if h, found := bytes.CutSuffix(rest, []byte("\n---")); found {
		head = h
	} else {
		return fm, "", errors.New("SKILL.md frontmatter is not closed with ---")
	}
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return fm, "", fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	switch {
	case !validName(fm.Name):
		return fm, "", fmt.Errorf("SKILL.md name %q: use lower-case letters, digits and single hyphens, at most %d characters", fm.Name, maxNameLen)
	case fm.Description == "":
		return fm, "", errors.New("SKILL.md description is required")
	case utf8.RuneCountInString(fm.Description) > maxDescription:
		return fm, "", fmt.Errorf("SKILL.md description is longer than %d characters", maxDescription)
	case !utf8.Valid(body):
		return fm, "", errors.New("SKILL.md is not valid UTF-8")
	}
	return fm, string(body), nil
}
