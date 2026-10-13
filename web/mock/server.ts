// Mock gateway for UI development: `npm run mock` serves the SPA with every
// /api route answered from in-memory fixtures and a scripted SSE run, so the
// screens can be built and reviewed without a Go build or real providers.
//
//   MOCK_SETUP=1  start in first-run state (setup token "demo-setup-token")
//   MOCK_LOGIN=0  start logged out (any username, any password but "wrong")
//
// Dev only: vite.config.ts installs it only when BOTHY_MOCK=1 (`npm run mock`,
// or `npm run preview:mock` for the built app under the production CSP), so
// nothing here ships in the build.
import type { Plugin } from 'vite';
import type { Approval, BannerData, Card, Entry, Message, Schedule, Session, SetupState } from '../src/lib/api/types.gen.ts';
import effectiveFixture from './fixtures/effective.json' with { type: 'json' };
import schema from './fixtures/schema.json' with { type: 'json' };
import * as fx from './fixtures.ts';

interface Req {
  method?: string;
  url?: string;
  headers: Record<string, string | string[] | undefined>;
  on(ev: 'data' | 'end' | 'close', fn: (chunk?: { toString(enc?: string): string }) => void): void;
}
interface Res {
  statusCode: number;
  setHeader(k: string, v: string): void;
  write(s: string): void;
  end(s?: string): void;
  on(ev: 'close', fn: () => void): void;
}
declare const process: { env: Record<string, string | undefined> };

const env = process.env;
const clone = <T>(v: T): T => structuredClone(v);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const alphabet = '0123456789abcdefghjkmnpqrstvwxyz';
const newId = (p: string) => `${p}_` + Array.from({ length: 26 }, () => alphabet[Math.floor(Math.random() * 32)]).join('');
const nowISO = () => new Date().toISOString();

const SETUP_TOKEN = 'demo-setup-token';
const boot = newId('boot').slice(5, 13);

function fresh(setup = env.MOCK_SETUP === '1', login = env.MOCK_LOGIN !== '0') {
  return {
    setup: { needs_setup: setup, claimed: false, owner_set: !setup, sandbox_available: true } as SetupState,
    loggedIn: !setup && login,
    me: clone(fx.owner),
    mode: 'single',
    paused: false,
    sessions: clone(fx.sessions),
    messages: clone(fx.messages) as Record<string, Message[]>,
    pins: clone(fx.pins),
    approvals: clone(fx.approvals) as Approval[],
    recent: clone(fx.recentApprovals) as Approval[],
    schedules: clone(fx.schedules) as Schedule[],
    memory: clone(fx.memory),
    etags: { MEMORY: 'e1', USER: 'e1', HEARTBEAT: 'e1' } as Record<string, string>,
    memoryHistory: clone(fx.memoryHistory),
    tokens: clone(fx.tokens),
    secrets: clone(fx.secrets),
    grants: clone(fx.grants),
    skills: clone(fx.skills),
    identities: clone(fx.identities),
    spaces: clone(fx.spaces),
    users: [clone(fx.owner), clone(fx.member)],
    persona: fx.persona,
    effective: clone(effectiveFixture) as { entries: Entry[]; errors: unknown[]; safe_mode: boolean; restart_required: string[] },
    banners: [{ level: 'warn', code: 'mcp_pins', text: 'One MCP tool changed and waits for your review.', action: '/settings/mcp' }] as BannerData[],
    seq: 0,
  };
}

let st = fresh();

// Unit tests reuse the fixture gateway through handle(); resetMock gives
// each test a clean state.
export function resetMock(opts: { setup?: boolean; login?: boolean } = {}) {
  st = fresh(opts.setup ?? false, opts.login ?? true);
}

const clients = new Set<Res>();
const waiting = new Map<string, (choice: string) => void>();
const stopped = new Set<string>();

function emit(type: string, data: unknown, sessionId?: string, runId?: string) {
  const ev = { seq: ++st.seq, type, principal_id: fx.owner.id, session_id: sessionId, run_id: runId, at: nowISO(), data };
  const frame = `id: ${boot}-${ev.seq}\nevent: ${type}\ndata: ${JSON.stringify(ev)}\n\n`;
  for (const c of clients) c.write(frame);
}

function me() {
  return { user: st.me, csrf: 'mock-csrf', mode: st.mode, principals: fx.principals, paused: st.paused, sandbox_available: true, banners: st.banners, access: { peer: 'local', tls: false } };
}

function json(res: Res, status: number, body?: unknown) {
  res.statusCode = status;
  res.setHeader('Content-Type', 'application/json');
  res.setHeader('Cache-Control', 'no-store');
  res.end(body === undefined ? '' : JSON.stringify(body));
}

function fail(res: Res, status: number, code: string, message: string, fix?: string) {
  json(res, status, { error: { code, message, fix } });
}

function body(req: Req): Promise<string> {
  return new Promise((resolve) => {
    let b = '';
    req.on('data', (c) => (b += c?.toString('latin1') ?? ''));
    req.on('end', () => resolve(b));
  });
}

function addMessage(sid: string, m: Omit<Message, 'id' | 'seq' | 'session_id' | 'created_at' | 'tokens'>): Message {
  const list = (st.messages[sid] ??= []);
  const full: Message = { ...m, id: Math.floor(Math.random() * 1e9), seq: (list.at(-1)?.seq ?? 0) + 1, session_id: sid, tokens: 50, created_at: nowISO() };
  list.push(full);
  const s = st.sessions.find((x) => x.id === sid);
  if (s) s.last_message_at = full.created_at;
  emit('message.created', { message: full }, sid, m.run_id);
  return full;
}

