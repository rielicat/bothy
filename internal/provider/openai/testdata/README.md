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
  written in the documented chunk shape.

Faults (429 with `Retry-After`, 500, context overflow, mid-stream drop,
malformed frame, missing usage) come from `internal/testkit/mockllm`.
