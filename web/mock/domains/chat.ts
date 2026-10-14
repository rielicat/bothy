// Chat (C3): sessions, messages, pins, uploads, search, and the scripted run
// that streams a reply (with an approval card when a message asks to run,
// send or delete something).
import type { Approval, Card, Message, Session } from '../../src/lib/api/types.gen.ts';
import { state as approvalState, waiting } from './approvals.ts';
import { ago, body, clone, core, emit, fail, ids, isPaused, json, newId, nowISO, on, onReset, owner, scenario, sleep, type Req } from './core.ts';

const ses = (id: string, title: string, origin: string, minAgo: number, extra: Partial<Session> = {}): Session => ({
  id,
  principal_id: owner.id,
  origin,
  channel_key: origin === 'web' || origin === 'schedule' ? '' : `${origin}:dm:123`,
  title,
  model_slot: 'main',
  summary_upto: 0,
  epoch: 0,
  taint: 0,
  hidden: false,
  archived: false,
  created_at: ago(minAgo + 30),
  updated_at: ago(minAgo),
  last_message_at: ago(minAgo),
  ...extra,
});

export const sessions: Session[] = [
  ses(ids.trip, 'Weekend in Edinburgh', 'web', 12, { taint: 2 | 4 }),
  ses('ses_01jinbox00000000000000000', 'Summarize the newsletter', 'telegram', 95, { taint: 1 | 2 | 4 }),
  ses(ids.brief, 'Morning briefing · Mon 12 Oct', 'schedule', 60 * 9),
  ses('ses_01jbudget0000000000000000', 'Household budget spreadsheet', 'web', 60 * 26, { taint: 1 }),
  ses('ses_01jslack00000000000000000', 'Release notes draft', 'slack', 60 * 50),
  ses('ses_01jrecipe0000000000000000', 'Dinner ideas without dairy', 'discord', 60 * 24 * 9),
  ses('ses_01jfamily0000000000000000', 'Family group', 'telegram', 40, { principal_id: 'spc_01jfamily0000000000000000', taint: 2 }),
];

let msgId = 1000;
const msg = (session: string, seq: number, role: string, parts: Message['parts'], extra: Partial<Message> = {}): Message => ({
  id: ++msgId,
  session_id: session,
  seq,
  role,
  kind: 'normal',
  parts,
  provenance: role === 'user' ? 'user' : role === 'tool' ? 'tool' : 'system',
  sender: role === 'user' ? `web:${owner.id}` : undefined,
  tokens: 40,
  created_at: ago(30 - seq),
  ...extra,
});

const T = ids.trip;
const RUN1 = ids.tripRun;

export const messages: Record<string, Message[]> = {
  [T]: [
    msg(T, 1, 'user', [{ t: 'text', text: 'Plan a relaxed weekend in Edinburgh for two. We like food, walking and old bookshops. Budget around £400.' }]),
    msg(
      T,
      2,
      'assistant',
      [
        { t: 'text', text: "I'll check the weather and a few current recommendations first." },
        { t: 'tool_call', call: { id: 'call_1', name: 'web_search', args: { query: 'Edinburgh weekend weather forecast' } } },
        { t: 'tool_call', call: { id: 'call_2', name: 'web_fetch', args: { url: 'https://www.edinburghbookshops.example/guide' } } },
      ],
      { run_id: RUN1 },
    ),
    msg(
      T,
      3,
      'tool',
      [
        { t: 'tool_result', result: { call_id: 'call_1', name: 'web_search', content: 'Sat: 14°C, light showers in the morning. Sun: 16°C, sunny spells.', untrusted: true } },
        { t: 'tool_result', result: { call_id: 'call_2', name: 'web_fetch', content: 'A guide to independent bookshops in the Old Town and Stockbridge…', untrusted: true } },
      ],
      { run_id: RUN1, provenance: 'untrusted' },
    ),
    msg(
      T,
      4,
      'assistant',
      [
        {
          t: 'text',
          text: [
            "Here's a gentle plan. Saturday starts damp, so the indoor stops come first.",
            '',
            '## Saturday',
            '1. **Breakfast** at a café on Broughton Street',
            '2. Browse the bookshops on *West Port* (three within five minutes)',
            '3. Lunch at the Grassmarket, then the National Museum while it rains',
            '4. Sunset walk up **Calton Hill**',
            '',
            '## Sunday',
            '- Water of Leith walk to Stockbridge market',
            '- Afternoon tea, then the Dean Village',
            '',
            '| Item | Estimate |',
            '| --- | ---: |',
            '| Hotel, 1 night | £180 |',
            '| Food | £150 |',
            '| Museums and extras | £40 |',
            '| **Total** | **£370** |',
            '',
            '> Tip: book Saturday dinner ahead; places fill up by Thursday.',
            '',
            'The bookshop guide is at https://www.edinburghbookshops.example/guide and a map link is `maps/edinburgh.html` in your workspace.',
          ].join('\n'),
        },
      ],
      { run_id: RUN1, tokens: 420 },
    ),
  ],
  ses_01jinbox00000000000000000: [
    msg('ses_01jinbox00000000000000000', 1, 'user', [{ t: 'text', text: 'Summarize the newsletter I forwarded and save the dates to my memory.' }], { sender: 'telegram:5512' }),
    msg('ses_01jinbox00000000000000000', 2, 'notice', [{ t: 'text', text: "I'll keep following: 'don't email anyone without asking'. /pins to manage." }], { role: 'assistant', kind: 'notice' }),
    msg('ses_01jinbox00000000000000000', 3, 'assistant', [
      {
        t: 'text',
        text: 'The newsletter lists three events. It also asks readers to "confirm your subscription" at [this link](https://evil.example/confirm?u=sam) — I would not click it.\n\n- Oct 18: school fair\n- Oct 24: half term starts\n- Nov 2: dentist reminders',
      },
    ]),
  ],
};

