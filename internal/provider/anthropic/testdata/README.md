# Anthropic fixtures

Copied byte for byte unless noted. Fetched 2026-10-12.

| File | Source |
|---|---|
| `sdk_basic.sse` | [anthropic-sdk-python](https://github.com/anthropics/anthropic-sdk-python) `tests/lib/streaming/fixtures/basic_response.txt` (fixtures last changed in `b132569c`) |
| `sdk_tool_use.sse` | same directory, `tool_use_response.txt` |
| `sdk_usage_cache.sse` | same directory, `message_delta_fields_response.txt` (cache read/write and cumulative usage in `message_delta`) |
| `sdk_refusal.sse` | same directory, `refusal_response.txt` |
| `sdk_incomplete_tool_json.sse` | same directory, `incomplete_partial_json_response.txt` (`max_tokens` inside a tool call) |
| `docs_basic.sse` | [Streaming messages](https://platform.claude.com/docs/en/build-with-claude/streaming), "Basic streaming request" response |
| `docs_tool_use.sse` | same page, "Streaming request with tool use" response |
| `docs_thinking.sse` | same page, "Streaming request with thinking" response (thinking deltas and `signature_delta`; the documented trace carries no usage) |
| `docs_error.sse` | same page, "Error events" example |
| `composed_midstream_error.sse` | **composed**: the first three events of `docs_basic.sse` followed by the event of `docs_error.sse`, all unchanged. No published stream shows an error after content. |
| `composed_parallel_tool_use.sse` | **composed**: `docs_tool_use.sse` with the `tool_use` block of `sdk_tool_use.sse` inserted before `message_delta`; only that block's `"index"` changes (1 → 2). No published stream carries two client tool calls. |

The error-kind table in `anthropic_test.go` uses the documented error shape
from [Claude API errors](https://platform.claude.com/docs/en/api/errors) with
each documented status and `type`.
