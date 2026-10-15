package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// serviceNames spells the services a plain capital gets wrong: MCP servers
// are named by their lowercase config ids. Approval cards and the audit log
// both name servers with it; web/src/lib/format.ts has the same list.
var serviceNames = map[string]string{
	"bigquery":     "BigQuery",
	"circleci":     "CircleCI",
	"clickup":      "ClickUp",
	"deepwiki":     "DeepWiki",
	"dynamodb":     "DynamoDB",
	"github":       "GitHub",
	"gitlab":       "GitLab",
	"google-drive": "Google Drive",
	"hubspot":      "HubSpot",
	"launchdarkly": "LaunchDarkly",
	"mongodb":      "MongoDB",
	"mysql":        "MySQL",
	"openai":       "OpenAI",
	"pagerduty":    "PagerDuty",
	"posthog":      "PostHog",
	"postgres":     "Postgres",
	"postgresql":   "PostgreSQL",
	"pypi":         "PyPI",
	"sendgrid":     "SendGrid",
	"sqlite":       "SQLite",
	"youtube":      "YouTube",
}

// ServiceName names a configured MCP server for people: "deepwiki" →
// "DeepWiki", "team-docs" → "Team docs".
func ServiceName(id string) string {
	if n, ok := serviceNames[strings.ToLower(id)]; ok {
		return n
	}
	s := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(id))
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
