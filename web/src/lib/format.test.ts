import { describe, expect, it } from 'vitest';
import { lineDiff } from './diff';
import { bytes, countdown, duration, estimate, expiresIn, fullTime, ms, money, parseGoDuration, plural, prettyGoDuration, relTime, tokens, usd } from './format';

describe('format', () => {
  it('formats micro-dollars', () => {
    expect(usd(0)).toBe('$0');
    expect(usd(4_200_000)).toBe('$4.2');
    expect(usd(18_400)).toBe('$0.018');
    expect(usd(31)).toBe('<$0.0001');
    expect(usd(250_000_000)).toBe('$250');
    expect(usd(5, { known: false })).toBe('price unknown');
  });

  it('formats tokens, bytes and v0.1 latencies', () => {
    expect(tokens(950)).toBe('950');
    expect(tokens(1500)).toBe('1.5K');
    expect(tokens(200_000)).toBe('200K');
    expect(tokens(1_250_000)).toBe('1.3M');
    expect(bytes(2048)).toBe('2 KB');
    expect(ms(840)).toBe('840 ms');
    expect(ms(2_100)).toBe('2.1 s');
    expect(ms(125_000)).toBe('2m 5s');
  });

  it('parses and prettifies Go durations', () => {
    expect(parseGoDuration('15m')).toBe(15 * 60e9);
    expect(parseGoDuration('1h30m')).toBe(5400e9);
    expect(parseGoDuration('1.2s')).toBe(1.2e9);
    expect(parseGoDuration('0')).toBe(0);
    expect(parseGoDuration('15 minutes')).toBeNull();
    expect(parseGoDuration('')).toBeNull();
    expect(prettyGoDuration('15m0s')).toBe('15m');
    expect(prettyGoDuration('24h0m0s')).toBe('24h');
    expect(prettyGoDuration('1m30s')).toBe('1m30s');
    expect(prettyGoDuration('0s')).toBe('0s');
  });

  it('counts down to a deadline', () => {
    const now = Date.parse('2026-10-12T10:00:00Z');
    expect(countdown('2026-10-12T10:14:05Z', now)).toBe('14:05');
    expect(countdown('2026-10-12T12:30:00Z', now)).toBe('2h 30m');
    expect(countdown('2026-10-12T09:00:00Z', now)).toBe('0:00');
  });
});

