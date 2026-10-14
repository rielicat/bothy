// Activity (C6): tasks (runs), schedules, spending and the audit log.
import type { AuditEntry, EgressRecord, Run, Schedule, ToolCallRecord } from '../../src/lib/api/types.gen.ts';
import { breakdown, sessions } from './chat.ts';
import { ago, ahead, body, clone, fail, ids, json, newId, nowISO, on, onReset, owner, scenario, type Req, type Res } from './core.ts';

const limits = { max_steps: 0, max_tokens: 0, max_time: 0, max_cost_micros: 0 };

export const schedules: Schedule[] = [
  {
    id: ids.briefSchedule,
    principal_id: owner.id,
    name: 'Morning briefing',
    kind: 'cron',
    spec: '0 8 * * 1-5',
    tz: 'Europe/London',
    prompt: 'Check the weather and my calendar, then send me a short plan for the day.',
    slot: 'fast',
    limits,
    missed_run: 'skip',
    enabled: true,
    created_by: `user:${owner.id}`,
    failures: 0,
    next_run_at: ahead(60 * 14),
    last_run_at: ago(60 * 9),
    last_status: 'parked',
    created_at: ago(60 * 24 * 10),
    updated_at: ago(60 * 24 * 2),
  },
  {
    id: 'sch_01jweekly0000000000000000',
    principal_id: owner.id,
    name: 'Weekly money check',
    kind: 'cron',
    spec: '0 9 * * 1',
    tz: 'Europe/London',
    prompt: 'Look at budget.xlsx in my workspace and tell me if anything is off compared with last week.',
    slot: 'main',
    limits,
    missed_run: 'once',
    enabled: false,
    created_by: `user:${owner.id}`,
    failures: 0,
    next_run_at: ahead(60 * 24 * 3),
    last_run_at: ago(60 * 24 * 4),
    last_status: 'done',
    created_at: ago(60 * 24 * 20),
    updated_at: ago(60 * 24 * 4),
  },
];

export const scheduleRuns: Run[] = [1, 2, 3].map((d) => ({
  id: `run_01jbrief${d}000000000000000`,
  session_id: ids.brief,
  principal_id: owner.id,
  user_id: owner.id,
  trigger: 'schedule',
  schedule_id: ids.briefSchedule,
  status: d === 1 ? 'parked' : 'done',
  steps: 3,
  tool_calls: 2,
  usage: { input: 3200, output: 410, cache_read: 2100, cache_write: 0, reasoning: 0 },
  cost_micros: 2100 + d * 300,
  models: ['anthropic/claude-haiku-4-5'],
  limits,
  created_at: ago(60 * 24 * d),
  started_at: ago(60 * 24 * d),
  finished_at: ago(60 * 24 * d - 1),
}));

export const runDetail = (id: string) => {
  const tool: ToolCallRecord = {
    id: 'tcl_01jsearch0000000000000000',
    run_id: id,
    session_id: ids.trip,
    principal_id: owner.id,
    step: 1,
    call_id: 'call_1',
    tool: 'web_search',
    source: 'builtin',
    side_effect: 'read',
    action: { tool: 'web_search', kind: 'search', side_effect: 'read', payload: { query: 'Edinburgh weekend weather forecast' }, fingerprint: 'f1' },
    verdict: 'auto',
    rule: 'default:read',
    status: 'ok',
    result_preview: 'Sat: 14°C, light showers in the morning. Sun: 16°C, sunny spells.',
    result_bytes: 1830,
    untrusted: true,
    duration: 840e6,
    created_at: ago(20),
    finished_at: ago(20),
  };
  const fetch: ToolCallRecord = {
    ...tool,
    id: 'tcl_01jfetch10000000000000000',
    call_id: 'call_2',
    tool: 'web_fetch',
    action: { tool: 'web_fetch', kind: 'http', side_effect: 'read', method: 'GET', url: 'https://www.edinburghbookshops.example/guide', host: 'www.edinburghbookshops.example', payload: { url: 'https://www.edinburghbookshops.example/guide' }, fingerprint: 'f2' },
    rule: 'grant:grt_01jbooks000000000000000000',
    duration: 1310e6,
  };
  return {
    run: { ...scheduleRuns[1], id, session_id: ids.trip, trigger: 'message', schedule_id: undefined, status: 'done', models: ['anthropic/claude-sonnet-4-6'], cost_micros: 18400 },
    llm_calls: [1, 2].map((step) => ({
      id: `llm_01j${step}00000000000000000000`,
      ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' },
      slot: 'main',
      purpose: 'agent',
      step,
      attempts:
        step === 1
          ? [
              { ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, err: '529 overloaded', kind: 'overloaded', latency: 420e6 },
              { ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, latency: 1900e6 },
            ]
          : [{ ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, latency: 2600e6 }],
      usage: { input: 1200 * step, output: 180 * step, cache_read: 3270, cache_write: step === 1 ? 3270 : 0, reasoning: 0 },
      usage_estimated: false,
      cost_micros: 6200 * step,
      price_known: true,
      ttft: 640e6,
      latency: 2400e6,
      breakdown: { ...breakdown, history: breakdown.history * step, total: breakdown.total + (step - 1) * 3300 },
      prefix_hash: 'a1b2c3d4e5f60718',
      stop: step === 1 ? 'tool_use' : 'end_turn',
    })),
    tool_calls: [tool, fetch],
    approvals: [],
  };
};

