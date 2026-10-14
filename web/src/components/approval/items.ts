// Inbox items that are not action approvals (§8.8, S12): tool reviews,
// changed skills, new senders, new group chats and note suggestions.
import { api } from '../../lib/api/client';
import { appName, humanize } from '../../lib/cardcopy';
import { plural } from '../../lib/format';
import type { InboxItem } from './types';

/** Every waiting item; none when the server doesn't list them yet, so they stay on their settings pages. */
export async function loadItems(): Promise<InboxItem[]> {
  try {
    const r = await api.get<{ items?: InboxItem[] }>('/api/approvals/items');
    return (r.items ?? []).filter((i) => !!i?.id);
  } catch {
    return [];
  }
}

export function decideItem(id: string, body: { choice: string; side_effects?: Record<string, string>; user_id?: string }): Promise<unknown> {
  return api.post(`/api/approvals/items/${encodeURIComponent(id)}`, body);
}

export function itemQuestion(i: InboxItem): string {
  switch (i.type) {
    case 'mcp_review':
      return `Review changes to ${humanize(i.server)} tools`;
    case 'skill_review':
      return `Review changes to the “${i.skill}” skill`;
    case 'unknown_sender':
      return `Someone new messaged Bothy on ${appName(i.app)}`;
    case 'group_chat':
      return `Bothy was added to ${i.chat} in ${appName(i.app)}`;
    case 'note_suggestion':
      return `Update the notes for ${i.repo}?`;
  }
}

/** The collapsed row's source: "2 tools changed", "@jordan_k". */
export function itemSource(i: InboxItem): string {
  switch (i.type) {
    case 'mcp_review':
      return `${plural(i.tools.length, 'tool')} changed`;
    case 'skill_review':
      return 'Changed files';
    case 'unknown_sender':
      return i.handle;
    case 'group_chat':
      return i.added_by ? `Added by ${i.added_by}` : appName(i.app);
    case 'note_suggestion':
      return 'Suggested by Bothy';
  }
}
