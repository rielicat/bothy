// Escape-all markdown renderer (SPEC §9, D21). It is a security control: the
// output is the only HTML the app injects, so every byte of input is escaped
// and the only tags emitted are the fixed ones below, with attributes built
// here from validated values. Images and raw HTML are never rendered.

export interface MdOptions {
  // Exposed sessions (private + untrusted data in context): links to hosts
  // outside `allow` render inert so a click cannot exfiltrate.
  exposed?: boolean;
  // "example.com" (exact) or "*.example.com" (any subdomain).
  allow?: string[];
}

const ESC: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

export function esc(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ESC[c]);
}

export function hostAllowed(host: string, allow: string[] = []): boolean {
  const h = host.toLowerCase();
  return allow.some((raw) => {
    const p = raw.toLowerCase();
    return p.startsWith('*.') ? h.endsWith(p.slice(1)) && h.length > p.length - 1 : h === p;
  });
}

// Absolute http(s) and mailto only; the URL parser strips the whitespace and
// control-character obfuscations ("java\tscript:", " javascript:") first.
export function safeUrl(raw: string): URL | null {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  return u.protocol === 'http:' || u.protocol === 'https:' || u.protocol === 'mailto:' ? u : null;
}

const FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*([^`\s]*)/;
const HEADING = /^ {0,3}(#{1,6})(?=[ \t]|$)/;
const HR = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const QUOTE = /^ {0,3}> ?/;
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])([ \t]+|$)/;
const TABLE_SEP = /^[ \t]*\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;

export function renderMarkdown(src: string, opts: MdOptions = {}): string {
  // U+E000/U+E001 delimit internal placeholders; strip them so input cannot forge one.
  const text = src
    .replace(/\r\n?/g, '\n')
    .replace(/[\u0000\uE000\uE001]/g, '')
    .replace(/\t/g, '    ');
  return blocks(text.split('\n'), opts, 0);
}

function indentOf(l: string): number {
  return l.length - l.trimStart().length;
}

function isTable(lines: string[], i: number): boolean {
  const sep = lines[i + 1];
  return lines[i].includes('|') && sep !== undefined && sep.includes('-') && TABLE_SEP.test(sep) && cells(sep).length === cells(lines[i]).length;
}

function startsBlock(lines: string[], i: number): boolean {
  const l = lines[i];
  return FENCE.test(l) || HEADING.test(l) || HR.test(l) || QUOTE.test(l) || ITEM.test(l) || isTable(lines, i);
}

// Nesting beyond this renders as plain paragraphs, so hostile input like
// 10,000 ">" cannot exhaust the stack.
const MAX_DEPTH = 16;

function blocks(lines: string[], o: MdOptions, depth: number): string {
  if (depth > MAX_DEPTH) return `<p>${inline(lines.join('\n').trim(), o)}</p>`;
  let out = '';
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    let m: RegExpExecArray | null;
    if ((m = FENCE.exec(line))) {
      const close = new RegExp(`^ {0,3}\\${m[1][0]}{${m[1].length},}[ \\t]*$`);
      const lang = m[2].replace(/[^\w+-]/g, '').slice(0, 20);
      const indent = indentOf(line);
      const body: string[] = [];
      for (i++; i < lines.length && !close.test(lines[i]); i++) body.push(lines[i].slice(Math.min(indent, indentOf(lines[i]))));
      i++;
      out += `<pre><button type="button" class="md-copy" data-copy-code aria-label="Copy code">Copy</button><code${lang ? ` class="lang-${lang}"` : ''}>${esc(body.join('\n'))}</code></pre>`;
      continue;
    }
    if ((m = HEADING.exec(line))) {
      const n = m[1].length;
      out += `<h${n}>${inline(headingText(line.slice(m[0].length)), o)}</h${n}>`;
      i++;
      continue;
    }
    if (HR.test(line)) {
      out += '<hr>';
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const body: string[] = [];
      for (; i < lines.length && QUOTE.test(lines[i]); i++) body.push(lines[i].replace(QUOTE, ''));
      out += `<blockquote>${blocks(body, o, depth + 1)}</blockquote>`;
      continue;
    }
    if (ITEM.test(line)) {
      const [html, next] = list(lines, i, o, depth);
      out += html;
      i = next;
      continue;
    }
    if (isTable(lines, i)) {
      const [html, next] = table(lines, i, o);
      out += html;
      i = next;
      continue;
    }
    const para = [line.trimStart()];
    for (i++; i < lines.length && lines[i].trim() && !startsBlock(lines, i); i++) para.push(lines[i].trimStart());
    out += `<p>${inline(para.join('\n').trimEnd(), o)}</p>`;
  }
  return out;
}

// A closing run of "#" counts only after whitespace or as the whole text. This
// is done by hand: the regex form backtracks quadratically on long space runs.
function headingText(s: string): string {
  const t = s.trim();
  let e = t.length;
  while (e > 0 && t[e - 1] === '#') e--;
  if (e === 0) return '';
  return e < t.length && (t[e - 1] === ' ' || t[e - 1] === '\t') ? t.slice(0, e).trimEnd() : t;
}

function list(lines: string[], start: number, o: MdOptions, depth: number): [string, number] {
  const first = ITEM.exec(lines[start])!;
  const ordered = /\d/.test(first[2]);
  const base = first[1].length;
  const items: string[][] = [];
  let loose = false;
  let i = start;
  for (let m: RegExpExecArray | null = first; m && m[1].length === base && /\d/.test(m[2]) === ordered; m = i < lines.length ? ITEM.exec(lines[i]) : null) {
    const off = m[1].length + m[2].length + (m[3].length === 0 || m[3].length > 4 ? 1 : m[3].length);
    const item = [lines[i].slice(off)];
    for (i++; i < lines.length; ) {
      const l = lines[i];
      if (!l.trim()) {
        let j = i + 1;
        while (j < lines.length && !lines[j].trim()) j++;
        if (j < lines.length && indentOf(lines[j]) >= off) {
          for (; i < j; i++) item.push('');
          continue;
        }
        const n = j < lines.length ? ITEM.exec(lines[j]) : null;
        if (n && n[1].length === base && /\d/.test(n[2]) === ordered) {
          loose = true;
          i = j;
        }
        break;
      }
      if (indentOf(l) >= off) item.push(l.slice(off));
      else if (!startsBlock(lines, i) && item[item.length - 1].trim()) item.push(l.trim());
      else break;
      i++;
    }
    items.push(item);
  }
  const startNum = ordered ? parseInt(first[2], 10) : 1;
  const tag = ordered ? 'ol' : 'ul';
  let html = `<${tag}${ordered && startNum !== 1 ? ` start="${startNum}"` : ''}>`;
  for (const item of items) {
    let body = item;
    let box = '';
    const task = /^\[([ xX])\][ \t]+/.exec(item[0]);
    if (task) {
      box = task[1] === ' ' ? '☐ ' : '☑ ';
      body = [item[0].slice(task[0].length), ...item.slice(1)];
    }
    let inner = blocks(body, o, depth + 1);
    // Tight lists drop the first paragraph wrapper; paragraphs never nest, so
    // the first "</p>" closes it.
    if (!loose && inner.startsWith('<p>')) {
      const e = inner.indexOf('</p>');
      inner = inner.slice(3, e) + inner.slice(e + 4);
    }
    html += `<li>${box}${inner}</li>`;
  }
  return [html + `</${tag}>`, i];
}

function cells(l: string): string[] {
  let s = l.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, '|'));
}

function table(lines: string[], start: number, o: MdOptions): [string, number] {
  const head = cells(lines[start]);
  const align = cells(lines[start + 1]).map((c) =>
    c.startsWith(':') && c.endsWith(':') ? ' class="text-center"' : c.endsWith(':') ? ' class="text-right"' : '',
  );
  let html = '<table><thead><tr>' + head.map((c, k) => `<th${align[k]}>${inline(c, o)}</th>`).join('') + '</tr></thead><tbody>';
  let i = start + 2;
  for (; i < lines.length && lines[i].trim() && lines[i].includes('|'); i++) {
    const row = cells(lines[i]);
    html += '<tr>' + head.map((_, k) => `<td${align[k]}>${inline(row[k] ?? '', o)}</td>`).join('') + '</tr>';
  }
  return [html + '</tbody></table>', i];
}

const AUTOLINK = /<((?:https?|mailto):[^\s<>]+)>/iy;
const AUTOMAIL = /<([^\s@<>()]+@[^\s@<>()]+\.[^\s@<>()]+)>/y;
const BARE = /https?:\/\/[^\s<]+/iy;
const PUNCT = /[!-/:-@[-`{-~]/;

function inline(s: string, o: MdOptions, inLink = false): string {
  const parts: string[] = [];
  const ph = (html: string) => `\uE000${parts.push(html) - 1}\uE001`;
  let out = '';
  let buf = '';
  const flush = () => {
    out += esc(buf);
    buf = '';
  };
  // Lengths of backtick runs known to have no closing run further on; keeps
  // a line of unmatched backticks linear instead of quadratic.
  const unclosed = new Set<number>();
  // A failed link parse can scan thousands of characters; once this budget
  // is spent, brackets stay text, so "[](" repeated cannot stall rendering.
  const scan = { left: 16 * s.length + 8192 };
  let i = 0;
  while (i < s.length) {
    const ch = s[i];
    if (ch === '\\' && i + 1 < s.length && (s[i + 1] === '\n' || PUNCT.test(s[i + 1]))) {
      flush();
      out += ph(s[i + 1] === '\n' ? '<br>' : esc(s[i + 1]));
      i += 2;
      continue;
    }
    if (ch === '`') {
      let n = 1;
      while (s[i + n] === '`') n++;
      const fence = '`'.repeat(n);
      let close = unclosed.has(n) ? -1 : s.indexOf(fence, i + n);
      while (close >= 0 && s[close + n] === '`') close = s.indexOf(fence, close + n + 1);
      if (close < 0) {
        unclosed.add(n);
        buf += fence;
        i += n;
        continue;
      }
      let code = s.slice(i + n, close).replace(/\n/g, ' ');
      if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ') && code.trim()) code = code.slice(1, -1);
      flush();
      out += ph(`<code>${esc(code)}</code>`);
      i = close + n;
      continue;
    }
    if (!inLink && scan.left > 0 && (ch === '[' || (ch === '!' && s[i + 1] === '['))) {
      const img = ch === '!';
      const l = parseLink(s, img ? i + 1 : i, scan);
      if (l) {
        flush();
        const label = img ? '' : inline(l.text, o, true);
        out += ph(link(l.href, label, o, img));
        i = l.end;
        continue;
      }
    }
    if (!inLink && ch === '<') {
      AUTOLINK.lastIndex = i;
      AUTOMAIL.lastIndex = i;
      const a = AUTOLINK.exec(s);
      const m = a ? null : AUTOMAIL.exec(s);
      if (a || m) {
        flush();
        const href = a ? a[1] : `mailto:${m![1]}`;
        out += ph(link(href, esc(a ? a[1] : m![1]), o));
        i += (a ?? m)![0].length;
        continue;
      }
    }
    if (!inLink && (ch === 'h' || ch === 'H') && !/[\p{L}\p{N}/]/u.test(s[i - 1] ?? ' ')) {
      BARE.lastIndex = i;
      const b = BARE.exec(s);
      if (b) {
        let url = b[0];
        let open = 0;
        let close = 0;
        for (const c of url) {
          if (c === '(') open++;
          else if (c === ')') close++;
        }
        for (;;) {
          const last = url[url.length - 1];
          if ('?!.,:;*_~\'"'.includes(last)) url = url.slice(0, -1);
          else if (last === ')' && open < close) {
            url = url.slice(0, -1);
            close--;
          } else break;
        }
        flush();
        out += ph(link(url, esc(url), o));
        i += url.length;
        continue;
      }
    }
    buf += ch;
    i++;
  }
  flush();
  out = out
    // Emphasis stays within a line and a bounded span so unmatched markers
    // cannot make the lazy scans quadratic.
    .replace(/\*\*(?=\S)([^\n]{0,400}?\S)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[^\p{L}\p{N}])__(?=\S)([^\n]{0,400}?\S)__(?![\p{L}\p{N}])/gu, '$1<strong>$2</strong>')
    .replace(/~~(?=\S)([^\n]{0,400}?\S)~~/g, '<del>$1</del>')
    .replace(/(^|[^*])\*(?=[^\s*])([^\n]{0,400}?[^\s*])\*(?!\*)/g, '$1<em>$2</em>')
    .replace(/(^|[^\p{L}\p{N}])_(?=\S)([^\n]{0,400}?\S)_(?![\p{L}\p{N}])/gu, '$1<em>$2</em>');
  return breaks(out).replace(/\uE000(\d+)\uE001/g, (_, n: string) => parts[Number(n)]);
}

// Newlines become <br>, dropping the spaces before each one. A / *\n/ regex
// would rescan every run of spaces from each of its positions.
function breaks(s: string): string {
  if (!s.includes('\n')) return s;
  const lines = s.split('\n');
  for (let i = 0; i < lines.length - 1; i++) {
    const l = lines[i];
    let e = l.length;
    while (e > 0 && l.charCodeAt(e - 1) === 32) e--;
    lines[i] = l.slice(0, e);
  }
  return lines.join('<br>');
}

function parseLink(s: string, at: number, scan: { left: number }): { text: string; href: string; end: number } | null {
  let depth = 0;
  let j = at;
  const stop = Math.min(s.length, at + 1000);
  for (; j < stop; j++) {
    if (s[j] === '\\') j++;
    else if (s[j] === '[') depth++;
    else if (s[j] === ']' && --depth === 0) break;
  }
  scan.left -= j - at;
  if (j >= stop || s[j + 1] !== '(') return null;
  const text = s.slice(at + 1, j);
  let k = j + 2;
  while (s[k] === ' ') k++;
  let href = '';
  if (s[k] === '<') {
    const e = s.indexOf('>', k);
    scan.left -= (e < 0 ? s.length : e) - k;
    if (e < 0 || s.slice(k, e).includes('\n')) return null;
    href = s.slice(k + 1, e);
    k = e + 1;
  } else {
    let parens = 0;
    const b = k;
    for (const end = Math.min(s.length, k + 4000); k < end && !/\s/.test(s[k]); k++) {
      if (s[k] === '(') parens++;
      else if (s[k] === ')' && parens-- === 0) break;
    }
    scan.left -= k - b;
    href = s.slice(b, k);
  }
  while (s[k] === ' ') k++;
  if (s[k] === '"' || s[k] === "'") {
    const e = s.indexOf(s[k], k + 1);
    if (e < 0) return null;
    k = e + 1;
    while (s[k] === ' ') k++;
  }
  if (s[k] !== ')') return null;
  return { text, href, end: k + 1 };
}

function shortUrl(u: URL): string {
  const p = u.hostname + (u.pathname === '/' ? '' : u.pathname);
  return p.length > 48 ? p.slice(0, 47) + '…' : p;
}

function link(href: string, label: string, o: MdOptions, image = false): string {
  const u = safeUrl(href);
  if (image) label = esc(`image: ${u ? shortUrl(u) : href}`);
  if (!u) return image ? `<span class="md-img">${label}</span>` : label;
  const mail = u.protocol === 'mailto:';
  if (o.exposed && (mail || !hostAllowed(u.hostname, o.allow))) {
    const where = mail ? u.pathname : u.hostname;
    const prefix = label && label !== esc(href) && label !== esc(u.href) ? `${label} ` : '';
    return `${prefix}<span class="md-inert" title="Links are disabled in this session because it holds private and untrusted data">${esc(where)}</span><button type="button" class="md-copy" data-copy="${esc(u.href)}" aria-label="Copy link">Copy</button>`;
  }
  return `<a href="${esc(u.href)}" target="_blank" rel="noopener noreferrer nofollow" title="${esc(u.href)}"${image ? ' class="md-img"' : ''}>${label || esc(u.href)}</a>`;
}
