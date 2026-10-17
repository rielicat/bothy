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
/** heading: medium weight; bullet: "•" in front. The Markdown markers themselves never show. */
export type ProseMark = 'heading' | 'bullet';
export type ProseBlock =
  | { kind: 'same' | 'add' | 'del'; text: string; mark?: ProseMark }
  | { kind: 'change'; segments: Segment[]; mark?: ProseMark }
  /** Unchanged lines left out between the groups that changed. */
  | { kind: 'gap' };

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

/**
 * A Markdown line as prose reads it: a leading "#", "-" or "*" would read as
 * part of the change (a "-" as "removed"), so it becomes a mark; code ticks
 * go, as in search snippets. Link addresses stay. A numbered item keeps its
 * "1." as text: the number is the step's order, which a "•" would lose.
 */
export function proseLine(t: string): { text: string; mark?: ProseMark } {
  const ticks = (x: string) => x.replace(/`+/g, '');
  const h = /^\s*#{1,6}\s+/.exec(t);
  if (h) return { text: ticks(t.slice(h[0].length)), mark: 'heading' };
  const b = /^\s*[-*+]\s+(?:\[[ xX]\]\s+)?/.exec(t);
  if (b) return { text: ticks(t.slice(b[0].length)), mark: 'bullet' };
  const n = /^\s*(\d+\.)\s+(?:\[[ xX]\]\s+)?/.exec(t);
  if (n) return { text: `${n[1]} ${ticks(t.slice(n[0].length))}` };
  return { text: ticks(t) };
}

/**
 * Prose view: unchanged lines as they are, a removed line followed by an
 * added one as an inline word change. Only the changed lines and `around`
 * lines next to them are kept, with a gap between groups, however long the
 * document: an unchanged document would otherwise be printed whole.
 */
export function proseBlocks(diff: string, around = 1): ProseBlock[] {
  const blocks: ProseBlock[] = [];
  const rows = parseDiff(diff).rows;
  for (let k = 0; k < rows.length; ) {
    const r = rows[k];
    if (r.kind === 'hunk') {
      // Separate hunks are separate places in the document.
      if (blocks.length) blocks.push({ kind: 'gap' });
      k++;
      continue;
    }
    if (r.kind === 'ctx') {
      blocks.push({ kind: 'same', ...proseLine(r.text) });
      k++;
      continue;
    }
    const dels: string[] = [];
    const adds: string[] = [];
    while (k < rows.length && rows[k].kind === 'del') dels.push((rows[k++] as DiffLine).text);
    while (k < rows.length && rows[k].kind === 'add') adds.push((rows[k++] as DiffLine).text);
    const pairs = Math.min(dels.length, adds.length);
    for (let p = 0; p < pairs; p++) {
      const before = proseLine(dels[p]);
      const after = proseLine(adds[p]);
      blocks.push({ kind: 'change', segments: wordDiff(before.text, after.text), mark: after.mark ?? before.mark });
    }
    for (const t of dels.slice(pairs)) blocks.push({ kind: 'del', ...proseLine(t) });
    for (const t of adds.slice(pairs)) blocks.push({ kind: 'add', ...proseLine(t) });
  }
  const changed = (b: ProseBlock) => b.kind !== 'same' && b.kind !== 'gap';
  if (!blocks.some(changed)) return blocks.filter((b) => b.kind !== 'gap');
  const keep = blocks.map(() => false);
  blocks.forEach((b, i) => {
    if (changed(b)) for (let j = Math.max(0, i - around); j <= Math.min(blocks.length - 1, i + around); j++) keep[j] = blocks[j].kind !== 'gap';
  });
  const out: ProseBlock[] = [];
  blocks.forEach((b, i) => {
    if (keep[i]) out.push(b);
    else if (out.at(-1)?.kind !== 'gap' && (keep.slice(0, i).some(Boolean) || keep.slice(i).some(Boolean))) out.push({ kind: 'gap' });
  });
  // A blank line at the edge of a group is no context, only an empty row before the "…".
  const blank = (b?: ProseBlock) => b?.kind === 'same' && !b.text.trim();
  const edge = (i: number) => i === 0 || i === out.length - 1 || out[i - 1].kind === 'gap' || out[i + 1].kind === 'gap';
  for (let i = out.findIndex((b, j) => blank(b) && edge(j)); i >= 0; i = out.findIndex((b, j) => blank(b) && edge(j))) out.splice(i, 1);
  return out.filter((b, i) => b.kind !== 'gap' || out[i - 1]?.kind !== 'gap');
}
