// Activity (C6): tasks (runs), schedules and the check-in, releases, spending
// and the audit log. Rows carry the names and plain lines the server tracks
// add (S8, S16, docs/design/ui.md §17) where the UI shows them; a few rows
// leave them out so the client-side fallbacks show too.
import type { Approval, AuditEntry, EgressRecord, Run, Schedule, ToolCallRecord } from '../../src/lib/api/types.gen.ts';
import { state as approvalState } from './approvals.ts';
import { breakdown } from './chat.ts';
import { releaseEnvironments } from './connections.ts';
import { ago, asMember, body, clone, fail, ids, json, member, newId, nowISO, on, onReset, owner, routes, scenario, viewer, type Req, type Res } from './core.ts';

const limits = { max_steps: 0, max_tokens: 0, max_time: 0, max_cost_micros: 0 };
const telegramDM = { channel: 'telegram', chat_id: '5512', kind: 'dm' };

// 08:00 UTC is 9:00 in London until the clocks change.
const dayAt = (days: number, hourUTC: number) => {
  const d = new Date(Date.now() + days * 86400_000);
  d.setUTCHours(hourUTC, 0, 0, 0);
  return d.toISOString();
};

const sched = (id: string, name: string, kind: string, spec: string, extra: Partial<Schedule> = {}): Schedule => ({
  id,
  principal_id: owner.id,
  name,
  kind,
  spec,
  tz: 'Europe/London',
  prompt: '',
  slot: 'fast',
  limits,
  missed_run: 'once',
  enabled: true,
  created_by: `user:${owner.id}`,
  failures: 0,
  next_run_at: dayAt(1, 8),
  last_run_at: ago(60 * 4),
  last_status: 'done',
  created_at: ago(60 * 24 * 30),
  updated_at: ago(60 * 24 * 2),
  ...extra,
});

export const schedules: Schedule[] = [
  sched('sch_01jcheckin00000000000000', 'Check-in', 'heartbeat', '30m', { active_hours: '08:00-22:00', enabled: false, created_by: 'system', last_run_at: ago(60 * 24 * 3) }),
  sched('sch_01jstandup00000000000000', 'Daily standup summary', 'cron', '0 9 * * 1-5', {
    prompt: "Summarise yesterday's merged pull requests and open issues in acme/web, then post them for the standup.",
    deliver: telegramDM,
  }),
  sched('sch_01jprdigest0000000000000', 'Weekly PR digest', 'cron', '0 8 * * 1', {
    next_run_at: dayAt(((8 - new Date().getUTCDay()) % 7) || 7, 7),
    prompt: 'List the pull requests that waited more than two days for review last week, with links.',
    deliver: { channel: 'web', chat_id: '', kind: 'dm' },
    last_status: 'failed',
    failures: 1,
  }),
  sched(ids.briefSchedule, 'Morning briefing', 'cron', '0 8 * * *', {
    prompt: 'Summarise overnight alerts from status.example.com and anything that changed in the release checklist.',
    enabled: false,
    last_status: 'skipped',
  }),
  sched('sch_01jinvoices0000000000000', 'Invoice reminder', 'cron', '0 9 1,15 * *', { prompt: 'List the client invoices that are still unpaid.', slot: 'main', next_run_at: dayAt(new Date().getUTCDate() < 15 ? 15 - new Date().getUTCDate() : 3, 8) }),
];

type TaskRun = Run & { requested_by_name?: string; chat_title?: string; schedule_name?: string; origin?: string; reason?: string; refined?: boolean };

const task = (id: string, minAgo: number, extra: Partial<TaskRun>): TaskRun => ({
  id,
  session_id: `ses_${id.slice(4)}`,
  principal_id: owner.id,
  user_id: owner.id,
  trigger: 'message',
  status: 'done',
  steps: 3,
  tool_calls: 2,
  usage: { input: 3200, output: 410, cache_read: 2100, cache_write: 0, reasoning: 0 },
  cost_micros: 30_000,
  models: ['anthropic/claude-sonnet-4-6'],
  limits,
  created_at: ago(minAgo),
  started_at: ago(minAgo),
  finished_at: ago(minAgo - 2),
  origin: 'web',
  ...extra,
});

