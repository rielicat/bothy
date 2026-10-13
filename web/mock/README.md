# web/mock

A fixture gateway for UI work without a Go build: `server.ts` answers every
`/api` route of SPEC §8.3 from in-memory state seeded by `fixtures.ts`, streams
SSE (`/api/events`) with a scripted run when a message is sent (deltas, a tool
call, an approval card for messages that ask to run, send or delete something,
usage and the finished event), and emits `pairing.linked` a few seconds after a
pairing code is created.

`fixtures/effective.json` and `fixtures/schema.json` are the real output of
`internal/config` (`Effective()` and `Schema()` for its `testdata/bothy.yaml`
with one env and two web UI overrides), so settings pages render real paths,
sources and enums.

It is loaded three ways, never by a production build:

- `npm run mock`: Vite dev server middleware (`BOTHY_MOCK=1`);
- `npm run preview:mock`: the built app under the gateway's CSP and headers;
- unit tests: `src/test/api.ts` routes `fetch` to `handle()` and `resetMock()`
  gives each test fresh state.

Login accepts any username with any password except `wrong`; the CSRF token is
`mock-csrf`.
