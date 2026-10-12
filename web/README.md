# web

The Svelte 5 single-page app (SPEC §9), built with Vite and Tailwind into
`../internal/webui/dist`, where the Go binary embeds it. Svelte is the only
runtime dependency; the router (`src/lib/router.svelte.ts`), API client
(`src/lib/api/client.ts`) and SSE store (`src/lib/sse.svelte.ts`) are written
by hand. `src/lib/api/types.gen.ts` is generated from the Go DTOs by `make gen`
and must not be edited. CSS is always external and the built HTML carries no
inline scripts or styles, so the server's CSP needs no `unsafe-inline`; the
build also writes a `.gz` next to every text asset for the server to send
pre-compressed. `npm run dev` proxies `/api` and `/v1` to a gateway on
127.0.0.1:8080; `npm run check` type-checks and `npm test` runs vitest. Install
with `npm ci --ignore-scripts` only, and run `make ui-reset` before committing.