async function stream(sid: string, run: string, text: string) {
  const words = text.split(/(?<=\s)/);
  for (let i = 0; i < words.length; i += 3) {
    if (stopped.has(run)) return;
    emit('run.delta', { text: words.slice(i, i + 3).join('') }, sid, run);
    await sleep(45);
  }
}

const bd = { constitution: 1650, persona: 240, memory: 180, skills: 120, pins: 0, tools: 1380, summary: 0, history: 2400, tool_results: 900, turn_context: 160, total: 7030, window: 200000, reserve: 8192, cacheable: 3270 };

async function runScenario(sid: string, text: string) {
  const run = newId('run');
  const s = st.sessions.find((x) => x.id === sid);
  emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, sid, run);
  await sleep(250);
  emit('run.status', { status: 'thinking' }, sid, run);
  await sleep(500);
  const risky = /shell|script|run |approve|delete|email|send/i.test(text);
  const intro = risky ? 'I can do that. I need to run a short script in the sandbox first.' : "Let me check a couple of sources.";
  await stream(sid, run, intro);
  const tool = risky ? 'shell' : 'web_search';
  const args = risky ? { command: 'python3 tidy.py --dry-run reports/' } : { query: text.slice(0, 60) };
  const callId = `call_${Math.floor(Math.random() * 1e6)}`;
  addMessage(sid, { role: 'assistant', kind: 'normal', provenance: 'system', run_id: run, parts: [{ t: 'text', text: intro }, { t: 'tool_call', call: { id: callId, name: tool, args } }] });
  const tcl = newId('tcl');
  const action = risky
    ? { tool, kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', args.command as string], cwd: '.', payload: args, fingerprint: 'x' }
    : { tool, kind: 'search', side_effect: 'read', payload: args, fingerprint: 'y' };
  emit('run.tool_call', { call_id: tcl, tool, title: risky ? 'Run shell command' : 'Search the web', action, verdict: risky ? 'ask' : 'auto', rule: risky ? 'default:local_write' : 'default:read' }, sid, run);
  let allowed = true;
  if (risky) {
    emit('run.status', { status: 'waiting_approval' }, sid, run);
    const card: Card = {
      title: 'Run shell command',
      risk: 'local_write',
      fields: [
        { label: 'Command', value: args.command as string, mono: true },
        { label: 'Folder', value: 'reports/', mono: true },
      ],
      warnings: s && (s.taint & 3) === 3 ? ['This chat holds private and untrusted data'] : undefined,
      context: `Web · session “${s?.title || 'New chat'}”`,
      choices: ['allow_once', 'allow_session', 'allow_always', 'deny'],
      expires: new Date(Date.now() + 15 * 60_000).toISOString(),
      web_url: '',
    };
    const a: Approval = {
      id: newId('apr'),
      run_id: run,
      session_id: sid,
      principal_id: fx.owner.id,
      tool_call_id: tcl,
      action: action as Approval['action'],
      decision: { verdict: 'ask', rule: 'default:local_write', reason: 'Writes files in your workspace', floor: false, approver: { user_id: fx.owner.id, admin_only: false }, timeout: 900e9, on_timeout: 'deny', choices: card.choices },
      card,
      status: 'pending',
      approver: { user_id: fx.owner.id, admin_only: false },
      on_timeout: 'deny',
      created_at: nowISO(),
      expires_at: card.expires,
    };
    st.approvals.push(a);
    emit('approval.requested', { approval: a }, sid, run);
    const choice = await new Promise<string>((resolve) => waiting.set(a.id, resolve));
    allowed = choice !== 'deny';
    if (stopped.has(run)) return;
  }
  emit('run.status', { status: 'tool', detail: risky ? '`python3 tidy.py`' : 'web search' }, sid, run);
  if (allowed) {
    for (const line of risky ? ['scanning reports/ …\n', 'would move 3 files to reports/archive\n', 'done in 1.4 s\n'] : ['3 results\n']) {
      emit('run.tool_output', { call_id: tcl, stream: 1, chunk: line }, sid, run);
      await sleep(350);
    }
  }
  const preview = allowed ? (risky ? 'would move 3 files to reports/archive' : 'Top result: a recent overview with three key points.') : 'denied by the user';
  emit('run.tool_result', { call_id: tcl, status: allowed ? 'ok' : 'denied', preview, bytes: 512, ms: 1400, truncated: false, untrusted: !risky }, sid, run);
  addMessage(sid, { role: 'tool', kind: 'normal', provenance: risky ? 'tool' : 'untrusted', run_id: run, parts: [{ t: 'tool_result', result: { call_id: callId, name: tool, content: preview, is_error: !allowed, untrusted: !risky } }] });
  emit('run.status', { status: 'thinking' }, sid, run);
  await sleep(400);
  const answer = allowed
    ? risky
      ? 'Done. The dry run found **3 files** to archive:\n\n- `q1-summary.pdf`\n- `q2-summary.pdf`\n- `old-notes.txt`\n\nSay the word and I will move them for real.'
      : `Here is what I found about **${text.slice(0, 40)}**:\n\n1. The main points are easy to summarize.\n2. Sources broadly agree.\n3. One source disagrees on the details.\n\nWant me to keep an eye on this and send you a weekly digest?`
    : "Understood, I won't run it. Let me know if you'd like a different approach.";
  await stream(sid, run, answer);
  if (stopped.has(run)) return;
  addMessage(sid, { role: 'assistant', kind: 'normal', provenance: 'system', run_id: run, parts: [{ t: 'text', text: answer }] });
  const used = { input: 1800, output: 240, cache_read: 3270, cache_write: 0, reasoning: 0 };
  emit('run.usage', { call: { id: newId('llm'), ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, slot: 'main', purpose: 'agent', step: 2, attempts: [], usage: used, usage_estimated: false, cost_micros: 6900, price_known: true, ttft: 600e6, latency: 2100e6, breakdown: { ...bd, history: bd.history + 900, total: bd.total + 900 }, prefix_hash: 'a1b2', stop: 'end_turn' }, run_cost_micros: 11200, day_cost_micros: 431_200, day_limit_micros: 3_000_000 }, sid, run);
  emit('run.finished', { status: 'done', steps: 2, usage: used, cost_micros: 11200 }, sid, run);
  if (s && !s.title) {
    s.title = text.length > 36 ? text.slice(0, 36) + '…' : text;
    emit('session.updated', { title: s.title, taint: s.taint, epoch: s.epoch }, sid);
  }
}

