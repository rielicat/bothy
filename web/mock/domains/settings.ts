// Settings (C7): people, API tokens, the personality, models and providers,
// and the effective config every settings page reads and writes.
import type { Entry, ModelInfo, ProviderHealth, TokenRecord, User } from '../../src/lib/api/types.gen.ts';
import effectiveFixture from '../fixtures/effective.json' with { type: 'json' };
import schema from '../fixtures/schema.json' with { type: 'json' };
import { ago, ahead, body, clone, core, emit, fail, json, member, newId, nowISO, on, onReset, owner, scenario, viewer, ZERO, type Req } from './core.ts';

export const tokens: TokenRecord[] = [
  { id: 'tok_01jwebui000000000000000000', user_id: owner.id, name: 'Open WebUI', scopes: ['chat'], on_approval: 'deny', rate_per_min: 30, created_at: ago(60 * 24 * 8), last_used_at: ago(60 * 3), expires_at: ahead(60 * 24 * 82), revoked_at: ZERO },
  { id: 'tok_01jrelease00000000000000000', user_id: owner.id, name: 'Release notes script', scopes: ['chat', 'read'], on_approval: 'deny', rate_per_min: 30, created_at: ago(60 * 24 * 20), last_used_at: ZERO, expires_at: ahead(60 * 24 * 70), revoked_at: ZERO },
];

const model = (provider: string, id: string, display: string, context: number, inp: number, out: number): ModelInfo => ({
  ref: { provider, model: id },
  display,
  context,
  max_output: 8192,
  tools: true,
  vision: provider !== 'ollama',
  thinking: false,
  cache: provider === 'anthropic' ? 1 : 0,
  local: provider === 'ollama',
  price: { in_per_m: inp, out_per_m: out, cache_read_per_m: inp / 10, cache_write_per_m: inp * 1.25, known: true },
  source: 'catalog',
});

export const models: ModelInfo[] = [
  model('anthropic', 'claude-sonnet-4-6', 'Claude Sonnet 4.6', 200000, 3_000_000, 15_000_000),
  model('anthropic', 'claude-haiku-4-5', 'Claude Haiku 4.5', 200000, 1_000_000, 5_000_000),
  model('ollama', 'qwen3:14b', 'Qwen3 14B (local)', 16384, 0, 0),
];

// Bedrock is a server need (S3): it shows only with ?mock=provider-bedrock.
const bedrockModels: ModelInfo[] = [
  model('bedrock', 'us.anthropic.claude-sonnet-4-5-20250929-v1:0', 'Claude Sonnet 4.5', 200000, 3_000_000, 15_000_000),
  model('bedrock', 'us.anthropic.claude-haiku-4-5-20251001-v1:0', 'Claude Haiku 4.5', 200000, 1_000_000, 5_000_000),
  model('bedrock', 'us.amazon.nova-pro-v1:0', 'Amazon Nova Pro', 300000, 800_000, 3_200_000),
];

export const providers: ProviderHealth[] = [
  { id: 'anthropic', kind: 'anthropic', state: 'ok', since: ago(60 * 24) },
  { id: 'ollama', kind: 'ollama', state: 'down', last_error: 'Post "http://host.docker.internal:11434/api/chat": dial tcp 172.17.0.1:11434: connect: connection refused', since: ago(60 * 3) },
];

export const persona = `# Bothy

You are Bothy, the AI agent for the Acme team. Be warm, brief and practical.
Prefer plain language and link to the code or the document you used.
Ask before doing anything that leaves the company or can't be undone.`;

const changedPersona = `${persona}
When someone asks about a release, check the release checklist in acme/web first.`;