export const audit: AuditEntry[] = Array.from({ length: 24 }, (_, i) => {
  const actions = ['auth.login', 'config.set', 'tool.exec', 'approval.decide', 'egress.allow', 'memory.write', 'secret.use', 'token.create'];
  const action = actions[i % actions.length];
  return {
    id: 500 - i,
    at: ago(i * 37),
    actor: action === 'tool.exec' || action === 'memory.write' ? `agent:${ids.tripRun}` : `user:${owner.id}`,
    action,
    target: action === 'config.set' ? 'policy.preset' : action === 'tool.exec' ? 'web_search' : action === 'secret.use' ? 'anthropic_api_key' : '',
    outcome: i % 9 === 4 ? 'denied' : 'ok',
    session_id: action === 'tool.exec' ? ids.trip : undefined,
    run_id: action === 'tool.exec' ? ids.tripRun : undefined,
    ip: action === 'auth.login' ? '127.0.0.1' : undefined,
    detail: action === 'config.set' ? { path: 'policy.preset', old: 'careful', new: 'balanced' } : { purpose: 'provider' },
  };
});

export const egress: EgressRecord[] = [
  { id: 90, at: ago(20), dialer: 'public', session_id: ids.trip, principal_id: owner.id, run_id: ids.tripRun, dest: '', host: 'www.edinburghbookshops.example', port: 443, ip: '93.184.216.34', decision: 'allow', bytes_up: 820, bytes_down: 48211, duration: 900e6 },
  { id: 89, at: ago(21), dialer: 'box', session_id: ids.trip, principal_id: owner.id, run_id: ids.tripRun, dest: '', host: 'pypi.org', port: 443, decision: 'deny', reason: 'not on the allowlist', bytes_up: 0, bytes_down: 0, duration: 0 },
  { id: 88, at: ago(25), dialer: 'system', dest: 'provider:anthropic', host: 'api.anthropic.com', port: 443, decision: 'allow', bytes_up: 21000, bytes_down: 3400, duration: 2400e6 },
];

const now = Date.now();

export function usageRows(groupBy: string, days: number) {
  const out = [];
  if (groupBy === 'day') {
    for (let i = days - 1; i >= 0; i--) {
      // UTC dates, like the gateway's day keys
      const key = new Date(now - i * 86400_000).toISOString().slice(0, 10);
      const cost = Math.round(120_000 + 90_000 * Math.sin(i * 1.7) ** 2 + (i % 6 === 0 ? 260_000 : 0));
      out.push({ key, cost_micros: cost, input: 42000, output: 6100, cache_read: 30500, cache_write: 4200, calls: 18 + (i % 5), price_known: i !== 3 });
    }
  } else if (groupBy === 'model') {
    out.push({ key: 'anthropic/claude-sonnet-4-6', cost_micros: 2_310_000 * (days / 7), input: 310000, output: 42000, cache_read: 220000, cache_write: 30000, calls: 102, price_known: true });
    out.push({ key: 'anthropic/claude-haiku-4-5', cost_micros: 380_000 * (days / 7), input: 120000, output: 21000, cache_read: 60000, cache_write: 9000, calls: 61, price_known: true });
    out.push({ key: 'ollama/qwen3:14b', cost_micros: 0, input: 90000, output: 12000, cache_read: 0, cache_write: 0, calls: 22, price_known: true });
  } else if (groupBy === 'session') {
    for (const s of sessions.slice(0, 4)) out.push({ key: s.id, cost_micros: 300_000 + s.title.length * 9000, input: 40000, output: 5000, cache_read: 20000, cache_write: 2000, calls: 12, price_known: true });
  } else if (groupBy === 'schedule') {
    out.push({ key: ids.briefSchedule, cost_micros: 41_000, input: 12000, output: 1500, cache_read: 9000, cache_write: 0, calls: 9, price_known: true });
  } else if (groupBy === 'user') {
    out.push({ key: 'sam', cost_micros: 2_400_000, input: 400000, output: 60000, cache_read: 250000, cache_write: 30000, calls: 160, price_known: true });
    out.push({ key: 'alex', cost_micros: 290_000, input: 60000, output: 9000, cache_read: 30000, cache_write: 9000, calls: 25, price_known: true });
  }
  return out;
}

