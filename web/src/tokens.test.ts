import { describe, expect, it } from 'vitest';

// docs/design/ui.md §3.1: the palette and the contrast every pair must keep.
// The ratios in the table are rounded to two decimals; the 0.05 slack absorbs that.
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
    bg: '#fffdfa',
    sidebar: '#f4f0e8',
    surface: '#ffffff',
    raised: '#ffffff',
    subtle: '#f4eee4',
    hover: '#ece6db',
    selected: '#e9e2d5',
    border: '#ece5d9',
    'border-strong': '#ddd4c5',
    control: '#958e82',
    text: '#1f1d1a',
    muted: '#67615a',
    faint: '#716b61',
    accent: '#2a6a54',
    'accent-hover': '#225a47',
    'accent-active': '#1c4b3b',
    'accent-text': '#2a6a54',
    'accent-subtle': '#e6f0ea',
    'on-accent': '#ffffff',
    focus: '#2a6a54',
    danger: '#b42318',
    'danger-hover': '#9a1d14',
    'danger-text': '#b42318',
    'danger-subtle': '#fcedeb',
    warning: '#c2660f',
    'warning-text': '#97530f',
    'warning-subtle': '#fcefdf',
    'diff-add': '#eaf4ec',
    chart: '#958e82',
    scrim: 'rgb(31 24 14 / 0.4)',
  },
  dark: {
    bg: '#1a1917',
    sidebar: '#121110',
    surface: '#211f1d',
    raised: '#2a2826',
    subtle: '#272522',
    hover: '#2f2d2a',
    selected: '#35322e',
    border: '#2e2b28',
    'border-strong': '#3d3a36',
    control: '#726d65',
    text: '#eeebe5',
    muted: '#a39d93',
    faint: '#949087',
    accent: '#2f7d61',
    'accent-hover': '#296f56',
    'accent-active': '#235f4a',
    'accent-text': '#86cdae',
    'accent-subtle': '#1b2d25',
    'on-accent': '#ffffff',
    focus: '#86cdae',
    danger: '#b4372d',
    'danger-hover': '#a3332a',
    'danger-text': '#f0938a',
    'danger-subtle': '#3a221f',
    warning: '#e89a4c',
    'warning-text': '#f0b673',
    'warning-subtle': '#30251a',
    'diff-add': '#1e2e22',
    chart: '#726d65',
    scrim: 'rgb(0 0 0 / 0.6)',
  },
} as const;

// [foreground, background, ratio from §3.1, WCAG minimum for the use]
type Pair = [string, string, number, number];
const TEXT = 4.5;
const UI = 3;