function setConfig(path: string, value: unknown, by: string) {
  const isObj = value && typeof value === 'object' && !Array.isArray(value);
  const leaves: [string, unknown][] = isObj ? Object.entries(value as Record<string, unknown>).map(([k, v]) => [`${path}.${k}`, v]) : [[path, value]];
  for (const [p, v] of leaves) {
    let e = st.effective.entries.find((x) => x.path === p);
    if (!e) {
      e = { path: p, value: v, source: { layer: 'ui' }, shadowed: [], locked: false, hot: true, secret: /api_key|token/.test(p) };
      st.effective.entries.push(e);
    }
    if (e.source.layer !== 'ui') e.shadowed = [{ source: e.source, value: e.value }, ...(e.shadowed ?? [])];
    e.value = v;
    e.source = { layer: 'ui', file: '/data/bothy.ui.yaml', by, at: nowISO() };
  }
}

type Handler = (req: Req, res: Res, m: RegExpMatchArray, q: URLSearchParams) => unknown | Promise<unknown>;
const routes: [string, RegExp, Handler][] = [];
const on = (method: string, path: string, h: Handler) => routes.push([method, new RegExp(`^${path.replace(/\{(\w+)\.\.\.\}/g, '(.+)').replace(/\{(\w+)\}/g, '([^/]+)')}$`), h]);

on('GET', '/api/setup/state', (_q, res) => json(res, 200, st.setup));
on('POST', '/api/setup/claim', async (req, res) => {
  const { token } = JSON.parse(await body(req));
  if (token !== SETUP_TOKEN) return fail(res, 401, 'unauthorized', 'That setup token is not valid or has expired.', 'Run `docker exec bothy /bothy setup-token` to print a new one.');
  st.setup.claimed = true;
  st.setup.csrf = 'mock-csrf';
  json(res, 200, { csrf: 'mock-csrf' });
});
on('POST', '/api/setup/owner', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (b.password === 'password1234') return fail(res, 400, 'bad_request', 'This password is on the list of common passwords. Pick something less guessable.');
  st.me = { ...st.me, username: b.username, display_name: b.username, timezone: b.timezone };
  st.setup.owner_set = true;
  delete st.setup.csrf;
  st.loggedIn = true;
  json(res, 200, me());
});
on('POST', '/api/setup/complete', (_q, res) => {
  // Like the gateway, setup routes are gone once setup is done.
  if (!st.setup.needs_setup) return fail(res, 404, 'not_found', 'not found');
  st.setup.needs_setup = false;
  json(res, 204);
});
on('POST', '/api/auth/login', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (!b.username || b.password === 'wrong') return fail(res, 401, 'unauthorized', 'Wrong username or password.');
  st.loggedIn = true;
  json(res, 200, me());
});

// Everything below needs a session.
const open = new Set(['/api/setup/state', '/api/setup/claim', '/api/setup/owner', '/api/auth/login']);

on('POST', '/api/auth/logout', (_q, res) => {
  st.loggedIn = false;
  json(res, 204);
});
on('GET', '/api/auth/me', (_q, res) => json(res, 200, me()));
on('POST', '/api/auth/password', (_q, res) => json(res, 204));
on('GET', '/api/events', (req, res) => {
  res.statusCode = 200;
  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-store');
  res.write(`event: hello\ndata: ${JSON.stringify({ boot, seq: st.seq, paused: st.paused, sandbox: true, banners: st.banners })}\n\n`);
  clients.add(res);
  const ping = setInterval(() => res.write(': ping\n\n'), 15000);
  req.on('close', () => {
    clearInterval(ping);
    clients.delete(res);
  });
});