const D = 60 * 24;
export const runs: TaskRun[] = [
  task('run_01jreview00000000000000', 2, { chat_title: 'Review the onboarding doc', status: 'running', steps: 1, cost_micros: 0, finished_at: '' }),
  task('run_01jfixlogin0000000000000', 25, { chat_title: 'Fix login redirect', steps: 6, tool_calls: 6, cost_micros: 120_000 }),
  task('run_01jstandup10000000000000', 90, { trigger: 'schedule', schedule_id: 'sch_01jstandup00000000000000', schedule_name: 'Daily standup summary', origin: 'schedule', cost_micros: 30_000 }),
  task('run_01jdeployweb000000000000', 110, {
    chat_title: 'Deploy acme/web to production',
    user_id: member.id,
    principal_id: member.id,
    requested_by_name: 'Alex',
    origin: 'slack',
    status: 'waiting_approval',
    steps: 2,
    cost_micros: 0,
    finished_at: '',
  }),
  task('run_01jprdigest1000000000000', 180, {
    trigger: 'schedule',
    schedule_id: 'sch_01jprdigest0000000000000',
    schedule_name: 'Weekly PR digest',
    origin: 'schedule',
    status: 'failed',
    reason: 'the GitHub token was rejected',
    cost_micros: 80_000,
  }),
  task(ids.tripRun, D + 60, { chat_title: undefined, session_id: ids.trip, cost_micros: 18_400, origin: undefined }),
  task('run_01jpricing00000000000000', D + 120, { chat_title: 'Q3 pricing summary', refined: true, steps: 4, cost_micros: 420_000 }),
  task('run_01jnotes0000000000000000', D + 200, { chat_title: 'Draft the release notes', user_id: member.id, principal_id: member.id, requested_by_name: 'Alex', origin: 'telegram', status: 'stopped', cost_micros: 50_000 }),
  task('run_01jissues000000000000000', 3 * D, { chat_title: 'Summarise all open issues', origin: 'discord', status: 'budget_exceeded', steps: 9, cost_micros: 500_000 }),
  task('run_01jstaging00000000000000', 3 * D + 30, { chat_title: 'Check staging health', user_id: member.id, principal_id: member.id, requested_by_name: 'Alex', trigger: 'api', origin: 'api', status: 'interrupted', cost_micros: 9_000 }),
  task('run_01jbrief1000000000000000', 4 * D, { trigger: 'schedule', schedule_id: ids.briefSchedule, schedule_name: 'Morning briefing', origin: 'schedule', status: 'skipped', steps: 0, cost_micros: 0 }),
  task('run_01jrename00000000000000', 5 * D, { chat_title: 'Rename the test files', status: 'loop_detected', steps: 12, cost_micros: 210_000 }),
  ...[1, 2, 3].map((d) => task(`run_01jcheckin${d}00000000000`, 3 * D + d * 30, { trigger: 'heartbeat', schedule_id: 'sch_01jcheckin00000000000000', schedule_name: 'Check-in', origin: 'schedule', cost_micros: 2_000 + d * 300 })),
];
// The heartbeat runs belong to the check-in's history, not the main list.
const listed = (r: TaskRun) => r.trigger !== 'heartbeat';
export const scheduleRuns = runs.filter((r) => r.schedule_id);

const call = (run: string, step: number, tool: string, action: Partial<ToolCallRecord['action']> & { payload: unknown }, extra: Partial<ToolCallRecord> = {}): ToolCallRecord => ({
  id: `tcl_01j${run.slice(7, 12)}${step}000000000000000`.slice(0, 30),
  run_id: run,
  session_id: `ses_${run.slice(4)}`,
  principal_id: owner.id,
  step,
  call_id: `call_${step}`,
  tool,
  source: 'builtin',
  side_effect: 'read',
  action: { tool, kind: 'other', side_effect: 'read', fingerprint: `f${step}`, ...action } as ToolCallRecord['action'],
  verdict: 'auto',
  rule: 'default:read',
  status: 'ok',
  result_preview: '',
  result_bytes: 0,
  untrusted: false,
  duration: 900e6 + step * 100e6,
  created_at: ago(30 - step),
  finished_at: ago(30 - step),
  ...extra,
});