const pairs: Record<'light' | 'dark', Pair[]> = {
  light: [
    ['text', 'bg', 16.56, TEXT],
    ['text', 'surface', 16.81, TEXT],
    ['text', 'sidebar', 14.79, TEXT],
    ['text', 'raised', 16.81, TEXT],
    ['text', 'subtle', 14.57, TEXT],
    ['text', 'selected', 13.05, TEXT],
    ['muted', 'bg', 6.02, TEXT],
    ['muted', 'surface', 6.11, TEXT],
    ['muted', 'sidebar', 5.38, TEXT],
    ['muted', 'raised', 6.11, TEXT],
    ['muted', 'subtle', 5.30, TEXT],
    ['muted', 'hover', 4.92, TEXT],
    ['muted', 'selected', 4.75, TEXT],
    ['muted', 'accent-subtle', 5.25, TEXT],
    ['faint', 'bg', 5.20, TEXT],
    ['faint', 'surface', 5.28, TEXT],
    ['faint', 'subtle', 4.57, TEXT],
    ['faint', 'sidebar', 4.64, TEXT],
    ['accent-text', 'bg', 6.29, TEXT],
    ['accent-text', 'surface', 6.39, TEXT],
    ['accent-text', 'sidebar', 5.62, TEXT],
    ['accent-text', 'raised', 6.39, TEXT],
    ['accent-text', 'accent-subtle', 5.48, TEXT],
    ['accent-text', 'subtle', 5.53, TEXT],
    ['on-accent', 'accent', 6.39, TEXT],
    ['on-accent', 'accent-hover', 8.01, TEXT],
    ['on-accent', 'accent-active', 9.91, TEXT],
    ['on-accent', 'danger', 6.57, TEXT],
    ['on-accent', 'danger-hover', 8.20, TEXT],
    ['danger-text', 'bg', 6.47, TEXT],
    ['danger-text', 'surface', 6.57, TEXT],
    ['danger-text', 'danger-subtle', 5.78, TEXT],
    ['text', 'danger-subtle', 14.77, TEXT],
    ['warning-text', 'bg', 5.81, TEXT],
    ['warning-text', 'surface', 5.90, TEXT],
    ['warning-text', 'sidebar', 5.19, TEXT],
    ['warning-text', 'raised', 5.90, TEXT],
    ['warning-text', 'subtle', 5.11, TEXT],
    ['warning-text', 'warning-subtle', 5.21, TEXT],
    ['text', 'warning-subtle', 14.85, TEXT],
    ['text', 'accent-subtle', 14.43, TEXT],
    ['text', 'diff-add', 14.93, TEXT],
    ['warning', 'sidebar', 3.55, UI],
    ['warning', 'raised', 4.04, UI],
    ['warning', 'hover', 3.25, UI],
    ['warning', 'selected', 3.13, UI],
    ['warning', 'bg', 3.98, UI],
    ['control', 'bg', 3.20, UI],
    ['control', 'surface', 3.25, UI],
    ['chart', 'bg', 3.20, UI],
    ['accent', 'bg', 6.29, UI],
    ['accent', 'surface', 6.39, UI],
    ['focus', 'bg', 6.29, UI],
    ['focus', 'sidebar', 5.62, UI],
    ['focus', 'surface', 6.39, UI],
  ],
  dark: [
    ['text', 'bg', 14.77, TEXT],
    ['text', 'surface', 13.81, TEXT],
    ['text', 'sidebar', 15.85, TEXT],
    ['text', 'raised', 12.34, TEXT],
    ['text', 'subtle', 12.85, TEXT],
    ['text', 'selected', 10.72, TEXT],
    ['muted', 'bg', 6.53, TEXT],
    ['muted', 'surface', 6.10, TEXT],
    ['muted', 'sidebar', 7.01, TEXT],
    ['muted', 'raised', 5.46, TEXT],
    ['muted', 'subtle', 5.68, TEXT],
    ['muted', 'hover', 5.10, TEXT],
    ['muted', 'selected', 4.74, TEXT],
    ['muted', 'accent-subtle', 5.39, TEXT],
    ['faint', 'bg', 5.52, TEXT],
    ['faint', 'surface', 5.16, TEXT],
    ['faint', 'subtle', 4.80, TEXT],
    ['faint', 'sidebar', 5.93, TEXT],
    ['accent-text', 'bg', 9.50, TEXT],
    ['accent-text', 'surface', 8.88, TEXT],
    ['accent-text', 'sidebar', 10.20, TEXT],
    ['accent-text', 'raised', 7.94, TEXT],
    ['accent-text', 'accent-subtle', 7.84, TEXT],
    ['accent-text', 'subtle', 8.27, TEXT],
    ['on-accent', 'accent', 4.97, TEXT],
    ['on-accent', 'accent-hover', 6.00, TEXT],
    ['on-accent', 'accent-active', 7.48, TEXT],
    ['on-accent', 'danger', 5.96, TEXT],
    ['on-accent', 'danger-hover', 6.85, TEXT],
    ['danger-text', 'bg', 7.73, TEXT],
    ['danger-text', 'surface', 7.23, TEXT],
    ['danger-text', 'danger-subtle', 6.47, TEXT],
    ['text', 'danger-subtle', 12.36, TEXT],
    ['warning-text', 'bg', 9.74, TEXT],
    ['warning-text', 'surface', 9.11, TEXT],
    ['warning-text', 'sidebar', 10.46, TEXT],
    ['warning-text', 'raised', 8.14, TEXT],
    ['warning-text', 'subtle', 8.47, TEXT],
    ['warning-text', 'warning-subtle', 8.29, TEXT],
    ['text', 'warning-subtle', 12.56, TEXT],
    ['text', 'accent-subtle', 12.18, TEXT],
    ['text', 'diff-add', 12.01, TEXT],
    ['warning', 'sidebar', 8.23, UI],
    ['warning', 'raised', 6.40, UI],
    ['warning', 'hover', 5.99, UI],
    ['warning', 'selected', 5.56, UI],
    ['warning', 'bg', 7.66, UI],
    ['control', 'bg', 3.42, UI],
    ['control', 'surface', 3.20, UI],
    ['chart', 'bg', 3.42, UI],
    ['accent', 'bg', 3.54, UI],
    ['accent', 'surface', 3.31, UI],
    ['focus', 'bg', 9.50, UI],
    ['focus', 'sidebar', 10.20, UI],
    ['focus', 'surface', 8.88, UI],
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

  it("resets Tailwind's colours, sizes, radii and shadows so only these tokens compile", () => {
    const theme = block(/@theme\s*\{/);
    for (const ns of ['color', 'text', 'radius', 'shadow']) expect(theme, `--${ns}-*: initial`).toMatch(new RegExp(`--${ns}-\\*:\\s*initial;`));
    expect(Object.entries(light).filter(([, v]) => v.startsWith('var(')), 'legacy aliases').toEqual([]);
  });

  it('names every elevation, with its own dark value (§3.4)', () => {
    const theme = block(/@theme\s*\{/);
    const lightBlock = block(/:root\s*\{/);
    const darkBlock = block(/:root\[data-theme='dark'\]\s*\{/);
    for (const name of ['raised', 'card', 'primary', 'focus', 'frame', 'pop', 'overlay']) {
      // Tailwind would inline a literal into the utility, and the dark value would never apply.
      expect(theme, `--shadow-${name}`).toMatch(new RegExp(`--shadow-${name}: var\\(--elevation-${name}\\);`));
      expect(lightBlock, `light --elevation-${name}`).toMatch(new RegExp(`--elevation-${name}:`));
      expect(darkBlock, `dark --elevation-${name}`).toMatch(new RegExp(`--elevation-${name}:`));
    }
    expect(darkBlock).toMatch(/--elevation-overlay:\s*0 0 0 1px #34312d, 0 8px 24px rgb\(0 0 0 \/ 0\.5\);/);
  });

  it('keeps the radii at 6, 10 and 14 px, and 20 for the sign-in card (§3.4)', () => {
    const theme = block(/@theme\s*\{/);
    const radii = Object.fromEntries([...theme.matchAll(/--radius-([a-z]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
    expect(radii).toEqual({ sm: '6px', md: '10px', lg: '14px', xl: '20px' });
  });

  it('uses the system font stack and loads no web fonts (§3.2)', () => {
    expect(block(/@theme\s*\{/)).toMatch(/--font-sans:\s*ui-sans-serif, system-ui,/);
    expect(css).not.toMatch(/@font-face|@import\s+url\(/);
  });
});
