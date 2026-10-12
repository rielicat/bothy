# internal/channel/discord

The Discord `core.Channel` (SPEC §4.6, §12.2 M10), built on disgo's gateway
and REST client with every disgo cache off. The gateway identifies with
`GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT`, uncompressed, and disgo
resumes dropped sessions itself; on shutdown the adapter closes with a
resumable code and stores the session cursor in Meta
(`channel.discord.resume`) so a restart resumes too. Every connection, the
WebSocket and its resume URLs included, goes through the egress system dialer
for that exact `host:port`. Messages are sent with `SUPPRESS_EMBEDS` and all
mention parsing off, Bothy markdown is escaped for Discord's dialect, threads
arrive as `ThreadID` under their parent channel, and button presses get a
deferred-update response before the hub sees them, so the 3 s deadline holds;
`Ack` then sends the toast as an ephemeral followup. 429s are retried per
route by disgo's rate limiter. A 401 or close code 4004 reports
`auth_failed`; 4014 reports it with the Message Content intent hint.
`InviteURL` builds the §8.6 invite link.

`discordtest` is a fake Discord (REST plus a WebSocket gateway with resume)
implementing `conformance.PlatformFake`, shared with the E2E suite. disgo
v0.19.6's gateway has data races of its own, so tests that drive it over
WebSocket are built only without `-race`; under `-race` the same suites run
through the adapter's `newGateway` seam with an in-process feed from the fake.
Run both: `go test ./internal/channel/discord/...` and
`CGO_ENABLED=1 go test -race ./internal/channel/discord/...`.
