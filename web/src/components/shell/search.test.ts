import { describe, expect, it } from 'vitest';
import { dayLabel, highlight, plainSnippet } from './search';

describe('plainSnippet (S15 fallback)', () => {
  it('drops the index markers and Markdown, keeping the words', () => {
    expect(plainSnippet('…the [launch] checklist for **v2.3**. Freeze `main` on…')).toBe('…the launch checklist for v2.3. Freeze main on…');
    expect(plainSnippet('## Saturday\n1. **Breakfast** at a café\n- Browse the *bookshops*')).toBe('Saturday Breakfast at a café Browse the bookshops');
    expect(plainSnippet('click [this [link]](https://evil.example/confirm?u=sam) now')).toBe('click this link now');
    expect(plainSnippet('| Item | Estimate |\n| Hotel | £180 |')).toBe('Item Estimate Hotel £180');
    expect(plainSnippet('> Tip: book [ahead]')).toBe('Tip: book ahead');
  });

  it('leaves snake_case and file names alone', () => {
    expect(plainSnippet('see build_itinerary.py and notes_2026.md')).toBe('see build_itinerary.py and notes_2026.md');
  });
});

describe('highlight', () => {
  it('marks every word of the query, case-insensitively', () => {
    expect(highlight('Launch checklist for the launch', 'launch')).toEqual([
      { t: 'Launch', hit: true },
      { t: ' checklist for the ', hit: false },
      { t: 'launch', hit: true },
    ]);
    expect(highlight('Q3 pricing notes', 'q3 notes').filter((p) => p.hit).map((p) => p.t)).toEqual(['Q3', 'notes']);
  });

  it('treats regex characters as text', () => {
    expect(highlight('costs $5 (approx.)', '(approx.)')).toEqual([
      { t: 'costs $5 ', hit: false },
      { t: '(approx.)', hit: true },
    ]);
  });

  it('returns the text whole for an empty query', () => {
    expect(highlight('anything', '  ')).toEqual([{ t: 'anything', hit: false }]);
  });
});

describe('dayLabel', () => {
  const now = new Date(2026, 9, 14, 0, 20).getTime();
  it('says Today, Yesterday, then the date, by calendar day', () => {
    expect(dayLabel(new Date(2026, 9, 14, 0, 5).toISOString(), now)).toBe('Today');
    expect(dayLabel(new Date(2026, 9, 13, 23, 50).toISOString(), now)).toBe('Yesterday');
    expect(dayLabel(new Date(2026, 9, 2, 9).toISOString(), now)).toMatch(/2 Oct|Oct 2/);
    expect(dayLabel('0001-01-01T00:00:00Z', now)).toBe('');
  });
});