on('GET', '/api/sessions', (_q, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const items = st.sessions.filter((s) => !s.archived && (!term || s.title.toLowerCase().includes(term))).sort((a, b) => b.last_message_at.localeCompare(a.last_message_at));
  json(res, 200, { items });
});
on('POST', '/api/sessions', (_q, res) => {
  const s: Session = { ...clone(fx.sessions[0]), id: newId('ses'), title: '', taint: 0, created_at: nowISO(), updated_at: nowISO(), last_message_at: nowISO() };
  st.sessions.unshift(s);
  st.messages[s.id] = [];
  json(res, 200, s);
});
const session = (id: string) => st.sessions.find((s) => s.id === id);
on('GET', '/api/sessions/{id}', (_q, res, m) => {
  const s = session(m[1]);
  if (!s) return fail(res, 404, 'not_found', 'No such chat.');
  json(res, 200, { session: s, pins: st.pins[s.id] ?? [], pending_approvals: st.approvals.filter((a) => a.session_id === s.id && (a.status === 'pending' || a.status === 'parked')) });
});
on('PATCH', '/api/sessions/{id}', async (req, res, m) => {
  const s = session(m[1]);
  if (!s) return fail(res, 404, 'not_found', 'No such chat.');
  const b = JSON.parse(await body(req));
  if (b.title !== undefined) s.title = b.title;
  if (b.model_override !== undefined) s.model_override = b.model_override || undefined;
  json(res, 200, s);
});
on('DELETE', '/api/sessions/{id}', (_q, res, m) => {
  const s = session(m[1]);
  if (s) s.archived = true;
  json(res, 204);
});
on('GET', '/api/sessions/{id}/messages', (_q, res, m, q) => {
  const all = st.messages[m[1]] ?? [];
  const before = Number(q.get('before') ?? 0);
  const limit = Number(q.get('limit') ?? 50);
  const list = before ? all.filter((x) => x.seq < before) : all;
  json(res, 200, { messages: list.slice(-limit), has_more: list.length > limit });
});
on('POST', '/api/sessions/{id}/messages', async (req, res, m) => {
  if (st.paused) return fail(res, 503, 'paused', 'Bothy is paused by an admin.');
  const b = JSON.parse(await body(req));
  const parts: Message['parts'] = [{ t: 'text', text: b.text }];
  for (const u of b.uploads ?? []) parts.push({ t: 'file', blob: { mime: 'application/octet-stream', name: `${u}.txt`, ref: `.bothy/in/${u}/file.txt`, size: 1234 } });
  addMessage(m[1], { role: 'user', kind: 'normal', provenance: 'user', sender: `web:${st.me.id}`, parts });
  void runScenario(m[1], b.text);
  json(res, 202, { run_id: 'run_pending', queued: false, steered: false });
});
on('POST', '/api/sessions/{id}/stop', (_q, res, m) => {
  const last = [...(st.messages[m[1]] ?? [])].reverse().find((x) => x.run_id)?.run_id;
  if (last) stopped.add(last);
  for (const [id, resolve] of waiting) {
    const a = st.approvals.find((x) => x.id === id);
    if (a?.session_id === m[1]) {
      a.status = 'cancelled';
      emit('approval.resolved', { approval_id: id, status: 'cancelled', via: 'stop' }, m[1]);
      resolve('deny');
    }
  }
  addMessage(m[1], { role: 'assistant', kind: 'notice', provenance: 'system', parts: [{ t: 'text', text: 'Stopped. 1 step, $0.004' }] });
  emit('run.finished', { status: 'stopped', steps: 1, usage: { input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0 }, cost_micros: 4000 }, m[1], last);
  json(res, 200, { stopped: true });
});
on('POST', '/api/sessions/{id}/compact', (_q, res) => json(res, 202));
on('GET', '/api/sessions/{id}/context', (_q, res) => json(res, 200, bd));
on('POST', '/api/sessions/{id}/fork', (_q, res, m) => {
  const old = session(m[1]);
  const s: Session = { ...clone(old ?? fx.sessions[0]), id: newId('ses'), title: `${old?.title ?? 'Chat'} (clean)`, taint: 0, created_at: nowISO(), last_message_at: nowISO() };
  st.sessions.unshift(s);
  st.messages[s.id] = [{ id: 1, session_id: s.id, seq: 1, role: 'assistant', kind: 'notice', parts: [{ t: 'text', text: `Clean fork of “${old?.title}”. Earlier history is not in context.` }], provenance: 'system', tokens: 10, created_at: nowISO() }];
  json(res, 200, s);
});
on('GET', '/api/sessions/{id}/pins', (_q, res, m) => json(res, 200, st.pins[m[1]] ?? []));
on('POST', '/api/sessions/{id}/pins', async (req, res, m) => {
  const b = JSON.parse(await body(req));
  const p = { id: Math.floor(Math.random() * 1e6), session_id: m[1], text: b.text, source: 'user', created_at: nowISO() };
  (st.pins[m[1]] ??= []).push(p);
  json(res, 200, p);
});
on('DELETE', '/api/sessions/{id}/pins/{pin}', (_q, res, m) => {
  st.pins[m[1]] = (st.pins[m[1]] ?? []).filter((p) => String(p.id) !== m[2]);
  json(res, 204);
});
on('POST', '/api/uploads', async (req, res) => {
  const raw = await body(req);
  const name = /filename="([^"]*)"/.exec(raw)?.[1] ?? 'file';
  json(res, 200, { id: newId('upl'), name, mime: 'application/octet-stream', size: raw.length });
});
on('GET', '/api/runs/{id}', (_q, res, m) => json(res, 200, fx.runDetail(m[1])));
on('POST', '/api/runs/{id}/continue', (_q, res) => json(res, 202, { queued: false, steered: false }));
on('GET', '/api/search', (_q, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const hits = [];
  for (const [sid, list] of Object.entries(st.messages))
    for (const msg of list)
      for (const p of msg.parts) {
        const i = p.text?.toLowerCase().indexOf(term) ?? -1;
        if (p.text && i >= 0)
          hits.push({ session_id: sid, title: session(sid)?.title ?? '', seq: msg.seq, snippet: `…${p.text.slice(Math.max(0, i - 30), i)}[${p.text.slice(i, i + term.length)}]${p.text.slice(i + term.length, i + term.length + 40)}…`, at: msg.created_at, rank: 1, trusted: msg.provenance !== 'untrusted' });
      }
  json(res, 200, hits.slice(0, 20));
});

