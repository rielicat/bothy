import { describe, expect, it } from 'vitest';
import { describeWhen, fromSpec, onceInput, timeOptions, toSpec, validRule, type When } from './schedules';

const base: When = { kind: 'daily', time: '09:00', day: 1, date: '2026-10-16', rule: '' };

describe('"When" and the repeat rule (docs/design/ui.md §10.3)', () => {
  const presets: [When, string, string][] = [
    [{ ...base, kind: 'daily', time: '09:00' }, 'cron', '0 9 * * *'],
    [{ ...base, kind: 'weekdays', time: '08:30' }, 'cron', '30 8 * * 1-5'],
    [{ ...base, kind: 'weekly', time: '16:00', day: 5 }, 'cron', '0 16 * * 5'],
    [{ ...base, kind: 'weekly', time: '07:15', day: 0 }, 'cron', '15 7 * * 0'],
    [{ ...base, kind: 'monthly', time: '09:00', day: 15 }, 'cron', '0 9 15 * *'],
    [{ ...base, kind: 'once', time: '14:30', date: '2026-11-02' }, 'once', '2026-11-02T14:30'],
    [{ ...base, kind: 'custom', rule: '0 9 1,15 * *' }, 'cron', '0 9 1,15 * *'],
  ];

  for (const [when, kind, spec] of presets) {
    it(`${when.kind} ${spec} round-trips`, () => {
      expect(toSpec(when)).toEqual({ kind, spec });
      const back = fromSpec(kind, spec, 'Europe/London');
      expect(back.kind).toBe(when.kind);
      expect(toSpec(back)).toEqual({ kind, spec });
    });
  }

  it('reads Sunday written as 7 as a weekly choice', () => {
    expect(fromSpec('cron', '0 9 * * 7')).toMatchObject({ kind: 'weekly', day: 0, time: '09:00' });
  });

  it('keeps anything else as Custom, with the rule as written', () => {
    for (const spec of ['*/15 * * * *', '0 9 * 1 *', '0 9 1 * 1', '0 9-17 * * *']) expect(fromSpec('cron', spec)).toMatchObject({ kind: 'custom', rule: spec });
  });

  it('reads a saved once spec back on the clock of its time zone', () => {
    expect(onceInput('2026-10-13T15:00:00-04:00', 'America/New_York')).toBe('2026-10-13T15:00');
    expect(fromSpec('once', '2026-10-13T15:00:00-04:00', 'America/New_York')).toMatchObject({ kind: 'once', date: '2026-10-13', time: '15:00' });
  });

  it('describes each choice in words, and Custom not at all', () => {
    const l = 'en-GB';
    expect(describeWhen({ ...base, kind: 'weekdays', time: '09:00' }, l)).toBe('Weekdays at 9:00');
    expect(describeWhen({ ...base, kind: 'weekly', day: 1, time: '08:00' }, l)).toBe('Mondays at 8:00');
    expect(describeWhen({ ...base, kind: 'daily', time: '18:30' }, 'en-US')).toBe('Every day at 6:30 PM');
    expect(describeWhen({ ...base, kind: 'monthly', day: 1 }, l)).toBe('Every month on day 1 at 9:00');
    expect(describeWhen({ ...base, kind: 'once', date: '2026-10-16' }, l)).toBe('Once on 16 Oct at 9:00');
    expect(describeWhen({ ...base, kind: 'custom', rule: '0 9 * * *' }, l)).toBeNull();
  });

  it('checks the shape of a repeat rule', () => {
    for (const ok of ['0 9 * * 1-5', '*/15 8-18 * * *', '0 9 1,15 * *']) expect(validRule(ok), ok).toBe(true);
    for (const bad of ['every monday', '0 9 * *', '0 9 * * * *', '0 9 ? * MON']) expect(validRule(bad), bad).toBe(false);
  });

  it('offers half hours, plus a saved time that falls between them', () => {
    const opts = timeOptions('08:15', 'en-GB');
    expect(opts).toHaveLength(49);
    expect(opts.map((o) => o.value)).toContain('08:15');
    expect(opts[0]).toEqual({ value: '00:00', label: '0:00' });
  });
});
