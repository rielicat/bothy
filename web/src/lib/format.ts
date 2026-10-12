import type { Micros } from './api/types.gen';

const NS_PER_MS = 1e6;

export function usd(m: Micros | undefined | null, opts: { known?: boolean } = {}): string {
  if (opts.known === false) return 'price unknown';
  const v = (m ?? 0) / 1e6;
  if (v === 0) return '$0';
  const abs = Math.abs(v);
  if (abs < 0.0001) return v < 0 ? '-<$0.0001' : '<$0.0001';
  const digits = abs >= 100 ? 0 : abs >= 1 ? 2 : abs >= 0.01 ? 3 : 4;
  return (v < 0 ? '-$' : '$') + abs.toFixed(digits).replace(/(\.\d*?[1-9])0+$|\.0+$/, '$1');
}

export function micros(dollars: number): Micros {
  return Math.round(dollars * 1e6);
}

export function tokens(n: number | undefined | null): string {
  const v = n ?? 0;
  if (v < 1000) return String(v);
  if (v < 10_000) return (v / 1000).toFixed(1).replace(/\.0$/, '') + 'K';
  if (v < 1_000_000) return Math.round(v / 1000) + 'K';
  return (v / 1e6).toFixed(1).replace(/\.0$/, '') + 'M';
}

export function bytes(n: number | undefined | null): string {
  const v = n ?? 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1).replace(/\.0$/, '')} KB`;
  return `${(v / 1024 / 1024).toFixed(1).replace(/\.0$/, '')} MB`;
}

// Go time.Duration fields arrive as integer nanoseconds.
export function duration(ns: number | undefined | null): string {
  return ms((ns ?? 0) / NS_PER_MS);
}

export function ms(v: number): string {
  if (v < 1000) return `${Math.round(v)} ms`;
  const s = v / 1000;
  if (s < 60) return `${s.toFixed(1).replace(/\.0$/, '')} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

const ZERO_TIME = '0001-01-01T00:00:00Z';

export function isZeroTime(t: string | undefined | null): boolean {
  return !t || t.startsWith('0001-01-01');
}

export function date(t: string | undefined | null): Date | null {
  if (!t || t === ZERO_TIME || isZeroTime(t)) return null;
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? null : d;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto', style: 'short' });

export function ago(t: string | undefined | null, now = Date.now()): string {
  const d = date(t);
  if (!d) return 'never';
  const diff = (d.getTime() - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return 'just now';
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day');
  return d.toLocaleDateString();
}

export function when(t: string | undefined | null): string {
  const d = date(t);
  if (!d) return '—';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

export function clock(t: string | undefined | null): string {
  const d = date(t);
  return d ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : '';
}

export function countdown(t: string | undefined | null, now = Date.now()): string {
  const d = date(t);
  if (!d) return '';
  const s = Math.max(0, Math.round((d.getTime() - now) / 1000));
  if (s >= 3600) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

// Parses Go duration strings ("1h30m", "45s", "1.2s") into nanoseconds.
export function parseGoDuration(s: string): number | null {
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  const unit: Record<string, number> = { ns: 1, us: 1e3, µs: 1e3, ms: 1e6, s: 1e9, m: 60e9, h: 3600e9 };
  const t = s.trim();
  if (t === '0') return 0;
  let total = 0;
  let consumed = 0;
  for (const m of t.matchAll(re)) {
    if (m.index !== consumed) return null;
    total += parseFloat(m[1]) * unit[m[2]];
    consumed += m[0].length;
  }
  return consumed === t.length && consumed > 0 ? total : null;
}

// "15m0s" -> "15m", "24h0m0s" -> "24h".
export function prettyGoDuration(s: string): string {
  return s.replace(/(\D)0s$/, '$1').replace(/(\D)0m$/, '$1') || s;
}
