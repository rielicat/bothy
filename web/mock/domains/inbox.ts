// The approvals inbox (S12): GET /api/inbox, worked out from the other
// domains the way internal/httpapi/h_inbox.go does, so deciding an item
// through its own endpoint (a pin approval, a skill or identity change, a
// group chat change) takes it off the list.
import type { InboxItem, MCPPin, SkillChanges, SkillMeta } from '../../src/lib/api/types.gen.ts';
import { list as approvals, forYou } from './approvals.ts';
import { state as connections } from './connections.ts';
import { fail, json, member, on, owner, scenario, viewer, type Req } from './core.ts';
import { state as knowledge } from './knowledge.ts';
import { state as settings } from './settings.ts';

const noItems = scenario('approvals-no-items', 'GET /api/inbox fails, so the inbox shows the approvals it lists itself.', 'approvals');
const none = scenario('approvals-empty', 'Nothing waits for approval and nothing was decided.', 'approvals');

const rank: Record<string, number> = { member: 1, admin: 2, owner: 3 };
const names: Record<string, string> = { [owner.id]: owner.display_name, [member.id]: member.display_name };

function serverOff(name: string): boolean {
  return settings.effective.entries?.some((e) => e.path === `mcp.servers.${name}.enabled` && e.value === false) ?? false;
}

// What changed since approval, as h_uistate.go skillChanges sends it.
function skillChanges(s: SkillMeta): SkillChanges {
  if (s.status === 'pending') return { files: [{ path: 'SKILL.md', change: 'added', after: `---\nname: ${s.name}\ndescription: ${s.description}\n---\n\nUploaded from a zip file.\n` }] };
  return {
    files: [
      {
        path: 'SKILL.md',
        change: 'changed',
        before: '1. Build a timeline from the chat, the deploys and the alerts.\n2. Say what customers saw and for how long.\n',
        after: '1. Build a timeline from the chat, the deploys and the alerts.\n2. Say what customers saw and for how long.\n3. Post the review to #incidents when it is done.\n',
      },
      { path: 'scripts/post.sh', change: 'added', after: '#!/bin/sh\ncurl -s -X POST "$SLACK_WEBHOOK" -d @review.json\n' },
    ],
  };
}

export function inbox(req: Req): InboxItem[] {
  const me = viewer(req);
  const admin = (rank[me.role] ?? 0) >= rank.admin;
  const out: InboxItem[] = approvals(req)
    .filter((a) => a.status === 'pending' || a.status === 'parked')
    .map((a) => ({ id: a.id, type: a.card.kind === 'review' || a.action?.tool === 'notes' ? 'note_suggestion' : 'approval', question: a.card.question ?? a.card.title, for_you: forYou(a, req), created_at: a.created_at, approval: a }));
  if (admin) {
    const pending = connections.pins.filter((p) => p.status === 'pending' && !serverOff(p.server));
    for (const server of [...new Set(pending.map((p) => p.server))]) {
      const mine = pending.filter((p) => p.server === server);
      // Each changed tool's previous definition rides along, for "Show what changed".
      const before = mine.map((p) => connections.pins.find((x) => x.server === server && x.tool === p.tool && x !== p && x.status !== 'pending')).filter((p): p is MCPPin => !!p);
      out.push({ id: `mcp:${server}`, type: 'mcp_review', question: '', summary: `${mine.length} ${mine.length === 1 ? 'tool' : 'tools'} changed`, for_you: true, created_at: mine[0].first_seen_at, mcp: { server, tools: [...mine, ...before] } });
    }
    for (const s of knowledge.skills.filter((x) => x.status === 'changed' || x.status === 'pending'))
      out.push({ id: `skill:${s.name}`, type: 'skill_review', question: '', for_you: true, created_at: s.updated_at, skill: s, skill_changes: skillChanges(s) });
    for (const i of connections.identities.filter((x) => x.status === 'pending')) out.push({ id: `sender:${i.channel}:${i.external_id}`, type: 'unknown_sender', question: '', for_you: true, created_at: i.first_seen_at, sender: i });
  }
  for (const sp of connections.spaces.filter((x) => x.kind === 'group' && !x.enabled && x.updated_at === x.created_at && (admin || x.owner_user_id === me.id))) {
    out.push({ id: `group:${sp.principal_id}`, type: 'group_chat', question: '', for_you: true, created_at: sp.created_at, group: { space: sp, added_by: names[sp.owner_user_id] ?? 'Someone' } });
  }
  return out;
}

on('GET', '/api/inbox', (req, res) => {
  if (noItems(req)) return fail(res, 404, 'not_found', 'mock: no route for GET /api/inbox');
  json(res, 200, { items: none(req) ? [] : inbox(req) });
});
