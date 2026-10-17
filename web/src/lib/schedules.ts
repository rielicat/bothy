import { api, qs } from './api/client';
import type { Grant, Schedule, ScheduleKind, ScheduleUpsertReq } from './api/types.gen';

export async function scheduleGrants(id: string): Promise<Grant[]> {
  return api.get<Grant[]>(`/api/grants${qs({ scope: 'schedule', scope_id: id })}`);
}

// Go decodes time.Time only from RFC 3339, so unset times go out as the zero time.
export const ZERO = '0001-01-01T00:00:00Z';
const z = (t: string) => t || ZERO;

// PATCH replaces the schedule's grants, so every save resends the ones to keep.
export function saveSchedule(s: Schedule, grants: Grant[]): Promise<Schedule> {
  const schedule = { ...s, next_run_at: z(s.next_run_at), last_run_at: z(s.last_run_at), created_at: z(s.created_at), updated_at: z(s.updated_at) };
  const body: ScheduleUpsertReq = { schedule, grants: grants.map((g) => ({ ...g, created_at: z(g.created_at), expires_at: z(g.expires_at) })) };
  return s.id ? api.patch<Schedule>(`/api/schedules/${encodeURIComponent(s.id)}`, body) : api.post<Schedule>('/api/schedules', body);
}

export function timezones(): string[] {
  try {
    return Intl.supportedValuesOf('timeZone');
  } catch {
    return ['UTC'];
  }
}

export function localZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
}

/** "Europe/London" → "London (Europe)", so typing a city finds it. */
export function zoneLabel(z: string): string {
  const parts = z.split('/').map((p) => p.replace(/_/g, ' '));
  const city = parts.pop() ?? z;
  return parts.length ? `${city} (${parts.join(', ')})` : city;
}

/** A zone in a sentence, by its city: "London time", "New York time"; "UTC" as it is. */
export function zoneTime(z: string): string {
  const city = z.split('/').pop()?.replace(/_/g, ' ');
  return z.includes('/') && city ? `${city} time` : z;
}

/** Every zone the browser knows, by city, plus `current` when the browser leaves it out (browsers list no "UTC"). */
export function zoneOptions(current: string): { value: string; label: string }[] {
  const list = timezones();
  if (current && !list.includes(current)) list.unshift(current);
  return list.map((z) => ({ value: z, label: zoneLabel(z) }));
}

