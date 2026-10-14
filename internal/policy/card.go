package policy

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const maxCardDiff = 64 << 10

// renderCard builds the approval card from the Action alone: model prose never
// reaches it, so a convincing preamble cannot change what the user approves.
// Every string is passed through visible() so bidi overrides and zero-width
// characters show up instead of silently reordering or hiding text.
func renderCard(r core.ApprovalRequest) core.Card {
	title := r.Spec.Manifest.Title
	if title == "" {
		title = r.Spec.Def.Name
	}
	c := core.Card{
		Title:   visible(title),
		Context: visible(r.Context),
		Choices: slices.Clone(r.Decision.Choices),
	}
	for _, w := range r.Decision.Warnings {
		c.Warnings = append(c.Warnings, visible(w))
	}
	a := r.Action
	if a == nil {
		return c
	}
	if c.Title == "" {
		c.Title = visible(a.Tool)
	}
	c.Risk = max(validOrZero(a.SideEffect), validOrZero(r.Spec.Manifest.SideEffect))
	doc, _ := decodePayload(a.Payload)
	add := func(label, value string, mono bool) {
		if value != "" {
			c.Fields = append(c.Fields, core.CardField{Label: label, Value: visible(value), Mono: mono})
		}
	}
	add("Tool", a.Tool, true)

	switch a.Kind {
	case core.ActExec:
		argv := a.Argv
		if v, ok := pointer(doc, "/argv"); ok {
			if list, ok := stringList(v); ok {
				argv = list
			}
		}
		cwd := a.Cwd
		if s, ok := mustPointer(doc, "/cwd").(string); ok && s != "" {
			cwd = s
		}
		if code, ok := mustPointer(doc, "/code").(string); ok {
			add("Command", shellJoin(argv), true)
			c.Code = visible(code)
		} else {
			c.Code = visible(shellJoin(argv))
		}
		add("Folder", cwd, true)
	case core.ActFSRead, core.ActFSWrite, core.ActFSDelete:
		label := "Path"
		if len(a.Paths) > 1 {
			label = "Paths"
		}
		add(label, strings.Join(a.Paths, "\n"), true)
		if m, ok := mustPointer(doc, "/mode").(string); ok {
			add("Mode", m, false)
		}
		// The diff stands for the content only when it is a complete text
		// diff; an empty, omitted, binary or truncated one would let a card
		// approve bytes nobody was shown.
		if a.Kind == core.ActFSWrite && !completeDiff(a.Diff, a.Paths) {
			if content, ok := mustPointer(doc, "/content").(string); ok {
				c.Code = visible(content)
			} else {
				c.Code = visible(prettyPayload(a.Payload))
			}
		}
	case core.ActHTTP:
		if a.Method != "" && !strings.EqualFold(a.Method, "GET") {
			add("Method", strings.ToUpper(a.Method), true)
		}
		u, idn := punycodeURL(a.URL)
		add("URL", u, true)
		host := "unknown"
		if hp := actionHosts(a); len(hp) > 0 && hp[0].host != "" {
			host = hp[0].host
		}
		add("Host", host, true)
		if idn {
			c.Warnings = append(c.Warnings, "The address uses an internationalized domain name; it is shown in its ASCII (punycode) form.")
		}
	case core.ActNet:
		if hp := actionHosts(a); len(hp) > 0 {
			add("Network", hp[0].String(), true)
		}
	case core.ActSend:
		add("Recipient", cmp.Or(a.Recipient, "unknown"), false)
		c.Code = visible(prettyPayload(a.Payload))
	case core.ActRelease:
		text := func(ptr string) string { s, _ := mustPointer(doc, ptr).(string); return s }
		add("Environment", text("/environment"), false)
		add("Repository", text("/repo"), true)
		add("Workflow", text("/workflow"), true)
		add("Ref", text("/ref"), true)
		if m, ok := mustPointer(doc, "/inputs").(map[string]any); ok && len(m) > 0 {
			lines := make([]string, 0, len(m))
			for _, k := range slices.Sorted(maps.Keys(m)) {
				v, _ := pointerText(m, "/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
				lines = append(lines, k+"="+v)
			}
			add("Inputs", strings.Join(lines, "\n"), true)
		}
	case core.ActMCP:
		add("MCP server", a.MCPServer, true)
		add("MCP tool", a.MCPTool, true)
		add("Recipient", a.Recipient, false)
		u, _ := punycodeURL(a.URL)
		add("URL", u, true)
		c.Code = visible(prettyPayload(a.Payload))
	default:
		c.Code = visible(prettyPayload(a.Payload))
	}
	if len(a.Domains) > 0 {
		hosts := make([]string, 0, len(a.Domains))
		for _, d := range a.Domains {
			if hp, ok := splitHostPort(d); ok {
				hosts = append(hosts, hp.String())
			} else {
				hosts = append(hosts, d)
			}
		}
		add("Domains", strings.Join(hosts, "\n"), true)
	}
	if ap := r.Decision.Approver; ap.Multi() {
		add("Approvals needed", fmt.Sprintf("%d, not counting whoever asked", ap.Needed()), false)
	}
	c.Diff = visible(capDiff(a.Diff))
	return c
}

// completeDiff reports whether a tool's unified diff shows every changed
// line: it has hunks and neither the tool (64 KB) nor the card cut it. A
// path with a line break could forge hunk lines in the diff header.
func completeDiff(d string, paths []string) bool {
	for _, p := range paths {
		if strings.ContainsAny(p, "\r\n") || strings.ContainsFunc(p, hidden) {
			return false
		}
	}
	return len(d) <= maxCardDiff && strings.Contains(d, "\n@@ -") && !strings.Contains(d, "diff truncated")
}

// visible replaces control, format (bidi, zero-width), separator and filler
// characters with a visible ⟨U+XXXX⟩ token, keeping newlines and tabs.
func visible(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if !strings.ContainsFunc(s, hidden) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if hidden(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hidden(r rune) bool {
	switch r {
	case '\n', '\t':
		return false
	case '\u2028', '\u2029', '\u115F', '\u1160', '\u3164', '\uFFA0':
		return true
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// shellJoin quotes argv so the line reads back exactly as the argv it shows.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for i := 0; i < len(s); i++ {
		if !shellSafe(s[i]) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func shellSafe(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_@%+=:,./-", c) >= 0
}

// punycodeURL rewrites only the URL's host into its ASCII form, keeping the
// rest verbatim; idn reports whether the host changed.
func punycodeURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, false
	}
	start := strings.Index(raw, "://")
	if start < 0 {
		return raw, false
	}
	start += 3
	end := len(raw)
	if i := strings.IndexAny(raw[start:], "/?#"); i >= 0 {
		end = start + i
	}
	ascii := normalizeHost(u.Hostname())
	host := ascii
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	authority := host
	if p := u.Port(); p != "" {
		authority = net.JoinHostPort(strings.Trim(host, "[]"), p)
	}
	if at := strings.LastIndex(raw[start:end], "@"); at >= 0 {
		authority = raw[start:start+at+1] + authority
	}
	out := raw[:start] + authority + raw[end:]
	return out, ascii != strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

func prettyPayload(p json.RawMessage) string {
	if len(bytes.TrimSpace(p)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, p, "", "  "); err != nil {
		return string(p)
	}
	return buf.String()
}

// capDiff keeps diffs at or under 64 KB, cutting at a line boundary.
func capDiff(d string) string {
	if len(d) <= maxCardDiff {
		return d
	}
	const marker = "\n… diff truncated …\n"
	cut := d[:maxCardDiff-len(marker)]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}
