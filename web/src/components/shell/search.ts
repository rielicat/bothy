// Text helpers for the search dialog (docs/design/ui.md §6.3). Until the
// server sends plain snippets with match ranges (S15), the client strips the
// Markdown and the full-text index's [ ] markers and marks the terms itself.
import { date, relTime } from '../../lib/format';

/** A search snippet as plain text: no Markdown, no link targets, no index markers. */
export function plainSnippet(s: string): string {
  return s
    .replace(/\]\((?:[^()\s]|\([^()\s]*\))*\)/g, ']')
    .replace(/[[\]]/g, '')
    .replace(/(^|\n)\s{0,3}(#{1,6}|>|[-*+]|\d+\.)\s+/g, '$1')
    .replace(/(\*\*|__|`+|~~)/g, '')
    .replace(/(^|[\s(])[*_](?=\S)|(?<=\S)[*_](?=[\s.,;:)?]|$)/g, '$1')
    .replace(/\s*\|\s*/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

export interface Piece {
  t: string;
  hit: boolean;
}

/** Splits text into pieces, marking every case-insensitive occurrence of the query's words. */
export function highlight(text: string, q: string): Piece[] {
  const words = [...new Set(q.trim().split(/\s+/).filter((w) => w.length > 0))].sort((a, b) => b.length - a.length);
  if (!words.length) return [{ t: text, hit: false }];
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'gi');
  return text
    .split(re)
    .filter(Boolean)
    .map((t) => ({ t, hit: words.some((w) => w.toLowerCase() === t.toLowerCase()) }));
}

/** "Today", "Yesterday", then the date ("14 Oct"), for a result's last activity. */
export function dayLabel(t: string, now = Date.now()): string {
  const d = date(t);
  if (!d) return '';
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((start(new Date(now)) - start(d)) / 86_400_000);
  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  return relTime(t, { now });
}
