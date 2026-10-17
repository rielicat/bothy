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
mention dedup in the hub; a reply in a thread the bot started counts as a
mention), `/bothy stop` becomes `/stop` (a slash command is `Private`: only
its sender saw it, and it works in channels the app is not in), and
`block_actions` presses become
`ActionEvent`s whose `AckRef` is the `response_url` and whose `Msg` matches
the `MsgRef` `Send` returned (a message with thread replies stays in its
channel); `Ack` posts the toast there as an ephemeral reply, and a failed
`Ack` never puts the `response_url` in its error. Messages go out with
`unfurl_links` and `unfurl_media` false, Bothy markdown converted to mrkdwn
with `&`, `<` and `>` escaped, and buttons as Block Kit actions. All HTTP and
the WebSocket go through the egress system dialer for the exact host:port
(only an overridden `api_base` host may resolve to a private address); the
bot token is sent only to the API host and `files.slack.com`.
Socket Mode has no typing indicator, so `Caps.Typing` is off.

Senders are named through `users.info` and channels through
`conversations.info`, both cached (an hour, a minute after a failure, 512
entries, 5 s per lookup): a channel message's `ChatTitle` is `"#name"`, and a
slash command's comes from its `channel_name` unless that is a placeholder
(`directmessage`, `privategroup`) or a group DM's `mpdm-…`. Reading channel
names needs `channels:read` and `groups:read`; an app created before the
manifest had them gets `missing_scope` until it is reinstalled to the
workspace (Slack: OAuth & Permissions → Reinstall to Workspace), which leaves
`ChatTitle` empty, so spaces keep names like "Slack group C0123", and logs one
warning an hour.

Slack decides who can use Bothy (`core.MemberChecker`): a full member of the
workspace the bot token belongs to, or of any workspace of the same
Enterprise Grid org, is a member; guests, Slack Connect strangers,
deactivated accounts, bots, workflow bots and Slackbot are outsiders
(`user_not_found` too). Before `auth.test` answered, a check is an error, not
a verdict, and so is any other API error. Verdicts are cached five minutes,
apart from the hour-long name cache; one `users.info` call fills both when
they race. `MemberScope` is `team/enterprise`, and `Status()` reports the
workspace name (`Team`), whether it is an org install (`Org`) and the app's
id from the Socket Mode hello (`AppID`), for links to the app's settings,
where only a person can change its icon: Slack has no `core.PictureSetter`. Messages
from other apps and workflows (`bot_id`, `bot_profile`) are dropped.
`InspectChat` reads `conversations.info` with the member count and whether
the channel is shared with another company.

`slacktest` is a fake Slack (Web API, Socket Mode with pings, files and
response URLs) implementing `conformance.PlatformFake`, shared with the E2E
suite; `SetUserFlags` and `SetChannelInfo` set what `users.info` and
`conversations.info` report. `TestManifestScopesCoverEveryCall` checks every Web API method the
adapter calls against the bot scopes of the manifest the server serves
(httpapi's golden copy of SPEC §8.6).