// Settings the server doesn't have yet (Bothy's name and tone, Refine); C7 shows their rows only when the config lists them.
const pending: Entry[] = [
  { path: 'agent.name', value: 'Bothy', source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.tone', value: 'warm', source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'refine.default', value: false, source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'refine.preset', value: 'standard', source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
];

const bedrockEntries: Entry[] = [
  ['id', 'bedrock'],
  ['type', 'bedrock'],
  ['region', 'us-east-1'],
].map(([k, v]) => ({ path: `providers.bedrock.${k}`, value: v, source: { layer: 'ui' }, shadowed: [], locked: false, hot: true, secret: false }));

type UserV2 = User & { last_login_at?: string; must_change_password?: boolean };

const people: UserV2[] = [
  { ...owner, last_login_at: ago(2) },
  { ...member, last_login_at: ago(60 * 5) },
  { ...member, id: 'usr_01jpriya00000000000000000', username: 'priya', display_name: 'Priya Rao', role: 'admin', created_at: ago(60 * 24 * 10), last_login_at: ago(60 * 24 * 3) },
  { ...member, id: 'usr_01jjordan0000000000000000', username: 'jordan', display_name: 'Jordan Kim', created_at: ago(60 * 24), last_login_at: ZERO, must_change_password: true },
];

type Effective = { entries: Entry[]; errors: unknown[]; safe_mode: boolean; restart_required: string[] };

export const state = {
  tokens: [] as TokenRecord[],
  users: [] as UserV2[],
  persona: '',
  personaReads: 0,
  approvalsIn: 'web',
  effective: {} as Effective,
};
onReset(() =>
  Object.assign(state, {
    tokens: clone(tokens),
    users: clone(people),
    persona,
    personaReads: 0,
    approvalsIn: 'web',
    effective: { ...(clone(effectiveFixture) as Effective), entries: [...(clone(effectiveFixture) as Effective).entries, ...clone(pending)] },
  }),
);

const solo = scenario('people-solo', 'Only the owner has an account (People shows its empty state).', 'settings');
const bedrock = scenario('provider-bedrock', "A Bedrock provider using this server's AWS role (S3 shape).", 'settings');
const personaChanged = scenario('persona-changed', 'Someone else saves the personality while it is being edited.', 'settings');

function setConfig(path: string, value: unknown, by: string) {
  const isObj = value && typeof value === 'object' && !Array.isArray(value);
  const leaves: [string, unknown][] = isObj ? Object.entries(value as Record<string, unknown>).map(([k, v]) => [`${path}.${k}`, v]) : [[path, value]];
  for (const [p, v] of leaves) {
    let e = state.effective.entries.find((x) => x.path === p);
    if (!e) {
      e = { path: p, value: v, source: { layer: 'ui' }, shadowed: [], locked: false, hot: true, secret: /api_key|token/.test(p) };
      state.effective.entries.push(e);
    }
    if (e.source.layer !== 'ui') e.shadowed = [{ source: e.source, value: e.value }, ...(e.shadowed ?? [])];
    e.value = v;
    e.source = { layer: 'ui', file: '/data/bothy.ui.yaml', by, at: nowISO() };
  }
  // Like the gateway, removing a list item or map entry (null) drops everything under it.
  if (value === null) state.effective.entries = state.effective.entries.filter((x) => !x.path.startsWith(`${path}.`));
}

function effective(req: Req): Effective {
  if (!bedrock(req) || state.effective.entries.some((e) => e.path.startsWith('providers.bedrock.'))) return state.effective;
  return { ...state.effective, entries: [...state.effective.entries, ...bedrockEntries] };
}

on('GET', '/api/config', (req, res, _m, q) => json(res, 200, { ...effective(req), schema: q.get('schema') ? schema : undefined }));
on('PATCH', '/api/config', async (req, res) => {
  const { set } = JSON.parse(await body(req));
  const bad = Object.entries(set as Record<string, unknown>)
    .filter(([p, v]) => (p === 'budgets.daily_usd' && typeof v === 'number' && v < 0) || p.startsWith('server.listen'))
    .map(([p]) => ({ path: p, source: { layer: 'ui' }, message: p.startsWith('server.listen') ? `bothy.ui.yaml: ${p}: locked; set it in bothy.yaml or the environment` : `bothy.ui.yaml: ${p}: must be 0 or more` }));
  if (bad.length) return json(res, 400, { error: { code: 'bad_request', message: bad.map((b) => b.message).join('; ') }, fields: bad });
  if (bedrock(req) && !state.effective.entries.some((e) => e.path.startsWith('providers.bedrock.'))) state.effective.entries.push(...clone(bedrockEntries));
  for (const [p, v] of Object.entries(set as Record<string, unknown>)) setConfig(p, v, `user:${core.me.id}`);
  const paths = Object.keys(set);
  const restart = paths.filter((p) => p.startsWith('sandbox.backend') || state.effective.entries.some((e) => e.path === p && !e.hot));
  state.effective.restart_required = [...new Set([...state.effective.restart_required, ...restart])];
  emit('config.changed', { paths, restart_required: restart });
  json(res, 200, { restart_required: restart });
});
on('DELETE', '/api/config/{path...}', (_q, res, m) => {
  const e = state.effective.entries.find((x) => x.path === decodeURIComponent(m[1]));
  if (e?.source.layer === 'ui' && e.shadowed?.length) {
    const [next, ...rest] = e.shadowed;
    e.value = next.value;
    e.source = next.source;
    e.shadowed = rest;
  }
  json(res, 204);
});

on('GET', '/api/providers', (req, res) =>
  json(res, 200, bedrock(req) ? [...providers, { id: 'bedrock', kind: 'bedrock', state: 'ok', since: ago(60 * 24 * 2), source: 'instance role bothy-prod' }] : providers),
);
on('GET', '/api/models', (req, res) => json(res, 200, bedrock(req) ? [...models, ...bedrockModels] : models));

on('GET', '/api/persona', (req, res) => {
  state.personaReads++;
  json(res, 200, { content: personaChanged(req) && state.personaReads > 1 ? changedPersona : state.persona });
});
on('PUT', '/api/persona', async (req, res) => {
  state.persona = JSON.parse(await body(req)).content;
  json(res, 204);
});

// The signed-in person's own profile (server need: self-service name and approval delivery).
on('GET', '/api/me', (req, res) => json(res, 200, { display_name: viewer(req).display_name, approvals_in: state.approvalsIn }));
on('PATCH', '/api/me', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (typeof b.display_name === 'string') {
    if (!b.display_name.trim()) return fail(res, 400, 'bad_request', 'display_name must not be empty');
    if (viewer(req).id === core.me.id) core.me.display_name = b.display_name;
    const u = state.users.find((x) => x.id === viewer(req).id);
    if (u) u.display_name = b.display_name;
  }
  if (typeof b.approvals_in === 'string') state.approvalsIn = b.approvals_in;
  json(res, 200, { display_name: viewer(req).display_name, approvals_in: state.approvalsIn });
});

