# internal/channel

The channel router (`core.ChannelHub`, owner M9). `channel.Hub` runs the chat
adapters cmd/bothy hands it, starting, stopping and restarting them as
`channels.*` config changes (hot reload). Every inbound message goes through
the SPEC §5.1 pipeline: dedup, identity, the sender limit (before any
lookup), membership, space gating and provenance, the command fast path,
attachment download into `<work>/ws/<workspace>/.bothy/in/<msgid>/`, then
`Runner.Submit`. People are never linked to accounts: a chat app that
implements `core.MemberChecker` (Slack, Discord) says who is in the team, and
on Telegram an admin lets people in. Verdicts younger than an hour are used as
they are; older ones are asked again off the dispatcher, in order per sender,
through a per-adapter limiter (30 a minute, 4 in flight); when the platform
doesn't answer, a member verdict under four hours still counts. A change of
what membership means (another Slack workspace, the set of Discord servers
with a channel on) makes every verdict stale (`member_scope:<channel>` in
meta), and a sweep every 15 minutes renews approvers and teammates with
schedules. Someone who left loses access: their runs stop, their chats stay.
Outsiders get one line a day in DMs and silence in channels; people waiting
for an admin get one line a day and approvers a DM (at most three an hour,
none spent without an approver), never a model call. Teammates are greeted
once (`Greet`: hello, or "moved" for accounts linked before 0013).
`StreamTo` streams a run back to a chat (typing, a "Working…" placeholder
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
meta key `agent.refine.<session>`, which the runner reads). Web accounts'
own approvals stay on the web. A channel's or teammate's request goes as a
card to every chat approver's DM (a teammate an admin marked Can approve;
one whose verdict is over two hours old gets a card that only links to
Bothy, as do admin-only requests), never to a group; the chat that asked gets
a recorded waiting line with Cancel request, which only the person who asked
can press, edited to the outcome (a group never sees the approver's reason).
Cards that do not fit are truncated to Deny plus a web link, and every
message is edited to the outcome. Cards speak the policy module's
plain copy (question, consequence, warnings, the verb on the primary button,
danger style only for what can't be undone; a built-in tool's action JSON
stays in the web UI) and become their decided line in place of the question,
naming their reader "You" ("✓ You approved deleting notes/a.md.", "✗ Sam
Patel denied deleting notes/a.md in Slack."). A team card shows who asked as
the platform's own mention, the chat's owner as context, and grant buttons
naming the teammate ("Allow in Maya's chat"); a channel's card has none.
Presses are checked again against fresh membership (a 15-minute fallback when
the recheck fails). Multi-approver votes (deploys) are cast on the web; cards
sent before 0013 are still refreshed after each `approval.voted`. A pending
sender's first words (their first message that is not a command such as
Telegram's "/start") are kept for the admins' inbox, and people waiting and
group chats publish `inbox.changed`.
It also implements `Messenger`, `PrincipalChat` (a teammate's DM while their
verdict is fresh enough, a channel's chat while it is on; never a web
account), `TeamActive` (whether a team schedule may run now), `Greet`,
`TellApprover`, `InvalidateTeams`, `Test` and the doctor check
`channels.auth_failed`.

Contracts adapters can opt into (by method set; adapters never import this
package):

- `Reconfigure(*config.Config) error` (`channel.Reconfigurer`): receives the
  new snapshot when the adapter's `channels.<name>` section changes, before
  the hub restarts its `Run`. Without it a restart reuses the boot config, so
  a chat app turned on in Settings would stay off; every built-in adapter
  implements it (`TestBuiltInAdaptersFollowConfig`).

A newly sighted group becomes a disabled space that belongs to the team,
owned by nobody, named by `Inbound.ChatTitle` (or "<App> group <chat id>"
when the platform gave none); `InspectChat` fills in its server, member count
and Slack Connect sharing. Admins get a banner and an inbox item, chat
approvers a DM (at most three an hour). A space still under that fallback
name takes the first title a later message carries, its principal too; a
title anyone gave it is never overwritten. A group `Inbound` with `AddedBy`
and no text or attachments only announces the group. On Slack and Discord
only admitted teammates are answered in a channel; on Telegram anyone not
blocked may talk, as untrusted. In a channel another sender's message never
steers a running task.

An `Inbound.Private` message in a group (a Slack slash command, which only
its sender saw and which works in channels the bot is not in) never creates
a space. In a group without an enabled space it is answered in the sender's
DM (the adapter's `OpenDM`, remembered as the identity's DM chat): a teammate
is told Bothy is off there, at most once an hour per channel; anyone else
gets what a DM would. In an enabled space it goes on as any message.

Commands: `/stop`, `/status`, `/help`, `/pins` and `/usage` work for anyone
Bothy answers in the chat; `/new`, `/pin`, `/unpin`, `/compact`, `/model` and
`/refine` need the DM's teammate or, in a channel, someone who can approve;
`/stopall` is for approvers in any chat (resuming happens on the web, which
`/resume` says); `/pair` says link codes are gone.

Markdown the hub sends uses CommonMark backslash escapes for punctuation and
backtick fences or spans longer than any backtick run they contain, so system
text (cards, notices, names) renders verbatim; adapters must honour both.

A panic in another service while the hub handles one message, press, card or
stream is logged with its stack and contained to that piece of work; a
panicking adapter `Run` is restarted with backoff like any other exit.
