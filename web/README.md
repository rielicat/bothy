# web

The Bothy web UI (SPEC §9): a Svelte 5 single-page app built with Vite and
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
| `npm run check` | `svelte-check` over `src/` and `mock/`, warnings fail |
| `npm test` | vitest: unit, component and axe tests in jsdom against the fixture gateway |
| `npm run preview:mock` | serves the built app with the fixture gateway and the gateway's CSP |
| `npm run smoke` | Playwright against `preview:mock`: 360 px layout, keyboard-only approval, axe with contrast in light and dark, no CSP violations; `PLAYWRIGHT_CHROMIUM=<path>` uses a local Chromium |

From the repository root, `make ui` installs and builds, `scripts/budgets/ui-size.sh`
checks the gates, and `make ui-reset` restores the committed placeholder; run
it before committing so a build never lands in git.

## Layout

```
src/
  App.svelte            route table, guards (setup → login → app), shell
  app.css               design tokens (light and dark), component classes, markdown styles
  lib/
    api/client.ts       fetch wrapper: X-CSRF-Token, dto.Error → ApiError, 401 → login
    api/types.gen.ts    generated from the Go DTOs by `make gen` (do not edit)
    api/extra.ts        fields tygo drops from types.gen.ts (see ICR in the module report)
    router.svelte.ts    history router; safeNext() for ?next=
    sse.svelte.ts       one EventSource: typed handlers, hello, resync, reconnect with backoff
    stores/             session and approvals, theme and toasts, one-second clock
    markdown.ts         escape-all renderer (security control, see below)
    text.ts             bidi/zero-width visualisation and punycode for approval cards
    transcript.ts       persisted messages + live run events → transcript rows
    config.svelte.ts    effective config (§7.3): entries by path, schema walk, set/reset
    qr.ts               QR encoder, byte mode, versions 1–6, level M (lazy chunk)
    yaml.ts, diff.ts    effective-config YAML, line diffs for memory and MCP pins
  components/           ApprovalCard, ConfigField + SourceChip, charts, dialogs, setup pieces
  screens/              chat, approvals, run trace, schedules, memory, usage, audit,
                        settings (13 pages) and the first-run wizard
  test/                 jsdom shims and the fetch → fixture-gateway adapter
mock/                   fixture gateway (dev and tests only; never bundled)
e2e/                    Playwright smoke test
public/                 theme.js (sets the theme before first paint), PWA manifest, icons
```

## Security rules this package keeps

- **No inline scripts or styles.** CSS is external (`css: 'external'`); there are
  no `style="…"` attributes in markup, only `style:` directives and CSSOM writes,
  which the gateway CSP (`style-src 'self'`) allows. `public/theme.js` is an
  external classic script. `npm run smoke` fails on any CSP violation.
- **Zero other origins.** System fonts only, no CDNs, no remote images.
- **Markdown is escape-all.** `lib/markdown.ts` escapes every input byte and emits
  a fixed tag set with attributes it builds itself; images become text links,
  only absolute http(s)/mailto links survive (with `noopener noreferrer nofollow`),
  and in Exposed sessions links become inert host chips with a copy button.
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

JS ≤ 120 KB gz (target 90), CSS ≤ 20 KB gz, enforced by
`scripts/budgets/ui-size.sh`. Everything but the QR encoder ships as one bundle,
which compresses better than a chunk per screen.
