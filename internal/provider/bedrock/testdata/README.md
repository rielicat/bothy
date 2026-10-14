# Bedrock fixtures

Event streams are stored one frame per line (`.jsonl`): the frame's string
headers and its payload verbatim, so they diff and review as text; the tests
encode them back into event-stream frames with fresh CRCs. One recording is
also kept byte for byte (`live_text.eventstream`) so the decoder is tested on
Bedrock's own framing. JSON responses keep the status, content type,
`X-Amzn-ErrorType` (when sent) and body; other response headers, every request
header, signatures and credentials are never recorded.

## Recorded (`live_test.go`, 2026-10-14, us-west-2)

Recorded with `BOTHY_RECORD=1 go test -tags live -run TestLiveBedrock` against
a real account through an SSO profile. AWS account ids are replaced by the
documentation account `111122223333`, also inside the Claude reasoning
signature, which embeds the caller's account id (the signature is otherwise
unchanged and no longer verifies with AWS, which the unit tests do not need).

| File | Request |
|---|---|
| `live_text.jsonl`, `live_text.eventstream` | Claude Sonnet 4.5 (`us.`), "Reply with exactly these words: Hello from Bedrock" |
| `live_parallel_tools.jsonl` | Sonnet 4.5, two tools, two `toolUse` blocks in one turn |
| `live_reasoning_tool.jsonl` | Sonnet 4.5 with `thinking: {type: enabled, budget_tokens: 1024}`: signed `reasoningContent`, text, `toolUse` |
| `live_reasoning_tool_result.jsonl` | the follow-up with the signed reasoning and a `toolResult` sent back (the round trip AWS verifies) |
| `live_cache_write.jsonl`, `live_cache_read.jsonl` | Sonnet 4.5, a ~2K-token system block with a `cachePoint`, twice |
| `live_image.jsonl` | Claude Opus 4.5 (`us.`), a 16×16 PNG |
| `live_max_tokens.jsonl` | Sonnet 4.5, `maxTokens: 3` |
| `live_nova_tool.jsonl` | Nova Lite (`us.`), a forced tool call (`toolChoice: any`) and a system `cachePoint` |
| `live_error_unknown_model.json` | an unknown model id: 400 `ValidationException` |
| `live_list_foundation_models.json` | `ListFoundationModels?byOutputModality=TEXT`, trimmed to the Anthropic and Amazon entries plus the first Meta and Mistral AI ones (93 → 25), order and fields unchanged |
| `live_list_inference_profiles.json` | `ListInferenceProfiles?type=SYSTEM_DEFINED`, trimmed to the Anthropic and Amazon profiles plus the first Meta one (97 → 40), order and fields unchanged |

## Copied

| File | Source |
|---|---|
| `sdk_*.error.snap` | [aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2) `service/bedrockruntime` v1.63.4, `response_snapshot/*.error.snap`, byte for byte: the Bedrock Runtime error shapes and status codes the SDK's tests use |
| `docs_converse_stream_request.json` | [ConverseStream API reference](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ConverseStream.html), "Sample Request" body (read 2026-10-14) |

## Composed

AWS publishes no raw ConverseStream responses, and these cases were not worth
provoking live; they use the documented event and exception shapes.

| File | Composition |
|---|---|
| `composed_midstream_exception.jsonl` | the first three frames of `live_text.jsonl`, then a `throttlingException` exception frame |
| `composed_model_stream_error.jsonl` | `live_text.jsonl`'s first frame, then a `modelStreamErrorException` with `originalStatusCode` |
| `composed_truncated.jsonl` | `live_text.jsonl` cut before `messageStop` |
| `composed_redacted_reasoning.jsonl` | a `redactedContent` reasoning block, a signed reasoning block and text ([ReasoningContentBlockDelta](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ReasoningContentBlockDelta.html)); the base64 values are made up |
