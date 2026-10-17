import { describe, expect, it } from 'vitest';
import { changeWords, dayMonth, inSentence, proseDiff } from './text';

describe('knowledge copy helpers', () => {
  it('fits relative times into a sentence', () => {
    expect(inSentence('2 h ago')).toBe('2 h ago');
    expect(inSentence('just now')).toBe('just now');
    expect(inSentence('Yesterday at 14:02')).toBe('yesterday at 14:02');
    expect(inSentence('12 Oct')).toBe('on 12 Oct');
  });

  it('says what a change did', () => {
    expect(changeWords('', 'a\nb')).toBe('wrote the first version');
    expect(changeWords('a', 'a\nb\nc\nd')).toBe('added 3 lines');
    expect(changeWords('a\nb', 'a')).toBe('removed 1 line');
    expect(changeWords('a\nb', 'a\nc')).toBe('edited');
    expect(changeWords('a', '')).toBe('cleared everything');
  });

  it('diffs the text without its blank lines', () => {
    expect(proseDiff('a\n\nb', 'a\n\nc').split('\n')).toEqual([' a', '-b', '+c']);
  });

  it('writes dates in one short form', () => {
    expect(dayMonth('2026-10-13T12:00:00Z')).toBe(new Date('2026-10-13T12:00:00Z').toLocaleDateString(undefined, { day: 'numeric', month: 'short' }));
  });
});