export const pins: Record<string, { id: number; session_id: string; text: string; source: string; created_at: string }[]> = {
  ses_01jinbox00000000000000000: [{ id: 1, session_id: 'ses_01jinbox00000000000000000', text: "Don't email anyone without asking", source: 'auto', created_at: ago(90) }],
};

export const state = { sessions: [] as Session[], messages: {} as Record<string, Message[]>, pins: {} as typeof pins };
onReset(() => Object.assign(state, { sessions: clone(sessions), messages: clone(messages), pins: clone(pins) }));

const noChats = scenario('chat-empty', 'No chats yet: the chat list and search are empty.', 'chat');

const stopped = new Set<string>();

function addMessage(sid: string, m: Omit<Message, 'id' | 'seq' | 'session_id' | 'created_at' | 'tokens'>): Message {
  const list = (state.messages[sid] ??= []);
  const full: Message = { ...m, id: Math.floor(Math.random() * 1e9), seq: (list.at(-1)?.seq ?? 0) + 1, session_id: sid, tokens: 50, created_at: nowISO() };
  list.push(full);
  const s = state.sessions.find((x) => x.id === sid);
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

export const breakdown = { constitution: 1650, persona: 240, memory: 180, skills: 120, pins: 0, tools: 1380, summary: 0, history: 2400, tool_results: 900, turn_context: 160, total: 7030, window: 200000, reserve: 8192, cacheable: 3270 };

async function runScenario(sid: string, text: string) {
  const run = newId('run');
  const s = state.sessions.find((x) => x.id === sid);
  emit('run.started', { trigger: 'message', model: 'anthropic/claude-sonnet-4-6' }, sid, run);
  await sleep(250);
  emit('run.status', { status: 'thinking' }, sid, run);
  await sleep(500);
  const risky = /shell|script|run |approve|delete|email|send/i.test(text);
  const intro = risky ? 'I can do that. I need to run a short script in the sandbox first.' : 'Let me check a couple of sources.';
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
      principal_id: owner.id,
      tool_call_id: tcl,
      action: action as Approval['action'],
      decision: { verdict: 'ask', rule: 'default:local_write', reason: 'Writes files in your workspace', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 900e9, on_timeout: 'deny', choices: card.choices },
      card,
      status: 'pending',
      approver: { user_id: owner.id, admin_only: false },
      on_timeout: 'deny',
      created_at: nowISO(),
      expires_at: card.expires,
    };
    approvalState.approvals.push(a);
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
  emit(
    'run.usage',
    {
      call: { id: newId('llm'), ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, slot: 'main', purpose: 'agent', step: 2, attempts: [], usage: used, usage_estimated: false, cost_micros: 6900, price_known: true, ttft: 600e6, latency: 2100e6, breakdown: { ...breakdown, history: breakdown.history + 900, total: breakdown.total + 900 }, prefix_hash: 'a1b2', stop: 'end_turn' },
      run_cost_micros: 11200,
      day_cost_micros: 431_200,
      day_limit_micros: 3_000_000,
    },
    sid,
    run,
  );
  emit('run.finished', { status: 'done', steps: 2, usage: used, cost_micros: 11200 }, sid, run);
  if (s && !s.title) {
    s.title = text.length > 36 ? text.slice(0, 36) + '…' : text;
    emit('session.updated', { title: s.title, taint: s.taint, epoch: s.epoch }, sid);
  }
}

const session = (id: string) => state.sessions.find((s) => s.id === id);
const visible = (req: Req) => (noChats(req) ? [] : state.sessions);