on('GET', '/api/approvals', (_q, res, _m, q) => {
  const status = q.get('status') ?? 'pending';
  json(res, 200, { items: status === 'recent' ? st.recent : st.approvals.filter((a) => a.status === status) });
});
on('GET', '/api/approvals/{id}', (_q, res, m) => {
  const a = [...st.approvals, ...st.recent].find((x) => x.id === m[1]);
  return a ? json(res, 200, a) : fail(res, 404, 'not_found', 'No such approval.');
});
on('POST', '/api/approvals/{id}', async (req, res, m) => {
  const a = st.approvals.find((x) => x.id === m[1]);
  if (!a) return fail(res, 404, 'not_found', 'No such approval.');
  if (a.status !== 'pending' && a.status !== 'parked') return fail(res, 409, 'conflict', 'Already decided.');
  const { choice } = JSON.parse(await body(req));
  Object.assign(a, { status: choice === 'deny' ? 'denied' : 'allowed', choice, decided_by: st.me.id, decided_via: 'web', decided_at: nowISO() });
  st.recent.unshift(a);
  emit('approval.resolved', { approval_id: a.id, status: a.status, choice, by: st.me.id, via: 'web' }, a.session_id, a.run_id);
  waiting.get(a.id)?.(choice);
  waiting.delete(a.id);
  json(res, 200, a);
});

on('GET', '/api/grants', (_q, res, _m, q) => json(res, 200, q.get('scope') === 'schedule' ? [] : st.grants));
on('POST', '/api/grants', async (req, res) => {
  const g = { ...JSON.parse(await body(req)), id: newId('grt'), scope: 'principal', scope_id: '', uses: 0, max_uses: 0, allow_when_tainted: false, created_by: st.me.id, created_at: nowISO(), expires_at: fx.ZERO };
  st.grants.push(g);
  json(res, 200, g);
});
on('DELETE', '/api/grants/{id}', (_q, res, m) => {
  st.grants = st.grants.filter((g) => g.id !== m[1]);
  json(res, 204);
});
on('POST', '/api/policy/preview', async (req, res) => {
  const b = JSON.parse(await body(req));
  const cmd = String(b.args?.command ?? '');
  const ask = !/^(ls|cat|pwd)\b/.test(cmd);
  json(res, 200, {
    action: { tool: b.tool, kind: 'exec', side_effect: 'local_write', argv: ['bash', '-lc', cmd], payload: b.args, fingerprint: 'p' },
    decision: { verdict: ask ? 'ask' : 'auto', rule: ask ? 'default:local_write' : 'rule:readonly-shell', reason: ask ? 'Writes files in your workspace' : 'Matches the read-only shell rule', floor: false, approver: { user_id: st.me.id, admin_only: false }, timeout: 900e9, on_timeout: 'deny', choices: ['allow_once', 'allow_session', 'deny'] },
    card: ask ? { title: 'Run shell command', risk: 'local_write', fields: [{ label: 'Command', value: cmd, mono: true }], context: 'Policy tester', choices: ['allow_once', 'allow_session', 'deny'], expires: nowISO(), web_url: '' } : undefined,
  });
});

const memFile = (f: string) => f.toUpperCase();
on('GET', '/api/memory/export', (_q, res) => {
  res.setHeader('Content-Disposition', 'attachment; filename="memory.tar"');
  res.end('mock tar');
});
on('GET', '/api/memory/{file}', (_q, res, m) => {
  const f = memFile(m[1]);
  json(res, 200, { file: f, content: st.memory[f] ?? '', etag: st.etags[f], cap_bytes: f === 'MEMORY' ? 4096 : 2048 });
});
on('PUT', '/api/memory/{file}', async (req, res, m) => {
  const f = memFile(m[1]);
  if (req.headers['if-match'] !== st.etags[f]) return fail(res, 409, 'conflict', 'The file changed since you loaded it.');
  const { content } = JSON.parse(await body(req));
  st.memoryHistory.unshift({ id: Math.floor(Math.random() * 1e6), principal_id: st.me.id, file: f, op: 'edit', before: st.memory[f] ?? '', after: content, actor: `user:${st.me.id}`, provenance: 'user', at: nowISO() });
  st.memory[f] = content;
  st.etags[f] = newId('e').slice(0, 8);
  json(res, 200, { file: f, content, etag: st.etags[f], cap_bytes: f === 'MEMORY' ? 4096 : 2048 });
});
on('GET', '/api/memory/{file}/history', (_q, res, m) => json(res, 200, { items: st.memoryHistory.filter((e) => e.file === memFile(m[1])) }));
on('GET', '/api/persona', (_q, res) => json(res, 200, { content: st.persona }));
on('PUT', '/api/persona', async (req, res) => {
  st.persona = JSON.parse(await body(req)).content;
  json(res, 204);
});
on('GET', '/api/constitution', (_q, res) => json(res, 200, { version: '3', content: fx.constitution }));

