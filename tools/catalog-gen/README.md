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
and `openrouter`, with the tiers of `tiers.json` as read on 2026-10-13 and
the Bedrock overlay of `bedrock.json` as read on 2026-10-14.
`testdata/` holds two of those files verbatim.

## Amazon Bedrock

catwalk's Bedrock files carry Anthropic's prices on `us.` ids, but AWS bills
geographic profiles (and in-region use) 10% above global ones for Claude
Sonnet 4.5 and later, and they lack Nova. `bedrock.json` (embedded, hand
maintained) is the source instead: one entry per base model id with the
global `price` and, where higher, the `geo_price`, both in USD per 1M tokens
from the AWS Price List API (`AmazonBedrockFoundationModels` for Anthropic,
`AmazonBedrock` for Nova; us-east-1 and us-west-2 agree), plus optional
`tiers`. A Claude entry names the Anthropic model it `like`s, whose window,
output limit and capabilities it copies from the generated catalog (so the
anthropic file must be among the inputs); other entries state them with a
`note` on their source. Re-read the price list offers (`curl
https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/<offer>/current/region_index.json`)
when updating; `TestCommittedCatalogMatchesBedrockOverlay` fails when
`catalog.json` and `bedrock.json` disagree.

Not in the overlay: models this account's listings did not offer or that
are retiring (Claude 3.x, Claude Mythos 5.1, Nova 2 Pro and Omni, which were
priced but not listed in us-west-2), and every non-Anthropic, non-Amazon
vendor; Bothy still runs them with unknown prices (`pricing` sets one).
