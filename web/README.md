# web

The Bothy web UI (SPEC §9; every screen is specified in `docs/design/ui.md`): a Svelte 5 single-page app built with Vite and
Tailwind into `../internal/webui/dist`, which the Go binary embeds and serves
pre-gzipped. `svelte` is the only runtime dependency. The router, API client,
SSE store, markdown renderer, QR encoder, charts and icons are written here.

## Commands

| Command | What it does |
|---|---|
| `npm ci --ignore-scripts` | install exactly what `package-lock.json` pins (never `npm install` without `--ignore-scripts`) |
| `npm run mock` | dev server with the fixture gateway in `mock/`: every `/api` route, a scripted SSE run and approvals; `MOCK_SETUP=1` starts at the first-run wizard (token `demo-setup-token`, or open `/setup#t=demo-setup-token`), `MOCK_LOGIN=0` starts logged out |
| `npm run dev` | dev server proxying `/api`, `/v1` and `/healthz` to a gateway on 127.0.0.1:8080 |
| `npm run build` | production build into `../internal/webui/dist` (plus a `.gz` next to every text asset) |
| `npm run check` | `svelte-check` over `src/` and `mock/` (warnings fail), then `npm run ui-lint` |
| `npm run ui-lint` | `scripts/ui-lint.sh`: the class and markup rules of `docs/design/ui.md` §18.2 G5 over `src/` (no chips, blur, large radii, stray shadows, uppercase, bold, pixel sizes, `style=`, v0.1 or palette colours) |
| `npm test` | vitest: unit, component and axe tests in jsdom against the fixture gateway, and `src/copy.test.ts`, which holds every interface string to the copy guide (§14) |
| `npm run preview:mock` | serves the built app with the fixture gateway and the gateway's CSP |
| `npm run smoke` | Playwright against `preview:mock`: every route at 360 px and at the 408 px left edge, keyboard-only approval, axe with contrast in light and dark, no CSP violations, tabs sharing one event stream; `PLAYWRIGHT_CHROMIUM=<path>` uses a local Chromium (screenshot sets: `e2e/README.md`) |

From the repository root, `make ui` installs and builds, `scripts/budgets/ui-size.sh`
checks the gates, and `make ui-reset` restores the committed placeholder; run
it before committing so a build never lands in git.

## Layout

```
src/
  App.svelte            routing, guards (setup → login → first password → app)
  lazy/                 the route groups loaded on demand (setup and sign in, approvals,
                        knowledge, activity, settings) and their prefetching
  app.css               design tokens (light and dark; Tailwind's own reset), component classes, markdown styles
  copy.test.ts          the copy guide over every source string
  lib/
    api/client.ts       fetch wrapper: X-CSRF-Token, dto.Error → ApiError, 401 → login
    api/types.gen.ts    generated from the Go DTOs by `make gen` (do not edit)
    api/knowledge.ts    Knowledge client and its helpers
    api/repos.ts        the repositories Bothy reads (new chat; kept out of the Knowledge chunk)
    router.svelte.ts    history router; safeNext() for ?next=
    routes.ts           the routes and the v0.1 redirects (docs/design/ui.md §4.5)
    sse.svelte.ts       one EventSource per browser, shared by tabs (Web Lock + BroadcastChannel):
                        typed handlers, hello, resync, reconnect with backoff
    stores/             session, approvals and the inbox count, theme and shell state, one-second clock
    format.ts           money, times, durations, countdowns and plurals in the copy guide's words
    activity.ts         what Bothy did, said per tool and tense (chat and task pages)
    cardcopy.ts         plain approval-card copy when a card predates the server's
    markdown.ts         escape-all renderer (security control, see below)
    text.ts             bidi/zero-width visualisation and punycode for approval cards
    transcript.ts       persisted messages + live run events → transcript rows
    config.svelte.ts    effective config (§7.3) for Security and Advanced
    icons.ts            the 18 inline icons
    qr.ts               QR encoder, byte mode, versions 1–6, level M (lazy chunk)
  components/
    ui/                 primitives (buttons, fields, lists, dialogs, menus, notices, DiffView…) and Gallery (/dev/ui, dev only)
    shell/              sidebar, settings navigation, phone top bar and drawer, banners, search and dialogs
    chat/               activity lines, composer pieces, chat dialogs and drawers
    approval/           approval cards, votes, decided lines, inbox review cards
  screens/              chat, approvals, knowledge, activity, settings (with connections,
                        security and advanced), setup, sign in, first password
  test/                 jsdom shims and the fetch → fixture-gateway adapter
mock/                   fixture gateway (dev and tests only; never bundled)
e2e/                    Playwright smoke tests and screenshot sets
scripts/ui-lint.sh      the G5 source rules, run by npm run check
scripts/icons.mjs       renders the PNG icons and the default chat-app picture
                        from the SVGs in public/ (make icons)
public/                 theme.js (sets the theme before first paint), PWA manifest,
                        icon.svg (the mascot on its tile), icon-maskable.svg
                        (full bleed) and the PNGs rendered from them
```

## Security rules this package keeps

- **No inline scripts or styles.** CSS is external (`css: 'external'`); there are
  no `style="…"` attributes in markup, only `style:` directives and CSSOM writes,
  which the gateway CSP (`style-src 'self'`) allows. `public/theme.js` is an
  external classic script. `npm run smoke` fails on any CSP violation.
- **Zero other origins.** System fonts only, no CDNs, no remote images.
- **Markdown is escape-all.** `lib/markdown.ts` escapes every input byte and emits
  a fixed tag set with attributes it builds itself; images become text links,
  only absolute http(s)/mailto links survive (with `noopener noreferrer nofollow`;
  bare domains such as `attacker.com/?q=…` are never linked, escaped or not),
  and in Exposed sessions links become inert hosts with a copy button.
  `Markdown.svelte` is the only `{@html}` in the app; `markdown.test.ts` holds
  the XSS corpus.
- **Approval cards show only the card DTO**, verbatim, with bidi and zero-width
  characters drawn as `⟨U+202E⟩` and non-ASCII hosts in punycode. Model text never
  appears on a card.
- **Secrets are write-only**: key fields are password inputs sent once to the
  vault; nothing reads a value back. The setup token is read from the URL
  fragment once and stripped with `history.replaceState`.
- `?next=` only accepts same-origin paths (`safeNext`).

## Budgets

`scripts/budgets/ui-size.sh` holds the first screen (the scripts `index.html`
loads, their static imports and its stylesheet) to ≤ 120 KB gz of JS and
≤ 20 KB gz of CSS, and every JS chunk together to ≤ 260 KB gz, and prints each
file's size.

Chat, the shell and the primitives they use are the entry, built as one file
(`vite.config.ts`). Setup and sign in, Approvals, Knowledge, Activity and
Settings are each a chunk behind a dynamic import (`src/lazy`); what two or more
of them share is one more chunk, and the QR encoder is its own. `package.json`
marks only CSS as having side effects, so a module that imports the
`components/ui` barrel pulls in only the primitives it uses. Once someone is
signed in, the groups load one at a time while the browser is idle (not with
Save-Data on), and a link starts loading its group as soon as a pointer, finger
or focus reaches it, so moving between areas stays instant. Vite's module
preload adds `<link rel="modulepreload">` from script, never inline code, so the
CSP stays `script-src 'self'`. A chunk that fails to arrive (offline, or a build
that replaced it) shows "This page didn't load" with Try again, which reloads.
