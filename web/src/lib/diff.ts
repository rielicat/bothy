// Line diff (LCS) rendered as "+"/"-"/" " prefixed lines for DiffView. Memory
// files and MCP tool definitions are small; past MAX lines it degrades to a
// whole-text replacement instead of an O(n·m) table.
import { lcsDiff } from '../components/ui/diff';

const MAX = 1500;
const mark = { same: ' ', del: '-', add: '+' };

export function lineDiff(before: string, after: string): string {
  const a = before ? before.split('\n') : [];
  const b = after ? after.split('\n') : [];
  if (a.length > MAX || b.length > MAX) return [...a.map((l) => `-${l}`), ...b.map((l) => `+${l}`)].join('\n');
  const out: string[] = [];
  lcsDiff(a, b, (kind, line) => out.push(mark[kind] + line));
  return out.join('\n');
}
