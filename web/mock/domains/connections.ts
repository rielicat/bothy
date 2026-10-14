// Connections (C8): chat apps, pairing, linked accounts, group chats and MCP
// servers. GitHub joins here when S19 lands (docs/design/ui.md §17).
import type { ChannelIdentity, ChannelStatus, MCPPin, MCPStatus, Space } from '../../src/lib/api/types.gen.ts';
import { ago, body, clone, core, emit, json, on, onReset, owner, sleep, ZERO } from './core.ts';

export const channels: ChannelStatus[] = [
  { name: 'telegram', enabled: true, state: 'connected', identity: '@sams_bothy_bot', since: ago(60 * 24), deep_links: true },
  { name: 'discord', enabled: false, state: 'disabled', since: ago(60 * 24), deep_links: false },
  { name: 'slack', enabled: false, state: 'disabled', since: ago(60 * 24), deep_links: false },
];

export const identities: ChannelIdentity[] = [
  { channel: 'telegram', external_id: '5512', username: 'sam_k', display_name: 'Sam K', status: 'linked', user_id: owner.id, dm_chat_id: '5512', first_seen_at: ago(60 * 24 * 20), last_seen_at: ago(95) },
  { channel: 'telegram', external_id: '8841', username: 'grandma_b', display_name: 'Grandma B', status: 'pending', first_seen_at: ago(300), last_seen_at: ago(300) },
];

export const spaces: Space[] = [
  {
    principal_id: 'spc_01jfamily0000000000000000',
    kind: 'group',
    chat: { channel: 'telegram', chat_id: '-100223', kind: 'group' },
    title: 'Family group',
    owner_user_id: owner.id,
    enabled: true,
    mention_only: true,
    created_at: ago(60 * 24 * 9),
    updated_at: ago(60 * 24 * 9),
  },
];

export const mcp: MCPStatus[] = [
  { name: 'docs', transport: 'http', state: 'pins_pending', protocol: '2026-07-28', tools: 4, pending: 1, since: ago(60) },
  { name: 'notes', transport: 'stdio', state: 'disabled', protocol: '', tools: 0, pending: 0, since: ago(60 * 24) },
];

export const mcpPins: MCPPin[] = [
  {
    server: 'docs',
    tool: 'search_docs',
    hash: 'b3c1',
    definition: { name: 'search_docs', description: 'Search the team handbook and return matching sections.', inputSchema: { type: 'object', properties: { query: { type: 'string' }, limit: { type: 'integer' } } } },
    status: 'pending',
    side_effect: 'read',
    private: false,
    first_seen_at: ago(60),
    approved_at: ZERO,
  },
  {
    server: 'docs',
    tool: 'search_docs',
    hash: 'a9f0',
    definition: { name: 'search_docs', description: 'Search the team handbook.', inputSchema: { type: 'object', properties: { query: { type: 'string' } } } },
    status: 'superseded',
    side_effect: 'read',
    private: false,
    first_seen_at: ago(60 * 24 * 5),
    approved_at: ago(60 * 24 * 5),
  },
];

export const state = { identities: [] as ChannelIdentity[], spaces: [] as Space[] };
onReset(() => Object.assign(state, { identities: clone(identities), spaces: clone(spaces) }));

on('GET', '/api/channels', (_q, res) => json(res, 200, channels));
on('POST', '/api/channels/{name}/test', async (_q, res, m) => {
  await sleep(500);
  json(res, 200, { identity: m[1] === 'telegram' ? '@sams_bothy_bot' : m[1] === 'discord' ? 'Bothy#4021' : 'bothy (T0123)' });
});
on('GET', '/api/channels/slack/manifest', (_q, res) => {
  res.setHeader('Content-Type', 'text/yaml');
  res.end('display_information:\n  name: "Bothy"\nsettings:\n  socket_mode_enabled: true\n');
});
on('GET', '/api/channels/discord/invite', (_q, res) => json(res, 200, { url: 'https://discord.com/oauth2/authorize?client_id=123&scope=bot%20applications.commands&permissions=274877975552' }));

on('POST', '/api/pairing', (_q, res) => {
  setTimeout(() => emit('pairing.linked', { channel: 'telegram', external_id: '5512', display: 'Sam K' }), 6000);
  json(res, 200, { code: '7KQ2MX4D', expires_at: new Date(Date.now() + 15 * 60_000).toISOString(), deep_links: { telegram: 'https://t.me/sams_bothy_bot?start=7KQ2MX4D' } });
});
on('POST', '/api/pairing/approve', (_q, res) => json(res, 200, state.identities[1]));
on('GET', '/api/identities', (_q, res) => json(res, 200, state.identities));
on('PATCH', '/api/identities/{channel}/{external}', async (req, res, m) => {
  const i = state.identities.find((x) => x.channel === m[1] && x.external_id === m[2]);
  if (i) i.status = JSON.parse(await body(req)).status;
  json(res, 200, i);
});
on('DELETE', '/api/identities/{channel}/{external}', (_q, res, m) => {
  state.identities = state.identities.filter((x) => !(x.channel === m[1] && x.external_id === m[2]));
  json(res, 204);
});
on('GET', '/api/spaces', (_q, res) => json(res, 200, state.spaces));
on('PATCH', '/api/spaces/{id}', async (req, res, m) => {
  const s = state.spaces.find((x) => x.principal_id === m[1]);
  if (s) Object.assign(s, JSON.parse(await body(req)));
  json(res, 200, s);
});

on('GET', '/api/mcp', (_q, res) => json(res, 200, mcp));
on('GET', '/api/mcp/{server}/pins', (_q, res, m) => json(res, 200, mcpPins.filter((p) => p.server === m[1])));
on('POST', '/api/mcp/{server}/approve', (_q, res) => {
  core.banners = core.banners.filter((b) => b.code !== 'mcp_pins');
  emit('banner', { level: 'warn', code: 'mcp_pins', text: '', clear: true });
  json(res, 204);
});
on('POST', '/api/mcp/{server}/install', (_q, res) => json(res, 202));
on('POST', '/api/mcp/{server}/reload', (_q, res) => json(res, 202));
