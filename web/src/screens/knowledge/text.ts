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

/** "Oct 12": a date in the browser's locale without the year, the short form relTime uses too. */
export function dayMonth(t: string): string {
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
}

/** A line diff for the prose view, blank lines dropped; DiffView keeps only the changed lines and their neighbours. */
export function proseDiff(before: string, after: string): string {
  const squeeze = (t: string) => t.replace(/\n\s*\n/g, '\n').trim();
  return lineDiff(squeeze(before), squeeze(after));
}
