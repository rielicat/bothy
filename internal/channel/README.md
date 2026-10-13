# internal/channel

The channel router (`core.ChannelHub`, owner M9). `channel.Hub` runs the chat
adapters cmd/bothy hands it, starting, stopping and restarting them as
`channels.*` config changes (hot reload). Every inbound message goes through
the SPEC §5.1 pipeline: dedup, identity, the unknown-sender flow (pairing via
`/pair` or `/start CODE`, one notice a day, never a model call; unknown
senders are rate-limited silently), space gating and provenance, then the
sender rate limit (after gating, so the bot never answers in a group it must
stay silent in), the command fast path, attachment download into
`<work>/ws/<workspace>/.bothy/in/<msgid>/`, then `Runner.Submit`.
`StreamTo` streams a run back to a chat (typing, a "Thinking…" placeholder
with a Stop button, edits no faster than the channel's `EditMinInterval`,
splits at `MaxLen` without breaking code fences, redaction always, status
line included, and link neutralization once the session is Exposed,
re-checked after every tool result). The hub delivers approval cards to the
approver's DM (never a group), truncating cards that do not fit to Deny plus
a web link, and edits them to the outcome.
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
