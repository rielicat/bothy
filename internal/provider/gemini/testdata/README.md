# Gemini fixtures

Fetched 2026-10-12. The `js_*` files come from real API recordings in
[googleapis/js-genai](https://github.com/googleapis/js-genai)
`test/system/recordings/`. Those store each server-sent event of an
`alt=sse` response as a parsed `bodySegments` entry; each segment is written
back as one `data: <compact JSON>` event terminated by CRLF, which is how
Gemini frames the stream (`.gitattributes` keeps the CRLF). The JSON content
is unchanged.

| File | Recording (interaction) |
|---|---|
| `js_text_stream.sse` | `Client_Tests_generateContentStream_ML_Dev_should_stream_generate_content_with_system_instruction.json` (0): six chunks, cumulative `usageMetadata` with `thoughtsTokenCount` |
| `js_function_call_signature.sse` | `AFC_Streaming_Tests_chat_stream_AFC_enabled_AFC_max_calls_exceeded_Google_AI_can_continue_after_max_calls_exceeded.json` (0): `functionCall` with `thoughtSignature` |
| `js_text_signature.sse` | same recording (2): text part with `thoughtSignature` |
| `js_function_call_stream.sse` | `Chats_Tests_chats_function_calling_Google_AI_with_function_calling_stream.json` (0) |
| `js_parallel_calls.sse` | `MCP_related_client_Tests_generateContent_ML_Dev_one_CallableTool_with_MCPClients_and_conduct_automated_function_calling.json` (0): two parallel `functionCall` parts, the signature on the first only. A non-streaming `generateContent` body, i.e. the same `GenerateContentResponse` a stream chunk carries, framed as a single event; no streaming recording has parallel calls. |
| `js_usage_cache.sse` | `Client_Tests_generateContent_ML_Dev_should_generate_content_with_given_valid_json_schema_in_responseSchema.json` (0): `cachedContentTokenCount`; non-streaming body framed as one event, as above |
| `js_error_404.json` | `Chats_Tests_sendMessage_Send_message_stream_with_error.json` (0): the 404 body |
| `docs_error_api_key.json` | [API errors](https://ai.google.dev/gemini-api/docs/generate-content/api-errors), "Error response format" (an invalid key is a 400 `INVALID_ARGUMENT` with reason `API_KEY_INVALID`) |

The status table in `gemini_test.go` follows the HTTP error-code table of the
same page.
