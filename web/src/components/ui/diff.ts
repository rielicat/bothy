// Parsing for DiffView: unified diffs (git, card diffs) and the bare
// " "/"+"/"-" line diffs of lib/diff.ts, into rows with one line number each.

export type DiffLine = { kind: 'add' | 'del' | 'ctx'; text: string; no: number };
export type DiffRow = DiffLine | { kind: 'hunk'; label: string };

export interface ParsedDiff {
  path?: string;
  added: number;
  removed: number;
  rows: DiffRow[];
}

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;
const META = /^(diff --git |index |new file mode|deleted file mode|old mode|new mode|similarity index|rename from|rename to|Binary files )/;

function cleanPath(raw: string): string | undefined {
  const p = raw.split('\t')[0].trim();
  if (!p || p === '/dev/null') return undefined;
  return p.replace(/^[ab]\//, '');
}

/** "Lines 10–24" for a hunk, from the new side unless the hunk only removes lines. */
export function hunkLabel(oldStart: number, oldCount: number, newStart: number, newCount: number): string {
  const [start, count] = newCount > 0 ? [newStart, newCount] : [oldStart, oldCount];
  if (count <= 1) return `Line ${start}`;
  return `Lines ${start}–${start + count - 1}`;
}

export function looksLikeUnified(diff: string): boolean {
  return /^@@ -\d/m.test(diff) || /^\+\+\+ /m.test(diff);
}

export function parseDiff(diff: string): ParsedDiff {
  const out: ParsedDiff = { added: 0, removed: 0, rows: [] };
  const lines = diff.replace(/\n$/, '').split('\n');
  let oldNo = 1;
  let newNo = 1;
  let remOld = 0;
  let remNew = 0;
  let sawHunk = false;

  for (const l of lines) {
    // Content lines start with " ", "+" or "-", so "@@" always opens a hunk, even when a count was wrong.
    const h = HUNK.exec(l);
    if (h) {
      sawHunk = true;
      oldNo = Number(h[1]);
      newNo = Number(h[3]);
      remOld = h[2] === undefined ? 1 : Number(h[2]);
      remNew = h[4] === undefined ? 1 : Number(h[4]);
      out.rows.push({ kind: 'hunk', label: hunkLabel(oldNo, remOld, newNo, remNew) });
      continue;
    }
    const inHunk = remOld > 0 || remNew > 0;
    if (!inHunk) {
      if (META.test(l) || l.startsWith('\\ ')) continue;
      if (!out.rows.length || sawHunk) {
        if (l.startsWith('--- ')) {
          out.path ??= cleanPath(l.slice(4));
          continue;
        }
        if (l.startsWith('+++ ')) {
          out.path = cleanPath(l.slice(4)) ?? out.path;
          continue;
        }
      }
      // Past the end of a hunk only another header can follow.
      if (sawHunk) continue;
    }
    if (l.startsWith('\\ ')) continue;
    if (l.startsWith('+')) {
      out.rows.push({ kind: 'add', text: l.slice(1), no: newNo++ });
      out.added++;
      remNew--;
    } else if (l.startsWith('-')) {
      out.rows.push({ kind: 'del', text: l.slice(1), no: oldNo++ });
      out.removed++;
      remOld--;
    } else {
      out.rows.push({ kind: 'ctx', text: l.startsWith(' ') ? l.slice(1) : l, no: newNo++ });
      oldNo++;
      remOld--;
      remNew--;
    }
  }
  return out;
}

/** The lines of a whole file, given either as plain content or as a diff that adds or removes all of it. */
export function fileLines(input: string): string[] {
  if (looksLikeUnified(input)) {
    return parseDiff(input)
      .rows.filter((r): r is DiffLine => r.kind !== 'hunk')
      .map((r) => r.text);
  }
  return input.replace(/\n$/, '').split('\n');
}

export type Segment = { kind: 'same' | 'add' | 'del'; text: string };
export type ProseBlock = { kind: 'same' | 'add' | 'del'; text: string } | { kind: 'change'; segments: Segment[] };

const MAX_TOKENS = 600;

/** Word-level difference between two strings; whitespace is kept as its own tokens. */
export function wordDiff(before: string, after: string): Segment[] {
  const a = before.split(/(\s+)/).filter((t) => t !== '');
  const b = after.split(/(\s+)/).filter((t) => t !== '');
  if (a.length > MAX_TOKENS || b.length > MAX_TOKENS) {
    return [
      { kind: 'del', text: before },
      { kind: 'add', text: after },
    ];
  }
  const n = a.length;
  const m = b.length;
  const lcs = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const out: Segment[] = [];
  const push = (kind: Segment['kind'], text: string) => {
    const last = out[out.length - 1];
    if (last && last.kind === kind) last.text += text;
    else out.push({ kind, text });
  };
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      push('same', a[i++]);
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) push('del', a[i++]);
    else push('add', b[j++]);
  }
  while (i < n) push('del', a[i++]);
  while (j < m) push('add', b[j++]);
  return out;
}

/** Prose view: unchanged lines as they are, a removed line followed by an added one as an inline word change. */
export function proseBlocks(diff: string): ProseBlock[] {
  const lines = parseDiff(diff).rows.filter((r): r is DiffLine => r.kind !== 'hunk');
  const out: ProseBlock[] = [];
  for (let k = 0; k < lines.length; ) {
    if (lines[k].kind === 'ctx') {
      out.push({ kind: 'same', text: lines[k++].text });
      continue;
    }
    const dels: string[] = [];
    const adds: string[] = [];
    while (k < lines.length && lines[k].kind === 'del') dels.push(lines[k++].text);
    while (k < lines.length && lines[k].kind === 'add') adds.push(lines[k++].text);
    const pairs = Math.min(dels.length, adds.length);
    for (let p = 0; p < pairs; p++) out.push({ kind: 'change', segments: wordDiff(dels[p], adds[p]) });
    for (const t of dels.slice(pairs)) out.push({ kind: 'del', text: t });
    for (const t of adds.slice(pairs)) out.push({ kind: 'add', text: t });
  }
  return out;
}
