// Screenshot sets for review (docs/design/ui.md §18.2 G3) with the layout
// probes of G4 run on every capture. A chunk's spec lists its states once;
// each state is captured at 1280 × 800 and 360 × 780 in light and dark (plus
// `-full` when the page is taller than the window) into
// web/e2e/shots/out/<chunk>/, with an index.html to review them side by side.
// See web/e2e/README.md for how to run and write them.
import { expect, test, type Browser, type Page, type TestInfo } from '@playwright/test';
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const OUT = fileURLToPath(new URL('../shots/out/', import.meta.url));

export const viewports = {
  desktop: { name: '1280', width: 1280, height: 800, coarse: false },
  // A phone: touch and a coarse pointer, so 44 px targets and 16 px inputs show.
  phone: { name: '360', width: 360, height: 780, coarse: true },
} as const;
export type ViewportName = keyof typeof viewports;
export type Viewport = (typeof viewports)[ViewportName];
export const themes = ['light', 'dark'] as const;
export type Theme = (typeof themes)[number];

/** The content edge every column page and the chat share at 1280 px (§3.3). */
export const LEFT_EDGE = 408;

export interface ProbeOptions {
  /** No sideways page scroll (`scrollWidth <= width`). */
  overflow?: boolean;
  /** At 1280 px the first visible h1 starts at x = 408 ± 1. Turn off for pages outside the column (setup, sign in). */
  leftEdge?: boolean;
  /** No rendered text under 12 px. */
  minText?: boolean;
  /** No backdrop-filter anywhere. */
  blur?: boolean;
  /** box-shadow only on floating layers (dialogs, menus, popovers, tooltips, [data-floating]) and focus rings. */
  shadows?: boolean;
  /** No bordered box inside another one, except inside overlays; form controls and buttons don't count. */
  nesting?: boolean;
  /**
   * Controls are at least 44 × 44 px under a coarse pointer and 24 × 24 px otherwise (§16, G8).
   * Links inside running text are exempt (WCAG 2.5.8); a checkbox or radio counts its label.
   */
  targets?: boolean;
}

export const allProbes: Required<ProbeOptions> = { overflow: true, leftEdge: true, minText: true, blur: true, shadows: true, nesting: true, targets: true };

export interface ShotState {
  /** File-name stem, kebab case, unique within the chunk: "inbox-empty". */
  name: string;
  /** The address to open. */
  route: string;
  /** Scenario switches for the mock gateway (web/mock/README.md), sent as ?mock=. */
  mock?: string | string[];
  /** Waits for this text (or runs this) after load, before setup. */
  ready?: string | ((page: Page) => Promise<unknown>);
  /** Puts the page in the state to capture (open a dialog, type, focus). Runs for every viewport and theme. */
  setup?: (page: Page, at: { viewport: Viewport; theme: Theme }) => Promise<unknown>;
  /** 'auto' (default): also `-full.png` when the page is taller than the window. */
  full?: boolean | 'auto';
  /** Probe switches, merged over the spec's defaults. */
  probes?: ProbeOptions;
  only?: { viewports?: ViewportName[]; themes?: Theme[] };
}

export interface ShotsOptions {
  /** 'assert' (default) fails on probe problems; 'report' only records them (baselines). */
  mode?: 'assert' | 'report';
  probes?: ProbeOptions;
}

export interface Capture {
  file: string;
  full?: string;
  viewport: string;
  theme: Theme;
  problems: string[];
}

const FLOATING = 'dialog[open], [role=dialog], [role=alertdialog], [role=menu], [role=listbox], [role=tooltip], [popover], [data-floating]';
const CONTROLS =
  'input, select, textarea, button, a, summary, option, kbd, img, svg, hr, td, th, progress, meter, iframe, video, [role=button], [role=switch], [role=checkbox], [role=radio], [role=tab], [role=option], [role=menuitem], [role=menuitemradio], [role=combobox], [role=slider]';

const TARGETS = 'button, a[href], input:not([type=hidden]), select, textarea, summary, [role=button], [role=switch], [role=checkbox], [role=radio], [role=tab], [role=menuitem], [role=menuitemradio], [role=option]';