on('GET', '/api/sessions', (req, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const items = visible(req)
    .filter((s) => !s.archived && (!term || s.title.toLowerCase().includes(term)))
    .sort((a, b) => b.last_message_at.localeCompare(a.last_message_at));
  json(res, 200, { items });
});
on('POST', '/api/sessions', (_q, res) => {
  const s: Session = { ...clone(sessions[0]), id: newId('ses'), title: '', taint: 0, created_at: nowISO(), updated_at: nowISO(), last_message_at: nowISO() };
  state.sessions.unshift(s);
  state.messages[s.id] = [];
  json(res, 200, s);
});
on('GET', '/api/sessions/{id}', (_q, res, m) => {
  const s = session(m[1]);
  if (!s) return fail(res, 404, 'not_found', 'No such chat.');
  json(res, 200, { session: s, pins: state.pins[s.id] ?? [], pending_approvals: approvalState.approvals.filter((a) => a.session_id === s.id && (a.status === 'pending' || a.status === 'parked')) });
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
  const all = state.messages[m[1]] ?? [];
  const before = Number(q.get('before') ?? 0);
  const limit = Number(q.get('limit') ?? 50);
  const list = before ? all.filter((x) => x.seq < before) : all;
  json(res, 200, { messages: list.slice(-limit), has_more: list.length > limit });
});
on('POST', '/api/sessions/{id}/messages', async (req, res, m) => {
  if (isPaused(req)) return fail(res, 503, 'paused', 'Bothy is paused by an admin.');
  const b = JSON.parse(await body(req));
  const parts: Message['parts'] = [{ t: 'text', text: b.text }];
  for (const u of b.uploads ?? []) parts.push({ t: 'file', blob: { mime: 'application/octet-stream', name: `${u}.txt`, ref: `.bothy/in/${u}/file.txt`, size: 1234 } });
  addMessage(m[1], { role: 'user', kind: 'normal', provenance: 'user', sender: `web:${core.me.id}`, parts });
  void runScenario(m[1], b.text);
  json(res, 202, { run_id: 'run_pending', queued: false, steered: false });
});
on('POST', '/api/sessions/{id}/stop', (_q, res, m) => {
  const last = [...(state.messages[m[1]] ?? [])].reverse().find((x) => x.run_id)?.run_id;
  if (last) stopped.add(last);
  for (const [id, resolve] of waiting) {
    const a = approvalState.approvals.find((x) => x.id === id);
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
on('GET', '/api/sessions/{id}/context', (_q, res) => json(res, 200, breakdown));
on('POST', '/api/sessions/{id}/fork', (_q, res, m) => {
  const old = session(m[1]);
  const s: Session = { ...clone(old ?? sessions[0]), id: newId('ses'), title: `${old?.title ?? 'Chat'} (clean)`, taint: 0, created_at: nowISO(), last_message_at: nowISO() };
  state.sessions.unshift(s);
  state.messages[s.id] = [{ id: 1, session_id: s.id, seq: 1, role: 'assistant', kind: 'notice', parts: [{ t: 'text', text: `Clean fork of “${old?.title}”. Earlier history is not in context.` }], provenance: 'system', tokens: 10, created_at: nowISO() }];
  json(res, 200, s);
});
on('GET', '/api/sessions/{id}/pins', (_q, res, m) => json(res, 200, state.pins[m[1]] ?? []));
on('POST', '/api/sessions/{id}/pins', async (req, res, m) => {
  const b = JSON.parse(await body(req));
  const p = { id: Math.floor(Math.random() * 1e6), session_id: m[1], text: b.text, source: 'user', created_at: nowISO() };
  (state.pins[m[1]] ??= []).push(p);
  json(res, 200, p);
});
on('DELETE', '/api/sessions/{id}/pins/{pin}', (_q, res, m) => {
  state.pins[m[1]] = (state.pins[m[1]] ?? []).filter((p) => String(p.id) !== m[2]);
  json(res, 204);
});
on('POST', '/api/uploads', async (req, res) => {
  const raw = await body(req);
  const name = /filename="([^"]*)"/.exec(raw)?.[1] ?? 'file';
  json(res, 200, { id: newId('upl'), name, mime: 'application/octet-stream', size: raw.length });
});
on('POST', '/api/runs/{id}/continue', (_q, res) => json(res, 202, { queued: false, steered: false }));
on('GET', '/api/search', (req, res, _m, q) => {
  const term = (q.get('q') ?? '').toLowerCase();
  const hits = [];
  for (const [sid, list] of Object.entries(noChats(req) ? {} : state.messages))
    for (const m of list)
      for (const p of m.parts) {
        const i = p.text?.toLowerCase().indexOf(term) ?? -1;
        if (p.text && i >= 0)
          hits.push({ session_id: sid, title: session(sid)?.title ?? '', seq: m.seq, snippet: `…${p.text.slice(Math.max(0, i - 30), i)}[${p.text.slice(i, i + term.length)}]${p.text.slice(i + term.length, i + term.length + 40)}…`, at: m.created_at, rank: 1, trusted: m.provenance !== 'untrusted' });
      }
  json(res, 200, hits.slice(0, 20));
});
