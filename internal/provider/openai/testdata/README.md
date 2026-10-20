# OpenAI fixtures

Copied byte for byte unless noted. Fetched 2026-10-12.

| File | Source |
|---|---|
| `sdk_text.sse` | [openai-python](https://github.com/openai/openai-python) `.inline-snapshot/external/e2aad469b71d1d4894ff833ea147020a9d875eb7ce644a0ff355581690a4cbfd.bin`, recorded from the live API for `tests/lib/chat/test_completions_streaming.py::test_parse_nothing` |
| `sdk_parallel_tools.sse` | same directory, `f82268f2fefd5cfbc7eeb59c297688be2f6ca0849a6e4f17851b517310841d9b.bin` (`test_parse_multiple_pydantic_tools`: two tool calls, `include_usage`) |
| `sdk_max_tokens.sse` | same directory, `4cc50a6135d254573a502310e6af1246f55edb6ad95fa24059f160996b68866d.bin` (`test_parse_max_tokens_reached`: `finish_reason: "length"`) |
| `docs_stream.sse` | [openai-openapi](https://github.com/openai/openai-openapi) `openapi.yaml`, Create chat completion, "Streaming" example response, with the YAML indentation removed (it includes an SSE comment line) |

Inline in `openai_test.go`:

- the usage object with `cached_tokens` from xAI's
  [Prompt caching: usage & pricing](https://docs.x.ai/developers/advanced-api-usage/prompt-caching/usage-and-pricing.md);
- `reasoning_content` chunks from xAI's
  [streaming](https://docs.x.ai/developers/model-capabilities/text/streaming.md)
  example with its `<completion_id>`/`<creation_time>` placeholders filled in,
  and vLLM's newer `reasoning` field name
  ([reasoning outputs](https://github.com/vllm-project/vllm/blob/main/docs/features/reasoning_outputs.md));
- the `<tool_call>{"name": …, "arguments": …}</tool_call>` text form of the
  Qwen/Hermes chat templates;
- error bodies in the documented `{"error":{"message","type","param","code"}}`
  shape with the codes of the OpenAI
  [error-code guide](https://developers.openai.com/api/docs/guides/error-codes),
  plus the looser bodies compatible servers return;
- quirk chunks (no call id, every call on index 0, arguments as an object)
  written in the documented chunk shape;
- in `cache_test.go`, an OpenRouter stream for an Anthropic model, written
  (not recorded) in its chunk shape with the usage fields of OpenRouter's
  [prompt caching](https://openrouter.ai/docs/features/prompt-caching) page
  (`prompt_tokens_details.cached_tokens` 10318, as in its example, and
  `cache_write_tokens`); the expected `cache_control` request shape follows
  the same page (fetched 2026-10-20).

Faults (429 with `Retry-After`, 500, context overflow, mid-stream drop,
malformed frame, missing usage) come from `internal/testkit/mockllm`.

## Responses API

Fetched 2026-10-14. "Spec" is [openai-openapi](https://github.com/openai/openai-openapi)
`openapi.json` at commit `803527048c4b4d059cee8129eac9630f2130bc75` (API
version 2.3.0); its examples also appear on the
[create](https://developers.openai.com/api/reference/resources/responses/methods/create)
and [streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)
reference pages. The tests frame pretty-printed JSON as one compacted `data:`
line and close streams that have no terminal event as noted.

| File | Source |
|---|---|
| `spec_stream.sse` | spec, `POST /responses` `x-oaiMeta` "Streaming" example response, byte for byte (`event:` lines and an SSE comment line, no `[DONE]`) |
| `spec_functions.json` | spec, "Functions" example response (non-streaming; the tests wrap it in a `response.completed` event) |
| `spec_reasoning.json` | spec, "Reasoning" example response (usage with 832 reasoning tokens inside `output_tokens`) |
| `spec_incomplete.json`, `spec_failed.json`, `spec_error.json` | spec, `x-oaiMeta.example` of `ResponseIncompleteEvent`, `ResponseFailedEvent` and `ResponseErrorEvent` |
| `spec_summary_events.jsonl` | spec, the examples of the four `ResponseReasoningSummary*Event` schemas (part added, text delta, text done, part done), one compacted event per line |
| `docs_function_call_events.jsonl` | [function calling guide](https://developers.openai.com/api/docs/guides/function-calling), "Streaming" output events, verbatim (they carry `response_id` and no `sequence_number`; the test closes them with `spec_functions.json`) |
| `docs_parallel_calls.json` | same guide, "Sample response with multiple function calls" (the test wraps the three calls and `codex_usage.json` in a `response.completed` event, so no `output_item` events are sent) |
| `sdk_reasoning_stream.sse` | [openai-node](https://github.com/openai/openai-node) `tests/lib/__snapshots__/ResponseStream.test.ts.snap` at `0bf85e01`, "reasoning works": raw `reasoning_text`, no `output_item.done`, trailing `[DONE]` |
| `codex_failed_events.jsonl` | [openai/codex](https://github.com/openai/codex) `codex-rs/codex-api/src/sse/responses.rs` at `c22a06c1`, the `response.failed` test fixtures for a rate limit, context window, quota, cyber policy and misalignment policy, verbatim |
| `codex_usage.json` | same file, `parses_cache_write_token_usage`: usage with `cached_tokens` and `cache_write_tokens` (Rust indentation removed) |
| `cookbook_reasoning_tool.sse` | **Derived.** The items are the [openai-cookbook](https://github.com/openai/openai-cookbook) `examples/responses_api/reasoning_items.ipynb` outputs transcribed from their Python reprs: the o3 reasoning item with its `encrypted_content` (cell 19) and the o4-mini `get_weather` call (cell 11). They are framed in the spec's `output_item.added`/`function_call_arguments.*`/`output_item.done`/`response.completed` event shapes (the arguments split in two deltas), with the usage of `spec_reasoning.json`. |
| `cc_tools_reasoning_error.json` | The Chat Completions refusal of function tools with reasoning on: the message is verbatim from the live error a user saw for `gpt-6-astra` (Azure documents the same sentence for `gpt-5.6-sol`); the body is OpenAI's documented error shape, with `type`, `param` and `code` assumed, since no source shows them. |

`responses_test.go` also writes the reasoning-summary stream's
`output_item.added`/`.done` envelope around `spec_summary_events.jsonl`, and
`effort_test.go` the second refusal (`'none'` not supported), whose wording
is unverified.