/** Runs the G4 probes (and the G8 target size) on the page as it is; returns one line per problem. */
export async function probe(page: Page, opts: ProbeOptions = allProbes): Promise<string[]> {
  return page.evaluate(
    ({ opts, edge, floating, controls, targets }) => {
      const out: string[] = [];
      const width = window.innerWidth;
      const name = (el: Element) => {
        const cls = [...el.classList].slice(0, 3).join('.');
        const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
        return `<${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ''}${cls ? `.${cls}` : ''}>${text ? ` “${text}”` : ''}`;
      };
      // Visually hidden (sr-only) content clips itself away rather than collapsing, so its box can still be large.
      const clipped = (el: Element) => {
        for (let e: Element | null = el; e; e = e.parentElement) {
          const cs = getComputedStyle(e);
          if (cs.clipPath === 'inset(50%)' || cs.clip === 'rect(0px, 0px, 0px, 0px)') return true;
        }
        return false;
      };
      const shown = (el: Element) => {
        const r = el.getBoundingClientRect();
        return r.width > 1 && r.height > 1 && el.checkVisibility({ visibilityProperty: true, opacityProperty: true }) && !clipped(el);
      };
      const all = [...document.body.querySelectorAll('*')];

      if (opts.overflow) {
        const w = document.scrollingElement?.scrollWidth ?? 0;
        if (w > width) out.push(`overflow: the page scrolls sideways (scrollWidth ${w} > ${width})`);
      }

      if (opts.leftEdge && width >= 1280) {
        const h1 = [...document.querySelectorAll('h1')].find(shown);
        if (!h1) out.push('left-edge: no visible h1');
        else {
          const x = Math.round(h1.getBoundingClientRect().left * 10) / 10;
          if (Math.abs(x - edge) > 1) out.push(`left-edge: the h1 starts at x = ${x}, not ${edge} (${name(h1)})`);
        }
      }

      if (opts.minText) {
        const small = new Map<string, string>();
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = walker.nextNode(); n; n = walker.nextNode()) {
          const el = n.parentElement;
          if (!el || !n.textContent?.trim() || el.closest('option, script, style') || !shown(el)) continue;
          const size = parseFloat(getComputedStyle(el).fontSize);
          if (size < 12) small.set(`${size}px ${name(el)}`, '');
        }
        for (const k of [...small.keys()].slice(0, 10)) out.push(`min-text: ${k}`);
        if (small.size > 10) out.push(`min-text: … and ${small.size - 10} more`);
      }

      if (opts.blur) {
        for (const el of [...all, ...document.querySelectorAll('dialog')]) {
          const cs = getComputedStyle(el);
          const filters = [cs.backdropFilter, cs.getPropertyValue('-webkit-backdrop-filter')];
          if (el.tagName === 'DIALOG') filters.push(getComputedStyle(el, '::backdrop').backdropFilter);
          if (filters.some((f) => f && f !== 'none')) {
            out.push(`blur: backdrop-filter on ${name(el)}`);
            break;
          }
        }
      }

      // A shadow layer counts when its colour is not transparent and it has
      // any offset, blur or spread.
      const layers = (v: string) => v.split(/,(?![^(]*\))/).map((s) => s.trim());
      const alpha = (layer: string) => {
        const c = /(rgba?|oklab|oklch|lab|lch|hsla?|color)\(([^)]*)\)/.exec(layer);
        if (!c) return layer.includes('transparent') ? 0 : 1;
        const slash = c[2].split('/');
        if (slash.length > 1) return parseFloat(slash[1]);
        const parts = c[2].split(',');
        return parts.length === 4 ? parseFloat(parts[3]) : 1;
      };
      const lengths = (layer: string) => (layer.replace(/\([^)]*\)/g, '').match(/-?[\d.]+px/g) ?? []).map(parseFloat);
      const visibleShadow = (v: string) => v !== 'none' && layers(v).some((l) => alpha(l) > 0 && lengths(l).some((n) => n !== 0));
      const ring = (v: string) => layers(v).every((l) => alpha(l) === 0 || lengths(l).slice(0, 3).every((n) => n === 0));

      if (opts.shadows) {
        const seen = new Set<string>();
        for (const el of all) {
          const v = getComputedStyle(el).boxShadow;
          if (!visibleShadow(v) || el.closest(floating) || !shown(el)) continue;
          if (ring(v) && el.matches(':focus-within')) continue;
          const k = name(el);
          if (!seen.has(k)) out.push(`shadow: box-shadow outside a floating layer on ${k}`);
          seen.add(k);
          if (seen.size >= 10) break;
        }
      }

      if (opts.nesting) {
        const boxed = (el: Element) => {
          const cs = getComputedStyle(el);
          return (['Top', 'Right', 'Bottom', 'Left'] as const).every((s) => {
            const w = parseFloat(cs.getPropertyValue(`border-${s.toLowerCase()}-width`));
            const st = cs.getPropertyValue(`border-${s.toLowerCase()}-style`);
            return w > 0 && st !== 'none' && st !== 'hidden' && alpha(cs.getPropertyValue(`border-${s.toLowerCase()}-color`)) > 0;
          });
        };
        const seen = new Set<string>();
        for (const el of all) {
          if (el.matches(controls) || el.closest(controls) || !boxed(el) || !shown(el)) continue;
          for (let up = el.parentElement; up && up !== document.body; up = up.parentElement) {
            if (up.matches(floating)) break;
            if (!up.matches(controls) && boxed(up)) {
              const k = `${name(el)} inside ${name(up)}`;
              if (!seen.has(k)) out.push(`nesting: bordered ${k}`);
              seen.add(k);
              break;
            }
          }
          if (seen.size >= 10) break;
        }
      }

      if (opts.targets) {
        const min = matchMedia('(pointer: coarse)').matches ? 44 : 24;
        const small: string[] = [];
        for (const el of document.body.querySelectorAll<HTMLElement>(targets)) {
          if (!shown(el) || el.closest('[inert]')) continue;
          // A link inside a sentence takes the line's height (WCAG 2.5.8 inline exception).
          if (el.tagName === 'A' && getComputedStyle(el).display === 'inline' && (el.parentElement?.textContent ?? '').trim() !== (el.textContent ?? '').trim()) continue;
          let r = el.getBoundingClientRect();
          if (el instanceof HTMLInputElement && (el.type === 'checkbox' || el.type === 'radio')) {
            const label = el.closest('label') ?? (el.id ? document.querySelector(`label[for="${CSS.escape(el.id)}"]`) : null);
            if (label) r = label.getBoundingClientRect();
          }
          if (Math.round(r.width) < min || Math.round(r.height) < min) small.push(`${Math.round(r.width)}×${Math.round(r.height)} ${name(el)}`);
        }
        for (const s of small.slice(0, 10)) out.push(`targets: under ${min} px: ${s}`);
        if (small.length > 10) out.push(`targets: … and ${small.length - 10} more`);
      }
      return out;
    },
    { opts: { ...allProbes, ...opts }, edge: LEFT_EDGE, floating: FLOATING, controls: CONTROLS, targets: TARGETS },
  );
}

