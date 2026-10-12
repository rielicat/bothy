package discord

import (
	"net/url"
	"strconv"
)

// InvitePermissions is what the invite asks for (SPEC §8.6): View Channels,
// Send Messages, Read Message History and Send Messages in Threads.
const InvitePermissions = 1024 | 2048 | 65536 | 274877906944

// InviteURL returns the OAuth2 URL that adds the bot to a server, in the
// exact form of the SPEC §8.6 template.
func InviteURL(applicationID string) string {
	return "https://discord.com/oauth2/authorize?client_id=" + url.QueryEscape(applicationID) +
		"&scope=bot%20applications.commands&permissions=" + strconv.FormatInt(InvitePermissions, 10)
}
