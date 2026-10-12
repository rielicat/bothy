# httpapi test fixtures

- `openai_client_request.json`: the JSON body the official `openai` Python
  client (v1.x) sends for
  `client.chat.completions.create(model="bothy", messages=[...], stream=True,
  stream_options={"include_usage": True}, temperature=0.2, user="alice")`.
  Written by hand from the client's documented request shape; it is not a
  byte-for-byte capture. The test decodes the streamed response with the same
  rules the client's SSE decoder applies (`data:` lines, `[DONE]` sentinel,
  comment lines ignored) and checks every field the client's
  `ChatCompletionChunk` model requires.
- `slack_manifest.golden.yaml`: the SPEC §8.6 Slack app manifest rendered for
  the agent name "Moss".
