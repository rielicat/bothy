import type { Approval } from '../../lib/api/types.gen';
import type { IconName } from '../../lib/icons';
import type { InboxItem } from './types';

// The type icon at the start of a card and an inbox row (docs/design/ui.md §3.7, §8.1).
const byTool: Record<string, IconName> = {
  repo_open_pr: 'pull-request',
  github_comment: 'pull-request',
  repo_checkout: 'code',
  release_deploy: 'deploy',
  shell: 'terminal',
  python: 'terminal',
  message: 'mail',
  web_fetch: 'globe',
  net_allow: 'globe',
  skill_install: 'file',
  notes: 'file',
  memory: 'file',
};

const byItem: Record<InboxItem['type'], IconName> = {
  mcp_review: 'plug',
  skill_review: 'file',
  unknown_sender: 'users',
  group_chat: 'users',
};

export function approvalIcon(a: Approval): IconName {
  const t = a.action?.tool ?? '';
  if (byTool[t]) return byTool[t];
  if (t.startsWith('fs_')) return 'file';
  if (t.startsWith('schedule_')) return 'calendar';
  if (t.startsWith('mcp__') || a.action?.kind === 'mcp') return 'plug';
  return 'approvals';
}

export const itemIcon = (i: InboxItem): IconName => byItem[i.type] ?? 'approvals';
