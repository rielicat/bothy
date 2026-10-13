# ollama

`core.Provider` for Ollama's native API rather than its OpenAI-compatible
endpoint, because only `/api/chat` honours `options.num_ctx`: the facade sends
the model's effective window as `num_ctx` and `MaxTokens` as `num_predict`:
the configured context, else `Window` of the trained maximum (`/api/show`)
and the served context (`/api/ps`), capped at `DefaultWindow` (16,384) unless
the server already serves more, because Ollama reserves memory for whatever
num_ctx asks. The NDJSON reader takes thinking, content and
whole tool calls per line (ids synthesized when absent), usage from the final
`done` line's `prompt_eval_count` and `eval_count` (a missing prompt count is
left for the facade to estimate), `done_reason: "length"` as `max_tokens`, and
`{"error": …}` lines and bodies as provider errors. Thinking goes back in the
assistant message's `thinking` field. `Tags`, `Show` (trained context and
capabilities from `/api/show`) and `Running` (`/api/ps`, the context the
server actually allocated) serve discovery and `Probe`. An API key, when
configured, is sent as a bearer token for Ollama behind a proxy. Fixtures:
`testdata/README.md`.
