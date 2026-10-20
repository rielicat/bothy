import type { RunStatus } from './api/types.gen';
import { capital, plural } from './format';
import { channelName } from './labels';

export type Tone = 'muted' | 'warning' | 'danger';

export interface Outcome {
  text: string;
  tone: Tone;
}

/** The app's name for a chat origin or channel: "Web", "Slack", "API". */
export function appName(channel: string | undefined): string {
  return channelName[channel ?? ''] ?? (channel ? capital(channel) : 'Web');
}

/** Where a task came from: the chat app, the API, or the web. */
export function where(origin: string | undefined, trigger: string): string {
  if (trigger === 'api') return 'API';
  return appName(origin && origin !== 'schedule' ? origin : 'web');
}

export interface OutcomeInput {
  status: RunStatus;
  steps: number;
  /** The server's plain reason: a clause after "Failed:", a phrase after "Stopped" ("by Maya Chen"). */
  reason?: string;
  refined?: boolean;
  /** Multi-approver progress of the approval the task waits on. */
  votes?: { approved: number; required: number };
}

/** The outcome at the end of line 2: nothing extra when done ("6 steps"), otherwise a word. */
export function outcome(t: OutcomeInput): Outcome {
  switch (t.status) {
    case 'done':
      return { text: plural(t.steps, 'step') + (t.refined ? ' · Refined' : ''), tone: 'muted' };
    case 'waiting_approval':
    case 'parked':
      return { text: `Waiting for approval${t.votes && t.votes.required > 1 ? ` (${t.votes.approved} of ${t.votes.required})` : ''}`, tone: 'warning' };
    case 'failed':
      return { text: t.reason ? `Failed: ${t.reason}` : 'Failed', tone: 'danger' };
    case 'loop_detected':
      return { text: 'Failed: Bothy kept repeating the same step', tone: 'danger' };
    case 'budget_exceeded':
      return { text: 'Hit the spending limit', tone: 'warning' };
    case 'stopped':
      return { text: t.reason ? `Stopped ${t.reason}` : 'Stopped', tone: 'muted' };
    case 'interrupted':
      return { text: 'Interrupted', tone: 'muted' };
    case 'skipped':
      return { text: 'Skipped', tone: 'muted' };
    case 'queued':
      return { text: 'Waiting to start', tone: 'muted' };
    default:
      return { text: 'Working…', tone: 'muted' };
  }
}

export const toneClass: Record<Tone, string> = { muted: '', warning: 'text-warning-text', danger: 'text-danger-text' };

/** "Any result / Needs attention / Failed" (§10.1). */
export function matchesResult(status: RunStatus, filter: string): boolean {
  if (filter === 'attention') return ['waiting_approval', 'parked', 'budget_exceeded'].includes(status);
  if (filter === 'failed') return status === 'failed' || status === 'loop_detected';
  return true;
}

/** "Anywhere / Chats / Schedules / Chat apps / API" (§10.1). */
export function matchesWhere(origin: string | undefined, trigger: string, filter: string): boolean {
  const scheduled = trigger === 'schedule' || trigger === 'heartbeat';
  switch (filter) {
    case 'chats':
      return !scheduled && trigger !== 'api' && (!origin || origin === 'web');
    case 'schedules':
      return scheduled;
    case 'apps':
      return !scheduled && !!origin && ['slack', 'telegram', 'discord'].includes(origin);
    case 'api':
      return trigger === 'api';
    default:
      return true;
  }
}
