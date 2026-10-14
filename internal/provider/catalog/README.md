# catalog

The embedded model catalog: `catalog.json` lists, per vendor endpoint, each
model's window, output limit, tool/vision/thinking support, cache mode and
price in integer USD micros per million tokens (input, output, cache read,
cache write, optional tiers). It is generated at release time by
`tools/catalog-gen` from catwalk data and never edited by hand. `Find` matches
Anthropic, OpenAI and Gemini providers by kind and OpenAI-compatible ones by
the host of their base URL (OpenRouter, xAI, Groq, DeepSeek); Ollama and local
servers have no entry. `Provider.Model` falls back to ignoring a trailing
release date, so `claude-haiku-4-5` finds `claude-haiku-4-5-20251001`. A
model without a price is reported as unknown (`Price.Known == false`); the
`pricing` config section overrides the catalog.

The `bedrock` provider (matched by kind, any region) lists Claude and Nova
by base model id (`anthropic.claude-sonnet-4-5-20250929-v1:0`); a
cross-region profile id (`us.…`, `eu.…`, `global.…`) or its ARN finds its
model. `price` is the global profile's, `geo_price` the price AWS charges for
geographic profiles and in-region use where it is higher (10% for Claude
Sonnet 4.5 and later), so `us.` and `eu.` ids are priced with `geo_price`
and `global.` ids with `price`. Prices are AWS's US list prices; other
regions may cost more.
