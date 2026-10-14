# bedrock

`core.Provider` for Amazon Bedrock: `POST /model/{modelId}/converse-stream`
on the Bedrock Runtime endpoint, answered in the AWS event-stream framing.
The Converse mapping is hand-written like the other clients; the AWS SDK for
Go v2 is used only for what is unsafe to hand-roll: credential resolution
(`config.LoadDefaultConfig`), SigV4 signing (`aws/signer/v4`) and
event-stream decoding (`aws/protocol/eventstream`). No SDK service client
sends a model request, and SDK retries are off (`aws.NopRetryer`): the facade
owns retries and fallback.

**Requests.** System blocks, tools (`toolSpec` with the JSON schema), text,
images (png, jpeg, gif, webp; others become a file note), tool calls and
results (results lead the user message that answers them; empty output
becomes "(no output)" since Converse refuses blank text; `is_error` is
`status: "error"`), consecutive same-role messages merged, and the reasoning
round trip: a thinking part this kind produced goes back as
`reasoningContent.reasoningText {text, signature}`, a redacted one as
`reasoningContent.redactedContent`; thinking from another kind, or without a
signature, is never sent, nor any reasoning to another vendor's model (Nova
rejects Claude's signed reasoning after a fallback). `cachePoint` blocks
follow `SystemBlock.CacheBreak`, the tools (`ToolsCache`) and the last
message (`CacheTail`) where Bedrock accepts them: Claude 3.5 Haiku, 3.7
Sonnet and every Claude 4+ model (system, messages, tools) and the Nova
understanding models (system and messages, and only after a text block, so
in a tool loop Nova's tail point follows the latest message ending in
text); at most four, the earliest system ones dropped first. Tool choice
`required` is `any`, except on the Claude models that refuse a forced choice
(Opus 5.5, Sonnet 5.5, Fable 5.1, Mythos 5.1), where it degrades to auto;
`none` leaves the tools out unless the history needs their definitions (then
auto). A history with tool blocks but no tool definitions renders them as
text, since Converse refuses `toolUse` without a `toolConfig`. Model ids are
escaped like the SDKs do (`:` and `/` too), so ids, inference-profile ids
and ARNs all work.

**Streams.** `messageStart`, `contentBlockStart` (tool use), `contentBlockDelta`
(text, tool input, reasoning text, signature, redacted reasoning),
`contentBlockStop`, `messageStop` and `metadata` (usage: `inputTokens`
already excludes `cacheReadInputTokens` and `cacheWriteInputTokens`, as the
recordings show) build the response through `wire.Builder`; thinking parts
carry `SigKind` `bedrock`. Stop reasons: `tool_use`; `max_tokens` and
`model_context_window_exceeded` are `max_tokens`; `guardrail_intervened` and
`content_filtered` are `refusal`; the rest `end_turn`. A stream that ends
before `messageStop` is a network failure, a corrupt frame a server fault,
and in-stream exceptions map like HTTP errors.

**Errors.** The code comes from `X-Amzn-ErrorType`, else the body's `__type`
or `code`: throttling and service quota are `rate_limit`, service
unavailable and model not ready `overloaded`, access denied and the
signature or token errors `auth` (the cached credentials are dropped),
validation `invalid_request` or `context_length` when it says the input is
too long, model timeout, model error and internal errors `server`. Messages
lose 12-digit account ids (`ScrubAccounts`) before they reach health state,
logs or the doctor.

**Credentials** (`Chain`). The SDK's default chain: environment, shared
config and credentials files (SSO sessions, assumed roles, `aws login`
sessions), web identity (IRSA), ECS and EKS Pod Identity container
credentials and EC2 IMDSv2. Its HTTP goes through a router: IP literals and
the container full URI's host to the egress credentials client, everything
else to an egress system client limited to the credential hosts the
configuration needs (STS of each named region, the SSO portal and OIDC
endpoints of SSO sessions, sign-in endpoints of login sessions, their FIPS
and dual-stack forms when configured, an `AWS_ENDPOINT_URL`), made only when
a credential service is actually called. The container provider is given
the router explicitly (it ignores the config's client). `credential_process`
is refused: the program would act outside egress. The config is loaded
lazily and again after a failure, so a fixed `~/.aws/config` needs no
restart. `Describe` says where credentials came from ("AWS SSO, profile dev
(session corp)", "EC2 instance role (IMDSv2)", "web identity token from the
environment (IRSA)") and never names keys, role ARNs or accounts.

**Models.** `Discover` lists `ListFoundationModels` (streamed text output)
and `ListInferenceProfiles` (system-defined, paginated) on the control-plane
endpoint and keeps the Claude and Nova text models: active cross-region
profiles and on-demand foundation models by bare id, named "Claude Sonnet
4.6 (US)", with models AWS marks LEGACY last. Speech models (Sonic) are left
out. Fixtures: `testdata/README.md`; the live smoke test is `live_test.go`
(`-tags live`).