function withMock(route: string, mock: ShotState['mock']): string {
  const url = new URL(route, 'http://x');
  // Always sent, even empty: the mock resets its fixtures on ?mock=, so every capture starts clean.
  url.searchParams.set('mock', Array.isArray(mock) ? mock.join(',') : (mock ?? ''));
  return url.pathname + url.search + url.hash;
}

/** Opens a fresh context for one viewport and theme (colour scheme and pointer are fixed per context). */
export async function openPage(browser: Browser, info: TestInfo, viewport: Viewport, theme: Theme): Promise<Page> {
  const ctx = await browser.newContext({
    baseURL: process.env.SHOTS_BASE_URL ?? (info.project.use.baseURL as string | undefined),
    viewport: { width: viewport.width, height: viewport.height },
    deviceScaleFactor: Number(process.env.SHOTS_DPR ?? 1),
    colorScheme: theme,
    hasTouch: viewport.coarse,
    isMobile: viewport.coarse,
  });
  return ctx.newPage();
}

/** Loads a state, captures it and probes it. */
export async function capture(page: Page, chunk: string, state: ShotState, viewport: Viewport, theme: Theme, probes: ProbeOptions): Promise<Capture> {
  await page.goto(withMock(state.route, state.mock));
  await page.waitForLoadState('networkidle');
  await page.evaluate(() => document.fonts.ready);
  if (typeof state.ready === 'string') await page.getByText(state.ready, { exact: false }).first().waitFor();
  else if (state.ready) await state.ready(page);
  if (state.setup) {
    await state.setup(page, { viewport, theme });
    await page.waitForLoadState('networkidle');
  }
  const dir = join(OUT, chunk);
  mkdirSync(dir, { recursive: true });
  const stem = `${state.name}-${viewport.name}-${theme}`;
  const shot = { animations: 'disabled', caret: 'hide' } as const;
  await page.screenshot({ ...shot, path: join(dir, `${stem}.png`) });
  const tall = await page.evaluate(() => (document.scrollingElement?.scrollHeight ?? 0) > window.innerHeight + 1);
  let full: string | undefined;
  if (state.full === true || (state.full !== false && tall)) {
    full = `${stem}-full.png`;
    await page.screenshot({ ...shot, path: join(dir, full), fullPage: true });
  }
  const problems = await probe(page, { ...probes, ...state.probes });
  return { file: `${stem}.png`, full, viewport: viewport.name, theme, problems };
}

