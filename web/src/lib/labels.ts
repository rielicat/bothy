import type { IconName } from './icons';
import type { Choice, RunStatus, SideEffect, Verdict } from './api/types.gen';

export const risk: Record<SideEffect, { label: string; cls: string }> = {
  read: { label: 'Read only', cls: 'text-risk-read bg-risk-read/10 border-risk-read/30' },
  local_write: { label: 'Changes files', cls: 'text-risk-local bg-risk-local/10 border-risk-local/30' },
  external_send: { label: 'Sends data out', cls: 'text-risk-send bg-risk-send/10 border-risk-send/30' },
  irreversible: { label: 'Irreversible', cls: 'text-risk-irrev bg-risk-irrev/10 border-risk-irrev/30' },
  financial: { label: 'Financial', cls: 'text-risk-money bg-risk-money/10 border-risk-money/30' },
  admin: { label: 'Admin', cls: 'text-risk-admin bg-risk-admin/10 border-risk-admin/30' },
};

export const riskBar: Record<SideEffect, string> = {
  read: 'bg-risk-read',
  local_write: 'bg-risk-local',
  external_send: 'bg-risk-send',
  irreversible: 'bg-risk-irrev',
  financial: 'bg-risk-money',
  admin: 'bg-risk-admin',
};

export const sideEffects = Object.keys(risk) as SideEffect[];

export const choiceLabel: Record<Choice, string> = {
  allow_once: 'Allow once',
  allow_session: 'Allow for this session',
  allow_always: 'Always allow',
  deny: 'Deny',
};

export const choiceDone: Record<Choice, string> = {
  allow_once: 'Allowed once',
  allow_session: 'Allowed for this session',
  allow_always: 'Always allowed',
  deny: 'Denied',
};

export const choiceKey: Record<Choice, string> = { allow_once: 'a', allow_session: 's', allow_always: 'l', deny: 'd' };

export const verdictChip: Record<Verdict | 'granted' | 'approved' | 'denied', string> = {
  auto: 'chip',
  ask: 'chip chip-warn',
  deny: 'chip chip-bad',
  granted: 'chip chip-info',
  approved: 'chip chip-ok',
  denied: 'chip chip-bad',
};

export const runStatus: Record<RunStatus, { label: string; cls: string }> = {
  queued: { label: 'Queued', cls: 'chip' },
  running: { label: 'Running', cls: 'chip chip-info' },
  waiting_approval: { label: 'Waiting for approval', cls: 'chip chip-warn' },
  parked: { label: 'Parked', cls: 'chip chip-warn' },
  done: { label: 'Done', cls: 'chip chip-ok' },
  stopped: { label: 'Stopped', cls: 'chip' },
  failed: { label: 'Failed', cls: 'chip chip-bad' },
  budget_exceeded: { label: 'Budget hit', cls: 'chip chip-warn' },
  loop_detected: { label: 'Loop stopped', cls: 'chip chip-warn' },
  interrupted: { label: 'Interrupted', cls: 'chip chip-warn' },
  skipped: { label: 'Skipped', cls: 'chip' },
};

export function statusOf(s: string | undefined) {
  return runStatus[s as RunStatus] ?? { label: s || '—', cls: 'chip' };
}

export const originIcon: Record<string, IconName> = {
  web: 'globe',
  telegram: 'plane',
  discord: 'game',
  slack: 'hash',
  api: 'code',
  schedule: 'clock',
};

export const channelName: Record<string, string> = {
  web: 'Web',
  telegram: 'Telegram',
  discord: 'Discord',
  slack: 'Slack',
  api: 'API',
  schedule: 'Schedule',
  timeout: 'timeout',
  stop: 'stop',
};
