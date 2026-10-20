import { describe, expect, it } from 'vitest';
import { codepoint, hasHidden, punycode, splitUrl, visualize } from './text';

describe('visualize', () => {
  it('marks bidi controls and zero-width characters', () => {
    expect(visualize('rm -rf \u202E/ cod.exe')).toEqual([
      { text: 'rm -rf ', hidden: false },
      { text: 'U+202E', hidden: true },
      { text: '/ cod.exe', hidden: false },
    ]);
    expect(visualize('a\u200Bb\uFEFF').filter((s) => s.hidden).map((s) => s.text)).toEqual(['U+200B', 'U+FEFF']);
    expect(visualize('x\u0007y\u2066z').filter((s) => s.hidden).map((s) => s.text)).toEqual(['U+0007', 'U+2066']);
  });

  it('leaves ordinary text, newlines, tabs and emoji alone', () => {
    expect(visualize('héllo\n\twörld ❤️')).toEqual([{ text: 'héllo\n\twörld ❤️', hidden: false }]);
    expect(hasHidden('plain')).toBe(false);
    expect(hasHidden('a\u2069')).toBe(true);
    expect(hasHidden('plain')).toBe(false);
  });

  it('formats code points', () => {
    expect(codepoint('\u202E')).toBe('U+202E');
    expect(codepoint('\u{E0041}')).toBe('U+E0041');
  });
});

describe('punycode', () => {
  it('shows homograph hosts in URLs and bare domains as xn--', () => {
    expect(punycode('https://аpple.com/login')).toBe('https://xn--pple-43d.com/login');
    expect(punycode('send to bob@аpple.com or visit аpple.com')).toContain('xn--pple-43d.com');
    expect(punycode('https://user@exämple.org:8443/x')).toBe('https://user@xn--exmple-cua.org:8443/x');
  });

  it('keeps ASCII text unchanged', () => {
    expect(punycode('curl https://example.com/a?b=1 && ls')).toBe('curl https://example.com/a?b=1 && ls');
    expect(punycode('curl http://EXAMPLE.com http://0x7f.1/')).toBe('curl http://EXAMPLE.com http://0x7f.1/');
  });

  it('never drops text the URL parser would read as a query or path', () => {
    expect(punycode('see 例え.jp?token=abc#x')).toBe('see 例え.jp?token=abc#x');
    expect(punycode('mail bob@аpple.com')).toBe('mail bob@xn--pple-43d.com');
  });

  // Bothy's own “…” quotes and Spanish punctuation put a non-ASCII character
  // beside a full stop in plain prose.
  it('leaves sentences alone', () => {
    for (const s of [
      'Priya Shah denied remembering “The office wifi password rotates on mondays.” for everyone',
      'Abrimos el café. ¿Vienes?',
      'café.',
      'Ana.¿Ok?',
      'Ünïcode…',
    ])
      expect(punycode(s)).toBe(s);
    expect(punycode('visit аpple.com.')).toBe('visit xn--pple-43d.com.');
    expect(punycode('(аpple.com)')).toBe('(xn--pple-43d.com)');
  });

  // An approval card or tool output can carry a long field (a message in
  // Chinese, a base64 blob); these took seconds before.
  it('stays linear on long runs without spaces', () => {
    const t0 = performance.now();
    punycode('中'.repeat(50_000));
    punycode('中.'.repeat(25_000));
    punycode('中-'.repeat(25_000) + '!');
    punycode('a'.repeat(50_000));
    punycode('a+'.repeat(25_000));
    expect(performance.now() - t0).toBeLessThan(300);
  });
});

describe('splitUrl', () => {
  it('splits scheme, host and rest', () => {
    expect(splitUrl('https://x.example:8080/p?q')).toEqual({ before: 'https://', host: 'x.example', after: ':8080/p?q' });
    expect(splitUrl('python3 run.py')).toBeNull();
  });
});