// The wall-clock time of d in tz as YYYY-MM-DDTHH:MM, which is what the
// server reads in the schedule's tz for a once spec without an offset.
export function wallClock(d: Date, tz: string): string {
  try {
    const f = new Intl.DateTimeFormat('en-US', { timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    const p = Object.fromEntries(f.formatToParts(d).map((x) => [x.type, x.value]));
    return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}`;
  } catch {
    // a zone the browser does not know: show UTC
    return d.toISOString().slice(0, 16);
  }
}

// A saved once spec is RFC 3339 with an offset; the form edits it as
// wall-clock time in the schedule's tz.
export function onceInput(spec: string, tz: string): string {
  if (!/(Z|[+-]\d\d:\d\d)$/i.test(spec)) return spec;
  const d = new Date(spec);
  return Number.isNaN(d.getTime()) ? spec : wallClock(d, tz);
}

// "When" in the schedule form (docs/design/ui.md §10.3). Cron shows only
// under Custom; every other choice maps to one fixed cron shape and back.
export type WhenKind = 'daily' | 'weekdays' | 'weekly' | 'monthly' | 'once' | 'custom';

export interface When {
  kind: WhenKind;
  /** HH:MM, 24 h. */
  time: string;
  /** Weekday 0–6 (Sunday first) for weekly, day of the month 1–31 for monthly. */
  day: number;
  /** YYYY-MM-DD for once. */
  date: string;
  /** The cron expression under Custom. */
  rule: string;
}

export const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

const pad = (n: number) => String(n).padStart(2, '0');

export function toSpec(w: When): { kind: ScheduleKind; spec: string } {
  const [h, m] = w.time.split(':').map(Number);
  const hm = `${m || 0} ${h || 0}`;
  switch (w.kind) {
    case 'daily':
      return { kind: 'cron', spec: `${hm} * * *` };
    case 'weekdays':
      return { kind: 'cron', spec: `${hm} * * 1-5` };
    case 'weekly':
      return { kind: 'cron', spec: `${hm} * * ${w.day}` };
    case 'monthly':
      return { kind: 'cron', spec: `${hm} ${w.day} * *` };
    case 'once':
      return { kind: 'once', spec: `${w.date}T${w.time}` };
    default:
      return { kind: 'cron', spec: w.rule.trim().replace(/\s+/g, ' ') };
  }
}

const num = (s: string, max: number, min = 0) => (/^\d{1,2}$/.test(s) && +s >= min && +s <= max ? +s : null);

/** Reads a saved schedule back into the form's choices; anything else is Custom. */
export function fromSpec(kind: string, spec: string, tz = 'UTC', now = new Date()): When {
  // "Once on" starts tomorrow: today at 9:00 has often passed already.
  const base: When = { kind: 'custom', time: '09:00', day: 1, date: wallClock(new Date(now.getTime() + 86_400_000), tz).slice(0, 10), rule: spec };
  if (kind === 'once') {
    const wall = onceInput(spec, tz);
    const m = /^(\d{4}-\d\d-\d\d)T(\d\d:\d\d)/.exec(wall);
    return m ? { ...base, kind: 'once', date: m[1], time: m[2], rule: '' } : base;
  }
  if (kind !== 'cron') return base;
  const f = spec.trim().split(/\s+/);
  if (f.length !== 5 || f[3] !== '*') return base;
  const min = num(f[0], 59);
  const hour = num(f[1], 23);
  if (min === null || hour === null) return base;
  const at = { ...base, time: `${pad(hour)}:${pad(min)}`, rule: '' };
  const [dom, , dow] = [f[2], f[3], f[4]];
  if (dom === '*' && dow === '*') return { ...at, kind: 'daily' };
  if (dom === '*' && dow === '1-5') return { ...at, kind: 'weekdays' };
  const wd = dom === '*' ? num(dow, 7) : null;
  if (wd !== null) return { ...at, kind: 'weekly', day: wd % 7 };
  const md = dow === '*' ? num(dom, 31, 1) : null;
  if (md !== null) return { ...at, kind: 'monthly', day: md };
  return base;
}

/** A wall-clock HH:MM in the reader's 12 or 24 h style: "9:00", "9:00 AM". */
export function clockLabel(hm: string, locale?: string): string {
  const [h, m] = hm.split(':').map(Number);
  return new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit', timeZone: 'UTC' }).format(Date.UTC(2000, 0, 1, h || 0, m || 0));
}

/** Times in the server's own wording ("Weekdays at 08:00") in the reader's style, to match the rest of the page. */
export function localTimes(text: string, locale?: string): string {
  return text.replace(/\b([01]?\d|2[0-3]):([0-5]\d)\b/g, (hm) => clockLabel(hm, locale));
}

/** "Weekdays at 9:00", "Mondays at 8:00", "Every month on day 1 at 9:00", "Once on 16 Oct at 9:00"; null for Custom. */
export function describeWhen(w: When, locale?: string): string | null {
  const at = `at ${clockLabel(w.time, locale)}`;
  switch (w.kind) {
    case 'daily':
      return `Every day ${at}`;
    case 'weekdays':
      return `Weekdays ${at}`;
    case 'weekly':
      return `${WEEKDAYS[w.day]}s ${at}`;
    case 'monthly':
      return `Every month on day ${w.day} ${at}`;
    case 'once': {
      const [y, mo, d] = w.date.split('-').map(Number);
      const day = new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(Date.UTC(y, mo - 1, d));
      return `Once on ${day} ${at}`;
    }
    default:
      return null;
  }
}

/** A repeat rule has five fields of digits, ranges, lists and steps. The server has the final word. */
export function validRule(rule: string): boolean {
  const f = rule.trim().split(/\s+/);
  return f.length === 5 && f.every((x) => /^(\*|\d+(-\d+)?)(\/\d+)?(,(\*|\d+(-\d+)?)(\/\d+)?)*$/.test(x));
}

/** Every half hour, as select options, plus the current value when it falls between. */
export function timeOptions(current: string, locale?: string): { value: string; label: string }[] {
  const out: string[] = [];
  for (let i = 0; i < 48; i++) out.push(`${pad(Math.floor(i / 2))}:${i % 2 ? '30' : '00'}`);
  if (current && !out.includes(current)) out.push(current);
  return out.sort().map((v) => ({ value: v, label: clockLabel(v, locale) }));
}