/**
 * Declares one test per state. Shot specs run only with SHOTS=1, so the
 * smoke run stays fast: `SHOTS=1 npx playwright test e2e/shots/<chunk>.spec.ts`.
 */
export function shots(chunk: string, states: ShotState[], opts: ShotsOptions = {}) {
  const mode = opts.mode ?? 'assert';
  test.describe(`shots: ${chunk}`, () => {
    test.skip(!process.env.SHOTS, 'screenshots run with SHOTS=1');
    test.describe.configure({ mode: 'default' });

    for (const state of states) {
      test(state.name, async ({ browser }, info) => {
        test.setTimeout(120_000);
        const results: Capture[] = [];
        for (const vp of state.only?.viewports ?? (Object.keys(viewports) as ViewportName[])) {
          for (const theme of state.only?.themes ?? themes) {
            const page = await openPage(browser, info, viewports[vp], theme);
            try {
              results.push(await capture(page, chunk, state, viewports[vp], theme, { ...allProbes, ...opts.probes }));
            } finally {
              await page.context().close();
            }
          }
        }
        writeFileSync(join(OUT, chunk, `${state.name}.json`), JSON.stringify({ state: state.name, route: state.route, mock: state.mock ?? '', captures: results }, null, 2));
        if (mode === 'assert') for (const r of results) expect.soft(r.problems, `${state.name} at ${r.viewport} ${r.theme}`).toEqual([]);
      });
    }

    test.afterAll(() => writeIndex(chunk));
  });
}

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] ?? c);

/** Writes out/<chunk>/index.html from the captures on disk. */
export function writeIndex(chunk: string) {
  const dir = join(OUT, chunk);
  let files: string[];
  try {
    files = readdirSync(dir).filter((f: string) => f.endsWith('.json'));
  } catch {
    return;
  }
  const sections = files.sort().map((f) => {
    const s = JSON.parse(readFileSync(join(dir, f), 'utf8')) as { state: string; route: string; mock: string; captures: Capture[] };
    const cells = s.captures
      .map((c) => {
        const issues = c.problems.length ? `<ul>${c.problems.map((p) => `<li>${esc(p)}</li>`).join('')}</ul>` : '<p class="ok">Probes clean</p>';
        const full = c.full ? ` · <a href="${esc(c.full)}">full page</a>` : '';
        return `<figure><a href="${esc(c.file)}"><img loading="lazy" src="${esc(c.file)}" alt="${esc(`${s.state} ${c.viewport} ${c.theme}`)}"></a><figcaption>${c.viewport} · ${c.theme}${full}</figcaption>${issues}</figure>`;
      })
      .join('');
    return `<section id="${esc(s.state)}"><h2>${esc(s.state)}</h2><p><code>${esc(s.route)}</code>${s.mock ? ` · ?mock=${esc(s.mock)}` : ''}</p><div class="grid">${cells}</div></section>`;
  });
  const css =
    'body{font:14px/1.4 system-ui,sans-serif;margin:24px;color:#1e1d1a}h2{font-size:16px;margin:32px 0 4px}.grid{display:grid;grid-template-columns:2fr 2fr 1fr 1fr;gap:16px;align-items:start}figure{min-width:0}figure{margin:0}img{width:100%;border:1px solid #d3cfc6}figcaption{color:#625e56;margin-top:4px}ul{color:#b42318;padding-left:18px;margin:4px 0}.ok{color:#625e56;margin:4px 0}';
  writeFileSync(join(dir, 'index.html'), `<!doctype html><meta charset="utf-8"><title>${esc(chunk)} shots</title><style>${css}</style><h1>${esc(chunk)}</h1>${sections.join('')}`);
}