const llm = (step: number, extra: Record<string, unknown> = {}) => ({
  id: `llm_01j${step}00000000000000000000`,
  ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' },
  slot: 'main',
  purpose: 'agent',
  step,
  attempts: [{ ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, latency: 2600e6 }],
  usage: { input: 1200 * step, output: 180 * step, cache_read: 3270, cache_write: step === 1 ? 3270 : 0, reasoning: 0 },
  usage_estimated: false,
  cost_micros: 6200 * step,
  price_known: true,
  ttft: 640e6,
  latency: 2400e6,
  breakdown: { ...breakdown, history: breakdown.history * step, total: breakdown.total + (step - 1) * 3300 },
  prefix_hash: 'a1b2c3d4e5f60718',
  stop: 'end_turn',
  ...extra,
});

const prApproval = (run: string): Approval => ({
  id: 'apr_01jprlogin000000000000000',
  run_id: run,
  session_id: `ses_${run.slice(4)}`,
  principal_id: owner.id,
  tool_call_id: 'call_6',
  action: { tool: 'repo_open_pr', kind: 'other', side_effect: 'external_send', payload: { repo: 'acme/web', head: 'bothy/fix-login-redirect', base: 'main' }, fingerprint: 'fpr' },
  decision: { verdict: 'ask', rule: 'default:external_send', reason: 'Opening a pull request sends changes to GitHub.', floor: false, approver: { user_id: owner.id, admin_only: false }, timeout: 15 * 60e9, on_timeout: 'deny', choices: ['allow_once', 'deny'] },
  card: { title: 'Open a pull request in acme/web?', risk: 'external_send', fields: [], context: '', choices: ['allow_once', 'deny'], expires: ago(5), web_url: '' },
  status: 'allowed',
  choice: 'allow_once',
  approver: { user_id: owner.id, admin_only: false },
  on_timeout: 'deny',
  decided_by: member.id,
  decided_via: 'web',
  created_at: ago(27),
  expires_at: ago(12),
  decided_at: ago(26),
});

const diff = '--- a/src/auth/redirect.ts\n+++ b/src/auth/redirect.ts\n@@ -10,6 +10,9 @@\n export function returnTo(req) {\n-  return "/";\n+  const next = req.query.next;\n+  if (next && next.startsWith("/") && !next.startsWith("//")) return next;\n+  return "/";\n }\n';