// §14.2. Locale and time zone are pinned so the strings match the plan.
describe('format (v0.2)', () => {
  const en = { locale: 'en-GB' };

  it('formats money', () => {
    expect(money(0, en)).toBe('$0.00');
    expect(money(31, en)).toBe('<$0.01');
    expect(money(9_999, en)).toBe('<$0.01');
    expect(money(10_000, en)).toBe('$0.01');
    expect(money(70_000, en)).toBe('$0.07');
    expect(money(12_400_000, en)).toBe('$12.40');
    expect(money(999_994_000, en)).toBe('$999.99');
    expect(money(1_240_000_000, en)).toBe('$1,240');
    expect(money(1_240_400_000, en)).toBe('$1,240');
    expect(money(null, en)).toBe('$0.00');
    expect(money(5, { local: true })).toBe('Free');
    expect(money(5, { known: false })).toBe('Price not set');
    expect(money(-2_500_000, en)).toBe('-$2.50');
  });

  it('rounds estimates up', () => {
    expect(estimate(170_000, en)).toBe('about $0.20');
    expect(estimate(200_000, en)).toBe('about $0.20');
    expect(estimate(12_300, en)).toBe('about $0.02');
    expect(estimate(950_000, en)).toBe('about $1.00');
    expect(estimate(1_230_000, en)).toBe('about $1.30');
    expect(estimate(1_300_000, en)).toBe('about $1.30');
    expect(estimate(12_310_000, en)).toBe('about $12.40');
    expect(estimate(1_234_500_000, en)).toBe('about $1,235');
    expect(estimate(4_000, en)).toBe('less than $0.01');
    expect(estimate(0, en)).toBe('Free');
    expect(estimate(5, { local: true })).toBe('Free');
    expect(estimate(5, { known: false })).toBe('Price not set');
  });

  it('says how long ago, then the date', () => {
    const now = Date.parse('2026-10-14T15:30:00Z');
    const o = { ...en, timeZone: 'UTC', now };
    expect(relTime('2026-10-14T15:29:31Z', o)).toBe('just now');
    expect(relTime('2026-10-14T15:25:00Z', o)).toBe('5 min ago');
    expect(relTime('2026-10-14T14:30:01Z', o)).toBe('59 min ago');
    expect(relTime('2026-10-14T13:20:00Z', o)).toBe('2 h ago');
    expect(relTime('2026-10-14T00:10:00Z', o)).toBe('15 h ago');
    expect(relTime('2026-10-13T14:02:00Z', o)).toBe('Yesterday at 14:02');
    expect(relTime('2026-10-13T09:05:00Z', o)).toBe('Yesterday at 9:05');
    expect(relTime('2026-10-12T09:00:00Z', o)).toBe('12 Oct');
    expect(relTime('2026-01-02T09:00:00Z', o)).toBe('2 Jan');
    expect(relTime('2025-10-14T09:00:00Z', o)).toBe('14 Oct 2025');
    expect(relTime('2026-10-14T15:35:00Z', o)).toBe('in 5 min');
    expect(relTime('2026-10-14T18:30:00Z', o)).toBe('in 3 h');
    expect(relTime('2026-10-15T09:00:00Z', o)).toBe('Tomorrow at 9:00');
    expect(relTime('0001-01-01T00:00:00Z', o)).toBe('');
    expect(relTime(null, o)).toBe('');
  });

  it('counts calendar days in the given time zone', () => {
    // 23:30 in Santiago is already the next day in UTC.
    const now = Date.parse('2026-10-14T02:30:00Z');
    expect(relTime('2026-10-13T22:00:00Z', { ...en, now, timeZone: 'America/Santiago' })).toBe('4 h ago');
    expect(relTime('2026-10-13T22:00:00Z', { ...en, now, timeZone: 'UTC' })).toBe('Yesterday at 22:00');
  });

  it('follows the locale for the clock', () => {
    const now = Date.parse('2026-10-14T15:30:00Z');
    expect(relTime('2026-10-13T14:02:00Z', { locale: 'en-US', timeZone: 'UTC', now })).toBe('Yesterday at 2:02 PM');
    expect(relTime('2026-10-12T09:00:00Z', { locale: 'en-US', timeZone: 'UTC', now })).toBe('Oct 12');
  });

  it('gives the full date for titles', () => {
    expect(fullTime('2026-10-14T10:42:00Z', { ...en, timeZone: 'UTC' })).toBe('14 October 2026, 10:42');
    expect(fullTime(undefined)).toBe('');
  });

  it('formats durations without milliseconds', () => {
    expect(duration(0)).toBe('less than a second');
    expect(duration(840e6)).toBe('less than a second');
    expect(duration(3e9)).toBe('3 s');
    expect(duration(2.6e9)).toBe('3 s');
    expect(duration(59.4e9)).toBe('59 s');
    expect(duration(59.6e9)).toBe('1 min');
    expect(duration(130e9)).toBe('2 min 10 s');
    expect(duration(3_900e9)).toBe('1 h 5 min');
    expect(duration(7_200e9)).toBe('2 h');
  });

  it('counts down in words', () => {
    const now = Date.parse('2026-10-14T10:00:00Z');
    expect(expiresIn('2026-10-14T10:14:57Z', { now })).toBe('Expires in 15 min');
    expect(expiresIn('2026-10-14T10:14:00Z', { now })).toBe('Expires in 14 min');
    expect(expiresIn('2026-10-14T10:00:59Z', { now })).toBe('Expires in less than a minute');
    expect(expiresIn('2026-10-14T10:59:40Z', { now })).toBe('Expires in 1 h');
    expect(expiresIn('2026-10-14T12:00:00Z', { now })).toBe('Expires in 2 h');
    expect(expiresIn('2026-10-15T08:00:00Z', { now })).toBe('Expires in 22 h');
    expect(expiresIn('2026-10-17T10:00:00Z', { now })).toBe('Expires in 3 days');
    expect(expiresIn('2026-10-14T09:59:00Z', { now })).toBe('Expired');
    expect(expiresIn(null, { now })).toBe('');
  });

  it('pluralises with locale grouping', () => {
    expect(plural(1, 'file')).toBe('1 file');
    expect(plural(3, 'file')).toBe('3 files');
    expect(plural(0, 'file')).toBe('0 files');
    expect(plural(1204, 'entry', 'entries', 'en-GB')).toBe('1,204 entries');
  });
});

describe('helpers', () => {
  it('diffs lines', () => {
    expect(lineDiff('a\nb\nc', 'a\nc\nd')).toBe(' a\n-b\n c\n+d');
    expect(lineDiff('', 'x')).toBe('+x');
  });
});