on('GET', '/api/skills', (_q, res) => json(res, 200, st.skills));
on('GET', '/api/skills/{name}', (_q, res, m) => {
  const s = st.skills.find((x) => x.name === m[1]);
  json(res, 200, { meta: s, body: `---\nname: ${s?.name}\ndescription: ${s?.description}\n---\n\n# ${s?.name}\n\nUse this skill when the user asks for help with it.\n\n1. Read the files in the skill folder.\n2. Follow the steps.\n\n[Reference](https://example.com/docs) and <script>alert(1)</script> stay inert.`, files: ['SKILL.md', 'templates/week.md'] });
});
on('PATCH', '/api/skills/{name}', async (req, res, m) => {
  const s = st.skills.find((x) => x.name === m[1]);
  if (s) s.status = JSON.parse(await body(req)).enabled ? 'enabled' : 'disabled';
  json(res, 200, s);
});
on('DELETE', '/api/skills/{name}', (_q, res, m) => {
  st.skills = st.skills.filter((x) => x.name !== m[1]);
  json(res, 204);
});
on('POST', '/api/skills/rescan', (_q, res) => json(res, 200, st.skills));
on('POST', '/api/skills', (_q, res) => {
  const s = { ...st.skills[1], name: `uploaded-${st.skills.length}`, status: 'pending', installed_at: nowISO() };
  st.skills.push(s);
  json(res, 200, s);
});

on('GET', '/api/schedules', (_q, res) => json(res, 200, st.schedules));
on('POST', '/api/schedules/preview', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (b.kind === 'cron' && b.spec.trim().split(/\s+/).length !== 5) return fail(res, 400, 'bad_request', 'A cron expression has five fields: minute hour day month weekday.');
  const human = b.kind === 'heartbeat' ? `Every ${b.spec} (${b.tz})` : b.kind === 'once' ? `Once on ${b.spec} (${b.tz})` : b.spec === '0 8 * * 1-5' ? `Weekdays at 08:00 (${b.tz})` : b.spec === '0 9 * * 1' ? `Mondays at 09:00 (${b.tz})` : `Cron ${b.spec} (${b.tz})`;
  json(res, 200, { human, next: [1, 2, 3, 4, 5].map((d) => new Date(Date.now() + d * 86400_000).toISOString()) });
});
on('GET', '/api/schedules/{id}', (_q, res, m) => {
  const s = st.schedules.find((x) => x.id === m[1]);
  return s ? json(res, 200, s) : fail(res, 404, 'not_found', 'No such schedule.');
});
const upsert = async (req: Req, res: Res, id?: string) => {
  const b = JSON.parse(await body(req));
  const s: Schedule = { ...b.schedule, id: id ?? newId('sch'), next_run_at: new Date(Date.now() + 86400_000).toISOString(), updated_at: nowISO() };
  st.schedules = [...st.schedules.filter((x) => x.id !== s.id), s];
  json(res, 200, s);
};
on('POST', '/api/schedules', (req, res) => upsert(req, res));
on('PATCH', '/api/schedules/{id}', (req, res, m) => upsert(req, res, m[1]));
on('DELETE', '/api/schedules/{id}', (_q, res, m) => {
  st.schedules = st.schedules.filter((x) => x.id !== m[1]);
  json(res, 204);
});
on('POST', '/api/schedules/{id}/run', (_q, res) => json(res, 202, { run_id: 'run_01jbrief1000000000000000' }));
on('GET', '/api/schedules/{id}/runs', (_q, res) => json(res, 200, { items: fx.scheduleRuns }));

on('GET', '/api/mcp', (_q, res) => json(res, 200, fx.mcp));
on('GET', '/api/mcp/{server}/pins', (_q, res, m) => json(res, 200, fx.mcpPins.filter((p) => p.server === m[1])));
on('POST', '/api/mcp/{server}/approve', (_q, res) => {
  st.banners = st.banners.filter((b) => b.code !== 'mcp_pins');
  emit('banner', { level: 'warn', code: 'mcp_pins', text: '', clear: true });
  json(res, 204);
});
on('POST', '/api/mcp/{server}/install', (_q, res) => json(res, 202));
on('POST', '/api/mcp/{server}/reload', (_q, res) => json(res, 202));

on('GET', '/api/providers', (_q, res) => json(res, 200, fx.providers));
on('POST', '/api/providers/test', async (req, res) => {
  const b = JSON.parse(await body(req));
  await sleep(900);
  if (b.api_key === 'bad') return json(res, 200, { ok: false, latency: 300e6, tool_calling: false, usage_reported: false, context: 0, models: [], cost_micros: 0, detail: '401: key revoked or wrong project' });
  if (b.kind === 'ollama') return json(res, 200, { ok: true, latency: 480e6, tool_calling: true, usage_reported: true, context: 4096, models: ['qwen3:14b', 'qwen3:4b'], cost_micros: 0, detail: 'Ollama serves qwen3:14b with 4,096 context; set OLLAMA_CONTEXT_LENGTH=16384 or pick a model with ≥ 16K' });
  json(res, 200, { ok: true, latency: 820e6, tool_calling: true, usage_reported: true, context: 200000, models: ['claude-sonnet-4-6', 'claude-haiku-4-5'], cost_micros: 31, detail: '' });
});
on('POST', '/api/providers/detect', async (_q, res) => {
  await sleep(600);
  json(res, 200, fx.detections);
});
on('GET', '/api/models', (_q, res) => json(res, 200, fx.models));
on('GET', '/api/channels', (_q, res) => json(res, 200, fx.channels));
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
on('POST', '/api/pairing/approve', (_q, res) => json(res, 200, st.identities[1]));
on('GET', '/api/identities', (_q, res) => json(res, 200, st.identities));
on('PATCH', '/api/identities/{channel}/{external}', async (req, res, m) => {
  const i = st.identities.find((x) => x.channel === m[1] && x.external_id === m[2]);
  if (i) i.status = JSON.parse(await body(req)).status;
  json(res, 200, i);
});
on('DELETE', '/api/identities/{channel}/{external}', (_q, res, m) => {
  st.identities = st.identities.filter((x) => !(x.channel === m[1] && x.external_id === m[2]));
  json(res, 204);
});
on('GET', '/api/spaces', (_q, res) => json(res, 200, st.spaces));
on('PATCH', '/api/spaces/{id}', async (req, res, m) => {
  const s = st.spaces.find((x) => x.principal_id === m[1]);
  if (s) Object.assign(s, JSON.parse(await body(req)));
  json(res, 200, s);
});

