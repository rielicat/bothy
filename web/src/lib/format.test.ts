import { describe, expect, it } from 'vitest';
import { lineDiff } from './diff';
import { bytes, countdown, duration, parseGoDuration, prettyGoDuration, tokens, usd } from './format';
import { strength } from './password';
import { windowNote } from './providers';

describe('format', () => {
  it('formats micro-dollars', () => {
    expect(usd(0)).toBe('$0');
    expect(usd(4_200_000)).toBe('$4.2');
    expect(usd(18_400)).toBe('$0.018');
    expect(usd(31)).toBe('<$0.0001');
    expect(usd(250_000_000)).toBe('$250');
    expect(usd(5, { known: false })).toBe('price unknown');
  });

  it('formats tokens, bytes and durations', () => {
    expect(tokens(950)).toBe('950');
    expect(tokens(1500)).toBe('1.5K');
    expect(tokens(200_000)).toBe('200K');
    expect(tokens(1_250_000)).toBe('1.3M');
    expect(bytes(2048)).toBe('2 KB');
    expect(duration(840e6)).toBe('840 ms');
    expect(duration(2_100e6)).toBe('2.1 s');
    expect(duration(125e9)).toBe('2m 5s');
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

describe('helpers', () => {
  it('diffs lines', () => {
    expect(lineDiff('a\nb\nc', 'a\nc\nd')).toBe(' a\n-b\n c\n+d');
    expect(lineDiff('', 'x')).toBe('+x');
  });

  it('rates passwords locally without a word list', () => {
    expect(strength('short').score).toBe(0);
    expect(strength('aaaaaaaaaaaaaa').score).toBeLessThanOrEqual(1);
    expect(strength('Granite-River-42-Lantern').score).toBe(4);
  });

  it('labels small context windows', () => {
    expect(windowNote(200_000)).toBe('');
    expect(windowNote(16_384)).toBe('compact mode');
    expect(windowNote(12_000)).toBe('degraded');
    expect(windowNote(4096)).toBe('text-only');
  });
});
