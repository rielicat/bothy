// Line diff (LCS) rendered as "+"/"-"/" " prefixed lines for DiffView. Memory
// files and MCP tool definitions are small; past MAX lines it degrades to a
// whole-text replacement instead of an O(n·m) table.
const MAX = 1500;

export function lineDiff(before: string, after: string): string {
  const a = before ? before.split('\n') : [];
  const b = after ? after.split('\n') : [];
  if (a.length > MAX || b.length > MAX) return [...a.map((l) => `-${l}`), ...b.map((l) => `+${l}`)].join('\n');
  const n = a.length;
  const m = b.length;
  const lcs = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const out: string[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push(` ${a[i++]}`);
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) out.push(`-${a[i++]}`);
    else out.push(`+${b[j++]}`);
  }
  while (i < n) out.push(`-${a[i++]}`);
  while (j < m) out.push(`+${b[j++]}`);
  return out.join('\n');
}
