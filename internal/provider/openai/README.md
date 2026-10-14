# openai

Two `core.Provider`s. `ResolveAPI` picks one from `providers[].api`: `auto`
(or empty) is Responses for kind `openai` and Chat Completions for
`openai_compat` (LM Studio, vLLM, OpenRouter, Groq, xAI, DeepSeek…);
`responses` and `chat` force it. Both put the key in `Authorization: Bearer`
(omitted when none is configured, as for local servers), and `Discover` lists
`GET /models` with OpenRouter's `context_length` or vLLM's `max_model_len`.

**Responses** (`NewResponses`, `POST /responses`) is stateless: `store: false`,
`stream: true`, `include: ["reasoning.encrypted_content"]` (left out for
models the catalog knows do not reason), the system blocks as `instructions`,
`max_output_tokens` (at least 16), `tool_choice` auto/none/required, function
tools with `strict: false` (the API would otherwise coerce schemas to strict
mode), user parts as `input_text`/`input_image` (data URL, `detail: auto`),
tool results as `function_call_output`, and call ids longer than the API's 64
characters hashed alike on the call and its output. `temperature` goes out
only when the request sets one; `stop`, `reasoning` and `prompt_cache_key`
never do: the API has no stop sequences, so `Request.Stop` is ignored, the
model's default effort applies, and `core.Request` carries no per-session
key. Every history item renders from
its own message only, so the prefix stays byte-identical across turns. The
reader keys every delta by output index, treats `output_item.done` (or,
when a stream never sends it, the terminal response's `output`) as
authoritative, and maps items to parts in output order: a reasoning item
becomes a thinking part (summary or raw reasoning text as its text; the
item with its `encrypted_content` as the signature), a message a text part
(its id and `phase` as the signature, never its text, which the agent
redacts), a function call a tool-call part (its `fc_` id as the signature).
Signatures carry SigKind `openai_responses` and the provider id, so they go
back verbatim only to the same provider, in the original order; a reasoning
item without encrypted content is never replayed, and the items after it
then go back without ids. Reasoning that no item of its own reply follows
(the call after it was cut off) is left out, since the API refuses a
reasoning item without its following item and the whole history goes back
every turn. Encrypted reasoning is bound to the organization that wrote it:
when the server refuses it before any event (`invalid_encrypted_content`,
e.g. after the key moved to another organization), the request is sent once
more without the reasoning of earlier messages, which is not replayed again
until a restart; a model that refuses encrypted content ("not supported with
this model", one the catalog did not know does not reason) is sent once more
without the `include` and never asked for it again. Usage comes from the terminal event:
`input_tokens` minus cached and cache-write tokens is `Input`,
`output_tokens` (reasoning included) is `Output`. `response.incomplete` is
`max_tokens` (`content_filter`: refusal) with cut-off calls dropped;
`response.failed` and `error` events (flat or nested) map like HTTP errors,
including a rate limit's "try again in" wait; a stream that ends without a
terminal event is a network failure.

**Chat Completions** (`New`, `NewChat`, `POST /chat/completions`) streams with
`stream_options.include_usage`, sends `max_completion_tokens` to OpenAI and
`max_tokens` to compatible servers, and uses the plain string content form
unless an image needs parts. When a request with tools is refused because the
model reasons (OpenAI from GPT-5.4 on, and servers that forward it; matched
by the message's key terms) and nothing has been streamed, it is sent once
more with `reasoning_effort: "none"`; an `EffortMemory` shared by the
facade's clients remembers per provider and model that tools need reasoning
off (later calls send it at once) or, when "none" is refused too (GPT-6
Astra), that tools do not work over Chat Completions at all (later calls
fail without asking again). The reader
assembles tool-call deltas by index (synthesizing `call_<n>` ids, telling
apart calls that all claim index 0 by id, accepting whole arguments in one
delta or as a JSON object; on `finish_reason: "length"` a call with cut-off
arguments is dropped), reads reasoning from `reasoning_content` or else
`reasoning` (reasoning that arrived as `reasoning_content` goes back under
that name to the same provider, which DeepSeek's thinking mode requires in
tool loops), extracts `<tool_call>{…}</tool_call>` text from `openai_compat`
servers when the request offered tools (OpenAI itself calls tools natively,
so there the tag is left as prose), keeps the last of repeated cumulative
usage frames, normalises cached and cache-write tokens out of `Input` and
reasoning reported beside `completion_tokens` (xAI) into `Output`, and marks
missing usage for estimation.

Both map error bodies (including the looser shapes of compatible servers and
in-stream errors) to `core.ErrorKind`: an account that cannot pay
(`insufficient_quota`, `credit_balance_exhausted`, the spend and usage limit
codes) is `auth`, policy violations are `content_filter`. Fixtures:
`testdata/README.md`.
