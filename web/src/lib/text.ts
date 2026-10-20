// Characters that reorder or hide text (bidi controls, zero-width joiners,
// BOM, soft hyphen, Hangul fillers, tag characters) plus C0/C1 controls other
// than tab and newline. Approval cards show each one as ⟨U+XXXX⟩ so an Action
// cannot look different from what will run. Variation selectors are left
// alone: they only pick emoji glyphs.
const HIDDEN =
  /[\u0000-\u0008\u000B-\u001F\u007F-\u009F\u00AD\u061C\u115F\u1160\u17B4\u17B5\u180E\u200B-\u200F\u202A-\u202E\u2060-\u2064\u2066-\u206F\u3164\uFEFF\uFFA0\u{E0000}-\u{E007F}]/gu;

export interface Segment {
  text: string;
  hidden: boolean;
}

export function codepoint(ch: string): string {
  return 'U+' + ch.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0');
}

export function visualize(s: string): Segment[] {
  const out: Segment[] = [];
  let last = 0;
  for (const m of s.matchAll(HIDDEN)) {
    if (m.index > last) out.push({ text: s.slice(last, m.index), hidden: false });
    out.push({ text: codepoint(m[0]), hidden: true });
    last = m.index + m[0].length;
  }
  if (last < s.length) out.push({ text: s.slice(last), hidden: false });
  return out;
}

export function hasHidden(s: string): boolean {
  HIDDEN.lastIndex = 0;
  const found = HIDDEN.test(s);
  HIDDEN.lastIndex = 0;
  return found;
}

const NON_ASCII = /[^\x00-\x7F]/;

// Only a bare host is rewritten: if the parser would read part of the text as
// a path, query or credentials, rewriting would make that part vanish from
// the card. ASCII hosts stay verbatim unless percent-escapes could hide a
// homograph.
function punyHost(host: string): string {
  if (!NON_ASCII.test(host) && !host.includes('%')) return host;
  try {
    const u = new URL(`http://${host}`);
    return u.pathname === '/' && !u.search && !u.hash && !u.username && !u.password ? u.host : host;
  } catch {
    return host;
  }
}

const EDGE = /[\p{P}\p{S}]/u;
const LEAD = /^[\p{P}\p{S}]*/u;
const LABEL = /^[\p{L}\p{N}]/u;

// The punctuation around a run ("“аpple.com”", "visit аpple.com.") is kept
// as written and only the name between is a host. Trailing punctuation is
// found by a backwards scan: /[\p{P}\p{S}]+$/ is quadratic on a long run of
// dashes.
function trimEdges(run: string): [number, number] {
  const start = LEAD.exec(run)![0].length;
  let end = run.length;
  while (end > start) {
    const lo = run.charCodeAt(end - 1) >= 0xdc00 && run.charCodeAt(end - 1) <= 0xdfff && end - 2 >= start ? end - 2 : end - 1;
    if (!EDGE.test(run.slice(lo, end))) break;
    end = lo;
  }
  return [start, end];
}

// Rewrites hosts inside URLs, and bare non-ASCII domain names, to punycode so
// homographs ("аpple.com" with a Cyrillic а) are visible as xn--…. Both
// patterns match each run of characters once, so long fields stay linear.
export function punycode(s: string): string {
  return s
    .replace(/(?<![a-z0-9+.-])([a-z][a-z0-9+.-]*:\/\/)([^\s/?#"'<>]+)/gi, (_, scheme: string, auth: string) => {
      const at = auth.lastIndexOf('@');
      return scheme + auth.slice(0, at + 1) + punyHost(auth.slice(at + 1));
    })
    .replace(/(^|[\s(,;@])([^\s/:@(),;]+)/gu, (m, pre: string, run: string) => {
      const [start, end] = trimEdges(run);
      const host = run.slice(start, end);
      // Every label must start with a letter or digit, which prose such as
      // "Ana.¿Ok" fails; symbols inside a label still count, since a lookalike
      // slash ("paypal.com⁄signin.evil.io") is the spoof to reveal.
      if (!NON_ASCII.test(host) || !host.includes('.') || !host.split('.').every((l) => LABEL.test(l))) return m;
      return pre + run.slice(0, start) + punyHost(host) + run.slice(end);
    });
}

export interface UrlParts {
  before: string;
  host: string;
  after: string;
}

// Splits a single absolute URL so the host can be emphasised.
export function splitUrl(s: string): UrlParts | null {
  const m = /^([a-z][a-z0-9+.-]*:\/\/(?:[^\s/?#@]*@)?)([^\s/?#:]+)(.*)$/i.exec(s.trim());
  return m ? { before: m[1], host: m[2], after: m[3] } : null;
}