export function runDetail(id: string) {
  const run = runs.find((r) => r.id === id) ?? { ...runs[1], id };
  if (id === 'run_01jfixlogin0000000000000') {
    return {
      run,
      llm_calls: [llm(1), llm(2), llm(3, { attempts: [{ ref: { provider: 'anthropic', model: 'claude-sonnet-4-6' }, err: '529 overloaded', kind: 'overloaded', latency: 420e6 }, { ref: { provider: 'anthropic', model: 'claude-haiku-4-5' }, latency: 1900e6 }] })],
      tool_calls: [
        call(id, 1, 'repo_checkout', { payload: { repo: 'acme/web', ref: 'main' } }),
        call(id, 2, 'fs_read', { kind: 'fs.read', paths: ['acme/web/src/auth/redirect.ts'], payload: { path: 'acme/web/src/auth/redirect.ts' } }, { result_preview: 'export function returnTo(req) {\n  return "/";\n}' }),
        call(id, 3, 'shell', { kind: 'exec', argv: ['npm', 'test'], payload: { command: 'npm test' } }, { result_preview: 'Tests  48 passed (48)' }),
        call(id, 4, 'fs_write', { kind: 'fs.write', paths: ['src/auth/redirect.ts'], diff, payload: { path: 'src/auth/redirect.ts' } }, { side_effect: 'local_write', rule: 'default:local_write' }),
        call(id, 6, 'repo_open_pr', { host: 'api.github.com', payload: { repo: 'acme/web', number: 142 } }, { side_effect: 'external_send', verdict: 'ask', rule: 'default:external_send', approval_id: 'apr_01jprlogin000000000000000', result_preview: 'Opened #142' }),
      ],
      approvals: [prApproval(id)],
      answer:
        'The return address is dropped in the login form: `returnTo` always sends people to `/`, so anyone who signs in from a deep link lands on the home page.\n\nI changed it to keep `next` when it is a path on this site, added a test for links that try to leave the site, and opened pull request #142 in acme/web.\n\nThe tests pass. Two other places build their own redirects (`/invite` and `/billing`); they have the same problem and are worth a follow-up.',
    };
  }
  if (id === 'run_01jpricing00000000000000') {
    const entry = (draft: number, round: number, role: string, text: string) => ({ draft, round, role, text, cost_micros: 40_000 });
    return {
      run,
      llm_calls: [llm(1), llm(2)],
      tool_calls: [call(id, 1, 'knowledge_search', { payload: { query: 'Q3 pricing' } })],
      approvals: [],
      refine: {
        drafts: 3,
        depth: 2,
        rounds: 2,
        early_stop: false,
        winner: 1,
        votes: [{ answer: 'raise team plan to $24', count: 2, drafts: [0, 1] }, { answer: 'keep $20', count: 1, drafts: [2] }],
        entries: [entry(0, 0, 'draft', 'Raise the team plan to $24 …'), entry(1, 0, 'draft', 'Raise the team plan to $24, keep …'), entry(2, 0, 'draft', 'Keep the team plan at $20 …'), entry(1, 1, 'critique', 'Draft 3 ignores the churn numbers …'), entry(1, 2, 'correct', 'Raise the team plan to $24 from January …')],
        calls: 7,
        cost_micros: 280_000,
      },
      answer: 'Raise the team plan from $20 to $24 a seat from January. Churn on the team plan stayed under 2% through Q3, and the two competitors closest to us charge $25 and $29.',
    };
  }
  if (id === 'run_01jdeployweb000000000000') {
    return {
      run,
      llm_calls: [llm(1)],
      tool_calls: [
        call(id, 1, 'release_status', { payload: { environment: 'production' } }),
        call(id, 2, 'release_deploy', { payload: { repo: 'acme/web', environment: 'production', ref: 'main' } }, { status: 'waiting', verdict: 'ask', side_effect: 'external_send', rule: 'release:production' }),
      ],
      approvals: [],
    };
  }
  if (id === 'run_01jprdigest1000000000000') {
    return { run, llm_calls: [llm(1)], tool_calls: [call(id, 1, 'github_runs', { payload: { repo: 'acme/web' } }, { status: 'error', result_preview: 'GitHub answered 401: Bad credentials' })], approvals: [] };
  }
  if (id === ids.tripRun) {
    const search = call(id, 1, 'web_search', { kind: 'search', payload: { query: 'Edinburgh weekend weather forecast' } }, { untrusted: true, result_preview: 'Sat: 14°C, light showers in the morning. Sun: 16°C, sunny spells.', session_id: ids.trip });
    const fetch = call(id, 2, 'web_fetch', { kind: 'http', method: 'GET', url: 'https://www.edinburghbookshops.example/guide', host: 'www.edinburghbookshops.example', payload: { url: 'https://www.edinburghbookshops.example/guide' } }, { untrusted: true, rule: 'grant:grt_01jbooks000000000000000000', session_id: ids.trip });
    return { run: { ...run, session_id: ids.trip }, llm_calls: [llm(1), llm(2)], tool_calls: [search, fetch], approvals: [] };
  }
  return { run, llm_calls: [llm(1)], tool_calls: [], approvals: [] };
}

