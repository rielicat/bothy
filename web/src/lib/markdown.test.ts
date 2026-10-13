import { describe, expect, it } from 'vitest';
import { hostAllowed, renderMarkdown, safeUrl } from './markdown';

const ALLOWED_TAGS = new Set(['P', 'BR', 'STRONG', 'EM', 'DEL', 'CODE', 'PRE', 'A', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'UL', 'OL', 'LI', 'BLOCKQUOTE', 'HR', 'TABLE', 'THEAD', 'TBODY', 'TR', 'TH', 'TD', 'SPAN', 'BUTTON']);
const ALLOWED_ATTRS = new Set(['href', 'target', 'rel', 'title', 'class', 'type', 'data-copy', 'data-copy-code', 'aria-label', 'start']);

// Parses the output the way the browser will and checks that nothing can run:
// only the fixed tag set, no event handlers or style attributes, and every
// href an absolute http(s) or mailto URL that opens without an opener.
function assertInert(html: string) {
  const doc = new DOMParser().parseFromString(`<div id="root">${html}</div>`, 'text/html');
  const root = doc.getElementById('root')!;
  for (const el of root.querySelectorAll('*')) {
    expect(ALLOWED_TAGS.has(el.tagName), `unexpected <${el.tagName.toLowerCase()}> in ${html}`).toBe(true);
    for (const a of el.attributes) {
      expect(ALLOWED_ATTRS.has(a.name), `unexpected attribute ${a.name} in ${html}`).toBe(true);
      expect(a.name.startsWith('on')).toBe(false);
    }
    if (el.tagName === 'A') {
      const href = el.getAttribute('href') ?? '';
      expect(/^(https?:\/\/|mailto:)/i.test(href), `bad href ${href}`).toBe(true);
      expect(el.getAttribute('rel')).toBe('noopener noreferrer nofollow');
      expect(el.getAttribute('target')).toBe('_blank');
    }
    if (el.tagName === 'BUTTON') expect(el.getAttribute('type')).toBe('button');
  }
  return root;
}

const corpus = [
  '<script>alert(1)</script>',
  '<img src=x onerror=alert(1)>',
  '<svg onload=alert(1)>',
  '<iframe src="javascript:alert(1)"></iframe>',
  '<a href="javascript:alert(1)">x</a>',
  '<style>body{display:none}</style>',
  '<div style="background:url(javascript:alert(1))">x</div>',
  '[x](javascript:alert(1))',
  '[x](JaVaScRiPt:alert(1))',
  '[x]( javascript:alert(1))',
  '[x](java\tscript:alert(1))',
  '[x](&#106;avascript:alert(1))',
  '[x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)',
  '[x](vbscript:msgbox(1))',
  '[x](//evil.example/x)',
  '[x](https://ok.example" onmouseover="alert(1))',
  '[x](<https://ok.example/a b> "t")',
  '[<img src=x onerror=alert(1)>](https://ok.example)',
  '![a](https://evil.example/pixel.png)',
  '![a](javascript:alert(1))',
  '<javascript:alert(1)>',
  '<https://ok.example/?q=<script>>',
  'https://ok.example/"><script>alert(1)</script>',
  '`<script>alert(1)</script>`',
  '```html" onload="alert(1)\n<script>alert(1)</script>\n```',
  '# <b onclick=alert(1)>title</b>',
  '| a | b |\n|---|---|\n| <img src=x> | [x](javascript:1) |',
  '> <script>alert(1)</script>',
  '- <script>alert(1)</script>\n  - nested <img src=x>',
  '**<b>x</b>** _<i>y</i>_ ~~<s>z</s>~~',
  'a \uE0000\uE001 b',
  '[a](https://ok.example/\u202Egnp.exe)',
  '&lt;script&gt; &#x3C;script&#x3E;',
  '<!-- comment --><x-foo bar=1>',
  '[[x](https://a.example)](https://b.example)',
  'mailto:<a@b.example> <a@b.example>',
];

