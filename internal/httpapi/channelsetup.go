package httpapi

import (
	"bufio"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
