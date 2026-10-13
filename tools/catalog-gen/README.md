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

catwalk has no long-context prices, so `tiers.json` (embedded) adds them: each
rule names a catwalk provider id, a regexp over model ids, the tiers (as
`core.PriceTier`: `above_input` tokens, `permille` multipliers with optional
`out_permille`, `cache_read_permille`, `cache_write_permille`) and the source
and date the prices were read from. Gemini Pro, xAI and OpenAI's 1M-window
models, directly and through OpenRouter, are covered; re-check the vendors'
pricing pages (and OpenRouter's `pricing.overrides` in `/api/v1/models`) when
updating. A rule that matches no priced model is reported as a warning, and
`TestCommittedCatalogMatchesOverlay` fails when `catalog.json` and `tiers.json`
disagree.

```sh
cd tools
go run ./catalog-gen -source "catwalk v0.52.69 (dea274af)" \
  -out ../internal/provider/catalog/catalog.json path/to/catwalk/internal/providers/configs
```

The committed catalog was generated from catwalk v0.52.69 (commit
`dea274af`), files `anthropic`, `openai`, `gemini`, `xai`, `deepseek`, `groq`
and `openrouter`, with the tiers of `tiers.json` as read on 2026-10-13.
`testdata/` holds two of those files verbatim.
