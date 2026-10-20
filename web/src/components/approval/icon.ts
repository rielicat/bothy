import type { Approval } from '../../lib/api/types.gen';
import type { IconName } from '../../lib/icons';
import type { InboxItem } from './types';

// The type icon at the start of a card and an inbox row (docs/design/ui.md §3.7, §8.1).
const byTool: Record<string, IconName> = {
  repo_open_pr: 'pull-request',
  github_comment: 'pull-request',
  release_deploy: 'deploy',
  shell: 'terminal',
  python: 'terminal',
  message: 'mail',
  web_fetch: 'globe',
  net_allow: 'globe',
};

export function approvalIcon(a: Approval): IconName {
  const t = a.action?.tool ?? '';
  return (
    byTool[t] ??
    (/^(fs_|notes$|memory$|skill_)/.test(t) ? 'file' : t.startsWith('schedule_') ? 'calendar' : t.startsWith('mcp__') || a.action?.kind === 'mcp' ? 'plug' : 'approvals')
  );
}

export const itemIcon = (i: InboxItem): IconName => (i.type === 'mcp_review' ? 'plug' : i.type === 'skill_review' ? 'file' : 'users');
