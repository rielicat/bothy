// Activity's API types and shared lookups. The generated server types,
// plus the few fields some records were stored without.
import { api } from '../../lib/api/client';
import type {
  Approval,
  AuditEntry,
  AuditRow as ServerAuditRow,
  AuditVerify,
  DeployView,
  EgressRecord,
  ReleaseEnvironment,
  RunDetail,
  RunView,
  Session,
  UsageRowView,
  User,
} from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';

export type { RefineEntry, RefineTrace, RefineVote, ReleaseEnvironment } from '../../lib/api/types.gen';

export type TaskRun = RunView;

export type TaskDetail = Omit<RunDetail, 'run'> & { run?: TaskRun; answer?: string };

export type Deploy = DeployView;

export interface Releases {
  environments: ReleaseEnvironment[];
  deploys: (Deploy | undefined)[];
}

export type VotedApproval = Approval & { requested_by_name?: string };

export type AuditCheck = AuditVerify & { checked?: number; bad_at?: string };

// Optional here so the line builders keep their own wording for rows recorded without them.
export type AuditRow = AuditEntry & Partial<Pick<ServerAuditRow, 'actor_name' | 'summary'>>;
export type EgressRow = EgressRecord & { outcome?: 'reached' | 'allowed_unreachable' | 'blocked' };
export type NamedUsageRow = Omit<UsageRowView, 'name'> & { name?: string };

/** Allow votes so far and votes needed, for "(1 of 2)". */
export function votesOf(a: VotedApproval | undefined): { approved: number; required: number } | undefined {
  const required = a?.approver?.required ?? 0;
  if (!a || required < 2) return undefined;
  return { approved: (a.votes ?? []).filter((v) => v.choice !== 'deny').length, required };
}

/** Display names by user id. Members can't list people, so they know only themselves. */
export async function people(): Promise<Map<string, string>> {
  const out = new Map<string, string>();
  const me = session.me?.user;
  if (session.isAdmin) for (const u of await api.get<User[]>('/api/users').catch(() => [] as User[])) out.set(u.id, u.display_name || u.username);
  if (me) out.set(me.id, me.display_name || me.username);
  return out;
}

/** "You" for the viewer, then the name, then a neutral word; never an id. */
export function personName(id: string | undefined, names: Map<string, string>, given?: string): string {
  if (id && id === session.me?.user.id) return 'You';
  return given || (id && names.get(id)) || 'Someone';
}

export async function sessionsById(): Promise<Map<string, Session>> {
  const list = await api.get<{ items: Session[] }>('/api/sessions?limit=200').catch(() => ({ items: [] as Session[] }));
  return new Map((list.items ?? []).map((s) => [s.id, s]));
}

// The Releases tab shows once an environment exists (§10), so every Activity
// page asks once; servers without releases answer 404 and the tab stays hidden.
let releases: Promise<Releases> | null = null;
export function loadReleases(fresh = false): Promise<Releases> {
  if (fresh || !releases) releases = api.get<Releases>('/api/releases').catch(() => ({ environments: [], deploys: [] }));
  return releases;
}