on('GET', '/api/tokens', (_q, res) => json(res, 200, state.tokens));
on('POST', '/api/tokens', async (req, res) => {
  const b = JSON.parse(await body(req));
  const meta = { id: newId('tok'), user_id: core.me.id, name: b.name, scopes: b.scopes, on_approval: b.on_approval, rate_per_min: b.rate_per_min || 30, created_at: nowISO(), last_used_at: ZERO, expires_at: new Date(Date.now() + b.ttl / 1e6).toISOString(), revoked_at: ZERO };
  state.tokens.push(meta);
  json(res, 200, { token: `bothy_pat_${meta.id.slice(4).toUpperCase()}_mockSecretValueOnlyShownOnce1234567890abcdEFGH_AbC123`, meta });
});
on('DELETE', '/api/tokens/{id}', (_q, res, m) => {
  const t = state.tokens.find((x) => x.id === m[1]);
  if (t) t.revoked_at = nowISO();
  json(res, 204);
});

on('GET', '/api/users', (req, res) => json(res, 200, solo(req) ? state.users.filter((u) => u.role === 'owner') : state.users));
on('POST', '/api/users', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (state.users.some((u) => u.username.toLowerCase() === String(b.username).toLowerCase())) return fail(res, 409, 'conflict', `username "${b.username}" is taken`);
  // S9: people an admin adds sign in once with the password they were given, then choose their own.
  const u: UserV2 = { ...clone(member), id: newId('usr'), username: b.username, display_name: b.display_name, role: b.role, created_at: nowISO(), last_login_at: ZERO, must_change_password: true };
  state.users.push(u);
  json(res, 200, u);
});
on('PATCH', '/api/users/{id}', async (req, res, m) => {
  const u = state.users.find((x) => x.id === m[1]);
  if (u) Object.assign(u, JSON.parse(await body(req)));
  if (u && u.id === core.me.id) core.me.display_name = u.display_name;
  json(res, 200, u);
});
on('DELETE', '/api/users/{id}', (_q, res, m) => {
  const u = state.users.find((x) => x.id === m[1]);
  if (u) u.disabled = true;
  json(res, 204);
});
// S9: an admin resets someone's password to a new one-time password.
on('POST', '/api/users/{id}/password', (_q, res, m) => {
  const u = state.users.find((x) => x.id === m[1]);
  if (!u) return fail(res, 404, 'not_found', 'no such user');
  u.must_change_password = true;
  json(res, 200, { one_time_password: 'Wd4k-7RmP-q9Xz-3hTn' });
});