export const state = { schedules: [] as Schedule[] };
onReset(() => Object.assign(state, { schedules: clone(schedules) }));

const empty = scenario('activity-empty', 'No schedules, spending or audit entries yet.', 'activity');
const spendLimit = scenario('spend-limit', "Today's spending reached the daily limit.", 'activity');

on('GET', '/api/runs/{id}', (_q, res, m) => json(res, 200, runDetail(m[1])));

on('GET', '/api/schedules', (req, res) => json(res, 200, empty(req) ? [] : state.schedules));
on('POST', '/api/schedules/preview', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (b.kind === 'cron' && b.spec.trim().split(/\s+/).length !== 5) return fail(res, 400, 'bad_request', 'A cron expression has five fields: minute hour day month weekday.');
  const human = b.kind === 'heartbeat' ? `Every ${b.spec} (${b.tz})` : b.kind === 'once' ? `Once on ${b.spec} (${b.tz})` : b.spec === '0 8 * * 1-5' ? `Weekdays at 08:00 (${b.tz})` : b.spec === '0 9 * * 1' ? `Mondays at 09:00 (${b.tz})` : `Cron ${b.spec} (${b.tz})`;
  json(res, 200, { human, next: [1, 2, 3, 4, 5].map((d) => new Date(Date.now() + d * 86400_000).toISOString()) });
});
on('GET', '/api/schedules/{id}', (_q, res, m) => {
  const s = state.schedules.find((x) => x.id === m[1]);
  return s ? json(res, 200, s) : fail(res, 404, 'not_found', 'No such schedule.');
});
const upsert = async (req: Req, res: Res, id?: string) => {
  const b = JSON.parse(await body(req));
  const s: Schedule = { ...b.schedule, id: id ?? newId('sch'), next_run_at: new Date(Date.now() + 86400_000).toISOString(), updated_at: nowISO() };
  state.schedules = [...state.schedules.filter((x) => x.id !== s.id), s];
  json(res, 200, s);
};
on('POST', '/api/schedules', (req, res) => upsert(req, res));
on('PATCH', '/api/schedules/{id}', (req, res, m) => upsert(req, res, m[1]));
on('DELETE', '/api/schedules/{id}', (_q, res, m) => {
  state.schedules = state.schedules.filter((x) => x.id !== m[1]);
  json(res, 204);
});
on('POST', '/api/schedules/{id}/run', (_q, res) => json(res, 202, { run_id: ids.briefRun }));
on('GET', '/api/schedules/{id}/runs', (_q, res) => json(res, 200, { items: scheduleRuns }));

on('GET', '/api/usage', (req, res, _m, q) => {
  const days = Math.max(1, Math.round((Date.parse(q.get('to') ?? '') - Date.parse(q.get('from') ?? '')) / 86400_000) + 1) || 7;
  const rows = empty(req) ? [] : usageRows(q.get('group_by') ?? 'day', days);
  const totals = rows.reduce(
    (t, r) => ({ key: '', cost_micros: t.cost_micros + r.cost_micros, input: t.input + r.input, output: t.output + r.output, cache_read: t.cache_read + r.cache_read, cache_write: t.cache_write + r.cache_write, calls: t.calls + r.calls, price_known: t.price_known && r.price_known }),
    { key: '', cost_micros: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, calls: 0, price_known: true },
  );
  json(res, 200, { rows, totals });
});
on('GET', '/api/usage/today', (req, res) => {
  if (spendLimit(req)) return json(res, 200, { spent_micros: 3_000_000, limit_micros: 3_000_000, global_spent_micros: 5_000_000, global_limit_micros: 5_000_000 });
  json(res, 200, { spent_micros: empty(req) ? 0 : 420_000, limit_micros: 3_000_000, global_spent_micros: empty(req) ? 0 : 710_000, global_limit_micros: 3_000_000 });
});
on('GET', '/api/audit', (req, res, _m, q) => {
  const a = q.get('action') ?? '';
  json(res, 200, { items: empty(req) ? [] : audit.filter((e) => e.action.startsWith(a)) });
});
on('GET', '/api/audit/verify', (_q, res) => json(res, 200, { ok: true }));
on('GET', '/api/egress', (req, res) => json(res, 200, { items: empty(req) ? [] : egress }));
