import { describe, expect, it } from 'vitest';
import type { Action, Approval } from '../../lib/api/types.gen';
import { approvalIcon, itemIcon } from './icon';
import type { InboxItem } from './types';

const ap = (tool: string, kind = 'generic'): Approval => ({ action: { tool, kind, side_effect: 'local_write', payload: {}, fingerprint: 'f' } as Action }) as Approval;

// docs/design/ui.md §3.7: the type icon on a card and an inbox row.
describe('approval type icons', () => {
  it.each([
    ['repo_open_pr', 'pull-request'],
    ['github_comment', 'pull-request'],
    ['release_deploy', 'deploy'],
    ['shell', 'terminal'],
    ['python', 'terminal'],
    ['message', 'mail'],
    ['web_fetch', 'globe'],
    ['net_allow', 'globe'],
    ['fs_delete', 'file'],
    ['fs_write', 'file'],
    ['notes', 'file'],
    ['memory', 'file'],
    ['skill_install', 'file'],
    ['schedule_create', 'calendar'],
    ['mcp__linear__create_issue', 'plug'],
    ['something_new', 'approvals'],
  ])('%s → %s', (tool, icon) => expect(approvalIcon(ap(tool))).toBe(icon));

  it('reads an MCP action by its kind too', () => expect(approvalIcon(ap('create_issue', 'mcp'))).toBe('plug'));

  it('gives each review item its own', () => {
    const item = (type: InboxItem['type']) => ({ type }) as InboxItem;
    expect(['mcp_review', 'skill_review', 'unknown_sender', 'group_chat'].map((t) => itemIcon(item(t as InboxItem['type'])))).toEqual(['plug', 'file', 'users', 'users']);
  });
});