type AuditRow = AuditEntry & { actor_name?: string; summary?: string };
const entry = (id: number, minAgo: number, actor: string, action: string, extra: Partial<AuditRow> = {}): AuditRow => ({ id, at: ago(minAgo), actor, action, target: '', outcome: 'ok', detail: {}, ...extra });
const sam = `user:${owner.id}`;
const alex = `user:${member.id}`;
export const audit: AuditRow[] = [
  entry(512, 18, sam, 'approval.decide', { summary: 'Approved deleting notes/staging.md', detail: { choice: 'allow_once' }, session_id: ids.trip, run_id: ids.tripRun }),
  entry(511, 19, 'agent:run_01jfixlogin0000000000000', 'approval.request', { summary: 'Asked to delete notes/staging.md', run_id: 'run_01jfixlogin0000000000000' }),
  entry(510, 48, sam, 'auth.login', { ip: '192.0.2.10' }),
  entry(509, 90, 'agent:run_01jstandup10000000000000', 'tool.exec', { target: 'github_issues', summary: 'Ran “Daily standup summary”', actor_name: 'Schedule', run_id: 'run_01jstandup10000000000000' }),
  entry(508, 93, 'agent:run_01jstandup10000000000000', 'egress.deny', { target: 'paste.example.net', outcome: 'denied', run_id: 'run_01jstandup10000000000000' }),
  entry(507, 140, alex, 'config.set', { target: 'budgets.daily_usd', detail: { path: 'budgets.daily_usd', old: 3, new: 5 } }),
  entry(506, D + 30, alex, 'auth.login', { ip: '192.0.2.44' }),
  entry(505, D + 60, 'agent:run_01jtrip1000000000000000', 'tool.exec', { target: 'web_search', session_id: ids.trip, run_id: ids.tripRun }),
  entry(504, D + 70, sam, 'token.create', { target: 'Open WebUI' }),
  entry(503, D + 90, 'system', 'secret.use', { target: 'anthropic_api_key', detail: { purpose: 'provider' } }),
  entry(502, 2 * D, sam, 'schedule.create', { target: 'sch_01jstandup00000000000000' }),
  entry(501, 2 * D + 5, 'agent:run_01jissues000000000000000', 'budget.exceeded', { outcome: 'error', run_id: 'run_01jissues000000000000000' }),
];

type EgressRow = EgressRecord & { outcome?: string };
export const egress: EgressRow[] = [
  { id: 93, at: ago(20), dialer: 'public', principal_id: owner.id, run_id: 'run_01jfixlogin0000000000000', dest: '', host: 'api.github.com', port: 443, decision: 'allow', bytes_up: 820, bytes_down: 48211, duration: 900e6, outcome: 'reached' },
  { id: 92, at: ago(60), dialer: 'public', principal_id: owner.id, run_id: 'run_01jstandup10000000000000', dest: '', host: 'api.example.com', port: 443, decision: 'allow', bytes_up: 300, bytes_down: 0, duration: 10e9 },
  { id: 91, at: ago(93), dialer: 'box', principal_id: owner.id, run_id: 'run_01jstandup10000000000000', dest: '', host: 'paste.example.net', port: 443, decision: 'deny', reason: 'not on the allowlist', bytes_up: 0, bytes_down: 0, duration: 0, outcome: 'blocked' },
  { id: 90, at: ago(D + 61), dialer: 'public', session_id: ids.trip, principal_id: owner.id, run_id: ids.tripRun, dest: '', host: 'www.edinburghbookshops.example', port: 443, ip: '93.184.216.34', decision: 'allow', bytes_up: 820, bytes_down: 48211, duration: 900e6 },
];

const now = Date.now();

// Day amounts in dollars for the last 30 days, newest last; the highest is $4.10.
const daily = [0.9, 1.1, 0.4, 0.6, 2.2, 1.7, 1.4, 0.8, 0, 0, 1.6, 2.4, 1.9, 1.2, 0.7, 4.1, 2.8, 1.5, 0.9, 0, 0.3, 1.8, 2.1, 1.6, 1.3, 0.5, 0, 1.9, 2.5, 1.2];

