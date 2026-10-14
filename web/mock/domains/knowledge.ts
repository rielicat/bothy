// Knowledge (C5): memory files and skills. Repositories, project notes and
// knowledge search join here when S18 lands (docs/design/ui.md §17).
import type { MemoryEvent, SkillMeta } from '../../src/lib/api/types.gen.ts';
import { ago, body, clone, core, fail, ids, json, newId, nowISO, on, onReset, owner } from './core.ts';

export const memory: Record<string, string> = {
  MEMORY: '- Partner: Jo. Anniversary 14 March.\n- Prefers trains over flights in the UK.\n- Vegetarian on weekdays.\n- Works on the Bothy docs on Fridays.',
  USER: '- Call me Sam.\n- I run a small design studio.\n- Keep answers short unless I ask for detail.',
  HEARTBEAT: '- [ ] Remind me about the dentist on Nov 2\n- [ ] Tell me if the electricity bill is over £120',
};

export const memoryHistory: MemoryEvent[] = [
  { id: 41, principal_id: owner.id, file: 'MEMORY', op: 'add', before: '- Partner: Jo. Anniversary 14 March.\n- Prefers trains over flights in the UK.', after: memory.MEMORY, run_id: ids.tripRun, actor: `agent:${ids.tripRun}`, provenance: 'user', at: ago(40) },
  { id: 40, principal_id: owner.id, file: 'MEMORY', op: 'edit', before: '- Partner: Jo.', after: '- Partner: Jo. Anniversary 14 March.\n- Prefers trains over flights in the UK.', actor: `user:${owner.id}`, provenance: 'user', at: ago(60 * 30) },
];

export const skills: SkillMeta[] = [
  { name: 'meal-planner', description: 'Plans a week of meals from what is in the fridge.', path: 'skills/meal-planner', hash: 'h1', approved_hash: 'h1', status: 'enabled', tokens: 64, installed_at: ago(60 * 24 * 6), updated_at: ago(60 * 24 * 6) },
  { name: 'invoice-helper', description: 'Drafts invoices from a short description of the work.', path: 'skills/invoice-helper', hash: 'h2', approved_hash: '', status: 'pending', tokens: 88, installed_at: ago(60 * 2), updated_at: ago(60 * 2) },
  { name: 'trip-notes', description: 'Keeps a travel journal in the workspace.', path: 'skills/trip-notes', hash: 'h3b', approved_hash: 'h3', status: 'changed', tokens: 51, installed_at: ago(60 * 24 * 30), updated_at: ago(60 * 5) },
];

export const state = { memory: {} as Record<string, string>, etags: {} as Record<string, string>, memoryHistory: [] as MemoryEvent[], skills: [] as SkillMeta[] };
onReset(() => Object.assign(state, { memory: clone(memory), etags: { MEMORY: 'e1', USER: 'e1', HEARTBEAT: 'e1' }, memoryHistory: clone(memoryHistory), skills: clone(skills) }));

const memFile = (f: string) => f.toUpperCase();
on('GET', '/api/memory/export', (_q, res) => {
  res.setHeader('Content-Disposition', 'attachment; filename="memory.tar"');
  res.end('mock tar');
});
on('GET', '/api/memory/{file}', (_q, res, m) => {
  const f = memFile(m[1]);
  json(res, 200, { file: f, content: state.memory[f] ?? '', etag: state.etags[f], cap_bytes: f === 'MEMORY' ? 4096 : 2048 });
});
on('PUT', '/api/memory/{file}', async (req, res, m) => {
  const f = memFile(m[1]);
  if (req.headers['if-match'] !== state.etags[f]) return fail(res, 409, 'conflict', 'The file changed since you loaded it.');
  const { content } = JSON.parse(await body(req));
  state.memoryHistory.unshift({ id: Math.floor(Math.random() * 1e6), principal_id: core.me.id, file: f, op: 'edit', before: state.memory[f] ?? '', after: content, actor: `user:${core.me.id}`, provenance: 'user', at: nowISO() });
  state.memory[f] = content;
  state.etags[f] = newId('e').slice(0, 8);
  json(res, 200, { file: f, content, etag: state.etags[f], cap_bytes: f === 'MEMORY' ? 4096 : 2048 });
});
on('GET', '/api/memory/{file}/history', (_q, res, m) => json(res, 200, { items: state.memoryHistory.filter((e) => e.file === memFile(m[1])) }));

on('GET', '/api/skills', (_q, res) => json(res, 200, state.skills));
on('GET', '/api/skills/{name}', (_q, res, m) => {
  const s = state.skills.find((x) => x.name === m[1]);
  json(res, 200, {
    meta: s,
    body: `---\nname: ${s?.name}\ndescription: ${s?.description}\n---\n\n# ${s?.name}\n\nUse this skill when the user asks for help with it.\n\n1. Read the files in the skill folder.\n2. Follow the steps.\n\n[Reference](https://example.com/docs) and <script>alert(1)</script> stay inert.`,
    files: ['SKILL.md', 'templates/week.md'],
  });
});
on('PATCH', '/api/skills/{name}', async (req, res, m) => {
  const s = state.skills.find((x) => x.name === m[1]);
  if (s) s.status = JSON.parse(await body(req)).enabled ? 'enabled' : 'disabled';
  json(res, 200, s);
});
on('DELETE', '/api/skills/{name}', (_q, res, m) => {
  state.skills = state.skills.filter((x) => x.name !== m[1]);
  json(res, 204);
});
on('POST', '/api/skills/rescan', (_q, res) => json(res, 200, state.skills));
on('POST', '/api/skills', (_q, res) => {
  const s = { ...state.skills[1], name: `uploaded-${state.skills.length}`, status: 'pending', installed_at: nowISO() };
  state.skills.push(s);
  json(res, 200, s);
});
