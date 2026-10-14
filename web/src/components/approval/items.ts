// Inbox items that are not action approvals (§8.8, S12): tool reviews,
// changed skills, new senders and new group chats. GET /api/inbox lists
// them; each is decided through the endpoint its kind already has.
import { api } from '../../lib/api/client';
import type { Inbox, InboxItem as ServerItem, MCPPin } from '../../lib/api/types.gen';
import { appName, humanize } from '../../lib/cardcopy';
import { lineDiff } from '../../lib/diff';
import { plural } from '../../lib/format';
import type { InboxItem } from './types';

const enc = encodeURIComponent;
const pretty = (p: MCPPin) => JSON.stringify(p.definition ?? {}, null, 2);

function fromServer(i: ServerItem): InboxItem | null {
  const base = { id: i.id, created_at: i.created_at };
  switch (i.type) {
    case 'mcp_review': {
      if (!i.mcp) return null;
      const pins = i.mcp.tools.filter((p): p is MCPPin => !!p);
      const tools = pins
        .filter((p) => p.status === 'pending')
        .map((p) => {
          const before = pins.find((x) => x.tool === p.tool && x !== p);
          const d = (p.definition ?? {}) as { title?: string; description?: string };
          return { name: p.tool, title: d.title, description: d.description ?? '', side_effect: p.side_effect, diff: lineDiff(before ? pretty(before) : '', pretty(p)), hash: p.hash, private: p.private, field_map: p.field_map };
        });
      return tools.length ? { ...base, type: 'mcp_review', server: i.mcp.server, tools } : null;
    }
    case 'skill_review': {
      if (!i.skill) return null;
      const c = i.skill_changes;
      const files = (c?.files ?? []).map((f) => ({ path: f.path, change: f.change, diff: f.opaque ? undefined : lineDiff(f.before ?? '', f.after ?? '') }));
      return { ...base, type: 'skill_review', skill: i.skill.name, hash: i.skill.hash, files, noBaseline: !!c?.no_baseline, truncated: !!c?.truncated };
    }
    case 'unknown_sender': {
      const s = i.sender;
      if (!s) return null;
      return { ...base, type: 'unknown_sender', app: s.channel, external_id: s.external_id, display_name: s.display_name || s.username || s.external_id, handle: s.username ? `@${s.username}` : s.external_id, text: s.first_text };
    }
    case 'group_chat': {
      const g = i.group;
      return g ? { ...base, type: 'group_chat', app: g.space.chat.channel, chat: g.space.title, space: g.space.principal_id, added_by: g.added_by } : null;
    }
  }
  // Approvals and note suggestions arrive with GET /api/approvals.
  return null;
}

/** Every waiting item; none when the inbox can't be read, so they stay on their settings pages. */
export async function loadItems(): Promise<InboxItem[]> {
  try {
    const r = await api.get<Inbox>('/api/inbox');
    return (r.items ?? []).map(fromServer).filter((i): i is InboxItem => !!i);
  } catch {
    return [];
  }
}

export function decideItem(item: InboxItem, choice: string, extra: { side_effects?: Record<string, string>; user_id?: string } = {}): Promise<unknown> {
  switch (item.type) {
    case 'mcp_review':
      if (choice === 'turn_off') return api.patch('/api/config', { set: { [`mcp.servers.${item.server}.enabled`]: false } });
      return api.post(`/api/mcp/${enc(item.server)}/approve`, {
        tools: Object.fromEntries(item.tools.map((t) => [t.name, { side_effect: extra.side_effects?.[t.name] ?? t.side_effect, private: t.private, field_map: t.field_map, hash: t.hash }])),
      });
    case 'skill_review':
      return api.patch(`/api/skills/${enc(item.skill)}`, choice === 'approve' ? { enabled: true, hash: item.hash } : { enabled: false });
    case 'unknown_sender':
      return api.patch(`/api/identities/${enc(item.app)}/${enc(item.external_id)}`, choice === 'link' ? { status: 'linked', user_id: extra.user_id } : { status: choice === 'block' ? 'blocked' : 'guest' });
    case 'group_chat':
      // Any change marks the group as seen; "Ignore" leaves it off.
      return api.patch(`/api/spaces/${enc(item.space)}`, { enabled: choice === 'turn_on' });
  }
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
  }
}

/** The collapsed row's source: "2 tools changed", "@jordan_k". */
export function itemSource(i: InboxItem): string {
  switch (i.type) {
    case 'mcp_review':
      return `${plural(i.tools.length, 'tool')} changed`;
    case 'skill_review':
      return i.files.length ? `${plural(i.files.length, 'file')} changed` : 'Changed files';
    case 'unknown_sender':
      return i.handle;
    case 'group_chat':
      return i.added_by ? `Added by ${i.added_by}` : appName(i.app);
  }
}
