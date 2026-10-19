import type { ChatRef, Schedule, Session, ToolCallRecord } from '../../lib/api/types.gen';
import { appName, outcome, where, type Outcome } from '../../lib/taskStatus';
import { personName, votesOf, type TaskRun, type VotedApproval } from './api';

/** Where results go, after "Posts" or "Posted": each of an account's results gets a chat in its list, since accounts have no chat app. */
export function deliverTo(ref: ChatRef | undefined): string {
  return !ref || ref.channel === 'web' ? 'to your chats' : `to ${appName(ref.channel)}`;
}

/** A recipient permission as people read it in "Allowed without asking". */
export function sendSentence(value: string): string {
  // Connected tools key recipients by their own field (an address, a pattern), not by chat.
  if (!value.includes(':')) return `Send messages to ${value}`;
  return `Send messages to a chat on ${appName(value.split(':')[0])}`;
}

export interface TaskLookups {
  names: Map<string, string>;
  sessions: Map<string, Session>;
  schedules: Map<string, Schedule>;
  /** Pending approvals by run id, for "(1 of 2)". */
  waiting: Map<string, VotedApproval>;
}

export interface TaskView {
  title: string;
  /** Line 2 before the outcome: who and where, or "Schedule". */
  lead: string[];
  outcome: Outcome;
}

export function taskView(r: TaskRun, l: TaskLookups): TaskView {
  const scheduled = r.trigger === 'schedule' || r.trigger === 'heartbeat';
  const chat = l.sessions.get(r.session_id);
  const schedule = r.schedule_id ? l.schedules.get(r.schedule_id) : undefined;
  let out = outcome({ status: r.status, steps: r.steps, reason: r.reason, refined: r.refined, votes: votesOf(l.waiting.get(r.id)) });
  if (scheduled) {
    // A finished schedule says where its result went instead of counting steps. A check-in with
    // nothing to report posts nothing: its chat stays out of the list.
    if (r.status === 'done' && schedule && r.trigger === 'schedule') out = { text: `Posted ${deliverTo(schedule.deliver)}`, tone: 'muted' };
    // A run keeps no name of its own: once its schedule is deleted, say so.
    const title = r.schedule_name || schedule?.name || (r.trigger === 'heartbeat' ? 'Check-in' : r.schedule_id ? 'A schedule' : 'Deleted schedule');
    return { title, lead: ['Schedule'], outcome: out };
  }
  return {
    title: r.chat_title || chat?.title || 'A chat',
    lead: [personName(r.user_id, l.names, r.requested_by_name), where(r.origin, r.trigger)],
    outcome: out,
  };
}

/**
 * The outcome a task page adds to its meta line. A finished task needs none, and a waiting
 * task already shows its waiting step; a running one says "Working…", since it may have no step yet.
 */
export function detailOutcome(r: TaskRun): Outcome | null {
  if (['done', 'waiting_approval', 'parked'].includes(r.status)) return null;
  return outcome({ status: r.status, steps: r.steps, reason: r.reason });
}

const GITHUB_AUTH = /bad credentials|\b401\b|token (was )?(rejected|expired|revoked)|unauthori[sz]ed/i;

/** Whether a failed task failed because GitHub stopped accepting Bothy's access, which only an admin can fix in Settings. */
export function githubAuthFailed(r: Pick<TaskRun, 'status' | 'reason'>, calls: Pick<ToolCallRecord, 'tool' | 'status' | 'result_preview'>[]): boolean {
  if (r.status !== 'failed') return false;
  if (/github/i.test(r.reason ?? '') && GITHUB_AUTH.test(r.reason ?? '')) return true;
  return calls.some((c) => /^(github_|repo_)/.test(c.tool) && c.status === 'error' && GITHUB_AUTH.test(c.result_preview ?? ''));
}

/** A CodeBlock's maxLines that never hides just a line or two behind "Show all". */
export function clampLines(code: string, max: number): number | undefined {
  return code.replace(/\n$/, '').split('\n').length > max + 2 ? max : undefined;
}

/** Day groups for a list sorted newest first: "Today", "Yesterday", then dates. */
export function dayLabel(t: string, now = Date.now(), locale?: string): string {
  const d = new Date(t);
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(new Date(now)) - day(d)) / 86_400_000);
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Yesterday';
  const sameYear = d.getFullYear() === new Date(now).getFullYear();
  return d.toLocaleDateString(locale, sameYear ? { weekday: 'long', day: 'numeric', month: 'long' } : { day: 'numeric', month: 'long', year: 'numeric' });
}

export function groupByDay<T>(items: T[], at: (x: T) => string, now = Date.now()): { label: string; items: T[] }[] {
  const out: { label: string; items: T[] }[] = [];
  for (const x of items) {
    const label = dayLabel(at(x), now);
    const last = out[out.length - 1];
    if (last?.label === label) last.items.push(x);
    else out.push({ label, items: [x] });
  }
  return out;
}
