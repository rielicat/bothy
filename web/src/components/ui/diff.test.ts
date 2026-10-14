import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { lineDiff } from '../../lib/diff';
import { fileLines, hunkLabel, parseDiff, proseBlocks, wordDiff } from './diff';
import DiffView from './DiffView.svelte';

const unified = `diff --git a/docs/release.md b/docs/release.md
index 1a2b3c4..5d6e7f8 100644
--- a/docs/release.md
+++ b/docs/release.md
@@ -10,5 +10,6 @@ ## Releasing
 1. Freeze main.
-2. Run the tests.
+2. Run the tests and the browser checks.
+3. Ask for approval.
 3. Tag the release.
 4. Publish the notes.
 5. Unfreeze main.
@@ -40,3 +41,3 @@
 Roll back from the release page.
--- old rule
+++ new rule
 Write down what happened.`;

describe('parseDiff', () => {
  it('turns hunk headers into "Lines a–b" and reads the path and counts', () => {
    const d = parseDiff(unified);
    expect(d.path).toBe('docs/release.md');
    expect(d.added).toBe(3);
    expect(d.removed).toBe(2);
    const hunks = d.rows.filter((r) => r.kind === 'hunk').map((r) => (r.kind === 'hunk' ? r.label : ''));
    expect(hunks).toEqual(['Lines 10–15', 'Lines 41–43']);
  });

  it('keeps content lines that look like file headers inside a hunk', () => {
    const rows = parseDiff(unified).rows.map((r) => (r.kind === 'hunk' ? '' : `${r.kind}:${r.text}`));
    expect(rows).toContain('del:-- old rule');
    expect(rows).toContain('add:++ new rule');
  });

  it('numbers each line once: new side for added and kept lines, old side for removed ones', () => {
    const rows = parseDiff(unified).rows;
    const at = (text: string) => rows.find((r) => r.kind !== 'hunk' && r.text === text);
    expect(at('1. Freeze main.')).toMatchObject({ kind: 'ctx', no: 10 });
    expect(at('2. Run the tests.')).toMatchObject({ kind: 'del', no: 11 });
    expect(at('3. Ask for approval.')).toMatchObject({ kind: 'add', no: 12 });
    expect(at('3. Tag the release.')).toMatchObject({ kind: 'ctx', no: 13 });
  });

  it('labels single-line and removal-only hunks', () => {
    expect(hunkLabel(5, 1, 5, 1)).toBe('Line 5');
    expect(hunkLabel(20, 4, 19, 0)).toBe('Lines 20–23');
    expect(parseDiff('@@ -3 +3 @@\n-a\n+b').rows[0]).toEqual({ kind: 'hunk', label: 'Line 3' });
  });

  it('opens a new hunk even after a wrong count', () => {
    const d = parseDiff('@@ -1,9 +1,9 @@\n a\n-b\n+c\n@@ -20,2 +20,2 @@\n x\n-y\n+z');
    expect(d.rows.filter((r) => r.kind === 'hunk')).toHaveLength(2);
    expect(d.added).toBe(2);
  });

  it('reads the bare line diffs of lib/diff.ts', () => {
    const d = parseDiff(lineDiff('one\ntwo', 'one\nthree'));
    expect(d.rows.map((r) => r.kind)).toEqual(['ctx', 'del', 'add']);
    expect(d.path).toBeUndefined();
  });

  it('reads whole files as content or as a diff', () => {
    expect(fileLines('a\nb\n')).toEqual(['a', 'b']);
    expect(fileLines('--- a/x\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b')).toEqual(['a', 'b']);
  });
});

describe('prose diffs', () => {
  it('marks changed words inside a changed line', () => {
    expect(wordDiff('Summaries on Monday mornings.', 'Summaries on Friday mornings.')).toEqual([
      { kind: 'same', text: 'Summaries on ' },
      { kind: 'del', text: 'Monday' },
      { kind: 'add', text: 'Friday' },
      { kind: 'same', text: ' mornings.' },
    ]);
  });

  it('pairs removed and added lines and keeps the rest whole', () => {
    const blocks = proseBlocks(lineDiff('Ships on Thursdays.\nOld line.', 'Ships on Fridays.\nNew line.\nAnother.'));
    expect(blocks.map((b) => b.kind)).toEqual(['change', 'change', 'add']);
  });
});

describe('DiffView', () => {
  it('shows code changes with one header row and a hunk separator', () => {
    render(DiffView, { props: { diff: unified } });
    expect(screen.getByText('docs/release.md')).toBeTruthy();
    expect(screen.getByText('+3')).toBeTruthy();
    expect(screen.getByText('−2')).toBeTruthy();
    expect(screen.getByText('Lines 10–15')).toBeTruthy();
    expect(screen.queryByText(/^@@/)).toBeNull();
    expect(screen.queryByText(/^\+\+\+ b\//)).toBeNull();
  });

  it('describes a deleted file and keeps its content behind "Show content"', async () => {
    render(DiffView, { props: { diff: 'Staging runs the release branch.\nDeploys need no approval.\nReset on Fridays.', mode: 'deleted-file' } });
    expect(screen.getByText('The whole file will be removed (3 lines).')).toBeTruthy();
    expect(screen.queryByText('Reset on Fridays.')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Show content' }));
    expect(screen.getByText('Reset on Fridays.')).toBeTruthy();
  });

  it('says "1 line" for a one-line deleted file', () => {
    render(DiffView, { props: { diff: 'only line', mode: 'deleted-file' } });
    expect(screen.getByText('The whole file will be removed (1 line).')).toBeTruthy();
  });

  it('shows a new file as added lines with only a "+" count', () => {
    render(DiffView, { props: { diff: '# Launch\n\n- Freeze main', mode: 'new-file', path: 'notes/launch.md' } });
    expect(screen.getByText('notes/launch.md')).toBeTruthy();
    expect(screen.getByText('+3')).toBeTruthy();
    expect(screen.queryByText(/^−/)).toBeNull();
  });

  it('stops at maxLines and offers the rest', async () => {
    const big = Array.from({ length: 30 }, (_, i) => `+line ${i + 1}`).join('\n');
    render(DiffView, { props: { diff: big, maxLines: 10 } });
    expect(screen.queryByText('line 11')).toBeNull();
    await fireEvent.click(screen.getByRole('button', { name: 'Show all 30 lines' }));
    expect(screen.getByText('line 30')).toBeTruthy();
  });

  it('strikes deletions and underlines insertions in prose, with words for screen readers', () => {
    const { container } = render(DiffView, { props: { diff: lineDiff('Ships on Thursdays.', 'Ships on Fridays.'), mode: 'prose' } });
    expect(container.querySelector('del')?.textContent).toBe('Removed: Thursdays.');
    expect(container.querySelector('ins')?.textContent).toBe('Added: Fridays.');
  });
});