describe('renderMarkdown XSS corpus', () => {
  for (const src of corpus) {
    it(`renders inert: ${JSON.stringify(src).slice(0, 60)}`, () => {
      assertInert(renderMarkdown(src));
      assertInert(renderMarkdown(src, { exposed: true }));
    });
  }

  it('escapes raw HTML as text', () => {
    const root = assertInert(renderMarkdown('<script>alert(1)</script> & "q"'));
    expect(root.textContent).toBe('<script>alert(1)</script> & "q"');
  });

  it('never renders images; image syntax becomes a text link', () => {
    const html = renderMarkdown('![cat](https://img.example/cat.png)');
    expect(html).not.toMatch(/<img/i);
    const a = assertInert(html).querySelector('a')!;
    expect(a.textContent).toBe('image: img.example/cat.png');
    expect(a.getAttribute('href')).toBe('https://img.example/cat.png');
  });

  it('turns javascript:, data: and vbscript: links into plain text', () => {
    for (const href of ['javascript:alert(1)', 'data:text/html,x', 'vbscript:x', 'JAVASCRIPT:alert(1)']) {
      const root = assertInert(renderMarkdown(`[click me](${href})`));
      expect(root.querySelector('a')).toBeNull();
      expect(root.textContent).toContain('click me');
    }
  });

  it('adds rel and target to safe links', () => {
    const a = assertInert(renderMarkdown('[docs](https://docs.example/a?b=1)')).querySelector('a')!;
    expect(a.href).toBe('https://docs.example/a?b=1');
    expect(a.textContent).toBe('docs');
  });

  it('autolinks bare URLs without swallowing trailing punctuation', () => {
    const root = assertInert(renderMarkdown('See https://a.example/x). And (https://b.example/y).'));
    expect([...root.querySelectorAll('a')].map((a) => a.getAttribute('href'))).toEqual(['https://a.example/x', 'https://b.example/y']);
  });

  it('neutralises links in exposed sessions: host shown, copy button, no anchor', () => {
    const root = assertInert(renderMarkdown('Go to [this page](https://evil.example/steal?d=1) or https://evil.example/x', { exposed: true }));
    expect(root.querySelector('a')).toBeNull();
    const inert = root.querySelectorAll('.md-inert');
    expect(inert.length).toBe(2);
    expect(inert[0].textContent).toBe('evil.example');
    const copy = root.querySelector<HTMLButtonElement>('button.md-copy[data-copy]')!;
    expect(copy.dataset.copy).toBe('https://evil.example/steal?d=1');
  });

  it('keeps allowlisted hosts clickable in exposed sessions', () => {
    const html = renderMarkdown('[wiki](https://en.wikipedia.org/wiki/Bothy) [x](https://evil.example)', { exposed: true, allow: ['*.wikipedia.org'] });
    const root = assertInert(html);
    expect([...root.querySelectorAll('a')].map((a) => a.hostname)).toEqual(['en.wikipedia.org']);
    expect(root.querySelector('.md-inert')?.textContent).toBe('evil.example');
  });

  // The chat router defangs bare domains because chat clients auto-link
  // them; the web renderer only links absolute http(s) and mailto URLs, so
  // the same text must stay plain here, escapes and emphasis included.
  it('never links bare domains, escaped or emphasised hosts', () => {
    const srcs = [
      'exfil: attacker.com/?q=secret and www.attacker.com/x',
      'evil\\.com/?q=1 evil**.**com/?q=1 https\\://evil.example/x',
      '<attacker.com/?q=1> [x](attacker.com/?q=1) 203.0.113.7/collect',
    ];
    for (const exposed of [true, false]) {
      for (const src of srcs) {
        const root = assertInert(renderMarkdown(src, { exposed }));
        expect(root.querySelector('a'), `${src} (exposed ${exposed})`).toBeNull();
      }
    }
  });

  it('neutralises mailto links in exposed sessions', () => {
    const root = assertInert(renderMarkdown('<mailto:me@evil.example>', { exposed: true }));
    expect(root.querySelector('a')).toBeNull();
    expect(root.querySelector('.md-inert')?.textContent).toBe('me@evil.example');
  });

  it('shows IDN hosts as punycode in neutralised links', () => {
    const root = assertInert(renderMarkdown('https://аpple.example/login', { exposed: true }));
    expect(root.querySelector('.md-inert')?.textContent).toBe('xn--pple-43d.example');
  });

  it('cannot forge internal placeholders', () => {
    const html = renderMarkdown('`x` \uE0000\uE001');
    expect(html).toBe('<p><code>x</code> 0</p>');
  });

  it('sanitises the fenced code language', () => {
    const html = renderMarkdown('```js" onload="x\nconst a = 1 < 2;\n```');
    expect(html).toContain('class="lang-js"');
    expect(html).toContain('const a = 1 &lt; 2;');
  });
});

