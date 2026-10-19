# internal/channel/telegram/telegramtest

A fake Telegram Bot API server (`httptest`) implementing
`conformance.PlatformFake`, for adapter and hub tests, the E2E suite (point
`channels.telegram.api_base` at `Server.URL` and store `Server.Token` as the
bot token) and the RSS budget job. It validates what Telegram does and Bothy
depends on: the token in the path, strict HTML parse mode (`Visible` rejects
unknown tags, bad nesting and unescaped `<`, `>`, `&`), the 4096-character
limit, 64-byte callback data, loopback button URLs and the bot's profile
photo (`setMyProfilePhoto` takes only a static JPEG uploaded with the
request; `getUserProfilePhotos` lists them newest first, each with its own
`file_unique_id`). Tests inject user messages,
button presses, "bot added to group" updates and redeliveries, give the bot a
photo as BotFather would (`SetProfilePhoto`), queue 429s with `RateLimit`, and
inspect what each chat shows (`SentTexts`, `Messages`), callback answers,
chat actions, registered commands and the current photo (`ProfilePhoto`).
