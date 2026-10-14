// Knowledge citations (`owner/repo@sha:path#L10-L42`) read as "acme/web ·
// src/auth/redirect.ts, lines 10–42" and open GitHub at that commit (§7.3).
// The commit stays in the link target, so a reply never shows it in the text.
const CITATION = /\b([\w.-]+\/[\w.-]+)@([0-9a-f]{7,40}):([^\s#)\]`<>]+)#L(\d+)(?:-L(\d+))?/g;
const MD_PUNCT = /[\\`*_[\]<>]/g;

function cite(m: string[]): { label: string; href: string } {
  const [, repo, sha, path, from, to] = m;
  return {
    label: `${repo} · ${path}, ${to ? `lines ${from}–${to}` : `line ${from}`}`,
    href: `https://github.com/${repo}/blob/${sha}/${encodeURI(path)}#L${from}${to ? `-L${to}` : ''}`,
  };
}

// Code shows what was written, so citations inside fences and code spans stay as they are.
function outsideCode(line: string, f: (s: string) => string): string {
  return line
    .split(/(`+[^`]*`+)/)
    .map((part, i) => (i % 2 ? part : f(part)))
    .join('');
}

/**
 * The reply's Markdown with each citation turned into a link. In an exposed
 * chat the link renders inert like any other, so it gets no "↗".
 */
export function linkCitations(text: string, exposed = false): string {
  if (!text.includes('@')) return text;
  let fence = '';
  return text
    .split('\n')
    .map((line) => {
      const open = /^\s{0,3}(`{3,}|~{3,})/.exec(line)?.[1];
      if (fence) {
        if (open && open[0] === fence[0] && open.length >= fence.length) fence = '';
        return line;
      }
      if (open) {
        fence = open;
        return line;
      }
      return outsideCode(line, (s) =>
        s.replace(CITATION, (whole: string, ...rest: unknown[]) => {
          const at = rest[5] as number;
          // Already the target of a Markdown link or an autolink.
          if (s[at - 1] === '(' || s[at - 1] === '<') return whole;
          const c = cite([whole, ...(rest.slice(0, 5) as string[])]);
          return `[${c.label.replace(MD_PUNCT, '\\$&')}${exposed ? '' : ' ↗'}](<${c.href}>)`;
        }),
      );
    })
    .join('\n');
}