export function usageRows(groupBy: string, days: number, user?: string, today = daily[daily.length - 1]) {
  const scale = days / 30;
  const row = (key: string, dollars: number, extra: Record<string, unknown> = {}) => ({
    key,
    cost_micros: Math.round(dollars * 1e6),
    input: Math.round(42000 * dollars),
    output: Math.round(6100 * dollars),
    cache_read: Math.round(30500 * dollars),
    cache_write: Math.round(4200 * dollars),
    calls: Math.max(1, Math.round(18 * dollars)),
    price_known: true,
    ...extra,
  });
  const share = user === member.id ? 0.3 : 1;
  if (groupBy === 'day') {
    return [...daily.slice(30 - days, -1), today].map((v, i) => row(new Date(now - (days - 1 - i) * 86400_000).toISOString().slice(0, 10), v * share));
  }
  if (groupBy === 'model') {
    return [
      row('anthropic/claude-sonnet-4-6', 35 * scale * share),
      row('anthropic/claude-haiku-4-5', 5.1 * scale * share),
      row('ollama/qwen3:14b', 0, { calls: 22 }),
      row('openai/gpt-5-mini', 2 * scale * share, { price_known: false }),
    ];
  }
  if (groupBy === 'session') {
    const chats: [string, string, number][] = [
      ['ses_01jfixlogin0000000000000', 'Fix login redirect', 0.42],
      ['ses_01jpricing00000000000000', 'Q3 pricing summary', 0.42],
      ['ses_01jissues000000000000000', 'Summarise all open issues', 0.5],
      ['ses_01jrename00000000000000', 'Rename the test files', 0.21],
      ['ses_01jnotes0000000000000000', 'Draft the release notes', 0.05],
      [ids.trip, '', 0.02],
      ['ses_01jstaging00000000000000', 'Check staging health', 0.01],
    ];
    return chats.map(([k, name, v]) => row(k, v * share, name ? { name } : {})).sort((a, b) => b.cost_micros - a.cost_micros);
  }
  if (groupBy === 'schedule') return user === member.id ? [] : [row('sch_01jstandup00000000000000', 0.9 * scale), row('sch_01jprdigest0000000000000', 0.2 * scale)];
  if (groupBy === 'user') return [row(owner.id, 18.2 * scale, { name: 'Sam' }), row(member.id, 12.05 * scale, { name: 'Alex' })];
  return [];
}

const deploy = (id: string, env: string, sha: string, subject: string, status: string, minAgo: number, extra: Record<string, unknown> = {}) => ({
  id,
  environment: env,
  repo: 'acme/web',
  workflow: 'deploy.yml',
  ref: 'main',
  principal_id: owner.id,
  session_id: `ses_${id.slice(4)}`,
  requested_by: owner.id,
  approved_by: [member.id],
  status,
  sha,
  subject,
  created_at: ago(minAgo),
  updated_at: ago(minAgo),
  finished_at: status === 'running' ? '0001-01-01T00:00:00Z' : ago(minAgo - 3),
  ...extra,
});
export const deploys = [
  deploy('dep_01jrunning0000000000000000', 'staging', '9e1b3c0a1', 'Add audit export', 'running', 2, { run_url: 'https://github.com/acme/web/actions/runs/912', approved_by_names: ['Alex'] }),
  deploy('dep_01jprod00000000000000000', 'production', '4f2c1a9d2', 'Fix login redirect loop', 'succeeded', 2 * D, { run_id: 'run_01jfixlogin0000000000000', approved_by: [member.id, 'usr_01jmaya0000000000000000'], approved_by_names: ['Alex', 'Maya Chen'] }),
  deploy('dep_01jfailed00000000000000', 'staging', '1b2c3d4e5', 'Upgrade the router', 'failed', 8 * D, { requested_by: member.id, requested_by_name: 'Alex', approved_by: [owner.id], run_url: 'https://github.com/acme/web/actions/runs/877' }),
];

// Deploy approvals live in the approvals domain; this scenario adds a waiting
// two-approver deploy and a denied one for Activity without changing its seed.
const deployApprovals = (): Approval[] => {
  const base = (id: string, run: string, status: string, minAgo: number, extra: Partial<Approval> = {}) =>
    ({
      ...clone(approvalState.recent[0] ?? approvalState.approvals[0]),
      id,
      run_id: run,
      session_id: `ses_${run.slice(4)}`,
      tool_call_id: 'call_2',
      action: { tool: 'release_deploy', kind: 'other', side_effect: 'external_send', payload: { repo: 'acme/web', environment: 'production', ref: 'main' }, fingerprint: 'fdep' },
      status,
      approver: { user_id: '', admin_only: false, roles: ['admin'], required: 2, requester: member.id },
      created_at: ago(minAgo),
      ...extra,
    }) as Approval;
  return [
    base('apr_01jdeployweb000000000000', 'run_01jdeployweb000000000000', 'pending', 110, { votes: [{ approval_id: 'apr_01jdeployweb000000000000', user_id: owner.id, choice: 'allow_once', via: 'web', at: ago(100) }] } as Partial<Approval>),
    base('apr_01jdenied00000000000000', 'run_01jdenied00000000000000', 'denied', 3 * D, { decided_by: owner.id, comment: 'Not before the freeze ends', requested_by_name: 'Alex' } as Partial<Approval>),
  ];
};

