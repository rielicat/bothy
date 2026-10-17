// Inbox items that are not action approvals (§8.8, S12): tool reviews,
// changed skills, new senders and new group chats. GET /api/inbox lists
// them; each is decided through the endpoint its kind already has.
import { api } from '../../lib/api/client';
import type { Inbox, InboxItem as ServerItem, MCPPin } from '../../lib/api/types.gen';
import { appName } from '../../lib/cardcopy';
import { lineDiff } from '../../lib/diff';
import { plural, serviceName, skillName } from '../../lib/format';
import type { GroupChatItem, InboxItem } from './types';

const enc = encodeURIComponent;
const pretty = (p: MCPPin) => JSON.stringify(p.definition ?? {}, null, 2);

/** What changed between an approved and a pending definition: the description alone reads as prose, anything else as the definition's diff. */
function definitionChange(before: MCPPin | undefined, after: MCPPin): { diff: string; prose: boolean } {
  const prev = (before?.definition ?? {}) as Record<string, unknown>;
  const next = (after.definition ?? {}) as Record<string, unknown>;
  const changed = [...new Set([...Object.keys(prev), ...Object.keys(next)])].filter((k) => JSON.stringify(prev[k]) !== JSON.stringify(next[k]));
  if (before && changed.length === 1 && changed[0] === 'description') return { diff: lineDiff(String(prev.description ?? ''), String(next.description ?? '')), prose: true };
  return { diff: lineDiff(before ? pretty(before) : '', pretty(after)), prose: false };
}

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
          return { name: p.tool, title: d.title, description: d.description ?? '', side_effect: p.side_effect, ...definitionChange(before, p), hash: p.hash, private: p.private, field_map: p.field_map };
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
      // Many chat accounts have no username; their numeric id stays in the card's Details.
      return { ...base, type: 'unknown_sender', app: s.channel, external_id: s.external_id, display_name: s.display_name || s.username || 'Someone', handle: s.username ? `@${s.username}` : '', text: s.first_text };
    }
    case 'group_chat': {
      const g = i.group;
      if (!g) return null;
      const s = g.space;
      return { ...base, type: 'group_chat', app: s.chat.channel, chat: s.title, space: s.principal_id, added_by: g.added_by, team: s.team_name, members: s.members, external: s.external, mention_only: s.mention_only };
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

export function decideItem(item: InboxItem, choice: string, extra: { side_effects?: Record<string, string>; tool_profile?: string } = {}): Promise<unknown> {
  switch (item.type) {
    case 'mcp_review':
      if (choice === 'turn_off') return api.patch('/api/config', { set: { [`mcp.servers.${item.server}.enabled`]: false } });
      return api.post(`/api/mcp/${enc(item.server)}/approve`, {
        tools: Object.fromEntries(item.tools.map((t) => [t.name, { side_effect: extra.side_effects?.[t.name] ?? t.side_effect, private: t.private, field_map: t.field_map, hash: t.hash }])),
      });
    case 'skill_review':
      return api.patch(`/api/skills/${enc(item.skill)}`, choice === 'approve' ? { enabled: true, hash: item.hash } : { enabled: false });
    case 'unknown_sender':
      return api.patch(`/api/identities/${enc(item.app)}/${enc(item.external_id)}`, { status: choice === 'block' ? 'blocked' : 'teammate' });
    case 'group_chat':
      // Any change marks the group as seen; "Ignore" leaves it off.
      return api.patch(`/api/spaces/${enc(item.space)}`, choice === 'turn_on' ? { enabled: true, tool_profile: extra.tool_profile ?? 'restricted' } : { enabled: false });
  }
}

export function itemQuestion(i: InboxItem): string {
  switch (i.type) {
    case 'mcp_review':
      return `Review changes to ${serviceName(i.server)} actions`;
    case 'skill_review':
      return `Review changes to the “${skillName(i.skill)}” skill`;
    case 'unknown_sender':
      return `Someone new messaged Bothy on ${appName(i.app)}`;
    case 'group_chat':
      return `Bothy was added to ${i.chat} in ${appName(i.app)}`;
  }
}

/** The lines under a new group chat: who added it, who is there and who turning it on lets in. */
export function groupLines(i: GroupChatItem): string[] {
  const lines: string[] = [];
  if (i.added_by) lines.push(`Added by ${i.added_by}.`);
  // "This server" would read as Bothy's own host (§14.1).
  const server = i.team || 'the Discord server';
  if (i.app === 'discord') {
    if (i.members > 0) lines.push(`${i.team || 'The Discord server'} has ${plural(i.members, 'member')}.`);
    lines.push(`Turning this on lets ${i.members > 0 ? `all ${plural(i.members, 'member')} of` : 'everyone in'} ${server} message Bothy, here and in direct messages.`);
  } else {
    if (i.members > 0) lines.push(`${plural(i.members, 'person is', 'people are')} in this chat.`);
    if (i.app === 'slack' && i.external) lines.push("People from other companies are in this channel. They'll see Bothy's replies, though Bothy won't answer them.");
    if (i.app === 'telegram') lines.push("Anyone in this group can use Bothy once it's on, not only people you let in.");
  }
  return lines;
}

/** The collapsed row's source: "2 actions changed", "@jordan_k". */
export function itemSource(i: InboxItem): string {
  switch (i.type) {
    case 'mcp_review':
      return `${plural(i.tools.length, 'action')} changed`;
    case 'skill_review':
      return i.files.length ? `${plural(i.files.length, 'file')} changed` : 'Changed files';
    case 'unknown_sender':
      return i.handle || appName(i.app);
    case 'group_chat':
      return i.added_by ? `Added by ${i.added_by}` : appName(i.app);
  }
}
