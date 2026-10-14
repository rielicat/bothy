// "If nobody answers" choices (§11.6) over policy.approval.*: a timeout, then
// deny, or park (keep waiting up to park_ttl). A value set elsewhere that
// matches no choice is shown as its own option, so nothing is silently changed.
import type { SelectOption } from '../../../components/ui';
import { parseGoDuration } from '../../../lib/format';

const P = 'policy.approval.';

/** Nanoseconds as words: "90 seconds", "15 minutes", "1 hour", "24 hours", "3 days". */
export function longDuration(ns: number): string {
  const s = Math.round(ns / 1e9);
  const unit = (n: number, one: string) => `${n} ${n === 1 ? one : one + 's'}`;
  if (s < 120 && s % 60) return unit(s, 'second');
  const m = Math.round(s / 60);
  if (m < 60 || m % 60) return unit(m, 'minute');
  const h = m / 60;
  if (h <= 48 || h % 24) return unit(h, 'hour');
  return unit(h / 24, 'day');
}

const ns = (v: unknown, fallback: number) => parseGoDuration(String(v ?? '')) ?? fallback;
const H = 3600e9;
const M = 60e9;

export interface TimeoutField {
  value: string;
  options: SelectOption[];
}

export interface ApprovalConfig {
  interactiveTimeout: unknown;
  interactiveOnTimeout: unknown;
  scheduledTimeout: unknown;
  scheduledOnTimeout: unknown;
  parkTtl: unknown;
}

export function chatChoice(c: ApprovalConfig): TimeoutField {
  const park = longDuration(ns(c.parkTtl, 24 * H));
  const t = ns(c.interactiveTimeout, 15 * M);
  const options: SelectOption[] = [
    { value: 'deny-15m', label: 'Deny after 15 minutes' },
    { value: 'deny-1h', label: 'Deny after 1 hour' },
    { value: 'park', label: `Keep waiting (up to ${park})` },
  ];
  if (c.interactiveOnTimeout === 'park') return { value: 'park', options };
  if (t === 15 * M) return { value: 'deny-15m', options };
  if (t === H) return { value: 'deny-1h', options };
  return { value: 'current', options: [{ value: 'current', label: `Deny after ${longDuration(t)}` }, ...options] };
}

export function scheduledChoice(c: ApprovalConfig): TimeoutField {
  const park = longDuration(ns(c.parkTtl, 24 * H));
  const t = ns(c.scheduledTimeout, 2 * M);
  const options: SelectOption[] = [
    { value: 'park', label: `Wait up to ${park}` },
    { value: 'deny-1h', label: 'Deny after 1 hour' },
  ];
  if (c.scheduledOnTimeout !== 'deny') return { value: 'park', options };
  if (t === H) return { value: 'deny-1h', options };
  return { value: 'current', options: [{ value: 'current', label: `Deny after ${longDuration(t)}` }, ...options] };
}

/** The config writes for a chosen option; `current` writes nothing. */
export function chatChanges(value: string): Record<string, unknown> | null {
  if (value === 'deny-15m') return { [`${P}interactive_timeout`]: '15m', [`${P}interactive_on_timeout`]: 'deny' };
  if (value === 'deny-1h') return { [`${P}interactive_timeout`]: '1h', [`${P}interactive_on_timeout`]: 'deny' };
  if (value === 'park') return { [`${P}interactive_on_timeout`]: 'park' };
  return null;
}

export function scheduledChanges(value: string): Record<string, unknown> | null {
  if (value === 'deny-1h') return { [`${P}scheduled_timeout`]: '1h', [`${P}scheduled_on_timeout`]: 'deny' };
  if (value === 'park') return { [`${P}scheduled_on_timeout`]: 'park' };
  return null;
}
