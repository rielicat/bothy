# internal/channel

The channel router (`core.ChannelHub`, owner M9). `channel.Hub` runs the chat
adapters cmd/bothy hands it, starting, stopping and restarting them as
`channels.*` config changes (hot reload). Every inbound message goes through
the SPEC §5.1 pipeline: dedup, identity, the unknown-sender flow (pairing via
`/pair` or `/start CODE`, one notice a day, never a model call; unknown
senders are rate-limited silently, and admins get at most three DMs an hour
about them while the web banner counts every one), space gating and provenance, then the
sender rate limit (after gating, so the bot never answers in a group it must
stay silent in), the command fast path, attachment download into
`<work>/ws/<workspace>/.bothy/in/<msgid>/`, then `Runner.Submit`.
`StreamTo` streams a run back to a chat (typing, a "Thinking…" placeholder
with a Stop button, edits no faster than the channel's `EditMinInterval`,
splits at `MaxLen` without breaking code fences, redaction always (model
text enters the reply only redacted and once no later delta can complete a
secret in it, so neither a live edit nor a split shows part of one), status
line included (in plain words: "Deleting `notes/a.md`…", "Refining the
answer…"), and link neutralization once the session is Exposed,
re-checked after every tool result: scheme URLs (IPv6 hosts included),
markdown and autolinks, bare domains and IPv4 addresses, also when only
rendering joins them, as in `evil\.com` or `evil**.**com`; the rules live in
`core.NeutralizeMarkdown`, shared with the agent's sink; the first
neutralized reply of a conversation says why, once). In `final` mode
(schedule delivery) the reply opens with a line naming the schedule or
check-in that sent it and where to change it. Forwarded messages
(`Inbound.Forwarded`) are untrusted whoever forwards them and never run as
commands; a lone unknown `/word` gets a short reply instead of a run;
`/compact` runs in the background and replies when done, and
`/refine on|off|default` sets the conversation's refine mode (the session's
meta key `agent.refine.<session>`, which the runner reads). The hub
delivers approval cards to the
approver's DM (never a group), truncating cards that do not fit to Deny plus
a web link, and edits them to the outcome. Cards speak the policy module's
plain copy (question, consequence, warnings, the verb on the primary button,
danger style only for what can't be undone; a built-in tool's action JSON
stays in the web UI) and become their decided line in place of the question,
naming their reader "You" ("✓ You approved deleting notes/a.md.", "✗ Sam
Patel denied deleting notes/a.md in Slack."). A multi-approver card
(deploys) goes to every eligible approver's DM but never the requester's,
shows who asked, "1 of 2 approvals", every vote and who may still approve,
is edited after each `approval.voted`, and refuses the requester's allow
press with a toast; the originating chat gets a waiting line. A pending
sender's first words (their first message that is not a command such as
Telegram's "/start") are kept for the admins' inbox, and new senders and
group chats publish `inbox.changed`.
It also implements `Messenger`, `PreferredChat`, `DeepLink`, `Test` and the
doctor check `channels.auth_failed`.

Contracts adapters can opt into (by method set; adapters never import this
package):

- `Reconfigure(*config.Config) error` (`channel.Reconfigurer`): receives the
  new snapshot when the adapter's `channels.<name>` section changes, before
  the hub restarts its `Run`. Without it a restart reuses the boot config.

A newly sighted group becomes a disabled space named by `Inbound.ChatTitle`
and owned by the linked user `Inbound.AddedBy` names (the platform's "added
by" or owner), otherwise by the instance owner. A group `Inbound` with
`AddedBy` and no text or attachments only announces the group.

Markdown the hub sends uses CommonMark backslash escapes for punctuation and
backtick fences or spans longer than any backtick run they contain, so system
text (cards, notices, names) renders verbatim; adapters must honour both.

A panic in another service while the hub handles one message, press, card or
stream is logged with its stack and contained to that piece of work; a
panicking adapter `Run` is restarted with backoff like any other exit.
