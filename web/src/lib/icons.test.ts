import { describe, expect, it } from 'vitest';
import { brands, paths, sprite } from './icons';

// docs/design/ui.md §3.7: every icon is one <symbol> in the sprite vite.config.ts writes into index.html.
describe('icon sprite', () => {
  const html = sprite();
  const doc = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html');
  const symbols = [...doc.querySelectorAll('#icons symbol')];

  it('holds one symbol per icon, with unique ids', () => {
    const names = [...Object.keys(paths), ...Object.keys(brands)];
    expect(symbols.map((s) => s.id)).toEqual(names.map((n) => `i-${n}`));
    expect(new Set(symbols.map((s) => s.id)).size).toBe(symbols.length);
  });

  it('draws the outline icons as one stroked path on the 24 grid, never filled', () => {
    for (const n of Object.keys(paths)) {
      const s = doc.getElementById(`i-${n}`)!;
      expect(s.getAttribute('viewBox'), n).toBe('0 0 24 24');
      const p = s.querySelectorAll('path');
      expect(p.length, n).toBe(1);
      expect(p[0].getAttribute('fill'), n).toBeNull();
      expect(p[0].getAttribute('vector-effect'), n).toBe('non-scaling-stroke');
    }
  });

  it('keeps the brand marks unstroked, so the outer stroke never outlines them', () => {
    for (const n of Object.keys(brands)) expect(doc.getElementById(`i-${n}`)!.getAttribute('stroke'), n).toBe('none');
    // Google's four colours stay its own; GitHub's mark follows the text colour.
    expect(doc.querySelectorAll('#i-google path[fill^="#"]')).toHaveLength(4);
    expect(doc.querySelector('#i-github path')!.getAttribute('fill')).toBe('currentColor');
  });

  it('carries nothing the page CSP or the size check refuse', () => {
    expect(html).not.toMatch(/style=|<style|<script|on[a-z]+=|https?:/);
    expect(doc.getElementById('icons')!.getAttribute('aria-hidden')).toBe('true');
  });
});
