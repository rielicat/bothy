# internal/channel/telegram

The Telegram adapter (`core.Channel`, owner M9), a hand-written Bot API
client with no third-party dependency. `Run` long-polls `getUpdates` (offset
in meta key `channel.telegram.offset`, so a restart neither replays nor skips
updates), reconnects with exponential backoff, waits out 429 `retry_after`,
and reports `auth_failed` when Telegram rejects the token. Messages are sent
and edited in HTML parse mode from the router's markdown subset, with every
other character escaped and `link_preview_options.is_disabled` always set;
buttons become inline keyboards (callback data at most 64 bytes, loopback
link buttons dropped). It also implements typing (`sendChatAction`), button
acknowledgement (`answerCallbackQuery`), attachment download (`getFile`,
size-capped), `setMyCommands`, `getMe` self-test and
`https://t.me/<bot>?start=<code>` deep links. The bot token comes from the
vault via `AdapterDeps.Secret`, is only ever placed in the request path, and
is scrubbed from transport errors. The HTTP client is the egress system
client for exactly the `api_base` host; a non-default (self-hosted or test)
`api_base` may be a private address. `telegramtest` is the fake Bot API
server used by the conformance suite, hub tests and E2E.
