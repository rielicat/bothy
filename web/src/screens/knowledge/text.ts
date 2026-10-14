// Small copy helpers shared by the Knowledge screens.
import { lineDiff } from '../../lib/diff';

/** relTime() inside a sentence: "updated 2 h ago", "updated yesterday at 14:02", "updated on 12 Oct". */
export function inSentence(rel: string): string {
  if (/^(Yesterday|Tomorrow)\b/.test(rel)) return rel[0].toLowerCase() + rel.slice(1);
  return /ago$|^just now$|^in \d/.test(rel) ? rel : `on ${rel}`;
}

/** What a change did, in a few words: "wrote the first notes", "added 3 lines", "edited". */
export function changeWords(before: string, after: string): string {
  if (!before.trim()) return 'wrote the first version';
  if (!after.trim()) return 'cleared everything';
  const lines = lineDiff(before, after).split('\n');
  const added = lines.filter((l) => l.startsWith('+') && l.slice(1).trim()).length;
  const removed = lines.filter((l) => l.startsWith('-') && l.slice(1).trim()).length;
  if (added && !removed) return `added ${added === 1 ? '1 line' : `${added} lines`}`;
  if (removed && !added) return `removed ${removed === 1 ? '1 line' : `${removed} lines`}`;
  return 'edited';
}

/** "12 October": a date in the browser's locale without the year. */
export function dayMonth(t: string): string {
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
}

/**
 * A line diff for the prose view: blank lines dropped, and in longer
 * documents only the changed lines with one line around them, "…" between.
 */
export function proseDiff(before: string, after: string, around = 1): string {
  const squeeze = (t: string) => t.replace(/\n\s*\n/g, '\n').trim();
  const lines = lineDiff(squeeze(before), squeeze(after)).split('\n');
  if (lines.length <= 12) return lines.join('\n');
  const keep = lines.map(() => false);
  lines.forEach((l, i) => {
    if (l[0] === '+' || l[0] === '-') for (let j = Math.max(0, i - around); j <= Math.min(lines.length - 1, i + around); j++) keep[j] = true;
  });
  const out: string[] = [];
  lines.forEach((l, i) => {
    if (keep[i]) out.push(l);
    else if (keep[i - 1] || (i === 0 && keep.some(Boolean))) out.push(' …');
  });
  return out.join('\n');
}
