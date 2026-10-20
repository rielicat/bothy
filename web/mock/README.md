# web/mock

A fixture gateway for UI work without a Go build: `server.ts` answers every
`/api` route of SPEC §8.3 from in-memory state, streams SSE (`/api/events`)
with a scripted run when a message is sent (deltas, a tool call, an approval
card for messages that ask to run, send or delete something, usage and the
finished event).

The routes and their seed data live in `domains/`, one file per area of the UI
(docs/design/ui.md §18.3), so each UI chunk edits only its own file:

| File | Area |
|---|---|
| `core.ts` | shared plumbing: route table, event stream, people, signed-in state, scenarios |
| `shell.ts` | `/api/auth/me`, the event stream, banners, pausing |
| `setup.ts` | setup code, owner, sign in, provider detection and checks |
| `chat.ts` | sessions, messages, pins, uploads, search, the scripted run |
| `approvals.ts` | approvals and decisions |
| `knowledge.ts` | memory and skills |
| `activity.ts` | runs, schedules, usage, audit |
| `settings.ts` | config, models, providers, people, API tokens, personality |
| `connections.ts` | chat apps, the people Bothy answers in them, group chats, MCP |
| `picture.ts` | Bothy's picture: the default or an upload, and what a change did in each chat app |
| `security.ts` | grants, policy tester, vault, sandbox, doctor, backups |
| `signin.ts` | sign in with Google and GitHub: the sign-in page's buttons, a fake provider round trip (`/api/mock/oauth/{p}`), Account → Sign-in methods, Security → Sign-in |

`fixtures.ts` re-exports the seed values for tests. `fixtures/effective.json`
and `fixtures/schema.json` are the real output of `internal/config`
(`Effective()` and `Schema()` for its `testdata/bothy.yaml` with one env and
two web UI overrides), so settings pages render real paths, sources and enums.

It is loaded three ways, never by a production build:

- `npm run mock`: Vite dev server middleware (`BOTHY_MOCK=1`);
- `npm run preview:mock`: the built app under the gateway's CSP and headers;
- unit tests: `src/test/api.ts` routes `fetch` to `handle()` and `resetMock()`
  gives each test fresh state.

Login accepts any username with any password except `wrong`; the CSRF token is
`mock-csrf`.

## Scenarios

States the default fixtures don't show (banners, failures, detection results,
empty lists) are switched on per page with `?mock=`:

```
/chat?mock=paused,sealed
/setup?mock=setup,detect-aws
/setup?mock=setup,provider-fail:out_of_credit
/login?mock=logged-out,signin-google,signin-github
/settings/security/google-sign-in?mock=signin-google,signin-check-fail:key_rejected
```

Sign-in has `signin-google`, `signin-github`, `signin-fail:<code>`,
`signin-linked`, `signin-only`, `signin-no-address`, `signin-http-address`,
`signin-check-fail:<code>`, `signin-no-secret`, `wrong-address` and
`team-mode`. The mock runs at `https://bothy.acme.com` when the config
fixture has no web address. Its GitHub knows every organization but
`no-such-org`, and a refused round trip leaves the account in the
`bothy_signin_who` cookie, as the gateway does.

Chats include related ones (SPEC §14.10): "Pricing page copy for the launch"
continued from "Q3 pricing notes", two results each of "Morning briefing" and
the check-in (one answered all-clear, then by the person), and `team-chats`
adds a schedule's series with a chat continued from an earlier result. A fork
lists its chat under the one it came from with the clean-chat notice.
`GET /api/starters` reads the other domains' routes, as the gateway reads its
services, so `knowledge-empty`, `github-none`, `github-no-repos`,
`chat-apps-off`, `people-solo` and `member` change the rows; `starters-fail`
fails it. `welcome` gives the account its welcome chat once.

`hero` is the Friendly mockup's hero (docs/design/ui.md D46): “Release notes
for v2.4”, continued from “Web v2.4 release”, whose turn ends on the pull
request it waits for, and an inbox holding only that request, a newsletter
for you and a deploy waiting for others. Its chats and requests are stored
always and listed only with the scenario, so the default lists and counts
stay as they are.

Bothy's picture has `picture-custom` (an admin replaced the default),
`picture-fail[:telegram|discord]` (a change fails in that chat app; Discord
says it allows only a few changes an hour) and
`picture-refused[:not-picture|too-large|too-small]`. The mock keeps any upload:
a PNG, JPEG or WebP comes back as sent, anything else as a stand-in picture.

Opening a page with `?mock=` resets the fixtures and stores the list in the
`bothy_mock` cookie, so the page's `/api` calls and later in-app navigation
see it; `?mock=` with nothing after it resets and clears the list. `MOCK=a,b`
in the environment and `resetMock({ mock: 'a,b' })` in unit tests set a
default list. `GET /api/mock/scenarios` lists every scenario and what it does.

To add one, declare it in the domain that serves the state and test it in the
handler:

```ts
const empty = scenario('skills-empty', 'No skills installed.', 'knowledge');
on('GET', '/api/skills', (req, res) => json(res, 200, empty(req) ? [] : state.skills));
```

`scenario.arg(req)` returns the text after `name:` (for example the failure
code in `provider-fail:out_of_credit`).
