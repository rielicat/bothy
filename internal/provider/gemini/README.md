# gemini

`core.Provider` for the Gemini API: `POST
/v1beta/models/{model}:streamGenerateContent?alt=sse` with the key in the
`x-goog-api-key` header, never in the URL. The model goes into the path, so
only `name`, `models/name` and `tunedModels/name` with a plain name
(letters, digits, `.`, `_`, `-`) are accepted; anything else is an
`invalid_request` before any request is sent. Requests carry
`systemInstruction`, tools as `functionDeclarations` with
`parametersJsonSchema` (full JSON Schema), `toolConfig` (AUTO, NONE, or ANY
for the probe's forced call) and `generationConfig`; tool results go back as
`functionResponse` parts, and consecutive same-role contents merge. The
reader turns `functionCall` parts into whole tool calls, keeps each
`thoughtSignature` on the part it arrived with (so the first of several
parallel calls carries it back; the first call of each step of the current
turn without one, made by another vendor before a fallback, gets the
documented `skip_thought_signature_validator`), reads thought parts as
thinking, takes the last cumulative `usageMetadata` (cached tokens out of
`Input`, thoughts counted as output), maps safety finishes to `refusal` and
`MALFORMED_FUNCTION_CALL`/`UNEXPECTED_TOOL_CALL` to a retryable server
error. An invalid key is a
400 `INVALID_ARGUMENT` with reason `API_KEY_INVALID` and maps to `auth`.
Synthesized call ids are not echoed back. `Discover` lists `GET /v1beta/models`
with their token limits. Fixtures: `testdata/README.md`.