export const state = { schedules: [] as Schedule[] };
onReset(() => Object.assign(state, { schedules: clone(schedules) }));

const empty = scenario('activity-empty', 'No tasks, schedules, spending or audit entries yet.', 'activity');
const spendLimit = scenario('spend-limit', "Today's spending reached the daily limit.", 'activity');
const spend50 = scenario('spend-50', "Today's spending is half the daily limit.", 'activity');
const spend80 = scenario('spend-80', "Today's spending passed 80% of the daily limit.", 'activity');
const noReleases = scenario('releases-none', 'No deploy environments, so the Releases tab is hidden.', 'activity');
const noDeploys = scenario('releases-empty', 'Deploy environments exist but nothing was deployed yet.', 'activity');
const deployWaiting = scenario('deploy-waiting', 'A two-approver deploy waits for one more approval; an earlier one was denied.', 'activity');
const auditBroken = scenario('audit-broken', "The audit log's hash chain breaks at one entry.", 'activity');

const mine = (req: Req) => (asMember(req) ? member.id : undefined);

const listApprovals = routes.find(([m, re]) => m === 'GET' && re.test('/api/approvals'));
routes.unshift([
  'GET',
  /^\/api\/approvals$/,
  (req, res, m, q) => {
    if (!deployWaiting(req) || !listApprovals) return listApprovals?.[2](req, res, m, q);
    const status = q.get('status') ?? 'pending';
    const seed = status === 'recent' ? approvalState.recent : approvalState.approvals.filter((a) => a.status === status);
    json(res, 200, { items: [...seed, ...deployApprovals().filter((a) => (status === 'recent' ? a.status !== 'pending' : a.status === status))] });
  },
]);

on('GET', '/api/runs', (req, res, _m, q) => {
  if (empty(req)) return json(res, 200, { items: [] });
  const who = mine(req);
  const before = Number(q.get('before') ?? 0);
  const limit = Number(q.get('limit') ?? 50);
  const items = runs
    .filter(listed)
    .filter((r) => (!who || r.user_id === who) && (!q.get('session') || r.session_id === q.get('session')) && (!q.get('schedule') || r.schedule_id === q.get('schedule')) && (!q.get('status') || r.status === q.get('status')))
    .filter((r) => !before || Date.parse(r.created_at) < before);
  const page = items.slice(0, limit);
  json(res, 200, { items: page, next: items.length > limit ? Date.parse(page[page.length - 1].created_at) : 0 });
});
// Any id answers, like the v0.1 mock (the chat's live runs ask for theirs), except a "missing" one.
on('GET', '/api/runs/{id}', (_q, res, m) => (m[1].includes('missing') ? fail(res, 404, 'not_found', 'No such run.') : json(res, 200, runDetail(m[1]))));

const human = (b: { kind: string; spec: string; tz: string }) => {
  const known: Record<string, string> = { '0 9 1,15 * *': 'On the 1st and 15th of each month at 09:00' };
  if (b.kind === 'heartbeat') return `Every ${b.spec} (${b.tz})`;
  if (b.kind === 'once') return `Once on ${b.spec.replace('T', ' at ')} (${b.tz})`;
  return `${known[b.spec] ?? 'On a custom repeat rule'} (${b.tz})`;
};
// The next five matches of a cron spec, read in UTC; a heartbeat or once spec gets daily stand-ins.
function nextRuns(kind: string, spec: string): string[] {
  const field = (f: string, v: number) =>
    f.split(',').some((part) => {
      const [range, step = '1'] = part.split('/');
      const [lo, hi] = range === '*' ? [0, 99] : range.split('-').map(Number);
      return v >= lo && v <= (hi ?? lo) && (v - (range === '*' ? 0 : lo)) % Number(step) === 0;
    });
  const f = spec.trim().split(/\s+/);
  const out: string[] = [];
  if (kind !== 'cron' || f.length !== 5) return [1, 2, 3, 4, 5].map((d) => new Date(Date.now() + d * 86400_000).toISOString());
  for (let t = Math.ceil(Date.now() / 60_000) * 60_000; out.length < 5 && t < Date.now() + 62 * 86400_000; t += 60_000) {
    const d = new Date(t);
    if (field(f[0], d.getUTCMinutes()) && field(f[1], d.getUTCHours()) && field(f[2], d.getUTCDate()) && field(f[3], d.getUTCMonth() + 1) && field(f[4], d.getUTCDay())) out.push(d.toISOString());
  }
  return out;
}

