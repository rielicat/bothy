# anthropic

`core.Provider` for the Anthropic Messages API: `POST /v1/messages` with
`stream: true`, the key in `x-api-key` and `anthropic-version: 2023-06-01`.
Requests map system blocks, tools and messages (tool results become user
`tool_result` blocks placed first, consecutive same-role messages merge) and
add `cache_control` breakpoints from `SystemBlock.CacheBreak`, `ToolsCache` and
`CacheTail`, dropping the earliest system ones beyond the API's limit of four.
The SSE reader assembles text, thinking with its signature (and
`redacted_thinking`, kept as an opaque signature) and tool calls by block
index, takes usage from `message_start` and the cumulative `message_delta`
(cache reads and writes reported separately, so `Input` already excludes
them), maps `error` events and HTTP errors to `core.ErrorKind` (a 400 about
an exhausted credit balance is `auth`, like `billing_error`), drops tool
calls cut off by `max_tokens`, and treats a stream that ends before
`message_stop` as a network failure. Thinking parts go back verbatim only to
Anthropic. `Discover` lists `GET /v1/models`. Fixtures: `testdata/README.md`.
