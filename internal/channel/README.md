# internal/channel

The channel router (`core.ChannelHub`, owner M9). `channel.Hub` runs the chat
adapters cmd/bothy hands it, starting, stopping and restarting them as
`channels.*` config changes (hot reload). Every inbound message goes through
the SPEC §5.1 pipeline: dedup, identity, the unknown-sender flow (pairing via
`/pair` or `/start CODE`, one notice a day, never a model call), the sender
rate limit, space gating and provenance, the command fast path, attachment
download into `<work>/ws/<workspace>/.bothy/in/<msgid>/`, then
`Runner.Submit`. `StreamTo` streams a run back to a chat (typing, a
"Thinking…" placeholder with a Stop button, edits no faster than the
channel's `EditMinInterval`, splits at `MaxLen` without breaking code fences,
redaction always, link neutralization when the session is Exposed). The hub
delivers approval cards to the approver's DM (never a group), truncating
cards that do not fit to Deny plus a web link, and edits them to the outcome.
It also implements `Messenger`, `PreferredChat`, `DeepLink`, `Test` and the
doctor check `channels.auth_failed`.

Contracts adapters can opt into (by method set; adapters never import this
package):

- `Reconfigure(*config.Config) error` (`channel.Reconfigurer`): receives the
  new snapshot when the adapter's `channels.<name>` section changes, before
  the hub restarts its `Run`. Without it a restart reuses the boot config.
- `ChatTitle(chatID string) string` (`channel.ChatTitler`): names a newly
  sighted group space.
- An `Inbound` in a group with no text and no attachments is a presence
  signal (the bot was added, by `SenderID`): it only records the group as a
  disabled space owned by that user, if linked.

Markdown the hub sends uses CommonMark backslash escapes for punctuation and
backtick fences or spans longer than any backtick run they contain, so system
text (cards, notices, names) renders verbatim; adapters must honour both.