on('GET', '/api/schedules', (req, res) => json(res, 200, empty(req) || asMember(req) ? [] : state.schedules));
on('POST', '/api/schedules/preview', async (req, res) => {
  const b = JSON.parse(await body(req));
  if (b.kind === 'cron' && b.spec.trim().split(/\s+/).length !== 5) return fail(res, 400, 'bad_request', 'A cron expression has five fields: minute hour day month weekday.');
  json(res, 200, { human: human(b), next: nextRuns(b.kind, b.spec) });
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
on('POST', '/api/schedules/{id}/run', (_q, res) => json(res, 202, { run_id: 'run_01jstandup10000000000000' }));
on('GET', '/api/schedules/{id}/runs', (_q, res, m) => json(res, 200, { items: runs.filter((r) => r.schedule_id === m[1]) }));

// Environments come from config (the connections domain) so edits in Settings show here; the approvals domain adds the deploys its approved cards started.
on('GET', '/api/releases', (req, res) => {
  if (noReleases(req)) return json(res, 200, { environments: [], deploys: [] });
  json(res, 200, { environments: releaseEnvironments(req), deploys: noDeploys(req) || empty(req) ? [] : [...deploys, ...approvalState.deploys] });
});

// Today's bar on the chart matches the meter in every spending scenario.
const spentToday = (req: Req) => (empty(req) ? 0 : spendLimit(req) ? 5_000_000 : spend80(req) ? 4_100_000 : spend50(req) ? 2_500_000 : 1_200_000);

on('GET', '/api/usage', (req, res, _m, q) => {
  const days = Math.min(30, Math.max(1, Math.round((Date.parse(q.get('to') ?? '') - Date.parse(q.get('from') ?? '')) / 86400_000) + 1) || 7);
  const today = spentToday(req) / 1e6;
  const rows = empty(req) ? [] : usageRows(q.get('group_by') ?? 'day', days, mine(req), today);
  const totals = usageRows('day', days, mine(req), today).reduce(
    (t, r) => ({ ...t, cost_micros: t.cost_micros + r.cost_micros, input: t.input + r.input, output: t.output + r.output, cache_read: t.cache_read + r.cache_read, cache_write: t.cache_write + r.cache_write, calls: t.calls + r.calls }),
    { key: '', cost_micros: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, calls: 0, price_known: true },
  );
  json(res, 200, { rows, totals: empty(req) ? { ...totals, cost_micros: 0 } : totals });
});
on('GET', '/api/usage/today', (req, res) => {
  const limit = 5_000_000;
  const spent = spentToday(req);
  json(res, 200, { spent_micros: asMember(req) ? Math.round(spent * 0.3) : spent, limit_micros: asMember(req) ? 2_000_000 : 0, global_spent_micros: spent, global_limit_micros: limit });
});
on('GET', '/api/audit', (req, res, _m, q) => {
  if (viewer(req).role === 'member') return fail(res, 403, 'forbidden', 'Admins only.');
  const actor = q.get('actor') ?? '';
  const from = Date.parse(q.get('from') ?? '') || 0;
  json(res, 200, { items: empty(req) ? [] : audit.filter((e) => (!actor || e.actor === actor) && Date.parse(e.at) >= from) });
});
on('GET', '/api/audit/verify', (req, res) => json(res, 200, auditBroken(req) ? { ok: false, bad_id: 507, bad_at: audit.find((e) => e.id === 507)?.at } : { ok: true, checked: 1284 }));
on('GET', '/api/egress', (req, res) => json(res, 200, { items: empty(req) ? [] : egress }));
