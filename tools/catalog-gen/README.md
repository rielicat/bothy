# catalog-gen

Regenerates `internal/provider/catalog/catalog.json`, the embedded model
catalog (window, output limit, capabilities and USD-micros prices), from
[catwalk](https://github.com/charmbracelet/catwalk) provider data at release
time. It reads catwalk's published JSON (the files under
`internal/providers/configs/` at a tag, or the array the catwalk service
serves) rather than importing the Go package, so the tools module carries no
extra dependency. Only providers Bothy can talk to are kept: Anthropic, OpenAI
and Gemini by kind, and OpenAI-compatible ones (OpenRouter, xAI, Groq,
DeepSeek…) keyed by their `https://` endpoint; catwalk's `cost_per_1m_in_cached`
is the cache-write price and `cost_per_1m_out_cached` the cache-read price,
except where an entry lists a read price under `in_cached` (cheaper than
input), which is recognised and moved. A cache-read price above the input
price (some entries carry the output price there) is dropped, since a cache
hit never costs more than the input it replaces. Output is sorted, one model
per line.

```sh
cd tools
go run ./catalog-gen -source "catwalk v0.52.69 (dea274af)" \
  -out ../internal/provider/catalog/catalog.json path/to/catwalk/internal/providers/configs
```

The committed catalog was generated from catwalk v0.52.69 (commit
`dea274af`), files `anthropic`, `openai`, `gemini`, `xai`, `deepseek`, `groq`
and `openrouter`. `testdata/` holds two of those files verbatim.
