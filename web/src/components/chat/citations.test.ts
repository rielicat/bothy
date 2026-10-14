import { describe, expect, it } from 'vitest';
import { renderMarkdown } from '../../lib/markdown';
import { linkCitations } from './citations';

describe('linkCitations', () => {
  it('turns a citation into a link that names the repository, file and lines', () => {
    expect(linkCitations('The change is in acme/web@4f2c1a9:src/auth/callback.ts#L10-L18.')).toBe(
      'The change is in [acme/web · src/auth/callback.ts, lines 10–18 ↗](<https://github.com/acme/web/blob/4f2c1a9/src/auth/callback.ts#L10-L18>).',
    );
  });

  it('says "line" for a single line', () => {
    expect(linkCitations('See acme/api@0123abcd:main.go#L7')).toBe('See [acme/api · main.go, line 7 ↗](<https://github.com/acme/api/blob/0123abcd/main.go#L7>)');
  });

  it('never shows the commit in the rendered text', () => {
    const html = renderMarkdown(linkCitations('In acme/web@4f2c1a9:src/build_tools/x_y.ts#L1-L2 it is set.'), {});
    const text = html.replace(/<[^>]+>/g, '');
    expect(text).toBe('In acme/web · src/build_tools/x_y.ts, lines 1–2 ↗ it is set.');
    expect(html).toContain('href="https://github.com/acme/web/blob/4f2c1a9/src/build_tools/x_y.ts#L1-L2"');
  });

  it('stays inert in an exposed chat', () => {
    const html = renderMarkdown(linkCitations('acme/web@4f2c1a9:a.ts#L1', true), { exposed: true });
    expect(html).not.toContain('<a ');
    expect(html).not.toContain('↗');
    expect(html).toContain('md-inert');
  });

  it('leaves code, existing links and plain text alone', () => {
    const code = 'Run `git show acme/web@4f2c1a9:a.ts#L1`';
    expect(linkCitations(code)).toBe(code);
    const fenced = '```\nacme/web@4f2c1a9:a.ts#L1\n```';
    expect(linkCitations(fenced)).toBe(fenced);
    const linked = '[here](acme/web@4f2c1a9:a.ts#L1)';
    expect(linkCitations(linked)).toBe(linked);
    expect(linkCitations('Mail sam@acme.example about it')).toBe('Mail sam@acme.example about it');
  });
});
