# provider

`provider.Facade` implements `core.LLM` (SPEC §4.3, §5.14) over hand-written
clients for the Anthropic Messages API, the OpenAI Responses API and Chat
Completions (`providers[].api`: `auto` picks Responses for type `openai` and
Chat Completions for `openai_compat`), Gemini `streamGenerateContent` and
native Ollama `/api/chat`. It re-reads the `providers` config on every call
(hot reload, so providers added by the setup wizard, or a changed `api`, work
without a restart), resolves each `secret:<name>` key through the vault and
keeps it for at most five minutes, and gets every HTTP client from
`core.Egress.SystemClient`, hardened to no proxy, TLS 1.2+ and no redirects
(`base_url` must be http(s) without credentials, query or fragment).
`Complete` walks the slot's fallback chain: before the first streamed event a
retryable error (429, 408, 5xx, network, overloaded) is retried once after
250 ms ±20% or `Retry-After` (≤ 20 s), then the next candidate is tried; auth
errors mark the provider `auth_failed` and move on; `context_length`,
`invalid_request` and `content_filter` return to the runner; after the first
event nothing is retried or switched. Before a call, notice messages are
dropped and thinking parts or signatures of another SigKind than the client's
are stripped (the Responses client writes `openai_responses`, so its
encrypted reasoning and item ids never reach the Chat Completions client or
another vendor). Three consecutive failures open a provider's breaker for
60 s. Model info merges kind defaults, discovery, the embedded catalog
(`catalog/`), `providers[].models` and `pricing`; costs are integer micros.
Also here: the bytes/3.6 estimator with a per-model EWMA correction,
missing-usage estimation, `Probe` (one-token completion and forced tool call
through the API the provider would use, reported in `ProbeResult.API` and
explained in its detail, including a Chat Completions endpoint that takes
tools only with reasoning off; Ollama ones sent with the num_ctx Bothy will
use, then a context check with the `OLLAMA_CONTEXT_LENGTH` hint), `Detect`
(Ollama, LM Studio and vLLM on this machine and on the Docker host, and the
names of well-known key variables) and the `providers.auth_failed`,
`providers.ollama_context` and `providers.chat_tools` checks (the last lists
models that Chat Completions refused tools while reasoning, from the memory
the Chat Completions clients share, which a provider config change clears).
An Ollama model without a configured context never gets its trained maximum
as num_ctx: `ollama.Window` caps it at 16,384, or at the served context when
Ollama already runs the model with more.
`wire/` holds what the clients share; `wiretest/` serves recorded vendor
responses in tests.
