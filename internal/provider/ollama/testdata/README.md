# Ollama fixtures

From [ollama/ollama](https://github.com/ollama/ollama) `docs/`, fetched from
`main` on 2026-10-12. The API reference prints each streamed object
pretty-printed; the wire format is one compact JSON object per line, so the
documented objects are re-serialized that way (key order and values
unchanged).

| File | Source |
|---|---|
| `docs_chat_stream.ndjson` | `docs/api.md`, "Chat request (Streaming)": the streamed object and the final response |
| `docs_tools_stream.ndjson` | `docs/api.md`, "Chat request (Streaming with tools)" |
| `docs_tags.json` | `docs/api.md`, "List Local Models" response |
| `docs_show.json` | `docs/openapi.yaml`, `/api/show` 200 example, converted from YAML to JSON (`gemma4.context_length: 131072`) |
| `docs_ps.json` | `docs/openapi.yaml`, `/api/ps` 200 example, converted from YAML to JSON (`context_length: 4096`) |
| `docs_midstream_error.ndjson` | `docs/api/errors.mdx`, "Errors that occur while streaming", verbatim |

The error-status table in `ollama_test.go` uses the documented
`{"error": "…"}` body with the statuses listed in `docs/api/errors.mdx`.
`TestStreamParallelToolsAndThinking` writes its chunks inline in the
documented message shape (a `tool_calls` array with two entries and the
`thinking` field) because the docs publish no stream with either.