on('GET', '/api/config', (_q, res, _m, q) => json(res, 200, { ...st.effective, schema: q.get('schema') ? schema : undefined }));
on('PATCH', '/api/config', async (req, res) => {
  const { set } = JSON.parse(await body(req));
  const bad = Object.entries(set as Record<string, unknown>)
    .filter(([p, v]) => (p === 'budgets.daily_usd' && typeof v === 'number' && v < 0) || p.startsWith('server.listen'))
    .map(([p]) => ({ path: p, source: { layer: 'ui' }, message: p.startsWith('server.listen') ? `bothy.ui.yaml: ${p}: locked; set it in bothy.yaml or the environment` : `bothy.ui.yaml: ${p}: must be 0 or more` }));
  if (bad.length) return json(res, 400, { error: { code: 'bad_request', message: bad.map((b) => b.message).join('; ') }, fields: bad });
  for (const [p, v] of Object.entries(set as Record<string, unknown>)) setConfig(p, v, `user:${st.me.id}`);
  const restart = Object.keys(set).filter((p) => p.startsWith('sandbox.backend'));
  st.effective.restart_required = [...new Set([...st.effective.restart_required, ...restart])];
  json(res, 200, { restart_required: restart });
});
on('DELETE', '/api/config/{path...}', (_q, res, m) => {
  const e = st.effective.entries.find((x) => x.path === decodeURIComponent(m[1]));
  if (e?.source.layer === 'ui' && e.shadowed?.length) {
    const [next, ...rest] = e.shadowed;
    e.value = next.value;
    e.source = next.source;
    e.shadowed = rest;
  }
  json(res, 204);
});
on('GET', '/api/vault/status', (_q, res) => json(res, 200, fx.vaultStatus));
on('POST', '/api/vault/import', async (req, res) => {
  const b = JSON.parse(await body(req));
  const s = { scope: 'global', name: b.name, source: 'vault', hint: 'aB3d', updated_at: nowISO(), last_used_at: fx.ZERO };
  st.secrets.push(s);
  json(res, 200, s);
});
on('GET', '/api/vault', (_q, res) => json(res, 200, st.secrets));
on('PUT', '/api/vault/{scope}/{name}', async (req, res, m) => {
  const { value } = JSON.parse(await body(req));
  const s = { scope: decodeURIComponent(m[1]), name: m[2], source: 'vault', hint: String(value).slice(-4), updated_at: nowISO(), last_used_at: fx.ZERO };
  st.secrets = [...st.secrets.filter((x) => !(x.name === s.name && x.scope === s.scope)), s];
  json(res, 200, s);
});
on('DELETE', '/api/vault/{scope}/{name}', (_q, res, m) => {
  st.secrets = st.secrets.filter((x) => !(x.name === m[2] && x.scope === decodeURIComponent(m[1])));
  json(res, 204);
});
on('POST', '/api/vault/unseal', (_q, res) => json(res, 200, fx.vaultStatus));

