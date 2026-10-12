# internal/channel/slack

The Slack `core.Channel` (SPEC §4.6, §12.2 M10), built on slack-go's Web API
client and `socketmode`. Run checks the bot token with `auth.test`, then holds
a Socket Mode connection opened with the app-level token
(`apps.connections.open`); slack-go reconnects on Slack's `disconnect`
request and on network errors. Every envelope is acknowledged as soon as its
event is queued for the hub, well inside Slack's 3 s, and one that cannot be
queued is left unacknowledged for Slack to redeliver. `app_mention` and
`message.im` / `message.channels` / `message.groups` become `Inbound` (thread
`ts` as `ThreadID`, `MsgID` `"<channel>:<ts>"`, so the duplicate copies of a
mention dedup in the hub), `/bothy stop` becomes `/stop`, and `block_actions`
presses become `ActionEvent`s whose `AckRef` is the `response_url`; `Ack`
posts the toast there as an ephemeral reply. Messages go out with
`unfurl_links` and `unfurl_media` false, Bothy markdown converted to mrkdwn
with `&`, `<` and `>` escaped, and buttons as Block Kit actions. All HTTP and
the WebSocket go through the egress system dialer for the exact host:port;
the bot token is sent only to the API host and `files.slack.com`.
Socket Mode has no typing indicator, so `Caps.Typing` is off.

`slacktest` is a fake Slack (Web API, Socket Mode with pings, files and
response URLs) implementing `conformance.PlatformFake`, shared with the E2E
suite. `TestManifestScopesCoverEveryCall` checks every Web API method the
adapter calls against the bot scopes of the §8.6 manifest.