describe('renderMarkdown formatting', () => {
  it('renders headings, emphasis, code and line breaks', () => {
    expect(renderMarkdown('# Hi\n\n**b** *i* _u_ ~~s~~ `c`\nnext')).toBe(
      '<h1>Hi</h1><p><strong>b</strong> <em>i</em> <em>u</em> <del>s</del> <code>c</code><br>next</p>',
    );
  });

  it('leaves intraword underscores alone', () => {
    expect(renderMarkdown('snake_case_name')).toBe('<p>snake_case_name</p>');
  });

  it('renders tight and loose lists, nesting, task items and start numbers', () => {
    expect(renderMarkdown('- a\n- b\n  - c')).toBe('<ul><li>a</li><li>b<ul><li>c</li></ul></li></ul>');
    expect(renderMarkdown('3. x\n4. y')).toBe('<ol start="3"><li>x</li><li>y</li></ol>');
    expect(renderMarkdown('- a\n\n- b')).toBe('<ul><li><p>a</p></li><li><p>b</p></li></ul>');
    expect(renderMarkdown('- [x] done\n- [ ] todo')).toBe('<ul><li>☑ done</li><li>☐ todo</li></ul>');
  });

  it('renders tables with alignment classes', () => {
    const html = renderMarkdown('| a | b |\n|:-:|--:|\n| 1 | 2 |');
    expect(html).toBe('<table><thead><tr><th class="text-center">a</th><th class="text-right">b</th></tr></thead><tbody><tr><td class="text-center">1</td><td class="text-right">2</td></tr></tbody></table>');
  });

  it('renders blockquotes, rules and fenced code with a copy button', () => {
    expect(renderMarkdown('> q\n\n---')).toBe('<blockquote><p>q</p></blockquote><hr>');
    expect(renderMarkdown('```\na\n```')).toBe('<pre><button type="button" class="md-copy" data-copy-code aria-label="Copy code">Copy</button><code>a</code></pre>');
  });

  it('keeps an unclosed fence open while streaming', () => {
    expect(renderMarkdown('```py\nprint(1)')).toContain('<code class="lang-py">print(1)</code>');
  });

  it('stays fast and bounded on hostile input', () => {
    const t0 = performance.now();
    renderMarkdown('>'.repeat(20000));
    renderMarkdown('['.repeat(20000));
    renderMarkdown('`'.repeat(5000) + 'x');
    renderMarkdown('**a '.repeat(5000));
    renderMarkdown('- '.repeat(5000));
    expect(performance.now() - t0).toBeLessThan(2000);
  });

  // Each of these took seconds (quadratic regex backtracking or rescans) on
  // ~100 KB of model output, re-rendered on every streamed delta.
  it.each([
    ['a heading with a long space run', '# a' + ' '.repeat(100_000) + 'b'],
    ['a paragraph with a long space run', 'a' + ' '.repeat(100_000) + 'b'],
    ['a bare URL ending in parentheses', 'http://a.example' + ')'.repeat(100_000)],
    ['unterminated link targets', '[]('.repeat(30_000)],
  ])('renders %s in linear time', (_, src) => {
    const t0 = performance.now();
    renderMarkdown(src);
    expect(performance.now() - t0).toBeLessThan(500);
  });

  it('keeps heading closers and line-break spacing', () => {
    expect(renderMarkdown('## Title ##')).toBe('<h2>Title</h2>');
    expect(renderMarkdown('# C#')).toBe('<h1>C#</h1>');
    expect(renderMarkdown('# #')).toBe('<h1></h1>');
    expect(renderMarkdown('#nope')).toBe('<p>#nope</p>');
    expect(renderMarkdown('a   \nb  ')).toBe('<p>a<br>b</p>');
    expect(renderMarkdown('see http://a.example/x_(y))')).toContain('href="http://a.example/x_(y)"');
  });
});

describe('url helpers', () => {
  it('accepts only absolute http(s) and mailto URLs', () => {
    expect(safeUrl('https://a.example')?.hostname).toBe('a.example');
    expect(safeUrl('mailto:a@b.example')).not.toBeNull();
    expect(safeUrl('/relative')).toBeNull();
    expect(safeUrl('ftp://x.example')).toBeNull();
    expect(safeUrl('java\nscript:alert(1)')).toBeNull();
  });

  it('matches allowlist entries exactly or by subdomain', () => {
    expect(hostAllowed('a.example', ['a.example'])).toBe(true);
    expect(hostAllowed('x.a.example', ['*.a.example'])).toBe(true);
    expect(hostAllowed('a.example', ['*.a.example'])).toBe(false);
    expect(hostAllowed('evila.example', ['*.a.example'])).toBe(false);
  });
});