on('GET', '/api/usage', (_q, res, _m, q) => {
  const days = Math.max(1, Math.round((Date.parse(q.get('to') ?? '') - Date.parse(q.get('from') ?? '')) / 86400_000) + 1) || 7;
  const rows = fx.usageRows(q.get('group_by') ?? 'day', days);
  const totals = rows.reduce((t, r) => ({ key: '', cost_micros: t.cost_micros + r.cost_micros, input: t.input + r.input, output: t.output + r.output, cache_read: t.cache_read + r.cache_read, cache_write: t.cache_write + r.cache_write, calls: t.calls + r.calls, price_known: t.price_known && r.price_known }), { key: '', cost_micros: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, calls: 0, price_known: true });
  json(res, 200, { rows, totals });
});
on('GET', '/api/usage/today', (_q, res) => json(res, 200, { spent_micros: 420_000, limit_micros: 3_000_000, global_spent_micros: 710_000, global_limit_micros: 3_000_000 }));
on('GET', '/api/audit', (_q, res, _m, q) => {
  const a = q.get('action') ?? '';
  json(res, 200, { items: fx.audit.filter((e) => e.action.startsWith(a)) });
});
on('GET', '/api/audit/verify', (_q, res) => json(res, 200, { ok: true }));
on('GET', '/api/egress', (_q, res) => json(res, 200, { items: fx.egress }));
on('GET', '/api/sandbox', (_q, res) => json(res, 200, fx.sandbox));
on('GET', '/api/tokens', (_q, res) => json(res, 200, st.tokens));
on('POST', '/api/tokens', async (req, res) => {
  const b = JSON.parse(await body(req));
  const meta = { id: newId('tok'), user_id: st.me.id, name: b.name, scopes: b.scopes, on_approval: b.on_approval, rate_per_min: b.rate_per_min || 30, created_at: nowISO(), last_used_at: fx.ZERO, expires_at: new Date(Date.now() + b.ttl / 1e6).toISOString(), revoked_at: fx.ZERO };
  st.tokens.push(meta);
  json(res, 200, { token: `bothy_pat_${meta.id.slice(4).toUpperCase()}_mockSecretValueOnlyShownOnce1234567890abcdEFGH_AbC123`, meta });
});
on('DELETE', '/api/tokens/{id}', (_q, res, m) => {
  const t = st.tokens.find((x) => x.id === m[1]);
  if (t) t.revoked_at = nowISO();
  json(res, 204);
});
on('GET', '/api/users', (_q, res) => json(res, 200, st.users));
on('POST', '/api/users', async (req, res) => {
  const b = JSON.parse(await body(req));
  const u = { ...clone(fx.member), id: newId('usr'), username: b.username, display_name: b.display_name, role: b.role };
  st.users.push(u);
  json(res, 200, u);
});
on('PATCH', '/api/users/{id}', async (req, res, m) => {
  const u = st.users.find((x) => x.id === m[1]);
  if (u) Object.assign(u, JSON.parse(await body(req)));
  json(res, 200, u);
});
on('DELETE', '/api/users/{id}', (_q, res, m) => {
  const u = st.users.find((x) => x.id === m[1]);
  if (u) u.disabled = true;
  json(res, 204);
});
on('POST', '/api/admin/pause', async (req, res) => {
  st.paused = JSON.parse(await body(req)).paused;
  const b: BannerData = st.paused ? { level: 'error', code: 'paused', text: 'Bothy is paused. Every run was stopped; only admins can resume.' } : { level: 'error', code: 'paused', text: '', clear: true };
  st.banners = st.paused ? [...st.banners, b] : st.banners.filter((x) => x.code !== 'paused');
  emit('banner', b);
  json(res, 204);
});
on('POST', '/api/admin/restart', (_q, res) => json(res, 202));
on('POST', '/api/admin/backup', (_q, res) => json(res, 200, { path: '/data/backups/bothy-2026-10-12T1830.tar' }));
on('GET', '/api/doctor', (_q, res) => json(res, 200, fx.doctor));
on('POST', '/api/security-audit', async (req, res) => {
  const { fix } = JSON.parse(await body(req));
  json(res, 200, { ...fx.doctor, fixed: fix ? ['tokens.expired_unrevoked'] : undefined });
});

let latency = 80;

async function dispatch(req: Req, res: Res, next: () => void) {
  const url = new URL(req.url ?? '/', 'http://mock');
  if (!url.pathname.startsWith('/api/')) return next();
  const route = routes.find(([m, re]) => m === req.method && re.test(url.pathname));
  if (!route) return fail(res, 404, 'not_found', `mock: no route for ${req.method} ${url.pathname}`);
  if (!open.has(url.pathname) && !st.loggedIn) return fail(res, 401, 'unauthorized', 'Log in first.');
  if (req.method !== 'GET' && !open.has(url.pathname) && req.headers['x-csrf-token'] !== 'mock-csrf') return fail(res, 403, 'forbidden', 'missing or invalid X-CSRF-Token', 'GET /api/auth/me returns the token');
  if (latency) await sleep(latency);
  try {
    await route[2](req, res, url.pathname.match(route[1])!, url.searchParams);
  } catch (e) {
    fail(res, 500, 'internal', `mock: ${String(e)}`);
  }
}

// In-process entry point for unit tests: no latency, no SSE.
export async function handle(method: string, url: string, body = '', headers: Record<string, string> = {}): Promise<{ status: number; headers: Record<string, string>; body: string }> {
  latency = 0;
  const out = { status: 200, headers: {} as Record<string, string>, body: '' };
  const listeners: Record<string, ((c?: { toString(): string }) => void)[]> = {};
  const req: Req = { method, url, headers: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v])), on: (ev, fn) => void (listeners[ev] ??= []).push(fn) };
  let done!: () => void;
  const finished = new Promise<void>((r) => (done = r));
  const res: Res = {
    get statusCode() {
      return out.status;
    },
    set statusCode(v: number) {
      out.status = v;
    },
    setHeader: (k, v) => void (out.headers[k] = v),
    write: (s) => void (out.body += s),
    end: (s) => {
      out.body += s ?? '';
      done();
    },
    on: () => {},
  };
  const run = dispatch(req, res, () => {
    out.status = 404;
    done();
  });
  queueMicrotask(() => {
    if (body) listeners.data?.forEach((fn) => fn({ toString: () => body }));
    listeners.end?.forEach((fn) => fn());
  });
  await Promise.race([finished, run.then(() => finished)]);
  return out;
}

// SPEC §8.1 security headers, applied by `vite preview` so the production
// build is exercised under the real CSP (the dev server injects inline styles).
const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";

export function mockGateway(): Plugin {
  // node:http types are not installed; the structural Req/Res cover what the routes use.
  const mount = (use: (fn: (req: unknown, res: unknown, next: () => void) => void) => void, headers: boolean) => {
    use((req, res, next) => {
      if (headers) {
        const r = res as Res;
        r.setHeader('Content-Security-Policy', CSP);
        r.setHeader('X-Content-Type-Options', 'nosniff');
        r.setHeader('Referrer-Policy', 'no-referrer');
      }
      void dispatch(req as Req, res as Res, next);
    });
  };
  // Both hooks must return nothing: Vite runs a returned function as a post hook.
  return {
    name: 'bothy-mock-gateway',
    configureServer(server) {
      mount((fn) => void server.middlewares.use(fn), false);
    },
    configurePreviewServer(server) {
      mount((fn) => void server.middlewares.use(fn), true);
    },
  };
}
