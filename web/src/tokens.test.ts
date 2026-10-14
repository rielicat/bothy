import { describe, expect, it } from 'vitest';

// docs/design/ui.md §3.1: the palette and the contrast every pair must keep.
// The ratios in the plan are rounded to one decimal, hence the 0.05 slack.
//
// Vitest stubs CSS imports (even ?raw), so the file is read from disk. The
// package has no @types/node, hence the untyped imports; the path stays a
// string because jsdom's URL is not the one fs accepts.
const fs = (await import('node:fs' as string)) as { readFileSync(path: string, enc: 'utf8'): string };
const url = (await import('node:url' as string)) as { fileURLToPath(u: string): string };
const css = fs.readFileSync(url.fileURLToPath(import.meta.url).replace(/tokens\.test\.ts$/, 'app.css'), 'utf8');

function block(start: RegExp): string {
  const m = start.exec(css);
  if (!m) throw new Error(`no block for ${start}`);
  let depth = 0;
  for (let i = m.index + m[0].length - 1; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}' && --depth === 0) return css.slice(m.index + m[0].length, i);
  }
  throw new Error(`unclosed block for ${start}`);
}

function colors(src: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of src.matchAll(/--color-([a-z0-9-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim().toLowerCase();
  return out;
}

const light = colors(block(/@theme\s*\{/));
const dark = { ...light, ...colors(block(/:root\[data-theme='dark'\]\s*\{/)) };

function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6})$/.exec(hex);
  if (!m) throw new Error(`not a 6-digit hex colour: ${hex}`);
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(m[1].slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const palette = {
  light: {
    bg: '#ffffff',
    sidebar: '#f7f6f3',
    surface: '#ffffff',
    subtle: '#f4f3ef',
    hover: '#eceae5',
    selected: '#e7e5df',
    border: '#e6e3dc',
    'border-strong': '#d3cfc6',
    control: '#8f8a80',
    text: '#1e1d1a',
    muted: '#625e56',
    faint: '#6f6b62',
    accent: '#2a6a54',
    'accent-hover': '#225a47',
    'accent-active': '#1c4b3b',
    'accent-text': '#2a6a54',
    'accent-subtle': '#e7f0ec',
    'on-accent': '#ffffff',
    focus: '#2a6a54',
    danger: '#b42318',
    'danger-hover': '#9a1d14',
    'danger-text': '#b42318',
    'danger-subtle': '#fcedeb',
    'warning-text': '#8a5300',
    'warning-subtle': '#fbf1db',
    'diff-add': '#eaf4ec',
    chart: '#8f8a80',
    scrim: 'rgb(20 19 17 / 0.4)',
  },
  dark: {
    bg: '#1a1918',
    sidebar: '#201f1d',
    surface: '#232220',
    subtle: '#2a2927',
    hover: '#32302d',
    selected: '#383632',
    border: '#363431',
    'border-strong': '#4a4743',
    control: '#7a766e',
    text: '#eceae5',
    muted: '#a8a49b',
    faint: '#949087',
    accent: '#2e7a5f',
    'accent-hover': '#286c54',
    'accent-active': '#225c48',
    'accent-text': '#7cc4a6',
    'accent-subtle': '#1f2f29',
    'on-accent': '#ffffff',
    focus: '#7cc4a6',
    danger: '#b4372d',
    'danger-hover': '#a3332a',
    'danger-text': '#f0938a',
    'danger-subtle': '#3a221f',
    'warning-text': '#e0b25e',
    'warning-subtle': '#30281a',
    'diff-add': '#1e2e22',
    chart: '#7a766e',
    scrim: 'rgb(0 0 0 / 0.55)',
  },
} as const;

// [foreground, background, ratio from §3.1, WCAG minimum for the use]
type Pair = [string, string, number, number];
const TEXT = 4.5;
const UI = 3;

const pairs: Record<'light' | 'dark', Pair[]> = {
  light: [
    ['text', 'selected', 13.4, TEXT],
    ['muted', 'selected', 5.1, TEXT],
    ['control', 'bg', 3.4, UI],
    ['control', 'sidebar', 3.2, UI],
    ['text', 'bg', 16.9, TEXT],
    ['muted', 'bg', 6.5, TEXT],
    ['muted', 'sidebar', 6.0, TEXT],
    ['faint', 'bg', 5.3, TEXT],
    ['on-accent', 'accent', 6.4, TEXT],
    ['on-accent', 'accent-hover', 8.0, TEXT],
    ['on-accent', 'accent-active', 9.9, TEXT],
    ['accent-text', 'bg', 6.4, TEXT],
    ['accent-text', 'accent-subtle', 5.5, TEXT],
    ['focus', 'bg', 6.4, UI],
    ['on-accent', 'danger', 6.6, TEXT],
    ['on-accent', 'danger-hover', 8.2, TEXT],
    ['danger-text', 'bg', 6.6, TEXT],
    ['danger-text', 'danger-subtle', 5.8, TEXT],
    ['text', 'danger-subtle', 14.8, TEXT],
    ['warning-text', 'bg', 6.3, TEXT],
    ['warning-text', 'warning-subtle', 5.6, TEXT],
    ['text', 'warning-subtle', 15.0, TEXT],
    ['text', 'diff-add', 15.0, TEXT],
    ['chart', 'bg', 3.4, UI],
  ],
  dark: [
    ['text', 'selected', 10.0, TEXT],
    ['muted', 'selected', 4.9, TEXT],
    ['control', 'bg', 3.9, UI],
    ['control', 'surface', 3.5, UI],
    ['text', 'bg', 14.6, TEXT],
    ['text', 'surface', 13.2, TEXT],
    ['muted', 'bg', 7.1, TEXT],
    ['muted', 'surface', 6.4, TEXT],
    ['faint', 'bg', 5.5, TEXT],
    ['faint', 'surface', 5.0, TEXT],
    ['on-accent', 'accent', 5.2, TEXT],
    ['accent', 'bg', 3.4, UI],
    ['on-accent', 'accent-hover', 6.2, TEXT],
    ['on-accent', 'accent-active', 7.8, TEXT],
    ['accent-text', 'bg', 8.6, TEXT],
    ['accent-text', 'accent-subtle', 6.9, TEXT],
    ['focus', 'bg', 8.6, UI],
    ['on-accent', 'danger', 6.0, TEXT],
    ['on-accent', 'danger-hover', 6.9, TEXT],
    ['danger-text', 'bg', 7.7, TEXT],
    ['danger-text', 'danger-subtle', 6.5, TEXT],
    ['text', 'danger-subtle', 12.2, TEXT],
    ['warning-text', 'bg', 8.9, TEXT],
    ['warning-text', 'warning-subtle', 7.4, TEXT],
    ['text', 'diff-add', 11.9, TEXT],
    ['chart', 'bg', 3.9, UI],
  ],
};

describe('design tokens (§3.1)', () => {
  for (const theme of ['light', 'dark'] as const) {
    const actual = theme === 'light' ? light : dark;

    it(`defines the ${theme} palette exactly`, () => {
      for (const [name, hex] of Object.entries(palette[theme])) expect(actual[name], `--color-${name}`).toBe(hex);
    });

    for (const [fg, bg, want, min] of pairs[theme]) {
      it(`${theme}: ${fg} on ${bg} is ${want}:1`, () => {
        const got = ratio(actual[fg], actual[bg]);
        expect(Math.abs(got - want), `${got.toFixed(3)}:1`).toBeLessThanOrEqual(0.05);
        expect(got).toBeGreaterThanOrEqual(min);
      });
    }
  }

  it('overrides every theme colour for dark except the white on accent and the aliases', () => {
    const overridden = Object.keys(colors(block(/:root\[data-theme='dark'\]\s*\{/)));
    const own = Object.keys(palette.light).filter((n) => n !== 'on-accent');
    expect(overridden.sort()).toEqual(own.sort());
  });

  it('points every legacy alias at a v0.2 token', () => {
    const aliases = Object.entries(light).filter(([, v]) => v.startsWith('var('));
    expect(aliases.length).toBeGreaterThan(0);
    for (const [name, v] of aliases) {
      const target = /^var\(--color-([a-z0-9-]+)\)$/.exec(v)?.[1];
      expect(target && target in palette.light, `--color-${name}: ${v}`).toBe(true);
    }
  });

  it('gives floating layers their own dark shadow', () => {
    expect(block(/:root\[data-theme='dark'\]\s*\{/)).toMatch(/--shadow-overlay:\s*0 0 0 1px #363431, 0 8px 24px rgb\(0 0 0 \/ 0\.5\);/);
  });
});
