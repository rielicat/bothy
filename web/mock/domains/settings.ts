// Settings (C7): people, API tokens, the personality, models and providers,
// and the effective config every settings page reads and writes.
import type { Entry, ModelInfo, Profile, ProviderHealth, TokenRecord, User } from '../../src/lib/api/types.gen.ts';
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
  { id: 'anthropic', kind: 'anthropic', state: 'ok', since: ago(60 * 24), source: 'Saved key' },
  { id: 'ollama', kind: 'ollama', state: 'down', last_error: 'Post "http://host.docker.internal:11434/api/chat": dial tcp 172.17.0.1:11434: connect: connection refused', since: ago(60 * 3) },
];

export const persona = `# Bothy

You are Bothy, the AI agent for the Acme team. Be warm, brief and practical.
Prefer plain language and link to the code or the document you used.
Ask before doing anything that leaves the company or can't be undone.`;

const changedPersona = `${persona}
When someone asks about a release, check the release checklist in acme/web first.`;

// Settings the effective.json fixture predates.
const pending: Entry[] = [
  { path: 'agent.name', value: 'Bothy', source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.tone', value: 'warm', source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.refine.enabled', value: false, source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.refine.drafts', value: 3, source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.refine.depth', value: 2, source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  { path: 'agent.refine.early_stop', value: true, source: { layer: 'default' }, shadowed: [], locked: false, hot: true, secret: false },
  ...(
    [
      ['auth.sign_in.allow', []],
      ['auth.sign_in.allow_new', false],
      ['auth.sign_in.google.enabled', false],
      ['auth.sign_in.google.client_id', ''],
      ['auth.sign_in.google.client_secret', ''],
      ['auth.sign_in.google.api_base', ''],
      ['auth.sign_in.github.enabled', false],
      ['auth.sign_in.github.client_id', ''],
      ['auth.sign_in.github.client_secret', ''],
      ['auth.sign_in.github.orgs', []],
      ['auth.sign_in.github.org_ids', []],
      ['auth.sign_in.github.api_base', ''],
    ] as [string, unknown][]
  ).map(([path, value]): Entry => ({ path, value, source: { layer: 'default' }, shadowed: [], locked: path.endsWith('.api_base'), hot: true, secret: path.endsWith('.client_secret') })),
];

const bedrockEntries: Entry[] = [
  ['id', 'bedrock'],
  ['type', 'bedrock'],
  ['region', 'us-east-1'],
].map(([k, v]) => ({ path: `providers.bedrock.${k}`, value: v, source: { layer: 'ui' }, shadowed: [], locked: false, hot: true, secret: false }));

const people: User[] = [
  owner,
  member,
  { ...member, id: 'usr_01jpriya00000000000000000', username: 'priya', display_name: 'Priya Rao', role: 'admin', created_at: ago(60 * 24 * 10), last_login_at: ago(60 * 24 * 3) },
  { ...member, id: 'usr_01jjordan0000000000000000', username: 'jordan', display_name: 'Jordan Kim', created_at: ago(60 * 24), last_login_at: ZERO, must_change_password: true },
];

type Effective = { entries: Entry[]; errors: unknown[]; safe_mode: boolean; restart_required: string[] };

export const state = {
  tokens: [] as TokenRecord[],
  users: [] as User[],
  persona: '',
  personaReads: 0,
  approvalsIn: '',
  preferredChat: '',
  effective: {} as Effective,
};
onReset(() =>
  Object.assign(state, {
    tokens: clone(tokens),
    users: clone(people),
    persona,
    personaReads: 0,
    approvalsIn: '',
    preferredChat: '',
    effective: { ...(clone(effectiveFixture) as Effective), entries: [...(clone(effectiveFixture) as Effective).entries, ...clone(pending)] },
  }),
);

// GET /api/refine: the defaults and the cost the server works out from them (h_refine.go refineWork).
// Shares chat.ts's "refine" switch: refine:none takes the estimate and these defaults away.
const refineSwitch = scenario('refine', 'Refine cost for the composer (see chat.ts).', 'chat');
const refineDown = (req: Req) => refineSwitch(req) && refineSwitch.arg(req) === 'none';
on('GET', '/api/refine', (req, res) => {
  if (refineDown(req)) return fail(res, 500, 'internal', 'mock: refine:none');
  const v = (p: string, d: unknown) => state.effective.entries.find((e) => e.path === p)?.value ?? d;
  const drafts = Math.max(Number(v('agent.refine.drafts', 3)), 1);
  const depth = Math.max(Number(v('agent.refine.depth', 2)), 0);
  const early = v('agent.refine.early_stop', true) !== false;
  const rounds = early ? Math.floor((depth + 1) / 2) : depth;
  json(res, 200, { enabled: v('agent.refine.enabled', false) === true, drafts, depth, early_stop: early, critic_slot: 'main', max_calls: drafts * (1 + 3 * depth), multiplier: drafts * (1 + 3 * rounds) });
});

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
  json(res, 200, { restart_required: writeConfig(set) });
});

