# web/e2e

Browser checks against the production build, served by `vite preview` with the
fixture gateway (`web/mock`) and the gateway's CSP. Two kinds live here:

- **Smoke** (`*.spec.ts` here and in `lib/`): layout at 360 px and the 408 px
  left edge at 1280 px, axe with contrast in both themes on every route,
  redirects from v0.1 addresses,
  keyboard-only approval, streaming under the CSP, tabs sharing one event
  stream, and the self-tests of the helpers below. This is gate G2.
- **Screenshot sets** (`shots/<chunk>.spec.ts`): every route and state a chunk
  owns, at 1280 × 800 and 360 × 780, light and dark, with the layout probes run
  on each capture. These are gates G3 and G4 (docs/design/ui.md §18.2). They
  are skipped unless `SHOTS=1`, so the smoke run stays quick.

## Running

```sh
cd web
npm ci --ignore-scripts          # once
npm run build                    # both kinds test the build, so rebuild after changes
npm run smoke                    # G2
SHOTS=1 npx playwright test e2e/shots/all.spec.ts        # every route, all probes enforced
SHOTS=1 npx playwright test e2e/shots/chat.spec.ts       # one area's states
open e2e/shots/out/all/index.html                        # review them
cd .. && make ui-reset           # before committing: put the dist placeholder back
```

### Which Chromium

Playwright looks for the Chromium revision that `@playwright/test` pins. When
that one is not installed (offline, or a cache from an older version), point it
at any cached one; `lib/chromium.mjs` prints the newest, preferring the headless
shell:

```sh
export PLAYWRIGHT_CHROMIUM="$(node e2e/lib/chromium.mjs)"
npm run smoke
```

`playwright.config.ts` passes `PLAYWRIGHT_CHROMIUM` to the launcher as
`executablePath`. With network access, `npx playwright install chromium`
installs the pinned revision instead.

### Shooting the dev server

The screenshots use `SHOTS_BASE_URL` when it is set, so pages that exist only
in dev builds (the primitive gallery at `/dev/ui`) can be shot from
`npm run mock`:

```sh
npm run mock &                   # http://localhost:5173
SHOTS=1 SHOTS_BASE_URL=http://localhost:5173 npx playwright test e2e/shots/primitives.spec.ts
```

`SHOTS_DPR=2` doubles the pixel density of the images (default 1).

## Writing a chunk's shot spec

One file per chunk, named after it: `shots/shell.spec.ts`, `shots/setup.spec.ts`
and so on. List each state once; the helper captures it four times.

```ts
import { shots } from '../lib/shots';

shots('approvals', [
  { name: 'inbox', route: '/approvals' },
  { name: 'inbox-empty', route: '/approvals', mock: 'approvals-empty' },
  {
    name: 'deny-reason',
    route: '/approvals',
    setup: async (page) => {
      await page.getByRole('button', { name: 'Deny' }).first().click();
    },
  },
  { name: 'sign-in', route: '/login', mock: 'logged-out', probes: { leftEdge: false } },
]);
```

A state takes:

| Field | Meaning |
|---|---|
| `name` | file stem, unique in the chunk; images are `<name>-<1280\|360>-<light\|dark>[-full].png` |
| `route` | the address to open |
| `mock` | scenario switches for the fixture gateway (below) |
| `ready` | text to wait for (or a function) before capturing |
| `setup(page, { viewport, theme })` | interactions that put the page in the state: open a dialog, type, focus; runs for each of the four captures |
| `full` | `'auto'` (default) adds `-full.png` when the document is taller than the window; `true` or `false` force it |
| `probes` | switch probes off for this state, e.g. `{ leftEdge: false }` for setup and sign in |
| `only` | limit to some `viewports` (`desktop`, `phone`) or `themes` |

Every capture opens a fresh browser context: the colour scheme is set before
the first paint (`public/theme.js` resolves it), and the phone context is a
touch device with a coarse pointer, so 44 px targets and 16 px inputs show as
on a real phone. Each page is opened with `?mock=…` (empty when the state has
no scenarios), which resets the fixture gateway, so states never leak into each
other.

Output goes to `web/e2e/shots/out/<chunk>/` (gitignored): the PNGs, one JSON
file per state with its probe results, and `index.html`, which lays the four
captures of each state side by side with any probe problems under them. Link
or attach that set in the PR (G3).

`shots(chunk, states, { mode: 'report' })` records probe problems without
failing; only the F1 baseline uses it.

### The probes (G4, plus target size for G8)

`probe(page, options)` runs on every capture, and any spec can call it
directly. It returns one line per problem:

| Probe | Fails when |
|---|---|
| `overflow` | `document.scrollingElement.scrollWidth` is wider than the window (the page scrolls sideways) |
| `leftEdge` | at 1280 px, the first visible `h1` does not start at x = 408 ± 1 (the shared content edge, §3.3) |
| `minText` | any rendered text is under 12 px |
| `blur` | any element, or a dialog's `::backdrop`, has a `backdrop-filter` |
| `shadows` | a `box-shadow` sits outside a floating layer; a 0-blur ring on an element holding focus (the composer) is allowed |
| `nesting` | a box bordered on all four sides sits inside another one; form controls, buttons and links don't count, and anything inside an overlay is its own view |
| `targets` | a control is under 44 × 44 px with a coarse pointer, 24 × 24 px otherwise; links inside running text are exempt, and a checkbox or radio counts its label |

Floating layers are `dialog[open]`, `[role=dialog]`, `[role=alertdialog]`,
`[role=menu]`, `[role=listbox]`, `[role=tooltip]`, `[popover]` and anything
marked `data-floating`. Primitives that float without one of those roles (the
toast) carry `data-floating`.

## Plain copy (G6)

```ts
import { assertPlainCopy } from '../lib/copy';

await assertPlainCopy(page);
```

It reads what the page renders, including screen-reader-only text, `aria-label`,
`title`, `placeholder` and `alt`, plus the document title, and fails on the
words §14.3 keeps out of the main flow (the two G6 patterns), exclamation
marks, `usr_`/`ses_`/`run_`/`apr_`/`sch_` ids, "Untitled chat" and "never" as
a time. The rules live in `lib/copy-patterns.ts`, which C10's source scan
shares.

It skips what is not main-flow interface copy:

- `[data-details]`: Details, Technical details and Raw record content (the F2
  `Disclosure` sets it);
- `[data-copy-ignore]`: what people or models wrote and verbatim paths,
  commands and hosts;
- `.md`: rendered Markdown (model replies, notes, skills);
- every page under `/settings/advanced`.

Closed disclosures render nothing, so they are skipped on their own. `allow`
takes exact strings or patterns for the rare legitimate match; keep it short.
`plainCopyProblems(page)` returns the list instead of failing.

## Mock scenarios

States the default fixtures don't show are switched on with `mock` (sent as
`?mock=a,b:arg`): banners (`paused`, `sealed`, `provider-auth`, `safe-mode`,
`restart`), a member viewer (`member`), first run (`setup`), signed out
(`logged-out`), `slow` and `api-down`, setup detection (`detect-aws`,
`detect-ollama`, `detect-none`), provider check failures by code
(`provider-fail:out_of_credit`), empty lists (`chat-empty`, `approvals-empty`,
`activity-empty`), `spend-limit`, `sandbox-off`, `no-findings`. The full list
with descriptions is at `GET /api/mock/scenarios`; `web/mock/README.md` shows
how a chunk adds its own in its domain file.
