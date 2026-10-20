import { toast } from '../../components/ui';
import { api, ApiError, refusal } from '../../lib/api/client';
import type { Grant, RunScheduleResp, Schedule, User } from '../../lib/api/types.gen';
import { toolGrantPhrase } from '../../lib/grants';
import { router } from '../../lib/router.svelte';
import { localZone } from '../../lib/schedules';
import { sendSentence } from './tasks';

/** Run now, from the list or the schedule's page: '' once it started (the toast opens its chat), else why not. */
export async function runNow(s: Schedule): Promise<string> {
  try {
    const chat = (await api.post<RunScheduleResp>(`/api/schedules/${encodeURIComponent(s.id)}/run`))?.session_id;
    toast('Schedule started.', chat ? { action: { label: 'Open chat', onclick: () => router.navigate(`/chat/${encodeURIComponent(chat)}`) } } : {});
    return '';
  } catch (e) {
    // An empty check-in list, or Bothy paused: trying again won't help.
    return refusal(e) || (e instanceof ApiError && e.code === 'paused' ? 'Bothy is paused by an admin, so nothing new starts.' : "Couldn't start it. Try again.");
  }
}

export function blankSchedule(user: User | undefined): Schedule {
  return {
    id: '',
    principal_id: user?.id ?? '',
    name: '',
    kind: 'cron',
    spec: '0 9 * * 1-5',
    tz: user?.timezone || localZone(),
    active_hours: '',
    prompt: '',
    slot: 'fast',
    limits: { max_steps: 0, max_tokens: 0, max_time: 0, max_cost_micros: 0 },
    missed_run: 'once',
    enabled: true,
    created_by: '',
    failures: 0,
    next_run_at: '',
    last_run_at: '',
    created_at: '',
    updated_at: '',
  };
}

/** The check-in (heartbeat) before it was ever saved: off, every 30 minutes, 8:00–22:00. */
export function checkinBlank(user: User | undefined): Schedule {
  return { ...blankSchedule(user), name: 'Check-in', kind: 'heartbeat', spec: '30m', active_hours: '08:00-22:00', enabled: false };
}

/** A heartbeat interval in Go duration form ("30m", "1h") as "Every 30 min", "Every hour". */
export function every(spec: string): string {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:0s)?$/.exec(spec.trim());
  const min = m ? Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0) : 30;
  if (min === 60) return 'Every hour';
  if (min > 60 && min % 60 === 0) return `Every ${min / 60} hours`;
  return `Every ${min || 30} min`;
}

/** The next run in the schedule's time zone: "today at 9:00", "tomorrow at 9:00", "Friday at 9:00", "16 Oct at 9:00". */
export function nextRun(t: string, tz: string, now = Date.now(), locale?: string): string {
  const d = new Date(t);
  if (Number.isNaN(d.getTime())) return '';
  let zone: string | undefined = tz;
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: tz });
  } catch {
    zone = undefined;
  }
  const ymd = (x: Date | number) => new Intl.DateTimeFormat('en-CA', { timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit' }).format(x);
  const days = Math.round((Date.parse(ymd(d)) - Date.parse(ymd(now))) / 86_400_000);
  const at = `at ${new Intl.DateTimeFormat(locale, { timeZone: zone, hour: 'numeric', minute: '2-digit' }).format(d)}`;
  if (days === 0) return `today ${at}`;
  if (days === 1) return `tomorrow ${at}`;
  if (days > 1 && days < 7) return `${new Intl.DateTimeFormat(locale, { timeZone: zone, weekday: 'long' }).format(d)} ${at}`;
  return `${new Intl.DateTimeFormat(locale, { timeZone: zone, day: 'numeric', month: 'short' }).format(d)} ${at}`;
}

/** Up to 48 characters of the prompt, cut at a word: the name until someone types one. */
export function nameFrom(prompt: string): string {
  const first = prompt.trim().split(/[.!?](?:\s|$)|\n/)[0].trim();
  if (first.length <= 48) return first;
  const cut = first.slice(0, 48);
  // Drops the half-phrase a cut leaves behind: "… pull requests in" reads "… pull requests".
  return cut
    .slice(0, cut.lastIndexOf(' ') > 20 ? cut.lastIndexOf(' ') : 48)
    .replace(/(\s+(in|on|at|to|and|or|the|a|an|of|for|with|from|by))+$/i, '')
    .replace(/[\s,;:]+$/, '');
}

/** A grant as the sentence people read in "Allowed without asking". */
export function grantSentence(g: Pick<Grant, 'kind' | 'value' | 'tool' | 'pattern'>): string {
  const host = (g.value ?? '').replace(/:443$/, '');
  if (g.kind === 'domain') return `Read websites on ${host}`;
  if (g.kind === 'recipient') return sendSentence(g.value ?? '');
  const globs = Object.values(g.pattern ?? {});
  return globs.length ? `${toolGrantPhrase(g.tool ?? '')} matching ${globs.join(', ')}` : toolGrantPhrase(g.tool ?? '');
}

/** A date in a list of dates, without the comma some locales put inside it: "Thu Oct 15", "Thu 15 Oct". */
export function listDate(t: string | number | Date, tz?: string, locale?: string): string {
  const opts: Intl.DateTimeFormatOptions = { weekday: 'short', day: 'numeric', month: 'short' };
  let f: Intl.DateTimeFormat;
  try {
    f = new Intl.DateTimeFormat(locale, { ...opts, timeZone: tz || undefined });
  } catch {
    f = new Intl.DateTimeFormat(locale, opts);
  }
  return f
    .formatToParts(new Date(t))
    .map((p) => (p.type === 'literal' ? p.value.replace(/,/g, ' ') : p.value))
    .join('')
    .replace(/\s+/g, ' ')
    .trim();
}

/** Website hosts mentioned in a prompt, for suggested permissions. */
export function hostsIn(prompt: string): string[] {
  const out = new Set<string>();
  for (const m of prompt.matchAll(/\b(?:https?:\/\/)?((?:[a-z0-9-]+\.)+(?:com|net|org|io|dev|app|co|ai|so|uk|eu|de))\b/gi)) out.add(m[1].toLowerCase());
  return [...out].slice(0, 3);
}

const ITEM = /^\s*-\s+(?:\[[ xX]\]\s+)?/;

/**
 * The check-in list as the form shows it: a file of "- [ ] …" items reads as
 * plain lines. A file with any line that isn't an item is shown as it is,
 * so saving never rewrites what someone wrote by hand.
 */
export function checkinText(content: string): { text: string; items: boolean } {
  const lines = content.split('\n').filter((l) => l.trim());
  if (!lines.every((l) => ITEM.test(l))) return { text: content, items: false };
  return { text: lines.map((l) => l.replace(ITEM, '').trimEnd()).join('\n'), items: true };
}

/** The file to save for what the form holds: each line back as an item when the file was a list. */
export function checkinFile(text: string, items: boolean): string {
  if (!items) return text;
  const lines = text.split('\n').filter((l) => l.trim());
  return lines.length ? lines.map((l) => `- [ ] ${l.trim()}`).join('\n') + '\n' : '';
}