/** Applies a config change set as PATCH /api/config does; returns what needs a restart. */
export function writeConfig(set: Record<string, unknown>): string[] {
  for (const [p, v] of Object.entries(set)) setConfig(p, v, `user:${core.me.id}`);
  const paths = Object.keys(set);
  const restart = paths.filter((p) => p.startsWith('sandbox.backend') || state.effective.entries.some((e) => e.path === p && !e.hot));
  state.effective.restart_required = [...new Set([...state.effective.restart_required, ...restart])];
  emit('config.changed', { paths, restart_required: restart });
  return restart;
}
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
  json(res, 200, bedrock(req) ? [...providers, { id: 'bedrock', kind: 'bedrock', state: 'ok', since: ago(60 * 24 * 2), source: "This server's AWS access (instance role bothy-prod)" }] : providers),
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

// The signed-in person's own profile, worked out as h_uistate.go profile() does.
// Linked identities belong to connections.ts, which reads this domain's state at reset, so it is imported late.
async function profile(req: Req): Promise<Profile> {
  const { state: connections } = await import('./connections.ts');
  const me = viewer(req);
  const linked = connections.identities.filter((i) => i.status === 'linked' && i.user_id === me.id && i.dm_chat_id);
  const chats = [...new Set(linked.map((i) => i.channel))].sort();
  const last = [...linked].sort((a, b) => b.last_seen_at.localeCompare(a.last_seen_at))[0];
  let approvalsIn = 'web';
  if (state.approvalsIn !== 'web') approvalsIn = chats.includes(state.preferredChat) ? state.preferredChat : (last?.channel ?? 'web');
  const tz = state.users.find((x) => x.id === me.id)?.timezone || me.timezone;
  return { user_id: me.id, username: me.username, display_name: me.display_name, timezone: tz, approvals_in: approvalsIn, preferred_chat: state.preferredChat, chats };
}
on('GET', '/api/me', async (req, res) => json(res, 200, await profile(req)));
function knownZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}
on('PATCH', '/api/me', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (typeof b.display_name === 'string') {
    const name = b.display_name.trim();
    if (!name) return fail(res, 400, 'bad_request', 'enter a name');
    const same = (s: string) => s.trim().toLowerCase() === name.toLowerCase();
    if (name !== viewer(req).display_name && state.users.some((x) => x.id !== viewer(req).id && (same(x.display_name) || same(x.username))))
      return fail(res, 400, 'bad_request', `invalid: someone else here goes by "${name}"; add an initial or a surname`);
    if (viewer(req).id === core.me.id) core.me.display_name = name;
    const u = state.users.find((x) => x.id === viewer(req).id);
    if (u) u.display_name = name;
  }
  if (typeof b.timezone === 'string') {
    // auth.validTimezone: IANA names only.
    const tz = b.timezone.trim();
    if (!tz || tz.toLowerCase() === 'local' || !knownZone(tz)) return fail(res, 400, 'bad_request', 'invalid: time zone must be an IANA name, like Europe/London');
    if (viewer(req).id === core.me.id) core.me.timezone = tz;
    const u = state.users.find((x) => x.id === viewer(req).id);
    if (u) u.timezone = tz;
  }
  if (typeof b.approvals_in === 'string') {
    if (b.approvals_in !== 'web' && !(await profile(req)).chats.includes(b.approvals_in)) return fail(res, 400, 'bad_request', `link ${b.approvals_in} to your account first`);
    state.approvalsIn = b.approvals_in === 'web' ? 'web' : '';
    if (b.approvals_in !== 'web') state.preferredChat = b.approvals_in;
  }
  json(res, 200, await profile(req));
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
  // S9: without a password the server makes a one-time password, replaced at the first sign-in.
  const u: User = { ...clone(member), id: newId('usr'), username: b.username, display_name: b.display_name, role: b.role, created_at: nowISO(), last_login_at: ZERO, must_change_password: !b.password };
  state.users.push(u);
  json(res, 200, b.password ? u : { ...u, one_time_password: 'h7Fq-2mKx-9wPr-tB4n' });
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
