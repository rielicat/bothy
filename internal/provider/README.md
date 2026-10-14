# provider

`provider.Facade` implements `core.LLM` (SPEC §4.3, §5.14) over hand-written
clients for the Anthropic Messages API, the OpenAI Responses API and Chat
Completions (`providers[].api`: `auto` picks Responses for type `openai` and
Chat Completions for `openai_compat`), Gemini `streamGenerateContent`, native
Ollama `/api/chat` and Amazon Bedrock `ConverseStream`. It re-reads the
`providers` config on every call (hot reload, so providers added by the setup
wizard, or a changed `api`, work without a restart), resolves each `secret:<name>` key through the vault and
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

**Amazon Bedrock** (`type: bedrock`, `bedrock/`) has no key: each provider
builds an AWS default credential chain for its `region` and optional
`profile`, whose instance and container credential requests go through
`Deps.Credentials` (egress's credentials-only client) and whose STS/SSO
calls go through a system client for exactly the hosts the AWS config
needs. The runtime endpoint (or a `base_url` VPC endpoint, https only) and
the control-plane endpoint share one egress destination. An auth failure
also drops the chain's cached credentials, and the banner and doctor speak
of AWS credentials instead of a key. Bedrock models get explicit caching
only where Bedrock takes cache points, and an `allow_private` VPC endpoint
does not make them local (free). `Probe` resolves the credentials and
reports their source, lists the usable Claude and Nova models with display
names (`ProbeResult.Choices`) and tests a small one by default; `Detect`
offers "Use this server's AWS access" first when the chain resolves (within
4 s when the environment or `~/.aws` suggests AWS, else within the local
detection timeout for IMDS), with region and models; the doctor check
`providers.bedrock_credentials` reports each bedrock provider's credential
source (info) or why there is none (high), never values.
`wire/` holds what the clients share; `wiretest/` serves recorded vendor
responses in tests.
