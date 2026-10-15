import { api } from '../../lib/api/client';
import type { ChannelIdentity, ChatRef, Schedule, Session, Space } from '../../lib/api/types.gen';
import { session } from '../../lib/stores/session.svelte';
import { appName, outcome, where, type Outcome } from '../../lib/taskStatus';
import { personName, votesOf, type TaskRun, type VotedApproval } from './api';

export interface Chats {
  identities: ChannelIdentity[];
  spaces: Space[];
}

export async function loadChats(): Promise<Chats> {
  const me = session.me?.user?.id;
  const [identities, spaces] = await Promise.all([
    api.get<ChannelIdentity[]>('/api/identities').catch(() => [] as ChannelIdentity[]),
    api.get<Space[]>('/api/spaces').catch(() => [] as Space[]),
  ]);
  // Admins are sent everyone's identities; a schedule can only post to its owner's own.
  return {
    identities: identities.filter((i) => i.status === 'linked' && i.dm_chat_id && i.user_id === me),
    spaces: spaces.filter((s) => s.kind === 'group'),
  };
}

/** Where results go, after "Posts" or "Posted": "here", "to your Telegram messages", "to #eng on Slack". */
export function deliverTo(ref: ChatRef | undefined, chats: Chats): string {
  if (!ref) return chats.identities.length ? 'to your chat app' : 'here';
  if (ref.channel === 'web') return 'here';
  const space = chats.spaces.find((s) => s.chat.channel === ref.channel && s.chat.chat_id === ref.chat_id);
  if (space) return `to ${space.title} on ${appName(ref.channel)}`;
  if (ref.kind === 'dm') return `to your ${appName(ref.channel)} messages`;
  return `to ${appName(ref.channel)}`;
}

/** A chat the way the server keys recipient permissions (ChatRef.Key in Go): "telegram:5512:". */
export function chatKey(ref: ChatRef): string {
  return `${ref.channel}:${ref.chat_id}:${ref.thread_id ?? ''}`;
}

/** A recipient permission as people read it in "Allowed without asking". */
export function sendSentence(value: string, chats: Chats): string {
  // Connected tools key recipients by their own field (an address, a pattern), not by chat.
  if (!value.includes(':')) return `Send messages to ${value}`;
  const [channel, chatId] = value.split(':');
  const app = appName(channel);
  const space = chats.spaces.find((s) => s.chat.channel === channel && s.chat.chat_id === chatId);
  if (space) return `Send messages to ${space.title} on ${app}`;
  if (chats.identities.some((i) => i.channel === channel && i.dm_chat_id === chatId)) return `Send you messages on ${app}`;
  return `Send messages to a chat on ${app}`;
}

export interface TaskLookups {
  names: Map<string, string>;
  sessions: Map<string, Session>;
  schedules: Map<string, Schedule>;
  /** Pending approvals by run id, for "(1 of 2)". */
  waiting: Map<string, VotedApproval>;
  chats: Chats;
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
    // A finished schedule says where its result went instead of counting steps.
    if (r.status === 'done' && schedule) out = { text: `Posted ${deliverTo(schedule.deliver, l.chats)}`, tone: 'muted' };
    const title = r.schedule_name || schedule?.name || (r.trigger === 'heartbeat' ? 'Check-in' : 'A schedule');
    return { title, lead: ['Schedule'], outcome: out };
  }
  return {
    title: r.chat_title || chat?.title || 'A chat',
    lead: [personName(r.user_id, l.names, r.requested_by_name), where(r.origin, r.trigger)],
    outcome: out,
  };
}

/**
 * The outcome a task page adds to its meta line. Finished and running tasks need none, and a
 * waiting task already shows its waiting step.
 */
export function detailOutcome(r: TaskRun): Outcome | null {
  if (['done', 'running', 'queued', 'waiting_approval', 'parked'].includes(r.status)) return null;
  return outcome({ status: r.status, steps: r.steps, reason: r.reason });
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
