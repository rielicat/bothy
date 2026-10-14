// Activity's API types and shared lookups.
//
// Local types, to swap for types.gen.ts in C10 once the server tracks land
// (docs/design/ui.md §17): S8 names on runs, usage, audit rows and deploys;
// S16 audit summaries and egress outcomes; the release, vote and refine
// shapes as internal/core on main defines them. Every extra field is
// optional, and each screen has a fallback for when it is missing.
import { api } from '../../lib/api/client';
import type { Approval, AuditEntry, AuditVerify, EgressRecord, Run, RunDetail, Session, UsageRow, User } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';

export type TaskRun = Run & {
  requested_by_name?: string;
  chat_title?: string;
  schedule_name?: string;
  /** The chat's origin: web, slack, telegram, discord, api, schedule. */
  origin?: string;
  /** A plain one-line reason for a failure. */
  reason?: string;
  refined?: boolean;
};

export interface RefineVote {
  answer: string;
  count: number;
  drafts: number[];
}

export interface RefineEntry {
  draft: number;
  round: number;
  role: string;
  text: string;
  model?: string;
  cost_micros: number;
  error?: string;
}

export interface RefineTrace {
  drafts: number;
  depth: number;
  rounds: number;
  early_stop: boolean;
  fallback?: string;
  winner: number;
  votes: RefineVote[];
  entries?: RefineEntry[];
  calls: number;
  cost_micros: number;
}

export type TaskDetail = Omit<RunDetail, 'run'> & { run?: TaskRun; refine?: RefineTrace; answer?: string };

export interface ReleaseEnvironment {
  name: string;
  repo: string;
  workflow: string;
  ref_pattern: string;
  required: number;
  approvers: { kind: string; id: string; name: string }[];
}

export interface Deploy {
  id: string;
  environment: string;
  repo: string;
  ref: string;
  session_id: string;
  run_id?: string;
  approval_id?: string;
  requested_by: string;
  approved_by: string[];
  status: string;
  run_url?: string;
  conclusion?: string;
  error?: string;
  created_at: string;
  finished_at: string;
  requested_by_name?: string;
  approved_by_names?: string[];
  sha?: string;
  subject?: string;
}

export interface Releases {
  environments: ReleaseEnvironment[];
  deploys: (Deploy | undefined)[];
}

export type VotedApproval = Approval & {
  votes?: { user_id: string; choice: string; at: string }[];
  approver: Approval['approver'] & { required?: number; requester?: string };
  requested_by_name?: string;
  decided_by_name?: string;
  comment?: string;
};

export type AuditRow = AuditEntry & { actor_name?: string; summary?: string };
export type EgressRow = EgressRecord & { outcome?: 'reached' | 'allowed_unreachable' | 'blocked' };
export type NamedUsageRow = UsageRow & { name?: string };
export type AuditCheck = AuditVerify & { checked?: number; bad_at?: string };

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
