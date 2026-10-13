# openai

`core.Provider` for Chat Completions, used for kind `openai` and every
`openai_compat` server (LM Studio, vLLM, OpenRouter, Groq, xAI, DeepSeek…).
Requests stream with `stream_options.include_usage`, send
`max_completion_tokens` to OpenAI and `max_tokens` to compatible servers, put
the key in `Authorization: Bearer` (omitted when none is configured, as for
local servers), and use the plain string content form unless an image needs
parts. The reader assembles tool-call deltas by index (synthesizing
`call_<n>` ids, telling apart calls that all claim index 0 by id, accepting
whole arguments in one delta or as a JSON object; on `finish_reason:
"length"` a call with cut-off arguments is dropped), reads reasoning from
`reasoning_content` or else `reasoning` (reasoning that arrived as
`reasoning_content` goes back under that name to the same provider, which
DeepSeek's thinking mode requires in tool loops), extracts `<tool_call>{…}</tool_call>`
text from `openai_compat` servers when the request offered tools (OpenAI
itself calls tools natively, so there the tag is left as prose), keeps the
last of repeated cumulative
usage frames, normalises cached and cache-write tokens out of `Input` and
reasoning reported beside `completion_tokens` (xAI) into `Output`, marks
missing usage for estimation, and maps error bodies (including the looser
shapes of compatible servers and in-stream `error` chunks) to
`core.ErrorKind`, an exhausted `insufficient_quota` account as `auth`. `Discover` lists `GET /models` with OpenRouter's
`context_length` or vLLM's `max_model_len`. Fixtures: `testdata/README.md`.
