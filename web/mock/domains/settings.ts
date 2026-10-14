// Settings (C7): people, API tokens, the personality, models and providers,
// and the effective config every settings page reads and writes.
import type { Entry, ModelInfo, ProviderHealth, TokenRecord, User } from '../../src/lib/api/types.gen.ts';
import effectiveFixture from '../fixtures/effective.json' with { type: 'json' };
import schema from '../fixtures/schema.json' with { type: 'json' };
import { ago, ahead, body, clone, core, json, member, newId, nowISO, on, onReset, owner, ZERO } from './core.ts';

export const tokens: TokenRecord[] = [
  { id: 'tok_01jwebui000000000000000000', user_id: owner.id, name: 'Open WebUI', scopes: ['chat'], on_approval: 'deny', rate_per_min: 30, created_at: ago(60 * 24 * 8), last_used_at: ago(60 * 3), expires_at: ahead(60 * 24 * 82), revoked_at: ZERO },
];

export const models: ModelInfo[] = [
  ['anthropic', 'claude-sonnet-4-6', 'Claude Sonnet 4.6', 200000, 3_000_000, 15_000_000],
  ['anthropic', 'claude-haiku-4-5', 'Claude Haiku 4.5', 200000, 1_000_000, 5_000_000],
  ['ollama', 'qwen3:14b', 'Qwen3 14B (local)', 16384, 0, 0],
].map(([provider, model, display, context, inp, out]) => ({
  ref: { provider: provider as string, model: model as string },
  display: display as string,
  context: context as number,
  max_output: 8192,
  tools: true,
  vision: provider === 'anthropic',
  thinking: false,
  cache: provider === 'anthropic' ? 1 : 0,
  local: provider === 'ollama',
  price: { in_per_m: inp as number, out_per_m: out as number, cache_read_per_m: (inp as number) / 10, cache_write_per_m: (inp as number) * 1.25, known: true },
  source: 'catalog',
}));

export const providers: ProviderHealth[] = [
  { id: 'anthropic', kind: 'anthropic', state: 'ok', since: ago(60 * 24) },
  { id: 'ollama', kind: 'ollama', state: 'degraded', last_error: 'served context 4,096 is smaller than configured 16,384', since: ago(60 * 3) },
];

export const persona = `# Bothy

You are Bothy, Sam's assistant. Be warm, brief and practical.
Prefer plain language. Ask before doing anything that leaves this machine.`;

type Effective = { entries: Entry[]; errors: unknown[]; safe_mode: boolean; restart_required: string[] };

export const state = { tokens: [] as TokenRecord[], users: [] as User[], persona: '', effective: {} as Effective };
onReset(() => Object.assign(state, { tokens: clone(tokens), users: [clone(owner), clone(member)], persona, effective: clone(effectiveFixture) as Effective }));

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
}

on('GET', '/api/config', (_q, res, _m, q) => json(res, 200, { ...state.effective, schema: q.get('schema') ? schema : undefined }));
on('PATCH', '/api/config', async (req, res) => {
  const { set } = JSON.parse(await body(req));
  const bad = Object.entries(set as Record<string, unknown>)
    .filter(([p, v]) => (p === 'budgets.daily_usd' && typeof v === 'number' && v < 0) || p.startsWith('server.listen'))
    .map(([p]) => ({ path: p, source: { layer: 'ui' }, message: p.startsWith('server.listen') ? `bothy.ui.yaml: ${p}: locked; set it in bothy.yaml or the environment` : `bothy.ui.yaml: ${p}: must be 0 or more` }));
  if (bad.length) return json(res, 400, { error: { code: 'bad_request', message: bad.map((b) => b.message).join('; ') }, fields: bad });
  for (const [p, v] of Object.entries(set as Record<string, unknown>)) setConfig(p, v, `user:${core.me.id}`);
  const restart = Object.keys(set).filter((p) => p.startsWith('sandbox.backend'));
  state.effective.restart_required = [...new Set([...state.effective.restart_required, ...restart])];
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

on('GET', '/api/providers', (_q, res) => json(res, 200, providers));
on('GET', '/api/models', (_q, res) => json(res, 200, models));

on('GET', '/api/persona', (_q, res) => json(res, 200, { content: state.persona }));
on('PUT', '/api/persona', async (req, res) => {
  state.persona = JSON.parse(await body(req)).content;
  json(res, 204);
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

on('GET', '/api/users', (_q, res) => json(res, 200, state.users));
on('POST', '/api/users', async (req, res) => {
  const b = JSON.parse(await body(req));
  const u = { ...clone(member), id: newId('usr'), username: b.username, display_name: b.display_name, role: b.role };
  state.users.push(u);
  json(res, 200, u);
});
on('PATCH', '/api/users/{id}', async (req, res, m) => {
  const u = state.users.find((x) => x.id === m[1]);
  if (u) Object.assign(u, JSON.parse(await body(req)));
  json(res, 200, u);
});
on('DELETE', '/api/users/{id}', (_q, res, m) => {
  const u = state.users.find((x) => x.id === m[1]);
  if (u) u.disabled = true;
  json(res, 204);
});
