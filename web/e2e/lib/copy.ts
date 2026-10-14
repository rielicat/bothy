// assertPlainCopy (docs/design/ui.md §18.2 G6): the words people read on a
// page follow the copy guide. It reads what the page renders (text, sr-only
// text, labels, placeholders, titles), not the source, so it also catches
// strings that come from the server.
//
// Out of scope by design, and skipped:
// - Details, Technical details and Raw record: mark the disclosure content
//   with `data-details` (the F2 Disclosure does this for variant 'details').
// - Content that is not interface copy: model replies (`.md`), what people
//   typed, verbatim paths and commands. Mark them `data-copy-ignore`.
// - Settings → Advanced (/settings/advanced…), where config keys belong.
import { expect, type Page } from '@playwright/test';
import { COPY_RULES, copyMatches, type CopyRule } from './copy-patterns';

export const DEFAULT_IGNORE = ['[data-details]', '[data-copy-ignore]', '.md', 'script', 'style', 'noscript', 'template'];

export interface CopyOptions {
  /** More selectors whose subtree is skipped. */
  ignore?: string[];
  /** Exact strings or patterns allowed despite a rule (keep this list short). */
  allow?: (string | RegExp)[];
  rules?: CopyRule[];
}

export interface CopyProblem {
  rule: string;
  match: string;
  text: string;
  where: string;
}

interface Found {
  text: string;
  where: string;
}

/** Collects the page's interface strings in the browser. */
async function collect(page: Page, ignore: string[]): Promise<Found[]> {
  return page.evaluate((skip) => {
    const sel = skip.join(',');
    const out: { text: string; where: string }[] = [];
    const where = (el: Element | null): string => {
      const parts: string[] = [];
      for (let e = el; e && e !== document.body && parts.length < 3; e = e.parentElement) {
        const cls = [...e.classList].slice(0, 2).join('.');
        parts.unshift(e.tagName.toLowerCase() + (e.id ? `#${e.id}` : '') + (cls ? `.${cls}` : ''));
      }
      return parts.join(' > ');
    };
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const el = n.parentElement;
      const text = n.textContent?.trim();
      if (!el || !text || el.closest(sel)) continue;
      // Closed <details>, display:none and the like render nothing.
      if (!el.checkVisibility()) continue;
      out.push({ text, where: where(el) });
    }
    for (const el of document.body.querySelectorAll('[aria-label], [aria-description], [title], [placeholder], [alt], input[type=button], input[type=submit]')) {
      if (el.closest(sel)) continue;
      for (const a of ['aria-label', 'aria-description', 'title', 'placeholder', 'alt', 'value']) {
        const v = el.getAttribute(a)?.trim();
        if (v && (a !== 'value' || el.matches('input[type=button], input[type=submit]'))) out.push({ text: v, where: `${where(el)} [${a}]` });
      }
    }
    if (document.title) out.push({ text: document.title, where: 'document.title' });
    return out;
  }, ignore);
}

/** Every copy-guide problem on the current page (empty on Advanced pages). */
export async function plainCopyProblems(page: Page, opts: CopyOptions = {}): Promise<CopyProblem[]> {
  if (new URL(page.url()).pathname.startsWith('/settings/advanced')) return [];
  const found = await collect(page, [...DEFAULT_IGNORE, ...(opts.ignore ?? [])]);
  const allowed = (s: string) => (opts.allow ?? []).some((a) => (typeof a === 'string' ? a === s : a.test(s)));
  const out: CopyProblem[] = [];
  const seen = new Set<string>();
  for (const f of found) {
    if (allowed(f.text)) continue;
    for (const m of copyMatches(f.text, opts.rules ?? COPY_RULES)) {
      const key = `${m.rule}|${f.text}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({ ...m, text: f.text.length > 120 ? `${f.text.slice(0, 117)}…` : f.text, where: f.where });
    }
  }
  return out;
}

/** Fails the test with every problem listed (rule, the matched text, where). */
export async function assertPlainCopy(page: Page, opts: CopyOptions = {}): Promise<void> {
  const problems = await plainCopyProblems(page, opts);
  expect(
    problems.map((p) => `${p.rule}: “${p.match}” in “${p.text}” (${p.where})`),
    `plain copy on ${new URL(page.url()).pathname}`,
  ).toEqual([]);
}
