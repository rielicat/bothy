# internal/channel/discord

The Discord `core.Channel` (SPEC §4.6, §12.2 M10), built on disgo's gateway
and REST client with every disgo cache off. The gateway identifies with
`GUILDS | GUILD_MESSAGES | DIRECT_MESSAGES | MESSAGE_CONTENT`, uncompressed, and disgo
resumes dropped sessions itself; on shutdown the adapter closes with a
resumable code and stores the session cursor in Meta
(`channel.discord.resume`) so a restart resumes too. Every connection, the
WebSocket and its resume URLs included, goes through the egress system dialer
for that exact `host:port`; only an overridden `api_base` or `gateway_url` host
may resolve to a private address, never a host the server hands out.
Messages are sent with `SUPPRESS_EMBEDS` and all mention parsing off, Bothy
markdown is escaped for Discord's dialect (only where it would form markup,
since escapes count against the 2000-character limit), threads
arrive as `ThreadID` under their parent channel, and button presses get a
deferred-update response before the hub sees them, so the 3 s deadline holds;
`Ack` then sends the toast as an ephemeral followup. Interaction tokens are
masked in disgo's log output and kept out of errors. 429s are retried per
route by disgo's rate limiter, one per API base for the whole process:
disgo never stops a limiter's goroutine, and the hub rebuilds the adapter
on every config change. A 401 or close code 4004 reports
`auth_failed`; 4014 reports it with the Message Content intent hint.
`InviteURL` builds the §8.6 invite link. `core.PictureSetter`: `GET
/users/@me` says whether the bot has an avatar, and `PATCH /users/@me` sets
one from a PNG data URI in a request of its own, outside disgo's limiter,
which would wait out the avatar bucket for up to an hour; Discord's 429 and
its 400 `AVATAR_RATE_LIMIT` both come back at once with the FailureCode
`rate_limited`.

Discord says who belongs to the team (`core.MemberChecker`): someone is a
member of a server where Bothy is on in a channel (the hub passes those
guild ids; at most 10 are asked) when the single-member lookup finds them,
not a bot and past the server's screening. Not found in a server Bothy can
read means outsider; with no server, or none letting the bot look (403 or
code 50001 everywhere), Discord can't tell (`unknown`, and the status detail
says "can't check who is in the server", logged at most hourly), and an
admin lets people in. `MemberScope` is empty: the hub adds the servers.
`GUILD_CREATE`, `GUILD_UPDATE` and `GUILD_DELETE` keep the list of servers
the bot is in (`Teams()`); guild messages carry their guild id in
`Inbound.Team`. `InspectChat` reads the channel's server and its approximate
member count.

`discordtest` is a fake Discord (REST plus a WebSocket gateway with resume)
implementing `conformance.PlatformFake`, shared with the E2E suite
(`AddGuild`, `RemoveGuild`, `SetMember`, `SetChannelGuild`, and the bot's
avatar: `Avatar`, `SetAvatar`, `SlowAvatarChanges`). disgo
v0.19.6's gateway has data races of its own, so tests that drive it over
WebSocket are built only without `-race`; under `-race` the same suites run
through the adapter's `newGateway` seam with an in-process feed from the fake.
Run both: `go test ./internal/channel/discord/...` and
`CGO_ENABLED=1 go test -race ./internal/channel/discord/...`.
