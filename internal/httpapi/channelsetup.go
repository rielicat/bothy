package httpapi

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/httpapi/dto"
)

type configMCP = config.MCPServer

// slackManifestTmpl is SPEC §8.6 verbatim; {{name}} is the agent name.
const slackManifestTmpl = `display_information:
  name: "{{name}}"
  description: Self-hosted assistant (Bothy)
features:
  bot_user: {display_name: "{{name}}", always_online: true}
  slash_commands:
    - {command: /bothy, description: "Talk to {{name}} (stop, new, status, help)", should_escape: false}
oauth_config:
  scopes:
    bot: [app_mentions:read, channels:history, groups:history, im:history, im:read, im:write,
          chat:write, commands, users:read, files:read, reactions:write]
settings:
  event_subscriptions:
    bot_events: [app_mention, message.im, message.channels, message.groups]   # channel messages only matter for spaces with mention_only off; duplicates of app_mention are dropped by inbound dedup (same ts)
  interactivity: {is_enabled: true}
  org_deploy_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
`

// discordPermissions = View Channels 1024 + Send Messages 2048 + Read Message
// History 65536 + Send Messages in Threads 274877906944.
const discordPermissions = 274877975552

const maxAgentName = 35 // Slack's limit for app and bot display names

// agentName reads the first Markdown heading (or a "Name:" line) of SOUL.md.
func agentName(r io.Reader) string {
	sc := bufio.NewScanner(io.LimitReader(r, maxPersona))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		var name string
		switch {
		case strings.HasPrefix(line, "# "):
			name = strings.TrimSpace(line[2:])
		case len(line) > 5 && strings.EqualFold(line[:5], "name:"):
			name = strings.TrimSpace(line[5:])
		default:
			continue
		}
		if name = cleanName(name); name != "" {
			return name
		}
	}
	return "Bothy"
}

// cleanName keeps the name safe inside a double-quoted YAML scalar.
func cleanName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" -_.'", r) {
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	for utf8.RuneCountInString(out) > maxAgentName {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return strings.TrimSpace(out)
}

func slackManifest(name string) string {
	return strings.ReplaceAll(slackManifestTmpl, "{{name}}", name)
}

func (s *Server) slackManifest(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	name := "Bothy"
	if root, file, err := s.personaRoot(); err == nil {
		defer root.Close()
		if f, err := root.Open(file); err == nil {
			name = agentName(f)
			f.Close()
		}
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = io.WriteString(w, slackManifest(name))
	return nil
}

func discordInviteURL(appID string) string {
	return "https://discord.com/oauth2/authorize?client_id=" + url.QueryEscape(appID) +
		"&scope=bot%20applications.commands&permissions=" + strconv.FormatInt(discordPermissions, 10)
}

func (s *Server) discordInvite(w http.ResponseWriter, _ *http.Request, _ core.Actor) error {
	id := strings.TrimSpace(s.cfg().Channels.Discord.ApplicationID)
	if id == "" {
		return newError(http.StatusConflict, "conflict", "set channels.discord.application_id first").withFix("/settings/channels")
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return badRequest("channels.discord.application_id must be the numeric application id")
	}
	return ok(w, dto.DiscordInvite{URL: discordInviteURL(id)})
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// describeSchedule renders common cron shapes in plain words ("Weekdays at
// 08:00 (Europe/London)"); anything else falls back to the raw expression.
func describeSchedule(kind core.ScheduleKind, spec, tz string) string {
	if tz == "" {
		tz = "UTC"
	}
	switch kind {
	case core.SchedHeartbeat:
		if d, err := time.ParseDuration(spec); err == nil && d > 0 {
			return "Every " + humanDuration(d) + " (" + tz + ")"
		}
	case core.SchedOnce:
		if t, err := time.Parse(time.RFC3339, spec); err == nil {
			return "Once at " + t.Format("2006-01-02 15:04") + " (" + tz + ")"
		}
	case core.SchedCron:
		if d := describeCron(spec); d != "" {
			return d + " (" + tz + ")"
		}
	}
	return fmt.Sprintf("%s %s (%s)", kind, spec, tz)
}

func humanDuration(d time.Duration) string {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case d%time.Minute != 0:
		return d.String()
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}

func describeCron(spec string) string {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return ""
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	if month != "*" {
		return ""
	}
	if n, ok := strings.CutPrefix(minute, "*/"); ok && hour == "*" && dom == "*" && dow == "*" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			return fmt.Sprintf("Every %d minutes", v)
		}
	}
	m, errM := strconv.Atoi(minute)
	if errM != nil || m < 0 || m > 59 {
		return ""
	}
	if hour == "*" && dom == "*" && dow == "*" {
		return fmt.Sprintf("Every hour at :%02d", m)
	}
	h, errH := strconv.Atoi(hour)
	if errH != nil || h < 0 || h > 23 {
		return ""
	}
	at := fmt.Sprintf("%02d:%02d", h, m)
	switch {
	case dom == "*" && dow == "*":
		return "Daily at " + at
	case dom == "*" && (dow == "1-5" || dow == "MON-FRI" || dow == "mon-fri"):
		return "Weekdays at " + at
	case dom == "*" && (dow == "0,6" || dow == "6,0" || dow == "SAT,SUN" || dow == "sat,sun"):
		return "Weekends at " + at
	case dom == "*":
		if d, err := strconv.Atoi(dow); err == nil && d >= 0 && d <= 7 {
			return "Every " + weekdays[d%7] + " at " + at
		}
	case dow == "*":
		if d, err := strconv.Atoi(dom); err == nil && d >= 1 && d <= 31 {
			return fmt.Sprintf("Monthly on day %d at %s", d, at)
		}
	}
	return ""
}
