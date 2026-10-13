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
