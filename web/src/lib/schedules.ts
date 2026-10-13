import { api, qs } from './api/client';
import type { Grant, Schedule, ScheduleUpsertReq } from './api/types.gen';

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

export const cronPresets = [
  { label: 'Every morning at 08:00', spec: '0 8 * * *' },
  { label: 'Weekdays at 08:00', spec: '0 8 * * 1-5' },
  { label: 'Every evening at 18:00', spec: '0 18 * * *' },
  { label: 'Mondays at 09:00', spec: '0 9 * * 1' },
  { label: 'Every hour', spec: '0 * * * *' },
];

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
