import type { Micros } from './api/types.gen';

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

// v0.1 latency label; v0.2 screens use duration().
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
  return d ? d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : '';
}

export function countdown(t: string | undefined | null, now = Date.now()): string {
  const d = date(t);
  if (!d) return '';
  const s = Math.max(0, Math.round((d.getTime() - now) / 1000));
  if (s >= 3600) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
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

// v0.2 formats (docs/design/ui.md §14.2). `locale` and `timeZone` default to
// the browser's; tests pin them.
export interface FormatOpts {
  locale?: string;
  timeZone?: string;
  now?: number;
}

const formats = new Map<string, Intl.NumberFormat | Intl.DateTimeFormat>();

function currency(locale: string | undefined, digits: number): Intl.NumberFormat {
  const key = `n|${locale}|${digits}`;
  let f = formats.get(key) as Intl.NumberFormat | undefined;
  if (!f) {
    f = new Intl.NumberFormat(locale, { style: 'currency', currency: 'USD', currencyDisplay: 'narrowSymbol', minimumFractionDigits: digits, maximumFractionDigits: digits });
    formats.set(key, f);
  }
  return f;
}

function dateFormat(locale: string | undefined, timeZone: string | undefined, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = `d|${locale}|${timeZone}|${JSON.stringify(opts)}`;
  let f = formats.get(key) as Intl.DateTimeFormat | undefined;
  if (!f) {
    f = new Intl.DateTimeFormat(locale, { ...opts, timeZone });
    formats.set(key, f);
  }
  return f;
}

/** Money from micro-dollars: "<$0.01", "$12.40", "$1,240"; "Free" for local models, "Price not set" when unknown. */
export function money(m: Micros | undefined | null, opts: { local?: boolean; known?: boolean; locale?: string } = {}): string {
  if (opts.local) return 'Free';
  if (opts.known === false) return 'Price not set';
  const v = (m ?? 0) / 1e6;
  const abs = Math.abs(v);
  const sign = v < 0 ? '-' : '';
  if (abs > 0 && abs < 0.01) return `${sign}<${currency(opts.locale, 2).format(0.01)}`;
  return sign + currency(opts.locale, abs >= 1000 ? 0 : 2).format(abs);
}

// Rounds up so an estimate never undersells: one significant figure under $1,
// the next $0.10 below $1,000, whole dollars above. The epsilon keeps float
// noise (0.3 / 0.1 = 2.9999…) from pushing an exact value up a step.
function roundUp(v: number): number {
  const step = v < 1 ? 10 ** Math.floor(Math.log10(v)) : v < 1000 ? 0.1 : 1;
  return Math.ceil(v / step - 1e-9) * step;
}

/** A cost shown before the work: "about $0.20", "about $1.30", "less than $0.01". */
export function estimate(m: Micros | undefined | null, opts: { local?: boolean; known?: boolean; locale?: string } = {}): string {
  if (opts.local) return 'Free';
  if (opts.known === false) return 'Price not set';
  const v = Math.max(0, (m ?? 0) / 1e6);
  if (v === 0) return 'Free';
  if (v < 0.01) return `less than ${currency(opts.locale, 2).format(0.01)}`;
  const r = roundUp(v);
  return `about ${currency(opts.locale, r >= 1000 ? 0 : 2).format(r)}`;
}

function dayNumber(d: Date, timeZone: string | undefined): number {
  const parts = dateFormat('en-CA', timeZone, { year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(d);
  const get = (t: string) => Number(parts.find((p) => p.type === t)?.value);
  return Date.UTC(get('year'), get('month') - 1, get('day')) / 86_400_000;
}

function clockTime(d: Date, opts: FormatOpts): string {
  return dateFormat(opts.locale, opts.timeZone, { hour: 'numeric', minute: '2-digit' }).format(d);
}

/** "just now", "5 min ago", "2 h ago" (same day), "Yesterday at 14:02", "14 Oct", "14 Oct 2025"; future times read "in 5 min", "Tomorrow at 9:00". */
export function relTime(t: string | undefined | null, opts: FormatOpts = {}): string {
  const d = date(t);
  if (!d) return '';
  const now = opts.now ?? Date.now();
  const past = now - d.getTime();
  const abs = Math.abs(past);
  if (abs < 60_000) return 'just now';
  if (abs < 3_600_000) {
    const m = Math.floor(abs / 60_000);
    return past < 0 ? `in ${m} min` : `${m} min ago`;
  }
  const days = dayNumber(new Date(now), opts.timeZone) - dayNumber(d, opts.timeZone);
  if (days === 0) {
    const h = Math.floor(abs / 3_600_000);
    return past < 0 ? `in ${h} h` : `${h} h ago`;
  }
  if (days === 1) return `Yesterday at ${clockTime(d, opts)}`;
  if (days === -1) return `Tomorrow at ${clockTime(d, opts)}`;
  const year = (x: Date) => dateFormat('en-CA', opts.timeZone, { year: 'numeric' }).format(x);
  const style: Intl.DateTimeFormatOptions = year(d) === year(new Date(now)) ? { day: 'numeric', month: 'short' } : { day: 'numeric', month: 'short', year: 'numeric' };
  return dateFormat(opts.locale, opts.timeZone, style).format(d);
}

/** The full date for the title or aria-label of a relative time: "14 October 2026, 10:42". */
export function fullTime(t: string | undefined | null, opts: Omit<FormatOpts, 'now'> = {}): string {
  const d = date(t);
  if (!d) return '';
  return `${dateFormat(opts.locale, opts.timeZone, { day: 'numeric', month: 'long', year: 'numeric' }).format(d)}, ${clockTime(d, opts)}`;
}

/** A Go time.Duration (nanoseconds) as "3 s", "2 min 10 s", "1 h 5 min"; never "0 ms". Callers add "Took". */
export function duration(ns: number | undefined | null): string {
  const seconds = (ns ?? 0) / 1e9;
  if (seconds < 1) return 'less than a second';
  const s = Math.round(seconds);
  if (s < 60) return `${s} s`;
  if (s < 3600) {
    const m = Math.floor(s / 60);
    return s % 60 ? `${m} min ${s % 60} s` : `${m} min`;
  }
  const total = Math.round(s / 60);
  const h = Math.floor(total / 60);
  return total % 60 ? `${h} h ${total % 60} min` : `${h} h`;
}

/** "Expires in 14 min", "Expires in 2 h", "Expires in less than a minute", "Expired". */
export function expiresIn(t: string | undefined | null, opts: Pick<FormatOpts, 'now'> = {}): string {
  const d = date(t);
  if (!d) return '';
  const s = (d.getTime() - (opts.now ?? Date.now())) / 1000;
  if (s <= 0) return 'Expired';
  if (s < 60) return 'Expires in less than a minute';
  const min = Math.round(s / 60);
  if (min < 60) return `Expires in ${min} min`;
  const h = Math.round(s / 3600);
  if (h < 24) return `Expires in ${h} h`;
  return `Expires in ${plural(Math.round(s / 86_400), 'day')}`;
}

/** "1 file", "3 files", "1,204 entries". */
export function plural(n: number, one: string, many = one + 's', locale?: string): string {
  return `${n.toLocaleString(locale)} ${n === 1 ? one : many}`;
}

// The services a plain capital spells wrong: MCP servers are named by their
// lowercase config ids. internal/core/names.go has the same list.
const serviceNames: Record<string, string> = {
  bigquery: 'BigQuery',
  circleci: 'CircleCI',
  clickup: 'ClickUp',
  deepwiki: 'DeepWiki',
  dynamodb: 'DynamoDB',
  github: 'GitHub',
  gitlab: 'GitLab',
  'google-drive': 'Google Drive',
  hubspot: 'HubSpot',
  launchdarkly: 'LaunchDarkly',
  mongodb: 'MongoDB',
  mysql: 'MySQL',
  openai: 'OpenAI',
  pagerduty: 'PagerDuty',
  posthog: 'PostHog',
  postgres: 'Postgres',
  postgresql: 'PostgreSQL',
  pypi: 'PyPI',
  sendgrid: 'SendGrid',
  sqlite: 'SQLite',
  youtube: 'YouTube',
};

/** The first letter up: "deploy" → "Deploy". */
export const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** An id in words, sentence case: "create_issue" → "Create issue". */
export const humanise = (s: string) => capital(s.replace(/[-_]+/g, ' ').trim());

/** A configured MCP server's name for people: "deepwiki" → "DeepWiki", "team-docs" → "Team docs". */
export function serviceName(id: string): string {
  const known = serviceNames[id.toLowerCase()];
  if (known) return known;
  return humanise(id);
}

/**
 * A skill's name for people: the SKILL.md front matter's title or its first
 * heading when the file is at hand, else the folder id in words, sentence
 * case ("incident-review" → "Incident review"). The id stays in file paths
 * and Details.
 */
export function skillName(slug: string, body = ''): string {
  const front = /^---\n([\s\S]*?)\n---\n/.exec(body);
  const title = front && /^title:\s*["']?(.+?)["']?\s*$/m.exec(front[1])?.[1];
  if (title) return title;
  const heading = /^\s*#\s+(.+)$/m.exec(front ? body.slice(front[0].length) : body)?.[1]?.trim();
  // A heading that only repeats the folder id is no name either.
  if (heading && heading.toLowerCase() !== slug.toLowerCase()) return heading;
  const words = slug.replace(/[-_]+/g, ' ').trim();
  return words ? words[0].toUpperCase() + words.slice(1).toLowerCase() : slug;
}
